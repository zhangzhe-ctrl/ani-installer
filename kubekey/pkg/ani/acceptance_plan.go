package ani

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
)

// ---------------------------------------------------------------------------
// C04: one component's acceptance may legitimately have to rebuild more than one
// business object — the metrics stack has Prometheus and Alertmanager, and a
// Fluent Bit durability run touches the logging backend as well as a collector.
// The level used to model acceptance as exactly one target per component, so
// those checks could only run as a shell script that deleted pods by label with
// no ledger, no precondition and no quota behind them.
//
// This file adds the smallest structure that expresses "an ordered set of fixed
// checks, some of which change a declared object" without becoming a workflow
// engine: a step is data plus a function, a plan is an ordered slice of steps,
// and every step that changes something must name a target from the one registry
// below. There is no dynamic graph, no external task language and no state store
// — the durable state is still the single acceptance ledger file per
// (base run, target), written by claimAcceptanceLedger before the change.
// ---------------------------------------------------------------------------

// VerifyStepResult is one step's outcome. Steps are reported because a component
// that "passed" while running one of its three checks has not passed the check
// set, and an operator needs to see which step the claim rests on.
type VerifyStepResult struct {
	ID       string            `json:"id"`
	Title    string            `json:"title"`
	Status   string            `json:"status"`
	Detail   string            `json:"detail,omitempty"`
	Evidence map[string]string `json:"evidence,omitempty"`
	// Target is the registry key of the business object a mutating step changed,
	// and QuotaState is what the durable ledger said about it. Both are printed so
	// a dependency change is visible from the report alone.
	Target     string `json:"target,omitempty"`
	QuotaState string `json:"quotaState,omitempty"`
}

// acceptanceStep is one phase of one component's acceptance plan.
type acceptanceStep struct {
	ID    string
	Title string
	// Target names the registry entry this step changes. Empty means the step
	// changes no business object: a data read, or work confined to this attempt's
	// own temporary resources.
	Target string
	// Run fills in what it learned on the result it is handed (a quota state, an
	// identity) and returns an error to fail. Failing a step stops every later
	// change in the plan.
	Run func(a *acceptanceAttempt, result *VerifyStepResult) error
}

// acceptancePlan is the fixed, ordered check set a component declares.
type acceptancePlan struct {
	Steps []acceptanceStep
	// Dependencies lists the business objects this component's acceptance changes
	// on behalf of another component. They are declared so the report can name
	// them: a run scoped with --only must not be able to hide a change to a
	// shared object by leaving the owning component out of the scope.
	Dependencies []string
}

// acceptanceAttempt is the one context every step of one component's plan shares.
// The attempt identity and the token are made once here, so a later phase reads
// back what an earlier phase wrote instead of writing something new and reading
// that back as proof of persistence.
type acceptanceAttempt struct {
	ctx       context.Context
	runner    kubectlRunner
	deleter   *podDeleter
	component string
	stateDir  string
	runID     string
	registry  string
	attempt   string
	outputDir string
	scriptDir string
	evidence  map[string]string
	// wroteToken records that this attempt's data write genuinely happened, so a
	// read-back cannot be satisfied by a fresh write.
	wroteToken bool
}

// attemptID is the attempt label every temporary object of this run carries.
// Repeating an acceptance makes a NEW attempt, which is what stops a retry from
// deleting the leftovers of the run it is retrying.
func attemptID() string {
	return fmt.Sprintf("%d-%s", time.Now().UTC().Unix(), acceptanceToken())
}

// acceptanceStepBudget bounds one step. Every step's network calls run under it,
// and a step that outlives its budget fails rather than drifting into the next.
func acceptanceStepBudget() time.Duration {
	budget := 20 * time.Minute
	if v := strings.TrimSpace(os.Getenv("ANI_ACCEPTANCE_STEP_TIMEOUT")); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil && parsed > 0 {
			budget = parsed
		}
	}
	return budget
}

// runStep executes one step under its own deadline, derived from the run's
// context so a cancellation stops the whole plan and cannot be ignored by a step
// that is mid-flight.
func (a *acceptanceAttempt) runStep(step acceptanceStep) VerifyStepResult {
	result := VerifyStepResult{ID: step.ID, Title: step.Title}
	// Steps report what they observed even when they fail, because a refusal
	// without the identities it was computed from cannot be checked by anyone.
	result.Evidence = map[string]string{}
	if step.Target != "" {
		if _, ok := acceptanceTargets[step.Target]; !ok {
			result.Status = VerifyStatusFailed
			result.Detail = fmt.Sprintf("step %s names target %q, which no acceptance plan declares", step.ID, step.Target)
			return result
		}
		result.Target = step.Target
	}
	if err := a.ctx.Err(); err != nil {
		result.Status = VerifyStatusNotRun
		result.Detail = fmt.Sprintf("the run was cancelled before this step started: %v", err)
		return result
	}
	ctx, cancel := context.WithTimeout(a.ctx, acceptanceStepBudget())
	defer cancel()
	owned := *a
	owned.ctx = ctx
	err := step.Run(&owned, &result)
	if err != nil {
		result.Status = VerifyStatusFailed
		if strings.TrimSpace(result.Detail) == "" {
			result.Detail = err.Error()
		}
		return result
	}
	// A step that reached its own deadline is a failure, never a success that
	// happened to return late.
	if ctx.Err() != nil && a.ctx.Err() == nil {
		result.Status = VerifyStatusFailed
		result.Detail = fmt.Sprintf("the step exceeded its %s budget: %v (%s)", acceptanceStepBudget(), ctx.Err(), result.Detail)
		return result
	}
	result.Status = VerifyStatusPass
	return result
}

// planOverall folds a component's step results into one status. All required
// steps must genuinely pass: an empty set, a skip, a not_run or an unknown is not
// a pass, which is the same rule C04 applied to the level as a whole.
func planOverall(component string, steps []VerifyStepResult) VerifyComponentResult {
	if len(steps) == 0 {
		return VerifyComponentResult{
			Component: component,
			Status:    VerifyStatusFailed,
			Detail:    fmt.Sprintf("acceptance ran no steps for %s, which is not a verification", component),
		}
	}
	status := VerifyStatusPass
	var notes []string
	firstFailure := ""
	merged := map[string]string{}
	for _, step := range steps {
		for key, value := range step.Evidence {
			merged[key] = value
		}
		if step.Status != VerifyStatusPass {
			status = VerifyStatusFailed
			notes = append(notes, fmt.Sprintf("%s=%s", step.ID, step.Status))
			if firstFailure == "" {
				firstFailure = fmt.Sprintf("%s: %s", step.ID, step.Detail)
			}
		}
	}
	// The component line carries the reason, not only the tally: whoever reads
	// `verify acceptance <component>: fail` must see why without opening the
	// report, and the full per-step detail stays in Steps.
	detail := fmt.Sprintf("%d step(s): %s", len(steps), strings.Join(stepSummaries(steps), ", "))
	if firstFailure != "" {
		detail = firstFailure + " | " + detail
	}
	if len(notes) > 0 {
		merged["failedSteps"] = strings.Join(notes, " ")
	}
	return VerifyComponentResult{
		Component: component,
		Status:    status,
		Detail:    detail,
		Steps:     steps,
		Evidence:  merged,
	}
}

func stepSummaries(steps []VerifyStepResult) []string {
	out := make([]string, 0, len(steps))
	for _, step := range steps {
		out = append(out, fmt.Sprintf("%s:%s", step.ID, step.Status))
	}
	return out
}

// peekAcceptanceLedger reads an existing quota record WITHOUT creating one. A
// plan checks every target it is about to change before it changes any of them,
// so a run that cannot finish what it declares does not spend part of the budget
// and leave the rest untouched.
func peekAcceptanceLedger(stateDir, runID string, target acceptanceTarget) (acceptanceLedger, bool, error) {
	path := target.ledgerFile(stateDir, runID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return acceptanceLedger{}, false, nil
		}
		return acceptanceLedger{}, false, errors.Wrapf(err, "read the acceptance ledger %s", path)
	}
	var rec acceptanceLedger
	if err := json.Unmarshal(data, &rec); err != nil {
		// An unreadable record is treated as unknown, exactly as a corrupt record
		// is at claim time: a delete is never re-issued on a guess.
		return acceptanceLedger{State: ledgerStateUnknown}, true, nil
	}
	return rec, true, nil
}

// authorizeTarget reads the LIVE facts that make a declared object the object
// this plan is allowed to change. Nothing here trusts a name: the pod's owner
// reference must point at the declared controller's own current uid, and the
// storage the target declares must be the storage the pod really uses.
//
// It returns the pod uid to authorise and the stable identity whose uid must
// survive the change (the PVC for a volume-backed workload; the managing
// DaemonSet for a hostPath collector, which has no PVC and is never given a
// fabricated one).
func authorizeTarget(ctx context.Context, runner kubectlRunner, t acceptanceTarget) (podUID, stableUID, controllerUID string, evidence map[string]string, err error) {
	evidence = map[string]string{
		"target":     t.LedgerToken,
		"controller": t.ControllerKind + "/" + t.ControllerName,
		"namespace":  t.Namespace,
	}
	podName := t.PodName
	if podName == "" {
		// A DaemonSet pod is identified by the node it runs on, which is the
		// instance the durability claim is about.
		if t.NodeName == "" {
			return "", "", "", evidence, errors.New("a target with no pod name must declare the node whose daemon pod it means")
		}
		evidence["node"] = t.NodeName
		selector := t.PodSelector
		if selector == "" {
			return "", "", "", evidence, fmt.Errorf("target %s has no pod selector for its daemon pods", t.LedgerToken)
		}
		out, rerr := runner.run(ctx, "get", "pod", "-n", t.Namespace, "-l", selector,
			"--field-selector", "spec.nodeName="+t.NodeName, "-o", "jsonpath={.items[0].metadata.name}")
		if rerr != nil {
			return "", "", "", evidence, errors.Wrapf(rerr, "no pod of %s runs on node %s", selector, t.NodeName)
		}
		podName = strings.TrimSpace(string(out))
		if podName == "" {
			return "", "", "", evidence, fmt.Errorf("no pod of %s runs on node %s; refusing to change a workload that is not there", selector, t.NodeName)
		}
	}
	evidence["pod"] = podName

	podUID, err = runner.jsonpath(ctx, "pod", podName, t.Namespace, "{.metadata.uid}")
	if err != nil || podUID == "" {
		return "", "", "", evidence, fmt.Errorf("target pod %s/%s not found or unreadable (Forbidden is not NotFound): %w", t.Namespace, podName, err)
	}
	ownerName, err := runner.jsonpath(ctx, "pod", podName, t.Namespace, "{.metadata.ownerReferences[0].name}")
	if err != nil || ownerName != t.ControllerName {
		return "", "", "", evidence, fmt.Errorf("pod %s is not owned by the declared controller %s (owner=%q): refusing to mutate an unexpected object", podName, t.ControllerName, ownerName)
	}
	if isController, cerr := runner.jsonpath(ctx, "pod", podName, t.Namespace, "{.metadata.ownerReferences[0].controller}"); cerr != nil || isController != "true" {
		return "", "", "", evidence, fmt.Errorf("pod %s owner reference %q is not marked controller=true: refusing to recreate a Pod whose managing controller is unconfirmed", podName, t.ControllerName)
	}
	controllerKind := strings.ToLower(t.ControllerKind)
	controllerUID, err = runner.jsonpath(ctx, controllerKind, t.ControllerName, t.Namespace, "{.metadata.uid}")
	if err != nil || controllerUID == "" {
		return "", "", "", evidence, fmt.Errorf("declared controller %s/%s cannot be read in %s: %w", t.ControllerKind, t.ControllerName, t.Namespace, err)
	}
	ownerUID, err := runner.jsonpath(ctx, "pod", podName, t.Namespace, "{.metadata.ownerReferences[0].uid}")
	if err != nil || ownerUID != controllerUID {
		return "", "", "", evidence, fmt.Errorf("pod %s is owned by %q uid %q but the live %s/%s has uid %q: the Pod belongs to a different controller generation",
			podName, t.ControllerName, ownerUID, t.ControllerKind, t.ControllerName, controllerUID)
	}
	evidence["controllerUid"] = controllerUID

	switch {
	case t.PVCName != "":
		stableUID, err = runner.jsonpath(ctx, "pvc", t.PVCName, t.Namespace, "{.metadata.uid}")
		if err != nil || stableUID == "" {
			return "", "", "", evidence, fmt.Errorf("target PVC %s/%s not found: %w", t.Namespace, t.PVCName, err)
		}
		claimNames, cerr := runner.jsonpath(ctx, "pod", podName, t.Namespace, "{.spec.volumes[*].persistentVolumeClaim.claimName}")
		if cerr != nil {
			return "", "", "", evidence, fmt.Errorf("the volumes of pod %s cannot be read: %w", podName, cerr)
		}
		if !jsonpathListContains(claimNames, t.PVCName) {
			return "", "", "", evidence, fmt.Errorf("pod %s does not mount the declared PVC %s (its claims are %q): refusing to certify persistence of a volume it does not use",
				podName, t.PVCName, claimNames)
		}
		pvName, _ := runner.jsonpath(ctx, "pvc", t.PVCName, t.Namespace, "{.spec.volumeName}")
		if pvName == "" {
			return "", "", "", evidence, fmt.Errorf("PVC %s/%s is not bound to a persistent volume, so a recreation cannot certify durable storage", t.Namespace, t.PVCName)
		}
		claimRefUID, perr := runner.jsonpath(ctx, "persistentvolume", pvName, "", "{.spec.claimRef.uid}")
		if perr != nil || claimRefUID != stableUID {
			return "", "", "", evidence, fmt.Errorf("PV %s is claimed by uid %q but PVC %s has uid %q: the binding is not this pair, so persistence is not attestable",
				pvName, claimRefUID, t.PVCName, stableUID)
		}
		evidence["pvc"] = t.PVCName
		evidence["pv"] = pvName
		evidence["pvcUid"] = stableUID
	case t.HostPath != "":
		// A collector keeps its cursor on a per-node hostPath. The identity that
		// must survive is therefore the node plus that directory, and the pod must
		// really mount it — verified from the pod spec, not from a name.
		if t.NodeName == "" {
			return "", "", "", evidence, fmt.Errorf("target %s declares a hostPath but no node, so there is no identity to hold it to", t.LedgerToken)
		}
		mounts, herr := runner.jsonpath(ctx, "pod", podName, t.Namespace, "{.spec.volumes[*].hostPath.path}")
		if herr != nil {
			return "", "", "", evidence, fmt.Errorf("the hostPath volumes of pod %s cannot be read: %w", podName, herr)
		}
		if !jsonpathListContains(mounts, t.HostPath) {
			return "", "", "", evidence, fmt.Errorf("pod %s does not mount the declared hostPath %s (it mounts %q): the cursor of that directory is not this pod's state",
				podName, t.HostPath, mounts)
		}
		nodeUID, nerr := runner.jsonpath(ctx, "node", t.NodeName, "", "{.metadata.uid}")
		if nerr != nil || nodeUID == "" {
			return "", "", "", evidence, fmt.Errorf("node %s cannot be read: %w", t.NodeName, nerr)
		}
		stableUID = nodeUID
		evidence["hostPath"] = t.HostPath
		evidence["node"] = t.NodeName
		evidence["nodeUid"] = nodeUID
		// The DaemonSet's own uid is the stable controller identity here; a replaced
		// DaemonSet would be a different workload, so it is reported and compared
		// by the plan that uses this target.
	default:
		return "", "", "", evidence, fmt.Errorf("target %s declares neither a PVC nor a hostPath, so nothing is known about what must survive its rebuild", t.LedgerToken)
	}
	return podUID, stableUID, controllerUID, evidence, nil
}

// persistenceCheck is the business-data half of a recreation: what this attempt
// writes before the change and must read back after it, through the component's
// real protocol. Both halves run inside the same ledger-guarded step, so the
// read-back cannot be served by a second write.
type persistenceCheck struct {
	// Write puts the token down. A false return means the change is not made at
	// all — no delete is issued against storage whose contents are unknown.
	Write func(a *acceptanceAttempt, t acceptanceTarget, token string) (string, bool)
	// ReadBack looks for the SAME token after the object came back.
	ReadBack func(a *acceptanceAttempt, t acceptanceTarget, token string) (string, bool)
	// AfterRecreation optionally re-checks a stable identity besides the volume
	// (the managing controller, the node). Detail is reported either way.
	AfterRecreation func(a *acceptanceAttempt, t acceptanceTarget, evidence map[string]string) (string, bool)
}

// recreateOnce is the single entry through which an acceptance plan may change a
// business object. It is the only function in this package that calls
// deletePodWithUIDPrecondition for a workload pod, and it enforces the whole
// sequence in one place:
//
//  1. authorise the target from live facts (owner generation, the storage the pod
//     really uses);
//  2. durably record the intent, atomically and flushed, BEFORE any change — so a
//     second call for this (base run, target) can only ever be refused;
//  3. write through the real protocol;
//  4. delete exactly once, with the authorised uid inside the request;
//  5. wait for a different uid that is Ready;
//  6. read the same token back and confirm the stable identity is unchanged.
//
// Refusals (conflict, Forbidden, nothing at the name) and unknown outcomes
// (timeout, cancellation) are recorded apart: the first says nothing happened,
// the second says nobody knows. Neither is retried, and neither re-reads a fresh
// uid to try again with.
func (a *acceptanceAttempt) recreateOnce(t acceptanceTarget, check persistenceCheck) VerifyStepResult {
	result := VerifyStepResult{ID: "", Title: "", Target: t.LedgerToken}
	podUID, stableUID, _, evidence, err := authorizeTarget(a.ctx, a.runner, t)
	result.Evidence = evidence
	if err != nil {
		result.Status = VerifyStatusFailed
		result.Detail = err.Error()
		return result
	}
	// The keys the single-target acceptance already reported: oldPodUID/newPodUID
	// for the object that had to be replaced, oldPVCUID/newPVCUID for the volume
	// that had to survive untouched. A hostPath target records the node identity
	// under its own keys instead, because it has no PVC to name.
	evidence["oldPodUID"] = podUID
	if t.PVCName != "" {
		evidence["oldPVCUID"] = stableUID
	} else {
		evidence["oldStableUid"] = stableUID
	}
	if existing, found, perr := peekAcceptanceLedger(a.stateDir, a.runID, t); perr != nil {
		result.Status = VerifyStatusFailed
		result.Detail = perr.Error()
		return result
	} else if found {
		result.Status = VerifyStatusFailed
		result.QuotaState = existing.State
		result.Detail = fmt.Sprintf("the recreation of %s for base run %s is already recorded as %s; refusing a second delete — start a new install run instead of replaying", t.LedgerToken, a.runID, existing.State)
		return result
	}

	token := "ani-accept-" + acceptanceToken()
	rec := acceptanceLedger{
		State: ledgerStateAttempted, RunID: a.runID, Target: t.LedgerToken,
		OldPodUID: podUID, OldPVCUID: stableUID,
		Controller: t.ControllerKind + "/" + t.ControllerName,
		Token:      token, StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	existing, created, err := claimAcceptanceLedger(a.stateDir, a.runID, t, rec)
	if err != nil {
		result.Status = VerifyStatusFailed
		result.QuotaState = existing.State
		result.Detail = fmt.Sprintf("could not record the recreation intent: %v", err)
		return result
	}
	if !created {
		result.Status = VerifyStatusFailed
		result.QuotaState = existing.State
		result.Detail = fmt.Sprintf("the recreation of %s for base run %s was claimed by a concurrent attempt (state %s); this run changes nothing", t.LedgerToken, a.runID, existing.State)
		return result
	}
	result.QuotaState = ledgerStateAttempted

	if detail, ok := check.Write(a, t, token); !ok {
		result.Status = VerifyStatusFailed
		result.Detail = detail + ledgerWarning(finalizeAcceptanceLedger(a.stateDir, a.runID, t, terminalLedger(rec, ledgerStateUnknown, "write-failed")))
		return result
	}
	a.wroteToken = true
	if t.AuxMarkerPath != "" {
		_, _ = a.runner.execIn(a.ctx, t.Namespace, podName(t, evidence), t.Container,
			fmt.Sprintf("printf '%%s' '%s' > '%s'", token, t.AuxMarkerPath))
	}

	// Immediate pre-delete recheck. It is not the authorization — the server-side
	// precondition is — but a name that already changed hands should be refused
	// here, where the reason can be stated exactly.
	liveUID, err := a.runner.jsonpath(a.ctx, "pod", podName(t, evidence), t.Namespace, "{.metadata.uid}")
	if err != nil || liveUID != podUID {
		result.Status = VerifyStatusFailed
		result.QuotaState = ledgerStateRefused
		result.Detail = fmt.Sprintf("the target pod identity changed before the delete (authorized %s, live %q); refusing to delete a same-name replacement", podUID, liveUID)
		// The claim stays: this (run,target) had its budget read against a real
		// delete intent, and re-arming it from a later attempt would be a second
		// chance at the same object.
		result.Detail += ledgerWarning(finalizeAcceptanceLedger(a.stateDir, a.runID, t, terminalLedger(rec, ledgerStateRefused, "identity-changed")))
		return result
	}

	outcome, deleteErr := a.deleter.deletePodWithUIDPrecondition(a.ctx, t.Namespace, podName(t, evidence), podUID)
	evidence["deleteOutcome"] = string(outcome)
	switch outcome {
	case deleteDeleted:
	case deleteConflict, deleteForbidden, deleteNotFound:
		result.Status = VerifyStatusFailed
		result.QuotaState = ledgerStateRefused
		result.Detail = ledgerWarning(finalizeAcceptanceLedger(a.stateDir, a.runID, t, terminalLedger(rec, ledgerStateRefused, string(outcome)))) +
			fmt.Sprintf("the uid-preconditioned delete of pod %s/%s was refused (%s: %v); this run deleted nothing and will not retry without the precondition",
				t.Namespace, podName(t, evidence), outcome, deleteErr)
		return result
	default:
		result.Status = VerifyStatusFailed
		result.QuotaState = ledgerStateUnknown
		result.Detail = ledgerWarning(finalizeAcceptanceLedger(a.stateDir, a.runID, t, terminalLedger(rec, ledgerStateUnknown, "delete-"+string(outcome)))) +
			fmt.Sprintf("the uid-preconditioned pod delete failed with an unknown remote outcome (%s: %v); it is not retried without the precondition",
				outcome, deleteErr)
		return result
	}

	newPodUID, err := a.waitForReplacedPod(t, podName(t, evidence), podUID, rec)
	if err != nil {
		result.Status = VerifyStatusFailed
		result.QuotaState = ledgerStateUnknown
		result.Detail = err.Error()
		return result
	}
	evidence["newPodUID"] = newPodUID

	if t.PVCName != "" {
		newStable, perr := a.runner.jsonpath(a.ctx, "pvc", t.PVCName, t.Namespace, "{.metadata.uid}")
		if perr != nil || newStable == "" {
			result.Status = VerifyStatusFailed
			result.QuotaState = ledgerStateUnknown
			result.Detail = fmt.Sprintf("the PVC %s is gone after the recreation: %v", t.PVCName, perr) +
				ledgerWarning(finalizeAcceptanceLedger(a.stateDir, a.runID, t, terminalLedgerWithNewPod(rec, ledgerStateUnknown, "pvc-gone", newPodUID)))
			return result
		}
		if newStable != stableUID {
			result.Status = VerifyStatusFailed
			result.QuotaState = ledgerStateDone
			result.Detail = "the PVC was replaced by a new object; a persistence acceptance can never pass on fresh storage" +
				ledgerWarning(finalizeAcceptanceLedger(a.stateDir, a.runID, t, terminalLedgerWithNewPod(rec, ledgerStateDone, "pvc-replaced", newPodUID)))
			return result
		}
		evidence["newPVCUID"] = newStable
	}
	if check.AfterRecreation != nil {
		if detail, ok := check.AfterRecreation(a, t, evidence); !ok {
			result.Status = VerifyStatusFailed
			result.QuotaState = ledgerStateDone
			result.Detail = detail + ledgerWarning(finalizeAcceptanceLedger(a.stateDir, a.runID, t, terminalLedgerWithNewPod(rec, ledgerStateDone, "identity-changed", newPodUID)))
			return result
		}
	}
	if detail, ok := check.ReadBack(a, t, token); !ok {
		result.Status = VerifyStatusFailed
		result.QuotaState = ledgerStateDone
		result.Detail = detail + ledgerWarning(finalizeAcceptanceLedger(a.stateDir, a.runID, t, terminalLedgerWithNewPod(rec, ledgerStateDone, "data-lost", newPodUID)))
		return result
	}
	if err := finalizeAcceptanceLedger(a.stateDir, a.runID, t, terminalLedgerWithNewPod(rec, ledgerStateDone, VerifyStatusPass, newPodUID)); err != nil {
		result.Status = VerifyStatusFailed
		result.Detail = fmt.Sprintf("the recreation passed but its ledger could not be finalized (result is not silently retried): %v", err)
		return result
	}
	result.Status = VerifyStatusPass
	result.QuotaState = ledgerStateDone
	result.Detail = fmt.Sprintf("one planned recreation of %s completed under uid %s", t.LedgerToken, podUID)
	return result
}

// declareTargetCompletely is the pre-change check that a declared object states
// everything the run needs to authorise it: a workload, a pod or a node to act
// on, a protocol, and one — and only one — kind of durable storage identity.
func declareTargetCompletely(t acceptanceTarget) error {
	if strings.TrimSpace(t.Namespace) == "" || strings.TrimSpace(t.ControllerKind) == "" || strings.TrimSpace(t.ControllerName) == "" {
		return fmt.Errorf("target %s does not declare its namespace and managing controller", t.ledgerKey())
	}
	if t.Protocol == "" {
		return fmt.Errorf("target %s declares no data protocol, so nothing would be read back after the rebuild", t.ledgerKey())
	}
	switch {
	case t.PodName == "" && t.NodeName == "":
		return fmt.Errorf("target %s declares neither a pod nor a node, so there is no object to authorise", t.ledgerKey())
	case t.PodName != "" && t.NodeName != "":
		return fmt.Errorf("target %s declares both a pod name and a node, which is two different objects", t.ledgerKey())
	case t.NodeName != "" && t.PodSelector == "":
		return fmt.Errorf("target %s names a node but no selector for its daemon pods", t.ledgerKey())
	case t.PVCName != "" && t.HostPath != "":
		return fmt.Errorf("target %s declares both a PVC and a hostPath; a workload has one durable storage identity", t.ledgerKey())
	case t.PVCName == "" && t.HostPath == "":
		return fmt.Errorf("target %s declares neither a PVC nor a hostPath, so nothing is known about what must survive its rebuild", t.ledgerKey())
	case t.HostPath != "" && t.ControllerKind != "DaemonSet":
		return fmt.Errorf("target %s binds a hostPath to a %s; a per-node directory belongs to a daemon workload",
			t.ledgerKey(), t.ControllerKind)
	}
	return nil
}

// podName is the pod authorizeTarget resolved for this target. It is reported
// through the evidence map because a daemon pod's name is a live fact, not a
// declaration.
func podName(t acceptanceTarget, evidence map[string]string) string {
	if name := evidence["pod"]; name != "" {
		return name
	}
	return t.PodName
}

// waitForReplacedPod waits one bounded budget for the controller to bring back a
// pod with a DIFFERENT uid that is Ready. No second delete, no agent restart, and
// Running-but-not-Ready is never a pass. The ledger it reports into is the record
// this attempt claimed, so a cancelled or timed-out wait closes the entry it
// opened instead of inventing an unrelated one.
func (a *acceptanceAttempt) waitForReplacedPod(t acceptanceTarget, name, oldPodUID string, rec acceptanceLedger) (string, error) {
	recreateTimeout := 5 * time.Minute
	if v := strings.TrimSpace(os.Getenv("ANI_VERIFY_POD_RECREATE_TIMEOUT")); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil && parsed > 0 {
			recreateTimeout = parsed
		}
	}
	deadline := time.Now().Add(recreateTimeout)
	lastUID := ""
	for {
		if ctxErr := a.ctx.Err(); ctxErr != nil {
			// errors.Wrapf(nil, ...) would answer nil here, and a nil error from a
			// cancelled wait would let the plan continue past a change whose remote
			// effect is unknown. The cancellation IS the failure.
			ledgerErr := finalizeAcceptanceLedger(a.stateDir, a.runID, t, terminalLedger(rec, ledgerStateUnknown, "cancelled"))
			return "", fmt.Errorf("waiting for the recreated pod was cancelled; the remote result is unknown and the delete is not replayed (ledger close-out: %v): %w", ledgerErr, ctxErr)
		}
		read, err := a.runner.jsonpath(a.ctx, "pod", name, t.Namespace, "{.metadata.uid}")
		if err == nil && read != "" {
			lastUID = read
		}
		if lastUID != "" && lastUID != oldPodUID {
			ready, _ := a.runner.jsonpath(a.ctx, "pod", name, t.Namespace, "{.status.conditions[?(@.type==\"Ready\")].status}")
			if ready == "True" {
				return lastUID, nil
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("the pod did not come back Ready with a new UID within %s (last uid=%q); Running-but-not-Ready is never a pass%s",
				recreateTimeout, lastUID, ledgerWarning(finalizeAcceptanceLedger(a.stateDir, a.runID, t, terminalLedgerWithNewPod(rec, ledgerStateUnknown, "not-ready", lastUID))))
		}
		select {
		case <-a.ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

// runPlan executes one component's declared steps in order. The first failure
// stops every later change; the steps after it are recorded as not_run rather
// than skipped silently, so a partial run cannot read as a complete one.
func (a *acceptanceAttempt) runPlan(plan acceptancePlan) []VerifyStepResult {
	results := make([]VerifyStepResult, 0, len(plan.Steps))
	stopped := false
	for _, step := range plan.Steps {
		if stopped {
			results = append(results, VerifyStepResult{
				ID: step.ID, Title: step.Title, Status: VerifyStatusNotRun,
				Target: step.Target,
				Detail: "an earlier step failed or the run was cancelled; no further change is attempted",
			})
			continue
		}
		result := a.runStep(step)
		if result.ID == "" {
			result.ID = step.ID
			result.Title = step.Title
		}
		results = append(results, result)
		if result.Status != VerifyStatusPass || a.ctx.Err() != nil {
			stopped = true
		}
	}
	return results
}

// targetQuotaDigest is what the report prints for a declared target, so an
// operator can see which durable record the run was checked against.
func targetQuotaDigest(stateDir, runID string, t acceptanceTarget) string {
	rec, found, err := peekAcceptanceLedger(stateDir, runID, t)
	if err != nil {
		return "unreadable"
	}
	if !found {
		return "unspent"
	}
	return rec.State
}

// planKeys hashes the plan's step ids + target keys. It is used only as a report
// label: two different declarations of the same component must not be able to
// share a report file, and no quota decision reads it.
func planKeysHash(plan acceptancePlan) string {
	var builder strings.Builder
	for _, step := range plan.Steps {
		fmt.Fprintf(&builder, "%s/%s\n", step.ID, step.Target)
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:])[:12]
}

// preflightPlan is the "refuse before the first change" gate: every mutating
// step's target must be declared completely, be authorisable from live facts,
// and still have its one budget. A plan that cannot state all of that up front
// is refused before it writes, deletes, or spends anything.
func preflightPlan(a *acceptanceAttempt, plan acceptancePlan) error {
	seen := map[string]bool{}
	for _, step := range plan.Steps {
		if step.Run == nil {
			return fmt.Errorf("step %s of %s declares no work", step.ID, a.component)
		}
		if step.Target == "" {
			continue
		}
		if seen[step.Target] {
			return fmt.Errorf("step %s of %s changes %s again; an acceptance plan may declare at most one change per target",
				step.ID, a.component, step.Target)
		}
		seen[step.Target] = true
		target, ok := acceptanceTargets[step.Target]
		if !ok {
			return fmt.Errorf("step %s names target %q which %s does not declare", step.ID, step.Target, a.component)
		}
		// A target that cannot say what must survive its rebuild is not a check,
		// so it is refused here rather than after a delete.
		if err := declareTargetCompletely(target); err != nil {
			return fmt.Errorf("acceptance of %s is refused before any change: %w", a.component, err)
		}
		// The LIVE identity is read inside the step, once, immediately before the
		// change it authorises. Re-reading it here would put a second read between
		// the authorisation and the delete, and a pod replaced in that window would
		// then be the one this run recorded — the client would look consistent
		// while aiming at a stranger.
		if rec, found, err := peekAcceptanceLedger(a.stateDir, a.runID, target); err != nil {
			return fmt.Errorf("acceptance of %s is refused before any change: %w", a.component, err)
		} else if found {
			return fmt.Errorf("acceptance of %s is refused before any change: target %s is already recorded as %s for base run %s",
				a.component, target.LedgerToken, rec.State, a.runID)
		}
	}
	for _, dependency := range plan.Dependencies {
		if _, ok := acceptanceTargets[dependency]; !ok {
			return fmt.Errorf("%s declares a dependency on %q, which is not a declared target", a.component, dependency)
		}
	}
	return nil
}

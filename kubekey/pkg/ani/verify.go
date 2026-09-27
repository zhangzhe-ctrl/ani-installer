/*
Copyright 2026 The KubeSphere Contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ani

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/cockroachdb/errors"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// ---------------------------------------------------------------------------
// R13/A14: the verification dispatcher. `kk ani verify` reads a run record
// (run.json written by `kk ani validate`), never the site YAML, and dispatches
// the three verification layers separately:
//
//   smoke      — read-only: runs each enabled component's packaged verify
//                script. It never deletes or recreates a component Pod and
//                never touches global alert routing.
//   acceptance — the declared, one-shot persistence check: with an explicit
//                --allow-pod-recreate flag, exactly one planned Pod recreation
//                per pre-declared target, with old/new Pod UID, controller,
//                PVC UID tracking and a data marker that must survive.
//
// Reports are per run and per level; a failed acceptance is recorded and the
// same run cannot be re-run through the same mutation (no parameter-identical
// second attempt), while the install state stays untouched.
// ---------------------------------------------------------------------------

// Verify levels and statuses.
const (
	VerifyLevelSmoke      = "smoke"
	VerifyLevelAcceptance = "acceptance"

	VerifyStatusPass   = "pass"
	VerifyStatusFailed = "fail"
	// VerifyStatusNotRun is the one non-pass that is not itself a component
	// failure: it names what a first failure stopped this level from attempting.
	// There is deliberately no "skipped" status any more — C04 removed the branch
	// that produced it, because a level that skipped its targets and reported pass
	// was indistinguishable from one that checked them.
	VerifyStatusNotRun = "not_run"

	VerifyReportSchemaVersion = 1
)

// VerifyInput is the input of `kk ani verify`.
type VerifyInput struct {
	// RunFile is the run record: run.json as written by `kk ani validate`.
	RunFile string
	// StateFile optionally points at the install state (run-state.json from
	// the runner). When present, its RunID identifies the run and a failed
	// install phase refuses verification.
	StateFile string
	Level     string
	Only      []string
	// AllowPodRecreate gates the whole acceptance level: without it, the
	// dispatcher refuses to run any mutation (R13 step 5).
	AllowPodRecreate bool
	// ScriptDir is where the packaged component verify scripts live
	// (/etc/kubernetes/ani on a real install node).
	ScriptDir string
	// Kubeconfig is used for acceptance kubectl calls and handed to smoke
	// scripts through ANI_VERIFY_KUBECONFIG.
	Kubeconfig string
	// Output is the report directory.
	Output string
}

// VerifyComponentResult is one component's outcome for one level.
type VerifyComponentResult struct {
	Component string            `json:"component"`
	Status    string            `json:"status"`
	Detail    string            `json:"detail,omitempty"`
	Evidence  map[string]string `json:"evidence,omitempty"`
	// Steps is the component's declared check set with each step's own outcome.
	// A component status of pass means every step below passed; the steps stay in
	// the report so the claim can be read instead of trusted.
	Steps []VerifyStepResult `json:"steps,omitempty"`
	// Dependencies names the business objects this component's acceptance changed
	// on behalf of another component, so a scoped run cannot hide their impact.
	Dependencies []string `json:"dependencies,omitempty"`
}

// VerifyReport is the machine-readable record of one verify invocation.
type VerifyReport struct {
	SchemaVersion int    `json:"schemaVersion"`
	RunID         string `json:"runId"`
	// RecordKind names what kind of subject this verification ran against, and
	// BaseRunID is the install run whose quota and invariants apply. They are
	// separate because a components execution introduces its own run id.
	RecordKind string `json:"recordKind,omitempty"`
	BaseRunID  string `json:"baseRunId,omitempty"`
	// Operation + DidInstall come from the execution record: they say whether
	// this subject installed anything, so a verification can never be read as
	// proof of an install it did not observe.
	Operation        string                  `json:"operation,omitempty"`
	DidInstall       *bool                   `json:"didInstall,omitempty"`
	InstallerCode    string                  `json:"installerCodeBinaryDigest,omitempty"`
	VerifierCode     string                  `json:"verifierCodeBinaryDigest,omitempty"`
	ClusterName      string                  `json:"clusterName"`
	ConfigDigest     string                  `json:"configDigest"`
	NetworkStack     string                  `json:"networkStack"`
	Level            string                  `json:"level"`
	AllowPodRecreate bool                    `json:"allowPodRecreate"`
	StartedAt        string                  `json:"startedAt"`
	FinishedAt       string                  `json:"finishedAt"`
	Results          []VerifyComponentResult `json:"results"`
	// NotDeclared names the components in scope that this level has no declared
	// check for. They are reported, never counted as a pass: an acceptance that
	// ran nothing must not look like an acceptance that passed.
	NotDeclared []string `json:"notDeclared,omitempty"`
	Overall     string   `json:"overall"`
}

// acceptanceTarget is a pre-declared persistence check for one component:
// the workload identity, its PVC, and the REAL data protocol that must survive
// the single planned recreation. Declarations live here, as data, so a component
// cannot invent mutation scope at runtime.
//
// Protocol selects the business-semantics verifier (F09/C):
//
//	postgres-sql     — a committed row inserted via psql before the rebuild and
//	                   read back by SELECT after it. The aux marker file is
//	                   written for extra evidence but is NEVER the pass test.
//	nats-jetstream   — a persisted JetStream message published before the
//	                   rebuild and consumed+acked after it via the approved
//	                   nats-box client. cat-ing a file is not JetStream.
type acceptanceTarget struct {
	Namespace      string
	ControllerKind string
	ControllerName string
	PodName        string
	PVCName        string
	Container      string
	Protocol       string
	AuxMarkerPath  string
	// ClientImage is the approved one-shot client used to run the protocol for
	// servers that do not ship their own CLI (NATS uses nats-box; PostgreSQL
	// execs psql that is present in its own container, so it is empty).
	ClientImage string
	// LedgerToken is the stable identity the one-change budget is keyed to. It
	// deliberately does not contain the component: a shared object reached from two
	// components' plans is one target with one budget, and a rename of the
	// component, a sub-run or a different --output cannot reopen it. Empty falls
	// back to ControllerName, which is what the original single-target components
	// already used, so their existing ledger files keep blocking.
	LedgerToken string
	// NodeName + PodSelector + HostPath describe a workload whose pods are per-node
	// and whose durable state is a hostPath rather than a PVC (the collector). A
	// target declares either a PVC or a hostPath, never a fabrication of one.
	NodeName    string
	PodSelector string
	HostPath    string
}

// acceptanceProtocols.
const (
	acceptancePostgresSQL   = "postgres-sql"
	acceptanceNATSJetStream = "nats-jetstream"
)

// acceptanceTargets registers every implemented acceptance check. Components
// without an entry are skipped (never silently passed).
// acceptanceTargets is the registry of business objects an acceptance plan may
// change. It is keyed by the stable target identity rather than by component,
// because one object can legitimately appear in two components' plans (the
// logging backend is Fluent Bit's durability dependency and Loki's own target):
// they resolve to the same entry, so they share the same one-change budget.
var acceptanceTargets = map[string]acceptanceTarget{
	"postgresql": {
		LedgerToken:    "postgresql",
		Namespace:      "ani-platform",
		ControllerKind: "StatefulSet",
		ControllerName: "postgresql",
		PodName:        "postgresql-0",
		PVCName:        "data-postgresql-0",
		Container:      "postgresql",
		Protocol:       acceptancePostgresSQL,
		AuxMarkerPath:  "/var/lib/postgresql/data/ani-acceptance-marker",
	},
	"prometheus": {
		LedgerToken: "prometheus", Namespace: MetricsNamespace,
		ControllerKind: "StatefulSet", ControllerName: "prometheus-ani-metrics-prometheus",
		PodName:   "prometheus-ani-metrics-prometheus-0",
		PVCName:   "prometheus-ani-metrics-prometheus-db-prometheus-ani-metrics-prometheus-0",
		Container: "prometheus", Protocol: "metrics-prometheus",
	},
	"alertmanager": {
		LedgerToken: "alertmanager", Namespace: MetricsNamespace,
		ControllerKind: "StatefulSet", ControllerName: "alertmanager-ani-metrics-alertmanager",
		PodName:   "alertmanager-ani-metrics-alertmanager-0",
		PVCName:   "alertmanager-ani-metrics-alertmanager-db-alertmanager-ani-metrics-alertmanager-0",
		Container: "alertmanager", Protocol: "metrics-alertmanager",
	},
	"nats": {
		LedgerToken:    "nats",
		Namespace:      "ani-platform",
		ControllerKind: "StatefulSet",
		ControllerName: "nats",
		PodName:        "nats-0",
		PVCName:        "nats-js-nats-0",
		Container:      "nats",
		Protocol:       acceptanceNATSJetStream,
		AuxMarkerPath:  "/data/ani-acceptance-marker",
		// Approved, offline one-shot client for the nats CLI. The empty host
		// means "resolve from the run manifest registry" at execution time.
		ClientImage: "natsio/nats-box:0.19.7",
	},
}

// protocolWriteThrough and protocolReadBackThrough adapt the existing
// per-protocol implementations to a step's persistenceCheck.
func protocolWriteThrough(a *acceptanceAttempt, t acceptanceTarget, token string) (string, bool) {
	return protocolWrite(a.ctx, a.runner, t, a.registry, token)
}

func protocolReadBackThrough(a *acceptanceAttempt, t acceptanceTarget, token string) (string, bool) {
	return protocolReadBack(a.ctx, a.runner, t, a.registry, token)
}

// recreateStep is the step shape a single-target component uses: one declared
// object, one budget, the component's real protocol on both sides of it.
func recreateStep(stepID, stepTitle, targetKey string) acceptanceStep {
	return acceptanceStep{
		ID: stepID, Title: stepTitle, Target: targetKey,
		Run: func(a *acceptanceAttempt, result *VerifyStepResult) error {
			step := a.recreateOnce(acceptanceTargets[targetKey], persistenceCheck{
				Write:    protocolWriteThrough,
				ReadBack: protocolReadBackThrough,
			})
			result.QuotaState = step.QuotaState
			result.Detail = step.Detail
			for key, value := range step.Evidence {
				result.Evidence[key] = value
			}
			if step.Status != VerifyStatusPass {
				return errors.New(step.Detail)
			}
			return nil
		},
	}
}

// acceptancePlans declares what acceptance can actually check, per component. A
// component with no entry here has no acceptance, and naming it with --only is
// refused before anything runs.
var acceptancePlans = map[string]acceptancePlan{
	"metrics": metricsAcceptancePlan(),
	"postgresql": {Steps: []acceptanceStep{
		recreateStep("PG-01", "a committed row survives the one planned postgresql-0 recreation", "postgresql"),
	}},
	"nats": {Steps: []acceptanceStep{
		recreateStep("NATS-01", "a persisted JetStream message survives the one planned nats-0 recreation", "nats"),
	}},
}

// declaredAcceptanceComponents lists the components acceptance is implemented for,
// sorted, for the refusal messages.
func declaredAcceptanceComponents() []string {
	names := make([]string, 0, len(acceptancePlans))
	for name := range acceptancePlans {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// acceptanceLedger is the durable, pre-change intent record that makes a
// declared recreation happen at most once per (run, target). It is written
// atomically into a canonical state directory (NOT the --output dir) while the
// shared installer lock is held, so a crash, a concurrent invocation, or a
// changed --output cannot re-acquire the delete quota.
type acceptanceLedger struct {
	State      string `json:"state"` // attempted | done | unknown
	RunID      string `json:"runId"`
	Target     string `json:"target"`
	OldPodUID  string `json:"oldPodUID"`
	OldPVCUID  string `json:"oldPVCUID"`
	NewPodUID  string `json:"newPodUID,omitempty"`
	Controller string `json:"controller"`
	Token      string `json:"token"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
}

const (
	ledgerStateAttempted = "attempted"
	// ledgerStateDone is the terminal state of an attempt whose change happened.
	ledgerStateDone = "done"
	// ledgerStateUnknown is terminal too, but for the opposite reason: the change
	// was issued and its effect could not be read. Neither may be replayed.
	ledgerStateUnknown = "unknown"
	// ledgerStateRefused is the third terminal state C03 needs: the server answered
	// definitively that it would not delete (uid precondition conflict, Forbidden,
	// or nothing at the name). Nothing changed, and that is knowable — which is why
	// it must not be written as a deletion, nor retried against whatever stands
	// there now.
	ledgerStateRefused = "refused"
)

// acceptanceStateDir is the canonical directory for the one-shot ledger. It is
// keyed by the install run and the target, resolved from the install run record
// location (stable, per cluster), never the report output directory. Tests set
// ANI_ACCEPTANCE_STATE_DIR to keep writes inside a temp dir; production uses the
// installer runtime root. This makes the delete quota independent of --output.
func acceptanceStateDir(clusterName string) (string, error) {
	base := strings.TrimSpace(os.Getenv("ANI_ACCEPTANCE_STATE_DIR"))
	if base == "" {
		base = filepath.Join(runtimeBaseDir, clusterName, "acceptance")
	}
	if clusterName == "" || clusterName == "." || clusterName == ".." ||
		strings.ContainsAny(clusterName, `/\`) {
		return "", fmt.Errorf("cluster name %q cannot form a safe state path", clusterName)
	}
	return base, nil
}

// ledgerKey is the identity the one-change budget is bound to. Falling back to
// the controller name keeps the files the single-target components already wrote
// blocking after the change, so an old spent record cannot be re-armed by
// renaming a constant in Go.
func (a acceptanceTarget) ledgerKey() string {
	if token := strings.TrimSpace(a.LedgerToken); token != "" {
		return token
	}
	return a.ControllerName
}

func (a acceptanceTarget) ledgerFile(stateDir, runID string) string {
	return filepath.Join(stateDir, fmt.Sprintf("ledger-%s-%s.json", runID, a.ledgerKey()))
}

// VerifyInputDefaults fills the operational defaults.
func (input *VerifyInput) defaults() error {
	input.RunFile = strings.TrimSpace(input.RunFile)
	if input.RunFile == "" {
		return errors.New("verify needs --run pointing at a run.json record")
	}
	input.Level = strings.TrimSpace(input.Level)
	switch input.Level {
	case VerifyLevelSmoke, VerifyLevelAcceptance:
	default:
		return fmt.Errorf("verify --level must be %q or %q, got %q", VerifyLevelSmoke, VerifyLevelAcceptance, input.Level)
	}
	if strings.TrimSpace(input.ScriptDir) == "" {
		input.ScriptDir = "/etc/kubernetes/ani"
	}
	if strings.TrimSpace(input.Kubeconfig) == "" {
		input.Kubeconfig = DefaultKubeconfigPath
	}
	if strings.TrimSpace(input.Output) == "" {
		input.Output = "/var/lib/ani-installer/verify"
	}
	return nil
}

// loadVerifyRun reads the run record and the optional install state, then
// enforces the F04 success contract: verification may only proceed against a
// genuine install-success record whose identity is well-formed and matches the
// install state. Config-validation records, intermediate/failed/cancelled/
// remote-unknown results, mismatched cluster/digest/runID, and malformed or
// short digests are all refused with an error — never a panic. The run record is
// the ONLY source of component/scope/identity facts; the site YAML is never
// re-parsed here.
// loadedVerifyRun is a run record plus the facts about what it lets the caller
// verify. BaseRunID is the ledger key: an acceptance quota belongs to the
// install being verified, so no execution record's own run id or output
// directory can move it.
type loadedVerifyRun struct {
	manifest    RunManifest
	runID       string
	baseRunID   string
	isExecution bool
	scope       []string
}

// operation names what the consumed record claimed. An install-success record
// has no operation: it installed the cluster it describes.
func (l loadedVerifyRun) operation() string {
	if l.manifest.ComponentsExecution == nil {
		return ""
	}
	return l.manifest.ComponentsExecution.Operation
}

func loadVerifyRun(ctx context.Context, input VerifyInput) (RunManifest, string, error) {
	loaded, err := loadVerifyRunRecord(ctx, input)
	return loaded.manifest, loaded.runID, err
}

func loadVerifyRunRecord(ctx context.Context, input VerifyInput) (loadedVerifyRun, error) {
	data, err := os.ReadFile(input.RunFile)
	if err != nil {
		return loadedVerifyRun{}, errors.Wrapf(err, "read the run record %s", input.RunFile)
	}
	var manifest RunManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		// A components PLAN is a different document that happens to share some
		// field names; name what it is instead of leaking a type mismatch.
		var envelope struct {
			RecordKind       string `json:"recordKind"`
			BaseConfigDigest string `json:"baseConfigDigest"`
			NewConfigDigest  string `json:"newConfigDigest"`
		}
		if json.Unmarshal(data, &envelope) == nil {
			switch {
			case envelope.RecordKind != "" && envelope.RecordKind != RecordKindInstallSuccess &&
				envelope.RecordKind != RecordKindComponentsExecution:
				return loadedVerifyRun{}, fmt.Errorf("run record is %q; verification consumes a %q or a %q record, and a plan is neither",
					envelope.RecordKind, RecordKindInstallSuccess, RecordKindComponentsExecution)
			case envelope.RecordKind == "" && envelope.BaseConfigDigest != "" && envelope.NewConfigDigest != "":
				// A components plan: read-only intent, no execution behind it.
				return loadedVerifyRun{}, fmt.Errorf("this is a components plan, not an execution record; verify consumes a %q or a %q record and a plan is neither",
					RecordKindInstallSuccess, RecordKindComponentsExecution)
			}
		}
		return loadedVerifyRun{}, errors.Wrapf(err, "parse the run record %s", input.RunFile)
	}
	// The block, not the declared string, decides what a record is. Without this
	// a components execution record could be re-labelled "install-success" to skip
	// every base-bytes, evidence and live-cluster check — and, worse, to have its
	// one declared change budget keyed to its own subject run instead of the base.
	if manifest.ComponentsExecution != nil && manifest.RecordKind == RecordKindInstallSuccess {
		return loadedVerifyRun{}, fmt.Errorf("run record %s carries a componentsExecution block, so it records one components execution and is consumed through the execution checks only; declaring it %q cannot skip them",
			input.RunFile, RecordKindInstallSuccess)
	}
	if manifest.RecordKind == RecordKindComponentsExecution {
		return loadComponentsExecutionRun(ctx, input, manifest)
	}

	var state *InstallState
	statePath := input.StateFile
	if statePath == "" {
		candidate := filepath.Join(filepath.Dir(input.RunFile), "run-state.json")
		if _, err := os.Stat(candidate); err == nil {
			statePath = candidate
		}
	}
	if statePath != "" {
		st, err := ReadRunState(statePath)
		if err != nil {
			return loadedVerifyRun{}, errors.Wrapf(err, "read the install state %s", statePath)
		}
		state = st
	}
	if err := ValidateSuccessRecord(manifest, state); err != nil {
		return loadedVerifyRun{}, err
	}
	return loadedVerifyRun{manifest: manifest, runID: manifest.RunID, baseRunID: manifest.RunID}, nil
}

// bindVerifyTarget makes every verification answer for the cluster and the host
// that are standing here NOW, not for whatever a JSON file once described.
//
// C01: the components-execution path already re-read the live cluster (through
// ValidateComponentsExecutionBase) but the install-success path did not, so
// cluster A's genuine record handed to `--kubeconfig` for cluster B sailed past
// every check and could run SQL and a named Pod delete against B, because B
// happened to use the same namespace and object names. A copied record plus a
// copied kubeconfig also brings a different local flock, so the host the record
// names is checked too.
func bindVerifyTarget(ctx context.Context, input VerifyInput, loaded loadedVerifyRun) error {
	manifest := loaded.manifest
	want := manifest.Identity.ClusterUID
	if loaded.isExecution {
		// The base install owns the invariants and the quota; the execution's own
		// observation must agree with it, and both must be the cluster live here.
		want = manifest.ComponentsExecution.BaseClusterUID
	}
	if strings.TrimSpace(want) == "" {
		return errors.New("the record binds no cluster identity to verify against; refusing to verify an unbound record")
	}
	runner := kubectlRunner{bin: kubectlBin(), kubeconfig: input.Kubeconfig}
	liveID, _, err := captureLiveCluster(ctx, runner)
	if err != nil {
		return errors.Wrap(err, "read the live cluster identity this record must belong to")
	}
	if liveID.ClusterUID != want {
		return fmt.Errorf("the record belongs to cluster uid %s but the kubeconfig in use points at %s; refusing to verify (and never to mutate) a different cluster than the record attests",
			want, liveID.ClusterUID)
	}
	// Only the level that changes anything is pinned to the original installer
	// host: that is where the shared product lock lives, and a copied record plus
	// a copied kubeconfig on another machine brings a different flock. A read-only
	// smoke of the right cluster is still read-only wherever it is run from.
	if input.Level == VerifyLevelAcceptance {
		return verifyExecutionHostAddress("installer node", manifest.Installer.Name, manifest.Installer.Address)
	}
	return nil
}

// loadComponentsExecutionRun consumes the record of one components execution
// (L-06). It re-runs the same identity machinery an install record passes — on
// the BASE it names — and then re-observes the live cluster, so a record cannot
// be replayed against a different cluster, a different base or a mutated file.
func loadComponentsExecutionRun(ctx context.Context, input VerifyInput, manifest RunManifest) (loadedVerifyRun, error) {
	if err := ValidateComponentsExecutionShape(manifest); err != nil {
		return loadedVerifyRun{}, err
	}
	if manifest.Result != ResultSucceeded {
		return loadedVerifyRun{}, fmt.Errorf("components execution record %s has result %q; only a succeeded execution may be verified (a %s pass keeps its record as an event fact, never as a pass)",
			manifest.RunID, manifest.Result, manifest.Result)
	}
	runner := kubectlRunner{bin: kubectlBin(), kubeconfig: input.Kubeconfig}
	base, err := ValidateComponentsExecutionBase(ctx, manifest, runner)
	if err != nil {
		return loadedVerifyRun{}, err
	}
	scope, allowsMutation, err := componentsExecutionScope(manifest)
	if err != nil {
		return loadedVerifyRun{}, err
	}
	if !allowsMutation && input.Level == VerifyLevelAcceptance {
		return loadedVerifyRun{}, fmt.Errorf("a read-only observation record (didInstall=false) grants verification scope only; acceptance performs a declared recreation and is refused for run %s", manifest.RunID)
	}
	if allowsMutation && input.Level == VerifyLevelAcceptance {
		// Acceptance through an execution record is allowed only for what that
		// execution actually installed, and the quota stays keyed to the base
		// install run — so no new sub-run id can re-arm a spent target.
		return loadedVerifyRun{manifest: manifest, runID: manifest.RunID, baseRunID: base.RunID, isExecution: true, scope: scope}, nil
	}
	return loadedVerifyRun{manifest: manifest, runID: manifest.RunID, baseRunID: base.RunID, isExecution: true, scope: scope}, nil
}

// verifyScope resolves the component list from --only (a subset of the run
// record's components, in record order).
// verifyScope resolves --only against the set the record itself attests. For a
// components execution record that set is what the execution installed or
// confirmed ANI-owned — never the enabled set of some config file, which would
// let a later config edit widen a spent or unrelated scope.
func verifyScope(manifest RunManifest, attested, only []string) ([]string, error) {
	allowed := attested
	if len(allowed) == 0 {
		allowed = manifest.Components
	}
	if len(allowed) == 0 {
		// A record that names no component at all can verify nothing, and an
		// empty result set is reported — and exited — as a pass. Refusing here
		// keeps "pass" meaning "the checks in this record passed".
		return nil, errors.New("the run record attests no components to verify; refusing to report an empty scope as a pass")
	}
	if len(only) == 0 {
		return allowed, nil
	}
	selected := map[string]bool{}
	for _, name := range only {
		selected[strings.TrimSpace(name)] = true
	}
	scope := make([]string, 0, len(only))
	for _, component := range allowed {
		if selected[component] {
			scope = append(scope, component)
			delete(selected, component)
		}
	}
	if len(selected) > 0 {
		names := make([]string, 0, len(selected))
		for name := range selected {
			names = append(names, name)
		}
		return nil, fmt.Errorf("--only names components outside what this run record attests (%s): %s",
			strings.Join(allowed, ","), strings.Join(names, ","))
	}
	return scope, nil
}

// kubectlRunner bounds every kubectl invocation (R12) and is the seam the
// tests drive with a fake binary on PATH.
type kubectlRunner struct {
	bin        string
	kubeconfig string
}

func (r kubectlRunner) argv(args ...string) []string {
	full := []string{r.bin, "--kubeconfig", r.kubeconfig, "--request-timeout=60s"}
	return append(full, args...)
}

func (r kubectlRunner) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.argv(args...)[0], r.argv(args...)[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, errors.Wrapf(err, "kubectl %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (r kubectlRunner) jsonpath(ctx context.Context, resource, name, namespace, path string) (string, error) {
	args := []string{"get", resource}
	// A cluster-scoped resource has no namespace, and `-n ""` is an argument
	// kubectl refuses outright, so the flag is only present when there is one.
	if namespace != "" {
		args = append(args, name, "-n", namespace)
	} else {
		args = append(args, name)
	}
	args = append(args, "-o", "jsonpath="+path)
	out, err := r.run(ctx, args...)
	return strings.TrimSpace(string(out)), err
}

// jsonpathListContains compares whole entries, so a claim called
// "data-postgresql-0-extra" never satisfies a target called "data-postgresql-0".
func jsonpathListContains(spaceSeparated, want string) bool {
	for _, got := range strings.Fields(spaceSeparated) {
		if got == want {
			return true
		}
	}
	return false
}

// RunVerify dispatches one verification level over the run record and writes
// the per-run, per-level report.
func RunVerify(ctx context.Context, input VerifyInput, stdout io.Writer) error {
	if err := input.defaults(); err != nil {
		return err
	}
	loaded, err := loadVerifyRunRecord(ctx, input)
	if err != nil {
		return err
	}
	manifest, runID := loaded.manifest, loaded.runID
	// C01: before this command can attest, report or change anything, the cluster
	// its --kubeconfig actually reaches has to be the cluster the record belongs
	// to. Both record kinds pass through here, so the install-success path can no
	// longer be replayed against a lookalike cluster.
	if err := bindVerifyTarget(ctx, input, loaded); err != nil {
		return err
	}
	scope, err := verifyScope(manifest, loaded.scope, input.Only)
	if err != nil {
		return err
	}

	// Acceptance is mutation: without the explicit flag it is refused before
	// anything runs (R13 step 5 / T-R13-01).
	if input.Level == VerifyLevelAcceptance && !input.AllowPodRecreate {
		return errors.New("acceptance performs a declared Pod recreation and needs --allow-pod-recreate; refusing to run without it")
	}
	// C04: naming a component whose acceptance is not implemented is refused up
	// front, before the shared lock or any one-shot quota is touched. Silently
	// recording it as `skipped` and then reporting the level as passed is the
	// opposite of a verification: it would let `--only metrics` "pass" without
	// ever checking metrics.
	var acceptanceNotDeclared []string
	if input.Level == VerifyLevelAcceptance {
		implemented := declaredAcceptanceComponents()
		declared, skipped := splitDeclaredTargets(scope)
		// C04, explicitly requested: naming a component whose acceptance does not
		// exist is refused BEFORE the lock and before any one-shot quota is
		// consumed. Recording it `skipped` and passing the level would let
		// `--only metrics` certify metrics without ever looking at metrics.
		if len(input.Only) > 0 && len(skipped) > 0 {
			return fmt.Errorf("acceptance is not implemented for %s; this build declares it only for %s. Refusing to run the rest and report the level as passed — do not substitute a hand-run script that bypasses the ledger",
				strings.Join(skipped, ","), strings.Join(implemented, ","))
		}
		// C04, not explicitly requested: the level narrows to what it can really
		// check, and says plainly what it left out. A level that would run nothing
		// at all is refused instead of passing on an empty result set.
		if len(declared) == 0 {
			return fmt.Errorf("acceptance has no declared check for anything in this scope (%s); it declares %s. Refusing to report a level that verified nothing as a pass",
				strings.Join(scope, ","), strings.Join(implemented, ","))
		}
		acceptanceNotDeclared = skipped
		scope = declared
	}

	var install *bool
	if e := manifest.ComponentsExecution; e != nil {
		install = &e.DidInstall
	}
	report := VerifyReport{
		NotDeclared:      acceptanceNotDeclared,
		SchemaVersion:    VerifyReportSchemaVersion,
		RunID:            runID,
		RecordKind:       manifest.RecordKind,
		BaseRunID:        loaded.baseRunID,
		Operation:        loaded.operation(),
		InstallerCode:    manifest.Identity.CodeBinaryDigest,
		VerifierCode:     selfBinaryDigest(),
		DidInstall:       install,
		ClusterName:      manifest.ClusterName,
		ConfigDigest:     manifest.ConfigDigest,
		NetworkStack:     manifest.NetworkStack,
		Level:            input.Level,
		AllowPodRecreate: input.AllowPodRecreate,
		StartedAt:        time.Now().UTC().Format(time.RFC3339),
	}

	// The report path: smoke stays run-scoped (it is read-only and re-runnable);
	// acceptance is keyed per (run, scope) so a later, different target in the
	// same run is not mis-blocked by an earlier target's report (T07). The
	// delete quota is NOT decided by any report path: it lives in the durable
	// acceptance ledger, written BEFORE the change under the shared installer
	// lock in a canonical state dir that --output cannot influence.
	reportDir := input.Output
	// A report is named for ITS OWN subject, so an execution record's smoke run
	// can never overwrite the base install's report (or another subject's).
	// The mutation quota is the exception: it is keyed to the BASE install run,
	// because that is the run whose one-declared-change budget applies.
	scopeKey := runID
	if input.Level == VerifyLevelAcceptance {
		scopeKey = acceptanceScopeKey(loaded.baseRunID, scope)
	}
	reportPath := filepath.Join(reportDir, fmt.Sprintf("verify-%s-%s.json", input.Level, scopeKey))

	runner := kubectlRunner{bin: kubectlBin(), kubeconfig: input.Kubeconfig}
	if input.Level == VerifyLevelSmoke {
		report.Results = runSmokeScope(ctx, input, loaded.runID, scope)
	} else {
		// C03: the recreation is deleted through an API client built from the same
		// kubeconfig the record was just bound to, so the authorised uid can travel
		// inside the delete request. Building it here — before the shared lock and
		// before any one-shot quota is claimed — keeps an input that could never
		// have worked from consuming an attempt.
		deleter, clientErr := newPodDeleter(input.Kubeconfig)
		if clientErr != nil {
			return clientErr
		}
		// Acceptance mutates. Serialize it with the first install and component
		// execute on the ONE shared product lock.
		release, lockErr := AcquireInstallFlock(productLockPath())
		if lockErr != nil {
			return errors.Wrap(lockErr, "acceptance needs the shared installer lock")
		}
		report.Results, err = runAcceptanceScope(ctx, input, runner, deleter, scope, loaded.baseRunID, manifest)
		release()
		if err != nil {
			return err
		}
	}

	// C04: a level passes only when every result in it passed. The previous rollup
	// flipped on an explicit `fail` alone, so `skipped`, `not_run` or an unset
	// status fell through to `overall: pass` with exit 0 — "nothing was checked"
	// and "everything was checked" were indistinguishable.
	report.Overall = VerifyStatusPass
	if len(report.Results) == 0 {
		report.Overall = VerifyStatusFailed
	}
	for _, result := range report.Results {
		if result.Status != VerifyStatusPass {
			report.Overall = VerifyStatusFailed
			break
		}
	}
	report.FinishedAt = time.Now().UTC().Format(time.RFC3339)

	if err := writeVerifyReport(reportDir, reportPath, report); err != nil {
		return err
	}

	out := stdout
	if out == nil {
		out = os.Stdout
	}
	for _, result := range report.Results {
		fmt.Fprintf(out, "verify %s %s: %s\n", input.Level, result.Component, result.Status)
	}
	if len(report.NotDeclared) > 0 {
		fmt.Fprintf(out, "verify %s did NOT check (no declared check in this build): %s\n", input.Level, strings.Join(report.NotDeclared, ","))
	}
	fmt.Fprintf(out, "verify %s overall: %s (run=%s report=%s)\n", input.Level, report.Overall, runID, reportPath)
	// State plainly what kind of subject was verified, so a read-only
	// observation can never be read as a fresh install and an execution record
	// is never mistaken for the first install itself.
	switch {
	case loaded.isExecution && loaded.operation() == ComponentsOperationNoop:
		fmt.Fprintf(out, "NOTE: subject is a components observation record for run %s (operation=%s, didInstall=false, base run %s): it verifies what it confirmed already ANI-owned and attests no installation by this pass.\n", runID, loaded.operation(), loaded.baseRunID)
	case loaded.isExecution:
		fmt.Fprintf(out, "NOTE: subject is a components execution record for run %s (operation=%s, didInstall=true, base run %s); it verifies this addition, not the base install.\n", runID, loaded.operation(), loaded.baseRunID)
	default:
		fmt.Fprintf(out, "NOTE: subject is the first install's own success record (run %s).\n", runID)
	}
	if report.Overall != VerifyStatusPass {
		return fmt.Errorf("verify level %s failed for run %s", input.Level, runID)
	}
	return nil
}

func kubectlBin() string {
	if bin := strings.TrimSpace(os.Getenv("ANI_VERIFY_KUBECTL")); bin != "" {
		return bin
	}
	return "kubectl"
}

func writeVerifyReport(dir, path string, report VerifyReport) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.Wrapf(err, "create the verify report directory %s", dir)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return errors.Wrap(err, "encode the verify report")
	}
	encoded = append(encoded, '\n')
	return os.WriteFile(path, encoded, 0o644)
}

// runSmokeScope runs the packaged verify script per component at the SMOKE
// level. It honours the caller's context: an already-cancelled or expiring
// context refuses to start a script, and every launched script runs under
// CommandContext so a cancellation bounds the real subprocess (F09). The smoke
// level is passed to the script via ANI_VERIFY_LEVEL so a checker can perform
// only necessary readiness/lightweight checks rather than the full durability/
// alert-chain that belongs to acceptance. First failure stops the scope; later
// components are recorded not_run.
func runSmokeScope(ctx context.Context, input VerifyInput, subjectRunID string, scope []string) []VerifyComponentResult {
	results := make([]VerifyComponentResult, 0, len(scope))
	failed := false
	for _, component := range scope {
		if failed {
			results = append(results, VerifyComponentResult{
				Component: component,
				Status:    VerifyStatusNotRun,
				Detail:    "an earlier component failed; nothing further is executed",
			})
			continue
		}
		if ctx.Err() != nil {
			// The context was cancelled before this script started: refuse to
			// launch it and record the rest as not_run (F09).
			results = append(results, VerifyComponentResult{
				Component: component,
				Status:    VerifyStatusFailed,
				Detail:    fmt.Sprintf("smoke cancelled before the script was started: %v", ctx.Err()),
			})
			failed = true
			continue
		}
		// Both the script path and the evidence directory are built from a name
		// that came out of the record, so neither may become a path: an execution
		// record is checked for this by its shape validator, and an install record
		// gets the same treatment here rather than trusting its own text.
		safeComponent := sanitizePathToken(component)
		script := filepath.Join(input.ScriptDir, safeComponent, "verify.sh")
		if _, err := os.Stat(script); err != nil {
			results = append(results, VerifyComponentResult{
				Component: component,
				Status:    VerifyStatusFailed,
				Detail:    fmt.Sprintf("the packaged verify script %s is missing", script),
			})
			failed = true
			continue
		}
		// The run id is part of the directory so a second smoke pass (base run,
		// execution run) can never overwrite the first one's evidence.
		outputDir := filepath.Join(input.Output, "smoke-"+sanitizePathToken(subjectRunID)+"-"+safeComponent)
		cmd := exec.CommandContext(ctx, "bash", script)
		// F09/C02: the script is a shell that spawns children (kubectl, sleeps).
		// CommandContext already installed a Cancel that kills only bash, and
		// Setpgid on its own does not change what that Cancel does, so the
		// replacement must be assigned unconditionally: cancelling has to take
		// the whole process group of THIS task's script — never a broad pattern
		// kill of shared processes.
		cmd.SysProcAttr = smokeSysProcAttr()
		cmd.Cancel = func() error { return smokeKillFunc(cmd) }
		if cmd.WaitDelay == 0 {
			cmd.WaitDelay = smokeWaitDelay
		}
		cmd.Env = append(os.Environ(),
			"ANI_VERIFY_KUBECONFIG="+input.Kubeconfig,
			"ANI_VERIFY_OUTPUT_DIR="+outputDir,
			"ANI_VERIFY_LEVEL="+VerifyLevelSmoke,
			// C08: this executable is the conditional-delete entry point the
			// checker uses for a probe it created. Without it the checker reports
			// its cleanup as incomplete instead of deleting by name.
			"ANI_KK_BIN="+selfExecutable(),
		)
		out, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			results = append(results, VerifyComponentResult{
				Component: component,
				Status:    VerifyStatusFailed,
				Detail:    fmt.Sprintf("smoke script cancelled: %v", ctx.Err()),
			})
			failed = true
			continue
		}
		if err != nil {
			results = append(results, VerifyComponentResult{
				Component: component,
				Status:    VerifyStatusFailed,
				Detail:    strings.TrimSpace(string(out)),
			})
			failed = true
			continue
		}
		results = append(results, VerifyComponentResult{
			Component: component,
			Status:    VerifyStatusPass,
			Detail:    strings.TrimSpace(string(out)),
		})
	}
	return results
}

// splitDeclaredTargets separates what this acceptance level can actually check
// from what it cannot, preserving scope order.
func splitDeclaredTargets(scope []string) (declared, notDeclared []string) {
	for _, component := range scope {
		if _, ok := acceptancePlans[component]; ok {
			declared = append(declared, component)
			continue
		}
		notDeclared = append(notDeclared, component)
	}
	return declared, notDeclared
}

// acceptanceScopeKey makes the acceptance report path stable per (run, scope)
// so a later, different target in the same run is not mis-blocked by an earlier
// target's report (T07). It is only the report filename; the delete quota lives
// in the durable ledger, not here.
func acceptanceScopeKey(runID string, scope []string) string {
	names := make([]string, 0, len(scope))
	for _, s := range scope {
		names = append(names, s)
	}
	sort.Strings(names)
	sum := sha256.Sum256([]byte(strings.Join(names, ",")))
	return fmt.Sprintf("%s-%s", runID, hex.EncodeToString(sum[:])[:16])
}

// claimAcceptanceLedger atomically records the intent to recreate (run,target)
// BEFORE any change, under the already-held installer lock. O_EXCL makes a
// concurrent or repeated attempt see the existing record instead of re-acquiring
// a delete quota. Returns (existingRecord, created, error).
//
// C03: the intent is only an authorization if it is durable before the change.
// Both the file contents and its directory entry are flushed, and a flush failure
// stops the mutation instead of issuing an unlogged delete.
func claimAcceptanceLedger(stateDir, runID string, target acceptanceTarget, rec acceptanceLedger) (acceptanceLedger, bool, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return acceptanceLedger{}, false, errors.Wrapf(err, "create acceptance ledger dir %s", stateDir)
	}
	if err := syncDir(stateDir); err != nil {
		return acceptanceLedger{}, false, err
	}
	path := target.ledgerFile(stateDir, runID)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return acceptanceLedger{}, false, errors.Wrapf(rerr, "read existing ledger %s", path)
			}
			var existing acceptanceLedger
			if jerr := json.Unmarshal(data, &existing); jerr != nil {
				// A corrupt/partial ledger is treated as UNKNOWN: the delete is
				// not re-issued (conservative, never silently retried).
				return acceptanceLedger{State: ledgerStateUnknown}, false, nil
			}
			return existing, false, nil
		}
		return acceptanceLedger{}, false, errors.Wrapf(err, "create ledger %s", path)
	}
	defer f.Close()
	enc, err := json.Marshal(rec)
	if err != nil {
		return acceptanceLedger{}, false, errors.Wrap(err, "encode ledger intent")
	}
	if _, err := f.Write(append(enc, '\n')); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return acceptanceLedger{}, false, errors.Wrapf(err, "write ledger %s", path)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return acceptanceLedger{}, false, errors.Wrapf(err, "flush ledger intent %s before any change", path)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return acceptanceLedger{}, false, errors.Wrapf(err, "close ledger intent %s", path)
	}
	if err := syncDir(stateDir); err != nil {
		_ = os.Remove(path)
		return acceptanceLedger{}, false, errors.Wrap(err, "the recreation intent is not durable; refusing to change anything")
	}
	return rec, true, nil
}

// finalizeAcceptanceLedger rewrites the ledger with its terminal state (done or
// unknown) after the change outcome is known. The rename and its directory entry
// are both flushed, so a recorded `done` cannot be lost and re-armed by a crash.
func finalizeAcceptanceLedger(stateDir, runID string, target acceptanceTarget, rec acceptanceLedger) error {
	path := target.ledgerFile(stateDir, runID)
	enc, err := json.Marshal(rec)
	if err != nil {
		return errors.Wrap(err, "encode final ledger")
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.Wrapf(err, "write final ledger %s", tmp)
	}
	if _, err := f.Write(append(enc, '\n')); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return errors.Wrapf(err, "write final ledger %s", tmp)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return errors.Wrapf(err, "flush final ledger %s", tmp)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return errors.Wrapf(err, "close final ledger %s", tmp)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return errors.Wrapf(err, "rename final ledger over %s", path)
	}
	return syncDir(stateDir)
}

// terminalLedger closes the intent record out without dropping any identity it
// carried: the old pod/PVC uids and the controller stay in the terminal entry, so
// a later reader can tell exactly which object the spent quota referred to.
func terminalLedger(rec acceptanceLedger, state, outcome string) acceptanceLedger {
	return terminalLedgerWithNewPod(rec, state, outcome, "")
}

func terminalLedgerWithNewPod(rec acceptanceLedger, state, outcome, newPodUID string) acceptanceLedger {
	out := rec
	out.State = state
	out.Outcome = outcome
	out.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	out.NewPodUID = newPodUID
	return out
}

// ledgerWarning turns a failed terminal ledger write into text the operator sees.
// The change has already happened by then, so the run must still report its real
// outcome while saying plainly that the durable record did not close.
func ledgerWarning(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("the durable recreation ledger could not be closed (%v); this quota may read as still-attempted, which is reported rather than retried: ", err)
}

// acceptanceToken is a per-run unique business value, never a fixed string, so
// a leftover object from another attempt cannot satisfy the check.
func acceptanceToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// execIn runs a shell program inside one container of a pod and returns stdout.
func (r kubectlRunner) execIn(ctx context.Context, namespace, pod, container, program string) ([]byte, error) {
	return r.run(ctx, "exec", "-n", namespace, pod, "-c", container, "--", "/bin/sh", "-c", program)
}

// smokeSysProcAttr puts the checker script into its own process group so a
// cancellation can terminate the script AND its descendants (F09: bounded exit
// of this task's processes only — never shared system processes).
func smokeSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// smokeWaitDelay bounds how long a cancelled smoke pass may keep waiting on the
// script's output pipes. It is a wait bound only: the pipes being closed is not
// what stops the descendants, smokeKillFunc is.
const smokeWaitDelay = 5 * time.Second

// smokeKillFunc signals the whole process group on context cancellation.
// cmd.Cancel runs with cmd.Process already started, and a group that has already
// exited reports ESRCH — which os/exec treats as "already gone", not a failure.
func smokeKillFunc(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// runAcceptanceScope executes the declared check sets. The FIRST failing step
// stops its component's plan and every later component's changes: the remaining
// work is recorded not_run, and no further Pod recreation is attempted
// (R13 step 7 / T-R13-04).
func runAcceptanceScope(ctx context.Context, input VerifyInput, runner kubectlRunner, deleter *podDeleter, scope []string, runID string, manifest RunManifest) ([]VerifyComponentResult, error) {
	stateDir, err := acceptanceStateDir(manifest.ClusterName)
	if err != nil {
		return nil, err
	}
	registry := fmt.Sprintf("%s:%d", manifest.Installer.RegistryHost, manifest.Installer.RegistryPort)
	results := make([]VerifyComponentResult, 0, len(scope))
	failed := false
	for _, component := range scope {
		plan, declared := acceptancePlans[component]
		if !declared {
			// runAcceptanceScope only receives declared components (C04 narrows the
			// scope first), so this is a programming error, not an outcome.
			return nil, fmt.Errorf("acceptance scope reached %s with no declaration; refusing to invent one", component)
		}
		if failed {
			results = append(results, VerifyComponentResult{
				Component:    component,
				Status:       VerifyStatusNotRun,
				Dependencies: plan.Dependencies,
				Detail:       "an earlier acceptance failed; no further mutation is attempted",
			})
			continue
		}
		attempt := &acceptanceAttempt{
			ctx:       ctx,
			runner:    runner,
			deleter:   deleter,
			component: component,
			stateDir:  stateDir,
			runID:     runID,
			registry:  registry,
			attempt:   attemptID(),
			// C07: an acceptance run's own temporary output lives under the verify
			// output it was given, never in the base install's runtime tree.
			outputDir: filepath.Join(input.Output, "acceptance-"+sanitizePathToken(runID)+"-"+component),
			scriptDir: input.ScriptDir,
			evidence:  map[string]string{},
		}
		if component == "metrics" {
			attempt.metrics = newMetricsAttemptState(attempt, manifest.ClusterName)
			// Failed attempts can stop before either Pod quota is spent. Give
			// each one a separate evidence directory so a later attempt cannot
			// overwrite its marker, silence or UID ownership record.
			attempt.outputDir = filepath.Join(attempt.outputDir, attempt.metrics.attempt)
			attempt.metrics.outputDir = attempt.outputDir
		}
		if err := os.MkdirAll(attempt.outputDir, 0o700); err != nil {
			return nil, errors.Wrapf(err, "create the acceptance output directory %s for %s", attempt.outputDir, component)
		}
		// Refuse the whole plan before the first change if any target it declares
		// is incomplete, unauthorisable, or already spent.
		if err := preflightPlan(attempt, plan); err != nil {
			results = append(results, VerifyComponentResult{
				Component:    component,
				Status:       VerifyStatusFailed,
				Dependencies: plan.Dependencies,
				Detail:       fmt.Sprintf("refused before any change: %v", err),
			})
			failed = true
			continue
		}
		result := planOverall(component, attempt.runPlan(plan))
		result.Dependencies = plan.Dependencies
		for key, value := range attempt.evidence {
			if result.Evidence == nil {
				result.Evidence = map[string]string{}
			}
			result.Evidence[key] = value
		}
		results = append(results, result)
		if result.Status != VerifyStatusPass {
			failed = true
		}
	}
	return results, nil
}

// acceptanceClient is the in-cluster one-shot image that carries the CLI for a
// server which does not ship its own client tooling. The references are the
// approved originals from images.tsv (registry-prefixed at run time); if the
// lock bumps a version, these constants must move with it — they are checked
// against the served registry by the component's own install verification.
const (
	postgresClientImageOriginal = "docker.io/library/postgres:17.11-bookworm"
	natsClientImageOriginal     = "docker.io/natsio/nats-box:0.19.7"
)

// secretEnv names one env var sourced from a Secret key (never a literal).
type secretEnv struct {
	name, secret, key string
}

// buildCheckJob assembles the one-shot acceptance client as a typed batchv1.Job.
//
// C04: this used to be a hand-written YAML string, and its `template:` block sat
// at the document's top level instead of under `spec:`. A Job with no pod
// template is not a Job that can run — it was rejected (or, worse, accepted as an
// empty job) by the server while every offline test that only faked `apply`
// reported success. Building the object from the same types the API uses, and
// then checking the serialised bytes before anything is applied, makes that
// shape error impossible to ship instead of possible to miss.
func buildCheckJob(namespace, name, image string, env map[string]string, sEnv []secretEnv, program string) (*batchv1.Job, error) {
	anyContainer := corev1.Container{
		Name:            "client",
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		// The program runs inside the container shell; it is passed via command
		// args, and the token is a generated hex string, never user input.
		Command: []string{"/bin/sh", "-c", program},
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		anyContainer.Env = append(anyContainer.Env, corev1.EnvVar{Name: k, Value: env[k]})
	}
	for _, s := range sEnv {
		anyContainer.Env = append(anyContainer.Env, corev1.EnvVar{
			Name: s.name,
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: s.secret},
				Key:                  s.key,
			}},
		})
	}
	labels := map[string]string{"app.kubernetes.io/name": "ani-acceptance"}
	backoff := int32(0)
	return &batchv1.Job{
		TypeMeta:   metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers:    []corev1.Container{anyContainer},
				},
			},
		},
	}, nil
}

// validateCheckJob is the local structural gate C04 asks for: everything the
// server would reject is checked here, BEFORE the one-shot delete quota is
// consumed, so a manifest that cannot run never spends an attempt.
func validateCheckJob(job *batchv1.Job) error {
	if job.APIVersion != "batch/v1" || job.Kind != "Job" {
		return fmt.Errorf("acceptance job must be a batch/v1 Job, got %s/%s", job.APIVersion, job.Kind)
	}
	if job.Name == "" || job.Namespace == "" {
		return errors.New("acceptance job must be named and namespaced")
	}
	containers := job.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		return fmt.Errorf("acceptance job declares %d pod template containers; exactly one client is expected "+
			"(a template that lost its containers is what a mis-nested manifest produces)", len(containers))
	}
	if job.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
		return fmt.Errorf("acceptance job must restartPolicy Never, got %q", job.Spec.Template.Spec.RestartPolicy)
	}
	c := containers[0]
	if strings.TrimSpace(c.Image) == "" {
		return errors.New("acceptance job container has no image")
	}
	if len(c.Command) < 3 || c.Command[0] != "/bin/sh" || c.Command[1] != "-c" || strings.TrimSpace(c.Command[2]) == "" {
		return errors.New("acceptance job container has no shell program to run")
	}
	return nil
}

// acceptanceJobManifest renders the typed job for `kubectl apply -f`. It is
// exported through the seam below so a test asserts on the bytes that actually
// leave this process, not on a re-derived copy.
func acceptanceJobManifest(job *batchv1.Job) ([]byte, error) {
	data, err := yaml.Marshal(job)
	if err != nil {
		return nil, errors.Wrap(err, "encode acceptance job manifest")
	}
	return data, nil
}

// runCheckJob runs a bounded one-shot Job in the namespace the same way the
// component's own verification scripts do: server-side apply, wait for the
// condition, read logs, and delete the job. Credentials reach the container
// only through Secret references — never argv, never a literal. The job name
// embeds the check token so the evidence is attributable to one attempt.
func (r kubectlRunner) runCheckJob(ctx context.Context, namespace, name, image string, env map[string]string, sEnv []secretEnv, program string) (string, error) {
	job, err := buildCheckJob(namespace, name, image, env, sEnv, program)
	if err != nil {
		return "", err
	}
	if err := validateCheckJob(job); err != nil {
		return "", errors.Wrap(err, "refusing to apply an acceptance job the cluster could not run")
	}
	manifest, err := acceptanceJobManifest(job)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "ani-acceptance-job-*.yaml")
	if err != nil {
		return "", errors.Wrap(err, "create acceptance job manifest")
	}
	manifestPath := f.Name()
	defer func() { _ = os.Remove(manifestPath) }()
	if _, err := f.Write(manifest); err != nil {
		_ = f.Close()
		return "", errors.Wrap(err, "write acceptance job manifest")
	}
	if err := f.Close(); err != nil {
		return "", errors.Wrap(err, "close acceptance job manifest")
	}

	if out, err := r.run(ctx, "apply", "--server-side", "-f", manifestPath); err != nil {
		return string(out), errors.Wrapf(err, "apply acceptance job %s", name)
	}
	defer func() {
		// Best-effort cleanup of THIS attempt's own job object only.
		clean := context.WithoutCancel(ctx)
		_, _ = r.run(clean, "delete", "job", name, "-n", namespace, "--wait=false", "--ignore-not-found")
	}()
	if _, err := r.run(ctx, "wait", "--for=condition=complete", "job/"+name, "-n", namespace, "--timeout=300s"); err != nil {
		logs, _ := r.run(context.WithoutCancel(ctx), "logs", "job/"+name, "-n", namespace)
		return string(logs), errors.Wrapf(err, "acceptance job %s did not complete (logs: %s)", name, strings.TrimSpace(string(logs)))
	}
	out, err := r.run(ctx, "logs", "job/"+name, "-n", namespace)
	return string(out), errors.Wrapf(err, "read acceptance job %s logs", name)
}

// protocolWrite persists the token through the component's real business
// protocol. It returns (detail, ok). A failure here is reported as the check
// failing, never as a success on a bare file marker.
func protocolWrite(ctx context.Context, runner kubectlRunner, target acceptanceTarget, registry, token string) (string, bool) {
	switch target.Protocol {
	case acceptancePostgresSQL:
		// Committed SQL: psql autocommits the INSERT, so the row is durable
		// before the rebuild. This is the component's real protocol, not a file
		// marker. Auth uses the container's local unix socket as the postgres
		// superuser (present in the image), and never prints a credential.
		program := "set -e\n" +
			"psql -v ON_ERROR_STOP=1 -U postgres -d postgres -c \"CREATE TABLE IF NOT EXISTS ani_acceptance(k text primary key, v text)\"\n" +
			fmt.Sprintf("psql -v ON_ERROR_STOP=1 -U postgres -d postgres -c \"INSERT INTO ani_acceptance(k,v) VALUES('%s','%s') ON CONFLICT (k) DO UPDATE SET v=EXCLUDED.v\"\n", token, token)
		if _, err := runner.execIn(ctx, target.Namespace, target.PodName, target.Container, program); err != nil {
			return fmt.Sprintf("PostgreSQL committed-SQL write failed (the check never fakes persistence): %v", err), false
		}
		return "", true
	case acceptanceNATSJetStream:
		// Publish one message into the JetStream file-storage stream and require
		// the server PubAck, exactly like the component's own verify job.
		program := "set -e\n" +
			"NATS=\"nats -s nats://nats." + target.Namespace + ".svc:4222 --token \"$NATS_TOKEN\"\"\n" +
			"$NATS stream add ANI_ACCEPT --subjects \"ani.accept.>\" --retention limits --storage file --discard old --replicas 1 --force >/dev/null 2>&1 || $NATS stream info ANI_ACCEPT >/dev/null\n" +
			fmt.Sprintf("$NATS pub -J ani.accept.check \"%s\" | grep -q 'Stored in Stream'\n", token) +
			"echo ANI-NATS-PUBACK-OK\n"
		out, err := runner.runCheckJob(ctx, target.Namespace, "ani-acc-natspub-"+token,
			registry+"/"+strings.TrimPrefix(natsClientImageOriginal, "docker.io/"), nil,
			[]secretEnv{{name: "NATS_TOKEN", secret: "ani-nats-auth", key: "token"}},
			program)
		if err != nil || !strings.Contains(out, "ANI-NATS-PUBACK-OK") {
			return fmt.Sprintf("NATS JetStream publish/PubAck failed (client tooling is approved material; absence is reported, not faked): %v %s", err, strings.TrimSpace(out)), false
		}
		return "", true
	default:
		return fmt.Sprintf("no real data protocol implemented for %q; refusing to fake persistence", target.Protocol), false
	}
}

// protocolReadBack re-reads the token through the component's real protocol
// after the recreation.
func protocolReadBack(ctx context.Context, runner kubectlRunner, target acceptanceTarget, registry, token string) (string, bool) {
	switch target.Protocol {
	case acceptancePostgresSQL:
		program := fmt.Sprintf("psql -tA -v ON_ERROR_STOP=1 -U postgres -d postgres -c \"SELECT v FROM ani_acceptance WHERE k='%s'\"", token)
		out, err := runner.execIn(ctx, target.Namespace, target.PodName, target.Container, program)
		got := strings.TrimSpace(string(out))
		if err != nil || got != token {
			return fmt.Sprintf("the committed PostgreSQL row did not survive the recreation (expected %s, read %q, err %v)", token, got, err), false
		}
		return "", true
	case acceptanceNATSJetStream:
		// Consume+ack the persisted message through a durable pull consumer:
		// this proves JetStream state, not a file cat.
		program := "set -e\n" +
			"NATS=\"nats -s nats://nats." + target.Namespace + ".svc:4222 --token \"$NATS_TOKEN\"\"\n" +
			fmt.Sprintf("printf '{\"durable_name\":\"ani-verify-%s\",\"filter_subject\":\"ani.accept.check\",\"ack_policy\":\"explicit\",\"deliver_policy\":\"all\",\"replay_policy\":\"instant\"}' > /tmp/consumer.json\n", token) +
			"$NATS consumer add ANI_ACCEPT --config /tmp/consumer.json >/dev/null 2>&1 || true\n" +
			fmt.Sprintf("got=\"$(%[1]s consumer next ANI_ACCEPT ani-verify-%[2]s --raw --ack --count 1)\"\n", "$NATS", token) +
			fmt.Sprintf("[ \"$got\" = \"%s\" ] || { echo \"ANI-NATS-MISMATCH got=$got\"; exit 1; }\n", token) +
			"echo ANI-NATS-CONSUMED-OK\n"
		out, err := runner.runCheckJob(ctx, target.Namespace, "ani-acc-natsget-"+token,
			registry+"/"+strings.TrimPrefix(natsClientImageOriginal, "docker.io/"), nil,
			[]secretEnv{{name: "NATS_TOKEN", secret: "ani-nats-auth", key: "token"}},
			program)
		if err != nil || !strings.Contains(out, "ANI-NATS-CONSUMED-OK") {
			return fmt.Sprintf("the persisted JetStream message was not consumed+acked after the rebuild (state may be lost, never passed on a marker file): %v %s", err, strings.TrimSpace(out)), false
		}
		return "", true
	default:
		return fmt.Sprintf("no real data protocol implemented for %q", target.Protocol), false
	}
}

// selfBinaryDigest authenticates which binary performed a verification. A code
// commit cannot do this: several builds share one commit.
func selfBinaryDigest() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// sanitizePathToken keeps an externally supplied run id from becoming a path.
func sanitizePathToken(value string) string {
	safe := make([]rune, 0, len(value))
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			safe = append(safe, r)
			continue
		}
		safe = append(safe, '_')
	}
	if len(safe) == 0 {
		return "run"
	}
	return string(safe)
}

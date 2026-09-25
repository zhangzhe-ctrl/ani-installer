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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
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

	VerifyStatusPass    = "pass"
	VerifyStatusFailed  = "fail"
	VerifyStatusSkipped = "skipped"
	VerifyStatusNotRun  = "not_run"

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
	Component string `json:"component"`
	Status    string `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Evidence  map[string]string `json:"evidence,omitempty"`
}

// VerifyReport is the machine-readable record of one verify invocation.
type VerifyReport struct {
	SchemaVersion    int                      `json:"schemaVersion"`
	RunID            string                   `json:"runId"`
	ClusterName      string                   `json:"clusterName"`
	ConfigDigest     string                   `json:"configDigest"`
	NetworkStack     string                   `json:"networkStack"`
	Level            string                   `json:"level"`
	AllowPodRecreate bool                     `json:"allowPodRecreate"`
	StartedAt        string                   `json:"startedAt"`
	FinishedAt       string                   `json:"finishedAt"`
	Results          []VerifyComponentResult  `json:"results"`
	Overall          string                   `json:"overall"`
}

// acceptanceTarget is a pre-declared persistence check for one component:
// the workload identity, its PVC and the data marker that must survive the
// single planned recreation. Declarations live here, as data, so a component
// cannot invent mutation scope at runtime.
type acceptanceTarget struct {
	Namespace      string
	ControllerKind string
	ControllerName string
	PodName        string
	PVCName        string
	Container      string
	MarkerPath     string
}

// acceptanceTargets registers every implemented acceptance check. Components
// without an entry are skipped (never silently passed).
var acceptanceTargets = map[string]acceptanceTarget{
	"postgresql": {
		Namespace:      "ani-platform",
		ControllerKind: "StatefulSet",
		ControllerName: "postgresql",
		PodName:        "postgresql-0",
		PVCName:        "data-postgresql-0",
		Container:      "postgresql",
		MarkerPath:     "/var/lib/postgresql/data/ani-acceptance-marker",
	},
	"nats": {
		Namespace:      "ani-platform",
		ControllerKind: "StatefulSet",
		ControllerName: "nats",
		PodName:        "nats-0",
		PVCName:        "nats-js-nats-0",
		Container:      "nats",
		MarkerPath:     "/data/ani-acceptance-marker",
	},
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
		input.Kubeconfig = "/etc/kubernetes/admin.conf"
	}
	if strings.TrimSpace(input.Output) == "" {
		input.Output = "/var/lib/ani-installer/verify"
	}
	return nil
}

// loadVerifyRun reads the run record and the optional install state. The run
// record is the ONLY source of component/scope/identity facts — the site YAML
// is never re-parsed here.
func loadVerifyRun(input VerifyInput) (RunManifest, string, error) {
	data, err := os.ReadFile(input.RunFile)
	if err != nil {
		return RunManifest{}, "", errors.Wrapf(err, "read the run record %s", input.RunFile)
	}
	var manifest RunManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return RunManifest{}, "", errors.Wrapf(err, "parse the run record %s", input.RunFile)
	}
	if manifest.ConfigDigest == "" || manifest.ClusterName == "" {
		return RunManifest{}, "", fmt.Errorf("%s is not a run.json record (missing configDigest/clusterName)", input.RunFile)
	}

	runID := ""
	statePath := input.StateFile
	if statePath == "" {
		candidate := filepath.Join(filepath.Dir(input.RunFile), "run-state.json")
		if _, err := os.Stat(candidate); err == nil {
			statePath = candidate
		}
	}
	if statePath != "" {
		state, err := ReadRunState(statePath)
		if err != nil {
			return RunManifest{}, "", errors.Wrapf(err, "read the install state %s", statePath)
		}
		if state.ClusterName != manifest.ClusterName {
			return RunManifest{}, "", fmt.Errorf("install state belongs to cluster %q, run record to %q", state.ClusterName, manifest.ClusterName)
		}
		if state.Phase == "install_failed" {
			return RunManifest{}, "", fmt.Errorf("the install run %s failed (phase=%s); verification requires a successful install", state.RunID, state.Phase)
		}
		runID = state.RunID
	}
	if runID == "" {
		// A validation-only record: deterministic identity from its digest, so
		// reports are still tied to exactly one run record.
		runID = "record-" + manifest.ConfigDigest[:12]
	}
	return manifest, runID, nil
}

// verifyScope resolves the component list from --only (a subset of the run
// record's components, in record order).
func verifyScope(manifest RunManifest, only []string) ([]string, error) {
	if len(only) == 0 {
		return manifest.Components, nil
	}
	selected := map[string]bool{}
	for _, name := range only {
		selected[strings.TrimSpace(name)] = true
	}
	scope := make([]string, 0, len(only))
	for _, component := range manifest.Components {
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
		return nil, fmt.Errorf("--only names components outside this run record: %s", strings.Join(names, ","))
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
	out, err := r.run(ctx, "get", resource, name, "-n", namespace, "-o", "jsonpath="+path)
	return strings.TrimSpace(string(out)), err
}

// RunVerify dispatches one verification level over the run record and writes
// the per-run, per-level report.
func RunVerify(ctx context.Context, input VerifyInput, stdout io.Writer) error {
	if err := input.defaults(); err != nil {
		return err
	}
	manifest, runID, err := loadVerifyRun(input)
	if err != nil {
		return err
	}
	scope, err := verifyScope(manifest, input.Only)
	if err != nil {
		return err
	}

	// Acceptance is mutation: without the explicit flag it is refused before
	// anything runs (R13 step 5 / T-R13-01).
	if input.Level == VerifyLevelAcceptance && !input.AllowPodRecreate {
		return errors.New("acceptance performs a declared Pod recreation and needs --allow-pod-recreate; refusing to run without it")
	}

	report := VerifyReport{
		SchemaVersion:    VerifyReportSchemaVersion,
		RunID:            runID,
		ClusterName:      manifest.ClusterName,
		ConfigDigest:     manifest.ConfigDigest,
		NetworkStack:     manifest.NetworkStack,
		Level:            input.Level,
		AllowPodRecreate: input.AllowPodRecreate,
		StartedAt:        time.Now().UTC().Format(time.RFC3339),
	}

	// One acceptance result per run: a recorded attempt (pass or fail) blocks
	// a parameter-identical second attempt (R13 step 7).
	reportDir := input.Output
	reportPath := filepath.Join(reportDir, fmt.Sprintf("verify-%s-%s.json", input.Level, runID))
	if input.Level == VerifyLevelAcceptance {
		if _, err := os.Stat(reportPath); err == nil {
			return fmt.Errorf("an acceptance report for run %s already exists (%s); its result is final — use a new install run instead of re-running the same mutation", runID, reportPath)
		}
	}

	runner := kubectlRunner{bin: kubectlBin(), kubeconfig: input.Kubeconfig}
	if input.Level == VerifyLevelSmoke {
		report.Results = runSmokeScope(input, scope)
	} else {
		report.Results, err = runAcceptanceScope(ctx, input, runner, scope)
		if err != nil {
			return err
		}
	}

	report.Overall = VerifyStatusPass
	for _, result := range report.Results {
		if result.Status == VerifyStatusFailed {
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
	fmt.Fprintf(out, "verify %s overall: %s (run=%s report=%s)\n", input.Level, report.Overall, runID, reportPath)
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

// runSmokeScope runs the packaged read-only verify script per component. It
// never deletes or recreates anything: the scripts it runs are the packaged
// read-only checkers, and nothing in this path issues a kubectl mutation.
// First failure stops the scope; later components are recorded not_run.
func runSmokeScope(input VerifyInput, scope []string) []VerifyComponentResult {
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
		script := filepath.Join(input.ScriptDir, component, "verify.sh")
		if _, err := os.Stat(script); err != nil {
			results = append(results, VerifyComponentResult{
				Component: component,
				Status:    VerifyStatusFailed,
				Detail:    fmt.Sprintf("the packaged verify script %s is missing", script),
			})
			failed = true
			continue
		}
		outputDir := filepath.Join(input.Output, "smoke-"+component)
		cmd := exec.Command("bash", script)
		cmd.Env = append(os.Environ(),
			"ANI_VERIFY_KUBECONFIG="+input.Kubeconfig,
			"ANI_VERIFY_OUTPUT_DIR="+outputDir,
		)
		out, err := cmd.CombinedOutput()
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

// runAcceptanceScope executes the declared one-shot persistence checks. The
// FIRST failure stops the scope: remaining mutation checks are recorded
// not_run and no further Pod recreation is attempted (R13 step 7 / T-R13-04).
func runAcceptanceScope(ctx context.Context, input VerifyInput, runner kubectlRunner, scope []string) ([]VerifyComponentResult, error) {
	results := make([]VerifyComponentResult, 0, len(scope))
	failed := false
	for _, component := range scope {
		target, declared := acceptanceTargets[component]
		if failed {
			results = append(results, VerifyComponentResult{
				Component: component,
				Status:    VerifyStatusNotRun,
				Detail:    "an earlier acceptance failed; no further mutation is attempted",
			})
			continue
		}
		if !declared {
			results = append(results, VerifyComponentResult{
				Component: component,
				Status:    VerifyStatusSkipped,
				Detail:    "no acceptance declaration is implemented for this component; it is never silently passed",
			})
			continue
		}
		result := runAcceptanceTarget(ctx, runner, component, target)
		results = append(results, result)
		if result.Status != VerifyStatusPass {
			failed = true
		}
	}
	return results, nil
}

// runAcceptanceTarget performs the single planned recreation:
//   1. record the old Pod UID and the PVC UID (the PVC must NOT change);
//   2. write the per-run data marker inside the container;
//   3. delete the Pod — the one planned recreation for this run;
//   4. wait until the Pod exists again with a DIFFERENT UID (a StatefulSet
//      recreates the same name, so only the UID proves the recreation);
//   5. verify the PVC UID is unchanged and the marker still reads back.
func runAcceptanceTarget(ctx context.Context, runner kubectlRunner, component string, target acceptanceTarget) VerifyComponentResult {
	result := VerifyComponentResult{Component: component}
	evidence := map[string]string{
		"controller": target.ControllerKind + "/" + target.ControllerName,
		"pod":        target.Namespace + "/" + target.PodName,
		"pvc":        target.PVCName,
	}

	oldPodUID, err := runner.jsonpath(ctx, "pod", target.PodName, target.Namespace, "{.metadata.uid}")
	if err != nil || oldPodUID == "" {
		result.Status = VerifyStatusFailed
		result.Detail = fmt.Sprintf("target pod %s/%s not found: %v", target.Namespace, target.PodName, err)
		result.Evidence = evidence
		return result
	}
	oldPVCUID, err := runner.jsonpath(ctx, "pvc", target.PVCName, target.Namespace, "{.metadata.uid}")
	if err != nil || oldPVCUID == "" {
		result.Status = VerifyStatusFailed
		result.Detail = fmt.Sprintf("target PVC %s/%s not found: %v", target.Namespace, target.PVCName, err)
		result.Evidence = evidence
		return result
	}
	evidence["oldPodUID"] = oldPodUID
	evidence["oldPVCUID"] = oldPVCUID

	// The data marker is written before the recreation and must survive it.
	marker := "ani-acceptance-" + oldPodUID
	if _, err := runner.run(ctx, "exec", "-n", target.Namespace, target.PodName, "-c", target.Container, "--",
		"sh", "-c", fmt.Sprintf("printf '%%s' '%s' > '%s'", marker, target.MarkerPath)); err != nil {
		result.Status = VerifyStatusFailed
		result.Detail = fmt.Sprintf("writing the data marker failed: %v", err)
		result.Evidence = evidence
		return result
	}
	evidence["markerWritten"] = marker

	// The single planned recreation for this run.
	if _, err := runner.run(ctx, "delete", "pod", target.PodName, "-n", target.Namespace, "--wait=true", "--timeout=300s"); err != nil {
		result.Status = VerifyStatusFailed
		result.Detail = fmt.Sprintf("the planned pod recreation failed: %v", err)
		result.Evidence = evidence
		return result
	}

	// Wait for the controller to recreate the Pod: same name (StatefulSet),
	// different UID — the only proof that the old object is really gone. The
	// budget is env-tunable so behaviour tests stay fast; production uses the
	// 5m default.
	recreateTimeout := 5 * time.Minute
	if v := strings.TrimSpace(os.Getenv("ANI_VERIFY_POD_RECREATE_TIMEOUT")); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil && parsed > 0 {
			recreateTimeout = parsed
		}
	}
	deadline := time.Now().Add(recreateTimeout)
	newPodUID := ""
	for {
		if ctx.Err() != nil {
			result.Status = VerifyStatusFailed
			result.Detail = fmt.Sprintf("waiting for the recreated pod was cancelled: %v", ctx.Err())
			result.Evidence = evidence
			return result
		}
		uid, err := runner.jsonpath(ctx, "pod", target.PodName, target.Namespace, "{.metadata.uid}")
		if err == nil && uid != "" && uid != oldPodUID {
			phase, _ := runner.jsonpath(ctx, "pod", target.PodName, target.Namespace, "{.status.phase}")
			if phase == "Running" {
				newPodUID = uid
				break
			}
		}
		if time.Now().After(deadline) {
			result.Status = VerifyStatusFailed
			result.Detail = fmt.Sprintf("the pod did not come back with a new UID within %s (last uid=%q); the old UID must disappear before the recreation counts as done", recreateTimeout, uid)
			result.Evidence = evidence
			return result
		}
		select {
		case <-ctx.Done():
			result.Status = VerifyStatusFailed
			result.Detail = fmt.Sprintf("waiting for the recreated pod was cancelled: %v", ctx.Err())
			result.Evidence = evidence
			return result
		case <-time.After(2 * time.Second):
		}
	}
	evidence["newPodUID"] = newPodUID

	newPVCUID, err := runner.jsonpath(ctx, "pvc", target.PVCName, target.Namespace, "{.metadata.uid}")
	if err != nil || newPVCUID == "" {
		result.Status = VerifyStatusFailed
		result.Detail = fmt.Sprintf("the PVC %s is gone after the recreation: %v", target.PVCName, err)
		result.Evidence = evidence
		return result
	}
	evidence["newPVCUID"] = newPVCUID
	if newPVCUID != oldPVCUID {
		result.Status = VerifyStatusFailed
		result.Detail = "the PVC was replaced by a new object; a persistence acceptance can never pass on fresh storage"
		result.Evidence = evidence
		return result
	}

	readBack, err := runner.run(ctx, "exec", "-n", target.Namespace, target.PodName, "-c", target.Container, "--",
		"sh", "-c", fmt.Sprintf("cat '%s'", target.MarkerPath))
	if err != nil || strings.TrimSpace(string(readBack)) != marker {
		result.Status = VerifyStatusFailed
		result.Detail = fmt.Sprintf("the data marker did not survive the recreation (read %q)", strings.TrimSpace(string(readBack)))
		result.Evidence = evidence
		return result
	}

	result.Status = VerifyStatusPass
	result.Detail = "one planned recreation completed; data marker survived and the PVC object is unchanged"
	result.Evidence = evidence
	return result
}

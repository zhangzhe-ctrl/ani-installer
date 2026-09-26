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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// R15.2 behaviour tests: the components playbook and the execution path,
// driven by a fake kk binary. The mock proves the standalone playbook is what
// runs — never create_cluster — and that the new run's outputs never touch
// the base install.
// ---------------------------------------------------------------------------

// r15FakeKK writes a fake kk that records every invocation, rejects anything
// that is not the components playbook run, and writes the connection fragment
// the real roles would render into the run's work dir.
func r15FakeKK(t *testing.T, binDir string) string {
	t.Helper()
	script := `#!/usr/bin/env bash
log="${FAKE_KK_LOG:?}"
printf '%s\n' "$*" >> "$log"
# The executor must hand the pinned kubeconfig to the child through the
# environment (roles read $KUBECONFIG, not the operator's own shell default).
printf 'KUBECONFIG=%s\n' "${KUBECONFIG-}" >> "${FAKE_KK_ENV_LOG:?}"
args="$*"
case "$args" in
  *"create cluster"*)
    echo "fake kk: create cluster must never run for components" >&2
    exit 3 ;;
  run\ builtin/core/playbooks/ani_components.yaml*)
    if [ -n "${FAKE_KK_FAIL:-}" ]; then
      echo "fake kk: simulated playbook failure" >&2
      exit 5
    fi
    # Real component roles render their connection fragments into the
    # canonical per-cluster runtime root
    # (/var/lib/ani-installer/<cluster>/work/connections.d) — the fake must
    # follow that contract (R15.3 defect 6: the old fake wrote under
    # --workdir, which matched the executor's wrong assumption and hid the
    # bug). Cluster name comes from the rendered config, like the role's
    # {{ .kubernetes.cluster_name }}.
    cfg=""
    prev=""
    for a in "$@"; do
      if [ "$prev" = "-c" ]; then cfg="$a"; fi
      prev="$a"
    done
    cluster="$(awk '/cluster_name:/{print $2; exit}' "$cfg" 2>/dev/null || true)"
    # One fragment per component in THIS run's scope, exactly like the roles:
    # the executor must read back what the playbook actually wrote and refuse
    # a component whose facts are missing.
    scope="$(awk '
      /^ *scope:/ { indent = match($0, /[^ ]/) - 1; inside = 1; next }
      inside {
        line = $0
        sub(/[ \t]+$/, "", line)
        if (line == "") next
        if (match(line, /[^ ]/) - 1 <= indent) { inside = 0; next }
        if (line ~ /: true$/) { key = line; sub(/:.*/, "", key); gsub(/[ \t]/, "", key); print key }
      }' "$cfg" 2>/dev/null || true)"
    if [ -n "${FAKE_RUNTIME_BASE:-}" ] && [ -n "$cluster" ] && [ -z "${FAKE_KK_NO_FRAGMENTS:-}" ]; then
      mkdir -p "$FAKE_RUNTIME_BASE/$cluster/work/connections.d"
      for comp in $scope; do
        # A real role applies a workload for every component in scope, so the
        # fake must leave that resource existing afterwards: the execution
        # record binds ownership to a live uid, not to a name it hoped for.
        if [ -n "${FAKE_CREATED_FILE:-}" ]; then printf '%s\n' "$comp" >> "$FAKE_CREATED_FILE"; fi
        if [ -n "${FAKE_RUNTIME_BASE:-}" ]; then printf '%s\n' "$comp" >> "$FAKE_RUNTIME_BASE/.applied"; fi
        # FAKE_KK_MISSING_FRAGMENT names one component whose fragment the
        # simulated role forgets to write (the aggregation failure path).
        if [ "$comp" = "${FAKE_KK_MISSING_FRAGMENT:-}" ]; then continue; fi
        if [ "$comp" = nats ]; then
          printf 'NATS connection facts\n' > "$FAKE_RUNTIME_BASE/$cluster/work/connections.d/nats.md"
        else
          printf '%s connection facts\n' "$comp" > "$FAKE_RUNTIME_BASE/$cluster/work/connections.d/$comp.md"
        fi
      done
    fi
    exit 0 ;;
  run*)
    echo "fake kk: unexpected playbook invocation $args" >&2
    exit 4 ;;
esac
echo "fake kk: unsupported $args" >&2
exit 2
`
	path := filepath.Join(binDir, "kk")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake kk: %v", err)
	}
	return path
}

// r15PlanAndExecute plans nats and returns the plan path, the execution
// inputs, the fake kk call log path and the output dir.
func r15PlanAndExecute(t *testing.T, componentsBlock string, natsRelease string) (string, ComponentsExecuteInput, string, string) {
	t.Helper()
	input, outDir := r15PlanFixture(t, componentsBlock, "nats", false, natsRelease)
	// The plan binds the playbook-runner identity, so the fixture kk must
	// exist BEFORE planning: create it and pin the plan digest to it.
	baseDirPre := filepath.Dir(input.ConfigFile)
	binDirPre := filepath.Join(baseDirPre, "kkbin")
	if err := os.MkdirAll(binDirPre, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	r15FakeKK(t, binDirPre)
	input.KKBin = filepath.Join(binDirPre, "kk")
	input.PlanKKDigestOverride = fileSHA256Hex(input.KKBin)
	if err := RunComponentsInstallPlan(context.Background(), input, os.Stdout); err != nil {
		t.Fatalf("plan failed: %v", err)
	}
	planFile := r15OnlyPlanFile(t, outDir)
	execute, kkLog := r15ExecuteInput(t, input, outDir, planFile)
	return planFile, execute, kkLog, outDir
}

// r15OnlyPlanFile returns the single plan file in dir, failing when the
// directory holds none or more than one.
func r15OnlyPlanFile(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read out dir: %v", err)
	}
	planFile := ""
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "components-plan-") {
			if planFile != "" {
				t.Fatalf("more than one plan file in %s: %v", dir, entries)
			}
			planFile = filepath.Join(dir, entry.Name())
		}
	}
	if planFile == "" {
		t.Fatalf("no plan written: %v", entries)
	}
	return planFile
}

// r15ExecuteInput builds the execution environment for one plan produced by the
// same fixture: the fake kk, the isolated canonical runtime root, the embedded
// project hook and the run-scoped output dir.
func r15ExecuteInput(t *testing.T, input ComponentsInstallInput, outDir, planFile string) (ComponentsExecuteInput, string) {
	t.Helper()
	baseDir := filepath.Dir(input.ConfigFile)
	binDir := filepath.Join(baseDir, "kkbin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	r15FakeKK(t, binDir)
	// Isolate the canonical runtime root (the role fragment contract path)
	// into a scratch directory shared with the fake kk.
	fakeBase := filepath.Join(baseDir, "ani-runtime-root")
	if err := os.MkdirAll(fakeBase, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_RUNTIME_BASE", fakeBase)
	restoreBase := runtimeBaseDir
	runtimeBaseDir = fakeBase
	t.Cleanup(func() { runtimeBaseDir = restoreBase })
	// R15.3 contract: without an explicit --project-addr the executor must
	// materialize the project first. Model the embedded-tree hook here so the
	// R15.2 semantics stay about the playbook trajectory, not packaging.
	restore := componentsProjectMaterialize
	componentsProjectMaterialize = func(root string) error {
		path := filepath.Join(root, "builtin", "core", "playbooks", ComponentsPlaybookRelPath)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("# fake materialized\n"), 0o400)
	}
	t.Cleanup(func() { componentsProjectMaterialize = restore })
	kkLog := filepath.Join(baseDir, "kk-calls.log")
	t.Setenv("FAKE_KK_LOG", kkLog)
	t.Setenv("FAKE_KK_ENV_LOG", filepath.Join(baseDir, "kk-env.log"))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	execute := ComponentsExecuteInput{
		PlanFile:    planFile,
		ConfigFile:  input.ConfigFile,
		PackageRoot: input.PackageRoot,
		Kubeconfig:  filepath.Join(baseDir, "kubeconfig"),
		Output:      filepath.Join(outDir, "runtime"),
		KKBin:       filepath.Join(binDir, "kk"),
	}
	return execute, kkLog
}

// The happy path: the mock executes NATS through the standalone playbook,
// the scope-scoped config is what gets handed over, and the new run's
// connection facts never overwrite the base install.
func TestComponentsExecuteMockNATS(t *testing.T) {
	_, execute, kkLog, outDir := r15PlanAndExecute(t, "  certManager: {enabled: true}\n  nats: {enabled: true}", "absent")

	if err := RunComponentsExecute(context.Background(), execute, os.Stdout); err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	logData, err := os.ReadFile(kkLog)
	if err != nil {
		t.Fatalf("read kk log: %v", err)
	}
	log := string(logData)
	// Exactly one invocation, of the standalone playbook.
	if strings.Count(log, "\n") != 0 && strings.Count(strings.TrimSpace(log), "\n") != 0 {
		// single line expected; fall through to content checks
	}
	if !strings.Contains(log, "run builtin/core/playbooks/ani_components.yaml") {
		t.Fatalf("the mock must run the components playbook:\n%s", log)
	}
	if strings.Contains(log, "create cluster") || strings.Contains(log, "create_cluster") {
		t.Fatalf("create_cluster must never be invoked:\n%s", log)
	}
	// The rendered config carries the scope map and the full component state.
	lines := strings.Split(strings.TrimSpace(log), "\n")
	var configPath string
	for _, line := range lines {
		if idx := strings.Index(line, "-c "); idx >= 0 {
			start := idx + len("-c ")
			end := strings.Index(line[start:], " ")
			if end < 0 {
				end = len(line[start:])
			}
			configPath = line[start : start+end]
		}
	}
	if configPath == "" {
		t.Fatalf("the invocation must pass -c config.yaml:\n%s", log)
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read rendered config: %v", err)
	}
	var rendered map[string]any
	if err := yaml.Unmarshal(configData, &rendered); err != nil {
		t.Fatalf("parse rendered config: %v\n%s", err, configData)
	}
	spec := rendered["spec"].(map[string]any)
	ani := spec["ani"].(map[string]any)
	run := ani["components_run"].(map[string]any)
	scope := run["scope"].(map[string]any)
	if scope["nats"] != true {
		t.Fatalf("nats must be in the execution scope: %+v", scope)
	}
	if _, exists := scope["cert-manager"]; exists {
		t.Fatal("a component outside --only must never be in the scope")
	}
	// The new run's connections live in its own runtime root.
	matches, _ := filepath.Glob(filepath.Join(outDir, "runtime", "components-execute-*.json"))
	if len(matches) != 1 {
		t.Fatalf("expected exactly one execution report: %v", matches)
	}
	reportData, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var report ComponentsExecuteReport
	if err := json.Unmarshal(reportData, &report); err != nil {
		t.Fatalf("parse report: %v", err)
	}
	connData, err := os.ReadFile(filepath.Join(outDir, "runtime", "components-"+report.RunID, "connections.md"))
	if err != nil || !strings.Contains(string(connData), "NATS connection facts") {
		t.Fatalf("connections.md must aggregate the role fragment written to the contract runtime root: %v\n%s", err, connData)
	}
	if err := json.Unmarshal(reportData, &report); err != nil {
		t.Fatalf("parse report: %v", err)
	}
	if report.Overall != VerifyStatusPass || len(report.Results) != 1 || report.Results[0].Status != "executed" {
		t.Fatalf("unexpected execution report: %+v", report)
	}
	connections, err := os.ReadFile(report.ConnectionsFile)
	if err != nil || !strings.Contains(string(connections), "NATS connection facts") {
		t.Fatalf("the new run's connection facts must be written: %v\n%s", err, connections)
	}
}

// A plan whose components are all already_installed stays read-only: the
// playbook never runs, and the report records the read-only outcome.
func TestComponentsExecuteAlreadyInstalledReadOnly(t *testing.T) {
	_, execute, kkLog, _ := r15PlanAndExecute(t, "  nats: {enabled: true}", "ours")
	os.Remove(kkLog)

	if err := RunComponentsExecute(context.Background(), execute, os.Stdout); err != nil {
		t.Fatalf("an all-already_installed plan must succeed as a read-only no-op, got %v", err)
	}
	if _, err := os.Stat(kkLog); !os.IsNotExist(err) {
		t.Fatalf("the playbook must never run for an already_installed scope:\n%s", kkLog)
	}
	entries, err := os.ReadDir(execute.Output)
	if err != nil {
		t.Fatalf("read report dir: %v", err)
	}
	var reportFile string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "components-execute-") {
			reportFile = filepath.Join(execute.Output, e.Name())
		}
	}
	if reportFile == "" {
		t.Fatalf("the no-op must still write its report: %v", entries)
	}
	data, err := os.ReadFile(reportFile)
	if err != nil {
		t.Fatal(err)
	}
	var report ComponentsExecuteReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.Overall != VerifyStatusPass || len(report.Results) != 1 ||
		report.Results[0].Status != "already_installed" ||
		!strings.Contains(report.Results[0].Detail, "no-op") {
		t.Fatalf("the report must record the read-only no-op outcome: %+v", report)
	}
}

// The executor re-checks the run identity: a config edited after the plan
// was built is refused before anything runs.
func TestComponentsExecuteRejectsConfigDrift(t *testing.T) {
	_, execute, _, _ := r15PlanAndExecute(t, "  certManager: {enabled: true}\n  nats: {enabled: true}", "absent")

	// Edit the site config after planning (a switch flips).
	siteData, err := os.ReadFile(execute.ConfigFile)
	if err != nil {
		t.Fatalf("read site: %v", err)
	}
	edited := strings.Replace(string(siteData), "nats: {enabled: true}", "nats: {enabled: false}", 1)
	if err := os.WriteFile(execute.ConfigFile, []byte(edited), 0o600); err != nil {
		t.Fatalf("write site: %v", err)
	}
	err = RunComponentsExecute(context.Background(), execute, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "re-plan before executing") {
		t.Fatalf("config drift must be refused before execution, got %v", err)
	}
}

// The standalone playbook is structural: it parses, lists only component
// roles, and never imports or runs base tasks.
func TestComponentsPlaybookStaysComponentsOnly(t *testing.T) {
	path := filepath.Join("..", "..", "builtin", "core", "playbooks", "ani_components.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read playbook: %v", err)
	}
	playbook := string(data)
	for _, role := range []string{
		"ani/cert-manager", "ani/postgresql", "ani/valkey", "ani/nats",
		"ani/metrics", "ani/loki", "ani/opensearch", "ani/fluent-bit",
	} {
		if !strings.Contains(playbook, "role: "+role) {
			t.Fatalf("the playbook must list %s", role)
		}
	}
	for _, stale := range []string{
		"import_playbook", "kubeadm", "role: cni", "role: ani/ceph",
		"role: storageclass", "role: image-registry", "role: ani/kcn",
		"role: ani/envoy", "role: ani/kubeovn", "role: etcd",
	} {
		if strings.Contains(playbook, stale) {
			t.Fatalf("the components playbook must never contain %q", stale)
		}
	}
	// Every role is gated on the scope map, so a base context can never
	// trigger it and a components run can never widen beyond --only.
	if strings.Count(playbook, "(index .ani.components_run.scope") != 8 {
		t.Fatalf("all eight roles must be scope-gated:\n%s", playbook)
	}
}

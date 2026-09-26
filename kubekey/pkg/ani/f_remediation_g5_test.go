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
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// F-remediation G5 regressions (F07 render semantics, F12 independent release
// defaults, plus the §4 small regressions T39/T40). Everything runs against
// fixture files in temp directories; no cluster, no network, no real Helm.
// ---------------------------------------------------------------------------

const g5RolesDir = "../../builtin/core/roles/ani"

// g5BaseSite is a profile=base site: the base chain ends after the network stack,
// so storage and every component are off (Validate refuses a base config that
// still enables them).
func g5BaseSite(stack string) string {
	return "name: ani-lab\n" +
		"installerNode: node1\n" +
		"profile: base\n" +
		"ssh: {user: ubuntu, port: 22, password: r06-secret-password}\n" +
		"nodes:\n" +
		"  - {name: node1, address: 192.0.2.11}\n" +
		"  - {name: node2, address: 192.0.2.12}\n" +
		"  - {name: node3, address: 192.0.2.13}\n" +
		"network:\n" +
		"  stack: " + stack + "\n" +
		"  managementInterface: ens34\n" +
		"  podCIDR: 10.16.0.0/16\n" +
		"  serviceCIDR: 10.96.0.0/16\n" +
		"  kcn: {managedDevices: [ens35], encapNetworks: [192.0.2.0/24], intranetNetworks: [192.0.2.0/24, 10.96.0.0/16]}\n" +
		"registry: {port: 5000}\n" +
		"storage:\n" +
		"  enabled: false\n" +
		"components:\n" +
		"  certManager: {enabled: false}\n"
}

func g5Table(t *testing.T, name string) ImageTable {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "ani", name))
	if err != nil {
		t.Fatalf("read images.tsv: %v", err)
	}
	table, err := LoadImageTable(strings.Split(string(raw), "\n"))
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return table
}

// T33 / F07: the render's role selection is the playbook's selection, including
// the kcn stack under profile=base, where the cluster has no data path without
// kcn, its dedicated Envoy and the smoke test.
func TestG5RenderSelectionMatchesThePlaybook(t *testing.T) {
	cases := []struct {
		name     string
		site     string
		table    string
		mustHave []string
		mustNot  []string
	}{
		{
			name:     "kcn base still deploys the kcn batch",
			site:     g5BaseSite("kcn"),
			mustHave: []string{"kcn-tasks-main.yaml", "envoy-tasks-main.yaml", "smoke-tasks-main.yaml"},
			mustNot:  []string{"ceph-tasks-main.yaml", "nats-tasks-main.yaml", "kubeovn-tasks-main.yaml"},
			table:    "images.tsv",
		},
		{
			name:     "kubeovn base deploys kube-ovn and no kcn batch",
			site:     g5BaseSite("kubeovn"),
			mustHave: []string{"kubeovn-tasks-main.yaml"},
			mustNot:  []string{"kcn-tasks-main.yaml", "envoy-tasks-main.yaml", "smoke-tasks-main.yaml", "ceph-tasks-main.yaml"},
			table:    "images-kubeovn.tsv",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cluster, err := ParseClusterConfig([]byte(tc.site))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if err := Validate(cluster); err != nil {
				t.Fatalf("validate: %v", err)
			}
			files, err := RenderSite(g5RolesDir, cluster, "/opt/ani", g5Table(t, tc.table))
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			present := map[string]bool{}
			for _, file := range files {
				present[file.Name] = true
			}
			for _, want := range tc.mustHave {
				if !present[want] {
					t.Fatalf("%s must be rendered by this selection; got %v", want, keysOf(present))
				}
			}
			for _, unwanted := range tc.mustNot {
				if present[unwanted] {
					t.Fatalf("%s must not be rendered by this selection", unwanted)
				}
			}
		})
	}
}

func keysOf(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	return out
}

// T33 / F07: the parity check itself must catch a playbook and a selection table
// that disagree — that is how a future role gate stops being invisible.
func TestG5PlaybookParityCatchesDivergence(t *testing.T) {
	cluster, err := ParseClusterConfig([]byte(r06SiteA))
	if err != nil {
		t.Fatal(err)
	}
	rolesDir := filepath.Join(t.TempDir(), "roles", "ani")
	if err := os.MkdirAll(filepath.Join(rolesDir, "metrics"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rolesDir, "..", "..", "playbooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A playbook that always runs ani/metrics, whatever the site says.
	playbook := "- hosts: [kube_control_plane]\n  roles:\n    - role: ani/metrics\n"
	if err := os.WriteFile(filepath.Join(rolesDir, "..", "..", "playbooks", "create_cluster.yaml"), []byte(playbook), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rolesDir, "..", "..", "playbooks", ComponentsPlaybookRelPath),
		[]byte("- hosts: [kube_control_plane]\n  roles:\n    - role: ani/metrics\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The site disables metrics, so the selection table says false while the
	// playbook gate says true: the render must refuse rather than show one of them.
	disabled := strings.Replace(r06SiteA, "certManager: {enabled: true}", "certManager: {enabled: true}\n  metrics: {enabled: false}", 1)
	metricsOff, err := ParseClusterConfig([]byte(disabled))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := KubeKeyConfig(metricsOff, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", g5Table(t, "images.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	err = assertPlaybookRoleSelection(rolesDir, spec, metricsOff)
	if err == nil || !strings.Contains(err.Error(), "the playbook gate evaluates true but the render's selection table says false") {
		t.Fatalf("a divergent playbook gate must be reported, got %v", err)
	}

	// A role directory that no playbook runs is a render lie as well.
	orphan := filepath.Join(t.TempDir(), "roles", "ani")
	if err := os.MkdirAll(filepath.Join(orphan, "nowhere"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(orphan, "metrics"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(orphan, "..", "..", "playbooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"create_cluster.yaml", ComponentsPlaybookRelPath} {
		if err := os.WriteFile(filepath.Join(orphan, "..", "..", "playbooks", name), []byte(playbook), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	err = assertPlaybookRoleSelection(orphan, spec, cluster)
	if err == nil || !strings.Contains(err.Error(), "run by neither playbook") {
		t.Fatalf("an unrun role directory must be reported, got %v", err)
	}
}

// T35 / F07: text classification and resource checks, both directions.
func TestG5RenderedArtifactChecksArePrecise(t *testing.T) {
	cases := map[string]struct {
		files []RenderedFile
		want  string
	}{
		"python heredoc is never validated as YAML": {
			// A python body parses as a YAML scalar: the old check called that a
			// pass. Classification must skip it as python, and the same file must
			// still be accepted.
			files: []RenderedFile{{Name: "metrics-verify.sh", Role: "metrics", Rel: "templates/verify.sh",
				Source:   []byte("python3 - <<'PY_EOF'\nx = 1\nPY_EOF\n"),
				Rendered: []byte("python3 - <<'PY_EOF'\nx = 1\nPY_EOF\n")}},
			want: "",
		},
		"a YAML heredoc that is only a scalar is refused": {
			files: []RenderedFile{{Name: "nats-tasks-main.yaml", Role: "nats", Rel: "tasks/main.yaml",
				Source:   []byte("command: |\n  cat > /tmp/values.yaml <<EOF\nEOF\n"),
				Rendered: []byte("command: |\n  cat > /tmp/values.yaml <<EOF\njust a string\nEOF\n")}},
			want: "parses only as the scalar",
		},
		"an invalid JSON heredoc is refused": {
			files: []RenderedFile{{Name: "smoke-verify.sh", Role: "smoke", Rel: "templates/verify.sh",
				Source:   []byte("cat > out.json <<'JSON'\nJSON\n"),
				Rendered: []byte("cat > out.json <<'JSON'\n{not json}\nJSON\n")}},
			want: "does not parse as JSON",
		},
		"a duplicate resource inside one file is refused": {
			files: []RenderedFile{{Name: "nats-manifests.yaml", Role: "nats", Rel: "templates/manifests.yaml",
				Rendered: []byte(manifest("v1", "Secret", "ani-platform", "ani-nats-auth") + "\n---\n" +
					manifest("v1", "Secret", "ani-platform", "ani-nats-auth"))}},
			want: "both render v1/Secret/ani-platform/ani-nats-auth",
		},
		"a duplicate across two files is refused": {
			files: []RenderedFile{
				{Name: "a-manifests.yaml", Role: "a", Rel: "templates/manifests.yaml",
					Rendered: []byte(manifest("v1", "ConfigMap", "ani-platform", "shared"))},
				{Name: "b-manifests.yaml", Role: "b", Rel: "templates/manifests.yaml",
					Rendered: []byte(manifest("v1", "ConfigMap", "ani-platform", "shared"))},
			},
			want: "a-manifests.yaml and b-manifests.yaml both render v1/ConfigMap/ani-platform/shared",
		},
		// The live render of the selected combination refused two shipped role
		// scripts that do not collide: each Job name is a shell variable the
		// script assigns itself, so the checker has to read that assignment
		// before it may call two documents the same resource.
		"a Job name each script assigns for its own component is not a duplicate": {
			files: []RenderedFile{
				{Name: "nats-verify.sh", Role: "nats", Rel: "templates/verify.sh",
					Rendered: []byte(verifyJobScript("ani-nats-ok"))},
				{Name: "postgresql-verify.sh", Role: "postgresql", Rel: "templates/verify.sh",
					Rendered: []byte(verifyJobScript("ani-pg-ok"))},
			},
			want: "",
		},
		// The same reading has to work the other way: two scripts that name
		// their Job through different variables but land on one name really do
		// collide, and only expanding the assignments can see it.
		"different variables that expand to one Job name are refused": {
			files: []RenderedFile{
				{Name: "nats-verify.sh", Role: "nats", Rel: "templates/verify.sh",
					Rendered: []byte(singleVarJobScript("NATS_JOB", "ani-verify-ok"))},
				{Name: "postgresql-verify.sh", Role: "postgresql", Rel: "templates/verify.sh",
					Rendered: []byte(singleVarJobScript("PG_JOB", "ani-verify-ok"))},
			},
			want: "both render batch/v1/Job/ani-platform/ani-verify-ok",
		},
		"a Job declared twice inside one script is still refused": {
			files: []RenderedFile{{Name: "nats-verify.sh", Role: "nats", Rel: "templates/verify.sh",
				Rendered: []byte(singleVarJobScript("JOB_OK", "ani-nats-ok") +
					"cat > /tmp/second-job.yaml <<JOB_EOF\n" +
					manifest("batch/v1", "Job", "$NS", "$JOB_OK") + "JOB_EOF\n")}},
			want: "both render batch/v1/Job/ani-platform/ani-nats-ok",
		},
		// A name the script assigns more than once has no single value to
		// resolve, so the checker keeps the identity unresolved instead of
		// picking one of the assignments.
		"a variable assigned twice is not resolved to either value": {
			files: []RenderedFile{
				{Name: "a-verify.sh", Role: "a", Rel: "templates/verify.sh",
					Rendered: []byte(singleVarJobScript("JOB_OK", "first") + singleVarJobScript("JOB_OK", "second"))},
				{Name: "b-verify.sh", Role: "b", Rel: "templates/verify.sh",
					Rendered: []byte(singleVarJobScript("JOB_OK", "first"))},
			},
			want: "a-verify.sh and a-verify.sh both render batch/v1/Job/$NS/$JOB_OK",
		},
		// A name that only exists once the script runs (per-run token) cannot be
		// proven equal across two scripts, because each run picks its own token.
		"a name that can only exist at run time is not claimed as a duplicate": {
			files: []RenderedFile{
				{Name: "nats-verify.sh", Role: "nats", Rel: "templates/verify.sh",
					Rendered: []byte(verifyJobScript("ani-nats-ok"))},
				{Name: "postgresql-verify.sh", Role: "postgresql", Rel: "templates/verify.sh",
					Rendered: []byte(verifyJobScript("ani-nats-ok"))},
			},
			want: "",
		},
		"deployed material that is not valid YAML is refused": {
			files: []RenderedFile{{Name: "loki-values.yaml", Role: "loki", Rel: "templates/values.yaml",
				Rendered: []byte("image:\n  repository: [unclosed\n")}},
			want: "deployed material does not parse as YAML",
		},
		"a document that claims to be a resource but does not parse is refused": {
			files: []RenderedFile{{Name: "nats-manifests.yaml", Role: "nats", Rel: "templates/manifests.yaml",
				Rendered: []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: broken\n bad: [indentation\n")}},
			want: "does not parse",
		},
		"an undefined value on a non-runtime line is refused": {
			files: []RenderedFile{{Name: "nats-values.yaml", Role: "nats", Rel: "templates/values.yaml",
				Source:   []byte("repository: {{ .ani.image_parts.nats.repository }}\ntag: 2.14.6\n"),
				Rendered: []byte("repository: nats\ntag: <no value>\n")}},
			want: "is not a runtime-bound field",
		},
		"a runtime-bound line stays accepted": {
			files: []RenderedFile{{Name: "ceph-tasks-main.yaml", Role: "ceph", Rel: "tasks/main.yaml",
				Source:   []byte("delegate_to: '{{ .item }}'\n"),
				Rendered: []byte("delegate_to: '<no value>'\n")}},
			want: "",
		},
		"a <no value> that cannot be traced to its source line is refused": {
			files: []RenderedFile{{Name: "kcn-tasks-main.yaml", Role: "kcn", Rel: "tasks/main.yaml",
				Source:   []byte("a: '{{ .item }}'\nb: 1\nc: 2\n"),
				Rendered: []byte("a: 1\nb: <no value>\n")}},
			want: "cannot be traced back to its template action",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateRenderedArtifacts(tc.files)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("this input is legitimate and must pass: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a problem mentioning %q, got %v", tc.want, err)
			}
		})
	}
}

func manifest(apiVersion, kind, namespace, name string) string {
	return fmt.Sprintf("apiVersion: %s\nkind: %s\nmetadata:\n  namespace: %s\n  name: %s\n",
		apiVersion, kind, namespace, name)
}

// verifyJobScript is the shape the shipped nats and postgresql verifiers use:
// the Job name comes from a variable the script assigns from a per-run token,
// so no static text can say what the object will be called.
func verifyJobScript(prefix string) string {
	return "NS=ani-platform\n" +
		"RUN_ID=\"$(date +%Y%m%d%H%M%S)-$$\"\n" +
		"JOB_OK=\"" + prefix + "-${RUN_ID}\"\n" +
		"cat > \"$OUT_DIR/ok-job.yaml\" <<JOB_EOF\n" +
		manifest("batch/v1", "Job", "$NS", "$JOB_OK") +
		"JOB_EOF\n"
}

// singleVarJobScript names the Job through one variable whose value the script
// states literally, which is what lets the checker resolve the real name.
func singleVarJobScript(varName, value string) string {
	return "NS=ani-platform\n" +
		varName + "=\"" + value + "\"\n" +
		"cat > /tmp/job.yaml <<JOB_EOF\n" +
		manifest("batch/v1", "Job", "$NS", "$"+varName) +
		"JOB_EOF\n"
}

// T34 / F07: the approved offline chart is really expanded with the artifact's
// Helm, and a broken final resource fails the render.
func TestG5ChartExpansionRunsTheApprovedChart(t *testing.T) {
	root := t.TempDir()
	spec := componentInstallSpecs["nats"]
	archive := filepath.Join(root, "charts", spec.Chart, spec.ChartVersion+".tgz")
	if err := os.MkdirAll(filepath.Join(root, "charts", spec.Chart), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, []byte("fixture chart archive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, "components.lock.yaml")
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf(
		"apiVersion: ani.installer/v1\nkind: ComponentMaterialLock\ncomponents:\n  nats:\n"+
			"    chartVersion: %s\n    name: %s\n    sha256: %s\n    artifactChartPath: charts/%s/%s.tgz\n"+
			"    release: %s\n    namespace: %s\n",
		spec.ChartVersion, spec.Chart, strings.Repeat("a", 64), spec.Chart, spec.ChartVersion, spec.Release, spec.Namespace)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMaterialsLock(lockPath); err != nil {
		t.Fatalf("the fixture lock must parse: %v", err)
	}
	cluster, err := ParseClusterConfig([]byte(strings.Replace(r06SiteA,
		"certManager: {enabled: true}", "certManager: {enabled: false}\n  nats: {enabled: true}", 1)))
	if err != nil {
		t.Fatal(err)
	}
	values := []RenderedFile{{Name: "nats-values.yaml", Role: "nats", Rel: "templates/values.yaml",
		Rendered: []byte("image:\n  repository: 127.0.0.1:5000/nats\n  tag: 2.14.6\n")}}

	helmDir := t.TempDir()
	helm := filepath.Join(helmDir, "helm")
	if err := os.WriteFile(helm, []byte(`#!/usr/bin/env bash
printf '%s\n' "$*" >> "${FAKE_HELM_LOG:?}"
case "$*" in
  *"--values "*) [ -s "$(echo "$*" | sed -n 's/.*--values \([^ ]*\).*/\1/p')" ] || { echo "values file is empty" >&2; exit 3; } ;;
esac
case "${FAKE_HELM_MODE:-ok}" in
  fail) echo "chart submission failed" >&2; exit 1 ;;
  empty) exit 0 ;;
  novalue) printf -- '---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: nats\n  token: <no value>\n' ;;
  noimage) printf -- '---\napiVersion: apps/v1\nkind: StatefulSet\nmetadata:\n  name: nats\n  repository: ""\n' ;;
  *) printf -- '---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: nats\n---\napiVersion: apps/v1\nkind: StatefulSet\nmetadata:\n  name: nats\n' ;;
esac
exit 0
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_HELM_LOG", filepath.Join(helmDir, "helm-calls.log"))

	input := ChartExpansionInput{HelmBin: helm, ChartsRoot: root, LockPath: lockPath}
	var log bytes.Buffer
	if err := RunChartExpansion(context.Background(), input, cluster, values, &log); err != nil {
		t.Fatalf("a chart that expands cleanly must pass: %v\n%s", err, log.String())
	}
	calls, err := os.ReadFile(filepath.Join(helmDir, "helm-calls.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"template " + spec.Release, archive, "--namespace " + spec.Namespace, "--values"} {
		if !strings.Contains(string(calls), want) {
			t.Fatalf("the Helm invocation must carry %q:\n%s", want, calls)
		}
	}
	if !strings.Contains(log.String(), "resource(s) rendered") {
		t.Fatalf("the expansion must report what it rendered:\n%s", log.String())
	}

	for _, mode := range []struct{ name, mode, want string }{
		{"helm failure", "fail", "chart submission failed"},
		{"no resources", "empty", "rendered no Kubernetes resources"},
		{"unresolved value", "novalue", "carries an unresolved value"},
	} {
		t.Setenv("FAKE_HELM_MODE", mode.mode)
		err := RunChartExpansion(context.Background(), input, cluster, values, &log)
		if err == nil || !strings.Contains(err.Error(), mode.want) {
			t.Fatalf("%s must fail mentioning %q, got %v", mode.name, mode.want, err)
		}
	}
	t.Setenv("FAKE_HELM_MODE", "")

	// A selection with no chart-backed component must not need Helm at all: the
	// render of a base profile has nothing to expand, and demanding a Helm binary
	// there would be a new false precondition.
	base, err := ParseClusterConfig([]byte(g5BaseSite("kcn")))
	if err != nil {
		t.Fatalf("parse base site: %v", err)
	}
	var quiet bytes.Buffer
	if err := RunChartExpansion(context.Background(), ChartExpansionInput{
		HelmBin: filepath.Join(root, "no-such-helm"), ChartsRoot: root, LockPath: lockPath,
	}, base, values, &quiet); err != nil {
		t.Fatalf("a chart-free selection must not require Helm: %v\n%s", err, quiet.String())
	}
	if !strings.Contains(quiet.String(), "nothing to do") {
		t.Fatalf("the expansion must say it had nothing to expand:\n%s", quiet.String())
	}

	// An unapproved chart, a missing archive and a PATH-free binary check.
	noLock := filepath.Join(t.TempDir(), "lock.yaml")
	if err := os.WriteFile(noLock, []byte("apiVersion: ani.installer/v1\nkind: ComponentMaterialLock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = RunChartExpansion(context.Background(), ChartExpansionInput{
		HelmBin: helm, ChartsRoot: root, LockPath: noLock,
	}, cluster, values, &log)
	if err == nil || !strings.Contains(err.Error(), "approves no chart") {
		t.Fatalf("a chart with no lock entry must be refused, got %v", err)
	}
	err = RunChartExpansion(context.Background(), ChartExpansionInput{
		HelmBin: helm, ChartsRoot: t.TempDir(), LockPath: lockPath,
	}, cluster, values, &log)
	if err == nil || !strings.Contains(err.Error(), "is not present under") {
		t.Fatalf("a missing chart archive must be refused, got %v", err)
	}
	err = RunChartExpansion(context.Background(), ChartExpansionInput{
		HelmBin: filepath.Join(root, "no-helm"), ChartsRoot: root, LockPath: lockPath,
	}, cluster, values, &log)
	if err == nil || !strings.Contains(err.Error(), "not an executable file") {
		t.Fatalf("a missing Helm binary must be refused, never resolved from PATH, got %v", err)
	}
}

// T36 / F12: the render's roles source is the material this release carries; only
// an explicit --roles-dir overrides it, and a build with nothing to show says so.
func TestG5RolesSourceResolution(t *testing.T) {
	// An explicit directory is honoured exactly.
	explicit := filepath.Join(t.TempDir(), "my-roles")
	if err := os.MkdirAll(explicit, 0o700); err != nil {
		t.Fatal(err)
	}
	got, cleanup, err := resolveRolesDir(explicit, "/does/not/matter")
	if err != nil {
		t.Fatalf("an explicit --roles-dir must be accepted: %v", err)
	}
	if got != explicit {
		t.Fatalf("the explicit directory must be used verbatim, got %s", got)
	}
	cleanup()

	if _, _, err := resolveRolesDir(filepath.Join(t.TempDir(), "missing"), ""); err == nil ||
		!strings.Contains(err.Error(), "is not usable") {
		t.Fatalf("a missing explicit directory must be refused, got %v", err)
	}

	// The development layout still works.
	root := t.TempDir()
	tree := filepath.Join(root, "builtin", "core", "roles", "ani")
	if err := os.MkdirAll(tree, 0o700); err != nil {
		t.Fatal(err)
	}
	got, cleanup, err = resolveRolesDir("", root)
	if err != nil || got != tree {
		t.Fatalf("the package-root tree must be used: %v (%s)", err, got)
	}
	cleanup()

	// No tree at all and no embedded project: an explicit refusal.
	Restore := componentsProjectMaterialize
	componentsProjectMaterialize = nil
	defer func() { componentsProjectMaterialize = Restore }()
	empty := t.TempDir()
	if _, _, err := resolveRolesDir("", empty); err == nil ||
		!strings.Contains(err.Error(), "built without -tags builtin") {
		t.Fatalf("a build with no roles must say why, got %v", err)
	}

	// With the embedded project available, the render materializes it and cleans
	// the temporary copy up afterwards.
	componentsProjectMaterialize = func(projectRoot string) error {
		path := filepath.Join(projectRoot, "builtin", "core", "roles", "ani", "metrics", "tasks", "main.yaml")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("- name: embedded\n"), 0o400)
	}
	got, cleanup, err = resolveRolesDir("", empty)
	if err != nil {
		t.Fatalf("the embedded tree must be usable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(got, "metrics", "tasks", "main.yaml")); err != nil {
		t.Fatalf("the materialized role tree must exist: %v", err)
	}
	cleanup()
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Fatalf("the materialized tree must be cleaned up, stat said %v", err)
	}
}

// T39 / §4: the shared IPv4 contract before any CNI-specific logic, without
// forbidding the legal kcn containment relationships.
func TestG5SharedIPv4NetworkContract(t *testing.T) {
	cases := []struct {
		name   string
		pod    string
		svc    string
		wantIn string
	}{
		{"ipv6 pod network", "fd00:10:16::/56", "10.96.0.0/16", "not an IPv4 network"},
		{"ipv6 service network", "10.16.0.0/16", "fd00:10:96::/112", "not an IPv4 network"},
		{"non-canonical pod network", "10.16.1.5/16", "10.96.0.0/16", "not the canonical network address"},
		{"identical networks", "10.16.0.0/16", "10.16.0.0/16", "overlaps"},
		{"service inside pod", "10.16.0.0/16", "10.16.128.0/17", "overlaps"},
		{"legal split", "10.16.0.0/16", "10.96.0.0/16", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			site := strings.Replace(r06SiteA, "podCIDR: 10.16.0.0/16", "podCIDR: "+tc.pod, 1)
			site = strings.Replace(site, "serviceCIDR: 10.96.0.0/16", "serviceCIDR: "+tc.svc, 1)
			cluster, err := ParseClusterConfig([]byte(site))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			err = Validate(cluster)
			if tc.wantIn == "" {
				if err != nil {
					t.Fatalf("a legal IPv4 split must validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("want a refusal mentioning %q, got %v", tc.wantIn, err)
			}
		})
	}

	// The kcn contract stops at format, family and coverage: an intranet entry
	// that is a SUPERNETWORK of an encap network covers it (the documented legal
	// layout), while an encap network no entry covers is still refused.
	contained := strings.Replace(r06SiteA,
		"intranetNetworks: [192.0.2.0/24, 10.96.0.0/16]",
		"intranetNetworks: [192.0.2.0/23, 10.96.0.0/12]", 1)
	cluster, err := ParseClusterConfig([]byte(contained))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(cluster); err != nil {
		t.Fatalf("a supernetwork intranet must be accepted: %v", err)
	}
	uncovered := strings.Replace(r06SiteA,
		"encapNetworks: [192.0.2.0/24]", "encapNetworks: [203.0.113.0/24]", 1)
	uncoveredCluster, err := ParseClusterConfig([]byte(uncovered))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	err = Validate(uncoveredCluster)
	if err == nil || !strings.Contains(err.Error(), "no network.kcn.intranetNetworks entry covers") {
		t.Fatalf("an encap network no intranet entry covers must be refused, got %v", err)
	}
	small := strings.Replace(r06SiteA, "encapNetworks: [192.0.2.0/24]", "encapNetworks: [192.0.2.0/31]", 1)
	small = strings.Replace(small, "intranetNetworks: [192.0.2.0/24, 10.96.0.0/16]", "intranetNetworks: [192.0.2.0/31]", 1)
	smallCluster, err := ParseClusterConfig([]byte(small))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(smallCluster); err != nil {
		t.Fatalf("a legal kcn encap network must still validate: %v", err)
	}
}

// T40 / §4: an explicit Kube-OVN gateway must reject both the network address and
// the broadcast address, and keep a legal non-default gateway working.
func TestG5KubeOVNGatewayAddresses(t *testing.T) {
	base := strings.Replace(r06SiteA, "stack: kcn", "stack: kubeovn", 1)
	cases := []struct {
		name    string
		gateway string
		wantIn  string
	}{
		{"network address", "10.16.0.0", "network address"},
		{"broadcast address", "10.16.255.255", "broadcast address"},
		{"legal non-default gateway", "10.16.0.1", ""},
		{"outside the pod network", "10.20.0.1", "outside the pod network"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			site := strings.Replace(base, "podCIDR: 10.16.0.0/16", "podCIDR: 10.16.0.0/16", 1)
			if tc.gateway == "" {
				site = strings.Replace(site, "  kcn: {", "  kubeovn: {}\n  kcn: {", 1)
			} else {
				site = strings.Replace(site, "  kcn: {", fmt.Sprintf("  kubeovn: {defaultGateway: %s}\n  kcn: {", tc.gateway), 1)
			}
			cluster, err := ParseClusterConfig([]byte(site))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			err = Validate(cluster)
			if tc.wantIn == "" {
				if err != nil {
					t.Fatalf("this configuration is legal and must validate: %v", err)
				}
				resolved, err := resolveKubeOVNNetwork(cluster)
				if err != nil {
					t.Fatalf("resolve: %v", err)
				}
				if resolved.DefaultGateway != tc.gateway {
					t.Fatalf("the explicit gateway must survive: got %s", resolved.DefaultGateway)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("want a refusal mentioning %q, got %v", tc.wantIn, err)
			}
		})
	}
}

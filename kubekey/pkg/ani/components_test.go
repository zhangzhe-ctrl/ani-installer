package ani

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

const siteConfigTemplate = `name: ani-lab
installerNode: node1
ssh:
  user: ubuntu
  port: 22
  password: site-password
nodes:
  - name: node1
    address: 192.0.2.11
  - name: node2
    address: 192.0.2.12
  - name: node3
    address: 192.0.2.13
network:
  managementInterface: ens34
  podCIDR: 10.16.0.0/16
  serviceCIDR: 10.96.0.0/16
  kcn:
    managedDevices: [ens35]
    encapNetworks: [192.0.2.0/24]
    intranetNetworks: [192.0.2.0/24, 10.96.0.0/16]
registry:
  port: 5000
%s
`

func parseSite(t *testing.T, components string) (ClusterConfig, error) {
	t.Helper()
	return ParseClusterConfig([]byte(strings.ReplaceAll(siteConfigTemplate, "%s", components)))
}

func TestComponentsDefaultOffAndStorageDefaults(t *testing.T) {
	c, err := parseSite(t, "")
	if err != nil {
		t.Fatalf("parse without components: %v", err)
	}
	if err := Validate(c); err != nil {
		t.Fatalf("Validate() without components = %v", err)
	}
	for _, row := range c.Components.Selection() {
		if row.Enabled {
			t.Fatalf("component %s must default to false", row.Name)
		}
	}
	if c.Components.PostgreSQL.StorageClass != DefaultStorageClass || c.Components.PostgreSQL.StorageSize != "10Gi" {
		t.Fatalf("postgresql defaults = %q/%q", c.Components.PostgreSQL.StorageClass, c.Components.PostgreSQL.StorageSize)
	}
	if c.Components.Valkey.StorageSize != "2Gi" || c.Components.NATS.StorageSize != "5Gi" {
		t.Fatalf("valkey/nats defaults = %q/%q", c.Components.Valkey.StorageSize, c.Components.NATS.StorageSize)
	}
}

func TestComponentsSelectionKeepsDocumentedOrder(t *testing.T) {
	c, err := parseSite(t, "components:\n  certManager:\n    enabled: true\n")
	if err != nil {
		t.Fatalf("parse with certManager: %v", err)
	}
	rows := c.Components.Selection()
	want := [][2]string{
		{"cert-manager", "true"},
		{"postgresql", "false"},
		{"valkey", "false"},
		{"nats", "false"},
		{"metrics", "false"},
		{"loki", "false"},
		{"opensearch", "false"},
		{"fluent-bit", "false"},
	}
	if len(rows) != len(want) {
		t.Fatalf("selection length = %d, want %d", len(rows), len(want))
	}
	for i, row := range rows {
		if row.Name != want[i][0] {
			t.Fatalf("selection[%d].Name = %q, want %q", i, row.Name, want[i][0])
		}
		got := "false"
		if row.Enabled {
			got = "true"
		}
		if got != want[i][1] {
			t.Fatalf("selection[%d].Enabled = %s, want %s", i, got, want[i][1])
		}
	}
}

func TestComponentsUnknownKeyRejected(t *testing.T) {
	if _, err := parseSite(t, "components:\n  certManager:\n    enabled: true\n    typoKey: 1\n"); err == nil {
		t.Fatal("unknown component key was accepted")
	}
	if _, err := parseSite(t, "components:\n  redis:\n    enabled: true\n"); err == nil {
		t.Fatal("unknown component name was accepted")
	}
}

// selectionEnabled reports the effective selection row for a component name.
func selectionEnabled(c ClusterConfig, name string) bool {
	for _, row := range c.Components.Selection() {
		if row.Name == name {
			return row.Enabled
		}
	}
	return false
}

func TestMetricsDefaultsAndValidation(t *testing.T) {
	c, err := parseSite(t, "")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m := c.Components.Metrics
	if m.Enabled {
		t.Fatal("metrics must default to disabled")
	}
	if m.StorageClass != DefaultStorageClass || m.PrometheusStorageSize != "5Gi" ||
		m.AlertmanagerStorageSize != "1Gi" || m.PrometheusRetention != "24h" {
		t.Fatalf("metrics defaults = %+v", m)
	}

	bad := []struct {
		name string
		site string
	}{
		{"zero prometheus size", "components:\n  metrics:\n    enabled: true\n    prometheusStorageSize: 0Gi\n"},
		{"negative alertmanager size", "components:\n  metrics:\n    enabled: true\n    alertmanagerStorageSize: -1Gi\n"},
		{"unparsable size", "components:\n  metrics:\n    enabled: true\n    prometheusStorageSize: lots\n"},
		{"empty storage class", "components:\n  metrics:\n    enabled: true\n    storageClass: \"\"\n"},
		{"retention without unit", "components:\n  metrics:\n    enabled: true\n    prometheusRetention: 24\n"},
		{"retention zero", "components:\n  metrics:\n    enabled: true\n    prometheusRetention: 0h\n"},
		{"retention unsupported unit", "components:\n  metrics:\n    enabled: true\n    prometheusRetention: 30m\n"},
	}
	for _, tc := range bad {
		c, err := parseSite(t, tc.site)
		if err != nil {
			t.Fatalf("%s: parse: %v", tc.name, err)
		}
		// metrics is not implemented yet, so a valid config also fails; assert on
		// the field error instead of the not-implemented gate.
		err = c.Components.validateMetrics()
		if err == nil {
			t.Fatalf("%s: validateMetrics accepted an invalid value", tc.name)
		}
	}

	for _, ok := range []string{"24h", "7d", "168h"} {
		c, err := parseSite(t, "components:\n  metrics:\n    enabled: true\n    prometheusRetention: "+ok+"\n")
		if err != nil {
			t.Fatalf("parse retention %s: %v", ok, err)
		}
		if err := c.Components.validateMetrics(); err != nil {
			t.Fatalf("validateMetrics rejected %s: %v", ok, err)
		}
	}
}

func TestLoggingBackendSelectionAndMutualExclusion(t *testing.T) {
	// default: none, collector not deployed
	c, err := parseSite(t, "")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Components.Logging.LogBackend() != loggingNone {
		t.Fatalf("default backend = %q, want none", c.Components.Logging.LogBackend())
	}
	if selectionEnabled(c, "fluent-bit") {
		t.Fatal("fluent-bit must not be selected when logging backend is none")
	}

	// loki: loki + fluent-bit on, opensearch off
	c, err = parseSite(t, "components:\n  logging:\n    backend: loki\n")
	if err != nil {
		t.Fatalf("parse loki: %v", err)
	}
	for name, want := range map[string]bool{"loki": true, "opensearch": false, "fluent-bit": true} {
		if got := selectionEnabled(c, name); got != want {
			t.Fatalf("backend=loki selection[%s] = %v, want %v", name, got, want)
		}
	}

	// opensearch requires cert-manager; without it validation must fail loudly
	c, err = parseSite(t, "components:\n  logging:\n    backend: opensearch\n")
	if err != nil {
		t.Fatalf("parse opensearch: %v", err)
	}
	err = c.Components.validateLogging()
	if err == nil || !strings.Contains(err.Error(), "certManager") {
		t.Fatalf("validateLogging error = %v, want an explicit certManager dependency error", err)
	}
	// with cert-manager the dependency is satisfied
	c, err = parseSite(t, "components:\n  certManager:\n    enabled: true\n  logging:\n    backend: opensearch\n")
	if err != nil {
		t.Fatalf("parse opensearch+certManager: %v", err)
	}
	if err := c.Components.validateLogging(); err != nil {
		t.Fatalf("validateLogging with certManager = %v", err)
	}
	for name, want := range map[string]bool{"opensearch": true, "loki": false, "fluent-bit": true} {
		if got := selectionEnabled(c, name); got != want {
			t.Fatalf("backend=opensearch selection[%s] = %v, want %v", name, got, want)
		}
	}

	// unknown backend must be rejected, and an empty backend means none
	c, err = parseSite(t, "components:\n  logging:\n    backend: elasticsearch\n")
	if err != nil {
		t.Fatalf("parse unknown backend: %v", err)
	}
	if err := c.Components.validateLogging(); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unknown backend error = %v, want a not-supported message", err)
	}
	c, err = parseSite(t, "components:\n  logging:\n    backend: \"\"\n")
	if err != nil {
		t.Fatalf("parse empty backend: %v", err)
	}
	if c.Components.Logging.LogBackend() != loggingNone {
		t.Fatalf("empty backend normalised to %q, want none", c.Components.Logging.LogBackend())
	}
	if err := c.Components.validateLogging(); err != nil {
		t.Fatalf("empty backend rejected: %v", err)
	}

	// log retention is a plain positive day count: a unit suffix is a typo
	for _, badDays := range []string{"0", "-1", "3d", "3h", "three"} {
		c, err := parseSite(t, "components:\n  logging:\n    backend: loki\n    retentionDays: \""+badDays+"\"\n")
		if err != nil {
			t.Fatalf("parse retentionDays %q: %v", badDays, err)
		}
		if err := c.Components.validateLogging(); err == nil {
			t.Fatalf("validateLogging accepted retentionDays %q", badDays)
		}
	}
	// the documented default form "3" is valid
	c, err = parseSite(t, "components:\n  logging:\n    backend: loki\n    retentionDays: \"3\"\n")
	if err != nil {
		t.Fatalf("parse default retentionDays: %v", err)
	}
	if err := c.Components.validateLogging(); err != nil {
		t.Fatalf("validateLogging rejected the documented day count: %v", err)
	}
}

func TestKubeKeyConfigCarriesMetricsAndLogging(t *testing.T) {
	c, err := parseSite(t, "components:\n  metrics:\n    enabled: true\n    prometheusRetention: 48h\n  logging:\n    backend: loki\n    retentionDays: 5\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// The observability rows are not deployable in this card, so build the spec
	// map directly: this asserts the exact keys a role will read.
	components := componentSpec(c.Components, c.Name)
	metrics, ok := components["metrics"].(map[string]any)
	if !ok {
		t.Fatalf("components.metrics missing")
	}
	if metrics["enabled"] != true || metrics["prometheus_retention"] != "48h" ||
		metrics["prometheus_storage_size"] != "5Gi" || metrics["alertmanager_storage_size"] != "1Gi" {
		t.Fatalf("metrics entry = %+v", metrics)
	}
	logging, ok := components["logging"].(map[string]any)
	if !ok {
		t.Fatalf("components.logging missing")
	}
	if logging["backend"] != "loki" || logging["enabled"] != true || logging["retention_days"] != "5" {
		t.Fatalf("logging entry = %+v", logging)
	}
	// Disabled metrics must still carry its fields so a role can render them.
	c, err = parseSite(t, "")
	if err != nil {
		t.Fatalf("parse default: %v", err)
	}
	components = componentSpec(c.Components, c.Name)
	metrics = components["metrics"].(map[string]any)
	if metrics["enabled"] != false || metrics["prometheus_retention"] != "24h" ||
		metrics["prometheus_storage_size"] != "5Gi" || metrics["alertmanager_storage_size"] != "1Gi" {
		t.Fatalf("disabled metrics entry = %+v", metrics)
	}
	logging = components["logging"].(map[string]any)
	if logging["enabled"] != false || logging["backend"] != "none" ||
		logging["storage_size"] != "5Gi" || logging["retention_days"] != "3" {
		t.Fatalf("disabled logging entry = %+v", logging)
	}
}

func TestEnabledComponentRequiresUsableCapacity(t *testing.T) {
	c, err := parseSite(t, "components:\n  nats:\n    enabled: true\n    storageSize: maybe\n")
	if err != nil {
		t.Fatalf("parse bad size: %v", err)
	}
	if err := Validate(c); err == nil {
		t.Fatal("Validate() accepted an unparsable storage size")
	}
	c, err = parseSite(t, "components:\n  postgresql:\n    enabled: true\n    storageClass: \"\"\n")
	if err != nil {
		t.Fatalf("parse empty class: %v", err)
	}
	if err := Validate(c); err == nil {
		t.Fatal("Validate() accepted an empty storageClass for an enabled component")
	}
	c, err = parseSite(t, "components:\n  certManager:\n    enabled: true\n")
	if err != nil {
		t.Fatalf("parse certManager: %v", err)
	}
	if err := Validate(c); err != nil {
		t.Fatalf("Validate() = %v for the implemented component", err)
	}
}

func TestKubeKeyConfigCarriesComponentSelection(t *testing.T) {
	c, err := parseSite(t, "components:\n  certManager:\n    enabled: true\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	spec, err := KubeKeyConfig(c, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", testImageTable())
	if err != nil {
		t.Fatalf("KubeKeyConfig() error = %v", err)
	}
	specMap, ok := spec["ani"].(map[string]any)
	if !ok {
		t.Fatalf("spec has no ani section: %#v", spec["ani"])
	}
	if specMap["artifact_root"] != "/opt/ani" {
		t.Fatalf("artifact_root = %v, want /opt/ani", specMap["artifact_root"])
	}
	components, ok := specMap["components"].(map[string]any)
	if !ok {
		t.Fatalf("spec has no ani.components: %#v", specMap)
	}
	for _, name := range []string{"cert-manager", "postgresql", "valkey", "nats"} {
		entry, ok := components[name].(map[string]any)
		if !ok {
			t.Fatalf("ani.components[%q] missing", name)
		}
		if _, ok := entry["enabled"].(bool); !ok {
			t.Fatalf("ani.components[%q].enabled is not a bool", name)
		}
	}
	selected := components["cert-manager"].(map[string]any)["enabled"]
	if selected != true {
		t.Fatalf("cert-manager selection = %v, want true", selected)
	}
	postgresql := components["postgresql"].(map[string]any)
	if postgresql["storage_class"] != DefaultStorageClass || postgresql["storage_size"] != "10Gi" {
		t.Fatalf("postgresql storage passed to roles = %v/%v", postgresql["storage_class"], postgresql["storage_size"])
	}
}

func TestWriteComponentSelectionIsStrictlyFormatted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "components-selection.tsv")
	c, err := parseSite(t, "components:\n  certManager:\n    enabled: true\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := writeComponentSelection(path, "abc123", c.Components.Selection()); err != nil {
		t.Fatalf("writeComponentSelection: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("selection file mode = %o, want 0600", perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := "# config_sha256=abc123\n" +
		"cert-manager\ttrue\npostgresql\tfalse\nvalkey\tfalse\nnats\tfalse\n" +
		"metrics\tfalse\nloki\tfalse\nopensearch\tfalse\nfluent-bit\tfalse\n"
	if string(data) != want {
		t.Fatalf("selection file = %q, want %q", string(data), want)
	}
}

// Every component verification script survives rendering and is valid bash, so a
// template typo cannot reach a node as a half-rendered script.
func TestComponentVerifyScriptsRenderAndParse(t *testing.T) {
	root := filepath.Join("..", "..", "builtin", "core", "roles", "ani")
	matches, err := filepath.Glob(filepath.Join(root, "*", "templates", "verify.sh"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no component verify.sh templates found")
	}
	ctx := map[string]any{
		"ani": map[string]any{
			"registry": "192.0.2.11:5000",
			"images": map[string]string{
				"docker.io/alpine/openssl:3.5.4":                      "192.0.2.11:5000/alpine/openssl:3.5.4",
				"docker.io/library/python:3.13.11-alpine3.23":         "192.0.2.11:5000/library/python:3.13.11-alpine3.23",
				"quay.io/prometheus/prometheus:v3.11.3-distroless":    "192.0.2.11:5000/prometheus/prometheus:v3.11.3-distroless",
				"quay.io/prometheus/alertmanager:v0.32.1":             "192.0.2.11:5000/prometheus/alertmanager:v0.32.1",
				"quay.io/prometheus/node-exporter:v1.11.1-distroless": "192.0.2.11:5000/prometheus/node-exporter:v1.11.1-distroless",
			},
			"components": map[string]any{
				"cert-manager": map[string]any{"enabled": true},
				"metrics": map[string]any{
					"enabled":       true,
					"namespace":     "ani-observability",
					"run_id":        "ani-ani-lab",
					"storage_class": "ani-block",
				},
			},
		},
		"kubernetes": map[string]any{"cluster_name": "ani-lab"},
	}
	for _, path := range matches {
		t.Run(filepath.Base(filepath.Dir(filepath.Dir(path))), func(t *testing.T) {
			tmpl, err := template.New("verify.sh").ParseFiles(path)
			if err != nil {
				t.Fatalf("parse template: %v", err)
			}
			rendered := &strings.Builder{}
			if err := tmpl.Execute(rendered, ctx); err != nil {
				t.Fatalf("execute template: %v", err)
			}
			if strings.Contains(rendered.String(), "{{") {
				t.Fatalf("rendered script still contains template delimiters:\n%s", rendered.String())
			}
			out := filepath.Join(t.TempDir(), "verify.sh")
			if err := os.WriteFile(out, []byte(rendered.String()), 0o700); err != nil {
				t.Fatalf("write rendered script: %v", err)
			}
			cmd := exec.Command("bash", "-n", out)
			if combined, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("bash -n rendered script: %v\n%s", err, combined)
			}
		})
	}
}

// The Chart values template renders to valid YAML with every image rewritten to
// the offline registry, so no public image reference can leak into the release.
func TestCertManagerValuesRenderOfflineReferences(t *testing.T) {
	path := filepath.Join("..", "..", "builtin", "core", "roles", "ani", "cert-manager", "templates", "values.yaml")
	tmpl, err := template.New("values.yaml").ParseFiles(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ctx := map[string]any{"ani": map[string]any{"registry": "192.0.2.11:5000"}}
	rendered := &strings.Builder{}
	if err := tmpl.Execute(rendered, ctx); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := rendered.String()
	for _, want := range []string{"crds:", "enabled: true", "imageRegistry: 192.0.2.11:5000", "imageNamespace: jetstack", "clusterResourceNamespace: cert-manager"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered values missing %q:\n%s", want, out)
		}
	}
	for _, public := range []string{"quay.io", "docker.io", "https://"} {
		if strings.Contains(out, public) {
			t.Fatalf("rendered values references public source %q:\n%s", public, out)
		}
	}
}

func TestCertManagerRoleTasksAreGatedAndUsePackagedMaterial(t *testing.T) {
	root := filepath.Join("..", "..")
	tasksData, err := os.ReadFile(filepath.Join(root, "builtin", "core", "roles", "ani", "cert-manager", "tasks", "main.yaml"))
	if err != nil {
		t.Fatalf("read tasks: %v", err)
	}
	tasks := string(tasksData)
	if !strings.Contains(tasks, "{{ .ani.artifact_root }}/charts/cert-manager/v1.21.2.tgz") {
		t.Fatal("role must install the chart shipped inside the offline artifact")
	}
	if !strings.Contains(tasks, "{{ .ani.artifact_root }}/bin/helm") {
		t.Fatal("role must use the packaged helm binary")
	}
	// kubectl jsonpath renders nothing for a JSON array of plain strings when
	// "{.}" is used, which silently turned the image-source check into a no-op.
	if strings.Contains(tasks, `range .args[*]}{.}`) {
		t.Fatal("role iterates container args with {.}; use {@} for string arrays")
	}
	// The template module does not honour its "mode" argument as an octal file
	// mode, so the verification script permissions must be set by an explicit
	// task instead.
	if strings.Contains(tasks, "mode: \"0700\"") {
		t.Fatal("role relies on the template module mode; set 0700 with chmod instead")
	}
	if !strings.Contains(tasks, "chmod 0700 /etc/kubernetes/ani/cert-manager/verify.sh") {
		t.Fatal("rendered verification script must be explicitly chmod 0700")
	}
	for _, forbidden := range []string{"helm repo update", "helm dependency update", "reset.sh", "kubeadm reset"} {
		if strings.Contains(tasks, forbidden) {
			t.Fatalf("role contains forbidden operation %q", forbidden)
		}
	}
	playbookData, err := os.ReadFile(filepath.Join(root, "builtin", "core", "playbooks", "create_cluster.yaml"))
	if err != nil {
		t.Fatalf("read playbook: %v", err)
	}
	playbook := string(playbookData)
	if !strings.Contains(playbook, "role: ani/cert-manager") {
		t.Fatal("cert-manager role is not wired into create_cluster.yaml")
	}
	if !strings.Contains(playbook, `(index .ani.components "cert-manager").enabled`) {
		t.Fatal("cert-manager role is not gated by its component switch")
	}
	if strings.Index(playbook, "ani/ceph") > strings.Index(playbook, "ani/cert-manager") {
		t.Fatal("cert-manager must run after the base storage role")
	}
}

// TestLegacySiteConfigKeepsEveryNewSwitchOff proves the compatibility promise
// in the plan: a site file written for the foundation batch still validates
// after the two new typed blocks exist, and none of the observability rows
// becomes enabled implicitly.
func TestLegacySiteConfigKeepsEveryNewSwitchOff(t *testing.T) {
	legacy := `name: ani-lab
installerNode: node1
ssh: {user: ubuntu, port: 22, password: pw}
nodes:
  - {name: node1, address: 192.0.2.11}
  - {name: node2, address: 192.0.2.12}
  - {name: node3, address: 192.0.2.13}
network:
  managementInterface: ens34
  podCIDR: 10.16.0.0/16
  serviceCIDR: 10.96.0.0/16
  kcn: {managedDevices: [ens35], encapNetworks: [192.0.2.0/24], intranetNetworks: [192.0.2.0/24, 10.96.0.0/16]}
registry: {port: 5000}
components:
  certManager: {enabled: true}
  postgresql: {enabled: true, storageClass: ani-block, storageSize: 10Gi}
  valkey: {enabled: true, storageClass: ani-block, storageSize: 2Gi}
  nats: {enabled: true, storageClass: ani-block, storageSize: 5Gi}
`
	c, err := ParseClusterConfig([]byte(legacy))
	if err != nil {
		t.Fatalf("parse legacy site config: %v", err)
	}
	if err := Validate(c); err != nil {
		t.Fatalf("legacy site config must still validate: %v", err)
	}
	rows := c.Components.Selection()
	if len(rows) != 8 {
		t.Fatalf("selection rows = %d, want 8", len(rows))
	}
	for _, row := range rows[:4] {
		if !row.Enabled {
			t.Fatalf("foundation row %s must stay enabled", row.Name)
		}
	}
	for _, row := range rows[4:] {
		if row.Enabled {
			t.Fatalf("observability row %s must default to disabled for a legacy config", row.Name)
		}
	}
	if c.Components.Metrics.Enabled || c.Components.Metrics.StorageClass != DefaultStorageClass {
		t.Fatalf("metrics must stay disabled with the default storage class, got %+v", c.Components.Metrics)
	}
	if c.Components.Metrics.PrometheusStorageSize != "5Gi" || c.Components.Metrics.PrometheusRetention != "24h" {
		t.Fatalf("metrics must keep its documented defaults, got %+v", c.Components.Metrics)
	}
	if c.Components.Logging.LogBackend() != loggingNone {
		t.Fatalf("logging backend = %q, want none", c.Components.Logging.LogBackend())
	}
	if c.Components.Logging.RetentionDays != "3" || c.Components.Logging.StorageSize != "5Gi" {
		t.Fatalf("logging must keep its documented defaults, got %+v", c.Components.Logging)
	}
}

// TestTwoLogBackendsCannotBeExpressed proves the mutual exclusion is structural
// rather than a check that could be forgotten: logging has exactly one backend
// key, so a site file cannot name two backends, and an unknown key inside the
// block is rejected by strict decoding.
func TestTwoLogBackendsCannotBeExpressed(t *testing.T) {
	twoBackends := `name: ani-lab
installerNode: node1
ssh: {user: ubuntu, port: 22, password: pw}
nodes:
  - {name: node1, address: 192.0.2.11}
  - {name: node2, address: 192.0.2.12}
  - {name: node3, address: 192.0.2.13}
network:
  managementInterface: ens34
  podCIDR: 10.16.0.0/16
  serviceCIDR: 10.96.0.0/16
  kcn: {managedDevices: [ens35], encapNetworks: [192.0.2.0/24], intranetNetworks: [192.0.2.0/24, 10.96.0.0/16]}
registry: {port: 5000}
components:
  logging:
    backend: loki
    loki: {enabled: true}
    opensearch: {enabled: true}
`
	if _, err := ParseClusterConfig([]byte(twoBackends)); err == nil {
		t.Fatal("a second backend key inside components.logging must be rejected as an unknown field")
	}

	// A single backend string can never turn both rows on.
	c, err := parseSite(t, "components:\n  logging:\n    backend: opensearch\n")
	if err != nil {
		t.Fatalf("parse opensearch: %v", err)
	}
	loki, opensearch := selectionEnabled(c, "loki"), selectionEnabled(c, "opensearch")
	if loki && opensearch {
		t.Fatal("both log backends were selected from one backend value")
	}
	if !opensearch || loki {
		t.Fatalf("backend=opensearch selected loki=%v opensearch=%v", loki, opensearch)
	}
}

// TestConnectionFragmentsRenderSiteValues renders each batch-1 connection
// fragment with a full context and asserts the result is a usable document:
// no leftover template syntax, no empty rendering, and the configured storage
// values actually substituted.
func TestConnectionFragmentsRenderSiteValues(t *testing.T) {
	root := filepath.Join("..", "..", "builtin", "core", "roles", "ani")
	ctx := map[string]any{
		"ani": map[string]any{
			"registry": "192.0.2.11:5000",
			"components": map[string]any{
				"postgresql": map[string]any{"enabled": true, "storage_class": "ani-block", "storage_size": "10Gi"},
				"valkey":     map[string]any{"enabled": true, "storage_class": "ani-block", "storage_size": "2Gi"},
				"nats":       map[string]any{"enabled": true, "storage_class": "ani-block", "storage_size": "5Gi"},
				"metrics": map[string]any{
					"enabled":                   true,
					"namespace":                 "ani-observability",
					"run_id":                    "ani-ani-lab",
					"storage_class":             "ani-block",
					"prometheus_storage_size":   "5Gi",
					"alertmanager_storage_size": "1Gi",
					"prometheus_retention":      "24h",
					"prometheus_retention_size": "4Gi",
				},
			},
		},
		"kubernetes": map[string]any{"cluster_name": "ani-lab"},
	}
	wantStorage := map[string]string{"postgresql": "10Gi", "valkey": "2Gi", "nats": "5Gi", "metrics": "5Gi"}
	for _, name := range []string{"cert-manager", "postgresql", "valkey", "nats", "metrics"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, name, "templates", "connection.md")
			tmpl, err := template.New("connection.md").ParseFiles(path)
			if err != nil {
				t.Fatalf("parse template: %v", err)
			}
			rendered := &strings.Builder{}
			if err := tmpl.Execute(rendered, ctx); err != nil {
				t.Fatalf("execute template: %v", err)
			}
			out := rendered.String()
			if strings.TrimSpace(out) == "" {
				t.Fatal("rendered connection fragment is empty")
			}
			for _, bad := range []string{"{{", "}}", "<no value>"} {
				if strings.Contains(out, bad) {
					t.Fatalf("rendered connection fragment contains %q:\n%s", bad, out)
				}
			}
			if !strings.Contains(out, "- namespace:") {
				t.Fatalf("connection fragment has no namespace entry:\n%s", out)
			}
			if !strings.Contains(out, "- retention:") {
				t.Fatalf("connection fragment has no retention entry:\n%s", out)
			}
			if !strings.Contains(out, "- verification:") {
				t.Fatalf("connection fragment has no verification entry:\n%s", out)
			}
			if size, ok := wantStorage[name]; ok {
				if !strings.Contains(out, size) || !strings.Contains(out, "ani-block") {
					t.Fatalf("connection fragment does not carry the configured %s/ani-block:\n%s", size, out)
				}
			}
			// Credentials are not allowed: only Secret names, never values.
			for _, leak := range []string{"CHANGE_ME", "password:", "token:"} {
				if strings.Contains(out, leak) {
					t.Fatalf("connection fragment appears to contain a credential (%q):\n%s", leak, out)
				}
			}
		})
	}
}

func contains(values []string, needle string) bool {
	for _, v := range values {
		if v == needle {
			return true
		}
	}
	return false
}

// localRefSuffix mirrors the images.tsv hauler_ref shape (registry host
// stripped, namespace kept) so a render test can build a plausible local
// reference for any upstream image without hard-coding every path.
func localRefSuffix(original string) string {
	parts := strings.SplitN(original, "/", 2)
	if len(parts) == 1 {
		return original
	}
	return parts[1]
}

// TestEnabledBatchTwoComponentsFailUntilImplemented makes the not-implemented
// gate real rather than vacuous: every observability row must be reachable in
// the selection (so the loop cannot be skipped) and Validate must reject it
// with the not-implemented message while the row is absent from
// ImplementedComponents. When a later card adds a role it must remove that row
// from this test in the same change.
func TestEnabledBatchTwoComponentsFailUntilImplemented(t *testing.T) {
	cases := []struct {
		component string
		site      string
	}{
		{"loki", "components:\n  logging:\n    backend: loki\n"},
		{"opensearch", "components:\n  certManager:\n    enabled: true\n  logging:\n    backend: opensearch\n"},
		{"fluent-bit", "components:\n  logging:\n    backend: loki\n"},
	}
	covered := map[string]bool{}
	for _, tc := range cases {
		if contains(ImplementedComponents, tc.component) {
			t.Fatalf("%s is now implemented; drop it from this test and add a role test instead", tc.component)
		}
		covered[tc.component] = true
		c, err := parseSite(t, tc.site)
		if err != nil {
			t.Fatalf("parse %s: %v", tc.component, err)
		}
		if !selectionEnabled(c, tc.component) {
			t.Fatalf("fixture does not enable %s; the not-implemented gate would be untested", tc.component)
		}
		err = Validate(c)
		if err == nil {
			t.Fatalf("Validate() accepted enabled %s although this release has no role for it", tc.component)
		}
		if !strings.Contains(err.Error(), "not implement") {
			t.Fatalf("Validate() error for %s = %v, want a not-implemented message", tc.component, err)
		}
	}
	// Every remaining unimplemented observability row is covered, so the gate
	// cannot silently regress. metrics is implemented as of C2 and is covered
	// by its own role test instead.
	for _, row := range []string{"loki", "opensearch", "fluent-bit"} {
		if !covered[row] {
			t.Fatalf("observability row %s has no not-implemented case", row)
		}
	}
}

// TestConnectionsDocumentAssemblesEnabledFragments proves the installer builds
// connections.md from the fragments its roles rendered, in the fixed component
// order, skips disabled components, and fails loudly when an enabled component
// produced nothing.
func TestConnectionsDocumentAssemblesEnabledFragments(t *testing.T) {
	workRoot := t.TempDir()
	dir := filepath.Join(workRoot, connectionsDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir connections.d: %v", err)
	}
	rows := []ComponentRow{
		{Name: "cert-manager", Enabled: true},
		{Name: "postgresql", Enabled: false},
		{Name: "valkey", Enabled: true},
		{Name: "nats", Enabled: false},
		{Name: "metrics", Enabled: true},
		{Name: "loki", Enabled: false},
		{Name: "opensearch", Enabled: false},
		{Name: "fluent-bit", Enabled: false},
	}
	for _, name := range []string{"cert-manager", "valkey", "metrics"} {
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte("## "+name+"\n\n- namespace: `x`"), 0o600); err != nil {
			t.Fatalf("write fragment %s: %v", name, err)
		}
	}
	dest := filepath.Join(workRoot, "connections.md")
	if err := writeConnections(dest, workRoot, rows); err != nil {
		t.Fatalf("writeConnections: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat connections.md: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("connections.md mode = %o, want 0600", perm)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read connections.md: %v", err)
	}
	out := string(data)
	if strings.Contains(out, "## postgresql") || strings.Contains(out, "## nats") {
		t.Fatalf("disabled components leaked into connections.md:\n%s", out)
	}
	// Fixed order: cert-manager before valkey before metrics.
	for _, want := range []string{"## cert-manager", "## valkey", "## metrics"} {
		if !strings.Contains(out, want) {
			t.Fatalf("connections.md missing %q:\n%s", want, out)
		}
	}
	if !(strings.Index(out, "## cert-manager") < strings.Index(out, "## valkey") &&
		strings.Index(out, "## valkey") < strings.Index(out, "## metrics")) {
		t.Fatalf("connections.md does not follow the fixed component order:\n%s", out)
	}

	// An enabled component with no fragment must abort instead of producing a
	// silently incomplete document.
	if err := os.Remove(filepath.Join(dir, "valkey.md")); err != nil {
		t.Fatalf("remove valkey fragment: %v", err)
	}
	if err := writeConnections(dest, workRoot, rows); err == nil {
		t.Fatal("writeConnections accepted an enabled component with no fragment")
	}
}

// TestComponentValuesRenderCompleteImages renders every Chart values template
// with a fully populated image map and asserts that no image field renders
// empty, as the Go map form, or as "<no value>". A missing key in the rendered
// values would otherwise only surface mid-install.
func TestComponentValuesRenderCompleteImages(t *testing.T) {
	root := filepath.Join("..", "..", "builtin", "core", "roles", "ani")
	matches, err := filepath.Glob(filepath.Join(root, "*", "templates", "values.yaml"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no component values.yaml templates found")
	}
	// Every original image key any current template asks for. Rendering with a
	// deliberately incomplete map must fail rather than produce an empty field.
	complete := map[string]string{}
	for _, original := range []string{
		"docker.io/library/nats:2.14.6-alpine",
		"docker.io/natsio/nats-server-config-reloader:0.23.0",
		"quay.io/prometheus-operator/prometheus-operator:v0.90.1",
		"quay.io/prometheus-operator/prometheus-config-reloader:v0.90.1",
		"quay.io/prometheus/prometheus:v3.11.3-distroless",
		"quay.io/prometheus/alertmanager:v0.32.1",
		"quay.io/prometheus/node-exporter:v1.11.1-distroless",
		"registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.19.0",
		"ghcr.io/jkroepke/kube-webhook-certgen:1.8.3",
		"docker.io/library/python:3.13.11-alpine3.23",
	} {
		complete[original] = "192.0.2.11:5000/" + localRefSuffix(original)
	}
	// Charts that build "registry/repository:tag" themselves read the split
	// parts, so the render context carries them the same way KubeKeyConfig
	// does. Building them from the real key list keeps this test in step with
	// the roles instead of duplicating the image names a second time.
	table := ImageTable{}
	for original := range complete {
		table[original] = Image{Original: original, HaulerRef: "127.0.0.1:5000/" + localRefSuffix(original)}
	}
	imageParts, err := ComponentImageParts(table, "192.0.2.11:5000")
	if err != nil {
		t.Fatalf("ComponentImageParts() error = %v", err)
	}
	ctx := map[string]any{
		"ani": map[string]any{
			"registry":    "192.0.2.11:5000",
			"images":      complete,
			"image_parts": imageParts,
			"components": map[string]any{
				"nats": map[string]any{"storage_class": "ani-block", "storage_size": "5Gi"},
				"metrics": map[string]any{
					"enabled":                   true,
					"namespace":                 "ani-observability",
					"run_id":                    "ani-ani-lab",
					"storage_class":             "ani-block",
					"prometheus_storage_size":   "5Gi",
					"alertmanager_storage_size": "1Gi",
					"prometheus_retention":      "24h",
					"prometheus_retention_size": "4Gi",
				},
			},
		},
		"kubernetes": map[string]any{"cluster_name": "ani-lab"},
	}
	for _, path := range matches {
		name := filepath.Base(filepath.Dir(filepath.Dir(path)))
		t.Run(name, func(t *testing.T) {
			tmpl, err := template.New("values.yaml").ParseFiles(path)
			if err != nil {
				t.Fatalf("parse template: %v", err)
			}
			rendered := &strings.Builder{}
			if err := tmpl.Execute(rendered, ctx); err != nil {
				t.Fatalf("execute template: %v", err)
			}
			out := rendered.String()
			// A nested map rendered as a string shows up as "map[repository:...".
			for _, bad := range []string{"map[", "<no value>", "{{"} {
				if strings.Contains(out, bad) {
					t.Fatalf("rendered values contain %q:\n%s", bad, out)
				}
			}
			// Every image-ish key must end with a tag or digest, never a bare
			// registry path with nothing after the colon.
			for _, line := range strings.Split(out, "\n") {
				trimmed := strings.TrimSpace(line)
				if !strings.Contains(trimmed, ": ") {
					continue
				}
				key := strings.TrimSpace(strings.SplitN(trimmed, ":", 2)[0])
				value := strings.TrimSpace(strings.SplitN(trimmed, ": ", 2)[1])
				if key == "fullImageName" || key == "image" {
					if value == "" || value == "\"\"" || value == "''" {
						t.Fatalf("image field %q rendered empty in %s:\n%s", key, path, out)
					}
				}
			}
		})
	}
}

// TestVerifyScriptExpectsEveryComponentRow keeps verify.sh's positional row
// list in lockstep with the Go side, so a new component row cannot be verified
// against the wrong script.
func TestVerifyScriptExpectsEveryComponentRow(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "verify.sh"))
	if err != nil {
		t.Fatalf("read verify.sh: %v", err)
	}
	script := string(data)
	want := "EXPECTED_COMPONENTS=(cert-manager postgresql valkey nats metrics loki opensearch fluent-bit)"
	if !strings.Contains(script, want) {
		t.Fatalf("verify.sh does not declare the fixed 8-row component list; want %q", want)
	}
	if !strings.Contains(script, `tail -n +2 "$SELECTION_FILE"`) {
		t.Fatal("verify.sh must skip the config_sha256 header row when reading the selection")
	}
	if !strings.Contains(script, `component selection must have exactly`) {
		t.Fatal("verify.sh must fail when the selection row count is wrong")
	}
	// The row list must match componentsOrder exactly.
	if len(componentsOrder) != 8 {
		t.Fatalf("componentsOrder has %d rows, want 8", len(componentsOrder))
	}
	for _, name := range componentsOrder {
		if !strings.Contains(script, name) {
			t.Fatalf("verify.sh does not mention component row %q", name)
		}
	}
}

// TestComponentChartMaterialsCoverEveryChartBackedComponent keeps the artifact
// requirement list aligned with the selection: anything the installer can turn
// on and that installs from a Chart must have its fixed path recorded.
func TestComponentChartMaterialsCoverEveryChartBackedComponent(t *testing.T) {
	for _, name := range []string{"cert-manager", "nats", "metrics", "loki", "opensearch", "fluent-bit"} {
		rel, ok := componentChartMaterials[name]
		if !ok {
			t.Fatalf("componentChartMaterials has no entry for %s", name)
		}
		if !strings.HasPrefix(rel, "charts/") || !strings.HasSuffix(rel, ".tgz") {
			t.Fatalf("componentChartMaterials[%s] = %q, want a charts/*.tgz path", name, rel)
		}
	}
}

// TestConnectionsFragmentsExistForEveryBatchComponent keeps the connection
// document complete: a role that installs resources must render facts for them.
func TestConnectionsFragmentsExistForEveryBatchComponent(t *testing.T) {
	root := filepath.Join("..", "..", "builtin", "core", "roles", "ani")
	for _, name := range []string{"cert-manager", "postgresql", "valkey", "nats", "metrics"} {
		fragment := filepath.Join(root, name, "templates", "connection.md")
		if _, err := os.Stat(fragment); err != nil {
			t.Fatalf("component %s has no connection facts template: %v", name, err)
		}
		tasks, err := os.ReadFile(filepath.Join(root, name, "tasks", "main.yaml"))
		if err != nil {
			t.Fatalf("read %s tasks: %v", name, err)
		}
		if !strings.Contains(string(tasks), "src: connection.md") {
			t.Fatalf("%s tasks do not render connection.md", name)
		}
		if !strings.Contains(string(tasks), "work/connections.d") {
			t.Fatalf("%s tasks do not create the connections.d directory", name)
		}
	}
}

// TestMetricsRoleIsWiredAndOffline covers the C2 card's own claims: the role is
// reachable from the playbook behind its switch, it installs from the packaged
// Chart, every image it names is an offline reference, it does not deploy
// Thanos or Grafana, and its verification reaches the real APIs. The role is
// what makes the metrics row deployable, so these are the properties that must
// hold before the row is listed as implemented.
func TestMetricsRoleIsWiredAndOffline(t *testing.T) {
	root := filepath.Join("..", "..")
	role := filepath.Join(root, "builtin", "core", "roles", "ani", "metrics")

	tasks, err := os.ReadFile(filepath.Join(role, "tasks", "main.yaml"))
	if err != nil {
		t.Fatalf("read metrics tasks: %v", err)
	}
	taskText := string(tasks)

	// The playbook must gate the role on the component switch, exactly like the
	// foundation roles, or an install would deploy it unconditionally.
	playbook, err := os.ReadFile(filepath.Join(root, "builtin", "core", "playbooks", "create_cluster.yaml"))
	if err != nil {
		t.Fatalf("read create_cluster.yaml: %v", err)
	}
	playbookText := string(playbook)
	if !strings.Contains(playbookText, "role: ani/metrics") {
		t.Fatal("create_cluster.yaml does not reference ani/metrics")
	}
	if !strings.Contains(playbookText, `when: '{{ (index .ani.components "metrics").enabled }}'`) {
		t.Fatal("ani/metrics is not gated on its component switch")
	}

	// The Chart comes from the offline artifact at the locked path.
	if !strings.Contains(taskText, "charts/kube-prometheus-stack/85.4.0.tgz") {
		t.Fatal("metrics tasks do not install the packaged kube-prometheus-stack 85.4.0 Chart")
	}
	if !strings.Contains(taskText, "{{ .ani.artifact_root }}/bin/helm") {
		t.Fatal("metrics tasks do not use the packaged Helm binary")
	}
	// The role must wait on the real objects, not only on the Helm release, and
	// it must wait for both PVCs to bind before the verification runs.
	for _, want := range []string{
		"--for=jsonpath='{.status.phase}'=Bound",
		"statefulset/prometheus-ani-metrics-prometheus",
		"statefulset/alertmanager-ani-metrics-alertmanager",
		"daemonset/ani-metrics-prometheus-node-exporter",
	} {
		if !strings.Contains(taskText, want) {
			t.Fatalf("metrics tasks do not wait on %q", want)
		}
	}
	// The verification script is installed root-only and executed.
	if !strings.Contains(taskText, "chmod 0700 /etc/kubernetes/ani/metrics/verify.sh") {
		t.Fatal("metrics tasks do not lock down and execute the verification script")
	}
	// Only the chart material, never a network fetch.
	for _, bad := range []string{"helm repo", "https://", "helm pull"} {
		if strings.Contains(taskText, bad) {
			t.Fatalf("metrics tasks appear to fetch from the network (%q)", bad)
		}
	}

	// Grafana and Thanos are explicitly out of scope for this batch.
	for _, bad := range []string{"grafana", "thanos"} {
		for _, line := range strings.Split(taskText, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") || trimmed == "" {
				continue
			}
			if strings.Contains(strings.ToLower(trimmed), bad) {
				t.Fatalf("metrics tasks reference %s outside a comment: %s", bad, trimmed)
			}
		}
	}

	// CRDs come from the chart, and the upgrade job stays off.
	values, err := os.ReadFile(filepath.Join(role, "templates", "values.yaml"))
	if err != nil {
		t.Fatalf("read metrics values: %v", err)
	}
	valuesText := string(values)
	for _, want := range []string{
		"fullnameOverride: ani-metrics",
		"crds:\n  enabled: true\n  upgradeJob:\n    enabled: false",
		"admissionWebhooks:\n    certManager:\n      enabled: false",
		"grafana:\n  enabled: false",
		"windowsMonitoring:\n  enabled: false",
		"defaultRules:\n  create: false",
	} {
		if !strings.Contains(valuesText, want) {
			t.Fatalf("metrics values do not contain %q", want)
		}
	}
	// Control-plane targets that need published loopback ports stay off.
	for _, want := range []string{
		"kubeEtcd:\n  enabled: false",
		"kubeControllerManager:\n  enabled: false",
		"kubeScheduler:\n  enabled: false",
		"kubeProxy:\n  enabled: false",
		"coreDns:\n  enabled: false",
		"kubeDns:\n  enabled: false",
	} {
		if !strings.Contains(valuesText, want) {
			t.Fatalf("metrics values do not disable %q", want)
		}
	}
	// node-exporter carries the plain tag because the sub-chart appends
	// "-distroless" itself when distroless is true. The tag is read from the
	// split image parts, so the assertion is on the exact template expression
	// plus the tag the key list resolves to; a whole reference here, or a tag
	// that already carries the suffix, would double up on the real render.
	if !strings.Contains(valuesText, "tag: {{ .ani.image_parts.metrics.nodeExporter.tag }}") {
		t.Fatal("node-exporter image must take its tag from the split image parts")
	}
	if !strings.Contains(valuesText, "distroless: true") {
		t.Fatal("node-exporter must set distroless: true so the sub-chart appends the suffix once")
	}
	if !strings.Contains(valuesText, "repository: {{ .ani.image_parts.metrics.nodeExporter.repository }}") {
		t.Fatal("node-exporter repository must come from the split image parts")
	}
	parts, err := ComponentImageParts(testImageTable(), "192.0.2.11:5000")
	if err != nil {
		t.Fatalf("ComponentImageParts() error = %v", err)
	}
	nodeExporter := parts["metrics"].(map[string]any)["nodeExporter"].(map[string]any)
	if nodeExporter["tag"] != "v1.11.1" {
		t.Fatalf("node-exporter tag = %v, want the plain v1.11.1 (the sub-chart appends -distroless)", nodeExporter["tag"])
	}
	// The rule and config selectors must be scoped, not left wide open.
	if !strings.Contains(valuesText, "release: ani-metrics") {
		t.Fatal("metrics values do not scope the Prometheus rule selector to this release")
	}
	if !strings.Contains(valuesText, "run_id:") {
		t.Fatal("metrics values do not scope the AlertmanagerConfig selector to this run")
	}
}

// TestMetricsVerifyExercisesRealApis pins the substance of the C2 acceptance:
// the script must query the real HTTP APIs, drive a firing/resolved pair, and
// rebuild pods. A script that only checked readiness would still render and
// parse, so the claims are asserted on content.
func TestMetricsVerifyExercisesRealApis(t *testing.T) {
	path := filepath.Join("..", "..", "builtin", "core", "roles", "ani", "metrics", "templates", "verify.sh")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read metrics verify.sh: %v", err)
	}
	script := string(data)

	for _, want := range []struct{ needle, why string }{
		{"/api/v1/query?", "must query the Prometheus HTTP API"},
		{"/api/v1/query_range?", "must read the pre-rebuild sample back by range query"},
		{"node_uname_info", "must prove node-exporter reads the machine"},
		{"kube_node_info", "must prove kube-state-metrics reads the API"},
		{"container_memory_working_set_bytes", "must check a real cAdvisor container metric"},
		{"vector(1) == 1", "must drive a real firing transition"},
		{"vector(0) == 1", "must resolve by evaluating to an empty vector"},
		{"sendResolved: true", "resolved delivery must be configured"},
		{"fingerprint", "the resolved notification must be matched on fingerprint"},
		{"delete pod -l app.kubernetes.io/name=prometheus", "must rebuild the Prometheus pod"},
		{"delete pod -l app.kubernetes.io/name=alertmanager", "must rebuild the Alertmanager pod"},
		{"ani_metrics_rebuild_marker", "must write its own sample before the rebuild"},
		{"silenceID", "must create and read back a real Alertmanager silence"},
		{"rollout status", "must wait on the workload state rather than sleeping"},
		{`.metadata.uid`, "must compare object identities across the rebuild"},
		{"node-exporter ready on", "must fail when node-exporter does not cover every node"},
	} {
		if !strings.Contains(script, want.needle) {
			t.Fatalf("metrics verify.sh does not contain %q: it %s", want.needle, want.why)
		}
	}

	// The alert must fire by returning a sample and resolve by returning none,
	// so the rule expressions must not use `bool`: with bool, vector(0) == 1
	// would return the sample 0 and keep the alert firing forever. Only the
	// expr lines are inspected, because the script explains the choice in
	// comments.
	for _, line := range strings.Split(script, "\n") {
		if !strings.Contains(line, "expr:") {
			continue
		}
		if strings.Contains(line, "bool") {
			t.Fatalf("rule expression uses a bool modifier and could never resolve: %s", strings.TrimSpace(line))
		}
	}
	// Both transitions must be expressed as a comparison against a literal, so
	// the non-firing case is an empty vector rather than a zero sample.
	if !strings.Contains(script, `expr: vector(1) == 1`) {
		t.Fatal("the firing rule must compare vector(1) against 1")
	}
	if !strings.Contains(script, `expr: vector(0) == 1`) {
		t.Fatal("the resolved rule must compare vector(0) against 1")
	}
	// Nothing may write alerts straight into Alertmanager instead of letting
	// Prometheus evaluate them.
	for _, bad := range []string{"/api/v2/alerts", "/api/v1/alerts"} {
		if strings.Contains(script, bad) {
			t.Fatalf("metrics verify.sh posts to %s, which would bypass Prometheus evaluation", bad)
		}
	}
	// The lab image must be the locked offline one, not a live pull.
	if !strings.Contains(script, `index .ani.images "docker.io/library/python:3.13.11-alpine3.23"`) {
		t.Fatal("metrics verify.sh does not use the locked offline python image")
	}
	// Cleanup has to remove this run's temporary objects.
	for _, want := range []string{
		`delete prometheusrule "$RULE_NAME"`,
		`delete alertmanagerconfig "$AMCFG_NAME"`,
		`delete deployment "$RECV_DEPLOY"`,
		`delete pod "$CLIENT_POD"`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("metrics verify.sh does not clean up: %q", want)
		}
	}
}

// TestMetricsRetentionSizeMustFitInTheVolume keeps the on-disk cap meaningful:
// a cap at or above the volume size would let Prometheus fill its PVC before
// the time based retention ever applied.
func TestMetricsRetentionSizeMustFitInTheVolume(t *testing.T) {
	c, err := parseSite(t, "components:\n  metrics:\n    enabled: true\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(c); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	if c.Components.Metrics.PrometheusRetentionSize != "4Gi" {
		t.Fatalf("default prometheusRetentionSize = %q, want 4Gi", c.Components.Metrics.PrometheusRetentionSize)
	}

	c, err = parseSite(t, "components:\n  metrics:\n    enabled: true\n    prometheusStorageSize: 5Gi\n    prometheusRetentionSize: 8Gi\n")
	if err != nil {
		t.Fatalf("parse oversized cap: %v", err)
	}
	if err := Validate(c); err == nil {
		t.Fatal("a retention cap larger than the volume must be rejected")
	}

	c, err = parseSite(t, "components:\n  metrics:\n    enabled: true\n    prometheusRetentionSize: not-a-size\n")
	if err != nil {
		t.Fatalf("parse bad cap: %v", err)
	}
	if err := Validate(c); err == nil {
		t.Fatal("a non-capacity retention cap must be rejected")
	}
}

// TestMetricsRunLabelScopesAlertRouting pins the value Alertmanager uses to
// decide which configs it accepts: it must be non-empty while the stack is on
// and empty while it is off, so a disabled stack never matches a route.
func TestMetricsRunLabelScopesAlertRouting(t *testing.T) {
	on, err := parseSite(t, "components:\n  metrics:\n    enabled: true\n")
	if err != nil {
		t.Fatalf("parse enabled: %v", err)
	}
	metrics := componentSpec(on.Components, on.Name)["metrics"].(map[string]any)
	if metrics["run_id"] != "ani-ani-lab" {
		t.Fatalf("enabled run_id = %v, want ani-ani-lab", metrics["run_id"])
	}
	if metrics["namespace"] != MetricsNamespace {
		t.Fatalf("namespace = %v, want %s", metrics["namespace"], MetricsNamespace)
	}

	off, err := parseSite(t, "")
	if err != nil {
		t.Fatalf("parse default: %v", err)
	}
	metrics = componentSpec(off.Components, off.Name)["metrics"].(map[string]any)
	if metrics["run_id"] != "" {
		t.Fatalf("disabled run_id = %v, want an empty string", metrics["run_id"])
	}
}

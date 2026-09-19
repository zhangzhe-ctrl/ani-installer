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
	components := componentSpec(c.Components)
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
	components = componentSpec(c.Components)
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
				"docker.io/alpine/openssl:3.5.4": "192.0.2.11:5000/alpine/openssl:3.5.4",
			},
			"components": map[string]any{
				"cert-manager": map[string]any{"enabled": true},
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
			},
		},
		"kubernetes": map[string]any{"cluster_name": "ani-lab"},
	}
	wantStorage := map[string]string{"postgresql": "10Gi", "valkey": "2Gi", "nats": "5Gi"}
	for _, name := range []string{"cert-manager", "postgresql", "valkey", "nats"} {
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
		{"metrics", "components:\n  metrics:\n    enabled: true\n"},
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
	// Every observability row is covered, so the gate cannot silently regress.
	for _, row := range []string{"metrics", "loki", "opensearch", "fluent-bit"} {
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
	} {
		complete[original] = "192.0.2.11:5000/" + strings.TrimPrefix(original, "docker.io/")
	}
	ctx := map[string]any{
		"ani": map[string]any{
			"registry": "192.0.2.11:5000",
			"images":   complete,
			"components": map[string]any{
				"nats": map[string]any{"storage_class": "ani-block", "storage_size": "5Gi"},
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
	for _, name := range []string{"cert-manager", "postgresql", "valkey", "nats"} {
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

package ani

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"text/template"

	kkTmpl "github.com/kubesphere/kubekey/v4/pkg/converter/tmpl"
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
storage:
  enabled: true
  provider: ceph
  makeDefaultStorageClass: true
  nodes:
    - {name: node1, devices: [/dev/disk/by-id/ata-ani-data-01]}
    - {name: node2, devices: [/dev/disk/by-id/ata-ani-data-02]}
    - {name: node3, devices: [/dev/disk/by-id/ata-ani-data-03]}
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
# R05: an old foundation-batch config keeps working, but it now has to say where
# its storage comes from instead of relying on profile: full implying Ceph.
storage:
  enabled: true
  provider: ceph
  makeDefaultStorageClass: true
  nodes:
    - {name: node1, devices: [/dev/disk/by-id/ata-ani-data-01]}
    - {name: node2, devices: [/dev/disk/by-id/ata-ani-data-02]}
    - {name: node3, devices: [/dev/disk/by-id/ata-ani-data-03]}
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
					"prometheus_retention_size": "4GiB",
				},
				// The log roles render from the same logging block: one
				// backend string, one namespace, one derived retention.
				"logging": map[string]any{
					"enabled":         true,
					"backend":         "loki",
					"namespace":       "ani-observability",
					"storage_class":   "ani-block",
					"storage_size":    "10Gi",
					"retention_days":  "3",
					"retention_hours": 72,
					"retention_iso":   "PT72H",
				},
			},
		},
		"kubernetes": map[string]any{"cluster_name": "ani-lab"},
	}
	wantStorage := map[string]string{"postgresql": "10Gi", "valkey": "2Gi", "nats": "5Gi", "metrics": "5Gi", "loki": "10Gi"}
	for _, name := range []string{"cert-manager", "postgresql", "valkey", "nats", "metrics", "loki"} {
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

// TestFluentBitConnectionFragmentRendersBothBackends renders the collector's
// connection fragment for each backend and asserts the destination it names is
// the selected one and only that one. A fragment naming both would tell an
// operator a deployment has two log stores, which it never does.
func TestFluentBitConnectionFragmentRendersBothBackends(t *testing.T) {
	root := filepath.Join("..", "..", "builtin", "core", "roles", "ani", "fluent-bit")
	path := filepath.Join(root, "templates", "connection.md")

	cases := []struct {
		backend string
		want    string
		absent  string
	}{
		{"loki", "ani-loki.ani-observability.svc.cluster.local:3100", "ani-opensearch-master"},
		{"opensearch", "ani-opensearch-master.ani-observability.svc.cluster.local:9200", "ani-loki."},
	}
	for _, tc := range cases {
		t.Run(tc.backend, func(t *testing.T) {
			ctx := map[string]any{
				"ani": map[string]any{
					"registry": "192.0.2.11:5000",
					"components": map[string]any{
						"logging": map[string]any{
							"enabled":         true,
							"backend":         tc.backend,
							"namespace":       "ani-observability",
							"storage_class":   "ani-block",
							"storage_size":    "10Gi",
							"retention_days":  "3",
							"retention_hours": 72,
							"retention_iso":   "PT72H",
						},
					},
				},
				"kubernetes": map[string]any{"cluster_name": "ani-lab"},
			}
			tmpl, err := template.New("connection.md").ParseFiles(path)
			if err != nil {
				t.Fatalf("parse template: %v", err)
			}
			rendered := &strings.Builder{}
			if err := tmpl.Execute(rendered, ctx); err != nil {
				t.Fatalf("execute template: %v", err)
			}
			out := rendered.String()
			for _, bad := range []string{"{{", "}}", "<no value>"} {
				if strings.Contains(out, bad) {
					t.Fatalf("rendered fragment contains %q:\n%s", bad, out)
				}
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("fragment does not name the selected backend destination %q:\n%s", tc.want, out)
			}
			if strings.Contains(out, tc.absent) {
				t.Fatalf("fragment names the unselected backend %q:\n%s", tc.absent, out)
			}
			if !strings.Contains(out, "- namespace:") || !strings.Contains(out, "- verification:") {
				t.Fatalf("fragment is missing its namespace or verification entry:\n%s", out)
			}
			// The collector never carries a credential value.
			for _, leak := range []string{"CHANGE_ME", "password:", "token:"} {
				if strings.Contains(out, leak) {
					t.Fatalf("fragment appears to contain a credential (%q):\n%s", leak, out)
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

// TestEnabledBatchTwoComponentsFailUntilImplemented keeps the not-implemented
// gate honest: a site may enable any row the schema understands, but a row with
// no packaged role must fail validation instead of deploying nothing. Each row
// leaves this test in the card that ships its role; every observability row has
// now left it, so what remains is the invariant that the gate itself still
// works, checked with a row name the selection understands but no role ships.
func TestEnabledBatchTwoComponentsFailUntilImplemented(t *testing.T) {
	// Every observability row is implemented as of C4. The gate is still
	// exercised directly, so it cannot regress to a no-op: a row the selection
	// knows but ImplementedComponents does not must fail.
	for _, row := range []string{"metrics", "loki", "opensearch", "fluent-bit"} {
		if !contains(ImplementedComponents, row) {
			t.Fatalf("%s must be an implemented component by now", row)
		}
	}

	// The gate in Validate loops Selection() and compares against
	// ImplementedComponents. Removing a row from a copy of the list must make
	// validation refuse the site that enables it, which is what proves the loop
	// still runs rather than having been short-circuited.
	saved := ImplementedComponents
	defer func() { ImplementedComponents = saved }()
	ImplementedComponents = []string{"cert-manager", "postgresql", "valkey", "nats", "metrics", "loki", "fluent-bit"}

	c, err := parseSite(t, "components:\n  certManager:\n    enabled: true\n  logging:\n    backend: opensearch\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !selectionEnabled(c, "opensearch") {
		t.Fatal("the fixture does not enable opensearch; the not-implemented gate would be untested")
	}
	err = Validate(c)
	if err == nil {
		t.Fatal("Validate() accepted an enabled row that has no role in the release")
	}
	if !strings.Contains(err.Error(), "not implement") {
		t.Fatalf("Validate() error = %v, want a not-implemented message", err)
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
	// The list is derived from componentImageKeys() plus the images the roles
	// name directly, so a newly added key cannot silently go uncovered here.
	complete := map[string]string{}
	originalKeys := []string{
		"docker.io/library/nats:2.14.6-alpine",
		"docker.io/natsio/nats-server-config-reloader:0.23.0",
	}
	for _, key := range componentImageKeys() {
		originalKeys = append(originalKeys, key.Original)
	}
	for _, original := range originalKeys {
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
					"prometheus_retention_size": "4GiB",
				},
				// The log roles render from the logging block; without it their
				// templates would compare an empty backend and render no output
				// at all, which is exactly the failure this test should catch.
				"logging": map[string]any{
					"enabled":         true,
					"backend":         "loki",
					"namespace":       "ani-observability",
					"storage_class":   "ani-block",
					"storage_size":    "10Gi",
					"retention_days":  "3",
					"retention_hours": 72,
					"retention_iso":   "PT72H",
				},
			},
		},
		"kubernetes": map[string]any{"cluster_name": "ani-lab"},
	}
	for _, path := range matches {
		name := filepath.Base(filepath.Dir(filepath.Dir(path)))
		t.Run(name, func(t *testing.T) {
			// The roles render with text/template from the installer's own
			// context. Helm's {{ .Values.* }} does not exist at that point, so a
			// role template using it would render "<no value>" and any consumer
			// reading the file could not tell that from a working render. The
			// output check below catches the symptom; this catches the cause.
			// Comment lines are skipped: a comment explaining why the chart's
			// own .Values form is not used is legitimate and must not fail.
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read template: %v", err)
			}
			for i, line := range strings.Split(string(source), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "#") {
					continue
				}
				if strings.Contains(line, ".Values.") {
					t.Fatalf("role values template uses Helm's .Values on line %d, which the installer's render never provides", i+1)
				}
			}

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
	for _, name := range []string{"cert-manager", "postgresql", "valkey", "nats", "metrics", "loki", "opensearch", "fluent-bit"} {
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

// TestRoleTasksUseTheContextKeysTheInstallerProvides guards the defect class
// that reached a real install: the metrics role's tasks read
// `.ani.metrics.namespace` nine times, but the installer puts the metrics block
// under `.ani.components`, and `.ani.metrics` is only the selection row, which
// carries no namespace. The template rendered `<no value>`, the shell read it
// as a redirection, and the install died on the first task. Rendering values
// alone never caught it, so the assertion is on the task files themselves.
//
// Only the context prefixes the installer actually builds are accepted: the
// component block, the split image parts, the registry and the network keys.
func TestRoleTasksUseTheContextKeysTheInstallerProvides(t *testing.T) {
	root := filepath.Join("..", "..")
	aniRoles := filepath.Join(root, "builtin", "core", "roles", "ani")

	roles, err := os.ReadDir(aniRoles)
	if err != nil {
		t.Fatalf("read ani roles: %v", err)
	}

	// A `.ani.<key>.` reference is only valid for these top-level keys. "storage"
	// joined the list in R05: the installer builds it from the site's storage
	// selection (enabled/provider/nodes/makeDefaultStorageClass), so the Ceph
	// role can be guarded and rendered by it.
	valid := map[string]bool{
		"components":  true,
		"image_parts": true,
		"registry":    true,
		"network":     true,
		"images":      true,
		"storage":     true,
	}

	checked := 0
	for _, role := range roles {
		if !role.IsDir() {
			continue
		}
		for _, rel := range []string{
			filepath.Join("tasks", "main.yaml"),
			filepath.Join("templates", "values.yaml"),
			filepath.Join("templates", "verify.sh"),
			filepath.Join("templates", "connection.md"),
		} {
			path := filepath.Join(aniRoles, role.Name(), rel)
			raw, err := os.ReadFile(path)
			if err != nil {
				continue // not every role owns every file
			}
			checked++
			for _, key := range aniContextKeys(string(raw)) {
				if valid[key] {
					continue
				}
				t.Fatalf("%s reads .ani.%s. which the installer does not build; "+
					"the component block is .ani.components.%s. and the bare key only "+
					"carries the selection row", path, key, key)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no role files were checked")
	}
}

// aniContextKeys returns every distinct top-level key of a `.ani.<key>.`
// reference in a role file. strings is enough here: the references are plain
// ASCII template paths and a regexp would not earn its import.
func aniContextKeys(text string) []string {
	var keys []string
	seen := map[string]bool{}
	for _, rest := range strings.Split(text, ".ani.")[1:] {
		end := strings.IndexByte(rest, '.')
		if end <= 0 {
			continue
		}
		key := rest[:end]
		for _, r := range key {
			if (r < 'a' || r > 'z') && r != '_' {
				key = ""
				break
			}
		}
		if key != "" && !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	return keys
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
	if !strings.Contains(playbookText, `when: '{{ and (index .ani.components "metrics").enabled (ne .ani.profile "base") }}'`) {
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
	if bad := externalMaterialFetch(taskText); bad != "" {
		t.Fatalf("metrics tasks appear to fetch from the network (%q)", bad)
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
	if c.Components.Metrics.PrometheusRetentionSize != "4GiB" {
		t.Fatalf("default prometheusRetentionSize = %q, want 4GiB", c.Components.Metrics.PrometheusRetentionSize)
	}

	c, err = parseSite(t, "components:\n  metrics:\n    enabled: true\n    prometheusStorageSize: 5Gi\n    prometheusRetentionSize: 8GiB\n")
	if err != nil {
		t.Fatalf("parse oversized cap: %v", err)
	}
	if err := Validate(c); err == nil {
		t.Fatal("a retention cap larger than the volume must be rejected")
	}

	// "4Gi" is a valid Kubernetes quantity but lacks the trailing B the
	// Prometheus CRD enforces on spec.retentionSize.
	c, err = parseSite(t, "components:\n  metrics:\n    enabled: true\n    prometheusRetentionSize: 4Gi\n")
	if err != nil {
		t.Fatalf("parse cap without trailing B: %v", err)
	}
	if err := Validate(c); err == nil {
		t.Fatal("a retention cap without the CRD's trailing B must be rejected")
	}

	c, err = parseSite(t, "components:\n  metrics:\n    enabled: true\n    prometheusRetentionSize: not-a-size\n")
	if err != nil {
		t.Fatalf("parse bad cap: %v", err)
	}
	if err := Validate(c); err == nil {
		t.Fatal("a non-capacity retention cap must be rejected")
	}
}

// TestLokiRoleIsWiredAndOffline mirrors the C2 role test for the log backend:
// the role must be gated on the backend actually being loki (not merely on the
// loki row being on), must take its Chart from the offline artifact, must wait
// on the objects the Chart really renders, and must keep every default-on
// component of the Loki Chart disabled.
// externalMaterialFetch returns the first network material fetch found in a
// role's task text, or "" when there is none.
//
// R04 narrowed this from a bare `strings.Contains(tasks, "https://")`, which
// also flagged a runtime call to a service *inside* the cluster
// (opensearch's `curl https://$svc:9200/_cluster/health`); that is not a
// material fetch. The ban is unchanged for what these tests were written to
// catch: `helm repo`/`helm pull` and any *literal* external URL. A URL whose host
// is a shell variable or a rendered template value is an in-cluster address and
// is allowed; scripts/test-check-code.py injects a literal external URL and
// proves the gate still fails on it.
var httpsLiteralRe = regexp.MustCompile(`https://([^\s"'/]*)`)

func externalMaterialFetch(tasks string) string {
	for _, bad := range []string{"helm repo", "helm pull"} {
		if strings.Contains(tasks, bad) {
			return bad
		}
	}
	for _, match := range httpsLiteralRe.FindAllStringSubmatch(tasks, -1) {
		host := match[1]
		if host != "" && !strings.HasPrefix(host, "$") && !strings.Contains(host, "{{") {
			return "https://" + host
		}
	}
	return ""
}

func TestLokiRoleIsWiredAndOffline(t *testing.T) {
	root := filepath.Join("..", "..")
	role := filepath.Join(root, "builtin", "core", "roles", "ani", "loki")

	tasks, err := os.ReadFile(filepath.Join(role, "tasks", "main.yaml"))
	if err != nil {
		t.Fatalf("read loki tasks: %v", err)
	}
	taskText := string(tasks)

	playbook, err := os.ReadFile(filepath.Join(root, "builtin", "core", "playbooks", "create_cluster.yaml"))
	if err != nil {
		t.Fatalf("read create_cluster.yaml: %v", err)
	}
	playbookText := string(playbook)
	if !strings.Contains(playbookText, "role: ani/loki") {
		t.Fatal("create_cluster.yaml does not reference ani/loki")
	}
	// Gating on the backend, not on the row, is what makes the two backends
	// mutually exclusive: a deployment can never render both roles.
	if !strings.Contains(playbookText,
		`when: '{{ and (index .ani.components "loki").enabled (eq .ani.components.logging.backend "loki") (ne .ani.profile "base") }}'`) {
		t.Fatal("ani/loki is not gated on the selected logging backend")
	}
	// Order matters: the backend must be installed and verified before the
	// collector starts writing to it.
	lokiAt := strings.Index(playbookText, "role: ani/loki")
	fluentAt := strings.Index(playbookText, "role: ani/fluent-bit")
	if lokiAt < 0 || fluentAt < 0 || lokiAt > fluentAt {
		t.Fatal("ani/loki must appear before ani/fluent-bit so the backend exists before collection")
	}
	metricsAt := strings.Index(playbookText, "role: ani/metrics")
	if metricsAt < 0 || metricsAt > lokiAt {
		t.Fatal("ani/metrics must appear before the log roles so they share one namespace")
	}

	if !strings.Contains(taskText, "charts/loki/18.13.3.tgz") {
		t.Fatal("loki tasks do not install the packaged loki 18.13.3 Chart")
	}
	if !strings.Contains(taskText, "{{ .ani.artifact_root }}/bin/helm") {
		t.Fatal("loki tasks do not use the packaged Helm binary")
	}
	// The monolith is a StatefulSet in this Chart; waiting on a Deployment
	// would never succeed.
	for _, want := range []string{
		"statefulset/ani-loki --timeout=600s",
		"--for=jsonpath='{.status.phase}'=Bound pvc/storage-ani-loki-0",
		"chmod 0700 /etc/kubernetes/ani/loki/verify.sh",
	} {
		if !strings.Contains(taskText, want) {
			t.Fatalf("loki tasks do not contain %q", want)
		}
	}
	if bad := externalMaterialFetch(taskText); bad != "" {
		t.Fatalf("loki tasks appear to fetch from the network (%q)", bad)
	}
	// Grafana is a later batch and must not appear at all.
	for _, line := range strings.Split(taskText, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(strings.ToLower(trimmed), "grafana") {
			t.Fatalf("loki tasks reference grafana outside a comment: %s", trimmed)
		}
	}

	values, err := os.ReadFile(filepath.Join(role, "templates", "values.yaml"))
	if err != nil {
		t.Fatalf("read loki values: %v", err)
	}
	valuesText := string(values)
	for _, want := range []string{
		"deploymentMode: Monolithic",
		"auth_enabled: false",
		// Every default-on component of this Chart has to be off, or the
		// install grows a gateway, a canary, a ruler, a test hook and ~9GiB of
		// memcached that this batch did not ask for.
		"chunksCache:\n  enabled: false",
		"resultsCache:\n  enabled: false",
		"gateway:\n  enabled: false",
		"lokiCanary:\n  enabled: false",
		"test:\n  enabled: false",
		"ruler:\n  enabled: false",
		"minio:\n  enabled: false",
		"  sidecar: false",
		// The Chart refuses to render a monolith unless all three distributed
		// replica groups are zero.
		"read:\n  replicas: 0",
		"write:\n  replicas: 0",
		"backend:\n  replicas: 0",
		// Retention only acts when the compactor is enabled with a delete
		// store; setting the limit alone marks nothing for deletion.
		"retention_enabled: true",
		"delete_request_store: filesystem",
		"working_directory: /var/loki/compactor",
		"flush_on_shutdown: true",
		"whenDeleted: Retain",
		"whenScaled: Retain",
		"enableStatefulSetAutoDeletePVC: false",
	} {
		if !strings.Contains(valuesText, want) {
			t.Fatalf("loki values do not contain %q", want)
		}
	}
	// The image must be built from the split parts; the chart concatenates
	// registry + repository + tag itself. image_parts carries only repository
	// and tag (the registry is the single site value), so the registry line
	// must reference .ani.registry while the other two come from the parts.
	if !strings.Contains(valuesText, "registry: {{ .ani.registry }}") {
		t.Fatal("loki image must take its registry from the site registry value")
	}
	if strings.Contains(valuesText, ".ani.image_parts.logs.loki.registry") {
		t.Fatal("image_parts has no registry field; the host must come from .ani.registry")
	}
	if !strings.Contains(valuesText, "repository: {{ .ani.image_parts.logs.loki.repository }}") {
		t.Fatal("loki image must take its repository from the split image parts")
	}
	if !strings.Contains(valuesText, "tag: {{ .ani.image_parts.logs.loki.tag }}") {
		t.Fatal("loki image must take its tag from the split image parts")
	}
	// Retention must be rendered from the derived hour count, not hand-written.
	if !strings.Contains(valuesText, "retention_period: {{ .ani.components.logging.retention_hours }}h") {
		t.Fatal("loki retention_period must come from the derived hour count")
	}
	if !strings.Contains(valuesText, "replication_factor: 1") {
		t.Fatal("loki must run with replication_factor 1 (one monolith, one volume)")
	}
}

// TestOpenSearchRoleIsWiredAndOffline pins the C4 role: it must be gated on the
// selected backend so the two backends are mutually exclusive, must install the
// packaged Chart from the offline artifact, and must never ship a demo
// configuration or a credential in a rendered file.
func TestOpenSearchRoleIsWiredAndOffline(t *testing.T) {
	root := filepath.Join("..", "..")
	role := filepath.Join(root, "builtin", "core", "roles", "ani", "opensearch")

	tasks, err := os.ReadFile(filepath.Join(role, "tasks", "main.yaml"))
	if err != nil {
		t.Fatalf("read opensearch tasks: %v", err)
	}
	taskText := string(tasks)

	playbook, err := os.ReadFile(filepath.Join(root, "builtin", "core", "playbooks", "create_cluster.yaml"))
	if err != nil {
		t.Fatalf("read create_cluster.yaml: %v", err)
	}
	playbookText := string(playbook)
	if !strings.Contains(playbookText, "role: ani/opensearch") {
		t.Fatal("create_cluster.yaml does not reference ani/opensearch")
	}
	// Gating on the backend, not on the row, is what makes the two log backends
	// mutually exclusive: a deployment can never render both roles.
	if !strings.Contains(playbookText,
		`when: '{{ and (index .ani.components "opensearch").enabled (eq .ani.components.logging.backend "opensearch") (ne .ani.profile "base") }}'`) {
		t.Fatal("ani/opensearch is not gated on the selected logging backend")
	}
	// Order matters: the backend must be installed and verified before the
	// collector starts writing to it, and both backends come after the metrics
	// stack because they share its namespace.
	metricsAt := strings.Index(playbookText, "role: ani/metrics")
	osAt := strings.Index(playbookText, "role: ani/opensearch")
	fluentAt := strings.Index(playbookText, "role: ani/fluent-bit")
	lokiAt := strings.Index(playbookText, "role: ani/loki")
	if metricsAt < 0 || osAt < 0 || fluentAt < 0 {
		t.Fatal("the observability roles are not all wired into create_cluster.yaml")
	}
	if metricsAt > osAt || osAt > fluentAt {
		t.Fatal("ani/opensearch must appear after ani/metrics and before ani/fluent-bit")
	}
	// loki must not have been displaced: both backends stay in the playbook,
	// each gated on its own backend value.
	if lokiAt < 0 || lokiAt > fluentAt {
		t.Fatal("ani/loki must still appear before ani/fluent-bit")
	}

	if !strings.Contains(taskText, "charts/opensearch/3.8.0.tgz") {
		t.Fatal("opensearch tasks do not install the packaged opensearch 3.8.0 Chart")
	}
	if !strings.Contains(taskText, "{{ .ani.artifact_root }}/bin/helm") {
		t.Fatal("opensearch tasks do not use the packaged Helm binary")
	}
	// The single node is a StatefulSet, so waiting on a Deployment would never
	// succeed, and the PVC is derived from the Chart's claim template rather
	// than hardcoded: <template>-<statefulset>-<ordinal>, where this Chart's
	// template is the StatefulSet name. The pod-shaped name
	// ani-opensearch-master-0 is not a PVC and shipped as a defect once.
	for _, want := range []string{
		"statefulset/ani-opensearch-master --timeout=900s",
		"jsonpath='{.spec.volumeClaimTemplates[0].metadata.name}'",
		`pvc="${template}-${sts}-0"`,
		"job/ani-opensearch-security-init",
		"90-ani-opensearch.conf",
		"chmod 0700 \"/etc/kubernetes/ani/opensearch/$script\"",
	} {
		if !strings.Contains(taskText, want) {
			t.Fatalf("opensearch tasks do not contain %q", want)
		}
	}
	// The security Secret must be applied before Helm runs, and the
	// initialization Job must be applied at all. Both were defects: the Secret
	// was created after `helm --wait`, which waits for a Pod that cannot start
	// without it, and the Job was rendered but never submitted, so the wait on
	// it could only time out.
	secretApply := strings.Index(taskText, "kubectl apply -f /etc/kubernetes/ani/opensearch/security-config.yaml")
	helmInstall := strings.Index(taskText, "helm upgrade --install ani-opensearch-master")
	if secretApply < 0 {
		t.Fatal("opensearch tasks never apply the security configuration Secret")
	}
	if helmInstall < 0 || secretApply > helmInstall {
		t.Fatal("the security configuration Secret is applied after Helm, so a clean install deadlocks on a missing Secret")
	}
	initApply := strings.Index(taskText, "kubectl apply -f /etc/kubernetes/ani/opensearch/security-init.yaml")
	initWait := strings.Index(taskText, "job/ani-opensearch-security-init")
	if initApply < 0 {
		t.Fatal("opensearch tasks render the security initialization Job but never apply it")
	}
	if initApply > initWait {
		t.Fatal("the security initialization Job is waited on before it is applied")
	}
	// vm.max_map_count is a node property and OpenSearch is not pinned to one
	// node, so the task has to reach every schedulable node rather than only the
	// node the role runs on.
	if !strings.Contains(taskText, `.groups.k8s_cluster | default list | toJson`) {
		t.Fatal("vm.max_map_count is not applied across the cluster's nodes")
	}
	if bad := externalMaterialFetch(taskText); bad != "" {
		t.Fatalf("opensearch tasks appear to fetch from the network (%q)", bad)
	}
	// The Chart's privileged sysctl init container must stay off; the node
	// setting is written by the role instead.
	if strings.Contains(taskText, "sysctlInit") && strings.Contains(taskText, "enabled: true") {
		t.Fatal("opensearch tasks appear to enable the Chart's privileged sysctl init container")
	}
	// Grafana is a later batch and must not appear at all.
	for _, line := range strings.Split(taskText, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(strings.ToLower(trimmed), "grafana") {
			t.Fatalf("opensearch tasks reference grafana outside a comment: %s", trimmed)
		}
	}

	values, err := os.ReadFile(filepath.Join(role, "templates", "values.yaml"))
	if err != nil {
		t.Fatalf("read opensearch values: %v", err)
	}
	valuesText := string(values)
	for _, want := range []string{
		"singleNode: true",
		// The demo configuration and the demo certificates are what install the
		// well-known users and the shared demo certificate; both are refused.
		"DISABLE_INSTALL_DEMO_CONFIG",
		"value: \"true\"",
		"plugins.security.allow_unsafe_democertificates: false",
		"plugins.security.allow_default_init_securityindex: false",
		"plugins.security.ssl.http.enabled: true",
		"CN=ani-opensearch-admin",
		"CN=ani-opensearch-node",
		// The security configuration is an externally created Secret, because
		// two of its files carry a hash that only exists at install time.
		"securityConfigSecret: ani-opensearch-security-config",
		// The privileged init container is off; the node setting is written by
		// the role instead.
		"sysctl:\n  enabled: false",
		"sysctlInit:\n  enabled: false",
		"serviceMonitor:\n  enabled: false",
		"plugins:\n  enabled: false",
		"protocol: https",
	} {
		if !strings.Contains(valuesText, want) {
			t.Fatalf("opensearch values do not contain %q", want)
		}
	}
	// The image must be built from the split parts with the registry in the
	// chart's own global field, which that chart prefixes with a slash.
	if !strings.Contains(valuesText, "dockerRegistry: {{ .ani.registry }}") {
		t.Fatal("opensearch must take its registry from the chart's global.dockerRegistry field")
	}
	if !strings.Contains(valuesText, "repository: {{ .ani.image_parts.logs.opensearch.repository }}") {
		t.Fatal("opensearch image must take its repository from the split image parts")
	}
	if !strings.Contains(valuesText, "tag: {{ .ani.image_parts.logs.opensearch.tag }}") {
		t.Fatal("opensearch image must take its tag from the split image parts")
	}
	// The chown init image is the locked busybox, also as split parts. The chart
	// prepends global.dockerRegistry to this reference itself, so only the
	// repository and tag belong here: repeating the registry would double it.
	if !strings.Contains(valuesText, "image: {{ .ani.image_parts.lab.busybox.repository }}") {
		t.Fatal("the chart's chown init image must be the locked busybox repository alone")
	}
	if strings.Contains(valuesText, "{{ .ani.registry }}/{{ .ani.image_parts.lab.busybox.repository }}") {
		t.Fatal("the chart already prepends global.dockerRegistry; the busybox repository must not repeat it")
	}
	// The storage must come from the logging block rather than being hardcoded.
	if !strings.Contains(valuesText, "storageClass: {{ .ani.components.logging.storage_class }}") {
		t.Fatal("opensearch storage must come from the logging storage class")
	}
	if !strings.Contains(valuesText, "size: {{ .ani.components.logging.storage_size }}") {
		t.Fatal("opensearch storage must come from the logging storage size")
	}
	// No credential may be written into a rendered values file.
	for _, bad := range []string{"password:", "hash:", "changeme", "admin:"} {
		if strings.Contains(valuesText, bad) {
			t.Fatalf("opensearch values appear to carry a credential (%q)", bad)
		}
	}

	// The security initialization is what makes the cluster usable: the
	// security index is not initialized by the plugin, so a Job must seed it,
	// and it must hash the password rather than ship one.
	init, err := os.ReadFile(filepath.Join(role, "templates", "security-init.yaml"))
	if err != nil {
		t.Fatalf("read opensearch security-init template: %v", err)
	}
	initText := string(init)
	for _, want := range []string{
		"kind: Job",
		"securityadmin.sh",
		"-cd ",
		"/admin-tls/ca.crt",
		"/admin-tls/tls.crt",
		"/admin-tls/tls.key",
		"hash.sh",
		"ani-opensearch-fluent-bit",
		"ani-opensearch-admin",
		"/dev/urandom",
		// The privilege is scoped to the two Secrets in one namespace.
		"kind: Role",
		"kind: RoleBinding",
		"runAsNonRoot: true",
		"allowPrivilegeEscalation: false",
	} {
		if !strings.Contains(initText, want) {
			t.Fatalf("opensearch security-init template does not contain %q", want)
		}
	}
	// No demo flag and no hardcoded password anywhere in the initialization.
	for _, bad := range []string{"--enable-demo", "admin/admin", "admin:\n", "-ts ", "changeme"} {
		if strings.Contains(initText, bad) {
			t.Fatalf("opensearch security-init appears to use a demo setting or a literal credential (%q)", bad)
		}
	}
	// The initialization Job must not be given cluster-wide Secret access: the
	// Role is namespaced and the binding references that Role, not a ClusterRole.
	if strings.Contains(initText, "kind: ClusterRole") {
		t.Fatal("the security initialization must not use a ClusterRole")
	}

	// The security configuration must carry two placeholder hashes and no other
	// credential, so the Job has exactly one thing to replace.
	config, err := os.ReadFile(filepath.Join(role, "templates", "security-config.yaml"))
	if err != nil {
		t.Fatalf("read opensearch security-config template: %v", err)
	}
	configText := string(config)
	placeholders := strings.Count(configText, "PLACEHOLDER-REPLACED-AT-INSTALL-TIME")
	if placeholders != 2 {
		t.Fatalf("the security configuration has %d hash placeholders, want exactly 2", placeholders)
	}
	for _, want := range []string{
		"type: \"internalusers\"",
		"type: \"rolesmapping\"",
		"type: \"nodesdn\"",
		"type: \"allowlist\"",
		"type: \"config\"",
		"anonymous_auth_enabled: false",
		"type: intern",
		"CN=ani-opensearch-node",
	} {
		if !strings.Contains(configText, want) {
			t.Fatalf("opensearch security configuration does not contain %q", want)
		}
	}
	// No demo user may be carried over from the upstream file.
	for _, bad := range []string{"kibanaro", "logstash:", "readall:", "snapshotrestore", "anomalyadmin", "admin_tenant"} {
		if strings.Contains(configText, bad) {
			t.Fatalf("the security configuration still carries the upstream demo entry %q", bad)
		}
	}
}

// TestOpenSearchNeedsStorageWhenSelected keeps the storage requirement where the
// failure is cheap: an OpenSearch with no volume would install and then lose
// every index on the first pod rebuild. It shares the logging storage settings
// with loki, because only one backend can ever be selected.
func TestOpenSearchNeedsStorageWhenSelected(t *testing.T) {
	base := "components:\n  certManager:\n    enabled: true\n  logging:\n    backend: opensearch\n"
	c, err := parseSite(t, base+"    storageClass: ''\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(c); err == nil {
		t.Fatal("opensearch with an empty storageClass must be rejected")
	}
	c, err = parseSite(t, base+"    storageSize: ''\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(c); err == nil {
		t.Fatal("opensearch with an empty storageSize must be rejected")
	}

	// With the defaults it must validate, and it must be storage-backed so the
	// two backends cannot disagree about what a volume is.
	c, err = parseSite(t, base)
	if err != nil {
		t.Fatalf("parse defaults: %v", err)
	}
	if err := Validate(c); err != nil {
		t.Fatalf("opensearch with the default storage settings must validate: %v", err)
	}
	storage := c.Components.storage("opensearch")
	if storage == nil {
		t.Fatal("opensearch must be treated as a storage-backed component")
	}
	if !storage.Enabled {
		t.Fatal("the opensearch storage row must be enabled when opensearch is the selected backend")
	}
	// The two backends are mutually exclusive, so selecting one must leave the
	// other's storage row disabled.
	if loki := c.Components.storage("loki"); loki != nil && loki.Enabled {
		t.Fatal("the loki storage row must not be enabled while opensearch is the selected backend")
	}
	if storage.StorageClass != c.Components.Logging.StorageClass {
		t.Fatal("opensearch storage must be the same settings the logging block carries")
	}
}

// TestFluentBitRoleIsWiredAndOffline pins the collection role: it must be gated
// on logging being enabled with a real backend, must ship exactly one output,
// and must never promote the full Kubernetes label set to Loki labels.
func TestFluentBitRoleIsWiredAndOffline(t *testing.T) {
	root := filepath.Join("..", "..")
	role := filepath.Join(root, "builtin", "core", "roles", "ani", "fluent-bit")

	tasks, err := os.ReadFile(filepath.Join(role, "tasks", "main.yaml"))
	if err != nil {
		t.Fatalf("read fluent-bit tasks: %v", err)
	}
	taskText := string(tasks)

	playbook, err := os.ReadFile(filepath.Join(root, "builtin", "core", "playbooks", "create_cluster.yaml"))
	if err != nil {
		t.Fatalf("read create_cluster.yaml: %v", err)
	}
	playbookText := string(playbook)
	if !strings.Contains(playbookText, "role: ani/fluent-bit") {
		t.Fatal("create_cluster.yaml does not reference ani/fluent-bit")
	}
	// Collection must be impossible without a backend: a collector with
	// backend "none" would only fill its local buffer.
	if !strings.Contains(playbookText,
		`when: '{{ and (index .ani.components "logging").enabled (ne .ani.components.logging.backend "none") (ne .ani.profile "base") }}'`) {
		t.Fatal("ani/fluent-bit is not gated on logging being enabled with a selected backend")
	}

	if !strings.Contains(taskText, "charts/fluent-bit/0.58.2.tgz") {
		t.Fatal("fluent-bit tasks do not install the packaged fluent-bit 0.58.2 Chart")
	}
	if !strings.Contains(taskText, "{{ .ani.artifact_root }}/bin/helm") {
		t.Fatal("fluent-bit tasks do not use the packaged Helm binary")
	}
	// The DaemonSet ready count must be compared against the node count, or a
	// collector missing on one node would silently drop that node's logs.
	for _, want := range []string{
		"daemonset/ani-fluent-bit --timeout=600s",
		"fluent-bit ready on",
		"chmod 0700 /etc/kubernetes/ani/fluent-bit/verify.sh",
	} {
		if !strings.Contains(taskText, want) {
			t.Fatalf("fluent-bit tasks do not contain %q", want)
		}
	}
	if bad := externalMaterialFetch(taskText); bad != "" {
		t.Fatalf("fluent-bit tasks appear to fetch from the network (%q)", bad)
	}

	values, err := os.ReadFile(filepath.Join(role, "templates", "values.yaml"))
	if err != nil {
		t.Fatalf("read fluent-bit values: %v", err)
	}
	valuesText := string(values)
	for _, want := range []string{
		"kind: DaemonSet",
		"testFramework:\n  enabled: false",
		"hotReload:\n  enabled: false",
		// Container logs only: the chart's systemd input reads kubelet's
		// journal and is out of scope for this batch.
		"Path /var/log/containers/*.log",
		"multiline.parser cri",
		// The cursor and buffer must be on the per-node persistent directory.
		"DB /var/lib/fluent-bit/tail.db",
		"storage.type filesystem",
		"storage.path /var/lib/fluent-bit/buffers",
		"storage.total_limit_size",
		// The whole Kubernetes label/annotation set must stay out of the
		// stream labels.
		"Labels Off",
		"Annotations Off",
		"Auto_Kubernetes_Labels Off",
	} {
		if !strings.Contains(valuesText, want) {
			t.Fatalf("fluent-bit values do not contain %q", want)
		}
	}
	// This chart has no registry field: its helper prints repository + ":" + tag,
	// so the repository must carry the whole host/path. A bare "fluent/fluent-bit"
	// would make every node pull from Docker Hub, which is unreachable here.
	if !strings.Contains(valuesText,
		"repository: {{ .ani.registry }}/{{ .ani.image_parts.logs.fluentBit.repository }}") {
		t.Fatal("fluent-bit repository must be prefixed with the site registry: this Chart has no registry field")
	}
	if !strings.Contains(valuesText, "tag: {{ .ani.image_parts.logs.fluentBit.tag }}") {
		t.Fatal("fluent-bit tag must come from the split image parts")
	}
	if strings.Contains(valuesText, "registry: {{ .ani.image_parts") {
		t.Fatal("the fluent-bit Chart has no registry field; the host belongs in repository")
	}
	// Exactly one output must be rendered for each backend, chosen by the
	// backend string, and both must be present as mutually exclusive branches.
	lokiOutput := strings.Count(valuesText, "Name loki")
	osOutput := strings.Count(valuesText, "Name opensearch")
	if lokiOutput != 1 || osOutput != 1 {
		t.Fatalf("expected one loki output and one opensearch output, got %d and %d", lokiOutput, osOutput)
	}
	for _, want := range []string{
		`{{- if eq .ani.components.logging.backend "loki" }}`,
		`{{- if eq .ani.components.logging.backend "opensearch" }}`,
	} {
		if !strings.Contains(valuesText, want) {
			t.Fatalf("fluent-bit values do not branch on the backend with %q", want)
		}
	}
	// The chart's own defaults must be gone: a leftover Elasticsearch output
	// would write every record a second time to a host that does not exist.
	if strings.Contains(valuesText, "Name es") {
		t.Fatal("the chart's default Elasticsearch output is still present, which would double-write")
	}
	if strings.Contains(valuesText, "Name systemd") {
		t.Fatal("the chart's systemd input is still present")
	}
	if strings.Contains(valuesText, "/var/lib/docker/containers") {
		t.Fatal("the chart's Docker container path is still present; this cluster runs containerd")
	}
	// The collector state directory must be a hostPath so the cursor survives
	// a pod rebuild, and the log directory must be mounted read-only.
	if !strings.Contains(valuesText, "/var/lib/ani-installer/fluent-bit") {
		t.Fatal("fluent-bit values do not put the cursor on a per-node hostPath")
	}
	if !strings.Contains(valuesText, "readOnly: true") {
		t.Fatal("the container log directory must be mounted read-only")
	}
	// No credential may be written into this file.
	for _, bad := range []string{"password:", "OS_PASSWORD:", "admin"} {
		if strings.Contains(valuesText, bad) {
			t.Fatalf("fluent-bit values appear to carry a credential (%q)", bad)
		}
	}
	// The pod-scoped environment must come from a Secret, not a literal.
	if !strings.Contains(valuesText, "secretKeyRef:") {
		t.Fatal("fluent-bit values must inject the OpenSearch credential from a Secret")
	}
}

// TestFluentBitVerifyProvesCollectionPath pins the C3 acceptance that no
// role-level check can substitute for: the markers must be found by querying
// the backend, after being written to a container's stdout. A script that
// pushed to the backend directly would prove the backend accepts writes and
// nothing about collection.
func TestFluentBitVerifyProvesCollectionPath(t *testing.T) {
	root := filepath.Join("..", "..")
	scriptPath := filepath.Join(root, "builtin", "core", "roles", "ani", "fluent-bit", "templates", "verify.sh")
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read fluent-bit verify.sh: %v", err)
	}
	script := string(data)

	for _, want := range []struct{ needle, why string }{
		{"/loki/api/v1/query_range?", "must query the Loki HTTP API for the markers"},
		{"ani-log-marker-", "must create a per-node marker pod"},
		{"ANI-MARKER-", "must write a unique marker to the container's stdout"},
		{"nodeName:", "must pin each marker pod to a specific node"},
		{"$EVIDENCE/markers-written.txt", "must know the expected marker from the pod's own stdout"},
		{"check_metadata.py", "must assert the pod/container/node metadata actually arrived"},
		{"tail.db", "must fingerprint the tail cursor before and after the rebuild"},
		{"delete pod \"$victim_pod\"", "must rebuild one collector pod"},
		{"storage.total_limit_size", "must confirm the local buffer is bounded"},
		{"retention-expiry=not_verified", "must record the expiry check as not verified rather than claim it"},
		{"rollout status", "must wait on the workload state rather than sleeping"},
	} {
		if !strings.Contains(script, want.needle) {
			t.Fatalf("fluent-bit verify.sh does not contain %q: it %s", want.needle, want.why)
		}
	}

	// The one thing that would invalidate the whole check: writing the marker
	// into the backend instead of letting the collector ship it.
	for _, bad := range []string{
		"/loki/api/v1/push",
		"/_bulk",
		"PUT /ani-logs",
	} {
		if strings.Contains(script, bad) {
			t.Fatalf("fluent-bit verify.sh writes to the backend directly (%q), which would fake collection", bad)
		}
	}
	// The rebuilds must be by explicit pod deletion, never by resetting or
	// wiping anything.
	for _, bad := range []string{
		"delete pvc",
		"delete namespace",
		"delete ns ",
		"--force",
		"--grace-period=0",
	} {
		if strings.Contains(script, bad) {
			t.Fatalf("fluent-bit verify.sh performs a destructive operation (%q)", bad)
		}
	}
	// The lab images must be the locked offline ones. The busybox key is
	// pinned to the full table key: A20 (evidence h4loki-a24) showed a
	// `busybox:1.37` spelling renders to a nil value and every marker pod
	// dies with InvalidImageName, so the shorter string must never come back.
	for _, want := range []string{
		`index .ani.images "docker.io/library/python:3.13.11-alpine3.23"`,
		`index .ani.images "docker.io/library/busybox:1.37.0"`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("fluent-bit verify.sh does not use the locked offline image %s", want)
		}
	}
	// This run's own test pods have to be removed.
	if !strings.Contains(script, "delete pod ani-log-marker-post") {
		t.Fatal("fluent-bit verify.sh does not clean up its post-rebuild marker pod")
	}
	// The script must fail loudly rather than continue past a broken step.
	if !strings.Contains(script, "set -euo pipefail") {
		t.Fatal("fluent-bit verify.sh does not fail fast")
	}
}

// TestLoggingRetentionUnitsDerivedFromDays pins the single unit conversion: the
// site states a day count, Loki needs an hour duration and OpenSearch needs an
// ISO-8601 duration. Deriving them in Go keeps a role template from doing
// arithmetic and keeps the two backends from disagreeing about the same value.
func TestLoggingRetentionUnitsDerivedFromDays(t *testing.T) {
	// There is no separate logging switch: selecting a backend is what enables
	// the log stack, so the fixture writes only `backend`.
	c, err := parseSite(t, "components:\n  logging:\n    backend: loki\n    retentionDays: 3\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	logging := componentSpec(c.Components, c.Name)["logging"].(map[string]any)
	if logging["retention_days"] != "3" {
		t.Fatalf("retention_days = %v, want 3", logging["retention_days"])
	}
	if logging["retention_hours"] != 72 {
		t.Fatalf("retention_hours = %v, want 72", logging["retention_hours"])
	}
	if logging["retention_iso"] != "PT72H" {
		t.Fatalf("retention_iso = %v, want PT72H", logging["retention_iso"])
	}
	// The log roles share the metrics namespace: one observability namespace,
	// which is what makes "no dashboards workload anywhere" checkable.
	if logging["namespace"] != MetricsNamespace {
		t.Fatalf("logging namespace = %v, want %s", logging["namespace"], MetricsNamespace)
	}

	// An invalid value must not render a nonsense duration. Validation rejects
	// it before a role could render, but the derivation must still be safe.
	for _, bad := range []string{"not-a-number", "0", "-1", ""} {
		if got := retentionHours(bad); got != 0 {
			t.Fatalf("retentionHours(%q) = %d, want 0", bad, got)
		}
		if got := retentionISOSeconds(bad); got != "" {
			t.Fatalf("retentionISOSeconds(%q) = %q, want empty", bad, got)
		}
	}
}

// TestLokiNeedsStorageWhenSelected keeps the storage requirement where the
// failure is cheap: a loki monolith with no volume would install and then lose
// every log on the first pod rebuild.
func TestLokiNeedsStorageWhenSelected(t *testing.T) {
	c, err := parseSite(t, "components:\n  logging:\n    backend: loki\n    storageClass: ''\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(c); err == nil {
		t.Fatal("loki with an empty storageClass must be rejected")
	}

	// fluent-bit is not storage-backed: it must validate without any volume.
	c, err = parseSite(t, "components:\n  logging:\n    backend: loki\n")
	if err != nil {
		t.Fatalf("parse defaults: %v", err)
	}
	if err := Validate(c); err != nil {
		t.Fatalf("loki with the default storage settings must validate: %v", err)
	}
	if storage := c.Components.storage("fluent-bit"); storage != nil {
		t.Fatal("fluent-bit must not be treated as a storage-backed component")
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

// ---------------------------------------------------------------------------
// R02 (A01): the verification path must never repair the cluster it verifies,
// and the top-level component loop must stop at the first failure.
//
// These tests drive the *rendered* role scripts and the real component loop
// with a fake kubectl on PATH. They are behavioural: the K-5 helpers are the
// real ones from the templates, so re-adding an agent restart, a second rebuild
// or a post-Ready re-entry makes them fail.
// ---------------------------------------------------------------------------

// r02Timestamp is the fake kubectl's marker for its own call log.
const r02TemplateKind = "verify.sh"

func r02RenderScript(t *testing.T, relPath string, ctx map[string]any) string {
	t.Helper()
	tmpl, err := template.New(r02TemplateKind).ParseFiles(relPath)
	if err != nil {
		t.Fatalf("parse %s: %v", relPath, err)
	}
	rendered := &strings.Builder{}
	if err := tmpl.Execute(rendered, ctx); err != nil {
		t.Fatalf("execute %s: %v", relPath, err)
	}
	out := rendered.String()
	if strings.Contains(out, "{{") {
		t.Fatalf("rendered %s still contains template delimiters", relPath)
	}
	path := filepath.Join(t.TempDir(), "verify.sh")
	if err := os.WriteFile(path, []byte(out), 0o700); err != nil {
		t.Fatalf("write rendered %s: %v", relPath, err)
	}
	return path
}

func r02Context() map[string]any {
	return map[string]any{
		"ani": map[string]any{
			"registry": "192.0.2.11:5000",
			"images": map[string]string{
				"docker.io/library/python:3.13.11-alpine3.23": "192.0.2.11:5000/library/python:3.13.11-alpine3.23",
				"docker.io/library/busybox:1.37.0":            "192.0.2.11:5000/library/busybox:1.37.0",
				"docker.io/alpine/openssl:3.5.4":              "192.0.2.11:5000/alpine/openssl:3.5.4",
			},
			"components": map[string]any{
				"metrics": map[string]any{
					"enabled":       true,
					"namespace":     "ani-observability",
					"run_id":        "ani-ani-lab",
					"storage_class": "ani-block",
				},
				"logging": map[string]any{
					"enabled":   true,
					"namespace": "ani-observability",
					"backend":   "loki",
				},
			},
		},
		"kubernetes": map[string]any{"cluster_name": "ani-lab"},
	}
}

// r02FakeKubectl writes a fake kubectl that records every argv and answers the
// few queries the K-5 helpers make. It never contacts a cluster.
func r02FakeKubectl(t *testing.T) (binDir string, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "kubectl-calls.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatalf("create kubectl log: %v", err)
	}
	// The kcn data-plane queries only exist so the pre-R02 control script can
	// follow its old recovery path without waiting: the fake answers them and
	// never sleeps.
	body := `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$KUBECTL_LOG"
case "$*" in
  *"get pods"*)
    echo "kcn-cni-ds-abc node1"; exit 0 ;;
  *"containerStatuses"*)
    echo "true"; exit 0 ;;
  *"get pod"*"-o wide"*)
    if [ -n "${FAKE_FAIL_DIAG:-}" ]; then echo "fake kubectl: api unavailable" >&2; exit 1; fi
    echo "NAME READY STATUS"; exit 0 ;;
  *"get events"*)
    if [ -n "${FAKE_FAIL_DIAG:-}" ]; then echo "fake kubectl: api unavailable" >&2; exit 1; fi
    echo "LAST SEEN TYPE REASON"; exit 0 ;;
  *"get pod"*".spec.nodeName"*)
    echo "node1"; exit 0 ;;
  *"get pod"*".items[0].metadata.name"*)
    echo "ani-metrics-prometheus-0"; exit 0 ;;
  *"get pod"*"jsonpath"*"Ready"*)
    if [ -n "${FAKE_READY:-}" ]; then echo "True"; fi
    exit 0 ;;
  *"get pod"*"-o name"*)
    exit 0 ;;
  *"describe"*)
    if [ -n "${FAKE_FAIL_DIAG:-}" ]; then exit 1; fi
    echo "fake describe"; exit 0 ;;
esac
exit 0
`
	script := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("write fake kubectl: %v", err)
	}
	// A no-op sleep keeps the bounded waits deterministic and instant; the
	// deadline arithmetic still uses the real clock, so a 0s timeout performs
	// no iteration and a satisfied wait returns immediately.
	noSleep := "#!/usr/bin/env bash\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "sleep"), []byte(noSleep), 0o700); err != nil {
		t.Fatalf("write fake sleep: %v", err)
	}
	return dir, logPath
}

func r02RunBash(t *testing.T, script string, env map[string]string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+env["PATH"])
	for key, value := range env {
		if key == "PATH" {
			continue
		}
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	outBuf := &strings.Builder{}
	errBuf := &strings.Builder{}
	cmd.Stdout = outBuf
	cmd.Stderr = errBuf
	err := cmd.Run()
	code = 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run bash: %v", err)
		}
		code = exitErr.ExitCode()
	}
	return outBuf.String(), errBuf.String(), code
}

func r02Count(logContent, needle string) int {
	return strings.Count(logContent, needle)
}

// TestVerifyNeverRepairsNetwork: a K-5 wait that never sees a Ready pod must
// fail the check, and the call log must contain no kcn-system/CNI/OVS mutation,
// no repeated rebuild and no post-Ready recovery re-entry.
func TestVerifyNeverRepairsNetwork(t *testing.T) {
	metricsRel := filepath.Join("..", "..", "builtin", "core", "roles", "ani", "metrics", "templates", "verify.sh")
	fluentRel := filepath.Join("..", "..", "builtin", "core", "roles", "ani", "fluent-bit", "templates", "verify.sh")

	t.Run("metrics/unready pod fails without repairing the datapath", func(t *testing.T) {
		rendered := r02RenderScript(t, metricsRel, r02Context())
		binDir, callLog := r02FakeKubectl(t)
		outDir := t.TempDir()
		script := `set +e
export ANI_VERIFY_LIB_ONLY=1
export ANI_VERIFY_OUTPUT_DIR="$OUT_DIR"
export KUBECTL_LOG="$KUBECTL_LOG"
source "$RENDERED"
set +e
k5_rebuild_wait "app.kubernetes.io/name=prometheus" "prometheus" 0s 0s
rc=$?
echo "REBUILD_RC=$rc"
`
		stdout, stderr, code := r02RunBash(t, script, map[string]string{
			"PATH":        binDir + ":" + os.Getenv("PATH"),
			"RENDERED":    rendered,
			"OUT_DIR":     outDir,
			"KUBECTL_LOG": callLog,
		})
		if code != 0 {
			t.Fatalf("harness failed: %s\n%s", stdout, stderr)
		}
		if !strings.Contains(stdout, "REBUILD_RC=1") {
			t.Fatalf("k5_rebuild_wait must return non-zero when the pod never becomes Ready; got:\n%s\n%s", stdout, stderr)
		}
		logContent, err := os.ReadFile(callLog)
		if err != nil {
			t.Fatalf("read kubectl log: %v", err)
		}
		calls := string(logContent)
		for _, forbidden := range []string{"kcn-system", "rollout restart", "patch ", "kcn-cni", "kcn-ovs"} {
			if strings.Contains(calls, forbidden) {
				t.Fatalf("verification touched the network layer (%q):\n%s", forbidden, calls)
			}
		}
		if got := r02Count(calls, "delete pod -l app.kubernetes.io/name=prometheus"); got != 1 {
			t.Fatalf("expected exactly one planned rebuild of the component's own pod, got %d:\n%s", got, calls)
		}
		evidence, err := os.ReadFile(filepath.Join(outDir, "k5-failure-prometheus.txt"))
		if err != nil {
			t.Fatalf("failure evidence was not written: %v", err)
		}
		if !strings.Contains(string(evidence), "get events") && !strings.Contains(calls, "get events") {
			t.Fatalf("failure evidence did not collect events:\n%s\n%s", string(evidence), calls)
		}
	})

	t.Run("metrics/normal path still waits and never rebuilds", func(t *testing.T) {
		rendered := r02RenderScript(t, metricsRel, r02Context())
		binDir, callLog := r02FakeKubectl(t)
		script := `set +e
export ANI_VERIFY_LIB_ONLY=1
export ANI_VERIFY_OUTPUT_DIR="$OUT_DIR"
export KUBECTL_LOG="$KUBECTL_LOG"
source "$RENDERED"
set +e
export FAKE_READY=1
k5_rebuild_wait "app.kubernetes.io/name=prometheus" "prometheus" 5s 5s
echo "REBUILD_RC=$?"
`
		stdout, stderr, code := r02RunBash(t, script, map[string]string{
			"PATH":        binDir + ":" + os.Getenv("PATH"),
			"RENDERED":    rendered,
			"OUT_DIR":     t.TempDir(),
			"KUBECTL_LOG": callLog,
			"FAKE_READY":  "1",
		})
		// FAKE_READY has to reach the fake kubectl, so it is exported for the call.
		if code != 0 {
			t.Fatalf("harness failed: %s\n%s", stdout, stderr)
		}
		if !strings.Contains(stdout, "REBUILD_RC=0") {
			t.Fatalf("a Ready pod must pass the wait; got:\n%s\n%s", stdout, stderr)
		}
		logContent, err := os.ReadFile(callLog)
		if err != nil {
			t.Fatalf("read kubectl log: %v", err)
		}
		if strings.Contains(string(logContent), "delete pod") {
			t.Fatalf("no rebuild may happen on the normal path:\n%s", string(logContent))
		}
	})

	t.Run("metrics/failing diagnostics keep the original failure", func(t *testing.T) {
		rendered := r02RenderScript(t, metricsRel, r02Context())
		binDir, callLog := r02FakeKubectl(t)
		script := `set +e
export ANI_VERIFY_LIB_ONLY=1
export ANI_VERIFY_OUTPUT_DIR="$OUT_DIR"
export KUBECTL_LOG="$KUBECTL_LOG"
source "$RENDERED"
set +e
k5_rebuild_wait "app.kubernetes.io/name=prometheus" "prometheus" 0s 0s
echo "REBUILD_RC=$?"
`
		stdout, stderr, code := r02RunBash(t, script, map[string]string{
			"PATH":           binDir + ":" + os.Getenv("PATH"),
			"RENDERED":       rendered,
			"OUT_DIR":        t.TempDir(),
			"KUBECTL_LOG":    callLog,
			"FAKE_FAIL_DIAG": "1",
		})
		if code != 0 {
			t.Fatalf("harness failed: %s\n%s", stdout, stderr)
		}
		if !strings.Contains(stdout, "REBUILD_RC=1") {
			t.Fatalf("a failing diagnostic must not change the primary failure: got\n%s\n%s", stdout, stderr)
		}
	})

	t.Run("control/HEAD recovery chain is detected", func(t *testing.T) {
		// The discriminating control for this test: the pre-R02 script is
		// materialised from git HEAD and driven with the same fake kubectl. It
		// must show the repair behaviour (a second rebuild plus a kcn-system
		// lookup) that the current script no longer performs.
		head, err := exec.Command("git", "show", "HEAD:kubekey/builtin/core/roles/ani/metrics/templates/verify.sh").Output()
		if err != nil {
			t.Skipf("git HEAD content unavailable: %v", err)
		}
		// The pre-R02 file has no test seam, so only its helper definitions are
		// taken (from `k5_pod_ready() {` up to the first body assignment). This
		// is a control on the OLD text, not a copy of the current product test.
		text := string(head)
		from := strings.Index(text, "k5_pod_ready() {")
		to := strings.Index(text, "\nPROM_STS=")
		if from < 0 || to < from {
			t.Fatalf("could not locate the pre-R02 helper block")
		}
		dir := t.TempDir()
		legacyHelpers := filepath.Join(dir, "legacy-helpers.sh")
		helpers := "NS=ani-observability\nKUBECTL=(kubectl --kubeconfig /dev/null)\n" + text[from:to] + "\n"
		if err := os.WriteFile(legacyHelpers, []byte(helpers), 0o600); err != nil {
			t.Fatalf("write legacy helpers: %v", err)
		}
		binDir, callLog := r02FakeKubectl(t)
		script := `set +e
export KUBECTL_LOG="$KUBECTL_LOG"
export OUT_DIR="$OUT_DIR"
source "$LEGACY_HELPERS"
k5_rebuild_wait "app.kubernetes.io/name=prometheus" "prometheus" 0s 0s 0s
echo "REBUILD_RC=$?"
`
		stdout, stderr, code := r02RunBash(t, script, map[string]string{
			"PATH":           binDir + ":" + os.Getenv("PATH"),
			"LEGACY_HELPERS": legacyHelpers,
			"OUT_DIR":        t.TempDir(),
			"KUBECTL_LOG":    callLog,
		})
		if code != 0 {
			t.Fatalf("harness failed: %s\n%s", stdout, stderr)
		}
		logContent, err := os.ReadFile(callLog)
		if err != nil {
			t.Fatalf("read kubectl log: %v", err)
		}
		calls := string(logContent)
		if !strings.Contains(calls, "kcn-system") {
			t.Fatalf("the pre-R02 script should have touched kcn-system; the control does not reproduce it:\n%s", calls)
		}
		if got := r02Count(calls, "delete pod -l app.kubernetes.io/name=prometheus"); got < 2 {
			t.Fatalf("the pre-R02 script should rebuild twice, got %d deletes:\n%s", got, calls)
		}
	})

	t.Run("fluent-bit/data-plane repair helpers are gone", func(t *testing.T) {
		rendered := r02RenderScript(t, fluentRel, r02Context())
		binDir, callLog := r02FakeKubectl(t)
		script := `set +e
export ANI_VERIFY_LIB_ONLY=1
export ANI_VERIFY_OUTPUT_DIR="$OUT_DIR"
export KUBECTL_LOG="$KUBECTL_LOG"
source "$RENDERED"
set +e
for fn in k5_dp_agent_restart k5_dp_agent_wait k5_dp_pod_node k5_read_again; do
  if declare -F "$fn" >/dev/null 2>&1; then echo "STILL_DEFINED=$fn"; fi
done
k5_pod_ready "app.kubernetes.io/name=fluent-bit" 0s
echo "POD_READY_RC=$?"
export FAKE_FAIL_DIAG=1
k5_collect_failure_evidence backend || true
echo "EVIDENCE_TOLERATED_RC=$?"
ls "$EVIDENCE"/k5-failure-backend.txt >/dev/null 2>&1 && echo "EVIDENCE_FILE=yes"
`
		stdout, stderr, code := r02RunBash(t, script, map[string]string{
			"PATH":           binDir + ":" + os.Getenv("PATH"),
			"RENDERED":       rendered,
			"OUT_DIR":        t.TempDir(),
			"KUBECTL_LOG":    callLog,
			"FAKE_FAIL_DIAG": "1",
		})
		if code != 0 {
			t.Fatalf("harness failed: %s\n%s", stdout, stderr)
		}
		if strings.Contains(stdout, "STILL_DEFINED=") {
			t.Fatalf("a data-plane repair helper still exists:\n%s", stdout)
		}
		if !strings.Contains(stdout, "POD_READY_RC=1") {
			t.Fatalf("an unready backend must fail the bounded wait:\n%s\n%s", stdout, stderr)
		}
		if !strings.Contains(stdout, "EVIDENCE_TOLERATED_RC=0") {
			t.Fatalf("a failing diagnostic must not kill the caller (|| true):\n%s\n%s", stdout, stderr)
		}
		if !strings.Contains(stdout, "EVIDENCE_FILE=yes") {
			t.Fatalf("the failure evidence file was not written:\n%s\n%s", stdout, stderr)
		}
		if strings.Contains(stdout, "kcn-system") {
			t.Fatalf("fluent-bit verification named the network layer:\n%s", stdout)
		}
	})
}

// TestVerifyStopsAfterFirstFailure: the component loop must run every enabled
// component on the clean path, and must stop at the first failure, recording
// the remaining enabled components as not_run without executing their scripts.
func TestVerifyStopsAfterFirstFailure(t *testing.T) {
	verifyRel := filepath.Join("..", "..", "scripts", "verify.sh")
	binDir, callLog := r02FakeKubectl(t)

	writeSelection := func(t *testing.T, configPath string, enabled map[string]bool) string {
		t.Helper()
		raw, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read config: %v", err)
		}
		sum := sha256.Sum256(raw)
		names := []string{"cert-manager", "postgresql", "valkey", "nats", "metrics", "loki", "opensearch", "fluent-bit"}
		var b strings.Builder
		fmt.Fprintf(&b, "# config_sha256=%s\n", hex.EncodeToString(sum[:]))
		for _, name := range names {
			state := "false"
			if enabled[name] {
				state = "true"
			}
			fmt.Fprintf(&b, "%s\t%s\n", name, state)
		}
		path := filepath.Join(t.TempDir(), "components-selection.tsv")
		if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
			t.Fatalf("write selection: %v", err)
		}
		return path
	}

	stubDir := func(t *testing.T, certManagerExit int, markerDir string) string {
		t.Helper()
		dir := t.TempDir()
		certDir := filepath.Join(dir, "cert-manager")
		pgDir := filepath.Join(dir, "postgresql")
		if err := os.MkdirAll(certDir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.MkdirAll(pgDir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		cert := "#!/usr/bin/env bash\necho 'cert-manager stub ran'\nexit " + strconv.Itoa(certManagerExit) + "\n"
		pg := "#!/usr/bin/env bash\necho 'postgresql stub ran'\ntouch " + markerDir + "/postgresql-ran\n"
		if err := os.WriteFile(filepath.Join(certDir, "verify.sh"), []byte(cert), 0o700); err != nil {
			t.Fatalf("write cert stub: %v", err)
		}
		if err := os.WriteFile(filepath.Join(pgDir, "verify.sh"), []byte(pg), 0o700); err != nil {
			t.Fatalf("write pg stub: %v", err)
		}
		return dir
	}

	configPath := filepath.Join(t.TempDir(), "site.yaml")
	if err := os.WriteFile(configPath, []byte("name: ani-lab\ninstallerNode: node1\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runLoop := func(t *testing.T, selection, scriptDir, logDir string) (string, string, int) {
		t.Helper()
		return r02RunBash(t, `set +e
export ANI_VERIFY_LIB_ONLY=1
export KUBECONFIG_FILE=/dev/null
export KUBECTL_LOG="$KUBECTL_LOG"
source "$VERIFY"
set +e
ani_verify_components "$CONFIG" "$SELECTION" "$SCRIPT_DIR" "$LOG_DIR"
echo "LOOP_RC=$?"
`, map[string]string{
			"PATH":        binDir + ":" + os.Getenv("PATH"),
			"VERIFY":      verifyRel,
			"CONFIG":      configPath,
			"SELECTION":   selection,
			"SCRIPT_DIR":  scriptDir,
			"LOG_DIR":     logDir,
			"KUBECTL_LOG": callLog,
		})
	}

	t.Run("first failure stops the loop and later components are not_run", func(t *testing.T) {
		markerDir := t.TempDir()
		scriptDir := stubDir(t, 3, markerDir)
		selection := writeSelection(t, configPath, map[string]bool{"cert-manager": true, "postgresql": true})
		logDir := t.TempDir()
		stdout, stderr, code := runLoop(t, selection, scriptDir, logDir)
		if code != 0 {
			t.Fatalf("harness failed: %s\n%s", stdout, stderr)
		}
		if !strings.Contains(stdout, "LOOP_RC=3") {
			t.Fatalf("the loop must return the FIRST failure's exit code (3); got:\n%s\n%s", stdout, stderr)
		}
		if !strings.Contains(stderr, "postgresql=not_run") {
			t.Fatalf("the later enabled component must be recorded not_run:\n%s", stderr)
		}
		if _, err := os.Stat(filepath.Join(markerDir, "postgresql-ran")); err == nil {
			t.Fatalf("the second component's verify script ran after the first failure")
		}
	})

	t.Run("clean path still runs every enabled component", func(t *testing.T) {
		markerDir := t.TempDir()
		scriptDir := stubDir(t, 0, markerDir)
		selection := writeSelection(t, configPath, map[string]bool{"cert-manager": true, "postgresql": true})
		logDir := t.TempDir()
		stdout, stderr, code := runLoop(t, selection, scriptDir, logDir)
		if code != 0 {
			t.Fatalf("harness failed: %s\n%s", stdout, stderr)
		}
		if !strings.Contains(stdout, "LOOP_RC=0") {
			t.Fatalf("the clean path must return 0; got:\n%s\n%s", stdout, stderr)
		}
		if _, err := os.Stat(filepath.Join(markerDir, "postgresql-ran")); err != nil {
			t.Fatalf("the second enabled component did not run on the clean path: %v", err)
		}
		if !strings.Contains(stdout+stderr, "cert-manager=pass") || !strings.Contains(stdout+stderr, "postgresql=pass") {
			t.Fatalf("the summary must show both passes:\n%s\n%s", stdout, stderr)
		}
	})
}

// ---------------------------------------------------------------------------
// R05 (A02): storage is explicit site input. These tests drive the real site
// parsing, the real template context (KubeKeyConfig) and the real templates, so
// a re-introduced implicit Ceph, a scanned disk or a default-class grab fails.
// ---------------------------------------------------------------------------

const r05Site = `name: ani-lab
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
%s
`

const r05CephStorage = `storage:
  enabled: true
  provider: ceph
  nodes:
    - {name: node1, devices: [/dev/disk/by-id/ata-ani-data-01]}
    - {name: node2, devices: [/dev/disk/by-id/ata-ani-data-02]}
    - {name: node3, devices: [/dev/disk/by-id/ata-ani-data-03]}
`

func r05Parse(t *testing.T, suffix string) (ClusterConfig, error) {
	t.Helper()
	return ParseClusterConfig([]byte(strings.Replace(r05Site, "%s", suffix, 1)))
}

func r05MustValidate(t *testing.T, suffix string) ClusterConfig {
	t.Helper()
	c, err := r05Parse(t, suffix)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(c); err != nil {
		t.Fatalf("expected the config to validate, got: %v", err)
	}
	return c
}

// r05RenderTemplate renders a role template with the real installer context.
// extra carries the variables a looped task would see (item, groups) so a role
// task file can be rendered as one iteration of its loop.
func r05RenderTemplate(t *testing.T, path string, c ClusterConfig, extra ...map[string]any) string {
	t.Helper()
	spec, err := KubeKeyConfig(c, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", testImageTable())
	if err != nil {
		t.Fatalf("KubeKeyConfig: %v", err)
	}
	for _, add := range extra {
		for key, value := range add {
			spec[key] = value
		}
	}
	tmpl, err := template.New(filepath.Base(path)).Funcs(kkTmpl.FuncMap()).ParseFiles(path)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	rendered := &strings.Builder{}
	if err := tmpl.Execute(rendered, spec); err != nil {
		t.Fatalf("execute %s: %v", path, err)
	}
	return rendered.String()
}

// r05EvalCondition renders a role's `when:` condition with the real context and
// returns the resulting expression text ("true"/"false").
func r05EvalCondition(t *testing.T, condition string, c ClusterConfig) string {
	t.Helper()
	spec, err := KubeKeyConfig(c, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", testImageTable())
	if err != nil {
		t.Fatalf("KubeKeyConfig: %v", err)
	}
	tmpl, err := template.New("when").Funcs(kkTmpl.FuncMap()).Parse(condition)
	if err != nil {
		t.Fatalf("parse condition %q: %v", condition, err)
	}
	rendered := &strings.Builder{}
	if err := tmpl.Execute(rendered, spec); err != nil {
		t.Fatalf("execute condition %q: %v", condition, err)
	}
	return strings.TrimSpace(rendered.String())
}

// r05PlaybookWhen returns the `when:` line that guards the named role.
func r05PlaybookWhen(t *testing.T, role string) string {
	t.Helper()
	path := filepath.Join("..", "..", "builtin", "core", "playbooks", "create_cluster.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read playbook: %v", err)
	}
	lines := strings.Split(string(raw), "\n")
	for index, line := range lines {
		if strings.TrimSpace(line) != "- role: "+role {
			continue
		}
		for _, next := range lines[index+1:] {
			if strings.HasPrefix(strings.TrimSpace(next), "when:") && strings.HasPrefix(next, "      ") {
				// The playbook writes the condition as a single-quoted YAML
				// scalar; the engine sees it without those quotes.
				condition := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(next), "when:"))
				return strings.Trim(condition, "'")
			}
			if strings.HasPrefix(strings.TrimSpace(next), "- ") {
				break
			}
		}
		t.Fatalf("role %s has no when: condition", role)
	}
	t.Fatalf("playbook has no role %s", role)
	return ""
}

// T-R05-01
func TestCephRequiresExplicitSelection(t *testing.T) {
	// (a) profile: full alone must no longer mean "install Ceph": the config
	//     stays valid, but nothing about it selects storage.
	fullOnly, err := r05Parse(t, "profile: full")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(fullOnly); err != nil {
		t.Fatalf("profile: full with no storage and no components is a valid base cluster: %v", err)
	}
	if fullOnly.Storage.Enabled {
		t.Fatal("storage must default to disabled")
	}

	// (b) the switch must not be contradicted.
	for name, suffix := range map[string]string{
		"provider without enabled":     "storage: {provider: ceph}\n",
		"nodes without enabled":        "storage: {nodes: [{name: node1, devices: [/dev/disk/by-id/a]}]}\n",
		"default flag without enabled": "storage: {makeDefaultStorageClass: true}\n",
	} {
		c, err := r05Parse(t, suffix)
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if err := Validate(c); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}

	// (c) the playbook guard must be the explicit storage switch, not the profile.
	cephWhen := r05PlaybookWhen(t, "ani/ceph")
	storageclassWhen := r05PlaybookWhen(t, "storageclass")
	for role, condition := range map[string]string{"ani/ceph": cephWhen, "storageclass": storageclassWhen} {
		if !strings.Contains(condition, ".ani.storage.enabled") {
			t.Fatalf("%s is not guarded by .ani.storage.enabled: %s", role, condition)
		}
	}
	if !strings.Contains(cephWhen, ".ani.storage.provider") {
		t.Fatalf("the Ceph role must also require provider=ceph: %s", cephWhen)
	}

	// (d) rendering that condition with the real context: off -> false.
	off := r05MustValidate(t, "")
	if got := r05EvalCondition(t, cephWhen, off); got != "false" {
		t.Fatalf("with storage disabled the Ceph guard renders %q, want false", got)
	}
	// An external provider must not run the Ceph role either.
	external := r05MustValidate(t, "storage: {enabled: true, provider: external, externalClass: ani-external}\n")
	if got := r05EvalCondition(t, cephWhen, external); got != "false" {
		t.Fatalf("with provider=external the Ceph guard renders %q, want false", got)
	}
	cephOn := r05MustValidate(t, r05CephStorage)
	if got := r05EvalCondition(t, cephWhen, cephOn); got != "true" {
		t.Fatalf("with storage.enabled+ceph the guard renders %q, want true", got)
	}

	// (e) the CephCluster template must not scan for disks any more.
	clusterYAML := r05RenderTemplate(t, filepath.Join("..", "..", "builtin", "core", "roles", "ani", "ceph", "templates", "cluster.yaml"), cephOn)
	for _, forbidden := range []string{"deviceFilter", "useAllNodes: true", "useAllDevices: true"} {
		if strings.Contains(clusterYAML, forbidden) {
			t.Fatalf("rendered CephCluster still contains %q", forbidden)
		}
	}
	if !strings.Contains(clusterYAML, "useAllNodes: false") || !strings.Contains(clusterYAML, "useAllDevices: false") {
		t.Fatal("rendered CephCluster must pin useAllNodes/useAllDevices to false")
	}
	for _, device := range []string{"/dev/disk/by-id/ata-ani-data-01", "/dev/disk/by-id/ata-ani-data-02", "/dev/disk/by-id/ata-ani-data-03"} {
		if !strings.Contains(clusterYAML, device) {
			t.Fatalf("rendered CephCluster is missing the declared device %s", device)
		}
	}
}

// T-R05-02 / T-R05-03
func TestCephDeviceAllowlist(t *testing.T) {
	// The allowlist is only what the site declared.
	rejected := map[string]string{
		"missing node declaration": `storage:
  enabled: true
  provider: ceph
  nodes:
    - {name: node1, devices: [/dev/disk/by-id/a]}
`,
		"node without devices": `storage:
  enabled: true
  provider: ceph
  nodes:
    - {name: node1, devices: [/dev/disk/by-id/a]}
    - {name: node2, devices: []}
    - {name: node3, devices: [/dev/disk/by-id/c]}
`,
		"relative device path": `storage:
  enabled: true
  provider: ceph
  nodes:
    - {name: node1, devices: [sdb]}
    - {name: node2, devices: [/dev/disk/by-id/b]}
    - {name: node3, devices: [/dev/disk/by-id/c]}
`,
		"node declared twice": `storage:
  enabled: true
  provider: ceph
  nodes:
    - {name: node1, devices: [/dev/disk/by-id/a]}
    - {name: node1, devices: [/dev/disk/by-id/b]}
    - {name: node3, devices: [/dev/disk/by-id/c]}
`,
		"unknown node": `storage:
  enabled: true
  provider: ceph
  nodes:
    - {name: node1, devices: [/dev/disk/by-id/a]}
    - {name: node2, devices: [/dev/disk/by-id/b]}
    - {name: node9, devices: [/dev/disk/by-id/c]}
`,
		"ceph with externalClass": `storage:
  enabled: true
  provider: ceph
  externalClass: ani-external
  nodes:
    - {name: node1, devices: [/dev/disk/by-id/a]}
    - {name: node2, devices: [/dev/disk/by-id/b]}
    - {name: node3, devices: [/dev/disk/by-id/c]}
`,
		"external without a class":       "storage: {enabled: true, provider: external}\n",
		"external with devices":          "storage: {enabled: true, provider: external, externalClass: x, nodes: [{name: node1, devices: [/dev/disk/by-id/a]}]}\n",
		"external making itself default": "storage: {enabled: true, provider: external, externalClass: x, makeDefaultStorageClass: true}\n",
		"unknown provider":               "storage: {enabled: true, provider: nfs}\n",
	}
	for name, suffix := range rejected {
		c, err := r05Parse(t, suffix)
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if err := Validate(c); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}

	// R15.3 field finding: device paths are per-node namespaces. The same
	// path on two DIFFERENT nodes is the blueprint-allowed VM-farm shape
	// (/dev/sdb on every node) and must validate; the same path twice on ONE
	// node is still a declaration error.
	vmFarm, err := r05Parse(t, `storage:
  enabled: true
  provider: ceph
  makeDefaultStorageClass: true
  nodes:
    - {name: node1, devices: [/dev/sdb]}
    - {name: node2, devices: [/dev/sdb]}
    - {name: node3, devices: [/dev/sdb]}
`)
	if err != nil {
		t.Fatalf("parse vm-farm fixture: %v", err)
	}
	if err := Validate(vmFarm); err != nil {
		t.Fatalf("the blueprint-allowed /dev/sdb-per-node site must validate: %v", err)
	}
	if got := vmFarm.Storage.Nodes[0].Devices; len(got) != 1 || got[0] != "/dev/sdb" {
		t.Fatalf("vm-farm fixture devices: %+v", got)
	}
	sameNodeTwice, parseErr := r05Parse(t, `storage:
  enabled: true
  provider: ceph
  nodes:
    - {name: node1, devices: [/dev/sdb, /dev/sdb]}
    - {name: node2, devices: [/dev/sdb]}
    - {name: node3, devices: [/dev/sdb]}
`)
	if parseErr != nil {
		t.Fatalf("parse same-node-twice fixture: %v", parseErr)
	}
	if err := Validate(sameNodeTwice); err == nil {
		t.Fatal("the same device listed twice on one node must be rejected")
	}

	// A partial declaration must name the missing nodes and must not suggest
	// useAllNodes as a way out.
	c, err := r05Parse(t, `storage:
  enabled: true
  provider: ceph
  nodes:
    - {name: node1, devices: [/dev/disk/by-id/a]}
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	err = Validate(c)
	if err == nil {
		t.Fatal("authorising only node1 on a three-node topology must be rejected")
	}
	if !strings.Contains(err.Error(), "node2") || !strings.Contains(err.Error(), "node3") {
		t.Fatalf("error = %v, want it to name the nodes without a declared device", err)
	}
	if strings.Contains(err.Error(), "useAllNodes") {
		t.Fatalf("error = %v, must not offer useAllNodes as a fix", err)
	}

	// A declaration for a node outside the topology is a typo, not a new node.
	valid := r05MustValidate(t, r05CephStorage)
	if got := valid.Storage.Nodes; len(got) != 3 {
		t.Fatalf("valid fixture parsed %d storage nodes, want 3", len(got))
	}
}

// T-R05-04
func TestDefaultStorageClassConflict(t *testing.T) {
	// The flag is refused where the class is not ours to mark.
	for name, suffix := range map[string]string{
		"external provider": "storage: {enabled: true, provider: external, externalClass: x, makeDefaultStorageClass: true}\n",
		"storage disabled":  "storage: {makeDefaultStorageClass: true}\n",
	} {
		c, err := r05Parse(t, suffix)
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if err := Validate(c); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}

	// With makeDefaultStorageClass=false the task must not have any patch at all.
	taskPath := filepath.Join("..", "..", "builtin", "core", "roles", "ani", "ceph", "tasks", "main.yaml")
	loopCtx := map[string]any{
		"item":   "node1",
		"groups": map[string]any{"k8s_cluster": []any{"node1", "node2", "node3"}},
	}
	raw, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatalf("read ceph tasks: %v", err)
	}
	tasks := string(raw)
	if strings.Contains(tasks, "is-default-class\":\"false\"") {
		t.Fatal("the storage tasks still clear other components' default StorageClass markers")
	}
	if !strings.Contains(tasks, "makeDefaultStorageClass") {
		t.Fatal("the default-class task does not read the site's makeDefaultStorageClass flag")
	}
	if !strings.Contains(tasks, "refusing to change or remove another component's default marker") {
		t.Fatal("a conflicting default StorageClass must fail with an explicit conflict message")
	}

	// Rendering the task command with the real context: the flag reaches the shell,
	// and the shipped device list carries only the declared devices.
	off := r05MustValidate(t, "")
	tasksYAML := r05RenderTemplate(t, taskPath, off, loopCtx)
	if !strings.Contains(tasksYAML, "if [ 'false' != 'true' ]") {
		t.Fatal("with the flag off the rendered task must short-circuit before any patch")
	}
	onDefault := r05MustValidate(t, `storage:
  enabled: true
  provider: ceph
  makeDefaultStorageClass: true
  nodes:
    - {name: node1, devices: [/dev/disk/by-id/ata-ani-data-01]}
    - {name: node2, devices: [/dev/disk/by-id/ata-ani-data-02]}
    - {name: node3, devices: [/dev/disk/by-id/ata-ani-data-03]}
`)
	if rendered := r05RenderTemplate(t, taskPath, onDefault, loopCtx); !strings.Contains(rendered, "if [ 'true' != 'true' ]") {
		t.Fatal("with the flag on the rendered task must continue to the conflict check")
	}
	deviceList := r05RenderTemplate(t, filepath.Join("..", "..", "builtin", "core", "roles", "ani", "ceph", "templates", "storage-devices.txt"), onDefault)
	for _, want := range []string{"node1 /dev/disk/by-id/ata-ani-data-01", "node2 /dev/disk/by-id/ata-ani-data-02", "node3 /dev/disk/by-id/ata-ani-data-03"} {
		if !strings.Contains(deviceList, want) {
			t.Fatalf("rendered device list is missing %q:\n%s", want, deviceList)
		}
	}
	// The pre-flight task must run for its own node and read the shipped list.
	if !strings.Contains(tasksYAML, "ceph-preflight.sh node1") {
		t.Fatal("the pre-flight task must be invoked with its own node name")
	}
	if !strings.Contains(tasksYAML, "ANI_CEPH_DEVICE_LIST") && !strings.Contains(tasksYAML, "storage-devices.txt") {
		t.Fatal("the pre-flight task must be able to find the declared device list")
	}
}

// TestExampleSiteConfigsMatchTheSchema keeps the shipped example site configs
// parseable and valid: R05 made storage explicit, so an example that still
// relies on `profile: full` implying Ceph would fail here instead of on a node.
func TestExampleSiteConfigsMatchTheSchema(t *testing.T) {
	examples, err := filepath.Glob(filepath.Join("..", "..", "..", "config", "examples", "*.yaml"))
	if err != nil {
		t.Fatalf("glob examples: %v", err)
	}
	if len(examples) == 0 {
		t.Fatal("no example site configs were found")
	}
	for _, path := range examples {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		c, err := ParseClusterConfig(raw)
		if err != nil {
			t.Fatalf("%s: parse: %v", path, err)
		}
		if err := Validate(c); err != nil {
			t.Fatalf("%s: validate: %v", filepath.Base(path), err)
		}
		if !c.Storage.Enabled || c.Storage.provider() != "ceph" {
			t.Fatalf("%s must declare its storage explicitly (R05)", filepath.Base(path))
		}
	}
}

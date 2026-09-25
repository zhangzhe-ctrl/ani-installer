package ani

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/cockroachdb/errors"
	kkTmpl "github.com/kubesphere/kubekey/v4/pkg/converter/tmpl"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/resource"
)

// ClusterConfig is the small site-specific input used by "kk ani install".
// It intentionally does not describe components or versions; those are fixed
// by the offline artifact.
// DefaultStorageClass is the RBD StorageClass created by the existing base
// install (Rook/Ceph). Foundation components use it unless overridden.
const DefaultStorageClass = "ani-block"

var (
	// componentsOrder is the fixed order used by every component artifact that
	// must list all components exactly once, including components-selection.tsv.
	// The first four are the foundation batch; the last four are the
	// observability batch, whose last three rows are derived from the typed
	// logging backend rather than written independently.
	componentsOrder = [8]string{
		"cert-manager", "postgresql", "valkey", "nats",
		"metrics", "loki", "opensearch", "fluent-bit",
	}
)

// CertManagerComponent configures the internal PKI component. It owns no PVC.
type CertManagerComponent struct {
	Enabled bool `yaml:"enabled"`
}

// StorageComponent configures a single-instance component backed by one RWO PVC.
type StorageComponent struct {
	Enabled      bool   `yaml:"enabled"`
	StorageClass string `yaml:"storageClass"`
	StorageSize  string `yaml:"storageSize"`
}

// MetricsComponent is the single switch for the whole metrics/alerts stack
// (Prometheus, Alertmanager, Prometheus Operator, kube-state-metrics,
// node-exporter). It has no per-workload switches by design.
type MetricsComponent struct {
	Enabled                 bool   `yaml:"enabled"`
	StorageClass            string `yaml:"storageClass"`
	PrometheusStorageSize   string `yaml:"prometheusStorageSize"`
	AlertmanagerStorageSize string `yaml:"alertmanagerStorageSize"`
	PrometheusRetention     string `yaml:"prometheusRetention"`
	// PrometheusRetentionSize caps what Prometheus keeps on disk. It must stay
	// below PrometheusStorageSize, otherwise the volume fills before the time
	// based retention ever applies. It is a Kubernetes quantity, so it uses the
	// Ki/Mi/Gi suffix family (Prometheus also accepts 4GB, but that form is not
	// a quantity and would not be comparable to the volume size here).
	PrometheusRetentionSize string `yaml:"prometheusRetentionSize"`
}

// LoggingComponent selects at most one log backend. An empty backend means
// logging is off and Fluent Bit is not deployed. The backend is a one-time
// choice at first install: switching is not a supported runtime operation.
type LoggingComponent struct {
	Backend       string `yaml:"backend"` // "" | "none" | "loki" | "opensearch"
	StorageClass  string `yaml:"storageClass"`
	StorageSize   string `yaml:"storageSize"`
	RetentionDays string `yaml:"retentionDays"`
}

// logging backends. "none" and "" are equivalent.
const (
	loggingNone       = "none"
	loggingLoki       = "loki"
	loggingOpenSearch = "opensearch"
)

// MetricsNamespace holds the whole observability batch: the metrics stack and
// whichever log backend is selected. It is a constant rather than a site field
// because the roles, the Prometheus rule selector and the Alertmanager config
// selector all have to agree on it.
const MetricsNamespace = "ani-observability"

// metricsRunID is the value the metrics role puts on its AlertmanagerConfig.
// Alertmanager only admits configs carrying it, so a stale config from an
// earlier installation cannot route a later run's alerts. Deriving it from the
// cluster name keeps it stable across the re-renders one install performs,
// while still differing between installations. It is empty while the stack is
// off, so a disabled stack never matches a route.
func metricsRunID(c Components, clusterName string) string {
	if !c.Metrics.Enabled {
		return ""
	}
	return "ani-" + clusterName
}

// Components selects which components this run installs. Every switch is
// independent and defaults to off.
type Components struct {
	CertManager CertManagerComponent `yaml:"certManager"`
	PostgreSQL  StorageComponent     `yaml:"postgresql"`
	Valkey      StorageComponent     `yaml:"valkey"`
	NATS        StorageComponent     `yaml:"nats"`
	Metrics     MetricsComponent     `yaml:"metrics"`
	Logging     LoggingComponent     `yaml:"logging"`
}

// Storage providers accepted by storage.provider.
const (
	storageProviderCeph     = "ceph"
	storageProviderExternal = "external"
)

// StorageNode declares the devices one node contributes to the new Ceph cluster.
// The list is site input on purpose: R05 (A02) removed the deviceFilter scan
// that used to authorise whichever disk happened to be /dev/sdb.
type StorageNode struct {
	Name    string   `yaml:"name"`
	Devices []string `yaml:"devices"`
}

// Storage decides where block storage comes from. It is never implicit: a
// `profile: full` install used to run Rook/Ceph, authorise a scanned disk and
// rewrite the cluster's default StorageClass without the site saying so.
type Storage struct {
	Enabled bool `yaml:"enabled"`
	// Provider is required when Enabled is true:
	//   "ceph"     — this run installs Rook/Ceph and its RBD/CephFS classes
	//   "external" — storage already exists; only an existing class is used
	Provider string `yaml:"provider"`
	// Nodes lists every cluster node with the data devices it contributes. All
	// three nodes must appear: a node without a declaration cannot be given a
	// disk by means of useAllNodes.
	Nodes []StorageNode `yaml:"nodes"`
	// MakeDefaultStorageClass marks the RBD class as the cluster default. When
	// false (the default) no default annotation is touched at all; when true and
	// another class is already the default, the install fails instead of
	// clearing somebody else's marker.
	MakeDefaultStorageClass bool `yaml:"makeDefaultStorageClass"`
	// ExternalClass is required when Provider is external.
	ExternalClass string `yaml:"externalClass"`
}

// provider returns the normalised provider name.
func (s Storage) provider() string {
	return strings.TrimSpace(s.Provider)
}

// DefaultComponents returns the documented defaults for a site that does not
// spell them out. Every switch stays disabled; storage defaults match the plan.
func DefaultComponents() Components {
	return Components{
		PostgreSQL: StorageComponent{StorageClass: DefaultStorageClass, StorageSize: "10Gi"},
		Valkey:     StorageComponent{StorageClass: DefaultStorageClass, StorageSize: "2Gi"},
		NATS:       StorageComponent{StorageClass: DefaultStorageClass, StorageSize: "5Gi"},
		Metrics: MetricsComponent{
			StorageClass:            DefaultStorageClass,
			PrometheusStorageSize:   "5Gi",
			AlertmanagerStorageSize: "1Gi",
			PrometheusRetention:     "24h",
			PrometheusRetentionSize: "4GiB",
		},
		Logging: LoggingComponent{
			Backend:       loggingNone,
			StorageClass:  DefaultStorageClass,
			StorageSize:   "5Gi",
			RetentionDays: "3",
		},
	}
}

// LogBackend normalises the configured backend; an omitted or empty value is
// the same as an explicit "none".
func (l LoggingComponent) LogBackend() string {
	if strings.TrimSpace(l.Backend) == "" {
		return loggingNone
	}
	return strings.TrimSpace(l.Backend)
}

// loggingEnabled reports whether a log backend was selected. Fluent Bit is
// deployed exactly when this is true, so there is no separate collector switch
// that could produce collection without a backend.
func (c Components) loggingEnabled() bool {
	return c.Logging.LogBackend() != loggingNone
}

// ComponentRow is one row of components-selection.tsv.
type ComponentRow struct {
	Name    string
	Enabled bool
}

// Selection returns every supported component in the fixed order with its
// effective on/off result, so downstream consumers never have to re-parse the
// nested site YAML. The loki/opensearch rows are mutually exclusive and the
// fluent-bit row follows the backend, so no combination can ask for two log
// backends or for collection without a backend.
func (c Components) Selection() []ComponentRow {
	enabled := map[string]bool{
		"cert-manager": c.CertManager.Enabled,
		"postgresql":   c.PostgreSQL.Enabled,
		"valkey":       c.Valkey.Enabled,
		"nats":         c.NATS.Enabled,
		"metrics":      c.Metrics.Enabled,
		"loki":         c.Logging.LogBackend() == loggingLoki,
		"opensearch":   c.Logging.LogBackend() == loggingOpenSearch,
		"fluent-bit":   c.loggingEnabled(),
	}
	rows := make([]ComponentRow, 0, len(componentsOrder))
	for _, name := range componentsOrder {
		rows = append(rows, ComponentRow{Name: name, Enabled: enabled[name]})
	}
	return rows
}

// storage returns the effective storage settings for the named storage-backed
// component, or nil when the name is unknown.
//
// loki and opensearch are included because each backend keeps its data on one
// PVC: they are storage-backed components like the foundation ones, so an
// enabled backend without a storage class must fail here rather than at
// install time. Both read the same logging storage settings, because only one
// backend can ever be selected. fluent-bit is not listed: a collector keeps
// only a bounded buffer on a hostPath, which is node-local scratch rather than
// a component volume.
func (c Components) storage(name string) *StorageComponent {
	switch name {
	case "postgresql":
		return &c.PostgreSQL
	case "valkey":
		return &c.Valkey
	case "nats":
		return &c.NATS
	case "loki":
		return &StorageComponent{
			Enabled:      c.Logging.LogBackend() == loggingLoki,
			StorageClass: c.Logging.StorageClass,
			StorageSize:  c.Logging.StorageSize,
		}
	case "opensearch":
		return &StorageComponent{
			Enabled:      c.Logging.LogBackend() == loggingOpenSearch,
			StorageClass: c.Logging.StorageClass,
			StorageSize:  c.Logging.StorageSize,
		}
	default:
		return nil
	}
}

// ImplementedComponents is the set of components this release can actually
// deploy. It grows one batch at a time; a site enabling anything else must fail
// before any deployment begins instead of silently skipping it.
//
// A row is only listed once its role and verification script are packaged, so
// "the switch is on" and "something will actually be installed" never diverge.
// The observability batch is complete here: metrics, the two mutually
// exclusive log backends and the collector all ship their own roles.
var ImplementedComponents = []string{
	"cert-manager", "postgresql", "valkey", "nats", "metrics", "loki", "opensearch", "fluent-bit",
}

// storageSizeOrErr parses a capacity and rejects values that are zero or
// negative, which resource.ParseQuantity alone accepts.
func parsePositiveQuantity(field, value string) error {
	q, err := resource.ParseQuantity(value)
	if err != nil {
		return fmt.Errorf("%s %q is not a valid capacity: %w", field, value, err)
	}
	if q.IsZero() || q.Sign() <= 0 {
		return fmt.Errorf("%s %q must be greater than zero", field, value)
	}
	return nil
}

// validRetention accepts a positive integer followed by one of the allowed
// units. The metrics stack accepts h/d.
func validRetention(value string, units []string) bool {
	v := strings.TrimSpace(value)
	if v == "" {
		return false
	}
	for _, unit := range units {
		if !strings.HasSuffix(v, unit) {
			continue
		}
		number := strings.TrimSuffix(v, unit)
		n, err := strconv.Atoi(number)
		return err == nil && n > 0
	}
	return false
}

// validPositiveDays accepts a plain positive day count, which is how the
// logging backend's retentionDays is documented ("retentionDays: 3"). It is
// deliberately not a duration: a unit suffix is a typo here.
func validPositiveDays(value string) bool {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	return err == nil && n > 0
}

// validateMetrics checks the metrics stack's own fields, but only when the
// stack is enabled: disabled features must not fail on unrelated defaults.
func (c Components) validateMetrics() error {
	m := c.Metrics
	if !m.Enabled {
		return nil
	}
	if strings.TrimSpace(m.StorageClass) == "" {
		return fmt.Errorf("components.metrics.storageClass is required when metrics is enabled")
	}
	for field, value := range map[string]string{
		"components.metrics.prometheusStorageSize":   m.PrometheusStorageSize,
		"components.metrics.alertmanagerStorageSize": m.AlertmanagerStorageSize,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required when metrics is enabled", field)
		}
		if err := parsePositiveQuantity(field, value); err != nil {
			return err
		}
	}
	// Prometheus retention is a plain duration; alertmanager does not take one.
	if !validRetention(m.PrometheusRetention, []string{"h", "d"}) {
		return fmt.Errorf("components.metrics.prometheusRetention %q must be a positive integer followed by h or d (for example 24h or 7d)", m.PrometheusRetention)
	}
	// The on-disk cap must exist and must be smaller than the volume, or the
	// volume fills up first and the cap is meaningless. The Prometheus CRD
	// mandates the trailing-B byte form (see PrometheusRetentionSize), so the
	// check requires that spelling up front and strips the B before the
	// quantity comparison — resource.ParseQuantity rejects GiB, while the CRD
	// rejects Gi. (Failure A2 on 2026-09-19: the installer accepted "4Gi" and
	// the CRD webhook then bounced the whole metrics install.)
	if !strings.HasSuffix(m.PrometheusRetentionSize, "B") {
		return fmt.Errorf("components.metrics.prometheusRetentionSize %q must be a byte capacity with a trailing B (for example 4GiB), matching the pattern the Prometheus CRD enforces", m.PrometheusRetentionSize)
	}
	retentionSize, err := resource.ParseQuantity(strings.TrimSuffix(m.PrometheusRetentionSize, "B"))
	if err != nil || retentionSize.Sign() <= 0 {
		return fmt.Errorf("components.metrics.prometheusRetentionSize %q must be a positive capacity (for example 4GiB)", m.PrometheusRetentionSize)
	}
	storageSize, err := resource.ParseQuantity(m.PrometheusStorageSize)
	if err != nil {
		return fmt.Errorf("components.metrics.prometheusStorageSize %q is not a valid capacity: %w", m.PrometheusStorageSize, err)
	}
	if retentionSize.Cmp(storageSize) >= 0 {
		return fmt.Errorf("components.metrics.prometheusRetentionSize %q must be smaller than prometheusStorageSize %q, otherwise Prometheus fills its volume before the retention limit applies", m.PrometheusRetentionSize, m.PrometheusStorageSize)
	}
	return nil
}

// validateLogging checks the selected backend and its storage. It never enables
// security features implicitly: an opensearch backend without cert-manager is
// an explicit error rather than a silent fallback to demo TLS.
func (c Components) validateLogging() error {
	l := c.Logging
	backend := l.LogBackend()
	switch backend {
	case loggingNone:
		return nil
	case loggingLoki, loggingOpenSearch:
	default:
		return fmt.Errorf("components.logging.backend %q is not supported; use one of none, loki, opensearch", l.Backend)
	}
	if strings.TrimSpace(l.StorageClass) == "" {
		return fmt.Errorf("components.logging.storageClass is required when logging.backend=%s", backend)
	}
	if strings.TrimSpace(l.StorageSize) == "" {
		return fmt.Errorf("components.logging.storageSize is required when logging.backend=%s", backend)
	}
	if err := parsePositiveQuantity("components.logging.storageSize", l.StorageSize); err != nil {
		return err
	}
	// Log retention is a plain day count, not a duration: "3" is valid and
	// "3d" is a typo that must be rejected rather than silently ignored.
	if !validPositiveDays(l.RetentionDays) {
		return fmt.Errorf("components.logging.retentionDays %q must be a positive integer number of days (for example 3)", l.RetentionDays)
	}
	if backend == loggingOpenSearch && !c.CertManager.Enabled {
		return fmt.Errorf("components.logging.backend=opensearch requires components.certManager.enabled=true: OpenSearch uses cert-manager's internal CA for non-demo TLS, and this build never falls back to demo certificates or disables security")
	}
	return nil
}

// validateComponents checks supported names, effective capacity, the
// "not implemented yet" gate and the batch-2 field rules.
func (c Components) validate() error {
	for _, row := range c.Selection() {
		if !row.Enabled {
			continue
		}
		if !containsString(ImplementedComponents, row.Name) {
			return fmt.Errorf("components.%s: enabled=true but this release does not implement %s yet; set it to false", row.Name, row.Name)
		}
		storage := c.storage(row.Name)
		if storage == nil {
			continue
		}
		if strings.TrimSpace(storage.StorageClass) == "" {
			return fmt.Errorf("components.%s.storageClass is required when the component is enabled", row.Name)
		}
		if strings.TrimSpace(storage.StorageSize) == "" {
			return fmt.Errorf("components.%s.storageSize is required when the component is enabled", row.Name)
		}
		if err := parsePositiveQuantity("components."+row.Name+".storageSize", storage.StorageSize); err != nil {
			return err
		}
	}
	if err := c.validateMetrics(); err != nil {
		return err
	}
	return c.validateLogging()
}

func containsString(values []string, needle string) bool {
	for _, v := range values {
		if v == needle {
			return true
		}
	}
	return false
}

type ClusterConfig struct {
	Name string `yaml:"name"`
	// Profile bounds how far the install chain runs:
	//   "" / "full" — the whole chain (storage, ceph, foundation and
	//                 observability components after the network stack)
	//   "base"      — stop after the base cluster: kubernetes + CNI network
	//                 stack (kcn batch incl. envoy+smoke, or kubeovn)
	Profile        string       `yaml:"profile"`
	InstallerNode  string       `yaml:"installerNode"`
	SSH            SSHConfig    `yaml:"ssh"`
	Nodes          []NodeConfig `yaml:"nodes"`
	Network        Network      `yaml:"network"`
	RegistryConfig Registry     `yaml:"registry"`
	Components     Components   `yaml:"components"`
	Storage        Storage      `yaml:"storage"`
}

// installProfile normalizes the configured profile for template use: an empty
// value (site configs written before the switch existed) means the full chain.
func installProfile(profile string) string {
	if profile == "" {
		return "full"
	}
	return profile
}

// componentImageKeysForRun filters the fixed split-reference key list down to
// the groups the install chain can actually render. The base profile ends the
// chain at the network stack: every component role below it is skipped by the
// playbook, so a base-mode artifact (such as the kubeovn one) intentionally
// ships none of the chart images, and requiring them here would fail the
// install before any deployment begins. In the full profile a group's keys
// are only required when its component switch is on, for the same reason: a
// disabled stack's images never render, so they must not gate the run.
// The lab group is kept unconditionally because its images ship with every
// artifact and the smoke and verification jobs read them in any profile.
// componentImageKeysForRun filters the declared key list down to the images the
// selected stack, profile and components actually require (R08/A08):
//   - the two main CNIs are either-or, chosen by network.stack;
//   - the log stack follows the selected backend — an unselected backend's
//     images are not required;
//   - foundation component images follow their own enabled switches;
//   - the cert-manager verification image follows cert-manager;
//   - the lab group ships with every artifact.
func componentImageKeysForRun(c ClusterConfig) []ImageKey {
	base := installProfile(c.Profile) == "base"
	stack := networkStack(c.Network.Stack)
	backend := c.Components.Logging.LogBackend()
	all := componentImageKeys()
	keys := make([]ImageKey, 0, len(all))
	for _, key := range all {
		switch key.Group {
		case "metrics":
			if base || !c.Components.Metrics.Enabled {
				continue
			}
		case "logs":
			if base {
				continue
			}
			switch key.Backend {
			case "loki":
				if backend != loggingLoki {
					continue
				}
			case "opensearch":
				if backend != loggingOpenSearch {
					continue
				}
			case "fluent-bit":
				if !c.Components.loggingEnabled() {
					continue
				}
			}
		case "kcn":
			if base || stack != "kcn" {
				continue
			}
		case "kubeovn":
			if base || stack != "kubeovn" {
				continue
			}
		case "components":
			switch key.Name {
			case "postgres":
				if base || !c.Components.PostgreSQL.Enabled {
					continue
				}
			case "valkey":
				if base || !c.Components.Valkey.Enabled {
					continue
				}
			case "nats", "natsConfigReloader", "natsBox":
				if base || !c.Components.NATS.Enabled {
					continue
				}
			}
		case "verification":
			if base || !c.Components.CertManager.Enabled {
				continue
			}
		}
		keys = append(keys, key)
	}
	return keys
}

// requiredChartPaths lists the artifact chart paths the enabled components need
// (R08). Enabling a chart-backed component without its chart material fails the
// artifact preflight instead of deadlocking mid-install.
func requiredChartPaths(c ClusterConfig) []string {
	base := installProfile(c.Profile) == "base"
	if base {
		return nil
	}
	var paths []string
	if c.Components.CertManager.Enabled {
		paths = append(paths, "charts/cert-manager/v1.21.2.tgz")
	}
	if c.Components.NATS.Enabled {
		paths = append(paths, "charts/nats/2.14.6.tgz")
	}
	if c.Components.Metrics.Enabled {
		paths = append(paths, "charts/kube-prometheus-stack/85.4.0.tgz")
	}
	if c.Components.loggingEnabled() {
		switch c.Components.Logging.LogBackend() {
		case loggingLoki:
			paths = append(paths, "charts/loki/18.13.3.tgz")
		case loggingOpenSearch:
			paths = append(paths, "charts/opensearch/3.8.0.tgz", "charts/fluent-bit/0.58.2.tgz")
		}
	}
	sort.Strings(paths)
	return paths
}

// requiresHelmTool reports whether any enabled component renders from a Chart,
// which makes the packaged helm binary a required material (R08).
func requiresHelmTool(c ClusterConfig) bool {
	return len(requiredChartPaths(c)) > 0
}

type SSHConfig struct {
	User       string `yaml:"user"`
	Port       int    `yaml:"port"`
	Password   string `yaml:"password"`
	PrivateKey string `yaml:"privateKey"`
}

type NodeConfig struct {
	Name    string `yaml:"name"`
	Address string `yaml:"address"`
}

type Network struct {
	// Stack selects the network stack, either-or:
	//   "kcn"    — the self-developed batch: kcn CNI + envoy-gateway gateway
	//              family + its smoke test (default when empty, for backward
	//              compatibility with existing site configs)
	//   "kubeovn" — Kube-OVN (v1.16.x); kcn, envoy and smoke are all skipped
	Stack               string `yaml:"stack"`
	ManagementInterface string `yaml:"managementInterface"`
	PodCIDR             string `yaml:"podCIDR"`
	ServiceCIDR         string `yaml:"serviceCIDR"`
	KCN                 KCN    `yaml:"kcn"`
	// KubeOVN carries the Kube-OVN network settings (R10/A05). The subsection
	// binds the kubeovn stack only; a kcn site may still carry it (ignored),
	// mirroring how the kcn subsection is ignored under kubeovn.
	KubeOVN KubeOVN `yaml:"kubeovn"`
}

// KubeOVN is the network.kubeovn subsection. The loadBalancer/multus fields of
// the blueprint §6.3 target schema are separate B01 work and deliberately not
// accepted here: strict decoding rejects them until that task lands.
type KubeOVN struct {
	// DefaultGateway is the pod network gateway. Empty derives the first
	// usable IPv4 address of the pod CIDR (network address + 1), so a
	// non-default pod CIDR no longer renders the historical 10.16.0.1.
	DefaultGateway string `yaml:"defaultGateway"`
	// JoinCIDR is the node (join) switch network. Empty keeps the historical
	// 172.19.0.0/16 the template shipped before the value became
	// configurable; it is a fallback, not a rule for every site.
	JoinCIDR string `yaml:"joinCIDR"`
}

type KCN struct {
	ManagedDevices   []string `yaml:"managedDevices"`
	EncapNetworks    []string `yaml:"encapNetworks"`
	IntranetNetworks []string `yaml:"intranetNetworks"`
}

// networkStack normalizes the configured stack for template use: an empty
// value (site configs written before the either-or switch existed) means the
// self-developed kcn batch, so legacy configs keep their behavior.
func networkStack(stack string) string {
	if stack == "" {
		return "kcn"
	}
	return stack
}

// defaultKubeOVNJoinCIDR keeps the join network the kubeovn template shipped
// before the value became configurable (R10/A05). It is a fallback, not a
// rule: a site whose address plan collides with it must set
// network.kubeovn.joinCIDR explicitly — validation still runs on the resolved
// value, so a colliding default is rejected instead of silently deployed.
const defaultKubeOVNJoinCIDR = "172.19.0.0/16"

// kubeovnNetwork holds the resolved Kube-OVN network values the template
// context renders (canonical spellings, defaults applied).
type kubeovnNetwork struct {
	DefaultGateway string
	JoinCIDR       string
}

// parseIPv4Prefix parses a site-config CIDR under the strict Kube-OVN network
// contract (R10/A05): the value must be IPv4, spelled as the canonical network
// address (no host bits, so "10.16.0.1/16" is rejected) and large enough to
// carry usable addresses (/31 and /32 cannot serve the pod, service or join
// networks this release deploys). The returned prefix is always masked.
//
// The address and prefix length are parsed separately instead of via
// netip.ParsePrefix so the rejection of non-canonical spellings is this
// contract's own, deterministic error rather than a Go-version-dependent one.
func parseIPv4Prefix(field, cidr string) (netip.Prefix, error) {
	addrStr, bitsStr, ok := strings.Cut(cidr, "/")
	if !ok {
		return netip.Prefix{}, fmt.Errorf("%s %q is invalid: a CIDR needs an address and a prefix length like 10.16.0.0/16", field, cidr)
	}
	addr, err := netip.ParseAddr(addrStr)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%s %q is invalid: %w", field, cidr, err)
	}
	bits, err := strconv.Atoi(bitsStr)
	if err != nil || bits < 0 || bits > addr.BitLen() {
		return netip.Prefix{}, fmt.Errorf("%s %q is invalid: bad prefix length %q", field, cidr, bitsStr)
	}
	if !addr.Is4() {
		return netip.Prefix{}, fmt.Errorf("%s %q is not an IPv4 network; this release supports IPv4 only", field, cidr)
	}
	prefix := netip.PrefixFrom(addr, bits)
	masked := prefix.Masked()
	if prefix != masked {
		return netip.Prefix{}, fmt.Errorf("%s %q is not the canonical network address; write %s", field, cidr, masked)
	}
	if bits > 30 {
		return netip.Prefix{}, fmt.Errorf("%s %q is too small; /31 and /32 cannot serve the network", field, cidr)
	}
	return masked, nil
}

// resolveKubeOVNNetwork resolves the effective Kube-OVN network values and, on
// the kubeovn stack, enforces the R10/A05 contract (blueprint §6.3):
//   - pod, service and join networks are canonical IPv4 prefixes, at most /30,
//     pairwise disjoint;
//   - no node management address falls inside pod, service or join;
//   - the pod gateway is either the configured one (an IPv4 address inside
//     the pod network that is not its network address) or, when empty, the
//     first usable address of the pod CIDR.
//
// On the kcn stack the Kube-OVN subsection is ignored (mirroring how the kcn
// subsection is ignored under kubeovn), but the values are still derived so
// the template context never renders <no value> when a site switches stacks.
func resolveKubeOVNNetwork(c ClusterConfig) (kubeovnNetwork, error) {
	joinRaw := strings.TrimSpace(c.Network.KubeOVN.JoinCIDR)
	if joinRaw == "" {
		joinRaw = defaultKubeOVNJoinCIDR
	}
	if networkStack(c.Network.Stack) != "kubeovn" {
		// kcn stack: derive without the kubeovn-only contract. Validate has
		// already checked both CIDRs with net.ParseCIDR; canonicalize through
		// it so the derivation below works on the network address.
		gateway := strings.TrimSpace(c.Network.KubeOVN.DefaultGateway)
		if gateway == "" {
			_, ipnet, err := net.ParseCIDR(c.Network.PodCIDR)
			if err != nil {
				return kubeovnNetwork{}, fmt.Errorf("network.podCIDR %q is invalid: %w", c.Network.PodCIDR, err)
			}
			pod, err := netip.ParsePrefix(ipnet.String())
			if err != nil {
				return kubeovnNetwork{}, fmt.Errorf("network.podCIDR %q is invalid: %w", c.Network.PodCIDR, err)
			}
			gateway = pod.Addr().Next().String()
		}
		return kubeovnNetwork{DefaultGateway: gateway, JoinCIDR: joinRaw}, nil
	}

	pod, err := parseIPv4Prefix("network.podCIDR", c.Network.PodCIDR)
	if err != nil {
		return kubeovnNetwork{}, err
	}
	svc, err := parseIPv4Prefix("network.serviceCIDR", c.Network.ServiceCIDR)
	if err != nil {
		return kubeovnNetwork{}, err
	}
	join, err := parseIPv4Prefix("network.kubeovn.joinCIDR", joinRaw)
	if err != nil {
		return kubeovnNetwork{}, err
	}
	if pod.Overlaps(svc) {
		return kubeovnNetwork{}, fmt.Errorf("network.podCIDR %s overlaps network.serviceCIDR %s; the pod and service networks must be disjoint", pod, svc)
	}
	if pod.Overlaps(join) {
		return kubeovnNetwork{}, fmt.Errorf("network.podCIDR %s overlaps network.kubeovn.joinCIDR %s; the pod and join networks must be disjoint", pod, join)
	}
	if svc.Overlaps(join) {
		return kubeovnNetwork{}, fmt.Errorf("network.serviceCIDR %s overlaps network.kubeovn.joinCIDR %s; the service and join networks must be disjoint", svc, join)
	}
	for _, n := range c.Nodes {
		addr, err := netip.ParseAddr(n.Address)
		if err != nil {
			return kubeovnNetwork{}, fmt.Errorf("node %q address %q is invalid: %w", n.Name, n.Address, err)
		}
		for _, clash := range []struct {
			network netip.Prefix
			field   string
		}{{pod, "network.podCIDR"}, {svc, "network.serviceCIDR"}, {join, "network.kubeovn.joinCIDR"}} {
			if clash.network.Contains(addr) {
				return kubeovnNetwork{}, fmt.Errorf("node %q management address %s falls inside %s %s; node addresses must stay outside the pod, service and join networks", n.Name, addr, clash.field, clash.network)
			}
		}
	}
	gateway := strings.TrimSpace(c.Network.KubeOVN.DefaultGateway)
	if gateway == "" {
		// First usable address of the pod network: network address + 1. The
		// prefix size check above keeps Next() from wrapping.
		return kubeovnNetwork{DefaultGateway: pod.Addr().Next().String(), JoinCIDR: join.String()}, nil
	}
	gw, err := netip.ParseAddr(gateway)
	if err != nil {
		return kubeovnNetwork{}, fmt.Errorf("network.kubeovn.defaultGateway %q is invalid: %w", gateway, err)
	}
	if !gw.Is4() {
		return kubeovnNetwork{}, fmt.Errorf("network.kubeovn.defaultGateway %q is not an IPv4 address", gateway)
	}
	if !pod.Contains(gw) {
		return kubeovnNetwork{}, fmt.Errorf("network.kubeovn.defaultGateway %s is outside the pod network %s; the gateway must belong to the pod CIDR", gw, pod)
	}
	if gw == pod.Addr() {
		return kubeovnNetwork{}, fmt.Errorf("network.kubeovn.defaultGateway %s is the network address of %s and cannot serve as the gateway", gw, pod)
	}
	return kubeovnNetwork{DefaultGateway: gw.String(), JoinCIDR: join.String()}, nil
}

type Registry struct {
	Port int `yaml:"port"`
}

func (c ClusterConfig) Installer() (NodeConfig, error) {
	for _, n := range c.Nodes {
		if n.Name == c.InstallerNode {
			return n, nil
		}
	}
	return NodeConfig{}, fmt.Errorf("installerNode %q is not one of nodes", c.InstallerNode)
}

func (c ClusterConfig) RegistryAddress() (string, error) {
	n, err := c.Installer()
	if err != nil {
		return "", err
	}
	if c.RegistryConfig.Port <= 0 || c.RegistryConfig.Port > 65535 {
		return "", fmt.Errorf("registry.port must be between 1 and 65535")
	}
	return net.JoinHostPort(n.Address, strconv.Itoa(c.RegistryConfig.Port)), nil
}

// validateStorage enforces the explicit storage contract from R05 (A02).
//
// nodes is the set of topology node names already validated by Validate, so a
// storage declaration can be checked against the cluster it will run on.
func validateStorage(c ClusterConfig, nodes map[string]struct{}) error {
	s := c.Storage
	if !s.Enabled {
		switch {
		case s.provider() != "":
			return fmt.Errorf("storage.provider is set (%q) but storage.enabled is false; enable storage or remove the provider", s.Provider)
		case len(s.Nodes) > 0:
			return fmt.Errorf("storage.nodes is set but storage.enabled is false; enable storage or remove the node declarations")
		case strings.TrimSpace(s.ExternalClass) != "":
			return fmt.Errorf("storage.externalClass is set but storage.enabled is false; enable storage or remove the class")
		case s.MakeDefaultStorageClass:
			return fmt.Errorf("storage.makeDefaultStorageClass is true but storage.enabled is false; enable storage or drop the flag")
		}
		// With no storage selected the built-in class does not exist, so no
		// enabled component may quietly rely on it.
		for _, row := range c.Components.Selection() {
			if !row.Enabled {
				continue
			}
			component := c.Components.storage(row.Name)
			if component != nil && strings.TrimSpace(component.StorageClass) == DefaultStorageClass {
				return fmt.Errorf("components.%s uses the built-in StorageClass %q, which only exists when storage.enabled is true with provider=%q; enable storage or point the component at an existing class", row.Name, DefaultStorageClass, storageProviderCeph)
			}
		}
		return nil
	}

	switch s.provider() {
	case "":
		return fmt.Errorf("storage.enabled is true but storage.provider is empty; set provider to %q or %q", storageProviderCeph, storageProviderExternal)
	case storageProviderCeph:
		if len(s.Nodes) == 0 {
			return fmt.Errorf("storage.provider=%q requires storage.nodes with the data devices of every node", storageProviderCeph)
		}
		declared := map[string]struct{}{}
		var missing []string
		for _, node := range s.Nodes {
			name := strings.TrimSpace(node.Name)
			if name == "" {
				return fmt.Errorf("storage.nodes entries require a node name")
			}
			if _, ok := declared[name]; ok {
				return fmt.Errorf("storage.nodes declares node %q twice", name)
			}
			declared[name] = struct{}{}
			if len(node.Devices) == 0 {
				return fmt.Errorf("storage.nodes[%s] declares no devices; every node needs its own data device", name)
			}
			// Device paths live in each node's own namespace: /dev/sdb on
			// node1 and /dev/sdb on node2 are two different physical disks.
			// The blueprint explicitly allows a site where every node's data
			// disk is /dev/sdb (§6.2), so dedup applies WITHIN one node only
			// (the same node cannot list the same device twice). A global
			// path-string dedup here wrongly rejected that documented VM-farm
			// shape and blocked the first install (R15.3 field finding).
			nodeDevices := map[string]struct{}{}
			for _, device := range node.Devices {
				device = strings.TrimSpace(device)
				if !strings.HasPrefix(device, "/dev/") {
					return fmt.Errorf("storage.nodes[%s] device %q must be an absolute /dev path", name, device)
				}
				if _, ok := nodeDevices[device]; ok {
					return fmt.Errorf("storage.nodes[%s] declares device %q twice; one device cannot serve the same node twice", name, device)
				}
				nodeDevices[device] = struct{}{}
			}
		}
		for _, node := range c.Nodes {
			if _, ok := declared[node.Name]; !ok {
				missing = append(missing, node.Name)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("storage.nodes is missing cluster node(s) %s: every cluster node needs its own declared data device", strings.Join(missing, ", "))
		}
		for name := range declared {
			if _, ok := nodes[name]; !ok {
				return fmt.Errorf("storage.nodes declares %q, which is not one of the cluster nodes", name)
			}
		}
		if strings.TrimSpace(s.ExternalClass) != "" {
			return fmt.Errorf("storage.externalClass is only valid with provider=%q", storageProviderExternal)
		}
	case storageProviderExternal:
		if strings.TrimSpace(s.ExternalClass) == "" {
			return fmt.Errorf("storage.provider=%q requires storage.externalClass", storageProviderExternal)
		}
		if len(s.Nodes) > 0 {
			return fmt.Errorf("storage.provider=%q must not declare storage.nodes: this run installs no Ceph", storageProviderExternal)
		}
		if s.MakeDefaultStorageClass {
			return fmt.Errorf("storage.makeDefaultStorageClass is not allowed with provider=%q: that class is not ours to mark", storageProviderExternal)
		}
	default:
		return fmt.Errorf("storage.provider must be %q or %q, got %q", storageProviderCeph, storageProviderExternal, s.Provider)
	}
	return nil
}

// storageNodesForTemplate renders the declared storage nodes for the playbook
// and role templates, one entry per node with its device list.
func storageNodesForTemplate(s Storage) []map[string]any {
	out := make([]map[string]any, 0, len(s.Nodes))
	for _, node := range s.Nodes {
		devices := make([]string, 0, len(node.Devices))
		for _, device := range node.Devices {
			devices = append(devices, strings.TrimSpace(device))
		}
		out = append(out, map[string]any{"name": node.Name, "devices": devices})
	}
	return out
}

func Validate(c ClusterConfig) error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.ContainsAny(c.Name, " \t/\\") || c.Name == "." || c.Name == ".." {
		return fmt.Errorf("name must be a safe path component")
	}
	if len(c.Nodes) != 3 {
		return fmt.Errorf("exactly 3 nodes are required, got %d", len(c.Nodes))
	}
	if _, err := c.Installer(); err != nil {
		return err
	}
	names := map[string]struct{}{}
	addresses := map[string]struct{}{}
	for _, n := range c.Nodes {
		if strings.TrimSpace(n.Name) == "" {
			return fmt.Errorf("each node requires a name")
		}
		ip := net.ParseIP(n.Address)
		if ip == nil || ip.To4() == nil {
			return fmt.Errorf("node %q address %q is not an IPv4 address", n.Name, n.Address)
		}
		if _, exists := names[n.Name]; exists {
			return fmt.Errorf("duplicate node name %q", n.Name)
		}
		if _, exists := addresses[n.Address]; exists {
			return fmt.Errorf("duplicate node address %q", n.Address)
		}
		names[n.Name] = struct{}{}
		addresses[n.Address] = struct{}{}
	}
	if err := validateStorage(c, names); err != nil {
		return err
	}
	if strings.TrimSpace(c.SSH.User) == "" {
		return fmt.Errorf("ssh.user is required")
	}
	// R06: the installer node is nodes[0] by definition of the run manifest and
	// the verify facts; a config that claims otherwise is ambiguous about where
	// the run happens and is rejected before anything executes.
	if len(c.Nodes) > 0 && c.InstallerNode != c.Nodes[0].Name {
		return fmt.Errorf("installerNode must be the first node %q, got %q", c.Nodes[0].Name, c.InstallerNode)
	}
	if c.SSH.Port <= 0 || c.SSH.Port > 65535 {
		return fmt.Errorf("ssh.port must be between 1 and 65535")
	}
	if strings.TrimSpace(c.SSH.Password) == "" && strings.TrimSpace(c.SSH.PrivateKey) == "" {
		return fmt.Errorf("ssh.password or ssh.privateKey is required")
	}
	if strings.TrimSpace(c.SSH.PrivateKey) != "" && !strings.HasPrefix(c.SSH.PrivateKey, "/") {
		return fmt.Errorf("ssh.privateKey must be an absolute path on the installer node")
	}
	if strings.TrimSpace(c.Network.ManagementInterface) == "" {
		return fmt.Errorf("network.managementInterface is required")
	}
	if _, _, err := net.ParseCIDR(c.Network.PodCIDR); err != nil {
		return fmt.Errorf("network.podCIDR %q is invalid: %w", c.Network.PodCIDR, err)
	}
	if _, _, err := net.ParseCIDR(c.Network.ServiceCIDR); err != nil {
		return fmt.Errorf("network.serviceCIDR %q is invalid: %w", c.Network.ServiceCIDR, err)
	}
	stack := c.Network.Stack
	if stack == "" {
		stack = "kcn"
	}
	if stack != "kcn" && stack != "kubeovn" {
		return fmt.Errorf("network.stack must be \"kcn\" or \"kubeovn\", got %q", c.Network.Stack)
	}
	switch c.Profile {
	case "", "full", "base":
	default:
		return fmt.Errorf("profile must be \"full\" or \"base\", got %q", c.Profile)
	}
	// R06: profile: base ends the chain before storage and every component, so a
	// config that still enables them is rejected here instead of being silently
	// skipped by the playbook.
	if installProfile(c.Profile) == "base" {
		for _, row := range c.Components.Selection() {
			if row.Enabled {
				return fmt.Errorf("profile=base stops before components, but components.%s is enabled; use profile=full or disable it", row.Name)
			}
		}
		if c.Storage.Enabled {
			return fmt.Errorf("profile=base stops before storage, but storage.enabled is true; use profile=full or disable it")
		}
	}
	// The kcn subsection is only required for the kcn stack. When kubeovn is
	// selected the site config may still carry the section (it is ignored),
	// so an existing site file needs no edits to switch stacks.
	if stack == "kcn" {
		if len(c.Network.KCN.ManagedDevices) == 0 {
			return fmt.Errorf("network.kcn.managedDevices is required")
		}
		if len(c.Network.KCN.EncapNetworks) == 0 {
			return fmt.Errorf("network.kcn.encapNetworks is required")
		}
		if len(c.Network.KCN.IntranetNetworks) == 0 {
			return fmt.Errorf("network.kcn.intranetNetworks is required")
		}
		for _, cidr := range append(c.Network.KCN.EncapNetworks, c.Network.KCN.IntranetNetworks...) {
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				return fmt.Errorf("kcn network %q is invalid: %w", cidr, err)
			}
		}
	}
	if _, err := c.RegistryAddress(); err != nil {
		return err
	}

	// Encap-network routing consistency is a kcn-stack concern only.
	if stack == "kcn" {
		intranetNetworks := make(map[string]struct{}, len(c.Network.KCN.IntranetNetworks))
		for _, network := range c.Network.KCN.IntranetNetworks {
			if _, ipnet, err := net.ParseCIDR(network); err == nil {
				intranetNetworks[ipnet.String()] = struct{}{}
			}
		}
		for _, network := range c.Network.KCN.EncapNetworks {
			_, ipnet, err := net.ParseCIDR(network)
			if err != nil {
				return fmt.Errorf("kcn network %q is invalid: %w", network, err)
			}
			if _, ok := intranetNetworks[ipnet.String()]; !ok {
				return fmt.Errorf("network.kcn.intranetNetworks must include each encap network so KCN can route host traffic: %s", ipnet.String())
			}
		}
	}
	// R10/A05: the Kube-OVN network contract (canonical IPv4 CIDRs, disjoint
	// pod/service/join networks, gateway inside the pod network, node
	// addresses outside all three) binds the kubeovn stack only.
	if networkStack(stack) == "kubeovn" {
		if _, err := resolveKubeOVNNetwork(c); err != nil {
			return err
		}
	}
	return c.Components.validate()
}

// LoadClusterConfig reads the site configuration strictly: unknown keys fail so
// a typo in components.* can never be silently ignored. Defaults are applied
// first so omitted switches stay off and omitted storage settings keep their
// documented values. Credentials inside the file are never logged.
func LoadClusterConfig(path string) (ClusterConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ClusterConfig{}, fmt.Errorf("read cluster config %s: %w", path, err)
	}
	return ParseClusterConfig(data)
}

// ParseClusterConfig applies strict decoding to an already-read site config.
func ParseClusterConfig(data []byte) (ClusterConfig, error) {
	cluster := ClusterConfig{Components: DefaultComponents()}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cluster); err != nil {
		if err == io.EOF {
			return ClusterConfig{}, fmt.Errorf("cluster config is empty")
		}
		return ClusterConfig{}, fmt.Errorf("parse cluster config: %w", err)
	}
	// Exactly one document: a second YAML document after the site config would
	// otherwise be silently ignored, so the file the operator edits would not be
	// the file that gets interpreted (R06/A09).
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ClusterConfig{}, fmt.Errorf("cluster config must contain exactly one YAML document")
	}
	return cluster, nil
}

// VerifyInstallerInterface confirms that the installer management IP in the
// site config is actually assigned to the configured local interface.
func VerifyInstallerInterface(c ClusterConfig) error {
	n, err := c.Installer()
	if err != nil {
		return err
	}
	iface, err := net.InterfaceByName(c.Network.ManagementInterface)
	if err != nil {
		return fmt.Errorf("management interface %q not found on installer: %w", c.Network.ManagementInterface, err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return err
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.Equal(net.ParseIP(n.Address)) {
			return nil
		}
	}
	return fmt.Errorf("installer address %q is not assigned to interface %q", n.Address, c.Network.ManagementInterface)
}

// VerifySSHAuth is only called on the installer. It never logs credentials.
func VerifySSHAuth(c ClusterConfig) error {
	if strings.TrimSpace(c.SSH.Password) != "" {
		return nil
	}
	info, err := os.Stat(c.SSH.PrivateKey)
	if err != nil {
		return fmt.Errorf("ssh private key is not readable on the installer node")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("ssh private key permissions are too open (maximum 0600)")
	}
	return nil
}

// KubeKeyConfig returns a KubeKey Config spec as a plain map. Keeping it as a
// map lets the installer emit the same format as the upstream YAML template.
// artifactRoot is the offline artifact root; roles locate their fixed Chart and
// other material through it so nothing is fetched from the internet.
func KubeKeyConfig(c ClusterConfig, artifactPath, artifactRoot string, imageTable ImageTable) (map[string]any, error) {
	if err := Validate(c); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(artifactPath, "/") {
		return nil, fmt.Errorf("artifact path %q must be absolute", artifactPath)
	}
	if !strings.HasPrefix(artifactRoot, "/") {
		return nil, fmt.Errorf("artifact root %q must be absolute", artifactRoot)
	}
	registry, err := c.RegistryAddress()
	if err != nil {
		return nil, err
	}
	imageRefs, err := LocalImageReferences(imageTable, registry)
	if err != nil {
		return nil, err
	}
	// Split image references for the charts that build "registry/repository:tag"
	// themselves. Those charts must not be handed a whole reference in the
	// registry field, or the resulting path is doubled and never pulls. Only
	// the groups this run can actually render are required: the base profile
	// ends the chain at the network stack and its artifact (such as the
	// kubeovn one) intentionally ships none of the chart images.
	imageParts, err := SplitImageReferences(imageTable, registry, componentImageKeysForRun(c))
	if err != nil {
		return nil, err
	}

	components := componentSpec(c.Components, c.Name)
	nodeNames := make([]string, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		nodeNames = append(nodeNames, n.Name)
	}
	nodeAddresses := make([]string, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		nodeAddresses = append(nodeAddresses, n.Address)
	}
	// The Kube-OVN gateway and join network are derived/validated once here so
	// the template renders canonical values and never its historical hardcoded
	// literals (R10/A05).
	kubeovnNet, err := resolveKubeOVNNetwork(c)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"zone": "",
		"download": map[string]any{
			"fetch":         false,
			"artifact_file": artifactPath,
		},
		"kubernetes": map[string]any{
			"kube_version": "v1.35.8",
			"cluster_name": c.Name,
			"control_plane_endpoint": map[string]any{
				"type": "local",
			},
			"custom_labels": map[string]any{
				"networking.kubercloud.com/role": "master",
			},
		},
		"etcd": map[string]any{
			"deployment_type": "internal",
			"etcd_version":    "v3.6.6",
			"image": map[string]any{
				"registry":   registry,
				"repository": "kubernetes",
				"tag":        "v3.6.6",
			},
		},
		"image_registry": map[string]any{
			"type": "",
			"auth": map[string]any{
				"registry":        registry,
				"plain_http":      true,
				"username":        "",
				"password":        "",
				"skip_tls_verify": true,
			},
		},
		"cri": map[string]any{
			"container_manager":  "containerd",
			"containerd_version": "v2.3.4",
			"runc_version":       "v1.4.3",
			"registry": map[string]any{
				"insecure_registries": []string{registry},
			},
		},
		"cni": map[string]any{
			"type":         "none",
			"multi_cni":    "none",
			"pod_cidr":     c.Network.PodCIDR,
			"service_cidr": c.Network.ServiceCIDR,
		},
		"storage_class": map[string]any{
			"local": map[string]any{"enabled": false},
			"nfs":   map[string]any{"enabled": false},
		},
		"dns": map[string]any{
			"nodelocaldns": map[string]any{"enabled": false},
		},
		"ani": map[string]any{
			"registry":    registry,
			"images":      imageRefs,
			"image_parts": imageParts,
			"components":  components,
			"profile":     installProfile(c.Profile),
			"storage": map[string]any{
				"enabled":                 c.Storage.Enabled,
				"provider":                c.Storage.provider(),
				"makeDefaultStorageClass": c.Storage.MakeDefaultStorageClass,
				"externalClass":           c.Storage.ExternalClass,
				"nodes":                   storageNodesForTemplate(c.Storage),
			},
			"artifact_root":  artifactRoot,
			"nodes":          nodeNames,
			"node_addresses": nodeAddresses,
			"installer_node": c.InstallerNode,
			"network": map[string]any{
				"stack":                networkStack(c.Network.Stack),
				"management_interface": c.Network.ManagementInterface,
				"pod_cidr":             c.Network.PodCIDR,
				"service_cidr":         c.Network.ServiceCIDR,
				"kubeovn": map[string]any{
					"default_gateway": kubeovnNet.DefaultGateway,
					"join_cidr":       kubeovnNet.JoinCIDR,
				},
				"kcn": map[string]any{
					"managedDevices":   c.Network.KCN.ManagedDevices,
					"encapNetworks":    c.Network.KCN.EncapNetworks,
					"intranetNetworks": c.Network.KCN.IntranetNetworks,
				},
			},
		},
	}, nil
}

// ComponentSpecForRender exposes componentSpec with the documented defaults for
// a render check. It exists so a lab render exercises the same keys a role
// reads instead of keeping a second copy that can drift out of step.
//
// The component defaults are used as they stand rather than run through
// Validate: the render only needs the key shape, and Validate is the site gate
// that needs a full cluster, so going through it would make an offline render
// depend on a topology it is not describing.
func ComponentSpecForRender(clusterName string) map[string]any {
	c := DefaultComponents()
	// The metrics stack is on for a render check: its fields are what the role
	// template needs, and a disabled stack would render empty image fields.
	c.Metrics.Enabled = true
	return componentSpec(c, clusterName)
}

// componentSpec turns the typed component configuration into the map that the
// roles read. It is separate from KubeKeyConfig so the spec shape can be tested
// without going through the site validation gate, and so there is exactly one
// place that decides which keys a role sees. clusterName is only used to derive
// the run label that scopes Alertmanager routing to this installation.
func componentSpec(c Components, clusterName string) map[string]any {
	spec := map[string]any{}
	for _, row := range c.Selection() {
		entry := map[string]any{"enabled": row.Enabled}
		if storage := c.storage(row.Name); storage != nil {
			entry["storage_class"] = storage.StorageClass
			entry["storage_size"] = storage.StorageSize
		}
		spec[row.Name] = entry
	}
	// The metrics stack carries its own storage and retention fields. The
	// values are always present so a role can render them without re-reading
	// the site YAML, even when the stack is disabled.
	spec["metrics"] = map[string]any{
		"enabled":                   c.Metrics.Enabled,
		"namespace":                 MetricsNamespace,
		"storage_class":             c.Metrics.StorageClass,
		"prometheus_storage_size":   c.Metrics.PrometheusStorageSize,
		"alertmanager_storage_size": c.Metrics.AlertmanagerStorageSize,
		"prometheus_retention":      c.Metrics.PrometheusRetention,
		"prometheus_retention_size": c.Metrics.PrometheusRetentionSize,
		// The run label is what scopes the Alertmanager route and the role's
		// temporary objects to this installation. An empty value keeps a
		// disabled stack from matching anything at all.
		"run_id": metricsRunID(c, clusterName),
	}
	// The log backend is one string, not a pair of booleans, so a role cannot
	// see two enabled backends. The loki/opensearch/fluent-bit rows above are
	// derived from this same backend, so they can never disagree with it.
	spec["logging"] = map[string]any{
		"enabled":       c.loggingEnabled(),
		"backend":       c.Logging.LogBackend(),
		"namespace":     MetricsNamespace,
		"storage_class": c.Logging.StorageClass,
		"storage_size":  c.Logging.StorageSize,
		// The site states retentionDays as a plain day count. Loki's
		// retention_period is a duration, and the two backends need different
		// units, so the hour count is derived here once instead of letting each
		// role template multiply. An unparseable value yields 0, which the
		// validation above already rejects before a role could render it.
		"retention_days":  c.Logging.RetentionDays,
		"retention_hours": retentionHours(c.Logging.RetentionDays),
		"retention_iso":   retentionISOSeconds(c.Logging.RetentionDays),
	}
	return spec
}

// retentionHours converts the plain day count the site configures into the hour
// count Loki's retention_period uses. An invalid value becomes 0 so a template
// can never render a nonsense duration.
func retentionHours(days string) int {
	n, err := strconv.Atoi(strings.TrimSpace(days))
	if err != nil || n <= 0 {
		return 0
	}
	return n * 24
}

// retentionISOSeconds renders the same day count as the ISO-8601 duration the
// OpenSearch index-state-management policy takes on its `min_index_age` field
// (for example 3d -> PT72H). ISM compares true durations, so the plain day count
// would be rejected there; deriving it here keeps the unit conversion in one
// place instead of duplicating arithmetic in a role template.
func retentionISOSeconds(days string) string {
	n, err := strconv.Atoi(strings.TrimSpace(days))
	if err != nil || n <= 0 {
		return ""
	}
	return "PT" + strconv.Itoa(n*24) + "H"
}

// KubeKeyInventory returns an Inventory spec with local connector only for
// the installer and native SSH connectors for the other two nodes.
func KubeKeyInventory(c ClusterConfig) (map[string]any, error) {
	if err := Validate(c); err != nil {
		return nil, err
	}
	hosts := map[string]any{}
	for _, n := range c.Nodes {
		var connector map[string]any
		if n.Name == c.InstallerNode {
			connector = map[string]any{
				"type": "local",
				"user": c.SSH.User,
			}
		} else {
			connector = map[string]any{
				"type": "ssh",
				"host": n.Address,
				"port": c.SSH.Port,
				"user": c.SSH.User,
			}
			if strings.TrimSpace(c.SSH.Password) != "" {
				connector["password"] = c.SSH.Password
			} else {
				connector["private_key"] = c.SSH.PrivateKey
			}
		}
		hosts[n.Name] = map[string]any{
			"connector":     connector,
			"internal_ipv4": n.Address,
		}
	}
	names := make([]string, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		names = append(names, n.Name)
	}
	return map[string]any{
		"hosts": hosts,
		"groups": map[string]any{
			"k8s_cluster":        map[string]any{"groups": []string{"kube_control_plane", "kube_worker"}},
			"kube_control_plane": map[string]any{"hosts": names},
			"kube_worker":        map[string]any{"hosts": names},
			"etcd":               map[string]any{"hosts": names},
		},
	}, nil
}
func LocalImageReferences(table ImageTable, registry string) (map[string]string, error) {
	refs := make(map[string]string, len(table))
	for original := range table {
		ref, err := table.LocalReference(original, registry)
		if err != nil {
			return nil, err
		}
		refs[original] = ref
	}
	return refs, nil
}

// aniRoleEnabled reports whether an ANI role's files are part of the selected
// install chain, so the render command skips disabled components instead of
// failing on their deliberately absent materials.
func aniRoleEnabled(role string, c ClusterConfig) bool {
	switch role {
	case "kcn", "envoy", "smoke":
		return networkStack(c.Network.Stack) == "kcn" && installProfile(c.Profile) != "base"
	case "kubeovn":
		return networkStack(c.Network.Stack) == "kubeovn"
	case "ceph":
		return c.Storage.Enabled && c.Storage.provider() == storageProviderCeph && installProfile(c.Profile) != "base"
	case "cert-manager":
		return c.Components.CertManager.Enabled && installProfile(c.Profile) != "base"
	case "postgresql":
		return c.Components.PostgreSQL.Enabled && installProfile(c.Profile) != "base"
	case "valkey":
		return c.Components.Valkey.Enabled && installProfile(c.Profile) != "base"
	case "nats":
		return c.Components.NATS.Enabled && installProfile(c.Profile) != "base"
	case "metrics":
		return c.Components.Metrics.Enabled && installProfile(c.Profile) != "base"
	case "loki":
		return c.Components.Logging.LogBackend() == loggingLoki && installProfile(c.Profile) != "base"
	case "opensearch":
		return c.Components.Logging.LogBackend() == loggingOpenSearch && installProfile(c.Profile) != "base"
	case "fluent-bit":
		return c.Components.loggingEnabled() && installProfile(c.Profile) != "base"
	default:
		return true
	}
}

// RenderSite renders every file of every enabled ANI role with the production
// template context (R08): the same FuncMap, the same config generator and the
// same image table the installer uses. The result is a plain map of relative
// path -> rendered bytes, with no cluster access and no apply.
type r08Heredoc struct {
	delimiter string
	body      string
}

// heredocBodies extracts the bodies of unquoted shell here-documents so their
// content can be validated as YAML instead of being trusted by bash -n alone.
func heredocBodies(content string) []r08Heredoc {
	pattern := regexp.MustCompile(`(?m)^[^\n]*<<-?\s*['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?\s*$`)
	lines := strings.Split(content, "\n")
	var blocks []r08Heredoc
	for index, line := range lines {
		match := pattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		delimiter := match[1]
		var body []string
		for _, inner := range lines[index+1:] {
			if strings.TrimSpace(inner) == delimiter {
				blocks = append(blocks, r08Heredoc{delimiter: delimiter, body: strings.Join(body, "\n")})
				break
			}
			body = append(body, inner)
		}
	}
	return blocks
}

// RenderedFile pairs a rendered role file with its template source, so the
// semantic checks can tell legitimate runtime-bound values apart from defects.
type RenderedFile struct {
	Name     string
	Source   []byte
	Rendered []byte
}

func RenderSite(rolesDir string, c ClusterConfig, artifactRoot string, table ImageTable) ([]RenderedFile, error) {
	spec, err := KubeKeyConfig(c, filepath.Join(artifactRoot, "packages", "kubekey-artifact.tgz"), artifactRoot, table)
	if err != nil {
		return nil, err
	}
	// The executor merges the inventory (hosts/groups) with the config map in
	// production, so the render merges both generators too — no hand-written
	// third context (R08).
	inventory, err := KubeKeyInventory(c)
	if err != nil {
		return nil, err
	}
	for key, value := range inventory {
		if _, exists := spec[key]; !exists {
			spec[key] = value
		}
	}
	files := []RenderedFile{}
	walkErr := filepath.WalkDir(rolesDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || (!strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".sh") && !strings.HasSuffix(path, ".txt")) {
			return nil
		}
		roleRel, err := filepath.Rel(rolesDir, path)
		if err != nil {
			return err
		}
		role := strings.Split(filepath.ToSlash(roleRel), "/")[0]
		if !aniRoleEnabled(role, c) {
			return nil
		}
		parsed, parseErr := template.New(filepath.Base(path)).Funcs(kkTmpl.FuncMap()).ParseFiles(path)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		tmpl := parsed
		name := filepath.Base(path)
		if name == "main.yaml" {
			name = role + "-tasks-main.yaml"
		} else if name == "verify.sh" {
			name = role + "-verify.sh"
		} else if name == "values.yaml" {
			name = role + "-values.yaml"
		} else {
			name = role + "-" + name
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rendered := &bytes.Buffer{}
		if err := tmpl.Execute(rendered, spec); err != nil {
			return fmt.Errorf("render %s: %w", path, err)
		}
		files = append(files, RenderedFile{Name: name, Source: source, Rendered: rendered.Bytes()})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no role files were rendered from %s", rolesDir)
	}
	return files, nil
}

// renderSource keeps the template source of one rendered file.
func renderSource(files []RenderedFile, name string) string {
	for _, file := range files {
		if file.Name == name {
			return string(file.Source)
		}
	}
	return ""
}

// vendorMaterialFile names the upstream documents the roles ship verbatim (with
// only the image references rewritten): their schema placeholders are vendor
// text, not defects.
var vendorMaterialFiles = []string{"crds.yaml", "csi-operator.yaml", "install.yaml"}

// ValidateRenderedArtifacts runs the semantic checks over rendered output that
// a plain `bash -n` cannot provide (R08):
//   - no unrendered or missing template values (`{{`, `<no value>`), except the
//     two legitimate runtime-bound shapes (loop variables and command outputs
//     registered by earlier tasks), which are detected from the template source;
//   - no leftover placeholder or empty image references;
//   - every shell heredoc body that claims to be YAML parses as YAML;
//   - no duplicate apiVersion/kind/name resource in the rendered manifests.
//
// Upstream vendor documents (CRDs, the csi-operator manifest, the official
// envoy install manifest) ship schema placeholders and their own structure, so
// the shape checks skip them while the digest/identity checks do not.
func ValidateRenderedArtifacts(files []RenderedFile) error {
	var problems []string
	sorted := make([]RenderedFile, 0, len(files))
	for _, file := range files {
		sorted = append(sorted, file)
	}
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	resourceNames := map[string]string{}
	for _, file := range sorted {
		content := string(file.Rendered)
		source := string(file.Source)
		vendor := false
		for _, suffix := range vendorMaterialFiles {
			if strings.HasSuffix(file.Name, "-"+suffix) {
				vendor = true
				break
			}
		}

		if strings.Contains(content, "<no value>") {
			loopBound := strings.Contains(source, ".item") || strings.Contains(source, "(item")
			runtimeRegistered := strings.Contains(source, ".stdout")
			if !loopBound && !runtimeRegistered {
				problems = append(problems, fmt.Sprintf("%s: contains <no value> (an undefined template value rendered through)", file.Name))
			}
		}
		if !vendor && strings.Contains(content, "{{") {
			problems = append(problems, fmt.Sprintf("%s: contains unrendered {{...}} template delimiters", file.Name))
		}
		if strings.Contains(content, "REPLACE_") {
			problems = append(problems, fmt.Sprintf("%s: contains a leftover REPLACE_ placeholder", file.Name))
		}
		// The image keys render repository/tag fields (SplitImageReferences);
		// an empty one means a key resolved to nothing. A bare `image:` key with
		// a nested map is legitimate YAML and is not flagged.
		if !vendor && (regexp.MustCompile(`repository:\s*(""|$)`).MatchString(content) ||
			regexp.MustCompile(`tag:\s*(""|$)`).MatchString(content)) {
			problems = append(problems, fmt.Sprintf("%s: contains an empty image reference", file.Name))
		}
		if !vendor {
			// Every shell heredoc body that claims to be YAML must actually
			// parse. Heredocs with the PY delimiter are embedded python (the
			// repo convention) and are executed from fixtures instead.
			for _, block := range heredocBodies(content) {
				if block.delimiter == "PY" {
					continue
				}
				var probe any
				if err := yaml.Unmarshal([]byte(block.body), &probe); err != nil {
					problems = append(problems, fmt.Sprintf("%s: heredoc %s does not parse as YAML: %v", file.Name, block.delimiter, err))
				}
			}
			// Rendered k8s resources must not repeat apiVersion/kind/name.
			for _, doc := range strings.Split(content, "\n---\n") {
				var resource struct {
					APIVersion string `yaml:"apiVersion"`
					Kind       string `yaml:"kind"`
					Metadata   struct {
						Name      string `yaml:"name"`
						Namespace string `yaml:"namespace"`
					} `yaml:"metadata"`
				}
				if yaml.Unmarshal([]byte(doc), &resource) != nil {
					continue
				}
				if resource.APIVersion == "" || resource.Kind == "" || resource.Metadata.Name == "" {
					continue
				}
				if !strings.Contains(resource.APIVersion, "/") && resource.APIVersion != "v1" {
					continue
				}
				key := resource.APIVersion + "/" + resource.Kind + "/" + resource.Metadata.Namespace + "/" + resource.Metadata.Name
				if previous, ok := resourceNames[key]; ok && previous != file.Name {
					problems = append(problems, fmt.Sprintf("%s and %s both render %s", previous, file.Name, key))
				}
				resourceNames[key] = file.Name
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("rendered artifacts have %d problem(s):\n%s", len(problems), strings.Join(problems, "\n"))
}

// RunRender is the implementation of `kk ani render`: validate the config,
// render every enabled role file with the production context, run the semantic
// checks, and write the result into the output directory. It never touches a
// live cluster and never applies anything.
func RunRender(input ValidateInput, rolesDir string, stdout io.Writer) error {
	cluster, err := LoadClusterConfig(input.ConfigFile)
	if err != nil {
		return err
	}
	if err := Validate(cluster); err != nil {
		return errors.Wrap(err, "validate cluster config")
	}
	tableRows, err := os.ReadFile(filepath.Join(strings.TrimSpace(input.PackageRoot), "ani", "images.tsv"))
	if err != nil {
		if _, altErr := os.Stat(filepath.Join(strings.TrimSpace(input.PackageRoot), "images", "images.tsv")); altErr != nil {
			return errors.Wrapf(err, "read %s/ani/images.tsv (the render needs the packaged image table)", input.PackageRoot)
		}
		tableRows, err = os.ReadFile(filepath.Join(strings.TrimSpace(input.PackageRoot), "images", "images.tsv"))
		if err != nil {
			return errors.Wrapf(err, "read the packaged image table under %s", input.PackageRoot)
		}
	}
	table, err := LoadImageTable(strings.Split(string(tableRows), "\n"))
	if err != nil {
		return err
	}
	files, err := RenderSite(rolesDir, cluster, strings.TrimSpace(input.PackageRoot), table)
	if err != nil {
		return err
	}
	if err := ValidateRenderedArtifacts(files); err != nil {
		return err
	}
	if strings.TrimSpace(input.Output) != "" {
		if err := os.MkdirAll(input.Output, 0o755); err != nil {
			return errors.Wrapf(err, "create render output directory %s", input.Output)
		}
		for _, file := range files {
			target := filepath.Join(input.Output, file.Name)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return errors.Wrapf(err, "create directory for %s", target)
			}
			if err := os.WriteFile(target, file.Rendered, 0o644); err != nil {
				return errors.Wrapf(err, "write %s", target)
			}
		}
	}
	out := stdout
	if out == nil {
		out = os.Stdout
	}
	fmt.Fprintf(out, "rendered %d files for cluster=%s profile=%s network=%s (no live API access, nothing applied)\n",
		len(files), cluster.Name, installProfile(cluster.Profile), networkStack(cluster.Network.Stack))
	return nil
}

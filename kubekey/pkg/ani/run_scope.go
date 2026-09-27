package ani

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cockroachdb/errors"
)

// RunScope is the one execution context a run decides once and then hands to
// every layer that touches the cluster: the kubeconfig to use, where this run's
// logs go, and where this run's connection fragments are written and read back.
//
// C07: before this existed, each layer chose for itself. The roles hardcoded
// /etc/kubernetes/admin.conf into the checker's environment, so an explicitly
// non-default --kubeconfig reached the installer and the Helm/kubectl calls but
// not the checker; the checkers then also defaulted the same variable to
// admin.conf when it was absent; and every role wrote its connection fragment
// into the base install's canonical connections.d, so a components run polluted
// the install's aggregation directory while the run's own connections.md was
// assembled from that same shared place.
//
// It travels through the mechanism this repository already has: the map
// KubeKeyConfig returns is written verbatim into the Config spec, KubeKey's
// executor turns that spec into the template/condition context, and roles read
// it as {{ .ani.run.* }}. It is deliberately not an inherited environment — the
// connectors build the remote environment themselves and a parent
// os.Environ() would quietly not cross an SSH boundary.
type RunScope struct {
	// Kubeconfig is the file every kubectl, Helm and checker invocation uses.
	Kubeconfig string
	// LogsDir receives this run's checker logs.
	LogsDir string
	// ConnectionsDir receives this run's per-component fragments, and is the only
	// directory the aggregator reads.
	ConnectionsDir string
	// RunID names the run whose facts these are; empty for the first install,
	// whose runtime root is the cluster root itself.
	RunID string
	// KKBinary is this run's own executable, handed to the packaged checkers so a
	// probe they created is removed through the conditional delete below and not
	// through a delete-by-name. It is optional by design: a checker that cannot
	// find it must report its cleanup as incomplete rather than guess a way to
	// delete an object it can no longer condition on.
	KKBinary string
}

// InstallRunScope is the first install's context. admin.conf is the default here
// on purpose: the installer is the node being installed, and the file is created
// by the cluster initialisation this run performs. Because it does not exist yet
// when the scope is chosen, its presence is asserted by the roles before any
// component writes (see RequireKubeconfigOnDisk), not by an entry-point check
// that would have to run before the thing that produces the file.
func InstallRunScope(clusterName string) RunScope {
	base := filepath.Join(runtimeBaseDir, clusterName)
	return RunScope{
		Kubeconfig:     DefaultKubeconfigPath,
		LogsDir:        filepath.Join(base, "logs"),
		ConnectionsDir: filepath.Join(base, "work", connectionsDirName),
	}
}

// ComponentsRunScope is one `components execute` run's context. The kubeconfig is
// whatever this command was given and has already been used to bind the live
// cluster, so the addition cannot verify one cluster and install into another;
// the directories live under the run's own root, so the base install's
// connections.md, its fragments and its records are never what this run writes
// to or reads from.
func ComponentsRunScope(clusterName, runID, outputRoot, kubeconfig string) RunScope {
	root := filepath.Join(strings.TrimSpace(outputRoot), "components-"+runID)
	return RunScope{
		Kubeconfig:     strings.TrimSpace(kubeconfig),
		LogsDir:        filepath.Join(root, "logs"),
		ConnectionsDir: filepath.Join(root, "work", connectionsDirName),
		RunID:          runID,
	}
}

// DefaultKubeconfigPath is the cluster-admin config the installer itself creates.
const DefaultKubeconfigPath = "/etc/kubernetes/admin.conf"

// Validate refuses a scope that cannot describe a real run. A missing kubeconfig
// is only checkable where the file is expected to exist already, so the first
// install calls this after cluster initialisation and components execute calls it
// before it writes anything at all.
func (s RunScope) Validate() error {
	for _, field := range []struct{ name, value string }{
		{"kubeconfig", s.Kubeconfig}, {"logs dir", s.LogsDir}, {"connections dir", s.ConnectionsDir},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("the run scope has no %s", field.name)
		}
		if !filepath.IsAbs(field.value) {
			return fmt.Errorf("the run scope %s %q must be an absolute path", field.name, field.value)
		}
	}
	info, err := os.Stat(s.Kubeconfig)
	if err != nil {
		return errors.Wrapf(err, "the run's kubeconfig %s cannot be read", s.Kubeconfig)
	}
	if info.IsDir() {
		return fmt.Errorf("the run's kubeconfig %s is a directory", s.Kubeconfig)
	}
	// Existence is not readability of the identity: the caller that has a client
	// still resolves and compares the cluster uid. This catches the cheaper
	// "wrong machine, no such file" case before any component is touched.
	if _, err := os.ReadFile(s.Kubeconfig); err != nil {
		return errors.Wrapf(err, "the run's kubeconfig %s is not readable", s.Kubeconfig)
	}
	return nil
}

// specKey is where the scope is published into the KubeKey config map.
const specKey = "run"

// SpecMap is the scope as it appears in the config spec. KubeKeyConfig builds it,
// so every consumer of the spec — the render gate, the role templates and the
// condition engine — sees the same three paths.
func (s RunScope) SpecMap() map[string]any {
	return map[string]any{
		"kubeconfig":      s.Kubeconfig,
		"logs_dir":        s.LogsDir,
		"connections_dir": s.ConnectionsDir,
		"run_id":          s.RunID,
		"kk_bin":          s.KKBinary,
	}
}

// Apply writes the scope into a KubeKey config spec under `.ani.run`, which is
// what roles then read as `{{ .ani.run.kubeconfig }}` and friends. It refuses to
// overwrite a scope that was already chosen, because two different answers in one
// run is precisely the bug this exists to prevent.
func (s RunScope) Apply(spec map[string]any) error {
	if spec == nil {
		return errors.New("cannot apply a run scope to a nil config spec")
	}
	ani, _ := spec["ani"].(map[string]any)
	if ani == nil {
		return errors.New("the config spec carries no `.ani` block to hold the run scope")
	}
	if previous, ok := ani[specKey].(map[string]any); ok && len(previous) > 0 {
		return fmt.Errorf("a run scope is already applied (%v); one run must decide its context once", previous)
	}
	ani[specKey] = s.SpecMap()
	return nil
}

// Replace is Apply for the one case that legitimately re-decides the context: a
// components run owns its own directories rather than the install's. It refuses
// to run unless a scope was already applied, so it cannot paper over a generator
// that forgot to build one.
func (s RunScope) Replace(spec map[string]any) error {
	ani, _ := spec["ani"].(map[string]any)
	if ani == nil {
		return errors.New("the config spec carries no `.ani` block to hold the run scope")
	}
	if previous, ok := ani[specKey].(map[string]any); !ok || len(previous) == 0 {
		return errors.New("no run scope was applied to this spec; replacing nothing would hide a generator that never built one")
	}
	ani[specKey] = s.SpecMap()
	return nil
}

// FromSpec reads the scope back out of a config spec. Both the roles and the Go
// aggregator use this one source, so a fragment directory that was written under
// one path can never be read back from another.
func (s *RunScope) FromSpec(spec map[string]any) error {
	ani, _ := spec["ani"].(map[string]any)
	raw, _ := ani[specKey].(map[string]any)
	if len(raw) == 0 {
		return errors.New("the config spec carries no `.ani.run` block; the run scope was never applied")
	}
	read := func(key string) string { value, _ := raw[key].(string); return value }
	*s = RunScope{
		Kubeconfig:     read("kubeconfig"),
		LogsDir:        read("logs_dir"),
		ConnectionsDir: read("connections_dir"),
		RunID:          read("run_id"),
		KKBinary:       read("kk_bin"),
	}
	return s.Validate()
}

// RequireKubeconfigOnDisk is the shell guard the roles run before they write any
// component material. It belongs in the role, not only in Go, because the roles
// may be executed on a host reached over SSH where a file the parent could read
// is simply not there — and in that case failing before the first write is the
// only acceptable outcome. Sharing an admin credential by falling back locally
// would be a different, worse bug.
func RequireKubeconfigOnDisk() string {
	return `kubeconfig_file="$1"
if [ -z "$kubeconfig_file" ]; then
  echo "no kubeconfig was supplied for this run; refusing to guess one" >&2
  exit 1
fi
if [ ! -f "$kubeconfig_file" ]; then
  echo "kubeconfig $kubeconfig_file does not exist on this host; refusing to write component material against an unreachable context" >&2
  exit 1
fi`
}

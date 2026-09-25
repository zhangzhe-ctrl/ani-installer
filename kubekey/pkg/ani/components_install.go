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
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
)

// ---------------------------------------------------------------------------
// R15.1/A14: `kk ani components install` — plan and rejection paths only.
//
// This card delivers the pure plan: CLI parsing, the --only closure, the
// scope/owner preflight against the ORIGINAL base run and the live cluster,
// and the plan output (a new run.json for the follow-up execution card). It
// never mutates the cluster: no create_cluster, no kubeadm, no CNI, no Ceph,
// no Envoy, and no component execution (that is R15.2's playbook).
// ---------------------------------------------------------------------------

// The Kubernetes version every offline artifact of this installer ships; a
// live cluster on a different version is not the base this run assumes.
const componentsKubeVersion = "v1.35.8"

// componentInstallSpec is the pre-declared install identity of one canonical
// component: where it lives, who owns it (helm release or our rendered
// manifest) and what it requires. It is data, so a component cannot invent
// mutation scope at plan time.
type componentInstallSpec struct {
	Namespace    string
	Release      string // helm release name; empty = rendered manifest, no helm
	Chart        string // expected chart name for helm components
	ChartVersion string // expected chart version for helm components
	WorkloadKind string // primary workload for manifest components
	WorkloadName string
	NeedsStorage bool
	InternalDeps []string // static technical prerequisites, shown in the plan
}

// componentsDeferred lists the IDs later batches will add; --only rejects
// them with the deferred message instead of plain "unknown".
var componentsDeferred = []string{
	"metrics-server", "snapshot-controller", "milvus", "kubevirt", "cdi",
	"volcano", "harbor", "notebooks", "trainer", "hub", "kserve", "pipelines",
}

// componentInstallSpecs covers every canonical component ID of
// componentsOrder. Ids outside this map (metrics-server, milvus, ...) are
// deferred batches: --only rejects them outright.
var componentInstallSpecs = map[string]componentInstallSpec{
	"cert-manager": {Namespace: "cert-manager", Release: "cert-manager", Chart: "cert-manager", ChartVersion: "1.21.2", WorkloadKind: "Deployment", WorkloadName: "cert-manager"},
	"postgresql":   {Namespace: "ani-platform", WorkloadKind: "StatefulSet", WorkloadName: "postgresql", NeedsStorage: true},
	"valkey":       {Namespace: "ani-platform", WorkloadKind: "StatefulSet", WorkloadName: "valkey", NeedsStorage: true},
	"nats":         {Namespace: "ani-platform", Release: "nats", Chart: "nats", ChartVersion: "2.14.6", WorkloadKind: "StatefulSet", WorkloadName: "nats", NeedsStorage: true},
	"metrics":      {Namespace: "ani-observability", Release: "ani-metrics", Chart: "kube-prometheus-stack", ChartVersion: "85.4.0", WorkloadKind: "StatefulSet", WorkloadName: "ani-metrics-prometheus", NeedsStorage: true},
	"loki":         {Namespace: "ani-observability", Release: "ani-loki", Chart: "loki", ChartVersion: "18.13.3", WorkloadKind: "StatefulSet", WorkloadName: "ani-loki", NeedsStorage: true},
	"opensearch":   {Namespace: "ani-observability", Release: "ani-opensearch-master", Chart: "opensearch", ChartVersion: "3.8.0", WorkloadKind: "StatefulSet", WorkloadName: "ani-opensearch-master", NeedsStorage: true, InternalDeps: []string{"fluent-bit"}},
	"fluent-bit":   {Namespace: "ani-observability", Release: "ani-fluent-bit", Chart: "fluent-bit", ChartVersion: "0.58.2", WorkloadKind: "DaemonSet", WorkloadName: "ani-fluent-bit"},
}

// ComponentsInstallInput is the input of `kk ani components install`.
type ComponentsInstallInput struct {
	ConfigFile  string
	PackageRoot string
	Only        []string
	// BaseRunFile is the original base install's run.json; the baseline
	// invariants (cluster identity, nodes, main CNI, registry identity) are
	// compared against it. The new and old config digests are recorded
	// separately — adding components necessarily changes the digest.
	BaseRunFile string
	StateFile   string
	Kubeconfig  string
	Output      string
}

// ComponentsPlanComponent is one component's row in the plan.
type ComponentsPlanComponent struct {
	Component    string            `json:"component"`
	Status       string            `json:"status"` // planned | already_installed
	Namespace    string            `json:"namespace"`
	Owner        string            `json:"owner"` // helm release or rendered manifest
	InternalDeps []string          `json:"internalDeps,omitempty"`
	Images       int               `json:"images"`
	Charts       int               `json:"charts"`
	Evidence     map[string]string `json:"evidence,omitempty"`
	Detail       string            `json:"detail,omitempty"`
}

// r15PlanComponent builds one result row (the plan and execution reports
// share the same component row shape).
func r15PlanComponent(component, status, detail string) ComponentsPlanComponent {
	return ComponentsPlanComponent{Component: component, Status: status, Detail: detail}
}

// ComponentsPlan is the machine-readable plan plus its live preflight facts.
type ComponentsPlan struct {
	SchemaVersion    int                        `json:"schemaVersion"`
	RunID            string                     `json:"runId"`
	ClusterName      string                     `json:"clusterName"`
	BaseConfigDigest string                     `json:"baseConfigDigest"`
	NewConfigDigest  string                     `json:"newConfigDigest"`
	Invariants       map[string]string          `json:"invariants"`
	Components       []ComponentsPlanComponent  `json:"components"`
	Excluded         []string                   `json:"excluded"`
	LiveChecks       map[string]string          `json:"liveChecks"`
	StartedAt        string                     `json:"startedAt"`
	FinishedAt       string                     `json:"finishedAt"`
	Overall          string                     `json:"overall"`
}

// componentsPlanExcluded states — by name — what a components-only run never
// touches, so a plan is self-evidencing about the base.
var componentsPlanExcluded = []string{
	"create_cluster", "kubeadm", "CNI roles", "Ceph/storage-class tasks",
	"kcn/Envoy roles", "registry lifecycle",
}

// RunComponentsInstallPlan builds the pure plan. Every rejection happens
// BEFORE any file is written, so a failed plan leaves no output behind.
func RunComponentsInstallPlan(ctx context.Context, input ComponentsInstallInput, stdout io.Writer) error {
	clusterPtr, manifest, err := loadComponentsInstallConfig(input)
	if err != nil {
		return err
	}
	cluster := *clusterPtr
	scope, err := componentsScope(cluster, input.Only)
	if err != nil {
		return err
	}
	baseManifest, err := loadBaseRunRecord(input.BaseRunFile)
	if err != nil {
		return err
	}
	invariants, err := compareBaseInvariants(baseManifest, *manifest)
	if err != nil {
		return err
	}

	runner := kubectlRunner{bin: kubectlBin(), kubeconfig: input.kubeconfigOrDefault()}
	live, err := preflightLiveCluster(ctx, runner, cluster, scope)
	if err != nil {
		return err
	}
	planComponents, err := preflightComponentOwnership(ctx, runner, cluster, scope)
	if err != nil {
		return err
	}
	if err := preflightComponentImages(ctx, cluster, scope); err != nil {
		return err
	}

	// The plan identity: a fresh components run id, deterministic per
	// invocation time like the install runner's.
	runID := fmt.Sprintf("ani-components-%s-%s", manifest.ClusterName, time.Now().Format("20060102-150405"))
	plan := &ComponentsPlan{
		SchemaVersion:    1,
		RunID:            runID,
		ClusterName:      manifest.ClusterName,
		BaseConfigDigest: baseManifest.ConfigDigest,
		NewConfigDigest:  manifest.ConfigDigest,
		Invariants:       invariants,
		Components:       planComponents,
		Excluded:         componentsPlanExcluded,
		LiveChecks:       live,
		StartedAt:        time.Now().UTC().Format(time.RFC3339),
		Overall:          VerifyStatusPass,
	}
	plan.FinishedAt = time.Now().UTC().Format(time.RFC3339)

	// All checks passed: only now are any files written.
	outDir := strings.TrimSpace(input.Output)
	if outDir == "" {
		outDir = "/var/lib/ani-installer/components"
	}
	if err := writeComponentsPlan(outDir, runID, plan); err != nil {
		return err
	}
	// The new run record: the enabled-components config, for the execution
	// card and for `kk ani verify --run <new run.json>`.
	if err := WriteRunOutputs(filepath.Join(outDir, "run"), *manifest); err != nil {
		return err
	}

	out := stdout
	if out == nil {
		out = os.Stdout
	}
	for _, component := range plan.Components {
		fmt.Fprintf(out, "components plan %s: %s\n", component.Component, component.Status)
	}
	fmt.Fprintf(out, "components plan written: run=%s plan=%s runRecord=%s\n",
		runID, filepath.Join(outDir, fmt.Sprintf("components-plan-%s.json", runID)),
		filepath.Join(outDir, "run", RunManifestFileName))
	fmt.Fprintln(out, "NOTE: this is the R15.1 plan only; executing the planned components is the R15.2 card")
	return nil
}

func (input *ComponentsInstallInput) kubeconfigOrDefault() string {
	if strings.TrimSpace(input.Kubeconfig) != "" {
		return strings.TrimSpace(input.Kubeconfig)
	}
	return "/etc/kubernetes/admin.conf"
}

// loadComponentsInstallConfig parses and validates the NEW site config (the
// one that has the component enabled).
func loadComponentsInstallConfig(input ComponentsInstallInput) (*ClusterConfig, *RunManifest, error) {
	if strings.TrimSpace(input.ConfigFile) == "" {
		return nil, nil, errors.New("components install needs --config")
	}
	if strings.TrimSpace(input.PackageRoot) == "" {
		return nil, nil, errors.New("components install needs --package-root")
	}
	cluster, err := LoadClusterConfig(input.ConfigFile)
	if err != nil {
		return nil, nil, err
	}
	if err := Validate(cluster); err != nil {
		return nil, nil, errors.Wrap(err, "validate cluster config")
	}
	manifest, err := BuildRunManifest(cluster)
	if err != nil {
		return nil, nil, err
	}
	manifest.PackageRoot = strings.TrimSpace(input.PackageRoot)
	return &cluster, &manifest, nil
}

// componentsScope resolves --only: required, known, implemented, enabled in
// the config, storage-feasible, and closed over static internal dependencies.
func componentsScope(cluster ClusterConfig, only []string) ([]string, error) {
	trimmed := make([]string, 0, len(only))
	for _, name := range only {
		if name = strings.TrimSpace(name); name != "" {
			trimmed = append(trimmed, name)
		}
	}
	only = trimmed
	if len(only) == 0 {
		return nil, errors.New("--only is required and must name at least one component; a components run never implies a scope")
	}
	enabled := map[string]bool{}
	for _, row := range cluster.Components.Selection() {
		enabled[row.Name] = row.Enabled
	}
	var scope []string
	seen := map[string]bool{}
	var add func(name string) error
	add = func(name string) error {
		if seen[name] {
			return nil
		}
		spec, known := componentInstallSpecs[name]
		if !known {
			for _, deferred := range componentsDeferred {
				if name == deferred {
					return fmt.Errorf("component %q is a deferred batch; this installer does not implement it yet", name)
				}
			}
			return fmt.Errorf("unknown component id %q", name)
		}
		if !enabled[name] {
			return fmt.Errorf("component %q is not enabled in the site config; enable it there first — a components run never edits the config silently", name)
		}
		seen[name] = true
		for _, dep := range spec.InternalDeps {
			if !enabled[dep] {
				return fmt.Errorf("component %q requires %q, which the site config has not enabled", name, dep)
			}
			if err := add(dep); err != nil {
				return err
			}
		}
		scope = append(scope, name)
		return nil
	}
	for _, name := range only {
		if err := add(strings.TrimSpace(name)); err != nil {
			return nil, err
		}
	}
	// Storage is a cross-capability prerequisite: a storage-backed component
	// without an effective class is refused — the plan never deploys Ceph.
	class := effectiveStorageClass(cluster)
	for _, name := range scope {
		if componentInstallSpecs[name].NeedsStorage && class == "" {
			return nil, fmt.Errorf("component %q needs a storage class, but the site has no storage selected; prepare that batch's base per the blueprint — a components run never deploys Ceph", name)
		}
	}
	return scope, nil
}

// loadBaseRunRecord reads the original base install's run.json.
func loadBaseRunRecord(path string) (RunManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RunManifest{}, errors.Wrapf(err, "read the base run record %s", path)
	}
	var manifest RunManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return RunManifest{}, errors.Wrapf(err, "parse the base run record %s", path)
	}
	if manifest.ConfigDigest == "" || manifest.ClusterName == "" {
		return RunManifest{}, fmt.Errorf("%s is not a run.json record", path)
	}
	return manifest, nil
}

// compareBaseInvariants enforces the base invariants between the original run
// and the new (component-enabled) config: cluster identity, nodes, main CNI,
// profile and registry identity. The two config digests are intentionally NOT
// compared — adding components changes the digest by design — and both are
// recorded in the plan.
func compareBaseInvariants(base, new RunManifest) (map[string]string, error) {
	invariants := map[string]string{
		"clusterName":   base.ClusterName,
		"networkStack":  base.NetworkStack,
		"profile":       base.Profile,
		"registry":      fmt.Sprintf("%s:%d", base.Installer.RegistryHost, base.Installer.RegistryPort),
		"installerNode": base.Installer.Name,
	}
	check := func(field, baseValue, newValue string) error {
		if baseValue != newValue {
			return fmt.Errorf("base invariant %q changed: base=%q new=%q; adding components must not alter the base", field, baseValue, newValue)
		}
		return nil
	}
	if err := check("clusterName", base.ClusterName, new.ClusterName); err != nil {
		return nil, err
	}
	if err := check("networkStack", base.NetworkStack, new.NetworkStack); err != nil {
		return nil, err
	}
	if err := check("profile", base.Profile, new.Profile); err != nil {
		return nil, err
	}
	if err := check("registry", invariants["registry"], fmt.Sprintf("%s:%d", new.Installer.RegistryHost, new.Installer.RegistryPort)); err != nil {
		return nil, err
	}
	if err := check("installerNode", base.Installer.Name, new.Installer.Name); err != nil {
		return nil, err
	}
	baseNodes := map[string]string{}
	for _, node := range base.Nodes {
		baseNodes[node.Name] = node.Address
	}
	newNodes := map[string]string{}
	for _, node := range new.Nodes {
		newNodes[node.Name] = node.Address
	}
	if len(baseNodes) != len(newNodes) {
		return nil, fmt.Errorf("base invariant \"nodes\" changed: base has %d nodes, new config has %d", len(baseNodes), len(newNodes))
	}
	for name, address := range baseNodes {
		if newNodes[name] != address {
			return nil, fmt.Errorf("base invariant \"nodes\" changed: node %q address base=%q new=%q", name, address, newNodes[name])
		}
	}
	invariants["nodes"] = fmt.Sprint(len(baseNodes))
	return invariants, nil
}

// preflightLiveCluster performs the read-only live identity/health checks:
// Kubernetes version, per-component namespace UIDs and the StorageClass.
func preflightLiveCluster(ctx context.Context, runner kubectlRunner, cluster ClusterConfig, scope []string) (map[string]string, error) {
	live := map[string]string{}
	versionJSON, err := runner.run(ctx, "version", "-o", "json")
	if err != nil {
		return nil, errors.Wrap(err, "read the live cluster version")
	}
	var version struct {
		ServerVersion struct {
			GitVersion string `json:"gitVersion"`
		} `json:"serverVersion"`
	}
	if err := json.Unmarshal(versionJSON, &version); err != nil {
		return nil, errors.Wrap(err, "parse the live cluster version")
	}
	if version.ServerVersion.GitVersion != componentsKubeVersion {
		return nil, fmt.Errorf("the live cluster runs %s, but this installer's base is %s; stop — component additions never span versions", version.ServerVersion.GitVersion, componentsKubeVersion)
	}
	live["kubernetesVersion"] = version.ServerVersion.GitVersion

	namespaces := map[string]bool{}
	for _, component := range scope {
		spec := componentInstallSpecs[component]
		if namespaces[spec.Namespace] {
			continue
		}
		uid, err := runner.jsonpath(ctx, "namespace", spec.Namespace, "", "{.metadata.uid}")
		if err != nil || uid == "" {
			// The roles create their namespaces; a missing namespace is a
			// plan note, not a failure.
			live["namespace/"+spec.Namespace] = "will-be-created"
			continue
		}
		live["namespace/"+spec.Namespace] = "uid=" + uid
		namespaces[spec.Namespace] = true
	}

	class := effectiveStorageClass(cluster)
	if class != "" {
		if _, err := runner.jsonpath(ctx, "storageclass", class, "", "{.metadata.uid}"); err != nil {
			return nil, fmt.Errorf("the effective storage class %q does not exist in the live cluster: %w", class, err)
		}
		live["storageClass"] = class
	}
	return live, nil
}

// preflightComponentOwnership checks, per component, whether its primary
// object already exists and who owns it:
//   - helm components: the release secret decodes to our expected chart and
//     version → already_installed (never upgraded); anything else → reject;
//   - manifest components: an existing workload has no adoptable ownership →
//     reject (v1 has no --force/--adopt).
func preflightComponentOwnership(ctx context.Context, runner kubectlRunner, cluster ClusterConfig, scope []string) ([]ComponentsPlanComponent, error) {
	registry, err := cluster.RegistryAddress()
	if err != nil {
		return nil, err
	}
	results := make([]ComponentsPlanComponent, 0, len(scope))
	for _, component := range scope {
		spec := componentInstallSpecs[component]
		result := ComponentsPlanComponent{
			Component:    component,
			Namespace:    spec.Namespace,
			InternalDeps: spec.InternalDeps,
		}
		if spec.Release != "" {
			releaseJSON, err := helmReleaseChart(ctx, runner, spec.Namespace, spec.Release)
			if err != nil {
				return nil, err
			}
			if releaseJSON == nil {
				result.Status = "planned"
				result.Owner = "helm release " + spec.Release
			} else if releaseJSON.Name != spec.Chart || releaseJSON.Version != spec.ChartVersion {
				return nil, fmt.Errorf("component %q: the existing helm release %q carries chart %s-%s, expected %s-%s; adopting or upgrading a foreign release is not supported",
					component, spec.Release, releaseJSON.Name, releaseJSON.Version, spec.Chart, spec.ChartVersion)
			} else {
				result.Status = "already_installed"
				result.Owner = fmt.Sprintf("helm release %s (%s-%s, left untouched)", spec.Release, releaseJSON.Name, releaseJSON.Version)
			}
		} else {
			existing, err := runner.jsonpath(ctx, strings.ToLower(spec.WorkloadKind), spec.WorkloadName, spec.Namespace, "{.metadata.uid}")
			if err == nil && existing != "" {
				return nil, fmt.Errorf("component %q: %s %s/%s already exists and this installer has no ownership marker on it; adopting foreign workloads is not supported",
					component, strings.ToLower(spec.WorkloadKind), spec.Namespace, spec.WorkloadName)
			}
			result.Status = "planned"
			result.Owner = fmt.Sprintf("rendered manifest %s %s/%s", spec.WorkloadKind, spec.Namespace, spec.WorkloadName)
		}
		keys := imagesForComponent(cluster, component)
		result.Images = len(keys)
		charts := 0
		if spec.Chart != "" {
			charts = 1
		}
		result.Charts = charts
		_ = registry
		results = append(results, result)
	}
	return results, nil
}

// helmReleaseChart reads the helm release secret and decodes the chart
// metadata. nil (with nil error) means the release does not exist.
func helmReleaseChart(ctx context.Context, runner kubectlRunner, namespace, release string) (*struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}, error) {
	secretName := "sh.helm.release.v1." + release + ".v1"
	raw, err := runner.run(ctx, "get", "secret", secretName, "-n", namespace, "-o", "json")
	if err != nil {
		// Not found is the "planned" case; anything else is a preflight error.
		// kubectl spells it "(NotFound)", so compare without the space.
		if strings.Contains(strings.ToLower(fmt.Sprint(err)), "notfound") {
			return nil, nil
		}
		return nil, errors.Wrapf(err, "probe helm release %s/%s", namespace, release)
	}
	var secret struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(raw, &secret); err != nil {
		return nil, errors.Wrap(err, "parse the helm release secret")
	}
	// Helm writes Data["release"] as the bytes of base64(gzip(json)), and the
	// kubectl -o json output base64-encodes those bytes once more: the value
	// seen here is double base64 around the gzip stream (defect 7: the node1
	// re-plan of an installed release failed with "gzip: invalid header"
	// because only one layer was peeled).
	payload, err := base64.StdEncoding.DecodeString(secret.Data["release"])
	if err != nil {
		return nil, errors.Wrap(err, "decode the helm release secret")
	}
	inner, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(payload)))
	if err != nil {
		return nil, errors.Wrap(err, "decode the helm release payload (second layer)")
	}
	gz, err := gzip.NewReader(bytes.NewReader(inner))
	if err != nil {
		return nil, errors.Wrap(err, "open the helm release payload")
	}
	var decoded struct {
		Chart struct {
			Metadata struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"metadata"`
		} `json:"chart"`
	}
	if err := json.NewDecoder(gz).Decode(&decoded); err != nil {
		return nil, errors.Wrap(err, "parse the helm release metadata")
	}
	return &struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}{Name: decoded.Chart.Metadata.Name, Version: decoded.Chart.Metadata.Version}, nil
}

// imagesForComponent filters the declared image keys down to the ones the
// named component's install actually renders.
func imagesForComponent(cluster ClusterConfig, component string) []ImageKey {
	var keys []ImageKey
	for _, key := range componentImageKeysForRun(cluster) {
		switch {
		case component == "cert-manager" && key.Group == "verification":
			keys = append(keys, key)
		case component == "metrics" && key.Group == "metrics":
			keys = append(keys, key)
		case key.Group == "components" && key.Name == component:
			keys = append(keys, key)
		case key.Group == "logs" && key.Backend == component:
			keys = append(keys, key)
		}
	}
	return keys
}

// preflightComponentImages checks that every image the scope renders is
// already served by the original registry — a components run never restarts
// the registry and never downloads materials (R15 step 7).
func preflightComponentImages(ctx context.Context, cluster ClusterConfig, scope []string) error {
	registry, err := cluster.RegistryAddress()
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	for _, component := range scope {
		for _, key := range imagesForComponent(cluster, component) {
			ref := key.Original
			parts := strings.SplitN(strings.TrimPrefix(ref, ""), "/", 2)
			if len(parts) != 2 {
				return fmt.Errorf("image reference %q is not in repository form", ref)
			}
			repo := parts[1]
			tag := ""
			if idx := strings.LastIndex(repo, ":"); idx >= 0 {
				repo, tag = repo[:idx], repo[idx+1:]
			}
			url := fmt.Sprintf("http://%s/v2/%s/manifests/%s", registry, repo, tag)
			req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
			if err != nil {
				return err
			}
			req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.index.v1+json")
			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("probe %s from the registry: %w", key.Original, err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("image %s is missing from the registry %s (HTTP %d); prepare that batch's base per the blueprint — a components run never downloads materials", key.Original, registry, resp.StatusCode)
			}
		}
	}
	return nil
}

func writeComponentsPlan(dir, runID string, plan *ComponentsPlan) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.Wrapf(err, "create the components plan directory %s", dir)
	}
	encoded, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return errors.Wrap(err, "encode the components plan")
	}
	encoded = append(encoded, '\n')
	path := filepath.Join(dir, fmt.Sprintf("components-plan-%s.json", runID))
	return os.WriteFile(path, encoded, 0o644)
}

// ---------------------------------------------------------------------------
// R15.2: execution of the planned components. The executor re-checks the run
// identity, renders the components-scoped config (.ani.components_run.scope),
// invokes the standalone playbook and writes the new run's connection facts.
// already_installed components are never executed again.
// ---------------------------------------------------------------------------

// ComponentsExecuteInput is the input of `kk ani components execute`.
type ComponentsExecuteInput struct {
	// PlanFile is the plan the R15.1 card wrote; its scope and statuses drive
	// the execution.
	PlanFile    string
	ConfigFile  string
	PackageRoot string
	Kubeconfig  string
	Output      string
	// KKBin is the kk binary that runs the components playbook. Empty means
	// "kk" from PATH.
	KKBin string
	// ProjectAddr optionally points at the local playbook project root (the
	// directory containing builtin/). Empty relies on the binary's embedded
	// builtin project.
	ProjectAddr string
}

// ComponentsExecuteReport is the machine-readable record of one execution.
type ComponentsExecuteReport struct {
	SchemaVersion   int                      `json:"schemaVersion"`
	RunID           string                   `json:"runId"`
	ClusterName     string                   `json:"clusterName"`
	Results         []ComponentsPlanComponent `json:"results"`
	ConnectionsFile string                   `json:"connectionsFile,omitempty"`
	StartedAt       string                   `json:"startedAt"`
	FinishedAt      string                   `json:"finishedAt"`
	Overall         string                   `json:"overall"`
}

// RunComponentsExecute executes the planned scope of an R15.1 plan. The
// identity is re-checked (the config must still be the one the plan was built
// from), already_installed components stay read-only, and only the planned
// components are handed to the standalone playbook.
func RunComponentsExecute(ctx context.Context, input ComponentsExecuteInput, stdout io.Writer) error {
	planData, err := os.ReadFile(strings.TrimSpace(input.PlanFile))
	if err != nil {
		return errors.Wrapf(err, "read the components plan %s", input.PlanFile)
	}
	var plan ComponentsPlan
	if err := json.Unmarshal(planData, &plan); err != nil {
		return errors.Wrapf(err, "parse the components plan %s", input.PlanFile)
	}
	cluster, manifest, err := loadComponentsInstallConfig(ComponentsInstallInput{
		ConfigFile:  input.ConfigFile,
		PackageRoot: input.PackageRoot,
	})
	if err != nil {
		return err
	}
	if manifest.ConfigDigest != plan.NewConfigDigest {
		return fmt.Errorf("the site config digest %s does not match the plan's %s; re-plan before executing", manifest.ConfigDigest, plan.NewConfigDigest)
	}
	if manifest.ClusterName != plan.ClusterName {
		return fmt.Errorf("the site config belongs to cluster %q, the plan to %q", manifest.ClusterName, plan.ClusterName)
	}

	var executed, already, notRun []string
	scopeMap := map[string]any{}
	for _, component := range plan.Components {
		switch component.Status {
		case "planned":
			scopeMap[component.Component] = true
			executed = append(executed, component.Component)
		case "already_installed":
			already = append(already, component.Component)
		default:
			notRun = append(notRun, component.Component)
		}
	}
	if len(executed) == 0 {
		return errors.New("the plan has no planned components (only already_installed or skipped); there is nothing to execute")
	}

	// The components run gets its own runtime root, so the base install's
	// work tree, logs and connections document are never overwritten.
	root := filepath.Join(strings.TrimSpace(input.Output), "components-"+plan.RunID)
	workRoot := filepath.Join(root, "work")
	logRoot := filepath.Join(root, "logs")
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		return errors.Wrapf(err, "create the components work root %s", workRoot)
	}
	if err := os.MkdirAll(logRoot, 0o700); err != nil {
		return errors.Wrapf(err, "create the components log root %s", logRoot)
	}

	imageTable, err := LoadImageTable(strings.Split(readImageTableOrEmpty(input.PackageRoot), "\n"))
	if err != nil {
		return err
	}
	spec, err := KubeKeyConfig(*cluster, filepath.Join(input.PackageRoot, "packages", "kubekey-artifact.tgz"), input.PackageRoot, imageTable)
	if err != nil {
		return err
	}
	aniSpec := spec["ani"].(map[string]any)
	aniSpec["components_run"] = map[string]any{"scope": scopeMap}
	inventory, err := KubeKeyInventory(*cluster)
	if err != nil {
		return err
	}
	if err := writeYAML(filepath.Join(workRoot, "config.yaml"), map[string]any{
		"apiVersion": "kubekey.kubesphere.io/v1",
		"kind":       "Config",
		"spec":       spec,
	}); err != nil {
		return err
	}
	if err := writeYAML(filepath.Join(workRoot, "inventory.yaml"), map[string]any{
		"apiVersion": "kubekey.kubesphere.io/v1",
		"kind":       "Inventory",
		"metadata":   map[string]any{"name": "default"},
		"spec":       inventory,
	}); err != nil {
		return err
	}

	logFile, err := os.OpenFile(filepath.Join(logRoot, "components-install.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.Wrap(err, "open the components install log")
	}
	defer logFile.Close()
	fmt.Fprintf(logFile, "ANI components run %s started at %s; scope=%s already_installed=%s\n",
		plan.RunID, time.Now().Format(time.RFC3339), strings.Join(executed, ","), strings.Join(already, ","))

	report := ComponentsExecuteReport{
		SchemaVersion: 1,
		RunID:         plan.RunID,
		ClusterName:   plan.ClusterName,
		StartedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	// The one kk invocation: the standalone components playbook with the
	// scope-scoped rendered config. The playbook itself gates every role on
	// the scope map, so the base roles can never leak in.
	//
	// R15.3: `kk run` resolves a playbook only from a local project directory
	// (pkg/project/local.go), and a code release carries no builtin/ tree —
	// so the executor must hand it a resolvable project. An explicit
	// --project-addr wins; otherwise this binary materializes the project
	// from its own embedded builtin tree (matching the binary that planned
	// the run, byte for byte). A build without that ability refuses before
	// invoking kk, instead of repeating the node1 failure (zero tasks,
	// cannot find playbook).
	projectAddr := strings.TrimSpace(input.ProjectAddr)
	if projectAddr == "" {
		if componentsProjectMaterialize == nil {
			return errors.New("cannot resolve the components playbook project: this build carries no embedded builtin project and a code release has no builtin/ directory; rebuild with -tags builtin or pass --project-addr for a project containing " + componentsPlaybookRunPath)
		}
		projectAddr = filepath.Join(root, "project")
		if err := componentsProjectMaterialize(projectAddr); err != nil {
			return errors.Wrap(err, "materialize the components project from the embedded builtin tree")
		}
	}
	kk := strings.TrimSpace(input.KKBin)
	if kk == "" {
		kk = "kk"
	}
	args := []string{"run", componentsPlaybookRunPath,
		"-a", filepath.Join(input.PackageRoot, "packages", "kubekey-artifact.tgz"),
		"-c", filepath.Join(workRoot, "config.yaml"),
		"-i", filepath.Join(workRoot, "inventory.yaml"),
		"--workdir", root,
		"--project-addr", projectAddr,
	}
	cmd := exec.CommandContext(ctx, kk, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(logFile, "components run failed: %v\n", err)
		// A failed run keeps its log and material directories; nothing is
		// cleaned up or replayed (R15 step 7 / R14 boundary).
		report.Overall = VerifyStatusFailed
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		_ = writeComponentsExecuteReport(input.Output, plan.RunID, report)
		return errors.Wrap(err, "components playbook execution failed")
	}

	// The new run writes its own connection facts next to its runtime root;
	// the base install's connections document is never touched.
	//
	// R15.3 defect 6: the component roles render their fragments into the
	// canonical per-cluster runtime root (the base installer's contract,
	// hardcoded in every role as /var/lib/ani-installer/<cluster>/work/
	// connections.d), not into this run's --workdir. Read from that contract
	// location; connections.md itself still belongs to this run alone.
	if plan.ClusterName == "" || plan.ClusterName == "." || plan.ClusterName == ".." ||
		strings.ContainsAny(plan.ClusterName, `/\`) {
		return fmt.Errorf("cluster name %q cannot form a safe runtime path", plan.ClusterName)
	}
	fragmentRoot := filepath.Join(runtimeBaseDir, plan.ClusterName, "work")
	rows := []ComponentRow{}
	for _, row := range effectiveSelection(*cluster) {
		for _, component := range executed {
			if row.Name == component {
				rows = append(rows, row)
			}
		}
	}
	connectionsFile := filepath.Join(root, "connections.md")
	if err := writeConnections(connectionsFile, fragmentRoot, rows); err != nil {
		return err
	}
	for _, component := range executed {
		report.Results = append(report.Results, r15PlanComponent(component, "executed", ""))
	}
	for _, component := range already {
		report.Results = append(report.Results, r15PlanComponent(component, "already_installed",
			"read-only: the same-version release was not executed again"))
	}
	for _, component := range notRun {
		report.Results = append(report.Results, r15PlanComponent(component, VerifyStatusNotRun, ""))
	}
	report.ConnectionsFile = connectionsFile
	report.Overall = VerifyStatusPass
	report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	if err := writeComponentsExecuteReport(input.Output, plan.RunID, report); err != nil {
		return err
	}

	out := stdout
	if out == nil {
		out = os.Stdout
	}
	for _, result := range report.Results {
		fmt.Fprintf(out, "components execute %s: %s\n", result.Component, result.Status)
	}
	fmt.Fprintf(out, "components execute overall: %s (run=%s connections=%s)\n", report.Overall, plan.RunID, connectionsFile)
	return nil
}

func readImageTableOrEmpty(packageRoot string) string {
	data, err := os.ReadFile(filepath.Join(packageRoot, "images", "images.tsv"))
	if err != nil {
		return ""
	}
	return string(data)
}

func writeComponentsExecuteReport(dir, runID string, report ComponentsExecuteReport) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.Wrapf(err, "create the components report directory %s", dir)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return errors.Wrap(err, "encode the components execution report")
	}
	encoded = append(encoded, '\n')
	path := filepath.Join(dir, fmt.Sprintf("components-execute-%s.json", runID))
	return os.WriteFile(path, encoded, 0o644)
}

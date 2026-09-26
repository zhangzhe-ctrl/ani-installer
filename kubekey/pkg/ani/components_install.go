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
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/kubesphere/kubekey/v4/version"
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
	"cert-manager": {Namespace: "cert-manager", Release: "cert-manager", Chart: "cert-manager", ChartVersion: "v1.21.2", WorkloadKind: "Deployment", WorkloadName: "cert-manager"},
	"postgresql":   {Namespace: "ani-platform", WorkloadKind: "StatefulSet", WorkloadName: "postgresql", NeedsStorage: true},
	"valkey":       {Namespace: "ani-platform", WorkloadKind: "StatefulSet", WorkloadName: "valkey", NeedsStorage: true},
	"nats":         {Namespace: "ani-platform", Release: "nats", Chart: "nats", ChartVersion: "2.14.6", WorkloadKind: "StatefulSet", WorkloadName: "nats", NeedsStorage: true},
	"metrics":      {Namespace: "ani-observability", Release: "ani-metrics", Chart: "kube-prometheus-stack", ChartVersion: "85.4.0", WorkloadKind: "StatefulSet", WorkloadName: "prometheus-ani-metrics-prometheus", NeedsStorage: true},
	"loki":         {Namespace: "ani-observability", Release: "ani-loki", Chart: "loki", ChartVersion: "18.13.3", WorkloadKind: "StatefulSet", WorkloadName: "ani-loki", NeedsStorage: true},
	"opensearch":   {Namespace: "ani-observability", Release: "ani-opensearch-master", Chart: "opensearch", ChartVersion: "3.8.0", WorkloadKind: "StatefulSet", WorkloadName: "ani-opensearch-master", NeedsStorage: true, InternalDeps: []string{"fluent-bit"}},
	"fluent-bit":   {Namespace: "ani-observability", Release: "ani-fluent-bit", Chart: "fluent-bit", ChartVersion: "0.58.2", WorkloadKind: "DaemonSet", WorkloadName: "ani-fluent-bit"},
}

// ComponentsInstallInput is the input of `kk ani components install`.
type ComponentsInstallInput struct {
	ConfigFile  string
	PackageRoot string
	Only        []string
	// BaseRunFile is the original base install's run.json — an INSTALL-SUCCESS
	// record produced by `kk ani install` (a config-validation record is
	// refused). The baseline invariants (cluster identity, nodes, main CNI,
	// registry identity) are compared against it AND against the live cluster.
	// The new and old config digests are recorded separately — adding
	// components necessarily changes the digest.
	BaseRunFile string
	// StateFile optionally points at the base install's run-state.json beside
	// BaseRunFile; the success record and its state must agree.
	StateFile  string
	Kubeconfig string
	Output     string
	// KKBin optionally pins the playbook-runner binary the plan binds its
	// identity to (execute defaults to this executable; the plan records the
	// same default or the pinned one).
	KKBin string
	// PlanKKDigestOverride is test-only: it records the digest of a fixture
	// playbook runner (a fake kk) without changing production behaviour.
	PlanKKDigestOverride string
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
	SchemaVersion    int    `json:"schemaVersion"`
	RunID            string `json:"runId"`
	ClusterName      string `json:"clusterName"`
	BaseConfigDigest string `json:"baseConfigDigest"`
	NewConfigDigest  string `json:"newConfigDigest"`
	// Identity binding (F02): execute must be run by the same binary against
	// the same cluster the plan inspected. The plan records what IT observed;
	// execute re-observes and compares. None of this is a signature — it stops
	// honest drift (new plan needed after code/material/cluster changes), and
	// the shared product lock stops concurrent installers.
	KKBinaryDigest string `json:"kkBinaryDigest"`
	CodeCommit     string `json:"codeCommit"`
	BaseClusterUID string `json:"baseClusterUid"`
	// BaseRunFile records the exact success record the plan was built against;
	// execute must load the same file and reach the same digests.
	BaseRunFile string                    `json:"baseRunFile,omitempty"`
	Invariants  map[string]string         `json:"invariants"`
	Components  []ComponentsPlanComponent `json:"components"`
	Excluded    []string                  `json:"excluded"`
	LiveChecks  map[string]string         `json:"liveChecks"`
	StartedAt   string                    `json:"startedAt"`
	FinishedAt  string                    `json:"finishedAt"`
	Overall     string                    `json:"overall"`
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
	baseManifest, err := loadBaseRunRecord(input.BaseRunFile, input.StateFile)
	if err != nil {
		return err
	}
	invariants, err := compareBaseInvariants(baseManifest, *manifest)
	if err != nil {
		return err
	}

	runner := kubectlRunner{bin: kubectlBin(), kubeconfig: input.kubeconfigOrDefault()}
	// F02/F03: the LIVE cluster must be the base cluster — kube-system UID and
	// the full Ready node set compared against the install-success record's
	// captured identity, not merely "the config files match each other".
	liveID, notReady, err := captureLiveCluster(ctx, runner)
	if err != nil {
		return err
	}
	if err := requireBaseIdentity(baseManifest, liveID, notReady); err != nil {
		return err
	}
	live, err := preflightLiveCluster(ctx, runner, cluster, *manifest, scope)
	if err != nil {
		return err
	}
	planComponents, err := preflightComponentOwnership(ctx, runner, cluster, scope)
	if err != nil {
		return err
	}
	artifactLock, err := artifactMaterialsLock(input.PackageRoot)
	if err != nil {
		return err
	}
	if err := preflightComponentImages(ctx, cluster, input.PackageRoot, artifactLock, scope, stdout); err != nil {
		return err
	}

	// All checks passed: only now are any files written, and the first write is
	// the run's own directory claim. A components run id is a second-granular
	// timestamp, so two consecutive additions (or a script that plans twice in
	// one second) can derive the same id; the exclusive mkdir below is what
	// proves the id is free, and it bumps to -2, -3, ... until one is claimed.
	// Every artifact of a run — plan, new run record, runtime root, report — is
	// named by that id, so no run ever overwrites another's (T43).
	outDir := strings.TrimSpace(input.Output)
	if outDir == "" {
		outDir = "/var/lib/ani-installer/components"
	}
	runID, runRecordDir, err := claimComponentsRunDir(outDir, manifest.ClusterName)
	if err != nil {
		return err
	}
	// The playbook-runner identity: THIS executable unless the operator pinned
	// a different --kk; the plan records whichever binary will actually run,
	// and execute refuses a child that differs from it (F02).
	kkSelf, err := os.Executable()
	if err != nil {
		return errors.Wrap(err, "locate the running kk for plan identity binding")
	}
	playerBin := strings.TrimSpace(input.KKBin)
	if playerBin == "" {
		playerBin = kkSelf
	}
	if input.PlanKKDigestOverride != "" && !isHex64(input.PlanKKDigestOverride) {
		return errors.New("PlanKKDigestOverride must be 64-hex (test-only)")
	}
	playerDigest := input.PlanKKDigestOverride
	if playerDigest == "" {
		playerDigest = fileSHA256Hex(playerBin)
	}
	absBaseRun, err := filepath.Abs(input.BaseRunFile)
	if err != nil {
		return err
	}
	plan := &ComponentsPlan{
		SchemaVersion:    1,
		RunID:            runID,
		ClusterName:      manifest.ClusterName,
		BaseConfigDigest: baseManifest.ConfigDigest,
		NewConfigDigest:  manifest.ConfigDigest,
		KKBinaryDigest:   playerDigest,
		CodeCommit:       version.Get().GitCommit,
		BaseClusterUID:   liveID.ClusterUID,
		BaseRunFile:      absBaseRun,
		Invariants:       invariants,
		Components:       planComponents,
		Excluded:         componentsPlanExcluded,
		LiveChecks:       live,
		StartedAt:        time.Now().UTC().Format(time.RFC3339),
		Overall:          VerifyStatusPass,
	}
	plan.FinishedAt = time.Now().UTC().Format(time.RFC3339)

	// Everything below is a write, and it can only happen after every check above
	// passed: a rejected plan leaves no output behind.
	if err := writeComponentsPlan(outDir, runID, plan); err != nil {
		return err
	}
	// The new run record lives in this run's own directory, so the record of a
	// previous addition (and the base install's record) is never overwritten.
	if err := WriteRunOutputs(runRecordDir, *manifest); err != nil {
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
		filepath.Join(runRecordDir, RunManifestFileName))
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

// loadBaseRunRecord reads the original base install's run.json and enforces
// the F04 success contract on it: only an install-success record generated by
// a completed first install (never a validate artifact, never a failed or
// half-written one) can anchor a component addition.
func loadBaseRunRecord(path, statePath string) (RunManifest, error) {
	if strings.TrimSpace(path) == "" {
		return RunManifest{}, errors.New("components install needs --base-run pointing at the install-success run.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return RunManifest{}, errors.Wrapf(err, "read the base run record %s", path)
	}
	var manifest RunManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return RunManifest{}, errors.Wrapf(err, "parse the base run record %s", path)
	}
	var state *InstallState
	resolvedState := strings.TrimSpace(statePath)
	if resolvedState == "" {
		candidate := filepath.Join(filepath.Dir(path), "run-state.json")
		if _, err := os.Stat(candidate); err == nil {
			resolvedState = candidate
		}
	}
	if resolvedState != "" {
		st, err := ReadRunState(resolvedState)
		if err != nil {
			return RunManifest{}, errors.Wrapf(err, "read the base install state %s", resolvedState)
		}
		state = st
	}
	if err := ValidateSuccessRecord(manifest, state); err != nil {
		return RunManifest{}, errors.Wrapf(err, "base run %s", path)
	}
	return manifest, nil
}

// notFoundErr reports whether a kubectl API read failed because the object is
// genuinely absent. Forbidden, timeouts, and unreadable output are NOT NotFound
// and must never be mapped onto "create it" (T21).
func notFoundErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "notfound") || strings.Contains(s, "not found")
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

// captureLiveCluster reads the stable identity and health of the LIVE cluster:
// the kube-system namespace UID (cluster fingerprint) and every node with its
// Ready condition. Config equality proves nothing about which cluster is
// standing there (T12); a NotReady node set proves nothing about health (T13).
func captureLiveCluster(ctx context.Context, runner kubectlRunner) (ManifestIdentity, []string, error) {
	var id ManifestIdentity
	uid, err := runner.jsonpath(ctx, "namespace", "kube-system", "", "{.metadata.uid}")
	if err != nil {
		return id, nil, errors.Wrapf(err, "read the live cluster fingerprint (kube-system namespace uid)")
	}
	if strings.TrimSpace(uid) == "" {
		return id, nil, errors.New("the live kube-system namespace uid came back empty")
	}
	id.ClusterUID = strings.TrimSpace(uid)
	nodesJSON, err := runner.run(ctx, "get", "nodes", "-o", "json")
	if err != nil {
		return id, nil, errors.Wrap(err, "read the live node set")
	}
	var nodes struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return id, nil, errors.Wrap(err, "parse the live node set")
	}
	if len(nodes.Items) == 0 {
		return id, nil, errors.New("the live cluster reports zero nodes")
	}
	var notReady []string
	for _, node := range nodes.Items {
		ready := false
		for _, cond := range node.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == "True" {
				ready = true
			}
		}
		if ready {
			id.ReadyNodes = append(id.ReadyNodes, node.Metadata.Name)
		} else {
			notReady = append(notReady, node.Metadata.Name)
		}
	}
	sort.Strings(id.ReadyNodes)
	id.NodeCount = len(nodes.Items)
	return id, notReady, nil
}

// requireBaseIdentity compares the live cluster with the base install-success
// record's captured identity: same cluster fingerprint, same node set, all
// nodes Ready (T12/T13).
func requireBaseIdentity(base RunManifest, liveID ManifestIdentity, notReady []string) error {
	if base.Identity.ClusterUID == "" {
		return errors.New("the base success record carries no cluster uid; re-run the first install to produce a complete record")
	}
	if liveID.ClusterUID != base.Identity.ClusterUID {
		return fmt.Errorf("the live cluster is %s but the base run record was captured against %s; same config on a different cluster is NOT the same base — refusing", liveID.ClusterUID, base.Identity.ClusterUID)
	}
	if len(notReady) > 0 {
		return fmt.Errorf("nodes %v are NotReady; component additions require the full base node set healthy", notReady)
	}
	baseReady := append([]string(nil), base.Identity.ReadyNodes...)
	sort.Strings(baseReady)
	if strings.Join(baseReady, ",") != strings.Join(liveID.ReadyNodes, ",") {
		return fmt.Errorf("the live Ready node set %v differs from the base %v; re-plan after the topology matches the base again", liveID.ReadyNodes, baseReady)
	}
	return nil
}

// preflightLiveCluster performs the read-only live checks that must hold for
// the scoped additions: Kubernetes version, per-namespace existence WITHOUT
// conflating Forbidden with NotFound (T21), and the StorageClass each PVC will
// ACTUALLY bind to — per-component class overrides included (T41).
func preflightLiveCluster(ctx context.Context, runner kubectlRunner, cluster ClusterConfig, manifest RunManifest, scope []string) (map[string]string, error) {
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

	// The base's own data path has to be healthy before anything is added to
	// it: nodes can report Ready while the CNI DaemonSet is only half-deployed,
	// and every new Pod would then be stuck without an IP.
	if err := checkCNIHealth(ctx, runner, cluster, live); err != nil {
		return nil, err
	}

	namespaces := map[string]bool{}
	for _, component := range scope {
		spec := componentInstallSpecs[component]
		if namespaces[spec.Namespace] {
			continue
		}
		uid, err := runner.jsonpath(ctx, "namespace", spec.Namespace, "", "{.metadata.uid}")
		if err != nil {
			if notFoundErr(err) {
				live["namespace/"+spec.Namespace] = "will-be-created"
				continue
			}
			return nil, errors.Wrapf(err, "probe namespace %s (Forbidden or unreachable is not NotFound; refusing to plan a blind create)", spec.Namespace)
		}
		if strings.TrimSpace(uid) != "" {
			live["namespace/"+spec.Namespace] = "uid=" + strings.TrimSpace(uid)
			namespaces[spec.Namespace] = true
		} else {
			live["namespace/"+spec.Namespace] = "will-be-created"
		}
	}

	// Storage: check the class the actual PVC will use, not just the global.
	classes := map[string]bool{}
	for _, component := range scope {
		if !componentInstallSpecs[component].NeedsStorage {
			continue
		}
		class := ""
		if c := cluster.Components.storage(component); c != nil {
			class = strings.TrimSpace(c.StorageClass)
		}
		if class == "" {
			class = manifest.StorageClass
		}
		if class == "" {
			return nil, fmt.Errorf("component %q needs a PVC but neither it nor the site names a storage class", component)
		}
		if classes[class] {
			continue
		}
		classes[class] = true
		if _, err := runner.jsonpath(ctx, "storageclass", class, "", "{.metadata.uid}"); err != nil {
			return nil, fmt.Errorf("the storage class %q that %q would bind does not exist in the live cluster (or is unreadable): %w", class, component, err)
		}
	}
	liveClasses := make([]string, 0, len(classes))
	for class := range classes {
		liveClasses = append(liveClasses, class)
	}
	sort.Strings(liveClasses)
	if len(liveClasses) > 0 {
		live["storageClasses"] = strings.Join(liveClasses, ",")
	}
	return live, nil
}

// aniManagedByLabel marks every resource THIS installer owns. Its value is the
// cluster name. Roles apply it (helm --labels / manifest labels); ownership is
// never inferred from a name or chart coincidence.
const aniManagedByLabel = "ani.io/managed-by"

// helmReleaseState is the CURRENT state of one helm release, read from the
// release secrets: the highest revision wins — its status and chart identity —
// never revision 1 (F08).
type helmReleaseState struct {
	revision int
	status   string
	chart    string
	version  string
	labels   map[string]string
}

// preflightComponentOwnership checks, per component, whether its primary
// object already exists, WHO owns it, and in WHAT current state:
//   - helm: the highest release revision must be deployed, carry the expected
//     chart/version, AND the ANI ownership label to be already_installed; a
//     failed/pending current revision or a foreign owner is refused — this
//     installer never upgrades or adopts (T15/T19/T20/T21);
//   - manifest: an existing workload with the ANI label is already_installed;
//     without the marker it is foreign and refused;
//   - only a definitive NotFound produces a "planned" row; Forbidden or an
//     unreadable probe refuses the plan.
//
// cniWorkload names one DaemonSet that carries part of the cluster's primary
// CNI data path.
type cniWorkload struct{ namespace, name string }

// cniHealthProbes returns the workloads that must be fully ready for the
// cluster's own network stack. The list is per stack, never inferred from what
// happens to exist in the cluster: an absent CNI DaemonSet is a refusal, not a
// skipped check.
func cniHealthProbes(stack string) ([]cniWorkload, error) {
	switch networkStack(stack) {
	case "kcn":
		return []cniWorkload{
			{"kcn-system", "kcn-cni-ds"},
			{"kcn-system", "kcn-ovs-ds"},
		}, nil
	case "kubeovn":
		return []cniWorkload{
			{"kube-system", "kube-ovn-cni"},
			{"kube-system", "ovs-ovn"},
		}, nil
	}
	return nil, fmt.Errorf("network stack %q has no known CNI workloads to health-check; this installer supports kcn and kubeovn only", stack)
}

// checkCNIHealth reads the desired/ready counts of the stack's CNI DaemonSets
// and records them as live facts. Any read error (missing, Forbidden,
// unreachable, unparsable) is a refusal: a components run that lands on a
// cluster whose data path is half-deployed would only create Pods that can
// never get an IP.
func checkCNIHealth(ctx context.Context, runner kubectlRunner, cluster ClusterConfig, live map[string]string) error {
	probes, err := cniHealthProbes(cluster.Network.Stack)
	if err != nil {
		return err
	}
	for _, workload := range probes {
		raw, err := runner.jsonpath(ctx, "daemonset", workload.name, workload.namespace,
			"{.status.desiredNumberScheduled} {.status.numberReady}")
		if err != nil {
			return errors.Wrapf(err, "probe CNI workload %s/%s (absent, Forbidden or unreachable is not healthy)",
				workload.namespace, workload.name)
		}
		fields := strings.Fields(raw)
		if len(fields) != 2 {
			return fmt.Errorf("CNI workload %s/%s reported %q instead of a desired/ready pair; the cluster's network stack is not verifiably healthy",
				workload.namespace, workload.name, raw)
		}
		desired, derr := strconv.Atoi(fields[0])
		ready, rerr := strconv.Atoi(fields[1])
		if derr != nil || rerr != nil {
			return fmt.Errorf("CNI workload %s/%s reported %q which is not a desired/ready pair: the network stack health cannot be established",
				workload.namespace, workload.name, raw)
		}
		if desired <= 0 {
			return fmt.Errorf("CNI workload %s/%s schedules %d pods: the cluster's network stack is not deployed, so nothing may be added to it",
				workload.namespace, workload.name, desired)
		}
		if ready < desired {
			return fmt.Errorf("CNI workload %s/%s has %d/%d pods ready: the cluster's own network data path is unhealthy and a components run would schedule Pods that can never get an IP",
				workload.namespace, workload.name, ready, desired)
		}
		live["cni/"+workload.namespace+"/"+workload.name] = fmt.Sprintf("%d/%d ready", ready, desired)
	}
	return nil
}

func preflightComponentOwnership(ctx context.Context, runner kubectlRunner, cluster ClusterConfig, scope []string) ([]ComponentsPlanComponent, error) {
	results := make([]ComponentsPlanComponent, 0, len(scope))
	for _, component := range scope {
		spec := componentInstallSpecs[component]
		result := ComponentsPlanComponent{
			Component:    component,
			Namespace:    spec.Namespace,
			InternalDeps: spec.InternalDeps,
		}
		if spec.Release != "" {
			rel, err := helmCurrentRelease(ctx, runner, spec.Namespace, spec.Release)
			if err != nil {
				if notFoundErr(err) {
					result.Status = "planned"
					result.Owner = "helm release " + spec.Release
				} else {
					return nil, errors.Wrapf(err, "probe current helm release %s/%s (unreachable or Forbidden is not NotFound)", spec.Namespace, spec.Release)
				}
			} else if rel.status != "deployed" {
				return nil, fmt.Errorf("component %q: helm release %s/%s is %q at revision %d; a failed/pending release must be resolved by an operator, this installer never upgrades or force-adopts",
					component, spec.Namespace, spec.Release, rel.status, rel.revision)
			} else if rel.chart != spec.Chart || rel.version != spec.ChartVersion {
				return nil, fmt.Errorf("component %q: current release revision %d is deployed from %s-%s, expected %s-%s; adopting or upgrading a foreign release is not supported",
					component, rel.revision, rel.chart, rel.version, spec.Chart, spec.ChartVersion)
			} else if rel.labels[aniManagedByLabel] != cluster.Name {
				return nil, fmt.Errorf("component %q: release %s/%s matches the chart but carries no %s=%s marker; it is not this installer's object and will not be treated as installed or upgraded",
					component, spec.Namespace, spec.Release, aniManagedByLabel, cluster.Name)
			} else {
				result.Status = "already_installed"
				result.Owner = fmt.Sprintf("helm release %s rev%d (%s-%s, ANI-owned, left untouched)", spec.Release, rel.revision, rel.chart, rel.version)
			}
		} else {
			kind := strings.ToLower(spec.WorkloadKind)
			uid, err := runner.jsonpath(ctx, kind, spec.WorkloadName, spec.Namespace, "{.metadata.uid}")
			if err != nil {
				if !notFoundErr(err) {
					return nil, errors.Wrapf(err, "probe %s %s/%s (Forbidden or unreachable is not absence)", kind, spec.Namespace, spec.WorkloadName)
				}
				uid = ""
			}
			if strings.TrimSpace(uid) != "" {
				owner, lerr := runner.jsonpath(ctx, kind, spec.WorkloadName, spec.Namespace,
					`{.metadata.labels.ani\.io/managed-by}`)
				if lerr != nil || strings.TrimSpace(owner) != cluster.Name {
					return nil, fmt.Errorf("component %q: %s %s/%s already exists and this installer has no ownership marker on it (read %q err %v); adopting foreign workloads is not supported",
						component, kind, spec.Namespace, spec.WorkloadName, owner, lerr)
				}
				result.Status = "already_installed"
				result.Owner = fmt.Sprintf("rendered manifest %s %s/%s (ANI-owned, left untouched)", spec.WorkloadKind, spec.Namespace, spec.WorkloadName)
			} else {
				result.Status = "planned"
				result.Owner = fmt.Sprintf("rendered manifest %s %s/%s", spec.WorkloadKind, spec.Namespace, spec.WorkloadName)
			}
		}
		keys := imagesForComponent(cluster, component)
		result.Images = len(keys)
		charts := 0
		if spec.Chart != "" {
			charts = 1
		}
		result.Charts = charts
		results = append(results, result)
	}
	return results, nil
}

// helmCurrentRelease reads the CURRENT helm release state: ALL release secret
// revisions for the release, and the highest revision's status, chart identity,
// and labels (F08: fixed `.v1` is wrong when v1 was pruned or a newer
// revision exists). "not found" is reported as an error whose text marks it
// NotFound so callers can distinguish it from Forbidden.
func helmCurrentRelease(ctx context.Context, runner kubectlRunner, namespace, release string) (*helmReleaseState, error) {
	raw, err := runner.run(ctx, "get", "secret", "-n", namespace, "-l", "owner=helm,name="+release, "-o", "json")
	if err != nil {
		return nil, errors.Wrapf(err, "list helm release secrets for %s/%s", namespace, release)
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Data map[string]string `json:"data"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, errors.Wrap(err, "parse helm release secret list")
	}
	if len(list.Items) == 0 {
		return nil, fmt.Errorf("helm release %s/%s not found", namespace, release)
	}
	best := -1
	bestIndex := -1
	for i := range list.Items {
		rev := 0
		if v, found := list.Items[i].Metadata.Labels["version"]; found {
			rev, _ = strconv.Atoi(v)
		}
		if rev > best {
			best = rev
			bestIndex = i
		}
	}
	if bestIndex < 0 {
		return nil, fmt.Errorf("helm release %s/%s has no readable revision", namespace, release)
	}
	item := list.Items[bestIndex]
	state := &helmReleaseState{
		revision: best,
		status:   item.Metadata.Labels["status"],
		labels:   item.Metadata.Labels,
	}
	// Helm writes Data["release"] as the bytes of base64(gzip(json)), and the
	// kubectl -o json output base64-encodes those bytes once more: the value
	// seen here is double base64 around the gzip stream (defect 7: the node1
	// re-plan of an installed release failed with "gzip: invalid header"
	// because only one layer was peeled).
	payload, err := base64.StdEncoding.DecodeString(item.Data["release"])
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
	state.chart = decoded.Chart.Metadata.Name
	state.version = decoded.Chart.Metadata.Version
	return state, nil
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

// artifactMaterialsLock reads the approved materials lock the artifact ships.
// Both entries of the image gate need it: an artifact whose lock cannot be read
// proves nothing about what its images were approved to be (F06).
func artifactMaterialsLock(packageRoot string) (*MaterialsLock, error) {
	path := filepath.Join(packageRoot, "config", "components.lock.yaml")
	lock, err := LoadMaterialsLock(path)
	if err != nil {
		return nil, errors.Wrapf(err, "read the approved materials lock %s of this artifact", path)
	}
	return lock, nil
}

// preflightComponentImages runs the SAME content gate the first install runs
// (F06): every image a scoped component needs is looked up in the artifact's
// own images.tsv — the approved hauler mapping — and then resolved, blob-checked
// and digest-compared against the local registry. A components run never guesses
// a registry location from the original reference, and never accepts a bare
// HTTP 200 as proof that the right bytes are there.
func preflightComponentImages(ctx context.Context, cluster ClusterConfig, packageRoot string, lock *MaterialsLock, scope []string, stdout io.Writer) error {
	registry, err := cluster.RegistryAddress()
	if err != nil {
		return err
	}
	tableData, err := os.ReadFile(filepath.Join(packageRoot, "images", "images.tsv"))
	if err != nil {
		return errors.Wrap(err, "read the artifact image table for the component image gate")
	}
	table, err := LoadImageTable(strings.Split(string(tableData), "\n"))
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	gate := NewRegistryContentChecker(client, registry, lock, EvidenceRoots(packageRoot), true)
	for _, component := range scope {
		for _, key := range imagesForComponent(cluster, component) {
			row, ok := table[key.Original]
			if !ok {
				return fmt.Errorf("component %q needs image %s, which is not in this artifact's images.tsv: a components run reads the approved hauler mapping of the package it installs from and never downloads or guesses a location",
					component, key.Original)
			}
			approval, err := gate.Verify(ctx, row)
			if err != nil {
				if absentInRegistry(err) {
					return fmt.Errorf("image %s is missing from the registry %s (%v); prepare that batch's base per the blueprint — a components run never downloads materials",
						key.Original, registry, err)
				}
				return errors.Wrapf(err, "component %q image %s", component, key.Original)
			}
			if stdout != nil {
				fmt.Fprintf(stdout, "components image %s: %s\n", key.Original, approval)
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
	// O_EXCL: the run id was just claimed, so an existing plan file here means
	// something else already owns this id. Overwriting it would silently delete
	// the evidence of another run.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return errors.Wrapf(err, "write the components plan %s without overwriting an existing run", path)
	}
	if _, err := file.Write(encoded); err != nil {
		file.Close()
		return errors.Wrapf(err, "write the components plan %s", path)
	}
	return file.Close()
}

// claimComponentsRunDir derives the plan identity and makes it real: a fresh
// components run id (second-granular timestamp, like the install runner's) plus
// a serial suffix when needed, proven free by exclusively creating the
// directory that will hold this run's new run record. Two consecutive additions
// therefore always get separate plan files, run records, runtime roots, logs and
// reports, and neither can overwrite the other or the base install record.
func claimComponentsRunDir(outDir, clusterName string) (string, string, error) {
	stamp := time.Now().Format("20060102-150405")
	if err := os.MkdirAll(filepath.Join(outDir, "runs"), 0o700); err != nil {
		return "", "", errors.Wrapf(err, "create the components runs directory under %s", outDir)
	}
	for serial := 0; serial < 100; serial++ {
		runID := fmt.Sprintf("ani-components-%s-%s", clusterName, stamp)
		if serial > 0 {
			runID = fmt.Sprintf("%s-%d", runID, serial+1)
		}
		dir := filepath.Join(outDir, "runs", runID)
		err := os.Mkdir(dir, 0o700)
		if err == nil {
			return runID, dir, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", "", errors.Wrapf(err, "claim the components run directory %s", dir)
		}
	}
	return "", "", fmt.Errorf("more than 100 components plans already share the timestamp %s in %s; re-plan a second later", stamp, outDir)
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
	// BaseRunFile/BaseStateFile point at the original install-success record.
	// Execute re-verifies the live cluster against it under the shared product
	// lock; a validation-only record or a mismatch is refused before any write.
	BaseRunFile   string
	BaseStateFile string
	Kubeconfig    string
	Output        string
	// KKBin is the kk binary that runs the components playbook. It defaults to
	// the running executable's absolute path (F12), never a bare PATH lookup.
	KKBin string
	// ProjectAddr optionally points at the local playbook project root (the
	// directory containing builtin/). Empty relies on the binary's embedded
	// builtin project.
	ProjectAddr string
}

// ComponentsExecuteReport is the machine-readable record of one execution.
type ComponentsExecuteReport struct {
	SchemaVersion   int                       `json:"schemaVersion"`
	RunID           string                    `json:"runId"`
	ClusterName     string                    `json:"clusterName"`
	Results         []ComponentsPlanComponent `json:"results"`
	ConnectionsFile string                    `json:"connectionsFile,omitempty"`
	StartedAt       string                    `json:"startedAt"`
	FinishedAt      string                    `json:"finishedAt"`
	Overall         string                    `json:"overall"`
}

// RunComponentsExecute executes the planned scope of an R15.1 plan. The
// identity is re-checked (the config must still be the one the plan was built
// from), already_installed components stay read-only, and only the planned
// components are handed to the standalone playbook.
// validateComponentsPlan enforces the plan schema before anything trusts it:
// version, identity bindings, digest formats, legal statuses, and no
// duplicates or unknown scope members (F02 — a plan is an input, not a
// promise; anything malformed means re-plan).
func validateComponentsPlan(plan ComponentsPlan) error {
	if plan.SchemaVersion != 1 {
		return fmt.Errorf("plan schemaVersion %d is not supported; re-plan", plan.SchemaVersion)
	}
	if strings.TrimSpace(plan.RunID) == "" || !strings.HasPrefix(plan.RunID, "ani-components-") {
		return fmt.Errorf("plan runId %q is malformed; re-plan", plan.RunID)
	}
	// The run id names directories, so it must stay a single safe path element.
	if plan.RunID != filepath.Base(plan.RunID) || strings.ContainsAny(plan.RunID, `/\`) {
		return fmt.Errorf("plan runId %q is not a single safe path element; re-plan", plan.RunID)
	}
	if !filepath.IsAbs(plan.BaseRunFile) || filepath.Clean(plan.BaseRunFile) != plan.BaseRunFile {
		return fmt.Errorf("plan baseRunFile %q is not an absolute clean path; re-plan", plan.BaseRunFile)
	}
	if !isHex64(plan.NewConfigDigest) || !isHex64(plan.BaseConfigDigest) {
		return errors.New("plan config digests are malformed; re-plan")
	}
	if !isHex64(plan.KKBinaryDigest) || plan.BaseClusterUID == "" || strings.TrimSpace(plan.BaseRunFile) == "" {
		return errors.New("plan carries no binary/cluster/base-run identity binding (older plan shape); re-plan before executing")
	}
	seen := map[string]bool{}
	planned := 0
	for _, c := range plan.Components {
		if seen[c.Component] {
			return fmt.Errorf("plan repeats component %q; re-plan", c.Component)
		}
		seen[c.Component] = true
		switch c.Status {
		case "planned":
			planned++
		case "already_installed":
		default:
			return fmt.Errorf("plan component %q has illegal status %q; re-plan", c.Component, c.Status)
		}
	}
	if len(plan.Components) == 0 || planned+len(alreadyInstalledNames(plan)) == 0 {
		return errors.New("plan declares no components; re-plan")
	}
	return nil
}

func alreadyInstalledNames(plan ComponentsPlan) []string {
	var out []string
	for _, c := range plan.Components {
		if c.Status == "already_installed" {
			out = append(out, c.Component)
		}
	}
	return out
}

// verifyInstallerExecutionHost refuses to plan/execute from any host that is
// not the installer node's own address: a local lock protects nothing when
// two different machines both believe they are the installer (T17).
func verifyInstallerExecutionHost(cluster ClusterConfig) error {
	installer, err := cluster.Installer()
	if err != nil {
		return err
	}
	wanted := net.ParseIP(strings.TrimSpace(installer.Address))
	candidates := []net.IP{}
	if wanted == nil {
		if ips, err := net.LookupIP(strings.TrimSpace(installer.Address)); err == nil {
			candidates = ips
		} else {
			return fmt.Errorf("cannot resolve installer node address %q: %w", installer.Address, err)
		}
	} else {
		candidates = []net.IP{wanted}
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return errors.Wrap(err, "enumerate local interfaces")
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		for _, want := range candidates {
			if ip != nil && ip.Equal(want) {
				return nil
			}
		}
	}
	return fmt.Errorf("this host does not own the installer node address %s; components execute must run ON the installer node where the product lock lives", installer.Address)
}

func RunComponentsExecute(ctx context.Context, input ComponentsExecuteInput, stdout io.Writer) error {
	planData, err := os.ReadFile(strings.TrimSpace(input.PlanFile))
	if err != nil {
		return errors.Wrapf(err, "read the components plan %s", input.PlanFile)
	}
	var plan ComponentsPlan
	if err := json.Unmarshal(planData, &plan); err != nil {
		return errors.Wrapf(err, "parse the components plan %s", input.PlanFile)
	}
	if err := validateComponentsPlan(plan); err != nil {
		return err
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
	baseRun := strings.TrimSpace(input.BaseRunFile)
	if baseRun == "" {
		baseRun = plan.BaseRunFile
	}
	if abs, err := filepath.Abs(baseRun); err == nil && plan.BaseRunFile != "" && abs != plan.BaseRunFile {
		return fmt.Errorf("execute was pointed at base run %s but the plan was built against %s; re-plan", abs, plan.BaseRunFile)
	}
	baseManifest, err := loadBaseRunRecord(baseRun, input.BaseStateFile)
	if err != nil {
		return err
	}
	if err := verifyInstallerExecutionHost(*cluster); err != nil {
		return err
	}

	// Shared product lock with the first install and acceptance: no other
	// change-bearing operation of this installer can interleave (T16).
	releaseLock, err := AcquireInstallFlock(productLockPath())
	if err != nil {
		return err
	}
	defer releaseLock()

	// Under the lock, re-verify the LIVE scene the plan inspected: cluster
	// fingerprint + node health, per-namespace/SC facts, ownership current
	// state, and the recomputed scope. Any drift requires a new plan; nothing
	// has been written to the cluster yet.
	kubeconfig := strings.TrimSpace(input.Kubeconfig)
	if kubeconfig == "" {
		kubeconfig = "/etc/kubernetes/admin.conf"
	}
	runner := kubectlRunner{bin: kubectlBin(), kubeconfig: kubeconfig}
	liveID, notReady, err := captureLiveCluster(ctx, runner)
	if err != nil {
		return err
	}
	if err := requireBaseIdentity(baseManifest, liveID, notReady); err != nil {
		return err
	}
	if liveID.ClusterUID != plan.BaseClusterUID {
		return fmt.Errorf("the live cluster %s is not the cluster the plan inspected (%s); re-plan", liveID.ClusterUID, plan.BaseClusterUID)
	}
	plannedNames := make([]string, 0, len(plan.Components))
	for _, c := range plan.Components {
		if c.Status == "planned" {
			plannedNames = append(plannedNames, c.Component)
		}
	}
	// The artifact's approved materials lock is re-read here so the image gate
	// below compares against the same approval the plan used (F06).
	artifactLock, err := artifactMaterialsLock(input.PackageRoot)
	if err != nil {
		return err
	}
	if len(plannedNames) > 0 {
		recomputedScope, err := componentsScope(*cluster, plannedNames)
		if err != nil {
			return errors.Wrap(err, "plan scope no longer resolves against the site config")
		}
		// The closure is recomputed from the site config and must still be
		// exactly the set the plan intends to WRITE. already_installed rows are
		// not in it (they are never executed), so the comparison is planned-vs-
		// planned: a dependency that appeared or vanished after planning means
		// the plan no longer describes this cluster.
		plannedSorted := append([]string(nil), plannedNames...)
		sort.Strings(plannedSorted)
		recomputedSorted := append([]string(nil), recomputedScope...)
		sort.Strings(recomputedSorted)
		if strings.Join(recomputedSorted, ",") != strings.Join(plannedSorted, ",") {
			return fmt.Errorf("the recomputed --only/dependency closure %v no longer matches the planned component scope %v; re-plan", recomputedScope, plannedNames)
		}
		if _, err := preflightLiveCluster(ctx, runner, *cluster, *manifest, recomputedScope); err != nil {
			return errors.Wrap(err, "the live preflight changed after planning; re-plan")
		}
		if err := preflightComponentImages(ctx, *cluster, input.PackageRoot, artifactLock, recomputedScope, nil); err != nil {
			return errors.Wrap(err, "the packaged image content changed after planning; re-plan")
		}
		freshOwnership, err := preflightComponentOwnership(ctx, runner, *cluster, recomputedScope)
		if err != nil {
			return errors.Wrap(err, "ownership changed after planning; re-plan")
		}
		freshStatus := map[string]string{}
		for _, c := range freshOwnership {
			freshStatus[c.Component] = c.Status
		}
		for _, c := range plan.Components {
			if c.Status == "planned" && freshStatus[c.Component] != "planned" {
				return fmt.Errorf("planned component %q is now %q on the live cluster; the scene changed after planning — re-plan", c.Component, freshStatus[c.Component])
			}
		}
	}
	// Material identity: the artifact's approved lock must still be the one the
	// base install was built from (F02 materials identity, cheap digest check).
	// A package root that carries no lock at all cannot prove anything — that
	// is the fixture case and is reported; a lock whose digest differs from the
	// base identity means the scene changed after planning.
	// The materials identity this execution actually saw, recorded even when the
	// base pinned no lock (a package root with no lock cannot prove anything).
	lockDigestForRecord := sha256FileHex(filepath.Join(input.PackageRoot, "config", "components.lock.yaml"))
	currentLock := lockDigestForRecord
	if baseManifest.Identity.MaterialsLockDigest != "" {
		if currentLock == "" {
			return fmt.Errorf("the base install recorded approved materials lock %s but this package root carries none; re-plan from the approved artifact", baseManifest.Identity.MaterialsLockDigest)
		}
		currentCanonical := currentLock
		if !strings.HasPrefix(baseManifest.Identity.MaterialsLockDigest, "sha256:") {
			// digest fields in the record are stored without prefix in some
			// shapes; compare like-for-like.
			currentCanonical = "sha256:" + currentLock
		}
		if currentLock != baseManifest.Identity.MaterialsLockDigest && currentCanonical != baseManifest.Identity.MaterialsLockDigest {
			return fmt.Errorf("the artifact lock digest %s no longer matches the base install's %s; materials changed after planning — rebuild the plan from the approved artifact", currentLock, baseManifest.Identity.MaterialsLockDigest)
		}
	}

	var executed, already []string
	scopeMap := map[string]any{}
	for _, component := range plan.Components {
		switch component.Status {
		case "planned":
			scopeMap[component.Component] = true
			executed = append(executed, component.Component)
		case "already_installed":
			already = append(already, component.Component)
		default:
			// validateComponentsPlan admits only the two statuses above, so this
			// is an internal contract break; refusing beats recording a
			// component under a status nobody checked.
			return fmt.Errorf("plan component %q carries status %q that execute cannot map; re-plan", component.Component, component.Status)
		}
	}
	// F12: the child kk is THIS executable by absolute default, never a bare
	// PATH name that could resolve to a different (older) binary; an explicit
	// --kk is recorded so the report says which binary really wrote.
	//
	// This is resolved and verified BEFORE any directory, log or record of the
	// run is created: a run that cannot prove it is executing the planned code
	// leaves no half-written run behind. The read-only pass below is included —
	// its record attests this digest as the identity that observed the cluster,
	// so it may not be written by code the plan was never bound to.
	kk := strings.TrimSpace(input.KKBin)
	if kk == "" {
		kkSelf, err := os.Executable()
		if err != nil {
			return errors.Wrap(err, "locate the running kk")
		}
		kk = kkSelf
	}
	// The plan is bound to the binary that will run its playbook; a different
	// child (even at the same path) means the code the plan inspected is not
	// the code about to write (F02).
	if fileSHA256Hex(kk) != plan.KKBinaryDigest {
		return fmt.Errorf("the plan is bound to kk digest %s but the playbook runner is %s; re-plan with the intended release", plan.KKBinaryDigest, fileSHA256Hex(kk))
	}

	if len(executed) == 0 && len(already) > 0 {
		// Nothing left to install: a read-only no-op success, recorded, with
		// the playbook never invoked and the base never touched (spec §4.4).
		report := ComponentsExecuteReport{
			SchemaVersion: 1,
			RunID:         plan.RunID,
			ClusterName:   plan.ClusterName,
			StartedAt:     time.Now().UTC().Format(time.RFC3339),
		}
		for _, component := range already {
			report.Results = append(report.Results, r15PlanComponent(component, "already_installed",
				"read-only no-op: the ANI-owned same-version release was not executed again"))
		}
		report.Overall = VerifyStatusPass
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		reportPath, err := writeComponentsExecuteReport(input.Output, plan.RunID, report)
		if err != nil {
			return err
		}
		// L-06: this read-only pass is worth a consumable record — it attests
		// what it observed, that it installed nothing, and which evidence it
		// relied on. It never claims the earlier install happened now.
		statuses := map[string]string{}
		for _, name := range already {
			statuses[name] = "already_installed"
		}
		recordPath, recErr := recordComponentsExecution(ctx, runner, baseManifest, plan, liveID, currentLock,
			ComponentsOperationNoop, ResultSucceeded, statuses,
			[]ComponentsEvidenceRef{{Kind: ComponentsEvidencePlan, Path: input.PlanFile, SHA256: sha256FileHex(input.PlanFile)},
				{Kind: ComponentsEvidenceExecuteReport, Path: reportPath, SHA256: sha256FileHex(reportPath)}})
		if recErr != nil {
			return errors.Wrapf(recErr, "the read-only no-op ran but its consumable record could not be written; re-run execute to obtain it")
		}
		out := stdout
		if out == nil {
			out = os.Stdout
		}
		for _, result := range report.Results {
			fmt.Fprintf(out, "components execute %s: %s\n", result.Component, result.Status)
		}
		fmt.Fprintf(out, "components execute overall: %s (run=%s no-op=true)\n", report.Overall, plan.RunID)
		fmt.Fprintf(out, "components record: %s\n", recordPath)
		fmt.Fprintf(out, "NOTE: this record attests a read-only observation (didInstall=false); it grants verification scope for the components it confirmed ANI-owned, and no change authority over anything else.\n")
		return nil
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
	args := []string{"run", componentsPlaybookRunPath,
		"-a", filepath.Join(input.PackageRoot, "packages", "kubekey-artifact.tgz"),
		"-c", filepath.Join(workRoot, "config.yaml"),
		"-i", filepath.Join(workRoot, "inventory.yaml"),
		"--workdir", root,
		"--project-addr", projectAddr,
	}
	cmd := exec.CommandContext(ctx, kk, args...)
	// F02: the kubeconfig that plan-checks were made against is the kubeconfig
	// the child playbook and its kubectl/Helm steps use. Inherited HOME or
	// KUBECONFIG cannot silently repoint the write target.
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if runErr := cmd.Run(); runErr != nil {
		fmt.Fprintf(logFile, "components run failed: %v\n", runErr)
		// A failed run keeps its log and material directories; nothing is
		// cleaned up or replayed (R15 step 7 / R14 boundary). The report still
		// names every component of this run, so a partial failure is recorded
		// as what happened rather than as an empty result (§4.5).
		for _, component := range executed {
			report.Results = append(report.Results, r15PlanComponent(component, VerifyStatusFailed,
				"the playbook invocation failed; this installer never replays or cleans up partial work — re-plan to read the live ownership state"))
		}
		for _, component := range already {
			report.Results = append(report.Results, r15PlanComponent(component, "already_installed",
				"read-only: the same-version release was not executed again"))
		}
		report.Overall = VerifyStatusFailed
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		reportPath, writeErr := writeComponentsExecuteReport(input.Output, plan.RunID, report)
		if writeErr != nil {
			return errors.Wrapf(runErr, "record the failed components run too: %v", writeErr)
		}
		// The failed pass gets a record too, marked failed: it is an event
		// fact, and verify must be able to say why it refuses it.
		recordFailedExecution(ctx, runner, baseManifest, plan, liveID, lockDigestForRecord,
			executed, already, input.PlanFile, reportPath)
		return errors.Wrap(runErr, "components playbook execution failed")
	}

	for _, component := range executed {
		report.Results = append(report.Results, r15PlanComponent(component, "executed", ""))
	}
	for _, component := range already {
		report.Results = append(report.Results, r15PlanComponent(component, "already_installed",
			"read-only: the same-version release was not executed again"))
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
		// The playbook did run, so the run's own outcome stays recorded; only
		// the aggregation is unresolved and the operator is told so.
		for index := range report.Results {
			if report.Results[index].Status == "executed" {
				report.Results[index].Detail = fmt.Sprintf("the playbook ran but its connection facts could not be aggregated: %v", err)
			}
		}
		report.Overall = VerifyStatusFailed
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		reportPath, writeErr := writeComponentsExecuteReport(input.Output, plan.RunID, report)
		if writeErr != nil {
			return errors.Wrapf(err, "record the components run whose connection facts failed: %v", writeErr)
		}
		// The playbook already changed the cluster, so this outcome must be as
		// consumable as any other. Landing no record here would reproduce L-06
		// exactly — a mutated cluster with only an untyped report that verify
		// refuses — and would leave the operator no record to reason from.
		recordFailedExecution(ctx, runner, baseManifest, plan, liveID, lockDigestForRecord,
			executed, already, input.PlanFile, reportPath)
		return errors.Wrap(err, "write the new run's connection facts")
	}
	report.ConnectionsFile = connectionsFile
	report.Overall = VerifyStatusPass
	report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	reportPath, err := writeComponentsExecuteReport(input.Output, plan.RunID, report)
	if err != nil {
		return err
	}
	// L-06: only now — after the playbook ran, the facts aggregated and the
	// report landed — does the product write the record that `ani verify` can
	// consume. Its absence fails the command; nothing is re-executed to
	// manufacture one, and the site keeps whatever the playbook did.
	statuses := map[string]string{}
	for _, name := range executed {
		statuses[name] = "executed"
	}
	for _, name := range already {
		statuses[name] = ComponentsTargetAlreadyInstalled
	}
	evidence := []ComponentsEvidenceRef{
		{Kind: ComponentsEvidencePlan, Path: input.PlanFile, SHA256: sha256FileHex(input.PlanFile)},
		{Kind: ComponentsEvidenceExecuteReport, Path: reportPath, SHA256: sha256FileHex(reportPath)},
	}
	if connectionsFile != "" {
		evidence = append(evidence, ComponentsEvidenceRef{Kind: ComponentsEvidenceConnections, Path: connectionsFile, SHA256: sha256FileHex(connectionsFile)})
	}
	recordPath, recErr := recordComponentsExecution(ctx, runner, baseManifest, plan, liveID, lockDigestForRecord,
		ComponentsOperationAdd, ResultSucceeded, statuses, evidence)
	if recErr != nil {
		return errors.Wrapf(recErr, "the components ran but their consumable record could not be written; verify cannot run without it, and nothing here re-executes the playbook")
	}

	out := stdout
	if out == nil {
		out = os.Stdout
	}
	for _, result := range report.Results {
		fmt.Fprintf(out, "components execute %s: %s\n", result.Component, result.Status)
	}
	fmt.Fprintf(out, "components execute overall: %s (run=%s connections=%s)\n", report.Overall, plan.RunID, connectionsFile)
	fmt.Fprintf(out, "components record: %s\n", recordPath)
	fmt.Fprintf(out, "verify this run with: kk ani verify --run %s --level smoke\n", recordPath)
	return nil
}

// recordFailedExecution lands the failed record of a run that reached the
// cluster. Every executed component is recorded as failed, because the pass is
// unresolved and nothing here may claim it succeeded. A record this helper
// cannot write is reported on stderr but never replaces the run's own failure:
// the operator must see what broke first, and only then what could not be
// written down.
func recordFailedExecution(ctx context.Context, runner kubectlRunner, base RunManifest, plan ComponentsPlan,
	liveID ManifestIdentity, lockDigest string, executed, already []string, planFile, reportPath string) {

	statuses := make(map[string]string, len(executed)+len(already))
	for _, name := range executed {
		statuses[name] = ComponentsTargetFailed
	}
	for _, name := range already {
		statuses[name] = ComponentsTargetAlreadyInstalled
	}
	evidence := []ComponentsEvidenceRef{{Kind: ComponentsEvidencePlan, Path: planFile, SHA256: sha256FileHex(planFile)}}
	if reportPath != "" {
		evidence = append(evidence, ComponentsEvidenceRef{
			Kind: ComponentsEvidenceExecuteReport, Path: reportPath, SHA256: sha256FileHex(reportPath)})
	}
	recordPath, recErr := recordComponentsExecution(ctx, runner, base, plan, liveID, lockDigest,
		ComponentsOperationAdd, ResultFailed, statuses, evidence)
	if recErr != nil {
		fmt.Fprintf(os.Stderr, "components record could not be written for this failed run, so verify has nothing to refuse: %v\n", recErr)
		return
	}
	fmt.Fprintf(os.Stderr, "components record (failed, not consumable): %s\n", recordPath)
}

func readImageTableOrEmpty(packageRoot string) string {
	data, err := os.ReadFile(filepath.Join(packageRoot, "images", "images.tsv"))
	if err != nil {
		return ""
	}
	return string(data)
}

func writeComponentsExecuteReport(dir, runID string, report ComponentsExecuteReport) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", errors.Wrapf(err, "create the components report directory %s", dir)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", errors.Wrap(err, "encode the components execution report")
	}
	encoded = append(encoded, '\n')
	path := filepath.Join(dir, fmt.Sprintf("components-execute-%s.json", runID))
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

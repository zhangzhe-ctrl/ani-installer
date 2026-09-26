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
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// R15.1 behaviour tests: the components plan with its rejection paths, driven
// by a fake kubectl and a fake registry. The plan path must never mutate a
// cluster: no create_cluster, no kubeadm, no CNI/Ceph/Envoy, no component
// execution.
// ---------------------------------------------------------------------------

// r15BaseSite is the base install's site: storage on, nats NOT enabled.
const r15BaseSite = `name: ani-lab
installerNode: node1
ssh: {user: ubuntu, port: 22, password: r15-secret}
nodes:
  - {name: node1, address: 127.0.0.1}
  - {name: node2, address: 192.0.2.12}
  - {name: node3, address: 192.0.2.13}
network:
  stack: kcn
  managementInterface: ens34
  podCIDR: 10.16.0.0/16
  serviceCIDR: 10.96.0.0/16
  kcn: {managedDevices: [ens35], encapNetworks: [192.0.2.0/24], intranetNetworks: [192.0.2.0/24, 10.96.0.0/16]}
registry: {port: 5000}
storage:
  enabled: true
  provider: ceph
  nodes:
    - {name: node1, devices: [/dev/disk/by-id/ata-r15-01]}
    - {name: node2, devices: [/dev/disk/by-id/ata-r15-02]}
    - {name: node3, devices: [/dev/disk/by-id/ata-r15-03]}
components:
  certManager: {enabled: true}
`

// r15SiteWithComponents rewrites the base site: the registry port points at
// the given port and the given components block replaces the default one.
func r15SiteWithComponents(registryPort int, componentsBlock string) string {
	site := strings.Replace(r15BaseSite, "registry: {port: 5000}", fmt.Sprintf("registry: {port: %d}", registryPort), 1)
	return strings.Replace(site, "components:\n  certManager: {enabled: true}", "components:\n"+componentsBlock, 1)
}

// r15FakeKubectl answers the read-only queries the plan and execute preflight
// make and records everything. Knobs:
//
//	FAKE_NATS_RELEASE: absent | ours | foreign | no-marker | v2-failed
//	FAKE_CLUSTER_UID   overrides the kube-system uid (wrong-cluster control)
//	FAKE_NODE_NOT_READY=1  one node reports Ready=False
//	FAKE_NS_FORBIDDEN=1    namespace probes answer Forbidden, not NotFound
func r15FakeKubectl(t *testing.T, binDir, natsSecretFile string) string {
	t.Helper()
	script := `#!/usr/bin/env bash
log="${FAKE_KUBECTL_LOG:?}"
printf '%s\n' "$*" >> "$log"
while true; do
  case "$1" in
    --kubeconfig) shift 2 ;;
    --request-timeout=*) shift ;;
    *) break ;;
  esac
done
args="$*"
uid="${FAKE_CLUSTER_UID:-uid-kube-cluster-a}"
case "$args" in
  "version -o json")
    echo '{"serverVersion":{"gitVersion":"v1.35.8"}}'; exit 0 ;;
  *"get namespace kube-system"*)
    echo "$uid"; exit 0 ;;
  *"get nodes"*"-o json"*)
    if [ -n "${FAKE_NODE_NOT_READY:-}" ]; then
      echo '{"items":[{"metadata":{"name":"node1"},"status":{"conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"node2"},"status":{"conditions":[{"type":"Ready","status":"False"}]}},{"metadata":{"name":"node3"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}'
    else
      echo '{"items":[{"metadata":{"name":"node1"},"status":{"conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"node2"},"status":{"conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"node3"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}'
    fi
    exit 0 ;;
  *"get namespace"*)
    if [ -n "${FAKE_NS_FORBIDDEN:-}" ]; then
      echo 'Error from server (Forbidden): forbidden' >&2; exit 1
    fi
    echo "uid-ns-ani"; exit 0 ;;
  *"get storageclass"*)
    echo "uid-sc-ani"; exit 0 ;;
  *"get daemonset"*)
    # The CNI health probe reads desired/ready counts. The base stack here is
    # kcn, so both kcn DaemonSets answer; FAKE_CNI_UNHEALTHY models a data path
    # that is only half-deployed.
    case "$args" in
      *kcn-system*|*kube-system*) ;;
      *) echo "Error from server (NotFound)" >&2; exit 1 ;;
    esac
    if [ -n "${FAKE_CNI_UNHEALTHY:-}" ]; then
      echo "3 1"; exit 0
    fi
    echo "3 3"; exit 0 ;;
  *"get secret"*"-l owner=helm,name=nats"*)
    mode="${FAKE_NATS_RELEASE:-absent}"
    case "$mode" in
      absent) echo '{"items":[]}'; exit 0 ;;
      foreign) cat "${FAKE_FOREIGN_SECRET_FILE:?}" ;;
      *) cat "${FAKE_NATS_SECRET_FILE:?}" ;;
    esac
    exit 0 ;;
  *"get statefulset postgresql"*"{.metadata.labels"*)
    echo "ani-lab"; exit 0 ;;
  *"get statefulset"*"{.metadata.uid}"*)
    # Ownership and the execution record both bind to a LIVE uid. Which
    # StatefulSets exist is scripted per case: FAKE_STS_EXISTS is the comma
    # list of names present; absent names answer NotFound like a real cluster.
    nm="$(printf '%s\n' "$args" | awk '{for(i=1;i<=NF;i++) if($i=="statefulset"){print $(i+1); exit}}')"
    # What the (fake) playbook created for this run exists afterwards, exactly
    # as a real apply would leave a StatefulSet behind for the record to bind.
    existing="${FAKE_STS_EXISTS:-nats}"
    for created in "${FAKE_CREATED_FILE:-}" "${FAKE_RUNTIME_BASE:-}/.applied"; do
      if [ -n "$created" ] && [ -f "$created" ]; then
        existing="$existing,$(tr '\n' ',' < "$created" | sed 's/,$//')"
      fi
    done
    present=0
    case ",${existing}," in
      *",${nm},"*) present=1 ;;
    esac
    if [ -n "${FAKE_STS_ABSENT:-}" ]; then present=0; fi
    if [ "$present" = "0" ]; then
      echo "Error from server (NotFound): statefulsets.apps \"$nm\" not found" >&2; exit 1
    fi
    echo "${FAKE_STS_UID:-uid-sts-mock}"; exit 0 ;;
  *"get statefulset postgresql"*)
    echo "Error from server (NotFound)" >&2
    exit 1 ;;
  *"get statefulset valkey"*)
    echo "Error from server (NotFound)" >&2
    exit 1 ;;
esac
echo "fake kubectl: unsupported $args" >&2
exit 2
`
	path := filepath.Join(binDir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake kubectl: %v", err)
	}
	return path
}

// r15HelmSecret builds a helm release-secret LIST for the nats release with
// the given chart/version, current revision status, and ownership labels.
// mode: ours (deployed + ANI marker) | foreign (chart mismatch) |
// no-marker (deployed, marker names another cluster) | v2-failed (v1 deployed
// + newer v2 failed).
func r15HelmSecret(t *testing.T, dir, mode string) string {
	t.Helper()
	release := map[string]any{
		"chart": map[string]any{
			"metadata": map[string]any{"name": "nats", "version": "2.14.6"},
		},
	}
	if mode == "foreign" {
		release = map[string]any{
			"chart": map[string]any{"metadata": map[string]any{"name": "nginx", "version": "1.0.0"}},
		}
	}
	payload, err := json.Marshal(release)
	if err != nil {
		t.Fatalf("marshal release: %v", err)
	}
	var gz bytes.Buffer
	writer := gzip.NewWriter(&gz)
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	// Fidelity (defect 7): helm stores Data["release"] as the BYTES of
	// base64(gzip(json)), and `kubectl get secret -o json` base64-encodes
	// those bytes again — the payload is double base64 around the gzip.
	encode := func(rev int, status string) map[string]any {
		labels := map[string]any{
			"owner": "helm", "name": "nats", "status": status,
			"version": fmt.Sprint(rev),
		}
		switch mode {
		case "ours", "v2-failed":
			labels[aniManagedByLabel] = "ani-lab"
		case "no-marker":
			labels[aniManagedByLabel] = "another-lab"
		}
		return map[string]any{
			"metadata": map[string]any{
				"name":   fmt.Sprintf("sh.helm.release.v1.nats.v%d", rev),
				"labels": labels,
			},
			"data": map[string]string{"release": base64.StdEncoding.EncodeToString([]byte(base64.StdEncoding.EncodeToString(gz.Bytes())))},
		}
	}
	var items []map[string]any
	switch mode {
	case "v2-failed":
		items = []map[string]any{encode(1, "deployed"), encode(2, "failed")}
	default:
		items = []map[string]any{encode(1, "deployed")}
	}
	encoded, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		t.Fatalf("marshal secrets: %v", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("helm-nats-%s.json", mode))
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	return path
}

// r15SuccessBaseRun writes the base install-success record (plus its
// run-state.json) that a real first install would have produced: record kind,
// result, and the captured live identity the preflight compares against.
func r15SuccessBaseRun(t *testing.T, baseConfig ClusterConfig, path string) RunManifest {
	t.Helper()
	manifest, err := BuildRunManifest(baseConfig)
	if err != nil {
		t.Fatalf("build base manifest: %v", err)
	}
	manifest.SchemaVersion = RunManifestSchemaVersion
	manifest.RecordKind = RecordKindInstallSuccess
	manifest.RunID = "ani-ani-lab-20260924-120000"
	manifest.Result = ResultSucceeded
	manifest.MaterialsValidated = true
	// The fixture package root carries a components.lock.yaml like a real
	// artifact; its digest is the materials identity execute must re-verify.
	// r15PlanFixture writes the real-shape lock (with the fake registry's
	// digests) before calling this; a second base record built elsewhere only
	// needs a parsable lock of its own.
	lockPath := filepath.Join(filepath.Dir(path), "config", "components.lock.yaml")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		if err := os.WriteFile(lockPath, []byte("apiVersion: ani.installer/v1\nkind: ComponentMaterialLock\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest.Identity = ManifestIdentity{
		SiteConfigDigest:    manifest.ConfigDigest,
		MaterialsLockDigest: sha256FileHex(lockPath),
		ClusterUID:          "uid-kube-cluster-a",
		NodeCount:           3,
		ReadyNodes:          []string{"node1", "node2", "node3"},
	}
	if err := WriteInstallSuccessRecord(filepath.Dir(path), manifest); err != nil {
		t.Fatalf("write base success record: %v", err)
	}
	// run.json landed next to the requested path; also record a matching
	// install state so loadBaseRunRecord can cross-check it.
	state := InstallState{
		SchemaVersion:    InstallStateSchemaVersion,
		RunID:            manifest.RunID,
		Phase:            PhaseSucceeded,
		Result:           ResultSucceeded,
		ChangesStarted:   true,
		RemoteResult:     RemoteResultDeterministic,
		ClusterName:      manifest.ClusterName,
		SiteConfigDigest: manifest.ConfigDigest,
		ConfigDigest:     manifest.ConfigDigest,
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "run-state.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return manifest
}

// r15BlobDigest is the deterministic digest the fake registry uses for one
// blob kind of one repository.
func r15BlobDigest(kind, repo string) string {
	sum := sha256.Sum256([]byte(kind + ":" + repo))
	return fmt.Sprintf("sha256:%x", sum)
}

// r15PlatformManifest is the single linux/amd64 manifest the fake registry
// serves for a repository: one config blob and one layer blob.
func r15PlatformManifest(repo string) []byte {
	// Sizes must describe the bytes the registry actually serves: the content gate
	// compares each downloaded blob's length with the manifest's declaration.
	return []byte(fmt.Sprintf(`{"schemaVersion":2,"config":{"digest":"%s","size":%d},"layers":[{"digest":"%s","size":%d}]}`,
		r15BlobDigest("config", repo), len("config:"+repo), r15BlobDigest("layer", repo), len("layer:"+repo)))
}

func r15DigestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return fmt.Sprintf("sha256:%x", sum)
}

// r15Registry is an in-process registry that obeys the OCI digest contract:
// a tag answers with a platform manifest (or, under FAKE_REGISTRY_INDEX, a
// multi-arch index pointing at it), and only the two blobs that manifest
// references exist. The fixture rewrites images.tsv with the platform digests,
// so the plan runs its real content gate — resolve, blob HEADs, digest pin —
// against it instead of a blanket HTTP 200.
//
// Knobs: FAKE_REGISTRY_INDEX=<repo substring> serves that repo's tag as an
// index; FAKE_REGISTRY_TAMPER=<repo substring> serves bytes that no longer hash
// to the pinned digest; FAKE_REGISTRY_MISSING_BLOB=<repo substring> makes a
// referenced blob 404.
func r15Registry(t *testing.T, refuseAll bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refuseAll {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/v2/")
		if marker := strings.Index(path, "/manifests/"); marker >= 0 {
			repo := path[:marker]
			reference := path[marker+len("/manifests/"):]
			platform := r15PlatformManifest(repo)
			if !strings.HasPrefix(reference, "sha256:") && strings.Contains(repo, os.Getenv("FAKE_REGISTRY_TAMPER")) &&
				os.Getenv("FAKE_REGISTRY_TAMPER") != "" {
				platform = append(platform, ' ')
			}
			if strings.HasPrefix(reference, "sha256:") && r15DigestOf(platform) != reference {
				http.Error(w, "unknown manifest digest", http.StatusNotFound)
				return
			}
			indexFor := os.Getenv("FAKE_REGISTRY_INDEX")
			if indexFor != "" && !strings.HasPrefix(reference, "sha256:") && strings.Contains(repo, indexFor) {
				body := []byte(`{"manifests":[{"digest":"` + r15DigestOf(platform) +
					`","platform":{"os":"linux","architecture":"amd64"}}]}`)
				w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
				_, _ = w.Write(body)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			_, _ = w.Write(platform)
			return
		}
		if marker := strings.Index(path, "/blobs/"); marker >= 0 {
			repo := path[:marker]
			digest := path[marker+len("/blobs/"):]
			if missing := os.Getenv("FAKE_REGISTRY_MISSING_BLOB"); missing != "" && strings.Contains(repo, missing) {
				http.Error(w, "unknown blob", http.StatusNotFound)
				return
			}
			// Serve the bytes the digest names: an installer gate that hashes what
			// it downloads cannot be satisfied by a 200 with no body.
			for _, kind := range []string{"config", "layer"} {
				candidate := []byte(kind + ":" + repo)
				if r15DigestOf(candidate) == digest {
					w.Header().Set("Content-Type", "application/octet-stream")
					_, _ = w.Write(candidate)
					return
				}
			}
			http.Error(w, "unknown blob", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server
}

// r15RewriteLockDigests takes the shipped materials lock and replaces every
// amd64ManifestDigest with the digest the fake registry really serves for that
// entry's haulerRef. The lock keeps its real shape (so its parser and its
// approval structure are exercised), while the fake registry and the fake
// images.tsv agree with it.
func r15RewriteLockDigests(t *testing.T, lockText string) string {
	t.Helper()
	lines := strings.Split(lockText, "\n")
	repo := ""
	for index, line := range lines {
		if match := regexpHaulerRef.FindStringSubmatch(line); match != nil {
			repo = strings.TrimPrefix(match[1], "127.0.0.1:5000/")
			if colon := strings.LastIndex(repo, ":"); colon > 0 {
				repo = repo[:colon]
			}
			continue
		}
		if match := regexpPlatformDigest.FindStringSubmatch(line); match != nil {
			if repo == "" {
				t.Fatalf("lock digest line %d has no preceding haulerRef", index+1)
			}
			lines[index] = match[1] + "amd64ManifestDigest: " + r15DigestOf(r15PlatformManifest(repo))
		}
	}
	return strings.Join(lines, "\n")
}

var (
	regexpHaulerRef      = regexp.MustCompile(`^\s*haulerRef:\s*(\S+)\s*$`)
	regexpPlatformDigest = regexp.MustCompile(`^(\s*)amd64ManifestDigest:\s*sha256:[0-9a-f]{64}\s*$`)
)

// r15RewriteImageTable copies the shipped images.tsv into the fixture artifact
// root with every actual_digest replaced by the digest the fake registry really
// serves for that row's hauler_ref. The real table cannot be reused verbatim:
// its digests come from upstream repositories this test never contacts.
func r15RewriteImageTable(t *testing.T, imagesDir string) {
	t.Helper()
	shipped, err := os.ReadFile(filepath.Join("..", "..", "ani", "images.tsv"))
	if err != nil {
		t.Fatalf("read shipped images.tsv: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(shipped), "\n"), "\n")
	rewritten := make([]string, 0, len(lines))
	for index, line := range lines {
		if index == 0 || strings.TrimSpace(line) == "" {
			rewritten = append(rewritten, line)
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			t.Fatalf("shipped images.tsv line %d has %d fields", index+1, len(fields))
		}
		repo := strings.TrimPrefix(fields[1], "127.0.0.1:5000/")
		if colon := strings.LastIndex(repo, ":"); colon > 0 {
			repo = repo[:colon]
		}
		fields[2] = r15DigestOf(r15PlatformManifest(repo))
		rewritten = append(rewritten, strings.Join(fields, "\t"))
	}
	if err := os.WriteFile(filepath.Join(imagesDir, "images.tsv"), []byte(strings.Join(rewritten, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write images.tsv: %v", err)
	}
}

func r15ReadPlan(t *testing.T, outDir string) ComponentsPlan {
	t.Helper()
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("read out dir: %v", err)
	}
	var planFile string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "components-plan-") {
			planFile = filepath.Join(outDir, entry.Name())
		}
	}
	if planFile == "" {
		t.Fatalf("no components plan written under %s: %v", outDir, entries)
	}
	data, err := os.ReadFile(planFile)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	var plan ComponentsPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	return plan
}

// r15PlanFixture wires base run record, new site config, fake kubectl and a
// fake registry for one plan invocation.
func r15PlanFixture(t *testing.T, componentsBlock, only string, refuseRegistry bool, natsRelease string) (ComponentsInstallInput, string) {
	t.Helper()
	// The base install only ever selected cert-manager, so every component this
	// plan adds genuinely changes the effective config. C06 needs the opposite
	// shape — a component the FIRST INSTALL itself put in place — which is
	// r15PlanFixtureOverBase with a base block that already enables it.
	return r15PlanFixtureOverBase(t, "  certManager: {enabled: true}", componentsBlock, only, refuseRegistry, natsRelease)
}

// r15PlanFixtureOverBase is the same fixture with the base install's own
// component block chosen by the caller.
func r15PlanFixtureOverBase(t *testing.T, baseBlock, componentsBlock, only string, refuseRegistry bool, natsRelease string) (ComponentsInstallInput, string) {
	baseDir := t.TempDir()
	stateDir := filepath.Join(baseDir, "state")
	binDir := filepath.Join(baseDir, "bin")
	outDir := filepath.Join(baseDir, "out")
	for _, dir := range []string{stateDir, binDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	registry := r15Registry(t, refuseRegistry)
	port := registry.Listener.Addr().(*net.TCPAddr).Port

	site := r15SiteWithComponents(port, componentsBlock)
	siteFile := filepath.Join(baseDir, "site.yaml")
	if err := os.WriteFile(siteFile, []byte(site), 0o600); err != nil {
		t.Fatalf("write site: %v", err)
	}

	baseSite := r15SiteWithComponents(port, baseBlock)
	baseConfig, err := ParseClusterConfig([]byte(baseSite))
	if err != nil {
		t.Fatalf("parse base site: %v", err)
	}
	// The image and lock inputs are the artifact's own: the shipped table (with
	// the digests the fake registry really serves) and the shipped materials
	// lock. Both gates read them from the package root, and the base success
	// record pins the lock's digest.
	imagesDir := filepath.Join(baseDir, "images")
	if err := os.MkdirAll(imagesDir, 0o700); err != nil {
		t.Fatalf("mkdir images: %v", err)
	}
	r15RewriteImageTable(t, imagesDir)
	if err := os.MkdirAll(filepath.Join(baseDir, "config"), 0o700); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	// A copy with the fake registry's digests, not a link: the temp dir may live
	// on a different filesystem, and the lock must agree with what is served.
	shippedLock, err := os.ReadFile(filepath.Join("..", "..", "ani", "components.lock.yaml"))
	if err != nil {
		t.Fatalf("read the shipped materials lock: %v", err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "config", "components.lock.yaml"),
		[]byte(r15RewriteLockDigests(t, string(shippedLock))), 0o600); err != nil {
		t.Fatalf("write the fixture materials lock: %v", err)
	}

	baseRunFile := filepath.Join(baseDir, "run.json")
	r15SuccessBaseRun(t, baseConfig, baseRunFile)

	if natsRelease != "absent" {
		if natsRelease == "foreign" {
			foreign := r15HelmSecret(t, baseDir, "foreign")
			t.Setenv("FAKE_FOREIGN_SECRET_FILE", foreign)
		} else {
			ours := r15HelmSecret(t, baseDir, natsRelease)
			t.Setenv("FAKE_NATS_SECRET_FILE", ours)
		}
		t.Setenv("FAKE_NATS_RELEASE", natsRelease)
	} else {
		t.Setenv("FAKE_NATS_RELEASE", "absent")
	}
	t.Setenv("FAKE_KUBECTL_LOG", filepath.Join(baseDir, "kubectl-calls.log"))
	r15FakeKubectl(t, binDir, "")
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	input := ComponentsInstallInput{
		ConfigFile:  siteFile,
		PackageRoot: baseDir,
		Only:        strings.Split(only, ","),
		BaseRunFile: baseRunFile,
		Output:      outDir,
		Kubeconfig:  filepath.Join(baseDir, "kubeconfig"),
	}
	return input, outDir
}

// T-R15-01 + T-R15-05: --only nats plans exactly nats (nats has no declared
// internal prerequisites), records both digests, leaves the unselected
// logging backend untouched, and the whole trajectory is read-only.
func TestComponentsInstallPlan(t *testing.T) {
	input, outDir := r15PlanFixture(t, "  certManager: {enabled: true}\n  nats: {enabled: true}", "nats", false, "absent")
	if err := RunComponentsInstallPlan(context.Background(), input, os.Stdout); err != nil {
		t.Fatalf("plan failed: %v", err)
	}
	plan := r15ReadPlan(t, outDir)
	if len(plan.Components) != 1 || plan.Components[0].Component != "nats" || plan.Components[0].Status != "planned" {
		t.Fatalf("the plan must contain exactly nats as planned: %+v", plan.Components)
	}
	// T-R15-05: the unselected logging backend is untouched.
	for _, stale := range []string{"loki", "opensearch", "fluent-bit"} {
		for _, component := range plan.Components {
			if component.Component == stale {
				t.Fatalf("the unselected logging backend %s must not be planned", stale)
			}
		}
	}
	if plan.NewConfigDigest == plan.BaseConfigDigest {
		t.Fatal("adding a component necessarily changes the digest; both must be recorded separately")
	}
	if plan.Invariants["clusterName"] != "ani-lab" || plan.Invariants["networkStack"] != "kcn" || plan.Invariants["nodes"] != "3" {
		t.Fatalf("invariants must be recorded: %+v", plan.Invariants)
	}
	excluded := strings.Join(plan.Excluded, ",")
	for _, want := range []string{"create_cluster", "kubeadm", "CNI", "Ceph", "Envoy"} {
		if !strings.Contains(excluded, want) {
			t.Fatalf("the plan must name the excluded base scope %q: %s", want, excluded)
		}
	}
	if plan.LiveChecks["kubernetesVersion"] != "v1.35.8" {
		t.Fatalf("the live version check must be recorded: %+v", plan.LiveChecks)
	}
	// The whole trajectory is read-only: no mutating kubectl verb ever ran.
	logData, err := os.ReadFile(filepath.Join(filepath.Dir(input.ConfigFile), "kubectl-calls.log"))
	if err != nil {
		t.Fatalf("read kubectl log: %v", err)
	}
	for _, verb := range []string{"apply", "create ", "delete", "patch", "replace", "exec"} {
		if strings.Contains(string(logData), verb) {
			t.Fatalf("the plan must not mutate the cluster (saw %q):\n%s", verb, logData)
		}
	}
	// The new run record is a real run.json with the NEW digest, written into
	// this run's own directory.
	newManifestData, err := os.ReadFile(filepath.Join(outDir, "runs", plan.RunID, "run.json"))
	if err != nil {
		t.Fatalf("read new run.json: %v", err)
	}
	var newManifest RunManifest
	if err := json.Unmarshal(newManifestData, &newManifest); err != nil {
		t.Fatalf("parse new run.json: %v", err)
	}
	if newManifest.ConfigDigest != plan.NewConfigDigest {
		t.Fatal("the new run record must carry the new digest")
	}
}

// T-R15-02: every rejection leaves the output directory empty.
func TestComponentsInstallRejections(t *testing.T) {
	cases := []struct {
		name            string
		componentsBlock string
		only            string
		refuseRegistry  bool
		wantInErr       []string
		mutateBase      func(t *testing.T, input ComponentsInstallInput)
	}{
		{
			name:            "empty only",
			componentsBlock: "  nats: {enabled: true}",
			only:            "",
			wantInErr:       []string{"--only is required"},
		},
		{
			name:            "unknown id",
			componentsBlock: "  nats: {enabled: true}",
			only:            "nginx-ingress",
			wantInErr:       []string{"unknown component id"},
		},
		{
			name:            "deferred id",
			componentsBlock: "  nats: {enabled: true}",
			only:            "milvus",
			wantInErr:       []string{"deferred batch"},
		},
		{
			name:            "component not enabled in config",
			componentsBlock: "  nats: {enabled: false}",
			only:            "nats",
			wantInErr:       []string{"not enabled in the site config"},
		},
		{
			name:            "cluster identity mismatch",
			componentsBlock: "  nats: {enabled: true}",
			only:            "nats",
			wantInErr:       []string{"clusterName"},
			// The base record is broken inside the case via baseRunFile rewrite.
			mutateBase: func(t *testing.T, input ComponentsInstallInput) {
				data, err := os.ReadFile(input.BaseRunFile)
				if err != nil {
					t.Fatalf("read base run: %v", err)
				}
				var manifest RunManifest
				if err := json.Unmarshal(data, &manifest); err != nil {
					t.Fatalf("parse: %v", err)
				}
				manifest.ClusterName = "another-cluster"
				encoded, err := json.MarshalIndent(manifest, "", "  ")
				if err != nil {
					t.Fatalf("encode: %v", err)
				}
				if err := os.WriteFile(input.BaseRunFile, encoded, 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
				// Keep the state consistent with the (foreign) record so the
				// failure under test is the base INVARIANT check, not the
				// record/state agreement check.
				sPath := filepath.Join(filepath.Dir(input.BaseRunFile), "run-state.json")
				stData, err := os.ReadFile(sPath)
				if err != nil {
					t.Fatalf("read base state: %v", err)
				}
				var st InstallState
				if err := json.Unmarshal(stData, &st); err != nil {
					t.Fatal(err)
				}
				st.ClusterName = "another-cluster"
				stEncoded, err := json.MarshalIndent(st, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(sPath, stEncoded, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:            "missing registry image",
			componentsBlock: "  nats: {enabled: true}",
			only:            "nats",
			refuseRegistry:  true,
			wantInErr:       []string{"missing from the registry"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input, outDir := r15PlanFixture(t, tc.componentsBlock, tc.only, tc.refuseRegistry, "absent")
			if tc.mutateBase != nil {
				tc.mutateBase(t, input)
			}
			err := RunComponentsInstallPlan(context.Background(), input, os.Stdout)
			if err == nil {
				t.Fatalf("the case must be rejected")
			}
			for _, want := range tc.wantInErr {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %v, want it to mention %q", err, want)
				}
			}
			// Zero writes: the output directory must not exist, or be empty.
			entries, readErr := os.ReadDir(outDir)
			if readErr != nil {
				if !os.IsNotExist(readErr) {
					t.Fatalf("read out dir: %v", readErr)
				}
			} else if len(entries) != 0 {
				t.Fatalf("a rejected plan must write nothing, found: %v", entries)
			}
		})
	}

	// The storage gate: a storage-backed component without an effective class
	// is refused — the plan never deploys Ceph (white-box: Validate already
	// blocks the whole config in this shape, so the gate is exercised alone).
	t.Run("storage-backed component without a class is refused", func(t *testing.T) {
		site := strings.Replace(r15BaseSite, "registry: {port: 5000}", "registry: {port: 5000}", 1)
		site = strings.Replace(site,
			"storage:\n  enabled: true\n  provider: ceph\n  nodes:\n    - {name: node1, devices: [/dev/disk/by-id/ata-r15-01]}\n    - {name: node2, devices: [/dev/disk/by-id/ata-r15-02]}\n    - {name: node3, devices: [/dev/disk/by-id/ata-r15-03]}\n",
			"", 1)
		site = strings.Replace(site, "components:\n  certManager: {enabled: true}", "components:\n  nats: {enabled: true, storageClass: ani-local}", 1)
		cluster, err := ParseClusterConfig([]byte(site))
		if err != nil {
			t.Fatalf("parse storage-less site: %v", err)
		}
		if err := Validate(cluster); err != nil {
			t.Fatalf("the storage-less site must still be a valid config (external class): %v", err)
		}
		_, err = componentsScope(cluster, []string{"nats"})
		if err == nil || !strings.Contains(err.Error(), "never deploys Ceph") {
			t.Fatalf("the storage gate must refuse nats, got %v", err)
		}
	})

	// T-R15-03: foreign ownership is rejected, never adopted; our own release
	// plans as already_installed.
	t.Run("foreign helm release rejected and our release already_installed", func(t *testing.T) {
		input, _ := r15PlanFixture(t, "  nats: {enabled: true}", "nats", false, "foreign")
		err := RunComponentsInstallPlan(context.Background(), input, os.Stdout)
		if err == nil || !strings.Contains(err.Error(), "adopting or upgrading a foreign release is not supported") {
			t.Fatalf("a foreign release must be rejected, got %v", err)
		}

		input2, outDir := r15PlanFixture(t, "  nats: {enabled: true}", "nats", false, "ours")
		if err := RunComponentsInstallPlan(context.Background(), input2, os.Stdout); err != nil {
			t.Fatalf("our own release must plan cleanly: %v", err)
		}
		plan := r15ReadPlan(t, outDir)
		if plan.Components[0].Status != "already_installed" {
			t.Fatalf("our own release must be recorded already_installed: %+v", plan.Components[0])
		}
		if !strings.Contains(plan.Components[0].Owner, "left untouched") {
			t.Fatalf("already_installed must never upgrade: %+v", plan.Components[0])
		}
	})
}

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
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// r15FakeKubectl answers the read-only queries the plan preflight makes and
// records everything. Knob FAKE_NATS_RELEASE: absent | ours | foreign.
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
case "$args" in
  "version -o json")
    echo '{"serverVersion":{"gitVersion":"v1.35.8"}}'; exit 0 ;;
  *"get namespace"*)
    echo "uid-ns-ani"; exit 0 ;;
  *"get storageclass"*)
    echo "uid-sc-ani"; exit 0 ;;
  *"get secret"*"sh.helm.release.v1.nats.v1"*)
    if [ "${FAKE_NATS_RELEASE:-absent}" = "absent" ]; then
      echo "Error from server (NotFound)" >&2
      exit 1
    fi
    if [ "${FAKE_NATS_RELEASE:-}" = "foreign" ]; then
      cat "${FAKE_FOREIGN_SECRET_FILE:?}"
    else
      cat "${FAKE_NATS_SECRET_FILE:?}"
    fi
    exit 0 ;;
  *"get statefulset postgresql"*)
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

// r15HelmSecret builds a helm release secret JSON for the given chart.
func r15HelmSecret(t *testing.T, dir, chart, version string) string {
	t.Helper()
	release := map[string]any{
		"chart": map[string]any{
			"metadata": map[string]any{"name": chart, "version": version},
		},
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
	secret := map[string]any{
		"data": map[string]string{"release": base64.StdEncoding.EncodeToString([]byte(base64.StdEncoding.EncodeToString(gz.Bytes())))},
	}
	encoded, err := json.Marshal(secret)
	if err != nil {
		t.Fatalf("marshal secret: %v", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("helm-%s-%s.json", chart, version))
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	return path
}

// r15Registry starts a fake registry; refuseAll makes every manifest HEAD 404
// (the missing-material rejection path).
func r15Registry(t *testing.T, refuseAll bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refuseAll {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server
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

	baseSite := r15SiteWithComponents(port, "  certManager: {enabled: true}")
	baseConfig, err := ParseClusterConfig([]byte(baseSite))
	if err != nil {
		t.Fatalf("parse base site: %v", err)
	}
	baseManifest, err := BuildRunManifest(baseConfig)
	if err != nil {
		t.Fatalf("build base manifest: %v", err)
	}
	encodedBase, err := json.MarshalIndent(baseManifest, "", "  ")
	if err != nil {
		t.Fatalf("encode base manifest: %v", err)
	}
	baseRunFile := filepath.Join(baseDir, "base-run.json")
	if err := os.WriteFile(baseRunFile, encodedBase, 0o600); err != nil {
		t.Fatalf("write base run: %v", err)
	}

	// The execution step reads the shipped image table from the artifact
	// root; the fixture copies the real one.
	imagesDir := filepath.Join(baseDir, "images")
	if err := os.MkdirAll(imagesDir, 0o700); err != nil {
		t.Fatalf("mkdir images: %v", err)
	}
	shipped, err := os.ReadFile(filepath.Join("..", "..", "ani", "images.tsv"))
	if err != nil {
		t.Fatalf("read shipped images.tsv: %v", err)
	}
	if err := os.WriteFile(filepath.Join(imagesDir, "images.tsv"), shipped, 0o600); err != nil {
		t.Fatalf("write images.tsv: %v", err)
	}

	if natsRelease != "absent" {
		if natsRelease == "foreign" {
			foreign := r15HelmSecret(t, baseDir, "nginx", "1.0.0")
			t.Setenv("FAKE_FOREIGN_SECRET_FILE", foreign)
		} else {
			ours := r15HelmSecret(t, baseDir, "nats", "2.14.6")
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
	// The new run record is a real run.json with the NEW digest.
	newManifestData, err := os.ReadFile(filepath.Join(outDir, "run", "run.json"))
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

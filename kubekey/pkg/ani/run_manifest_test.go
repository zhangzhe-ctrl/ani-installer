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
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A valid site config with exactly the secrets a real one carries. The secret is
// synthetic and the tests assert it never leaves the input file.
const r06Secret = "r06-secret-password"

const r06SiteA = `name: ani-lab
installerNode: node1
ssh: {user: ubuntu, port: 22, password: r06-secret-password}
nodes:
  - {name: node1, address: 192.0.2.11}
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
    - {name: node1, devices: [/dev/disk/by-id/ata-ani-data-01]}
    - {name: node2, devices: [/dev/disk/by-id/ata-ani-data-02]}
    - {name: node3, devices: [/dev/disk/by-id/ata-ani-data-03]}
components:
  certManager: {enabled: true}
`

// The same configuration, rewritten: quoted values, comments, blank lines and a
// different key order. It must parse, validate and digest identically.
const r06SiteB = `# rewritten by hand: same meaning, different spelling

registry: {port: 5000}   # registry first this time
name: ani-lab
network:
  kcn: {managedDevices: [ens35], encapNetworks: [192.0.2.0/24], intranetNetworks: [192.0.2.0/24, 10.96.0.0/16]}
  podCIDR: 10.16.0.0/16
  serviceCIDR: 10.96.0.0/16
  managementInterface: ens34
  stack: "kcn"           # quoted
nodes:
  - {name: node1, address: 192.0.2.11}
  - {name: node2, address: 192.0.2.12}
  - {name: node3, address: 192.0.2.13}
storage:
  nodes:
    - {name: node1, devices: [/dev/disk/by-id/ata-ani-data-01]}
    - {name: node2, devices: [/dev/disk/by-id/ata-ani-data-02]}
    - {name: node3, devices: [/dev/disk/by-id/ata-ani-data-03]}
  provider: ceph
  enabled: true
components:
  certManager: {enabled: true}
installerNode: node1
ssh: {port: 22, user: ubuntu, password: 'r06-secret-password'}
`

// T-R06-01
func TestEquivalentSiteYAMLFormsAgree(t *testing.T) {
	a, err := ParseClusterConfig([]byte(r06SiteA))
	if err != nil {
		t.Fatalf("parse form A: %v", err)
	}
	if err := Validate(a); err != nil {
		t.Fatalf("validate form A: %v", err)
	}
	b, err := ParseClusterConfig([]byte(r06SiteB))
	if err != nil {
		t.Fatalf("parse form B: %v", err)
	}
	if err := Validate(b); err != nil {
		t.Fatalf("validate form B: %v", err)
	}

	digestA, err := ConfigDigest(a)
	if err != nil {
		t.Fatalf("digest A: %v", err)
	}
	digestB, err := ConfigDigest(b)
	if err != nil {
		t.Fatalf("digest B: %v", err)
	}
	if digestA != digestB {
		t.Fatalf("equivalent configs must share one digest:\nA=%s\nB=%s", digestA, digestB)
	}
	if a.Network.Stack != "kcn" || b.Network.Stack != "kcn" {
		t.Fatalf("quoted and unquoted stack must normalise to kcn, got %q / %q", a.Network.Stack, b.Network.Stack)
	}
}

// T-R06-02
func TestStrictSiteParsingRejectsAmbiguity(t *testing.T) {
	cases := map[string]string{
		"stack typo": strings.Replace(r06SiteA, "stack: kcn", "stack: kcnn", 1),
		"unknown top-level field": strings.Replace(r06SiteA,
			"name: ani-lab", "name: ani-lab\nnetwrok: {}", 1),
		"duplicate key": strings.Replace(r06SiteA,
			"installerNode: node1", "installerNode: node1\nname: other", 1),
		"invalid profile": strings.Replace(r06SiteA,
			"installerNode: node1", "installerNode: node1\nprofile: minimal", 1),
		"second YAML document": r06SiteA + "---\nname: other\n",
		"IPv6 address":         strings.Replace(r06SiteA, "192.0.2.13", "2001:db8::13", 1),
		"installer node is not nodes[0]": strings.Replace(r06SiteA,
			"installerNode: node1", "installerNode: node2", 1),
		"profile base with enabled components": strings.Replace(r06SiteA,
			"installerNode: node1", "installerNode: node1\nprofile: base", 1),
	}
	for name, text := range cases {
		config, err := ParseClusterConfig([]byte(text))
		if err == nil {
			err = Validate(config)
		}
		if err == nil {
			t.Fatalf("%s must be rejected", name)
		}
		if strings.Contains(err.Error(), "kubeovn") && strings.Contains(err.Error(), "default") {
			t.Fatalf("%s: a bad stack must not silently default to kubeovn: %v", name, err)
		}
	}

	// The stack typo names the offending value instead of silently mapping it.
	config, err := ParseClusterConfig([]byte(strings.Replace(r06SiteA, "stack: kcn", "stack: kcnn", 1)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	err = Validate(config)
	if err == nil || !strings.Contains(err.Error(), "kcnn") {
		t.Fatalf("stack typo error = %v, want it to name the value", err)
	}
}

// T-R06-03: an installer node that is not nodes[0] fails validation, and the
// failing run executes nothing and writes nothing.
func TestInstallerNodeMustBeFirstNodeAndRunNothing(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "site.yaml")
	text := strings.Replace(r06SiteA, "installerNode: node1", "installerNode: node2", 1)
	if err := os.WriteFile(configPath, []byte(text), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// PATH contains exactly one recording tool: if RunValidate executed anything
	// at all, the recording file would show it.
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	recorder := filepath.Join(dir, "commands.log")
	if err := os.WriteFile(recorder, nil, 0o600); err != nil {
		t.Fatalf("create recorder: %v", err)
	}
	fake := "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> " + strconv.Quote(recorder) + "\nexit 0\n"
	for _, name := range []string{"kubectl", "hauler", "systemctl", "kk", "sha256sum", "awk", "install"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(fake), 0o700); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}
	outDir := filepath.Join(dir, "out")

	oldPath := os.Getenv("PATH")
	defer func() { _ = os.Setenv("PATH", oldPath) }()
	if err := os.Setenv("PATH", bin); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	err := RunValidate(context.Background(),
		ValidateInput{ConfigFile: configPath, PackageRoot: dir, Output: outDir}, os.Stdout)
	if err == nil {
		t.Fatal("RunValidate must fail when installerNode is not nodes[0]")
	}
	if !strings.Contains(err.Error(), "installerNode") {
		t.Fatalf("error = %v, want it to name installerNode", err)
	}
	if _, statErr := os.Stat(outDir); !os.IsNotExist(statErr) {
		t.Fatal("a rejected config must not create the output directory (zero side effects)")
	}
	if content, readErr := os.ReadFile(recorder); readErr != nil || len(content) != 0 {
		t.Fatalf("a rejected config must execute nothing; recorder=%q readErr=%v", content, readErr)
	}
}

// T-R06-04: the run manifest and the facts file never carry the site secret.
func TestRunManifestCarriesNoSecret(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "site.yaml")
	if err := os.WriteFile(configPath, []byte(r06SiteA), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	outDir := filepath.Join(dir, "out")
	if err := RunValidate(context.Background(),
		ValidateInput{ConfigFile: configPath, PackageRoot: dir, Output: outDir}, os.Stdout); err != nil {
		t.Fatalf("RunValidate: %v", err)
	}

	manifest, err := os.ReadFile(filepath.Join(outDir, "run.json"))
	if err != nil {
		t.Fatalf("read run.json: %v", err)
	}
	facts, err := os.ReadFile(filepath.Join(outDir, "verify-facts.env"))
	if err != nil {
		t.Fatalf("read verify-facts.env: %v", err)
	}
	for _, artifact := range []string{string(manifest), string(facts)} {
		if strings.Contains(artifact, r06Secret) {
			t.Fatalf("a secret leaked into a run artifact:\n%s", artifact)
		}
	}

	var decoded map[string]any
	if err := json.Unmarshal(manifest, &decoded); err != nil {
		t.Fatalf("run.json is not valid JSON: %v", err)
	}
	if got := decoded["networkStack"]; got != "kcn" {
		t.Fatalf("networkStack = %v, want kcn", got)
	}
	if got := decoded["profile"]; got != "full" {
		t.Fatalf("profile = %v, want full", got)
	}
	installer, ok := decoded["installer"].(map[string]any)
	if !ok {
		t.Fatalf("installer = %#v, want an object", decoded["installer"])
	}
	if installer["address"] != "192.0.2.11" || installer["registryPort"] != float64(5000) {
		t.Fatalf("installer = %v, want the site's installer address and registry port", installer)
	}
	components, ok := decoded["components"].([]any)
	if !ok || len(components) != 1 || components[0] != "cert-manager" {
		t.Fatalf("components = %v, want [cert-manager]", decoded["components"])
	}
	if decoded["storageClass"] != "ani-block" {
		t.Fatalf("storageClass = %v, want ani-block", decoded["storageClass"])
	}
	if decoded["materialsValidated"] != false {
		t.Fatal("this build does not validate materials; the manifest must say so")
	}

	// The facts file is shell-sourceable and carries the same non-secret values.
	probed := filepath.Join(dir, "probe.sh")
	probe := "#!/usr/bin/env bash\nsource " + strconv.Quote(filepath.Join(outDir, "verify-facts.env")) +
		"\nprintf '%s' \"$ANI_NETWORK_STACK|$ANI_REGISTRY\"\n"
	if err := os.WriteFile(probed, []byte(probe), 0o700); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	out, err := exec.Command("bash", probed).Output()
	if err != nil {
		t.Fatalf("source facts: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "kcn|192.0.2.11:5000" {
		t.Fatalf("sourced facts = %q, want kcn|192.0.2.11:5000", out)
	}

	// A parse failure must not echo the file content either.
	broken := strings.Replace(r06SiteA, "installerNode: node1",
		"installerNode: node1\nname: other", 1)
	brokenPath := filepath.Join(dir, "broken.yaml")
	if err := os.WriteFile(brokenPath, []byte(broken), 0o600); err != nil {
		t.Fatalf("write broken config: %v", err)
	}
	err = RunValidate(context.Background(),
		ValidateInput{ConfigFile: brokenPath, PackageRoot: dir, Output: ""}, os.Stdout)
	if err == nil {
		t.Fatal("a duplicate key must be rejected")
	}
	if strings.Contains(err.Error(), r06Secret) {
		t.Fatal("the error output must not contain the site secret")
	}
}

package ani

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKubeflowMissingMaterialFailsOnlyWhenSelected(t *testing.T) {
	c := kubeflowTestConfig()
	for _, key := range kubeflowImageKeys() {
		table := r08FullTable(t)
		delete(table, key.Original)
		if _, err := KubeKeyConfig(c, "/offline/packages/kubekey-artifact.tgz", "/offline", table); err == nil || !strings.Contains(err.Error(), key.Original) {
			t.Fatalf("missing selected image %s: %v", key.Original, err)
		}
	}
	c.Kubeflow.Enabled = false
	c.Components.CertManager.Enabled = true
	table := r08FullTable(t)
	for _, key := range kubeflowImageKeys() {
		delete(table, key.Original)
	}
	if _, err := KubeKeyConfig(c, "/offline/packages/kubekey-artifact.tgz", "/offline", table); err != nil {
		t.Fatalf("disabled Kubeflow requires Kubeflow material: %v", err)
	}
}

func TestKubeflowAssetsRejectCrossValidArtifact(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "manifests", "kubeflow", KubeflowRelease)
	if err := os.MkdirAll(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	var approval struct {
		Files map[string]string `json:"files"`
	}
	if err := json.Unmarshal(kubeflowAssetsApproval, &approval); err != nil {
		t.Fatal(err)
	}
	for name := range approval.Files {
		source := filepath.Join("..", "..", "ani", "kubeflow", name)
		if name == "resources.json" || name == "overlay.lock.json" {
			source = filepath.Join("..", "..", "ani", "kubeflow", "overlay", name)
		} else if strings.HasPrefix(name, "probe-") {
			source = filepath.Join("..", "..", "ani", "kubeflow", "probes", strings.TrimPrefix(name, "probe-"))
		} else if name == "requirements.lock" {
			source = filepath.Join("..", "..", "ani", "kubeflow", "execution-image", name)
		}
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bundle, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bundle, "assets.lock.json"), kubeflowAssetsApproval, 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyKubeflowAssets(root); err != nil {
		t.Fatal(err)
	}
	changed := []byte("# unauthorized replacement\n")
	if err := os.WriteFile(filepath.Join(bundle, "resources.py"), changed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyKubeflowAssets(root); err == nil {
		t.Fatal("changed role asset was accepted")
	}
	var forged map[string]any
	if err := json.Unmarshal(kubeflowAssetsApproval, &forged); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(changed)
	forged["files"].(map[string]any)["resources.py"] = hex.EncodeToString(hash[:])
	data, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "assets.lock.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyKubeflowAssets(root); err == nil {
		t.Fatal("regenerated self-consistency lock approved an unauthorized asset")
	}
}

func kubeflowTestConfig() ClusterConfig {
	c := validConfig()
	c.Network.Stack = "kubeovn"
	c.Storage = Storage{Enabled: true, Provider: "ceph", Nodes: []StorageNode{
		{Name: "node1", Devices: []string{"/dev/sdb"}},
		{Name: "node2", Devices: []string{"/dev/sdb"}},
		{Name: "node3", Devices: []string{"/dev/sdb"}},
	}}
	c.ObjectStorage = &ObjectStorage{Provider: "rustfs", RustFS: RustFSStorage{Mode: "standalone", StorageClass: "ani-block", StorageSize: "20Gi"}}
	c.Kubeflow = &KubeflowConfig{Enabled: true, Release: KubeflowRelease, EntryAddress: "192.0.2.11", HTTPPort: 30443, GRPCPort: 30444,
		Database:  KubeflowDatabase{StorageClass: "ani-block", StorageSize: "20Gi"},
		Workspace: KubeflowWorkspace{StorageClass: "ani-cephfs", MaxSize: "5Gi", MaxClaimsPerTenant: 2},
		Tenants:   []string{"ani-kfp-probe-a", "ani-kfp-probe-b"}}
	return c
}

func TestKubeflowSelectionAndDefaultOffDigest(t *testing.T) {
	c := validConfig()
	before, err := ConfigDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	c.Kubeflow = &KubeflowConfig{Enabled: false}
	after, err := ConfigDigest(c)
	if err != nil || before != after {
		t.Fatalf("default-off digest changed: %s -> %s (%v)", before, after, err)
	}
	if aniRoleEnabled("kubeflow", c) {
		t.Fatal("disabled config enabled the role")
	}
	for _, row := range effectiveSelection(c) {
		if row.Name == "kubeflow" {
			t.Fatal("disabled selection adds a Kubeflow row")
		}
	}
	c = kubeflowTestConfig()
	if err := Validate(c); err != nil {
		t.Fatal(err)
	}
	if _, err := KubeKeyConfig(c, "/offline/packages/kubekey-artifact.tgz", "/offline", r08FullTable(t)); err != nil {
		t.Fatal(err)
	}
	if !c.EffectiveComponents().CertManager.Enabled || c.Components.CertManager.Enabled {
		t.Fatal("certificate dependency was not derived without mutating the site")
	}
	manifest, err := BuildRunManifest(c)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Kubeflow == nil || manifest.Components[len(manifest.Components)-1] != "kubeflow" {
		t.Fatal("first-install records omit Kubeflow")
	}
	if _, additionTarget := componentInstallSpecs["kubeflow"]; additionTarget {
		t.Fatal("Kubeflow must not become an addition target")
	}
}

func TestKubeflowRejectsUnsupportedSiteBeforeRendering(t *testing.T) {
	cases := map[string]func(*ClusterConfig){
		"base":                func(c *ClusterConfig) { c.Profile = "base" },
		"version":             func(c *ClusterConfig) { c.Kubeflow.Release = "latest" },
		"entry":               func(c *ClusterConfig) { c.Kubeflow.EntryAddress = "192.0.2.99" },
		"ports":               func(c *ClusterConfig) { c.Kubeflow.GRPCPort = c.Kubeflow.HTTPPort },
		"storage":             func(c *ClusterConfig) { c.Storage.Enabled = false },
		"s3":                  func(c *ClusterConfig) { c.ObjectStorage.Provider = "none" },
		"network":             func(c *ClusterConfig) { c.Network.Stack = "kcn" },
		"tenant":              func(c *ClusterConfig) { c.Kubeflow.Tenants = []string{"default"} },
		"duplicate tenant":    func(c *ClusterConfig) { c.Kubeflow.Tenants = []string{"ani-kfp-a", "ani-kfp-a"} },
		"empty class":         func(c *ClusterConfig) { c.Kubeflow.Workspace.StorageClass = "" },
		"zero storage":        func(c *ClusterConfig) { c.Kubeflow.Workspace.MaxSize = "0Gi" },
		"unbounded retention": func(c *ClusterConfig) { c.Kubeflow.Workspace.MaxClaimsPerTenant = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := kubeflowTestConfig()
			mutate(&c)
			if err := Validate(c); err == nil {
				t.Fatal("unsupported site passed validation")
			}
		})
	}
}

func TestKubeflowRoleIsFirstInstallOnly(t *testing.T) {
	root := filepath.Join("..", "..", "builtin", "core", "playbooks")
	for _, file := range []string{"create_cluster.yaml", ComponentsPlaybookRelPath} {
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		present := strings.Contains(string(data), "role: ani/kubeflow")
		if present != (file == "create_cluster.yaml") {
			t.Fatalf("unexpected Kubeflow role in %s", file)
		}
	}
}

func TestKubeflowUsesProductionRenderContext(t *testing.T) {
	roles := filepath.Join("..", "..", "builtin", "core", "roles", "ani")
	c := kubeflowTestConfig()
	for _, enabled := range []bool{false, true} {
		c.Kubeflow.Enabled = enabled
		// RustFS independently needs the certificate component when Kubeflow is off.
		c.Components.CertManager.Enabled = !enabled
		files, err := RenderSite(roles, c, "/offline", r08FullTable(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateRenderedArtifacts(files); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, file := range files {
			if file.Role == "kubeflow" {
				found = true
			}
			if file.Name == "kubeflow-site.yaml" && !strings.Contains(string(file.Rendered), `"http_port": 30443`) {
				t.Fatal("entry port was not rendered")
			}
		}
		if found != enabled {
			t.Fatalf("Kubeflow rendered=%v, enabled=%v", found, enabled)
		}
	}
}

package ani

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestKubeflowKubeOVNManifestListBinding(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "ani", "kubeovn", "source-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	if "sha256:"+hex.EncodeToString(hash[:]) != KubeOVNImagePin {
		t.Fatal("source manifest list differs from approved pin")
	}
	var index struct {
		Manifests []struct {
			Digest   string
			Platform struct {
				OS           string
				Architecture string
			}
		}
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	selected := ""
	for _, m := range index.Manifests {
		if m.Platform.OS == "linux" && m.Platform.Architecture == "amd64" {
			if selected != "" {
				t.Fatal("ambiguous amd64 platform")
			}
			selected = m.Digest
		}
	}
	if selected != KubeOVNAMD64ManifestDigest {
		t.Fatal("source list does not bind the declared running manifest")
	}
	c := kubeflowTestConfig()
	files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), c, "/offline", r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Name == "kubeflow-site.yaml" {
			var site map[string]any
			if err := json.Unmarshal(file.Rendered, &site); err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(site["network_image"].(string), "@"+selected) {
				t.Fatal("runtime site binds the source list instead of its actual platform")
			}
			return
		}
	}
	t.Fatal("enabled runtime site missing")
}

func TestKubeflowKCNTestEnvironmentCapability(t *testing.T) {
	c := kubeflowTestConfig()
	c.Network.Stack = "kcn"
	if err := Validate(c); err == nil {
		t.Fatal("KCN Kubeflow must require the explicit test-environment capability contract")
	}
	if err := yaml.Unmarshal([]byte("networkPolicy: kcn-test-unsupported-v1\n"), c.Kubeflow); err != nil {
		t.Fatal(err)
	}
	if err := Validate(c); err != nil {
		t.Fatalf("accepted KCN test environment cannot reach the first-install chain: %v", err)
	}
	spec := kubeflowSpec(c)
	if spec["network_stack"] != "kcn" || spec["network_policy"] != "unsupported" || spec["network_policy_contract"] != "kcn-test-unsupported-v1" {
		t.Fatalf("runtime capability differs from selected provider: %v", spec)
	}
	c.Network.Stack = "kubeovn"
	if err := Validate(c); err == nil {
		t.Fatal("KCN unsupported acknowledgement must not weaken Kube-OVN isolation")
	}
}

func TestKubeflowKubeOVNCapabilityRemainsRequired(t *testing.T) {
	c := kubeflowTestConfig()
	if err := Validate(c); err != nil {
		t.Fatal(err)
	}
	spec := kubeflowSpec(c)
	if spec["network_stack"] != "kubeovn" || spec["network_policy"] != "required" || spec["network_policy_contract"] != "kubeovn-required-v1" {
		t.Fatalf("Kube-OVN isolation contract absent: %v", spec)
	}
}

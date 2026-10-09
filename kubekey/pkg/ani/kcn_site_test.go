package ani

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestKCNCompleteSiteRendering(t *testing.T) {
	for _, multus := range []bool{false, true} {
		c := validConfig()
		c.Network.KCN.ManagedDevices = nil
		c.Network.PodCIDR = "10.42.0.0/16"
		c.Network.Multus = MultusNetwork{Enabled: multus}
		if multus {
			c.Network.Multus.TestCIDR = "10.240.0.0/24"
		}
		if err := Validate(c); err != nil {
			t.Fatalf("overlay-only KCN should work without adopting a host NIC: %v", err)
		}
		files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), c, "/offline", r08FullTable(t))
		if err != nil {
			t.Fatal(err)
		}
		crds, workloads := 0, 0
		basic, config := false, false
		for _, file := range files {
			if file.Role != "kcn" || filepath.Ext(file.Name) != ".yaml" || file.Name == "kcn-tasks-main.yaml" {
				continue
			}
			decoder := yaml.NewDecoder(bytes.NewReader(file.Rendered))
			for {
				var value map[string]any
				if err := decoder.Decode(&value); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				meta := value["metadata"].(map[string]any)
				switch value["kind"] {
				case "CustomResourceDefinition":
					crds++
					basic = basic || meta["name"] == "basicnetworkisolations.networking.kubercloud.com"
				case "ConfigMap":
					data := value["data"].(map[string]any)
					want := "false"
					if multus {
						want = "true"
					}
					if data["hasMultusCNI"] != want || data["managedDevices"] != "" {
						t.Fatalf("site CNI contract differs: %v", data)
					}
					config = true
				case "Deployment", "DaemonSet":
					workloads++
					if !bytes.Contains(file.Rendered, []byte("--default-cidr=10.42.0.0/16")) || !bytes.Contains(file.Rendered, []byte("KUBERNETES_SERVICE_HOST")) {
						t.Fatal("KCN pod CIDR/bootstrap missing from runtime")
					}
				}
			}
		}
		if crds != 17 || !basic || workloads != 4 || !config {
			t.Fatalf("incomplete KCN material: crds=%d basic=%v workloads=%d config=%v", crds, basic, workloads, config)
		}
	}
}

func TestKCNRefusesManagementNICAdoption(t *testing.T) {
	c := validConfig()
	c.Network.KCN.ManagedDevices = []string{c.Network.ManagementInterface}
	if err := Validate(c); err == nil {
		t.Fatal("management NIC cannot be adopted as underlay")
	}
}

func TestKCNFullSiteConfigurationRenders(t *testing.T) {
	data, err := os.ReadFile("../../../config/examples/kcn-full.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseClusterConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), c, "/offline", r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRenderedArtifacts(files); err != nil {
		t.Fatal(err)
	}
	selected := map[string]bool{}
	for _, row := range effectiveSelection(c) {
		selected[row.Name] = row.Enabled
	}
	for _, name := range []string{"cert-manager", "postgresql", "valkey", "nats", "metrics", "opensearch", "fluent-bit", "rustfs", "milvus", "metrics-server", "snapshot-controller", "kubevirt", "volcano", "harbor", "kubeflow"} {
		if !selected[name] {
			t.Fatalf("complete first-install selection lost %s", name)
		}
	}
	if selected["loki"] || selected["rgw"] {
		t.Fatal("excluded backend selected")
	}
}

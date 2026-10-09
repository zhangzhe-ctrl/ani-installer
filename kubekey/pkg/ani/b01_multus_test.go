package ani

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestB01MultusAttachmentUsesSupportedProviderContract(t *testing.T) {
	for _, stack := range []string{"kcn", "kubeovn"} {
		t.Run(stack, func(t *testing.T) {
			text := strings.Replace(r06SiteA, "  stack: kcn", "  stack: "+stack+"\n  multus: {enabled: true, testCIDR: 10.250.0.0/24}", 1)
			c, err := ParseClusterConfig([]byte(text))
			if err != nil {
				t.Fatal(err)
			}
			files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), c, "/opt/ani", r08FullTable(t))
			if err != nil {
				t.Fatal(err)
			}
			objects := map[string]map[string]any{}
			probe := ""
			for _, file := range files {
				if file.Rel == "multus/templates/verify.sh" {
					probe = string(file.Rendered)
				}
				if file.Rel != "multus/templates/test-nad.yaml" {
					continue
				}
				for _, document := range strings.Split(string(file.Rendered), "---") {
					var object map[string]any
					if err := yaml.Unmarshal([]byte(document), &object); err != nil {
						t.Fatal(err)
					}
					if object != nil {
						objects[object["kind"].(string)] = object
					}
				}
			}
			nad, ok := objects["NetworkAttachmentDefinition"]
			if !ok {
				t.Fatal("attachment NAD missing")
			}
			var config map[string]any
			if err := json.Unmarshal([]byte(nad["spec"].(map[string]any)["config"].(string)), &config); err != nil {
				t.Fatal(err)
			}
			if stack == "kcn" {
				if config["type"] != "kc-networking" || config["server_socket"] != "/run/openvswitch/kc-networking-daemon.sock" {
					t.Fatal("KCN controller cannot reconcile this attachment configuration")
				}
				for _, kind := range []string{"VPC", "Subnet"} {
					value, ok := objects[kind]
					if !ok {
						t.Fatalf("KCN attachment %s missing", kind)
					}
					if value["metadata"].(map[string]any)["namespace"] != "ani-platform" || value["spec"].(map[string]any)["cidrBlock"] != "10.250.0.0/24" {
						t.Fatalf("incorrect %s scope or CIDR", kind)
					}
				}
				if objects["Subnet"]["spec"].(map[string]any)["gateway"] != "ani-platform/ani-b01-vpc" || !strings.Contains(probe, "net1.networking.kubercloud.com/subnet: ani-platform/ani-b01-secondary") {
					t.Fatal("secondary interface is not bound to its declared VPC subnet")
				}
			} else if config["type"] != "bridge" || len(objects) != 1 || config["ipam"].(map[string]any)["subnet"] != "10.250.0.0/24" {
				t.Fatal("Kube-OVN node-local bridge contract changed")
			}
		})
	}
}

func TestB01MultusProductionSelectionAndRender(t *testing.T) {
	off, err := ParseClusterConfig([]byte(r06SiteA))
	if err != nil {
		t.Fatal(err)
	}
	if aniRoleEnabled("multus", off) {
		t.Fatal("Multus defaulted on")
	}
	onText := strings.Replace(r06SiteA, "  stack: kcn", "  stack: kcn\n  multus: {enabled: true, testCIDR: 10.250.0.0/24}", 1)
	on, err := ParseClusterConfig([]byte(onText))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(on); err != nil {
		t.Fatal(err)
	}
	if !aniRoleEnabled("multus", on) {
		t.Fatal("selected Multus role disabled")
	}
	table := r08FullTable(t)
	delete(table, "ghcr.io/k8snetworkplumbingwg/multus-cni:v4.3.1-thick")
	if _, err := KubeKeyConfig(on, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", table); err == nil || !strings.Contains(err.Error(), "multus-cni:v4.3.1-thick") {
		t.Fatalf("missing Multus image accepted: %v", err)
	}
	files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), on, "/opt/ani", r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range files {
		if f.Role != "multus" {
			continue
		}
		seen[f.Rel] = true
		if f.Rel != "multus/tasks/main.yaml" && strings.Contains(string(f.Rendered), "<no value>") {
			t.Errorf("unbound %s", f.Rel)
		}
		if f.Rel == "multus/templates/resources.yaml" {
			for _, want := range []string{"ani-multus", "v4.3.1-thick", "/hostroot", "/host/run/multus/"} {
				if !strings.Contains(string(f.Rendered), want) {
					t.Errorf("missing %s", want)
				}
			}
			if strings.Contains(string(f.Rendered), "snapshot-thick") {
				t.Error("quickstart image leaked")
			}
		}
	}
	for _, rel := range []string{"tasks/main.yaml", "templates/preflight-node.sh", "templates/preflight-global.sh", "templates/nad-crd.yaml", "templates/resources.yaml", "templates/test-nad.yaml", "templates/verify.sh"} {
		if !seen["multus/"+rel] {
			t.Errorf("missing %s", rel)
		}
	}
	if err := ValidateRenderedArtifacts(files); err != nil {
		t.Fatal(err)
	}
}

func TestB01MultusRejectsMissingOrConflictingTestNetwork(t *testing.T) {
	for _, cidr := range []string{"", "10.16.0.0/24", "10.96.0.0/16", "192.0.2.0/24", "10.250.0.0/31", "bad"} {
		site := strings.Replace(r06SiteA, "  stack: kcn", "  stack: kcn\n  multus: {enabled: true, testCIDR: '"+cidr+"'}", 1)
		c, err := ParseClusterConfig([]byte(site))
		if err != nil {
			t.Fatal(err)
		}
		if err := Validate(c); err == nil {
			t.Errorf("accepted testCIDR=%q", cidr)
		}
	}
}

func TestB01MultusCannotBeSilentlyAddedByComponentsRun(t *testing.T) {
	base, err := ParseClusterConfig([]byte(r06SiteA))
	if err != nil {
		t.Fatal(err)
	}
	changedText := strings.Replace(r06SiteA, "  stack: kcn", "  stack: kcn\n  multus: {enabled: true, testCIDR: 10.250.0.0/24}", 1)
	changed, err := ParseClusterConfig([]byte(changedText))
	if err != nil {
		t.Fatal(err)
	}
	before, err := BuildRunManifest(base)
	if err != nil {
		t.Fatal(err)
	}
	after, err := BuildRunManifest(changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compareBaseInvariants(before, after); err == nil || !strings.Contains(err.Error(), "networkMultus") {
		t.Fatalf("network CNI wrapper drift accepted: %v", err)
	}
}

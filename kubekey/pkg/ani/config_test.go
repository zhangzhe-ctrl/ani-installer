package ani

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func validConfig() ClusterConfig {
	return ClusterConfig{
		Name:          "ani-lab",
		InstallerNode: "node1",
		SSH:           SSHConfig{User: "ubuntu", Port: 22, Password: "site-password"},
		Nodes: []NodeConfig{
			{Name: "node1", Address: "192.0.2.11"},
			{Name: "node2", Address: "192.0.2.12"},
			{Name: "node3", Address: "192.0.2.13"},
		},
		Network: Network{
			ManagementInterface: "ens34",
			PodCIDR:             "10.16.0.0/16",
			ServiceCIDR:         "10.96.0.0/16",
			KCN: KCN{
				ManagedDevices:   []string{"ens35"},
				EncapNetworks:    []string{"192.0.2.0/24"},
				IntranetNetworks: []string{"192.0.2.0/24", "10.96.0.0/16"},
			},
		},
		RegistryConfig: Registry{Port: 5000},
	}
}

func TestValidateAndRegistryAddress(t *testing.T) {
	c := validConfig()
	if err := Validate(c); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	got, err := c.RegistryAddress()
	if err != nil {
		t.Fatalf("RegistryAddress() error = %v", err)
	}
	if got != "192.0.2.11:5000" {
		t.Fatalf("RegistryAddress() = %q, want 192.0.2.11:5000", got)
	}
	c.Nodes = c.Nodes[:2]
	if err := Validate(c); err == nil {
		t.Fatal("Validate() unexpectedly accepted two nodes")
	}

	c = validConfig()
	c.Name = "bad name"
	if err := Validate(c); err == nil {
		t.Fatal("Validate() unexpectedly accepted an unsafe cluster name")
	}
	c.Name = "../escape"
	if err := Validate(c); err == nil {
		t.Fatal("Validate() unexpectedly accepted a path traversal cluster name")
	}
}

func TestKubeKeyInventoryUsesLocalOnlyForInstaller(t *testing.T) {
	spec, err := KubeKeyInventory(validConfig())
	if err != nil {
		t.Fatalf("KubeKeyInventory() error = %v", err)
	}
	hosts := spec["hosts"].(map[string]any)
	for _, name := range []string{"node1", "node2", "node3"} {
		host := hosts[name].(map[string]any)
		connector := host["connector"].(map[string]any)
		want := "ssh"
		if name == "node1" {
			want = "local"
		}
		if connector["type"] != want {
			t.Fatalf("%s connector type = %v, want %s", name, connector["type"], want)
		}
	}
	if spec["groups"].(map[string]any)["image_registry"] != nil {
		t.Fatal("image_registry group must not be generated")
	}
	for _, name := range []string{"node2", "node3"} {
		connector := hosts[name].(map[string]any)["connector"].(map[string]any)
		if connector["password"] != "site-password" {
			t.Fatalf("%s password connector = %v", name, connector["password"])
		}
		if _, exists := connector["private_key"]; exists {
			t.Fatalf("%s unexpectedly generated private_key", name)
		}
	}
	if _, exists := hosts["node1"].(map[string]any)["connector"].(map[string]any)["password"]; exists {
		t.Fatal("installer local connector unexpectedly generated password")
	}
	localConnector := hosts["node1"].(map[string]any)["connector"].(map[string]any)
	if localConnector["user"] != "ubuntu" {
		t.Fatalf("installer local connector user = %v, want ubuntu", localConnector["user"])
	}
}

func TestKubeKeyConfigOfflineAndNetworkValues(t *testing.T) {
	spec, err := KubeKeyConfig(validConfig(), "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", testImageTable())
	if err != nil {
		t.Fatalf("KubeKeyConfig() error = %v", err)
	}
	etcd := spec["etcd"].(map[string]any)
	if etcd["deployment_type"] != "internal" || etcd["etcd_version"] != "v3.6.6" {
		t.Fatalf("etcd config = %#v", etcd)
	}
	etcdImage := etcd["image"].(map[string]any)
	if etcdImage["registry"] != "192.0.2.11:5000" || etcdImage["repository"] != "kubernetes" || etcdImage["tag"] != "v3.6.6" {
		t.Fatalf("etcd image config = %#v", etcdImage)
	}
	criSpec := spec["cri"].(map[string]any)
	if criSpec["containerd_version"] != "v2.3.4" {
		t.Fatal("runtime containerd version must match packaged artifact")
	}
	if criSpec["runc_version"] != "v1.4.3" {
		t.Fatalf("runtime runc version = %#v, want v1.4.3", criSpec["runc_version"])
	}
	download := spec["download"].(map[string]any)
	if download["fetch"] != false || download["artifact_file"] != "/opt/ani/packages/kubekey-artifact.tgz" {
		t.Fatalf("offline download config = %#v", download)
	}
	cri := spec["cri"].(map[string]any)["registry"].(map[string]any)
	want := []string{"192.0.2.11:5000"}
	if !reflect.DeepEqual(cri["insecure_registries"], want) {
		t.Fatalf("insecure_registries = %#v", cri["insecure_registries"])
	}
	cni := spec["cni"].(map[string]any)
	if cni["type"] != "none" || cni["multi_cni"] != "none" || cni["pod_cidr"] != "10.16.0.0/16" {
		t.Fatalf("cni config = %#v", cni)
	}
	ani := spec["ani"].(map[string]any)
	if got := ani["node_addresses"]; !reflect.DeepEqual(got, []string{"192.0.2.11", "192.0.2.12", "192.0.2.13"}) {
		t.Fatalf("node_addresses = %#v", got)
	}
	network := ani["network"].(map[string]any)
	kcn := network["kcn"].(map[string]any)
	if got := kcn["managedDevices"]; !reflect.DeepEqual(got, []string{"ens35"}) {
		t.Fatalf("managedDevices = %#v", got)
	}
	if got := kcn["encapNetworks"]; !reflect.DeepEqual(got, []string{"192.0.2.0/24"}) {
		t.Fatalf("encapNetworks = %#v", got)
	}
	if network["service_cidr"] != "10.96.0.0/16" {
		t.Fatalf("service_cidr = %v, want 10.96.0.0/16", network["service_cidr"])
	}
}

// testImageTable lists the images the component roles read. It holds the same
// entries the packaged TSV does, so a role that starts reading a new image
// fails here rather than rendering an empty field the chart would happily
// concatenate.
func testImageTable() ImageTable {
	table := ImageTable{
		"docker.changqingyun.cn/kubercloud/gateway:v1.8.3": {
			Original:  "docker.changqingyun.cn/kubercloud/gateway:v1.8.3",
			HaulerRef: "127.0.0.1:5000/kubercloud/gateway:v1.8.3",
			Digest:    "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			Use:       "Envoy Gateway",
		},
	}
	for _, key := range componentImageKeys() {
		table[key.Original] = Image{
			Original:  key.Original,
			HaulerRef: "127.0.0.1:5000/" + strings.TrimPrefix(key.Original, strings.SplitN(key.Original, "/", 2)[0]+"/"),
			Digest:    "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			Use:       key.Group + "/" + key.Name,
		}
	}
	return table
}

// TestKubeKeyConfigBaseProfileSkipsComponentImages guards the base-mode
// install: the kubeovn artifact ships no component chart images, so a base
// profile must build image_parts from the lab images alone instead of failing
// on the first absent metrics or logs reference.
func TestKubeKeyConfigBaseProfileSkipsComponentImages(t *testing.T) {
	baseTable := ImageTable{}
	for _, key := range componentImageKeys() {
		if key.Group != "lab" {
			continue
		}
		baseTable[key.Original] = Image{
			Original:  key.Original,
			HaulerRef: "127.0.0.1:5000/library/" + key.Name + ":test",
			Digest:    "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			Use:       key.Group + "/" + key.Name,
		}
	}

	c := validConfig()
	c.Profile = "base"
	spec, err := KubeKeyConfig(c, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", baseTable)
	if err != nil {
		t.Fatalf("KubeKeyConfig() error = %v", err)
	}
	parts := spec["ani"].(map[string]any)["image_parts"].(map[string]any)
	if len(parts) != 1 {
		t.Fatalf("base profile image_parts groups = %#v, want lab only", parts)
	}
	if _, ok := parts["lab"]; !ok {
		t.Fatalf("base profile image_parts missing the lab group: %#v", parts)
	}

	// The full profile keeps the same strictness for the components the site
	// actually enables: an enabled metrics stack without its images must fail.
	c = validConfig()
	c.Components.Metrics.Enabled = true
	if _, err := KubeKeyConfig(c, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", baseTable); err == nil {
		t.Fatal("full profile unexpectedly accepted an enabled metrics stack without its images")
	}
}

// TestInstallerNodeMustBeTheFirstNode pins the R06 contract: installerNode is
// nodes[0] by definition, so a config that puts the installer on another node is
// ambiguous about where the run happens and is rejected. The registry helper
// itself still follows the installer node for a valid config.
func TestInstallerNodeMustBeTheFirstNode(t *testing.T) {
	c := validConfig()
	c.InstallerNode = "node3"
	err := Validate(c)
	if err == nil {
		t.Fatal("Validate() must reject installerNode != nodes[0]")
	}
	if !strings.Contains(err.Error(), "node1") {
		t.Fatalf("error = %v, want it to name the expected first node", err)
	}

	valid := validConfig()
	got, err := valid.RegistryAddress()
	if err != nil || got != "192.0.2.11:5000" {
		t.Fatalf("RegistryAddress() = %q, want 192.0.2.11:5000", got)
	}
	spec, err := KubeKeyInventory(valid)
	if err != nil {
		t.Fatalf("KubeKeyInventory() error = %v", err)
	}
	hosts := spec["hosts"].(map[string]any)
	for _, name := range []string{"node1", "node2", "node3"} {
		connector := hosts[name].(map[string]any)["connector"].(map[string]any)
		want := "ssh"
		if name == "node1" {
			want = "local"
		}
		if connector["type"] != want {
			t.Fatalf("connector type for %s = %v, want %s", name, connector["type"], want)
		}
	}
}

func TestKubeadmEtcdTemplateUsesConfiguredRepository(t *testing.T) {
	templates := []string{
		"../../builtin/core/roles/kubernetes/init-kubernetes/templates/kubeadm/kubeadm-init.v1beta3",
		"../../builtin/core/roles/kubernetes/init-kubernetes/templates/kubeadm/kubeadm-init.v1beta4",
	}
	for _, name := range templates {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		want := "imageRepository: {{ .etcd.image.registry }}/{{ .etcd.image.repository }}"
		if !strings.Contains(string(data), want) {
			t.Fatalf("%s must render etcd registry and repository", name)
		}
	}
}

func TestValidateRequiresEncapNetworksInIntranetNetworks(t *testing.T) {
	c := validConfig()
	c.Network.KCN.IntranetNetworks = []string{"10.96.0.0/16"}
	if err := Validate(c); err == nil {
		t.Fatal("Validate() unexpectedly accepted an encap network missing from intranetNetworks")
	}
	c.Network.KCN.IntranetNetworks = []string{"192.0.2.0/24", "10.96.0.0/16"}
	if err := Validate(c); err != nil {
		t.Fatalf("Validate() with all encap networks in intranetNetworks = %v", err)
	}
}

func TestANIKCNConfiguresReversePathFilter(t *testing.T) {
	data, err := os.ReadFile("../../builtin/core/roles/ani/kcn/tasks/main.yaml")
	if err != nil {
		t.Fatalf("read kcn tasks: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"net.ipv4.conf.default.rp_filter=0",
		"net.ipv4.conf.ovn0.rp_filter=0",
		`delegate_to: "{{ .item }}"`,
		`loop: "{{ .groups.k8s_cluster | default list | toJson }}"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("kcn tasks missing %q", want)
		}
	}
}

func TestANITasksAvoidUnsupportedFileModule(t *testing.T) {
	tasks := []string{
		"../../builtin/core/roles/ani/kcn/tasks/main.yaml",
		"../../builtin/core/roles/ani/smoke/tasks/main.yaml",
	}
	for _, name := range tasks {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "file:" {
				t.Fatalf("%s uses the unsupported file module", name)
			}
		}
	}
}

// TestKubeKeyConfigFollowsTheInstallerNode keeps the substance of the old
// order test under the R06 contract: the rendered config points at the
// installer node's registry, and the installer node is nodes[0].
func TestKubeKeyConfigFollowsTheInstallerNode(t *testing.T) {
	c := validConfig()
	spec, err := KubeKeyConfig(c, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", testImageTable())
	if err != nil {
		t.Fatalf("KubeKeyConfig() error = %v", err)
	}
	cri := spec["cri"].(map[string]any)["registry"].(map[string]any)
	want := []string{"192.0.2.11:5000"}
	if !reflect.DeepEqual(cri["insecure_registries"], want) {
		t.Fatalf("insecure_registries = %#v, want %#v", cri["insecure_registries"], want)
	}
	ani := spec["ani"].(map[string]any)
	images := ani["images"].(map[string]string)
	original := "docker.changqingyun.cn/kubercloud/gateway:v1.8.3"
	if got := images[original]; got != "192.0.2.11:5000/kubercloud/gateway:v1.8.3" {
		t.Fatalf("image reference = %q, want 192.0.2.11:5000/kubercloud/gateway:v1.8.3", got)
	}
}

func TestNetworkStackSelection(t *testing.T) { // The default (empty stack) is the self-developed kcn batch: kcn fields
	// stay required and the rendered map normalizes to "kcn".
	c := validConfig()
	if err := Validate(c); err != nil {
		t.Fatalf("Validate() with empty stack = %v", err)
	}
	kk, err := KubeKeyConfig(c, "/tmp/ani-artifact.tgz", "/opt/ani-installer/artifacts", testImageTable())
	if err != nil {
		t.Fatalf("KubeKeyConfig() = %v", err)
	}
	network := kk["ani"].(map[string]any)["network"].(map[string]any)
	if network["stack"] != "kcn" {
		t.Fatalf("stack = %v, want normalized kcn", network["stack"])
	}

	// An explicit kubeovn stack skips every kcn-specific validation, so an
	// existing site file can switch stacks without touching network.kcn.
	kubeovn := validConfig()
	kubeovn.Network.Stack = "kubeovn"
	kubeovn.Network.KCN = KCN{}
	if err := Validate(kubeovn); err != nil {
		t.Fatalf("Validate() with kubeovn stack = %v", err)
	}

	// Anything else is rejected.
	bogus := validConfig()
	bogus.Network.Stack = "calico"
	if err := Validate(bogus); err == nil {
		t.Fatal("Validate() unexpectedly accepted stack=calico")
	}
}

func TestInstallProfileSelection(t *testing.T) {
	// Empty profile normalizes to "full" in the rendered map.
	c := validConfig()
	if err := Validate(c); err != nil {
		t.Fatalf("Validate() with empty profile = %v", err)
	}
	kk, err := KubeKeyConfig(c, "/tmp/ani-artifact.tgz", "/opt/ani-installer/artifacts", testImageTable())
	if err != nil {
		t.Fatalf("KubeKeyConfig() = %v", err)
	}
	if got := kk["ani"].(map[string]any)["profile"]; got != "full" {
		t.Fatalf("profile = %v, want normalized full", got)
	}

	// base is accepted and passes through.
	base := validConfig()
	base.Profile = "base"
	if err := Validate(base); err != nil {
		t.Fatalf("Validate() with base profile = %v", err)
	}

	// Anything else is rejected.
	bogus := validConfig()
	bogus.Profile = "minimal"
	if err := Validate(bogus); err == nil {
		t.Fatal("Validate() unexpectedly accepted profile=minimal")
	}
}

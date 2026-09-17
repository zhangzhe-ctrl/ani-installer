package ani

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// ClusterConfig is the small site-specific input used by "kk ani install".
// It intentionally does not describe components or versions; those are fixed
// by the offline artifact.
type ClusterConfig struct {
	Name           string       `yaml:"name"`
	InstallerNode  string       `yaml:"installerNode"`
	SSH            SSHConfig    `yaml:"ssh"`
	Nodes          []NodeConfig `yaml:"nodes"`
	Network        Network      `yaml:"network"`
	RegistryConfig Registry     `yaml:"registry"`
}

type SSHConfig struct {
	User       string `yaml:"user"`
	Port       int    `yaml:"port"`
	Password   string `yaml:"password"`
	PrivateKey string `yaml:"privateKey"`
}

type NodeConfig struct {
	Name    string `yaml:"name"`
	Address string `yaml:"address"`
}

type Network struct {
	ManagementInterface string `yaml:"managementInterface"`
	PodCIDR             string `yaml:"podCIDR"`
	ServiceCIDR         string `yaml:"serviceCIDR"`
	KCN                 KCN    `yaml:"kcn"`
}

type KCN struct {
	ManagedDevices   []string `yaml:"managedDevices"`
	EncapNetworks    []string `yaml:"encapNetworks"`
	IntranetNetworks []string `yaml:"intranetNetworks"`
}

type Registry struct {
	Port int `yaml:"port"`
}

func (c ClusterConfig) Installer() (NodeConfig, error) {
	for _, n := range c.Nodes {
		if n.Name == c.InstallerNode {
			return n, nil
		}
	}
	return NodeConfig{}, fmt.Errorf("installerNode %q is not one of nodes", c.InstallerNode)
}

func (c ClusterConfig) RegistryAddress() (string, error) {
	n, err := c.Installer()
	if err != nil {
		return "", err
	}
	if c.RegistryConfig.Port <= 0 || c.RegistryConfig.Port > 65535 {
		return "", fmt.Errorf("registry.port must be between 1 and 65535")
	}
	return net.JoinHostPort(n.Address, strconv.Itoa(c.RegistryConfig.Port)), nil
}

func Validate(c ClusterConfig) error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.ContainsAny(c.Name, " \t/\\") || c.Name == "." || c.Name == ".." {
		return fmt.Errorf("name must be a safe path component")
	}
	if len(c.Nodes) != 3 {
		return fmt.Errorf("exactly 3 nodes are required, got %d", len(c.Nodes))
	}
	if _, err := c.Installer(); err != nil {
		return err
	}
	names := map[string]struct{}{}
	addresses := map[string]struct{}{}
	for _, n := range c.Nodes {
		if strings.TrimSpace(n.Name) == "" {
			return fmt.Errorf("each node requires a name")
		}
		if net.ParseIP(n.Address) == nil {
			return fmt.Errorf("node %q address %q is not an IPv4/IPv6 address", n.Name, n.Address)
		}
		if _, exists := names[n.Name]; exists {
			return fmt.Errorf("duplicate node name %q", n.Name)
		}
		if _, exists := addresses[n.Address]; exists {
			return fmt.Errorf("duplicate node address %q", n.Address)
		}
		names[n.Name] = struct{}{}
		addresses[n.Address] = struct{}{}
	}
	if strings.TrimSpace(c.SSH.User) == "" {
		return fmt.Errorf("ssh.user is required")
	}
	if c.SSH.Port <= 0 || c.SSH.Port > 65535 {
		return fmt.Errorf("ssh.port must be between 1 and 65535")
	}
	if strings.TrimSpace(c.SSH.Password) == "" && strings.TrimSpace(c.SSH.PrivateKey) == "" {
		return fmt.Errorf("ssh.password or ssh.privateKey is required")
	}
	if strings.TrimSpace(c.SSH.PrivateKey) != "" && !strings.HasPrefix(c.SSH.PrivateKey, "/") {
		return fmt.Errorf("ssh.privateKey must be an absolute path on the installer node")
	}
	if strings.TrimSpace(c.Network.ManagementInterface) == "" {
		return fmt.Errorf("network.managementInterface is required")
	}
	if _, _, err := net.ParseCIDR(c.Network.PodCIDR); err != nil {
		return fmt.Errorf("network.podCIDR %q is invalid: %w", c.Network.PodCIDR, err)
	}
	if _, _, err := net.ParseCIDR(c.Network.ServiceCIDR); err != nil {
		return fmt.Errorf("network.serviceCIDR %q is invalid: %w", c.Network.ServiceCIDR, err)
	}
	if len(c.Network.KCN.ManagedDevices) == 0 {
		return fmt.Errorf("network.kcn.managedDevices is required")
	}
	if len(c.Network.KCN.EncapNetworks) == 0 {
		return fmt.Errorf("network.kcn.encapNetworks is required")
	}
	if len(c.Network.KCN.IntranetNetworks) == 0 {
		return fmt.Errorf("network.kcn.intranetNetworks is required")
	}
	for _, cidr := range append(c.Network.KCN.EncapNetworks, c.Network.KCN.IntranetNetworks...) {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("kcn network %q is invalid: %w", cidr, err)
		}
	}
	if _, err := c.RegistryAddress(); err != nil {
		return err
	}

	intranetNetworks := make(map[string]struct{}, len(c.Network.KCN.IntranetNetworks))
	for _, network := range c.Network.KCN.IntranetNetworks {
		if _, ipnet, err := net.ParseCIDR(network); err == nil {
			intranetNetworks[ipnet.String()] = struct{}{}
		}
	}
	for _, network := range c.Network.KCN.EncapNetworks {
		_, ipnet, err := net.ParseCIDR(network)
		if err != nil {
			return fmt.Errorf("kcn network %q is invalid: %w", network, err)
		}
		if _, ok := intranetNetworks[ipnet.String()]; !ok {
			return fmt.Errorf("network.kcn.intranetNetworks must include each encap network so KCN can route host traffic: %s", ipnet.String())
		}
	}
	return nil
}

// VerifyInstallerInterface confirms that the installer management IP in the
// site config is actually assigned to the configured local interface.
func VerifyInstallerInterface(c ClusterConfig) error {
	n, err := c.Installer()
	if err != nil {
		return err
	}
	iface, err := net.InterfaceByName(c.Network.ManagementInterface)
	if err != nil {
		return fmt.Errorf("management interface %q not found on installer: %w", c.Network.ManagementInterface, err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return err
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.Equal(net.ParseIP(n.Address)) {
			return nil
		}
	}
	return fmt.Errorf("installer address %q is not assigned to interface %q", n.Address, c.Network.ManagementInterface)
}

// VerifySSHAuth is only called on the installer. It never logs credentials.
func VerifySSHAuth(c ClusterConfig) error {
	if strings.TrimSpace(c.SSH.Password) != "" {
		return nil
	}
	info, err := os.Stat(c.SSH.PrivateKey)
	if err != nil {
		return fmt.Errorf("ssh private key is not readable on the installer node")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("ssh private key permissions are too open (maximum 0600)")
	}
	return nil
}

// KubeKeyConfig returns a KubeKey Config spec as a plain map. Keeping it as a
// map lets the installer emit the same format as the upstream YAML template.
func KubeKeyConfig(c ClusterConfig, artifactPath string, imageTable ImageTable) (map[string]any, error) {
	if err := Validate(c); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(artifactPath, "/") {
		return nil, fmt.Errorf("artifact path %q must be absolute", artifactPath)
	}
	registry, err := c.RegistryAddress()
	if err != nil {
		return nil, err
	}
	imageRefs, err := LocalImageReferences(imageTable, registry)
	if err != nil {
		return nil, err
	}
	nodeNames := make([]string, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		nodeNames = append(nodeNames, n.Name)
	}
	nodeAddresses := make([]string, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		nodeAddresses = append(nodeAddresses, n.Address)
	}
	return map[string]any{
		"zone": "",
		"download": map[string]any{
			"fetch":         false,
			"artifact_file": artifactPath,
		},
		"kubernetes": map[string]any{
			"kube_version": "v1.35.8",
			"cluster_name": c.Name,
			"control_plane_endpoint": map[string]any{
				"type": "local",
			},
			"custom_labels": map[string]any{
				"networking.kubercloud.com/role": "master",
			},
		},
		"etcd": map[string]any{
			"deployment_type": "internal",
			"etcd_version":    "v3.6.6",
			"image": map[string]any{
				"registry":   registry,
				"repository": "kubernetes",
				"tag":        "v3.6.6",
			},
		},
		"image_registry": map[string]any{
			"type": "",
			"auth": map[string]any{
				"registry":        registry,
				"plain_http":      true,
				"username":        "",
				"password":        "",
				"skip_tls_verify": true,
			},
		},
		"cri": map[string]any{
			"container_manager":  "containerd",
			"containerd_version": "v2.3.4",
			"runc_version":       "v1.4.3",
			"registry": map[string]any{
				"insecure_registries": []string{registry},
			},
		},
		"cni": map[string]any{
			"type":         "none",
			"multi_cni":    "none",
			"pod_cidr":     c.Network.PodCIDR,
			"service_cidr": c.Network.ServiceCIDR,
		},
		"storage_class": map[string]any{
			"local": map[string]any{"enabled": false},
			"nfs":   map[string]any{"enabled": false},
		},
		"dns": map[string]any{
			"nodelocaldns": map[string]any{"enabled": false},
		},
		"ani": map[string]any{
			"registry":       registry,
			"images":         imageRefs,
			"nodes":          nodeNames,
			"node_addresses": nodeAddresses,
			"installer_node": c.InstallerNode,
			"network": map[string]any{
				"management_interface": c.Network.ManagementInterface,
				"pod_cidr":             c.Network.PodCIDR,
				"service_cidr":         c.Network.ServiceCIDR,
				"kcn": map[string]any{
					"managedDevices":   c.Network.KCN.ManagedDevices,
					"encapNetworks":    c.Network.KCN.EncapNetworks,
					"intranetNetworks": c.Network.KCN.IntranetNetworks,
				},
			},
		},
	}, nil
}

// KubeKeyInventory returns an Inventory spec with local connector only for
// the installer and native SSH connectors for the other two nodes.
func KubeKeyInventory(c ClusterConfig) (map[string]any, error) {
	if err := Validate(c); err != nil {
		return nil, err
	}
	hosts := map[string]any{}
	for _, n := range c.Nodes {
		var connector map[string]any
		if n.Name == c.InstallerNode {
			connector = map[string]any{
				"type": "local",
				"user": c.SSH.User,
			}
		} else {
			connector = map[string]any{
				"type": "ssh",
				"host": n.Address,
				"port": c.SSH.Port,
				"user": c.SSH.User,
			}
			if strings.TrimSpace(c.SSH.Password) != "" {
				connector["password"] = c.SSH.Password
			} else {
				connector["private_key"] = c.SSH.PrivateKey
			}
		}
		hosts[n.Name] = map[string]any{
			"connector":     connector,
			"internal_ipv4": n.Address,
		}
	}
	names := make([]string, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		names = append(names, n.Name)
	}
	return map[string]any{
		"hosts": hosts,
		"groups": map[string]any{
			"k8s_cluster":        map[string]any{"groups": []string{"kube_control_plane", "kube_worker"}},
			"kube_control_plane": map[string]any{"hosts": names},
			"kube_worker":        map[string]any{"hosts": names},
			"etcd":               map[string]any{"hosts": names},
		},
	}, nil
}
func LocalImageReferences(table ImageTable, registry string) (map[string]string, error) {
	refs := make(map[string]string, len(table))
	for original := range table {
		ref, err := table.LocalReference(original, registry)
		if err != nil {
			return nil, err
		}
		refs[original] = ref
	}
	return refs, nil
}

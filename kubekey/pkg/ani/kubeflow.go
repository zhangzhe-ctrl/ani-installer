package ani

import (
	"fmt"
	"net"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

const KubeflowRelease = "26.03-kfp2.16-trainer2.1-v1"

// Kubeflow belongs to the first-install chain, outside Components. It is not
// an addition target; omitting this pointer preserves legacy config digests.
type KubeflowConfig struct {
	Enabled      bool              `yaml:"enabled"`
	Release      string            `yaml:"release"`
	EntryAddress string            `yaml:"entryAddress"`
	HTTPPort     int               `yaml:"httpPort"`
	GRPCPort     int               `yaml:"grpcPort"`
	Database     KubeflowDatabase  `yaml:"database"`
	Workspace    KubeflowWorkspace `yaml:"workspace"`
	Tenants      []string          `yaml:"tenants"`
}

type KubeflowDatabase struct {
	StorageClass string `yaml:"storageClass"`
	StorageSize  string `yaml:"storageSize"`
}

type KubeflowWorkspace struct {
	StorageClass       string `yaml:"storageClass"`
	MaxSize            string `yaml:"maxSize"`
	MaxClaimsPerTenant int    `yaml:"maxClaimsPerTenant"`
}

func (c ClusterConfig) KubeflowEnabled() bool {
	return c.Kubeflow != nil && c.Kubeflow.Enabled
}

var kubeflowDNSName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func validateKubeflow(c ClusterConfig) error {
	if !c.KubeflowEnabled() {
		return nil
	}
	k := c.Kubeflow
	if installProfile(c.Profile) != "full" {
		return fmt.Errorf("kubeflow.enabled requires profile=full; Kubeflow is a first-install selection")
	}
	if k.Release != KubeflowRelease {
		return fmt.Errorf("kubeflow.release must be %q, got %q", KubeflowRelease, k.Release)
	}
	if !c.Storage.Enabled {
		return fmt.Errorf("kubeflow.enabled requires persistent storage")
	}
	if c.ObjectStorageProvider() != objectProviderRustFS {
		return fmt.Errorf("Kubeflow release %s requires declared objectStorage.provider=rustfs", KubeflowRelease)
	}
	if networkStack(c.Network.Stack) != "kubeovn" {
		return fmt.Errorf("Kubeflow release %s requires network.stack=kubeovn with enforced NetworkPolicy", KubeflowRelease)
	}
	address := net.ParseIP(k.EntryAddress)
	if address == nil || address.To4() == nil {
		return fmt.Errorf("kubeflow.entryAddress must be a declared node IPv4")
	}
	declared := false
	for _, node := range c.Nodes {
		declared = declared || node.Address == k.EntryAddress
	}
	if !declared {
		return fmt.Errorf("kubeflow.entryAddress %q is not a declared node address", k.EntryAddress)
	}
	if k.HTTPPort < 30000 || k.HTTPPort > 32767 || k.GRPCPort < 30000 || k.GRPCPort > 32767 || k.HTTPPort == k.GRPCPort {
		return fmt.Errorf("kubeflow.httpPort and grpcPort must be distinct NodePorts within 30000..32767")
	}
	if c.Components.Harbor.Enabled && (k.HTTPPort == 30003 || k.GRPCPort == 30003) {
		return fmt.Errorf("Kubeflow entry conflicts with Harbor NodePort 30003")
	}
	for name, value := range map[string]string{"database.storageClass": k.Database.StorageClass, "workspace.storageClass": k.Workspace.StorageClass} {
		if len(value) > 63 || !kubeflowDNSName.MatchString(value) {
			return fmt.Errorf("kubeflow.%s must be an explicit StorageClass name", name)
		}
	}
	for name, value := range map[string]string{"database.storageSize": k.Database.StorageSize, "workspace.maxSize": k.Workspace.MaxSize} {
		quantity, err := resource.ParseQuantity(value)
		if err != nil || quantity.Sign() <= 0 {
			return fmt.Errorf("kubeflow.%s must be a positive storage quantity", name)
		}
	}
	if k.Workspace.MaxClaimsPerTenant < 1 || k.Workspace.MaxClaimsPerTenant > 4 {
		return fmt.Errorf("kubeflow.workspace.maxClaimsPerTenant must be within 1..4; retained failed runs count against this bound")
	}
	if len(k.Tenants) < 1 || len(k.Tenants) > 8 {
		return fmt.Errorf("kubeflow.tenants requires 1..8 explicitly managed namespaces")
	}
	seen := map[string]bool{}
	for _, tenant := range k.Tenants {
		if len(tenant) > 63 || !strings.HasPrefix(tenant, "ani-kfp-") || !kubeflowDNSName.MatchString(tenant) || seen[tenant] {
			return fmt.Errorf("kubeflow tenant %q must be a unique ani-kfp- namespace", tenant)
		}
		seen[tenant] = true
	}
	return nil
}

// This map is consumed by the same production render context as every ANI
// role. No credentials are accepted in the site contract or connection facts.
func kubeflowSpec(c ClusterConfig) map[string]any {
	spec := map[string]any{"enabled": c.KubeflowEnabled()}
	if !c.KubeflowEnabled() {
		return spec
	}
	k := c.Kubeflow
	spec["release"] = k.Release
	spec["entry_address"] = k.EntryAddress
	spec["http_port"] = k.HTTPPort
	spec["grpc_port"] = k.GRPCPort
	spec["database_class"] = k.Database.StorageClass
	spec["database_size"] = k.Database.StorageSize
	spec["workspace_class"] = k.Workspace.StorageClass
	spec["workspace_max_size"] = k.Workspace.MaxSize
	spec["workspace_max_claims"] = k.Workspace.MaxClaimsPerTenant
	spec["tenants"] = k.Tenants
	return spec
}

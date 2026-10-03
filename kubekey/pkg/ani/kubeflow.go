package ani

import (
	"fmt"
	"math"
	"net"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

const KubeflowRelease = "26.03-kfp2.16-trainer2.1-v1"

func kubeflowImageKeys() []ImageKey {
	return []ImageKey{
		{Group: "kubeflow", Name: "api", Original: "ghcr.io/kubeflow/kfp-api-server:2.16.0"},
		{Group: "kubeflow", Name: "metadataEnvoy", Original: "ghcr.io/kubeflow/kfp-metadata-envoy:2.16.0"},
		{Group: "kubeflow", Name: "metadataWriter", Original: "ghcr.io/kubeflow/kfp-metadata-writer:2.16.0"},
		{Group: "kubeflow", Name: "persistence", Original: "ghcr.io/kubeflow/kfp-persistence-agent:2.16.0"},
		{Group: "kubeflow", Name: "scheduledWorkflow", Original: "ghcr.io/kubeflow/kfp-scheduled-workflow-controller:2.16.0"},
		{Group: "kubeflow", Name: "driver", Original: "ghcr.io/kubeflow/kfp-driver:2.16.0"},
		{Group: "kubeflow", Name: "launcher", Original: "ghcr.io/kubeflow/kfp-launcher:2.16.0"},
		{Group: "kubeflow", Name: "mlmd", Original: "gcr.io/tfx-oss-public/ml_metadata_store_server:1.14.0"},
		{Group: "kubeflow", Name: "argo", Original: "quay.io/argoproj/workflow-controller:v3.7.3"},
		{Group: "kubeflow", Name: "argoExecutor", Original: "quay.io/argoproj/argoexec:v3.7.3"},
		{Group: "kubeflow", Name: "trainer", Original: "ghcr.io/kubeflow/trainer/trainer-controller-manager:v2.1.0"},
		{Group: "kubeflow", Name: "jobset", Original: "registry.k8s.io/jobset/jobset:v0.10.1"},
		{Group: "kubeflow", Name: "mysql", Original: "docker.io/library/mysql:8.4.11"},
		{Group: "kubeflow", Name: "entry", Original: "docker.io/library/nginx:1.30.5"},
		{Group: "kubeflow", Name: "execution", Original: "ani.local/kubeflow-execution:26.03-v1"},
	}
}

func kubeflowPinnedImages(table ImageTable, registry string) (map[string]string, error) {
	refs := map[string]string{}
	for _, key := range kubeflowImageKeys() {
		ref, err := table.LocalReference(key.Original, registry)
		if err != nil {
			return nil, err
		}
		image := table[key.Original]
		if !strings.HasPrefix(image.Digest, "sha256:") || !isHex64(strings.TrimPrefix(image.Digest, "sha256:")) {
			return nil, fmt.Errorf("Kubeflow image %q has no approved platform digest", key.Original)
		}
		refs[key.Original] = strings.Split(ref, "@")[0] + "@" + image.Digest
	}
	return refs, nil
}

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
	workspaceQuantity := resource.MustParse(k.Workspace.MaxSize)
	workspaceSize := workspaceQuantity.Value()
	if workspaceSize <= 0 || workspaceSize > math.MaxInt64/int64(k.Workspace.MaxClaimsPerTenant) {
		return fmt.Errorf("kubeflow.workspace.maxSize exceeds the supported aggregate quota quantity")
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
	workspaceQuantity := resource.MustParse(k.Workspace.MaxSize)
	spec["workspace_quota_size"] = fmt.Sprint(workspaceQuantity.Value() * int64(k.Workspace.MaxClaimsPerTenant))
	spec["workspace_mode"] = "managed-execution-pvc-v1"
	spec["tenants"] = k.Tenants
	return spec
}

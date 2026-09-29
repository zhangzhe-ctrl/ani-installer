package ani

import "fmt"

// MilvusS3Binding is the non-secret connection contract shared by installation,
// verification and records. A provider change is a data migration, not a
// component add or a no-op.
type MilvusS3Binding struct {
	Provider        string `json:"provider"`
	Endpoint        string `json:"endpoint"`
	Host            string `json:"host"`
	Port            int    `json:"port"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	RootPath        string `json:"rootPath"`
	SecretNamespace string `json:"secretNamespace"`
	SecretName      string `json:"secretName"`
	AccessKeyField  string `json:"accessKeyField"`
	SecretKeyField  string `json:"secretKeyField"`
	CANamespace     string `json:"caNamespace"`
	CAConfigMap     string `json:"caConfigMap"`
	CAMountPath     string `json:"caMountPath"`
	CAPath          string `json:"caPath"`
	Addressing      string `json:"addressing"`
}

func ResolveMilvusS3Binding(c ClusterConfig) (MilvusS3Binding, error) {
	b := MilvusS3Binding{
		Provider: c.ObjectStorageProvider(), Region: "us-east-1",
		Bucket: "ani-milvus-objects", RootPath: "files",
		SecretNamespace: "ani-platform", AccessKeyField: "AWS_ACCESS_KEY_ID",
		SecretKeyField: "AWS_SECRET_ACCESS_KEY", CANamespace: "ani-platform",
		Addressing: "path",
	}
	switch b.Provider {
	case objectProviderRGW:
		b.Host = "rook-ceph-rgw-ani-store.rook-ceph.svc.cluster.local"
		b.Port = 443
		b.SecretName = "ani-milvus-objects"
		b.CAConfigMap = "ani-rgw-ca"
		b.CAMountPath = "/etc/ani-rgw-ca"
		b.CAPath = "/etc/ani-rgw-ca/ca.crt"
	case objectProviderRustFS:
		b.Host = "ani-rustfs-svc.ani-platform.svc.cluster.local"
		b.Port = 9000
		b.SecretName = "ani-milvus-rustfs"
		b.CAConfigMap = "ani-rustfs-ca"
		b.CAMountPath = "/etc/ani-rustfs-ca"
		b.CAPath = "/etc/ani-rustfs-ca/ca.crt"
	default:
		return MilvusS3Binding{}, fmt.Errorf("Milvus needs an RGW or RustFS object storage binding, got %q", b.Provider)
	}
	b.Endpoint = fmt.Sprintf("https://%s:%d", b.Host, b.Port)
	return b, nil
}

func (b MilvusS3Binding) templateSpec() map[string]any {
	return map[string]any{
		"provider": b.Provider, "endpoint": b.Endpoint, "host": b.Host,
		"port": b.Port, "region": b.Region, "bucket": b.Bucket,
		"root_path": b.RootPath, "secret_namespace": b.SecretNamespace,
		"secret_name": b.SecretName, "access_key_field": b.AccessKeyField,
		"secret_key_field": b.SecretKeyField, "ca_namespace": b.CANamespace,
		"ca_config_map": b.CAConfigMap, "ca_mount_path": b.CAMountPath,
		"ca_path":    b.CAPath,
		"addressing": b.Addressing,
	}
}

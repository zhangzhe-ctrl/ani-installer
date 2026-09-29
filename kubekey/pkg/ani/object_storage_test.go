package ani

import (
	"strings"
	"testing"
)

func TestObjectStorageSelectionAndValidation(t *testing.T) {
	base := validConfig()
	base.Storage = Storage{Enabled: true, Provider: storageProviderCeph, Nodes: []StorageNode{
		{Name: "node1", Devices: []string{"/dev/sdb"}},
		{Name: "node2", Devices: []string{"/dev/sdb"}},
		{Name: "node3", Devices: []string{"/dev/sdb"}},
	}}
	if got := base.ObjectStorageProvider(); got != objectProviderRGW {
		t.Fatalf("legacy Ceph provider = %q", got)
	}
	for _, tc := range []struct {
		name    string
		change  func(*ClusterConfig)
		want    string
	}{
		{"explicit none", func(c *ClusterConfig) { c.ObjectStorage = &ObjectStorage{Provider: objectProviderNone} }, ""},
		{"none with Milvus", func(c *ClusterConfig) { c.ObjectStorage = &ObjectStorage{Provider: objectProviderNone}; c.Components.Milvus.Enabled = true }, "components.milvus"},
		{"RGW without Ceph", func(c *ClusterConfig) { c.Storage = Storage{}; c.ObjectStorage = &ObjectStorage{Provider: objectProviderRGW} }, "requires storage"},
		{"RustFS without volume", func(c *ClusterConfig) { c.Storage = Storage{}; c.ObjectStorage = &ObjectStorage{Provider: objectProviderRustFS} }, "persistent storage"},
		{"RustFS invalid size", func(c *ClusterConfig) { c.ObjectStorage = &ObjectStorage{Provider: objectProviderRustFS, RustFS: RustFSStorage{Mode: "standalone", StorageClass: DefaultStorageClass, StorageSize: "0Gi"}} }, "greater than zero"},
		{"RustFS pending implementation", func(c *ClusterConfig) { c.ObjectStorage = &ObjectStorage{Provider: objectProviderRustFS, RustFS: RustFSStorage{Mode: "standalone", StorageClass: DefaultStorageClass, StorageSize: "20Gi"}} }, "not deployable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.change(&c)
			err := Validate(c)
			if tc.want == "" {
				if err != nil { t.Fatalf("unexpected error: %v", err) }
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

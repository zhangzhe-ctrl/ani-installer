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
		name   string
		change func(*ClusterConfig)
		want   string
	}{
		{"explicit none", func(c *ClusterConfig) { c.ObjectStorage = &ObjectStorage{Provider: objectProviderNone} }, ""},
		{"none with Milvus", func(c *ClusterConfig) {
			c.ObjectStorage = &ObjectStorage{Provider: objectProviderNone}
			c.Components.Milvus.Enabled = true
		}, "components.milvus"},
		{"RGW without Ceph", func(c *ClusterConfig) {
			c.Storage = Storage{}
			c.ObjectStorage = &ObjectStorage{Provider: objectProviderRGW}
		}, "requires storage"},
		{"RustFS without volume", func(c *ClusterConfig) {
			c.Storage = Storage{}
			c.ObjectStorage = &ObjectStorage{Provider: objectProviderRustFS}
		}, "persistent storage"},
		{"RustFS invalid size", func(c *ClusterConfig) {
			c.ObjectStorage = &ObjectStorage{Provider: objectProviderRustFS, RustFS: RustFSStorage{Mode: "standalone", StorageClass: DefaultStorageClass, StorageSize: "0Gi"}}
		}, "greater than zero"},
		{"RustFS base profile cannot deploy storage", func(c *ClusterConfig) {
			c.Profile = "base"
			c.ObjectStorage = &ObjectStorage{Provider: objectProviderRustFS, RustFS: RustFSStorage{Mode: "standalone", StorageClass: DefaultStorageClass, StorageSize: "20Gi"}}
		}, "profile=full"},
		{"RustFS pending implementation", func(c *ClusterConfig) {
			c.ObjectStorage = &ObjectStorage{Provider: objectProviderRustFS, RustFS: RustFSStorage{Mode: "standalone", StorageClass: DefaultStorageClass, StorageSize: "20Gi"}}
			c.Components.CertManager.Enabled = true
		}, "not deployable"},
		{"RustFS without internal CA", func(c *ClusterConfig) {
			c.ObjectStorage = &ObjectStorage{Provider: objectProviderRustFS, RustFS: RustFSStorage{Mode: "standalone", StorageClass: DefaultStorageClass, StorageSize: "20Gi"}}
		}, "components.certManager"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.change(&c)
			err := Validate(c)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestMilvusS3BindingKeepsRGWNamesAndSeparatesRustFS(t *testing.T) {
	c := validConfig()
	c.Storage = Storage{Enabled: true, Provider: storageProviderCeph}
	rgw, err := ResolveMilvusS3Binding(c)
	if err != nil {
		t.Fatal(err)
	}
	if rgw.Endpoint != "https://rook-ceph-rgw-ani-store.rook-ceph.svc.cluster.local:443" ||
		rgw.SecretName != "ani-milvus-objects" || rgw.CAPath != "/etc/ani-rgw-ca/ca.crt" ||
		rgw.Bucket != "ani-milvus-objects" || rgw.RootPath != "files" {
		t.Fatalf("legacy RGW binding changed: %+v", rgw)
	}
	c.ObjectStorage = &ObjectStorage{Provider: objectProviderRustFS}
	rustfs, err := ResolveMilvusS3Binding(c)
	if err != nil {
		t.Fatal(err)
	}
	if rustfs.Endpoint != "https://ani-rustfs-svc.ani-platform.svc.cluster.local:9000" ||
		rustfs.SecretName == rgw.SecretName || rustfs.CAConfigMap == rgw.CAConfigMap ||
		rustfs.Addressing != "path" {
		t.Fatalf("RustFS binding is not independent: %+v", rustfs)
	}
	c.ObjectStorage.Provider = objectProviderNone
	if _, err := ResolveMilvusS3Binding(c); err == nil {
		t.Fatal("Milvus accepted objectStorage=none")
	}
}

func TestRustFSComponentClosureUsesObjectStorageSelection(t *testing.T) {
	c := validConfig()
	c.Storage = Storage{Enabled: true, Provider: storageProviderCeph}
	c.ObjectStorage = &ObjectStorage{Provider: objectProviderRustFS, RustFS: RustFSStorage{
		Mode: "standalone", StorageClass: DefaultStorageClass, StorageSize: "20Gi",
	}}
	c.Components.CertManager.Enabled = true
	c.Components.Milvus.Enabled = true
	if c.Components.RustFS.Enabled {
		t.Fatal("RustFS must not become a second site component switch")
	}
	if !c.EffectiveComponents().RustFS.Enabled {
		t.Fatal("the selected RustFS provider did not enable its component")
	}
	scope, err := componentsScope(c, []string{"milvus"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cert-manager", "rustfs", "milvus"}
	if len(scope) != len(want) {
		t.Fatalf("Milvus component closure = %v, want %v", scope, want)
	}
	for i := range want {
		if scope[i] != want[i] {
			t.Fatalf("Milvus component closure = %v, want %v", scope, want)
		}
	}
}

func TestComponentsObjectStorageMigrationGuard(t *testing.T) {
	c := validConfig()
	c.Storage = Storage{Enabled: true, Provider: storageProviderCeph}
	c.Components.Milvus.Enabled = true
	current, err := BuildRunManifest(c)
	if err != nil {
		t.Fatal(err)
	}
	legacy := current
	legacy.ObjectStorage = nil // Old success records have no objectStorage field.
	if _, err := compareBaseInvariants(legacy, current); err != nil {
		t.Fatalf("legacy RGW binding should remain valid: %v", err)
	}
	changed := current
	changed.ObjectStorage = &ManifestObjectStorage{Provider: objectProviderRustFS}
	if _, err := compareBaseInvariants(legacy, changed); err == nil || !strings.Contains(err.Error(), "migrate") {
		t.Fatalf("old RGW to RustFS must be rejected: %v", err)
	}
	base := current
	base.Components = nil
	base.ObjectStorage = &ManifestObjectStorage{Provider: objectProviderNone}
	add := current
	add.ObjectStorage = &ManifestObjectStorage{Provider: objectProviderRustFS, RustFSClass: DefaultStorageClass, RustFSSize: "20Gi"}
	if _, err := compareBaseInvariants(base, add); err != nil {
		t.Fatalf("empty base may add RustFS: %v", err)
	}
	add.ObjectStorage.Provider = objectProviderRGW
	if _, err := compareBaseInvariants(base, add); err == nil {
		t.Fatal("components path accepted none to RGW without a Ceph object-store role")
	}
}

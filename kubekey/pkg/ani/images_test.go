package ani

import "testing"

func TestImageTableMapping(t *testing.T) {
	rows := []string{
		"original_ref\thauler_ref\tactual_digest\tuse_location",
		"docker.changqingyun.cn/kubercloud/ratelimit:1e50889b\t127.0.0.1:5000/kubercloud/ratelimit:1e50889b\tsha256:test\tEnvoy rate limit",
	}
	table, err := LoadImageTable(rows)
	if err != nil {
		t.Fatalf("LoadImageTable() error = %v", err)
	}
	got, err := table.LocalReference("docker.changqingyun.cn/kubercloud/ratelimit:1e50889b", "192.0.2.11:5000")
	if err != nil {
		t.Fatalf("LocalReference() error = %v", err)
	}
	if got != "192.0.2.11:5000/kubercloud/ratelimit:1e50889b" {
		t.Fatalf("LocalReference() = %q", got)
	}
	if _, err := table.LocalReference("example.invalid/missing:v1", "192.0.2.11:5000"); err == nil {
		t.Fatal("mapping a missing image unexpectedly succeeded")
	}
}

func TestManifestURL(t *testing.T) {
	got, err := ManifestURL("127.0.0.1:5000/kubercloud/ratelimit:1e50889b")
	if err != nil {
		t.Fatalf("ManifestURL() error = %v", err)
	}
	if got != "/v2/kubercloud/ratelimit/manifests/1e50889b" {
		t.Fatalf("ManifestURL() = %q", got)
	}
}

package ani

import (
	"path/filepath"
	"strings"
	"testing"
)

// The production config, image filter, playbook gate and role templates must
// move together. A site may keep Milvus off without carrying B02 material.
func TestB02MilvusProductionSelectionAndRender(t *testing.T) {
	off, err := ParseClusterConfig([]byte(r06SiteA))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range off.Components.Selection() {
		if row.Name == "milvus" && row.Enabled {
			t.Fatal("Milvus defaulted on")
		}
	}
	if aniRoleEnabled("milvus", off) {
		t.Fatal("Milvus role enabled while switch is off")
	}
	if strings.Contains(strings.Join(requiredChartPaths(off), " "), "milvus") {
		t.Fatal("disabled Milvus requires its Chart")
	}

	onText := strings.Replace(r06SiteA, "  certManager: {enabled: true}", "  certManager: {enabled: true}\n  milvus: {enabled: true, storageClass: ani-block, storageSize: 10Gi, etcdStorageSize: 5Gi}", 1)
	on, err := ParseClusterConfig([]byte(onText))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(on); err != nil {
		t.Fatal(err)
	}
	if !aniRoleEnabled("milvus", on) {
		t.Fatal("Milvus role absent when selected")
	}
	table := r08FullTable(t)
	delete(table, "docker.io/milvusdb/etcd:3.5.25-r1")
	if _, err := KubeKeyConfig(on, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", table); err == nil || !strings.Contains(err.Error(), "etcd:3.5.25-r1") {
		t.Fatalf("missing dedicated etcd image should stop preflight: %v", err)
	}
	files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), on, "/opt/ani", r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"tasks/main.yaml": false, "templates/values.yaml": false, "templates/verify.sh": false, "templates/s3-verify.sh": false, "templates/bucket.yaml": false, "templates/rgw-ca.sh": false}
	for _, file := range files {
		if file.Role != "milvus" {
			continue
		}
		rel := strings.TrimPrefix(file.Rel, "milvus/")
		if _, known := want[rel]; known {
			want[rel] = true
		}
		if strings.Contains(string(file.Rendered), "<no value>") {
			t.Fatalf("unbound Milvus template: %s", file.Rel)
		}
		if rel == "templates/verify.sh" {
			for _, required := range []string{"/proc/1/environ", "tlsCACert: /etc/ani-rgw-ca/ca.crt", "b02-process-result.txt", "/milvusdb/milvus:v2.6.24"} {
				if !strings.Contains(string(file.Rendered), required) {
					t.Errorf("Milvus process trust check missing %q", required)
				}
			}
		}
		if rel == "templates/values.yaml" {
			for _, required := range []string{"port: 443", "useSSL: true", "region: us-east-1", "SSL_CERT_FILE", "AWS_CA_BUNDLE", "name: ani-rgw-ca", "tlsCACert: /etc/ani-rgw-ca/ca.crt", "secretKeyRef:"} {
				if !strings.Contains(string(file.Rendered), required) {
					t.Errorf("Milvus TLS or OBC binding missing %q", required)
				}
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("selected Milvus did not render %s", name)
		}
	}
	for _, file := range files {
		if file.Role != "ceph" {
			continue
		}
		if file.Rel == "ceph/templates/object.yaml" {
			for _, required := range []string{"port: 80", "securePort: 443", "sslCertificateRef: ani-rgw-server-tls"} {
				if !strings.Contains(string(file.Rendered), required) {
					t.Errorf("Ceph RGW TLS missing %q", required)
				}
			}
		}
	}
	if err := ValidateRenderedArtifacts(files); err != nil {
		t.Fatal(err)
	}
}

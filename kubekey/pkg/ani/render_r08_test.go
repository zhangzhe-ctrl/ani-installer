/*
Copyright 2026 The KubeSphere Contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ani

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// r08FullTable loads the real shipped image table, which covers every declared
// key (CNI, components, verification, lab) — the same files a real build uses.
func r08FullTable(t *testing.T) ImageTable {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "ani", "images.tsv"))
	if err != nil {
		t.Fatalf("read images.tsv: %v", err)
	}
	table, err := LoadImageTable(strings.Split(string(raw), "\n"))
	if err != nil {
		t.Fatalf("parse images.tsv: %v", err)
	}
	return table
}

// r08SiteWithStack returns a full-profile site config with the requested stack.
func r08SiteWithStack(stack string) string {
	return strings.Replace(r06SiteA, "stack: kcn", "stack: "+stack, 1)
}

// T-R08-01: a stack/package mismatch must fail before deployment: a kubeovn
// config against a kcn-only package names the missing kube-ovn image, and the
// reverse names the kcn image. Neither silently falls back.
func TestMaterialKeysMatchTheSelectedStack(t *testing.T) {
	// kubeovn config + kcn-only package: drop both kube-ovn images.
	kcnOnly := r06SiteA
	kcnOnly = strings.Replace(kcnOnly, "stack: kcn", "stack: kubeovn", 1)
	config, err := ParseClusterConfig([]byte(kcnOnly))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	table := r08FullTable(t)
	delete(table, "docker.io/kubeovn/kube-ovn:v1.16.6")
	delete(table, "docker.io/kubeovn/vpc-nat-gateway:v1.16.6")
	_, err = KubeKeyConfig(config, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", table)
	if err == nil {
		t.Fatal("a kubeovn config with a kcn-only image set must fail before deployment")
	}
	if !strings.Contains(err.Error(), "kube-ovn") {
		t.Fatalf("error = %v, want it to name the missing kube-ovn image", err)
	}

	// Reverse: a kcn config without the kcn networking image.
	kcnConfig, err := ParseClusterConfig([]byte(r06SiteA))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	table = r08FullTable(t)
	delete(table, "docker.changqingyun.cn/kubercloud/kc-networking:dev")
	_, err = KubeKeyConfig(kcnConfig, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", table)
	if err == nil {
		t.Fatal("a kcn config without the kcn networking image must fail before deployment")
	}
	if !strings.Contains(err.Error(), "kc-networking") {
		t.Fatalf("error = %v, want it to name the missing kcn image", err)
	}
}

// T-R08-02: removing any single required image must fail KubeKeyConfig with an
// error naming exactly that image.
func TestEveryRequiredImageKeyIsEnforced(t *testing.T) {
	text := strings.Replace(r06SiteA,
		"certManager: {enabled: true}",
		"certManager: {enabled: true}\n  postgresql: {enabled: true}\n  valkey: {enabled: true}\n  nats: {enabled: true}\n  metrics: {enabled: true}", 1)
	config, err := ParseClusterConfig([]byte(text))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(config); err != nil {
		t.Fatalf("validate: %v", err)
	}
	required := componentImageKeysForRun(config)
	if len(required) < 14 {
		t.Fatalf("the full config requires %d images, want at least 14 (CNI+components+metrics+verification+lab)", len(required))
	}
	for _, key := range required {
		table := r08FullTable(t)
		if _, ok := table[key.Original]; !ok {
			t.Fatalf("required image %q is not in the shipped images.tsv", key.Original)
		}
		delete(table, key.Original)
		_, err := KubeKeyConfig(config, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", table)
		if err == nil {
			t.Fatalf("removing required image %q must fail KubeKeyConfig", key.Original)
		}
		if !strings.Contains(err.Error(), key.Original) {
			t.Fatalf("removing %q: error = %v, want it to name the image", key.Original, err)
		}
	}
}

// T-R08-03: the log backend selection scopes the required images — a loki-only
// run does not require the OpenSearch image and vice versa.
func TestLogBackendScopesRequiredImages(t *testing.T) {
	base := strings.Replace(r06SiteA, "components:\n  certManager: {enabled: true}\n",
		"components:\n  certManager: {enabled: true}\n  logging:\n    backend: loki\n    storageClass: ani-block\n    storageSize: 5Gi\n", 1)
	lokiConfig, err := ParseClusterConfig([]byte(base))
	if err != nil {
		t.Fatalf("parse loki config: %v", err)
	}
	table := r08FullTable(t)
	delete(table, "docker.io/opensearchproject/opensearch:3.8.0")
	if _, err := KubeKeyConfig(lokiConfig, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", table); err != nil {
		t.Fatalf("a loki-only run must not require the OpenSearch image: %v", err)
	}

	opensearchConfig, err := ParseClusterConfig([]byte(strings.Replace(base, "backend: loki", "backend: opensearch", 1)))
	if err != nil {
		t.Fatalf("parse opensearch config: %v", err)
	}
	table2 := r08FullTable(t)
	delete(table2, "docker.io/grafana/loki:3.7.8")
	if _, err := KubeKeyConfig(opensearchConfig, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", table2); err != nil {
		t.Fatalf("an opensearch-only run must not require the Loki image: %v", err)
	}
}

// T-R08-04: the rendered-output semantic checks catch empty images, leftover
// placeholders, unrendered values and broken heredoc YAML.
func TestRenderedArtifactChecks(t *testing.T) {
	cases := map[string][]RenderedFile{
		"unrendered value": {{Name: "metrics-values.yaml",
			Rendered: []byte("image: {{ .ani.image_parts.metrics.prometheus }}")}},
		"no value leak": {{Name: "metrics-values.yaml",
			Rendered: []byte("repository: <no value>")}},
		"leftover placeholder": {{Name: "config.yaml",
			Rendered: []byte("device: /dev/disk/by-id/REPLACE_WITH_NODE1_DATA_DEVICE\n")}},
		"empty image": {{Name: "values.yaml",
			Rendered: []byte("image:\n  repository: \"\"\n  tag: \"\"\n")}},
		"broken heredoc yaml": {{Name: "nats-tasks-main.yaml",
			Rendered: []byte("- name: write values\n  command: |\n    cat > /tmp/values.yaml <<EOF\n" +
				"image: \"nats:1\nauthorization:\nEOF\n")}},
	}
	for name, files := range cases {
		if err := ValidateRenderedArtifacts(files); err == nil {
			t.Fatalf("%s must be rejected by the semantic checks", name)
		}
	}
}

// T-R08-05 (table-driven) + the render positive path: every enabled role file
// renders with the production context and passes the semantic checks, across
// stacks, profiles, component on/off and log mutual exclusion. Each selection
// uses the image table a real build of it would use.
func TestRenderSiteAcrossSelections(t *testing.T) {
	rolesDir := filepath.Join("..", "..", "builtin", "core", "roles", "ani")
	selections := []struct {
		name     string
		text     string
		tableRel string
	}{
		{"kcn-full-minimal", strings.Replace(r06SiteA, "certManager: {enabled: true}", "certManager: {enabled: false}", 1), "images.tsv"},
		{"kcn-loki", strings.Replace(r06SiteA,
			"certManager: {enabled: true}",
			"certManager: {enabled: true}\n  logging:\n    backend: loki\n    storageClass: ani-block\n    storageSize: 5Gi", 1), "images.tsv"},
		{"kcn-opensearch", strings.Replace(r06SiteA,
			"certManager: {enabled: true}",
			"certManager: {enabled: true}\n  logging:\n    backend: opensearch\n    storageClass: ani-block\n    storageSize: 5Gi", 1), "images.tsv"},
		{"kubeovn-minimal", strings.Replace(strings.Replace(r06SiteA,
			"stack: kcn", "stack: kubeovn", 1),
			"certManager: {enabled: true}", "certManager: {enabled: false}", 1), "images-kubeovn.tsv"},
	}
	for _, selection := range selections {
		config, err := ParseClusterConfig([]byte(selection.text))
		if err != nil {
			t.Fatalf("%s: parse: %v", selection.name, err)
		}
		if err := Validate(config); err != nil {
			t.Fatalf("%s: validate: %v", selection.name, err)
		}
		raw, err := os.ReadFile(filepath.Join("..", "..", "ani", selection.tableRel))
		if err != nil {
			t.Fatalf("%s: read %s: %v", selection.name, selection.tableRel, err)
		}
		table, err := LoadImageTable(strings.Split(string(raw), "\n"))
		if err != nil {
			t.Fatalf("%s: parse %s: %v", selection.name, selection.tableRel, err)
		}
		files, err := RenderSite(rolesDir, config, "/opt/ani", table)
		if err != nil {
			t.Fatalf("%s: render: %v", selection.name, err)
		}
		if err := ValidateRenderedArtifacts(files); err != nil {
			t.Fatalf("%s: rendered artifacts have problems: %v", selection.name, err)
		}
		// The rendered set must contain the enabled roles' files and none of the
		// disabled ones (mutual exclusion / switch off).
		renderedRoles := map[string]bool{}
		for _, file := range files {
			renderedRoles[strings.Split(file.Name, "-tasks-main.yaml")[0]] = true
		}
		for _, role := range []string{"kcn", "kubeovn", "ceph", "cert-manager", "metrics", "loki", "opensearch", "fluent-bit"} {
			_, present := renderedRoles[role]
			enabled := aniRoleEnabled(role, config)
			if enabled && !present {
				t.Fatalf("%s: enabled role %s has no rendered task file", selection.name, role)
			}
			if !enabled && present {
				t.Fatalf("%s: disabled role %s was rendered anyway", selection.name, role)
			}
		}
	}
}

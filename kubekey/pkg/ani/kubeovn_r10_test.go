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

// r10KubeOVNSite returns the shared site config rewritten for the kubeovn
// stack: the podCIDR/serviceCIDR/kcn lines of the network section are replaced
// wholesale with the given text (which must spell podCIDR and serviceCIDR).
// The nodes stay at 192.0.2.x so they never collide with the pod/service/join
// networks unless a test moves them deliberately.
func r10KubeOVNSite(network string) string {
	site := strings.Replace(r06SiteA, "stack: kcn", "stack: kubeovn", 1)
	// cert-manager stays off: the kubeovn image table (the one a kubeovn
	// artifact ships) carries no certTLS verification image — the same
	// selection TestRenderSiteAcrossSelections makes for kubeovn-minimal.
	site = strings.Replace(site, "certManager: {enabled: true}", "certManager: {enabled: false}", 1)
	return strings.Replace(site,
		"podCIDR: 10.16.0.0/16\n  serviceCIDR: 10.96.0.0/16\n  kcn: {managedDevices: [ens35], encapNetworks: [192.0.2.0/24], intranetNetworks: [192.0.2.0/24, 10.96.0.0/16]}",
		network, 1)
}

// r10MustFail validates site and requires the error to mention every fragment.
func r10MustFail(t *testing.T, site string, want ...string) {
	t.Helper()
	config, err := ParseClusterConfig([]byte(site))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	err = Validate(config)
	if err == nil {
		t.Fatalf("config must be rejected, got nil error")
	}
	for _, fragment := range want {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("error = %v, want it to mention %q", err, fragment)
		}
	}
}

// r10KubeOVNTable loads the shipped kubeovn image table — the one a real
// kubeovn-stack run uses.
func r10KubeOVNTable(t *testing.T) ImageTable {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "ani", "images-kubeovn.tsv"))
	if err != nil {
		t.Fatalf("read images-kubeovn.tsv: %v", err)
	}
	table, err := LoadImageTable(strings.Split(string(raw), "\n"))
	if err != nil {
		t.Fatalf("parse images-kubeovn.tsv: %v", err)
	}
	return table
}

// r10KubeOVNContext renders the KubeKey config and returns .ani.network.kubeovn.
func r10KubeOVNContext(t *testing.T, site string) map[string]any {
	t.Helper()
	config, err := ParseClusterConfig([]byte(site))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(config); err != nil {
		t.Fatalf("validate: %v", err)
	}
	spec, err := KubeKeyConfig(config, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", r10KubeOVNTable(t))
	if err != nil {
		t.Fatalf("KubeKeyConfig: %v", err)
	}
	network := spec["ani"].(map[string]any)["network"].(map[string]any)
	return network["kubeovn"].(map[string]any)
}

// T-R10-01: the gateway is derived from the pod CIDR when
// network.kubeovn.defaultGateway is empty — 10.244.0.0/16 renders
// 10.244.0.1, the historical 10.16.0.0/16 keeps 10.16.0.1 — and an explicit
// gateway wins. The resolved values, not literals, reach the render.
func TestKubeOVNGateway(t *testing.T) {
	// Non-default pod CIDR derives its own first usable address.
	got := r10KubeOVNContext(t, r10KubeOVNSite("podCIDR: 10.244.0.0/16\n  serviceCIDR: 10.96.0.0/16"))
	if got["default_gateway"] != "10.244.0.1" {
		t.Fatalf("podCIDR 10.244.0.0/16 with empty defaultGateway must derive 10.244.0.1, got %v", got["default_gateway"])
	}
	if got["join_cidr"] != "172.19.0.0/16" {
		t.Fatalf("empty joinCIDR must keep the historical 172.19.0.0/16, got %v", got["join_cidr"])
	}

	// The historical default pod CIDR keeps the historical gateway.
	got = r10KubeOVNContext(t, r10KubeOVNSite("podCIDR: 10.16.0.0/16\n  serviceCIDR: 10.96.0.0/16"))
	if got["default_gateway"] != "10.16.0.1" {
		t.Fatalf("podCIDR 10.16.0.0/16 with empty defaultGateway must derive 10.16.0.1, got %v", got["default_gateway"])
	}

	// An explicit gateway inside the pod network wins, canonically spelled.
	got = r10KubeOVNContext(t, r10KubeOVNSite("podCIDR: 10.244.0.0/16\n  serviceCIDR: 10.96.0.0/16\n  kubeovn: {defaultGateway: 10.244.128.254, joinCIDR: 172.19.0.0/16}"))
	if got["default_gateway"] != "10.244.128.254" {
		t.Fatalf("explicit defaultGateway must win, got %v", got["default_gateway"])
	}

	// An explicit join CIDR is rendered canonically.
	got = r10KubeOVNContext(t, r10KubeOVNSite("podCIDR: 10.244.0.0/16\n  serviceCIDR: 10.96.0.0/16\n  kubeovn: {joinCIDR: 172.20.0.0/22}"))
	if got["join_cidr"] != "172.20.0.0/22" {
		t.Fatalf("explicit joinCIDR must be kept, got %v", got["join_cidr"])
	}

	// The rendered kubeovn manifest carries the resolved values end to end.
	site := r10KubeOVNSite("podCIDR: 10.244.0.0/16\n  serviceCIDR: 10.96.0.0/16")
	config, err := ParseClusterConfig([]byte(site))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(config); err != nil {
		t.Fatalf("validate: %v", err)
	}
	files, err := RenderSite("../../builtin/core/roles/ani", config, "/opt/ani", r10KubeOVNTable(t))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var manifest *RenderedFile
	var envoyRendered bool
	for i, file := range files {
		if file.Name == "kubeovn-kubeovn-install.yaml" {
			manifest = &files[i]
		}
		if strings.Contains(file.Name, "envoy") {
			envoyRendered = true
		}
	}
	if manifest == nil {
		t.Fatalf("the kubeovn install manifest was not rendered; got %d files", len(files))
	}
	for _, want := range []string{
		"--default-cidr=10.244.0.0/16",
		"--default-gateway=10.244.0.1",
		"--node-switch-cidr=172.19.0.0/16",
	} {
		if !strings.Contains(string(manifest.Rendered), want) {
			t.Fatalf("rendered manifest missing %q", want)
		}
	}
	// T-R10-03 (half): the kubeovn stack renders no envoy role file at all.
	if envoyRendered {
		t.Fatalf("the kubeovn stack must not render any envoy file")
	}
}

// T-R10-03: with LB still B01 work, the LB configuration does not exist at the
// config layer (strict decoding rejects it), the rendered kubeovn manifest
// carries no B01 LB resources and no Envoy appears anywhere.
func TestKubeOVNLBAndEnvoyStayOut(t *testing.T) {
	// The blueprint §6.3 loadBalancer/multus fields are B01 scope; strict
	// decoding must reject them so no LB resource can be configured yet.
	site := r10KubeOVNSite("podCIDR: 10.16.0.0/16\n  serviceCIDR: 10.96.0.0/16\n  kubeovn: {loadBalancer: {enabled: true}}")
	if _, err := ParseClusterConfig([]byte(site)); err == nil {
		t.Fatal("network.kubeovn.loadBalancer is B01 scope and must be rejected until that task lands")
	} else if !strings.Contains(err.Error(), "loadBalancer") {
		t.Fatalf("error = %v, want it to name loadBalancer", err)
	}

	site = r10KubeOVNSite("podCIDR: 10.16.0.0/16\n  serviceCIDR: 10.96.0.0/16\n  multus: {enabled: true}")
	if _, err := ParseClusterConfig([]byte(site)); err == nil {
		t.Fatal("network.multus is B01 scope and must be rejected until that task lands")
	} else if !strings.Contains(err.Error(), "multus") {
		t.Fatalf("error = %v, want it to name multus", err)
	}

	// Rendered manifest: no B01 LB attachment/subnet resources.
	config, err := ParseClusterConfig([]byte(r10KubeOVNSite("podCIDR: 10.16.0.0/16\n  serviceCIDR: 10.96.0.0/16")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(config); err != nil {
		t.Fatalf("validate: %v", err)
	}
	files, err := RenderSite("../../builtin/core/roles/ani", config, "/opt/ani", r10KubeOVNTable(t))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, file := range files {
		if strings.Contains(file.Name, "envoy") {
			t.Fatalf("rendered file %s must not exist on the kubeovn stack", file.Name)
		}
		if file.Name != "kubeovn-kubeovn-install.yaml" {
			continue
		}
		rendered := string(file.Rendered)
		for _, stale := range []string{"ani-lb-external", "attachmentName"} {
			if strings.Contains(rendered, stale) {
				t.Fatalf("rendered manifest contains B01 LB value %q", stale)
			}
		}
	}
}

// T-R10-02 (overlap): pod, service and join networks must be pairwise
// disjoint, duplicates rejected, and node management addresses must stay
// outside all three. A gateway from another network is out of bounds.
func TestNetworkCIDROverlap(t *testing.T) {
	cases := []struct {
		name    string
		network string
		want    []string
	}{
		{
			name:    "pod duplicates service",
			network: "podCIDR: 10.16.0.0/16\n  serviceCIDR: 10.16.0.0/16",
			want:    []string{"overlaps", "network.serviceCIDR"},
		},
		{
			name:    "service inside pod",
			network: "podCIDR: 10.16.0.0/16\n  serviceCIDR: 10.16.128.0/17",
			want:    []string{"overlaps"},
		},
		{
			name:    "join overlaps pod",
			network: "podCIDR: 10.16.0.0/16\n  serviceCIDR: 10.96.0.0/16\n  kubeovn: {joinCIDR: 10.16.0.0/17}",
			want:    []string{"overlaps", "joinCIDR"},
		},
		{
			name:    "join overlaps service",
			network: "podCIDR: 10.16.0.0/16\n  serviceCIDR: 10.96.0.0/16\n  kubeovn: {joinCIDR: 10.96.0.0/17}",
			want:    []string{"overlaps", "joinCIDR"},
		},
		{
			name:    "node ip inside pod network",
			network: "podCIDR: 192.0.2.0/24\n  serviceCIDR: 10.96.0.0/16",
			want:    []string{"node", "inside", "network.podCIDR"},
		},
		{
			name:    "node ip inside service network",
			network: "podCIDR: 10.16.0.0/16\n  serviceCIDR: 192.0.2.0/24",
			want:    []string{"node", "inside", "network.serviceCIDR"},
		},
		{
			name:    "node ip inside join network",
			network: "podCIDR: 10.16.0.0/16\n  serviceCIDR: 10.96.0.0/16\n  kubeovn: {joinCIDR: 192.0.2.0/24}",
			want:    []string{"node", "inside", "joinCIDR"},
		},
		{
			name:    "gateway from another network",
			network: "podCIDR: 10.244.0.0/16\n  serviceCIDR: 10.96.0.0/16\n  kubeovn: {defaultGateway: 10.16.0.1}",
			want:    []string{"outside the pod network"},
		},
		{
			name:    "gateway is the pod network address",
			network: "podCIDR: 10.244.0.0/16\n  serviceCIDR: 10.96.0.0/16\n  kubeovn: {defaultGateway: 10.244.0.0}",
			want:    []string{"network address"},
		},
		{
			name:    "default join collides with the site plan",
			network: "podCIDR: 172.19.0.0/16\n  serviceCIDR: 10.96.0.0/16",
			want:    []string{"overlaps", "joinCIDR"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r10MustFail(t, r10KubeOVNSite(tc.network), tc.want...)
		})
	}
}

// T-R10-02 (format): the kubeovn stack accepts canonical IPv4 networks only —
// IPv6, non-canonical spellings and /31 //32 networks are rejected per
// contract, for pod, service, join and gateway alike.
func TestIPv4Only(t *testing.T) {
	cases := []struct {
		name    string
		network string
		want    []string
	}{
		{
			name:    "ipv6 pod cidr",
			network: "podCIDR: 2001:db8::/32\n  serviceCIDR: 10.96.0.0/16",
			want:    []string{"IPv4"},
		},
		{
			name:    "ipv6 service cidr",
			network: "podCIDR: 10.16.0.0/16\n  serviceCIDR: 2001:db8::/32",
			want:    []string{"IPv4"},
		},
		{
			name:    "ipv6 join cidr",
			network: "podCIDR: 10.16.0.0/16\n  serviceCIDR: 10.96.0.0/16\n  kubeovn: {joinCIDR: 2001:db8::/32}",
			want:    []string{"IPv4"},
		},
		{
			name:    "ipv6 gateway",
			network: "podCIDR: 10.16.0.0/16\n  serviceCIDR: 10.96.0.0/16\n  kubeovn: {defaultGateway: 2001:db8::1}",
			want:    []string{"IPv4"},
		},
		{
			name:    "non-canonical pod cidr",
			network: "podCIDR: 10.16.0.1/16\n  serviceCIDR: 10.96.0.0/16",
			want:    []string{"canonical", "10.16.0.0/16"},
		},
		{
			name:    "non-canonical service cidr",
			network: "podCIDR: 10.16.0.0/16\n  serviceCIDR: 10.96.0.1/16",
			want:    []string{"canonical", "10.96.0.0/16"},
		},
		{
			name:    "non-canonical join cidr",
			network: "podCIDR: 10.16.0.0/16\n  serviceCIDR: 10.96.0.0/16\n  kubeovn: {joinCIDR: 172.19.0.5/16}",
			want:    []string{"canonical", "172.19.0.0/16"},
		},
		{
			name:    "pod cidr too small /31",
			network: "podCIDR: 10.16.0.0/31\n  serviceCIDR: 10.96.0.0/16",
			want:    []string{"too small"},
		},
		{
			name:    "pod cidr too small /32",
			network: "podCIDR: 10.16.0.1/32\n  serviceCIDR: 10.96.0.0/16",
			want:    []string{"too small"},
		},
		{
			name:    "service cidr too small",
			network: "podCIDR: 10.16.0.0/16\n  serviceCIDR: 10.96.0.0/32",
			want:    []string{"too small"},
		},
		{
			name:    "join cidr too small",
			network: "podCIDR: 10.16.0.0/16\n  serviceCIDR: 10.96.0.0/16\n  kubeovn: {joinCIDR: 172.19.0.0/31}",
			want:    []string{"too small"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r10MustFail(t, r10KubeOVNSite(tc.network), tc.want...)
		})
	}
}

// The kubeovn contract binds the kubeovn stack only: a kcn site keeps its
// current behavior, including carrying (ignored) kubeovn subsection values.
func TestKubeOVNSectionIgnoredUnderKCNStack(t *testing.T) {
	site := strings.Replace(r06SiteA,
		"kcn: {managedDevices: [ens35], encapNetworks: [192.0.2.0/24], intranetNetworks: [192.0.2.0/24, 10.96.0.0/16]}",
		"kcn: {managedDevices: [ens35], encapNetworks: [192.0.2.0/24], intranetNetworks: [192.0.2.0/24, 10.96.0.0/16]}\n  kubeovn: {joinCIDR: 10.16.0.0/17}", 1)
	config, err := ParseClusterConfig([]byte(site))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(config); err != nil {
		t.Fatalf("a kcn site must ignore the kubeovn subsection: %v", err)
	}
	config2, err := ParseClusterConfig([]byte(site))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	spec, err := KubeKeyConfig(config2, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", r08FullTable(t))
	if err != nil {
		t.Fatalf("KubeKeyConfig: %v", err)
	}
	network := spec["ani"].(map[string]any)["network"].(map[string]any)
	got := network["kubeovn"].(map[string]any)
	if got["join_cidr"] != "10.16.0.0/17" {
		t.Fatalf("the ignored subsection values must still render for template safety, got %v", got["join_cidr"])
	}
}

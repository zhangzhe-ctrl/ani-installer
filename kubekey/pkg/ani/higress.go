package ani

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	HigressVersion         = "2.2.4"
	HigressChartPath       = "charts/higress/2.2.4.tgz"
	HigressChartSHA256     = "c048d97805f7925d74051674e1f639fa178e5b7ce4eabd893d03b02eff653787"
	HigressControllerImage = "higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/higress:2.2.4"
	HigressPilotImage      = "higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/pilot:2.2.4"
	HigressGatewayImage    = "higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/gateway:2.2.4"
)

var higressImageApproval = map[string]struct{ platform, index string }{
	HigressControllerImage: {"sha256:ab9012ff6ff08135d44696cf8b44cb9b7070d22067ec7d0d9be2a5410ab2e885", "sha256:0a5b7809a107cbec150f41352a156e31160b8a92df787798e2a9a06cdd5587da"},
	HigressPilotImage:      {"sha256:47f3897770e6ee65d27826233efac970b9b5cce53f11d178d062129d3efbb921", "sha256:f742ed20f938c5c1eaf6f8c36c6481a87052d06e903ab6cb0c079165ac0c8284"},
	HigressGatewayImage:    {"sha256:679841c12ba34efa15f47402156195c2abbd4a58327a28c2367ea4a219ff6b2a", "sha256:3dbd609df5db3fca61653eafe0e2310705e485190c4f8cd02d9aab8f07dcf329"},
}

func higressSpec(h HigressComponent, clusterName string) map[string]any {
	probe := fmt.Sprintf("ani-higress-probe-%x", sha256.Sum256([]byte(clusterName)))[:28]
	allowed := append([]string(nil), h.AllowedNamespaces...)
	if !containsString(allowed, probe) {
		allowed = append(allowed, probe)
	}
	return map[string]any{"enabled": h.Enabled, "version": HigressVersion,
		"chart_path": HigressChartPath, "chart_sha256": HigressChartSHA256,
		"gateway_class": h.GatewayClass, "gateway_name": h.GatewayName,
		"http_port": h.HTTPPort, "https_port": h.HTTPSPort, "hostname": h.Hostname,
		"tls_secret": h.TLSSecret, "allowed_namespaces": allowed, "probe_namespace": probe,
		"wasm_registry": h.WasmRegistry, "probe_key_file": h.ProbeKeyFile}
}

// Higress is selected independently. HTTPSPort remains reserved until the
// named TLS Secret is verified and an HTTPS listener is configured.
type HigressComponent struct {
	Enabled           bool     `yaml:"enabled"`
	GatewayClass      string   `yaml:"gatewayClass,omitempty"`
	GatewayName       string   `yaml:"gatewayName,omitempty"`
	HTTPPort          int      `yaml:"httpPort,omitempty"`
	HTTPSPort         int      `yaml:"httpsPort,omitempty"`
	Hostname          string   `yaml:"hostname,omitempty"`
	TLSSecret         string   `yaml:"tlsSecret,omitempty"`
	AllowedNamespaces []string `yaml:"allowedNamespaces,omitempty"`
	// Harbor uses its verified CA. Material selects only this site's exact
	// bootstrap registry authority, whose existing transport is HTTP.
	WasmRegistry string `yaml:"wasmRegistry,omitempty"`
	ProbeKeyFile string `yaml:"probeKeyFile,omitempty"`
}

func effectiveHigress(c ClusterConfig) HigressComponent {
	h := c.Components.Higress
	if !h.Enabled {
		return h
	}
	if h.GatewayClass == "" {
		h.GatewayClass = "higress"
	}
	if h.GatewayName == "" {
		h.GatewayName = "ani-higress"
	}
	if h.HTTPPort == 0 {
		h.HTTPPort = 30445
	}
	if h.HTTPSPort == 0 {
		h.HTTPSPort = 30446
	}
	if len(h.AllowedNamespaces) == 0 {
		h.AllowedNamespaces = []string{"ani-system"}
	}
	if h.WasmRegistry == "" {
		h.WasmRegistry = "material"
		if c.Components.Harbor.Enabled {
			h.WasmRegistry = "harbor"
		}
	}
	return h
}

func validateHigress(c ClusterConfig) error {
	h := effectiveHigress(c)
	if !h.Enabled {
		return nil
	}
	if h.HTTPPort < 30000 || h.HTTPPort > 32767 || h.HTTPSPort < 30000 || h.HTTPSPort > 32767 || h.HTTPPort == h.HTTPSPort {
		return fmt.Errorf("Higress httpPort and httpsPort must be distinct NodePorts within 30000..32767")
	}
	for _, port := range []int{h.HTTPPort, h.HTTPSPort} {
		if c.KubeflowEnabled() && (port == c.Kubeflow.HTTPPort || port == c.Kubeflow.GRPCPort) {
			return fmt.Errorf("Higress NodePort %d conflicts with Kubeflow", port)
		}
		if c.Components.Harbor.Enabled && (port == 30002 || port == 30003) {
			return fmt.Errorf("Higress NodePort %d conflicts with Harbor", port)
		}
		if port == c.RegistryConfig.Port {
			return fmt.Errorf("Higress NodePort %d conflicts with the declared material registry", port)
		}
	}
	for _, name := range []string{h.GatewayClass, h.GatewayName} {
		if len(name) > 63 || !kubeflowDNSName.MatchString(name) {
			return fmt.Errorf("Higress Gateway identity must use DNS label names")
		}
	}
	if len(h.AllowedNamespaces) > 32 {
		return fmt.Errorf("Higress allows at most 32 explicitly named namespaces")
	}
	seen := map[string]bool{}
	for _, name := range h.AllowedNamespaces {
		if len(name) > 63 || !kubeflowDNSName.MatchString(name) || seen[name] {
			return fmt.Errorf("Higress allowedNamespaces must contain distinct DNS namespace names")
		}
		seen[name] = true
	}
	if h.Hostname != "" {
		hostname := strings.TrimPrefix(h.Hostname, "*.")
		if len(hostname) > 253 {
			return fmt.Errorf("Higress hostname is too long")
		}
		for _, label := range strings.Split(hostname, ".") {
			if len(label) > 63 || !kubeflowDNSName.MatchString(label) {
				return fmt.Errorf("Higress hostname must be a DNS name")
			}
		}
	}
	if h.TLSSecret != "" && (h.Hostname == "" || len(h.TLSSecret) > 63 || !kubeflowDNSName.MatchString(h.TLSSecret)) {
		return fmt.Errorf("Higress HTTPS requires hostname and a named TLS Secret in higress-system")
	}
	switch h.WasmRegistry {
	case "harbor":
		if !c.Components.Harbor.Enabled {
			return fmt.Errorf("Higress wasmRegistry=harbor requires the selected Harbor registry")
		}
	case "material":
	default:
		return fmt.Errorf("Higress wasmRegistry must be harbor or material")
	}
	if h.ProbeKeyFile != "" && !filepath.IsAbs(h.ProbeKeyFile) {
		return fmt.Errorf("Higress probeKeyFile must be an absolute protected credential path")
	}
	return nil
}

package ani

import (
	"fmt"
	"net/url"
	"strings"
)

const placeholderRegistry = "127.0.0.1:5000"
const KCNImageReference = "docker.changqingyun.cn/kubercloud/kc-networking@sha256:494432d2f7b896eb953647166c6aa18d6ea2113b133d40d82127cfcbe416712f"

// The supplied reference is an OCI index. Offline Ubuntu amd64 delivery lands
// its verified platform manifest, preserving the index as source evidence.
const KCNAMD64ManifestDigest = "sha256:26470989d18f7c14ba21823c80709678b44149d7a1ec8282aab1906f11a77234"

// The existing Kube-OVN TSV pin is a multi-platform manifest list. The source
// list bytes select this exact amd64 manifest, also served by the offline pack.
const KubeOVNImagePin = "sha256:82cd6fc07fbc476a532aba710f3758419588d0fb4d62e9dc36978f4f093198e2"
const KubeOVNAMD64ManifestDigest = "sha256:2b505e4dab411036f21985d765ec25fd2c5aff46521a96cf5e5dc964f302e805"

// Image records one actual image in the offline package.
type Image struct {
	Original  string
	HaulerRef string
	Digest    string
	Use       string
}

// ImageTable is loaded from the packaged TSV and used to render explicit local
// image references without relying on a global hostname rewrite.
type ImageTable map[string]Image

// checkRepositoryTag requires every hauler_ref to name exactly one tag: a
// reference without one resolves to an empty manifest path, and a second colon
// would make the "tag" swallow part of the repository.
func checkRepositoryTag(haulerRef string) error {
	path := haulerRef
	if slash := strings.Index(haulerRef, "/"); slash >= 0 {
		path = haulerRef[slash+1:]
	}
	lastSegment := path[strings.LastIndex(path, "/")+1:]
	if strings.Count(lastSegment, ":") != 1 {
		return fmt.Errorf("hauler_ref %q is not a repository:tag reference", haulerRef)
	}
	tag := lastSegment[strings.LastIndex(lastSegment, ":")+1:]
	if tag == "" {
		return fmt.Errorf("hauler_ref %q has an empty tag", haulerRef)
	}
	return nil
}

// LoadImageTable parses the four-column TSV shipped with the package.
func LoadImageTable(rows []string) (ImageTable, error) {
	table := ImageTable{}
	haulerRefs := map[string]string{}
	for line, raw := range rows {
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.HasPrefix(raw, "original_ref\t") {
			continue
		}
		fields := strings.Split(raw, "\t")
		if len(fields) != 4 {
			return nil, fmt.Errorf("images.tsv line %d has %d fields, want 4", line+1, len(fields))
		}
		img := Image{
			Original:  fields[0],
			HaulerRef: fields[1],
			Digest:    fields[2],
			Use:       fields[3],
		}
		if img.Original == "" || img.HaulerRef == "" || img.Digest == "" || img.Use == "" {
			return nil, fmt.Errorf("images.tsv line %d has an empty required field", line+1)
		}
		if !strings.HasPrefix(img.HaulerRef, placeholderRegistry+"/") {
			return nil, fmt.Errorf("images.tsv line %d hauler_ref %q does not use %s", line+1, img.HaulerRef, placeholderRegistry)
		}
		if _, exists := table[img.Original]; exists {
			return nil, fmt.Errorf("images.tsv duplicate original_ref %q", img.Original)
		}
		if previous, exists := haulerRefs[img.HaulerRef]; exists {
			// Two rows sharing one store tag would land one object and leave the
			// other's approved bytes unreachable.
			return nil, fmt.Errorf("images.tsv duplicate hauler_ref %q (rows %q and %q); one packaged reference is one image",
				img.HaulerRef, previous, img.Original)
		}
		haulerRefs[img.HaulerRef] = img.Original
		if err := checkRepositoryTag(img.HaulerRef); err != nil {
			return nil, fmt.Errorf("images.tsv line %d: %w", line+1, err)
		}
		table[img.Original] = img
	}
	if len(table) == 0 {
		return nil, fmt.Errorf("images.tsv is empty")
	}
	return table, nil
}

// LocalReference maps an original image reference to the installer registry
// address used for this run.
func (t ImageTable) LocalReference(original, registry string) (string, error) {
	img, ok := t[original]
	if !ok {
		return "", fmt.Errorf("image %q is not listed in images.tsv", original)
	}
	if !strings.HasPrefix(img.HaulerRef, placeholderRegistry+"/") {
		return "", fmt.Errorf("image %q has invalid hauler_ref %q", original, img.HaulerRef)
	}
	return strings.Replace(img.HaulerRef, placeholderRegistry, registry, 1), nil
}

// ComponentImageParts is SplitImageReferences with the installer's fixed key
// list. It is exported so a render check can build the same context the roles
// see without duplicating the key names.
func ComponentImageParts(table ImageTable, registry string) (map[string]any, error) {
	return SplitImageReferences(table, registry, componentImageKeys())
}

// ManifestURL returns the registry URL for the hauler_ref without doing any
// image content rewrite.
func ManifestURL(haulerRef string) (string, error) {
	if !strings.HasPrefix(haulerRef, placeholderRegistry+"/") {
		return "", fmt.Errorf("hauler_ref %q does not start with %s", haulerRef, placeholderRegistry)
	}
	parts := strings.SplitN(strings.TrimPrefix(haulerRef, placeholderRegistry+"/"), ":", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("hauler_ref %q does not contain a tag", haulerRef)
	}
	return "/v2/" + pathSegmentsEscape(parts[0]) + "/manifests/" + url.PathEscape(parts[1]), nil
}
func pathSegmentsEscape(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// SplitReference breaks a local reference into the parts a Helm chart
// concatenates itself. Several charts build "registry/repository:tag", so
// handing them a whole reference in the registry field produces a doubled path
// that can never be pulled.
func SplitReference(ref string) (registry, repository, tag string, err error) {
	slash := strings.Index(ref, "/")
	if slash < 0 {
		return "", "", "", fmt.Errorf("image reference %q has no repository", ref)
	}
	registry = ref[:slash]
	rest := ref[slash+1:]
	// A tag is the last colon that appears after the last slash. Anything
	// before that (including a registry port) is not a tag.
	tagSep := strings.LastIndex(rest, ":")
	if tagSep < 0 {
		return "", "", "", fmt.Errorf("image reference %q has no tag", ref)
	}
	repository = rest[:tagSep]
	tag = rest[tagSep+1:]
	if registry == "" || repository == "" || tag == "" {
		return "", "", "", fmt.Errorf("image reference %q is incomplete", ref)
	}
	return registry, repository, tag, nil
}

// ImageKey names one entry of SplitImageReferences. The values are the
// template-facing names, so a role reads .ani.image_parts.metrics.prometheus
// and the installer never repeats a repository or tag string twice.
//
// TagOverride replaces the tag taken from the locked reference. It is only for
// charts that append a suffix themselves; when empty the locked tag is used.
//
// Backend marks log-stack entries whose requirement depends on the selected
// log backend ("loki", "opensearch", "fluent-bit"); an empty Backend means the
// entry's requirement is decided by the group filter (R08).
type ImageKey struct {
	Group       string
	Name        string
	Original    string
	TagOverride string
	Backend     string
}

// componentImageKeys lists the images whose references a Chart divides up
// itself. Each entry names the image as the lock file does, and the parts are
// read from the same images.tsv the whole-reference map comes from, so a
// version bump only happens in one place.
//
// TagOverride exists for the images whose tag in the lock carries a suffix the
// Chart adds back. A chart that appends "-distroless" when distroless is true
// must be given the plain version, or the tag doubles up and the reference
// never resolves. The override is stated next to the locked entry it applies to
// rather than in the role template, so there is one place to read.
func componentImageKeys() []ImageKey {
	return append([]ImageKey{
		// R08: the two main CNIs and the foundation components now have real,
		// declared keys (every original exists in images.tsv / images-kubeovn.tsv
		// exactly as shipped), so a required image that is missing from the
		// package fails before deployment instead of rendering an empty field.
		{Group: "kcn", Name: "networking", Original: KCNImageReference},
		{Group: "kubeovn", Name: "kubeOvn", Original: "docker.io/kubeovn/kube-ovn:v1.16.6"},
		{Group: "kubeovn", Name: "vpcNatGateway", Original: "docker.io/kubeovn/vpc-nat-gateway:v1.16.6"},
		{Group: "multus", Name: "daemon", Original: "ghcr.io/k8snetworkplumbingwg/multus-cni:v4.3.1-thick"},
		{Group: "components", Name: "postgres", Original: "docker.io/library/postgres:17.11-bookworm"},
		{Group: "components", Name: "valkey", Original: "docker.io/valkey/valkey:8.1.10-alpine"},
		{Group: "components", Name: "nats", Original: "docker.io/library/nats:2.14.6-alpine"},
		{Group: "components", Name: "natsConfigReloader", Original: "docker.io/natsio/nats-server-config-reloader:0.23.0"},
		{Group: "components", Name: "natsBox", Original: "docker.io/natsio/nats-box:0.19.7"},
		{Group: "milvus", Name: "server", Original: "docker.io/milvusdb/milvus:v2.6.24"},
		{Group: "milvus", Name: "etcd", Original: "docker.io/milvusdb/etcd:3.5.25-r1"},
		{Group: "milvus", Name: "s3Client", Original: "docker.io/amazon/aws-cli:2.31.30"},
		{Group: "milvus", Name: "checker", Original: "ani.local/milvus-checker:v1"},
		{Group: "rustfs", Name: "server", Original: "docker.io/rustfs/rustfs:1.0.0"},
		{Group: "rustfs", Name: "init", Original: "docker.io/library/busybox:1.37.0"},
		{Group: "metrics-server", Name: "server", Original: "registry.k8s.io/metrics-server/metrics-server:v0.9.0"},
		{Group: "snapshot-controller", Name: "server", Original: "registry.k8s.io/sig-storage/snapshot-controller:v8.5.0"},
		{Group: "kubevirt", Name: "virt_operator", Original: "quay.io/kubevirt/virt-operator:v1.9.0"},
		{Group: "kubevirt", Name: "virt_api", Original: "quay.io/kubevirt/virt-api:v1.9.0"},
		{Group: "kubevirt", Name: "virt_controller", Original: "quay.io/kubevirt/virt-controller:v1.9.0"},
		{Group: "kubevirt", Name: "virt_handler", Original: "quay.io/kubevirt/virt-handler:v1.9.0"},
		{Group: "kubevirt", Name: "virt_launcher", Original: "quay.io/kubevirt/virt-launcher:v1.9.0"},
		{Group: "kubevirt", Name: "virt_exportproxy", Original: "quay.io/kubevirt/virt-exportproxy:v1.9.0"},
		{Group: "kubevirt", Name: "virt_exportserver", Original: "quay.io/kubevirt/virt-exportserver:v1.9.0"},
		{Group: "kubevirt", Name: "virt_synchronization_controller", Original: "quay.io/kubevirt/virt-synchronization-controller:v1.9.0"},
		{Group: "kubevirt", Name: "pr_helper", Original: "quay.io/kubevirt/pr-helper:v1.9.0"},
		{Group: "kubevirt", Name: "sidecar_shim", Original: "quay.io/kubevirt/sidecar-shim:v1.9.0"},
		{Group: "kubevirt", Name: "cdi_operator", Original: "quay.io/kubevirt/cdi-operator:v1.66.1"},
		{Group: "kubevirt", Name: "cdi_controller", Original: "quay.io/kubevirt/cdi-controller:v1.66.1"},
		{Group: "kubevirt", Name: "cdi_importer", Original: "quay.io/kubevirt/cdi-importer:v1.66.1"},
		{Group: "kubevirt", Name: "cdi_cloner", Original: "quay.io/kubevirt/cdi-cloner:v1.66.1"},
		{Group: "kubevirt", Name: "cdi_apiserver", Original: "quay.io/kubevirt/cdi-apiserver:v1.66.1"},
		{Group: "kubevirt", Name: "cdi_uploadserver", Original: "quay.io/kubevirt/cdi-uploadserver:v1.66.1"},
		{Group: "kubevirt", Name: "cdi_uploadproxy", Original: "quay.io/kubevirt/cdi-uploadproxy:v1.66.1"},
		{Group: "harbor", Name: "nginx_photon", Original: "docker.io/goharbor/nginx-photon:v2.15.2"},
		{Group: "harbor", Name: "harbor_portal", Original: "docker.io/goharbor/harbor-portal:v2.15.2"},
		{Group: "harbor", Name: "harbor_core", Original: "docker.io/goharbor/harbor-core:v2.15.2"},
		{Group: "harbor", Name: "harbor_jobservice", Original: "docker.io/goharbor/harbor-jobservice:v2.15.2"},
		{Group: "harbor", Name: "registry_photon", Original: "docker.io/goharbor/registry-photon:v2.15.2"},
		{Group: "harbor", Name: "harbor_registryctl", Original: "docker.io/goharbor/harbor-registryctl:v2.15.2"},
		{Group: "harbor", Name: "trivy_adapter_photon", Original: "docker.io/goharbor/trivy-adapter-photon:v2.15.2"},
		{Group: "harbor", Name: "harbor_db", Original: "docker.io/goharbor/harbor-db:v2.15.2"},
		{Group: "harbor", Name: "valkey_photon", Original: "docker.io/goharbor/valkey-photon:v2.15.2"},
		{Group: "volcano", Name: "controller", Original: "docker.io/volcanosh/vc-controller-manager:v1.15.2"},
		{Group: "volcano", Name: "scheduler", Original: "docker.io/volcanosh/vc-scheduler:v1.15.2"},
		{Group: "volcano", Name: "admission", Original: "docker.io/volcanosh/vc-webhook-manager:v1.15.2"},
		{Group: "verification", Name: "certTLS", Original: "docker.io/alpine/openssl:3.5.4"},
		{Group: "metrics", Name: "operator", Original: "quay.io/prometheus-operator/prometheus-operator:v0.90.1"},
		{Group: "metrics", Name: "configReloader", Original: "quay.io/prometheus-operator/prometheus-config-reloader:v0.90.1"},
		{Group: "metrics", Name: "prometheus", Original: "quay.io/prometheus/prometheus:v3.11.3-distroless"},
		{Group: "metrics", Name: "alertmanager", Original: "quay.io/prometheus/alertmanager:v0.32.1"},
		{
			Group:       "metrics",
			Name:        "nodeExporter",
			Original:    "quay.io/prometheus/node-exporter:v1.11.1-distroless",
			TagOverride: "v1.11.1",
		},
		{Group: "metrics", Name: "kubeStateMetrics", Original: "registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.19.0"},
		{Group: "metrics", Name: "webhookCertgen", Original: "ghcr.io/jkroepke/kube-webhook-certgen:1.8.3"},
		// The log stack. Loki, Fluent Bit and OpenSearch all take a split
		// image: their charts build "registry/repository:tag" themselves the
		// same way the metrics sub-charts do. The OpenSearch chart puts the
		// registry in one field for every image it renders, so its chown init
		// image (the locked busybox) needs its parts as well.
		{Group: "logs", Name: "loki", Original: "docker.io/grafana/loki:3.7.8", Backend: "loki"},
		{Group: "logs", Name: "fluentBit", Original: "cr.fluentbit.io/fluent/fluent-bit:5.1.2", Backend: "fluent-bit"},
		{Group: "logs", Name: "opensearch", Original: "docker.io/opensearchproject/opensearch:3.8.0", Backend: "opensearch"},
		{Group: "lab", Name: "python", Original: "docker.io/library/python:3.13.11-alpine3.23"},
		{Group: "lab", Name: "busybox", Original: "docker.io/library/busybox:1.37.0"},
	}, kubeflowImageKeys()...)
}

// SplitImageReferences turns the listed images into per-name registry,
// repository and tag values. Every entry must exist in the table: a key whose
// image is missing would otherwise render an empty field, and the resulting
// chart would reference a nonsense image rather than fail here.
func SplitImageReferences(table ImageTable, registry string, keys []ImageKey) (map[string]any, error) {
	groups := map[string]any{}
	for _, key := range keys {
		local, err := table.LocalReference(key.Original, registry)
		if err != nil {
			return nil, fmt.Errorf("image key %s/%s: %w", key.Group, key.Name, err)
		}
		_, repository, tag, err := SplitReference(local)
		if err != nil {
			return nil, fmt.Errorf("image key %s/%s: %w", key.Group, key.Name, err)
		}
		if key.TagOverride != "" {
			tag = key.TagOverride
		}
		group, ok := groups[key.Group].(map[string]any)
		if !ok {
			group = map[string]any{}
			groups[key.Group] = group
		}
		group[key.Name] = map[string]any{
			"repository": repository,
			"tag":        tag,
		}
	}
	return groups, nil
}

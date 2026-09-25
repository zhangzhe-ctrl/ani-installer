package ani

import (
	"fmt"
	"net/url"
	"strings"
)

const placeholderRegistry = "127.0.0.1:5000"

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

// LoadImageTable parses the four-column TSV shipped with the package.
func LoadImageTable(rows []string) (ImageTable, error) {
	table := ImageTable{}
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
	return []ImageKey{
		// R08: the two main CNIs and the foundation components now have real,
		// declared keys (every original exists in images.tsv / images-kubeovn.tsv
		// exactly as shipped), so a required image that is missing from the
		// package fails before deployment instead of rendering an empty field.
		{Group: "kcn", Name: "networking", Original: "docker.changqingyun.cn/kubercloud/kc-networking:dev"},
		{Group: "kubeovn", Name: "kubeOvn", Original: "docker.io/kubeovn/kube-ovn:v1.16.6"},
		{Group: "kubeovn", Name: "vpcNatGateway", Original: "docker.io/kubeovn/vpc-nat-gateway:v1.16.6"},
		{Group: "components", Name: "postgres", Original: "docker.io/library/postgres:17.11-bookworm"},
		{Group: "components", Name: "valkey", Original: "docker.io/valkey/valkey:8.1.10-alpine"},
		{Group: "components", Name: "nats", Original: "docker.io/library/nats:2.14.6-alpine"},
		{Group: "components", Name: "natsConfigReloader", Original: "docker.io/natsio/nats-server-config-reloader:0.23.0"},
		{Group: "components", Name: "natsBox", Original: "docker.io/natsio/nats-box:0.19.7"},
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
	}
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

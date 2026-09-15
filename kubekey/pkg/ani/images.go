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

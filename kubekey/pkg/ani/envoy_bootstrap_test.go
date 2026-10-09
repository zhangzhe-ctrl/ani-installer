package ani

import (
	"bytes"
	"io"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestEnvoyRenderedBootstrapStages(t *testing.T) {
	files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), validConfig(), "/offline", r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]map[string]int{}
	for _, file := range files {
		if file.Role != "envoy" || file.Name == "envoy-tasks-main.yaml" || filepath.Ext(file.Name) != ".yaml" {
			continue
		}
		counts[file.Name] = map[string]int{}
		decoder := yaml.NewDecoder(bytes.NewReader(file.Rendered))
		for {
			var value map[string]any
			if err := decoder.Decode(&value); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if kind, ok := value["kind"].(string); ok {
				counts[file.Name][kind]++
			}
		}
	}
	first := counts["envoy-install.yaml"]
	if first["CustomResourceDefinition"] != 20 || first["Namespace"] != 1 || first["Deployment"] != 0 || first["Job"] != 0 {
		t.Fatalf("initial API stage starts runtime before certificates: %v", first)
	}
	if counts["envoy-controller.yaml"]["Deployment"] != 1 || counts["envoy-certgen.yaml"]["Job"] != 1 {
		t.Fatalf("controller/certificate stages absent: %v", counts)
	}
}

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
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

// regexpEmptyImage catches a repository or tag that resolved to nothing in the
// resources Helm produced.
var regexpEmptyImage = regexp.MustCompile(`(?m)^\s*(repository|tag):\s*(""?)\s*$`)

// Chart expansion (F07/T34): rendered values are only a syntax check until the
// published offline chart actually consumes them. This runs the packaged Helm
// over the packaged chart with the rendered values, exactly as the role will,
// and inspects the resources that come out.

// ChartExpansionInput describes one expansion run over rendered values.
type ChartExpansionInput struct {
	// HelmBin is the fixed Helm binary the artifact ships. It is never resolved
	// from PATH: a different Helm on the build host must not decide what the
	// release validated against (F12).
	HelmBin string
	// ChartsRoot is the artifact root (charts/...) or the development tree root
	// (ani/charts/...).
	ChartsRoot string
	// LockPath is the approved materials lock naming each chart's artifact path.
	LockPath string
	// Scratch receives the temporary values files; it is removed afterwards.
	Scratch string
}

// chartFor locates the approved lock entry for a chart name and version.
func chartFor(lock *MaterialsLock, name, version string) (*LockedChart, error) {
	for index := range lock.Charts {
		candidate := &lock.Charts[index]
		if candidate.Name == name && candidate.ChartVersion == version {
			return candidate, nil
		}
	}
	return nil, fmt.Errorf("the materials lock approves no chart %s %s", name, version)
}

// chartArchive resolves a lock artifactChartPath inside a charts root, accepting
// both the artifact layout (charts/…) and the development tree (ani/charts/…).
func chartArchive(chartsRoot, artifactPath string) (string, error) {
	candidates := []string{
		filepath.Join(chartsRoot, filepath.FromSlash(artifactPath)),
		filepath.Join(chartsRoot, "ani", filepath.FromSlash(artifactPath)),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Size() > 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("chart archive %s is not present under %s", artifactPath, chartsRoot)
}

// RunChartExpansion expands every rendered component values file with the
// packaged Helm against its approved offline chart. Each chart is a failure if
// Helm cannot run it, if it produces no resources, if it produces resources
// without a name, or if any of them carries an unrendered or empty value — so a
// values file that merely parses cannot hide a broken final manifest.
func RunChartExpansion(ctx context.Context, input ChartExpansionInput, cluster ClusterConfig, files []RenderedFile, stdout io.Writer) error {
	out := materialWriter(stdout)

	// A selection with no chart-backed component needs no Helm at all: the
	// requirement is checked against what this render actually has to expand, not
	// as a blanket precondition of the render command.
	enabled := map[string]bool{}
	for _, row := range effectiveSelection(cluster) {
		if row.Enabled {
			enabled[row.Name] = true
		}
	}
	expected := 0
	for role, spec := range componentInstallSpecs {
		if spec.Chart != "" && enabled[role] {
			expected++
		}
	}
	if expected == 0 {
		fmt.Fprintf(out, "no chart-backed component is enabled in this selection; chart expansion has nothing to do\n")
		return nil
	}
	if strings.TrimSpace(input.HelmBin) == "" {
		return errors.New("chart expansion needs the artifact's Helm binary: pass --helm pointing at the packaged bin/helm")
	}
	info, err := os.Stat(input.HelmBin)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("--helm %s is not an executable file", input.HelmBin)
	}
	lock, err := LoadMaterialsLock(input.LockPath)
	if err != nil {
		return err
	}
	scratch := strings.TrimSpace(input.Scratch)
	if scratch == "" {
		scratch, err = os.MkdirTemp("", "ani-chart-expansion-")
		if err != nil {
			return errors.Wrap(err, "create the chart expansion scratch directory")
		}
		defer os.RemoveAll(scratch)
	}

	expanded := 0
	for _, file := range files {
		if !strings.HasSuffix(file.Name, "-values.yaml") {
			continue
		}
		role := strings.TrimSuffix(file.Name, "-values.yaml")
		spec, known := componentInstallSpecs[role]
		if !known || spec.Chart == "" {
			continue
		}
		if !enabled[role] {
			return fmt.Errorf("component %q rendered values although it is disabled in this selection", role)
		}
		chart, err := chartFor(lock, spec.Chart, spec.ChartVersion)
		if err != nil {
			return errors.Wrapf(err, "component %q", role)
		}
		archive, err := chartArchive(input.ChartsRoot, chart.ArtifactPath)
		if err != nil {
			return errors.Wrapf(err, "component %q", role)
		}
		valuesFile := filepath.Join(scratch, role+"-values.yaml")
		if err := os.WriteFile(valuesFile, file.Rendered, 0o600); err != nil {
			return errors.Wrapf(err, "write the rendered values of %q", role)
		}
		rendered, err := expandChart(ctx, input.HelmBin, archive, valuesFile, spec, chart)
		if err != nil {
			return errors.Wrapf(err, "component %q: helm template", role)
		}
		if err := checkExpandedResources(role, rendered); err != nil {
			return err
		}
		expanded++
		fmt.Fprintf(out, "chart %s %s expanded for component %q: %d resource(s) rendered by %s\n",
			spec.Chart, spec.ChartVersion, role, countDocuments(rendered), input.HelmBin)
	}
	if expanded != expected {
		return fmt.Errorf("chart expansion covered %d of the %d enabled chart-backed components; a values file is missing",
			expanded, expected)
	}
	return nil
}

// expandChart runs one `helm template` with the same release, namespace and
// values file the role will use.
func expandChart(ctx context.Context, helmBin, archive, valuesFile string, spec componentInstallSpec, chart *LockedChart) (string, error) {
	namespace := spec.Namespace
	if chart.Namespace != "" {
		namespace = chart.Namespace
	}
	release := spec.Release
	if chart.Release != "" {
		release = chart.Release
	}
	if release == "" {
		return "", fmt.Errorf("neither the install spec nor the lock names a release for chart %s", chart.Name)
	}
	command := exec.CommandContext(ctx, helmBin, "template", release, archive,
		"--namespace", namespace, "--values", valuesFile)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func countDocuments(rendered string) int {
	count := 0
	for _, document := range strings.Split(rendered, "\n---\n") {
		if strings.TrimSpace(document) != "" {
			count++
		}
	}
	return count
}

// checkExpandedResources inspects what Helm produced: real, individually parsed
// resources with identity, and no trace of a value that resolved to nothing.
func checkExpandedResources(role, rendered string) error {
	resources := 0
	for index, document := range strings.Split(rendered, "\n---\n") {
		if strings.TrimSpace(document) == "" {
			continue
		}
		var resource struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
			Metadata   struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(document), &resource); err != nil {
			return fmt.Errorf("component %q: resource document %d from Helm does not parse: %v", role, index+1, err)
		}
		if resource.Kind == "" && resource.APIVersion == "" {
			continue
		}
		if resource.Kind == "" || resource.Metadata.Name == "" {
			return fmt.Errorf("component %q: resource document %d has no kind or metadata.name", role, index+1)
		}
		resources++
	}
	if resources == 0 {
		return fmt.Errorf("component %q: Helm rendered no Kubernetes resources from the approved chart", role)
	}
	if strings.Contains(rendered, "<no value>") || strings.Contains(rendered, "{{") {
		return fmt.Errorf("component %q: the expanded chart still carries an unresolved value", role)
	}
	if regexpEmptyImage.MatchString(rendered) {
		return fmt.Errorf("component %q: the expanded chart renders an empty image reference", role)
	}
	return nil
}

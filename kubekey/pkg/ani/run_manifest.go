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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/cockroachdb/errors"
)

// RunManifestSchemaVersion is bumped whenever the manifest layout changes.
const RunManifestSchemaVersion = 1

// Files written by `kk ani validate --output <dir>`.
const (
	RunManifestFileName = "run.json"
	// VerifyFactsFileName carries the same facts in a form a shell can source,
	// so scripts/verify.sh never has to read YAML itself (R06/A09).
	VerifyFactsFileName = "verify-facts.env"
	// ValidationFileName is the R09 card's machine-readable validation record:
	// identical content to run.json under the name the manual checks document.
	ValidationFileName = "validation.json"
)

// RunManifest is the non-secret description of a validated run: everything a
// later step needs to know what was validated, and nothing that could leak a
// credential. It is produced from the parsed config, never from raw YAML text,
// so comments, quoting and key order cannot change it.
type RunManifest struct {
	SchemaVersion int    `json:"schemaVersion"`
	ConfigDigest  string `json:"configDigest"`
	ClusterName   string `json:"clusterName"`
	Profile       string `json:"profile"`
	NetworkStack  string `json:"networkStack"`

	Installer        ManifestInstaller `json:"installer"`
	Nodes            []ManifestNode    `json:"nodes"`
	Components       []string          `json:"components"`
	StorageClass     string            `json:"storageClass"`
	Storage          ManifestStorage   `json:"storage"`
	ComponentClasses map[string]string `json:"componentStorageClasses,omitempty"`

	// PackageRoot is recorded so a later step can find the artifact, but this
	// build only validates the configuration: materials are R07's job.
	PackageRoot        string `json:"packageRoot,omitempty"`
	MaterialsValidated bool   `json:"materialsValidated"`
}

// ManifestInstaller is the installer node and the registry it serves.
type ManifestInstaller struct {
	Name         string `json:"name"`
	Address      string `json:"address"`
	RegistryHost string `json:"registryHost"`
	RegistryPort int    `json:"registryPort"`
}

// ManifestNode is one cluster node; exactly one of them is the installer.
type ManifestNode struct {
	Name      string `json:"name"`
	Address   string `json:"address"`
	Installer bool   `json:"installer"`
}

// ManifestStorage describes the site's storage selection without any device
// secret (devices are paths, not credentials).
type ManifestStorage struct {
	Enabled                 bool                  `json:"enabled"`
	Provider                string                `json:"provider"`
	MakeDefaultStorageClass bool                  `json:"makeDefaultStorageClass"`
	ExternalClass           string                `json:"externalClass,omitempty"`
	Nodes                   []ManifestStorageNode `json:"nodes,omitempty"`
}

// ManifestStorageNode is the per-node device allowlist.
type ManifestStorageNode struct {
	Name    string   `json:"name"`
	Devices []string `json:"devices"`
}

// ConfigDigest hashes the normalised, secret-free configuration, so two files
// that differ only in comments, quoting or key order produce the same digest.
// The ssh password and private key are blanked before hashing and never leave
// this process.
func ConfigDigest(c ClusterConfig) (string, error) {
	clean := c
	clean.SSH.Password = ""
	clean.SSH.PrivateKey = ""
	// encoding/json on a struct is field-order deterministic; the parsed struct
	// has already absorbed quoting, comments and key order.
	canonical, err := json.Marshal(clean)
	if err != nil {
		return "", errors.Wrap(err, "encode the normalised config for its digest")
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// BuildRunManifest turns a validated config into the non-secret manifest.
func BuildRunManifest(c ClusterConfig) (RunManifest, error) {
	digest, err := ConfigDigest(c)
	if err != nil {
		return RunManifest{}, err
	}
	installer, err := c.Installer()
	if err != nil {
		return RunManifest{}, err
	}
	registryAddress, err := c.RegistryAddress()
	if err != nil {
		return RunManifest{}, err
	}
	host, port, err := splitHostPort(registryAddress)
	if err != nil {
		return RunManifest{}, err
	}

	manifest := RunManifest{
		SchemaVersion: RunManifestSchemaVersion,
		ConfigDigest:  digest,
		ClusterName:   c.Name,
		Profile:       installProfile(c.Profile),
		NetworkStack:  networkStack(c.Network.Stack),
		Installer: ManifestInstaller{
			Name:         installer.Name,
			Address:      installer.Address,
			RegistryHost: host,
			RegistryPort: port,
		},
		MaterialsValidated: false,
	}

	for _, node := range c.Nodes {
		manifest.Nodes = append(manifest.Nodes, ManifestNode{
			Name:      node.Name,
			Address:   node.Address,
			Installer: node.Name == installer.Name,
		})
	}

	// Canonical component IDs, in the fixed order the selection file uses.
	classes := map[string]string{}
	for _, row := range c.Components.Selection() {
		if !row.Enabled {
			continue
		}
		manifest.Components = append(manifest.Components, row.Name)
		if component := c.Components.storage(row.Name); component != nil {
			classes[row.Name] = strings.TrimSpace(component.StorageClass)
		}
	}
	if len(classes) > 0 {
		manifest.ComponentClasses = classes
	}

	manifest.Storage = ManifestStorage{
		Enabled:                 c.Storage.Enabled,
		Provider:                c.Storage.provider(),
		MakeDefaultStorageClass: c.Storage.MakeDefaultStorageClass,
		ExternalClass:           strings.TrimSpace(c.Storage.ExternalClass),
	}
	for _, node := range c.Storage.Nodes {
		devices := make([]string, 0, len(node.Devices))
		for _, device := range node.Devices {
			devices = append(devices, strings.TrimSpace(device))
		}
		manifest.Storage.Nodes = append(manifest.Storage.Nodes, ManifestStorageNode{
			Name:    node.Name,
			Devices: devices,
		})
	}
	manifest.StorageClass = effectiveStorageClass(c)
	return manifest, nil
}

// effectiveStorageClass is the class storage-backed components use unless they
// override it: the built-in RBD class for a Ceph install, the site's existing
// class for an external provider, and empty when no storage was selected.
func effectiveStorageClass(c ClusterConfig) string {
	switch {
	case !c.Storage.Enabled:
		return ""
	case c.Storage.provider() == storageProviderExternal:
		return strings.TrimSpace(c.Storage.ExternalClass)
	default:
		return DefaultStorageClass
	}
}

func splitHostPort(address string) (string, int, error) {
	host, portText, found := strings.Cut(address, ":")
	if !found {
		return "", 0, fmt.Errorf("registry address %q has no port", address)
	}
	port, err := strconv.Atoi(strings.TrimSpace(portText))
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("registry address %q has an invalid port %q", address, portText)
	}
	return host, port, nil
}

// VerifyFactsEnv renders the manifest as KEY=value lines a POSIX shell can
// source. Every value is single-quoted with embedded quotes escaped, so a value
// from the site config can never break out into the calling script.
func VerifyFactsEnv(m RunManifest) string {
	pairs := [][2]string{
		{"ANI_MANIFEST_SCHEMA", fmt.Sprint(m.SchemaVersion)},
		{"ANI_CONFIG_DIGEST", m.ConfigDigest},
		{"ANI_CLUSTER_NAME", m.ClusterName},
		{"ANI_PROFILE", m.Profile},
		{"ANI_NETWORK_STACK", m.NetworkStack},
		{"ANI_INSTALLER_NODE", m.Installer.Name},
		{"ANI_INSTALLER_ADDRESS", m.Installer.Address},
		{"ANI_REGISTRY_HOST", m.Installer.RegistryHost},
		{"ANI_REGISTRY_PORT", fmt.Sprint(m.Installer.RegistryPort)},
		{"ANI_REGISTRY", fmt.Sprintf("%s:%d", m.Installer.RegistryHost, m.Installer.RegistryPort)},
		{"ANI_STORAGE_CLASS", m.StorageClass},
		{"ANI_COMPONENTS", strings.Join(m.Components, ",")},
		{"ANI_MATERIALS_VALIDATED", fmt.Sprint(m.MaterialsValidated)},
	}
	// Deterministic output regardless of construction order.
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i][0] < pairs[j][0] })
	var b strings.Builder
	b.WriteString("# Generated by `kk ani validate`. Non-secret site facts, safe to source.\n")
	for _, pair := range pairs {
		fmt.Fprintf(&b, "%s='%s'\n", pair[0], strings.ReplaceAll(pair[1], "'", `'\''`))
	}
	return b.String()
}

// WriteRunOutputs writes run.json and verify-facts.env into dir. The directory
// is created with 0700 because it is evidence for one run; the files themselves
// hold no credentials and are world-readable on purpose, so an operator can
// inspect what was validated.
func WriteRunOutputs(dir string, m RunManifest) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.Wrapf(err, "create output directory %s", dir)
	}
	encoded, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return errors.Wrap(err, "encode the run manifest")
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(filepath.Join(dir, RunManifestFileName), encoded, 0o644); err != nil {
		return errors.Wrapf(err, "write %s", RunManifestFileName)
	}
	facts := VerifyFactsEnv(m)
	if err := os.WriteFile(filepath.Join(dir, VerifyFactsFileName), []byte(facts), 0o644); err != nil {
		return errors.Wrapf(err, "write %s", VerifyFactsFileName)
	}
	if err := os.WriteFile(filepath.Join(dir, ValidationFileName), encoded, 0o644); err != nil {
		return errors.Wrapf(err, "write %s", ValidationFileName)
	}
	return nil
}

// ValidateInput is the input of `kk ani validate`.
type ValidateInput struct {
	ConfigFile  string
	PackageRoot string
	Output      string
}

// RunValidate validates the site configuration and, when Output is set, writes
// the non-secret run manifest and the shell facts file.
//
// This build validates the configuration only. It records PackageRoot but does
// not inspect the artifact: material validation arrives with R07, and until
// then the manifest says materialsValidated=false and the caller prints that
// limitation — an intermediate validate must never be reported as complete.
func RunValidate(_ context.Context, input ValidateInput, stdout io.Writer) error {
	if strings.TrimSpace(input.ConfigFile) == "" {
		return errors.New("validate needs --config")
	}
	cluster, err := LoadClusterConfig(input.ConfigFile)
	if err != nil {
		return err
	}
	if err := Validate(cluster); err != nil {
		return errors.Wrap(err, "validate cluster config")
	}
	manifest, err := BuildRunManifest(cluster)
	if err != nil {
		return err
	}
	manifest.PackageRoot = strings.TrimSpace(input.PackageRoot)

	if strings.TrimSpace(input.Output) != "" {
		if err := WriteRunOutputs(input.Output, manifest); err != nil {
			return err
		}
	}

	out := stdout
	if out == nil {
		out = os.Stdout
	}
	fmt.Fprintf(out, "site config is valid: cluster=%s profile=%s network=%s installer=%s\n",
		manifest.ClusterName, manifest.Profile, manifest.NetworkStack,
		fmt.Sprintf("%s@%s", manifest.Installer.Name, manifest.Installer.Address))
	fmt.Fprintf(out, "config digest: %s\n", manifest.ConfigDigest)
	if len(manifest.Components) > 0 {
		fmt.Fprintf(out, "enabled components: %s\n", strings.Join(manifest.Components, ","))
	}
	if manifest.StorageClass != "" {
		fmt.Fprintf(out, "storage class: %s\n", manifest.StorageClass)
	}
	if strings.TrimSpace(input.Output) != "" {
		fmt.Fprintf(out, "wrote %s and %s in %s\n", RunManifestFileName, VerifyFactsFileName, input.Output)
	}
	fmt.Fprintln(out, "NOTE: this command validates the configuration only (materialsValidated=false); wiring the approved materials lock into the artifact checks is a follow-up card")
	return nil
}

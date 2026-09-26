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

// RecordKind distinguishes what a run.json actually attests (F04). A
// config-validation record proves nothing was installed; only an install-success
// record may be consumed by verification and component additions.
const (
	RecordKindConfigValidation = "config-validation"
	RecordKindInstallSuccess   = "install-success"
)

// ManifestIdentity is the real, per-source identity chain recorded only by a
// successful install. Every field comes from its own authoritative source; the
// artifact package.yaml digest is deliberately NOT used as a stand-in for the
// site or source identity (the old bug).
type ManifestIdentity struct {
	SourceTreeFingerprint string   `json:"sourceTreeFingerprint,omitempty"`
	CodeCommit            string   `json:"codeCommit,omitempty"`
	CodeBinaryDigest      string   `json:"codeBinaryDigest,omitempty"`
	SiteConfigDigest      string   `json:"siteConfigDigest,omitempty"`
	MaterialsLockDigest   string   `json:"materialsLockDigest,omitempty"`
	PackageConfigDigest   string   `json:"packageConfigDigest,omitempty"`
	ClusterUID            string   `json:"clusterUid,omitempty"`
	NodeCount             int      `json:"nodeCount"`
	ReadyNodes            []string `json:"readyNodes,omitempty"`
}

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

	// RecordKind + RunID + Result + Identity are the F04 identity contract.
	// A config-validation record leaves Result empty and Identity zeroed;
	// only a completed first install produces RecordKind=install-success with
	// Result=succeeded and a populated Identity, and only that may be consumed
	// by `kk ani verify` acceptance or `kk ani components` additions.
	RecordKind string           `json:"recordKind,omitempty"`
	RunID      string           `json:"runId,omitempty"`
	Result     string           `json:"result,omitempty"`
	Identity   ManifestIdentity `json:"identity,omitempty"`

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

	// ComponentsExecution is present only on a components-execution record:
	// the subject that one `ani components execute` pass really did against a
	// named base install. It never re-attributes the install itself.
	ComponentsExecution *ComponentsExecution `json:"componentsExecution,omitempty"`
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
		RecordKind:    RecordKindConfigValidation,
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
	// HelmBin pins the Helm used for chart expansion. Empty means the fixed
	// <package-root>/bin/helm the artifact ships; the render never guesses a Helm
	// from PATH (F12), because a different Helm would validate different output.
	HelmBin string
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
	fmt.Fprintln(out, "NOTE: this command validates the site configuration only and writes a config-validation record (materialsValidated=false); artifact materials are verified by `kk ani install`'s preflight, never by this record")
	return nil
}

// isHex64 is a strict 64-lowercase-hex validator so a short or malformed digest
// returns an error instead of panicking a later [:N] slice (F04).
func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// BuildInstallSuccessManifest turns a completed, verified first install into the
// authoritative install-success record. It starts from the validated config
// manifest and overlays the run identity, the accurate final result and the real
// per-source identity chain. MaterialsValidated is only ever true here — a
// validate artifact never claims materials were checked (F04).
func BuildInstallSuccessManifest(base RunManifest, state InstallState, identity ManifestIdentity) (RunManifest, error) {
	if state.RunID == "" {
		return RunManifest{}, errors.New("an install-success record needs a run id")
	}
	if identity.SiteConfigDigest == "" || !isHex64(identity.SiteConfigDigest) {
		return RunManifest{}, fmt.Errorf("install-success identity has no well-formed site digest")
	}
	if identity.ClusterUID == "" {
		return RunManifest{}, errors.New("install-success identity has no live cluster uid")
	}
	m := base
	m.SchemaVersion = RunManifestSchemaVersion
	m.RecordKind = RecordKindInstallSuccess
	m.RunID = state.RunID
	m.Result = state.Result
	m.Identity = identity
	m.MaterialsValidated = true
	return m, nil
}

// WriteInstallSuccessRecord writes the install-success run.json (and the shell
// facts) into dir. It is the sole record that later verification and component
// additions are allowed to consume.
func WriteInstallSuccessRecord(dir string, m RunManifest) error {
	if m.RecordKind != RecordKindInstallSuccess {
		return errors.New("refusing to write a non install-success record as the trusted base run")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.Wrapf(err, "create install record directory %s", dir)
	}
	encoded, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return errors.Wrap(err, "encode the install-success manifest")
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(filepath.Join(dir, RunManifestFileName), encoded, 0o644); err != nil {
		return errors.Wrapf(err, "write %s", RunManifestFileName)
	}
	if err := os.WriteFile(filepath.Join(dir, VerifyFactsFileName), []byte(VerifyFactsEnv(m)), 0o644); err != nil {
		return errors.Wrapf(err, "write %s", VerifyFactsFileName)
	}
	return nil
}

// ValidateSuccessRecord enforces the F04 contract: the run.json must attest a
// COMPLETED, SUCCESSFUL install whose identity chain is well-formed and whose
// site digest matches the (optional) install state. It refuses config-validation
// records, non-success results, malformed/short digests and mismatched
// cluster/digest/identity, always with an error and never a panic. A nil state is
// permitted only when the record is internally complete (identity present).
func ValidateSuccessRecord(m RunManifest, state *InstallState) error {
	if m.ConfigDigest == "" || m.ClusterName == "" {
		return fmt.Errorf("run record is not a run.json (missing configDigest/clusterName)")
	}
	if !isHex64(m.ConfigDigest) {
		return fmt.Errorf("run record configDigest %q is not a 64-hex digest", m.ConfigDigest)
	}
	if m.RecordKind != RecordKindInstallSuccess {
		return fmt.Errorf("run record is %q; verification and component additions require an install-success record produced by a completed first install", m.RecordKind)
	}
	// An execution record re-labelled "install-success" would otherwise be
	// accepted here and skip every base-bytes, evidence and live-cluster check —
	// on the consume path and as the --base-run anchor of a later addition. The
	// block's presence is the structural fact; the declared string is not.
	if m.ComponentsExecution != nil {
		return fmt.Errorf("run record carries a componentsExecution block, so it attests one components execution and not a first install; refusing to use it as an %q record", RecordKindInstallSuccess)
	}
	if m.Result != ResultSucceeded {
		return fmt.Errorf("run record result is %q; verification requires a successful install (result %q)", m.Result, ResultSucceeded)
	}
	if !isHex64(m.Identity.SiteConfigDigest) || m.Identity.SiteConfigDigest != m.ConfigDigest {
		return fmt.Errorf("install-success identity site digest %q does not match the record configDigest %q", m.Identity.SiteConfigDigest, m.ConfigDigest)
	}
	if m.Identity.ClusterUID == "" {
		return errors.New("install-success record has no live cluster identity to bind later operations to")
	}
	if !isHex64(m.Identity.MaterialsLockDigest) {
		return fmt.Errorf("install-success record materials lock digest %q is not well-formed", m.Identity.MaterialsLockDigest)
	}
	if state != nil {
		if state.RunID != m.RunID {
			return fmt.Errorf("install state run %q does not match the record run %q", state.RunID, m.RunID)
		}
		if state.ClusterName != m.ClusterName {
			return fmt.Errorf("install state belongs to cluster %q, the record to %q", state.ClusterName, m.ClusterName)
		}
		if state.Result != ResultSucceeded || state.Phase != PhaseSucceeded {
			return fmt.Errorf("install state is result=%q phase=%q; verification requires a succeeded install", state.Result, state.Phase)
		}
		if state.ConfigDigest != "" && state.ConfigDigest != m.ConfigDigest {
			return fmt.Errorf("install state site digest %q does not match the record %q", state.ConfigDigest, m.ConfigDigest)
		}
	}
	return nil
}

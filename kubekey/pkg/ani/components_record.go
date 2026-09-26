package ani

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/kubesphere/kubekey/v4/version"
)

// This file closes gap L-06: a components addition really executes, but until
// now nothing it produced could be consumed by `kk ani verify`, so an added
// component could not be smoke-verified at all (the plan record is
// config-validation by F04's design, and the old execute report was untyped
// JSON that no loader read). The record here is deliberately narrow: it attests
// one execution against one known base, and it grants only what that
// execution actually proved.

const (
	// RecordKindComponentsExecution attests one `kk ani components execute`
	// pass. It is a distinct subject from the install it extends: consuming it
	// never re-attributes an install to it.
	RecordKindComponentsExecution = "components-execution"

	ComponentsOperationAdd  = "add"
	ComponentsOperationNoop = "noop"

	ComponentsTargetExecuted         = "executed"
	ComponentsTargetAlreadyInstalled = "already_installed"
	ComponentsTargetFailed           = "failed"
	ComponentsTargetCancelled        = "cancelled"
	ComponentsTargetUnknown          = "unknown"

	ComponentsEvidencePlan           = "plan"
	ComponentsEvidenceExecuteReport  = "execute-report"
	ComponentsEvidenceConnections    = "connections"
	ComponentsEvidencePriorExecution = "prior-execution"
)

// RecordResultUnknown is the fourth terminal state a record may carry: the
// action's outcome could not be determined, which is never a pass.
const RecordResultUnknown = "unknown"

// notEmbeddedIdentity mirrors runner.go: a plain `go build` has no embedded
// tree digest and must say so instead of borrowing another identity.
const notEmbeddedIdentity = "not-embedded"

// treeFingerprintForRecord reports the source identity of THIS binary the same
// way the install path does.
func treeFingerprintForRecord() string {
	if strings.TrimSpace(SourceTreeFingerprint) == "" {
		return notEmbeddedIdentity
	}
	return SourceTreeFingerprint
}

// commitForRecord follows the same rule: a build that carries no commit says
// so, instead of recording a blank an attacker (or a later reader) could fill
// in with anything.
func commitForRecord() string {
	if commit := strings.TrimSpace(version.Get().GitCommit); commit != "" {
		return commit
	}
	return notEmbeddedIdentity
}

// ComponentsExecutionTarget is one component's outcome with the resource
// identity that proves who owns it. Without Namespace/Kind/Name/UID a record
// would only claim that a name appeared, which is not ownership.
type ComponentsExecutionTarget struct {
	Component string `json:"component"`
	Status    string `json:"status"`
	Namespace string `json:"namespace,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name,omitempty"`
	UID       string `json:"uid,omitempty"`
	Images    int    `json:"images,omitempty"`
	Charts    int    `json:"charts,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// ComponentsEvidenceRef points at a file whose bytes this record was built from.
// The digest is what makes the pointer checkable instead of decorative.
type ComponentsEvidenceRef struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	RunID  string `json:"runId,omitempty"`
}

// ComponentsExecution hangs off a RunManifest so the record shares every
// identity check an install record already passes: the installer's identity is
// the manifest's Identity, and the base it extends is named explicitly.
type ComponentsExecution struct {
	Operation      string   `json:"operation"`
	DidInstall     bool     `json:"didInstall"`
	RequestedScope []string `json:"requestedScope"`
	Executed       []string `json:"executed,omitempty"`
	ObservedExists []string `json:"observedExisting,omitempty"`

	BaseRunID        string `json:"baseRunId"`
	BaseRecordFile   string `json:"baseRecordFile"`
	BaseRecordSHA256 string `json:"baseRecordSha256"`
	BaseClusterUID   string `json:"baseClusterUid"`
	BaseConfigDigest string `json:"baseConfigDigest"`

	StartedAt  string                      `json:"startedAt"`
	FinishedAt string                      `json:"finishedAt"`
	Targets    []ComponentsExecutionTarget `json:"targets"`
	Evidence   []ComponentsEvidenceRef     `json:"evidence,omitempty"`
}

// ComponentsExecutionFields is where the execution record carries its subject on
// the shared manifest shape.
const ComponentsExecutionFileName = "components-run.json"

// NewComponentsExecutionManifest builds the consumable record of one execution.
// op=add requires at least one executed target; op=noop must have executed
// nothing, which is what keeps a read-only observation from claiming a install.
func NewComponentsExecutionManifest(base RunManifest, plan ComponentsPlan, liveID ManifestIdentity,
	lockDigest, op, result string, targets []ComponentsExecutionTarget, evidence []ComponentsEvidenceRef) (RunManifest, error) {

	if base.RunID == "" || base.RecordKind != RecordKindInstallSuccess {
		return RunManifest{}, errors.New("a components execution record needs an install-success base record")
	}
	if op != ComponentsOperationAdd && op != ComponentsOperationNoop {
		return RunManifest{}, fmt.Errorf("operation %q is neither %q nor %q", op, ComponentsOperationAdd, ComponentsOperationNoop)
	}
	switch result {
	case ResultSucceeded, ResultFailed, ResultCancelled, RecordResultUnknown:
	default:
		return RunManifest{}, fmt.Errorf("result %q is not a state a components record may attest", result)
	}

	var executed, observed []string
	for _, target := range targets {
		switch target.Status {
		case ComponentsTargetExecuted:
			executed = append(executed, target.Component)
		case ComponentsTargetAlreadyInstalled:
			observed = append(observed, target.Component)
		}
	}
	// A pass that installed nothing has already run out of things to do: that
	// IS the no-op, so it records as one even though the operator asked for an
	// add. Silently re-labelling a real add as a noop is what the checks below
	// forbid.
	if len(executed) == 0 && len(observed) > 0 && result == ResultSucceeded {
		op = ComponentsOperationNoop
	}
	if op == ComponentsOperationAdd && result == ResultSucceeded && len(executed) == 0 {
		return RunManifest{}, errors.New("a succeeded add record has nothing executed in it; re-plan instead of recording this")
	}
	if op == ComponentsOperationNoop && len(executed) > 0 {
		return RunManifest{}, errors.New("a noop record may not name an executed component")
	}
	if op == ComponentsOperationNoop && result == ResultSucceeded && len(observed) == 0 {
		return RunManifest{}, errors.New("a succeeded noop record observed nothing; there is nothing to attest")
	}
	for _, target := range targets {
		if target.Namespace == "" || target.Kind == "" || target.Name == "" {
			return RunManifest{}, fmt.Errorf("target %q carries no resource identity to bind ownership to", target.Component)
		}
		if target.Status == ComponentsTargetExecuted || target.Status == ComponentsTargetAlreadyInstalled {
			if target.UID == "" {
				return RunManifest{}, fmt.Errorf("target %q is reported %s without a live resource uid", target.Component, target.Status)
			}
		}
	}

	// The identity of an execution record is the identity of the code and
	// materials that RAN it, read live — never a copy of the base's. They are
	// separate facts and a reader must be able to tell them apart.
	identity := ManifestIdentity{
		SourceTreeFingerprint: treeFingerprintForRecord(),
		CodeCommit:            commitForRecord(),
		CodeBinaryDigest:      plan.KKBinaryDigest,
		SiteConfigDigest:      plan.NewConfigDigest,
		MaterialsLockDigest:   lockDigest,
		ClusterUID:            liveID.ClusterUID,
		NodeCount:             liveID.NodeCount,
		ReadyNodes:            liveID.ReadyNodes,
	}
	// Bind to the exact bytes of the base record, not merely to a path that
	// could later be pointed at a different file.
	baseDigest, err := sha256File(plan.BaseRunFile)
	if err != nil {
		return RunManifest{}, errors.Wrapf(err, "the base record %s this execution extends cannot be read", plan.BaseRunFile)
	}
	if base.Identity.ClusterUID == "" {
		return RunManifest{}, errors.New("the base record carries no cluster identity to bind this execution to")
	}
	if liveID.ClusterUID != base.Identity.ClusterUID {
		return RunManifest{}, fmt.Errorf("refusing to record an execution observed against cluster %s onto base cluster %s", liveID.ClusterUID, base.Identity.ClusterUID)
	}
	if lockDigest == "" {
		return RunManifest{}, errors.New("the execution saw no materials lock to record")
	}
	// C06: the rule is about the operation, not about the digest moving.
	// An ADDITION changes the effective config, so a digest still equal to the
	// base's means nothing was added and the record would be empty of meaning.
	// A NO-OP on a component the base install itself installed has, by
	// definition, the same effective config — refusing that made the one honest
	// observation of an already-present component unrecordable, and pushed
	// operators to edit an unrelated field just to move the digest.
	if plan.NewConfigDigest == base.ConfigDigest && op == ComponentsOperationAdd {
		return RunManifest{}, fmt.Errorf("the effective config digest equals the base install's (%s) but this pass installed something; an addition changes it — re-plan", base.ConfigDigest)
	}
	// The record's own config digest is the EFFECTIVE config of this execution
	// (a new component changes it); the base's digest stays recorded separately.
	m := RunManifest{
		SchemaVersion:      RunManifestSchemaVersion,
		ConfigDigest:       plan.NewConfigDigest,
		ClusterName:        plan.ClusterName,
		Profile:            base.Profile,
		NetworkStack:       base.NetworkStack,
		RecordKind:         RecordKindComponentsExecution,
		RunID:              plan.RunID,
		Result:             result,
		Identity:           identity,
		Installer:          base.Installer,
		Nodes:              base.Nodes,
		Components:         append(append([]string{}, executed...), observed...),
		StorageClass:       base.StorageClass,
		Storage:            base.Storage,
		ComponentClasses:   base.ComponentClasses,
		PackageRoot:        base.PackageRoot,
		MaterialsValidated: base.MaterialsValidated,
		ComponentsExecution: &ComponentsExecution{
			Operation:        op,
			DidInstall:       op == ComponentsOperationAdd,
			RequestedScope:   scopeNames(targets),
			Executed:         executed,
			ObservedExists:   observed,
			BaseRunID:        base.RunID,
			BaseRecordFile:   plan.BaseRunFile,
			BaseRecordSHA256: baseDigest,
			BaseClusterUID:   base.Identity.ClusterUID,
			BaseConfigDigest: base.ConfigDigest,
			StartedAt:        plan.StartedAt,
			FinishedAt:       plan.FinishedAt,
			Targets:          targets,
			Evidence:         evidence,
		},
	}
	return m, nil
}

func scopeNames(targets []ComponentsExecutionTarget) []string {
	names := make([]string, 0, len(targets))
	for _, target := range targets {
		names = append(names, target.Component)
	}
	return names
}

// WriteComponentsExecutionRecord lands the record atomically and returns its
// path. A half-written record would be read later as a claim about a run, so
// the write goes to a unique temp file, is flushed, renamed and the directory
// is flushed too; any failure is returned rather than papered over.
func WriteComponentsExecutionRecord(dir string, m RunManifest) (string, error) {
	if m.ComponentsExecution == nil {
		return "", errors.New("refusing to write a components execution record with no execution in it")
	}
	if m.RecordKind != RecordKindComponentsExecution {
		return "", fmt.Errorf("refusing to write record kind %q as a components execution record", m.RecordKind)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", errors.Wrapf(err, "create the components record directory %s", dir)
	}
	encoded, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", errors.Wrap(err, "encode the components execution manifest")
	}
	encoded = append(encoded, '\n')
	tmp, err := os.CreateTemp(dir, ".components-run-*.json.tmp")
	if err != nil {
		return "", errors.Wrap(err, "create a temporary components record")
	}
	tmpName := tmp.Name()
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		return "", errors.Wrapf(err, "write the temporary components record %s", tmpName)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", errors.Wrap(err, "flush the temporary components record")
	}
	if err := tmp.Close(); err != nil {
		return "", errors.Wrap(err, "close the temporary components record")
	}
	path := filepath.Join(dir, ComponentsExecutionFileName)
	if err := os.Rename(tmpName, path); err != nil {
		return "", errors.Wrapf(err, "move the components record into %s", path)
	}
	if err := syncDir(dir); err != nil {
		return path, errors.Wrapf(err, "the components record at %s exists but its directory was not flushed; re-check before trusting the listing", path)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return path, errors.Wrapf(err, "components record %s could not be restricted to its owner", path)
	}
	return path, nil
}

func syncDir(dir string) error {
	fh, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer fh.Close()
	return fh.Sync()
}

// isGitCommit accepts the shapes version.Get() actually produces: abbreviated
// or full hex, optionally with a describe suffix.
func isGitCommit(value string) bool {
	core, _, _ := strings.Cut(value, "-")
	if len(core) < 7 || len(core) > 40 {
		return false
	}
	for _, r := range core {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// sha256File is the digest a record links its evidence files by.
func sha256File(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// ValidateComponentsExecutionShape is the pure part of consuming a components
// execution record: schema, subject, operation/result semantics, identity
// completeness and internal consistency. It performs no I/O so a forged or
// half-built record is refused before anything reads the cluster.
func ValidateComponentsExecutionShape(m RunManifest) error {
	if m.SchemaVersion != RunManifestSchemaVersion {
		return fmt.Errorf("components execution record schema is %d, this build reads %d", m.SchemaVersion, RunManifestSchemaVersion)
	}
	if m.RecordKind != RecordKindComponentsExecution {
		return fmt.Errorf("run record is %q; expected %q", m.RecordKind, RecordKindComponentsExecution)
	}
	e := m.ComponentsExecution
	if e == nil {
		return errors.New("record kind is components-execution but no componentsExecution block is present; this is not a record this build wrote")
	}
	if m.RunID == "" || m.ClusterName == "" {
		return errors.New("components execution record has no run id or cluster name")
	}
	// The subject run id names the smoke evidence directory of every verify run
	// made from this record, so it must not be able to become a path.
	if m.RunID != filepath.Base(m.RunID) || strings.ContainsAny(m.RunID, `/\`) {
		return fmt.Errorf("components execution record run id %q is not a single safe path element", m.RunID)
	}
	if !isHex64(m.ConfigDigest) {
		return fmt.Errorf("components execution record configDigest %q is not a 64-hex digest", m.ConfigDigest)
	}
	// The installer's identity is required in full: a commit alone cannot
	// distinguish this build from any other build of the same commit.
	switch {
	case !isHex64(m.Identity.CodeBinaryDigest):
		return fmt.Errorf("components execution record has no well-formed installing binary digest (%q); it cannot be consumed", m.Identity.CodeBinaryDigest)
	case !isHex64(m.Identity.SourceTreeFingerprint) && m.Identity.SourceTreeFingerprint != notEmbeddedIdentity:
		return fmt.Errorf("components execution record has no usable installing source tree fingerprint (%q); a release embeds the tree digest and a dev build says %q",
			m.Identity.SourceTreeFingerprint, notEmbeddedIdentity)
	case !isHex64(m.Identity.MaterialsLockDigest):
		return fmt.Errorf("components execution record has no well-formed materials lock digest (%q)", m.Identity.MaterialsLockDigest)
	case m.Identity.CodeCommit == "":
		return errors.New("components execution record names no code commit")
	case m.Identity.CodeCommit != notEmbeddedIdentity && !isGitCommit(m.Identity.CodeCommit):
		return fmt.Errorf("components execution record code commit %q is neither a git commit nor %q",
			m.Identity.CodeCommit, notEmbeddedIdentity)
	}
	if e.Operation != ComponentsOperationAdd && e.Operation != ComponentsOperationNoop {
		return fmt.Errorf("components execution record operation %q is neither %q nor %q", e.Operation, ComponentsOperationAdd, ComponentsOperationNoop)
	}
	if want := e.Operation == ComponentsOperationAdd; e.DidInstall != want {
		return fmt.Errorf("components execution record says didInstall=%v with operation %q; the two must agree", e.DidInstall, e.Operation)
	}
	switch e.BaseRecordSHA256 {
	case "":
		return errors.New("components execution record does not say which base record it extends")
	default:
		if !isHex64(e.BaseRecordSHA256) {
			return fmt.Errorf("components execution record base record digest %q is not a 64-hex digest", e.BaseRecordSHA256)
		}
	}
	if e.BaseRunID == "" || e.BaseClusterUID == "" || !isHex64(e.BaseConfigDigest) {
		return fmt.Errorf("components execution record base linkage is incomplete (run %q, cluster uid %q, config digest %q)",
			e.BaseRunID, e.BaseClusterUID, e.BaseConfigDigest)
	}
	// C06: only a record that installed something must show a changed config.
	if e.DidInstall && e.BaseConfigDigest == m.ConfigDigest {
		return fmt.Errorf("components execution record installed something yet its effective config digest equals the base's (%s); an addition changes it", m.ConfigDigest)
	}
	if len(e.Targets) == 0 {
		return errors.New("components execution record names no targets")
	}
	seen := map[string]bool{}
	var executed, observed int
	for _, target := range e.Targets {
		if target.Component == "" {
			return errors.New("components execution record has a target with no component name")
		}
		// The component name selects the packaged verify script and the evidence
		// directory of this subject, so it stays a single path element.
		if target.Component != filepath.Base(target.Component) || strings.ContainsAny(target.Component, `/\`) {
			return fmt.Errorf("target component %q is not a single safe path element", target.Component)
		}
		if seen[target.Component] {
			return fmt.Errorf("components execution record names %s twice", target.Component)
		}
		seen[target.Component] = true
		switch target.Status {
		case ComponentsTargetExecuted, ComponentsTargetAlreadyInstalled,
			ComponentsTargetFailed, ComponentsTargetCancelled, ComponentsTargetUnknown:
		default:
			return fmt.Errorf("target %s has status %q that no components record may carry", target.Component, target.Status)
		}
		if target.Namespace == "" || target.Kind == "" || target.Name == "" {
			return fmt.Errorf("target %s carries no resource identity", target.Component)
		}
		if target.Status == ComponentsTargetExecuted || target.Status == ComponentsTargetAlreadyInstalled {
			if target.UID == "" {
				return fmt.Errorf("target %s is reported %s without a resource uid", target.Component, target.Status)
			}
		}
		switch target.Status {
		case ComponentsTargetExecuted:
			executed++
		case ComponentsTargetAlreadyInstalled:
			observed++
		}
	}
	// The scope a record grants is read from these two lists, so they must be
	// exactly the targets the record carries. A name in scope with no matching
	// target would let verify smoke a component this execution never proved
	// anything about, and a target kept out of scope would hide what ran.
	statusOf := make(map[string]string, len(e.Targets))
	for _, target := range e.Targets {
		statusOf[target.Component] = target.Status
	}
	// The writer builds manifest.Components and the two scope lists from one set
	// of run components, so a consumable record must keep them the same set.
	recorded := make(map[string]bool, len(m.Components))
	for _, name := range m.Components {
		recorded[name] = true
	}
	for _, list := range []struct {
		kind, label string
		names       []string
	}{
		{ComponentsTargetExecuted, "executed", e.Executed},
		{ComponentsTargetAlreadyInstalled, "observedExisting", e.ObservedExists},
	} {
		named := map[string]bool{}
		for _, name := range list.names {
			// These names also select the packaged script and the evidence
			// directory of the subject, so a scope entry is held to the same
			// rule as a target name.
			if name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
				return fmt.Errorf("%s names %q, which is not a single safe path element", list.label, name)
			}
			if !recorded[name] {
				return fmt.Errorf("%s names %s, which this record does not carry as a component of the run", list.label, name)
			}
			if named[name] {
				return fmt.Errorf("the record names %s in %s twice", name, list.label)
			}
			named[name] = true
			if statusOf[name] != list.kind {
				return fmt.Errorf("the record grants scope to %s as %s, but no target of that name reports %s",
					name, list.label, list.kind)
			}
		}
		for name, status := range statusOf {
			if status == list.kind && !named[name] {
				return fmt.Errorf("target %s reports %s but the record leaves it out of %s", name, list.kind, list.label)
			}
		}
	}
	if e.Operation == ComponentsOperationAdd && m.Result == ResultSucceeded && executed == 0 {
		return errors.New("a succeeded add record executed nothing")
	}
	if e.Operation == ComponentsOperationNoop {
		if executed > 0 {
			return errors.New("a noop record executed something")
		}
		if m.Result == ResultSucceeded && observed == 0 {
			return errors.New("a succeeded noop record observed nothing")
		}
	}
	if m.Result == ResultSucceeded {
		for _, target := range e.Targets {
			if target.Status != ComponentsTargetExecuted && target.Status != ComponentsTargetAlreadyInstalled {
				return fmt.Errorf("a succeeded record carries target %s in state %q", target.Component, target.Status)
			}
		}
	}
	if len(e.RequestedScope) == 0 {
		return errors.New("components execution record does not name what was requested")
	}
	for _, ref := range e.Evidence {
		if ref.Path == "" || !isHex64(ref.SHA256) {
			return fmt.Errorf("evidence entry %q is not a path plus a 64-hex digest", ref.Path)
		}
	}
	// Evidence is what makes the digest re-checks mean something: a record that
	// lists none would re-hash nothing and still grant scope. A pass always
	// produces its plan and its execute report, so both are required.
	if m.Result == ResultSucceeded {
		present := map[string]bool{}
		for _, ref := range e.Evidence {
			present[ref.Kind] = true
		}
		for _, kind := range []string{ComponentsEvidencePlan, ComponentsEvidenceExecuteReport} {
			if !present[kind] {
				return fmt.Errorf("a succeeded execution record carries no %s evidence; it would re-hash nothing and still grant verification scope", kind)
			}
		}
	}
	return nil
}

// ValidateComponentsExecutionBase checks the record against the base install it
// claims to extend, and against the cluster that is actually standing there.
// It returns the base record so the caller can reuse its facts.
func ValidateComponentsExecutionBase(ctx context.Context, m RunManifest, runner kubectlRunner) (RunManifest, error) {
	e := m.ComponentsExecution
	if e == nil {
		return RunManifest{}, errors.New("no components execution to validate")
	}
	basePath := e.BaseRecordFile
	if basePath == "" {
		return RunManifest{}, errors.New("components execution record does not name its base record file")
	}
	digest, err := sha256File(basePath)
	if err != nil {
		return RunManifest{}, errors.Wrapf(err, "the base record %s this execution claims cannot be read", basePath)
	}
	if digest != e.BaseRecordSHA256 {
		return RunManifest{}, fmt.Errorf("the base record %s hashes to %s but the execution record was built against %s; the base changed or this is not that record",
			basePath, digest, e.BaseRecordSHA256)
	}
	raw, err := os.ReadFile(basePath)
	if err != nil {
		return RunManifest{}, errors.Wrapf(err, "read the base record %s", basePath)
	}
	var base RunManifest
	if err := json.Unmarshal(raw, &base); err != nil {
		return RunManifest{}, errors.Wrapf(err, "parse the base record %s", basePath)
	}
	statePath := filepath.Join(filepath.Dir(basePath), "run-state.json")
	var state *InstallState
	if _, statErr := os.Stat(statePath); statErr == nil {
		st, err := ReadRunState(statePath)
		if err != nil {
			return RunManifest{}, errors.Wrapf(err, "read the base install state %s", statePath)
		}
		state = st
	}
	if err := ValidateSuccessRecord(base, state); err != nil {
		return RunManifest{}, errors.Wrap(err, "the execution record's base is not a valid install-success record")
	}
	if base.RunID != e.BaseRunID {
		return RunManifest{}, fmt.Errorf("the base record is run %q but the execution record claims %q", base.RunID, e.BaseRunID)
	}
	if base.ClusterName != m.ClusterName {
		return RunManifest{}, fmt.Errorf("the execution record belongs to cluster %q, its base to %q", m.ClusterName, base.ClusterName)
	}
	if base.ConfigDigest != e.BaseConfigDigest {
		return RunManifest{}, fmt.Errorf("the base record's config digest is %s, the execution record says %s", base.ConfigDigest, e.BaseConfigDigest)
	}
	if base.Identity.ClusterUID != e.BaseClusterUID {
		return RunManifest{}, fmt.Errorf("the base record's cluster uid is %s, the execution record says %s", base.Identity.ClusterUID, e.BaseClusterUID)
	}
	// Every evidence pointer must still describe the bytes it claimed.
	for _, ref := range m.ComponentsExecution.Evidence {
		actual, err := sha256File(ref.Path)
		if err != nil {
			return RunManifest{}, errors.Wrapf(err, "the execution record's %s evidence %s cannot be read", ref.Kind, ref.Path)
		}
		if actual != ref.SHA256 {
			return RunManifest{}, fmt.Errorf("the execution record's %s evidence %s hashes to %s, not %s", ref.Kind, ref.Path, actual, ref.SHA256)
		}
	}
	liveID, _, err := captureLiveCluster(ctx, runner)
	if err != nil {
		return RunManifest{}, errors.Wrap(err, "the live cluster identity needed to consume this execution record could not be read")
	}
	if liveID.ClusterUID != e.BaseClusterUID {
		return RunManifest{}, fmt.Errorf("the live cluster is %s but this execution record belongs to %s; the same config on a different cluster is not the same base",
			liveID.ClusterUID, e.BaseClusterUID)
	}
	return base, nil
}

// componentsExecutionScope is the only set --only may draw from for an
// execution record: what this execution really installed, plus (for a read-only
// observation) what it verified as ANI-owned and left untouched. It is never
// the enabled set of some config file.
func componentsExecutionScope(m RunManifest) (smoke []string, allowsMutation bool, err error) {
	e := m.ComponentsExecution
	if e == nil {
		return nil, false, errors.New("no components execution to scope from")
	}
	switch e.Operation {
	case ComponentsOperationAdd:
		if len(e.Executed) == 0 {
			return nil, false, errors.New("the add record names nothing it executed")
		}
		return append([]string{}, e.Executed...), true, nil
	case ComponentsOperationNoop:
		scope := append([]string{}, e.ObservedExists...)
		if len(scope) == 0 {
			return nil, false, errors.New("the observation record names nothing it observed")
		}
		return scope, false, nil
	default:
		return nil, false, fmt.Errorf("operation %q grants no verification scope", e.Operation)
	}
}

// componentsRecordTargets reads back, for every component of one execution,
// the live resource that execution owns. A component whose resource is absent
// is recorded in the failed state rather than dropped, because a record that
// quietly omits a target would overstate what happened.
func componentsRecordTargets(ctx context.Context, runner kubectlRunner, plan ComponentsPlan, statuses map[string]string) []ComponentsExecutionTarget {
	targets := make([]ComponentsExecutionTarget, 0, len(plan.Components))
	for _, planned := range plan.Components {
		spec := componentInstallSpecs[planned.Component]
		status, ok := statuses[planned.Component]
		if !ok {
			status = ComponentsTargetUnknown
		}
		target := ComponentsExecutionTarget{
			Component: planned.Component,
			Status:    status,
			Namespace: planned.Namespace,
			Kind:      spec.WorkloadKind,
			Name:      spec.WorkloadName,
			Images:    planned.Images,
			Charts:    planned.Charts,
			Detail:    planned.Owner,
		}
		if target.Namespace == "" {
			target.Namespace = spec.Namespace
		}
		if target.Kind == "" {
			target.Kind = spec.WorkloadKind
		}
		if target.Name == "" {
			target.Name = spec.WorkloadName
		}
		if target.Kind != "" && target.Name != "" && target.Namespace != "" {
			if uid, err := runner.jsonpath(ctx, strings.ToLower(target.Kind), target.Name, target.Namespace, "{.metadata.uid}"); err == nil {
				target.UID = strings.TrimSpace(uid)
			}
		}
		if (status == ComponentsTargetExecuted || status == ComponentsTargetAlreadyInstalled) && target.UID == "" {
			// No identity means no ownership claim: say so instead of
			// recording a success against an unverified object.
			target.Status = ComponentsTargetUnknown
			target.Detail = fmt.Sprintf("the %s %s/%s carries no readable uid; ownership could not be established", target.Kind, target.Namespace, target.Name)
		}
		targets = append(targets, target)
	}
	return targets
}

// componentsRecordDir is the canonical home of a consumable execution record:
// keyed by cluster and execution run, never by whatever --output the operator
// happened to pass.
func componentsRecordDir(clusterName, runID string) (string, error) {
	if clusterName == "" || clusterName == "." || clusterName == ".." ||
		strings.ContainsAny(clusterName, `/\`) {
		return "", fmt.Errorf("cluster name %q cannot form a safe record path", clusterName)
	}
	if runID == "" || runID == "." || runID == ".." || strings.ContainsAny(runID, `/\`) {
		return "", fmt.Errorf("run id %q cannot form a safe record path", runID)
	}
	return filepath.Join(runtimeBaseDir, clusterName, "components", runID), nil
}

// findPriorExecutionRecord looks for the consumable record that really
// installed this component, so a later read-only observation can point at
// verifiable evidence instead of asserting history. Absence is reported as
// itself: an installation from before this record type existed stays unproven.
func findPriorExecutionRecord(clusterName, component string) (*ComponentsEvidenceRef, string) {
	dir := filepath.Join(runtimeBaseDir, clusterName, "components")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, "no earlier components execution record exists for this cluster"
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name(), ComponentsExecutionFileName)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m RunManifest
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		if m.RecordKind != RecordKindComponentsExecution || m.ComponentsExecution == nil ||
			!m.ComponentsExecution.DidInstall || m.Result != ResultSucceeded {
			continue
		}
		for _, target := range m.ComponentsExecution.Targets {
			if target.Component != component || target.Status != ComponentsTargetExecuted {
				continue
			}
			digest, err := sha256File(path)
			if err != nil {
				return nil, fmt.Sprintf("the earlier record %s cannot be read back", path)
			}
			return &ComponentsEvidenceRef{
				Kind:   ComponentsEvidencePriorExecution,
				Path:   path,
				SHA256: digest,
				RunID:  m.RunID,
			}, ""
		}
	}
	return nil, fmt.Sprintf("no earlier components execution record installed %s; this observation cannot claim when it was installed", component)
}

// recordComponentsExecution lands the consumable record of one execution and
// returns its path. Failing to land it is a failure of the operation as far as
// verifiability goes, so it is reported loudly — but it never re-runs the
// playbook to manufacture a nicer outcome.
func recordComponentsExecution(ctx context.Context, runner kubectlRunner, base RunManifest, plan ComponentsPlan,
	liveID ManifestIdentity, lockDigest, op, result string, statuses map[string]string, evidence []ComponentsEvidenceRef) (string, error) {

	targets := componentsRecordTargets(ctx, runner, plan, statuses)
	if priorNeeded := op == ComponentsOperationNoop; priorNeeded {
		for index, target := range targets {
			ref, reason := findPriorExecutionRecord(plan.ClusterName, target.Component)
			if ref != nil {
				evidence = append(evidence, *ref)
				continue
			}
			if target.Status == ComponentsTargetAlreadyInstalled {
				targets[index].Detail = fmt.Sprintf("%s; %s", target.Detail, reason)
			}
		}
	}
	m, err := NewComponentsExecutionManifest(base, plan, liveID, lockDigest, op, result, targets, evidence)
	if err != nil {
		return "", err
	}
	dir, err := componentsRecordDir(plan.ClusterName, plan.RunID)
	if err != nil {
		return "", err
	}
	return WriteComponentsExecutionRecord(dir, m)
}

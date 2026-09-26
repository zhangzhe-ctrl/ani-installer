package ani

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Guards added when the L-06 closeout was independently re-audited rather than
// read back from its own ledger. Each one pins a rule that production code had
// announced but did not enforce at the place where an operator would learn it.

// The execution record binds a component's ownership to a live workload read by
// name. That name comes from componentInstallSpecs while the object itself is
// created by the component's role, and the two drifted for metrics: the spec
// said StatefulSet "ani-metrics-prometheus" while the role waits
// "statefulset/prometheus-ani-metrics-prometheus". A read of a name that never
// exists yields no uid, the target silently degrades to "unknown", and the
// record this pass lands is one its own consumer refuses — after the cluster was
// changed. So every spec must name a workload its role really rolls out.
func TestComponentInstallSpecsNameTheWorkloadTheirRolesCreate(t *testing.T) {
	workloadToken := regexp.MustCompile(`(statefulset|deployment|daemonset)/[a-z0-9-]+`)
	for name, spec := range componentInstallSpecs {
		role := filepath.Join("..", "..", "builtin", "core", "roles", "ani", name, "tasks", "main.yaml")
		data, err := os.ReadFile(role)
		if err != nil {
			t.Fatalf("%s: read %s: %v", name, role, err)
		}
		want := strings.ToLower(spec.WorkloadKind) + "/" + spec.WorkloadName
		found := workloadToken.FindAllString(string(data), -1)
		present := false
		for _, token := range found {
			if token == want {
				present = true
				break
			}
		}
		if !present {
			t.Fatalf("%s: the installer binds this component's ownership to %q, but %s never names that workload (it waits on %s); "+
				"the record-time uid read of a workload that does not exist would degrade this component to an unusable record",
				name, want, role, strings.Join(found, ", "))
		}
	}
}

// The record kind was the only thing separating the two consumers, so an
// execution record that re-labelled itself "install-success" skipped every
// base-bytes, evidence and live-cluster check on the verify side. The same
// forgery is dangerous on the WRITE side: --base-run is validated by
// ValidateSuccessRecord alone, so a relabelled execution record would anchor a
// later addition onto a subject run instead of onto a first install.
func TestComponentsExecutionRecordCannotServeAsBaseRun(t *testing.T) {
	tc := runR15Execution(t, "absent")
	if tc.recordPath == "" {
		t.Fatalf("no landed record to re-label; stdout:\n%s", tc.stdout)
	}
	data, err := os.ReadFile(tc.recordPath)
	if err != nil {
		t.Fatalf("read the landed record: %v", err)
	}
	var m RunManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse the landed record: %v", err)
	}
	if m.ComponentsExecution == nil {
		t.Fatal("the landed record carries no execution block to give it away")
	}
	m.RecordKind = RecordKindInstallSuccess
	forge := filepath.Join(t.TempDir(), "run.json")
	encoded, err := json.MarshalIndent(&m, "", "  ")
	if err != nil {
		t.Fatalf("encode the forged base: %v", err)
	}
	if err := os.WriteFile(forge, append(encoded, '\n'), 0o600); err != nil {
		t.Fatalf("write the forged base: %v", err)
	}

	_, err = loadBaseRunRecord(forge, "")
	if err == nil {
		t.Fatal("a re-labelled execution record was accepted as the base install of a new addition")
	}
	if !strings.Contains(err.Error(), "componentsExecution") {
		t.Fatalf("the refusal must name the block that gives the record away, got %v", err)
	}

	// The genuine base install record must still load exactly as before.
	base, err := loadBaseRunRecord(tc.baseRun, "")
	if err != nil {
		t.Fatalf("the real install-success record must still serve as a base: %v", err)
	}
	if base.ComponentsExecution != nil || base.RecordKind != RecordKindInstallSuccess {
		t.Fatalf("the install record was mis-read: kind=%q block=%v", base.RecordKind, base.ComponentsExecution != nil)
	}
}

// The consumer refuses an execution record whose effective config digest is the
// base's, because an addition changes it. The writer must not land a record it
// knows will always be refused: that would print a consumable-looking path, exit
// successfully, and leave the operator with a record that can never verify.
func TestComponentsExecutionWriterRefusesARecordVerifyWouldReject(t *testing.T) {
	base := RunManifest{
		SchemaVersion: RunManifestSchemaVersion,
		RecordKind:    RecordKindInstallSuccess,
		RunID:         "ani-ani-lab-20260924-120000",
		Result:        ResultSucceeded,
		ClusterName:   "ani-lab",
		ConfigDigest:  strings.Repeat("a", 64),
		Identity: ManifestIdentity{
			SiteConfigDigest:    strings.Repeat("a", 64),
			MaterialsLockDigest: strings.Repeat("b", 64),
			ClusterUID:          "uid-kube-cluster-a",
		},
	}
	dir := t.TempDir()
	// The writer hashes the base record's own bytes, and every record names the
	// evidence it was built from, so the fixture must supply real files.
	baseRunFile := filepath.Join(dir, "run.json")
	if err := os.WriteFile(baseRunFile, []byte(`{"schemaVersion":1}`), 0o600); err != nil {
		t.Fatalf("write the base record: %v", err)
	}
	planFile := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(planFile, []byte(`{"recordKind":"components-plan"}`), 0o600); err != nil {
		t.Fatalf("write the plan evidence: %v", err)
	}
	reportFile := filepath.Join(dir, "report.json")
	if err := os.WriteFile(reportFile, []byte(`{"overall":"pass"}`), 0o600); err != nil {
		t.Fatalf("write the execute report evidence: %v", err)
	}
	evidence := []ComponentsEvidenceRef{
		{Kind: ComponentsEvidencePlan, Path: planFile, SHA256: sha256FileHex(planFile)},
		{Kind: ComponentsEvidenceExecuteReport, Path: reportFile, SHA256: sha256FileHex(reportFile)},
	}

	plan := ComponentsPlan{
		RunID:            "ani-components-ani-lab-20260926-120000",
		ClusterName:      "ani-lab",
		NewConfigDigest:  base.ConfigDigest,
		BaseConfigDigest: base.ConfigDigest,
		BaseRunFile:      baseRunFile,
		KKBinaryDigest:   strings.Repeat("c", 64),
	}
	targets := []ComponentsExecutionTarget{{
		Component: "valkey", Status: ComponentsTargetAlreadyInstalled,
		Namespace: "ani-platform", Kind: "StatefulSet", Name: "valkey", UID: "uid-sts-valkey",
	}}

	// C06 corrects what this test used to assert. A component the FIRST INSTALL
	// itself put in place leaves the effective config exactly as the base recorded
	// it, so a read-only no-op over it legitimately carries the same digest.
	// Refusing that made the honest observation unrecordable and pushed an
	// operator to edit an unrelated field only to move the digest — which is a
	// fabricated difference, not evidence of a change.
	landed, err := NewComponentsExecutionManifest(base, plan, ManifestIdentity{ClusterUID: "uid-kube-cluster-a"},
		strings.Repeat("b", 64), ComponentsOperationNoop, ResultSucceeded, targets, evidence)
	if err != nil {
		t.Fatalf("a same-config no-op must be recordable: %v", err)
	}
	if landed.ComponentsExecution.Operation != ComponentsOperationNoop || landed.ComponentsExecution.DidInstall {
		t.Fatalf("the observation record lost its read-only meaning: %+v", landed.ComponentsExecution)
	}
	if landed.ComponentsExecution.BaseConfigDigest != landed.ConfigDigest {
		t.Fatalf("the record no longer says the config did not move: %s vs %s",
			landed.ComponentsExecution.BaseConfigDigest, landed.ConfigDigest)
	}
	if err := ValidateComponentsExecutionShape(landed); err != nil {
		t.Fatalf("the record the writer accepts must be shaped for the consumer: %v", err)
	}
	// A same-config no-op still installs nothing: it grants observation scope,
	// never change authority, and it does not re-arm anything.
	if _, allowsMutation, err := componentsExecutionScope(landed); err != nil || allowsMutation {
		t.Fatalf("a same-config no-op must not allow mutation (err %v, allowsMutation %v)", err, allowsMutation)
	}

	// The digest rule still binds an ADDITION: claiming a component was installed
	// while the effective config is unchanged is a contradiction, and that is the
	// case the guard exists for.
	addTargets := []ComponentsExecutionTarget{{
		Component: "valkey", Status: ComponentsTargetExecuted,
		Namespace: "ani-platform", Kind: "StatefulSet", Name: "valkey", UID: "uid-sts-valkey",
	}}
	if _, err := NewComponentsExecutionManifest(base, plan, ManifestIdentity{ClusterUID: "uid-kube-cluster-a"},
		strings.Repeat("b", 64), ComponentsOperationAdd, ResultSucceeded, addTargets, evidence); err == nil ||
		!strings.Contains(err.Error(), "equals the base install") {
		t.Fatalf("an add with an unchanged effective config must still be refused, got %v", err)
	}
	// And with a real change the same add succeeds, so the refusal is about the
	// digest and not about the shape.
	changed := plan
	changed.NewConfigDigest = strings.Repeat("d", 64)
	if _, err := NewComponentsExecutionManifest(base, changed, ManifestIdentity{ClusterUID: "uid-kube-cluster-a"},
		strings.Repeat("b", 64), ComponentsOperationAdd, ResultSucceeded, addTargets, evidence); err != nil {
		t.Fatalf("an add that really changed the config must be recordable: %v", err)
	}
}

// A read-only pass must not be able to run under a binary its plan was never
// bound to: its record attests that binary as the identity that observed the
// cluster. The add path checked this and returned before the noop branch, so a
// no-op wrote a record claiming code it had never verified.
func TestComponentsReadOnlyPassStillHonoursTheBinaryBinding(t *testing.T) {
	tc := runR15Execution(t, "ours") // everything already installed: the noop pass
	if tc.recordPath == "" {
		t.Fatalf("no record from the read-only pass; stdout:\n%s", tc.stdout)
	}
	if tc.record.ComponentsExecution.Operation != ComponentsOperationNoop {
		t.Fatalf("this fixture must produce the read-only pass, got %q", tc.record.ComponentsExecution.Operation)
	}

	// The same plan executed by a different child binary is refused before any
	// record of that pass exists.
	other := filepath.Join(t.TempDir(), "kk")
	data, err := os.ReadFile(tc.execute.KKBin)
	if err != nil {
		t.Fatalf("read the planned kk: %v", err)
	}
	if err := os.WriteFile(other, append(append([]byte(nil), data...), '\n'), 0o700); err != nil {
		t.Fatalf("write the unbound kk: %v", err)
	}
	execute := tc.execute
	execute.KKBin = other
	execute.Output = filepath.Join(t.TempDir(), "unbound")
	err = RunComponentsExecute(context.Background(), execute, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "the plan is bound to kk digest") {
		t.Fatalf("a read-only pass driven by an unbound binary must be refused, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(execute.Output, "components-"+tc.record.RunID)); !os.IsNotExist(statErr) {
		t.Fatalf("a refused pass may not leave a run directory behind: %v", statErr)
	}
	if landed := sha256FileHex(tc.recordPath); landed == "" {
		t.Fatalf("the record of the bound pass disappeared: %v", landed)
	}
}

package ani

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Revalidation preserves the original successful acceptance and grants one
// new check per target to this exact verifier binary, independently of output.
// Failed/unknown original attempts and components noop records cannot authorize it.
type acceptanceRevalidation struct {
	originalDir string
	verifier    string
	clusterUID  string
	parents     map[string]acceptanceRevalidationParent
}

type acceptanceRevalidationParent struct {
	ledger acceptanceLedger
	digest string
}

func prepareAcceptanceRevalidation(ctx context.Context, input VerifyInput, runner kubectlRunner, manifest RunManifest, runID, stateDir string, scope []string) (*acceptanceRevalidation, error) {
	if manifest.RecordKind != RecordKindInstallSuccess || manifest.ComponentsExecution != nil {
		return nil, fmt.Errorf("preservation revalidation requires the original install-success record; a components record cannot grant a new mutation budget")
	}
	verifier := selfBinaryDigest()
	if !isHex64(verifier) || !isHex64(manifest.Identity.CodeBinaryDigest) || verifier == manifest.Identity.CodeBinaryDigest {
		return nil, fmt.Errorf("preservation revalidation requires a fixed verifier binary distinct from the recorded original installer")
	}
	if len(input.Only) == 0 {
		return nil, fmt.Errorf("preservation revalidation requires an explicit --only scope")
	}
	r := &acceptanceRevalidation{originalDir: stateDir, verifier: verifier,
		clusterUID: manifest.Identity.ClusterUID, parents: map[string]acceptanceRevalidationParent{}}
	for _, component := range scope {
		for _, step := range acceptancePlans[component].Steps {
			if step.Target == "" {
				continue
			}
			target, ok := acceptanceTargets[step.Target]
			if !ok || target.PVCName == "" {
				return nil, fmt.Errorf("preservation revalidation requires a declared volume-backed target: %s", step.Target)
			}
			parent, err := readSuccessfulAcceptanceParent(stateDir, runID, target)
			if err != nil {
				return nil, err
			}
			livePVC, err := runner.jsonpath(ctx, "pvc", target.PVCName, target.Namespace, "{.metadata.uid}")
			if err != nil || livePVC != parent.ledger.OldPVCUID {
				return nil, fmt.Errorf("preservation revalidation target %s no longer uses the original accepted PVC", step.Target)
			}
			r.parents[step.Target] = parent
		}
	}
	return r, nil
}

func readSuccessfulAcceptanceParent(stateDir, runID string, target acceptanceTarget) (acceptanceRevalidationParent, error) {
	info, err := os.Lstat(target.ledgerFile(stateDir, runID))
	if err != nil || !info.Mode().IsRegular() {
		return acceptanceRevalidationParent{}, fmt.Errorf("preservation revalidation requires a regular original acceptance ledger for %s", target.LedgerToken)
	}
	data, err := os.ReadFile(target.ledgerFile(stateDir, runID))
	if err != nil {
		return acceptanceRevalidationParent{}, err
	}
	var ledger acceptanceLedger
	if err := json.Unmarshal(data, &ledger); err != nil || ledger.State != ledgerStateDone || ledger.Outcome != VerifyStatusPass ||
		ledger.RunID != runID || ledger.Target != target.LedgerToken || ledger.Controller != target.ControllerKind+"/"+target.ControllerName ||
		ledger.OldPVCUID == "" || ledger.OldPodUID == "" || ledger.NewPodUID == "" || ledger.NewPodUID == ledger.OldPodUID || ledger.FinishedAt == "" {
		return acceptanceRevalidationParent{}, fmt.Errorf("preservation revalidation requires the prior successful, identity-bound acceptance of %s; failed or unknown attempts cannot be retried", target.LedgerToken)
	}
	sum := sha256.Sum256(data)
	return acceptanceRevalidationParent{ledger: ledger, digest: hex.EncodeToString(sum[:])}, nil
}

func (r *acceptanceRevalidation) stateDir() string {
	return filepath.Join(r.originalDir, "revalidation", r.verifier)
}

func (r *acceptanceRevalidation) bindTarget(runID string, target acceptanceTarget, stableUID string) (string, error) {
	parent, err := readSuccessfulAcceptanceParent(r.originalDir, runID, target)
	want, ok := r.parents[target.LedgerToken]
	if err != nil || !ok || parent.digest != want.digest || stableUID != parent.ledger.OldPVCUID {
		return "", fmt.Errorf("original acceptance or PVC identity changed before revalidating %s; refusing any change", target.LedgerToken)
	}
	return parent.digest, nil
}

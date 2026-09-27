package ani

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/cockroachdb/errors"
)

// ---------------------------------------------------------------------------
// The one way a packaged checker removes a probe it created.
//
// C08: the Fluent Bit checker creates a client pod, per-node marker pods and one
// inspector pod per node, all named after the run. Until now its cleanup could
// only ever be "delete the object standing at this name, having just read its uid
// and compared it". That read-then-delete is not a condition: between the read and
// the delete the object can be replaced, and the second request would then remove
// whoever stands there now. A label-scoped batch delete has the same class of
// problem in reverse — it authorises whatever carries the label, which is a set
// the run never created a claim over.
//
// The capability to do it properly already exists in this package: pod_delete.go
// puts the uid inside DeleteOptions.Preconditions, so the server performs the
// check and the deletion as one operation. This file exposes that as the
// narrowest possible internal call for a checker: one pod, one uid it holds from
// the create response, one answer.
//
// It is deliberately not a general pod-deletion API: there is no listing, no
// namespace discovery, no label selection and no retry, and it never deletes
// anything it was not handed the uid of.
// ---------------------------------------------------------------------------

// PodReleaseInput names the single object a checker is standing behind.
type PodReleaseInput struct {
	Kubeconfig string
	Namespace  string
	Pod        string
	UID        string
}

// The answer is the outcome token of the delete itself, reusing pod_delete.go's
// vocabulary rather than minting a second one that can drift from it. A caller
// branches on exactly two of them:
//
//	deleted   the object this run created was removed under its own uid
//	not_found there was nothing to remove — which is NOT "this run removed it"
// anything else  the object stays, the ownership record stays, the run reports it
const (
	PodReleaseReleased = string(deleteDeleted)
	PodReleaseAbsent   = string(deleteNotFound)
)

// selfExecutable is this process's binary, which is what a checker is told to call
// back into. A run that cannot name itself says so by handing over an empty value,
// and the checker then reports its cleanup as incomplete rather than inventing a
// delete path.
func selfExecutable() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(path)
}

// RunPodRelease deletes one pod conditionally on the uid the caller holds.
//
// The outcome is written to out as one token line, and an error is returned when
// the object was not removed and was not found: the caller must keep its
// ownership record and fail, because a probe left behind is a fact about the run,
// not a detail to swallow.
func RunPodRelease(ctx context.Context, input PodReleaseInput, out io.Writer) error {
	for _, required := range []struct{ field, value string }{
		{"--kubeconfig", input.Kubeconfig}, {"--namespace", input.Namespace},
		{"--pod", input.Pod}, {"--uid", input.UID},
	} {
		if strings.TrimSpace(required.value) == "" {
			return fmt.Errorf("pod-release needs %s and a uid to condition on; refusing to delete a pod by name alone", required.field)
		}
	}
	deleter, err := newPodDeleter(input.Kubeconfig)
	if err != nil {
		return err
	}
	outcome, deleteErr := deleter.deletePodWithUIDPrecondition(ctx, input.Namespace, input.Pod, input.UID)
	if out != nil {
		fmt.Fprintf(out, "%s %s %s %s\n", outcome, input.Namespace, input.Pod, input.UID)
	}
	switch outcome {
	case deleteDeleted:
		return nil
	case deleteNotFound:
		// Absent is not "deleted by this run". It ends the obligation because
		// there is nothing left to remove, and the token says which of the two
		// happened; a caller must not print the one for the other.
		return nil
	default:
		// conflict / forbidden / timeout / failed: the object stays, the caller
		// keeps ownership, and the error names what the server said. No retry
		// here, and no re-read of a fresh uid to try again with — that would be
		// deleting whatever stands at the name now under the old object's
		// authority.
		if deleteErr == nil {
			deleteErr = fmt.Errorf("the conditional delete returned %s with no explanation", outcome)
		}
		return errors.Wrapf(deleteErr, "pod %s/%s was not released", input.Namespace, input.Pod)
	}
}

package ani

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8scorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ---------------------------------------------------------------------------
// The one place a Pod is deleted with the identity that authorised it.
//
// C03: the code this replaces shelled out
//
//	kubectl delete pod <name> -n <ns> --field-selector metadata.uid=<uid> --ignore-not-found=false
//
// and called that a UID precondition. It is not one, on two independently
// checkable grounds.
//
// A selector selects the SET a request acts on; it is not a condition on the
// object being deleted. No uid travels in a DeleteOptions, so whichever object the
// selection resolved to would be deleted unconditionally — which is the opposite of
// what an authorisation check must do.
//
// And metadata.uid is not a field a Pod offers for selection at all. In
// kubernetes' pkg/registry/core/pod/strategy.go, ToSelectableFields builds
// spec.nodeName, spec.restartPolicy, spec.schedulerName, spec.serviceAccountName,
// spec.hostNetwork, status.phase, status.podIP and status.nominatedNodeName, then
// hands off to generic.AddObjectMetaFieldsSet for the metadata fields. What the
// delete would have been rejected for, or silently matched nothing, depends on the
// server's field-selector validation.
//
// Two things could NOT be established in this environment and are recorded rather
// than assumed: the kubectl source is not in this module's dependency graph and the
// cached kubernetes tree is pruned before pkg/kubectl/cmd, so (a) whether that
// client rejects a name given together with a selector, and (b) whether a
// CEL-based field selector rescues metadata.uid on a v1.35 API server, are both
// unverified here. Neither changes the conclusion — even a working selector is not
// a precondition — but neither is claimed either.
//
// What this file does instead is the documented mechanism. apimachinery's
// DeleteOptions says of Preconditions: "Must be fulfilled before a deletion is
// carried out. If not possible, a 409 Conflict status will be returned." The uid
// travels inside the request body, so the check and the deletion are one
// server-side operation, and a same-name replacement that appears after the last
// client-side read cannot be removed by this run.
// ---------------------------------------------------------------------------

// podDeleteTimeout bounds one conditional delete request. It is a request bound,
// not a wait-for-gone: the caller keeps its own bounded wait for the recreated Pod,
// and a delete that does not answer is recorded as unknown and never re-issued.
const podDeleteTimeout = 30 * time.Second

// deleteOutcome is what a conditional delete actually returned. Each branch is
// distinguished because "the server refused", "it is already gone" and "we do not
// know" lead to different operator actions — and only one of them may read as a
// deletion that happened.
type deleteOutcome string

const (
	deleteDeleted   deleteOutcome = "deleted"
	deleteConflict  deleteOutcome = "conflict"
	deleteNotFound  deleteOutcome = "not_found"
	deleteForbidden deleteOutcome = "forbidden"
	deleteTimeout   deleteOutcome = "timeout"
	deleteFailed    deleteOutcome = "failed"
)

// podDeleteClient is client-go's generated delete method for one namespace's pods,
// spelled exactly as it is generated so a test observes the real request rather
// than a local imitation of it. The seam is transport only: the verb, the path and
// the body are chosen by the code under test.
type podDeleteClient interface {
	Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error
}

// podDeleter deletes one named Pod conditional on its uid, against the cluster one
// specific kubeconfig file names. It resolves the namespace at call time through a
// func rather than an interface, because Go interfaces are not covariant: the
// generated Pods() returns the full PodInterface, and declaring that as the seam
// would force every test to implement methods this package never calls.
type podDeleter struct {
	podsFor func(namespace string) podDeleteClient
}

// newPodDeleter loads exactly one kubeconfig file — the one this verification was
// given — and refuses rather than choosing a cluster by itself.
//
// Two fallbacks are deliberately avoided, both of which would be a second target
// wearing the first one's authorisation. client-go's DeferredLoadingClientConfig
// falls through to the in-cluster service-account config when the merged file
// equals its defaults, so the config is loaded and then passed to
// NewNonInteractiveClientConfig, which has no such branch. And a loading rule with
// an ExplicitPath never consults Precedence, which is where $KUBECONFIG and
// $HOME/.kube/config would otherwise enter — a missing explicit file is an error.
func newPodDeleter(kubeconfig string) (*podDeleter, error) {
	path := strings.TrimSpace(kubeconfig)
	if path == "" {
		return nil, errors.New("a kubeconfig path is required to delete a pod conditionally; refusing to choose a cluster")
	}
	loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: path}
	merged, err := loadingRules.Load()
	if err != nil {
		return nil, errors.Wrapf(err, "read the kubeconfig %s this verification was given", path)
	}
	// An empty context name means "whatever current-context the file itself
	// declares" — the same resolution client-go performs, with no environment input.
	clientConfig := clientcmd.NewNonInteractiveClientConfig(*merged, "", &clientcmd.ConfigOverrides{}, loadingRules)
	raw, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, errors.Wrapf(err, "resolve an API server from the kubeconfig %s", path)
	}
	// A file that exists but names no server would otherwise send to an empty host.
	if strings.TrimSpace(raw.Host) == "" {
		return nil, fmt.Errorf("the kubeconfig %s resolves to no API server", path)
	}
	config := rest.CopyConfig(raw)
	config.Timeout = podDeleteTimeout
	// Pin the body's media type. Left alone, a kubeconfig-derived client negotiates
	// application/vnd.kubernetes.protobuf — observed directly while writing the
	// tests for this file, where the delete body arrived as protobuf bytes that did
	// carry the uid but could not be decoded by anything that speaks JSON. The
	// precondition is the authorisation of this whole operation, so the format it
	// travels in is pinned rather than negotiated, and JSON is the one this package
	// can also prove on the wire in a test.
	config.ContentType = "application/json"
	client, err := k8scorev1.NewForConfig(config)
	if err != nil {
		return nil, errors.Wrapf(err, "build a client for the kubeconfig %s", path)
	}
	// The generated Pods() returns the full PodInterface; wrapping it is how the
	// narrow seam above stays honest about what this package actually calls.
	return &podDeleter{podsFor: func(namespace string) podDeleteClient { return client.Pods(namespace) }}, nil
}

// deletePodWithUIDPrecondition deletes one Pod by name with the authorised uid as a
// server-side precondition. It never falls back to a precondition-free delete: a
// conflict, a refusal, a timeout or any other failure is returned as itself, and
// the caller must not try again against whatever object stands there now.
func (d *podDeleter) deletePodWithUIDPrecondition(ctx context.Context, namespace, name, authorizedUID string) (deleteOutcome, error) {
	if d == nil || d.podsFor == nil {
		return deleteFailed, errors.New("no pod delete client was authorised for this run")
	}
	for _, required := range []struct{ field, value string }{{"namespace", namespace}, {"pod name", name}, {"uid", authorizedUID}} {
		if strings.TrimSpace(required.value) == "" {
			return deleteFailed, fmt.Errorf("the conditional delete needs a %s and has none", required.field)
		}
	}
	// A finished caller must not reach the server at all: past this point the change
	// may have happened without an answer, which is recorded as unknown rather than
	// replayed.
	if err := ctx.Err(); err != nil {
		return deleteFailed, errors.Wrapf(err, "refusing to issue the conditional delete of %s/%s on an already-finished context", namespace, name)
	}
	options := metav1.NewPreconditionDeleteOptions(authorizedUID)
	err := d.podsFor(namespace).Delete(ctx, name, *options)
	switch {
	case err == nil:
		return deleteDeleted, nil
	case apierrors.IsConflict(err):
		// 409 is the precondition firing: the object at this name is not the one
		// that was authorised. Nothing was deleted, whatever is there now stays,
		// and re-reading a fresh uid and deleting again would be exactly the
		// confusion this precondition exists to prevent.
		return deleteConflict, errors.Wrapf(err, "the pod %s/%s is no longer the authorised object (uid %s)", namespace, name, authorizedUID)
	case apierrors.IsNotFound(err):
		return deleteNotFound, errors.Wrapf(err, "no pod %s/%s existed to delete", namespace, name)
	case apierrors.IsForbidden(err):
		return deleteForbidden, errors.Wrapf(err, "this kubeconfig's identity may not delete pod %s/%s", namespace, name)
	case apierrors.IsTimeout(err), errors.Is(err, context.DeadlineExceeded):
		return deleteTimeout, errors.Wrapf(err, "the conditional delete of pod %s/%s timed out; the remote outcome is unknown", namespace, name)
	default:
		return deleteFailed, errors.Wrapf(err, "the conditional delete of pod %s/%s failed", namespace, name)
	}
}

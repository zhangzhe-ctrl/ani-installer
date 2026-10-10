# KServe Standard and LLM platform combination

Combination identity: {{ .ani.kubeflow.release }}. Standard and its original
runtime/S3/CA configuration belong to the base {{ .ani.kubeflow.base_release }}.
Official LLMI {{ (index .ani.components "kserve").version }} runs in `kserve`,
Helm release `ani-kserve-llmi`, Deployment `llmisvc-controller-manager`,
webhook Service `llmisvc-webhook-server-service`, Certificate
`llmisvc-serving-cert`, TLS Secret `llmisvc-webhook-server-cert`, and Lease
`llminferenceservice-kserve-controller-manager`.

Served contracts: `serving.kserve.io/v1beta1` InferenceService,
`serving.kserve.io/v1alpha1` LLMInferenceService (official conversion to
v1alpha2 storage), and `leaderworkerset.x-k8s.io/v1` LeaderWorkerSet.
The installer derives cert-manager and LWS from this combination.
The owned empty pipeline-parallel preset retains caller leader/worker PodSpecs
and startup commands. No router, scheduler, GIE, Istio or Knative is selected.
ANI owns subsequent HTTPRoute publication through its chosen Higress Gateway.

The CPU HTTP verifier replaces only the external inference engine boundary.
It checks admission/reconcile, actual UID ownership, shared PVC bytes and
HTTP content. Engine startup, model loading and ANI business acceptance are
separate work.

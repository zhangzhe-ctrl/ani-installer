## Kubeflow

- Release: {{ .ani.kubeflow.release }}; KFP 2.16.0, Trainer 2.1.0, JobSet 0.10.1, Argo 3.7.3.
- HTTP: https://{{ .ani.kubeflow.entry_address }}:{{ .ani.kubeflow.http_port }}
- gRPC TLS: {{ .ani.kubeflow.entry_address }}:{{ .ani.kubeflow.grpc_port }}
- Authentication: Kubernetes TokenReview; short-lived SA Bearer token, audience `pipelines.kubeflow.org`; Namespace SAR; shared reads disabled. No trusted client identity header.
- Entry CA: `kubeflow/ani-kfp-ca` ConfigMap key `ca.crt` (certificate only); frontend disabled.
- MySQL: dedicated databases and credentials; Secret references `kubeflow/ani-kfp-api-db`, `kubeflow/ani-kfp-mlmd-db`. No credential values in this fragment.
- Database storage: {{ .ani.kubeflow.database_class }} / {{ .ani.kubeflow.database_size }}.
- Workspace bounds: {{ .ani.kubeflow.workspace_class }} / {{ .ani.kubeflow.workspace_max_size }} / {{ .ani.kubeflow.workspace_max_claims }} claims per tenant, including retained failures.
- Managed environment namespaces: {{ range .ani.kubeflow.tenants }}`{{ . }}` {{ end }}
- Runtime: immutable `ani-single-process-v1-<approved execution digest first 8 characters>`; single node/process CPU. The actual name and UID are in the current install/check report. GPU behavior is not verified.
- Workspace mode: `managed-execution-pvc-v1`; create-only per-execution claim, captured PVC UID, no Workflow ownerReference, retained on failure/stop.
- Install evidence: independent `kubeflow-install-<attempt>/report.json` and `kubeflow-check-<attempt>/report.json` under {{ .ani.run.logs_dir }}. Control-plane smoke does not certify ENV05/ENV06 integration cases.
# Native Notebook and Standard serving contract

Notebook Controller runs standalone with USE_ISTIO=false. The environment probe
uses ani-kf-stage2-a and ani-kf-stage2-b, notebook-workload and predictor-workload
ServiceAccounts, both with token automount disabled and no Kubernetes creation
role. These are environment identities; product user ownership and login are
separate integration work.

An administrator prepares one independent RWX PVC ani-notebook-workspace per
probe namespace, StorageClass {{ .ani.kubeflow.workspace_class }}, capacity
{{ .ani.kubeflow.workspace_max_size }}, mounted at /home/jovyan with UID/GID
1000:1000. Notebook CR produces its StatefulSet, Pod and Service on port 8888.
The native base path is /notebook/<namespace>/<notebook-name>/. The token stays in
Secret ani-jupyter-auth; retrieve it only through authorized administrator
access. Limit any port-forward to the managed host and 127.0.0.1. Native stop uses
the kubeflow-resource-stopped annotation; removing it resumes the Notebook.
Stopping or deleting the Notebook preserves its separately created PVC. Data
deletion requires a separate, explicit owner/UID decision.

Model endpoint: HTTPS ani-rustfs-svc.ani-platform.svc.cluster.local:9000,
us-east-1, path-style, verified TLS. Each probe namespace has bucket
ani-kf-stage2-<namespace> and models/ prefix. Notebook receives only Secret
ani-model-writer (PutObject); storage-initializer consumes ani-model-reader
(GetObject and prefix-scoped ListBucket). ConfigMap ani-model-ca mounts ca.crt at
/etc/ani-model-ca/ca.crt and AWS_CA_BUNDLE points there. Credentials are absent
from these connection facts.

Only kserve-sklearnserver ClusterServingRuntime is registered. InferenceService
uses explicit Standard, no automatic Ingress/Gateway, with a Deployment and
internal <inferenceservice-name>-predictor Service. The fixed joblib model uses
sklearn 1.5.2 and joblib 1.4.2. POST /v1/models/<name>:predict with instances
[[0,0],[10,10],[20,20]] must return predictions [0,1,2]. The main probe records
the actual kernel model SHA256, S3 key and independent object hash.

These facts describe component access. They do not attest product tenants,
IAM/SSO, a management UI, business Gateway/WebSocket proxying or GPU acceptance.

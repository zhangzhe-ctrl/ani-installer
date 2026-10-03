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

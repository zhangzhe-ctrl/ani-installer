## Kubeflow

- Release: {{ .ani.kubeflow.release }}; KFP 2.16.0, Trainer 2.1.0, JobSet 0.10.1, Argo 3.7.3.
- HTTP: https://{{ .ani.kubeflow.entry_address }}:{{ .ani.kubeflow.http_port }}
- gRPC TLS: {{ .ani.kubeflow.entry_address }}:{{ .ani.kubeflow.grpc_port }}
- Authentication: Kubernetes TokenReview; short-lived SA Bearer token, audience `pipelines.kubeflow.org`; Namespace SAR; shared reads disabled. No trusted client identity header.
- Entry CA: `kubeflow/ani-kfp-entry-tls` key `ca.crt` (certificate only); frontend disabled.
- MySQL: dedicated databases and credentials; Secret references `kubeflow/ani-kfp-api-db`, `kubeflow/ani-kfp-mlmd-db`. No credential values in this fragment.
- Database storage: {{ .ani.kubeflow.database_class }} / {{ .ani.kubeflow.database_size }}.
- Workspace bounds: {{ .ani.kubeflow.workspace_class }} / {{ .ani.kubeflow.workspace_max_size }} / {{ .ani.kubeflow.workspace_max_claims }} claims per tenant, including retained failures.
- Managed environment namespaces: {{ range .ani.kubeflow.tenants }}`{{ . }}` {{ end }}
- Runtime: `ani-single-process-v1`; single node/process CPU. GPU behavior is not verified.
- Install evidence: {{ .ani.run.logs_dir }}/kubeflow-install.json. Control-plane smoke does not certify ENV05/ENV06 business or integration cases.

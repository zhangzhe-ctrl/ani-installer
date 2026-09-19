## Metrics and alerting (Prometheus + Alertmanager)

- namespace: `{{ .ani.components.metrics.namespace }}`
- version: chart `kube-prometheus-stack 85.4.0` (Prometheus Operator `v0.90.1`)
- workloads (all created by the Operator from the chart's custom resources):
  - `deployment/ani-metrics-operator` — Prometheus Operator, with its config-reloader sidecar
  - `statefulset/prometheus-ani-metrics-prometheus` (1 replica, 1 shard)
  - `statefulset/alertmanager-ani-metrics-alertmanager` (1 replica)
  - `deployment/ani-metrics-kube-state-metrics`
  - `daemonset/ani-metrics-prometheus-node-exporter` (one pod per node, including the control plane)
- service DNS / port:
  - Prometheus UI and HTTP API: `ani-metrics-prometheus.{{ .ani.components.metrics.namespace }}.svc.cluster.local:9090`
  - Alertmanager UI and API: `ani-metrics-alertmanager.{{ .ani.components.metrics.namespace }}.svc.cluster.local:9093`
  - Operator webhook: `ani-metrics-operator.{{ .ani.components.metrics.namespace }}.svc.cluster.local:443`
- versions in use: Prometheus `v3.11.3-distroless`, Alertmanager `v0.32.1`,
  kube-state-metrics `v2.19.0`, node-exporter `v1.11.1-distroless`
- scopes: node metrics from node-exporter; object state from kube-state-metrics;
  container/cAdvisor metrics and API-server metrics through the API server proxy.
  etcd, controller-manager, scheduler, kube-proxy, CoreDNS and kube-dns scrape
  targets are **off** — this cluster binds those ports to loopback, and this
  batch does not open them or hand out control-plane credentials to scrape them
- PVCs (StorageClass `{{ .ani.components.metrics.storage_class }}`):
  - Prometheus `{{ .ani.components.metrics.prometheus_storage_size }}` — TSDB blocks and WAL
  - Alertmanager `{{ .ani.components.metrics.alertmanager_storage_size }}` — silences and notification state
- retention: `{{ .ani.components.metrics.prometheus_retention }}`, capped at
  `{{ .ani.components.metrics.prometheus_retention_size }}` on disk so the volume
  cannot fill up before the time limit; Alertmanager retention is unbounded by age
- disabled on purpose in this batch: Grafana, Thanos (Ruler and every sidecar),
  remote write, the Prometheus admin API, the OTLP receiver, the default rule
  library, and Windows monitoring
- rule intake: only `PrometheusRule` objects in `{{ .ani.components.metrics.namespace }}`
  labelled `release=ani-metrics` are loaded
- alert routing intake: only `AlertmanagerConfig` objects in
  `{{ .ani.components.metrics.namespace }}` labelled `run_id` are accepted
- secrets (references only, no values):
  - `{{ .ani.components.metrics.namespace }}/ani-metrics-admission` — webhook serving certificate, issued by the Operator's certgen
- credentials: this stack has no user-facing credential. Prometheus and Alertmanager
  are unauthenticated inside the cluster, exactly as the upstream chart ships them;
  this document never contains a token or password
- durability: Prometheus and Alertmanager each write to their own RBD PVC, so
  rebuilding a pod keeps the series and silences. This is durability against a pod
  rebuild, not an HA claim — both run one replica
- verification: `verify.sh` (packaged at `/etc/kubernetes/ani/metrics/verify.sh`)
  queries real series through the Prometheus HTTP API, drives a rule from firing to
  resolved through Alertmanager to a temporary receiver, rebuilds the Prometheus
  and Alertmanager pods, and reads the pre-rebuild sample and silence back

## Log storage backend (Loki, monolithic)

- namespace: `{{ .ani.components.logging.namespace }}`
- backend selected: `loki` (this is a first-install choice, not a runtime switch:
  a deployment either has Loki or OpenSearch, never both)
- version: chart `loki 18.13.3`, Loki `3.7.8`
- workload: `statefulset/ani-loki` (1 replica, monolith mode)
- service DNS / port:
  - push and query API: `ani-loki.{{ .ani.components.logging.namespace }}.svc.cluster.local:3100`
  - the Service is `ClusterIP` only — there is no NodePort, Ingress or Gateway,
    and no public endpoint by design
- storage: `filesystem` on one RBD PVC
  - PVC `storage-ani-loki-0`, StorageClass `{{ .ani.components.logging.storage_class }}`,
    size `{{ .ani.components.logging.storage_size }}`, mounted at `/var/loki`
  - the write-ahead log, the TSDB index, the compactor working directory and the
    deletion markers all live on this volume, so a pod rebuild keeps the logs
- retention: `{{ .ani.components.logging.retention_hours }}h`
  (site value `retentionDays: {{ .ani.components.logging.retention_days }}`),
  enforced by the Loki Compactor (`retention_enabled: true`, delete store
  `filesystem`). Setting the limit without the compactor would mark nothing for
  deletion, which is why both are configured
- authentication: `auth_enabled: false`. This is a single-tenant service inside
  the cluster and is deliberately **not** an authentication feature; anyone who
  can reach the Service can read and write logs. It must not be published
  outside the cluster
- disabled on purpose in this batch: the distributed `read`/`write`/`backend`
  replicas, both memcached caches (`chunksCache`, `resultsCache`), the gateway,
  the canary, the ruler, MinIO/S3 object storage, and the chart's self-test hook
- credentials: none. This fragment never contains a token or password
- verification: `verify.sh` (packaged at `/etc/kubernetes/ani/loki/verify.sh`)
  checks the StatefulSet, the bound PVC, the live HTTP API (`/ready`,
  buildinfo, a real `query_range`), and that the running configuration reports
  the configured retention, compactor and schema. It does **not** prove that
  container logs reach this backend — that is the collection path, verified by
  the Fluent Bit component

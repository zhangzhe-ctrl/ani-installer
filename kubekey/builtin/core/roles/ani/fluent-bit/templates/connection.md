## Log collection (Fluent Bit)

- namespace: `{{ .ani.components.logging.namespace }}`
- version: chart `fluent-bit 0.58.2`, Fluent Bit `5.1.2`
- workload: `daemonset/ani-fluent-bit` — one pod per node, so every node's
  container logs are collected; the ready count is checked against the node
  count rather than assumed
- what is collected: the container log files under `/var/log/containers/*.log`
  (mounted read-only), parsed with the CRI multiline parser. This is container
  stdout/stderr only — kubelet's own journal is **not** collected, because this
  batch is about application container logs
- metadata: the kubernetes filter attaches namespace, pod, container and node
  to every record. The full Kubernetes label and annotation sets stay in the
  record body and are deliberately **not** promoted to Loki stream labels — an
  unbroken label set would explode the index
- single write path: the chart's two default Elasticsearch outputs are replaced
  by exactly one output. Every record is written once, to
  `{{ .ani.components.logging.backend }}` and nowhere else. `verify.sh` asserts
  there is exactly one output block and that it is the selected backend, so a
  second write path cannot be introduced silently
- local buffering: the tail cursor (`tail.db`) and the filesystem buffer live
  under `/var/lib/ani-installer/fluent-bit` on each node (a hostPath, created on
  demand). The buffer has an upper bound (`storage.total_limit_size`), so if the
  backend is unreachable the collector buffers within that bound rather than
  losing the cursor or filling the disk. The buffered records are **not** a
  replacement for the backend's own PVC — the durable copy is in the backend
- destination:
{{- if eq .ani.components.logging.backend "loki" }}
  - Loki push API `ani-loki.{{ .ani.components.logging.namespace }}.svc.cluster.local:3100`
{{- end }}
{{- if eq .ani.components.logging.backend "opensearch" }}
  - OpenSearch `ani-opensearch-master.{{ .ani.components.logging.namespace }}.svc.cluster.local:9200`,
    index prefix `ani-logs`
{{- end }}
- credentials: none embedded here. When the backend is OpenSearch the collector
  reads its username and password from the `ani-opensearch-fluent-bit` Secret
  through environment variables; this fragment never contains a token
- verification: `verify.sh` (packaged at
  `/etc/kubernetes/ani/fluent-bit/verify.sh`) writes a unique marker to each
  node's stdout from a throwaway pod, then **queries the backend** for it and
  asserts the pod/container/node metadata, rebuilds the backend pod and re-reads
  the markers, and rebuilds one collector pod to confirm the cursor survives and
  collection resumes. It never pushes to the backend directly — a direct push
  would not prove that collection works
- not verified in this run: that records are actually **deleted** once their
  retention period expires (recorded as `retention-expiry=not_verified`). The
  retention *configuration* is verified by the backend component

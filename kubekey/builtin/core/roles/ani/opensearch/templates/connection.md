## Log storage backend (OpenSearch, single node)

- namespace: `{{ .ani.components.logging.namespace }}`
- backend selected: `opensearch` (this is a first-install choice, not a runtime
  switch: a deployment either has OpenSearch or Loki, never both)
- version: chart `opensearch 3.8.0`, OpenSearch `3.8.0`
- workload: `statefulset/ani-opensearch-master` (1 replica, single node)
- service DNS / port:
  - HTTP API: `ani-opensearch-master.{{ .ani.components.logging.namespace }}.svc.cluster.local:9200`
  - transport: port `9300` on the same Service; a headless Service
    (`ani-opensearch-master-headless`) exists for the transport addresses
  - the Service is `ClusterIP` only — there is no NodePort, Ingress or Gateway,
    and no public endpoint by design
- storage: one RBD PVC
  - PVC `ani-opensearch-master-ani-opensearch-master-0`, StorageClass `{{ .ani.components.logging.storage_class }}`,
    size `{{ .ani.components.logging.storage_size }}`, mounted at `/usr/share/opensearch/data`
  - the indices live on this volume, so a pod rebuild keeps the logs
- retention: `{{ .ani.components.logging.retention_days }}` days
  (rendered as the ISM duration `{{ .ani.components.logging.retention_iso }}`),
  enforced by the index-state-management policy `ani-logs-retention`, which moves
  a day index to a `delete` state once its age exceeds that period. The day
  indices are named `ani-logs-YYYY.MM.DD`
- index shape: the template `ani-logs-*` sets one primary shard and zero
  replicas. A single node cannot allocate a replica, so leaving the default
  would leave every index yellow forever
- TLS: the node and the HTTP layer present a certificate issued by the
  cert-manager internal CA (`ani-ca` ClusterIssuer), with the node DN
  `CN=ani-opensearch-node`. A client must trust that CA; the CA certificate is
  in the Secret `ani-opensearch-node-tls` under the key `ca.crt`
- authentication: **on, and not optional.** The demo configuration is never
  installed (`DISABLE_INSTALL_DEMO_CONFIG=true`), the demo certificates are not
  trusted (`allow_unsafe_democertificates: false`), and the security index is
  not initialized by the plugin itself
  (`allow_default_init_securityindex: false`) — it is seeded once by
  `securityadmin.sh` during the install. Anonymous access is off, so every
  request needs a credential
- credentials, and where to find them — this fragment deliberately contains no
  password:
  - the cluster administrator's credential is in the Secret
    `ani-opensearch-admin` (keys `username`, `password`), created during the
    install with a password generated in the cluster. Read it with
    `kubectl -n <namespace> get secret ani-opensearch-admin -o jsonpath='{.data.password}' | base64 -d`
  - the collector's credential is in the Secret `ani-opensearch-fluent-bit`
    (keys `username`, `password`). Fluent Bit reads it through
    `secretKeyRef`; it is never written into a rendered file
  - the collector account `ani-collector` may write to `ani-logs-*` and read
    nothing: its role grants the write path and `cluster_monitor` only
- disabled on purpose in this batch: additional nodes (this is one single-node
  master, not a cluster with replicas or dedicated data nodes), OpenSearch
  Dashboards, the service monitor, plugins, and the Chart's privileged
  `sysctl` init container (the installer sets `vm.max_map_count` on the node
  through `/etc/sysctl.d/90-ani-opensearch.conf` instead)
- verification: `verify.sh` (packaged at
  `/etc/kubernetes/ani/opensearch/verify.sh`) checks the StatefulSet, the bound
  PVC, that an anonymous request is refused while the administrator's succeeds
  over TLS with the cluster CA, that the demo password does not work, the
  node certificate's subject, the day-index template, the retention policy, and
  that no public endpoint or unexpected workload exists. It does **not** prove
  that container logs reach this backend — that is the collection path, verified
  by the Fluent Bit component

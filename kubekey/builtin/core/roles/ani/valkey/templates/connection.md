## Valkey

- namespace: `ani-platform`
- version: `valkey/valkey:8.1.10-alpine`
- workload: `statefulset/valkey` (1 replica)
- service DNS / port: `valkey.ani-platform.svc.cluster.local:6379` (ClusterIP)
- PVC: `data-valkey-0`, StorageClass `{{ .ani.components.valkey.storage_class }}`, request `{{ .ani.components.valkey.storage_size }}`, access mode ReadWriteOnce
- retention: no time-based retention; data persists on the PVC until the PVC is deleted
- secrets (references only, no values):
  - `ani-platform/ani-valkey-config` — server config including `requirepass`
- credentials: the password is loaded by the server from the Secret-backed config file; this document never contains it
- verification: `verify.sh` (packaged at `/etc/kubernetes/ani/valkey/verify.sh`) authenticates over the Service DNS, performs real SET/GET against the running instance and confirms the RWO PVC is bound

## PostgreSQL

- namespace: `ani-platform`
- version: `postgres:17.11-bookworm`
- workload: `statefulset/postgresql` (1 replica)
- service DNS / port: `postgresql.ani-platform.svc.cluster.local:5432` (ClusterIP)
- application database and account: database `ani`, role `ani_app` (created by the packaged initdb ConfigMap on first init)
- PVC: `data-postgresql-0`, StorageClass `{{ .ani.components.postgresql.storage_class }}`, request `{{ .ani.components.postgresql.storage_size }}`, access mode ReadWriteOnce
- retention: no time-based retention; data persists on the PVC until the PVC is deleted
- secrets (references only, no values):
  - `ani-platform/ani-postgres-admin` — superuser password (`postgres-password` key)
  - `ani-platform/ani-postgres-app` — application password (`app-password` key)
  - `ani-platform/ani-postgres-initdb` — first-init script ConfigMap
- credentials: passwords are read by the container from the Secrets above; this document never contains them
- verification: `verify.sh` (packaged at `/etc/kubernetes/ani/postgresql/verify.sh`) connects as the application account over the Service DNS, runs real SQL and confirms the RWO PVC is bound

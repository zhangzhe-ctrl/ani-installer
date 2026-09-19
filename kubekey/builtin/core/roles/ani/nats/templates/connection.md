## NATS (JetStream)

- namespace: `ani-platform`
- version: chart `2.14.6` (app `2.14.6-alpine`)
- workload: `statefulset/nats` (1 replica; clustering disabled)
- service DNS / port: `nats.ani-platform.svc.cluster.local:4222` (client), `nats-headless.ani-platform.svc.cluster.local` (headless)
- JetStream: enabled, file storage on the PVC (`replicas=1` streams), memory storage disabled
- PVC: managed by the Chart for `statefulset/nats`, StorageClass `{{ .ani.components.nats.storage_class }}`, request `{{ .ani.components.nats.storage_size }}`
- retention: no time-based retention; JetStream messages persist in the file store on the PVC until a stream's own limits remove them
- secrets (references only, no values):
  - `ani-platform/ani-nats-auth` — client token (`token` key)
- credentials: the token reaches the server and CLI only through the Secret; this document never contains it
- verification: `verify.sh` (packaged at `/etc/kubernetes/ani/nats/verify.sh`) proves token auth is enforced over the Service DNS and exercises JetStream end to end (create a file-storage stream, a durable pull consumer, publish, consume and ack)

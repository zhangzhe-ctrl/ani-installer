## RustFS standalone

- namespace: `ani-platform`
- release/chart: `ani-rustfs` / official RustFS `1.0.0`
- service: `https://ani-rustfs-svc.ani-platform.svc.cluster.local:9000`
- workload: Deployment `ani-rustfs`; data PVC `ani-rustfs-data` on `{{ .ani.components.rustfs.storage_class }}` (`{{ .ani.components.rustfs.storage_size }}`)
- server certificate: `ani-platform/ani-rustfs-tls`, issued by the internal `ani-ca`; public CA: `ani-platform/ani-rustfs-ca`
- root credentials: private Secret `ani-platform/ani-rustfs-root`, retained across reruns and never supplied to Milvus
- availability: one RustFS server process; this is not a high-availability object service

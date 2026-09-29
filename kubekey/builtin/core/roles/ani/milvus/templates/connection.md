## Milvus Standalone

- namespace: `ani-platform`
- release/chart: `ani-milvus` / Milvus `5.0.25` with service image `v2.6.24`
- service: `ani-milvus.ani-platform.svc.cluster.local:19530`
- deployment: `ani-milvus-standalone` (one replica, RocksMQ)
- metadata: dedicated `ani-milvus-etcd` StatefulSet, one replica
- object store: `{{ .ani.objectStorage.milvus_s3.provider }}` at `{{ .ani.objectStorage.milvus_s3.endpoint }}`; bucket `{{ .ani.objectStorage.milvus_s3.bucket }}`, rootPath `{{ .ani.objectStorage.milvus_s3.root_path }}`
- S3 credentials: Secret `{{ .ani.objectStorage.milvus_s3.secret_namespace }}/{{ .ani.objectStorage.milvus_s3.secret_name }}`; no value is printed here
- trusted CA: ConfigMap `{{ .ani.objectStorage.milvus_s3.ca_namespace }}/{{ .ani.objectStorage.milvus_s3.ca_config_map }}` mounted at `{{ .ani.objectStorage.milvus_s3.ca_path }}`
- storage: `{{ .ani.components.milvus.storage_class }}`; Milvus `{{ .ani.components.milvus.storage_size }}`, etcd `{{ .ani.components.milvus.etcd_storage_size }}`
- availability: fixed Standalone, not a high-availability deployment

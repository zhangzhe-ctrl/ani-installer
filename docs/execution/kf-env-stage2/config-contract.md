# 二阶段配置与接入合同

完整站点见 `site.example.yaml`。沿用 `kubeflow.enabled` 与 `kubeflow.release`；省略或关闭时不创建二阶段对象。启用时 release 必须为 `26.03-kubeflow-stage2-v1`，要求 full 首装、持久存储、RustFS 和 cert-manager。没有 Notebooks/KServe 子开关或追加安装入口。

## 固定来源与平台

KFP 2.16.0、Trainer 2.1.0、JobSet 0.10.1 沿用 BASE_SHA 锁定字节。Notebook Controller 1.10.0 源码为 `90e987bf87d3e7c900926310b00bfa16b59e41eb`，使用 standalone，`USE_ISTIO=false`。KServe 0.16.0 源码为 `5b033a4024429302440b72180472ae2d26b44086`，显式 Standard，仅注册 CPU sklearn Runtime。

核心版本来自 [Kubeflow 26.03 发行矩阵](https://www.kubeflow.org/docs/kubeflow-distribution/releases/kubeflow-26.03/)。[KServe 0.16 Standard 文档](https://raw.githubusercontent.com/kserve/website/main/versioned_docs/version-0.16/admin-guide/kubernetes-deployment.md)要求 Kubernetes 1.32+、cert-manager 1.15+；当前 BASE_SHA 为 Kubernetes 1.35.8、cert-manager 1.21.2，未升级共享组件。满足版本要求并不证明 ANI 裁剪组合已实测兼容；实际状态只见本轮 README 与交接。

保留 Standard manager 实际注册的 InferenceService、TrainedModel、InferenceGraph、ServingRuntime、ClusterServingRuntime 与 LocalModelCache 等 CRD/webhook 依赖。排除的两个 LLMI webhook 在该 manager 中没有注册端点，属于未选择的独立 LLM controller。逐对象处置和渲染输入摘要保存在 `kubekey/ani/kubeflow/stage2/overlay/overlay.lock.json`。关闭自动 Ingress、Gateway API、自动 Gateway、modelcar 和 LocalModel 业务能力；不安装 Istio/Knative/LWS/LLMI/GPU Runtime。

## 原生资源与模型

安装器准备两个独立环境探针 Namespace `ani-kf-stage2-a/b`、SA、配额、NetworkPolicy、Jupyter token Secret、模型 writer/reader Secret 与 CA ConfigMap。受限管理员探针另外创建 PVC 和 Notebook CR；这不是产品生命周期后端。Notebook 的工作负载与 KFP pipeline-runner 权限分开。

每个探针 Namespace 一张独立 `ani-notebook-workspace` PVC，RWX、站点 workspace StorageClass/maxSize，挂载 `/home/jovyan`，UID/GID/fsGroup 1000。Notebook Controller 创建 StatefulSet/Pod/Service；内部 Service 的 80 端口映射容器原生 Jupyter 8888，base path `/notebook/<namespace>/<name>/`。使用 Secret token 和受限管理员连接，port-forward 只绑定受管主机的 127.0.0.1。停止用 Notebook 的 `kubeflow-resource-stopped` 注解，恢复移除注解；不缩放子 StatefulSet。独立 PVC 不由 Notebook ownerReference 回收。

工作区镜像固定 Python 3.11.14、sklearn 1.5.2、joblib 1.4.2、JupyterLab 4.4.10，全部 wheel 与 OCI 镜像字节闭合。摘要在源锁和 `ani/images.tsv`；运行时不安装依赖。固定生成代码在 `workspace/model.py`，DecisionTreeClassifier、random_seed=42、joblib 模型文件 `model.joblib`；实际主链记录模型 SHA256 与 S3 key。固定输入 `[[0,0],[10,10],[20,20]]`，期望 `[0,1,2]`。

S3 为 HTTPS `ani-rustfs-svc.ani-platform.svc.cluster.local:9000`、region `us-east-1`、path-style。每个 Namespace 的 bucket 为 `ani-kf-stage2-<namespace>`，只开放 `models/`。Notebook 的 `ani-model-writer` 允许 PutObject；KServe 的 `ani-model-reader` 允许 GetObject 与前缀限定的 ListBucket。两者不是同一个凭据。`ani-model-ca` 的 `ca.crt` 挂载到 `/etc/ani-model-ca/ca.crt`，由真实 `AWS_CA_BUNDLE` 消费；不关闭 TLS 校验。

InferenceService 显式 Standard，生成 `<name>-predictor` Deployment/内部 Service。实际请求 `POST /v1/models/<name>:predict`，body 为 `{"instances":[[0,0],[10,10],[20,20]]}`；必须断言 predictions，Ready/HTTP 200 不替代结果。主链探针用不同只读身份独立核对 kernel 上传对象的摘要。

这份合同交给后续 backend/前端做 owner、用户身份、长期保留/GC、登录与业务 Gateway/WebSocket 接入。本轮原生组件与环境身份检查不能证明上述产品能力或 GPU 验收。

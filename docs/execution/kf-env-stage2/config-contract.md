# 二阶段配置与接入合同

完整站点见 `site.example.yaml`。沿用 `kubeflow.enabled` 与 `kubeflow.release`；省略或关闭时不创建二阶段对象。`26.03-kubeflow-stage2-v1` 保持原 Notebook/Standard 组合，要求 full 首装、持久存储、RustFS 和 cert-manager。新组合 `26.10-kubeflow-llm-v1` 保留同一 Standard 与 KFP 基础材料，增加官方 KServe LLMI v0.17.0 并自动引入 LWS v0.10.0；不增加 `kserve.enabled` 或独立 LLMI 开关。`components.lws.enabled` 可独立选择 LWS。保留集群通过 `components install/execute --only kserve` 补齐 LLMI，仅允许存在原成功 Kubeflow Standard 基础且身份、材料、共享对象归属匹配，不重跑 Standard。当前真实交付状态以唯一进度记录为准。

`components.higress.enabled` 独立选择官方 Higress 2.2.4，不派生 Kubeflow。
正式字段为 `gatewayClass`、`gatewayName`、`httpPort`、`httpsPort`、`hostname`、
`tlsSecret`、`allowedNamespaces`、`wasmRegistry` 和可选 `probeKeyFile`。
HTTP/预留 HTTPS 默认 30445/30446；拒绝与 Kubeflow 的 30443/30444、Harbor
30002/30003 或材料 registry 冲突。没有命名 TLS Secret 时不创建 HTTPS listener
或对应 Service 端口，不把预留端口记为 HTTPS 可用。

`wasmRegistry=harbor` 要求已选择 Harbor，并复用其受验证 CA；完整组合默认
使用此路径。独立安装默认 `material`，只向本站原只读材料 registry 的专用仓库
追加批准 OCI 字节，不替换 registry unit/store、不重启服务。
四个插件由独立 `OCIWasmMaterialLock` 绑定原 manifest/config/content 摘要；
插件不是 Pod 镜像。共享 Gateway API 先核对 served API 与 Established，再复用；
缺失 API 仅从现有批准 Gateway API 材料补齐，不升级已存在 CRD。

Higress 补装使用 `components install/execute --only higress`。正式角色仅保留
controller/gateway 与其 RBAC/Service/ConfigMap，固定三个平台镜像，不安装
console、独立观测栈、GIE 或 Istio。平台 WasmPlugin 默认禁用、没有业务凭据；
正式探针在隔离域名启用四插件、临时身份和两个明确的外部引擎边界 backend，
随后恢复默认并正常清理。API key 在内存中注入真实 key-auth schema，未假设
SecretKeyRef；可选输入文件须为私有权限的普通文件。运行态路由、模块加载、
权限负例和冷启动结果必须另以本候选真实集群验收确认。

当前网络合同支持 Kube-OVN，以及明确接受能力差异的 KCN 测试环境。Kube-OVN 省略 `kubeflow.networkPolicy` 或填 `required`，必须启用策略控制器、安装规定 NetworkPolicy 并通过健康控制端点拒绝和 DNS/S3 正向探针。KCN 必须填 `kubeflow.networkPolicy: kcn-test-unsupported-v1`；这是固定测试环境能力合同，不是通用跳过检查开关。完整选择示例见 `config/examples/kcn-full.yaml`。

Go 从网络选择推导 provider、NetworkPolicy 能力与合同，正式 site 将其与锁定 CNI 镜像一同传给安装、检查、smoke 和 stage2 探针。缺失或矛盾的声明、不同的 live provider/镜像、Kube-OVN 未开启策略控制器都失败。KCN 不创建声称有效的 NetworkPolicy，不把 KCN 原生隔离对象充当它；控制端点记录实际可达性与 `unsupported`，不生成网络隔离就绪。Kubernetes 身份/RBAC、跨 Namespace Secret、S3 writer/reader 与前缀、应用 HTTP/gRPC TokenReview/audience 等独立保护继续要求通过。原有 Kube-OVN live 记录保持原来的环境、候选和隔离含义，不转记为 KCN PASS。

## 固定来源与平台

KFP 2.16.0、Trainer 2.1.0、JobSet 0.10.1 沿用 BASE_SHA 锁定字节。Notebook Controller 1.10.0 源码为 `90e987bf87d3e7c900926310b00bfa16b59e41eb`，使用 standalone，`USE_ISTIO=false`。KServe 0.16.0 源码为 `5b033a4024429302440b72180472ae2d26b44086`，显式 Standard，仅注册 CPU sklearn Runtime。

核心版本来自 [Kubeflow 26.03 发行矩阵](https://www.kubeflow.org/docs/kubeflow-distribution/releases/kubeflow-26.03/)。[KServe 0.16 Standard 文档](https://raw.githubusercontent.com/kserve/website/main/versioned_docs/version-0.16/admin-guide/kubernetes-deployment.md)要求 Kubernetes 1.32+、cert-manager 1.15+；当前 BASE_SHA 为 Kubernetes 1.35.8、cert-manager 1.21.2，未升级共享组件。满足版本要求并不证明 ANI 裁剪组合已实测兼容；实际状态只见本轮 README 与交接。

保留 Standard manager 实际注册的 InferenceService、TrainedModel、InferenceGraph、ServingRuntime、ClusterServingRuntime 与 LocalModelCache 等 CRD/webhook 依赖。Standard 的两个未注册 LLMI webhook 仍从原 overlay 排除，处置和渲染输入摘要保存在 `kubekey/ani/kubeflow/stage2/overlay/overlay.lock.json`。新组合由独立官方 LLMI Chart 提供自己的 controller、CRD conversion、webhook、证书和 Lease，复用现有 cert-manager 与 Ready 的 `ani-ca` ClusterIssuer；不创建原组合已排除的 `selfsigned-issuer`，不修改既有 CA 或覆盖 `inferenceservice-config`。仅安装 LLMI API 和控制器，不安装 GIE/router/scheduler；调用方继续自行发布 HTTPRoute。空的官方 LLMInferenceServiceConfig 预置不注入引擎命令或覆盖调用方 leader/worker PodSpec，消费 API 为 v1alpha1，存储版本为 v1alpha2。Istio、Knative 和 GPU Runtime 保持未选择。

固定 v0.17.0 LLMI Chart 的受控过滤器补齐官方 CA bundle reconciler 所需的 ConfigMap 创建权限与 `global-ca-bundle` 更新权限；不授予 Pod 写权限或其他 ConfigMap 的更新权限。

## 原生资源与模型

安装器准备两个独立环境探针 Namespace `ani-kf-stage2-a/b`、SA、配额、NetworkPolicy、Jupyter token Secret、模型 writer/reader Secret 与 CA ConfigMap。受限管理员探针另外创建 PVC 和 Notebook CR；这不是产品生命周期后端。Notebook 的工作负载与 KFP pipeline-runner 权限分开。

每个探针 Namespace 一张独立 `ani-notebook-workspace` PVC，RWX、站点 workspace StorageClass/maxSize，挂载 `/home/jovyan`，UID/GID/fsGroup 1000。Notebook Controller 创建 StatefulSet/Pod/Service；内部 Service 的 80 端口映射容器原生 Jupyter 8888，base path `/notebook/<namespace>/<name>/`。使用 Secret token 和受限管理员连接，port-forward 只绑定受管主机的 127.0.0.1。停止用 Notebook 的 `kubeflow-resource-stopped` 注解，恢复移除注解；不缩放子 StatefulSet。独立 PVC 不由 Notebook ownerReference 回收。

工作区镜像固定 Python 3.11.14、sklearn 1.5.2、joblib 1.4.2、JupyterLab 4.4.10，全部 wheel 与 OCI 镜像字节闭合。摘要在源锁和 `ani/images.tsv`；运行时不安装依赖。固定生成代码在 `workspace/model.py`，DecisionTreeClassifier、random_seed=42、joblib 模型文件 `model.joblib`；实际主链记录模型 SHA256 与 S3 key。固定输入 `[[0,0],[10,10],[20,20]]`，期望 `[0,1,2]`。

S3 为 HTTPS `ani-rustfs-svc.ani-platform.svc.cluster.local:9000`、region `us-east-1`、path-style。每个 Namespace 的 bucket 为 `ani-kf-stage2-<namespace>`，只开放 `models/`。Notebook 的 `ani-model-writer` 允许 PutObject；KServe 的 `ani-model-reader` 允许 GetObject 与前缀限定的 ListBucket。两者不是同一个凭据。Notebook 的 `ani-model-ca` 使用 `ca.crt`，挂载 `/etc/ani-model-ca/ca.crt`。KServe 0.16 从控制器命名空间 `kserve/ani-model-ca` 读取 `cabundle.crt`，传播到工作负载命名空间的 `global-ca-bundle`，下载器的 `AWS_CA_BUNDLE` 指向 `/etc/ani-model-ca/cabundle.crt`。两种路径均消费同一安装公共 CA；不关闭 TLS 校验。

InferenceService 显式 Standard，生成 `<name>-predictor` Deployment/内部 Service。实际请求 `POST /v1/models/<name>:predict`，body 为 `{"instances":[[0,0],[10,10],[20,20]]}`；必须断言 predictions，Ready/HTTP 200 不替代结果。主链探针用不同只读身份独立核对 kernel 上传对象的摘要。

这份合同交给后续 backend/前端做 owner、用户身份、长期保留/GC、登录与业务 Gateway/WebSocket 接入。本轮原生组件与环境身份检查不能证明上述产品能力或 GPU 验收。

# ENV01 配置与首装合同

状态：CODE_READY / REMOTE_CHECKED / ENV_READY。源候选 `dee542af0122e9f95bded2f349849df3a1a53e4a` 经真实联调、冻结、最终干净统一首装和新环境独立 EAC01～EAC14 验证。实际发布物与结果分别见 `records/candidate-freeze.json`、`records/environment-handoff.json`。CPU-P01/BFF 业务和 GPU 未验收。

首装站点新增 `kubeflow` 节，省略或 `enabled: false` 保持既有选择与安装行为。启用只接受本轮固定 `release: 26.03-kfp2.16-trainer2.1-v1`，与 `profile: full` 配合；不接受未知 release、GPU runtime、base profile 或隐式缺依赖。产品名称仍为 Kubeflow。部署角色只在 `create_cluster` 的所选基础组件尾部接入；不把角色加入 `ani_components.yaml`。

固定源与版本：KFP 2.16.0、SDK/driver/launcher 同版、Argo v3.7.3、Trainer v2.1.0、JobSet v0.10.1。传统 MySQL 模式，API/cache/MLMD 使用专属库与账号、受管 Secret、持久 CSI 卷；不共用 ANI 业务表，不改 PostgreSQL 技术栈。精确镜像由实际上游渲染与动态源码引用枚举，未知 digest 禁止安装。数据库镜像的上游 `mysql:8.4` 必须落为固定补丁版/摘要并记录差异，不能用浮动 minor tag 交付。

## 认证和入口

采用固定 Nginx TLS 入口与 KFP 原生 TokenReview。受限客户端携带 audience 为 `pipelines.kubeflow.org` 的短期 Kubernetes SA Bearer token；KFP TokenReview 验证身份并继续 Namespace SAR，`MULTIUSER=true`、`MULTIUSER_SHARED_READ=false`。环境探针 SA 只获 A 或 B 的 KFP 权限，后续 CPU02 再绑定正式服务身份。

固定 KFP 源 `auth.GetAuthenticators` 的首个认证器是 HTTP 身份头，随后才是 TokenReview。空环境变量被 Viper 默认忽略，不能用空 `KUBEFLOW_USERID_HEADER` 表示关闭。overlay 将该字段固定为 `:`（普通 HTTP/gRPC 客户端不能声明的头名），随后使用原生 TokenReview。固定 Nginx 1.30.5 TLS 入口拒绝/剥离常见身份头及变体，不能仅依赖 NetworkPolicy。联调环境已通过 62 项真实 HTTP/gRPC 请求，覆盖无凭据、无效/错误 audience、重复 Authorization、重复/大小写/下划线身份头和 A→B；最终环境独立复验，不沿用该 PASS。

原始 API HTTP/gRPC 端口只允许本期控制面必要调用和入口 Pod；训练 Pod 无控制面证书、管理员 S3 凭据、默认 SA token 或任意 Kubernetes 创建权。MLMD/MySQL 只允许已列明内部消费者；受限启动/driver 的必要访问单列，不能给全部 Namespace 放通。

本轮默认无 frontend、Istio、Dex、oauth2-proxy、Profile/KFAM、Dashboard、Notebook、Katib、TensorBoard、Hub、Knative、LWS、GPU/HAMi。KFP pipelines-profile-controller 的租户初始化由受管清单补齐：SA/RBAC、kfp-launcher、元数据与存储引用、配额/AdmissionPolicy/NetworkPolicy。其他内部对象先枚举说明用途，不凭名字删减。

## Runtime 与工作区

一个版本化 Runtime，当前为单节点单进程 CPU，固定镜像和 entrypoint、Never restart、JobSet 有界失败策略；不批量开放 upstream CUDA runtime。AdmissionPolicy 拒绝多节点/GPU/未获准 Runtime 和任意覆盖存储的请求。

固定本轮工作区为 `managed-execution-pvc-v1`：环境 probe 程序为单执行 create-only 创建独立 PVC，捕获响应 UID，无 Workflow ownerReference，显式把 claim 名称/UID 传到外部 TrainJob。人不逐 Run 建卷；这不是 modeldev 的正式工作区 owner。需真实验证只读输入/独立输出、跨 Pod/必要跨节点交接、两执行隔离及失败/停止保留。

选择依据来自固定 KFP 源：`GetWorkspacePVC` 会应用用户 PVC patch，再用用户 size 覆盖默认 requests；原生卷由 Argo Workflow 管理。`backend/src/apiserver/resource/resource_manager.go:ReportWorkflowResource` 会删除已持久终态的 Workflow，持久化 agent 默认一天后再次上报；该生命周期不能保证发布未成的唯一文件继续保留。不能把 agent TTL 设置为 0 当作关闭 GC，0 会立即越过 TTL。故安装前固定上述独立卷方案，保留上游 Workflow GC，不运行失败后切换。原生 Workspace 的运行行为仍为 NOT_RUN，不声称实机复现。管理员以 admission 和 ResourceQuota 强制限定 StorageClass、容量、卷数量与 owner 策略。

## 容量核算

最终完整站点及产品 smoke 后共 28 个 PVC，声明合计 140Gi，含 MySQL 20Gi、四个 5Gi 执行卷及保留的存储探针卷。现场 raw 600Gi，RBD/CephFS 三副本，声明上界 420Gi；另预留 raw 20%（120Gi），剩余 60Gi。原始 PVC 与副本记录在最终证据目录。预算不含无限保留、新增外部负载或无限快照增长；未减少批准容量或副本。

三节点总 allocatable CPU 46.8、memory 约 88Gi。最终 scheduler 分配表已计入实际 Pod/init/DaemonSet 请求，基础 CPU 请求合计 7.325；两租户 requests.cpu 配额合计 12，合计上界 19.325。Runtime 每执行请求 1 CPU/512Mi，限制 1 CPU/2Gi。真实用例顺序执行并验证互不覆盖；这些容量规划不等于最大并发或吞吐验收。

## 写入与复验边界

ENV00 全部是只读（cluster writes=0）。计划安装新增：Kubeflow Namespace、Trainer/JobSet/Argo CRD 与各唯一控制器/webhook、KFP 后端/专属数据库与 Secret/入口证书、两环境探针 Namespace 及受限身份、Runtime、quota/admission/network policies。共享组件只复用，若需替换/升级先列影响；不抢 field ownership，不 force-conflicts。

验收新增对象按 attempt 隔离：PVC、Workflow、Run、TrainJob/JobSet/Pod、S3 前缀/工件及数据库记录。先保留现场、捕获创建 UID；失败停止后续依赖写入。持久化只重启本轮独占数据库实例，停止只针对本轮 UID；不重建 ANI/共享业务 Pod。原始凭据、私有站点及大包放 Fedora 任务私有根，Git 仅脱敏记录。

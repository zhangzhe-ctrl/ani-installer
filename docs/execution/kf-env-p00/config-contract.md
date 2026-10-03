# ENV01 配置与首装合同草案

状态：IN_PROGRESS。以下是待实现并验证的合同，不是 CODE_READY 或 ENV_READY。

首装站点新增 `kubeflow` 节，省略或 `enabled: false` 保持既有选择与安装行为。启用只接受本轮固定 `release: 26.03-kfp2.16-trainer2.1-v1`，与 `profile: full` 配合；不接受未知 release、GPU runtime、base profile 或隐式缺依赖。产品名称仍为 Kubeflow。部署角色只在 `create_cluster` 的所选基础组件尾部接入；不把角色加入 `ani_components.yaml`。

固定源与版本：KFP 2.16.0、SDK/driver/launcher 同版、Argo v3.7.3、Trainer v2.1.0、JobSet v0.10.1。传统 MySQL 模式，API/cache/MLMD 使用专属库与账号、受管 Secret、持久 CSI 卷；不共用 ANI 业务表，不改 PostgreSQL 技术栈。精确镜像由实际上游渲染与动态源码引用枚举，未知 digest 禁止安装。数据库镜像的上游 `mysql:8.4` 必须落为固定补丁版/摘要并记录差异，不能用浮动 minor tag 交付。

## 认证和入口

优先固定成熟 TLS 代理与 KFP 原生 TokenReview，避免开发新 Go 代理。候选机制：受限客户端携带 audience 为 `pipelines.kubeflow.org` 的短期 Kubernetes SA Bearer token；KFP TokenReview 验证身份并继续 Namespace SAR，`MULTIUSER=true`、`MULTIUSER_SHARED_READ=false`。环境探针 SA 只获 A 或 B 的 KFP 权限，后续 CPU02 再绑定正式服务身份。

固定 KFP 源 `auth.GetAuthenticators` 的首个认证器是 HTTP 身份头，随后才是 TokenReview。本轮必须在配置及真实协议上关闭客户端可声明的头身份，入口拒绝/剥离常见身份头及变体；不能仅依赖 NetworkPolicy。HTTP 与 gRPC 都必须检查无凭据、无效/错误 audience、重复 Authorization、重复/大小写/下划线身份头、A→B。尚未证明空 `KUBEFLOW_USERID_HEADER` 能可靠关闭首个认证器，不能提前记 PASS；需运行固定源码测试和真实请求。

原始 API HTTP/gRPC 端口只允许本期控制面必要调用和入口 Pod；训练 Pod 无控制面证书、管理员 S3 凭据、默认 SA token 或任意 Kubernetes 创建权。MLMD/MySQL 只允许已列明内部消费者；受限启动/driver 的必要访问单列，不能给全部 Namespace 放通。

本轮默认无 frontend、Istio、Dex、oauth2-proxy、Profile/KFAM、Dashboard、Notebook、Katib、TensorBoard、Hub、Knative、LWS、GPU/HAMi。KFP pipelines-profile-controller 的租户初始化由受管清单补齐：SA/RBAC、kfp-launcher、元数据与存储引用、配额/AdmissionPolicy/NetworkPolicy。其他内部对象先枚举说明用途，不凭名字删减。

## Runtime 与工作区

一个版本化 Runtime，当前为单节点单进程 CPU，固定镜像和 entrypoint、Never restart、JobSet 有界失败策略；不批量开放 upstream CUDA runtime。AdmissionPolicy 拒绝多节点/GPU/未获准 Runtime 和任意覆盖存储的请求。

固定本轮工作区为 `managed-execution-pvc-v1`：环境 probe 程序为单执行 create-only 创建独立 PVC，捕获响应 UID，无 Workflow ownerReference，显式把 claim 名称/UID 传到外部 TrainJob。人不逐 Run 建卷；这不是 modeldev 的正式工作区 owner。需真实验证只读输入/独立输出、跨 Pod/必要跨节点交接、两执行隔离及失败/停止保留。

选择依据来自固定 KFP 源：`GetWorkspacePVC` 会应用用户 PVC patch，再用用户 size 覆盖默认 requests；原生卷由 Argo Workflow 管理。`backend/src/apiserver/resource/resource_manager.go:ReportWorkflowResource` 会删除已持久终态的 Workflow，持久化 agent 默认一天后再次上报；该生命周期不能保证发布未成的唯一文件继续保留。不能把 agent TTL 设置为 0 当作关闭 GC，0 会立即越过 TTL。故安装前固定上述独立卷方案，保留上游 Workflow GC，不运行失败后切换。原生 Workspace 的运行行为仍为 NOT_RUN，不声称实机复现。管理员以 admission 和 ResourceQuota 强制限定 StorageClass、容量、卷数量与 owner 策略。

## 初始容量核算（尚未冻结）

现有 23 个 PVC 声明合计 100 GiB；现场 raw 600 GiB、Ceph 三副本。按完整基础选择保留相同需求，候选 KFP MySQL 20 GiB，加两租户合计最多四个 5 GiB 执行卷（含失败保留），逻辑声明 140 GiB，三副本 420 GiB；另预留 raw 20%（120 GiB），剩余约 60 GiB raw。该预算不含无限保留、新增未声明外部负载或磁盘快照增长，正式冻结前按完整站点和每项 PVC 重算。不能通过降副本或减原批准容量凑通过。

三节点总 allocatable CPU 46.8、memory 约 88 GiB，当前 metrics 使用约 2.34 CPU/21 GiB。实际请求、init 最大值、DaemonSet 副本、可调度容量与本轮控制器/数据库/Runtime/两执行共存预算仍需冻结，不用低实时使用量代替请求预算。

## 写入与复验边界

ENV00 全部是只读（cluster writes=0）。计划安装新增：Kubeflow Namespace、Trainer/JobSet/Argo CRD 与各唯一控制器/webhook、KFP 后端/专属数据库与 Secret/入口证书、两环境探针 Namespace 及受限身份、Runtime、quota/admission/network policies。共享组件只复用，若需替换/升级先列影响；不抢 field ownership，不 force-conflicts。

验收新增对象按 attempt 隔离：PVC、Workflow、Run、TrainJob/JobSet/Pod、S3 前缀/工件及数据库记录。先保留现场、捕获创建 UID；失败停止后续依赖写入。持久化只重启本轮独占数据库实例，停止只针对本轮 UID；不重建 ANI/共享业务 Pod。原始凭据、私有站点及大包放 Fedora 任务私有根，Git 仅脱敏记录。

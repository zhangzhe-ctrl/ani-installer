# Kubeflow 二阶段统一首装

本轮目标来自 2026-10-08 的 `goal-objective.md`：在同一次 `kk ani install` 中交付固定组合 `26.03-kubeflow-stage2-v1`，包含既有 KFP 2.16.0、Trainer 2.1.0、JobSet 0.10.1，以及 Notebook Controller 1.10.0、JupyterLab、KServe 0.16.0 Standard 和一个 CPU sklearn Runtime。

主链：Notebook CR → 原生 Jupyter kernel → 固定模型 → 受限 S3 对象 → KServe 加载同一对象 → 固定输入的正确预测。随后验证停止/恢复后的 PVC 持久化、预测 Pod 重建、错误路径/凭据、基础保护和受影响第一阶段回归，再冻结候选，进行干净统一首装。

当前状态：保留的实验集群上真实主链、计划内持久化、失败、基础保护及第一阶段回归均通过；正在冻结候选，干净完整二阶段统一首装 `NOT_RUN`，尚未达到 `ENV_READY`。第一阶段冻结报告保持原含义，不作为本轮环境验收。

## 基线与工作路径

- 原工作区：`/home/chabking/workspace/ani-installer`，原分支 `release`，核实时干净；另有既存聊天引用该目录，因此采用独立工作树。
- origin：`ssh://git@ssh.github.com:443/zhangzhe-ctrl/ani-installer.git`。
- 2026-10-08T13:42:49Z 开始执行 `git fetch origin refs/heads/release:refs/remotes/origin/release`，直接退出码 0；更新后引用与 fetch 结果一致。
- BASE_SHA：`cf54a5c129e6a7d003ef1e148ff85fc23fd40a37`。
- 任务分支：`codex/kubeflow-stage2`。
- 独立工作树：`/home/chabking/.codex/worktrees/kubeflow-stage2/ani-installer`。
- Fedora：`ssh fedora` → `chabking@172.16.101.31`；已核实 hostname `fedora`、Fedora 44、Go 1.26.7、Python、Podman、Skopeo。本机只编辑/读取和操作 Git；生成、格式化、构建、测试与物料准备在 Fedora。
- Fedora 任务根：`/home/chabking/ani-installer-runs/kf-env-stage2-20261008`，仅使用本任务目录。

## 当前环境与断点

目标仅为 `.20`～`.22`。用户确认已重装这三台可信实验 VM；新密钥只保存到任务私有 known_hosts，严格 SSH 登录通过。ESXi 的当前 guest IP 与 BIOS UUID 对应已核实，Fedora 任务根记录为 `vm-identities-initial.json`；磁盘和快照映射为 `vm-disk-identities-initial-attempt-02.json`。首装前均为 Ubuntu 24.04.4、8 vCPU/16GiB，无 K8s；各自稳定 by-path 数据设备指向空白 200GiB sdb，实际正式 Ceph 只读预检退出码均 0。未还原快照。

基础安装代码绑定已推送 `b74640caacc6609044bd1efbc5f2962e881e42bc`，Fedora 正式 `build-code.sh` 退出码 0；源码指纹 `bc6174e1272f66c267d083eecf6c9f5d2545f8ffda3fe6ff1b1fba410ac73be9` 在门禁、编译与打包间一致。首错是测试生成 pycache 导致发布保护拒绝；保留首错后用禁止字节码写入的同源重跑通过。基础物料重新核验后，`kk ani install` 于 2026-10-08T14:53:14Z 直接退出 0，run 为 `ani-ani-kf-stage2-20261008-143528`，集群 UID 为 `bc286cf0-db38-4e03-b3d4-9645657a1f09`。基础准备时关闭 Kubeflow，不作为完整二阶段统一首装验收。CephFS/RBD、内部 CA、RustFS 已就绪；Ceph 20.2.4 存在密钥类型兼容性 HEALTH_WARN，未修改共享组件配置来隐藏告警。

联调物料 `materials-integration-f44ed3e-attempt-01` 已通过 112 镜像实际内容门禁，启用完整站点渲染退出码 0。为续用基础环境，联调仅在三台节点预加载同摘要的 8 个新增镜像；正式干净首装仍使用完整包和正常 registry 拉取。首次真实角色部署停在 KServe：Helm 省略三个资源的 namespace，导致它们落到 default，控制器无法创建 Pod。保存 FailedCreate 与原始写入 UID 后中断当前安装器（直接退出 130）；仅以 UID/resourceVersion 条件删除这三个本任务误置资源。修复显式 namespace 与查找前作用域保护，续用原集群，不恢复快照。证据均在上述 Fedora 任务根，分别为 `integration-first-error-kserve-namespace.json`、`integration-install-attempt-01.json` 和 `namespace-recovery-attempt-01.log`。

当前 Jupyter 镜像 `sha256:8a8d9a3eb0f4ed63702e873ba9658c49fa2e147c8f927a3fba196768f5d8e712` 在 Fedora 从已推送 `912748731c931946c5d2b3fc9d943188ec950be0` 构建并核验镜像层，100 个 wheel 闭合；修复原启动日志级别后，原 Notebook/PVC UID 保留。原生 Jupyter REST 保存和读取 ipynb、真实 kernel/WebSocket 执行生成 joblib 模型，上传 S3；独立只读身份校验同一对象摘要。KServe 私有 CA 来源和文件名映射修复后，原 InferenceService UID 保留，Standard 预测返回 `[0,1,2]`。

主链联调源码 `b6476f4bbddc9cf6e51988c90ed64485d1d0fd6e`，节点记录 `/var/lib/ani-installer/ani-kf-stage2/logs/stage2-main-integration-attempt-06/report.json`，Fedora 索引 `stage2-main-integration-attempt-06.json`，直接退出码 0。Notebook UID `0a86e876-13ac-48fa-bf92-9a5b3494cbb1`，PVC UID `29119d0f-76e5-4fc5-9bfb-a3d04a34013a`，InferenceService UID `1c7803f2-21f0-43c4-9c75-e4d7fb3cfccb`。模型 SHA256 `4dcecdea7d2ffbd1c38ee4bfd54926bf723003da5559471eedd803aca9b9c292`，对象 `s3://ani-kf-stage2-ani-kf-stage2-a/models/lab-20261008-01/model.joblib`。这是联调证据，完整冻结物料必须替换先前 f44ed3e 包内的旧 Jupyter 摘要；不得把旧包或这份联调 PASS 提升为干净首装验收。

原生行为探针源码 `36367bd81b90199997aa0952bcc574b5e17402ce`，四项 `stage2-contract-<case>-attempt-01.json` 的直接退出码均 0：停止/恢复使 Notebook Pod UID 从 `255cff82-a09e-40a6-b821-81feb4bf4b34` 变为 `1c62692d-8ed0-41fb-82d8-9519489ee230`，原 PVC UID 与 ipynb、固定内容、模型和格式合同四个文件摘要一致；预测 Pod 按原 UID 删除一次后仍正确预测；错误路径与错误独立凭据均由 storage-initializer 退出 1，原模型继续正确预测。保护探针源码 `5e952f7e3cf202190b180a35126c442a0ac9126c`，`stage2-protection-attempt-01.json` 退出 0，验证两组独立环境模型身份的正向 HTTPS 和职责/前缀/跨 Namespace 拒绝、普通 Notebook/预测容器无 token、对健康控制端点的网络拒绝及 SA 创建授权拒绝。原始报告已导出到 Fedora `integration-evidence-export-attempt-01/index.json`。

第一阶段真实回归源码 `dd1b42949018aec8a9d40858e843dfd70d0eb60b`，`stage1-{success,fail,stop}-integration-attempt-02.json` 均退出 0。成功 Run `9a522465-d3e4-4ef7-b1ec-4f7f61bd0d6b` 对应 TrainJob/JobSet 完成；失败 Run `92c7ed89-5e82-4670-9818-5321e06116f2` 与训练 Pod 退出 42 关联；停止 Run `6b711ea3-4739-43f8-b903-5effca933e07` 的原 TrainJob 挂起、训练 Pod 消失、原生 KFP 停止确认，KFP 实际终态为 `FAILED`，不改写为 `CANCELED`。三张原执行卷经 node3 独立只读探针读回文件；原 admission、普通工作负载保护及 62 个 HTTP/gRPC 身份请求通过，见 `stage1-workspace-boundary-regression-attempt-01.json` 与 `stage1-entry-matrix-attempt-01.json`。

回归首错是旧编译 Pipeline 固定选择 `ani-01`，当前节点为 `node1`～`node3`；原 Run `1ce564b8-ea13-48ac-b1e2-f3e266446987` 和 PVC 保留，以 KFP 原生接口停止后在 Fedora 使用同一锁定 SDK、无网络重新编译为 node1，未改节点标签或恢复集群。完整候选 `dd1b429` 的首次发布门禁停在新增探针的 Go 测试夹具路径；补齐映射后的 `ff7c14e9d29d96ed0f6deced7562d67211ff706c` 正在执行正式全门禁。上述均为联调与构建事实，完整包、快照恢复和干净统一首装仍待独立核验。

完整站点示例为 `site.example.yaml`。私有站点从当前公开合同及既有受管 SSH 凭据生成，未继承 `.10`～`.12` 的私有配置、kubeconfig、入口或历史 PASS。

不安装 Notebooks WebApp、Dashboard、Profiles/KFAM、PodDefaults、Dex、oauth2-proxy、Istio、Knative、LWS 或 GPU。管理前端、BFF/modeldev、产品租户、IAM/业务网关均属于后续接入。

本目录是本轮唯一恢复与证据入口；最终交接写入 `records/environment-handoff.json`。

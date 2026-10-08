# Kubeflow 二阶段统一首装

本轮目标来自 2026-10-08 的 `goal-objective.md`：在同一次 `kk ani install` 中交付固定组合 `26.03-kubeflow-stage2-v1`，包含既有 KFP 2.16.0、Trainer 2.1.0、JobSet 0.10.1，以及 Notebook Controller 1.10.0、JupyterLab、KServe 0.16.0 Standard 和一个 CPU sklearn Runtime。

主链：Notebook CR → 原生 Jupyter kernel → 固定模型 → 受限 S3 对象 → KServe 加载同一对象 → 固定输入的正确预测。随后验证停止/恢复后的 PVC 持久化、预测 Pod 重建、错误路径/凭据、基础保护和受影响第一阶段回归，再冻结候选，进行干净统一首装。

当前状态：实施中，主链与本轮首装均 `NOT_RUN`。第一阶段冻结报告保持原含义，不作为本轮环境验收。

## 基线与工作路径

- 原工作区：`/home/chabking/workspace/ani-installer`，原分支 `release`，核实时干净；另有既存聊天引用该目录，因此采用独立工作树。
- origin：`ssh://git@ssh.github.com:443/zhangzhe-ctrl/ani-installer.git`。
- 2026-10-08T13:42:49Z 开始执行 `git fetch origin refs/heads/release:refs/remotes/origin/release`，直接退出码 0；更新后引用与 fetch 结果一致。
- BASE_SHA：`cf54a5c129e6a7d003ef1e148ff85fc23fd40a37`。
- 任务分支：`codex/kubeflow-stage2`。
- 独立工作树：`/home/chabking/.codex/worktrees/kubeflow-stage2/ani-installer`。
- Fedora：`ssh fedora` → `chabking@172.16.101.31`；已核实 hostname `fedora`、Fedora 44、Go 1.26.7、Python、Podman、Skopeo。本机只编辑/读取和操作 Git；生成、格式化、构建、测试与物料准备在 Fedora。
- Fedora 任务根：`/home/chabking/ani-installer-runs/kf-env-stage2-20261008`，仅使用本任务目录。

## 环境核实中的断点

目标仅为 `.20`～`.22`。Fedora 以严格主机密钥校验连接三个地址均返回 255，受管 known_hosts 与当前返回的密钥不同；尚未确认 VM/集群/快照身份。未写目标节点，未还原快照。待可信身份与访问配置核实后再推进环境动作；不复用 `.10`～`.12` 的 kubeconfig、私有站点配置或历史 PASS。

不安装 Notebooks WebApp、Dashboard、Profiles/KFAM、PodDefaults、Dex、oauth2-proxy、Istio、Knative、LWS 或 GPU。管理前端、BFF/modeldev、产品租户、IAM/业务网关均属于后续接入。

本目录是本轮唯一恢复与证据入口；最终交接写入 `records/environment-handoff.json`。

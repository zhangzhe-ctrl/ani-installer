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

## 当前环境与断点

目标仅为 `.20`～`.22`。用户确认已重装这三台可信实验 VM；新密钥只保存到任务私有 known_hosts，严格 SSH 登录通过。ESXi 的当前 guest IP 与 BIOS UUID 对应已核实，见 `vm-identities.json`；磁盘和快照映射见 `vm-disks.json`。三台均为 Ubuntu 24.04.4、8 vCPU/16GiB，无 K8s；各自稳定 by-path 数据设备指向空白 200GiB sdb，实际正式 Ceph 只读预检退出码均 0。未还原快照。

基础安装代码绑定已推送 `b74640caacc6609044bd1efbc5f2962e881e42bc`，Fedora 正式 `build-code.sh` 退出码 0；源码指纹 `bc6174e1272f66c267d083eecf6c9f5d2545f8ffda3fe6ff1b1fba410ac73be9` 在门禁、编译与打包间一致。首错是测试生成 pycache 导致发布保护拒绝；保留首错后用禁止字节码写入的同源重跑通过。基础物料正在从既有锁定字节重新打包、实际 registry 内容门禁核验；基础集群尚未安装。

新增 Jupyter 镜像 `sha256:231029b0c72de5c4fecd2ef899f1948c980ac51b7e65fe3cf627bb28f8aafb62` 在 Fedora 从已推送 `ae5ce46b0da47ed9859a22c2a6d993861772a02c` 构建并核验镜像层，100 个 wheel 全部闭合。固定模型生成验证通过，不等于原生 kernel 或集群 PASS。改动对应 Kubeflow Go 测试与 Python 运行保护测试在 `b94adef` 退出码均 0。主链探针已编写，真实链路仍 `NOT_RUN`；后续先运行主链再补行为约束。

完整站点示例为 `site.example.yaml`。私有站点从当前公开合同及既有受管 SSH 凭据生成，未继承 `.10`～`.12` 的私有配置、kubeconfig、入口或历史 PASS。

不安装 Notebooks WebApp、Dashboard、Profiles/KFAM、PodDefaults、Dex、oauth2-proxy、Istio、Knative、LWS 或 GPU。管理前端、BFF/modeldev、产品租户、IAM/业务网关均属于后续接入。

本目录是本轮唯一恢复与证据入口；最终交接写入 `records/environment-handoff.json`。

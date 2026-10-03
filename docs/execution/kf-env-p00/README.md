# Kubeflow 统一首装：ENV00～ENV06

本轮范围来自用户提供的 `goal-objective.md`（2026-10-03），优先于旧 B12 和任务包中冲突的约束。只交付首装：站点显式启用后，同一次 `kk ani install` 执行基础集群、所选基础组件、KFP 与 Trainer。默认关闭；不扩大安装后的 components add/execute 功能。CPU 是当前验收 Runtime，不是产品名称或 GPU 已验证声明。

任务源：`/home/chabking/workspace/ANI-doc/02-issues/modeldev/kf-env-p00`，只读。交付记录写入本目录；不修改任务源模板或历史报告。CPU-P01 只读依赖来自同级 `cpu-p01`。

## 源码与执行边界

- 原分支 `main`，干净基线 `78d8418307b6156954feb10aee8d6972d2ff1bbd`。
- 任务分支 `feat/kubeflow-cpu-p00`；工作树 `/home/chabking/workspace/ani-installer`。
- remote `https://github.com/zhangzhe-ctrl/ani-installer.git`。
- 本地只读写源码、文档及 Git；生成、格式化、渲染、构建和测试在已核实的 `ssh fedora`。
- 任务专属远程根 `/home/chabking/ani-installer-runs/kf-env-p00-20261003`；使用其 `tmp`/`cache`，不清他人缓存。
- 目标访问使用现有 `ani-test-1/2/3` SSH 身份；固定 installerNode 上 `/etc/kubernetes/admin.conf`，不回落其他 context。

## 当前检查点

ENV00 调查进行中，ENV01～ENV06 未完成。没有安装或恢复快照，没有设置 `ENV_READY`。

已实际核实：三节点均 Ubuntu 24.04.4 amd64、Kubernetes v1.35.8、containerd 2.3.4。API 为 `https://lb.kubesphere.local:6443`；context 为 `kubernetes-admin@ani-lab`。集群标识采用 kube-system Namespace UID `87ecef8e-ac4e-442b-8e15-5e906263be6b`。完整脱敏清单见 `records/cluster-record.json`。

三节点各 15.6 CPU allocatable、约 29.3 GiB allocatable memory；每节点已有 200 GiB Ceph 数据盘，600 GiB raw 总量。现有 `ani-block`、`ani-cephfs` 和 23 个 Bound PVC 不代表本轮工作卷已通过实际写读。现有 Ceph 20.2.4 为 HEALTH_WARN，含四个 CSI 客户端不安全密钥类型、允许/可创建旧密钥及一项 BlueStore slow-op 告警，不静音、不轮换共享身份。

Kube-OVN v1.16.6 运行参数开启 `--enable-np=true`、`--np-enforcement=standard`；7 项现有 NetworkPolicy 不是本轮防旁路验收。CoreDNS Service 为 `coredns`（10.96.0.3），已有两个 EndpointSlice endpoint。KFP、Argo、Trainer、JobSet 的相关 CRD 未发现；其缺失是本任务安装前提。现有 cert-manager v1.21.2、RustFS 1.0.0、PostgreSQL 17.11、Harbor 和 ANI 服务须保持所有权边界。

时间基线是 installer 既有离线主从：ani-01 `NTPSynchronized=no`，chrony 无外部源、local stratum 10；ani-02/03 实际同步 ani-01、stratum 11，观测偏差分别约 29/17 微秒。该布局与当前 `KubeKeyConfig.native.ntp` 一致，不将未接外部时间源误判成两从节点未同步；UTC 外部可靠性仍未验证，Ceph 节点间同步继续核实。证书到期检查显示集群证书有效至 2027-09-29。

ESXi 已通过既有受管凭据、严格主机密钥验证只读核实：VMID 8/9/10 分别为 ani-01/02/03，guest IP 与本轮目标一致。三个 `iso-install-complete` snapshot Id2 存在，每台两块 200 GiB persistent 磁盘均被快照覆盖；Id3 是旧 kcn/RGW 环境，不能作最终干净快照。见 `records/lab-vm-inventory.txt`。用户已明确当前实验数据无需保留、直接还原；恢复尚未执行，仍须先完成联调和候选冻结。旧恢复脚本有 .20～.22 白名单及额外 power.on，本轮不运行其恢复入口、不放宽白名单。

## 后续次序

先完成当前基础能力与容量、远程构建和数据边界调查，固定配置/版本/物料合同。保留现有联调环境，完成 ENV02～ENV05 真实联调。通过后才冻结候选和正式发布物，随后还原核实的干净快照，完成 ENV06 一次统一首装与独立 EAC01～EAC14。最终首装不继承联调 PASS。

源码上游已在 Fedora 固定：KFP tag 2.16.0 → `e4ebca310f404dac306e16bc880de12bbbf63b95`；Trainer tag v2.1.0 → `73c9bece741ce17d2124abda3ec6bfcf5b5b8e87`。Trainer 源锁定 JobSet v0.10.1；KFP Argo 源引用 v3.7.3。14 个必需上游镜像与一个已远程构建的 SDK/CPU 执行镜像已登记平台摘要；旧 V1 cache 两镜像仅留研究证据，不交付。完整包、registry readback 和运行时拉取仍未完成。

ENV01 已接入默认关闭配置、自动证书依赖、首装末尾角色、物料条目和生产渲染。Fedora 定向门禁 `9c0a757` rc=0，Python 部署模块语法检查 rc=0。完整门禁首轮 `b7d418d` 因上下文守卫缺登记失败，随后 `207f1c4` 因容量 Quantity 调用编译失败；两轮日志保留，修复后定向通过，但最终候选全门禁仍未执行。部署/check 脚本是未安装的实现草稿，不能标记 CODE_READY 或 ENV_READY。

[官方 GHSA-gqww-5pj5-8fq7](https://github.com/kubeflow/pipelines/security/advisories/GHSA-gqww-5pj5-8fq7) 已核对：frontend ≤2.16.0 受影响。本轮不部署 frontend，不通过其代理形成可信调用链。

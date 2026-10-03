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

ENV00～ENV06 已完成，状态 `ENV_READY`。候选源码 `dee542af0122e9f95bded2f349849df3a1a53e4a` 在三台已授权还原 Id2 的干净主机上完成统一首装，实际退出码 0；首装 runId `ani-ani-lab-20261003-162500`。随后新环境的 15 项产品 smoke 和独立 EAC01～EAC14 均 PASS。最终集群 UID 为 `5277649d-e28d-4a0a-ae76-8354365c63bc`，未继承联调 PASS。

联调基线：三节点均 Ubuntu 24.04.4 amd64、Kubernetes v1.35.8、containerd 2.3.4。API 为 `https://lb.kubesphere.local:6443`；context 为 `kubernetes-admin@ani-lab`。旧 kube-system Namespace UID `87ecef8e-ac4e-442b-8e15-5e906263be6b` 仅属于已还原的联调环境。历史清单见 `records/cluster-record.json`，当前首装前主机身份见 `records/clean-first-install-start.json`。

容量与时间历史调查见 `records/capacity-and-time.txt`；联调期间已验证实际 PVC 写读、跨节点交接和普通工作负载访问拒绝。旧 Ceph 与时间告警保留在历史记录中，最终环境重新观察，不沿用旧健康结论。

ESXi 已通过既有受管凭据、严格主机密钥验证核实：VMID 8/9/10 分别为 ani-01/02/03，guest IP 与本轮目标一致。三个 `iso-install-complete` snapshot Id2 均已直接还原，每台两块 200 GiB persistent 磁盘均被快照覆盖；见 `records/clean-first-install-start.json`。快照恢复仅属于本轮实验室准备，不进入安装包和正常产品流程。

## 统一首装入口

在安装前将 [Kubeflow 显式选项](kubeflow-site-option.yaml) 合入完整站点配置；该片段不能独立执行。主机、存储、registry 和所选基础组件按站点完整声明。字段与身份合同见 [config-contract.md](config-contract.md)。配置关闭时保持原安装行为。

以下绝对路径为本轮正式制品和私有配置，不是通用客户默认路径：

```sh
sudo /home/ubuntu/ani-kubeflow-first-install-dee542a-20261004/code-frozen-dee542a-attempt-01/kk ani install \
  --config /home/ubuntu/ani-kubeflow-first-install-dee542a-20261004/site.private.yaml \
  --package-root /home/ubuntu/ani-kubeflow-first-install-dee542a-20261004/materials-frozen-dee542a-attempt-01
```

本轮已执行该命令并成功。当前正式安装记录是 `/var/lib/ani-installer/ani-lab/run.json`，对应 `run-state.json` 为 `succeeded`；后续核验消费这个安装记录。首装失败时保留现场，定位后回到源码与正式发布链修复。

冻结门禁、完整材料及目标摘要见 [candidate-freeze.json](records/candidate-freeze.json)：同源远程门禁/代码包/材料包/生产渲染均 rc=0，121 个角色文件，104 个镜像实际字节检查。正式 registry 为 `172.16.101.10:5000`；旧联调的 5001 仅属于历史开发环境。

联调和最终复验分别覆盖：真实 Pipeline→TrainJob→工作卷→工件成功链、实际退出 42、停止后无替代 Pod、专用 MySQL 容器重启后相同记录与新执行、两执行互不覆盖、S3 12 项明确 403、62 项 HTTP/gRPC 身份请求，以及普通无凭据 Pod 到 10 个控制目标和 4 个 Kubernetes 创建目标的拒绝。最终 18 个唯一文件已在节点外按原始字节、大小和 SHA256 校验保存。

结果见 [final-eac-summary.json](records/final-eac-summary.json)、[environment-handoff.json](records/environment-handoff.json) 和 [final-file-index.json](records/final-file-index.json)。原始记录与大包在 Fedora 任务专属目录；最终原始归档 SHA256 为 `0594bff8452e0cb5f1b5bf839c503d1184ab1babf7cda1f5e0ba32426aef3c09`。联调与首错历史见 [integration-attempts.md](records/integration-attempts.md)。

Ceph 当前保留三项认证配置 HEALTH_WARN；离线主时钟使用 local stratum 10，两从节点同步它，外部 UTC 源仍 NOT_VERIFIED。未静音告警或轮换密钥。当前 28 个 PVC 声明总计 140Gi，三副本上界 420Gi；raw 600Gi，另留 120Gi 余量。四个执行卷均保留，两探针租户已达到每租户两个卷的上限，清理须按实际 UID 和输出保留决定推进。

交 CPU-P01：CPU00 复核最终身份与正式记录；CPU02 绑定并复验真实服务身份。环境探针 SA 不作 modeldev 正式授权。CPU-P01/BFF 业务、GPU 和最大并发吞吐均未验收。

[官方 GHSA-gqww-5pj5-8fq7](https://github.com/kubeflow/pipelines/security/advisories/GHSA-gqww-5pj5-8fq7) 已核对：frontend ≤2.16.0 受影响。本轮不部署 frontend，不通过其代理形成可信调用链。

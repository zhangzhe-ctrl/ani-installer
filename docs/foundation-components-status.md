# 基础组件第一轮：分批状态与证据

执行依据：[`foundation-components-batch-execution-plan-20260918.md`](foundation-components-batch-execution-plan-20260918.md)
材料锁：[`../kubekey/ani/components.lock.yaml`](../kubekey/ani/components.lock.yaml)
隔离工作目录（Fedora）：`/home/chabking/ani-installer-runs/foundation-20260918/`
实验锁：`/home/chabking/ani-installer-runs/locks/cluster-20-22.lock`

本文件每批更新一次。**未启用、未执行、因环境限制未测的项目必须明确区分**，不得合成一个含糊的“测试通过”。

---

## 批次总览

| 批次 | 内容 | 状态 | 完成时间 |
| --- | --- | --- | --- |
| 底座（r12） | K8s v1.35.8 / containerd v2.3.4 / runc v1.4.3 / Hauler v2.0.3 / kcn v0.6.2 / Envoy / Rook v1.20.7 + Ceph v20.2.4 | user_reported_pass | 用户反馈（2026-09-18） |
| B0 | 固定输入与材料、容量核对、材料锁 | pass | 2026-09-18 |
| B1 | 最小组件开关 + cert-manager | pass | 2026-09-18 |
| B2 | PostgreSQL | pending | — |
| B3 | Valkey | pending | — |
| B4 | NATS JetStream | pending | — |
| B5 | 最终交付与人工复现入口 | pending | — |

状态取值：`pending` / `in_progress` / `pass` / `fail` / `blocked` / `user_reported_pass`

---

## 底座（历史，非本轮重新验证）

`user_reported_pass`：用户反馈 r12 手动安装验证通过。本轮不重复重装底座，也不升级底座组件。
该状态是用户验收记录，不代表本文件作者重新执行了一轮验证。

---

## B0：固定输入与材料

**状态：pass**

### B0-1 输入核对

| 项目 | 位置 | 结果 |
| --- | --- | --- |
| 当前源码快照 | Fedora `foundation-20260918/inputs/src-20260918-b0.tar.gz` | sha256 `d1d639ffa02b903e90c1f073b57ab9dcf652d96252fba2f2502122f048de748c` |
| 源码树摘要（`find -type f \| sha256sum \| sha256sum`） | 同上解压后 `foundation-20260918/src` | `bb6cae2cd878e42aa347a4edbe182351bf84e28834095e33640865dda2f22927` |
| 本地 Git HEAD | `92b2bf7 Fix KubeKey kubeconfig export and isolated APT installs` | 工作区干净，无未提交改动 |
| r12 代码包 | `fix-kubeconfig-20260918/ani-code-kubeconfig-r12` | kk sha256 `288574d20507a58c3431b9fbb8d0fa41bc73493663092af080020ce1caa84dbd` |
| r2 artifact | `platform-20260918/run/ani-artifact-ubuntu24-amd64-20260918-r2` | SHA256SUMS 内 10 个文件全部保留作为本轮累计 artifact 基线 |
| 站点配置（参考结构） | `recheck-20260918/cluster.yaml` | sha256 `ac1e8c362b3a47f490735a2b4d311959f2b80ecf1615f958ec055289d476c190` |
| 节点执行器 | `ani-ops/run_on_node.sh` | 可用（非可执行位，需 `bash` 显式调用） |
| 断网工具 | `ani-ops/apply_offline_isolation.sh` | 可用 |
| 快照工具 | `recheck-20260918/restore_esxi_snapshots.sh` | 可用，dry-run 尚未执行（首次在进入安装阶段前执行） |

说明：执行计划文本中写的本地源码目录 `/home/chabking/workspace/ani-installer` 在 Fedora 上**不存在**；
实际源码位于本地工作区 `D:\Workspace\ani-installer`，已按“只把明确的当前源码快照作为后续构建输入”的要求打包传到 Fedora。

### B0-2 三台目标机容量与网络条件（干净快照状态）

| 项目 | node1 `172.16.101.20` | node2 `172.16.101.21` | node3 `172.16.101.22` |
| --- | --- | --- | --- |
| 主机名 | test-installer-01 | test-installer-02 | test-installer-03 |
| CPU | 4 | 4 | 4 |
| 内存 | 7.8Gi（可用 7.2Gi） | 7.8Gi（可用 7.2Gi） | 7.8Gi（可用 7.2Gi） |
| 根盘可用 | 39G | 39G | 39G |
| `/dev/sdb` | 50G，无分区无文件系统（空盘） | 同 | 同 |
| ens34 | 172.16.101.20/24 UP | .21/24 UP | .22/24 UP |
| ens35 | DOWN，无主机 IP（给 kcn） | 同 | 同 |
| ens36 | DOWN（本轮不配置） | 同 | 同 |
| `/etc/kubernetes/admin.conf` | 不存在 | 不存在 | 不存在 |
| `/var/lib/ani-installer` | 不存在 | 不存在 | 不存在 |

互斥检查：三台无正在进行的 kk / kubeadm / apt / 安装进程；`.20` 只有一个历史 SSH 会话残留（`who` 显示 pts/0）。

### B0-3 材料核对（Fedora 实际下载/读取，非路径名代替内容校验）

Chart tgz 摘要与执行计划中记录的值完全一致：

| Chart | 来源 | SHA256 | 核对 |
| --- | --- | --- | --- |
| cert-manager v1.21.2 | `https://charts.jetstack.io/charts/cert-manager-v1.21.2.tgz` | `73a56e1728edd6c99f1f31082618c3259d279a76b7ebd3d4bdc5475c2442d34a` | 一致 |
| nats 2.14.6 | `https://github.com/nats-io/k8s/releases/download/nats-2.14.6/nats-2.14.6.tgz` | `72f7412d6856a6c75c80d4aa4a1ed88c3846f5728fa8e0cc134a76da0febe3d9` | 一致 |

cert-manager Chart 实际内容核对（`helm template` 实际渲染，非推测）：

- `Chart.yaml`：`appVersion: v1.21.2`，`version: v1.21.2`（两者一致，但分别记录）。
- `crds.enabled` **默认 `false`**，必须由本轮 values 显式置 `true` —— 执行计划“已核对本版本字段 crds.enabled: true”理解为我们要设置的值，与 Chart 默认值不同，此处纠正记录。
- 渲染出的常驻 Deployment：controller / webhook / cainjector；hook Job：startupapicheck。
- controller 实际默认参数包含 `--acme-http01-solver-image=quay.io/jetstack/cert-manager-acmesolver:v1.21.2`，
  因此 acmesolver 虽然不产生 Pod，仍按执行计划要求纳入离线包（不启用 ACME）。
- `--cluster-resource-namespace=$(POD_NAMESPACE)`，需确保根 Secret 位于 cert-manager namespace。

NATS Chart 实际内容核对（用执行计划给出的 values 渲染通过）：

- `Chart.yaml`：`version: 2.14.6`，`appVersion: 2.14.6`。
- 渲染结果：单个 `StatefulSet/nats`，PVC `nats-js`（5Gi，`storageClassName: ani-block`），Service `nats` / `nats-headless`。
- 渲染出的镜像只有 `nats:2.14.6-alpine` 与 `natsio/nats-server-config-reloader:0.23.0`。
- `promExporter.enabled` 与 `natsBox.enabled` 默认均为 `false`，本轮保持关闭。

镜像摘要（10 张 + 1 张证书验证工具镜像）已全部写入 `components.lock.yaml`，
区分源 index 摘要与 linux/amd64 manifest 摘要，两者的确不相等。

证书验证工具：`docker.io/alpine/openssl:3.5.4`，容器内实际命令为 `/usr/bin/openssl`，
Fedora 上实测输出 `OpenSSL 3.5.4 30 Sep 2025`。目标机不安装 openssl，验证 Job 直接引用离线包内镜像。

Helm：本轮 chart 渲染需要 helm 二进制。复用既有固定版本 **v3.20.0**
（来源 tarball sha256 `dbb4c8fc8e19d159d1a63dda8db655f9ffa4aac1b9a6b188b34a40957119b286`，
展开后二进制 sha256 `1f7ed083dbc200a10fdfe04df94c21530140fdc955de5a1daed2694150f77b17`），
随 artifact 以 `bin/helm` 供应，不升级，不在目标机联网获取。

### B0-4 预计资源占用（普通清单，不实现容量规划器）

| 资源 | 组件 | 预计 | 备注 |
| --- | --- | --- | --- |
| PVC（RBD `ani-block`，replicas=3） | PostgreSQL | 10Gi | 默认；可用时保持，必要时在批次记录中调整实验值 |
| PVC | Valkey | 2Gi | 同上 |
| PVC | NATS JetStream fileStore | 5Gi | 同上 |
| PVC 合计 | — | 17Gi → 原始约 51Gi | Ceph 原始容量 3×50Gi=150Gi，副本 3 下可用约 50Gi，四组件合计在预算内；实际可用空间在 B1 安装后由 `ceph df` 复核 |
| 新增常驻 Pod | cert-manager | 3 | controller / webhook / cainjector |
| 新增常驻 Pod | PostgreSQL / Valkey / NATS | 各 1 | 单副本 |
| 一次性 Job | startupapicheck、各组件验证 Job | 一次性 | 验证完成后由本次唯一名称/标签定位清理；失败时保留 |
| 根盘新增 | 镜像拉取 | 约 400MiB | 三台根盘各剩 39G，充足 |

### B0-5 产出

- `../kubekey/ani/components.lock.yaml`：四组件 + 工具镜像 + helm 的材料锁，附 Chart 实际渲染事实。
- `../config/examples/foundation-b1.yaml` … `foundation-b4.yaml`：累计启用的脱敏示例，密码为占位符。
- 本文件。

### B0 完成标准核对

- [x] 材料和配置有确定来源（官方 URL + SHA256 + 实际渲染结果）
- [x] 当前入口可继续复用（r12 代码包入口 `install.sh CONFIG ARTIFACT_ROOT`、verify.sh、probe.sh 均在）
- [x] 没有未解释的底座版本变化（未<｜hy_place▁holder▁no▁813｜>升级；发现的唯一差异是 cert-manager `crds.enabled` 默认值，已记录）
- [x] 不需要为 B0 重装底座
- [x] 未提前下载非本轮组件材料

---

## B1：最小组件开关 + cert-manager

**状态：pass**（attempt a6；干净断网累计安装通过，普通 `ubuntu` 入口 `INSTALL_EXIT=0`）

### B1-0 批次事实

| 项 | 值 |
| --- | --- |
| 批次 / attempt | B1 / a6（a1–a5 为 installer 缺陷迭代，源码修复后于 a6 通过） |
| 源码快照 | Fedora `foundation-20260918/src`（B0 快照 `d1d639ff…` 之上叠加 B1 改动，本地工作区未提交） |
| 代码包 | `ani-code-20260918-b1`；kk sha256 `73078b974f6e58f0569826bb12b0623af3bc372a8cfed6b78d09f3660a4c4dc8` |
| artifact | `ani-artifact-ubuntu24-amd64-20260918-b1`（含 cert-manager Chart `v1.21.2.tgz` sha256 `73a56e1728edd6c99f1f31082618c3259d279a76b7ebd3d4bdc5475c2442d34a`、helm `1f7ed083dbc200a10fdfe04df94c21530140fdc955de5a1daed2694150f77b17`） |
| 站点配置 | 私有 `site/cluster.yaml`；sha256 `0a9dde898549344543e6492ab7f5f6a1e9045249359d9d69112ca5080eeebdf9`（components.certManager=true，其余 false） |
| 启用组件 | cert-manager=true（appVersion v1.21.2 / chartVersion v1.21.2）；postgresql/valkey/nats=false |

### B1-1 快照与断网证据

- 还原 `restore_esxi_snapshots.sh execute`：3/3（test-installer-01/02/03，VMID 5/6/7，snapshotId=1），其余 5 台 VM 未触碰（`evidence/b1a6-restore.log`）。
- 安装前与安装后均 `apply_offline_isolation.sh verify` 通过：路由表无 ifindex0 黑洞、OUTPUT 跳 `ANI-OFFLINE` 存在、公网 HTTPS（`example.com`/`1.1.1.1`）code=000、DNS 失败、管理 SSH 可达（日志见 Fedora `foundation-20260918/`）。

### B1-2 安装退出码

`INSTALL_EXIT=0`；`total: 430, success: 420, ignored: 10, failed: 0`（结束 `2026-09-18T07:03:23Z`，`evidence/b1a6-install.log`）。普通 `ubuntu` 用户经 `install.sh` 入口，无需手动 export KUBECONFIG。

### B1-3 功能验证

- **cert-manager：pass**。独立 `verify.sh`（`sudo`，`VERIFY_EXIT=0`）：controller/webhook/cainjector 均 available 1/1；ClusterIssuer `ani-ca` + Certificate `ani-root-ca`（isCA）与测试叶子 `ani-ca-test-leaf` Ready；Secret 含 `tls.crt`/`ca.crt`/`tls.key`；随包 `172.16.101.20:5000/alpine/openssl:3.5.4` 验证链 `OK`，叶子 subject/issuer 正确、SAN=`ani-ca-test-leaf.ani-cert-test.svc[.cluster.local]`、根 CA 为自签名内部 CA（`ANI-CERT-MANAGER-VERIFY-OK`）。启动 hook Job `cert-manager-startupapicheck` `SuccessCriteriaMet=True`，镜像来自离线仓库 `172.16.101.20:5000/jetstack/cert-manager-startupapicheck:v1.21.2`。
- 镜像离线来源：cert-manager 角色断言 controller/webhook/cainjector 渲染镜像与 acmesolver 默认参数引用全部来自 `172.16.101.20:5000`（含 acmesolver，虽不启用 ACME 仍随包）。
- 网络/Envoy 回归：独立 verify 显示 `network=ANI-NETWORK-OK`、`Envoy HTTP=ANI-INSTALLER-OK`，registry=34 images，nodes=3。
- postgresql / valkey / nats：**skipped**（本批未启用，选择文件明确 false）。

### B1-4 持久化验证

cert-manager 为 CA 能力组件，本轮不引入数据 PVC；CA Secret 持久化重建不在 B1 范围，标记 **not_applicable**。

### B1-5 已知问题与责任方

- cert-manager 角色实现中修复的 installer 缺陷（a1–a5）：startupapicheck hook 默认删除策略、kubectl jsonpath 转义、openssl 3.5 输出格式、`{@}` vs `{.}` 遍历字符串数组、unattended-upgrades 占用 dpkg 锁、`template mode` 未生效改显式 `chmod` ——均已在源码修复并经 a6 验证通过。其中 `chmod` 修复使渲染出的 `verify.sh` 现为 `0700 root`、values/internal-ca 为 `0600 root`（证据 `evidence/b1a6-ev2.log`）。
- Ceph 仍 HEALTH_WARN（AES 认证与 PG 数），按既有记录保留，不在本轮处理。

### B1-6 下一批

B1 通过，可进入 B2（PostgreSQL）。

---

## B2：PostgreSQL

等待开始。

---

## B3：Valkey

等待开始。

---

## B4：NATS JetStream

等待开始。

---

## B5：最终交付

等待开始。

---

## 已知问题（跨批次）

| 编号 | 描述 | 责任方 | 状态 |
| --- | --- | --- | --- |
| K-1 | Ceph 认证与 PG 数告警按既有记录保留，本轮不调参消警 | 用户既有决定 | open（不在本轮处理） |
| K-2 | 执行计划 §7 称 cert-manager `crds.enabled: true` 为本版本字段，实际 Chart 默认为 `false`；本轮 values 显式置 true | installer（本轮） | resolved（B0 记录） |
| K-3 | 证书验证需要 openssl  binary；以随包 `alpine/openssl:3.5.4` 镜像提供，不在目标机安装 | installer（本轮） | resolved（B0 记录） |
| K-4 | B1 cert-manager 角色实现中修复的多项 installer 缺陷（startupapicheck hook 删除策略、jsonpath 转义、`{@}` 遍历、dpkg 锁、模板 `mode` 未生效改显式 `chmod`） | installer（本轮） | resolved（B1 a6 验证通过） |

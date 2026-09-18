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
| B2 | PostgreSQL | pass（a7 单次运行内 安装+独立verify+持久化 全绿；底座 Pod 重建 netns 抖动见 K-5） | 2026-09-18 |
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

**状态：产品判据 pass；底座存在未定位根因的阻塞（K-5）**。a7 为参考运行：安装、独立 verify、持久化三者在**同一运行内**全部通过，且是在真实干净快照、全程断网条件下取得。**但该次并非零干预通过**——运行中先对 PostgreSQL Pod 执行了“删除 → 由 StatefulSet 重建 → 直到网络可达”的**实验室恢复动作**，之后 verify 与持久化才一次通过。底座 kcn/OVN 存在**未定位根因**的 Pod 网络缺陷（详见「B2-5」与「附录 A」）。**因此不得据 a7 推断底座 pod 网络健康**；本批结论仅限于“PostgreSQL 产品实现按判据通过”。

### B2-0 批次事实

| 项 | 值 |
| --- | --- |
| 批次 / attempt | B2 / **a7 通过**（单次运行内 安装 + 独立 verify + 持久化 全绿）。a1/a4 底座 CNI 抖动、a2 凭据 SIGPIPE、a3 verify heredoc、a5 持久化过但 verify 抖动、a6 未预清 dpkg 锁早期失败，均定位并记录 |
| 源码快照 | Fedora `foundation-20260918/src`（B0 快照 + B1/B2 改动） |
| 代码包 | `ani-code-20260918-b2`；kk sha256 `f39b4fd993845d935c5af90270a977421ad69765a471c8ef7a581a2b92623035` |
| artifact | `ani-artifact-ubuntu24-amd64-20260918-b2`（新增 `postgres:17.11-bookworm`，amd64 manifest digest `sha256:7bade6d532592ca8ce7ee32def7399dad2607c4ea5583839fc4352a095a11ea6`；底座 runtime/ISO/hauler 与 B1 逐字节相同） |
| 站点配置 | 私有 `site/cluster.yaml`；sha256 `27f1d2987cbf382cd2fce5fcf4e4d583ea4f1868402a1c36c9c2825fde2db0a0`（certManager=true, postgresql=true, valkey/nats=false） |
| 启用组件 | cert-manager=true；postgresql=true（appVersion 17.11 / imageTag 17.11-bookworm / chartVersion null） |

### B2-1 快照与断网证据

- `restore_esxi_snapshots.sh execute` 3/3（test-installer-01/02/03，VMID 5/6/7，snapshotId=1）（`evidence/b2a7-restore.log`）；clean-check 干净（无 admin.conf/runtime，containerd/kubelet inactive，sdb 空）。安装前先清 dpkg 锁（`lab/preplock.sh`，3/3 `LOCK_FREE`）。
- 安装前/后 `apply_offline_isolation.sh` apply+verify 通过：路由表无 ifindex0 黑洞、OUTPUT 跳 `ANI-OFFLINE`、公网 HTTPS/DNS 失败、管理 SSH 可达（`evidence/b2a7-isolation-after.log` 等）。

### B2-2 安装退出码

以 a7 为参考运行：`INSTALL_EXIT=0`；`total: 446, success: 436, ignored: 10, failed: 0`（结束 `2026-09-18T13:40:47Z`，`evidence/b2a7-install.log`）。普通 `ubuntu` 用户经 `install.sh` 入口，无需手动 export KUBECONFIG。普通入口会在中途执行 PostgreSQL 组件自检（安装退出码即其通过）。

### B2-3 功能验证

- **PostgreSQL：pass**。独立 `verify.sh`（`VERIFY_EXIT=0`，同一构建 kk `f39b4fd9`，a7 参考运行 `evidence/b2a7-verify2.log`）：独立管理员与应用用户 Secret 均存在；应用用户 `ani_app`（非超级用户）经 Service DNS `postgresql.ani-platform.svc.cluster.local` 建表、插入唯一值、查询精确比对（`ANI-PG-CRUD-OK`）；错误密码连接被拒绝（`ANI-PG-AUTH-REJECTED`）；StatefulSet readyReplicas=1；PVC `data-postgresql-0` Bound。
- **cert-manager 回归：pass**。随包 `alpine/openssl:3.5.4` 验证证书链 `OK`，叶子 SAN=`ani-ca-test-leaf.ani-cert-test.svc[.cluster.local]`，内部自签 CA（`ANI-CERT-MANAGER-VERIFY-OK`）。
- 网络/Envoy 回归：`ANI-NETWORK-OK` / `ANI-INSTALLER-OK`，nodes=3。
- valkey / nats：**skipped**（本批未启用，选择文件明确 false）。

### B2-4 持久化验证（lab，正常 Pod 重建）

**pass**（`evidence/b2a7-persist2.log`，`B2-PERSISTENCE-OK`）。流程：记录 StatefulSet/PVC/Secret/Pod UID → 经 Service 以 `ani_app` 写入唯一值（`PERSIST-WRITE-OK`）→ 正常删除唯一服务 Pod（新 Pod UID 不同）→ 等待新 Pod Ready 且 Service EndpointSlice `ready=true` → 经同一 Service/用户读回原值（`PERSIST-READ-OK`）→ StatefulSet/PVC/Secret UID 均不变（a7：sts `0e9e6e50…`、pvc `c8cddced…`、secret `37733124…` 前后一致）。重建后的 Pod 落在 node2。

### B2-5 已知问题与责任方

- **K-5（底座 kcn/OVN Pod 网络缺陷，非本轮实现）— 未解决**：Pod 删除/重建后有概率被分配到**坏 netns**，该 Pod 的 **Pod IP 直连**与**经 Service ClusterIP** 从所有节点均超时；而 Pod 仍显示 `Ready`（探针为容器内 `exec`，不经网络）、端点显示 `ready: true`。同一 Pod 时而可达时而不可达，且**不限于**本批组件（coredns / smoke 等底座 Pod 同样复现）。已排除本轮相关因素：离线隔离（`remove` 后等待 75s 仍复现）、本批物料（底座 runtime artifact 与 B1 逐字节相同）、传播延迟（等待端点 ready 并多次重试仍超时）。**根因未定位**，触发条件与失败概率未量化。实验室绕过手段（**非修复**）：再次删除该 Pod 让 StatefulSet 重建，通常 1–2 次内可取得可达实例（a7 即如此）。责任方：底座 kcn/OVN。**完整诚实记录见「附录 A」。**
- B2 实现中修复并经重建 kk `f39b4fd9` 验证的 installer 缺陷：
  - 凭据生成 `tr -dc 'A-Za-z0-9' </dev/urandom | head -c 24` 在 `set -o pipefail` 下触发 SIGPIPE（`exit status 141`）；改为有界读取（`head -c 256`）+ `cut -c1-24`，已隔离验证 `len=24`。
  - 组件 `verify.sh` 使用**未加引号 heredoc**，使 `$(psql ...)` 在目标宿主机本地展开（`verify.sh: line 31: psql: command not found`）；改为转义容器侧 `\$`（与 cert-manager verify 一致），并已在 Fedora 上实际渲染+执行 heredoc 验证。
  - 组件 `verify.sh` 的 PVC 检查由 `kubectl ... | grep -q` 改为变量比较，避免 pipefail 下的 SIGPIPE。

### B2-6 下一批

a7 单次运行内产品判据全部通过（安装退出 0、独立 verify 0、持久化 0）。底座 kcn/OVN 在 Pod 重建时的坏 netns 抖动为环境阻塞（K-5），会使该 Pod 短时不可达，需“重建 Pod 重试”恢复。**B3（Valkey）具备开始条件，但需预期同类的 Pod 重建抖动并按 K-5 方式重试**（其认证/读写/持久化验证同样依赖 pod 网络）。

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
| K-5 | **底座 kcn/OVN Pod 网络缺陷（未定位根因）**：Pod 删除/重建后有概率被分配到坏 netns → 该 Pod IP 直连与 Service ClusterIP 从所有节点均超时，而 Pod 仍 `Ready`（探针为容器内 `exec`）、端点显示 `ready: true`；同 Pod 时而可达时而不可达；不限于本批组件（coredns / smoke Pod 同样复现）。已排除：离线隔离（移除后仍复现）、本批物料（底座 artifact 与 B1 逐字节相同）、传播延迟（等端点 ready 并多次重试仍超时）。**未确定**：根因、触发条件与失败概率。实验室绕过（**非修复**）：再删除该 Pod 重建，1–2 次内可取可达实例（`lab/b2-heal.sh`）。详见「附录 A」 | 底座 kcn/OVN（组件/环境） | **open（未解决）**；B2 已用重建绕过；建议底座修复后再判 B3/B4 |
| K-6 | B2 PostgreSQL 角色实现中修复的 installer 缺陷（凭据生成 pipefail SIGPIPE exit 141、verify.sh 未加引号 heredoc 本地展开 psql、PVC 检查管道） | installer（本轮） | resolved（B2 a7 验证通过） |

---

## 附录 A：底座 kcn/OVN Pod 网络缺陷（诚实记录）

> 本附录是本轮唯一**未修复、未定位到根因**的阻塞项，单独列出以便底座/kcn 维护方接手。
> 本轮范围内**没有、也不允许**对底座的 kcn/OVN 组件源码、镜像或配置做任何修改（见执行计划的职责边界），
> 因此以下全部为**黑盒观测记录**，不含根因结论。

### A-1 现象（观测到的事实）

- 组件安装完成、集群 3/3 Ready 之后，**对某个 Pod 执行删除/重建**（本批为 PostgreSQL 单副本 StatefulSet 的 Pod），
  有概率被 kcn/OVN 分配到一个**无法通信的 netns**：
  - 该 Pod 的 **Pod IP 直连**（来自 node1/node2/node3 的临时 Job）全部超时；
  - 经 **Service ClusterIP**（`10.96.x.x:5432`）同样超时；
  - Pod 自身仍 `Ready=True`——就绪探针是**容器内 `exec`** 的 `pg_isready`，不经过网络，**因此不能证明网络可用**；
  - Service EndpointSlice 中该端点显示 `ready: true`，但实际不可达；
  - 同一 Pod **时而可达、时而不可达**，无稳定周期（a5 一次运行中同一 Pod 先可达、随后不可达）。
- 该现象**不限于** PostgreSQL Pod：`ani-installer-smoke` 的底座 Pod、coredns Pod 也观察到同类不可达。
- 更早一次（B2 attempt a1）表现为：coredns 两个副本都落在 node3，**node3 宿主机 → 任意 Pod IP** 全部超时
  （宿主机到 10.16.0.2/.3/.10 均不可达），而 **node1 宿主机 → 同一 Pod IP 正常**；该次直接导致底座 smoke
  探针 DNS 解析失败（`wget: bad address`）、整轮安装中止（`total 363 / failed 1`，PostgreSQL 角色根本没跑到）。
- 不可达时段，node3 上 kcn-cni 日志可见 `del port` / `Nic is deleted` 之类的端口增删记录。

### A-2 已排除的因素

| 假设 | 验证方式 | 结论 |
| --- | --- | --- |
| 是本批 PostgreSQL 物料/写法问题 | 底座 runtime artifact（`kubekey-artifact.tgz`/ISO/hauler）与 B1 逐字节相同；StatefulSet/Service 为最简标准写法；安装内自检每次通过 | 排除 |
| 是实验室离线隔离（iptables `ANI-OFFLINE`）导致 | `apply_offline_isolation.sh remove` 后等待 75s 再复测，pod→pod、pod→Service 仍超时 | 排除 |
| 是 Pod 重建后的正常端点传播延迟 | 重建后等待 EndpointSlice `ready=true`，并跨数分钟重试多次仍超时 | 排除 |
| 是节点资源/调度问题 | 三节点 3/3 Ready，宿主机资源充裕；coredns 在其它节点健康 | 排除 |
| 是 DNS 配置问题 | Pod IP 直连亦超时（不经 DNS）；且同一 Pod 时好时坏 | 排除 |

### A-3 **未**确定的内容（诚实声明）

- **根因未定位**：本轮**没有**对 kcn-cni / ovn-central / ovs 的源码、日志与数据面做定位分析，
  也没有识别出触发条件（为何“某些重建”会拿到坏 netns）。
- **触发条件未量化**：仅知“Pod 重建后有一定概率发生”，**未测得**失败概率、时间窗口，以及与节点/镜像/资源的相关性。
- **“恢复手段”是实验室绕过，不是修复**：观察到的可用手段是“**再次删除该 Pod、由 StatefulSet 重建，通常 1–2 次内取得可达实例**”
  （`lab/b2-heal.sh`）。它只是把故障实例换掉，**并未修复底座缺陷**；缺陷仍然存在，只是被绕开。
- **影响范围未完全评估**：仅确认“Pod 重建后可能不可达”，**未验证**在正常业务运行（不重建 Pod）下是否也会发生。
  且 a1 的形态（宿主机→Pod 全断并影响 coredns）**不依赖我们删除 Pod**，说明风险不止于“重建”这一种触发。

### A-4 对 B2 结论的影响（诚实声明）

- B2 的**产品判据（安装 / 独立 verify / 持久化）确实在 a7 同一运行内全部通过**，且是在真实干净快照、全程断网条件下取得。
- 但 **a7 不是零干预的通过**：在跑 verify 与持久化之前，先执行了 A-3 的“重建 Pod 直到可达”恢复动作。
  因此 **a7 的结果不能用来断言底座 pod 网络健康**；若底座网络在检查期间劣化，落在该时段的临时验证 Job 会超时失败。
- 结论：**B2 的产品实现按判据通过；底座 kcn/OVN 的 Pod 网络缺陷为独立的环境阻塞（K-5），未解决。**

### A-5 建议（交由底座/kcn 维护方）

1. **复现**：清洁集群 → 删除并重建任意单副本 Pod（如 `ani-platform/postgresql-0`）→ 从其它节点验证 Pod IP 与 Service 连通性。
2. **定位**：比对“可达实例”与“不可达实例”的 netns / OVN 流表 / `ovn0` 与 host→pod 路由，重点排查 kcn-cni 在
   **del port → re-add** 路径上的竞态（不可达时段可见 `del port` / `Nic is deleted`）。
3. **修复前不建议**把依赖 pod 网络的组件（B3 Valkey / B4 NATS 及其验证）判为稳定通过。

# 第二批组件（指标 / 日志）离线集群 — 手动复现 Runbook

> **2026-09-19 实测更正：本文件尚未达到可交付状态。** 已完成两次真实离线安装尝试，分别在 metrics 模板路径和 Loki 配置解析处失败；OpenSearch 未执行。真实材料路径、证据与接续任务见 [最新交接文档](observability-live-handoff-20260919.md)。下文的“未实测”、占位产物路径、OpenSearch PVC 名称和部分入口说明是旧记录，不能照此宣称整批完成。

> 适用：把 ANI 离线集群（KubeKey 底座 + 第一批基础组件 + 第二批指标与日志组件）在测试集群
> `172.16.101.20/.21/.22` 上**从干净快照**拉起来，供人工或智能体测试。
>
> **本文件是本批（2026-09-19 第二批）的交付入口。** 第一批的入口是
> [foundation-components-manual-runbook.md](foundation-components-manual-runbook.md)，
> 本文件的所有路径都以第二批的真实工作区为准，**不引用**第一批的 heal / watch / 逐次修补脚本。
>
> **所有命令统一在 `fedora` 上执行**（`fedora` 是唯一可经 VPN 访问 `172.16.101.x`、
> 且能 SSH 到 ESXi `172.16.255.12` 的机器）。节点密码只从
> `~/ani-installer-runs/platform-20260918/access/node-password` 读取，
> 经 `sudo -S` 的 stdin 传入，**绝不落到命令行参数或屏幕**。
>
> **当前状态：本批 live 全部 `not_verified`。** 新版 kcn 的修复仅由用户口头告知，
> 镜像材料尚未提供，因此尚未执行过三节点真实安装。本文件描述的是**已经存在于代码里的
> 真实路径**，用户提供固定材料并排期后即可照此执行。

---

## 0. 拓扑、物料与凭据位置

| 项 | 值 |
|---|---|
| ESXi 宿主机 | `172.16.255.12`（凭据见 `restore_esxi_snapshots.sh` 默认）|
| 测试节点 | `172.16.101.20`(installer) / `.21` / `.22` |
| 本轮工作区(fedora) | `~/ani-installer-runs/observability-20260919/` |
| 快照还原脚本 | 仓库根 `restore_esxi_snapshots.sh`；fedora `~/ani-ops/restore_esxi_snapshots.sh` |
| 离线隔离脚本 | 仓库根 `apply_offline_isolation.sh`；fedora `~/ani-ops/apply_offline_isolation.sh` |
| 节点密码 | `~/ani-installer-runs/platform-20260918/access/node-password` |
| SSH askpass | `~/ani-installer-runs/platform-20260918/access/askpass.sh` |
| 节点安装目录 | `/opt/ani-installer/{code,artifacts,site}/`（干净快照里**没有**，每次要传）|
| 共享实验锁 | `~/ani-installer-runs/locks/`（与其它任务共用，实验前必须确认无占用）|

本批工作区（`~/ani-installer-runs/observability-20260919/`）目录用途：

| 目录 | 内容 |
|---|---|
| `src/` | 源码快照（`config/` + `kubekey/`），从本地树打包传输，**不是 git 仓库** |
| `inputs/charts/` | C0 锁定的四份 Chart tgz |
| `inputs/values/` | 渲染用的 values（代码渲染，非手抄）|
| `inputs/rendered/` | 门禁渲染出的完整 workload YAML |
| `inputs/expanded/` | `helm template` 展开的 Chart 源码，用于逐字段核对 |
| `releases/` | 本批 code / artifact 发布物（**当前为空，材料未提供**）|
| `evidence/` | 每卡证据（`c1/`、`c2-render-gate/`、`c3-render-gate/`、`c4-render-gate/`）|
| `lab/` | 门禁脚本副本（`c2-render-gate/`、`c3-render-gate/`、`c4-render-gate/`）|

> 与第一批的隔离说明：第一批工作区是 `~/ani-installer-runs/foundation-20260918/`。
> 本批**不修改**其内容，也不复用它 `${R}` 里过期的 kk 二进制。两批的 code/artifact 各自独立命名。

---

## 1. 组件与开关（照抄，不要自创字段）

选择文件固定 **八行**，第一行是 `# config_sha256=<sha256>`：

```
cert-manager / postgresql / valkey / nats / metrics / loki / opensearch / fluent-bit
```

`loki` / `opensearch` / `fluent-bit` 三行**由单一 `logging.backend` 字符串派生**，
因此两个日志后端在结构上不可能同时为 true，也不会出现"有采集器但没有后端"。
未实现的组件如果被启用，安装器**在改变目标之前就以明确错误失败**，不会静默跳过。

两种日志组合各自的站点配置示例（均已保留第一批四项开关）：

| 组合 | 示例文件 | 后端 |
|---|---|---|
| 指标 + Loki | `config/examples/observability-loki.yaml` | `logging.backend: loki` |
| 指标 + OpenSearch | `config/examples/observability-opensearch.yaml` | `logging.backend: opensearch` |

要点：
- `backend: opensearch` **要求** `components.certManager.enabled=true`（非 demo TLS 复用内部 CA
  `ani-ca`），不满足时明确报错，不会静默关闭安全功能。
- 启用日志时自动部署 Fluent Bit；`backend: none` 时**不部署**采集器。
- 默认 `storageClass: ani-block`（现有 Ceph RBD），日志容量默认 5Gi。

---

## 2. 标准 7 步（顺序不可乱）

> 步骤代号与第一批一致，便于对照；下面每个脚本路径都是本批的真实路径。

```bash
# 步骤 A. 还原干净快照（硬白名单：只动 .20/.21/.22）
bash ~/ani-ops/restore_esxi_snapshots.sh          # 先 dry-run 看计划
bash ~/ani-ops/restore_esxi_snapshots.sh execute  # 真正还原（3/3）

# 步骤 B. 重新施加离线隔离（★必须，见陷阱 #1）
bash ~/ani-ops/apply_offline_isolation.sh apply
bash ~/ani-ops/apply_offline_isolation.sh verify  # 期望 public HTTPS code=000 / dns-failed / MGMT-OK

# 步骤 C. 解除 unattended-upgrades 占锁（★3 台都要，见陷阱 #2）
#   脚本副本：kubekey/lab/foundation-bringup/preplock.sh（lab 通用件，两批共用）
SRC=~/ani-installer-runs/observability-20260919/lab/preplock.sh
PW=~/ani-installer-runs/platform-20260918/access/node-password
for N in 20 21 22; do
  ssh -o StrictHostKeyChecking=no ubuntu@172.16.101.$N "cat > /tmp/_pl.sh" < "$SRC"
  cat "$PW" | ssh -o StrictHostKeyChecking=no ubuntu@172.16.101.$N "sudo -S bash /tmp/_pl.sh"
done   # 每台应输出 LOCK_FREE after=...s

# 步骤 D. 传输物料到安装节点 .20（干净快照无包，必做）
#   照 b1-transfer.sh 的形状改三个变量与 code/artifact 名即可，见第 4 节。

# 步骤 E. 以普通 ubuntu 用户后台拉起安装
#   照 launch3.sh 的形状改 code/artifact/site 名即可，见第 4 节。

# 步骤 F. 轮询（指标栈 + 单节点日志后端，整轮明显长于第一批）
ssh ubuntu@172.16.101.20 "tail -n 20 /tmp/ani-install-<RUN>.stdout"

# 步骤 G. 独立功能校验
ssh ubuntu@172.16.101.20 "( cat \$HOME/.ani_pw; printf '\n' ) | script -qec \"sudo bash /tmp/_v.sh\" /dev/null"
# 期望 VERIFY_EXIT=0，且 components=... 每项 pass/skipped
```

---

## 3. ⚠️ 必踩坑清单（先读，照做就不会重蹈覆辙）

### 陷阱 #1：快照还原后必须重新施加离线隔离
- **现象**：`restore_esxi_snapshots.sh execute` 会重启节点，节点上**临时的 `ANI-OFFLINE`
  iptables 规则会被清空**。
- **后果**：不重新 apply，安装会跑在一台**能访问公网**的节点上，彻底破坏"离线安装"的证明。
- **正确做法**：**每次还原快照后、安装前，必做** `apply` + `verify`。
  `verify` 必须确认 `public https code=000 / dns-failed / MGMT-OK`。

### 陷阱 #2：unattended-upgrades 占 dpkg 锁
- **现象**：快照重启后 `unattended-upgrades` 在后台占用 `/var/lib/dpkg/lock-frontend`。
- **后果**：安装器 apt 阶段卡死/失败（第一批 a4/a6 即此坑）。
- **正确做法**：安装前对 **3 台**节点都跑 `preplock.sh`。只停 installer 节点（.20）不够。
  脚本的"成功"必须真实：不要复用只打印剩余进程却返回成功的旧逻辑。

### 陷阱 #3：干净快照里没有安装物料
- 快照是**底座预装前的干净态**，`/opt/ani-installer/{code,artifacts,site}` 不存在。
- 每次全新拉起都**必须**传输 code + artifact + site 配置，并校验 `sha256sum -c SHA256SUMS`。

### 陷阱 #4：离线隔离用 iptables，别退化成 blackhole 路由
- 用 **OUTPUT/FORWARD 链的 `ANI-OFFLINE` 规则**（放行 lo/established/私网/管理网，DROP 其余）。
- **绝不能**改成 `ip route add blackhole 0.0.0.0/1`：kubeadm 的 Go 路由枚举会因 `ifindex 0` 报
  `route ip+net: no such network interface`，导致 `Generate kubeadm join token` 失败。
- INPUT 链与默认路由**永不改动**，管理 SSH 不会被锁死。

### 陷阱 #5：模板 `mode:` 不被模板模块应用（已在源码修复，勿回归）
- 渲染出的脚本权限会变成非 0700，导致 root 执行异常。
- 修复方式：渲染后用显式 `chown root:root` + `chmod 0700` 修正。
  本批每个组件的 `tasks/main.yaml` 都含这两步（metrics / loki / opensearch / fluent-bit），改 role 时不要删。

### 陷阱 #6（本批新增）：日志后端的 PVC 名字不是 Chart 默认前缀
- Loki 的 PVC 是 `storage-ani-loki-0`；OpenSearch 的 PVC 是
  **`ani-opensearch-master-ani-opensearch-master-0`**
  （StatefulSet 的 PVC 名是 `<claim template>-<statefulset>-<ordinal>`，而该 Chart 的
  claim template 名就是 StatefulSet 名 `ani-opensearch-master`）。
  **不是** `ani-opensearch-master-0`（那是 Pod 名的形状，从来不是 PVC 名），
  也不是 `data-` 前缀。校验脚本从渲染出的 StatefulSet 推导该名字，不再硬编码。
- OpenSearch Chart 的 `volumeClaimTemplates[0].metadata.name` 是模板
  `opensearch.uname` = `<clusterName>-<nodeGroup>`，所以名字里没有 `data-`。
- 校验脚本里的 PVC 常量写错会表现为"PVC 明明 Bound 却校验失败"。

### 陷阱 #7（本批新增）：Chart 的 registry 拼接约定三个 Chart 各不相同
写 values 时必须逐 Chart 用渲染实测，不能类推：

| Chart | 字段 | 约定 |
|---|---|---|
| kube-prometheus-stack | 有 `global.imageRegistry` 等 registry 字段 | registry 单独填，**不能**塞整条引用 |
| fluent-bit | **没有** registry 字段，helper 是 `printf "%s:%s" .repository .tag` | `repository` 必须写成 `<registry>/host/path` 完整形式 |
| opensearch | 有 `global.dockerRegistry`，且会**同时**前置到 `image.repository` 与 `persistence.image`（init 容器镜像）| 这两个字段只写**不含 registry**的路径 |

---

## 4. 传输与安装命令（照形状改名字）

### 4.1 传输（步骤 D）

`b1-transfer.sh` 的形状可直接复用，改 4 处：

```bash
R=~/ani-installer-runs/observability-20260919          # 本批工作区
SRC=$R/releases
A=~/ani-installer-runs/platform-20260918/access
CODE=ani-code-<DATE>-obs-<BACKEND>                    # 实际发布名
ARTI=ani-artifact-ubuntu24-amd64-<DATE>-obs-<BACKEND> # 实际发布名
```

它做四件事：建 `/opt/ani-installer/{code,artifacts,site}`（属主 ubuntu）→
解包 code → 解包 artifact → 写 `site/cluster.yaml`（`umask 077`）→
对两边跑 `sha256sum -c SHA256SUMS` 并打印 `TRANSFER_PASS`。

### 4.2 安装（步骤 E）

`.20` 上以普通 `ubuntu` 身份执行的等价命令（`install.sh` 按 `EUID` 自动决定 `sudo -E`）：

```bash
/opt/ani-installer/code/<CODE>/install.sh \
  /opt/ani-installer/site/cluster.yaml \
  /opt/ani-installer/artifacts/<ARTI>
```

后台拉起时用 `setsid`/`nohup` 并重定向到 `/tmp/ani-install-<RUN>.stdout`，
轮询该文件里的 `INSTALL_EXIT=0`（步骤 F）。

### 4.3 独立校验（步骤 G）

```bash
code_root=/opt/ani-installer/code/<CODE>
artifact_root=/opt/ani-installer/artifacts/<ARTI>
sudo bash "$code_root/verify.sh" /opt/ani-installer/site/cluster.yaml "$artifact_root"
echo "VERIFY_EXIT=$?"
```

`verify.sh` 是**每个组件自己的** `verify.sh`，按 `work/components-selection.tsv`
里八行的开启状态逐个执行，并打印
`components=cert-manager=... postgresql=... ... metrics=... loki/opensearch=... fluent-bit=...`。

---

## 5. 两条日志后端各自的从零流程（互斥，二选一）

**一个部署要么有 Loki，要么有 OpenSearch，不会有第三个状态。** 切换后端必须**从干净快照
重新安装**，不支持在已有集群上卸载一个再装另一个（也不复制数据、不删 PVC 来演示替换）。

### 5.1 组合 A：指标 + Loki + Fluent Bit

1. 站点配置用 `config/examples/observability-loki.yaml`（`backend: loki`）。
2. 安装顺序（playbook 已固定）：`metrics → loki → fluent-bit`。
3. 后端自身校验（`/etc/kubernetes/ani/loki/verify.sh`）：
   StatefulSet `ani-loki`、PVC `storage-ani-loki-0` Bound、`/ready`、buildinfo、
   一次真实 `query_range`，并确认运行中的配置报告了 retention / compactor / schema。
4. 采集链校验（`/etc/kubernetes/ani/fluent-bit/verify.sh`）：在三台各起一个测试 Pod
   向 stdout 打印唯一 marker，然后**查询 Loki** 找回来并核对 namespace/pod/container/node 元数据。
5. 持久性：重建 Loki Pod 后原 marker 仍在；重建一个采集器 Pod 后游标保留、继续采集。

### 5.2 组合 B：指标 + OpenSearch + Fluent Bit

1. 站点配置用 `config/examples/observability-opensearch.yaml`（`backend: opensearch`），
   且**必须**开 `certManager`。
2. 安装顺序（playbook 已固定）：`metrics → opensearch → fluent-bit`。
3. OpenSearch 内部顺序（`ani/opensearch` role 已固定）：
   cert-manager 证书 Ready → Chart 启动并监听 9200 → **一次性安全初始化 Job** →
   认证/TLS API 校验 → 日索引模板与 ISM 保留策略 → Fluent Bit。
   Chart 的 TCP 就绪只是中间条件，**不是**安装完成。
4. 角色分工：管理员身份只用于首次初始化；Fluent Bit 用独立的 `ani-collector` 写入身份，
   权限限制到 `ani-logs-*`；验收查询用相应读取身份。
5. 索引与保留：日索引 `ani-logs-YYYY.MM.DD`，模板 `ani-logs-*`（1 主分片 / **0 副本**，
   单节点不能分配副本，否则永远 yellow），ISM 策略 `ani-logs-retention`
   按索引年龄进入 `delete` 状态。
6. 安全：`DISABLE_INSTALL_DEMO_CONFIG=true`、`allow_unsafe_democertificates: false`、
   `allow_default_init_securityindex: false`，http 走 https，匿名访问关闭。
   **不使用** demo 证书 / 默认 admin 密码 / `tls.verify off` 来换取通过。
7. 采集链校验同 5.1 第 4 步，只是查询目标换成 OpenSearch `_search`，
   并额外确认 Fluent Bit 用的是写入身份而**不是** admin。
8. 节点前提：OpenSearch 需要 `vm.max_map_count >= 262144`，
   由安装 role 通过 `/etc/sysctl.d/90-ani-opensearch.conf` 完成并记录（Chart 自带的
   `sysctlInit` 是 `privileged: true` 的 init 容器，本批**关闭**）。

---

## 6. 人工验收清单（必须真跑，不能只看 Ready）

指标（组合 A/B 共用）：

- [ ] 三个 node-exporter 与基础 targets 有效；经 Prometheus HTTP API 查询 `up`、
      `node_uname_info`、`kube_node_info` 与一项真实 cAdvisor 容器指标，返回当前三台。
- [ ] 真实 `Prometheus → Alertmanager → 临时接收器`：唯一标签规则 `vector(1) == 1` 进入 firing，
      接收器收到匹配标签的 webhook；改成 `vector(0) == 1` 后收到**同一 fingerprint 的 resolved**。
      **不要加 `bool`**，也**不要**直接 POST 给 Alertmanager 冒充求值链。
- [ ] 记录 Prometheus PVC/STS UID，产生唯一测试序列并取样本时间；
      正常重建唯一 Prometheus Pod（PVC 不变），用 range query 读回重建前样本。
- [ ] Alertmanager 用一个唯一 silence：写入 → 记 ID → 正常重建 AM Pod → API 读回同一 ID，
      PVC/Secret 不变。

日志（按所选后端）：

- [ ] 三台各起测试 Pod，stdout 打印唯一 marker（含 run/node/序号，非真实业务数据）；
      经**正常容器日志文件 → Fluent Bit → 后端查询 API** 找回三份 marker 及正确的
      namespace/pod/container/node。**不能直接 push 给后端冒充采集成功。**
- [ ] 记录后端 PVC/STS UID；**仅**正常重建后端 Pod，读回原 marker 且 PVC 不变。
- [ ] 正常重建一个 Fluent Bit Pod，游标/缓存目录保留；该节点继续产生新 marker，
      旧/新日志均可查。本批允许 at-least-once 重复，不承诺 exactly-once。
- [ ] 保留配置已生效可查（Loki：retention + compactor；OpenSearch：ISM 策略已关联）。
      **真实过期删除未做有界专项实验时，明确记 `retention-expiry=not_verified`**，
      不凭配置就报"删除已通过"，也不写定时 `rm` 脚本替代产品策略。
- [ ] 确认**没有**部署 Grafana / Dashboards / Jaeger；也确认不存在另一个日志后端。
      指标栈与第一批组件回归通过。

---

## 7. 状态与失败处理

- 每卡状态记录在 [observability-components-status.md](observability-components-status.md)：
  分开写 `code_status`（Fedora 上代码/材料/渲染/测试）与 `live_status`（三台真实验证）。
  每次 attempt 填写统一字段模板（卡号/attempt、各 hash、kcn 版本与状态、启用组件与后端、
  快照 3/3、离线前后、安装退出码、指标/告警/日志验收、重建持久性与 UID、保留配置与实际过期、
  已知问题与责任方）。
- 失败归因规则：
  - **实验准备错误**（隔离未施加、dpkg 锁未释放）→ 修 lab 输入；节点已被改则重置再开始。
  - **正确输入下的组件自身 bug / 已知旧 kcn** → 保留现场并交接，**不在 installer 里加修复**，
    不靠快照碰运气。
  - **原因不明地重复两次 / 快照结果不明 / 集群有人使用** → 停止该实验，报告具体阻塞，
    不抢锁、不清现场。
- **重试门槛**：必须能写出"上一失败证据、责任方、本次具体修正"。相同源码/物料/配置/环境
  未改变，**不重新安装**。不得重复相同的失败条件。
- 用户已授权实验阶段按需自动还原三台（不必逐次申请），但每次还原前**先保存证据、
  核对白名单 3 台与锁占用**。

## 8. 安全边界

- `restore_esxi_snapshots.sh` 是**硬白名单**：只还原 IP/名称命中 `.20/.21/.22` 的 VM，
  其余（含 `.10/.11/.12/.30` 等）只打印、只跳过、绝不写操作。
- 安装器只允许：处理固定物料、支持配置、必要 OS 前提、正常安装编排、等待、校验、日志。
  **禁止** ESXi/重置/清盘/删失败 ns/PVC/强 detach/重启赌博，也**禁止**改组件源码、
  构建热修镜像、关闭认证或探针、吞错误。
- 普通 `verify` **不重启服务**；持久化 Pod 重建只在 lab 中按明确 UID 执行。
- 节点密码只经 `sudo -S` stdin 或 `SSH_ASKPASS` 传入，绝不出现在 argv 或日志明文。
- **本批的 `connections.md` 与本文档都不含任何明文凭据**，只有 Secret 名与命名空间。

---

## 9. 连接说明（怎么连、凭据在哪 — 无明文）

安装成功后，安装器会把每个已启用组件的连接事实片段拼成
`/var/lib/ani-installer/<cluster_name>/connections.md`（**root-only 0600**，
因为里面列出了 Secret 名与命名空间）。下面是从该文件提炼的入口，
**下面没有任何密码**：

| 组件 | 入口 | 端口 | 凭据位置（**只有引用**）|
|---|---|---|---|
| Prometheus | `ani-metrics-prometheus.<ns>.svc.cluster.local` | 9090 | 无（集群内不认证）|
| Alertmanager | `ani-metrics-alertmanager.<ns>.svc.cluster.local` | 9093 | 无（集群内不认证）|
| Loki（组合 A）| `ani-loki.<ns>.svc.cluster.local` | 3100 | 无（单租户，`auth_enabled: false`，**不可**对外发布）|
| OpenSearch（组合 B）| `ani-opensearch-master.<ns>.svc.cluster.local` | 9200 / transport 9300 | 管理员 Secret `ani-opensearch-admin`；采集器 Secret `ani-opensearch-fluent-bit` |
| OpenSearch CA | — | — | Secret `ani-opensearch-node-tls`，key `ca.crt` |
| Fluent Bit | `daemonset/ani-fluent-bit` | — | 无（后端凭据经 `secretKeyRef` 注入）|

命名空间：指标与日志都在 `ani-observability`（第一批组件在 `ani-platform`）。

读凭据的方式（**不落磁盘、不写入 shell 历史**）：

```bash
# 例：取 OpenSearch 管理员密码。注意：这条命令会把密码**输出到终端**，
# 因此只适用于交互式排查；脚本化场景请直接赋给变量，不要经过 stdout。
kubectl -n ani-observability get secret ani-opensearch-admin \
  -o jsonpath='{.data.password}' | base64 -d
```

上面这条会打印到屏幕，**不要**用它来证明"读凭据但不上屏"。真正不上屏的写法是把值直接
喂给使用者（例如 `read -r PW <<<"$(... )"` 或经 `secretKeyRef` 注入容器），
中间结果不经过终端。

其余要点：

- Loki 与 Prometheus / Alertmanager 的 Service 都是 **ClusterIP only**，
  没有 NodePort / Ingress / Gateway，**故意没有对外入口**。
- OpenSearch 强制 TLS + 认证，匿名访问被拒；客户端必须信任 `ani-ca` 签发的 CA。
- **第一批组件的连接片段由本批补齐**：`ani/cert-manager`、`ani/postgresql`、`ani/valkey`、
  `ani/nats` 的 role 各自渲染片段，与第二批一起被拼进同一个 `connections.md`。
  这**不改动**第一批的业务实现。

---

## 10. 脚本与证据索引

| 用途 | 位置 |
|---|---|
| 门禁总入口（C2 指标）| `kubekey/lab/c2-render-gate/run-c2-gate.sh` |
| 门禁总入口（C3 Loki + Fluent Bit）| `kubekey/lab/c3-render-gate/run-c3-gate.sh` |
| 门禁总入口（C4 OpenSearch + Fluent Bit）| `kubekey/lab/c4-render-gate/run-c4-gate.sh` |
| 通用模板渲染（被上述门禁调用）| `kubekey/lab/{c2,c3,c4}-render-gate/render-values.sh`（三份内容一致；`ROLE` + `BACKEND` + `TEMPLATE_KIND`）|
| 通用 Chart 渲染门 | `kubekey/lab/c2-render-gate/render-gate.sh`（C3/C4 为同名脚本，`ROLE_ROOT` 可覆盖）|

**每张门禁都渲染 role 的 `tasks/main.yaml`**（`TEMPLATE_KIND=tasks`），不只是 `values.yaml`。
这不是可有可无的附加项：真实安装的第一次失败正是任务模板读错上下文档位——九处
`{{ .ani.metrics.namespace }}`（那是选择行，只有 `enabled`）而不是
`{{ .ani.components.metrics.namespace }}`，渲染成 `<no value>` 后 `no` 被当成了重定向，
节点上报 `/bin/bash: line 1: no: No such file or directory` 与 `error: no objects passed to apply`。
只渲染 values / verify.sh / connection.md 的检查永远看不到这一类缺陷。

> `kubekey/lab/*-render-gate/` 是**参考副本**，原件在 fedora 工作区
> `~/ani-installer-runs/observability-20260919/lab/`。注意门禁通过 `ssh` 运行时，
> 必须把 `c*-render-gate` 目录**同时**放到 `$SRC/kubekey/lab/` 和 `$RUN_ROOT/lab/`，
> 因为驱动脚本会从 `$LAB/render-values.sh` 取通用渲染器。

本批已完成并留证的门禁结果（均为**离线**，不含真实安装）：

| 卡 | 证据目录 | 结论 |
|---|---|---|
| C1 | `evidence/c1/` | typed 配置与八行选择文件，`go test ./pkg/ani` 通过 |
| C2 | `evidence/c2-render-gate/` | 指标渲染门通过，外来镜像 0 |
| C3 | `evidence/c3-render-gate/` | Loki 441 行 / Fluent Bit（loki）238 行 / Fluent Bit（opensearch）262 行，外来镜像 0 |
| C4 | `evidence/c4-render-gate/` | OpenSearch 293 行，四个渲染全部通过，外来镜像 0 |

物料锁与版本见 [observability-components-status.md](observability-components-status.md)
的 C0 章节（Chart SHA256、镜像 digest、amd64 manifest 清单）。

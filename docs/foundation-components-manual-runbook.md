# 基础组件离线集群 — 手动拉起 Runbook（Agent 必读）

> 2026-09-19 B5 复核：本文件主要仍为 B1 操作路径，不是最终 B4 交付入口。当前 NATS 存在宿主机到 Pod 网络故障，B5 blocked。不要按第 4 节直接修改组件开关后重跑已有集群；保留现场，见 [B5 实测复核](foundation-b5-verification-20260919.md)。

> 用途：从**干净快照**手动把 ANI 离线集群（KubeKey 底座 + 基础组件）在测试集群
> `172.16.101.20/.21/.22` 上拉起来，供人工或智能体测试。
> 当前进度：B1（cert-manager）已验证通过；B2–B5 未开始。
>
> 所有命令**统一在 `fedora` 上执行**（`fedora` 是唯一可经 VPN 访问 `172.16.101.x`
> 且能 SSH 到 ESXi `172.16.255.12` 的机器）。节点登录密码统一从
> `~/ani-installer-runs/platform-20260918/access/node-password` 读取，**绝不落到命令行参数或屏幕**。

---

## 0. 拓扑与物料位置（先记住，后面高频用到）

| 项 | 值 |
|---|---|
| ESXi 宿主机 | `172.16.255.12`（root / 凭据见 `restore_esxi_snapshots.sh` 默认）|
| 测试节点 | `172.16.101.20`(installer) / `.21` / `.22` |
| 离线隔离脚本 | 仓库根 `apply_offline_isolation.sh`；fedora `~/ani-ops/apply_offline_isolation.sh` |
| 快照还原脚本 | 仓库根 `restore_esxi_snapshots.sh`；fedora `~/ani-ops/restore_esxi_snapshots.sh` |
| 本轮工作区(fedora) | `~/ani-installer-runs/foundation-20260918/`（`lab/` 脚本、`releases/` 物料、`inputs/` 站点配置）|
| 凭据(fedora) | `~/ani-installer-runs/platform-20260918/access/{node-password,askpass.sh}` |
| 节点安装目录 | `/opt/ani-installer/{code,artifacts,site}/`（干净快照里**没有**，每次要传）|

物料（fedora `releases/`）：
- `ani-code-20260918-b1/`：`kk` 二进制 + `install.sh` + `verify.sh` + `SHA256SUMS`
- `ani-artifact-ubuntu24-amd64-20260918-b1/`：Helm charts、hauler 镜像 store、`components.lock.yaml`、`SHA256SUMS`
- `inputs/site-b1-cluster.yaml`：B1 站点配置（仅启用 `certManager`）

脚本副本已归档在仓库 `kubekey/lab/foundation-bringup/`（见末尾「脚本索引」）。

---

## 1. 标准 7 步（顺序不可乱）

```bash
# 步骤 A. 还原干净快照（只动白名单 .20/.21/.22）
bash ~/ani-ops/restore_esxi_snapshots.sh        # 先 dry-run 看计划
bash ~/ani-ops/restore_esxi_snapshots.sh execute # 真正还原

# 步骤 B. 重新施加离线隔离（★必须，见陷阱 #1）
bash ~/ani-ops/apply_offline_isolation.sh apply
bash ~/ani-ops/apply_offline_isolation.sh verify   # 期望：public HTTPS/DNS FAIL、MGMT-OK

# 步骤 C. 传输物料到安装节点 .20（干净快照无包，必做）
bash ~/ani-installer-runs/foundation-20260918/lab/b1-transfer.sh

# 步骤 D. 解除 unattended-upgrades 占锁（3 台都要，见陷阱 #2）
SRC=~/ani-installer-runs/foundation-20260918/lab/preplock.sh
PW=~/ani-installer-runs/platform-20260918/access/node-password
for N in 20 21 22; do
  ssh -o StrictHostKeyChecking=no ubuntu@172.16.101.$N "cat > /tmp/_pl.sh" < "$SRC"
  cat "$PW" | ssh -o StrictHostKeyChecking=no ubuntu@172.16.101.$N "sudo -S bash /tmp/_pl.sh"
done   # 每台应输出 LOCK_FREE after=...s

# 步骤 E. 以普通 ubuntu 用户后台拉起安装
bash ~/ani-installer-runs/foundation-20260918/lab/launch3.sh

# 步骤 F. 轮询（整轮约 12–15 分钟）
ssh ubuntu@172.16.101.20 "tail -n 20 /tmp/ani-install-B1-a1.stdout; echo EXIT=\$(grep -c INSTALL_EXIT /tmp/ani-install-B1-a1.stdout)"
# 看到 INSTALL_EXIT=0（total 430 / success 420 / failed 0）即成功

# 步骤 G. 独立功能校验
ssh ubuntu@172.16.101.20 "cat > /tmp/_vb1.sh" < ~/ani-installer-runs/foundation-20260918/lab/verifyb1.sh
ssh ubuntu@172.16.101.20 "( cat \$HOME/.ani_pw; printf '\n' ) | script -qec \"sudo bash /tmp/_vb1.sh\" /dev/null"
# 应看到 VERIFY_EXIT=0 与 ANI-CERT-MANAGER-VERIFY-OK
```

---

## 2. ⚠️ 必踩坑清单（写在最前面，照做就不会重蹈覆辙）

### 陷阱 #1：快照还原后必须重新施加离线隔离
- **现象**：`restore_esxi_snapshots.sh execute` 会重启节点，节点上**临时的 `ANI-OFFLINE` iptables 规则会被清空**。
- **后果**：若不重新 `apply_offline_isolation.sh apply`，安装会跑在一台**能访问公网**的节点上，彻底破坏「离线安装」的证明。
- **正确做法**：**每次还原快照后，安装前，必做** `apply` + `verify`。`verify` 必须确认 `public https code=000 / dns-failed / MGMT-OK`。

### 陷阱 #2：unattended-upgrades 占 dpkg 锁
- **现象**：快照重启后 `unattended-upgrades` 会在后台占用 dpkg 锁，且可能超过安装器有界等待时长。
- **后果**：安装器 apt 阶段卡死/失败（a4 即此坑）。
- **正确做法**：安装前对 **3 台**节点都跑 `preplock.sh`（停服务 + 轮询锁释放）。只停 installer 节点（.20）不够。

### 陷阱 #3：干净快照里没有安装物料
- 快照是**底座预装前的干净态**，`/opt/ani-installer/{code,artifacts,site}` 不存在。
- 每次全新拉起都**必须** `b1-transfer.sh` 传输 + 校验 `sha256sum -c`。

### 陷阱 #4：离线隔离用 iptables，别退化成 blackhole 路由
- `apply_offline_isolation.sh` 用 **OUTPUT/FORWARD 链的 `ANI-OFFLINE` 规则**（放行 lo/established/私网/管理网，DROP 其余）。
- **绝不能**改成 `ip route add blackhole 0.0.0.0/1` 那种无接口黑洞路由——kubeadm 的 Go 路由枚举会因 `ifindex 0` 报
  `route ip+net: no such network interface`，导致 `Generate kubeadm join token` 失败。
- INPUT 链与默认路由**永不改动**，管理 SSH 不会被锁死。

### 陷阱 #5：模板 `mode:` 不被模板模块应用（已在源码修复，勿回归）
- 早期渲染出的 `verify.sh` 权限变成 `--w-r-xr--`（非 0700），导致 root 执行异常。
- 修复方式：渲染后用显式 `command: install -m 0700 ...` 修正。源码已含该修复，后续改 role 时不要删掉这些 chmod 任务。

---

## 3. 底层安装命令（排查时用）

`launch3.sh` 最终在 `.20` 上以 `ubuntu` 身份执行的等价命令：

```bash
sudo -E /opt/ani-installer/code/ani-code-20260918-b1/kk ani install \
  --config /opt/ani-installer/site/cluster.yaml \
  --package-root /opt/ani-installer/artifacts/ani-artifact-ubuntu24-amd64-20260918-b1
```

`install.sh` 会按 `EUID` 决定 `sudo -E` 与否，并校验 `CONFIG`/`ARTIFACT_ROOT`/`kk` 存在且 `kk` 可执行。

---

## 4. 测试其它批次（B2/B3/B4/B5）

改 `inputs/site-b1-cluster.yaml` 的 `components:` 块即可（保持前序组件启用，增量叠加）：

```yaml
components:
  certManager: { enabled: true }    # B1 已验证
  postgresql:  { enabled: true }    # B2
  valkey:      { enabled: true }    # B3
  nats:        { enabled: true }    # B4/B5
```

改完重跑 **步骤 C（传输，会重写 site/cluster.yaml）→ E（安装）→ G（校验）**。
每个批次必须从干净快照起、真实验证通过后再进下一批（见 `foundation-components-batch-execution-plan-20260918.md`）。

---

## 5. 安全边界

- `restore_esxi_snapshots.sh` 是**硬白名单**：只还原 IP/名称命中 `.20/.21/.22` 的 VM，其余（含 `.10/.11/.12/.30` 等）只打印、只跳过、绝不写操作。
- 安装器只允许：处理物料、支持配置、正常安装顺序、等待、校验、日志。**禁止** ESXi/重置/清盘/删失败 ns/PVC/强 detach/重启赌博，也**禁止**改组件源码或构建热修镜像、吞错误。
- 节点密码只经 `sudo -S` stdin 或 `SSH_ASKPASS` 传入，绝不出现在 argv 或日志明文。

---

## 6. 脚本索引

| 脚本 | 位置 | 作用 |
|---|---|---|
| `restore_esxi_snapshots.sh` | 仓库根 / fedora `~/ani-ops/` | 白名单快照还原（dry-run / execute）|
| `apply_offline_isolation.sh` | 仓库根 / fedora `~/ani-ops/` | 离线隔离（apply / verify / remove）|
| `launch3.sh` | `kubekey/lab/foundation-bringup/` | 把安装脚本推到 .20 并后台拉起 |
| `nodeinstall.sh` | 同上 | .20 上的实际安装入口（调 `install.sh`）|
| `preplock.sh` | 同上 | 停 unattended-upgrades 并等 dpkg 锁释放 |
| `b1-transfer.sh` | 同上 | 传输 code/artifact/site 到 .20 并校验 |
| `mksite.sh` | 同上 | 生成 `inputs/site-b1-cluster.yaml`（含组件开关）|
| `verifyb1.sh` | 同上 | 在 .20 上跑独立 `verify.sh` 功能校验 |

> `kubekey/lab/foundation-bringup/` 下为**参考副本**，原件在 fedora 工作区。
> 路径变量（`R`/`A`/`L`）按你的工作区调整即可复用。

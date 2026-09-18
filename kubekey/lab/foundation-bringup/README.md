# foundation-bringup 实验室脚本（参考副本）

本目录是 **2026-09-18 基础组件离线安装 B1（cert-manager）/ B2（PostgreSQL）轮** 实际使用的实验室编排脚本的**参考副本**，
原件位于 `fedora` 的 `~/ani-installer-runs/foundation-20260918/lab/`。

用途：从干净快照手动把 ANI 离线集群拉起并验证。完整步骤与踩坑说明见
[`docs/foundation-components-manual-runbook.md`](../../../docs/foundation-components-manual-runbook.md)。

## 文件清单

### 通用 / B1（`b1-*`、`launch3.sh` 等）

| 文件 | 作用 |
|---|---|
| `launch3.sh` | 把 `nodeinstall.sh` 推到 .20，并以普通 `ubuntu` 用户后台拉起安装 |
| `nodeinstall.sh` | .20 上的实际安装入口（调用 `install.sh` → `kk ani install`）|
| `preplock.sh` | 停 `unattended-upgrades` 并轮询等待 dpkg 锁释放（每台节点各跑一次）|
| `b1-transfer.sh` | 传输 `code/artifact/site` 到 .20 并 `sha256sum -c` 校验 |
| `mksite.sh` | 生成 `inputs/site-b1-cluster.yaml`（含组件开关，密码运行时注入）|
| `verifyb1.sh` | 在 .20 上跑安装包自带 `verify.sh` 做独立功能校验 |
| `clean-check.sh` | 节点侧清洁态检查（admin.conf/runtime/sdb/iptables 残留）|

### B2（PostgreSQL）

| 文件 | 作用 |
|---|---|
| `b2-transfer.sh` | B2 物料（`ani-code-20260918-b2` + 累计 artifact + site）传输与校验 |
| `mksite-b2.sh` | 生成 `inputs/site-b2-cluster.yaml`（certManager=true, postgresql=true，其余 false）|
| `nodeinstall-b2.sh` | .20 上 B2 的安装入口（与 `nodeinstall.sh` 差异仅在 release id 与日志名）|
| `launch-b2.sh` | 把 B2 入口推到 .20 并以普通 `ubuntu` 用户外加 pty 后台拉起 |
| `run-verify-b2.sh` | fedora 侧发起、在 .20 上以 pty+sudo 运行安装包自带 `verify.sh` |
| `verifyb2.sh` | .20 上的 verify payload（`sudo bash verify.sh`，不手动 export KUBECONFIG）|
| `b2-nodecheck.sh` | 三节点清洁态检查（基于 `clean-check.sh` + `run_on_node.sh`）|
| `b2-persist.sh` | **lab** Pod 重建持久化测试：记录 UID → 写值 → 删 Pod → 重建 → 读回 → UID 不变 |
| `b2-heal.sh` | **lab** 绕过底座 kcn/OVN 坏 netns：反复删除/重建 Pod 直到可达（K-5，详见 `docs/foundation-components-status.md` 附录 A）|
| `a6-watch.sh` | 安装结束后**自动并行**触发持久化与独立 verify 的看护脚本（争取底座网络健康窗口）|


## 复用须知

- 脚本里的 `R` / `A` / `L` 是 fedora 工作区绝对路径，**按你的环境调整**即可复用。
- 节点密码只经 `sudo -S` stdin 或 `SSH_ASKPASS` 传入，**绝不出现在命令行参数或日志明文**。
- `launch3.sh` / `nodeinstall.sh` / `preplock.sh` / `verifyb1.sh` 中 `/opt/ani-installer/...`
  为节点侧固定路径，无需调整。
- 这些脚本**不改产品代码**，仅用于实验室编排；快照还原与离线隔离请用仓库根的
  `restore_esxi_snapshots.sh` 与 `apply_offline_isolation.sh`。

## 关键顺序

还原快照 → 重新施加离线隔离 → 传输物料 → 解除 dpkg 锁（3 台）→ 安装 → 校验。
漏掉「重新施加离线隔离」会让安装跑在有公网的节点上，破坏离线证明（已踩过坑）。
漏掉「解除 dpkg 锁」会被开机 `unattended-upgrades` 占住 `/var/lib/dpkg/lock-frontend`，装机在 300s 等待后以 exit 100 早退（B2 attempt a6 即如此，属实验准备问题，非产品缺陷）。

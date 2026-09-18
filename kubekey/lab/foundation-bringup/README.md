# foundation-bringup 实验室脚本（参考副本）

本目录是 **2026-09-18 基础组件离线安装 B1 轮** 实际使用的实验室编排脚本的**参考副本**，
原件位于 `fedora` 的 `~/ani-installer-runs/foundation-20260918/lab/`。

用途：从干净快照手动把 ANI 离线集群拉起并验证。完整步骤与踩坑说明见
[`docs/foundation-components-manual-runbook.md`](../../../docs/foundation-components-manual-runbook.md)。

## 文件清单

| 文件 | 作用 |
|---|---|
| `launch3.sh` | 把 `nodeinstall.sh` 推到 .20，并以普通 `ubuntu` 用户后台拉起安装 |
| `nodeinstall.sh` | .20 上的实际安装入口（调用 `install.sh` → `kk ani install`）|
| `preplock.sh` | 停 `unattended-upgrades` 并轮询等待 dpkg 锁释放（每台节点各跑一次）|
| `b1-transfer.sh` | 传输 `code/artifact/site` 到 .20 并 `sha256sum -c` 校验 |
| `mksite.sh` | 生成 `inputs/site-b1-cluster.yaml`（含组件开关，密码运行时注入）|
| `verifyb1.sh` | 在 .20 上跑安装包自带 `verify.sh` 做独立功能校验 |

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

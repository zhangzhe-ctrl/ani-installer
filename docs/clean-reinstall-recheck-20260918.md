# 2026-09-18 全新离线安装复核

结论：**FAIL**。按用户要求执行一次快照还原后的安装；失败后停止，未修改代码、未重编译、未重试、未手动补装依赖。

## 本次输入与操作

- 所有环境操作经 SSH config host `fedora` 执行。
- 使用项目 `restore_esxi_snapshots.sh`，先 dry-run，再 execute。
- 仅还原 `test-installer-01/02/03`（172.16.101.20～22，VMID 5/6/7，snapshotId=1），3/3 成功；其它 VM 未操作。
- 还原后确认三台无旧 Kubernetes/installer 运行目录，kubelet/containerd inactive，`/dev/sdb` 为 50 GiB 空盘。
- 沿用现场配置：ens34 管理网络；kcn 接管无主机地址的 ens35；ens36 未配置。本次不验证专用存储网络。
- 使用既有 `apply_offline_isolation.sh`。三台公网域名访问及 1.1.1.1 HTTPS 均超时，管理连接正常，路由表无 blackhole。
- 未重新构建；使用现有发布 `ani-code-20260918-r10` 和独立 artifact `ani-artifact-ubuntu24-amd64-20260918-r2`。Fedora 与目标机校验均通过。
- 当前现场配置在还原前保存到 Fedora 的私有运行目录，未写入本报告。
- kk SHA256：`54e0e8aaaa6e64a9dbdce8beda1952c25bf7d8d60b14890a9cdc914b5c7bf08d`。

本结论针对上述发布二进制，不代表重新编译当前本地源码的结果。

## 安装命令与结果

在 `.20` 宿主机以 root 运行：

```bash
/opt/ani-installer/code/ani-code-20260918-r10/kk ani install \
  --config /opt/ani-installer/site/cluster.yaml \
  --package-root /opt/ani-installer/artifacts/ani-artifact-ubuntu24-amd64-20260918-r2
```

- 开始：2026-09-18 09:43:23 Asia/Shanghai。
- 结束：2026-09-18 09:44:42 Asia/Shanghai。
- 退出码：1；任务统计：136 total / 132 success / 3 ignored / 1 failed。
- 失败任务：`NTP | Configure NTP server`，node1，底层退出码 2。

```text
sed: can't read /etc/chrony/chrony.conf: No such file or directory
/bin/bash: line 11: /etc/chrony/chrony.conf: No such file or directory
```

失败后只读确认：node1 的 chrony 未安装（dpkg 状态 `un`），`/etc/chrony` 不存在，`/etc/kubernetes/admin.conf` 不存在，kubelet/containerd inactive，临时镜像服务 active，安装进程已退出。

直接问题是执行 NTP 配置时，所依赖的 chrony 包/配置文件尚未准备好。属于 installer 系统依赖准备和执行顺序这一层；本次未定位到具体代码根因，不能归因于 Ceph 或 kcn。未继续运行独立 verify，也未测试存储读写。

## 证据和现场

Fedora：`/home/chabking/ani-installer-runs/recheck-20260918/`

- `restore-dry.log`、`restore-execute.log`
- `clean-172.16.101.20.log`、`clean-172.16.101.21.log`、`clean-172.16.101.22.log`
- `artifact-checksums.log`、`transfer.log`
- `isolation-apply.log`、`isolation-before.log`
- `launch.log`、`install.raw.log`、`install.clean.log`、`failure-state.log`

node1：`/var/log/ani-install-run.log` 和 `/var/lib/ani-installer/ani-lab/logs/install.log`。

失败现场原样保留，临时仓库与离线限制保留。后续如修复并再次测试，需要重新还原三台快照；本次未执行第二次还原或安装。

## 后续只读诊断：上游与 fork 对比

- 读取 KubeKey v4.0.7 官方 `builtin/core/roles/native/repository/tasks/install_package.yaml`，与 r10 二进制嵌入的同名内容比较：唯一差异是 Debian chrony 安装检测从 `systemctl ... LoadState=loaded` 改为 `dpkg-query ... Status=install ok installed`。本次不是 r10 遗漏这项修复。
- 两者均保留原生 ISO 本地 APT 源安装能力，并请求 chrony 等系统依赖；没有证据表明 fork 重写或删掉了这段安装链路。
- 两者的 Debian 安装脚本均没有可靠地在 `apt-get update` / `apt install` 失败时终止，后面恢复软件源的命令可能使整个步骤返回成功。随后 NTP 任务才因配置文件不存在报错。不能把 NTP 最后的错误当作最早的失败原因。
- 离线 ISO 包含 95 个 deb，包括 `chrony_4.5-1ubuntu4.2_amd64.deb`，并有索引记录。
- 在独立 `/tmp/ani-apt-diagnosis` 中使用该 ISO 的索引与目标机现有 dpkg 状态，执行 `apt-get --simulate install socat conntrack ipset ebtables chrony ipvsadm nftables`，依赖求解成功。另以原 ISO 只读挂载核对索引读取成功，检查后卸载。没有实际安装软件，没有改系统 APT 源或系统索引。这排除了明显的缺 chrony 包/索引和当前状态下不可求解问题，不能代替真实安装通过。
- journal 记录 `apt-daily.service` 在本地时间 09:43:54～09:50:01 运行，与 09:44:32 开始的依赖任务重叠；APT 锁竞争是候选原因，尚无本次原始锁错误，不能宣称已证实。
- 本次成功任务的完整 APT stdout/stderr 未在已有安装日志中保留，最早的 APT 错误仍未确定。没有进行修复或安装重试。

诊断材料在同一 Fedora 证据目录：`upstream-install_package.yaml`、`r10-install_package.yaml`、`apt-sim.log`、`apt-sim-full.log`、`iso-perms.log`、`apt-command.log`、`apt-lock.log`。

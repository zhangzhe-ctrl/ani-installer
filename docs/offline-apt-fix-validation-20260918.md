# 离线系统依赖修复与全新安装验证

2026-09-18：**本轮完整离线安装 PASS**。用户授权修复及按需还原三台测试虚拟机后，仅操作 172.16.101.20、.21、.22。环境操作、构建和测试全部通过 SSH config host `fedora` 执行；未推送代码。

## 修改内容

保留 KubeKey 原生 ISO 和 Debian 系统依赖安装流程，仅修改 `kubekey/builtin/core/roles/native/repository/tasks/install_package.yaml` 的 Debian 分支：

1. Shell 遇错立即退出，APT update/install 的错误不再被后续软件源恢复命令掩盖。
2. ISO 安装使用临时专用 sources、lists 和 cache，不移动或删除系统 `/etc/apt` 软件源，不与 apt-daily 共用索引目录。
3. apt-get 在索引更新报错时失败；dpkg 锁使用有界 300 秒等待，不删除锁、不杀包管理进程。
4. 使用非交互安装，继续沿用已有缺失依赖检测。

没有修改 Ceph、kcn、Envoy 镜像或配置，没有手动补装 chrony，没有重打 artifact。

原问题中“错误被吞掉”已由执行级回归测试复现：模拟 apt-get update 返回 100，旧任务返回 0。新任务分别保留 update=100、install=101，并保证系统源不变。历史运行与 apt-daily 重叠，但历史 APT 原始错误没有保留，因此不能把锁竞争宣称为已证实的唯一根因。

## 测试与构建

- `python3 scripts/test-debian-repository.py`：3 个分支通过，覆盖更新失败、安装失败及成功。
- `go test -p 4 ./pkg/ani ./pkg/connector`：通过。
- `go test -p 4 -tags=builtin ./pkg/ani ./cmd/kk/app`：通过；app 包无测试。
- 独立代码发布构建、help 和校验和检查：通过。
- 构建来自本地源码的完整隔离副本，未覆盖 Fedora 上原有脏工作目录。构建日志中 `hack/version.sh` 有执行权限警告，make 仍成功产出二进制；本轮用下述 SHA256 精确标识验证对象，不以构建版本字符串作为来源证据。

代码发布（Fedora）：

```text
/home/chabking/ani-installer-runs/fix-apt-20260918/ani-code-aptfix-r11
kk SHA256: 18cf5ae6905649afd1017c040f8a77633cf835114670244a28b7463a24010007
```

继续复用 artifact（Fedora）：

```text
/home/chabking/ani-installer-runs/platform-20260918/run/ani-artifact-ubuntu24-amd64-20260918-r2
```

## 全新离线安装

- 项目还原脚本成功还原 VMID 5/6/7 的 snapshotId=1，三台均完成干净状态和空盘检查；其它虚拟机未操作。
- 三台应用既有断网规则，安装前后公网 DNS/HTTPS 均失败、管理 SSH 正常。
- `.20` 同时承担 installer、临时 Hauler 仓库及集群节点。
- 从 `.20` 以 root 执行一次修正版安装：

```bash
/opt/ani-installer/code/ani-code-aptfix-r11/kk ani install \
  --config /opt/ani-installer/site/cluster.yaml \
  --package-root /opt/ani-installer/artifacts/ani-artifact-ubuntu24-amd64-20260918-r2
```

- 开始：10:14:32；结束：10:24:14，Asia/Shanghai；耗时 9 分 42 秒。
- `INSTALL_EXIT=0`；413 total / 403 success / 10 ignored / 0 failed。
- 系统依赖、NTP、Kubernetes、kcn、Envoy、Ceph 均由 installer 完成，无现场补装。
- 安装内置的 RBD/CephFS provisioning、写入、读回验证任务通过。
- 独立 `verify.sh` 10:24:53～10:25:26 通过，`VERIFY_EXIT=0`：28 镜像、3 Ready 节点、新的网络/Envoy 请求通过。
- 三台 chrony、socat、conntrack、ipset、ipvsadm 均为 installed，chrony 服务 active。独立 ebtables 包未安装；原生任务按 `ebtables -L` 命令可用性决定是否安装该包，不能把此结果写成“依赖列表中每一个 deb 均已安装”。

## 最终状态与边界

- Ubuntu 24.04.4，内核 6.8.0-139-generic，Kubernetes v1.35.8，containerd 2.3.4。
- `ani-block`（默认 RBD SC）、`ani-cephfs` 已创建。
- CephCluster、CephFilesystem `ani-fs`、CephObjectStore `ani-store` 均 Ready。
- 3 MON quorum，3 OSD up/in，观察时 265 PG 全部 active+clean。
- Ceph 仍为 HEALTH_WARN：AES 认证相关告警以及 PG 数超过建议阈值（265 > 250）。未修改组件规避告警；安装通过不等于 HEALTH_OK 或生产验收。
- 当前仍为已有网络方案，不包含 ens36 专用存储网验证。
- S3 API 读写、故障切换、整群断电恢复、本轮未验。

## 证据

Fedora：`/home/chabking/ani-installer-runs/fix-apt-20260918/`。

包括 `restore.log`、`clean-*.log`、`isolation-before.log`、`isolation-after.log`、`transfer.log`、`tests.log`、`tests-builtin.log`、`build.log`、`install.raw.log`、`install.clean.log`、`verify.log`、`final-state.log`、`os-*.log`。

node1：`/var/log/ani-install-run.log`、`/var/log/ani-verify-run.log`、`/var/lib/ani-installer/ani-lab/logs/`。现有成功集群保留，临时镜像仓库和断网规则保留。

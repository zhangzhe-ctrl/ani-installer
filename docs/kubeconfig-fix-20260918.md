# kubeconfig 入口与导出修复

## 问题与修改

此前手动安装在 `Init | Add worker label to node` 访问 localhost:8080 失败。现场 `/etc/kubernetes/admin.conf` 和 `/root/.kube/config` 存在，显式使用 admin.conf 查询 API `/readyz` 返回 ok。通过 sudo -E 保留 HOME=/home/ubuntu 时，kubectl 找不到默认上下文；HOME=/root 时正常。

- ANI 调用 KubeKey create cluster 的子进程固定 `KUBECONFIG=/etc/kubernetes/admin.conf`，覆盖调用方遗留变量，不依赖 HOME。直接运行 kk ani install 和通过 install.sh 启动都进入这一逻辑。
- Init 和 Join 的控制节点导出任务读取 `connector.user`（ANI inventory 来自 `ssh.user`），通过 getent 查询真实 home、UID、GID，不再依赖 SUDO_USER 或拼接 `/home/用户名`。
- root 及指定管理用户的 `.kube` 目录使用 0700，config 使用 0600，管理用户文件使用其真实 UID/GID。找不到用户时失败，不假装完成导出。
- 导出的是集群管理员 kubeconfig，仅对配置指定的管理用户执行；不向普通 worker 分发管理员凭据。

## 验证

在 Fedora 隔离源码副本运行：

```bash
python3 scripts/test-kubeconfig-export.py
go test -p 4 ./pkg/ani ./pkg/connector
go test -p 4 -tags=builtin ./pkg/ani ./cmd/kk/app
```

均通过。覆盖 root/Ubuntu HOME、外部错误 KUBECONFIG 的覆盖；Init/Join 的自定义 home、0600/0700、属主、错误 SUDO_USER 和不存在的用户。导出测试使用临时目录，没有改真实用户的 kubeconfig。

独立代码包已在 Fedora 构建并通过 SHA256SUMS 和 help 检查：

```text
/home/chabking/ani-installer-runs/fix-kubeconfig-20260918/ani-code-kubeconfig-r12
kk SHA256: 288574d20507a58c3431b9fbb8d0fa41bc73493663092af080020ce1caa84dbd
```

继续使用 `ani-artifact-ubuntu24-amd64-20260918-r2`，无需重打 artifact。

**本轮仅修改、测试和构建；未还原虚拟机、未补写现场 kubeconfig、未重跑安装、未替换目标机 r11，也未提交推送。r12 的普通用户发起全新离线安装仍待真实验证。** 下一轮必须从三台干净快照验证，不能在当前失败现场重复执行安装。此前传输脚本固定引用 r11，使用 r12 时需更新代码包来源和目标目录。

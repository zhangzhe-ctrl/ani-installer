# B5 实测复核：blocked（2026-09-19）

## 结论

**B5 未通过，当前不能宣布基础组件第一轮全部完成。**
本次通过 Fedora 核对当前源码和 B4 产物，并在保留的 `.20/.21/.22` 集群执行正式独立验证。
`verify.sh` 退出 1：cert-manager、PostgreSQL、Valkey 功能通过，NATS StatefulSet 未就绪。
未修改产品代码，未重新安装、还原快照、删除服务 Pod、重启网络组件或放宽探针。

后续已定位为 **kcn 的迟到 CNI DEL 误删同名 Pod 新 sandbox 网卡**，详见下文。用户明确表示新版 kcn 已修复，要求本次只记录原因，后续自行安排批量验证。因此停止修复和新镜像验证；新版修复状态为 `user_reported_fixed / not_verified`，B5 保持 `blocked / verification_deferred_by_user`。临时新增的 kcn 回归测试已撤回，kc-networking 工作区恢复干净。

用户确认前一任务结束并授权释放遗留实验锁；本次释放原 PID 929521/929563 后持有同一锁执行验证，结束时释放本次锁，保留失败现场。

## 输入与产物

- 本地 Git：`e9c0a5c`，开始时工作区干净。
- 本次源码副本：`fedora:/home/chabking/ani-installer-runs/b5-20260919/source/`。
- B4 构建源码：`fedora:/home/chabking/ani-installer-runs/foundation-20260918/src/kubekey/`。
- 对比 594 个运行代码/构建输入文件：532 个仅 CRLF/LF 不同，未发现缺失或实质内容差异。这不等于可复现构建证明；嵌入 shell 仍必须沿用 Linux LF 构建流程。
- Fedora 代码包：`/home/chabking/ani-installer-runs/foundation-20260918/releases/ani-code-20260918-b4`；节点代码包：`/opt/ani-installer/code/ani-code-20260918-b4`。
- 实际 kk SHA256：`84dc71a2c9ae569612aff208a7d220238bcebfb3111c0203f9209b414db15f00`。Fedora 构建输出、代码包、`.20` 二进制相同，代码包四项校验通过。**历史 B4 文档中的 `256c0e1e…` 与现存包不同**；未查到独立证明历史安装时二进制摘要的完整记录，不能补造或继续使用旧摘要描述当前包。
- artifact：`fedora:/home/chabking/ani-installer-runs/foundation-20260918/releases/ani-artifact-ubuntu24-amd64-20260918-b4`。14 项 SHA256SUMS 全部通过；`SHA256SUMS` 文件本身 SHA256 为 `b548fde08a518bd0020723cba963e12eb70feb901e014502a1357f792725744b`。目录没有发现名为 kk 的文件。代码包部署脚本和 artifact 组件锁与当前源码一致（统一换行符比较）。
- 节点私有配置：`/opt/ani-installer/site/cluster.yaml`，SHA256 `eb6c74f55172680311f618d30363ae43d0551ee9a1c467fb28c8bb38f2def940`。未展示或提交凭据。

## 本次实际验证

| 项目 | 结果 | 证据/限制 |
| --- | --- | --- |
| `go test ./pkg/ani` | pass | 当前源码，Fedora 执行 |
| `go test -tags=builtin ./pkg/ani ./cmd/kk/app` | pass | ANI 测试通过；app 无测试文件 |
| 代码包/artifact 完整性 | pass | Fedora 全项校验；节点二进制一致；正式 verify 的节点 artifact 检查亦通过 |
| 三台断网隔离检查 | pass | 公网域名/IP HTTPS 失败、DNS 失败、管理 SSH 正常、ANI-OFFLINE 存在 |
| Kubernetes | pass | 三节点 Ready，v1.35.8/containerd 2.3.4 |
| 普通 ubuntu kubeconfig | pass | 目录 0700、文件 0600、owner ubuntu；未设置 KUBECONFIG 时 get nodes 成功 |
| 既有网络/Envoy smoke | pass | 正式主动测试通过，不代表所有 Pod 网络正常 |
| cert-manager | pass | CA/叶子证书链和 SAN 的实际验证 Job 通过 |
| PostgreSQL | pass | Service DNS 上应用用户 CRUD、错误密码拒绝、PVC Bound |
| Valkey | pass | Service DNS 上认证 SET/GET、TTL、未认证拒绝、PVC Bound |
| NATS | fail | StatefulSet 未就绪；首次采集 Pod 1/2，主容器重启 30 次 |
| 当前 B4 集群 PG/Valkey 正常 Pod 重建持久化 | not_verified | 历史 B2/B3 是各自集群；发现网络阻塞后未继续删除服务 Pod |
| 当前 NATS 消息/持久化复验 | blocked | 未就绪；历史成功不能覆盖当前失败 |
| 连接说明自动生成 | missing | runtime 下没有 connections.md；源码亦未定位到实现 |
| 最终人工复现文档 | incomplete | 现有 runbook 仍主要使用 B1 包和脚本 |

正式验证等价命令（`.20` 上，无手动 KUBECONFIG）：

```bash
sudo bash /opt/ani-installer/code/ani-code-20260918-b4/verify.sh \
  /opt/ani-installer/site/cluster.yaml \
  /opt/ani-installer/artifacts/ani-artifact-ubuntu24-amd64-20260918-b4
```

实际由 Fedora 现有 run_on_node.sh 经 sudo 执行，包装脚本保留真实退出码：

```text
foundation component verification failed; components: cert-manager=pass postgresql=pass valkey=pass nats=fail
B5_VERIFY_EXIT=1
RUNNER_EXIT=1
```

## NATS 故障的已证实范围

- Pod `ani-platform/nats-0`，UID `dae989b4-61bb-4402-8189-fc11d8a6a44c`，node2，IP `10.16.0.86`，与 B4 最终持久化日志中的重建后 UID 相同。
- reloader 就绪且无重启；nats 主容器未就绪，前次退出码 0、Completed，没有 OOM 证据。
- 事件明确记录 startupProbe 访问 `http://10.16.0.86:8222/healthz` 超时并触发 kubelet 重启。
- NATS 日志显示 JetStream 恢复消息/consumer、Server is ready，之后收到终止信号正常退出。
- 容器内访问 `127.0.0.1:8222/healthz` 与自身 Pod IP 的 healthz 均返回 `{"status":"ok"}`。
- 三台宿主机访问同一 Pod 的 healthz 均在 5 秒检查中超时（curl 28）；没有 Kubernetes NetworkPolicy。

初步定位为宿主机到 Pod 的网络路径；随后通过 kcn CNI 日志与 OVS/CR 状态确认了以下具体原因。没有调整隔离规则或通过重启/重建获取通过结果。

### 最终根因：迟到的旧 sandbox DEL 误删新 NIC

以下均为 2026-09-19，CNI 日志时间为 UTC+08:00：

1. **02:47:58**：旧 sandbox `5bc5a6f874dc…` 收到正常 DEL，旧 NIC `nic-ani-platform-nats-0-5cntj` 删除。
2. **02:47:59**：新 sandbox `d00e1ec6c583…` 收到 ADD，创建新 NIC `nic-ani-platform-nats-0-9w4zn`，对应 vNIC `ani-platform/auto-nats-0-vnic-lllst`、IP `10.16.0.86`。
3. **02:48:33**：运行时再次发来 **旧 sandbox `5bc5a6f874dc…` 的 DEL**，此时 `net_ns` 为空。紧接着日志显示被删除的却是 **新 NIC `nic-ani-platform-nats-0-9w4zn`**。
4. 现场新 Pod/网络命名空间和宿主机 veth `d00e1ec6c583_h` 仍存在，但 OVS 中找不到该 Interface；新 vNIC 的 `status.nic`、`status.nodeName` 已清空。Pod 进程仍运行且容器内 healthz 正常，外部访问失去数据路径。

源码核对位置：`/home/chabking/workspace/kc-networking/internal/daemon/cniserver/handler.go`（核对时本地 HEAD `a224588`）：

- `handleOvsDel` 按 namespace/name 取当前 Pod，构造参数后调用 `deleteNicCR`。
- `deleteNicCR` 仅按 Pod namespace/name + interface 的索引列出 NIC，并逐一删除；**未使用请求的 containerID/netns 核对 sandbox 归属**。
- `internal/daemon/networking/nic_controller.go` 的删除处理随后调用 OVS `del-port`，解释了新 veth 仍在但 OVS 端口消失的状态。

这是 **kcn 处理重复/迟到 DEL 时的实例身份隔离缺陷**，不是 NATS 版本、Ceph、installer 安装顺序或健康探针本身的问题。本地源码核对是对现场日志的解释，不代表已独立证明运行镜像的构建 commit。运行时为什么重复发出 DEL 不是本次修复前提；DEL 不应删除其它 sandbox 的资源。

用户已告知新版 kcn 修复，**本次未获取新版镜像、未验证修复、不回补旧版 kcn、不在 installer 中加补丁**。该结论只覆盖本次 NATS 故障；不据此把所有历史 K-5 现象的根因一并定案。

## 历史证据和剩余交付

B4 a2 restore 日志确有 VMID 5/6/7、snapshotId=1、3/3；安装日志含普通 ubuntu 入口、INSTALL_EXIT=0、474/464/10/0。
但 watcher 原记录 persist_rc=1，最终持久化是后续单独执行的 b4a2-persist-final.log，不能描述成首次 watcher 全绿。最终日志含 B4-PERSISTENCE-OK，也含调用不存在的 b4-heal.sh 的错误（被忽略）；没有证据显示该次实际成功执行 heal，但此实验脚本不能原样复用为严格验收。历史瞬时成功不构成当前 B5 通过。

后续完成条件：

1. 用户后续指定已修复的新版 kcn 材料和摘要；本次不再排查/修复旧版，也不往 installer 中加清理/重启绕过。
2. 统一源码、构建记录、最终代码包和摘要；补齐连接说明及使用真实最终路径的 runbook。不能照 B1 文档改开关后直接重跑已有集群。
3. 用户后续安排批量验证时，再依实验锁从三台快照安装，记录实际二进制摘要、断网和退出码；完成四组件功能及 PG/Valkey/NATS 正常 Pod 重建持久化，不调用 heal、不吞错。特别检查旧 sandbox 的迟到/重复 DEL 不影响新 Pod 网卡，不能只检查重建瞬间的 Ready。
4. 以上通过才将 B5 改为 pass；本次不扩大到 HA、升级或其它组件。

## 证据位置

Fedora：`/home/chabking/ani-installer-runs/b5-20260919/evidence/`：

- initial-state.log、nats-ready.log、nats-diag.log、nats-paths.log、node-paths.log
- net-node2.log、ovn-inventory.log、ovn-ports.log、nats-binding.log（迟到 DEL 与误删新 NIC 的关键原始日志）
- verify.log、isolation.log、artifact-check.log
- source-compare.json、provenance.json、go-test.log、go-test-builtin.log

节点正式验证目录：`/var/lib/ani-installer/ani-lab/logs/verify-20260919-102309-1375172-18529/`。
历史记录：`fedora:/home/chabking/ani-installer-runs/foundation-20260918/evidence/b4a2-*`。

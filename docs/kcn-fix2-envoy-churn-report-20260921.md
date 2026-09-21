# kcn fix2 dev：安装期 envoy-gateway Deployment 换代 pod 网络死亡（OVS 流表证据） — 2026-09-21

> **一句话摘要**：kcn fix2 dev（amd64 manifest sha256:05ea46f8…）安装失败于
> `ANI Envoy | Wait for controller`（exit=1）：envoy-gateway 因 rollout restart 产生两个
> RS 换代 pod，双双陷入"宿主→pod 无 ARP / 探针超时 / liveness 反复杀容器 / 进程本身活着"
> 的网络死亡，容器退出原因为 pod→apiserver(10.96.0.1) i/o timeout。OVS table=79 反欺骗
> 流表显示：pod 自身流量正从 ovn0（宿主网关，端口 2）回进 br-int，被按伪造源 drop，
> 其中 10.16.0.4 规则 idle_age=0 持续命中（另一 pod 138–169s 间歇命中）。
> 上轮同站点同安装链同点位（old dev 5a8cf98f）通过；本轮唯一大变量 = kcn fix2 镜像 + 新
> install-dev.yaml。以下按事实/假设严格区分，供上游定位参考。
>
> **局限性声明**：本事件为单节点（node2）单工作负载（envoy-gateway）单轮安装（n=1）的
> 单次观测，尚未成功复现第二例（复现配方见 §8）；关键流表证据为两个时间点（13:47/13:52
> UTC）采样。

---

## 1. 环境与材料

| 项 | 值 |
|---|---|
| 节点 | node1=172.16.101.20（装机+registry:5000）、node2=172.16.101.21、node3=172.16.101.22 |
| 系统 | Ubuntu 24.04，k8s v1.35.8，containerd 2.3.4 |
| kcn 镜像 | `172.16.101.20:5000/kubercloud/kc-networking:dev`，amd64 manifest sha256:05ea46f8cdcb033158cd0c80eac41b88a63eeb87ee4e9f5823be97c94171563a（本地 tar 供料；全部 12 个 kcn workload 实际运行该 digest，`kcn-imageids.txt`） |
| envoy-gateway 镜像 | `172.16.101.20:5000/kubercloud/gateway:v1.8.3`（sha256:3c3b5b61…，`envoy-pod-879nr.txt`） |
| 站点参数 | podCIDR 10.16.0.0/16，serviceCIDR 10.96.0.0/16，managedDevices=[ens35]，encapNetworks=[172.16.101.0/24]，intranetNetworks=[172.16.101.0/24,10.96.0.0/16]（`site-network.yaml`） |
| 配置注入 | 这 3 项旧版经 ConfigMap `kcn-config` 注入；**上游 fix2 dev 清单已删除该 ConfigMap**，我方 installer 本地恢复插回（`kcn-install-excerpts.txt:4239`）；fix2 新 kcn 是否仍读取它**未确认** |
| fix2 新增 CRD | learnedroutes / transitrouters / vpcattachments（新 install-dev.yaml） |
| 对照组 | 2026-09-20 kcndev-a5 轮（old dev=5a8cf98f 世代，manifest 048cafa8…）：同站点、同安装链、同点位（Envoy wait）**通过**，推进至 fluent-bit 校验步才失败（`timeline.md` §3） |
| 本轮 | 2026-09-21 kcndev2-manual 轮：Envoy wait **失败**，安装 exit=1 |

**与上轮唯一大变量**：kcn fix2 镜像 + 新 install-dev.yaml（+3 CRD、删 kcn-config ConfigMap）。envoy-gateway 镜像、helm chart、站点参数均未变。

## 2. 现象时间线（全部 UTC，2026-09-21）

| 时刻 | 事件 | 证据 |
|---|---|---|
| 12:40:32 | 安装发射（node1 `ANI install started`） | `timeline.md` §1–2 |
| 12:44:46–12:45:27 | KCN \| Wait for controller **通过**（41s，与 09-19 轮 44s 相当） | `timeline.md` §2/§4 |
| 12:45:36 | deploy/envoy-gateway 创建（gen=1），RS `677c8686c8` pod **879nr**（IP 10.16.0.4）created | `envoy-pod-annotations.txt` |
| 12:45:37–38 | Envoy \| Restart controller → gen=2，RS `7769d88549` pod **fcwq5**（IP 10.16.0.6）created；Wait for controller 开始（timeout 5m0s） | `timeline.md` §2 |
| ≤12:46:0x（推定） | 首次 CrashLoop：容器每实例存活约 30s，退出原因 `dial tcp 10.96.0.1:443: i/o timeout`（首退时刻被日志轮转回收，按 30s 周期推定） | `envoy-pod-fcwq5.txt:98` |
| 12:45:36 起 ≈9m43s 窗口内 | 早期 kubelet 事件：FailedMount `secret "envoy-gateway" not found` ×7（secret 由 gen1 pod 所在 install 流程补齐后消失；事件事后已轮转，未落盘，为主 agent 安装期实测） | 主 agent 实测 |
| 12:50:38 | rollout 超时：`1 old replicas are pending termination` + `error: timed out` → **安装 exit=1** | `timeline.md` §2 |
| 13:47–13:52（事后取证） | 两 pod 仍 CrashLoop：restarts 22/19，liveness Killing ×14/×13，readiness 失败 ×120/×82，持续恶化；两 RS desired 仍均=1（旧 RS 未缩容） | `pod-snapshot.txt`、`envoy-pod-879nr.txt`、`envoy-pod-fcwq5.txt` |

## 3. 症状四件套与连通性矩阵

**四件套**（两 pod 一致）：

| 症状 | 实测 | 证据 |
|---|---|---|
| 宿主→pod 死 | node2 宿主 ping 10.16.0.4/10.16.0.6 100% 丢包，`ip neigh` 两 IP 均无 ARP 应答（宿主路由 `via 100.64.0.1 dev ovn0`） | `host-net.txt` |
| 探针超时 | liveness/readiness 均 `context deadline exceeded`（非拒绝、非 5xx——包根本到不了），liveness 杀容器持续不愈 | `envoy-pod-879nr.txt:84,89` |
| 换代不愈 | 容器换代 22/19 次、RS 滚动替换 1 次（879nr→fcwq5），症状原样复制到新 RS 新 pod | `pod-snapshot.txt` |
| 进程活着 | 进程正常启动、runner 全起、优雅关闭日志正常；存活期内至少一次成功当选 leader lease（13:52:00.435 acquired，`envoy-pod-879nr.txt:155`；更早的当选记录见于主 agent 安装期 events 实测，已随事件轮转未落盘）——纯网络层死亡 | `envoy-pod-879nr.txt:155` |

**三向连通性矩阵**（13:4x–13:5x 实测，原始输出 `connectivity-matrix-raw.log`）：

| 方向 | 结果 |
|---|---|
| node2 宿主 → 10.16.0.4/.6 | 死（无 ARP 应答，100% 丢包） |
| pod → 10.96.0.1（apiserver） | 死（i/o timeout，每实例 30s 后退出） |
| pod → pod（node2 全新 busybox → 10.16.0.6） | 通（0.6ms） |
| 跨节点（busybox → 10.16.0.2，node1 pod） | 通 |
| node1 全部 pod（coredns 双副本等） | 全部健康 |
| node2 STS busybox（k5sts-0）原地重建 ×2 代 | 未复现 K-5 症状（见下方注） |

> **关于 STS 行的口径**：该实验为 busybox 无 PVC、1 副本、node2 定点，两代 delete 重建各
> 观察 10 分钟（各 60 次采样全部 Ready=True、restarts=0，前后流表 diff 无针对 pod 的
> 新增 drop，原始记录 `../kcndev2-k5-sts-direct/k5-sts-ev/` 与
> `../kcndev2-k5-sts-direct/sts-direct-run-stdout.log`）。它只能说明 K-5 的"槽位反欺骗
> 残留误杀"机制在本轮 fix2 的**该简化形态**上未触发，与真实故障场景（loki：PVC 挂载 +
> memberlist 服务的 StatefulSet）存在差距，不能直接外推为"K-5 已修复"；K-5 的 loki 形态
> 验证需待安装链修复后按 runbook 全链进行。

即：node2 数据面本身可用，**病灶特定于 envoy 两个 pod 的生命周期**（创建→早期 FailedMount→liveness 反复杀/换代→RS 滚动替换），非节点级、非全局策略级。

## 4. OVS 流表证据（node2 kcn-ovs-ds pod 内，br-int）

**关键流原文**（13:47:26Z，`t79-active-drops.txt`）：

```
cookie=0xac875ac4, duration=3644s, table=79, n_packets=7348, idle_age=5,
  priority=100,ip,reg14=0x2,metadata=0x3,dl_src=fa:00:1c:36:e1:57,nw_src=10.16.0.4 actions=drop
cookie=0x45c11c24, duration=3643s, table=79, n_packets=5402, idle_age=169,
  priority=100,ip,reg14=0x2,metadata=0x3,dl_src=fa:00:23:85:9e:bd,nw_src=10.16.0.6 actions=drop
cookie=0xf07595ce, duration=3604s, table=79, n_packets=3306, idle_age=5,
  priority=100,ip,reg14=0x1,metadata=0x3,dl_src=4e:86:5d:ab:56:39,nw_src=100.64.0.4 actions=drop
```

要点（均为流表计数实测，非推测）：

1. **reg14=0x2 = ovn0**：`ovs-ofctl show br-int` 端口表 port 2 = ovn0（`port-map.txt`）；ovn0 是宿主网关口（node2 宿主 `100.64.0.4/17`，`host-net.txt`）。即这些 drop 规则只对"从 ovn0 进入 br-int、且源 MAC/IP 冒充该 pod"的包生效——反欺骗逻辑本身正常。
2. **两条中毒规则持续命中**：10.16.0.4 规则 n_packets 7348→7804（13:47→13:52，idle_age=0/5）；10.16.0.6 规则 5402→5444。说明**确有源地址=这两个 pod 的流量持续从 ovn0 回进 br-int**——pod 自己的流量没有走自己的 veth 端口（ofport 4/5），而是落到了宿主栈、经 ovn0 绕回，被当作伪造源丢弃。13:52 复采时 10.16.0.6 命中趋缓（idle_age=138），与该 pod CrashLoopBackOff 退避节奏吻合。
3. **同型规则健康 pod 全为 0**：node2 同表内其余全部 pod（10.16.0.2/.3/.5/.7/.8…及 k5sts-0=10.16.0.14）的 ovn0 反欺骗 ip 规则 n_packets=0（个别历史计数 ≤2）。仅两个 envoy pod 中招。
4. **table=26 正常逻辑 egress 流 n_packets=0**：两 pod 的 `priority=24000,ip,reg15=0x1,…,nw_src=10.16.0.4/.6` 正常 egress 流零命中（`t26-envoy.txt`）——即 pod 从自身 veth 端口（reg14=0x4/0x5）进入的正常转发路径**从未发生**，与第 2 点互为印证。
5. 附带：100.64.0.4（=node2 宿主 ovn0 自身地址）从 reg14=0x1（ovn-encap 口）冒充进入被 drop 3306 次，与"pod 流量经宿主栈走 geneve 绕回"的图景一致，一并附上供参考。

## 5. 沙箱-veth 端口映射取证结论

为验证"pod 重建竞态导致沙箱 veth 与 OVS 端口失联"（假说 A 的可检验预测），13:49 做了三方核对（`sandbox-map.txt`、`ovs-iface-external-ids.txt`、`pod-netns-detail.txt`）：

| 核对项 | 879nr (10.16.0.4) | fcwq5 (10.16.0.6) |
|---|---|---|
| OVS Interface 存在且 ofport | eecab2f6d08a_h = 4 | c5bb1874eb92_h = 5 |
| external_ids: ip / pod_name / pod_netns | 全部正确，`ovn-installed=true` | 全部正确，`ovn-installed=true` |
| pod_netns 与 crictl sandbox NetNSPath | 一致（cni-055e78d2-…） | 一致（cni-9e703f15-…） |
| 链路状态 | admin/link up，driver=veth | admin/link up，driver=veth |
| Interface statistics | rx 7390 / tx 6459（双向有量） | rx 5483 / tx 4803 |
| netns 内 eth0 | 10.16.0.4/16 up，MAC fa:00:1c:36:e1:57 与流表 dl_src 一致 | 10.16.0.6/16 up，MAC fa:00:23:85:9e:bd 一致 |

**结论（事实）**：OVS 端口**并未缺失**，external_ids/netns/MAC 三方完全一致，veth 双向计数非零。"端口丢失"这一具体形态**未获支持**；但流表计数证明流量实际路径确与端口归属矛盾（见 §4.2/4.4），路径级异常仍在。PodReconciler/CNIServer 侧日志显示两 pod 创建时 vnic 分配、bind、"Successfully set up vnic resources" 均正常无错（`kcn-controller-logs.txt` 12:45:36/38 段，`kcn-cni-ds-node2.log` 12:46:40/41 add port request 段）。

## 6. 假说与证据对照（A/B/C，均未证实；支持与矛盾如实列出）

### 假说 A：pod 重建/换代竞态导致沙箱 veth 与 OVS 端口失联（流量落宿主栈、经 ovn0 回进 br-int 被反欺骗丢弃）

- **支持**：t79 中毒规则持续命中 + t26 零命中（§4.2/4.4）客观证明"pod 流量经 ovn0 绕回"；病灶与 pod 生命周期事件（创建/换代/反复重启）强相关，健康 pod 同型规则全零。
- **矛盾/修正**：端口映射取证显示 OVS 端口存在且参数全对（§5），"端口缺失"变体不成立；竞态若存在，应发生在更细的流表/转发路径层面（如端口注册时序、流表写 order、宿主路由/ARP 响应代理），需上游结合代码定位。
- **性质**：路径异常是流表计数实证；"竞态"归因是推测。

### 假说 B：上游删除 kcn-config 后，fix2 对 10.96.0.0/16（service CIDR，属 intranetNetworks）的 egress 策略变化，pod→apiserver 流量被改道 ovn0

- **支持**：容器退出原因恰为 pod→10.96.0.1 i/o timeout；本轮与上轮的 manifest 差异恰含 kcn-config 删除。
- **矛盾（如实记录）**：① leader election 间歇**成功**（存活期内含 13:52:00.435 一次当选，成功当选需要可达 apiserver），与"→10.96.0.1 全断"矛盾——更像按包/按流级别的路径抖动而非稳定策略改道；② node1 全部 pod、node2 其他 pod（含同样访问 apiserver 的 coredns）全部正常，若是全局 service CIDR egress 策略变化不应只打中两个 envoy pod；③ busybox→10.16.0.6（pod→pod 同网段）通，说明 not 全局 egress 死。
- **性质**：推测，且现有矛盾较多；但我们无法确认 fix2 新 kcn 是否读取了本地恢复的 kcn-config（见 §7 问题 1）。

### 假说 C：与我方 installer 侧合并错误有关

- **可排除项（已核验）**：kcn-config 注入点与 image 引用均已核验无站点硬编码残留（12 个 kcn workload 运行正确 digest，`kcn-imageids.txt`）。
- **如实写明**：ConfigMap kcn-config 的本地恢复属于**超出上游 fix2 dev 清单预期的本地状态**（上游已删、我方插回，`kcn-install-excerpts.txt:4239-4256`）。若 fix2 代码已改为从别处读取这三项参数，本地恢复的 ConfigMap 理论上应是惰性冗余；但它确实在集群中存在，作为变量如实申报，请上游一并判断。

### 假说 D：早期 FailedMount（secret "envoy-gateway" not found ×7）才是根因，kcn 无辜

- **支持排除的证据**：① FailedMount 只阻断 volume 挂载，不影响网络面配置路径；② secret 补齐后两 pod 的容器均能正常 Created/Started（`envoy-pod-879nr.txt`/`envoy-pod-fcwq5.txt` 事件段），即挂载问题已消失，但网络死亡持续存在直至 13:52 取证（restarts 22/19 仍增长）——症状在 FailedMount 终止后依旧持续，它无法解释死亡的持续性；③ 全新 busybox pod 在同一节点、同一时段挂载/网络全部正常。
- **不能排除的部分（如实记录）**：FailedMount 窗口（12:45:36 起约 10 分钟）与死亡起点强耦合；若 kubelet 因挂载失败反复重建容器/触发 RS 滚动，恰好制造了假说 A 所需的高强度换代场景，则 FailedMount 更可能是**诱因**而非根因（A 为机制、D 为触发器，两者可叠加）。注意 FailedMount 事件本身已随 events 轮转未落盘（×7 为安装期实测），此假说无法用归档证据完全闭环。

## 7. 请上游确认的问题清单

1. fix2 dev 是否仍读取 ConfigMap `kcn-config`（kcn-system）中的 encapNetworks/intranetNetworks/managedDevices？若已废弃，这三项现在从何处配置（CR？flag？默认值）？我方本地恢复插回的 ConfigMap 是否可能干扰？
2. 新 install-dev.yaml 新增的 learnedroutes/transitrouters/vpcattachments CRD 是否改变了对 service CIDR（10.96.0.0/16）的 egress/路由策略？
3. pod 重建路径（Deployment 换代、容器反复重启）中 veth/OVS port/流表的生命周期管理是否有已知竞态？特别是：什么条件下会出现"pod 流量从 ovn0 回进 br-int"（t79 反欺骗按 pod MAC/IP 命中数千包）而该 pod 自身 veth 端口的正常 egress 流（table=26）零命中？
4. table=79 反欺骗规则的 cookie 生命周期：pod 删除/换代后旧规则是否可靠回收？（本轮两 pod 规则与 pod 一一对应、无跨代残留，与 K-5 的"槽位残留"形态不同，供区分。）
5. 容器每实例约 30s 即因 `dial tcp 10.96.0.1:443: i/o timeout` 退出、但 leader election 又能间歇成功——上游是否见过"按连接建立时机决定通断"的路径问题？

## 8. 我方可复现步骤（快照还原→全链安装，同 K-5 手册）

```bash
# 在 fedora 跳板（172.16.101.x 唯一入口）：
bash ~/ani-ops/restore_esxi_snapshots.sh execute && sleep 20
cd ~/ani-installer-runs/obs-live-20260919
CODE_RELEASE=ani-code-kcnfix2-20260921 \
ARTIFACT_RELEASE=ani-artifact-kcnfix2-20260921 \
  bash lab/run-attempt.sh kcndev2-manual loki-cumulative 2>&1 | tee ~/kcndev2-manual-run.log
bash lab/poll-attempt.sh   # 预期：ANI Envoy | Wait for controller 5m0s 超时，exit=1
```

流表取证（node2）：

```bash
OVS=$(kubectl -n kcn-system get pod -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.nodeName}{"\n"}{end}' | awk '$2=="node2" && /ovs-ds/{print $1}')
kubectl -n kcn-system exec $OVS -- ovs-ofctl dump-flows br-int table=79 | grep -E 'n_packets=[1-9]'
kubectl -n kcn-system exec $OVS -- ovs-ofctl dump-flows br-int table=26 | grep -E '10\.16\.0\.(4|6)'
kubectl -n kcn-system exec $OVS -- ovs-vsctl list Interface            # external_ids 三方核对
```

## 9. 证据文件清单（fedora `~/ani-installer-runs/obs-live-20260919/evidence/`）

| 文件 | 内容 |
|---|---|
| `kcndev2-envoy-forensics/timeline.md` | 六步发射 + 安装内事件 + 上轮对照全时间线（UTC） |
| `kcndev2-envoy-forensics/t79-active-drops.txt` | table=79 活跃 drop 流原文（13:47:26Z 与 13:52:28Z 双采） |
| `kcndev2-envoy-forensics/t26-envoy.txt` | 两 pod 的 table=26 egress 流（n_packets=0） |
| `kcndev2-envoy-forensics/port-map.txt` | br-int 端口表（ovn0=port 2、两 pod veth=4/5） |
| `kcndev2-envoy-forensics/ovs-iface-external-ids.txt` | Interface external_ids/statistics 全文 |
| `kcndev2-envoy-forensics/sandbox-map.txt` / `pod-netns-detail.txt` | crictl sandbox↔netns↔OVS 三方核对、netns 内 addr/route |
| `kcndev2-envoy-forensics/envoy-pod-879nr.txt` / `envoy-pod-fcwq5.txt` | describe（restart/探针/Killing 计数）+ logs（10.96.0.1 i/o timeout、lease 当选） |
| `kcndev2-envoy-forensics/envoy-pod-annotations.txt` / `pod-snapshot.txt` | pod 创建时刻、vnic 注解、实时状态 |
| `kcndev2-envoy-forensics/host-net.txt` | node2 宿主链路/路由/neigh（ovn0、无 ARP） |
| `kcndev2-envoy-forensics/ovs-vsctl-show.txt` | br-int 拓扑（geneve 隧道、残留 error 端口系历史 pod 已清） |
| `kcndev2-envoy-forensics/kcn-controller-logs.txt`（raw 同名 .raw） | 两 pod 创建期 PodReconciler/VNic/分配全流程无错 |
| `kcndev2-envoy-forensics/kcn-cni-ds-node2.log` | node2 CNIServer add port / Nic CR 流程 |
| `kcndev2-envoy-forensics/kcn-imageids.txt` / `kcn-install-excerpts.txt` / `site-network.yaml` | digest 核验、kcn-config 本地恢复段、站点参数 |
| `kcndev2-envoy-forensics/connectivity-matrix-raw.log` | §3 连通性矩阵原始实测输出（fresh probe ping、宿主 ping、ARP） |
| `kcndev2-k5-sts-direct/k5-sts-ev/`、`kcndev2-k5-sts-direct/sts-direct-run-stdout.log` | STS 原地重建 ×2 代实验：观察日志（gen1/gen2 各 60 采样）、前后全量流表、运行 stdout |
| `kcndev2-manual-*.log`（同目录上层） | 发射六步与传输校验 |

---
*报告人：ANI installer 侧（本报告所有流表计数/日志行均出自上列证据文件原文，未做外推；推定项已单独标注）。*

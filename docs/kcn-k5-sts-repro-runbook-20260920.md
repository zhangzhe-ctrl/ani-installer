# K-5（STS 槽位重建网络死亡）手动复现手册 — 2026-09-20

> **一句话 bug**：pod 所在的 OVS 槽位残留上一代的防欺骗 drop 流表；对 StatefulSet 这类
> "同名同槽位原地重建"的 pod，新 pod 出生即被旧规则误杀——外部进不来（kubelet 探针超时）、
> 自己出不去（DNS UDP i/o timeout）。容器重启不愈（沙箱活、容器换代），只能整轮快照还原。
> v0.6.2 时代已存在（a17 实验实锤机制），kcn dev 修复了"新建 pod"形态（实测 9/9 干净），
> **未修复"原地重建"形态**（本手册复现目标）。
>
> 今日实测锚点：kcndev-a5，loki-0 连续 2 代重建死亡（gen2 出生即坏、gen3 Ready 82 秒后死），
> 后续 crash loop restart 计数 11+ 持续恶化。PVC/存储 IO 全程正常，纯网络层死亡。

---

## 1. 环境速查（全部为实测值）

### 1.1 节点与入口

| 项 | 值 |
|---|---|
| 节点 | node1=172.16.101.20（装机+registry:5000）、node2=172.16.101.21、node3=172.16.101.22 |
| 节点登录 | `ubuntu` 用户；密码文件 fedora `~/ani-installer-runs/platform-20260918/access/node-password`（配套 `askpass.sh`，SSH_ASKPASS_REQUIRE=force 用法见 §3） |
| ESXi 宿主 | 172.16.255.12；快照还原只动白名单三台（`~/ani-ops/restore_esxi_snapshots.sh`） |
| fedora 跳板 | 唯一可进 172.16.101.x 的机器；`ssh fedora` 直达 |
| node1 上 kubectl | `/usr/local/bin/kubectl`，ubuntu 免 sudo 可用 |
| kcn 安装产物（node1） | `/etc/kubernetes/ani/kcn-install.yaml`（已渲染 manifest） |
| 站点配置（node1） | `/opt/ani-installer/site/cluster.yaml`（源：obs-live `inputs/site-loki-cumulative.yaml`） |
| 节点安装目录 | `/opt/ani-installer/{code,artifacts,site}` |
| 本轮包 | code `ani-code-kcndev-20260920`（kk sha256 前缀 14cb327a）、artifact `ani-artifact-kcndev-20260920`（≈3.0G，18 条 sha，6 chart） |

### 1.2 kcn 网络参数（站点配置原文，实测渲染生效）

```yaml
network:
  managementInterface: ens34        # 节点管理网卡
  podCIDR: 10.16.0.0/16             # pod 网段（实测 pod IP 10.16.0.84-107）
  serviceCIDR: 10.96.0.0/16         # service 网段（CoreDNS ClusterIP=10.96.0.3）
  kcn:
    managedDevices: [ens35]         # kcn 接管的第二网卡
    encapNetworks: [172.16.101.0/24]        # Geneve 封装流量走节点网段
    intranetNetworks: [172.16.101.0/24, 10.96.0.0/16]
```

OVN 运行时实况（node1 实测）：

- 封装：`ovn-encap-type=geneve`，`ovn-encap-ip=<节点自身 IP>`
- SB 接入：`ovn-remote=tcp:[172.16.101.20]:6642,tcp:[172.16.101.21]:6642,tcp:[172.16.101.22]:6642`（三节点 raft）
- 节点网关口 `ovn0`（node1 实测）：`100.64.0.2/17` 与 `100.64.128.2/17` 双地址
- **OVS 不在宿主机**（宿主无 ovs-vsctl）——全部流表操作必须 exec 进 `kcn-ovs-ds` pod
- 反欺骗流表 `table=79` 实测 218 条（node2 的 ovs-ds pod）

### 1.3 kcn 工作负载（命名空间 `kcn-system`）

| 类型 | 名字 | 副本 | 说明 |
|---|---|---|---|
| deploy | kcn-controller | 3 | |
| deploy | kcn-ovn-central | 3 | command=`["bash","/kc-networking/start-db.sh"]`（dev 数组形；leader-checker 已上游化删除） |
| ds | kcn-cni-ds | 3 | **hostNetwork** |
| ds | kcn-ovs-ds | 3 | **hostNetwork**，OVS/OVN 本体在此 |
| svc | kcn-ovn-nb / kcn-ovn-northd / kcn-ovn-sb | — | 6641/6643/6642；dev 改名后无裸 `ovn-nb` |

- 统一标签：`networking.kubercloud.com/app={controller,ovn-central,cni-ds,ovs-ds}`
- 镜像：`172.16.101.20:5000/kubercloud/kc-networking:dev`
  （⚠️ images.tsv 目标列写的是 `127.0.0.1:5000/...`，安装器渲染时改写为安装节点 IP——
  **手写任何脚本必须用 `172.16.101.20:5000` 形式**，127.0.0.1 在 node2/3 上指向自己）
- 验证辅助镜像：`172.16.101.20:5000/library/busybox:1.37.0`、`172.16.101.20:5000/library/python:3.13.11-alpine3.23`

---

## 2. 全链复现（快照还原 → 安装 → 到达故障场景）

前提：fedora 可用、三台白名单虚机在 ESXi 172.16.255.12 上有干净快照。

```bash
# —— 以下全部在 fedora 上执行 ——

# [1] 快照还原三台（约 1-2 分钟；还原=节点重启）
bash ~/ani-ops/restore_esxi_snapshots.sh execute
sleep 20        # 等 sshd

# [2] 一条命令跑完整装机链（约 50 分钟）
cd ~/ani-installer-runs/obs-live-20260919
CODE_RELEASE=ani-code-kcndev-20260920 \
ARTIFACT_RELEASE=ani-artifact-kcndev-20260920 \
  bash lab/run-attempt.sh kcndev-manual loki-cumulative 2>&1 | tee ~/kcndev-manual-run.log
```

`run-attempt.sh` 内部六步（手工等价时按下述拆）：

1. **离线隔离** `bash ~/ani-ops/apply_offline_isolation.sh apply` → `verify`
   （⚠️ 还原=重启，临时 iptables 规则 `ANI-OFFLINE` 已被清空，**必须重打**，
   否则安装跑在有公网节点上，离线证明作废；隔离用 OUTPUT/FORWARD 链，不可用 blackhole 路由替代）
2. **解 dpkg 锁 ×3**（还原后 unattended-upgrades 占锁）：把
   `obs-live/src/kubekey/lab/foundation-bringup/preplock.sh` 推到三台 `/tmp/`，
   `sudo -S bash /tmp/obs-preplock.sh < node-password`
3. **三台时钟同步**（NTP 漂移会导致证书/令牌校验失败）
4. **建目录** node1：`sudo install -d -o ubuntu -g ubuntu /opt/ani-installer/{code,artifacts,site}`
5. **传输** code+artifact（≈3GB，tar 打包传输；⚠️ tar 不跟软链，源用 `cp -a` 真实拷贝）
   → node 上 sha256 全量校验（SHA256SUMS 两条：code 4 条 + artifact 18 条 = 22 行）
6. **站点配置 + 分离式安装**：
   `umask 077; cat > /opt/ani-installer/site/cluster.yaml < inputs/site-loki-cumulative.yaml`
   然后 `sudo install.sh /opt/ani-installer/site/cluster.yaml /opt/ani-installer/artifacts/<ART> &`
   （分离式：stdout→`/tmp/obs-install.stdout`，退出码→`/tmp/obs-install.exit`）

```bash
# [3] 轮询安装（在 fedora；exit 码落盘即结束）
bash lab/poll-attempt.sh     # 单次；或循环 until [ -f ] node1:/tmp/obs-install.exit
# 约 50 分钟；exit=0 才算装机成功
```

⚠️ **fail-loud**：安装失败会留下 runtime root `/var/lib/ani-installer/ani-lab` 并拒绝重装
→ 只能回 [1] 重新快照还原。**不存在"原地重试"。**

---

## 3. 最小 bug 复现（已装好的集群，约 15 分钟）

> 前提：刚完成 §2 的全新安装（此刻 loki-0 是"原装健康代"——a5 实测原装代健康 25+ 分钟）。
> 登录 node1：`ssh fedora` 后 `bash ~/ani-installer-runs/obs-live-20260919/lab/node.sh 'bash -s'`
> （stdin 传脚本），或直接 askpass ssh。下述命令均在 node1 上。

```bash
NS=ani-observability
OVS_POD=$(kubectl -n kcn-system get pod -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.nodeName}{"\n"}{end}' | awk '$2=="node1"{print $1}')
```

### 步骤 1：记录重建前基线

```bash
kubectl -n $NS get pod ani-loki-0 -o wide          # 记下 IP（记为 $OLD_IP）与 UID
kubectl -n $NS get pod ani-loki-0 -o jsonpath='{.metadata.uid}{"\n"}'
kubectl -n kcn-system exec $OVS_POD -- ovs-ofctl dump-flows br-int > /tmp/before-flows.txt
kubectl -n kcn-system exec $OVS_POD -- ovs-ofctl dump-flows br-int table=79 | wc -l   # 反欺骗基线条数
```

### 步骤 2：原地重建（触发点）

```bash
kubectl -n $NS delete pod ani-loki-0 --wait=true   # STS 数秒内同名重建
```

### 步骤 3：高频观察 Ready 翻转（关键证据，每 10s 一次、连续 5 分钟）

```bash
while :; do
  kubectl -n $NS get pod ani-loki-0 -o jsonpath="Ready={.status.conditions[?(@.type==\"Ready\")].status} restarts={.status.containerStatuses[0].restartCount} ip={.status.podIP} $(date -u +%T){"\n"}"
  sleep 10
done
```

**a5 实测预期**：新 pod Ready=True 维持约 **60–90 秒**后翻 False，此后不再恢复；
容器随后进入 crash loop（每代存活 ≈5 分钟：memberlist join 重试 4m18s×10 后退出）。

### 步骤 4：网络死亡三向判定（新 pod IP 记为 $NEW_IP）

```bash
# (a) 宿主→pod（入向；预期: 超时——HTTP 卡死无响应）
curl -m 5 -sv http://$NEW_IP:3100/ready

# (b) pod→外（出向 DNS；预期: i/o timeout）
kubectl run k5dns --image=172.16.101.20:5000/library/busybox:1.37.0 -n default --restart=Never \
  --overrides '{"spec":{"nodeName":"node1"}}' -- nslookup -timeout=3 kubernetes.default.svc.cluster.local 10.96.0.3
kubectl wait --for=condition=Ready pod/k5dns -n default --timeout=120s   # ⚠️ 此步预期失败=症状本身
kubectl logs k5dns -n default

# (c) 跨节点→pod（预期: 超时）
# 在 node2 上 curl -m 5 http://$NEW_IP:3100/ready
```

⚠️ loki 镜像 distroless 无 shell，不能 exec 进 loki 做探测——一律用旁挂 busybox probe。

### 步骤 5：OVS 流表验尸（定位残留规则）

```bash
kubectl -n kcn-system exec $OVS_POD -- ovs-ofctl dump-flows br-int > /tmp/after-flows.txt
diff /tmp/before-flows.txt /tmp/after-flows.txt | grep -E '^[<>]' | grep -iE 'drop|79' | head -40
# 重点：table=79 反欺骗段——找按 旧$OLD_IP / 旧MAC 记账的 drop 规则，
# 对照新 pod 的 in_port/新 IP 是否仍被旧 in_port 的规则覆盖（a17 机制：按 MAC/IP 钉死槽位）
```

### 步骤 6：恢复实验（判定 Form A / Form C）

```bash
# a17 配方：重启该节点两个 kcn 数据面 agent → agent Ready 后再删一次 loki-0
CNI_POD=$(kubectl -n kcn-system get pod -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.nodeName}{"\n"}{end}' | awk '$2=="node1" && /cni-ds/{print $1}')
kubectl -n kcn-system delete pod $CNI_POD $OVS_POD --wait=true
kubectl -n $NS delete pod ani-loki-0 --wait=true
# 等 5 分钟观察 Ready：
kubectl -n $NS get pod ani-loki-0 -w
```

**判定**：恢复 = Form A（残留可被 agent 重启配方清除）；仍死 = Form C（与 a5 实测一致——
只能整轮快照还原）。

### 判定标准总表

| 观察项 | 健康 | K-5 阳性 |
|---|---|---|
| 新 pod Ready | True 且持续 | True 数十秒后翻 False 永不恢复（或从未 True） |
| 宿主→pod:3100 | 立即应答 | 超时（无 RST，纯卡死） |
| pod→DNS 10.96.0.3 | 正常解析 | UDP i/o timeout |
| 容器重启能否自愈 | — | 不能（沙箱活，换容器无用） |
| PVC IO | — | 正常（`uploading tables` 日志正常打点）——存储无罪 |
| crash loop 日志 | — | `failed to resolve ani-loki-memberlist... i/o timeout` → `joining memberlist cluster failed` → `error running loki err="failed services"` |

---

## 4. a5 实测对照数据（2026-09-20，预期输出基准）

| 时刻(UTC) | 事件 |
|---|---|
| ~06:0x | gen1（安装原装）健康，服务 [1][2] 全部验证 |
| 06:20 | [3] delete#1 → gen2（.90）：探针 503/超时混合，06:25 liveness 杀容器，600s 从未 Ready |
| 06:31 | K5_RETRY → delete#2 → gen3（.91）06:30:36 创建 |
| 06:31:5x | gen3 Ready=True —— **仅 82 秒** |
| 06:32:00 | gen3 Ready→False，网络双向死亡；[4] 查询 300s 12 连 socket 超时 → 安装 exit=1 |
| 06:32 至今 | crash loop：restart 6→11+ 持续增长，每代存活 ≈5 分钟 |

细节证据：fedora `~/ani-installer-runs/obs-live-20260919/evidence/kcndev-a5/`
（`loki-postmortem-a5.txt`、`kcn-health-a5.txt`、`k5-repro-a5.txt`）。

**反向对照（kcn dev 修复生效的形态）**：`k5-repro.sh` 三轮 9 次 **cni-ds 重建 + 全新 probe pod**
全部干净（跨节点 ping OK）——即"新建 pod"路径无病，病灶仅在"原地重建"。

---

## 5. 铁律与坑（违反即重来）

1. 快照还原后必须重打离线隔离（apply+verify），否则破坏离线证明。
2. 三台都要 preplock 解 dpkg 锁。
3. 安装失败 = runtime root 残留 = 必须重新快照还原，无原地重试。
4. `images.tsv` 是打包物料，任何脚本禁写；集群内镜像引用一律用 `172.16.101.20:5000` 形式。
5. `kubectl rollout status` 在本集群 kubectl v1.35 有假通过前科——等待一律轮询 pod Ready condition。
6. hostNetwork pod（cni-ds/ovs-ds）按节点过滤用 jsonpath `{.spec.nodeName}`，勿用 `-o wide` 列号。
7. 白名单三台之外勿动；凭证只在 access/site 文件，勿入仓库、勿写脚本。

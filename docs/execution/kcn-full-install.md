# KCN full 统一首装合同与证据入口

本轮唯一状态在 [progress.yaml](progress.yaml) 的 `KCN-FULL-20261009`。
单一配置为 [kcn-full.yaml](../../config/examples/kcn-full.yaml)。目标仅为
172.16.101.20、21、22；本地处理源码/Git/传输，重任务均在 Fedora 172.16.101.31。
原始 `release` 基线为 `0f377f6ed7655c42d37e7da8777f92448311814d`。

主链为源码修复、同源正式 CLI 和完整离线材料、预检、一次 `kk ani install`、
全组件行为、同版本 components noop、正常清理。以下表描述选择和规划，不签发实装 PASS。
不部署 ANI 应用、Kube-OVN、Loki、RGW、Istio、Knative、GPU 或 KFP frontend。

| 选择 | 固定材料/组合 | 依赖与必须观察的行为 | 规划持久卷 |
|---|---|---|---:|
| 基础 | Kubernetes 1.35.8、containerd 2.3.4、runc 1.4.3、etcd、Hauler 2.0.3 | 三节点/时间/证书、正式离线 registry 和底座 artifact | 系统盘 |
| KCN | 上游 `2b9467c`、17 CRD、controller/OVN central/OVS/CNI | 先 CRD Established，bootstrap 直连 API；跨节点 Pod/Service/DNS、VPC/Subnet 生命周期 | 0 |
| 专用 Envoy | 已批准 controller、定制 Envoy 和 shutdown-manager 镜像 | CRD→RBAC/Service/config→certgen/TLS→controller→Proxy/Class；路由内容和每副本直连 | 0 |
| Multus | 已批准镜像和 role | 等每节点 KCN primary conflist，再委托；主/副接口真实通信 | 0 |
| Ceph | Rook 1.20.7、Ceph 20.2.4，RBD `ani-block`、CephFS `ani-cephfs` | 三台稳定数据盘；MON quorum/OSD up,in/PG、读写与 CSI | 3 × 200 GiB 原始数据盘 |
| cert-manager | Chart/app 1.21.2 | 原生 CA/链/SAN/有效期；后续 HTTPS 依赖 | 0 |
| PostgreSQL | 17.11 | Ceph；认证、SQL、pg_trgm、既有持久化检查 | 10 GiB |
| Valkey | 8.1.10 | Ceph；认证读写、TTL、未认证拒绝 | 2 GiB |
| NATS | Chart 2.14.6，锁定 server 镜像 | Ceph；JetStream durable 消息与持久化 | 5 GiB |
| metrics | kube-prometheus-stack 85.4.0，完整选定五组件 | Ceph；实际采集和报警生命周期 | 5 + 1 GiB |
| OpenSearch/Fluent Bit | Chart 3.8.0 / 0.58.2，Fluent Bit 5.1.2 | Ceph/CA/认证；唯一 Pod 日志经采集到查询命中、3 天保留 | 10 GiB |
| RustFS | Chart 1.0.0，provider 派生 standalone | Ceph/CA；原生 HTTPS Put/Get/hash、权限拒绝 | 20 GiB |
| Milvus | Chart 5.0.25、实际进程镜像 2.6.24，专用 etcd | Ceph/RustFS；insert/search、持久化 | 10 + 5 GiB |
| Metrics Server | Chart 3.14.0、app 0.9.0 | API/节点；真实 CPU/内存指标 | 0 |
| Snapshot Controller | 8.5.0，RBD/CephFS 两个 Class | Ceph CSI；两种卷写读→快照→新卷恢复→字节比对 | 临时测试卷另计 |
| KubeVirt/CDI | 1.9.0 / 1.66.1、固定 CirrOS 0.6.3 guest | KVM/Ceph/网络；CDI→真实 guest、网络、持久化 | 2 GiB root；scratch 另计 |
| Volcano | Chart/app 1.15.2 | API/节点；gang、cancel、accounting | 0 |
| Harbor | Chart 1.19.2、app 2.15.2，专用 DB/cache/registry/scanner | Ceph/CA；认证、固定 digest push/pull、实际扫描 | 10 + 2 + 5 + 2 + 8 GiB |
| Kubeflow stage2 | `26.03-kubeflow-stage2-v1` | KFP 2.16.0、Trainer 2.1.0、JobSet 0.10.1、Argo/MLMD/MySQL；Run→训练→workspace→artifact/MLMD 成功/exit-42/停止/持久化 | MySQL 20 GiB |
| Notebook/KServe | Notebook Controller 1.10.0、固定 JupyterLab、KServe 0.16 Standard/CPU sklearn | 原生 kernel/WebSocket、workspace 恢复、受限 TLS S3、预测 `[0,1,2]`、重建/错误归因 | workspace 每租户最多 2 × 5 GiB |

完整摘要以 `kubekey/ani/components.lock.yaml`、images.tsv、KCN/Envoy material index、
Kubeflow assets lock 为准。用户供给 KCN `sha256:494432d2…` 是 OCI index；
其经供给归档字节验证的 linux/amd64 manifest 为 `sha256:26470989…`。
平台实际 imageID 对照后者；来源 index 与运行平台摘要不得混用。
Kube-OVN 回归采用来源列表 `82cd6fc0…` 实际选择的 amd64 manifest
`2b505e4d…`。兼容已有批准标签时仍逐 Pod 核对 imageID，并继续要求策略
controller 的 `--enable-np=true` 和独立保护探针；不继承历史隔离 PASS。

## 容量与执行次序

三台实测各 8 CPU / 16 GiB，系统盘和数据盘各 200 GiB，有硬件 `/dev/kvm`。
旧集群节点 allocatable 各约 7.6 CPU / 14.35 GiB，只作同一机器容量参考。
本次完整配置在 Fedora 通过正式 CLI 渲染及选中 Chart 展开后，静态请求合计
7.9 CPU / 12.93 GiB；声明持久卷 115 GiB，两个租户工作区上限另为 20 GiB。
Ceph 三副本的原始逻辑上限约 200 GiB，尚未扣除元数据及安全余量。
验收 VM root/scratch、快照源卷/恢复卷和数据增长需保留约 15 GiB 以上余量，
不得把 PVC 请求总量冒充实际物理占用。

部分 Chart 和 Ceph/KubeVirt/CDI operator 生成的容器未声明 requests，0 请求不能
解释为 0 消耗。旧集群基础/Ceph 请求盘点是历史机器规划输入，不能替代候选实装；
全组合 Ready、实际内存/CPU/卷空间和训练/Notebook/预测/VM 共存后才能接受容量。
不以关闭选中组件、削减必要验证或联网补料解锁安装。

顺序复用正式 role：离线 registry/底座→KCN primary→Multus/Envoy→Ceph→CA→
中间件/metrics/OpenSearch/Fluent Bit/RustFS→依赖其存储与证书的计算/Harbor/Kubeflow。
RustFS 为 Milvus/Kubeflow 提供对象存储；MySQL/etcd/Harbor DB/cache 保持各自归属。
最终全选择由同一首装配置完成，不用事后 components add 拼装。

Multus 正式通信用例按 provider 选择附件：固定 KCN 使用原生 `kc-networking`
NAD（`server_socket=/run/openvswitch/kc-networking-daemon.sock`），在 `ani-platform`
创建受当前 cluster 归属保护的 `ani-b01-vpc`/`ani-b01-secondary`，CIDR 来自
`network.multus.testCIDR`；Pod 用 `net1.networking.kubercloud.com/subnet` 绑定该
Subnet，保留 primary `eth0`。先等待 VPC/Subnet Ready，再验证独立 IP、双向副接口、
primary 通信和 DNS。Kube-OVN 保留原 node-local bridge/host-local 用例。

## 身份、恢复和验收边界

Fedora 任务根：`/home/chabking/ani-installer-runs/kcn-full-20261009`。
`esxi-inventory-attempt-03` 已以严格 hostkey、唯一 guest IP + BIOS UUID 核对
test-01/02/03（VMID 14/15/16）。快照为 Snapshot 1，ID 1/2/2，创建时间分别为
2026-10-08 11:58:06、12:11:27、12:11:34（ESXi 输出的原始时区未额外推定）。
每台两块 200 GiB persistent 磁盘，需在实际还原前重新核对 backing/parent 与快照覆盖，
还原后核对 SSH、机器 UUID、空数据盘和不存在 Kubernetes，才可视作干净基线。
管理口 ens34/管理地址/默认路由保留，Ceph 仅用配置中的逐节点稳定 by-path 设备。

被替换的旧集群 UID 为 `a14b07e9-cf39-46cd-8bea-c2fb9d8c55aa`。
其 6 个 PVC 和 16 个 namespace 已核对为前轮 installer/验收数据，任务输出在
`preserved-current-a14` 保存；完整对象卷 tar 和 MySQL 一致性 dump 在
`current-storage-backup` 保存，摘要与对象 UID 在各自 index。备份含私有数据，
只交付脱敏索引。原来 `kf-env-stage2-20261008` 的冻结 CLI/材料仍保留；
需要恢复原组合时，可从相同干净快照重装旧候选，再在对应 PVC/专用 DB 恢复保存数据。
此路径与当前候选安装分离；尚未执行的数据恢复不标 PASS。

KCN 固定 `kcn-test-unsupported-v1`：实际 provider/imageID 与声明一致才成立。
NetworkPolicy 隔离记 `unsupported`，记录实际可达性；身份/RBAC/跨 namespace Secret、
S3 writer/reader/prefix、HTTP/gRPC、TokenReview/audience 仍须正负例通过。
Kube-OVN 必需隔离合同和既有保护门禁保留，历史验收不提升为本轮 live PASS。
Envoy 测试资源清理必须正常等待，覆盖已知 360 秒终止宽限，不强删。

本轮构建/材料/恢复/安装/验收命令与原始退出码按任务根文件索引保存；
阶段结论只更新 `progress.yaml`。任一必验项 `fail` / `not_verified` 均不满足 Goal 完成。

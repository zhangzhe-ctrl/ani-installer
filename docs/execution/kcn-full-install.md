# KCN full 统一首装合同与证据入口

本轮唯一状态在 [progress.yaml](progress.yaml) 的 `KCN-FULL-20261009`。
单一配置为 [kcn-full.yaml](../../config/examples/kcn-full.yaml)。目标仅为
172.16.101.20、21、22；本地处理源码/Git/传输，重任务均在 Fedora 172.16.101.31。
原始 `release` 基线为 `0f377f6ed7655c42d37e7da8777f92448311814d`。

主链为源码修复、同源正式 CLI 和完整离线材料、预检、一次 `kk ani install`、
全组件行为、同版本 components noop、正常清理。冻结 `41fa6b7` 已完成这条真实主链，
本轮环境为 `ENV_READY`；逐项回执及原始失败保留在
[脱敏证据索引](kcn-full-install/records/evidence-index.json)。以下表固定本次选择及验收合同。
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
本次三节点 allocatable 各约 7.6 CPU / 14.35 GiB；系统盘设备 200 GiB，
当前根文件系统约 100 GiB，节点报告 ephemeral-storage 为 101593080 KiB，不能按设备
容量宣称文件系统有 200 GiB 可用空间。最终节点身份和容量原值见证据索引。
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

固定 KCN 把 Pod 名用于 annotation 的 name 部分，长度上限为 63 字节。
Kubeflow 在 KCN 下为 Argo 3.7.3 设置 `POD_NAMES=v1`，使用 node ID，避免
默认 v2 附加 template 名导致真实 KFP container driver 的 64 字符 Pod 失败。
正式 checker 核实该配置；Kube-OVN 保留默认格式。v1 不会截断 Workflow 名，
因此 KCN 上 Workflow 名仍须不超过 52 字符，为 node ID 的哈希后缀留足空间。
该适配不修改 KCN 源码，也不改变身份、存储或 NetworkPolicy 能力合同。

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

停止用例核对原 Run 的两个停止请求回执、Workflow
`activeDeadlineSeconds=0` 与真实 Failed 终态、原 TrainJob Suspended、原训练 Pod
消失且无替换，以及原工作区保持。KFP 持久化异步采样的 state history 可能跳过
短暂 `CANCELING`；检查器保留实际历史并单独标记 `observed` / `not_observed`，
不能把未采样状态补入历史或把 Failed 改写为 Canceled。仅有 FAILED、未确认的
停止请求、身份错配、未终止 Workflow 或替换训练 Pod 均不能通过。

NATS acceptance 使用固定材料中的 CLI、Secret 引用的 `NATS_TOKEN` 和非交互
JSON 创建本次独立 file-storage stream；只有对应 stream 的 Sequence 1 PubAck
及计划内 Pod 重建后同一消息的 durable consume/ack 才能证明持久化。
不能吞掉创建失败、复用旧 stream 的消息或用 marker 代替协议读回。
metrics 清理核对本次登记的 UID，等待正常 Pod 宽限和异步删除完成；中断后
已消失对象可以关闭登记，同名不同 UID 对象不能删除，不缩短 grace 或强删。

安装、components 和制包的 registry 字节门禁使用同一 90 秒请求传输预算，
覆盖冷缓存大层的完整读入；逐 blob 摘要和声明大小校验、缺失对象拒绝和
调用方取消均保留。读取通过不代表 components 已执行或业务数据已验收。

Milvus 正式 `vector-verify.py` 保留默认 smoke（insert/flush/search/删除自身
collection），并提供 `--mode write|readback|cleanup --receipt <文件>` 的持久化
检查阶段。write 保留本次唯一 collection、服务端 collection ID 和原始向量摘要；
计划内 Pod 重建后 readback 只读取同一 ID 的原始向量并搜索，不重新插入。
cleanup 也须核实相同 ID/向量后才删除该 collection。Pod 重建由验收驱动核对
Deployment/ReplicaSet/Pod/PVC 归属及 UID 后，使用正式 UID 条件删除并正常等待；
检查脚本自身不重建业务 Pod，不以磁盘 marker 代替 Milvus API 读回。

本轮构建/材料/恢复/安装/验收命令与原始退出码按任务根文件索引保存；
阶段结论只更新 `progress.yaml`。任一必验项 `fail` / `not_verified` 均不满足 Goal 完成。

## 最终环境交接：41fa6b7

本次正式运行源为 `41fa6b7dc4bc2e48b8dbc690beb1b8c586f29ca3`，源码树指纹
`565824b438777a652a965f0ebf5d3e0c70176b0de693a7778c4e465573a9434b`。
Fedora 使用 `go1.26.7-X:nodwarf5 linux/amd64`，正式 `scripts/build-code.sh`
调用 `scripts/check-code.sh`，完成 untagged/builtin build、vet、受影响行为及合同门禁；
`scripts/build-offline.sh`、同源 `kk ani validate` 和 `kk ani render` 均退出 0。
原命令环境、日志和 `.rc` 位于任务根的 `build-code-final-41fa6b7-attempt-01.*`、
`build-materials-final-41fa6b7-attempt-01.*`、`final-validate-41fa6b7-attempt-01.*`、
`final-render-41fa6b7-attempt-01.*`。本次后续文档提交不改变该冻结源码或二进制。

替换前 `9ac20d6a` 集群的三次工作区、Notebook 四文件、RustFS 数据卷、MySQL
一致性 dump 和 24 namespace/28 PVC 身份已保存并校验，入口为
`preserved-current-9ac/index.json`。这不声明全部 28 PVC 字节备份或恢复演练通过。
严格核实 ESXi hostkey、三台 VM/磁盘链/快照、管理网络和时间后，
`restore-final-41fa6b7-attempt-01/index.json` 于 2026-10-09 15:20:05 UTC 确认干净基线。
一次统一安装于 15:28:55 UTC 开始、16:19:25 UTC 退出 0，没有事后补装或节点手改组件。
实际命令为：

```bash
sudo env PYTHONDONTWRITEBYTECODE=1 \
  /home/ubuntu/kcn-full/final-41fa6b7-attempt-01/code/kk ani install \
  --config /home/ubuntu/kcn-full/final-41fa6b7-attempt-01/kcn-full-site.private.yaml \
  --package-root /home/ubuntu/kcn-full/final-41fa6b7-attempt-01/materials
```

真实 `install-success` 的 run 为 `ani-ani-kcn-full-20261009-152856`，集群 UID 为
`3de961b2-8bdd-449c-9840-5939bbb023d4`。三节点 IP/UID/DMI、源码、材料和运行摘要
均绑定在证据索引。私有站点原字节 SHA256 为
`165b613a307676c9a4048f75e0402687980f7857f69961e494402264f7488b86`；
公开配置只脱敏 SSH 密码。它与安装记录中的规范化 `siteConfigDigest` 是不同摘要域。

| 实际行为 | 本候选的结果与边界 |
|---|---|
| KCN/Multus/专用 Envoy | 三节点 primary 委托、跨节点 Pod/Service/DNS、原生 VPC/Subnet 与副接口通信通过；HTTPRoute 内容和每个 Envoy 副本直连通过。测试 Envoy 正常终止约 199 秒，覆盖 360 秒宽限，没有强删。 |
| Ceph/RBD/CephFS/快照 | 真实卷写读、两种快照恢复字节比对通过；清理后 3 MON quorum、3 OSD up/in、81 PG active+clean。三类 auth HEALTH_WARN 未静音，见下文。 |
| cert-manager/PostgreSQL/Valkey/NATS/RustFS | CA/TLS/SAN、SQL/pg_trgm、认证与权限负例、Valkey TTL、原生 HTTPS 对象写读摘要通过；PostgreSQL 原行和 NATS 原 Sequence 1 消息在各一次计划内 Pod 重建后读回，同一 PVC 保持。 |
| metrics/OpenSearch/Fluent Bit/Metrics Server | 实际采集、报警 firing→resolved、Prometheus 原样本及 Alertmanager 原 silence 重建后保持；唯一 Pod 日志经 Fluent Bit 到 OpenSearch 查询命中，TLS/认证/保留接线通过；节点 CPU/内存指标可读。 |
| Milvus/Harbor | 原始向量 insert/search 后，经一次 Pod 重建和 noop 读取同一 collection ID/向量，未重插入；Harbor 固定 digest push/pull、认证与实际扫描通过。 |
| KubeVirt/CDI/Volcano | 固定离线 guest 导入后真实 VM 启动，guest 网络及持久数据读回；正式 gang/cancel/accounting 检查通过。 |
| KFP/Trainer/JobSet/MLMD/MySQL | 原 Run→TrainJob→JobSet→workspace→artifact/MLMD 成功、exit-42 和停止通过，MySQL 原数据重启后保持；停止实际历史 PENDING→RUNNING→CANCELING→FAILED，两个请求及原对象终止已确认，不改写终态。 |
| Notebook/KServe | 真实 kernel/WebSocket 执行、工作区停止恢复原四文件、受限 TLS S3 模型加载与预测 `[0,1,2]` 通过；预测 Pod 重建后结果相同，坏路径/坏凭据有实际归因。身份/RBAC/Secret/S3/HTTP/gRPC 保护正负例继续通过。 |
| 同版本 noop 与原数据 | 正式 components 执行 `operation=noop`、`didInstall=false`；14 项已有安装，资源 UID/spec/data 和运行 imageID 保持，PostgreSQL/NATS/Milvus 原数据保持；三训练工作区、Notebook/S3 模型原字节及预测在 noop 后再次读回相同。 |

同源正式 `kk ani verify --level smoke` 对全部 15 项选择退出 0；现有 acceptance
仅声明 PostgreSQL、NATS、metrics，本次三项均退出 0。其余规定行为使用同源正式
专项 checker 和固定 SDK/runtime，由 Fedora 驱动；证据索引区分这三类回执，
不宣称存在全组件 acceptance 入口。清理后 236 个实际 container/init imageID
逐个与冻结 linux/amd64 manifest 相同，全部节点及运行控制器/副本就绪。

KCN 的 `kcn-test-unsupported-v1` 贯穿 runtime、checker 和交接，NetworkPolicy
隔离为 **unsupported**；普通工作负载实际可达性独立记录，不签发隔离 PASS。
Kube-OVN 受影响配置/渲染/runtime/保护探针回归通过，其隔离门禁保留，
本次没有新 Kube-OVN live PASS。ANI 服务/UI/IAM/租户业务验收不在本轮环境交接范围。

Ceph 实际为 **HEALTH_WARN**：`AUTH_INSECURE_CLIENT_KEY_TYPE` 有 4 个 auth client
entities，另有 `AUTH_INSECURE_KEYS_ALLOWED` 和 `AUTH_INSECURE_KEYS_CREATABLE`。
三类均未静音，`mutes=[]`；功能/持久化与 quorum/OSD/PG 检查通过，
没有签发 HEALTH_OK 或安全认证 PASS，也没有关闭这些告警来完成验收。

任务清理已通过：按 UID/归属正常删除测试 Envoy、TrainJob/JobSet/Workflow、
Notebook/InferenceService、8 个测试 PVC、4 组快照及关联后端、VM/CDI/网络附件；
保留正式 snapshot class 的 Retain。另删除三个独立 NATS stream、Harbor 测试项目及
robots/测试 pull Secret、六个摘要绑定的 S3 键、三条值/归属绑定的 PostgreSQL 测试行。
15 个临时授权的真实 TokenReview 均确认过期拒绝，相应 token 文件和 guest SSH 密钥
已删除；正式 runtime 身份/凭据、PVC、控制器及 KFP/MLMD 历史保留。
清理前原输出已导出并核实摘要，清理后的运行镜像、拓扑和其他数据库数据保持。

Harbor 首装 smoke 的项目/robots 现在已清理，未来复验需经正式流程创建新的归属明确
测试资源；不能复用已撤销的授权或把旧 PASS 当作清理后重新运行结果。
本次持久化重建与 noop 的原回执保留，不重用已消耗的验收预算。
驱动中断及映射/导入/认证字段/已消失文件/API 版本错误的原失败均在索引保留；
后续只续接未完成步骤或核对原结果，没有改写失败或重放已完成副作用。

## 二进制、完整包与复现资料

| 交付物 | 位置与 SHA256 |
|---|---|
| Linux amd64 `kk` | 本地 `/home/chabking/Documents/Codex/2026-10-09/kcn-full-install/artifacts/41fa6b7/kk`；`e13098aa5f42f59b110c9766d8d8e0a1020e27deddc236e3326173a758c9f59b` |
| 完整公开离线包 | Fedora 任务根 `delivery-41fa6b7/ani-kcn-full-41fa6b7-amd64.tar`，12733358080 字节；`00673f507f6c871039ee2cc3741b4bf28c2fe0feec6a3d30c11af5aba2c3c1c4` |
| 代码包校验表 | Fedora `code-final-41fa6b7-attempt-01/SHA256SUMS`；`55d92f2987cfbe26bb37d9b9f714196050f38b86984ffc0edba4cf8e547bdff5` |
| 材料包校验表 | Fedora `materials-final-41fa6b7-attempt-01/SHA256SUMS`；`5e4d96e1a02c7adbdd93d6ec488e77ebd1f93ffe59fc4c0a10d9575b37babd2c` |
| 脱敏证据索引 | 本文链接及本地 artifacts 目录 `evidence-index.json`；`5ba8e71595546d808bc887d2ee055787fefe1de9da53858afb2b37a6883705a1` |
| 配置与源码 | 本地 artifacts 目录 `kcn-full.yaml`、`candidate.json`、`archive-receipt.json`；本地任务目录 `source-41fa6b7.bundle`，保留冻结提交与历史 |

完整包包含同源 code、全部 materials、脱敏 `kcn-full.yaml` 与 candidate，包含 112 个
批准镜像、11 Chart、23 Kubeflow 资产及 SDK/workspace wheels、guest、scanner、
Ubuntu ISO 和底座 artifact。完整包已在 Fedora 保存；本地仅保留二进制和小型交付资料。
凭据、私有配置、数据 dump 不进入公开包。可下载到容量足够的自选目录：

```bash
scp fedora:/home/chabking/ani-installer-runs/kcn-full-20261009/delivery-41fa6b7/ani-kcn-full-41fa6b7-amd64.tar \
  /path/with-enough-space/
```

下载后核对上表整包 SHA256，解包后分别在 code/materials 目录核对 `SHA256SUMS`。
公开配置的 `CHANGE_ME` 需要替换为本地私有 SSH 凭据，私有配置放在发布包之外；
新安装仍须重新核实站点身份、干净基线和设备归属。本次源码仅在任务分支提交，
未推送或合并；原 release 工作树保持 `0f377f6` 且干净，其他仓库与集群未变更。

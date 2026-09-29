# ANI Installer：Kube-OVN 路径与可选 RustFS 接入方案

版本：2026-09-29 / r2 执行环境修订，设计稿，尚未实施。

> **r2 执行环境修订（2026-09-29）**：本机是唯一人工源码编辑与 Git commit/push 的工作区；Fedora 是测试、材料、格式化/生成、渲染、构建、制包、诊断和获准现场操作的执行端。允许本机 Git/SSH/SCP 等源码交接操作，但不在本机运行产品、测试或容器。本修订替代旧版“在 Fedora 编码和提交”的表述，技术范围及 RGW 保留要求不变。


代码核对基点：`zhangzhe-ctrl/ani-installer` 的 `feature/b00-b07-install`，`7dfffe7aa1a8ca657a8a6b896761d5e4e72d2abe`。原方案核对时，`main` 为 `61e79e1c2114160c40bd7df34f7156406bf47c3a`。实施应继承 B 分支及实际后继成果，不能从较旧 main 起步漏掉 B00–B07。

**结论：新增 RustFS 选项，不删除 RGW；验证 Kube-OVN 新集群路径，不在线替换 kcn。两个变化先各自联调，最终才用累计正式包进行干净离线首装。**

## 1. 交付边界

唯一完成定义保持不变：

> 使用正式交付包，在声明支持的干净环境与离线条件下，按选定组合完成首装和基本功能检查；支持范围内的组件新增不会改坏底座；出现真实错误会停止并准确报告；整个过程不依赖临时手工修改源码或补漏下载。

本轮新增的目标组合为：

- Kubernetes + Kube-OVN 主 CNI + 已选 Multus；
- 继续使用 Ceph 提供 RBD/CephFS；
- 对象接口选择 RustFS，Milvus 通过受信任 HTTPS 访问 RustFS；
- 继承 B00–B07 中其余已选组件和版本，不以切换对象接口为由变动 Harbor、日志、数据库或调度拓扑；
- RGW 保持正式可选能力，原 kcn + Ceph + RGW 组合不能因新实现退化。

本轮不做：已有 RGW 数据迁移、现存 Milvus 存储后端在线切换、kcn→Kube-OVN 在线替换、RGW 卸载、RustFS 分布式 HA、对象存储故障注入、B08–B12、完整租户/网关平台。外部 LB、租户 VPC、Underlay 等仍按原条件选择，不自动纳入。

**“按需启用”是新安装的选择及明确支持的新增，不是把关闭开关解释为删除集群里的既有资源。**

## 2. 本次核对的事实与设计判断

### 2.1 代码实际情况

1. `config.go` 已将 `storage.provider` 定义为 `ceph|external`，但它没有独立的对象接口选择。`MilvusComponent` 的注释及实现仍绑定 RGW。[C1]
2. `roles/ani/ceph/tasks/main.yaml` 中，CephFilesystem 后直接签发 RGW 证书、创建并等待 `CephObjectStore/ani-store`，然后继续配置块/文件 StorageClass。当前 RGW 不是独立可关的分支。[C2]
3. Milvus role 自己发布 RGW CA、检查 RGW、创建 OBC、检查 S3，然后安装 Chart；values 中写死 RGW Service、OBC Secret、CA 名称和应用内 `tlsCACert`。[C3][C4]
4. Kube-OVN 已有独立 role 与网络字段；但当前 role 主要是部署与 rollout 等待，仍须验证真实数据面。Multus 当前 `testCIDR` 注释限定为同节点测试，不能据此宣称跨节点附加网络/IPAM 已通过。[C5][C6]
5. 当前 Loki Chart 使用 PVC filesystem，不使用 RGW。切换 Milvus 对象接口不应顺带把 Loki 改为 RustFS。[C7]

### 2.2 从上一轮明确吸取的教训

- 独立 S3 客户端成功，不代表实际 Milvus 配置与请求成功；渲染出现 CA 路径也不代表进程已加载。[C4][E1]
- 不能凭“不需要这个业务功能”裁掉官方运行必需的 CRD。Volcano 上轮已因删除 Flow CRD 返工，后来恢复官方 Chart 才通过。[E1]
- 安装后检查必须能重复运行，不能在 VM 已运行时再无条件 start。[E1]
- 首装、真实新增、同版本 no-op 必须在同一组件接入时验证，不能最后才发现依赖含义不同。[E1]
- 已验证成果和当前现场要分开。交接时那次新增底座只记录了开始，应先查询真实终态，不再盲目重发安装。[E1]
- 模板、客户端和依赖的局部问题在组件范围内解决；最终干净首装负责验证从零重现，不承担每个字段的首次调试。

下面的字段、内部结构和流程是本方案提出的改动，不是当前代码已支持的接口。

## 3. 配置设计：网络、块/文件存储、对象接口分别选择

### 3.1 只增加一个对象接口选择，不建立插件平台

保留现有：

- `network.stack: kcn|kubeovn`；
- `storage.enabled`、`storage.provider: ceph|external` 及明确的磁盘声明；
- B00–B07 现有组件开关与参数。

新增一个顶层块：

| 字段 | 本轮约定 |
|---|---|
| `objectStorage.provider` | `rgw`、`rustfs`、`none`；新示例显式填写 |
| `objectStorage.rustfs.mode` | 首期只接受 `standalone`，不接受将 replicas 调大冒充分布式 |
| `objectStorage.rustfs.storageClass` | 显式 StorageClass；本轮使用已选 Ceph RBD 类 |
| `objectStorage.rustfs.storageSize` | 正容量，纳入累计容量预算；不能沿用 Chart 的微型演示容量 |

第一版不额外暴露任意 Helm values 透传、动态 provider 插件、多副本拓扑、匿名访问或跳过 TLS 开关。RustFS、RGW TLS 和初始化凭据使用已有受保护的安装输入/生成机制，避免为本轮再造凭据系统。

`objectStorage` 在解析结构中保留“整块未出现”这一信息，例如使用指针；不要让普通零值同时代表“旧配置”与“显式关闭”。

### 3.2 向后兼容与合法组合

| 输入/现场 | 行为 |
|---|---|
| 旧 site 没有 `objectStorage` | 严格保持旧的有效行为：原来会执行 Ceph/RGW 的配置继续 RGW；原来不部署对象存储的配置继续不部署 |
| 新 site 选择 `rgw` | 使用保留的 RGW/OBC/CA 实现；当前受管 RGW 要求 Ceph 已选 |
| 新 site 选择 `rustfs` | Ceph RBD/CephFS 保留；只启用 RustFS 对象服务，不创建本次新的 RGW 实例/OBC |
| 新 site 选择 `none`，无对象使用方 | 可以仅部署块/文件存储 |
| 选择 `none`，但 Milvus 仍启用 | 在变更前报依赖错误，不偷偷启用 RGW/RustFS |
| `rustfs` 但无可用声明存储 | 明确拒绝，不自动改用 hostPath、local-path 或新部署存储系统 |
| 已安装 Milvus，拟改变 provider/endpoint/bucket/rootPath | 普通首装/新增入口拒绝，说明需要独立迁移；不改工作负载指向 |
| 已部署 RGW，随后传关闭配置 | 不执行删除；如属于移除/切换请求则明确拒绝，不能报告“已经关闭” |

当前只管理一个站点默认对象后端；不为同一个 Milvus 提供自动双写、双后端回退。开发期间旧 RGW 与隔离 RustFS 测试实例可以并存，但这不等于现存 Milvus 已迁移，也不自动删除旧服务。

显式 `rgw` 的同义配置与旧配置应解析成相同的有效选择。老成功记录缺少新字段时，只能根据该记录已有配置/资源事实作受限兼容解释，不能一律把任何旧记录解释成 RGW；不能重写旧文件来抹平差异。

### 3.3 配置示例（拟新增 schema 的片段，不是可直接执行的完整 site）

```yaml
network:
  stack: kubeovn
  # 其他现有管理网卡、Pod/Service CIDR、joinCIDR 和 Multus 字段，
  # 使用已核准的真实站点值，不用示例网段覆盖现场。

storage:
  enabled: true
  provider: ceph
  # 原来的 nodes/devices 声明必须保留。

objectStorage:
  provider: rustfs
  rustfs:
    mode: standalone
    storageClass: ani-block
    storageSize: 20Gi  # 只是预算示例；真实值必须通过容量核算。

components:
  milvus:
    enabled: true
    storageClass: ani-block
    storageSize: 10Gi
    etcdStorageSize: 5Gi
  # 其余 B00–B07 已选字段原样保留。
```

新建 RGW 站点仅把对象块改为：

```yaml
objectStorage:
  provider: rgw
```

这是不同新站点配置的例子，不是现存 Milvus 的在线切换命令。

## 4. 保留 RGW 的具体实现边界

### 4.1 Ceph role

保持原 `object.yaml`、`rgw-tls.sh` 和相关代码。对“渲染 RGW TLS → 创建证书 → 渲染/创建 CephObjectStore → 等待对象服务 → RGW 专属检查/连接输出”整体加有效 provider 条件。

选择 RustFS/none 时不能只跳过 apply，却仍然等待 `ani-store Ready`；也不能在通用存储检查中继续硬等 RGW。

以下继续正常运行：Rook/CSI、CephCluster、OSD、CephFilesystem、RBD pool、StorageClass、RBD/CephFS 写读。RGW 分支不应该成为这些资源完成的必要条件。

**保留官方共用 CRD/RBAC/控制器定义。** 新配置没有 RGW 实例不代表必须删除 `CephObjectStore` CRD、OBC CRD 或从官方 Rook 包裁掉其控制器定义。验证的是“未创建不需要的实例/池/桶/服务”，不是全文不得出现 RGW 单词。

### 4.2 Milvus role

保留 RGW 的 `bucket.yaml`、`rgw-ca.sh` 等文件。选择 RGW 时仍由 Rook OBC 供给桶及凭据，保留既有桶名、Secret key 和 CA/路径兼容。

选择 RustFS 时仅跳过 RGW 专属前置、CA 发布和 OBC 创建，改走 RustFS 的桶/应用凭据初始化。向量功能检查仍复用同一实际 Milvus 流程，不复制一个“永远通过”的简版。

原 RGW 的镜像 pin、材料和回归继续保留。新匹配代码+材料包必须能够处理旧 site；旧版本发布物也保留，不强求任意新代码混用任意旧 artifact。

## 5. 对使用方提供一份明确的 S3 连接结果

只增加小型数据结构和有限分支，例如 `ResolveObjectStorage` 与 `ResolveMilvusS3Binding`。这是拟定内部名称，可合入现有文件，不是要求另建服务。

一份 Milvus 连接结果包含：

| 内容 | 说明 |
|---|---|
| provider | 实际选中的 RGW/RustFS |
| endpoint、region | URL 与签名区域；从同一事实拆出 host/port/TLS，避免多处各自设置 |
| bucket、rootPath | 该使用方的独立桶/前缀；存储绑定，不因重跑改变 |
| credentialsSecretRef | namespace/name、access key 与 secret key 的字段名，不含明文 |
| caRef、clientCAPath | 公有 CA 来源与容器实际路径，不分发服务端私钥 |
| addressing | 本轮固定 path-style，避免另引通配 DNS/证书 |

配置验证、依赖计划、首装渲染、新增、no-op、smoke、连接说明都消费这份结果，不各自拼 `rook-ceph-rgw-...` 或 `ani-rustfs-...`。

RGW 既有 CA 挂载路径可保持不变；RustFS 使用自己的公有 CA。核心是 binding 输出与 `user.yaml`、挂载、进程环境一致，不要求为了命名统一重建已有 release。

Milvus 必须显式设置实际使用的 endpoint、port、SSL、region、bucket、rootPath、Secret 引用以及 `minio.ssl.tlsCACert`。不靠 AWS CLI 的环境变量推断 Milvus 的配置优先级。保持 Milvus 2.6.24、Chart 5.0.25、专用 etcd、RocksMQ 原组合，不复制 RustFS 示例里的其他 Milvus 版本、Woodpecker 或 Attu。[C4][W1]

## 6. RustFS 首期部署方案

### 6.1 范围与版本

本方案选 **官方 Helm 的 standalone 模式 + 一份数据 PVC + 内部 HTTPS Service**，用于当前实验交付。RustFS 官方文档将 standalone 描述为 Deployment+PVC；因此不能一开始就把 `componentInstallSpecs` 的主工作负载写成 StatefulSet，必须依据冻结 Chart 的实际结果登记。[W2]

Ceph RBD 可为卷提供自身冗余，但单 RustFS 进程仍不是对象服务 HA；更换对象接口也不会修好 Ceph 的 CSI 认证、PG 或时间问题。

现有 Kube-OVN/Milvus/Ceph/Helm 版本和有效 pin 不随此任务升级。RustFS `1.0.0-rc.1` 可作为本次材料评估候选：本次看到的官方发布页将其列为 pre-release，不能称作已认证稳定版。[W5]

**本方案没有取得并核验 RustFS 的全部镜像层、匹配 Chart 和管理客户端包，因此不提供虚构 digest 或已锁定 Chart 版本。** 第一阶段确认一组 server tag+digest、Chart 版本+包摘要、管理客户端版本+摘要；随后联调和正式发布使用同一组。若既有工作区已经完成可靠锁定，优先复用，不追逐 latest。

### 6.2 资源约定

建议使用 `ani-platform` 命名空间、`ani-rustfs` release，便于与当前 Milvus 集成；实际对象名以锁定 Chart/明确 fullname 配置为准，并在生产测试核对。服务只暴露集群内部；Console 关闭或仅内部管理访问，不安装 Ingress、Gateway、RustFS Operator、外部认证系统。

参数通过真实 Chart values 设置；不能通过随意删除模板裁掉必需依赖。必须核对：standalone/distributed 开关、PVC 类/容量、用户 UID/fsGroup、目录写权限、Secret 引用、探针协议、管理端口、默认 Ingress、init 镜像和日志卷。所有需运行的镜像都必须入离线包。

数据卷首期使用已核准的 `ani-block` 类。日志若用 stdout 就显式选择，不无意多出日志 PVC；若冻结版本确需日志卷，则明确预算。不得继承 tiny 示例容量或默认 local-path。[W2]

### 6.3 TLS 一次接完整

优先复用 installer 已有的内部 CA/证书生成或已选 cert-manager 能力，不另起 PKI。为 RustFS 签发自己的服务端证书，SAN 覆盖实际 Service DNS。签发者身份和公有 CA 输出明确，私钥仅到服务端 Secret。

官方原生 TLS 以 `RUSTFS_TLS_PATH` 指定目录，并要求 `rustfs_cert.pem`、`rustfs_key.pem` 文件名；Secret 投影、实际文件权限以及非 root 进程读取能力必须实测。[W3]

同时核对：

- Service 端口、应用 listener、探针 HTTP/HTTPS 协议匹配；
- Milvus 的最终配置文件、环境和挂载都指向同一 CA；
- 凭据初始化客户端、S3 基本检查与 Milvus 都验证证书和主机名；
- 通过实际 client 的请求证明信任链，不能只以 `openssl verify` 或文件存在作结论；
- 不用 `-k`、`--insecure`、关闭 verifySSL、匿名桶或无签名访问解决问题。

本轮不把证书自动轮换或 HA TLS 演练扩成新项目；记录证书有效期与更新限制。

### 6.4 桶、应用身份和权限由正式流程完成

RustFS role 负责部署服务、保存/复用受保护的 root Secret、TLS 与卷；Milvus role 在 RustFS 分支负责该应用的桶、应用用户/访问密钥、限权策略与 Secret。

首次安装可使用已有受保护输入或安全生成的新凭据；生成一次后保存，再执行复用，不因 smoke/no-op 生成新密码。保留已有实验环境口令，不把旧密码轮换拉进本轮；新 RustFS 不使用公开默认凭据。[W4]

使用冻结版本支持的官方管理客户端/管理 API 完成初始化。S3 数据面可用不证明 MinIO/RustFS 管理 API 完全相同；不能根据命令名字编出不存在的操作。[W6]

应用 Secret 只包含专用应用密钥，不含 root 管理凭据；管理凭据只暴露给初始化 Job/必要受控操作。权限限于本应用桶及实际使用的 S3 操作。操作应覆盖实际需要的 list/get/put/delete、multipart 等，不授予管理其他桶/用户的权限；以负向请求验证未授权范围拒绝。

初始化的重复语义：能确认同名对象为本流程已创建且配置一致才复用；不明来源冲突或凭据丢失时停止，不覆盖/轮换来凑通过。初始化失败保留阶段信息和已创建资源，不全局删桶。现有安装记录与原生对象足够，不新建独立 IAM 控制面。

## 7. Kube-OVN 路径验证

保留 kcn role 和其专属 Envoy。新 site 显式 `network.stack: kubeovn`，互斥选择主 CNI。不得在已有 kcn 集群在线改主网络，也不以“附属 CNI 模式”绕过本次干净主 CNI 验证。

先复核当前 Kube-OVN v1.16.6 的固定源清单与材料。代码注释只是线索，kernel、IPv6 开关、网卡、MTU、控制面标签、主机网络路径和防火墙要求要针对现场/固定版核实，不能把“assumed present”当预检结果。[C5][W7]

使用现有字段的真实值检查 PodCIDR、ServiceCIDR、joinCIDR 与管理网络无冲突；网关在声明网段内；不复用 kcn 专属 sysctl/设备配置，也不删除它的源代码。

Kube-OVN 的 command/Helm/checker 沿用 C07 的同一 `RunScope` kubeconfig，不再使用隐式 HOME 目标。所有变更与测试在声明执行节点运行。

基础网络验收要求：

1. 官方组件与必需 CRD 就绪，地址分配和路由正常；
2. 每个节点上的自有测试 Pod 访问其他节点 Pod，双向连通；
3. 同一批测试的 ClusterIP Service 与 DNS 查询正确；
4. 节点及 Pod 可到达本次离线 Registry、Kubernetes API、存储服务；按选定功能检查 NodePort；
5. Multus delegate 指向本次 Kube-OVN 主配置，不递归，附加网测试不破坏 eth0/default route/DNS；
6. 本次未启用的 LB/外部网关不自动部署，kcn 及专属 Envoy 不参与本次新集群。

**Multus 的跨节点附加网必须有真实跨节点二层/IPAM设计才可宣称。** 当前同节点 bridge 测试只证明同节点 net1；本轮必须明确写出附加网的支持边界，不能把跨节点主网测试当成跨节点 net1。用户未新增外部 Underlay/VPC/LB 要求时，不借此扩范围。[C6]

## 8. 最小代码修改面

| 位置 | 改动 | 不做什么 |
|---|---|---|
| `pkg/ani/config.go`，必要时 `object_storage.go` | 可选对象块、旧配置兼容、唯一有效选择及 binding、冲突检查 | 不新造统一配置/插件系统 |
| 配置生成、材料选择、有效组件选择 | 一次解析后供给各层；RustFS 独立 canonical component；按所选 provider 取材料 | 不要求 RustFS 关闭时也具备它的包 |
| `roles/ani/ceph/tasks/main.yaml` | RGW 专属步骤整体条件化；块/文件步骤继续 | 不删旧模板、不裁官方共享 CRD |
| 新 `roles/ani/rustfs/` | 固定 Chart、PVC、Secret/TLS、就绪和 S3 基本检查/连接事实 | 不安装 Operator/网关/HA框架 |
| `roles/ani/milvus/` | RGW分支保留；新增 RustFS 初始化；公共 values/checker消费 binding | 不改 WAL/版本，不复制弱化验证 |
| `components_install.go` 与 `ani_components.yaml` | 同步首装/新增/no-op的组件与技术依赖含义；RustFS先于Milvus | 不将内部桶/IAM步骤误当用户开关 |
| `run_manifest.go`、记录消费处 | 记录 provider 与使用方存储绑定；防止新增入口伪装迁移 | 不改写老成功记录、不发放新验收额度 |
| `roles/ani/kubeovn/`及网络检查 | 执行上下文、前置、真实数据面验证 | 不照搬另一个CNI规则、不改kcn |
| 材料锁/供料脚本/回归 | 添加 RustFS及客户端固定材料；旧 pin 保留 | 不重建供应链平台、不重新研究所有旧工具 |

改动按上述范围审阅。发现无关问题另列，不自动扩大本轮；已知会影响当前数据/目标安全的共用缺陷则最小修复。

## 9. 验证顺序：先组件、再网络、最后全集

### 阶段一：接手和配置/材料冻结

本机先核对源码仓库根目录、分支、HEAD 与未提交修改，再经 `ssh fedora` 核对远端执行 worktree、对应 SHA 和上一轮长任务终态。源码修订及 commit/push 只在本机；Fedora 只接收固定提交并执行。保留两端原有开发稿、旧包与现场，不因旧状态说明回退或覆盖。详细分工见第 12 节。

先完成旧 site、显式 rgw、rustfs、none 的配置/材料/渲染回归，以及固定 Chart 的展开。准备一组 RustFS/管理客户端候选，不在尚未确认官方材料时宣称实现已完成。

Fedora 保存局部日志和候选材料；脱敏结果回传本机更新现有 progress，不在远端手改并提交产品文档，也不新建十几套任务卡。

### 阶段二：RustFS + 实际 Milvus 组件联调

先在已有可用网络的授权开发测试范围运行 RustFS。可用独立 namespace/release/PVC/桶和测试 Milvus+etcd，防止更改现存 Milvus。使用同一生产 Chart、值生成器和固定镜像；仅 namespace/release/fixture 隔离的差异必须明确。

若现有 role 的固定名字不适合临时并行实例，可以在隔离测试环境运行，或先用同一 Chart和配置生成器验证应用兼容，再在阶段三复核正式 role；不能为此将全仓命名改造成另一套平台。

必须依次通过：

- RustFS 已加载正式 TLS，专用应用身份对本桶有权限、其他范围被拒绝；
- HeadBucket、Put/Get、List、删除自有测试对象，必要的 multipart 等实际操作；
- **实际 Milvus 2.6.24 进程**读取生成的 user.yaml 与 CA；
- 新 collection → 固定 ID/向量 → flush → index → load → search，结果正确；
- 对应桶/前缀实际产生对象，不是走了未发现的本地/旧RGW回退；
- 正常检查与重复检查都能工作，不无条件重建服务。

同时用修改后的 RGW 分支对旧配置和隔离测试 collection 做兼容回归。需要重建旧 Milvus才能验证变更时，应使用隔离实例/单独授权，不碰原数据。

**进入下一阶段前，不能只交付 AWS CLI 成功、独立 minio-go 成功、Chart 渲染成功或 CA 文件存在。**

### 阶段三：Kube-OVN 开发基线 + 正式新增

在明确授权的干净目标建立 Kube-OVN 开发基线。可使用 `profile: full` 但只启用所需底座和 Ceph RBD/CephFS、`objectStorage.provider: none`、关闭 Milvus及RustFS。当前 `profile: base` 会在存储前停止，不能误以为它能自动提供后续 PVC。[C8]

网络和块/文件存储检查通过，取得真实成功底座记录。随后使用正式新增入口从 `none` 增加 RustFS，再增加 Milvus，或让同一计划显式包含顺序依赖。

这是有限的受支持状态变更：**无使用方绑定时 none→rustfs 的新增可以支持；已绑定 RGW 的 Milvus 切向 RustFS 不支持。** 新增路径要在第一次写入前验证这一差别。

完成 RustFS和Milvus真实新增、基本检查、同版本no-op；确认底座UID/存储/主CNI未被重装。检查合理新增的CRD/CA/Secret不同于错误破坏；不用 resourceVersion 或控制器状态时间戳全等证明“零影响”。

阶段中的配置问题就在开发基线局部解决。允许经授权使用源码生成的局部部署做联调，但明确是 dev，不补签失败的正式 install-success。

### 阶段四：冻结候选，最终干净离线首装

只有阶段二/三通过，才固定本机候选提交并同步到 Fedora，在 Fedora 运行一次必要完整门禁与同源发布。build 自带 gate 时不对同树重复独立 gate。材料未变就复用对应累计包；只加 RustFS和必要客户端，不反复下载重打无关内容。

使用匹配源码/代码包/材料/完整site，在另一个获准干净目标或重新获准的初始快照上首装最终集合：

`Kubernetes + Kube-OVN + Multus + Ceph RBD/CephFS + RustFS + 已选 B00–B07`。

本次不创建RGW实例，但保留官方共用CRD。首装直接选全本次组件，不在首装后手工补装缺项冒充“全量首装通过”。使用真实成功记录执行基本检查，再执行同版本no-op。真实新增证据来自阶段三，不能被no-op替代。

Kube-OVN 网络初次开发和最终全集验收可能各需干净环境；这不是承诺只需一次实验，而是禁止每修一个应用字段都恢复全集。

### 阶段五：固定结果与结束

通过后由 Fedora 保存原始证据和当前结果索引；将脱敏结果回传本机，仅在本机更新并提交现有 progress/手册。源码SHA、材料摘要、site摘要和实际run绑定，私有凭据留远端仓外。本机推送有实际测试对应的分支/PR，不自动合并，不为了把CI成功写回同一分支循环提交文档。

本轮结论只覆盖新增组合及旧RGW兼容回归，不把 kcn+RustFS、Kube-OVN+RGW 都写成已做全集实机认证，也不把B08–B12写成完成。

## 10. 必须通过的测试与准入条件

| 场景 | 验收要点 |
|---|---|
| 旧配置 + 新匹配发布物 | 有效选择仍为原RGW，不多装RustFS，不改旧OBC/绑定 |
| 显式RGW | 可配置、可渲染、真实Milvus向量回归通过；旧代码/材料未删除 |
| RustFS + Ceph | 不生成RGW实例/TLS/OBC/wait；RBD/CephFS仍正常；RustFS资源齐全 |
| none + 无S3使用方 | 无对象服务资源；块/文件流程能成功 |
| none + Milvus；坏参数/缺料 | 首次写入前拒绝，不能隐式选择provider |
| 初次RustFS初始化 | TLS、目录权限、Secret、桶、限权身份顺序正确 |
| RustFS重复/no-op | 不再次安装、不换凭据、不覆盖桶策略，不清空数据 |
| Milvus实际客户端 | 全向量链＋实际对象，非脚本关键词/健康端口替代 |
| 旧S3绑定变化 | 新增入口拒绝迁移；错误endpoint、CA、Secret不触发替代后端回退 |
| Kube-OVN | 跨节点Pod、Service、DNS及离线基础服务可达；Multus边界准确 |
| 四种静态组合 | kcn/ovn × rgw/rustfs能正常选择；静态通过不写成四组合实机通过 |
| 最终正式包 | 无开发目录/旧服务/公网补料依赖，全集首装+基本功能+no-op真实通过 |

负向测试在隔离对象/模拟端点执行，不破坏真实业务凭据或数据。等待有总截止时间。证书错误/权限错误/配置错误不是“再试几次”的理由；有限重试仅用于已识别的暂态连接/异步就绪，不重新发出整套安装或重放未知变更。

## 11. 如何决定重测范围

| 改动/失败 | 先做什么 | 不自动做什么 |
|---|---|---|
| S3地址、CA、应用user.yaml | 实际RustFS/Milvus组件复验 | 不重新装Kubernetes/Ceph |
| 检查器已有状态/no-op错误 | 真实脚本/计划器正反回归，再核对应组件 | 不恢复三节点 |
| 新物料/Chart变化 | 摘要、真实展开、必需对象和启动能力 | 不凭文件名裁剪依赖 |
| 主CNI/内核/管理网/MTU变化 | Kube-OVN开发基线验证 | 不先装全部应用等待后端报错 |
| 已冻结包的首装顺序/缺物料 | 保存首错，修改对应层，评估受影响组合重验 | 不自动循环快照或目标机补源码 |
| 文档/证据索引变化 | 文档检查 | 不重复构建/首装 |

## 12. 实施环境与权限（r2）

### 12.1 三类主机的职责不能再混用

| 环境 | 允许的工作 | 不执行的工作 |
|---|---|---|
| 本机：当前 agent 编辑器所在主机 | 阅读/编辑源码、测试代码及文档；分支、commit/push 等 Git 操作；任务文件放置；发起 SSH/SCP 交接 | 不运行测试、编译、go vet、模板渲染、材料下载/校验、制包、容器、产品 CLI 或集群命令 |
| 远程 Fedora：`ssh fedora` | 接收固定源码、依赖与材料准备、格式化/生成、局部和集成测试、渲染、构建、制包、诊断、证据；控制获准现场操作 | 不人工修订产品源码/测试/版本锁/入库文档；不 commit/push，不向 GitHub 推送“现场修复” |
| 获准 installerNode / 集群 | 由 Fedora 发起，执行已批准的组件联调、正式包安装及检查 | 不现场手改产品源码，不靠临时下载补缺项，不把 Fedora 本身当集群首装节点 |

本机实际仓库绝对路径尚未给出，先用 `git rev-parse --show-toplevel` 确认，不能套用 Fedora 的 `/home/chabking/workspace/...` 路径。`fedora` 指向 `chabking@172.16.101.31`，SSH IdentityFile `/home/chabking/.ssh/id_ed25519` 属于发起连接的本机配置，不读取、输出或复制私钥，也不关闭主机指纹校验。

任务包以本机的 `docs/execution/plans/kubeovn-rustfs-optional-r2/` 为受维护副本；随源码同步到 Fedora 的执行 worktree，保持相同相对路径。旧 r1 可以留作历史，不再作为执行指令。

### 12.2 源码只从本机流向 Fedora

1. 本机选择包含 `7dfffe7` 及后继有效成果的 B 工作树；核实后复用或创建 `feature/kubeovn-rustfs-optional`。这里的 SHA 是历史基点，不是要求回退。旧 main 尚缺 B 成果时不得基于它漏做现有能力；保留两端未提交草稿，不 reset/clean/stash。
2. 按小的功能阶段在本机 commit，正常 push 开发分支；标为待测的开发提交不等于已验收。Fedora 在核实后的 Git 仓库中 fetch，并在任务专用 worktree 检出完整提交 SHA。不要在有草稿的远端原工作树直接 pull、强切分支或覆盖。
3. 每轮执行前核对 Fedora `HEAD`、分支/分离状态和受跟踪源码内容；必须与本机指定的候选一致。以完整 SHA 为准，不仅核对一个可移动的分支名。可以复用空闲、无受跟踪改动的本任务执行 worktree，不必每个小改动重新复制全部物料。
4. 未推送的开发提交可以通过 Git bundle 交接；这是源码传输，不是本机测试。不得用未编号的散文件、双向 rsync、覆盖 `.git` 的方式形成不可追溯候选。无需每保存一个文件就提交/推送。
5. Fedora 工具生成了格式化、代码生成或锁文件候选时，作为产物回传本机审阅，再由本机提交；远端不得手改这些源码来“修到绿”。最终门禁和发布必须基于本机已纳入、已同步的固定版本。格式化改变源码后跑受影响复验，不用旧 SHA 冒充新结果。
6. 若本机提交钩子要求运行工具，沿用仓库已有远程执行方案；没有时明确说明冲突，不私自 `--no-verify`、改弱钩子或在本机安装工具链。

**单向闭环：本机修订 → 本机提交/源码交接 → Fedora 检出同 SHA → Fedora 验证 → 诊断返回本机 → 本机定点修复。** 不创建第二个代码权威副本。

### 12.3 Fedora 的路径和执行方式

远端源码执行目录必须先核对；需要新 worktree 时可使用 `/home/chabking/workspace/ani-installer-kubeovn-rustfs-run/` 下的任务专用目录，不能把该建议路径写成已经存在。远端日志、缓存、私有 site 和正式包建议统一放在 `/home/chabking/ani-installer-runs/kubeovn-rustfs/`，避免提交入库。

控制端只发起 SSH。每次远端命令显式 `cd`，多行命令使用带引号 heredoc，避免本机提前展开远端变量。沿用 Fedora 已有工具链与合规缓存，TMPDIR 优先放任务自有 home 目录。Fedora 不可达时不退回本机跑测试；报告连接阻塞，但可以继续独立的本机源码编辑。

不要把原始日志、私有凭据或大型材料全部拉回本机仓库。只回传必要诊断、生成的候选差异及脱敏结果。每条测试/构建/联调证据记录实际受测 SHA、命令和退出码，最终发布的本机提交、推送提交和 Fedora 受测版本一致；CI 另按相同 SHA 记录。只改文档不自动使未变化的二进制需要重建，也不得把文档后继 SHA 冒充已现场测试源码。

### 12.4 现场授权保持独立

本轮目标节点必须从最新授权/site确认。较新的记录是 `.10/.11/.12`，历史曾用 `.20/.21/.22`；两者都不是可以直接照抄的当前授权清单。连接 Fedora 的权限不是恢复 VM、清盘、切主 CNI 或删除数据的授权。需要恢复时列明 VM、当前数据、快照和影响，在本轮取得明确确认；该次确认不是无限次重置许可。

先检查上一轮长任务是否还在执行及其终态；SSH 断开先查原任务，不重新启动安装。保留现有实验锁，不占无限 sleep、不杀不明进程。不改 ESXi 宿主网络，不删除旧 RGW、PVC、池、桶及原始证据。

## 13. 结束条件与未确认点

本轮结束必须同时满足：

1. RGW实现和材料保留，可显式启用；旧site兼容回归通过。
2. RustFS按选择安装，桶/限权凭据/TLS自动初始化；实际Milvus固定组合工作。
3. Kube-OVN数据面与已承诺的Multus能力验证通过。
4. none→RustFS→Milvus的受支持新增与no-op通过，不迁移已有RGW绑定。
5. 最终累计包在获准干净环境完成Kube-OVN+RustFS+B00–B07首装与基本检查。
6. 原有数据/材料未删除；未测试组合、容量限制、RustFS成熟度和Ceph告警明确记录。

本方案不是测试结果。目前仍需在实施第一阶段确认：RustFS最终镜像/Chart/管理客户端版本与摘要、当前现场和新增任务终态、最新资源余量、固定版Chart的TLS/初始化参数。资料不足不编造，也不因此让执行者重新规划整个installer。

## 来源

### 已读项目源码（固定7dfffe7）

- [C1 config.go](https://github.com/zhangzhe-ctrl/ani-installer/blob/7dfffe7aa1a8ca657a8a6b896761d5e4e72d2abe/kubekey/pkg/ani/config.go)：存储、网络、Milvus及组件配置。
- [C2 Ceph role](https://github.com/zhangzhe-ctrl/ani-installer/blob/7dfffe7aa1a8ca657a8a6b896761d5e4e72d2abe/kubekey/builtin/core/roles/ani/ceph/tasks/main.yaml)：当前RGW创建与块/文件存储步骤。
- [C3 Milvus role](https://github.com/zhangzhe-ctrl/ani-installer/blob/7dfffe7aa1a8ca657a8a6b896761d5e4e72d2abe/kubekey/builtin/core/roles/ani/milvus/tasks/main.yaml)：OBC、CA、Chart及向量验证。
- [C4 Milvus values](https://github.com/zhangzhe-ctrl/ani-installer/blob/7dfffe7aa1a8ca657a8a6b896761d5e4e72d2abe/kubekey/builtin/core/roles/ani/milvus/templates/values.yaml)：真实TLS/user.yaml/Secret/endpoint绑定。
- [C5 Kube-OVN role](https://github.com/zhangzhe-ctrl/ani-installer/blob/7dfffe7aa1a8ca657a8a6b896761d5e4e72d2abe/kubekey/builtin/core/roles/ani/kubeovn/tasks/main.yaml)。
- [C6 network config](https://github.com/zhangzhe-ctrl/ani-installer/blob/7dfffe7aa1a8ca657a8a6b896761d5e4e72d2abe/kubekey/pkg/ani/config.go#L704-L741)：主CNI和Multus现有能力边界。
- [C7 Loki values](https://github.com/zhangzhe-ctrl/ani-installer/blob/7dfffe7aa1a8ca657a8a6b896761d5e4e72d2abe/kubekey/builtin/core/roles/ani/loki/templates/values.yaml)：当前filesystem模式，来源为上传归档同提交源码。
- [C8 profile config](https://github.com/zhangzhe-ctrl/ani-installer/blob/7dfffe7aa1a8ca657a8a6b896761d5e4e72d2abe/kubekey/pkg/ani/config.go#L509-L532)：base/full边界，来源为上传归档同提交源码。

### 上轮证据

- [E1] 用户提供的 `ani-installer-b00-b07-b13-handoff-20260928-7dfffe7.zip` 及上一轮 `evidence-digest.md`。本次重读摘录，使用其中明确注明来源的v12/v13/v15/v16结果和限制；没有重复全包校验、执行归档程序或访问现场。

### 本次外部核对（官方资料；不是本组合认证）

- [W1 RustFS–Milvus integration](https://docs.rustfs.com/en/developer/integration/big-data/milvus)：示例为2.6.0+alpha.83/Woodpecker，本方案不照搬版本和组件。
- [W2 RustFS Helm installation](https://docs.rustfs.com/en/installation/cloud-native)：standalone、PVC、官方Chart默认及探针。
- [W3 RustFS native TLS](https://docs.rustfs.com/en/integration/tls-configured)：TLS目录、文件名和权限。
- [W4 RustFS credentials](https://docs.rustfs.com/en/operations/credentials)：root、Secret文件及应用身份边界。
- [W5 RustFS releases](https://github.com/rustfs/rustfs/releases)：本次读取到1.0.0-rc.1为pre-release，仅作材料候选，不宣称最新稳定版或已锁定发布包。
- [W6 RustFS S3 compatibility](https://docs.rustfs.com/en/administration/protocols/s3)：数据面与管理API差异、实际使用操作需按版本验证。
- [W7 Kube-OVN prerequisites](https://kubeovn.github.io/docs/stable/en/start/prepare/)：现场前置的官方参考；执行仍应对照既定v1.16.6，而不是无条件照搬最新文档。

本次没有操作Fedora或实验节点，没有修改仓库，也没有安装RustFS。所有新增配置与内部函数均为建议实现合同。

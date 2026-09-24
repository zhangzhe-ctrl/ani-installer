# 03｜后续组件分批详细执行计划

2026-09-24 / r1。以已选版本矩阵和源码缺项审查为基础，保留“Milvus→虚拟化/调度→Harbor→Kubeflow”的主业务顺序；网络/既有底座修复是公共前置。**先完成第二份计划的正确性与新增组件入口，不把新增组件和旧缺陷一起调。**

## 1. 范围

继续：Multus、Kube-OVN适配、Milvus、Metrics Server、CSI Snapshot、KubeVirt/CDI、Volcano CPU、Harbor、Kubeflow26.03的Notebooks/Trainer/Hub/KServe/Pipelines（按需）。
明确暂缓：LWS、vCluster、GPU相关、业务Envoy Gateway/AI Gateway。kcn专用Envoy保留原专属流程，不作为业务公共入口。Istio/Knative默认不安装；无Istio首轮只承诺独立组件/内部API，不伪称完整Kubeflow多租户Web界面。

Dex/Kata/Jaeger保留O卡，只在用户明确选择时实施；不会自动成为B批次的必装项。这里是沿用前轮矩阵的条件状态，不把“详细写计划”解释成所有候选均默认启用。

## 2. 每批四步，快速模型一次只拿一步

| 子任务 | 工作 | 不允许 | 通过后产物 |
|---|---|---|---|
| `.M` 材料 | 查固定版官方清单/Chart、字段映射、递归依赖、真正摘要 | 装到测试集群、换版本、虚构SHA | material-report、已核对锁、离线测试物料 |
| `.I` 实现 | 本批强类型配置/role/注册/连接输出/checker | 跨卡重构、改CNI、自动升级别的服务 | 局部diff、可渲染/可构建源码 |
| `.T` 局部测试 | 真模板/Chart/fixture/失败路径执行 | 用三节点重装当第一道测试 | tests.log、render清单、覆盖报告 |
| `.V` 真实验证 | 只在所有前置满足后执行授权离线实验 | 失败原地修补、反复还原碰运气 | live结果、持久化证据、边界说明 |

复杂批次B05、B07、B12在I/V内部还拆子场景；一次只处理其中一个。文档`tasks/Bxx-M.md`等可单独交给执行模型。阶段快照仅供lab加速：健康底座+本批材料，引用固定源/包/站点身份。最终每个正式交付组合仍需原始干净快照完整首装。

## 3. 共有实现清单

每个主组件都需要：配置结构与默认false；依赖/冲突/已实现列表；离线材料闭包；顺序role；必要就绪；独立smoke/acceptance；连接说明；具名测试；状态/用户手册。内部依赖只一个owner，不能用helm --take-ownership或kubectl --force-conflicts抢资源。

新Helm安装不使用--atomic：该开关在Helm3.20会失败时删除安装，违背本项目保存失败现场的边界。先ownership预检，再本地Chart install并等待。也不要用upgrade --install偷偷升级已存在release；首装/新增和升级是不同授权。原项目已有upgrade --install的受管首装路径应在R15明确限制。[WEB-HELM]

## 4. 批次总表

| 批次 | 内容 | 前置/边界 |
|---|---|---|
| B00 | 冻结既有底座和基础服务，不顺手升级 | R16 |
| B01 | Multus 接入；默认 VPC LoadBalancer 单独开关 | R10, R11, B00 |
| B02 | Milvus 单一固定组合 | B00 |
| B03 | Metrics Server：补齐PodMetrics API | B00 |
| B04 | CSI快照控制器、快照类与恢复 | B00 |
| B05 | KubeVirt与CDI：先CPU虚拟机，再导入持久磁盘 | B00；仅虚拟机快照场景额外依赖B04 |
| B06 | Volcano CPU 队列与gang调度 | B00 |
| B07 | Harbor、离线扫描、节点信任与准入接入 | B00 |
| B08 | Notebooks 控制器与CPU工作区 | B00 |
| B09 | Trainer与JobSet：CPU训练先闭环 | B00 |
| B10 | Hub / Model Registry 独立API | B00 |
| B11 | KServe Standard，无Istio、无业务Envoy | B00 |
| B12 | Pipelines：拆开依赖、执行器与工件验证 | B00 |
| B13 | 选定组件的整体离线收官 | B02, B03, B04, B05, B06, B07, B08, B09, B10, B11, B12 |
| O01 | 可选：Dex / 外部OIDC接入 | 仅用户明确选择；B00 |
| O02 | 可选：Kata沙箱及kata-monitor | 仅用户明确选择；B00 |
| O03 | 可选：Jaeger持久化追踪 | 仅用户明确选择；B00 |

所有依赖按实际启用集合计算。B13不要求强行开启所有可选组件；未启用分支是skipped。B01b默认VPC LB依赖外部网络条件，可blocked而不阻塞无此需求的Milvus。B09 CPU Trainer不要求Volcano gang对接已完成，更不依赖暂缓LWS/GPU。

---

# B00｜冻结既有底座和基础服务，不顺手升级

**状态：**计划待实施　**模式：**baseline　**前置：**R16

## 锁定组合
继承当前代码锁；完整96项研究记录见06-version-register.yaml

这是继承的设计组合，不是本次实装认证。M阶段必须取真实官方材料并锁摘要；不擅自换latest。
命名空间/所有权：沿用 rook-ceph / cert-manager / ani-platform / ani-observability。

## 拟新增配置契约
```text
保持已有组件字段，不新增“大而全”的统一配置。
components.certManager / postgresql / valkey / nats / metrics / logging 的开关与存储字段按当前代码。
storage 按R05；network按R10；新增全部组件仍默认false。
```
上述字段是本批实现目标，旧installer不支持；不能把小段配置当成已经可执行的完整site.yaml。

## M｜材料和依赖清单
记录Kubernetes 1.35.8、containerd 2.3.4、runc 1.4.3、Rook 1.20.7、Ceph 20.2.4、Ceph CSI 3.17.1/Operator 1.0.4。
记录cert-manager 1.21.2、PG 17.11、Valkey 8.1.10、NATS 2.14.6；metrics Chart85.4.0/Operator0.90.1/Prometheus3.11.3/Alertmanager0.32.1；Loki3.7.8 Chart18.13.3、Fluent Bit5.1.2 Chart0.58.2、OpenSearch3.8.0。
对应工具、子镜像、摘要全部从上传锁读取，不因本文件的简称遗漏。HAProxy/kube-vip仅物料历史项，当前local控制面路径不执行。

交付 `materials/<id>/material-report.md`、真实摘要锁、完整静态/动态/验证镜像清单、固定来源与支持条件。没有摘要、来源不可核验或硬件不满足时blocked；不编造。

## I｜实现与执行步骤
1. 对照研究矩阵、当前锁、归档和实际已有run，分别列 code/material/live 四种状态；无现网访问则不猜已部署。
2. PostgreSQL验证 pg_trgm 扩展是否随所选镜像可用，为知识库实际目标数据库启用扩展提供一个受控初始化Job；不把ANI全部业务migration塞进PG role。
3. 定义后续组件领取独立数据库/用户与RGW桶/用户的操作：Secret只含必要权限，连接说明只列Secret位置，不复制管理员密码。
4. 现有PG的ani_app不作为Hub/KFP/Harbor管理员账号；RGW服务端点与将来客户端公开签名端点分字段记录，验证者可达性明确。
5. cert-manager做内部CA/服务证书，无公网ACME隐含依赖。内部自签证书有效不代表客户浏览器自动信任。
6. 日志维持Loki/OpenSearch安装前二选一，Fluent Bit随选择；ANI已实现Loki查询不等于OpenSearch业务适配已实现。
7. 批次结束冻结本批累计artifact与健康底座快照标识，作为后续新增组件测试起点。

除本卡明确的网络/运行时扩展外，复用R15的组件新增入口。role→配置→材料→验证→连接输出必须同时注册。原生清单优先vendor固定来源并做小overlay；Chart依赖在Fedora材料侧闭合。禁止目标机helm repo update/dependency update或curl公网补包。

## T/V｜局部与真实验收标准
PG：写入/查询一条隔离测试记录、确认pg_trgm可用；Valkey：专用key读写；NATS：JetStream持久消息→消费者确认，不只TCP连通。
Ceph：RBD文件写读；CephFS两节点共享读；RGW专用前缀上传/下载/摘要一致。
metrics：目标真实采样；日志：唯一标记经Fluent Bit进入被选后端可查。专项重建后数据仍在。
关闭组件和另一日志后端的资源不得出现。

V前先通过T；正常Pod重建只能在acceptance中对声明目标执行一次；失败仅只读取证，不修底层组件。所有测试对象带runID标签，测试数据用独立库/桶前缀，不动业务数据。

## 人工检查命令
变量/文件均须来自实际render/connection输出，环境见04手册。`CHECKS_DIR` 下脚本是本批I阶段必须交付的产物，**本执行包没有假装已经提供这些组件实现**。

```bash
kubectl --request-timeout=10s get nodes -o wide
kubectl --request-timeout=10s get storageclass
kubectl --request-timeout=10s -n rook-ceph get cephcluster,cephfilesystem,cephobjectstore
kubectl --request-timeout=10s -n ani-platform get pods,svc,pvc
# 实际账户验证由本批输出的只读/隔离测试脚本执行，不打印Secret

```

端口转发在当前终端前台运行；请求测试在第二个可信终端执行，结束后Ctrl-C停止。`*_SERVICE_PORT`从本批实际Service和连接输出取得，不能猜端口。它不是已交付的ANI业务访问入口。

## 本批必须交付的操作材料
1. 具体role及配置测试；`scripts/acceptance/<component>/`中的具名检查器，并在code release内放到`checks/<component>/`。
2. 本批render目录的`acceptance/<component>/README.md`：填写真实resource名称/namespace/port、创建顺序、请求样例、预期输出和只读诊断命令；所有REPLACE在进入V前消除。
3. 工具镜像/SDK/wheel/测试模型/guest镜像等大物料的固定hash；不能只有controller镜像。
4. 连接信息：内部地址、端口、数据库/桶/类、TLS CA/Secret引用、作用域和未交付的业务能力。禁止输出密码/token。
5. task-result、代码/材料/实机/持久化/ANI接入五类状态和独立证据路径。

## 停止条件
任一既有依赖实际不健康，阻断依赖它的后续实机卡；不能用重新安装数据库、删PVC恢复。HAProxy材料维护不能擅自变成控制面HA改造。

## 资料
SRC-CODE, SRC-AUDIT, SRC-MATRIX，详见06版本登记与reference/official-sources.md。继承来源不表示本次逐项重新验证。


---

# B01｜Multus 接入；默认 VPC LoadBalancer 单独开关

**状态：**计划待实施　**模式：**fresh-cluster-network　**前置：**R10, R11, B00

## 锁定组合
Multus4.3.1 + CNI plugins1.9.0；Kube-OVN/vpc-nat-gateway1.16.6

这是继承的设计组合，不是本次实装认证。M阶段必须取真实官方材料并锁摘要；不擅自换latest。
命名空间/所有权：kube-system（共享NAD CRD只有一个owner）。

## 拟新增配置契约
```text
network.multus.enabled: bool=false；mode: thick（首版只支持此值）。
network.kubeovn.loadBalancer.enabled: bool=false；masterInterface/externalCIDR/gateway/excludeIPs/nodeSelector、NAD/Subnet名称见蓝图§6.3。
本批分B01a“仅Multus”和B01b“显式LB”验收；基本网络修复不要求LB=true。
```
上述字段是本批实现目标，旧installer不支持；不能把小段配置当成已经可执行的完整site.yaml。

## M｜材料和依赖清单
固定发行清单中的multus-daemon/shim镜像、CNI二进制（macvlan/bridge/tuning等按所选模式）、NAD CRD、所有init镜像。
LB额外需要同版vpc-nat-gateway镜像和Kube-OVN控制器创建它所需配置；镜像已在原包不代表能力已启用。
现场还需外部网卡、子网/网关/排除地址、可用于验证的外部主机。缺少这些只阻塞B01b，不擅自占用网段。

交付 `materials/<id>/material-report.md`、真实摘要锁、完整静态/动态/验证镜像清单、固定来源与支持条件。没有摘要、来源不可核验或硬件不满足时blocked；不编造。

## I｜实现与执行步骤
1. B01a.M记录containerd实际CNI confDir/binDir、主CNI配置文件、Multussocket目录；固定delegate，避免自动选择自身配置。
2. B01a.I新建ani/multus role，放在主CNI正常之后、业务Pod之前。保留原CNI delegate；更改conf文件的行为必须明确列入网络扩展计划，不从普通组件新增路径偷偷执行。
3. 检查已有NAD CRD：兼容已有owner则引用，不抢管理权；不兼容停止。只安装定义，不自动创建租户网络。
4. 使用两个独立指定IP的测试Pod/NAD或官方受控IPAM示例证明net1出现、同附加网互通、eth0和DNS不受影响；不拿一个host-local池直接跨节点使用，避免重复IP。
5. B01b.M核对官方默认VPC简化LB方案。NAD的macvlan master来自明确选择网卡，Subnet provider与NAD名/namespace关系以固定版模式为准；不要混抄.ovn和非.ovn方案。
6. B01b.I只在LB=true且依赖齐备时打开enable-lb-svc，生成NAD/Subnet/NAT镜像引用。默认false不输出LB资源。
7. 创建一个echo后端与LoadBalancer Service，分配的地址必须在授权池内、非排除地址，外部主机实际请求验证。
8. Kube-OVN自定义租户VPC公共LB不被此验收覆盖，单列业务适配；任何路径都不依赖kcn Envoy。

除本卡明确的网络/运行时扩展外，复用R15的组件新增入口。role→配置→材料→验证→连接输出必须同时注册。原生清单优先vendor固定来源并做小overlay；Chart依赖在Fedora材料侧闭合。禁止目标机helm repo update/dependency update或curl公网补包。

## T/V｜局部与真实验收标准
T-B01-01：Multus关闭时没有daemon/shim配置；开启后原主CNI delegate不丢失且无递归。
T-B01-02：Pod有eth0+net1，实际跨节点附加网络通信，默认路由/DNS不受破坏；重复/未授权IP被拒绝。
T-B01-03：LB关闭无隐式开启；LB开启但缺macvlan/外部配置前置失败。
T-B01-04：默认VPC LoadBalancer外部请求body正确；只有EXTERNAL-IP状态不算通过。
T-B01-05：执行轨迹与资源diff无kcn专属Envoy变化。

V前先通过T；正常Pod重建只能在acceptance中对声明目标执行一次；失败仅只读取证，不修底层组件。所有测试对象带runID标签，测试数据用独立库/桶前缀，不动业务数据。

## 人工检查命令
变量/文件均须来自实际render/connection输出，环境见04手册。`CHECKS_DIR` 下脚本是本批I阶段必须交付的产物，**本执行包没有假装已经提供这些组件实现**。

```bash
kubectl --request-timeout=10s get crd network-attachment-definitions.k8s.cni.cncf.io
kubectl --request-timeout=10s -n kube-system get daemonset,pod -o wide
kubectl --request-timeout=10s -n "$TEST_NS" get pods -o wide
timeout 30s kubectl --request-timeout=10s -n "$TEST_NS" exec "$CLIENT_POD" -- ip -j address
timeout 30s kubectl --request-timeout=10s -n "$TEST_NS" exec "$CLIENT_POD" -- ip route
kubectl --request-timeout=10s -n "$TEST_NS" get svc "$LB_SERVICE" -o wide
# 在授权外部验证机上：IP/port从本批实际输出读取
curl --connect-timeout 5 --max-time 15 "http://${LB_IP}:${LB_PORT}/"

```

端口转发在当前终端前台运行；请求测试在第二个可信终端执行，结束后Ctrl-C停止。`*_SERVICE_PORT`从本批实际Service和连接输出取得，不能猜端口。它不是已交付的ANI业务访问入口。

## 本批必须交付的操作材料
1. 具体role及配置测试；`scripts/acceptance/<component>/`中的具名检查器，并在code release内放到`checks/<component>/`。
2. 本批render目录的`acceptance/<component>/README.md`：填写真实resource名称/namespace/port、创建顺序、请求样例、预期输出和只读诊断命令；所有REPLACE在进入V前消除。
3. 工具镜像/SDK/wheel/测试模型/guest镜像等大物料的固定hash；不能只有controller镜像。
4. 连接信息：内部地址、端口、数据库/桶/类、TLS CA/Secret引用、作用域和未交付的业务能力。禁止输出密码/token。
5. task-result、代码/材料/实机/持久化/ANI接入五类状态和独立证据路径。

## 停止条件
基础网络不通则回R10/R11；Multus/上游LB出错只取证，不清OVN、不安装MetalLB/Envoy替代，不自行分配外部地址。

## 资料
SRC-MATRIX, WEB-NET, WEB-MULTUS，详见06版本登记与reference/official-sources.md。继承来源不表示本次逐项重新验证。


---

# B02｜Milvus 单一固定组合

**状态：**计划待实施　**模式：**components　**前置：**B00

## 锁定组合
Milvus2.6.24 / Chart5.0.25；专用etcd3.5.25-r1 / 子Chart8.12.0；RocksMQ内嵌

这是继承的设计组合，不是本次实装认证。M阶段必须取真实官方材料并锁摘要；不擅自换latest。
命名空间/所有权：建议ani-platform，release=milvus（本批固定，不随模型改名）。

## 拟新增配置契约
```text
components.milvus.enabled: bool=false
components.milvus.storageClass / storageSize / etcdStorageSize: 必填正容量
components.milvus.objectStore.endpoint / bucket / region / credentialsSecret / useTLS / caSecret: 显式输入
单副本Standalone为本批唯一模式；不提供replicas任意扩成HA的开关。
```
上述字段是本批实现目标，旧installer不支持；不能把小段配置当成已经可执行的完整site.yaml。

## M｜材料和依赖清单
Chart5.0.25原配app2.6.21，本计划显式覆盖2.6.24，必须记录为待本地验收组合。
固定Milvus、专用etcd、init/权限配置/验证用镜像；验证镜像内预装与服务兼容的pymilvus及依赖，版本和wheel hash在M阶段真实锁定，不能假定SDK版本等于server版本。
RGW使用既有服务，独立桶/凭据；RocksMQ PVC与etcd PVC各自持久化。

交付 `materials/<id>/material-report.md`、真实摘要锁、完整静态/动态/验证镜像清单、固定来源与支持条件。没有摘要、来源不可核验或硬件不满足时blocked；不编造。

## I｜实现与执行步骤
1. M阶段下载固定Chart及子Chart，读取真实values字段；写values-contract.md列“源字段→ANI字段”，不要根据别的Chart猜名称。
2. 禁用Chart内置MinIO、Pulsar/Pulsarv3、Kafka、外部Woodpecker及非Standalone分支；render资源清单证明未混入。
3. 为Milvus创建专用etcd，不借用Kubernetes控制面etcd；创建独立RGW访问凭据与bucket，证据不输出密钥。
4. 新增ani/milvus role、强类型配置、required材料、连接输出、smoke/acceptance。image tag覆盖在可审计values中，不修改供应商Chart源码凑版本。
5. API测试使用唯一collection，插入确定的ID/向量，flush、创建索引、load、search；断言返回同一最近邻ID，不只检查健康端口。
6. 验收各重建一次Milvus与专用etcd目标Pod（分场景，非重复恢复），保持PVC UID，重连读回相同数据/索引。
7. 本批首装为单节点服务语义，不宣称分布式HA；后续WAL/拓扑转换另开任务。

除本卡明确的网络/运行时扩展外，复用R15的组件新增入口。role→配置→材料→验证→连接输出必须同时注册。原生清单优先vendor固定来源并做小overlay；Chart依赖在Fedora材料侧闭合。禁止目标机helm repo update/dependency update或curl公网补包。

## T/V｜局部与真实验收标准
局部：关闭依赖资源不出现；wrong bucket/empty password/无etcd材料失败；Chart app覆盖可见。
实机：插入如IDs[1,2,3]、固定4维向量并搜索ID1相同向量，top1=1；存在持久元数据与对象。
错误端点/权限拒绝在预检或smoke准确失败，不能创建公开匿名bucket绕过。
专项：UID改变/PVC不变后查询仍正确。

V前先通过T；正常Pod重建只能在acceptance中对声明目标执行一次；失败仅只读取证，不修底层组件。所有测试对象带runID标签，测试数据用独立库/桶前缀，不动业务数据。

## 人工检查命令
变量/文件均须来自实际render/connection输出，环境见04手册。`CHECKS_DIR` 下脚本是本批I阶段必须交付的产物，**本执行包没有假装已经提供这些组件实现**。

```bash
kubectl --request-timeout=10s -n ani-platform get pods,svc,pvc -l app.kubernetes.io/instance=milvus
# 本批I阶段必须交付此检查脚本及固定客户端镜像；原仓库没有此文件
bash "$CHECKS_DIR/milvus/smoke.sh" --run "$RUN_JSON"
# 脚本输出collection、插入ID、检索ID、耗时与结果文件，不输出密钥

```

端口转发在当前终端前台运行；请求测试在第二个可信终端执行，结束后Ctrl-C停止。`*_SERVICE_PORT`从本批实际Service和连接输出取得，不能猜端口。它不是已交付的ANI业务访问入口。

## 本批必须交付的操作材料
1. 具体role及配置测试；`scripts/acceptance/<component>/`中的具名检查器，并在code release内放到`checks/<component>/`。
2. 本批render目录的`acceptance/<component>/README.md`：填写真实resource名称/namespace/port、创建顺序、请求样例、预期输出和只读诊断命令；所有REPLACE在进入V前消除。
3. 工具镜像/SDK/wheel/测试模型/guest镜像等大物料的固定hash；不能只有controller镜像。
4. 连接信息：内部地址、端口、数据库/桶/类、TLS CA/Secret引用、作用域和未交付的业务能力。禁止输出密码/token。
5. task-result、代码/材料/实机/持久化/ANI接入五类状态和独立证据路径。

## 停止条件
官方材料不存在/摘要不符、RGW实际操作不兼容则block并保留请求证据；不临时换MinIO/RustFS/新Milvus版本。

## 资料
SRC-MATRIX:S21-S25，详见06版本登记与reference/official-sources.md。继承来源不表示本次逐项重新验证。


---

# B03｜Metrics Server：补齐PodMetrics API

**状态：**计划待实施　**模式：**components　**前置：**B00

## 锁定组合
0.9.0 / Chart3.14.0

这是继承的设计组合，不是本次实装认证。M阶段必须取真实官方材料并锁摘要；不擅自换latest。
命名空间/所有权：建议kube-system，release=metrics-server。

## 拟新增配置契约
```text
components.metricsServer.enabled: bool=false
components.metricsServer.kubeletCASecret: 可选证书引用
默认禁止kubelet-insecure-tls；实验豁免只能显式lab字段且结果不得记生产安全通过。
```
上述字段是本批实现目标，旧installer不支持；不能把小段配置当成已经可执行的完整site.yaml。

## M｜材料和依赖清单
metrics-server镜像、Chart、证书/aggregation API所需配置、CPU测试Pod镜像。既有Prometheus不是这个API提供者。
核对kubelet serving证书地址/SAN和CA、API aggregation设置；支持范围来自所选官方版本，不用skip TLS掩盖。

交付 `materials/<id>/material-report.md`、真实摘要锁、完整静态/动态/验证镜像清单、固定来源与支持条件。没有摘要、来源不可核验或硬件不满足时blocked；不编造。

## I｜实现与执行步骤
1. 先只读检查metrics.k8s.io APIService是否已有owner；已有兼容提供者则引用，不第二次部署。
2. 阅读Chart真实字段，锁本地image及资源、toleration；不要硬编码kubelet IP类型导致证书不匹配。
3. 安装后等待APIService Available，创建可观测的CPU测试Pod，等待metrics采样并给出明确总超时。
4. 请求该Pod的PodMetrics，检查CPU/内存量值与新近timestamp，而非仅kubectl top成功退出。
5. 连接输出报告API组、支持对象和证书验证方式；与Prometheus单独记录。

除本卡明确的网络/运行时扩展外，复用R15的组件新增入口。role→配置→材料→验证→连接输出必须同时注册。原生清单优先vendor固定来源并做小overlay；Chart依赖在Fedora材料侧闭合。禁止目标机helm repo update/dependency update或curl公网补包。

## T/V｜局部与真实验收标准
存在同名APIService冲突拒绝；APIService Available但采不到指定Pod返回fail；陈旧采样不能误判。
TLS失败输出SAN/CA问题，不自动加--kubelet-insecure-tls。
关闭此组件不影响原Prometheus。

V前先通过T；正常Pod重建只能在acceptance中对声明目标执行一次；失败仅只读取证，不修底层组件。所有测试对象带runID标签，测试数据用独立库/桶前缀，不动业务数据。

## 人工检查命令
变量/文件均须来自实际render/connection输出，环境见04手册。`CHECKS_DIR` 下脚本是本批I阶段必须交付的产物，**本执行包没有假装已经提供这些组件实现**。

```bash
kubectl --request-timeout=10s get apiservice v1beta1.metrics.k8s.io
kubectl --request-timeout=10s get --raw "/apis/metrics.k8s.io/v1beta1/namespaces/${TEST_NS}/pods/${TEST_POD}"
kubectl --request-timeout=10s top pod -n "$TEST_NS" "$TEST_POD"

```

端口转发在当前终端前台运行；请求测试在第二个可信终端执行，结束后Ctrl-C停止。`*_SERVICE_PORT`从本批实际Service和连接输出取得，不能猜端口。它不是已交付的ANI业务访问入口。

## 本批必须交付的操作材料
1. 具体role及配置测试；`scripts/acceptance/<component>/`中的具名检查器，并在code release内放到`checks/<component>/`。
2. 本批render目录的`acceptance/<component>/README.md`：填写真实resource名称/namespace/port、创建顺序、请求样例、预期输出和只读诊断命令；所有REPLACE在进入V前消除。
3. 工具镜像/SDK/wheel/测试模型/guest镜像等大物料的固定hash；不能只有controller镜像。
4. 连接信息：内部地址、端口、数据库/桶/类、TLS CA/Secret引用、作用域和未交付的业务能力。禁止输出密码/token。
5. task-result、代码/材料/实机/持久化/ANI接入五类状态和独立证据路径。

## 停止条件
证书不匹配停在证书/节点配置整改，不能关闭TLS让界面暂时有数值。

## 资料
SRC-MATRIX:S19，详见06版本登记与reference/official-sources.md。继承来源不表示本次逐项重新验证。


---

# B04｜CSI快照控制器、快照类与恢复

**状态：**计划待实施　**模式：**components　**前置：**B00

## 锁定组合
external-snapshotter8.5.0；保留Ceph CSI3.17.1

这是继承的设计组合，不是本次实装认证。M阶段必须取真实官方材料并锁摘要；不擅自换latest。
命名空间/所有权：snapshot-controller目标namespace=ani-snapshot-system（本方案新命名）；CRD全局唯一owner。

## 拟新增配置契约
```text
components.snapshotController.enabled: bool=false
components.snapshotController.rbdClassName: ani-rbd-snapshot
components.snapshotController.cephfsClassName: ani-cephfs-snapshot
components.snapshotController.deletionPolicy: Retain（本方案保守默认）
components.snapshotController.makeDefault: bool=false；需ANI未指定类的路径时显式启用并检查同driver无冲突。
```
上述字段是本批实现目标，旧installer不支持；不能把小段配置当成已经可执行的完整site.yaml。

## M｜材料和依赖清单
同版VolumeSnapshot/VolumeSnapshotContent/VolumeSnapshotClass CRDs、全局snapshot-controller和已有sidecar。
RBD/CephFS快照类driver、clusterID、secretName以实际StorageClass与所选Rook固定示例核对。Retain会留后端快照，收尾清理需单独授权。

交付 `materials/<id>/material-report.md`、真实摘要锁、完整静态/动态/验证镜像清单、固定来源与支持条件。没有摘要、来源不可核验或硬件不满足时blocked；不编造。

## I｜实现与执行步骤
1. 检查已有snapshot CRDs/controller，兼容则引用；不把CSI sidecar当全局controller。
2. 先CRDs Established，再RBAC/controller Available，再快照类。快照类driver必须与源PVC的CSI驱动一致。
3. 新建专用PVC并写唯一标记，停止写入，创建显式volumeSnapshotClassName的VolumeSnapshot。
4. 等待readyToUse=true及绑定content；创建一个新PVC，以该快照为dataSource，启动只读验证Pod并比对标记。
5. 不删除原PVC，不重写已有class，不声称运行中数据库应用一致性/内存checkpoint通过。
6. 如为ANI旧代码提供默认快照类：每个driver最多一个默认类，先检查冲突，显式配置才标记。
7. RBD与CephFS分别测试，失败的driver不借另一个结果替代。

除本卡明确的网络/运行时扩展外，复用R15的组件新增入口。role→配置→材料→验证→连接输出必须同时注册。原生清单优先vendor固定来源并做小overlay；Chart依赖在Fedora材料侧闭合。禁止目标机helm repo update/dependency update或curl公网补包。

## T/V｜局部与真实验收标准
Snapshot ready但恢复PVC数据错→fail；源/目标驱动不匹配前置失败；存在第二默认class冲突拒绝。
恢复PVC UID应不同于源，内容一致；测试不能使用原PVC冒充恢复。
无权限/secret错误不得换用集群管理员secret绕过。

V前先通过T；正常Pod重建只能在acceptance中对声明目标执行一次；失败仅只读取证，不修底层组件。所有测试对象带runID标签，测试数据用独立库/桶前缀，不动业务数据。

## 人工检查命令
变量/文件均须来自实际render/connection输出，环境见04手册。`CHECKS_DIR` 下脚本是本批I阶段必须交付的产物，**本执行包没有假装已经提供这些组件实现**。

```bash
kubectl --request-timeout=10s get volumesnapshotclasses.snapshot.storage.k8s.io
kubectl --request-timeout=10s -n "$TEST_NS" get volumesnapshot "$SNAPSHOT_NAME" -o jsonpath='{.status.readyToUse}{"\n"}'
kubectl --request-timeout=10s -n "$TEST_NS" get pvc "$RESTORED_PVC" -o jsonpath='{.status.phase}{"\n"}'
# 示例清单见templates/snapshot-restore.example.yaml；先替换明确占位符再apply

```

端口转发在当前终端前台运行；请求测试在第二个可信终端执行，结束后Ctrl-C停止。`*_SERVICE_PORT`从本批实际Service和连接输出取得，不能猜端口。它不是已交付的ANI业务访问入口。

## 本批必须交付的操作材料
1. 具体role及配置测试；`scripts/acceptance/<component>/`中的具名检查器，并在code release内放到`checks/<component>/`。
2. 本批render目录的`acceptance/<component>/README.md`：填写真实resource名称/namespace/port、创建顺序、请求样例、预期输出和只读诊断命令；所有REPLACE在进入V前消除。
3. 工具镜像/SDK/wheel/测试模型/guest镜像等大物料的固定hash；不能只有controller镜像。
4. 连接信息：内部地址、端口、数据库/桶/类、TLS CA/Secret引用、作用域和未交付的业务能力。禁止输出密码/token。
5. task-result、代码/材料/实机/持久化/ANI接入五类状态和独立证据路径。

## 停止条件
CRD版本/driver能力冲突停止，不删除旧CRD/PVC“重新试”。

## 资料
SRC-MATRIX:S20, WEB-ROOK-SNAPSHOT，详见06版本登记与reference/official-sources.md。继承来源不表示本次逐项重新验证。


---

# B05｜KubeVirt与CDI：先CPU虚拟机，再导入持久磁盘

**状态：**计划待实施　**模式：**components　**前置：**B00；仅虚拟机快照场景额外依赖B04

## 锁定组合
KubeVirt/virtctl1.9.0；CDI1.66.1

这是继承的设计组合，不是本次实装认证。M阶段必须取真实官方材料并锁摘要；不擅自换latest。
命名空间/所有权：kubevirt / cdi；测试VM在独立namespace。

## 拟新增配置契约
```text
components.kubevirt.enabled: bool=false；components.cdi.enabled: bool=false
components.cdi.storageClass / scratchStorageClass: 显式实际类
components.kubevirt.allowEmulation: bool=false（实验模拟不能替代KVM验收）
VM附加网卡只有明确选择B01且通过时使用；不默认启用迁移/热插拔。
```
上述字段是本批实现目标，旧installer不支持；不能把小段配置当成已经可执行的完整site.yaml。

## M｜材料和依赖清单
固定operator/CR、virt-api/controller/handler/launcher、virtctl、CDI importer/uploadserver/cloner/worker/scratch镜像和轻量guest/containerdisk。
真实测试需/dev/kvm及虚拟化暴露；Nested virtualization必须现场确认。guest镜像与SHA、内部HTTP/S3测试来源需制包锁定。

交付 `materials/<id>/material-report.md`、真实摘要锁、完整静态/动态/验证镜像清单、固定来源与支持条件。没有摘要、来源不可核验或硬件不满足时blocked；不编造。

## I｜实现与执行步骤
1. B05.1先安装KubeVirt operator/CR，验证KVM与CPU guest启动；CDI可以尚未启用，容器磁盘样例仅证明启动链。
2. B05.2安装CDI operator/CR，设置实际StorageProfile/默认storageClass；把importer动态镜像替换到本地，不只operator。
3. 从固定内部HTTP/S3地址创建DataVolume，等待Succeeded，检查PVC；若WaitForFirstConsumer须按所选storage模式处理，不误报永久卡死。
4. VM挂该DV启动，用virtctl console或guest输出验证真的执行过命令；VMI Ready不单独证明guest磁盘内容正确。
5. 关闭VM后再次启动，guest磁盘标记存在；不删除DV/PVC，不把临时containerdisk持久性当DV持久性。
6. B05.3按需接入快照和附加网络：快照恢复验证依赖B04；net1验证依赖B01。未选功能不强行装Multus或改默认网络。
7. CPU VM生命周期、磁盘导入、可选网络/快照分别记录；迁移不在本批必做，若选择另定义存储和网络前置。

除本卡明确的网络/运行时扩展外，复用R15的组件新增入口。role→配置→材料→验证→连接输出必须同时注册。原生清单优先vendor固定来源并做小overlay；Chart依赖在Fedora材料侧闭合。禁止目标机helm repo update/dependency update或curl公网补包。

## T/V｜局部与真实验收标准
CPU guest真实启动；DV内部来源导入完成；重启VM后标记存在；缺virt-launcher/importer镜像在M/T阶段拦住。
源镜像不可达/摘要错、KVM不具备时准确block，不能默认software emulation并宣称硬件路径通过。
Multus未选时VM默认网仍可用，kcn Envoy不被业务VM流程接管。

V前先通过T；正常Pod重建只能在acceptance中对声明目标执行一次；失败仅只读取证，不修底层组件。所有测试对象带runID标签，测试数据用独立库/桶前缀，不动业务数据。

## 人工检查命令
变量/文件均须来自实际render/connection输出，环境见04手册。`CHECKS_DIR` 下脚本是本批I阶段必须交付的产物，**本执行包没有假装已经提供这些组件实现**。

```bash
kubectl --request-timeout=10s -n kubevirt get kubevirt
kubectl --request-timeout=10s -n cdi get cdi
kubectl --request-timeout=10s -n "$TEST_NS" get datavolume,pvc,vm,vmi -o wide
timeout 330s kubectl -n "$TEST_NS" wait --for=condition=Ready "vmi/${VM_NAME}" --timeout=300s
"$VIRTCTL" -n "$TEST_NS" console "$VM_NAME"
# DV/VM实例来自本批render生成的固定版本样例，不从main临时抓取

```

端口转发在当前终端前台运行；请求测试在第二个可信终端执行，结束后Ctrl-C停止。`*_SERVICE_PORT`从本批实际Service和连接输出取得，不能猜端口。它不是已交付的ANI业务访问入口。

## 本批必须交付的操作材料
1. 具体role及配置测试；`scripts/acceptance/<component>/`中的具名检查器，并在code release内放到`checks/<component>/`。
2. 本批render目录的`acceptance/<component>/README.md`：填写真实resource名称/namespace/port、创建顺序、请求样例、预期输出和只读诊断命令；所有REPLACE在进入V前消除。
3. 工具镜像/SDK/wheel/测试模型/guest镜像等大物料的固定hash；不能只有controller镜像。
4. 连接信息：内部地址、端口、数据库/桶/类、TLS CA/Secret引用、作用域和未交付的业务能力。禁止输出密码/token。
5. task-result、代码/材料/实机/持久化/ANI接入五类状态和独立证据路径。

## 停止条件
磁盘/虚拟化前置不足停在预检；不能通过清盘、删除PVC或修改组件源码解决。

## 资料
SRC-MATRIX:S26-S27，详见06版本登记与reference/official-sources.md。继承来源不表示本次逐项重新验证。


---

# B06｜Volcano CPU 队列与gang调度

**状态：**计划待实施　**模式：**components　**前置：**B00

## 锁定组合
1.15.2

这是继承的设计组合，不是本次实装认证。M阶段必须取真实官方材料并锁摘要；不擅自换latest。
命名空间/所有权：volcano-system。

## 拟新增配置契约
```text
components.volcano.enabled: bool=false
components.volcano.schedulerName: volcano（首版固定值）
本批不提供gpu/vgpu/lws自动开关。全局默认调度器保持不变。
```
上述字段是本批实现目标，旧installer不支持；不能把小段配置当成已经可执行的完整site.yaml。

## M｜材料和依赖清单
scheduler/controllers/admission、webhook证书Job、所需CRDs与固定CPU训练/测试镜像。按所选Chart或固定发行清单原配；不同时安装另一份调度器。
验收需要明确的可用CPU/内存预算，不能把资源不足误判成调度器故障。

交付 `materials/<id>/material-report.md`、真实摘要锁、完整静态/动态/验证镜像清单、固定来源与支持条件。没有摘要、来源不可核验或硬件不满足时blocked；不编造。

## I｜实现与执行步骤
1. 仅安装CPU调度控制面，保留default-scheduler；工作负载显式schedulerName=volcano。
2. 创建专用Queue与minAvailable>1的测试Job/PodGroup，先用可控资源条件使其整体等待，再释放仅本测试创建的占位资源。
3. 观察PodGroup条件、scheduler事件和同时满足gang条件后的成员调度，不用两个普通Pod恰好运行代替gang验收。
4. 完成CPU任务，日志含固定结果；测试取消只删除本run的Job，等待其Pod终止、资源统计恢复。
5. 验证默认调度器的已有业务Pod不受影响。暂缓LWS和GPU组件不得出现在render/镜像新增列表。
6. Trainer的JobSet与Volcano对接不是本批默认完成事项；后续B09分别记CPU运行与gang对接状态。

除本卡明确的网络/运行时扩展外，复用R15的组件新增入口。role→配置→材料→验证→连接输出必须同时注册。原生清单优先vendor固定来源并做小overlay；Chart依赖在Fedora材料侧闭合。禁止目标机helm repo update/dependency update或curl公网补包。

## T/V｜局部与真实验收标准
资源未满足minAvailable时不部分误启动；资源满足后完成；取消后无孤儿Pod。
自有测试Queue不覆盖用户Queue；webhook就绪和证书校验通过。
render不含volcano-vgpu-device-plugin、GPU Operator、LWS；不改default-scheduler。

V前先通过T；正常Pod重建只能在acceptance中对声明目标执行一次；失败仅只读取证，不修底层组件。所有测试对象带runID标签，测试数据用独立库/桶前缀，不动业务数据。

## 人工检查命令
变量/文件均须来自实际render/connection输出，环境见04手册。`CHECKS_DIR` 下脚本是本批I阶段必须交付的产物，**本执行包没有假装已经提供这些组件实现**。

```bash
kubectl --request-timeout=10s -n volcano-system get deploy,pods
kubectl --request-timeout=10s get queue.scheduling.volcano.sh
kubectl --request-timeout=10s -n "$TEST_NS" get podgroup.scheduling.volcano.sh
kubectl --request-timeout=10s -n "$TEST_NS" get jobs.batch.volcano.sh,pods -o wide
kubectl --request-timeout=10s -n "$TEST_NS" get events --sort-by=.metadata.creationTimestamp

```

端口转发在当前终端前台运行；请求测试在第二个可信终端执行，结束后Ctrl-C停止。`*_SERVICE_PORT`从本批实际Service和连接输出取得，不能猜端口。它不是已交付的ANI业务访问入口。

## 本批必须交付的操作材料
1. 具体role及配置测试；`scripts/acceptance/<component>/`中的具名检查器，并在code release内放到`checks/<component>/`。
2. 本批render目录的`acceptance/<component>/README.md`：填写真实resource名称/namespace/port、创建顺序、请求样例、预期输出和只读诊断命令；所有REPLACE在进入V前消除。
3. 工具镜像/SDK/wheel/测试模型/guest镜像等大物料的固定hash；不能只有controller镜像。
4. 连接信息：内部地址、端口、数据库/桶/类、TLS CA/Secret引用、作用域和未交付的业务能力。禁止输出密码/token。
5. task-result、代码/材料/实机/持久化/ANI接入五类状态和独立证据路径。

## 停止条件
gang测试资源预算/队列状态不符合前置时先纠正fixture；不安装额外调度体系凑结果。

## 资料
SRC-MATRIX:S28，详见06版本登记与reference/official-sources.md。继承来源不表示本次逐项重新验证。


---

# B07｜Harbor、离线扫描、节点信任与准入接入

**状态：**计划待实施　**模式：**components　**前置：**B00

## 锁定组合
Harbor2.15.2 / Chart1.19.2；adapter镜像2.15.2、源码0.38.0、Trivy0.72.0

这是继承的设计组合，不是本次实装认证。M阶段必须取真实官方材料并锁摘要；不擅自换latest。
命名空间/所有权：harbor，release=harbor。

## 拟新增配置契约
```text
components.harbor.enabled: bool=false；externalURL: 必填确定HTTPS域名/IP
storageClass / storageSize / databaseStorageSize / cacheStorageSize: 显式容量
expose.type: nodePort（首轮不依赖任何Envoy）；expose.httpsPort: 合法非冲突端口
tlsSecret / caSecret / adminSecret: Secret引用；trivy.enabled=true；trivy.databaseArtifact: 已批准离线DB快照引用
复用内置专用DB/缓存；复用平台PG是另一profile，不在本批同时做。
```
上述字段是本批实现目标，旧installer不支持；不能把小段配置当成已经可执行的完整site.yaml。

## M｜材料和依赖清单
完整Harbor镜像族，专用DB/Valkey镜像，不把photon tag当上游数据库版本。Trivy adapter和扫描DB/Java DB及metadata、更新时间、摘要。
节点与推镜像客户端需TLS信任链，测试镜像使用可公开分发且离线可扫描的固定样例；禁止借用kcn Envoy。
不预填“最近漏洞DB”标签，冻结实际拿到的快照，并标数据年龄。

交付 `materials/<id>/material-report.md`、真实摘要锁、完整静态/动态/验证镜像清单、固定来源与支持条件。没有摘要、来源不可核验或硬件不满足时blocked；不编造。

## I｜实现与执行步骤
1. B07.1先按固定Chart渲染TLS NodePort，核对externalURL与实际节点入口一致；不默认type=LoadBalancer或Ingress带入业务Envoy。
2. 持久卷、内置DB/缓存、jobservice、registry、scanner按完整依赖就绪；首次失败保留Helm现场，不用--atomic抹证据。
3. 创建专用项目和最小权限robot测试账号，推送与拉取同一digest；测试匿名pull按项目策略被允许或拒绝，不能默认全公开。
4. B07.2预置扫描数据库，关闭在线更新且明确离线模式；只改offlineScan并不足够。触发scan，等待终态，报告scanner版本/DB日期/扫描结果摘要。
5. 在测试节点配置显式Harbor CA信任与拉取凭据，验证实际容器runtime能pull；Docker客户端成功不代表containerd已信任。
6. B07.3独立记录ANI接口/镜像门禁集成：把真实扫描摘要交ANI验证。不改ANI默认enforce，不以镜像push/pull通过冒充准入完成。
7. 原自举registry继续服务底座镜像；本批没有自动交接、改写所有existing镜像或停止Hauler。

除本卡明确的网络/运行时扩展外，复用R15的组件新增入口。role→配置→材料→验证→连接输出必须同时注册。原生清单优先vendor固定来源并做小overlay；Chart依赖在Fedora材料侧闭合。禁止目标机helm repo update/dependency update或curl公网补包。

## T/V｜局部与真实验收标准
TLS正确、错误CA失败；robot只能访问授权项目；push digest=pull digest。
无公网条件下scan终态成功且DB非空；扫描失败可定位数据库/镜像访问而不是关门禁。
重建Harbor registry/DB单个受控Pod后项目/镜像/扫描结果仍在；原基础组件运行不变。
有已知测试策略拒绝样例，ANI拒绝理由符合实际扫描，不空造CVE。

V前先通过T；正常Pod重建只能在acceptance中对声明目标执行一次；失败仅只读取证，不修底层组件。所有测试对象带runID标签，测试数据用独立库/桶前缀，不动业务数据。

## 人工检查命令
变量/文件均须来自实际render/connection输出，环境见04手册。`CHECKS_DIR` 下脚本是本批I阶段必须交付的产物，**本执行包没有假装已经提供这些组件实现**。

```bash
kubectl --request-timeout=10s -n harbor get pods,svc,pvc
# 密码通过受保护文件/stdin，不放进命令字面量
skopeo inspect --authfile "$HARBOR_AUTHFILE" "docker://${HARBOR_HOST}/${TEST_PROJECT}/${TEST_IMAGE}@${TEST_DIGEST}"
# 本批脚本记录scan任务ID、终态和DB metadata，不能在shell开启set -x
bash "$CHECKS_DIR/harbor/scan.sh" --run "$RUN_JSON"

```

端口转发在当前终端前台运行；请求测试在第二个可信终端执行，结束后Ctrl-C停止。`*_SERVICE_PORT`从本批实际Service和连接输出取得，不能猜端口。它不是已交付的ANI业务访问入口。

## 本批必须交付的操作材料
1. 具体role及配置测试；`scripts/acceptance/<component>/`中的具名检查器，并在code release内放到`checks/<component>/`。
2. 本批render目录的`acceptance/<component>/README.md`：填写真实resource名称/namespace/port、创建顺序、请求样例、预期输出和只读诊断命令；所有REPLACE在进入V前消除。
3. 工具镜像/SDK/wheel/测试模型/guest镜像等大物料的固定hash；不能只有controller镜像。
4. 连接信息：内部地址、端口、数据库/桶/类、TLS CA/Secret引用、作用域和未交付的业务能力。禁止输出密码/token。
5. task-result、代码/材料/实机/持久化/ANI接入五类状态和独立证据路径。

## 停止条件
证书/扫描数据库材料未闭合就block；不装Envoy替代NodePort，不关扫描，不停原registry。

## 资料
SRC-MATRIX:S29-S31, WEB-HELM，详见06版本登记与reference/official-sources.md。继承来源不表示本次逐项重新验证。


---

# B08｜Notebooks 控制器与CPU工作区

**状态：**计划待实施　**模式：**components　**前置：**B00

## 锁定组合
Notebook Controller1.10.0（Kubeflow26.03）

这是继承的设计组合，不是本次实装认证。M阶段必须取真实官方材料并锁摘要；不擅自换latest。
命名空间/所有权：kubeflow（单一命名空间仍不等于安全多租户）。

## 拟新增配置契约
```text
components.notebooks.enabled: bool=false
components.notebooks.storageClass / workspaceSize: 必填
components.notebooks.cpuImage: 本批批准的镜像键，不接受未知公网ref
components.notebooks.useIstio: 固定false；exposure: internal（本批固定）
不自动安装Jupyter WebApp/Profile/Dashboard/PodDefaults。
```
上述字段是本批实现目标，旧installer不支持；不能把小段配置当成已经可执行的完整site.yaml。

## M｜材料和依赖清单
Notebook CRD/controller、CPU Jupyter工作区镜像（基础OS/Python包闭合）、探测工具镜像。CPU镜像的具体tag与digest在M阶段来自官方固定发行资源/构建输入；不能认为controller版本就是所有Notebook镜像版本。
如镜像需要下载扩展或pip安装，预装到镜像，目标机不联网。

交付 `materials/<id>/material-report.md`、真实摘要锁、完整静态/动态/验证镜像清单、固定来源与支持条件。没有摘要、来源不可核验或硬件不满足时blocked；不编造。

## I｜实现与执行步骤
1. 只vendoring指定Notebook controller路径，渲染USE_ISTIO=false及所有配置引用；保留证书/RBAC实际依赖，不把整套distribution安装后删Istio。
2. 注册CPU镜像和workspace PVC契约，限制权限与挂载，不给Notebook默认cluster-admin。
3. 创建Notebook CR，观察控制器生成StatefulSet/Service；确认CPU Pod无nvidia.com/gpu请求。
4. 初期只通过授权管理员的本地port-forward测试Jupyter，不把无鉴权Notebook端口开放到所有用户。
5. 创建一个文件并计算其内容摘要，计划内重建工作区Pod，PVC UID不变且文件仍在；不要重建controller去假装工作区持久性。
6. 外部访问/SSO/动态路由是ANI适配，记ani_integration=not_verified；不复用kcn Envoy，不默默加Dex/业务Envoy。
7. 所有关闭的WebApp/Profiles/Istio对象应在render中缺失。

除本卡明确的网络/运行时扩展外，复用R15的组件新增入口。role→配置→材料→验证→连接输出必须同时注册。原生清单优先vendor固定来源并做小overlay；Chart依赖在Fedora材料侧闭合。禁止目标机helm repo update/dependency update或curl公网补包。

## T/V｜局部与真实验收标准
Notebook CR创建→CPU工作区可执行cell/文件操作；不同Notebook使用独立PVC和ServiceAccount。
USE_ISTIO=false真正生效，不创建VirtualService/Gateway依赖。
测试不含在线pip/npm/模型下载；工作区重建后文件保留。
内部测试成功不得报告公网安全多租户已完成。

V前先通过T；正常Pod重建只能在acceptance中对声明目标执行一次；失败仅只读取证，不修底层组件。所有测试对象带runID标签，测试数据用独立库/桶前缀，不动业务数据。

## 人工检查命令
变量/文件均须来自实际render/connection输出，环境见04手册。`CHECKS_DIR` 下脚本是本批I阶段必须交付的产物，**本执行包没有假装已经提供这些组件实现**。

```bash
kubectl --request-timeout=10s -n "$TEST_NS" get notebooks.kubeflow.org
kubectl --request-timeout=10s -n "$TEST_NS" get statefulset,pods,svc,pvc
timeout 30s kubectl --request-timeout=10s -n "$TEST_NS" exec "$NOTEBOOK_POD" -- sh -c 'id; test -d /home/jovyan'
# 仅在可信终端的127.0.0.1上；具体Service/port从connections读取
kubectl -n "$TEST_NS" port-forward --address 127.0.0.1 "svc/${NOTEBOOK_SERVICE}" "18888:${NOTEBOOK_SERVICE_PORT}"

```

端口转发在当前终端前台运行；请求测试在第二个可信终端执行，结束后Ctrl-C停止。`*_SERVICE_PORT`从本批实际Service和连接输出取得，不能猜端口。它不是已交付的ANI业务访问入口。

## 本批必须交付的操作材料
1. 具体role及配置测试；`scripts/acceptance/<component>/`中的具名检查器，并在code release内放到`checks/<component>/`。
2. 本批render目录的`acceptance/<component>/README.md`：填写真实resource名称/namespace/port、创建顺序、请求样例、预期输出和只读诊断命令；所有REPLACE在进入V前消除。
3. 工具镜像/SDK/wheel/测试模型/guest镜像等大物料的固定hash；不能只有controller镜像。
4. 连接信息：内部地址、端口、数据库/桶/类、TLS CA/Secret引用、作用域和未交付的业务能力。禁止输出密码/token。
5. task-result、代码/材料/实机/持久化/ANI接入五类状态和独立证据路径。

## 停止条件
镜像依赖未闭合/路由鉴权未接不改为匿名公网开放；不借kcn专用Envoy实现访问。

## 资料
WEB-KF, WEB-NOTEBOOK，详见06版本登记与reference/official-sources.md。继承来源不表示本次逐项重新验证。


---

# B09｜Trainer与JobSet：CPU训练先闭环

**状态：**计划待实施　**模式：**components　**前置：**B00

## 锁定组合
Trainer2.1.0 + JobSet0.10.1（Kubeflow26.03）

这是继承的设计组合，不是本次实装认证。M阶段必须取真实官方材料并锁摘要；不擅自换latest。
命名空间/所有权：kubeflow，JobSet只有一个CRD/controller owner。

## 拟新增配置契约
```text
components.trainer.enabled: bool=false
components.trainer.runtimeProfile: cpu（固定首轮）
components.trainer.checkpointStorageClass: 可选但checkpoint测试时必填
components.trainer.schedulerIntegration: default（本轮已承诺CPU基线）；volcano对接另列明确子场景
components.trainer.jobset.mode: managed|existing（existing必须检查版本/owner）
```
上述字段是本批实现目标，旧installer不支持；不能把小段配置当成已经可执行的完整site.yaml。

## M｜材料和依赖清单
Trainer/JobSet控制器、CRDs和webhook依赖、CPU训练runtime镜像、init与checkpoint样例。Trainer源码固定JobSet0.10.1；不替换成LWS。
锁一个CPU runtime和其预装PyTorch/数据脚本；训练小样例数据随fixture本地供应，不从HuggingFace公网拉取。

交付 `materials/<id>/material-report.md`、真实摘要锁、完整静态/动态/验证镜像清单、固定来源与支持条件。没有摘要、来源不可核验或硬件不满足时blocked；不编造。

## I｜实现与执行步骤
1. 从v2.1.0固定资源vendoring JobSet0.10.1，先CRD/JobSet，再Trainer及CPU runtime；检查共享owner。
2. 使用该版本官方TrainJob/TrainingRuntime示例结构，替换CPU镜像与本地训练脚本；CR字段从固定schema核对，不猜旧PyTorchJob字段。
3. 跑一个小型确定性CPU训练，记录JobSet/Jobs/Pods关联、完成状态、训练输出或checkpoint摘要。
4. 跑一个明确失败脚本，确认失败状态正确传播；取消测试只作用本run任务，检查无孤儿Pod。
5. 如需checkpoint，挂实际PVC/对象存储，确认输出读回；不要把容器stdout当模型产物持久化。
6. Volcano对接作为B09-VG单独任务，前置B06；需要该版本官方集成支持/实际PodGroup资源与调度测试，不仅在Pod上设schedulerName就声称gang通过。无证据先保留CPU默认调度成功，VG=blocked。
7. 渲染不引入GPU/LWS/Kueue/legacy Training Operator；不能把用户暂缓决策改成“依赖必装”。

除本卡明确的网络/运行时扩展外，复用R15的组件新增入口。role→配置→材料→验证→连接输出必须同时注册。原生清单优先vendor固定来源并做小overlay；Chart依赖在Fedora材料侧闭合。禁止目标机helm repo update/dependency update或curl公网补包。

## T/V｜局部与真实验收标准
TrainJob→JobSet→Job/Pod真正运行；退出码/失败/取消可观测；训练产物可验证。
关闭Trainer无JobSet隐式部署；既有JobSet冲突拒绝。
GPU请求与LWS资源不存在；默认CPU成功不等于Volcano gang接入成功。

V前先通过T；正常Pod重建只能在acceptance中对声明目标执行一次；失败仅只读取证，不修底层组件。所有测试对象带runID标签，测试数据用独立库/桶前缀，不动业务数据。

## 人工检查命令
变量/文件均须来自实际render/connection输出，环境见04手册。`CHECKS_DIR` 下脚本是本批I阶段必须交付的产物，**本执行包没有假装已经提供这些组件实现**。

```bash
kubectl --request-timeout=10s get crd jobsets.jobset.x-k8s.io
kubectl --request-timeout=10s -n "$TEST_NS" get trainjobs.trainer.kubeflow.org
kubectl --request-timeout=10s -n "$TEST_NS" get jobsets.jobset.x-k8s.io,jobs,pods -o wide
# 本批I阶段提供固定版本cpu-trainjob.yaml和CPU训练fixture
bash "$CHECKS_DIR/trainer/smoke.sh" --run "$RUN_JSON"

```

端口转发在当前终端前台运行；请求测试在第二个可信终端执行，结束后Ctrl-C停止。`*_SERVICE_PORT`从本批实际Service和连接输出取得，不能猜端口。它不是已交付的ANI业务访问入口。

## 本批必须交付的操作材料
1. 具体role及配置测试；`scripts/acceptance/<component>/`中的具名检查器，并在code release内放到`checks/<component>/`。
2. 本批render目录的`acceptance/<component>/README.md`：填写真实resource名称/namespace/port、创建顺序、请求样例、预期输出和只读诊断命令；所有REPLACE在进入V前消除。
3. 工具镜像/SDK/wheel/测试模型/guest镜像等大物料的固定hash；不能只有controller镜像。
4. 连接信息：内部地址、端口、数据库/桶/类、TLS CA/Secret引用、作用域和未交付的业务能力。禁止输出密码/token。
5. task-result、代码/材料/实机/持久化/ANI接入五类状态和独立证据路径。

## 停止条件
官方版本无证据支撑的调度集成不能补虚构CR或换Trainer版本；CPU与Volcano集成状态分开。

## 资料
WEB-KF, WEB-JOBSET，详见06版本登记与reference/official-sources.md。继承来源不表示本次逐项重新验证。


---

# B10｜Hub / Model Registry 独立API

**状态：**计划待实施　**模式：**components　**前置：**B00

## 锁定组合
0.3.7（26.03仍称Model Registry）

这是继承的设计组合，不是本次实装认证。M阶段必须取真实官方材料并锁摘要；不擅自换latest。
命名空间/所有权：kubeflow；独立PG数据库/账号。

## 拟新增配置契约
```text
components.hub.enabled: bool=false
components.hub.database.host / port / name / credentialsSecret / tlsCASecret: 必填或明确不使用TLS的内网实验声明
components.hub.exposure: internal（首轮固定）
禁用自动迁移ANI现有模型仓库数据。
```
上述字段是本批实现目标，旧installer不支持；不能把小段配置当成已经可执行的完整site.yaml。

## M｜材料和依赖清单
固定0.3.7服务/迁移与所需sidecar镜像，OpenAPI和最小客户端fixture。复用PG17.11的专用数据库属于ANI组合，不是官方原配保证。
保留该release实际model-registry镜像名和包路径，不能把项目改名Hub后拼出不存在的新镜像。

交付 `materials/<id>/material-report.md`、真实摘要锁、完整静态/动态/验证镜像清单、固定来源与支持条件。没有摘要、来源不可核验或硬件不满足时blocked；不编造。

## I｜实现与执行步骤
1. 阅读0.3.7固定README/数据库配置/OpenAPI，确认所选PostgreSQL后端初始化方式与实际API路径，生成curl测试脚本。
2. 由受控数据库初始化创建专用库和最小账号，Secret放服务namespace，管理员密码不进入Hub应用容器。
3. 单独部署API，不安装需要Istio的完整UI/Operator组合；初始化或migration失败停止，不反复删除数据库。
4. API创建registered model、model version、artifact引用，再读取相同ID；artifact引用指向专用测试对象，不导入真实ANI仓库。
5. 重建API Pod后元数据可读，数据库不被重建；服务HTTP200但数据未写入不能通过。
6. Alpha成熟度写在交付说明；业务模型仓库替换/认证入口不属于本批，状态分开。
7. PG17.11不兼容时block并提交具体SQL/协议证据，不默默换数据库产品。

除本卡明确的网络/运行时扩展外，复用R15的组件新增入口。role→配置→材料→验证→连接输出必须同时注册。原生清单优先vendor固定来源并做小overlay；Chart依赖在Fedora材料侧闭合。禁止目标机helm repo update/dependency update或curl公网补包。

## T/V｜局部与真实验收标准
CRUD和关联关系存在；重复写/错误ID有正确返回；新旧API Pod UID变化后对象仍可查询。
凭据错误/数据库缺失准确失败；不使用ani_app超级权限绕过。
API-only不开放无鉴权公网NodePort/Ingress；无Envoy复用。

V前先通过T；正常Pod重建只能在acceptance中对声明目标执行一次；失败仅只读取证，不修底层组件。所有测试对象带runID标签，测试数据用独立库/桶前缀，不动业务数据。

## 人工检查命令
变量/文件均须来自实际render/connection输出，环境见04手册。`CHECKS_DIR` 下脚本是本批I阶段必须交付的产物，**本执行包没有假装已经提供这些组件实现**。

```bash
kubectl --request-timeout=10s -n kubeflow get deploy,pod,svc
# OpenAPI操作路径由0.3.7固定源在M阶段确定；此脚本须随I阶段交付
bash "$CHECKS_DIR/hub/metadata-roundtrip.sh" --run "$RUN_JSON"
# 结果需包含modelId/versionId/artifactId及重启前后查询结果，不含密码

```

端口转发在当前终端前台运行；请求测试在第二个可信终端执行，结束后Ctrl-C停止。`*_SERVICE_PORT`从本批实际Service和连接输出取得，不能猜端口。它不是已交付的ANI业务访问入口。

## 本批必须交付的操作材料
1. 具体role及配置测试；`scripts/acceptance/<component>/`中的具名检查器，并在code release内放到`checks/<component>/`。
2. 本批render目录的`acceptance/<component>/README.md`：填写真实resource名称/namespace/port、创建顺序、请求样例、预期输出和只读诊断命令；所有REPLACE在进入V前消除。
3. 工具镜像/SDK/wheel/测试模型/guest镜像等大物料的固定hash；不能只有controller镜像。
4. 连接信息：内部地址、端口、数据库/桶/类、TLS CA/Secret引用、作用域和未交付的业务能力。禁止输出密码/token。
5. task-result、代码/材料/实机/持久化/ANI接入五类状态和独立证据路径。

## 停止条件
上游Alpha限制或PG组合错误单列，不扩成替换ANI现有模型仓库工程。

## 资料
WEB-KF, SRC-MATRIX:S07-S08，详见06版本登记与reference/official-sources.md。继承来源不表示本次逐项重新验证。


---

# B11｜KServe Standard，无Istio、无业务Envoy

**状态：**计划待实施　**模式：**components　**前置：**B00

## 锁定组合
0.16.0（Kubeflow26.03）

这是继承的设计组合，不是本次实装认证。M阶段必须取真实官方材料并锁摘要；不擅自换latest。
命名空间/所有权：kserve（本方案固定），测试InferenceService独立namespace。

## 拟新增配置契约
```text
components.kserve.enabled: bool=false
components.kserve.deploymentMode: Standard（首轮固定）
components.kserve.disableIngressCreation: true；gatewayAPI.enabled=false
components.kserve.servingRuntime: sklearn（首轮最小CPU样例）
objectStore.endpoint / credentialsSecret / caSecret / bucket: 测试模型所在S3配置
不启用Knative、LWS、LLMInferenceService、多节点、GPU runtime。
```
上述字段是本批实现目标，旧installer不支持；不能把小段配置当成已经可执行的完整site.yaml。

## M｜材料和依赖清单
controller/agent/router/storage-initializer0.16.0及必要rbac-proxy；只选CPU sklearn runtime镜像，不把所有默认ServingRuntime都带入。
固定可重复创建的小模型文件、输入JSON、期望预测结果、客户端/serializer版本；模型文件随artifact或预置RGW，不运行时联网下载。
官方values默认deploymentMode=Knative，必须显式覆盖；关Ingress不能只靠注释。

交付 `materials/<id>/material-report.md`、真实摘要锁、完整静态/动态/验证镜像清单、固定来源与支持条件。没有摘要、来源不可核验或硬件不满足时blocked；不编造。

## I｜实现与执行步骤
1. 用0.16.0固定资源，渲染deploymentMode=Standard、gateway.disableIngressCreation=true、enableGatewayApi=false/createGateway=false；不要全量安装官方示例后删Istio。
2. 关闭未选ServingRuntime、多节点HuggingFace等；检查controller是否有实际watch依赖。若仍硬需暂缓CRD，先做最小构建/启动证据，不能自作主张装整套网关。
3. 复用cert-manager而不降级；CRD/webhook/证书就绪后创建单个CPU InferenceService。
4. 配置RGW路径、最小Secret与CA，使storage-initializer能拉模型。TLS验证失败不关verifySSL。
5. 检查生成的Deployment/Service实际与Standard模式一致；本轮仅内部Service通信，外部发布另记未实施。
6. 发送固定predict请求，断言数值/类别，不用/healthz替代推理正确性；测试错误bucket/key的失败归因。
7. 计划内重建一个推理Pod后重新拉取/使用同模型可预测；kcn Envoy配置/CRD/Deployment均无变化。

除本卡明确的网络/运行时扩展外，复用R15的组件新增入口。role→配置→材料→验证→连接输出必须同时注册。原生清单优先vendor固定来源并做小overlay；Chart依赖在Fedora材料侧闭合。禁止目标机helm repo update/dependency update或curl公网补包。

## T/V｜局部与真实验收标准
render与实机均不出现Istio/Knative/LWS/GPU/业务Gateway；检查不能只看strings.Contains配置。
真实推理返回固定预期；缺模型/错Secret明确失败；模型和Pod重建后结果一致。
内部InferenceService Ready不能宣称外部ANI网关发布通过。

V前先通过T；正常Pod重建只能在acceptance中对声明目标执行一次；失败仅只读取证，不修底层组件。所有测试对象带runID标签，测试数据用独立库/桶前缀，不动业务数据。

## 人工检查命令
变量/文件均须来自实际render/connection输出，环境见04手册。`CHECKS_DIR` 下脚本是本批I阶段必须交付的产物，**本执行包没有假装已经提供这些组件实现**。

```bash
kubectl --request-timeout=10s -n "$TEST_NS" get inferenceservices.serving.kserve.io
kubectl --request-timeout=10s -n "$TEST_NS" get deploy,pod,svc
# 按实际输出Service/端口，从可信终端port-forward；只绑定127.0.0.1
kubectl -n "$TEST_NS" port-forward --address 127.0.0.1 "svc/${PREDICTOR_SERVICE}" "18080:${PREDICTOR_SERVICE_PORT}"
# 另一个终端；模型名和请求文件由本批固定fixture提供
curl --fail --connect-timeout 5 --max-time 30 -H 'Content-Type: application/json'   -d @"$FIXTURE_DIR/kserve/input.json" "http://127.0.0.1:18080/v1/models/${MODEL_NAME}:predict"

```

端口转发在当前终端前台运行；请求测试在第二个可信终端执行，结束后Ctrl-C停止。`*_SERVICE_PORT`从本批实际Service和连接输出取得，不能猜端口。它不是已交付的ANI业务访问入口。

## 本批必须交付的操作材料
1. 具体role及配置测试；`scripts/acceptance/<component>/`中的具名检查器，并在code release内放到`checks/<component>/`。
2. 本批render目录的`acceptance/<component>/README.md`：填写真实resource名称/namespace/port、创建顺序、请求样例、预期输出和只读诊断命令；所有REPLACE在进入V前消除。
3. 工具镜像/SDK/wheel/测试模型/guest镜像等大物料的固定hash；不能只有controller镜像。
4. 连接信息：内部地址、端口、数据库/桶/类、TLS CA/Secret引用、作用域和未交付的业务能力。禁止输出密码/token。
5. task-result、代码/材料/实机/持久化/ANI接入五类状态和独立证据路径。

## 停止条件
无Istio路径不成立就报告具体版本约束，不复用kcn Envoy、不解除暂缓、不换版本掩盖。

## 资料
WEB-KF, WEB-KSERVE，详见06版本登记与reference/official-sources.md。继承来源不表示本次逐项重新验证。


---

# B12｜Pipelines：拆开依赖、执行器与工件验证

**状态：**计划待实施　**模式：**components　**前置：**B00

## 锁定组合
KFP2.16.0；Argo3.7.3；MLMD1.14.0；MySQL8.4.11；RGW复用20.2.4

这是继承的设计组合，不是本次实装认证。M阶段必须取真实官方材料并锁摘要；不擅自换latest。
命名空间/所有权：kubeflow；Argo按KFP选择的受限作用域。

## 拟新增配置契约
```text
components.pipelines.enabled: bool=false
components.pipelines.mysql.storageClass / storageSize / credentialsSecret: 显式
components.pipelines.objectStore.endpoint / bucket / region / credentialsSecret / caSecret: 显式
components.pipelines.exposure: internal（首轮固定）；multiUser: false（内部管理实验，不声称客户安全多租户）
KFP与MLMD独立数据库/用户；Argo与MLMD为内部技术依赖，不随意直接升级。
```
上述字段是本批实现目标，旧installer不支持；不能把小段配置当成已经可执行的完整site.yaml。

## M｜材料和依赖清单
固定KFP所有apiserver/persistence-agent/scheduledworkflow/cache/driver/launcher/frontend等实际选用镜像，Argo controller/argoexec、MLMD、MySQL、测试步骤镜像。
官方standalone示例带SeaweedFS；本方案替换为RGW，不额外安装SeaweedFS/MinIO。MySQL8.4.11是将示例浮动8.4固定后的ANI选择。
SDK/编译器wheels、pipeline YAML/IR、步骤运行依赖全部预置；不能在Pod里pip install kfp。MLMD所需认证插件配置从固定源码确认，不降低共享数据库全局安全策略。

交付 `materials/<id>/material-report.md`、真实摘要锁、完整静态/动态/验证镜像清单、固定来源与支持条件。没有摘要、来源不可核验或硬件不满足时blocked；不编造。

## I｜实现与执行步骤
B12.1仅依赖：先专用MySQL+数据库/用户，再MLMD+Argo；记录每个依赖角色和作用域。验证MySQL/MLMD连接，错误凭据必须fail。
B12.2 KFP控制面：vendoring platform-agnostic固定路径及递归资源，去除SeaweedFS并配置RGW；namespace变更必须覆盖cache/RBAC等所有引用。不能只改顶层namespace字段。
B12.3 真正运行：固定SDK编译一个两步骤pipeline，step1输出唯一文件工件，step2读取并校验内容；KFP运行终态成功、Argo对象正常、MLMD/数据库中可追踪记录、RGW对象可读。
B12.4 缓存/失败/取消：同输入重复运行观察明确缓存行为；失败步骤以非零退出验证状态传播；取消后无孤儿执行Pod。不要用简单echo pipeline替代工件链。
B12.5 持久化：分别计划内重建API/必要有状态Pod，保留PVC，已完成run/工件仍能查询。
整个批次不部署Istio/Knative/业务Envoy，UI/API只供内部授权管理员，经本地port-forward测试。
生产/租户安全接入另立业务任务，落实身份、授权、namespace/SA/RBAC、S3工件隔离；不能把一个可伪造身份header当认证。

除本卡明确的网络/运行时扩展外，复用R15的组件新增入口。role→配置→材料→验证→连接输出必须同时注册。原生清单优先vendor固定来源并做小overlay；Chart依赖在Fedora材料侧闭合。禁止目标机helm repo update/dependency update或curl公网补包。

## T/V｜局部与真实验收标准
本批必须先T依赖再T控制面再T运行fixture，不把所有东西第一次一起apply到三台。
两步工件读取、失败、取消、缓存、持久元数据分别有结果；外部网络阻断且没有pip/curl在线补运行材料。
Argo CRDs只一个owner，controller监听范围可证；不与别的workflow控制器重复处理。
客户安全多租户没有完成时标not_implemented，不因API安装成功转绿。

V前先通过T；正常Pod重建只能在acceptance中对声明目标执行一次；失败仅只读取证，不修底层组件。所有测试对象带runID标签，测试数据用独立库/桶前缀，不动业务数据。

## 人工检查命令
变量/文件均须来自实际render/connection输出，环境见04手册。`CHECKS_DIR` 下脚本是本批I阶段必须交付的产物，**本执行包没有假装已经提供这些组件实现**。

```bash
kubectl --request-timeout=10s -n kubeflow get deploy,statefulset,pod,svc,pvc
kubectl --request-timeout=10s -n "$TEST_NS" get workflows.argoproj.io
# 以下脚本由本批实现并打包，内部使用已锁定SDK/客户端镜像
bash "$CHECKS_DIR/pipelines/two-step-artifact.sh" --run "$RUN_JSON"
bash "$CHECKS_DIR/pipelines/failure-cancel.sh" --run "$RUN_JSON"
# 输出KFP runID、Argo workflow UID、artifact URI及内容摘要

```

端口转发在当前终端前台运行；请求测试在第二个可信终端执行，结束后Ctrl-C停止。`*_SERVICE_PORT`从本批实际Service和连接输出取得，不能猜端口。它不是已交付的ANI业务访问入口。

## 本批必须交付的操作材料
1. 具体role及配置测试；`scripts/acceptance/<component>/`中的具名检查器，并在code release内放到`checks/<component>/`。
2. 本批render目录的`acceptance/<component>/README.md`：填写真实resource名称/namespace/port、创建顺序、请求样例、预期输出和只读诊断命令；所有REPLACE在进入V前消除。
3. 工具镜像/SDK/wheel/测试模型/guest镜像等大物料的固定hash；不能只有controller镜像。
4. 连接信息：内部地址、端口、数据库/桶/类、TLS CA/Secret引用、作用域和未交付的业务能力。禁止输出密码/token。
5. task-result、代码/材料/实机/持久化/ANI接入五类状态和独立证据路径。

## 停止条件
MySQL/MLMD认证或RGW不兼容先停对应子任务；不把全栈数据库强行换PG，也不额外安装存储/网关兜底。

## 资料
WEB-KF, WEB-KFP, SRC-MATRIX:S10-S14，详见06版本登记与reference/official-sources.md。继承来源不表示本次逐项重新验证。


---

# B13｜选定组件的整体离线收官

**状态：**计划待实施　**模式：**acceptance　**前置：**B02, B03, B04, B05, B06, B07, B08, B09, B10, B11, B12

## 锁定组合
继承各已通过批次版本；不升级任何版本

这是继承的设计组合，不是本次实装认证。M阶段必须取真实官方材料并锁摘要；不擅自换latest。
命名空间/所有权：复用所选组合，不新建平台控制面。

## 拟新增配置契约
```text
只开启用户本次明确选择的组件；B13的前置列表按最终启用集合取子集，不强制安装所有可选组件。
保留单独的networkStack/日志backend/组件集合及容量计划；不能把两个网络栈或两个日志后端都装进一个现场。
```
上述字段是本批实现目标，旧installer不支持；不能把小段配置当成已经可执行的完整site.yaml。

## M｜材料和依赖清单
累计artifact包含所有选中controller+动态运行镜像、工具、模型样例、数据库资料；未选/暂缓项只留研究记录。
验证总requests/存储预算和实际可用空间。三台实验机若装不下全选组合，分组验收并记录未做全选组合，不通过删requests/limits或降低Ceph副本伪造容量。

交付 `materials/<id>/material-report.md`、真实摘要锁、完整静态/动态/验证镜像清单、固定来源与支持条件。没有摘要、来源不可核验或硬件不满足时blocked；不编造。

## I｜实现与执行步骤
1. 冻结最终selected功能集合、容量测量、sourceTree/kk/artifact锁/site/实验快照身份。
2. 从原始干净快照执行完整首装；不是只从带缓存的阶段快照运行。每次恢复后重新核验离线隔离，保留管理和集群内通信。
3. 跑共用network/storage/证书/消息和各组件已定义smoke，然后一次专项acceptance；不复制各组件恢复重试。
4. 基础设施闭环：固定数据/模型可经过训练样例产出、对象存储保存、Hub引用、KServe内部预测。仅在各组件官方API及fixture已实现时组合；不临时开发ANI微服务接入。
5. 业务接入（ANI模型仓库、镜像准入、Notebook访问、KFP租户授权）逐项列pass/not_verified，不混到installer安装结果。
6. 执行只读冲突检查：无重复CRD owner、无临时公网源、无kcn Envoy复用、无暂缓组件。
7. 交付实际输入模板、连接说明、镜像BOM/摘要、故障索引、覆盖矩阵和已知限制。

除本卡明确的网络/运行时扩展外，复用R15的组件新增入口。role→配置→材料→验证→连接输出必须同时注册。原生清单优先vendor固定来源并做小overlay；Chart依赖在Fedora材料侧闭合。禁止目标机helm repo update/dependency update或curl公网补包。

## T/V｜局部与真实验收标准
完整干净首装、所有已选smoke、专项重建和材料离线覆盖；未选项应是skipped，不是pass。
记录真实运行总时长和每阶段耗时，不预报时间；与旧大循环比较错误发现阶段而非虚构性能提升百分比。
控制面HA/组件HA/租户隔离未单独验证的不声明已认证。

V前先通过T；正常Pod重建只能在acceptance中对声明目标执行一次；失败仅只读取证，不修底层组件。所有测试对象带runID标签，测试数据用独立库/桶前缀，不动业务数据。

## 人工检查命令
变量/文件均须来自实际render/connection输出，环境见04手册。`CHECKS_DIR` 下脚本是本批I阶段必须交付的产物，**本执行包没有假装已经提供这些组件实现**。

```bash
# 目标安装节点，R13接口与所有所选批次已实现后
sudo "$CODE_ROOT/kk" ani verify --run "$RUN_JSON" --level smoke
# 仅当实验授权及本run的验收目标清单齐备
sudo "$CODE_ROOT/kk" ani verify --run "$RUN_JSON" --level acceptance --allow-pod-recreate

```

端口转发在当前终端前台运行；请求测试在第二个可信终端执行，结束后Ctrl-C停止。`*_SERVICE_PORT`从本批实际Service和连接输出取得，不能猜端口。它不是已交付的ANI业务访问入口。

## 本批必须交付的操作材料
1. 具体role及配置测试；`scripts/acceptance/<component>/`中的具名检查器，并在code release内放到`checks/<component>/`。
2. 本批render目录的`acceptance/<component>/README.md`：填写真实resource名称/namespace/port、创建顺序、请求样例、预期输出和只读诊断命令；所有REPLACE在进入V前消除。
3. 工具镜像/SDK/wheel/测试模型/guest镜像等大物料的固定hash；不能只有controller镜像。
4. 连接信息：内部地址、端口、数据库/桶/类、TLS CA/Secret引用、作用域和未交付的业务能力。禁止输出密码/token。
5. task-result、代码/材料/实机/持久化/ANI接入五类状态和独立证据路径。

## 停止条件
容量不足或某可选组合未验，诚实分组记录，不继续自动加机器/换存储/改调度架构。

## 资料
SRC-AUDIT, SRC-MATRIX，详见06版本登记与reference/official-sources.md。继承来源不表示本次逐项重新验证。


---

# O01｜可选：Dex / 外部OIDC接入

**状态：**可选、未默认授权　**模式：**components　**前置：**B00

## 锁定组合
Dex2.45.0；OAuth2 Proxy7.14.3仅独立UI确需时

这是继承的设计组合，不是本次实装认证。M阶段必须取真实官方材料并锁摘要；不擅自换latest。
命名空间/所有权：专用auth namespace；不借kcn Envoy。

## 拟新增配置契约
```text
components.dex.enabled=false；issuerURL/clientID/redirectURIs/storageSecret显式；已有OIDC可只配置接入，不部署Dex。
```
上述字段是本批实现目标，旧installer不支持；不能把小段配置当成已经可执行的完整site.yaml。

## M｜材料和依赖清单
固定Dex及所选connector/storage依赖。OIDC issuer DNS/证书及回调URI必须现场确认，不使用示例密钥。

交付 `materials/<id>/material-report.md`、真实摘要锁、完整静态/动态/验证镜像清单、固定来源与支持条件。没有摘要、来源不可核验或硬件不满足时blocked；不编造。

## I｜实现与执行步骤
1. 用户明确选择本项后才供料/安装。先验证ANI的OIDC配置契约与精确redirectURI。
2. 内部HTTPS及持久存储、client secret准备；Dex动态身份源仅选择一个可验证connector。
3. 验证discovery/JWKS/授权码换token、aud/iss/exp及回调，错误issuer/audience拒绝。
4. OAuth2 Proxy不是必装；即便装也不能代替ANI/KFP授权和数据隔离。
5. 外部入口需要独立网关时保持blocked，不复用kcn Envoy或自行解除暂缓。

除本卡明确的网络/运行时扩展外，复用R15的组件新增入口。role→配置→材料→验证→连接输出必须同时注册。原生清单优先vendor固定来源并做小overlay；Chart依赖在Fedora材料侧闭合。禁止目标机helm repo update/dependency update或curl公网补包。

## T/V｜局部与真实验收标准
有效登录成功；错误aud/iss拒绝；重建Dex后配置/会话按选定存储契约；日志不输出token/secret。

V前先通过T；正常Pod重建只能在acceptance中对声明目标执行一次；失败仅只读取证，不修底层组件。所有测试对象带runID标签，测试数据用独立库/桶前缀，不动业务数据。

## 人工检查命令
变量/文件均须来自实际render/connection输出，环境见04手册。`CHECKS_DIR` 下脚本是本批I阶段必须交付的产物，**本执行包没有假装已经提供这些组件实现**。

```bash
curl --fail --cacert "$CA_FILE" "${ISSUER_URL}/.well-known/openid-configuration"
```

端口转发在当前终端前台运行；请求测试在第二个可信终端执行，结束后Ctrl-C停止。`*_SERVICE_PORT`从本批实际Service和连接输出取得，不能猜端口。它不是已交付的ANI业务访问入口。

## 本批必须交付的操作材料
1. 具体role及配置测试；`scripts/acceptance/<component>/`中的具名检查器，并在code release内放到`checks/<component>/`。
2. 本批render目录的`acceptance/<component>/README.md`：填写真实resource名称/namespace/port、创建顺序、请求样例、预期输出和只读诊断命令；所有REPLACE在进入V前消除。
3. 工具镜像/SDK/wheel/测试模型/guest镜像等大物料的固定hash；不能只有controller镜像。
4. 连接信息：内部地址、端口、数据库/桶/类、TLS CA/Secret引用、作用域和未交付的业务能力。禁止输出密码/token。
5. task-result、代码/材料/实机/持久化/ANI接入五类状态和独立证据路径。

## 停止条件
用户未选择、本地issuer不可达或无合适入口时不默认部署，记录依赖待定。

## 资料
SRC-MATRIX:S01，详见06版本登记与reference/official-sources.md。继承来源不表示本次逐项重新验证。


---

# O02｜可选：Kata沙箱及kata-monitor

**状态：**可选、未默认授权　**模式：**runtime-extension　**前置：**B00

## 锁定组合
Kata/Chart4.2.0；monitor随发行源码/物料核对

这是继承的设计组合，不是本次实装认证。M阶段必须取真实官方材料并锁摘要；不擅自换latest。
命名空间/所有权：节点运行时＋RuntimeClass=sandbox-kata。

## 拟新增配置契约
```text
components.kata.enabled=false；nodeSelector/runtimeClass/handler显式；NFD如需只复用或明确安装单一owner，不因GPU暂缓一并装GPU。
```
上述字段是本批实现目标，旧installer不支持；不能把小段配置当成已经可执行的完整site.yaml。

## M｜材料和依赖清单
Kata runtime、hypervisor、guest kernel/rootfs/固件及kata-deploy镜像；monitor不要虚构独立tag。

交付 `materials/<id>/material-report.md`、真实摘要锁、完整静态/动态/验证镜像清单、固定来源与支持条件。没有摘要、来源不可核验或硬件不满足时blocked；不编造。

## I｜实现与执行步骤
1. 用户选择后，先核对/dev/kvm、containerd2.3.4 runtime handler与Kata4.2.0组合；不凭KubeVirt已安装推断Kata可用。
2. 节点配置变更列出差异和受影响节点，单独授权窗口；保持默认runc不变，RuntimeClass仅被选工作负载使用。
3. 创建RuntimeClass=sandbox-kata与CPU测试Pod，证明实际VM沙箱而非回落runc。
4. kata-monitor如ANI需要则验证kata_guest_meminfo指标名/标签；不把node-exporter替代它。
5. 只验证卷快照不等于进程内存checkpoint；后者单独需求，不在本卡虚称完成。

除本卡明确的网络/运行时扩展外，复用R15的组件新增入口。role→配置→材料→验证→连接输出必须同时注册。原生清单优先vendor固定来源并做小overlay；Chart依赖在Fedora材料侧闭合。禁止目标机helm repo update/dependency update或curl公网补包。

## T/V｜局部与真实验收标准
Kata handler确实运行；普通Pod仍runc；runtime不可用前置拒绝；monitor指标满足ANI查询契约。

V前先通过T；正常Pod重建只能在acceptance中对声明目标执行一次；失败仅只读取证，不修底层组件。所有测试对象带runID标签，测试数据用独立库/桶前缀，不动业务数据。

## 人工检查命令
变量/文件均须来自实际render/connection输出，环境见04手册。`CHECKS_DIR` 下脚本是本批I阶段必须交付的产物，**本执行包没有假装已经提供这些组件实现**。

```bash
kubectl --request-timeout=10s get runtimeclass sandbox-kata
kubectl --request-timeout=10s -n "$TEST_NS" get pod "$KATA_POD" -o jsonpath='{.spec.runtimeClassName}{"\n"}'
```

端口转发在当前终端前台运行；请求测试在第二个可信终端执行，结束后Ctrl-C停止。`*_SERVICE_PORT`从本批实际Service和连接输出取得，不能猜端口。它不是已交付的ANI业务访问入口。

## 本批必须交付的操作材料
1. 具体role及配置测试；`scripts/acceptance/<component>/`中的具名检查器，并在code release内放到`checks/<component>/`。
2. 本批render目录的`acceptance/<component>/README.md`：填写真实resource名称/namespace/port、创建顺序、请求样例、预期输出和只读诊断命令；所有REPLACE在进入V前消除。
3. 工具镜像/SDK/wheel/测试模型/guest镜像等大物料的固定hash；不能只有controller镜像。
4. 连接信息：内部地址、端口、数据库/桶/类、TLS CA/Secret引用、作用域和未交付的业务能力。禁止输出密码/token。
5. task-result、代码/材料/实机/持久化/ANI接入五类状态和独立证据路径。

## 停止条件
运行时/硬件组合未验则block；不能重写默认containerd运行时、触发全部节点重启。

## 资料
SRC-MATRIX:S32-S33，详见06版本登记与reference/official-sources.md。继承来源不表示本次逐项重新验证。


---

# O03｜可选：Jaeger持久化追踪

**状态：**可选、未默认授权　**模式：**components　**前置：**B00

## 锁定组合
Jaeger2.20.0 / Chart4.13.1

这是继承的设计组合，不是本次实装认证。M阶段必须取真实官方材料并锁摘要；不擅自换latest。
命名空间/所有权：专用observability命名空间或明确复用现有。

## 拟新增配置契约
```text
components.jaeger.enabled=false；storageBackend/endpoint/credentialsSecret/retention显式，后端先定型再实施。
```
上述字段是本批实现目标，旧installer不支持；不能把小段配置当成已经可执行的完整site.yaml。

## M｜材料和依赖清单
固定Jaeger、Chart子依赖、选定持久化后端客户端/验证工具。旧矩阵尚未确定后端，不能默认memory冒充完成。

交付 `materials/<id>/material-report.md`、真实摘要锁、完整静态/动态/验证镜像清单、固定来源与支持条件。没有摘要、来源不可核验或硬件不满足时blocked；不编造。

## I｜实现与执行步骤
1. 用户选择且后端方案有明确版本支持证据后，先关闭材料gate。
2. 发送带唯一traceID的跨服务示例span，验证查询返回父子关系和服务名。
3. 重建collector/query受控Pod后trace可查，后端存储不被清空。
4. ANI OTLP端点、TLS和采样配置单独输出，不将全量高采样率作为默认。
5. 没定后端时只做研究/材料验证，禁止下发安装任务。

除本卡明确的网络/运行时扩展外，复用R15的组件新增入口。role→配置→材料→验证→连接输出必须同时注册。原生清单优先vendor固定来源并做小overlay；Chart依赖在Fedora材料侧闭合。禁止目标机helm repo update/dependency update或curl公网补包。

## T/V｜局部与真实验收标准
发送trace后可精确检索；错误TLS/权限失败；持久化可证；memory模式只能demo-not-accepted。

V前先通过T；正常Pod重建只能在acceptance中对声明目标执行一次；失败仅只读取证，不修底层组件。所有测试对象带runID标签，测试数据用独立库/桶前缀，不动业务数据。

## 人工检查命令
变量/文件均须来自实际render/connection输出，环境见04手册。`CHECKS_DIR` 下脚本是本批I阶段必须交付的产物，**本执行包没有假装已经提供这些组件实现**。

```bash
bash "$CHECKS_DIR/jaeger/trace-roundtrip.sh" --run "$RUN_JSON"
```

端口转发在当前终端前台运行；请求测试在第二个可信终端执行，结束后Ctrl-C停止。`*_SERVICE_PORT`从本批实际Service和连接输出取得，不能猜端口。它不是已交付的ANI业务访问入口。

## 本批必须交付的操作材料
1. 具体role及配置测试；`scripts/acceptance/<component>/`中的具名检查器，并在code release内放到`checks/<component>/`。
2. 本批render目录的`acceptance/<component>/README.md`：填写真实resource名称/namespace/port、创建顺序、请求样例、预期输出和只读诊断命令；所有REPLACE在进入V前消除。
3. 工具镜像/SDK/wheel/测试模型/guest镜像等大物料的固定hash；不能只有controller镜像。
4. 连接信息：内部地址、端口、数据库/桶/类、TLS CA/Secret引用、作用域和未交付的业务能力。禁止输出密码/token。
5. task-result、代码/材料/实机/持久化/ANI接入五类状态和独立证据路径。

## 停止条件
未定持久后端或资料不支持所选组合，阻塞该可选卡，不重新扩整个可观测性栈。

## 资料
SRC-MATRIX:S54，详见06版本登记与reference/official-sources.md。继承来源不表示本次逐项重新验证。

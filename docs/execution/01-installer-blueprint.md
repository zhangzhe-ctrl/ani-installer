# 01｜ANI installer 蓝图与实现契约

版本：2026-09-24 / r1。性质：**实施目标，不是已经实现的功能说明。**

配套入口：[README](README.md)；逐项整改：[02](02-remediation-plan.md)；组件批次：[03](03-component-batches.md)；人工操作：[04](04-manual-runbook.md)。

## 1. 依据、优先级与当前事实

本方案以用户上传的 `ani-installer.zip`、离线对话、代码审查报告及版本矩阵为依据，不换框架、不重新进行一轮组件选型。源码快照 HEAD 为 `c7a97bb508b699d5117db625da2fa4a8155b838f`，ZIP SHA256 为 `2a4c9cc54becab39bb1c5c9f204ecb567ac9b0e90336dfa13a625994308269b3`。**HEAD 不能代替工作树内容摘要**，因为上传快照/后续本地目录可能包含未提交修改。

依据优先级：用户最新明确指令 → 当前源代码与实际物料证据 → 本执行包的明确设计决定 → 较早报告/历史交接。冲突必须在状态记录中说明，不能静默选一份。

当前已存在：KubeKey fork，Go ANI 配置入口，`kk ani install --config --package-root`，`scripts/build-code.sh`，`scripts/build-offline.sh`，安装/验证 shell，CNI 分支、Ceph 及八项基础组件 role。**`validate`、`render`、`components install`、结构化 `verify` 均是本计划拟新增入口。** [SRC-CODE、SRC-AUDIT]

需要校准的文档/范围差异要在 R00 处理：

- 旧 `kubekey/AGENTS.md` 仍写“新版 kcn 材料未提供”，后续 `ani/components.lock.yaml` 已记录 2026-09-21 fix2 供料与摘要。按真实可读取归档及摘要决定材料是否就绪，不凭旧文字阻断，也不凭后续注释直接宣布实装通过。
- 旧版本矩阵曾写“以后解耦 kcn 的 Envoy”。此句被用户后续指令撤销：**kcn 专属 Envoy 保持专属；业务 Envoy 必须另一条独立安装流程，当前暂缓。**
- 现代码 `pkg/ani/config.go` 的 `control_plane_endpoint.type=local`，镜像表把 HAProxy/kube-vip 标为 unused。矩阵中的 HAProxy 更新是材料维护候选，不据此改控制面架构或假定现场已经运行它。

## 2. 产品目标与不做的事情

目标：运维提供一份站点配置和两个不可变发布物，能预先看清变化，在离线集群中完成首装或新增选定组件；失败可定位、可保留现场、不会暗中修复其他组件。

| 负责 | 不负责 |
|---|---|
| 配置解析、支持范围校验、材料完整性 | 修 kcn/Kube-OVN/Ceph 等组件自身算法或镜像 |
| 系统准备及集群首装的既有 KubeKey 流程 | 新建 Ansible/Kubespray 替代实现 |
| 固定顺序安装 role、准备账号/Secret/连接信息 | 部署 ANI 业务微服务、创建真实租户、迁移业务数据 |
| 明确的就绪检查、最小功能探测、专项验收 | 失败后删 PVC、清盘、清 OVN/LSP、重启 CNI/OVS“修好再测” |
| 只读故障取证和真实状态记录 | 自动回滚、任意断点续装、跨版本升级/卸载 |
| 实验工具与产品代码隔离 | 把 ESXi 快照还原、断网规则写进 installer |

首版只承诺 Ubuntu 24.04、linux/amd64、三节点、IPv4；`installerNode` 必须等于 `nodes[0]`。这里是在代码层落实既有范围，不把配置做成接受任意拓扑的假通用接口。

## 3. 两条安装流程和三条网络边界

### 3.1 集群首装

```text
只读配置/材料预检 → 取得安装互斥锁 → 记录变更开始
  → 本地离线物料服务 → KubeKey 主机准备/containerd/kubeadm
  → 所选主 CNI
      kcn 分支：kcn → 既定专用 Envoy → 专用检查
      kubeovn 分支：Kube-OVN → 通用网络检查（不接触 kcn Envoy）
  → 按显式选择安装存储和基础组件
  → 每组件必要就绪和最小读写探测
  → 导出连接信息、结果与证据
```

主 CNI 在首装时二选一，**不支持运行中切换主 CNI**。`profile: base` 只完成核心集群；`full` 表示允许继续执行显式开启的组件，不等于自动安装 Ceph 和所有服务。

### 3.2 新增组件

```text
核验现有集群身份/健康/组件所有权/已存在材料
  → 计算 --only 的技术依赖闭包
  → 拒绝未实现、暂缓、冲突或缺材料项
  → 执行独立 components playbook 中选定的 role
  → 必要就绪/最小功能探测 → 新 run 与连接信息
```

组件新增允许新有效配置与旧run的全量hash不同，但必须核验底座不变量（集群UID、节点、主CNI、底座版本及registry身份）未变；不能用“所有配置必须同hash”拒绝正常新增开关。

不调用 create_cluster，不执行 kubeadm、主 CNI 安装、已有 CephCluster 重建、磁盘初始化。首版只支持向健康 ANI 集群安装**尚未部署**的组件；不兼作 upgrade/install 自动混合器。已存在且元数据一致可返回 `already_installed`、只读验证；版本/所有者不同则拒绝，不加 `--force`。

### 3.3 Multus、LB、业务网关

Multus 是附加网络能力，独立 role 和开关；不取代主 CNI。Kube-OVN LB 是另外的显式能力，首轮只验默认 VPC；Multus 开启不代表 LB 自动开启。业务 Envoy 与 kcn 专属 Envoy不共享 release、实例、运行配置或安装任务；未来同集群部署时还须先检查集群级共享 CRD 兼容性。当前本方案不实现业务 Envoy。[WEB-NET]

## 4. 保留现有结构，只增加少量文件

以下“新增”是目标路径；不要求一次性建齐未实施组件目录。

```text
ani-installer/                         # Git 仓库根，不是 Go 模块根
  .github/workflows/ani-check.yaml     # 新增：实际可发现的 CI
  docs/execution/                     # 放本执行包三主文档、任务卡、状态
  lab/                                # 新增/归并：仅实验编排，不进 artifact
  kubekey/
    cmd/kk/app/builtin/ani.go          # 复用 Cobra 入口，按卡新增子命令
    pkg/ani/
      config.go                       # 现有配置与默认值，逐步减小职责
      runner.go                       # 现有首装流程
      images.go                       # 现有镜像表读取
      preflight.go                    # 新增：聚合无变更检查
      run_manifest.go                 # 新增：运行记录/结果 JSON
      materials.go                    # 新增：真实物料锁校验
      components_install.go           # 新增：独立新增组件流程
      verify.go                       # 新增：验证分发与失败边界
    builtin/core/playbooks/
      create_cluster.yaml             # 保留首装
      ani_components.yaml             # 新增：只列组件 role
    builtin/core/roles/ani/<name>/
      tasks/main.yaml                 # 安装+必要就绪+轻量探测
      templates/                      # 小型站点覆盖，不重写官方 CRD
      files/checker.*                 # 按需：易局部运行的检查器
    ani/
      versions.yaml
      components.lock.yaml            # 已有锁扩展，不拿研究 VersionMatrix 覆盖
      images*.tsv                     # 仍保留既有格式兼容入口
    scripts/
      check-code.sh                   # 新增：代码发布唯一门禁
      build-code.sh                   # 不打大物料包
      build-offline.sh                # 只在物料变化时执行
```

不引入通用插件 SDK、任意 DAG/版本求解器、控制面数据库、异步任务服务。组件少时保留固定顺序表；新增组件必须在配置、必需材料、role、验证、连接输出五处形成一致契约，通过测试检查一致性，避免“添加名字即算实现”。

## 5. 命令契约：现有与新增严格区分

| 入口 | 当前状态 | 语义 / 产物 | 实现任务 |
|---|---|---|---|
| `kk ani install --config SITE --package-root ARTIFACT` | 已存在 | 全新集群首装；保留 CLI 兼容 | 在整改中逐步修复 |
| `install.sh SITE ARTIFACT` | 已存在 | root/sudo 包装，不包含恢复 | 保留 |
| `kk ani validate --config SITE --package-root ARTIFACT --output DIR` | **拟新增** | 本地支持范围、配置、材料检查；写验证结果，不改主机/集群 | R06/R07/R09 |
| `kk ani render --config SITE --package-root ARTIFACT --output DIR` | **拟新增** | 不连接集群；输出实际模板/Chart 渲染结果及动作清单 | R08 |
| `kk ani components install --config SITE --package-root ARTIFACT --only CSV` | **拟新增** | 明确向既有健康集群新增所选组件；执行前包含只读集群检查 | R15 |
| `kk ani verify --run RUN_JSON --level smoke --only CSV` | **拟新增** | 指定 run 的轻量验证；不重建组件 Pod | R13 |
| `kk ani verify --run RUN_JSON --level acceptance --only CSV --allow-pod-recreate` | **拟新增** | 授权的一次计划内重建；不恢复、不删 PVC | R13 |

统一规则：unknown flag/未知组件/未知 YAML 字段立即非零退出；不得静默忽略。`--only` 使用规范组件 ID，逗号分隔，无输入时拒绝组件安装；验证不传 `--only` 则按该 run 实际安装项执行。`--allow-pod-recreate` 不是任意删除授权。

`validate` 只证明本地输入/材料合格，不伪装成磁盘、网卡、CRD 已实机通过。首装与组件安装在变更前额外执行目标主机/集群只读预检。`render` 对自己管理的模板产生计划文件；不能据此声称已把上游 KubeKey 所有 OS task 变成完全静态可预演事务。

拟定退出码：0 成功；2 输入/范围错误；3 材料不满足；4 环境/所有权冲突；5 安装失败；6 验证失败；7 远端结果未知；130 本地取消。原入口尚不能细分的错误可先保留非零，但必须在 JSON 中准确分类，不因映射错误改成成功。

## 6. 站点配置：只增加真正需要的字段

### 6.1 配置兼容规则

保留已有 `name/profile/installerNode/ssh/nodes/network/registry/components`。新增字段不得直接交给旧版本执行；R06 要用严格解析阻止拼写错误。禁止给现场提供任意组件版本号，版本由已批准的材料锁决定。

研究矩阵和部署开关分开：有版本记录≠开关已经实现；用户暂缓项若传 true，应报 `component_deferred`，不能自动拉进依赖。

### 6.2 存储目标字段（R05 新增）

```yaml
storage:
  enabled: true
  provider: rook-ceph
  makeDefaultStorageClass: false
  defaultStorageClassName: ani-block
  nodes:
    - name: node1
      devices:
        - /dev/disk/by-id/REPLACE_WITH_AUTHORIZED_DEVICE
    - name: node2
      devices:
        - /dev/disk/by-id/REPLACE_WITH_AUTHORIZED_DEVICE
    - name: node3
      devices:
        - /dev/disk/by-id/REPLACE_WITH_AUTHORIZED_DEVICE
```

`enabled` 缺省 false；关闭时不渲染 Ceph 资源、不检查新盘。只有 Rook-Ceph 是本期内置 provider；外部存储通过关闭内置存储和填写既有 `components.<name>.storageClass` 接入，不新增抽象 provider 体系。若用户选用 `ani-block`，却既未选择 Ceph也没有同名既有类，报错。

启用 Ceph 时每节点设备必须显式授权；允许现实环境只有 `/dev/sdb`，但必须记录解析后的设备身份，不能扫描“所有空盘”。mounted、系统盘及祖先/子分区、已有文件系统/LVM/RAID 签名或其他占用都拒绝；不执行 wipefs -a、sgdisk、zap。已有受管 Ceph 场景不走“新盘必须空白”的首装检查，component-only 不检查/初始化底座盘。

设置默认类时：没有其他默认类才可标记；其他默认存在时拒绝冲突，不遍历撤销。原先隐式 Ceph 的旧配置需要人工增加明确选择，这是有意的安全收紧，不用推测恢复旧默认。

### 6.3 Kube-OVN 目标字段（R10 / B01 新增）

```yaml
network:
  stack: kubeovn
  managementInterface: REPLACE_WITH_VERIFIED_INTERFACE
  podCIDR: 10.244.0.0/16
  serviceCIDR: 10.96.0.0/16
  kubeovn:
    defaultGateway: ""             # 留空：取 Pod 网络地址后第一个可用 IPv4
    joinCIDR: 172.19.0.0/16         # 示例值，不是所有现场的固定规则
    loadBalancer:
      enabled: false
      attachmentName: ani-lb-external
      attachmentNamespace: kube-system
      masterInterface: ""
      subnetName: ani-lb-external
      externalCIDR: ""
      gateway: ""
      excludeIPs: []
      nodeSelector: {}
  multus:
    enabled: false
    mode: thick
```

这段是目标 schema，不是可直接运行的现有配置。非默认 Pod CIDR 时必须生成一致网关；拒绝 CIDR 非法/重叠、网关越界、节点管理 IP 落入 Pod/Service/join 段、IPv6。没有宿主管理网掩码时不能宣称完成整个管理网段的重叠证明，补充现场只读路由证据。

Multus 的 CNI confDir/binDir、socket hostPath、默认 delegate 以实际 containerd/CNI 配置为准。只支持通过预检的路径组合；不把不明路径作为自动猜测。旧 CNI 配置不删除，不允许 Multus 把自己选成默认 delegate 造成递归。

### 6.4 新组件字段原则

每个 B 批次只加入自己的强类型字段和缺省 false 开关；字段表写在对应任务中。不能为接一个 Chart 增加任意 raw values 逃生口，让快速模型绕过依赖/安全约束。先支持单一已选拓扑，升级/HA 等另开明确任务。

### 6.5 规范组件ID与新增目录（目标契约）

| `--only` ID | 配置入口 | role目录（`builtin/core/roles/ani/`下） |
|---|---|---|
| metrics-server | components.metricsServer | metrics-server |
| snapshot-controller | components.snapshotController | snapshot-controller |
| milvus | components.milvus | milvus |
| kubevirt / cdi | components.kubevirt / components.cdi | kubevirt / cdi |
| volcano / harbor | components.volcano / components.harbor | volcano / harbor |
| notebooks / trainer | components.notebooks / components.trainer | notebooks / trainer |
| hub / kserve / pipelines | components.hub / components.kserve / components.pipelines | hub / kserve / pipelines |

`metrics`沿用原Prometheus组合，不与`metrics-server`混名。JobSet、Milvus专用etcd、KFP专用依赖在resolved清单中显示，但首版不提供绕过父组件约束的独立安装入口。Multus/LB属于显式网络扩展，不当作R15普通组件任意插入活跃集群。条件O卡也只有启用并实现后才注册ID。

## 7. 依赖、共享资源和顺序

依赖分两类：组件内部固定依赖（如 Trainer→JobSet、KFP→Argo/MLMD/MySQL）由开启主组件时加入**可见计划**；跨能力前置（已有 StorageClass/S3/认证入口）必须显式配置或存在，不默默安装 Ceph、Dex、Istio。

| 能力 | 管理者 | 消费者不能做什么 |
|---|---|---|
| 主 CNI | 首装对应 CNI role | components 入口不得更换、重启或补丁修复 |
| kcn 专用 Envoy | 既定 kcn 路径 | Kubeflow/业务网关不得复用/覆盖 |
| NAD CRD | Multus 接入批次 | KubeVirt/LB 不各装一份；已存在同源兼容 CRD 可引用，不抢 owner |
| Snapshot CRDs/controller | Snapshot 批次 | Ceph/CDI/KubeVirt 不重复安装控制器 |
| cert-manager | 当前 cert-manager role | Kubeflow 不重复部署、不降版本 |
| JobSet | Trainer 依赖项的唯一 owner | 不能把 LWS 当替代品，也不默认引入 Kueue |
| Argo Workflows | 当前 KFP 配套安装 | 不与将来其他独立 Argo 控制器重复监听同一资源 |
| PostgreSQL / RGW | 当前底座 | 新组件只拿独立数据库/账号/桶，不能改共享实例认证或升级版本 |

写入共享资源前检查现有 GVK、schema、存储版本、管理标签/Helm 注解；不兼容拒绝。禁止 `--force-conflicts`、删除重建 CRD、改别的 release ownership。固定 CRD 更新也需要独立授权，不能借“按需安装”完成升级。

## 8. 两种发布物与可信材料

### 8.1 Code release

包含 `kk`、小型入口脚本、`checks/<component>/`检查器、对应测试通过记录和摘要。检查器源码归属`scripts/acceptance/<component>/`，按已实现组件收录；它们不是大镜像artifact的一部分。`kk` 内含所需 builtin roles。记录 Go 工具链、工作树内容摘要、二进制摘要、构建参数与文档修订。只改编排/checker 不重做 artifact；代码发布不等于对外发布或 Git push。

### 8.2 Artifact

包含已批准镜像归档、KubeKey artifact、系统软件包 ISO、Helm/Hauler 等工具、Chart、确有需要的固定来源资源/运行样本。用户场地密码、实验脚本、日志和 kubeconfig 不进入 artifact。

继承 `components.lock.yaml`、镜像 TSV 和现有布局，不直接覆盖为本包的 `06-version-register.yaml`。材料锁需要能表示源 index digest、平台 manifest digest、本地实际提供的 manifest digest；格式转换使 digest 变化时，记录转换来源和配置/layer 内容对应证据，不能伪造摘要相等。

镜像身份检查最少覆盖：原始 ref、linux/amd64 平台、实际内容摘要、目标 ref 无碰撞、所有 layers/config 可读取；HTTP 200 和“数量一致”都不够。Chart SHA、依赖 Chart SHA、工具 SHA、ISO SHA 逐一比较。未选但已声明的材料可留在基础包中；**未声明的多余 Chart/镜像不得自动收进新发布物**。

可信锁来自已审核源版本/独立保存的 release manifest。攻击者或误操作把锁与包一起重算，SHA256SUMS 也会通过；因此记录批准的锁摘要，不能只信包内自证。首版不强制引入新的签名基础设施。

### 8.3 新增组件的材料供应

R15 的 component-only **不负责在线下载或偷偷重启原 registry**。本轮简化为：每批建立健康底座时先提供包含本批所需镜像的累计 artifact；健康底座快照与这个 artifact 绑定；之后代码迭代只新增组件，不重复安装 Kubernetes。

若现有 registry 缺新材料，预检必须停止。准备新的批次底座/材料交付，不让执行模型手工覆盖旧 hauler store。已有健康底座的增量材料更新可另开受控能力，但不是本期正确性的前提。最终发布仍从原始干净快照跑全链一次。

## 9. Run 记录与异常语义

每次生成新 runID，采用 `runs/<runID>/` 保存证据，**已有报告目录不等于发生过集群变更**。最低记录如下：

```json
{
  "schemaVersion": 1,
  "runID": "EXAMPLE_ONLY",
  "mode": "components",
  "target": {"name": "ani-lab", "networkStack": "kubeovn", "clusterUID": "REPLACE"},
  "identity": {"sourceTreeSHA256": "REPLACE", "kkSHA256": "REPLACE", "artifactLockSHA256": "REPLACE", "effectiveConfigSHA256": "REPLACE"},
  "requested": ["milvus"],
  "resolved": ["milvus-etcd", "milvus"],
  "changesStarted": false,
  "installation": {"status": "planned"},
  "verification": {"smoke": "not_run", "acceptance": "not_run"},
  "evidenceDir": "REPLACE"
}
```

`clusterUID` 可取已建集群固定 namespace UID 并记录所取对象，不仅按同名 cluster 判断。运行身份还记录三台地址、实际安装节点、命令、起止时间。`effectiveConfigSHA256` 基于不含秘密值的有效配置与 secretRef；带密码旧配置仅可私有保存并记引用，不作为公开可离线猜测凭据的摘要输出。

状态：`planned → preflight_failed`，或 `planned → installing → succeeded/install_failed/remote_result_unknown`。统一使用上述 snake_case 命名，三文档与代码必须一致。验证状态单独记录 pass/fail/skipped/not_run；“安装已完成、专项验收失败”可以同时成立，不能合并成全部通过。

首个写操作之前必须写 `changesStarted=true`。本地取消不表示远端操作未发生；不确定时标记 unknown，禁止重放。取得本地安装节点锁和 Fedora 实验锁后才能变更；锁占用就退出，不能 kill 旧进程或删除锁文件绕过。

## 10. 三层验证，不重复重建

| 层次 | 操作范围 | 触发方式 |
|---|---|---|
| 安装必要探测 | 控制器/工作负载就绪；专用测试对象最小读写；不得重建组件 | role 安装时一次 |
| smoke | 只读检查为主，允许声明的独立临时测试资源；不删组件 Pod | `verify --level smoke` |
| acceptance | 已知对象 UID 的一次计划内 Pod 重建、数据复读、断网端到端验证 | 单独显式执行；需旗标 |

只读诊断可以继续；一个变更型检查失败后，其他变更型检查 not_run。成功后的清理也只能删除 run 明确创建、带标签且 UID 匹配的测试资源；失败时默认保留。专用测试 PVC 若确需清理，只能在另行明确的实验收尾中列出，不得将删除 PVC 混入故障恢复。

每次 API 有单请求超时，每阶段有总 deadline；失败输出最后一次条件、事件与相关日志，不给 CNI/OVS 自动重启。超时不是自动加倍等待的理由，调整预算需要证据和单独记录。

## 11. CI、增量反馈与定义“完成”

统一 `scripts/check-code.sh` 跑：Go 单测；真实模板函数渲染；生成 YAML 解析；嵌入脚本语法/行为；错误传播；镜像闭包；已声明 checker fixture。普通 PR 不自动还原三台实机。Go 版本按 `go.mod` 所需工具链准备，不改 go.mod 降级逃避。

测试不能只靠字符串存在；旧审查复现中的 `confirmed=true` 表示“缺陷被复现”，**不是通过**。每次修复将错误预期倒转，成为回归测试。

完成至少四个独立字段：`code_tested`、`material_verified`、`live_smoke`、`clean_offline_acceptance`，另记 `ani_integration`。仅代码通过不得标记整个任务 done；无环境则写 not_verified/blocked 并给下一条可执行动作。

## 12. 本蓝图的实施顺序

不先搞一次大重构：R00 校准来源 → R01/R02/R03 消除风险 → R04 建局部门禁 → R05–R09 落存储/配置/材料/预检 → R10–R14 修网络和验证/生命周期 → R15 最小新增组件入口 → R16 真实验收闭环。随后按第三份计划接组件。

每个任务只做本卡允许范围；所需公共能力缺失则依赖对应 R 卡，不在某个组件任务中私造另一个入口。详细步骤、命令及退出标准见各卡。

## 来源

SRC-CODE：上传 `ani-installer.zip` 的上述文件与配套 `reference/source-baseline.json`。
SRC-AUDIT：`ANI-installer-code-audit-20260924.md` §A01–A15。
SRC-MATRIX：`ANI-installer-version-matrix-20260923.md/.yaml`；使用时以本包的 Envoy 隔离决定覆盖已撤销条款。
SRC-HTML：离线对话最后形成的边界：KubeKey、独立发布、只操作授权实验机、失败保存现场。
WEB-NET：官方 Kube-OVN 1.16.x LoadBalancer Service 文档，本次复核，见 `reference/official-sources.md`。

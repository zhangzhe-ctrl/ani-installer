# 基础组件第二批：指标、告警和可选日志后端执行方案

日期：2026-09-19。项目：`/home/chabking/workspace/ani-installer`，Go 模块：`kubekey/`。

**本文件是第二批实施依据。一次只实现一张任务卡，不把所有组件堆完再试装。本文要求编写代码和验证的工作，由后续执行 AI 完成；本文编写过程没有实施这些组件。**

## 1. 范围和当前起点

整体顺序保持：第一批数据库/缓存/消息/证书 → **本批指标/告警/日志** → Milvus → KubeVirt/CDI/Volcano/LWS → Harbor/GPU。不要跳到其它批次。

本批有以下安装选择，第一批组件保持原实现：

| 配置 | 本批部署内容 |
| --- | --- |
| metrics=false、logging.backend=none | 本批全部关闭，旧配置行为保留 |
| metrics=true、logging.backend=none | Prometheus、Alertmanager、Prometheus Operator、kube-state-metrics、node-exporter及必需配套 |
| logging.backend=loki | 单实例 Loki + Fluent Bit；metrics 独立选择 |
| logging.backend=opensearch | 单实例 OpenSearch + Fluent Bit；metrics 独立选择 |

日志默认关闭。Loki/OpenSearch 是**首装前二选一**，不是热切换、双写或历史数据迁移。启用日志时自动部署 Fluent Bit，不额外给它一个容易造成无后端采集的用户开关。指标栈作为一个整体开关。

本批不含 Grafana、OpenSearch Dashboards、Jaeger、Envoy AI Gateway、ANI 业务服务/指标/规则、APM、业务审计、HA、外部日志服务接入、安装后增装、升级、卸载、备份、跨集群采集、CNI 修复或通用插件框架。OpenSearch 只作容器日志后端，不提供 ANI 搜索/向量业务能力。

### 1.1 第一批尚未完全验收

先读 [B5 复核与 kcn 根因](foundation-b5-verification-20260919.md)。现场的旧 kcn v0.6.2 会把旧 sandbox 的迟到 DEL 用于删除同名 Pod 的新 NIC；用户告知新版已修复，但**尚未提供新版镜像标识，也未复验**。当前 NATS 失败不能被第二批覆盖成 pass。

执行方式分为两个工作阶段，**不是新增 installer CLI 参数**：

- **材料/代码阶段**：可按 C0→C1→C2→C3→C4 完成材料核对、代码、Fedora 测试和真实 Chart 渲染。每卡达到代码验收后才能写下一卡；真实安装统一标 `not_verified`。缺新版 kcn 时采用这个阶段，不能为等输入伪造材料，也不能反复用旧版试装。
- **真实安装阶段**：用户后续提供明确的已修复 kcn 镜像/tag/摘要并安排验证后，冻结此输入；按 C2→C3→C4 分别从干净快照验证，逐卡通过再继续。首张卡同时复核第一批遗留问题。镜像固定替换属于材料集成，不修改 kcn 源码，也不假定不同镜像可复用未经确认的安装清单。

如果新 kcn 的安装清单/配置接口有变化，由组件方提供配套材料；不要修改组件源码或自行发明兼容分支。当前用户仅要求本次产出执行文档，**不授权本次编写文档期间启动集群安装**。

## 2. installer 职责边界（每卡都必须遵守）

### 2.1 程序必须承担的工作

- 使用固定离线镜像/Chart，处理受支持参数，正常配置系统前提，创建 namespace/Secret/PVC/证书，按顺序部署、等待、验证、记录原始错误。
- Fluent Bit 的 Kubernetes 日志路径、元数据、输出端、TLS/凭据及日志后端的正常保留策略；Prometheus 的基础抓取与 Alertmanager 连接。
- OpenSearch 所需宿主机参数，例如核实并设置 `vm.max_map_count`。需要时由正常安装步骤配置，不要求用户登录三台手工执行。
- 安装及验证使用正确 kubeconfig；普通 ubuntu 用户安装入口保持可用。
- 负责缺镜像、错 values、证书 SAN、Secret 引用、PVC/权限、CRD 顺序、镜像地址、探测脚本错误。先排除这些输入错误，再判定为组件 bug。

### 2.2 明确禁止

不得向 kk、安装 role、代码包或 artifact 加入：ESXi 调用、快照还原、kubeadm reset、清盘、清 `/var/lib`、清 CNI/OVN、删失败 namespace/PVC、强制 detach、循环重启 Pod、忽略失败、自动换版本或自动改后端。

不得改 Prometheus/OpenSearch/Loki/Fluent Bit/kcn 等组件源码、制作组件临时修复镜像，或者关闭健康探针/认证、用 emptyDir 替代数据 PVC来获取通过。不得为本批引入 Ansible/Kubespray、第二套 SSH 引擎、DAG、修复服务或 add/repair/resume/reset 产品命令。

**组件自身问题交给组件维护方；installer 只保存明确的版本、配置、复现和日志。**官方 Chart 的正常配置、证书/用户初始化、OpenSearch 索引模板/保留策略属于安装编排，可以实现；修组件内核或修用户数据不属于 installer。

实验快照、离线隔离、传输、持久化重建测试放在 Fedora 本轮 `lab/` 中，禁止被 `install.sh`/role 调用或打入发布包。普通 `verify.sh` 不重启服务。

成功后删除本次唯一标识的测试 Job/测试规则，属于测试资源生命周期；失败时保留证据和测试资源。不得使用宽泛 selector 清整类 Pod，不清用户日志或数据库。

## 3. 固定环境与执行位置

| 项目 | 固定值 |
| --- | --- |
| 本地 | `/home/chabking/workspace/ani-installer`，只编辑/阅读源码和文档、发起传输 |
| 构建/测试/格式化/渲染/下载/镜像/制包/实验调度 | 全部通过 SSH config host **fedora** |
| 目标 | 仅 `172.16.101.20/.21/.22`，SSH 用户 ubuntu |
| .20 | 同时作为 installer、临时 Hauler 仓库和集群节点 |
| 系统 | Ubuntu Server 24.04 amd64，现有快照实际为 24.04.4 / 6.8.0-139-generic |
| 网络 | ens34 管理；无主机 IP 的 ens35 给 kcn；ens36 本批不配置 |
| Ceph | 三台各 `/dev/sdb` 空盘，沿用既定三节点 Rook/Ceph；RBD SC `ani-block` |
| 禁止操作 | `.10/.11/.12`、其它 VM/集群、ANI 业务源码、组件仓库 |

底座固定 K8s v1.35.8、containerd v2.3.4、runc v1.4.3、Hauler v2.0.3、Helm v3.20.0、Rook v1.20.7、Ceph v20.2.4、既定定制 Envoy。kcn 的新固定输入单列待提供；不顺便升级其它底座。

本文件编写时已只读核实以下 Fedora 路径存在。C0 再核对内容，不把目录名当作构建证明：

```text
旧代码参考：/home/chabking/ani-installer-runs/foundation-20260918/releases/ani-code-20260918-b4
旧物料参考：/home/chabking/ani-installer-runs/foundation-20260918/releases/ani-artifact-ubuntu24-amd64-20260918-b4
旧私有站点：/home/chabking/ani-installer-runs/foundation-20260918/inputs/site-b4-cluster.yaml
凭据目录：/home/chabking/ani-installer-runs/platform-20260918/access/
节点执行器：/home/chabking/ani-ops/run_on_node.sh
离线隔离：/home/chabking/ani-ops/apply_offline_isolation.sh
快照工具：/home/chabking/ani-installer-runs/recheck-20260918/restore_esxi_snapshots.sh
实验锁：/home/chabking/ani-installer-runs/locks/cluster-20-22.lock
```

旧 kk 实际 SHA256 为 `84dc71a2c9ae569612aff208a7d220238bcebfb3111c0203f9209b414db15f00`，部分旧文档写错成 `256c0e1e…`；本批以实际构建/传输/运行三处摘要为准。不要覆盖旧包并沿用同一版本目录。

在 Fedora 创建 `ani-installer-runs/observability-<时间>/`，分 `src/ inputs/ releases/ evidence/ lab/`。同步当前未提交的完整工作内容，不能只复制旧 HEAD 覆盖修改；保留 Linux LF 的 shell 物料。所有制包先落 Fedora，目标快照会删除节点副本。

密码读取 access 下已有私有文件，不打印、不作为 argv、不提交、不写 artifact。相同节点不可并发调用会写 `/tmp/_ron.sh` 的旧 runner。原有 b2/b3/b4 的 heal/watch 脚本不得复用。

## 4. 唯一配置接口与代码落点

### 4.1 增加现有 components 下的两项，不建另一套配置

下列字段是本批拟实现接口，当前代码尚不存在；其余第一批字段保持原样。

```yaml
components:
  metrics:
    enabled: false
    storageClass: ani-block
    prometheusStorageSize: 5Gi
    alertmanagerStorageSize: 1Gi
    prometheusRetention: 24h
  logging:
    backend: none                 # none | loki | opensearch
    storageClass: ani-block
    storageSize: 5Gi
    retentionDays: 3
```

缺省 metrics 为关闭；缺省 logging 为 none。storageClass/容量有上述默认值；只有启用对应功能才校验。只做未知字段、枚举、正数容量/保留时间、明确依赖、启用项物料这些直接检查，不先做复杂兼容/容量规则引擎。容量不能只检查 ParseQuantity 成功，还必须大于零。

metrics 的保留时间本批只接受正整数 `h`/`d`；logging 保留天数是正整数。OpenSearch 非 demo TLS 使用已有内部 CA，因此 backend=opensearch 要求第一批 `components.certManager.enabled=true`，不满足时给清楚错误，不静默启用或关闭安全功能。Loki/指标栈不因此强制依赖第一批数据库。

日志配置不暴露任意 Chart values、任意镜像、双后端、外部URL、TLS skip verify或自定义保留脚本。资源配置由固定部署方案管理，C0 记录实际值；需要调整已知资源值时改明确字段/固定 values，不开任意注入接口。

### 4.2 现有代码最小扩展

| 文件 | 要做的改动 |
| --- | --- |
| `kubekey/pkg/ani/config.go` | typed Metrics/Logging；默认值、依赖/枚举；统一 Selection 与 KubeKeyConfig 参数 |
| `kubekey/pkg/ani/runner.go` | 新选择表、启用 Chart 必需文件、成功后的连接说明；保留原安装链路 |
| `kubekey/ani/components.lock.yaml`、`images.tsv` | 追加固定材料和唯一镜像映射，不另建 registry 映射表 |
| `kubekey/builtin/core/playbooks/create_cluster.yaml` | NATS 后依次 metrics → 选中的日志后端 → Fluent Bit，整个 role 条件跳过 |
| `kubekey/builtin/core/roles/ani/{metrics,loki,opensearch,fluent-bit}/` | 正常安装、固定 values、唯一 verify 模板 |
| `kubekey/scripts/verify.sh` | 同版选择文件解析；调用实际启用组件的唯一脚本；逐项结果 |
| `kubekey/scripts/build-code.sh`、`build-offline.sh` | 沿用现有双发布流程，只补确实缺少的材料支持 |

选择文件沿用首行 `# config_sha256=<sha256>`，其后**固定八行**：

```text
cert-manager
postgresql
valkey
nats
metrics
loki
opensearch
fluent-bit
```

每行第二列 TSV 为 true/false。后四行由 typed 配置推导：metrics=enabled，loki/opensearch 互斥，fluent-bit=(backend!=none)。kk 与 verify 同时更新；未知/缺行/重复/配置摘要不一致明确失败。旧 YAML 用新代码全新安装仍成立；不实现旧四行 runtime 迁移，不让新 verify 去解释旧安装目录。

现有 `componentChartMaterials` 只列了 cert-manager；扩展时补齐已有 NATS 和本批所有启用 Chart。现有 lock 并非版本求解器，也未完整驱动检查；本批明确核对固定 Chart/镜像和 SHA，不声称已有自动能力。build-offline 当前会复制 CHARTS_DIR 下全部 tgz，使用本批专属目录，不混入无关下载物。

## 5. 固定材料与发布

官方材料核对详见本批两份研究附录；附录的候选不是安装通过证明。C0 将实际使用的 Chart/app、来源、SHA256、linux/amd64 镜像摘要、支持范围证据和资源名填入材料锁后才制包。

本次选定的候选如下；执行 AI 按此固定组合开始，不自行追最新版。材料/静态渲染通过与真实集群验证分开记录：

| Chart | 固定版本 | 应用组合 |
| --- | --- | --- |
| kube-prometheus-stack | 85.4.0 | Operator/reloader 0.90.1；Prometheus 3.11.3-distroless；Alertmanager 0.32.1；kube-state-metrics 2.19.0；node-exporter 1.11.1-distroless；certgen 1.8.3 |
| Loki | 18.13.3 | Loki 3.7.8，固定官方迁移后的 grafana-community Chart 来源 |
| OpenSearch | 3.8.0 | OpenSearch 3.8.0 |
| Fluent Bit | 0.58.2 | Fluent Bit 5.1.2 |

实验HTTP接收器/客户端/日志发射器复用固定官方工具镜像 `docker.io/library/python:3.13.11-alpine3.23`（本次已核对amd64可用，摘要见指标附录），通过短脚本运行，不装pip/apk、不自建组件热修镜像。所有工具同样离线供应。

- [指标及采集材料核对](research/observability-metrics-materials-20260919.md)
- [Loki/OpenSearch 材料核对](research/observability-logs-materials-20260919.md)

优先固定官方 Chart，复用其工作负载；不自写 Operator，不换一个商业镜像发行源，不让 AI 遇错就找其它 Chart。精确候选以附录最终核对结果为准，禁止 `helm install ... --version latest`。

所有主容器、operator、config reloader、exporter、init、hook、证书生成和验证客户端镜像都必须进入 images.tsv/Hauler。Prometheus/Alertmanager 实际镜像由 Operator 二次生成，不能只扫描 helm template 中的 Deployment。Chart依赖须已装入tgz，目标不执行 dependency update/repo update。

检查渲染输出：image 不为空、不含 `<no value>`，实际工作负载全用 `.20:5000/...` 等站点 registry 地址，不能把物料表占位符 `127.0.0.1:5000` 放进其它节点 Pod。分别记录源 multi-arch index 与实际 amd64 manifest 摘要。

```text
独立代码包/                  独立 artifact/
  kk install.sh               bin/hauler、bin/helm
  verify.sh probe.sh          repository/<原Ubuntu ISO>
  SHA256SUMS                  packages/<原KubeKey runtime artifact>
                              images/images.tsv、images.haul.tar.zst
                              charts/<组件>/<固定版本>.tgz
                              config/components.lock.yaml 等
                              licenses/、SHA256SUMS
```

Go/role/values/verify 变化只重建小代码包；Chart/镜像变化才更新相应物料。复用原 ISO/runtime/archive，日常使用展开目录，不每改一行都重打/传整个大包。最终日志两个后端的物料可同时放入同一 artifact 以供离线选择，**存在物料不等于部署两个后端**。

## 6. 每张任务卡的执行纪律

每卡报告 `code_status` 和 `live_status` 两列。代码 PASS 不等于安装 PASS；缺新版 kcn 时不启动实验，只记录 live=not_verified，并可继续下一张已定义的代码卡。

真实安装阶段每个新 attempt：

1. Fedora 构建/渲染/针对性测试成功，冻结完整源码摘要、kk SHA、代码包和 artifact SHA、私有配置 SHA。只使用本卡已存在的实际路径。
2. 持有实验锁，确认三台未被其它任务占用；调用现有快照脚本 dry-run，严格核对 test-installer-01/02/03、VMID 5/6/7、snapshotId=1、总数3。结果变化则停，不扩大白名单。
3. execute 还原3/3；确认 SSH、admin.conf/runtime 不存在、既定 sdb 空盘；不靠手工清理制造“干净”。
4. 重新 apply/verify 离线隔离：三台公网域名及公网IP HTTPS失败、DNS失败、管理SSH可用。快照会清掉临时规则，不能省略。
5. 传输独立 code/artifact/site，逐项校验；以 ubuntu 的 `install.sh CONFIG ARTIFACT_ROOT` 入口安装，不手工 export KUBECONFIG。
6. 安装真实退出0后运行同包独立 verify，执行本卡 lab 的端到端及正常 Pod 重建测试，再复核断网；保存原始退出码和失败日志。

```bash
# 这是命令形状；C5 必须替换为实际产物路径。此命令在 ubuntu@.20 执行。
set -o pipefail
code_root=/opt/ani-installer/code/REPLACE_CODE_ID
artifact_root=/opt/ani-installer/artifacts/REPLACE_ARTIFACT_ID
bash "$code_root/install.sh" /opt/ani-installer/site/cluster.yaml "$artifact_root" \
  2>&1 | tee "$HOME/ani-install-REPLACE_ATTEMPT.log"
install_rc=${PIPESTATUS[0]}
printf 'INSTALL_EXIT=%s\n' "$install_rc"
exit "$install_rc"
```

实验进程有有界超时、独立日志和结束时释放的 flock。不得创建无人收尾的 `sleep infinity` 锁；不能杀其它持锁任务。SSH中断先确认原进程，不重复起安装。

### 6.1 失败后何时自动还原

用户已授权本轮实验按需自动还原三台，**不用每次重新申请**。但授权不等于相同条件下无限循环：

| 失败类别 | 处理 |
| --- | --- |
| Fedora 编译/测试/下载错误，目标未变 | 修对应代码或物料，不重置 |
| 传输中断、未安装 | 重传本次文件，不重置 |
| installer 配置/编排/验证 bug | 保存证据→修源码→针对性测试→新构建→三台快照→新 attempt |
| 实验准备错误 | 修 lab 输入；若节点已安装修改则重置再开始 |
| 正确输入下组件自身 bug/已知旧 kcn | 保留现场并交接；不得靠快照碰运气或在 installer 中加修复 |
| 未解释原因重复两次/快照结果不明/集群有人使用 | 停止该实验，报告具体阻塞，不抢锁、不清现场 |

每次重试必须能写出“上一失败证据、责任方、本次具体修正”。相同源码/物料/配置/环境未改变，不重新安装。不要复用旧 preplock 中只打印剩余进程却返回成功的逻辑；系统包锁由既有有界等待处理，准备脚本的成功必须真实。

## 7. C0：固定候选、容量与验证输入

只做材料和输入，不改安装引擎。

- 建立 `docs/observability-components-status.md`，记录 C0～C5 的 code/live 状态，旧 B5 blocked、新 kcn not_provided/user_reported_fixed，禁止补造新镜像地址。
- 固定本批 Chart 及全部镜像清单、真实渲染资源名、API/协议、存储、版本；保存 Fedora 下载/渲染命令与摘要。
- 保存三种累计脱敏示例：`config/examples/observability-metrics.yaml`、`observability-loki.yaml`、`observability-opensearch.yaml`，均保留第一批四项开关，后两份 metrics=true、分别选一个日志后端。
- 读现有容量记录并在实际开测前刷新：三台各4CPU/约8Gi内存、50Gi Ceph空盘是历史值。第一批 PVC 合计17Gi，本批默认 Prom5Gi+AM1Gi+一个日志5Gi，合计约28Gi逻辑容量，Ceph三副本还需考虑实际可用空间和预留。
- 显式关闭会增加无用资源的默认组件/缓存/副本。确认每个 Pod 的 request/limit/heap；装不下时报告实际缺口，不能改 emptyDir、删 request 或把单实例验证伪装成 HA。

小规模实验的初始资源值固定如下，写入真实Chart字段并核对渲染结果。它们是候选值而非容量保证；有明确OOM/调度证据时可调整、记录并重新验证，不静默改副本或关闭功能：

| 容器 | CPU request | 内存 request / limit |
| --- | --- | --- |
| Prometheus | 500m | 512Mi / 1536Mi |
| Alertmanager / Operator | 各100m | 各128Mi / 256Mi |
| kube-state-metrics | 100m | 128Mi / 256Mi |
| node-exporter（每节点） | 50m | 64Mi / 128Mi |
| Fluent Bit（每节点） | 50m | 64Mi / 128Mi |
| Loki | 100m | 256Mi / 1Gi |
| OpenSearch | 500m | 2Gi / 2Gi，heap 1Gi |

reloader/init/验证Job同样要纳入实际资源表，不把主容器limit当成整个Pod预算。不另外压缩CPU limit来制造无依据的启动超时。Prometheus retentionSize 默认4GB（对应5Gi PVC），如果用户改容量，则计算保守上限并保证小于PVC，不继续硬编码4GB。

代码验收：材料可获取且可静态渲染；版本和字段有明确来源；未获取项目写 not_verified。新版 kcn 未提供不阻止 C1～C4 的 Fedora 工作，但阻止目标安装。

## 8. C1：配置、选择记录和最小公共接线

只新增第4节的 typed 配置与通路，不实现所有后端。新项在对应卡未完成前，即使解析合法，启用也必须明确报“本批尚未实现”，不得静默跳过。

- 默认/非法枚举/容量/明确依赖；8行选择与同版 verify 一起改。
- 为新组件增加路径入口，保留整体跳过；不创建空壳伪成功 role。
- 最小 Chart 材料检查，补 NATS 既有遗漏；启用缺物料在改变目标前失败。
- 新增真实的“未实现启用即失败”用例。现有测试可能因所有四项均实现而空循环，不算此项证据。
- 渲染测试给完整镜像与组件参数，拒绝空 image、`<no value>`；仅 bash -n 不够。
- 明确补实现成功后生成 `/var/lib/ani-installer/<name>/connections.md`：namespace、实际 Service DNS/端口、Secret引用、CA Secret、保留时间、PVC、版本、验证状态；包括已启用第一批组件，不改它们业务实现。不得假设旧代码已生成此文件，不写明文密码或让普通用户读整个 root runtime。

后端role在Fluent Bit前执行：Loki/OpenSearch的唯一verify先检查各自API、认证/TLS、PVC等正常后端能力；**完整stdout→Fluent Bit→后端查询属于最后执行的Fluent Bit verify/本卡lab**。不能在采集器尚未安装时等待采集结果，也不能直写后端冒充已验证采集链。

验收：Fedora `go test ./pkg/ani`、`go test -tags=builtin ./pkg/ani ./cmd/kk/app`；旧配置的新增项全关，原 APT/kubeconfig 测试不退化；选择缺失/格式不符、日志双后端不可能产生。此卡无须为接口单独重装一次集群。

## 9. C2 / 2A：指标与告警

使用固定 kube-prometheus-stack 官方 Chart，release `ani-metrics`、namespace `ani-observability`。不自行部署第二个 Prometheus Operator。具体版本/values 以官方材料附录和 C0 固定结果为准。

最小部署形态：

- Prometheus 1副本、RBD PVC、配置保留时长和低于PVC容量的存储上限；Alertmanager 1副本、RBD PVC。
- kube-state-metrics 单副本；node-exporter DaemonSet 覆盖三台节点并处理控制节点 taint；Operator及必要reloader/webhook保持正常工作。
- Grafana、Thanos、远程写入、Ingress/公网入口关闭。基础抓取包含 kube-apiserver、kubelet/cAdvisor、kube-state-metrics、node-exporter；不为碰不到的 scheduler/controller-manager/etcd/kube-proxy 端点临时开放端口或泄露控制面凭据。
- 先关闭默认整套规则，明确加入本批测试规则；不把所有 NATS/数据库/ANI业务监控一并打开。本批不承诺生产业务告警规则库。

固定Chart的首次CRD路径为 `crds.enabled=true`、`crds.upgradeJob.enabled=false`。保持admission webhook及create/patch hook（固定certgen镜像），不引入指标栈对cert-manager的强制依赖。Operator参数中的禁用Thanos默认镜像引用在闭包报告标明为禁用，不为消除字符串部署Thanos；本批运行/测试会实际使用的引用必须全部离线改写。

真实验收：

1. 三台 node-exporter 与相应基础 targets 有效；经 Prometheus HTTP API 查询 `up`、`node_uname_info`、`kube_node_info` 和一项实际 cAdvisor 容器指标，返回当前三台/测试工作负载。不能只查 `/ready`。
2. 验证真实 Prometheus→Alertmanager→临时接收器：lab创建唯一标签 PrometheusRule，表达式 `vector(1) == 1` 进入 firing；接收器收到匹配标签的 webhook；再改为 `vector(0) == 1`，接收器收到同一fingerprint的 resolved。不要加 `bool`：告警表达式必须变为空向量才能恢复，单独 `vector(0)` 仍会触发告警。检查路由和 send_resolved，不能直接 POST 告警给 AM 替代 Prometheus 求值链。
3. 临时规则/路由/接收器只绑定唯一测试标签；真实通知渠道（邮件、企业微信等）不在本批，也不能向他人发送测试消息。接收器用 C0 锁定的离线工具镜像，不现场联网安装。
4. lab记录 Prometheus PVC/STS UID，产生唯一测试序列并取得样本时间；正常重建唯一 Prometheus Pod（PVC不变），通过 range query 读回重建前样本。不要只查重建后的 up。
5. Alertmanager 持久化用一个唯一静默（silence）记录：写入→记录 ID→正常重建 AM Pod→API 读回同一 ID、PVC/Secret不变。不宣称进程重启期间告警绝不丢失。
6. 全部已启用第一批组件、原网络/Envoy、Ceph正常功能回归；证据按组件分别记录。

C2 pass：干净断网安装、指标真实查询、firing/resolved实际接收、正常重建持久化均通过。默认不加24小时压测或性能指标承诺。

## 10. C3 / 2B-1：Loki + Fluent Bit

单体 Loki、一个 RBD PVC；使用官方 Chart 支持的 filesystem 部署方案，不为本批引入新 S3 或重新配置 Ceph RGW。查询与写入仅 ClusterIP 内部服务，不开放公共无认证入口。

- 明确关闭读/写/backend分布式副本、额外缓存、canary、gateway、测试hook等未使用组件，防止 Chart默认内存放不下。
- 固定本版对应的单体模式字段、replication_factor=1、存储schema；retentionDays渲染成有效的compactor保留配置，删除标记/WAL/数据需落持久化路径。不能只有参数名而保留策略实际未启用。
- Fluent Bit 官方 Chart，DaemonSet覆盖三台；容器日志用 tail + 正确 CRI解析/Kubernetes元数据，按 namespace/pod/container/node保留可检索字段；不要把每行日志/traceID作为Loki label。
- tail游标DB及有限filesystem buffer为节点上的独立持久目录（hostPath仅用于采集器游标/缓存，**不是替代Loki数据PVC**）。配置大小上限、背压与重试，不承诺无限断网零丢失；不输出认证信息或OpenSearch密码。
- Fluent Bit仅一个选中输出，不同时发stdout和后端造成日志自采集放大。backend=none时不部署采集器。

真实验收：

1. lab在三台各创建一个指定节点的测试日志Pod，stdout打印唯一marker（含此次run/node/序号，非真实业务数据）；通过正常容器日志文件→Fluent Bit→Loki查询API找到三份marker及正确namespace/pod/container/node。不能直接push给Loki冒充采集成功。
2. 记录查询时间和内容、Loki PVC/STS UID；仅正常重建Loki Pod，读回原marker且PVC不变。
3. 正常重建一个Fluent Bit Pod，游标/缓存目录保留；该节点继续产生新marker，旧/新日志均可查。本批允许at-least-once重复，不以重复重试伪造唯一性或承诺exactly-once。
4. 验证实际生效的保留配置；本卡不为等待默认3天而阻塞。真实过期删除如未做有界专项实验，明确 `retention-expiry=not_verified`，不能凭配置就报删除已通过。
5. 确认没有部署OpenSearch/Dashboards；指标栈及第一批回归通过。

## 11. C4 / 2B-2：OpenSearch + Fluent Bit

本卡是真正替代方案：**从三台干净快照安装 OpenSearch 组合，不在 C3 集群卸载Loki后切换。**不复制Loki数据、不删除其PVC来演示替换。

- 固定官方OpenSearch Chart，单节点 discovery.type=single-node，一副本，RBD PVC；日志索引设置副本数0，不能让单节点永远yellow后声称健康。
- 读取并正常配置实际允许调度节点上的 `vm.max_map_count`（按本版官方要求，通常至少262144），在安装 role/节点准备步骤中完成、记录结果。用户不应手工执行；保留原管理网络。不要依赖用户曾经手工改过的状态。
- 显式heap、CPU/内存request/limit，heap低于容器限制并给堆外内存留空间。资源不足报告缺口，不通过清空requests/关闭探针规避。
- 保持Security插件和TLS；关闭demo配置，使用cert-manager内部CA签发HTTP/transport/admin所需证书，实际Service/Pod DNS在SAN中。证书申请、Secret、初始安全配置、受限日志写入用户由程序完成；不能用demo证书、默认admin密码、tls.verify=off或关闭security获取通过。
- 管理员仅用于正常首次初始化；Fluent Bit用独立日志写入身份，限制到本批日志索引；验收查询使用相应读取身份。凭据在Secret/保护文件中，不进命令行/日志；不建立ANI租户权限平台。
- 固定按UTC日分割索引 `ani-logs-YYYY.MM.DD`，创建匹配 `ani-logs-*` 的mapping/模板和ISM保留策略。按索引年龄删除，保留天数不是逐条文档的精确年龄；不把持续写入的所有日志放在一个永久索引后定时整库删除，不另做rollover引擎。Fluent Bit output 配置匹配本版，含 `Logstash_Format On`、相应prefix/date format、`Suppress_Type_Name On`、可用的记录ID机制和保留Kubernetes元数据。错误应进入可诊断日志，不silent drop。

具体初始化顺序固定为：cert-manager证书Ready → Chart启动并监听9200 → 一次性安全初始化Job → 认证/TLS API验证 → 日索引模板和ISM策略 → Fluent Bit。Chart TCP就绪只是中间条件，不是安装完成。初始化Job复用同版OpenSearch镜像中的securityadmin工具，挂载独立admin证书和受版本管理的安全文件；hash生成优先使用已有Go bcrypt能力，不把密码交给命令行。严禁用 `--no-hooks`、demo配置、`-nhnv`、删安全索引或循环初始化绕过错误。仅日志索引零副本，不全局修改所有系统索引来消health告警。

真实验收：

1. 正确CA+认证访问API；错误凭据明确401/403或协议中的认证拒绝，缺CA验证失败。连接超时不能算“认证拒绝”。单节点健康检查、主分片和索引副本设置正确。
2. 同C3从三节点测试Pod stdout采集marker，经OpenSearch `_search` 查回载荷及元数据，验证Fluent Bit用写入身份而非admin。
3. 记录Pod/STS/PVC/Secret UID，正常重建唯一OpenSearch Pod，TLS/认证仍正确，同一索引读回原marker，PVC和凭据未替换。
4. 检查日志索引已关联保留策略；真实老索引删除没有实测则标not_verified，不睡3天，也不写定时rm脚本替代产品策略。
5. 确认Loki/Grafana/Dashboards不存在，指标栈和第一批回归通过。

## 12. C5：交付、状态与人工复现

如果C3和C4已有同一最终代码/物料版本的完整证据，直接复用；收尾不为了跑命令再重装。后续修改影响前一后端时必须补对应验证，不能拿旧二进制结果证明新二进制。

需要交付：

1. 源码、针对性测试、固定材料锁；不自动提交/推送/发release。
2. Fedora独立code/artifact及SHA256SUMS、源码快照和实际运行kk对应关系；材料按目录存放，最后才按需归档。
3. 上述三份脱敏配置、私有真实站点配置位置；连接说明包含实际Service/端口/证书与Secret引用，不含凭据。
4. `docs/observability-components-manual-runbook.md`：真实存在的路径、还原→断网→传输→普通ubuntu安装→独立verify；两日志后端给出独立从零流程，不引用B1/B2 heal/watch。
5. `docs/observability-components-status.md` 与 `kubekey/docs/progress.md`，附每attempt失败原因和最终各项结果。

每卡统一填写：

```text
card / attempt:
code_status: pending | pass | fail
live_status: not_verified | pass | fail | blocked
source hash / kk hash / artifact manifest hash / config hash:
KCN version / digest / validated status:
enabled components / backend / chart and app versions:
snapshot 3/3 / offline before-after / install exit:
metrics scrape-query / firing-resolved / logs three-node ingestion:
normal Pod rebuild persistence / PVC and Secret UIDs:
retention configuration / actual expiry (separate):
known issue / owner / exact next step:
```

**整批通过需要两种日志组合分别真实验证。**某后端没做就写not_verified；旧kcn已知故障不能“豁免后全绿”。指标/日志可用不等于ANI业务可观测性完成，不承诺HA、灾备或断电恢复。

## 13. 可直接交给执行 AI 的提示词

```text
请执行 docs/observability-components-batch-execution-plan-20260919.md，并先读 kubekey/AGENTS.md、两份材料研究附录和 foundation-b5-verification-20260919.md。

只做第二批：指标栈Prometheus/Alertmanager/Operator/kube-state-metrics/node-exporter，及统一Fluent Bit采集到Loki或OpenSearch。不要加入Grafana、Dashboards、Jaeger、ANI业务、Milvus、计算/GPU、Harbor或HA。按C0→C1→C2→C3→C4→C5执行，一次只写一张卡。

本地仅编辑/读文件和发起传输；构建、格式化、测试、下载、镜像、Helm渲染、制包及节点操作全部经ssh config host fedora。测试目标只准172.16.101.20/.21/.22，.20承载installer。使用任务私有目录和共享实验锁，不使用其它任务的脏目录，不读取ANI或修改组件仓库。

新版kcn修复目前仅用户告知，镜像材料尚未提供；不能修旧kcn或发明新版地址。缺材料时做各卡Fedora代码/渲染/测试，live统一not_verified，不启动三台安装。用户后续提供固定材料并安排验证后，再按C2/C3/C4从干净快照分别完整安装；不把关闭故障组件当完整通过。

installer只负责固定物料、配置、必要OS前提、正常初始化编排、等待、验证和日志。组件自身bug记录交接，不改源码，不做临时修复镜像，不关闭认证/探针。禁止产品代码调用快照、reset、清盘、清CNI/OVN、删失败PVC/namespace、循环重启或吞退出码。

用户已授权实验阶段按需自动还原三台，不用逐次申请。先保存证据、核对白名单3台与占用；明确installer或实验准备修正后才新attempt，组件bug不靠快照碰运气。不得重复相同失败条件。普通verify不重启服务；持久化Pod重建只在lab按明确UID执行。

保持KubeKey、独立kk代码包与artifact；代码修改只重建小包，镜像/Chart变化才更新材料，不重做已有ISO/runtime。配置严格使用本文components.metrics和components.logging，选择文件固定八行，旧YAML默认本批全关，未实现的启用明确失败，不写第二套解析或通用插件引擎。

每卡更新status和progress，区分代码pass、真实安装pass与not_verified。验证必须经过实际指标、告警webhook firing/resolved、三节点容器日志采集和正常Pod重建回读；不能只测Ready或直接写后端代替采集。C5补真实路径的手动文档和无明文凭据的连接说明。不得自动提交、推送或发布。
```

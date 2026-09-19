# 基础组件第一轮：分批代码实施与真实离线验证

> 2026-09-19：第二批指标/日志实施请按 [第二批分卡执行方案](observability-components-batch-execution-plan-20260919.md)。本文件继续约束第一批四组件。B5 的当前阻塞与新版 kcn 待验证状态见 [复核记录](foundation-b5-verification-20260919.md)，不能据旧计划重新修旧版 kcn 或重复试装。

日期：2026-09-18。仓库：`/home/chabking/workspace/ani-installer`；Go 模块：`kubekey/`。

**这是下一轮当前执行依据，范围只有 cert-manager、PostgreSQL、Valkey、NATS。先读第 1～4 节，再从 B0 开始，一次只完成一个批次。禁止把历史完整组件计划中的所有任务一并恢复。**

用户已反馈 r12 的手动安装验证通过。现有 Kubernetes、kcn、定制 Envoy、Ceph 作为本轮底座，不再次升级。该反馈是用户验收记录；不是本文件作者重新执行了一轮安装。

## 1. 目标、范围与完成条件

最终用户仍通过普通 `ubuntu` 用户运行现有 `install.sh CONFIG ARTIFACT_ROOT`，由 `.20` 同时承担 installer、临时 Hauler 仓库及集群节点。从三台干净 Ubuntu VM 开始，自动完成现有底座和选中的四个组件。用户不应手动安装依赖、导出 KUBECONFIG、创建 Secret/PVC、安装 Chart 或在宿主机下载验证客户端。

本轮新增：

| 组件 | 唯一部署方案 | 存储/认证 |
| --- | --- | --- |
| cert-manager | 官方固定 Chart；最小内部 CA | 不使用公网 ACME；私钥仅在 Secret/受保护文件中 |
| PostgreSQL | 单实例 StatefulSet，官方 postgres 镜像 | `ani-block` RWO PVC；独立管理员与应用凭据 |
| Valkey | 单实例 StatefulSet，官方 valkey 镜像 | `ani-block` RWO PVC；认证；AOF everysec |
| NATS | 官方固定 Chart，单实例 JetStream | fileStore + `ani-block` RWO PVC；客户端认证 |

不做：ANI 系统或其业务迁移、其他基础组件、Harbor/镜像交接、HA、集群扩缩容、升级、卸载、安装后增装、备份平台、数据迁移、整群断电自动恢复、CNI 切换、Ceph 改网、GPU、通用插件/依赖框架。

四个开关独立、默认关闭；没有新配置字段的旧站点配置仍按原流程安装。单实例仅指本轮四组件的既定服务形态，不改变 kcn、Envoy、Ceph 已有副本数。

最终 PASS 必须同时满足：实际运行的是本轮代码包；三台干净快照；安装前后断网证据；普通用户入口完整安装退出 0；三个数据组件的实际功能及正常 Pod 重建持久化验证、cert-manager 的签发/SAN/证书链验证；原网络/Envoy/Ceph 回归；连接说明可用。Ready、模板渲染成功、测试全绿均不能单独代表本轮完成。

## 2. installer 与测试流程的职责边界

### 2.1 installer 应做什么

- 准备固定物料、离线镜像和系统依赖，渲染受支持参数，创建 namespace/Secret/PVC/工作负载，按固定顺序部署，等待就绪，运行正常功能探测并保存原始错误。
- 对遗漏镜像、错误仓库路径、Chart values 不生效、缺 Secret、错误 PVC/权限、CRD 顺序、客户端配置、connector/提权环境错误负责。
- 安装任务明确使用 `/etc/kubernetes/admin.conf`；管理用户 kubeconfig 导出继续读取配置用户和真实 home，保留 r12 修复。
- 只创建部署这些组件所需的基础数据库/用户、消息服务与证书能力；不导入 ANI 的业务表、应用配置或业务数据。

### 2.2 installer 不应做什么

**禁止把以下行为加入 kk、安装 role、生产脚本或 artifact：**

- 调用 ESXi、还原快照、自动 kubeadm reset、删除 `/var/lib` 状态、擦盘、扫描并清理上一次安装残留。
- 为了使安装通过而删除组件 namespace/PVC、强制解除卷挂载、清理 OVN/LSP、删除数据库内部对象、反复重启组件。
- 忽略失败、吞退出码、失败后自动换版本/实现、无限重试、无限增加 timeout、用 emptyDir/hostPath 替代持久卷。
- 修改 PostgreSQL、Valkey、NATS、cert-manager、Ceph、kcn、Envoy 等组件源码或构建“临时修复镜像”。
- 新建 add-component、repair、resume、reset 命令，或通用 DAG、插件系统、兼容矩阵引擎。

正确配置下组件自身崩溃、错误数据或内部状态异常，属于组件维护方。先核对实际镜像、渲染配置、PVC/Secret、权限和顺序，再归责；不能把 installer 参数错误推给组件。组件问题记录版本、配置、复现步骤和日志交给用户，不擅自操作组件仓库、发送外部消息或换镜像。

### 2.3 正常操作与“清理失败现场”的区别

| 行为 | 是否允许 |
| --- | --- |
| 官方正常初始化、Chart 配置、CRD/资源创建、就绪等待 | 允许，由 installer 执行 |
| 清理本次构建的临时目录 | 允许，不涉及节点或业务数据 |
| 验证成功后删除本次测试创建的测试资源 | 允许，按唯一名称/标签定位；失败时保留 |
| 专用实验中正常删除一个已确认 UID 的服务 Pod，验证同一 PVC 上的数据恢复 | 允许，仅测试脚本执行；不是自动恢复逻辑 |
| 安装失败后删资源再继续安装 | 禁止；改完后从三台快照重新开始 |
| 测试环境通过现有脚本还原三台指定 VM | 用户已授权，归测试流程；不得进入产品安装链路 |

普通 `verify.sh` 不应重启用户正在使用的数据库。持久化重建测试放在实验脚本中，明确只用于本轮专用测试集群。

## 3. 环境、基线与执行边界

### 3.1 固定环境

| 项目 | 固定值 |
| --- | --- |
| 本地源码目录 | `/home/chabking/workspace/ani-installer` |
| 构建、格式化、测试、下载、制包、调度 | **全部通过 `ssh fedora`** |
| installer / node1 | `172.16.101.20` |
| node2 / node3 | `172.16.101.21` / `172.16.101.22` |
| SSH 用户 | `ubuntu` |
| 系统 | Ubuntu Server 24.04 amd64，沿用已验收快照 |
| 网卡 | ens34 管理网；无主机 IP 的 ens35 给 kcn；本轮不配置 ens36 |
| Ceph 盘 | 每台既定空白 `/dev/sdb`，不自动选择其它盘 |
| 禁止操作 | `.10/.11/.12`、其它 VM、ANI 应用源码和其它组件仓库 |

本地只读写源码/文档及发起 SSH 传输；不在本地运行 go test、gofmt、make、Helm 渲染、镜像操作或安装命令。不要切换历史 Ubuntu 构建机，也不要使用 Fedora 上其它任务的工作目录。

### 3.2 当前可复用材料（B0 再核对一次）

以下均为 Fedora 路径，本文件编写时已确认存在；不能用路径名代替内容校验：

```text
代码：/home/chabking/ani-installer-runs/fix-kubeconfig-20260918/ani-code-kubeconfig-r12
物料：/home/chabking/ani-installer-runs/platform-20260918/run/ani-artifact-ubuntu24-amd64-20260918-r2
站点配置：/home/chabking/ani-installer-runs/recheck-20260918/cluster.yaml
节点密码文件：/home/chabking/ani-installer-runs/platform-20260918/access/node-password
ASKPASS：/home/chabking/ani-installer-runs/platform-20260918/access/askpass.sh
节点脚本执行器：/home/chabking/ani-ops/run_on_node.sh
断网工具：/home/chabking/ani-ops/apply_offline_isolation.sh
快照工具：/home/chabking/ani-installer-runs/recheck-20260918/restore_esxi_snapshots.sh
```

密码读取已有私有文件，不写入文档/命令行/代码/提交。站点配置也含凭据，不直接打印、提交或打入 artifact。

已有底座固定为 Kubernetes v1.35.8、containerd v2.3.4、runc v1.4.3、Hauler v2.0.3、kcn v0.6.2、既定定制 Envoy、Rook v1.20.7、Ceph v20.2.4。镜像摘要和材料以 B0 核对的现有包为准。不要顺手升级 Helm、系统 ISO、Ceph CSI 或替换定制 Envoy。

Ceph 认证与 PG 数告警按已有记录保留，不在本轮悄悄调参消警；如果新出现 OSD 不可用、PG 不干净或 PVC 故障，应作为阻塞证据处理。本轮不因这些已知事项扩成 Ceph 优化项目。

### 3.3 隔离工作目录与互斥

在 Fedora 创建独立 `ani-installer-runs/foundation-<时间>/`，包含 `src/`、`inputs/`、`releases/`、`logs/`、`evidence/`、`lab/`。同步当前完整源码和未提交改动到 src；不要用旧 HEAD 替代当前源码，不覆盖既有远端脏工作区。

使用一个固定 Fedora 文件锁（例如 `/home/chabking/ani-installer-runs/locks/cluster-20-22.lock`）覆盖一次实验的快照、传输、安装和验收全程。用普通 flock 即可，不开发锁服务。开始前确认没有其它任务/人工安装正在用三台；占用时停止，不杀进程或“清锁”。SSH 中断后先确认原进程及退出记录，不能重复启动。

已有 runner 向目标的固定 `/tmp/_ron.sh` 写脚本；**同一节点不可并发调用它**。SSH 从管道接收 tar 或脚本内容时保留 stdin；无输入的 SSH 命令用 `/dev/null`，避免 SSH 吃掉外层脚本剩余内容。目标机恢复后主机名会变化，使用固定 IP 和站点配置。

## 4. 最小接口和物料约定

### 4.1 首装配置（本轮要实现，不是现有功能）

在现有站点 YAML 增加以下可选部分；其它字段保持兼容：

```yaml
components:
  certManager:
    enabled: false
  postgresql:
    enabled: false
    storageClass: ani-block
    storageSize: 10Gi
  valkey:
    enabled: false
    storageClass: ani-block
    storageSize: 2Gi
  nats:
    enabled: false
    storageClass: ani-block
    storageSize: 5Gi
```

缺省 components、缺省组件或缺省 enabled 均为关闭。启用时默认存储参数如上；B0 按三台可用内存/磁盘做一次容量核对，必要时调整实验值并记录，不改变副本模式、不清空资源请求。只校验未知键、必要参数、有效容量、启用组件材料是否存在这些直接错误，不先建设大量抽象预检。

配置不接受任意镜像/Chart 版本、任意 values 注入、HA 或自动回退。Go 使用明确类型，并把选择结果传入 `ani.components`。旧配置解析、CLI 和已修复的用户导出逻辑必须保留。尚未完成的组件被设为 true 时必须在部署前明确报“本批未实现”，不能接受开关后静默跳过。例如 B1 只能启用 certManager，另外三项仍只能 false；到对应批次再放开。

### 4.2 目录、版本与镜像

- 每个新组件一个 KubeKey role：`builtin/core/roles/ani/{cert-manager,postgresql,valkey,nats}/`。
- 固定顺序：现有底座（含 Ceph）→ cert-manager → PostgreSQL → Valkey → NATS；未启用的 role 整体跳过，不创建其 namespace、Secret、PVC、Chart release 或测试 Job。
- 增加普通 `kubekey/ani/components.lock.yaml`，每组件记录 appVersion、chartVersion（如有）、官方来源、材料路径、SHA256、状态和真实验证记录。它只描述固定材料，不实现版本求解器。
- 镜像映射继续以 `ani/images.tsv` 为唯一来源；包含所有启用副容器、init、hook Job、CRD/webhook 相关镜像和验证工具。禁止 latest 被重新解析后静默替换固定内容。
- 官方 Chart 在 Fedora 下载固定 tgz，放 artifact 的 `charts/<组件>/<固定版本>.tgz`。自己的 values 模板、role、验证代码跟随代码发布，不混进不可变 Chart 物料。
- PostgreSQL/Valkey 用小型 StatefulSet 清单即可，不添加数据库 Operator、Sentinel、Patroni、Valkey Cluster。

本轮候选材料：PostgreSQL `postgres:17.11-bookworm`；Valkey `valkey/valkey:8.1.10-alpine`；NATS 官方 Chart `2.14.6`、app `2.14.6`；cert-manager 官方 Chart/app `v1.21.2`。Chart 与应用版本分别记录，不从一个推测另一个；B0 读取实际包的 Chart.yaml/values，锁全镜像和摘要后才构建。

本文件编写时，通过 Fedora 已下载核对两个官方 Chart，确认上述三种数据库/消息镜像存在 linux/amd64 平台。状态仅为 `materials_confirmed / installation_not_verified`：

| Chart | 固定下载地址 | SHA256 |
| --- | --- | --- |
| NATS 2.14.6 | `https://github.com/nats-io/k8s/releases/download/nats-2.14.6/nats-2.14.6.tgz` | `72f7412d6856a6c75c80d4aa4a1ed88c3846f5728fa8e0cc134a76da0febe3d9` |
| cert-manager v1.21.2 | `https://charts.jetstack.io/charts/cert-manager-v1.21.2.tgz` | `73a56e1728edd6c99f1f31082618c3259d279a76b7ebd3d4bdc5475c2442d34a` |

锁镜像时区分源 multi-arch index digest 与包内 amd64 manifest digest，不能要求二者相等。对应内容只要有变，就要更新材料记录并重新验证，不能静默沿用旧 PASS。

### 4.3 kk 与 artifact 独立

继续使用 `scripts/build-code.sh` 与 `scripts/build-offline.sh`；不创建第三套发布格式。

```text
代码包/                         artifact/
  kk                              bin/hauler
  install.sh                      packages/kubekey-artifact.tgz
  verify.sh                       repository/<既有Ubuntu依赖ISO>
  probe.sh                        images/images.haul.tar.zst
  SHA256SUMS                      images/images.tsv
  ...必要的自有验证代码             charts/<组件>/<版本>.tgz
                                  config/...及components.lock.yaml
                                  licenses/
                                  SHA256SUMS
```

代码改动（Go、role、values 模板、验证脚本）只重建小代码包；不重新下载已有镜像，不重建 Ubuntu ISO 或 KubeKey runtime artifact。镜像或 Chart 改动时，仅准备变化物料，复用原内容，再输出新 artifact 及校验和。按批制作“当前基线+已完成组件”的累计包，不先下载其它未来组件。

部署与验证时必须实际引用包内 Chart 路径，不执行 helm repo update、helm dependency update 或向公网拉 Chart。Chart 依赖应提前打入 tgz。构建阶段的 kk 可作为工具输入，但绝不能被复制进最终 artifact。

原始下载按 charts/images/packages/repository/config 等目录保存，交付时可以再归档大包；日常迭代使用已展开目录，不能每改一行代码就强制压缩、传输整个大包。

### 4.4 凭据、连接说明与验证入口

- 应用 namespace 使用 `ani-platform`；cert-manager 使用 `cert-manager`。固定 Service：`postgresql`、`valkey`、`nats`，均为 ClusterIP，不额外暴露公网入口。
- 初装创建各自 Secret；凭据随机生成且在本次 run 中保持不变，不因重新渲染 values 或验证而轮换。不要把密码写到 Git、artifact、命令参数、普通日志或连接说明。
- PostgreSQL 为管理员与应用用户分别提供 Secret；应用用户不得是超级用户。基础应用库名/用户名用固定文档值（建议 `ani` / `ani_app`），不执行 ANI 业务迁移。
- 连接说明固定输出到 `/var/lib/ani-installer/<name>/connections.md`，内容包括 namespace、Service DNS、端口、数据库名/用户名、Secret 名及字段名、读取方法、StorageClass、PVC、版本、验证结果。可以另行复制无凭据内容给 ubuntu 阅读；不能为此放宽 runtime 目录或凭据文件权限。密码不明文输出。
- 组件验证脚本源码固定在 `builtin/core/roles/ani/<组件>/templates/verify.sh`，随 kk builtin 资源进入代码包，由本轮 role 渲染到 `/etc/kubernetes/ani/<组件>/verify.sh`，root 所有、0700。role 与现有 verify.sh 都调用这份目标脚本；不要复制出多套不同判据。启用组件的脚本缺失必须失败，不能跳过；只接受本次干净安装生成的文件，不从旧代码包/旧 runtime 复制脚本。
- 启用结果由 Go 在 `/var/lib/ani-installer/<name>/work/components-selection.tsv` 写入，root 所有、0600。格式为第一行 `# config_sha256=<本次站点配置SHA256>`，之后严格四行 TSV：`cert-manager`、`postgresql`、`valkey`、`nats`，每行第二列只允许 true/false。verify.sh 沿用现有 name 定位 runtime 的方式，核对传入配置摘要和文件格式，按固定 case 调用对应脚本；文件缺失、缺行、重复、未知值或配置摘要不一致必须失败，不能当成全部关闭。不要用 awk 自行解析嵌套站点 YAML，不 source/eval 元数据。
- verify.sh 保留既有网络/Envoy检查，并验证本次启用组件；未启用显示 skipped，不写成 PASS。普通 verify 不重启服务 Pod。实验持久化重建测试单列在 lab 脚本。

独立验证保持已有 root 要求：普通 ubuntu 用户执行 `sudo bash /opt/ani-installer/code/实际代码包/verify.sh 配置路径 物料目录`，无需 export KUBECONFIG。不要把验证器改为普通用户读取整个 root runtime 或自动输出所有 Secret。

## 5. 每批共同执行流程与失败处理

### 5.1 每批固定顺序

1. 在本地完成该批最小代码及针对性测试；在 Fedora 的本轮源码副本运行格式化、测试、渲染和构建。
2. 在 Fedora 准备、校验本批代码包及累计 artifact。记录源码快照摘要、代码包 ID/kk SHA256、artifact ID/SHA256、脱敏站点配置。所有持久材料必须先在 Fedora，快照会抹去节点副本。
3. 确认没有别的安装占用三台；持有实验锁后，调用现有快照脚本 dry-run，核对仅 `.20/.21/.22` 对应 `test-installer-01/02/03`，预期 VMID 5/6/7、snapshotId=1。映射不同、多快照或只匹配两台时停止核对，不能扩大白名单。
4. 执行快照还原，确认 3/3 成功、SSH 就绪、旧 admin.conf/runtime 不存在、三块 sdb 为空盘。不能仅凭脚本退出 0 就认定完整还原。
5. 用已有断网工具 apply/verify；需要看到三台公网域名与公网 IP HTTPS 失败、管理 SSH 正常、断网规则存在。不能用只断 DNS 的方法证明离线。
6. 向 `.20` 传输本批独立代码包、累计 artifact 和站点配置；检查实际路径/哈希。传输失败可以重传本轮文件，不属于安装重试；未开始安装不必为此重置 VM。
7. **以普通 ubuntu 用户、无需手动 export KUBECONFIG 的方式运行 install.sh**。保存真实退出码，不把 tee 的退出码当成安装成功。
8. 安装退出 0 后，执行独立 verify、该批功能/持久化测试、原底座回归和安装后断网复核。保存证据，更新批次状态后才进入下一批。

现场安装命令的形状（占位符必须换成本批已存在的绝对路径）：

```bash
# fedora 上连接目标；下列安装命令在 ubuntu@172.16.101.20 执行
set -o pipefail
code_root=/opt/ani-installer/code/REPLACE_CODE_ID
artifact_root=/opt/ani-installer/artifacts/REPLACE_ARTIFACT_ID
bash "$code_root/install.sh" \
  /opt/ani-installer/site/cluster.yaml \
  "$artifact_root" \
  2>&1 | tee "$HOME/ani-install-REPLACE_BATCH-REPLACE_ATTEMPT.log"
install_rc=${PIPESTATUS[0]}
printf 'INSTALL_EXIT=%s\n' "$install_rc"
```

自动化仍应实际经过该普通用户入口，不能所有实验都改成 root 直接运行 kk。使用已有私有凭据解决 sudo 输入；不把密码拼在 shell 命令中。产品不得要求用户自己处理 HOME/SUDO_USER 差异。

### 5.2 失败后自动还原的条件

**用户已授权本轮按需自动还原，不必每次申请。自动还原不是“失败就盲目循环重装”。**

| 情况 | 下一步 |
| --- | --- |
| 本地编辑或 Fedora 测试/构建失败，尚未改变测试机 | 修对应代码/输入；不还原测试机 |
| 安装/验收失败，确认属于 installer | 先收集证据；修源码并通过针对性测试、构建新代码/材料；再自动还原全部三台，从零安装 |
| 网络/实验准备错误 | 记录明确原因；修实验准备；节点已有安装修改则重新还原后开始 |
| 正确输入下组件 bug | 保留现场及交接记录，停止本批；等待组件维护方解决，不循环还原碰运气 |
| 尚不清楚原因、日志丢失、快照结果不明确 | 有限只读诊断；无法判定则报告，不清理现场掩盖问题 |
| 检查到别的任务/用户正在安装 | 停止，不抢锁、不还原、不杀进程 |

每次重试写清“上次失败 → 证据 → 责任方 → 本次具体改动”。**相同代码、物料、配置及环境没有变化，不得再次安装。**同一未解释原因连续重现两次就停止收集结论；有明确新根因和针对性修复时才继续，不设置不必要的人工批准流程。

快照/断网/传输/日志采集仅用现有工具或本轮 Fedora `lab/` 中的薄脚本；不打进发布代码包和 artifact，不从产品命令调用。每次重置前把关键日志保存到 Fedora，保留失败尝试，不能覆盖成一份 install.log。

## 6. B0：固定输入和物料，不重写架构

**只做：**核对 r12/r2、当前源码、上述远端文件；读取三台容量及既定网络/磁盘条件；获取四组件正式材料的元数据并确定每批需要的镜像；写固定物料锁和脱敏的批次配置。

需要产出：

- `docs/foundation-components-status.md`：每批状态 pending/in_progress/pass/fail/blocked、代码/物料 ID、日志路径；用户确认的 r12 验收标为 user_reported_pass。
- `kubekey/ani/components.lock.yaml`：四组件候选与来源、app/chart 独立版本字段；未下载的摘要不填假值。
- `config/examples/foundation-b1.yaml` 到 `foundation-b4.yaml`：分别累计启用本批及前批组件，密码使用占位符。真实实验配置在 Fedora 私有目录生成。
- 普通清单列出预计工作负载/PVC/内存/磁盘，不开发容量规划器。

发现源码与 r12 包不一致时，识别已有未提交改动并保留；不能 checkout/reset 到历史提交覆盖当前改动。只把明确的当前源码快照作为后续构建输入。

**完成标准：**材料和配置有确定来源，当前入口可继续复用，没有未解释的底座版本变化。B0 不需要再无意义重装一次已验收底座，也不要求先下载全套未来组件。

## 7. B1：最小组件开关 + cert-manager

只修改新增明确类型、默认值与 KubeKey 参数传递所需代码；给本批 role 加条件；扩展物料复制/校验以支持固定 Chart。不要提前实现 PostgreSQL/Valkey/NATS 的 role。

部署 cert-manager 官方 Chart：CRD 安装启用，等待 CRD Established、controller/webhook/cainjector 就绪，确认启动 hook 所需镜像也来自离线包。values 键以固定版本实际内容为准，不照抄别的版本。

已核对本版本字段 `crds.enabled: true`；顶层镜像前缀有 `imageRegistry`、`imageNamespace`。实际 controller、webhook、cainjector、startupapicheck 的四个镜像均为 `quay.io/jetstack/cert-manager-<名称>:v1.21.2`，B0 锁定摘要。values 还引用 acmesolver 镜像；本輪不启用 ACME，但默认控制器参数中的镜像引用也要检查并明确处理，不能只扫描已创建的 Pod。若保持该引用，则将同版本 acmesolver 一并纳入离线包，B0 核实其存在性与摘要。

创建本轮固定内部 CA 配方：SelfSigned Issuer 引导 CA Certificate，再用 CA Issuer 签发一个叶子证书。明确 `isCA`、SAN、有效期和 Secret 所在 namespace。这里只提供内部 CA 能力，不把所有数据库强制改为 TLS，也不引入公网 ACME。

建议根 CA Certificate 和根 Secret 在 `cert-manager`；用 `ClusterIssuer/ani-ca` 引用该 Secret，为测试 namespace 的 Certificate 签发。确认 controller 的 cluster-resource-namespace 与根 Secret namespace 一致。根 CA 有明确 subject/commonName；不做主机信任库分发、自动换根或外部 PKI 对接。证书验证工具也必须随包，B0 在 Fedora 检查其实际命令，不在目标机临时安装 openssl。

验收必须验证：实际签发的证书 Secret 存在，叶子证书 SAN 与请求一致，CA/叶子证书属性正确，证书链能够用随包工具验证。只看 controller Ready 或 Certificate Ready 不够。私钥不得进日志。

回归：旧配置/四开关默认 false 不创建新增资源；嵌套配置能正确传到 role；缺 Chart/镜像时明确失败。至少用执行/渲染结果验证，不只测试 YAML 中出现某个字符串。

**B1 PASS：**干净断网累计安装（底座+cert-manager）成功，内部 CA 签发/链验证通过，网络/Envoy/Ceph 原有验证不退化。

## 8. B2：PostgreSQL

保持 B1；只新增 PostgreSQL role、物料和验证。

- `ani-platform/postgresql` 单副本 StatefulSet，官方固定镜像，RBD PVC；明确 PGDATA 和挂载路径，按该镜像启动方式设置权限，不用 chmod 777。
- 使用官方支持的 `POSTGRES_PASSWORD_FILE` 读取 Secret，PGDATA 使用卷内子目录；不要把挂载根目录的 lost+found 当成数据库损坏，也不要为此清空整个卷。
- 管理员凭据及应用用户凭据分离。初始化创建应用用户 `ani_app` 和数据库 `ani`；应用用户不能成为超级用户。启动初始化材料只用于新空数据库，不开发业务迁移器。
- 使用独立客户端 Job，通过 Service DNS、应用用户凭据执行建表、插入唯一值、查询并精确比对；不得只在数据库容器里用本地 trust 连接证明认证成功。
- 明确未认证/错误密码连接被拒绝。工具使用 PostgreSQL 镜像自带 psql，不要求目标宿主机 apt 安装客户端。
- 实验脚本记录 StatefulSet Pod/PVC/Secret UID，正常删除唯一服务 Pod，等待新 Pod 就绪，通过同一 Service/用户读回原值；PVC/Secret UID 必须不变。

**B2 PASS：**底座+cert-manager+PostgreSQL 从零离线安装通过；远程认证连接、数据读写和正常 Pod 重建后的数据回读通过；B1 回归通过。不能把默认管理员连接成功当作应用用户权限验证。

## 9. B3：Valkey

保持 B1/B2；只新增 Valkey role、物料和验证。

- `ani-platform/valkey` 单副本 StatefulSet；独立认证 Secret；2Gi RBD 起步，启用 AOF、appendfsync everysec，挂载官方镜像所用的数据目录。
- 使用普通配置文件/Secret 引用，不把密码展开到 Pod 命令参数或日志中。明确镜像用户与 PVC 权限。
- 客户端 Job 经 Service DNS 使用认证执行 SET/GET，核对唯一值；未认证请求应失败；TTL 键确实过期。
- 实验中记录实际 AOF 持久化完成证据后，正常删除唯一 Pod，等待重建，读回非过期键，核对 PVC/Secret 不变；不要依赖固定 sleep 猜测持久化完成。
- AOF everysec 的测试只承诺正常 Pod 生命周期下的恢复，不宣称突然断电零丢失。

**B3 PASS：**累计组件从零离线安装成功；Valkey 实际认证/读写/过期/持久化恢复及此前组件回归通过。

## 10. B4：NATS JetStream

保持 B1～B3；只新增 NATS role、固定官方 Chart 物料及验证。

- `ani-platform/nats` 单实例。明确关闭 NATS cluster，打开 JetStream fileStore 和 RBD PVC，配置客户端认证。不得改用 memoryStore，不部署 NACK、Kafka 或 Pulsar。
- 本轮认证固定为一个随机 token Secret `ani-nats-auth`，不扩成 NKey/JWT/operator 或多租户账号系统。Chart 已核对支持如下 values（其余镜像重写以 B0 实际字段补全）：

```yaml
config:
  cluster:
    enabled: false
  jetstream:
    enabled: true
    fileStore:
      enabled: true
      pvc:
        enabled: true
        size: 5Gi
        storageClassName: ani-block
    memoryStore:
      enabled: false
  merge:
    authorization:
      token: "<< $TOKEN >>"
container:
  env:
    TOKEN:
      valueFrom:
        secretKeyRef:
          name: ani-nats-auth
          key: token
natsBox:
  enabled: false
```

`<< $TOKEN >>` 是该 Chart 的特殊渲染语法，会生成从环境读取的 NATS token 配置；不要替换成一个普通字面字符串，也不要编造 `auth.enabled`。`cluster.enabled: false` 时，不把其下默认 replicas=3 误认为实际三副本。默认 reloader 镜像 `natsio/nats-server-config-reloader:0.23.0` 要随包；客户端使用 `natsio/nats-box:0.19.7` 创建测试 Job，不保留常驻 natsBox。NATS CLI 的 JetStream 命令/JSON 输出在 Fedora 检查真实 `--help` 后冻结，不凭印象编写。

- 读取真实 Chart values，锁 server、reloader、nats-box/测试客户端等所有实际镜像。关闭不使用的功能，不因为开发机缓存存在就漏打镜像。
- 验证客户端通过 Service 和认证连接，创建本次独有的 file storage、replicas=1 stream 与 durable explicit-ack consumer。
- 发布两条带唯一 ID 的消息并得到 JetStream publish ack；消费并确认第一条，保留第二条尚未确认/未消费的状态。记录 server/stream/consumer 序列，不以普通 core NATS publish 成功代替持久化确认。
- 实验脚本正常删除唯一 server Pod，等待同一 PVC 上新 Pod 就绪，核对 stream、durable consumer、已确认状态，以及第二条消息仍可消费确认。控制过滤条件与等待上限，不把不同消费者重放当恢复成功。
- 验证错误凭据被拒绝。清理只操作本次唯一命名的 stream/consumer，失败时保留，不清空整个 JetStream 数据目录。

**B4 PASS：**四组件累计完整离线安装、NATS 消息/消费状态持久化、前批回归通过。失败不能通过关掉 JetStream 或换 memoryStore 缩小目标。

## 11. B5：最终交付与人工复现入口

如果 B4 已满足完整干净安装、普通用户入口、所有功能/持久化和断网复核，直接引用该轮证据，不为收尾重复重装。若最后又修改了运行代码/Chart/配置，按受影响范围补验证，不能把修改前结果标给修改后产物。

交付：

1. 当前本地源码修改和针对性测试；不自动提交、推送或发 release。
2. Fedora 上独立代码包、累计 artifact、SHA256SUMS、固定材料锁、源码快照对应关系。
3. 真实实验配置私有保存；仓库只放脱敏示例。
4. 一份连接说明；普通 ubuntu 用户可运行 kubectl，不需要手工修 kubeconfig。
5. 一份手动复现文档：还原→断网→传输→普通用户 install.sh→verify。只使用本次实际存在的路径，不能遗留 r11/r12 的旧代码包路径。
6. 批次证据表：reset 3/3、离线前后、安装退出码、版本/摘要、每组件真实操作结果、持久化前后资源 UID、已知问题和未验证项。

对每批填写：

```text
批次 / attempt：
源码快照 / 代码包 / kk SHA256 / artifact / 配置：
启用组件及 appVersion、chartVersion：
快照与断网证据：
安装退出码和日志：
功能验证：pass / fail / not_verified（逐项）
持久化验证：pass / fail / not_verified（逐项）
已知问题与责任方：
下一批是否可以开始：
```

不要把所有项目合成一个含糊“测试通过”。未启用、未执行、因环境限制未测的项目必须明确区分。

## 12. 可以直接发给执行 AI 的提示词

```text
请执行 /home/chabking/workspace/ani-installer/docs/foundation-components-batch-execution-plan-20260918.md。

先完整阅读该文件和 kubekey/AGENTS.md。本次只做 cert-manager、PostgreSQL、Valkey、NATS 四组件，按 B0→B1→B2→B3→B4→B5 顺序。一次只实现一个批次；该批真实验证通过后再进入下一批，不要一口气堆完全部代码。

本地只读写代码/文档和发起SSH传输。构建、格式化、测试、下载、制包及所有节点命令一律通过 ssh config host fedora。只允许测试集群 172.16.101.20/.21/.22；.20 承担 installer 和临时仓库。不要查看 ANI 业务代码，不碰 .10/.11/.12 或其它虚拟机。

用户已授权本轮按需调用现有 restore_esxi_snapshots.sh 自动还原三台快照，无需逐次申请。先核对目标、保存失败证据并确认无人占用。每批完整安装从干净快照开始；installer失败修源码/物料后再还原，组件bug保留现场交给组件维护方，不能无依据循环重装。

installer只负责物料、受支持配置、正常安装顺序、等待、验证、日志。禁止在kk/role/artifact加入ESXi、reset、清盘、删失败namespace/PVC、清OVN/LSP、强制detach或重启碰运气。不得改组件源码或临时造修复镜像，不得吞错误。实验Pod重建测试与快照操作只在lab流程，不变成产品恢复功能。

保留KubeKey和当前底座版本，保留APT与kubeconfig修复。kk和artifact继续独立发布，代码改动只重建小代码包。Chart/app版本分开锁定，所有部署及测试镜像均从离线包供应。不在目标机联网补依赖，不引入通用框架、不实现HA/增装/升级。

每批结束更新 docs/foundation-components-status.md 和 kubekey/docs/progress.md，列出实际产物/哈希、reset/断网/安装退出码、功能和持久化结果。不要打印或提交凭据。不要自动推送或发布。只有B5全部要求满足才能宣布本轮完成；遇到组件阻塞或未知原因重复出现，停止并给出明确证据，不能缩小范围假装完成。
```

## 13. 参考与历史关系

- 本轮基线：[kubeconfig 修复记录](kubeconfig-fix-20260918.md)、[APT 修复和离线验证](offline-apt-fix-validation-20260918.md)。其中“待用户验证”的旧状态由后续用户反馈补充，不应覆盖其历史事实。
- [旧最小首装方案](ani-installer-platform-iteration-execution-plan.md) 的“基础组件暂缓、每次重置请用户批准”已被本轮新范围和自动快照授权取代，其职责边界继续适用。
- [旧完整组件资料](archive/ani-installer-platform-iteration-plan-before-mvp-20260917.md) 仅供查材料，不是扩展本轮范围的依据。
- 官方材料来源：[PostgreSQL 官方镜像](https://hub.docker.com/_/postgres)、[Valkey 官方容器配方](https://github.com/valkey-io/valkey-container)、[NATS Chart](https://github.com/nats-io/k8s/tree/nats-2.14.6/helm/charts/nats)、[cert-manager Helm 安装](https://cert-manager.io/docs/installation/helm/)。最终具体镜像、Chart 校验和及验证状态写入 B0 固定材料锁。

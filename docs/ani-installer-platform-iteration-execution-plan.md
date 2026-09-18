# ani-installer 当前执行方案：职责边界与最小离线首装

> 2026-09-18：本文件保留为最小首装阶段的历史方案。用户已确认 r12 手动验证通过，当前新增任务以 [四组件分批执行方案](foundation-components-batch-execution-plan-20260918.md) 为准。新授权允许测试流程按需自动还原 `.20/.21/.22`，不再逐次请求；快照和失败清理不得加入产品安装逻辑。下文旧范围及“请用户还原”不覆盖新方案。

修订日期：2026-09-17。仓库：`/home/chabking/workspace/ani-installer`；Go 模块及 KubeKey fork：`kubekey/`。

**当前只完成最小离线首装。** 优先分开发布 kk 和 artifact；由一台目标节点运行 installer，在干净虚拟机上安装三节点 Kubernetes、kcn 和既定 Envoy，并通过真实流量验证。完成后停止，基础组件补全另行讨论。

本次用户修正是执行依据：

1. **kk 与 artifact 独立构建、独立校验、独立发布。代码修改不触发大物料包重做。**
2. **失败现场只保留证据，不就地修到成功。下一次完整安装前，请用户还原三台虚拟机快照。**
3. **installer 只解决自己的问题；待部署组件的缺陷交由组件修复，不能在 installer 内增加绕过或补丁。**

本文件是待实施方案，不表示拆包或接口调整已经实现。先核对已有工作，复用已经完成的部分，不重写一套安装器。旧的完整组件方案保存在 [历史资料](archive/ani-installer-platform-iteration-plan-before-mvp-20260917.md)，**其中 P2～P7 等扩展批次不再是当前任务**。

## 1. 职责边界：先判断谁的问题

installer 负责固定物料的分发、受支持参数的渲染、必要系统准备、安装顺序、明确的就绪等待、验收和日志。底座仍是 KubeKey v4.0.7 fork 和 Hauler，不添加新的部署引擎。

| 情况 | 归属与处理 |
| --- | --- |
| 包里漏了镜像/二进制，路径或镜像重写错误 | installer：修制包或路径处理 |
| 生成了错误 IP、参数、凭据引用、manifest 或 containerd 配置 | installer：修配置生成 |
| CRD 尚未建立就创建 CR、遗漏正常安装步骤、错误传递失败 | installer：修编排 |
| KubeKey connector/调用代码错误、验证访问错入口或读旧日志 | installer：修 fork 内相应实现 |
| 正确输入下 kcn/OVN 删除错网卡、网络对象或残留状态 | kcn/OVN：记录并交给组件修复 |
| 正确安装后 Envoy 控制器/数据面自身异常 | Envoy 对应组件：记录并交接 |
| 正确配置下 containerd/Kubernetes 自身的缺陷 | 对应组件：记录并交接，不修改其内部实现 |
| 网卡/IP/硬件条件不符合已确定的输入 | 报告具体现场差异，不擅自改方案 |
| 暂时无法判断归属 | 标记待定位，用只读证据继续判断，不先加兜底 |

归属判断至少核对实际版本/镜像、生成配置、安装步骤是否符合本轮固定输入和组件支持的部署方式。不能把所有报错都归咎于组件，也不能把所有组件报错接进 installer 修复。

### 1.1 installer 不得承担的工作

- 不修改 kcn、Envoy、Kubernetes、containerd 等组件源码或自制修复镜像。
- 不通过清理 OVN/LSP/数据库内部对象、周期重启组件、重写组件内部状态来掩盖缺陷。
- 不通过忽略退出码、跳过验收、无限重试或反复扩展超时把失败变成通过。
- 不为失败重装引入 kubeadm reset、残留扫描、自动清盘或“修复后继续”流程。
- 不因为某组件有 bug 就自行切换 CNI、WAL、镜像版本或组件实现。
- 不写通用兼容矩阵引擎、自动修复系统、插件框架、断点续装状态机。

正常安装所需的资源创建、官方参数、等待和已经约定的定制 Envoy 配置，属于 installer 职责。删除错误生成的逻辑、修正安装顺序也属于 installer；这与修复组件内部缺陷不同。

### 1.2 现有代码中需要优先检查的越界逻辑

核对 `builtin/core/roles/ani/envoy/tasks/main.yaml` 对 `cleanup-orphan-lsp.py` 的调用。**installer 不得借这个脚本修复 kcn/OVN 内部网络状态。** 从正常安装路径移除这类职责外修复；只为它服务的脚本和依赖应一并整理，不能挪到另一个脚本继续调用。

若移除后暴露了组件缺陷，记录为组件阻塞，由对应组件修复。不要为了恢复旧的“安装成功”记录而加回绕过逻辑，也不要顺便大规模清理其他未涉及代码。

### 1.3 组件问题的交接

保存一份简短记录：组件版本/镜像 digest、现场输入与生成配置、触发步骤、关键日志、预期/实际行为、已排除的 installer 输入错误。

将记录交给用户用于组件修复；本任务不自行发消息、创建外部 issue 或修改组件仓库。组件提供修正版或明确的受支持配置后，再更新对应物料/配置，从干净快照验证。组件问题未解决时，明确报告阻塞，不能宣称目标已完成。

## 2. 远端和现场范围

**本地只阅读、编辑代码/文档和发起 SSH 文件传输。** 构建、格式化、测试、下载、制包、运行二进制及所有目标机命令，均通过 SSH config host **`fedora`** 执行，不换成其他构建机，不在本地兜底运行。

| 项目 | 当前固定输入 |
| --- | --- |
| 构建/调度 | `ssh fedora` |
| installer / 节点 1 | `172.16.101.20` |
| 节点 2 / 节点 3 | `172.16.101.21` / `172.16.101.22` |
| SSH 用户 | `ubuntu` |
| SSH 密码，仅本地执行文档临时保存 | `zhu1241jie` |
| 系统 | Ubuntu 24.04 Server，非精简，amd64；记录实际默认内核 |
| 管理网络 | ens34，`172.16.101.0/24`，已经配置 |
| kcn 管理接口 | ens35，无主机 IP |
| kcn encapNetworks | `172.16.101.0/24` |
| Pod / Service CIDR | `10.16.0.0/16` / `10.96.0.0/16` |
| 暂不使用 | ens36、Ceph 数据盘 /dev/sdb |

凭据按用户要求暂存，提交前脱敏。不写入源码、artifact、镜像或普通日志；现场配置权限 0600。本任务不提交、不推送、不发布。

所有目标操作都由 fedora 再访问 .20～.22。Hauler 和 installer 运行在 .20 宿主机；fedora 不成为目标集群的公网代理或运行依赖。不得访问 .10～.12 的已有平台或检查 ANI 源码。

“已经还原 ISO”是此前现场信息，后续执行先核对当前状态，不据此假定仍为空白环境。用户负责操作虚拟机快照；执行 AI 不操作未授权的虚拟化宿主机。

## 3. 只保留一套最小基线

| 项目 | 本轮使用 |
| --- | --- |
| 安装底座 | 当前 KubeKey v4.0.7 fork |
| Kubernetes | v1.35.8 |
| containerd / runc | v2.3.4 / v1.4.3 |
| etcd / pause | v3.6.6 / 3.10.1 |
| Hauler | v2.0.3 |
| kcn | kc-networking:v0.6.2，现有固定镜像和安装材料 |
| 定制 Envoy | gateway:v1.8.3、envoy:distroless-v1.38.3，现有配套配置和固定 sidecar digest |
| 验证镜像 | 复用当前已锁定 BusyBox 等基础验证镜像 |

containerd/runc 是此前同意的基线，不退回旧 1.7.13/1.1.12，也不趁机升级其余版本。只实现这套 Ubuntu/Kubernetes/运行时配对，不新增兼容多个系统、多个运行时版本的分支。

kcn 和定制 Envoy 的镜像、原定功能配置保持固定；离线镜像地址重写可以做，组件功能修复不在这里做。`gateway-dev:latest` 使用已有记录的 digest，不能重新解析浮动 tag 换入未知内容。

Ceph、PostgreSQL、Valkey、NATS、监控日志、Milvus、KubeVirt/CDI、Volcano、LWS、Harbor、GPU、HA、仓库交接、三网自动配置等都放到下一轮。本轮不为了准备这些组件而下载大量物料或扩展配置结构。

## 4. kk 和 artifact 分开交付

### 4.1 两个独立发布物

**代码发布物：** kk 二进制及少量同版本验证脚本。KubeKey roles/playbooks/项目模板继续内嵌 kk。现有共同 smoke 脚本由同一源文件生成/复制，不维护两套验证实现。

**物料发布物：** 系统离线依赖、Kubernetes/containerd/runc 二进制、镜像归档、Hauler 等固定第三方工具、镜像/版本清单及相应许可证。里面不能出现 kk、本项目可变安装脚本、角色模板或验证逻辑。

示意目录：

```text
/opt/ani-installer/
  code/
    <code-id>/
      kk
      verify.sh
      probe.sh
      SHA256SUMS
  artifacts/
    <artifact-id>/
      bin/hauler
      packages/kubekey-artifact.tgz
      images/images.haul.tar.zst
      images/images.tsv
      config/package.yaml
      config/versions.yaml
      licenses/
      SHA256SUMS
  site/cluster.yaml
```

代码发布物很小，可单独传输；只修改验证脚本时也只替换代码发布物。artifact 独立压缩、独立保存和复用，禁止再把两者套进必须整体更新的大包。

只需分别记录 code-id/源码版本、kk 与脚本校验、artifact-id/物料校验及实际测试组合。不增加复杂的自动兼容协商，也不要求 code-id 必须等于 artifact-id。

### 4.2 最少的接口调整

优先保留当前已存在的参数，避免为拆包重建 CLI：

```bash
sudo /opt/ani-installer/code/<code-id>/kk ani install \
  --config /opt/ani-installer/site/cluster.yaml \
  --package-root /opt/ani-installer/artifacts/<artifact-id>

sudo /opt/ani-installer/code/<code-id>/verify.sh \
  /opt/ani-installer/site/cluster.yaml \
  /opt/ani-installer/artifacts/<artifact-id>
```

其中 `--package-root` 本轮就是物料目录。verify.sh 增加明确的物料目录参数，从同版本代码目录调用 probe.sh。不要自动在多个目录寻找旧 kk，也不要保留 `$ROOT/bin/kk` 的隐式绑定。若保留 install.sh，只能薄包装调用显式指定的独立 kk，不放进 artifact。

本轮不增加 components 子命令、--only 过滤、任意阶段重试或多种 archive/目录自动识别接口。用户给定一个已经解压的 artifact 目录即可。

运行日志、生成配置、凭据和 Hauler 工作数据放在独立运行目录，如 `/var/lib/ani-installer/<cluster>/`；不修改 artifact 的静态内容。只调整现有路径拼接，不建设新的缓存/恢复框架。

### 4.3 拆开构建过程

1. `make kk` 只编译 kk 和内嵌资源。测试、格式化、编译都在 fedora。
2. `scripts/build-offline.sh` 接受已经构建好的 kk 路径，必要时调用它导出 KubeKey artifact；**脚本内部不再执行 make，也不把 kk 复制到物料输出**。
3. 移除制包脚本中复制项目 install/verify、`manifests/ani` 等执行逻辑的步骤。固定第三方材料仍按目录保存。
4. 现有 artifact 若确实包含本轮版本，直接复用。若仍是旧 containerd/runc，只更新一次相应物料；镜像集合没变，不重复收集镜像。
5. 构建依赖使用 fedora 的现有缓存，不每次清空 Go 缓存或重新下载。必要 Ubuntu 制包操作在 fedora 的 Ubuntu 24 环境执行，不把 Fedora RPM 安装到目标机。

| 本次改动 | 应做的工作 |
| --- | --- |
| Go 逻辑、角色、模板、命令顺序变化，物料未变 | 重编 kk，传代码发布物，复用原 artifact |
| verify/probe 逻辑变化 | 更新小型代码发布物，复用 artifact |
| 现场 cluster.yaml 填写修正 | 更新现场文件，不制包 |
| 增加镜像或改变依赖二进制版本 | 更新受影响物料，生成新 artifact；不重复下载未变化内容 |
| 组件内部 bug | 移交组件；得到明确修正版后再更新对应物料 |

## 5. 本轮代码工作清单

按这个顺序实施，已完成的项目只核对，不重复重写：

1. **拆开代码与物料交付。** 修改 build-offline.sh、必要的入口和路径处理，证明 artifact 不含 kk，外置 kk 可读取已有物料。
2. **移除职责外修复。** 按第 1 节处理 installer 内对组件内部状态的修补；不增加兼容半安装环境的逻辑。
3. **同步固定运行时。** ANI 配置生成、package.yaml、K8s 1.35 的下载默认值与测试统一为 containerd 2.3.4/runc 1.4.3。只有物料确实改变才制包。
4. **生成本基线支持的配置。** 对照 containerd 2.3.4 固定格式生成 runtime/cgroup/pause/CNI/HTTP registry 配置，镜像全部指向 .20 的离线供应。只处理这一基线；错误输入或漏物料直接说明，不做在线兜底。
5. **完成最少的可靠验收。** 复用原收尾方案的失败退出、Envoy 入口精确选择和每次新建客户端；验证逻辑随代码发布物更新。
6. **只跑必要测试。** 在 fedora 执行相关 Go 包/入口测试、脚本语法和 make kk；不跑全上游矩阵，不为未纳入的组件写代码或测试。

重点源码位置：`scripts/build-offline.sh`、`scripts/install.sh`、`scripts/verify.sh`、`pkg/ani/runner.go`、`pkg/ani/config.go`、相关测试，以及当前 ani roles。KubeKey 原生 roles/connectors 继续复用，不手写另一套 kubeadm 安装链。

## 6. 真实安装怎么跑

1. 在 fedora 完成代码测试和独立发布物。记录本次 kk/脚本校验及 artifact 校验；代码修复不得调用物料制包流水线。
2. 确认用户已将三节点还原到约定的安装前快照。只核对必要事实：管理 SSH/提权可用、节点身份正确、没有已有 Kubernetes/运行时安装状态。不要给用户几十项手工预检。
3. 经 fedora 分别传输代码发布物和 artifact。artifact 已在该干净快照中且确认一致时无需重复传输；缺失时复制 fedora 保留的同一份 artifact，不重新制作。
4. 在首装之前验证三节点无公网，保留管理 SSH和集群必需网络。沿用本实验已确定的隔离方式，不删除默认路由、不全局清空防火墙、不将 fedora 配成公网下载代理。不要新增网络隔离产品。
5. 从 .20 执行第 4 节的正式命令。installer 使用原生安装步骤一次拉起集群，Hauler 由 .20 宿主机供应镜像。不能在目标机在线 APT/pip/curl 补材料。
6. 干净环境第一次安装需要导入镜像，这是正常首装步骤。**复用 artifact 省掉的是重新制作和反复压缩发布，不是把半安装现场保留为重试起点。**
7. 命令和日志保留在 .20 并回收到 fedora。长任务保存 PID/退出码或使用现有 systemd 运行能力；SSH 断开时先查看原进程，不能重复启动安装。不要为此另建恢复引擎。

可选的提速方式：由用户制作“仅完成 ISO/管理网/SSH，并放好 artifact 文件”的安装前快照。里面不能带已安装 Kubernetes、containerd、已启动 Hauler 或已导入的运行状态。是否采用由用户决定，本方案不擅自操作快照。

## 7. 一旦失败，按这个流程处理

**首装或正式验收失败，本轮结果就是 fail。正常、有截止时间的就绪等待可以继续；已经明确失败后，不能继续对现场做补装、修复和反复重试。**

1. 停止后续安装变更。保存原命令、退出码、版本、配置、关键日志和只读状态到 fedora；不要先清理现场。
2. 按第 1 节定位归属。只读 describe/logs/状态查询可以做；不要用现场改配置、重启组件或删除对象来证明“补一下就好”。
3. installer 自身问题：本地修源码，在 fedora 测试并生成新 kk。物料没有变化就继续复用旧 artifact。
4. 组件问题：输出交接记录，等待组件修正版或明确受支持配置。原因未解决时，不对同一组合反复重装，也不在 installer 中添加修复脚本。
5. 下一次安装所需代码/物料准备好后，**请用户还原 .20、.21、.22 的安装前快照并明确告知完成**。请求里说明本次失败原因、归属、已经准备好的修正版和日志位置。
6. 等待还原期间可以继续源码、测试和文档工作；不得用时间经过代替用户确认。用户确认后核对必要的干净状态，用正式入口从头安装。
7. 若修正后仍失败，重复以上流程。不得 kubeadm reset、删除集群、擦除运行目录/etcd/磁盘以冒充快照还原。

还原请求示例：

> 本次首装失败，原因是【具体错误】，归属【installer/组件】。日志已保存到【fedora 路径】，修正版【kk 版本或组件物料版本】已准备好。请将 172.16.101.20～22 还原到约定的安装前快照，并告知完成；确认后我从正式入口重新安装。

组件修正版尚未准备好时，先报告组件阻塞，不急于请求无意义的快照还原。

## 8. 最小验收与完成条件

| 项目 | 通过依据 |
| --- | --- |
| 发布物分离 | artifact 不含 kk/本项目执行逻辑；代码发布物可独立校验和传输 |
| 修改代码的反馈路径 | 替换 kk/验证脚本时复用同一 artifact；日志中没有再次执行 artifact export、镜像收集、归档压缩和大包发布 |
| 正式首装 | 三节点从确认的干净快照出发，.20 上正式入口退出 0，无临时手工修补 |
| 离线 | 引导前已无公网；安装材料来自本次固定 artifact，未由 fedora 代理补下载 |
| 运行时 | 三节点实际 containerd/runc 版本符合基线，CRI 拉取和 Pod 启动成功 |
| 基础网络 | 新客户端跨节点访问 Pod IP、Service IP、DNS，内容正确且失败会非零退出 |
| 定制 Envoy | 精确选中该 Gateway 的 Envoy Service/端口，真实 HTTP 得到预期内容；不能直连 backend 代替 |
| 证据新鲜 | 保存本次客户端 UID/日志；再执行一次 verify 创建新的客户端，不读取历史 Succeeded Pod |
| 职责边界 | 没有依赖 OVN 状态清理、组件补丁、失败后现场修复等越界手段通过验收 |

保留 `pass/fail/not_verified` 和实际测试的 kk + artifact 组合。源码测试通过不等于集群安装通过，阶段补丁后能运行也不等于干净首装通过。

**一套最终 kk + artifact 在干净快照完整安装并通过上述验收，本轮即完成。** 不再追加另一轮组件安装、HA、存储或性能测试；没有新改动或失败依据，不要求反复重装。

最终交付：源码改动、独立 kk/脚本及校验、可复用 artifact 的位置/校验、本次首装和流量验证结果、真实阻塞及归属。更新已有 progress 文档即可，不建设额外证据平台，不提交或推送。

## 9. 可直接发送给执行 AI 的指令

```text
执行 ani-installer/docs/ani-installer-platform-iteration-execution-plan.md。

当前只完成最小离线首装：Ubuntu24 amd64，Kubernetes1.35.8，containerd2.3.4/runc1.4.3，既定kcn和定制Envoy。旧文档中Ceph、数据库、监控、虚拟化、Harbor等完整组件迭代已暂停，不得继续扩展范围。

第一优先级是职责边界：installer只负责分发、配置、编排和验证。先核对输入和安装步骤，再判断错误归属；installer错误修源码，组件内部bug交给组件解决。不得清理OVN/LSP、反复重启组件、忽略错误或自制补丁镜像来让安装通过。处理现有越界逻辑，不增加兼容半安装现场的代码。

先拆开kk与artifact。kk和少量配套验证脚本独立发布；artifact只装固定依赖和镜像。代码变化只重编/传输代码发布物，复用原artifact；物料版本或内容改变才更新相应物料。不在每次测试时重新制作和传输大包，也不引入复杂兼容/缓存框架。

本地仅编辑代码文档及SSH文件传输。所有构建、测试、下载、制包、运行和目标机命令必须通过SSH config host fedora。唯一目标是172.16.101.20～22，.20运行installer和Hauler。账号等在方案第2节。不得访问.10～.12，不看ANI源码，不提交或推送。

安装/正式验收一旦失败，只收集只读证据并停止后续变更。修好installer代码或拿到组件修正版后，请用户还原三台安装前快照；明确确认后再从正式入口完整安装。禁止现场补装、kubeadm reset、自动删集群或清残留来替代快照。组件bug未解决时报告阻塞，不在installer内绕过。

完成一套固定kk+artifact的干净离线首装及真实网络/Envoy验收后停止。诚实记录pass/fail/not_verified，不用历史成功、手工修复或缓存日志代替本轮结果。
```

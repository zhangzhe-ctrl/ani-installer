# ANI installer 代码详细审查与整改建议

日期：2026-09-24。审查对象是已上传 `ani-installer.zip` 的源代码快照，不是实际集群，也不是上一轮提出的尚未落实的组件/版本规划。

## 结论

保留 KubeKey fork、Go 配置入口、固定 role 编排和 `kk` / artifact 分离；不建议推倒重写。

当前主要问题不是“还缺多少组件”，而是：组件故障绕过进入安装验收、验收结果有假阳性、配置与材料验证晚于应有时点、局部测试未成为发布必经门禁。它们把本可局部发现的错误放大为多轮全量集群重装。先收敛这些问题，再增加 Milvus/KubeVirt/Kubeflow，可减少重复调试成本。

这里 P0 表示安全/数据授权/执行边界必须优先处理；P1 表示正确性与开发效率阻断；P2 表示生命周期补齐。不对应第三方 CVSS 评分，也不表示现场已经发生对应事故。

## 审查依据与范围

- 先复核离线对话的约束，再检查当前源代码；聊天中原来的“组件 bug 交组件修、失败停止变更、禁止清理补丁、代码与物料独立发布”仍作为边界。
- `kcn` 与其专属 Envoy 保持原有专属流程；Kube-OVN 通用网络验证不得复用该 Envoy。未来业务 Envoy 独立安装、暂缓实施。
- 重点覆盖 `kubekey/pkg/ani`、ANI CLI、制包/安装/验证脚本、主 playbook、ANI roles/模板、影响上述路径的连接器与命令执行、测试/CI 和实验记录。不是对全部上游 KubeKey 每一行实现的穷尽审计。
- 没有运行 SSH、操作 ESXi、安装集群、删除业务 Pod 或修改原仓库；复现仅使用临时目录、source copy 和 mock 命令。
- 源码 HEAD：`c7a97bb508b699d5117db625da2fa4a8155b838f`。
- ZIP SHA256：`2a4c9cc54becab39bb1c5c9f204ecb567ac9b0e90336dfa13a625994308269b3`。
- 所有路径相对于上传仓库根目录 `ani-installer/`。行号对应此快照。

## 整改项总览

| 编号 | 优先级 | 问题 |
|---|---|---|
| A01 | P0 | 验收脚本越界修复 kcn，失败后仍继续变更 |
| A02 | P0 | Ceph 隐式启用、磁盘选择和默认 StorageClass 固化为实验环境 |
| A03 | P0 | 受 Git 跟踪的实验脚本含明文基础设施密码 |
| A04 | P1 | Kube-OVN 跳过网络探测仍输出网络通过 |
| A05 | P1 | Kube-OVN CIDR/网关配置不一致，实验网络参数进入产品模板 |
| A06 | P1 | 多命令 task 可吞掉第一条失败，未落实失败即停止 |
| A07 | P1 | 物料锁未与实际内容闭合，SHA256SUMS 只能证明打包后的自洽 |
| A08 | P1 | 所选 CNI/组件的离线依赖未完整预检，缺模板键不报错 |
| A09 | P1 | installerNode 可不是第一个节点，但角色假设本地材料在第一个控制面 |
| A10 | P1 | verify.sh 用 awk 另解析 YAML，合法引号可绕过网络验证 |
| A11 | P1 | 预检失败也占用运行目录并要求还原快照，增加无谓失败成本 |
| A12 | P1 | SSH 连接器丢弃 context，等待上限不能可靠约束远程命令 |
| A13 | P1 | CI 未落在当前仓库入口，构建不跑验收门禁，测试语义不足 |
| A14 | P1 | 安装与高成本破坏性验收捆绑，并在顶层 verify 再执行 |
| A15 | P2 | 自举仓库的重启与长期镜像来源边界不明确 |

## 详细发现与验收标准

### A01 · P0 · 验收脚本越界修复 kcn，失败后仍继续变更

**位置：** `kubekey/builtin/core/roles/ani/metrics/templates/verify.sh:102–152`；`kubekey/builtin/core/roles/ani/fluent-bit/templates/verify.sh:434–507`；`kubekey/builtin/core/roles/ani/fluent-bit/templates/verify.sh:603–627`；`kubekey/builtin/core/roles/ani/metrics/tasks/main.yaml:94–118`；`kubekey/scripts/verify.sh:133–153`。

**代码事实：** metrics 的 k5_rebuild_wait 在组件重建失败后继续删除业务 Pod，随后 k5_dp_agent_restart 直接删除 kcn-system 下 cni/ovs Pod。代理等待失败只打印 continuing，再重建业务 Pod。fluent-bit 有同类逻辑。它不是仅供人工执行的 lab 实验：metrics role 在安装过程中执行该 verify.sh；顶层 verify.sh 又会执行它。顶层脚本还在一个组件验证失败后 continue，后续脚本仍可能创建、删除和修改资源。

**影响/证据边界：** 组件验收悄悄改变底层网络，破坏故障现场，可能影响其他工作负载，并且将“人工干预后暂时恢复”混同于本次组件验收成功。Kube-OVN 路径也不应夹带任何 kcn-specific 恢复逻辑。

**具体整改：** 从交付的安装/验收链删除 k5_rebuild_wait、k5_read_again 中的修复式重建以及 k5_dp_agent_restart 等越界逻辑。正常的“计划内重建一个已标识 Pod 以验证持久化”保留在专项验收中，但失败立即收集只读证据并退出。顶层失败后剩余组件记 not_run；允许只读诊断继续，禁止执行其他变更型验收。需要研究 K-5 时另置 lab 场景，显式授权、独立结果，不能被 install/verify 自动调用。

**完成标准：** mock kubectl 返回组件不 Ready 时，脚本非零退出；命令记录中不得出现删除/重启 kcn-system、OVS、CNI 的动作；第一个失败后的组件脚本未被调用。专项持久化测试可以发生一次计划内重建，但不能失败后追加“修复性”重建。

### A02 · P0 · Ceph 隐式启用、磁盘选择和默认 StorageClass 固化为实验环境

**位置：** `kubekey/builtin/core/playbooks/create_cluster.yaml:113–125`；`kubekey/builtin/core/roles/ani/ceph/templates/cluster.yaml:34–69`；`kubekey/builtin/core/roles/ani/ceph/tasks/main.yaml:128–137`。

**代码事实：** full profile 无条件执行 storageclass 和 ani/ceph，与其他组件开关无关。CephCluster 使用 useAllNodes: true 和 deviceFilter: ^sdb$，注释假定每台机器有一块空白 50G 数据盘。随后将 ani-block 设为默认，并遍历所有其他 StorageClass 撤销默认标记。

**影响/证据边界：** 这在当前约定的三台实验机上有设计背景，不代表 Rook 会无条件擦除已有文件系统，也没有证据表明发生过数据损坏。但它不是通用可选存储契约：更换现场可能占用未授权空盘，选择外部 StorageClass 仍然部署 Ceph，并更改其他存储默认行为。

**具体整改：** 增加最小 storage.enabled/provider 与每节点 device allowlist；当前实验值移动到 lab 示例，不写死在产品模板。创建 CephCluster 前仅做只读磁盘身份、根盘/挂载/签名/PV 使用检查，未明确授权即拒绝，不添加清盘逻辑。需要默认 StorageClass 时显式配置；默认不修改其他类的 annotation。对于本期不打算扩展的拓扑，明确拒绝不支持场景，不必自研通用存储编排。

**完成标准：** 关闭 Ceph 时计划中无 Ceph CR/磁盘操作；未提供授权盘时 preflight 失败；检测到挂载/系统盘/签名则停止；已有默认 StorageClass 不被静默修改。

### A03 · P0 · 受 Git 跟踪的实验脚本含明文基础设施密码

**位置：** `restore_esxi_snapshots.sh:36–36`。

**代码事实：** restore_esxi_snapshots.sh 第 36 行将 ESXI_PASS 写成明文字面量，文件受当前仓库 Git 跟踪。本报告和复现包不会复述或携带密码。

**影响/证据边界：** 这是凭据管理风险；不能据此断言外部泄漏已经发生。仅把现在的脚本改成环境变量，不能撤销旧文件/仓库历史中已经暴露的凭据。

**具体整改：** 先更换相关凭据并核对可访问范围。脚本改成必填环境变量/受保护凭据文件，不保留默认密码，不将密码写入发布日志。扫描当前树、历史、实验配置和已发布包；需要重写历史时由仓库所有者确认协作方式后单独实施。快照脚本继续只属于 lab，不进入 installer 的失败恢复路径。

**完成标准：** 源码和交付物凭据扫描通过；缺失凭据时脚本在连接前报错；测试/日志不回显密码；旧凭据已由负责人完成撤销或轮换记录。

### A04 · P1 · Kube-OVN 跳过网络探测仍输出网络通过

**位置：** `kubekey/scripts/verify.sh:56–78`；`kubekey/scripts/verify.sh:156–161`；`kubekey/builtin/core/roles/ani/kubeovn/tasks/main.yaml:35–51`。

**代码事实：** 顶层脚本仅在 NETWORK_STACK=kcn 时调用 probe.sh。Kube-OVN 分支没有执行对应跨节点/Service/DNS 网络探测，却在最后打印 network=ANI-NETWORK-OK (kube-ovn)。安装 role 的 rollout 就绪检查不能补上此处未执行的网络探测。

**影响/证据边界：** “通过”报告超出实际执行的检查范围。节点和控制器 Ready 时，Service、DNS 或跨节点流量仍可能未通过验收；本次未连接集群，未声称现场网络实际不通。

**具体整改：** 将通用 CNI 网络探测与 kcn 专属 Envoy 探测分开：前者验证跨节点 Pod、ClusterIP 和 DNS，两个主 CNI 均执行；后者只由 kcn 专属流程执行。输出 pass/fail/skipped/not_run 与具体证据，禁止没有执行测试就写 OK。不把 kcn 的 Envoy 改成通用业务网关。

**完成标准：** 在 mock/隔离测试中保持节点 Ready、让 DNS 或 Service 请求失败，最终必须失败；选择 Kube-OVN 时任何命令均不等待、安装或调用 kcn 专属 Envoy。

### A05 · P1 · Kube-OVN CIDR/网关配置不一致，实验网络参数进入产品模板

**位置：** `kubekey/builtin/core/roles/ani/kubeovn/templates/kubeovn-install.yaml:7898–7921`；`kubekey/pkg/ani/config.go:519–556`；`kubekey/pkg/ani/config.go:919–923`。

**代码事实：** default-cidr 接配置，但 default-gateway 固定 10.16.0.1；node-switch-cidr 固定 172.19.0.0/16，旁边注释说明为实验离线隔离规则调整。Validate 只做 CIDR 语法检查，没有网段重叠、地址族一致性校验；节点地址接受 IPv6，却写入 internal_ipv4。

**影响/证据边界：** 局部执行真实 Kube-OVN 模板，把 Pod CIDR 配为 10.244.0.0/16，网关仍是 10.16.0.1，不属于该子网。当前默认值可能不触发，但更换合法现场配置会失败。

**具体整改：** 网关从 Pod CIDR 推导，或显式字段并验证属于该段；join CIDR 配置化并检查与 Pod/Service/管理地址不冲突。实验防火墙规则从实际网络配置生成，不倒过来改产品地址段以适配实验规则。当前仅承诺 IPv4 就在 Validate 明确拒绝 IPv6，不扩展本期范围。enable-lb-svc=false 本身不是 bug；LB、Multus 单独按已选能力接入，不能由基本网络修复隐式打开。

**完成标准：** 测试默认 CIDR、非默认合法 CIDR、错误网关、重叠网段、地址族不一致；非法组合应在任何远程变更前失败。实际离线验收分别验证主网络与可选 LB/附加网络。

### A06 · P1 · 多命令 task 可吞掉第一条失败，未落实失败即停止

**位置：** `kubekey/builtin/core/roles/ani/ceph/tasks/main.yaml:28–40`；`kubekey/pkg/modules/command/command.go:75–82`；`kubekey/pkg/connector/local_connector.go:125–139`；`kubekey/pkg/connector/ssh_connector.go:311–324`。

**代码事实：** Ceph 的 Apply CRDs/common/csi-operator 是一个连续三条 kubectl 的 command 块，没有 set -e 或 &&。Command 模块将整个字符串交给 shell -c；连接器没有自动为此块增加 errexit。

**影响/证据边界：** 安全 mock 复现：第一条 apply 返回 42，后两条返回 0，整个 task 返回 0。后续 CRD wait 可能再失败，但初始错误已被掩盖，且发生了后续变更；不等于整个安装一定会最终成功。

**具体整改：** 逐一审核 ANI roles 的多命令块，明确以 Bash 执行并设置 set -euo pipefail，或拆成单命令 task/显式 &&。对预期 NotFound 等可接受错误进行精确处理，不能全盘 || true。不要直接改变上游所有 shell task 语义，以免引入新的兼容面。

**完成标准：** 第一/中间/最后一条命令分别注入失败都使 task 非零退出，且失败后的写操作不会执行；管道前段失败同样被捕获。

### A07 · P1 · 物料锁未与实际内容闭合，SHA256SUMS 只能证明打包后的自洽

**位置：** `kubekey/scripts/build-offline.sh:112–160`；`kubekey/scripts/build-offline.sh:169–187`；`kubekey/scripts/build-offline.sh:209–214`；`kubekey/pkg/ani/images.go:24–68`；`kubekey/pkg/ani/runner.go:487–510`。

**代码事实：** 制包循环读取 actual_digest，却按 original tag 拉取；默认路径只对比镜像数量。传入 HAULER_ARCHIVE 时仅复制。Chart 注释写“仅收录锁内项目”，代码却复制目录中所有 tgz。预置 runtime/ISO/组件锁被复制但未对照实际输入验证。运行时镜像检查仅判断 manifest HTTP 200，不比对 Digest。CONFIG 可覆盖导出输入，第 183 行却始终复制默认 package.yaml。

**影响/证据边界：** 局部隔离制包使用故意无效的归档和未列入锁的 Chart，脚本仍成功并生成通过 sha256sum --check 的目录；这是错误材料被重新生成自洽校验和，不是材料符合预先批准的版本锁，更不是部署通过。原 images.go 也接受非 digest 字符串与两个原始镜像映射到同一 local ref。

**具体整改：** 做一个可由制包和安装预检共用的轻量物料校验入口：解析实际锁；核验二进制/ISO/Chart SHA；验证归档结构和选定能力的镜像集合；拉取使用 digest 或明确比较解析结果；本地 registry 逐个比对对应 amd64 manifest 摘要。多架构 index 摘要和单架构 manifest 摘要分字段，不能盲目相等比较。CONFIG 元数据复制实际使用的配置，同时记录其 hash；上游 tag 只保留为人类可读来源。

**完成标准：** 错误 Chart、正确数量但错误镜像、过期 tag、损坏 archive、未声明 Chart、local ref 冲突、工具/ISO 校验失败全部在制包/远程变更前拦住。自校验和正确不能覆盖源锁验证失败。

### A08 · P1 · 所选 CNI/组件的离线依赖未完整预检，缺模板键不报错

**位置：** `kubekey/pkg/converter/tmpl/template.go:45–64`；`kubekey/pkg/ani/config.go:411–439`；`kubekey/pkg/ani/images.go:146–169`；`kubekey/pkg/ani/runner.go:127–161`。

**代码事实：** 模板大量用 index .ani.images 直接取镜像；通用模板执行没有必需镜像断言。componentImageKeysForRun 只按 metrics/logs 分组，logging 开启时同时要求 Loki 和 OpenSearch 的拆分镜像，即使只选一个后端。主 CNI 镜像没有同等的选定能力必需集合检查。required 文件列表也没有按 Chart 使用需求检查 bin/helm。

**影响/证据边界：** 在真实 Kube-OVN 模板的最小同字段上下文中，空镜像 map 仍能渲染成功并产生空 image 字段；甚至仅加 missingkey=error 也不能让内置 index 自动变成必需项检查。此复现不是完整 KubeKey render，实际值类型不同可能表现为 <no value>，共同问题是缺项没有立即报错。

**具体整改：** 为两个网络栈、各组件及其验证工具声明最小 required image/material keys；在创建运行占位和 kubeadm 前执行。提供 requiredImage(key) 之类能返回 error 的模板函数，或严格的渲染前/后语义检查。logging 依赖按 backend 过滤。外层模板、shell heredoc 里的 YAML 和验证 Pod 镜像都纳入检查。Helm 文件与可执行性按选中 Chart 检查。

**完成标准：** kubeovn + kcn-only artifact 立刻失败；缺任意运行/验证工具镜像立刻失败；Loki-only 不因缺 OpenSearch 镜像失败；未选择组件不强制拉取其大镜像。

### A09 · P1 · installerNode 可不是第一个节点，但角色假设本地材料在第一个控制面

**位置：** `kubekey/pkg/ani/config.go:506–518`；`kubekey/pkg/ani/config.go:893–937`；`kubekey/builtin/core/playbooks/create_cluster.yaml:95–108`；`kubekey/builtin/core/roles/ani/metrics/tasks/main.yaml:22–48`；`kubekey/builtin/core/roles/ani/metrics/tasks/main.yaml:111–118`；`kubekey/pkg/ani/runner.go:269–300`。

**代码事实：** Validate 仅要求 installerNode 是节点成员。inventory 按原 nodes 顺序构建控制面组，只有 installerNode 使用 local connector。ANI roles 固定在 kube_control_plane[0]，又直接使用 .ani.artifact_root 和写连接信息到该机器本地；RunInstall 最终在 installer 进程本地读取连接片段。

**影响/证据边界：** 静态路径推导：当 installerNode=node2，而 nodes[0]=node1 时，role 的 Helm/Chart 路径在 node1，材料却准备在 node2；连接片段也可能留在另一台节点。本轮未实装该拓扑，因此属于有明确触发配置的代码路径缺陷，不声称已观察现场失败。

**具体整改：** 最小修复先明确当前契约：要求 installerNode 必须是 nodes[0]，前置拒绝其余配置；如需要任意安装节点，再引入明确 ani_installer 执行组/连接片段 fetch，而不是隐式复制完整大包到所有机器。不要只在文档里口头规定。

**完成标准：** nodes[0] 与 installerNode 不同时，在材料导入和远程操作前明确报错；支持扩展方案时还需证明文件读写节点、kubeconfig、Helm 路径完全一致。

### A10 · P1 · verify.sh 用 awk 另解析 YAML，合法引号可绕过网络验证

**位置：** `kubekey/scripts/verify.sh:26–38`；`kubekey/scripts/verify.sh:60–78`；`kubekey/scripts/verify.sh:156–161`。

**代码事实：** Cluster name、installerNode、registry 和 stack 由 awk 取第二列，和 Go YAML 解析器不是同一套语义。

**影响/证据边界：** 直接执行原 awk 提取代码：stack: kcn 得到 kcn；stack: "kcn" 得到包含双引号的字符串。后者不满足 shell 的 == kcn，于是走不执行 probe 的分支。这不是用户配置错误，而是同一合法 YAML 在两处解释不同。

**具体整改：** 使用 Go 解析得到的 normalized run manifest，包含 network stack、profile、installer address、端口、component selection、config hash、artifact hash；验证入口消费该产物或调用 Go inspect/verify 入口。保留当前 config hash 一致性检查，但不再让 awk 推测 YAML。不要再增加第三套配置解析。

**完成标准：** 带引号、注释、等价键顺序等合法 YAML 生成相同有效配置和验证分支；未知枚举拒绝；不存在“非 kcn 一律当 Kube-OVN 成功”的兜底。

### A11 · P1 · 预检失败也占用运行目录并要求还原快照，增加无谓失败成本

**位置：** `kubekey/pkg/ani/runner.go:80–90`；`kubekey/pkg/ani/runner.go:164–200`；`kubekey/pkg/ani/runner.go:226–237`；`kubekey/pkg/ani/runner.go:434–442`。

**代码事实：** 先创建不可复用的 runtime root，再校验 artifact checksum、解析 images.tsv 和生成配置。一旦这些只读阶段失败，下一次被目录存在拦住，错误统一要求恢复干净快照。全局 registry unit 冲突也到 Hauler store load 后才检查。

**影响/证据边界：** 尚未变更目标集群的错误被归类成必须重装的现场失败；昂贵的导入完成后才暴露固定 unit 冲突。当前规则有防止在脏现场盲目重试的合理目标，但失败阶段区分不足。

**具体整改：** 只读配置/锁/材料/执行节点检查前移。运行日志目录和“已开始现场变更”的状态分开；preflight-failed 明确允许修正输入后新建 run，不宣称需回滚集群。进入变更阶段后沿用停止/保存证据/实验快照恢复规则。单执行者用简单明确的互斥锁，不靠任意报告目录是否存在。registry unit/端口/磁盘空间冲突在导入前检查。

**完成标准：** 损坏 checksum、缺镜像、无 Helm、unit 冲突均无远程写操作，并不制造必须还原快照的安装状态。变更已开始或远端结果不确定时，仍不得自动重放命令。

### A12 · P1 · SSH 连接器丢弃 context，等待上限不能可靠约束远程命令

**位置：** `kubekey/pkg/connector/ssh_connector.go:311–382`；`kubekey/builtin/core/roles/ani/metrics/templates/verify.sh:144–152`。

**代码事实：** sshConnector.ExecuteCommand 的参数写成 _ context.Context，内部 ReadByte/session.Wait 没有响应 context 的路径。若远端长时间不退出，传入 context 的 deadline 不会由该连接器主动处理。部分 shell 等待循环还有不带 request-timeout 的 kubectl 请求，循环次数不能证明严格的总耗时上限。

**影响/证据边界：** 超时/取消行为难预测，是“为什么一直等”的实际风险。外层进程被终止可能结束本地等待，但不能据此确认远端命令是否已经生效；本轮未注入真实 SSH 故障。

**具体整改：** 为 SSH session 增加 ctx.Done 后关闭/取消的受控路径，执行完成及时释放 goroutine；明确本地取消与远端结果未知的区别，不自动重试。对 API 请求设置单次超时，再设置阶段总体 deadline。修复只针对已有连接器问题，不另写远程任务框架。

**完成标准：** fake SSH server 保持会话不退出：context 超时后调用及时返回；无 goroutine 泄漏；报告为 canceled/remote_result_unknown，不能标记执行未发生并自动重试。

### A13 · P1 · CI 未落在当前仓库入口，构建不跑验收门禁，测试语义不足

**位置：** `kubekey/.github/workflows/golangci-lint.yaml:1–84`；`kubekey/scripts/build-code.sh:21–49`；`kubekey/pkg/ani/components_test.go:1315–1347`。

**代码事实：** 上传仓库根目录没有 .github/workflows，4 个工作流在 kubekey/.github/workflows。GolangCILint 的 jobs 仍限定 github.repository == kubesphere/kubekey，路径过滤也按上游根目录。build-code.sh 调 make build-kk-dev 后复制发布物，没有调用现有 Go/模板/行为门禁。TestMetricsVerifyExercisesRealApis 等测试的主体检查脚本包含关键词，不执行它声称的真实 API 行为。

**影响/证据边界：** 不能把“仓库有 workflow YAML/大量 test 文件”当成“本仓库每次变更都被自动验证”。字符串测试可用于防止漏项，但抓不到路径错位、错误 UID 断言、API 语义、失败后是否越界删除资源等问题。若另有未上传外部 CI，本次无法评价其覆盖。

**具体整改：** 在根 .github/workflows 建 ANI 的轻量流水线，工作目录 kubekey，路径覆盖 pkg/cmd/builtin/ani/scripts 及共享门禁；移除上游仓库限定。把既有有价值的 render gate 固化为单入口 scripts/check-code.sh（拟新增），构建发布必须调用或验证同一 commit 的门禁凭证。补真实执行的局部 fixture/mock：YAML/heredoc、验证脚本嵌入 Python、API 返回体、Pod UID、失败命令轨迹。不要求每次 PR 都重装三节点，但最终发布仍做真实离线验收。

**完成标准：** 故意引入一个缺失镜像键、坏 heredoc、带引号的 stack、错误 API fixture、网络组件删除动作，任意一项都能在构建发布前失败；只改模板或 scripts 也会触发 CI。

### A14 · P1 · 安装与高成本破坏性验收捆绑，并在顶层 verify 再执行

**位置：** `kubekey/builtin/core/roles/ani/metrics/tasks/main.yaml:94–109`；`kubekey/scripts/verify.sh:133–147`；`kubekey/cmd/kk/app/builtin/ani.go:28–49`。

**代码事实：** metrics 的 945 行 verify、fluent-bit 的 891 行 verify 含多段 API 断言、Pod 重建和恢复测试。安装 role 已执行组件验证，顶层 verify 又重新调用同一脚本。ANI CLI 目前仅注册 install，没有独立组件安装入口。

**影响/证据边界：** 一个很小的验证器错误往往要经过材料复制、整套集群安装和所有前置测试，才能到达未覆盖分支。重复运行持久化/重建测试增加等待和故障扰动。这不是说真实持久化测试可以删除，而是执行层次不合适。

**具体整改：** 按职责拆分：install 只负责部署、必要就绪与轻量读写探测；verify/smoke 复用固定语义，避免未声明破坏性重建；acceptance 专门执行一次计划内重建、持久化和端到端离线场景。抽出少量共享的 wait/evidence/http/marker helper，保留组件各自断言。为按计划后续补组件增加最小 component-only 安装入口，校验既有集群并拒绝触碰 kubeadm/CNI/磁盘；不扩展为通用升级/卸载/断点恢复引擎。

**完成标准：** 同一发布的专项持久化验收有唯一 run/结果，不因 install 后执行 verify 再跑一遍。纯 checker 修复可以先通过本地 fixture 验证。component-only 不重装 Kubernetes、不改 CNI、不重建 Ceph。

### A15 · P2 · 自举仓库的重启与长期镜像来源边界不明确

**位置：** `kubekey/pkg/ani/runner.go:232–252`；`kubekey/pkg/ani/runner.go:434–461`。

**代码事实：** service 文件含 WantedBy=multi-user.target，但 RunInstall 只执行 daemon-reload 和 restart，没有 enable。所有 LocalReference 仍指向安装节点上的这个 registry。

**影响/证据边界：** 首次安装能成功不意味着冷启动后镜像服务自动恢复。节点已有缓存时可能暂时看不到问题。本期没有真实冷启动验证记录可在此独立确认，且完整控制面/存储灾难恢复不应顺便扩展成本期范围。

**具体整改：** 明确自举仓库是需持续存在的离线镜像来源，还是未来交接 Harbor 的临时服务。前者应启用启动项、验证物料路径持久性；后者需要显式交接契约与冷拉验证，未交接前不能删除原包或停掉唯一源。不要因为 Harbor 在路线图中就假定节点镜像来源已切换。

**完成标准：** 在授权的实验专项中重启安装节点，registry 自动恢复；测试未命中缓存的必要镜像拉取。后续交接需要验证实际地址/凭据/镜像引用，不只是 Harbor push/pull。

## 为什么开发/调试耗时这么长

### 1. 不是猜测，历史记录明确显示“验证脚本的 bug”消耗整轮安装

下表均为仓库历史记录，不是本次实测；这些编号对应的问题已有后续修复，不能当作当前未修复缺陷再次统计。

| 记录 | 那一轮真正失败原因 | 记录中的安装运行时间 | 本可提前执行的检查 |
|---|---|---|---|
| A19 / h4loki-a23 | heredoc 中 YAML 行尾多了 `]` | 约 36 分钟 | 实际展开的 heredoc 交 YAML parser |
| A20 / h4loki-a24 | `busybox:1.37` 与锁中 `1.37.0` 键不匹配，产生无效镜像 | 约 32 分钟 | 所有模板镜像键与所选镜像集合校验 |
| A21 / h4loki-a25 | Pod 内执行 Python，却传宿主机路径，另有参数和比较断言问题 | 约 22 分钟 | 同执行位置的 checker fixture 与参数测试 |
| A23 / h4loki-a27 | StatefulSet 重建比较 Pod 名字而不是 UID | 该轮约 38 分钟（交接记录） | 重建对象 UID fixture |

前三个记录中的安装运行时间相加约 90 分钟，还不包括编译、传输、快照恢复、分析和下一轮调度。不能据此推算项目总工时。

证据：`docs/observability-components-status.md:1717–1786,1803–1808`；`docs/observability-h4-h5-handoff-20260920.md:63–82,137–139`。

### 2. 每轮都支付很长的固定成本

交接文档记录了：快照恢复约 10–13 分钟、编排/3GB 传输约 3–5 分钟，安装本体随失败点深入分别达到 22/21/38 分钟。它们是当时环境的记录，不是本次给出的性能承诺或所有安装的固定耗时。

开发反馈路径变成：改 checker → 编译 → 同步两个 releases 目录 → 还原三节点 → 传包 → 安装底座 → 运行前置组件全部验收 → 才执行刚改到的分支。这使一个字符或参数切片错误也支付一次整轮成本。

### 3. 恢复重试既扩大耗时，也干扰判断

`metrics/verify.sh:132` 中 `600s + 420s + 600s` 仅三次等待预算就有 27 分钟，还没有计入 Pod 删除、CNI/OVS 等待和 API 请求耗时；它并不意味着每次成功执行都耗时 27 分钟。源码还有 read-again 重入，反复“恢复后再测”可能把真正的网络故障与验证器错误混在一起。

### 4. 静态门禁与行为门禁混同

`bash -n` 能检查 shell 语法，但不验证它输出的 YAML、Pod 内文件路径或 API 返回语义。关键词存在测试能防止漏掉某段设计，但不能证明分支可正确运行。历史修复不断扩充文档和字符串检查，却没有把全部后端/故障分支变成可局部执行的回归场景。

### 5. 构建/实验工具有多份入口和状态

交接文档 `:113–115,172–179` 明确要求同步两个 releases 目录和 c2/c3/c4 三份门禁。这些步骤本身不是组件功能，却容易发生“改的是 A、跑的是 B”。需要统一 source SHA → code release SHA → artifact SHA → site config SHA → attempt ID 的关联，避免人工靠目录名字确认。

**不能从现有文件判断：** 总劳动时间如何分配、模型推理占比、真实网络/磁盘吞吐、每位执行者效率、所有轮次是否都有必要。因此不对项目总工时给虚构比例，也不把全部成本都归咎于同一个组件。

## 不应被误判为缺陷的内容

- `kk` 与 artifact 已有独立脚本和产物布局，这是正确方向。聊天里曾经抱怨的“每次代码修改重制大包”不能原封不动算作现在仍存在；现在应补材料验证和引用指纹，而非取消分离。
- 三节点、Ubuntu/amd64 的限定有首版范围依据，暂不支持任意拓扑本身不是 bug。真正问题是部分接口允许了实现不支持的取值，或者 lab 假设未显式声明。
- 正常的计划内 Pod 重建用于持久化验收有价值；应删除的是失败后的底层故障绕过，不是删掉全部真实验收。
- Kube-OVN 的 `enable-lb-svc=false` 是未开启可选功能，不应单独报成组件 bug；Multus/LB 是另外的选定功能接入。
- 大量官方 CRD YAML 是供应商材料，不因文件长就重写；应保留固定上游来源、摘要和小的站点 overlay。
- 当前未接入 Milvus/Kubeflow 等是路线图待办，不混入本次既有代码缺陷统计。
- 固定版本研究文档不是已执行的材料锁，也不是实装兼容认证；本次不再次替换所有组件版本。

## 建议拆成六个可验收变更集

| 顺序 | 变更集 | 主要文件/范围 | 交付门槛 |
|---|---|---|---|
| 1 | 执行边界与敏感信息 | A01/A02/A03/A06 | 无隐式盘授权、无 kcn 修复绕过、无明文凭据；失败不继续写 |
| 2 | 真正生效的代码门禁 | A13，先把本报告复现纳入回归 | 根级 CI + 同 commit 本地 check 入口；模板与失败行为均有执行测试 |
| 3 | 物料与安装前预检 | A07/A08/A09/A10/A11 | 所有缺项/错包/错节点/错误配置在远程写前失败 |
| 4 | Kube-OVN 修复与报告真实性 | A04/A05 | 非默认 CIDR 和实际 Pod/Service/DNS 场景通过；无 Envoy 复用 |
| 5 | 验证层次与等待治理 | A12/A14 | 局部 fixture 快速覆盖，专项验收只跑一次，取消/超时有明确结果 |
| 6 | 自举仓库与后续组件入口 | A15、A14 的 component-only | 生命周期清晰；补组件不触碰已建底座；随后恢复既定组件实施 |

每个变更集都先有失败用例，再改实现，再证明确实拦截；最后才做干净快照真实离线验收。对已经发生现场变更且失败的 run，不建议脏现场重试。阶段快照如要使用，只能属于实验协议且与固定代码/物料关联，不能塞进 installer 作为自动恢复；正式发布仍需原始干净快照全链检查。

这里不要求先开发通用插件 SDK、任意 DAG、自动回滚或分布式恢复系统。以现有 Go 入口、role 和少量共享 helper 解决即可。

## 本次实际验证与限制

| 检查 | 结果 | 限制 |
|---|---|---|
| ZIP 解包/路径检查、Git HEAD/源包 hash | 已完成 | 只代表所给快照 |
| 原 `test-debian-repository.py` | 3 场景通过 | 使用 mock apt，不连接节点 |
| 原 `test-kubeconfig-export.py` | 按其非 root 运行约定，4 场景通过 | 临时 HOME/mock getent；未写真实 kubeconfig |
| 原 images.go/images_test.go 独立执行 | 2 个测试通过 | 只复制标准库依赖的这两个源文件测试，不等于完整 pkg/ani 测试 |
| `kubekey/scripts` 下 8 个 shell 文件 `bash -n` | 通过 | 不代表模板或生成资源有效 |
| 本报告局部负向复现 | 7 项全部观察到当前错误行为 | “confirmed=true/PASS”表示问题成功复现，不表示产品验收通过 |
| 完整 `go test ./pkg/ani` | 未完成 | 代码要求 Go 1.25.0，本环境为 Go 1.23.2；编译前即终止，没有修改 go.mod 降级 |
| Helm 完整渲染、实机 SSH、三节点离线安装、网络故障/冷启动 | 未执行 | 当前环境没有对应工具/授权现场；本报告不宣称这些通过 |

原始测试输出见附带复现包 `results/`。复现脚本接受你的现有仓库路径，以临时目录和 mock 验证，无需执行 installer、SSH 或 Kubernetes。

## 少量外部语义核对

外部资料仅用于核对运行平台语义，不替代上传源代码事实：

- GitHub Actions 工作流只从仓库根 `.github/workflows` 搜索： https://docs.github.com/en/actions/concepts/workflows-and-actions/workflows
- Kubernetes 镜像 tag 与 digest 的区别、摘要固定内容： https://kubernetes.io/docs/concepts/containers/images/

本报告没有进行新的组件版本兼容性审计，也没有用最新版本文档来替换该快照的原有设计。

# ANI installer main 复审：R 系列完成度与代码问题

**审查日期：2026-09-25**  
**仓库：`zhangzhe-ctrl/ani-installer`**  
**固定提交：`f58430e84a1949b837c59139dc45979c33ae5c4f`**  
**比较基线：`275827f17abeb0d84694d078d0bc2877307cf87e`；main 比它多 3 个提交。**

## 0. 结论

不建议将当前提交认定为“R 系列所有必需项均已实现并验收”。已经有实质整改、真实绿色 CI，以及仓库登记的一个选定组合实机通过结果；但首装/验证/新增组件的关键合同仍有断点。尤其 R02/R13 的重建边界、R07/R08/R09 的材料与运行记录、R15 的现场身份/执行前检查需要继续收尾。

建议暂缓 B 系列自动实机安装；不必停止独立材料核对，也不必从 R00 重新开始。修具体缺陷，补真实执行路径回归，再重验受影响的组合。

### 本次边界

- 通过已连接的 GitHub 工具读取 main、固定提交源码、比较结果、GitHub Actions 状态及日志。
- 对核心文件整段/分段检查，覆盖 ANI CLI、首装、组件计划与执行、材料、渲染、验证、状态、Ceph 预检、SSH、CI 及 R16 记录；不是穷尽审计全部上游 KubeKey。
- 未修改远端仓库、未建立 PR、未连接 Fedora、ESXi 或实验集群，未执行真实安装。
- 完整 main 没有在本沙箱构建：Git 克隆因 DNS 失败，本地 Go 为 1.23.2，而仓库需要 1.25.0。本次没有降低 go.mod。
- 独立运行的是从固定提交摘出的函数/命令片段与隔离 fixture。12 个场景含正反对照；不冒充完整仓库测试。
- 附带可放回仓库的建议回归文件只做了 gofmt 语法处理，**未在完整仓库中编译执行**。
- 保留用户最新边界：当前实验密码不轮换，不把密码专项整改列成阻塞；kcn 专属 Envoy 不供业务复用；不重写框架；不添加故障绕过。

## 1. 仓库的“完成”证据到底支持什么

### 1.1 CI：确实成功，不是凭 agent 总结判断

固定提交的 Actions run 为 **36145032955**，job 为 **108103938750**。读取到的 job 日志显示 Go 1.25.0 环境下：

- `go build ./...` 通过；
- `go vet ./pkg/ani/...` 通过；
- `go test -count=1 ./pkg/ani/...` 通过；
- 7 组 Python 行为/制包/门禁测试执行完毕；
- release shell 语法检查通过。

但是 gate 没有 `-tags builtin`，也没有执行 `pkg/connector` 测试。绿色 CI 只证明它实际运行的集合，不证明全部产品路径安全。详见 F10。

来源：`.github/workflows/ani-check.yaml`、`kubekey/scripts/check-code.sh`、上述 job 日志。

### 1.2 R16：是选定组合通过，不是全矩阵通过

`docs/execution/evidence/R16-20260925/task-result-r2-final.yaml` 的整体值是 `pass_selected_combination`。

记录覆盖 kcn/Ceph 路线、cert-manager/PostgreSQL/NATS/metrics/Loki/Fluent Bit，以及后续新增 Valkey；登记了首装、smoke、部分重建验收和拒绝重复接管的场景。测试新增 Valkey 而非 NATS，本身不推翻“新增一个组件”的测试目的，应明确记录实际案例即可。

同一记录及 `kubekey/docs/progress.md` 又明确保留 OpenSearch 实机未验证、Kube-OVN 完整首装/外部存储/冷启动等待覆盖项，以及 render heredoc、run-state 字段、组件执行器 `--kk` 默认值的待办。

本次读到的是仓库提交的结果记录，不是本次重跑。多数原始现场日志在用户 Fedora/工作区外部；不能由报告中的绝对路径推定它们现在存在或内容已被本次验证。

**建议状态表分开保留：代码阶段、选定组合实机、其他支持组合、已知缺陷。不要把 `blocked`、`not_verified` 或 `pass_selected_combination` 统一折算成“全部完成”。**

## 2. 优先级说明与问题总览

P0/P1/P2 是本项目整改优先级，不是 CVSS，也不代表已经发生生产事故。

| 编号 | 优先级 | 核心问题 | 主要关联 |
|---|---|---|---|
| F01 | P0 | smoke/首装仍进入旧重型验证，含状态服务 Pod 删除、超时后的第二次重建 | R02/R13 |
| F02 | P0 | 组件 execute 未重新核验现场，忽略 kubeconfig，无共享执行互斥 | R09/R15 |
| F03 | P1 | base-run 只是配置身份，缺成功安装和真实集群身份/健康约束 | R06/R09/R15 |
| F04 | P1 | 首装终态与 run.json 产出没有闭合，验证接受未成功/不匹配记录 | R06/R09/R13 |
| F05 | P1 | 预检存在空实现、反向判断和漏项；checksum 仍在占用运行目录后检查 | R07/R09 |
| F06 | P1 | 材料检查存在组件绑定、归档、索引子 manifest 与新增路径缺口 | R07/R08/R15 |
| F07 | P1 | render 与真实安装选择不同，未渲染 Chart，语义检查既误报又漏报 | R08 |
| F08 | P1 | Helm 所有权探测固定读取 v1 修订，不能代表当前 release；错误被当不存在 | R15 |
| F09 | P1 | smoke 不传 context，专项一次性控制只依赖结果文件，持久化断言不足 | R12/R13 |
| F10 | P1 | CI 没覆盖正式 builtin 构建与 SSH 测试；只读 smoke 测试替换了真实脚本 | R04/R12/R13/R15 |
| F11 | P2 | 冷启动仅凭证据文件存在即标 verified，缺内容/运行身份绑定 | R14 |
| F12 | P2 | CLI 默认资源/子进程路径依赖源码目录或 PATH，不符合独立发布物使用 | R08/R15 |

## 3. 详细发现

### F01：smoke 和首装仍会执行有破坏性的旧脚本

**位置：** `kubekey/pkg/ani/verify.go` 的 `runSmokeScope`（约 366–418 行）；`builtin/core/roles/ani/metrics/tasks/main.yaml` 的 Run component verification；`metrics/templates/verify.sh` 的起始清理、`k5_rebuild_wait`、[7/8]、[8/8]。

`runSmokeScope` 注释称调用只读 checker，实际执行的是 `/etc/kubernetes/ani/<component>/verify.sh`。只设置 kubeconfig/output 环境变量，没有传验证层级，也没有改成另一份轻量脚本。

metrics role 在安装阶段也直接执行这份脚本。脚本会清理相同 run label 的历史测试对象、创建/修改告警配置、执行完整 firing/resolved 检查，并删除 Prometheus 和 Alertmanager Pod。

此外，实际调用链是：

```text
调用者先 delete Prometheus Pod
  → k5_rebuild_wait 先等待
  → 等待超时后 helper 再 delete Pod
  → 第二次恢复成功则 helper 返回 0
```

把第二次删除命名为 `one planned rebuild` 没有改变其“失败后补救”的性质。本次安全 fixture 执行该 helper 与调用序列，观察到 **2 次 mock delete，退出码 0**。

**影响：** 普通 smoke、首装均可重建状态服务；`--allow-pod-recreate` 只约束外层 acceptance，不能约束这条旁路。安装完成后再执行 smoke，还会重复重型验收。这里不声称发生了 PVC 删除或数据损坏。

**最小整改：**

1. 真正分离轻量 smoke 与 acceptance 实现，不只在 Go 外层增加 `level` 名字。
2. 安装阶段只执行必要就绪与受控轻量读写；重建必须在有授权的专项路径。
3. 删除等待超时后追加的第二次重建；保留第一次计划内重建后的只读等待/取证。
4. 测试资源使用当前 attempt 的唯一身份；不在普通验证开头清理历史现场。
5. Pod 删除限定准确对象和原 UID，不用宽泛标签承担授权。

**回归：** 对真实渲染的 metrics/fluent-bit 脚本执行命令轨迹测试；smoke 不得删除/重启现有服务 Pod；专项只允许一次明确重建，之后超时必须失败。

### F02：组件计划与执行之间缺关键的重新核验

**位置：** `components_install.go:660–804` 的 `RunComponentsExecute`；`ComponentsExecuteInput.Kubeconfig`；`ani_components.yaml`；各 Helm role。

计划阶段做过部分现场检查，但 execute 只比较新站点配置摘要和 clusterName，然后信任计划里的 `planned/already_installed` 集合并启动 KubeKey。

执行阶段没有调用现场身份、健康、所有权、镜像真实性复核；没有重新计算规范组件范围；没有与首装/专项共享的 `AcquireInstallFlock`。独立输出目录不等于集群级互斥。

execute 输入中的 kubeconfig 没有送给子进程或其 kubectl/Helm 调用。计划使用 A 配置而执行环境默认指向 B 时，缺少明确绑定。部分 role 仍使用 `helm upgrade --install`，因此计划后出现的同名 release 可能被升级而不是拒绝。

**触发例：** 计划时 NATS 不存在；其他操作在执行前创建 NATS；旧计划照样运行 Helm。或两份不同输出目录的计划并行执行同一组件。无需恶意修改计划即可出现。

**整改：** 在同一产品侧互斥区中重新检查当前集群 UID/健康、安装节点、受管 release/CRD、实际材料和重算 scope；绑定计划/执行的 kubeconfig 和代码/材料身份。未知、禁用、暂缓、重复组件及不合法状态必须拒绝。新增路径不得隐式 upgrade。

**回归：** plan 后更换 kubeconfig、变更 release 所有者/版本、替换 artifact、并发执行，都必须在首次写集群前失败。不能只测试“计划阶段会拒绝”。

### F03：所谓底座身份，目前主要比较两份配置

**位置：** `components_install.go:319–436` 的 `loadBaseRunRecord`、`compareBaseInvariants`、`preflightLiveCluster`；`run_manifest.go` 的 `RunManifest`。

base-run 只要求非空 `ConfigDigest/ClusterName`。不要求匹配的安装成功状态。`--state` 对应字段没有在该计划校验链中真正落实。

不变量比较的是两份记录中的 clusterName、networkStack、profile、registry 地址、installerNode、节点名称/IP；不是把实际集群与原安装身份比对。现场检查读取 Kubernetes 版本、业务 Namespace UID、StorageClass；业务 Namespace UID 只是新记录，不与原底座比对。没有查询全体节点 Ready、实际节点集、主 CNI 健康或稳定的集群身份。

Namespace 查询的任意错误都写成 `will-be-created`，这也把 Forbidden/连接失败与 NotFound 混为一谈。

**影响：** 同版本、同名存储类的其他集群可能通过；有 NotReady 节点的集群也没有被这里的健康条件拒绝。不能把“配置未改”称作“底座身份未变”。

**整改：** 首装成功生成受管安装记录，包含稳定集群标识（可用受管记录结合 kube-system UID）、节点身份、CNI/底座版本及存储/registry 身份。新增时实读比对，区分原底座配置摘要和新有效配置摘要。错误仅在明确 NotFound 时允许计划创建。

**回归：** 同配置但不同集群 UID、NotReady 节点、节点集不符、Namespace Forbidden、failed/preflight-only base run 都零写拒绝。

### F04：首装状态、成功记录和验证入口没有连起来

**位置：** `runner.go:174–360`；`preflight.go` 的报告摘要赋值；`run_manifest.go` 的 `RunValidate/BuildRunManifest`；`verify.go:174–217`。

首装把状态写到 `registry_content_verified` 后进入 KubeKey。KubeKey 失败的 return 路径没有写安装失败；成功路径也没有最终 `succeeded`，只是输出连接说明与 completed 日志。多个早退同样保留旧中间阶段。

当前 `RunInstall` 没有在成功后生成新增入口所要求的完整成功 `run.json`。`RunValidate` 能生成名为 run.json 的配置记录，但不代表安装发生；两者没有类型/状态约束。

`SourceTreeFp` 实际赋 `report.ConfigDigest`，而该摘要来自 artifact 的 `config/package.yaml`，不是源码树；这不能作为 source → kk → site → live 的证据关联。

`loadVerifyRun` 只拒绝精确字符串 `install_failed`：`installing`、`preflight_failed`、`registry_content_verified`、远端未知以及不同 config digest 的状态仍会被接受。无 state 时也接受配置记录；短的非空 ConfigDigest 会在 `[:12]` 处 panic。

**本次复现：** 隔离执行原函数，以上三类非成功 phase + 不同 configDigest 均被接受；短摘要触发 slice bounds panic；精确 `install_failed` 的对照能够拒绝。

**整改：**

- 区分“配置校验产物”和“成功安装记录”；成功安装记录只能在首装完整成功后生成。
- 所有返回路径写准确结果：最后完成阶段与最终结果分字段；失败、取消、远端未知均明确。
- record/state 的 schema、runID、摘要和集群身份一致才能做成功验收。
- 源码指纹取代码发布记录，站点摘要取规范化站点配置，不复用无关摘要字段。
- 校验摘要格式和长度，错误返回而非 panic。

**与之前阻塞的关系：** “底座从哪里来”应由正常首装回答；当前产品没有产出可靠成功底座记录，确实会迫使实验流程额外补记录。这不是仅填写几个环境变量就能解决的完整产品闭环。

### F05：预检还存在空实现、反向判断和过晚失败

**位置：** `preflight.go:173–385`；`runner.go` 中声明的 required 列表与 checksum 调用顺序。

`CheckUnitInactive` 在 runner 为 nil 时替换成永远返回 nil 的函数。生产调用传 nil，因此不执行 systemctl。即便传真实 runner，`systemctl is-active` 返回 0（active）被放行，非零却报告 active，判断方向反了。本次对三个分支均执行隔离测试确认。

RunPreflight 的 required 集合检查了元数据、所选 Chart 路径，却未覆盖原先声明的所有核心归档/二进制；外层 required 列表没有被实际消费。Chart 这里只查存在，不按对应 LockedChart 核对内容。ISO 记录只有恰好两个字段才校验，解析不合要求会跳过而不是失败。

整体 SHA256SUMS 的真正验证仍发生在创建不可复用 runtimeRoot、写 `changesStarted=true` 后。坏包仍可能留下“安装已经开始”的状态，随后要求处理旧 run。

**整改：** 一个共同的 required/material 校验入口由 validate/首装/新增使用；真实 systemd 查询和错误分类；按所选依赖核验内容/可执行性；checksum/本地渲染在运行占位和任何目标写操作前完成。输入错误只留下 preflight 证据，不污染安装状态。

**回归：** active unit、inactive unit、缺 hauler/archive/系统包、错 Chart、坏 checksum、畸形 ISO 记录全部测试；配置/材料拒绝后可以安全修正输入，不需要还原三台机器。

### F06：材料真实性校验没有覆盖完整发布与消费链

**位置：** `scripts/build-offline.sh:126–243,285–322`；`materials.go:551–699`；`runner.go:630–731`；`components_install.go:576–610`。

有真实进展：新增 lock parser，首装对纳入 ComponentMaterialLock 的镜像做 manifest 摘要比较。这不应被忽略。

但还存在：

1. 制包读取 `actual_digest` 后仍按 original tag 拉镜像，只比较 image count；直接传入 HAULER_ARCHIVE 仍只复制。错误内容可能要导入启动 registry 后才发现，不是制包已经保证正确。
2. Chart 哈希在整份 YAML 中 `grep` 命中就算批准，未绑定具体 chart 条目的 name/version/artifactPath。**隔离复现把 beta Chart 放入 alpha 目录，alpha 锁摘要明确不同，但脚本仍复制到 alpha 的正式路径并退出 0。** 对照正确摆放时也返回 0。
3. 已存在 tar 中的 ISO 只按文件名是否出现判断需不需要注入，没有核验内部 ISO 和外部批准 ISO 内容一致。
4. 处理 OCI index 时只验证 index 及其声明的 amd64 digest；没有继续获取对应子 manifest。index 分支返回空 config/layer 集，后续 blob 循环因此没有覆盖实际层。可出现 index 存在但子 manifest/层缺失。
5. 不在 ComponentMaterialLock 的 base/KubeKey 镜像只做存在性检查，代码诚实打印 not lock-verified，但没有补上其独立批准来源的实际校验。
6. 新增组件路径只做 HEAD 200，没有复用首装摘要校验；镜像 URL 又从 original 猜，而非使用批准的 Hauler 映射。

**边界：** 不声称任意坏包最终都能安装成功。首装的已有摘要检查确实能抓住部分错误；问题在于发现过晚、范围不完整、不同入口标准不同。

**整改：** 真正共用已有 MaterialsLock API；Chart 每条目精确配对并复验落盘文件；镜像以 digest 拉取或在制包完成前核对实际 digest 与层；解析归档且验证嵌套 ISO；index 递归到所选平台；新增入口复用同一内容校验。不要再建第三套 grep/awk 物料规则。

### F07：render 未能代表实际部署，既有误报也有漏报

**位置：** `config.go:1430–1740` 的 `aniRoleEnabled`、`RenderSite`、`ValidateRenderedArtifacts/RunRender`；`create_cluster.yaml:95–115`；ANI render CLI。

- `kcn + base` 的实际首装仍运行 kcn、专属 Envoy、smoke；`aniRoleEnabled` 却把三者都排除在 base 渲染外。
- RenderSite 只渲染 role 中的 Go 模板/values/task；没有对实际离线 Chart 执行 Helm template，不能将 values 语法检查当成最终 Chart 资源验证。
- Python heredoc 只豁免 delimiter `PY`，真实 metrics 使用 `PY_EOF`；这也是仓库自己登记的 render 待办。
- 普通资源 YAML 解析出错直接 `continue`，没有报告失败。
- 同一文件内重复同一个资源，因 `previous != file.Name` 条件而漏掉。
- 文件源只要出现 `.item` 或 `.stdout`，就整体豁免 `<no value>`，不是只允许那个运行时字段。
- vendor 依据 `*-install.yaml` 等后缀判断，Kube-OVN 的站点模板也会落进过宽豁免。

**整改：** 与真实 playbook 共享选择结果；用相同已发布 builtin 材料渲染，固定离线 Helm 执行实际 Chart 展开；明确 YAML/Python/shell 文本分类；非法 YAML、重复资源、非允许缺值均失败；供应商 CRD 只豁免确有依据的字段检查，不豁免文件语法。

**特别强调：** 校正 kcn 的 render 选择不等于把其 Envoy 解耦成业务共享网关，后者依旧不做。

### F08：release 所有权探测只看 revision 1

**位置：** `components_install.go:445–563` 的 `preflightComponentOwnership/helmReleaseChart`。

固定读取 `sh.helm.release.v1.<name>.v1` 只能代表第一份记录，不能代表当前部署。旧 revision 与当前不同或 v1 已被历史保留策略清除时，可能判断错误。仅 chart 名/版本相同，也不等于属于本 installer 或当前状态健康。

manifest 管理的 PostgreSQL/Valkey 没有受管所有权标记，已存在就拒绝；不能兑现“同版本自有组件返回 already_installed”的通用承诺。查询这些 workload 的其他错误又被当作未存在。

**整改：** 查询当前 release 的实际 revision、状态、所有权；明确 installer 受管标记；只有已部署且同版本同 owner 才 no-op。明确 NotFound 才进入新建。拒绝升级/接管时不添加强制所有权补丁。

**回归：** v1 与 v2 不同、v1 已清除、failed/pending release、同 Chart 但外部所有者、API Forbidden，以及自有同版本 manifest 组件。

### F09：超时与一次性验收仍有漏洞

**位置：** `verify.go:283–331,366–574`；metrics 等 shell 的 kubectl 调用；`ssh_connector.go:334–451`。

SSH ExecuteCommand 已增加取消处理，这是有效整改；但 smoke 路径使用 `exec.Command`，没有接收 RunVerify 的 ctx。上层取消/截止时间不约束内部脚本，脚本中的多处 kubectl 也无单次请求上限。不能因为 kubectlRunner 支持 context 就认定所有路径都有界。

acceptance “只执行一次”的标记是在全部结束后写结果文件，未在变更前持久记录目标授权/执行状态，且无统一互斥。崩溃、并发或更换输出目录可绕过；反之同一默认结果文件也可能挡住之后对另一组件的合法专项。

现有 acceptanceTargets 只有 PostgreSQL 和 NATS，写的是 PVC 文件 marker。它证明特定文件随 Pod 重建保留，**不等于重建前 SQL 行或 JetStream 消息能够在重建后由真实 API 读取/确认**。其他组件是 skipped，不能让整体 pass 掩盖必需检查缺失。

**整改：** 将 context/deadline 传到实际脚本进程及必要 API；取消后保留远端未知语义；run+目标级一次性日志在删除前写入并受锁保护；持久化分别用 SQL/JetStream 等真实数据协议验证。该写入检查针对隔离测试数据，不是新业务服务。

### F10：绿色 CI 没有覆盖正式产品模式

**位置：** `scripts/check-code.sh:125–166`；`cmd/kk/app/builtin/ani.go:1–2`；`components_project_builtin*.go`；`verify_dispatch_r13_test.go:134–210`；根 workflow。

CI 位置和触发链已经修复，不能再说“这个仓库的 CI 不会运行”。

剩余缺口是：

- build/vet/test 都未启用 `builtin`；正式 ANI CLI 文件带 `//go:build builtin`，对应嵌入项目代码/测试没有进入这组门禁。
- 修改了 `pkg/connector`，却只 test `pkg/ani`。
- R13 “smoke 不删 Pod”测试使用临时的 echo stub，不是实际 metrics/fluent-bit 脚本。它证明 dispatcher 运行无害 stub 时无害，不能证明产品 checker 无害。
- workflow 的 paths 缺仓库根 `run_on_node.sh`、`restore_esxi_snapshots.sh` 和 `config/**`；仅改这些文件不会触发当前自动 gate。

**整改：** 保留现有门禁，增补正式 builtin build/test、connector tests、实际打包 CLI --help/默认路径测试、真实渲染 checker 的行为合同测试，并补路径触发。不要删除已有能抓错误的测试；不要全改成昂贵实机测试。

建议回归命令按当前包结构核实后使用：

```bash
# Fedora / 已核准 Go 1.25+ 环境，在 kubekey/ 中
GOTOOLCHAIN=local go test -count=1 -tags builtin ./pkg/ani ./pkg/connector ./cmd/kk/app/builtin
GOTOOLCHAIN=local go build -tags builtin ./cmd/kk
# 上述是复审建议，本次没有在完整源码环境执行。
```

### F11：文件存在不能证明冷启动

**位置：** `registry_lifecycle.go:119–132`。

InspectRegistryLifecycle 只要 `registry-reboot-evidence.json` 的 `os.Stat` 成功，就标 coldStart=verified；没有读取 JSON 内容、schema、unit、主机、bootID、registry/材料身份及冷拉结果。空文件或别的旧运行留下的文件同样有效。

**整改：** 读取并验证有约束的证据，绑定当前服务/主机/材料与新旧 bootID、真实冷拉结果。无效/旧/不匹配证据保持 not_verified。不要为此自动重启主机。

### F12：独立发布物的默认入口仍需手工补参数

**位置：** ANI CLI render 默认 rolesDir；`RunRender`；`RunComponentsExecute` 的 KKBin 默认值与嵌入项目处理。

render 默认从 package-root 的 builtin 目录读源码，而 artifact 与 code release 本来分离，实际 artifact 不带整套 builtin 源码树。组件 execute 默认执行 PATH 中的 `kk`，不是当前程序的绝对路径：按 `$CODE_ROOT/kk ...` 调用时，PATH 可能没有 kk，或恰好有另一个旧版。

组件项目嵌入/导出问题已有新实现，这是进展；但 render 没有共用它，子进程二进制也没有绑定。

**整改：** 使用当前代码发布物内固定嵌入资源，或显式随 code release 携带的受校验资源；用 `os.Executable()` 取得同一 kk。人工覆盖作为开发能力要明确记录，不能成为正常发布物默认使用的必要补丁。

## 4. 其他应顺手补足的小回归，不扩大成新平台

- Kube-OVN 的 IPv4/不重叠检查已有改善，但 kcn 的 Pod/Service CIDR 仍主要做 net.ParseCIDR，可能接受 IPv6 或重叠；公共 IPv4 合同应在 CNI 专属逻辑之前统一校验，别误禁止 kcn 合法的 encap/intranet 包含关系。
- Kube-OVN 显式网关只拒绝网络地址，没有拒绝广播地址。
- 存储新增预检只看全局 effectiveStorageClass，与组件各自 storageClass 的配置不完全一致：应逐个验证实际要创建的 PVC 的类。
- Ceph 预检中 `blkid ... || true` 不区分“无签名”与检查失败；失败不能被当作空盘证明。保持只读，不追加清盘。
- 新增组件的工作目录虽独立，role 的连接片段仍写回 base 的 canonical connections.d；应通过一次受控路径配置让每次新增输出独立，不靠事后读取共享目录补救。

这些是确定的代码分支/缺少条件观察，但本次未对所有小项单独做动态复现，不把它们与已执行的隔离场景混为一类。

## 5. 哪些整改值得保留

- 根目录真实 Actions workflow、统一 check-code/build gate 已落地并运行。
- 多命令错误传播拆分、pipefail 及局部失败回归，比原先直到实装后才报错明显进步。
- 配置使用严格单文档 YAML，installerNode 与首节点关系和部分 IPv4 范围已显式校验。
- Ceph 不再因为 full 就隐式开启，不再简单 `deviceFilter=^sdb$` 发现磁盘；默认 StorageClass 有保护。
- Kube-OVN 网关配置化、CIDR 校验，以及两种主 CNI 的通用网络与 kcn Envoy 分支已经向正确方向拆分。
- SSH context 处理、组件独立 playbook、嵌入项目供给等有真实实现。
- 实机过程中暴露的重复磁盘路径、Helm release 双 base64、项目路径等问题已有针对性修补。

因此不建议换框架，也不要求推翻全部 R 工作。要补的是关键路径之间的连接，以及测试到底有没有执行到真实实现。

## 6. 建议收尾顺序（不是重新跑 R00）

| 次序 | 收尾变更 | 完成门槛 |
|---|---|---|
| 1 | F01 + F09 验证边界/取消 | 真实 smoke 无服务重建；一次授权重建后失败即停；取消约束实际脚本 |
| 2 | F02 + F03 + F08 组件执行身份/所有权/互斥 | 错集群、过期计划、并发、owner/revision变化均首次写前拒绝 |
| 3 | F04 + F05 成功记录与预检 | 坏包不污染 runtime；首装真正生成 success run；未成功/未知记录不可验收 |
| 4 | F06 材料闭合 | 错配 Chart、错摘要、坏归档、缺 index 子 manifest/层在合适前置阶段被拒绝 |
| 5 | F07 + F12 渲染及发布默认路径 | 实际发布物在无源码/PATH无kk的隔离目录也能使用正确命令与资源 |
| 6 | F10 + F11 门禁和证据 | builtin/connector/真实checker回归进CI，冷启动不凭空文件标通过 |

门禁修补应随前五项同步增加，不必等到最后才写测试。每组先局部回归，再根据影响选择实机场景。最后重新做选定组合首装→新增一个组件→smoke/专项验收，保留未覆盖矩阵，才恢复 B 系列自动实机推进。

## 7. 本次独立复现与附带文件

### 已执行

`repro/extracted_functions.go`：固定提交两个函数的原函数体，配最小数据类型/错误包装适配；不依赖完整 Go 模块。执行环境 Go 1.23.2。

- 3 个 unit 检查分支；
- 3 个不应作为成功安装的 phase；
- 无 state 的配置记录；
- 短 digest panic；
- 精确 install_failed 拒绝的对照。

`repro/run-shell-repros.py`：3 个隔离场景。

- metrics 原 helper + 原调用序列，mock 2 次删除后成功；
- Chart 精确配对的正向对照；
- 把 beta Chart 放入 alpha 目录，错误配对仍被接受。

**12 个场景的 PASS/bug_observed 仅表示观察到预期当前行为，不是产品功能验收通过。** 没有执行实际 systemctl、SSH、kubectl、Helm 安装或快照。

### 待在用户源码环境运行

`repro/review_regression_test.go.example` 是建议的反向回归：临时复制为 `kubekey/pkg/ani/review_regression_test.go`，在正确工具链执行 `go test ./pkg/ani -run '^TestAudit' -count=1 -v`。多数用例预计在本提交失败，修复后应通过。不要为了变绿删测试或调整完成定义。

该文件只做 gofmt 语法处理，未声称完整仓库编译通过；源码变化后须按实际接口适配。不要在生产集群执行任何验证脚本来“复现删除”；真实脚本检查通过 LIB_ONLY + fake命令隔离。

## 8. 固定源码索引

以下链接固定到本次提交，避免 main 后续变化造成行号漂移：

- [主 CLI（builtin 标签）](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/cmd/kk/app/builtin/ani.go#L1-L208) — `kubekey/cmd/kk/app/builtin/ani.go`
- [首装运行链](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/pkg/ani/runner.go#L174-L360) — `kubekey/pkg/ani/runner.go`
- [registry manifest 检查](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/pkg/ani/runner.go#L624-L737) — `kubekey/pkg/ani/runner.go`
- [新增组件 base/live/ownership](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/pkg/ani/components_install.go#L319-L610) — `kubekey/pkg/ani/components_install.go`
- [新增组件 execute](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/pkg/ani/components_install.go#L660-L876) — `kubekey/pkg/ani/components_install.go`
- [配置记录/validate](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/pkg/ani/run_manifest.go#L1-L336) — `kubekey/pkg/ani/run_manifest.go`
- [预检](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/pkg/ani/preflight.go#L173-L385) — `kubekey/pkg/ani/preflight.go`
- [验证分发及 acceptance](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/pkg/ani/verify.go#L174-L574) — `kubekey/pkg/ani/verify.go`
- [真实 metrics role](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/builtin/core/roles/ani/metrics/tasks/main.yaml#L90-L121) — `kubekey/builtin/core/roles/ani/metrics/tasks/main.yaml`
- [metrics 重建 helper](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/builtin/core/roles/ani/metrics/templates/verify.sh#L75-L161) — `kubekey/builtin/core/roles/ani/metrics/templates/verify.sh`
- [metrics 状态服务重建](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/builtin/core/roles/ani/metrics/templates/verify.sh#L794-L892) — `kubekey/builtin/core/roles/ani/metrics/templates/verify.sh`
- [metrics 前置清理](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/builtin/core/roles/ani/metrics/templates/verify.sh#L333-L379) — `kubekey/builtin/core/roles/ani/metrics/templates/verify.sh`
- [采用 stub 的只读 smoke 测试](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/pkg/ani/verify_dispatch_r13_test.go#L134-L210) — `kubekey/pkg/ani/verify_dispatch_r13_test.go`
- [实际首装组件选择](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/builtin/core/playbooks/create_cluster.yaml#L95-L166) — `kubekey/builtin/core/playbooks/create_cluster.yaml`
- [新增组件 playbook](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/builtin/core/playbooks/ani_components.yaml#L1-L44) — `kubekey/builtin/core/playbooks/ani_components.yaml`
- [渲染选择和语义检查](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/pkg/ani/config.go#L1420-L1740) — `kubekey/pkg/ani/config.go`
- [材料锁 API](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/pkg/ani/materials.go#L1-L709) — `kubekey/pkg/ani/materials.go`
- [制包脚本](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/scripts/build-offline.sh#L126-L323) — `kubekey/scripts/build-offline.sh`
- [新门禁](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/scripts/check-code.sh#L1-L167) — `kubekey/scripts/check-code.sh`
- [GitHub workflow](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/.github/workflows/ani-check.yaml#L1-L74) — `.github/workflows/ani-check.yaml`
- [SSH 取消处理](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/pkg/connector/ssh_connector.go#L334-L451) — `kubekey/pkg/connector/ssh_connector.go`
- [冷启动证据](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/pkg/ani/registry_lifecycle.go#L68-L133) — `kubekey/pkg/ani/registry_lifecycle.go`
- [Ceph 设备检查](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/builtin/core/roles/ani/ceph/templates/ceph-preflight.sh#L42-L106) — `kubekey/builtin/core/roles/ani/ceph/templates/ceph-preflight.sh`
- [R16 选定组合结果](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/docs/execution/evidence/R16-20260925/task-result-r2-final.yaml) — `docs/execution/evidence/R16-20260925/task-result-r2-final.yaml`
- [当前进度/已登记待办](https://github.com/zhangzhe-ctrl/ani-installer/blob/f58430e84a1949b837c59139dc45979c33ae5c4f/kubekey/docs/progress.md#L1-L70) — `kubekey/docs/progress.md`

CI：[run 36145032955](https://github.com/zhangzhe-ctrl/ani-installer/actions/runs/36145032955)。

注：源码函数名是定位主键；上述行区间覆盖相关上下文，不表示每行都存在问题。后续 main 更新后仍应按本提交复现，再验证修复提交。

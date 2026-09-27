# C01–C10 定点整改台账（2026-09-27）

审查依据：`ANI-installer-review-ee8c4cc-20260927.md`（固定提交 `ee8c4cc`，分支末次观察 `8f5a1cb`）。
本轮起点：`8f5a1cbd9fbd130929c28f016e706221be6e489`，工作树除本资料目录外干净。
状态口径：`code_fixed` 仅表示生产路径已改并有同条件测试；`local_gate_pass` 表示本地门禁通过；
`ci_pass` 只写远端实际结果且必须落到具体 run/job 与提交 SHA；`live_not_run` 表示本轮授权禁止现场操作，未运行。
本轮汇总：C01–C10 全部 code_fixed；本地统一门禁 rc=0；**代码候选所在提交 8d9de2c7 的 CI 实际为
completed/success（run 36262250848 / job 108460180210）**；现场验收全部 live_not_run。
历史日志与既有实机记录（含已消耗的 090732 专项额度）一律不改写、不重置。

## 工具链（本仓实测，不沿用其他仓库要求）

- `go version`：`go1.26.7-X:nodwarf5 linux/amd64`
- `kubekey/go.mod`：`go 1.25.0`
- 复现包：`SHA256SUMS` 4/4 校验通过；`repros/` 解压至独立临时目录 `/tmp/c-repros-20260927`，未覆盖任何产品文件。

## 逐项

### C01 install-success 记录 verify 不核对实时目标 — code_fixed

问题：`loadVerifyRunRecord` 两条分支只有一条绑定现场集群。components-execution 经
`ValidateComponentsExecutionBase` 调用 `captureLiveCluster` 比对 `BaseClusterUID`；
install-success 只过纯字段检查 `ValidateSuccessRecord`，`verify.go` 全文没有
`captureLiveCluster`／集群比对／安装主机规则。

修改：
- `kubekey/pkg/ani/components_install.go`：把主机归属判定从 `verifyInstallerExecutionHost(cluster)`
  中拆出 `verifyExecutionHostAddress(kind, name, address)`，记录侧可复用，原函数语义不变。
- `kubekey/pkg/ani/verify.go`：新增 `bindVerifyTarget`，在 `RunVerify` 分派任何级别、任何写入之前执行；
  两种记录类型统一比对 `captureLiveCluster` 的实际集群 uid（执行记录以 base 的 uid 为准），
  不一致即拒绝；acceptance 额外要求本机持有记录所写的 installer 地址（smoke 只读，不要求换机即禁）。

测试：`TestC01_ARecordOfClusterBCannotBeVerifiedAgainstClusterA`（smoke/acceptance 两级别，
断言拒绝语同时点名记录 uid 与现场 uid、零 delete/exec/apply、acceptance 零额度消耗）；
`TestC01_AcceptanceIsRefusedOnAHostThatDoesNotOwnTheInstallerNode`（记录正确、主机不对 → 拒绝且无报告）。
`TestVerifyDispatcherLevels/smoke…` 的旧断言“smoke 完全不调 kubectl”已按新语义改为
“smoke 只读、必须绑定现场身份、绝不调用任何变更动词”，变更动词逐条枚举。

状态：code_fixed / local_gate_pass / live_not_run。

### C02 取消回调永不注册 — code_fixed

问题：`exec.CommandContext` 本身把 `cmd.Cancel` 设为 `Process.Kill`，故 `if cmd.Cancel == nil` 永不成立，
`Setpgid` 建的进程组从未被信号化；旧测试只看父脚本末行 `touch finished`，杀掉 bash 而留下子进程也能通过。

修改：`kubekey/pkg/ani/verify.go` `runSmokeScope` 在 `Start` 前无条件注册
`cmd.Cancel = smokeKillFunc(cmd)`；`smokeKillFunc` 忽略 ESRCH（组已退出不是失败）；等待上界抽为 `smokeWaitDelay`。

测试：`TestC02_CancelledSmokeStopsDescendantsNotJustTheParent` — 由**后代**（同组子 shell）在取消后
2 秒写文件；并在断言时刻用 `kill(pid,0)` 确认后代已消失；`t.Cleanup` 回收本测试自建的 pid，不留孤儿。
红/绿已实测：还原 `if cmd.Cancel == nil` 守卫后该测试失败（`child-wrote` 出现，5.28s），修复后 0.28s 通过。

状态：code_fixed / local_gate_pass。

### C03 授权旧 UID 未进入 DELETE 请求 — code_fixed（服务端语义见“残留”）

问题：先读 UID、再复读、随后 `kubectl delete pod <name> -n <ns> --wait=true`，请求本身不携带授权 UID；
最后一次读与真正删除之间的同名替换无人拦住。owner 只比名字，controller UID／controller 标志、
Pod 实际挂载的 PVC、PV 回指关系均未核对。`claimAcceptanceLedger` 只做 O_EXCL 不 fsync；
`finalizeAcceptanceLedger` 用 `os.WriteFile`+rename 且不落目录；三处终态写入的 `Controller` 等字段被丢掉。

修改（`kubekey/pkg/ani/verify.go`）：
- 授权前置扩为：owner 名 + `ownerReferences[0].controller == true` + owner UID == 实时
  `statefulset/<controller>` 的 UID + Pod 的 `spec.volumes[*].persistentVolumeClaim.claimName`
  必须真的包含声明的 PVC + PVC 已绑定 PV + `persistentvolume/<pv>.spec.claimRef.uid == PVC UID`。
  任一不符在**消耗额度之前**拒绝。
- `kubectlRunner.jsonpath` 支持集群级资源（namespace 为空时不再传 `-n ""`）。
- DELETE 携带 `--field-selector metadata.uid=<oldUID>` 与 `--ignore-not-found=false`：
  选择由服务端按 UID 完成，同名替换不再匹配；服务端不能校验该选择器时直接失败，绝不回退成无条件删除。
- 账本持久化：`claimAcceptanceLedger` 写后 `f.Sync()` + 目录 `syncDir`，失败即删除意图文件并拒绝变更；
  `finalizeAcceptanceLedger` 改为 O_EXCL 临时文件 + fsync + rename + 目录 fsync；
  终态统一由 `terminalLedger`/`terminalLedgerWithNewPod` 从意图派生，不再手抄字段；
  账本关闭失败写入结果 detail，不把“已变更”降级成静默；`acceptanceLedger` 增加 `NewPodUID`。
- 顺带修掉本轮改造中暴露的一类缺陷：三处失败分支只设 Detail 未设 `Status`，会让失败被汇总成 pass。

测试：`TestC03_TheDeleteCarriesTheAuthorizedPodUID`（从 fake 服务端读回请求携带的 UID）；
`TestC03_ReplacementAfterTheLastClientReadIsNotDeleted`（fake 在**最后一次 GET 之后**换对象，
断言 intruder 未被删、请求确实带授权 UID、额度未被重放）；
`TestC03_OwnerAndStorageRelationshipsAreAuthorizingFacts`（四个前置各一条，均要求零删除、零账本）；
`TestC03_TheClosedLedgerKeepsEveryIdentityTheIntentCarried`。
fake `apply` 不再无条件放行（见 C04）。

残留（如实记录，不当作已解决）：`--field-selector metadata.uid` 的 kubectl 客户端语义与
API server 对该字段选择器的支持，本轮**没有**真实 kubectl、也不授权连集群，未能实测。
设计因此按“不支持即硬失败、绝不退化为无条件删除”取值：要么条件生效，要么变更不发生。
现场验证需在允许连接集群的那一轮完成。

状态：code_fixed / local_gate_pass / live_not_run。

### C04 NATS 专项 Job 层级错误 + 跳过后整体 pass — code_fixed（重型套件未接通）

问题：`runCheckJob` 手工拼 YAML，`template:` 落在文档顶层而非 `spec.template`，摘取生产字符串实际解析
证实顶层 template=true、`spec.template`=false；fake apply 无条件成功所以离线全绿。
`acceptanceTargets` 只有 postgresql/nats，指定 metrics/fluent-bit 记为 skipped，而
`report.Overall` 只对显式 fail 取反，于是 `skipped` 仍 `overall: pass` 且 rc=0。

修改（`kubekey/pkg/ani/verify.go`）：
- 改用已有类型 `batchv1.Job` + `corev1`/`metav1` 构造，`sigs.k8s.io/yaml` 序列化（均为本仓既有依赖）；
  新增 `buildCheckJob`／`validateCheckJob`（容器数、restartPolicy、镜像、shell 程序），
  本地可判定的结构错误在 apply 之前拒绝，不消耗额度；删除已无用的 `indentBlock`。
- acceptance 分派：显式 `--only` 指向未实现专项 → 在取锁与账本之前拒绝；
  未显式指定时把级别范围收敛到**已声明**的目标，把未检查项写进报告 `notDeclared` 并在 stdout 明说；
  一个可声明目标都没有 → 拒绝。`report.Overall` 改为“全部 pass 才 pass”，空结果集亦不通过。

测试：`TestC04_EmittedAcceptanceJobIsATypedJobWithAPodTemplate`（序列化后独立反解，断言
`spec.template.spec.containers`、Secret 引用不落明文、并匹配真实 YAML 嵌套）；
`TestC04_TheOldMisNestedJobShapeIsRejected`（把审查人描述的旧字节作为 fixture，证明新校验会拒它）；
`TestC04_AcceptanceQuotaIsNeverSpentOnAnUnimplementedSpecialist`（三个未实现名字，零账本条目、零删除、零报告）；
`TestC04_AcceptanceCannotPassOnNothingChecked`；
`TestC04_DefaultAcceptanceRunsDeclaredTargetsAndNamesWhatItSkipped`。
`r13FakeKubectl` 的 `apply` 现在会保存并校验实际发射清单（缺 `spec.template` 即按 API server 方式报错），
`wait/logs` 也不再无条件放行。

未接通（保留为未完成，不结项）：原范围要求的 metrics / fluent-bit 重型验收仍只存在于旧 shell 路径，
尚未接入受 ledger 约束的正式入口。本轮按 goal 明确保留为 open，不删除功能、不整体 skip、
不叫用户手跑绕过账本的脚本。

状态：code_fixed（已声明分支）/ local_gate_pass / live_not_run；重型入口 open。

### C05 依赖已满足被误判为错误；noop 跳过现场复核 — code_fixed

问题：opensearch 声明 `InternalDeps=[fluent-bit]`。计划为 OpenSearch=planned、Fluent Bit=already_installed 时，
execute 取 `plannedNames=[opensearch]`，`componentsScope` 展开为 `[fluent-bit, opensearch]`，
却要求两者相等 → 该状态永久被拒，重规划也改不掉事实。同时全部 ownership/CNI 新鲜度检查都在
`if len(plannedNames) > 0` 里，纯 noop 完全不重查现场。

修改（`kubekey/pkg/ani/components_install.go`）：
- 抽出 `validatePlanClosure(plan, cluster, plannedNames)`：以**计划自己的行集合**（planned + already_installed）
  重算闭合并与之比较，闭包不得引入计划未命名的组件、计划项不得掉出闭包；
  执行写入集合仍是 planned 子集。请求集合用全量行而非 planned，纯 noop 也能过解析。
- 新鲜度复核（live preflight、镜像内容、ownership）移出 planned 分支，对完整闭包恒执行；
  already_installed 行若现场变成非 already_installed（release／归属／版本／健康变化）→ 拒绝并要求重规划。

测试：`TestC05_AlreadyInstalledDependencyIsStillAValidPlan`（正是审查人场景，断言 closure 确含 fluent-bit）；
`TestC05_ClosureChangesAfterPlanningStillRefuse`（两个方向各一条，且要求命中预期拒绝原因）；
`TestC05_NoopReVerifiesWhatThePlanClaimedToHaveSeen`（计划后即改掉现场 release，走 `RunComponentsExecute`
生产入口拒绝，且不留记录）；既有 `TestComponentsExecutionRecordMixedScope` 继续覆盖真实混合状态。
`r15PlanFixture`／`r15PlanAndExecute`／`runR15Execution` 增加 base 块参数以便构造同摘要场景。

状态：code_fixed / local_gate_pass。

### C06 同配置合法 noop 被拒 — code_fixed

问题：`NewComponentsExecutionManifest` 与 `ValidateComponentsExecutionShape` 无条件禁止
`NewConfigDigest == base.ConfigDigest`。组件本就是首装装上的，同一站点配置做只读 no-op 时摘要理应相同，
构造者却认为“摘要没变所以无事可记”，noop 因此无法落成可消费记录。

修改（`kubekey/pkg/ani/components_record.go`）：按操作语义判定 —— writer 只在 `op == add` 时要求摘要变化；
shape 校验改为 `e.DidInstall && 摘要相同` 才拒。noop 允许同摘要，且仍然 `didInstall=false`、
不扩 scope、`componentsExecutionScope` 不给变更权、不重开专项额度。

测试：`TestC06_SameConfigNoopGoesWriterToLoaderToSmoke` 走生产 writer→loader→`RunVerify` smoke 全链，
并显式断言 base 摘要 == 记录摘要、acceptance 被拒、base 的 run.json/run-state.json 字节前后不变。
同时把本仓自带的 `TestComponentsExecutionWriterRefusesARecordVerifyWouldReject` 由“同摘要 noop 必须被拒”
纠正为“同摘要 noop 必须可记录；同摘要 add 仍必须被拒”——它原先编码的正是 C06 这条错误规则。
红/绿已实测：还原两处旧守卫后新测试失败（记录落不下来）。

状态：code_fixed / local_gate_pass。

### C07 显式 kubeconfig 在真实 role/checker 边界失效 — code_fixed

问题：execute 给子 kk 传 `KUBECONFIG=A`，role 里 Helm/kubectl 用 A，但 role 调 checker 只设输出目录；
checker 写 `${ANI_VERIFY_KUBECONFIG:-/etc/kubernetes/admin.conf}`，没有该变量时回落到默认而非 A。
反向，CLI smoke 设了 `ANI_VERIFY_KUBECONFIG=A`，Fluent Bit 脚本却用裸 `kubectl -n …`，
在第一次真实 API 调用前根本不读它，实际跟随继承来的 `KUBECONFIG`/`HOME`。

修改：
- 8 个组件 checker（cert-manager / metrics / nats / postgresql / valkey / fluent-bit / loki / opensearch）
  统一加同一段前置：`ANI_VERIFY_KUBECONFIG` 必填、无默认；文件不存在即拒；
  环境里另有 `KUBECONFIG` 且值不同 → 以“ambiguous target”拒绝，不允许任何一层自己猜；
  `export KUBECONFIG="$KUBECONFIG_FILE"`，使脚本内所有裸 `kubectl` 也被钉在同一上下文。
- 8 个 role 调用 checker 处显式传 `ANI_VERIFY_KUBECONFIG="/etc/kubernetes/admin.conf"`，
  不再依赖 checker 的隐式默认。

测试：`TestC07_CheckersHonourThePinnedKubeconfig`、`TestC07_NoCheckerSilentlyFallsBackToAdminConf`、
`TestC07_ConflictingContextsAreRefusedNotGuessed`（各 8 个组件）。测试**执行真实模板文件**：先用安装器
同一套 `text/template` + 安装器上下文渲染（未渲染的 fluent-bit 会在后端选择处早退，测试将什么都测不到），
再用记录 `--kubeconfig` 参数与自身所见 `KUBECONFIG`/`HOME` 的 fake kubectl 读取最终命令目标，
并强制“至少真发生了一次 API 调用”，否则判定为未测。夹具里 `HOME/.kube/config` 指向第三个集群作为陷阱。

状态：code_fixed / local_gate_pass / live_not_run。

### C08 Fluent Bit smoke 删除固定共享探针 — code_fixed

问题：`CLIENT_POD=ani-fluent-bit-verify-client`、marker 固定 `ani-log-marker-1/2/3`、
post 固定 `ani-log-marker-post`、cursor inspector 固定名，且每个 helper 先 `delete --ignore-not-found`。
`RUN_ID` 只进消息体不进对象名。两次/并发 smoke 会互删对方的客户端与 marker，也会删掉上一轮
为失败取证而留下的对象。

修改（`kubekey/builtin/core/roles/ani/fluent-bit/templates/verify.sh`）：
- `RUN_ID` 改为 `UTC 秒 + $$ + 8 位 uuid`（全小写、DNS label 安全、长度足够短），
  客户端／每节点 marker／post marker／inspector 全部按尝试命名。
- 新增 `OWNED` 归属表 + `own_pod`/`release_pod`/`release_all_owned`：创建后记录 UID，
  只删本轮创建且 UID 仍一致的对象；名字被占用时**报错**而不是删掉别人的探针；UID 变了就放弃删除并说明。
- 清理只在成功路径上执行一次，失败退出到不了那里，探针与证据留在现场；
  残留检查只扫本轮自己的前缀，不把历史对象当本轮垃圾，也不因历史对象存在而误报。
- Python 元数据断言接受 `MARKER_PREFIX` 参数，期望 pod 名由尝试推导而非硬编码。

测试：`TestC08_OverlappingRunsNeverDeleteEachOthersProbes` — 真实渲染并**并发跑两遍**该 checker，
fake 集群建模 UID 与 AlreadyExists；预置两个旧固定名对象；从服务端读回两次运行各自创建与删除了什么，
要求：各自创建不同名探针、二者名字都不是旧固定名、谁都没删旧对象、旧对象仍在。
夹具先补齐 section [1] 需要的集群事实，否则脚本走不到探针阶段（该测试会显式判定“什么都没测”）。
红/绿已实测：把 `CLIENT_POD` 改回旧固定名并恢复先删后建后测试失败。

状态：code_fixed / local_gate_pass / live_not_run。

### C09 成功状态早于成功记录落盘 — code_fixed

问题：`RunInstall` 在 `BuildInstallSuccessManifest`/`WriteInstallSuccessRecord` 之前就把
`Phase/Result` 置为 succeeded；defer 终态器的条件是 `if state.Result != ResultSucceeded`，
于是记录写失败并返回 error 时不纠正 result，最终可留下“命令失败、状态成功”；
末尾 `WriteRunStateAtomic` 错误被 `_ =` 吞掉，defer 落盘失败也只打 WARNING。

修改（`kubekey/pkg/ani/runner.go`）：
- 成功尾部拆成 `publishInstallSuccess`：先写成功记录（用 pending 副本，不动活状态），
  再原子写终态 run-state，两者都成功后才把活状态推进到 succeeded。任一失败都返回 error，
  且第二个失败会说明记录已经落地在哪里，避免被误读成需要重装。
- defer 终态器改为 `settleTerminalResult(&state, retErr, ctx.Err(), installSucceeded)`，
  以函数真实返回值为唯一依据（不再被先前赋值屏蔽）；终态落盘失败时把该错误并入返回值，
  不再出现 rc!=0 而 persisted=succeeded。中间阶段 `_ = WriteRunStateAtomic` 保留（R09 阶段标记）。

测试：`TestC09_TerminalResultFollowsTheReturnedError`（含“已提前盖 succeeded + 返回错误”这一原始条件）；
`TestC09_SuccessIsOnlyAnnouncedAfterBothWritesLand`（正常成功、注入记录写失败、注入终态写失败三种，
断言返回值、活状态未成功、磁盘上未出现虚假成功状态、已真实发生的记录仍被报告且不自动重做安装）。
两者都调用生产函数本体。RunInstall 需要 root 与真集群，本轮不现场跑。

状态：code_fixed / local_gate_pass / live_not_run。

### C10 干净 checkout 缺 Chart 物料 — code_fixed

问题：门禁测试 `TestChartSpecsMatchTheApprovedLockAndTheChartBytes` 直接打开
`ani/charts/**/*.tgz`，而仓库根 `.gitignore` 第 7 行忽略 `*.tgz`，所以干净 checkout 一个都没有。
CI 真实失败（run 36254508211 / job 108438616076）报的就是这个缺失文件。这是缺输入，不是断言错。

按已选定方案实现（正式 gate 之前显式准备并校验固定 Chart）：
- 新增 `kubekey/pkg/ani/materials_charts_prepare.go`：`RunMaterialsPrepareCharts`。
  以批准锁为唯一权威，逐条按 `sha256`/`chartSha256` 全摘要校验，内容寻址缓存
  （`<cache>/sha256/<digest>.tgz`），缓存命中仍重新校验；下载写临时文件 + fsync + rename，
  落位后再读回摘要；非 200、空响应、超 64 MiB、摘要不符一律拒绝且不落地；
  拒绝非 https 来源；落位后用 `readChartIdentity` 校验归档自述的 name/version 与条目一致
  （与 `RunMaterialsPlaceCharts` 同一配对规则）；目标已存在但摘要不符时报错要求人工处置，
  不静默覆盖；`--offline` 一次列出全部缺失项。
- 顺带修掉一个真实缺陷（测试逼出来的）：目标路径由 `chart.Name`/`chartVersion` 拼成，
  而 `filepath.Join` 会边拼边清理，`../evil` 这类名字会被折叠成仍在 root 内、
  但不在 `charts/<name>/` 下的路径。现在在任何 join 之前先按单一路径元素校验，
  并在落位后确认它确实在该 chart 自己的目录里。
- 修掉锁解析器的一个真实丢数据缺陷：组件段写 `chartSource`、批处理段写 `source`，
  `rawChart` 只映射后者（`chartSha256` 早就兼容了，`chartSource` 没有），
  导致 cert-manager 与 nats 两条的来源被静默丢弃、无法准备。现在两种拼法都读。
- CLI：`kubekey/cmd/kk/app/builtin/ani.go` 新增 `ani materials prepare-charts`。
- 门禁：`kubekey/scripts/check-code.sh` 在 go build/go vet 之后、任何 go test 之前加
  `locked chart material the source-tree gate reads (C10)` 步骤（唯一的联网步骤），
  失败即 `fail`。位置经过控制测试校准：放在 build 之前会让
  `test-check-code.py` 的 T-R04-02g「门禁必须编译到 X 并非零」在 build 前就退出而误判。
- CI：`.github/workflows/ani-check.yaml` 在 checkout 之后、gate 之前加
  `actions/cache`，key 为 `hashFiles(kubekey/ani/components.lock.yaml)`；
  命中仍由上面那条摘要校验兜底。CI 与本地共用同一个准备入口，不存在两套实现。
- 无 skip、无 `|| true`、无 `continue-on-error`、无断言删除、无大文件入库。

测试：`kubekey/pkg/ani/materials_charts_prepare_test.go` 十个用例（全部用 `httptest` TLS
进程内服务，测试本体不联外网）：落地三条并逐一复核摘要+自述身份、第二次运行服务端计数为零
（幂等不是从日志推断的）、缓存命中离线可用、摘要不符/404/空响应三种拒绝且零落地、
缓存条目与自身摘要不符被拒、磁盘上已有错字节时要求人工处置且字节未被覆盖、
归档自称另一条 chart 被拒、非 https 被拒、恶意 chart 名被拒、离线一次点名全部缺失、
以及一条纯数据测试证明**出厂锁的 6 条都有 https 来源与 64-hex 摘要**。

真实数据校验（非夹具）：用出厂锁实际联网准备一次，6/6 全部按批准摘要下载并验证通过，
第二次全部 `already present`，空目录 + 缓存离线的场景全部 `from cache (re-verified)`，
空缓存 + `--offline` 明确点名 6 条缺失并拒绝。落地文件名与开发树完全一致
（`cert-manager-v1.21.2.tgz`、`nats-2.14.6.tgz`、`kube-prometheus-stack-85.4.0.tgz`、
`loki-18.13.3.tgz`、`opensearch-3.8.0.tgz`、`fluent-bit-0.58.2.tgz`）。

状态：code_fixed / local_gate_pass（完整 `scripts/check-code.sh` rc=0）/ **ci_pass**。

干净 checkout 实证（不是推断）：`git clone` 本候选到 `/tmp/pristine-c10`，checkout 后
`find . -name '*.tgz'` 计数为 **0**（即审查人描述的裸树条件），在 `ANI_CHART_CACHE` 指向**空目录**下
跑完整 `scripts/check-code.sh` → **rc=0**；准备步骤打印 6 条 `downloaded and verified` 与
`charts prepared: 6/6`，7 个行为套件合计 205 case、0 失败。
反向退出码同样实测：删掉缓存与落地文件后 `--offline` → rc=1 并点名 `nats 2.14.6`；
把缓存条目改成另一份合法归档（与自身文件名摘要不符）→ rc=1 报 `cache entry … hashes to something
other than the digest it is stored under`；随后允许联网再跑 → rc=0 重新取回并校验。
目标位置被人为改坏时 → 拒绝且要求 `remove it deliberately`，不覆盖。

GitHub 真实结果（对应提交 `8d9de2c7a39cc3f8fd55ac83a3627978daa364ad`，非本地推断）：
run **36262250848** / job **108460180210** → `status=completed conclusion=success`，
九个步骤全为 success，其中 `Restore the locked chart cache` 与 `Run the code gate` 即本轮新增/改动部分。
上一轮记录的 CI 失败（run 36254508211 / job 108438616076，失败步骤 `Run the code gate`，
报 `../../ani/charts/nats/2.14.6.tgz` 缺失）在同一入口下已不复现。

## 提交、推送与 CI 实读（分支 review/installer-f-remediation-20260926）

全部经现有 git 凭据正常推送（无 force、无 --no-verify），每次推送后 `git ls-remote` 回读远端 SHA 与本地 HEAD 一致：

| 提交 | 内容 | CI run | 结论（实读） |
|---|---|---|---|
| `8b9257e664eca26e375899aa17747b0bbe73608b` | C01–C10 代码+测试+门禁+workflow（36 文件） | 无独立 run | 被同分支后续提交覆盖，GitHub 只在分支头触发 |
| `8d9de2c7a39cc3f8fd55ac83a3627978daa364ad` | 逐条台账与资料包 | 36262250848 / job 108460180210 | completed / **success**（9 步全 success） |
| `f071cb28245f90f3af86dd12f5efdc1ec463a2c3` | 推送与 Draft PR 交接 | 36262440894 | completed / **success** |
| `2b8373ab93275fd4d90890e338d84c34dd4b69ee` | CI 结果与 pristine-clone 实证入档 | 36262541202 | completed / **success** |
| `80ff740d9f2837927bd091b018f6c8a3037df518` | C07/C08 证据口径收窄 | 36262602687 | completed / **success** |
| `ed2b9eb49ad2e26c9892c532fcdad81777da7b7c` | 删除已不可达的 skipped 状态常量、改正 acceptance 步骤注释 | 36262936791 | completed / **success** |
| `174001101503b0dc36b643cff3371c0856358544` | 本表入档（纯文档） | 36263026975 | completed / **success** |
| 本行所在提交（纯文档，只把上一行结论从“待读”改成实读） | — | 交付时 in_progress | 见下条口径 |

口径：包含全部 C01–C10 生产代码的提交（`8b9257e` 及其每一个后续分支头）都已被至少一个
`completed/success` 的 run 覆盖——`8d9de2c7` 与 `80ff740d` 两个分支头都含同一份代码树
（`2b8373a` 之后只改文档，`ed2b9eb` 只删一个不可达常量与改注释）。
本地对最终工作树重跑完整门禁同样 rc=0（`/tmp/gate-final.log`）。未取到自己 run 结论的提交
不写成 ci_pass。

一条自指导致的永久口径限制（不假装能消除）：本台账的每一行“CI 结论”都是由一次**文档提交**写进
分支头的，而那次提交自己的 run 只能在下一次提交时才看得到。因此本文件中最后一个提交的对象级
结论天然是 pending；已绿的提交树内容全部被上表已列 run 覆盖，本文件本身不参与代码门禁判定。
要一次性闭合只能在代码候选提交上直接带全 CI 结论（需要 push 前知道 push 后结果），不可实现。

一处 cosmetic 缺陷如实记录：`ed2b9eb` 的 commit message 正文里 `` `skipped` `` 的反引号在
`git commit -m "…"` 的双引号串中被 shell 当成命令替换吃掉，正文现在读作“produced a  result”，
缺一个词。提交内容本身正确、subject 完整；分支已推送，修正需要 force push（本轮禁止），
因此保留原样并在此说明，不改写历史。

Draft PR 仍未创建（`gh` token 无效，写操作 401），正文与命令见 `PUSH-AND-PR-HANDOFF.md`。

## 门禁与残留

推送与 CI：分支 `review/installer-f-remediation-20260926` 已用现有 git 凭据推送成功（非 force），
远端 SHA 回读一致。`gh` 的 GitHub token 无效（写操作 401），Draft PR 未创建，正文与精确命令见
`PUSH-AND-PR-HANDOFF.md`；公开仓库只读 API 可用，因此 CI 结果为实际读取而非猜测。

完整 `kubekey/scripts/check-code.sh` 实测 rc=0：tools / python modules / go.mod 工具链 /
role 任务形状与 shell 语法与无网络抓取 / go build（untagged 与 builtin）/ go vet 两种 /
C10 chart 准备 / `go test ./pkg/ani/...`（两种 tag）/ `./pkg/connector/... ./cmd/kk/...` /
7 个 `scripts/test-*.py` 行为套件（合计 205 个 case，0 失败）/ 发布脚本语法。
本地门禁各步实测：`go build ./...`、`go vet ./pkg/ani ./cmd/...`、
`go build -tags builtin ./cmd/kk`、`go vet -tags builtin`、
`go test ./pkg/ani`（无 tag 与 `-tags builtin` 各一次全绿）、`gofmt -l` 为空、
7 个 `scripts/test-*.py` 行为套件全绿。C10 与完整 `scripts/check-code.sh`、干净 checkout 复跑见 C10.md。

C07/C08 的 ansible 侧证据口径（如实收窄）：本机**未安装 ansible-playbook**，`--syntax-check` 无法执行。
role 改动的证据是三件：门禁自带的 ANI role 任务形状/键白名单/YAML 解析检查（13 个文件 0 失败）、
`bash -n` 全部 checker、以及 `TestC07_*` 与 `TestC08_*` 用安装器同一套 `text/template` 渲染后
**真实执行**脚本正文。这证明的是脚本内容与命令目标，不证明 ansible 会按预期把这些变量传下去——
那属于 live，本轮未运行。

本轮明确未做（不以旧记录代替）：三台实验节点／ESXi 的任何操作；现场 smoke／acceptance；
SQL 写入；删 Pod/PVC；重启；快照；抢锁；额度重置。修复后代码的现场验收保持待测。
open 项：metrics／fluent-bit 重型验收未接入受账本约束的正式入口（C04）；
新执行 role 的 connections 片段仍写进 base 的 canonical `connections.d`（代码注释与本台账均如实声明）；
C03 的 UID 选择器端到端语义需允许连集群时补测。

---

# 第二轮（2026-09-27，复核提交 153bada2 之后）

本轮范围：关闭复核确认的剩余实现——C03 真实 UID 前置、C08 清理、C07 上下文与片段隔离、C04 重型验收入口。

## C03 的更正：上一轮的“code_fixed”不成立，现在才成立

上一轮把删除改成 `kubectl delete pod <name> -n <ns> --field-selector metadata.uid=<uid> --ignore-not-found=false`，
并记为“UID 前置已进请求”。两处都不成立，且比“不够强”更糟：

- kubectl 的 delete 在有 name 时若设了 field-selector，Builder 先走 visitBySelector，
  其中 `len(b.names) != 0` 直接报 `name cannot be provided when a selector is specified`；
  该错误在 `Do()` 里不被 ContinueOnError 吞掉。也就是说这条命令**根本不会发出请求**：
  在真集群上 acceptance 永远失败，Pod 也永远没被删。我上一轮的测试之所以绿，是因为
  fake kubectl 自己用 sed 解释 `metadata.uid` 并扮演“服务端同意”，即测试断言的是命令行形状。
- 退一步，即便按纯 selector 删除，k8s v1.35 的 delete 走 LIST 再按 name 逐个删，
  `options := &metav1.DeleteOptions{}` 之后只设 PropagationPolicy，全文无 Preconditions；
  `--wait` 用的 uid 是删除**响应之后**取来等消失的，不是授权条件。
- 再看服务端：单个对象 DELETE 的 handler 从请求 body（或空 body 时的 query 解码）取 DeleteOptions
  后按 name 删；fieldSelector 只在 DeleteCollection 路径被消费。所以即使把
  `?fieldSelector=metadata.uid=…` 发给具名 DELETE，也是无条件删同名对象。

本轮实现（`kubekey/pkg/ani/pod_delete.go`）：用 go.mod 已直接依赖的 client-go 发具名 Pod DELETE，
`metav1.NewPreconditionDeleteOptions(uid)` 作为请求 body；不再有绕过前置的路径。
conflict/forbidden/notFound 记为新的终态账本状态 `refused`（确定未删，不得换 UID 重试），
timeout/cancel 记 `unknown`（不重放）。API 客户端在取产品锁与申领额度之前构造，
kubeconfig 不可加载时不消耗任何额度。原 controller/PVC/PV 授权检查、锁与 fsync 账本全部保留。

证据口径（区分请求集成与真实服务端）：测试让真实 client-go 打到本机隔离 HTTP 端点，
断言方法=DELETE、路径=/api/v1/namespaces/<ns>/pods/<name>、body 解码后的 `preconditions.uid`、
以及请求 Content-Type；409 由端点按文档语义返回并验证同名新对象未被删。
本轮无 envtest/真 apiserver 条件，因此这是**请求集成测试**，不声称证明 etcd 侧强制；
“本机无 kubectl 且未授权连集群”那条限制在上一轮是针对选择器支持，现改名为：真实服务端强制未测。

做这一步时由测试与源码核对新发现并修掉的两件事：
1) 由 kubeconfig 构造的 client 默认协商 `application/vnd.kubernetes.protobuf`，前置确实在请求里但
   非 JSON 解码器不可读，故显式钉 `application/json`；
2) 409 携带 reason `AlreadyExists` 不被 `apierrors.IsConflict` 命中（它只对未知 reason 回退到状态码），
   所以隔离端点必须回 `Conflict`。

红/绿：把删除换回上一轮的 kubectl argv 形态后，全部 acceptance 用例失败，且 fake 明确报
“a pod delete must go through the API client, not kubectl argv”；换回 client-go 实现后全绿。

## C08 清理：本轮完成，并修掉一个真实缺陷

`builtin/core/roles/ani/fluent-bit/templates/verify.sh`：
- 归属 UID 改为取**创建响应**返回值（`create_pod` 从 `kubectl apply --output jsonpath='{.metadata.uid}'` 取），
  不再创建后重新 GET 把后来同名对象的 UID 认领为本轮所有。
- 清理改为按**本轮自带 attempt 标签**做服务端选择删除，删后逐个复核记录过的 UID 确已消失。
- 读 UID 失败不再被 `|| true` 吞掉：失败即保留对象并写入 `CLEANUP_FAILURES`；
  `report_cleanup` 在有记录时让 run 失败并逐条打印，证据目录保留。
- 创建改为“清单先落盘成文件、再 `create_pod <name> <file>`”。原先 `cat <<EOF | create_pod` 让函数跑在子 shell 里，
  `OWNED` 关联数组的赋值根本不回到主 shell——**归属表实际一直是空的**，这是本轮测试直接抓出来的真 bug
  （另一处同类：heredoc 经命令替换也到不了 kubectl，创建会发空文档）。
- 顺带把 marker/post pod 的 `while [ \$i … ]` 计数循环换成 `sleep <N>`：反斜杠在严格 YAML 里是非法转义，
  门禁的清单解析器在我把清单改为落盘文件后开始真正解析它们并报错；改循环而不是放宽解析器。

测试：`TestC08_OwnershipIsProofFromTheCreateNotFromALaterRead` 经包内既有 `ANI_VERIFY_LIB_ONLY` 装配线
**source 真实 checker**后直接驱动 `create_pod/own_pod/release_pod/release_all_owned/report_cleanup`，覆盖：
创建响应无 UID 则不认领、只删本轮创建且带本轮标签的对象且不碰遗留探针、
同名 UID 已变则保留并点名新 UID、删成功但对象仍在必须报告、有清理失败时 run 必须失败。
`TestC08_OverlappingRunsNeverDeleteEachOthersProbes` 仍做并发整跑校验，并明确写出它能到与不能到的边界
（真集群才有的后端查询之前停止，故只建到 client 探针；marker/inspector 路径由上面那组覆盖）。

## C07 与连接片段隔离：本轮未实施

现状仍是：8 个 checker 已强制要求 `ANI_VERIFY_KUBECONFIG`（上轮改进，保留），但 role 仍把它
**硬编码**为 `/etc/kubernetes/admin.conf`；`--kubeconfig` 显式值未贯穿 Go→子 kk→role→Helm/kubectl→checker。
已核实这一项不能靠环境变量传递：SSH 连接器为远端命令从零构造环境（`pkg/connector/ssh_connector.go` 的
`varsToEnviron`），只导出 `http_proxy`/`KUBECONFIG`/`KUBERNETES_SERVICE_HOST/PORT`，父 kk 的 `os.Environ()` 不过去。
**这一段的两处事实在第三轮被实测推翻，见文末“第三轮”更正条；保留原文以便审计，不再作为改动依据。**
正确接法是走本仓真实使用的渲染上下文与 inventory 变量（`pkg/ani/config.go` 构造 `.ani/.kubernetes/.images`，
`pkg/kkims` 的 `kube_config` 已是 `/etc/kubernetes/<name>.conf` 的现成先例），
并同一条上下文承载 connections 片段/日志目录，使首装写 base 目录、组件新增写自己的 run 目录并从同一目录聚合
（今日 `writeConnections` 读 `<runtimeBaseDir>/<cluster>/work/connections.d`，即 base 规范目录）。
这需要改 inventory/role/checker 三层并配“两份互斥 dummy kubeconfig 跑真实渲染与命令执行链、断言最终命令目标”的回归；
本轮预算内未能完成，按未完成登记，不登记为 live_not_run，也不改注释结项。

## C04 metrics/fluent-bit 重型验收入口：本轮未实施

已核实的阻塞事实：`acceptanceTargets` 是 **每组件恰好一个** `acceptanceTarget`，一个 `Protocol` 字符串、
一个 Pod/PVC；而 fluent-bit 的旧重型脚本要重建后端 Pod 与 collector Pod 两处，`metrics` 段还含告警路由/静默与持久化，
一张“组件→单目标”的表表达不了多工作负载，硬塞等于给整段旧脚本发一张无限变更许可。
另有装配陷阱：`.ani.components.<name>.enabled` 在真实渲染上下文里**恒为 false**（Go 字段无 yaml tag，
渲染上下文由 `yaml.Marshal` 产生，键是 `certManager`/`metrics`/`logging`），
所以任何以它为条件的 role 在实机每次都会跳过——接通时必须用 `.backend`/`.metrics.enabled` 这类真实字段。
**这条在第三轮按真实生成器→落地 YAML→变量合并→条件求值实测推翻：`.enabled` 读到的就是站点写的值，
开启时门条件为 true。原判断不成立，因此没有修改任何 `when` 条件。**详见文末更正条。
本轮保留：batchv1.Job 构造与本地校验、显式未实现专项的拒绝与非通过汇总、全 pass 才 pass；
未删除功能、未叫用户手跑绕账本的脚本；多目标账本（每目标至多一次、别名/子 run/output 不得二次开额度）
与 RunVerify→真实协议函数集成测试待下一轮实现。

## 本轮门禁

`scripts/check-code.sh` 完整一次 rc=0，树指纹 `c641e6b14b1765083bb54effeef8eeea336405ad630787ff178540dc32aa38a4`
（gate/build/打包三处一致的机制未变）；包含本轮 C03/C08 改动。C10 的固定 Chart 准备与摘要校验保留、未改版本或批准 pin。

## 第三轮（2026-09-27）：C07 执行上下文与连接片段隔离、C08 探针创建与条件清理

本轮只交付这两项。C04 多目标重型验收入口保持“未实施”，需求未删除、未标完成。

### 更正两条既有记录（实测，非口径调整）

1. **`pkg/connector/ssh_connector.go` 不存在 `varsToEnviron`，SSH 也不导出 `KUBECONFIG`/`http_proxy`。**
   真实代码是 `buildSudoCommand`：远端只收到一条
   `TERM=dumb; export LANG=C.UTF-8; SUDO_USER=<user>; sudo -E <shell> -c "<渲染后的命令文本>"`，
   会话建立过程一个 `env` 请求都不发（`sshConnector.session()` 仅 `RequestPty`）。
   方向与原文相反的是**本地连接器**：`localConnector.ExecuteCommand` 用
   `sudo -SE <shell> -c <cmd>` 且 `SetEnv(append(os.Environ(), "SUDO_USER=…"))`，父 kk 的环境**会**跟着过去。
   结论仍是“不能靠环境变量承载运行上下文”，但理由变成：两条链对环境的处理天然不对称，
   唯一两者都无条件携带的是渲染进命令文本本身。
   证据：`TestC07_LocalAndSSHChainsCarryTheSameCommandAndNoInheritedTarget`（本进程注入 `KUBECONFIG=/parent-process/…`
   后，SSH exec 报文里既不出现该路径也没有 `env` 请求，本地记录到的环境里它就是父进程的值；
   两条链交给 shell 的 `-c` 脚本文本逐字节相等）、
   `TestC07_SSHPayloadCarriesNoFallbackCredentialLookup`、
   `TestC07_RemoteHostWithoutThePinnedKubeconfigIsStillGivenThePinnedPath`、
   `TestC07_FakeExecReportsTheEnvironmentItWasGiven`（守卫：断言 fake 真的看见父环境，否则不对称结论不成立）。
2. **“`.ani.components.<name>.enabled` 恒为 false”不成立，因此没有修改任何 `when` 条件。**
   `componentSpec` 显式构造 `map[string]any{"enabled": row.Enabled}`，不依赖 Go 字段的 yaml tag。
   实测链：站点 YAML → `LoadClusterConfig` → `KubeKeyConfig` → `writeYAML` → 真实 kk 加载器
   （`cmd/kk/app/options.CommonOptions.Complete`）→ `variable.New`+`GetAllVariable` 合并 →
   `api/project/v1` 解析真实 playbook → `tmpl.ParseBool` 求值：
   开启=true、关闭=false、开启但不在本轮 scope=false；三种结果由
   `TestC07_ComponentSwitchHasExactlyTheThreeStatesThePlaybooksDescribe` 记录。
   顺带实测到另一条与 playbook 注释相反的事实：**整块 `components_run` 缺失时，条件不是“取零值跳过”，
   而是 `index of untyped nil` 模板错误**（`ani_components.yaml` 原注释据此更正）。该组合在生产里不可达
   ——该 playbook 只由 components run 调用，scope 必然渲染出来——所以只改注释，不改条件。

### C07：一次确定的运行上下文 + 连接片段目录隔离 — code_fixed

新增 `pkg/ani/run_scope.go` 的 `RunScope{Kubeconfig, LogsDir, ConnectionsDir, RunID, KKBinary}`，
复用既有数据结构：由 `KubeKeyConfig` 直接构造并写进 `.ani.run`（不是某处 append `os.Environ`，也不是新增配置系统），
`Apply` 拒绝二次决定，`Replace` 只允许覆盖一个已存在的 scope。
- 首装：`InstallRunScope(cluster)` → admin.conf（由本次集群初始化产生）、`<base>/logs`、`<base>/work/connections.d`；
  runner 再把自身可执行文件补进同一 scope，并让 `runKubeKeyLogged`、`captureClusterIdentity`、`writeConnections` 都读它，
  三处此前硬编码 `/etc/kubernetes/admin.conf` 的调用点改为 scope 值；`verify.go`/`components_install.go`/`cmd/kk/app/builtin/ani.go`
  的字面量统一为 `DefaultKubeconfigPath` 单一来源。
- 组件新增：`ComponentsRunScope` → 命令行给定的 kubeconfig（已由 live-cluster 绑定校验背书）+ 自己的 `components-<run>/logs|work/connections.d`。
- 8 个组件 role + smoke role：connections 片段目录、日志目录、checker 的 `ANI_VERIFY_KUBECONFIG`、
  smoke 的 `KUBECONFIG_FILE` 全部改读 `{{ .ani.run.* }}`；96 处 kubectl/Helm 调用前置 `KUBECONFIG="{{ .ani.run.kubeconfig }}"`；
  每个 role 的第一个 task 是“本 run 执行上下文”守卫（文件不在本机就失败，不回落到 admin.conf）。
  ceph/envoy/kubeovn 三个 role 有意未改：它们在远端节点上跑，依赖节点自身的 `~/.kube/config`，不在本项边界内。
- 片段写入与聚合读同一目录：`writeConnections(dest, dir, rows)` 的 dir 由调用方从 scope 传入，
  组件 run 不再读写 base 的 `connections.d`，缺本轮片段直接失败而不借用旧片段，base 的 `run.json`/片段/`connections.md` 前后摘要不变。
- 证据：`TestC07_RoleTasksRenderTheRunsKubeconfigIntoEveryClusterCall`（9 个 role 的每一条 cluster 调用）、
  `TestC07_InstallScopeIsTheAdminConfigTheInstallCreates`、
  `TestC07_TheCheckerIsGivenTheRunsOwnExecutable`、
  `TestC07_ThePinnedContextDecidesWhichEndpointIsContactedAtAll`（两份互斥 dummy kubeconfig + 两个隔离 HTTP 端点：
  执行真实渲染出的 nats kubectl/Helm 文本，pin=A 时 A 端点收到与调用数相等的请求、B 端点 0 条，交换后镜像成立）、
  `TestC07_AKubeconfigMissingOnTheWritingHostFailsBeforeAnyWrite`、
  `TestC07_AComponentsRunWritesAndReadsOnlyItsOwnFragments`、`TestC07_OneRunCannotDecideItsContextTwice`。
  既有 `TestC07_CheckersHonourThePinnedKubeconfig`/`NoCheckerSilentlyFallsBackToAdminConf`/`ConflictingContextsAreRefusedNotGuessed` 保留。
- 未安装 Ansible：条件求值与任务构建用的是本仓真实执行器（`pkg/executor` 的 `dealWhen`/`converter.MarshalBlock`/`modules.FindModule`/`variable.Extension2String` 同一条路径）。

### C08：探针创建与条件清理 — code_fixed

`fluent-bit/templates/verify.sh`：
- **只创建语义**：`create_pod` 由 `kubectl apply -f` 改为 `kubectl create -f`；名字已被占用即失败且不认领任何东西，
  归属记录只写创建响应返回的 uid。`start_client` 同样先创建、失败即报“未认领”。
  测试侧 fake kubectl 对 `apply -f` 直接拒绝，回归会被抓住。
- **删除接到既有 kk/client-go 条件删除能力**：新增最窄内部入口 `kk ani pod-release --kubeconfig --namespace --pod --uid`
  （`pkg/ani/pod_release.go` → 复用 `pod_delete.go`，不另建服务、不复制凭据处理），
  checker 的 `release_pod`/`release_all_owned` 改为逐条按登记 uid 调它，
  不再有 GET-后-按名删除，不再有 `-l` 批量删除（标签只是辅助定位，不构成扩大删除集合的许可）。
- **答案分支不许含糊**：`deleted` 才结束义务（且只声称“删除被接受”，不声称“已验证消失”、不二次读取新 uid 重删）；
  `not_found` 记为“已不存在”，明确不叫“本轮删除”；`conflict`/`forbidden`/`timeout`/无法解析的答案保留对象、
  **保留 OWNED 归属**并写入 `CLEANUP_FAILURES`，`report_cleanup` 让 run 失败并保留证据目录。
  没有 `ANI_KK_BIN` 时不删除任何东西并报未完成——宁留探针也不按名删。
- 业务 Pod 重建（acceptance 段 [3]/[4] 的两次计划内删除）与本轮探针清理是两条独立路径，额度互不消费；
  它们仍是有名删除，属 C04 未接通范围，本轮不改动、不宣称完成。
- 证据：wire 级 `TestC08_PodReleaseSendsTheUIDAsAServerSidePrecondition`（断言隔离端点收到的 DELETE 请求体
  `preconditions.uid` 与 `Content-Type: application/json`，不是断言命令串）、
  `TestC08_AReplacementUnderTheSameNameIsRefusedAndNotRetried`（409 只发一次请求、对象不变）、
  `TestC08_AnAbsentPodIsReportedAsAbsentAndNeverAsDeleted`、
  `TestC08_ForbiddenAndUnreachableAnswersKeepTheObligation`、
  `TestC08_PodReleaseRefusesToInventATargetOrACondition`（缺任一入参时零请求到达端点）、
  `TestC08_PodReleaseHonoursContextCancellationBeforeTouchingTheServer`、
  `TestC08_PodReleaseCommandIsWiredAndRejectsAnUnconditionedCall`（checker 传的 flag 与 CLI 注册的 flag 一致）。
  脚本级 `TestC08_OwnershipIsProofFromTheCreateNotFromALaterRead` 现覆盖：创建响应无 UID 不认领、
  正常创建+清理、同名被占只创建失败、同名换 uid 被服务端拒绝且保留归属、forbidden/timeout/无法解析三种答案、
  404 只记“已不存在”、无入口时零删除、只创建不认领、成功答案不重复发删除；
  `TestC08_OverlappingRunsNeverDeleteEachOthersProbes` 保留并发整跑校验。
- **边界照实登记**：这些是客户端与隔离端点上的证明。只跑到 client 探针的整跑不等于完整 Loki/collector 验收；
  真 API server 是否在 etcd 层强制该前置条件、新增代码的实机 add-chain、连续离线冷启动仍为未测。

### 第三轮门禁

`bash scripts/check-code.sh` 完整一次 rc=0，树指纹 `13f7c394b6aa0aa62e568f0de6816c8265f82b2895e18223ff77967334d4ae67`（go1.26.7）。
本轮为把 C07 的上下文放进 role 而修正了 `scripts/test-ani-task-errors.py` 的两处测试装配：
模板占位符不再截掉整行（否则命令名一起被截掉，66 个多命令块全部无法注入），
以及纯 builtin 的上下文守卫改为“直接执行、要求非零且零写入”的正向断言——两者都是加强，未放宽解析器或跳过任何块。
C10 的固定 Chart 准备、lock 与版本/批准 pin 未改动。

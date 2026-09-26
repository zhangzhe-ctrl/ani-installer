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

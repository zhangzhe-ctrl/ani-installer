# ANI installer：已推送审查分支复审

审查日期：2026-09-27（Asia/Singapore）
仓库：zhangzhe-ctrl/ani-installer
审查分支：review/installer-f-remediation-20260926
固定提交：`ee8c4ccf4f6168d94c6abbafd13896f2d3af2061`
对照基线：`f58430e84a1949b837c59139dc45979c33ae5c4f`

## 结论与边界

用户手动推送已成功，远端审查分支存在，main仍在旧基线。无需继续处理agent的推送凭据。候选可以作为审查对象，但不建议在以下明确缺口关闭前合并并恢复自动实机推进。

本次实际读取GitHub固定提交的生产代码、测试、workflow、审查说明和对应CI日志。没有修改仓库、创建PR、运行用户Fedora命令或操作实验集群。并非穷尽检查上游KubeKey所有代码。

本地克隆因github.com DNS解析失败，沙箱Go为1.23.2，因此没有完整构建/测试该仓库，也没有降低go.mod。附带复现是从固定源码摘取的函数或控制流片段，加最小类型/外部命令替身；结果不能冒充完整产品或实机验收。本次确实执行了取消、Job结构、依赖范围、脚本路径及终态控制流复现。官方Go与Kubernetes文档仅用于核实API语义，未用其替代仓库代码。

以下P0/P1是整改优先级，不是CVSS或生产事故判断。一个选定组合成功不被否定，但不能据此推断其他分支正确。

## 1. 已核实的远端及CI

- 本次逐文件固定在ee8c4cc；结束时分支推进到8f5a1cbdb9fbd130929c28f016e706221be6e489。已核对两次新增提交62345d5、8f5a1cb的实际diff，均只修改docs/execution/REVIEW-20260926.md，未改生产代码、测试或workflow；本报告代码结论仍适用于该末次观察的分支内容。
- Actions run：36254508211；job：108438616076；对应head_sha=ee8c4cc。
- 实际结果：completed/failure，失败步骤为Run the code gate。
- 日志中普通build、builtin build和先行vet已成功；在普通pkg/ani测试时失败，后续完整门禁未运行完。
- 具体失败：TestChartSpecsMatchTheApprovedLockAndTheChartBytes，f_live_materials_test.go:70，缺少../../ani/charts/nats/2.14.6.tgz。
- 因此仓库内“预期CI失败”现在已有真实CI失败证据，但不能把其中负向fixture打印的acceptance fail当成新的集群事故。

## 2. 问题总览

| ID | 优先级 | 当前缺陷 | 证据类型 |
|---|---|---|---|
| C01 | P0 | 原首装记录的verify未做现场集群及变更执行主机绑定 | 生产调用链 |
| C02 | P1 | smoke的进程组Cancel回调没有真正注册，取消后子进程可继续写 | 源码＋隔离正反复现 |
| C03 | P1 | Pod删除未携带授权旧UID的服务端前置条件 | 生产调用链；未执行真实删除 |
| C04 | P1 | NATS专项Job的template层级错误；未实现专项可整体pass | 源码＋实际发射片段YAML解析 |
| C05 | P1 | 新增执行的依赖闭包比较错误，且noop跳过当前所有权复核 | 源码＋依赖范围隔离复现 |
| C06 | P1 | 合法的同配置noop被记录生产者/消费者拒绝 | 生产条件分支 |
| C07 | P1 | kubeconfig在Go→role→真实checker链上没有一致传递 | 源码＋fake命令轨迹 |
| C08 | P1 | Fluent Bit smoke删除固定共享探针，不是attempt独占资源 | 源码＋fake命令轨迹 |
| C09 | P1 | 成功状态早于成功记录持久化，写失败仍可能留下succeeded | 源码＋终态控制流复现 |
| C10 | P1 | CI依赖未供应的真实Chart；干净checkout不能复现本地门禁 | 真实GitHub CI＋workflow/测试 |

## C01：原首装记录verify不核对实时目标

位置：`kubekey/pkg/ani/verify.go` 的loadVerifyRunRecord、RunVerify、runAcceptanceScope；`run_manifest.go` 的ValidateSuccessRecord。

组件执行记录分支会调用ValidateComponentsExecutionBase并读取现场ClusterUID。原install-success分支却只调用纯字段检查ValidateSuccessRecord，随后直接进入smoke或acceptance。该纯函数只验证ClusterUID非空等内部字段，不读取当前kubeconfig所指集群；RunVerify的acceptance分支也没有复用组件执行已有的verifyInstallerExecutionHost。

结果：拿集群A的真实成功记录，给verify传集群B的kubeconfig，只要B恰有同名namespace/PostgreSQL/Pod/PVC，当前路径没有先把A的ClusterUID与B比较。专项可能先执行SQL再按名字删除B的Pod。此为代码风险，不表示已发生该事故，也不依赖攻击者伪造全部JSON。

另一个执行主机复制成功记录和kubeconfig后，可能使用另一把本地锁和另一份ledger；本地flock不能自行跨主机保护同一集群。

最小整改：两个成功记录类型共用现场身份解析；任何协议写入和专项变更前核对当前集群、实际目标和原安装主机。当前单安装节点拓扑可明确只允许原installerNode执行变更，无需设计分布式锁。变更取得产品锁后复核关键对象；只读查看失败记录不能报告成功验收。

回归：A记录+B上下文、相同名字不同ClusterUID、非安装主机执行、错误controller/PVC挂载都应零业务写入拒绝。测试要到RunVerify生产入口，而不只测试纯字段函数。

## C02：取消回调条件永远不满足

位置：`verify.go:runSmokeScope`。

```go
cmd := exec.CommandContext(ctx, "bash", script)
cmd.SysProcAttr = smokeSysProcAttr()
if cmd.Cancel == nil {
    cmd.Cancel = func() error { return smokeKillFunc(cmd) }
}
```

CommandContext本来就把Cancel设置为非nil的Process.Kill。因此这里不会设置预期的进程组终止函数。Setpgid只建立组，不会自动把默认Kill改成杀整个组。WaitDelay限制等待输出的时间，同样不等于取消遗留子进程。

隔离复现当前结构：`default_cancel_non_nil=true`、`group_callback_called=false`、`child_wrote_after_cancel=true`。仅将赋值改成无条件注册的正向对照：回调被调用、子进程没有写入。测试仅创建本沙箱自己的shell/子进程，不连接集群。

当前TestFRemediation_CancelledSmokeKillsStartedScript只检查父脚本末尾的finished没有出现；杀掉bash但留下sleep仍可以通过该断言。

最小整改：在启动前明确注册本任务进程组Cancel；保留有界收尾，不结束其他共享进程。回归必须让已启动子进程持有独立后续写动作，并验证取消后该动作不会发生、后代不会继续活动。不能用父脚本不走最后一行替代。

## C03：授权旧Pod UID没有进入DELETE请求

位置：`verify.go:runAcceptanceTarget`。

当前先读取旧UID，再立即重读相等，实际命令仍是：

```text
kubectl delete pod <name> -n <namespace> --wait=true --timeout=300s
```

该命令没有携带本次授权的oldPodUID。最后一次读取与真正删除之间发生同名替换，仍有误删新对象的窗口。即便客户端删除过程自己再读取对象，也不能把后来看到的新UID当作原授权。

当前测试只模拟UID在最后重读之前改变，未覆盖最后读取之后、DELETE提交之前的替换。owner只比ownerReferences[0].name，实际controller UID、Pod挂载的PVC/PV关系还没有完整核对。

最小整改：使用携带旧UID的DeleteOptions.Preconditions或等价服务端条件变更；冲突失败即停止，不重新取新UID继续删。补真实owner UID/controller标志及实际挂载关系的校验。隔离API在最后GET之后替换对象，断言旧UID前置条件仍随DELETE发送并被拒绝。禁止真实删除来复现。

## C04：NATS专项清单错误，且部分专项被跳过后仍整体成功

位置：`verify.go:runCheckJob`、acceptanceTargets、runAcceptanceScope、RunVerify。

实际字符串构造为：

```yaml
spec:
  backoffLimit: 0
template:
  metadata: ...
  spec: ...
```

template应位于spec.template。摘取生产发射片段并用YAML解析，实际结果是顶层template=true、spec.template=false。没有调用Kubernetes API；按Job结构，这不是有效的Job Pod模板。该helper用于NATS协议客户端；PostgreSQL走已有Pod中的psql，PG实机成功不能覆盖这一分支。

另外，acceptanceTargets只有postgresql/nats。指定metrics或fluent-bit时生成skipped结果，RunVerify只将fail折算成总体失败，所以可能打印`acceptance <组件>: skipped`，同时`overall: pass`和rc=0。旧重型shell测试被保留在acceptance分支，不代表正式Go入口已经接入了它们。

最小整改：使用类型化batchv1.Job构造并序列化，或正确修复整个YAML层级；测试实际发射结果的spec.template及containers，而非fake apply无条件成功。明确指定尚未实现的专项要拒绝或non-pass，不得“全部跳过等于通过”。原范围要求的重型测试接到受控路径，不能要求用户直接手跑绕过ledger的脚本。

无效清单目前还可能在记录专项意图后才发现；能本地确定的结构错误应提前拒绝，避免无意义消耗授权额度。既有090732的PG额度仍不可重置。

## C05：执行范围把“已满足依赖”判为错误；noop又跳过现场复核

位置：`components_install.go:componentInstallSpecs/componentsScope/RunComponentsExecute`；`components_record.go:componentsRecordTargets`。

实际opensearch声明InternalDeps=[fluent-bit]。计划如果是OpenSearch=planned、Fluent Bit=already_installed，execute先只抽取plannedNames=[opensearch]，再componentsScope展开完整依赖得到[fluent-bit,opensearch]，却要求它等于只有写入项目的plannedNames。该状态永远被拒绝，重新计划不改变事实。

隔离运行摘取的闭包及比较条件：两个都是planned时不拒绝；依赖已经安装、只新增opensearch时拒绝。当前混合scope测试是NATS和Valkey两个独立组件，不能证明有依赖的混合状态正确。

与此同时，所有权、CNI等fresh检查都放在`if len(plannedNames)>0`分支；全部already_installed时没有重新判断当前release/工作负载owner、version和健康。记录writer只是再读当前UID，把新的对象UID写入旧计划的already_installed声明。计划后release改版本/归属，不一定被这条noop路径拒绝。

最小整改：区分完整请求＋依赖集合与本次写入集合；在锁内复核完整集合，已满足依赖留在完整集合但不执行role。noop也重新检查当前owner/revision/version/健康及必要配置；有实质变化要求重计划。不要为了避免误拒绝直接排除依赖检查。

回归：依赖已存在时合法新增、依赖缺失时完整计划、计划后owner/version变化、纯noop变更、健康同版本noop零角色写入。

## C06：原配置完全相同的合法noop被拒绝

位置：`components_record.go:NewComponentsExecutionManifest/ValidateComponentsExecutionShape`。

两个位置无条件禁止`NewConfigDigest == base.ConfigDigest`。如果某组件已经随原首装安装，用户使用同一站点配置对它执行只读no-op，配置摘要理应相同；当前构造者反而认为“摘要没变，所以无事可记”，导致no-op结果无法落为可消费记录。

此前Valkey实机例子先在base中关闭Valkey、后续再打开，摘要不同，所以该例子通过不能覆盖原首装已存在组件场景。

最小整改：按操作语义判断。noop允许同摘要，仅表示当前观察，不能增加安装或变更权限。不要让用户改一个无关字段制造摘要变化。也不要将配置摘要变化本身当作实际新增成功的证明。

回归：同配置已安装组件的noop writer→loader→smoke成功；未授权新增、failed/unknown仍拒绝；原base字节和专项额度不变。

## C07：显式kubeconfig在真实role/checker边界失效

位置：`RunComponentsExecute`、NATS role及verify.sh、Fluent Bit verify.sh、runSmokeScope。

execute给子kk传KUBECONFIG=A，NATS role的kubectl/Helm可使用A，但role调用checker只设置输出目录；checker强制`--kubeconfig ${ANI_VERIFY_KUBECONFIG:-/etc/kubernetes/admin.conf}`，于是没有ANI变量时回到默认admin.conf，而非A。

反向的CLI smoke给ANI_VERIFY_KUBECONFIG=A；Fluent Bit脚本却使用裸`kubectl -n ...`，没有在最初的实际API调用前读取该变量，使用继承的KUBECONFIG/HOME。

fake外部命令轨迹确认上述变量选择与命令参数，不连接集群。这意味着“Go外层传入kubeconfig”不等于“实际安装和验证全程是同一目标”。

最小整改：一个已核准上下文贯穿kk、role、checker；所有kubectl/Helm调用明确接受它，拒绝不一致，不在不同层各自静默默认。测试设置两个不同的dummy配置，必须执行真实role/checker片段，逐条断言实际命令目标。

## C08：Fluent Bit probe不是本attempt独占，确实会删除共享对象

位置：`fluent-bit/templates/verify.sh:start_client`及[2]marker步骤。

CLIENT_POD固定为ani-fluent-bit-verify-client，start_client先delete再run。每个节点marker又固定命名ani-log-marker-1/2/3，先删除同名Pod再创建。RUN_ID虽用于消息内容，却没有用于这些Pod名。smoke会进入这一段。

两次/并发smoke可以互相删除对方的客户端和marker，也会删除上一attempt现场。隔离调用实际helper两遍，确认两次都删除同一固定client。尚不能据此证明历史那一次Fluent Bit偶发失败一定由此造成，但当前缺陷无需再猜。

最小整改：本attempt唯一资源名，记录UID并只清理本attempt拥有的探针；失败保留证据，既有历史对象不由本轮自动清理。保持真实日志采集断言，不增加泛化重试直到变绿。

## C09：写成功记录失败后，状态仍可能是succeeded

位置：`runner.go:RunInstall`的成功结尾及defer终态器；`run_manifest.go:WriteInstallSuccessRecord`。

当前在Build/WriteInstallSuccessRecord之前先设PhaseSucceeded、ResultSucceeded。后续成功记录写失败虽返回error，defer却因为`state.Result != ResultSucceeded`条件不成立而不纠正result，最终可留下“命令失败、状态成功”。最终WriteRunStateAtomic的调用仍有忽略错误，defer持久化失败也仅打印WARNING。

摘取终态控制流并模拟记录写失败，得到：`return_error=true persisted_result=succeeded persisted_phase=succeeded`。正常对照则正常成功。这不是完整RunInstall实装测试，但足以确认当前条件顺序错误。

最小整改：完成必要记录/状态落盘后才发布成功；各失败路径都以真实返回结果结算，不让提前赋值屏蔽错误。持久化失败必须影响返回值并报告已发生动作，不自动重复安装。复用现有状态模型，不再造一套状态机。补success-record写失败、最终state写失败、正常success的注入回归。

## C10：干净CI缺Chart物料，不应靠Skip解决

位置：`.github/workflows/ani-check.yaml`、`scripts/check-code.sh`、`f_live_materials_test.go:TestChartSpecsMatchTheApprovedLockAndTheChartBytes`。

CI已实际触发并失败，不再是“尚未推送”或预测。测试真实打开6个固定Chart压缩包；它们是外部锁定材料、未提交Git，workflow只有checkout、setup-go和gate，没有供应步骤。因此本地已有材料时绿，干净runner红。

建议选择最小可行方案：在正式gate前显式准备这6项固定Chart，按现有结构化锁的来源、版本及摘要验证，并落到测试声明的位置；缓存以内容摘要为键。获取发生在批准的CI材料准备阶段，测试本体与离线目标不隐式联网。必要时用已有go run纯材料入口，避开“必须先过gate才能构建下载工具，而gate又缺材料”的循环。

不需要把大型artifact提交到Git；小Chart也不被一概禁止vendoring，但不是唯一或默认修法。另一可行路线是分离纯代码与真实材料检查，并将真实材料检查设成必跑release/CI gate；不得只靠t.Skip、||true或删除测试令CI变绿。

回归：全新checkout＋空缓存完成批准准备及完整gate；错误摘要、缺失材料准确失败；只有当前候选对应GitHub run成功才能改写ci_pass。

## 3. 附加观察：不扩大成新一轮架构重做

1. 新增的connection.md输出文件确实是独立的，但NATS等role仍把片段写到base的canonical connections.d，execute又从base目录聚合。应通过当前运行上下文传递独立片段/日志目录；不要声称base所有工作文件都不变。没有证据表明base的connections.md本体被此路径直接覆盖。
2. acceptance意图O_EXCL创建后没有fsync文件/目录，finalize也没有完整持久化屏障。它对一般进程重复执行已有保护，但不宜把它宣传为经断电验证的严格持久账本；可随C03/C09定点补齐，不开展实机断电实验。
3. no-op记录、base身份与源/验证器代码身份区分、正式builtin构建、create-only Helm等是真实进展，应保留。不能因发现剩余问题否定此前所有实机结果。
4. 本次没有证据推翻已记录的Valkey空store材料内容验证，也没有重新下载它的全部镜像层；不将其重开为Hauler审批任务。

## 4. 有限整改顺序与退出条件

建议先在同一审查分支完成四组，而非重新派发R00–R16/F01–F12：

1. **核准执行对象和取消**：C01/C02/C03/C07。错误上下文、非原安装主机、UID竞争、取消后代均在隔离测试证明不越界。
2. **验证器可执行性和真实结论**：C04/C08。NATS有效Job、明确专项不静默skip-pass、probe按attempt隔离。
3. **新增与状态记录**：C05/C06/C09，附带独立connections目录。依赖已满足的新增、当前noop、同配置noop、失败落盘均有真实生产链测试。
4. **可重复交付**：C10。新checkout补准材料后跑完整gate，提交并推同一review分支，读取真实CI结果。

先实现及隔离回归，再按实际影响安排实机；本次审查不授权新的PG acceptance、Pod重建、节点重启、快照恢复、锁抢占或回滚。已消耗的090732额度保持不变。真实add记录链、持续离线冷启动等既有未覆盖项如实保留，不让它们被noop或旧日志替代，也不要求为每个纯函数修复重装三台机器。

## 5. 本次复现使用说明

证据包repros/内容：
- cancel.go / cancel.out：当前取消结构与无条件注册对照；只创建本沙箱的进程组。
- job_shape.go / job-current.yaml / job-shape.out：生产Job字符串构造片段与缩进结构对照，实际YAML解析。
- scope.go / scope.out：生产依赖展开函数加最小类型适配与execute比较，两个计划状态。
- script_paths.sh / script-paths.out：真实NATS头部与Fluent Bit helper，外部kubectl为fake记录器。
- terminal.go / terminal.out：生产终态控制流片段，模拟成功记录落盘失败，不执行文件或集群安装。

这些复现没有导入完整ANI模块，也没有使用完整生产类型图，不是可直接合并的修复patch。建议agent将同样触发条件落入真实仓库现有测试中，以生产函数为被测对象。取消复现的fix分支只是最小对照，不等于提供完整可提交的取消处理方案。

## 6. 固定来源

- [分支列表](https://api.github.com/repos/zhangzhe-ctrl/ani-installer/branches?per_page=100)
- [实际CI job](https://github.com/zhangzhe-ctrl/ani-installer/actions/runs/36254508211/job/108438616076)
- [审查期间文档变更1](https://github.com/zhangzhe-ctrl/ani-installer/commit/62345d52c82cb85b185a98ddc90f16d0f0f67290)
- [审查期间文档变更2](https://github.com/zhangzhe-ctrl/ani-installer/commit/8f5a1cbdb9fbd130929c28f016e706221be6e489)
- [候选审查说明](https://github.com/zhangzhe-ctrl/ani-installer/blob/ee8c4ccf4f6168d94c6abbafd13896f2d3af2061/docs/execution/REVIEW-20260926.md)
- [验证入口与专项](https://github.com/zhangzhe-ctrl/ani-installer/blob/ee8c4ccf4f6168d94c6abbafd13896f2d3af2061/kubekey/pkg/ani/verify.go)
- [成功记录](https://github.com/zhangzhe-ctrl/ani-installer/blob/ee8c4ccf4f6168d94c6abbafd13896f2d3af2061/kubekey/pkg/ani/run_manifest.go)
- [首装](https://github.com/zhangzhe-ctrl/ani-installer/blob/ee8c4ccf4f6168d94c6abbafd13896f2d3af2061/kubekey/pkg/ani/runner.go)
- [组件执行与依赖](https://github.com/zhangzhe-ctrl/ani-installer/blob/ee8c4ccf4f6168d94c6abbafd13896f2d3af2061/kubekey/pkg/ani/components_install.go)
- [组件记录](https://github.com/zhangzhe-ctrl/ani-installer/blob/ee8c4ccf4f6168d94c6abbafd13896f2d3af2061/kubekey/pkg/ani/components_record.go)
- [取消与UID现有测试](https://github.com/zhangzhe-ctrl/ani-installer/blob/ee8c4ccf4f6168d94c6abbafd13896f2d3af2061/kubekey/pkg/ani/f_remediation_g1_test.go)
- [验证测试fixture](https://github.com/zhangzhe-ctrl/ani-installer/blob/ee8c4ccf4f6168d94c6abbafd13896f2d3af2061/kubekey/pkg/ani/verify_dispatch_r13_test.go)
- [NATS role](https://github.com/zhangzhe-ctrl/ani-installer/blob/ee8c4ccf4f6168d94c6abbafd13896f2d3af2061/kubekey/builtin/core/roles/ani/nats/tasks/main.yaml)
- [NATS checker](https://github.com/zhangzhe-ctrl/ani-installer/blob/ee8c4ccf4f6168d94c6abbafd13896f2d3af2061/kubekey/builtin/core/roles/ani/nats/templates/verify.sh)
- [Fluent Bit checker](https://github.com/zhangzhe-ctrl/ani-installer/blob/ee8c4ccf4f6168d94c6abbafd13896f2d3af2061/kubekey/builtin/core/roles/ani/fluent-bit/templates/verify.sh)
- [Chart材料测试](https://github.com/zhangzhe-ctrl/ani-installer/blob/ee8c4ccf4f6168d94c6abbafd13896f2d3af2061/kubekey/pkg/ani/f_live_materials_test.go)
- [CI定义](https://github.com/zhangzhe-ctrl/ani-installer/blob/ee8c4ccf4f6168d94c6abbafd13896f2d3af2061/.github/workflows/ani-check.yaml)
- [Go CommandContext规范](https://pkg.go.dev/os/exec#CommandContext)
- [Kubernetes Job结构](https://kubernetes.io/docs/concepts/workloads/controllers/job/)

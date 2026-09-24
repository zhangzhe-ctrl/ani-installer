# 02｜现有问题详细整改执行计划

2026-09-24 / r1。以代码审查 A01–A15 为事实基础，展开为 R00–R16 共17张实施卡；新增任务入口与状态契约见[蓝图](01-installer-blueprint.md)。本文件是计划，不声称问题已修复。

## 先读：为什么这样拆

先清除越界恢复/错误吞没与凭据问题，再建立有效局部门禁，然后修配置/物料/网络，最后拆验证和新增组件入口。每张卡先做失败用例，完成最小修改，证明确实阻断；不要为一个checker参数错误先重装三台。

原审查的复现包里 PASS/confirmed=true 表示“缺陷被复现”，不是修复通过。原有已经通过的 apt/kubeconfig 测试继续保留，不重新报成当前缺陷。A01–A15 的编号不变；R编号是执行任务，不与历史实验A19等混用。[SRC-AUDIT]

本轮没有执行这些修改，也没有运行真实集群。人工命令分为现有命令和实施后命令；后者不能直接在原 c7a97bb 快照上使用。

## 任务依赖与完成标准

每卡先code_tested，再按需要material_verified/live_smoke/clean_offline_acceptance。缺环境只停止对应实机环节，不能把code pass写成整个任务通过。新命令统一遵守蓝图，不能同义另造三个不同入口。

| 任务 | 审查映射 | 内容 | 前置 |
|---|---|---|---|
| R00 | 基线/收官 | 冻结工作树、校准旧文档和执行环境 | 无 |
| R01 | A03 | 移除明文凭据，修正实验凭据使用方式 | R00 |
| R02 | A01 | 删除网络故障绕过，首错后停止变更 | R00 |
| R03 | A06 | 保证多命令 task 错误传播 | R00 |
| R04 | A13 | 建立唯一代码门禁和根级 CI | R02, R03 |
| R05 | A02 | Ceph 显式启用、磁盘授权和默认类保护 | R04 |
| R06 | A09,A10 | 统一配置解释，新增只读 validate/run 输入 | R04 |
| R07 | A07 | 让批准的材料锁约束真实内容 | R06 |
| R08 | A08 | 按启用项校验完整镜像集合和真实渲染 | R07 |
| R09 | A11 | 预检前移、运行状态和互斥 | R05, R08 |
| R10 | A05 | 修复 Kube-OVN CIDR/网关和地址校验 | R06, R08 |
| R11 | A04 | 两种主 CNI 都做真实网络验证 | R02, R10 |
| R12 | A12 | 让 SSH/API 等待真正响应超时和取消 | R04, R09 |
| R13 | A14 | 分离安装探测、smoke 和专项 acceptance | R02, R06, R11, R12 |
| R14 | A15 | 固定自举 registry 生命周期和冷拉验证 | R07, R09 |
| R15 | A14 | 最小新增组件入口，不重装底座 | R05, R08, R09, R13, R14 |
| R16 | 基线/收官 | 整改收官：真实离线验收与耗时记录 | R01, R02, R03, R04, R05, R06, R07, R08, R09, R10, R11, R12, R13, R14, R15 |

## 每次执行的固定步骤

阅读本卡与对应源码 → 记录源码摘要/既有修改 → 写失败fixture并实际复现 → 最小修改 → 运行具名测试及统一门禁 → 检查diff未越界 → 保存结果。仅卡片要求并满足实机前置时执行授权实验。不要一次改完多个独立问题后才整体测试。

R07和R15较大，已经明确拆分子任务，快速模型一次只做一个子任务。所有拟新增测试以本卡写出的名称落地；为防止 `go test -run` 无匹配却退出0，检查 `-v` 日志中出现所有预期用例，并在统一门禁验证覆盖清单。

---

# R00｜冻结工作树、校准旧文档和执行环境

**类型：**remediation　**状态：**待实施　**审查映射：**基线/收官新增任务　**前置：**无

> 一次只执行本卡指定子任务；目标命令/测试名若未实现，先实现再运行。禁止执行整份计划的全部卡。

## 目标
建立唯一源目录和任务基线；本卡只读校准与文档记录，不编译、不安装、不还原快照。

## 允许修改/核对范围
- `仓库根 README.md、既有 AGENTS.md（按实际层级）`
- `kubekey/AGENTS.md`
- `docs/execution/、kubekey/docs/progress.md`
- `ani/components.lock.yaml / images*.tsv（只读核对，不改摘要）`

## 逐步执行
1. 先读蓝图 §1–3、审查 A01–A15、已有版本表的实施状态。把 kcn Envoy 隔离、暂缓项、禁止清理补丁写进当前执行规则。
2. 在本地权威仓库记录 git status --short、HEAD；不 stash/reset/clean，不覆盖未提交修改。运行本包 source-snapshot.py，记录真实文件树摘要。
3. 通过 ssh fedora 核对系统、工具与既有目录。选一个本轮 Fedora 源码镜像目录；不复用 c2/c3/c4 三套门禁，不维持两个 releases 发射目录。本卡仅把核准路径和传输计划写入 lab.env；R01处理凭据后才实际同步源码，rsync 不带 --delete。
4. 针对旧 AGENTS “kcn 材料未到”与 components.lock.yaml 的 fix2 记录建立 status-decision.md：有实际归档并校验通过才能 material_verified；只有注释则材料待核。两者都不等于 live pass。
5. 旧矩阵“解耦 kcn Envoy”明确标为已撤销。HAProxy/kube-vip 在当前 local 控制面路径未使用，作为已有物料记录，不借这张卡启用。
6. 原审查 15 项保持 A 编号；新实施任务使用 R/B 编号，防止和旧实验 A19/A20 混淆。创建空的状态表，全部 not_started。

## 必须新增或保留的行为测试
T-R00-01：快照工具输出相同输入树时摘要相同；改一个临时文件则不同。
T-R00-02：文档和任务表没有把暂缓项列成自动依赖；kcn 专用 Envoy 与业务 Envoy分项。
T-R00-03：Fedora 源码与权威工作树摘要一致后才允许构建。git HEAD 相同但文件不同应拒绝混用。

## 人工操作命令
运行环境遵循04手册。以下新测试文件/函数必须在本卡先落地；命令中的环境变量由人工核验，不推测路径。`go test -run` 匹配0个测试不算通过，须检查 `-v` 输出的测试名称。

```bash
# 本地权威仓库；只读检查，输出存放到仓库外
cd "$REPO_LOCAL"
git status --short
git rev-parse HEAD
python3 "$PLAN_DIR/scripts/source-snapshot.py" "$REPO_LOCAL" "$EVIDENCE/source-baseline.json"
# 本地发起，Fedora 只读核对；缺工具只记录，不自动升级系统
ssh fedora 'uname -a; id; command -v go; go version; command -v helm; command -v skopeo'
```

## 结束标准
写明本轮 5 个绝对路径、三台白名单节点、代码/材料/现场状态及冲突裁决。只记录证据，不把旧日志抄成新验收。

## 失败时停止位置
目录解析不一致、已有未解释运行进程、授权节点不匹配时停止现场动作。不能为了统一路径删除旧工作区。

## 提交给负责人的结果
使用 `templates/task-result.yaml`，列出修改文件、实际执行命令、退出码、证据路径、代码/材料/实机状态。没有执行的项目写 not_run/not_verified。不要自动Git提交、push或对外发布；本地构建包按已有任务授权执行。


---

# R01｜移除明文凭据，修正实验凭据使用方式

**类型：**remediation　**状态：**待实施　**审查映射：**A03　**前置：**R00

> 一次只执行本卡指定子任务；目标命令/测试名若未实现，先实现再运行。禁止执行整份计划的全部卡。

## 目标
产品/计划/发布物不携带明文基础设施凭据；旧凭据轮换由负责人完成并记录。

## 允许修改/核对范围
- `restore_esxi_snapshots.sh:36`
- `已跟踪 docs/config 中含凭据的文件（仅精确定位修改）`
- `.gitignore、实验脚本的凭据读取入口`

## 逐步执行
1. 先用只输出文件名的扫描定位凭据，禁止把命中行及密码复制进 issue、聊天或报告。
2. 原 ESXI_PASS 明文默认改成必填环境变量/0600 文件。缺凭据必须在任何 ssh 前退出；不加通用默认密码。
3. 人工轮换相关旧凭据；只有代码修改时记录 credential_rotation=pending，不能假称已完成撤销。Git 历史清理需要负责人单独批准，不执行自动 filter-branch/force-push。
4. 所有 lab helper 禁止 set -x 输出秘密；sudo/SSH 使用已约定的密钥或受保护 ASKPASS，不在命令行字面量拼密码。
5. 从 code/artifact 打包清单排除 config 私密文件、askpass、节点密码、实验恢复脚本。凭据文档仅保留路径约定。
6. 检查 run_on_node.sh：上传 payload 必须成功后才能执行；使用每次唯一 payload 路径，原固定 /tmp/_ron.sh 与缺 errexit 不应让上传失败后运行旧脚本。这是本轮对 lab 交付安全的补充，不冒充原 A03 的已复現结论。

## 必须新增或保留的行为测试
T-R01-01：清空凭据环境，用 fake ssh（只记调用）执行参数预检，必须非零退出且 ssh 未被调用。
T-R01-02：扫描日志不得包含测试用秘密值；发布目录中不得出现凭据或快照脚本。
T-R01-03：模拟 payload 上传失败，后续远程执行次数必须为 0；并发 payload 不共用固定路径。

## 人工操作命令
运行环境遵循04手册。以下新测试文件/函数必须在本卡先落地；命令中的环境变量由人工核验，不推测路径。`go test -run` 匹配0个测试不算通过，须检查 `-v` 输出的测试名称。

```bash
# 仅输出疑似文件名，不输出密码行；误报人工判定
git -C "$REPO_LOCAL" grep -l -I -E '(ESXI_PASS=|password:|TOKEN=)' -- ':!*.sum' || test "$?" -eq 1
# 上述是辅助定位，不是完整秘密扫描器
# 新增测试后，在 Fedora Go 模块目录执行
python3 scripts/test-lab-credentials.py
```

## 结束标准
代码测试通过；源码/打包物未携带凭据；人工轮换状态单列，未轮换不得进行使用旧密码的实机恢复。

## 失败时停止位置
发现可能仍有效的凭据先限制继续传播；不能擅自修改 ESXi 管理账号或重写共享 Git 历史。

## 提交给负责人的结果
使用 `templates/task-result.yaml`，列出修改文件、实际执行命令、退出码、证据路径、代码/材料/实机状态。没有执行的项目写 not_run/not_verified。不要自动Git提交、push或对外发布；本地构建包按已有任务授权执行。


---

# R02｜删除网络故障绕过，首错后停止变更

**类型：**remediation　**状态：**待实施　**审查映射：**A01　**前置：**R00

> 一次只执行本卡指定子任务；目标命令/测试名若未实现，先实现再运行。禁止执行整份计划的全部卡。

## 目标
验收失败不能通过重启/删除 kcn、OVS、CNI 或重复重建组件来继续闯关。

## 允许修改/核对范围
- `kubekey/builtin/core/roles/ani/metrics/templates/verify.sh:102–152`
- `kubekey/builtin/core/roles/ani/fluent-bit/templates/verify.sh:434–507,603–627`
- `kubekey/scripts/verify.sh:133–153`
- `kubekey/pkg/ani/components_test.go`

## 逐步执行
1. 使用 rg 定位 k5_rebuild_wait、k5_read_again、k5_dp_agent_restart 及调用链。先保存其行为测试，不只删除函数名。
2. 用明确的 wait→失败取证→return 非零代替恢复链。计划内的一次重建先保留标记，R13 再移到 acceptance；绝不在超时后再次 delete/restart。
3. 删除 continuing 模式的假成功逻辑。必须成功的状态未达到就 fail；诊断收集失败不能覆盖原始退出码。
4. 顶层组件循环收到第一项失败，后续变更型验证记 not_run 并退出。只读 collect 可继续，但不执行其他组件的写测试。
5. 不改 kcn 镜像、CNI 配置和 Envoy release。不把原恢复逻辑藏进公共 helper。
6. 调整原字符串测试，防止它反过来要求保留旧绕过。

## 必须新增或保留的行为测试
T-R02-01：fake kubectl 的 wait 永远失败；脚本返回非零。记录中不得有针对 kcn-system/OVS/CNI 的 delete、patch、rollout restart。
T-R02-02：第一组件 checker fail；第二 checker 创建的标记文件必须不存在，结果 not_run。
T-R02-03：正常路径仍通过原实际 API 断言，不能把验证函数改成空函数。
T-R02-04：诊断命令也失败时，主结果仍为最初验证失败。

## 人工操作命令
运行环境遵循04手册。以下新测试文件/函数必须在本卡先落地；命令中的环境变量由人工核验，不推测路径。`go test -run` 匹配0个测试不算通过，须检查 `-v` 输出的测试名称。

```bash
# 定位；在 Fedora 源码模块目录
rg -n 'k5_rebuild_wait|k5_read_again|k5_dp_agent_restart|continuing' builtin/core/roles/ani scripts
# 新增行为测试并完成实现后
go test -count=1 ./pkg/ani -run 'TestVerifyStopsAfterFirstFailure|TestVerifyNeverRepairsNetwork'
```

## 结束标准
失败轨迹和成功轨迹都有局部执行证据；没有底层修復动作；不靠禁用断言获得 green。

## 失败时停止位置
实机暴露相同 kcn 故障就只读取证、组件缺陷记录；不能以“让本卡通过”为理由再加重启。

## 提交给负责人的结果
使用 `templates/task-result.yaml`，列出修改文件、实际执行命令、退出码、证据路径、代码/材料/实机状态。没有执行的项目写 not_run/not_verified。不要自动Git提交、push或对外发布；本地构建包按已有任务授权执行。


---

# R03｜保证多命令 task 错误传播

**类型：**remediation　**状态：**待实施　**审查映射：**A06　**前置：**R00

> 一次只执行本卡指定子任务；目标命令/测试名若未实现，先实现再运行。禁止执行整份计划的全部卡。

## 目标
第一条或中间命令失败，后续写操作不执行，错误在首个失败点暴露。

## 允许修改/核对范围
- `kubekey/builtin/core/roles/ani/ceph/tasks/main.yaml:28–40`
- `所有 ANI role 中的多命令 command 块`
- `相关 test fixture；不全局改上游 shell 语义`

## 逐步执行
1. 遍历 ANI role，列出包含换行、管道、分号、|| true 的 command 块，分类“必需操作/只读诊断/允许的特定错误”。
2. Ceph CRDs/common/CSI operator 的三次 apply 拆成三个 task，这是优先的最小改法。
3. 必须保持一个脚本的块，放到明确 bash 脚本并启用 set -euo pipefail；确认实际由 bash 执行。不要在默认 sh 中使用 pipefail。
4. 对条件语句、函数调用上下文导致 errexit 失效的情况显式检查退出码，不只机械插入 set -e。
5. 对 NotFound 等可接受场景单独识别错误，不使用宽泛 || true。保持原日志中具体命令和退出码。
6. 不把 Command 模块默认行为全局改掉，不在本卡重构 SSH。

## 必须新增或保留的行为测试
T-R03-01：第一/中间/最后 apply 分别返回 42，task 非零，失败后的写标记不存在。
T-R03-02：管道前段失败、后段成功，整体非零。
T-R03-03：诊断容错不让主体容错；无故障时仍执行全部必需步骤。

## 人工操作命令
运行环境遵循04手册。以下新测试文件/函数必须在本卡先落地；命令中的环境变量由人工核验，不推测路径。`go test -run` 匹配0个测试不算通过，须检查 `-v` 输出的测试名称。

```bash
# 首先人工看目标块
sed -n '20,55p' builtin/core/roles/ani/ceph/tasks/main.yaml
# 本卡新增测试后
python3 scripts/test-ani-task-errors.py
```

## 结束标准
每个修改块有注入错误的运行测试，而非只检查包含 set -e。

## 失败时停止位置
不明确是否允许失败的命令先保守失败，记录判定；不扩大成全部上游 KubeKey 改造。

## 提交给负责人的结果
使用 `templates/task-result.yaml`，列出修改文件、实际执行命令、退出码、证据路径、代码/材料/实机状态。没有执行的项目写 not_run/not_verified。不要自动Git提交、push或对外发布；本地构建包按已有任务授权执行。


---

# R04｜建立唯一代码门禁和根级 CI

**类型：**remediation　**状态：**待实施　**审查映射：**A13　**前置：**R02, R03

> 一次只执行本卡指定子任务；目标命令/测试名若未实现，先实现再运行。禁止执行整份计划的全部卡。

## 目标
代码、模板、检查器的低成本错误在构建发布前被真正执行的测试拦住。

## 允许修改/核对范围
- `.github/workflows/ani-check.yaml（仓库根新增）`
- `kubekey/scripts/check-code.sh（新增）`
- `kubekey/scripts/build-code.sh`
- `kubekey/pkg/ani/*_test.go、相关脚本测试`

## 逐步执行
1. 新建唯一 scripts/check-code.sh，集中调用实际存在的 Go/模板/shell/行为测试；缺依赖工具直接 fail，不自动安装或跳过。
2. Go 工具链满足 go.mod（当前最低 1.25.0），记录 Fedora 实际版本；不修改 go.mod 降级。网络下载只允许制包/准备环境，不允许离线安装阶段。
3. 根 .github/workflows/ani-check.yaml 设置 working-directory: kubekey，触发 paths 覆盖 pkg/cmd/builtin/ani/scripts、根 lab 和门禁自身。移除 kubesphere/kubekey 仓库限定。
4. build-code.sh 构建前执行同一个 check 入口；同一不可变源码树内测试、编译、封装，中间内容变化拒绝发布。不能只按 HEAD 复用检查凭证。
5. 保留 apt/kubeconfig 已有行为测试；把零散 render 门禁归入此入口，使用真实 KubeKey FuncMap 和真实配置上下文。
6. 未完成 R07 等后续任务的测试不以“永久 skip”掩盖：本卡只加入已可执行的门禁，后续卡同改实现与测试，CI 逐卡增长。
7. 原 audit_repros.py 中 confirmed=true 是错误复现，不能直接作为正确性测试成功条件。

## 必须新增或保留的行为测试
T-R04-01：仅修改模板/脚本也触发工作流；检查 workflow 工作目录真实存在。
T-R04-02：故意放入坏 heredoc YAML、checker 参数错、API fixture 错，check-code.sh 非零且 build-code.sh 不生成可发布包。
T-R04-03：GOTOOLCHAIN=local 且版本不满足时清楚失败；root/nonroot 测试按原约定运行，不破坏真实 HOME。
T-R04-04：测试后改源码，发布身份校验失败。

## 人工操作命令
运行环境遵循04手册。以下新测试文件/函数必须在本卡先落地；命令中的环境变量由人工核验，不推测路径。`go test -run` 匹配0个测试不算通过，须检查 `-v` 输出的测试名称。

```bash
# 新入口实现后，全部在 Fedora Go 模块目录执行
GOTOOLCHAIN=local bash scripts/check-code.sh
ANI_CODE_OUT="$CODE_OUT" bash scripts/build-code.sh
(cd "$CODE_OUT" && sha256sum -c SHA256SUMS)
# CODE_OUT 是本轮新目录，不得覆盖旧 release
```

## 结束标准
只保留一份门禁实现，本地与 CI 调同一入口；发布物明确对应被测文件树。

## 失败时停止位置
外部 CI 未接通只能记本地通过，不能写 CI 已通过；不为了修 CI 顺便升级全部依赖或运行实机。

## 提交给负责人的结果
使用 `templates/task-result.yaml`，列出修改文件、实际执行命令、退出码、证据路径、代码/材料/实机状态。没有执行的项目写 not_run/not_verified。不要自动Git提交、push或对外发布；本地构建包按已有任务授权执行。


---

# R05｜Ceph 显式启用、磁盘授权和默认类保护

**类型：**remediation　**状态：**待实施　**审查映射：**A02　**前置：**R04

> 一次只执行本卡指定子任务；目标命令/测试名若未实现，先实现再运行。禁止执行整份计划的全部卡。

## 目标
按照蓝图 §6.2 落实 storage.enabled/provider/nodes/default-class，不进行磁盘清理。

## 允许修改/核对范围
- `kubekey/pkg/ani/config.go、配置测试`
- `kubekey/builtin/core/playbooks/create_cluster.yaml:113–125`
- `kubekey/builtin/core/roles/ani/ceph/templates/cluster.yaml:34–69`
- `kubekey/builtin/core/roles/ani/ceph/tasks/main.yaml:128–137`
- `实验站点配置，不能改生产模板为实验固定盘`

## 逐步执行
1. 先加入字段和默认 false；测试 old profile=full 不再隐式等于 Ceph=true。显式旧实验配置增加 storage 选择。
2. Playbook 的 storageclass/ceph 用显式 storage.enabled 守卫，不让通用上游 storageclass 安装另一套默认存储。
3. CephCluster 渲染 useAllNodes=false、useAllDevices=false 和每节点 devices；移除 ^sdb$ 扫描式授权。设备名单必须来自预检确认的站点声明。
4. 前置只读检查 lsblk/findmnt/blkid/LVM/RAID 状态，沿父子设备链排除根盘与系统盘。识别不到设备身份也拒绝；不自动格式化已有签名。
5. makeDefaultStorageClass=false 时不修改任何默认标记；true 但另一个类默认时失败并指明冲突，不撤销别人的标记。
6. 存储消费组件显式用 storageClass；Ceph关闭时只校验外部类/后端能力，不要求本地授权盘。
7. 将空盘预检限定为新建 Ceph 的集群首装，不在 component-only 对已经被 Ceph 使用的盘报脏盘或尝试清理。

## 必须新增或保留的行为测试
T-R05-01：Ceph关闭时渲染中无 CephCluster、无默认类 patch、无磁盘动作。
T-R05-02：缺盘、根盘、子分区、已挂载、有签名、重复设备均前置失败。
T-R05-03：仅 node1 授权而三节点拓扑要求三块盘时拒绝，不能 useAllNodes 偷补。
T-R05-04：已有外部默认类不被修改；default=true 冲突有清楚错误。
T-R05-05：真实新装后 RBD 写读与 CephFS 跨节点共享读写单独验收，不能用 Pod Ready 替代。

## 人工操作命令
运行环境遵循04手册。以下新测试文件/函数必须在本卡先落地；命令中的环境变量由人工核验，不推测路径。`go test -run` 匹配0个测试不算通过，须检查 `-v` 输出的测试名称。

```bash
# 以下为只读人工核对示例，须在被授权的节点上执行
lsblk -o NAME,PATH,TYPE,SIZE,FSTYPE,MOUNTPOINTS,SERIAL,WWN
findmnt -rn -o SOURCE,TARGET
# 新测试实现后在 Fedora 执行
go test -count=1 ./pkg/ani -run 'TestCephRequiresExplicitSelection|TestCephDeviceAllowlist|TestDefaultStorageClassConflict'
```

## 结束标准
配置、模板和预检三者一致；没有隐式磁盘授权和默认类抢占。真实验收另记状态。

## 失败时停止位置
设备身份未知就停止，不通过清盘排除错误；不在本任务设计通用存储平台或存量迁移。

## 提交给负责人的结果
使用 `templates/task-result.yaml`，列出修改文件、实际执行命令、退出码、证据路径、代码/材料/实机状态。没有执行的项目写 not_run/not_verified。不要自动Git提交、push或对外发布；本地构建包按已有任务授权执行。


---

# R06｜统一配置解释，新增只读 validate/run 输入

**类型：**remediation　**状态：**待实施　**审查映射：**A09, A10　**前置：**R04

> 一次只执行本卡指定子任务；目标命令/测试名若未实现，先实现再运行。禁止执行整份计划的全部卡。

## 目标
同一 YAML 只有 Go 一套解释；不支持的安装节点组合在前置阶段拒绝。

## 允许修改/核对范围
- `kubekey/pkg/ani/config.go、config_test.go`
- `kubekey/pkg/ani/run_manifest.go（新增）`
- `kubekey/cmd/kk/app/builtin/ani.go`
- `kubekey/scripts/verify.sh:26–38`

## 逐步执行
1. 扩展 LoadClusterConfig 的严格解析，未知字段/枚举、重复键应报错；保留当前字段名称。按蓝图记录规范组件 ID。
2. Validate 强制 installerNode==nodes[0]；节点地址唯一、IPv4；profile=base 却要求安装被跳过组件时拒绝而不是静默跳过。
3. 新增有效配置结构，不含密码，包含 networkStack、installer地址、端口、profile、组件集合、StorageClass。JSON 由标准编码器生成，不拼接 shell。
4. 新增 kk ani validate --config --package-root --output；本卡先完成配置分支，材料验证在 R07接入。中间版本不能对外宣称完整 validate 已交付。
5. scripts/verify.sh 不再用 awk 读 YAML；最终作为 Go verify 的兼容包装。R13 尚未完成时保留清楚的中间限制，不假定新增命令已经工作。
6. 密码/SSH key 不写 run.json、公开日志、stdout；如保留私有输入副本必须 0600 并在证据导出时排除。
7. 配置 digest 对规范化非秘密输入计算；注释、引号、键顺序等价配置得到相同结果。

## 必须新增或保留的行为测试
T-R06-01：stack: kcn / stack: "kcn" / 单引号和注释/键顺序变换生成相同有效值和摘要。
T-R06-02：stack拼错、未知顶层字段、重复键、无效profile 均非零；不得默认视为 kubeovn。
T-R06-03：installerNode=node2 且 nodes[0]=node1，执行命令记录为空。
T-R06-04：run.json 和错误输出不含输入中测试用密码。

## 人工操作命令
运行环境遵循04手册。以下新测试文件/函数必须在本卡先落地；命令中的环境变量由人工核验，不推测路径。`go test -run` 匹配0个测试不算通过，须检查 `-v` 输出的测试名称。

```bash
# 当前可执行的基线测试
go test -count=1 ./pkg/ani
# 新入口完成且重新构建后（本卡材料分支完成前不得当完整验证）
"$CODE_OUT/kk" ani validate --config "$SITE_FILE" --package-root "$ARTIFACT_DIR" --output "$EVIDENCE/validate"
```

## 结束标准
合法等价 YAML 行为一致；任意节点配置不再接受后才失败；配置错误零现场写操作。

## 失败时停止位置
不通过改变用户 YAML 写法来修解析器，不在这张卡支持任意安装节点分发大包。

## 提交给负责人的结果
使用 `templates/task-result.yaml`，列出修改文件、实际执行命令、退出码、证据路径、代码/材料/实机状态。没有执行的项目写 not_run/not_verified。不要自动Git提交、push或对外发布；本地构建包按已有任务授权执行。


---

# R07｜让批准的材料锁约束真实内容

**类型：**remediation　**状态：**待实施　**审查映射：**A07　**前置：**R06

> 一次只执行本卡指定子任务；目标命令/测试名若未实现，先实现再运行。禁止执行整份计划的全部卡。

## 目标
能拒绝摘要自洽但来源/内容错误的包；不重算 SHA 来掩盖错误。

## 允许修改/核对范围
- `kubekey/pkg/ani/materials.go（新增）、images.go`
- `kubekey/scripts/build-offline.sh:112–214`
- `kubekey/pkg/ani/runner.go:487–510`
- `kubekey/ani/components.lock.yaml（按真实证据扩展 schema）`

## 逐步执行
拆成三个连续子任务，一次只做一项：R07.1 锁解析和纯函数校验；R07.2 制包接入和归档内容校验；R07.3 安装预检与 registry 实际内容比对。
1. 定义批准锁身份、文件材料、镜像身份/平台/localRef。保留源 index 与平台 manifest 的不同字段；本地 docker-archive 注入允许没有源 index，但必须记 archiveSHA 与转换证据。
2. 按锁精确枚举 Chart，不再 charts/**/*.tgz 全复制。验证 Chart 元数据和依赖包；原 tarball SHA 与必要展开文件都来自真实下载/检查。
3. 下载用 digest，或者拉取后核实同一 digest；镜像清单不能按数量合格。检查重复 original、local ref 冲突、非法 digest、平台和所需 layer/config 完整。
4. HAULER_ARCHIVE/STORE/EXTRA_IMAGE_TARS 每条路径都走内容校验；对已有归档不能只有 cp。Fedora 上使用隔离目录/临时 localhost 服务验证，绝不加载到生产 registry。
5. build-offline.sh 复制实际 CONFIG，不总是默认 package.yaml；记录其源文件摘要、编译工具摘要和材料来源。不因代码修改重包未变材料。
6. 安装前比较包摘要与独立批准 manifest；registry 启动后比较实际提供的 manifest/config/layers 与锁，再开始 kubeadm 或组件安装。
7. HTTP 200、归档能解压、总条数一致和 SHA256SUMS 自校验都只是子检查，不能单独作为材料合格。
8. 目标机完全离线；所有远端查询都在 Fedora 制包/核对阶段。未知摘要留 blocked，不编造补齐。

## 必须新增或保留的行为测试
T-R07-01：损坏归档、未知 Chart、Chart内容错、工具/ISO摘要错均失败。
T-R07-02：镜像数量正确但内容错误、同tag新内容、localRef冲突、缺layer，均失败。
T-R07-03：正确 amd64 镜像的 index digest 与平台 digest 不同仍可正确通过；不错误比较两者。
T-R07-04：本地 tar无index但有可验证材料链可以通过；错误RepoTag被提前拦住。
T-R07-05：攻击/误操作同时重算包内 SHA256SUMS，但不匹配独立批准锁，仍失败。
T-R07-06：CONFIG自定义后包内 metadata 与实际输入一致。

## 人工操作命令
运行环境遵循04手册。以下新测试文件/函数必须在本卡先落地；命令中的环境变量由人工核验，不推测路径。`go test -run` 匹配0个测试不算通过，须检查 `-v` 输出的测试名称。

```bash
# 现有工具，只在 Fedora 材料阶段；摘要来自批准锁，不填写假值
: "${SOURCE_IMAGE:?}" "${AMD64_DIGEST:?}"
skopeo inspect --raw "docker://${SOURCE_IMAGE}@${AMD64_DIGEST}" > "$EVIDENCE/source-manifest.json"
sha256sum "$EVIDENCE/source-manifest.json"
# 实现后的局部回归
go test -count=1 ./pkg/ani -run 'TestMaterial|TestImageTable|TestRegistryDigest'
```

## 结束标准
三条制包输入路径都有负向测试，正确包可复用，错误包无法靠重新生成校验和变合格。

## 失败时停止位置
上游重打标签/格式转换导致差异先保留证据、解释差异；不能拿另一个版本的摘要顶替。

## 提交给负责人的结果
使用 `templates/task-result.yaml`，列出修改文件、实际执行命令、退出码、证据路径、代码/材料/实机状态。没有执行的项目写 not_run/not_verified。不要自动Git提交、push或对外发布；本地构建包按已有任务授权执行。


---

# R08｜按启用项校验完整镜像集合和真实渲染

**类型：**remediation　**状态：**待实施　**审查映射：**A08　**前置：**R07

> 一次只执行本卡指定子任务；目标命令/测试名若未实现，先实现再运行。禁止执行整份计划的全部卡。

## 目标
缺运行镜像/验证镜像/Helm，全部在部署前失败；render 可以离线产生真实可检查资源。

## 允许修改/核对范围
- `kubekey/pkg/ani/config.go:411–439、images.go:146–169`
- `kubekey/pkg/converter/tmpl/template.go（仅必要扩展）`
- `kubekey/pkg/ani/*_test.go`
- `kubekey/cmd/kk/app/builtin/ani.go`
- `各 ANI role 的镜像与工具引用`

## 逐步执行
1. 建立两种主 CNI、Ceph、八项基础组件和其验证工具的 required material keys。依赖声明只使用现有真实镜像键，不用从名称拼tag。
2. logs 改成按 Loki/OpenSearch 分支筛选；未选后端不要求它的镜像，已声明基础大包可保留额外材料但不自动部署。
3. 检查模板内 index镜像取值：增加 requiredImage 或完整渲染前/后断言；仅 missingkey=error 不足以保护内置index。
4. 开启任一 Chart 时检查 bin/helm 可执行、版本和SHA；CRD/原清单路径也列为材料。所有 checker/test Pod引用纳入表。
5. 新增 kk ani render，使用生产 FuncMap、生产配置生成器，不再维护三套手写上下文。输出 tasks/values、helm template 结果以及测试资源。
6. heredoc 的产出也必须实际展开并过 YAML parser；嵌入 Python从独立文件/fixture 执行；不只 bash -n。
7. 检查未知/空 image、遗留 REPLACE、未允许外部镜像、重复 GVK/name、误入暂缓资源。保留官方 CRD，不手写删字段。
8. render 不访问 live API，不执行 apply/install；确需集群查询的值列为待实机预检项。

## 必须新增或保留的行为测试
T-R08-01：kubeovn配置+kcn-only包前置失败；反向同理。
T-R08-02：逐个删除 required 镜像/Chart/工具，校验必然失败。
T-R08-03：Loki-only没有OpenSearch镜像也通过；OpenSearch反向同理。
T-R08-04：空镜像map渲染不成功；坏heredoc、checker路径错、未定义变量都失败。
T-R08-05：两种主CNI、base/full、组件关闭/独立开启、日志互斥等小规模表驱动覆盖。

## 人工操作命令
运行环境遵循04手册。以下新测试文件/函数必须在本卡先落地；命令中的环境变量由人工核验，不推测路径。`go test -run` 匹配0个测试不算通过，须检查 `-v` 输出的测试名称。

```bash
# 新命令实现后在 Fedora执行，不连接集群
"$CODE_OUT/kk" ani render --config "$SITE_FILE" --package-root "$ARTIFACT_DIR" --output "$EVIDENCE/render"
# 这里只做补充人工检查；正式结果以语义检查器为准
rg -n '(<no value>|REPLACE_|image:[[:space:]]*$)' "$EVIDENCE/render" && exit 1
```

## 结束标准
真实 ANI 模板/Chart/测试资源都被检查；不把一个正则或字符串测试当完整渲染验收。

## 失败时停止位置
发现新组件未实施不临时给它补空实现；按开关拒绝，回到其批次卡。

## 提交给负责人的结果
使用 `templates/task-result.yaml`，列出修改文件、实际执行命令、退出码、证据路径、代码/材料/实机状态。没有执行的项目写 not_run/not_verified。不要自动Git提交、push或对外发布；本地构建包按已有任务授权执行。


---

# R09｜预检前移、运行状态和互斥

**类型：**remediation　**状态：**待实施　**审查映射：**A11　**前置：**R05, R08

> 一次只执行本卡指定子任务；目标命令/测试名若未实现，先实现再运行。禁止执行整份计划的全部卡。

## 目标
输入错误不能制造“必须还原三台”的现场；已变更/未知结果也不能被当作未变更重试。

## 允许修改/核对范围
- `kubekey/pkg/ani/runner.go:80–90,164–252,434–442`
- `kubekey/pkg/ani/preflight.go、run_manifest.go（新增/扩展）`
- `lab 单一实验锁入口`

## 逐步执行
1. 将配置、文件摘要、required材料、安装节点、已有unit/端口冲突、可用空间检查放在第一现场写操作之前。
2. 拆开证据目录和安装状态：预检失败可写新报告，但 changesStarted=false；下次修正输入允许新run，不靠目录存在一律要求快照。
3. 第一写操作前持锁并原子记录 installing/changesStarted=true。记录源树、kk、artifact锁、有效配置、目标三节点与集群身份。
4. 安装节点本地flock避免并发进程；Fedora实验flock覆盖快照/传输/安装/验收。占用直接返回，不删除锁文件、不杀进程。
5. 磁盘与网卡等只读预检在已有SSH连接器执行；只读操作也设置超时，未经检查不能进入写阶段。
6. 发生现场失败则 install_failed；SSH断开无可靠结果则 remote_result_unknown。只读诊断不改变原错误；禁止自动重放。
7. 旧run没有状态元数据只允许诊断，不推断可续装；本项目无真实用户迁移要求，首轮从干净实验基线产生新格式即可。

## 必须新增或保留的行为测试
T-R09-01：错误checksum/无Helm/缺镜像/unit冲突不执行远程写；日志可存在但 changesStarted=false。
T-R09-02：第二执行者不能运行；前者context取消释放本地锁但unknown状态仍阻止盲重试。
T-R09-03：第一写前状态落盘；写失败留下准确阶段，不将部分成功回滚成未发生。
T-R09-04：空间不足在Hauler导入前报错；不能自动删旧镜像腾空间。

## 人工操作命令
运行环境遵循04手册。以下新测试文件/函数必须在本卡先落地；命令中的环境变量由人工核验，不推测路径。`go test -run` 匹配0个测试不算通过，须检查 `-v` 输出的测试名称。

```bash
# 新版 validate，先运行再进行任何还原/安装
"$CODE_OUT/kk" ani validate --config "$SITE_FILE" --package-root "$ARTIFACT_DIR" --output "$EVIDENCE/validate"
# 核对JSON字段，不通过grep猜状态
python3 -m json.tool "$EVIDENCE/validate/validation.json"
```

## 结束标准
只读失败允许修正后重跑预检；变更失败明确要求实验流程处理，不自动“继续”。

## 失败时停止位置
不知道远端是否仍运行时只读调查；不删除runtime目录或重启机器作为通用解法。

## 提交给负责人的结果
使用 `templates/task-result.yaml`，列出修改文件、实际执行命令、退出码、证据路径、代码/材料/实机状态。没有执行的项目写 not_run/not_verified。不要自动Git提交、push或对外发布；本地构建包按已有任务授权执行。


---

# R10｜修复 Kube-OVN CIDR/网关和地址校验

**类型：**remediation　**状态：**待实施　**审查映射：**A05　**前置：**R06, R08

> 一次只执行本卡指定子任务；目标命令/测试名若未实现，先实现再运行。禁止执行整份计划的全部卡。

## 目标
非默认合法 Pod CIDR 可以正确生成网关；保留原管理链路，不在本卡接LB/Multus。

## 允许修改/核对范围
- `kubekey/pkg/ani/config.go:519–556`
- `kubekey/builtin/core/roles/ani/kubeovn/templates/kubeovn-install.yaml:7898–7921`
- `Kube-OVN 模板/配置测试`
- `实验网络隔离配置（与产品分开）`

## 逐步执行
1. 按蓝图 §6.3 添加 kubeovn.defaultGateway/joinCIDR，网关为空则从IPv4网段算首个可用地址。
2. 用 net/netip 判断网络地址、范围和重叠；拒绝/31、/32等不能满足本期Pod网的配置、越界网关、重复或非IPv4输入。
3. 替换模板中固定 10.16.0.1 和实验性 join 常量；渲染后断言它们来自有效配置。
4. 验证Pod、Service、join彼此不重叠，节点IP不在这三段；实机还核验管理网的连接路由，而非把默认路由0/0当作冲突。
5. 保留 managementInterface，不在overlay模式迁移主机管理地址/默认路由。物理接管模式不在此次最小修复范围。
6. enable-lb-svc仍默认false；Multus/LB有独立 B01任务。kcn字段不受影响，不触碰Envoy。
7. 实验断网规则按实际地址范围核验，不能反过来改产品网段迎合旧实验脚本。

## 必须新增或保留的行为测试
T-R10-01：10.244.0.0/16→10.244.0.1；默认10.16.0.0/16→10.16.0.1。
T-R10-02：网关来自另一个网段、Pod/Service重叠、join与管理IP冲突、IPv6、非规范网段输入按契约拒绝。
T-R10-03：LB关闭时没有LB资源，无任何Envoy新增/变更。
T-R10-04：真实非默认CIDR网络由R11验收，不仅看控制器Ready。

## 人工操作命令
运行环境遵循04手册。以下新测试文件/函数必须在本卡先落地；命令中的环境变量由人工核验，不推测路径。`go test -run` 匹配0个测试不算通过，须检查 `-v` 输出的测试名称。

```bash
# 新测试名称，须同卡实现后运行
go test -count=1 ./pkg/ani -run 'TestKubeOVNGateway|TestNetworkCIDROverlap|TestIPv4Only'
"$CODE_OUT/kk" ani render --config "$SITE_FILE" --package-root "$ARTIFACT_DIR" --output "$EVIDENCE/render-kubeovn"
```

## 结束标准
输入、派生值、模板、实际验证一致；合法非默认CIDR不再生成写死网关。

## 失败时停止位置
不能通过固定所有用户用10.16/16来绕过；不在已有集群热改Pod CIDR。

## 提交给负责人的结果
使用 `templates/task-result.yaml`，列出修改文件、实际执行命令、退出码、证据路径、代码/材料/实机状态。没有执行的项目写 not_run/not_verified。不要自动Git提交、push或对外发布；本地构建包按已有任务授权执行。


---

# R11｜两种主 CNI 都做真实网络验证

**类型：**remediation　**状态：**待实施　**审查映射：**A04　**前置：**R02, R10

> 一次只执行本卡指定子任务；目标命令/测试名若未实现，先实现再运行。禁止执行整份计划的全部卡。

## 目标
把通用Pod/Service/DNS探测与kcn专属Envoy探测分开，未测不写OK。

## 允许修改/核对范围
- `kubekey/scripts/verify.sh:56–78,156–161`
- `kubekey/builtin/core/roles/ani/smoke/templates/probe.sh`
- `新增独立通用 network-smoke checker 和fixture`

## 逐步执行
1. 从probe中识别纯网络操作与Envoy操作。纯网络实现放独立helper/脚本；原kcn专用Envoy安装及检查仍从kcn路径调用。
2. 生成本run专用namespace、跨两节点的server/client，所有镜像走本地锁；等待实际调度节点并断言节点不同。
3. 验证client→server PodIP、client→ClusterIP、DNS解析Service名称及解析结果的HTTP响应内容；不只ping节点或kubectl get pods。
4. 可按矩阵覆盖第三节点到其他节点；输出每对源/目的节点、结果、响应标记和时间。
5. Kube-OVN不得等待kcn-system/Envoy资源；kcn分支单独输出网络结果与专属Envoy结果。
6. 任何探测失败即保存只读事件/logs/对象status，退出；没执行就skipped/not_run，不打印统一ANI-NETWORK-OK。
7. 失败不清理现场；成功实验收尾按run标签和UID精准处理，不删用户资源。

## 必须新增或保留的行为测试
T-R11-01：节点Ready但DNS失败→最终fail；HTTP 200但body错误→fail。
T-R11-02：调度在同一节点→测试不成立/失败，不报告跨节点通过。
T-R11-03：选择kubeovn时fake kubectl轨迹不含Envoy/kcn查询或写动作。
T-R11-04：两个有效网络分支都输出独立结构化证据；未知stack直接拒绝。

## 人工操作命令
运行环境遵循04手册。以下新测试文件/函数必须在本卡先落地；命令中的环境变量由人工核验，不推测路径。`go test -run` 匹配0个测试不算通过，须检查 `-v` 输出的测试名称。

```bash
# 本卡新增局部行为测试
go test -count=1 ./pkg/ani -run 'TestNetworkProbe|TestKubeOVNDoesNotUseKCNEnvoy'
# R13完成后可从已知run发起通用验证；不能在R13前假定此命令存在
"$CODE_OUT/kk" ani verify --run "$RUN_JSON" --level smoke --only network
```

## 结束标准
有真实网络请求和内容断言；不存在未执行却pass；kcn Envoy隔离经负向测试证明。

## 失败时停止位置
网络问题属于组件或环境时交对应责任方；不在checker里清LSP、改路由或重启网络代理。

## 提交给负责人的结果
使用 `templates/task-result.yaml`，列出修改文件、实际执行命令、退出码、证据路径、代码/材料/实机状态。没有执行的项目写 not_run/not_verified。不要自动Git提交、push或对外发布；本地构建包按已有任务授权执行。


---

# R12｜让 SSH/API 等待真正响应超时和取消

**类型：**remediation　**状态：**待实施　**审查映射：**A12　**前置：**R04, R09

> 一次只执行本卡指定子任务；目标命令/测试名若未实现，先实现再运行。禁止执行整份计划的全部卡。

## 目标
用户不再无限等；本地超时与远端未发生不是一回事。

## 允许修改/核对范围
- `kubekey/pkg/connector/ssh_connector.go:311–382`
- `调用该连接器的已有单测/新增fake SSH server测试`
- `ANI checker 中kubectl/http等待入口`

## 逐步执行
1. 使用传入ctx，不再_ context.Context。执行完成/ctx.Done 两个分支，超时关闭本次session并释放管道读取；不要随意关闭其他任务共用的SSH client。
2. session.Wait和stdout/stderr收集需可结束；防止取消时等待goroutine卡在ReadByte，避免双重Wait/数据竞争。
3. fake SSH server覆盖永不退出、只写部分行、stdout持续、stderr持续、正常退出、取消竞态。
4. 报告远端操作可能已生效的unknown，不把本地context.DeadlineExceeded解释为“安全重试”。没有远端确认就不自动重放。
5. 给kubectl每次请求设置request-timeout，curl设置连接/总超时；阶段总deadline独立于重试次数。
6. 等待条件不满足输出最后condition/最近事件，不能机械增加timeout或重新启动组件。

## 必须新增或保留的行为测试
T-R12-01：fake会话不退出，短ctx后函数返回且资源释放，测试不访问实际Fedora/节点。
T-R12-02：go test -race目标包无本卡引入的并发问题；稳定重复运行不累积goroutine。
T-R12-03：远端成功但响应中断记录unknown，后续命令不重复发出。
T-R12-04：一次API请求卡住也受到总deadline限制。

## 人工操作命令
运行环境遵循04手册。以下新测试文件/函数必须在本卡先落地；命令中的环境变量由人工核验，不推测路径。`go test -run` 匹配0个测试不算通过，须检查 `-v` 输出的测试名称。

```bash
# 目标测试必须只连接本机fake服务
go test -count=1 ./pkg/connector -run 'TestSSHCommandCancellation|TestSSHCommandDeadline'
go test -race -count=1 ./pkg/connector -run 'TestSSHCommand'
```

## 结束标准
局部测试证明可取消、无泄漏、无重放；实机SSH断连专项另记not_verified直至真实执行。

## 失败时停止位置
本卡不写远程作业管理系统，不试图保证关闭SSH即杀死远端进程。

## 提交给负责人的结果
使用 `templates/task-result.yaml`，列出修改文件、实际执行命令、退出码、证据路径、代码/材料/实机状态。没有执行的项目写 not_run/not_verified。不要自动Git提交、push或对外发布；本地构建包按已有任务授权执行。


---

# R13｜分离安装探测、smoke 和专项 acceptance

**类型：**remediation　**状态：**待实施　**审查映射：**A14　**前置：**R02, R06, R11, R12

> 一次只执行本卡指定子任务；目标命令/测试名若未实现，先实现再运行。禁止执行整份计划的全部卡。

## 目标
普通安装不反复重建组件；专项持久化仍真实执行，结果可独立追踪。

## 允许修改/核对范围
- `kubekey/pkg/ani/verify.go（新增）`
- `kubekey/cmd/kk/app/builtin/ani.go`
- `各已实施role的tasks/main.yaml及verify脚本`
- `kubekey/scripts/verify.sh 兼容包装`

## 逐步执行
1. 逐组件列出现脚本中的read-only、独立test资源、组件Pod删除、恢复/重试操作，按蓝图§10分层。
2. role安装仅部署、必要等待、最小实际读写。未将安装读写成功标成persistence acceptance通过。
3. 新增Go verify分发器，按run记录读取组件、scope、identity，不另解析site YAML；兼容shell委托给新入口。
4. smoke默认不重建组件、不改全局告警路由；验收使用唯一runID对象、独立数据库键/桶前缀。
5. acceptance需要--allow-pod-recreate。每个预先声明目标只允许一次计划内重建，记录旧UID、新UID、所属controller和PVC UID；旧UID未消失不能认为重建完成。
6. 重建后验证原数据标记仍在、PVC未换；StatefulSet同名Pod不能用名字相同/不同判断。
7. 同一release专项结果唯一关联run；后续smoke不自动重复acceptance。已失败的mutation run不能带原参数继续闯关。
8. 从长脚本中仅抽小型wait/http/evidence helper，业务断言仍归组件；不引入shell DSL。

## 必须新增或保留的行为测试
T-R13-01：install与smoke轨迹均无组件Pod重建；acceptance无显式旗标被拒绝。
T-R13-02：旧/新Pod名字相同但UID不同判为重建；名字变了但数据丢失判fail。
T-R13-03：数据经Pod重建可读且PVC UID相同；删PVC后新建不能通过。
T-R13-04：首次失败后不再追加重建；其余变更型检查not_run。
T-R13-05：安装pass/acceptance fail能同时记录，不互相覆盖。

## 人工操作命令
运行环境遵循04手册。以下新测试文件/函数必须在本卡先落地；命令中的环境变量由人工核验，不推测路径。`go test -run` 匹配0个测试不算通过，须检查 `-v` 输出的测试名称。

```bash
# 新接口完成后，在目标安装节点使用；RUN_JSON必须指向真实成功安装记录
sudo "$CODE_ROOT/kk" ani verify --run "$RUN_JSON" --level smoke
# 仅授权实验环境；会执行声明的一次组件Pod重建
sudo "$CODE_ROOT/kk" ani verify --run "$RUN_JSON" --level acceptance --only postgresql,nats --allow-pod-recreate
```

## 结束标准
三层验证边界可测试，报告可区分代码/材料/安装/持久化/业务接入。

## 失败时停止位置
碰到CNI故障即fail；不得因acceptance失败改成skip后总绿。

## 提交给负责人的结果
使用 `templates/task-result.yaml`，列出修改文件、实际执行命令、退出码、证据路径、代码/材料/实机状态。没有执行的项目写 not_run/not_verified。不要自动Git提交、push或对外发布；本地构建包按已有任务授权执行。


---

# R14｜固定自举 registry 生命周期和冷拉验证

**类型：**remediation　**状态：**待实施　**审查映射：**A15　**前置：**R07, R09

> 一次只执行本卡指定子任务；目标命令/测试名若未实现，先实现再运行。禁止执行整份计划的全部卡。

## 目标
当前选择：原自举仓库在显式交接前持续保留；Harbor加入不会自动替换它。

## 允许修改/核对范围
- `kubekey/pkg/ani/runner.go:232–252,434–461`
- `registry unit模板/材料路径检查`
- `registry只读验证与实验冷启动场景`

## 逐步执行
1. 在新的首装中创建受管unit后启用开机启动，验证enabled+active；不接管名称相同但不属于本run的unit。
2. Hauler二进制、store、registry-data所在目录必须在重启后仍存在；不从临时目录启动。
3. 明确registry仅对授权内部网络开放，不将实验无认证HTTP源暴露公网；containerd镜像引用保持当前批准地址。
4. 安装过程中不停止唯一镜像源；failed run保留日志和物料目录，不自动清理。
5. R15只使用已供料registry；如缺新镜像，先准备新的批次底座，不临时替换已有store。
6. 真实实验对安装节点做一次计划内重启，核对服务自动恢复；选从未用于该节点的预留验证镜像证明确实发生blob传输。只设置imagePullPolicy=Always不等于证明layer缓存为空。
7. 不把这一单点registry描述为生产HA；后续Harbor交接独立计划。

## 必须新增或保留的行为测试
T-R14-01：生成unit具有持久路径，enable操作只发生于本run所有权正确的服务。
T-R14-02：重启后registry可用；manifest摘要正确；无缓存拉取有日志/字节证据。
T-R14-03：Harbor选中与否不隐式停/删原服务；新artifact路径冲突拒绝。
T-R14-04：未做真实冷启动只能写not_verified，不能用systemctl is-enabled替代。

## 人工操作命令
运行环境遵循04手册。以下新测试文件/函数必须在本卡先落地；命令中的环境变量由人工核验，不推测路径。`go test -run` 匹配0个测试不算通过，须检查 `-v` 输出的测试名称。

```bash
# 目标安装节点只读核对
systemctl is-enabled ani-image-registry.service
systemctl is-active ani-image-registry.service
systemctl show ani-image-registry.service -p ExecStart -p WorkingDirectory
# 实际重启在单独实验手册中执行；这里不提供自动重启命令
```

## 结束标准
生命周期在文档与实现一致；有真实冷拉/启动证据或明确未验记录。

## 失败时停止位置
安装节点重启范围不在当前授权时不得执行；不清containerd全部缓存制造测试条件。

## 提交给负责人的结果
使用 `templates/task-result.yaml`，列出修改文件、实际执行命令、退出码、证据路径、代码/材料/实机状态。没有执行的项目写 not_run/not_verified。不要自动Git提交、push或对外发布；本地构建包按已有任务授权执行。


---

# R15｜最小新增组件入口，不重装底座

**类型：**remediation　**状态：**待实施　**审查映射：**A14　**前置：**R05, R08, R09, R13, R14

> 一次只执行本卡指定子任务；目标命令/测试名若未实现，先实现再运行。禁止执行整份计划的全部卡。

## 目标
在健康、身份已知、材料齐备的ANI集群上新增组件；不做升级、卸载或主CNI变更。

## 允许修改/核对范围
- `kubekey/cmd/kk/app/builtin/ani.go`
- `kubekey/pkg/ani/components_install.go（新增）`
- `kubekey/builtin/core/playbooks/ani_components.yaml（新增）`
- `所选组件role与CLI/计划测试`

## 逐步执行
拆成R15.1纯计划/拒绝路径，R15.2执行所选现有role，R15.3实机一项组件新增，三步不能合并跳测。
1. 接入--only必填规范ID；未知/暂缓/未实现ID拒绝。配置中该ID未enabled也拒绝，防止命令暗改配置。
2. 读取原底座run和live集群身份/健康；验证namespace UID、Kubernetes版本、StorageClass/CRD owner、目标installer节点和原registry中所需镜像。
2a. 比较旧底座不变量（集群身份、节点、主CNI、底座版本、registry身份），不要求整个新配置hash等于旧run：新增组件开关必然改变有效配置。新旧hash分别记录；底座不变量改变则拒绝。
3. 只建立静态组件依赖闭包。技术内部依赖显示在plan；跨能力存储未满足则拒绝，不自动部署Ceph。
4. 独立playbook只列允许组件role，不import create_cluster，不运行kubeadm/cni/storageclass底座任务。
5. 已存在自有同版本组件记录already_installed并不执行upgrade；不同版本/非本工具所有权拒绝。首版不提供--force/--adopt。
6. 新run写自己的连接片段，输出汇总可引用已有组件信息，不覆盖旧run/旧连接说明。
7. 无新registry重启、无材料在线下载；材料不足返回依赖缺项，按蓝图§8.3准备本批底座。
8. Multus/LB的首次接入属于B01显式网络扩展，本版先通过首装配置执行，不放进通用component-only隐式改CNI配置。

## 必须新增或保留的行为测试
T-R15-01：only=nats仅执行nats及显式内部前置，不执行kubeadm/CNI/Ceph/Envoy。
T-R15-02：--only为空、未知项、deferred项、enabled=false、错误集群身份、缺材料全部零写失败。
T-R15-03：遇到已有别人的Helm release/CRD拒绝，不打ownership补丁。
T-R15-04：以底座→新增NATS为实机最小案例；之后既有底座UID/配置摘要不变。
T-R15-05：未选旧日志后端不被更新或重启。

## 人工操作命令
运行环境遵循04手册。以下新测试文件/函数必须在本卡先落地；命令中的环境变量由人工核验，不推测路径。`go test -run` 匹配0个测试不算通过，须检查 `-v` 输出的测试名称。

```bash
# 新入口完成后；目标安装节点、已健康底座、有材料
sudo "$CODE_ROOT/kk" ani components install --config "$SITE_FILE" --package-root "$ARTIFACT_DIR" --only nats
# 输出给出新run.json，之后再人工核验
sudo "$CODE_ROOT/kk" ani verify --run "$RUN_JSON" --level smoke --only nats
```

## 结束标准
有命令轨迹证明未触碰底座；单个新增组件成功并且无覆盖旧服务。

## 失败时停止位置
已有集群不健康/来源不明/版本不同就停止；不把组件新增扩为修复任意脏现场。

## 提交给负责人的结果
使用 `templates/task-result.yaml`，列出修改文件、实际执行命令、退出码、证据路径、代码/材料/实机状态。没有执行的项目写 not_run/not_verified。不要自动Git提交、push或对外发布；本地构建包按已有任务授权执行。


---

# R16｜整改收官：真实离线验收与耗时记录

**类型：**remediation　**状态：**待实施　**审查映射：**基线/收官新增任务　**前置：**R01, R02, R03, R04, R05, R06, R07, R08, R09, R10, R11, R12, R13, R14, R15

> 一次只执行本卡指定子任务；目标命令/测试名若未实现，先实现再运行。禁止执行整份计划的全部卡。

## 目标
证明修正后的既有能力仍可安装，新增组件入口不扰动底座，并准确记录未覆盖的组合。

## 允许修改/核对范围
- `docs/execution/status.yaml、验收报告`
- `lab/run-attempt与取证（仅实验目录）`
- `产品源码只在有明确新缺陷时另开任务修改`

## 逐步执行
1. 先检查所有R卡code门禁；冻结源码树/kk/artifact锁/site/实验ID，不在验收过程中改代码。
2. 执行手册04中的白名单、锁、凭据、快照和隔离检查。未经本轮核验不得照抄历史VMID/快照ID。
3. 为kubeovn与kcn分别记录支持矩阵；选择某一行进行完整干净首装。kcn材料未实际闭合就该行blocked，不阻断能独立验证的kubeovn行，也不把其他行标绿。
4. 基础组件：cert签发、PG/Valkey/NATS实际读写与NATS持久消费；metrics实查指标/告警；Loki与OpenSearch分别验证collector日志标记。两个后端不能同一站点同时开启。
5. 特殊场景：Ceph关闭/外部StorageClass、quoted YAML、非默认Pod CIDR先局部验证，再按选定覆盖矩阵实机。跨节点网络和磁盘授权不能只用mock代替。
6. 计划内一次Pod重建验证持久化；失败即停取证。冷启动registry另列独立实验，不混进所有组件安装。
7. 记录每阶段实际秒数、材料复用情况、失败发现阶段。单次局部checker修复不得先启动全量快照重装。
8. 最终完整首装pass后才能给对应版本/网络/配置组合签发验收结果；不是宣称所有组合都认证。

## 必须新增或保留的行为测试
T-R16-01：选定组合从原始干净快照完整安装；断网记录覆盖主机和工作负载，不依赖公网代理/镜像缓存掩盖缺项。
T-R16-02：健康底座新增单组件；原底座配置/UID不变。
T-R16-03：一次故障注入准确失败并停止变更，报告/日志可追到最初任务。
T-R16-04：Loki和OpenSearch的两条分支独立证明，不用一个分支替代另一个。

## 人工操作命令
运行环境遵循04手册。以下新测试文件/函数必须在本卡先落地；命令中的环境变量由人工核验，不推测路径。`go test -run` 匹配0个测试不算通过，须检查 `-v` 输出的测试名称。

```bash
# 先在Fedora只读确认发布链
(cd "$CODE_OUT" && sha256sum -c SHA256SUMS)
(cd "$ARTIFACT_DIR" && sha256sum -c SHA256SUMS)
# 实机命令及还原顺序严格见04手册，不从本卡直接启动旧run-attempt无限循环
```

## 结束标准
输出覆盖矩阵与每项证据；未验组合not_verified；确认没有新增网络绕过/临时线上下载。

## 失败时停止位置
同一失败签名再次出现而无新的代码/物料修正，不允许下一轮自动还原重装。先升级到具体问题诊断。

## 提交给负责人的结果
使用 `templates/task-result.yaml`，列出修改文件、实际执行命令、退出码、证据路径、代码/材料/实机状态。没有执行的项目写 not_run/not_verified。不要自动Git提交、push或对外发布；本地构建包按已有任务授权执行。


## 来源

SRC-AUDIT：上传《ANI-installer-code-audit-20260924.md》§A01–A15。所有行号仅对应上传快照，后续优先按函数/语义定位。具体源码快照身份见 `reference/source-baseline.json`。

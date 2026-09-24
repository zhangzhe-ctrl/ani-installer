# 04｜人工操作手册：从一张任务卡到一次可信验收

2026-09-24 / r1。**本文件是将来执行步骤，不是本次实际操作日志。** 本轮没有连接Fedora/目标集群；路径必须由R00核对后填写。允许的测试节点为`172.16.101.20`、`.21`、`.22`；任何其他目标都停止。

## 1. 区分三种终端

| 标记 | 所在位置 | 可以做什么 |
|---|---|---|
| LOCAL | 你当前的权威源码目录 | 编辑、看diff、记录指纹、传输源码；不假定Windows/Linux固定路径 |
| FEDORA | `ssh fedora`登录后的编译与实验控制机 | Go编译、模板/fixture测试、联网制包、lab锁、传输与实验编排 |
| TARGET | 实际`installerNode`，本期必须为nodes[0] | 运行已批准发布物、读取集群状态、执行本次明确验收 |

`kubekey/`是Go模块目录，`ani-installer/`是Git仓库根。命令里的`REPO_LOCAL`是仓库根；运行Go命令要进入`$FEDORA_SRC/kubekey`。

历史路径只用来帮助查找，不能直接当本轮事实：旧Fedora曾有`~/ani-installer-runs/obs-fix-20260919/src/kubekey`和两个releases目录。本轮选定一个源码目录和一个发布目录，在`lab.env`记录绝对路径。旧目录不删除、不覆盖未知内容。

## 2. 第一次只做R00，不操作集群

LOCAL：从`templates/lab.env.example.sh`复制成你自己的`lab.env`，放在仓库外。模板里的`REPLACE_...`必须替换；不要把私密密码加进Git。

```bash
set -euo pipefail
# LOCAL；这些变量由你核对后设置，不复制模板占位值执行
: "${REPO_LOCAL:?}" "${PLAN_DIR:?}" "${EVIDENCE:?}"
cd "$REPO_LOCAL"
git status --short
git rev-parse HEAD
python3 "$PLAN_DIR/scripts/source-snapshot.py" "$REPO_LOCAL" "$EVIDENCE/source-baseline.json"
# 只读探测，不安装软件
ssh fedora 'uname -a; id; command -v go; go version; command -v python3; command -v helm; command -v skopeo'
```

预期：得到有效源目录、HEAD和treeSHA256；Fedora可连；工具缺项是待办，不自动执行系统升级。若工作树有已有修改，记录归属，不`git reset --hard`、`stash`或`clean`。

Go工具链必须满足当前`go.mod`（上传快照要求1.25.0）。由已有Fedora工具链或批准的固定工具包供应；不为了通过而降低go.mod，也不临时下载浮动latest。

## 3. 复制源码，但不维持两个“权威副本”

R01先处理源码中的明文凭据。R00阶段可以只读核对/指纹；**未清理前不把含凭据的源码复制到新位置或打进交付包。** 旧历史归档按凭据轮换规则处理，本计划不自动重写Git历史。

LOCAL：先检查传输列表。以下rsync只是不带删除选项的范式，确切排除项要与R00指纹规则一致。`FEDORA_SRC`使用新建且已确认属于本轮的目录，不覆盖未知工作区。

```bash
set -euo pipefail
# LOCAL；先干跑，看将传输哪些文件
rsync -an --itemize-changes \
  --exclude='.git/' --exclude='releases/' --exclude='node_modules/' \
  --exclude='.venv/' --exclude='__pycache__/' --exclude='*.log' \
  "$REPO_LOCAL/" "fedora:$FEDORA_SRC/"
# 检查没有密码、kubeconfig、运行日志或大镜像后，才执行同一列表
rsync -a --itemize-changes \
  --exclude='.git/' --exclude='releases/' --exclude='node_modules/' \
  --exclude='.venv/' --exclude='__pycache__/' --exclude='*.log' \
  "$REPO_LOCAL/" "fedora:$FEDORA_SRC/"
```

不要加`--delete`；也不能因它没有删除旧文件就假定两端一致。对两端分别运行同一版本的`source-snapshot.py`，比较`treeSHA256`。如果旧文件残留导致不同，使用新的干净镜像目录；不要盲目批量删除。

指纹工具忽略构建输出/凭据型文件，**不是秘密扫描器**。R01单独完成秘密扫描和旧凭据轮换。需复制的计划包也先检查本包SHA，再记录`PLAN_DIR`在LOCAL/FEDORA上的不同路径。

## 4. 执行一张R卡的最小循环

先写任务ID、开始源码指纹、允许文件列表、预期失败用例。修改不超出这张卡；先运行新增负向测试，能看到修复前失败；再改实现，看到同用例通过。

FEDORA：

```bash
set -euo pipefail
cd "$FEDORA_SRC/kubekey"
# 举例：先按R10真正新增这些用例，然后运行。原仓库未必有同名测试。
go test -v -count=1 ./pkg/ani -run 'TestKubeOVN|TestNetworkCIDR'
```

检查输出包含实际`=== RUN`名称与断言，而不是`[no tests to run]`。只做字符串contains测试不足以通过R10；用真实模板渲染结果断言网关/CIDR。

R04完成之后，统一入口必须实际存在：

```bash
set -euo pipefail
# FEDORA；只有R04已完成才能运行
cd "$FEDORA_SRC/kubekey"
test -f scripts/check-code.sh
set -o pipefail
bash scripts/check-code.sh 2>&1 | tee "$EVIDENCE/check-code.log"
```

预期：退出码0、全部必测用例确实执行、没有未解释skip，结果关联当前treeSHA256。任一失败留在本地修正，不运行snapshot restore，不把集群当第一道语法检查。

同一轮改动涉及需要镜像/Chart变化时，转M任务；否则只构建代码，不重包几十个组件。

## 5. 发布代码：使用已有build-code.sh

上传代码中的环境变量为`GO_BIN`、`ANI_CODE_OUT`。**当前脚本未调用完整门禁，R04要补齐后才可作为可信发布入口。**

```bash
set -euo pipefail
# FEDORA；R04后，CODE_OUT是本次新的版本目录
cd "$FEDORA_SRC/kubekey"
: "${GO_BIN:?已核对的Go可执行路径}" "${CODE_OUT:?本次代码输出目录}"
GO_BIN="$GO_BIN" ANI_CODE_OUT="$CODE_OUT" bash scripts/build-code.sh
(cd "$CODE_OUT" && sha256sum --check SHA256SUMS)
"$CODE_OUT/kk" ani --help
"$CODE_OUT/kk" ani install --help
```

后续任务实现新入口后，逐一核对`validate --help`、`render --help`、`components install --help`、`verify --help`。缺入口代表该任务未完成，不造一个同名shell脚本绕过去。

本地发布记录至少保存：源码treeSHA、HEAD、Go版本、构建参数、kk SHA、CODE_OUT。新组件的`checks/<component>/`属于代码发布物；只修改检查器不能迫使重做大artifact。

## 6. 物料变化才制包

先在每批M阶段写物料清单：固定官方tag/commit、Chart和子Chart、静态与后续动态Pod镜像、工具、SDK依赖、测试模型/磁盘/数据集、摘要及支持范围。

下面变量名来自现有`build-offline.sh`；不是要求每轮都运行。路径和输入必须经过R07校验，不能以包内新生成的SHA256SUMS替代来源校验。

```bash
set -euo pipefail
# FEDORA联网材料阶段；使用本批审核过的实际路径
cd "$FEDORA_SRC/kubekey"
: "${PACKAGE_CONFIG:?}" "${IMAGES_TSV:?}" "${COMPONENT_LOCK:?}"
: "${HAULER_BIN:?}" "${HELM_BIN:?}" "${REPOSITORY_ISO:?}" "${CHARTS_DIR:?}"
: "${KK_BIN:?}" "${ARTIFACT_OUT:?}"
CONFIG="$PACKAGE_CONFIG" IMAGES_TSV="$IMAGES_TSV" \
COMPONENT_LOCK="$COMPONENT_LOCK" CHARTS_DIR="$CHARTS_DIR" \
HAULER_BIN="$HAULER_BIN" HELM_BIN="$HELM_BIN" \
REPOSITORY_ISO="$REPOSITORY_ISO" KK_BIN="$KK_BIN" \
ANI_ARTIFACT_OUT="$ARTIFACT_OUT" bash scripts/build-offline.sh
```

已有归档路径`KUBEKEY_ARTIFACT`、`HAULER_ARCHIVE`可按真实输入显式提供；它们**同样必须经过R07内容检查**。不要给一个任意tar后只运行`sha256sum --check`就认为材料通过。

包可能保留已声明而未启用镜像；未声明来源和多余Chart不应混入。先前研究登记中的摘要待采集项保持待采集，不复制旧版本SHA。

## 7. 首装前先validate/render，再申请一次实验运行

```bash
set -euo pipefail
# FEDORA或准备好的TARGET本地；R06/R07/R08/R09实现后
"$CODE_ROOT/kk" ani validate --config "$SITE_FILE" --package-root "$ARTIFACT_DIR" --output "$EVIDENCE/validate"
"$CODE_ROOT/kk" ani render --config "$SITE_FILE" --package-root "$ARTIFACT_DIR" --output "$EVIDENCE/render"
```

这是拟新增接口；作用仅为本地输入与渲染检查，不证明目标机器健康。检查输出：主CNI只一套；组件关闭项没有资源；Kube-OVN不调用kcn Envoy；没有意外Istio/LWS/GPU；磁盘只允许名单；所有镜像都映射至批准来源；没有密码出现在结果。

有错误就在此停止。**只读阶段失败不还原快照、不更换run目录骗过错误、不执行安装。**

## 8. 实验锁、快照和断网验证

这一节只用于已授权的三台测试机，不进入产品CLI。R01凭据整改与R12取消语义完成前，不扩大实验。

FEDORA在同一个shell里取得锁，覆盖取证、还原、传输、安装和验收整个过程：

```bash
set -euo pipefail
# FEDORA；LAB_LOCK_DIR须预先核对且属于本轮控制机
: "${LAB_LOCK_DIR:?}"
mkdir -p "$LAB_LOCK_DIR"
exec 9>"$LAB_LOCK_DIR/ani-three-node.lock"
flock -n 9 || { echo '已有实验运行，停止；不要删除锁文件或kill旧进程' >&2; exit 4; }
# 此后不要换一个未持锁shell启动写操作；FD9保持到本次结束
```

持锁不等于允许恢复未知现场。先只读确认三台IP对应的VM UUID/名称、当前快照、磁盘与允许损失的数据。旧记录中的VMID5/6/7、snapshotId1只能辅助核对，**不能直接当当前身份执行还原**。

现有`restore_esxi_snapshots.sh`没有在本次被确认支持`--dry-run`，所以文档不提供虚构的dry-run命令。R01应先建立lab-only映射核验入口或人工列表流程；它只列出身份/计划，不改变VM。缺凭据应在连接前失败；不得把密码默认值带回脚本。

触发一次restore必须同时有：固定任务ID、新的已通过局部测试的修正版、旧attempt证据已保存、三台与快照映射已核准、明确这是实验环境。用户此前同意该实验范围内按需恢复，不需要每一轮机械重复询问；但范围变化或映射不明就停止。

还原完成后重新核对SSH和主机网络，再使用已审阅的`~/ani-ops/apply_offline_isolation.sh apply`，随后`verify`。这是历史已有接口，先检查文件及其白名单、CIDR与本次配置一致；不能直接沿用过时网段。它必须保留SSH/集群内网/批准物料源路径，不删除默认路由或flush全部防火墙。

断网证据要显示：公网下载路径不可用，内部离线registry与所需DNS仍可用。容器已有缓存会掩盖缺材料，正式离线验收使用原始干净快照；**不要在工作集群清空全部containerd缓存模拟冷启动**。

## 9. 传输与执行：只在已核验的TARGET运行

通过当前已有的安全传输/askpass机制传代码包、站点配置、已核验artifact。密码文件在Fedora仓库外，权限受限；不要打印其内容或把值写在命令行。脚本`run_on_node.sh`在R01修复“上传失败仍运行旧/tmp脚本”之前不得用于写操作。

传输策略：artifact按内容ID复用；代码到新版本目录；站点配置单独受保护传输。先核对目标路径归属和容量，不覆写未知目录。目标重新计算摘要，与Fedora记录比对。

```bash
set -euo pipefail
# TARGET；这两个入口当前已经存在，但只有整改门禁满足后才能实机使用
: "${CODE_ROOT:?}" "${SITE_FILE:?}" "${ARTIFACT_DIR:?}"
(cd "$CODE_ROOT" && sha256sum --check SHA256SUMS)
sudo "$CODE_ROOT/install.sh" "$SITE_FILE" "$ARTIFACT_DIR"
# 等价底层入口是：
# sudo "$CODE_ROOT/kk" ani install --config "$SITE_FILE" --package-root "$ARTIFACT_DIR"
# 二者只选一个，绝不能连续执行两次首装。
```

安装后取新run.json的实际路径，检查安装结果。旧代码原有的高成本verify不能在R13前再次运行；整改后才执行：

```bash
set -euo pipefail
# TARGET；R13新增，按实际输出设置RUN_JSON
sudo "$CODE_ROOT/kk" ani verify --run "$RUN_JSON" --level smoke
# 专项验收必须列出本次批准重建的组件，下面以本批Milvus为例
sudo "$CODE_ROOT/kk" ani verify --run "$RUN_JSON" --level acceptance --only milvus --allow-pod-recreate
```

预期不是只看退出码：安装状态succeeded；smoke逐项pass；没有安装项目标skipped；acceptance列原Pod UID、新UID、同一PVC UID、数据读取断言。只安装了CPU组件就不能写GPU通过。

## 10. 新增组件的正确迭代，不每改一行都重装底座

前提：R15完成，健康底座和其run身份已知，**原registry已经供应本批材料**。为某批创建加速快照前，以包含本批材料的累计artifact装好健康底座；记录快照与材料身份。组件代码迭代从这个批次底座开始，不跑kubeadm/CNI/Ceph初始化。

```bash
set -euo pipefail
# TARGET；R15新增，此处只能在本批组件尚未安装时执行
sudo "$CODE_ROOT/kk" ani components install \
  --config "$SITE_FILE" --package-root "$ARTIFACT_DIR" --only milvus
sudo "$CODE_ROOT/kk" ani verify --run "$RUN_JSON" --level smoke --only milvus
```

如果registry缺材料，停在预检。首版不支持现场隐式注入/重启registry，不手工覆盖Hauler store。换成已验证本批材料的底座；以后需要增量物料更新时另开设计。

已经存在的同版本自有组件不被upgrade；不同版本/他人所有权拒绝。组件代码或配置错误导致现场失败，先本地修正并测，再恢复绑定本批的健康快照复验；**最终正式交付还需从原始干净快照完成整个所选组合**。

Multus/LB初次引入是B01明确网络扩展，本版通过新集群首装路径测试。不能借components install偷偷修改活跃主CNI。

## 11. 失败后的只读取证范式

先记录失败命令、退出码、开始时间、最后日志、run ID、source/kk/artifact/config身份。取证输出权限应为仅负责人可读，分享前脱敏。不要用`kubectl get secrets -A -o yaml`或完整敏感ConfigMap导出来图省事。

```bash
set -euo pipefail
# TARGET；只读，stdout放到私有证据目录
umask 077
kubectl --request-timeout=10s get nodes -o wide > "$EVIDENCE/nodes.txt"
kubectl --request-timeout=10s get pods -A -o wide > "$EVIDENCE/pods.txt"
kubectl --request-timeout=10s get events -A --sort-by=.lastTimestamp > "$EVIDENCE/events.txt"
# NS和POD从失败run读取，只取相关目标，不遍历Secret
kubectl --request-timeout=10s -n "$NS" describe pod "$POD" > "$EVIDENCE/pod-describe.txt"
kubectl --request-timeout=10s -n "$NS" logs "$POD" --all-containers --tail=300 > "$EVIDENCE/pod-logs.txt" 2>&1
```

日志本身仍可能有敏感值，自动归档不代表可以公开。某条只读取证失败，记录失败并继续其他只读检查；不得用成功的取证退出码覆盖最初安装/验收失败。

| 失败类型 | 下一步 | 禁止 |
|---|---|---|
| 配置/缺材料/预检 | 本地修配置或M材料后新run | 无理由恢复三台快照 |
| 模板/脚本/参数错误 | 最小fixture复现，R卡修复，局部测试通过后再实验 | 原地kubectl patch临时修过就宣布installer成功 |
| 组件自身缺陷 | 保存版本/事件/最小复现，交组件方 | 修改controller镜像、清理网络、重启底层“撑过去” |
| SSH中断/超时 | 标remote_result_unknown，只读确认远端状态 | 自动重放可能已生效命令 |
| 资源不足/硬件不支持 | 写blocked与具体缺项 | 临时开启emulation、换GPU驱动、增加另一套中间件 |

## 12. 每个attempt的记录，以及何时到下一张卡

一个attempt只引用一个源码treeSHA、kkSHA、artifactLockSHA、有效配置身份和快照身份。`commands`列表写真实运行机、cwd、命令、退出码、日志。分别记录源同步、局部测试、构建、传输、还原、底座、组件、smoke、acceptance的起止时间，不估算为实际耗时。

连续两次出现相同签名而没有新的因果解释，就停下补局部复现；不继续重复还原碰运气。成功后填`templates/task-result.yaml`，更新progress，只开放满足依赖的下一张卡。失败不自动勾done；`blocked`不是“失败被解决”。

所有计划内资源删除均限制本次创建/指定的准确对象及UID；不删除PVC/CRD作为恢复。实验收尾清理必须另列准确资源名单。无论成功与否，本轮都不自动Git commit/push、不对外发布、不启动下一批。

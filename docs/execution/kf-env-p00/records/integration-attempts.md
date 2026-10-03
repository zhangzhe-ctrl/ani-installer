# 保留现场联调记录

这些记录只属于开发联调，不进入最终干净首装的 EAC PASS。三台 VM 的 Id2 恢复仍未执行；用户已授权当前实验数据无需保留。阶段 C/D 通过后才执行恢复。

## 同源构建与闭合物料

- `1c45135ed3b090e01e131b31909f4b9810232906` 已推送；Fedora `build-code.sh` 真实退出码 0，统一门禁、编译、发布连续指纹 `1b690b7633c46db0b4e00980a93e3999fe93ccd19aee3321c7c0630bcd5dd006`。日志：构建任务根 `build-code-closed-assets-attempt-01.log`。
- 13 个角色/探针源文件、41 个实际离线 wheels、gRPC 客户端源码归档纳入源绑定物料；审批文件 SHA256 `2bd910cddadaba2bb50a41cacc0aa70a1b212cae3fb5deeed7a9a491182068e6`。gRPC 客户端离线重建与固定二进制字节相同，协议认证仍未执行。
- 保留两次首次门禁失败：`4278d5b` 缺工具源码归档摘要；`74517a6` 的物料测试仍固定四工具。修复后 `1c45135` 定向检查及统一门禁 PASS，未跳过检查。
- 完整旧基础物料在 Fedora `reference-artifacts-attempt-02/artifact-c165ae7-candidate-r2`，逐项原始 SHA256SUMS 验证通过；原清单 SHA256 `441f3bc8c4f081a8e660ce35fc3eaa5d05aaf257d3721f18b2b977692e020640`。104 镜像累计候选已通过既有实际 registry 内容门禁：15 个 pin-only 字节一致、75 个 lock+pin 字节一致、14 个来源 index 的 amd64 内容一致。完整包 `build-offline-materials-1c45135-attempt-01.log` 仍 FAIL，真实退出码 1：最后目录检查错误禁止已受审 Kubeflow manifests；保留失败输出，未补 SHA256SUMS 或发布 PASS。源修复只允许固定 release 的源绑定完整文件集、41 wheels 与固定 gRPC 源归档，其他目录、文件、篡改和自签审批继续拒绝。正式发布物尚未冻结。

## 现场安装尝试

固定联调集群 kube-system UID `87ecef8e-ac4e-442b-8e15-5e906263be6b`，kubeconfig `/etc/kubernetes/admin.conf`。所有写入使用真实内核产品锁 `/var/lib/ani-installer/ani-install.lock`。

1. 首次角色源码 `5052e81`：确认 77 次写入；CRD Established；等待控制器 600 秒后 FAIL，进程退出码 1。开发 registry 为 HTTP 5001，运行时尝试 HTTPS 导致 ErrImagePull。首次 shell 未落退出码文件，采用实际执行工具返回的进程退出码，不采用 tail 状态。
2. 同源重入：确认 15 次写入，退出码 1；空占位证书 Secret 与控制器生成的数据不同，被凭据轮换保护拒绝。仅两个固定控制器证书的空占位允许保留现有字节，其他凭据和替换行为继续拒绝；四项回归通过。
3. 源码 `1c45135`、独立目录 `integration-attempt-03`：包 SHA256 `cd797812084f7892364834c87e853e62389e936f20bdcb401b3ed1d20a322404`，站点 SHA256 `81d3c832e7cd55824e33da4cf460e9d1fb64d467e40926eecda65ad4e19319e8`。真实退出码 1，确认 70 次写入。webhook 干运行在原 create manager 的原子 rules 列表发生 SSA 冲突；现场规则已由 API 默认补入 `scope: "*"`，源码未显式给出。后续 S3、数据库、KFP 消费者未执行。修复在源码显式给出 API 默认值，不 force-conflicts，不修改节点源码，不删除控制器。
4. 角色 `c34bd9c` 的完整远程代码门禁与同源构建退出码 0，连续指纹 `1c77716bf9a8a7bba77b9b7a5b9c46844be440fa8e31bccdb5f9463bf4b5a2e3`。第四次节点目录 `integration-attempt-04` 包 SHA256 `34b9069c2f5a617bfbca90c2396a4432cdd7f1658d1061f8ba9aaff3258f5d37`。控制器/webhook CA 阶段通过，原 Pod UID 不变、重启数 0；随后 ValidatingAdmissionPolicy 服务端干运行拒绝 Quantity.sign()，退出码 1、确认 109 次写入。S3/数据库/KFP 消费者未写入。源修复将正数判断改为 compareTo(quantity('0')) > 0，保留原上界；安装在消费者之前等待策略 observedGeneration 和实际 typeChecking，无状态不能当通过。当前在线文档含更丰富的数量成员函数，现场编译器结果优先，不升级 API 或放宽边界凑通过；参考 [Kubernetes CEL](https://kubernetes.io/docs/reference/using-api/cel/)。
5. 角色 `7d6d8a7` 经远程定向代码检查、六项回归与现场策略 strict dry-run 后，在独立 `integration-attempt-05` 调用同一首装角色。包 SHA256 `95f9c04cc294726e18a8af0f0a51a1de9bd76e3287345bf5ebec4c83c0d72ef9`。真实退出码 1、确认 116 次写入；新策略的 observedGeneration=1，Trainer 无 typeChecking 告警，PVC 策略出现 requests 未定义告警，安装在 S3/数据库之前正确停止。当前 OpenAPI 的 Quantity 为 oneOf(string, number)，资源子树静态声明未暴露 requests；源修复仅将资源子树显式转为 dyn，实际 quantity 正数/上界继续判定，仍拒绝 typeChecking 告警。规则列表显式保留 scope 默认值。旧默认 kubectl-create 的 Update-owned 策略原子列表，即使同名 manager 的 SSA 也在服务端 dry-run 冲突；记录 `/home/ubuntu/kf-env-p00-policy-dryrun-attempt-02`，退出码 1，无持久写入。源修复仅对两个受管且无第三方 spec manager 的旧策略执行实际 UID+resourceVersion 条件 replace，保留 metadata，不 force-conflicts；以后创建统一使用 ani-kubeflow。
6. 角色 `a455c04` 的源绑定定向代码检查退出码 0；第六次 `integration-attempt-06` 包 SHA256 `cf9a47f67bf288557778422307a5f83b7eb3f9fdc98954b70ff9bbb05db8e85d`。真实退出码 1、确认 106 次写入。整个准入组预检在旧 Binding 的原子 `.spec.matchResources` 默认值差异处停止，策略新版本本次未写入，S3/数据库仍未执行。源修复显式 matchPolicy=Equivalent 和空 objectSelector/namespaceSelector 默认值，保持租户 selector；公共 prepare 方法从 apply 中原样提取，以实际同源代码预检全部十个准入资源，未新增产品入口或安装后追加能力。
7. 角色 `0a65b10` 的源绑定定向检查退出码 0；第七次 `integration-attempt-07` 包 SHA256 `4a9d8d73e86e5f0f031d3cc36c689dec4a5e9a625c01211aa273bc5836f0bb65`、站点 SHA256 `8b8e41df68e243570cc349a3bf846db5c6a2e3a6550501c2bf467381de681ee1`。整组十次实际服务器 dry-run 通过，无持久写入。持锁安装进行中，两个控制器原 Pod UID 不变、重启 0；工作卷策略 generation/observedGeneration=2，但清除原警告后 JSON 省略空 typeChecking，角色尚未推进 S3/数据库。实际 raw API managedFields 显示 validatingadmissionpolicy-status 的 Apply/status 在同一时间拥有 observedGeneration 和 typeChecking；其更新时间不早于 spec 更新。[固定 Kubernetes v1.35.8 状态控制器](https://github.com/kubernetes/kubernetes/blob/v1.35.8/pkg/controller/validatingadmissionpolicystatus/controller.go) 同次 ApplyStatus 提交二者。修复 `cea048e` 仅在该控制器归属、当前 generation 和更新时间证据齐备时识别空结果；裸 observedGeneration、过期时间、其他 manager 和实际告警继续拒绝。远程十四项回归退出码 0，实际当前 raw API 响应也经同源函数核验；资源正负向与最终环境验收仍未通过。

控制器原 Pod UID：JobSet `f6b035f0-cd2e-4800-b8e2-826d6f15f025`、Trainer `d0453fc7-82cf-4d06-a794-c123e9d1365c`；第三次调用前两者 Running、重启数 0。此观察不证明 TrainJob、入口或业务验收通过。

第七次最终按 600 秒等待上限退出，真实退出码 1、确认 116 次写入；S3/数据库/KFP 消费者仍未写入。外存归档 `integration-seventh-failure.tar` SHA256 `33f3260f8591cd975aec993dd9fe90ebc5663fb3bbd36ec9908826044a9ec84c`。`d8c404c` 在当前两租户实际执行十二次 PVC server dry-run，允许各自 5Gi RWX 无 ownerRef 的受管卷，明确由工作卷策略拒绝 6Gi、其他 StorageClass、错误命名、RWO 和 Workflow ownerReference；无持久创建。十四项回归及源绑定配置/默认关闭/物料定向 Go 检查退出码 0。

第八次源 `d8c404c1b35fb66d6b5b2c8182f6d317da48e2e8`；独立开发包 SHA256 `9cd7a7442fdd3605104506d3e4524a4fa9c5d6de61358125f78155f1a57426e0`、站点 SHA256 `7fb94e0abda502764a39f89d5b2b8ef94bd84b164cb9b81705bbbe15935ca877`。目标逐项清单核验、整组十次准入 dry-run 与十二次 PVC 正负向 dry-run 均通过后持产品锁调用同一角色。安装进行中，结果待实际落盘；不是正式同源发布物冻结或最终验收。

第八次实际退出码 1，确认 127 次 Kubernetes 写入；准入阶段通过，S3 三次写入（控制 bucket、所有权标记、受限账号）均 CONFIRMED。账号读回的父身份、账号类别、状态及 explicit policy 正确，但 RustFS 将 StringLike 的相同前缀集合重新排序，原 JSON 列表顺序比较误拒绝。只读诊断源码 `cc3fcc5`，无持久操作；修复 `87bac84` 只规范 Action/Resource/s3:prefix 集合顺序，保留全部字段与重复项，新增权限、广域 Resource/前缀、缺失 Condition、其他身份仍拒绝。真实第八次失败为 RED，十八项受影响回归和实际返回的等价策略比较为 GREEN；本轮首次 Git TLS EOF 后有一次远程定向检查先于推送完成，记录该偏差、不进入最终发布证明，随后核实同一 SHA 已推送，再做交付验证。外存原始失败归档 SHA256 `a9b4dc3d2a075132d8b362438ae247bc5339b1c02959de07682d0d4db7ffd898`。未轮换现有账号，数据库/KFP 消费者未创建。

前两次原始日志已移出 VM：Fedora `integration-first-failures.tar`，SHA256 `0ecb1cee90ee2143e42d80af266f579692812b36fa8b30ce5fb1e401f4dd16ed`。第三次外存归档 SHA256 `f7bcc1797fc594670dc5410a4cc12629a801fb1ece7148729142de934ab43ad1`。

`985f003eb6dd76301d9c515bb14defb0bae3db44` 的远程回归实际运行六项，退出码 0，日志 `targeted-webhook-replay-attempt-03.log`。回归覆盖两类 webhook 原子规则的 API 默认 scope 重入、显式 Namespaced scope 保留，以及既有四项证书保护。源绑定审批只变更 common.py 摘要和生成源码引用；新审批 SHA256 `f7eb88958ae20cc27477ec2d9f4e6209c7333d47a8cece9e9f0f614c8254380c`。修复后现场重入尚未执行。

## 执行偏差及证据限制

- 开发阶段使用明确的 HTTP registry 和 `ctr --plain-http` 缓存准备；没有修改 containerd 配置或重启服务。三节点 15 镜像 pull/CRI inspect 验证只属于开发准备。最终新集群必须独立证明正常 kubelet 拉取，不能使用继承缓存作为 PASS。
- `e2fc071` helper 在 ani-01 的第三次缓存准备先于该提交推送完成。Git TLS EOF 后只读核对远端，再推送并验证同一 SHA；之后其他节点动作使用已发布源码。该提前执行不进入最终发布/验收证据。
- 首次图表门禁误设 `ANI_CHART_CACHE_DIR`，实际变量为 `ANI_CHART_CACHE`，共享缓存被填充；保留原缓存，后续使用任务目录，未清理他人缓存。
- 两次慢速转移终止后保留了部分文件；`reference-artifacts-attempt-01` 不是完整物料，不用于构建。完整 attempt-02 经过实际清单校验。
- 首次 Hauler load 因 `/tmp/hauler3155932284` 配额失败，保留日志与独立 attempt-01 输出。显式任务 TMPDIR 和 `--tempdir` 后 attempt-02 退出码 0；这是环境失败，不是源物料损坏。
- 节点时间与 Fedora 有约 18 秒差，tar 解包输出 future timestamp 警告；逐项摘要通过。外部 UTC 仍 NOT_VERIFIED，未静音或调整时钟凑通过。

本轮必需验收仍有 NOT_RUN，`ENV_READY` 不成立；CPU-P01 业务未运行。

`4faade2` 的实际源绑定物料目录回归九项 PASS，退出码 0，日志 `material-layout-regression-attempt-01.log`。新完整构建候选使用 4faade2 的构建脚本与 c34bd9c 的受审 kk，二者分别记录；这是联调候选，不是最终同源冻结发布物。

`55e92205d445f99f0891596d835acb5efa74b26d` 的两条修复策略在 Fedora 生成，经审阅后在固定集群执行 strict server dry-run，真实退出码 0；输入 SHA256 `bdf7ce955de44afde3a3351a2d16650d4638652703cee4de9c6d3426db79adfe`，节点证据目录 `/home/ubuntu/kf-env-p00-policy-dryrun-attempt-01`。这仅证明当前 API 接受策略编译；安装后控制器实际 typeChecking 与资源正负向请求仍未通过。六项既有回归同时 PASS。

完整联调物料候选 `materials-candidate-4faade2-attempt-02` 的既有 build-offline 退出码 0，104 镜像实际内容门禁、固定工具/Chart/ISO/guest/scanner、源绑定 manifests 均通过。SHA256SUMS 文件摘要 `7b0852087e2da0a3d86a64356d2eb3a3de9af7f08403a1e87b9d0035c6d4c114`；镜像归档 `bed5b9793c861ffbe61369ffdee49aef1a2466fb071e371db97901db882e12c2`。后续策略源变更要求更新角色物料并在阶段 D 最终同源冻结。

`7e2b778` 的八项远程回归 PASS，退出码 0；相同实际策略 UID+resourceVersion 的 strict server-side replace dry-run 退出码 0，节点证据 `/home/ubuntu/kf-env-p00-policy-dryrun-attempt-03`。仅检查两个本任务创建响应 UID 的资源，第三方 spec manager 拒绝；没有持久 API 写入，也不宣称控制器实际类型检查已通过。

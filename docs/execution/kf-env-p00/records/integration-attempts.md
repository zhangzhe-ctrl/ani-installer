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

第九次源 `991b1ad`，包 SHA256 `ad45d1dce1dad13abbbb0305fa2264a49f572410e02ef84c072e247b7960c125`；真实退出码 1，134 次确认写入。三套 S3 身份认证通过，隔离 MySQL/PVC 创建。原容器无 readinessProbe，初始化期间被当作 Ready，首个认证查询退出码 1；稍后相同 Pod 认证 SELECT 1 返回 1，日志显示临时 socket-only 初始化服务器随后切换到最终 3306 服务。修复 `1451eb2` 给同一受管 MySQL 配置有界、带身份认证的 TCP startup/readiness 查询，继续复用原凭据/PVC，不修改共享 PostgreSQL。原失败归档 SHA256 `f50b6efa97ad5760ae6745ba3df1699eb7d6d7cf767d479f3b589acb381beb1b`，已转移到 Fedora 和本机任务附件。

第十次源 `0bbb832`，包 SHA256 `d83342ced57dd67da2ca913de6dbb5083748c31ec0dd15171fc6de15d36fdd26`；真实退出码 130，131 次确认写入。MySQL generation=2 的受审配置更新通过认证，PVC/凭据不变；MLMD 和 metadata Envoy 部署就绪。KFP API 明确 fatal：缺少 ML_PIPELINE_VISUALIZATIONSERVER_SERVICE_HOST。固定 e4ebca3 的 main.go 无条件注册旧 VisualizationService 并要求 host/port。首个可用完整脱敏容器日志已保存；确认致命错误后，核验当前任务 install.py 的精确 PID/cmdline，仅 SIGINT 中止其只读等待，异常处理与直接退出码均落盘；无未知写入，不删除 Pod，不干等剩余 deadline。修复 `6a84dee` 保持旧功能目标为未监听的 127.0.0.1:9，成熟入口拒绝对应 HTTP/gRPC 路由；不新建 Service 或部署被排除组件。只跑改动对应行为测试；最终仍须真实 API 启动和 Run 验证。归档 SHA256 `17a199b4b0702c1077e3d298d1e6d2c68f7d02bf6f9c8e00bea390bf8bc7c9ef`，已转移到 Fedora 和本机任务附件。

第十一次源 `5beb433`，包 SHA256 `6b214c09127fc92b5a9391f8ffcd30c7b3a58b47f417e70126d57baf071a3f0a`，退出码 1、130 次确认写入。消费者整组 dry-run 被旧 Downward API 原子字段默认值阻断；`a6e0339` 在公共 prepare 显式补充 fieldRef.apiVersion=v1，定向重入测试通过，没有 force-conflicts。

第十二次源 `3f2d4dd`，包 SHA256 `154dd652682d93a9c43499fa70609e99367b25c3a0466500eacf7b2ef1f95288`，退出码 1、135 次确认写入。KFP API 与全部消费者首次就绪，但入口整组 dry-run 被已有 ANI Envoy 服务占用的 30443 阻断，入口本次未写入。只读核实 30445/30446 空闲，站点显式改用这两个端口；`1e3d7d8` 将端口冲突检查前移到任何写入之前，两项定向测试通过，原 ANI 服务保留。

第十三次源 `f4f8972`，包 SHA256 `616c2f856999964caca208f2cb16cdd3fde61f43caa7a275a6b2373737c1a266`，站点 SHA256 `f748344c1295ae565cdcf3175143440d2d482bce34198da0455030fc5565ce5d`，退出码 130、140 次确认写入、未知写入 0。入口创建、证书签发通过；Nginx 在只读根目录创建默认 fastcgi_temp 时明确退出。核验本任务 Python PID/cmdline 后仅中断其只读等待，原 Pod 保留。`ddbdf20` 显式将三个剩余临时目录放入既有 /tmp 卷，并用配置摘要触发受管入口配置更新；Fedora 锁定镜像、非 root、无额外权限、只读根文件系统的真实 nginx -t 退出码 0。首次测试调用因 Podman 不接受 tmpfs uid 选项退出 125，保留原日志，改用共享可写 tmpfs 模式后通过；没有改变产品安全配置。

第十一至十三次原始日志合并归档 `integration-11-13-first-failures.tar`，SHA256 `17e6a63c8ea2203251ce377177f2f3932910f4a9ac710f8b3b9f8bddab0af3c5`，Fedora 和本机附件摘要均复核通过。第十四次源 `807f49c`，包 SHA256 `1952541943aee446f3177c907d5c361ff2de5f13b868dcade2bb57ee55e7fa3c`、站点 SHA256 `712f08b15c827ca08d8ccee6168a81da425b14b2b4c379c4b80d6dcf4ce0d44f`，退出码 1、135 次确认写入、未知写入 0。入口组 dry-run 在 ConfigMap 的 Update/Apply 字段归属处失败；入口新配置和 Deployment 本次均未写入。`0ffc5fb` 只对 kubeflow/ani-kfp-entry 配置执行 UID/resourceVersion 条件更新，保留元数据，并拒绝外部 data manager、额外 data 或 binaryData。三项受影响测试远程通过；第十五次源 `bd023a5` 正在同一现场持锁执行。尚无 Pipeline Run 或 TrainJob 验收，不设 ENV_READY。

前两次原始日志已移出 VM：Fedora `integration-first-failures.tar`，SHA256 `0ecb1cee90ee2143e42d80af266f579692812b36fa8b30ce5fb1e401f4dd16ed`。第三次外存归档 SHA256 `f7bcc1797fc594670dc5410a4cc12629a801fb1ece7148729142de934ab43ad1`。

第十五次源 `bd023a5`，包 SHA256 `c88877d4f49114fbf95d67b932e5fcbabd0700f611f2ac6c5d64f55b3574d4c9`、站点 SHA256 `ba0745c5a57c4ea7f1b7180b5de618765953cee5f08629eee65349986e59cfbe`，角色真实退出码 0、141 次确认写入、未知写入 0，终态 INSTALLED_PENDING_CURRENT_CHECK。入口新 Pod 就绪、重启 0，Runtime `ani-single-process-v1-7dd0ba78` 创建。当前控制检查退出码 0、CONTROL_PLANE_CHECKED：逐项归属、固定运行摘要、EndpointSlice、TLS HTTP、无身份及伪造身份拒绝、A 身份访问 B 拒绝、gRPC h2、Runtime spec 和两租户 TrainJob dry-run 通过。gRPC 授权、完整 EAC 和干净首装未由该检查证明。

首个真实主链客户端在 Fedora 锁定 SDK 镜像执行，namespace ani-kfp-probe-a，短时效 API/KFP 两种 audience 身份经私有文件注入，产品锁的实际持有者经核验。客户端退出码 1；PVC `42abe074-e0eb-402c-9678-8ea798b6ea13` 创建并绑定，Experiment `1fee684b-7f49-428d-a30b-d8925492661b`、Run `aa9cab42-5fd3-43ea-81a0-9a0475e6b6ac` 均为确认创建响应。Workflow `ani-environment-handoff-kn7sl` 的数据准备步骤完成，外部训练步骤在创建 /workspace/input 时 PermissionError，Run 实际 FAILED；TrainJob 未创建。实际 Pod 缺 fsGroup，只设置了容器 UID/GID=1000，不能将此失败记为训练失败用例 PASS。工作卷/原 Pod 保留；Fedora 归档 `pipeline-first-failure-attempt-01.tar` SHA256 `994e7075e6b3fddcb45374444d7a86d091245123ebb36ef2f9e687658424b030`，私有客户端日志/报告亦在 Fedora，不含管理员 kubeconfig。

修复 `f9624e4` 通过固定 KFP e4ebca3 的 COMPILED_PIPELINE_SPEC_PATCH 和 Argo bded09f 的最终 PodSpec 战略合并设置 fsGroup=1000、OnRootMismatch，不 chmod 或放宽 Pod 权限。`9fb49fb` 首次远程物化检查发现已有同名空补丁变量；未部署，修复为替换原值而非重复 env 后定向检查通过。第十六次源 `21c4c78`，包 SHA256 `7c8f4178ccd72f7132f98d60cf5ea0bc7914c771b8554a1e2d55a98bbc099233`，退出码 1、130 次确认写入、未知写入 0，在 API 旧 Update 所有的补丁 value 更新 dry-run 处停止，新值未写入。`2b88d05` 仅在受管 ml-pipeline spec 全部属于 ani-kubeflow 时，以 UID/resourceVersion 条件替换该配置变化，保留元数据并拒绝外部 spec manager；两项定向验证通过。

`985f003eb6dd76301d9c515bb14defb0bae3db44` 的远程回归实际运行六项，退出码 0，日志 `targeted-webhook-replay-attempt-03.log`。回归覆盖两类 webhook 原子规则的 API 默认 scope 重入、显式 Namespaced scope 保留，以及既有四项证书保护。源绑定审批只变更 common.py 摘要和生成源码引用；新审批 SHA256 `f7eb88958ae20cc27477ec2d9f4e6209c7333d47a8cece9e9f0f614c8254380c`。修复后现场重入尚未执行。

第十七次源 `22c4f58`，包 SHA256 `2f5e915f2e0f4fcafd676e9ea7010d73a310ee12b830448e182b715738c45d0a`，退出码 1。API 工作流 fsGroup 补丁已实际写入，Deployment generation=3，新 Pod Ready；旧 terminating Pod 尚在列表而已不计入 Deployment 状态，原 checker 立即误拒绝。原失败保留；`06b741f` 将两项实际状态合并为有界收敛条件，三项定向验证通过，未删除 Pod。第十八次仅执行源 `9e51d9c` 的当前 checker，不重跑安装；退出码 0、CONTROL_PLANE_CHECKED，包 SHA256 `b422704cdb77470a90c69ddc03ea8aa0fb2847183a8ff7b5662ee3aa8707d468`。正式 EAC 仍未执行。

第二次 A 主链 Run `5a8fa18c-181a-40b3-8888-a95794d47e2c` 实际 FAILED，客户端退出码 1；fsGroup 已实际生效，工作目录及输入文件写入成功。Trainer strict dry-run 拒绝重复 volumeMount name，因此没有 TrainJob。`dbe1032` 将同一 PVC 的输入和输出使用两个独立 volume 名称，继续保持输入只读与 output 独立 subPath；Fedora 编译并提交源绑定 Pipeline `cbfa026c55268d59411b594887a9c6b3a929aa94630bdc71aeb1f87ac48a860e`。

第一次 B 主链 Run `53cdbafa-fea6-4bd4-a51e-860f1dc734ce` 实际 FAILED，客户端退出码 1。外部 TrainJob UID `f1cc8e31-f85d-46a6-b7d0-b3b54b3693ea` 实际 Complete，普通 CPU Pod UID `20359309-40a4-43c3-a8ce-61027f7bdee4` 在 ani-03 退出 0，唯一输出已 fsync。Pipeline 控制步骤的旧单 volume 引用判定误拒绝两次同 PVC 引用，尚未发布 Model/Link；不能记为整链成功。`42ab722` 按两个实际 volume 名称、同一 PVC、只读输入和独立输出挂载逐项核验；Fedora 编译的当前 Pipeline SHA256 `bf31a9457392d25da911d06220a048ad83c6810b5969e93a01d6165e81797134`，锁提交 `0695694`。

第二次 B 主链第一次实际全部成功：执行 `env-1383024fd6ed4ac5`，Run `d550bc09-400f-41b4-a5a6-0dc48233cc5f` SUCCEEDED，TrainJob UID `177d4111-e176-4dc7-9ded-07028fa66492` Complete，CPU Pod UID `8f9d6ba7-ac16-43f1-a722-eb9f2cc67597` 在 ani-03 退出 0，固定实际镜像摘要 `7dd0ba785e785449d77a67a093763fe536689837eac8aeee045e455ff12435cb`。PVC UID `49148ca6-c804-44ed-9874-ad112eb21c52` 无 Workflow ownerReference。Pipeline 在 ani-01 控制训练并发布真实 Model/Link/Metrics。原客户端随后因 SDK datetime JSON 序列化失败退出 1，原部分 `.new` 文件保留。`5e8eb32` 改用官方 SDK serializer 并在打开临时文件前完成序列化、可靠落盘；定向真实 SDK 检查通过。新的只读恢复读取相同 Run/PVC/TrainJob/Pod 创建 UID 和终态，退出码 0，无新的持久写入；原失败记录未改写。

独立普通只读 reader 在 ani-02 对第二次 B 工作卷回读，退出码 0，FILES_READ_BACK；输入 SHA256 `709f9c339a8f3724660e7fa9143a587e2d95c5ccc5be27f78623e1074a8550ab`，模型 SHA256 `b2074e00c22f4f9f28bc96221be71e6c0ecf2c78591ffdbc5336acc39727edcf`，训练输入和 reader 写入均得到 EROFS=30。实际 MySQL/MLMD 只读查询证实 Run context 5、数据集 artifact 10 到 prepare execution 11 和 training execution 12 的事件，以及 Link 13/Metrics 14/Model 15 到同一 training execution 的输出事件。Model/Link 属性中的执行、Run、TrainJob UID、PVC UID 均匹配创建响应；这不代替独立 S3 工件读取。

该检查点之后完成下述真实联调；最终统一首装仍独立 NOT_RUN。

## ENV05 联调通过与候选冻结

- 失败执行 `env-18fb0137b8a242f9`：Run `7e6cf92d-a49d-400b-a989-0862099f350c` 实际 FAILED，CPU Pod `35b18954-7a4d-451c-8550-245381399433` 在 ani-02 实际退出 42，TrainJob Failed、JobSet Failed/restarts=0。独立 ani-03 reader 退出 0，输入、unique 和 correlation 保留，无模型。PVC UID `9dcc36ce-fbdb-4290-b7cf-b40a55ded162`。
- 停止执行 `env-1e36e66d582d4070`：Run `7eff9b74-55ec-4347-9bb1-caca563d2b44` 原生历史 PENDING→RUNNING→CANCELING→FAILED；固定 KFP e4ebca3 的终止通过 Workflow activeDeadlineSeconds=0 实现，终态真实为 FAILED，未伪写 CANCELED。TrainJob UID `53ea5559-5d17-4727-887f-cf193acc094e` 和 JobSet 实际 Suspended；原 CPU Pod UID `469a9180-4c14-49e3-9ad1-d950f0ffb134` 已消失，无替换 Pod。两项停止请求均确认成功。原客户端因错误预期 CANCELED 退出 1，保留原报告；`34c3271` 只读对账同一对象、真实历史与物理终止，退出 0、无新写入。独立 ani-02 reader 退出 0，停止文件保留。
- S3 实际 I/O：三个既有 scoped 身份各写入一个唯一对象并回读原字节；15 个显式 403 越权拒绝，未知结果 0。B2 Model/Link 实际读取并匹配 PVC 模型和执行、Run、TrainJob/PVC 创建 UID。首次负向 rc put 返回 7、结果 UNKNOWN，保留原失败；仅用 root HEAD 对该精确键只读确认不存在，随后 scoped rc pipe 返回显式 403；未重写已确认的正向对象。证据 `integration-attempt-19/s3-data-attempt-02/report.json`。
- HTTP/gRPC 身份矩阵：`a26e88f` 的实际 62 请求退出 0；两身份本租户允许、互访拒绝，缺失/无效/错 audience/重复 Authorization 拒绝，六种保护身份头的大小写、下划线及重复变体拒绝。HTTP 与原生固定 Go gRPC 客户端均 TLS 校验；探针身份不是 CPU02 正式服务授权。
- 普通工作负载：`ef171df` 的 attempt-04 退出 0，实际 Pod UID `36f4e343-b268-4042-867e-45fd7dc7f07f`，无 token/Secret/客户端证书。10 个真实 Ready 的 KFP 原口、MLMD、数据库 Service/Pod 端口均超时拒绝；Kubernetes 创建干运行的三个地址传输拒绝，ani-02 API 返回明确 403。TLS 使用公共服务器 CA，未跳验证。attempt-02 原失败保留；attempt-03 产品锁到期，在创建前退出。
- 独立数据库持久化：`e580f4a` attempt-02 对本任务专用 MySQL 执行一次 mysqladmin graceful shutdown。实际 Pod UID `be7bdfb8-b6c0-4d3f-939f-dc99a51e952d`、PVC UID `c8cb3eeb-0dfc-4e71-ad2b-ef734a6c1b78` 未变，容器 restartCount 0→1；重启前后实际 KFP/MLMD 8 行完全相同，SHA256 `ea1536b90673ab60392da748215da585071f140315e84ff6a66ba61b0dcba23e`。共享 PostgreSQL 未重启。attempt-01 锁已到期，在数据库操作前退出。
- 重启后新主链：`env-85ec691ef9424b8b`，Run `175e6bb3-0720-418b-a02d-7f6ccc447032` 实际 SUCCEEDED，客户端退出 0；PVC UID `52508ce7-9591-4971-a3a1-971cb4cded01`，TrainJob UID `9ee5184b-caaf-4ebe-ab35-1bf987d4bf94` Complete，CPU Pod UID `babef277-00b5-4542-b045-61bffe091599` 在 ani-03 退出 0。独立 ani-02 reader 原字节导出/摘要/EROFS=30 通过。真实 MLMD Dataset 24→prepare 23/training 24；Link 27/Metrics 28/Model 29→training 24 输出事件。独立 S3 scoped Model/Link 读取退出 0，与模型 SHA256 `8f3875da8b3ed5c1db4816fc17cd2ad42bd542ddbd28132967dc9683a7591cce` 和创建 UID 一致。
- 隔离与恢复文件：B1/B2/B3 不同执行的 PVC UID 和 input/model/unique/correlation 摘要各自绑定，互不覆盖。B1 全部唯一字节及终态资源先导出 Fedora，归档 SHA256 `ebe7761ca608c69af188d9254b844dc8366dbbc08ef2b7c02c435fd08715383c`；随后按创建 UID 删除其终态 TrainJob/Workflow、只读 reader 和 PVC，每项 body dryRun+原 UID 存续核实+实际 UID 条件删除，均确认消失。B2/B3/失败/停止卷仍保留，`452214e` 将受限 reader 的实际白名单文件字节随原摘要导出；`dee542a` 增加只读 S3 Model/Link 导出，不重跑已通过的 S3 写入矩阵。

C 必需联调通过；代码候选 `dee542af0122e9f95bded2f349849df3a1a53e4a`，13 项源绑定审批 SHA256 `ede18d55e6579ed752a5df1559afbbc2903597b64dadb28274d996a4fe809f57`。阶段 D 在 Fedora 干净独立 checkout 运行既有统一门禁及同源构建；D 通过前不恢复快照。最终首装和 EAC01～EAC14 均不继承 C 的 PASS。

外部终态检查点 `/home/chabking/ani-installer-runs/kf-env-p00-20261003/probes/integration-terminal-c-checkpoint-attempt-02.tar` 与目标端 SHA256 均为 `f3a15118de93ec1d9ae1c7963d99bc8515fd9674cae5c1f8953d9713e3112d89`。包含角色 14～19 原始日志、控制检查、删除条件与回执、数据库前后记录、S3/权限/边界报告，以及 B2/B3/失败/停止卷白名单唯一文件和 B2/B3 Model/Link 原字节；短期身份 token 和 Secret 配置未入包。首次 archive-attempt-01 因 sh 不展开 brace 而缺角色日志，保留部分包，未用于恢复门禁。

阶段 D 已通过，完整冻结身份见 `records/candidate-freeze.json`。Fedora 干净 checkout `frozen-source-dee542a` 的统一 gate、同源 build-code、build-offline 均退出 0，门禁/编译/打包源码指纹始终 `cd0515ec5d196c28486ca564bc24f6872c68cacfb14b5a060c36e69584d919cc`。104 镜像实际内容门禁通过；13 源绑定角色文件、41 wheels、固定 gRPC 工具/源码、11 Chart、仓库 ISO/guest/scanner 实际封装。完整配置 validate、121 文件生产渲染退出 0。正式配置来自已核实完整基础选择，启用 Kubeflow、入口 30445/30446，正式 registry 5000；SSH 及既有口令未改变。

发布传输包 SHA256 `c1f00b7af44a5cea649a0c97d65657231f82dde69dff0580163b35d892b30a61` 在 Fedora/当前 ani-01 一致；ani-01 代码和材料 SHA256SUMS 全部逐项通过，正式 site 原文件/归一化 digest 与 Fedora 一致。目标核包/配置日志已在节点外保存，归档 `release/target-freeze-dee542a-attempt-01.tar` SHA256 `507a22ea00581ab5888e484cf7713256741176361b9046dad2bc069b4c9fd3ae`。不将 config-validation 记录当 install-success。三 VM UUID、guest IP、Id2 及两块 200GiB persistent backing 已重新核实；尚未恢复。下一步仅按授权直接 revert Id2，再重发现身份，以同一发布物进行最终统一首装。

## 执行偏差及证据限制

- 早期诊断清理曾把 dryRun 放在 raw DELETE URL query，同时提供 DeleteOptions body；kubectl 原生 RawDelete 此时未传递 query 意图，A1 的拟 dry-run 实际删除了本任务已完整导出的 reader/工作卷，意图记录晚于这两个首个效果。原后续 404/等待超时保留并只读对账，未清理其他对象。已改为 DeleteOptions body 的 dryRun=[All]，并在实际删除前核实同 UID 未出现 deletionTimestamp；后续删除请求均先记 UNKNOWN、使用服务端 UID 条件、只发一次。未移除 finalizer、未强制删 Pod。
- B3 第一次客户端因 output 目录预存在 mkdir 处退出，尚未创建任何 API 客户端或资源。保留首次 rc/log，用同一输入、不同日志和新的子目录启动后实际退出 0。
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

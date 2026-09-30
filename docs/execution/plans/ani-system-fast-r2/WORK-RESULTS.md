# ANI-system r2 工作结果

本轮应用已在既有集群部署，必要入口检查、完整 gate/build 和交付制包完成。此记录不含秘密。部署与重试命令、入口及凭据取得方式见 [DELIVERY.md](DELIVERY.md)。下方逐轮记录保留当时状态，当前结果以本表和末尾记录为准。

- 本机分支：feature/ani-system-fast-20260930。
- 本机 worktree：/home/chabking/workspace/ani-installer-ani-system-fast-20260930。
- 固定 origin/main 基线：78d8418307b6156954feb10aee8d6972d2ff1bbd（原树 main 干净）。
- 用户输入：/home/chabking/下载/ani-system-fast-r2；只按 README 选择性复制，reference 原件仓外保存。
- Fedora 镜像输入：/home/chabking/ani-installer-runs/ani-system-fast/incoming/ani-images-download.zip；归档及镜像字节校验通过，24 个镜像已导入本任务 Harbor 项目并回读 digest。
- 目标限定 172.16.101.10/.11/.12（ani-01/02/03）；用户确认无人争用 .10，写操作复用该节点既有 ani-install.lock，无 Fedora 新锁。
- 全部非编辑/Git/传输执行通过 ssh fedora；不改动共享凭据、旧材料锁或旧 run。

| 阶段 | 状态 | 证据与限制 |
| --- | --- | --- |
| 隔离分支、基线与资料 | pass | 原树只读，新树从 origin/main 建立 |
| P1 输入校验 | pass | 24 linux/amd64 镜像、3 Chart；实际锁单独保存 |
| P1 Harbor 导入/回读 | pass | 24 个实际 manifest digest 保留并从本任务私有项目回读；原包不变 |
| P2 配置 | pass | 固定现场参数；JWT、mint、Session、专用 S3 与运行账号复用首次种子；18 份私有 Secret |
| P3 专用库、首管与核心入口 | pass | 73 条迁移账本；admin/platform-admin；真实平台密码登录 200、access/refresh token 返回 |
| P4 installer 接入源码 | pass | 2cbed2f；默认关闭开关、一个 role、同一分步脚本；完整 gate/build rc=0，交付脚本 wait rc=0 |
| P4 应用启动 | pass | 20 个声明的 Deployment 和 2 个生成的业务 Envoy Deployment 均 Ready；2 个 HTTPS Gateway Programmed |
| P5 Ready/API/前端 | pass | Console/BOSS/API/Session 健康入口 200；受保护 API 匿名 401/登录后 200；专用 S3 PUT/HEAD 200 |
| 分步重试 | pass | crds/init/wait rc=0；共享 CRD 兼容复用无写入，73 条迁移全部跳过，首管及凭据未重置 |
| 完整 gate/build | pass | 2cbed2f 的 build-code.sh rc=0；包含完整 gate，测试/构建/制包源码指纹一致；首次失败保留 |
| 应用包/代码包 | pass | 内部清单及归档解包回验均通过；摘要与敏感性见末尾及 delivery-manifest.json |
| 推送/PR | pr_open | 用户指定 SSH 443 路径已验证，PR #4 已创建；当前提交与检查以 PR 为准，未合并 |
| GitHub CI | see_PR | 与 Fedora gate 分开；状态以 PR 当前 head 检查为准，不据 Fedora 结果冒称 GitHub CI 通过 |
| 人工登录 | manual_not_run | 不能代称用户登录通过 |
| 裸机从零首装 | not_run | 本轮不重置现有集群 |
| 完整业务验收 | not_verified | 用户后续验证 |

## P1 输入核验与当前环境限制

- 原 ZIP SHA-256：a5be1c9433d1cecb7a3dfac84315bfa5934cf1b41171d77d8e60fc72226c1f47。
- ZIP CRC、安全解包和内层 ani-images-bundle.tar.gz.sha256：pass；工具 verify rc=0，24 个 linux/amd64 Skopeo dir 镜像，3 个 Chart。原件只读保留。
- 已按实际内容新建 materials/actual-images.lock.json；目标 Harbor 引用尚未导入，targetDigestRef 为 null；不冒称 P1 完成。
- Chart 实际归档：gateway-helm v1.8.1、ai-gateway/CRDs v1.0.0，摘要在新应用锁中。
- 常驻与动态 model-import-worker 二进制 SHA-256 分别为 ee7c05d4495391dd20c71cc8e49fdcdcd9bc3eadc68f857ebd86ca4af770d121、f365f7c9ddfa863db8a9d5377fa333a06c37a290ccc724b965cad444013486a4；不合并用途。
- 实际 Console/BOSS nginx 都代理固定 ani-gateway.ani-system.svc.cluster.local:8080；未重编。
- Fedora 默认 installer 路径已不存在；/tmp/clone-with-history 是旧 installer 库，fetch 首错为磁盘配额。基于它的 task-owned Git 副本携带损坏 pack；保留该失败副本，使用本机完整 bundle 在本任务新 bare 库恢复提交历史。
- 有效执行树改为 /home/chabking/workspace/ani-installer-ani-system-fast-20260930-exec-2；使用本任务 home 下 bare 库接收每阶段本机 bundle，按完整提交 SHA detached 执行。每轮先核对 SHA 和受跟踪源码干净。其他开发树未切换。
- Fedora 无 kubeconfig；已有节点 SSH 身份认证被拒；旧 ~/.ani-askpass 的目标密码文件已不存在。访问材料缺口已向用户集中请求。
- Python 初始材料/应用工具 22 项 pass，bash -n pass。78f1ce8e6191a23a78e285ef553804fd0283793e 上 `go test ./pkg/ani -run TestANISystem -count=1` rc=0；3 项检查覆盖关闭时旧配置摘要身份、真实 playbook 开关选择、非法路径/base profile 拒绝。生成格式 diff 在 Fedora 仓外审阅后回本机修改。

## 本次补充验证与访问缺口

- 实际 Session 二进制参数验证：未提供 PUBLIC_WS_BASE_URL 时 rc=1；提供后，错误 ticket key 明确报 `TICKET_ENCRYPTION_KEY_FILE must contain exactly 32 raw bytes`、rc=1。日志为 logs/session-key-contract-2.log、session-key-contract-3.log。这是镜像配置合同证据，不能称为 Session Ready。
- bac60b03cdbc85e4895a49e5356527deb3c311ae：Fedora `python3 -m unittest discover -s docs/execution/plans/ani-system-fast-r2/tests -v` 24 项 rc=0（logs/stage5b-python.log/.rc）。含下载的真实三个 Chart 离线渲染，检查 cert-manager CA 注解、控制器/init digest、HTTPS Gateway 和 failOpen=false authz。
- cb98dc898b2cc8829219c6e641565bcbed6ba59f 起增加整份所选应用参考适配检查。首次失败为残余 10.10.1.66，后续定位到 Session NetworkPolicy、inference publisher 的 10.10.1.67 和 model 健康探测的旧 ani-s07 namespace；同时修复按环境值误过滤 metering Deployment 的问题。各轮首错/rc 保留在 logs/stage5c-reference、stage6-python、stage6b-python。未删校验或假称失败通过。
- 离线检查全部使用 fixture.invalid 引用和测试凭据，只在临时测试目录输出并删除；原 actual-images.lock.json 仍是 registryImport=not_run、targetDigestRef=null，未把测试结果当作真实应用包或 Harbor 回读。
- 当前实际集群节点访问失败：Fedora 上 `ssh -o BatchMode=yes ubuntu@172.16.101.10` 返回认证拒绝；已有 ~/.ani-askpass 指向 platform-20260918/access/node-password，但文件不存在。未读/复制 SSH 私钥，未关闭主机校验；本机 fedora Host 配置本身可用。已请求 Fedora 有效 kubeconfig 或节点凭据文件路径。
- GitHub connector 身份可读取为 zhangzhe-ctrl，但本机 Git 凭据无效。首次 push 为 TLS unexpected EOF，第二次明确 Invalid username or token/Authentication failed（rc=128）；已请求本机恢复 Git 登录，不传递或输出 token。未用 API 另造不同 SHA 的提交。
- 业务初始化仍待现场核对：旧 migration 明确包含 ALTER ROLE ani_app NOLOGIN 和默认权限 FOR ROLE ani；底座 ani_app 用途不同，不能照搬。Metering 镜像/源码存在固定 SET ROLE ani_metering_writer，需要先查实例级角色归属，再选择本任务专用库和限定 SQL；尚未执行或声称迁移成功。
- 站点需要真实 API/Console/BOSS/Session/推理公开地址、S3 HTTPS/CA/专用权限、PG/Valkey/NATS 连接、Milvus 和监控地址，以及现有协调锁。未配置真实模型，未创建假模型。外部开物集成、裸机首装和人工登录尚未验证。

## 用户补充访问材料后的只读核对

- 用户提供节点登录方式后，凭据只写 Fedora 本任务 private/access（目录 0700、文件 0600），不入 Git、日志或应用公开材料。
- .10 身份为 ani-01/ubuntu；通过其既有 admin.conf 得到本任务 kubeconfig 副本。Fedora 的 lb.kubesphere.local 被代理解析至错误地址，初次 API 请求 EOF（rc=1）；只在副本改用 https://172.16.101.10:6443，保留 TLS 校验，读取成功。
- 三节点 ani-01/02/03 为 172.16.101.10/.11/.12，均 Ready，Kubernetes v1.35.8。已有底座在 ani-platform、ani-harbor、ani-observability；尚无 ani-system、ani-aigw 或本任务业务控制器 namespace。现有 PVC 只读保留，未删除。
- PG 只读 SQL 确认只有 postgres 超级用户与可登录非超级用户 ani_app；ani 库 owner=ani_app。无本任务业务库；未改共享角色、口令、权限或现有库。ClusterIssuer ani-ca Ready，默认 StorageClass ani-block。
- 已定位 .10 的 /var/lib/ani-installer/ani-install.lock；尚未定位 Fedora 共享实验锁，已请求当前协调入口，集群写入等待核对。不在本任务目录创建第二把实验锁。
- HTTPS Git 凭据错误已通过用户指定 SSH 路径绕开；22 端口被网络关闭。443 使用既有 github.com 主机公钥校验（HostKeyAlias），`git push ssh://git@ssh.github.com/zhangzhe-ctrl/ani-installer.git HEAD:refs/heads/feature/ani-system-fast-20260930` 成功，发布 dcbe8bf74bd6f9ee787e9763143d35d46a0dc166。未关闭主机校验、未改变原树 origin。
- 只读证据：logs/cluster-access.log/.rc、cluster-nodes.txt、cluster-inventory.txt、cluster-crds.txt、postgres-inventory.log；实际 cluster UID 保存在 materials/cluster-uid.txt。

本轮私有证据根：/home/chabking/ani-installer-runs/ani-system-fast-20260930/；输入校验 logs/p1-input.log；局部 tests logs/stage1-python.log、stage1-python.rc。

## 用户要求的安全暂停（2026-09-30）

- 已按用户关机要求暂停；没有本任务后台执行进程，没有持有实验锁。未执行 Harbor 导入、集群资源写入或业务库初始化；集群只读访问与 PG 清单检查已结束。
- 本机源码提交：9bb9d583481a0b00a284056ecd114bfb972d2bff；本机任务树 /home/chabking/workspace/ani-installer-ani-system-fast-20260930。Fedora 执行树 /home/chabking/workspace/ani-installer-ani-system-fast-20260930-exec-2 停留在同一完整 SHA、受跟踪源码干净。本次暂停记录为后续纯文档提交，不需要重跑构建。
- ab0e5613c0b89bddab09b83b8f45b12bb2eef4e6 的 Fedora 局部检查：Python 24 项、Go 3 项、Bash 语法均通过，bcrypt 工具 gofmt diff 为空。新增 TLS S3 入口和 NodePort 冲突检查仍仅为候选，未部署。
- 9bb9d583481a0b00a284056ecd114bfb972d2bff 的 SQL 边界检查 3 项通过，72 份初始化/迁移 SQL 已准备到 private/sql-candidate-2，未执行。首次准备失败与限定事务适配修复保留在 logs/sql-prepare-1.log/.rc、sql-prepare-2.log/.rc、stage10-sql-tests.log/.rc。
- 首次私有种子、JWT、首管 bcrypt 及节点/Harbor 访问材料均在 Fedora 本任务 private 下保留，恢复时复用，不重新生成或轮换；不在 Git、日志、聊天中记录凭据内容。原镜像包不变，actual-images.lock.json 仍为 registryImport=not_run。
- 草稿 PR：https://github.com/zhangzhe-ctrl/ani-installer/pull/4；未合并。恢复先确认本机/远端 SHA、进程与既有实验协调入口，再继续 Harbor 项目权限及导入回读、实际站点配置、专用库初始化、分步部署和入口核验。完整 gate/build、人工登录与裸机首装仍 not_run；不能据局部检查宣称应用交付完成。

## 恢复与 P1 导入完成

- 用户恢复 Goal，并确认无人争用 .10。未新建 Fedora 实验锁；实际写操作复用 .10 已有 /var/lib/ani-installer/ani-install.lock，SSH stdin EOF/命令退出释放，有界 timeout，不留无限后台锁。
- af182066dcb6b51d04da83344b4e21f4f5f96bd3：创建私有 Harbor 项目 ani-system-fast-20260930（project_id=6），专用 pull robot 仅限该项目，凭据留 private/access；不修改共享鉴权、TLS、扫描配置。
- 原 ani_images.py push --execute（Skopeo dir）24 个镜像成功，保留 digest，并逐一 inspect --raw 回读核对，rc=0。日志 logs/p1-harbor-import.log/.rc、materials/harbor-import/*.push.log/*.inspect.log；actual-images.lock.json 已按真实回读结果更新 registryImport=verified。输入归档原件未修改。
- 站点候选固定 console/boss/api/session/s3.ani.test:30443 和 inference.ani.test:30444；这些地址尚未部署/验收，后续需要客户 DNS/hosts 与 CA。专用拉取 Secret 纳入静态服务、Chart、Envoy 和本任务 ServiceAccount；不把 Harbor admin 凭据交给业务服务。

## P2 私有配置与 P3 初始化

- 75ee6d17711e8a2b7752814dcbcc20be2824bf16：Fedora 27 项局部 Python 检查通过（stage13-python.log/.rc）。私有 runtime 使用已保存种子与只读取得的底座连接材料；首次生成 18 份 Secret 清单，private/runtime.json 与 runtime.site.yaml 为 0600，重试复用。Fedora 系统 CA 默认链接缺失，已使用其实际 extracted PEM bundle，不降低 TLS 验证。
- S3 首次请求连接到被占用的固定本地端口，TLS rc=60、无写入；改为确认本任务 kubectl 临时监听后再连接，保留其他监听。RustFS 1.0.0 的缺用户错误为 404/NoSuchResource，缺策略错误为 500/InternalError + policy does not exist，分别做准确限定适配。3b9e3bcdd657e1ddf01c4497740fcd66cecb61eb 的 stage17-s3.log/.rc=0：新专用用户与 ani-fast-* 桶权限策略准备成功，不复用 Milvus 限桶账号，不更改 root 口令/现有策略。参考 API：https://docs.rustfs.com/en/security-compliance/iam/policies 。
- p3-init-1 rc=0 但复核账本只有 1 条：kubectl exec -i 查询吞掉了 migrations.tsv。1d1a48fbf8ba1f9136a908b551593d5b478a1173 修复 query stdin，p3-init-2 在第 35 份 SQL 的角色 membership REVOKE 处 rc=3，整份事务回滚；已成功的 34 份保留。
- 27535a190f83ef422c862a05e3134ec5a40b0394 仅去掉第 35 份 SQL 中已由专用 bootstrap 排除的冗余角色 membership REVOKE，保留所有库内 GRANT/REVOKE；不扩大迁移角色 ADMIN 权限。局部 SQL 3 项检查通过，p3-init-3 rc=0。p3-init-verification.log/.rc=0 确认账本 72 条、local:admin|platform-admin、ani|ani_app、ani_fast_20260930|ani_fast_migrator。未重置首管、未清库。
- 应用候选当前 private/application-candidate-3（更新 CRD approval 元数据）；旧 candidate-1/2、SQL candidate-1/2/3 和各轮首错日志保留。CRD 首次失败为缺 api-approved.kubernetes.io 注解；c435a662d93f3f1fa5849578e34d8ed8cb13b613 恢复必要注解，不接管已有 CRD。

## P3 登录修复与 P4～P5 现场结果

- 业务 Envoy 控制器首次启动后因 watch namespace 未包括自身 namespace，读取自身 Secret/EndpointSlice 被 RBAC 拒绝。1788da9 将本任务控制器的 Chart watch 范围限定为 ani-aigw、ani-business-envoy、ani-system、ani-platform；Chart 创建其自身读取角色与绑定。仅重启本任务 envoy-gateway Deployment（logs/p4-envoy-restart.log），未操作 kcn 控制器或强抢共享 CRD。
- 实际 Auth 平台密码登录最初返回 400，PG 首错是 refresh_tokens RLS 拒绝新增行。旧表只有 RESTRICTIVE policy，没有 PERMISSIVE 正向许可。648d028 增加专用库内 998_auth_refresh_rls.sql，只对 ani_fast_app 允许 tenant_id IS NULL 或 app.current_tenant_id 的同租户行，WITH CHECK 同条件；保留原限制策略，不关闭 RLS、不授予业务登录 BYPASSRLS。
- logs/p3-init-4.log/.rc=0，新增固定修复后账本共 73 条。已有 local:admin（请求用户名为 admin）、platform-admin 关联、bcrypt cost 12 保留。logs/p3-public-login-verification.json：实际 HTTPS `POST /api/v1/auth/platform/password/login` 200，accessToken/refreshToken 均存在。凭据、响应 token 和原始诊断保存在 private，未入 Git。
- df306ee4e99e54cde281a09a02a3e2e569a135b7：Fedora Python 28 项检查 pass（logs/stage23-python.log/.rc）。包含实际三个 Chart、完整所选脱敏应用适配、SQL 边界和共享 CRD conversion=None 默认化；Webhook/版本/schema 差异仍拒绝。最终运行候选 private/application-candidate-6 与 sql-candidate-5，旧候选不覆盖。
- logs/p5-crds-retry.log/.rc=0：已安装共享 CRD schema 兼容复用，无写入；logs/p5-init-retry.log/.rc=0：73 条迁移全部按摘要跳过，无重新生成凭据或重置首管；logs/p5-wait-retry.log/.rc=0：20 个声明 Deployment rollout Ready。另核对 2 个生成的 Envoy Deployment Ready、两个 Gateway Programmed=True。
- logs/p5-entry-checks.json：通过正确 hostname/SNI、现有 CA 和 .10 NodePort 检查 Console/BOSS 根路径、API/Session /healthz，均 HTTP 200/curl rc=0。S3 匿名根路径 403；推理根路径 404（无已发布真实模型），二者是预期结果，不代表业务推理成功。
- logs/p5-authenticated-api-check.json：`GET /api/v1/admin/tenants` 匿名 401、真实登录 token 200。logs/p5-s3-own-policy-check.json：专用账号通过公开 TLS 入口创建本任务空桶 ani-fast-bootstrap（PUT 200），HEAD 200；原 RustFS 根账号、Milvus 限桶账号及其他桶未修改。
- 旧镜像启动日志会输出带 userinfo 的 NATS URL。原始诊断只留本任务 private；对外结果脱敏，不复制此日志到交付公开证据，也不因此轮换共享 NATS 凭据或重编镜像。
- 仍需人工：客户端 DNS/hosts、信任公共 CA、浏览器以 admin 登录。真实模型/embedding、动态模型任务、KB/RAG 完整业务、外部开物集成未配置/未验收；没有虚假模型。动态租户第一次使用时，需将项目限定的 ani-fast-pull 拉取 Secret 与 ani-inference-fetcher ServiceAccount 配到该租户 namespace。裸机首装 not_run。

## 候选冻结与交付构建

- 源码冻结 SHA：df306ee4e99e54cde281a09a02a3e2e569a135b7。Fedora detached HEAD 一致、受跟踪源码干净；局部回归与分步重试通过后开始一次 `bash kubekey/scripts/build-code.sh`，该入口含完整 gate，不另跑 gate。
- 完整命令/rc/首错在 logs/final-build-command.txt、final-build.log、final-build.rc。11 份底座 Chart 从已有 artifact-c165ae7-candidate-r2 按原 components.lock.yaml 摘要核验后复用到本任务 cache；ANI_CHARTS_OFFLINE=1，不修改旧锁、不重拉材料、不清共享缓存。
- 首次完整构建 rc=1，未产生代码包：`TestRoleTasksUseTheContextKeysTheInstallerProvides` 的既有白名单尚不认识新应用专用 .ani.system 上下文。ff8a1f7 把该范围加入生成器存在性检查，并在开关选择测试中核对实际 enabled/package_root 值；不跳过 role 或删测试。Fedora gofmt 发现同一已改测试文件的三行旧格式漂移，2cbed2f 仅应用审阅过的格式 diff。最终冻结 SHA 更新为 2cbed2f392aa5c6184f2d2177c14b54337ab4f6b；产品脚本/渲染工具与已部署的 df306ee 一致。
- logs/stage25-go.log/.rc=0：新增上下文 guard 与三个 ANISystem 检查通过，gofmt diff 为空。随后同一 2cbed2f detached SHA、受跟踪源码干净，执行 `build-code.sh` 复验，logs/final-build-2.log/.rc=0：无标签/builtin 编译、vet、两种应用测试、连接器/CLI 测试、全部现有行为套件和发布 shell 语法均通过。不是另跑一次 gate；完整 build 内含 gate。
- 测试、构建、制包前后源码树指纹始终为 b366bffb032bc770c88a17741584eef575f0e701cc322b41aa941b54a57a5568。代码包 release/code-2cbed2f；其中 ani-system.sh 与共享 application.sh 字节相同。
- release/application-2cbed2f 来自已验证 candidate-6，包含 73 份已记账初始化文件、实际镜像锁、CRD、清单和 SOURCE.json，文件 0600/目录 0700。应用包含 Secret，仅受限传输；节点密码、kubeconfig、Harbor admin 访问文件及原始诊断不在交付包。
- 两个归档均解到本任务 tmp/delivery-archive-check 并完整核对内部 SHA256SUMS，通过。根交付校验 logs/delivery-release-check.log；公开材料只有实际镜像映射、公共 CA、身份清单与脱敏 JSON 证据。
- 用最终代码包内脚本执行 `--stage wait`（含 preflight），logs/delivery-built-entry-wait.log/.rc=0，20 个声明 Deployment 再次 Ready；既有 .10 锁已释放。logs/delivery-migration-count.txt 回读 73。制包未重放 install.sh 或全量重新部署。

| 归档 | 字节数 | SHA-256 |
| --- | ---: | --- |
| ani-system-fast-20260930-application-2cbed2f.tar.gz | 1847555 | 27604f773db49702bb3c800ea70c9337751bf8b4823bc6e64da9904234331eb6 |
| ani-system-fast-20260930-code-2cbed2f.tar.gz | 51265855 | ad6fce3bc92d278439cd8e5efec01f8a506e52c784cb33322dcee9d393979304 |

Fedora 交付根为 /home/chabking/ani-installer-runs/ani-system-fast-20260930/release；本机接收目录 /home/chabking/ani-installer-delivery/ani-system-fast-20260930。原镜像 ZIP 保持 incoming 原路径不变。收尾结果/交付说明为纯文档更新，不重编代码包。人工与已知问题统一列于 DELIVERY.md 与本记录 P5 段，不另外建立任务体系。

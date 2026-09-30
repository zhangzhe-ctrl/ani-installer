# ANI-system r2 工作结果

本轮正在执行；此记录不含秘密。

- 本机分支：feature/ani-system-fast-20260930。
- 本机 worktree：/home/chabking/workspace/ani-installer-ani-system-fast-20260930。
- 固定 origin/main 基线：78d8418307b6156954feb10aee8d6972d2ff1bbd（原树 main 干净）。
- 用户输入：/home/chabking/下载/ani-system-fast-r2；只按 README 选择性复制，reference 原件仓外保存。
- Fedora 镜像输入：/home/chabking/ani-installer-runs/ani-system-fast/incoming/ani-images-download.zip；归档及镜像字节校验通过，导入尚未执行。
- 目标限定 172.16.101.10/.11/.12；实际集群身份、协调锁待核对。
- 全部非编辑/Git/传输执行通过 ssh fedora；不改动共享凭据、旧材料锁或旧 run。

| 阶段 | 状态 | 证据与限制 |
| --- | --- | --- |
| 隔离分支、基线与资料 | pass | 原树只读，新树从 origin/main 建立 |
| P1 输入校验 | pass | 24 linux/amd64 镜像、3 Chart；实际锁单独保存 |
| P1 Harbor 导入/回读 | pass | 24 个实际 manifest digest 保留并从本任务私有项目回读；原包不变 |
| P2 配置 | in_progress | 候选渲染工具；现场参数、稳定私有凭据尚未完成 |
| P3 空库、首管与核心入口 | partial | 专用库 72 条迁移及首管关联已核对；核心入口待启动验证 |
| P4 installer 接入源码 | candidate | 默认关闭开关、一个 role、同一分步脚本；Go 局部检查通过 |
| P4 应用启动 | not_run | 未向集群写入资源 |
| P5 Ready/API/前端 | not_run | 无现场成功证据 |
| 完整 gate/build | not_run | 候选尚未完成现场联调/冻结；不提前重复制包 |
| 推送/PR | draft | 按用户指定改用 SSH 443 推送成功；草稿 PR #4，未合并 |
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

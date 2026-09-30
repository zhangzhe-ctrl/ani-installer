# 旧 ANI 快速部署交付（2026-09-30）

本轮将用户下载的实际镜像部署到 172.16.101.10/.11/.12 既有集群。必要应用 Ready，程序化登录、受保护 API 和前端入口通过；人工浏览器登录、真实模型业务验收与裸机首装仍单列未执行。逐项结果和首错见 [WORK-RESULTS.md](WORK-RESULTS.md)。

## 材料与身份

Fedora 交付根：`/home/chabking/ani-installer-runs/ani-system-fast-20260930/release/`。源码冻结 SHA 为 `2cbed2f392aa5c6184f2d2177c14b54337ab4f6b`；后续结果文档提交不改变已验证代码。交付清单 `delivery-manifest.json` 记录实际归档摘要、源码树指纹和运行结果。

本机已接收归档、公共 CA、镜像映射、清单与脱敏证据到 `/home/chabking/ani-installer-delivery/ani-system-fast-20260930/`。完整 gate/build 和交付脚本 `wait` 均 rc=0，归档解包后的内部 SHA256SUMS 回验通过。

| 归档 | SHA-256 |
| --- | --- |
| ani-system-fast-20260930-application-2cbed2f.tar.gz | 27604f773db49702bb3c800ea70c9337751bf8b4823bc6e64da9904234331eb6 |
| ani-system-fast-20260930-code-2cbed2f.tar.gz | ad6fce3bc92d278439cd8e5efec01f8a506e52c784cb33322dcee9d393979304 |

| 材料 | 用途 |
| --- | --- |
| `code-2cbed2f/`、`ani-system-fast-20260930-code-2cbed2f.tar.gz` | 现有完整 gate/build 产出的 kk、install.sh、verify.sh、ani-system.sh 及探测脚本；内部 SHA256SUMS |
| `application-2cbed2f/`、`ani-system-fast-20260930-application-2cbed2f.tar.gz` | 本现场匹配的应用清单、CRD、专用库 SQL、实际镜像锁与校验清单；**含私有 Secret，目录 0700/文件 0600，仅受限传输** |
| `actual-images.lock.json`、`image-map.json` | 服务及动态用途 → 实际 manifest/config digest → Harbor digest 引用；不合并两种 worker 用途 |
| `ani-site-root-ca.crt` | 已有 ani-ca 的公共根证书，用于浏览器/客户端信任；不含私钥 |
| `delivery-manifest.json`、`SHA256SUMS` | 交付身份与文件校验 |

用户原镜像 ZIP 原样保留在 Fedora `/home/chabking/ani-installer-runs/ani-system-fast/incoming/ani-images-download.zip`，SHA-256 为 `a5be1c9433d1cecb7a3dfac84315bfa5934cf1b41171d77d8e60fc72226c1f47`。它是 Skopeo dir 集合的包装，24 个 linux/amd64 镜像、3 个 Chart；使用 [ani_images.py](scripts/ani_images.py) verify/push，不能对外层 ZIP/tar 做 docker load 或 hauler load。原件不重复覆盖到 release。

镜像已进入私有项目 `172.16.101.10:30003/ani-system-fast-20260930`，全部回读摘要一致。专用拉取 robot 仅有该项目 pull/list/read 权限，创建时有效期 90 天；应用不使用 Harbor admin。版本变化是本轮批准采用的实际内容，不能称与旧参考环境同构。

## 入口和首次人工访问

在访问电脑的 DNS 或 hosts 中，将下列六个 hostname 指向 `172.16.101.10`；再信任交付的 `ani-site-root-ca.crt`。

```text
172.16.101.10 console.ani.test boss.ani.test api.ani.test session.ani.test s3.ani.test inference.ani.test
```

| 入口 | 地址与已执行检查 |
| --- | --- |
| Console | https://console.ani.test:30443 ，根路径 200 |
| BOSS | https://boss.ani.test:30443 ，根路径 200 |
| API | https://api.ani.test:30443 ，/healthz 200；匿名受保护 API 401、登录后 200 |
| Session | wss://session.ani.test:30443/api/v1/realtime ，HTTPS /healthz 200；真实 WebSocket 业务未验收 |
| S3 | https://s3.ani.test:30443 ，专用账号 PUT/HEAD 本任务桶 200，匿名根路径 403 |
| 推理 | https://inference.ani.test:30444 ，TLS/路由入口可达；无真实模型，根路径 404，推理业务未执行 |

首管用户名 `admin`，角色 `platform-admin`。密码由本轮一次生成，保存在 Fedora 本任务 `private/runtime-seed.json` 的 `admin.password`；在可信终端本地读取或受限传输给操作人，不写入聊天、Git 或公开日志。节点密码、数据库口令、JWT 私钥、S3 密钥及 robot 凭据均在该任务 private 下；不使用旧业务凭据。

API 已执行真实平台密码登录，返回 access/refresh token；这不代替用户浏览器登录。原始登录响应只在 private。业务 PostgreSQL 使用专用库 `ani_fast_20260930`、专用非超级用户，原 `ani` owner/`ani_app` 不变；现有 PG transport ssl=off，应用 sslmode=prefer，如实保留底座现状。专用 RustFS 用户仅允许 `ani-fast-*` 桶，现有 Milvus 限桶密钥未复用。

## 在当前集群重试

以下命令通过已有 SSH 配置进入 Fedora 执行。当前集群已安装，不重放 `install.sh`。示例只做只读 `wait`；修复本任务配置后可指定相应 stage，或用 `all` 执行同一应用脚本。写操作复用 .10 的既有 `/var/lib/ani-installer/ani-install.lock`，不另建 Fedora 锁。

```bash
ssh fedora
task_root=/home/chabking/ani-installer-runs/ani-system-fast-20260930
task_tree=/home/chabking/workspace/ani-installer-ani-system-fast-20260930-exec-2
export PATH="$task_root/materials/bin:$PATH"
export NO_PROXY=172.16.101.10,172.16.101.11,172.16.101.12,127.0.0.1,.ani.test,.svc,.cluster.local
export no_proxy="$NO_PROXY"
python3 "$task_tree/docs/execution/plans/ani-system-fast-r2/scripts/with_cluster_lock.py" \
  --password-file "$task_root/private/access/node-password" -- \
  bash "$task_root/release/code-2cbed2f/ani-system.sh" \
  --package "$task_root/release/application-2cbed2f" \
  --kubeconfig "$task_root/private/access/kubeconfig" --stage wait
```

阶段为 `preflight → crds → prepare → controllers → init → core → apps → gateway → wait`；`all` 按此顺序执行。preflight 校验所有文件、目标节点/cluster UID 和 NodePort 冲突；资源必须无同名冲突或已有本任务标签。已有共享 CRD 只比较兼容 schema 后复用，不强制接管；数据库初始化按文件摘要跳过已成功迁移，已有首管不重置。本轮 crds/init/wait 重试均 rc=0，73 条迁移全部跳过。

不要改交付文件后沿用旧 SHA256SUMS。现场配置需要适配时，本机编辑工具或 site 文本，提交完整 SHA 后同步 Fedora；在新的私有输出目录渲染、核验并提交回读锁/文档。只修本任务应用，不覆盖并行资源，不清库、删 PVC、轮换共享凭据或重放底座。

## 首装末尾接入与异地客户交付

新增设置默认关闭，使用现有 full profile，在客户第一控制节点可读的绝对路径提前放置匹配的私有应用包：

```yaml
aniSystem:
  enabled: true
  packageRoot: /opt/ani/application
```

原 `create_cluster` 底座末尾通过一个 `ani/system` role 调用与上面相同的应用脚本；关闭时不执行该 role。`packageRoot` 不是 Fedora 私有目录的远程引用，应用材料必须实际位于执行首装的控制节点。此接入已做源码/模板/gate 验证，裸机首装本轮 **not_run**。

本现场应用包绑定当前节点和 cluster UID。新客户不能照搬其凭据/数据库身份/registry 地址或 UID：先按该客户材料和底座连接生成首次种子，核验实际镜像，导入其可写 Harbor 并回读；再用本包 [assemble_runtime.py](scripts/assemble_runtime.py)、[prepare_sql.py](scripts/prepare_sql.py)、[render_gateways.py](scripts/render_gateways.py)、[render_application.py](scripts/render_application.py) 在 Fedora 准备匹配应用材料。工具 `--help` 提供必需输入；参考原件和 ANI 已提交迁移来源另行受限传输，未将整份归档写入 Git。动态版本也必须来自实际锁，不重新匹配旧 digest 或默认重编服务。

## 待人工与已知限制

- DNS/hosts、公共 CA 信任及 admin 浏览器登录待人工执行。
- 没有配置虚假模型或 embedding 服务。真实模型/动态 worker、推理、KB/RAG 完整业务验收尚未执行；外部开物集成未配置。
- 首次创建真实动态租户时，将项目限定的 `ani-fast-pull` Secret 安装到该任务新租户 namespace，并为 `ani-inference-fetcher` ServiceAccount 设置 imagePullSecrets；静态服务、控制器、init 和生成 Envoy 已配置拉取权限。只处理该租户新资源，不能覆盖其他任务同名对象。
- 旧镜像日志可能打印带 token userinfo 的 NATS URL，原始日志保持私有，对外交付使用脱敏结果；本轮不修改 ANI 镜像或轮换共享凭据。
- 22 个 Deployment Ready 和入口检查证明本轮所需安装结果；不等同人工登录、完整业务验收、裸机流程或长期应用管理能力。

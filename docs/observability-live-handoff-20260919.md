# 第二批真实安装失败记录与后续执行交接

## 0. 当前结论与停止位置

本文件覆盖旧 C5 文档中的完成结论。**第二批未完成，不能进入“已验证可用”的交付状态。**

用户最新要求：本轮若再次失败则暂停，由下一位执行者接续。已执行两次干净快照安装，第二次失败后停止。
没有修改产品代码，没有在失败集群上修复后重跑，没有执行 OpenSearch 安装。

| attempt | 实际配置 | 结果 | 执行边界 |
| --- | --- | --- | --- |
| `loki-cumulative` | 第一批四项 + metrics + Loki | **fail**，470 tasks / 459 success / 10 ignored / 1 failed | 在 metrics 创建命名空间失败；Loki、Fluent Bit 未执行 |
| `loki` | 第一批四项 + Loki，metrics=false | **fail**，495 tasks / 484 success / 10 ignored / 1 failed | Loki 启动并通过 Ready/版本/空查询；配置解析失败；Fluent Bit 未执行 |
| OpenSearch | 未执行 | **not_verified** | 遵照用户暂停要求，不启动第三轮 |

两次 installer 退出码均为 1。第一轮失败时间 2026-09-19 06:53:56 UTC，第二轮为 07:13:33 UTC。
两次都完成 `.20/.21/.22` 三台快照还原和安装前离线隔离验证。
**两次失败均为 installer 缺陷，不能标记 kcnbug。** 未人工重启 kcn-controller。

先前确实做了源码、材料研究、Go 测试和 Chart 渲染，不是完全没有工作；但这些检查遗漏了实际任务路径。
此前 C5 宣称 code 全部完成，不能替代真实安装，也没有覆盖当时缺失的发布材料。

## 1. 不得改变的职责与环境

- 本地只编辑代码/文档、管理源码和发起同步。编译、测试、格式化、下载、打包、集群操作均通过 `ssh fedora`。
- 唯一测试集群：`172.16.101.20/.21/.22`，`.20` 自身承载 installer。禁止操作 `.10/.11/.12` 等其他集群。
- 固定底座、组件版本和现有 KubeKey 流程；不新增安装框架、兼容分支、任意 values 注入或清理恢复引擎。
- installer 负责材料、参数、安装顺序、依赖准备、等待、验证和错误证据。组件自身 bug 交给组件，不修改组件源码或补丁镜像。
- `kk` 与 artifact 分开交付。纯代码修改只编译小 code 包，复用本次已就绪的 artifact。
- 需要重新首装时先取证，再通过实验室快照脚本还原三台；禁止将快照、删失败 namespace/PVC、清 CNI/OVN、强制 detach 等塞入 installer。
- 用户已允许在实测遇到 kcn 故障时尝试重启 `kcn-controller`；必须先有网络故障证据。重启无效时记录 kcn 阻塞并交接，不反复重启碰运气。
- 当前是**暂停交接状态**，接到用户的继续执行指令后再开始下一轮。

## 2. 真实失败 A：metrics 使用错误的模板路径

文件：`kubekey/builtin/core/roles/ani/metrics/tasks/main.yaml`，第 29 行及该文件其他 `.ani.metrics.namespace` 引用。

实际 runtime spec 位于 `.ani.components.metrics`，而 task 使用 `.ani.metrics.namespace`。
其渲染结果是 `<no value>`，被 shell 当成重定向，失败日志为：

```text
ANI Metrics | Create the observability namespace
/bin/bash: line 1: no: No such file or directory
error: no objects passed to apply
INSTALL_EXIT=1
```

**下一位最小修正：**统一 role 任务里的真实上下文路径，搜索同一 role 其他引用，不只改第一行。
补一个使用真实 `componentSpec`/runtime 数据渲染实际任务命令的针对性检查；不能只渲染 values，不能在测试上下文补一个产品实际不存在的字段来让测试通过。
之后仍须真实安装证明指标 role 能执行完。

## 3. 真实失败 B：Loki `/config` 被错误按 JSON 解析

文件：`kubekey/builtin/core/roles/ani/loki/templates/verify.sh:131`，其中第 135 行调用 `json.load(r)`。

第二轮现场：

- `ani-loki-0` 为 Running / Ready，0 次重启，Loki `3.7.8`。
- `storage-ani-loki-0` Bound，5Gi，`ani-block`。
- `/ready` HTTP 200；buildinfo 版本正确；`query_range` 返回 success / streams / 0 series。
- GET `/config` 为 HTTP 200，Content-Type `text/plain; charset=utf-8`，响应开头为：

```yaml
target: all
http_prefix: ""
ballast_bytes: 0
server:
```

因此 `json.load()` 抛 `JSONDecodeError: Expecting value: line 1 column 1 (char 0)`。
这不是网络不可达，也不是 Loki 组件故障。
现场有效配置还观察到 `retention_enabled: true`、`delete_request_store: filesystem`、`limits_config` 对应 retention 的显示值为 `3d`；脚本期待固定字符串 `72h`，下一位应一并验证等价时长处理，不能依赖文本显示格式。

**下一位最小修正：**按真实 API 格式读取配置并验证准确字段，不把全文中任意一个 `retention_period` 当成 limits_config 的值。
离线工具镜像只有 Python 标准库，禁止在目标节点 `pip install`；如果真的需要新解析依赖，必须明确制包变更及其必要性，优先复用现有可用工具/接口。
用本次真实响应格式补最小回归检查，然后重新首装。不要捕获异常后当成功，不要删除保留期验证。

**尚未证明：**Fluent Bit 安装、三节点 stdout 入库、标签完整性、后端/采集器重建后持久化、实际保留期过期删除。
`Loki Ready` 不能记为 C3 pass。

## 4. OpenSearch：未实装，先前审查已发现的缺陷

参见 [C5 代码复核](observability-c5-review-20260919.md)，R1～R4 仍未修：

1. Chart 所需安全配置 Secret 在 `helm --wait` 之后才 apply，干净首装会等待不存在的 Secret。
2. `security-init.yaml` 只渲染未 apply，随后却等待其中 Job。
3. PVC 错用 `ani-opensearch-master-0`；实际 StatefulSet 与 claim template 名都是 `ani-opensearch-master`，PVC 应为 `ani-opensearch-master-ani-opensearch-master-0`。
4. vm.max_map_count 任务实际仅在首控制节点执行，没有覆盖允许调度 OpenSearch 的其他节点。

这些是静态确定的编排缺陷，**没有本轮实装复现证据**。修正与运行都由下一位完成，不得把这张清单写成已修复/已实测。

## 5. 可直接复用的真实路径与材料

以下路径均在 `ssh fedora` 上，不在本机：

```text
R=/home/chabking/ani-installer-runs/obs-live-20260919
源码快照: R/src
code: R/releases/ani-code-c7661ca
artifact: R/releases/ani-artifact-obs
Hauler store: R/inputs/store
Chart: R/inputs/charts
实验脚本: R/lab
证据: R/evidence
```

源码提交：`c7661ca5c5861a4331e97eac43d1594551cfc14a`，产品源码无修改。

```text
kk SHA256:
e986c0eadadcfeade6870f5d40823b82435ce13679aa7d4ce259aed14d99b914
artifact/SHA256SUMS 文件 SHA256:
351877e8a92c6c59c4cbd590d06c4881391f2bc5a060aa1925fd565adeadbebe
```

code 约 120 MB，artifact 约 4.4 GB，含 50 条 images.tsv 镜像记录、6 份 Chart。
两次传输均已在 `.20` 校验 code 与 artifact 的 SHA256SUMS。
构建日志有上游 `hack/version.sh` 不可执行警告，构建退出成功，但版本 ldflags 缺失；本次用源码提交与二进制 SHA 标识，不凭 `kk version` 猜提交。

配置（含私有凭据，0600，禁止打印/提交）：

- `R/inputs/site-loki-cumulative.yaml`：第一轮累计配置，metrics=true。
- `R/inputs/site-loki.yaml`：第二轮实际配置，metrics=false。
- `R/inputs/site-opensearch-cumulative.yaml`：准备好的累计 OpenSearch 配置，尚未执行。
- `R/inputs/site-opensearch.yaml`：metrics=false 的 OpenSearch 隔离配置，尚未执行。

节点凭据沿用 `/home/chabking/ani-installer-runs/platform-20260918/access/`，不要把内容复制到文档。
联网制包曾遇到 DNS、EOF 和下载慢；最终通过 Fedora 本地 HTTP 代理 `127.0.0.1:7897` 获取镜像。
该代理仅用于 Fedora 制包，不在三台目标节点配置，不是离线安装依赖。
材料已齐，无须为纯代码修正重下镜像。

## 6. 实验入口与恢复现场

`.20` 上本次实际入口：

```text
/opt/ani-installer/code/ani-code-c7661ca/install.sh
/opt/ani-installer/artifacts/ani-artifact-obs
/opt/ani-installer/site/cluster.yaml
/tmp/obs-install.stdout
/tmp/obs-install.exit
```

保留当前失败集群，方便只读诊断。Kubernetes/Ceph/第一批四项/Loki 已创建；metrics 本轮关闭，Fluent Bit 未执行，OpenSearch 未安装。
Ceph 为 HEALTH_WARN：insecure key types / 允许 AES key / PGs 265 > 250，不能写成 HEALTH_OK；本轮不修 Ceph。
kcn 有启动期容器重启，但两次失败不具备 kcn 网络故障证据，不能据此归因为 kcnbug。

Fedora 实验脚本（不要放入产品包）：

| 脚本 | 用途与注意 |
| --- | --- |
| `R/lab/node.sh '只读命令'` | 从 Fedora 登录 `.20`，已有 askpass；普通 ubuntu kubeconfig 可用 |
| `R/lab/collect.sh <attempt>` | 收集安装日志、节点/Pod/PVC/events；每次换新 attempt 名，避免覆盖 |
| `R/lab/run-mode.sh loki\|opensearch` | 隔离、系统锁准备、传输、哈希校验、普通用户启动；**不负责快照还原**。其中路径固定到旧 code，下一位必须改成新构建名后再用 |
| `/home/chabking/ani-ops/restore_esxi_snapshots.sh` | 先 dry-run，再 execute；确认只操作 .20/.21/.22 |
| `/home/chabking/ani-ops/apply_offline_isolation.sh` | 每次还原后 apply + verify，安装后再 verify |

后台安装需伪终端；本次无终端启动第一次在 sudo 处失败，未进入 installer，随后用 `script -qefc` 启动成功。
不要把这一实验启动问题算作产品失败，也不要为此修改 installer 的 sudo 逻辑。
现场 `.20` 有本轮私有 `.obs-password` / `.obs-askpass` 文件（0600/0700），不可打印或打入材料；快照还原会移除。
共享实验锁为 `/home/chabking/ani-installer-runs/locks/cluster-20-22.lock`；交接后需要重新以 flock 获取，不能只看残留 owner 文本。

## 7. 下一位按顺序执行的任务卡

### H1：修正两个已实证的 installer 缺陷

只修 metrics 上下文与 Loki 配置解析/时长比较，补相应回归检查。Fedora 测试，失败就先修对应原因。
不要顺手重构框架或增加吞错兼容逻辑。

### H2：修正 OpenSearch 编排缺陷

按第 4 节逐项完成，核对真实 role 执行顺序和 Chart 对象名，不能只 grep 模板里是否有关键词。
确认 Secret → workload 可启动 → 初始化 Job/RBAC apply → Job 完成 → 认证及保留策略验证 → collector 的顺序。

### H3：独立构建新 code，保留 artifact

在 Fedora 新目录同步修正后的源码，运行现有 `scripts/build-code.sh`，设置新的 `ANI_CODE_OUT`。
记录新提交/源码快照、新 kk SHA、测试结果；artifact 不变则复用上述目录并校验哈希。
更新实验脚本中的 code 路径、attempt 日志文件名，保留所有本次证据。
不要覆盖 `ani-code-c7661ca` 或 `obs-live-20260919/evidence` 里的历史结果。

### H4：从干净快照跑累计 Loki

用 metrics=true 的累计配置，新 code + 现有 artifact。
按锁 → 3/3 还原 → 离线隔离 → 传输校验 → 普通用户安装 → 独立 verify 执行。
必须证明指标查询、Prometheus→Alertmanager→测试接收器的 firing/resolved，及三节点 stdout→Fluent Bit→Loki 查询。
指标/告警/日志持久化用计划要求的正常 Pod 重建测试验证；测试行为留在实验流程，不把破坏性重建塞进普通安装验证。
任何前置失败都要把后续未执行项写为 not_verified。

### H5：再次从干净快照跑累计 OpenSearch

复用同一最终 code/artifact，仅切换后端。执行安装和独立 verify。
证明 TLS/CA/认证、匿名与错误密码拒绝、采集器独立写入身份、三节点日志实际进入索引、正确 retention policy 关联及持久化回读。
不能直接向后端写数据冒充 Fluent Bit 采集验收。

### H6：最终交付

只有 H4、H5 都有匹配最终二进制的完整证据，才能标记整批 pass。
实际保留期过期未测仍单列 not_verified，不能借配置正确声称过期删除已验证。
更新 status / progress / 手动 runbook，删除占位路径和错误 PVC/API 说明，给出真实存在的发布路径及哈希。
不自动提交/推送。最终汇报逐项区分 pass、fail、not_verified，不再使用“代码完成所以整批完成”的说法。

## 8. 证据索引（Fedora）

所有文件位于 `R/evidence/`：

- `provenance.txt`、`release-hashes.txt`、`build-code.log`、`build.log`。
- `loki-cumulative-restore.log`、`loki-cumulative-isolation-before.log`、`loki-cumulative-isolation-after.log`、`loki-cumulative-transfer-sha.log`。
- `loki-cumulative/install.log`、`loki-cumulative/cluster-state.txt`。
- `loki-restore.log`、`loki-isolation-before.log`、`loki-isolation-after.log`、`loki-transfer-sha.log`。
- `loki/install.log`、`loki/cluster-state.txt`、`loki/config-response-summary.txt`、`loki/kcn-ceph-state.txt`。

`/config` 响应摘要为额外只读查询获得，明确证明 HTTP 成功与 JSON 解析失败；未修改工作负载、未重新执行失败验证。

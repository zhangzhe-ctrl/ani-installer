# ani-installer 首轮离线安装收尾执行方案

日期：2026-09-15。目标仓库：`/home/chabking/workspace/ani-installer`。

本方案只处理已经发现的验收漏洞，给当前已经装好的测试集群补上可信的流量证据。完成后结束本轮工作，下一轮功能另行讨论。

## 1. 本轮目标与结论边界

用户已经手动安装成功，`kubekey/docs/progress.md` 也记录了首次离线安装和干净快照重装。保留这些成果，不重新建设安装器，不重跑整套安装。

本轮完成三件事：

1. 网络探测任何一步失败，都必须使验收失败。
2. Envoy 探测必须访问对应 Gateway 的 Envoy Service，不能误访后端 Service。
3. 每次执行 `verify.sh`，都自动创建新的测试客户端并发起请求，不能使用历史成功日志。

实现后，在现有三台虚拟机组成的集群执行两次新版验证，证明流量通过且两次确实使用了不同的新客户端。第二次只用于证明验证可重复，不重装集群。

最终可以确认的是“当前集群通过修正后的主动网络与 Envoy 验证”。本轮不产生新的干净快照安装结论，也不把此前安装后才修改的 local connector 宣称为已经重新完成首装验证。

## 2. 开始时只核对这些事实

编写方案时，代码为 `main` 分支，HEAD 为 `8ad7c899977450d4a6554af881cd0c770ee62004`，工作区干净。完整 KubeKey fork 在 `kubekey/`，不是仓库根目录。

执行者先读 `kubekey/AGENTS.md`，查看当前 `git status` 和相关文件。如果 HEAD 已前进，只记录当前版本并核对这几个目标函数/脚本；普通版本前进不是要求用户回退的理由。保留现有未提交修改。

历史记录中的专用测试环境如下，连接时以用户当前提供的配置和已有任务授权为准：

| 项目 | 已有记录 |
| --- | --- |
| installer / 节点 1 | `test-installer-01`，`172.16.101.20` |
| 节点 2 / 节点 3 | `172.16.101.21` / `172.16.101.22` |
| 现场配置 | `/home/ubuntu/ani-installer/cluster.yaml` |
| 已用离线包 | `/home/ubuntu/ani-installer/ani-offline-ubuntu24-amd64-20260915-s2e` |
| 系统 | Ubuntu 24.04 Server，amd64 |
| 网络 | 管理网 ens34；kcn 管理无主机 IP 的 ens35；存储网 ens36 |

实际包目录、SSH 通道或 kubeconfig 若已改变，读取当前配置确认即可。没有连接信息时，继续完成代码和测试，只询问缺少的连接信息。不要把大段环境预检交给用户手工执行。

## 3. 已确认的问题与修改范围

| 位置，相对仓库根目录 | 当前问题 |
| --- | --- |
| `kubekey/builtin/core/roles/ani/smoke/templates/network-client-pod.yaml` | `/bin/sh -c` 下，前面的 wget/test 失败仍会执行末尾 printf，最终可能退出 0 |
| `kubekey/builtin/core/roles/ani/smoke/tasks/main.yaml` | 取 Service `.items[0]` 和 `.ports[0]`，可能选中同命名空间的 `ani-smoke-backend` |
| `kubekey/scripts/verify.sh` | 只读取固定名称 Pod 的既有状态和日志，没有重新发送流量 |

第二项只能证明历史验收存在绕过网关的可能，不能据此声称历史流量一定绕过了 Envoy。

允许修改的代码文件：

- `kubekey/builtin/core/roles/ani/smoke/tasks/main.yaml`。
- 新增 `kubekey/builtin/core/roles/ani/smoke/templates/probe.sh`，作为共同探测实现。
- 删除被共同实现替代的 `network-client-pod.yaml` 和 `client-pod.yaml`，并同步删除其引用。
- `kubekey/scripts/verify.sh`。
- `kubekey/scripts/build-offline.sh`：仅同步必备文件清单及本次探测材料。
- `kubekey/pkg/ani/` 下针对本次行为的少量测试文件。
- `kubekey/ani/README.md`、`kubekey/docs/progress.md`，以及本轮结果文档。

不修改 Kubernetes、kcn、Envoy 的版本、镜像、安装配置或安装顺序。APT 离线回退、镜像摘要锁定、Python 依赖整理、总包格式、HA、存储和其他组件全部留到下一轮。不要顺手修复。

## 4. 实施一：用一个小脚本统一两处验收

### 4.1 固定实现方式

新增一个普通 Bash 脚本 `probe.sh`，集中处理服务选择、客户端创建、等待和结果判断。不新增 CLI 子命令、通用执行引擎或验证框架。

该脚本放在现有 smoke role 的 `templates/` 中，但不包含现场 Go 模板变量。安装角色用现有 `template` 任务将它下发到 `/etc/kubernetes/ani/smoke-probe.sh`，然后用 `bash` 执行。独立 `verify.sh` 通过 `bash "$ROOT/manifests/ani/smoke/templates/probe.sh"` 执行包内脚本，不依赖模板文件的可执行权限。两个调用点都显式传入 kubeconfig 和日志目录，并直接传播脚本退出码。

这样，初装和安装后的验证使用同一份代码。不要在两处各复制一套网络命令或 Service 选择逻辑。

内部调用约定：沿用 `KUBECONFIG_FILE`，默认 `/etc/kubernetes/admin.conf`；增加仅供调用者设置的 `ANI_SMOKE_OUTPUT` 日志目录。未提供日志目录时脚本自行创建并打印路径。用户的验证入口保持不变：

```bash
KUBECONFIG_FILE=/home/ubuntu/.kube/config ./verify.sh /home/ubuntu/ani-installer/cluster.yaml
```

路径按实际现场使用，不能把示例主机路径硬编码到源代码。

### 4.2 获取本次探测输入

脚本自动读取：

- 命名空间：`ani-installer-smoke`。
- 后端 Pod：`ani-smoke-backend` 的当前 Pod IP、所在节点和 `backend` 容器镜像。
- 后端 Service：`ani-smoke-backend` 的当前 ClusterIP。
- Gateway：`ani-smoke`；HTTP listener：`http-b`，当前端口 9090。
- 当前节点名称。除后端所在节点外，其余两节点按名称排序，分别运行 Envoy 客户端和网络客户端。

保持现有三节点验证范围；输出各测试的节点分配，确保网络客户端与后端不同节点。复用后端正在使用的离线 BusyBox 镜像引用，设置合适的 `IfNotPresent` 拉取策略，不引入新镜像或公网拉取地址。

脚本只检查本次请求必需的输入是否存在、是否可唯一确定。无需增加一整套 OS、磁盘、网卡和组件兼容性预检。

### 4.3 准确选择 Envoy Service

先通过只读查询查看实际 Gateway 和 Service 的标签、selector、端口及 EndpointSlice，确认定制 Envoy 生成 Service 的关联方式。将查到的精确关联条件写入实现和一份脱敏测试样本。

必须满足以下规则：

1. Service 明确属于 `ani-installer-smoke/ani-smoke` Gateway。若使用关联标签，同时匹配 Gateway 名称和命名空间。标签名称以实际材料为准，不猜测。
2. 匹配结果恰好一个；零个或多个都失败，并打印候选名称。
3. Service 名称和 ClusterIP 都不能等于后端 Service。
4. 从 Service 端口中明确选择 listener 对应的 HTTP 9090 端口，不能取第一项。缺少对应端口即失败。
5. 输出选择依据、Service 名称、ClusterIP 和端口。真实验证时保留其 EndpointSlice 和对应 Envoy Pod 信息，证明这个入口指向数据面。

即使上游某版本使用过某种标签，也不能直接假定私有控制器完全相同。若实际生成信息无法建立唯一关联，记录观察到的事实再报告，不准回退到命名空间第一项、名称模糊匹配或后端地址。

只使用当前已有的 Bash、kubectl 和基础命令，不为了解析结果给目标节点增加 jq 或其他安装依赖。查询与筛选失败必须传递到最终退出码。

### 4.4 每次自动新建两个客户端

使用 `metadata.generateName` 配合 `kubectl create`，前缀分别为 `ani-smoke-network-client-` 和 `ani-smoke-client-`，由 API 返回本次实际名称。保存名称、UID、创建时间、节点和测试镜像。

后续的等待、读日志和结果判断，只使用本次返回的实际 Pod 名称与 UID。不读取原来固定名称的成功 Pod，不复用 `/etc/kubernetes/ani/` 中旧客户端 YAML。

本轮保留测试 Pod 和结果便于检查，不清理原有 Pod，不删除 namespace。两次验证只会新增四个短任务客户端，不需要在这里设计历史任务回收功能。

两个客户端都设为 `restartPolicy: Never`，通过 `/bin/sh -ec` 运行请求。每个 wget 设置例如 10 秒的超时。避免把失败命令放进会吞掉状态的结构，也不要依赖未确认支持的 Bash `pipefail`，因为容器里使用 `/bin/sh`。

网络客户端按顺序执行：

1. 请求当前后端 `PodIP:3000`，精确比较响应为 `ANI-INSTALLER-OK`。
2. 请求后端 Service ClusterIP 的 80 端口，做同样比较。
3. 请求 `ani-smoke-backend.ani-installer-smoke.svc.cluster.local`，做同样比较。
4. 每项通过打印对应项标记，三项全部通过才打印最终 `ANI-NETWORK-OK` 并退出 0。

Envoy 客户端请求准确选出的 `Envoy Service ClusterIP:9090`，精确比较响应为 `ANI-INSTALLER-OK`，然后输出明确的 Envoy 通过标记。不得改成请求后端、NodePort 或 port-forward。

脚本以“本次 Pod 成功结束、容器退出 0、对应成功标记完整”作为通过条件。新增分项日志后，同步替换旧的“整个日志必须等于单个字符串”判断，避免修复后被旧比较逻辑误判。

两个客户端的终态等待都有总时限，例如各 180 秒。出现 Failed 立即收集当前 Pod 状态与日志并退出非零；到期仍未结束也退出非零。不循环重建客户端直到碰巧成功。诊断日志收集本身失败时保留原始失败退出码。

### 4.5 接入两个调用点

初装 smoke role 保留 namespace、后端 Pod/Service、Gateway、Backend、HTTPRoute 的准备和就绪步骤。去掉旧客户端创建、历史日志断言及模糊 Service 查询，改为最后下发并调用共同脚本，直接传播其退出码。

`verify.sh` 保留已有仓库、镜像清单、节点和控制器状态检查，用共同脚本替换原来的历史 Pod 日志读取。每次自动生成独立日志目录，例如包内 `logs/<cluster>/verify-<时间>-<随机值>/`。

独立 verify 不重新应用后端、Gateway 或 HTTPRoute，更不运行安装角色。若现有后端已变化而测试 Backend 仍指向旧 IP，应报告这一实际测试资源问题；不要静默修路由把失败掩盖掉。

## 5. 实施二：少量有意义的失败回归

测试调用实际脚本中的命令或函数，不只搜索源码是否包含 `-e`、某个标签或成功字符串。可以用临时目录中的假 wget/kubectl 和固定数据，不连接集群、不访问网络。

| 场景 | 必须观察到的结果 |
| --- | --- |
| 三项网络请求均返回正确内容 | 退出 0，分项和最终标记完整 |
| Pod IP / Service IP / DNS 任一请求失败，分别覆盖 | 退出非零，不打印网络最终成功标记 |
| 三项中任一响应内容不正确，分别覆盖 | 同样失败，不能被后续 printf 掩盖 |
| backend Service 排在第一项，调整 Service 列表顺序 | 始终选中同一正确 Envoy Service |
| 网关 Service 为零个、多个，或者没有 9090 端口 | 明确失败，不选择其他地址兜底 |
| 已存在旧 Succeeded 客户端，但本次新客户端失败 | verify 失败，不读旧 Pod 作为成功证据 |

同一个测试表覆盖这些分支即可，不增加一套测试框架。另加一个小范围接线检查，确认 role 与 verify 使用共同脚本、包内材料存在，且删除的旧模板不再被构建清单引用。

在现有 Ubuntu amd64 构建环境执行，复用已有 Go 依赖缓存：

```bash
cd /home/chabking/workspace/ani-installer/kubekey
bash -n scripts/verify.sh scripts/build-offline.sh builtin/core/roles/ani/smoke/templates/probe.sh
go test ./pkg/ani
go test -tags=builtin ./pkg/ani
make kk
```

远端 checkout 路径不同时替换 `cd` 路径。新增测试必须被上述命令执行到；若使用独立测试脚本，额外记录并执行它的准确命令。不要跑整个上游测试、race、HA 或性能测试。

## 6. 实施三：同步修订包，不重收集离线材料

`builtin/core/fs.go` 会将 role 和 template 内嵌到 `kk`。因此只改松散 YAML 或 verify.sh 不够，必须使用本次源码重新编译包含 builtin 的 `kk`。

本轮采用小范围修订包：在构建机的全新目录复制已有离线包的静态材料，替换本次构建的 `bin/kk`、`verify.sh`、`manifests/ani/smoke/` 和说明文件，重新生成并校验 `SHA256SUMS`。同时修改源代码中 `build-offline.sh` 的必备文件清单，保证以后正常制包也会包含新脚本并移除旧模板要求。

规则：

- 沿用原来的 KubeKey artifact、Hauler 镜像归档、镜像清单、Hauler 二进制和版本配置，不重新下载或收集镜像。
- 仅复制静态交付文件；不要带入现场 `work/`、`registry-data/`、运行日志、SSH 密钥、密码配置或 kubeconfig。
- 不使用会连带修改原包的硬链接覆盖方式。
- 新修订包使用独立目录，不把普通 build-offline.sh 的输出目录指向正在运行的 Hauler 根目录。
- 记录源代码版本/差异、修订包路径和校验结果，确保内嵌 kk、共同脚本和 verify.sh 来自同一次修改。

把修订包放到 installer 节点独立目录后，直接运行其中的 verify.sh。现有 Hauler 服务和镜像地址继续使用，不切换或重启它。禁止为执行新版验证而运行 `install.sh`、`kk ani install` 或 `kk create cluster`。

## 7. 实施四：在现有集群补齐真实证据

以下工作由执行 AI 自动完成，用户只需要提供已经授权的测试环境入口。

1. 保存当前节点、测试后端、Gateway/HTTPRoute/Backend、相关 Service 和 EndpointSlice 的简要状态。首次创建测试 Pod 前，由执行 AI 将查询到的节点名称和 InternalIP 与现场配置核对，确认 kubeconfig 指向本次专用集群；这是一次现场定位，不新增用户预检流程。保存旧客户端名称和 UID，作为与新探测区分的参考。
2. 查看现有断网规则是否仍在，并进行有超时的外网失败探测。不要删除路由或修改防火墙来制造测试环境；若已解除断网，记录当前验证的网络条件，不能把这次结果称作断网验证。保留原先离线安装证据。
3. 在 installer 节点运行修订包的 verify.sh，保存 stdout/stderr 和真实退出码。使用 tee 时保留被执行命令的退出码，不能以 tee 成功判定验证成功。
4. 核对本次两个新 Pod 的 UID、节点分配、实际命令/目标地址、终态和日志；确认 Envoy 目标 Service 的 EndpointSlice 指向 Envoy 数据面。
5. 再执行一次同一验证命令，确认又创建了两个新 Pod，UID 与第一次不同，且真实请求通过。两次结果分别保存，失败记录也保留。

两次验证都不改系统网络、不重启 kcn/Envoy、不触发孤儿 LSP 清理、不调整任何业务工作负载。若遇到这些组件的实际故障，保存证据并报告，修复范围留待用户决定，不能靠重启或重装把本轮测试刷绿。

原始日志保留在目标节点结果目录。仓库只新增简洁脱敏的 `kubekey/docs/validation-closeout.md`，并追加更新 `kubekey/docs/progress.md`。不要提交完整 kubeconfig、现场密码配置或包含凭据的原始输出。

## 8. 完成条件与最终交付

| 交付项 | 完成条件 |
| --- | --- |
| 失败传播 | 失败和错误响应测试均为非零退出，无最终成功标记 |
| Envoy 入口选择 | 列表顺序不影响选择；唯一关联、准确端口，无 backend 兜底 |
| 验证新鲜性 | 两次 verify 分别创建新客户端，UID 不同；旧 Pod 不参与判定 |
| 源码与包同步 | 小范围测试通过，builtin kk 构建完成，修订包校验通过 |
| 实际网络 | 本次新客户端跨节点 Pod IP、Service IP、DNS 请求均通过 |
| 实际网关 | 本次新客户端访问真实 Envoy Service，并获得正确业务响应 |
| 结果记录 | 记录命令、时间、退出码、客户端 UID、访问入口和原始日志路径 |

每项只填 `pass`、`fail` 或 `not_verified`。代码与构建通过、真实环境验证通过、历史离线安装记录三者分别陈述。没有访问环境就保留 `not_verified`，不能用单元测试或旧日志补成通过。

不得改写此前安装成功的历史条目。追加说明：旧网络/Envoy 探测存在假阳性风险，本轮已用修正后的新客户端补验；分别列出当前结果。未执行的新版干净安装明确保留为未验证，但不要求在本轮补做。

最终交给用户：改动文件列表、测试结果、修订包位置、两次真实验证结果，以及确实遇到的遗留问题。无需罗列假想风险。完成后停止，等待下一轮迭代讨论；不提交、推送或发布。

## 9. 给执行 AI 的启动提示词

```text
请执行 /home/chabking/workspace/ani-installer/docs/ani-installer-validation-closeout-plan.md。

这是现有离线安装的收尾任务，目标是修复网络探测失败未退出、Envoy Service 选择不准确两处假阳性，并让每次 verify 自动发起新的真实请求。

先阅读方案和 kubekey/AGENTS.md，再按方案完成共同探测脚本、少量失败回归、包含 builtin kk 的修订包，以及现有测试集群的两次主动验证。复用现有离线材料和已经授权的专用测试环境；缺少环境入口时先继续完成代码，只询问缺失信息。

不要重装集群、恢复快照、重启网络组件、修改网络/防火墙、重新收集镜像，也不要扩展 OS、HA、存储、摘要锁定等下一轮功能。不要把大量手动预检交给用户。源码位置是仓库内 kubekey/。

只认本次新建客户端的 UID、真实请求、退出码与日志，不能复用旧 Succeeded Pod 判定成功。保留历史安装成果，诚实区分 pass/fail/not_verified。不提交或推送。完成后给出简洁的交付清单并停止，下一轮另行讨论。
```

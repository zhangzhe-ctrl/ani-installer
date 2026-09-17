# 历史资料：基础组件迭代方案（已停止执行）

归档日期：2026-09-17。以下内容是范围收缩前的方案快照，其中的完整组件批次、交接、恢复及发布约定均不得当作当前执行指令。

当前只执行 [最小离线首装方案](../ani-installer-platform-iteration-execution-plan.md)。组件版本研究暂存于此，后续重新讨论时参考。当前方案已明确 kk/artifact 独立发布、失败后申请恢复快照、组件缺陷交由组件负责；本历史资料中的冲突条款全部失效。

---

# ani-installer 基础组件迭代：代码实施与真实离线验证方案

日期：2026-09-15。源码仓库：`/home/chabking/workspace/ani-installer`，实际 Go 模块及 KubeKey fork 在 `kubekey/`。

2026-09-17 修订：**kk 与 artifact 必须分开构建、分开校验、分开发布。artifact 中不包含 kk，也不包含本项目的可变执行脚本/角色模板。仅修改执行代码时，只重编和替换 kk，复用已有物料及已加载镜像。先完成第 13 节的解耦，再继续其余组件工作；不得每修一次代码就重新导出、压缩、传输或导入全部物料。**

本文是下一位执行 AI 的完整工作说明。先写代码，再使用安装器分批实际安装；代码问题修进独立的 kk，物料缺失或依赖版本变化才修订 artifact，最终验证一个明确的 kk + artifact 组合。本文中的版本是执行输入，不代表已经通过本轮离线验证。原有“已恢复 ISO”是 2026-09-15 的现场信息；续执行时核对当前状态，本次解耦不要求恢复快照或重装已有集群。

## 1. 必须遵守的执行边界

1. **本地只阅读、编辑源码和文档，以及发起 SSH/SFTP/rsync 传输。** `gofmt`、代码生成、语法检查、单元测试、编译、下载、Chart 渲染、镜像处理、制包、运行二进制、安装及验证命令，全部通过 SSH config host **`fedora`** 执行。本地不运行这些命令，也不回退到其他构建机。
2. **唯一安装测试集群是 `172.16.101.20`、`172.16.101.21`、`172.16.101.22`。** 用户确认三台已经恢复到刚安装完 ISO 的状态。`.20` 同时承担 installer 和集群节点，Hauler 必须运行在 `.20` 宿主机。`fedora` 只做构建、文件传输和 SSH 调度，不承担集群运行依赖。
3. 所有目标机访问都经 `fedora` 发起，包括 `.20` 上的安装命令。禁止本地直连目标机执行命令。不得触碰 `172.16.101.10～12` 的已有平台，不使用那套平台的 kubeconfig，也不检查 ANI 应用源码。
4. 沿用 **KubeKey v4.0.7 fork + 原生 roles/connectors + artifact + Hauler**。不恢复 Kubespray/Ansible Runner，不增加自写 kubeadm/SSH 安装引擎，不重建通用工作流、插件 SDK 或庞大的预检体系。
5. 正式安装入口负责完成系统依赖准备、网络配置、运行时、集群、组件和验证。不要给用户一长串手工安装依赖或创建 Kubernetes 资源的操作。
6. 有实际安装错误才修相应问题。不得通过手工 kubeadm、手工补包、临时改现场 YAML 把正式安装失败包装成成功。诊断性调整若必要，必须记录并回写代码，之后由正式入口复现。
7. 不提交、不推送、不发布。保留当前未提交工作。用户后续要求提交时，先按第 2 节脱敏。

### 1.1 与旧方案的关系

- `docs/ani-installer-validation-closeout-plan.md` 中的网络失败传播、正确选择 Envoy Service、每次新建验证客户端，继续执行或复用已经完成的实现。
- **旧方案中“只在现有集群补验、不得重装”的现场假设已被本次用户指令替代。** 目标已经恢复 ISO，不能等待那个旧集群出现；将修正后的探测并入本轮首次安装。
- 原方案把 kk、执行脚本、物料一起打包的规则由本次修订替代。containerd/runc 的物料版本发生变化时，导出一次对应的新 artifact；之后调试同一组物料的安装代码，继续复用它，不反复导出。
- `kubekey/AGENTS.md` 中第一轮仅 kcn/Envoy 的范围，按本次用户批准的组件范围更新。保留 KubeKey 底座、真实日志、禁止发布和禁止修改非测试集群等规则。
- 如果另一个 AI 正在修改相同文件，先协调文件归属，复用其成果，不覆盖修改、不另起一套实现。

## 2. 现场和执行目录

### 2.1 用户已确认的测试输入

| 项目 | 本轮输入 |
| --- | --- |
| 构建及调度入口 | SSH config host `fedora`，不要替换成 IP 或其他 host |
| 节点 1 / installer | `172.16.101.20`，历史名称 `test-installer-01` |
| 节点 2 | `172.16.101.21`，历史名称 `test-installer-02` |
| 节点 3 | `172.16.101.22`，历史名称 `test-installer-03` |
| SSH 账号 | `ubuntu` |
| SSH 密码 | 历史归档已脱敏；仅现行执行文档临时保存 |
| OS | Ubuntu 24.04 Server，非精简安装，amd64，ISO 默认内核 |
| 管理网 | `ens34`，管理 IP 已配置 |
| kcn 接口 | `ens35`，不给主机配置 IP，交给 kcn 管理 |
| 专用存储接口 | `ens36`，用户填写地址规划后由安装器配置 |
| Ceph 数据盘 | 每台空白 `/dev/sdb`；系统盘绝不使用 |
| kcn 隧道出口 | 管理网络 `172.16.101.0/24`，不是无主机 IP 的 ens35 |
| Pod / Service CIDR | `10.16.0.0/16` / `10.96.0.0/16` |
| Hauler | `.20:5000`，宿主机 systemd 服务 |

密码仅为用户授权的本地执行记录。不得复制进源码、镜像、离线发行包、普通日志或测试快照。提交前将本表密码替换为占位符，同时检查其他待提交文件。现场 `cluster.yaml` 和生成的 Secret/robot 凭据保存在运行目录，权限 `0600`，不进入发行包。

开始执行时读取三台真实 hostname、内核、网卡、磁盘和内存；上述历史 hostname 如有变化，以实际 hostname 更新现场配置，不强制重命名。用户已允许使用三块空白 `/dev/sdb`，不需要再次询问同一授权。若实际不是空盘、并非该设备或承载系统，则只暂停存储操作并报告具体差异。

本次编写方案时，`fedora` 可达，已发现 Go、Podman、Docker、rsync、Python、skopeo、make、zstd；约有 483 GiB 可用磁盘、15 GiB 内存。工具可用性不是已运行测试的证明；不能因 host 名为 fedora，就拿 Fedora RPM 给 Ubuntu 节点安装。目标账号密码由用户在连接探测后提供，编写方案时尚未用该密码完成目标登录验证。

### 2.2 远端目录和操作方式

在 `fedora` 为本轮选择一个独立目录，例如 `/home/chabking/ani-installer-runs/platform-20260915/`，固定后记录，不覆盖其他任务目录。建议结构：

```text
platform-20260915/
  src/                 # 本地源码同步副本
  cache/               # Go、Chart、二进制、镜像下载缓存
  tools/               # 构建/制包所需固定版本工具
  releases/kk/         # 独立 kk 二进制、版本记录及校验
  artifacts/           # 独立物料包/解压目录；仅物料变化才增加版本
  evidence/            # 构建、渲染、物料检查、安装证据
  access/              # 现场配置和凭据，不同步回源码
```

本地编辑后，用经 SSH 的 rsync 同步源码；排除 `.git/`、`kubekey/build/`、`kubekey/_output/`、运行目录和含凭据的现场文件。不要用全目录 `--delete` 删除远端缓存或其他任务文件。格式化、代码生成在 `fedora` 执行，生成的源文件再按具体路径传回本地，不反向覆盖整个工作区。

命令形态如下，路径按本轮固定目录替换：

```bash
ssh fedora 'mkdir -p /home/chabking/ani-installer-runs/platform-20260915/src'
rsync -a -e ssh --exclude=.git --exclude=kubekey/build --exclude=kubekey/_output \
  /home/chabking/workspace/ani-installer/ \
  fedora:/home/chabking/ani-installer-runs/platform-20260915/src/
ssh fedora 'cd /home/chabking/ani-installer-runs/platform-20260915/src/kubekey && GOMAXPROCS=4 go test -p 4 ./pkg/ani'
```

以上本地命令只是 SSH 传输/调度。测试、编译等进程实际运行在 `fedora`。若需 Ubuntu 构建环境，在 **fedora 上**使用 Ubuntu 24.04 容器并挂载本轮目录；目标节点不因此安装 Docker。优先直接使用 fedora 的 Go 工具链，`GO_BIN` 必须设为实际路径，不能照抄脚本默认的 `/usr/local/go/bin/go`。

执行 AI 自行完成 `fedora → 三节点` 的 SSH 准备。使用用户给定密码或现有凭据配置，凭据不出现在进程参数和普通日志中；必要的 sshpass/SSH 辅助工具只在 fedora 准备。ISO 恢复后按明确的三个目标处理 SSH host key；不要全局关闭 SSH 校验。installer 对 `.21/.22` 使用现场配置中的认证，对自身沿用本地 connector。

长时间构建/制包/安装应保存运行 ID、日志、PID 或 systemd unit 和退出码。目标 `.20` 的安装可通过 systemd-run 启动独立服务，SSH 仅负责调度与查看，避免 SSH 中断就丢失结果。install/components 对同一 cluster 使用同一个 flock 锁，锁已占用时打印原任务位置，不并发启动。连接中断后先检查原进程/单元和退出状态，仍运行则接回原任务；结果未知时不能重复启动 install/components。这个要求用小脚本和文件即可满足，不增加持久工作流系统。

## 3. 本轮范围和版本输入

### 3.1 固定范围

- Kubernetes、kcn 和定制 Envoy 沿用已跑通方式；升级 containerd/runc。
- 基础组件有独立开关。完整示例启用本轮所有 VM 可验证组件；旧配置没有 `components` 时，新增组件默认关闭，不能突然全装。
- PostgreSQL、Valkey、NATS、Milvus、Harbor、Loki、Prometheus、Alertmanager 使用单实例方案。Ceph 使用三节点三副本；kcn 和其他既有控制器的副本数不因“单实例优先”被擅自修改。
- Ceph 提供 RBD、CephFS、RGW，支持共享管理网和专用存储网两种首装配置。
- 本轮不新增 Kube-OVN、单网卡三网融合、管理 IP 迁移、控制面 VIP/LB 切换或 HA 开关。**Ceph 复用管理网不等于将 CNI 改为三网融合**：仍使用 ens35 上的 kcn。
- 不安装 ANI 系统、Envoy AI Gateway、Jaeger、MinIO、RustFS、OpenSearch、Grafana、NACK、Kafka、Pulsar。GPU 只保留 NVIDIA 后续硬件批次，不在这三台普通 VM 上宣称 GPU 已验证。
- 不实现现有集群版本升级、组件卸载、Ceph 在线改网、数据迁移、全站断电自动恢复或生产 HA。

### 3.2 版本表

所有 `candidate` 都必须真实安装验证后才升级为 `verified`。Chart 与服务版本分别记录，实际 image/init/hook/sidecar/Operator 动态镜像需全部进入离线包。

| 组件 | 本轮版本及材料 | 部署方式 |
| --- | --- | --- |
| Kubernetes | `v1.35.8` | 现有 KubeKey 首装 |
| containerd / runc | `v2.3.4` / `v1.4.3` | 新运行时配对，重新制包并验证 |
| etcd / pause | 沿用 `v3.6.6` / `3.10.1` | Kubernetes 控制面专用 |
| Hauler | `v2.0.3` | 现有宿主机临时 registry |
| Helm / crane | `v3.20.0` / `v0.20.6` | 包内固定 Linux amd64 工具；下载和校验在 fedora 完成 |
| kcn | `kc-networking:v0.6.2` | 复用现有模板和镜像摘要 |
| 定制 Envoy | controller `v1.8.3`；Envoy `distroless-v1.38.3` | 复用现有配置、RBAC、sidecar；不换官方组合 |
| Rook / Ceph | `v1.20.7` / `v20.2.4` | 复用已核验 YAML |
| Ceph CSI / CSI Operator | `v3.17.1` / `v1.0.4` | 与上述 Rook YAML 配套，不额外混装 CSI Chart |
| cert-manager | app / Chart `v1.21.2` | 官方 OCI Chart，包含 CRD |
| PostgreSQL | `17.11`，`postgres:17.11-bookworm` | 自有小型单副本 StatefulSet + RBD |
| Valkey | `8.1.10`，优先既有 `8.1.10-alpine` 镜像配方 | 自有小型单副本 StatefulSet，AOF + RBD |
| NATS | server / 官方 Chart `2.14.6` | 单实例 JetStream fileStore + RBD |
| Prometheus / Alertmanager | kube-prometheus-stack Chart `91.4.0`；Operator `0.94.0`、Prometheus `3.14.0`、Alertmanager `0.34.0` | 关闭 Grafana，分别单副本 |
| Loki | app `3.7.7` / Chart `18.13.1` | SingleBinary + RGW，关闭内置 MinIO |
| Fluent Bit | app `5.1.2` / Chart `0.58.2` | DaemonSet → Loki |
| Milvus | app `2.6.21` / Chart `5.0.25` | standalone + embedded Woodpecker + RGW |
| Milvus 专用 etcd | `milvusdb/etcd:3.5.25-r1`；包内 etcd 子 Chart `8.12.0` | 本期单副本 + RBD，不复用控制面 etcd |
| KubeVirt / CDI | `v1.9.0` / `v1.66.1` | 官方固定版 Operator 清单和 CR |
| Volcano | `v1.15.2` | 官方固定发布材料 |
| LWS | `v0.9.0` | 官方固定发布材料 |
| Harbor | app `2.15.2` / Chart `1.19.2` | 官方单实例配方 + RBD |
| Harbor 内部 DB / 缓存 | `goharbor/harbor-db:v2.15.2` / `goharbor/valkey-photon:v2.15.2` | 不复用业务 PostgreSQL/Valkey；Chart 字段仍叫 redis |
| NVIDIA GPU Operator | `26.7.0`，仅后续候选 | 型号/驱动未冻结；本轮默认禁用，GPU 实测单列 |

保留现有 `ani/images.tsv` 中定制 Envoy 的配套 digest，包括 tag 名为 `gateway-dev:latest` 的已固定内容；构建时按已记录 digest 取内容，不能重新解析浮动 tag 后悄悄替换。若固定内容不可获取，报告缺失物料，不自行换上游 Envoy。

为基础组件添加一份普通 `ani/components.lock.yaml`，记录上述版本、材料来源、材料 SHA256、启用 profile 和实际依赖。继续使用 `ani/images.tsv` 做镜像映射，避免再造互相竞争的多份镜像清单。版本来源集中管理；测试验证生成配置与制包结果一致，而不是仅比较几个字符串。

实施中发现真正的版本冲突：保留失败日志，指出具体组件和错误，提出最小替换；不要自行升级整张表。缺失的工具版本或发行附件路径可由执行 AI 从官方固定发布中核对并记录，不需要重新询问常规下载/实现选择。

## 4. 用户入口、配置和代码组织

### 4.1 保留简单入口

下列是本次修订要求实现的正式接口，不代表当前代码已经支持。kk 放在 artifact 目录之外，以绝对路径调用；`--artifact` 指向已准备的物料目录，`--workdir` 是独立运行目录。

```bash
sudo /opt/ani-installer/bin/kk ani install --config /absolute/path/cluster.yaml \
  --artifact /opt/ani-installer/artifacts/platform-r1 --workdir /var/lib/ani-installer/ani-lab
sudo /opt/ani-installer/bin/kk ani components --config /absolute/path/cluster.yaml \
  --artifact /opt/ani-installer/artifacts/platform-r1 --workdir /var/lib/ani-installer/ani-lab
sudo /opt/ani-installer/bin/kk ani verify --config /absolute/path/cluster.yaml \
  --artifact /opt/ani-installer/artifacts/platform-r1 --workdir /var/lib/ani-installer/ani-lab
```

- `kk ani install`：空白节点 → 系统/运行时/Kubernetes/kcn/Envoy → 已选组件 → 验证 → 按配置执行最后的仓库交接。
- `kk ani components`：本轮分批迭代时，对已安装 Kubernetes 执行已选组件；不重新运行 kubeadm、OS 初始化或 CNI 安装。同一组镜像已就绪时不重载、不重启 Hauler；镜像物料确实变化才执行第 13 节的受控供应更新。
- `kk ani verify`：只验证，不顺便安装、重载镜像或修改组件配置。主动客户端必须是本次新建；验证脚本从本次执行的 kk 内嵌资源提取，不能读取 artifact 中的旧副本。
- components/verify 增加简单的 `--only <组件名>` 过滤，复用固定组件列表和角色；调试 Ceph 就仅运行 Ceph 相关步骤，不重装其余组件。依赖已就绪时不重复安装，未就绪时指出缺项。不要扩成任意 DAG/通用恢复引擎，也不宣称部分步骤通过等于完整首装通过。
- 在现有命令基础上添加上述参数和 verify/components 子命令；`--package-root` 如需兼容，作为 `--artifact` 的旧目录参数别名，两个参数冲突时报错。不要把旧二进制的参数假装成已经实现的新接口。
- 老 `install.sh`/`components.sh`/`verify.sh` 若保留，只能作为薄包装，通过显式 `KK_BIN` 或 PATH 调用独立 kk；不再固定 `$ROOT/bin/kk`，不再承载另一份业务逻辑。这些包装不进入物料 artifact；权威使用说明采用上面的 kk 命令。

artifact 按只读物料使用，运行中不在其中创建 logs/work、展开运行模板或加载 Hauler store。日志、kubeconfig、凭据、渲染结果、导入记录和 registry-data 进入 `--workdir`。每次执行记录独立的 kk 版本/摘要、artifact ID/摘要和本次运行 ID。

为 components 新增一个 KubeKey playbook，按明确列表执行启用的 roles；不设计通用 DAG。固定依赖顺序：Ceph、cert-manager → 基础数据 → 可观测 → Milvus → KubeVirt/CDI/Volcano/LWS → Harbor → 最后交接。

Chart 使用包内 `.tgz`，通过包内固定 Helm 二进制执行 `helm upgrade --install`，给每个组件稳定 release/namespace 和有限等待时间。不在目标节点 `helm repo update`、`helm dependency update`、curl GitHub 或 pip install。第一次失败不自动 uninstall，不用 `--atomic` 抹掉诊断现场。已选但缺包/缺必需依赖的组件明确报错，不悄悄跳过或自动引入另一组件。

### 4.2 配置约定

扩展 `pkg/ani/config.go` 中现有结构，版本由包决定，现场文件仅描述目标节点、组件开关、网络、存储、资源和服务访问参数。

```yaml
# 下列是新增字段示意，必须与现有 name/installerNode/ssh/network/registry 合并。
nodes:
  - name: test-installer-01
    address: 172.16.101.20
    storage:
      devices: [/dev/sdb]
      interface: ens36
      address: REPLACE_WITH_STORAGE_IP_AND_PREFIX
  # .21/.22 同样填写；不能将示例占位符传给实际命令。
components:
  ceph:
    enabled: true
    network:
      mode: dedicated             # dedicated 或 shared-management
      cidr: REPLACE_WITH_STORAGE_CIDR
      configureInterface: true
    storageClasses:
      block: ani-block
      filesystem: nfs
  certManager: {enabled: true}
  postgresql: {enabled: true}
  valkey: {enabled: true}
  nats: {enabled: true}
  prometheusStack: {enabled: true}
  loki: {enabled: true}
  fluentBit: {enabled: true}
  milvus: {enabled: true}
  kubevirt: {enabled: true}
  cdi: {enabled: true}
  volcano: {enabled: true}
  lws: {enabled: true}
  harbor: {enabled: true}
registry:
  port: 5000
  handoff:
    enabled: true
```

第一批仅 runtime/kcn/Envoy，关闭所有新增组件，显式设置 `registry.handoff.enabled:false`。P2～P6 继续保持交接关闭；P7 所有选中组件验证通过后才打开。上面的 `true` 仅表示最终完整首装示例。依赖校验只指出当前必需项，不强制让未启用某组件的用户配置它。

本期依赖固定为：PG/Valkey/NATS/Prometheus/Loki/Milvus/Harbor 的持久化配方要求 Ceph；Loki、Milvus 另要求 RGW；Fluent Bit 的本期输出配方要求 Loki；CDI 的本期磁盘验证要求 Ceph RBD，VM 验证同时要求 CDI+KubeVirt；Harbor HTTPS 要求 cert-manager/已生成内部 CA；交接要求 Harbor 启用及全部选中组件验证成功。缺失时给出确切组件名，不自动启用一串依赖，也不自动回退 emptyDir/hostPath。

生产口令不放默认 values。安装器首次生成并持久保存每个服务独立的密码/令牌，重跑沿用同一份，不能因重跑导致现有数据无法登录。运行日志脱敏，不输出 Secret 完整对象；提供一个仅含服务地址、端口、Secret 名称和获取方法的连接清单。

### 4.3 修改范围和落点

| 位置 | 工作 |
| --- | --- |
| `kubekey/pkg/ani/config.go`、相关测试 | 新组件/存储字段、默认值、必要输入检查、KubeKey 配置生成 |
| `kubekey/pkg/ani/runner.go` | 分离 executable/artifact/workdir；复用物料与已加载 Hauler；初装结束调用组件流程；不重写执行器 |
| `kubekey/pkg/ani/components.go` 等少量新文件 | 组件流程入口、静态顺序、配置/日志准备 |
| `kubekey/cmd/kk/app/builtin/ani.go` | 接入 components/verify、独立 artifact/workdir 参数和有限的 --only 过滤 |
| `kubekey/builtin/core/playbooks/` | 组件 playbook；通过同一 roles 同时服务初装和分批安装 |
| `kubekey/builtin/core/roles/ani/<component>/` | 各组件任务、模板、固定 values 和验证材料 |
| `kubekey/ani/` | 完整示例、组件锁定文件、镜像映射、制包输入 |
| `kubekey/scripts/`、内嵌验证资源 | 拆开 kk 构建与物料制作；入口仅薄包装；共同验证实现随 kk 内嵌 |
| `kubekey/docs/` | 使用说明、版本矩阵、执行结果和进度 |

组件 roles 只能使用该 KubeKey fork 已支持的任务模块，不照搬 Ansible `file:` 等语法。目录创建沿用现有 `command: install -d`。不要重写已工作的 kcn/Envoy，除非本轮日志证明一个直接阻塞问题。

## 5. 批次 P0：收尾修复并接通远端工作流

1. 阅读当前源文件和既有收尾方案，复用已完成修改；把新的执行范围和 fedora 规则写入 AGENTS/进度说明。
2. **优先完成第 13 节的独立发布和快速调试路径。** 对现有 artifact 做一次确认后复用，先证明更换 kk 不会触发制包、Hauler 导入或集群重建，再继续组件迭代。不能把这项留到所有组件写完后再处理。
3. 完成共同 smoke 脚本：网络各步骤失败必须非零；Envoy 必须唯一关联到指定 Gateway 和 9090 listener；每次生成新客户端及 UID，不能读取旧 Succeeded Pod。
4. 在 fedora 同步源码，执行收尾方案中必要的负例测试、`go test ./pkg/ani`、`go test -tags=builtin ./pkg/ani` 和变更涉及的 CLI 包测试。用 `make kk` 构建内嵌 builtin 资源的 Linux amd64 二进制；该命令不制作物料包。
5. 开始日志要覆盖安装器第一条引导操作，不能等 Kubernetes/组件启动后才记录。角色/模板/verify 逻辑来自本次 kk；物料由独立 artifact 清单描述。两者不要求同一发布版本，只要求格式和本次所用物料兼容。

首次开始首装任务时使用用户提供的干净现场；若正在已有集群调试，则保留现场，不根据旧日期的快照说明重置三节点。首次连入只做实际执行所需的短检查：系统/架构、管理地址、提权、指定网卡和磁盘、空间/内存。正常可自动准备的依赖由程序准备。只有 SSH 或 sudo 入口本身不可用时，给出针对该用户的准确修复命令。

## 6. 批次 P1：containerd 2.3.4 全离线首装

### 6.1 版本同步

同步以下位置，不能只改一处：

- `pkg/ani/config.go`：生成 `v2.3.4/v1.4.3`。
- `ani/package.yaml`：收集相同 containerd/runc。
- `pkg/ani/config_test.go`：断言新组合及生成配置。
- `builtin/core/roles/defaults/vars/v1.35.yaml`：已为 containerd 2.3.4，但 runc 默认仍是 1.2.6，改成本轮配对。
- `builtin/core/roles/defaults/templates/manifests.yaml` 的 **K8s 1.35 分支**：自动制包默认仍可能为 1.7.13/1.1.12，同步修改。
- `builtin/core/defaults/config/v1.35.yaml`：同步相关示例说明；不调整其他 K8s 版本分支。

containerd 2.3.4 的官方 CI 使用 runc 1.4.3；这是本轮配对依据，不等于宣称它是硬性最低可运行版本。旧 containerd 1.7 成功记录不能当成本轮配对成功。

### 6.2 运行时配置

在现有 containerd role 内为 ANI 的 2.x 路径生成明确的 config version 3，保留未涉及的运行时设置：

- runtime、CNI、`SystemdCgroup=true` 位于 `io.containerd.cri.v1.runtime` 配置树，runtime type 继续 `io.containerd.runc.v2`。
- pause 位于 `io.containerd.cri.v1.images.pinned_images.sandbox`，仍指向当前 Hauler 重写后的固定镜像。
- CNI 的实际字段按 v2.3.4 固定版配置文档生成，使用 `/opt/cni/bin`、`/etc/cni/net.d`，不要混入 1.x 的字段路径。
- 镜像源使用 `config_path=/etc/containerd/certs.d`。为 `<installer-IP>:5000` 写 `hosts.toml`，初始 server 和 host 均明确为 `http://<installer-IP>:5000`，允许 pull/resolve。
- 不同时写旧 `registry.mirrors`。Harbor 阶段需要的 auth-only `registry.configs` 是第 12 节明确说明的兼容策略，不是随意恢复旧模板。
- 若保留上游 1.x 路径，使用独立条件分支，不能全局将版本号改为 3 却继续输出旧 CRI 配置树。

官方仍支持自动转换 config version 2，所以不能把原模板描述成一定不能运行。本轮选择显式 2.x 配置，是为了让之后的镜像交接配置清晰且可测试，不新增独立迁移工具。

### 6.3 制包和第一次安装

1. 在 fedora 下载新 containerd/runc，核对发布校验；如果现有 artifact 还是旧运行时，导出一次新版本。若已经有核实包含 2.3.4/1.4.3 的 artifact，则直接复用，不因重编 kk 再导出。复用 `KUBEKEY_ARTIFACT` 时核对其实际物料；记录目标二进制实际版本。
2. Ubuntu 系统依赖沿用当前 KubeKey 的 Ubuntu 24 离线材料。实际缺包时，在 fedora 的 Ubuntu 24 环境补齐进入现有制包路径；不改成现场在线 APT，不重建 Python/Ansible 控制环境。
3. 将物料传到 `.20` 的 artifact 目录，独立传输 kk 到 binary 目录；已传且已核对的物料无需再传。通过 fedora 登录 `.20`，用第 4 节命令显式选择 kk、artifact 和 workdir。
4. **在第一条引导命令之前隔离三节点公网访问。** 保留管理默认路由和 SSH，使用实验环境已有出口隔离，或仅在这三个节点添加可恢复的独立防火墙规则。允许集群节点、Pod/Service、所选存储网及必要 SSH 流量；同时处理 IPv4/IPv6。不要清空全机防火墙、破坏 CNI 规则、删除默认路由或影响其他主机。
5. 保存隔离规则和有时限的公网连接失败证据；关闭目标机公网代理，不将 fedora 配成下载代理/镜像源/DNS 隧道。fedora 可联网下载和通过 SSH 传包，目标节点始终离线。
6. 初装后，在三节点记录 containerd/runc 实际版本和 CRI 信息；通过 CRI 拉取一个未缓存的验证镜像，再实际启动 Pod。不能用 `ctr pull --plain-http` 或 Hauler `/v2/` 200 代替 CRI 验证。
7. 运行新 smoke：跨节点 PodIP、ServiceIP、DNS、经过真实 Envoy Service 的 HTTP 全部通过。运行两次 `kk ani verify`，保存不同客户端 UID，顺便完成旧收尾方案的新鲜性验证。

**P1 完成标准：明确的 kk + artifact 组合、正式入口、三台空白节点、无公网、installer 在 `.20`，上述数据请求均成功。** 若中途补丁后只能在半安装状态通过，记录“阶段修复通过”，最终干净首装另验；补丁调试阶段不要求每次重新制包或重装。

## 7. 批次 P2：Ceph 与两种网络模式

### 7.1 材料来源

只读复用本地以下目录中的固定材料，将所需文件复制进 installer 源码，再同步到 fedora；不访问原平台：

`/home/chabking/workspace/ani-network-service/docs/runbooks/platform-manual-assets/`

参考说明：`/home/chabking/workspace/ani-network-service/docs/runbooks/platform-manual-install.md`。

主要材料是 Rook CRD/common、CSI Operator、Rook Operator/OperatorConfig/Driver、CephCluster、RBD、CephFS、RGW。复制时保留来源和 hash，删除模板中旧平台 IP、节点名和现场凭据；不要把整个目录直接 apply。CSI sidecar 使用这份 Rook 1.20.7 材料的配套版本。

安装顺序固定：Rook CRD/common → CSI Operator → 等待 CSI CRD Established → Rook Operator/配置/Driver → CephCluster → 等待集群可用 → pools/filesystem/objectstore → StorageClass/bucket 凭据 → 三类存储验证。

### 7.2 网络与磁盘

| 模式 | 行为 |
| --- | --- |
| `shared-management` | `provider:host`；public/cluster 都使用管理网；MON IP 取各节点管理 IP；不配置 ens36 |
| `dedicated` | `provider:host`；public/cluster 都使用专用存储网；为节点写 `network.rook.io/mon-ip=<存储IP>`；程序按规划配置 ens36 |

“专用”指 Ceph 客户端 I/O 和 OSD 复制共同走存储网，与管理网分开，不再增加第四张网卡。不要只把复制网络放到 ens36，却宣称全部存储流量已经分离。

专用模式的现场输入包括每节点 `interface/address` 和同一存储 CIDR。已有历史资料提到 `192.168.100.0/24`，但本轮未确认各节点 IP；执行 AI 连入后读取现有规划，若没有，集中向用户询问一次三台存储 IP/前缀，其他实现继续进行。不得套用旧平台 `172.16.202.10～12`。

安装器按节点逐一配置专用接口：保存原配置 → 写仅描述指定存储接口的 netplan 文件 → `netplan generate` → 只重载/reconfigure 该接口 → 检查本机存储地址和管理 SSH。三台全部配置完成后，再验证存储网两两互通，然后才安装 Ceph；不要在第一台配置后等待另外两台尚未配置的地址。

静态存储接口关闭该接口 DHCP，填入规划的 addresses；不添加默认路由或 DNS，额外路由仅来自用户明确规划。沿用 Ubuntu Server 的实际 networkd 配置，只重配置指定接口，不全局执行网络重启。保留管理 IP、默认路由和 DNS；ens35 保持无主机 IP。配置冲突时展示差异，不自动覆盖管理网络。

先把选定存储 CIDR 加入 kcn 的 intranetNetworks，使 Pod 可到 Ceph MON/OSD/RGW 所在网络；优先在首次部署 kcn 时一并渲染。若通过 components 后补，按现有 kcn 支持路径最小更新，验证 Pod 到存储地址；不能仅凭主机互 ping 认为 CSI 可用。必要的 `rp_filter` 设置只作用于相关接口，结合实际日志确认。

Ceph 仅消费配置列出的三块 `/dev/sdb`，`useAllNodes:false`、`useAllDevices:false`，正常配置不启用自动 cleanup。执行前核对其容量、挂载、分区和既有签名，不碰系统盘、不自动选“第一块空盘”、不自动 wipe 有数据的盘。三 MON、每节点一个 OSD，池副本数 3；不能为了绿色状态降成单副本。

### 7.3 服务和验收

- 创建块 SC，默认名 `ani-block`；CephFS SC，默认名 `nfs`，两者可改名。`nfs` 只是名称，底层为 CephFS；不安装 NFS Server/Ganesha。仅 `ani-block` 设置为默认 SC，消费者仍显式引用所选 SC。
- 建 CephFS active/standby MDS、RGW 及所需对象存储桶；沿用已核验材料的副本配方。不将 RGW 宣称为 AWS S3 全功能兼容。
- 若实际内核为 6.8，沿用 `security.cephx.csi.keyType:aes` 和已验证 RBD features。保存准确认证告警，只有已经解释的告警可记为已知限制；任意其他 HEALTH_WARN/ERROR 不能一律忽略。
- RBD：写入确定内容并落盘，正常卸载后换另一节点重新挂载并校验；不能强制 detach 掩盖故障。
- CephFS：不同节点两 Pod 挂同一 RWX PVC，双向写读并校验。
- RGW：用独立测试桶 PUT/GET/DELETE、核对内容；Milvus/Loki 需要的实际读写继续在后续批次验证。
- 记录三 MON quorum、三个 OSD up/in、PG 状态和 MON/OSD 实际地址，证明所选网络真实生效。

优先用 `dedicated` 完成本轮各组件迭代。`shared-management` 的配置和模板同步实现，在第 13 节下一次干净首装中实际验证；不在已有 PVC/业务数据的 Ceph 上改网来模拟另一模式。

## 8. 批次 P3：证书与基础数据

建议命名空间：cert-manager 用 `cert-manager`，PostgreSQL/Valkey/NATS 用 `ani-platform`。Pod/PVC/service 名称固定，用户使用连接清单即可，不必查找随机资源。

### cert-manager

官方 v1.21.2 Chart/CRD；只使用离线可用的自签根 CA/内部 CA，不使用公网 ACME。安装器生成并保存根 CA，签发一个带实际 SAN 的测试证书，验证证书链、SAN 和 Secret。需要 CA 的 Harbor 使用同一明确的内部信任链。

### PostgreSQL

17.11 单副本 StatefulSet，20 GiB RBD 起步，独立 Secret 和数据库用户。使用镜像自带 psql 验证建表、写入和查询；正常重建 Pod 后读取同一数据。不安装 Patroni、数据库 Operator 或备份平台。

### Valkey

8.1.10 单副本 StatefulSet，2 GiB RBD 起步，开启 AOF、`appendfsync everysec` 和认证。不安装 Redis、Sentinel 或 Valkey Cluster。写入确定的持久值和 TTL 键，重建 Pod 后读持久值并检查过期语义；不据此承诺突然断电零丢失。

### NATS

官方 Chart 2.14.6，cluster 关闭，JetStream fileStore 开启，10 GiB RBD 起步，独立认证。明确收集启用的 reloader 等辅助镜像。测试创建 file storage、replicas=1 的 stream 和 durable consumer，publish ack、consume ack，重建 Pod 后核对消息/消费状态。不安装 NACK。

通用规则：实例重建保留 PVC 和 Secret；等待完成再验证。验证 Job 的工具镜像随包携带，不在目标节点下载客户端。正常重启后的持久化是本期承诺范围，HA/断电/RPO/RTO 单列未验证。

## 9. 批次 P4：Prometheus、Alertmanager、Loki、Fluent Bit

建议命名空间 `ani-observability`。

- kube-prometheus-stack 91.4.0：明确关 Grafana；Prometheus/Alertmanager 各一副本。保留必要 Operator、node-exporter、kube-state-metrics、CRD 和 webhook jobs，并把所有实际镜像纳入包。按实验资源配置较短 retention 与 RBD PVC。
- Loki 3.7.7 / Chart 18.13.1：SingleBinary 一副本，其余 deployment 模式副本显式为 0；replication factor 1；关闭 MinIO，对象存储指向独立 Ceph RGW 桶和凭据。WAL/工作目录需要持久化；不引入额外缓存集群。
- Fluent Bit 5.1.2 / Chart 0.58.2：DaemonSet 读取 containerd CRI 日志，持久记录必要的 tail 状态，输出到 Loki；配置排除明显递归采集，避免采集自己的发送失败日志造成失控增长。
- 指标验收：真实测试应用暴露一个确定指标，Prometheus 实際查询到它；测试告警触发，Alertmanager API 能看到对应告警。不向外部邮箱/聊天渠道发消息。
- 日志验收：测试应用输出唯一标识，Fluent Bit 收集，LogQL 查询同一记录；Loki 正常重建后仍能查询历史记录。不能只验证直接向 Loki push 绕过 Fluent Bit。

资源参数记录在 lab values；不要看到 Pending 就把所有 requests/limits 清空。读取三台实际容量后给出简洁资源表，若不足，具体指出需要增加的内存/磁盘或受阻组件，已通过部分继续保留。

## 10. 批次 P5：Milvus

Milvus 是本轮要实施的组件，不能只留一个 enabled 开关或空角色就宣布完成。采用官方 Chart 5.0.25 与 Milvus 2.6.21，standalone + embedded Woodpecker + Ceph RGW。

已核验正式 Chart 包包含 etcd 子 Chart 8.12.0，顶层覆盖镜像为 `milvusdb/etcd:3.5.25-r1`。保留专用 etcd，但本期 `replicaCount:1`，满足单实例优先；这不意味着 Milvus 有 HA。绝不连接 Kubernetes 控制面 etcd。

以下字段必须显式设置，避免上游默认带入另一个消息队列：

```yaml
cluster: {enabled: false}
image:
  all: {repository: milvusdb/milvus, tag: v2.6.21}
standalone:
  replicas: 1
  messageQueue: woodpecker
  persistence:
    enabled: true
    persistentVolumeClaim:
      storageClass: ani-block
      accessModes: ReadWriteOnce
streaming:
  enabled: true
  woodpecker:
    embedded: true
    storage: {type: minio}
woodpecker: {enabled: false}
minio: {enabled: false}
pulsar: {enabled: false}
pulsarv3: {enabled: false}
kafka: {enabled: false}
externalPulsar: {enabled: false}
externalKafka: {enabled: false}
externalEtcd: {enabled: false}
etcd:
  enabled: true
  replicaCount: 1
  image: {registry: docker.io, repository: milvusdb/etcd, tag: 3.5.25-r1}
  persistence: {enabled: true, storageClass: ani-block}
  volumePermissions: {enabled: false}
externalS3:
  enabled: true
  host: REPLACE_WITH_RGW_SERVICE_DNS
  port: 80
  bucketName: REPLACE_WITH_MILVUS_BUCKET
  rootPath: milvus
  useSSL: false
  useIAM: false
  cloudProvider: aws
  useVirtualHost: false
```

执行时镜像路径、StorageClass、RGW 端口/region/凭据通过模板替换，不把示例值直接照抄。`streaming.woodpecker.storage.type:minio` 是 S3 驱动名称，配合 externalS3 指向 RGW，**不是安装 MinIO**；`woodpecker.enabled:false` 关闭独立 Woodpecker 服务，不是关闭 embedded WAL。`pulsarv3.enabled` 上游默认 true，必须关闭。

在 fedora 渲染确认实际 workload 数量、镜像和配置；不猜测 values 已生效。Milvus 客户端做成随包提供的验证镜像，在 fedora 准备固定 SDK/依赖，不给目标宿主机加 pip 安装流程。

测试建立集合、插入固定向量、flush、建索引、查询预期 ID；正常重建 Milvus 后再次查询，并保留 Woodpecker/RGW 的实际写入和恢复日志。失败时先检查生成配置、RGW/S3 行为和 WAL 日志；不能自行换 MinIO、Kafka、Pulsar或 RocksMQ 掩盖问题。有真实不兼容证据才提出替代方案。

## 11. 批次 P6：KubeVirt、CDI、Volcano、LWS

- KubeVirt 1.9.0、CDI 1.66.1 使用官方固定发行清单，分别等待 CRD Established、Operator 和 CR 就绪；收集动态启动的 virt-launcher、handler、API、CDI importer/cloner/uploadserver 等镜像，不能只打包 Operator。
- 明确 Ceph RBD 的 StorageProfile、access mode、volumeMode，先使用实际支持的组合。不要强行把 RWO 伪装成 RWX 来通过验证。
- CDI 从 `.20` 的离线 HTTP 文件服务导入 CirrOS `0.6.2` 的 `cirros-0.6.2-x86_64-disk.img`；磁盘文件和在 fedora 计算记录的 SHA256 都在总包中。HTTP 服务仅发布包内 `vm-images/`，不暴露现场凭据目录。不要假定该下载目录存在名为 `SHA256SUMS` 的上游附件。
- 测试真正创建 VM、观察 guest 启动、在 guest 写入确定内容、正常重启 VM 并读回。目标没有 `/dev/kvm` 时，允许显式选择 **lab 的软件模拟模式**完成基础功能验证，记录为 emulation；不能声称已通过 KVM 加速、嵌套虚拟化或生产性能测试。
- Volcano 1.15.2：固定官方安装材料和镜像，运行一个 CPU-only Volcano Job。先用 minAvailable 条件观察 gang 等待，再满足条件完成任务，保存调度事件；只看 scheduler Ready 不算验证。不要引入 GPU 来完成本项。
- LWS 0.9.0：固定官方发布材料，运行一组 leader + worker，应用内部通过预期服务名实际互通，记录响应；不只检查 Pod 数量。
- 不把普通 VM 上的缺 GPU 当作整批失败。GPU Operator 26.7.0 留在候选表，未选择真实型号/驱动前不安装、不拉取整个驱动矩阵、不标 verified。

## 12. 批次 P7：Harbor 与最后的镜像交接

### 12.1 Harbor 首装

使用官方 Chart 1.19.2，Harbor 2.15.2。采用 Chart 内置的单实例数据库和 Valkey，镜像分别是 `harbor-db:v2.15.2`、`valkey-photon:v2.15.2`；values 名称 `redis.type:internal` 仍保留，实际不是部署 Redis 产品。

```yaml
database:
  type: internal
  internal:
    image: {repository: docker.io/goharbor/harbor-db, tag: v2.15.2}
redis:
  type: internal
  internal:
    image: {repository: docker.io/goharbor/valkey-photon, tag: v2.15.2}
persistence:
  enabled: true
  resourcePolicy: keep
  imageChartStorage: {type: filesystem}
  persistentVolumeClaim:
    registry: {storageClass: ani-block, accessMode: ReadWriteOnce}
    database: {storageClass: ani-block, accessMode: ReadWriteOnce}
    redis: {storageClass: ani-block, accessMode: ReadWriteOnce}
    jobservice:
      jobLog: {storageClass: ani-block, accessMode: ReadWriteOnce}
```

给 registry 至少 50 GiB 起步，根据实际镜像总量调整；DB、缓存、joblog 单独 PVC。关闭本轮不验证的 Trivy 扫描和数据库下载，避免隐式公网依赖。Harbor 首装自身所有镜像仍来自 Hauler。

本期选 **HTTPS NodePort**，使用 `.20` 管理 IP 加一个未占用固定端口，例如 30443；证书 SAN 包含实际入口 IP，`externalURL` 与之相同。节点信任包内/运行目录中的内部 CA，不依赖集群 CoreDNS 才能找到 Harbor，也不要求先配置外部 LB/Ingress。不得强行占用已用端口。

对应固定 Chart 字段如下，与前面的持久化 values 合并。Secret 在 Harbor namespace 中由 cert-manager 签发，等待 Ready 后再安装 Harbor；真实端口变更时同时更新证书入口说明、externalURL 和 containerd 认证/hosts 地址。

```yaml
expose:
  type: nodePort
  tls:
    enabled: true
    certSource: secret
    secret: {secretName: ani-harbor-tls}
  nodePort:
    ports:
      https: {port: 443, nodePort: 30443}
externalURL: https://172.16.101.20:30443
trivy: {enabled: false}
```

先验证私有测试项目、上传镜像和目标节点拉取；完成所有选中组件验证后才能进入交接。未来安装 ANI 时，交接调用点必须顺延到 ANI 安装及验证完成之后，不能因为 Harbor Ready 就提前停止 Hauler。

### 12.2 保持镜像引用，切换运行时仓库端点

本期选择明确且有限的方式：**保留工作负载的 `<installer-IP>:5000/<project>/<repository>` 镜像引用，通过 containerd hosts.toml 将该镜像命名空间映射到 Harbor。** 不批量改 Kubernetes 静态 Pod、kcn、Rook 和各服务的镜像引用。

现有映射都是至少两级路径。新增镜像也必须遵循此规则；按第一段在 Harbor 建私有项目，复制镜像时保持 repository、tag 和 digest。上传使用专用推送身份；节点只使用对本期项目有 Pull Repository 权限的 system robot。不要自动把项目改公开，也不要给所有节点分发 Harbor 管理员密码。

containerd 2.3.4 明确采用 local image pull 兼容模式，合并以下配置，保留其他运行时字段：

```toml
[plugins."io.containerd.cri.v1.images"]
  use_local_image_pull = true
[plugins."io.containerd.cri.v1.images".registry]
  config_path = "/etc/containerd/certs.d"
[plugins."io.containerd.cri.v1.images".registry.configs."172.16.101.20:30443".auth]
  username = "REPLACE_WITH_HARBOR_RETURNED_ROBOT_NAME"
  password = "REPLACE_WITH_ROBOT_SECRET"
```

认证键必须是 **实际 Harbor host:port**，不是原 Hauler 地址。containerd 2.3.4 支持 `config_path + auth-only configs`；它已弃用但仍可用，和不允许并存的旧 mirrors 不同。不要自造 hosts.toml username/password 字段或 Basic header 认证。

原命名空间目录 `/etc/containerd/certs.d/172.16.101.20:5000/hosts.toml` 改为：

```toml
server = "https://172.16.101.20:30443"
[host."https://172.16.101.20:30443"]
  capabilities = ["pull", "resolve"]
  ca = "/etc/containerd/certs.d/ani-harbor-ca.crt"
```

server 不能保留 Hauler，也不能省略，否则 fallback 会让交接验证失真。分节点保存旧配置、写新配置、重启 containerd 并核对节点恢复；不重启整群机器。凭据文件权限 `0600`，日志脱敏。

### 12.3 交接验收与失败处理

1. 全量复制本期镜像，逐项核对 digest；使用随包提供的固定镜像复制工具，安装节点不联网获取工具。
2. 为三节点准备不同的、真正含未缓存内容的私有验证镜像；它们在 fedora 构建并进入离线物料，但此前不导入目标 containerd。不要仅改 tag 来伪装未缓存内容，也不通过清空正在使用的系统/CNI/存储镜像缓存来制造这个条件。
3. 完成三节点映射和 robot 配置后停止 Hauler；每个节点通过**不带 `--creds` 的 CRI 拉取**，使用原 `<installer-IP>:5000/...` 引用成功拉取，再运行 Pod。
4. 保存 Harbor 请求日志、实际目标节点、镜像 digest 和 CRI 结果；任何一项失败，启动并确认旧 Hauler 供应，恢复本轮保存的 hosts/config，对实际修改过的节点逐台重启 containerd，再通过 CRI 拉取确认旧供应恢复。不能只恢复磁盘文件却保留进程中的新配置。保留失败证据，不继续宣称交接成功。
5. `kk ani verify` 按保存的当前供应阶段验证 Hauler 或 Harbor，不因成功停止 Hauler而误报失败。用一个小的交接状态文件保存完成阶段和证据路径即可，不建设通用状态机。

交接只承诺当前运行中的集群可以继续拉取镜像。保留原离线包和恢复 Hauler 所需配置；不承诺整群断电后的自动自举和 Harbor HA，这些是用户已同意暂缓的范围。

## 13. 制包改动和最终真实验证

### 13.1 保留一个制作入口，按目录组织

沿用 `scripts/build-offline.sh`，保留现有 KubeKey package.yaml 输入，增加简单的 `--profile base|platform` 和 `--output` 选项；执行者只需运行一个制作命令。`base` 只收集已有 Kubernetes/kcn/Envoy 及验证材料，用于 P1；`platform` 收集本轮全部启用组件，用于后续和最终总包。必要工具缺失时由 fedora 上的准备步骤自动下载固定版本，不能让现场用户逐个准备几十个路径。Go/Hauler/Helm/crane 版本记录到包内清单。

在 components.lock.yaml 中记录组件到 images.tsv 条目的引用关系，制包按 profile 取并集；镜像来源、重写地址和 digest 仍只在 images.tsv 中定义。不要为了 P1 先下载全部基础组件，更不要为普通 VM 构建默认拉取 GPU 驱动矩阵。

```text
ani-offline-ubuntu24-amd64-<bundle-id>/
  bin/                 # kk、hauler、helm、必要的镜像/验证工具
  packages/            # kubekey-artifact.tgz；Ubuntu 系统材料按既有机制
  images/              # images.haul.tar.zst、images.tsv
  charts/<component>/  # 精确版本 tgz，已包含需要的子 Chart
  manifests/ani/       # 与 builtin 同一版本的 roles/templates
  config/              # 制包配置、组件锁定表、无凭据的示例
  verification/        # 组件验证脚本和数据
  vm-images/           # CDI 导入用固定磁盘和校验
  licenses/
  install.sh
  components.sh
  verify.sh
  cluster.example.yaml
  README.md
  SHA256SUMS
```

先形成上述目录，再生成一个总压缩包及总包 SHA256；不要把文件全放根目录。制包读取实际传入的配置并复制它，修正当前脚本可能始终复制默认 `ani/package.yaml` 的问题。安装日志、work、registry-data、访问凭据、kubeconfig 不能进入总包或 SHA256SUMS 静态清单。

新增组件分批打包时，可复用 **已经核对内容** 的缓存，不重复下载全部镜像；每次输出新 bundle-id。运行中的 Hauler store 不能由构建脚本清空或覆盖。当前 registry 是 readonly 服务，不具备已经验证的热加载能力，不准假定向它 push 或创建 staging 目录就能供应新镜像。

供应更新采用明确流程：在 `.20` 独立新目录加载包含旧镜像和新增镜像并集的完整归档 → 使用独立 store/registry-data 和临时端口启动短时检查服务并核对全部 manifest → 停止检查服务 → 保存旧 systemd 配置 → 短暂停止正式 Hauler → 切换到新目录并以原地址/端口启动 → 核对供应 → 执行 components。失败恢复旧配置和旧目录；不得删除旧供应。临时检查端口不写入工作负载镜像引用。这个流程可以由组件入口自动调用，但绝不能调用当前 RunInstall 的清空 store + create cluster 路径。

每批已选组件使用当前累计的 profile，不支持交接完成后又偷偷切回 Hauler。Harbor 交接始终留在最终批次。

在 fedora 从包内 Chart、渲染结果和固定 Operator 配置收集镜像：包括 init、hook、证书 jobs、CSI sidecar、KubeVirt/CDI 动态镜像及所有验证客户端。发现漏镜像，修制包输入并重新生成归档，不能在目标机临时直连公网补镜像。用新 store 从交付归档恢复一次以检查物料完整性，不把 fedora 的既有缓存当作总包内容。

全部镜像 manifest 存在与真实安装能成功，是两个不同的结果。镜像摘要锁定和包校验沿用现有简单清单；不为本轮新建签名平台、SBOM 服务或供应链验证框架。

### 13.2 最小测试集合

全部在 fedora 执行：

- shell 语法、Go 相关包测试、builtin CLI 测试、实际 `make kk`；只跑本轮涉及范围，不跑整个上游测试矩阵或无关 race/性能测试。
- 配置：旧现场配置保持兼容；组件开关生效；缺少已选组件必要依赖时报错；未选组件不要求配置。
- 模板：containerd 插件字段/HTTP registry/Harbor auth 路径；Ceph 两网络模式不会交叉改 ens34/ens35；StorageClass 名正确传给消费者。
- Chart：使用精确包和本期 values 实际渲染，检查意外 MinIO/Pulsar/Kafka/Grafana/Redis 镜像、缺失镜像、重复 CRD/CSI。
- 行为：真实 smoke 的失败退出码/新 UID/Envoy 唯一选择；正常重跑组件不删 PVC、不重置口令、不重建 Kubernetes。
- artifact 与包内实际二进制/Chart/磁盘文件一致，包移动路径后入口仍可解析相对材料。

不要添加只匹配源码字符串的大量“测试通过”检查来替代真正模板渲染或数据请求。

### 13.3 两轮现场验证安排

**第一轮，当前已恢复 ISO 的三台：** 先 P1 新 runtime 首装；按 P2～P7 累积集成并验证，Ceph 优先 dedicated。目标始终断网，所有安装都从 `.20` 正式入口运行。分批成功后留存状态与日志，失败时原位诊断并修源码，不随意重置集群。

**第二轮，最终包干净首装：** 第一轮所有组件和最终包完成后，通知用户再次恢复这三台的 ISO 快照，再使用最终总包执行一次完整 `install.sh`。当前这次“已还原”只授权/描述第一次起点，不能假装未来自动恢复已经发生，也不操作未授权的虚拟化宿主机。待用户恢复期间继续完成文档、制包与未依赖集群的检查。

第二轮使用 `shared-management` 验证另一种 Ceph 首装模式，其他组件选择和版本保持最终表一致，同时证明总包能一次安装完整栈。报告分别记录：dedicated 分批实装结果、shared-management 最终干净首装结果。若最后的代码调整影响 dedicated 路径，应追加对应验证；没有影响则不用重复所有测试。

如果没有第二次快照恢复，第一轮交付仍有用，但“最终包干净首装”和尚未跑过的网络组合必须为 `not_verified`；不能把第一轮逐次补包的成功改写成最终包独立首装成功。

## 14. 验收、记录和完成条件

为每批建一个小的结果目录，保存实际命令、时间、退出码、包 ID、关键日志和本次新建测试资源信息。使用 tee 时保留原命令退出码；禁止 `|| true` 吞掉安装/验证错误。验证资源使用专用 namespace/前缀，本轮保留失败现场；不得删除别的任务资源。

建议维护 `kubekey/docs/platform-iteration-results.md` 和 `kubekey/docs/component-matrix.md`，每批追加 `kubekey/docs/progress.md`。只用 `pass`、`fail`、`not_verified`，并列出所测配置，不用“基本可用”代替结果。

| 项目 | pass 必须具备的证据 |
| --- | --- |
| 代码/构建 | fedora 执行的实际测试、builtin 编译、当前源码和包版本对应 |
| 新运行时 | 三节点实测 containerd 2.3.4/runc 1.4.3；CRI 未缓存拉取与 Pod 运行 |
| 离线自举 | 初始干净节点、引导前断网、installer 在 .20、正式入口全过程日志 |
| kcn/Envoy | 新客户端跨节点 Pod/Service/DNS 请求及实际 Envoy 入口 HTTP |
| Ceph 每种模式 | MON/OSD 地址、状态、RBD 跨节点重挂、CephFS 双向读写、S3 读写 |
| PG/Valkey/NATS | 每个服务真实写入和正常 Pod 重建后的数据/状态回读 |
| 证书 | 实际签发证书的 SAN/链/Secret |
| 指标/告警/日志 | 应用指标查询、真实测试告警、Fluent Bit 到 Loki 的唯一日志查询 |
| Milvus | 向量插入/flush/索引/检索/重启恢复，明确 embedded Woodpecker+RGW |
| KubeVirt/CDI | 离线磁盘实际导入、guest 启动和重启后文件；注明 KVM/emulation |
| Volcano/LWS | gang 调度任务完成、leader/worker 实际通信 |
| Harbor/交接 | 私有项目推拉、全量 digest 对齐、停 Hauler 后三节点无手工凭据 CRI 拉取 |
| 最终总包 | 第二轮空白节点完整安装及所有选中组件的对应验证 |

GPU、生产 HA、升级、全站断电、未执行的组合不在本期通过结论中。Ceph AES 已知告警独立说明。若一个组件受实际兼容问题阻塞，写清失败位置和可复现命令，继续完成不依赖它的批次；不能从清单悄悄删除组件再宣布全部完成。

最终交付：本地源码改动、fedora 测试/制包路径与总包校验、每批结果和版本矩阵、完整现场配置的受限保存位置、脱敏连接清单、未完成项及真实原因。不要提交或推送；用户确认下一步后再处理发布。

## 15. 固定资料入口

这些是版本/字段依据。只经 fedora 下载或读取；执行时保存实际附件和摘要，不能将链接本身当成已经验证安装。

- containerd 2.3.4：[配置](https://github.com/containerd/containerd/blob/v2.3.4/docs/cri/config.md)、[hosts](https://github.com/containerd/containerd/blob/v2.3.4/docs/hosts.md)、[CRI registry](https://github.com/containerd/containerd/blob/v2.3.4/docs/cri/registry.md)、[runc CI 版本](https://github.com/containerd/containerd/blob/v2.3.4/script/setup/runc-version)。
- Rook 1.20.7：[支持范围](https://github.com/rook/rook/blob/v1.20.7/Documentation/Getting-Started/maintenance-and-support.md)、[固定 Ceph values](https://github.com/rook/rook/blob/v1.20.7/deploy/charts/rook-ceph-cluster/values.yaml)。实际安装复用本地已经核验的 YAML。
- Milvus：[正式 Chart 5.0.25](https://github.com/zilliztech/milvus-helm/releases/download/milvus-5.0.25/milvus-5.0.25.tgz)、[固定 values](https://github.com/zilliztech/milvus-helm/blob/milvus-5.0.25/charts/milvus/values.yaml)、[配置模板](https://github.com/zilliztech/milvus-helm/blob/milvus-5.0.25/charts/milvus/templates/config.tpl)。
- Harbor：[正式 Chart 1.19.2](https://helm.goharbor.io/harbor-1.19.2.tgz)、[固定 values](https://github.com/goharbor/harbor-helm/blob/v1.19.2/values.yaml)、[system robot](https://goharbor.io/docs/2.15.0/administration/robot-accounts/)。
- cert-manager：[支持范围](https://cert-manager.io/docs/releases/)、官方 OCI 仓库 `quay.io/jetstack/charts/cert-manager`。
- NATS：[固定 Chart](https://github.com/nats-io/k8s/blob/nats-2.14.6/helm/charts/nats/Chart.yaml)。
- Helm：[3.20.0 Linux amd64](https://get.helm.sh/helm-v3.20.0-linux-amd64.tar.gz)、[发布校验](https://get.helm.sh/helm-v3.20.0-linux-amd64.tar.gz.sha256sum)；crane：[go-containerregistry 0.20.6 Linux x86_64 工具包](https://github.com/google/go-containerregistry/releases/download/v0.20.6/go-containerregistry_Linux_x86_64.tar.gz)。
- KubeVirt：[1.9.0 Operator](https://github.com/kubevirt/kubevirt/releases/download/v1.9.0/kubevirt-operator.yaml)；CDI：[1.66.1 Operator](https://github.com/kubevirt/containerized-data-importer/releases/download/v1.66.1/cdi-operator.yaml)。相应 CR 与 virtctl 使用同一固定发行版本。
- VM 磁盘：[CirrOS 0.6.2 x86_64](https://download.cirros-cloud.net/0.6.2/cirros-0.6.2-x86_64-disk.img)。
- Volcano：[1.15.2 固定安装清单](https://github.com/volcano-sh/volcano/blob/v1.15.2/installer/volcano-development.yaml)；LWS：[0.9.0 固定发布清单](https://github.com/kubernetes-sigs/lws/releases/download/v0.9.0/manifests.yaml)。Volcano 文件中的开发版命名不允许传递成浮动镜像，按实际内容锁定并重写全部引用。
- 其他 Chart 使用上述版本表中的官方发行包，执行 AI 在 fedora 核对 Chart.yaml/values/CRD 后将准确 URL、SHA256 和全部镜像写入锁定文件；不换成第三方一键 Chart。

## 16. 可直接发给执行 AI 的启动指令

```text
执行 /home/chabking/workspace/ani-installer/docs/ani-installer-platform-iteration-execution-plan.md。

目标是在已经跑通的 KubeKey fork 上升级 containerd/runc，补全该方案定义的基础组件，按批通过安装器完成真实离线安装和数据验证。不要重新设计安装架构，也不要只生成代码后就结束。

本地仅阅读/编辑代码文档、发起 SSH 文件传输。所有格式化、代码生成、测试、编译、下载、Chart 渲染、镜像处理、制包和运行操作都必须经 SSH config host fedora。不得本地运行这些任务，不得换构建机。

唯一测试节点是172.16.101.20、172.16.101.21、172.16.101.22，用户已恢复ISO；.20承担installer、Hauler和集群节点。所有目标命令经fedora发起。账号及本地临时凭据在方案第2节，提交前必须脱敏，但本任务不提交、不推送。不得访问.10～.12的已有平台，也不得检查ANI源码。

先复用或完成旧收尾方案的真实探测修复，再执行P1运行时首装，随后逐批集成组件。旧方案“只在现有集群补验”的假设已经失效，不能因此禁止本轮首装。Ceph使用用户确认的ens36与空白/dev/sdb；不碰系统盘，ens35不给主机IP。专用存储IP缺失时集中问一次，其他代码继续实现。

全部正常安装操作由程序完成，不把大量预检和补依赖命令交给用户。有错误先读日志，修源码与制包，再正式重试；不得手工kubeadm、临时联网补镜像、换组件/WAL或吞错误刷成功。组件单实例优先；Milvus明确embedded Woodpecker+RGW；Harbor使用其固定DB/Valkey，最后才交接镜像。GPU、HA、整群断电恢复不在本轮承诺中。

每批更新进度和pass/fail/not_verified证据，所有判断基于本次真实请求与数据回读。最后准备最终总包，待用户再次恢复这三台ISO后执行完整首装；未恢复就明确留为not_verified，不能借历史结果代替。先通读方案，再开始实施，不另写一套庞大计划或验证框架。
```


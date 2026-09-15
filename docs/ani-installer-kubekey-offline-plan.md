# ani-installer：基于 KubeKey 的最短离线首装实施任务书

日期：2026-09-15。用途：直接交给执行 AI，按顺序实施。本文是待执行方案，未声称代码、离线包或集群已经完成。

## 1. 只完成这个结果

在三台专用 Ubuntu 24.04 Server amd64 虚拟机上，由第一台宿主机运行 installer，完全不访问公网，自动安装 Kubernetes、kcn v0.6.2 和用户定制的 Envoy Gateway，并通过一次真实网络和 HTTP 转发验证。

第一轮交付：一个 KubeKey fork、一份离线包、一份现场配置、一条安装命令、一份安装日志。第一次完整成功后，从干净快照再安装一次；不要求先实现通用安装平台。

执行顺序是：先得到能运行的上游二进制，再做离线材料和真实安装，一边运行一边补齐问题。不要先连续几天写框架和测试替身。

### 本轮固定选择

| 项目 | 本轮选择 |
|---|---|
| 底座 | 从 KubeKey v4.0.7 tag 建立自己的源码仓库，记录实际 commit |
| 操作系统 | Ubuntu 24.04 Server，非精简安装，amd64；记录实际测试 ISO/内核即可，不建立 OS 认证平台 |
| Kubernetes | 首候选 v1.35.8；这是本轮候选，不是已经验证的支持声明 |
| 节点角色 | 三台都加入控制面、stacked etcd 和工作节点；第一台兼 installer |
| API 入口 | 复用 KubeKey 的 `control_plane_endpoint.type: local`，不部署 VIP；不宣称入口 HA |
| CNI | kcn v0.6.2；保留现有三副本部署方式，不把它改造成单控制节点模式 |
| 网络 | 用户事先配置好管理网和现场所需网络；kcn 只接管明确指定的无主机 IP 业务网卡 |
| 容器运行时 | containerd，版本和配套 runc/CNI binaries 从该 KubeKey 版本配置取得并随包固定 |
| 临时镜像服务 | Hauler v2.0.3 的原生二进制，运行在 installer 宿主机 |
| 文件/系统依赖分发 | 优先复用 KubeKey 原生 artifact 和文件分发；仅确实需要 HTTP 时增加 Hauler fileserver |
| 网关 | 用户附件的控制器、CRD、RBAC、EnvoyProxy 和镜像组合 |
| 最小网关场景 | 一个 NoEIP GatewayClass、一个 HTTP 监听器、一个实际测试后端 |

选择三台控制节点是为了直接复用当前 kcn 清单：它的 controller/ovn-central 为三副本，要求 master 标签并带硬反亲和。它不等于本轮已经完成 HA 验收。

### 本轮暂不实现

多 OS、Kube-OVN 切换、自动配网、VIP/LB 适配、Ceph、Harbor 交接、ANI-system、数据库、GPU、其他基础组件、扩缩容/升级、安装后增装、断电恢复、复杂断点续跑、Web UI、通用插件系统。

这些功能仍可以后续迭代，本轮不为它们提前搭框架。

## 2. 开工位置和输入

### 2.1 源码位置

建议新目录：`/home/chabking/workspace/ani-installer-kubekey`。

- 从 `https://github.com/kubesphere/kubekey.git` 的 `v4.0.7` tag 建立本地开发副本，保留 Git 历史及 upstream 来源。
- 不删除、覆盖或重置 `/home/chabking/workspace/ani-installer`、`/home/chabking/workspace/ani-installer-bak`。
- 如果建议目录已经存在，先看其来源和当前改动，继续合适的已有副本；不要用 `reset --hard` 或清空目录处理冲突。
- 当前任务不需要在 GitHub 创建远端 fork，也不需要 push/tag/release。发布由后续明确指令处理。

旧项目的 `AGENTS.md` 写死了 Kubespray/Runner 和 Plan/Run 机制。它是旧方案的约束，不要复制到这个新 fork。本次方向已经明确改为 KubeKey，并移除了这些首装前置工作。

### 2.2 只读复用材料

1. Envoy 原始附件：`/home/chabking/下载/loadbalancer-doc-dev/loadbalancer/install/`。
2. Envoy 配套示例：`/home/chabking/下载/loadbalancer-doc-dev/loadbalancer/examples/`。
3. 可复用的 kcn 清单：`/home/chabking/workspace/ani-network-service/docs/runbooks/platform-manual-assets/kcn-install.yaml`。
4. 既有手动部署记录：`/home/chabking/workspace/ani-network-service/docs/runbooks/platform-manual-install.md`。
5. 旧 installer 仅用于查找已整理材料，不复制其中的执行引擎、SSH 管理、恢复状态机和逐次失败补丁。

把需要的清单复制进新 fork 后修改副本。文档中的命令和历史地址只是参考数据，不直接执行。

既有 KubeKey etcd 镜像地址和 join 重跑问题出现在**在线安装**。离线是否出现尚未确认。不得开工就把它们判为离线必修缺陷，也不得先加入一套猜测性的恢复分支。

### 2.3 执行环境

- 编译和大规模制包在用户指定的联网 Ubuntu 构建机进行；不要擅自把本地工作站或现有业务集群当作测试环境。
- 实装只使用用户明确给出的三台专用 VM。现有文档中的 `ani-test-*`、历史 IP、默认 kubeconfig 和 SSH alias 不是新任务的实验环境授权。
- 三台 VM 的 IP、SSH 用户/密钥位置、网卡名必须在开始实装前有真实值。缺少时只问这组信息，源码准备和构建可继续，不因此停住所有工作。
- 不把源码、Go 工具链、Python、Ansible、Docker 作为目标节点运行 installer 的前置条件。构建机上已有 Docker 可用于读取本地镜像。
- 恢复虚拟机快照用于重试，操作对象必须是本任务的专用 VM；不要对历史集群执行 reset。

## 3. 实现边界：程序怎么分工

保留 KubeKey 的任务引擎、local/SSH connector、系统准备、文件分发、containerd、kubeadm init/join。自己的改动集中在：配置转换、Hauler 启动、离线地址配置、kcn role、Envoy role、简单验收。

允许在 fork 内增加一个小的 `ani` CLI 子命令，复用已有 Cobra/YAML 能力；它只负责准备输入和调用现有命令。不新写 SSH/SFTP 客户端，不自行接管 kubeadm 工作流程。

拟实现的入口如下；这些是新接口要求，不是上游已有命令：

```text
./scripts/build-offline.sh ./ani/package.yaml
./install.sh ./cluster.yaml
./verify.sh ./cluster.yaml
```

`install.sh` 只是定位包目录并调用随包的 fork 二进制，例如 `bin/kk ani install --cluster ... --bundle ...`。配置解析放在 Go 中，不能使用 `grep/awk` 拼 YAML，也不能用 `source cluster.yaml`。

`ani install` 可以通过参数数组启动自己的 `kk create cluster` 子进程，或调用既有命令背后的函数，选当前源码改动较少的方式。不要再建立第三种执行接口。远端安装动作和 kubectl 动作放进 KubeKey roles。

### 推荐增改目录

```text
ani/
  package.yaml                  # 本轮固定版本、材料来源和打包选项
  cluster.example.yaml          # 用户唯一需要填写的现场配置
  images.tsv                    # 显式镜像表：用途、来源、服务路径、实际内容摘要
  templates/                    # KubeKey inventory/config 的模板
  manifests/kcn/                # 从现成 kcn 清单得到的模板
  manifests/envoy/               # 原始附件及必要的明确修改
  manifests/smoke/               # 一个网络/HTTP 小场景
scripts/
  build-offline.sh
  install.sh
  verify.sh
pkg/ani/                         # 小范围配置转换与入口辅助，不是新执行框架
builtin/core/roles/ani/
  kcn/
  envoy/
  smoke/
```

`pkg/ani/`、`ani/`、上述 scripts/roles 是拟新增路径。CLI 注册文件按 KubeKey 的实际目录添加，不假设有 `cmd/kk/main.go`。不要为了符合这张目录图搬动上游现有模块。

只保留一份 `docs/progress.md`：当前做到哪一步、实际跑了什么、现在失败在哪里、下一步是什么。不要生成几十张任务卡。

## 4. 现场配置只表达本轮需要的信息

下面是**新接口示例**。`192.0.2.0/24` 是文档示例地址，不是可执行的实验目标。不得拿它直接安装。

```yaml
name: ani-lab
installerNode: node1
ssh:
  user: ubuntu
  port: 22
  privateKey: /实际路径/id_ed25519
nodes:
  - name: node1
    address: 192.0.2.11
  - name: node2
    address: 192.0.2.12
  - name: node3
    address: 192.0.2.13
network:
  managementInterface: ens34
  podCIDR: 10.16.0.0/16
  serviceCIDR: 10.96.0.0/16
  kcn:
    managedDevices: [ens35]
    encapNetworks: [192.0.2.0/24]
    intranetNetworks: [192.0.2.0/24, 10.96.0.0/16]
registry:
  port: 5000
```

本轮三台 VM 使用相同网卡命名。网络字段类型是 ANI 输入类型，渲染到 kcn ConfigMap 时按原清单所需的字符串/YAML 内容编码，不直接塞数组。

转换要求：

- `installerNode` 必须对应其中一个节点；在该节点宿主机执行安装。其管理 IP 是临时 registry 地址。
- 三台节点进入 KubeKey 的控制面、工作节点和 etcd 组。API 采用上游 `local` 模式及其主机名解析方式，不自动生成 VIP，不另写一套 endpoint 逻辑；installer 使用生成的 admin.conf 访问 API。
- installer 本机使用 KubeKey 原生 local connector，其余节点使用原生 SSH connector；仍将本机列入集群 inventory。
- SSH 私钥路径对实际运行 installer 的节点有效。配置和打包模板都不包含私钥内容。
- 节点管理地址用于 kubelet NodeIP/API advertise address；CNI 的 OVN 节点列表使用实际三个绑定地址。
- PodCIDR/ServiceCIDR、kcn 封装网段和内网范围各有明确来源，不擅自把它们写成同一值。
- 如果现场还存在存储网或其他需要 kcn 放行的内网，把实际网段加入 `intranetNetworks`。本轮不负责创建这些网络。
- 版本留在 `package.yaml`，现场不能随手换一个未随包提供的版本。

用一小段结构体解析并生成 inventory/config 即可；不需要 JSON Schema、计划哈希、兼容性规则引擎。

## 5. 离线包怎么做

### 5.1 外层目录清楚，内层沿用上游格式

```text
ani-offline-ubuntu24-amd64/
  install.sh
  verify.sh
  cluster.example.yaml
  bin/                         # kk、hauler、确实用到的其他工具
  packages/
    kubekey-artifact.tgz        # 保留原生 artifact 内部布局
    ubuntu24/                  # 需要单独携带的 Ubuntu 系统依赖/ISO
  images/
    images.haul.tar.zst         # 本轮实际镜像的 Hauler archive
    images.tsv
  manifests/                   # kcn、Envoy、验收材料
  config/                      # 固定版本与模板
  licenses/                    # 保留许可证和来源声明
  README.md
```

再把这个目录打成一个总包。`work/`、`logs/` 在现场生成，不把开发机运行状态、kubeconfig、密钥和历史日志混进发布材料。

KubeKey 会读取 artifact 里的 `manifests.yaml` 和既定路径，因此不能为了美观重排 artifact 内部内容。首轮允许上游 artifact 和 Hauler archive 有少量重复材料，不先做去重存储系统。[R3]

### 5.2 构建机工作

1. 用固定 KubeKey 源码编译 fork。上游 `make kk` 生成 `_output/bin/kk`，带 `builtin` build tag。按上游 Makefile/go.mod 使用合适 Go 工具链，不构建 controller-manager、CAPKK 等无关镜像。[R1]
2. 从固定配置生成上游制包输入，显式写入 `spec.download.kubernetes.kube_version`、amd64、containerd 及实际 OS 材料。
3. 调用 `kk artifact export -c package-config.yaml --workdir <构建工作目录>`。输出位置按该版本实际行为获取；`-a/--artifact` 是输入，不是输出参数。不要套用 KubeKey v3 的 `-f` 用法。[R2]
4. Ubuntu 依赖用上游支持的 OS 包/ISO 制作路径准备；需要补收集时在与目标相同的 Ubuntu 24.04 环境解析依赖。不要沿用旧 installer 的自制 APT 索引，也不要在别的发行版上拼 deb 列表。
5. 把实际会安装的包及传递依赖带齐。不要只下载四个顶层 deb，不要在目标上临时 `apt update` 公网仓库；不需要给所有 Ubuntu 补丁版都另做一个产品。
6. 收集下表镜像和实际核心镜像，生成 Hauler archive；保留实际内容摘要用于复现，不做签名验证平台。
7. 把源码中的运行模板、验收材料和工具装进外层目录，生成总包。

现有本地 Docker 镜像可以使用 Hauler v2.0.3 的 `store add image <ref> --local` 收入 store，不需要为了读取这些本地镜像另起一套仓库。`--local` 读取本地 Docker daemon，必须确认实际是 amd64 镜像；它与从远端按 `--platform linux/amd64` 收集是不同输入方式。[R6]

如果制包在远端而镜像只在用户本地，先在本地用该能力导出 Hauler archive，再交给构建机合并。不要把 `docker save` 的 tar 当成 Hauler archive；不自动向远端仓库推送用户镜像。

### 5.3 必须显式列出的镜像

| 用途 | 已知来源；最终仍以实际被部署材料为准 |
|---|---|
| Kubernetes 核心 | kube-apiserver、controller-manager、scheduler、etcd、CoreDNS、pause，以及实际配置启用的 kube-proxy 等 |
| kcn | `docker.changqingyun.cn/kubercloud/kc-networking:v0.6.2`，覆盖主容器和 initContainer |
| Envoy 控制器/certgen | `docker.changqingyun.cn/kubercloud/gateway:v1.8.3` |
| Envoy 数据面 | `docker.changqingyun.cn/kubercloud/envoy:distroless-v1.38.3` |
| shutdown-manager | `docker.changqingyun.cn/kubercloud/gateway-dev:latest`，取用户现有内容并记录 digest |
| 附件配置引用 | `docker.changqingyun.cn/kubercloud/ratelimit:1e50889b`；只有实际启用功能才启动，不为它扩出 Valkey/限流验收 |
| 测试后端/客户端 | 选择现有可用的固定 amd64 镜像，确保具备实际使用的 HTTP/DNS 工具，并随包带入 |

`gateway-dev:latest` 不能替换成 `gateway:v1.8.3` 来凑齐材料；它们不是同一个组件。可以在本地服务中保持原 tag 指向包内固定内容，目标现场不能联网重新解析 latest。

从 Deployment 里搜 `image:` 只能得到部分清单。还要读取 Envoy ConfigMap、EnvoyProxy patch、certgen Job、initContainer 和验收资源。

### 5.4 镜像地址只维护一张表

`images.tsv` 记录：原始引用、Hauler 服务中的仓库路径/标签、实际摘要、使用位置。制包和清单渲染共同使用它。

- 用 Hauler 支持的重写/导出方式建立明确的本地仓库路径；实际请求 `/v2/<repository>/manifests/<tag>` 能找到对应内容。
- 不凭经验猜测 Hauler 是否保留原 registry hostname，不给所有镜像粗暴套同一个字符串替换。
- Kubernetes 镜像配置和 kubeadm 最终请求的名字要与表中一致，etcd/CoreDNS 也一样；这是材料对应关系，不是提前认定上次在线问题重现。
- kcn、Envoy 控制器、certgen、EnvoyProxy 中的动态镜像都渲染到本地地址。

## 6. 现场自举和 KubeKey 对接

### 6.1 安装入口实际做的事

1. 建立本次 `work/` 和 `logs/`，读取配置和随包版本。
2. 定位 installer 对应的管理 IP，生成 KubeKey inventory/config 以及组件渲染材料。
3. 加载包内 Hauler archive，启动宿主机 registry。首轮实验网使用 HTTP，不新建 CA/认证系统；containerd 必须显式配置为可访问该 HTTP endpoint。
4. 用简单 systemd service 管理这个 Hauler 进程，避免随终端退出丢失。保持固定服务名称、明确工作目录和日志即可，不实现 worker 调度/恢复协议。
5. 通过 KubeKey 原生流程准备系统依赖、安装 containerd，再完成 init/join、kcn 和 Envoy。
6. 运行最小验收，打印实际结果及日志位置。

Hauler `load` 使用 `--filename`；服务子命令是 `store serve registry` 和 `store serve fileserver`。具体 root/port 等参数从固定二进制 `--help` 核实，不发明不存在的 `serve files` 参数。[R6]

Hauler 成功启动时可能需要先把内容准备到其 registry 数据目录，不能只 sleep 一秒就假定可用。做一次带短超时的本地 `/v2/` 等待即可。

**安装结束保留 Hauler 服务和离线包。** 本轮没有 Harbor 交接，后续 Pod 仍可能需要拉取镜像。README 说明服务位置即可，不加入仓库 HA 和整群断电恢复工作。

### 6.2 KubeKey 配置必须落实的几项

- 安装配置明确设置 `spec.download.fetch: false`。只传 `--artifact` 不够：如果配置已经写了 true，参数不会保证关闭下载。[R2][R3]
- 使用原生 `--artifact <包路径>` 解包，尊重实际 `binary_dir`/workdir 布局；目标节点上的文件安装和复制继续走上游实现。
- `image_registry` **主机组为空**，并关闭内置仓库部署类型。不能把 installer 加入这个组，否则上游会执行 `cri/docker`。[R4]
- 配置实际 containerd 版本对应的 registry HTTP/认证字段。读取正在使用的模板，不套用 Kubespray 的变量，也不把 containerd 1.x 的配置段原样塞到 2.x。
- 不依赖仅覆盖 docker.io 的 mirror 配置去处理所有 registry；清单使用明确的本地镜像地址。仅为本次 Hauler endpoint 开启 HTTP。[R5]
- 显式设置 `spec.storage_class.local.enabled: false`、`spec.storage_class.nfs.enabled: false`，关闭默认存储安装；`spec.dns.nodelocaldns.enabled: false`，首轮只保留 CoreDNS，少带一个组件。[R7]
- 原生 `cni.type` 和 `cni.multi_cni` 都设为 `none`，以跳过原有 CNI/Multus；随后插入 `ani/kcn` role。首轮不扩展 CNI 类型枚举及版本矩阵。[R4]
- inventory 的 etcd 组写入本次三个控制节点；使用上游已有 stacked/internal etcd 配置。
- `spec.kubernetes.control_plane_endpoint.type: local` 是原生模式：上游会处理控制节点/工作节点的 endpoint 主机名解析。不要把它替换为自造的 `type: none` 或 `type: external`。[R7]
- 三台同时承载普通 Pod。先复用上游对 worker 组的调度处理；若实际还留有阻止普通工作负载的 control-plane NoSchedule taint，在本次安装 role 中处理这三个节点，不让用户手工解除，不修改其他节点。

配置渲染得到的实际调用形态：

```text
kk create cluster \
  -i <work/inventory.yaml> \
  -c <work/config.yaml> \
  --artifact <packages/kubekey-artifact.tgz> \
  --workdir <work/kubekey>
```

不要把上面的占位符直接执行，也不要只依赖 `--with-kubernetes` 猜测制包和安装版本已经一致。[R2]

## 7. kcn 角色怎么实现

修改 `builtin/core/playbooks/create_cluster.yaml`：在现有 Kubernetes init/join 结束、原生 `cni` role 之后、`storageclass` 之前加入 `ani/kcn`；Envoy 放在 kcn 网络可用之后。[R4]

用复制来的清单做小范围模板化：

| 原字段/内容 | 值来自哪里 |
|---|---|
| `OVN_DB_IPS` | 本次三个 OVN 节点的实际绑定地址 |
| `NODE_IPS` | 同一组实际节点地址，保留清单要求的编码方式 |
| `encapNetworks` | `cluster.yaml` 中对应输入 |
| `intranetNetworks` | 现场实际内网/Service 网络范围 |
| `managedDevices` | 明确选择的无主机 IP 业务网卡 |
| `--service-cluster-ip-range` | Kubernetes ServiceCIDR |
| 所有 image 字段 | 镜像表中的本地服务引用 |
| `networking.kubercloud.com/role=master` | 通过 KubeKey `kubernetes.custom_labels` 或安装 role 给本次三个控制节点加上标签；不要误写为 Kubernetes 的旧 master 标签 |

顺序：API 可访问 → 给节点添加 kcn 所需标签 → 创建 Namespace/CRD/RBAC/ConfigMap → 创建工作负载 → 等待 kcn 和网络可用。

API 可访问不等于 NodeReady。**不能在安装 kcn 之前等待所有节点 Ready/CoreDNS Ready，否则可能形成等待循环。** 若上游某一步确实阻塞在这里，只调整有证据的等待位置，不删除全部健康等待。

保留当前清单的 `isDefaultCNI="true"`、`hasMultusCNI="false"`、`ENABLE_SSL="false"`，不在此任务重设计 kcn。当前清单本身没有 VPC/Subnet 实例；如果首次实际 Pod 失败指向缺少默认网络，检查该版本已有的初始化材料并纳入 role，不自行创造网络语义。

管理口即使只有一张也不能被 `managedDevices` 接管。本轮不实现主机 IP/路由迁移到 OVS，也不把已有地址删除来通过安装。

## 8. 定制 Envoy 角色怎么实现

只复制和模板化用户材料，不换官方 quickstart，不另选新版 Envoy。

执行顺序：

1. 对附件 `install/install.yaml` 执行 server-side apply，创建 CRD/RBAC/控制器等。
2. 等 CRD 可用、certgen Job 完成、控制器能启动；只看本次相关对象。
3. 应用 `install/gateway-namespace-mode.yaml` 中所需的集群写权限，以及 `system:auth-delegator` 的绑定。
4. 在原有 Envoy Gateway 配置上合并 `provider.kubernetes.deploy.type=GatewayNamespace`、`extensionApis.enableBackend=true`、`extensionApis.enableEnvoyPatchPolicy=true`。
5. 保留并正确替换原有镜像配置；更新后重启并等待控制器。
6. 从实际示例中选取 `lb-small-noeip` 对应的 EnvoyProxy 和 GatewayClass，先创建 EnvoyProxy，再创建 GatewayClass。
7. 部署一个最小 HTTP 验收场景。

需要保留的定制点：`preserveRouteOrder`、EnvoyProxy 里的容器 patch、shutdown-manager 镜像及用户的 GatewayNamespace 模式。ConfigMap 中的 shutdownManager 默认镜像会被 EnvoyProxy patch 覆盖，两处都必须处理。

示例文字中的 GatewayClass 数量已经与 YAML 不同；首轮只提取实际关联的一套 NoEIP 规格，不按文字数量去补造对象，不创建 dynamic 占位规格。

示例中关闭了部分数据面探针。首轮保留用户当前行为，并通过真实 HTTP 判断可用；不要为了完成健康报告临时修改探针，也不要声称这已经是生产探针方案。

对 CRD/模板用结构化 YAML 修改或明确的 patch；不对数万行 install.yaml 做不可控全局替换，不用简化 ConfigMap 覆盖原有完整配置。`--force-conflicts` 如确实用于新建的本次 EnvoyProxy，限定到该对象，不能加到所有 apply 命令上。

### 唯一首轮 HTTP 场景

- 从附件 `examples/http/` 的 `http-b:9090 → HTTPRoute → Backend:3000` 关系提取场景。
- GatewayClass 使用 `lb-small-noeip`，资源放进 `ani-installer-smoke` namespace。
- 后端使用一个真实普通 Pod，实际监听 3000 并返回固定的 `ANI-INSTALLER-OK`；从集群读取它本次的 Pod IP，填入定制 Backend。
- HTTPRoute 的后端引用保留 `group: gateway.envoyproxy.io`、`kind: Backend`，不要替换为普通 Service 型 backendRefs，否则会漏掉这次需要验证的定制路径。
- 不复制示例里旧的后端 IP，不使用真实 VM、EIP 或业务后端。
- 让客户端普通 Pod 调度在与后端不同的节点；读取本次 Envoy 数据面 Service 的 ClusterIP 和实际 HTTP 端口。
- 从客户端请求这个 ClusterIP，带与 HTTPRoute 相符的 Host，必须返回预期内容。不得用 port-forward 或直接请求后端代替 Envoy 链路。
- 后端重建换 IP 时重新生成本次测试 Backend；不为此开发长期发现控制器。

这个场景证明控制器、定制 Backend、数据面和集群网络的基础集成，不扩展为全部网关规格、TLS、限流或 HA 认证。

## 9. 六步实施，每步都要有实际产物

### S1：拿到可以运行的 fork 二进制

做：建立源码副本、保留许可证、加入本任务短版规则；先用上游构建方式编译。读本任务涉及的 CLI/options、下载、CRI、create_cluster 文件即可，不通读整个仓库。

完成标志：实际二进制可运行 `version` 和相关 `--help`；记录上游 tag/commit 和构建命令。这个阶段不做目标节点扫描，不建立配置平台。

### S2：做出第一份包和安装入口，开始断网自举

做：实现小范围配置读取、制包脚本、Hauler 加载/启动，生成 KubeKey 实际输入。使用专用 VM 从第一次安装尝试起就断公网。

完成标志：目标节点上的 Hauler 可用；KubeKey 能从包准备系统依赖和 containerd；目标 CRI 能从本地服务拉到包内镜像。用一次 `crictl pull` 即可，不建仓库测试套件。

遇到缺包就补包并重试；Ubuntu 已装有的基础命令可以直接用，不为了证明“无依赖”卸载系统包。

### S3：跑通 Kubernetes 和 kcn

做：由 KubeKey 执行 init/join，随后运行 kcn role，修复实际出现的路径、镜像、等待顺序或参数问题。

完成标志：三个节点加入、kcn 正常、跨节点 Pod/Service/DNS 实际可用。只把 S3 实测中出现的上游问题作为补丁依据。

可以人工定位故障，但最终命令必须归入程序。不能以“让用户另执行 kubeadm join”作为本阶段完成。

### S4：安装定制 Envoy 并完成一次 HTTP

做：实现第 8 节 role 和最小场景，补齐动态镜像。

完成标志：普通客户端 Pod 经 Envoy 数据面 Service 和定制 Backend 到达实际后端，返回 `ANI-INSTALLER-OK`。安装器自动执行，无需手工编辑 ConfigMap。

### S5：整理为一个可搬走的包

做：把 S2—S4 已跑过的命令完整串进 `install.sh`，清除对开发目录、开发机镜像缓存、临时脚本的隐式依赖。日志保留在包目录旁。

完成标志：把包放到一个新路径、只修改现场配置，入口就能找到所有材料；用户不需要先手动启动 Hauler、配置 containerd 或导入镜像。

### S6：从干净快照重新安装一次

做：使用已经明确可重置的专用 VM 快照，保持断公网，使用同一份总包和实际配置，重新执行单一入口。

完成标志：三项最小验收通过。记录使用的包、配置、开始/结束时间与日志路径。

首次成功但第二次尚未执行时，写“首次离线安装通过，干净重装未验证”，不要写成完整交付完成。如果有真实环境输入缺失，完成能做的代码/制包工作，并逐项列出 S2—S6 中哪些真实安装环节未执行；不能只写“剩下 S6”，不能用模拟测试冒充。

## 10. 检查和日志保持简单

新增前置检查只负责：配置能读、节点可连接和提权、包/工具存在、指定网卡存在，以及业务口没有需要保留的主机地址。格式错误直接说明哪个字段，命令不足就在对应步骤补安装。

不要在 KubeKey 外再加容量规划、内核全特性检查、兼容矩阵、网络规则引擎和签名门禁。KubeKey 自带的检查先保留；若它实际误挡当前场景，针对该处修正，不发明不存在的 `--skip-precheck`，也不把全部检查清空。

最低限度的软件验证：构建成功；新增 shell 用 `bash -n`；如果增加配置转换 Go 代码，给节点地址/镜像路径映射写少量有意义的测试。不要把跑整个 KubeKey 项目的测试矩阵、race、长时间 soak 当作首次实装前置。

安装后只做：

1. 三个节点 Ready。
2. 普通 Pod 的跨节点直连、Service 访问、集群内 DNS 查询成功。
3. 普通 Pod 经 Envoy 数据面 Service 请求，得到真实后端的预期 HTTP 内容。

真实环境应从第一次安装到验收始终无法访问公网，只保留实验网内部通信。断网优先通过实验虚拟网络/出口设置实现，不在管理口上随意清空默认路由或防火墙。安装器失败后不能临时打开公网补下载再宣称离线成功。

日志只需总输出加关键服务输出，标明节点、步骤和错误；不要打印 token、密码、私钥和完整 Secret。失败返回非零并停止后续依赖步骤，保留现场，不执行自动 reset。超时要打印当前等待对象和相关日志，不无限重试或用长 sleep 遮盖问题。

## 11. 执行 AI 的明确行为约束

1. 按 S1→S6 推进，完成一个阶段后继续下一个，不要每一步都等待用户批准。
2. 只为当前实际错误修改代码；在线历史错误是线索，不是离线缺陷证据。
3. 不恢复 Kubespray/Ansible/Python 方案，不写新的 SSH/kubeadm 安装引擎。
4. 不增加 Plan/Run/Attempt、数据库、插件注册表、自动修复策略或复杂恢复流程。
5. 不自动升级 KubeKey、Kubernetes、kcn、Envoy，也不把私有镜像替换成名字相近的公共镜像。
6. 碰到工具参数/配置键不确定，读固定版本 `--help` 和对应源码，然后用实际命令确认；不得编造成功输出、镜像摘要或测试结果。
7. 需要用户输入时只问缺失的实验节点/凭据路径或确实会改变范围的选择。缺材料先把具体镜像/文件列出来，不让用户跑一大堆预检命令。
8. 一个问题连续两次用同一种猜测仍失败，就带着原始错误检查实际调用和生成配置，不再追加 RunID/环境名特判。
9. 不自动清空磁盘、迁移管理 IP、删除现有集群、push 或发布。这些都不是本次快速首装的必要步骤。
10. 每完成一个阶段，在 `docs/progress.md` 写简短真实记录，然后继续。不要把精力花在扩写方案本身。

## 12. 最终交付说明只需要回答这些问题

- fork 在哪里、基于哪个 tag/commit、编译命令是什么？
- 离线包在哪里、目标节点执行哪一条命令、用户只需要填写哪些配置？
- 哪一组三台 VM 完成了首次安装和干净重装？
- 节点/网络/Envoy 三项实际结果是什么，日志在哪里？
- 还有什么没有验证？Hauler 为什么仍保留、怎样查看其状态？

不要求先做版本发布、全依赖许可证审计或产品宣传材料；保留已有许可证/版权声明和修改记录，商业交付前再单独处理完整依赖许可核查。

## 附：实施依据

下面是固定版本源码定位，不是让执行 AI 扩展研究范围。先使用这些入口，只在实际错误需要时继续追踪。

- [R1 — KubeKey v4.0.7 Makefile，kk 二进制构建](https://github.com/kubesphere/kubekey/blob/v4.0.7/Makefile)
- [R2 — KubeKey 公共 CLI 参数和 artifact/fetch 处理](https://github.com/kubesphere/kubekey/blob/v4.0.7/cmd/kk/app/options/option.go)
- [R2a — create 命令的版本处理](https://github.com/kubesphere/kubekey/blob/v4.0.7/cmd/kk/app/options/builtin/create.go)
- [R2b — artifact 命令选项](https://github.com/kubesphere/kubekey/blob/v4.0.7/cmd/kk/app/options/builtin/artifact.go)
- [R3 — 原生 artifact 解包及下载条件](https://github.com/kubesphere/kubekey/blob/v4.0.7/builtin/core/roles/download/tasks/main.yaml)
- [R3a — 官方离线制包与安装说明](https://github.com/kubesphere/kubekey/blob/v4.0.7/docs/en/installation/offline.md)
- [R4 — create_cluster 的实际角色顺序及 Docker 仓库路径](https://github.com/kubesphere/kubekey/blob/v4.0.7/builtin/core/playbooks/create_cluster.yaml)
- [R4a — CNI 角色分派](https://github.com/kubesphere/kubekey/blob/v4.0.7/builtin/core/roles/cni/meta/main.yaml)
- [R4b — 原生网络类型允许列表](https://github.com/kubesphere/kubekey/blob/v4.0.7/builtin/core/roles/defaults/defaults/main/01-cluster_require.yaml)
- [R5 — containerd registry 模板入口](https://github.com/kubesphere/kubekey/blob/v4.0.7/builtin/core/roles/cri/containerd/templates/config.toml)
- [R6 — Hauler v2.0.3 CLI，包含本地 Docker 镜像输入和服务子命令](https://github.com/hauler-dev/hauler/blob/v2.0.3/cmd/hauler/cli/store.go)
- [R6a — Hauler 本地镜像与 rewrite 实现](https://github.com/hauler-dev/hauler/blob/v2.0.3/cmd/hauler/cli/store/add.go)
- [R6b — Hauler registry/fileserver 实现](https://github.com/hauler-dev/hauler/blob/v2.0.3/cmd/hauler/cli/store/serve.go)
- [R7 — Kubernetes 1.35 配置中的原生 local endpoint、存储和 DNS 开关](https://github.com/kubesphere/kubekey/blob/v4.0.7/builtin/core/defaults/config/v1.35.yaml)
- [本地 kcn 清单](/home/chabking/workspace/ani-network-service/docs/runbooks/platform-manual-assets/kcn-install.yaml)
- [本地 Envoy 初始化说明](/home/chabking/下载/loadbalancer-doc-dev/loadbalancer/install/setup.md)
- [本地 EnvoyProxy 实际配置](/home/chabking/下载/loadbalancer-doc-dev/loadbalancer/examples/envoy-proxy.yaml)
- [本地 GatewayClass 实际配置](/home/chabking/下载/loadbalancer-doc-dev/loadbalancer/examples/gatewayclass.yaml)
- [本地 HTTP 场景](/home/chabking/下载/loadbalancer-doc-dev/loadbalancer/examples/http/gateway.yaml)
- [历史手动部署记录；不等于离线全流程成功](/home/chabking/workspace/ani-network-service/docs/runbooks/platform-manual-install.md)

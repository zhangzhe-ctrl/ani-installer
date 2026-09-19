# 第二批日志后端材料研究（2026-09-19）

本文是执行方案的材料依据，不是已安装通过的兼容矩阵。范围为 `none / loki / opensearch` 三选一，Fluent Bit 统一采集 Kubernetes 容器 stdout/stderr。不开启 Grafana、OpenSearch Dashboards、Jaeger，也不增加业务日志 SDK、数据迁移、双写或在线切换。

本次只在 `ssh fedora` 下载官方 Chart、检查镜像 manifest 和渲染 YAML；未连接、安装或重置 `.20～.22` 测试集群。现有测试环境是 Kubernetes 1.35.8、Ubuntu 24、三台 8GiB VM、Rook 1.20.7 / Ceph 20.2.4、`ani-block`。宿主机总内存不是组件可用内存，后续必须计入已有 Ceph 和基础组件的 requests。

## 1. 固定候选与材料证据

| 组件 | Chart | app / 主镜像 | 本次状态 |
|---|---|---|---|
| Loki | `18.13.3`，Grafana 文档指向的 grafana-community 维护仓库 | `docker.io/grafana/loki:3.7.8` | Chart 下载及静态渲染通过，linux/amd64 manifest 存在；installation_not_verified |
| OpenSearch | `3.8.0`，opensearch-project 官方仓库 | `docker.io/opensearchproject/opensearch:3.8.0` | Chart 下载及静态渲染通过，linux/amd64 manifest 存在；安全初始化与安装未验证 |
| Fluent Bit | `0.58.2`，fluent 官方仓库 | `cr.fluentbit.io/fluent/fluent-bit:5.1.2` | Chart 下载及 Loki 输出候选静态渲染通过，linux/amd64 manifest 存在；真实管道未验证 |
| OpenSearch PVC 初始化镜像 | 不另装 Chart | `docker.io/library/busybox:1.37.0` | linux/amd64 manifest 存在；替换 Chart 隐式 `busybox:latest` |

固定包及 SHA-256：

| 官方下载 | SHA-256 |
|---|---|
| [loki-18.13.3.tgz](https://github.com/grafana-community/helm-charts/releases/download/loki-18.13.3/loki-18.13.3.tgz) | `ddb31751a90269980332eb17ddbd67f2971fc1b0e847d6d1752a749c8e86232a` |
| [opensearch-3.8.0.tgz](https://github.com/opensearch-project/helm-charts/releases/download/opensearch-3.8.0/opensearch-3.8.0.tgz) | `cad6c77d04ec2389be6f61b5422ce61e7264eb7145e42b4c78dbc00e9d7dbe4d` |
| [fluent-bit-0.58.2.tgz](https://github.com/fluent/helm-charts/releases/download/fluent-bit-0.58.2/fluent-bit-0.58.2.tgz) | `2404614ace4c7dc049b76fd39d13b695f7b66332c38095327ebaa8fc91f0b046` |

镜像平台 manifest（不是多架构 index 摘要；制作离线包时两种摘要分别记录）：

| 镜像 | linux/amd64 manifest digest |
|---|---|
| Loki 3.7.8 | `sha256:81a6802ec4bd1b88c564494f06376889ed022998a188826190d26d2754ac2aae` |
| OpenSearch 3.8.0 | `sha256:68a688de28fb9bb66601552650b91a52a9fd5e7eac5481dd2b225ecb66fd09b0` |
| Fluent Bit 5.1.2 | `sha256:71cda445290efc2d45d565c12e0de4b15aa0182510276ad50c317aae1423d7ee` |
| BusyBox 1.37.0 | `sha256:7a3ebe5bfd1a4a19797d20b0c0bb39d44393e9a03fd852c0865b0f540d868df0` |

来源为上述发布包以及 [Loki 仓库索引](https://grafana-community.github.io/helm-charts/index.yaml)、[OpenSearch 仓库索引](https://opensearch-project.github.io/helm-charts/index.yaml)、[Fluent 仓库索引](https://fluent.github.io/helm-charts/index.yaml)。本次使用现有 Helm 3.20.0，`--kube-version 1.35.8`；渲染成功不能替代 Kubernetes API 验证、镜像导入、权限或功能实测。

Fedora 证据目录：`/home/chabking/ani-installer-runs/obs-logs-materials-20260919/`，包含原始 tgz、展开后的 Chart、三个候选 values 和 rendered YAML。Helm 来自 `/home/chabking/ani-installer-runs/foundation-20260918/releases/ani-artifact-ubuntu24-amd64-20260918-b4/bin/helm`。

## 2. Loki：单体、filesystem、RBD PVC

采用 TSDB v13 + filesystem + 单实例 StatefulSet 即可，不需要为本批引入 RGW、MinIO 或 RustFS。Loki 官方把 filesystem 定位为低流量、验证性部署；底层 Ceph 提供持久卷并不会把 Loki 变成 HA，也不构成生产容量承诺。filesystem 不按剩余磁盘空间自动删日志，容量不足应告警，不能在 installer 中写删除文件逻辑。[官方 filesystem 说明](https://grafana.com/docs/loki/latest/operations/storage/filesystem/)

Chart 已于 2026-03-16 迁移到 grafana-community；不要把旧 `grafana/loki` Chart 当作持续维护的 OSS 入口。[官方迁移说明](https://github.com/grafana/loki/issues/20705)

固定 Chart 的关键字段如下，已按这些字段成功渲染。`deploymentMode` 的值是 `Monolithic`；`singleBinary` 仍是 values 的字段名。若只设置单体副本数而不清零 `read/write/backend`，此 Chart 会直接拒绝渲染。

```yaml
deploymentMode: Monolithic
loki:
  auth_enabled: false
  commonConfig:
    replication_factor: 1
  storage:
    type: filesystem
  schemaConfig:
    configs:
      - from: "2024-04-01"
        store: tsdb
        object_store: filesystem
        schema: v13
        index:
          prefix: index_
          period: 24h
  limits_config:
    retention_period: 72h
  compactor:
    working_directory: /var/loki/compactor
    retention_enabled: true
    delete_request_store: filesystem
  ingester:
    wal:
      enabled: true
      dir: /var/loki/wal
      flush_on_shutdown: true
  storage_config:
    tsdb_shipper:
      active_index_directory: /var/loki/tsdb-index
      cache_location: /var/loki/tsdb-cache
  analytics:
    reporting_enabled: false
singleBinary:
  replicas: 1
  sidecar: false
  persistence:
    enabled: true
    storageClass: ani-block
    size: 5Gi
    enableStatefulSetAutoDeletePVC: false
  resources:
    requests: {cpu: 100m, memory: 256Mi}
    limits: {memory: 1Gi}
read: {replicas: 0}
write: {replicas: 0}
backend: {replicas: 0}
chunksCache: {enabled: false}
resultsCache: {enabled: false}
gateway: {enabled: false}
lokiCanary: {enabled: false}
test: {enabled: false}
sidecar:
  rules: {enabled: false}
minio: {enabled: false}
ruler: {enabled: false}
```

渲染结果只有一个 Loki 容器，PVC 挂载 `/var/loki`，Service 为 `ani-loki.ani-observability.svc.cluster.local:3100`。默认 chunks/results cache 分别声明约 8GiB / 1GiB 缓存，必须显式关闭；禁用 Chart 自带测试也避免其 `grafana/loki-helm-test:latest` 成为隐藏离线依赖。

默认 `retentionDays: 3` 映射为 `72h`，允许执行方案约定的有限配置范围。保留由 Loki Compactor 实现，包含持久化 marker 目录和 `delete_request_store`；只写 `limits_config.retention_period` 不算完成。[官方保留机制](https://grafana.com/docs/loki/latest/operations/storage/retention/)

`auth_enabled: false` 是本轮内部单租户服务选择，并不提供用户认证。仅 ClusterIP，不暴露 NodePort / Ingress / Gateway；不承诺多租户隔离。不能为“看日志”顺带发布公网入口。本轮验收是 API 查询，不依赖 Grafana。

## 3. OpenSearch：单节点、安全插件和首次初始化

固定 Chart 的 values 已渲染；以下是实施约束而非已通过的运行结果。

| 字段 | 本轮候选 |
|---|---|
| `clusterName` / `nodeGroup` / `masterService` | `ani-opensearch` / `master` / `ani-opensearch-master`；`masterService` 必须显式设置，否则 Service 仍可能叫默认的 `opensearch-cluster-master` |
| `singleNode` / `replicas` | `true` / `1` |
| `image.repository` / `image.tag` | 上表固定镜像；安装时按既有镜像映射改为离线仓库 |
| `opensearchJavaOpts` | `-Xms1g -Xmx1g` |
| `resources.requests` / `resources.limits` | requests `cpu:500m,memory:2Gi`，limit `memory:2Gi`；这是小规模实测起点，不是官方保证最低配置 |
| `persistence.enabled/storageClass/size` | `true` / `ani-block` / `5Gi` |
| `persistence.image/imageTag` | `docker.io/library/busybox` / `1.37.0`，不能留下 `latest` |
| `sysctl.enabled` / `sysctlInit.enabled` | `false` / `false`；宿主参数由 installer 正常节点配置阶段设置 |
| `extraEnvs` | `DISABLE_INSTALL_DEMO_CONFIG=true`，不设置 `DISABLE_SECURITY_PLUGIN=true` |
| `protocol` | `https` |
| `secretMounts` | 挂载 cert-manager 签发的节点 TLS Secret 到 `/usr/share/opensearch/config/ani-tls` |
| `securityConfig.enabled` | `true`；这是 Chart 配置文件开关，不等于已完成安全索引初始化 |

Chart 产生 StatefulSet `ani-opensearch-master`、Service `ani-opensearch-master` 与 headless Service；初始化镜像为同版本 OpenSearch 和固定 BusyBox。上游默认 PVC ownership 初始化可以按其字段使用；不额外添加清盘、`chmod 777` 或强制修权限循环。[固定 Chart](https://github.com/opensearch-project/helm-charts/releases/tag/opensearch-3.8.0)

OpenSearch 默认示例使用 demo 安装器。自定义证书时应设置 `DISABLE_INSTALL_DEMO_CONFIG=true`；此路径不依赖 `OPENSEARCH_INITIAL_ADMIN_PASSWORD` 初始化 demo admin。不要为省事禁用安全插件、使用 demo 证书或 `admin/admin`。[官方 Chart 的默认安装与自定义证书说明](https://github.com/opensearch-project/helm-charts)

### 3.1 正常部署步骤

1. OpenSearch 选项要求本轮已有 cert-manager CA 能力；在配置中明确该依赖，不能隐式落到不安全模式。使用已有 CA 签发两个不同 Secret：节点证书和 admin client 证书。节点证书 CN `ani-opensearch-node`，兼具 serverAuth / clientAuth；admin CN `ani-opensearch-admin`，只供初始化 Job，不能交给 Fluent Bit。私钥使用 PKCS#8。节点证书 SAN 覆盖实际 Service FQDN、短 Service 名、headless Service、实际 Pod FQDN；集群域名使用实际配置，不能假定永远是 cluster.local。
2. `config.opensearch.yml` 设置 `plugins.security.ssl.transport.{pemcert_filepath,pemkey_filepath,pemtrustedcas_filepath}` 指向 `ani-tls/tls.crt`、`ani-tls/tls.key`、`ani-tls/ca.crt`；HTTP 层设置相同 PEM 路径并 `plugins.security.ssl.http.enabled: true`。`plugins.security.allow_unsafe_democertificates: false`，`plugins.security.allow_default_init_securityindex: false`；`plugins.security.authcz.admin_dn` 和 `plugins.security.nodes_dn` 分别匹配两个证书的实际 DN。保留主机名校验。[TLS 路径和证书要求](https://docs.opensearch.org/latest/security/configuration/tls/)
3. 应用 Chart，等待进程监听；此时安全索引未初始化是预期的中间状态。Chart 默认就绪检查仅 TCP 9200，不能把 Helm Ready 当作最终验收。随后运行一个明确的一次性初始化 Job，镜像复用 `opensearchproject/opensearch:3.8.0`，使用其 `plugins/opensearch-security/tools/securityadmin.sh`，通过 HTTPS 9200 和 admin client 证书提交受版本管理的安全配置。连接目标必须在 SAN 内，不能加 `-nhnv` / 关闭 TLS 校验。运行时无参数调用实际脚本打印选项（`-h` 表示主机，不是帮助），核对下列参数。Job 失败保留输出并停止，不反复删除安全索引重建。[官方 securityadmin 工具](https://docs.opensearch.org/latest/security/configuration/security-admin/)
4. installer 生成独立随机凭据写入 Secret，至少划分 `ani_log_writer` 与只读查询用户。安全配置文件保留该版本要求的 `_meta` / `config_version` 等结构，关闭匿名认证；internal users 文件不能保留 demo 用户。优先复用项目现有 Go bcrypt 能力生成密码 hash；若用 OpenSearch `hash.sh`，密码通过受保护输入读取且禁止 `set -x`、打印或写入交付文档。初始化 Job 只装配这份产品配置，不执行泛化修复逻辑。[官方内部用户与角色](https://docs.opensearch.org/latest/security/access-control/users-roles/)
5. writer 仅授予 `ani-logs-*` 的 bulk 写入/必要建索引权限，query 用户只读相同前缀；模板和 ISM 由初始化身份创建。权限是否够用必须用实际 Fluent Bit bulk 请求验证，不因 403 一次性授予所有索引管理权限。用户密码明文只存在 Secret 或受保护运行文件；交付说明只给 Secret 引用和读取命令。
6. 初始化成功后，未认证请求返回 401；受信 CA 的查询身份可查询日志；Fluent Bit 身份不能改安全配置。不要把端口通、Pod Ready 或 `/_cluster/health` 单项成功代替这些检查。

上述安全初始化路径尚未运行，必须安排为独立实施步骤。若确切配置/权限遇到问题，先保留错误并修正配置；不得切换到 demo、关闭 TLS 校验或禁插件来标记通过。

初始化 Job 内的命令形状如下，路径由 Job 的 Secret/ConfigMap 挂载确定；此命令只供首次新集群初始化，不进入通用 `verify.sh`，也不能在每次验证时重放覆盖已有用户。

```bash
/usr/share/opensearch/plugins/opensearch-security/tools/securityadmin.sh \
  -h ani-opensearch-master.ani-observability.svc.cluster.local \
  -p 9200 -cn ani-opensearch \
  -cd /security-config \
  -cacert /admin-tls/ca.crt \
  -cert /admin-tls/tls.crt \
  -key /admin-tls/tls.key
```

本次还下载核对了 [security 3.8.0.0 的固定 config 目录](https://github.com/opensearch-project/security/tree/3.8.0.0/config)。模板必须保留该版本的八个文件结构：`config.yml`、`internal_users.yml`、`roles.yml`、`roles_mapping.yml`、`action_groups.yml`、`tenants.yml`、`nodes_dn.yml`、`allowlist.yml`；各自 `_meta.config_version: 2`，`_meta.type` 分别为 `config/internalusers/roles/rolesmapping/actiongroups/tenants/nodesdn/allowlist`。这是独立配置的上游结构参考，不能原样启用其中 demo 用户。Basic 认证块中的 `authentication_backend.type` 实际值是 **`intern`**，不是凭直觉填写的 `internal`；`http_authenticator.type: basic` 且 `challenge: true`，只启用本轮使用的内部认证域。实际安全文件、角色权限和初始化命令仍需在安装批次验证。

### 3.2 宿主机参数与资源

只有选择 OpenSearch 时，installer 在所有允许调度 OpenSearch 的目标节点通过既有 SSH / KubeKey role 设置 `vm.max_map_count >= 262144`：写专用 `/etc/sysctl.d/90-ani-opensearch.conf`，应用该文件，记录前后实际值；已有更高值不降低。这是声明过的组件系统前提，属于 installer 正常职责，无须要求用户逐台手工执行，也不需要特权 sysctl Pod。[官方 Linux 参数](https://docs.opensearch.org/latest/install-and-configure/install-opensearch/docker/)

官方建议堆内存约可用内存的一半；1Gi heap / 2Gi 容器只是本测试集群的小流量候选。官方 Helm 文档给的整个默认三节点部署资源提示不能当成单实例 2Gi 的运行保证。若出现 OOM 或可调度 requests 不足，应记录容量限制，不降 requests 假装有空闲、不自动裁剪已启用组件、不扩充到生产 HA。[官方堆设置](https://docs.opensearch.org/latest/install-and-configure/install-opensearch/index/)、[官方 Helm 安装资源提示](https://docs.opensearch.org/latest/install-and-configure/install-opensearch/helm/)

### 3.3 索引与保留

首次启动 Fluent Bit 前创建 `ani-logs-*` 索引模板：单 primary shard、`number_of_replicas: 0`；这与本轮单节点选择一致。通过 `/_plugins/_ism/policies/ani-logs-retention` 创建 ISM policy，默认 hot 状态，`min_index_age: 3d` 后转 delete，`ism_template.index_patterns: ["ani-logs-*"]`，新日志索引自动套用。按天索引使用固定前缀 `ani-logs`；不引入 rollover alias 管理器或 installer 定时清索引脚本。[官方 ISM policy](https://docs.opensearch.org/latest/im-plugin/ism/policies/)

验收读取实际模板、索引设置、ISM explain，确认写入索引关联了预期策略。3 天计时删除若未跑满就标为 `not_verified`；不能靠 installer 手动删索引冒充。仅日志索引零副本，不要全局把所有系统索引设零副本来掩盖集群 health yellow。

## 4. Fluent Bit：共同采集层

官方固定 Chart `0.58.2` 使用 `kind: DaemonSet`、`image.repository/tag`、`config.service/inputs/filters/outputs`、`daemonSetVolumes/daemonSetVolumeMounts`、`env`、`extraVolumes/extraVolumeMounts`、`tolerations`。实际默认输出是 Elasticsearch，必须整体替换，不要在末尾追加第二个输出造成意外双写。[固定 Chart](https://github.com/fluent/helm-charts/releases/tag/fluent-bit-0.58.2)

共同要求：

- 只采集 `/var/log/containers/*.log`，使用 `tail` 与 `multiline.parser cri`；本轮不采集 systemd journal，覆盖默认 systemd input。配置 Kubernetes filter 关联 namespace/pod/container，禁止把所有 Pod label 自动提升为 Loki label。
- containerd 场景只读挂载宿主 `/var/log`，不要保留默认 Docker `/var/lib/docker/containers` 依赖；tail DB 和 filesystem buffer 放在专用 hostPath `/var/lib/ani-installer/fluent-bit`，容器内挂载 `/var/lib/fluent-bit`。installer 不在失败重试中清除此目录。
- `config.service` 设置 `storage.path /var/lib/fluent-bit/buffers`、`storage.backlog.mem_limit 10M`；tail `DB /var/lib/fluent-bit/tail.db` 与 `storage.type filesystem`；每个唯一输出 `storage.total_limit_size 100M`。这是有界本地缓冲，不承诺零丢失或 exactly-once；磁盘/缓冲满的行为应记录。[官方缓冲](https://docs.fluentbit.io/manual/administration/buffering-and-storage)
- 在控制节点具有 NoSchedule taint 时添加对应窄 toleration，确保测试三节点都覆盖；不用任意 `operator: Exists` 来容忍一切污点。
- requests `50m / 64Mi`、memory limit `128Mi` 可作小流量候选，OOM 后基于日志调整真实需求，不仅删 limit。
- 禁用 `testFramework.enabled`，本轮验收用项目明确打包的 smoke Pod / API 客户端；额外测试镜像必须进入镜像闭包，不在线临时拉取。

Loki 输出字段：`Name loki`、`Host ani-loki.ani-observability.svc.cluster.local`、`Port 3100`、`Match kube.*`、`Line_Format json`。标签限制到 job / namespace / pod / container，使用 record accessor 读取 Kubernetes 元信息；验收一个唯一 marker 经普通 Pod stdout、节点日志文件、Fluent Bit、Loki 到 `/loki/api/v1/query_range` 的完整路径。不能直接 POST Loki 再查询来代替采集验证。[官方 Loki 输出](https://docs.fluentbit.io/manual/data-pipeline/outputs/loki)

OpenSearch 输出字段：`Name opensearch`、`Host ani-opensearch-master.ani-observability.svc.cluster.local`、`Port 9200`、`HTTP_User ${OS_USER}`、`HTTP_Passwd ${OS_PASSWORD}`、`tls On`、`tls.verify On`、`tls.ca_file /fluent-bit/tls/ca.crt`、`Suppress_Type_Name On`、`Logstash_Format On`、`Logstash_Prefix ani-logs`、`Generate_ID On`。凭据通过 Chart `env[].valueFrom.secretKeyRef`，CA 通过只读 Secret volume；不写在 ConfigMap。开启日志排错也不能 `Trace_Output` 打印敏感请求。[官方 OpenSearch 输出](https://docs.fluentbit.io/manual/data-pipeline/outputs/opensearch)

`none` 不部署 Fluent Bit 或任一日志后端；`loki` 仅部署 Loki+Fluent Bit；`opensearch` 仅部署 OpenSearch+Fluent Bit。两个后端用不同干净快照分别安装验证，不能在同一现场卸载、删 PVC 再换后端后声称完成首次离线安装。

## 5. 实施验收的最低证据

1. 离线渲染资源不存在意外组件、外网镜像或 mutable tag；所有初始化/测试/sidecar 镜像计入材料清单。Loki 候选运行镜像闭包是 Loki；OpenSearch 是 OpenSearch+BusyBox；两条管道均加 Fluent Bit，smoke 客户端按最终方案另计。
2. 三节点普通 Pod 各输出唯一 marker，分别在所选后端查询到，保留 namespace/pod/container 身份；Fluent Bit 无持续输出错误，记录每个节点对应采集 Pod。
3. 有状态后端正常删除一次自身 Pod 后重建，原 PVC UID 不变，重建前 marker 仍可查询；这是实验验证，不写成 installer 自愈动作。若再现旧 kcn 迟到 DEL 故障，归属组件并保留证据，不添加重建网卡/重启网络/反复删 Pod 逻辑。
4. OpenSearch 验证可信 TLS、拒绝匿名、writer 写入、reader 查询和权限边界；Loki 验证本期仅内部无认证服务并记录限制。
5. 读取实际 Loki 保留配置或 OpenSearch ISM 关联；实际过期删除未观察则单列 not_verified。
6. 只有本测试集群拥有者和当前执行锁确认可用时才操作。失败已改目标节点且原因已修正，按执行主计划自动还原 `.20～.22` 后再装；快照工具与现场采证属于 lab，不进入发行包。

未验证事项：所有真实部署、离线导入、OpenSearch 安全配置文件完整性/`securityadmin.sh` 实际参数与权限、两个日志后端的完整 Fluent Bit 数据路径、三节点容量、保留期限自然到期、节点重启或整群断电恢复、性能容量与生产 HA。

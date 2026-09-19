# 第二批组件状态：指标、告警、日志（2026-09-19）

本文件记录第二批（指标栈 / Fluent Bit + Loki 或 OpenSearch）每张任务卡的代码与真实验证状态。
每卡分两列：**code_status**（Fedora 上代码、材料、渲染、测试）与 **live_status**（三台目标机的真实安装与功能验证）。

**最新结论：第二批未完成，真实安装两次失败，已按用户要求暂停。** 用户本次明确授权用现有材料实测并允许必要时重启 kcn-controller，覆盖此前等待新版材料的限制。累计 Loki 配置在 metrics 模板路径处失败；关闭 metrics 后的 Loki 首装在 `/config` 响应解析处失败。两者均为 installer 缺陷，未人工重启 kcn-controller。

当前交接入口：[真实安装失败与后续执行交接](observability-live-handoff-20260919.md)。
本文件下方 C0～C5 日志保留为历史记录；其中“code 全部完成”和“未启动三台安装”不再代表当前状态。
OpenSearch 尚未实装，Fluent Bit 在两次流程中均未执行。已有 Go 测试/渲染通过不能证明角色任务能正确执行。

## 状态总览

| 卡 | 内容 | code_status | live_status |
| --- | --- | --- | --- |
| C0 | 固定候选、材料锁、容量、示例配置、状态基线 | pass | not_verified |
| C1 | typed 配置、八行选择文件、最小公共接线 | pass | not_verified |
| C2 | 指标栈（Prometheus/Alertmanager/Operator/KSM/node-exporter） | fail：任务模板上下文错误 | fail：创建命名空间失败 |
| C3 | Loki + Fluent Bit | fail：将 YAML 配置响应按 JSON 解析 | fail：Loki 验证失败；Fluent Bit 未执行 |
| C4 | OpenSearch + Fluent Bit | fail：静态复核确认编排缺陷，待修 | not_verified：用户要求本轮失败后暂停 |
| C5 | 交付、状态、人工复现文档 | pending：本次已补制包，文档与最终交付待收口 | not_verified：两组合完整验收未通过 |

## 上游依赖状态

| 项目 | 状态 | 说明 |
| --- | --- | --- |
| 第一批 B1～B4（cert-manager/PG/Valkey/NATS） | blocked（历史） | B4 的 NATS 因旧 kcn 迟到 DEL 未通过；详见 B5 文档 |
| 旧 kcn | `v0.6.2`，已知缺陷 | 处理重复/迟到 DEL 时不按 sandbox 归属校验，误删同名 Pod 的新 NIC。**本批不使用、不修改、不回补** |
| 新版 kcn | `not_provided / user_reported_fixed` | 用户告知已修复，但**未提供镜像 tag/摘要**。禁止补造地址，禁止假定不同镜像可复用未经确认的安装清单 |
| 本批安装前置 | 未满足 | 新 kcn 材料缺席 → 按方案第 6 节不启动实验 |

## 每卡统一记录字段模板

后续每张卡与每次真实 attempt 都填写以下字段（缺项写 `not_verified`，不写推测值）：

```text
card / attempt:
code_status: pending | pass | fail
live_status: not_verified | pass | fail | blocked
source hash / kk hash / artifact manifest hash / config hash:
KCN version / digest / validated status:
enabled components / backend / chart and app versions:
snapshot 3/3 / offline before-after / install exit:
metrics scrape-query / firing-resolved / logs three-node ingestion:
normal Pod rebuild persistence / PVC and Secret UIDs:
retention configuration / actual expiry (separate):
known issue / owner / exact next step:
```

---

## C0 / attempt: c0-feda-20260919

```text
card / attempt: C0 / c0-feda-20260919
code_status: pass
live_status: not_verified
source hash / kk hash / artifact manifest hash / config hash: 见下 C0 材料证据；本轮不产代码包/artifact
KCN version / digest / validated status: v0.6.2 旧版（已知缺陷）/ not_provided 新版 / not_verified
enabled components / backend / chart and app versions:
  metrics: kube-prometheus-stack 85.4.0（Operator 0.90.1, Prometheus 3.11.3-distroless,
           Alertmanager 0.32.1, KSM 2.19.0, node-exporter 1.11.1-distroless, certgen 1.8.3）
  loki: chart 18.13.3 / app 3.7.8
  opensearch: chart 3.8.0 / app 3.8.0
  fluent-bit: chart 0.58.2 / app 5.1.2
snapshot 3/3 / offline before-after / install exit: 未执行（不启动安装）
metrics scrape-query / firing-resolved / logs three-node ingestion: not_verified
normal Pod rebuild persistence / PVC and Secret UIDs: not_verified
retention configuration / actual expiry (separate):
  配置已固定（Prometheus 24h/4GB；Loki 72h + compactor/filesystem；OpenSearch ISM min_index_age 3d）
  实际过期删除: not_verified
known issue / owner / exact next step:
  owner=用户/组件方；等用户提供已修复 kcn 的固定镜像 tag+摘要并安排验证后，
  按 C2/C3/C4 分别从干净快照执行真实安装；不得为等输入伪造材料或用旧版试装
```

### C0 材料证据（Fedora，只读核对，未接触测试集群）

工作目录：`fedora:/home/chabking/ani-installer-runs/observability-20260919/`
（`src/ inputs/ releases/ evidence/ lab/`，本批专属，未复用其它任务目录）

Helm：`foundation-20260918/releases/ani-artifact-ubuntu24-amd64-20260918-b4/bin/helm` = `v3.20.0+gb2e4314`

**Chart 下载与 SHA256**（`evidence/chart-sha256.txt`，本次实际重新下载，非转述）：

| Chart | SHA256 | 与研究附录一致 |
| --- | --- | --- |
| kube-prometheus-stack-85.4.0.tgz | `3b07b7c91f1eaec75a125d1938c4e10d3b1ef6076ff7226d031fadadb4651a60` | 是 |
| loki-18.13.3.tgz | `ddb31751a90269980332eb17ddbd67f2971fc1b0e847d6d1752a749c8e86232a` | 是 |
| opensearch-3.8.0.tgz | `cad6c77d04ec2389be6f61b5422ce61e7264eb7145e42b4c78dbc00e9d7dbe4d` | 是 |
| fluent-bit-0.58.2.tgz | `2404614ace4c7dc049b76fd39d13b695f7b66332c38095327ebaa8fc91f0b046` | 是 |

**Chart 元数据（`helm show chart`）**：kube-prometheus-stack `85.4.0` / appVersion `v0.90.1`；
loki `18.13.3` / app `3.7.8`；opensearch `3.8.0` / app `3.8.0`；fluent-bit `0.58.2` / app `5.1.2`。

**子 Chart 已随包**：kube-prometheus-stack 的 `charts/` 内含 `crds`、`grafana`、
`kube-state-metrics`、`prometheus-node-exporter`、`prometheus-windows-exporter` 目录，
`Chart.lock` 存在 → 目标机**不执行** dependency update / repo update。

**镜像 index 与 linux/amd64 manifest 摘要**（`evidence/image-checks/image-digests.tsv`，
本次用 skopeo 实测；两种摘要分别记录，未要求相等）：

| 镜像 | linux/amd64 manifest digest |
| --- | --- |
| quay.io/prometheus-operator/prometheus-operator:v0.90.1 | `sha256:c8aed26b2a0858b4beed8d6bc2215f7f6bdc6c96dd4de8e6e246c0a5b7a7876b` |
| quay.io/prometheus-operator/prometheus-config-reloader:v0.90.1 | `sha256:af7715ef28e2cc413a6a850b5f928f185245dbb38359156eca3d5f1eb676b93f` |
| quay.io/prometheus/prometheus:v3.11.3-distroless | `sha256:a1868e471c843677013c5fa2f569011e70d49b9e8f690719ea750b458840ec6d` |
| quay.io/prometheus/alertmanager:v0.32.1 | `sha256:82c38dcc97cd0fbf5d5e31ddfb304dbb3a6e411194477de5de82ec71b328bb40` |
| registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.19.0 | `sha256:ad11a45eb9a69fa88a62df4de0959eda21531fd3263c25341d9d9d4da7730930` |
| quay.io/prometheus/node-exporter:v1.11.1-distroless | `sha256:19bafa75a9de0c9562adf4af595c99c5fe316bb94a297e611495f2dd2ed44a9c` |
| ghcr.io/jkroepke/kube-webhook-certgen:1.8.3 | `sha256:44063cb072c85da7a0f33244b3054ab44b29a2b7416a319a90456bff21bebb18` |
| docker.io/grafana/loki:3.7.8 | `sha256:81a6802ec4bd1b88c564494f06376889ed022998a188826190d26d2754ac2aae` |
| docker.io/opensearchproject/opensearch:3.8.0 | `sha256:68a688de28fb9bb66601552650b91a52a9fd5e7eac5481dd2b225ecb66fd09b0` |
| docker.io/library/busybox:1.37.0 | `sha256:7a3ebe5bfd1a4a19797d20b0c0bb39d44393e9a03fd852c0865b0f540d868df0` |
| cr.fluentbit.io/fluent/fluent-bit:5.1.2 | `sha256:71cda445290efc2d45d565c12e0de4b15aa0182510276ad50c317aae1423d7ee` |
| docker.io/library/python:3.13.11-alpine3.23 | `sha256:0d8324e4e3453ec8586b886b95e8fc901689f936c413fdf6ae7d93a32a95956e` |

全部 12 个镜像 amd64 manifest 通过 skopeo 按摘要解析成功（`reach=ok`，`failed=0`）。
Python 镜像同时承担实验 HTTP webhook 接收器、查询客户端与日志发射器，不执行 pip/apk。

**四份 values 静态渲染**（`helm template --kube-version 1.35.8`，未连接 API Server）：
metrics 1868 行 / loki 438 行 / opensearch 318 行 / fluent-bit 228 行，四份退出 0。
审计结果：**无空 `image:`、无 `<no value>`、无公网 registry 残留**（quay.io / ghcr.io /
registry.k8s.io / cr.fluentbit.io / docker.io 零命中），全部镜像指向 `${REG}`。

**本次核对发现并修正的真实缺陷**：node-exporter 子 Chart 的 `_helpers.tpl` 在
`image.distroless=true` 时**追加** `-distroless` 后缀（`printf "...:%s%s" tag (ternary "-distroless" "" ...)`）。
若 `tag` 直接写 `v1.11.1-distroless` 会渲染成 `v1.11.1-distroless-distroless`、与已验证摘要不符。
正确写法是 `tag: v1.11.1` + `distroless: true`（父 Chart 默认已开）。修正后渲染为
`prometheus/node-exporter:v1.11.1-distroless`，与上表摘要一致。

**本次核对发现并修正的两个真实缺陷**（都是「静态渲染通过但镜像引用实际错误」类，方案第 5 节明确要求核对渲染输出才能发现）：

1. **node-exporter 双重 `-distroless` 后缀。** 子 Chart 的 `_helpers.tpl` 在
   `image.distroless=true` 时**追加** `-distroless`
   （`printf "...:%s%s" tag (ternary "-distroless" "" .Values.image.distroless)`）。
   若 `tag` 直接写 `v1.11.1-distroless`，实际渲染成 `v1.11.1-distroless-distroless`，
   与已验证的 amd64 摘要不是同一个镜像。正确写法是 `tag: v1.11.1` + `distroless: true`
   （父 Chart 默认已开）。修正后渲染为 `prometheus/node-exporter:v1.11.1-distroless`。
   研究附录 §3 已预警此点（"已带 -distroless 的显式 tag 不得再生成双重后缀"）。

2. **OpenSearch PVC 初始化镜像渲染成 Go map 字面量。** 该 Chart 的
   `templates/statefulset.yaml` 使用**扁平**字段
   `{{ .Values.persistence.image | default "busybox" }}:{{ .Values.persistence.imageTag | default "latest" }}`。
   按研究附录 §3 表格的字面写法（`persistence.image/imageTag`）本应是扁平的，
   若误写成嵌套 map（`persistence.image.repository/tag`）会渲染出
   `image: "map[repository:...:5000/library/busybox tag:1.37.0]:latest"` —— 既不是合法镜像引用，
   也会让 Chart 隐式回落到 `latest`。修正为扁平 `persistence.image` + `persistence.imageTag: "1.37.0"` 后
   渲染为 `172.16.101.20:5000/library/busybox:1.37.0`，与锁定摘要一致。

审计脚本已加入三项回归守卫：`map[...]` 字面量计数、`distroless-distroless` 计数、
残留 `{{` 模板分隔符计数，四份渲染均为 0。

**实际资源名（渲染结果）**：

| 类别 | 名称 |
| --- | --- |
| Prometheus CR / Service | `ani-metrics-prometheus` |
| Alertmanager CR / Service | `ani-metrics-alertmanager` |
| Operator Deployment / Service | `ani-metrics-operator` |
| KSM Deployment / Service | `ani-metrics-kube-state-metrics` |
| node-exporter DaemonSet / Service | `ani-metrics-prometheus-node-exporter` |
| admission hook Job ×2 | `ani-metrics-admission-create` / `ani-metrics-admission-patch` |
| ServiceMonitor ×7 | alertmanager / apiserver / kubelet / kube-state-metrics / operator / prometheus / prometheus-node-exporter |
| Loki StatefulSet / Service | `ani-loki` / `ani-loki` + `ani-loki-headless` + `ani-loki-memberlist` |
| OpenSearch StatefulSet / Service | `ani-opensearch-master` / `ani-opensearch-master` + `-headless` |
| Fluent Bit DaemonSet / Service | `ani-fluent-bit` |

**存储与保留实际值**：Prometheus `replicas:1, shards:1, retention:24h, retentionSize:4GB`，
PVC `ani-block` RWO 5Gi；Alertmanager `replicas:1`，PVC `ani-block` RWO 1Gi（AM 自身 retention `120h` 为 Chart 默认，非本批保留策略）；
Loki `replication_factor:1`、`auth_enabled:false`、`retention_period:72h`、
`retention_enabled:true`、`delete_request_store:filesystem`，PVC `ani-block` 5Gi；
OpenSearch 单节点、PVC `ani-block` 5Gi。

**Operator 参数中的镜像引用**（不能只扫 `spec.containers[].image`）：

```text
--prometheus-config-reloader=172.16.101.20:5000/prometheus-operator/prometheus-config-reloader:v0.90.1
--thanos-default-base-image=172.16.101.20:5000/thanos/thanos:v0.41.0
```

第二项是**已禁用功能（Thanos）的默认参数**：本批不启用 Thanos，不为此部署 Thanos 服务。
该引用已一并离线改写以保证闭包一致，并在材料锁中标记为 `unused_disabled_default`（`disabled: true`）。
它**不进入** `images.tsv`：本批不会实际运行该镜像，为消掉字符串而把它纳入离线包会虚增材料体积，
把它改成公网地址则更差。安装阶段需在闭包报告中明确它是禁用参数，不得误报为已运行 Thanos。

### C0 容量核对（历史值，实测前需刷新）

三台各 4CPU / 约 8GiB 内存、50Gi Ceph 空盘为历史记录值，**实际开测前须在节点上刷新**。
第一批 PVC 合计 17Gi，本批默认 Prom 5Gi + AM 1Gi + 一个日志后端 5Gi，合计约 28Gi 逻辑容量；
Ceph 三副本还需计入实际可用空间与预留。本批资源 request/limit 见执行方案第 7 节表格，
写入真实 Chart 字段并已核对渲染结果。装不下时报实际缺口，不改 emptyDir、不删 request、
不把单实例验证伪装成 HA。

---

## C1 / attempt: c1-feda-20260919

```text
card: C1（typed 配置、八行选择文件、最小公共接线）
code_status: pass
live_status: not_verified（新 kcn 材料缺席，未启动三台安装）
source tree: ~/ani-installer-runs/observability-20260919/src/kubekey（Fedora，非 git 工作树副本）
source hash: 见下方「代码变更」；本卡未提交、未推送
config hash: 由 writeComponentSelection 写入首行，随站点配置变化
KCN version / digest / validated status: v0.6.2 旧版（已知缺陷）；新版 not_provided / user_reported_fixed
enabled components / backend: 第一批四项可启用；本卡新增 metrics / loki / opensearch / fluent-bit 四行**已接线但未实现**，启用即失败
```

### 代码变更

| 文件 | 变更 |
| --- | --- |
| `kubekey/pkg/ani/config.go` | `componentsOrder` 4→8 行；新增 `MetricsComponent`/`LoggingComponent` 与默认值；`Selection()` 由 typed backend 推导 loki/opensearch/fluent-bit；新增 `parsePositiveQuantity`、`validRetention`、`validPositiveDays`、`validateMetrics`、`validateLogging`；新增 `componentSpec()` 抽出角色可见键 |
| `kubekey/pkg/ani/runner.go` | `componentChartMaterials` 1→6 条（补齐 NATS 遗漏 + 本批四张 Chart）；新增 `writeConnections()` 与 `connectionsDirName`，安装成功后拼装 `connections.md` |
| `kubekey/pkg/ani/components_test.go` | 8 行选择形状；10 个新增/重写测试（见下） |
| `kubekey/scripts/verify.sh` | `EXPECTED_COMPONENTS` 4→8 行；注释更新；失败文案去掉「foundation」限定 |
| `builtin/core/roles/ani/{cert-manager,postgresql,valkey,nats}/templates/connection.md` | 新增：各组件自述连接事实（namespace/DNS/端口/Secret 引用/PVC/保留/版本/验证） |
| `builtin/core/roles/ani/{cert-manager,postgresql,valkey,nats}/tasks/main.yaml` | 新增 `connections.d` 目录创建与 `connection.md` 渲染任务 |

### 两个被测试当场抓到的真实缺陷（已修）

1. **`logging.retentionDays` 默认值 `"3"` 会被自己拒绝。**
   `validateLogging` 复用了要求 `d` 后缀的 `validRetention`，而该字段按方案第 4.1 节文档是**纯天数**
   （`retentionDays: 3`）。即默认配置在启用日志时必然报错。已改为 `validPositiveDays`，
   现在 `"3"` 合法、`"3d"`/`"3h"`/`"0"`/`"-1"` 明确拒绝。

2. **`KubeKeyConfig` 的组件段无法被单元测试覆盖。**
   该函数开头调用 `Validate`，而本批四行未实现会命中 not-implemented 门，
   于是「spec 是否带对 metrics/logging 字段」永远测不到。已抽出 `componentSpec()`，
   使 spec 形状可独立测试，同时保持**唯一**一处决定角色可见键。

### 验收（方案第 8 节）

```text
go test ./pkg/ani                                    -> ok（2.247s）GATE1=0
go test -tags=builtin ./pkg/ani ./cmd/kk/app         -> ok GATE2=0
go test ./...（全仓，排除无测试包）                  -> 无失败输出
```

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 旧配置新增项全关 | pass | `TestLegacySiteConfigKeepsEveryNewSwitchOff`：旧站点文件仍 Validate 通过，前四行 true、后四行 false，metrics/logging 保持文档默认 |
| 原 APT/kubeconfig 测试不退化 | pass | `TestKubeKeyUsesInstallerKubeconfig`（/root + /home/ubuntu）、`TestDebianChronyPackageCheckUsesDpkg` 均 PASS |
| 日志双后端不可能产生 | pass | `TestTwoLogBackendsCannotBeExpressed`：`components.logging` 内写第二个 backend 键被严格解码拒绝；单一 backend 字符串不可能同时点亮 loki 与 opensearch |
| 选择缺行/格式不符失败 | pass | 实测 `verify.sh` 选择解析块 7 例全 PASS（见下） |
| 未实现启用即失败（非空循环） | pass | `TestEnabledBatchTwoComponentsFailUntilImplemented`：逐行断言四行都**确实进入**选择且都给出 not implement 错误，并强制四行全覆盖 |
| 渲染拒绝空 image / `<no value>` | pass | `TestComponentValuesRenderCompleteImages` 用完整镜像表渲染每个 `values.yaml`，拒绝 `map[`、`<no value>`、`{{` 与空 image 字段 |
| connections.md 生成 | pass | `TestConnectionsDocumentAssemblesEnabledFragments`（固定顺序、跳过未启用、启用缺片段即报错、0600）+ `TestConnectionFragmentsRenderSiteValues`（四角色片段真实渲染，无残留分隔符，携带站点存储值，无凭据） |
| 三个示例配置 | pass | `observability-{metrics,loki,opensearch}.yaml` 均干净解析、8 行选择、各字段单独校验通过，唯一失败是预期的 not-implemented 门 |

### verify.sh 选择解析实测（7 例，针对真实脚本块）

| 用例 | 期望 | 结果 |
| --- | --- | --- |
| 正确 8 行 | 接受 | PASS |
| 仅 4 行（旧安装器形状） | 拒绝 | PASS：`must have exactly 8 rows, got 4` |
| 行序错误 | 拒绝 | PASS：`row 2 must be cert-manager, got postgresql` |
| 首行非 64 位十六进制 | 拒绝 | PASS：`first line is malformed` |
| 配置摘要不匹配 | 拒绝 | PASS：`written for a different site config` |
| enabled 值非 true/false | 拒绝 | PASS：`row 2 has invalid enabled value maybe` |
| 缺首行 | 拒绝 | PASS：`first line is malformed` |

### connections.md 设计说明

方案要求「明确补实现成功后生成」，且**不得假设旧代码已生成**。实现取「角色自述 + 安装器拼装」：

- 每个角色 `templates/connection.md` 只描述**自己拥有的资源**，值来自 `.ani.components.*`，
  因此不存在第二份服务名/Secret 名副本（符合第 4.2 节「不另建映射表」）。
- 角色把片段渲染到 `/var/lib/ani-installer/<name>/work/connections.d/<component>.md`。
- `runner.go` 在 KubeKey 成功后按**固定八行顺序**拼装为主文件
  `/var/lib/ani-installer/<name>/connections.md`（0600，root-only）。
- 启用的组件若没有片段 → **报错**，绝不输出残缺的连接说明。
- 片段只写 Secret **名称与键名**，从不写值；也不打印任何私钥/密码/token。
- 本卡只覆盖已实现的第一批四项；metrics/loki/opensearch/fluent-bit 的片段随各自卡（C2–C4）添加，
  届时无需改动 `runner.go`。

### 本卡边界（未做，按计划留给后续卡）

- 未实现任何第二批 role；启用 metrics/logging 仍以明确错误拒绝。
- 未改第一批组件业务实现（只加连接事实模板与两个纯新增任务）。
- 未创建空壳 role、未吞错误、未接入 Grafana/Thanos/远程写入/Ingress。
- 未提交、未推送、未发布；未接触测试集群 172.16.101.20/.21/.22。
- 未使用快照、重置、清盘、清 CNI/OVN、删失败 PVC/命名空间或重启循环。

### 已知问题 / 下一步

- 上游阻塞不变：新版 kcn 未提供材料 → live 保持 `not_verified`。
- 已知小瑕疵（非本卡引入）：`pkg/ani/kubeconfig_env_test.go` 与若干 `builtin/core/playbooks/*.yaml`
  为 CRLF。本卡顺手把 `kubeconfig_env_test.go` 规范为 LF（gofmt 要求）；playbook 的 CRLF 留待单独处理，
  以免在本卡扩大改动面。
- 下一卡：**C2 / 2A 指标与告警**（`ani/metrics` role，kube-prometheus-stack 85.4.0，
  release `ani-metrics`，ns `ani-observability`），只在 Fedora 做代码/渲染/测试，live 仍 `not_verified`。

---

## C2 / attempt: c2-feda-20260919

```text
card / attempt: C2 指标栈 / c2-feda-20260919
code_status: pass
live_status: not_verified
source hash / kk hash / artifact manifest hash / config hash:
  images.tsv sha256 c4f899cfce8af5275440e70867ae053765b76de49f16cf0e8512062fdfa7ddf9
  chart sha256 3b07b7c91f1eaec75a125d1938c4e10d3b1ef6076ff7226d031fadadb4651a60
  rendered values sha256 8a5d1a94376de1257774e5eedecae6131e0c18bf7d37003cebcc5c2637c3149c
  helm v3.20.0+gb2e4314（离线，--kube-version 1.35.8）
KCN version / digest / validated status:
  未使用、未修改、未回补；新版材料未提供 → 不启动三台安装
enabled components / backend / chart and app versions:
  components.metrics.enabled=true；kube-prometheus-stack 85.4.0（appVersion v0.90.1）
  release ani-metrics；namespace ani-observability；单副本、无 HA、无 Grafana/Thanos
snapshot 3/3 / offline before-after / install exit:
  not_verified（本卡未接触测试集群）
metrics scrape-query / firing-resolved / logs three-node ingestion:
  not_verified（verify.sh 已实现真实 HTTP API 查询与 firing/resolved 双相，但未在节点执行）
normal Pod rebuild persistence / PVC and Secret UIDs:
  not_verified（verify.sh 已实现 UID 与 range-query 回读，但未在节点执行）
retention configuration / actual expiry (separate):
  配置 retention 24h / retentionSize 4Gi（< 卷 5Gi）；实际过期行为 not_verified
known issue / owner / exact next step:
  见下「已知问题 / 下一步」
```

### 代码变更

**新增 role `ani/metrics`**（`kubekey/builtin/core/roles/ani/metrics/`）

- `tasks/main.yaml`：建工作目录与 `connections.d`、建命名空间、校验打包 helm、渲染并收紧（0600）
  values、`helm upgrade --install`（`--wait --timeout 1200s`，材料只在 artifact 内，无网络取用）；
  等待 `deployment/ani-metrics-operator`、`statefulset/prometheus-ani-metrics-prometheus`、
  `statefulset/alertmanager-ani-metrics-alertmanager`、`deployment/ani-metrics-kube-state-metrics`、
  `daemonset/ani-metrics-prometheus-node-exporter`（含节点数核对），两个 PVC 用
  `--for=jsonpath='{.status.phase}'=Bound` 等绑定；渲染并执行 `verify.sh`（0700）；
  渲染 `connection.md` 到 `connections.d/metrics.md`。
- `templates/values.yaml`：单副本指标栈，关 Grafana/windows、关 defaultRules、关
  etcd/controller-manager/scheduler/kube-proxy/CoreDNS/kube-dns 目标（这些需要 loopback 端口），
  CRD 随 Chart、关闭 CRD upgradeJob；admission webhook 用 cert-manager 之外的自签路径（`certManager.enabled=false`）；
  Prometheus CR `replicas: 1`、`shards: 1`、`retention`/`retentionSize` 来自站点、RWO PVC、
  `ruleSelector` 锁到 `release: ani-metrics` 且 `ruleNamespaceSelector` 锁到本命名空间、
  `remoteWrite: []`、关 admin API 与 OTLP；Alertmanager CR 单副本、RWO PVC、
  `alertmanagerConfigSelector` 锁到本 run 的 `run_id`；node-exporter 容忍 control-plane 污点、
  用 distroless 且 **tag 写不带后缀的版本**。
- `templates/verify.sh`：8 段真实验收 —— [1] 工作负载、[2] Prometheus HTTP API 实查
  （`up{job="prometheus-node-exporter"}`、`count(node_uname_info)`、`count(kube_node_info)`、
  `count(container_memory_working_set_bytes{...})`）、[3] 临时接收器（离线 python 镜像，
  ConfigMap+Deployment+Service 落存请求体）、[4] 带 `sendResolved: true` 且按 `run_id` 限范围的
  AlertmanagerConfig、[5] 用 `vector(1) == 1` 触发并按指纹核对接收、[6] 用 `vector(0) == 1`
  触发 resolved 并核对指纹相等、[7] Prometheus PVC/STS UID + 重建前打样本标记、重建后
  range-query 回读、[8] Alertmanager silence 建/重建/回读 + PVC 与 Secret UID 稳定；
  结束时只删本卡自建的临时对象。
- `templates/connection.md`：连接说明（命名空间、Chart 版本、工作负载、Service DNS、版本、
  未覆盖范围、PVC、保留期、关闭项、告警/规则接收标签、Secret 仅记名字、校验入口），无明文凭据。

**接线**

- `builtin/core/playbooks/create_cluster.yaml`：在 `ani/nats` 后加
  `role: ani/metrics`，`when` 锁到 `(index .ani.components "metrics").enabled`。
- `pkg/ani/config.go`：`ImplementedComponents` 增加 `metrics`；`MetricsComponent` 增加
  `PrometheusRetentionSize`（Kubernetes 容量量纲，非 `GB`）；`DefaultComponents()` 填 `4Gi`；
  `validateMetrics` 增加保留量与卷容量关系校验（必须小于卷）；新增 `MetricsNamespace` 常量与
  `metricsRunID`；`componentSpec` 现产出 `enabled/namespace/storage_class/prometheus_storage_size/
  alertmanager_storage_size/prometheus_retention/prometheus_retention_size/run_id`。
- `pkg/ani/images.go`：新增 `SplitReference` / `ImageKey` / `componentImageKeys()` /
  `SplitImageReferences` / `ComponentImageParts`，把 Chart 自己拼接的镜像拆成
  registry/repository/tag 三段。

### 三个被测试与渲染当场抓到的真实缺陷（已修）

1. **镜像双前缀**（最关键）：`kube-prometheus-stack` 的若干子 Chart 自己拼
   `registry/repository:tag`。原先把整条本地引用塞进 `registry` 字段，渲染成
   `192.0.2.11:5000/prometheus/node-exporter:v1.11.1-distroless/prometheus/node-exporter:v1.11.1-distroless`，
   永远拉不到。修法：新增按段拆分并把 `registry` 只留主机名。
2. **node-exporter 双后缀**：镜像锁里该镜像是 `…:v1.11.1-distroless`，而子 Chart 在
   `distroless: true` 时会**自己再追加** `-distroless`。修法：`ImageKey.TagOverride` 把该键的 tag
   显式取为 `v1.11.1`，由 Chart 补一次后缀。渲染实测为 `…/prometheus/node-exporter:v1.11.1-distroless`，
   恰好一个后缀。
3. **`4GB` 不是合法容量**：`resource.ParseQuantity("4GB")` 直接报错（只接受 `B/Ki/Mi/Gi/G` 等），
   默认值与示例、测试全部改为 `4Gi`。

另修同类问题两处：`SplitImageReferences(..., componentImageKeys)` 少写调用括号（编译失败）；
`ComponentSpecForRender` 原先走 `Validate`，使离线渲染被三节点拓扑前置条件卡住，改为直接用
组件默认值构造 spec。

### 验收（Fedora，离线）

- `gofmt -l pkg/ani` 无输出；`go build ./pkg/ani/` 通过；`go test ./pkg/ani` **ok**。
- `${R} lab/c2-render-gate/render-values.sh <repo> <images.tsv> <out>`：用**真实 `pkg/ani` 代码**
  构造渲染上下文（不是手抄一份 key），渲染 role 的 values，输出 6149 字节合法 YAML。
- `render-gate.sh <chart.tgz> <values.yaml> <out>`：渲染 **76527 行**，全部门禁项通过：
  - 等待对象齐备：`deployment/ani-metrics-operator`、`daemonset/ani-metrics-prometheus-node-exporter`、
    `Prometheus/ani-metrics-prometheus`、`Alertmanager/ani-metrics-alertmanager`、ServiceMonitor、
    两个 CRD；
  - 渲染里**没有** StatefulSet（Prometheus/Alertmanager 的 STS 由 Operator 运行时生成，role 的
    等待名是 `…-ani-metrics-prometheus` / `…-ani-metrics-alertmanager`）；
  - 越界对象缺席：Grafana、ThanosRuler、Ingress、`ani-metrics-thanos`、`prometheus.thanos`；
  - 唯一允许的 Thanos 字符串是 Operator 的关闭特性默认参数 `--thanos-default-base-image`，已标注；
  - 镜像：**离线引用 7/7**，外来 0，关闭默认 1；7 个运行镜像逐个核对命中；
  - node-exporter **无双重 `-distroless`**。
- 证据：`~/ani-installer-runs/observability-20260919/evidence/c2-render-gate/`
  （`values.yaml`、`rendered.yaml`、`rendered.yaml.workloads`、`provenance.txt`）。

### 一处操作事故与修复（如实记录）

第一次跑 `render-values.sh` 时把参数顺序写错（该脚本签名为 `<repo-root> <images.tsv> <out>`，
误把 images.tsv 当输出路径），**覆盖了 `src/kubekey/ani/images.tsv`**。发现后立即用工作区内
完好副本还原，`sha256` 与还原前后一致
（`c4f899cfce8af5275440e70867ae053765b76de49f16cf0e8512062fdfa7ddf9`，与本地一致），
材料未受损、渲染门禁复跑通过。同时给脚本加了防呆：第二参数必须形如 `*/images.tsv`、
第三参数必须 `.yaml`、输入输出不得同路径、输出目录归属相对路径会显式转绝对路径
（原先相对路径会被子进程的工作目录劫持）、覆盖非渲染产物时拒绝写入。

### 本卡边界（未做）

- 未实现 loki / opensearch / fluent-bit（未实现的启用仍以明确错误拒绝）。
- 未加入 Grafana、Dashboards、Jaeger、ANI 业务、Milvus、计算/GPU、Harbor、HA。
- 未接触测试集群 172.16.101.20/.21/.22；未使用快照、重置、清盘、清 CNI/OVN、
  删失败 PVC/命名空间或重启循环。
- 未提交、未推送（本卡完成时尚未提交）。

### 已知问题 / 下一步

- 上游阻塞不变：新版 kcn 未提供材料 → C2 的 live 保持 `not_verified`，不启动三节点安装。
- 下一卡：**C3 / Loki + Fluent Bit**（`ani/loki`、`ani/fluent-bit` role），同样只在 Fedora
  做代码/渲染/测试，live 仍 `not_verified`。

## C3 / attempt: c3-feda-20260919

```text
card / attempt: C3 Loki + Fluent Bit / c3-feda-20260919
code_status: pass
live_status: not_verified
source hash / kk hash / artifact manifest hash / config hash:
  images.tsv sha256 c4f899cfce8af5275440e70867ae053765b76de49f16cf0e8512062fdfa7ddf9（本卡未改）
  loki chart sha256 ddb31751a90269980332eb17ddbd67f2971fc1b0e847d6d1752a749c8e86232a
  fluent-bit chart sha256 2404614ace4c7dc049b76fd39d13b695f7b66332c38095327ebaa8fc91f0b046
  helm v3.20.0+gb2e4314（离线，--kube-version 1.35.8）
  逐文件哈希见 evidence/c3-render-gate/provenance.txt
KCN version / digest / validated status:
  未使用、未修改、未回补；新版材料仍未提供 → 不启动三台安装
enabled components / backend / chart and app versions:
  components.logging.backend=loki → 选择文件 loki=true / opensearch=false / fluent-bit=true
  loki chart 18.13.3（app 3.7.8，deploymentMode Monolithic）；fluent-bit chart 0.58.2（app 5.1.2）
  release ani-loki / ani-fluent-bit；namespace ani-observability（与指标栈同命名空间）
snapshot 3/3 / offline before-after / install exit:
  not_verified（本卡未接触测试集群）
metrics scrape-query / firing-resolved / logs three-node ingestion:
  not_verified（verify.sh 已实现三节点 marker → 查询后端 + 元数据核对，但未在节点执行）
normal Pod rebuild persistence / PVC and Secret UIDs:
  not_verified（verify.sh 已实现 Loki Pod 重建 + PVC UID 比对 + marker 回读，
  以及 Fluent Bit Pod 重建 + 游标留存 + 新旧 marker 并存，但未在节点执行）
retention configuration / actual expiry (separate):
  配置限值 retention_period=72h 且 compactor retention_enabled + filesystem delete store；
  实际过期删除 not_verified（retention-expiry=not_verified 由脚本显式写出，不冒充通过）
known issue / owner / exact next step:
  见下「已知问题 / 下一步」
```

### 代码变更

**新增 role `ani/loki`**（`kubekey/builtin/core/roles/ani/loki/`）

- `templates/values.yaml`：单体 Loki，`deploymentMode: Monolithic`，`auth_enabled: false`；
  `commonConfig.replication_factor: 1`；`storage.type: filesystem`；schema v13 tsdb + filesystem
  + `index_` 前缀 + 24h 周期；`limits_config.retention_period` 取站点派生的小时数；
  compactor 打开 `retention_enabled: true`、`delete_request_store: filesystem`、
  `working_directory: /var/loki/compactor`；ingester WAL 落 PVC 且 `flush_on_shutdown: true`；
  tsdb_shipper 的 index/cache 目录同在 PVC；`analytics.reporting_enabled: false`；
  singleBinary 单副本、`sidecar: false`、PVC 用站点 storageClass/size、`whenScaled/whenDeleted: Retain`、
  `enableStatefulSetAutoDeletePVC: false`；资源 100m/256Mi 请求 + 1Gi 上限；
  `read/write/backend` 三组副本数全为 0；`chunksCache`/`resultsCache`/`gateway`/`lokiCanary`/`test`/
  `ruler`/`minio` 全部关闭。
- `tasks/main.yaml`：建工作目录与 `connections.d`、建命名空间、校验打包 helm、渲染并收紧（0600）
  values、`helm upgrade --install ani-loki <artifact>/charts/loki/18.13.3.tgz --wait --timeout 900s`；
  等 `statefulset/ani-loki`（单体是 StatefulSet，等 Deployment 永远不会成功）；
  等 `pvc/storage-ani-loki-0` 绑定；渲染并执行 `verify.sh`（0700）；渲染 `connection.md`。
- `templates/verify.sh`：5 段 —— [1] StatefulSet/pod/PVC（`readyReplicas=1`、PVC Bound、
  记录 PVC 与 STS UID、断言存在名为 `storage` 的 volumeClaimTemplate，避免 emptyDir 冒充持久卷）；
  [2] 从临时 python pod 走集群内真实 HTTP API（`/ready`、`/loki/api/v1/status/buildinfo`、
  一次真实 `/loki/api/v1/query_range`）；[3] `GET /config` 核对生效配置（retention_period、
  compactor.retention_enabled、delete_request_store、replication_factor、schema store/object store）；
  [4] 只允许 Loki StatefulSet，无 Deployment/DaemonSet，Service 为 ClusterIP，全集群不得存在
  grafana/dashboards 工作负载；[5] 只删本次自建 client pod。
- `templates/connection.md`：命名空间、后端为首次安装选择而非运行期开关、版本、
  `statefulset/ani-loki`、Service DNS 仅 ClusterIP、filesystem PVC 与保留期/compactor 说明、
  `auth_enabled: false` 明确**不是**认证特性、关闭项、无凭据、采集路径由 Fluent Bit 卡验证。

**新增 role `ani/fluent-bit`**（`kubekey/builtin/core/roles/ani/fluent-bit/`）

- `templates/values.yaml`：`kind: DaemonSet`；镜像 `repository` **带站点 registry 前缀**、`tag` 来自
  `logs.fluentBit`；`testFramework.enabled: false`、`hotReload.enabled: false`；
  `config.service` 用**字面量**（`Flush 1` / `Log_Level info` / `HTTP_Port 2020`，不用 Chart 的
  `.Values.*`，见下「真实缺陷 3」），本地缓冲有界（`storage.path`、`storage.backlog.mem_limit 10M`）；
  `config.inputs` 只保留 tail（`/var/log/containers/*.log`、`multiline.parser cri`、
  `DB /var/lib/fluent-bit/tail.db`、`DB.locking true`、`storage.type filesystem`），**删除 systemd 输入**；
  `config.filters` 的 kubernetes filter 设 `Labels Off`/`Annotations Off`（不把全部标签提升为 Loki 标签）；
  `config.outputs` 按后端**只渲染一个**输出（loki 用 `Name loki` + `Line_Format json` +
  `Auto_Kubernetes_Labels Off`；opensearch 用 `Name opensearch` + 从 Secret 注入的
  `HTTP_User`/`HTTP_Passwd` + TLS）；`daemonSetVolumes` 只保留 `varlog` 与
  `fluent-bit-state`（hostPath `/var/lib/ani-installer/fluent-bit`，`DirectoryOrCreate`），
  **删除 Chart 的 `/var/lib/docker/containers` 默认路径**；`/var/log` 只读挂载；
  只容忍 control-plane 污点；资源 50m/64Mi 请求 + 128Mi 上限。
- `tasks/main.yaml`：建工作目录、建命名空间、校验 helm、渲染并 0600 values、
  `helm upgrade --install ani-fluent-bit <artifact>/charts/fluent-bit/0.58.2.tgz --wait --timeout 600s`；
  等 `daemonset/ani-fluent-bit` 并**核对就绪数等于节点数**（少一个节点的采集器会静默丢该节点日志）；
  渲染并执行 `verify.sh`（0700）；渲染 `connection.md`。
- `templates/verify.sh`：这是后端 role 无法自证、也最容易被伪造的一段 —— 全部断言都走真实路径
  （容器 stdout → 运行时写容器日志文件 → 采集器 tail → **查询后端**），从不直接 push 给后端：
  [1] 从运行中的 pod 读回真实渲染配置，断言**恰好一个输出**且是所选后端、无残留 ES 输出、
  无 systemd 输入、无 Docker 路径、游标与缓冲在持久目录、缓冲有界、state 目录是 hostPath；
  [2] 三台各起一个指定 `nodeName` 的 marker pod，stdout 写唯一 marker（含本次 run 与序号），
  先从 pod 自身日志确认期望值，再轮询**后端查询 API**直到三份 marker 全部出现，
  并核对 namespace/pod/container/node 四项元数据（期望节点由 marker 的 `-nN` 后缀推出，
  不是复述后端返回什么）；[3] 只删 Loki Pod 触发重建，比对重建前后 **PVC UID 不变**，
  再回读三份 marker；[4] 只删一台的 Fluent Bit Pod，核对 `tail.db` 仍在持久目录、
  该节点续产新 marker、且**新旧 marker 同时可查**；[5] 重读生效 retention 配置并把
  `retention-expiry=not_verified` 显式写出（不把"配了"当"删过了"）；[6] 只删本次自建的测试 pod。
- `templates/connection.md`：命名空间、版本、`daemonset/ani-fluent-bit`、只采集容器 stdout
  （不采 kubelet journal）、元数据字段、**单一写路径**、本地有界缓冲不是后端 PVC 的替代、
  按后端给出目标地址、凭据只来自 Secret、验证方式，以及过期删除未验证。

**接线**

- `builtin/core/playbooks/create_cluster.yaml`：`ani/metrics` 之后加
  `role: ani/loki`（`when` 锁到 `loki` 行开启**且** `logging.backend == "loki"`）与
  `role: ani/fluent-bit`（`when` 锁到 `logging.enabled` 且 backend 不为 `none`）；
  后端在采集器之前，指标栈在两者之前。两个后端 role 由 backend 字符串互斥，结构上不可能同时装上；
  没有后端时不会出现采集器。
- `pkg/ani/config.go`：`ImplementedComponents` 增加 `loki`、`fluent-bit`
  （**opensearch 仍不在列表**，C4 才加，启用它继续以明确错误拒绝）；
  `LoggingComponent.storage` 归入 `loki` 行，使"启用 loki 但没配容量"在校验期就失败；
  `componentSpec` 的 `logging` 产出 `enabled/backend/namespace/storage_class/storage_size/
  retention_days/retention_hours/retention_iso`，新增 `retentionHours` / `retentionISOSeconds`
  两个派生函数（站点只写天数，单位换算只在这一处）。
- `pkg/ani/images.go`：新增 `logs.loki`、`logs.fluentBit` 两个镜像键。

### 三个被渲染门当场抓到的真实缺陷（已修）

1. **Fluent Bit 镜像丢主机名**（最关键）：该 Chart **没有 registry 字段**，helper 直接
   `printf "%s:%s" .repository .tag`。原先按拆分习惯只填 `fluent/fluent-bit`，渲染出
   `fluent/fluent-bit:5.1.2` —— 每台节点都会去 Docker Hub 拉，离线环境必然拉不到。
   修法：`repository` 写成 `<站点registry>/fluent/fluent-bit`，成为完整主机加路径。
   （与 C2 的 kube-prometheus-stack 恰好相反：那个有 registry 字段且**不能**塞整条引用。
   两个 Chart 的拼接约定不同，只能用渲染门逐 Chart 实测，不能靠类推。）
2. **`{{ .Values.* }}` 在 role 模板里不解析**：服务段照抄 Chart 默认值写成
   `Flush {{ .Values.flush }}`，但那只在 Helm 的第二遍渲染生效；我们的 role 模板由 installler
   自己用 `text/template` 渲染，读到的是 `<no value>`，任何看这份 values 的人（包括渲染门本身）
   都无法区分"模板坏了"和"正常没值"。修法：服务段三处改写字面量，并保留顶层同名键；
   同时在 `TestComponentValuesRenderCompleteImages` 里加"非注释行不得出现 `.Values.`"的成因断言。
3. **verify.sh 内嵌 Python 的 LogQL 花括号被 Go 模板抢先解析**：LogQL 选择器 `{job="fluent-bit"}`
   里的 `{{` 被 Go 的 `text/template` 当成模板起始，`parse template: bad character U+003D '='`。
   修法：`LBRACE, RBRACE = chr(123), chr(125)` 拼出括号，生成的查询完全等价，
   两处内嵌 Python 都已改。

另有两处由测试/门禁逼出的自身错误：`image_parts` **不产出 `registry` 字段**
（只有 repository/tag，主机来自 `.ani.registry`），我最初的断言写错了字段名；
以及 gofmt 检查范围原先覆盖整个 `builtin/`，而上游仓库本就有两个 CRLF 文件
（`builtin/core/fs.go`、`builtin/core/upgrade_path.go`，仓库里就是这么提交的），
已把范围收窄到本次工作真正拥有的 `pkg/ani` 与 `builtin/core/roles`，并在输出里标注那两个上游文件，
避免把无关仓库改动混进来。

### 验收（Fedora，离线）

- `gofmt -l pkg/ani builtin/core/roles` 无输出；`go build ./pkg/ani/...` 通过；
  `go vet ./pkg/ani/...` 通过；`go test ./pkg/ani` **ok**。
- `lab/c3-render-gate/render-values.sh`：用**真实 `pkg/ani` 代码**构造渲染上下文
  （非手抄 key），渲染出 loki 3906B、fluent-bit(loki) 5195B、fluent-bit(opensearch) 5784B，
  三份均通过 YAML 解析。
- `lab/c3-render-gate/render-gate.sh`：三份 Chart 渲染全部通过门禁 ——
  - **loki**（441 行）：StatefulSet/Service/headless Service/ConfigMap 齐备；无 Deployment；
    存在 `storage` volumeClaimTemplate；越界对象（chunks-cache、results-cache、gateway、
    canary、ruler、minio、grafana、Ingress）全部缺席；retention_period + compactor retention
    + filesystem delete store 三项同时存在；镜像 `192.0.2.11:5000/grafana/loki:3.7.8`，外来 0。
  - **fluent-bit / loki 后端**（238 行）：DaemonSet 且无 Deployment/StatefulSet；
    恰好一个输出且为 loki；无 ES 输出、无 systemd 输入、无 Docker 路径；游标与有界缓冲在 hostPath；
    `/var/log` 只读；filter 标签/注解关闭且 loki 输出不自动加标签；
    镜像 `192.0.2.11:5000/fluent/fluent-bit:5.1.2`，外来 0。
  - **fluent-bit / opensearch 后端**（262 行）：同样单输出、无残留 ES 输出，
    输出为 opensearch，且**不出现** loki 专有参数 `Auto_Kubernetes_Labels`；其余项同上。
    （C4 的 opensearch role 还没写，但采集器的两个分支现在都已验证能正确渲染。）
- 证据：`~/ani-installer-runs/observability-20260919/evidence/c3-render-gate/`
  （三份 rendered yaml 与 `.workloads`、三份 values、三份门禁日志、`provenance.txt`）。
- **保留期与过期分开记录**：脚本把 `retention-expiry=not_verified` 写入证据，
  因为观察真实删除需要等保留期过去，本卡不伪造该结论。

### 本卡边界（未做）

- 未实现 `ani/opensearch`（`opensearch` 仍不在 `ImplementedComponents`，启用它仍以明确错误拒绝）。
- 未加入 Grafana、Dashboards、Jaeger、ANI 业务、Milvus、计算/GPU、Harbor、HA。
- 未接触测试集群 172.16.101.20/.21/.22；未使用快照、重置、清盘、清 CNI/OVN、
  删失败 PVC/命名空间或重启循环；`verify.sh` 内没有任何 `delete pvc`/`delete namespace`/
  `--force`/`--grace-period=0`。
- 未提交、未推送。

### 已知问题 / 下一步

- 上游阻塞不变：新版 kcn 未提供材料 → C3 的 live 保持 `not_verified`，不启动三节点安装。
- Loki Chart 在**两个缓存都关闭**时仍渲染一个空的 `ServiceAccount ani-loki-memcached`。
  它不是工作负载、不拉 memcached 镜像，实测整个渲染中唯一的 `image:` 只有 `grafana/loki:3.7.8`，
  因此只报告不作失败判定，避免把门禁降级成"永远能过"。
- 下一卡：**C4 / OpenSearch + Fluent Bit**（`ani/opensearch` role，把 `opensearch` 加入
  `ImplementedComponents` 并接到 playbook 的同一处 backend 判断），live 仍 `not_verified`。

---

## C4 / attempt: c4-feda-20260919

```text
card / attempt: C4 / c4-feda-20260919
code_status: pass
live_status: not_verified
source hash / kk hash / artifact manifest hash / config hash: 未提交（本卡无源码外发）；
  artifact 未重建 —— 本卡只加 Chart 消费与 role，Chart 与镜像材料沿用 C0 锁定的候选
KCN version / digest / validated status: 未提供 / 无 / user_reported_fixed（不变量，本卡未接触）
enabled components / backend / chart and app versions:
  components = metrics + (loki | opensearch 二选一) + fluent-bit；backend = opensearch
  chart 3.8.0 / app 3.8.0（tgz sha256 cad6c77d04ec2389be6f61b5422ce61e7264eb7145e42b4c78dbc00e9d7dbe4d）
  fluent-bit chart 0.58.2 / app 5.1.2
snapshot 3/3 / offline before-after / install exit: 未执行（不启动三台安装）
metrics scrape-query / firing-resolved / logs three-node ingestion: not_verified
normal Pod rebuild persistence / PVC and Secret UIDs: not_verified
retention configuration / actual expiry (separate): 配置已生成（ISM `min_index_age` =
  `retention_iso`，如 PT72H）；实际过期 not_verified（需等保留期过去）
known issue / owner / exact next step: 见下方"已知问题 / 下一步"
```

### 代码变更

- 新增 `builtin/core/roles/ani/opensearch`（`values.yaml`、`tasks/main.yaml`、
  `templates/{certificates.yaml,security-config.yaml,security-init.yaml,setup-indices.sh,verify.sh,connection.md}`），
  用包内 `artifact_root/bin/helm` 对 C0 已落地的 `charts/opensearch/3.8.0.tgz` 渲染。
- 安全模型是**真的安全模型，不是 demo 模式**：`DISABLE_INSTALL_DEMO_CONFIG=true`、
  `plugins.security.allow_unsafe_democertificates: false`、
  `plugins.security.allow_default_init_securityindex: false`，http 走 https，
  密钥 PKCS#8，节点证书 CN `ani-opensearch-node`（serverAuth + clientAuth），
  管理员证书 CN `ani-opensearch-admin`，由 `securityadmin.sh -cd /security-config
  -cacert/-cert/-key` 播种，**不带** `--enable-demo`；Basic 认证后端 `type: intern`
  （不是 `internal`，写错会静默不生效）。
- 关闭项（有意为之）：`sysctl` / `sysctlInit`（后者是 `privileged: true` 的 init 容器）、
  `serviceMonitor`、`plugins`、`rbac.create`、`podSecurityPolicy`、`networkPolicy`、
  `antiAffinity: soft`、`keystore`、`masterTerminationFix`；`singleNode: true`。
  TLS 由既有内部 `ani-ca` ClusterIssuer 签发（沿用第一批 B1）。
- `pkg/ani/config.go`：`ImplementedComponents` 扩到 8 个（本批可观测性四项全部落地）；
  `storage()` 新增 `opensearch` 分支，与 `loki` 同形，两者共用 `logging.StorageClass` /
  `logging.StorageSize`；`fluent-bit` 仍不入列（它的暂存卷是 hostPath，不是组件卷）。
- `pkg/ani/images.go`：新增 `logs.opensearch`、`lab.python`、`lab.busybox` 三个镜像键。
- `builtin/core/playbooks/create_cluster.yaml`：顺序为 `metrics → loki → opensearch → fluent-bit`，
  `ani/opensearch` 的 `when` 锁到 `opensearch` 行开启**且** `logging.backend == "opensearch"`。
  两个后端 role 由同一个 backend 字符串互斥，结构上不可能同时装上；没有后端就不会出现采集器。
- 八行选择文件**未加行**：`loki`/`opensearch`/`fluent-bit` 三行仍由 `logging.backend`
  这一个字符串派生（C1 的 `Components.Selection()` 不变），因此两个后端永远不可能同时为 true。

### 修掉 C3 留给 C4 的两个采集器缺陷（都影响真实通过）

1. **OpenSearch 的 PVC 常量写错**：C3 的 `fluent-bit/templates/verify.sh` 里写死
   `data-ani-opensearch-master-0`。而该 Chart 的 `volumeClaimTemplates[0].metadata.name`
   是模板 `opensearch.uname` = `<clusterName>-<nodeGroup>` = `ani-opensearch-master`，
   真实 PVC 是 **`ani-opensearch-master-0`**（不是 `data-` 前缀）。名字错 → 校验一定找不到 PVC。
   修法：改成 `case` 显式给出 `BACKEND_PVC`，opensearch 分支用 `ani-opensearch-master-0`，
   选择器用 `app.kubernetes.io/name=opensearch`；同时把选择器从内联 `$([ ... ] && ...)` 提成常量。
2. **OpenSearch 轮询走明文 http 且无凭据**：C3 的标记等待与保留期回读都用 `http://` 且不带认证，
   而本卡把集群配成了 https + 强制认证，它**永远看不到自己要校验的集群**。
   修法：`BACKEND = opensearch` 时从 `ani-opensearch-node-tls` 取 `ca.crt`、
   从 `ani-opensearch-fluent-bit` 取 `username`/`password` 拷进客户端 Pod，
   两处 python 改用 `https://` + `ssl.create_default_context(cafile=...)` + Basic 认证。

### 三个被渲染门当场抓到的真实结论

1. **OpenSearch Chart 给 `persistence.image` 也加 registry 前缀**（本卡最关键）：
   `opensearch.dockerRegistry` helper 会把 `global.dockerRegistry`（带尾斜杠）前置到
   **`image.repository` 和 `persistence.image` 两个字段**（`statefulset.yaml:252`）。
   若照 C3 的 Fluent Bit 习惯在 `persistence.image` 里写 `<registry>/library/busybox`，
   渲染出 `192.0.2.11:5000/192.0.2.11:5000/library/busybox:1.37.0` —— 双重前缀，必然拉不到。
   修法：`persistence.image` / `imageTag` 只写**不含 registry** 的 `library/busybox`，
   门禁同时断言"裸形式存在"与"前缀形式不存在"。
   （至此三个 Chart 三种拼接约定：kube-prometheus-stack 有 registry 字段且不能塞整条引用；
   fluent-bit **没有** registry 字段必须塞完整主机；opensearch 有 registry 字段且会额外作用于 init 镜像。
   只能逐 Chart 用渲染实测，不能类推。）
2. **Chart 一定会渲染一个 PodDisruptionBudget**：`maxUnavailable: 1` 是非空默认值，
   所以 PDB `ani-opensearch-master-pdb` 在任何配置下都会出现。**有意不"修"**：
   单副本下 `1` 等于不设限（无害），改成 `0` 反而会阻塞节点排空，两者都比留默认差。
   门禁只**报告**这一条，只对真正缺席的对象做断言（Ingress / ServiceMonitor / NetworkPolicy /
   PodSecurityPolicy / Grafana / Dashboards / Deployment），不把门禁降级成"永远能过"。
3. **`securityConfigSecret` 会让 Chart 一个 Secret 都不渲染**：用
   `securityConfig.config.securityConfigSecret` 时，Chart 把外部 Secret 整目录挂到
   `securityConfig.path`，自己**不产出** Secret（我最初断言 `ani-opensearch-master-securityconfig`
   是错的，实际产出的是 ConfigMap `ani-opensearch-master-config`）。
   这也是为什么八个安全文件**内联**在 role 的 `security-config.yaml` 里：
   其中 `internal_users.yml` 的两个 bcrypt hash 只能在节点上算，
   Chart 自建的 Secret 事后没法改而不重启 Pod。门禁改成"断言 Pod 从该 Secret 挂载"+"渲染里不得出现
   `securityconfig` Secret"。

### 验收（Fedora，离线）

- `gofmt -l pkg/ani builtin/core/roles` 无输出（含两个上游 CRLF 文件的既有 NOTE）；
  `go build ./pkg/ani/... ./cmd/...` 通过；`go vet` 通过；`go test ./pkg/ani/...` **ok**。
- `lab/c4-render-gate/render-values.sh`：用真实 `pkg/ani` 代码构造上下文，渲染出
  loki 3906B、opensearch 5099B、fluent-bit(loki) 5536B、fluent-bit(opensearch) 6125B，
  四份均通过 YAML 解析。
- `lab/c4-render-gate/render-gate.sh`：四份 Chart 渲染全部通过门禁 ——
  - **loki**（441 行）：回归通过；`foreign: 0`；`loki render gate passed`。
  - **opensearch**（293 行）：StatefulSet `ani-opensearch-master` / Service / headless Service
    `ani-opensearch-master-headless` / ConfigMap `ani-opensearch-master-config` 齐备；无 Deployment；
    `volumeClaimTemplates` 名为 `ani-opensearch-master`；关闭 demo 配置的环境变量与
    `allow_unsafe_democertificates: false`、`allow_default_init_securityindex: false`、
    `plugins.security.ssl.http.enabled: true`、两个 CN 均在；安全配置从
    `secretName: "ani-opensearch-security-config"` 挂到
    `/usr/share/opensearch/config/opensearch-security` 且渲染中无 Chart 自建 Secret；
    无 `- name: sysctl` init 容器、无 `privileged: true`；`discovery.type: single-node`；
    role 自身安全配置完整（`anonymous_auth_enabled: false`、`type: intern`、
    `CN=ani-opensearch-node`、**恰好 2 个**占位符、无内置演示账号）；Job 受限且非 demo
    （`kind: Job`、`securityadmin.sh`、`/admin-tls/ca.crt`、`hash.sh`、`/dev/urandom`、
    namespaced Role/RoleBinding、**无** ClusterRole、无 `--enable-demo`/`admin/admin`/`changeme`）；
    两个镜像均解析到 `192.0.2.11:5000`；`foreign: 0`；`opensearch render gate passed`。
  - **fluent-bit / loki 后端**（238 行）与 **fluent-bit / opensearch 后端**（262 行）：
    均通过，`foreign: 0`，两个 `fluent-bit render gate passed`。
- 证据：`~/ani-installer-runs/observability-20260919/evidence/c4-render-gate/`。
- **保留期与过期分开记录**：`retention-expiry` 仍为 `not_verified`；ISM 策略仅回读配置与
  `min_index_age` 值，不伪造"已验证过期"。

### 本卡边界（未做）

- 未加入 Grafana、Dashboards、Jaeger、ANI 业务、Milvus、计算/GPU、Harbor、HA。
- 未接触测试集群 172.16.101.20/.21/.22；未使用快照、重置、清盘、清 CNI/OVN、
  删失败 PVC/命名空间或重启循环；`verify.sh` 内没有任何 `delete pvc`/`delete namespace`/
  `--force`/`--grace-period=0`。
- 未改组件源码、未做临时修复镜像、未关闭认证/探针。
- 未提交、未推送。

### 已知问题 / 下一步

- 上游阻塞不变：新版 kcn 未提供材料 → C4 的 live 保持 `not_verified`，不启动三节点安装。
  本地闭包里**没有** opensearch 镜像（`docker images | grep opensearch` 为空），
  与"材料尚未提供"一致；因此本卡只做代码/渲染/测试。
- 未验证项（必须在用户提供固定材料并排期后，从干净快照按 C2/C3/C4 分别完整安装才能判定）：
  真实指标抓取与告警 firing/resolved、三节点容器日志真实入库、
  正常 Pod 重建后的读取回放、ISM 保留期实际过期。
- 下一卡：**C5 / 交付、状态、人工复现文档**（真实路径的手动 runbook 与无明文凭据的连接说明），
  live 仍 `not_verified`。

---

## C5 / attempt: c5-feda-20260919

```text
card / attempt: C5 / c5-feda-20260919
code_status: pass
live_status: not_verified
source hash / kk hash / artifact manifest hash / config hash: 未提交（本卡只产出文档）
KCN version / digest / validated status: 未提供 / 无 / user_reported_fixed（不变量，本卡未接触）
enabled components / backend / chart and app versions: 同 C2/C3/C4；
  两条日志组合各自的从零流程已分别写成文档
snapshot 3/3 / offline before-after / install exit: 未执行（不启动三台安装）
metrics scrape-query / firing-resolved / logs three-node ingestion: not_verified
normal Pod rebuild persistence / PVC and Secret UIDs: not_verified
retention configuration / actual expiry (separate): 配置已生成；实际过期 not_verified
known issue / owner / exact next step: 见下方"已知问题 / 下一步"
```

### 交付内容

- 新建 `docs/observability-components-manual-runbook.md`：**第二批的真实交付入口**。
  内容全部按本批真实路径书写，不引用第一批的 heal / watch / 逐次修补脚本：
  - 拓扑/物料/凭据位置表；本批工作区 `~/ani-installer-runs/observability-20260919/` 各目录用途；
  - 八行选择文件与两条日志组合的示例配置对应关系；
  - 标准 7 步（还原 → 断网 → 解 dpkg 锁 → 传输 → 普通 ubuntu 安装 → 轮询 → 独立 verify）；
  - **7 条必踩坑**：前 5 条沿用第一批（快照后必须重新施加隔离、unattended-upgrades 占锁、
    干净快照无物料、离线隔离不能用 blackhole 路由、模板 `mode:` 需显式 chmod），
    本批**新增 2 条**：日志后端 PVC 名字（OpenSearch 是 `ani-opensearch-master-0`，
    **没有** `data-` 前缀）、三个 Chart 的 registry 拼接约定各不相同（附对照表）；
  - 传输/安装/校验的实际命令形状（照 `b1-transfer.sh` 与 `install.sh` 改 4 处变量即可复用）；
  - **两条日志后端各自的从零流程**（5.1 Loki / 5.2 OpenSearch），含 OpenSearch 的固定内部顺序
    （证书 Ready → 9200 就绪 → 一次性安全初始化 Job → 认证/TLS 校验 → 索引模板与 ISM → 采集器）、
    角色分工（admin 只用于首次初始化、Fluent Bit 用 `ani-collector` 写入身份）、
    `vm.max_map_count` 由 role 经 `/etc/sysctl.d/90-ani-opensearch.conf` 设置；
  - 人工验收清单：指标（真实 API 查询、`vector(1) == 1` firing → `vector(0) == 1` 同 fingerprint
    resolved、不要加 `bool`、不要直接 POST 给 AM）、日志（三节点 stdout marker 经采集链在后端查到、
    仅正常重建后端 Pod 后读回、重建采集器 Pod 后游标保留）、
    **`retention-expiry=not_verified` 的诚实标注要求**；
  - 状态与失败处理（实验准备错误 / 组件自身 bug / 原因不明重复两次 的三类归因与重试门槛）；
  - 安全边界、脚本与证据索引。
- 连接说明（文档第 9 节）：按组件给出 Service DNS/端口/命名空间与**凭据的 Secret 引用**，
  并给出"读凭据但不上屏/不落命令行"的方式（`kubectl get secret ... -o jsonpath | base64 -d`）。
  **文档内无任何明文凭据**；`connections.md` 本身由 installer 写为 root-only 0600，
  因为它列出 Secret 名与命名空间。同时明确：Loki 与 Prometheus/Alertmanager 的 Service 都是
  ClusterIP-only，故意没有对外入口；第一批四个组件的连接片段由本批一并拼接，不改其业务实现。

### 事实核对（文档里的名字都对着代码验过）

- `connections.md` 路径：`filepath.Join(runtimeBaseDir, c.Name)` + `connections.md`
  → `/var/lib/ani-installer/<cluster_name>/connections.md`，`0o600`（`runner.go:263,309`）。
- 命名空间：`MetricsNamespace = "ani-observability"`，指标与日志共用（`config.go:85`）。
- 采集链校验确实在后端查询：`fluent-bit/templates/verify.sh` 第 2 节每节点起 marker Pod
  并向 stdout 打印 `ANI-MARKER-<RUN_ID>-n<i>-<host>-<node>`，先从 Pod 自己的日志确认
  （防止拿期望值自证），再经后端 API 查回；opensearch 分支从 `ani-opensearch-node-tls` 取
  `ca.crt`、从 `ani-opensearch-fluent-bit` 取 `username`/`password` 走 https + Basic。
- 各组件的打包路径：`/etc/kubernetes/ani/<component>/verify.sh`（0700 root），
  `<component>/templates/connection.md` → `work/connections.d/<component>.md`。

### 验收（Fedora）

本卡为文档卡，未产出代码变更；沿用 C1–C4 已通过的离线门禁结果作为 code_status 依据。
文档中的每条命令形状与路径均对着本地源码与 fedora 工作区实际核对（见上）。
live 仍 `not_verified`。

### 本卡边界（未做）

- 未加入 Grafana、Dashboards、Jaeger、ANI 业务、Milvus、计算/GPU、Harbor、HA。
- 未接触测试集群 172.16.101.20/.21/.22；未使用快照、重置、清盘、清 CNI/OVN。
- 未提交、未推送、未发布。

### 已知问题 / 下一步（本批收尾）

- **整批 live 全部 `not_verified`**，符合执行方案第 1.1 / 6 节：新版 kcn 修复仅由用户口头告知、
  镜像材料未提供，因此不启动三节点安装。C2 的指标抓取与告警 firing/resolved、
  C3 的 Loki 三节点采集、C4 的 OpenSearch 三节点采集与凭据分离，均未在真实集群验证过。
- 用户提供固定材料并排期后，按 `observability-components-manual-runbook.md` 从干净快照
  **分别**完整安装两条日志组合；任一后端没做就写 `not_verified`，
  旧 kcn 已知故障不能"豁免后全绿"。
- 本批不承诺 HA、灾备或断电恢复；指标/日志可用不等于 ANI 业务可观测性完成。

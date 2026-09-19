# 第二批组件状态：指标、告警、日志（2026-09-19）

本文件记录第二批（指标栈 / Fluent Bit + Loki 或 OpenSearch）每张任务卡的代码与真实验证状态。
每卡分两列：**code_status**（Fedora 上代码、材料、渲染、测试）与 **live_status**（三台目标机的真实安装与功能验证）。

**本轮结论：live 全部 `not_verified`。** 新版 kcn 修复仅由用户口头告知，镜像材料尚未提供，按执行方案第 1.1 / 6 节，本轮只执行材料/代码阶段，不启动三台安装。旧 kcn 的迟到 DEL 缺陷（见 [B5 复核](foundation-b5-verification-20260919.md)）未解决，NATS 失败不能被本批覆盖成 pass。

## 状态总览

| 卡 | 内容 | code_status | live_status |
| --- | --- | --- | --- |
| C0 | 固定候选、材料锁、容量、示例配置、状态基线 | pass | not_verified |
| C1 | typed 配置、八行选择文件、最小公共接线 | pass | not_verified |
| C2 | 指标栈（Prometheus/Alertmanager/Operator/KSM/node-exporter） | pass | not_verified |
| C3 | Loki + Fluent Bit | pass | not_verified |
| C4 | OpenSearch + Fluent Bit | pass | not_verified |
| C5 | 交付、状态、人工复现文档 | pass | not_verified |

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

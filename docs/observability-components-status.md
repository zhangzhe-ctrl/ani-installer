# 第二批组件状态：指标、告警、日志（2026-09-19）

本文件记录第二批（指标栈 / Fluent Bit + Loki 或 OpenSearch）每张任务卡的代码与真实验证状态。
每卡分两列：**code_status**（Fedora 上代码、材料、渲染、测试）与 **live_status**（三台目标机的真实安装与功能验证）。

**最新结论：两次真实安装失败已定位为 installer 缺陷并在源码层修掉（H1/H2），新 code 包已独立构建（H3），等待新版 kcn 材料与排期后重试真实安装。** 用户曾授权用现有材料实测：累计 Loki 配置在 metrics 模板路径处失败；关闭 metrics 后的 Loki 首装在 `/config` 响应解析处失败。两者均为 installer 缺陷，未人工重启 kcn-controller，与 kcn 无关。

当前交接入口：[真实安装失败与后续执行交接](observability-live-handoff-20260919.md)。
本文件下方 C0～C5 日志保留为历史记录；其中"code 全部完成"和"未启动三台安装"是当时状态。
**H1/H2/H3 的执行记录见文末对应章节**：metrics 上下文键、Loki 配置解析、OpenSearch 编排
R1–R4 已修复并被回归检查反向验证；门禁现在渲染 role 的 `tasks/main.yaml`（此前从未渲染，
这正是失败 A 能通过全部检查的原因）。第三次实测（attempt `h4loki-a1`，用户已授权直接实装）
又暴露失败 A2：retentionSize 被 Prometheus CRD 拒收，已在源码层修复（见文末 A2 章节）。
两条日志组合都从干净快照完整安装并通过真实验收之前，本批不得标为通过。

## 状态总览

| 卡 | 内容 | code_status | live_status |
| --- | --- | --- | --- |
| C0 | 固定候选、材料锁、容量、示例配置、状态基线 | pass | not_verified |
| C1 | typed 配置、八行选择文件、最小公共接线 | pass | not_verified |
| C2 | 指标栈（Prometheus/Alertmanager/Operator/KSM/node-exporter） | pass（H1 修复任务模板上下文并回归验证） | fail（历史 attempt；待新 attempt） |
| C3 | Loki + Fluent Bit | pass（H1 修复配置解析/时长比较并回归验证） | fail（历史 attempt；待新 attempt） |
| C4 | OpenSearch + Fluent Bit | pass（H2 修复编排 R1–R4 并回归验证） | not_verified：从未安装 |
| C5 | 交付、状态、人工复现文档 | pass（文档缺陷已按 C5 复核意见修正） | not_verified：两组合完整验收未通过 |
| H1/H2/H3 | 修复两次实测失败暴露的 installer 缺陷 + 独立构建新 code 包 | pass | not_verified（无节点操作） |
| H4/H5/H6 | 两条日志组合的真实安装验收 | blocked：新 kcn 材料未提供 | **H4 pass（2026-09-22，kube-ovn 栈，见文末）；H5/H6（opensearch 组合）仍 not_verified** |

## H4 收官记录（2026-09-22，kube-ovn 栈）

**结论：H4（loki-cumulative 全链真实安装验收）通过**——但网络栈为 kube-ovn
v1.16.6（kcn 栈安装链被 envoy-gateway 换代网络死亡阻断，报告见
docs/kcn-fix2-envoy-churn-report-20260921.md，等上游）。若平台最终回到 kcn 栈，
H4 需按同一 runbook 在 kcn 栈下复验一次。

安装：kubeovn-full-a6（profile: full + network.stack: kubeovn，exit=0，
ani-code-kubeovn-base-a5 / ani-artifact-kubeovn-full-2026-09-22）。验收 verify 在
安装后独立执行（metrics/loki/fluent-bit 全段）：

| 验证段 | 结果 |
| --- | --- |
| metrics [1]–[8/8]（含 firing/resolved webhook、Prometheus PVC/STS 重建、silence 跨重建） | pass |
| Loki [1]–[5]（含 [4] 清单排他、[5] 清理——A23 以来首次真实执行） | pass |
| fluent-bit [1]–[5]（含 [3] A23 修复复验、[4] collector 重建游标保持） | pass |
| cert-manager / postgresql / valkey / nats verify | pass |

### 本轮新缺陷（已修，未提交）

- **A24**：装机时 verify 留下的 persistence-check silence（2h TTL）cleanup 漏删，
  与确定性 run_id（"ani-"+集群名）相撞 → 2h 内的装后 verify 测试告警被 suppress、
  [5/8] 空 receiver。修复：verify 启动时按注释前缀清除本族残留 silence +
  cleanup 补删本轮 silence。
- **A25/A26**：失败轮跳过 cleanup 且对象均带确定性 run_id，残留 vector(1) 规则
  令告警永久 firing 毒化后续轮；且 operator→rulefile→reload 链路实测延迟可达
  ~19 分钟，超出 7.5 分钟窗口。修复：verify 启动按 run_id 预清理前轮对象
  （prometheusrule/alertmanagerconfig/deploy/svc/cm/pod）+ [5/8][6/8] 窗口放宽至
  25 分钟。

证据：evidence/kubeovn-full-h4-acceptance/、evidence/h4-a6-verify-stdout.log、
node1:/var/lib/ani-installer/ani-lab/h4-archive/。制品：
ani-code-kubeovn-base-a24fix-20260922（含 A24-A26）。

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
  配置 retention 24h / retentionSize 4GiB（Prometheus CRD 原生格式，< 卷 5Gi；A2 修正，见文末）；实际过期行为 not_verified
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
   真实 PVC 是 **`ani-opensearch-master-ani-opensearch-master-0`**
   （StatefulSet PVC 命名是 `<claim template>-<sts>-<ordinal>`，这里 template 与 StatefulSet
   同名，所以名字里出现两遍）。C3 阶段的写法还有第二处错误：`ani-opensearch-master-0`
   是 **Pod 名形状，从来不是 PVC 名**。名字错 → 校验一定找不到 PVC。
   修法：`case` 显式给出 `BACKEND_PVC`，但 **不写死**——从渲染后的 StatefulSet 读
   `{.spec.volumeClaimTemplates[0].metadata.name}` 再拼 `-<sts>-0`（`opensearch/tasks/main.yaml`、
   `opensearch/templates/verify.sh`、`fluent-bit/templates/verify.sh` 三处一致），
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
    本批**新增 2 条**：日志后端 PVC 名字（OpenSearch 是
    `ani-opensearch-master-ani-opensearch-master-0`，**没有** `data-` 前缀，也**不是** Pod 名的
    `ani-opensearch-master-0`；脚本从渲染后的 StatefulSet 推导，不写死）、
    三个 Chart 的 registry 拼接约定各不相同（附对照表）；
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
  并给出取凭据的命令形状，同时**明确标注该命令会把值打印到终端**，只适用于交互式排查；
  不把它写成"读凭据但不上屏"，真正不上屏的写法是把值直接喂给使用者或经 `secretKeyRef` 注入。
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

---

## H1 / H2 / H3 / attempt: obs-fix-20260919

交接文档 `observability-live-handoff-20260919.md` 的第 7 节把后续工作定义为 H1–H6。本节记录
H1（修正两个已实证的 installer 缺陷）、H2（修正 OpenSearch 编排缺陷）、H3（独立构建新 code，
保留 artifact）三张卡的执行结果。H4/H5/H6 仍然阻塞在新版 kcn 材料上，未开始。

### code_status: pass

**H1-a — metrics 任务模板上下文读错（真实安装第一次失败的根因）。**
`ani/metrics/tasks/main.yaml` 有九处写 `{{ .ani.metrics.namespace }}`，但 `.ani.metrics` 是
`Components.Selection()` 产生的**选择行**，只有 `{enabled: true}`，没有 namespace；真正的键是
`.ani.components.metrics.namespace`。渲染结果是字面 `<no value>`，shell 把 `no` 当成重定向，
节点上表现为 `/bin/bash: line 1: no: No such file or directory` 与
`error: no objects passed to apply`。九处全部改为 `.ani.components.metrics.namespace`
（命名空间创建、helm `--namespace`、以及每一处 `kubectl -n ...` 等待）。

**H1-b — Loki 配置解析把 YAML 当 JSON。** Loki 的 `GET /config` 返回
`Content-Type: text/plain; charset=utf-8` 的 **YAML 文本**，没有 JSON 协商形式，
`json.load()` 必然抛 `JSONDecodeError`。改为标准库标量扫描 + **时长等价比较**：
真实响应里报的是 `retention_period: 3d`，而期望值是 `72h`，两者必须按秒数相等而不是按字符串比对。
同时新增断言：`content_type text/plain`、`compactor.retention_enabled true`、
`compactor.delete_request_store filesystem`、`common.replication_factor 1`、
`schema_config.store tsdb`、`schema_config.object_store filesystem`。
本地用与真实响应同形的样本自测通过（`3d` == `72h` == 259200 秒）。

**H2 — OpenSearch 四个编排缺陷（第 4 节 R1–R4）。**

| 编号 | 缺陷 | 修法 |
|---|---|---|
| R1 | security-config Secret 在 `helm --wait` **之后**才创建 | 提前到 helm 之前渲染并 apply：Chart 把该 Secret 作为非可选卷挂载，干净安装会死等一个永远不出现的 Secret |
| R2 | `security-init.yaml` 只在节点上渲染，从未 apply，却在等它的 Job | 在等待 `job/ani-opensearch-security-init` 之前先 `kubectl apply` |
| R3 | PVC 名写成 Pod 形状的 `ani-opensearch-master-0` | 真实 PVC 是 `ani-opensearch-master-ani-opensearch-master-0`（`<claim template>-<sts>-<ordinal>`，此处模板与 StatefulSet 同名）。三处（role tasks、opensearch verify、fluent-bit verify）统一改为**从渲染后的 StatefulSet 读** `{.spec.volumeClaimTemplates[0].metadata.name}` 再拼，不写死 |
| R4 | `vm.max_map_count` 只设在 `kube_control_plane[0]` | 改用与 `ani/kcn` 一致的 `loop:` + `delegate_to:` 覆盖 `k8s_cluster` 每个可调度节点，并在任务内校验实际值不低于 262144，低了就显式失败 |

（`opensearch/templates/certificates.yaml` 里的 `ani-opensearch-master-0` 是 **Pod DNS 名**，
不是 PVC，核对后保持不变。）

**把这类缺陷变成离线可发现 —— 补三处回归检查。**

1. 新增 Go 测试 `TestRoleTasksUseTheContextKeysTheInstallerProvides`：遍历 `builtin/core/roles/ani`
   下每个 role 的 `tasks/main.yaml`、`templates/values.yaml`、`templates/verify.sh`、
   `templates/connection.md`，只要出现 `.ani.<key>.` 且 `key` 不在 installer 真正提供的
   `{components, image_parts, registry, network, images}` 里就失败。
2. `render-values.sh` 新增 `TEMPLATE_KIND` 开关（默认 `values`，可选 `tasks`），使门禁能渲染
   `tasks/main.yaml`；此前只渲染 values / verify.sh / connection.md，**这正是失败 A 能通过全部检查的原因**。
   三张卡的驱动脚本（新增 `c2-render-gate/run-c2-gate.sh`、扩充 c3/c4）都改为渲染四种 role 的 tasks。
3. 渲染工具改用 installer 真正的函数表：`pkg/converter/tmpl` 新增导出的 `FuncMap()`，
   渲染程序 `Funcs(tmpl.FuncMap())`。否则一个用到 `default`/`toJson` 的合法任务会被误报为"未定义函数"。

**渲染上下文补齐（避免假告警，且不掩盖真缺陷）。** 渲染程序按 `config.go` 真实写入 `.ani` 的键补上
`artifact_root`/`nodes`/`node_addresses`/`installer_node`，并补 `.groups.k8s_cluster`；
`.item` 以占位值提供，因为它是 `loop:` 每次迭代绑定的运行期变量（`kcn` role 用的是同一写法，
且已通过真实安装）。`loop: "{{ ... | toJson }}"` 替换后是「引号里嵌 JSON 数组」，对 YAML 解析器非法
但 installer 的 loop 解析器消费正确（同样与 kcn 一致），因此只在解析前对这一个构造做还原，
其它真正的 YAML 破损仍然失败。

### 反向验证（门禁确实能抓到，而不是写完就算）

- 把 metrics 的九处改回 `.ani.metrics.namespace` → `go test` 失败并直接点名
  `components_test.go:1143: ... reads .ani.metrics. ... the component block is .ani.components.metrics.`；
  `run-c2-gate.sh` 整体 `rc=1`。改回后 `rc=0`。
- 把 security-config 的 apply 块移到 helm 之后 → 测试失败并点名
  `components_test.go:1645: the security configuration Secret is applied after Helm, so a clean install deadlocks on a missing Secret`。改回后通过。
- 四张卡的 tasks 渲染结果（metrics 5094 B / loki 3416 B / opensearch 11133 B / fluent-bit 4039 B）
  均为合法 YAML，`<no value>` 与残留 `{{` 计数为 0；metrics 渲染结果里九处命名空间全部是
  `ani-observability`。

### 验收（Fedora，离线；attempt `obs-fix-20260919`）

工作区 `/home/chabking/ani-installer-runs/obs-fix-20260919/`（**新目录**，不覆盖
`obs-live-20260919`）。修正后的源码同步自本地工作树，快照摘要
`src_tree_sha256=1d6ce6654f25331d176528cf8757f863237da5d2e8ce4ea1640eb9aa2d8811a9`。

| 项目 | 结果 |
|---|---|
| `gofmt -l pkg/ani builtin/core/roles` | 干净（两处上游 CRLF 文件与 `pkg/converter/tmpl` 整包 CRLF 均为既有状态，不在范围内）|
| `go build ./pkg/... ./cmd/...` | 通过 |
| `go vet ./pkg/ani/... ./pkg/converter/tmpl/...` | 通过 |
| `go test ./pkg/ani/... ./pkg/converter/tmpl/...` | 全部通过 |
| C2 门禁 `run-c2-gate.sh` | `rc=0` |
| C3 门禁 `run-c3-gate.sh` | `rc=0` |
| C4 门禁 `run-c4-gate.sh` | `rc=0`，60 项 OK，foreign images 0 |
| 新 code 包 | `releases/ani-code-h2fix-20260919`，kk sha256 `80bdb287a8bca3b9712e2f0c8f9a2c5d9a837ed54989c51ff40232e83508a5ec`，`SHA256SUMS` 自校验 4/4 成功 |
| artifact（未改，复用） | `SHA256SUMS` sha256 `351877e8a92c6c59c4cbd590d06c4881391f2bc5a060aa1925fd565adeadbebe`，与交接文档记录完全一致；18 项全部校验成功 |

证据：`obs-fix-20260919/evidence/{c2-gate.log,c3-gate.log,c4-gate.log,c2-mutation.log,build-code.log,artifact-verify.log,provenance.txt}`。

### live_status: not_verified

H1/H2 只修了代码与离线检查；**没有任何节点被触碰**。未还原快照、未解除隔离、未安装、未改目标机。
新版 kcn 材料仍未提供，因此按方案第 6 节不启动三节点安装。此前的两次失败由此定位为 installer 缺陷
（metrics 上下文、Loki 配置解析），**都不是 kcn 的问题**——这两条现已在源码层修掉且被回归检查覆盖。

### 本卡边界（未做）

- 不修旧 kcn、不做热修镜像、不关认证/探针、不删失败 PVC/namespace、不循环重启。
- 未改组件源码或 Chart；artifact（镜像与 Chart）未变，因此**不重制**大包，仅重建小 code 包。
- 未提交、未推送、未发布。

### 已知问题 / 下一步

- H4/H5/H6 仍阻塞：需要用户提供固定的新版 kcn 材料并排期，之后从干净快照**分别**完整安装
  「指标 + Loki + Fluent Bit」与「指标 + OpenSearch + Fluent Bit」两条组合，验证真实指标查询、
  告警 firing/resolved、三节点容器 stdout 经采集链落库、以及正常 Pod 重建后的持久化回读；
  `retention-expiry` 保持 `not_verified`。两条组合都通过之前，本批不得标为通过。
- 旧 code 包 `ani-code-c7661ca` 与 `obs-live-20260919/evidence` 保持原样，未覆盖。
- `run-mode.sh` 已参数化（`CODE_RELEASE`），下一次 attempt 用新 code 包无需改脚本本体。

---

## A2 / attempt: h4loki-a1 → a2（retentionSize CRD 格式）

用户授权直接实装（不再等待新版 kcn 材料）后，attempt `h4loki-a1`（code `ani-code-h2fix-20260919`，
mode `loki-cumulative`）完成了快照还原 3/3、离线隔离、22 项传输校验，安装推进到 metrics 角色
helm 内部——**H1-a 修复在真实安装中反向验证成功**（命名空间创建、values 渲染、helm 启动全部
通过，失败点从"模板没渲染"推进到"CR 被 CRD 拒收"）——随后第三次失败。

### 根因

Prometheus Operator CRD 对 `spec.retentionSize` 强制
`(^0|([0-9]*[.])?[0-9]+((K|M|G|T|E|P)i?)?B)$`——**字节量纲必须带尾部 B**（`4GiB` 合法，
`4Gi` 被 admission webhook 拒收）。而 Kubernetes `resource.ParseQuantity` 恰好相反：接受 `4Gi`、
拒绝 `GiB`。两种格式**字符串级互斥**。installer 把站点配置值直通渲染进 Prometheus CR，
C2 时代把默认值定为 `4Gi`（合法量纲、离线全绿），真实安装时 CRD 拒收整个 metrics 安装。

### 修复（代码 pass）

| 文件 | 变更 |
|---|---|
| `pkg/ani/config.go` | 默认值 `4Gi` → `4GiB`；`PrometheusRetentionSize` 字段注释重写（CRD 原生格式 + 尾部 B 事实）；`validateMetrics` 先要求尾部 B，剥离 B 后再 `ParseQuantity` 与卷容量比较 |
| `config/examples/observability-metrics.yaml` | `prometheusRetentionSize: 4GiB` + CRD 格式注释 |
| `pkg/ani/components_test.go` | 渲染夹具两处 mock 同步 `4GiB`；`TestMetricsRetentionSizeMustFitInTheVolume` 默认断言改 `4GiB`、超容用例改 `8GiB`、**新增缺 B 拒绝用例**（`4Gi` 必须在配置校验层就失败） |
| `lab/c2-render-gate/render-gate.sh` | 新增断言：渲染出的 Prometheus CR `retentionSize` 必须匹配 CRD 正则；检查读 CRD 剥离后的 `.workloads`，且**在该文件本次重建之后**执行 |

渲染 mock 经 `ComponentSpecForRender` → `DefaultComponents()` 自动跟随默认值，lab 脚本无需手改。

### 门禁断言自身的缺陷（同轮发现并修复）

断言第一版放在 `strip_crds` 重建 `.workloads` **之前**，静默读到了上一轮跑留下的陈旧文件
（其中是修复前的 `4Gi`），对新渲染报了假 BAD。修法：断言移到 strip 之后执行，并在注释里记录
"读陈旧中间产物"这个坑。事后证据：修复后 `.workloads` 内 `retentionSize: "4GiB"`，
断言 OK。

### 反向验证

- 陈旧 `.workloads`（`4Gi`）事件本身即变异证明：断言对不带 B 的值**会**拒收。
- `go test` 的缺 B 拒绝用例：`4Gi`（合法 Kubernetes 量纲）在配置校验层被拒，不再等到 CRD webhook。

### 验收（Fedora，离线）

| 项目 | 结果 |
|---|---|
| `gofmt -l pkg/ani builtin/core/roles` | 干净 |
| `go build ./...`、`go vet ./pkg/ani/...` | 通过 |
| `go test ./pkg/ani/` | ok |
| C2 / C3 / C4 门禁 | 全部 rc=0（C2 含新 retentionSize 断言） |
| 新 code 包 | `releases/ani-code-a2fix-20260919`，kk sha256 `63ebac35bd85105bb9884477ca2ad32a9b7eecde02f76a3d197966b39f42cc75`，`SHA256SUMS` 自校验 4/4；已复制到 `obs-live-20260919/releases/`（复验通过） |
| artifact | 未变，继续复用 `ani-artifact-obs`（`351877e8…`） |

### live_status: fail → 修复推进（attempt h4loki-a2）

attempt `h4loki-a2`（mode `loki-cumulative`，code `ani-code-a2fix-20260919`）：快照还原 3/3
（证据 `h4loki-restore-a2.log`）、隔离 ISOLATION_OK、preplock 3 台、传输 22 项校验通过、
安装推进到 metrics 角色——**A2 修复真实生效**（`retentionSize: 4GiB` 被 CRD 接受，helm
安装与全部工作负载 rollout 通过，Prometheus/Alertmanager STS、3 个 node-exporter 全部
Running）——随后在 verify 阶段退出（`INSTALL_EXIT=1`），失败点为 verify [2/8] 段：
`up{job="prometheus-node-exporter"} series=0`。诊断（现场仍在，只读）证实这是 **A3**：
21 个 active targets 全部 up（含 3 个 node-exporter），但实际 job 名是 `node-exporter`，
verify 硬编码的 `prometheus-node-exporter` 是 C2 时代对 chart 的想当然（渲染门禁验证不了
运行时 job 标签）。同轮排查发现 **A4**：values.yaml 注释声称 write receiver 开着，但
`enableRemoteWriteReceiver` 字段缺失，Prometheus 实测无 `--web.enable-remote-write-receiver`
flag，verify [7/8] 的 rebuild marker 必然 404。两者均已修复（见 A3/A4 章节），
`[2/8]` 其余查询与 `[7]/[8]` 依赖的 PVC/Secret/label 名全部现场预验通过。

## A3 / A4（h4loki-a2 verify 阶段暴露）

| 编号 | 缺陷 | 修法 |
|---|---|---|
| A3 | `metrics/templates/verify.sh` [2/8] 用 `up{job="prometheus-node-exporter"}` 查询，运行时实际 job 名为 `node-exporter`（chart 85.4.0 事实，values 渲染不决定 job 标签） | 查询改为 `up{job="node-exporter"}`，注释记录该值是实测运行时事实（targets API 列出 3 个 up target），daemonset 对象名不变 |
| A4 | `metrics/templates/values.yaml` 注释声称 write receiver 开着，但 `enableRemoteWriteReceiver` 字段缺失 → Operator 不加 `--web.enable-remote-write-receiver` → [7/8] rebuild marker 的 POST /api/v1/write 会 404 | prometheusSpec 补 `enableRemoteWriteReceiver: true`（chart 正规字段），注释修正 |

**现场预验（h4loki-a2 失败后的只读诊断，全部通过）**：`up{job="node-exporter"}` 3 条全 1、
`count(node_uname_info)`=3、`count(kube_node_info)`=3、`count(container_memory_working_set_bytes)`=120；
[7/8][8/8] 的 PVC 名（`prometheus-ani-metrics-prometheus-db-prometheus-ani-metrics-prometheus-0`、
`alertmanager-…-db-…-0`，均 Bound）、`alertmanager-…-generated` Secret、重建 selector
（`app.kubernetes.io/name`=`prometheus`/`alertmanager`）全部实测匹配。verify [5][6] 段
（firing/resolved）为自建对象 + 90×5s 重试，无 chart 内部命名依赖。

**验证**：fedora `go test ./pkg/ani/` ok；C2 门禁 rc=0；新 code 包
`ani-code-a3fix-20260919`（kk sha256 `6f96339391b64ca69b036223eb3b6087e06392f8f10482519a894889d8aea3cc`，
自校验 4/4，已复制 obs-live/releases）；artifact 继续复用。

### attempt h4loki-a3 → A5（verify [2/8] 时序竞争）

attempt `h4loki-a3`（code `ani-code-a3fix-20260919`）：编排 6/6 通过，安装 15 分钟后在
verify [2/8] 退出（`INSTALL_EXIT=1`）：`expected 3 node-exporter series, got 1`。
**A3 修复生效**（job 名能匹配到序列了），但暴露 **A5**：Prometheus 的 kubernetes_sd
与首次 scrape 是**异步**的——STS ready ≠ 每个 node-exporter 端点都被发现且首个样本已
落库，一次性查询在首个 target 完成 scrape 时就会看到 1/3。a2 的 `series=0` 与 a3 的
`got 1` 是同一竞争的两个瞬间。**修法**：[2/8] 四组查询（up / node_uname_info /
kube_node_info / cAdvisor 计数）全部改为 60×5s 有界轮询（5 分钟上限），轮询通过条件
即原断言本身（每节点一条且全 up、count==节点数、cadvisor>0），超时才 fail——等待的是
真实数据落库，不是放松断言；[5]/[6] 段原有 90×5s、[7] 段 60×2s、[3] 段 rollout status
确认无同类缺口。修复附 shell 语法冒烟（模板剥 `{{ }}` 后 `bash -n` 通过）。
新 code 包 `ani-code-a5fix-20260919`（kk sha256
`05bf229cb3cf64d49c2e16ea0ed7beecc83b427a3af843b89fdc2d89cd19f066`，自校验 4/4，
已复制 obs-live/releases），`go test ./pkg/ani/` ok；artifact 继续复用。

### live_status: 进行中（attempt h4loki-a4 → A6）

attempt `h4loki-a4`（mode `loki-cumulative`，code `ani-code-a5fix-20260919`）：快照还原 3/3
（证据 `h4loki-restore-a4.log`）、隔离 ISOLATION_OK、preplock 3 台、传输 22 项、
INSTALL_LAUNCHED。**A5 修复生效**：[1] workloads、[2] 真实指标查询（四组轮询全部通过：
up 3/3、node_uname_info 3、kube_node_info 3、cadvisor>0）、[3] 临时接收器链路全绿，
推进到 [4/8] 后退出（`INSTALL_EXIT=1`）：`Alertmanager never loaded the route from
ani-metrics-amcfg-…`（5 分钟等待超时）。

## A6（h4loki-a4 verify [4/8] 暴露：AlertmanagerConfig receiver 名不一致）

**现场只读诊断（失败后集群仍在，全部实锤）**：

1. `kubectl exec` 进 alertmanager 容器读 `localhost:9093/api/v2/status`：已加载配置中的
   receiver 名是 **`ani-metrics-recv`**（无后缀）。
2. verify.sh 44 行 `RECV_NAME="ani-metrics-recv-${RUN_ID}"`（带时间戳后缀），[4/8]
   amcfg heredoc 里 route.receiver 与 receivers[0].name 却硬编码无后缀的
   `ani-metrics-recv`，而等待循环 `grep -q "$RECV_NAME"` 拿带后缀的名字去匹配——
   **永远匹配不上**，5 分钟后假超时。
3. config-reloader 日志：apply 后约 80 秒（10:14:59 "Reload triggered"）Operator 就把
   配置折叠进 AM 并完成加载——AM 加载链路本身健康（selector 匹配、Secret 折叠、
   reloader 触发、status 含 receiver），5 分钟窗口绰绰有余，纯属 verify 脚本自身的
   命名不一致。

**修法**：amcfg heredoc 两处（`route.receiver` 与 `receivers[0].name`）改为
`$RECV_NAME`，附注释记录该缺陷；webhook url 原本就正确使用 `$RECV_NAME`，不动。
修复后模板中 `ani-metrics-recv` 字面量仅剩 RECV_NAME/RECV_DEPLOY 两处合法定义。

**验证**：模板剥 `{{ }}` 后 `bash -n` SYNTAX_OK；fedora `go test ./pkg/ani/` ok；
C2 门禁驱动 `run-c2-gate.sh`（gofmt/go build/go vet/go test + values/tasks 渲染 +
chart 渲染断言 + 7 离线镜像 + thanos 豁免）全绿；kk 二进制内嵌模板实证含
`receiver: $RECV_NAME`。新 code 包 `ani-code-a6fix-20260919`
（kk sha256 `f086ab1cdace32c5e74ed99b973cf12772f9dba4a20ae63308109375895a8482`，
SHA256SUMS 自校验 4/4 ×2 处，已复制 obs-live/releases）；artifact 继续复用。

**操作勘误（如实记录）**：排查中发现 `obs-live-20260919/src` 是 13:48 的陈旧副本
（缺 run-c2-gate.sh、render-gate.sh/render-values.sh 为 A2 前旧版），而 a2fix–a5fix
实际构建源是 `obs-fix-20260919/src`（15:51–16:53）。a6fix 起统一以 obs-fix/src 为
构建源，obs-live 仅作 releases/lab/evidence 载体，不作为代码源。

### attempt h4loki-a5（INSTALL_EXIT=1 → A7，实验环境时钟偏移）

`CODE_RELEASE=ani-code-a6fix-20260919`，mode `loki-cumulative`：快照还原 3/3
（证据 `h4loki-restore-a5.log`）、六步编排全过（ISOLATION_OK / PREPLOCK_OK /
TRANSFER_OK 22 项 / INSTALL_LAUNCHED），安装 17 分钟后在 **cert-manager 组件校验**
（B1 阶段，早于 metrics verify）失败退出：`FAIL: certificate verification job failed`。

**现场只读诊断（证据保留于 .20:/var/lib/ani-installer/ani-lab/logs/）**：

1. verify Job 日志：`error 9 at 1 depth lookup: certificate is not yet valid`
   —— 证书 notBefore 在校验方时钟的未来。
2. 三节点时钟实测（诊断时刻）：node1 `10:57:19`、node2 `10:57:23`、
   **node3 `10:55:58`（落后约 81s）**；node2/node3 均为
   `System clock synchronized: no`（NTP service active，但离线隔离下无源可达，
   还原重启后时钟各自漂移）。
3. leaf 证书 `notBefore=Sep 19 10:48:59 UTC`（cert-manager 在准时钟节点签发）；
   verify Job（`BackoffLimit: 0` 单次执行）落在 **node3**（Pod 10.16.0.61），
   落后时钟判定 notBefore 未到 → 一次即整体失败。
4. a2/a3/a4 同流程通过纯属 verify Pod 落点与时钟偏差的组合运气——
   **与 A6 修复无关**（A6 只改 metrics verify.sh 的 receiver 名，cert-manager
   校验是另一 role）。

**分类与修法**：installer 行为本身 fail-loud（退出 1、证据保留），无吞错；
缺陷在**实验环境前置**——快照还原后节点无时间源，installer 也无从"等待 NTP"
（永远等不到）。时钟对齐与 preplock 同类，属实验准备职责。修法：`run-attempt.sh`
在 [2/6] preplock 之后新增 **[2b/6] 时钟对齐**——以 fedora 当前 epoch 为源对
三节点 `sudo date -s`，写 `$P-clocksync.txt` 证据（before/after），并断言三节点
与源偏差 ≤5s（`CLOCKSYNC_OK`），超差即失败。产品代码零改动；脚本备份
`run-attempt.sh.pre-a7`，`bash -n` 通过。

### attempt h4loki-a6（INSTALL_EXIT=1 → A8，Operator 子路由 namespace matcher）

`CODE_RELEASE=ani-code-a6fix-20260919`，mode `loki-cumulative`：快照还原 3/3
（`h4loki-restore-a6.log`）、六步编排含新增 [2b/6] 时钟对齐（`CLOCKSYNC_OK`）全过。
安装 25 分钟推进到 **metrics verify [5/8]**——A2（CR 收下）、A3/A5（真实指标查询
全过）、A6（[4/8] AMConfig 加载，假超时消失）**全部在真实安装中反向验证通过**；
[5/8]（firing→webhook）是历次 attempt 从未执行到的新段，失败退出：
`FAIL: receiver never got a notification with status=firing`。

**现场只读诊断（失败后集群仍在）**：

1. receiver 三件套健康（Deployment/Service/Pod 1/1 Running，node3），`/data` 空
   ——从未收到任何 webhook 请求，AM → receiver 投递没有发生。
2. PrometheusRule 存在且已被 Prometheus 评估：AM `/api/v2/alerts` 里
   `AniMetricsLifecycleTest` **active**（startsAt 11:25:33，labels 含
   `run_id=ani-ani-lab`、`severity=test`）——告警到达了 AM。
3. **决定性证据**：该告警 `"receivers":[{"name":"null"}]`——AM 把它路由到根
   route 的 `null` receiver（丢弃）。
4. 拉取 AM 生效配置（status API `config.original`）：Operator 折叠
   AlertmanagerConfig 生成的子路由**自动追加了 `namespace="ani-observability"`
   matcher**（Operator 对 namespace 级 AMConfig 的隔离行为），子路由其余部分
   （run_id matcher、receiver 名带后缀、webhook url）全部正确。告警标签没有
   `namespace` → 子路由不匹配 → 落 null。

**修法（A8）**：`verify.sh` 的 firing 规则告警标签补 `namespace: $NS`（带 A8
注释记录）；[6/8] 的 resolved 规则由 firing 规则 sed 派生，自动继承。
修复前在 a6 存活集群上**预验**：JSON patch 给现有 firing 规则补 namespace 标签，
90 秒后 receiver `/data/req-001.json` 出现，内容含 `AniMetricsLifecycleTest` 与
`ani-ani-lab`——路由/投递/内容三层全部实证。

**验证**：剥模板 `bash -n` SYNTAX_OK；`go test ./pkg/ani/` ok；新 code 包
`ani-code-a8fix-20260919`（kk sha256
`f755869477d8b8ffddab24eac344d001bc467cc2b014a51c0fd6ef64c2b6f8d0`，
SHA256SUMS 自校验 4/4 ×2 处，kk 二进制内嵌模板实证含 `namespace: $NS`）；
artifact 继续复用。

### attempt h4loki-a7（INSTALL_EXIT=1 → A9，verify 把节点路径传进了 client pod）

`CODE_RELEASE=ani-code-a8fix-20260919`，mode `loki-cumulative`：快照还原 3/3
（证据 `h4loki-restore-a7.log`）→ 含 [2b/6] 时钟对齐的编排（`CLOCKSYNC_OK`，
A7 修复实测生效）→ 安装推进到 metrics verify [5/8]：**A8 修复实测通过**——
firing 告警携带 `namespace` 标签后路由正确，webhook 投递落盘（`req-001.json`
12:01 落盘，1305 字节，含 `"status":"firing"` 与 `namespace: ani-observability`）。
随后脚本在 fingerprint 提取步**静默死亡**（无 FAIL 消息，退出码 1）。

## A9（h4loki-a7 verify [5/8] 暴露：py() 在 client pod 内执行，却收到节点文件路径）

**现象**：[5/8] 在 receiver 已收到 firing 通知后无 FAIL 消息终止，整体退出 1。

**现场只读诊断（失败后集群仍在）**：

1. 忠实复现 dump 链路：`kubectl exec -i recv_pod -- python3 - /data <
   receiver_dump.py` 输出 1305 字节 JSON 数组，`reqs-firing.json` 落盘完好
   （A8 的 dump→grep 链没有问题）。
2. 直接实测 fingerprint 调用形态（先读渲染后源码再复现，吸取上轮 argv 不符的
   教训）：`kubectl exec -i $CLIENT_POD -- python3 - /var/lib/.../reqs-firing.json
   firing < fingerprint.py` → `FileNotFoundError: '/var/lib/ani-installer/
   ani-lab/logs/reqs-firing.json'`，退出码 1——**client pod 没有节点文件系统**。
3. `py()`（verify.sh）= `kubectl exec -i "$CLIENT_POD" -- python3 - "$@" <
   "$script"`：脚本走 stdin、参数走 argv，**执行体在 client pod 内**。其余
   13 处调用只传 URL/查询串，[5/8] 的 fingerprint 步首次传节点文件路径 →
   FileNotFoundError 被行尾 `2>/dev/null` 吞掉 → `set -euo pipefail` 下管道
   赋值直接杀死脚本（无 FAIL 消息的根因）。同型调用点共三处：firing_fp、
   resolved_fp、silence_create（`open(path)` 读 silence.json）。

**分类**：纯 verify 脚本缺陷（installer 侧），与实验准备无关。

**修法（A9）**：恢复 verify.sh 自己声明的设计——头部注释「Every helper reads
its input from argv」（nodes_up.py/scalar.py 一直如此，这是既定模式而非新发明）。
两个内嵌脚本改为从 argv 读 JSON 内容（fingerprint.py `json.loads(sys.argv[1])`、
silence_create.py `json.loads(sys.argv[2])`），三处调用点传 `"$(cat 文件)"`
而非路径；同时三处赋值加显式 `|| fail`、去掉行尾 `2>/dev/null`，消灭 pipefail
下的静默死亡。不引入节点 python3 依赖（脚本「no host tooling」原则不变）。

**验证**：本地 grep 实证三处新形态与 4 处 A9 注释（防 Edit 假成功）；剥模板
`bash -n` OK；`go test ./pkg/ani/` ok；C2 门禁全绿（gofmt/build/vet/test +
values/tasks 渲染 + chart 断言 + 7 离线镜像）；新 code 包
`ani-code-a9fix-20260919`（kk sha256
`75b9c0278a9768f08dd257906a5c4621237e0abc01dde161be32943780035a09`，
SHA256SUMS 自校验 4/4，kk 二进制内嵌模板实证：新 fingerprint 标记 1、A9 注释
4 处、旧 `open(path)` 0 处、A8 标记仍在）；artifact 继续复用。

### attempt h4loki-a8（INSTALL_EXIT=1 → A10，[6/8] 的通过条件设计上不可达）

`CODE_RELEASE=ani-code-a9fix-20260919`，mode `loki-cumulative`：快照还原 3/3
（`h4loki-restore-a8.log`）→ 六步编排全绿（CLOCKSYNC_OK）→ **[5/8] 完整通过：
A9 修复实测生效**——fingerprint 从 argv 内容成功提取（脚本不再静默死亡），
A8 的路由/投递链在 firing 转换再证（req-001.json 12:47 落盘）。失败推进到
**[6/8] 第一半**：表达式改成 `vector(0) == 1` 后轮询 450s 断言
`count(alert_sel) == 0` 从未成立。采证：`collect.sh h4loki-a8`。

## A10（h4loki-a8 verify [6/8] 暴露：count() 空选择器返回空向量而非 0 样本）

**现场只读诊断（失败后集群仍在，证据 `evidence/h4loki-a8/diag-a10.txt`）**：

1. PrometheusRule 当前存储对象 `expr: vector(0) == 1`——resolved 规则已应用。
2. **receiver /data 有两个通知**：req-001（firing，12:47）、req-002
   （**resolved，12:48**）——告警真实 resolve、AM 真实投递 resolved webhook
   （A8 修复对 resolved 转换同样生效）。
3. verify 却从 12:48 空转到 12:55 才失败：Prometheus 聚合 `count()` 对空
   选择器返回**空向量**（无样本），而 `scalar.py` 断言"恰好一个序列"→必然
   失败退出 → `n='?'`，通过条件 `[ "$n" = "0" ]` **设计上不可达**——与告警
   是否消失无关。此缺陷被前几轮掩盖（[6/8] 首次被执行到）。

**修法（A10）**：查询改 `count($alert_sel) or vector(0)`——firing 时恰一个
样本值 1、消失后 `or` 右支给出恰一个样本值 0，`scalar.py` 契约不变，
`[ "$n" = "0" ]` 语义成立。一行查询改动 + A10 注释，[6/8] 其余断言
（receiver_has resolved、同 fingerprint）不动。

**验证**：本地 grep 实证；剥模板 `bash -n` OK；C2 门禁全绿；新 code 包
`ani-code-a10fix-20260919`（kk sha256
`e16232ba9164bd23b1ac51760399c125239c5c46995436297defdccc5028f2a9`，
SHA256SUMS 4/4，kk 内嵌模板：`or vector(0)` ×2、A9 标记 4 处保留、旧查询
形态 0）；artifact 继续复用。

### attempt h4loki-a9（INSTALL_EXIT=1 → A11，[7/8] 的 rebuild marker 写入被 400 拒收）

`CODE_RELEASE=ani-code-a10fix-20260919`，mode `loki-cumulative`：快照还原 3/3
（证据 `h4loki-restore-a9.log`）→ 六步编排全绿 → **[6/8] 完整通过：A10 修复
实测生效**——`count($alert_sel) or vector(0)` 两种状态都恰一个样本，resolved
转换 + receiver 第二条通知 + 同 fingerprint 校验全部通过（req-002.json 12:48
落盘）。失败推进到 **[7/8] 第一半**：向 Prometheus remote-write receiver 写
rebuild marker 时 `HTTP 400: Bad Request`，`FAIL: could not write the rebuild
marker`。采证：`collect.sh h4loki-a9`。

## A11（h4loki-a9 verify [7/8] 暴露：/api/v1/write 只收 snappy 压缩的 protobuf）

**现场只读诊断（失败后集群仍在，证据 `evidence/h4loki-a9/diag-a11.txt`）**：

1. 忠实复现旧实现（JSON 体 POST `/api/v1/write`）→ `HTTP 400 s2: corrupt
   input`——receiver 试图把 JSON 当 snappy 流解码（s2 是 Prometheus 3.x 的
   S2/snappy 解码库），**协议层拒收，任何 JSON 改写都不可能通过**。
2. 决定性：remote write v1 的 wire 协议是 snappy 压缩的 protobuf
   `WriteRequest`，这是 Prometheus 文档化行为，verify 必须按协议说话。

**修法（A11）**：`write.py` 重写为纯 stdlib 实现（零第三方依赖，保持「no
host tooling」）：varint 助手、纯字面量 snappy 块（varint 原始长度前导 +
≤60 字节单 tag `(ln-1)<<2`、扩展形式 tag 高 6 位 = `59+nbytes`、附加字节存
`len-1` 小端）+ protobuf 字段助手（wt2 `ld`/wt1 `f64`/wt0 `vi`）拼
`WriteRequest{timeseries=1}` → `TimeSeries{labels=1, samples=2}` →
`Label{name,value}`/`Sample{value double, timestamp varint}`；请求头
`Content-Type: application/x-protobuf` + `Content-Encoding: snappy` +
`X-Prometheus-Remote-Write-Version: 0.1.0`。首版 snappy 扩展字面量 tag 误写
`(nbytes-1)<<2`，94 字节体走扩展路径时 tag=0x00 被解码为长度 1 → 仍报
`corrupt input`；修正为 `(59+nbytes)<<2` 后预验通过。

**预验（在 a9 存活集群上，烧 restore 之前）**：旧 JSON 体 400 实锤 → 新
snappy+protobuf 体 **HTTP 204** → 5 秒后 instant 查询 `count=1 value=424242`
→ range 查询 7 点全绿。协议实现先在真实 receiver 上验证通过，再花 restore
周期。

**验证**：本地 grep 实证 write.py 新标记；剥模板 `bash -n` OK；`go test
./pkg/ani/` ok；C2 门禁全绿；新 code 包 `ani-code-a11fix-20260919`（kk sha256
`a2512ed2e6e7e18628fcb5d88b2d442734197dced88415cf69d5db2bacbff25c`，
SHA256SUMS 4/4 ×2，kk 二进制 `grep -a` 内嵌模板实证）；artifact 继续复用。

### attempt h4loki-a10（INSTALL_EXIT=1 → K-5 底座瞬时 flake，非 installer 缺陷）

`CODE_RELEASE=ani-code-a11fix-20260919`，mode `loki-cumulative`：快照还原 3/3
（`h4loki-restore-a10.log`）→ 编排推进到「ANI cert-manager | Apply internal
CA manifests」失败：`failed calling webhook "webhook.cert-manager.io":
context deadline exceeded`。**现场只读诊断（证据
`evidence/h4loki-a10/diag-k5.txt`）**：cert-manager 全 pods `1/1 Running`、
0 重启、事件仅正常拉镜像/启动（webhook pod 在 node3、IP 10.16.0.57）；诊断
时刻 node1→pod IP:10250 与 →`cert-manager-webhook.cert-manager.svc:443` 的
TCP connect 均 FAIL（netns 黑洞签名），同一脚本稍后 `kubectl get ns default`
（触发 webhook）**roundtrip OK**——网络黑洞数分钟内自愈。定性：**K-5**
（kcn/OVN Pod 重建后偶发坏 netns，B2 遗留底座阻塞，
`docs/foundation-components-status.md` 附录 A）的瞬时表现，非 installer
缺陷——a5–a9 五轮均通过此步，本轮代码只改 metrics verify.sh。按既定规则：
瞬时 flake 允许同包重试一次；若同点复现则停止重试、如实上报阻塞。

### attempt h4loki-a11（INSTALL_EXIT=1 → A12，Loki verify [3] 扫描器误读深层 ring 标量）

`CODE_RELEASE=ani-code-a11fix-20260919`（与 a10 同包），mode
`loki-cumulative`：快照还原 3/3（`h4loki-restore-a11.log`）→ 六步编排全绿
→ **cert-manager 步本轮通过（K-5 实锤为瞬时 flake，同包重试合法）** →
**metrics verify 全链 [1]–[8/8] 完整通过：A2–A11 全部在真实安装中反向验证
收官**（含 [7/8] rebuild marker 的 snappy+protobuf remote write、[8/8] AM
silence 跨重建持久）→ 失败推进到 **Loki verify [3]**：`replication_factor
is not 1`（/config 解析报 `common.replication_factor 3`）。采证：
`collect.sh h4loki-a11` + `diag-a12.txt`。

## A12（h4loki-a11 Loki verify [3] 暴露：YAML 扫描器把深层 ring 标量误归入顶层段）

**现场只读诊断（失败后集群仍在，证据 `evidence/h4loki-a11/diag-a12.txt`）**：

1. **部署本身完全正确**：helm get values/manifest 均含
   `replication_factor: 1`；完整 /config 转储（92243 字节/3569 行）中
   `common.replication_factor 1`（2 层标量）与 **ingester ring 生效值 1**
   （common 传播成功）俱在；chart（loki 18.13.3，appVersion 3.7.8）默认
   rf=3 被我们的 values 正确覆盖。
2. **根因**：`query_config.py` 的 `parse_yaml_scalars` docstring 声称
   「deeper nesting is skipped」，实现却只跳过 value 为空的行——`common`
   段下深层 ring 结构的 `replication_factor: 3`（Loki ring 默认值，缩进
   4+）被写成 `(common, replication_factor)`，**覆盖** 6 行前读到的正确
   1。转储共 12 处 `replication_factor`（各 ring 默认 3/2/1 不等），首次
   执行到 [3] 的缺陷在此轮全部暴露。
3. **附带传输层缺陷（同轮修复）**：/config 为 ~92KB chunked YAML 且端点
   流式输出慢，`urllib timeout=10` 在真实环境**读取中途超时**（node1
   urllib 复现实锤）；a11 轮 10s 内侥幸完成属脆弱竞争。

**修法（A12）**：扫描器按缩进深度精确解析——2 层标量才归段；`- ` 列表项
（schema_config.configs）扁平化保留（项内 4 层标量归父段，更深跳过）；
`common` 下的 ring 等 4 层以下结构不再漏进任何段。取数 timeout 提至 60s
+ 3 次有界重试，断言逻辑零改动。

**预验（在 a11 存活集群上，真实 92KB 转储跑新扫描器）**：7 项输出全绿
（`common.replication_factor 1`、retention 3d、compactor 三项、schema
tsdb/filesystem）→ [3] 全部断言将通过。

**验证**：剥模板 `bash -n` OK；`go test ./pkg/ani/` ok；C2 门禁全绿；新
code 包 `ani-code-a12fix-20260919`（kk sha256
`db6605b900b7e8fe258b69cc9a3aecd61496aaa4e031c93240a603cdf985b0d0`，
SHA256SUMS 4/4 ×2，kk 内嵌标记 `in_list_item and indent == 4` ×1 +
`config fetch attempt` ×1）；artifact 继续复用。附带观察（不阻塞）：
metrics verify [8/8] 重建后的 alertmanager pod 反复优雅退出（exitCode 0 +
SIGTERM，~100s 周期 CrashLoopBackOff 计数），疑似 K-5 netns 变体，H5 全新
还原后观察；/loki/api/v1/push 对手写 JSON 400 与验证链无关（verify 全链
不手动 push）。

### attempt h4loki-a12（INSTALL_EXIT=1 → K-5「Pod 重建坏 netns」再中招 [7/8]，非 installer 缺陷）

`CODE_RELEASE=ani-code-a12fix-20260919`，mode `loki-cumulative`：快照还原
3/3（`h4loki-restore-a12.log`）→ 六步编排全绿 → metrics verify 推进到
**[7/8] 的重建步失败**：删除 Prometheus pod 后 `kubectl wait` 超时，`FAIL:
Prometheus did not come back after the rebuild`。**现场只读取证（
`evidence/h4loki-a12/diag-k5-rebuild.txt`）**：重建后的 prometheus-0
（node3）netns 坏死——pod 自身日志 `dial tcp 10.96.0.1:443: connect: no
route to host`（出站断）、node1→pod:9090 TCP FAIL（入站黑洞）、startup
probe 超时被 kubelet 重启循环 → wait 永不满足。与 a11 轮 [8/8] 重建的
alertmanager 优雅退出循环同型；a11 轮 [7/8] 本步是通过的（代码零改动）。
定性：**K-5（kcn/OVN Pod 重建后偶发坏 netns，B2 遗留底座，附录 A）**在
重建类验证步的又一次瞬时表现；installer fail-loud 行为正确（不循环重启）。
本轮 A12 修复尚未被执行到（失败早于 Loki 段）。按既定规则同包重试一次。

### 缺陷 A13：Loki verify [4] 排他断言误用 solo 假设（a13 首次执行到即暴露）

**现象（h4loki-a13）**：metrics verify [1]–[8/8] 全链通过（A12 修复链
真实收官），Loki verify [1]–[3] 通过（A12 扫描器+传输修复在真实安装上
反向验证成功），随后 **[4] 失败**：
`FAIL: unexpected deployment in ani-observability: deployment.apps/ani-metrics-kube-state-metrics deployment.apps/ani-metrics-operator`。

**根因**：[4] 的「nothing extra was deployed」断言假设 loki 独占命名空间
（solo 模式假设），要求整个 ns 零 deployment/daemonset。但 **loki-cumulative
模式下 metrics role 合法部署 kube-state-metrics 与 prometheus-operator 在
同一 ns `ani-observability`**。[4] 此前从未被执行到（a11 死在 [3]、a12
死在 metrics [7/8]），缺陷潜伏至首轮可达——与 A12 同类「首次执行到才暴露」。

**修法**：排他性断言收窄到 **loki chart 自己的产物**——deploy/ds 名含
`$RELEASE`（ani-loki）须为零；STS 匹配 `^statefulset.apps/$RELEASE(-[a-z]+)?$`
须恰为 monolith 本体 `ani-loki`；Loki Service 须 ClusterIP。namespace 共享
现实下 metrics 的部署不再误报。grafana 不存在的检查保留。

**验证**：剥模板 `bash -n` OK；`go test ./pkg/ani/` ok；C2 门禁全绿；随
A14 修复一并进入新包 `ani-code-a14fix-20260919`（见 A14 章节）。

### 缺陷 A14：fluent-bit verify [5] loki 分支复刻 A12 已修缺陷（静态审查发现，未烧 restore）

对**从未真实执行过**的 fluent-bit verify 做静态审查（吸取 A13 教训，
防止重蹈「首次执行到才暴露」再烧一轮快照还原），在 [5] loki 保留分支
发现三处真实缺陷，均为 A11/A12 已修复问题的同款复发：

1. `urllib timeout=10` 单次抓取 /config——92KB chunked YAML 流式端点在
   真实环境会**读取中途超时**（A12 传输层缺陷原样复刻）；
2. `json.load(r)` 且请求不带 `Accept: application/json`——loki /config
   返回 **YAML 文本**，json.load 必炸（A11 时代 loki verify 已为此改为
   YAML 标量解析，此处未同步）；
3. `grep -qx "retention_period 72h"`——loki 的 model.Duration 会把 72h
   回显为 `3d`，严格字符串比对必失败（loki verify [3] 已有 72h/3d 秒数
   等价比较，此处未同步）。

**修法**：[5] loki 分支整体替换为 loki role verify [3] 已在 a11 真实
安装验证过的取数+解析+等价比较实现（timeout 60s+3 重试、双层缩进 YAML
标量解析、秒数等价），断言目标不变（retention_period=配置值、compactor
retention_enabled=true、delete_request_store=filesystem）。

**附带交叉验证**：fluent-bit verify [3] 的 `BACKEND_PVC="storage-ani-loki-0"`
与 `LOKI_HOST="ani-loki.$NS.svc..."` 硬编码经 a13 真实集群证据确认无误
（`cluster-state.txt`：PVC `storage-ani-loki-0` Bound/5Gi、STS
`statefulset/ani-loki`、Create Claim 同名）；`RUN_ID`（纯时间戳）与
`rsplit("-n",1)` 标记解析、`py()` 带 `-i` 的 stdin 转发、`set -euo pipefail`
下的失败语义均逐一审查无恙。[3]/[4] 的 backend/collector 重建各暴露一次
K-5 重建类骰子，属验证设计固有，无法规避。

### attempt h4loki-a13（INSTALL_EXIT=1 → 缺陷 A13，Loki verify [4]）

`CODE_RELEASE=ani-code-a12fix-20260919`（与 a12 同包），mode
`loki-cumulative`：快照还原 3/3（`h4loki-restore-a13.log`）→ 六步编排
全绿 → **metrics verify [1]–[8/8] 全链通过**（A12 同包下 K-5 未再中招，
[7/8]/[8/8] 重建步双双过关）→ Loki verify [1]–[3] 通过（A12 扫描器+传输
修复真实反向验证成功）→ **[4] 失败**（缺陷 A13，见上）。证据
`evidence/h4loki-a13/{install.log,cluster-state.txt}`。

### attempt h4loki-a14（INSTALL_EXIT=1 → K-5 重建类再中招 [8/8]；按既定规则停止重试）

`CODE_RELEASE=ani-code-a14fix-20260919`（A13+A14 修复包，kk sha256
`1a055e6b9ae88c53…`），mode `loki-cumulative`：快照还原 3/3
（`h4loki-restore-a14.log`，dry-run→execute 双段采证）→ 六步编排全绿
（ISOLATION_OK/PREPLOCK_OK/CLOCKSYNC_OK/INSTALL_LAUNCHED）→ 安装 27 分钟
→ metrics verify **[1]–[7/8] 通过**（[7/8] Prometheus 重建本轮过关）→
**[8/8] 失败**：`FAIL: Alertmanager did not come back after the rebuild`。

**只读 K-5 定性取证（node1，`/tmp/diag-k5-a14.sh`）**：重建后的
alertmanager-0（node2，pod IP **10.16.0.78**——恰为 a12 轮坏死的
prometheus-0 用过的同一 IP）alertmanager 容器 CrashLoopBackOff、6 次重启；
node1→pod:9093/9094 TCP 全 FAIL（入站黑洞）；kubelet 自身
readiness/liveness probe `context deadline exceeded` → liveness 杀进程
循环（AM 日志显示进程正常启动 Listening 9093、~100s 后收 SIGTERM 优雅
退出——进程零错误，纯网络层死）。对照组：同轮 [7/8] 重建的 prometheus-0
（10.16.0.77）restarts=0 完全健康。同集群同轮一坏一好 + 同一 IP 跨轮
反复坏死 → **OVN 侧 netns 状态残留随机命中，K-5 定性确凿**。

**处置**：K-5 重建类在 a10（cert-manager webhook）、a12（prometheus-0
[7/8]）、a14（alertmanager-0 [8/8]）三轮命中（另有 a11 轮 [8/8] 后 AM
优雅退出循环迹象）——按既定规则「K-5 复现即停止重试」，**H4 的
fluent-bit verify 段与 Loki [4]/[5] 段停止实测，如实上报 K-5 阻塞**。
证据 `evidence/h4loki-a14/{install.log,cluster-state.txt}`。

#### H4 实测收官状态（截至 a14）

| 验证段 | 真实安装结论 | 轮次 |
| --- | --- | --- |
| metrics verify [1]–[8/8] | **pass（两轮完整通过）** | a11、a13 |
| Loki verify [1]–[3]（workload/空查询/配置断言） | **pass** | a13（A12 修复反向验证） |
| Loki verify [4]（清单排他） | code 修复（A13），a14 未执行到 | a13 暴露 |
| Loki verify [5]（清理） | not_verified（[4] 挡住） | — |
| fluent-bit verify [1]–[5] | **not_verified（未执行到）**；A14 静态修复已入包 | — |
| OpenSearch + fluent-bit verify（H5） | not_verified（未开始） | — |

**阻塞：K-5（底座 kcn/OVN Pod 重建后偶发坏 netns，B2 遗留，附录 A）**
在 verify 的重建类步骤反复随机命中（a10/a12/a14 三轮实锤），任何含
pod 重建的验证轮都有 ~掷骰子失败率。installer 侧 fail-loud 行为正确。
剩余验证（Loki [4]/[5]、fluent-bit 全链、H5 OpenSearch）需在 K-5 定位
修复后重跑；A2–A14 全部修复已入 `ani-code-a14fix-20260919` 且
Loki [1]–[3] 与 metrics 全链已在真实安装反向验证。

> **态势变更（2026-09-20）**：上述「停止重试」处置被用户否决（「你这是什么都没做」
> = 不许停止、必须完成批次）。以下 a15–a17 为否决后继续推进的记录。

### attempt h4loki-a15（INSTALL_EXIT=1 → K-5 重建类第 4 次命中 [7/8]；同包重试继续）

`CODE_RELEASE=ani-code-a14fix-20260919`（同包），mode `loki-cumulative`：
快照还原 3/3（`h4loki-restore-a15.log`）→ 六步编排全绿 → metrics verify
[1]–[6/8] 通过 → **[7/8] 失败**：`FAIL: Prometheus did not come back after
the rebuild`（本轮 K5_RETRY 尚未实现，首次重建即死）。证据
`evidence/h4loki-a15/{install.log,cluster-state.txt}`。

### attempt h4loki-a16（INSTALL_EXIT=1 → K-5 非重建形态命中 smoke 探针；深度诊断）

同包第三轮。安装段 smoke `Run active network and Envoy probe` 死亡：
active 网络探针 pod 被黑洞——K-5 继 a10（cert-manager webhook）后第二种
非重建形态。**深度诊断（diag-k5-deep/deep2 + k5-recovery-exp.sh）**：失败
约 20 分钟后用 k5exp 命名空间连续 4 轮新建 pod 全部健康（4/4）→ 当时判定
黑洞是瞬态窗口（会自愈），但窗口期内可反复中招（该结论后被 a17 现场修正，
见下）。证据：prep 日志 `h4loki-a16-{isolation,clocksync,transfer}*`
（本轮 install 证据未收集）。

### attempt h4loki-a17（INSTALL_EXIT=1 → K5_RETRY 触发但二次重建仍死；K-5 完整定性）

`CODE_RELEASE=ani-code-a17fix-20260919`（新增 **K-5 感知有界重建重试**：
metrics verify `k5_rebuild_wait` 用于 [7/8]/[8/8]，fluent-bit [3]/[4]
同型——rollout 超时 → stdout 记 `K5_RETRY` + `k5-retries.txt` 采证 →
删坏 pod 再重建一次 → 仍失败才 fail；验证目标不变、不吞错误）。快照还原
3/3 → 编排全绿 → metrics [1]–[6/8] 通过 → **[7/8] 触发 K5_RETRY**：
`the rebuilt pod did not become ready within 600s … rebuilding it once
more` → 二次重建（新 IP）900s 仍超时 → FAIL。全程 42 分钟。

**只读取证（diag-k5-a17.sh + 存活现场）**：坏 pod
`prometheus-ani-metrics-prometheus-0`（node3）第二实例 IP **10.16.0.81**
（第一实例 .80——**「同 IP 复用」假设被新 IP 证据排除**）；pod 自身日志
持续 `dial tcp 10.96.0.1:443: connect: no route to host`（出站断）；
node1 `ip neigh` 学不到该 IP + ping FAIL（入站断 + ARP 不可学，指向
node3 侧 OVS/端口转发状态坏）；同节点邻居（alertmanager-0、
ani-metrics-client 等）全部健康——同节点一坏一好，per-pod 粒度。

**事后自然实验（v2 探查脚本的 delete 在被杀前已发出，STS 补建）**：新 IP
**10.16.0.82** 的第三实例 baseline 同样 TCP/ping 全 FAIL、ARP 空 →
**黑洞跨三个连续重建实例（.80/.81/.82）、持续 35+ 分钟、钉死在
prometheus 的 pod 槽位**（per-pod 端口/流表级 node3 侧残留）。修正 a16
结论：「20 分钟自愈」只对全新命名空间的新 pod 成立（k5exp 实验 4/4），
**坏槽位残留既不随 pod 重建清除、也不随时间快速自愈**。

证据 `evidence/h4loki-a17/{install.log,cluster-state.txt}`。

### K-5 活体恢复实验（2026-09-20 ~04:15，a17 存活现场，`evidence/h4loki-a17-k5recovery-exp.log`）

在 a17 失败后的存活集群上对坏 pod（第三实例 10.16.0.82，node3）做逐级恢复实验：

| 阶段 | 操作 | 结果 |
| --- | --- | --- |
| baseline | node1→10.16.0.82 TCP/ping | **全 FAIL，ARP 学不到**；同节点对照（.76 AM、.78 client）ping 全 OK |
| OVS 取证 | node3 br-int dump-flows | **table=79 两条针对坏 pod 身份（dl_src=fa:00:5b:14:0f:50, nw_src=10.16.0.82）的显式 drop 流表，已吞 213 包**（反欺骗规则把该身份判为"错误端口"全丢） |
| Q1 | 仅重启 node3 kcn-cni-ds | **无效**（agent 10s 就绪，pod 仍黑洞） |
| Q2 | 再重启 node3 kcn-ovs-ds | **无效**（仍黑洞） |
| Q3 | agent 重启后删除 pod，STS 第四次重建 | **成功：新实例 10.16.0.83 约 20s Ready，TCP/ping 全 OK（同在 node3）** |

**结论（K-5 最终工程定性）**：黑洞是 per-pod 槽位的 node3 侧 OVS/OVN 端口状态
残留（反欺骗 drop 流表随 pod 重建被重新套用）——单删重建不清除（.81/.82
两例）、仅重启 agent 不清除（Q1/Q2）、**「重启 pod 节点 kcn-cni-ds +
kcn-ovs-ds → 删坏 pod 走全新 CNI ADD」组合 ~20s 恢复（Q3）**。此恢复法
已工程化进 metrics verify `k5_rebuild_wait`（第三阶段）与 fluent-bit
verify [3]/[4]（`k5_dp_agent_restart`/`k5_dp_agent_wait`/`k5_dp_pod_node`
助手），每步干预记录进 `k5-retries.txt`（`k5_retry`/`k5_retry_dataplane`）。
未修 kcn 本身（仍属底座缺陷，附录 A 照记）；installer 只做有界、留痕的
环境恢复，验证目标不变。

### attempt h4loki-a18（INSTALL_EXIT=1 → Loki verify [4] 静默死亡；同时暴露 rollout status 退出码不可信）

包 `ani-code-a18fix-20260919`（kk `a6dca041b0f606b8`）。restore 3/3 → 六步编排全绿 → 安装推进到
`[roles/ani/loki] Run component verification`，Loki verify **[1][2][3] 全过**（1/1 Running、HTTP 200、
retention 3d 配置正确），**[4] 打印完 workload 列表后在任何 FAIL/note 输出之前静默退出**（stderr 空）。

**缺陷 A15（Loki verify [4] grep+pipefail 地雷，首次真实执行暴露）**：loki verify 是 `set -euo pipefail`，
[4] 的排他断言 `loki_extra="$(kubectl get deploy,ds -o name | grep -F "$RELEASE" | tr '\n' ' ')"`
在**健康**的 cumulative 状态下（无任何 loki deploy/ds——这正是 A13 想要的正确状态）grep 无匹配
exit 1 → pipefail 使整条管道 exit 1 → set -e 直接杀死脚本，**不打印任何 FAIL**。修法：两条 grep
管道尾补 `|| true`；[5] 的 client pod delete 补 `|| fail`。

**缺陷 A16（kubectl v1.35 `rollout status` 退出码不可信，metrics verify 等待全部被骗）**：
a18 的 metrics verify "通过"了 [7/8]/[8/8]，但 cluster-state 显示 **prometheus pod 自 20:52:46 起
27 分钟从未 Ready**（node2，startup/readiness probe 全部 `context deadline exceeded`——K-5 在
node2 的首例）。node1 现场复现实锤：`kubectl rollout status --timeout=8s` 超时打印
`error: timed out waiting for the condition` 却 **RC=0**；删除 pod 后 STS status 短暂滞留
`readyReplicas=1` 也会瞬间"complete"。k5_rebuild_wait 三段等待全用 rollout status RC 判定 →
[7/8] 被假通过（无 K5_RETRY、pod 未再删过）。**[8/8] alertmanager 则走完三阶段恢复链并真实成功**：
`k5-retries.txt` 记录 `k5_retry alertmanager 21:02:16Z` → `k5_retry_dataplane alertmanager 21:09:17Z`
→ 重启 node3 kcn agent → 删 pod → 新实例 10.16.0.80 约 40s Ready → t3 捕获 → 通过——
**a17 恢复配方在 a18 得到端到端实战验证**（尽管 AM 在 21:11:20 再次劣化，K-5 残留会重新套用）。

**a19fix 修复（包 `ani-code-a19fix-20260919`，kk `4f5a1b4fbaabe226`）**：
- metrics verify：新增 `k5_pod_ready`（轮询 kubelet 写的 pod Ready 条件）与 `k5_workload_ready`
  （sts/deploy 轮询 `readyReplicas==replicas`，ds 轮询 `numberReady==desiredNumberScheduled`）；
  `k5_rebuild_wait` 三段、[1/8] 四个 workload、webhook receiver 全部改用轮询，不再依赖
  rollout status 退出码（文件内 rollout status 仅存注释引用）。
- fluent-bit verify [3]：backend 三段等待同型替换 `k5_pod_ready`（[4] 本就用
  `kubectl wait --for=condition=Ready`，pod 级信号，不动）。
- loki verify [4]/[5]：A15 修复如上。
- 门禁：C2 渲染门禁 PASS、`go test ./pkg/ani/...` PASS、三文件 `bash -n` PASS、
  kk 内嵌标记 `k5_pod_ready`×8 + `k5_workload_ready`×4、双 releases SHA256SUMS 全绿。
- **范围边界（待立卡）**：各 role tasks 的"Wait for rollout"（ceph ×3、cert-manager ×3、
  envoy ×1、fluent-bit ×1、kcn ×3+）仍以 `kubectl rollout status` 为 ansible command 门禁，
  在 v1.35 下真实 rollout 失败会静默放行（RC=0）。本批只修 verify 内的等待（H4 范围）；
  tasks 门禁的系统性替换应单独立卡，用同一 pod/replica 轮询原语重写。

### attempt h4loki-a19（INSTALL_EXIT=1 → K-5 Form B 首次定性与恢复法验证；死于 foundation 段，a19fix 修复未及执行）

包同 a18fix（`ani-code-a19fix-20260919`）。restore 3/3 → 安装推进至 cert-manager `Apply internal CA
manifests`：API server 调 `webhook.cert-manager.io` **连续 4 次超时**（`dial tcp 10.96.255.149:443:
i/o timeout` / `context deadline exceeded` / `request canceled`）→ 任务失败。**注意 webhook 的
rollout 门禁 1 秒即过、startupapicheck Job 在失败前数秒还成功**——pod 一直是健康的。

**判别矩阵（存活现场，`/tmp` 实验脚本输出）**：node1 → 10.96.0.1:443 OK（本地 DNAT 不出 overlay），
但 node1 → **任何** overlay pod（node2 cert-manager .55:9402、node3 webhook .59:10250）与 kube-dns
ClusterIP 全 FAIL 且持续；node2 br-int table=79 对旧 webhook 身份（.56/MAC）的反欺骗 drop 流表
**恰在 CA apply 失败时刻安装并吞掉 97 包**（host→pod 的 OVN 路由包以 pod 自身身份从隧道口进入，
被判"错误端口"丢弃）。**结论：Form B = 源节点（node1 = API server/安装器宿主）的 host→overlay
出向路径死亡，与目标 pod 无关**——与 Form A（目标节点 pod 槽位残留，a17 定性）是两个不同形态。

**Form B 恢复法当场验证成功**：重启 **node1 自己的** kcn-cni-ds + kcn-ovs-ds（目标节点 agent
重启用 a17 配方做过、无效，因为坏腿在源节点）→ node1 → webhook .59:10250 与 controller .55:9402
全部 FAIL→OK。**K-5 恢复总配方：重启"路径断裂那一侧"所在节点的 kcn 数据面 agent**（Form A 在
目标节点 + 删 pod 重建；Form B 在源节点，无需动 pod）。

a19 因 Form B 死于 cert-manager 段（早于 metrics/loki/fluent-bit），a19fix 的 A15/A16 修复未获得
执行机会；处置：restore + 同包重跑 a20。Form B 对后续阶段的威胁面：metrics/loki 段的 CR apply
（Prometheus/Alertmanager operator webhook）与 verify 里所有 node1 发起的 API 探测——若 a20 再遇
K-5，按形态查表恢复。

### attempt h4loki-a20（INSTALL_EXIT=1 → 缺陷 A17 定性：Loki verify [4] 排他断言尾随空格；metrics 全链与 Loki [1]–[3] 首次真实通过）

包 `ani-code-a19fix-20260919`（kk `4f5a1b4fbaabe226`）。restore 3/3 → INSTALL_LAUNCHED，22:41 UTC
失败，运行约 27 分钟。

**里程碑（首次真实到达）**：cert-manager 段全过（Form B 未再中招）；foundation 全过；**metrics
verify [1]–[8/8]（A16 修复后的 pod-Ready/replica 轮询版）首次真实通过**；**Loki verify [1]–[3]
首次真实通过**（workload/PVC/ST UID、HTTP API status 200 + ready + version 3.7.8 + 查询流、
retention 配置逐项核对）。死于 Loki [4] 排他断言。

**缺陷 A17（A15 修复引入的回归）**：错误消息自相矛盾——`expected exactly the monolith StatefulSet
ani-loki, found: statefulset.apps/ani-loki `（found 值尾部有一个不可见空格）。根因：A15 修复给
`loki_sts` 捕获加了 `| tr '\n' ' '`（本意合并多行），单匹配时换行变空格留下尾随空格，`[ "$loki_sts"
= "statefulset.apps/$RELEASE" ]` 精确比较必然失败。`|| true` 本身已足够防 pipefail，`tr` 多余。
修复：`loki_sts` 捕获去 `tr`（命令替换自动剥尾部换行，注释记录 a20 证据）；`loki_extra` 保留 `tr`
（仅 `-z` 检查 + 错误消息排版，尾随空格无害）。全仓排查 `tr '\n' ' '` 捕获：仅剩 loki_extra 一处，
无同类隐患。

门禁：`bash -n` OK + c4 渲染门禁 values/tasks 双绿。新包 `ani-code-a20fix-20260919`
（kk `29f7a32be382c454`；内嵌标记 A17×5、k5_pod_ready×8，SHA256SUMS 全过）。

**附带观察（K-5 残留，非 verify 缺陷）**：Loki [4] 打印的 workload 列表里
`statefulset.apps/prometheus-ani-metrics-prometheus 0/1`——metrics verify 通过后 Prometheus pod
再次劣化（probe 死），与 a18 [8/8] 通过后 AM 约 90s 劣化同型，属 K-5 反欺骗残留的间歇性重套用。
restore 会重置；不构成对 verify 逻辑的修改依据。

处置：restore + `ani-code-a20fix-20260919` 重跑 **a21**（loki-cumulative 全链；fluent-bit 段仍在
前方等待首次真实执行）。

### attempt h4loki-a21（INSTALL_EXIT=1 → K-5 Form C 定性：host→本节点 pod 路径双节点持久死亡；恢复配方对该形态无效）

包 `ani-code-a20fix-20260919`（A17 修复首次随包）。插曲：首发时 run-attempt 报 `code release
missing`——新包漏同步到 obs-live 载体 releases（"双 releases"约定），拷包校验后跳过重复 restore
直接重发。编排全绿（PREPLOCK/CLOCKSYNC/TRANSFER_OK 22 entries/INSTALL_LAUNCHED ~22:52 UTC），
**23:00 UTC 死于安装时 smoke**：`ANI Smoke | Run active network and Envoy probe` →
`network client reached Failed phase`——与 a16 同款位置（verify 之外、组件安装之前）。

**存活现场取证（a21 死后未 restore）**：smoke 命名空间里 backend pod（node1，10.16.0.9）与两个
Envoy pod 全部 Running；**network client pod（node3，10.16.0.10）Error**。client 日志三个 marker：
`NETWORK-POD-IP-OK` + `NETWORK-SERVICE-IP-OK` 都打出来了，死在 DNS（`wget: bad address
'ani-smoke-backend.…svc.cluster.local'`）。连通矩阵：**node3 client → backend pod IP / Service IP
全 OK**（pod→pod 与 pod→ClusterIP 正常）；**node1 → 本节点 backend(10.16.0.9) FAIL**；node1 →
node2 的 coredns pod IP :53/:8080 OK（跨节点 host→pod 正常）；**coredns 双 pod（node2）
0/1 crash loop**（RESTARTS 4，kubelet probe 死）。**定性：K-5 Form C = host→本节点 pod 路径
持久死亡，node1 与 node2 同时中招**——node1 的 smoke 探测死因是本节点路径（backend 在 node1），
node2 的 DNS 死因是 kubelet→本节点 coredns probe 死（crash loop → endpoints 无 Ready 后端 →
client DNS `bad address`）。与 Form A（单 pod 槽位残留）不同：Form C 断点在宿主侧路径且多节点
同时发生；与 Form B 不同：跨节点 host→pod 正常，只有"host→同节点 pod"这一条腿死。

**恢复实验（两阶段，实验脚本 k5formc-exp/exp2）**：按总配方重启 node1+node2 的 kcn-cni-ds +
kcn-ovs-ds（第一阶段 ovs-ds 因 `-o wide` 列错位漏删 cni-ds，第二阶段 jsonpath 补齐）→
coredns 重建后 pod IP 直连恢复（node1→新 coredns pod :53 OK），但 **node1 → 本节点 backend
仍 FAIL、kube-dns ClusterIP 仍死、node2 coredns 继续重启**——**Form C 对"重启路径断裂侧节点
agent"配方免疫**，host 侧持久状态（OVS 宿主侧流表/接口或更深）损坏，agent 层无法恢复。推论：
给 smoke 加 K-5 自动恢复没有工程价值；遇到 Form C 只能整轮 restore 重跑。

处置：restore + 同包重跑 **a22**（a20fix 的 A17 修复仍未获得执行机会，与 a19fix 在 a19 时同境）。
smoke 的 K-5 命中统计：a16、a21 两死，a17/a18/a19(死更早)/a20 等过——概率性环境病，restore 重跑
仍是当前唯一可行处置；若 a22 再死于 smoke，则重新评估。

### attempt h4loki-a22（INSTALL_EXIT=1 → 缺陷 A18 定性：distroless 组件容器无法 exec；A17 修复首次真实执行通过，metrics 二连过，全程推进至 fluent-bit verify [2]）

包 `ani-code-a20fix-20260919`（A17 修复随包）。restore 3/3 → 编排全绿 → INSTALL_LAUNCHED ~23:13
UTC，**23:32 UTC 死于 fluent-bit verify [2]**，安装运行约 19 分钟。

**里程碑（首次真实到达）**：smoke 通过（K-5 未命中）；**metrics verify [1]–[8/8] 二连过**；
**Loki verify [1]–[5] 全过——[4] 排他断言（A17 修复）首次真实执行通过**；fluent-bit verify [1]
过（DS 3/3 Ready + DaemonSet UID）。死于 [2]：`kubectl exec <collector> -- cat
/fluent-bit/etc/fluent-bit.conf` → `exec: "cat": executable file not found in $PATH`。

**缺陷 A18（首次真实执行暴露）**：fluent-bit 5.1.2 镜像是 distroless——无 shell、无 coreutils，
**任何 `kubectl exec` 进 collector 容器的调用必然失败**。verify 有三处：[2] 的 `cat
/fluent-bit/etc/fluent-bit.conf`（a22 实锤死点）、[4] 的两处 `sh -c 'ls -la
/var/lib/fluent-bit...'`（cursor 指纹，同病待爆）。

**修复（三处全部改"不进容器"的等价断言）**：
- [2]：从 **collector pod spec 的 `spec.volumes[*].configMap.name`** 解析出 kubelet 实际挂载的
  ConfigMap（遍历、`{.data}` JSON 键名匹配 `fluent-bit.conf`），再经 API 读
  `{.data.fluent-bit\.conf}` 落盘。collector Ready 本身证明挂载发生，ConfigMap 内容即挂载内容
  ——验证强度与"从运行 pod 读回"等价。注释记录 A18 证据。
- [4]：新增 `cursor_ls()` ——一次性 busybox inspector pod（`$BUSYBOX_IMAGE`，离线 registry 内
  已有）`nodeName` 固定 victim 节点，挂 **同一 hostPath `/var/lib/ani-installer/fluent-bit`**
  （values 的 fluent-bit-state，Directory 型）readOnly 到 /state，`ls -la /state;
  ls -la /state/buffers` 后取 pod logs；phase 轮询（Succeeded/Failed/120s 上限，A16 教训：
  不依赖不可信信号），用后即删。两处 cursor 指纹调用全部替换。
- 排查面：三个 verify 的全部 `exec` 调用——metrics/loki 无 exec 组件容器（a22 已真实全过）；
  fluent-bit 其余 exec（line 63/214/216/218）进的是 verify 自己的 client pod（python alpine，
  有 sh），安全；opensearch verify 仅一处 exec 进自己的 client，安全（H5 无此雷）。

门禁：`bash -n` OK + c4 渲染门禁 values/tasks 双绿 + `{{` 占位符审计（6 处全为原有，新代码无
模板序列）。新包 `ani-code-a22fix-20260919`（kk `0da3b88b0892ca27`；内嵌标记 A18×9、
cursor_ls×3、CONF_CM×6、k5_pod_ready×8；SHA256SUMS 全过）；吸取坑 #21，先同步 obs-live
releases（LIVE_SYNC_OK）再发射。

处置：restore + `ani-code-a22fix-20260919` 重跑 **a23**（loki-cumulative 全链）。a22 已证明
代码链推进到 fluent-bit [2]；若 a23 fluent-bit [2]–[5]（ConfigMap 链 + inspector pod）全过即
H4 收官。

### attempt h4loki-a23（INSTALL_EXIT=1 → 缺陷 A19 定性：从未执行过的 heredoc 行尾多 `]`；A18 修复首次真实执行通过 [2] 前半段）

包 `ani-code-a22fix-20260919`。restore 3/3 → 编排全绿 → INSTALL_LAUNCHED ~00:02 UTC，00:39 UTC
死，安装运行约 36 分钟。

**里程碑**：A18 修复（[2] 的 ConfigMap 链）**首次真实执行通过**——`note "collector config read
from ConfigMap ani-fluent-bit (mounted by ...)"` 语义生效，config 从 API 读回并过了 [1]/[2] 的
排他断言。推进到 [2] 的 **marker pod heredoc apply** 时炸：

```
error parsing STDIN: error converting YAML to JSON: yaml: line 14: did not find expected '-' indicator
```

**缺陷 A19（从未执行段的第一颗雷）**：marker pod heredoc 的 `args` 块序列行尾多一个 `]`
（`...; i=$((i+1)); done"]`）——YAML 非法。post-marker heredoc（`ani-log-marker-post`）**同病**。
两段代码从未真实执行过（a22 死在 [2] 更早的 exec cat），首次执行即炸——与 A15/A18 同属"未执行
分支"魔咒。

**修复**：两处 `; done"]` → `; done"`（replace_all 实证 2 处）。**新增 heredoc YAML 模拟校验
门禁**（`/tmp/check-heredoc-yaml.sh`，已固化进 .workbuddy/tmp）：抽取全部 `cat <<EOF | $KC apply`
heredoc、样例值替换 shell 变量、python yaml.safe_load_all 校验——fluent-bit verify 3 个
heredoc 全 OK。**顺手对 opensearch verify 跑同款检查：0 个 apply-heredoc + bash -n OK——H5
无此雷**。

门禁：bash -n + c4 双门禁绿。新包 `ani-code-a23fix-20260919`（kk `af690e7e61bacc2f`；`done"`
标记 ×6 实锤修复入包；SUMS 全过）+ LIVE_SYNC_OK 后发射。

处置：restore + `ani-code-a23fix-20260919` 重跑 **a24**。链路状态：metrics 二连过、Loki 全过、
fluent-bit [1] + [2] 前半（ConfigMap 链）真实通过；a24 需 fluent-bit [2] marker 采集 → [3]
backend 恢复 → [4] cursor/inspector → [5] 排他断言全过即 **H4 收官**。

### attempt h4loki-a24（INSTALL_EXIT=1 → 缺陷 A20 定性：verify.sh 镜像键 typo 渲染为 nil；A19 修复首次真实执行通过；verify 渲染门禁落地）

包 `ani-code-a23fix-20260919`。restore 3/3 → 编排全绿 → INSTALL_LAUNCHED ~00:44 UTC，01:16 UTC
死，运行约 32 分钟。

**里程碑**：A19 修复（两处 heredoc `]`）**首次真实执行通过**——marker heredoc apply 成功。
推进到 [2] 的 marker pod 等待：`kubectl wait --for=condition=Ready pod/ani-log-marker-1
--timeout=180s` 超时。

**存活现场取证推翻 K-5 猜测**：三个 marker pod（node1/2/3）**全部 InvalidImageName**，事件显示
`Failed to apply default image tag "<no value>": invalid reference format`——**不是网络病，是
镜像名渲染成了 Go 模板 nil 值**。**缺陷 A20**：fluent-bit verify.sh 写的是
`index .ani.images "docker.io/library/busybox:1.37"`，而 images.tsv 的键是
`docker.io/library/busybox:1.37.0`——**键名差一个 `.0`**，`index` 返回 nil 渲染为
`<no value>`。逐键校验 8 个 role verify 引用的全部 7 个镜像键：**唯一缺失就是这一处**
（python:3.13.11-alpine3.23、openssl:3.5.4、nats-box:0.19.7、postgres:17.11-bookworm、
valkey:8.1.10-alpine、busybox:1.37.0 全在表）。loki/metrics verify 只用 python（键正确），
所以此前真实执行不炸——A20 是 fluent-bit 独有的雷，被 A15→A18→A19 一路挡到 a24 才首次暴露。

**修复三件套**：
1. verify.sh 键名对齐 `busybox:1.37.0`（注释记录 a24 证据）。
2. **渲染门禁落地 `TEMPLATE_KIND=verify`**（render-values.sh 三副本同步）：渲染 verify.sh 用
   installer 真上下文，产物级扫描 `<no value>`/`<nil>`（Go 渲染程序原有防线 + python 段双保险）
   + `bash -n` + `.sh` 输出防呆放宽。这是 H1（tasks 进门禁）教训之后第二个"未渲染文件"——
   **verify.sh 从此纳入离线渲染门禁**。
3. **全量回归**：8 个 role 的 verify 渲染 8/8 PASS（BACKEND=loki）+ fluent-bit/opensearch 的
   BACKEND=opensearch 分支 2/2 PASS——H5 的 verify 无同类雷。

插曲：A20 的注释文本里写了 `<no value>` 字面量，被门禁产物级扫描拦截（注释也是产物）——
改写注释并在其中说明"注释不得含该字面量"。新包 `ani-code-a24fix-20260919`
（kk `cce36749aa74d171`；SUMS 全过）+ LIVE_SYNC_OK。

处置：restore + `ani-code-a24fix-20260919` 重跑 **a25**。链路只剩 fluent-bit [2] marker 采集
→ [3] backend 恢复 → [4] cursor/inspector → [5] 排他断言——全过即 **H4 收官**。

### attempt h4loki-a25（INSTALL_EXIT=1 → 缺陷 A21 定性：check_metadata.py 三雷同发，marker 采集本身首次真实通过）

包 `ani-code-a24fix-20260919`。restore 3/3 → 编排全绿 → INSTALL_LAUNCHED ~01:38 UTC，01:59 UTC
死，运行约 22 分钟。

**里程碑（采集链路本体首次真实通过）**：A20 修复（busybox 键）真实生效——三个 marker pod
（node1/2/3）全部 Ready；`await_markers.py` 在 client pod 内查 Loki，**三枚 marker 全部查到**，
返回 JSON 带 namespace/pod/container/node 标签（`service_name: marker`），tee 成功写盘
`markers-found.txt`（3090B，root 0700）。**容器 stdout → 运行时日志文件 → collector tail →
Loki 可查**——这条 C3 卡的核心链路真实走通了。

推进到 [2] 的元数据校验即死：

```
File "<stdin>", line 3, in <module>
FileNotFoundError: [Errno 2] No such file or directory:
  '/var/lib/ani-installer/ani-lab/logs/fluent-bit-verify-20260920T015654Z/markers-found.txt'
command terminated with exit code 1
```

**缺陷 A21（一个函数三颗雷，全部属"从未执行分支"）**：`check_metadata.py` 从未真实运行过
（a24 死在它前面的 marker 等待）。
1. **宿主路径传入 pod**：`py()` 经 `kubectl exec` 把脚本送进 client pod 执行，而第一参数是
   node1 宿主机路径 `$EVIDENCE/markers-found.txt`——pod 内无此路径，直接 FileNotFoundError
   （报错里的 `command terminated with exit code 1` 是 kubectl exec 格式，实锤执行位置）。
2. **参数切片错位**：`nodes = sys.argv[2:]` 会把尾部 NS 一并吞入，`expected_ns =
   sys.argv[2 + len(nodes)]` 越界 → IndexError（修完第 1 颗也会死在这）。
3. **序号解析错误**：`marker.rsplit("-n", 1)` 对 `…-n3-node1-node3` 会切在 `-node3` 的
   `-n` 上，`int("ode3")` → ValueError（修完前两颗还会死在这）。

**修复**：① 查询结果先上传进 pod（`$KC exec -i … -- sh -c 'cat > /tmp/markers-found.json'`），
checker 读 pod 内路径，node 侧 tee 文件保留为证据；② 切片改 `sys.argv[2:-1]` / `sys.argv[-1]`；
③ 序号用 `re.search(r"-n(\d+)-", marker)` 锚定解析。**顺手修仓库源码同步缺口**：渲染门禁
verify 扩展此前只改了仓库 c4 副本（fedora 三副本已同步）——c2/c3 仓库副本补齐，三副本一致。
**离线复验**：从 node1 sudo 拉回 a25 真实 `markers-found.txt`，跑修复后 checker → **RC=0**，
三枚 marker 的 namespace/pod/container/node 断言全过——a25 若已带修复，[2] 即通过。渲染门禁
8/8 verify PASS。新包 `ani-code-a25fix-20260919`（kk `c5e184e5fab9627c`；SUMS 全过）+
LIVE_SYNC_OK。

处置：restore + `ani-code-a25fix-20260919` 重跑 **a26**。链路状态：[2] 自 check_metadata 起
为新执行面，[3] backend 恢复 → [4] cursor/inspector → [5] 排他断言仍未真实执行——继续逐段
真实推进，全过即 **H4 收官**。

### attempt h4loki-a26（INSTALL_EXIT=1 → K-5 新变体：post-Ready netns 死亡；A21 修复真实通过 [2]/[3]，A22 修复 = 断言级恢复重入）

包 `ani-code-a25fix-20260919`。restore 3/3 → 编排全绿 → INSTALL_LAUNCHED ~02:16 UTC，02:37 UTC
死，运行约 21 分钟。

**里程碑**：**A21 修复真实执行通过**——[2] 全过（JSON 上传进 pod、check_metadata 三雷全排：
正则序号、切片、pod 内路径），**[3] backend pod 重建恢复也过**（PVC/UID 不变、旧 marker 复读
通过）。推进到 **metrics [7/8]**（Prometheus 重建持久性）：

```
FAIL: the pre-rebuild sample is not readable after the rebuild
urllib.error.URLError: <urlopen error timed out>   # TCP connect 阶段超时
```

**K-5 新变体定性（post-Ready 死亡）**：重建的 prometheus-0（node3，10.16.0.77）短暂 Ready
→ `k5_rebuild_wait` 由此通过（无 K5_RETRY）→ **约 30 秒后 netns 死亡**：容器自身日志
`dial tcp 10.96.0.1:443: connect: no route to host`（出向死）+ node1→pod IP:9090 TCP 直连
FAIL（入向黑洞）+ kubelet 探测 `context deadline exceeded` + **`ani-metrics-prometheus`
Service Endpoints 清空** → range 查询打到空后端的 ClusterIP → connect 超时。Ready 条件
翻 False@02:37:05（FAIL@02:37:23）。容器 restart=1（liveness 杀，优雅退出 exit=0），
无 OOM（未设 limits）。**k5_rebuild_wait 的盲窗**：pod 级 Ready 信号通过后、断言执行前的
窗口内 netns 死亡，pod 级信号无法预见——与 a12/a17 的"从未 Ready"形态互补。

**缺陷 A22（恢复链盲窗）+ 修复**：metrics verify 新增 **k5_read_again**（一函数一职责：
读断言失败 → 记 `k5_retry_sample_read` 进 k5-retries.txt → 删 pod 重走 k5_rebuild_wait
三阶段恢复 → 返回后重读；样本在 PVC 上，二次重建不损断言）：
1. [7/8] range 查询包一层恢复重入（首读失败 → k5_read_again → 重读，仍败才 fail 原消息）。
2. [8/8] silence 读回轮询抽出 am_silence_poll，同型包装。
3. **测试夹具滞后修正**：`TestFluentBitVerifyProvesCollectionPath` 仍断言 A20 修复前的
   `busybox:1.37`（带闭引号）旧键 → 改为 `busybox:1.37.0` 并注释键必须逐字符对齐的缘由
   （该测试此前在 a24/a25 轮未跑——build-code.sh 不含测试，教训：修 verify 键必须同步
   grep 测试夹具里的键字面量）。

门禁：渲染门禁 8/8 verify PASS + bash -n + go test ./pkg/ani/ **ok**（夹具修正后）。
新包 `ani-code-a26fix-20260919`（kk `dff7cfe8f834643f`；SUMS 全过）+ LIVE_SYNC_OK。

处置：restore + `ani-code-a26fix-20260919` 重跑 **a27**。残留风险：fluent-bit [3]/[4] 的
重建后 await 有同样的 post-Ready 盲窗（内部 120–300s 轮询可吸收瞬态，持续死亡仍会 FAIL），
本轮先不动——若被击中则同型包装。全过即 **H4 收官**。

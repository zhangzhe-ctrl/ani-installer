# 第二批指标、告警与 Fluent Bit：官方材料核对

日期：2026-09-19。本文是实施方案的材料依据，不是安装通过报告。

所有下载、镜像 manifest 查询和 Helm 渲染均在 `ssh fedora` 执行；没有访问或修改测试集群，没有编译或修改产品代码。证据目录：`fedora:/home/chabking/ani-installer-runs/observability-materials-20260919/metrics/`。

## 1. 固定候选与依据

建议直接使用完整的官方 `kube-prometheus-stack 85.4.0` Chart，不另写 Prometheus/Alertmanager StatefulSet，不拆开拼接任意新版本。

| 材料 | 固定候选 | 本次确认 |
|---|---|---|
| kube-prometheus-stack | 85.4.0 | 官方 tgz 下载、SHA256 与官方 index 一致；包含子 Chart 和 CRD |
| Prometheus Operator | v0.90.1 | Chart appVersion；镜像 manifest 存在且有 linux/amd64 |
| config-reloader | v0.90.1 | Operator 参数显式引用；镜像存在且有 linux/amd64 |
| Prometheus | v3.11.3-distroless | 此 Chart 默认镜像；存在且有 linux/amd64 |
| Alertmanager | v0.32.1 | 此 Chart 默认镜像；存在且有 linux/amd64 |
| kube-state-metrics | Chart 7.4.0 / app 2.19.0 | 捆绑子 Chart；镜像存在且有 linux/amd64 |
| node-exporter | Chart 4.55.0 / app 1.11.1-distroless | 父 Chart 覆盖为 distroless；实际渲染确认；镜像存在且有 linux/amd64 |
| admission create/patch Job | kube-webhook-certgen 1.8.3 | 两个 Helm hook 使用同一镜像；存在且有 linux/amd64 |
| Fluent Bit | Chart 0.58.2 / app 5.1.2 | 官方 tgz 下载、SHA256 与官方 index 一致；日志材料报告负责完整后端组合验证 |

材料链接：[Prometheus 官方 Chart index](https://prometheus-community.github.io/helm-charts/index.yaml)、[85.4.0 发布包](https://github.com/prometheus-community/helm-charts/releases/download/kube-prometheus-stack-85.4.0/kube-prometheus-stack-85.4.0.tgz)、[固定 Chart 源码](https://github.com/prometheus-community/helm-charts/tree/kube-prometheus-stack-85.4.0/charts/kube-prometheus-stack)、[Fluent 官方 Chart index](https://fluent.github.io/helm-charts/index.yaml)、[Fluent Bit 0.58.2 发布包](https://github.com/fluent/helm-charts/releases/download/fluent-bit-0.58.2/fluent-bit-0.58.2.tgz)。

Chart SHA256：

```text
kube-prometheus-stack-85.4.0.tgz
3b07b7c91f1eaec75a125d1938c4e10d3b1ef6076ff7226d031fadadb4651a60

fluent-bit-0.58.2.tgz
2404614ace4c7dc049b76fd39d13b695f7b66332c38095327ebaa8fc91f0b046
```

版本选择原因：Operator v0.90.1 固定源码使用 `client-go v0.35.2`，kube-state-metrics v2.19.0 对应 Kubernetes 1.35；比研究时最新 stack 91.4.1 的 Operator/client-go 0.37、KSM/client-go 1.36 更贴近现有 Kubernetes v1.35.8。Chart 85.4.0 的 Kubernetes 声明是 `>=1.25.0-0`。这支持作为候选，**不构成整个组合已通过 Kubernetes 1.35.8 安装验证的证明**。[Operator v0.90.1 compatibility](https://github.com/prometheus-operator/prometheus-operator/blob/v0.90.1/Documentation/getting-started/compatibility.md)、[KSM v2.19.0 README](https://github.com/kubernetes/kube-state-metrics/blob/v2.19.0/README.md)。

必须保留的限制：Operator v0.90.1 自身的主要 e2e 默认是 Prometheus v3.10.0、Alertmanager v0.31.1；Chart 85.4.0 已将这两者更新到表中的版本。我们选择保留 Chart 自带组合，真实离线安装通过之前状态只能为 `candidate / installation_not_verified`，不得把 Operator 文档误写为该 Chart 全组合的实际验收记录。

## 2. 最小部署轮廓与真实 values 字段

固定 release `ani-metrics`、namespace `ani-observability`、`fullnameOverride: ani-metrics`，单实例。实际用现有 Helm v3.20.0 加 `--kube-version 1.35.8 --include-crds` 完成离线渲染，返回 0。渲染材料是 `metrics-values.yaml` 和 `metrics-render.yaml`，没有连接 API Server。

存储字段是：

| 目标 | values 路径 | 本轮建议 |
|---|---|---|
| Prometheus 副本 | `prometheus.prometheusSpec.replicas`、`shards` | 都为 1 |
| Prometheus PVC | `prometheus.prometheusSpec.storageSpec.volumeClaimTemplate.spec` | `ani-block`，RWO，5Gi |
| Prometheus 保留 | `prometheus.prometheusSpec.retention`、`retentionSize` | 24h、4GB；容量为实验轮廓，不承诺生产容量 |
| Alertmanager 副本 | `alertmanager.alertmanagerSpec.replicas` | 1 |
| Alertmanager PVC | `alertmanager.alertmanagerSpec.storage.volumeClaimTemplate.spec` | `ani-block`，RWO，1Gi |

保持 kubeApiServer、kubelet/cAdvisor、kube-state-metrics、node-exporter 和 stack 自身的采集。第一版关闭 kubeEtcd、kubeControllerManager、kubeScheduler、kubeProxy、coreDns、kubeDns 的对应 `enabled` 字段，避免为了抓取未暴露端点反过来修改 Kubernetes 控制面。后续若要纳入这些端点，必须独立授权与验证，不能静默修改服务监听地址。

建议本轮 `defaultRules.create: false`，只验收告警引擎和明确生成的实验告警；这不是宣称已经交付完整平台告警规则库。关闭 Grafana、windowsMonitoring、ThanosRuler 及 Thanos 相关服务/sidecar；不启用 external ingress、remoteWrite、OTLP receiver、Prometheus admin API。Grafana 和业务告警尚未纳入用户授权范围。[固定 values](https://github.com/prometheus-community/helm-charts/blob/kube-prometheus-stack-85.4.0/charts/kube-prometheus-stack/values.yaml)。

CRD 使用本包自带的首次安装路径：`crds.enabled: true`，`crds.upgradeJob.enabled: false`；没有升级、旧 CRD 清理或强制夺取现有字段所有权的需求。保持 admission webhook 默认开启，采用 Chart 自带 create/patch hooks，不引入 cert-manager 的强制依赖（`prometheusOperator.admissionWebhooks.certManager.enabled: false`）。hook 镜像必须已进入 artifact。[CRD 子 Chart](https://github.com/prometheus-community/helm-charts/tree/kube-prometheus-stack-85.4.0/charts/kube-prometheus-stack/charts/crds)、[admission templates](https://github.com/prometheus-community/helm-charts/tree/kube-prometheus-stack-85.4.0/charts/kube-prometheus-stack/templates/prometheus-operator/admission-webhooks)。

`helm template` 成功不证明 CRD 已被 API Server 接受，更不证明 webhook 可达。安装阶段分别记录 CRD Established、hook Job 成功、Operator Ready、Prometheus/Alertmanager CR 被调谐和业务读写结果。不得用 `--no-hooks`、禁用 webhook 或放宽验证来掩盖缺镜像、网络错误。

## 3. 实际镜像闭包与改写

以下 7 个运行镜像都已经通过官方 registry manifest GET 确认 200，且存在 linux/amd64 条目：

```text
quay.io/prometheus-operator/prometheus-operator:v0.90.1
quay.io/prometheus-operator/prometheus-config-reloader:v0.90.1
quay.io/prometheus/prometheus:v3.11.3-distroless
quay.io/prometheus/alertmanager:v0.32.1
registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.19.0
quay.io/prometheus/node-exporter:v1.11.1-distroless
ghcr.io/jkroepke/kube-webhook-certgen:1.8.3
```

`image-checks.json` 记录了前 6 个相关 manifest 查询及辅助镜像；node-exporter 最初核对了非 distroless 版本，随后发现父 Chart 覆盖并独立确认实际 distroless 镜像，不能把前一次查询误当实际镜像：其 index digest 为 `sha256:6112664fd761bb964d8a2d3d0119d6c8402618a89edbb3a43c8f7b4090fb53c9`，linux/amd64 manifest 为 `sha256:19bafa75a9de0c9562adf4af595c99c5fe316bb94a297e611495f2dd2ed44a9c`。

重要的漏包点：

- `helm template` 中 Prometheus、Alertmanager 镜像位于自定义资源，不是 Pod。
- reloader 镜像位于 Operator Deployment 的 `--prometheus-config-reloader=` 参数；由 Operator 后续生成工作负载，不能只扫描 `spec.containers[].image`。
- admission create、patch Job 属于 Helm hook；不能因为 Job 安装后消失而遗漏。
- 父 Chart 设定 node-exporter distroless，优先看最终合并值和渲染结果，不能只读子 Chart 默认值。
- Operator 默认参数还带 `--thanos-default-base-image=quay.io/thanos/thanos:v0.41.0`。当前不启用 Thanos 时不会因此启动 Thanos；应在闭包报告明确它是禁用功能的默认参数。若把所有镜像形态引用都统一纳入重写，则要同步锁定/制包该引用；不得误标为已运行 Thanos，也不要仅为了消掉字符串新增 Thanos 服务。

离线镜像值通过现有 `ani/images.tsv` 的映射生成，显式覆盖：

```text
prometheusOperator.image
prometheusOperator.prometheusConfigReloader.image
prometheusOperator.admissionWebhooks.patch.image
prometheus.prometheusSpec.image
alertmanager.alertmanagerSpec.image
kube-state-metrics.image
prometheus-node-exporter.image
```

每项要同时考虑 registry、repository、tag 以及 Chart 特有的 sha/digest 字段；不要只填 `global.imageRegistry` 却忘了仓库路径重命名。node-exporter 已带 `-distroless` 的显式 tag 不得再生成双重后缀。镜像 index digest、linux/amd64 manifest digest、导入后的 digest 需区分；也不要把 `sha256:` 前缀重复拼接到 Chart 的裸 sha 字段。[Prometheus CR 模板](https://github.com/prometheus-community/helm-charts/blob/kube-prometheus-stack-85.4.0/charts/kube-prometheus-stack/templates/prometheus/prometheus.yaml)、[Alertmanager CR 模板](https://github.com/prometheus-community/helm-charts/blob/kube-prometheus-stack-85.4.0/charts/kube-prometheus-stack/templates/alertmanager/alertmanager.yaml)。

## 4. 告警真实链路：最短验收方案

建议采用单独的实验接收器，不接入 ANI 通知、邮件、Slack，也不把接收器变成长期基础组件。官方工具镜像候选 `docker.io/library/python:3.13.11-alpine3.23` 已确认 manifest 200、linux/amd64；index digest `sha256:2f607129b1b915a949320bf0c4831a73d1c1b1be663c2b1d8c93aa35a5f44a95`。只通过 ConfigMap 提供短 Python 标准库 HTTPServer 脚本，不执行 pip、apk 或公网下载。[Docker Official Python](https://github.com/docker-library/python)。

该镜像可同时承担隔离的 HTTP 查询客户端、日志发射器和 webhook 接收器，减少验证工具镜像数。常规验证客户端脚本可以由 code 包/role 提供；接收器等实验工作负载属于 `scripts/lab` 与验收材料，不作为生产常驻服务，也不属于安装恢复逻辑。

建议真实测试顺序：

1. 生成 `run_id`，创建任务专属 namespace、receiver Deployment/Service。receiver 只接受预期路径的 POST，读完 JSON 后将完整脱敏事件写入 stdout/任务文件并 flush，再返回 200；收到请求这一事实不能由 Prometheus/AM 查询 API 替代。
2. 创建 `PrometheusRule`，metadata label `release: ani-metrics`。规则初始表达式 `vector(1) == 1`，短 `for`，带 `run_id` 和本实验 namespace 标签。实际 Prometheus CR 的 ruleSelector 默认为该 release；必须检查实际 CR，防止规则根本未被选中。
3. 创建任务专属 `AlertmanagerConfig`，明确短 groupWait/groupInterval、run_id matcher、webhook URL 和 `sendResolved: true`；其 labels 必须匹配实际 Alertmanager CR 的选择器。Operator 会按 AlertmanagerConfig 所在 namespace 限定告警路由，故测试告警的 `namespace` label 必须匹配，不能只加 run_id。
4. 等待 Prometheus 显示 firing，同时接收器真实收到本 run_id 的 firing POST。保存匹配到的单条 alert、时间和 fingerprint，不以存在任意告警作为通过。
5. 将同一条规则表达式改为 `vector(0) == 1`，保留告警身份和 labels；等待告警变为 inactive、接收器收到对应 resolved。不要只删除规则或等 HTTP 200 就宣称恢复通知成功。
6. 验收完成后保存完整 evidence；可删除本任务精确命名的测试资源。失败时先保存现场，不能用大范围删除 monitoring namespace/PVC 或清理网络来重试。

以下示例字段已经对照本包 CRD 核对：AlertmanagerConfig 当前 served/storage 版本为 `monitoring.coreos.com/v1alpha1`，不是随意选择 v1beta1。`RENDERED_RUN_ID`、namespace 和接收器 Service 名由实验脚本用 YAML 序列化器填入，不能把占位文本原样 apply。

```yaml
apiVersion: monitoring.coreos.com/v1alpha1
kind: AlertmanagerConfig
metadata:
  name: ani-alert-check
  namespace: ani-observe-verify-RENDERED_RUN_ID
  labels:
    run_id: RENDERED_RUN_ID
spec:
  route:
    receiver: lab
    groupBy: [alertname, run_id]
    groupWait: 5s
    groupInterval: 10s
    repeatInterval: 1h
    matchers:
      - name: run_id
        value: RENDERED_RUN_ID
        matchType: '='
  receivers:
    - name: lab
      webhookConfigs:
        - url: http://receiver.ani-observe-verify-RENDERED_RUN_ID.svc:8080/alerts
          sendResolved: true
---
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: ani-alert-check
  namespace: ani-observe-verify-RENDERED_RUN_ID
  labels:
    release: ani-metrics
spec:
  groups:
    - name: ani-lab
      interval: 15s
      rules:
        - alert: AniInstallerRound2Probe
          expr: vector(1) == 1
          for: 15s
          labels:
            run_id: RENDERED_RUN_ID
            namespace: ani-observe-verify-RENDERED_RUN_ID
            severity: info
```

实验配置必须明确限定 `alertmanager.alertmanagerSpec.alertmanagerConfigSelector.matchLabels.run_id` 为本 run_id，并将 `alertmanagerConfigNamespaceSelector.matchLabels["kubernetes.io/metadata.name"]` 限定为本实验 namespace。Prometheus 的规则 namespace selector 同样明确覆盖该 namespace，ruleSelector 保留 `release: ani-metrics`。这些是实验轮廓的选择器，不把临时 run_id 固化为用户产品 API，也不把全 namespace 选取当作解决选不到规则的办法。

接收器只用集群内临时地址，不需要外部凭据。如果后续 webhook 需要认证，URL 中的 token 用 `urlSecret` 引用同 namespace 的 Secret，HTTP 认证按 `httpConfig` 的 Secret selector 配置；不得放进 values、日志或连接说明。此处不实现外部通知系统接入。

这些是建议的实验流程，尚未在测试集群运行。Alertmanager webhook JSON 有整体 status 和单条 alerts[].status，应按 run_id+fingerprint 识别对应事件；`send_resolved`/CR 的 `sendResolved` 是两种配置层级，不要混用字段名。[Alertmanager webhook 格式](https://prometheus.io/docs/alerting/latest/configuration/#webhook_config)、[Operator Alerting Routes](https://prometheus-operator.dev/docs/developer/alerting/)。

指标验收至少实际查询：三台 node-exporter 对应的 `node_uname_info`、kube-state-metrics 的三台 `kube_node_info`、kubelet/cAdvisor 的容器序列、API Server 指标；检查各自采集目标 `up == 1`。目标数应按实际部署对象计算，不能写死所有 job 的目标数。测试 Prometheus 正常 Pod 重建后仍能查询重建前指定时间点的历史样本；测试 Alertmanager 正常 Pod 重建后保留任务唯一 silence。保存 PVC UID 前后不变的证据。这些重建仅发生在实验脚本，不在每次普通 `verify.sh` 中执行；不据此承诺 HA 或整群掉电恢复。

## 5. Fluent Bit 的材料注意事项

官方 Chart `0.58.2` 的镜像字段为 `image.repository/tag/digest`，appVersion 为 `5.1.2`，默认 repository 是 `cr.fluentbit.io/fluent/fluent-bit`。不能套用 stack 的 registry/repository 分拆字段。默认 `testFramework.enabled: true` 使用 `busybox:latest`，本期关闭，使用上面的固定实验工具镜像完成真实链路验收；`hotReload.enabled: false` 保持关闭，否则会新增 configmap-reload 镜像。[固定 Chart values](https://github.com/fluent/helm-charts/blob/fluent-bit-0.58.2/charts/fluent-bit/values.yaml)。

Chart 默认输出仍是 Elasticsearch 且包含 systemd/kubelet 日志输入，本期要整体替换 `config.inputs`、`config.filters`、`config.outputs` 为明确的容器日志轮廓；不能 append 第二个后端或遗留默认输出导致双写。containerd 容器日志使用 CRI parser，挂载真实 `/var/log` 链路；不因默认 Docker 路径缺失而创建一套 Docker 或修补节点目录。

DaemonSet 要明确覆盖三台节点的 taints，记录只采集预期节点。Log backend 选择为 none 时不部署 Fluent Bit。Loki 与 OpenSearch 分别配置官方 output plugin，不实现 installer 自己的转发进程。[Fluent Bit Loki output](https://docs.fluentbit.io/manual/data-pipeline/outputs/loki)、[OpenSearch output](https://docs.fluentbit.io/manual/data-pipeline/outputs/opensearch)。

## 6. 当前结论

- `pass`：上述官方 Chart 下载及哈希、指标运行镜像 amd64 manifest 存在、指标最小轮廓 Helm 渲染。
- `not_verified`：真实 Kubernetes 1.35.8 CRD/webhook 调谐、完全离线安装、指标采集、firing/resolved 通知、PVC 重建持久化、Fluent Bit 两种日志后端的真实链路。
- installer 只负责这些确定材料的分发、配置、安装、等待和验收入口。kcn/CSI/Prometheus/日志引擎自身缺陷分别归组件；不得为通过测试添加网络清理、清 PVC、改组件源码或静默降级。

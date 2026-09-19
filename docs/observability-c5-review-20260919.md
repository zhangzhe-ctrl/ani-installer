# 第二批 C5 完成度复核（2026-09-19）

## 结论

复核源码：`c7661ca5c5861a4331e97eac43d1594551cfc14a`。不能认定 C0～C5 全部完成。
本轮发现 C4 OpenSearch 的确定性安装编排缺陷，C5 交付物也不齐全。
代码测试通过不能替代安装路径正确性或真实离线验收。

本次仅核对源码、文档、Fedora 材料并运行针对性测试。未修改产品代码、未连接或重置测试节点、未启动安装、未提交或推送。
沿用此前约定：新版 kcn 材料尚未提供，不使用旧版已知故障反复重装验证。

## 必须修正的安装问题

### R1 / P1：OpenSearch 启动依赖的 Secret 创建顺序错误

位置：`kubekey/builtin/core/roles/ani/opensearch/tasks/main.yaml:92`、`:124`、`:151`。

Chart values 配置 `securityConfig.config.securityConfigSecret=ani-opensearch-security-config`。
实际渲染 StatefulSet 将该 Secret 作为非 optional 卷挂载。
然而 role 先执行 `helm upgrade --install --wait --timeout 1200s`，之后才渲染并 apply 这个 Secret。
干净安装时 Pod 无法挂载不存在的 Secret，Helm 等待超时，永远到不了创建 Secret 的任务。

最小修正：把所需 Secret 的渲染和 apply 放到 Helm 安装之前。无需增加重试、清理或兼容逻辑。

### R2 / P1：安全初始化 Job 只有渲染，没有 apply

位置：同文件 `:119`、`:148`。

`security-init.yaml` 包含 Job、ServiceAccount、Role、RoleBinding。
role 渲染该文件后，只 apply `security-config.yaml`，随后直接等待 Job 完成。
未见提交 `security-init.yaml` 的任务。因此即使修正 R1，初始化 Job 仍不存在，等待会失败。

最小修正：在服务能够接受初始化请求后 apply 初始化清单，再等待 Job 完成。
保留真实失败信息，不用重建集群内对象来掩盖初始化错误。

### R3 / P1：等待和验证使用了错误的 PVC 名称

位置：`tasks/main.yaml:111`、`templates/verify.sh:30`（均在 OpenSearch role 下）。

实际 Chart 渲染：StatefulSet 名 `ani-opensearch-master`，volumeClaimTemplate 名也为
`ani-opensearch-master`。StatefulSet 动态 PVC 名为 `<template>-<statefulset>-<ordinal>`，因此本例是：

```text
ani-opensearch-master-ani-opensearch-master-0
```

当前代码等待的是 Pod 名形状 `ani-opensearch-master-0`，不是 PVC 名。
render gate 第 296 行和手动文档第 149 行也重复了相同错误。

最小修正：统一正确名称，或由实际 StatefulSet/Pod 的卷引用取得 claimName；不做多名称兜底探测。
检查应从 Chart 渲染的 StatefulSet 名与 claim template 名推导预期结果，不能重复错误常量自证。

### R4 / P2：宣称设置所有节点，实际只设置首控制节点

位置：`kubekey/builtin/core/playbooks/create_cluster.yaml:95` 与 OpenSearch role `tasks/main.yaml:42`。

该 play 的 hosts 只有 `kube_control_plane[0]`。其中 “Raise vm.max_map_count on every node”
任务没有遍历其他节点，实际仅修改首控制节点。OpenSearch 没有限定必须调度到这个节点。
其他节点参数不足时，Pod 调度或重建到这些节点可能启动失败。

最小修正：通过已有执行机制在实际允许调度的节点设置所需参数；不自建 SSH 执行框架。
这是部署前置依赖，属于 installer 职责，不是 OpenSearch 组件 bug。

## C5 交付缺口

执行方案第 12 节要求独立 code/artifact、SHA256SUMS、源码与实际 kk 对应关系，以及真实路径的操作文档。
本次 Fedora 核对 `/home/chabking/ani-installer-runs/observability-20260919/releases/` 为空。
手动文档自己也承认发布物为空，安装命令仍为 `<CODE>` / `<ARTI>` 占位符。
因此 C5 目前只能算文档草稿，不能记作完整交付 pass。不要因此把 kk 放进 artifact；两者继续独立交付。

手动文档还需修正：

- 第 355 行引用不存在的 `kubekey/lab/c2-render-gate/run-c2-gate.sh`；应提供现存入口的实际调用方式。
- 第 214 行“不会有第三个状态”与支持 `logging.backend: none` 的契约矛盾。
- 第 337 行 `kubectl get secret ... | base64 -d` 会把密码输出到终端，不是“读凭据但不上屏”。不要用这条命令证明不输出凭据。
- 修正 OpenSearch PVC 说明及 status 中重复的错误结论。

## 本次验证证据

所有执行均在 SSH host `fedora`，使用当前 HEAD 的 `git archive` 源码快照。
独立工作目录：`/home/chabking/ani-installer-runs/c5-audit-20260919/`。

| 检查 | 结果 | 证据 |
| --- | --- | --- |
| 当前 HEAD `go test ./pkg/ani/...` | pass | `go-test.log`，`ok .../pkg/ani 2.365s` |
| 当前 OpenSearch values 渲染 | pass | `opensearch-values.yaml`、`render-values.log` |
| 当前 OpenSearch Chart render gate | pass | `render-gate.log`，foreign images 0 |
| 实际 Chart 卷与 Secret 引用 | 已核对 | `opensearch-render.yaml` |
| C5 code/artifact 交付 | 未完成 | 原工作区 releases 为空 |
| 两后端完整离线安装 | not_verified | 本次未运行 |
| 指标查询、告警 firing/resolved、三节点采集、Pod 重建持久化 | not_verified | 本次未运行 |

这解释了为什么已有 gate 显示通过：它主要检查模板、对象字符串和镜像引用，没有证明
role 创建依赖的先后关系、初始化清单确实被提交、运行时 PVC 名称正确。
本报告不是对其余路径“没有缺陷”的保证；已发现的问题足以否定全部完成。

## 最小后续顺序

1. 只修 R1～R4，并补针对这些失败方式的检查；不增加通用校验框架、清理或自动修复逻辑。
2. Fedora 复跑相应 Go 测试与 Chart 渲染，核对实际执行任务依赖；更新 C4 状态。
3. 修正 C5 文档，独立产出 code 与 artifact 并记录哈希。代码变动只重建小 code 包；镜像/Chart 不变不重制大包。
4. 新版 kcn 材料到位且安排实测后，按既有计划分别从干净快照跑 Loki、OpenSearch 两组合。
   失败先留证据、归因；需要从零验证时通过 Fedora 的实验室快照脚本还原，不写进 installer。
5. 未执行的功能与持久化验收继续保留 not_verified。只有材料交付和对应实测都完成，才能宣布整批完成。

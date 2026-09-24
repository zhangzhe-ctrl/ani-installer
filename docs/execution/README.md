# ANI installer 执行计划包

**2026-09-24 / r1｜用途：人工执行 + 快速模型小步迭代｜状态：计划已编写，产品整改尚未在本轮执行。**

先读本文件，不要把整个 ZIP 一次性交给模型要求“全部完成”。本包没有修改 installer，没有连接实验机，也没有产生新的组件镜像摘要或实装通过记录。

## 三份主计划

| 文件 | 解决什么问题 | 读到哪里开始执行 |
|---|---|---|
| [01 蓝图](01-installer-blueprint.md) | 产品边界、现有与拟新增入口、配置、依赖、材料和运行记录 | 先读 §1–5，再按任务引用读其余章节 |
| [02 现有问题整改](02-remediation-plan.md) | 15项审查问题到17个R任务的映射，具体改哪里、怎样测、何时停 | 从R00开始，每次一张卡或一个子任务 |
| [03 组件分批计划](03-component-batches.md) | B00–B13活动批次、O01–O03条件批次，各批材料/实现/测试/实机步骤 | R阶段基础正确性收官后，先B00，随后按已选能力执行 |

辅助：[04 人工操作手册](04-manual-runbook.md)、[05 模型执行规则与提示词](05-model-instructions.md)、[06 版本研究登记](06-version-register.yaml)、[任务状态表](templates/progress.yaml)、[官方来源](reference/official-sources.md)、[本轮文档校准证据](reference/decision-corrections.md)。每张任务卡单独存放在 `tasks/`。

## 先固定五条决定

保持KubeKey fork和Go入口，不重写框架。代码发布物与artifact分离，纯代码修改不重新制作大包。失败停止变更，不让验证器修CNI/OVS/Ceph。**kcn专用Envoy保持独立，业务Envoy不能复用它。** LWS、vCluster、GPU和业务Envoy保持暂缓；五个Kubeflow组件按需独立接入，默认不装Istio/Knative。

旧版本表里的“以后解耦kcn Envoy”已经被后续明确要求撤销。本包不重发这条旧规则。

## 顺序，而不是全部同时开工

```text
R00 来源/环境校准
  → R01 凭据与lab边界
  → R02 删除验收越界恢复
  → R03 命令失败传播
  → R04 本地检查/真实CI
  → R05–R09 存储授权、配置、材料、渲染、预检
  → R10–R14 Kube-OVN、网络验证、超时、分层验收、仓库生命周期
  → R15 独立新增组件入口
  → R16 干净离线回归
  → B00 既有基础能力确认
  → B01 网络扩展（按选择）；B02 Milvus；B03/B04所需公共能力
  → B05 KubeVirt/CDI；B06 Volcano CPU；B07 Harbor
  → B08/B09/B10/B11/B12按需接入Kubeflow组件
  → B13 对所选组合完整收官
```

R07和R15各有三个更小的子卡。每个组件批次拆成 `.M`材料、`.I`实现、`.T`局部测试、`.V`真实验证。**材料研究可以在来源已确认后提前做，不需要等到集群安装完；但这不授予提前写目标集群的权限。** 新组件实施不得绕过尚未完成的公共整改。

O01 Dex、O02 Kata、O03 Jaeger是条件计划，不在本次默认执行集合。B13只覆盖实际选中能力，不能为了填满表格开启所有组件。

## 第一次交给执行模型的内容

粘贴 `05-model-instructions.md` 的通用规则，再附 `tasks/R00.md`，追加：

> 本次只执行R00。先确认本地权威源码与Fedora工作目录，记录内容指纹，核对旧文档冲突。不得安装、还原快照、清理目录、升级组件、改CNI、触碰kcn专用Envoy或自动提交Git。完成后按task-result模板报告；不要接着执行R01。

也可以在本地查看单张卡（这个工具只输出文字，不执行安装）：

```bash
python3 scripts/show-task.py R00
python3 scripts/show-task.py R07.1
python3 scripts/show-task.py B02.M
```

## 三种命令标识

**已存在**：当前源码中确有的 `kk ani install`、`install.sh`、build脚本。存在不表示缺陷已修复，不应在R阶段门禁之前直接部署。

**本包辅助工具**：`scripts/source-snapshot.py`、`scripts/show-task.py`，仅本地读取/记录，不连接节点；使用与自测范围见 `evidence/kit-validation.md`。

**拟新增**：`kk ani validate/render/components install/verify`、`scripts/check-code.sh`、组件检查器。必须先完成对应任务并核对 `--help` 或文件实际存在，再运行文中示例。旧代码不会因为文档写了命令就支持它。

## 已知、待核与不应扩大范围的事情

上传源码HEAD为`c7a97bb508b699d5117db625da2fa4a8155b838f`，但执行时必须以真实工作树指纹为准。旧AGENTS的kcn缺料描述与后续锁注释不一致，R00按实际材料核验，不重复旧阻塞。HAProxy/kube-vip在当前local控制面路径未使用：保留材料维护项，不启动控制面重构。

`06-version-register.yaml`继承先前研究版本，并附本轮范围修正。它不是`ComponentMaterialLock`，不能覆盖installer配置。每批M阶段要把官方tag/Chart/镜像/SDK/示例实际落成可验证材料；来源不可读时写blocked，不编造SHA，也不换最新版。

## 完成的含义

一张卡分别报告代码、材料、局部测试、实机smoke、专项持久化、干净离线全链、ANI业务接入状态。缺哪项就写`not_run`或`blocked`。`go test`匹配零用例、`bash -n`成功、Pod Ready、旧日志、计划文件存在，都不能代替功能验收。

本包SHA256SUMS只用于传输完整性；不充当组件材料真实性或生产兼容性认证。

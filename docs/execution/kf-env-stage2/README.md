# Kubeflow 二阶段统一首装

本轮已达到 **ENV_READY**：同一完整站点配置、同一次 `kk ani install` 安装基础集群、所选基础组件与 `26.03-kubeflow-stage2-v1` 完整组合。在最终新集群独立通过 Notebook → 原生 Jupyter kernel → 固定模型 → 受限 S3 → KServe 正确预测，以及持久化、失败、基础保护和受影响第一阶段回归。

ENV_READY 仅表示 installer 的原生组件环境可用。管理前端、BFF/modeldev、真实产品用户/租户/owner、IAM、业务网关/Cookie/XSRF/WebSocket 代理、长期 GC、CPU-P01 与 GPU/HAMi 均为本轮范围外或 NOT_RUN。

## 当前交接与身份

唯一交接为 [environment-handoff.json](records/environment-handoff.json)，证据摘要见 [evidence-index.json](records/evidence-index.json)，后续 backend/前端接入见 [config-contract.md](config-contract.md)。原第一阶段冻结报告保持原有含义，不作为本轮 PASS。

- 原工作区 `/home/chabking/workspace/ani-installer`，原分支 `release`；原状保留。
- origin `ssh://git@ssh.github.com:443/zhangzhe-ctrl/ani-installer.git`；2026-10-08T13:42:49Z 实际 fetch release，退出 0。
- BASE_SHA `cf54a5c129e6a7d003ef1e148ff85fc23fd40a37`。
- 任务分支 `codex/kubeflow-stage2`，工作树 `/home/chabking/.codex/worktrees/kubeflow-stage2/ani-installer`。
- 最终运行源码 `f8ca074473e6413ba5027ab4a003a42f99cf33c1`，已推送；后续交接文档提交不改变安装候选身份。
- Fedora `ssh fedora` → `chabking@172.16.101.31`，Fedora 44、Go 1.26.7；生成、测试、编译、镜像及物料准备均在该机完成。
- Fedora 任务根 `/home/chabking/ani-installer-runs/kf-env-stage2-20261008`。
- 目标仅 `172.16.101.20/.21/.22`，Ubuntu 24.04.4、各 8 vCPU/16GiB；各两块 200GiB 持久磁盘。用户确认重装后的 VM 可信，任务 known_hosts 严格校验；ESXi guest/IP/BIOS、快照和磁盘映射在每次还原前重新核实。
- 最终集群 UID `a14b07e9-cf39-46cd-8bea-c2fb9d8c55aa`；context `kubernetes-admin@ani-kf-stage2`，API `https://lb.kubesphere.local:6443`。实际节点与 Namespace UID 见 [cluster-identity.json](records/cluster-identity.json) 和交接。

## 同一候选的正式首装

[候选冻结](records/candidate-freeze.json) 的代码包、完整物料包、生产渲染及正式发布门禁均直接退出 0。源码指纹 `7a7dbe516ebc59a302eed36db2c685b648f069563b784e222e9fd77fae17b753` 在门禁、编译、打包期间一致。112 个镜像通过实际 registry 内容门禁；23 个批准源码文件、41 个 SDK wheel 与 100 个工作区 wheel 闭合，目标运行时不在线补依赖。

Fedora 发布目录：

- `code-candidate-f8ca074-attempt-01`，kk SHA256 `33b5bfce4caf5a7411ab967aee8779084350d01e1cf1f554f8aacdda0ff86378`。
- `materials-candidate-f8ca074-attempt-01`，SHA256SUMS 文件摘要 `136c793ea4f7d7bea7dcd462fa610753d8ae00b598621c8723776c89606fde37`。
- `render-candidate-f8ca074-attempt-01`；全部文件摘要、镜像/来源锁、站点身份与验收脚本摘要见冻结记录。

[最终首装记录](records/unified-first-install.json)：run `ani-ani-kf-stage2-20261008-184255`，2026-10-08T19:05:50Z 直接退出 0；`run.json` 为 install-success，`run-state.json` 的 phase/result 均 succeeded，并与上述候选身份一致。目标发布物位于 `.20:/home/ubuntu/kf-env-stage2/frozen-f8ca074-attempt-02/{code,materials}`。同目录私有站点的原始摘要为 `bb5d85ae96a78ecf7f69557d727a667d8cd3b58d80812bed932965107354f472`；凭据值不进入 Git 或公开包。

完整公开站点示例见 [site.example.yaml](site.example.yaml)。正式入口仍是 `kk ani install --config <完整私有站点> --package-root <完整物料包>`；没有阶段选择框架、Notebooks/KServe 子开关或安装后追加入口。关闭 Kubeflow 的行为与受影响模块由正式发布门禁验证。

## 新集群真实验收

下列结果均来自最终候选和最终集群 UID，各直接退出 0。原始脱敏报告在 Fedora `final-evidence-export-attempt-02`，按唯一索引读取。

| 检查 | 结果与可观察事实 |
| --- | --- |
| 主链 | PASS；原生 JupyterLab 页面/资源、REST 保存读回、真实 kernel/WebSocket 执行；独立 S3 摘要一致；预测 `[0,1,2]` |
| Notebook 停止/恢复 | PASS；原生注解停止/恢复，Pod UID 改变，原 PVC UID 和四个文件摘要一致 |
| 预测 Pod 重建 | PASS；仅按原 UID 条件删除一次，新的预测 Pod 仍返回 `[0,1,2]` |
| 错误模型路径/凭据 | PASS；两个独立负向 InferenceService 的 storage-initializer 可归因退出 1，原模型持续正确预测 |
| 基础保护 | PASS；普通 Notebook/预测 Pod 无管理员或原 KFP 控制面凭据；健康控制端点的网络拒绝、24 项 SA 授权、四组 S3 writer/reader 的职责/前缀/跨 Namespace 拒绝 |
| 第一阶段真实链 | PASS；KFP → TrainJob → JobSet 成功、真实退出 42 失败及原生停止；停止的实际 KFP 终态 FAILED 原样保留 |
| 执行卷 | PASS；三张原执行卷在 node3 独立只读挂载并读回，与原执行/TrainJob/PVC UID 关联 |
| admission | PASS；22 个 server-side dry-run 正向/拒绝请求，持久写入 0，原训练限制保持 |
| 第一阶段保护与交接 | PASS；普通工作负载控制面隔离、12 项受限 S3 检查及实际模型/link 读回、62 个 HTTP/gRPC 身份请求 |

当前主链对象均位于 `ani-kf-stage2-a`，owner label 为 `ani.io/managed-by=ani-kf-stage2`：

- Notebook `native-clean-20261009-02`，UID `84ff414a-145d-415f-b5aa-005076b0018e`。
- 独立 PVC `ani-notebook-workspace`，UID `e3d1098d-a258-41f6-bd95-481e85678427`；ani-cephfs、RWX、5Gi、`/home/jovyan`、UID/GID/fsGroup 1000，无 Notebook/Workflow ownerReference。
- InferenceService `sklearn-clean-20261009-02`，UID `6a41178d-f156-46fb-ba61-7c955e047fd2`；Standard、一个 CPU `kserve-sklearnserver` Runtime。
- 模型 `s3://ani-kf-stage2-ani-kf-stage2-a/models/clean-20261009-02/model.joblib`，SHA256 `4dcecdea7d2ffbd1c38ee4bfd54926bf723003da5559471eedd803aca9b9c292`；sklearn 1.5.2 / joblib 1.4.2，输入 `[[0,0],[10,10],[20,20]]`。

Notebook Controller 1.10.0 standalone/USE_ISTIO=false；JupyterLab 4.4.10/Python 3.11.14；KServe 0.16.0 Standard；KFP 2.16.0、Trainer 2.1.0、JobSet 0.10.1。Notebook writer 与 KServe reader 凭据职责分开，S3 HTTPS/CA 验证启用。实际 Secret/CA 引用、内部 Service/路径、Pod/PVC/Runtime UID、镜像摘要与后续安全动作均在交接中。

## 首错、恢复与保留边界

[首次干净验收记录](records/first-clean-attempt.json)：ff7c14e 首装及六项二阶段验收通过，但第一阶段 Run `eaec1012-0609-45f9-8145-412639aa0a8f` 在训练完成后 GET/list JobSet 遇 `429 storage is (re)initializing`、Retry-After 1，真实终态 FAILED。原身份在三个 API 实例随后均读取到同一 JobSet UID，排除 RBAC/API 版本错误。源码修复只对 GET/429 有界重试，最多五次、延迟 1～5 秒；写请求和权限拒绝不重放。Fedora 红/绿回归、锁定 SDK 无网络编译及保留环境的真实成功/失败/停止通过后，才冻结最终候选并决定另一次干净验收。原失败没有改写成 PASS。

[最终还原记录](records/clean-first-install-start.json)：原集群 `941f4bfe` 的 Notebook 四个文件、原失败及修复后三次执行的原始文件、28 份必要节点证据已在 Fedora `unique-output-preservation-current941` 保存并核对摘要。VM/快照/两块持久磁盘、Namespace/PV/PVC 身份及锁全部复核后，仅直接还原三台已核实快照，各退出 0；无额外 reboot/reset/off/on。还原后身份一致、数据盘空白、无 K8s。最初联调环境的唯一输出和 94 份证据仍在 `unique-output-preservation-attempt-01` 保留。

当前没有未通过的必要验收项。Ceph 20.2.4 实测仍有 AUTH_INSECURE_CLIENT_KEY_TYPE、AUTH_INSECURE_KEYS_ALLOWED、AUTH_INSECURE_KEYS_CREATABLE 三类 HEALTH_WARN；存储功能与持久化通过，未升级共享组件或修改配置隐藏告警，实际状态和 fsid 已纳入交接。

本任务对象和数据继续保留，未请求自动清理。后续业务任务应先读交接，明确 owner、用户身份、长期保留/GC 与业务网关接入；需要删除或修改时核实具体 UID 和权限边界。

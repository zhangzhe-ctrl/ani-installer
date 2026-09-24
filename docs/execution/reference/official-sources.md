# 来源与继承边界

本包首先是将上传源码审查与已选范围转换为执行计划。不是把全部上游版本重新更新一遍。下列“本次复核”只覆盖明确写出的语义；不等于本次完成对应镜像拉取、SHA采集、版本兼容或实机测试。

## 本地输入

- SRC-CODE：用户上传ani-installer.zip，SHA256 `2a4c9cc54becab39bb1c5c9f204ecb567ac9b0e90336dfa13a625994308269b3`，HEAD `c7a97bb508b699d5117db625da2fa4a8155b838f`；路径摘录以此快照为准。
- SRC-AUDIT：ANI-installer-code-audit-20260924.md，15项A01–A15及测试限制。R卡为本次设计，不冒充原审查已经完成整改。
- SRC-MATRIX：ANI-installer-version-matrix-20260923.md/.yaml，设计版本而非可执行锁。完整来源S01–S57保留于06登记；本包没有重新拉取96项物料。
- SRC-HTML：设计ANI离线安装器方案离线网页（2026-09-23保存），末期KubeKey、Fedora、三台测试机、快照仅lab、代码/物料分离等约束。后续聊天的Envoy隔离优先。
- SRC-ANI：原ANI.zip及前次基础设施差异审查，用于解释需支持的能力；本包不宣称修改或重验ANI应用。

## 本次针对性官方复核

- WEB-KF：Kubeflow26.03官方版本矩阵，五组件版本与Kubernetes门槛。https://www.kubeflow.org/docs/kubeflow-distribution/releases/kubeflow-26.03/
- WEB-NET：Kube-OVN1.16.x LB文档，Multus/macvlan/VPC NAT依赖、默认关闭、默认VPC简化范围。https://kubeovn.github.io/docs/v1.16.x/en/guide/loadbalancer-service/
- WEB-NOTEBOOK：v1.10.0 Notebook Controller manager配置，USE_ISTIO配置入口。https://raw.githubusercontent.com/kubeflow/kubeflow/v1.10.0/components/notebook-controller/config/manager/manager.yaml
- WEB-JOBSET：Trainer2.1.0固定JobSet0.10.1来源。https://raw.githubusercontent.com/kubeflow/trainer/v2.1.0/manifests/third-party/jobset/kustomization.yaml
- WEB-KSERVE：KServe0.16.0资源Chart，Standard/disableIngressCreation及默认ServingRuntime选择。https://raw.githubusercontent.com/kserve/kserve/v0.16.0/charts/kserve-resources/values.yaml
- WEB-KFP：KFP2.16.0 standalone资源依赖及官方独立安装安全边界。https://raw.githubusercontent.com/kubeflow/pipelines/2.16.0/manifests/kustomize/env/platform-agnostic/kustomization.yaml ；https://www.kubeflow.org/docs/components/pipelines/operator-guides/installation/
- WEB-ROOK-SNAPSHOT：Rook1.20.7 RBD SnapshotClass字段与CSI凭据引用。https://raw.githubusercontent.com/rook/rook/v1.20.7/deploy/examples/csi/rbd/snapshotclass.yaml
- WEB-HELM：Helm3.20.0 install命令源码，atomic失败删除及take-ownership/dependency-update参数；不用Helm4的变更语义替代。https://raw.githubusercontent.com/helm/helm/v3.20.0/cmd/helm/install.go

## 仅继承的版本来源

WEB-MULTUS及任务卡中SRC-MATRIX:Sxx引用均指原版本矩阵的固定来源。M阶段需要按该版本实际获取并保存hash；未在上面的“本次复核”中列出的项目，不宣称本次重新验证官方发行信息。

## 明确由本计划新增的设计决定

R/B任务划分、拟新增CLI、run.json/TaskResult字段、退出码、存储白名单、一次任务范围、局部测试名字、人工流程和加速快照纪律，均为本次实施方案。官方材料提供技术依据，不表示上游要求完全照本包的路径/字段组织。

副本与引用中撤销“kcn Envoy以后解耦为公共入口”；业务Envoy保持独立/暂缓。HAProxy/kube-vip在当前local控制面路径标unused；因此长期维护目标不直接成为当前实机升级任务。Kube-OVN enable-lb-svc=false不当作上游bug。

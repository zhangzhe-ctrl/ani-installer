# ani/kubeovn（network.stack: kubeovn 二选一路径）

自研网络批次（kcn CNI + envoy-gateway 家族 + smoke）之外的第二选择。由站点配置
`network.stack: kubeovn` 启用（`network.stack: kcn` 或缺省 = 自研批次，见
`create_cluster.yaml` 的 role when 门控）。

## 材料状态（2026-09-22：Kube-OVN v1.16.6 材料已就位并集成）

- `templates/kubeovn-install.yaml`：真实上游清单（CRD + SA + ovn + ovs-ovn +
  workloads + pinger，共 8 段文档），重写自上游 one-step 安装器。v1.16.6 无独立
  kube-ovn.yaml 下载（raw 404），清单内嵌于 install.sh heredoc，供料阶段已按
  默认变量展开；官方 sed 变量点改为 Go template 注入：
  - `--default-cidr` -> `.ani.network.pod_cidr`
  - `--service-cluster-ip-range`（controller 与 cni-server 两处）-> `.ani.network.service_cidr`
  - `--iface` / `--default-interface-name` -> `.ani.network.management_interface`
  - 全部 image 引用 -> `{{ index .ani.images "<original>" }}`
  其余变量保持 install.sh v1.16.6 默认值（JOIN_CIDR=100.64.0.0/16、
  POD_GATEWAY=10.16.0.1、geneve、ENABLE_LB/NP/METRICS=true 等），见模板头部注释。
- `kubekey/ani/images-kubeovn.tsv`：已填行，2 个镜像
  （kubeovn/kube-ovn:v1.16.6、kubeovn/vpc-nat-gateway:v1.16.6），amd64 RepoDigest
  来自供料阶段 skopeo 实测。默认拉取路径直达（docker.io 官方），无 tar 注入。
- `tasks/main.yaml`：等待列表已按真实清单核对——6 个工作负载全部在
  **kube-system**：Deployment/ovn-central、DaemonSet/ovs-ovn、
  DaemonSet/kube-ovn-cni、Deployment/kube-ovn-controller、
  Deployment/kube-ovn-monitor、DaemonSet/kube-ovn-pinger。
- 宿主内核：install.sh v1.16.6 与现行官方文档均不再要求 rp_filter/sysctl 调整
  （旧文档要求已移除），故本 role 未加 sysctl 步骤；前置条件为内核模块
  （geneve/openvswitch/ip_tables/iptable_nat）与 IPv6 启用。
- `ani/components.lock.yaml`：base.kubeovn = v1.16.6（not_verified，尚未真实安装实证）。

## 离线打包

`IMAGES_TSV=$ROOT/ani/images-kubeovn.tsv bash scripts/build-offline.sh`
（build-offline.sh 已支持 IMAGES_TSV 覆盖，无需改脚本）。

## 与自研批次的边界

- smoke role（envoy 网关冒烟）属自研批次，kubeovn 栈下不安装。
- `network.kcn.*` 子段在 kubeovn 栈下被忽略（校验也跳过），旧站点文件无需删改即可切换。

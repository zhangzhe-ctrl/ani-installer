# kcn v0.6.2 → dev 升级部署修改方案（2026-09-20）

> 材料：`install-dev.yaml`（上游 dev 安装清单）+ `5a8cf98f….tar`（OCI 多架构镜像包，
> index digest `sha256:5a8cf98fcccf118ca80990a6310773f225e7b2b999f2068a18999bca563f30a5`，
> 文件 sha256 `dc4f1129…`；amd64 + arm64 + 2 个 attestation manifest）。
> 对比基准：`kubekey/builtin/core/roles/ani/kcn/templates/install.yaml`（v0.6.2，30 段）。
> 结构化 diff 结论：**30 段 vs 30 段，kind/name 序列完全对齐**，差异集中在 10 段。

## 1. 上游变化清单（v0.6.2 → dev）

| 段 | 变化 |
|---|---|
| eips CRD、vnics CRD | schema 新增 `qos`（bandwidth / ingress.maxRate / egress.maxRate，0–102400 Mbps），纯新增 +33 行 |
| ConfigMap kcn-config | 上游硬编码 `encapNetworks: 33.3.14.0/23`、8 个 `intranetNetworks` 网段、`managedDevices: ""`——**这 3 个字段在 v0.6.2 模板是 installer 注入点** |
| Service ×3 | **改名**：`ovn-nb`→`kcn-ovn-nb`、`ovn-northd`→`kcn-ovn-northd`、`ovn-sb`→`kcn-ovn-sb` |
| kcn-controller Deployment | 主容器 imagePullPolicy `IfNotPresent`→`Always`；image→dev；OVN_DB_IPS/`--service-cluster-ip-range` 上游硬编码站点值 |
| kcn-ovn-central Deployment | **command 大幅简化（-18 行）**：v0.6.2 模板里的 bash 内联 leader-checker 双进程+信号转发脚本消失，改为直跑 `/kc-networking/start-db.sh`——ANI 的「KCN leader fix」已被上游化进镜像；主容器 policy→Always |
| kcn-cni-ds DaemonSet | `install-cni` init 容器从最后移到最前（启动时序变化）；主容器 policy→Always |
| kcn-ovs-ds DaemonSet | image→dev；OVN_DB_IPS 硬编码 |
| 全部 8 处 image | `kc-networking:v0.6.2` → `kc-networking:dev` |

## 2. 待执行的代码修改（需确认）

### A. role 模板 `builtin/core/roles/ani/kcn/templates/install.yaml` 重写
以 install-dev.yaml 为底本，但**必须恢复 6 个注入点**（不能照抄上游硬编码，否则
离线实验室拓扑错乱）：
1. `encapNetworks: {{ .ani.network.kcn.encapNetworks | join "," }}`
2. `intranetNetworks: |` + `{{ range .ani.network.kcn.intranetNetworks }}    - {{ . }}\n{{ end }}`
3. `managedDevices: {{ .ani.network.kcn.managedDevices | join "," }}`
4. controller Deployment + cni-ds：`- --service-cluster-ip-range={{ .ani.network.service_cidr }}`
5. controller Deployment `OVN_DB_IPS`、ovn-central Deployment `NODE_IPS`、ovs-ds
   `OVN_DB_IPS`：`value: {{ .ani.node_addresses | join "," }}`

其余照抄新版：CRD qos schema、Service 改名、ovn-central 简化 command、cni-ds 容器顺序。

**决策点 1 — imagePullPolicy**：新版主容器是 `Always`（4 处），旧模板 `IfNotPresent`。
推荐**保持 IfNotPresent**：离线环境 kubelet 每次都打节点本地 hauler registry，Always
把 registry 变成 pod 启动的运行时依赖（registry 重启/未就绪则 pod 起不来）；v0.6.2
即 IfNotPresent 且工作正常。偏离上游记模板注释。（若你要与上游完全一致选 Always，
安装期 verifyRegistryImages 也能兜底，但运行时风险自担。）

### B. tasks/main.yaml
- 版本注释 v0.6.2 → dev；等待列表不变（4 个工作负载名两版一致，已验证）。
- **新增旧 Service 清理**（改名残留）：apply 前
  `kubectl -n kcn-system delete service/ovn-nb service/ovn-northd service/ovn-sb --ignore-not-found`。

### C. images.tsv（人工编辑，脚本禁写）
加一行（4 列制表符分隔）：
```
docker.changqingyun.cn/kubercloud/kc-networking:dev	127.0.0.1:5000/kubercloud/kc-networking:dev	sha256:5a8cf98fcccf118ca80990a6310773f225e7b2b999f2068a18999bca563f30a5	kcn dev fix (K-5 netns rebuild fix, injected from local tar)
```
digest 口径说明：文件名 = OCI index digest；build-offline.sh 与安装侧均不消费该列
（记录性），安装期校验走 hauler_ref 的 HTTP manifest 探测。

**决策点 2 — v0.6.2 行去留**：推荐**删除**（模板升级后不再引用；留 = artifact 重打时
多打 ~200MB 旧镜像；回滚走 git 历史）。

### D. components.lock.yaml
- `base.kcn: v0.6.2` → dev 锁定描述（含 index digest，注明「本地 tar 注入，
  2026-09-20 用户提供」）。
- 211 行注释「新版 kcn 材料未提供…」已过时，更新。

### E. template_test.go 断言反转（方向性修改，共 3 组）
- `want`：`name: ovn-nb/ovn-northd/ovn-sb` → `name: kcn-ovn-nb/kcn-ovn-northd/kcn-ovn-sb`。
- `want`：`start-db.sh &` + leader-checker 两行 → `- /kc-networking/start-db.sh`
  （leader fix 上游化，测试语义从「断言本地修复」改为「断言上游简化 command」）。
- `stale`：`name: kcn-ovn-nb` ×3 → 裸名 `ovn-nb` ×3（与上次方向相反，注释说明 dev 反转）。
- `stale`：`33.3.1.201/202/203`、`33.3.64.0/19` 保留；**新增** `33.3.14.0/23`、
  `33.3.96.0/19`、`192.1.1.0/24`（新版 ConfigMap 硬编码网段，注入后不得出现）。

### F. 已完成（本轮，无需重做）
1. 镜像包已 scp 至 fedora `~/ani-installer-runs/kcn-fix-20260920/inputs/`，
   sha256 与本地一致（`dc4f1129…`）。
2. fedora docker load ✓（`docker.changqingyun.cn/kubercloud/kc-networking:dev`，
   842MB 解压）→ `docker tag kubercloud/kc-networking:dev`（hauler 剥前缀形状）→
   `docker save` 生成 **`kc-networking-dev-registry-named.tar`**（220MB，
   sha256 `25d90a9f…`，RepoTags 已实证 = `kubercloud/kc-networking:dev`）。
3. `scripts/build-offline.sh` 扩展 **EXTRA_IMAGE_TARS / EXTRA_IMAGE_ORIGINALS**
   （本地 docker-archive 注入：1:1 条数校验、tsv 行存在性校验、拉取循环跳过、
   `hauler store load` 装载；与 HAULER_ARCHIVE/HAULER_STORE 直通路径互斥）。
   已同步 fedora obs-fix src，bash -n 通过。本地与 fedora 同一份编辑。

## 3. 制包与验证链（代码修改确认后执行）
1. `go test ./pkg/ani/`（模板渲染断言 + 全包回归）。
2. artifact 重打（C0 基线退役，新 artifact 含 kcn dev 镜像）：
   ```bash
   EXTRA_IMAGE_TARS=~/ani-installer-runs/kcn-fix-20260920/inputs/kc-networking-dev-registry-named.tar \
   EXTRA_IMAGE_ORIGINALS="docker.changqingyun.cn/kubercloud/kc-networking:dev" \
   bash scripts/build-offline.sh   # 其余 env 同 C0 打包流程
   ```
3. code 包（模板+tsv+lock+测试改动，可与 a27fix 合并为一个包）。
4. 快照还原 → 三台安装 → kcn 四工作负载健康（controller/ovn-central/cni-ds/ovs-ds）。
5. **K-5 修复实证**：重建 pod 多轮，验证坏 netns（OVS anti-spoof 残留）是否真的
   修复——这是本次升级的核心目的。
6. 通过后解锁整批 `not_verified` 的 live 验证（H4 a28 发射、H5）；K-5 恢复链
   （k5_read_again 等）保留为防御层，预期不再触发。

## 4. 风险与回滚
- 回滚锚点：git 历史（模板/tsv/lock/测试均可 checkout 恢复）+ C0 artifact 基线保留。
- Service 改名：安装期清理步骤（§2B）防止裸名残留被误用。
- leader fix 上游化：信任上游 dev 镜像内 start-db.sh；K-5 复现实验（§3.5）同时
  间接验证 ovn-central 在重建风暴下的行为。
- dev 是浮动 tag：以 index digest 锁定（lock + tsv 记录），实际分发走本地 tar，
  不依赖远端 tag 指向不变。

## 5. 镜像 tar 陷阱与修复实录（2026-09-20 制包阶段）

vendor 供料 tar（文件名 5a8cf98f…，即 OCI index digest）无法直接 `hauler store load`，
本轮实测两条死路 + 一条修复路：

1. **vendor 原 tar**：OCI index 里 4 个 manifest（amd64 + arm64 + 2 attestation），
   但 tar 只打包了 amd64 与 amd64-attestation 的 blob，**arm64 manifest（ad0c55b1…）
   与另一 attestation blob（684aeafc…）缺失**。hauler 展开嵌套 index 时
   `failed to fetch manifest: …/ad0c55b1…: no such file or directory`。
2. **docker save 重新导出无效**：docker（containerd image store）load 后 save，
   输出 tar 与上午 registry-named.tar sha256 完全相同（25d90a9f…）——save 保留了
   嵌套 index 结构，且 hauler store load **只接受 OCI layout**（无 index.json 的纯
   docker-archive 直接报 `open …/index.json: no such file or directory`）。
3. **修复（已验证）**：以 tar 内完整的 amd64 manifest blob（048cafa8…，与 docker
   manifest.json config+7 层逐一比对一致）为唯一条目重建 OCI layout：
   - index.json 单条目直指 048cafa8，annotations
     `org.opencontainers.image.ref.name = kubercloud/kc-networking:dev`（**必须写完整
     hauler 形名**，vendor 原 index 的 ref.name 只有 dev）
   - 保留 9 个 blob（manifest + config + 7 层，逐一 sha256 自校验），剔除嵌套
     index/attestation/docker manifest.json
   - 产物 `kcn-fix-20260920/inputs/kc-networking-dev-oci.tar`（sha256 ec604362c63a8009…）
   - `hauler store load` LOAD_OK，store index 中 dev 全名 1 次、v0.6.2 遗留 2 次
     （b4 store 原有，tsv 已删行，无运行时消费）

诊断脚本存于 fedora `kcn-fix-20260920/`：inspect_image_tar.py、
diagnose_nested_index.py、check_amd64_manifest.py、rebuild_docker_tar.py、
build_oci_tar.py（最后一个是修复本体）。

## 6. 制包决策（2026-09-20 实际采用）

方案 §3 原计划 EXTRA_IMAGE_TARS 注入；实际改走 **C0 同款 HAULER_STORE 直通**：
store = hauler-store-b4 副本 + 上述 OCI tar load。理由：默认拉取路径需 51 个
original 全部联网可达（dev 上游不可达恰是供 tar 的原因），而 store load 与
EXTRA_IMAGE_TARS 循环体执行同一 hauler 原语、路径已验证。EXTRA_IMAGE_TARS
机制保留在 build-offline.sh 中，留给未来无 store 可复用的冷启动场景首航。

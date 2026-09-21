# 可观测性批次 H4/H5 交接文档（2026-09-20）

> 交接时刻：2026-09-20 上午（北京时间），11:40 复核刷新。写此文档时用户已叫停发射
> 流程，交接人接手后从 §0「接下来做」继续即可。所有命令均在 fedora 上执行（唯一可经
> VPN 访问 172.16.101.x 并操作 ESXi 172.16.255.12 的机器）。

---

## 0. 速览：暂停时正在做什么、接下来做什么

**暂停时刻正在做的**：H4（loki-cumulative 全链真实安装验收）收官推进。a27 轮已真实
通过 metrics [1]–[8/8]、Loki [1]–[5]、fluent-bit [1][2]，死于 [3] 替换断言，定性为
A23（backend 是 StatefulSet，pod 名重建后恒为 `ani-loki-0`，名字比较断言结构上永假）。
**A23 修复已完成并通过全部离线验证**（§5），正要执行的动作是打包 a27fix code 包——
在打包前被叫停。此后未做任何动作，状态经复核零漂移。

**接下来按顺序做**（命令级步骤见 §6）：
1. 打包 `ani-code-a27fix-20260919`（源码就绪，一条命令）+ 记录 kk sha256 前 16 位；
2. 双 releases 目录同步（obs-fix → obs-live，坑 #21）；
3. 快照还原（独立步骤，~10-13 分钟，脚本 3/3 自验证；会覆盖三台节点上的 a27 残留集群）；
4. 发射 `h4loki-a28 loki-cumulative`（run-attempt 内置隔离/preplock/对时/传输/安装）；
5. collect 判定：fluent-bit [3]（A23 修复复验）→ [4] cursor/inspector → [5] 排他断言，
   全过 = **H4 收官**；中途再败则按 FAIL 行 + k5-retries.txt 定性 → A24+ 修复 →
   a29fix 包 → 新前缀发射，循环到过；
6. H4 收官后启动 H5（opensearch-cumulative，同 code 包，预检已过）。

---

## 1. 任务全景与当前位置

- 批次底座 B0–B5（除 B5 交付收尾）与可观测性 C0–C5 code 阶段均已完成并推送 origin/main。
- 当前处于**第二批 live 验收收官**：
  - **H4 = loki-cumulative 全链真实安装验收**——差最后一步（见 §3 进度矩阵）。
  - **H5 = opensearch-cumulative**——H4 收官后启动，同 code 包，预检已完成
    （opensearch verify 无 A21 同类雷、OS 凭据上传模式正确）。
- 验收方式：快照还原 → 离线隔离 → 三台安装 → 组件 verify 全链真实通过。
  verify 分段断言在安装后于 installer 节点执行，任何一段 fail 即整轮失败。

## 2. 当前精确状态（截至交接）

| 项 | 状态 |
|---|---|
| A23 修复（fluent-bit [3] 替换断言改 UID 比较） | **已完成**：本地 `kubekey/builtin/core/roles/ani/fluent-bit/templates/verify.sh`（未提交）与 fedora `obs-fix` src 已同步，渲染门禁 + bash -n + go test 全过（§5 验证记录） |
| a27fix code 包 | **未打包**（源码就绪，§6 步骤 1 一条命令） |
| 快照还原 / a28 发射 | **未做**（用户叫停）。三台节点上仍是 a27 残留集群（k8s 在跑、fluent-bit [3] 断言失败现场），下次发射前 restore 会覆盖，无需处理 |
| a27 结论 | metrics verify [1]–[8/8] 真实全过；Loki [1]–[5] 真实全过；fluent-bit [1][2] 真实全过；**死于 [3] 替换断言 = A23（结构性假断言，非环境问题）** |
| A1–A23 修复 | 全部**未提交**（用户从未要求提交） |

## 3. H4 验收进度矩阵（三态：code pass / 真实安装 pass / 从未执行到）

| verify 段 | 状态 | 备注 |
|---|---|---|
| metrics [1]–[6] | 真实安装 pass（a27） | 含 Prometheus/Alertmanager 查询链 |
| metrics [7/8] range 查询、[8/8] silence 读回 | 真实安装 pass（a27） | A22 的 `k5_read_again` 恢复重入代码**从未被真实触发**（a27 一次过），其真实工作能力待后续 K-5 打击再证 |
| Loki [1]–[5] | 真实安装 pass（a27） | 采集链本体 a25 首次真实通过 |
| fluent-bit [1] [2] | 真实安装 pass（a27） | [2] = 采集 + 查询 + marker 断言 |
| fluent-bit [3] backend 重建恢复 | **A23 修复后待 a28 复验** | a27 证明替换真实发生（delete --wait 成功、新 pod Ready、PVC UID 不变），只是断言比较错对象 |
| fluent-bit [4] cursor/inspector、[5] 排他断言 | 从未执行到 | 已知残留风险：[3]/[4] 存在 post-Ready 盲窗（K-5 变体，§7） |

**H4 收官判据**：a28 中 fluent-bit [3] → [4] → [5] 全过（前段已证）即 H4 收官。
随即启动 H5：restore → `run-attempt.sh <新前缀> opensearch-cumulative` → collect 判定。

## 4. A20–A23 缺陷链摘要（A1–A19 见 docs/observability-components-status.md）

- **A20**：verify 模板用短镜像键 `busybox:1.37` 渲染成 nil → marker pod 全部
  InvalidImageName。修复 = 锁定全键 `docker.io/library/busybox:1.37.0`。教训：
  go 测试夹具 `TestFluentBitVerifyProvesCollectionPath` 滞留旧键两个包周期——
  **build-code.sh 不跑 go test**，改键必须手动 `go test ./pkg/ani/` 并 grep 夹具。
- **A21**：`check_metadata.py` 一函数三雷（pod 内执行却传宿主机路径；`sys.argv`
  切片错位吞入 NS；`rsplit("-n",1)` 切错 marker）。修复 = JSON 经
  `kubectl exec -i -- sh -c 'cat > /tmp/…'` 送进 pod + argv 切片 `[2:-1]/[-1]`
  + 正则 `-n(\d+)-` 锚定。验证手法：从 node1 拉回 a25 真实 markers 文件离线
  回放修复版 checker，RC=0 后才打包（没烧安装轮次）。
- **A22**：K-5 新变体——pod 重建后**短暂 Ready 然后 netns 死亡**（出向
  `no route to host`、入向黑洞、Service Endpoints 清空 → 查询必超时）。修复 =
  metrics verify 新增 `k5_read_again`（一次有界恢复重入：删 pod → k5_rebuild_wait
  → 重读），包装 [7/8][8/8] 两个读回点。
- **A23**（本次）：fluent-bit [3] 替换证明断言比较 **pod 名字**，backend 是
  StatefulSet，重建后名字恒为 `ani-loki-0` → 断言结构上永假（此前所有轮次都死在
  更早位置，该断言 a27 才首次真实执行）。修复 = 采集
  `old/new_backend_pod_uid`（`{.metadata.uid}`），断言改 UID 不等，注释记录
  a27 证据。**修复过程踩到一次「Edit 报成功但未落盘」的假成功**，靠渲染产物
  grep 复核抓回——关键编辑必须读回实证，勿信工具返回值。

## 5. A23 修复的离线验证记录（已全部通过，无需重做）

- **版本指纹**：A23 完整版 `verify.sh` md5 = `cd6ef0bcfe87579bf68c9b9d9c49bb36`
  （本地 `D:\Workspace\ani-installer` 与 fedora `obs-fix` src 一致，2026-09-20 11:40
  复核；接手时可用此值核对文件版本，防半截补丁）。
- fedora `obs-fix` src：`bash -n` OK；`grep -c new_backend_pod_uid` = 3。
- 全量渲染门禁（3 角色 metrics/loki/fluent-bit × 3 类模板 values/tasks/verify ×
  2 后端 loki/opensearch = 18 项）全过：GATES_RC=0；6 份渲染 verify 全部
  valid bash；fluent-bit 渲染产物含 A23 键（3 次/份）与 A21/A22 键。
- `cd ~/ani-installer-runs/obs-fix-20260919/src/kubekey && GOCACHE=… gocache
  go test ./pkg/ani/` → ok（2.3s）。
- 门禁脚本调用防呆（踩过）：输出文件扩展名——values/tasks 必须 `.yaml`、
  verify 必须 `.sh`，否则被拒。渲染门禁三副本（c2/c3/c4）md5 一致
  （6b5ffb529fa5857ac7c10728bc30185e）。

## 6. 接手操作手册（照抄即可，全在 fedora）

```bash
ssh fedora

# [1] 打包 a27fix（输出目录必须不存在）
cd ~/ani-installer-runs/obs-fix-20260919/src/kubekey
ANI_CODE_OUT=~/ani-installer-runs/obs-fix-20260919/releases/ani-code-a27fix-20260919 \
  bash scripts/build-code.sh
sha256sum ~/ani-installer-runs/obs-fix-20260919/releases/ani-code-a27fix-20260919/kk
#    ↑ 取前 16 位记入 status/progress（包链：a24fix cce36749aa74d171 →
#      a25fix c5e184e5fab9627c → a26fix dff7cfe8f834643f → a27fix <待记>）

# [2] 双 releases 同步（坑 #21：obs-fix=构建源，obs-live=发射载体）
cp -a ~/ani-installer-runs/obs-fix-20260919/releases/ani-code-a27fix-20260919 \
      ~/ani-installer-runs/obs-live-20260919/releases/

# [3] 快照还原（独立步骤，脚本自身会 3/3 验证；~10-13 分钟，会重启三节点）
bash ~/ani-ops/restore_esxi_snapshots.sh   # 具体脚本名以 ~/ani-ops/ 实际为准

# [4] 发射 a28（run-attempt 内置：隔离施加+验证 → preplock×3 → 对时 →
#     传输 code+artifact → sha 校验 → site 配置 → 分离式安装）
cd ~/ani-installer-runs/obs-live-20260919
CODE_RELEASE=ani-code-a27fix-20260919 bash lab/run-attempt.sh h4loki-a28 loki-cumulative
#    evidence prefix 不能复用旧前缀（脚本自带防呆）；artifact 不重打，
#    恒用 C0 基线 ani-artifact-obs（kk 记录 351877e8…）。

# [5] 轮询与收证（用法以各脚本头部为准）
bash lab/poll-attempt.sh h4loki-a28
bash lab/collect.sh h4loki-a28

# [6] 判定：看 a28 verify 日志 fluent-bit [3]→[4]→[5]；
#     全过 = H4 收官 → 立即按 §3 启动 H5（restore + opensearch-cumulative）。
#     若 [4]/[5] 暴露新缺陷：按 FAIL 行 + k5-retries.txt 定性 → A24+ 修复 →
#     门禁/测试 → a29fix 包 → 新前缀发射，阻塞式推进直到批次完成。
```

发射后单轮耗时参考：restore ~10-13 分钟 + 编排/3GB 传输 ~3-5 分钟 + 安装本体
（失败点越深越久：a25 22 分 / a26 21 分 / a27 38 分）+ K-5 恢复链有界等待
600+420+600s。

## 7. 已知残留风险与未决事项

1. **K-5 根因未定位**：kcn/OVN Pod 重建后偶发坏 netns，已见两种形态——
   从未 Ready（a12/a17）与 post-Ready 死亡（a26）。诚实记录于
   `docs/foundation-components-status.md` 附录 A。fluent-bit [3]/[4] 若在 a28
   踩 post-Ready 盲窗，可将 metrics 的 `k5_read_again` 同型加固移植（现为未做状态）。
2. **kcn 新版材料**：用户口头告知已修但未提供镜像地址 → 相关 live 验证保持
   `not_verified`，不启动三台安装、不补造地址、不改旧 kcn。整批通过需两条日志
   组合分别真实验证，不许「豁免后全绿」。
3. **k5_rebuild_wait 职责边界**（保持有界恢复 vs 退化纯 fail-loud）——待用户决策，
   现行按有界恢复推进。
4. A1–A23 修复全部**未提交**；仓库历史文件（`restore_esxi_snapshots.sh`、
   `docs/*execution-plan*`）含明文凭证，待单独清理。
5. B2 遗留底座阻塞 K-5 根因分析（附录 A）。

## 8. 接手必须遵守的约束

- **不自动提交/推送/发布**——用户明确要求时才做。
- 只操作 172.16.101.20/.21/.22 三台白名单节点（+ESXi 快照还原）；一切经
  `ssh fedora`；本地只编辑/读文件与发起传输。
- 禁止改 kcn 组件源码、建热修镜像、吞错误；installer 侧只做 fail-loud 验证。
- 状态三态记录：code pass / 真实安装 pass / not_verified，每卡写
  `docs/observability-components-status.md`，长条目写 `kubekey/docs/progress.md`。
- 本批只用任务私有工作区 `~/ani-installer-runs/obs-fix-20260919/`（构建源）与
  `~/ani-installer-runs/obs-live-20260919/`（发射载体）；凭据只在
  `platform-20260918/access/`，勿入仓库。
- `kubekey/ani/images.tsv` 是打包物料，任何脚本不得写它。
- fedora src 不是 git 仓库，改动前确认本地 `D:\Workspace\ani-installer` 有完好副本。

## 9. 反复踩过的坑（接手前必读，全量版见项目记忆）

1. 快照还原会重启节点 → 临时 ANI-OFFLINE iptables 规则被清空 → 每次还原后必须
   重新施加并验证隔离（run-attempt 已内置，勿跳过）。
2. 快照重启后 unattended-upgrades 占 dpkg 锁 → preplock×3（run-attempt 已内置）。
3. 离线隔离必须用 iptables OUTPUT/FORWARD（ANI-OFFLINE），不能退化成 blackhole
   路由（kubeadm 因 ifindex 0 报错）。
4. role 的 `tasks/main.yaml` 必须进渲染门禁（TEMPLATE_KIND=tasks）；渲染上下文
   用 installer 真键集 + `pkg/converter/tmpl.FuncMap()` 真函数表；三副本
   （c2/c3/c4）改一必须同步三。
5. build-code.sh 不跑 go test；改 verify/模板后手动 go test + grep 测试夹具键。
6. 测试夹具必须覆盖 role 读的全部键；断言不与模板字面量耦合。
7. Helm Chart 镜像字段契约（kube-prometheus-stack 分三段 / fluent-bit 无
   registry 字段 / opensearch global.dockerRegistry）只能逐 Chart 渲染实测，勿类推。
8. `resource.ParseQuantity` 不接受 `GB`，容量一律 `4Gi` 形态。
9. Prometheus/Alertmanager 的 STS 由 Operator 运行时生成；渲染门禁不能断言
   Chart 渲染的 STS；`--thanos-default-base-image` 是禁用功能默认参数，审计豁免。
10. OpenSearch PVC 真名 `<claim template>-<sts>-0`，运行时从渲染后 STS jsonpath
    推导，不写死；Basic 后端字段 `type: intern`（写 internal 静默不生效）；
    bcrypt hash 在节点上算，内联 security-config.yaml 由 securityadmin.sh 播种。
11. Fluent Bit verify 里 LogQL 的 `{`/`}` 用 `chr(123)/chr(125)` 拼；role 模板里
    `{{ .Values.* }}` 永不解析（installer 是 text/template，Helm 不跑第二遍）。
12. 长行（>2000 字符）记忆/文档无法用行尾锚编辑 → python 按行首前缀定位整行追加。
13. **关键编辑后必须读回/grep 实证**——本次 A23 第一遍补丁「报成功未落盘」，
    靠渲染产物键复核抓回。

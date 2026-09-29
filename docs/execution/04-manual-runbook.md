# 04｜人工操作手册：从一张任务卡到一次可信验收

## 2026-09-29｜Kube-OVN + 可选 RustFS 正式交付结果

**本次实际结果：**代码提交 `4003b12bea4011e82b28ffe67c35ceb276a54eb7` 在 Fedora 的同源 `build-code.sh` 自带完整 gate rc=0，[GitHub CI run 36546382274](https://github.com/zhangzhe-ctrl/ani-installer/actions/runs/36546382274) 为 success。正式代码包 `/home/chabking/ani-installer-runs/kubeovn-rustfs/release/code-4003b12-candidate` 与累计材料包 `/home/chabking/ani-installer-runs/kubeovn-rustfs/release/artifact-c165ae7-candidate-r2` 的 `SHA256SUMS` 文件摘要分别为 `c95a1dc84158152406ff0b4a29ca316ebb35bf6f09861af4c0ace2079ddba304`、`441f3bc8c4f081a8e660ce35fc3eaa5d05aaf257d3721f18b2b977692e020640`；这是清单文件摘要，不代替逐项核验。获准 installerNode `ani-01`（172.16.101.10）上两份清单逐项校验、完整 site `validate/render` 均 rc=0；site 文件在 Fedora 私有目录 `/home/chabking/ani-installer-runs/kubeovn-rustfs/site/site-kubeovn-rustfs-milvus.private.yaml`，其 SHA-256 为 `d916a3840fb5ebfc394dcb0428653c1038ce6c06af4f6e87c7af86916080c20c`，不得入 Git。

正式环境为 ESXi VMID 8/9/10 对应 `ani-01/02/03`（172.16.101.10–12）。先在保留的失败集群完成 KubeVirt 同名源 Pod/VM 的原因定位、现场修复验证和全部后续组件诊断，才把三台从 `iso-install-complete` Id2 干净快照还原。旧 kcn/RGW 集群仅保留在 `pre-kubeovn-rustfs-20260929` Id3 冷快照；没有在线迁移对象、在线切主 CNI 或删除旧桶/池/PVC。三台干净目标无旧 `admin.conf`、`/dev/sdb` 无文件系统；离线规则阻断公网 TCP，私有 registry/API 可达。

| 执行阶段 | 真实结果 |
| --- | --- |
| 开发基线 `objectStorage=none`、Milvus 关闭 | 首装 `ani-ani-lab-20260929-095835` rc=0，正式 `install-success`；底座 smoke rc=0 |
| 正式新增 none→RustFS→Milvus | 两个新增执行记录均 `operation=add,didInstall=true`，各自 smoke rc=0；Milvus 应用身份完成 S3 Put/Get 与 collection→insert→flush→index→load→search，`nearest_id=101`，每轮新对象数 7，provider=rustfs |
| 新增后回归与重复操作 | 底座全项 smoke、Ceph RBD/CephFS 再次创建卷并写读 rc=0；重复计划与执行 `no-op=true,didInstall=false`，RustFS/Milvus 关键资源 UID 不变 |
| 全集干净离线首装 | 正式包在重新还原的 `ani-01` 上一次首装 rc=0，run `ani-ani-lab-20260929-111355`，718 total / 708 success / 10 ignored / 0 failed；Kube-OVN、Multus、Ceph RBD/CephFS、RustFS、Milvus 和其余已选 B00–B07 均按完整 site 安装 |
| 全集基本功能和 noop | 14 个启用组件 smoke 全 pass；RustFS/Milvus 再次真实验证，重复计划与执行 `no-op=true,didInstall=false`，Deployment、StatefulSet、PVC、Secret UID 不变 |
| 网络与离线 | 最终集群 2 个跨节点对各 4/4 通过 Pod IP、Service IP、DNS；三节点公网 TCP 阻断、私有 registry/API 可达；Harbor HTTPS NodePort 30003 经内部 CA 校验均返回 200 |

**证据位置与身份：**Fedora 仓外私有目录 `/home/chabking/ani-installer-runs/kubeovn-rustfs/` 保存原始配置、日志和材料。阶段新增证据包 `site/formal-add-4003b12/staged-add-evidence.tar.gz` SHA-256 `959d07bb416f83d3b7fdc586d3f6659be035ddd4ff2dbfe6488fe25c8e6615a3`，对应产品记录包 `staged-add-run-ledger.tar.gz` 为 `b3127f50e096e1f51d98f6f46909d946fd009b285c585f074869d59cd9e9f690`。最终全集证据包 `site/final-full-4003b12/full-install-evidence.tar.gz` 为 `948fe7be03a1e48e3894fe4197392fd7400cd85ff3ff3f89be6a78ab44a915bd`，对应产品记录包 `full-install-run-ledger.tar.gz` 为 `e7c25f0fd5c47959564b47af200ea9d1228060091eccededd816e76281470d53`。旧 RGW 独立回归证据 `integration/rgw/evidence/rgw-evidence.tar.gz` 为 `30fd938295fd6b26ef050c4c867a6fba9bf4ff9130ba66cc984d42351ef6f4c4`。这些原包含私有信息，不复制到仓库。

**限制：**最终 CephCluster `Ready` 但 `HEALTH_WARN`，仅有四个 CSI 客户端 AES 密钥与允许旧 cipher 的三项认证告警。节点内核为 6.8；[Rook 的 CephX 说明](https://rook.io/docs/rook/latest/Storage-Configuration/Advanced/cephx-key-rotation/)指出 CSI 的 AES256K 内核挂载需 7.0+，因此未现场轮换密钥、收紧 cipher 或静音告警。RBD/CephFS 创建、写读和快照恢复实测通过，但不宣称 Ceph `HEALTH_OK` 或安全认证。RGW 模板、材料与选项保留；本次 RustFS 分支未创建 CephObjectStore。kcn+RustFS、kubeovn+RGW 只完成静态选择回归，不冒充四组合实机认证；未进入 B08–B12。

当前 `.10–.12` 是已安装的完整集群，**不得重放 `install.sh`**。复核时以 `/var/lib/ani-installer/ani-lab/run.json` 的 `install-success` 记录运行 `kk ani verify --run ... --level smoke`；新增必须另按计划、执行、执行记录 smoke 的顺序走，并拒绝已有使用方的 provider/endpoint/bucket/rootPath 迁移。未来在另一获准干净目标重用本包时，先核对目标身份、site、两份 `SHA256SUMS`、材料锁及离线条件，再从正式包首装；不能借开发目录或公网补料。

## 2026-09-28｜B00–B07 累计包与 B13 本阶段预检（尚未首装）

本节只覆盖 B00–B07 的 kcn+Ceph+基础服务+Multus+Milvus+Metrics Server+CSI Snapshot+KubeVirt/CDI+Volcano+Harbor 全选组合。B01b 外部 LB 未选，Kube-OVN 是另一个主 CNI 方案；B08–B12 未实施，完整 B13 未完成。详情与摘要见 [本阶段状态](evidence/B13-stage-20260928/status.md)。下方 2026-09-27 的首装是**旧底座候选**的历史结果，不能作本轮新增或最终首装证据。

Fedora 上最终代码包为 `/home/chabking/ani-installer-runs/b00-b07/code-b00-b07-harbor-ci-fix`，正式累计材料包为 `/home/chabking/ani-installer-runs/b00-b07/artifact-b00-b07-4d98b3f-r3`，私有完整 site 为 `/home/chabking/ani-installer-runs/b00-b07/site-b00-b07-full.private.yaml`（0600）。代码提交 `1d00b0688bad8ffa4a5c8f370a1662aa4a231b67`。代码与材料两个 `SHA256SUMS` 均 rc=0；正式包的 `kk ani validate` 与 `kk ani render` 均 rc=0，render 没有开发目录覆盖。它们仅证明静态输入；B00–B07 新增功能和 B13 干净首装均 **not_run**。

**现场停点：**三台现有目标的 Ceph 数据盘各 50 GiB、三副本，原始容量 150 GiB；所选 PVC 声明合计 72 GiB，满额需要至少 216 GiB raw，差额至少 66 GiB raw（另需 Ceph 开销和快照余量）。三节点均无有效 NTP 同步源，现有 Ceph 有 MON_CLOCK_SKEW、CSI 认证类型和 PG 告警。容量与离线时间前置未满足，未恢复快照、未清盘、未向 node1 传包或运行安装；不能通过缩小选项/副本或用旧集群补装来冒充本阶段完整首装。只读命令和退出码在 Fedora 私有的 `b13-readonly-preflight-20260928.txt`，容量计算在 `b13-capacity-preflight-20260928.md`。

具备声明容量与时间源、并确认原始干净目标和安装权限后，先把上述**完整**代码包、材料包及私有 site 安全放到现场确认的 installerNode（不是 Fedora）。在该 installerNode 上以实际绝对路径设 `CODE_ROOT`、`ARTIFACT_DIR`、`SITE_FILE`、`EVIDENCE`，然后执行；以下命令尚未在本阶段目标上运行：

```bash
: "${CODE_ROOT:?}" "${ARTIFACT_DIR:?}" "${SITE_FILE:?}" "${EVIDENCE:?}"
(cd "$CODE_ROOT" && sha256sum -c SHA256SUMS)
(cd "$ARTIFACT_DIR" && sha256sum -c SHA256SUMS)
"$CODE_ROOT/kk" ani validate --config "$SITE_FILE" --package-root "$ARTIFACT_DIR" --output "$EVIDENCE/validate"
"$CODE_ROOT/kk" ani render --config "$SITE_FILE" --package-root "$ARTIFACT_DIR" --output "$EVIDENCE/render"
sudo "$CODE_ROOT/install.sh" "$SITE_FILE" "$ARTIFACT_DIR"
# 仅在读取到本次真实 install-success run.json 后，再按其实际路径设 BASE_RUN：
sudo "$CODE_ROOT/kk" ani verify --run "$BASE_RUN" --level smoke --kubeconfig "$KUBECONFIG_FILE" --output "$EVIDENCE/smoke"
```

运行前还要按本阶段任务卡检查完整共存请求、KVM、地址、证书、离线时间及材料可用性；安装失败保留现场和真实 rc，不在目标机改源码或联网补料。安装通过后以同一正式包检查同版本 noop；本轮旧集群的底座旧证据不能替代新增证据。

## 2026-09-27｜正式离线包的原始安装用法

本节记录 2026-09-27 在获准的三节点实验环境对候选 `f76ff2e36ccfeaef39a36c2c4ea9bad08d84f3dd` 的**实际执行**；下方 2026-09-24 的 R 卡是历史计划。本次只覆盖 kcn + Ceph、底座 cert-manager/PostgreSQL/NATS/metrics/Loki/Fluent Bit、计划内新增 Valkey。**结论：指定组合安装和基本功能通过，保留 Ceph 健康告警。**这不签发其他可选组合、专项重建恢复或生产全面验收。

### 本次执行实例（仅供核对，当前集群不得重放首装）

执行机为 `node1`（172.16.101.20），Fedora 仅用于控制和归档。经明确授权，`test-installer-01/02/03`（VMID 5/6/7，与 .20/.21/.22 对应）在只读映射核对后恢复到各自 `snapshotId=1`；恢复后确认无旧集群/安装记录，三台声明的 Ceph 设备路径均指向未挂载的 `/dev/sdb`。三台按既有 `ANI-OFFLINE` netfilter 方法隔离，安装前后规则核对均 rc=0，实验锁已释放。没有触碰其他 VM、合并 main、发布 Release 或执行重型验收。

```bash
CODE_ROOT=/home/ubuntu/f-live/ani-code-install-03
ARTIFACT_DIR=/home/ubuntu/f-live/ani-artifact-live-02
SITE_FILE=/home/ubuntu/f-live/site-base-r2.yaml
ADD_SITE_FILE=/home/ubuntu/f-live/site-valkey.yaml
EVIDENCE=/home/ubuntu/f-live/evidence-install-f76ff2e
KUBECONFIG_FILE=/etc/kubernetes/admin.conf
BASE_RUN=/var/lib/ani-installer/ani-lab/run.json
PLAN_JSON=$EVIDENCE/components/components-plan-ani-components-ani-lab-20260927-203545.json
COMP_RUN=/var/lib/ani-installer/ani-lab/components/ani-components-ani-lab-20260927-203545/components-run.json
REPEAT_PLAN_JSON=$EVIDENCE/components-repeat/components-plan-ani-components-ani-lab-20260927-204115.json
```

配置文件 SHA-256：底座 `deb4a8b7d673e014fb17d01f88565c0273c1912c4d17af470b903dad1da77552`，新增 `57db2f4a7bb6203a6a8fe26984a3c0f8fcd95b1120375ab962fee1c8a31890cf`；两者语义上仅 Valkey 的启用选择不同。产品记录的配置摘要分别为 `67969228dca842bd28a9efd7f514f57e2d92f47c06a04e5c6ca5cd69037d94e2` 和 `003b0da327e3621ce0ec0654892a1a6e4ce8c32432ced665f9a41c6c9a28a9f6`。执行机两个包的 `sha256sum -c SHA256SUMS` 均 rc=0：`kk` **文件摘要**为 `3c4caae4cd09a6905f030210c3e339fe8d58594d6c326a5320bc413e476278fd`，代码包 **SHA256SUMS 清单文件摘要**为 `08f6db49d5f0c2542d3480855037ee1f2d3e2306be4ac954d3f16439af5649c8`，材料包 **SHA256SUMS 清单文件摘要**为 `1e2a4e73190037d46a9b664b26b82f8e04011b5787d245058dbddb1c703b4799`，材料锁摘要为 `02d5dea90ffceadb047f8471e47539ed4d55b2d9fe09a8caa8f079afd1d9dd34`。清单文件摘要**不是**整个材料归档的单文件摘要。[该提交 CI](https://github.com/zhangzhe-ctrl/ani-installer/actions/runs/36314798711) 的 head SHA 相同且结论为 success。

| 阶段 | 实际结果与记录 |
| --- | --- |
| 一次首装 | `kk ani install` rc=0；`/var/lib/ani-installer/ani-lab/run.json` 为 `recordKind=install-success`、`result=succeeded`、run `ani-ani-lab-20260927-120855`，仅含原六组件，Valkey 未启用。 |
| 基本功能 | 对该产品记录运行正式 `ani verify --level smoke` rc=0，六组件逐项及 overall 为 pass；现有通用跨节点网络/DNS 探针 rc=0，node1→node3、node2→node3 的 PodIP/ServiceIP/DNS 内容断言均通过；安装内 RBD 与 CephFS 写入读回任务成功。 |
| Valkey 真实新增 | `components install --only valkey` 计划 rc=0 且仅 Valkey planned；`components execute` rc=0，产品记录 `operation=add,didInstall=true`；从该执行记录运行 `verify --level smoke --only valkey` rc=0，带认证 SET/GET、TTL 到期与未认证拒绝均通过。 |
| 同版本重复执行 | 再生成计划 rc=0，Valkey `already_installed`；执行 rc=0，记录 `operation=noop,didInstall=false`；前后 14 个相关控制器/PVC 的 UID、代次和 spec、14 个 Pod UID、原首装与新增记录摘要均未变化。 |

每一步的实际命令、真实退出码和日志在 `EVIDENCE` 下的对应 `.command/.rc/.log`。smoke 期间控制端 SSH 断开，先核实原进程和 node1 的 `smoke.rc=0` 后继续，没有重发 smoke。13 项关键证据的 `delivery-evidence.sha256` 在 node1 与 Fedora 私有归档中均逐项校验通过；**证据清单文件摘要**为 `8f355721504277a9e71c766d37b579225db4a6d2d7a397b67e33e8493da2aaec`。Fedora 仓外归档在 `/home/chabking/.cache/ani-install-delivery-20260927/archive/node1/`，原始配置、日志和记录留在私有目录，不进入 Git。

### Ceph 已知告警（仅只读判读，本轮未处理）

本次运行的是 Ceph Tentacle `20.2.4`，三 OSD 均 up/in，CephCluster 为 `Ready / HEALTH_WARN`。基础 RBD/CephFS 读写通过**不等于**安全认证、故障恢复或持续可用性已验收。

- **认证：**`AUTH_INSECURE_CLIENT_KEY_TYPE` 指向 `client.csi-cephfs-node.1`、`client.csi-cephfs-provisioner.1`、`client.csi-rbd-node.1`、`client.csi-rbd-provisioner.1` 四个 `aes` 客户端密钥；并有 `AUTH_INSECURE_KEYS_ALLOWED`（monmap 仍允许 `aes,aes256k`）和 `AUTH_INSECURE_KEYS_CREATABLE`。只读回读的 `mon_auth_allow_insecure_key` 在 mon.a 为 true、mon.b/c 为 false；不能用 mon.b 的 false 代替全群结论。20.2.4 含相关安全补丁，但旧 `aes` 凭据的风险仍需按 [Ceph Tentacle 告警说明](https://docs.ceph.com/en/tentacle/rados/operations/health-checks/)和 [CephX 安全公告](https://docs.ceph.com/en/latest/security/CVE-2025-30156/)处理。后续先核对 Rook/CSI 客户端兼容与密钥分发路径，再有计划地轮换四个客户端凭据、验证 CSI，并在所有必要实体安全后收紧允许的 cipher；不能直接关闭 `aes` 而中断认证。本轮未读取/输出密钥，也未更改认证策略。
- **时钟：**`MON_CLOCK_SKEW` 最近一次回读显示 mon.b（node1）偏差 `0.422753s`、mon.c（node2）`0.0982324s`，均高于实际 `mon_clock_drift_allowed=0.05s`；偏差会随时间变化。三节点 chrony active，但 `NTPSynchronized=no`、`chronyc activity` 为 0 在线源；配置 `cn.pool.ntp.org` 当前地址未知，并启用 `local stratum 10`。按 [Ceph Tentacle 时钟告警说明](https://docs.ceph.com/en/tentacle/rados/operations/health-checks/)应规划离线可达的可靠时间源并核实监视器相互同步，随后确认告警消退；本轮不提高容忍阈值。监视器时间未可靠同步前，不把高可用持续性视为通过。
- **PG：**`TOO_MANY_PGS` 为 `265 > mon_max_pg_per_osd=250`，当前 3 个 OSD in；现有 12 个池的 `pg_num` 合计 265（包括 RGW data 128、CephFS metadata 16/data 32、RBD 32，其他池合计 57）。依 [Ceph Tentacle 告警说明](https://docs.ceph.com/en/tentacle/rados/operations/health-checks/)，超阈值会阻止新建池、提高 `pg_num` 或提高副本数，并增加 OSD/mon/mgr 负担。后续应按实际池和 OSD 容量审定 PG/扩容方案；告警未解除前不把新增池/副本扩展视为已支持。本轮不提高阈值、不修改池。

**使用限制：**本次只签发指定实验组合的安装与基本功能。上述认证和时间告警尚未收敛，不作为生产稳定版、安全认证或故障恢复的放行依据；未发现本轮读写失败，也没有证据证明现有数据损坏。Fluent Bit 重建恢复、告警生命周期、持久化重启及其他可选组件仍未由本次验证覆盖。

### 后续仅在新的干净、获授权目标复用的操作顺序

1. 在执行机上分别设置 `CODE_ROOT`（本轮同源代码包目录）、`ARTIFACT_DIR`（已批准且摘要校验过的离线材料包目录）、`SITE_FILE`（该站点已审核的底座配置）、`EVIDENCE`（本次私有输出目录）。四个输入必须是实际绝对路径；示例配置中的 `CHANGE_ME` 和磁盘占位符不能直接使用。先核对执行机、配置、材料、目标磁盘与允许范围。**不要把 Fedora 上的包路径当成目标机路径。**
2. 在执行机核包并做本地输入检查；`validate` 只检查配置，不能代替安装的材料预检：

   ```bash
   : "${CODE_ROOT:?}" "${ARTIFACT_DIR:?}" "${SITE_FILE:?}" "${EVIDENCE:?}"
   (cd "$CODE_ROOT" && sha256sum -c SHA256SUMS)
   (cd "$ARTIFACT_DIR" && sha256sum -c SHA256SUMS)
   "$CODE_ROOT/kk" ani validate --config "$SITE_FILE" --output "$EVIDENCE/validate"
   "$CODE_ROOT/kk" ani render --config "$SITE_FILE" --package-root "$ARTIFACT_DIR" --output "$EVIDENCE/render"
   ```

3. 只在已确认是**干净、获授权**的安装目标上执行一次首装，并保存 stdout/stderr、真实退出码、`run-state.json` 与命令打印的 install-success `run.json` 路径。安装自身还会执行材料、锁、端口、目标和磁盘预检；失败后不要用第二个目录或确认变量盲重试。

   ```bash
   sudo "$CODE_ROOT/install.sh" "$SITE_FILE" "$ARTIFACT_DIR"
   # 与 sudo "$CODE_ROOT/kk" ani install --config "$SITE_FILE" --package-root "$ARTIFACT_DIR" 二选一
   ```

4. 读取上一步**真实成功记录路径**为 `BASE_RUN`，设置本次选定的 `KUBECONFIG_FILE`，执行基本检查并查看生成的报告；不得从 `validate` 的输出取 `run.json`，也不要调用旧 `verify.sh CONFIG ARTIFACT`。本范围不调用 `--level acceptance` 或 `--allow-pod-recreate`。

   ```bash
   : "${BASE_RUN:?install-success run.json}" "${KUBECONFIG_FILE:?}"
   sudo "$CODE_ROOT/kk" ani verify --run "$BASE_RUN" --level smoke \
     --kubeconfig "$KUBECONFIG_FILE" --output "$EVIDENCE/smoke"
   ```

5. 只对已批准、材料已在原 registry、且当前底座健康的组件新增。以计划内 Valkey 为例，先把 `ADD_SITE_FILE` 设成仅含该新增选择的已审核配置；`--only` 不能用来改变网络、存储或已有底座。`components install` 只产计划，检查结果后把其实际打印的计划路径设为 `PLAN_JSON`，再执行一次。执行结果打印的记录路径设为 `COMP_RUN`，用该记录做 smoke；观察记录的 `already_installed`/`noop` 只证明只读重复执行，不等于又安装了一次。

   ```bash
   : "${ADD_SITE_FILE:?}" "${BASE_RUN:?}" "${KUBECONFIG_FILE:?}"
   sudo "$CODE_ROOT/kk" ani components install --config "$ADD_SITE_FILE" \
     --package-root "$ARTIFACT_DIR" --base-run "$BASE_RUN" \
     --kubeconfig "$KUBECONFIG_FILE" --only valkey --output "$EVIDENCE/components"
   : "${PLAN_JSON:?use the plan path printed by components install}"
   sudo "$CODE_ROOT/kk" ani components execute --plan "$PLAN_JSON" \
     --config "$ADD_SITE_FILE" --package-root "$ARTIFACT_DIR" \
     --base-run "$BASE_RUN" --kubeconfig "$KUBECONFIG_FILE" \
     --kk "$CODE_ROOT/kk" --output "$EVIDENCE/components"
   : "${COMP_RUN:?use the record path printed by components execute}"
   sudo "$CODE_ROOT/kk" ani verify --run "$COMP_RUN" --level smoke \
     --only valkey --kubeconfig "$KUBECONFIG_FILE" --output "$EVIDENCE/components-smoke"
   ```

**版本与证据边界：**上方实例是候选 `f76ff2e36ccfeaef39a36c2c4ea9bad08d84f3dd` 的本次实机结果；旧 R16/090732 结果仍只属于各自代码树与配置，旧账本及已消耗额度不改。当前集群已经安装，**不得重放第 3 步首装**；这些命令仅供另一台新的干净、获授权目标按其实际路径执行。`validate/render` 仍只证明输入检查，不能替代实机结果。


以下是 **2026-09-24 / r1 的历史计划**，其中“本轮未连接”仅指当时轮次，不能覆盖上方 2026-09-27 的实测记录。历史步骤中的路径须按对应 R00 核对后填写。允许的测试节点为`172.16.101.20`、`.21`、`.22`；任何其他目标都停止。

## 1. 区分三种终端

| 标记 | 所在位置 | 可以做什么 |
|---|---|---|
| LOCAL | 你当前的权威源码目录 | 编辑、看diff、记录指纹、传输源码；不假定Windows/Linux固定路径 |
| FEDORA | `ssh fedora`登录后的编译与实验控制机 | Go编译、模板/fixture测试、联网制包、lab锁、传输与实验编排 |
| TARGET | 实际`installerNode`，本期必须为nodes[0] | 运行已批准发布物、读取集群状态、执行本次明确验收 |

`kubekey/`是Go模块目录，`ani-installer/`是Git仓库根。命令里的`REPO_LOCAL`是仓库根；运行Go命令要进入`$FEDORA_SRC/kubekey`。

历史路径只用来帮助查找，不能直接当本轮事实：旧Fedora曾有`~/ani-installer-runs/obs-fix-20260919/src/kubekey`和两个releases目录。本轮选定一个源码目录和一个发布目录，在`lab.env`记录绝对路径。旧目录不删除、不覆盖未知内容。

## 2. 第一次只做R00，不操作集群

LOCAL：从`templates/lab.env.example.sh`复制成你自己的`lab.env`，放在仓库外。模板里的`REPLACE_...`必须替换；不要把私密密码加进Git。

```bash
set -euo pipefail
# LOCAL；这些变量由你核对后设置，不复制模板占位值执行
: "${REPO_LOCAL:?}" "${PLAN_DIR:?}" "${EVIDENCE:?}"
cd "$REPO_LOCAL"
git status --short
git rev-parse HEAD
python3 "$PLAN_DIR/scripts/source-snapshot.py" "$REPO_LOCAL" "$EVIDENCE/source-baseline.json"
# 只读探测，不安装软件
ssh fedora 'uname -a; id; command -v go; go version; command -v python3; command -v helm; command -v skopeo'
```

预期：得到有效源目录、HEAD和treeSHA256；Fedora可连；工具缺项是待办，不自动执行系统升级。若工作树有已有修改，记录归属，不`git reset --hard`、`stash`或`clean`。

Go工具链必须满足当前`go.mod`（上传快照要求1.25.0）。由已有Fedora工具链或批准的固定工具包供应；不为了通过而降低go.mod，也不临时下载浮动latest。

## 3. 复制源码，但不维持两个“权威副本”

R01先处理源码中的明文凭据。R00阶段可以只读核对/指纹；**未清理前不把含凭据的源码复制到新位置或打进交付包。** 旧历史归档按凭据轮换规则处理，本计划不自动重写Git历史。

LOCAL：先检查传输列表。以下rsync只是不带删除选项的范式，确切排除项要与R00指纹规则一致。`FEDORA_SRC`使用新建且已确认属于本轮的目录，不覆盖未知工作区。

```bash
set -euo pipefail
# LOCAL；先干跑，看将传输哪些文件
rsync -an --itemize-changes \
  --exclude='.git/' --exclude='releases/' --exclude='node_modules/' \
  --exclude='.venv/' --exclude='__pycache__/' --exclude='*.log' \
  "$REPO_LOCAL/" "fedora:$FEDORA_SRC/"
# 检查没有密码、kubeconfig、运行日志或大镜像后，才执行同一列表
rsync -a --itemize-changes \
  --exclude='.git/' --exclude='releases/' --exclude='node_modules/' \
  --exclude='.venv/' --exclude='__pycache__/' --exclude='*.log' \
  "$REPO_LOCAL/" "fedora:$FEDORA_SRC/"
```

不要加`--delete`；也不能因它没有删除旧文件就假定两端一致。对两端分别运行同一版本的`source-snapshot.py`，比较`treeSHA256`。如果旧文件残留导致不同，使用新的干净镜像目录；不要盲目批量删除。

指纹工具忽略构建输出/凭据型文件，**不是秘密扫描器**。R01单独完成秘密扫描和旧凭据轮换。需复制的计划包也先检查本包SHA，再记录`PLAN_DIR`在LOCAL/FEDORA上的不同路径。

## 4. 执行一张R卡的最小循环

先写任务ID、开始源码指纹、允许文件列表、预期失败用例。修改不超出这张卡；先运行新增负向测试，能看到修复前失败；再改实现，看到同用例通过。

FEDORA：

```bash
set -euo pipefail
cd "$FEDORA_SRC/kubekey"
# 举例：先按R10真正新增这些用例，然后运行。原仓库未必有同名测试。
go test -v -count=1 ./pkg/ani -run 'TestKubeOVN|TestNetworkCIDR'
```

检查输出包含实际`=== RUN`名称与断言，而不是`[no tests to run]`。只做字符串contains测试不足以通过R10；用真实模板渲染结果断言网关/CIDR。

R04完成之后，统一入口必须实际存在：

```bash
set -euo pipefail
# FEDORA；只有R04已完成才能运行
cd "$FEDORA_SRC/kubekey"
test -f scripts/check-code.sh
set -o pipefail
bash scripts/check-code.sh 2>&1 | tee "$EVIDENCE/check-code.log"
```

预期：退出码0、全部必测用例确实执行、没有未解释skip，结果关联当前treeSHA256。任一失败留在本地修正，不运行snapshot restore，不把集群当第一道语法检查。

同一轮改动涉及需要镜像/Chart变化时，转M任务；否则只构建代码，不重包几十个组件。

## 5. 发布代码：使用已有build-code.sh

上传代码中的环境变量为`GO_BIN`、`ANI_CODE_OUT`。**当前脚本未调用完整门禁，R04要补齐后才可作为可信发布入口。**

```bash
set -euo pipefail
# FEDORA；R04后，CODE_OUT是本次新的版本目录
cd "$FEDORA_SRC/kubekey"
: "${GO_BIN:?已核对的Go可执行路径}" "${CODE_OUT:?本次代码输出目录}"
GO_BIN="$GO_BIN" ANI_CODE_OUT="$CODE_OUT" bash scripts/build-code.sh
(cd "$CODE_OUT" && sha256sum --check SHA256SUMS)
"$CODE_OUT/kk" ani --help
"$CODE_OUT/kk" ani install --help
```

后续任务实现新入口后，逐一核对`validate --help`、`render --help`、`components install --help`、`verify --help`。缺入口代表该任务未完成，不造一个同名shell脚本绕过去。

本地发布记录至少保存：源码treeSHA、HEAD、Go版本、构建参数、kk SHA、CODE_OUT。新组件的`checks/<component>/`属于代码发布物；只修改检查器不能迫使重做大artifact。

## 6. 物料变化才制包

先在每批M阶段写物料清单：固定官方tag/commit、Chart和子Chart、静态与后续动态Pod镜像、工具、SDK依赖、测试模型/磁盘/数据集、摘要及支持范围。

下面变量名来自现有`build-offline.sh`；不是要求每轮都运行。路径和输入必须经过R07校验，不能以包内新生成的SHA256SUMS替代来源校验。

```bash
set -euo pipefail
# FEDORA联网材料阶段；使用本批审核过的实际路径
cd "$FEDORA_SRC/kubekey"
: "${PACKAGE_CONFIG:?}" "${IMAGES_TSV:?}" "${COMPONENT_LOCK:?}"
: "${HAULER_BIN:?}" "${HELM_BIN:?}" "${REPOSITORY_ISO:?}" "${CHARTS_DIR:?}"
: "${KK_BIN:?}" "${ARTIFACT_OUT:?}"
CONFIG="$PACKAGE_CONFIG" IMAGES_TSV="$IMAGES_TSV" \
COMPONENT_LOCK="$COMPONENT_LOCK" CHARTS_DIR="$CHARTS_DIR" \
HAULER_BIN="$HAULER_BIN" HELM_BIN="$HELM_BIN" \
REPOSITORY_ISO="$REPOSITORY_ISO" KK_BIN="$KK_BIN" \
ANI_ARTIFACT_OUT="$ARTIFACT_OUT" bash scripts/build-offline.sh
```

已有归档路径`KUBEKEY_ARTIFACT`、`HAULER_ARCHIVE`可按真实输入显式提供；它们**同样必须经过R07内容检查**。不要给一个任意tar后只运行`sha256sum --check`就认为材料通过。

包可能保留已声明而未启用镜像；未声明来源和多余Chart不应混入。先前研究登记中的摘要待采集项保持待采集，不复制旧版本SHA。

## 7. 首装前先validate/render，再申请一次实验运行

```bash
set -euo pipefail
# FEDORA或准备好的TARGET本地；R06/R07/R08/R09实现后
"$CODE_ROOT/kk" ani validate --config "$SITE_FILE" --package-root "$ARTIFACT_DIR" --output "$EVIDENCE/validate"
"$CODE_ROOT/kk" ani render --config "$SITE_FILE" --package-root "$ARTIFACT_DIR" --output "$EVIDENCE/render"
```

这是拟新增接口；作用仅为本地输入与渲染检查，不证明目标机器健康。检查输出：主CNI只一套；组件关闭项没有资源；Kube-OVN不调用kcn Envoy；没有意外Istio/LWS/GPU；磁盘只允许名单；所有镜像都映射至批准来源；没有密码出现在结果。

有错误就在此停止。**只读阶段失败不还原快照、不更换run目录骗过错误、不执行安装。**

## 8. 实验锁、快照和断网验证

这一节只用于已授权的三台测试机，不进入产品CLI。R01凭据整改与R12取消语义完成前，不扩大实验。

FEDORA在同一个shell里取得锁，覆盖取证、还原、传输、安装和验收整个过程：

```bash
set -euo pipefail
# FEDORA；LAB_LOCK_DIR须预先核对且属于本轮控制机
: "${LAB_LOCK_DIR:?}"
mkdir -p "$LAB_LOCK_DIR"
exec 9>"$LAB_LOCK_DIR/ani-three-node.lock"
flock -n 9 || { echo '已有实验运行，停止；不要删除锁文件或kill旧进程' >&2; exit 4; }
# 此后不要换一个未持锁shell启动写操作；FD9保持到本次结束
```

持锁不等于允许恢复未知现场。先只读确认三台IP对应的VM UUID/名称、当前快照、磁盘与允许损失的数据。旧记录中的VMID5/6/7、snapshotId1只能辅助核对，**不能直接当当前身份执行还原**。

现有`restore_esxi_snapshots.sh`没有在本次被确认支持`--dry-run`，所以文档不提供虚构的dry-run命令。R01应先建立lab-only映射核验入口或人工列表流程；它只列出身份/计划，不改变VM。缺凭据应在连接前失败；不得把密码默认值带回脚本。

触发一次restore必须同时有：固定任务ID、新的已通过局部测试的修正版、旧attempt证据已保存、三台与快照映射已核准、明确这是实验环境。用户此前同意该实验范围内按需恢复，不需要每一轮机械重复询问；但范围变化或映射不明就停止。

还原完成后重新核对SSH和主机网络，再使用已审阅的`~/ani-ops/apply_offline_isolation.sh apply`，随后`verify`。这是历史已有接口，先检查文件及其白名单、CIDR与本次配置一致；不能直接沿用过时网段。它必须保留SSH/集群内网/批准物料源路径，不删除默认路由或flush全部防火墙。

断网证据要显示：公网下载路径不可用，内部离线registry与所需DNS仍可用。容器已有缓存会掩盖缺材料，正式离线验收使用原始干净快照；**不要在工作集群清空全部containerd缓存模拟冷启动**。

## 9. 传输与执行：只在已核验的TARGET运行

通过当前已有的安全传输/askpass机制传代码包、站点配置、已核验artifact。密码文件在Fedora仓库外，权限受限；不要打印其内容或把值写在命令行。脚本`run_on_node.sh`在R01修复“上传失败仍运行旧/tmp脚本”之前不得用于写操作。

传输策略：artifact按内容ID复用；代码到新版本目录；站点配置单独受保护传输。先核对目标路径归属和容量，不覆写未知目录。目标重新计算摘要，与Fedora记录比对。

```bash
set -euo pipefail
# TARGET；这两个入口当前已经存在，但只有整改门禁满足后才能实机使用
: "${CODE_ROOT:?}" "${SITE_FILE:?}" "${ARTIFACT_DIR:?}"
(cd "$CODE_ROOT" && sha256sum --check SHA256SUMS)
sudo "$CODE_ROOT/install.sh" "$SITE_FILE" "$ARTIFACT_DIR"
# 等价底层入口是：
# sudo "$CODE_ROOT/kk" ani install --config "$SITE_FILE" --package-root "$ARTIFACT_DIR"
# 二者只选一个，绝不能连续执行两次首装。
```

安装后取新run.json的实际路径，检查安装结果。旧代码原有的高成本verify不能在R13前再次运行；整改后才执行：

```bash
set -euo pipefail
# TARGET；R13新增，按实际输出设置RUN_JSON
sudo "$CODE_ROOT/kk" ani verify --run "$RUN_JSON" --level smoke
# 专项验收必须列出本次批准重建的组件，下面以本批Milvus为例
sudo "$CODE_ROOT/kk" ani verify --run "$RUN_JSON" --level acceptance --only milvus --allow-pod-recreate
```

预期不是只看退出码：安装状态succeeded；smoke逐项pass；没有安装项目标skipped；acceptance列原Pod UID、新UID、同一PVC UID、数据读取断言。只安装了CPU组件就不能写GPU通过。

## 10. 新增组件的正确迭代，不每改一行都重装底座

前提：R15完成，健康底座和其run身份已知，**原registry已经供应本批材料**。为某批创建加速快照前，以包含本批材料的累计artifact装好健康底座；记录快照与材料身份。组件代码迭代从这个批次底座开始，不跑kubeadm/CNI/Ceph初始化。

```bash
set -euo pipefail
# TARGET；R15新增，此处只能在本批组件尚未安装时执行
sudo "$CODE_ROOT/kk" ani components install \
  --config "$SITE_FILE" --package-root "$ARTIFACT_DIR" --only milvus
sudo "$CODE_ROOT/kk" ani verify --run "$RUN_JSON" --level smoke --only milvus
```

如果registry缺材料，停在预检。首版不支持现场隐式注入/重启registry，不手工覆盖Hauler store。换成已验证本批材料的底座；以后需要增量物料更新时另开设计。

已经存在的同版本自有组件不被upgrade；不同版本/他人所有权拒绝。组件代码或配置错误导致现场失败，先本地修正并测，再恢复绑定本批的健康快照复验；**最终正式交付还需从原始干净快照完成整个所选组合**。

Multus/LB初次引入是B01明确网络扩展，本版通过新集群首装路径测试。不能借components install偷偷修改活跃主CNI。

## 11. 失败后的只读取证范式

先记录失败命令、退出码、开始时间、最后日志、run ID、source/kk/artifact/config身份。取证输出权限应为仅负责人可读，分享前脱敏。不要用`kubectl get secrets -A -o yaml`或完整敏感ConfigMap导出来图省事。

```bash
set -euo pipefail
# TARGET；只读，stdout放到私有证据目录
umask 077
kubectl --request-timeout=10s get nodes -o wide > "$EVIDENCE/nodes.txt"
kubectl --request-timeout=10s get pods -A -o wide > "$EVIDENCE/pods.txt"
kubectl --request-timeout=10s get events -A --sort-by=.lastTimestamp > "$EVIDENCE/events.txt"
# NS和POD从失败run读取，只取相关目标，不遍历Secret
kubectl --request-timeout=10s -n "$NS" describe pod "$POD" > "$EVIDENCE/pod-describe.txt"
kubectl --request-timeout=10s -n "$NS" logs "$POD" --all-containers --tail=300 > "$EVIDENCE/pod-logs.txt" 2>&1
```

日志本身仍可能有敏感值，自动归档不代表可以公开。某条只读取证失败，记录失败并继续其他只读检查；不得用成功的取证退出码覆盖最初安装/验收失败。

| 失败类型 | 下一步 | 禁止 |
|---|---|---|
| 配置/缺材料/预检 | 本地修配置或M材料后新run | 无理由恢复三台快照 |
| 模板/脚本/参数错误 | 最小fixture复现，R卡修复，局部测试通过后再实验 | 原地kubectl patch临时修过就宣布installer成功 |
| 组件自身缺陷 | 保存版本/事件/最小复现，交组件方 | 修改controller镜像、清理网络、重启底层“撑过去” |
| SSH中断/超时 | 标remote_result_unknown，只读确认远端状态 | 自动重放可能已生效命令 |
| 资源不足/硬件不支持 | 写blocked与具体缺项 | 临时开启emulation、换GPU驱动、增加另一套中间件 |

## 12. 每个attempt的记录，以及何时到下一张卡

一个attempt只引用一个源码treeSHA、kkSHA、artifactLockSHA、有效配置身份和快照身份。`commands`列表写真实运行机、cwd、命令、退出码、日志。分别记录源同步、局部测试、构建、传输、还原、底座、组件、smoke、acceptance的起止时间，不估算为实际耗时。

连续两次出现相同签名而没有新的因果解释，就停下补局部复现；不继续重复还原碰运气。成功后填`templates/task-result.yaml`，更新progress，只开放满足依赖的下一张卡。失败不自动勾done；`blocked`不是“失败被解决”。

所有计划内资源删除均限制本次创建/指定的准确对象及UID；不删除PVC/CRD作为恢复。实验收尾清理必须另列准确资源名单。无论成功与否，本轮都不自动Git commit/push、不对外发布、不启动下一批。

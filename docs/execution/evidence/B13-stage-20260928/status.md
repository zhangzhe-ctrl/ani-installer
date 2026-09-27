# B00–B07 与 B13 本阶段状态（2026-09-28）

范围仅为 B00–B07 和 B13 的本阶段全选组合；B08–B12 未实施，完整 B13 未完成。Fedora 隔离树分支 `feature/b00-b07-install` 的代码提交为 `1d00b0688bad8ffa4a5c8f370a1662aa4a231b67`。原工作树未重置、清理或覆盖。

| 检查 | 实际结果 |
| --- | --- |
| `build-code.sh` 所含全量门禁与代码包 | rc=0；日志 `/home/chabking/ani-installer-runs/b00-b07/gates/build-code-harbor-ci-fix-home-tmp.log`；源码树指纹 `2e8b9e665bbfeee2f90203ec6493d2269b555e239d403cc04eddb7e16c796065`。 |
| 累计材料包 | rc=0；日志 `/home/chabking/ani-installer-runs/b00-b07/gates/build-offline-4d98b3f-r3.log`；86/86 镜像由只读 registry 的安装同款内容门验证，10 个 Chart 和工具、guest、扫描库、ISO 按锁复读。 |
| 最终代码/材料 `sha256sum -c SHA256SUMS` | 均 rc=0，记录在 `/home/chabking/ani-installer-runs/b00-b07/final-static-harbor-ci-fix/`。 |
| 全选 site `kk ani validate` / `kk ani render` | 均 rc=0；render 设置本轮专用 `TMPDIR` 后执行，使用正式材料包与最终 kk 内嵌角色，不使用开发目录；同目录保存日志及渲染文件。只是离线静态检查。 |
| B00–B07 实际新增/功能/noop | **not_run**：未在目标机执行。原底座旧版本实测不能签发新增批次。 |
| B13 本阶段干净离线首装及基本功能 | **blocked / not_run**：未恢复快照、未清盘、未安装、未取得 install-success。 |
| B08–B12 与全量 B13 | **not_implemented / not_completed**；不作为本阶段 B13 的依赖。 |

交付候选（Fedora 路径）：

- 代码包 `/home/chabking/ani-installer-runs/b00-b07/code-b00-b07-harbor-ci-fix`，`kk` sha256 `11c702732bdee814788c08451bb6627ee26deeba66f3a5b5539a7cbbfc028fbd`，`SHA256SUMS` 文件 sha256 `dc8d51a2e04fbea009603e820f41a963313cdd48fc72d4dab34702dab1d2beca`。
- 累计材料包 `/home/chabking/ani-installer-runs/b00-b07/artifact-b00-b07-4d98b3f-r3`（约 9.5 GiB），`SHA256SUMS` 文件 sha256 `e92742b94166d9c512fbe1251bce9362b54c2e7cf0e3f4848866258f2db40c19`，材料锁 sha256 `9efd924ba94276b93f32def2726197b8ee60bc34a2487705c514173aeba5ac88`，`images.haul.tar.zst` sha256 `5acded081e9bed9349dfafa48809a2169cd070238786ad6f39df039e1991c606`。
- 已批准底座 KubeKey artifact 复用输入 sha256 `aa236abc09e8ee72f5b80d9cb4b08bc37c92e080766aa81e58966ef5e64de45f`，其批准 `package.yaml` sha256 与本轮相同（`50ff2faae7f3f922a58d4407f9bf2d07380e33540a1f08f8ecd1de48b88ed924`）。新增角色由本轮 `kk` 携带；所有新增运行镜像在累计 haul 中。
- 全选私有 site `/home/chabking/ani-installer-runs/b00-b07/site-b00-b07-full.private.yaml`（0600），文件 sha256 `5359e9b49e39242ed130343eda72092a16fac36a49927b24f129188de57c420e`，validate 配置摘要 `5415247f135ad5c2d05bc02b6b492bbe6ecb68b27c973b7710be54b356e977fd`。不入库、不输出其中凭据。

B13 只读现场预检保存于 `/home/chabking/ani-installer-runs/b00-b07/b13-readonly-preflight-20260928.txt` 和 `b13-capacity-preflight-20260928.md`。三节点各 4 vCPU、约 8.33 GB 内存、KVM 设备存在、数据盘各 50 GiB。Ceph 三副本原始容量 150 GiB；本阶段选定 PVC 声明量 72 GiB，满额至少需 216 GiB raw，当前至少缺 66 GiB raw，尚未计入 Ceph 开销、快照和增长。现场 `ani-block-pool` 的 `MAX AVAIL` 约 45 GiB，比声明量少 27 GiB。三节点均 `NTPSynchronized=no`，Ceph 仍有时钟偏差、CSI 不安全密钥类型和 PG 过多告警。未降低副本、缩小存储请求、放宽阈值或静音告警。

首次使用系统 `/tmp` 的代码构建与 render 因该 tmpfs 的用户配额已满失败；保留原日志，改用本轮专用 home `TMPDIR` 后代码构建 rc=0、render rc=0。PR #2 的较早 head `ecad83b` 在 GitHub Actions run `36353316743` 失败，原因是 Harbor CA 激活和 runtime 重启混在同一任务；当前提交已拆分且 Fedora R03 通过，当前 head 的 GitHub CI 结果仍待确认。

容量未满足前不得把可能依赖薄置备启动的结果当作所选全集共存通过；必须先使目标具备声明范围的磁盘和可用的离线时间源，再在原始干净目标上用正式包直接首装全集，读取真实 install-success，逐项基本检查及同版本 noop。当前代码/材料/静态结果不代替这些步骤，也不启动 B08。

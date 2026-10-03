# 保留现场联调记录

这些记录只属于开发联调，不进入最终干净首装的 EAC PASS。三台 VM 的 Id2 恢复仍未执行；用户已授权当前实验数据无需保留。阶段 C/D 通过后才执行恢复。

## 同源构建与闭合物料

- `1c45135ed3b090e01e131b31909f4b9810232906` 已推送；Fedora `build-code.sh` 真实退出码 0，统一门禁、编译、发布连续指纹 `1b690b7633c46db0b4e00980a93e3999fe93ccd19aee3321c7c0630bcd5dd006`。日志：构建任务根 `build-code-closed-assets-attempt-01.log`。
- 13 个角色/探针源文件、41 个实际离线 wheels、gRPC 客户端源码归档纳入源绑定物料；审批文件 SHA256 `2bd910cddadaba2bb50a41cacc0aa70a1b212cae3fb5deeed7a9a491182068e6`。gRPC 客户端离线重建与固定二进制字节相同，协议认证仍未执行。
- 保留两次首次门禁失败：`4278d5b` 缺工具源码归档摘要；`74517a6` 的物料测试仍固定四工具。修复后 `1c45135` 定向检查及统一门禁 PASS，未跳过检查。
- 完整旧基础物料在 Fedora `reference-artifacts-attempt-02/artifact-c165ae7-candidate-r2`，逐项原始 SHA256SUMS 验证通过；原清单 SHA256 `441f3bc8c4f081a8e660ce35fc3eaa5d05aaf257d3721f18b2b977692e020640`。104 镜像累计候选包正在走既有完整内容门禁；尚未冻结正式发布物。

## 现场安装尝试

固定联调集群 kube-system UID `87ecef8e-ac4e-442b-8e15-5e906263be6b`，kubeconfig `/etc/kubernetes/admin.conf`。所有写入使用真实内核产品锁 `/var/lib/ani-installer/ani-install.lock`。

1. 首次角色源码 `5052e81`：确认 77 次写入；CRD Established；等待控制器 600 秒后 FAIL，进程退出码 1。开发 registry 为 HTTP 5001，运行时尝试 HTTPS 导致 ErrImagePull。首次 shell 未落退出码文件，采用实际执行工具返回的进程退出码，不采用 tail 状态。
2. 同源重入：确认 15 次写入，退出码 1；空占位证书 Secret 与控制器生成的数据不同，被凭据轮换保护拒绝。仅两个固定控制器证书的空占位允许保留现有字节，其他凭据和替换行为继续拒绝；四项回归通过。
3. 源码 `1c45135`、独立目录 `integration-attempt-03`：包 SHA256 `cd797812084f7892364834c87e853e62389e936f20bdcb401b3ed1d20a322404`，站点 SHA256 `81d3c832e7cd55824e33da4cf460e9d1fb64d467e40926eecda65ad4e19319e8`。真实退出码 1，确认 70 次写入。webhook 干运行在原 create manager 的原子 rules 列表发生 SSA 冲突；现场规则已由 API 默认补入 `scope: "*"`，源码未显式给出。后续 S3、数据库、KFP 消费者未执行。修复在源码显式给出 API 默认值，不 force-conflicts，不修改节点源码，不删除控制器。

控制器原 Pod UID：JobSet `f6b035f0-cd2e-4800-b8e2-826d6f15f025`、Trainer `d0453fc7-82cf-4d06-a794-c123e9d1365c`；第三次调用前两者 Running、重启数 0。此观察不证明 TrainJob、入口或业务验收通过。

前两次原始日志已移出 VM：Fedora `integration-first-failures.tar`，SHA256 `0ecb1cee90ee2143e42d80af266f579692812b36fa8b30ce5fb1e401f4dd16ed`。第三次日志仍在固定节点目录，将在恢复前外存。

## 执行偏差及证据限制

- 开发阶段使用明确的 HTTP registry 和 `ctr --plain-http` 缓存准备；没有修改 containerd 配置或重启服务。三节点 15 镜像 pull/CRI inspect 验证只属于开发准备。最终新集群必须独立证明正常 kubelet 拉取，不能使用继承缓存作为 PASS。
- `e2fc071` helper 在 ani-01 的第三次缓存准备先于该提交推送完成。Git TLS EOF 后只读核对远端，再推送并验证同一 SHA；之后其他节点动作使用已发布源码。该提前执行不进入最终发布/验收证据。
- 首次图表门禁误设 `ANI_CHART_CACHE_DIR`，实际变量为 `ANI_CHART_CACHE`，共享缓存被填充；保留原缓存，后续使用任务目录，未清理他人缓存。
- 两次慢速转移终止后保留了部分文件；`reference-artifacts-attempt-01` 不是完整物料，不用于构建。完整 attempt-02 经过实际清单校验。
- 首次 Hauler load 因 `/tmp/hauler3155932284` 配额失败，保留日志与独立 attempt-01 输出。显式任务 TMPDIR 和 `--tempdir` 后 attempt-02 退出码 0；这是环境失败，不是源物料损坏。
- 节点时间与 Fedora 有约 18 秒差，tar 解包输出 future timestamp 警告；逐项摘要通过。外部 UTC 仍 NOT_VERIFIED，未静音或调整时钟凑通过。

本轮必需验收仍有 NOT_RUN，`ENV_READY` 不成立；CPU-P01 业务未运行。

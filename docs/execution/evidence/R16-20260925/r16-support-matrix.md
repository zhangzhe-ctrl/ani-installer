# R16 step3 支持矩阵（attempt-r2 记录）
| 行 | 网络栈 | 本卡干净首装 | 组件覆盖 | 状态 |
|---|---|---|---|---|
| A | kcn + ceph存储 | **已完整实机**：干净快照还原→隔离→1540s 计时首装 pass（run=ani-ani-lab-20260925-120811，0 公网命中）；smoke 6 组件全 pass；PG/NATS 各一次计划内重建持久化 pass；valkey 计划内新增 36s pass 且底座不变；postgresql 重复规划有界拒绝零写入 | 全行组件 | **pass（本组合已签发）** |
| B | kubeovn | 本卡未选行。历史材料仅到：选型代码 CNI 可切换（commit 6eae264）+ 9/22 材料/recipe（runs/kubeovn-1.16.6-2026-09-22/BUILD-RECIPE.md、kubeovn-full-verify/store）；R11/R12 progress liveSmoke=not_run，无 R16 级干净首装证据 | 底座+kubeovn | not_verified（不虚标；如需 B 行认证须专门轮次实机） |
| C | 存储变体 | Ceph 关闭 + 外部 StorageClass | step5 局部验证 v3 rc=0 | local_pass_live_not_verified（A 行实机走的是 ceph 路） |
| D | quoted YAML / 非默认 Pod CIDR | step5 v1/v2 rc=0；v2b 记录离线放行观察 | 配置语义 | local_pass |
| E | 冷启动 registry 独立实验 | 未排期（卡规定不混入全组件安装） | — | not_run（本轮 r2 首试恰好发现 unattended-upgrades dpkg 锁竞争，已批准基线卫生修复并复跑通过） |
| F | opensearch 后端分支 | 本地 R03-coverage-opensearch 全 PASS；实机需独立站点轮（后端互斥） | collector 标记分支 | local_pass_live_not_verified |

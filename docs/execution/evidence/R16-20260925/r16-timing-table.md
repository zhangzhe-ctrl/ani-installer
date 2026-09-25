# R16 step7 分阶段耗时表（04手册:248 九阶段，实测 epoch/UTC，未估算）
| 阶段 | r1（物料预检失败轮） | r2（终版：一次因 dpkg 锁竞争失败，重跑成功） |
|---|---|---|
| 源同步 | 无（冻结树 3fca769b） | 无；窗口复测 rc=0 同指纹（15:xx r16-gate-recheck-r2window.log） |
| 局部测试 | check-code 全绿（r16-frozen-gate.log） | r2 轮前复跑 rc=0；判据仿真回归 4/4（r16-polling-criteria-regression.txt） |
| 构建 | code 12:39:38 冻结 | 物料重建 haul 13:11→13:21（haul-surgery 链）；新 artifact 13:24:55→13:26（r16-build-fixed-artifact.log，含 serve 摘要实证） |
| 传输 | 51s（05:00:30Z→05:01:21Z） | 45s（12:07:26Z→12:08:11Z） |
| 还原 | dry-run 20s + execute 58s（12:52-12:54） | dry-run rc=0（S1 12:04:32Z→12:04:51Z）；execute+开机+网络 64s（→12:05:55Z） |
| 隔离 | 2m38s | apply+verify+proof 75s（12:06:02Z→12:07:17Z）；S4b unattended 停用 8s（r3 批准的基线卫生） |
| 底座 kk ani install | 122s 后预检拒绝 postgres（phase=install_failed@registry_ready） | **1540s 成功**（12:08:11Z→12:33:51Z INSTALL_COMPLETED；run=ani-ani-lab-20260925-120811；控制台公网 URL 命中=0） |
| 组件 | — | 底座内置 6 组件含于上；valkey 计划内新增 plan 1s + execute 36s（13:02:59Z→13:03:35Z） |
| smoke | — | 6 组件 606s 全 pass（12:33:52Z→12:43:58Z）；valkey add smoke pass（SMOKE_RC=0） |
| acceptance(持久化) | — | PG+NATS 各一次计划内重建 pass（12:43:58Z→12:44:19Z 判定，ACCEPT_RC=0；重建后 pod Running、PVC Bound 不变） |
| 失败发现阶段(r2首试) | — | Repository apt 步 dpkg 锁竞争（unattended-upgr 持锁 300s 超时，490s 处判 FAILED——新判据正确未误报），非产品缺陷 |
备注：run-state.json 的 phase 字段停在 registry_content_verified（成功轮亦如此），与 r1 记录的 sourceTreeFingerprint==configDigest 同族，登记为范围外观察（代码卡处理），验收结论以完成标记+报告+实查为准。

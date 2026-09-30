# ANI-system r2 工作结果

本轮正在执行；此记录不含秘密。

- 本机分支：feature/ani-system-fast-20260930。
- 本机 worktree：/home/chabking/workspace/ani-installer-ani-system-fast-20260930。
- 固定 origin/main 基线：78d8418307b6156954feb10aee8d6972d2ff1bbd（原树 main 干净）。
- 用户输入：/home/chabking/下载/ani-system-fast-r2；只按 README 选择性复制，reference 原件仓外保存。
- Fedora 镜像输入：/home/chabking/ani-installer-runs/ani-system-fast/incoming/ani-images-download.zip；校验尚未完成。
- 目标限定 172.16.101.10/.11/.12；实际集群身份、协调锁待核对。
- 全部非编辑/Git/传输执行通过 ssh fedora；不改动共享凭据、旧材料锁或旧 run。

| 阶段 | 状态 | 证据与限制 |
| --- | --- | --- |
| 隔离分支、基线与资料 | pass | 原树只读，新树从 origin/main 建立 |
| P1 实际镜像校验/导入 | not_run | 已定位输入 |
| P2 配置 | not_run | |
| P3 空库、首管与核心入口 | not_run | |
| P4 应用及 installer 接入 | not_run | |
| P5 Ready/API/前端 | not_run | |
| 人工登录 | manual_not_run | 不能代称用户登录通过 |
| 裸机从零首装 | not_run | 本轮不重置现有集群 |
| 完整业务验收 | not_verified | 用户后续验证 |

## P1 输入核验与当前环境限制

- 原 ZIP SHA-256：a5be1c9433d1cecb7a3dfac84315bfa5934cf1b41171d77d8e60fc72226c1f47。
- ZIP CRC、安全解包和内层 ani-images-bundle.tar.gz.sha256：pass；工具 verify rc=0，24 个 linux/amd64 Skopeo dir 镜像，3 个 Chart。原件只读保留。
- 已按实际内容新建 materials/actual-images.lock.json；目标 Harbor 引用尚未导入，targetDigestRef 为 null；不冒称 P1 完成。
- Chart 实际归档：gateway-helm v1.8.1、ai-gateway/CRDs v1.0.0，摘要在新应用锁中。
- 常驻与动态 model-import-worker 二进制 SHA-256 分别为 ee7c05d4495391dd20c71cc8e49fdcdcd9bc3eadc68f857ebd86ca4af770d121、f365f7c9ddfa863db8a9d5377fa333a06c37a290ccc724b965cad444013486a4；不合并用途。
- 实际 Console/BOSS nginx 都代理固定 ani-gateway.ani-system.svc.cluster.local:8080；未重编。
- Fedora 默认 installer 路径已不存在；/tmp/clone-with-history 是旧 installer 库，fetch 首错为磁盘配额。基于它的 task-owned Git 副本携带损坏 pack；保留该失败副本，使用本机完整 bundle 在本任务新 bare 库恢复提交历史。
- 有效执行树改为 /home/chabking/workspace/ani-installer-ani-system-fast-20260930-exec-2，detached SHA 1374241e39721766891912845d2aa474b7b121d9；后续修复 SHA 待记录。其他开发树未切换。
- Fedora 无 kubeconfig；已有节点 SSH 身份认证被拒；旧 ~/.ani-askpass 的目标密码文件已不存在。访问材料缺口已向用户集中请求。
- Python 材料/应用工具 22 个单元测试 pass，bash -n pass；Go 局部验证和实际渲染未完成。生成格式 diff 在 Fedora 仓外审阅后回本机修改。

本轮私有证据根：/home/chabking/ani-installer-runs/ani-system-fast-20260930/；输入校验 logs/p1-input.log；局部 tests logs/stage1-python.log、stage1-python.rc。

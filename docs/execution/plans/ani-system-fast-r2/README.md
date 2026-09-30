# ANI-system 快速部署｜单包执行版 r2

本包只包含任务资料、部署参考和材料接收工具，不含用户已经下载的实际 ANI 镜像。不是已完成的安装实现。

## 用户只做两件事

1. 将此 ZIP 解压到本机下载目录，得到 `~/Downloads/ani-system-fast-r2/`。不要先放进正在并行开发的旧工作树；其它下载目录也可以，给 agent 真实路径即可。
2. 将 GOAL.txt 全文交给 agent。它先创建新分支、新 worktree，再按下面规则复制本包。无需再下载其它任务文件。

已经下载的镜像包及同名校验文件放到 Fedora：
`/home/chabking/ani-installer-runs/ani-system-fast/incoming/`。
已有其它明确路径的包直接复用，不必改名或重复上传。镜像若仍在本机，由用户手工上传，不在本机重新下载、解析或构建。

## 本轮新决定（覆盖旧参考资料）

- **镜像以用户已下载的实际内容为准。** 与旧探测、旧请求、旧 runtime ID 不同已获得用户批准，无须再请求同一版本替换批准。文件完整性、平台与最小启动兼容性仍需检查。原包不改写。
- **部署目标为现有 172.16.101.10/.11/.12 集群。** 从 Fedora 控制，只安装 ANI 应用；本轮不恢复快照、不重装基础设施。
- **本机只编辑和提交推送；重任务全部 Fedora 执行。** 本机、Fedora 都必须使用本任务独立 worktree，不影响并行开发。
- 旧 ANI 将退役，允许有记录的人工步骤；不改造通用材料锁，不建设应用升级/回滚平台。

## 工作区与路径

| 用途 | 路径/规则 |
| --- | --- |
| 本机用户解压位置 | `~/Downloads/ani-system-fast-r2/` |
| 新分支建议 | `feature/ani-system-fast-20260930`；已占用则新建带后缀的名字 |
| 本机新 worktree | 原 installer 仓库同级 `ani-installer-ani-system-fast-20260930/`；不切换原工作树 |
| 正式任务资料位置 | 新 worktree 内 `docs/execution/plans/ani-system-fast-r2/` |
| Fedora 执行 worktree | `/home/chabking/workspace/ani-installer-ani-system-fast-20260930-exec`，每阶段 detached 到本机固定 SHA |
| 既有输入目录 | `/home/chabking/ani-installer-runs/ani-system-fast/incoming/` |
| 本轮远端输出根 | `/home/chabking/ani-installer-runs/ani-system-fast-20260930/` |

复制规则：README、GOAL、PLAN、scripts/、materials/、templates/、tests/、TEST-REPORT.md 可以进入本任务新树。reference/ 中的原归档及原始核对资料先传 Fedora 仓外 private/reference/，不提交到 Git；相同原件已存在则复用。归档 source SHA 不等于当前应用镜像构建 SHA。顶层 SHA256SUMS 只用于核对完整输入任务包，不应复制为“代码仓库全文件清单”。

只有原参考包有内嵌脱敏资源；不能整包直接 apply。`materials/*.request.json` 是旧下载请求，保留供工具接口与组件用途查询，不是本轮镜像锁，也不是需要重新下载的任务。

## 阅读顺序

GOAL → PLAN（含 P1～P5）→ scripts/USAGE（材料处理时才读）→ 按需读取原参考。结果只维护一份 WORK-RESULTS.md，可从 templates/ 拷贝。不要求重新读 R/F/B 的历史整改。

本轮不自动执行任何命令；不含工作区创建脚本或一键连接生产集群脚本。branch/worktree 创建、镜像导入和现场部署由 agent 核对实际环境后完成。

# ANI installer C01–C10 定点整改资料

## 放置位置
在 ani-installer 仓库根（包含 kubekey/ 的那层）新建：
`docs/execution/remediation/20260927/`
将本包根目录文件解压到那里，保留 20260925/ 原整改资料和原台账。
如该目录已经有文件，先核对内容，不自动覆盖。

## 执行入口
先读 `ANI-installer-C01-C10-goal-20260927.txt`，再读
`ANI-installer-review-ee8c4cc-20260927.md`。
goal 给出本轮决定与权限；报告给出逐条问题、证据类型及最小整改建议。
复现包 `ANI-installer-review-ee8c4cc-repros-20260927.zip` 是原审查证据，
保持原件不变；需要时在独立临时目录解压并先读其 README。
其摘取片段不是可直接提交的生产回归或修复补丁。

## 本轮范围
在现有 review/installer-f-remediation-20260926 分支推进：
C01/C02/C03/C07 → C04/C08 → C05/C06/C09 → C10 最终门禁。
C10 固定 Chart 材料准备允许提前，解除干净门禁的输入前置。
允许本地/Fedora代码工作、隔离测试、代码包构建及 review 分支提交推送。
不操作三台实验VM或ESXi，不实机验收，不合并 main，不开展 B 系列。
已消耗专项额度、健康底座、历史证据和材料备份全部保留。

## 校验
在本目录执行 `sha256sum --check SHA256SUMS`，仅校验资料文件，不执行复现程序。
本包没有外层总目录。根目录包含 README、goal、报告、原复现ZIP 和 SHA256SUMS。

## 交付
候选完整SHA、C01–C10逐项结果、当前提交的真实CI状态、测试证据和代码包身份。
真实add记录链、持续离线冷启动及修复后现场验收未运行的，分别保留待测。
不能用旧门禁或旧live记录证明新代码已通过。

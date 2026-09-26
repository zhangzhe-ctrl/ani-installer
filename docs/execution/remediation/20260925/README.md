# ANI installer R 系列复审整改任务包

## 使用
把 `ANI-installer-R-remediation-goal-20260925.txt` 全文作为一条 goal 发给执行 agent，并将本任务包解压到它可以读取的任务资料目录。
让 agent 一并读取 `ANI-installer-R-remediation-spec-20260925.md`。细则与 goal 共同定义本轮任务；不用把整份细则再粘进 goal 输入框。

## 内容
- ANI-installer-R-remediation-goal-20260925.txt：一条可直接投递的整改 goal。
- ANI-installer-R-remediation-spec-20260925.md：执行边界、六组修复、43项最低行为回归、实机及交付标准。
- ANI-installer-main-audit-20260925.md：上一轮固定提交复审报告，保留 F01–F12 编号。
- ANI-installer-main-audit-evidence-20260925.zip：原始复现与建议回归证据包，未修改。
- SHA256SUMS：本任务包中以上文件及 README 的校验值，只用于检测传输变化，不是组件材料的审批锁。

## 重要区分
本次新增的是任务指令和执行细则，没有修改 GitHub 源码，没有重新运行产品测试或实机验收。依据的审查提交是 f58430e84a1949b837c59139dc45979c33ae5c4f；执行时应核对当前代码，不回退覆盖后续有效修改。
原证据包里建议的 review_regression_test.go.example 尚未在完整仓库编译，agent 须自行适配验证。原隔离复现发现的缺陷、历史 CI 和 R16 选定组合通过，不能代替本轮修复提交的验收。
代码、隔离测试、构建可先做；实机变更、快照恢复、重启与推送须遵守用户已有且有效的明确授权。

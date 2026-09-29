# 任务包说明 — r2：本机编码，Fedora 执行

日期：2026-09-29。此次修订只纠正工作主机分工和源码交接，不改变 Kube-OVN / 可选 RustFS 方案，不删除 RGW 选项，不增加 B08–B12 等需求。

## 放在哪里

**先放在本机正在编辑的 ani-installer 仓库**，相对路径为：

`docs/execution/plans/kubeovn-rustfs-optional-r2/`

本机绝对路径未提供，请在实际仓库用 `git rev-parse --show-toplevel` 确认。不要把 `/home/chabking/workspace/ani-installer-b00-b07` 等 Fedora 历史路径当成本机路径。

ZIP 已包含 `kubeovn-rustfs-optional-r2/` 外层，解压目标为本机仓库的 `docs/execution/plans/`。不要套两层同名目录。原 r1 保留为历史，执行时只读本 r2。

本机纳入任务文件并按阶段提交之后，Fedora 通过 Git fetch / 独立 worktree 取得同一 SHA；任务目录在远端源码中保持相同相对路径。不需要你手动维护两套说明。

## 文件

- `PLAN.md`：详细技术方案；第 12 节已改为新的主机边界。
- `PLAN.html`：由本 r2 Markdown 生成的离线浏览版。
- `GOAL.txt`：修订后的完整提示词，3,806 个字符（含换行），替换旧 Goal。
- `SHA256SUMS`：上述四个文件的完整性校验清单，不是组件材料锁；在 Fedora 执行 `sha256sum -c SHA256SUMS`。

## 唯一工作分工

**本机**：阅读、编写源码/测试/文档，Git commit/push，以及发起 Git/SSH/SCP 源码交接。

**Fedora**：材料准备、格式化/生成、静态检查、测试、渲染、编译、制包、诊断、证据和获准现场操作。不能在 Fedora 手工改产品代码或 commit/push。工具生成的源码候选带回本机审阅并提交。

**获准目标节点**：正式包的实际安装执行位置，由 Fedora 控制；不是本机，也不把 Fedora 冒充目标集群。

建议远端仓外产物目录：`/home/chabking/ani-installer-runs/kubeovn-rustfs/`。实际执行 worktree 先核对再建立，旧开发稿、材料及证据不覆盖。

## 注意

这是任务资料包，不是代码发行包，不包含 RustFS 镜像、凭据或私有 site。版本研究沿用原方案，尚需实施阶段核实；本次 r2 没有重新查询上游版本，也没有操作 GitHub、Fedora 或集群。

# 推送与 Draft PR 交接（2026-09-27）

## 已完成

分支 `review/installer-f-remediation-20260926` 已用现有正常 Git 凭据推送成功，未使用 force push：

    8f5a1cb..8d9de2c  review/installer-f-remediation-20260926 -> review/installer-f-remediation-20260926

远端 SHA 回读与本地 HEAD 完全一致：

    8d9de2c7a39cc3f8fd55ac83a3627978daa364ad

本轮两个提交：

- `8b9257e664eca26e375899aa17747b0bbe73608b` — C01–C10 代码、测试、门禁脚本与 workflow（36 个文件）
- `8d9de2c7a39cc3f8fd55ac83a3627978daa364ad` — 逐条台账与资料包（8 个文件）

同源代码包（门禁 rc=0，树指纹在 gate/build/package 三处一致）：

- 树 `40e1b894f24dafda22db7edb7a8aaaad928184864c31e985970efbb3f2b9a145`
- `kk` `d926604a1d007db8fd0fa3b3d6f1ba401b4329cddd09a1e1ffb7549cdc107863`
- SHA256SUMS 5/5 重算通过；包路径 `/tmp/ani-code-c10`（临时目录，不入库）

## 未完成：Draft PR 未创建

`gh` 的 GitHub token 无效，写操作 401：

    X Failed to log in to github.com account zhangzhe-ctrl (default)
    - The token in default is invalid.

推送能成功是因为 git 走的是另一套凭据（SSH/credential helper），与 `gh api` 的 token 无关。
公开仓库的只读 API 仍可访问，所以 CI 状态查询不受影响。

本轮没有扫描凭据库、没有向聊天索要明文 Token，因此 PR 交由用户手动创建。正文已写好，
未提交入库的临时副本在 `docs/execution/remediation/20260927/DRAFT-PR-BODY.md`。

创建命令（需先在会话里执行 `! gh auth login -h github.com` 恢复认证）：

    cd /home/chabking/workspace/ani-installer && \
    gh api -X POST repos/zhangzhe-ctrl/ani-installer/pulls \
      -f title="WIP: ANI installer C01-C10 targeted remediation (for review, do not merge)" \
      -f head=review/installer-f-remediation-20260926 \
      -f base=main -f draft=true \
      -f body="$(cat docs/execution/remediation/20260927/DRAFT-PR-BODY.md)" \
      --jq .html_url

或走网页：https://github.com/zhangzhe-ctrl/ani-installer/compare/main...review/installer-f-remediation-20260926

## CI

Run 36262250848 / job 108460180210，对应分支头 `8d9de2c7`（GitHub 只在分支头触发，
`8b9257e` 被同一分支的后续提交覆盖，没有单独 run）。实际结果见 C01-C10-LEDGER.md 末段，
未取到绿色之前不改写 `ci_pass`。

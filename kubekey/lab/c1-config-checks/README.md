# c1-config-checks 实验室脚本（参考副本）

本目录是 **2026-09-19 第二批 C1（typed 配置、八行选择文件、最小公共接线）轮** 在 `fedora` 上
实际使用的两个静态校验脚本的**参考副本**。它们不需要集群访问，只读取工作树里的文件。

这两个脚本**不进发布包**，也不被 `install.sh` / 任何 role 调用；它们只是把人肉做过的检查固化下来，
后续卡（C2–C4）改动了选择文件或角色模板后可以直接重跑。

## 文件清单

| 文件 | 作用 |
|---|---|
| `check-verify-selection.sh` | 从 `scripts/verify.sh` 里**抽出选择文件解析块**，喂 7 组合成选择文件，断言接受/拒绝行为正确（行数、行序、首行摘要格式、摘要不匹配、enabled 取值、缺首行）。 |
| `check-connection-fragments.sh` | 用真实模板上下文渲染四个角色 `templates/connection.md`，断言非空、无残留 `{{`/`<no value>`、含 namespace/retention/verification，携带站点存储值，且不含任何凭据。 |

## 用法

在 `fedora` 上、工作树根目录为 `SRC` 时：

```bash
bash kubekey/lab/c1-config-checks/check-verify-selection.sh "$SRC"
bash kubekey/lab/c1-config-checks/check-connection-fragments.sh "$SRC"
```

`check-connection-fragments.sh` 会写一个临时 Go 程序到 `/tmp` 并 `go run`，所以它需要 Go 工具链；
`check-verify-selection.sh` 只依赖 bash / grep / sed / awk / sha256sum。

两者的输出都以 `..._TESTS_DONE` 结尾表示全部通过，失败时打印 `..._TESTS_FAILED=<n>` 并以非零退出。

## 注意

- `check-verify-selection.sh` 假设 `verify.sh` 里仍存在
  `component_script="/etc/kubernetes/ani` 这一行作为抽取终点。若 C2–C4 改写了该行，
  脚本会抽取失败并显式报错，而不是静默跑出假通过——此时请同步更新抽取边界。
- 两个脚本都**不接触**测试集群 172.16.101.20/.21/.22，也不做任何安装、快照、重置操作。

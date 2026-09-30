# Fedora 材料接收工具用法（本轮无需重新下载）

所有下述处理在 Fedora 执行。本机只传输用户已经下载的包，不在本机运行镜像/构建工具。

## 1. 权威输入与历史版本

用户已批准其实际下载的 ANI 镜像版本。旧 `capturedRuntimeIdentityUnconfirmed` 或 `not_matched_needs_review` 表示未与参考运行环境匹配，本轮记为“用户批准采用实际下载身份”，不是要求重拉旧版或再次批准。`verify` 检查包内实际摘要，不比较旧镜像请求表；相同 tag 不能替代实际内容摘要。

本轮没有上传实际镜像给本方案作者，所以以下文件名为之前工具的输出约定；如用户有其它实际格式或路径，先识别再使用对应现有工具。

## 2. 核包与解包

建议输入目录 `/home/chabking/ani-installer-runs/ani-system-fast/incoming/`。
输出根 `/home/chabking/ani-installer-runs/ani-system-fast-20260930/`，使用新目录；输入归档不改写。先检查 tar 成员，拒绝绝对路径、路径越界和不安全链接，不覆盖已有目录。使用已有安全解包工具，无须开发新的归档系统。

按实际文件名执行外层校验；此前输出为：
```bash
cd /home/chabking/ani-installer-runs/ani-system-fast/incoming
sha256sum -c ani-images-bundle.tar.gz.sha256
```

若包内是 `ani-images-bundle/{bundle.json,SHA256SUMS,images/...}`：
```bash
# 以下两条路径由 agent 填成已核对的真实目录；只在 Fedora 执行。
python3 "$TASK_DIR/scripts/ani_images.py" verify --bundle "$BUNDLE_DIR"
```
`TASK_DIR` 是 Fedora 执行 worktree 中本任务资料目录；`BUNDLE_DIR` 是实际安全解包后的 ani-images-bundle 目录。verify只读、不联网，需要 Python。

PARTIAL 表示导出请求未全部完成，不可只改名冒充完整。可复用成功材料，同时按本轮实际应用选择确认真正缺项；缺料只阻塞依赖步骤。若文件内容与它自己的 metadata/清单矛盾，先报告内部问题，不删除校验或伪造旧包通过。

## 3. 导入内网可写仓库

先核对已存在 Harbor、项目、凭据和受信任CA。本任务可创建专用项目/robot；不得接管其它任务的项目/标签，不向只读 Hauler serve 推送，不更改共享鉴权或扫描规则。

```bash
# Fedora。TARGET为真实host[:port]/project，不含协议和密码。
python3 "$TASK_DIR/scripts/ani_images.py" push \
  --bundle "$BUNDLE_DIR" --registry "$TARGET"

# 核对预览后才加 --execute；RESULT在源包外，本任务独占。
python3 "$TASK_DIR/scripts/ani_images.py" push \
  --bundle "$BUNDLE_DIR" --registry "$TARGET" \
  --authfile "$AUTHFILE" --cert-dir "$CERTDIR" \
  --result "$RESULT" --execute
```

这些环境变量是说明用的实际路径/地址占位，未设置不能直接执行。工具不会创建项目，不执行 kubectl；认证文件留 Fedora 私有目录，不入包或Git。

输出 `image-map.json`，用 `targetDigestRef` 更新部署与动态注入字段。归档 config、单平台 manifest 和多架构 index 的 digest 不可混写；需要转换时记录来源和目标身份，不篡改原始内容。

## 4. 工具边界

`ani_images.py` 是上一包的原样工具，保留export/verify/push能力；本轮只需verify/push。`materials/*.request.json` 只保存旧请求，不能当实际镜像锁，不自动运行export。用户若采用Docker/OCI等其它格式，按实际内容使用现有导入工具，不强行让这个helper处理未知格式。

校验通过不是发布者签名验证，也不是ANI启动通过。实际镜像的配置/API/迁移兼容性仍在P2～P5按最小范围核实。

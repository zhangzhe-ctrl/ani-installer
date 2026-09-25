#!/usr/bin/env bash
# run_on_node.sh — reusable fedora->node runner (no sshpass; ASKPASS auth).
# Usage: run_on_node.sh <node-ip> <fedora-payload-file> [sudo]
#   Uploads the payload to a per-invocation unique remote path and runs it
#   (as root if 3rd arg = sudo). The payload is executed only after the upload
#   step succeeded; a failed upload exits non-zero without running anything.
#
# Credentials are never stored in this file: the ASKPASS helper supplies the
# SSH password, and sudo mode reads the node password from a 0600 file.
# Missing credentials or an unreadable payload fail before any connection.
set -uo pipefail
IP="${1:?need node ip}"
PAYLOAD="${2:?need payload file on fedora}"
SUDO="${3:-}"

ACCESS="${NODE_ACCESS_DIR:-/home/chabking/ani-installer-runs/platform-20260918/access}"
PWFILE="${NODE_PWFILE:-$ACCESS/node-password}"
ASKPASS="${NODE_ASKPASS:-$ACCESS/askpass.sh}"
SSHOPT="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=15 -o NumberOfPasswordPrompts=1"
NODE="ubuntu@$IP"

err() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] [FAIL] $*" >&2; }

# 凭据/输入预检：必须早于任何 ssh 连接；错误消息不含秘密值。
preflight() {
  [ -f "$PAYLOAD" ] && [ -r "$PAYLOAD" ] || { err "payload 不可读：$PAYLOAD"; exit 66; }
  [ -f "$ASKPASS" ] && [ -x "$ASKPASS" ] || { err "ASKPASS 助手不存在或不可执行：$ASKPASS"; exit 67; }
  if [ "$SUDO" = "sudo" ]; then
    local mode
    [ -f "$PWFILE" ] || { err "sudo 模式需要普通文件形式的节点密码文件：$PWFILE"; exit 67; }
    [ -r "$PWFILE" ] || { err "节点密码文件不可读：$PWFILE"; exit 67; }
    [ -s "$PWFILE" ] || { err "节点密码文件为空：$PWFILE"; exit 67; }
    mode="$(stat -c '%a' "$PWFILE" 2>/dev/null || echo unknown)"
    if [ "$mode" != "600" ] && [ "$mode" != "400" ]; then
      err "节点密码文件权限必须为 0600（只读挂载下允许 0400；当前 $mode）：$PWFILE"
      exit 67
    fi
  fi
}

rsh() { SSH_ASKPASS="$ASKPASS" SSH_ASKPASS_REQUIRE=force setsid -w ssh $SSHOPT "$NODE" "$@"; }

preflight

# 每次调用使用独立的远程 payload 路径（pid+纳秒+随机数）：
# 并发调用在正常情形下不会互相覆盖，也不再共用固定 /tmp/_ron.sh，
# 因此不存在“上传失败却执行了上一次残留脚本”的固定路径问题。
# 残余风险（已在 status-decision 记录，不在本卡扩大范围）：
#   1) 唯一性是概率性的，不是原子创建；同一 PID 命名空间内极端情况下仍可能碰撞；
#   2) 上传与执行是两次 SSH 会话，存在上传后被执行前替换的 TOCTOU 窗口；
#   3) 若第二次会话失败，远端会留下当次文件（路径唯一，不污染其他调用）。
REMOTE="/tmp/_ron.$$.$(date +%s%N).$RANDOM.sh"

if ! rsh "cat > '$REMOTE'" < "$PAYLOAD"; then
  err "payload 上传失败：$PAYLOAD -> $NODE:$REMOTE（未执行任何远程脚本）"
  exit 70
fi

if [ "$SUDO" = "sudo" ]; then
  # sudo -S 从凭据文件读密码；脚本已在节点上
  rsh "sudo -S -p '' bash '$REMOTE'; rc=\$?; rm -f '$REMOTE'; exit \$rc" < "$PWFILE"
  rc=$?
else
  rsh "bash '$REMOTE'; rc=\$?; rm -f '$REMOTE'; exit \$rc"
  rc=$?
fi

exit "$rc"

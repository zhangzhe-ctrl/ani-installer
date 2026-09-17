#!/usr/bin/env bash
#
# restore_esxi_snapshots.sh
#
# 在「单台 ESXi 主机」上，只还原白名单内的虚拟机快照。
#
# 拓扑（重要）：
#   - ESXi 宿主机（管理网）：172.16.255.12   凭据 root/Beta123@   版本 8.0.3
#   - 目标虚拟机（业务网 172.16.101.0/24）：
#       172.16.101.20  test-installer-01
#       172.16.101.21  test-installer-02
#       172.16.101.22  test-installer-03
#   - 同一宿主机上可能还有其它虚拟机（如 .10/.11/.12/.30/.31 等）——一律不许触碰。
#
# 安全设计：
#   - 硬白名单：只有「客户机 IP ∈ ALLOWED_IPS」的虚拟机才可能被还原；
#     若某目标 VM 处于关机、拿不到 IP，则退化为「名称 ∈ ALLOWED_NAMES」匹配。
#   - 任何不在白名单内的虚拟机：只打印、只跳过，绝不执行任何写操作。
#   - 默认 dry-run（只枚举+显示将要对哪些 VM 操作，不做任何改动）。
#     确认无误后再用 execute 真正还原。
#
# 运行位置：在能通过 SSH 访问到 ESXi(172.16.255.12) 的机器上运行（本例为 fedora）。
# 依赖：bash、ssh、setsid、sed/grep/sort（无需 sshpass，使用 SSH_ASKPASS 方式免交互登录）。
#
# 用法：
#   bash restore_esxi_snapshots.sh            # 等价于 dry-run
#   bash restore_esxi_snapshots.sh dry-run    # 只枚举、只显示计划，不改动
#   bash restore_esxi_snapshots.sh execute    # 真正还原白名单内的 VM
#
set -uo pipefail

# ========================== 可配置区 ==========================

ESXI_HOST="${ESXI_HOST:-172.16.255.12}"
ESXI_USER="${ESXI_USER:-root}"
ESXI_PASS="${ESXI_PASS:-Beta123@}"
ESXI_SSH_PORT="${ESXI_SSH_PORT:-22}"

# 硬白名单：只有这些「客户机 IP」对应的 VM 才允许被还原
ALLOWED_IPS=(
  "172.16.101.20"
  "172.16.101.21"
  "172.16.101.22"
)
# 名称白名单：仅当目标 VM 关机、拿不到客户机 IP 时作为退化匹配依据
ALLOWED_NAMES=(
  "test-installer-01"
  "test-installer-02"
  "test-installer-03"
)

# 还原后期望电源状态：true=确保开机（供后续任务使用）；false=保持快照记录的状态
ENSURE_POWER_ON="true"

# 还原后从本机 ping 目标 IP，等待其就绪
WAIT_FOR_PING="true"
PING_TIMEOUT="240"
PING_INTERVAL="5"

SSH_OPTS=(
  -o StrictHostKeyChecking=no
  -o UserKnownHostsFile=/dev/null
  -o ConnectTimeout=10
  -o NumberOfPasswordPrompts=1
  -o PreferredAuthentications=keyboard-interactive,password
  -o KbdInteractiveAuthentication=yes
  -o PubkeyAuthentication=no
  -o HostKeyAlgorithms=+ssh-rsa
)

# ========================== 配置区结束 ==========================

export ESXI_PASS   # 供 SSH_ASKPASS 助手进程读取，避免把密码写进磁盘文件

MODE="${1:-dry-run}"

# ---------------- 日志辅助 ----------------
_ts() { date '+%Y-%m-%d %H:%M:%S'; }
log()  { echo "[$(_ts)] $*"; }
ok()   { echo "[$(_ts)] [ OK ] $*"; }
warn() { echo "[$(_ts)] [WARN] $*" >&2; }
err()  { echo "[$(_ts)] [FAIL] $*" >&2; }

# ---------------- 免交互密码登录（SSH_ASKPASS + setsid） ----------------
ASKPASS=""
setup_askpass() {
  ASKPASS="$(mktemp)"
  cat > "$ASKPASS" <<'ASKPASS_EOF'
#!/bin/sh
printf '%s\n' "$ESXI_PASS"
ASKPASS_EOF
  chmod 700 "$ASKPASS"
  trap 'rm -f "$ASKPASS"' EXIT
}

# 在 ESXi 主机上执行命令（单条命令字符串，管道/重定向在 ESXi 端解释）
# 注意：必须带 -n，否则 ssh 会吃掉 while read 循环的 stdin，导致只处理第一台 VM
esxi_ssh() {
  SSH_ASKPASS="$ASKPASS" SSH_ASKPASS_REQUIRE=force DISPLAY="${DISPLAY:-:0}" \
    setsid -w ssh -n "${SSH_OPTS[@]}" -p "$ESXI_SSH_PORT" "${ESXI_USER}@${ESXI_HOST}" "$@"
}

check_deps() {
  local missing=0 bin
  for bin in ssh setsid sed grep sort mktemp; do
    command -v "$bin" >/dev/null 2>&1 || { err "缺少依赖：$bin"; missing=1; }
  done
  [ "$missing" -eq 0 ] || exit 1
}

in_list() {
  local needle="$1"; shift
  local x
  for x in "$@"; do [ "$x" = "$needle" ] && return 0; done
  return 1
}

# ---------------- ESXi 数据采集 ----------------
test_connection() {
  esxi_ssh 'echo __ESXI_OK__; vmware -v 2>/dev/null' 2>/dev/null
}

# 输出：每行 "vmid<TAB>name"
list_vms() {
  esxi_ssh 'vim-cmd vmsvc/getallvms' 2>/dev/null \
    | awk 'NR>1 && $1 ~ /^[0-9]+$/ {print $1"\t"$2}'
}

# 采集单个 VM 的电源/快照/客户机IP，写入全局变量
probe_vm() {
  local vmid="$1" out
  out="$(esxi_ssh "echo @POWER@; vim-cmd vmsvc/power.getstate ${vmid} 2>/dev/null; echo @SNAP@; vim-cmd vmsvc/snapshot.get ${vmid} 2>/dev/null; echo @GUEST@; vim-cmd vmsvc/get.guest ${vmid} 2>/dev/null; echo @END@" 2>/dev/null)"

  VM_POWER="$(printf '%s\n' "$out" | sed -n '/@POWER@/,/@SNAP@/p' | grep -oiE 'Powered (on|off)' | head -n1)"
  local snapblock guestblock
  snapblock="$(printf '%s\n' "$out" | sed -n '/@SNAP@/,/@GUEST@/p')"
  guestblock="$(printf '%s\n' "$out" | sed -n '/@GUEST@/,/@END@/p')"

  VM_SNAPCOUNT="$(printf '%s\n' "$snapblock" | grep -c 'Snapshot Name')"
  VM_SNAPIDS="$(printf '%s\n' "$snapblock" | grep -oE 'Snapshot Id[[:space:]]*:[[:space:]]*[0-9]+' | grep -oE '[0-9]+$' | tr '\n' ' ' | sed 's/ *$//')"
  VM_SNAPNAMES="$(printf '%s\n' "$snapblock" | sed -nE 's/.*Snapshot Name[[:space:]]*:[[:space:]]*(.*)/\1/p' | tr '\n' ',' | sed 's/,$//')"
  # 客户机「主 IP」：get.guest 顶层 ipAddress 字段（第一个 scalar 形式）。
  # 这是 VMware Tools 上报的本机主 IP，精确且不含路由/网关/DNS/Pod 网段等噪声。
  VM_PRIMARY_IP="$(printf '%s\n' "$guestblock" | grep -m1 -oE 'ipAddress = "[0-9.]+"' | grep -oE '[0-9.]+' | head -n1)"
  VM_IPS="${VM_PRIMARY_IP}"
}

# ---------------- 主流程 ----------------
main() {
  case "$MODE" in
    dry-run|execute) ;;
    -h|--help) grep -E '^# ' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) err "未知模式：$MODE（应为 dry-run 或 execute）"; exit 64 ;;
  esac

  check_deps
  setup_askpass

  echo "=============================================================="
  echo " ESXi 快照还原（白名单模式）"
  echo "   模式        : $MODE"
  echo "   ESXi 宿主机 : ${ESXI_USER}@${ESXI_HOST}:${ESXI_SSH_PORT}"
  echo "   IP 白名单   : ${ALLOWED_IPS[*]}"
  echo "   名称白名单  : ${ALLOWED_NAMES[*]}（仅在拿不到IP时退化匹配）"
  echo "=============================================================="

  log "测试到 ESXi 的连接 ..."
  local conn
  conn="$(test_connection)"
  if ! printf '%s\n' "$conn" | grep -q '__ESXI_OK__'; then
    err "无法通过 SSH 登录 ESXi（${ESXI_HOST}）。请检查网络、SSH 服务是否开启、账号密码是否正确。"
    exit 1
  fi
  ok "ESXi 连接正常"
  printf '%s\n' "$conn" | grep -vi '__ESXI_OK__' | sed '/^$/d' | sed 's/^/    ESXi 版本信息: /'

  local vms
  vms="$(list_vms)"
  if [ -z "$vms" ]; then
    err "未能列出任何虚拟机（getallvms 返回为空）。"
    exit 1
  fi

  echo
  log "枚举宿主机上的全部虚拟机并判定是否为白名单目标 ..."
  echo "--------------------------------------------------------------"
  printf '%-6s %-22s %-12s %-6s %-28s %s\n' "VMID" "NAME" "POWER" "SNAP" "GUEST_IP" "判定"
  echo "--------------------------------------------------------------"

  # 收集目标： "vmid|snapid|name|matchedBy"
  TARGETS=()
  local vmid name matchedBy ip snapid
  while IFS=$'\t' read -r vmid name; do
    [ -z "${vmid:-}" ] && continue
    probe_vm "$vmid"

    # 判定是否目标
    matchedBy=""
    for ip in $VM_IPS; do
      if in_list "$ip" "${ALLOWED_IPS[@]}"; then matchedBy="IP=${ip}"; break; fi
    done
    if [ -z "$matchedBy" ] && in_list "$name" "${ALLOWED_NAMES[@]}"; then
      matchedBy="NAME=${name}(无IP,关机?)"
    fi

    local verdict
    if [ -n "$matchedBy" ]; then
      # 选择快照 id
      snapid=""
      if [ "${VM_SNAPCOUNT:-0}" -eq 1 ]; then
        snapid="$(echo "$VM_SNAPIDS" | awk '{print $1}')"
      fi
      verdict="★目标(${matchedBy})"
      if [ "${VM_SNAPCOUNT:-0}" -eq 0 ]; then
        verdict="$verdict 但无快照→跳过"
      elif [ "${VM_SNAPCOUNT:-0}" -gt 1 ]; then
        verdict="$verdict 但有${VM_SNAPCOUNT}个快照(预期1)→跳过"
      fi
      printf '%-6s %-22s %-12s %-6s %-28s %s\n' \
        "$vmid" "$name" "${VM_POWER:-?}" "${VM_SNAPCOUNT:-0}" "${VM_IPS:-<none>}" "$verdict"
      if [ -n "$snapid" ]; then
        TARGETS+=("${vmid}|${snapid}|${name}|${matchedBy}")
      fi
    else
      printf '%-6s %-22s %-12s %-6s %-28s %s\n' \
        "$vmid" "$name" "${VM_POWER:-?}" "${VM_SNAPCOUNT:-0}" "${VM_IPS:-<none>}" "—（非白名单，跳过）"
    fi
  done <<< "$vms"
  echo "--------------------------------------------------------------"

  echo
  log "白名单内、可安全还原的目标共 ${#TARGETS[@]} 台："
  if [ "${#TARGETS[@]}" -eq 0 ]; then
    warn "没有匹配到任何可还原的目标 VM。请确认目标 VM 是否开机（以便通过 IP 匹配）或名称是否正确。"
  else
    local t
    for t in "${TARGETS[@]}"; do
      IFS='|' read -r vmid snapid name matchedBy <<< "$t"
      echo "    - ${name} (vmid=${vmid}, snapshotId=${snapid}, 匹配依据 ${matchedBy})"
    done
  fi

  if [ "$MODE" = "dry-run" ]; then
    echo
    ok "dry-run 结束：未对任何虚拟机做出改动。确认上面的目标无误后，用 execute 模式执行还原。"
    exit 0
  fi

  # ---------------- execute：仅还原 TARGETS ----------------
  echo
  if [ "${#TARGETS[@]}" -eq 0 ]; then
    warn "execute 模式：无目标，未执行任何还原。"
    exit 0
  fi

  log "开始还原（只处理上面列出的 ${#TARGETS[@]} 台白名单 VM，其它一律不动）..."
  local reverted=0 t vmid snapid name matchedBy
  for t in "${TARGETS[@]}"; do
    IFS='|' read -r vmid snapid name matchedBy <<< "$t"
    log "  还原 ${name}(vmid=${vmid}) 到快照 id=${snapid} ..."
    if esxi_ssh "vim-cmd vmsvc/snapshot.revert ${vmid} ${snapid} false" >/dev/null 2>&1; then
      ok "  ${name}(vmid=${vmid}) 快照还原成功"
      reverted=$((reverted + 1))
      sleep 2
      if [ "$ENSURE_POWER_ON" = "true" ]; then
        local st
        st="$(esxi_ssh "vim-cmd vmsvc/power.getstate ${vmid}" 2>/dev/null | grep -oiE 'Powered (on|off)' | head -n1)"
        if printf '%s' "$st" | grep -qi 'on'; then
          log "  ${name}(vmid=${vmid}) 已开机"
        else
          log "  ${name}(vmid=${vmid}) 正在开机 ..."
          esxi_ssh "vim-cmd vmsvc/power.on ${vmid}" >/dev/null 2>&1
        fi
      fi
    else
      err "  ${name}(vmid=${vmid}) 快照还原失败"
    fi
  done

  # 还原后从本机 ping 目标 IP，确认可达
  if [ "$WAIT_FOR_PING" = "true" ] && command -v ping >/dev/null 2>&1; then
    echo
    log "等待目标 VM 网络就绪（ping 白名单 IP，最长 ${PING_TIMEOUT}s/台）..."
    local ip waited
    for ip in "${ALLOWED_IPS[@]}"; do
      waited=0
      while [ "$waited" -lt "$PING_TIMEOUT" ]; do
        if ping -c1 -W2 "$ip" >/dev/null 2>&1; then ok "  ${ip} 已可达"; break; fi
        sleep "$PING_INTERVAL"; waited=$((waited + PING_INTERVAL))
      done
      [ "$waited" -ge "$PING_TIMEOUT" ] && warn "  ${ip} 在 ${PING_TIMEOUT}s 内未就绪（可能仍在启动或被防火墙拦截 ICMP）"
    done
  fi

  echo
  ok "还原完成：成功还原 ${reverted}/${#TARGETS[@]} 台白名单虚拟机；其它虚拟机未被触碰。"
  [ "$reverted" -eq "${#TARGETS[@]}" ] && exit 0 || exit 3
}

main "$@"

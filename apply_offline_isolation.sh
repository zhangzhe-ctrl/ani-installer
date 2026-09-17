#!/usr/bin/env bash
#
# apply_offline_isolation.sh — reusable OFFLINE ISOLATION for the ANI installer
# target nodes (172.16.101.20/.21/.22).
#
# ── WHY THIS EXISTS (do not "simplify" it back to blackhole routes) ──────────
# The 2026-09-17 attempt forced offline with interface-less "blackhole" routes
#   (ip route add blackhole 0.0.0.0/1 ; ip route add blackhole 128.0.0.0/1).
# A blackhole route has NO output interface (ifindex 0) and 0.0.0.0/1 also covers
# the cluster's internal Service CIDR 10.96.0.0/16 and Pod CIDR 10.16.0.0/16.
# kubeadm's Go net route enumeration then aborts with:
#   "error: route ip+net: no such network interface"
# which failed the r6 install at [InitKubernetes | Generate kubeadm join token].
#
# This script instead enforces offline with netfilter (iptables/ip6tables) on the
# OUTPUT and FORWARD chains only. That leaves the kernel ROUTING TABLE CLEAN (no
# ifindex-0 routes), so kubeadm/KubeKey work normally. This is the same style of
# firewall-based enforcement the 2026-09-15 clean-snapshot install PASSED with.
#
# ── POLICY ──────────────────────────────────────────────────────────────────
# A dedicated chain ANI-OFFLINE is jumped-to from OUTPUT and FORWARD. It RETURNs
# (not ACCEPTs, so kube-proxy's KUBE-* chains still run) for:
#   loopback, ESTABLISHED/RELATED, 127/8, 10/8, 172.16/12, 192.168/16,
#   169.254/16 (link-local), and the management/VPN peer CIDR.
# Everything else (public internet) is DROPped. IPv6 mirrors this (allow lo,
# established, ::1, fe80::/10, fc00::/7; drop rest).
# INPUT is NEVER touched and the default route is NEVER removed, so inbound
# management SSH can never be locked out. Existing firewall is not flushed.
#
# ── RUN LOCATION / AUTH ─────────────────────────────────────────────────────
# Run ON `fedora` (the only host that reaches 172.16.101.x via its VPN).
# The node login+sudo password is read from a 0600 file via `sudo -S` on stdin —
# it never appears on argv and is never printed.
#
# ── USAGE ───────────────────────────────────────────────────────────────────
#   ./apply_offline_isolation.sh apply     # install isolation on all nodes
#   ./apply_offline_isolation.sh verify    # read-only: prove offline + clean routes
#   ./apply_offline_isolation.sh remove    # fully reverse (v4 + v6)
#
# ENV OVERRIDES: NODES, RUN, PWFILE, ASKPASS, MGMT_PEER_CIDR
set -u

MODE="${1:-verify}"
case "$MODE" in apply|verify|remove) ;; *) echo "usage: $0 {apply|verify|remove}"; exit 2;; esac

NODES="${NODES:-172.16.101.20 172.16.101.21 172.16.101.22}"
RUN="${RUN:-/home/chabking/ani-installer-runs/platform-20260918}"
PWFILE="${PWFILE:-$RUN/access/node-password}"
ASKPASS="${ASKPASS:-$RUN/access/askpass.sh}"
MGMT_PEER_CIDR="${MGMT_PEER_CIDR:-198.18.0.0/15}"
SSHOPT="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 -o NumberOfPasswordPrompts=1"

[ -f "$PWFILE" ] || { echo "FATAL: PWFILE missing: $PWFILE"; exit 3; }
mkdir -p "$(dirname "$ASKPASS")"; chmod 700 "$(dirname "$ASKPASS")" 2>/dev/null || true
printf '#!/usr/bin/env bash\ncat %s\n' "$PWFILE" > "$ASKPASS"; chmod 700 "$ASKPASS"

# ---- node-side payload (executed as root through sudo -S) ----
PAYLOAD_FILE="$(mktemp /tmp/ani-iso-payload.XXXXXX.sh)"
cat > "$PAYLOAD_FILE" <<'PEOF'
#!/usr/bin/env bash
set -u
MODE="$1"; PEER="${2:-198.18.0.0/15}"; CHAIN=ANI-OFFLINE
apply_v4() {
  iptables -N "$CHAIN" 2>/dev/null || true
  iptables -F "$CHAIN"
  iptables -A "$CHAIN" -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
  iptables -A "$CHAIN" -o lo -j RETURN
  iptables -A "$CHAIN" -d 127.0.0.0/8    -j RETURN
  iptables -A "$CHAIN" -d 10.0.0.0/8     -j RETURN
  iptables -A "$CHAIN" -d 172.16.0.0/12  -j RETURN
  iptables -A "$CHAIN" -d 192.168.0.0/16 -j RETURN
  iptables -A "$CHAIN" -d 169.254.0.0/16 -j RETURN
  iptables -A "$CHAIN" -d "$PEER"        -j RETURN
  iptables -A "$CHAIN" -j DROP
  iptables -D OUTPUT  -j "$CHAIN" 2>/dev/null || true
  iptables -I OUTPUT  1 -j "$CHAIN"
  iptables -D FORWARD -j "$CHAIN" 2>/dev/null || true
  iptables -I FORWARD 1 -j "$CHAIN"
}
apply_v6() {
  ip6tables -N "$CHAIN" 2>/dev/null || true
  ip6tables -F "$CHAIN"
  ip6tables -A "$CHAIN" -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
  ip6tables -A "$CHAIN" -o lo -j RETURN
  ip6tables -A "$CHAIN" -d ::1/128   -j RETURN
  ip6tables -A "$CHAIN" -d fe80::/10 -j RETURN
  ip6tables -A "$CHAIN" -d fc00::/7  -j RETURN
  ip6tables -A "$CHAIN" -j DROP
  ip6tables -D OUTPUT  -j "$CHAIN" 2>/dev/null || true
  ip6tables -I OUTPUT  1 -j "$CHAIN"
  ip6tables -D FORWARD -j "$CHAIN" 2>/dev/null || true
  ip6tables -I FORWARD 1 -j "$CHAIN"
}
remove_all() {
  for t in iptables ip6tables; do
    $t -D OUTPUT  -j "$CHAIN" 2>/dev/null || true
    $t -D FORWARD -j "$CHAIN" 2>/dev/null || true
    $t -F "$CHAIN" 2>/dev/null || true
    $t -X "$CHAIN" 2>/dev/null || true
  done
}
verify() {
  echo "host=$(hostname)"
  echo "-- ip route (must show NO blackhole/unreachable/prohibit) --"; ip route
  echo "-- ifindex0-route-count (want 0) --"; ip route | grep -cE 'blackhole|unreachable|prohibit'
  echo "-- OUTPUT jump present? --"; iptables -S OUTPUT | grep -F "$CHAIN" || echo "NO-JUMP"
  echo "-- public https://example.com (expect FAIL) --"; timeout 8 curl -sS -m 6 -o /dev/null -w 'code=%{http_code}\n' https://example.com 2>&1 || echo "curl-rc=$?"
  echo "-- public https://1.1.1.1 (expect FAIL) --"; timeout 8 curl -sS -m 6 -o /dev/null -w 'code=%{http_code}\n' https://1.1.1.1 2>&1 || echo "curl-rc=$?"
  echo "-- dns example.com (expect FAIL) --"; timeout 8 getent hosts example.com || echo "dns-failed"
  echo "-- mgmt 172.16.101.20:22 (expect OK) --"; timeout 5 bash -c 'cat < /dev/null > /dev/tcp/172.16.101.20/22' 2>/dev/null && echo "MGMT-OK" || echo "MGMT-FAIL"
}
case "$MODE" in
  apply)  apply_v4; apply_v6; echo "APPLIED on $(hostname)";;
  remove) remove_all; echo "REMOVED on $(hostname)";;
  verify) verify;;
esac
PEOF

push_run() {
  local ip="$1"
  echo "===================== $ip ($MODE) ====================="
  SSH_ASKPASS="$ASKPASS" SSH_ASKPASS_REQUIRE=force setsid -w \
    ssh $SSHOPT "ubuntu@$ip" 'cat > /tmp/ani-iso.sh' < "$PAYLOAD_FILE"
  SSH_ASKPASS="$ASKPASS" SSH_ASKPASS_REQUIRE=force setsid -w \
    ssh $SSHOPT "ubuntu@$ip" "sudo -S -p '' bash /tmp/ani-iso.sh '$MODE' '$MGMT_PEER_CIDR'; rm -f /tmp/ani-iso.sh" < "$PWFILE"
  echo "rc=$?"
}

for ip in $NODES; do push_run "$ip"; done
rm -f "$PAYLOAD_FILE"
echo "DONE mode=$MODE"

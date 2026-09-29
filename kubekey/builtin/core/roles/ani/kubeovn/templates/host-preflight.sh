#!/usr/bin/env bash
# Read-only host facts required before the Kube-OVN v1.16.6 manifest is applied.
set -euo pipefail

iface='{{ .ani.network.management_interface }}'
test -n "$iface" || { echo 'Kube-OVN management interface is empty' >&2; exit 1; }
ip -4 -o addr show dev "$iface" | grep -Eq ' inet [0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/' || {
  echo "Kube-OVN management interface $iface has no IPv4 address" >&2
  exit 1
}
mtu="$(ip -o link show dev "$iface" | awk '{for (i=1;i<=NF;i++) if ($i=="mtu") {print $(i+1); exit}}')"
case "$mtu" in ''|*[!0-9]*) echo "cannot read MTU of $iface" >&2; exit 1 ;; esac
test "$mtu" -gt 0 || { echo "invalid MTU $mtu on $iface" >&2; exit 1; }

test -r /proc/sys/net/ipv6/conf/all/disable_ipv6 || {
  echo 'kernel IPv6 setting is unavailable' >&2; exit 1;
}
test "$(cat /proc/sys/net/ipv6/conf/all/disable_ipv6)" = 0 || {
  echo 'IPv6 is disabled on this host; Kube-OVN v1.16.6 prerequisite is not met' >&2
  exit 1
}
for module in geneve openvswitch ip_tables iptable_nat; do
  if [ ! -d "/sys/module/$module" ]; then
    modinfo "$module" >/dev/null 2>&1 || {
      echo "Kube-OVN kernel module $module is not loaded or available" >&2
      exit 1
    }
  fi
done
printf 'Kube-OVN host prerequisite pass: host=%s kernel=%s interface=%s mtu=%s ipv6=enabled\n' \
  "$(hostname)" "$(uname -r)" "$iface" "$mtu"

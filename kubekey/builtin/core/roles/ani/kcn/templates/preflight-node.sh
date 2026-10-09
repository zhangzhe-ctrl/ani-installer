#!/usr/bin/env bash
set -euo pipefail
bootstrap='{{ .ani.network.bootstrap_address }}'
status=$(curl --noproxy '*' --cacert /etc/kubernetes/pki/ca.crt --connect-timeout 5 --max-time 15 \
  --silent --show-error --output /dev/null --write-out '%{http_code}' "https://${bootstrap}:6443/livez")
case "$status" in 200|401|403) ;; *) echo "KCN bootstrap API is unavailable: $status" >&2; exit 1 ;; esac
management='{{ .ani.network.management_interface }}'
for device in {{ join " " .ani.network.kcn.managedDevices }}; do
  test "$device" != "$management"
  test -d "/sys/class/net/$device"
  if ip -o addr show dev "$device" | grep -Eq ' inet6? '; then
    echo "KCN underlay device $device has a host address; refusing adoption" >&2
    exit 1
  fi
  if ip route show default | grep -Eq "(^| )dev $device( |$)"; then
    echo "KCN underlay device $device carries the default route" >&2
    exit 1
  fi
done

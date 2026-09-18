#!/usr/bin/env bash
#
# preplock.sh — 实验室准备：停掉开机自启的 unattended-upgrades，避免它占用 dpkg 锁
# 吃掉安装器有界等待时长。不改产品代码。在每台目标节点（.20/.21/.22）上各跑一次。
# 参考副本（原件：~/ani-installer-runs/foundation-20260918/lab/preplock.sh on fedora）。
echo "host=$(hostname)"
systemctl stop unattended-upgrades.service 2>/dev/null || echo "service-stop=noop"
for i in $(seq 1 18); do
  if [ -z "$(pgrep -x unattended-upgr || true)" ] && [ -z "$(pgrep -x apt-get || true)" ] && [ -z "$(pgrep -x dpkg || true)" ]; then
    echo "LOCK_FREE after=$((i*10))s"
    break
  fi
  sleep 10
done
echo "remaining unattended=$(pgrep -x unattended-upgr | tr '\n' ' ')"
echo "remaining apt=$(pgrep -x apt-get | tr '\n' ' ') dpkg=$(pgrep -x dpkg | tr '\n' ' ')"

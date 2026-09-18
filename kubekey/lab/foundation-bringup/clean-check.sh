#!/usr/bin/env bash
echo "host=$(hostname)"
for p in /opt/ani-installer /etc/kubernetes/admin.conf /var/lib/ani-installer; do
  if [ -e "$p" ]; then echo "DIRTY:$p"; else echo "CLEAN:$p"; fi
done
systemctl is-active containerd 2>/dev/null || echo "containerd=inactive"
systemctl is-active kubelet 2>/dev/null || echo "kubelet=inactive"
echo "-- sdb --"; lsblk -d -o NAME,SIZE /dev/sdb; blkid /dev/sdb 2>/dev/null || echo "sdb=no-filesystem"
echo "-- nics --"; ip -o addr show ens34 | awk '{print $2, $4}'
echo "-- iptables leftovers --"; iptables -S OUTPUT 2>/dev/null | grep -c ANI-OFFLINE
echo "-- uptime --"; uptime

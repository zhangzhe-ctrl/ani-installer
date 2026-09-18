#!/usr/bin/env bash
#
# b1-transfer.sh — 把离线物料传输到安装节点 172.16.101.20 并校验。
# 干净快照里没有这些包，每次全新拉起都必须执行。
# 参考副本（原件：~/ani-installer-runs/foundation-20260918/lab/b1-transfer.sh on fedora）。
# 调整 R / A 到你的工作区即可复用。节点密码只经 sudo -S stdin 传入，不落 argv。
set -euo pipefail
R=/home/chabking/ani-installer-runs/foundation-20260918
SRC=$R/releases
A=/home/chabking/ani-installer-runs/platform-20260918/access
export SSH_ASKPASS="$A/askpass.sh" SSH_ASKPASS_REQUIRE=force
rsh() { setsid -w ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=15 ubuntu@172.16.101.20 "$@"; }
rsh 'sudo -S -p "" install -d -o ubuntu -g ubuntu /opt/ani-installer/code /opt/ani-installer/artifacts /opt/ani-installer/site' < "$A/node-password"
tar -C "$SRC" -cf - ani-code-20260918-b1 | rsh 'tar -xf - -C /opt/ani-installer/code'
tar -C "$SRC" -cf - ani-artifact-ubuntu24-amd64-20260918-b1 | rsh 'tar -xf - -C /opt/ani-installer/artifacts'
rsh 'umask 077; cat > /opt/ani-installer/site/cluster.yaml' < "$R/inputs/site-b1-cluster.yaml"
rsh 'set -e
cd /opt/ani-installer/code/ani-code-20260918-b1 && sha256sum -c SHA256SUMS
cd /opt/ani-installer/artifacts/ani-artifact-ubuntu24-amd64-20260918-b1 && sha256sum -c SHA256SUMS > /tmp/artsum.log 2>&1; tail -1 /tmp/artsum.log
stat -c "%a %n" /opt/ani-installer/site/cluster.yaml
ls -1 /opt/ani-installer/code/ani-code-20260918-b1
ls -1 /opt/ani-installer/artifacts/ani-artifact-ubuntu24-amd64-20260918-b1
ls -1 /opt/ani-installer/artifacts/ani-artifact-ubuntu24-amd64-20260918-b1/charts/cert-manager'
echo TRANSFER_PASS

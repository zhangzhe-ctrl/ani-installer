#!/usr/bin/env bash
set -euo pipefail
R=~/ani-installer-runs/foundation-20260918
SRC=$R/releases
A=~/ani-installer-runs/platform-20260918/access
export SSH_ASKPASS="$A/askpass.sh" SSH_ASKPASS_REQUIRE=force
rsh() { setsid -w ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=15 ubuntu@172.16.101.20 "$@"; }
rsh 'sudo -S -p "" install -d -o ubuntu -g ubuntu /opt/ani-installer/code /opt/ani-installer/artifacts /opt/ani-installer/site' < "$A/node-password"
tar -C "$SRC" -cf - ani-code-20260918-b2 | rsh 'tar -xf - -C /opt/ani-installer/code'
tar -C "$SRC" -cf - ani-artifact-ubuntu24-amd64-20260918-b2 | rsh 'tar -xf - -C /opt/ani-installer/artifacts'
rsh 'umask 077; cat > /opt/ani-installer/site/cluster.yaml' < "$R/inputs/site-b2-cluster.yaml"
rsh 'umask 077; cat > "$HOME/.ani_pw"' < "$A/node-password"
rsh 'set -e
cd /opt/ani-installer/code/ani-code-20260918-b2 && sha256sum -c SHA256SUMS
cd /opt/ani-installer/artifacts/ani-artifact-ubuntu24-amd64-20260918-b2 && sha256sum -c SHA256SUMS > /tmp/artsum.log 2>&1; tail -1 /tmp/artsum.log
stat -c "%a %n" /opt/ani-installer/site/cluster.yaml
ls -1 /opt/ani-installer/code/ani-code-20260918-b2
ls -1 /opt/ani-installer/artifacts/ani-artifact-ubuntu24-amd64-20260918-b2/charts/cert-manager'
echo TRANSFER_PASS

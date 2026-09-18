#!/usr/bin/env bash
# Fedora-side launcher: runs verifyb2.sh on .20 as ubuntu through a pty so the
# inner `sudo bash verify.sh` receives the node password. Never exports KUBECONFIG.
set -euo pipefail
A=~/ani-installer-runs/platform-20260918/access
L=~/ani-installer-runs/foundation-20260918/lab
export SSH_ASKPASS="$A/askpass.sh" SSH_ASKPASS_REQUIRE=force
rsh() { setsid -w ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ubuntu@172.16.101.20 "$@"; }
rsh 'cat > /tmp/_ani_b2_verify.sh' < "$L/verifyb2.sh"
rsh 'cat > /tmp/_ani_b2_vrun.sh' <<'EOS'
#!/usr/bin/env bash
umask 077
( cat "$HOME/.ani_pw"; printf "\n" ) | script -qec "bash /tmp/_ani_b2_verify.sh" /dev/null
EOS
rsh 'chmod 700 /tmp/_ani_b2_verify.sh /tmp/_ani_b2_vrun.sh; bash /tmp/_ani_b2_vrun.sh'

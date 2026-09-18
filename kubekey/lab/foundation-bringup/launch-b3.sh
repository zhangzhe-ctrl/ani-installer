#!/usr/bin/env bash
set -euo pipefail
A=~/ani-installer-runs/platform-20260918/access
L=~/ani-installer-runs/foundation-20260918/lab
export SSH_ASKPASS="$A/askpass.sh" SSH_ASKPASS_REQUIRE=force
rsh() { setsid -w ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ubuntu@172.16.101.20 "$@"; }
rsh 'cat > /tmp/_ani_b3_install.sh' < "$L/nodeinstall-b3.sh"
rsh 'cat > /tmp/_ani_b3_launch.sh' <<'EOS'
#!/usr/bin/env bash
umask 077
( cat "$HOME/.ani_pw"; printf "\n" ) | script -qec "bash /tmp/_ani_b3_install.sh" /dev/null > /tmp/ani-install-B3-a1.stdout 2>&1
EOS
rsh 'chmod 700 /tmp/_ani_b3_install.sh /tmp/_ani_b3_launch.sh; setsid nohup bash /tmp/_ani_b3_launch.sh >/dev/null 2>&1 < /dev/null & echo LAUNCHED'
sleep 15
rsh 'echo "running=$(pgrep -c -f "kk ani install" || true)"; tail -4 /tmp/ani-install-B3-a1.stdout | tr -d "\r"'

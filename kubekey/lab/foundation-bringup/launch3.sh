#!/usr/bin/env bash
#
# launch3.sh — 把安装脚本推到 172.16.101.20 并以普通 ubuntu 用户后台拉起安装。
# 参考副本（原件：~/ani-installer-runs/foundation-20260918/lab/launch3.sh on fedora）。
# 依赖：fedora 上已配好 SSH_ASKPASS + access/askpass.sh；节点 .20 已离线隔离且物料已传输。
set -euo pipefail
A=/home/chabking/ani-installer-runs/platform-20260918/access
L=/home/chabking/ani-installer-runs/foundation-20260918/lab
export SSH_ASKPASS="$A/askpass.sh" SSH_ASKPASS_REQUIRE=force
rsh() { setsid -w ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ubuntu@172.16.101.20 "$@"; }
rsh 'cat > /tmp/_ani_b1_install.sh' < "$L/nodeinstall.sh"
rsh 'cat > /tmp/_ani_b1_launch.sh' <<'EOS'
#!/usr/bin/env bash
umask 077
( cat "$HOME/.ani_pw"; printf "\n" ) | script -qec "bash /tmp/_ani_b1_install.sh" /dev/null > /tmp/ani-install-B1-a1.stdout 2>&1
EOS
rsh 'chmod 700 /tmp/_ani_b1_install.sh /tmp/_ani_b1_launch.sh; setsid nohup bash /tmp/_ani_b1_launch.sh >/dev/null 2>&1 < /dev/null & echo LAUNCHED'
sleep 15
rsh 'echo "running=$(pgrep -c -f "kk ani install" || true)"; tail -4 /tmp/ani-install-B1-a1.stdout | tr -d "\r"'

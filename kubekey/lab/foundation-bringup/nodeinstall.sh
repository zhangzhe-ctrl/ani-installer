#!/usr/bin/env bash
#
# nodeinstall.sh — 在 172.16.101.20 上以普通 ubuntu 用户运行的实际安装入口。
# 参考副本（原件：~/ani-installer-runs/foundation-20260918/lab/nodeinstall.sh on fedora）。
# 无需手动 export KUBECONFIG；install.sh 内部用 sudo -E 继承环境。
set -uo pipefail
code_root=/opt/ani-installer/code/ani-code-20260918-b1
artifact_root=/opt/ani-installer/artifacts/ani-artifact-ubuntu24-amd64-20260918-b1
config=/opt/ani-installer/site/cluster.yaml
log="$HOME/ani-install-B1-a1.log"
cd "$HOME"
echo "WHOAMI=$(id -un) HOME=$HOME KUBECONFIG=${KUBECONFIG:-<unset>}"
echo "INSTALL_LAUNCH=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
set -o pipefail
bash "$code_root/install.sh" "$config" "$artifact_root" 2>&1 | tee "$log"
install_rc=${PIPESTATUS[0]}
printf 'INSTALL_EXIT=%s\n' "$install_rc"
printf 'INSTALL_END=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"

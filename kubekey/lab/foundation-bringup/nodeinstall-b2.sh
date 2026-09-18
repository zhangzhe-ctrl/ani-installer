#!/usr/bin/env bash
# Runs as the ordinary ubuntu user on 172.16.101.20. No manual KUBECONFIG export.
set -uo pipefail
code_root=/opt/ani-installer/code/ani-code-20260918-b2
artifact_root=/opt/ani-installer/artifacts/ani-artifact-ubuntu24-amd64-20260918-b2
config=/opt/ani-installer/site/cluster.yaml
log="$HOME/ani-install-B2-a1.log"
cd "$HOME"
echo "WHOAMI=$(id -un) HOME=$HOME KUBECONFIG=${KUBECONFIG:-<unset>}"
echo "INSTALL_LAUNCH=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
set -o pipefail
bash "$code_root/install.sh" "$config" "$artifact_root" 2>&1 | tee "$log"
install_rc=${PIPESTATUS[0]}
printf 'INSTALL_EXIT=%s\n' "$install_rc"
printf 'INSTALL_END=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"

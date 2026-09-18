#!/usr/bin/env bash
#
# verifyb1.sh — 在 172.16.101.20 上运行安装包自带的独立 verify.sh 做功能校验。
# 参考副本（原件：~/ani-installer-runs/foundation-20260918/lab/verifyb1.sh on fedora）。
set -uo pipefail
code_root=/opt/ani-installer/code/ani-code-20260918-b1
artifact_root=/opt/ani-installer/artifacts/ani-artifact-ubuntu24-amd64-20260918-b1
echo "VERIFY_WHOAMI=$(id -un)"
echo "KUBECONFIG=${KUBECONFIG:-<unset>}"
sudo bash "$code_root/verify.sh" /opt/ani-installer/site/cluster.yaml "$artifact_root"
echo "VERIFY_EXIT=$?"

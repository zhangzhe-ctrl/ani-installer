#!/usr/bin/env bash
# Payload executed on 172.16.101.20 as the ordinary ubuntu user via a pty.
# It escalates with sudo (password supplied over the pty, never on the command
# line) and runs the packaged verify.sh with no manual KUBECONFIG export.
set -uo pipefail
code_root=/opt/ani-installer/code/ani-code-20260918-b3
artifact_root=/opt/ani-installer/artifacts/ani-artifact-ubuntu24-amd64-20260918-b3
echo "VERIFY_WHOAMI=$(id -un)"
echo "KUBECONFIG=${KUBECONFIG:-<unset>}"
sudo bash "$code_root/verify.sh" /opt/ani-installer/site/cluster.yaml "$artifact_root"
echo "VERIFY_EXIT=$?"

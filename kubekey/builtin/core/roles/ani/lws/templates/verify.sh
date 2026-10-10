#!/usr/bin/env bash
set -euo pipefail
export ANI_RUN_KUBECONFIG="${ANI_VERIFY_KUBECONFIG:?}"
mode=probe
if [[ "${ANI_VERIFY_LEVEL:-}" == smoke ]]; then mode=check; fi
exec python3 /etc/kubernetes/ani/lws/manage.py "$mode" --output-dir "${ANI_VERIFY_OUTPUT_DIR:?}"

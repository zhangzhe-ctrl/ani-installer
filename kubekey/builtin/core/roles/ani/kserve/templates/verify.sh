#!/usr/bin/env bash
set -euo pipefail
export ANI_RUN_KUBECONFIG="${ANI_VERIFY_KUBECONFIG:?explicit verification kubeconfig is required}"
mode=probe
if [[ "${ANI_VERIFY_LEVEL:-}" == smoke ]]; then mode=check; fi
python3 /etc/kubernetes/ani/kserve/manage.py "$mode" --output-dir "${ANI_VERIFY_OUTPUT_DIR:?explicit evidence directory is required}"

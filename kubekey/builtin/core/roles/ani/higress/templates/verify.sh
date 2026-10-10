#!/usr/bin/env bash
set -euo pipefail
export ANI_RUN_KUBECONFIG="${ANI_VERIFY_KUBECONFIG:?explicit verification kubeconfig required}"
install -d -m 0700 "${ANI_VERIFY_OUTPUT_DIR:?explicit evidence directory required}"
mode=probe
if [[ "${ANI_VERIFY_LEVEL:-}" == smoke ]]; then mode=check; fi
python3 /etc/kubernetes/ani/higress/manage.py "$mode" --output-dir "${ANI_VERIFY_OUTPUT_DIR:?explicit evidence directory required}"

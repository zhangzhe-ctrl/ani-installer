#!/usr/bin/env bash
set -euo pipefail
: "${ANI_VERIFY_KUBECONFIG:?explicit kubeconfig required}"
: "${ANI_VERIFY_OUTPUT_DIR:?independent evidence directory required}"
test -f "$ANI_VERIFY_KUBECONFIG"
material='{{ .ani.artifact_root }}/manifests/kubeflow/{{ .ani.kubeflow.release }}'
python3 "$material/check.py" --site /etc/kubernetes/ani/kubeflow/site.json \
  --kubeconfig "$ANI_VERIFY_KUBECONFIG" --output "$ANI_VERIFY_OUTPUT_DIR"

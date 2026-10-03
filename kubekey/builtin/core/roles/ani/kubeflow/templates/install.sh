#!/usr/bin/env bash
set -euo pipefail
material='{{ .ani.artifact_root }}/manifests/kubeflow/{{ .ani.kubeflow.release }}'
test -f "$material/install.py"
python3 "$material/install.py" --site /etc/kubernetes/ani/kubeflow/site.json

#!/usr/bin/env bash
set -euo pipefail
k() { kubectl --kubeconfig '{{ .ani.run.kubeconfig }}' "$@"; }
ART='{{ .ani.artifact_root }}'
DIR=/etc/kubernetes/ani/harbor
k apply --server-side -f "$DIR/pvc.yaml"
k -n ani-harbor wait --for=jsonpath='{.status.phase}'=Bound pvc/ani-harbor-trivy-cache --timeout=300s
k apply --server-side -f "$DIR/seed-pod.yaml"
k -n ani-harbor wait --for=condition=Ready pod/ani-harbor-trivy-seed --timeout=300s
for item in db/trivy.db db/metadata.json java-db/trivy-java.db java-db/metadata.json; do
  k -n ani-harbor cp "$ART/scanner/$item" "ani-harbor-trivy-seed:/seed/trivy/$item" -c seed
done
for pair in \
  'db/trivy.db 4bf01c98f9af59d4ec9ab6c8f4f22740230830e8be9172970e74dcb8257cd53b' \
  'db/metadata.json eec14e933a21b2dba78c45d2c0de61a6408e14cee008a07908079e4cd6c905d7' \
  'java-db/trivy-java.db e99e2d1212f4f1ef8281f95ffa72b667ce3fb8d00db0c4b2ddfcc3283b87612c' \
  'java-db/metadata.json f730c0616742e306095594ef51359f81cd05e48cff224112a59c8fbd68449e31'; do
  set -- $pair
  actual="$(k -n ani-harbor exec ani-harbor-trivy-seed -c seed -- sha256sum "/seed/trivy/$1" | awk '{print $1}')"
  [ "$actual" = "$2" ] || { echo "scanner cache $1 digest mismatch: $actual" >&2; exit 1; }
done
k -n ani-harbor exec ani-harbor-trivy-seed -c seed -- chown -R 10000:10000 /seed/trivy
k -n ani-harbor delete pod ani-harbor-trivy-seed --wait=true --timeout=180s
echo 'B07 scanner database PVC seeded and verified from package bytes'

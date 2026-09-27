#!/usr/bin/env bash
set -euo pipefail
k() { kubectl --kubeconfig '{{ .ani.run.kubeconfig }}' "$@"; }
ART='{{ .ani.artifact_root }}'
HELM="$ART/bin/helm"
CHART="$ART/charts/harbor/1.19.2.tgz"
test -f '{{ .ani.run.kubeconfig }}'
test -x "$HELM"
test -s "$CHART"
echo '36d8eeb41b4df1aeff18c9af7709110a2fac2194b491d37957822b3359cd5e9a  '"$CHART" | sha256sum -c -
cat > /etc/kubernetes/ani/harbor/scanner-sha256.txt <<CHECKS
4bf01c98f9af59d4ec9ab6c8f4f22740230830e8be9172970e74dcb8257cd53b  $ART/scanner/db/trivy.db
eec14e933a21b2dba78c45d2c0de61a6408e14cee008a07908079e4cd6c905d7  $ART/scanner/db/metadata.json
e99e2d1212f4f1ef8281f95ffa72b667ce3fb8d00db0c4b2ddfcc3283b87612c  $ART/scanner/java-db/trivy-java.db
f730c0616742e306095594ef51359f81cd05e48cff224112a59c8fbd68449e31  $ART/scanner/java-db/metadata.json
CHECKS
sha256sum -c /etc/kubernetes/ani/harbor/scanner-sha256.txt
k get namespace >/dev/null
existing="$(k get namespace ani-harbor --ignore-not-found -o name)"
[ -z "$existing" ] || { echo "pre-existing ani-harbor namespace; refusing takeover" >&2; exit 1; }
if KUBECONFIG='{{ .ani.run.kubeconfig }}' "$HELM" status ani-harbor -n ani-harbor >/dev/null 2>&1; then
  echo 'pre-existing Harbor Helm release; first-install role cannot upgrade it' >&2
  exit 1
fi
k get sc '{{ (index .ani.components "harbor").storage_class }}' -o name >/dev/null
mode="$(k get sc '{{ (index .ani.components "harbor").storage_class }}' -o jsonpath='{.volumeBindingMode}')"
[ "$mode" = Immediate ] || { echo "Harbor offline scanner seed requires Immediate PVC binding; got $mode" >&2; exit 1; }
k get svc -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name} {.spec.ports[*].nodePort}{"\n"}{end}' |
while read -r owner ports; do
  for port in $ports; do
    case "$port" in
      30002|30003) echo "NodePort $port already used by $owner; refusing Harbor install" >&2; exit 1;;
    esac
  done
done
# The pinned external URL is a node address, not a DNS name that might resolve
# outside the offline site. TLS generation verifies this same IPv4 SAN.
test -n '{{ (index .ani.components "harbor").external_address }}'

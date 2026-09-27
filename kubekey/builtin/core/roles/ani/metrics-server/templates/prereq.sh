#!/usr/bin/env bash
# B03 refuses a foreign aggregation provider and kubelet TLS it cannot verify.
set -euo pipefail
KUBECONFIG_FILE="${ANI_RUN_KUBECONFIG:?}"
test -f "$KUBECONFIG_FILE"
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_FILE")
CA=/etc/kubernetes/pki/ca.crt
[ -s "$CA" ] || { echo "cluster CA $CA is absent on installerNode" >&2; exit 1; }
command -v openssl >/dev/null
command -v python3 >/dev/null
api=v1beta1.metrics.k8s.io
if [ -n "$("${KUBECTL[@]}" get apiservice "$api" --ignore-not-found -o name)" ]; then
  release="$("${KUBECTL[@]}" get apiservice "$api" -o jsonpath='{.metadata.annotations.meta\.helm\.sh/release-name}')"
  namespace="$("${KUBECTL[@]}" get apiservice "$api" -o jsonpath='{.metadata.annotations.meta\.helm\.sh/release-namespace}')"
  service="$("${KUBECTL[@]}" get apiservice "$api" -o jsonpath='{.spec.service.name}')"
  [ "$release:$namespace:$service" = 'ani-metrics-server:kube-system:ani-metrics-server' ] || {
    echo "foreign metrics.k8s.io APIService owner=$release namespace=$namespace service=$service; refusing a second provider" >&2
    exit 1
  }
fi
for ip in {{ range .ani.node_addresses }}'{{ . }}' {{ end }}; do
  log="$(mktemp)"
  if ! timeout 15 openssl s_client -connect "$ip:10250" -CAfile "$CA" -verify_ip "$ip" -verify_return_error < /dev/null > "$log" 2>&1 || ! grep -q 'Verify return code: 0 (ok)' "$log"; then
    echo "kubelet serving certificate for $ip:10250 is not trusted by the cluster CA or lacks the InternalIP SAN; B03 stops before writing resources" >&2
    tail -12 "$log" >&2
    rm -f "$log"
    exit 1
  fi
  rm -f "$log"
done

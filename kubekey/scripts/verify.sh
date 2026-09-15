#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG="${1:-$PWD/cluster.yaml}"
KUBECONFIG_FILE="${KUBECONFIG_FILE:-/etc/kubernetes/admin.conf}"
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_FILE")

if [[ ! -f "$CONFIG" ]]; then
  echo "site config not found: $CONFIG" >&2
  exit 1
fi
if [[ ! -f "$KUBECONFIG_FILE" ]]; then
  echo "kubeconfig not found: $KUBECONFIG_FILE" >&2
  exit 1
fi

CLUSTER_NAME="$(awk '/^[[:space:]]*name:/ {print $2; exit}' "$CONFIG")"
INSTALLER_NODE="$(awk '/^[[:space:]]*installerNode:/ {print $2; exit}' "$CONFIG")"
REGISTRY="$(awk -v installer="$INSTALLER_NODE" '
  $1 == "-" && $2 == "name:" && $3 == installer { found = 1; next }
  found && /^[[:space:]]*address:/ { print $2; exit }
' "$CONFIG")"
REGISTRY_PORT="$(awk '/^[[:space:]]*registry:/ { in_registry = 1; next }
  in_registry && /^[[:space:]]*port:/ { print $2; exit }
' "$CONFIG")"
[[ -n "$CLUSTER_NAME" && -n "$INSTALLER_NODE" && -n "$REGISTRY" && -n "$REGISTRY_PORT" ]]

systemctl is-active --quiet ani-image-registry.service
curl -fsS "http://$REGISTRY:$REGISTRY_PORT/v2/" >/dev/null
while IFS=$'\t' read -r original hauler_ref actual_digest use_location; do
  [[ "$original" == "original_ref" ]] && continue
  ref="${hauler_ref#*/}"
  repository="${ref%:*}"
  tag="${ref##*:}"
  curl -fsS -H 'Accept: application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json' \
    "http://$REGISTRY:$REGISTRY_PORT/v2/$repository/manifests/$tag" >/dev/null
done < "$ROOT/images/images.tsv"

node_count="$("${KUBECTL[@]}" get nodes -o name | wc -l)"
ready_count=$("${KUBECTL[@]}" get nodes -o jsonpath='{range .items[*]}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}' | grep -cx True)
[[ "$node_count" == "3" && "$ready_count" == "3" ]]

"${KUBECTL[@]}" wait --for=condition=Available deployment/envoy-gateway -n envoy-gateway-system --timeout=180s >/dev/null
"${KUBECTL[@]}" wait --for=condition=Ready pod/ani-smoke-backend -n ani-installer-smoke --timeout=180s >/dev/null
network_phase="$("${KUBECTL[@]}" get pod ani-smoke-network-client -n ani-installer-smoke -o jsonpath='{.status.phase}')"
[[ "$network_phase" == "Succeeded" ]]
network_response="$("${KUBECTL[@]}" logs ani-smoke-network-client -n ani-installer-smoke | tr -d '\r\n')"
[[ "$network_response" == "ANI-NETWORK-OK" ]]

client_phase="$("${KUBECTL[@]}" get pod ani-smoke-client -n ani-installer-smoke -o jsonpath='{.status.phase}')"
[[ "$client_phase" == "Succeeded" ]]
response="$("${KUBECTL[@]}" logs ani-smoke-client -n ani-installer-smoke | tr -d '\r\n')"
[[ "$response" == "ANI-INSTALLER-OK" ]]

image_count="$(awk 'NF && NR>1 { count++ } END { print count+0 }' "$ROOT/images/images.tsv")"
echo "ANI package verification passed: cluster=$CLUSTER_NAME registry=$image_count images, nodes=3, network=ANI-NETWORK-OK, Envoy HTTP=ANI-INSTALLER-OK"

#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG="${1:?usage: verify.sh CONFIG ARTIFACT_ROOT}"
ARTIFACT_ROOT="${2:?usage: verify.sh CONFIG ARTIFACT_ROOT}"
KUBECONFIG_FILE="${KUBECONFIG_FILE:-/etc/kubernetes/admin.conf}"
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_FILE")
PROBE="$ROOT/probe.sh"

CONFIG="$(cd "$(dirname "$CONFIG")" && pwd)/$(basename "$CONFIG")"
ARTIFACT_ROOT="$(cd "$ARTIFACT_ROOT" && pwd)"
IMAGE_TABLE="$ARTIFACT_ROOT/images/images.tsv"

if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
  echo "verify.sh must run as root because it writes runtime logs under /var/lib/ani-installer" >&2
  exit 1
fi
for path in "$CONFIG" "$KUBECONFIG_FILE" "$PROBE" "$ARTIFACT_ROOT/SHA256SUMS" "$IMAGE_TABLE"; do
  if [[ ! -s "$path" ]]; then
    echo "required verification input not found: $path" >&2
    exit 1
  fi
done

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

VERIFY_LOG_DIR="/var/lib/ani-installer/$CLUSTER_NAME/logs/verify-$(date +%Y%m%d-%H%M%S)-$$-$RANDOM"
mkdir -p "$VERIFY_LOG_DIR"

(
  cd "$ARTIFACT_ROOT"
  sha256sum --check --quiet SHA256SUMS
) > "$VERIFY_LOG_DIR/artifact-checksums.log" 2>&1

systemctl is-active --quiet ani-image-registry.service
curl -fsS "http://$REGISTRY:$REGISTRY_PORT/v2/" >/dev/null
while IFS=$'\t' read -r original hauler_ref actual_digest use_location; do
  [[ "$original" == "original_ref" ]] && continue
  ref="${hauler_ref#*/}"
  repository="${ref%:*}"
  tag="${ref##*:}"
  curl -fsS -H 'Accept: application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json' \
    "http://$REGISTRY:$REGISTRY_PORT/v2/$repository/manifests/$tag" >/dev/null
done < "$IMAGE_TABLE"

node_count="$("${KUBECTL[@]}" get nodes -o name | wc -l)"
ready_count=$("${KUBECTL[@]}" get nodes -o jsonpath='{range .items[*]}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}' | grep -cx True)
[[ "$node_count" == "3" && "$ready_count" == "3" ]]

"${KUBECTL[@]}" wait --for=condition=Available deployment/envoy-gateway -n envoy-gateway-system --timeout=180s >/dev/null
"${KUBECTL[@]}" wait --for=condition=Ready pod/ani-smoke-backend -n ani-installer-smoke --timeout=180s >/dev/null

set +e
KUBECONFIG_FILE="$KUBECONFIG_FILE" ANI_SMOKE_OUTPUT="$VERIFY_LOG_DIR" \
  bash "$PROBE" 2>&1 | tee "$VERIFY_LOG_DIR/verify.stdout"
PROBE_RC="${PIPESTATUS[0]}"
set -e
if [[ "$PROBE_RC" -ne 0 ]]; then
  echo "active smoke probe failed; exit=$PROBE_RC; log=$VERIFY_LOG_DIR/verify.stdout" >&2
  exit "$PROBE_RC"
fi

image_count="$(awk 'NF && NR>1 { count++ } END { print count+0 }' "$IMAGE_TABLE")"
echo "ANI artifact verification passed: cluster=$CLUSTER_NAME artifact=$ARTIFACT_ROOT registry=$image_count images, nodes=3, network=ANI-NETWORK-OK, Envoy HTTP=ANI-INSTALLER-OK"
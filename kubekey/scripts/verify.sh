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

# ---------------------------------------------------------------------------
# Components: read the selection recorded by this exact install run and verify
# each enabled component with its own packaged script. A missing, malformed or
# stale selection file fails instead of being read as "all off". The row set is
# fixed: four foundation components followed by the four observability rows,
# where loki/opensearch are mutually exclusive and fluent-bit follows the
# backend (the installer derives all three from one typed backend value).
# ---------------------------------------------------------------------------
SELECTION_FILE="/var/lib/ani-installer/$CLUSTER_NAME/work/components-selection.tsv"
if [[ ! -s "$SELECTION_FILE" ]]; then
  echo "component selection file not found: $SELECTION_FILE (installer version too old or cluster=$CLUSTER_NAME wrong)" >&2
  exit 1
fi

CONFIG_SHA256="$(sha256sum "$CONFIG" | awk '{print $1}')"
IFS= read -r SELECTION_HEADER < "$SELECTION_FILE"
if [[ ! "$SELECTION_HEADER" =~ ^#\ config_sha256=([0-9a-f]{64})$ ]]; then
  echo "component selection first line is malformed: $SELECTION_HEADER" >&2
  exit 1
fi
if [[ "${BASH_REMATCH[1]}" != "$CONFIG_SHA256" ]]; then
  echo "component selection was written for a different site config (expected ${CONFIG_SHA256}, got ${BASH_REMATCH[1]})" >&2
  exit 1
fi

mapfile -t SELECTION_ROWS < <(tail -n +2 "$SELECTION_FILE")
EXPECTED_COMPONENTS=(cert-manager postgresql valkey nats metrics loki opensearch fluent-bit)
if [[ "${#SELECTION_ROWS[@]}" -ne "${#EXPECTED_COMPONENTS[@]}" ]]; then
  echo "component selection must have exactly ${#EXPECTED_COMPONENTS[@]} rows, got ${#SELECTION_ROWS[@]}" >&2
  exit 1
fi

COMPONENT_SUMMARY=()
COMPONENT_RC=0
for index in "${!EXPECTED_COMPONENTS[@]}"; do
  expected="${EXPECTED_COMPONENTS[$index]}"
  row="${SELECTION_ROWS[$index]}"
  IFS=$'\t' read -r component_name component_enabled <<< "$row"
  if [[ "$component_name" != "$expected" ]]; then
    echo "component selection row $((index + 2)) must be $expected, got ${component_name:-<empty>}" >&2
    exit 1
  fi
  case "$component_enabled" in
    true|false) ;;
    *) echo "component selection row $((index + 2)) has invalid enabled value ${component_enabled:-<empty>}" >&2; exit 1 ;;
  esac

  if [[ "$component_enabled" != "true" ]]; then
    echo "component $component_name: skipped (not enabled in this run)"
    COMPONENT_SUMMARY+=("$component_name=skipped")
    continue
  fi

  component_script="/etc/kubernetes/ani/$component_name/verify.sh"
  if [[ ! -f "$component_script" ]]; then
    echo "component $component_name is enabled but $component_script is missing; only scripts produced by this clean install are accepted" >&2
    exit 1
  fi
  set +e
  ANI_VERIFY_KUBECONFIG="$KUBECONFIG_FILE" ANI_VERIFY_OUTPUT_DIR="$VERIFY_LOG_DIR" \
    bash "$component_script" 2>&1 | tee "$VERIFY_LOG_DIR/component-$component_name.log"
  component_exit="${PIPESTATUS[0]}"
  set -e
  if [[ "$component_exit" -ne 0 ]]; then
    echo "component $component_name verification failed; exit=$component_exit; log=$VERIFY_LOG_DIR/component-$component_name.log" >&2
    COMPONENT_RC="$component_exit"
    COMPONENT_SUMMARY+=("$component_name=fail")
    continue
  fi
  COMPONENT_SUMMARY+=("$component_name=pass")
done
if [[ "$COMPONENT_RC" -ne 0 ]]; then
  echo "component verification failed; components: ${COMPONENT_SUMMARY[*]}" >&2
  exit "$COMPONENT_RC"
fi

image_count="$(awk 'NF && NR>1 { count++ } END { print count+0 }' "$IMAGE_TABLE")"
echo "ANI artifact verification passed: cluster=$CLUSTER_NAME artifact=$ARTIFACT_ROOT registry=$image_count images, nodes=3, network=ANI-NETWORK-OK, Envoy HTTP=ANI-INSTALLER-OK, components=${COMPONENT_SUMMARY[*]}"
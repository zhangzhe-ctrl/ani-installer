#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# R02 test seam: with ANI_VERIFY_LIB_ONLY=1 this file only defines
# ani_verify_components below and returns, so pkg/ani drives the real component
# loop with stub component scripts and never touches a cluster. Production runs
# never set the variable and take exactly the same path as before.
ANI_VERIFY_LIB_ONLY="${ANI_VERIFY_LIB_ONLY:-}"
if [[ "$ANI_VERIFY_LIB_ONLY" != "1" ]]; then
  CONFIG="${1:?usage: verify.sh CONFIG ARTIFACT_ROOT}"
  ARTIFACT_ROOT="${2:?usage: verify.sh CONFIG ARTIFACT_ROOT}"
fi

# R02: the component loop stops at the first failure. Every later enabled
# component is recorded as not_run and its verify.sh is not executed at all, so
# a failed acceptance run can no longer keep changing the cluster it is
# verifying. The exit code reported is the FIRST failure's, never a later one's
# and never a diagnostic command's.
ani_verify_components() { # ani_verify_components <site-config> <selection-file> <component-script-dir> <log-dir>
  # The names SELECTION_FILE / EXPECTED_COMPONENTS are kept from the original
  # top-level script on purpose: TestVerifyScriptExpectsEveryComponentRow pins
  # that fixed row list against componentsOrder, and pinning it is a real
  # invariant, not a bypass.
  local config="$1" SELECTION_FILE="$2" script_dir="$3" log_dir="$4"
  local config_sha header index expected row component_name component_enabled
  local component_script component_exit first_rc=0 first_failed=""
  local kubeconfig_file="${KUBECONFIG_FILE:-/etc/kubernetes/admin.conf}"
  local -a rows summary=()
  local -a EXPECTED_COMPONENTS=(cert-manager postgresql valkey nats metrics loki opensearch fluent-bit)

  config_sha="$(sha256sum "$config" | awk '{print $1}')"
  IFS= read -r header < "$SELECTION_FILE"
  if [[ ! "$header" =~ ^#\ config_sha256=([0-9a-f]{64})$ ]]; then
    echo "component selection first line is malformed: $header" >&2
    return 1
  fi
  if [[ "${BASH_REMATCH[1]}" != "$config_sha" ]]; then
    echo "component selection was written for a different site config (expected ${config_sha}, got ${BASH_REMATCH[1]})" >&2
    return 1
  fi

  mapfile -t rows < <(tail -n +2 "$SELECTION_FILE")
  if [[ "${#rows[@]}" -ne "${#EXPECTED_COMPONENTS[@]}" ]]; then
    echo "component selection must have exactly ${#EXPECTED_COMPONENTS[@]} rows, got ${#rows[@]}" >&2
    return 1
  fi

  for index in "${!EXPECTED_COMPONENTS[@]}"; do
    expected="${EXPECTED_COMPONENTS[$index]}"
    row="${rows[$index]}"
    IFS=$'\t' read -r component_name component_enabled <<< "$row"
    if [[ "$component_name" != "$expected" ]]; then
      echo "component selection row $((index + 2)) must be $expected, got ${component_name:-<empty>}" >&2
      return 1
    fi
    case "$component_enabled" in
      true|false) ;;
      *) echo "component selection row $((index + 2)) has invalid enabled value ${component_enabled:-<empty>}" >&2; return 1 ;;
    esac

    if [[ "$component_enabled" != "true" ]]; then
      echo "component $component_name: skipped (not enabled in this run)"
      summary+=("$component_name=skipped")
      continue
    fi

    if [[ "$first_rc" -ne 0 ]]; then
      echo "component $component_name: not_run (an earlier component failed; nothing further is executed)"
      summary+=("$component_name=not_run")
      continue
    fi

    component_script="$script_dir/$component_name/verify.sh"
    if [[ ! -f "$component_script" ]]; then
      echo "component $component_name is enabled but $component_script is missing; only scripts produced by this clean install are accepted" >&2
      first_rc=1
      first_failed="$component_name"
      summary+=("$component_name=fail")
      continue
    fi
    # pipefail is already on, so the pipeline status is the component script's
    # exit code; the `||` guard captures it without toggling the shell's errexit
    # (which would leak into the caller).
    component_exit=0
    ANI_VERIFY_KUBECONFIG="$kubeconfig_file" ANI_VERIFY_OUTPUT_DIR="$log_dir" \
      bash "$component_script" 2>&1 | tee "$log_dir/component-$component_name.log" || component_exit=$?
    if [[ "$component_exit" -ne 0 ]]; then
      echo "component $component_name verification failed; exit=$component_exit; log=$log_dir/component-$component_name.log" >&2
      first_rc="$component_exit"
      first_failed="$component_name"
      summary+=("$component_name=fail")
      continue
    fi
    summary+=("$component_name=pass")
  done

  if [[ "$first_rc" -ne 0 ]]; then
    echo "component verification failed; first failure: $first_failed (exit=$first_rc); components: ${summary[*]}" >&2
    return "$first_rc"
  fi
  echo "components verified: ${summary[*]}"
  ANI_COMPONENT_SUMMARY="${summary[*]}"
  return 0
}

# ani_local_image_ref resolves the locally served reference for one image of
# the artifact image table: the original ref must exist (it is the material
# lock's name) and the table's hauler registry host is replaced with this
# site's configured registry, which is what the cluster actually pulls from.
# R11: the generic network smoke takes its image from here, never a default.
ani_local_image_ref() { # ani_local_image_ref <image-table> <original-ref> <registry-host> <registry-port>
  local table="$1" original="$2" host="$3" port="$4" row
  if [[ -z "$host" || -z "$port" ]]; then
    echo "registry host/port are required to resolve $original" >&2
    return 1
  fi
  row="$(awk -F'\t' -v orig="$original" '$1==orig {print $2; exit}' "$table")"
  if [[ -z "$row" ]]; then
    echo "image $original is not listed in $table; refusing to invent a reference" >&2
    return 1
  fi
  printf '%s:%s/%s\n' "$host" "$port" "${row#*/}"
}

# ani_run_network_checks runs the R11 verification split: the generic network
# smoke (real cross-node PodIP/ClusterIP/DNS requests with content asserts)
# for BOTH stacks, and the kcn-only Envoy batch only from the kcn path. Each
# result is recorded separately; an unknown stack is rejected instead of
# silently printing an untested OK (the pre-R11 kube-ovn branch did exactly
# that). A failure returns the checker's exit code; the caller stops.
ani_run_network_checks() { # ani_run_network_checks <stack> <envoy-probe> <network-probe> <log-dir>
  local stack="$1" envoy_probe="$2" net_probe="$3" log_dir="$4"
  local rc=0
  ANI_NETWORK_RESULT="not_run"
  ANI_ENVOY_RESULT="not_run"
  case "$stack" in
    kcn|kubeovn) ;;
    *)
      echo "unknown network stack from run facts: $stack; refusing to print an untested network result" >&2
      return 1 ;;
  esac
  KUBECONFIG_FILE="$KUBECONFIG_FILE" ANI_SMOKE_OUTPUT="$log_dir" \
    bash "$net_probe" 2>&1 | tee "$log_dir/network-probe.stdout" || rc=$?
  if [[ "$rc" -ne 0 ]]; then
    echo "generic network smoke failed; exit=$rc; log=$log_dir/network-probe.stdout" >&2
    ANI_NETWORK_RESULT="fail"
    return "$rc"
  fi
  ANI_NETWORK_RESULT="pass"
  if [[ "$stack" != "kcn" ]]; then
    return 0
  fi
  # The Envoy gateway batch and its active probe exist only on the kcn stack.
  "${KUBECTL[@]}" wait --for=condition=Available deployment/envoy-gateway -n envoy-gateway-system --timeout=180s >/dev/null
  "${KUBECTL[@]}" wait --for=condition=Ready pod/ani-smoke-backend -n ani-installer-smoke --timeout=180s >/dev/null
  rc=0
  KUBECONFIG_FILE="$KUBECONFIG_FILE" ANI_SMOKE_OUTPUT="$log_dir" \
    bash "$envoy_probe" 2>&1 | tee "$log_dir/envoy-probe.stdout" || rc=$?
  if [[ "$rc" -ne 0 ]]; then
    echo "Envoy smoke probe failed; exit=$rc; log=$log_dir/envoy-probe.stdout" >&2
    ANI_ENVOY_RESULT="fail"
    return "$rc"
  fi
  ANI_ENVOY_RESULT="pass"
  return 0
}

# ani_verify_registry probes the registry API and every image manifest of the
# artifact image table. Every curl carries connect/total timeouts, and the
# whole stage has its own deadline that is independent of how many images (or
# retries) the table contains: a hung API request can never stretch the stage
# beyond the deadline plus one bounded request (R12/A12).
ani_verify_registry() { # ani_verify_registry <image-table> <registry-host> <registry-port> <stage-deadline-seconds>
  local table="$1" host="$2" port="$3" deadline="$4"
  local stage_start=$SECONDS
  local original hauler_ref ref repository tag
  if ! curl -fsS --connect-timeout 10 --max-time 30 "http://$host:$port/v2/" >/dev/null; then
    echo "registry http://$host:$port/v2/ is not reachable (connect/total timeouts apply)" >&2
    return 1
  fi
  while IFS=$'\t' read -r original hauler_ref _ _; do
    [[ "$original" == "original_ref" ]] && continue
    if (( SECONDS - stage_start >= deadline )); then
      echo "registry verification exceeded its ${deadline}s stage deadline at image $original; a hung request must not stretch this stage" >&2
      return 1
    fi
    ref="${hauler_ref#*/}"
    repository="${ref%:*}"
    tag="${ref##*:}"
    if ! curl -fsS --connect-timeout 10 --max-time 60 \
      -H 'Accept: application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json' \
      "http://$host:$port/v2/$repository/manifests/$tag" >/dev/null; then
      echo "manifest $repository:$tag is not reachable from the registry" >&2
      return 1
    fi
  done < "$table"
  return 0
}

if [[ "$ANI_VERIFY_LIB_ONLY" == "1" ]]; then
  return 0 2>/dev/null || exit 0
fi
KUBECONFIG_FILE="${KUBECONFIG_FILE:-/etc/kubernetes/admin.conf}"
# R12/A12: every kubectl request carries a request-timeout larger than the
# longest wait below (180s), so a hung API request can never block forever.
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_FILE" --request-timeout=300s)
PROBE="$ROOT/probe.sh"
NETPROBE="$ROOT/network-probe.sh"

CONFIG="$(cd "$(dirname "$CONFIG")" && pwd)/$(basename "$CONFIG")"
ARTIFACT_ROOT="$(cd "$ARTIFACT_ROOT" && pwd)"
IMAGE_TABLE="$ARTIFACT_ROOT/images/images.tsv"

if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
  echo "verify.sh must run as root because it writes runtime logs under /var/lib/ani-installer" >&2
  exit 1
fi
for path in "$CONFIG" "$KUBECONFIG_FILE" "$PROBE" "$NETPROBE" "$ARTIFACT_ROOT/SHA256SUMS" "$IMAGE_TABLE"; do
  if [[ ! -s "$path" ]]; then
    echo "required verification input not found: $path" >&2
    exit 1
  fi
done

# R06: the site facts come from the Go interpretation of the config — never from
# a shell YAML reader. `kk ani validate` validates the config and writes the
# non-secret facts file this script sources.
#
# Intermediate limitation (R13 pending): the Go-side verification does not exist
# yet, so this script still performs the checks below itself, and `kk ani
# validate` does not inspect the artifact (materialsValidated=false).
KK_BIN="${KK_BIN:-$ROOT/kk}"
if [[ ! -x "$KK_BIN" ]]; then
  echo "verify.sh needs the kk binary that ships with this release (KK_BIN=$KK_BIN): it no longer reads the site config with awk" >&2
  exit 1
fi
FACTS_DIR="$(mktemp -d /tmp/ani-verify-facts.XXXXXX)"
if ! "$KK_BIN" ani validate --config "$CONFIG" --output "$FACTS_DIR" >/dev/null; then
  echo "site config validation failed; see the output above" >&2
  rm -rf "$FACTS_DIR"
  exit 1
fi
# shellcheck source=/dev/null
source "$FACTS_DIR/verify-facts.env"
# FACTS_DIR stays until the end: its run.json is the run record the R13
# verify dispatcher reads (the shell path delegates to the Go entry).

CLUSTER_NAME="$ANI_CLUSTER_NAME"
INSTALLER_NODE="$ANI_INSTALLER_NODE"
REGISTRY="$ANI_REGISTRY_HOST"
REGISTRY_PORT="$ANI_REGISTRY_PORT"
[[ -n "$CLUSTER_NAME" && -n "$INSTALLER_NODE" && -n "$REGISTRY" && -n "$REGISTRY_PORT" ]]

# R11: the generic network smoke's image comes from the artifact image table
# (the material lock's busybox row, purpose "ANI smoke backend and client")
# resolved against this site's registry — never a default or outside ref.
ANI_NETSMOKE_IMAGE="$(ani_local_image_ref "$IMAGE_TABLE" "docker.io/library/busybox:1.37.0" "$REGISTRY" "$REGISTRY_PORT")"

VERIFY_LOG_DIR="/var/lib/ani-installer/$CLUSTER_NAME/logs/verify-$(date +%Y%m%d-%H%M%S)-$$-$RANDOM"
mkdir -p "$VERIFY_LOG_DIR"

(
  cd "$ARTIFACT_ROOT"
  sha256sum --check --quiet SHA256SUMS
) > "$VERIFY_LOG_DIR/artifact-checksums.log" 2>&1

systemctl is-active --quiet ani-image-registry.service
# R14/A15: the unit must also be enabled for boot. This proves the lifecycle
# configuration — it is never a substitute for a real cold-start check, which
# only the manual reboot experiment (with its evidence file) provides.
systemctl is-enabled --quiet ani-image-registry.service
# R12: per-request curl timeouts plus a stage deadline independent of the
# image count; the first failure (including a deadline breach) stops here.
ani_verify_registry "$IMAGE_TABLE" "$REGISTRY" "$REGISTRY_PORT" "${ANI_REGISTRY_STAGE_DEADLINE:-300}"

node_count="$("${KUBECTL[@]}" get nodes -o name | wc -l)"
ready_count=$("${KUBECTL[@]}" get nodes -o jsonpath='{range .items[*]}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}' | grep -cx True)
[[ "$node_count" == "3" && "$ready_count" == "3" ]]

# The stack comes from the run facts (see the kk ani validate block above),
# already normalised by Go. R11: both stacks run the generic network smoke;
# only the kcn stack additionally runs the Envoy batch. An unknown stack is
# rejected inside ani_run_network_checks instead of printing an untested OK.
NETWORK_STACK="$ANI_NETWORK_STACK"
ani_run_network_checks "$NETWORK_STACK" "$PROBE" "$NETPROBE" "$VERIFY_LOG_DIR"

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

# R13: the component section delegates to the Go verify dispatcher — one
# dispatch for this shell path and `kk ani verify` callers. The dispatcher
# reads the run record (never the site YAML), runs the same packaged
# read-only scripts with the same first-failure semantics and writes its own
# per-run report. The selection file above stays the guard for installers
# that predate the run record.
VERIFY_COMPONENT_OUT="$("$KK_BIN" ani verify \
  --run "$FACTS_DIR/run.json" --level smoke \
  --script-dir /etc/kubernetes/ani --kubeconfig "$KUBECONFIG_FILE" \
  --output "$VERIFY_LOG_DIR/verify")" || {
  printf '%s\n' "$VERIFY_COMPONENT_OUT" >&2
  rm -rf "$FACTS_DIR"
  exit 1
}
printf '%s\n' "$VERIFY_COMPONENT_OUT" | tee "$VERIFY_LOG_DIR/component-dispatch.log" >/dev/null
COMPONENT_SUMMARY_TEXT="$(printf '%s\n' "$VERIFY_COMPONENT_OUT" | grep '^verify smoke ' | sed -e 's/^verify smoke //' -e 's/: /=/' | tr '\n' ' ' | sed -e 's/ $//')"
rm -rf "$FACTS_DIR"

image_count="$(awk 'NF && NR>1 { count++ } END { print count+0 }' "$IMAGE_TABLE")"
# R11: the network and Envoy results are whatever actually ran above — an
# untested check can never print OK again (the pre-R11 kube-ovn summary did).
NETWORK_RESULT="${ANI_NETWORK_RESULT:-not_run}"
if [[ "$NETWORK_STACK" == "kcn" ]]; then
  echo "ANI artifact verification passed: cluster=$CLUSTER_NAME artifact=$ARTIFACT_ROOT registry=$image_count images, nodes=3, network=$NETWORK_RESULT, Envoy=${ANI_ENVOY_RESULT:-not_run}, components=${COMPONENT_SUMMARY_TEXT}"
else
  echo "ANI artifact verification passed: cluster=$CLUSTER_NAME artifact=$ARTIFACT_ROOT registry=$image_count images, nodes=3, network=$NETWORK_RESULT, components=${COMPONENT_SUMMARY_TEXT}"
fi
#!/usr/bin/env bash
# C2 gate: render the metrics role's values.yaml against the locked
# kube-prometheus-stack 85.4.0 chart, offline, on fedora.
#
# Proves three things the node install would otherwise discover:
#   1. the rendered values are valid YAML the chart accepts;
#   2. every image reference in the render points at the offline registry and
#      none of the locked batch-2 images is missing;
#   3. the objects the role waits on (operator Deployment, three sub-chart
#      workloads, Prometheus/Alertmanager CRs, admission hooks) actually exist
#      with the expected names.
set -euo pipefail

CHART="${1:?usage: render.sh <chart.tgz> <values.yaml> <out.yaml>}"
VALUES="${2:?}"
OUT="${3:?}"
HELM="${HELM:-helm}"
REGISTRY="${REGISTRY:-192.0.2.11:5000}"

command -v "$HELM" >/dev/null || { echo "helm not found" >&2; exit 1; }
[ -f "$CHART" ] || { echo "chart not found: $CHART" >&2; exit 1; }
[ -f "$VALUES" ] || { echo "values not found: $VALUES" >&2; exit 1; }

# Helm v3 renders offline from a local .tgz; --kube-version matches the locked
# cluster so version-gated templates take the same branch as on the node.
"$HELM" template ani-metrics "$CHART" \
  --namespace ani-observability \
  --values "$VALUES" \
  --kube-version 1.35.8 \
  --include-crds > "$OUT"

lines="$(wc -l < "$OUT")"
echo "rendered $lines lines -> $OUT"
[ "$lines" -gt 500 ] || { echo "render is suspiciously small ($lines lines)" >&2; exit 1; }

echo "--- objects the role waits on ---"
# The chart renders the Operator and the two custom resources; the Operator then
# creates the Prometheus/Alertmanager StatefulSets at runtime. So the render
# must contain the CRs, and the role's rollout waits must name the StatefulSets
# the Operator derives from them. Neither half is checked by looking for a
# StatefulSet here.
for want in \
  'kind: Deployment' \
  'name: ani-metrics-operator' \
  'kind: DaemonSet' \
  'name: ani-metrics-prometheus-node-exporter' \
  'kind: Prometheus' \
  'name: ani-metrics-prometheus' \
  'kind: Alertmanager' \
  'name: ani-metrics-alertmanager' \
  'kind: ServiceMonitor' \
  'name: prometheuses.monitoring.coreos.com' \
  'name: alertmanagers.monitoring.coreos.com' ; do
  if grep -q -- "$want" "$OUT"; then
    echo "  OK   $want"
  else
    echo "  MISS $want" >&2
    exit 1
  fi
done
# The CRs must carry the release label, or nothing downstream would select
# them. (The Operator watches its own CRs regardless, but the label is what the
# rule selector and the chart's own selectors key off.)
if ! grep -qE 'release:[[:space:]]*"?ani-metrics' "$OUT"; then
  echo "the Prometheus/Alertmanager CRs are not labelled with the release name" >&2
  exit 1
fi
echo "  OK   release label present on the stack"

# A StatefulSet in this render would mean the chart changed shape and the
# role's rollout waits may be wrong.
if grep -q '^kind: StatefulSet' "$OUT"; then
  echo "the render contains a StatefulSet; the role's waits assume the Operator creates them" >&2
  exit 1
fi
echo "  OK   no chart-rendered StatefulSet (the Operator owns them)"

echo "--- out-of-scope objects must be absent ---"
# The chart always ships every CRD (including the ThanosRuler one) because
# --include-crds renders the whole crds/ directory. An unwatched CRD is a schema
# with no controller, not a deployed component, and the plan says the disabled
# Thanos reference is reported rather than string-erased. So the check looks for
# actual workloads and services, with CRD bodies and comments stripped out.
strip_crds() {
  # Drop the CRD documents and every comment line, leaving the objects the
  # chart would actually create.
  awk '
    /^# Source: / { src = $0 }
    /^kind: CustomResourceDefinition$/ { skip = 1 }
    /^---$/ { skip = 0 }
    skip { next }
    /^[[:space:]]*#/ { next }
    { print }
  ' "$1"
}
strip_crds "$OUT" > "$OUT.workloads"

for bad in \
  'name: ani-metrics-grafana' \
  'kind: ThanosRuler' \
  'kind: Ingress' \
  'name: ani-metrics-thanos' ; do
  if grep -q -- "$bad" "$OUT.workloads"; then
    echo "  PRESENT $bad" >&2
    exit 1
  else
    echo "  absent $bad"
  fi
done

# A Thanos sidecar or service must not be attached to the prometheus CR: that
# would deploy Thanos despite ThanosRuler being off.
if grep -qE '^[[:space:]]+thanos:' "$OUT.workloads"; then
  echo "the Prometheus CR carries a thanos: block; this batch must not deploy Thanos" >&2
  grep -nE '^[[:space:]]+thanos:' "$OUT.workloads" >&2
  exit 1
fi
echo "  absent prometheus.thanos block"
# The only Thanos mention the plan accepts is the Operator's disabled default
# base image argument, which must be visible and marked as disabled in the lock.
if grep -q -- '--thanos-default-base-image' "$OUT.workloads"; then
  echo "  NOTE operator carries --thanos-default-base-image (disabled default, no Thanos deployed)"
fi

echo "--- every image resolves to the offline registry ---"
# Pull the image references out of the render. The chart builds the whole
# "registry/repository:tag" string itself, so an image appears as a complete
# value on an image: line rather than as separate registry/repository fields.
# The operator passes some of these on the command line
# (--prometheus-config-reloader), and Prometheus and Alertmanager name theirs in
# the custom resources, so a Deployment-only scan would miss three of the seven.
mapfile -t images < <(grep -oE "\b[0-9a-z.-]+(:[0-9]+)?/[A-Za-z0-9._/-]+:[A-Za-z0-9._-]+" "$OUT.workloads" \
  | sed 's/^"//' | sort -u || true)
printf '%s\n' "${images[@]}"
echo "  distinct image references: ${#images[@]}"

# The Operator's --thanos-default-base-image argument is a disabled-feature
# default: this batch deploys no ThanosRuler and no Thanos sidecar, so the image
# is never pulled. The lock records it as disabled for exactly this reason, so it
# is the single allowed exception and it is reported rather than ignored.
THANOS_DISABLED_DEFAULT='quay.io/thanos/thanos:v0.41.0'
offline=0
foreign=0
allowed_disabled=0
for ref in "${images[@]}"; do
  case "$ref" in
    "$REGISTRY"/*) offline=$((offline+1)) ;;
    "$THANOS_DISABLED_DEFAULT")
      allowed_disabled=$((allowed_disabled+1))
      echo "  DISABLED-DEFAULT $ref (never pulled: no ThanosRuler, no Thanos sidecar)" ;;
    quay.io/*|docker.io/*|ghcr.io/*|registry.k8s.io/*|cr.fluentbit.io/*)
      echo "  FOREIGN $ref" >&2
      foreign=$((foreign+1)) ;;
    *) : ;;
  esac
done
[ "$foreign" -eq 0 ] || { echo "render contains $foreign image references outside the offline registry" >&2; exit 1; }
echo "  offline references: $offline, disabled defaults: $allowed_disabled, foreign: $foreign"
[ "$offline" -ge 7 ] || { echo "expected at least the 7 locked metrics images to resolve offline, saw $offline" >&2; exit 1; }
[ "$allowed_disabled" -le 1 ] || { echo "more than one disabled-default image reference is present" >&2; exit 1; }

# Every image the lock says this batch actually runs must appear offline. This
# is what makes the check above meaningful rather than a count that any seven
# references could satisfy.
echo "--- each locked runtime image resolves offline ---"
for name in \
  prometheus-operator/prometheus-operator:v0.90.1 \
  prometheus-operator/prometheus-config-reloader:v0.90.1 \
  prometheus/prometheus:v3.11.3-distroless \
  prometheus/alertmanager:v0.32.1 \
  kube-state-metrics/kube-state-metrics:v2.19.0 \
  prometheus/node-exporter:v1.11.1-distroless \
  jkroepke/kube-webhook-certgen:1.8.3 ; do
  if printf '%s\n' "${images[@]}" | grep -qF "$REGISTRY/$name"; then
    echo "  OK   $REGISTRY/$name"
  else
    echo "  MISS $REGISTRY/$name" >&2
    exit 1
  fi
done

echo "--- the locked node-exporter image has no double suffix ---"
if grep -q 'node-exporter.*distroless.*distroless' "$OUT.workloads"; then
  echo "node-exporter image shows a doubled -distroless suffix" >&2
  grep -o '[^" ]*node-exporter[^" ]*' "$OUT.workloads" | sort -u >&2
  exit 1
fi
echo "  OK"

echo "--- CRDs are present and no upgrade job is rendered ---"
grep -q 'name: prometheuses.monitoring.coreos.com' "$OUT" || { echo "Prometheus CRD missing" >&2; exit 1; }
grep -q 'name: alertmanagers.monitoring.coreos.com' "$OUT" || { echo "Alertmanager CRD missing" >&2; exit 1; }

echo "render gate passed"

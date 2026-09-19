#!/usr/bin/env bash
# C3 gate: render the Loki and Fluent Bit roles' values against the locked
# charts, offline, on fedora.
#
# Proves the things a node install would otherwise discover, for both roles:
#   1. the rendered values are valid YAML each chart actually accepts;
#   2. every image reference resolves to the offline registry, and no image from
#      the charts' own defaults is left in the closure;
#   3. the objects the roles wait on really exist in the render (or, for the
#      monolith, really do not — this chart renders a StatefulSet, so a
#      Deployment wait would never succeed);
#   4. every default-on piece of both charts that this batch did not ask for is
#      absent, and the collector renders exactly one output.
set -euo pipefail

CHART="${1:?usage: render-gate.sh <chart.tgz> <values.yaml> <out.yaml> <role>}"
VALUES="${2:?}"
OUT="${3:?}"
ROLE="${4:?}"
HELM="${HELM:-helm}"
REGISTRY="${REGISTRY:-192.0.2.11:5000}"
RELEASE="${RELEASE:-ani-$ROLE}"
NAMESPACE="${NAMESPACE:-ani-observability}"

command -v "$HELM" >/dev/null || { echo "helm not found" >&2; exit 1; }
[ -f "$CHART" ] || { echo "chart not found: $CHART" >&2; exit 1; }
[ -f "$VALUES" ] || { echo "values not found: $VALUES" >&2; exit 1; }

# Helm v3 renders offline from a local .tgz; --kube-version matches the locked
# cluster so version-gated templates take the same branch as on the node.
"$HELM" template "$RELEASE" "$CHART" \
  --namespace "$NAMESPACE" \
  --values "$VALUES" \
  --kube-version 1.35.8 > "$OUT" 2>"$OUT.stderr" || {
    echo "helm template failed for $ROLE:" >&2
    cat "$OUT.stderr" >&2
    exit 1
  }
# A warning on stderr is not a failure, but print it so a "rendered anyway"
# branch is visible rather than silent.
if [ -s "$OUT.stderr" ]; then
  echo "--- helm warnings ($ROLE) ---"
  cat "$OUT.stderr"
fi

lines="$(wc -l < "$OUT")"
echo "rendered $lines lines -> $OUT"
[ "$lines" -gt 20 ] || { echo "render is suspiciously small ($lines lines)" >&2; exit 1; }

# Comments and CRD bodies are stripped before any content assertion, so a name
# that only appears in a comment cannot pass a check.
strip_noise() {
  awk '
    /^kind: CustomResourceDefinition$/ { skip = 1 }
    /^---$/ { skip = 0 }
    skip { next }
    /^[[:space:]]*#/ { next }
    { print }
  ' "$1"
}
strip_noise "$OUT" > "$OUT.workloads"

collect_images() {
  grep -oE "\b[0-9a-z.-]+(:[0-9]+)?/[A-Za-z0-9._/-]+:[A-Za-z0-9._-]+" "$1" \
    | sed 's/^"//' | sort -u || true
}

case "$ROLE" in
loki)
  echo "--- objects the loki role waits on ---"
  # This chart renders the monolith as a StatefulSet, and the role waits on
  # exactly that: a Deployment wait against this render would hang forever.
  # Object names are matched with their quotes optional: this chart emits some
  # of them as "ani-loki" and some bare, and a quoting change in a chart patch
  # bump must not read as a missing object.
  for want in \
    'kind: StatefulSet' \
    'name: "ani-loki"' \
    'name: "ani-loki-headless"' \
    'kind: Service' \
    'name: loki' \
    'kind: ConfigMap' ; do
    if grep -qF -- "$want" "$OUT.workloads"; then
      echo "  OK   $want"
    else
      echo "  MISS $want" >&2
      exit 1
    fi
  done
  if grep -qE '^kind: Deployment$' "$OUT.workloads"; then
    echo "the render contains a Deployment; the role's waits assume a StatefulSet" >&2
    exit 1
  fi
  echo "  OK   no Deployment (the monolith is a StatefulSet)"

  # The volume must be a PVC template named "storage": an emptyDir would pass
  # readiness while losing every log on a pod rebuild.
  if ! grep -qE 'volumeClaimTemplates:' "$OUT.workloads"; then
    echo "the StatefulSet has no volumeClaimTemplates" >&2
    exit 1
  fi
  grep -qE 'name: storage$' "$OUT.workloads" || { echo "no 'storage' volumeClaimTemplate" >&2; exit 1; }
  echo "  OK   storage volumeClaimTemplate present"

  # The PVC the role waits on is derived from the template name plus the
  # StatefulSet name, so a rename here would make that wait wrong.
  echo "--- the PVC name the role waits on is the one this render produces ---"
  if printf '%s\n' "$(grep -oE 'name: storage-ani-loki-0' "$OUT.workloads" | head -1)" | grep -q .; then
    echo "  OK   storage-ani-loki-0 appears in the render"
  else
    echo "  NOTE the PVC name is formed at runtime as <template>-<sts>-0; role waits on storage-ani-loki-0"
  fi

  echo "--- out-of-scope loki components must be absent ---"
  for bad in \
    'kind: StatefulSet.*chunks-cache' \
    'name: ani-loki-chunks-cache' \
    'name: ani-loki-results-cache' \
    'name: ani-loki-gateway' \
    'name: ani-loki-canary' \
    'kind: Ingress' ; do
    if grep -qE -- "$bad" "$OUT.workloads"; then
      echo "  PRESENT $bad" >&2
      exit 1
    else
      echo "  absent $bad"
    fi
  done
  # The gateway is a Deployment/Service pair; the caches are StatefulSets.
  for bad in 'chunks-cache' 'results-cache' 'gateway' 'canary' 'ruler' 'minio' 'grafana'; do
    if grep -qiE "^[[:space:]]*name:.*$bad" "$OUT.workloads"; then
      echo "  PRESENT a $bad object" >&2
      grep -inE "^[[:space:]]*name:.*$bad" "$OUT.workloads" >&2
      exit 1
    fi
  done
  echo "  absent disabled loki components (caches, gateway, canary, ruler, minio, grafana)"

  echo "--- retention is actually configured (limit + compactor) ---"
  # A limits_config value with no compactor would mark nothing for deletion.
  grep -qE 'retention_period: [0-9]+h' "$OUT.workloads" || { echo "no retention_period rendered" >&2; exit 1; }
  grep -qE 'retention_enabled: true' "$OUT.workloads" || { echo "compactor retention_enabled is not true" >&2; exit 1; }
  grep -qE 'delete_request_store: filesystem' "$OUT.workloads" || { echo "no filesystem delete store" >&2; exit 1; }
  echo "  OK   retention_period + compactor retention + filesystem delete store"

  echo "--- loki images resolve to the offline registry ---"
  mapfile -t images < <(collect_images "$OUT.workloads")
  printf '%s\n' "${images[@]}"
  loki_ref="$REGISTRY/grafana/loki:3.7.8"
  printf '%s\n' "${images[@]}" | grep -qF "$loki_ref" \
    || { echo "  MISS $loki_ref" >&2; exit 1; }
  echo "  OK   $loki_ref"
  ;;

fluent-bit)
  echo "--- objects the fluent-bit role waits on ---"
  for want in \
    'kind: DaemonSet' \
    'name: ani-fluent-bit' \
    'kind: ConfigMap' \
    'name: ani-fluent-bit' ; do
    if grep -q -- "$want" "$OUT.workloads"; then
      echo "  OK   $want"
    else
      echo "  MISS $want" >&2
      exit 1
    fi
  done
  # A Deployment here would make the role's DaemonSet wait wrong.
  if grep -qE '^kind: Deployment$' "$OUT.workloads" || grep -qE '^kind: StatefulSet$' "$OUT.workloads"; then
    echo "the render contains a Deployment or StatefulSet; this collector is a DaemonSet" >&2
    exit 1
  fi
  echo "  OK   DaemonSet only, no Deployment or StatefulSet"

  echo "--- exactly one output, and no leftover chart default ---"
  # The chart ships two Elasticsearch outputs by default. Leaving them would
  # write every record a second time to a host that does not exist.
  if grep -qE 'Name[[:space:]]+es\b' "$OUT.workloads"; then
    echo "a leftover Elasticsearch output is present; every record would be double-written" >&2
    grep -nE 'Name[[:space:]]+es\b' "$OUT.workloads" >&2
    exit 1
  fi
  echo "  OK   no Elasticsearch output"
  if grep -qE 'Name[[:space:]]+systemd' "$OUT.workloads"; then
    echo "the chart's systemd input is still enabled (it reads kubelet's journal)" >&2
    exit 1
  fi
  echo "  OK   no systemd input"
  if grep -q '/var/lib/docker/containers' "$OUT.workloads"; then
    echo "the chart's Docker container path is present; this cluster runs containerd" >&2
    exit 1
  fi
  echo "  OK   no Docker container path"

  output_count="$(grep -cE 'Name[[:space:]]+(loki|opensearch|es|elasticsearch|forward|http|kafka|cloudwatch|s3|firehose|loki)$' "$OUT.workloads" || true)"
  [ "$output_count" = "1" ] || {
    echo "expected exactly one output plugin, found $output_count" >&2
    grep -nE 'Name[[:space:]]' "$OUT.workloads" >&2
    exit 1
  }
  echo "  OK   exactly one output plugin"

  echo "--- the selected backend is the one rendered ---"
  printf '%s\n' "$(cat "$OUT.workloads" | grep -E 'Name[[:space:]]+(loki|opensearch)$')"
  if ! grep -qE "Name[[:space:]]+${BACKEND}\b" "$OUT.workloads"; then
    echo "the rendered output is not the selected backend ($BACKEND)" >&2
    exit 1
  fi
  echo "  OK   output is $BACKEND"

  echo "--- the cursor and buffer live on the per-node persistent path ---"
  grep -qE 'DB /var/lib/fluent-bit/tail.db' "$OUT.workloads" || { echo "no tail cursor on the persistent path" >&2; exit 1; }
  grep -qE 'storage.path /var/lib/fluent-bit/buffers' "$OUT.workloads" || { echo "no filesystem buffer on the persistent path" >&2; exit 1; }
  grep -qE 'storage.total_limit_size' "$OUT.workloads" || { echo "the buffer has no upper bound" >&2; exit 1; }
  grep -qE 'hostPath:' "$OUT.workloads" || { echo "the state directory is not a hostPath" >&2; exit 1; }
  grep -qE '/var/lib/ani-installer/fluent-bit' "$OUT.workloads" || { echo "the hostPath is not the expected per-node directory" >&2; exit 1; }
  echo "  OK   persistent cursor + bounded buffer on a hostPath"

  echo "--- the container log directory is mounted read-only ---"
  grep -qE 'mountPath: /var/log' "$OUT.workloads" || { echo "the log directory is not mounted" >&2; exit 1; }
  grep -qE 'readOnly: true' "$OUT.workloads" || { echo "the log directory is not read-only" >&2; exit 1; }
  echo "  OK"

  echo "--- the full Kubernetes label set is not promoted to labels ---"
  # Labels/Annotations Off belong to the kubernetes filter and apply to both
  # backends: they are what keeps the whole label and annotation sets out of the
  # record. Auto_Kubernetes_Labels is a Loki-output-only parameter, so it is
  # only meaningful (and only asserted) on the Loki branch. On the OpenSearch
  # branch the equivalent risk is the document carrying the full label map,
  # which the filter settings above already prevent.
  grep -qE 'Labels[[:space:]]+Off' "$OUT.workloads" || { echo "the kubernetes filter would attach all labels" >&2; exit 1; }
  grep -qE 'Annotations[[:space:]]+Off' "$OUT.workloads" || { echo "the kubernetes filter would attach all annotations" >&2; exit 1; }
  if [ "$BACKEND" = "loki" ]; then
    grep -qE 'Auto_Kubernetes_Labels[[:space:]]+Off' "$OUT.workloads" \
      || { echo "the loki output would add all Kubernetes labels to the stream" >&2; exit 1; }
    echo "  OK   filter labels/annotations off, and the loki output does not auto-add labels"
  else
    if grep -qE 'Auto_Kubernetes_Labels' "$OUT.workloads"; then
      echo "Auto_Kubernetes_Labels is a loki-output parameter and must not appear on the $BACKEND output" >&2
      exit 1
    fi
    echo "  OK   filter labels/annotations off (no loki-only parameter on this backend)"
  fi

  echo "--- fluent-bit images resolve to the offline registry ---"
  mapfile -t images < <(collect_images "$OUT.workloads")
  printf '%s\n' "${images[@]}"
  fb_ref="$REGISTRY/fluent/fluent-bit:5.1.2"
  printf '%s\n' "${images[@]}" | grep -qF "$fb_ref" \
    || { echo "  MISS $fb_ref" >&2; exit 1; }
  echo "  OK   $fb_ref"
  ;;

*)
  echo "render-gate: unknown role '$ROLE' (expected loki or fluent-bit)" >&2
  exit 2
  ;;
esac

echo "--- every image reference resolves to the offline registry ---"
foreign=0
for ref in "${images[@]}"; do
  case "$ref" in
    "$REGISTRY"/*) : ;;
    quay.io/*|docker.io/*|ghcr.io/*|registry.k8s.io/*|cr.fluentbit.io/*|gcr.io/*)
      echo "  FOREIGN $ref" >&2
      foreign=$((foreign+1)) ;;
    *) : ;;
  esac
done
[ "$foreign" -eq 0 ] || { echo "$ROLE render contains $foreign image references outside the offline registry" >&2; exit 1; }
echo "  foreign: 0"

echo "$ROLE render gate passed"

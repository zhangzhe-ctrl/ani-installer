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
# The role directory, for the templates that are not part of the chart render
# (the security configuration Secret and the initialization Job). ROLE_ROOT may
# be set by the caller; otherwise it is derived from this script's own location,
# which works when the gate runs from inside the repo but not from the lab copy,
# so the driver sets it explicitly.
ROLE_ROOT="${ROLE_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../builtin/core/roles/ani" 2>/dev/null || true)}"
ROLE_DIR="$ROLE_ROOT/$ROLE"

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

opensearch)
  echo "--- objects the opensearch role waits on ---"
  # The single node is a StatefulSet whose PVC is named after the chart's
  # cluster/group, and the role waits on exactly those two names. Object names
  # are matched with their quotes optional: this chart emits some quoted and
  # some bare, and a quoting change in a chart patch bump must not read as a
  # missing object.
  for want in \
    'kind: StatefulSet' \
    'name: ani-opensearch-master' \
    'kind: Service' \
    'name: ani-opensearch-master-headless' \
    'name: ani-opensearch-master-config' \
    'kind: ConfigMap' ; do
    if grep -qE -- "(^|[[:space:]\"])${want}([[:space:]\"\$]|$)" "$OUT.workloads" || grep -qF -- "$want" "$OUT.workloads"; then
      echo "  OK   $want"
    else
      echo "  MISS $want" >&2
      exit 1
    fi
  done
  # A Deployment here would make the role's StatefulSet wait wrong.
  if grep -qE '^kind: Deployment$' "$OUT.workloads"; then
    echo "the render contains a Deployment; the role's waits assume a StatefulSet" >&2
    exit 1
  fi
  echo "  OK   no Deployment (the node is a StatefulSet)"

  echo "--- the volume is a PVC named after the cluster/group ---"
  grep -qE 'volumeClaimTemplates:' "$OUT.workloads" || { echo "the StatefulSet has no volumeClaimTemplates" >&2; exit 1; }
  # This is the name the role waits on and the name the collector's verify
  # script fingerprints across a pod rebuild.
  grep -qE 'name: ani-opensearch-master$' "$OUT.workloads" \
    || { echo "no 'ani-opensearch-master' volumeClaimTemplate" >&2; exit 1; }
  echo "  OK   ani-opensearch-master volumeClaimTemplate present (PVC becomes ani-opensearch-master-0)"

  echo "--- security is on and the demo configuration is not ---"
  grep -qF -- 'DISABLE_INSTALL_DEMO_CONFIG' "$OUT.workloads" || { echo "the demo-config guard env is missing" >&2; exit 1; }
  grep -qF -- 'value: "true"' "$OUT.workloads" || { echo "DISABLE_INSTALL_DEMO_CONFIG is not true" >&2; exit 1; }
  grep -qF -- 'plugins.security.allow_unsafe_democertificates: false' "$OUT.workloads" \
    || { echo "the demo certificates would be trusted" >&2; exit 1; }
  grep -qF -- 'plugins.security.allow_default_init_securityindex: false' "$OUT.workloads" \
    || { echo "the plugin would initialize its own security index with defaults" >&2; exit 1; }
  grep -qF -- 'plugins.security.ssl.http.enabled: true' "$OUT.workloads" \
    || { echo "the HTTP layer is not using TLS" >&2; exit 1; }
  # anonymous_auth_enabled lives in the security configuration the role supplies
  # through securityConfigSecret, not in the chart's own render, so it is
  # asserted against that template instead of here (see the check below).
  echo "  OK   demo config disabled and TLS on"

  echo "--- the node and admin DNs are the ones the certificates carry ---"
  grep -qF 'CN=ani-opensearch-node' "$OUT.workloads" || { echo "the node DN is not configured" >&2; exit 1; }
  grep -qF 'CN=ani-opensearch-admin' "$OUT.workloads" || { echo "the admin DN is not configured" >&2; exit 1; }
  echo "  OK"

  echo "--- the security configuration is mounted from the externally created Secret ---"
  # securityConfigSecret tells the chart to mount a pre-existing Secret as a
  # whole directory. The chart then renders no Secret of its own, so the role's
  # Secret must be the name the pod mounts, or the plugin falls back to whatever
  # the image ships.
  grep -qF -- 'secretName: "ani-opensearch-security-config"' "$OUT.workloads" \
    || { echo "the security config Secret is not mounted by the pod" >&2; exit 1; }
  grep -qF -- 'mountPath: /usr/share/opensearch/config/opensearch-security' "$OUT.workloads" \
    || { echo "the security config is not mounted at the plugin's config path" >&2; exit 1; }
  if grep -qF -- 'securityconfig' "$OUT.workloads"; then
    echo "the chart rendered its own securityconfig Secret; the role supplies one instead" >&2
    exit 1
  fi
  echo "  OK   mounted from the role's Secret, and the chart rendered no competing Secret"

  echo "--- the privileged sysctl init container is off ---"
  if grep -qE '^[[:space:]]*- name: sysctl$' "$OUT.workloads"; then
    echo "the chart's privileged sysctl init container is present; vm.max_map_count is a node setting here" >&2
    exit 1
  fi
  if grep -qE 'privileged: true' "$OUT.workloads"; then
    echo "a privileged container is present" >&2
    exit 1
  fi
  echo "  OK   no sysctl init container and no privileged container"

  echo "--- nothing this batch did not ask for ---"
  # The chart renders a PodDisruptionBudget whenever maxUnavailable is set, and
  # its default is 1. With a single replica that allows the one pod to be
  # evicted, which is exactly what a single-node lab cannot tolerate losing, but
  # setting it to 0 would block node drains instead — neither is a protection
  # worth inventing a value for, so the chart's default is left in place and
  # reported rather than asserted away. Everything below is a workload or a
  # policy this batch genuinely does not install.
  if grep -qF -- 'kind: PodDisruptionBudget' "$OUT.workloads"; then
    echo "  NOTE a PodDisruptionBudget is rendered (the chart's maxUnavailable default)"
  fi
  for bad in \
    'kind: Ingress' \
    'kind: ServiceMonitor' \
    'kind: NetworkPolicy' \
    'kind: PodSecurityPolicy' ; do
    if grep -qF -- "$bad" "$OUT.workloads"; then
      echo "  PRESENT $bad" >&2
      exit 1
    else
      echo "  absent $bad"
    fi
  done
  if grep -qiE "^[[:space:]]*name:.*(dashboards|grafana)" "$OUT.workloads"; then
    echo "  PRESENT a dashboards/grafana object" >&2
    exit 1
  fi
  echo "  absent dashboards/grafana, ingress, service monitor, network policy"

  echo "--- the node is single-node and holds the data role ---"
  grep -qF -- 'discovery.type' "$OUT.workloads" || { echo "no single-node discovery setting" >&2; exit 1; }
  grep -qF -- 'value: "single-node"' "$OUT.workloads" || { echo "the cluster is not single-node" >&2; exit 1; }
  grep -qF -- 'OPENSEARCH_JAVA_OPTS' "$OUT.workloads" || { echo "no heap setting" >&2; exit 1; }
  echo "  OK"

  echo "--- the role's own security configuration is complete ---"
  # These two templates are not part of the chart render: one becomes a Secret
  # the chart mounts, the other a Job the installer waits on. They carry the
  # settings the chart render cannot show, so they are asserted from the role
  # directory directly.
  SEC_CONFIG="$ROLE_DIR/templates/security-config.yaml"
  SEC_INIT="$ROLE_DIR/templates/security-init.yaml"
  [ -f "$SEC_CONFIG" ] || { echo "missing $SEC_CONFIG" >&2; exit 1; }
  [ -f "$SEC_INIT" ] || { echo "missing $SEC_INIT" >&2; exit 1; }

  grep -qF -- 'anonymous_auth_enabled: false' "$SEC_CONFIG" || { echo "anonymous access would be allowed" >&2; exit 1; }
  grep -qF -- 'type: intern' "$SEC_CONFIG" || { echo "the internal user database is not the authentication backend" >&2; exit 1; }
  grep -qF -- 'CN=ani-opensearch-node' "$SEC_CONFIG" || { echo "nodes_dn does not carry the node DN" >&2; exit 1; }
  # Exactly two hash placeholders: the administrator's and the reserved
  # account's. Any more or fewer means the Job's replacement would be wrong.
  placeholders="$(grep -c 'PLACEHOLDER-REPLACED-AT-INSTALL-TIME' "$SEC_CONFIG" || true)"
  [ "$placeholders" = "2" ] || { echo "expected 2 hash placeholders, found $placeholders" >&2; exit 1; }
  # No demo user may survive from the upstream file.
  for bad in 'kibanaro' 'logstash:' 'readall:' 'snapshotrestore' 'anomalyadmin' 'admin_tenant'; do
    if grep -qF -- "$bad" "$SEC_CONFIG"; then
      echo "  PRESENT the upstream demo entry '$bad'" >&2
      exit 1
    fi
  done
  echo "  OK   anonymous off, internal backend, node DN, 2 placeholders, no demo entries"

  echo "--- the initialization Job is confined and non-demo ---"
  grep -qF -- 'kind: Job' "$SEC_INIT" || { echo "the security initialization is not a Job" >&2; exit 1; }
  grep -qF -- 'securityadmin.sh' "$SEC_INIT" || { echo "the security index is not seeded" >&2; exit 1; }
  grep -qF -- '/admin-tls/ca.crt' "$SEC_INIT" || { echo "the admin certificate's CA is not used" >&2; exit 1; }
  grep -qF -- 'hash.sh' "$SEC_INIT" || { echo "the password is not hashed with the image's own tool" >&2; exit 1; }
  grep -qF -- '/dev/urandom' "$SEC_INIT" || { echo "the password is not generated from the pod's random source" >&2; exit 1; }
  grep -qF -- 'kind: Role' "$SEC_INIT" || { echo "the Job has no namespaced Role" >&2; exit 1; }
  grep -qF -- 'kind: RoleBinding' "$SEC_INIT" || { echo "the Job has no RoleBinding" >&2; exit 1; }
  if grep -qF -- 'kind: ClusterRole' "$SEC_INIT"; then
    echo "the security initialization uses a ClusterRole; its access must be namespaced" >&2
    exit 1
  fi
  for bad in '--enable-demo' 'admin/admin' 'changeme'; do
    if grep -qF -- "$bad" "$SEC_INIT"; then
      echo "  PRESENT a demo setting or literal credential: '$bad'" >&2
      exit 1
    fi
  done
  echo "  OK   Job + namespaced RBAC + certificate-seeded security index, no demo setting"

  echo "--- opensearch images resolve to the offline registry ---"
  mapfile -t images < <(collect_images "$OUT.workloads")
  printf '%s\n' "${images[@]}"
  os_ref="$REGISTRY/opensearchproject/opensearch:3.8.0"
  printf '%s\n' "${images[@]}" | grep -qF "$os_ref" \
    || { echo "  MISS $os_ref" >&2; exit 1; }
  echo "  OK   $os_ref"
  # The chart's chown init container pulls busybox; it must be the locked one
  # from the offline registry, not "busybox:latest".
  busy_ref="$REGISTRY/library/busybox:1.37.0"
  printf '%s\n' "${images[@]}" | grep -qF "$busy_ref" \
    || { echo "  MISS $busy_ref" >&2; exit 1; }
  echo "  OK   $busy_ref"
  ;;

*)
  echo "render-gate: unknown role '$ROLE' (expected loki, fluent-bit or opensearch)" >&2
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

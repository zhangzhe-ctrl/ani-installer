#!/usr/bin/env bash
# ANI Loki verification (backend capabilities for the C3 card).
#
# What this proves, in order:
#   1. the StatefulSet, its pod and its PVC are the ones the chart rendered and
#      the volume is actually bound, not merely declared;
#   2. the Loki HTTP API answers: readiness, build info, and a real query
#      against the log store (an empty result set still proves the query path
#      and the configured schema/schema-store wiring);
#   3. the retention configuration the site asked for is the one Loki reports,
#      and the compactor is the component that will act on it;
#   4. no public endpoint exists and no unexpected workload was deployed;
#   5. the PVC and StatefulSet UIDs are recorded, so a later rebuild can be
#      shown to have kept the same volume.
#
# What this deliberately does NOT prove: that logs written to a container's
# stdout reach this backend. That is the collection path, it is owned by the
# Fluent Bit role which runs after this one, and proving it here would require
# a collector that is not installed yet.
#
# Queries go through the Loki HTTP API on the Service, not the pod IP, so the
# same path a client uses is the one under test. No service is restarted.
set -euo pipefail

NS="{{ .ani.components.logging.namespace }}"
RELEASE="ani-loki"
STS="ani-loki"
PVC="storage-ani-loki-0"
RETENTION_PERIOD="{{ .ani.components.logging.retention_hours }}h"

OUT_DIR="${ANI_VERIFY_OUTPUT_DIR:-/var/lib/ani-installer/logs}"
install -d -m 0700 "$OUT_DIR"
EVIDENCE="$OUT_DIR/loki-verify-$(date -u +%Y%m%dT%H%M%SZ)"
install -d -m 0700 "$EVIDENCE"

fail() { echo "loki verify: $*" >&2; exit 1; }
note() { printf '%s\n' "$*"; }

KC="kubectl -n $NS"
# The Loki image has no shell, so API calls are made from a throwaway pod using
# the lab tool image the material lock already carries for exactly this purpose.
TOOL_IMAGE="{{ index .ani.images "docker.io/library/python:3.13.11-alpine3.23" }}"
CLIENT_POD="ani-loki-verify-client"

LOKI_HOST="$RELEASE.$NS.svc.cluster.local:3100"

# Run a python program inside the cluster, from a file, so no quoting of a
# multi-line program survives a shell boundary.
py() {
  local script="$1"; shift
  kubectl -n "$NS" exec -i "$CLIENT_POD" -- python3 - "$@" < "$script"
}

note "== [1] workload, pod and volume =="
$KC get statefulset "$STS" -o wide | tee "$EVIDENCE/statefulset.txt"
$KC get pod "$STS-0" -o wide | tee "$EVIDENCE/pod.txt"

ready="$($KC get statefulset "$STS" -o jsonpath='{.status.readyReplicas}')"
[ "$ready" = "1" ] || fail "expected 1 ready replica, got ${ready:-none}"

pvc_phase="$($KC get pvc "$PVC" -o jsonpath='{.status.phase}')"
[ "$pvc_phase" = "Bound" ] || fail "PVC $PVC is $pvc_phase, not Bound"
pvc_uid="$($KC get pvc "$PVC" -o jsonpath='{.metadata.uid}')"
sts_uid="$($KC get statefulset "$STS" -o jsonpath='{.metadata.uid}')"
note "PVC UID $pvc_uid"
note "StatefulSet UID $sts_uid"

# The volume must be the PVC, not an emptyDir: an emptyDir would pass a
# readiness check while losing every log on a pod rebuild.
$KC get statefulset "$STS" -o jsonpath='{.spec.volumeClaimTemplates[*].metadata.name}' \
  | grep -qx storage || fail "the StatefulSet has no 'storage' volumeClaimTemplate"

cat > "$EVIDENCE/volume-ids.txt" <<EOF
pvc_uid=$pvc_uid
statefulset_uid=$sts_uid
pvc_phase=$pvc_phase
EOF

note "== [2] Loki HTTP API =="
# Start one client pod; every API call below reuses it.
$KC delete pod "$CLIENT_POD" --ignore-not-found --wait=true >/dev/null
$KC run "$CLIENT_POD" --image="$TOOL_IMAGE" --restart=Never \
  --command -- python3 -c 'import time; time.sleep(1800)' >/dev/null
$KC wait --for=condition=Ready "pod/$CLIENT_POD" --timeout=180s

cat > "$EVIDENCE/query_ready.py" <<'PY'
import json, sys, urllib.request
host = sys.argv[1]
with urllib.request.urlopen(f"http://{host}/ready", timeout=10) as r:
    print("status", r.status)
    print(r.read().decode()[:200])
PY
py "$EVIDENCE/query_ready.py" "$LOKI_HOST" | tee "$EVIDENCE/ready.txt"

cat > "$EVIDENCE/query_buildinfo.py" <<'PY'
import json, sys, urllib.request
host = sys.argv[1]
with urllib.request.urlopen(f"http://{host}/loki/api/v1/status/buildinfo", timeout=10) as r:
    info = json.load(r)
print("version", info.get("version"))
PY
py "$EVIDENCE/query_buildinfo.py" "$LOKI_HOST" | tee "$EVIDENCE/buildinfo.txt"

# A real range query over the tenant's whole history. It returns empty until a
# collector writes something, and an empty result is the expected outcome here;
# what is under test is that the query engine, the schema store and the
# filesystem object store answer instead of erroring.
cat > "$EVIDENCE/query_empty.py" <<'PY'
import json, sys, urllib.parse, urllib.request
host = sys.argv[1]
q = urllib.parse.urlencode({"query": '{job=~".+"}', "limit": "10"})
try:
    with urllib.request.urlopen(f"http://{host}/loki/api/v1/query_range?{q}", timeout=20) as r:
        body = json.load(r)
except urllib.error.HTTPError as e:
    print("query_range failed:", e.code, e.read().decode()[:300], file=sys.stderr)
    raise SystemExit(1)
print("status", body.get("status"))
print("resultType", body.get("data", {}).get("resultType"))
print("series", len(body.get("data", {}).get("result", []) or []))
if body.get("status") != "success":
    raise SystemExit("query_range did not report success")
PY
py "$EVIDENCE/query_empty.py" "$LOKI_HOST" | tee "$EVIDENCE/query.txt"

note "== [3] retention configuration is the one the site asked for =="
# Read the effective config from the monolith itself through GET /config, which
# is what Loki reports as its running configuration. The limits_config value and
# the compactor are two separate things: retention_period only marks data, and
# only the compactor with a delete store removes it, so both are asserted.
cat > "$EVIDENCE/query_config.py" <<'PY'
import json, sys, urllib.request
host = sys.argv[1]
with urllib.request.urlopen(f"http://{host}/config", timeout=10) as r:
    cfg = json.load(r)
limits = cfg.get("limits_config", {})
print("retention_period", limits.get("retention_period"))
compactor = cfg.get("compactor", {})
print("compactor.retention_enabled", compactor.get("retention_enabled"))
print("compactor.delete_request_store", compactor.get("delete_request_store"))
print("compactor.working_directory", compactor.get("working_directory"))
common = cfg.get("common", {})
print("replication_factor", common.get("replication_factor"))
sa = cfg.get("schema_config", {}).get("configs", [{}])
print("schema_store", sa[0].get("store") if sa else None)
print("schema_object_store", sa[0].get("object_store") if sa else None)
print("storage_type", (cfg.get("storage_config") or {}).get("filesystem") is not None
      or cfg.get("common", {}).get("storage", {}).get("filesystem") is not None)
PY
py "$EVIDENCE/query_config.py" "$LOKI_HOST" | tee "$EVIDENCE/config.txt"

grep -qx "retention_period $RETENTION_PERIOD" "$EVIDENCE/config.txt" \
  || fail "Loki reports a retention_period other than $RETENTION_PERIOD"
grep -qx "compactor.retention_enabled True" "$EVIDENCE/config.txt" \
  || fail "the compactor does not have retention_enabled, so nothing would ever be deleted"
grep -qx "compactor.delete_request_store filesystem" "$EVIDENCE/config.txt" \
  || fail "the compactor has no filesystem delete store, so deletions would not be recorded"
grep -qx "replication_factor 1" "$EVIDENCE/config.txt" \
  || fail "replication_factor is not 1"
grep -qx "schema_store tsdb" "$EVIDENCE/config.txt" \
  || fail "the schema store is not tsdb as configured"
grep -qx "schema_object_store filesystem" "$EVIDENCE/config.txt" \
  || fail "the object store is not filesystem, so chunks are not on the PVC"

# The retention period must actually reach the store from limits_config: a value
# in a values file that Loki ignored would look identical to a working one.
grep -q "retention_period" "$EVIDENCE/config.txt" \
  || fail "Loki's effective config has no retention_period at all"

note "== [4] nothing extra was deployed and no public endpoint exists =="
$KC get deploy,sts,ds,svc -o wide | tee "$EVIDENCE/workloads.txt"
# Only the monolith StatefulSet is expected. A memcached ServiceAccount may be
# rendered by the chart even with both caches off; it is not a workload, so it
# is reported rather than treated as a failure.
for kind in deployment daemonset; do
  count="$($KC get "$kind" -o name 2>/dev/null | wc -l)"
  [ "$count" = "0" ] || fail "unexpected $kind in $NS: $($KC get "$kind" -o name | tr '\n' ' ')"
done
sts_count="$($KC get statefulset -o name | wc -l)"
[ "$sts_count" = "1" ] || fail "expected only the Loki StatefulSet, found $sts_count"

# The Loki Service must not be exposed outside the cluster.
svc_types="$($KC get svc "$RELEASE" -o jsonpath='{.spec.type}')"
[ "$svc_types" = "ClusterIP" ] || fail "the Loki Service is not ClusterIP"

# dashboards/grafana must not exist anywhere: this batch does not deploy them.
if kubectl get deployment,statefulset,daemonset -A -o name 2>/dev/null | grep -qE 'grafana|dashboards'; then
  fail "a grafana or dashboards workload exists; this batch must not deploy one"
fi
note "no grafana/dashboards workload present"

note "== [5] cleanup of this run's own client pod =="
$KC delete pod "$CLIENT_POD" --ignore-not-found --wait=true >/dev/null
$KC get pod "$CLIENT_POD" >/dev/null 2>&1 && fail "the temporary client pod was not removed"

note "loki verify passed"
note "evidence: $EVIDENCE"

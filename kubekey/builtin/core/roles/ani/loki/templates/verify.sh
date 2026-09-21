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
#
# Loki answers this endpoint with YAML text (Content-Type text/plain), not JSON.
# The first version of this check called json.load() on it and died on a live
# install, so the parser below reads the YAML directly. Loki's own config dump
# is a two-level mapping of plain scalars, which is all this needs to be; the
# client image carries only the Python standard library, so a YAML dependency
# is not available and adding one would mean changing the packaged material.
cat > "$EVIDENCE/query_config.py" <<'PY'
import sys, time, urllib.request

host = sys.argv[1]
# A12: the effective-config dump is ~92KB of chunked YAML and the endpoint
# streams it slowly; a 10s socket timeout cut the body mid-read on a real
# install, so the fetch carries a generous timeout and bounded retries. The
# assertions below are untouched.
text = None
ctype = ""
for attempt in range(3):
    try:
        with urllib.request.urlopen(f"http://{host}/config", timeout=60) as r:
            text = r.read().decode()
            ctype = r.headers.get("Content-Type", "")
        break
    except Exception as exc:
        print(f"config fetch attempt {attempt + 1} failed: {exc}", file=sys.stderr)
        text = None
        time.sleep(5)
if text is None:
    raise SystemExit("could not fetch the /config dump after 3 attempts")


def parse_yaml_scalars(text):
    """Return {(section, key): value} for the scalars this check reads.

    Loki's dump has sections such as limits_config and compactor whose entries
    are plain scalars, plus schema_config.configs, which is a list of mappings.
    A list entry is flattened into its parent section so `store` and
    `object_store` are reachable as schema_config.store and
    schema_config.object_store; the check only reads scalars. Quoting is
    stripped so a rendered "3d" compares as 3d.

    A12: a scalar belongs to its section only at two-space indent, and a list
    entry's own fields (exactly four-space indent) are flattened. Anything
    deeper is a nested mapping's business and is skipped. The first version
    accepted scalars at any depth, so the ring inside common -- whose
    replication_factor sits several levels below the section header with
    Loki's default of 3 -- overwrote the real common.replication_factor 1
    read six lines earlier, and a correctly deployed single-copy config was
    reported as replication_factor 3.
    """
    out = {}
    section = None
    in_list_item = False
    for raw in text.splitlines():
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        indent = len(raw) - len(raw.lstrip(" "))
        line = raw.strip()
        if ":" not in line:
            continue
        key, _, value = line.partition(":")
        key, value = key.strip(), value.strip()
        if indent == 0:
            section = key if value == "" else None
            in_list_item = False
            continue
        if section is None:
            continue
        if indent <= 2 and line.startswith("- "):
            in_list_item = True
            rest = line[2:]
            if ":" in rest:
                k, _, v = rest.partition(":")
                v = v.strip().strip("\"'")
                if v:
                    out[(section, k.strip())] = v
            continue
        if value == "":
            if indent <= 2:
                in_list_item = False
            continue
        if indent == 2 or (in_list_item and indent == 4):
            out[(section, key)] = value.strip("\"'")
    return out


cfg = parse_yaml_scalars(text)
print("content_type", ctype.split(";")[0])
for section, key in [
    ("limits_config", "retention_period"),
    ("compactor", "retention_enabled"),
    ("compactor", "delete_request_store"),
    ("compactor", "working_directory"),
    ("common", "replication_factor"),
    ("schema_config", "store"),
    ("schema_config", "object_store"),
]:
    print(f"{section}.{key}", cfg.get((section, key)))
if ("limits_config", "retention_period") not in cfg:
    raise SystemExit("config response did not parse as the expected YAML mapping")
PY
py "$EVIDENCE/query_config.py" "$LOKI_HOST" | tee "$EVIDENCE/config.txt"

grep -qx "content_type text/plain" "$EVIDENCE/config.txt" \
  || fail "Loki did not answer /config as YAML text, so this check reads the wrong format"

# The site's retention is expressed in hours (retentionDays x 24), but Loki
# echoes whatever unit the configuration used, and 72h and 3d are the same
# duration. Compare durations rather than display strings so an equivalent form
# is accepted without weakening the check.
RETENTION_PERIOD_SECONDS="$(python3 - "$RETENTION_PERIOD" <<'PY'
import re, sys
m = re.fullmatch(r"(\d+)([smhd])", sys.argv[1])
if not m:
    raise SystemExit(f"unparseable retention period: {sys.argv[1]}")
n, unit = int(m.group(1)), m.group(2)
print(n * {"s": 1, "m": 60, "h": 3600, "d": 86400}[unit])
PY
)"
echo "retention_period_seconds $RETENTION_PERIOD_SECONDS" >> "$EVIDENCE/config.txt"

# Compare the effective value as a duration: read the reported value, convert it
# the same way, and require the seconds to match.
REPORTED_PERIOD="$(awk '$1 == "limits_config.retention_period" {print $2}' "$EVIDENCE/config.txt")"
[ -n "$REPORTED_PERIOD" ] && [ "$REPORTED_PERIOD" != "None" ] \
  || fail "Loki's effective config has no limits_config.retention_period"
REPORTED_SECONDS="$(python3 - "$REPORTED_PERIOD" <<'PY'
import re, sys
value = sys.argv[1].strip().strip('"')
m = re.fullmatch(r"(\d+)(ns|us|ms|s|m|h|d|w|y)", value)
if not m:
    print("unparseable")
    raise SystemExit(0)
n, unit = int(m.group(1)), m.group(2)
print(n * {"ns": 0, "us": 0, "ms": 0, "s": 1, "m": 60, "h": 3600,
           "d": 86400, "w": 604800, "y": 31536000}[unit])
PY
)"
echo "reported_retention_period_seconds $REPORTED_SECONDS" >> "$EVIDENCE/config.txt"
[ "$REPORTED_SECONDS" = "$RETENTION_PERIOD_SECONDS" ] \
  || fail "Loki retains for $REPORTED_PERIOD ($REPORTED_SECONDS s), not the configured $RETENTION_PERIOD ($RETENTION_PERIOD_SECONDS s)"

grep -qx "compactor.retention_enabled true" "$EVIDENCE/config.txt" \
  || fail "the compactor does not have retention_enabled, so nothing would ever be deleted"
grep -qx "compactor.delete_request_store filesystem" "$EVIDENCE/config.txt" \
  || fail "the compactor has no filesystem delete store, so deletions would not be recorded"
grep -qx "common.replication_factor 1" "$EVIDENCE/config.txt" \
  || fail "replication_factor is not 1"
grep -qx "schema_config.store tsdb" "$EVIDENCE/config.txt" \
  || fail "the schema store is not tsdb as configured"
grep -qx "schema_config.object_store filesystem" "$EVIDENCE/config.txt" \
  || fail "the object store is not filesystem, so chunks are not on the PVC"

# The retention period must actually reach the store from limits_config: a value
# in a values file that Loki ignored would look identical to a working one.
grep -q "retention_period" "$EVIDENCE/config.txt" \
  || fail "Loki's effective config has no retention_period at all"

note "== [4] the chart's unused components are really off and nothing is public =="
$KC get deploy,sts,ds,svc -o wide | tee "$EVIDENCE/workloads.txt"
# A13: the chart's disabled pieces must not exist as workloads. Monolithic
# mode zero-replicates read/write/backend and turns the gateway, canary and
# both caches off, so the only loki-chart workload is the monolith
# StatefulSet itself: a gateway or memcached deployment, or a second loki
# StatefulSet, would mean the values did not take. The namespace is SHARED
# in cumulative runs -- the metrics role legitimately deploys kube-state-
# metrics and the operator here -- so exclusivity is asserted against the
# loki chart's own names only; the first version banned every deployment in
# the namespace and misread a healthy cumulative install as an extra deploy
# (proven by a real install).
# '|| true' on both greps: a no-match grep is the HEALTHY A13 state, and under
# `set -o pipefail` it exits the whole script with no FAIL line at all -- this
# killed the a18 verify silently right after the workload listing (evidence:
# h4loki-a18, dead between the listing and the first note of this section).
# A17 (evidence h4loki-a20): the sts capture must NOT join lines with
# `tr '\n' ' '` -- a single match then keeps a trailing space and the exact
# equality below fails with the absurd message "expected ani-loki, found:
# statefulset.apps/ani-loki ". Command substitution already strips trailing
# newlines, so the bare pipeline is exact; `|| true` alone guards pipefail.
loki_extra="$($KC get deploy,ds -o name 2>/dev/null | grep -F "$RELEASE" | tr '\n' ' ' || true)"
[ -z "$loki_extra" ] || fail "unexpected loki chart deployments/daemonsets in $NS: $loki_extra"
loki_sts="$($KC get statefulset -o name 2>/dev/null | grep -E "^statefulset.apps/$RELEASE(-[a-z]+)?$" || true)"
[ "$loki_sts" = "statefulset.apps/$RELEASE" ] \
  || fail "expected exactly the monolith StatefulSet $RELEASE, found: ${loki_sts:-none}"

# The Loki Service must not be exposed outside the cluster.
svc_types="$($KC get svc "$RELEASE" -o jsonpath='{.spec.type}')"
[ "$svc_types" = "ClusterIP" ] || fail "the Loki Service is not ClusterIP"

# dashboards/grafana must not exist anywhere: this batch does not deploy them.
if kubectl get deployment,statefulset,daemonset -A -o name 2>/dev/null | grep -qE 'grafana|dashboards'; then
  fail "a grafana or dashboards workload exists; this batch must not deploy one"
fi
note "no grafana/dashboards workload present"

note "== [5] cleanup of this run's own client pod =="
$KC delete pod "$CLIENT_POD" --ignore-not-found --wait=true >/dev/null \
  || fail "removing the temporary client pod failed"
$KC get pod "$CLIENT_POD" >/dev/null 2>&1 && fail "the temporary client pod was not removed"

note "loki verify passed"
note "evidence: $EVIDENCE"

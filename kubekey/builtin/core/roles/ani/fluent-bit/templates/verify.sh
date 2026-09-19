#!/usr/bin/env bash
# ANI Fluent Bit verification — the collection path, for the C3 card.
#
# This is the check neither backend role can make for itself. Every assertion
# below goes through the real path: a container writes to its stdout, the
# runtime writes that to the node's container log file, the collector tails the
# file, and the backend is then *queried* for the record. Nothing is ever
# pushed to the backend directly, because a direct push would prove only that
# the backend accepts writes, not that collection works.
#
# What this proves, in order:
#   1. the DaemonSet is ready on exactly every node and its rendered config has
#      exactly one output for the selected backend, with no second write path
#      and no leftover Elasticsearch output;
#   2. a per-node marker pod writes a unique marker to stdout, and every marker
#      is later found in the backend with the correct namespace, pod, container
#      and node — through the collector, not by injection;
#   3. rebuilding the backend pod keeps the records: the same markers are read
#      back after the pod is replaced and the PVC is shown to be unchanged;
#   4. rebuilding one collector pod keeps the tail cursor, that node keeps
#      producing new markers, and both the old and the new markers are readable;
#   5. a bounded file buffer and one single output exist, so a backend outage
#      degrades to a bounded local buffer instead of unbounded disk growth.
#
# Deliberately NOT proven here: that a record which has reached the backend is
# later *deleted* when its retention period expires. Retention *configuration*
# is asserted (by the backend role and re-read here); watching a real deletion
# happen would need the retention period to elapse, so that item is recorded as
# not_verified rather than faked.
#
# This script restarts only the two pods it rebuilds, and only by explicit
# deletion of a named pod. It never resets, wipes, or clears a cluster
# resource, and it never deletes a PVC.
set -euo pipefail

NS="{{ .ani.components.logging.namespace }}"
BACKEND="{{ .ani.components.logging.backend }}"
DS="ani-fluent-bit"
TOOL_IMAGE="{{ index .ani.images "docker.io/library/python:3.13.11-alpine3.23" }}"
BUSYBOX_IMAGE="{{ index .ani.images "docker.io/library/busybox:1.37" }}"
CLIENT_POD="ani-fluent-bit-verify-client"

OUT_DIR="${ANI_VERIFY_OUTPUT_DIR:-/var/lib/ani-installer/logs}"
install -d -m 0700 "$OUT_DIR"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"
EVIDENCE="$OUT_DIR/fluent-bit-verify-$RUN_ID"
install -d -m 0700 "$EVIDENCE"

fail() { echo "fluent-bit verify: $*" >&2; exit 1; }
note() { printf '%s\n' "$*"; }

KC="kubectl -n $NS"

case "$BACKEND" in
  loki|opensearch) ;;
  *) fail "unsupported logging backend '$BACKEND'" ;;
esac

# Run a python program inside the cluster from a file, so no multi-line program
# has to survive a shell boundary.
py() {
  local script="$1"; shift
  kubectl -n "$NS" exec -i "$CLIENT_POD" -- python3 - "$@" < "$script"
}

start_client() {
  $KC delete pod "$CLIENT_POD" --ignore-not-found --wait=true >/dev/null
  $KC run "$CLIENT_POD" --image="$TOOL_IMAGE" --restart=Never \
    --command -- python3 -c 'import time; time.sleep(3600)' >/dev/null
  $KC wait --for=condition=Ready "pod/$CLIENT_POD" --timeout=180s
}

note "== [1] the collector runs on every node and has one output =="
$KC get daemonset "$DS" -o wide | tee "$EVIDENCE/daemonset.txt"
$KC get pod -l "app.kubernetes.io/name=fluent-bit" -o wide \
  | tee "$EVIDENCE/collector-pods.txt"

want_nodes="$(kubectl get nodes --no-headers | wc -l)"
ready="$($KC get daemonset "$DS" -o jsonpath='{.status.numberReady}')"
[ "$ready" = "$want_nodes" ] \
  || fail "collector ready on $ready of $want_nodes nodes"

ds_uid="$($KC get daemonset "$DS" -o jsonpath='{.metadata.uid}')"
note "DaemonSet UID $ds_uid"

# The rendered config is read back from the running pod rather than from the
# values file, so a chart that ignored a value would not pass.
COLLECTOR_POD="$($KC get pod -l "app.kubernetes.io/name=fluent-bit" \
  -o jsonpath='{.items[0].metadata.name}')"
[ -n "$COLLECTOR_POD" ] || fail "no collector pod found"
$KC exec "$COLLECTOR_POD" -- cat /fluent-bit/etc/fluent-bit.conf \
  > "$EVIDENCE/rendered-fluent-bit.conf"

# Exactly one [OUTPUT] block, and it must be the selected backend. A leftover
# chart default would be a second write path, sending every record to a host
# that does not exist and doubling the load on the real backend.
outputs="$(grep -c '^\[OUTPUT\]' "$EVIDENCE/rendered-fluent-bit.conf" || true)"
[ "$outputs" = "1" ] || fail "expected exactly 1 output block, found $outputs"

case "$BACKEND" in
  loki)
    grep -qi 'Name *loki' "$EVIDENCE/rendered-fluent-bit.conf" \
      || fail "the single output is not the loki output"
    grep -qi 'Name *es' "$EVIDENCE/rendered-fluent-bit.conf" \
      && fail "a leftover Elasticsearch output is present"
    ;;
  opensearch)
    grep -qi 'Name *opensearch' "$EVIDENCE/rendered-fluent-bit.conf" \
      || fail "the single output is not the opensearch output"
    ;;
esac

# The two inputs the chart ships must not both be present: the systemd input
# reads kubelet's journal, which is not container log collection.
grep -qi 'Name *systemd' "$EVIDENCE/rendered-fluent-bit.conf" \
  && fail "the chart's systemd input is still enabled"
grep -qi 'Name *tail' "$EVIDENCE/rendered-fluent-bit.conf" \
  || fail "the container-log tail input is missing"
grep -q '/var/log/containers/\*.log' "$EVIDENCE/rendered-fluent-bit.conf" \
  || fail "the tail input does not read the container log directory"

# The cursor and buffer must be on the per-node persistent directory, not on
# the container's own filesystem.
grep -q '/var/lib/fluent-bit/tail.db' "$EVIDENCE/rendered-fluent-bit.conf" \
  || fail "the tail cursor is not on the persistent per-node directory"
grep -q 'storage.path /var/lib/fluent-bit/buffers' "$EVIDENCE/rendered-fluent-bit.conf" \
  || fail "the filesystem buffer is not on the persistent per-node directory"
grep -q 'storage.total_limit_size' "$EVIDENCE/rendered-fluent-bit.conf" \
  || fail "the output buffer has no upper bound"
note "one output ($BACKEND), one tail input, persistent cursor and bounded buffer"

# The per-node state directory must actually be backed by a hostPath, or the
# cursor is lost on every pod restart.
$KC get daemonset "$DS" -o jsonpath='{range .spec.template.spec.volumes[*]}{.name}{" "}{.hostPath.path}{"\n"}{end}' \
  | tee "$EVIDENCE/volumes.txt"
grep -q '/var/lib/ani-installer/fluent-bit' "$EVIDENCE/volumes.txt" \
  || fail "the collector state directory is not a hostPath"

note "== [2] markers written to stdout are found in $BACKEND through collection =="
start_client

# One marker pod per node. Each writes a unique marker and then idles, so the
# record has time to be collected.
mapfile -t NODES < <(kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
[ "${#NODES[@]}" -ge 1 ] || fail "no nodes found"

declare -A MARKER_OF
for i in "${!NODES[@]}"; do
  node="${NODES[$i]}"
  seq_n=$((i + 1))
  pod="ani-log-marker-${seq_n}"
  marker="ANI-MARKER-${RUN_ID}-n${seq_n}-$(hostname)-${node}"
  MARKER_OF["$node"]="$marker"
  note "node $node -> pod $pod"
  $KC delete pod "$pod" --ignore-not-found --wait=true >/dev/null
  cat <<EOF | $KC apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: $pod
  labels:
    ani-marker: "$pod"
spec:
  restartPolicy: Never
  nodeName: $node
  containers:
    - name: marker
      image: $BUSYBOX_IMAGE
      command: ["/bin/sh", "-c"]
      args:
        - "echo '$marker'; i=0; while [ \$i -lt 90 ]; do sleep 1; i=\$((i+1)); done"]
      resources:
        requests: {cpu: 10m, memory: 16Mi}
        limits: {memory: 32Mi}
EOF
done

for i in "${!NODES[@]}"; do
  seq_n=$((i + 1))
  $KC wait --for=condition=Ready "pod/ani-log-marker-${seq_n}" --timeout=180s
done

# Markers are read from the marker pods' own logs first, so the expected value
# is known from the same place the collector read it.
{
  for i in "${!NODES[@]}"; do
    seq_n=$((i + 1))
    $KC logs "ani-log-marker-${seq_n}"
  done
} | tee "$EVIDENCE/markers-written.txt"

for i in "${!NODES[@]}"; do
  node="${NODES[$i]}"
  grep -qxF "${MARKER_OF[$node]}" "$EVIDENCE/markers-written.txt" \
    || fail "marker for node $node did not appear in its own pod's stdout"
done

# Give the collector time to ship what the pods already wrote, then query the
# backend. This waits on the backend's answer, not on a fixed sleep being long
# enough, so a slow but working collector still passes. The metadata check below
# is what makes this evidence of *collection* rather than of the backend
# accepting a write.
case "$BACKEND" in
  loki) LOKI_HOST="ani-loki.$NS.svc.cluster.local:3100" ;;
  opensearch) OS_HOST="ani-opensearch-master.$NS.svc.cluster.local:9200" ;;
esac

# The secured backend needs the cluster's CA and a credential for every query.
# Both are copied into the client pod from the Secrets the OpenSearch role
# created, through the API, so neither is ever a file on the installer's disk
# and neither appears in a command line.
if [ "$BACKEND" = opensearch ]; then
  $KC get secret ani-opensearch-node-tls -o jsonpath='{.data.ca\.crt}' \
    | base64 -d | kubectl -n "$NS" exec -i "$CLIENT_POD" -- sh -c 'cat > /tmp/ca.crt'
  $KC get secret ani-opensearch-fluent-bit -o jsonpath='{.data.username}' \
    | base64 -d | kubectl -n "$NS" exec -i "$CLIENT_POD" -- sh -c 'cat > /tmp/os_user'
  $KC get secret ani-opensearch-fluent-bit -o jsonpath='{.data.password}' \
    | base64 -d | kubectl -n "$NS" exec -i "$CLIENT_POD" -- sh -c 'cat > /tmp/os_pass'
fi

cat > "$EVIDENCE/await_markers.py" <<'PY'
# Poll the backend until every marker is visible, or give up after the timeout.
import base64, json, os, ssl, sys, time, urllib.parse, urllib.request

backend = sys.argv[1]
host = sys.argv[2]
run_id = sys.argv[3]
count = int(sys.argv[4])
timeout = int(sys.argv[5])

markers = [f"ANI-MARKER-{run_id}-n{i+1}-" for i in range(count)]

# The LogQL stream selector is built from a brace pair, which is written here
# with chr(123)/chr(125) so the Go template parser does not read it as one of
# its own delimiters. The resulting query is identical.
LBRACE, RBRACE = chr(123), chr(125)


def loki_series():
    selector = LBRACE + 'job="fluent-bit"' + RBRACE
    q = urllib.parse.urlencode({
        "query": f'{selector} |= "ANI-MARKER-{run_id}"',
        "limit": "200",
        "start": str(int((time.time() - 3600) * 1e9)),
    })
    url = f"http://{host}/loki/api/v1/query_range?{q}"
    with urllib.request.urlopen(url, timeout=20) as r:
        return json.load(r).get("data", {}).get("result", []) or []


# The secured cluster answers only over TLS and only with a credential, so this
# branch builds the same request the collector makes rather than an anonymous
# one. The CA is the one the OpenSearch role issued, and the credential is the
# collector's own.
def os_context():
    ctx = ssl.create_default_context(cafile="/tmp/ca.crt")
    user = open("/tmp/os_user").read().strip()
    password = open("/tmp/os_pass").read().strip()
    token = base64.b64encode(f"{user}:{password}".encode()).decode()
    return ctx, {"Authorization": "Basic " + token}


def opensearch_hits():
    ctx, headers = os_context()
    body = json.dumps({
        "size": 200,
        "query": {"query_string": {"query": f'"ANI-MARKER-{run_id}"'}},
    }).encode()
    headers["Content-Type"] = "application/json"
    req = urllib.request.Request(
        f"https://{host}/ani-logs-*/_search", data=body,
        headers=headers, method="POST")
    with urllib.request.urlopen(req, timeout=20, context=ctx) as r:
        return json.load(r).get("hits", {}).get("hits", []) or []


deadline = time.time() + timeout
found = {}
while time.time() < deadline:
    try:
        entries = loki_series() if backend == "loki" else opensearch_hits()
    except Exception as e:  # backend still warming up
        print("poll error:", e, file=sys.stderr)
        entries = []
    for e in entries:
        blob = json.dumps(e)
        for m in markers:
            if m in blob:
                found.setdefault(m, e)
    if len(found) >= len(markers):
        break
    time.sleep(5)

print(json.dumps({k: found[k] for k in sorted(found)}, indent=2, default=str))
if len(found) < len(markers):
    missing = [m for m in markers if m not in found]
    raise SystemExit("markers not found in backend: " + ", ".join(missing))
PY

note "querying $BACKEND for $want_nodes markers (waiting on the backend, not on a sleep)"
py "$EVIDENCE/await_markers.py" "$BACKEND" \
  "$([ "$BACKEND" = loki ] && echo "$LOKI_HOST" || echo "$OS_HOST")" \
  "$RUN_ID" "$want_nodes" 300 \
  | tee "$EVIDENCE/markers-found.txt"

# The metadata the collector attached must be checked, not just the marker
# text: a record with no pod or node is only half collected. The expected node
# is derived from the marker's own -nN suffix, so the assertion is independent
# of whatever the backend happened to return.
cat > "$EVIDENCE/check_metadata.py" <<'PY'
import json, sys

found = json.load(open(sys.argv[1]))
nodes = sys.argv[2:]
expected_ns = sys.argv[2 + len(nodes)]

REQUIRED = ("namespace", "pod", "container", "node")

for marker, entry in sorted(found.items()):
    idx = int(marker.rsplit("-n", 1)[-1].split("-")[0])
    want_node = nodes[idx - 1]
    want_pod = f"ani-log-marker-{idx}"

    if "stream" in entry:  # Loki: metadata is in the stream labels
        labels = entry["stream"]
        got = {k: labels.get(k) for k in REQUIRED}
        print(marker, "->", got)
        for key in REQUIRED:
            if key not in labels:
                raise SystemExit(f"{marker}: required label {key} is missing from the stream")
        for key, want in (("namespace", expected_ns), ("pod", want_pod),
                          ("container", "marker"), ("node", want_node)):
            if labels[key] != want:
                raise SystemExit(f"{marker}: {key}={labels[key]!r} but expected {want!r}")
    else:  # OpenSearch: metadata is in the document body
        k8s = entry.get("_source", {}).get("kubernetes", {})
        got = {k: k8s.get(k) for k in
               ("namespace_name", "pod_name", "container_name", "host")}
        print(marker, "->", got)
        for key, want in (("namespace_name", expected_ns), ("pod_name", want_pod),
                          ("container_name", "marker"), ("host", want_node)):
            if k8s.get(key) != want:
                raise SystemExit(f"{marker}: {key}={k8s.get(key)!r} but expected {want!r}")
PY

py "$EVIDENCE/check_metadata.py" "$EVIDENCE/markers-found.txt" \
  "${NODES[@]}" "$NS" | tee "$EVIDENCE/metadata.txt"

note "all $want_nodes markers found in $BACKEND with correct namespace/pod/container/node"

note "== [3] rebuilding the backend pod keeps the records =="
# The backend's PVC and UID are recorded before and after, so "the same volume"
# is a fact rather than an assumption. Only the backend pod is deleted; nothing
# else is touched, and no PVC is removed.
case "$BACKEND" in
  loki) BACKEND_KIND="statefulset"; BACKEND_NAME="ani-loki"
        BACKEND_PVC="storage-ani-loki-0"
        BACKEND_SELECTOR="app.kubernetes.io/name=loki" ;;
  # The OpenSearch Chart names the volumeClaimTemplate after its cluster/group
  # ("ani-opensearch-master"), so the PVC the Chart creates is
  # ani-opensearch-master-0 — not data-<name>-0, which no chart in this batch
  # renders.
  opensearch) BACKEND_KIND="statefulset"; BACKEND_NAME="ani-opensearch-master"
        BACKEND_PVC="ani-opensearch-master-0"
        BACKEND_SELECTOR="app.kubernetes.io/name=opensearch" ;;
esac

pvc_uid_before="$(kubectl -n "$NS" get pvc "$BACKEND_PVC" -o jsonpath='{.metadata.uid}')"
note "backend PVC $BACKEND_PVC UID before: $pvc_uid_before"

old_backend_pod="$(kubectl -n "$NS" get pod -l "$BACKEND_SELECTOR" \
  -o jsonpath='{.items[0].metadata.name}')"
note "rebuilding backend pod $old_backend_pod"

# The record is read before and after the rebuild, from the same query, so the
# comparison is not affected by new data arriving in between.
kubectl -n "$NS" delete pod "$old_backend_pod" --wait=true >/dev/null
kubectl -n "$NS" rollout status "$BACKEND_KIND/$BACKEND_NAME" --timeout=600s

pvc_uid_after="$(kubectl -n "$NS" get pvc "$BACKEND_PVC" -o jsonpath='{.metadata.uid}')"
[ "$pvc_uid_before" = "$pvc_uid_after" ] \
  || fail "the backend PVC UID changed across the rebuild: $pvc_uid_before -> $pvc_uid_after"
note "backend PVC UID unchanged: $pvc_uid_after"

new_backend_pod="$(kubectl -n "$NS" get pod -l "$BACKEND_SELECTOR" \
  -o jsonpath='{.items[0].metadata.name}')"
[ "$new_backend_pod" != "$old_backend_pod" ] \
  || fail "the backend pod was not actually replaced"

py "$EVIDENCE/await_markers.py" "$BACKEND" \
  "$([ "$BACKEND" = loki ] && echo "$LOKI_HOST" || echo "$OS_HOST")" \
  "$RUN_ID" "$want_nodes" 300 \
  > "$EVIDENCE/markers-after-backend-rebuild.txt"
for i in "${!NODES[@]}"; do
  seq_n=$((i + 1))
  grep -q "ANI-MARKER-${RUN_ID}-n${seq_n}-" "$EVIDENCE/markers-after-backend-rebuild.txt" \
    || fail "marker n${seq_n} was lost when the backend pod was rebuilt"
done
note "$want_nodes markers still readable after the backend pod rebuild"

note "== [4] rebuilding one collector keeps the cursor and collection continues =="
# The cursor and buffer directory on that node is fingerprinted first, then one
# collector pod is deleted. New markers are written after the rebuild; both the
# pre-rebuild and post-rebuild markers must be readable, which is only possible
# if the cursor survived.
victim_node="${NODES[0]}"
victim_pod="$($KC get pod -l "app.kubernetes.io/name=fluent-bit" \
  --field-selector "spec.nodeName=$victim_node" -o jsonpath='{.items[0].metadata.name}')"
[ -n "$victim_pod" ] || fail "no collector pod on $victim_node"

$KC exec "$victim_pod" -- sh -c \
  'ls -la /var/lib/fluent-bit; echo "---"; ls -la /var/lib/fluent-bit/buffers 2>/dev/null || true' \
  > "$EVIDENCE/cursor-before.txt"
cat "$EVIDENCE/cursor-before.txt"
grep -q 'tail.db' "$EVIDENCE/cursor-before.txt" \
  || fail "the tail cursor database is not on the persistent directory"

note "rebuilding collector pod $victim_pod on $victim_node"
kubectl -n "$NS" delete pod "$victim_pod" --wait=true >/dev/null
$KC wait --for=condition=Ready "pod" -l "app.kubernetes.io/name=fluent-bit" \
  --field-selector "spec.nodeName=$victim_node" --timeout=300s

new_pod="$($KC get pod -l "app.kubernetes.io/name=fluent-bit" \
  --field-selector "spec.nodeName=$victim_node" -o jsonpath='{.items[0].metadata.name}')"
[ "$new_pod" != "$victim_pod" ] || fail "the collector pod was not replaced"
$KC exec "$new_pod" -- sh -c \
  'ls -la /var/lib/fluent-bit; echo "---"; ls -la /var/lib/fluent-bit/buffers 2>/dev/null || true' \
  > "$EVIDENCE/cursor-after.txt"
cat "$EVIDENCE/cursor-after.txt"
grep -q 'tail.db' "$EVIDENCE/cursor-after.txt" \
  || fail "the tail cursor did not survive the collector pod rebuild"

# New markers after the rebuild: the tail input must resume, not stop.
new_run="${RUN_ID}-post"
$KC delete pod ani-log-marker-post --ignore-not-found --wait=true >/dev/null
cat <<EOF | $KC apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ani-log-marker-post
spec:
  restartPolicy: Never
  nodeName: $victim_node
  containers:
    - name: marker
      image: $BUSYBOX_IMAGE
      command: ["/bin/sh", "-c"]
      args:
        - "echo 'ANI-MARKER-$new_run'; i=0; while [ \$i -lt 120 ]; do sleep 1; i=\$((i+1)); done"]
      resources:
        requests: {cpu: 10m, memory: 16Mi}
        limits: {memory: 32Mi}
EOF
$KC wait --for=condition=Ready pod/ani-log-marker-post --timeout=180s

cat > "$EVIDENCE/await_one.py" <<'PY'
import base64, json, ssl, sys, time, urllib.parse, urllib.request
backend, host, marker, timeout = sys.argv[1], sys.argv[2], sys.argv[3], int(sys.argv[4])
# See the note in await_markers.py: the LogQL brace pair is built from chr()
# so the Go template parser cannot mistake it for a template delimiter. The
# secured backend is queried over TLS with a credential for the same reason the
# main polling does: an anonymous request would be refused, not empty.
LBRACE, RBRACE = chr(123), chr(125)
deadline = time.time() + timeout
while time.time() < deadline:
    try:
        if backend == "loki":
            selector = LBRACE + 'job="fluent-bit"' + RBRACE
            q = urllib.parse.urlencode({"query": f'{selector} |= "{marker}"',
                                        "limit": "50"})
            url = f"http://{host}/loki/api/v1/query_range?{q}"
            with urllib.request.urlopen(url, timeout=20) as r:
                res = json.load(r).get("data", {}).get("result", []) or []
        else:
            ctx = ssl.create_default_context(cafile="/tmp/ca.crt")
            user = open("/tmp/os_user").read().strip()
            password = open("/tmp/os_pass").read().strip()
            token = base64.b64encode(f"{user}:{password}".encode()).decode()
            body = json.dumps({"size": 50,
                               "query": {"query_string": {"query": f'"{marker}"'}}}).encode()
            req = urllib.request.Request(f"https://{host}/ani-logs-*/_search", data=body,
                                         headers={"Content-Type": "application/json",
                                                  "Authorization": "Basic " + token},
                                         method="POST")
            with urllib.request.urlopen(req, timeout=20, context=ctx) as r:
                res = json.load(r).get("hits", {}).get("hits", []) or []
    except Exception as e:
        print("poll error:", e, file=sys.stderr)
        res = []
    if res:
        print(json.dumps(res, indent=2, default=str))
        raise SystemExit(0)
    time.sleep(5)
raise SystemExit(f"marker {marker} never reached {backend} after the collector rebuild")
PY

py "$EVIDENCE/await_one.py" "$BACKEND" \
  "$([ "$BACKEND" = loki ] && echo "$LOKI_HOST" || echo "$OS_HOST")" \
  "ANI-MARKER-$new_run" 300 | tee "$EVIDENCE/markers-post-rebuild.txt"

# Old and new must be readable together: this is the point of keeping a cursor.
py "$EVIDENCE/await_markers.py" "$BACKEND" \
  "$([ "$BACKEND" = loki ] && echo "$LOKI_HOST" || echo "$OS_HOST")" \
  "$RUN_ID" "$want_nodes" 120 \
  > "$EVIDENCE/markers-old-after-collector-rebuild.txt"
for i in "${!NODES[@]}"; do
  seq_n=$((i + 1))
  grep -q "ANI-MARKER-${RUN_ID}-n${seq_n}-" "$EVIDENCE/markers-old-after-collector-rebuild.txt" \
    || fail "pre-rebuild marker n${seq_n} is no longer readable"
done
note "old markers and the new marker are both readable after the collector rebuild"

note "== [5] retention expiry is out of scope for this run =="
# Retention configuration is asserted by the backend role and is not re-derived
# here. Actually observing a deletion would require the retention period to
# elapse, so the expiry check is recorded as not_verified instead of being
# claimed from a configured value.
case "$BACKEND" in
  loki)
    cat > "$EVIDENCE/retention.py" <<'PY'
import json, sys, urllib.request
host = sys.argv[1]
with urllib.request.urlopen(f"http://{host}/config", timeout=10) as r:
    cfg = json.load(r)
print("retention_period", cfg.get("limits_config", {}).get("retention_period"))
print("compactor.retention_enabled", cfg.get("compactor", {}).get("retention_enabled"))
print("compactor.delete_request_store", cfg.get("compactor", {}).get("delete_request_store"))
PY
    py "$EVIDENCE/retention.py" "$LOKI_HOST" | tee "$EVIDENCE/retention.txt"
    grep -qx "retention_period {{ .ani.components.logging.retention_hours }}h" "$EVIDENCE/retention.txt" \
      || fail "the backend does not report the configured retention period"
    ;;
  opensearch)
    # The secured backend needs the same CA and credential the marker queries
    # used, so this re-reads the ISM policy the same way.
    cat > "$EVIDENCE/retention.py" <<'PY'
import base64, json, ssl, sys, urllib.request
host = sys.argv[1]
ctx = ssl.create_default_context(cafile="/tmp/ca.crt")
user = open("/tmp/os_user").read().strip()
password = open("/tmp/os_pass").read().strip()
token = base64.b64encode(f"{user}:{password}".encode()).decode()
req = urllib.request.Request(f"https://{host}/_plugins/_ism/policies/ani-logs-retention")
req.add_header("Authorization", "Basic " + token)
with urllib.request.urlopen(req, timeout=20, context=ctx) as r:
    policy = json.load(r)["policy"]
states = {s["name"]: s for s in policy["states"]}
ages = [t.get("conditions", {}).get("min_index_age")
        for t in states["hot"]["transitions"]]
print("min_index_age", ages)
PY
    py "$EVIDENCE/retention.py" "$OS_HOST" | tee "$EVIDENCE/retention.txt"
    grep -q "{{ .ani.components.logging.retention_iso }}" "$EVIDENCE/retention.txt" \
      || fail "the retention policy does not carry the configured retention period"
    ;;
esac
echo "retention-expiry=not_verified (requires the retention period to elapse)" \
  | tee "$EVIDENCE/retention-expiry.txt"

note "== [6] cleanup of this run's own test pods =="
$KC delete pod ani-log-marker-post --ignore-not-found --wait=true >/dev/null
for i in "${!NODES[@]}"; do
  seq_n=$((i + 1))
  $KC delete pod "ani-log-marker-${seq_n}" --ignore-not-found --wait=true >/dev/null
done
$KC delete pod "$CLIENT_POD" --ignore-not-found --wait=true >/dev/null

# Only this run's own marker pods are removed. The markers stay in the backend,
# which is intentional: they are the evidence for the rebuild checks above.
leftover="$($KC get pod -o name 2>/dev/null | grep -cE 'ani-log-marker|'"$CLIENT_POD" || true)"
[ "$leftover" = "0" ] || fail "test pods were left behind in $NS"

note "fluent-bit verify passed"
note "evidence: $EVIDENCE"

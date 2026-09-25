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
# A20 (evidence h4loki-a24): the key must match images.tsv EXACTLY -- the
# table carries busybox:1.37.0, and this file once read busybox:1.37, which
# index() resolved to nothing and rendered as a Go-template nil value, so
# every marker pod failed with InvalidImageName on a real install. verify.sh
# is only checked offline by TEMPLATE_KIND=verify of the render gate, whose
# product-level scan would reject the nil spelling itself -- which is also
# why this comment must never contain that literal.
BUSYBOX_IMAGE="{{ index .ani.images "docker.io/library/busybox:1.37.0" }}"
CLIENT_POD="ani-fluent-bit-verify-client"

OUT_DIR="${ANI_VERIFY_OUTPUT_DIR:-/var/lib/ani-installer/logs}"
install -d -m 0700 "$OUT_DIR"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"
EVIDENCE="$OUT_DIR/fluent-bit-verify-$RUN_ID"
install -d -m 0700 "$EVIDENCE"

fail() { echo "fluent-bit verify: $*" >&2; exit 1; }
note() { printf '%s\n' "$*"; }

KC="kubectl -n $NS"

# K-5 (appendix A of the foundation status doc): the base kcn/OVN layer hands
# a rebuilt pod a dead sandbox every so often -- valid IP, ARP announced, but
# the datapath blackholes it and kubelet probe-restarts the container forever
# without rebuilding the sandbox (observed live on h4loki-a12/a14/a15/a17).
# The a17 live recovery experiment (2026-09-20, evidence
# h4loki-a17-k5recovery-exp.log) showed that restarting the kcn-cni-ds and
# kcn-ovs-ds agents on the pod's node and then deleting the pod once more
# recovered the slot in ~20s.
#
# R02 removed that recovery from the acceptance path. A verification must not
# repair the cluster it is verifying, so the agent restarts and the repeated
# rebuilds are gone: a hit K-5 flake now records read-only evidence and fails
# the run. What remains is the ONE planned rebuild of this script's own pods
# (backend / collector), which the durability checks already perform by
# deleting a named pod and letting its workload recreate it; each planned
# rebuild is recorded in $EVIDENCE/k5-retries.txt. Nothing outside $NS is ever
# deleted, and moving the planned rebuild to the acceptance level is R13's
# decision. The R02 task card is the authority for this change.
k5_collect_failure_evidence() { # k5_collect_failure_evidence <what> — read-only diagnostics, never changes the caller's result
  local what="$1" rc=0
  {
    echo "== k5 failure evidence: $what =="
    date -u +%Y-%m-%dT%H:%M:%SZ
  } >> "$EVIDENCE/k5-failure-$what.txt" 2>&1 || rc=1
  kubectl -n "$NS" get pod -o wide >> "$EVIDENCE/k5-failure-$what.txt" 2>&1 || rc=1
  kubectl -n "$NS" get events --sort-by=.lastTimestamp >> "$EVIDENCE/k5-failure-$what.txt" 2>&1 || rc=1
  echo "k5_failure_evidence $what $(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "$EVIDENCE/k5-retries.txt" 2>&1 || rc=1
  return "$rc"
}
k5_pod_ready() { # k5_pod_ready <selector> <timeout> — bounded wait until one Ready pod matches <selector>
  # The wait never uses `kubectl rollout status`: on the lab's kubectl v1.35
  # it can exit 0 on a timeout or on a stale workload status, which on a18
  # silently passed a prometheus pod that stayed un-Ready for 27 minutes
  # (evidence: h4loki-a18). The kubelet-written pod Ready condition is the
  # honest signal, so the K-5 waits poll that instead.
  local sel="$1" t="$2" deadline rdy
  deadline=$(( $(date +%s) + ${t%s} ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    rdy="$(kubectl -n "$NS" get pod -l "$sel" \
      -o jsonpath='{range .items[*]}{.status.conditions[?(@.type=="Ready")].status}{end}' 2>/dev/null)"
    [ "$rdy" = "True" ] && return 0
    sleep 10
  done
  return 1
}
# R02 test seam: ANI_VERIFY_LIB_ONLY=1 defines the helpers above and returns, so
# the offline behaviour tests can call the real functions with a fake kubectl.
# Production runs never set it and take exactly the same path as before.
if [ "${ANI_VERIFY_LIB_ONLY:-}" = "1" ]; then
  return 0 2>/dev/null || exit 0
fi


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
  $KC delete pod "$CLIENT_POD" --ignore-not-found --wait=true --timeout=300s >/dev/null
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

# The rendered config is read back from the cluster rather than from the
# values file, so a chart that ignored a value would not pass. It is resolved
# through the running collector's pod spec -- the volumes kubelet actually
# mounts -- and then read from that ConfigMap over the API: the fluent-bit
# image is distroless with no shell or coreutils, so exec-ing `cat` into the
# collector is impossible (A18, evidence h4loki-a22: exec failed with
# `cat: executable file not found in $PATH`). The collector being Ready is
# what proves the mount happened; the ConfigMap is what kubelet projected.
COLLECTOR_POD="$($KC get pod -l "app.kubernetes.io/name=fluent-bit" \
  -o jsonpath='{.items[0].metadata.name}')"
[ -n "$COLLECTOR_POD" ] || fail "no collector pod found"
CONF_CM=""
for cm in $($KC get pod "$COLLECTOR_POD" -o jsonpath='{.spec.volumes[*].configMap.name}'); do
  cm_data="$($KC get configmap "$cm" -o jsonpath='{.data}' 2>/dev/null || true)"
  case "$cm_data" in
    *'"fluent-bit.conf"'*) CONF_CM="$cm"; break ;;
  esac
done
[ -n "$CONF_CM" ] || fail "no ConfigMap mounted by the collector carries fluent-bit.conf"
$KC get configmap "$CONF_CM" -o jsonpath='{.data.fluent-bit\.conf}' \
  > "$EVIDENCE/rendered-fluent-bit.conf"
[ -s "$EVIDENCE/rendered-fluent-bit.conf" ] \
  || fail "the fluent-bit.conf read from ConfigMap $CONF_CM is empty"
note "collector config read from ConfigMap $CONF_CM (mounted by $COLLECTOR_POD)"

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
  $KC delete pod "$pod" --ignore-not-found --wait=true --timeout=300s >/dev/null
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
        - "echo '$marker'; i=0; while [ \$i -lt 90 ]; do sleep 1; i=\$((i+1)); done"
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
  opensearch)
    OS_HOST="ani-opensearch-master.$NS.svc.cluster.local:9200"
    RETENTION_DAYS="{{ .ani.components.logging.retention_days }}" ;;
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
  # A36b: the ISM policy API is admin-only, so the client pod also carries the
  # administrator credential for the retention-policy read.
  $KC get secret ani-opensearch-admin -o jsonpath='{.data.username}' \
    | base64 -d | kubectl -n "$NS" exec -i "$CLIENT_POD" -- sh -c 'cat > /tmp/admin_user'
  $KC get secret ani-opensearch-admin -o jsonpath='{.data.password}' \
    | base64 -d | kubectl -n "$NS" exec -i "$CLIENT_POD" -- sh -c 'cat > /tmp/admin_pass'
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
#
# A21 (evidence h4loki-a25): the helpers run inside the client pod, which has
# no access to the installer node filesystem, so a host path passed as an
# argument cannot be opened there. The query result is therefore uploaded into
# the pod first and the checker reads the pod-local copy; the node-side file
# written by tee below stays behind as run evidence. The sequence number is
# parsed with a regex anchored on -nN- because the marker text continues with
# the issuing host and the target node names, and a right-to-left split would
# land inside those. The namespace is read from the last argument, not from a
# length-derived index, because the node list already swallows every argument
# between the file and the namespace.
cat > "$EVIDENCE/check_metadata.py" <<'PY'
import json, re, sys

found = json.load(open(sys.argv[1]))
nodes = sys.argv[2:-1]
expected_ns = sys.argv[-1]

REQUIRED = ("namespace", "pod", "container", "node")

for marker, entry in sorted(found.items()):
    seq = re.search(r"-n(\d+)-", marker)
    if not seq:
        raise SystemExit(f"{marker}: cannot parse the marker sequence number")
    idx = int(seq.group(1))
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

$KC exec -i "$CLIENT_POD" -- sh -c 'cat > /tmp/markers-found.json' \
  < "$EVIDENCE/markers-found.txt"
py "$EVIDENCE/check_metadata.py" /tmp/markers-found.json \
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
  # The OpenSearch Chart names the volumeClaimTemplate after its cluster/group,
  # which is also the StatefulSet name, so the PVC is
  # ani-opensearch-master-ani-opensearch-master-0 (<template>-<sts>-<ordinal>).
  # The name is derived from the rendered StatefulSet rather than hardcoded:
  # an earlier version used the pod-shaped name ani-opensearch-master-0, which
  # is never a PVC.
  opensearch) BACKEND_KIND="statefulset"; BACKEND_NAME="ani-opensearch-master"
        BACKEND_PVC="$(kubectl -n "$NS" get statefulset ani-opensearch-master \
          -o jsonpath='{.spec.volumeClaimTemplates[0].metadata.name}')-ani-opensearch-master-0"
        # The jsonpath above reads the claim template name (ani-opensearch-master),
        # so the result is ani-opensearch-master-ani-opensearch-master-0.
        BACKEND_SELECTOR="app.kubernetes.io/name=opensearch" ;;
esac

pvc_uid_before="$(kubectl -n "$NS" get pvc "$BACKEND_PVC" -o jsonpath='{.metadata.uid}')"
note "backend PVC $BACKEND_PVC UID before: $pvc_uid_before"

old_backend_pod="$(kubectl -n "$NS" get pod -l "$BACKEND_SELECTOR" \
  -o jsonpath='{.items[0].metadata.name}')"
old_backend_pod_uid="$(kubectl -n "$NS" get pod "$old_backend_pod" \
  -o jsonpath='{.metadata.uid}')"
note "rebuilding backend pod $old_backend_pod (uid $old_backend_pod_uid)"

# The record is read before and after the rebuild, from the same query, so the
# comparison is not affected by new data arriving in between.
kubectl -n "$NS" delete pod "$old_backend_pod" --wait=true --timeout=300s >/dev/null
if ! k5_pod_ready "$BACKEND_SELECTOR" 600s; then
  note "K5_REBUILD backend: the rebuilt pod is not Ready; performing the one planned rebuild of the backend pod"
  echo "k5_rebuild_planned backend $(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "$EVIDENCE/k5-retries.txt"
  kubectl -n "$NS" delete pod -l "$BACKEND_SELECTOR" --timeout=180s >/dev/null 2>&1 || true
  if ! k5_pod_ready "$BACKEND_SELECTOR" 420s; then
    # R02: no agent restart and no third rebuild. Record read-only evidence and
    # fail the check; the caller's exit code stays this failure.
    k5_collect_failure_evidence backend || true
    fail "the rebuilt backend pod did not become Ready and the planned rebuild is already spent (no agent restart is attempted)"
  fi
fi

pvc_uid_after="$(kubectl -n "$NS" get pvc "$BACKEND_PVC" -o jsonpath='{.metadata.uid}')"
[ "$pvc_uid_before" = "$pvc_uid_after" ] \
  || fail "the backend PVC UID changed across the rebuild: $pvc_uid_before -> $pvc_uid_after"
note "backend PVC UID unchanged: $pvc_uid_after"

# A23 (evidence h4loki-a27): the backend is a StatefulSet, so a rebuilt pod
# always comes back under the SAME name -- the old name-vs-name comparison
# matched "ani-loki-0" against "ani-loki-0" and failed on a pod that HAD been
# genuinely replaced (delete --wait succeeded, the new pod went Ready, the PVC
# UID was unchanged). The replacement fact lives in the pod UID, not the name.
new_backend_pod="$(kubectl -n "$NS" get pod -l "$BACKEND_SELECTOR" \
  -o jsonpath='{.items[0].metadata.name}')"
new_backend_pod_uid="$(kubectl -n "$NS" get pod "$new_backend_pod" \
  -o jsonpath='{.metadata.uid}')"
[ "$new_backend_pod_uid" != "$old_backend_pod_uid" ] \
  || fail "the backend pod was not actually replaced (uid $old_backend_pod_uid survived the rebuild)"
note "backend pod replaced: $old_backend_pod/$old_backend_pod_uid -> $new_backend_pod/$new_backend_pod_uid"

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

# The state directory is fingerprinted via a disposable busybox pod mounted on
# the same per-node hostPath, not by exec-ing into the collector: the
# fluent-bit image is distroless and has no shell or coreutils (A18, evidence
# h4loki-a22). The inspector runs on the victim node with the exact hostPath
# the collector mounts at /var/lib/fluent-bit (values: fluent-bit-state ->
# /var/lib/ani-installer/fluent-bit), so it sees the collector's own files.
# Read-only, and deleted after each use.
cursor_ls() { # cursor_ls <node> <outfile>
  local node="$1" out="$2" inspector="ani-fb-cursor-inspector" ph i
  $KC delete pod "$inspector" --ignore-not-found --wait=true --timeout=300s >/dev/null 2>&1 || true
  cat <<EOF | $KC apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: $inspector
  labels:
    ani-verify: cursor-inspector
spec:
  restartPolicy: Never
  nodeName: $node
  containers:
    - name: inspect
      image: $BUSYBOX_IMAGE
      command: ["/bin/sh", "-c"]
      args: ["ls -la /state; echo '---'; ls -la /state/buffers 2>/dev/null || true"]
      resources:
        requests: {cpu: 10m, memory: 16Mi}
        limits: {memory: 32Mi}
      volumeMounts:
        - name: state
          mountPath: /state
          readOnly: true
  volumes:
    - name: state
      hostPath:
        path: /var/lib/ani-installer/fluent-bit
        type: Directory
EOF
  ph=""; i=0
  while [ "$i" -lt 60 ]; do
    ph="$($KC get pod "$inspector" -o jsonpath='{.status.phase}' 2>/dev/null)"
    [ "$ph" = "Succeeded" ] && break
    [ "$ph" = "Failed" ] && fail "the cursor inspector pod on $node entered Failed phase"
    sleep 2; i=$((i+1))
  done
  [ "$ph" = "Succeeded" ] || fail "the cursor inspector pod on $node did not finish in 120s"
  $KC logs "$inspector" > "$out"
  $KC delete pod "$inspector" --wait=true --timeout=300s >/dev/null
}

cursor_ls "$victim_node" "$EVIDENCE/cursor-before.txt"
cat "$EVIDENCE/cursor-before.txt"
grep -q 'tail.db' "$EVIDENCE/cursor-before.txt" \
  || fail "the tail cursor database is not on the persistent directory"

note "rebuilding collector pod $victim_pod on $victim_node"
kubectl -n "$NS" delete pod "$victim_pod" --wait=true --timeout=300s >/dev/null
# Same single-planned-rebuild handling as the backend step above: one bounded
# re-wait, and on failure read-only evidence plus a failing check. The agent
# restart and the extra rebuilds are gone (R02).
if ! $KC wait --for=condition=Ready "pod" -l "app.kubernetes.io/name=fluent-bit" \
  --field-selector "spec.nodeName=$victim_node" --timeout=300s; then
  note "K5_REBUILD collector: the rebuilt pod is not Ready; performing the one planned rebuild of the collector pod"
  echo "k5_rebuild_planned collector $(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "$EVIDENCE/k5-retries.txt"
  kubectl -n "$NS" delete pod -l "app.kubernetes.io/name=fluent-bit" \
    --field-selector "spec.nodeName=$victim_node" --timeout=180s >/dev/null 2>&1 || true
  if ! $KC wait --for=condition=Ready "pod" -l "app.kubernetes.io/name=fluent-bit" \
    --field-selector "spec.nodeName=$victim_node" --timeout=300s; then
    k5_collect_failure_evidence collector || true
    fail "the rebuilt collector pod did not become Ready and the planned rebuild is already spent (no agent restart is attempted)"
  fi
fi

new_pod="$($KC get pod -l "app.kubernetes.io/name=fluent-bit" \
  --field-selector "spec.nodeName=$victim_node" -o jsonpath='{.items[0].metadata.name}')"
[ "$new_pod" != "$victim_pod" ] || fail "the collector pod was not replaced"
cursor_ls "$victim_node" "$EVIDENCE/cursor-after.txt"
cat "$EVIDENCE/cursor-after.txt"
grep -q 'tail.db' "$EVIDENCE/cursor-after.txt" \
  || fail "the tail cursor did not survive the collector pod rebuild"

# New markers after the rebuild: the tail input must resume, not stop.
new_run="${RUN_ID}-post"
$KC delete pod ani-log-marker-post --ignore-not-found --wait=true --timeout=300s >/dev/null
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
        - "echo 'ANI-MARKER-$new_run'; i=0; while [ \$i -lt 120 ]; do sleep 1; i=\$((i+1)); done"
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
    # A14: the first version of this branch called json.load() on the /config
    # answer with a 10s socket timeout. Loki answers this endpoint with YAML
    # text, not JSON, and streams the ~92KB dump slowly enough that a 10s
    # timeout cut the body mid-read on a real install -- the same two defects
    # the loki role's own [3] fixed (A11/A12) and proved there. This branch
    # therefore runs the same fetch+parse+compare that passed a real install,
    # including the 72h/3d duration equivalence: Loki echoes whatever unit the
    # configuration used, and 72h and 3d are the same duration.
    cat > "$EVIDENCE/query_config.py" <<'PY'
import sys, time, urllib.request

host = sys.argv[1]
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
    """Return {(section, key): value} for the two-level scalars this reads.

    A scalar belongs to its section only at two-space indent; a list entry's
    own fields (four-space) are flattened into the parent section; anything
    deeper is a nested mapping's business and is skipped, so a same-named
    deeper key cannot overwrite the section's own value.
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
]:
    print(f"{section}.{key}", cfg.get((section, key)))
if ("limits_config", "retention_period") not in cfg:
    raise SystemExit("config response did not parse as the expected YAML mapping")
PY
    py "$EVIDENCE/query_config.py" "$LOKI_HOST" | tee "$EVIDENCE/retention.txt"

    grep -qx "content_type text/plain" "$EVIDENCE/retention.txt" \
      || fail "Loki did not answer /config as YAML text, so this check reads the wrong format"

    RETENTION_PERIOD_SECONDS="$(python3 - "{{ .ani.components.logging.retention_hours }}h" <<'PY'
import re, sys
m = re.fullmatch(r"(\d+)([smhd])", sys.argv[1])
if not m:
    raise SystemExit(f"unparseable retention period: {sys.argv[1]}")
n, unit = int(m.group(1)), m.group(2)
print(n * {"s": 1, "m": 60, "h": 3600, "d": 86400}[unit])
PY
)"
    echo "retention_period_seconds $RETENTION_PERIOD_SECONDS" >> "$EVIDENCE/retention.txt"

    REPORTED_PERIOD="$(awk '$1 == "limits_config.retention_period" {print $2}' "$EVIDENCE/retention.txt")"
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
    echo "reported_retention_period_seconds $REPORTED_SECONDS" >> "$EVIDENCE/retention.txt"
    [ "$REPORTED_SECONDS" = "$RETENTION_PERIOD_SECONDS" ] \
      || fail "Loki retains for $REPORTED_PERIOD ($REPORTED_SECONDS s), not the configured value ($RETENTION_PERIOD_SECONDS s)"

    grep -qx "compactor.retention_enabled true" "$EVIDENCE/retention.txt" \
      || fail "the compactor does not have retention_enabled, so nothing would ever be deleted"
    grep -qx "compactor.delete_request_store filesystem" "$EVIDENCE/retention.txt" \
      || fail "the compactor has no filesystem delete store"
    ;;
  opensearch)
    # The secured backend needs the same CA and credential the marker queries
    # used, so this re-reads the ISM policy the same way.
    cat > "$EVIDENCE/retention.py" <<'PY'
import base64, json, ssl, sys, urllib.request
host = sys.argv[1]
ctx = ssl.create_default_context(cafile="/tmp/ca.crt")
# A36b: the ISM policy API is admin-only (the collector identity gets 403),
# so this read uses the administrator credential.
user = open("/tmp/admin_user").read().strip()
password = open("/tmp/admin_pass").read().strip()
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
    grep -qE "{{ .ani.components.logging.retention_iso }}|${RETENTION_DAYS}d" "$EVIDENCE/retention.txt" \
      || fail "the retention policy does not carry the configured retention period"
    ;;
esac
echo "retention-expiry=not_verified (requires the retention period to elapse)" \
  | tee "$EVIDENCE/retention-expiry.txt"

note "== [6] cleanup of this run's own test pods =="
$KC delete pod ani-log-marker-post --ignore-not-found --wait=true --timeout=300s >/dev/null
for i in "${!NODES[@]}"; do
  seq_n=$((i + 1))
  $KC delete pod "ani-log-marker-${seq_n}" --ignore-not-found --wait=true --timeout=300s >/dev/null
done
$KC delete pod "$CLIENT_POD" --ignore-not-found --wait=true --timeout=300s >/dev/null

# Only this run's own marker pods are removed. The markers stay in the backend,
# which is intentional: they are the evidence for the rebuild checks above.
leftover="$($KC get pod -o name 2>/dev/null | grep -cE 'ani-log-marker|'"$CLIENT_POD" || true)"
[ "$leftover" = "0" ] || fail "test pods were left behind in $NS"

note "fluent-bit verify passed"
note "evidence: $EVIDENCE"

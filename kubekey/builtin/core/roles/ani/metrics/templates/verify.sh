#!/usr/bin/env bash
# Metrics and alerting component verification, rendered by the ANI metrics role.
# Template source: builtin/core/roles/ani/metrics/templates/verify.sh
#
# Acceptance for this card, in the plan's order:
#   1. real series through the Prometheus HTTP API: up, node_uname_info,
#      kube_node_info and one actual cAdvisor container metric, covering every
#      node and a running workload -- not just /-/ready;
#   2. a unique PrometheusRule driving `vector(1) == 1` through Prometheus,
#      Alertmanager and a temporary webhook receiver, then the same rule
#      changed to `vector(0) == 1` delivering a resolved notification with the
#      same fingerprint. The expression has no `bool` modifier: the rule only
#      recovers by evaluating to an empty vector, so a rule that merely goes
#      quiet would not produce the resolved the receiver requires;
#   3. the temporary receiver runs from the offline python image and every test
#      label is unique to this run, so no real notification channel is touched;
#   4. Prometheus PVC/StatefulSet UID before and after a normal single-pod
#      rebuild, with a unique sample written before the rebuild and read back
#      by range query afterwards -- `up` alone would prove nothing;
#   5. an Alertmanager silence created before the same kind of rebuild and read
#      back by ID afterwards, with the PVC and generated Secret unchanged. This
#      is not a claim that no alert is ever lost while the process restarts.
#
# Every piece of python this script needs is written to $OUT_DIR first and fed
# to the client pod on stdin, so nothing is executed before it exists and no
# lab artefact has to be copied into the cluster. Nothing writes alerts
# straight to Alertmanager: every notification the receiver sees was produced
# by Prometheus evaluating a rule, so the whole chain is exercised.
set -euo pipefail

KUBECONFIG_FILE="${ANI_VERIFY_KUBECONFIG:-/etc/kubernetes/admin.conf}"
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_FILE")
NS="{{ .ani.components.metrics.namespace }}"
PROM_SVC="ani-metrics-prometheus.$NS.svc.cluster.local:9090"
AM_SVC="ani-metrics-alertmanager.$NS.svc.cluster.local:9093"
PY_IMAGE="{{ index .ani.images "docker.io/library/python:3.13.11-alpine3.23" }}"
RUN_ID="$(date +%Y%m%d%H%M%S)-$$"
OUT_DIR="${ANI_VERIFY_OUTPUT_DIR:-/tmp/ani-metrics-verify-${RUN_ID}}"
mkdir -p "$OUT_DIR"
fail() { echo "FAIL: $*" >&2; exit 1; }

# K-5: the base kcn/OVN layer occasionally hands a *rebuilt* pod a dead
# sandbox -- the CNI answers add with a valid IP and even an ARP announce,
# but the datapath blackholes the pod in both directions, and kubelet then
# probe-restarts the container forever without ever rebuilding the sandbox,
# so the pod can never recover by itself (recorded since B2 as appendix A of
# the foundation status document, which documents "rebuild the pod" as the
# recovery). Observed live on h4loki-a12/a14/a15/a17. The a17 live recovery
# experiment (2026-09-20, evidence h4loki-a17-k5recovery-exp.log) showed that
# restarting the kcn-cni-ds and kcn-ovs-ds agents on the pod's node and then
# deleting the pod once more recovered the slot in ~20s.
#
# R02 removed that recovery from the acceptance path. A verification must not
# repair the cluster it is verifying: the agent restarts, the repeated rebuild
# and the post-Ready re-entry are gone, so a hit K-5 flake now fails the run
# visibly instead of being papered over. What a wait may still do is the ONE
# planned rebuild of the component's own pod that the durability check already
# performs (delete the pod, let the StatefulSet recreate it); that single
# rebuild stays here, recorded in $OUT_DIR/k5-retries.txt, and moving it to the
# acceptance level is R13's decision. Nothing outside $NS is ever deleted, and
# a timeout after that one rebuild records read-only evidence and returns
# non-zero. The R02 task card is the authority for this change.
#
# The waits themselves never use `kubectl rollout status`: on the lab's
# kubectl v1.35 it can exit 0 both on timeout and on a stale STS status
# ("readyReplicas" has not caught up with the deletion yet), which on a18
# silently passed a prometheus pod that stayed un-Ready for 27 minutes
# (evidence: h4loki-a18, k5-retries.txt has no "prometheus" entry). Every
# K-5 wait below polls the kubelet-written pod Ready condition instead.
k5_pod_ready() { # k5_pod_ready <selector> <timeout> — bounded wait until one Ready pod matches <selector>
  local sel="$1" t="$2" deadline rdy
  deadline=$(( $(date +%s) + ${t%s} ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    rdy="$("${KUBECTL[@]}" -n "$NS" get pod -l "$sel" \
      -o jsonpath='{range .items[*]}{.status.conditions[?(@.type=="Ready")].status}{end}' 2>/dev/null)"
    [ "$rdy" = "True" ] && return 0
    sleep 10
  done
  return 1
}
k5_workload_ready() { # k5_workload_ready <kind/name> <timeout> — bounded wait until every replica is Ready
  local wl="$1" t="$2" deadline stats want have
  deadline=$(( $(date +%s) + ${t%s} ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    case "$wl" in
      daemonset/*)
        stats="$("${KUBECTL[@]}" -n "$NS" get "$wl" \
          -o jsonpath='{.status.desiredNumberScheduled}{" "}{.status.numberReady}' 2>/dev/null)"
        [ -n "$stats" ] && [ "${stats%% *}" != "0" ] \
          && [ "${stats##* }" = "${stats%% *}" ] && return 0
        ;;
      *)
        stats="$("${KUBECTL[@]}" -n "$NS" get "$wl" \
          -o jsonpath='{.spec.replicas}{" "}{.status.readyReplicas}' 2>/dev/null)"
        want="${stats%% *}"; have="${stats##* }"
        [ -n "$stats" ] && [ "${want:-1}" != "0" ] \
          && [ "${have:-0}" = "${want:-1}" ] && return 0
        ;;
    esac
    sleep 10
  done
  return 1
}
k5_rebuild_wait() { # k5_rebuild_wait <selector> <what> <first-timeout> <second-timeout>
  # F01: this is a READ-ONLY bounded waiter. It never deletes and never rebuilds.
  # The single planned Pod recreation, when a durability check needs one, is
  # performed by the CALLER (the acceptance path, gated by --allow-pod-recreate
  # and a run+target one-shot ledger) — not as a timeout "recovery" here.
  # Removing the delete-on-timeout is exactly what stops the caller's one
  # planned rebuild from silently becoming a second rebuild. A pod that is still
  # not Ready records read-only evidence and returns non-zero; the caller fails
  # instead of repairing the cluster it is verifying.
  local sel="$1" what="$2" t1="$3" t2="$4"
  if k5_pod_ready "$sel" "$t1"; then
    return 0
  fi
  # Not ready within the first window: wait out the second window WITHOUT any
  # mutation, then give up with read-only evidence.
  echo "  K5_WAIT $what: not Ready within $t1; waiting $t2 more without rebuilding" >&2
  echo "k5_wait_extend $what $(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "$OUT_DIR/k5-retries.txt"
  if k5_pod_ready "$sel" "$t2"; then
    return 0
  fi
  echo "  K5_WAIT $what: still not Ready within $t2; no rebuild, no agent restart, no further change is attempted" >&2
  k5_collect_failure_evidence "$sel" "$what" || true
  return 1
}
k5_collect_failure_evidence() { # k5_collect_failure_evidence <selector> <what> — read-only diagnostics, never changes the caller's result
  local sel="$1" what="$2" rc=0 pods=""
  {
    echo "== k5 failure evidence: $what (selector $sel) =="
    date -u +%Y-%m-%dT%H:%M:%SZ
    echo "-- pods --"
  } >> "$OUT_DIR/k5-failure-$what.txt" 2>&1 || rc=1
  "${KUBECTL[@]}" -n "$NS" get pod -l "$sel" -o wide >> "$OUT_DIR/k5-failure-$what.txt" 2>&1 || rc=1
  pods="$("${KUBECTL[@]}" -n "$NS" get pod -l "$sel" -o name 2>/dev/null || true)"
  if [ -n "$pods" ]; then
    # shellcheck disable=SC2086 # $pods is a newline-separated list of pod names
    "${KUBECTL[@]}" -n "$NS" describe $pods >> "$OUT_DIR/k5-failure-$what.txt" 2>&1 || rc=1
  fi
  "${KUBECTL[@]}" -n "$NS" get events --sort-by=.lastTimestamp >> "$OUT_DIR/k5-failure-$what.txt" 2>&1 || rc=1
  echo "k5_failure_evidence $what $(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "$OUT_DIR/k5-retries.txt" 2>&1 || rc=1
  # Diagnostics are best-effort: a failing diagnostic command is recorded, and
  # the caller keeps its own (already failing) result.
  return "$rc"
}

# R02 test seam: with ANI_VERIFY_LIB_ONLY=1 this file only defines the helpers
# above and returns, so the offline behaviour tests in pkg/ani can call the real
# K-5 functions with a fake kubectl instead of re-implementing them. Production
# runs never set the variable and take exactly the same path as before.
if [ "${ANI_VERIFY_LIB_ONLY:-}" = "1" ]; then
  return 0 2>/dev/null || exit 0
fi

PROM_STS=statefulset/prometheus-ani-metrics-prometheus
AM_STS=statefulset/alertmanager-ani-metrics-alertmanager
RECV_NAME="ani-metrics-recv-${RUN_ID}"
RULE_NAME="ani-metrics-rule-${RUN_ID}"
AMCFG_NAME="ani-metrics-amcfg-${RUN_ID}"
CLIENT_POD="ani-metrics-client-${RUN_ID}"
RECV_DEPLOY="ani-metrics-recv-${RUN_ID}"
RUN_LABEL="{{ .ani.components.metrics.run_id }}"

# F01 real layer split. `smoke` (the default, and what the install role and
# `kk ani verify --level smoke` run) performs only necessary workload readiness
# ([1/8]) plus read-only functional probes scoped to THIS attempt ([2/8]) — it
# never mutates global alert routing, creates silences/rules/receivers, deletes
# any pod, or clears other attempts' objects. `acceptance` runs the full
# firing/resolved notification chain and the two planned Pod-recreation
# durability checks ([3/8]..[8/8]); it is reached only through the acceptance
# path with an explicit --allow-pod-recreate and a run+target one-shot ledger.
LEVEL="${ANI_VERIFY_LEVEL:-smoke}"
case "$LEVEL" in
  smoke|acceptance) : ;;
  *) echo "FAIL: ANI_VERIFY_LEVEL must be 'smoke' or 'acceptance', got '$LEVEL'" >&2; exit 1 ;;
esac

# ---------------------------------------------------------------------------
# python helpers, written before anything uses them
# ---------------------------------------------------------------------------
# Every helper reads its input from argv and writes one line to stdout, so the
# shell stays in charge of the assertions. They import only the standard
# library: the lab image has no site-packages and no package index.
cat > "$OUT_DIR/query.py" <<'PY_EOF'
import json, sys, urllib.parse, urllib.request
base, query = sys.argv[1], sys.argv[2]
url = base + "/api/v1/query?" + urllib.parse.urlencode({"query": query})
with urllib.request.urlopen(url, timeout=30) as resp:
    body = json.load(resp)
if body.get("status") != "success":
    sys.exit("prometheus query %r failed: %r" % (query, body))
result = body["data"]["result"]
print(json.dumps({"query": query, "type": body["data"]["resultType"],
                  "count": len(result),
                  "series": [{"metric": r["metric"],
                              "value": r["value"][1] if r.get("value") else None}
                             for r in result]}))
PY_EOF

cat > "$OUT_DIR/range.py" <<'PY_EOF'
import json, sys, urllib.parse, urllib.request
base, query, start, end, want = sys.argv[1:6]
url = base + "/api/v1/query_range?" + urllib.parse.urlencode(
    {"query": query, "start": start, "end": end, "step": "15"})
with urllib.request.urlopen(url, timeout=30) as resp:
    body = json.load(resp)
if body.get("status") != "success":
    sys.exit("range query failed: %r" % body)
results = body["data"]["result"]
if not results:
    sys.exit("no stored sample for %s in [%s, %s]" % (query, start, end))
points = results[0]["values"]
if not any(p[1] == want for p in points):
    sys.exit("stored sample does not contain %s: %r" % (want, points))
print(json.dumps({"series": len(results), "points": len(points), "value": want}))
PY_EOF

cat > "$OUT_DIR/write.py" <<'PY_EOF'
import struct, sys, urllib.request
# Remote write v1: the receiver only speaks snappy-compressed protobuf
# (WriteRequest), not JSON -- a JSON body is rejected with HTTP 400
# "s2: corrupt input" (A11, observed live on attempt h4loki-a9 and reproduced
# against the surviving cluster: JSON -> 400, the codecs below -> 204 and the
# sample queryable). Both codecs are hand-rolled with the standard library
# only: the lab image has no site-packages. Snappy literal tag, extended
# form: upper six bits are 59+nbytes and the extra bytes hold len-1
# little-endian.
def varint(n):
    out = bytearray()
    while True:
        b = n & 0x7F
        n >>= 7
        if n:
            out.append(b | 0x80)
        else:
            out.append(b)
            return bytes(out)

def snappy(raw):
    # Pure-literal snappy block: varint of the uncompressed length, then one
    # literal chunk per 65536 bytes. Decoders accept this without matches.
    out = bytearray(varint(len(raw)))
    i = 0
    while i < len(raw):
        chunk = raw[i:i + 65536]
        ln = len(chunk)
        if ln <= 60:
            out.append((ln - 1) << 2)
        else:
            nbytes = ((ln - 1).bit_length() + 7) // 8
            out.append((59 + nbytes) << 2)
            out.extend((ln - 1).to_bytes(nbytes, "little"))
        out.extend(chunk)
        i += ln
    return bytes(out)

def ld(num, data):
    return varint((num << 3) | 2) + varint(len(data)) + data

def f64(num, raw8):
    return varint((num << 3) | 1) + raw8

def vi(num, value):
    return varint((num << 3) | 0) + varint(value)

base, name, run_id, value, ts = sys.argv[1:6]
sample = f64(1, struct.pack("<d", float(value))) + vi(2, int(ts))
series = ld(1, ld(1, b"__name__") + ld(2, name.encode())) \
       + ld(1, ld(1, b"run_id") + ld(2, run_id.encode())) \
       + ld(2, sample)
body = snappy(ld(1, series))
req = urllib.request.Request(base + "/api/v1/write", data=body,
    headers={"Content-Type": "application/x-protobuf",
             "Content-Encoding": "snappy",
             "X-Prometheus-Remote-Write-Version": "0.1.0"})
with urllib.request.urlopen(req, timeout=30) as resp:
    if resp.status not in (200, 204):
        sys.exit("remote write returned %s" % resp.status)
print("wrote %s{%s}=%s at %s" % (name, run_id, value, ts))
PY_EOF

cat > "$OUT_DIR/scalar.py" <<'PY_EOF'
import json, sys
# Asserts the payload is exactly one scalar series and prints its value.
payload = json.loads(sys.argv[1])
if payload["count"] != 1 or not payload["series"]:
    sys.exit("expected a single scalar series, got count=%s" % payload["count"])
print(payload["series"][0]["value"])
PY_EOF

cat > "$OUT_DIR/nodes_up.py" <<'PY_EOF'
import json, sys
# Asserts there is exactly one node-exporter target per node and all report up.
payload = json.loads(sys.argv[1])
want = int(sys.argv[2])
series = payload["series"]
if len(series) != want:
    sys.exit("expected %d node-exporter series, got %d" % (want, len(series)))
for s in series:
    if s["value"] != "1":
        sys.exit("target %s is not up (value=%s)" % (s["metric"].get("instance"), s["value"]))
print("series=%d all=1" % len(series))
PY_EOF

cat > "$OUT_DIR/alertstate.py" <<'PY_EOF'
import json, sys
payload = json.loads(sys.argv[1])
print(payload["series"][0]["metric"].get("alertstate", "") if payload["series"] else "")
PY_EOF

cat > "$OUT_DIR/fingerprint.py" <<'PY_EOF'
import json, sys
# $1 is the stored request bodies as one JSON string (a JSON array of raw
# bodies, one per request), $2 is the alert status to look for. Prints the
# fingerprint of the first alert in that status. The content arrives as argv,
# not as a node path: this helper runs inside the client pod, which has no
# access to the node filesystem (A9, observed live on attempt h4loki-a7 --
# a node path made this die with FileNotFoundError and, under pipefail,
# killed the whole verify script silently).
bodies, want = json.loads(sys.argv[1]), sys.argv[2]
for raw in bodies:
    for alert in json.loads(raw).get("alerts", []):
        if alert.get("status") == want:
            print(alert.get("fingerprint", ""))
            sys.exit(0)
sys.exit("no alert with status=%s in %d stored request bodies" % (want, len(bodies)))
PY_EOF

cat > "$OUT_DIR/am_status.py" <<'PY_EOF'
import json, urllib.request, sys
with urllib.request.urlopen(sys.argv[1] + "/api/v2/status", timeout=10) as r:
    print(json.dumps(json.load(r)))
PY_EOF

cat > "$OUT_DIR/silence_create.py" <<'PY_EOF'
import json, sys, urllib.request
# $1 is the Alertmanager base URL, $2 the silence definition as one JSON
# string. Same argv rule as fingerprint.py: this helper runs inside the
# client pod, which has no access to the node filesystem (A9, observed live
# on attempt h4loki-a7).
base, payload = sys.argv[1], json.loads(sys.argv[2])
req = urllib.request.Request(base + "/api/v2/silences",
                             data=json.dumps(payload).encode(),
                             headers={"Content-Type": "application/json"})
with urllib.request.urlopen(req, timeout=30) as resp:
    print(json.load(resp).get("silenceID", ""))
PY_EOF

cat > "$OUT_DIR/silence_get.py" <<'PY_EOF'
import json, sys, urllib.request
base, sid = sys.argv[1], sys.argv[2]
with urllib.request.urlopen(base + "/api/v2/silence/" + sid, timeout=30) as resp:
    print(json.load(resp).get("id", ""))
PY_EOF

cat > "$OUT_DIR/silence_gc.py" <<'PY_EOF'
# Deletes every ACTIVE silence whose comment marks it as an ANI metrics
# persistence check (A24, observed live on kubeovn-full-a6: the install-time
# verify creates this silence with a 2h TTL and the cleanup block never
# removed it, so a post-install verify run inside that window had its test
# alert suppressed and [5/8] failed with an empty receiver). Scoped to our
# own comment prefix: an operator silence is never touched.
import json, sys, urllib.request
base = sys.argv[1]
with urllib.request.urlopen(base + "/api/v2/silences", timeout=30) as resp:
    silences = json.load(resp)
for s in silences:
    if s.get("status", {}).get("state") != "active":
        continue
    if not str(s.get("comment", "")).startswith("ANI metrics persistence check"):
        continue
    req = urllib.request.Request(base + "/api/v2/silence/" + s["id"], method="DELETE")
    with urllib.request.urlopen(req, timeout=30) as resp:
        resp.read()
    print("expired stale silence " + s["id"])
PY_EOF

cat > "$OUT_DIR/receiver_dump.py" <<'PY_EOF'
import json, os, sys
# Prints every request body the receiver stored under $1 as one JSON array.
d = sys.argv[1]
out = []
for name in sorted(os.listdir(d)) if os.path.isdir(d) else []:
    with open(os.path.join(d, name)) as fh:
        out.append(fh.read())
print(json.dumps(out))
PY_EOF

# A26 + F01: the deterministic run_id is install-scoped, so deleting by
# run_id="$RUN_LABEL" would clear OTHER attempts' objects — that is a mutation
# and belongs only to the acceptance durability run, never to smoke. Smoke uses
# attempt-unique object names and cleans up only its own probe.
if [ "$LEVEL" = acceptance ]; then
  "${KUBECTL[@]}" -n "$NS" delete prometheusrule,alertmanagerconfig -l run_id="$RUN_LABEL" --ignore-not-found >/dev/null
  "${KUBECTL[@]}" -n "$NS" delete deploy,svc,cm,pod -l run_id="$RUN_LABEL" --ignore-not-found >/dev/null
fi

# ---------------------------------------------------------------------------
# [1/8] workloads
# ---------------------------------------------------------------------------
echo "[1/8] workloads"
nodes_total="$("${KUBECTL[@]}" get nodes --no-headers | wc -l)"
[ "$nodes_total" -ge 3 ] || fail "expected at least 3 nodes, saw $nodes_total"
for obj in "$PROM_STS" "$AM_STS" \
           deployment/ani-metrics-operator \
           deployment/ani-metrics-kube-state-metrics; do
  k5_workload_ready "$obj" 300s || fail "$obj is not rolled out"
done
k5_workload_ready daemonset/ani-metrics-prometheus-node-exporter 300s \
  || fail "node-exporter DaemonSet is not rolled out"
ne_ready="$("${KUBECTL[@]}" -n "$NS" get daemonset ani-metrics-prometheus-node-exporter \
  -o jsonpath='{.status.numberReady}')"
[ "$ne_ready" = "$nodes_total" ] \
  || fail "node-exporter ready on $ne_ready of $nodes_total nodes"
for svc in ani-metrics-prometheus ani-metrics-alertmanager; do
  "${KUBECTL[@]}" -n "$NS" get "svc/$svc" >/dev/null || fail "Service $svc is missing"
done
echo "  nodes=$nodes_total node-exporter=$ne_ready services=present"

# One pod from the offline python image is the HTTP client for the whole run,
# started once and reused: no per-step image pull and no host tooling.
cat > "$OUT_DIR/client-pod.yaml" <<CLIENT_EOF
apiVersion: v1
kind: Pod
metadata:
  name: $CLIENT_POD
  namespace: $NS
  labels:
    run_id: "$RUN_LABEL"
spec:
  restartPolicy: Never
  containers:
    - name: client
      image: $PY_IMAGE
      imagePullPolicy: IfNotPresent
      command: ["sleep", "36000"]
CLIENT_EOF
"${KUBECTL[@]}" apply --server-side -f "$OUT_DIR/client-pod.yaml" >/dev/null
"${KUBECTL[@]}" -n "$NS" wait --for=condition=Ready "pod/$CLIENT_POD" --timeout=180s >/dev/null \
  || fail "HTTP client pod did not become ready"

# Feed a helper to the client pod on stdin; extra argv after the script name is
# forwarded to it.
py() {
  local script="$1"; shift
  "${KUBECTL[@]}" -n "$NS" exec -i "$CLIENT_POD" -- python3 - "$@" < "$script"
}
query_prom() { py "$OUT_DIR/query.py" "http://$PROM_SVC" "$1"; }

# ---------------------------------------------------------------------------
# [2/8] real series through the Prometheus HTTP API
# ---------------------------------------------------------------------------
echo "[2/8] real series through the Prometheus HTTP API"
# The job label is a chart runtime fact, not something the values render
# decides: kube-prometheus-stack 85.4.0 scrapes node-exporter as
# job="node-exporter" (verified live — the Prometheus targets API lists
# 3 up targets under that name, while "prometheus-node-exporter" matches
# nothing and the daemonset keeps the ani-metrics- prefixed object name).
#
# Discovery and the first scrape are asynchronous: the StatefulSet reports
# ready before kubernetes_sd has listed every node-exporter endpoint and
# before the first samples have landed in the TSDB, so every query in this
# section is polled with a bounded wait instead of asserted on the first
# answer (failure A5: the first poll saw 1 of 3 node-exporter targets).
up_ok=""
up_last=""
for _ in $(seq 1 60); do
  if up_json="$(query_prom 'up{job="node-exporter"}')" \
     && py "$OUT_DIR/nodes_up.py" "$up_json" "$nodes_total" > "$OUT_DIR/up.out" 2>&1 \
     && grep -q '^series=' "$OUT_DIR/up.out"; then
    up_ok=yes
    break
  fi
  up_last="$(py "$OUT_DIR/nodes_up.py" "$up_json" "$nodes_total" 2>&1 | tail -1 || true)"
  sleep 5
done
[ -n "$up_ok" ] || fail "node-exporter targets did not all report up within the 5m wait (last poll: ${up_last:-no answer})"
echo "  $(cat "$OUT_DIR/up.out")"

# One hostname series per node and one kube_node_info per node: these prove
# node-exporter reads the machine and kube-state-metrics reads the API, rather
# than each merely answering a health endpoint. Same bounded wait: KSM and the
# node-exporter pods report ready before their first samples are stored.
uname_ok=""
for _ in $(seq 1 60); do
  got_uname="$(py "$OUT_DIR/scalar.py" "$(query_prom 'count(node_uname_info)')" 2>/dev/null || true)"
  [ "$got_uname" = "$nodes_total" ] && { uname_ok=yes; break; }
  sleep 5
done
[ -n "$uname_ok" ] || fail "node_uname_info did not cover all $nodes_total nodes within the 5m wait (last: ${got_uname:-no answer})"
knodes_ok=""
for _ in $(seq 1 60); do
  got_knodes="$(py "$OUT_DIR/scalar.py" "$(query_prom 'count(kube_node_info)')" 2>/dev/null || true)"
  [ "$got_knodes" = "$nodes_total" ] && { knodes_ok=yes; break; }
  sleep 5
done
[ -n "$knodes_ok" ] || fail "kube_node_info did not cover all $nodes_total nodes within the 5m wait (last: ${got_knodes:-no answer})"
echo "  node_uname_info=$got_uname kube_node_info=$got_knodes"

# A real cAdvisor container metric, counted through the API. A positive count
# means container series are being scraped, which is the claim; a bare
# `up{job="kubelet"}` would not be.
cadvisor_ok=""
for _ in $(seq 1 60); do
  cadvisor="$(py "$OUT_DIR/scalar.py" \
    "$(query_prom 'count(container_memory_working_set_bytes{container!="",container!="POD"})')" 2>/dev/null || true)"
  case "${cadvisor%%.*}" in
    ""|*[!0-9]*) : ;;
    *) [ "${cadvisor%%.*}" -gt 0 ] && { cadvisor_ok=yes; break; } ;;
  esac
  sleep 5
done
[ -n "$cadvisor_ok" ] || fail "no cAdvisor container series appeared within the 5m wait (last: ${cadvisor:-no answer})"
echo "  container_memory_working_set_bytes series=$cadvisor"

# F01: smoke ends here. It performed only readiness ([1/8]) and read-only
# functional series queries ([2/8]) through this attempt's uniquely-named client
# pod. It must not touch alert routing or recreate pods, so it cleans up its own
# probe and exits before the acceptance chain below.
if [ "$LEVEL" = smoke ]; then
  "${KUBECTL[@]}" -n "$NS" delete pod "$CLIENT_POD" --ignore-not-found >/dev/null 2>&1 || true
  echo "metrics SMOKE verification passed: ns=$NS nodes=$nodes_total up=all cAdvisor=$cadvisor (read-only; no alert mutation, no pod rebuild)"
  exit 0
fi

# ---------------------------------------------------------------------------
# [3/8] the temporary receiver   (acceptance only)
# ---------------------------------------------------------------------------
echo "[3/8] temporary webhook receiver"
# The receiver stores every request body in an emptyDir, one file per request,
# so the assertions read what Alertmanager actually sent. It carries this run's
# label so Alertmanager's config selector admits it, and its configmap is
# mounted read-only.
cat > "$OUT_DIR/receiver.yaml" <<RECV_EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: $RECV_NAME
  namespace: $NS
  labels:
    run_id: "$RUN_LABEL"
data:
  server.py: |
    import http.server, os, socketserver

    OUT = "/data"
    os.makedirs(OUT, exist_ok=True)
    count = [0]

    class Handler(http.server.BaseHTTPRequestHandler):
        def do_POST(self):
            length = int(self.headers.get("Content-Length") or 0)
            body = self.rfile.read(length)
            count[0] += 1
            with open(os.path.join(OUT, "req-%03d.json" % count[0]), "wb") as fh:
                fh.write(body)
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b"{}")

        def log_message(self, *args):
            pass

    socketserver.TCPServer.allow_reuse_address = True
    with socketserver.TCPServer(("0.0.0.0", 8080), Handler) as httpd:
        httpd.serve_forever()
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: $RECV_DEPLOY
  namespace: $NS
  labels:
    run_id: "$RUN_LABEL"
spec:
  replicas: 1
  selector:
    matchLabels:
      app: $RECV_NAME
  template:
    metadata:
      labels:
        app: $RECV_NAME
        run_id: "$RUN_LABEL"
    spec:
      containers:
        - name: receiver
          image: $PY_IMAGE
          imagePullPolicy: IfNotPresent
          command: ["python3", "/app/server.py"]
          ports:
            - name: http
              containerPort: 8080
          volumeMounts:
            - name: script
              mountPath: /app
              readOnly: true
            - name: data
              mountPath: /data
      volumes:
        - name: script
          configMap:
            name: $RECV_NAME
        - name: data
          emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: $RECV_NAME
  namespace: $NS
  labels:
    run_id: "$RUN_LABEL"
spec:
  selector:
    app: $RECV_NAME
  ports:
    - name: http
      port: 8080
      targetPort: 8080
RECV_EOF

"${KUBECTL[@]}" apply --server-side -f "$OUT_DIR/receiver.yaml" >/dev/null
k5_workload_ready "deployment/$RECV_DEPLOY" 300s \
  || fail "temporary receiver did not become ready"
recv_pod="$("${KUBECTL[@]}" -n "$NS" get pod -l "app=$RECV_NAME" \
  -o jsonpath='{.items[0].metadata.name}')"
[ -n "$recv_pod" ] || fail "temporary receiver pod not found"
echo "  receiver pod=$recv_pod"

# Pull every stored request body out of the receiver pod and write it to $1.
# The receiver's interpreter is python3, so the fixed program is fed on stdin
# to `python3 -` as in every other helper here.
dump_requests() {
  "${KUBECTL[@]}" -n "$NS" exec -i "$recv_pod" -- python3 - /data \
    < "$OUT_DIR/receiver_dump.py" > "$1"
}
# True once a stored request body contains $1.
receiver_has() {
  "${KUBECTL[@]}" -n "$NS" exec "$recv_pod" -- sh -c \
    "grep -l -s -F '$1' /data/*.json >/dev/null 2>&1"
}

# ---------------------------------------------------------------------------
# [4/8] Alertmanager accepts this run's config only
# ---------------------------------------------------------------------------
echo "[4/8] AlertmanagerConfig for this run"
# sendResolved is what makes step [6/8] possible at all, so it is asserted as
# part of the loaded configuration here rather than assumed.
cat > "$OUT_DIR/amcfg.yaml" <<AMCFG_EOF
apiVersion: monitoring.coreos.com/v1alpha1
kind: AlertmanagerConfig
metadata:
  name: $AMCFG_NAME
  namespace: $NS
  labels:
    run_id: "$RUN_LABEL"
spec:
  route:
    # The receiver name must carry this run's RUN_ID suffix: Alertmanager only
    # reports the *loaded* receiver name in /api/v2/status, and [4/8] waits on
    # $RECV_NAME there. A hardcoded name would never match and fake a timeout
    # (A6, observed live on attempt h4loki-a4).
    receiver: $RECV_NAME
    groupBy: ["alertname"]
    groupWait: 5s
    groupInterval: 5s
    repeatInterval: 5m
    matchers:
      - name: run_id
        value: "$RUN_LABEL"
        matchType: "="
  receivers:
    - name: $RECV_NAME
      webhookConfigs:
        - url: http://$RECV_NAME.$NS.svc.cluster.local:8080/
          sendResolved: true
AMCFG_EOF

"${KUBECTL[@]}" apply --server-side -f "$OUT_DIR/amcfg.yaml" >/dev/null
# Wait for the Operator to fold the config into the running Alertmanager rather
# than sleeping and hoping. The receiver name is the marker: it only appears in
# the status once the Operator has loaded the config.
amcfg_ok=""
for _ in $(seq 1 60); do
  cfg="$(py "$OUT_DIR/am_status.py" "http://$AM_SVC" 2>/dev/null || true)"
  if printf '%s' "$cfg" | grep -q "$RECV_NAME"; then amcfg_ok=yes; break; fi
  sleep 5
done
printf '%s' "$cfg" > "$OUT_DIR/am-status.json"
[ -n "$amcfg_ok" ] || fail "Alertmanager never loaded the route from $AMCFG_NAME"

# A24: the install-time verify run leaves its persistence-check silence behind
# (2h TTL, cleanup never removed it) with the same deterministic run_id. Any
# verify run inside that window had its test alert suppressed and [5/8] died
# on an empty receiver. Expire our own leftovers before firing this run's
# alert; operator silences are never touched.
py "$OUT_DIR/silence_gc.py" "http://$AM_SVC" || true

# ---------------------------------------------------------------------------
# [5/8] a real firing transition
# ---------------------------------------------------------------------------
echo "[5/8] PrometheusRule vector(1) == 1 -> firing -> webhook"
# Unique labels on every object, and no `bool` modifier in the expression: the
# rule must return a sample to fire and an empty vector to resolve.
cat > "$OUT_DIR/rule-firing.yaml" <<RULE_EOF
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: $RULE_NAME
  namespace: $NS
  labels:
    release: ani-metrics
    run_id: "$RUN_LABEL"
spec:
  groups:
    - name: ani-metrics-test
      interval: 5s
      rules:
        - alert: AniMetricsLifecycleTest
          expr: vector(1) == 1
          for: 0s
          labels:
            run_id: "$RUN_LABEL"
            severity: test
            # The Operator folds every AlertmanagerConfig into a sub-route that
            # also matches namespace="<config namespace>" (namespace isolation),
            # so the alert must carry the namespace label: without it AM drops
            # the alert on the root "null" receiver and no webhook is ever sent
            # (A8, observed live on attempt h4loki-a6).
            namespace: $NS
          annotations:
            summary: "ANI metrics lifecycle test"
RULE_EOF

"${KUBECTL[@]}" apply --server-side -f "$OUT_DIR/rule-firing.yaml" >/dev/null

# Both halves of the claim are checked: Prometheus reports the alert, and the
# receiver got the notification. Either alone is not "firing".
alert_sel="ALERTS{alertname=\"AniMetricsLifecycleTest\",run_id=\"$RUN_LABEL\"}"
fired=""
# A25: operator+reloader reload latency on the a6 cluster exceeded 7.5min; widened to 25min (A26) after a 19min reload lag was observed.
for _ in $(seq 1 300); do
  state="$(py "$OUT_DIR/alertstate.py" "$(query_prom "$alert_sel")" 2>/dev/null || true)"
  if [ "$state" = "firing" ]; then fired=yes; break; fi
  sleep 5
done
[ -n "$fired" ] || fail "Prometheus never reported AniMetricsLifecycleTest as firing"
echo "  prometheus ALERTS alertstate=firing"

got_firing=""
# A25: operator+reloader reload latency on the a6 cluster exceeded 7.5min; widened to 25min (A26) after a 19min reload lag was observed.
for _ in $(seq 1 300); do
  if receiver_has '"status":"firing"' || receiver_has '"status": "firing"'; then got_firing=yes; break; fi
  sleep 5
done
dump_requests "$OUT_DIR/reqs-firing.json"
[ -n "$got_firing" ] || fail "receiver never got a notification with status=firing"

# The notification must be this run's alert, carrying this run's label. A body
# that merely mentions the alert name would not attribute it to this run.
grep -q 'AniMetricsLifecycleTest' "$OUT_DIR/reqs-firing.json" \
  || fail "firing notification does not name this run's alert"
grep -q "$RUN_LABEL" "$OUT_DIR/reqs-firing.json" \
  || fail "firing notification does not carry this run's unique label"

# The dump content is passed as argv, not as a node path: py() executes inside
# the client pod, which cannot read node filesystem paths (A9, observed live
# on attempt h4loki-a7). stderr stays visible and the assignment carries an
# explicit failure so a broken extraction cannot kill this script silently
# under pipefail again.
firing_fp="$(py "$OUT_DIR/fingerprint.py" "$(cat "$OUT_DIR/reqs-firing.json")" firing | tail -1)" \
  || fail "could not extract the fingerprint from the firing notification"
[ -n "$firing_fp" ] || fail "firing notification carries no fingerprint"
echo "  firing notification received: fingerprint=$firing_fp"

# ---------------------------------------------------------------------------
# [6/8] the resolved transition, same fingerprint
# ---------------------------------------------------------------------------
echo "[6/8] PrometheusRule vector(0) == 1 -> resolved -> webhook"
sed 's/expr: vector(1) == 1/expr: vector(0) == 1/' \
  "$OUT_DIR/rule-firing.yaml" > "$OUT_DIR/rule-resolved.yaml"
grep -q 'vector(0) == 1' "$OUT_DIR/rule-resolved.yaml" \
  || fail "could not build the resolved rule"
"${KUBECTL[@]}" apply --server-side -f "$OUT_DIR/rule-resolved.yaml" >/dev/null

# Prometheus must actually drop the series. With no `bool` modifier the rule
# evaluates to an empty vector, which is the only thing that resolves an alert;
# a rule that merely stopped being evaluated would leave the alert firing.
gone=""
# A25: operator+reloader reload latency on the a6 cluster exceeded 7.5min; widened to 25min (A26) after a 19min reload lag was observed.
for _ in $(seq 1 300); do
  # count() over an empty selector returns an EMPTY vector, not a zero sample:
  # once the alert resolves, scalar.py (which asserts exactly one series) can
  # only fail, so n could never become "0" and this pass condition was
  # unreachable by design (A10, observed live on attempt h4loki-a8 -- the
  # alert really resolved and the receiver even recorded the resolved webhook
  # while this poll kept waiting for a zero that count() never emits).
  # `or vector(0)` returns exactly one sample in both states: 1 while firing,
  # 0 once the series is gone.
  n="$(py "$OUT_DIR/scalar.py" "$(query_prom "count($alert_sel) or vector(0)")" 2>/dev/null || echo '?')"
  if [ "$n" = "0" ]; then gone=yes; break; fi
  sleep 5
done
[ -n "$gone" ] || fail "Prometheus still reports the alert after the expression became an empty vector"
echo "  prometheus ALERTS series gone after expr change"

got_resolved=""
# A25: operator+reloader reload latency on the a6 cluster exceeded 7.5min; widened to 25min (A26) after a 19min reload lag was observed.
for _ in $(seq 1 300); do
  if receiver_has '"status":"resolved"' || receiver_has '"status": "resolved"'; then got_resolved=yes; break; fi
  sleep 5
done
dump_requests "$OUT_DIR/reqs-resolved.json"
[ -n "$got_resolved" ] || fail "receiver never got resolved although sendResolved: true is set"

resolved_fp="$(py "$OUT_DIR/fingerprint.py" "$(cat "$OUT_DIR/reqs-resolved.json")" resolved | tail -1)" \
  || fail "could not extract the fingerprint from the resolved notification"
[ "$resolved_fp" = "$firing_fp" ] \
  || fail "resolved fingerprint $resolved_fp does not match firing fingerprint $firing_fp"
echo "  resolved notification received: fingerprint=$resolved_fp (matches firing)"

# ---------------------------------------------------------------------------
# [7/8] Prometheus durability across a normal pod rebuild
# ---------------------------------------------------------------------------
echo "[7/8] Prometheus PVC/StatefulSet survive a normal pod rebuild"
prom_pvc="prometheus-ani-metrics-prometheus-db-prometheus-ani-metrics-prometheus-0"
prom_pvc_before="$("${KUBECTL[@]}" -n "$NS" get "pvc/$prom_pvc" -o jsonpath='{.metadata.uid}')"
prom_sts_before="$("${KUBECTL[@]}" -n "$NS" get "$PROM_STS" -o jsonpath='{.metadata.uid}')"
[ -n "$prom_pvc_before" ] && [ -n "$prom_sts_before" ] \
  || fail "could not read the Prometheus PVC/StatefulSet identity"

# A unique sample written through the remote-write receiver: it provably came
# from this run, unlike a scrape target that may already have history.
marker_value="$(( RANDOM * 32768 + RANDOM ))"
marker_ts="$(( $(date +%s) * 1000 ))"
py "$OUT_DIR/write.py" "http://$PROM_SVC" ani_metrics_rebuild_marker "$RUN_LABEL" \
  "$marker_value" "$marker_ts" >/dev/null || fail "could not write the rebuild marker"

marker_sel="ani_metrics_rebuild_marker{run_id=\"$RUN_LABEL\"}"
seen_before=""
for _ in $(seq 1 60); do
  if py "$OUT_DIR/query.py" "http://$PROM_SVC" "$marker_sel" 2>/dev/null | grep -q "$marker_value"; then
    seen_before=yes; break
  fi
  sleep 2
done
[ -n "$seen_before" ] || fail "the marker sample is not queryable before the rebuild"

# A normal rebuild: delete the pod and let the StatefulSet recreate it. No
# snapshot, no PVC deletion, no namespace surgery. The wait below may perform
# ONE further planned rebuild of this component's own pod and then gives up
# with read-only evidence (see k5_rebuild_wait); it never restarts the kcn
# agents or repairs the datapath, so a real flake fails this check visibly.
"${KUBECTL[@]}" -n "$NS" delete pod -l app.kubernetes.io/name=prometheus --timeout=180s >/dev/null
k5_rebuild_wait "app.kubernetes.io/name=prometheus" "prometheus" 600s 420s \
  || fail "Prometheus did not come back after the rebuild (no further rebuild or agent restart is attempted)"

prom_pvc_after="$("${KUBECTL[@]}" -n "$NS" get "pvc/$prom_pvc" -o jsonpath='{.metadata.uid}')"
prom_sts_after="$("${KUBECTL[@]}" -n "$NS" get "$PROM_STS" -o jsonpath='{.metadata.uid}')"
[ "$prom_pvc_after" = "$prom_pvc_before" ] || fail "Prometheus PVC was replaced"
[ "$prom_sts_after" = "$prom_sts_before" ] || fail "Prometheus StatefulSet was replaced"

# Range query across the window holding the pre-rebuild sample. An instant
# query after the rebuild could be answered by a fresh scrape, so the assertion
# is on the stored sample. a26 (evidence h4loki-a26) is the post-Ready blind
# window: the rebuilt pod can pass Ready and THEN have its netns die. R02
# removed the re-entry that used to rebuild the pod again here — one failing
# read is now the check's answer, followed by read-only evidence.
range_out=""
range_out="$(py "$OUT_DIR/range.py" "http://$PROM_SVC" "$marker_sel" \
    "$(( marker_ts / 1000 - 60 ))" "$(( marker_ts / 1000 + 180 ))" "$marker_value")" \
  || { k5_collect_failure_evidence "app.kubernetes.io/name=prometheus" "prometheus" || true
       fail "the pre-rebuild sample is not readable after the rebuild"; }
printf '%s' "$range_out" > "$OUT_DIR/rebuild-range.json"
echo "  pre-rebuild sample read back by range query: $range_out"

# ---------------------------------------------------------------------------
# [8/8] Alertmanager silence durability across the same kind of rebuild
# ---------------------------------------------------------------------------
echo "[8/8] Alertmanager silence and PVC survive a normal pod rebuild"
am_pvc="alertmanager-ani-metrics-alertmanager-db-alertmanager-ani-metrics-alertmanager-0"
am_secret=alertmanager-ani-metrics-alertmanager-generated
am_pvc_before="$("${KUBECTL[@]}" -n "$NS" get "pvc/$am_pvc" -o jsonpath='{.metadata.uid}')"
am_sts_before="$("${KUBECTL[@]}" -n "$NS" get "$AM_STS" -o jsonpath='{.metadata.uid}')"
am_secret_before="$("${KUBECTL[@]}" -n "$NS" get "secret/$am_secret" -o jsonpath='{.metadata.uid}')"
[ -n "$am_pvc_before" ] && [ -n "$am_sts_before" ] && [ -n "$am_secret_before" ] \
  || fail "could not read the Alertmanager PVC/StatefulSet/Secret identity"

# A unique silence whose matcher value is this run's label, so it can only ever
# match this run's test alert and nothing else in the cluster.
cat > "$OUT_DIR/silence.json" <<SILENCE_EOF
{
  "matchers": [
    {"name": "alertname", "value": "AniMetricsLifecycleTest", "isRegex": false},
    {"name": "run_id", "value": "$RUN_LABEL", "isRegex": false}
  ],
  "startsAt": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "endsAt": "$(date -u -d '+2 hours' +%Y-%m-%dT%H:%M:%SZ)",
  "createdBy": "ani-installer-$RUN_ID",
  "comment": "ANI metrics persistence check $RUN_ID"
}
SILENCE_EOF

# Same argv rule as the fingerprint extraction: the silence definition goes in
# as content, not as a node path (A9, observed live on attempt h4loki-a7).
silence_id="$(py "$OUT_DIR/silence_create.py" "http://$AM_SVC" "$(cat "$OUT_DIR/silence.json")")" \
  || fail "could not create the Alertmanager silence"
[ -n "$silence_id" ] || fail "Alertmanager did not return a silence ID"
echo "  silence created: $silence_id"

# Normal rebuild of Alertmanager only, with the same single planned rebuild on
# failure as [7/8] (no agent restart, no repeated rebuild).
"${KUBECTL[@]}" -n "$NS" delete pod -l app.kubernetes.io/name=alertmanager --timeout=180s >/dev/null
k5_rebuild_wait "app.kubernetes.io/name=alertmanager" "alertmanager" 600s 420s \
  || fail "Alertmanager did not come back after the rebuild (no further rebuild or agent restart is attempted)"

am_pvc_after="$("${KUBECTL[@]}" -n "$NS" get "pvc/$am_pvc" -o jsonpath='{.metadata.uid}')"
am_sts_after="$("${KUBECTL[@]}" -n "$NS" get "$AM_STS" -o jsonpath='{.metadata.uid}')"
am_secret_after="$("${KUBECTL[@]}" -n "$NS" get "secret/$am_secret" -o jsonpath='{.metadata.uid}')"
[ "$am_pvc_after" = "$am_pvc_before" ] || fail "Alertmanager PVC was replaced"
[ "$am_sts_after" = "$am_sts_before" ] || fail "Alertmanager StatefulSet was replaced"
[ "$am_secret_after" = "$am_secret_before" ] || fail "Alertmanager generated Secret was replaced"

am_silence_poll() { # am_silence_poll — poll the silence read for up to 120s; sets `got` and returns 0 when read back
  local i
  for i in $(seq 1 60); do
    got="$(py "$OUT_DIR/silence_get.py" "http://$AM_SVC" "$silence_id" 2>/dev/null || true)"
    if [ "$got" = "$silence_id" ]; then return 0; fi
    sleep 2
  done
  return 1
}

silence_back=""
if am_silence_poll; then silence_back=yes; fi
# Same post-Ready blind window as [7/8] (a26): R02 removed the re-entry that
# used to rebuild the pod again here. One failed poll is the answer; the
# read-only evidence below records the state and the run fails.
if [ -z "$silence_back" ]; then
  k5_collect_failure_evidence "app.kubernetes.io/name=alertmanager" "alertmanager" || true
  fail "silence $silence_id could not be read back after the rebuild"
fi
[ -n "$silence_back" ] || fail "silence $silence_id is gone after the rebuild"
echo "  silence read back by ID after rebuild: $got"

# ---------------------------------------------------------------------------
# cleanup
# ---------------------------------------------------------------------------
echo "[cleanup] removing this run's temporary objects"
# A24: this run's persistence-check silence must not outlive the run — it
# matches the deterministic run_id and would suppress a later verify run's
# identical test alert for its full 2h TTL.
py "$OUT_DIR/silence_gc.py" "http://$AM_SVC" || true
"${KUBECTL[@]}" -n "$NS" delete prometheusrule "$RULE_NAME" --ignore-not-found >/dev/null
"${KUBECTL[@]}" -n "$NS" delete alertmanagerconfig "$AMCFG_NAME" --ignore-not-found >/dev/null
"${KUBECTL[@]}" -n "$NS" delete deployment "$RECV_DEPLOY" --ignore-not-found >/dev/null
"${KUBECTL[@]}" -n "$NS" delete service "$RECV_NAME" --ignore-not-found >/dev/null
"${KUBECTL[@]}" -n "$NS" delete configmap "$RECV_NAME" --ignore-not-found >/dev/null
"${KUBECTL[@]}" -n "$NS" delete pod "$CLIENT_POD" --ignore-not-found >/dev/null

echo "metrics verification passed: ns=$NS nodes=$nodes_total up=all cAdvisor=$cadvisor firing=$firing_fp resolved=$resolved_fp prom-pvc=stable am-pvc=stable silence=$silence_id prom=$PROM_SVC am=$AM_SVC evidence=$OUT_DIR"

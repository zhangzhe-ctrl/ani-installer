#!/usr/bin/env bash
# Metrics smoke checker: workloads and real Prometheus series only.
# The formal METRICS-01..11 acceptance runs in kk ani verify.
set -euo pipefail

# C07: one kubeconfig decides the target of this whole checker, and it is
# required rather than defaulted. Falling back to /etc/kubernetes/admin.conf here
# meant the layer that rendered this script could pin one cluster while kubectl
# silently used another (or whatever $HOME/.kube/config holds).
KUBECONFIG_FILE="${ANI_VERIFY_KUBECONFIG:?name the kubeconfig this verification runs against; ANI_VERIFY_KUBECONFIG has no default}"
if [ ! -f "$KUBECONFIG_FILE" ]; then
  echo "kubeconfig $KUBECONFIG_FILE does not exist; refusing to guess another target" >&2
  exit 1
fi
if [ -n "${KUBECONFIG:-}" ] && [ "$KUBECONFIG" != "$KUBECONFIG_FILE" ]; then
  echo "ambiguous target: ANI_VERIFY_KUBECONFIG=$KUBECONFIG_FILE but the environment carries KUBECONFIG=$KUBECONFIG" >&2
  exit 1
fi
# Exporting it is what binds every bare `kubectl` below to the same context, so
# no call can drift to a per-layer default.
export KUBECONFIG="$KUBECONFIG_FILE"
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_FILE")
NS="{{ .ani.components.metrics.namespace }}"
PROM_SVC="ani-metrics-prometheus.$NS.svc.cluster.local:9090"
PY_IMAGE="{{ index .ani.images "docker.io/library/python:3.13.11-alpine3.23" }}"
RUN_ID="$(date +%Y%m%d%H%M%S)-$$"
OUT_DIR="${ANI_VERIFY_OUTPUT_DIR:-/tmp/ani-metrics-verify-${RUN_ID}}"
mkdir -p "$OUT_DIR"
fail() { echo "FAIL: $*" >&2; exit 1; }

# Readiness uses pod conditions rather than `rollout status`, which previously
# returned success against stale StatefulSet status. The read-only K5 helpers
# below remain for the R02 regression seam; this smoke entry never rebuilds.
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
CLIENT_POD="ani-metrics-client-${RUN_ID}"
RUN_LABEL="{{ .ani.components.metrics.run_id }}"

# The install role and `kk ani verify --level smoke` use this script for
# workload readiness and real series queries. Acceptance is a Go plan behind
# the shared product lock and target ledgers.
LEVEL="${ANI_VERIFY_LEVEL:-smoke}"
case "$LEVEL" in
  smoke|acceptance) : ;;
  *) echo "FAIL: ANI_VERIFY_LEVEL must be 'smoke' or 'acceptance', got '$LEVEL'" >&2; exit 1 ;;
esac

# The historical acceptance script is retired. It must not create a receiver,
# route, rule or silence, and must not delete a business Pod. The production
# acceptance plan owns METRICS-01..11 and its one-change ledger.
if [ "$LEVEL" = acceptance ]; then
  echo "metrics acceptance is only available through: kk ani verify --level acceptance --only metrics --allow-pod-recreate (with the run record and kubeconfig)" >&2
  exit 2
fi

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
ANI_KK_BIN="${ANI_KK_BIN:?the smoke checker needs its own kk binary for UID-bound probe cleanup}"
[ -x "$ANI_KK_BIN" ] || fail "kk binary $ANI_KK_BIN is not executable"
client_uid="$("${KUBECTL[@]}" create -f "$OUT_DIR/client-pod.yaml" -o jsonpath='{.metadata.uid}')" \
  || fail "could not create the smoke client pod"
[ -n "$client_uid" ] || fail "client pod creation returned no UID; preserving evidence"
printf '%s\n' "$client_uid" > "$OUT_DIR/client-pod.uid"
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
# probe and exits; the acceptance chain lives in the production RunVerify plan.
if [ "$LEVEL" = smoke ]; then
  release_out="$("$ANI_KK_BIN" ani pod-release --kubeconfig "$KUBECONFIG_FILE" --namespace "$NS" --pod "$CLIENT_POD" --uid "$client_uid")" \
    || fail "UID-bound smoke client cleanup failed; pod identity remains in $OUT_DIR/client-pod.uid"
  case "$release_out" in
    "deleted $NS $CLIENT_POD $client_uid"|"not_found $NS $CLIENT_POD $client_uid") : ;;
    *) fail "unexpected pod-release answer for the smoke client: $release_out" ;;
  esac
  echo "metrics SMOKE verification passed: ns=$NS nodes=$nodes_total up=all cAdvisor=$cadvisor (read-only; no alert mutation, no pod rebuild)"
  exit 0
fi

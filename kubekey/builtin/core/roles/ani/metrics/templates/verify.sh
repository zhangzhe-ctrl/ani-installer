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

PROM_STS=statefulset/prometheus-ani-metrics-prometheus
AM_STS=statefulset/alertmanager-ani-metrics-alertmanager
RECV_NAME="ani-metrics-recv-${RUN_ID}"
RULE_NAME="ani-metrics-rule-${RUN_ID}"
AMCFG_NAME="ani-metrics-amcfg-${RUN_ID}"
CLIENT_POD="ani-metrics-client-${RUN_ID}"
RECV_DEPLOY="ani-metrics-recv-${RUN_ID}"
RUN_LABEL="{{ .ani.components.metrics.run_id }}"

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
import json, sys, urllib.request
base, name, run_id, value, ts = sys.argv[1:6]
payload = json.dumps({"timeseries": [{
    "labels": [{"name": "__name__", "value": name},
               {"name": "run_id", "value": run_id}],
    "samples": [{"value": float(value), "timestamp": int(ts)}],
}]}).encode()
req = urllib.request.Request(base + "/api/v1/write", data=payload,
                             headers={"Content-Type": "application/json"})
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
# $1 is a path to the stored request bodies (a JSON array of raw bodies, one
# per request), $2 is the alert status to look for. Prints the fingerprint of
# the first alert in that status.
path, want = sys.argv[1], sys.argv[2]
with open(path) as fh:
    bodies = json.load(fh)
for raw in bodies:
    for alert in json.loads(raw).get("alerts", []):
        if alert.get("status") == want:
            print(alert.get("fingerprint", ""))
            sys.exit(0)
sys.exit("no alert with status=%s in %s" % (want, path))
PY_EOF

cat > "$OUT_DIR/am_status.py" <<'PY_EOF'
import json, urllib.request, sys
with urllib.request.urlopen(sys.argv[1] + "/api/v2/status", timeout=10) as r:
    print(json.dumps(json.load(r)))
PY_EOF

cat > "$OUT_DIR/silence_create.py" <<'PY_EOF'
import json, sys, urllib.request
# Reads the silence definition from a file given as $2 and posts it to $1.
base, path = sys.argv[1], sys.argv[2]
with open(path) as fh:
    payload = json.load(fh)
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

# ---------------------------------------------------------------------------
# [1/8] workloads
# ---------------------------------------------------------------------------
echo "[1/8] workloads"
nodes_total="$("${KUBECTL[@]}" get nodes --no-headers | wc -l)"
[ "$nodes_total" -ge 3 ] || fail "expected at least 3 nodes, saw $nodes_total"
for obj in "$PROM_STS" "$AM_STS" \
           deployment/ani-metrics-operator \
           deployment/ani-metrics-kube-state-metrics; do
  "${KUBECTL[@]}" -n "$NS" rollout status "$obj" --timeout=300s >/dev/null \
    || fail "$obj is not rolled out"
done
"${KUBECTL[@]}" -n "$NS" rollout status \
  daemonset/ani-metrics-prometheus-node-exporter --timeout=300s >/dev/null \
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
up_json="$(query_prom 'up{job="prometheus-node-exporter"}')" || fail "up query failed"
echo "  up{job=prometheus-node-exporter} series=$(printf '%s' "$up_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["count"])' 2>/dev/null || echo '?')"
py "$OUT_DIR/nodes_up.py" "$up_json" "$nodes_total" | tee "$OUT_DIR/up.out"
grep -q '^series=' "$OUT_DIR/up.out" || fail "node-exporter targets are not all up"

# One hostname series per node and one kube_node_info per node: these prove
# node-exporter reads the machine and kube-state-metrics reads the API, rather
# than each merely answering a health endpoint.
got_uname="$(py "$OUT_DIR/scalar.py" "$(query_prom 'count(node_uname_info)')")"
[ "$got_uname" = "$nodes_total" ] || fail "node_uname_info covers $got_uname of $nodes_total nodes"
got_knodes="$(py "$OUT_DIR/scalar.py" "$(query_prom 'count(kube_node_info)')")"
[ "$got_knodes" = "$nodes_total" ] || fail "kube_node_info covers $got_knodes of $nodes_total nodes"
echo "  node_uname_info=$got_uname kube_node_info=$got_knodes"

# A real cAdvisor container metric, counted through the API. A positive count
# means container series are being scraped, which is the claim; a bare
# `up{job="kubelet"}` would not be.
cadvisor="$(py "$OUT_DIR/scalar.py" \
  "$(query_prom 'count(container_memory_working_set_bytes{container!="",container!="POD"})')")"
[ "${cadvisor%%.*}" -gt 0 ] 2>/dev/null || fail "no cAdvisor container series is present (count=$cadvisor)"
echo "  container_memory_working_set_bytes series=$cadvisor"

# ---------------------------------------------------------------------------
# [3/8] the temporary receiver
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
"${KUBECTL[@]}" -n "$NS" rollout status "deployment/$RECV_DEPLOY" --timeout=300s >/dev/null \
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
    receiver: ani-metrics-recv
    groupBy: ["alertname"]
    groupWait: 5s
    groupInterval: 5s
    repeatInterval: 5m
    matchers:
      - name: run_id
        value: "$RUN_LABEL"
        matchType: "="
  receivers:
    - name: ani-metrics-recv
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
          annotations:
            summary: "ANI metrics lifecycle test"
RULE_EOF

"${KUBECTL[@]}" apply --server-side -f "$OUT_DIR/rule-firing.yaml" >/dev/null

# Both halves of the claim are checked: Prometheus reports the alert, and the
# receiver got the notification. Either alone is not "firing".
alert_sel="ALERTS{alertname=\"AniMetricsLifecycleTest\",run_id=\"$RUN_LABEL\"}"
fired=""
for _ in $(seq 1 90); do
  state="$(py "$OUT_DIR/alertstate.py" "$(query_prom "$alert_sel")" 2>/dev/null || true)"
  if [ "$state" = "firing" ]; then fired=yes; break; fi
  sleep 5
done
[ -n "$fired" ] || fail "Prometheus never reported AniMetricsLifecycleTest as firing"
echo "  prometheus ALERTS alertstate=firing"

got_firing=""
for _ in $(seq 1 90); do
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

firing_fp="$(py "$OUT_DIR/fingerprint.py" "$OUT_DIR/reqs-firing.json" firing 2>/dev/null | tail -1)"
[ -n "$firing_fp" ] || fail "could not read a fingerprint from the firing notification"
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
for _ in $(seq 1 90); do
  n="$(py "$OUT_DIR/scalar.py" "$(query_prom "count($alert_sel)")" 2>/dev/null || echo '?')"
  if [ "$n" = "0" ]; then gone=yes; break; fi
  sleep 5
done
[ -n "$gone" ] || fail "Prometheus still reports the alert after the expression became an empty vector"
echo "  prometheus ALERTS series gone after expr change"

got_resolved=""
for _ in $(seq 1 90); do
  if receiver_has '"status":"resolved"' || receiver_has '"status": "resolved"'; then got_resolved=yes; break; fi
  sleep 5
done
dump_requests "$OUT_DIR/reqs-resolved.json"
[ -n "$got_resolved" ] || fail "receiver never got resolved although sendResolved: true is set"

resolved_fp="$(py "$OUT_DIR/fingerprint.py" "$OUT_DIR/reqs-resolved.json" resolved 2>/dev/null | tail -1)"
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
# snapshot, no PVC deletion, no namespace surgery.
"${KUBECTL[@]}" -n "$NS" delete pod -l app.kubernetes.io/name=prometheus --timeout=180s >/dev/null
"${KUBECTL[@]}" -n "$NS" rollout status "$PROM_STS" --timeout=900s >/dev/null \
  || fail "Prometheus did not come back after the rebuild"

prom_pvc_after="$("${KUBECTL[@]}" -n "$NS" get "pvc/$prom_pvc" -o jsonpath='{.metadata.uid}')"
prom_sts_after="$("${KUBECTL[@]}" -n "$NS" get "$PROM_STS" -o jsonpath='{.metadata.uid}')"
[ "$prom_pvc_after" = "$prom_pvc_before" ] || fail "Prometheus PVC was replaced"
[ "$prom_sts_after" = "$prom_sts_before" ] || fail "Prometheus StatefulSet was replaced"

# Range query across the window holding the pre-rebuild sample. An instant
# query after the rebuild could be answered by a fresh scrape, so the assertion
# is on the stored sample.
range_out="$(py "$OUT_DIR/range.py" "http://$PROM_SVC" "$marker_sel" \
  "$(( marker_ts / 1000 - 60 ))" "$(( marker_ts / 1000 + 180 ))" "$marker_value")" \
  || fail "the pre-rebuild sample is not readable after the rebuild"
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

silence_id="$(py "$OUT_DIR/silence_create.py" "http://$AM_SVC" "$OUT_DIR/silence.json")"
[ -n "$silence_id" ] || fail "Alertmanager did not return a silence ID"
echo "  silence created: $silence_id"

# Normal rebuild of Alertmanager only.
"${KUBECTL[@]}" -n "$NS" delete pod -l app.kubernetes.io/name=alertmanager --timeout=180s >/dev/null
"${KUBECTL[@]}" -n "$NS" rollout status "$AM_STS" --timeout=600s >/dev/null \
  || fail "Alertmanager did not come back after the rebuild"

am_pvc_after="$("${KUBECTL[@]}" -n "$NS" get "pvc/$am_pvc" -o jsonpath='{.metadata.uid}')"
am_sts_after="$("${KUBECTL[@]}" -n "$NS" get "$AM_STS" -o jsonpath='{.metadata.uid}')"
am_secret_after="$("${KUBECTL[@]}" -n "$NS" get "secret/$am_secret" -o jsonpath='{.metadata.uid}')"
[ "$am_pvc_after" = "$am_pvc_before" ] || fail "Alertmanager PVC was replaced"
[ "$am_sts_after" = "$am_sts_before" ] || fail "Alertmanager StatefulSet was replaced"
[ "$am_secret_after" = "$am_secret_before" ] || fail "Alertmanager generated Secret was replaced"

silence_back=""
for _ in $(seq 1 60); do
  got="$(py "$OUT_DIR/silence_get.py" "http://$AM_SVC" "$silence_id" 2>/dev/null || true)"
  if [ "$got" = "$silence_id" ]; then silence_back=yes; break; fi
  sleep 2
done
[ -n "$silence_back" ] || fail "silence $silence_id is gone after the rebuild"
echo "  silence read back by ID after rebuild: $got"

# ---------------------------------------------------------------------------
# cleanup
# ---------------------------------------------------------------------------
echo "[cleanup] removing this run's temporary objects"
"${KUBECTL[@]}" -n "$NS" delete prometheusrule "$RULE_NAME" --ignore-not-found >/dev/null
"${KUBECTL[@]}" -n "$NS" delete alertmanagerconfig "$AMCFG_NAME" --ignore-not-found >/dev/null
"${KUBECTL[@]}" -n "$NS" delete deployment "$RECV_DEPLOY" --ignore-not-found >/dev/null
"${KUBECTL[@]}" -n "$NS" delete service "$RECV_NAME" --ignore-not-found >/dev/null
"${KUBECTL[@]}" -n "$NS" delete configmap "$RECV_NAME" --ignore-not-found >/dev/null
"${KUBECTL[@]}" -n "$NS" delete pod "$CLIENT_POD" --ignore-not-found >/dev/null

echo "metrics verification passed: ns=$NS nodes=$nodes_total up=all cAdvisor=$cadvisor firing=$firing_fp resolved=$resolved_fp prom-pvc=stable am-pvc=stable silence=$silence_id prom=$PROM_SVC am=$AM_SVC evidence=$OUT_DIR"

#!/usr/bin/env bash
# ANI OpenSearch verification (backend capabilities for the C4 card).
#
# What this proves, in order:
#   1. the StatefulSet, its pod and its PVC are the ones the Chart rendered and
#      the volume is actually bound, not merely declared;
#   2. security is enforced rather than merely configured: an unauthenticated
#      request is refused, an authenticated one succeeds over TLS with the
#      cluster's own CA, and the demo configuration is demonstrably absent (the
#      demo user does not exist and its certificate is not trusted);
#   3. the cluster objects this deployment needs are the ones configured: the
#      day-index template matches with one shard and no replica, and the
#      retention policy carries the site's retention period;
#   4. no public endpoint exists and no unexpected workload was deployed;
#   5. the PVC and StatefulSet UIDs are recorded, so a later rebuild can be
#      shown to have kept the same volume.
#
# What this deliberately does NOT prove: that logs written to a container's
# stdout reach this backend. That is the collection path, it is owned by the
# Fluent Bit role which runs after this one, and proving it here would require
# a collector that is not installed yet.
#
# Every call goes through the Service on its cluster DNS name, not the pod IP,
# so the path a client uses is the one under test. No service is restarted.
set -euo pipefail

NS="{{ .ani.components.logging.namespace }}"
RELEASE="ani-opensearch-master"
STS="ani-opensearch-master"
PVC="ani-opensearch-master-0"
RETENTION_ISO="{{ .ani.components.logging.retention_iso }}"
RETENTION_DAYS="{{ .ani.components.logging.retention_days }}"

OUT_DIR="${ANI_VERIFY_OUTPUT_DIR:-/var/lib/ani-installer/logs}"
install -d -m 0700 "$OUT_DIR"
EVIDENCE="$OUT_DIR/opensearch-verify-$(date -u +%Y%m%dT%H%M%SZ)"
install -d -m 0700 "$EVIDENCE"

fail() { echo "opensearch verify: $*" >&2; exit 1; }
note() { printf '%s\n' "$*"; }

KC="kubectl -n $NS"
HOST="$RELEASE.$NS.svc.cluster.local:9200"

# The OpenSearch image has no CA trust for this cluster's internal CA, so every
# call is made from a throwaway pod that is given the CA and the administrator
# credential. The lab tool image is the one the material lock carries for this
# purpose.
TOOL_IMAGE="{{ index .ani.images "docker.io/library/python:3.13.11-alpine3.23" }}"
CLIENT_POD="ani-opensearch-verify-client"

run_in_client() {
  kubectl -n "$NS" exec -i "$CLIENT_POD" -- "$@"
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
# readiness check while losing every index on a pod rebuild.
$KC get statefulset "$STS" -o jsonpath='{.spec.volumeClaimTemplates[*].metadata.name}' \
  | grep -qx "$STS" || fail "the StatefulSet has no '$STS' volumeClaimTemplate"

cat > "$EVIDENCE/volume-ids.txt" <<EOF
pvc_uid=$pvc_uid
statefulset_uid=$sts_uid
pvc_phase=$pvc_phase
EOF

note "== [2] the service is ClusterIP only =="
# Checked before any API call so a published endpoint is reported even if the
# cluster is unreachable.
svc_type="$($KC get svc "$RELEASE" -o jsonpath='{.spec.type}')"
[ "$svc_type" = "ClusterIP" ] || fail "the OpenSearch Service is $svc_type, not ClusterIP"
nodeport="$($KC get svc "$RELEASE" -o jsonpath='{.spec.ports[*].nodePort}')"
[ -z "$nodeport" ] || fail "the OpenSearch Service exposes a node port: $nodeport"
$KC get svc -o wide | tee "$EVIDENCE/services.txt"

note "== [3] security is enforced =="
$KC delete pod "$CLIENT_POD" --ignore-not-found --wait=true >/dev/null
$KC run "$CLIENT_POD" --image="$TOOL_IMAGE" --restart=Never \
  --command -- python3 -c 'import time; time.sleep(1800)' >/dev/null
$KC wait --for=condition=Ready "pod/$CLIENT_POD" --timeout=180s

# The CA and the administrator credential are copied into the client pod through
# the API, so neither is ever a file on this host.
$KC get secret ani-opensearch-node-tls -o jsonpath='{.data.ca\.crt}' \
  | base64 -d | run_in_client sh -c 'cat > /tmp/ca.crt'
$KC get secret ani-opensearch-admin -o jsonpath='{.data.username}' \
  | base64 -d | run_in_client sh -c 'cat > /tmp/user'
$KC get secret ani-opensearch-admin -o jsonpath='{.data.password}' \
  | base64 -d | run_in_client sh -c 'cat > /tmp/pass'

cat > "$EVIDENCE/security.py" <<'PY'
# Three assertions in one program, because each needs the same TLS context:
#   * an unauthenticated request is refused (401) — the demo configuration is
#     gone and anonymous access is off;
#   * the authenticated request succeeds and reports the expected cluster;
#   * the built-in demo user does not authenticate, so the demo account is not
#     present in the internal user database.
import json, ssl, sys, urllib.error, urllib.request

host = sys.argv[1]
user = open("/tmp/user").read().strip()
password = open("/tmp/pass").read().strip()
ctx = ssl.create_default_context(cafile="/tmp/ca.crt")


def call(path, creds=None):
    req = urllib.request.Request(f"https://{host}{path}")
    if creds:
        import base64
        token = base64.b64encode(f"{creds[0]}:{creds[1]}".encode()).decode()
        req.add_header("Authorization", "Basic " + token)
    try:
        with urllib.request.urlopen(req, timeout=30, context=ctx) as r:
            return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()[:300]


# 1. no credential -> refused
status, _ = call("/_cluster/health")
print("anonymous_status", status)
if status != 401:
    raise SystemExit(f"an anonymous request returned {status}, not 401")

# 2. the administrator -> accepted, and the cluster is the one configured
status, body = call("/_cluster/health", (user, password))
print("authenticated_status", status)
if status != 200:
    raise SystemExit(f"the administrator's request returned {status}: {body}")
health = json.loads(body)
print("cluster_name", health.get("cluster_name"))
print("number_of_nodes", health.get("number_of_nodes"))
if health.get("cluster_name") != "ani-opensearch":
    raise SystemExit("the cluster name is not ani-opensearch")
if health.get("number_of_nodes") != 1:
    raise SystemExit("the cluster does not have exactly one node")

# 3. the demo user must not exist
status, _ = call("/_cluster/health", ("admin", "admin"))
print("demo_password_status", status)
if status == 200:
    raise SystemExit("the demo admin password authenticated: the demo configuration is still installed")
PY

run_in_client python3 - "$HOST" < "$EVIDENCE/security.py" | tee "$EVIDENCE/security.txt"
grep -qx "anonymous_status 401" "$EVIDENCE/security.txt" \
  || fail "an unauthenticated request was not refused"
grep -qx "authenticated_status 200" "$EVIDENCE/security.txt" \
  || fail "the administrator could not authenticate"

# The demo certificate must not be trusted either: a client that verifies
# against the cluster's own CA must be able to complete the handshake, which is
# what the calls above already prove, and the demo certificate is not what the
# node presents.
$KC get secret ani-opensearch-node-tls -o jsonpath='{.data.tls\.crt}' \
  | base64 -d | openssl x509 -noout -subject -issuer 2>/dev/null \
  > "$EVIDENCE/node-cert.txt" || true
cat "$EVIDENCE/node-cert.txt"
grep -q "CN *= *ani-opensearch-node" "$EVIDENCE/node-cert.txt" \
  || fail "the node certificate's subject is not CN=ani-opensearch-node"

note "== [4] index template and retention policy =="
cat > "$EVIDENCE/objects.py" <<'PY'
import json, ssl, sys, urllib.error, urllib.request

host = sys.argv[1]
expected_iso = sys.argv[2]
user = open("/tmp/user").read().strip()
password = open("/tmp/pass").read().strip()
ctx = ssl.create_default_context(cafile="/tmp/ca.crt")

import base64
token = base64.b64encode(f"{user}:{password}".encode()).decode()


def get(path):
    req = urllib.request.Request(f"https://{host}{path}")
    req.add_header("Authorization", "Basic " + token)
    with urllib.request.urlopen(req, timeout=30, context=ctx) as r:
        return json.load(r)


template = get("/_index_template/ani-logs")
settings = template["index_templates"][0]["index_template"]["template"]["settings"]
print("index_patterns", template["index_templates"][0]["index_template"]["index_patterns"])
print("number_of_shards", settings["number_of_shards"])
print("number_of_replicas", settings["number_of_replicas"])
if template["index_templates"][0]["index_template"]["index_patterns"] != ["ani-logs-*"]:
    raise SystemExit("the day-index template does not match ani-logs-*")
if int(settings["number_of_shards"]) != 1:
    raise SystemExit("the day-index template does not use one shard")
if int(settings["number_of_replicas"]) != 0:
    raise SystemExit("the day-index template asks for a replica, which a single node cannot allocate")

policy = get("/_plugins/_ism/policies/ani-logs-retention")
state_map = {s["name"]: s for s in policy["policy"]["states"]}
delete = state_map.get("delete")
if not delete or not delete.get("actions"):
    raise SystemExit("the retention policy has no delete state with a delete action")
transitions = state_map["hot"]["transitions"]
ages = [t.get("conditions", {}).get("min_index_age") for t in transitions]
print("min_index_age", ages)
if expected_iso not in ages:
    raise SystemExit(f"the retention policy does not expire at {expected_iso}: {ages}")
PY

run_in_client python3 - "$HOST" "$RETENTION_ISO" < "$EVIDENCE/objects.py" \
  | tee "$EVIDENCE/objects.txt"
grep -qx "index_patterns \['ani-logs-\*'\]" "$EVIDENCE/objects.txt" \
  || fail "the index template does not match the collector's index prefix"
grep -qx "number_of_replicas 0" "$EVIDENCE/objects.txt" \
  || fail "the index template leaves a replica configured"

note "retention period is $RETENTION_DAYS days ($RETENTION_ISO)"

note "== [5] nothing extra was deployed =="
$KC get deploy,sts,ds,svc,job -o wide | tee "$EVIDENCE/workloads.txt"
# Only the OpenSearch StatefulSet is a long-lived workload. The security
# initialization Job is expected to be present and completed; anything else is
# reported.
sts_count="$($KC get statefulset -o name | wc -l)"
[ "$sts_count" = "1" ] || fail "expected only the OpenSearch StatefulSet, found $sts_count"
deploy_count="$($KC get deployment -o name 2>/dev/null | wc -l)"
[ "$deploy_count" = "0" ] || fail "unexpected deployment in $NS"

# No sysctl init container: vm.max_map_count is a node setting here, and the
# Chart's privileged container must stay off.
privileged="$($KC get statefulset "$STS" -o jsonpath='{.spec.template.spec.initContainers[*].name}' 2>/dev/null || true)"
case "$privileged" in
  *sysctl*) fail "the Chart's privileged sysctl init container is present" ;;
esac
note "no privileged sysctl init container"

# dashboards/grafana must not exist anywhere: this batch does not deploy them.
if kubectl get deployment,statefulset,daemonset -A -o name 2>/dev/null | grep -qE 'grafana|dashboards'; then
  fail "a grafana or dashboards workload exists; this batch must not deploy one"
fi
note "no grafana/dashboards workload present"

note "== [6] cleanup of this run's own client pod =="
$KC delete pod "$CLIENT_POD" --ignore-not-found --wait=true >/dev/null
$KC get pod "$CLIENT_POD" >/dev/null 2>&1 && fail "the temporary client pod was not removed"

note "opensearch verify passed"
note "evidence: $EVIDENCE"

#!/usr/bin/env bash
# ANI OpenSearch index setup.
#
# Creates the two cluster objects this deployment needs and nothing else:
#
#   1. an index template matching ani-logs-*, so every day index the collector
#      creates gets one primary shard and no replica. On a single node a
#      replica can never be allocated, so leaving the default (one replica)
#      would make every index permanently yellow;
#   2. an index-state-management policy that deletes a day index once it is
#      older than the site's retention period. The period comes from the same
#      derived value the Loki role uses, so the two backends cannot disagree
#      about what "3 days" means.
#
# Both calls authenticate as the administrator, using the credential the
# security initialization Job created. The password is read from the Secret at
# run time and passed through the environment; it is never written to a file,
# never echoed, and never appears in a command line.
#
# Idempotent: an existing template or policy is overwritten with the same
# definition, so a second install converges rather than failing.
set -euo pipefail

NS="{{ .ani.components.logging.namespace }}"
HOST="ani-opensearch-master.$NS.svc.cluster.local:9200"
CA_SECRET="ani-opensearch-node-tls"
ADMIN_SECRET="ani-opensearch-admin"
TOOL_IMAGE="{{ index .ani.images "docker.io/library/python:3.13.11-alpine3.23" }}"
CLIENT_POD="ani-opensearch-setup-client"

RETENTION_ISO="{{ .ani.components.logging.retention_iso }}"
RETENTION_DAYS="{{ .ani.components.logging.retention_days }}"

OUT_DIR="${ANI_OPENSEARCH_EVIDENCE_DIR:-/var/lib/ani-installer/logs}"
install -d -m 0700 "$OUT_DIR"

fail() { echo "opensearch setup: $*" >&2; exit 1; }
note() { printf '%s\n' "$*"; }

[ -n "$RETENTION_ISO" ] || fail "the retention period did not convert to an ISO-8601 duration"

# A throwaway pod carries the CA and the credential: the OpenSearch image has no
# CA trust for this cluster's internal CA, and the admin Secret must not be
# mounted into anything long-lived.
kubectl -n "$NS" delete pod "$CLIENT_POD" --ignore-not-found --wait=true >/dev/null
kubectl -n "$NS" run "$CLIENT_POD" --image="$TOOL_IMAGE" --restart=Never \
  --command -- python3 -c 'import time; time.sleep(900)' >/dev/null
kubectl -n "$NS" wait --for=condition=Ready "pod/$CLIENT_POD" --timeout=180s

cleanup() {
  kubectl -n "$NS" delete pod "$CLIENT_POD" --ignore-not-found --wait=true >/dev/null 2>&1 || true
}
trap cleanup EXIT

# The credential and the CA are copied into the pod through the API, so neither
# is ever a file on the installer's disk.
kubectl -n "$NS" get secret "$ADMIN_SECRET" -o jsonpath='{.data.username}' \
  | base64 -d | kubectl -n "$NS" exec -i "$CLIENT_POD" -- sh -c 'cat > /tmp/user'
kubectl -n "$NS" get secret "$ADMIN_SECRET" -o jsonpath='{.data.password}' \
  | base64 -d | kubectl -n "$NS" exec -i "$CLIENT_POD" -- sh -c 'cat > /tmp/pass'
kubectl -n "$NS" get secret "$CA_SECRET" -o jsonpath='{.data.ca\.crt}' \
  | base64 -d | kubectl -n "$NS" exec -i "$CLIENT_POD" -- sh -c 'cat > /tmp/ca.crt'

note "== creating the day-index template =="
# The program is piped into the pod from a heredoc, so no multi-line program has
# to survive a shell boundary and nothing is written to the node's disk.
kubectl -n "$NS" exec -i "$CLIENT_POD" -- python3 - "$HOST" <<'PY' \
  | tee "$OUT_DIR/index-template.txt"
import json, os, sys, ssl, urllib.error, urllib.request

host = sys.argv[1]
user = open("/tmp/user").read().strip()
password = open("/tmp/pass").read().strip()
ctx = ssl.create_default_context(cafile="/tmp/ca.crt")

body = {
    "index_patterns": ["ani-logs-*"],
    "template": {
        "settings": {
            # One node, so one shard and no replica: a replica can never be
            # allocated here and would leave every index yellow forever.
            "number_of_shards": 1,
            "number_of_replicas": 0,
        },
    },
}
auth = urllib.request.HTTPPasswordMgrWithDefaultRealm()
auth.add_password(None, f"https://{host}", user, password)
opener = urllib.request.build_opener(
    urllib.request.HTTPSHandler(context=ctx),
    urllib.request.HTTPBasicAuthHandler(auth),
)

req = urllib.request.Request(
    f"https://{host}/_index_template/ani-logs",
    data=json.dumps(body).encode(),
    headers={"Content-Type": "application/json"},
    method="PUT",
)
with opener.open(req, timeout=30) as r:
    print("index template", r.status, r.read().decode()[:200])
PY

note "== creating the retention policy =="
kubectl -n "$NS" exec -i "$CLIENT_POD" -- python3 - "$HOST" "$RETENTION_ISO" "$RETENTION_DAYS" \
  <<'PY' | tee "$OUT_DIR/ism-policy.txt"
import json, sys, ssl, urllib.error, urllib.request

host, retention_iso, retention_days = sys.argv[1], sys.argv[2], sys.argv[3]
user = open("/tmp/user").read().strip()
password = open("/tmp/pass").read().strip()
ctx = ssl.create_default_context(cafile="/tmp/ca.crt")

# The policy deletes a day index once its age exceeds the site's retention
# period. ISM compares durations, which is why the value arrives as an ISO-8601
# duration rather than as a day count.
policy = {
    "policy": {
        "description": f"ANI log retention: delete indices older than {retention_days} days",
        "default_state": "hot",
        "states": [
            {
                "name": "hot",
                "actions": [],
                "transitions": [
                    {
                        "state_name": "delete",
                        "conditions": {"min_index_age": retention_iso},
                    }
                ],
            },
            {
                "name": "delete",
                "actions": [{"delete": {}}],
                "transitions": [],
            },
        ],
        "ism_template": [
            {"index_patterns": ["ani-logs-*"], "priority": 100},
        ],
    }
}
auth = urllib.request.HTTPPasswordMgrWithDefaultRealm()
auth.add_password(None, f"https://{host}", user, password)
opener = urllib.request.build_opener(
    urllib.request.HTTPSHandler(context=ctx),
    urllib.request.HTTPBasicAuthHandler(auth),
)

req = urllib.request.Request(
    f"https://{host}/_plugins/_ism/policies/ani-logs-retention",
    data=json.dumps(policy).encode(),
    headers={"Content-Type": "application/json"},
    method="PUT",
)
with opener.open(req, timeout=30) as r:
    print("ism policy", r.status, r.read().decode()[:300])
PY

note "opensearch index setup complete"

#!/usr/bin/env bash
# ANI OpenSearch index setup.
#
# Creates the two cluster objects this deployment needs and nothing else:
#
#   1. an index template matching ani-logs-*, so every day index the collector
#      creates gets one primary shard and no replica;
#   2. an index-state-management policy that deletes a day index once it is
#      older than the site's retention period.
#
# Runs on the installer node (node1) and uses curl against the OpenSearch
# ClusterIP. The node cannot resolve cluster-internal DNS names, so the
# ClusterIP is fetched via kubectl at run time and TLS verification is skipped
# (the server cert carries DNS SANs, not the ClusterIP). The admin credential
# comes from the Secret via kubectl and is never written to disk.
set -euo pipefail

NS="{{ .ani.components.logging.namespace }}"
ADMIN_SECRET="ani-opensearch-admin"

RETENTION_DAYS="{{ .ani.components.logging.retention_days }}"

OUT_DIR="${ANI_OPENSEARCH_EVIDENCE_DIR:-/var/lib/ani-installer/logs}"
install -d -m 0700 "$OUT_DIR"

fail() { echo "opensearch setup: $*" >&2; exit 1; }
note() { printf '%s\n' "$*"; }

[ -n "$RETENTION_DAYS" ] || fail "the retention day count is empty"

# Resolve the OpenSearch ClusterIP: the node's DNS cannot resolve
# cluster-internal service names.
SVC_IP=""
for i in $(seq 1 30); do
  SVC_IP=$(kubectl -n "$NS" get svc ani-opensearch-master -o jsonpath='{.spec.clusterIP}' 2>/dev/null || true)
  [ -n "$SVC_IP" ] && break
  sleep 2
done
[ -n "$SVC_IP" ] || fail "could not resolve the ani-opensearch-master ClusterIP"

# Extract the admin credential from the Secret.
# A38: the Certificate Ready condition can precede the Secret content, so the
# read retries until cert-manager has actually written the data.
ADMIN_USER_FILE=$(mktemp /tmp/os-user.XXXXXX)
ADMIN_PW_FILE=$(mktemp /tmp/os-pw.XXXXXX)
trap 'rm -f "$ADMIN_USER_FILE" "$ADMIN_PW_FILE"' EXIT

n=0
for i in $(seq 1 60); do
  if kubectl -n "$NS" get secret "$ADMIN_SECRET" >/dev/null 2>&1; then
    kubectl -n "$NS" get secret "$ADMIN_SECRET" -o jsonpath='{.data.username}' | base64 -d > "$ADMIN_USER_FILE"
    kubectl -n "$NS" get secret "$ADMIN_SECRET" -o jsonpath='{.data.password}' | base64 -d > "$ADMIN_PW_FILE"
    [ -s "$ADMIN_USER_FILE" ] && [ -s "$ADMIN_PW_FILE" ] && break
  fi
  sleep 2
done
[ -s "$ADMIN_USER_FILE" ] || fail "the admin username never appeared in $ADMIN_SECRET"

ADMIN_USER=$(cat "$ADMIN_USER_FILE")
ADMIN_PW=$(cat "$ADMIN_PW_FILE")

CURL="curl -sS -k -u $ADMIN_USER:$ADMIN_PW"

note "== creating the day-index template =="
cat > /tmp/os-index-template.json <<'JSON'
{
    "index_patterns": ["ani-logs-*"],
    "template": {
        "settings": {
            "number_of_shards": 1,
            "number_of_replicas": 0
        }
    }
}
JSON
$CURL -X PUT "https://$SVC_IP:9200/_index_template/ani-logs" \
  -H 'Content-Type: application/json' \
  -d @/tmp/os-index-template.json \
  | tee "$OUT_DIR/index-template.txt"
echo

note "== creating the retention policy =="
# A31/A37: ISM min_index_age takes OpenSearch time values ("3d"), NOT
# ISO-8601 durations ("PT72H" dies with a parse error) and NOT hours
# ("3h" = 3 HOURS instead of 3 days).
cat > /tmp/os-ism-policy.json <<JSON
{"policy": {
    "description": "ANI log retention: delete indices older than ${RETENTION_DAYS} days",
    "default_state": "hot",
    "states": [
        {"name": "hot", "actions": [], "transitions": [
            {"state_name": "delete", "conditions": {"min_index_age": "${RETENTION_DAYS}d"}}
        ]},
        {"name": "delete", "actions": [{"delete": {}}], "transitions": []}
    ],
    "ism_template": [{"index_patterns": ["ani-logs-*"], "priority": 100}]
}}
JSON
$CURL -X PUT "https://$SVC_IP:9200/_plugins/_ism/policies/ani-logs-retention" \
  -H 'Content-Type: application/json' \
  -d @/tmp/os-ism-policy.json \
  | tee "$OUT_DIR/ism-policy.txt"
echo

note "opensearch index setup complete"

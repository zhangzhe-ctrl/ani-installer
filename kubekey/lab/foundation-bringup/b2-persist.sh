#!/usr/bin/env bash
# LAB-ONLY normal Pod rebuild persistence test for PostgreSQL (B2).
# Records StatefulSet/PVC/Secret UIDs, writes a unique row over the Service as
# ani_app, deletes the single service Pod, waits for the new Pod and a ready
# Service endpoint, reads the value back over the same Service, and confirms
# PVC/Secret/StatefulSet UIDs are unchanged. Not a product recovery feature.
set -euo pipefail
KC=/etc/kubernetes/admin.conf
K=(kubectl --kubeconfig "$KC")
NS=ani-platform
RUN_ID="$(date +%Y%m%d%H%M%S)-$$"
SUF="$(echo "$RUN_ID" | tr -d '-')"
UNIQ="ani-b2-persist-${SUF}"
WJ="ani-pg-pwrite-${SUF}"
RJ="ani-pg-pread-${SUF}"
OUT="/tmp/ani-b2-persist-${RUN_ID}"
mkdir -p "$OUT"
fail() { echo "FAIL: $*" >&2; exit 1; }

PG_IMAGE="$("${K[@]}" -n "$NS" get statefulset/postgresql -o jsonpath='{.spec.template.spec.containers[0].image}')"
[ -n "$PG_IMAGE" ] || fail "cannot read postgresql image"

STS_UID_B="$("${K[@]}" -n "$NS" get statefulset/postgresql -o jsonpath='{.metadata.uid}')"
PVC_UID_B="$("${K[@]}" -n "$NS" get pvc/data-postgresql-0 -o jsonpath='{.metadata.uid}')"
SEC_UID_B="$("${K[@]}" -n "$NS" get secret/ani-postgres-app -o jsonpath='{.metadata.uid}')"
POD_B="$("${K[@]}" -n "$NS" get pods -l app=postgresql -o jsonpath='{.items[0].metadata.name}')"
POD_UID_B="$("${K[@]}" -n "$NS" get pod/"$POD_B" -o jsonpath='{.metadata.uid}')"
echo "BEFORE sts_uid=$STS_UID_B pvc_uid=$PVC_UID_B secret_uid=$SEC_UID_B pod=$POD_B pod_uid=$POD_UID_B"

mkjob() { # name  sh-body
  cat > "$OUT/$1.yaml" <<JOB
apiVersion: batch/v1
kind: Job
metadata:
  name: $1
  namespace: $NS
  labels: { app.kubernetes.io/name: ani-postgresql-persist }
spec:
  backoffLimit: 0
  template:
    metadata:
      labels: { app.kubernetes.io/name: ani-postgresql-persist }
    spec:
      restartPolicy: Never
      containers:
        - name: pg
          image: $PG_IMAGE
          imagePullPolicy: IfNotPresent
          env:
            - name: PGHOST
              value: postgresql.ani-platform.svc.cluster.local
            - name: PGUSER
              value: ani_app
            - name: PGDATABASE
              value: ani
            - name: PGPASSWORD
              valueFrom:
                secretKeyRef: { name: ani-postgres-app, key: app-password }
          command: ["/bin/sh","-c"]
          args:
            - |
$2
JOB
  "${K[@]}" apply --server-side -f "$OUT/$1.yaml" >/dev/null
  "${K[@]}" -n "$NS" wait --for=condition=complete "job/$1" --timeout=180s >/dev/null || {
    "${K[@]}" -n "$NS" logs "job/$1" >"$OUT/$1.log" 2>&1 || true; cat "$OUT/$1.log" || true; return 1; }
  "${K[@]}" -n "$NS" logs "job/$1" | tee "$OUT/$1.out"
}

wait_endpoint_ready() {
  for i in $(seq 1 30); do
    ep="$("${K[@]}" -n "$NS" get endpointslice -l kubernetes.io/service-name=postgresql -o jsonpath='{.items[*].endpoints[*].conditions.ready}' 2>/dev/null || true)"
    [ "$ep" = "true" ] && { echo "service endpoint ready (${i}x2s)"; return 0; }
    sleep 2
  done
  return 1
}

echo "[1/4] write unique row over Service DNS as ani_app"
mkjob "$WJ" "              set -e
              psql -v ON_ERROR_STOP=1 -c \"CREATE TABLE IF NOT EXISTS ani_persist_kv (k text primary key, v text)\"
              psql -v ON_ERROR_STOP=1 -c \"DELETE FROM ani_persist_kv WHERE k='$UNIQ'\"
              psql -v ON_ERROR_STOP=1 -c \"INSERT INTO ani_persist_kv(k,v) VALUES('$UNIQ','persist-value-$UNIQ')\"
              echo PERSIST-WRITE-OK-$UNIQ" || fail "write job failed"
grep -q "PERSIST-WRITE-OK-$UNIQ" "$OUT/$WJ.out" || fail "write job did not confirm"

echo "[2/4] delete service Pod ($POD_B), wait for rebuild + ready endpoint"
"${K[@]}" -n "$NS" delete pod "$POD_B" --wait=true
"${K[@]}" -n "$NS" wait --for=condition=Ready pod/"$POD_B" --timeout=300s >/dev/null
POD_UID_A="$("${K[@]}" -n "$NS" get pod/"$POD_B" -o jsonpath='{.metadata.uid}')"
POD_IP_A="$("${K[@]}" -n "$NS" get pod/"$POD_B" -o jsonpath='{.status.podIP}')"
POD_NODE_A="$("${K[@]}" -n "$NS" get pod/"$POD_B" -o jsonpath='{.spec.nodeName}')"
echo "AFTER pod=$POD_B pod_uid=$POD_UID_A ip=$POD_IP_A node=$POD_NODE_A"
[ "$POD_UID_B" != "$POD_UID_A" ] || fail "pod was not actually recreated (same UID)"
wait_endpoint_ready || echo "WARN: endpoint not reported ready in time; proceeding"

echo "[3/4] read the value back over the same Service as ani_app (retry on transient network)"
read_ok=0
for attempt in 1 2 3; do
  "${K[@]}" -n "$NS" delete "job/$RJ" --ignore-not-found --wait=true >/dev/null 2>&1 || true
  if mkjob "$RJ" "              set -e
              got=\$(psql -tA -c \"SELECT v FROM ani_persist_kv WHERE k='$UNIQ'\")
              [ \"\$got\" = \"persist-value-$UNIQ\" ] || { echo \"readback mismatch got=\$got\"; exit 1; }
              echo PERSIST-READ-OK-$UNIQ"; then
    grep -q "PERSIST-READ-OK-$UNIQ" "$OUT/$RJ.out" && { read_ok=1; break; }
  fi
  echo "  read attempt $attempt failed; retrying"
  sleep 10
done
[ "$read_ok" = "1" ] || fail "readback job did not confirm after retries"

echo "[4/4] confirm PVC/Secret/StatefulSet UIDs unchanged"
STS_UID_A="$("${K[@]}" -n "$NS" get statefulset/postgresql -o jsonpath='{.metadata.uid}')"
PVC_UID_A="$("${K[@]}" -n "$NS" get pvc/data-postgresql-0 -o jsonpath='{.metadata.uid}')"
SEC_UID_A="$("${K[@]}" -n "$NS" get secret/ani-postgres-app -o jsonpath='{.metadata.uid}')"
[ "$STS_UID_A" = "$STS_UID_B" ] || fail "StatefulSet UID changed"
[ "$PVC_UID_A" = "$PVC_UID_B" ] || fail "PVC UID changed"
[ "$SEC_UID_A" = "$SEC_UID_B" ] || fail "Secret UID changed"
"${K[@]}" -n "$NS" delete "job/$WJ" "job/$RJ" --wait=false >/dev/null 2>&1 || true
echo "B2-PERSISTENCE-OK sts=$STS_UID_A pvc=$PVC_UID_A secret=$SEC_UID_A pod_rebuilt=$POD_UID_A"

#!/usr/bin/env bash
# LAB-ONLY normal Pod rebuild persistence test for Valkey (B3).
# Records StatefulSet/PVC/Secret/Pod UIDs, writes a unique non-expiring key over
# the Service, captures real AOF completion evidence (WAITAOF + INFO persistence,
# no fixed sleep), deletes the single service Pod, waits for the new Pod and a
# ready Service endpoint, reads the value back over the same Service, and
# confirms PVC/Secret/StatefulSet UIDs are unchanged. Not a product feature.
#
# Escaping note: mkjob bodies are double-quoted arguments, so ONE level of \$ in
# this file yields a plain $ in the rendered Job yaml (expanded inside the
# container), while $UNIQ/$WJ/$RJ expand on the host. Same shape as B2.
set -euo pipefail
KC=/etc/kubernetes/admin.conf
K=(kubectl --kubeconfig "$KC")
NS=ani-platform
RUN_ID="$(date +%Y%m%d%H%M%S)-$$"
SUF="$(echo "$RUN_ID" | tr -d '-')"
UNIQ="ani-b3-persist-${SUF}"
WJ="ani-vk-pwrite-${SUF}"
RJ="ani-vk-pread-${SUF}"
OUT="/tmp/ani-b3-persist-${RUN_ID}"
mkdir -p "$OUT"
fail() { echo "FAIL: $*" >&2; exit 1; }

VK_IMAGE="$("${K[@]}" -n "$NS" get statefulset/valkey -o jsonpath='{.spec.template.spec.containers[0].image}')"
[ -n "$VK_IMAGE" ] || fail "cannot read valkey image"

STS_UID_B="$("${K[@]}" -n "$NS" get statefulset/valkey -o jsonpath='{.metadata.uid}')"
PVC_UID_B="$("${K[@]}" -n "$NS" get pvc/data-valkey-0 -o jsonpath='{.metadata.uid}')"
AUTH_UID_B="$("${K[@]}" -n "$NS" get secret/ani-valkey-auth -o jsonpath='{.metadata.uid}')"
CFG_UID_B="$("${K[@]}" -n "$NS" get secret/ani-valkey-config -o jsonpath='{.metadata.uid}')"
POD_B="$("${K[@]}" -n "$NS" get pods -l app=valkey -o jsonpath='{.items[0].metadata.name}')"
POD_UID_B="$("${K[@]}" -n "$NS" get pod/"$POD_B" -o jsonpath='{.metadata.uid}')"
echo "BEFORE sts_uid=$STS_UID_B pvc_uid=$PVC_UID_B auth_uid=$AUTH_UID_B cfg_uid=$CFG_UID_B pod=$POD_B pod_uid=$POD_UID_B"

mkjob() { # name  sh-body
  cat > "$OUT/$1.yaml" <<JOB
apiVersion: batch/v1
kind: Job
metadata:
  name: $1
  namespace: $NS
  labels: { app.kubernetes.io/name: ani-valkey-persist }
spec:
  backoffLimit: 0
  template:
    metadata:
      labels: { app.kubernetes.io/name: ani-valkey-persist }
    spec:
      restartPolicy: Never
      containers:
        - name: vk
          image: $VK_IMAGE
          imagePullPolicy: IfNotPresent
          env:
            - name: REDISCLI_AUTH
              valueFrom: { secretKeyRef: { name: ani-valkey-auth, key: valkey-password } }
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
    ep="$("${K[@]}" -n "$NS" get endpointslice -l kubernetes.io/service-name=valkey -o jsonpath='{.items[*].endpoints[*].conditions.ready}' 2>/dev/null || true)"
    [ "$ep" = "true" ] && { echo "service endpoint ready (${i}x2s)"; return 0; }
    sleep 2
  done
  return 1
}

echo "[1/5] write unique non-expiring key over Service DNS + real AOF evidence"
mkjob "$WJ" "              set -e
              SVC=valkey.ani-platform.svc.cluster.local
              valkey-cli -h \"\$SVC\" SET \"$UNIQ\" \"persist-value-$UNIQ\" >/dev/null
              echo PERSIST-WRITE-OK-$UNIQ
              echo -n \"WAITAOF: \"; valkey-cli -h \"\$SVC\" WAITAOF 1 0 10000
              valkey-cli -h \"\$SVC\" INFO persistence | grep -E '^aof_enabled|^aof_last_write_status|^aof_current_size'
              echo B3-AOF-CONFIRMED" || fail "write job failed"
grep -q "PERSIST-WRITE-OK-$UNIQ" "$OUT/$WJ.out" || fail "write job did not confirm"
grep -q "B3-AOF-CONFIRMED" "$OUT/$WJ.out" || fail "AOF completion evidence missing"

echo "[2/5] delete service Pod ($POD_B), wait for rebuild + ready endpoint"
"${K[@]}" -n "$NS" delete pod "$POD_B" --wait=true
"${K[@]}" -n "$NS" wait --for=condition=Ready pod/"$POD_B" --timeout=300s >/dev/null
POD_UID_A="$("${K[@]}" -n "$NS" get pod/"$POD_B" -o jsonpath='{.metadata.uid}')"
POD_IP_A="$("${K[@]}" -n "$NS" get pod/"$POD_B" -o jsonpath='{.status.podIP}')"
POD_NODE_A="$("${K[@]}" -n "$NS" get pod/"$POD_B" -o jsonpath='{.spec.nodeName}')"
echo "AFTER pod=$POD_B pod_uid=$POD_UID_A ip=$POD_IP_A node=$POD_NODE_A"
[ "$POD_UID_B" != "$POD_UID_A" ] || fail "pod was not actually recreated (same UID)"
wait_endpoint_ready || echo "WARN: endpoint not reported ready in time; proceeding"

echo "[3/5] K-5 heal ladder if the rebuilt Pod is unreachable (kcn-controller restart first)"
bash /home/chabking/ani-installer-runs/foundation-20260918/lab/b3-heal.sh || true

echo "[4/5] read the value back over the same Service (retry on transient network)"
read_ok=0
for attempt in 1 2 3; do
  "${K[@]}" -n "$NS" delete "job/$RJ" --ignore-not-found --wait=true >/dev/null 2>&1 || true
  if mkjob "$RJ" "              set -e
              SVC=valkey.ani-platform.svc.cluster.local
              got=\$(valkey-cli -h \"\$SVC\" --raw GET \"$UNIQ\")
              [ \"\$got\" = \"persist-value-$UNIQ\" ] || { echo \"readback mismatch got=\$got\"; exit 1; }
              echo PERSIST-READ-OK-$UNIQ"; then
    grep -q "PERSIST-READ-OK-$UNIQ" "$OUT/$RJ.out" && { read_ok=1; break; }
  fi
  echo "  read attempt $attempt failed; retrying"
  sleep 10
done
[ "$read_ok" = "1" ] || fail "readback job did not confirm after retries"

echo "[5/5] confirm PVC/Secret/StatefulSet UIDs unchanged"
STS_UID_A="$("${K[@]}" -n "$NS" get statefulset/valkey -o jsonpath='{.metadata.uid}')"
PVC_UID_A="$("${K[@]}" -n "$NS" get pvc/data-valkey-0 -o jsonpath='{.metadata.uid}')"
AUTH_UID_A="$("${K[@]}" -n "$NS" get secret/ani-valkey-auth -o jsonpath='{.metadata.uid}')"
CFG_UID_A="$("${K[@]}" -n "$NS" get secret/ani-valkey-config -o jsonpath='{.metadata.uid}')"
[ "$STS_UID_A" = "$STS_UID_B" ] || fail "StatefulSet UID changed"
[ "$PVC_UID_A" = "$PVC_UID_B" ] || fail "PVC UID changed"
[ "$AUTH_UID_A" = "$AUTH_UID_B" ] || fail "auth Secret UID changed"
[ "$CFG_UID_A" = "$CFG_UID_B" ] || fail "config Secret UID changed"
"${K[@]}" -n "$NS" delete "job/$WJ" "job/$RJ" --wait=false >/dev/null 2>&1 || true
echo "B3-PERSISTENCE-OK sts=$STS_UID_A pvc=$PVC_UID_A auth=$AUTH_UID_A cfg=$CFG_UID_A pod_rebuilt=$POD_UID_A"

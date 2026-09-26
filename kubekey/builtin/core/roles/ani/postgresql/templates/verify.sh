#!/usr/bin/env bash
# PostgreSQL component verification, rendered by the ANI postgresql role.
# Template source: builtin/core/roles/ani/postgresql/templates/verify.sh
#
# Proves the running instance actually authenticates the application user over
# the Service DNS and performs real CRUD, and that a wrong password is rejected.
# Only "StatefulSet is Ready" is not enough, and no password is ever printed or
# copied into an output file.
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
PG_IMAGE="{{ index .ani.images "docker.io/library/postgres:17.11-bookworm" }}"
NS=ani-platform
RUN_ID="$(date +%Y%m%d%H%M%S)-$$"
OUT_DIR="${ANI_VERIFY_OUTPUT_DIR:-/tmp/ani-postgresql-verify-${RUN_ID}}"
mkdir -p "$OUT_DIR"
fail() { echo "FAIL: $*" >&2; exit 1; }

echo "[1/5] workload and resources"
ready="$("${KUBECTL[@]}" -n "$NS" get statefulset/postgresql -o jsonpath='{.status.readyReplicas}')"
[ "$ready" = "1" ] || fail "statefulset/postgresql not ready (readyReplicas=$ready, want 1)"
pvc_phase="$("${KUBECTL[@]}" -n "$NS" get pvc/data-postgresql-0 -o jsonpath='{.status.phase}')"
[ "$pvc_phase" = "Bound" ] || fail "PVC data-postgresql-0 is not Bound (phase=$pvc_phase)"
"${KUBECTL[@]}" -n "$NS" get secret/ani-postgres-admin >/dev/null || fail "admin secret missing"
"${KUBECTL[@]}" -n "$NS" get secret/ani-postgres-app >/dev/null || fail "app secret missing"

echo "[2/5] authorized client job (ani_app CRUD over Service DNS)"
UNIQ="ani-b2-$(echo "$RUN_ID" | tr -d '-')"
JOB_OK="ani-pg-ok-${RUN_ID}"
cat > "$OUT_DIR/ok-job.yaml" <<JOB_EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: $JOB_OK
  namespace: $NS
  labels:
    app.kubernetes.io/name: ani-postgresql-verify
spec:
  backoffLimit: 0
  template:
    metadata:
      labels:
        app.kubernetes.io/name: ani-postgresql-verify
    spec:
      restartPolicy: Never
      containers:
        - name: pg
          image: $PG_IMAGE
          imagePullPolicy: IfNotPresent
          env:
            - name: PGHOST
              value: postgresql.ani-platform.svc.cluster.local
            - name: PGPORT
              value: "5432"
            - name: PGUSER
              value: ani_app
            - name: PGDATABASE
              value: ani
            - name: PGPASSWORD
              valueFrom:
                secretKeyRef:
                  name: ani-postgres-app
                  key: app-password
          command:
            - /bin/sh
            - -c
            - |
              set -e
              psql -v ON_ERROR_STOP=1 -c "CREATE TABLE IF NOT EXISTS ani_verify_kv (k text primary key, v text)"
              psql -v ON_ERROR_STOP=1 -c "DELETE FROM ani_verify_kv WHERE k='$UNIQ'"
              psql -v ON_ERROR_STOP=1 -c "INSERT INTO ani_verify_kv(k,v) VALUES('$UNIQ','$UNIQ-value')"
              got="\$(psql -tA -c "SELECT v FROM ani_verify_kv WHERE k='$UNIQ'")"
              [ "\$got" = "$UNIQ-value" ] || { echo "readback mismatch: got=\$got"; exit 1; }
              echo "ANI-PG-CRUD-OK unique=$UNIQ"
JOB_EOF

"${KUBECTL[@]}" apply --server-side -f "$OUT_DIR/ok-job.yaml" >/dev/null
if "${KUBECTL[@]}" -n "$NS" wait --for=condition=complete "job/$JOB_OK" --timeout=300s >/dev/null 2>&1; then
  echo "  job/$JOB_OK completed"
else
  echo "  job/$JOB_OK did not complete; logs follow" >&2
  "${KUBECTL[@]}" -n "$NS" logs "job/$JOB_OK" >"$OUT_DIR/ok-job-logs.txt" 2>&1 || true
  cat "$OUT_DIR/ok-job-logs.txt" || true
  fail "authorized client job failed; evidence in $OUT_DIR"
fi
"${KUBECTL[@]}" -n "$NS" logs "job/$JOB_OK" | tee "$OUT_DIR/ok-job.out"
grep -q '^ANI-PG-CRUD-OK ' "$OUT_DIR/ok-job.out" || fail "authorized CRUD job did not report success"

echo "[3/5] wrong password is rejected"
JOB_BAD="ani-pg-bad-${RUN_ID}"
cat > "$OUT_DIR/bad-job.yaml" <<JOB_EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: $JOB_BAD
  namespace: $NS
  labels:
    app.kubernetes.io/name: ani-postgresql-verify
spec:
  backoffLimit: 0
  template:
    metadata:
      labels:
        app.kubernetes.io/name: ani-postgresql-verify
    spec:
      restartPolicy: Never
      containers:
        - name: pg
          image: $PG_IMAGE
          imagePullPolicy: IfNotPresent
          env:
            - name: PGHOST
              value: postgresql.ani-platform.svc.cluster.local
            - name: PGPORT
              value: "5432"
            - name: PGUSER
              value: ani_app
            - name: PGDATABASE
              value: ani
            - name: PGPASSWORD
              value: this-is-not-the-real-password
          command:
            - /bin/sh
            - -c
            - |
              set -e
              if psql -tA -c "SELECT 1" >/dev/null 2>&1; then
                echo "connection with a wrong password unexpectedly succeeded"
                exit 1
              fi
              echo "ANI-PG-AUTH-REJECTED"
JOB_EOF

"${KUBECTL[@]}" apply --server-side -f "$OUT_DIR/bad-job.yaml" >/dev/null
if "${KUBECTL[@]}" -n "$NS" wait --for=condition=complete "job/$JOB_BAD" --timeout=120s >/dev/null 2>&1; then
  "${KUBECTL[@]}" -n "$NS" logs "job/$JOB_BAD" | tee "$OUT_DIR/bad-job.out"
  grep -q '^ANI-PG-AUTH-REJECTED$' "$OUT_DIR/bad-job.out" || fail "negative auth job did not report rejection"
else
  fail "negative auth job did not reach the rejection assertion"
fi

echo "[4/5] cleanup verification jobs"
"${KUBECTL[@]}" -n "$NS" delete "job/$JOB_OK" "job/$JOB_BAD" --wait=false >/dev/null 2>&1 || true

echo "[5/5] summary"
echo "postgresql verification passed: ns=$NS svc=postgresql pvc=Bound auth=rejected-wrong-password crud=ok evidence=$OUT_DIR"

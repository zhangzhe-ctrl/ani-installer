#!/usr/bin/env bash
# Valkey component verification, rendered by the ANI valkey role.
# Template source: builtin/core/roles/ani/valkey/templates/verify.sh
#
# Proves the running instance actually requires the password over the Service
# DNS (authenticated SET/GET of a unique value), that a TTL key really expires,
# and that an unauthenticated request is rejected. Only "StatefulSet is Ready"
# is not enough, and no password is ever printed or copied into an output file.
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
VK_IMAGE="{{ index .ani.images "docker.io/valkey/valkey:8.1.10-alpine" }}"
NS=ani-platform
RUN_ID="$(date +%Y%m%d%H%M%S)-$$"
OUT_DIR="${ANI_VERIFY_OUTPUT_DIR:-/tmp/ani-valkey-verify-${RUN_ID}}"
mkdir -p "$OUT_DIR"
fail() { echo "FAIL: $*" >&2; exit 1; }

echo "[1/4] workload and resources"
ready="$("${KUBECTL[@]}" -n "$NS" get statefulset/valkey -o jsonpath='{.status.readyReplicas}')"
[ "$ready" = "1" ] || fail "statefulset/valkey not ready (readyReplicas=$ready, want 1)"
pvc_phase="$("${KUBECTL[@]}" -n "$NS" get pvc/data-valkey-0 -o jsonpath='{.status.phase}')"
[ "$pvc_phase" = "Bound" ] || fail "PVC data-valkey-0 is not Bound (phase=$pvc_phase)"
"${KUBECTL[@]}" -n "$NS" get secret/ani-valkey-auth >/dev/null || fail "auth secret missing"
"${KUBECTL[@]}" -n "$NS" get secret/ani-valkey-config >/dev/null || fail "config secret missing"

echo "[2/4] authorized client job (auth SET/GET + TTL expiry over Service DNS)"
# The client authenticates through REDISCLI_AUTH (env, from the Secret), never
# through a command-line argument. The unique key is generated inside the
# container. Container-side shell variables are escaped (\$) so only the job
# name and image are expanded on this host (the B2 heredoc lesson).
JOB_OK="ani-vk-ok-${RUN_ID}"
cat > "$OUT_DIR/ok-job.yaml" <<JOB_EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: $JOB_OK
  namespace: $NS
  labels:
    app.kubernetes.io/name: ani-valkey-verify
spec:
  backoffLimit: 0
  template:
    metadata:
      labels:
        app.kubernetes.io/name: ani-valkey-verify
    spec:
      restartPolicy: Never
      containers:
        - name: vk
          image: $VK_IMAGE
          imagePullPolicy: IfNotPresent
          env:
            - name: REDISCLI_AUTH
              valueFrom:
                secretKeyRef:
                  name: ani-valkey-auth
                  key: valkey-password
          command:
            - /bin/sh
            - -c
            - |
              set -e
              SVC=valkey.ani-platform.svc.cluster.local
              UNIQ="ani-b3-\$(date +%s)-\$\$"
              valkey-cli -h "\$SVC" SET "\$UNIQ" "\$UNIQ-value" >/dev/null
              # --raw: plain GET wraps strings in quotes and prints (nil) for
              # missing keys, which would break exact comparisons.
              got="\$(valkey-cli -h "\$SVC" --raw GET "\$UNIQ")"
              [ "\$got" = "\$UNIQ-value" ] || { echo "readback mismatch: got=\$got"; exit 1; }
              echo "ANI-VALKEY-SETGET-OK unique=\$UNIQ"
              valkey-cli -h "\$SVC" SET "\$UNIQ-ttl" "\$UNIQ-ttl-value" EX 2 >/dev/null
              sleep 3
              ttl_got="\$(valkey-cli -h "\$SVC" --raw GET "\$UNIQ-ttl")"
              [ -z "\$ttl_got" ] || { echo "ttl key did not expire: got=\$ttl_got"; exit 1; }
              echo "ANI-VALKEY-TTL-EXPIRED"
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
grep -q '^ANI-VALKEY-SETGET-OK ' "$OUT_DIR/ok-job.out" || fail "authorized SET/GET job did not report success"
grep -q '^ANI-VALKEY-TTL-EXPIRED$' "$OUT_DIR/ok-job.out" || fail "TTL key did not expire"

echo "[3/4] unauthenticated request is rejected"
JOB_BAD="ani-vk-bad-${RUN_ID}"
cat > "$OUT_DIR/bad-job.yaml" <<JOB_EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: $JOB_BAD
  namespace: $NS
  labels:
    app.kubernetes.io/name: ani-valkey-verify
spec:
  backoffLimit: 0
  template:
    metadata:
      labels:
        app.kubernetes.io/name: ani-valkey-verify
    spec:
      restartPolicy: Never
      containers:
        - name: vk
          image: $VK_IMAGE
          imagePullPolicy: IfNotPresent
          command:
            - /bin/sh
            - -c
            - |
              set -e
              # valkey-cli exits 0 for server error replies, so the exit code
              # cannot distinguish "rejected" from "allowed". Assert on the
              # reply instead: NOAUTH/WRONGPASS are RESP-level error codes for
              # this exact situation, while a connection failure prints a
              # different message and must FAIL here, never masquerade as an
              # auth rejection (the exit-code version had that false-pass hole).
              out="\$(valkey-cli -h valkey.ani-platform.svc.cluster.local -t 10 GET ani-auth-probe 2>&1 || true)"
              case "\$out" in
                *NOAUTH*|*WRONGPASS*) echo "ANI-VALKEY-AUTH-REJECTED";;
                "") echo "empty reply from server"; exit 1;;
                *) echo "unexpected reply (want an auth error): \$out"; exit 1;;
              esac
JOB_EOF

"${KUBECTL[@]}" apply --server-side -f "$OUT_DIR/bad-job.yaml" >/dev/null
if "${KUBECTL[@]}" -n "$NS" wait --for=condition=complete "job/$JOB_BAD" --timeout=120s >/dev/null 2>&1; then
  "${KUBECTL[@]}" -n "$NS" logs "job/$JOB_BAD" | tee "$OUT_DIR/bad-job.out"
  grep -q '^ANI-VALKEY-AUTH-REJECTED$' "$OUT_DIR/bad-job.out" || fail "negative auth job did not report rejection"
else
  fail "negative auth job did not reach the rejection assertion"
fi

echo "[4/4] cleanup verification jobs"
"${KUBECTL[@]}" -n "$NS" delete "job/$JOB_OK" "job/$JOB_BAD" --wait=false >/dev/null 2>&1 || true

echo "valkey verification passed: ns=$NS svc=valkey pvc=Bound auth=rejected-unauthenticated setget=ok ttl=expired evidence=$OUT_DIR"

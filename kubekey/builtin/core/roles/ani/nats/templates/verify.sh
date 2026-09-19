#!/usr/bin/env bash
# NATS JetStream component verification, rendered by the ANI nats role.
# Template source: builtin/core/roles/ani/nats/templates/verify.sh
#
# Proves the running instance actually requires the token over the Service DNS
# and that JetStream works end to end: a unique file-storage replicas=1 stream,
# a durable explicit-ack pull consumer, two published messages each confirmed
# by a JetStream PubAck ("Stored in Stream: X Sequence: N"), the first message
# consumed+acked, and the second left pending (num_ack_pending=1).
# The token reaches the CLI only through the NATS_TOKEN env var (from the
# Secret) - never in command arguments. Only "StatefulSet is Ready" is not
# enough, and no token is ever printed or copied into an output file.
#
# Command shapes frozen against nats-box 0.19.7 (nats CLI) on the build host:
# stream/consumer creation must use --config <json> (no TTY in a Job), publish
# must use -J to get the PubAck, and auth failures print
# "nats: Authorization Violation" while still exiting - assert on content.
set -euo pipefail

KUBECONFIG_FILE="${ANI_VERIFY_KUBECONFIG:-/etc/kubernetes/admin.conf}"
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_FILE")
NB_IMAGE="{{ index .ani.images "docker.io/natsio/nats-box:0.19.7" }}"
NS=ani-platform
SVC="nats.$NS.svc.cluster.local"
RUN_ID="$(date +%Y%m%d%H%M%S)-$$"
OUT_DIR="${ANI_VERIFY_OUTPUT_DIR:-/tmp/ani-nats-verify-${RUN_ID}}"
mkdir -p "$OUT_DIR"
fail() { echo "FAIL: $*" >&2; exit 1; }

echo "[1/4] workload and resources"
ready="$("${KUBECTL[@]}" -n "$NS" get statefulset/nats -o jsonpath='{.status.readyReplicas}')"
[ "$ready" = "1" ] || fail "statefulset/nats not ready (readyReplicas=$ready, want 1)"
pvc_phase="$("${KUBECTL[@]}" -n "$NS" get pvc/nats-js-nats-0 -o jsonpath='{.status.phase}')"
[ "$pvc_phase" = "Bound" ] || fail "PVC nats-js-nats-0 is not Bound (phase=$pvc_phase)"
"${KUBECTL[@]}" -n "$NS" get secret/ani-nats-auth >/dev/null || fail "auth secret missing"

echo "[2/4] authorized client job (stream + durable pull consumer + PubAcks)"
# The stream/consumer names are unique per run and generated inside the
# container. Container-side shell variables are escaped (\$) so only the job
# name and image are expanded on this host (the B2/B3 heredoc pattern).
JOB_OK="ani-nats-ok-${RUN_ID}"
cat > "$OUT_DIR/ok-job.yaml" <<JOB_EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: $JOB_OK
  namespace: $NS
  labels:
    app.kubernetes.io/name: ani-nats-verify
spec:
  backoffLimit: 0
  template:
    metadata:
      labels:
        app.kubernetes.io/name: ani-nats-verify
    spec:
      restartPolicy: Never
      containers:
        - name: nb
          image: $NB_IMAGE
          imagePullPolicy: IfNotPresent
          env:
            - name: NATS_TOKEN
              valueFrom:
                secretKeyRef:
                  name: ani-nats-auth
                  key: token
          command:
            - /bin/sh
            - -c
            - |
              set -e
              SVC="$SVC"
              UNIQ="ani-b4-\$(date +%s)-\$RANDOM"
              NATS="nats -s nats://\$SVC"
              printf '{"name":"%s","subjects":["%s.data"],"retention":"limits","storage":"file","num_replicas":1,"discard":"old"}' "\$UNIQ" "\$UNIQ" > /tmp/stream.json
              \$NATS stream add --config /tmp/stream.json 2>&1 | grep -q "was created"
              echo "ANI-NATS-STREAM-OK stream=\$UNIQ storage=file replicas=1"
              \$NATS pub -J "\$UNIQ.data" "msg-one-\$UNIQ" 2>&1 | grep -q "Stored in Stream: \$UNIQ Sequence: 1"
              \$NATS pub -J "\$UNIQ.data" "msg-two-\$UNIQ" 2>&1 | grep -q "Stored in Stream: \$UNIQ Sequence: 2"
              echo "ANI-NATS-PUBACK-OK puback=jetstream messages=2"
              printf '{"durable_name":"%s-CON","filter_subject":"%s.data","ack_policy":"explicit","deliver_policy":"all","replay_policy":"instant"}' "\$UNIQ" "\$UNIQ" > /tmp/consumer.json
              \$NATS consumer add "\$UNIQ" --config /tmp/consumer.json 2>&1 | grep -q "created"
              got="\$(\$NATS consumer next "\$UNIQ" "\$UNIQ-CON" --raw --ack --count 1)"
              [ "\$got" = "msg-one-\$UNIQ" ] || { echo "first-message mismatch: got=\$got"; exit 1; }
              echo "ANI-NATS-CONSUMED-OK first=acked"
              \$NATS stream info "\$UNIQ" --json | grep -Eq '"messages":[[:space:]]*2[,}]'
              # the second message was never DELIVERED, so it counts as
              # num_pending (num_ack_pending is delivered-but-unacked)
              \$NATS consumer info "\$UNIQ" "\$UNIQ-CON" --json | grep -Eq '"num_pending":[[:space:]]*1[,}]'
              echo "ANI-NATS-PENDING-OK"
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
grep -q '^ANI-NATS-STREAM-OK ' "$OUT_DIR/ok-job.out" || fail "stream creation did not report success"
grep -q '^ANI-NATS-PUBACK-OK ' "$OUT_DIR/ok-job.out" || fail "JetStream PubAck was not confirmed"
grep -q '^ANI-NATS-CONSUMED-OK ' "$OUT_DIR/ok-job.out" || fail "first message was not consumed+acked"
grep -q '^ANI-NATS-PENDING-OK$' "$OUT_DIR/ok-job.out" || fail "second message is not pending"

echo "[3/4] wrong token is rejected"
JOB_BAD="ani-nats-bad-${RUN_ID}"
cat > "$OUT_DIR/bad-job.yaml" <<JOB_EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: $JOB_BAD
  namespace: $NS
  labels:
    app.kubernetes.io/name: ani-nats-verify
spec:
  backoffLimit: 0
  template:
    metadata:
      labels:
        app.kubernetes.io/name: ani-nats-verify
    spec:
      restartPolicy: Never
      containers:
        - name: nb
          image: $NB_IMAGE
          imagePullPolicy: IfNotPresent
          env:
            - name: NATS_TOKEN
              value: this-is-not-the-real-token
          command:
            - /bin/sh
            - -c
            - |
              set -e
              out="\$(nats -s nats://$SVC stream ls 2>&1 || true)"
              case "\$out" in
                *"Authorization Violation"*) echo "ANI-NATS-AUTH-REJECTED";;
                "") echo "empty reply"; exit 1;;
                *) echo "unexpected reply: \$out"; exit 1;;
              esac
JOB_EOF

"${KUBECTL[@]}" apply --server-side -f "$OUT_DIR/bad-job.yaml" >/dev/null
if "${KUBECTL[@]}" -n "$NS" wait --for=condition=complete "job/$JOB_BAD" --timeout=120s >/dev/null 2>&1; then
  "${KUBECTL[@]}" -n "$NS" logs "job/$JOB_BAD" | tee "$OUT_DIR/bad-job.out"
  grep -q '^ANI-NATS-AUTH-REJECTED$' "$OUT_DIR/bad-job.out" || fail "negative auth job did not report rejection"
else
  fail "negative auth job did not reach the rejection assertion"
fi

echo "[4/4] cleanup verification jobs"
"${KUBECTL[@]}" -n "$NS" delete "job/$JOB_OK" "job/$JOB_BAD" --wait=false >/dev/null 2>&1 || true

echo "nats verification passed: ns=$NS svc=nats pvc=Bound auth=rejected-wrong-token stream=file-replicas1 puback=ok consumed=1 pending=1 evidence=$OUT_DIR"

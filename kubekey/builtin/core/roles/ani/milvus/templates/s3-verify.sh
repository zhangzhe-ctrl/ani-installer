#!/usr/bin/env bash
# Prove an OBC-generated principal can put and read an object through RGW.
set -euo pipefail
KUBECONFIG_FILE="${ANI_VERIFY_KUBECONFIG:?}"
test -f "$KUBECONFIG_FILE"
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_FILE")
NS=ani-platform
CLAIM=ani-milvus-objects
IMAGE='{{ index .ani.images "docker.io/amazon/aws-cli:2.31.30" }}'
OUT_DIR="${ANI_VERIFY_OUTPUT_DIR:?}"
mkdir -p "$OUT_DIR"
"${KUBECTL[@]}" -n "$NS" wait "objectbucketclaim/$CLAIM" --for=jsonpath='{.status.phase}=Bound' --timeout=300s
"${KUBECTL[@]}" -n "$NS" get "secret/$CLAIM" >/dev/null
"${KUBECTL[@]}" -n "$NS" get "configmap/$CLAIM" >/dev/null
bucket="$("${KUBECTL[@]}" -n "$NS" get "configmap/$CLAIM" -o jsonpath='{.data.BUCKET_NAME}')"
[ "$bucket" = "$CLAIM" ] || { echo "OBC bucket name mismatch" >&2; exit 1; }
job="ani-milvus-s3-$(date +%Y%m%d%H%M%S)-$$"
cat > "$OUT_DIR/$job.yaml" <<JOB_EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: $job
  namespace: $NS
  labels:
    app.kubernetes.io/name: ani-milvus-s3-verify
spec:
  backoffLimit: 0
  template:
    metadata:
      labels:
        app.kubernetes.io/name: ani-milvus-s3-verify
    spec:
      restartPolicy: Never
      containers:
        - name: aws
          image: $IMAGE
          imagePullPolicy: IfNotPresent
          env:
            - name: AWS_ACCESS_KEY_ID
              valueFrom:
                secretKeyRef: {name: $CLAIM, key: AWS_ACCESS_KEY_ID}
            - name: AWS_SECRET_ACCESS_KEY
              valueFrom:
                secretKeyRef: {name: $CLAIM, key: AWS_SECRET_ACCESS_KEY}
            - name: BUCKET_HOST
              valueFrom:
                configMapKeyRef: {name: $CLAIM, key: BUCKET_HOST}
            - name: BUCKET_PORT
              valueFrom:
                configMapKeyRef: {name: $CLAIM, key: BUCKET_PORT}
            - name: BUCKET_NAME
              valueFrom:
                configMapKeyRef: {name: $CLAIM, key: BUCKET_NAME}
            - name: AWS_DEFAULT_REGION
              value: us-east-1
            - name: AWS_EC2_METADATA_DISABLED
              value: "true"
          command: ["/bin/sh", "-ec"]
          args:
            - |
              set -eu
              endpoint="http://\${BUCKET_HOST}:\${BUCKET_PORT}"
              key="ani-probe/$job"
              marker="ani-rgw-$job"
              printf '%s' "\$marker" > /tmp/marker
              aws --endpoint-url "\$endpoint" s3api put-object --bucket "\$BUCKET_NAME" --key "\$key" --body /tmp/marker --output json >/dev/null
              aws --endpoint-url "\$endpoint" s3api get-object --bucket "\$BUCKET_NAME" --key "\$key" /tmp/readback --output json >/dev/null
              test "\$(cat /tmp/readback)" = "\$marker"
              echo ANI-RGW-S3-PUT-GET-OK
JOB_EOF
"${KUBECTL[@]}" apply --server-side -f "$OUT_DIR/$job.yaml"
if ! "${KUBECTL[@]}" -n "$NS" wait "job/$job" --for=condition=complete --timeout=300s; then
  "${KUBECTL[@]}" -n "$NS" logs "job/$job" > "$OUT_DIR/$job.log" 2>&1 || true
  echo "S3 verification failed; job/$job retained for diagnosis" >&2
  exit 1
fi
"${KUBECTL[@]}" -n "$NS" logs "job/$job" > "$OUT_DIR/$job.log"
grep -qx 'ANI-RGW-S3-PUT-GET-OK' "$OUT_DIR/$job.log"
"${KUBECTL[@]}" -n "$NS" delete "job/$job" --wait=false >/dev/null
printf 'S3 write/read passed; evidence=%s\n' "$OUT_DIR/$job.log"

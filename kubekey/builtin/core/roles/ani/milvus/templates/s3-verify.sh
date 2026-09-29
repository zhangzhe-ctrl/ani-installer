#!/usr/bin/env bash
# Prove the selected application's limited S3 identity can write and read.
set -euo pipefail
KUBECONFIG_FILE="${ANI_VERIFY_KUBECONFIG:?}"
test -f "$KUBECONFIG_FILE"
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_FILE")
NS=ani-platform
IMAGE='{{ index .ani.images "docker.io/amazon/aws-cli:2.31.30" }}'
OUT_DIR="${ANI_VERIFY_OUTPUT_DIR:?}"
mkdir -p "$OUT_DIR"
{{ if eq .ani.objectStorage.milvus_s3.provider "rgw" }}
CLAIM=ani-milvus-objects
"${KUBECTL[@]}" -n "$NS" wait "objectbucketclaim/$CLAIM" --for=jsonpath='{.status.phase}=Bound' --timeout=300s
"${KUBECTL[@]}" -n "$NS" get "secret/$CLAIM" >/dev/null
"${KUBECTL[@]}" -n "$NS" get "configmap/$CLAIM" >/dev/null
bucket="$("${KUBECTL[@]}" -n "$NS" get "configmap/$CLAIM" -o jsonpath='{.data.BUCKET_NAME}')"
[ "$bucket" = "$CLAIM" ] || { echo "OBC bucket name mismatch" >&2; exit 1; }
{{ end }}
"${KUBECTL[@]}" -n "$NS" get secret/{{ .ani.objectStorage.milvus_s3.secret_name }} >/dev/null
"${KUBECTL[@]}" -n "$NS" get configmap/{{ .ani.objectStorage.milvus_s3.ca_config_map }} >/dev/null
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
                secretKeyRef: {name: {{ .ani.objectStorage.milvus_s3.secret_name }}, key: {{ .ani.objectStorage.milvus_s3.access_key_field }}}
            - name: AWS_SECRET_ACCESS_KEY
              valueFrom:
                secretKeyRef: {name: {{ .ani.objectStorage.milvus_s3.secret_name }}, key: {{ .ani.objectStorage.milvus_s3.secret_key_field }}}
            - name: BUCKET_NAME
              value: {{ .ani.objectStorage.milvus_s3.bucket }}
            - name: S3_ENDPOINT
              value: {{ .ani.objectStorage.milvus_s3.endpoint }}
            - name: AWS_CA_BUNDLE
              value: {{ .ani.objectStorage.milvus_s3.ca_path }}
            - name: AWS_DEFAULT_REGION
              value: {{ .ani.objectStorage.milvus_s3.region }}
            - name: AWS_EC2_METADATA_DISABLED
              value: "true"
          command: ["/bin/sh", "-ec"]
          volumeMounts:
            - name: s3-ca
              mountPath: {{ .ani.objectStorage.milvus_s3.ca_mount_path }}
              readOnly: true
          args:
            - |
              set -eu
              key="{{ .ani.objectStorage.milvus_s3.root_path }}/ani-probe/$job"
              marker="ani-s3-$job"
              printf '%s' "\$marker" > /tmp/marker
              aws --endpoint-url "\$S3_ENDPOINT" s3api put-object --bucket "\$BUCKET_NAME" --key "\$key" --body /tmp/marker --output json >/dev/null
              aws --endpoint-url "\$S3_ENDPOINT" s3api get-object --bucket "\$BUCKET_NAME" --key "\$key" /tmp/readback --output json >/dev/null
              test "\$(cat /tmp/readback)" = "\$marker"
              echo ANI-S3-PUT-GET-OK
      volumes:
        - name: s3-ca
          configMap: {name: {{ .ani.objectStorage.milvus_s3.ca_config_map }}}
JOB_EOF
"${KUBECTL[@]}" apply --server-side -f "$OUT_DIR/$job.yaml"
if ! "${KUBECTL[@]}" -n "$NS" wait "job/$job" --for=condition=complete --timeout=300s; then
  "${KUBECTL[@]}" -n "$NS" logs "job/$job" > "$OUT_DIR/$job.log" 2>&1 || true
  echo "S3 verification failed; job/$job retained for diagnosis" >&2
  exit 1
fi
"${KUBECTL[@]}" -n "$NS" logs "job/$job" > "$OUT_DIR/$job.log"
grep -qx 'ANI-S3-PUT-GET-OK' "$OUT_DIR/$job.log"
"${KUBECTL[@]}" -n "$NS" delete "job/$job" --wait=false >/dev/null
printf 'S3 write/read passed; evidence=%s\n' "$OUT_DIR/$job.log"

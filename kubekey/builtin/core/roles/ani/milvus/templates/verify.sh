#!/usr/bin/env bash
# B02 functional check; the packaged PyMilvus image executes the actual API.
set -euo pipefail
KUBECONFIG_FILE="${ANI_VERIFY_KUBECONFIG:?}"
test -f "$KUBECONFIG_FILE"
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_FILE")
NS=ani-platform
OUT_DIR="${ANI_VERIFY_OUTPUT_DIR:?}"
IMAGE='{{ index .ani.images "ani.local/milvus-checker:v1" }}'
mkdir -p "$OUT_DIR"
ready="$("${KUBECTL[@]}" -n "$NS" get deployment/ani-milvus-standalone -o jsonpath='{.status.readyReplicas}')"
[ "$ready" = 1 ] || { echo "Milvus Standalone is not ready" >&2; exit 1; }
ready="$("${KUBECTL[@]}" -n "$NS" get statefulset/ani-milvus-etcd -o jsonpath='{.status.readyReplicas}')"
[ "$ready" = 1 ] || { echo "dedicated Milvus etcd is not ready" >&2; exit 1; }
"${KUBECTL[@]}" -n "$NS" wait pvc/ani-milvus --for=jsonpath='{.status.phase}=Bound' --timeout=300s
"${KUBECTL[@]}" -n "$NS" wait pvc/data-ani-milvus-etcd-0 --for=jsonpath='{.status.phase}=Bound' --timeout=300s
job="ani-milvus-vector-$(date +%Y%m%d%H%M%S)-$$"
cat > "$OUT_DIR/$job.yaml" <<JOB_EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: $job
  namespace: $NS
  labels:
    app.kubernetes.io/name: ani-milvus-vector-verify
spec:
  backoffLimit: 0
  template:
    metadata:
      labels:
        app.kubernetes.io/name: ani-milvus-vector-verify
    spec:
      restartPolicy: Never
      containers:
        - name: client
          image: $IMAGE
          imagePullPolicy: IfNotPresent
          env:
            - name: MILVUS_URI
              value: http://ani-milvus.ani-platform.svc.cluster.local:19530
            - name: AWS_ACCESS_KEY_ID
              valueFrom:
                secretKeyRef: {name: ani-milvus-objects, key: AWS_ACCESS_KEY_ID}
            - name: AWS_SECRET_ACCESS_KEY
              valueFrom:
                secretKeyRef: {name: ani-milvus-objects, key: AWS_SECRET_ACCESS_KEY}
            - name: BUCKET_HOST
              valueFrom:
                configMapKeyRef: {name: ani-milvus-objects, key: BUCKET_HOST}
            - name: BUCKET_PORT
              valueFrom:
                configMapKeyRef: {name: ani-milvus-objects, key: BUCKET_PORT}
            - name: BUCKET_NAME
              valueFrom:
                configMapKeyRef: {name: ani-milvus-objects, key: BUCKET_NAME}
JOB_EOF
"${KUBECTL[@]}" apply --server-side -f "$OUT_DIR/$job.yaml"
if ! "${KUBECTL[@]}" -n "$NS" wait "job/$job" --for=condition=complete --timeout=900s; then
  "${KUBECTL[@]}" -n "$NS" logs "job/$job" > "$OUT_DIR/$job.log" 2>&1 || true
  echo "Milvus vector verification failed; job/$job retained for diagnosis" >&2
  exit 1
fi
"${KUBECTL[@]}" -n "$NS" logs "job/$job" > "$OUT_DIR/$job.log"
grep -q '^ANI-MILVUS-VECTOR-OK collection=.* nearest_id=101 new_rgws=[1-9][0-9]*$' "$OUT_DIR/$job.log"
"${KUBECTL[@]}" -n "$NS" delete "job/$job" --wait=false >/dev/null
printf 'Milvus vector verification passed; evidence=%s\n' "$OUT_DIR/$job.log"

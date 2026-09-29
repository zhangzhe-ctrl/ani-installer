#!/usr/bin/env bash
# B02 functional check; the packaged PyMilvus image executes the actual API.
set -euo pipefail
KUBECONFIG_FILE="${ANI_VERIFY_KUBECONFIG:?}"
test -f "$KUBECONFIG_FILE"
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_FILE")
NS=ani-platform
OUT_DIR="${ANI_VERIFY_OUTPUT_DIR:?}"
IMAGE='{{ index .ani.images "ani.local/milvus-checker:v1" }}'
CA_CONFIGMAP='{{ .ani.objectStorage.milvus_s3.ca_config_map }}'
CA_PATH='{{ .ani.objectStorage.milvus_s3.ca_path }}'
CHECKER=/etc/kubernetes/ani/milvus/vector-verify.py
mkdir -p "$OUT_DIR"
ready="$("${KUBECTL[@]}" -n "$NS" get deployment/ani-milvus-standalone -o jsonpath='{.status.readyReplicas}')"
[ "$ready" = 1 ] || { echo "Milvus Standalone is not ready" >&2; exit 1; }
ready="$("${KUBECTL[@]}" -n "$NS" get statefulset/ani-milvus-etcd -o jsonpath='{.status.readyReplicas}')"
[ "$ready" = 1 ] || { echo "dedicated Milvus etcd is not ready" >&2; exit 1; }
"${KUBECTL[@]}" -n "$NS" wait pvc/ani-milvus --for=jsonpath='{.status.phase}=Bound' --timeout=300s
"${KUBECTL[@]}" -n "$NS" wait pvc/data-ani-milvus-etcd-0 --for=jsonpath='{.status.phase}=Bound' --timeout=300s
# Read the running v2.6.24 process, not just Helm values or a CA file on disk.
pod="$("${KUBECTL[@]}" -n "$NS" get pods -l app.kubernetes.io/instance=ani-milvus,component=standalone -o jsonpath='{.items[0].metadata.name}')"
test -n "$pod"
image="$("${KUBECTL[@]}" -n "$NS" get pod "$pod" -o jsonpath='{.spec.containers[?(@.name=="standalone")].image}')"
[ "$image" = '{{ .ani.registry }}/milvusdb/milvus:v2.6.24' ] || { echo "unexpected Milvus process image: $image" >&2; exit 1; }
"${KUBECTL[@]}" -n "$NS" get configmap "$CA_CONFIGMAP" -o jsonpath='{.data.ca\.crt}' > "$OUT_DIR/b02-expected-s3-ca.crt"
expected_ca="$(sha256sum "$OUT_DIR/b02-expected-s3-ca.crt" | awk '{print $1}')"
actual_ca="$("${KUBECTL[@]}" -n "$NS" exec "$pod" -c standalone -- sha256sum "$CA_PATH" | awk '{print $1}')"
[ "$actual_ca" = "$expected_ca" ] || { echo 'Milvus mounted S3 CA differs from the selected ConfigMap' >&2; exit 1; }
"${KUBECTL[@]}" -n "$NS" exec "$pod" -c standalone -- sh -ec '
  tr "\000" "\n" < /proc/1/environ | grep -Fx "SSL_CERT_FILE={{ .ani.objectStorage.milvus_s3.ca_path }}"
  tr "\000" "\n" < /proc/1/environ | grep -Fx "AWS_CA_BUNDLE={{ .ani.objectStorage.milvus_s3.ca_path }}"
  grep -A4 "^  ssl:" /milvus/configs/user.yaml | grep -Fx "    tlsCACert: {{ .ani.objectStorage.milvus_s3.ca_path }}"
  test -s {{ .ani.objectStorage.milvus_s3.ca_path }}
' > "$OUT_DIR/b02-process-ca.log"
printf 'ANI-MILVUS-PROCESS-CA-OK pod=%s image=%s ca_sha256=%s\n' "$pod" "$image" "$expected_ca" | tee "$OUT_DIR/b02-process-result.txt"
test -s "$CHECKER" || { echo 'packaged vector checker script is absent' >&2; exit 1; }
check_name="ani-milvus-vector-$(sha256sum "$CHECKER" | cut -c1-12)"
"${KUBECTL[@]}" -n "$NS" create configmap "$check_name" --from-file=verify.py="$CHECKER" --dry-run=client -o yaml |
  "${KUBECTL[@]}" apply --server-side -f - >/dev/null
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
          command: ["python", "/opt/check/verify.py"]
          env:
            - name: MILVUS_URI
              value: http://ani-milvus.ani-platform.svc.cluster.local:19530
            - name: AWS_ACCESS_KEY_ID
              valueFrom:
                secretKeyRef: {name: {{ .ani.objectStorage.milvus_s3.secret_name }}, key: {{ .ani.objectStorage.milvus_s3.access_key_field }}}
            - name: AWS_SECRET_ACCESS_KEY
              valueFrom:
                secretKeyRef: {name: {{ .ani.objectStorage.milvus_s3.secret_name }}, key: {{ .ani.objectStorage.milvus_s3.secret_key_field }}}
            - name: S3_PROVIDER
              value: {{ .ani.objectStorage.milvus_s3.provider }}
            - name: S3_ENDPOINT
              value: {{ .ani.objectStorage.milvus_s3.endpoint }}
            - name: S3_BUCKET
              value: {{ .ani.objectStorage.milvus_s3.bucket }}
            - name: S3_ROOT_PATH
              value: {{ .ani.objectStorage.milvus_s3.root_path }}
            - name: S3_CA_PATH
              value: {{ .ani.objectStorage.milvus_s3.ca_path }}
            - name: AWS_DEFAULT_REGION
              value: {{ .ani.objectStorage.milvus_s3.region }}
            - name: AWS_EC2_METADATA_DISABLED
              value: "true"
          volumeMounts:
            - {name: check-code, mountPath: /opt/check, readOnly: true}
            - {name: s3-ca, mountPath: {{ .ani.objectStorage.milvus_s3.ca_mount_path }}, readOnly: true}
      volumes:
        - name: check-code
          configMap: {name: $check_name}
        - name: s3-ca
          configMap: {name: {{ .ani.objectStorage.milvus_s3.ca_config_map }}}
JOB_EOF
"${KUBECTL[@]}" apply --server-side -f "$OUT_DIR/$job.yaml"
if ! "${KUBECTL[@]}" -n "$NS" wait "job/$job" --for=condition=complete --timeout=900s; then
  "${KUBECTL[@]}" -n "$NS" logs "job/$job" > "$OUT_DIR/$job.log" 2>&1 || true
  echo "Milvus vector verification failed; job/$job retained for diagnosis" >&2
  exit 1
fi
"${KUBECTL[@]}" -n "$NS" logs "job/$job" > "$OUT_DIR/$job.log"
grep -q '^ANI-MILVUS-VECTOR-OK collection=.* nearest_id=101 new_objects=[1-9][0-9]* provider={{ .ani.objectStorage.milvus_s3.provider }}$' "$OUT_DIR/$job.log"
"${KUBECTL[@]}" -n "$NS" delete "job/$job" --wait=false >/dev/null
printf 'Milvus vector verification passed; evidence=%s\n' "$OUT_DIR/$job.log"

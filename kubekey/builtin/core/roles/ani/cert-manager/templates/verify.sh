#!/usr/bin/env bash
# cert-manager component verification, rendered by the ANI cert-manager role.
# Template source: builtin/core/roles/ani/cert-manager/templates/verify.sh
#
# Proves the running deployment actually issued a usable certificate chain: the
# Secret exists, the leaf's SAN matches the request, the chain validates against
# the internal root CA, and the CA/leaf attributes are correct. Only "controller
# is Ready" or "Certificate is Ready" is not enough, and no private key is ever
# printed or copied into an output file.
set -euo pipefail

KUBECONFIG_FILE="${ANI_VERIFY_KUBECONFIG:-/etc/kubernetes/admin.conf}"
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_FILE")
TOOL_IMAGE="{{ index .ani.images "docker.io/alpine/openssl:3.5.4" }}"

NS=cert-manager
TEST_NS=ani-cert-test
ROOT_SECRET=ani-root-ca
LEAF_SECRET=ani-ca-test-leaf
EXPECTED_DNS_NAMES=(ani-ca-test-leaf.ani-cert-test.svc ani-ca-test-leaf.ani-cert-test.svc.cluster.local)

RUN_ID="$(date +%Y%m%d%H%M%S)-$$"
JOB_NAME="ani-cert-verify-${RUN_ID}"
OUT_DIR="${ANI_VERIFY_OUTPUT_DIR:-/tmp/ani-cert-manager-verify-${RUN_ID}}"
mkdir -p "$OUT_DIR"
JOB_FILE="$OUT_DIR/job.yaml"

fail() { echo "FAIL: $*" >&2; exit 1; }

echo "[1/6] workloads"
for deploy in cert-manager cert-manager-webhook cert-manager-cainjector; do
  available="$("${KUBECTL[@]}" -n "$NS" get "deployment/$deploy" -o jsonpath='{.status.conditions[?(@.type=="Available")].status}')"
  [ "$available" = "True" ] || fail "deployment/$deploy is not Available (status=$available)"
  ready="$("${KUBECTL[@]}" -n "$NS" get "deployment/$deploy" -o jsonpath='{.status.readyReplicas}/{.status.replicas}')"
  echo "  deployment/$deploy available ready=$ready"
done

echo "[2/6] issuers and certificates"
"${KUBECTL[@]}" -n "$NS" get issuer/ani-ca-bootstrap >/dev/null || fail "issuer/ani-ca-bootstrap missing"
root_ready="$("${KUBECTL[@]}" -n "$NS" get certificate/ani-root-ca -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')"
[ "$root_ready" = "True" ] || fail "certificate/ani-root-ca is not Ready (status=$root_ready)"
leaf_ready="$("${KUBECTL[@]}" -n "$TEST_NS" get "certificate/$LEAF_SECRET" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')"
[ "$leaf_ready" = "True" ] || fail "certificate/$LEAF_SECRET is not Ready (status=$leaf_ready)"
cluster_ready="$("${KUBECTL[@]}" get clusterissuer/ani-ca -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')"
[ "$cluster_ready" = "True" ] || fail "clusterissuer/ani-ca is not Ready (status=$cluster_ready)"

echo "[3/6] secrets carry both key pairs"
# kubectl jsonpath needs the dot in "tls.crt" escaped, otherwise the field
# lookup silently returns nothing.
secret_field() {
  local namespace="$1" secret="$2" field="$3" escaped
  escaped="${field//./\\.}"
  "${KUBECTL[@]}" -n "$namespace" get "secret/$secret" -o "jsonpath={.data.$escaped}"
}

for field in tls.crt ca.crt tls.key; do
  value="$(secret_field "$NS" "$ROOT_SECRET" "$field")"
  [ -n "$value" ] || fail "secret/$ROOT_SECRET has no $field"
  echo "  secret/$ROOT_SECRET has $field"
done
for field in tls.crt ca.crt tls.key; do
  value="$(secret_field "$TEST_NS" "$LEAF_SECRET" "$field")"
  [ -n "$value" ] || fail "secret/$LEAF_SECRET has no $field"
  echo "  secret/$LEAF_SECRET has $field"
done

echo "[4/6] run the packaged certificate tool ($TOOL_IMAGE)"
cat > "$JOB_FILE" <<JOB_EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: $JOB_NAME
  namespace: $TEST_NS
  labels:
    app.kubernetes.io/name: ani-cert-manager-verify
spec:
  backoffLimit: 0
  completions: 1
  parallelism: 1
  template:
    metadata:
      labels:
        app.kubernetes.io/name: ani-cert-manager-verify
    spec:
      restartPolicy: Never
      containers:
        - name: cert-verify
          image: $TOOL_IMAGE
          imagePullPolicy: IfNotPresent
          command:
            - /bin/sh
            - -c
            - |
              set -eu
              leaf=/certs/leaf/tls.crt
              ca=/certs/leaf/ca.crt
              echo "-- openssl --"
              openssl version
              echo "-- verify chain --"
              openssl verify -CAfile "\$ca" "\$leaf"
              echo "-- leaf subject/issuer/dates --"
              openssl x509 -in "\$leaf" -noout -subject -issuer -dates -nameopt RFC2253
              openssl x509 -in "\$leaf" -noout -issuer -nameopt RFC2253 | grep -q "CN=ani-root-ca" || { echo "leaf issuer CN mismatch"; exit 1; }
              echo "-- leaf SAN --"
              openssl x509 -in "\$leaf" -noout -ext subjectAltName | tee /tmp/san.txt
              grep -q "DNS:ani-ca-test-leaf.ani-cert-test.svc" /tmp/san.txt || { echo "missing SAN svc"; exit 1; }
              grep -q "DNS:ani-ca-test-leaf.ani-cert-test.svc.cluster.local" /tmp/san.txt || { echo "missing SAN svc.cluster.local"; exit 1; }
              echo "-- basic constraints --"
              openssl x509 -in "\$leaf" -noout -text | grep -q "CA:FALSE" || { echo "leaf must not be a CA"; exit 1; }
              openssl x509 -in "\$ca" -noout -text | grep -q "CA:TRUE" || { echo "root must be a CA"; exit 1; }
              echo "-- root is our self-signed internal CA --"
              openssl x509 -in "\$ca" -noout -subject -nameopt RFC2253 | grep -q "CN=ani-root-ca" || { echo "root CN mismatch"; exit 1; }
              openssl x509 -in "\$ca" -noout -subject -nameopt RFC2253 | grep -q "O=ani-installer" || { echo "root organization mismatch"; exit 1; }
              subject=\$(openssl x509 -in "\$ca" -noout -subject -nameopt RFC2253 | sed 's/^subject=//')
              issuer=\$(openssl x509 -in "\$ca" -noout -issuer -nameopt RFC2253 | sed 's/^issuer=//')
              [ "\$subject" = "\$issuer" ] || { echo "root CA is not self-signed: \$subject vs \$issuer"; exit 1; }
              echo "-- validity --"
              openssl x509 -in "\$leaf" -noout -checkend 0 >/dev/null || { echo "leaf is not valid now"; exit 1; }
              openssl x509 -in "\$ca" -noout -checkend 0 >/dev/null || { echo "root CA is not valid now"; exit 1; }
              echo "ANI-CERT-MANAGER-VERIFY-OK"
          volumeMounts:
            - name: leaf
              mountPath: /certs/leaf
              readOnly: true
      volumes:
        - name: leaf
          secret:
            secretName: $LEAF_SECRET
JOB_EOF

"${KUBECTL[@]}" apply --server-side -f "$JOB_FILE" >/dev/null
if "${KUBECTL[@]}" -n "$TEST_NS" wait --for=condition=complete "job/$JOB_NAME" --timeout=300s >/dev/null 2>&1; then
  echo "  job/$JOB_NAME completed"
else
  echo "  job/$JOB_NAME did not complete; pod events and logs follow" >&2
  "${KUBECTL[@]}" -n "$TEST_NS" describe "job/$JOB_NAME" >"$OUT_DIR/job-describe.txt" 2>&1 || true
  "${KUBECTL[@]}" -n "$TEST_NS" logs "job/$JOB_NAME" >"$OUT_DIR/job-logs.txt" 2>&1 || true
  cat "$OUT_DIR/job-logs.txt" || true
  fail "certificate verification job failed; evidence retained in $OUT_DIR"
fi

echo "[5/6] verification output"
"${KUBECTL[@]}" -n "$TEST_NS" logs "job/$JOB_NAME" | tee "$OUT_DIR/openssl.log"
if ! grep -q '^ANI-CERT-MANAGER-VERIFY-OK$' "$OUT_DIR/openssl.log"; then
  fail "packaged certificate tool did not report success; see $OUT_DIR/openssl.log"
fi

echo "[6/6] SAN cross-check against the request"
for dns in "${EXPECTED_DNS_NAMES[@]}"; do
  grep -q "DNS:$dns" "$OUT_DIR/openssl.log" || fail "issued certificate is missing expected SAN $dns"
done

"${KUBECTL[@]}" -n "$TEST_NS" delete "job/$JOB_NAME" --wait=false >/dev/null 2>&1 || true
echo "cert-manager verification passed: chain=verified san=${#EXPECTED_DNS_NAMES[@]} evidence=$OUT_DIR"

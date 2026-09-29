#!/usr/bin/env bash
# Read-only, repeatable proof of the selected RustFS process and native TLS.
set -euo pipefail
KUBECONFIG_FILE="${ANI_VERIFY_KUBECONFIG:?}"
OUT_DIR="${ANI_VERIFY_OUTPUT_DIR:?}"
test -f "$KUBECONFIG_FILE"
install -d -m 0700 "$OUT_DIR"
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_FILE")
NS=ani-platform
"${KUBECTL[@]}" -n "$NS" rollout status deployment/ani-rustfs --timeout=60s
"${KUBECTL[@]}" -n "$NS" wait pvc/ani-rustfs-data --for=jsonpath='{.status.phase}=Bound' --timeout=60s
"${KUBECTL[@]}" -n "$NS" wait certificate/ani-rustfs-server --for=condition=Ready --timeout=60s
image="$("${KUBECTL[@]}" -n "$NS" get deployment/ani-rustfs -o jsonpath='{.spec.template.spec.containers[0].image}')"
[ "$image" = '{{ index .ani.images "docker.io/rustfs/rustfs:1.0.0" }}' ] || {
  echo "unexpected RustFS process image: $image" >&2; exit 1;
}
for probe in readinessProbe livenessProbe; do
  scheme="$("${KUBECTL[@]}" -n "$NS" get deployment/ani-rustfs -o "jsonpath={.spec.template.spec.containers[0].$probe.httpGet.scheme}")"
  [ "$scheme" = HTTPS ] || { echo "RustFS $probe is not HTTPS: $scheme" >&2; exit 1; }
done
for key in RUSTFS_ACCESS_KEY RUSTFS_SECRET_KEY; do
  [ -n "$("${KUBECTL[@]}" -n "$NS" get secret/ani-rustfs-root -o "jsonpath={.data.$key}")" ] || {
    echo "RustFS root identity lacks $key" >&2; exit 1;
  }
done
work="$(mktemp -d "$OUT_DIR/rustfs-verify.XXXXXX")"
pf_pid=""
cleanup() {
  if [ -n "$pf_pid" ]; then kill "$pf_pid" 2>/dev/null || true; wait "$pf_pid" 2>/dev/null || true; fi
  rm -rf "$work"
}
trap cleanup EXIT
"${KUBECTL[@]}" -n cert-manager get secret/ani-root-ca -o jsonpath='{.data.tls\.crt}' | base64 -d > "$work/root-ca.crt"
"${KUBECTL[@]}" -n "$NS" get configmap/ani-rustfs-ca -o jsonpath='{.data.ca\.crt}' > "$work/published-ca.crt"
cmp -s "$work/root-ca.crt" "$work/published-ca.crt" || { echo 'RustFS public CA differs from internal root' >&2; exit 1; }
"${KUBECTL[@]}" -n "$NS" port-forward service/ani-rustfs-svc :9000 --address 127.0.0.1 > "$work/port-forward.log" 2>&1 &
pf_pid=$!
port=""
for _ in $(seq 1 20); do
  if ! kill -0 "$pf_pid" 2>/dev/null; then cat "$work/port-forward.log" >&2; exit 1; fi
  port="$(sed -nE 's/.*Forwarding from 127\.0\.0\.1:([0-9]+) -> 9000.*/\1/p' "$work/port-forward.log" | head -1)"
  if [ -n "$port" ]; then break; fi
  sleep 1
done
[ -n "$port" ] || { echo 'RustFS port-forward did not become ready' >&2; exit 1; }
curl --fail --silent --show-error --max-time 20 --cacert "$work/root-ca.crt" "https://127.0.0.1:$port/health/ready" > "$work/health-response"
printf 'ANI-RUSTFS-HTTPS-READY-OK image=%s ca_sha256=%s\n' "$image" "$(sha256sum "$work/root-ca.crt" | awk '{print $1}')" | tee "$OUT_DIR/rustfs-https-result.txt"

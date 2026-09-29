#!/usr/bin/env bash
# Reuse the RustFS root identity and publish the existing internal CA.
# Every existing resource is checked before the first write.
set -euo pipefail
KUBECONFIG_FILE="${ANI_RUN_KUBECONFIG:?}"
test -f "$KUBECONFIG_FILE"
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_FILE")
NS=ani-platform
OWNER='{{ .kubernetes.cluster_name }}'
WORK=/etc/kubernetes/ani/rustfs
install -d -m 0700 "$WORK"
tmp="$(mktemp -d "$WORK/bootstrap.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT
umask 077

for ref in secret/ani-rustfs-root configmap/ani-rustfs-ca certificate/ani-rustfs-server \
           deployment/ani-rustfs service/ani-rustfs-svc pvc/ani-rustfs-data; do
  if [ -n "$("${KUBECTL[@]}" -n "$NS" get "$ref" --ignore-not-found -o name)" ]; then
    owner="$("${KUBECTL[@]}" -n "$NS" get "$ref" -o jsonpath='{.metadata.labels.ani\.io/managed-by}')"
    [ "$owner" = "$OWNER" ] || { echo "foreign $NS/$ref owner=$owner" >&2; exit 1; }
  fi
done
"${KUBECTL[@]}" wait clusterissuer/ani-ca --for=condition=Ready --timeout=60s
"${KUBECTL[@]}" -n cert-manager get secret/ani-root-ca -o jsonpath='{.data.tls\.crt}' | base64 -d > "$tmp/ca.crt"
test -s "$tmp/ca.crt" || { echo 'the internal CA has no certificate' >&2; exit 1; }

if [ -n "$("${KUBECTL[@]}" -n "$NS" get configmap/ani-rustfs-ca --ignore-not-found -o name)" ]; then
  "${KUBECTL[@]}" -n "$NS" get configmap/ani-rustfs-ca -o jsonpath='{.data.ca\.crt}' > "$tmp/existing-ca.crt"
  cmp -s "$tmp/ca.crt" "$tmp/existing-ca.crt" || { echo 'RustFS CA ConfigMap differs from the fixed internal CA' >&2; exit 1; }
else
  {
    printf 'apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: ani-rustfs-ca\n  namespace: %s\n  labels:\n    ani.io/managed-by: %s\ndata:\n  ca.crt: |\n' "$NS" "$OWNER"
    sed 's/^/    /' "$tmp/ca.crt"
  } > "$tmp/ca.yaml"
  "${KUBECTL[@]}" create -f "$tmp/ca.yaml" >/dev/null
fi

if [ -n "$("${KUBECTL[@]}" -n "$NS" get secret/ani-rustfs-root --ignore-not-found -o name)" ]; then
  type="$("${KUBECTL[@]}" -n "$NS" get secret/ani-rustfs-root -o jsonpath='{.type}')"
  [ "$type" = Opaque ] || { echo 'RustFS root Secret has an unexpected type' >&2; exit 1; }
  for key in RUSTFS_ACCESS_KEY RUSTFS_SECRET_KEY; do
    value="$("${KUBECTL[@]}" -n "$NS" get secret/ani-rustfs-root -o "jsonpath={.data.$key}")"
    [ -n "$value" ] || { echo "RustFS root Secret lacks $key" >&2; exit 1; }
  done
else
  if [ -n "$("${KUBECTL[@]}" -n "$NS" get deployment/ani-rustfs --ignore-not-found -o name)" ]; then
    echo 'RustFS workload exists without its root identity; refusing to replace credentials' >&2
    exit 1
  fi
  od -An -tx1 -N16 /dev/urandom | tr -d ' \n' > "$tmp/access"
  od -An -tx1 -N32 /dev/urandom | tr -d ' \n' > "$tmp/secret"
  {
    printf 'apiVersion: v1\nkind: Secret\nmetadata:\n  name: ani-rustfs-root\n  namespace: %s\n  labels:\n    ani.io/managed-by: %s\ntype: Opaque\ndata:\n' "$NS" "$OWNER"
    printf '  RUSTFS_ACCESS_KEY: %s\n' "$(base64 -w0 "$tmp/access")"
    printf '  RUSTFS_SECRET_KEY: %s\n' "$(base64 -w0 "$tmp/secret")"
  } > "$tmp/root.yaml"
  "${KUBECTL[@]}" create -f "$tmp/root.yaml" >/dev/null
fi
printf 'RustFS root identity and internal CA ready; existing credentials were retained\n'

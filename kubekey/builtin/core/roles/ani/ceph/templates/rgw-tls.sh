#!/usr/bin/env bash
# Issue a private, cluster-local RGW server certificate before creating the
# ObjectStore. Existing owned material is verified and never silently rotated.
set -euo pipefail
export KUBECONFIG='{{ .ani.run.kubeconfig }}'
owner='{{ .kubernetes.cluster_name }}'
name=ani-rgw-server-tls
work=/etc/kubernetes/ani/ceph/rgw-tls
install -d -m 0700 "$work"
umask 077
existing="$(kubectl -n rook-ceph get secret "$name" --ignore-not-found -o name)"
if [ -n "$existing" ]; then
  python3 - "$work" "$owner" "$name" <<'PY'
import base64,json,subprocess,sys
from pathlib import Path
work,owner,name=Path(sys.argv[1]),sys.argv[2],sys.argv[3]
x=json.loads(subprocess.check_output(['kubectl','-n','rook-ceph','get','secret',name,'-o','json']))
assert x['metadata'].get('labels',{}).get('ani.io/managed-by')==owner,'foreign RGW TLS secret'
assert x.get('type')=='kubernetes.io/tls','unexpected RGW TLS secret type'
for key in ('tls.crt','tls.key','ca.crt'):
    value=x.get('data',{}).get(key)
    assert value, f'RGW TLS secret missing {key}'
    (work/key).write_bytes(base64.b64decode(value,validate=True))
PY
else
  for f in ca.crt ca.key tls.crt tls.key leaf.csr leaf.crt; do
    test ! -e "$work/$f" || { echo "partial local RGW TLS material exists: $f" >&2; exit 1; }
  done
  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:3072 -out "$work/ca.key" >/dev/null 2>&1
  openssl req -x509 -new -sha256 -days 3650 -key "$work/ca.key" \
    -subj '/CN=ANI RGW internal CA' \
    -addext 'basicConstraints=critical,CA:TRUE' \
    -addext 'keyUsage=critical,keyCertSign,cRLSign' \
    -out "$work/ca.crt" >/dev/null 2>&1
  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:3072 -out "$work/tls.key" >/dev/null 2>&1
  openssl req -new -sha256 -key "$work/tls.key" \
    -subj '/CN=rook-ceph-rgw-ani-store.rook-ceph.svc.cluster.local' \
    -out "$work/leaf.csr" >/dev/null 2>&1
  printf '%s\n' \
    'basicConstraints=critical,CA:FALSE' \
    'keyUsage=critical,digitalSignature,keyEncipherment' \
    'extendedKeyUsage=serverAuth' \
    'subjectAltName=DNS:rook-ceph-rgw-ani-store.rook-ceph.svc,DNS:rook-ceph-rgw-ani-store.rook-ceph.svc.cluster.local' > "$work/leaf.ext"
  openssl x509 -req -sha256 -days 825 -in "$work/leaf.csr" \
    -CA "$work/ca.crt" -CAkey "$work/ca.key" -CAcreateserial \
    -extfile "$work/leaf.ext" -out "$work/leaf.crt" >/dev/null 2>&1
  cat "$work/leaf.crt" "$work/ca.crt" > "$work/tls.crt"
fi
openssl verify -CAfile "$work/ca.crt" "$work/tls.crt" >/dev/null
openssl x509 -in "$work/tls.crt" -noout -checkend 86400 >/dev/null
openssl x509 -in "$work/tls.crt" -noout -ext subjectAltName |
  grep -Fq 'DNS:rook-ceph-rgw-ani-store.rook-ceph.svc.cluster.local'
test "$(openssl pkey -in "$work/tls.key" -pubout 2>/dev/null | sha256sum | cut -d' ' -f1)" = \
     "$(openssl x509 -in "$work/tls.crt" -pubkey -noout | sha256sum | cut -d' ' -f1)"
if [ -z "$existing" ]; then
  kubectl -n rook-ceph create secret generic "$name" --type=kubernetes.io/tls \
    --from-file=tls.crt="$work/tls.crt" --from-file=tls.key="$work/tls.key" \
    --from-file=ca.crt="$work/ca.crt" --dry-run=client -o json |
    python3 -c 'import json,sys; x=json.load(sys.stdin); x["metadata"].setdefault("labels",{})["ani.io/managed-by"]=sys.argv[1]; json.dump(x,sys.stdout)' "$owner" |
    kubectl apply --server-side -f -
fi
printf 'owned RGW TLS certificate verified for service DNS; secret=%s\n' "$name"

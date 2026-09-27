#!/usr/bin/env bash
set -euo pipefail
k() { kubectl --kubeconfig '{{ .ani.run.kubeconfig }}' "$@"; }
DIR=/etc/kubernetes/ani/harbor
ADDR='{{ (index .ani.components "harbor").external_address }}'
umask 077
for name in ca.key ca.crt server.key server.csr server.crt admin-password db-password registry-password; do
  test ! -e "$DIR/$name" || { echo "existing Harbor PKI/credential $name; refusing rotation" >&2; exit 1; }
done
command -v openssl >/dev/null
openssl genrsa -out "$DIR/ca.key" 3072
openssl req -x509 -new -sha256 -days 3650 -key "$DIR/ca.key" \
  -subj '/CN=ANI Harbor Offline CA' -out "$DIR/ca.crt"
openssl genrsa -out "$DIR/server.key" 3072
openssl req -new -sha256 -key "$DIR/server.key" -subj "/CN=$ADDR" -out "$DIR/server.csr"
cat > "$DIR/server-ext.conf" <<EXT
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=IP:$ADDR
EXT
openssl x509 -req -sha256 -days 825 -in "$DIR/server.csr" \
  -CA "$DIR/ca.crt" -CAkey "$DIR/ca.key" -CAcreateserial \
  -extfile "$DIR/server-ext.conf" -out "$DIR/server.crt"
openssl verify -CAfile "$DIR/ca.crt" -verify_ip "$ADDR" "$DIR/server.crt"
openssl rand -hex 48 | tr -d '\n' > "$DIR/admin-password"
openssl rand -hex 48 | tr -d '\n' > "$DIR/db-password"
openssl rand -hex 48 | tr -d '\n' > "$DIR/registry-password"
chmod 0600 "$DIR"/*.key "$DIR"/*-password
k create namespace ani-harbor --dry-run=client -o yaml | k apply --server-side -f -
k -n ani-harbor create secret tls ani-harbor-tls \
  --cert="$DIR/server.crt" --key="$DIR/server.key" --dry-run=client -o yaml | k apply --server-side -f -
k -n ani-harbor create secret generic ani-harbor-admin \
  --from-file=HARBOR_ADMIN_PASSWORD="$DIR/admin-password" --dry-run=client -o yaml | k apply --server-side -f -
echo "Harbor CA and server certificate generated for $ADDR, namespace and TLS/admin secrets created"

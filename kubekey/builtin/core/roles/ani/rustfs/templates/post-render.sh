#!/usr/bin/env bash
# The official 1.0.0 Chart ignores the values probe scheme for native TLS.
# Transform only the two known HTTP probe schemes in its pinned render.
set -euo pipefail
umask 077
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
cat > "$tmp"
count="$(grep -Ec '^[[:space:]]+scheme: HTTP$' "$tmp" || true)"
if [ "$count" -ne 2 ]; then
  echo "RustFS Chart probe layout changed: expected exactly two HTTP schemes, got $count" >&2
  exit 1
fi
grep -q '^kind: Deployment$' "$tmp" || { echo 'RustFS Chart has no Deployment' >&2; exit 1; }
grep -q '^  name: ani-rustfs$' "$tmp" || { echo 'RustFS Chart Deployment identity changed' >&2; exit 1; }
sed -E 's/^([[:space:]]+scheme: )HTTP$/\1HTTPS/' "$tmp"

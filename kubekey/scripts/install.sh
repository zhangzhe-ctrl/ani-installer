#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG="${1:-$PWD/cluster.yaml}"

if [[ ! -f "$CONFIG" ]]; then
  echo "site config not found: $CONFIG" >&2
  exit 1
fi
CONFIG="$(cd "$(dirname "$CONFIG")" && pwd)/$(basename "$CONFIG")"

if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
  exec sudo -E "$ROOT/bin/kk" ani install --package-root "$ROOT" --config "$CONFIG"
fi
exec "$ROOT/bin/kk" ani install --package-root "$ROOT" --config "$CONFIG"

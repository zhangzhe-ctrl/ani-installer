#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG="${1:?usage: install.sh CONFIG ARTIFACT_ROOT}"
ARTIFACT_ROOT="${2:?usage: install.sh CONFIG ARTIFACT_ROOT}"

CONFIG="$(cd "$(dirname "$CONFIG")" && pwd)/$(basename "$CONFIG")"
ARTIFACT_ROOT="$(cd "$ARTIFACT_ROOT" && pwd)"
KK="$ROOT/kk"

if [[ ! -f "$CONFIG" ]]; then
  echo "site config not found: $CONFIG" >&2
  exit 1
fi
if [[ ! -d "$ARTIFACT_ROOT" ]]; then
  echo "artifact root not found: $ARTIFACT_ROOT" >&2
  exit 1
fi
if [[ ! -x "$KK" ]]; then
  echo "kk is not executable: $KK" >&2
  exit 1
fi

if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
  exec sudo -E "$KK" ani install --config "$CONFIG" --package-root "$ARTIFACT_ROOT"
fi
exec "$KK" ani install --config "$CONFIG" --package-root "$ARTIFACT_ROOT"
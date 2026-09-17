#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT="${ANI_CODE_OUT:-$ROOT/build/ani-code-$(date +%Y%m%d-%H%M%S)}"
GO_BIN="${GO_BIN:-$(command -v go)}"

if [[ "$(uname -s)/$(uname -m)" != "Linux/x86_64" ]]; then
  echo "build-code.sh must run on Linux amd64" >&2
  exit 1
fi
if [[ -e "$OUTPUT" ]]; then
  echo "code output already exists; use a new ANI_CODE_OUT: $OUTPUT" >&2
  exit 1
fi
if [[ ! -x "$GO_BIN" ]]; then
  echo "go binary is not executable: $GO_BIN" >&2
  exit 1
fi

OUTPUT="$(mkdir -p "$(dirname "$OUTPUT")" && cd "$(dirname "$OUTPUT")" && pwd)/$(basename "$OUTPUT")"
mkdir -p "$OUTPUT"

echo "[1/3] building kk with builtin assets"
PATH="$(dirname "$GO_BIN"):$PATH" make -C "$ROOT" build-kk-dev
KK="$ROOT/_output/bin/kk"
if [[ ! -s "$KK" ]]; then
  echo "make did not produce $KK" >&2
  exit 1
fi

echo "[2/3] copying the code release"
install -m 0755 "$KK" "$OUTPUT/kk"
install -m 0755 "$ROOT/scripts/install.sh" "$OUTPUT/install.sh"
install -m 0755 "$ROOT/scripts/verify.sh" "$OUTPUT/verify.sh"
install -m 0755 "$ROOT/builtin/core/roles/ani/smoke/templates/probe.sh" "$OUTPUT/probe.sh"

echo "[3/3] generating code checksums"
(
  cd "$OUTPUT"
  find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS
)
for forbidden in hauler images packages repository manifests; do
  if [[ -e "$OUTPUT/$forbidden" ]]; then
    echo "code output must not contain artifact file or directory: $OUTPUT/$forbidden" >&2
    exit 1
  fi
done
echo "code release complete at $OUTPUT"
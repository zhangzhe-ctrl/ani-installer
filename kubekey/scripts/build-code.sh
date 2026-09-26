#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# 共享发布体检：凭据/lab 脚本不得进入发布物；可只体检不构建。
source "$ROOT/scripts/release-guard.sh"
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

# The code gate runs before the output directory exists, so a failing gate can
# never leave a partial release behind. The tree fingerprint is taken before and
# after the gate: a release must be built from exactly the tree that was tested,
# and HEAD alone is not a voucher for file contents.
fingerprint_before="$(ani_tree_fingerprint "$ROOT")"
echo "[1/4] code gate (same entry as .github/workflows/ani-check.yaml)"
bash "$ROOT/scripts/check-code.sh"
fingerprint_after_gate="$(ani_tree_fingerprint "$ROOT")"
if [[ "$fingerprint_before" != "$fingerprint_after_gate" ]]; then
  echo "source tree changed while the code gate ran; refusing to build" >&2
  echo "  before=$fingerprint_before" >&2
  echo "  after =$fingerprint_after_gate" >&2
  exit 1
fi

OUTPUT="$(mkdir -p "$(dirname "$OUTPUT")" && cd "$(dirname "$OUTPUT")" && pwd)/$(basename "$OUTPUT")"
mkdir -p "$OUTPUT"

echo "[2/4] building kk with builtin assets"
# Embed the frozen source-tree fingerprint so the install-success record can
# carry a real code identity (F04). It equals fingerprint_before only if the
# tree is unchanged across the build, which is verified immediately below.
export ANI_SOURCE_TREE_FINGERPRINT="$fingerprint_before"
PATH="$(dirname "$GO_BIN"):$PATH" make -C "$ROOT" build-kk-dev
KK="$ROOT/_output/bin/kk"
if [[ ! -s "$KK" ]]; then
  echo "make did not produce $KK" >&2
  exit 1
fi

fingerprint_after_build="$(ani_tree_fingerprint "$ROOT")"
if [[ "$fingerprint_before" != "$fingerprint_after_build" ]]; then
  echo "source tree changed during the build; refusing to package it" >&2
  echo "  before=$fingerprint_before" >&2
  echo "  after =$fingerprint_after_build" >&2
  exit 1
fi

echo "[3/4] copying the code release"
install -m 0755 "$KK" "$OUTPUT/kk"
install -m 0755 "$ROOT/scripts/install.sh" "$OUTPUT/install.sh"
install -m 0755 "$ROOT/scripts/verify.sh" "$OUTPUT/verify.sh"
install -m 0755 "$ROOT/builtin/core/roles/ani/smoke/templates/probe.sh" "$OUTPUT/probe.sh"
# R11: the stack-independent generic network smoke checker ships beside probe.sh.
install -m 0755 "$ROOT/builtin/core/roles/ani/smoke/templates/network-probe.sh" "$OUTPUT/network-probe.sh"

echo "[4/4] generating code checksums"
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
ani_assert_release_clean "$OUTPUT" || exit 1

# Last check: packaging must not have touched the source tree either.
fingerprint_after_package="$(ani_tree_fingerprint "$ROOT")"
if [[ "$fingerprint_before" != "$fingerprint_after_package" ]]; then
  echo "source tree changed while the release was packaged; the output is not trustworthy" >&2
  echo "  before=$fingerprint_before" >&2
  echo "  after =$fingerprint_after_package" >&2
  exit 1
fi
echo "code release complete at $OUTPUT"
echo "source tree $fingerprint_before (unchanged across gate, build and packaging)"
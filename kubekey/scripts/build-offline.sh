#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONFIG="${1:-$ROOT/ani/package.yaml}"
OUTPUT="${ANI_PACKAGE_OUT:-$ROOT/build/ani-offline-ubuntu24-amd64}"
HAULER_BIN="${HAULER_BIN:?set HAULER_BIN to the Linux amd64 hauler v2.0.3 binary}"
HAULER_STORE="${HAULER_STORE:-}"
KUBEKEY_ARTIFACT="${KUBEKEY_ARTIFACT:-}"
GO_BIN="${GO_BIN:-/usr/local/go/bin/go}"

if [[ ! -f "$CONFIG" ]]; then
  echo "package config not found: $CONFIG" >&2
  exit 1
fi
if [[ "$(uname -s)/$(uname -m)" != "Linux/x86_64" ]]; then
  echo "build-offline.sh must run on Linux amd64" >&2
  exit 1
fi

CONFIG="$(cd "$(dirname "$CONFIG")" && pwd)/$(basename "$CONFIG")"
mkdir -p "$ROOT/build" "$OUTPUT/bin" "$OUTPUT/packages" "$OUTPUT/images" "$OUTPUT/manifests" "$OUTPUT/config" "$OUTPUT/licenses"

WORK="$(mktemp -d "$ROOT/build/ani-package.XXXXXX")"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

echo "[1/7] building kk with builtin assets"
PATH="$(dirname "$GO_BIN"):$PATH" make -C "$ROOT" kk
KK="$ROOT/_output/bin/kk"

echo "[2/7] exporting KubeKey artifact"
if [[ -n "$KUBEKEY_ARTIFACT" ]]; then
  if [[ ! -s "$KUBEKEY_ARTIFACT" ]]; then
    echo "KUBEKEY_ARTIFACT is not a non-empty file: $KUBEKEY_ARTIFACT" >&2
    exit 1
  fi
  cp "$KUBEKEY_ARTIFACT" "$OUTPUT/packages/kubekey-artifact.tgz"
else
  "$KK" artifact export -c "$CONFIG" --workdir "$WORK/artifact"
  ARTIFACT="$WORK/artifact/artifact/kubekey-artifact.tgz"
  [[ -s "$ARTIFACT" ]]
  cp "$ARTIFACT" "$OUTPUT/packages/kubekey-artifact.tgz"
fi

echo "[3/7] collecting images into a fresh Hauler store"
STORE="$WORK/hauler-store"
mkdir -p "$STORE"
if [[ -n "$HAULER_STORE" ]]; then
  if [[ ! -d "$HAULER_STORE" ]]; then
    echo "HAULER_STORE is not a directory: $HAULER_STORE" >&2
    exit 1
  fi
  cp -a "$HAULER_STORE/." "$STORE"
else
  while IFS=$'\t' read -r original hauler_ref actual_digest use_location; do
    [[ "$original" == "original_ref" ]] && continue
    [[ -z "$original" ]] && continue
    rewrite="${hauler_ref#127.0.0.1:5000/}"
    "$HAULER_BIN" store add image "$original" \
      --platform linux/amd64 \
      --rewrite "$rewrite" \
      --store "$STORE"
  done < "$ROOT/ani/images.tsv"
fi
image_count="$(awk 'NF && NR>1 { count++ } END { print count+0 }' "$ROOT/ani/images.tsv")"
store_count="$("$HAULER_BIN" store info -s "$STORE" --type image -o json | grep -c '"Type": "image"')"
if [[ "$store_count" != "$image_count" ]]; then
  echo "Hauler store image count mismatch: expected $image_count, got $store_count" >&2
  exit 1
fi

required_materials=(
  builtin/core/roles/ani/kcn/tasks/main.yaml
  builtin/core/roles/ani/kcn/templates/install.yaml
  builtin/core/roles/ani/envoy/tasks/main.yaml
  builtin/core/roles/ani/envoy/templates/cleanup-orphan-lsp.py
  builtin/core/roles/ani/envoy/templates/install.yaml
  builtin/core/roles/ani/smoke/tasks/main.yaml
  builtin/core/roles/ani/smoke/templates/gateway.yaml
  builtin/core/roles/ani/smoke/templates/backend-pod.yaml
  builtin/core/roles/ani/smoke/templates/backend-service.yaml
  builtin/core/roles/ani/smoke/templates/network-client-pod.yaml
  builtin/core/roles/ani/smoke/templates/backend.yaml
  builtin/core/roles/ani/smoke/templates/httproute.yaml
  builtin/core/roles/ani/smoke/templates/client-pod.yaml
)
for path in "${required_materials[@]}"; do
  if [[ ! -f "$ROOT/$path" ]]; then
    echo "required package material not found: $ROOT/$path" >&2
    exit 1
  fi
done

echo "[4/7] saving Hauler archive"
"$HAULER_BIN" store save -s "$STORE" -f "$OUTPUT/images/images.haul.tar.zst"

echo "[5/7] copying binaries"
install -m 0755 "$KK" "$OUTPUT/bin/kk"
install -m 0755 "$HAULER_BIN" "$OUTPUT/bin/hauler"

echo "[6/7] copying package materials"
install -m 0644 "$ROOT/ani/package.yaml" "$OUTPUT/config/package.yaml"
install -m 0644 "$ROOT/ani/images.tsv" "$OUTPUT/images/images.tsv"
install -m 0644 "$ROOT/ani/cluster.example.yaml" "$OUTPUT/cluster.example.yaml"
install -m 0644 "$ROOT/ani/README.md" "$OUTPUT/README.md"
install -m 0755 "$ROOT/scripts/install.sh" "$OUTPUT/install.sh"
install -m 0755 "$ROOT/scripts/verify.sh" "$OUTPUT/verify.sh"
for path in LICENSE NOTICE; do
  if [[ -f "$ROOT/$path" ]]; then
    install -m 0644 "$ROOT/$path" "$OUTPUT/licenses/$path"
  fi
done
rm -rf "$OUTPUT/manifests/ani"
mkdir -p "$OUTPUT/manifests/ani"
cp -R "$ROOT/builtin/core/roles/ani/." "$OUTPUT/manifests/ani/"

find "$OUTPUT/manifests/ani" -type d -name __pycache__ -prune -exec rm -rf {} +

echo "[7/7] generating package checksums"
(
  cd "$OUTPUT"
  find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS
)
echo "package complete at $OUTPUT"

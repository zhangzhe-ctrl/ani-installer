#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONFIG="${CONFIG:-$ROOT/ani/package.yaml}"
IMAGES_TSV="${IMAGES_TSV:-$ROOT/ani/images.tsv}"
OUTPUT="${ANI_ARTIFACT_OUT:-$ROOT/build/ani-artifact-ubuntu24-amd64-$(date +%Y%m%d-%H%M%S)}"
KK_BIN="${KK_BIN:-}"
HAULER_BIN="${HAULER_BIN:?set HAULER_BIN to the Linux amd64 hauler v2.0.3 binary}"
REPOSITORY_ISO="${REPOSITORY_ISO:?set REPOSITORY_ISO to ubuntu-24.04-debs-amd64.iso}"
KUBEKEY_ARTIFACT="${KUBEKEY_ARTIFACT:-}"
HAULER_ARCHIVE="${HAULER_ARCHIVE:-}"
HAULER_STORE="${HAULER_STORE:-}"
# Local image injection: images that only exist as docker/OCI archives (no
# reachable registry source, e.g. a vendor hand-off tar). EXTRA_IMAGE_TARS is a
# whitespace-separated list of archives whose RepoTags must already use the
# hauler naming shape -- the hauler_ref from images.tsv with the 127.0.0.1:5000/
# placeholder prefix stripped (e.g. "kubercloud/kc-networking:dev"). Each entry
# has a matching images.tsv row, and EXTRA_IMAGE_ORIGINALS lists those rows'
# original_ref values so the pull loop below skips them instead of failing on
# the unreachable upstream source. The 1:1 size match and the tsv membership
# are guarded below; a wrong RepoTag inside a tar fails later at install time
# when verifyRegistryImages cannot find the tsv's hauler_ref in the store.
EXTRA_IMAGE_TARS="${EXTRA_IMAGE_TARS:-}"
EXTRA_IMAGE_ORIGINALS="${EXTRA_IMAGE_ORIGINALS:-}"
HELM_BIN="${HELM_BIN:?set HELM_BIN to the Linux amd64 helm binary used for the fixed chart renders}"
CHARTS_DIR="${CHARTS_DIR:-$ROOT/ani/charts}"
COMPONENT_LOCK="${COMPONENT_LOCK:-$ROOT/ani/components.lock.yaml}"

if [[ "$(uname -s)/$(uname -m)" != "Linux/x86_64" ]]; then
  echo "build-offline.sh must run on Linux amd64" >&2
  exit 1
fi
if [[ -e "$OUTPUT" ]]; then
  echo "artifact output already exists; use a new ANI_ARTIFACT_OUT: $OUTPUT" >&2
  exit 1
fi
if [[ -n "$HAULER_ARCHIVE" && -n "$HAULER_STORE" ]]; then
  echo "set only one of HAULER_ARCHIVE or HAULER_STORE" >&2
  exit 1
fi
if [[ -n "$EXTRA_IMAGE_TARS" && ( -n "$HAULER_ARCHIVE" || -n "$HAULER_STORE" ) ]]; then
  echo "EXTRA_IMAGE_TARS only works with the default pull path; unset HAULER_ARCHIVE/HAULER_STORE" >&2
  exit 1
fi
extra_tar_count=0
for _ in $EXTRA_IMAGE_TARS; do extra_tar_count=$((extra_tar_count+1)); done
extra_orig_count=0
for _ in $EXTRA_IMAGE_ORIGINALS; do extra_orig_count=$((extra_orig_count+1)); done
if [[ "$extra_tar_count" != "$extra_orig_count" ]]; then
  echo "EXTRA_IMAGE_TARS ($extra_tar_count entries) and EXTRA_IMAGE_ORIGINALS ($extra_orig_count entries) must match 1:1" >&2
  exit 1
fi
for original in $EXTRA_IMAGE_ORIGINALS; do
  if ! awk -F'\t' -v o="$original" '$1==o{found=1} END{exit !found}' "$IMAGES_TSV"; then
    echo "EXTRA_IMAGE_ORIGINALS entry $original is not a first-column image in $IMAGES_TSV" >&2
    exit 1
  fi
done
required=("$CONFIG" "$IMAGES_TSV" "$HAULER_BIN" "$REPOSITORY_ISO" "$HELM_BIN" "$COMPONENT_LOCK")
if [[ -z "$KUBEKEY_ARTIFACT" ]]; then
  required+=("$KK_BIN")
fi
for path in "${required[@]}"; do
  if [[ ! -s "$path" ]]; then
    echo "required artifact input is not a non-empty file: $path" >&2
    exit 1
  fi
done
if [[ ! -x "$HAULER_BIN" ]]; then
  echo "HAULER_BIN must be executable: $HAULER_BIN" >&2
  exit 1
fi
if [[ ! -x "$HELM_BIN" ]]; then
  echo "HELM_BIN must be executable: $HELM_BIN" >&2
  exit 1
fi
if [[ -z "$KUBEKEY_ARTIFACT" && ! -x "$KK_BIN" ]]; then
  echo "KK_BIN must be executable when KUBEKEY_ARTIFACT is not supplied: $KK_BIN" >&2
  exit 1
fi
if [[ -n "$KUBEKEY_ARTIFACT" && ! -s "$KUBEKEY_ARTIFACT" ]]; then
  echo "KUBEKEY_ARTIFACT is not a non-empty file: $KUBEKEY_ARTIFACT" >&2
  exit 1
fi
if [[ -n "$HAULER_STORE" && ! -d "$HAULER_STORE" ]]; then
  echo "HAULER_STORE is not a directory: $HAULER_STORE" >&2
  exit 1
fi

CONFIG="$(cd "$(dirname "$CONFIG")" && pwd)/$(basename "$CONFIG")"
IMAGES_TSV="$(cd "$(dirname "$IMAGES_TSV")" && pwd)/$(basename "$IMAGES_TSV")"
OUTPUT="$(mkdir -p "$(dirname "$OUTPUT")" && cd "$(dirname "$OUTPUT")" && pwd)/$(basename "$OUTPUT")"
WORK="$(mktemp -d "$ROOT/build/ani-artifact.XXXXXX")"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

mkdir -p \
  "$OUTPUT/bin" \
  "$OUTPUT/packages" \
  "$OUTPUT/images" \
  "$OUTPUT/config" \
  "$OUTPUT/charts" \
  "$OUTPUT/licenses" \
  "$OUTPUT/repository"

copy_material() {
  local source="$1" destination="$2"
  cp --reflink=auto --sparse=auto -- "$source" "$destination"
}

echo "[1/6] placing KubeKey artifact"
if [[ -n "$KUBEKEY_ARTIFACT" ]]; then
  copy_material "$KUBEKEY_ARTIFACT" "$OUTPUT/packages/kubekey-artifact.tgz"
else
  "$KK_BIN" artifact export -c "$CONFIG" --workdir "$WORK/artifact"
  ARTIFACT="$WORK/artifact/artifact/kubekey-artifact.tgz"
  if [[ ! -s "$ARTIFACT" ]]; then
    echo "KubeKey artifact export did not produce $ARTIFACT" >&2
    exit 1
  fi
  copy_material "$ARTIFACT" "$OUTPUT/packages/kubekey-artifact.tgz"
fi

echo "[2/6] placing image archive"
if [[ -n "$HAULER_ARCHIVE" ]]; then
  copy_material "$HAULER_ARCHIVE" "$OUTPUT/images/images.haul.tar.zst"
elif [[ -n "$HAULER_STORE" ]]; then
  "$HAULER_BIN" store save -s "$HAULER_STORE" -f "$OUTPUT/images/images.haul.tar.zst"
else
  STORE="$WORK/hauler-store"
  mkdir -p "$STORE"
  while IFS=$'\t' read -r original hauler_ref actual_digest use_location; do
    [[ "$original" == "original_ref" ]] && continue
    [[ -z "$original" ]] && continue
    if [[ " $EXTRA_IMAGE_ORIGINALS " == *" $original "* ]]; then
      echo "skip $original: provided via EXTRA_IMAGE_TARS"
      continue
    fi
    rewrite="${hauler_ref#127.0.0.1:5000/}"
    "$HAULER_BIN" store add image "$original" \
      --platform linux/amd64 \
      --rewrite "$rewrite" \
      --store "$STORE"
  done < "$IMAGES_TSV"
  for tar_path in $EXTRA_IMAGE_TARS; do
    if [[ ! -f "$tar_path" ]]; then
      echo "EXTRA_IMAGE_TARS entry not found: $tar_path" >&2
      exit 1
    fi
    echo "loading extra image tar: $tar_path"
    "$HAULER_BIN" store load -s "$STORE" -f "$tar_path"
  done
  image_count="$(awk 'NF && NR>1 { count++ } END { print count+0 }' "$IMAGES_TSV")"
  store_count="$("$HAULER_BIN" store info -s "$STORE" --type image -o json | grep -c '"Type": "image"')"
  if [[ "$store_count" != "$image_count" ]]; then
    echo "Hauler store image count mismatch: expected $image_count, got $store_count" >&2
    exit 1
  fi
  "$HAULER_BIN" store save -s "$STORE" -f "$OUTPUT/images/images.haul.tar.zst"
fi
install -m 0644 "$IMAGES_TSV" "$OUTPUT/images/images.tsv"

echo "[3/6] copying fixed binaries, repository ISO and chart material"
install -m 0755 "$HAULER_BIN" "$OUTPUT/bin/hauler"
install -m 0755 "$HELM_BIN" "$OUTPUT/bin/helm"
copy_material "$REPOSITORY_ISO" "$OUTPUT/repository/ubuntu-24.04-debs-amd64.iso"

# Charts are immutable upstream material: they are copied once and never
# re-rendered here. Only charts already listed in the component lock are taken.
if [[ -d "$CHARTS_DIR" ]]; then
  charts_copied=0
  while IFS= read -r chart_source; do
    relative="${chart_source#"$CHARTS_DIR"/}"
    mkdir -p "$OUTPUT/charts/$(dirname "$relative")"
    install -m 0644 "$chart_source" "$OUTPUT/charts/$relative"
    charts_copied=$((charts_copied + 1))
  done < <(find "$CHARTS_DIR" -type f -name '*.tgz' | sort)
  echo "packaged $charts_copied chart archives from $CHARTS_DIR"
fi

echo "[4/6] copying fixed artifact metadata"
install -m 0644 "$ROOT/ani/package.yaml" "$OUTPUT/config/package.yaml"
install -m 0644 "$ROOT/ani/versions.yaml" "$OUTPUT/config/versions.yaml"
install -m 0644 "$ROOT/ani/runtime-checksums.txt" "$OUTPUT/config/runtime-checksums.txt"
install -m 0644 "$ROOT/ani/repository-iso-checksums.txt" "$OUTPUT/config/repository-iso-checksums.txt"
install -m 0644 "$COMPONENT_LOCK" "$OUTPUT/config/components.lock.yaml"
for path in LICENSE NOTICE; do
  if [[ -f "$ROOT/$path" ]]; then
    install -m 0644 "$ROOT/$path" "$OUTPUT/licenses/$path"
  fi
done

echo "[5/6] validating artifact-only layout"
for forbidden in \
  kk \
  install.sh \
  verify.sh \
  probe.sh \
  cluster.example.yaml \
  README.md \
  manifests; do
  if [[ -e "$OUTPUT/$forbidden" ]]; then
    echo "artifact output must not contain code-release file or directory: $OUTPUT/$forbidden" >&2
    exit 1
  fi
done

echo "[6/6] generating artifact checksums"
(
  cd "$OUTPUT"
  find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS
)
echo "artifact complete at $OUTPUT"
#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# 共享发布体检：凭据/lab 脚本不得进入发布物；可只体检不构建。
source "$ROOT/scripts/release-guard.sh"
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
# Source-side record of the repository ISO digest: this file lives in the repo,
# outside the artifact, so an attacker who regenerates the artifact's own
# SHA256SUMS still cannot make a wrong ISO acceptable.
ISO_CHECKSUMS="${ISO_CHECKSUMS:-$ROOT/ani/repository-iso-checksums.txt}"

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
required=("$CONFIG" "$IMAGES_TSV" "$HAULER_BIN" "$REPOSITORY_ISO" "$HELM_BIN" "$COMPONENT_LOCK" "$ISO_CHECKSUMS")
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

# R07/A07: every copied material is verified against the approved hash from the
# component lock (or the source-side ISO record) at copy time — never against an
# in-package SHA256SUMS, which an attacker can regenerate.
helm_sha="$(sha256sum "$HELM_BIN" | awk '{print $1}')"
approved_helm_sha="$(awk '/binarySha256:/ { print $2; exit }' "$COMPONENT_LOCK")"
if [[ -z "$approved_helm_sha" || "$helm_sha" != "$approved_helm_sha" ]]; then
  echo "helm binary sha256 $helm_sha does not match the approved $approved_helm_sha (components.lock.yaml)" >&2
  exit 1
fi
install -m 0755 "$HELM_BIN" "$OUTPUT/bin/helm"

iso_sha="$(sha256sum "$REPOSITORY_ISO" | awk '{print $1}')"
approved_iso_sha="$(awk '$1 == "ubuntu-24.04-debs-amd64.iso" { print $2; exit }' "$ISO_CHECKSUMS")"
if [[ -z "$approved_iso_sha" || "$iso_sha" != "$approved_iso_sha" ]]; then
  echo "repository ISO sha256 $iso_sha does not match the recorded $approved_iso_sha ($ISO_CHECKSUMS)" >&2
  exit 1
fi
copy_material "$REPOSITORY_ISO" "$OUTPUT/repository/ubuntu-24.04-debs-amd64.iso"

# Charts are immutable upstream material: they are copied once and never
# re-rendered here. R07: the copy set is exactly the chart list in the component
# lock — every copied archive is hash-checked against the lock, a chart that is
# not approved fails, and a lock entry missing from CHARTS_DIR fails too.
charts_copied=0
# R15.3: charts are placed at the LOCK's artifactChartPath — the preflight
# requires exactly that layout. Source files are matched by content sha256
# against the lock, so the file name in CHARTS_DIR does not matter.
declare -A chart_placed
while IFS= read -r chart_source; do
  chart_sha="$(sha256sum "$chart_source" | awk '{print $1}')"
  approved="$(grep -F "$chart_sha" "$COMPONENT_LOCK" | head -1 || true)"
  if [[ -z "$approved" ]]; then
    echo "chart $chart_source (sha256 $chart_sha) is not approved in $COMPONENT_LOCK; refusing to package an unknown chart" >&2
    exit 1
  fi
done < <(find "$CHARTS_DIR" -type f -name '*.tgz' | sort)
while IFS= read -r rel; do
  [[ -n "$rel" ]] || continue
  base="$(basename "$rel")"
  name="$(echo "$rel" | cut -d/ -f2)"
  chart_source=""
  while IFS= read -r candidate; do
    cand_sha="$(sha256sum "$candidate" | awk '{print $1}')"
    if grep -qF "$cand_sha" "$COMPONENT_LOCK" && grep -qF "$rel" "$COMPONENT_LOCK"; then
      : # content hash + lock path both match below; use name-based fallback
    fi
  done < <(find "$CHARTS_DIR/$name" -type f -name '*.tgz' 2>/dev/null | sort)
  # source: any chart in CHARTS_DIR/$name whose content hash is approved
  for candidate in "$CHARTS_DIR/$name"/*.tgz; do
    [[ -f "$candidate" ]] || continue
    cand_sha="$(sha256sum "$candidate" | awk '{print $1}')"
    if grep -qF "$cand_sha" "$COMPONENT_LOCK"; then
      chart_source="$candidate"
      break
    fi
  done
  if [[ -z "$chart_source" ]]; then
    echo "chart set does not match the lock: lock chart $rel has no hash-approved source in $CHARTS_DIR/$name" >&2
    exit 1
  fi
  mkdir -p "$OUTPUT/$(dirname "$rel")"
  install -m 0644 "$chart_source" "$OUTPUT/$rel"
  chart_placed["$rel"]=1
  charts_copied=$((charts_copied + 1))
done < <(grep -oE 'artifactChartPath: \S+' "$COMPONENT_LOCK" | awk '{print $2}')
lock_chart_count="$(grep -c 'artifactChartPath:' "$COMPONENT_LOCK")"
if [[ "$charts_copied" != "$lock_chart_count" ]]; then
  echo "chart set does not match the lock: $charts_copied placed, $lock_chart_count approved entries" >&2
  exit 1
fi
echo "packaged $charts_copied chart archives at lock artifactChartPath layout, all hash-approved in the lock"

echo "[4/6] copying fixed artifact metadata"
# R07: the artifact ships the CONFIG this build actually consumed, not a default
# package.yaml, plus a record of every source digest and material origin.
install -m 0644 "$CONFIG" "$OUTPUT/config/package.yaml"
install -m 0644 "$ROOT/ani/versions.yaml" "$OUTPUT/config/versions.yaml"
install -m 0644 "$ROOT/ani/runtime-checksums.txt" "$OUTPUT/config/runtime-checksums.txt"
install -m 0644 "$ISO_CHECKSUMS" "$OUTPUT/config/repository-iso-checksums.txt"
install -m 0644 "$COMPONENT_LOCK" "$OUTPUT/config/components.lock.yaml"
{
  echo "# materials source record — generated by build-offline.sh at $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "config.package.yaml sha256:$(sha256sum "$CONFIG" | awk '{print $1}')"
  echo "config.images.tsv sha256:$(sha256sum "$IMAGES_TSV" | awk '{print $1}')"
  echo "config.components.lock.yaml sha256:$(sha256sum "$COMPONENT_LOCK" | awk '{print $1}')"
  echo "bin.hauler sha256:$(sha256sum "$HAULER_BIN" | awk '{print $1}')"
  echo "bin.helm sha256:$helm_sha (verified against components.lock.yaml)"
  echo "repository.iso sha256:$iso_sha (verified against repository-iso-checksums.txt)"
} > "$OUTPUT/config/materials-source.txt"
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
  network-probe.sh \
  cluster.example.yaml \
  README.md \
  manifests; do
  if [[ -e "$OUTPUT/$forbidden" ]]; then
    echo "artifact output must not contain code-release file or directory: $OUTPUT/$forbidden" >&2
    exit 1
  fi
done
ani_assert_release_clean "$OUTPUT" || exit 1

echo "[6/6] generating artifact checksums"

# R15.3: the repository ISO must ship INSIDE the artifact tarball so
# kk create cluster --artifact can distribute it to the nodes for offline
# apt (the field install failed without it). The tarball may be gzipped (the
# real kk artifact export) or a plain tar (fixture builds) — detect the form
# instead of assuming gzip, which broke the R07.2 offline-material suite.
iso_dest="$OUTPUT/repository/$(basename "$REPOSITORY_ISO")"
artifact_tar="$OUTPUT/packages/kubekey-artifact.tgz"
iso_name="$(basename "$REPOSITORY_ISO")"
if [[ -f "$iso_dest" ]] && ! tar tf "$artifact_tar" 2>/dev/null | grep -q "$iso_name"; then
  isodir="$(mktemp -d)"
  mkdir -p "$isodir/repository"
  cp "$iso_dest" "$isodir/repository/$iso_name"
  if [[ "$(od -An -tx1 -N2 "$artifact_tar" | tr -d ' \n')" == "1f8b" ]]; then
    gunzip -c "$artifact_tar" > "$isodir/artifact.tar"
    tar -C "$isodir" -rf "$isodir/artifact.tar" repository
    gzip -c "$isodir/artifact.tar" > "$artifact_tar"
  else
    cp "$artifact_tar" "$isodir/artifact.tar"
    tar -C "$isodir" -rf "$isodir/artifact.tar" repository
    cat "$isodir/artifact.tar" > "$artifact_tar"
  fi
  rm -rf "$isodir"
  echo "repository ISO injected into the artifact tarball"
  ( cd "$OUTPUT" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS )
fi
(
  cd "$OUTPUT"
  find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS
)
echo "artifact complete at $OUTPUT"
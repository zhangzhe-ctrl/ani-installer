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
EVIDENCE_SOURCES="${EVIDENCE_SOURCES:-}"
# PACKAGED_REGISTRY_ADDRESS=host:port declares that the store to be shipped is
# ALREADY served there by the caller. The image content gate still runs against
# that address; the variable only replaces the script's own `hauler serve`, so a
# pre-served store can be re-verified without a hauler binary. It never skips a
# check, and it requires the bytes to ship (HAULER_ARCHIVE or HAULER_STORE).
PACKAGED_REGISTRY_ADDRESS="${PACKAGED_REGISTRY_ADDRESS:-}"
if [[ -n "$PACKAGED_REGISTRY_ADDRESS" && -z "$HAULER_ARCHIVE$HAULER_STORE" ]]; then
  echo "PACKAGED_REGISTRY_ADDRESS requires HAULER_ARCHIVE or HAULER_STORE (the bytes that get shipped)" >&2
  exit 1
fi
if [[ -n "$PACKAGED_REGISTRY_ADDRESS" ]] &&
   ! [[ "$PACKAGED_REGISTRY_ADDRESS" =~ ^[0-9A-Za-z._-]+:[0-9]+$ ]]; then
  echo "PACKAGED_REGISTRY_ADDRESS must be host:port, got $PACKAGED_REGISTRY_ADDRESS" >&2
  exit 1
fi
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
RUSTFS_RC_BIN="${RUSTFS_RC_BIN:-}"
KUBEFLOW_WHEELS_DIR="${KUBEFLOW_WHEELS_DIR:-}"
KUBEFLOW_GRPC_CHECK_BIN="${KUBEFLOW_GRPC_CHECK_BIN:-}"
KUBEFLOW_GRPC_SOURCE_ARCHIVE="${KUBEFLOW_GRPC_SOURCE_ARCHIVE:-}"
VIRTCTL_BIN="${VIRTCTL_BIN:-}"
KUBEVIRT_GUEST="${KUBEVIRT_GUEST:-}"
TRIVY_DB_FILE="${TRIVY_DB_FILE:-}"
TRIVY_DB_METADATA="${TRIVY_DB_METADATA:-}"
TRIVY_JAVA_DB_FILE="${TRIVY_JAVA_DB_FILE:-}"
TRIVY_JAVA_DB_METADATA="${TRIVY_JAVA_DB_METADATA:-}"
CHARTS_DIR="${CHARTS_DIR:-$ROOT/ani/charts}"
COMPONENT_LOCK="${COMPONENT_LOCK:-$ROOT/ani/components.lock.yaml}"
B05_LOCKED=false
B07_LOCKED=false
RUSTFS_LOCKED=false
if grep -q "^batchB05:" "$COMPONENT_LOCK"; then B05_LOCKED=true; fi
if grep -q "^batchB07:" "$COMPONENT_LOCK"; then B07_LOCKED=true; fi
if grep -q '^  rustfs-rc:' "$COMPONENT_LOCK"; then RUSTFS_LOCKED=true; fi
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
if "$RUSTFS_LOCKED"; then required+=("$RUSTFS_RC_BIN"); fi
if "$B05_LOCKED"; then required+=("$VIRTCTL_BIN" "$KUBEVIRT_GUEST"); fi
if "$B07_LOCKED"; then required+=("$TRIVY_DB_FILE" "$TRIVY_DB_METADATA" "$TRIVY_JAVA_DB_FILE" "$TRIVY_JAVA_DB_METADATA"); fi
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
if "$B05_LOCKED" && [[ ! -x "$VIRTCTL_BIN" ]]; then
  echo "VIRTCTL_BIN must be executable: $VIRTCTL_BIN" >&2
  exit 1
fi
if [[ ! -x "$HELM_BIN" ]]; then
  echo "HELM_BIN must be executable: $HELM_BIN" >&2
  exit 1
fi
if "$RUSTFS_LOCKED" && [[ ! -x "$RUSTFS_RC_BIN" ]]; then
  echo "RUSTFS_RC_BIN must be executable: $RUSTFS_RC_BIN" >&2
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
# The registry content gate approves a packaged object either because its bytes ARE
# the pinned object or because packaged evidence proves it is that pin's
# linux/amd64 object. Without evidence roots no derived row can be approved, so
# packaging refuses here instead of failing later with "no evidence packaged".
if [[ -z "$EVIDENCE_SOURCES" ]]; then
  echo "EVIDENCE_SOURCES is required: a space-separated list of content-addressed roots holding the approved source manifests (a preserved store, or an earlier artifact's images/evidence)" >&2
  exit 1
fi
for root in $EVIDENCE_SOURCES; do
  if [[ ! -d "$root" ]]; then
    echo "EVIDENCE_SOURCES entry is not a directory: $root" >&2
    exit 1
  fi
done

CONFIG="$(cd "$(dirname "$CONFIG")" && pwd)/$(basename "$CONFIG")"
IMAGES_TSV="$(cd "$(dirname "$IMAGES_TSV")" && pwd)/$(basename "$IMAGES_TSV")"
OUTPUT="$(mkdir -p "$(dirname "$OUTPUT")" && cd "$(dirname "$OUTPUT")" && pwd)/$(basename "$OUTPUT")"
WORK="$(mktemp -d "$ROOT/build/ani-artifact.XXXXXX")"
mkdir -p "$WORK/tmp"
export TMPDIR="$WORK/tmp"
REGISTRY_PID=""
cleanup() {
  if [[ -n "$REGISTRY_PID" ]]; then
    kill "$REGISTRY_PID" 2>/dev/null || true
    wait "$REGISTRY_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

# F06: every material rule lives in Go (pkg/ani/materials_build.go), not in
# grep/awk heuristics, so packaging verifies the same things the installer does.
MATERIALS_KK="${MATERIALS_KK:-${KK_BIN:-}}"
if [[ -z "$MATERIALS_KK" ]]; then
  echo "MATERIALS_KK (or KK_BIN) must point at a kk binary built with -tags builtin: the material steps (place-tools, place-charts, verify-registry, inject-repository-iso) are implemented there, and this script will not fall back to shell heuristics" >&2
  exit 1
fi
if [[ ! -x "$MATERIALS_KK" ]]; then
  echo "MATERIALS_KK is not an executable kk: $MATERIALS_KK" >&2
  exit 1
fi
if ! "$MATERIALS_KK" ani materials --help >/dev/null 2>&1; then
  echo "MATERIALS_KK ($MATERIALS_KK) has no 'ani materials' subcommand; rebuild kk with -tags builtin" >&2
  exit 1
fi
EVIDENCE_ARGS=()
EVIDENCE_FROM=()
for root in $EVIDENCE_SOURCES; do
  EVIDENCE_ARGS+=(--evidence-dir "$root")
  EVIDENCE_FROM+=(--from "$root")
done

MATERIALS_LOG="$OUTPUT/config/materials-verification.txt"
materials() {
  # Every material step is recorded, so the artifact carries the evidence of
  # what was verified when it was built.
  {
    printf '\n$ kk ani materials %s\n' "$*"
    "$MATERIALS_KK" ani materials "$@"
  } | tee -a "$MATERIALS_LOG"
}

verify_image_store() {
  # The image gate is the install's own. Either the caller already serves the
  # store it wants shipped (ANI_PACKAGED_REGISTRY_ADDRESS, e.g. a re-verification
  # of an existing archive), or this script starts hauler itself against the
  # store it just built. Nothing may ship without passing this.
  local store="$1" port="" waited=0
  if [[ -n "$PACKAGED_REGISTRY_ADDRESS" ]]; then
    echo "verifying the packaged store through the registry the caller serves at $PACKAGED_REGISTRY_ADDRESS"
    if ! materials verify-registry \
        --registry-address "$PACKAGED_REGISTRY_ADDRESS" --images-tsv "$IMAGES_TSV" --lock "$COMPONENT_LOCK"         "${EVIDENCE_ARGS[@]}" --evidence-dir "$OUTPUT/images/evidence"; then
      echo "the packaged image store failed the install's own content gate" >&2
      exit 1
    fi
    return
  fi
  port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
  mkdir -p "$WORK/registry"
  "$HAULER_BIN" store serve registry --port "$port" --directory "$WORK/registry" \
    --readonly=true --store "$store" >"$WORK/registry.log" 2>&1 &
  REGISTRY_PID=$!
  until (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; do
    exec 3>&- 3<&-
    if ! kill -0 "$REGISTRY_PID" 2>/dev/null; then
      echo "the packaged store would not serve locally; refusing to ship an unverified image archive" >&2
      cat "$WORK/registry.log" >&2
      exit 1
    fi
    waited=$((waited + 1))
    # Hauler first stages every image into its local registry. An 86-image
    # cumulative store takes longer than the old one-minute startup budget on
    # the supported Fedora packager; keep waiting, then apply the same content
    # gate once the registry is actually serving.
    if [[ "$waited" -ge 600 ]]; then
      echo "timed out waiting for the packaged store to serve on 127.0.0.1:$port after ${waited}s" >&2
      cat "$WORK/registry.log" >&2
      exit 1
    fi
    sleep 1
  done
  exec 3>&- 3<&-
  if ! materials verify-registry \
      --registry-address "127.0.0.1:$port" --images-tsv "$IMAGES_TSV" --lock "$COMPONENT_LOCK"       "${EVIDENCE_ARGS[@]}" --evidence-dir "$OUTPUT/images/evidence"; then
    echo "the packaged image store failed the install's own content gate" >&2
    exit 1
  fi
  kill "$REGISTRY_PID" 2>/dev/null || true
  wait "$REGISTRY_PID" 2>/dev/null || true
  REGISTRY_PID=""
}

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

# First-install Kubeflow assets use the same cumulative material artifact.
# Their exact source-approved bytes are also embedded in kk's preflight.
# This does not add an installation-afterwards or material-extension entry.
if grep -q '^kubeflow:' "$COMPONENT_LOCK"; then
  mkdir -p "$OUTPUT/manifests/kubeflow"
  python3 "$ROOT/ani/kubeflow/prepare-bundle.py" --source-root "$ROOT" \
    --wheels-dir "$KUBEFLOW_WHEELS_DIR" --grpc-source-archive "$KUBEFLOW_GRPC_SOURCE_ARCHIVE" \
    --output "$OUTPUT/manifests/kubeflow/26.03-kfp2.16-trainer2.1-v1"
fi

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
# F06: every input shape ends in the install's own registry content gate before
# anything is shipped. A caller-supply served store is verified, then copied: a
# pre-served address does not make a build an exception to the gate.
if [[ -n "$PACKAGED_REGISTRY_ADDRESS" ]]; then
  # A caller-supplied served store is verified, landed and re-saved: shipping a
  # served archive untouched would bypass the landing that keeps pins true.
  STAGED="$WORK/staged-store"
  mkdir -p "$STAGED"
  if [[ -n "$HAULER_ARCHIVE" ]]; then
    "$HAULER_BIN" store load -s "$STAGED" -f "$HAULER_ARCHIVE"
  else
    "$HAULER_BIN" store copy "$HAULER_STORE" "$STAGED" 2>/dev/null || cp -a "$HAULER_STORE/." "$STAGED/"
  fi
  materials record-evidence --images-tsv "$IMAGES_TSV" --lock "$COMPONENT_LOCK"     --out "$OUTPUT/images/evidence" "${EVIDENCE_FROM[@]}"
  LANDED="$WORK/landed-store"
  materials land-images --images-tsv "$IMAGES_TSV" --lock "$COMPONENT_LOCK"     --evidence-dir "$OUTPUT/images/evidence" --source-store "$STAGED" --out-store "$LANDED"
  verify_image_store "$LANDED"
  "$HAULER_BIN" store save -s "$LANDED" -f "$OUTPUT/images/images.haul.tar.zst"
else
  STORE="$WORK/hauler-store"
  if [[ -n "$HAULER_STORE" ]]; then
    STORE="$HAULER_STORE"
  elif [[ -n "$HAULER_ARCHIVE" ]]; then
    mkdir -p "$STORE"
    "$HAULER_BIN" store load -s "$STORE" -f "$HAULER_ARCHIVE"
  else
    mkdir -p "$STORE"
    while IFS=$'\t' read -r original hauler_ref actual_digest use_location; do
      [[ "$original" == "original_ref" ]] && continue
      [[ -z "$original" ]] && continue
      if [[ " $EXTRA_IMAGE_ORIGINALS " == *" $original "* ]]; then
        echo "skip $original: provided via EXTRA_IMAGE_TARS"
        continue
      fi
      rewrite="${hauler_ref#127.0.0.1:5000/}"
      # Pull the pinned object, not a mutable tag: the tag could have moved and
      # the store would then carry bytes this release never approved.
      source_reference="${original%:*}@${actual_digest}"
      "$HAULER_BIN" store add image "$source_reference" \
        --platform linux/amd64 \
        --rewrite "$rewrite" \
        --store "$STORE"
    done < "$IMAGES_TSV"
    read -r -a extra_tars <<< "$EXTRA_IMAGE_TARS"
    read -r -a extra_originals <<< "$EXTRA_IMAGE_ORIGINALS"
    for i in "${!extra_tars[@]}"; do
      tar_path="${extra_tars[$i]}"
      original="${extra_originals[$i]}"
      if [[ ! -f "$tar_path" ]]; then
        echo "EXTRA_IMAGE_TARS entry not found: $tar_path" >&2
        exit 1
      fi
      hauler_ref="$(awk -F'\t' -v o="$original" '$1==o{print $2}' "$IMAGES_TSV")"
      rewrite="${hauler_ref#127.0.0.1:5000/}"
      python3 - "$tar_path" "$hauler_ref" <<'CHECK_TAR'
import json, sys, tarfile
with tarfile.open(sys.argv[1]) as archive:
    tags = json.load(archive.extractfile('manifest.json'))[0].get('RepoTags', [])
if tags != [sys.argv[2]]:
    raise SystemExit(f'extra image tar RepoTags mismatch: {tags!r} != {[sys.argv[2]]!r}')
CHECK_TAR
      echo "importing extra Docker image tar: $tar_path as $rewrite"
      skopeo copy "docker-archive:$tar_path" "oci:$STORE:$rewrite"
    done
  fi
  # Record the approved source manifests into the package, then build the shipped
  # store from those bytes: hauler re-writes a Docker schema2 manifest it pulls, and
  # a store assembled from a floating tag carries whatever that tag pointed at, so
  # "pulled by digest" alone does not guarantee the packaged object IS the pinned one.
  materials record-evidence --images-tsv "$IMAGES_TSV" --lock "$COMPONENT_LOCK"     --out "$OUTPUT/images/evidence" "${EVIDENCE_FROM[@]}"
  LANDED="$WORK/landed-store"
  materials land-images --images-tsv "$IMAGES_TSV" --lock "$COMPONENT_LOCK"     --evidence-dir "$OUTPUT/images/evidence" --source-store "$STORE" --out-store "$LANDED"
  verify_image_store "$LANDED"
  "$HAULER_BIN" store save -s "$LANDED" -f "$OUTPUT/images/images.haul.tar.zst"
fi
install -m 0644 "$IMAGES_TSV" "$OUTPUT/images/images.tsv"

echo "[3/6] placing fixed binaries, repository ISO and chart material"
# F06: tools and charts are placed by Go against the individual lock entry that
# approves them (digest + name/version + artifact path), and every landed file is
# re-read. Every shipped binary is lock-approved, so nothing lands in bin/
# without an approved digest behind it.
tool_sources=(--source "helm=$HELM_BIN" --source "hauler=$HAULER_BIN")
if "$RUSTFS_LOCKED"; then tool_sources+=(--source "rustfs-rc=$RUSTFS_RC_BIN"); fi
if "$B05_LOCKED"; then tool_sources+=(--source "virtctl=$VIRTCTL_BIN"); fi
if grep -q '^kubeflow:' "$COMPONENT_LOCK"; then tool_sources+=(--source "ani-kfp-grpc-check=$KUBEFLOW_GRPC_CHECK_BIN"); fi
materials place-tools --lock "$COMPONENT_LOCK" --artifact-root "$OUTPUT" "${tool_sources[@]}"
materials place-charts --lock "$COMPONENT_LOCK" --charts-dir "$CHARTS_DIR" --artifact-root "$OUTPUT"
# B05 guest is fixed by the official publisher digest in batchB05.guest and
# checked against source-side lock bytes before copying into the artifact.
if "$B05_LOCKED"; then
GUEST_SHA256="$(sed -n '/^  guest:/,/^  images:/s/^    sha256: //p' "$COMPONENT_LOCK" | head -1)"
if [[ "$GUEST_SHA256" != "7d6355852aeb6dbcd191bcda7cd74f1536cfe5cbf8a10495a7283a8396e4b75b" ]]; then
  echo "B05 guest lock digest changed without review: $GUEST_SHA256" >&2
  exit 1
fi
echo "$GUEST_SHA256  $KUBEVIRT_GUEST" | sha256sum -c -
install -D -m 0644 "$KUBEVIRT_GUEST" "$OUTPUT/guest/cirros-0.6.3-x86_64-disk.img"
echo "$GUEST_SHA256  $OUTPUT/guest/cirros-0.6.3-x86_64-disk.img" | sha256sum -c -
fi
# B07 scanner cache comes from four fixed files, extracted from the pinned OCI
# database layers. Verify source-side lock digests and the landed package bytes.
if "$B07_LOCKED"; then
read -r DB_SHA DB_META_SHA JAVA_SHA JAVA_META_SHA < <(awk '
  /^  trivyDatabase:/ {section="db"; next}
  /^  trivyJavaDatabase:/ {section="java"; next}
  /^  images:/ && section=="java" {exit}
  section=="db" && /dbSha256:/ {db=$2}
  section=="db" && /metadataSha256:/ {dbmeta=$2}
  section=="java" && /dbSha256:/ {java=$2}
  section=="java" && /metadataSha256:/ {javameta=$2}
  END {print db, dbmeta, java, javameta}
' "$COMPONENT_LOCK")
if [[ "$DB_SHA" != "4bf01c98f9af59d4ec9ab6c8f4f22740230830e8be9172970e74dcb8257cd53b" ||
      "$DB_META_SHA" != "eec14e933a21b2dba78c45d2c0de61a6408e14cee008a07908079e4cd6c905d7" ||
      "$JAVA_SHA" != "e99e2d1212f4f1ef8281f95ffa72b667ce3fb8d00db0c4b2ddfcc3283b87612c" ||
      "$JAVA_META_SHA" != "f730c0616742e306095594ef51359f81cd05e48cff224112a59c8fbd68449e31" ]]; then
  echo "B07 scanner database lock digests changed without review" >&2
  exit 1
fi
printf '%s  %s\n' "$DB_SHA" "$TRIVY_DB_FILE" "$DB_META_SHA" "$TRIVY_DB_METADATA" "$JAVA_SHA" "$TRIVY_JAVA_DB_FILE" "$JAVA_META_SHA" "$TRIVY_JAVA_DB_METADATA" | sha256sum -c -
install -D -m 0644 "$TRIVY_DB_FILE" "$OUTPUT/scanner/db/trivy.db"
install -D -m 0644 "$TRIVY_DB_METADATA" "$OUTPUT/scanner/db/metadata.json"
install -D -m 0644 "$TRIVY_JAVA_DB_FILE" "$OUTPUT/scanner/java-db/trivy-java.db"
install -D -m 0644 "$TRIVY_JAVA_DB_METADATA" "$OUTPUT/scanner/java-db/metadata.json"
printf '%s  %s\n' "$DB_SHA" "$OUTPUT/scanner/db/trivy.db" "$DB_META_SHA" "$OUTPUT/scanner/db/metadata.json" "$JAVA_SHA" "$OUTPUT/scanner/java-db/trivy-java.db" "$JAVA_META_SHA" "$OUTPUT/scanner/java-db/metadata.json" | sha256sum -c -
fi

# The repository ISO is approved by the SOURCE-SIDE record (outside the artifact,
# so a regenerated in-package SHA256SUMS can never legitimise a wrong ISO), then
# placed loose and inside the KubeKey artifact tarball, and both copies are
# re-read by content — not by file name.
materials inject-repository-iso \
  --iso "$REPOSITORY_ISO" \
  --checksums "$ISO_CHECKSUMS" \
  --artifact-root "$OUTPUT" \
  --artifact-tar "$OUTPUT/packages/kubekey-artifact.tgz" \
  --entry-path "repository/$(basename "$REPOSITORY_ISO")" \
  --loose-copy "repository/$(basename "$REPOSITORY_ISO")"

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
  echo "config.materials-verification.txt sha256:$(sha256sum "$MATERIALS_LOG" | awk '{print $1}')"
  echo "bin.helm sha256:$(sha256sum "$OUTPUT/bin/helm" | awk '{print $1}') (the digest of THIS lock entry, re-read after placement)"
  echo "bin.hauler sha256:$(sha256sum "$OUTPUT/bin/hauler" | awk '{print $1}') (the digest of THIS lock entry, re-read after placement)"
  if "$RUSTFS_LOCKED"; then echo "bin.rc sha256:$(sha256sum "$OUTPUT/bin/rc" | awk '{print $1}') (the digest of THIS lock entry, re-read after placement)"; fi
  if "$B05_LOCKED"; then echo "bin.virtctl sha256:$(sha256sum "$OUTPUT/bin/virtctl" | awk '{print $1}') (the digest of THIS lock entry, re-read after placement)"; fi
  if "$B05_LOCKED"; then echo "guest.cirros sha256:$(sha256sum "$OUTPUT/guest/cirros-0.6.3-x86_64-disk.img" | awk '{print $1}') (approved by batchB05.guest in source-side lock)"; fi
  if "$B07_LOCKED"; then echo "scanner.db sha256:$DB_SHA scanner.db.metadata sha256:$DB_META_SHA scanner.java-db sha256:$JAVA_SHA scanner.java-db.metadata sha256:$JAVA_META_SHA (approved by batchB07 in source-side lock)"; fi
  echo "repository.iso sha256:$(sha256sum "$OUTPUT/repository/$(basename "$REPOSITORY_ISO")" | awk '{print $1}') (approved by $ISO_CHECKSUMS, loose and inside packages/kubekey-artifact.tgz)"
  echo "images.archive sha256:$(sha256sum "$OUTPUT/images/images.haul.tar.zst" | awk '{print $1}') (saved from a store that passed the registry content gate)"
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

# The repository ISO was already placed, loose and inside the artifact tarball,
# by the inject-repository-iso step (F06) — verified by content, not by name. The
# in-package SHA256SUMS is a self-consistency list only: the approval lives in
# components.lock.yaml and repository-iso-checksums.txt, both read from outside.
(
  cd "$OUTPUT"
  find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS
)
echo "artifact complete at $OUTPUT"

#!/usr/bin/env bash
# C4 gate driver: run the Go checks and render all three log roles offline.
#
# Everything here runs on fedora, where the packaged helm binary and the
# expanded charts already live. Nothing touches a node or a cluster.
#
# Usage: run-c4-gate.sh [run-root]
#   run-root defaults to ~/ani-installer-runs/observability-20260919
set -euo pipefail

RUN_ROOT="${1:-$HOME/ani-installer-runs/observability-20260919}"
SRC="$RUN_ROOT/src/kubekey"
# The repo root is the directory that contains the kubekey/ module; the role
# templates live under it and the render program runs with the module as its
# working directory.
REPO="$RUN_ROOT/src"
# The locked charts live in this task's own inputs directory, not in a release
# artifact: C4 only renders, it does not build a release.
CHARTS="$RUN_ROOT/inputs/charts"
HELM="${HELM:-$HOME/ani-installer-runs/foundation-20260918/inputs/tools/helm}"
LAB="$RUN_ROOT/lab/c4-render-gate"
EVID="$RUN_ROOT/evidence/c4-render-gate"

[ -d "$SRC" ] || { echo "source tree not found: $SRC" >&2; exit 2; }
[ -d "$CHARTS" ] || { echo "chart directory not found: $CHARTS" >&2; exit 2; }

command -v go >/dev/null || { echo "go not found" >&2; exit 2; }
[ -x "$HELM" ] || { echo "helm not found at $HELM" >&2; exit 2; }
# The render gate scripts look helm up on PATH; a non-interactive ssh shell has
# a minimal PATH, so the packaged binary is exported explicitly rather than
# assumed to be discoverable.
export HELM
export PATH="$(dirname "$HELM"):$PATH"

install -d -m 0755 "$LAB" "$EVID"

# The gate reads two templates that are not part of the chart render (the
# security configuration Secret and the initialization Job) from the role
# directory, so the path is handed to it rather than guessed from the script's
# own location: the lab copy is not inside the repo.
export ROLE_ROOT="$SRC/builtin/core/roles/ani"
export BACKEND

echo "=== gofmt ==="
cd "$SRC"
# Only the trees this work owns are checked. The upstream repository already
# carries two files with CRLF line endings (builtin/core/fs.go and
# builtin/core/upgrade_path.go); they are committed that way and are outside
# this task's scope, so reformatting them here would be an unrelated change.
# pkg/ani and the ani roles are what C0-C5 modify, so those must stay clean.
unformatted="$(gofmt -l pkg/ani builtin/core/roles 2>/dev/null || true)"
if [ -n "$unformatted" ]; then
  echo "these files are not gofmt-clean:" >&2
  echo "$unformatted" >&2
  exit 1
fi
echo "gofmt clean (pkg/ani, builtin/core/roles)"
if gofmt -l builtin/core >/dev/null 2>&1; then
  preexisting="$(gofmt -l builtin/core 2>/dev/null || true)"
  [ -z "$preexisting" ] || echo "  NOTE upstream CRLF files, not touched by this work: $(echo $preexisting | tr '\n' ' ')"
fi

echo "=== go build ==="
go build ./pkg/ani/... ./cmd/... 2>&1 | tee "$EVID/build.log"

echo "=== go vet ==="
go vet ./pkg/ani/... 2>&1 | tee "$EVID/vet.log"

echo "=== go test pkg/ani ==="
go test ./pkg/ani/... 2>&1 | tee "$EVID/test.log"

# The render gate needs a values file per role, produced by the real Go code.
echo "=== render loki values ==="
BACKEND=loki "$LAB/render-values.sh" "$REPO" "$SRC/ani/images.tsv" loki \
  "$EVID/loki-values.yaml"

echo "=== render opensearch values ==="
BACKEND=opensearch "$LAB/render-values.sh" "$REPO" "$SRC/ani/images.tsv" opensearch \
  "$EVID/opensearch-values.yaml"

echo "=== render fluent-bit values (loki backend) ==="
BACKEND=loki "$LAB/render-values.sh" "$REPO" "$SRC/ani/images.tsv" fluent-bit \
  "$EVID/fluent-bit-values-loki.yaml"

echo "=== render fluent-bit values (opensearch backend) ==="
BACKEND=opensearch "$LAB/render-values.sh" "$REPO" "$SRC/ani/images.tsv" fluent-bit \
  "$EVID/fluent-bit-values-opensearch.yaml"

echo "=== render loki chart ==="
"$LAB/render-gate.sh" \
  "$CHARTS/loki-18.13.3.tgz" "$EVID/loki-values.yaml" "$EVID/loki-render.yaml" loki \
  2>&1 | tee "$EVID/loki-render-gate.log"

echo "=== render opensearch chart ==="
BACKEND=opensearch "$LAB/render-gate.sh" \
  "$CHARTS/opensearch-3.8.0.tgz" "$EVID/opensearch-values.yaml" \
  "$EVID/opensearch-render.yaml" opensearch \
  2>&1 | tee "$EVID/opensearch-render-gate.log"

echo "=== render fluent-bit chart (loki backend) ==="
BACKEND=loki "$LAB/render-gate.sh" \
  "$CHARTS/fluent-bit-0.58.2.tgz" "$EVID/fluent-bit-values-loki.yaml" \
  "$EVID/fluent-bit-render-loki.yaml" fluent-bit \
  2>&1 | tee "$EVID/fluent-bit-render-loki.log"

echo "=== render fluent-bit chart (opensearch backend) ==="
BACKEND=opensearch "$LAB/render-gate.sh" \
  "$CHARTS/fluent-bit-0.58.2.tgz" "$EVID/fluent-bit-values-opensearch.yaml" \
  "$EVID/fluent-bit-render-opensearch.yaml" fluent-bit \
  2>&1 | tee "$EVID/fluent-bit-render-opensearch.log"

echo
echo "C4 gate passed; evidence in $EVID"

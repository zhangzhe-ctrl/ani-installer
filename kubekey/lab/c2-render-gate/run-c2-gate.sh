#!/usr/bin/env bash
# C2 gate driver: run the Go checks, render the metrics role, and render the
# kube-prometheus-stack chart offline.
#
# The metrics role's *tasks* file is rendered as well as its values. That is the
# specific gap that let a real defect through: nine tasks read
# `.ani.metrics.namespace`, which is the selection row and carries only
# `enabled`, while the namespace actually lives at `.ani.components.metrics`
# `.namespace`. Every earlier check rendered values.yaml, verify.sh and
# connection.md — never the task file — so the broken path only surfaced on the
# node as `error: no objects passed to apply`.
#
# Everything here runs on fedora, where the packaged helm binary and the locked
# charts already live. Nothing touches a node or a cluster.
#
# Usage: run-c2-gate.sh [run-root]
#   run-root defaults to ~/ani-installer-runs/observability-20260919
set -euo pipefail

RUN_ROOT="${1:-$HOME/ani-installer-runs/observability-20260919}"
SRC="$RUN_ROOT/src/kubekey"
# The repo root is the directory that contains the kubekey/ module; the role
# templates live under it and the render program runs with the module as its
# working directory.
REPO="$RUN_ROOT/src"
# The locked charts live in this task's own inputs directory, not in a release
# artifact: C2 only renders, it does not build a release.
CHARTS="$RUN_ROOT/inputs/charts"
HELM="${HELM:-$HOME/ani-installer-runs/foundation-20260918/inputs/tools/helm}"
LAB="$RUN_ROOT/lab/c2-render-gate"
EVID="$RUN_ROOT/evidence/c2-render-gate"

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

echo "=== gofmt ==="
cd "$SRC"
unformatted="$(gofmt -l pkg/ani builtin/core/roles 2>/dev/null || true)"
if [ -n "$unformatted" ]; then
  echo "these files are not gofmt-clean:" >&2
  echo "$unformatted" >&2
  exit 1
fi
echo "gofmt clean (pkg/ani, builtin/core/roles)"

echo "=== go build ==="
go build ./pkg/ani/... ./cmd/... 2>&1 | tee "$EVID/go-build.log"

echo "=== go vet ==="
go vet ./pkg/ani/... 2>&1 | tee "$EVID/go-vet.log"

echo "=== go test pkg/ani ==="
go test ./pkg/ani/... 2>&1 | tee "$EVID/go-test.log"

echo "=== render metrics values ==="
"$LAB/render-values.sh" "$REPO" "$SRC/ani/images.tsv" metrics \
  "$EVID/metrics-values.yaml"

# The tasks file, which is what the node actually executes.
echo "=== render metrics tasks ==="
TEMPLATE_KIND=tasks "$LAB/render-values.sh" "$REPO" "$SRC/ani/images.tsv" metrics \
  "$EVID/metrics-tasks.yaml"

# A task that reads a key the installer does not build renders to `<no value>`
# and the value lands in a shell command, so grep the render for the key shape
# as a second, independent check on top of the renderer's own guard.
if grep -q "no value" "$EVID/metrics-tasks.yaml"; then
  echo "the rendered metrics tasks still contain a missing value" >&2
  exit 1
fi
echo "  OK   no missing value in the rendered metrics tasks"
if grep -qE '\.ani\.metrics\.' "$EVID/metrics-tasks.yaml"; then
  echo "the rendered metrics tasks read a bare .ani.metrics. path" >&2
  exit 1
fi
echo "  OK   no bare .ani.metrics. path in the rendered metrics tasks"

echo "=== render kube-prometheus-stack chart ==="
"$LAB/render-gate.sh" \
  "$CHARTS/kube-prometheus-stack-85.4.0.tgz" "$EVID/metrics-values.yaml" \
  "$EVID/metrics-render.yaml" \
  2>&1 | tee "$EVID/metrics-render-gate.log"
echo
echo "C2 gate passed; evidence in $EVID"

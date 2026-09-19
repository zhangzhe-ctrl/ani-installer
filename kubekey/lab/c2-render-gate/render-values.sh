#!/usr/bin/env bash
# Render the metrics role's values.yaml with a realistic site context.
#
# The context is built by the real Go code, not by hand: this runs a small
# program that loads the packaged images.tsv, calls the same
# SplitImageReferences the installer calls, and executes the role template with
# the result. A key path mismatch or a missing image therefore shows up here
# rather than during a node install.
set -euo pipefail

REPO="${1:?usage: render-values.sh <repo-root> <images.tsv> <out-values.yaml>}"
IMAGE_TSV="${2:?}"
OUT="${3:?}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Guard the two paths that are destructive if swapped: the image table is
# packaged material and must never be written to, and a values file must never
# be read as material. An earlier invocation passed them in the wrong order and
# overwrote the image table, so the roles below fail loudly instead.
ROLE_TEMPLATE="$REPO/kubekey/builtin/core/roles/ani/metrics/templates/values.yaml"

case "$IMAGE_TSV" in
  */images.tsv) : ;;
  *) echo "render-values: second argument must be the images.tsv path, got '$IMAGE_TSV'" >&2; exit 2 ;;
esac
case "$OUT" in
  *.yaml|*.yml) : ;;
  *) echo "render-values: third argument must be an output .yaml path, got '$OUT'" >&2; exit 2 ;;
esac
[ -f "$IMAGE_TSV" ] || { echo "render-values: image table not found: $IMAGE_TSV" >&2; exit 2; }
[ -f "$ROLE_TEMPLATE" ] || { echo "render-values: role template not found: $ROLE_TEMPLATE" >&2; exit 2; }
[ "$IMAGE_TSV" != "$OUT" ] || { echo "render-values: input and output are the same file" >&2; exit 2; }
# The Go program runs with the module directory as its working directory, so a
# relative output path would land there instead of where the caller asked.
mkdir -p "$(dirname "$OUT")"
OUT="$(cd "$(dirname "$OUT")" && pwd)/$(basename "$OUT")"
IMAGE_TSV="$(cd "$(dirname "$IMAGE_TSV")" && pwd)/$(basename "$IMAGE_TSV")"
ROLE_TEMPLATE="$(cd "$(dirname "$ROLE_TEMPLATE")" && pwd)/$(basename "$ROLE_TEMPLATE")"
REPO="$(cd "$REPO" && pwd)"
if [ -e "$OUT" ] && ! grep -q 'ani-metrics' "$OUT"; then
  echo "render-values: refusing to overwrite $OUT, which is not a rendered values file" >&2
  exit 2
fi

# The program lives inside the repo module so it can import pkg/ani directly.
# It is written into a throwaway package directory and removed by the trap.
PKG="$REPO/kubekey/pkg/zrendercheck"
mkdir -p "$PKG"
trap 'rm -rf "$PKG" "$TMP"' EXIT

cat > "$PKG/main.go" <<'GO_EOF'
// Command zrendercheck renders one component role template with the context the
// installer builds. It exists so the render gate exercises the real key paths
// instead of a hand-written stand-in that can drift.
package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"text/template"

	"github.com/kubesphere/kubekey/v4/pkg/ani"
)

func main() {
	roleTemplate, imagesTSV, out := os.Args[1], os.Args[2], os.Args[3]

	rows, err := readLines(imagesTSV)
	if err != nil {
		die(err)
	}
	table, err := ani.LoadImageTable(rows)
	if err != nil {
		die(err)
	}
	const registry = "192.0.2.11:5000"
	whole, err := ani.LocalImageReferences(table, registry)
	if err != nil {
		die(err)
	}
	parts, err := ani.ComponentImageParts(table, registry)
	if err != nil {
		die(err)
	}
	components := ani.ComponentSpecForRender("ani-lab")

	ctx := map[string]any{
		"ani": map[string]any{
			"registry":    registry,
			"images":      whole,
			"image_parts": parts,
			"components":  components,
		},
		"kubernetes": map[string]any{"cluster_name": "ani-lab"},
	}

	raw, err := os.ReadFile(roleTemplate)
	if err != nil {
		die(err)
	}
	tmpl, err := template.New("t").Parse(string(raw))
	if err != nil {
		die(err)
	}
	buf := &bytes.Buffer{}
	if err := tmpl.Execute(buf, ctx); err != nil {
		die(err)
	}
	if strings.Contains(buf.String(), "{{") || strings.Contains(buf.String(), "<no value>") {
		die(fmt.Errorf("rendered output still contains template markers or a missing value"))
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o600); err != nil {
		die(err)
	}
	fmt.Printf("rendered %s -> %s (%d bytes)\n", roleTemplate, out, buf.Len())
}

func readLines(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n"), nil
}

func die(err error) {
	fmt.Fprintf(os.Stderr, "render-values: %v\n", err)
	os.Exit(1)
}
GO_EOF

( cd "$REPO/kubekey" && go run ./pkg/zrendercheck \
    "$ROLE_TEMPLATE" "$IMAGE_TSV" "$OUT" )

python3 -c "import sys, yaml; yaml.safe_load(open(sys.argv[1])); print('values are valid YAML')" "$OUT"

#!/usr/bin/env bash
# Render a component role's values.yaml with a realistic site context.
#
# The context is built by the real Go code, not by hand: this runs a small
# program that loads the packaged images.tsv, calls the same
# SplitImageReferences the installer calls, and executes the role template with
# the result. A key path mismatch or a missing image therefore shows up here
# rather than during a node install.
#
# Usage: render-values.sh <repo-root> <images.tsv> <role> <out-values.yaml>
#
# <role> is a directory name under builtin/core/roles/ani (metrics, loki,
# fluent-bit, ...). The logging backend used for the render is taken from
# BACKEND (default loki), because two of the log roles branch on it.
set -euo pipefail

REPO="${1:?usage: render-values.sh <repo-root> <images.tsv> <role> <out-values.yaml>}"
IMAGE_TSV="${2:?}"
ROLE="${3:?}"
OUT="${4:?}"
BACKEND="${BACKEND:-loki}"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Guard the two paths that are destructive if swapped: the image table is
# packaged material and must never be written to, and a values file must never
# be read as material. An earlier invocation passed them in the wrong order and
# overwrote the image table, so the roles below fail loudly instead.
# TEMPLATE_KIND selects which file of the role is rendered. values.yaml is the
# default, but the tasks file is rendered too: a task that reads a context key
# the role never receives renders to `<no value>` on the node and only then
# fails, which is exactly how the metrics role shipped a broken namespace path
# past every offline check. Both files are `text/template` and take the same
# context, so the same program renders them.
TEMPLATE_KIND="${TEMPLATE_KIND:-values}"
case "$TEMPLATE_KIND" in
  values) ROLE_TEMPLATE="$REPO/kubekey/builtin/core/roles/ani/$ROLE/templates/values.yaml" ;;
  tasks)  ROLE_TEMPLATE="$REPO/kubekey/builtin/core/roles/ani/$ROLE/tasks/main.yaml" ;;
  verify) ROLE_TEMPLATE="$REPO/kubekey/builtin/core/roles/ani/$ROLE/templates/verify.sh" ;;
  *) echo "render-values: TEMPLATE_KIND must be values, tasks or verify, got '$TEMPLATE_KIND'" >&2; exit 2 ;;
esac

case "$IMAGE_TSV" in
  */images.tsv) : ;;
  *) echo "render-values: second argument must be the images.tsv path, got '$IMAGE_TSV'" >&2; exit 2 ;;
esac
case "$ROLE" in
  */*|"") echo "render-values: third argument must be a role name, got '$ROLE'" >&2; exit 2 ;;
esac
case "$OUT" in
  *.yaml|*.yml) : ;;
  *.sh) [ "$TEMPLATE_KIND" = verify ] || { echo "render-values: only TEMPLATE_KIND=verify may write a .sh output, got '$OUT'" >&2; exit 2; } ;;
  *) echo "render-values: fourth argument must be an output .yaml (or .sh for verify) path, got '$OUT'" >&2; exit 2 ;;
esac
case "$BACKEND" in
  loki|opensearch) : ;;
  *) echo "render-values: BACKEND must be loki or opensearch, got '$BACKEND'" >&2; exit 2 ;;
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
# The guard only makes sense for values files: a tasks file legitimately carries
# neither an `ani-` name nor a `kind:` while an unrelated file could, so applying
# it to tasks would either reject every valid render or wave through the wrong one.
if [ -e "$OUT" ] && [ "$TEMPLATE_KIND" = values ] && ! grep -qE 'ani-|kind:' "$OUT"; then
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
	"github.com/kubesphere/kubekey/v4/pkg/converter/tmpl"
)

func main() {
	roleTemplate, imagesTSV, backend, out := os.Args[1], os.Args[2], os.Args[3], os.Args[4]

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
	// The two log roles branch on the selected backend, and the render gate has
	// to be able to exercise both branches, so the backend is overridden here
	// rather than taken from the default site config.
	if logging, ok := components["logging"].(map[string]any); ok {
		logging["backend"] = backend
	}

	// The context mirrors the keys config.go actually puts under .ani. The tasks
	// files (not just values.yaml) read artifact_root, nodes, node_addresses and
	// installer_node, so leaving them out would report a missing value for a key
	// the installer does provide — a false alarm that hides the real defect the
	// tasks render exists to catch.
	ctx := map[string]any{
		"ani": map[string]any{
			"registry":       registry,
			"images":         whole,
			"image_parts":    parts,
			"components":     components,
			"artifact_root":  "/opt/ani-installer/artifacts/ani-artifact",
			"nodes":          []string{"ani-lab-1", "ani-lab-2", "ani-lab-3"},
			"node_addresses": []string{"192.0.2.21", "192.0.2.22", "192.0.2.23"},
			"installer_node": "ani-lab-1",
		},
		"kubernetes": map[string]any{"cluster_name": "ani-lab"},
		// The inventory groups are provided because a node-level task resolves
		// its target list from .groups.k8s_cluster (the same pattern the kcn role
		// uses).
		"groups": map[string]any{
			"k8s_cluster": []string{"ani-lab-1", "ani-lab-2", "ani-lab-3"},
		},
		// The runner binds .item for the duration of each `loop:` iteration, so a
		// task that pairs `loop:` with `delegate_to: "{{ .item }}"` is correct.
		// The render supplies a placeholder because the value is only meaningful
		// per iteration; leaving it unset would fail every looped node task and
		// drown the real signal this check exists to produce.
		"item": "ani-lab-1",
	}

	raw, err := os.ReadFile(roleTemplate)
	if err != nil {
		die(err)
	}
	// The installer's own function map, not a stand-in: a role file that uses
	// `default`, `toJson` or any other helper would otherwise fail here on an
	// undefined function and report a defect where there is none.
	t, err := template.New("t").Funcs(tmpl.FuncMap()).Parse(string(raw))
	if err != nil {
		die(err)
	}
	buf := &bytes.Buffer{}
	if err := t.Execute(buf, ctx); err != nil {
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
    "$ROLE_TEMPLATE" "$IMAGE_TSV" "$BACKEND" "$OUT" )

# The rendered tasks file is a YAML list of tasks, so it parses the same way.
# Parsing it catches a template that renders to something structurally broken
# even when no key was missing.
#
# One construct needs care: the documented node-iteration form is
#   loop: "{{ .groups.k8s_cluster | default list | toJson }}"
# whose `toJson` deliberately emits a JSON array *inside* the surrounding
# double quotes. After substitution that reads `loop: "["ani-lab-1",...]"`,
# which is invalid YAML even though the installer's loop parser consumes it
# correctly (the kcn role ships the same line and passed a real install). The
# substitution is therefore unwrapped only for the parse, so a genuine YAML
# break elsewhere still fails.
# A20 (evidence h4loki-a24): verify.sh never had an offline render check, so a
# wrong .ani.images key (`busybox:1.37` vs the table's `busybox:1.37.0`)
# rendered to `<no value>` and every marker pod died with InvalidImageName on
# a real install. TEMPLATE_KIND=verify renders the verify script with the same
# real context as values/tasks: the product is bash, so it is syntax-checked
# with bash -n here and scanned for Go template nil spellings in the python
# step below; the YAML parse does not apply to it.
if [ "$TEMPLATE_KIND" = verify ]; then
  bash -n "$OUT" || { echo "render-values: rendered verify is not valid bash" >&2; exit 1; }
  echo "rendered verify is valid bash"
fi
python3 - "$OUT" "$TEMPLATE_KIND" <<'PY_EOF'
import re
import sys
import yaml

path, kind = sys.argv[1], sys.argv[2]
text = open(path).read()
if kind == "verify":
    # Both Go text/template nil spellings mean a key the installer does not
    # provide (or a typo'd key) — exactly the A20 defect class.
    for bad in ("<no value>", "<nil>"):
        if bad in text:
            print("render-values: rendered verify contains %s (a missing or mistyped key)" % bad, file=sys.stderr)
            sys.exit(1)
    print("rendered verify has no unresolved template values")
    sys.exit(0)
# `key: "<json array>"` -> `key: <json array inline-safe form>`
text = re.sub(r'(\bloop:\s*)"(\[.*?\])"', r'\1\2', text)
try:
    yaml.safe_load(text)
except yaml.YAMLError as err:
    print("render-values: rendered %s is not valid YAML: %s" % (kind, err), file=sys.stderr)
    sys.exit(1)
print("rendered %s is valid YAML" % kind)
PY_EOF

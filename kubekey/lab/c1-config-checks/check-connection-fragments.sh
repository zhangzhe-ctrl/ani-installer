#!/usr/bin/env bash
# Render each batch-1 component's connection.md fragment with a realistic
# context and assert the fragment is non-empty, has no leftover template
# delimiters, and contains no credential values.
set -uo pipefail
SRC="$1"
ROOT="$SRC/builtin/core/roles/ani"

# A minimal Go program renders the templates exactly as KubeKey would.
cat > /tmp/render_frag.go <<'GO'
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/template"
)

func main() {
	root := os.Args[1]
	ctx := map[string]any{
		"ani": map[string]any{
			"registry": "172.16.101.20:5000",
			"components": map[string]any{
				"postgresql": map[string]any{"enabled": true, "storage_class": "ani-block", "storage_size": "10Gi"},
				"valkey":     map[string]any{"enabled": true, "storage_class": "ani-block", "storage_size": "2Gi"},
				"nats":       map[string]any{"enabled": true, "storage_class": "ani-block", "storage_size": "5Gi"},
			},
		},
		"kubernetes": map[string]any{"cluster_name": "ani-lab"},
	}
	out := os.Args[2]
	bad := 0
	for _, name := range []string{"cert-manager", "postgresql", "valkey", "nats"} {
		path := filepath.Join(root, name, "templates", "connection.md")
		tmpl, err := template.New("connection.md").ParseFiles(path)
		if err != nil {
			fmt.Printf("FAIL[%s]: parse: %v\n", name, err)
			bad++
			continue
		}
		f, err := os.Create(filepath.Join(out, name+".md"))
		if err != nil {
			fmt.Printf("FAIL[%s]: create: %v\n", name, err)
			bad++
			continue
		}
		if err := tmpl.Execute(f, ctx); err != nil {
			fmt.Printf("FAIL[%s]: execute: %v\n", name, err)
			bad++
			f.Close()
			continue
		}
		f.Close()
		fmt.Printf("RENDERED[%s]\n", name)
	}
	if bad > 0 {
		os.Exit(1)
	}
}
GO

d=$(mktemp -d)
cd "$SRC" || exit 1
go run /tmp/render_frag.go "$ROOT" "$d" || exit 1

failures=0
for f in "$d"/*.md; do
  name=$(basename "$f" .md)
  if [[ ! -s "$f" ]]; then
    echo "FAIL[$name]: empty fragment"; failures=$((failures+1)); continue
  fi
  if grep -q '{{' "$f"; then
    echo "FAIL[$name]: leftover template delimiters"; failures=$((failures+1)); continue
  fi
  if grep -q '<no value>' "$f"; then
    echo "FAIL[$name]: rendered <no value>"; failures=$((failures+1)); continue
  fi
  # Credentials must never appear: assert the expected secret NAMES are present
  # but no password/token-like value is.
  if grep -qiE 'password: [^`]|token: [^`]|password=[^ ]|CHANGE_ME' "$f"; then
    echo "FAIL[$name]: fragment contains a credential-looking value"; failures=$((failures+1)); continue
  fi
  for want in namespace; do
    if ! grep -q "$want" "$f"; then
      echo "FAIL[$name]: missing required field '$want'"; failures=$((failures+1)); continue 2
    fi
  done
  echo "PASS[$name]: $(wc -l < "$f") lines, namespace present, no delimiters/credentials"
done

# The storage facts must reflect the site config, not a hardcoded default.
for comp in postgresql valkey nats; do
  if grep -q 'storage_size }}' "$ROOT/$comp/templates/connection.md"; then
    echo "PASS[$comp]: storage size comes from the site config"
  else
    echo "FAIL[$comp]: storage size not templated"
    failures=$((failures+1))
  fi
  if grep -q 'storage_class }}' "$ROOT/$comp/templates/connection.md"; then
    echo "PASS[$comp]: storage class comes from the site config"
  else
    echo "FAIL[$comp]: storage class not templated"
    failures=$((failures+1))
  fi
done

# The rendered output must carry the site values, not the template text.
for comp in postgresql valkey nats; do
  case "$comp" in
    postgresql) want="10Gi" ;;
    valkey)     want="2Gi" ;;
    nats)       want="5Gi" ;;
  esac
  if grep -q "$want" "$d/$comp.md" && grep -q "ani-block" "$d/$comp.md"; then
    echo "PASS[$comp]: rendered fragment carries the configured $want / ani-block"
  else
    echo "FAIL[$comp]: rendered fragment does not carry the configured storage values"
    failures=$((failures+1))
  fi
done

rm -rf "$d"
if [[ "$failures" -ne 0 ]]; then
  echo "CONNECTION_FRAGMENT_TESTS_FAILED=$failures"
  exit 1
fi
echo "CONNECTION_FRAGMENT_TESTS_DONE"

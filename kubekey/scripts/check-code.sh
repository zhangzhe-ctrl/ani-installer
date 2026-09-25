#!/usr/bin/env bash
# check-code.sh — the single code gate for this module.
#
# CI (.github/workflows/ani-check.yaml) and scripts/build-code.sh both call
# exactly this script, so a local run and CI cannot diverge: there is one gate
# implementation, not two. A missing tool fails here; nothing is installed and
# no check is skipped or permanently disabled.
#
# What it runs, cheapest first, so a broken task file fails before a full build:
#   1. required tools and a Go toolchain that satisfies go.mod
#   2. ANI role task shape + shell syntax + no-network-fetch check
#   3. go build ./... , go vet ./pkg/ani/... , go test ./pkg/ani/...
#   4. the behavioural suites in scripts/test-*.py (credentials, task errors,
#      apt repository, kubeconfig export)
#   5. bash -n over every release script
#
# The real render gates (template FuncMap, Chart values, connection docs) live
# in pkg/ani's Go tests and are therefore already part of step 3.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
source "$ROOT/scripts/release-guard.sh"

GO_BIN="${GO_BIN:-$(command -v go || true)}"
step() { printf '\n== %s ==\n' "$*"; }
fail() { echo "check-code FAILED: $*" >&2; exit 1; }

step "required tools (this gate never installs anything)"
for bin in go python3 awk find sort xargs sha256sum bash; do
  command -v "$bin" >/dev/null 2>&1 || fail "missing required tool: $bin"
done
[[ -n "$GO_BIN" && -x "$GO_BIN" ]] || fail "go toolchain not found; set GO_BIN to an executable go (never downloaded here)"
echo "go binary: $GO_BIN"
echo "python3: $(python3 -V 2>&1)"

step "Go toolchain satisfies go.mod (GOTOOLCHAIN=${GOTOOLCHAIN:-unset})"
required="$(awk '/^go /{print $2; exit}' go.mod)"
[[ -n "$required" ]] || fail "go.mod has no go directive"
actual="$("$GO_BIN" version 2>/dev/null | awk '{print $3}')"
[[ -n "$actual" ]] || fail "cannot read a version from $GO_BIN"
actual="${actual%%-*}"                       # go1.26.7-X:nodwarf5 -> go1.26.7
lowest="$(printf '%s\n%s\n' "go$required" "$actual" | LC_ALL=C sort -V | head -n1)"
if [[ "$lowest" != "go$required" ]]; then
  fail "go toolchain $actual is older than go.mod's go $required — install a satisfying toolchain deliberately; this gate never downloads one (GOTOOLCHAIN=${GOTOOLCHAIN:-unset})"
fi
echo "go $actual >= go.mod go $required"

step "ANI role tasks: shape, shell syntax, no network fetch"
python3 - "$ROOT" <<'PY_EOF'
import glob
import os
import re
import subprocess
import sys

import yaml

root = sys.argv[1]
ACTION_KEYS = {
    "command", "shell", "template", "copy", "file", "include", "include_tasks",
    "include_vars", "set_fact", "debug", "assert", "add_hostvars", "fetch",
    "image", "gen_cert", "http_get_file", "setup", "prometheus", "result",
    "testutil", "module",
}
META_KEYS = {
    "name", "when", "loop", "with_items", "delegate_to", "delegate_facts",
    "register", "vars", "tags", "ignore_errors", "become", "environment",
    "changed_when", "failed_when", "until", "retries", "delay", "run_once",
    "serial", "any_errors_fatal", "block", "rescue", "always", "notify",
    "args", "no_log", "check_mode", "loop_control", "throttle",
}

errors: list[str] = []
files = sorted(glob.glob(os.path.join(root, "builtin/core/roles/ani/*/tasks/*.yaml")))
if not files:
    sys.exit("no ANI role task files found")
for path in files:
    rel = os.path.relpath(path, root)
    try:
        doc = yaml.safe_load(open(path, encoding="utf-8"))
    except Exception as exc:                                   # noqa: BLE001
        errors.append(f"{rel}: YAML parse error: {exc}")
        continue
    if not isinstance(doc, list):
        errors.append(f"{rel}: a tasks file must be a YAML list")
        continue
    for index, task in enumerate(doc, 1):
        where = f"{rel}:task {index}"
        if not isinstance(task, dict):
            errors.append(f"{where}: task must be a mapping")
            continue
        if not task.get("name"):
            errors.append(f"{where}: task has no name")
        actions = [k for k in task if k in ACTION_KEYS]
        unknown = sorted(k for k in task if k not in ACTION_KEYS and k not in META_KEYS)
        if unknown:
            errors.append(f"{where}: unknown task keys {unknown}")
        if len(actions) != 1:
            errors.append(f"{where}: expected exactly one action key, found {actions}")
        command = task.get("command")
        if isinstance(command, str):
            if not command.strip():
                errors.append(f"{where}: empty command block")
            rendered = re.sub(r"\{\{[^}]*\}\}", "x", command)
            checked = subprocess.run(["bash", "-n"], input=rendered, text=True,
                                     capture_output=True)
            if checked.returncode != 0:
                first = (checked.stderr.strip().splitlines() or ["?"])[0]
                errors.append(f"{where}: command is not valid shell: {first}")
            # `bash -n` only *warns* about a here-document that is never closed
            # (exit status stays 0), so the delimiters are checked explicitly:
            # an unfinished heredoc would swallow the rest of the task at runtime.
            for match in re.finditer(r"<<-?\s*['\"]?([A-Za-z_][A-Za-z0-9_]*)['\"]?",
                                     command):
                delimiter = match.group(1)
                if not re.search(rf"(?m)^\s*{re.escape(delimiter)}\s*$",
                                 command[match.end():]):
                    errors.append(f"{where}: here-document <<{delimiter} is not terminated")
            for bad in ("helm repo", "helm pull"):
                if bad in command:
                    errors.append(f"{where}: network material fetch ({bad!r})")
            for match in re.finditer(r"https://([^\s\"'/]*)", command):
                host = match.group(1)
                if host and not host.startswith("$") and "{{" not in host:
                    errors.append(f"{where}: literal external URL https://{host}")

if errors:
    for item in errors:
        print(f"  {item}", file=sys.stderr)
    sys.exit(f"{len(errors)} ANI task problem(s)")
print(f"OK: {len(files)} ANI task files validated")
PY_EOF

step "go build ./..."
"$GO_BIN" build ./...

step "go vet ./pkg/ani/..."
"$GO_BIN" vet ./pkg/ani/...

step "go test -count=1 ./pkg/ani/... (render gates and behaviour tests)"
"$GO_BIN" test -count=1 ./pkg/ani/...

step "behaviour suites in scripts/"
for suite in test-lab-credentials.py test-ani-task-errors.py \
             test-ceph-storage-safety.py test-build-offline-materials.py \
             test-debian-repository.py test-kubeconfig-export.py \
             test-check-code.py; do
  if [[ ! -f "scripts/$suite" ]]; then
    fail "scripts/$suite is missing (the gate must not silently shrink)"
  fi
  echo "--- scripts/$suite"
  # ANI_CHECK_IN_COPY is set by scripts/test-check-code.py when it runs this gate
  # on a throwaway copy of the module. The meta-test uses it to avoid copying the
  # copy (which would copy again, forever); every other suite and every gate step
  # still runs in full, and a top-level run never sets it.
  ANI_CHECK_IN_COPY="${ANI_CHECK_IN_COPY:-}" python3 "scripts/$suite"
done

step "shell syntax of the release scripts"
for script in scripts/*.sh; do
  bash -n "$script" || fail "shell syntax error in $script"
done
echo "OK: $(git ls-files 'scripts/*.sh' 2>/dev/null | wc -l || ls scripts/*.sh | wc -l) release scripts checked"

echo
echo "check-code PASSED: go $actual, tree $(ani_tree_fingerprint "$ROOT")"

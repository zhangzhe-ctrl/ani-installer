#!/usr/bin/env bash
# Shared by the opt-in first-install role and an existing-cluster supplement.
set -euo pipefail
umask 077
package= kubeconfig= stage=all
while [[ $# -gt 0 ]]; do
  case "$1" in
    --package) package="${2:?}"; shift 2 ;;
    --kubeconfig) kubeconfig="${2:?}"; shift 2 ;;
    --stage) stage="${2:?}"; shift 2 ;;
    *) echo "usage: ani-system.sh --package DIR --kubeconfig FILE --stage {preflight|crds|prepare|controllers|init|core|apps|gateway|wait|all}" >&2; exit 2 ;;
  esac
done
[[ -d "$package" && -f "$kubeconfig" ]] || { echo 'application package and kubeconfig are required' >&2; exit 2; }
case "$stage" in preflight|crds|prepare|controllers|init|core|apps|gateway|wait|all) ;; *) echo 'unknown application stage' >&2; exit 2 ;; esac
package="$(cd "$package" && pwd)"
export KUBECONFIG="$kubeconfig"
K=(kubectl --request-timeout=30s)
task=ani-system-fast-20260930

preflight() {
  (cd "$package" && sha256sum -c SHA256SUMS)
  for file in expected-nodes.txt inventory.tsv workloads.tsv; do
    [[ -s "$package/$file" ]] || { echo "missing package file: $file" >&2; return 1; }
  done
  local got
  got="$("${K[@]}" get nodes -o jsonpath='{range .items[*]}{range .status.addresses[?(@.type=="InternalIP")]}{.address}{"\n"}{end}{end}' | sort)"
  [[ "$got" == "$(sort "$package/expected-nodes.txt")" ]] || { echo 'target node addresses do not match this application package' >&2; return 1; }
  if [[ -s "$package/expected-cluster-uid.txt" ]]; then
    [[ "$("${K[@]}" get namespace kube-system -o jsonpath='{.metadata.uid}')" == "$(cat "$package/expected-cluster-uid.txt")" ]] || { echo 'target cluster UID mismatch' >&2; return 1; }
  fi
  [[ -s "$package/expected-node-ports.txt" ]] || { echo 'missing declared application NodePorts' >&2; return 1; }
  "${K[@]}" get services -A -o json | python3 -c '
import json,sys
from pathlib import Path
ports={int(x) for x in Path(sys.argv[1]).read_text().splitlines()}
for service in json.load(sys.stdin)["items"]:
    meta=service["metadata"]
    if meta.get("labels",{}).get("ani.io/app-task")==sys.argv[2]: continue
    if any(p.get("nodePort") in ports for p in service["spec"].get("ports",[])):
        raise SystemExit("application NodePort conflict: "+meta["namespace"]+"/"+meta["name"])
' "$package/expected-node-ports.txt" "$task"
}

check_ownership() {
  local selected="$1" group ns kind name owner
  while IFS=$'\t' read -r group ns kind name; do
    [[ "$group" == "$selected" ]] || continue
    local args=()
    [[ "$ns" == '-' ]] || args=(-n "$ns")
    # --ignore-not-found distinguishes absence from authentication/API failure.
    owner="$("${K[@]}" "${args[@]}" get "$kind" "$name" --ignore-not-found -o jsonpath='{.metadata.labels.ani\.io/app-task}{"|"}{.metadata.name}')"
    [[ -z "$owner" || "$owner" == "$task|$name" ]] || { echo "resource conflict: $ns/$kind/$name ($selected paused)" >&2; return 1; }
  done < "$package/inventory.tsv"
}

apply_stage() {
  local selected="$1"
  check_ownership "$selected"
  [[ -s "$package/$selected.yaml" ]] || { echo "missing stage manifest $selected.yaml" >&2; return 1; }
  "${K[@]}" apply --server-side --field-manager=ani-system-fast -f "$package/$selected.yaml"
}

initialize() {
  [[ -s "$package/sql/bootstrap.sql" && -s "$package/sql/migrations.tsv" ]] || { echo 'database initialization material is missing' >&2; return 1; }
  # The existing base administrator is used only for database initialization.
  # Application containers consume separate non-superuser connection strings.
  local pg=("${K[@]}" -n ani-platform exec -i postgresql-0 -c postgresql --)
  local psql_cmd='export PGPASSWORD="$(cat /etc/postgres-admin/postgres-password)"; exec psql -X -v ON_ERROR_STOP=1 -U postgres'
  "${pg[@]}" sh -ec "$psql_cmd -d postgres" < "$package/sql/bootstrap.sql"
  "${pg[@]}" sh -ec "$psql_cmd -d ani_fast_20260930" < "$package/sql/ledger.sql"
  local file digest recorded
  while IFS=$'\t' read -r file digest; do
    [[ "$file" =~ ^[0-9A-Za-z_-]+\.sql$ && "$digest" =~ ^[a-f0-9]{64}$ ]] || { echo 'invalid migration list' >&2; return 1; }
    recorded="$("${pg[@]}" sh -ec "$psql_cmd -At -d ani_fast_20260930 -c \"SELECT sha256 FROM ani_fast_migrations WHERE file='$file'\"")"
    if [[ -n "$recorded" ]]; then
      [[ "$recorded" == "$digest" ]] || { echo "migration identity mismatch: $file" >&2; return 1; }
      echo "already initialized: $file"
      continue
    fi
    echo "initializing: $file"
    "${pg[@]}" sh -ec "$psql_cmd -d ani_fast_20260930" < "$package/sql/$file"
  done < "$package/sql/migrations.tsv"
}

wait_for_apps() {
  local ns kind name
  while IFS=$'\t' read -r ns kind name; do
    "${K[@]}" -n "$ns" rollout status "$kind/$name" --timeout=300s
  done < "$package/workloads.tsv"
}

install_crds() {
  local file name expected got existing
  [[ -s "$package/crds/specs.tsv" ]] || { echo 'verified CRD material is missing' >&2; return 1; }
  while IFS=$'\t' read -r file name expected; do
    [[ "$file" =~ ^[a-z0-9._-]+\.yaml$ && "$expected" =~ ^[a-f0-9]{64}$ ]] || return 1
    existing="$("${K[@]}" get crd "$name" --ignore-not-found -o name)"
    if [[ -n "$existing" ]]; then
      got="$("${K[@]}" get crd "$name" -o json | python3 -c 'import sys,json,hashlib; print(hashlib.sha256(json.dumps(json.load(sys.stdin)["spec"],sort_keys=True,separators=(",",":")).encode()).hexdigest())')"
      [[ "$got" == "$expected" ]] || { echo "shared CRD schema conflict: $name (no write)" >&2; return 1; }
      echo "compatible shared CRD reused without writes: $name"
    else
      "${K[@]}" apply --server-side --field-manager=ani-system-fast -f "$package/crds/$file"
    fi
    "${K[@]}" wait --for=condition=Established "crd/$name" --timeout=120s
  done < "$package/crds/specs.tsv"
}

preflight
case "$stage" in
  preflight) ;;
  crds) install_crds ;;
  prepare) apply_stage prepare ;;
  init) initialize ;;
  controllers|core|apps|gateway) apply_stage "$stage" ;;
  wait) wait_for_apps ;;
  all)
    install_crds
    apply_stage prepare
    apply_stage controllers
    initialize
    apply_stage core
    apply_stage apps
    apply_stage gateway
    wait_for_apps
    ;;
esac
# Application results are separate from the install-success/components ledger.
echo "ANI application stage $stage completed"

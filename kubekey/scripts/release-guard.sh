#!/usr/bin/env bash
# Shared release hygiene guard for ANI code releases and artifacts.
#
# Purpose: lab-only scripts and any credential-bearing file must never appear in
# a product release. This only checks file names/paths — it never reads or
# prints file contents.
#
# Sourced by build-code.sh / build-offline.sh; the same check can be run against
# a directory without building anything:
#
#   ANI_RELEASE_CHECK_ONLY=<dir> bash kubekey/scripts/release-guard.sh

ani_release_forbidden_names=(
  restore_esxi_snapshots.sh
  run_on_node.sh
  lab.env
  askpass.sh
  node-password
  kubeconfig
  admin.conf
  id_rsa
  id_ed25519
)

ani_assert_release_clean() {
  local dir="$1"
  local hits=()
  local name hit

  for name in "${ani_release_forbidden_names[@]}"; do
    if [[ -e "$dir/$name" ]]; then
      hits+=("$dir/$name")
    fi
  done
  while IFS= read -r hit; do
    hits+=("$hit")
  done < <(find "$dir" -maxdepth 3 \( -name '*.pem' -o -name '*.key' -o -name '*credential*' \) -print 2>/dev/null)

  if ((${#hits[@]} > 0)); then
    printf 'release output contains forbidden credential/lab file: %s\n' "${hits[@]}" >&2
    return 1
  fi
  return 0
}

ani_release_guard_main() {
  if [[ -n "${ANI_RELEASE_CHECK_ONLY:-}" ]]; then
    if ani_assert_release_clean "$ANI_RELEASE_CHECK_ONLY"; then
      echo "release guard: clean -> $ANI_RELEASE_CHECK_ONLY"
      exit 0
    fi
    echo "release guard: FAILED -> $ANI_RELEASE_CHECK_ONLY" >&2
    exit 1
  fi
}

ani_release_guard_main

# ani_tree_fingerprint <dir>: content fingerprint of a source tree.
#
# Used by build-code.sh to prove that the tree which passed the code gate is the
# tree that got packaged: the same digest is taken before the gate, right after
# it, and after the build. VCS metadata and build outputs are excluded, so a
# build cannot invalidate its own fingerprint. This is deliberately simpler than
# the plan's docs/execution/scripts/source-snapshot.py (which is a review aid and
# lives outside the module); the gate only needs "same or not".
ani_tree_fingerprint() {
  local dir="$1"
  (
    cd "$dir" || return 1
    find . -type f \
      -not -path './.git/*' -not -path './_output/*' -not -path './build/*' \
      -not -path './.tmp/*' -not -name '*.tgz' -not -name '*.tar.gz' \
      -print0 | LC_ALL=C sort -z | xargs -0 sha256sum | sha256sum | awk '{print $1}'
  )
}

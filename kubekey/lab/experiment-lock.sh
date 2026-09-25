#!/usr/bin/env bash
# R09/A11: the single experiment lock entry for every lab script.
#
# Usage:
#   experiment-lock.sh acquire  [COMMAND ...]  # hold the lock for the command's
#                                             # lifetime (or until release)
#   experiment-lock.sh release              # release an acquired lock
#   experiment-lock.sh status               # report held/free
#
# The lock is an exclusive flock(1) on ANI_LAB_LOCK (default
# /var/lib/ani-installer/lab.lock). The lock FILE is never deleted: releasing
# the fd is the only way it goes away, so a crashed script never leaves a stale
# marker that would have to be cleaned by hand. When the lock is held, `acquire`
# returns 37 immediately — the caller stops instead of queuing behind a run
# whose remote state is unknown.
set -euo pipefail

ANI_LAB_LOCK="${ANI_LAB_LOCK:-/var/lib/ani-installer/lab.lock}"
LOCK_EXIT=37

mkdir -p "$(dirname "$ANI_LAB_LOCK")"
exec 9>>"$ANI_LAB_LOCK"

case "${1:-}" in
  acquire)
    if ! flock -n 9; then
      echo "experiment lock $ANI_LAB_LOCK is held by another run; refusing to start" >&2
      exit "$LOCK_EXIT"
    fi
    if [[ $# -gt 1 ]]; then
      shift
      "$@"
      flock -u 9
    fi
    ;;
  release)
    flock -u 9
    ;;
  status)
    if flock -n 9; then
      flock -u 9
      echo "free"
    else
      echo "held"
    fi
    ;;
  *)
    echo "usage: $0 {acquire [COMMAND ...]|release|status}" >&2
    exit 2
    ;;
esac

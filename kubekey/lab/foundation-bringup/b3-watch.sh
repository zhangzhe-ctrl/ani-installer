#!/usr/bin/env bash
# Usage: b3-watch.sh <attempt-id>   e.g. a4
# Fires the persistence test and the independent verify sequentially the moment
# the install finishes; ALL evidence files are attempt-suffixed.
set -uo pipefail
ATT="${1:-manual}"
R=~/ani-installer-runs/foundation-20260918
E=$R/evidence
A=~/ani-installer-runs/platform-20260918/access
export SSH_ASKPASS="$A/askpass.sh" SSH_ASKPASS_REQUIRE=force
rsh() { setsid -w ssh -n -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 ubuntu@172.16.101.20 "$@"; }
for i in $(seq 1 150); do
  if rsh 'grep -q "INSTALL_EXIT=" /tmp/ani-install-B3-a1.stdout 2>/dev/null'; then break; fi
  sleep 8
done
echo "install finished; firing checks at $(date -u +%H:%M:%S)" > "$E/b3$ATT-watch.log"
bash ~/ani-ops/run_on_node.sh 172.16.101.20 "$R/lab/b3-persist.sh" sudo < /dev/null > "$E/b3$ATT-persist.log" 2>&1
echo "persist_rc=$?" >> "$E/b3$ATT-watch.log"
bash "$R/lab/run-verify-b3.sh" < /dev/null > "$E/b3$ATT-verify.log" 2>&1
echo "verify_rc=$?" >> "$E/b3$ATT-watch.log"
echo "ALL_DONE $(date -u +%H:%M:%S)" >> "$E/b3$ATT-watch.log"

#!/usr/bin/env bash
set -uo pipefail
R=~/ani-installer-runs/foundation-20260918
E=$R/evidence
A=~/ani-installer-runs/platform-20260918/access
export SSH_ASKPASS="$A/askpass.sh" SSH_ASKPASS_REQUIRE=force
rsh() { setsid -w ssh -n -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 ubuntu@172.16.101.20 "$@"; }
for i in $(seq 1 150); do
  if rsh 'grep -q "INSTALL_EXIT=" /tmp/ani-install-B2-a1.stdout 2>/dev/null'; then break; fi
  sleep 8
done
echo "install finished; firing checks at $(date -u +%H:%M:%S)" > "$E/b2a6-watcher.log"
bash ~/ani-ops/run_on_node.sh 172.16.101.20 "$R/lab/b2-persist.sh" sudo < /dev/null > "$E/b2a6-persist.log" 2>&1 &
P1=$!
bash "$R/lab/run-verify-b2.sh" < /dev/null > "$E/b2a6-verify.log" 2>&1 &
P2=$!
wait $P1; echo "persist_rc=$?" >> "$E/b2a6-watcher.log"
wait $P2; echo "verify_rc=$?" >> "$E/b2a6-watcher.log"
echo "ALL_DONE $(date -u +%H:%M:%S)" >> "$E/b2a6-watcher.log"

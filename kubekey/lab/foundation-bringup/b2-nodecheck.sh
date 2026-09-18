#!/usr/bin/env bash
# Clean-state check on all three nodes via the existing fedora->node runner.
set -euo pipefail
RON=~/ani-installer-runs/foundation-20260918/lab/clean-check.sh
for ip in 172.16.101.20 172.16.101.21 172.16.101.22; do
  echo "===================== $ip ====================="
  bash ~/ani-ops/run_on_node.sh "$ip" "$RON" sudo
done

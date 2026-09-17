#!/usr/bin/env bash
# run_on_node.sh — reusable fedora->node runner (no sshpass; ASKPASS auth).
# Usage: run_on_node.sh <node-ip> <fedora-payload-file> [sudo]
#   pushes payload to node:/tmp/_ron.sh and runs it (as root if 3rd arg = sudo).
set -uo pipefail
IP="${1:?need node ip}"
PAYLOAD="${2:?need payload file on fedora}"
SUDO="${3:-}"

ACCESS=/home/chabking/ani-installer-runs/platform-20260918/access
PWFILE="$ACCESS/node-password"
ASKPASS="$ACCESS/askpass.sh"
SSHOPT="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=15 -o NumberOfPasswordPrompts=1"
NODE="ubuntu@$IP"

rsh() { SSH_ASKPASS="$ASKPASS" SSH_ASKPASS_REQUIRE=force setsid -w ssh $SSHOPT "$NODE" "$@"; }

# push payload (stdin free for the file; auth via askpass)
rsh 'cat > /tmp/_ron.sh' < "$PAYLOAD"

if [ "$SUDO" = "sudo" ]; then
  # password on stdin for sudo -S; script already on node
  rsh "sudo -S -p '' bash /tmp/_ron.sh; rc=\$?; rm -f /tmp/_ron.sh; exit \$rc" < "$PWFILE"
else
  rsh 'bash /tmp/_ron.sh; rc=$?; rm -f /tmp/_ron.sh; exit $rc'
fi

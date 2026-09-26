#!/usr/bin/env bash
# ANI Ceph device pre-flight — strictly read-only (R05/A02).
#
# Usage: ceph-preflight.sh NODE [DEVICE...]
#
# Without explicit devices the list is read from the rendered
# /etc/kubernetes/ani/ceph/storage-devices.txt line for NODE, which is how the
# role invokes it. Explicit devices are accepted so the check can be exercised
# offline against fixtures.
#
# The device list is site input (storage.nodes[].devices). Nothing here scans for
# disks and nothing here writes to one: a device is accepted only when it
# resolves to a whole block device that sits on no mounted or in-use chain and
# carries no filesystem, LVM or RAID signature. When any of that cannot be
# established the script fails and the operator decides — Rook is never handed a
# disk this check has not cleared, and no code path below formats or cleans.
set -euo pipefail

NODE="${1:-}"
shift || true
if [ -z "$NODE" ]; then
  echo "ceph pre-flight: usage: ceph-preflight.sh NODE DEVICE [DEVICE...]" >&2
  exit 1
fi
DEVICE_LIST_FILE="${ANI_CEPH_DEVICE_LIST:-/etc/kubernetes/ani/ceph/storage-devices.txt}"
if [ "$#" -eq 0 ]; then
  if [ ! -r "$DEVICE_LIST_FILE" ]; then
    echo "ceph pre-flight FAILED on $NODE: no devices were passed and $DEVICE_LIST_FILE is not readable" >&2
    exit 1
  fi
  # shellcheck disable=SC2046 # the file is one line of space-separated paths
  set -- $(awk -v node="$NODE" '$1 == node { $1 = ""; print; exit }' "$DEVICE_LIST_FILE")
  if [ "$#" -eq 0 ]; then
    echo "ceph pre-flight FAILED on $NODE: $DEVICE_LIST_FILE has no line for this node" >&2
    exit 1
  fi
fi

fail() { echo "ceph pre-flight FAILED on $NODE: $*" >&2; exit 1; }

for tool in lsblk findmnt blkid readlink; do
  command -v "$tool" >/dev/null 2>&1 || fail "required tool missing: $tool"
done

# Every device name backing a mounted filesystem, whatever the mount point: a
# system disk can be reached through /boot or through an LVM volume as well, so
# the check is not limited to /.
mounted_names="$(findmnt -rn -o SOURCE 2>/dev/null | while IFS= read -r src; do
  [ -n "$src" ] || continue
  case "$src" in
    /dev/*) ;;
    *) continue ;;
  esac
  resolved="$(readlink -f "$src" 2>/dev/null || true)"
  [ -n "$resolved" ] || continue
  lsblk -ndo NAME "$resolved" 2>/dev/null | tr -d ' '
done | sort -u | tr '\n' ' ')"

echo "ceph pre-flight on $NODE: $# declared device(s)"

seen=""
for device in "$@"; do
  case " $seen " in *" $device "*) fail "device $device is declared more than once" ;; esac
  seen="$seen $device"

  case "$device" in
    /dev/disk/by-id/*|/dev/disk/by-path/*) : ;;
    *) fail "$device must be declared as /dev/disk/by-id/... or /dev/disk/by-path/...: a bare kernel name is not a stable identity" ;;
  esac

  resolved="$(readlink -f "$device" 2>/dev/null || true)"
  [ -n "$resolved" ] || fail "$device does not exist on $NODE"

  # lsblk reads sysfs, so it is the authority on whether the resolved path is a
  # whole block device: an empty answer means the device is not there at all.
  kind="$(lsblk -ndo TYPE "$resolved" 2>/dev/null | tr -d ' ')"
  [ -n "$kind" ] || fail "$device ($resolved) is not a block device known to lsblk on $NODE"
  [ "$kind" = "disk" ] || fail "$device ($resolved) is a $kind, not a whole disk"

  echo "  -- $device -> $resolved"
  echo "     $(lsblk -ndo NAME,SIZE,TYPE,FSTYPE,MOUNTPOINT "$resolved" 2>/dev/null | tr -s ' ')"
  # blkid's exit status is the answer, not its output: 0 means a signature is
  # there, 2 means the probe found none, and anything else means the probe did
  # not run. Treating `blkid ... || true` as "no signature" would let a failed
  # probe stand in as proof of a blank disk.
  blkid_rc=0
  blkid_out="$(blkid "$resolved" 2>&1)" || blkid_rc=$?
  case "$blkid_rc" in
    0)
      # A signature exists only if blkid actually named one; a zero status with
      # nothing on stdout is not a device to accept and not one to blame on the
      # operator either.
      [ -n "$blkid_out" ] || fail "blkid exited 0 for $resolved without naming a signature; the probe result is unusable"
      echo "     blkid: $blkid_out"
      fail "$device ($resolved) already carries a signature; refusing to touch it (wipe or re-declare it deliberately)"
      ;;
    2) echo "     blkid: no signature on $resolved" ;;
    *) fail "blkid could not probe $resolved (exit $blkid_rc): $blkid_out — a failed probe is not proof of a blank disk" ;;
  esac

  ancestors="$(lsblk -sno NAME "$resolved" 2>/dev/null | tr -d ' ' | sort -u)"
  descendants="$(lsblk -rno NAME "$resolved" 2>/dev/null | tr -d ' ' | sort -u)"
  # One space-separated line: the checks below match " name " against these
  # lists, which a multi-line list would silently defeat.
  ancestor_types="$(lsblk -sno TYPE "$resolved" 2>/dev/null | tr -s ' \n' ' ')"

  for candidate in $ancestors $descendants; do
    case " $mounted_names " in
      *" $candidate "*) fail "$device ($resolved) shares a disk with a filesystem mounted on this node ($candidate); a data device must not be part of the system chain" ;;
    esac
  done
  case " $ancestor_types " in
    *" lvm "*|*" raid"*|*" md "*|*" crypt "*) fail "$device ($resolved) sits on an LVM/RAID/encrypted device, which is not a blank data disk" ;;
  esac

  fstype="$(lsblk -ndo FSTYPE "$resolved" 2>/dev/null | tr -d ' ')"
  [ -z "$fstype" ] || fail "$device ($resolved) carries a $fstype filesystem"
  children_in_use="$(lsblk -rno NAME,MOUNTPOINT "$resolved" 2>/dev/null | awk 'NF > 1 {print $1}' | tr '\n' ' ')"
  [ -z "$children_in_use" ] || fail "$device ($resolved) has mounted children: $children_in_use"
done

echo "ceph pre-flight OK on $NODE: every declared device is a blank, unused disk"
echo "note: this check wrote nothing and cleaned nothing"

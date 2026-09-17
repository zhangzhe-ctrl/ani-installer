#!/usr/bin/env bash
# ANI Ceph health gate. Rendered verbatim (no template vars). Runs on kube_control_plane[0].
# Waits until: 3 OSDs up AND in, cluster health is not HEALTH_ERR, and every PG is active+clean.
# A fresh 3-node cluster with all OSDs on /dev/sdb normally reaches HEALTH_OK quickly.
set -uo pipefail

NS=rook-ceph
EXPECTED_OSD=3
TIMEOUT=900
INTERVAL=15
TOOLBOX=(kubectl -n "${NS}" exec deploy/rook-ceph-tools --)

deadline=$(( $(date +%s) + TIMEOUT ))
echo "[ceph-wait] waiting up to ${TIMEOUT}s for ${EXPECTED_OSD} OSDs up/in and all PGs active+clean ..."

while true; do
  status="$("${TOOLBOX[@]}" ceph -s 2>/dev/null || true)"
  pgstat="$("${TOOLBOX[@]}" ceph pg stat 2>/dev/null | head -1 || true)"

  health="$(echo "${status}" | awk '/health:/{print $2; exit}')"
  osd_line="$(echo "${status}" | grep -E '^[[:space:]]*osd:' | head -1)"
  osd_up="$(echo "${osd_line}" | grep -oE '[0-9]+ up' | grep -oE '^[0-9]+' | head -1)"
  osd_in="$(echo "${osd_line}" | grep -oE '[0-9]+ in' | grep -oE '^[0-9]+' | head -1)"
  pg_total="$(echo "${pgstat}" | sed -E 's/^([0-9]+) pgs:.*/\1/')"
  pg_clean="$(echo "${pgstat}" | grep -oE '[0-9]+ active\+clean' | grep -oE '^[0-9]+' | head -1)"

  echo "----- $(date -u +%H:%M:%S) health=${health:-?} osd=${osd_up:-?}up/${osd_in:-?}in pgs=${pg_total:-?}total/${pg_clean:-0}clean -----"
  echo "${status}"

  all_clean=false
  if [ -n "${pg_total}" ] && [ "${pg_total}" = "${pg_clean:-none}" ]; then all_clean=true; fi

  if [ "${osd_up:-0}" -ge "${EXPECTED_OSD}" ] && [ "${osd_in:-0}" -ge "${EXPECTED_OSD}" ] \
     && [ "${health:-HEALTH_ERR}" != "HEALTH_ERR" ] && [ "${all_clean}" = "true" ]; then
    echo "[ceph-wait] SUCCESS: health=${health}, ${osd_up} OSDs up/in, all ${pg_total} PGs active+clean."
    "${TOOLBOX[@]}" ceph osd status || true
    "${TOOLBOX[@]}" ceph df || true
    exit 0
  fi

  if [ "$(date +%s)" -ge "${deadline}" ]; then
    echo "[ceph-wait] TIMEOUT after ${TIMEOUT}s (health=${health:-?} osd=${osd_up:-?}up/${osd_in:-?}in pgs=${pg_total:-?}/${pg_clean:-0}clean)."
    echo "----- diagnostics -----"
    "${TOOLBOX[@]}" ceph health detail || true
    "${TOOLBOX[@]}" ceph osd tree || true
    kubectl -n "${NS}" get pods -o wide || true
    kubectl -n "${NS}" get cephcluster rook-ceph -o jsonpath='{.status.conditions}' || true
    echo
    exit 1
  fi
  sleep "${INTERVAL}"
done

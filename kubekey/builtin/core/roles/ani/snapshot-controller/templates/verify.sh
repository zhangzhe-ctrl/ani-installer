#!/usr/bin/env bash
# B04 proves data traveled through a new PVC restored from a real CSI snapshot.
set -euo pipefail
KUBECONFIG_FILE="${ANI_VERIFY_KUBECONFIG:?}"
test -f "$KUBECONFIG_FILE"
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_FILE")
OUT_DIR="${ANI_VERIFY_OUTPUT_DIR:?}"
NS=ani-platform
OWNER='{{ .kubernetes.cluster_name }}'
IMAGE='{{ index .ani.images "docker.io/library/busybox:1.37.0" }}'
install -d -m 0700 "$OUT_DIR"

run_driver() {
  local kind="$1" sc="$2" snapclass="$3" driver="$4"
  local suffix="$(date -u +%Y%m%d%H%M%S)-$$"
  local source="ani-snap-$kind-src-$suffix" restore="ani-snap-$kind-restore-$suffix"
  local writer="ani-snap-$kind-write-$suffix" reader="ani-snap-$kind-read-$suffix"
  local snapshot="ani-snap-$kind-$suffix" marker="ANI_SNAPSHOT_${kind}_${suffix}"
  cat > "$OUT_DIR/$source.yaml" <<PVC
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $source
  namespace: $NS
  labels: {ani.io/managed-by: "$OWNER", ani.io/check: snapshot-controller}
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: $sc
  resources:
    requests: {storage: 1Gi}
PVC
  "${KUBECTL[@]}" apply --server-side -f "$OUT_DIR/$source.yaml"
  cat > "$OUT_DIR/$writer.yaml" <<JOB
apiVersion: batch/v1
kind: Job
metadata:
  name: $writer
  namespace: $NS
  labels: {ani.io/managed-by: "$OWNER", ani.io/check: snapshot-controller}
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: writer
          image: $IMAGE
          imagePullPolicy: IfNotPresent
          command: ["/bin/sh", "-ec"]
          args: ["printf '%s' '$marker' > /data/marker; sync; echo ANI-SNAPSHOT-WRITTEN"]
          volumeMounts: [{name: data, mountPath: /data}]
      volumes:
        - name: data
          persistentVolumeClaim: {claimName: $source}
JOB
  "${KUBECTL[@]}" apply --server-side -f "$OUT_DIR/$writer.yaml"
  "${KUBECTL[@]}" -n "$NS" wait "pvc/$source" --for=jsonpath='{.status.phase}=Bound' --timeout=300s
  if ! "${KUBECTL[@]}" -n "$NS" wait "job/$writer" --for=condition=complete --timeout=300s; then
    "${KUBECTL[@]}" -n "$NS" logs "job/$writer" > "$OUT_DIR/$writer.log" 2>&1 || true
    echo "$kind source write failed; job and PVC retained; evidence=$OUT_DIR" >&2
    return 1
  fi
  "${KUBECTL[@]}" -n "$NS" logs "job/$writer" > "$OUT_DIR/$writer.log"
  grep -qx ANI-SNAPSHOT-WRITTEN "$OUT_DIR/$writer.log"
  "${KUBECTL[@]}" -n "$NS" delete "job/$writer" --cascade=foreground --wait=true >/dev/null
  local source_uid
  source_uid="$("${KUBECTL[@]}" -n "$NS" get "pvc/$source" -o jsonpath='{.metadata.uid}')"
  [ -n "$source_uid" ]
  cat > "$OUT_DIR/$snapshot.yaml" <<SNAP
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: $snapshot
  namespace: $NS
  labels: {ani.io/managed-by: "$OWNER", ani.io/check: snapshot-controller}
spec:
  volumeSnapshotClassName: $snapclass
  source:
    persistentVolumeClaimName: $source
SNAP
  "${KUBECTL[@]}" apply --server-side -f "$OUT_DIR/$snapshot.yaml"
  "${KUBECTL[@]}" -n "$NS" wait "volumesnapshot/$snapshot" --for=jsonpath='{.status.readyToUse}=true' --timeout=600s
  local content snap_uid content_uid content_driver content_policy
  content="$("${KUBECTL[@]}" -n "$NS" get "volumesnapshot/$snapshot" -o jsonpath='{.status.boundVolumeSnapshotContentName}')"
  snap_uid="$("${KUBECTL[@]}" -n "$NS" get "volumesnapshot/$snapshot" -o jsonpath='{.metadata.uid}')"
  [ -n "$content" ] && [ -n "$snap_uid" ]
  content_uid="$("${KUBECTL[@]}" get "volumesnapshotcontent/$content" -o jsonpath='{.spec.volumeSnapshotRef.uid}')"
  content_driver="$("${KUBECTL[@]}" get "volumesnapshotcontent/$content" -o jsonpath='{.spec.driver}')"
  content_policy="$("${KUBECTL[@]}" get "volumesnapshotcontent/$content" -o jsonpath='{.spec.deletionPolicy}')"
  [ "$content_uid" = "$snap_uid" ] && [ "$content_driver" = "$driver" ] && [ "$content_policy" = Retain ] || {
    echo "$kind snapshot content binding, driver or Retain policy mismatch" >&2; return 1;
  }
  "${KUBECTL[@]}" -n "$NS" get "volumesnapshot/$snapshot" -o json > "$OUT_DIR/$snapshot.json"
  "${KUBECTL[@]}" get "volumesnapshotcontent/$content" -o json > "$OUT_DIR/$content.json"
  cat > "$OUT_DIR/$restore.yaml" <<PVC
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $restore
  namespace: $NS
  labels: {ani.io/managed-by: "$OWNER", ani.io/check: snapshot-controller}
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: $sc
  resources:
    requests: {storage: 1Gi}
  dataSource:
    name: $snapshot
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
PVC
  "${KUBECTL[@]}" apply --server-side -f "$OUT_DIR/$restore.yaml"
  cat > "$OUT_DIR/$reader.yaml" <<JOB
apiVersion: batch/v1
kind: Job
metadata:
  name: $reader
  namespace: $NS
  labels: {ani.io/managed-by: "$OWNER", ani.io/check: snapshot-controller}
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: reader
          image: $IMAGE
          imagePullPolicy: IfNotPresent
          command: ["/bin/sh", "-ec"]
          args:
            - >-
              test "\$(cat /data/marker)" = '$marker'; echo ANI-SNAPSHOT-RESTORE-OK-$kind
          volumeMounts: [{name: data, mountPath: /data}]
      volumes:
        - name: data
          persistentVolumeClaim: {claimName: $restore}
JOB
  "${KUBECTL[@]}" apply --server-side -f "$OUT_DIR/$reader.yaml"
  "${KUBECTL[@]}" -n "$NS" wait "pvc/$restore" --for=jsonpath='{.status.phase}=Bound' --timeout=300s
  local restore_uid
  restore_uid="$("${KUBECTL[@]}" -n "$NS" get "pvc/$restore" -o jsonpath='{.metadata.uid}')"
  [ -n "$restore_uid" ] && [ "$restore_uid" != "$source_uid" ] || {
    echo "$kind restore reused the source PVC UID" >&2; return 1;
  }
  if ! "${KUBECTL[@]}" -n "$NS" wait "job/$reader" --for=condition=complete --timeout=300s; then
    "${KUBECTL[@]}" -n "$NS" logs "job/$reader" > "$OUT_DIR/$reader.log" 2>&1 || true
    echo "$kind snapshot restore read failed; resources retained; evidence=$OUT_DIR" >&2
    return 1
  fi
  "${KUBECTL[@]}" -n "$NS" logs "job/$reader" > "$OUT_DIR/$reader.log"
  grep -qx "ANI-SNAPSHOT-RESTORE-OK-$kind" "$OUT_DIR/$reader.log"
  "${KUBECTL[@]}" -n "$NS" delete "job/$reader" --cascade=foreground --wait=true >/dev/null
  [ "$("${KUBECTL[@]}" -n "$NS" get "pvc/$source" -o jsonpath='{.metadata.uid}')" = "$source_uid" ]
  "${KUBECTL[@]}" -n "$NS" get "pvc/$source" "pvc/$restore" -o json > "$OUT_DIR/$kind-pvcs-$suffix.json"
  echo "ANI-SNAPSHOT-RESTORE-OK driver=$kind source=$source restore=$restore snapshot=$snapshot content=$content source_uid=$source_uid restore_uid=$restore_uid"
}

run_driver rbd ani-block ani-rbd-retain rook-ceph.rbd.csi.ceph.com
run_driver cephfs ani-cephfs ani-cephfs-retain rook-ceph.cephfs.csi.ceph.com

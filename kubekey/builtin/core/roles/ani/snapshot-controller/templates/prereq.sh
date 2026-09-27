#!/usr/bin/env bash
# B04 refuses foreign ownership and a Ceph CSI configuration unlike the fixed classes.
set -euo pipefail
KUBECONFIG_FILE="${ANI_RUN_KUBECONFIG:?}"
test -f "$KUBECONFIG_FILE"
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_FILE")
owner='{{ .kubernetes.cluster_name }}'
check_owner() {
  local kind="$1" name="$2" namespace="${3:-}"
  local scope=()
  if [ -n "$namespace" ]; then scope=(-n "$namespace"); fi
  if [ -n "$("${KUBECTL[@]}" "${scope[@]}" get "$kind/$name" --ignore-not-found -o name)" ]; then
    local actual
    actual="$("${KUBECTL[@]}" "${scope[@]}" get "$kind/$name" -o jsonpath='{.metadata.labels.ani\.io/managed-by}')"
    [ "$actual" = "$owner" ] || { echo "foreign $kind/$name owner=$actual; refusing B04 takeover" >&2; exit 1; }
  fi
}
for name in volumesnapshotclasses volumesnapshotcontents volumesnapshots; do
  check_owner crd "$name.snapshot.storage.k8s.io"
done
check_owner clusterrole ani-snapshot-controller-runner
check_owner clusterrolebinding ani-snapshot-controller-role
for kind_name in 'serviceaccount ani-snapshot-controller' 'role ani-snapshot-controller-leaderelection' 'rolebinding ani-snapshot-controller-leaderelection' 'deployment ani-snapshot-controller'; do
  read -r kind name <<< "$kind_name"
  check_owner "$kind" "$name" kube-system
done
check_owner volumesnapshotclass ani-rbd-retain
check_owner volumesnapshotclass ani-cephfs-retain
"${KUBECTL[@]}" get storageclass ani-block ani-cephfs -o json | python3 -c '
import json,sys
items={x["metadata"]["name"]:x for x in json.load(sys.stdin)["items"]}
want={"ani-block":("rook-ceph.rbd.csi.ceph.com","rook-csi-rbd-provisioner"),"ani-cephfs":("rook-ceph.cephfs.csi.ceph.com","rook-csi-cephfs-provisioner")}
for name,(driver,secret) in want.items():
  obj=items[name]
  p=obj["parameters"]
  assert obj["provisioner"]==driver, f"{name} driver differs from B04 SnapshotClass"
  assert p["clusterID"]=="rook-ceph", f"{name} clusterID differs from B04 SnapshotClass"
  assert p["csi.storage.k8s.io/provisioner-secret-name"]==secret, f"{name} provisioner Secret differs from B04 SnapshotClass"
  assert p["csi.storage.k8s.io/provisioner-secret-namespace"]=="rook-ceph", f"{name} provisioner Secret namespace differs"
'
for driver in rook-ceph.rbd.csi.ceph.com rook-ceph.cephfs.csi.ceph.com; do
  "${KUBECTL[@]}" get "csidriver/$driver" >/dev/null
done
for secret in rook-csi-rbd-provisioner rook-csi-cephfs-provisioner; do
  "${KUBECTL[@]}" -n rook-ceph get "secret/$secret" >/dev/null
done

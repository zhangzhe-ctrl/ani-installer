#!/usr/bin/env bash
# ANI Ceph end-to-end storage verification. Rendered as a template (busybox image resolves via
# .ani.images). Provisions a real RBD (ani-block, RWO) and CephFS (ani-cephfs, RWX) volume,
# writes and reads back a file in each, then cleans up. Any failure exits non-zero with diagnostics.
set -uo pipefail

BUSYBOX="{{ index .ani.images "docker.io/library/busybox:1.37.0" }}"
NS=ani-ceph-verify
TIMEOUT=300

echo "[ceph-verify] busybox image = ${BUSYBOX}"

kubectl delete ns "${NS}" --ignore-not-found --wait=true --timeout=300s >/dev/null 2>&1 || true
kubectl create ns "${NS}" >/dev/null

fail() {
  echo "[ceph-verify] FAIL: $*"
  echo "----- diagnostics -----"
  kubectl -n "${NS}" get pvc,pv,pods -o wide || true
  kubectl -n "${NS}" describe pvc || true
  kubectl -n "${NS}" describe pods || true
  kubectl -n "${NS}" get events --sort-by=.lastTimestamp | tail -40 || true
  kubectl -n rook-ceph get pods -o wide | grep -iE 'csi|plugin' || true
  exit 1
}

# ---------- RBD (ani-block, ReadWriteOnce) ----------
echo "[ceph-verify] provisioning RBD test volume (ani-block) ..."
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ani-rbd-test
  namespace: ${NS}
spec:
  accessModes: ["ReadWriteOnce"]
  storageClassName: ani-block
  resources:
    requests:
      storage: 1Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: ani-rbd-test
  namespace: ${NS}
spec:
  restartPolicy: Never
  containers:
    - name: w
      image: ${BUSYBOX}
      command: ["/bin/sh","-c","echo ani-rbd-ok > /data/msg && sync && cat /data/msg"]
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: ani-rbd-test
EOF

kubectl -n "${NS}" wait --for=jsonpath='{.status.phase}'=Bound pvc/ani-rbd-test --timeout=${TIMEOUT}s || fail "RBD PVC ani-rbd-test not Bound"
kubectl -n "${NS}" wait --for=jsonpath='{.status.phase}'=Succeeded pod/ani-rbd-test --timeout=${TIMEOUT}s || fail "RBD pod ani-rbd-test did not Succeed"
rbd_out="$(kubectl -n "${NS}" logs pod/ani-rbd-test || true)"
echo "[ceph-verify] RBD pod output: ${rbd_out}"
echo "${rbd_out}" | grep -q "ani-rbd-ok" || fail "RBD read-back mismatch"

# ---------- CephFS (ani-cephfs, ReadWriteMany) ----------
echo "[ceph-verify] provisioning CephFS test volume (ani-cephfs) ..."
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ani-fs-test
  namespace: ${NS}
spec:
  accessModes: ["ReadWriteMany"]
  storageClassName: ani-cephfs
  resources:
    requests:
      storage: 1Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: ani-fs-test
  namespace: ${NS}
spec:
  restartPolicy: Never
  containers:
    - name: w
      image: ${BUSYBOX}
      command: ["/bin/sh","-c","echo ani-cephfs-ok > /data/msg && sync && cat /data/msg"]
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: ani-fs-test
EOF

kubectl -n "${NS}" wait --for=jsonpath='{.status.phase}'=Bound pvc/ani-fs-test --timeout=${TIMEOUT}s || fail "CephFS PVC ani-fs-test not Bound"
kubectl -n "${NS}" wait --for=jsonpath='{.status.phase}'=Succeeded pod/ani-fs-test --timeout=${TIMEOUT}s || fail "CephFS pod ani-fs-test did not Succeed"
fs_out="$(kubectl -n "${NS}" logs pod/ani-fs-test || true)"
echo "[ceph-verify] CephFS pod output: ${fs_out}"
echo "${fs_out}" | grep -q "ani-cephfs-ok" || fail "CephFS read-back mismatch"

# ---------- cleanup ----------
kubectl delete ns "${NS}" --wait=false >/dev/null 2>&1 || true
echo "[ceph-verify] SUCCESS: RBD (ani-block) and CephFS (ani-cephfs) both provisioned, written and read back."
exit 0

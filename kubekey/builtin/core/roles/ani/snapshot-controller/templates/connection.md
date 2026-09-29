# CSI snapshots

- Controller: `Deployment/ani-snapshot-controller`, two replicas with leader election in `kube-system`.
- API: `VolumeSnapshot`, `VolumeSnapshotContent`, `VolumeSnapshotClass` v1 CRDs from external-snapshotter v8.5.0.
- RBD: `VolumeSnapshotClass/ani-rbd-retain`, driver `rook-ceph.rbd.csi.ceph.com`, matching `StorageClass/ani-block`.
- CephFS: `VolumeSnapshotClass/ani-cephfs-retain`, driver `rook-ceph.cephfs.csi.ceph.com`, matching `StorageClass/ani-cephfs`.
- Both classes use `deletionPolicy: Retain`. Deleting a snapshot object can leave backend data and a retained content; inspect ownership and cleanup implications before deletion.
- This run's source PVCs and restored PVCs are retained for evidence; the checker removes only its completed write/read Jobs.

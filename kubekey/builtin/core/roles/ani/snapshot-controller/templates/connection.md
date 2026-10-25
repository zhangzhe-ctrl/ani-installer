# CSI snapshots

- Controller: `Deployment/ani-snapshot-controller`, two replicas with leader election in `kube-system`.
- API: `VolumeSnapshot`, `VolumeSnapshotContent`, `VolumeSnapshotClass` v1 CRDs from external-snapshotter v8.5.0.
- RBD: `VolumeSnapshotClass/ani-rbd-delete`, driver `rook-ceph.rbd.csi.ceph.com`, matching `StorageClass/ani-block`.
- CephFS: `VolumeSnapshotClass/ani-cephfs-delete`, driver `rook-ceph.cephfs.csi.ceph.com`, matching `StorageClass/ani-cephfs`.
- Both classes use `deletionPolicy: Delete` for new snapshots. Bound historical VolumeSnapshotContents keep their independent deletion policy and recorded class name; retiring legacy classes does not rename or delete those historical snapshots or contents.
- This run's source PVCs and restored PVCs are retained for evidence; the checker removes only its completed write/read Jobs.

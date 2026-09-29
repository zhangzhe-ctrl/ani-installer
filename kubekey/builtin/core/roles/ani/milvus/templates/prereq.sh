#!/usr/bin/env bash
# Refuse foreign or incompatible RGW objects before applying B02 resources.
set -euo pipefail
KUBECONFIG_FILE="${ANI_RUN_KUBECONFIG:?}"
test -f "$KUBECONFIG_FILE"
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_FILE")
OWNER='{{ .kubernetes.cluster_name }}'
CLASS=ani-rgw-retain
CLAIM=ani-milvus-objects

"${KUBECTL[@]}" -n rook-ceph wait cephobjectstore/ani-store --for=jsonpath='{.status.phase}=Ready' --timeout=60s
"${KUBECTL[@]}" -n rook-ceph get service/rook-ceph-rgw-ani-store >/dev/null

existing="$("${KUBECTL[@]}" get storageclass "$CLASS" --ignore-not-found -o name)"
if [ -n "$existing" ]; then
  owner="$("${KUBECTL[@]}" get storageclass "$CLASS" -o jsonpath='{.metadata.labels.ani\.io/managed-by}')"
  spec="$("${KUBECTL[@]}" get storageclass "$CLASS" -o jsonpath='{.provisioner}{" "}{.parameters.objectStoreName}{" "}{.parameters.objectStoreNamespace}{" "}{.reclaimPolicy}')"
  [ "$owner" = "$OWNER" ] || { echo "foreign StorageClass $CLASS owner=$owner" >&2; exit 1; }
  [ "$spec" = 'rook-ceph.ceph.rook.io/bucket ani-store rook-ceph Retain' ] || { echo "incompatible StorageClass $CLASS" >&2; exit 1; }
fi
existing="$("${KUBECTL[@]}" -n ani-platform get objectbucketclaim "$CLAIM" --ignore-not-found -o name)"
if [ -n "$existing" ]; then
  owner="$("${KUBECTL[@]}" -n ani-platform get objectbucketclaim "$CLAIM" -o jsonpath='{.metadata.labels.ani\.io/managed-by}')"
  spec="$("${KUBECTL[@]}" -n ani-platform get objectbucketclaim "$CLAIM" -o jsonpath='{.spec.bucketName}{" "}{.spec.storageClassName}')"
  [ "$owner" = "$OWNER" ] || { echo "foreign ObjectBucketClaim $CLAIM owner=$owner" >&2; exit 1; }
  [ "$spec" = 'ani-milvus-objects ani-rgw-retain' ] || { echo "incompatible ObjectBucketClaim $CLAIM" >&2; exit 1; }
fi

#!/usr/bin/env bash
set -euo pipefail
k() { kubectl --kubeconfig '{{ .ani.run.kubeconfig }}' "$@"; }
ART="{{ .ani.artifact_root }}"
test -f '{{ .ani.run.kubeconfig }}'
test -x "$ART/bin/virtctl"
test -s "$ART/guest/cirros-0.6.3-x86_64-disk.img"
echo '40ede2ee37c98a1aeed71c9c219616a05247ce2be109e1edddf0477572e8b978  '"$ART/bin/virtctl" | sha256sum -c -
echo '7d6355852aeb6dbcd191bcda7cd74f1536cfe5cbf8a10495a7283a8396e4b75b  '"$ART/guest/cirros-0.6.3-x86_64-disk.img" | sha256sum -c -
# --ignore-not-found keeps genuine transport/RBAC errors visible.
k get namespace >/dev/null
for ns in kubevirt cdi; do
  existing="$(k get namespace "$ns" --ignore-not-found -o name)"
  if [ -n "$existing" ]; then
    echo "namespace $ns already exists; KubeVirt/CDI first-install would take over its resources" >&2
    exit 1
  fi
done
for crd in kubevirts.kubevirt.io cdis.cdi.kubevirt.io datavolumes.cdi.kubevirt.io virtualmachines.kubevirt.io; do
  existing="$(k get crd "$crd" --ignore-not-found -o name)"
  if [ -n "$existing" ]; then
    echo "CRD $crd already exists; refusing takeover" >&2
    exit 1
  fi
done
for kind in services pods; do
  existing="$(k -n ani-platform get "$kind" ani-b05-guest --ignore-not-found -o name)"
  if [ -n "$existing" ]; then
    echo "pre-existing ani-b05-guest $kind; refusing takeover" >&2
    exit 1
  fi
done
for sc in '{{ (index .ani.components "kubevirt").storage_class }}' '{{ (index .ani.components "kubevirt").scratch_storage_class }}'; do
  mode="$(k get sc "$sc" -o jsonpath='{.volumeBindingMode}')"
  if [ "$mode" != Immediate ]; then
    echo "B05 requires an Immediate binding StorageClass for bounded CDI import: $sc is $mode" >&2
    exit 1
  fi
done
k get node '{{ (index .ani.components "kubevirt").vm_node }}' -o name >/dev/null

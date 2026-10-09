#!/usr/bin/env bash
# NAD CRD can be reused only if its v1 shape is compatible; other resources
# must be owned by this ANI cluster before server-side apply.
set -euo pipefail
KUBECONFIG_FILE="${ANI_RUN_KUBECONFIG:?}"
test -f "$KUBECONFIG_FILE"
K=(kubectl --kubeconfig "$KUBECONFIG_FILE")
owner='{{ .kubernetes.cluster_name }}'
"${K[@]}" get crd network-attachment-definitions.k8s.cni.cncf.io --ignore-not-found -o json > /etc/kubernetes/ani/multus/existing-nad-crd.json
if [ -s /etc/kubernetes/ani/multus/existing-nad-crd.json ]; then
  python3 - /etc/kubernetes/ani/multus/existing-nad-crd.json <<'PY'
import json,sys
x=json.load(open(sys.argv[1]))['spec']
assert x['group']=='k8s.cni.cncf.io' and x['scope']=='Namespaced'
assert x['names']['kind']=='NetworkAttachmentDefinition'
versions=[v for v in x['versions'] if v['name']=='v1' and v.get('served') and v.get('storage')]
assert len(versions)==1
config=versions[0]['schema']['openAPIV3Schema']['properties']['spec']['properties']['config']
assert config['type']=='string'
PY
  echo present > /etc/kubernetes/ani/multus/crd-state
else
  echo missing > /etc/kubernetes/ani/multus/crd-state
fi
items=('clusterrole ani-multus' 'clusterrolebinding ani-multus' 'serviceaccount ani-multus kube-system' 'configmap ani-multus-daemon-config kube-system' 'daemonset ani-multus kube-system')
{{ if eq .ani.network.stack "kcn" }}
items+=('vpc.networking.kubercloud.com ani-b01-vpc ani-platform' 'subnet.networking.kubercloud.com ani-b01-secondary ani-platform')
{{ end }}
if grep -qx present /etc/kubernetes/ani/multus/crd-state; then
  items+=('network-attachment-definition ani-b01-local ani-platform')
fi
for item in "${items[@]}"; do
  read -r kind name ns <<< "$item"
  args=(); if [ -n "${ns:-}" ]; then args=(-n "$ns"); fi
  if [ -n "$("${K[@]}" "${args[@]}" get "$kind/$name" --ignore-not-found -o name)" ]; then
    actual="$("${K[@]}" "${args[@]}" get "$kind/$name" -o jsonpath='{.metadata.labels.ani\.io/managed-by}')"
    [ "$actual" = "$owner" ] || { echo "foreign $kind/$name owner=$actual" >&2; exit 1; }
  fi
done

#!/usr/bin/env bash
# Publish only the RGW CA certificate to the Milvus namespace. The CA key
# and server key stay in the root-only Ceph work directory and TLS Secret.
set -euo pipefail
export KUBECONFIG='{{ .ani.run.kubeconfig }}'
owner='{{ .kubernetes.cluster_name }}'
work=/etc/kubernetes/ani/milvus
install -d -m 0700 "$work"
umask 077
python3 - "$work" "$owner" <<'PY'
import base64,json,subprocess,sys
from pathlib import Path
work,owner=Path(sys.argv[1]),sys.argv[2]
x=json.loads(subprocess.check_output(['kubectl','-n','rook-ceph','get','secret','ani-rgw-server-tls','-o','json']))
assert x['metadata'].get('labels',{}).get('ani.io/managed-by')==owner,'foreign RGW TLS secret'
for key,path in (('ca.crt','rgw-ca.crt'),('tls.crt','rgw-leaf-chain.crt')):
    value=x.get('data',{}).get(key)
    assert value, f'RGW TLS secret missing {key}'
    (work/path).write_bytes(base64.b64decode(value,validate=True))
PY
openssl verify -CAfile "$work/rgw-ca.crt" "$work/rgw-leaf-chain.crt" >/dev/null
existing="$(kubectl -n ani-platform get configmap ani-rgw-ca --ignore-not-found -o name)"
if [ -n "$existing" ]; then
  actual="$(kubectl -n ani-platform get configmap ani-rgw-ca -o jsonpath='{.metadata.labels.ani\.io/managed-by}')"
  [ "$actual" = "$owner" ] || { echo "foreign RGW CA ConfigMap" >&2; exit 1; }
fi
kubectl -n ani-platform create configmap ani-rgw-ca --from-file=ca.crt="$work/rgw-ca.crt" --dry-run=client -o json |
  python3 -c 'import json,sys; x=json.load(sys.stdin); x["metadata"].setdefault("labels",{})["ani.io/managed-by"]=sys.argv[1]; json.dump(x,sys.stdout)' "$owner" |
  kubectl apply --server-side -f -
service="$(kubectl -n rook-ceph get svc rook-ceph-rgw-ani-store -o jsonpath='{range .spec.ports[*]}{.port}{" "}{end}')"
printf '%s\n' "$service" | grep -Eq '(^| )443( |$)' || { echo 'RGW HTTPS service port 443 absent' >&2; exit 1; }
printf 'owned RGW CA and HTTPS service verified\n'

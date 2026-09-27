#!/usr/bin/env bash
# Every node must still use the selected primary CNI before Multus is allowed
# to write 00-multus.conf. This script performs no mutation.
set -euo pipefail
stack='{{ .ani.network.stack }}'
case "$stack" in
  kcn) primary=01-kc-networking.conflist; plugin=kc-networking ;;
  kubeovn) primary=01-kube-ovn.conflist; plugin=kube-ovn ;;
  *) echo "unsupported primary CNI stack: $stack" >&2; exit 1 ;;
esac
test -S /run/containerd/containerd.sock
test -d /etc/cni/net.d
test -d /opt/cni/bin
for bin in bridge host-local loopback portmap "$plugin"; do
  test -x "/opt/cni/bin/$bin" || { echo "missing CNI binary: $bin" >&2; exit 1; }
done
crictl --runtime-endpoint unix:///run/containerd/containerd.sock info | python3 -c '
import json,sys
x=json.load(sys.stdin)
found=[]
def walk(obj):
    if isinstance(obj,dict):
        for k,v in obj.items():
            if k.lower() in ("confdir","cniconfigdir","bindir","bindirs"):
                found.extend(v if isinstance(v,list) else [v])
            walk(v)
    elif isinstance(obj,list):
        for v in obj: walk(v)
walk(x)
assert "/etc/cni/net.d" in found, f"containerd CNI confDir unverified: {found}"
assert "/opt/cni/bin" in found, f"containerd CNI binDir unverified: {found}"
'
python3 - "$primary" "$plugin" <<'PY'
import glob,json,os,sys
primary,plugin=sys.argv[1:]
files=sorted(p for p in glob.glob('/etc/cni/net.d/*') if p.endswith(('.conf','.conflist','.json')))
assert primary in [os.path.basename(p) for p in files],f'primary CNI {primary} missing: {files}'
obj=json.load(open('/etc/cni/net.d/'+primary))
plugins=obj.get('plugins',[obj])
assert plugins[0].get('type')==plugin,f'{primary} has unexpected delegate {plugins[0].get("type")}'
assert all(p.get('type')!='multus' for p in plugins),f'{primary} would recurse into Multus'
others=[os.path.basename(p) for p in files if os.path.basename(p)!=primary]
assert not others or others==['00-multus.conf'],f'ambiguous or foreign CNI configs: {files}'
if others:
    existing=json.load(open('/etc/cni/net.d/00-multus.conf'))
    assert existing.get('type')=='multus', '00-multus.conf is not Multus'
    delegates=existing.get('delegates',[])
    assert delegates and delegates[0].get('name')==obj.get('name'), 'existing Multus delegates another primary CNI'
print('primary CNI checked:',primary,'delegate=',plugin,'configs=',files)
PY

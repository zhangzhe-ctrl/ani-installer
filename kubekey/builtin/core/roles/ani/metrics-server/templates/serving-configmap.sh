#!/usr/bin/env bash
# Keep kubeadm's shared kubelet configuration aligned with this selected B03
# component, including later node joins. Only the one bootstrap field changes.
set -euo pipefail
export KUBECONFIG='{{ .ani.run.kubeconfig }}'
python3 - <<'PY'
import json,re,subprocess,sys
from pathlib import Path

kubectl=['kubectl','--kubeconfig', '{{ .ani.run.kubeconfig }}']
cm=json.loads(subprocess.check_output(kubectl+['-n','kube-system','get','configmap','kubelet-config','-o','json']))
data=cm.get('data',{})
assert set(data)=={'kubelet'}, 'unexpected kubelet-config data keys'
old=data['kubelet']
assert re.search(r'^kind: KubeletConfiguration\s*$',old,re.M), 'not a KubeletConfiguration'
assert re.search(r'^rotateCertificates: true\s*$',old,re.M), 'kubelet client rotation not enabled'
lines=old.splitlines(keepends=True)
keys=[i for i,line in enumerate(lines) if re.match(r'^serverTLSBootstrap\s*:',line)]
assert len(keys)<=1, 'duplicate serverTLSBootstrap setting'
if keys:
    assert re.fullmatch(r'serverTLSBootstrap:\s*(true|false)\s*\n?',lines[keys[0]]), 'unrecognized serverTLSBootstrap value'
    if lines[keys[0]].strip()=='serverTLSBootstrap: true':
        print('kubeadm kubelet-config already enables serving TLS bootstrap')
        sys.exit(0)
    lines[keys[0]]='serverTLSBootstrap: true\n'
else:
    if lines and not lines[-1].endswith('\n'):
        lines[-1]+='\n'
    lines.append('serverTLSBootstrap: true\n')
new=''.join(lines)
work=Path('/etc/kubernetes/ani/metrics-server')
work.mkdir(mode=0o700,parents=True,exist_ok=True)
backup=work/'kubelet-config-before.yaml'
if backup.exists():
    assert backup.read_text()==old, 'kubelet-config backup differs from live content'
else:
    backup.write_text(old)
    backup.chmod(0o600)
patch=[{'op':'test','path':'/metadata/resourceVersion','value':cm['metadata']['resourceVersion']},
       {'op':'replace','path':'/data/kubelet','value':new}]
subprocess.check_call(kubectl+['-n','kube-system','patch','configmap','kubelet-config','--type=json','-p',json.dumps(patch,separators=(',',':'))])
check=json.loads(subprocess.check_output(kubectl+['-n','kube-system','get','configmap','kubelet-config','-o','json']))
assert check.get('data',{}).get('kubelet')==new, 'kubelet-config update did not persist'
print('kubeadm kubelet-config serving TLS bootstrap enabled')
PY

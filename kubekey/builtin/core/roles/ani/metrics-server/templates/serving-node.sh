#!/usr/bin/env bash
# Enable kubelet's own serving certificate CSR flow on this declared node.
set -euo pipefail
expected="${1:?declared node name required}"
[ "$(hostname -s)" = "$expected" ] || { echo "node identity mismatch: expected=$expected actual=$(hostname -s)" >&2; exit 1; }
test -f /etc/kubernetes/kubelet.conf
config=/var/lib/kubelet/config.yaml
test -f "$config" && test ! -L "$config"
python3 - "$config" <<'PY'
import os,re,sys,tempfile
from pathlib import Path
p=Path(sys.argv[1])
old=p.read_text()
assert re.search(r'^kind: KubeletConfiguration\s*$',old,re.M), 'not a KubeletConfiguration'
assert re.search(r'^rotateCertificates: true\s*$',old,re.M), 'kubelet client rotation not enabled'
lines=old.splitlines(keepends=True)
keys=[i for i,line in enumerate(lines) if re.match(r'^serverTLSBootstrap\s*:',line)]
assert len(keys)<=1, 'duplicate serverTLSBootstrap setting'
if keys:
    assert re.fullmatch(r'serverTLSBootstrap:\s*(true|false)\s*\n?',lines[keys[0]]), 'unrecognized serverTLSBootstrap value'
    if lines[keys[0]].strip()=='serverTLSBootstrap: true':
        print('kubelet serving TLS bootstrap already enabled')
        sys.exit(0)
    lines[keys[0]]='serverTLSBootstrap: true\n'
else:
    if lines and not lines[-1].endswith('\n'):
        lines[-1]+='\n'
    lines.append('serverTLSBootstrap: true\n')
new=''.join(lines)
work=Path('/etc/kubernetes/ani/metrics-server')
work.mkdir(mode=0o700,parents=True,exist_ok=True)
backup=work/'node-kubelet-config-before.yaml'
if backup.exists():
    assert backup.read_text()==old, 'kubelet config backup differs from live content'
else:
    backup.write_text(old)
    backup.chmod(0o600)
fd,tmp=tempfile.mkstemp(prefix='.kubelet-config-',dir=p.parent)
try:
    with os.fdopen(fd,'w') as out:
        out.write(new)
        out.flush()
        os.fsync(out.fileno())
    st=p.stat()
    os.chmod(tmp,st.st_mode & 0o777)
    os.chown(tmp,st.st_uid,st.st_gid)
    (work/'kubelet-restart-required').write_text('1\n')
    os.replace(tmp,p)
finally:
    if os.path.exists(tmp): os.unlink(tmp)
print('kubelet serving TLS bootstrap enabled; restart required')
PY
marker=/etc/kubernetes/ani/metrics-server/kubelet-restart-required
if test -e "$marker"; then
  systemctl restart kubelet
  systemctl is-active --quiet kubelet
  rm -f "$marker"
fi
systemctl is-active --quiet kubelet

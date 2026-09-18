#!/usr/bin/env python3
"""Execute both export tasks against temporary homes; never write a real kubeconfig."""
import base64
import os
from pathlib import Path
import re
import stat
import subprocess
import tempfile

root = Path(__file__).resolve().parents[1]
roles = root / 'builtin/core/roles/kubernetes'
cases = [
    ('init-kubernetes/tasks/init_kubernetes.yaml', 'Init | Copy kubeconfig to default directory'),
    ('join-kubernetes/tasks/main.yaml', 'Join | Set kubeconfig permissions'),
]
for path, task in cases:
    text = (roles / path).read_text().split('- name: ' + task + '\n', 1)[1]
    block = text.split('\n- name:', 1)[0].split('  command: |\n', 1)[1]
    script = '\n'.join(line[4:] for line in block.splitlines())
    assert '$SUDO_USER' not in script
    for missing in (False, True):
        with tempfile.TemporaryDirectory(prefix='ani-export-test-') as tmp:
            d = Path(tmp)
            home = d / 'custom home'
            (d / 'root/.kube').mkdir(parents=True)
            (d / 'admin.conf').write_text('test-admin-config\n')
            (d / 'root/.kube/config').write_text('test-admin-config\n')
            (d / 'bin').mkdir()
            getent = d / 'bin/getent'
            getent.write_text('#!/bin/sh\n' + ('exit 2\n' if missing else
                f'printf "%s\\n" "manager:x:{os.getuid()}:{os.getgid()}::{home}:/bin/bash"\n'))
            getent.chmod(0o755)
            rendered = re.sub(r'{{.*?}}', base64.b64encode(b'manager').decode(), script)
            rendered = rendered.replace('/etc/kubernetes/admin.conf', str(d / 'admin.conf'))
            rendered = rendered.replace('/root/.kube', str(d / 'root/.kube'))
            # Run as the unprivileged test user; root destinations remain in the sandbox.
            rendered = rendered.replace('-o root -g root', f'-o {os.getuid()} -g {os.getgid()}')
            rendered = rendered.replace('chown root:root', f'chown {os.getuid()}:{os.getgid()}')
            env = dict(os.environ, PATH=str(d / 'bin') + ':' + os.environ['PATH'], SUDO_USER='wrong-user', HOME='/missing')
            p = subprocess.run(['bash', '-c', rendered], env=env, capture_output=True, text=True)
            if missing:
                assert p.returncode != 0, p.stdout
                assert not (home / '.kube/config').exists()
            else:
                assert p.returncode == 0, p.stderr
                assert (home / '.kube/config').read_text() == 'test-admin-config\n'
                assert stat.S_IMODE((home / '.kube').stat().st_mode) == 0o700
                assert stat.S_IMODE((home / '.kube/config').stat().st_mode) == 0o600
                assert (home / '.kube/config').stat().st_uid == os.getuid()
                assert (home / '.kube/config').stat().st_gid == os.getgid()
            print(f'PASS {task}, missing_user={missing}')

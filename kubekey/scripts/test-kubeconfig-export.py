#!/usr/bin/env python3
"""Execute both export tasks against temporary homes; never write a real kubeconfig."""
import base64
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile

root = Path(__file__).resolve().parents[1]
roles = root / 'builtin/core/roles/kubernetes'
cases = [
    ('init-kubernetes/tasks/init_kubernetes.yaml', 'Init | Copy kubeconfig to default directory'),
    ('join-kubernetes/tasks/main.yaml', 'Join | Set kubeconfig permissions'),
]
# The user-lookup modes each exported task is exercised in. The aggregate counters
# below fail the run when no case is left to execute, so a suite that silently
# shrinks to zero cannot report success.
MISSING_USER_MODES = (False, True)


class Result:
    def __init__(self) -> None:
        self.passed: list[str] = []
        self.failed: list[str] = []

    def check(self, test_id: str, condition: bool, detail: str) -> bool:
        (self.passed if condition else self.failed).append(f"{test_id}: {detail}")
        print(f"  {'PASS' if condition else 'FAIL'}  {test_id} — {detail}")
        return condition


def case_script(path: str, task: str) -> str:
    """Lift the shipped command block out of the role task, exactly as before."""
    text = (roles / path).read_text().split('- name: ' + task + '\n', 1)[1]
    block = text.split('\n- name:', 1)[0].split('  command: |\n', 1)[1]
    script = '\n'.join(line[4:] for line in block.splitlines())
    assert '$SUDO_USER' not in script
    return script


def run_case(script: str, missing: bool) -> str:
    """Run one export case; every assertion is the original assertion for that case."""
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
    if missing:
        return 'refused with a non-zero exit and wrote no kubeconfig'
    return 'exported, 0700 dir, 0600 file, owned by the unprivileged test user'


def main() -> int:
    res = Result()
    for path, task in cases:
        script = case_script(path, task)
        for missing in MISSING_USER_MODES:
            test_id = f'{task}, missing_user={missing}'
            try:
                detail = run_case(script, missing)
            except AssertionError as exc:                   # keep the original message
                res.check(test_id, False, f'assertion failed: {exc}')
            else:
                res.check(test_id, True, detail)

    total = len(res.passed) + len(res.failed)
    print()
    print(f"cases passed: {len(res.passed)}")
    print(f"cases failed: {len(res.failed)}")
    for item in res.failed:
        print(f"  FAILED {item}")
    if res.failed:
        return 1
    if total == 0:
        print("ZERO CASES RUN: refusing to pass a suite that executed nothing",
              file=sys.stderr)
        return 1
    print("ALL KUBECONFIG EXPORT TESTS PASSED")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

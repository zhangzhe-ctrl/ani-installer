#!/usr/bin/env python3
"""Exercise the shipped Debian shell task with failed APT, without touching host packages."""
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile

root = Path(__file__).resolve().parents[1]
text = (root / 'builtin/core/roles/native/repository/tasks/install_package.yaml').read_text()
block = text.split('- name: Repository | Initialize Debian-based repository and install required system packages\n', 1)[1]
block = block.split('\n  when:', 1)[0].split('  command: |\n', 1)[1]
script = '\n'.join(line[4:] for line in block.splitlines())
script = re.sub(r'^PKGS=.*$', 'PKGS="chrony"', script, flags=re.M)

# The scenarios this suite covers. The aggregate counters below fail the run if
# this list is ever emptied, so a silently shrinking suite cannot report success.
CASES = ('update', 'install', 'none')


class Result:
    def __init__(self) -> None:
        self.passed: list[str] = []
        self.failed: list[str] = []

    def check(self, test_id: str, condition: bool, detail: str) -> bool:
        (self.passed if condition else self.failed).append(f"{test_id}: {detail}")
        print(f"  {'PASS' if condition else 'FAIL'}  {test_id} — {detail}")
        return condition


def run_case(failure: str) -> str:
    """Run one mocked-APT scenario; every assertion is exactly the original one."""
    with tempfile.TemporaryDirectory(prefix='ani-apt-test-') as tmp:
        d = Path(tmp)
        (d / 'bin').mkdir()
        (d / 'iso').mkdir()
        (d / 'repository.iso').touch()
        (d / 'etc/apt/sources.list.d').mkdir(parents=True)
        original = d / 'etc/apt/sources.list'
        original.write_text('original-source\n')
        original_part = d / 'etc/apt/sources.list.d/original.sources'
        original_part.write_text('original-part\n')
        for cmd, content in {
            'dpkg-query': '#!/bin/bash\necho "unknown ok not-installed"\n',
            'apt-get': '''#!/bin/bash
echo "$*" >> "$APT_TEST_LOG"
case " $* " in
  *" update "*) [ "$APT_TEST_FAILURE" != update ] || exit 100;;
  *" install "*) [ "$APT_TEST_FAILURE" != install ] || exit 101;;
esac
exit 0
''',
            'apt': '#!/bin/bash\nexec apt-get "$@"\n',
        }.items():
            p = d / 'bin' / cmd
            p.write_text(content)
            p.chmod(0o755)
        rendered = script.replace('{{ .tmp_dir }}', tmp).replace('/etc/apt/', str(d / 'etc/apt') + '/')
        env = dict(os.environ, PATH=str(d / 'bin') + ':' + os.environ['PATH'],
                   APT_TEST_FAILURE=failure, APT_TEST_LOG=str(d / 'calls'))
        result = subprocess.run(['bash', '-c', rendered], env=env, capture_output=True, text=True)
        expected = {'update': 100, 'install': 101, 'none': 0}[failure]
        assert result.returncode == expected, (failure, result.returncode, expected, result.stderr)
        assert original.read_text() == 'original-source\n'
        assert original_part.read_text() == 'original-part\n'
        calls = (d / 'calls').read_text()
        if failure == 'update':
            assert ' install ' not in ' ' + calls.replace('\n', ' '), calls
        assert 'Dir::State::lists=' in calls, calls
        assert 'Dir::Etc::sourceparts=-' in calls, calls
    return f'apt={failure}, exit={result.returncode}, original sources preserved'


def main() -> int:
    res = Result()
    for failure in CASES:
        try:
            detail = run_case(failure)
        except AssertionError as exc:                       # keep the original message
            res.check(f'apt={failure}', False, f'assertion failed: {exc}')
        else:
            res.check(f'apt={failure}', True, detail)

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
    print("ALL DEBIAN REPOSITORY TESTS PASSED")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

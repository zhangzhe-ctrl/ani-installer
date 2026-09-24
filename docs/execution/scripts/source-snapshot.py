#!/usr/bin/env python3
"""Record a read-only, content-based source fingerprint (no SSH, no build).

Usage: source-snapshot.py REPO_ROOT OUTPUT_JSON
Output must be outside the input tree. File contents and Git diffs are never
emitted. This is a comparison aid, NOT a credential scanner or artifact verifier.
"""
from __future__ import annotations
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys

EXCLUDED_DIRS = {'.git', '.venv', 'venv', 'node_modules', '__pycache__', '.pytest_cache',
                 '.mypy_cache', 'releases', '_output', 'dist', 'build', '.cache',
                 'evidence', 'runs'}
EXCLUDED_FILES = {'lab.env', '.env', 'node-password', 'askpass.sh', 'admin.conf',
                  'kubeconfig', 'id_rsa', 'id_ed25519'}
EXCLUDED_SUFFIXES = {'.log', '.pyc', '.pem', '.key', '.p12', '.pfx'}
MAX_FILES = 150000


def inside(path: Path, root: Path) -> bool:
    try:
        path.relative_to(root)
        return True
    except ValueError:
        return False


def sha_file(path: Path) -> str:
    before = path.stat()
    h = hashlib.sha256()
    with path.open('rb') as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b''):
            h.update(chunk)
    after = path.stat()
    if (before.st_size, before.st_mtime_ns) != (after.st_size, after.st_mtime_ns):
        raise ValueError(f'file changed during fingerprint: {path.name}')
    return h.hexdigest()


def snapshot(root: Path) -> dict:
    items = []
    omitted = []
    for current, dirs, files in os.walk(root, followlinks=False):
        cur = Path(current)
        keep = []
        for name in sorted(dirs):
            p = cur / name
            rel = p.relative_to(root).as_posix()
            if name in EXCLUDED_DIRS:
                omitted.append({'path': rel, 'reason': 'excluded_directory'})
            elif p.is_symlink():
                raise ValueError(f'directory symlink requires manual review: {rel}')
            else:
                keep.append(name)
        dirs[:] = keep
        for name in sorted(files):
            p = cur / name
            rel = p.relative_to(root).as_posix()
            if name in EXCLUDED_FILES or p.suffix in EXCLUDED_SUFFIXES:
                omitted.append({'path': rel, 'reason': 'excluded_sensitive_or_generated'})
                continue
            if p.is_symlink():
                target = os.readlink(p)
                resolved = p.resolve(strict=True)
                if not inside(resolved, root):
                    raise ValueError(f'external symlink refused: {rel}')
                items.append({'path': rel, 'type': 'symlink',
                              'sha256': hashlib.sha256(os.fsencode(target)).hexdigest()})
            elif p.is_file():
                items.append({'path': rel, 'type': 'file', 'size': p.stat().st_size,
                              'sha256': sha_file(p)})
            else:
                raise ValueError(f'non-regular file refused: {rel}')
            if len(items) > MAX_FILES:
                raise ValueError('too many files; verify this is a source root, not an artifact directory')
    items.sort(key=lambda x: x['path'])
    tree = hashlib.sha256()
    for i in items:
        tree.update(i['path'].encode('utf-8') + b'\0' + i['type'].encode('ascii') + b'\0'
                    + i['sha256'].encode('ascii') + b'\n')
    head = None
    if (root / '.git').exists():
        r = subprocess.run(['git', '-c', f'safe.directory={root}', '-C', str(root),
                            'rev-parse', 'HEAD'], capture_output=True, text=True, timeout=10)
        if r.returncode == 0:
            head = r.stdout.strip()
    return {'schemaVersion': 1, 'tool': 'ani-plan-source-snapshot-r1',
            'root': str(root), 'gitHEAD': head, 'treeSHA256': tree.hexdigest(),
            'fileCount': len(items), 'files': items,
            'exclusionPolicy': {'directoryNames': sorted(EXCLUDED_DIRS),
                                'fileNames': sorted(EXCLUDED_FILES),
                                'suffixes': sorted(EXCLUDED_SUFFIXES)},
            'omitted': sorted(omitted, key=lambda x: x['path']),
            'limitations': ['Not a secret scanner. No file contents are included.',
                           'Excluded files, executable modes, ownership, ignored directories are not certified.',
                           'Content fingerprint does not prove artifact validity or any live deployment.']}


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__, file=sys.stderr)
        return 2
    root = Path(sys.argv[1]).expanduser().resolve(strict=True)
    out = Path(sys.argv[2]).expanduser().resolve()
    if not root.is_dir() or inside(out, root):
        raise ValueError('input must be a directory and output must be outside the input tree')
    if out.exists():
        raise ValueError('output exists; use a new evidence filename rather than overwrite')
    data = snapshot(root)
    out.parent.mkdir(parents=True, exist_ok=True)
    fd = os.open(out, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(fd, 'w', encoding='utf-8') as f:
        json.dump(data, f, ensure_ascii=False, indent=2)
        f.write('\n')
    print(json.dumps({'output': str(out), 'fileCount': data['fileCount'],
                      'treeSHA256': data['treeSHA256']}, ensure_ascii=False))
    return 0

if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, ValueError, subprocess.SubprocessError) as exc:
        print(f'ERROR: {exc}', file=sys.stderr)
        sys.exit(2)

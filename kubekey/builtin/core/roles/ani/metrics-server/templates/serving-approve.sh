#!/usr/bin/env bash
set -euo pipefail
python3 - <<'PY'
"""Approve only this site's node-authenticated kubelet serving CSRs."""
import base64
import json
import os
import re
import subprocess
import sys
import tempfile
import time
from pathlib import Path

NODES = {{ .ani.nodes | toJson }}
ADDRESSES = {{ .ani.node_addresses | toJson }}
KUBECONFIG = '{{ .ani.run.kubeconfig }}'
CA = '/etc/kubernetes/pki/ca.crt'
KUBECTL = ['kubectl', '--kubeconfig', KUBECONFIG]
EXPECTED = dict(zip(NODES, ADDRESSES, strict=True))
assert len(EXPECTED) == len(NODES) and len(EXPECTED) > 0, 'duplicate or empty selected node list'
assert Path(KUBECONFIG).is_file() and Path(CA).is_file(), 'kubeconfig or cluster CA absent'


def kubectl_json(*args):
    return json.loads(subprocess.check_output(KUBECTL + list(args), text=True))


def validate_request(item, node, ip):
    spec = item['spec']
    assert spec['signerName'] == 'kubernetes.io/kubelet-serving'
    assert spec.get('username') == f'system:node:{node}', f'{node}: CSR requester mismatch'
    groups = set(spec.get('groups', []))
    assert 'system:nodes' in groups and groups <= {'system:nodes', 'system:authenticated'}, f'{node}: CSR requester groups exceed node identity'
    usages = set(spec.get('usages', []))
    assert len(usages) == len(spec.get('usages', [])), f'{node}: duplicate CSR usages'
    assert usages in ({'digital signature', 'server auth'},
                      {'digital signature', 'key encipherment', 'server auth'}), f'{node}: unsafe CSR usages {sorted(usages)}'
    pem = base64.b64decode(spec['request'], validate=True)
    out = subprocess.run(['openssl', 'req', '-noout', '-text', '-subject', '-nameopt', 'RFC2253', '-verify'],
                         input=pem, capture_output=True, check=True).stdout.decode()
    subject = re.search(r'^\s*Subject:\s*(.+)$', out, re.M)
    assert subject, f'{node}: missing CSR subject'
    parts = {p.strip() for p in subject.group(1).split(',')}
    assert parts == {f'O=system:nodes', f'CN=system:node:{node}'}, f'{node}: unsafe CSR subject'
    lines = out.splitlines()
    marks = [i for i, line in enumerate(lines) if 'X509v3 Subject Alternative Name:' in line]
    assert len(marks) == 1 and marks[0] + 1 < len(lines), f'{node}: missing or duplicate SAN extension'
    indent = len(lines[marks[0]]) - len(lines[marks[0]].lstrip())
    chunks = []
    for line in lines[marks[0] + 1:]:
        if not line.strip() or len(line) - len(line.lstrip()) <= indent:
            break
        chunks.append(line.strip())
    assert chunks, f'{node}: empty SAN extension'
    san = {part.strip() for part in ' '.join(chunks).split(',')}
    allowed = {f'DNS:{node}', f'IP Address:{ip}'}
    assert f'IP Address:{ip}' in san and san <= allowed, f'{node}: SAN exceeds selected node identity'
    assert not re.search(r'X509v3 Basic Constraints:\s*critical\s*\n\s*CA:TRUE', out), f'{node}: CA request forbidden'


def validate_issued(item, node, ip):
    pem = base64.b64decode(item['status']['certificate'], validate=True)
    with tempfile.NamedTemporaryFile(prefix='ani-kubelet-serving-', suffix='.crt', dir='/etc/kubernetes/ani/metrics-server') as f:
        f.write(pem)
        f.flush()
        subprocess.run(['openssl', 'verify', '-CAfile', CA, f.name], check=True, stdout=subprocess.DEVNULL)
        out = subprocess.check_output(['openssl', 'x509', '-in', f.name, '-noout', '-text', '-nameopt', 'RFC2253'], text=True)
    lines = out.splitlines()
    marks = [i for i, line in enumerate(lines) if 'X509v3 Subject Alternative Name:' in line]
    assert len(marks) == 1 and marks[0] + 1 < len(lines), f'{node}: issued certificate has no SAN'
    subject = re.search(r'^\s*Subject:\s*(.+)$', out, re.M)
    assert subject and {part.strip() for part in subject.group(1).split(',')} == {f'O=system:nodes', f'CN=system:node:{node}'}, f'{node}: issued certificate subject mismatch'
    indent = len(lines[marks[0]]) - len(lines[marks[0]].lstrip())
    chunks = []
    for line in lines[marks[0] + 1:]:
        if not line.strip() or len(line) - len(line.lstrip()) <= indent:
            break
        chunks.append(line.strip())
    assert chunks, f'{node}: issued certificate SAN empty'
    san = {part.strip() for part in ' '.join(chunks).split(',')}
    assert f'IP Address:{ip}' in san and san <= {f'DNS:{node}', f'IP Address:{ip}'}, f'{node}: issued certificate SAN mismatch'


def check_nodes():
    actual = {}
    for item in kubectl_json('get', 'nodes', '-o', 'json')['items']:
        name = item['metadata']['name']
        if name in EXPECTED:
            addresses = {x['type']: x['address'] for x in item['status'].get('addresses', [])}
            assert addresses.get('InternalIP') == EXPECTED[name], f'{name}: Node InternalIP mismatch'
            assert addresses.get('Hostname') == name, f'{name}: Node hostname mismatch'
            actual[name] = any(x['type'] == 'Ready' and x['status'] == 'True' for x in item['status'].get('conditions', []))
    assert set(actual) == set(EXPECTED), 'selected nodes missing from Kubernetes API'
    return all(actual.values())


def main():
    deadline = time.monotonic() + 240
    issued = set()
    while time.monotonic() < deadline:
        try:
            ready = check_nodes()
            csrs = kubectl_json('get', 'csr', '-o', 'json')['items']
        except subprocess.CalledProcessError:
            time.sleep(2)
            continue
        issued = set()
        for item in csrs:
            spec = item.get('spec', {})
            if spec.get('signerName') != 'kubernetes.io/kubelet-serving':
                continue
            requester = spec.get('username', '')
            if not requester.startswith('system:node:'):
                continue
            node = requester.removeprefix('system:node:')
            if node not in EXPECTED:
                continue
            validate_request(item, node, EXPECTED[node])
            conditions = {x['type'] for x in item.get('status', {}).get('conditions', [])}
            assert 'Denied' not in conditions and 'Failed' not in conditions, f'{node}: serving CSR denied or failed'
            name = item['metadata']['name']
            if 'Approved' not in conditions:
                subprocess.run(KUBECTL + ['certificate', 'approve', name], check=True, stdout=subprocess.DEVNULL)
                print(f'approved validated kubelet serving CSR for {node}: {name}', flush=True)
            elif item.get('status', {}).get('certificate'):
                validate_issued(item, node, EXPECTED[node])
                issued.add(node)
        if issued == set(EXPECTED) and ready:
            print(f'validated cluster-signed kubelet serving certificates: {len(issued)}/{len(EXPECTED)} nodes')
            return 0
        time.sleep(2)
    raise RuntimeError(f'kubelet serving CSR issuance or node readiness timed out; issued={sorted(issued)} expected={sorted(EXPECTED)}')


if __name__ == '__main__':
    try:
        sys.exit(main())
    except Exception as exc:
        print(f'kubelet serving certificate gate failed: {exc}', file=sys.stderr)
        sys.exit(1)

PY

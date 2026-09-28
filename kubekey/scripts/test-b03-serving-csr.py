#!/usr/bin/env python3
"""Exercise B03's actual CSR validator with signed PKCS#10 test requests."""
import base64
import json
import subprocess
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
TEMPLATE = ROOT / 'builtin/core/roles/ani/metrics-server/templates/serving-approve.sh'


def make_csr(work, name, ip, extra_san=""):
    path = work / (name.replace(':', '-') + '.csr')
    key = work / (name.replace(':', '-') + '.key')
    subprocess.run([
        'openssl', 'req', '-new', '-newkey', 'rsa:2048', '-nodes',
        '-keyout', str(key), '-out', str(path),
        '-subj', f'/CN=system:node:ani-01/O=system:nodes',
        '-addext', f'subjectAltName=DNS:ani-01,IP:{ip}{extra_san}',
    ], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    return base64.b64encode(path.read_bytes()).decode()


def item(request):
    return {'spec': {
        'signerName': 'kubernetes.io/kubelet-serving',
        'username': 'system:node:ani-01',
        'groups': ['system:nodes', 'system:authenticated'],
        'usages': ['digital signature', 'key encipherment', 'server auth'],
        'request': request,
    }}


def expect_reject(fn, obj, label):
    try:
        fn(obj, 'ani-01', '172.16.101.10')
    except (AssertionError, subprocess.CalledProcessError, ValueError, KeyError):
        print(f'PASS {label}: refused')
    else:
        raise AssertionError(f'{label}: unsafe CSR accepted')


def main():
    with tempfile.TemporaryDirectory(prefix='ani-b03-csr-') as td:
        work = Path(td)
        kubeconfig = work / 'isolated-kubeconfig'
        ca = work / 'test-ca'
        kubeconfig.write_text('test only\n')
        ca_key = work / 'ca.key'
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes',
                        '-keyout', str(ca_key), '-out', str(ca), '-days', '1',
                        '-subj', '/CN=ANI B03 test CA',
                        '-addext', 'basicConstraints=critical,CA:TRUE'],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        source = TEMPLATE.read_text()
        start = source.index("python3 - <<'PY'\n") + len("python3 - <<'PY'\n")
        end = source.rindex('\nPY\n')
        code = source[start:end]
        code = code.replace('{{ .ani.nodes | toJson }}', json.dumps(['ani-01']))
        code = code.replace('{{ .ani.node_addresses | toJson }}', json.dumps(['172.16.101.10']))
        code = code.replace("'{{ .ani.run.kubeconfig }}'", repr(str(kubeconfig)))
        code = code.replace("'/etc/kubernetes/pki/ca.crt'", repr(str(ca)))
        code = code.replace("dir='/etc/kubernetes/ani/metrics-server'", f"dir={str(work)!r}")
        scope = {'__name__': 'b03_csr_test'}
        exec(compile(code, str(TEMPLATE), 'exec'), scope)
        validate = scope['validate_request']
        good = item(make_csr(work, 'valid', '172.16.101.10'))
        validate(good, 'ani-01', '172.16.101.10')
        print('PASS valid node-authenticated PKCS#10 CSR')
        leaf = work / 'valid.crt'
        subprocess.run(['openssl', 'x509', '-req', '-in', str(work / 'valid.csr'),
                        '-CA', str(ca), '-CAkey', str(ca_key), '-CAcreateserial',
                        '-days', '1', '-copy_extensions', 'copy', '-out', str(leaf)],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        issued = item(good['spec']['request'])
        issued['status'] = {'certificate': base64.b64encode(leaf.read_bytes()).decode()}
        scope['validate_issued'](issued, 'ani-01', '172.16.101.10')
        print('PASS cluster-CA-signed issued certificate')
        wrong_ip = item(make_csr(work, 'wrong-ip', '172.16.101.99'))
        expect_reject(validate, wrong_ip, 'foreign IP SAN')
        extra_san = item(make_csr(work, 'extra-san', '172.16.101.10', ',DNS:unauthorized-node.example.internal'))
        expect_reject(validate, extra_san, 'extra DNS SAN')
        wrong_user = item(good['spec']['request'])
        wrong_user['spec']['username'] = 'system:admin'
        expect_reject(validate, wrong_user, 'foreign requester')
        wrong_group = item(good['spec']['request'])
        wrong_group['spec']['groups'] = ['system:authenticated']
        expect_reject(validate, wrong_group, 'missing node group')
        privileged_group = item(good['spec']['request'])
        privileged_group['spec']['groups'].append('system:masters')
        expect_reject(validate, privileged_group, 'unexpected privileged group')
        wrong_usage = item(good['spec']['request'])
        wrong_usage['spec']['usages'].append('client auth')
        expect_reject(validate, wrong_usage, 'client authentication usage')
        broken = item(base64.b64encode(b'not a CSR').decode())
        expect_reject(validate, broken, 'invalid PKCS#10 signature')
    print('B03 serving CSR safety cases: 9/9')


if __name__ == '__main__':
    main()

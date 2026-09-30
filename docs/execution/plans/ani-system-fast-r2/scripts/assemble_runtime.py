#!/usr/bin/env python3
"""Assemble Fedora private runtime inputs from saved seeds and existing Secrets.

Read-only cluster access; this does not generate/rotate credentials or apply
resources. It refuses to overwrite a prepared runtime input.
"""
import argparse
import base64
import json
import os
from pathlib import Path
import subprocess
from urllib.parse import quote
import yaml


def assemble(root, kubectl, site_input, output):
    if output.exists(): raise ValueError('runtime input already exists; reuse it')
    def get(kind, name, namespace=None):
        cmd = [str(kubectl), '--request-timeout=30s']
        if namespace: cmd += ['-n', namespace]
        return json.loads(subprocess.run(cmd + ['get', kind, name, '-o', 'json'], check=True, capture_output=True).stdout)
    def secret(name):
        return {k: base64.b64decode(v).decode() for k,v in get('secret', name, 'ani-platform')['data'].items()}
    seed = json.loads((root / 'private/runtime-seed.json').read_text())
    access = root / 'private/access'
    valkey = secret('ani-valkey-auth')['valkey-password']
    nats = secret('ani-nats-auth')['token']
    s3_ca = secret('ani-rustfs-tls')['ca.crt']
    cluster_ca = base64.b64decode(get('secret', 'ani-root-ca', 'cert-manager')['data']['tls.crt']).decode()
    (access / 'rustfs-ca.pem').write_text(s3_ca)
    site = yaml.safe_load(site_input.read_text())
    site['kubernetes_service_ip'] = get('service', 'kubernetes', 'default')['spec']['clusterIP']
    site['cluster_uid'] = get('namespace', 'kube-system')['metadata']['uid']
    passwords = seed['database_passwords']
    def db(role):
        return 'postgresql://' + role + ':' + quote(passwords[role], safe='') + '@postgresql.ani-platform.svc:5432/ani_fast_20260930?sslmode=prefer'
    common = {'database_url': db('ani_fast_app_user'), 'nats_url': 'nats://' + quote(nats, safe='') + '@nats.ani-platform.svc:4222',
              'redis_url': 'redis://:' + quote(valkey, safe='') + '@valkey.ani-platform.svc:6379/0',
              'auth_jwt_issuer': 'ani-auth-service', 'jwt_private_key_pem': (access / 'jwt-private.pem').read_text(),
              'jwt_public_key_pem': (access / 'jwt-public.pem').read_text()}
    objects = []
    def add(name, data, namespace='ani-system', kind='Opaque'):
        encoded = {key: base64.b64encode(value.encode()).decode() for key,value in data.items()}
        objects.append({'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': name, 'namespace': namespace}, 'type': kind, 'data': encoded})
    add('ani-services-runtime', common)
    add('ani-auth-production-shaped-runtime', common)
    add('ani-gateway-production-shaped-runtime', {'database_url': db('ani_fast_gateway_user')})
    add('ani-inference-platform-runtime', {'database_url': db('ani_fast_gateway_user')})
    add('ani-metering-runtime', {**common, 'database_url': db('ani_fast_metering_user')})
    add('ani-session-gateway-secrets', {'redis-url': common['redis_url']})
    objects[-1]['data']['ticket-encryption-key'] = seed['ticket_encryption_key']
    mint = seed['mint']
    add('inference-mint', {'credentials': ','.join(k + ':' + v for k,v in mint.items()),
        'caller_secret': mint['inference-service'], 'tenant_secret': mint['tenant-service'],
        'platform_settings_secret': mint['platform-settings-service']})
    # Actual services have mint clients configured; no fabricated static token.
    add('inference-c21-core-token', {'token': ''})
    own = seed['own_s3']
    add('ani-objectstore-production-shaped-runtime', {'access_key_id': own['access_key'], 'secret_access_key': own['secret_key'],
        'endpoint': site['s3_endpoint'], 'public_endpoint': site['s3_public_endpoint']})
    add('kb-rag-minio', {'access_key': own['access_key'], 'secret_key': own['secret_key']})
    add('ani-vectorstore-production-shaped-runtime', {'endpoint': site['milvus_endpoint'], 'database': 'default', 'token': ''})
    add('ani-fast-kaiwu-runtime', seed['kaiwu'])
    add('ani-kaiwu-runtime', {'proxy_signing_secret': seed['kaiwu']['console_secret']})
    robot = json.loads((access / 'harbor-task-pull-robot.json').read_text())
    add('ani-fast-registry-runtime', {'username': robot['name'], 'password': robot['secret']})
    pull = (access / 'harbor-task-pull-auth.json').read_text()
    for namespace in ('ani-system', 'ani-aigw', 'ani-business-envoy', 'ani-business-aigw'):
        add(site['image_pull_secret'], {'.dockerconfigjson': pull}, namespace, 'kubernetes.io/dockerconfigjson')
    seed.update({'secrets': objects, 's3_ca_pem': s3_ca,
        'site_ca_pem': Path('/etc/pki/tls/certs/ca-bundle.crt').read_text() + '\n' + cluster_ca + '\n' + s3_ca + '\n' + (access / 'registry-ca/ca.crt').read_text()})
    output.write_text(json.dumps(seed, indent=2) + '\n'); output.chmod(0o600)
    site_output = output.with_suffix('.site.yaml')
    site_output.write_text(yaml.safe_dump(site)); site_output.chmod(0o600)
    print('Saved private runtime input and finite site; Secret manifests=' + str(len(objects)))


if __name__ == '__main__':
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    for key in ('root', 'kubectl', 'site-input', 'output'): parser.add_argument('--' + key, type=Path, required=True)
    args = parser.parse_args()
    assemble(args.root, args.kubectl, args.site_input, args.output)

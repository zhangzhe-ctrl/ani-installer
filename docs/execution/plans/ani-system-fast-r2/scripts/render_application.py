#!/usr/bin/env python3
"""Fedora-only preparation of the one legacy ANI application package.

The reference is read outside Git; private runtime Secrets remain outside Git.
No cluster writes, source builds, downloads or secret generation happen here.
"""
import argparse
import base64
import copy
import hashlib
import json
from pathlib import Path
import re

import yaml

TASK = 'ani-system-fast-20260930'
CORE = {'ani-auth-service', 'ani-gateway'}
APPS = {
    'ani-metering-service', 'ani-reconcile-worker-a', 'ani-reconcile-worker-b',
    'ani-session-gateway', 'inference-service', 'inference-gateway-publisher',
    'model-service', 'model-import-worker', 'platform-settings-service',
    'task-service', 'tenant-service', 'kb-service', 'rag-engine',
    'ani-console', 'ani-boss-console',
}
DIGEST_REF = re.compile(r'[^\s@]+@sha256:[a-f0-9]{64}\Z')


def set_pointer(doc, pointer, value):
    parts = pointer.lstrip('/').split('/')
    current = doc
    for part in parts[:-1]:
        current = current[int(part)] if isinstance(current, list) else current[part]
    final = int(parts[-1]) if isinstance(current, list) else parts[-1]
    current[final] = value


def clean(doc):
    doc = copy.deepcopy(doc)
    doc.pop('status', None)
    meta = doc['metadata']
    for key in ('uid', 'resourceVersion', 'managedFields', 'creationTimestamp',
                'ownerReferences', 'finalizers', 'generation', 'annotations'):
        meta.pop(key, None)
    meta.setdefault('labels', {})['ani.io/app-task'] = TASK
    if doc['kind'] == 'Service':
        for key in ('clusterIP', 'clusterIPs', 'healthCheckNodePort'):
            doc['spec'].pop(key, None)
        for port in doc['spec'].get('ports', []):
            port.pop('nodePort', None)
    return doc


def image_mappings(image_map):
    if image_map.get('registryImport') != 'verified':
        raise ValueError('registry import and digest readback must be verified')
    origins, refs = {}, {}
    rows = image_map['images']
    for row in rows:
        target = row.get('targetDigestRef')
        if not target or not DIGEST_REF.fullmatch(target):
            raise ValueError('missing verified registry digest: ' + row['id'])
        for origin in row.get('uses', row.get('request', {}).get('origins', [])):
            origins[(origin['resource'], origin['path'])] = target
            old = origin['reference']
            if old in refs and refs[old] != target:
                raise ValueError('ambiguous image use: ' + old)
            refs[old] = target
    return origins, refs


def update_env(container, values):
    env = {item['name']: item for item in container.get('env', [])}
    for name, value in values.items():
        if value is None:
            env.pop(name, None)
        elif isinstance(value, dict):
            env[name] = {'name': name, 'valueFrom': value}
        else:
            env[name] = {'name': name, 'value': str(value)}
    # The reference used redaction strings for some real secretKeyRefs.
    for item in env.values():
        value = item.get('value', '')
        match = re.fullmatch(r'__SECRET_REF__ani-system/([^:]+):(.+)__', value)
        if match:
            item.pop('value')
            item['valueFrom'] = {'secretKeyRef': {'name': match[1], 'key': match[2]}}
    container['env'] = list(env.values())


def render(reference, site, image_map, runtime, output, gateway_material):
    origins, refs = image_mappings(image_map)
    required = ('console_url', 'boss_url', 'api_url', 'websocket_url',
                's3_endpoint', 's3_public_endpoint', 'milvus_endpoint',
                'prometheus_url', 'loki_url', 'storage_class')
    for key in required:
        if not site.get(key) or '__' in str(site[key]):
            raise ValueError('missing site parameter: ' + key)
    if not site['s3_endpoint'].startswith('https://') or not site['s3_public_endpoint'].startswith('https://'):
        raise ValueError('S3 internal and public endpoints must use HTTPS')
    if site.get('target_nodes') != ['172.16.101.10', '172.16.101.11', '172.16.101.12']:
        raise ValueError('this application selection targets .10/.11/.12')
    secrets = runtime['secrets']
    ticket = next(d for d in secrets if d['metadata']['name'] == 'ani-session-gateway-secrets')
    if len(base64.b64decode(ticket['data']['ticket-encryption-key'], validate=True)) != 32:
        raise ValueError('Session ticket key must contain exactly 32 raw bytes')
    groups = {key: [] for key in ('crds', 'prepare', 'controllers', 'core', 'apps', 'gateway')}
    for ns in ('ani-system', 'ani-aigw', 'ani-business-envoy', 'ani-business-aigw'):
        groups['prepare'].append(clean({'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': ns}}))
    groups['prepare'].extend(clean(d) for d in secrets)
    for folder in ('config/ani-system', 'workloads/ani-system', 'certificates', 'rbac'):
        for path in sorted((reference / folder).rglob('*.yaml')):
            doc = yaml.safe_load(path.read_text())
            kind, meta = doc['kind'], doc['metadata']
            name, ns = meta['name'], meta.get('namespace', '')
            if any(marker in json.dumps(doc) for marker in ('sprint13-prometheus', 'ani-dcgm-exporter', 'ani-fluent-bit')):
                continue
            if ns and ns not in ('ani-system', 'cert-manager'):
                continue
            if kind == 'Deployment' and name not in CORE | APPS:
                continue
            if kind == 'DaemonSet' or name in ('ani-dex', 'ani-dex-production-config', 'exporter-metrics-config-map'):
                continue
            if kind in ('Role', 'RoleBinding', 'ClusterRole', 'ClusterRoleBinding') and any(
                marker in name for marker in ('envoy', 'eg-gateway', 'aieg', 'dex')
            ):
                continue
            resource = f'{ns or "_cluster"}/{kind}/{name}'
            for (resource_name, pointer), target in origins.items():
                if resource_name == resource and pointer.startswith(('/spec/', '/data/')):
                    set_pointer(doc, pointer, target)
            doc = clean(doc)
            selected = 'prepare'
            if kind == 'Deployment':
                selected = 'core' if name in CORE else 'apps'
                pod = doc['spec']['template']['spec']
                pod.pop('nodeName', None)
                pod.get('nodeSelector', {}).pop('kubernetes.io/hostname', None)
                for container in pod['containers'] + pod.get('initContainers', []):
                    old = container['image']
                    container['image'] = refs.get(old, old)
                    if not DIGEST_REF.fullmatch(container['image']):
                        raise ValueError('unmapped image: ' + old)
                    container['imagePullPolicy'] = 'IfNotPresent'
                    values = {}
                    for item in container.get('env', []):
                        if item['name'] == 'OBJECT_STORE_SECURE': values[item['name']] = 'true'
                        if item['name'] == 'OBJECT_STORE_BUCKET_PREFIX': values[item['name']] = 'ani-fast-'
                        if item['name'] == 'VECTOR_STORE_COLLECTION_PREFIX': values[item['name']] = 'ani_fast_'
                        if item['name'] == 'INSTANCE_OBSERVABILITY_PROMETHEUS_URL': values[item['name']] = site['prometheus_url']
                        if item['name'] == 'INSTANCE_OBSERVABILITY_LOKI_URL': values[item['name']] = site['loki_url']
                        if item['name'].startswith(('AUTH_OIDC_', 'VCLUSTER_', 'VLLM_')): values[item['name']] = None
                        if item['name'] in ('K8S_CLUSTER_PROVIDER_MODE', 'K8S_CLUSTER_PROXY_MODE'): values[item['name']] = 'local'
                        if item['name'] == 'GPU_INVENTORY_PROVIDER': values[item['name']] = 'not_configured'
                        if name in ('kb-service', 'rag-engine') and item['name'] in ('CORE_SERVICE_TOKEN', 'ANI_CORE_API_TOKEN'):
                            values[item['name']] = None  # actual KB/RAG use request-scoped auth
                        if item['name'] == 'ANI_DEV_TENANT_ID': values[item['name']] = None
                    values.update(site.get('deployment_env', {}).get(name, {}))
                    update_env(container, values)
                    # Trust the site's existing internal CA for S3 HTTPS.
                    update_env(container, {'SSL_CERT_FILE': '/etc/ani-site-ca/ca.crt',
                                           'AWS_CA_BUNDLE': '/etc/ani-site-ca/ca.crt'})
                    container.setdefault('volumeMounts', []).append({'name': 'ani-site-ca', 'mountPath': '/etc/ani-site-ca', 'readOnly': True})
                pod.setdefault('volumes', []).append({'name': 'ani-site-ca', 'configMap': {'name': 'ani-site-ca'}})
            if kind == 'Service' and name in ('ani-console', 'ani-boss-console', 'ani-gateway', 'ani-session-gateway-websocket'):
                for port in doc['spec']['ports']:
                    number = site.get('node_ports', {}).get(name, {}).get(port['name'])
                    if number: port['nodePort'] = int(number)
            if kind == 'ConfigMap':
                if name == 'ani-session-gateway':
                    doc['data']['ALLOWED_ORIGINS'] = ','.join([site['console_url'], site['boss_url']])
                    doc['data']['PUBLIC_WS_BASE_URL'] = site['websocket_url']
                if name == 'kb-service-env':
                    doc['data']['RAG_ENGINE_GRPC_ADDR'] = 'rag-engine.ani-system.svc.cluster.local:50052'
                    doc['data'].pop('RAG_ENGINE_ADDR', None)
                if name == 'rag-engine-env':
                    doc['data'] = {'ANI_GATEWAY_INTERNAL_URL': 'http://ani-gateway.ani-system.svc.cluster.local:8080',
                                   'GRPC_BIND_ADDR': '[::]:50052', 'EMBEDDING_API_BASE': '',
                                   'EMBEDDING_API_KEY': '', 'VLLM_API_BASE': '', 'VLLM_API_KEY': '', 'VLLM_MODEL': ''}
                doc['data'].update(site.get('configmap_data', {}).get(name, {}))
                if name == 'ani-inference-materialization':
                    doc['data']['model_fetcher_allow_insecure_http'] = 'false'
            groups[selected].append(doc)
    if not gateway_material or not gateway_material.is_dir():
        raise ValueError('verified business gateway/AI Gateway manifests are required')
    for path in sorted(gateway_material.glob('*.yaml')):
        if path.stem not in ('crds', 'controllers', 'gateway'):
            raise ValueError('unknown gateway material stage: ' + path.name)
        groups[path.stem].extend(clean(doc) for doc in yaml.safe_load_all(path.read_text()) if doc)
    groups['prepare'].append(clean({'apiVersion': 'v1', 'kind': 'ConfigMap',
        'metadata': {'name': 'ani-site-ca', 'namespace': 'ani-system'},
        'data': {'ca.crt': runtime['site_ca_pem']}}))
    all_docs = [doc for values in groups.values() for doc in values]
    serialized = yaml.safe_dump_all(all_docs, sort_keys=False)
    forbidden = ('__REGENERATE_OR_SUPPLY__', '__SECRET_REF__', '10.10.1.66', '10.10.1.67',
                 'ani-s05-', 'ani-s06-', 'ani-s07-', 'ani-reconcile-ha-', 'qwen2.5-0.5b', 'bge-small-zh-v1-5-test')
    for marker in forbidden:
        if marker in serialized: raise ValueError('unresolved historical dependency: ' + marker)
    # Every referenced runtime Secret key must have a supplied or certificate-generated source.
    available = {(doc['metadata'].get('namespace'), doc['metadata']['name']): set(doc.get('data', {}))
                 for doc in all_docs if doc['kind'] == 'Secret'}
    for doc in all_docs:
        if doc['kind'] == 'Certificate':
            available[(doc['metadata']['namespace'], doc['spec']['secretName'])] = {'tls.crt', 'tls.key', 'ca.crt'}
    for doc in all_docs:
        if doc['kind'] != 'Deployment': continue
        for container in doc['spec']['template']['spec']['containers']:
            for item in container.get('env', []):
                ref = item.get('valueFrom', {}).get('secretKeyRef')
                if ref and ref['key'] not in available.get((doc['metadata']['namespace'], ref['name']), set()):
                    raise ValueError('missing runtime Secret key: ' + ref['name'] + '/' + ref['key'])
    output.mkdir(mode=0o700, exist_ok=False)
    inventory, workloads = [], []
    for group, docs in groups.items():
        (output / (group + '.yaml')).write_text(yaml.safe_dump_all(docs, sort_keys=False))
        for doc in docs:
            meta = doc['metadata'];ns = meta.get('namespace', '-')
            inventory.append('\t'.join([group, ns, doc['kind'], meta['name']]))
            if doc['kind'] in ('Deployment', 'StatefulSet', 'DaemonSet'):
                workloads.append('\t'.join([ns, doc['kind'].lower(), meta['name']]))
    (output / 'inventory.tsv').write_text('\n'.join(inventory) + '\n')
    (output / 'workloads.tsv').write_text('\n'.join(workloads) + '\n')
    (output / 'expected-nodes.txt').write_text('\n'.join(site['target_nodes']) + '\n')
    (output / 'expected-cluster-uid.txt').write_text(site.get('cluster_uid', '') + '\n')
    (output / 'actual-images.lock.json').write_text(json.dumps(image_map, indent=2) + '\n')
    crds = output / 'crds';crds.mkdir()
    specs = []
    for doc in groups['crds']:
        name = doc['metadata']['name'];file = name + '.yaml'
        (crds / file).write_text(yaml.safe_dump(doc, sort_keys=False))
        digest = hashlib.sha256(json.dumps(doc['spec'], sort_keys=True, separators=(',', ':')).encode()).hexdigest()
        specs.append('\t'.join([file, name, digest]))
    (crds / 'specs.tsv').write_text('\n'.join(specs) + '\n')
    # SQL is added after the selected schema has been verified; freeze checksums last.
    print('Rendered private application manifests; SQL/checksums still required: ' + str(output))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('reference', 'site', 'image-map', 'runtime', 'output', 'gateway-material'):
        parser.add_argument('--' + name, required=True, type=Path)
    args = parser.parse_args()
    render(args.reference, yaml.safe_load(args.site.read_text()),
           json.loads(args.image_map.read_text()), json.loads(args.runtime.read_text()),
           args.output, args.gateway_material)

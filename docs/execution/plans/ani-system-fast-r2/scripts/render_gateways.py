#!/usr/bin/env python3
"""Render the supplied Charts for the isolated business gateway on Fedora."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
from urllib.parse import urlparse

import yaml

from render_application import clean, image_mappings


def render(bundle, lock, helm, reference, site, runtime, output):
    image_mappings(lock)  # Require completed import/readback first.
    images = {row['id']: row['targetDigestRef'] for row in lock['images']}
    for required in ('envoy-gateway', 'envoy-proxy', 'ai-gateway-controller', 'ai-gateway-extproc', 'envoy-authz-adapter'):
        if required not in images: raise ValueError('missing business gateway image: ' + required)
    charts = {row['id']: row for row in lock['charts']}
    output.mkdir(mode=0o700, exist_ok=False)
    eg_config = {
        'gateway': {'controllerName': 'ani.io/ani-system-fast-gateway'},
        'provider': {'type': 'Kubernetes', 'kubernetes': {
            'watch': {'type': 'Namespaces', 'namespaces': ['ani-aigw']},
            'shutdownManager': {'image': images['envoy-gateway']}}},
        'logging': {'level': {'default': 'info'}},
        'extensionApis': {'enableBackend': True, 'enableEnvoyPatchPolicy': True},
        'extensionManager': {
            'hooks': {'xdsTranslator': {
                'post': ['Translation', 'Cluster', 'Route'],
                'translation': {key: {'includeAll': True} for key in ('cluster', 'listener', 'route', 'secret')}}},
            'service': {'fqdn': {'hostname': 'ai-gateway-controller.ani-business-aigw.svc.cluster.local', 'port': 1063}}},
    }
    eg = {'config': {'envoyGateway': eg_config}, 'topologyInjector': {'enabled': False}}
    ai = {'controller': {
        'mcp': {'sessionEncryption': {'seed': runtime['mcp_session_seed']}},
        'watch': {'namespaces': ['ani-aigw', 'ani-business-envoy']},
        'mutatingWebhook': {
            'certManager': {'enable': True},
            'namespaceSelector': {'matchLabels': {'ani.io/app-task': 'ani-system-fast-20260930'}}}},
        'envoyGateway': {'namespace': 'ani-business-envoy'}}
    docs = []
    with tempfile.TemporaryDirectory(prefix='gateway-values-') as temp:
        for ident, release, ns, values in (
            ('envoy-gateway', 'ani-business-eg', 'ani-business-envoy', eg),
            ('ai-gateway-crds', 'ani-business-aigw-crds', 'ani-business-aigw', {}),
            ('ai-gateway', 'ani-business-aigw', 'ani-business-aigw', ai),
        ):
            chart = charts[ident];path = bundle / 'charts' / ident / chart['file']
            if hashlib.sha256(path.read_bytes()).hexdigest() != chart['sha256']:
                raise ValueError('Chart bytes differ from the verified application lock: ' + ident)
            vf = Path(temp) / (ident + '.yaml');vf.write_text(yaml.safe_dump(values))
            proc = subprocess.run([str(helm), 'template', release, str(path), '--namespace', ns, '--include-crds', '-f', str(vf)],
                                  check=True, capture_output=True, text=True)
            docs.extend(doc for doc in yaml.safe_load_all(proc.stdout) if doc)
    crds, controllers = [], []
    for raw in docs:
        # Keep cainjector's required annotations; remove Helm release ownership.
        annotations = {k: v for k, v in raw.get('metadata', {}).get('annotations', {}).items()
                       if k.startswith('cert-manager.io/')}
        doc = clean(raw)
        if annotations: doc['metadata']['annotations'] = annotations
        if doc['kind'] == 'CustomResourceDefinition':
            crds.append(doc);continue
        pod = doc.get('spec', {}).get('template', {}).get('spec', {})
        for container in pod.get('containers', []) + pod.get('initContainers', []):
            old = container['image']
            if 'envoyproxy/gateway:' in old: container['image'] = images['envoy-gateway']
            elif 'envoyproxy/ai-gateway-controller:' in old: container['image'] = images['ai-gateway-controller']
            else: raise ValueError('unmapped Chart/init image: ' + old)
            args = container.get('args', [])
            container['args'] = [('--extProcImage=' + images['ai-gateway-extproc']) if arg.startswith('--extProcImage=') else arg for arg in args]
        controllers.append(doc)
    for group, selected in (('crds', crds), ('controllers', controllers)):
        (output / (group + '.yaml')).write_text(yaml.safe_dump_all(selected, sort_keys=False))
    routes = []
    for path in sorted((reference / 'gateway/ani-aigw').glob('*.yaml')):
        doc = clean(yaml.safe_load(path.read_text()))
        if doc['kind'] == 'EnvoyProxy':
            doc['spec']['provider']['kubernetes']['envoyDeployment']['container']['image'] = images['envoy-proxy']
        if doc['kind'] == 'Gateway':
            doc['spec']['listeners'] = [{'name': 'https', 'protocol': 'HTTPS', 'port': 443,
                'hostname': urlparse(site['inference_url']).hostname,
                'tls': {'mode': 'Terminate', 'certificateRefs': [{'kind': 'Secret', 'name': 'ani-entry-tls'}]},
                'allowedRoutes': {'namespaces': {'from': 'Same'}}}]
        routes.append(doc)
    routes.append(clean({'apiVersion': 'gateway.networking.k8s.io/v1', 'kind': 'GatewayClass',
        'metadata': {'name': 'ani-aigw'}, 'spec': {'controllerName': 'ani.io/ani-system-fast-gateway'}}))
    for folder in ('workloads/ani-aigw', 'rbac/ani-aigw'):
        for path in sorted((reference / folder).glob('*.yaml')):
            doc = clean(yaml.safe_load(path.read_text()))
            if doc['kind'] == 'ServiceAccount' and doc['metadata']['name'] == 'default':
                continue
            if doc['kind'] == 'Deployment':
                pod = doc['spec']['template']['spec'];pod.pop('nodeName', None)
                pod.get('nodeSelector', {}).pop('kubernetes.io/hostname', None)
                for container in pod['containers']: container['image'] = images['envoy-authz-adapter']
            routes.append(doc)
    hosts = [urlparse(site[key]).hostname for key in ('console_url', 'boss_url', 'api_url', 'websocket_url', 'inference_url')]
    if any(not host for host in hosts): raise ValueError('public entry hostnames are required')
    routes.append(clean({'apiVersion': 'cert-manager.io/v1', 'kind': 'Certificate',
        'metadata': {'name': 'ani-entry-tls', 'namespace': 'ani-aigw'},
        'spec': {'secretName': 'ani-entry-tls', 'dnsNames': sorted(set(hosts)),
                 'issuerRef': {'name': 'ani-ca', 'kind': 'ClusterIssuer'}}}))
    entry = {'apiVersion': 'gateway.networking.k8s.io/v1', 'kind': 'Gateway',
             'metadata': {'name': 'ani-entry', 'namespace': 'ani-aigw'},
             'spec': {'gatewayClassName': 'ani-aigw',
                      'infrastructure': {'parametersRef': {'group': 'gateway.envoyproxy.io', 'kind': 'EnvoyProxy', 'name': 'ani-aigw'}},
                      'listeners': [{'name': 'https', 'port': 443, 'protocol': 'HTTPS',
                          'tls': {'mode': 'Terminate', 'certificateRefs': [{'kind': 'Secret', 'name': 'ani-entry-tls'}]},
                          'allowedRoutes': {'namespaces': {'from': 'Same'}}}]}}
    routes.append(clean(entry))
    backends = [('console_url', 'ani-console', 80), ('boss_url', 'ani-boss-console', 80),
                ('api_url', 'ani-gateway', 8080), ('websocket_url', 'ani-session-gateway-websocket', 8080)]
    for key, service, port in backends:
        routes.append(clean({'apiVersion': 'gateway.networking.k8s.io/v1', 'kind': 'HTTPRoute',
            'metadata': {'name': service, 'namespace': 'ani-aigw'},
            'spec': {'parentRefs': [{'name': 'ani-entry'}], 'hostnames': [urlparse(site[key]).hostname],
                     'rules': [{'matches': [{'path': {'type': 'PathPrefix', 'value': '/'}}],
                                'backendRefs': [{'name': service, 'namespace': 'ani-system', 'port': port}]}]}}))
    routes.append(clean({'apiVersion': 'gateway.networking.k8s.io/v1beta1', 'kind': 'ReferenceGrant',
        'metadata': {'name': 'ani-entry-backends', 'namespace': 'ani-system'},
        'spec': {'from': [{'group': 'gateway.networking.k8s.io', 'kind': 'HTTPRoute', 'namespace': 'ani-aigw'}],
                 'to': [{'group': '', 'kind': 'Service', 'name': service} for _, service, _ in backends]}}))
    (output / 'gateway.yaml').write_text(yaml.safe_dump_all(routes, sort_keys=False))
    print('Business gateways and TLS entry routes rendered; cluster compatibility remains unverified: ' + str(output))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('bundle', 'image-lock', 'helm', 'reference', 'site', 'runtime', 'output'):
        parser.add_argument('--' + name, required=True, type=Path)
    args = parser.parse_args()
    render(args.bundle, json.loads(args.image_lock.read_text()), args.helm, args.reference,
           yaml.safe_load(args.site.read_text()), json.loads(args.runtime.read_text()), args.output)

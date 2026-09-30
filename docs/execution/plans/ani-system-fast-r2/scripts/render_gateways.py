#!/usr/bin/env python3
"""Render the supplied Charts for the isolated business gateway on Fedora."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile

import yaml

from render_application import clean, image_mappings


def render(bundle, lock, helm, output):
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
    print('Business gateway Charts rendered; routes/authz and cluster compatibility remain to be verified: ' + str(output))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('bundle', 'image-lock', 'helm', 'output'):
        parser.add_argument('--' + name, required=True, type=Path)
    args = parser.parse_args()
    render(args.bundle, json.loads(args.image_lock.read_text()), args.helm, args.output)

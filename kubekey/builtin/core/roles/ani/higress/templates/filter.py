#!/usr/bin/env python3
"""Bound the fixed official Chart to this site's independent shared gateway."""
import sys
import yaml

OWNER = '{{ .kubernetes.cluster_name }}'
IMAGES = {
    'higress-core': '{{ (index .ani.components "higress").controller_image }}',
    'discovery': '{{ (index .ani.components "higress").pilot_image }}',
    'higress-gateway': '{{ (index .ani.components "higress").gateway_image }}',
}
EXPECTED = {
    ('ServiceAccount', 'higress-controller'), ('ServiceAccount', 'higress-gateway'),
    ('ConfigMap', 'higress-config'),
    ('ClusterRole', 'higress-controller-higress-system'), ('ClusterRole', 'higress-gateway-higress-system'),
    ('ClusterRoleBinding', 'higress-controller-higress-system'), ('ClusterRoleBinding', 'higress-gateway-higress-system'),
    ('Role', 'higress-controller'), ('Role', 'higress-gateway'),
    ('RoleBinding', 'higress-controller'), ('RoleBinding', 'higress-gateway'),
    ('Service', 'higress-controller'), ('Service', 'higress-gateway'),
    ('Deployment', 'higress-controller'), ('Deployment', 'higress-gateway'),
}
CONSOLE = {
    ('ServiceAccount', 'higress-console'), ('ServiceAccount', 'higress-console-promtail'),
    ('Secret', 'higress-console'), ('ConfigMap', 'higress-console'),
    ('ClusterRole', 'higress-console-higress-system'), ('ClusterRoleBinding', 'higress-console-higress-system'),
    ('Role', 'higress-console'), ('Role', 'higress-console-promtail'),
    ('RoleBinding', 'higress-console'), ('RoleBinding', 'higress-console-promtail'),
    ('Service', 'higress-console'), ('Deployment', 'higress-console'),
}
REMOVED = CONSOLE | {('EnvoyFilter', 'higress-gateway-global-custom-response')}
objects = [o for o in yaml.safe_load_all(sys.stdin) if o]
kept, removed, seen = [], set(), set()
for obj in objects:
    key = (obj['kind'], obj['metadata']['name'])
    if key in seen:
        raise ValueError('duplicate official Higress object: ' + '/'.join(key))
    seen.add(key)
    if key in REMOVED:
        removed.add(key)
        continue
    if key not in EXPECTED:
        raise ValueError('unexpected official Higress object: ' + '/'.join(key))
    obj['metadata'].setdefault('labels', {})['ani.io/managed-by'] = OWNER
    if key[0] == 'Deployment':
        template = obj['spec']['template']
        template['metadata'].setdefault('labels', {})['ani.io/managed-by'] = OWNER
        spec = template['spec']
        expected_containers = {'higress-core', 'discovery'} if key[1] == 'higress-controller' else {'higress-gateway'}
        if {c['name'] for c in spec['containers']} != expected_containers or spec.get('initContainers'):
            raise ValueError('official Higress container identity changed')
        for container in spec['containers']:
            container['image'] = IMAGES[container['name']]
            if container['name'] == 'higress-gateway':
{{ if eq (index .ani.components "higress").wasm_registry "harbor" }}
                container.setdefault('env', []).append({'name': 'SSL_CERT_FILE', 'value': '/etc/ani-wasm-trust/ca.crt'})
                container.setdefault('volumeMounts', []).append({'name': 'ani-wasm-trust', 'mountPath': '/etc/ani-wasm-trust', 'readOnly': True})
                spec.setdefault('volumes', []).append({'name': 'ani-wasm-trust', 'configMap': {'name': 'ani-higress-wasm-trust'}})
{{ end }}
    kept.append(obj)
if removed != REMOVED or { (o['kind'], o['metadata']['name']) for o in kept } != EXPECTED:
    raise ValueError('official Higress Chart set differs from fixed source approval')
yaml.safe_dump_all(kept, sys.stdout, sort_keys=False)

#!/usr/bin/env python3
"""Bounded official LLMI Chart filter: no shared APIs or GIE installation."""
import sys
import yaml

GIE = {'inferencemodelrewrites.inference.networking.x-k8s.io',
       'inferenceobjectives.inference.networking.x-k8s.io',
       'inferencepoolimports.inference.networking.x-k8s.io',
       'inferencepools.inference.networking.x-k8s.io',
       'inferencepools.inference.networking.k8s.io'}
EXPECTED = {
    ('ServiceAccount', 'llmisvc-controller-manager'),
    ('ClusterRole', 'kserve-llmisvc-manager-role'),
    ('ClusterRoleBinding', 'llmisvc-manager-rolebinding'),
    ('Role', 'llmisvc-leader-election-role'),
    ('RoleBinding', 'llmisvc-leader-election-rolebinding'),
    ('Service', 'llmisvc-controller-manager-service'),
    ('Service', 'llmisvc-webhook-server-service'),
    ('Deployment', 'llmisvc-controller-manager'),
    ('Certificate', 'llmisvc-serving-cert'),
    ('ValidatingWebhookConfiguration', 'llminferenceservice.serving.kserve.io'),
    ('ValidatingWebhookConfiguration', 'llminferenceserviceconfig.serving.kserve.io'),
}
objects = [o for o in yaml.safe_load_all(sys.stdin) if o]
kept = []
removed = set()
for obj in objects:
    key = (obj['kind'], obj['metadata']['name'])
    if key[0] == 'CustomResourceDefinition' and key[1] in GIE:
        removed.add(key[1])
        continue
    if key not in EXPECTED:
        raise ValueError('unexpected object in official LLMI resources Chart: ' + '/'.join(key))
    obj['metadata'].setdefault('labels', {})['ani.io/managed-by'] = '{{ .kubernetes.cluster_name }}'
    kept.append(obj)
if removed != GIE or { (o['kind'], o['metadata']['name']) for o in kept } != EXPECTED or len(kept) != len(EXPECTED):
    raise ValueError('official LLMI resource set differs from source approval')
yaml.safe_dump_all(kept, sys.stdout, sort_keys=False)

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
    if obj['kind'] == 'Certificate':
        if obj['spec']['issuerRef'] != {'kind': 'Issuer', 'name': 'selfsigned-issuer'}:
            raise ValueError('official LLMI webhook issuer differs from the approved Chart')
        # Standard already uses this site's installed CA. Reuse its issuer
        # identity without creating an excluded Issuer or changing the CA.
        obj['spec']['issuerRef'] = {'kind': 'ClusterIssuer', 'name': 'ani-ca'}
    if obj['kind'] == 'ClusterRole':
        read_ca = {'apiGroups': [''], 'resources': ['configmaps', 'pods'], 'verbs': ['get', 'list', 'watch']}
        if obj['rules'].count(read_ca) != 1:
            raise ValueError('official LLMI ConfigMap read rule differs from the approved Chart')
        # v0.17.0 calls the shared CA-bundle reconciler, whose Create/Update
        # operations are absent from its official LLMI role. Keep Pod access
        # read-only and limit updates to the controller's fixed bundle name.
        obj['rules'] += [
            {'apiGroups': [''], 'resources': ['configmaps'], 'verbs': ['create']},
            {'apiGroups': [''], 'resources': ['configmaps'],
             'resourceNames': ['global-ca-bundle'], 'verbs': ['update']},
        ]
    obj['metadata'].setdefault('labels', {})['ani.io/managed-by'] = '{{ .kubernetes.cluster_name }}'
    kept.append(obj)
if removed != GIE or { (o['kind'], o['metadata']['name']) for o in kept } != EXPECTED or len(kept) != len(EXPECTED):
    raise ValueError('official LLMI resource set differs from source approval')
yaml.safe_dump_all(kept, sys.stdout, sort_keys=False)

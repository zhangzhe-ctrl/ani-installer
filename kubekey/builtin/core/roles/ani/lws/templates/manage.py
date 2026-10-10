#!/usr/bin/env python3
"""Official LWS install/check and isolated external-engine-boundary probe."""
import argparse
import base64
import hashlib
import json
import os
import pathlib
import subprocess
import sys
import time
import uuid

import yaml

OWNER = '{{ .kubernetes.cluster_name }}'
IMAGE = '{{ (index .ani.components "lws").image_ref }}'
CHART = pathlib.Path('{{ .ani.artifact_root }}/{{ (index .ani.components "lws").chart_path }}')
CHART_SHA = '{{ (index .ani.components "lws").chart_sha256 }}'
HELM = '{{ .ani.artifact_root }}/bin/helm'
PROBE_IMAGE = '{{ index .ani.images "docker.io/library/busybox:1.37.0" }}@sha256:7a3ebe5bfd1a4a19797d20b0c0bb39d44393e9a03fd852c0865b0f540d868df0'
NODES = [{{ range .ani.nodes }}'{{ . }}',{{ end }}]
NS = 'lws-system'


def command(args, payload=None):
    result = subprocess.run(args, input=payload, text=True, capture_output=True, timeout=660)
    if result.returncode:
        sys.stderr.write(result.stderr)
        raise subprocess.CalledProcessError(result.returncode, args, result.stdout, result.stderr)
    return result.stdout


def kube(*args, payload=None):
    return command(['kubectl', '--kubeconfig', os.environ['ANI_RUN_KUBECONFIG'], *args], payload)


def get(kind, name=None, namespace=None):
    args = ['get', kind]
    if name:
        args.append(name)
    if namespace:
        args += ['-n', namespace]
    text = kube(*args, '--ignore-not-found', '-o', 'json')
    return json.loads(text) if text.strip() else None


def apply(objects):
    kube('apply', '--server-side', '--field-manager=ani-lws', '-f', '-', payload=json.dumps(
        {'apiVersion': 'v1', 'kind': 'List', 'items': objects}))


def wait(description, observe, timeout=300):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = observe()
        if value:
            return value
        time.sleep(2)
    raise RuntimeError('timeout waiting for ' + description)


def install():
    if hashlib.sha256(CHART.read_bytes()).hexdigest() != CHART_SHA:
        raise ValueError('LWS chart differs from embedded approval')
    objects = [obj for obj in yaml.safe_load_all(command([
        HELM, 'template', 'ani-lws', str(CHART), '-n', NS, '--include-crds',
        '--kube-version', '1.35.8', '-f', '/etc/kubernetes/ani/lws/values.yaml'])) if obj]
    deployments = [obj for obj in objects if obj['kind'] == 'Deployment']
    if len(deployments) != 1 or [c['image'] for c in deployments[0]['spec']['template']['spec']['containers']] != [IMAGE]:
        raise ValueError('upstream LWS controller/image selection changed')
    crds = [obj for obj in objects if obj['kind'] == 'CustomResourceDefinition']
    lws = [obj for obj in crds if obj['metadata']['name'] == 'leaderworkersets.leaderworkerset.x-k8s.io']
    if len(lws) != 1 or not any(v['name'] == 'v1' and v['served'] for v in lws[0]['spec']['versions']):
        raise ValueError('LWS official chart does not serve the required v1 API')
    releases = get('secret', namespace=NS) or {}
    if any(o['metadata'].get('labels', {}).get('name') == 'ani-lws' and
           o['metadata'].get('labels', {}).get('owner') == 'helm' for o in releases.get('items', [])):
        raise ValueError('LWS release already exists; use formal plan/execute for noop')
    # Inspect the complete scope before the first write; shared/foreign APIs
    # and controllers are never adopted, replaced or uninstalled.
    global_kinds = {'CustomResourceDefinition', 'ClusterRole', 'ClusterRoleBinding',
                    'ValidatingWebhookConfiguration', 'MutatingWebhookConfiguration'}
    for obj in objects:
        namespace = None if obj['kind'] in global_kinds else NS
        if get(obj['kind'], obj['metadata']['name'], namespace):
            raise ValueError('pre-existing LWS object: ' + obj['kind'] + '/' + obj['metadata']['name'])
    namespace = get('namespace', NS)
    if namespace and namespace['metadata'].get('labels', {}).get('ani.io/managed-by') != OWNER:
        raise ValueError('foreign lws-system namespace; refusing takeover')
    if not namespace:
        apply([{'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {
            'name': NS, 'labels': {'ani.io/managed-by': OWNER}}}])
    for obj in crds:
        obj['metadata'].setdefault('labels', {})['ani.io/managed-by'] = OWNER
    apply(crds)
    for obj in crds:
        kube('wait', '--for=condition=Established', 'crd/' + obj['metadata']['name'], '--timeout=180s')
    # The official internal manager fills the empty TLS Secret and webhook CA.
    # Helm waits for its real readiness probe before any LWS workload is sent.
    command([HELM, '--kubeconfig', os.environ['ANI_RUN_KUBECONFIG'], 'install', 'ani-lws', str(CHART),
             '-n', NS, '--skip-crds', '-f', '/etc/kubernetes/ani/lws/values.yaml',
             '--labels', 'ani.io/managed-by=' + OWNER, '--wait', '--timeout', '600s'])
    return check()


def check():
    crd = get('crd', 'leaderworkersets.leaderworkerset.x-k8s.io')
    if not crd or crd['metadata'].get('labels', {}).get('ani.io/managed-by') != OWNER:
        raise ValueError('LWS CRD ownership differs')
    if not any(v['name'] == 'v1' and v['served'] for v in crd['spec']['versions']):
        raise ValueError('LWS v1 API unavailable')
    kube('rollout', 'status', 'deployment/lws-controller-manager', '-n', NS, '--timeout=300s')
    manager = get('deployment', 'lws-controller-manager', NS)
    if [c['image'] for c in manager['spec']['template']['spec']['containers']] != [IMAGE]:
        raise ValueError('LWS controller image differs from approval')
    secret = wait('LWS internal webhook certificate', lambda: get('secret', 'lws-webhook-server-cert', NS))
    data = secret.get('data', {})
    if not data.get('tls.crt') or not data.get('tls.key'):
        raise ValueError('LWS internal certificate/key incomplete')
    command(['openssl', 'x509', '-noout', '-checkend', '86400'], base64.b64decode(data['tls.crt']).decode())
    for kind in ('validatingwebhookconfiguration', 'mutatingwebhookconfiguration'):
        objects = get(kind)['items']
        webhooks = [w for obj in objects for w in obj.get('webhooks', [])
                    if w.get('clientConfig', {}).get('service', {}).get('namespace') == NS]
        if not webhooks or any(not w['clientConfig'].get('caBundle') for w in webhooks):
            raise ValueError('LWS webhook CA not ready')
    pods = kube('get', 'pods', '-n', NS, '-l', 'app.kubernetes.io/instance=ani-lws', '-o', 'json')
    image_ids = [c.get('imageID', '') for pod in json.loads(pods)['items']
                 for c in pod['status'].get('containerStatuses', [])]
    if not image_ids or any(IMAGE.split('@')[1] not in value for value in image_ids):
        raise ValueError('LWS actual imageID differs from the platform manifest')
    return {'api': 'leaderworkerset.x-k8s.io/v1', 'imageIDs': image_ids, 'webhookCertificate': 'ready', 'status': 'pass'}


def probe():
    result = check()
    name = 'ani-lws-probe-' + uuid.uuid4().hex[:10]
    labels = {'ani.io/lws-probe': name}
    apply([{'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {
        'name': name, 'labels': {'ani.io/managed-by': OWNER, **labels}}}])
    marker = 'LWS_EXTERNAL_ENGINE_BOUNDARY:' + name

    def template(role, node):
        return {'metadata': {'labels': {**labels, 'ani.io/probe-role': role}}, 'spec': {
            'automountServiceAccountToken': False, 'nodeSelector': {'kubernetes.io/hostname': node},
            'containers': [{'name': 'engine-boundary', 'image': PROBE_IMAGE, 'command': ['sh', '-ec',
                'mkdir -p /www; printf %s ' + marker + ':' + role + ' > /www/index.html; exec httpd -f -p 8080 -h /www'],
                'ports': [{'containerPort': 8080}], 'resources': {'requests': {'cpu': '10m', 'memory': '16Mi'},
                'limits': {'cpu': '100m', 'memory': '64Mi'}},
                'readinessProbe': {'httpGet': {'path': '/', 'port': 8080}, 'periodSeconds': 2}}]}}

    workload = {'apiVersion': 'leaderworkerset.x-k8s.io/v1', 'kind': 'LeaderWorkerSet',
                'metadata': {'name': 'group', 'namespace': name, 'labels': labels}, 'spec': {'replicas': 1,
                'leaderWorkerTemplate': {'size': 2, 'restartPolicy': 'RecreateGroupOnPodRestart',
                'leaderTemplate': template('leader', NODES[0]), 'workerTemplate': template('worker', NODES[1])}}}
    service = {'apiVersion': 'v1', 'kind': 'Service', 'metadata': {'name': 'engine-boundary', 'namespace': name},
               'spec': {'selector': {**labels, 'ani.io/probe-role': 'leader'},
                        'ports': [{'port': 8080, 'targetPort': 8080}]}}
    apply([workload, service])
    print(json.dumps({'probeNamespace': name, 'scope': 'external-inference-engine-boundary', 'stage': 'created'}), flush=True)
    kube('wait', '--for=condition=Available', 'leaderworkerset/group', '-n', name, '--timeout=300s')
    kube('wait', '--for=condition=Ready', 'pod', '--all', '-n', name, '--timeout=300s')
    group = get('leaderworkerset', 'group', name)
    pods = get('pods', namespace=name)['items']
    if len(pods) != 2 or {p['spec']['nodeName'] for p in pods} != set(NODES[:2]):
        raise ValueError('LWS did not schedule the real two-node leader/worker group')
    controllers = get('statefulsets', namespace=name)['items']
    # LWS owns the leader StatefulSet; the leader Pod owns its worker
    # StatefulSet. Traverse actual controller UIDs through both kinds.
    descendants = {o['metadata']['uid']: o for o in controllers + pods}
    def owned_by_group(obj):
        seen = set()
        while obj['metadata']['uid'] not in seen:
            seen.add(obj['metadata']['uid'])
            owners = [o for o in obj['metadata'].get('ownerReferences', []) if o.get('controller')]
            if len(owners) != 1:
                return False
            owner = owners[0]
            if owner['uid'] == group['metadata']['uid']:
                return owner['kind'] == 'LeaderWorkerSet' and owner['name'] == 'group'
            obj = descendants.get(owner['uid'])
            if not obj or owner['kind'] != obj['kind'] or owner['name'] != obj['metadata']['name']:
                return False
        return False
    if not controllers or not all(owned_by_group(o) for o in controllers + pods):
        raise ValueError('LWS StatefulSet/leader/worker UID ownership chain is broken')
    worker = next(p for p in pods if p['metadata']['labels']['ani.io/probe-role'] == 'worker')
    response = kube('exec', '-n', name, worker['metadata']['name'], '--', 'wget', '-qO-', 'http://engine-boundary:8080/')
    if response != marker + ':leader':
        raise ValueError('cross-node LWS HTTP content differs')
    endpoints = get('endpointslices', namespace=name)['items']
    if not any(e.get('conditions', {}).get('ready') for s in endpoints for e in s.get('endpoints', [])):
        raise ValueError('LWS probe Service has no ready EndpointSlice')
    result.update({'probeNamespace': name, 'groupUID': group['metadata']['uid'],
                   'podUIDs': [p['metadata']['uid'] for p in pods], 'nodes': NODES[:2],
                   'httpContent': response, 'scope': 'external-inference-engine-boundary'})
    # Only successful probes are cleaned. A failure retains its first error
    # and objects; no finalizer is removed and no shared resource is deleted.
    if get('leaderworkerset', 'group', name)['metadata']['uid'] != group['metadata']['uid']:
        raise ValueError('LWS probe UID changed before cleanup')
    kube('delete', 'leaderworkerset', 'group', '-n', name, '--wait=true', '--timeout=600s')
    wait('normal LWS Pod/StatefulSet finalization', lambda: not get('pods', namespace=name)['items'] and
         not get('statefulsets', namespace=name)['items'], timeout=600)
    kube('delete', 'namespace', name, '--wait=true', '--timeout=600s')
    result['cleanup'] = 'normal-finalization-pass'
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', choices=('install', 'check', 'probe'))
    parser.add_argument('--output-dir', type=pathlib.Path)
    args = parser.parse_args()
    result = {'install': install, 'check': check, 'probe': probe}[args.mode]()
    if args.output_dir:
        args.output_dir.mkdir(parents=True, exist_ok=True)
        (args.output_dir / ('lws-' + args.mode + '.json')).write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result))


if __name__ == '__main__':
    try:
        main()
    except subprocess.CalledProcessError as error:
        sys.exit(error.returncode)

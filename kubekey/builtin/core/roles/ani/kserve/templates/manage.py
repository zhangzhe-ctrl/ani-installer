#!/usr/bin/env python3
"""Official LLMI controller plus a clearly marked external-engine probe."""
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
ROOT = pathlib.Path('{{ .ani.artifact_root }}')
HELM = str(ROOT / 'bin/helm')
NS = 'kserve'
RELEASE = 'ani-kserve-llmi'
IMAGE = '{{ (index .ani.components "kserve").image_ref }}'
STANDARD_IMAGE = '{{ index .ani.kubeflow.images "docker.io/kserve/kserve-controller:v0.16.0" }}'
ENGINE_IMAGE = '{{ index .ani.kubeflow.images "ani.local/kubeflow-execution:26.03-v1" }}'
INIT_IMAGE = '{{ index .ani.images "docker.io/library/busybox:1.37.0" }}@sha256:7a3ebe5bfd1a4a19797d20b0c0bb39d44393e9a03fd852c0865b0f540d868df0'
NODES = [{{ range .ani.nodes }}'{{ . }}',{{ end }}]
CLASS = '{{ .ani.kubeflow.workspace_class }}'
CRD_NAMES = {'llminferenceservices.serving.kserve.io', 'llminferenceserviceconfigs.serving.kserve.io'}
CHARTS = (
    ('charts/kserve-llmisvc-crd/v0.17.0.tgz', 'e49aee5322397d4dacd9ee9eadf98b797e8f726e1e8c21acadc2854d9e27aa81'),
    ('charts/kserve-llmisvc-resources/v0.17.0.tgz', '456fd2cc244c344a262ca7d553092e6abb3e8cb2c3b87f12653cf46ad3fbffba'),
)
PRESETS = ['kserve-config-llm-template', 'kserve-config-llm-worker-pipeline-parallel',
           'kserve-config-llm-worker-data-parallel']
GLOBAL = {'CustomResourceDefinition', 'ClusterRole', 'ClusterRoleBinding',
          'ValidatingWebhookConfiguration', 'MutatingWebhookConfiguration'}


def command(args, payload=None):
    r = subprocess.run(args, input=payload, text=True, capture_output=True, timeout=660)
    if r.returncode:
        sys.stderr.write(r.stderr)
        raise subprocess.CalledProcessError(r.returncode, args, r.stdout, r.stderr)
    return r.stdout


def kube(*args, payload=None):
    return command(['kubectl', '--kubeconfig', os.environ['ANI_RUN_KUBECONFIG'], *args], payload)


def get(kind, name=None, namespace=None):
    args = ['get', kind]
    if name: args.append(name)
    if namespace: args += ['-n', namespace]
    raw = kube(*args, '--ignore-not-found', '-o', 'json')
    return json.loads(raw) if raw.strip() else (None if name else {'items': []})


def listing(kind, namespace):
    return (get(kind, namespace=namespace) or {}).get('items', [])


def apply(objects):
    kube('apply', '--server-side', '--field-manager=ani-kserve-llmi', '-f', '-',
         payload=json.dumps({'apiVersion': 'v1', 'kind': 'List', 'items': objects}))


def wait(description, observe, timeout=300):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = observe()
        if value: return value
        time.sleep(2)
    raise RuntimeError('timeout waiting for ' + description)


def standard():
    values = [get('namespace', NS), get('deployment', 'kserve-controller-manager', NS),
              get('configmap', 'inferenceservice-config', NS), get('issuer', 'selfsigned-issuer', NS),
              get('crd', 'inferenceservices.serving.kserve.io')]
    if not all(values) or values[0]['metadata'].get('labels', {}).get('ani.io/managed-by') != OWNER:
        raise ValueError('the selected Standard base/namespace is absent or foreign')
    manager = [c for c in values[1]['spec']['template']['spec']['containers'] if c['name'] == 'manager']
    if len(manager) != 1 or manager[0]['image'] != STANDARD_IMAGE:
        raise ValueError('Standard controller differs from the preserved base approval')
    if json.loads(values[2]['data']['deploy']).get('defaultDeploymentMode') != 'Standard':
        raise ValueError('Standard default deployment mode differs')
    if not any(c['type'] == 'Ready' and c['status'] == 'True' for c in values[3].get('status', {}).get('conditions', [])):
        raise ValueError('existing Standard selfsigned-issuer is not ready')
    protected = [{'uid': v['metadata']['uid'], 'labels': v['metadata'].get('labels', {}),
                  'spec': v.get('spec'), 'data': v.get('data')} for v in values]
    return hashlib.sha256(json.dumps(protected, sort_keys=True).encode()).hexdigest()


def preset(name):
    return {'apiVersion': 'serving.kserve.io/v1alpha2', 'kind': 'LLMInferenceServiceConfig',
            'metadata': {'name': name, 'namespace': NS, 'labels': {'ani.io/managed-by': OWNER}}, 'spec': {}}


def install():
    before = standard()
    lws = get('crd', 'leaderworkersets.leaderworkerset.x-k8s.io')
    if not lws or lws['metadata'].get('labels', {}).get('ani.io/managed-by') != OWNER:
        raise ValueError('owned LWS prerequisite is absent')
    kube('rollout', 'status', 'deployment/lws-controller-manager', '-n', 'lws-system', '--timeout=300s')
    for path, digest in CHARTS:
        if hashlib.sha256((ROOT / path).read_bytes()).hexdigest() != digest:
            raise ValueError('LLMI Chart differs from source approval: ' + path)
    raw = command([HELM, 'template', RELEASE, str(ROOT / CHARTS[0][0]), '-n', NS])
    crds = [o for o in yaml.safe_load_all(raw) if o and o['metadata']['name'] in CRD_NAMES]
    if len(crds) != 2 or {o['metadata']['name'] for o in crds} != CRD_NAMES:
        raise ValueError('official LLMI CRD Chart does not carry exactly the selected new APIs')
    os.chmod('/etc/kubernetes/ani/kserve/filter.py', 0o700)
    args = [HELM, 'template', RELEASE, str(ROOT / CHARTS[1][0]), '-n', NS,
            '-f', '/etc/kubernetes/ani/kserve/values.yaml', '--post-renderer', '/etc/kubernetes/ani/kserve/filter.py']
    objects = [o for o in yaml.safe_load_all(command(args)) if o]
    managers = [o for o in objects if o['kind'] == 'Deployment']
    if len(managers) != 1 or [c['image'] for c in managers[0]['spec']['template']['spec']['containers']] != [IMAGE]:
        raise ValueError('official LLMI controller/image selection changed')
    if any(o['metadata'].get('labels', {}).get('owner') == 'helm' and
           o['metadata'].get('labels', {}).get('name') == RELEASE for o in listing('secrets', NS)):
        raise ValueError('LLMI release already exists; use formal plan/execute for noop')
    # The complete mutation scope is inspected before the first API write.
    for obj in crds + objects:
        namespace = None if obj['kind'] in GLOBAL else NS
        if get(obj['kind'], obj['metadata']['name'], namespace):
            raise ValueError('pre-existing LLMI object: ' + obj['kind'] + '/' + obj['metadata']['name'])
    # Absent config CRD proves no presets can pre-exist. Querying that new
    # resource kind before Established would itself fail on a clean cluster.
    for obj in crds:
        obj['metadata'].setdefault('labels', {})['ani.io/managed-by'] = OWNER
    kube('create', '-f', '-', payload=json.dumps({'apiVersion': 'v1', 'kind': 'List', 'items': crds}))
    for obj in crds:
        kube('wait', '--for=condition=Established', 'crd/' + obj['metadata']['name'], '--timeout=180s')
    command([HELM, '--kubeconfig', os.environ['ANI_RUN_KUBECONFIG'], 'install', RELEASE,
             str(ROOT / CHARTS[1][0]), '-n', NS, '-f', '/etc/kubernetes/ani/kserve/values.yaml',
             '--post-renderer', '/etc/kubernetes/ani/kserve/filter.py', '--labels', 'ani.io/managed-by=' + OWNER,
             '--wait', '--timeout', '600s'])
    admission()
    # Empty official configs supply no engine argv, PodSpec or router defaults.
    kube('create', '-f', '-', payload=json.dumps({'apiVersion': 'v1', 'kind': 'List', 'items': [preset(name) for name in PRESETS]}))
    if standard() != before:
        raise ValueError('LLMI installation changed protected Standard objects')
    result = check()
    result['standardProtection'] = before
    return result


def admission():
    kube('rollout', 'status', 'deployment/llmisvc-controller-manager', '-n', NS, '--timeout=300s')
    kube('wait', '--for=condition=Ready', 'certificate/llmisvc-serving-cert', '-n', NS, '--timeout=300s')
    secret = get('secret', 'llmisvc-webhook-server-cert', NS)
    if not secret or not secret.get('data', {}).get('tls.crt') or not secret['data'].get('tls.key'):
        raise ValueError('official LLMI webhook certificate/key is incomplete')
    command(['openssl', 'x509', '-noout', '-checkend', '86400'], base64.b64decode(secret['data']['tls.crt']).decode())
    def ready():
        for name in CRD_NAMES:
            crd = get('crd', name)
            if not crd or crd['metadata'].get('labels', {}).get('ani.io/managed-by') != OWNER: return False
            conversion = crd['spec'].get('conversion', {})
            if conversion.get('strategy') == 'Webhook':
                config = conversion['webhook']['clientConfig']
                if not config.get('caBundle') or config['service']['name'] != 'llmisvc-webhook-server-service' or config['service']['namespace'] != NS: return False
        for name in ('llminferenceservice.serving.kserve.io', 'llminferenceserviceconfig.serving.kserve.io'):
            obj = get('validatingwebhookconfiguration', name)
            if not obj or not obj.get('webhooks'): return False
            if any(not w['clientConfig'].get('caBundle') or w['clientConfig']['service']['name'] != 'llmisvc-webhook-server-service' or w['clientConfig']['service']['namespace'] != NS for w in obj['webhooks']): return False
        return True
    wait('LLMI validation/conversion webhook CA', ready)


def check():
    protected = standard()
    admission()
    crd = get('crd', 'llminferenceservices.serving.kserve.io')
    if not any(v['name'] == 'v1alpha1' and v['served'] for v in crd['spec']['versions']):
        raise ValueError('required ANI LLMInferenceService v1alpha1 API is not served')
    manager = get('deployment', 'llmisvc-controller-manager', NS)
    if [c['image'] for c in manager['spec']['template']['spec']['containers']] != [IMAGE]:
        raise ValueError('LLMI controller image differs from approval')
    selector = ','.join(k + '=' + v for k, v in manager['spec']['selector']['matchLabels'].items())
    pods = json.loads(kube('get', 'pods', '-n', NS, '-l', selector, '-o', 'json'))['items']
    ids = [c.get('imageID', '') for p in pods for c in p['status'].get('containerStatuses', [])]
    if len(ids) != 1 or IMAGE.rsplit('@', 1)[1] not in ids[0]:
        raise ValueError('LLMI actual imageID is not the approved amd64 manifest')
    lease = get('lease', 'llminferenceservice-kserve-controller-manager', NS)
    if not lease or not lease['spec'].get('holderIdentity'):
        raise ValueError('LLMI dedicated controller Lease is not active')
    for name in PRESETS:
        config = get('llminferenceserviceconfig', name, NS)
        if not config or config['metadata'].get('labels', {}).get('ani.io/managed-by') != OWNER or config.get('spec', {}) != {}:
            raise ValueError('LLMI caller PodSpec preserving preset differs: ' + name)
    return {'status': 'pass', 'api': 'serving.kserve.io/v1alpha1', 'imageIDs': ids,
            'admission': 'validation-and-conversion-ca-ready', 'standardProtection': protected,
            'routing': 'caller-owned-HTTPRoute', 'engines': 'external-boundary'}


def probe():
    result = check()
    name = 'ani-llmi-probe-' + uuid.uuid4().hex[:10]
    labels = {'ani.io/llmi-probe': name}
    apply([{'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': name,
            'labels': {'ani.io/managed-by': OWNER, **labels}}},
           {'apiVersion': 'v1', 'kind': 'PersistentVolumeClaim', 'metadata': {'name': 'model', 'namespace': name},
            'spec': {'storageClassName': CLASS, 'accessModes': ['ReadWriteMany'], 'resources': {'requests': {'storage': '1Gi'}}}}])
    print(json.dumps({'probeNamespace': name, 'scope': 'external-inference-engine-boundary', 'stage': 'created'}), flush=True)
    marker = 'LLMI_EXTERNAL_ENGINE_BOUNDARY:' + name
    apply([{'apiVersion': 'batch/v1', 'kind': 'Job', 'metadata': {'name': 'material-boundary', 'namespace': name},
            'spec': {'backoffLimit': 0, 'template': {'spec': {'restartPolicy': 'Never',
                'automountServiceAccountToken': False, 'securityContext': {'runAsUser': 1000, 'runAsGroup': 1000, 'fsGroup': 1000},
                'volumes': [{'name': 'model', 'persistentVolumeClaim': {'claimName': 'model'}}],
                'containers': [{'name': 'material-boundary', 'image': INIT_IMAGE,
                    'command': ['sh', '-ec', 'mkdir -p /models/data; printf %s ' + marker + ':material > /models/data/input.txt; sync'],
                    'volumeMounts': [{'name': 'model', 'mountPath': '/models'}],
                    'resources': {'requests': {'cpu': '10m', 'memory': '16Mi'}, 'limits': {'cpu': '100m', 'memory': '64Mi'}}}]}}}}])
    kube('wait', '--for=condition=Complete', 'job/material-boundary', '-n', name, '--timeout=300s')
    material_pods = listing('pods', name)
    if len(material_pods) != 1 or not any(INIT_IMAGE.rsplit('@', 1)[1] in c.get('imageID', '') for c in material_pods[0]['status'].get('containerStatuses', [])):
        raise ValueError('material boundary actual imageID differs from approval')
    claim_uid = get('pvc', 'model', name)['metadata']['uid']
    kube('delete', 'job', 'material-boundary', '-n', name, '--cascade=foreground', '--wait=true', '--timeout=180s')
    wait('normal material boundary Job Pod finalization', lambda: not listing('pods', name))
    server = '''import http.server,json,os,pathlib
role=os.environ['PROBE_ROLE']; marker=os.environ['PROBE_MARKER']
root=pathlib.Path('/models/data'); root.mkdir(exist_ok=True)
(root/(role+'.txt')).write_text(marker+':'+role)
class Handler(http.server.BaseHTTPRequestHandler):
 def do_GET(self):
  body=(marker+':'+role).encode(); self.send_response(200); self.end_headers(); self.wfile.write(body)
 def do_POST(self):
  body=self.rfile.read(int(self.headers.get('Content-Length','0')))
  result=json.dumps({'scope':'external-inference-engine-boundary','probe':marker,'role':role,'request':json.loads(body)}).encode()
  self.send_response(200); self.end_headers(); self.wfile.write(result)
 def log_message(self,*args): pass
http.server.HTTPServer(('0.0.0.0',8000),Handler).serve_forever()
'''
    def template(role, node):
        return {
            'automountServiceAccountToken': False, 'nodeSelector': {'kubernetes.io/hostname': node},
            'securityContext': {'runAsUser': 1000, 'runAsGroup': 1000, 'fsGroup': 1000},
            'volumes': [{'name': 'caller-model', 'persistentVolumeClaim': {'claimName': 'model'}}],
            'containers': [{'name': 'main', 'image': ENGINE_IMAGE, 'command': ['python3', '-u', '-c', server], 'args': [],
                'env': [{'name': 'PROBE_ROLE', 'value': role}, {'name': 'PROBE_MARKER', 'value': marker}],
                'ports': [{'name': 'http', 'containerPort': 8000}],
                'volumeMounts': [{'name': 'caller-model', 'mountPath': '/models'}],
                'resources': {'requests': {'cpu': '20m', 'memory': '64Mi'}, 'limits': {'cpu': '200m', 'memory': '192Mi'}},
                'readinessProbe': {'httpGet': {'path': '/', 'port': 8000}, 'periodSeconds': 2}}]}
    workload = {'apiVersion': 'serving.kserve.io/v1alpha1', 'kind': 'LLMInferenceService',
                'metadata': {'name': 'engine-boundary', 'namespace': name, 'labels': labels}, 'spec': {
                    'replicas': 1, 'model': {'uri': 'pvc://model/data', 'name': 'external-boundary'},
                    'parallelism': {'pipeline': 2}, 'template': template('leader', NODES[0]),
                    'worker': template('worker', NODES[1])}}
    apply([workload])
    kube('wait', '--for=condition=Ready', 'llminferenceservice/engine-boundary', '-n', name, '--timeout=300s')
    def observe():
        pods = listing('pods', name)
        if len(pods) != 2 or any(not any(c['type'] == 'Ready' and c['status'] == 'True' for c in p.get('status', {}).get('conditions', [])) for p in pods): return None
        return pods
    pods = wait('actual LLMI leader/worker readiness', observe)
    llm = get('llminferenceservice', 'engine-boundary', name)
    groups = listing('leaderworkersets', name)
    if len(groups) != 1 or not any(o.get('controller') and o['kind'] == 'LLMInferenceService' and o['uid'] == llm['metadata']['uid'] for o in groups[0]['metadata'].get('ownerReferences', [])):
        raise ValueError('LLMI did not produce an LWS with its actual UID as controller owner')
    if {p['spec']['nodeName'] for p in pods} != set(NODES[:2]):
        raise ValueError('LLMI leader/worker did not schedule across the declared nodes')
    objects = groups + listing('statefulsets', name) + pods
    ancestors = {o['metadata']['uid']: o for o in objects}
    for pod in pods:
        obj, seen = pod, set()
        while obj['metadata']['uid'] != llm['metadata']['uid']:
            uid = obj['metadata']['uid']
            if uid in seen: raise ValueError('cycle in LLMI actual UID ownership chain')
            seen.add(uid)
            owners = [o for o in obj['metadata'].get('ownerReferences', []) if o.get('controller')]
            if len(owners) != 1: raise ValueError('missing LLMI controller ownership')
            owner = owners[0]
            obj = llm if owner['uid'] == llm['metadata']['uid'] else ancestors.get(owner['uid'])
            if not obj or owner['kind'] != obj['kind'] or owner['name'] != obj['metadata']['name']:
                raise ValueError('broken LLMI LWS StatefulSet Pod UID chain')
        main = next(c for c in pod['spec']['containers'] if c['name'] == 'main')
        if main['command'] != ['python3', '-u', '-c', server] or main.get('args', []) or main['image'] != ENGINE_IMAGE:
            raise ValueError('LLMI changed the caller engine command/image')
        statuses = [c for c in pod['status'].get('containerStatuses', []) if c['name'] == 'main']
        if len(statuses) != 1 or ENGINE_IMAGE.rsplit('@', 1)[1] not in statuses[0].get('imageID', ''):
            raise ValueError('external boundary actual imageID differs from approval')
        claim = next(v for v in pod['spec']['volumes'] if v['name'] == 'caller-model')['persistentVolumeClaim']['claimName']
        if claim != 'model': raise ValueError('LLMI changed the caller PVC binding')
    def pod_role(pod):
        main = next(c for c in pod['spec']['containers'] if c['name'] == 'main')
        return next(e['value'] for e in main['env'] if e['name'] == 'PROBE_ROLE')
    leader = next(p for p in pods if pod_role(p) == 'leader')
    worker = next(p for p in pods if pod_role(p) == 'worker')
    service = {'apiVersion': 'v1', 'kind': 'Service', 'metadata': {'name': 'probe-http', 'namespace': name},
               'spec': {'selector': {'statefulset.kubernetes.io/pod-name': leader['metadata']['name']},
                        'ports': [{'port': 8080, 'targetPort': 8000}]}}
    apply([service])
    def ready_slice():
        return any(e.get('conditions', {}).get('ready') for s in listing('endpointslices', name)
                   if s['metadata']['labels'].get('kubernetes.io/service-name') == 'probe-http' for e in s.get('endpoints', []))
    wait('actual leader Service EndpointSlice', ready_slice)
    code = 'import urllib.request; print(urllib.request.urlopen("http://probe-http:8080/",timeout=5).read().decode(),end="")'
    response = kube('exec', '-n', name, worker['metadata']['name'], '-c', 'main', '--', 'python3', '-c', code)
    if response != marker + ':leader': raise ValueError('LLMI cross-node HTTP content differs')
    if get('pvc', 'model', name)['metadata']['uid'] != claim_uid:
        raise ValueError('caller model PVC UID changed during LLMI reconcile')
    actual = kube('exec', '-n', name, worker['metadata']['name'], '-c', 'main', '--', 'cat', '/models/data/input.txt')
    if actual != marker + ':material': raise ValueError('LLMI lost the materialized model boundary bytes')
    for role in ('leader', 'worker'):
        actual = kube('exec', '-n', name, worker['metadata']['name'], '-c', 'main', '--', 'cat', '/models/data/' + role + '.txt')
        if actual != marker + ':' + role: raise ValueError('LLMI shared PVC bytes differ for ' + role)
    slices = listing('endpointslices', name)
    if not any(e.get('conditions', {}).get('ready') for s in slices if s['metadata']['labels'].get('kubernetes.io/service-name') == 'probe-http' for e in s.get('endpoints', [])):
        raise ValueError('LLMI probe Service lacks a ready EndpointSlice')
    result.update({'probeNamespace': name, 'llmiUID': llm['metadata']['uid'], 'lwsUID': groups[0]['metadata']['uid'],
                   'podUIDs': [p['metadata']['uid'] for p in pods], 'nodes': NODES[:2], 'httpContent': response,
                   'pvcBytes': 'leader-and-worker-exact-match', 'callerCommands': 'preserved'})
    kube('delete', 'llminferenceservice', 'engine-boundary', '-n', name, '--wait=true', '--timeout=600s')
    wait('normal LLMI LWS Pod StatefulSet finalization', lambda: not listing('leaderworkersets', name) and
         not listing('pods', name) and not listing('statefulsets', name), timeout=600)
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
        path = args.output_dir / ('kserve-llmi-' + args.mode + '.json')
        path.write_text(json.dumps(result, indent=2) + '\n')
        path.chmod(0o600)
    print(json.dumps(result))


if __name__ == '__main__':
    try:
        main()
    except subprocess.CalledProcessError as error:
        sys.exit(error.returncode)
    except (ValueError, RuntimeError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)

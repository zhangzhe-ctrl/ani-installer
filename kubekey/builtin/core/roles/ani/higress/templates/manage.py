#!/usr/bin/env python3
"""Independent official Higress; probes replace only external engine backends."""
import argparse
import base64
import hashlib
import json
import os
import pathlib
import re
import secrets
import ssl
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

import yaml

OWNER = '{{ .kubernetes.cluster_name }}'
ROOT = pathlib.Path('{{ .ani.artifact_root }}')
WORK = pathlib.Path('/etc/kubernetes/ani/higress')
NS = 'higress-system'
RELEASE = 'ani-higress'
HELM = str(ROOT / 'bin/helm')
CHART = ROOT / 'charts/higress/2.2.4.tgz'
CHART_SHA = 'c048d97805f7925d74051674e1f639fa178e5b7ce4eabd893d03b02eff653787'
WASM_LOCK_SHA = 'cc4b51b5719a38302792e0f85daca9f09c373d8f9bb32ccbc2887ccc705d3d1d'
MODE = '{{ (index .ani.components "higress").wasm_registry }}'
MATERIAL_REGISTRY = '{{ .ani.registry }}'
HARBOR_REGISTRY = '{{ (index .ani.components "harbor").external_address }}:30003'
REGISTRY = HARBOR_REGISTRY if MODE == 'harbor' else MATERIAL_REGISTRY
GATEWAY_CLASS = '{{ (index .ani.components "higress").gateway_class }}'
GATEWAY = '{{ (index .ani.components "higress").gateway_name }}'
HTTP_PORT = {{ (index .ani.components "higress").http_port }}
HTTPS_PORT = {{ (index .ani.components "higress").https_port }}
HOSTNAME = '{{ (index .ani.components "higress").hostname }}'
TLS_SECRET = '{{ (index .ani.components "higress").tls_secret }}'
ALLOWED = [{{ range (index .ani.components "higress").allowed_namespaces }}'{{ . }}',{{ end }}]
PROBE_NS = '{{ (index .ani.components "higress").probe_namespace }}'
KEY_FILE = {{ (index .ani.components "higress").probe_key_file | toJson }}
NODES = [{{ range .ani.nodes }}'{{ . }}',{{ end }}]
ADDRESSES = [{{ range .ani.node_addresses }}'{{ . }}',{{ end }}]
IMAGES = {'higress-core': '{{ (index .ani.components "higress").controller_image }}',
          'discovery': '{{ (index .ani.components "higress").pilot_image }}',
          'higress-gateway': '{{ (index .ani.components "higress").gateway_image }}'}
BACKEND_IMAGE = '{{ index .ani.images "docker.io/library/busybox:1.37.0" }}@sha256:7a3ebe5bfd1a4a19797d20b0c0bb39d44393e9a03fd852c0865b0f540d868df0'
API_VERSIONS = {'gatewayclasses.gateway.networking.k8s.io': 'v1',
                'gateways.gateway.networking.k8s.io': 'v1',
                'httproutes.gateway.networking.k8s.io': 'v1',
                'referencegrants.gateway.networking.k8s.io': 'v1beta1'}


def run(args, data=None, env=None):
    p = subprocess.run(args, input=data, capture_output=True, text=True, env=env)
    if p.returncode:
        # Admission/registry errors can echo a submitted key. Keep only the
        # first exit code and command identity; never dump payload or stderr.
        print(json.dumps({'failedCommand': args[0], 'exitCode': p.returncode}), file=sys.stderr)
        raise SystemExit(p.returncode)
    return p.stdout


def kube(*args, data=None):
    return run(['kubectl', '--kubeconfig', os.environ['ANI_RUN_KUBECONFIG'], *args], data)


def get(kind, name=None, ns=None):
    args = ['get', kind]
    if name: args.append(name)
    if ns: args += ['-n', ns]
    raw = kube(*args, '--ignore-not-found', '-o', 'json')
    return json.loads(raw) if raw.strip() else (None if name else {'items': []})


def apply(objects):
    kube('apply', '-f', '-', data='\n---\n'.join(json.dumps(o) for o in objects))


def meta(name, ns=None):
    out = {'name': name, 'labels': {'ani.io/managed-by': OWNER}}
    if ns: out['namespace'] = ns
    return out


def owned(obj):
    return obj and obj['metadata'].get('labels', {}).get('ani.io/managed-by') == OWNER


def wait(label, check, seconds=300):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        result = check()
        if result: return result
        time.sleep(2)
    raise ValueError('timeout: ' + label)


def digest(data):
    return 'sha256:' + hashlib.sha256(data).hexdigest()


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def http_open(request, timeout=30, context=None):
    # Site endpoints are explicit. Ambient proxies and redirects must never
    # carry a probe API key or registry credential to another authority.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}),
                                        urllib.request.HTTPSHandler(context=context), NoRedirect())
    return opener.open(request, timeout=timeout)


def protected(path):
    path = pathlib.Path(path)
    st = path.lstat()
    if not path.is_file() or path.is_symlink() or st.st_mode & 0o077:
        raise ValueError('credential must be a regular private file')
    value = path.read_text().strip()
    if not value or len(value) > 4096 or any(ord(c) < 32 or ord(c) == 127 for c in value): raise ValueError('invalid protected credential input')
    return value


def materials():
    if digest(CHART.read_bytes()) != 'sha256:' + CHART_SHA:
        raise ValueError('official Higress Chart differs from source approval')
    raw = (ROOT / 'wasm/wasm.lock.json').read_bytes()
    if digest(raw) != 'sha256:' + WASM_LOCK_SHA:
        raise ValueError('OCI Wasm lock differs from embedded source approval')
    lock = json.loads(raw)
    if lock['kind'] != 'OCIWasmMaterialLock' or len(lock['plugins']) != 4:
        raise ValueError('invalid approved OCI Wasm selection')
    for p in lock['plugins']:
        store = ROOT / p['artifactPath']
        for d in [p['manifest'], p['config'], *p['layers']]:
            b = (store / 'blobs/sha256' / d['digest'].split(':', 1)[1]).read_bytes()
            if digest(b) != d['digest'] or len(b) != d['size']:
                raise ValueError('OCI Wasm blob hash/size differs: ' + p['name'])
        manifest = json.loads((store / 'blobs/sha256' / p['manifest']['digest'].split(':', 1)[1]).read_bytes())
        if manifest['schemaVersion'] != 2 or manifest['mediaType'] != p['manifest']['mediaType'] or manifest['config'] != p['config'] or manifest['layers'] != p['layers']:
            raise ValueError('OCI Wasm descriptor/type closure differs: ' + p['name'])
        content = next(d for d in p['layers'] if d['mediaType'] == 'application/vnd.module.wasm.content.layer.v1+wasm')
        if content['digest'] != p['wasmDigest'] or (store / 'blobs/sha256' / content['digest'].split(':', 1)[1]).read_bytes()[:4] != b'\x00asm':
            raise ValueError('not the approved Wasm module')
    return lock['plugins']


def shared():
    facts = {}
    for name, version in API_VERSIONS.items():
        obj = get('crd', name)
        if not obj: continue
        if not any(v['name'] == version and v.get('served') for v in obj['spec']['versions']) or not any(c['type'] == 'Established' and c['status'] == 'True' for c in obj.get('status', {}).get('conditions', [])):
            raise ValueError('shared Gateway API is not compatible/Established: ' + name)
        facts[name] = {'uid': obj['metadata']['uid'], 'specHash': digest(json.dumps(obj['spec'], sort_keys=True).encode())}
    # KCN's Envoy objects and certificate identities are never adopted.
    for kind, namespace in [('deployments', 'envoy-gateway-system'), ('secrets', 'cert-manager')]:
        for obj in get(kind, ns=namespace)['items']:
            facts[kind + '/' + namespace + '/' + obj['metadata']['name']] = {
                'uid': obj['metadata']['uid'], 'payloadHash': digest(json.dumps(obj.get('spec', obj.get('data', {})), sort_keys=True).encode())}
    return facts


def publish_material(plugins):
    data_root = pathlib.Path('/var/lib/ani-installer') / OWNER / 'work/registry-data'
    unit = pathlib.Path('/etc/systemd/system/ani-image-registry.service').read_text()
    expected = ' --port ' + MATERIAL_REGISTRY.rsplit(':', 1)[1] + ' --directory ' + str(data_root) + ' --readonly=true --store ' + str(data_root.parent / 'hauler-store')
    if not any(line.startswith('ExecStart=') and line.endswith(expected) for line in unit.splitlines()):
        raise ValueError('material registry unit does not match this installation readonly service')
    run(['systemctl', 'is-active', '--quiet', 'ani-image-registry'])
    writes = {}
    for p in plugins:
        repo = data_root / 'docker/registry/v2/repositories' / p['registryRepository']
        for d in [p['manifest'], p['config'], *p['layers']]:
            sha = d['digest'].split(':', 1)[1]
            content = (ROOT / p['artifactPath'] / 'blobs/sha256' / sha).read_bytes()
            writes[data_root / 'docker/registry/v2/blobs/sha256' / sha[:2] / sha / 'data'] = content
            writes[repo / '_layers/sha256' / sha / 'link'] = d['digest'].encode()
        sha = p['manifest']['digest'].split(':', 1)[1]
        writes[repo / '_manifests/revisions/sha256' / sha / 'link'] = p['manifest']['digest'].encode()
        writes[repo / '_manifests/tags' / p['version'] / 'current/link'] = p['manifest']['digest'].encode()
        writes[repo / '_manifests/tags' / p['version'] / 'index/sha256' / sha / 'link'] = p['manifest']['digest'].encode()
    # Check the whole closed set before exclusive additions. Never overwrite
    # an old blob/tag, reload its Hauler store, or restart its registry service.
    for path, content in writes.items():
        if path.exists() and path.read_bytes() != content: raise ValueError('conflicting existing OCI Wasm registry object')
    for path, content in writes.items():
        if path.exists(): continue
        path.parent.mkdir(parents=True, exist_ok=True)
        with tempfile.NamedTemporaryFile(dir=path.parent, prefix='.ani-wasm-', delete=False) as f:
            temporary = pathlib.Path(f.name)
            f.write(content); f.flush(); os.fsync(f.fileno()); os.fchmod(f.fileno(), 0o644)
        try:
            try: os.link(temporary, path)
            except FileExistsError:
                if path.read_bytes() != content: raise ValueError('concurrent conflicting OCI Wasm registry object')
        finally: temporary.unlink()
    context = None
    for p in plugins:
        url = 'http://' + REGISTRY + '/v2/' + p['registryRepository'] + '/manifests/' + p['manifest']['digest']
        with http_open(urllib.request.Request(url, headers={'Accept': p['manifest']['mediaType']}), timeout=30, context=context) as response:
            if digest(response.read()) != p['manifest']['digest']: raise ValueError('served OCI Wasm manifest differs')


def publish_harbor(plugins):
    ca = pathlib.Path('/etc/kubernetes/ani/harbor/ca.crt')
    context = ssl.create_default_context(cafile=str(ca))
    basic = 'Basic ' + base64.b64encode(('admin:' + protected('/etc/kubernetes/ani/harbor/admin-password')).encode()).decode()
    origin = 'https://' + REGISTRY
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context), NoRedirect())
    def request(path, method='GET', body=None, headers=None, auth=None, missing=False):
        url = urllib.parse.urljoin(origin, path)
        parsed = urllib.parse.urlparse(url)
        if parsed.scheme != 'https' or parsed.netloc != REGISTRY: raise ValueError('registry redirect escaped the verified HTTPS authority')
        h = dict(headers or {})
        if auth: h['Authorization'] = auth
        try:
            with opener.open(urllib.request.Request(url, data=body, headers=h, method=method), timeout=30) as r:
                return r.status, dict(r.headers), r.read()
        except urllib.error.HTTPError as e:
            if missing and e.code == 404: return 404, {}, b''
            raise ValueError('verified registry request failed with HTTP ' + str(e.code)) from None
    project = 'ani-higress-platform'
    code, _, data = request('/api/v2.0/projects/' + project, auth=basic, missing=True)
    receipt = get('configmap', 'ani-higress-wasm-publication', NS)
    if code == 404:
        request('/api/v2.0/projects', 'POST', json.dumps({'project_name': project, 'metadata': {'public': 'true'}}).encode(), {'Content-Type': 'application/json'}, basic)
        _, _, data = request('/api/v2.0/projects/' + project, auth=basic)
        project_id = json.loads(data)['project_id']
        apply([{'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': meta('ani-higress-wasm-publication', NS), 'data': {'registry': REGISTRY, 'projectID': str(project_id)}}])
    elif not owned(receipt) or receipt['data'] != {'registry': REGISTRY, 'projectID': str(json.loads(data)['project_id'])} or json.loads(data)['metadata'].get('public') != 'true':
        raise ValueError('existing Harbor Wasm project is not this installation owned publication')
    for p in plugins:
        repo = p['registryRepository']
        _, _, token_data = request('/service/token?' + urllib.parse.urlencode({'service': 'harbor-registry', 'scope': 'repository:' + repo + ':push,pull'}), auth=basic)
        token = 'Bearer ' + json.loads(token_data).get('token', '')
        if token == 'Bearer ': raise ValueError('registry issued no scoped token')
        path = '/v2/' + repo
        code, _, data = request(path + '/manifests/' + p['version'], auth=token, headers={'Accept': p['manifest']['mediaType']}, missing=True)
        if code != 404:
            if digest(data) != p['manifest']['digest']: raise ValueError('existing Wasm version tag differs from source approval')
            continue
        for d in [p['config'], *p['layers']]:
            code, _, _ = request(path + '/blobs/' + d['digest'], 'HEAD', auth=token, missing=True)
            if code != 404: continue
            _, h, _ = request(path + '/blobs/uploads/', 'POST', b'', auth=token)
            location = h.get('Location') or h.get('location')
            if not location: raise ValueError('registry issued no bounded upload location')
            separator = '&' if '?' in location else '?'
            blob = (ROOT / p['artifactPath'] / 'blobs/sha256' / d['digest'].split(':', 1)[1]).read_bytes()
            request(location + separator + urllib.parse.urlencode({'digest': d['digest']}), 'PUT', blob, {'Content-Type': 'application/octet-stream'}, token)
        manifest = (ROOT / p['artifactPath'] / 'blobs/sha256' / p['manifest']['digest'].split(':', 1)[1]).read_bytes()
        request(path + '/manifests/' + p['version'], 'PUT', manifest, {'Content-Type': p['manifest']['mediaType']}, token)
        _, _, actual = request(path + '/manifests/' + p['manifest']['digest'], headers={'Accept': p['manifest']['mediaType']}, auth=token)
        if digest(actual) != p['manifest']['digest']: raise ValueError('Harbor served different OCI Wasm bytes')
    apply([{'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': meta('ani-higress-wasm-trust', NS), 'data': {'ca.crt': ca.read_text()}}])


def plugin_defaults(plugins):
    # The official OCI loader compares sha256 with the image manifest before
    # fetching its Wasm layer. loaded_modules separately verifies module bytes.
    return [{'apiVersion': 'extensions.higress.io/v1alpha1', 'kind': 'WasmPlugin',
             'metadata': meta('ani-platform-' + p['name'], NS),
             'spec': {'url': 'oci://' + REGISTRY + '/' + p['registryRepository'] + '@' + p['manifest']['digest'],
                      'sha256': p['manifest']['digest'].split(':', 1)[1], 'imagePullPolicy': 'IfNotPresent',
                      'defaultConfigDisable': True, 'failStrategy': 'FAIL_CLOSE'}} for p in plugins]


def gateway_objects():
    controller = 'higress.io/gateway-controller' + ('-' + GATEWAY_CLASS if GATEWAY_CLASS != 'higress' else '')
    allowed = {'namespaces': {'from': 'Selector', 'selector': {'matchExpressions': [{'key': 'kubernetes.io/metadata.name', 'operator': 'In', 'values': ALLOWED}]}}}
    listeners = [{'name': 'http', 'port': 80, 'protocol': 'HTTP', 'allowedRoutes': allowed}]
    if HOSTNAME: listeners[0]['hostname'] = HOSTNAME
    if TLS_SECRET:
        listeners.append({'name': 'https', 'port': 443, 'protocol': 'HTTPS', 'hostname': HOSTNAME,
                          'allowedRoutes': allowed, 'tls': {'mode': 'Terminate', 'certificateRefs': [{'group': '', 'kind': 'Secret', 'name': TLS_SECRET}]}})
    # Fixed upstream classInfo reconciles this description onto every managed
    # class. Declare it so strict identity checks compare the actual full spec.
    return [{'apiVersion': 'gateway.networking.k8s.io/v1', 'kind': 'GatewayClass', 'metadata': meta(GATEWAY_CLASS),
             'spec': {'controllerName': controller, 'description': 'The default Higress GatewayClass'}},
            {'apiVersion': 'gateway.networking.k8s.io/v1', 'kind': 'Gateway', 'metadata': meta(GATEWAY, NS), 'spec': {'gatewayClassName': GATEWAY_CLASS, 'listeners': listeners}}]


def install():
    plugins = materials()
    before = shared()
    existing = get('namespace', NS)
    if existing and not owned(existing): raise ValueError('foreign Higress namespace; refusing adoption')
    for name in ['ani-higress-wasm-trust', 'ani-higress-wasm-publication']:
        obj = get('configmap', name, NS)
        if obj and not owned(obj): raise ValueError('foreign Higress registry trust/publication identity')
    if MODE == 'harbor':
        ca = pathlib.Path('/etc/kubernetes/ani/harbor/ca.crt').read_text()
        ssl.create_default_context(cadata=ca)
        protected('/etc/kubernetes/ani/harbor/admin-password')
        trust = get('configmap', 'ani-higress-wasm-trust', NS)
        if trust and trust.get('data') != {'ca.crt': ca}: raise ValueError('existing Higress CA differs from verified Harbor CA')
    if KEY_FILE: protected(KEY_FILE)
    if TLS_SECRET:
        secret = get('secret', TLS_SECRET, NS)
        if not secret or secret.get('type') != 'kubernetes.io/tls' or not {'tls.crt', 'tls.key'} <= set(secret.get('data', {})):
            raise ValueError('configured HTTPS Secret is not ready in higress-system')
        run(['openssl', 'x509', '-noout', '-checkend', '86400'], base64.b64decode(secret['data']['tls.crt']).decode())
    for o in gateway_objects():
        crd = 'gatewayclasses.gateway.networking.k8s.io' if o['kind'] == 'GatewayClass' else 'gateways.gateway.networking.k8s.io'
        current = get(o['kind'], o['metadata']['name'], o['metadata'].get('namespace')) if get('crd', crd) else None
        if current and (not owned(current) or current['spec'] != o['spec']): raise ValueError('foreign or mismatched gateway identity')
    for o in plugin_defaults(plugins):
        current = get('wasmplugin', o['metadata']['name'], NS) if get('crd', 'wasmplugins.extensions.higress.io') else None
        if current and (not owned(current) or current['spec'] != o['spec']): raise ValueError('foreign or modified platform Wasm plugin')
    # Source-approved Gateway API document carries only four prerequisites.
    gateway_crds = [o for o in yaml.safe_load_all((WORK / 'gateway-api.yaml').read_text()) if o]
    if {o['metadata']['name'] for o in gateway_crds} != set(API_VERSIONS): raise ValueError('unexpected shared Gateway API prerequisite set')
    missing = [o for o in gateway_crds if o['metadata']['name'] not in before]
    with tarfile.open(CHART) as archive:
        crds = [o for n in ['higress/charts/higress-core/crds/customresourcedefinitions.gen.yaml', 'higress/charts/higress-core/crds/istio-envoyfilter.yaml'] for o in yaml.safe_load_all(archive.extractfile(n)) if o]
    if {o['metadata']['name'] for o in crds} != {'wasmplugins.extensions.higress.io', 'http2rpcs.networking.higress.io', 'mcpbridges.networking.higress.io', 'envoyfilters.networking.istio.io'}:
        raise ValueError('official Higress CRD set differs')
    new_crds = []
    for o in crds:
        current = get('crd', o['metadata']['name'])
        if current:
            required = o['spec']
            actual = current['spec']
            if any(actual[k] != required[k] for k in ['group', 'scope', 'versions']) or any(actual['names'].get(k) != v for k, v in required['names'].items()):
                raise ValueError('existing Higress/shared EnvoyFilter CRD schema differs')
        else:
            o['metadata'].setdefault('labels', {})['ani.io/managed-by'] = OWNER
            new_crds.append(o)
    (WORK / 'filter.py').chmod(0o700)
    rendered = run([HELM, 'template', RELEASE, str(CHART), '--namespace', NS, '--values', str(WORK / 'values.yaml'), '--post-renderer', str(WORK / 'filter.py')])
    for o in yaml.safe_load_all(rendered):
        if not o: continue
        current = get(o['kind'], o['metadata']['name'], o['metadata'].get('namespace'))
        if current: raise ValueError('official Higress install scope already contains an object')
    # Finish all API identity, schema and Chart scope checks before any write.
    apply([{'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': meta(NS)}])
    if missing:
        for o in missing: o['metadata'].setdefault('labels', {})['ani.io/managed-by'] = OWNER
        apply(missing)
    for name in API_VERSIONS: kube('wait', '--for=condition=Established', 'crd/' + name, '--timeout=180s')
    if new_crds: apply(new_crds)
    for o in crds:
        kube('wait', '--for=condition=Established', 'crd/' + o['metadata']['name'], '--timeout=180s')
    if MODE == 'harbor': publish_harbor(plugins)
    else: publish_material(plugins)
    env = dict(os.environ, KUBECONFIG=os.environ['ANI_RUN_KUBECONFIG'])
    run([HELM, 'install', RELEASE, str(CHART), '--namespace', NS, '--skip-crds', '--values', str(WORK / 'values.yaml'), '--post-renderer', str(WORK / 'filter.py'), '--labels', 'ani.io/managed-by=' + OWNER, '--wait', '--timeout', '600s'], env=env)
    apply(plugin_defaults(plugins))
    apply(gateway_objects())
    wait('Higress Gateway acceptance', lambda: gateway_ready())
    after = shared()
    if any(after.get(k) != v for k, v in before.items()): raise ValueError('shared Gateway API/Envoy/cert-manager identity changed')
    return check()


def gateway_ready():
    gateway = get('gateway', GATEWAY, NS)
    cls = get('gatewayclass', GATEWAY_CLASS)
    if not gateway or not cls: return False
    accepted = lambda o: any(c['type'] == 'Accepted' and c['status'] == 'True' and c.get('observedGeneration') == o['metadata']['generation'] for c in o.get('status', {}).get('conditions', []))
    return gateway if accepted(gateway) and accepted(cls) else False


def check():
    plugins = materials()
    facts = shared()
    if len([k for k in facts if k in API_VERSIONS]) != 4 or not gateway_ready(): raise ValueError('declared Gateway API and Gateway not accepted')
    for expected in gateway_objects():
        actual = get(expected['kind'], expected['metadata']['name'], expected['metadata'].get('namespace'))
        if not owned(actual) or actual['spec'] != expected['spec']: raise ValueError('Gateway owner/listener/allowedRoutes differs from this site')
    if MODE == 'harbor':
        trust = get('configmap', 'ani-higress-wasm-trust', NS)
        if not owned(trust) or trust.get('data') != {'ca.crt': pathlib.Path('/etc/kubernetes/ani/harbor/ca.crt').read_text()}: raise ValueError('Higress registry CA trust differs')
    ids = []
    for name in ['higress-controller', 'higress-gateway']:
        deployment = get('deployment', name, NS)
        if not owned(deployment) or deployment['status'].get('availableReplicas', 0) != deployment['spec']['replicas']:
            raise ValueError('owned Higress workload not available')
        for c in deployment['spec']['template']['spec']['containers']:
            if c['image'] != IMAGES[c['name']]: raise ValueError('Higress workload image differs from source approval')
        selector = ','.join(k + '=' + v for k, v in deployment['spec']['selector']['matchLabels'].items())
        pods = json.loads(kube('get', 'pods', '-n', NS, '-l', selector, '-o', 'json'))['items']
        if len(pods) != deployment['spec']['replicas']: raise ValueError('Higress replica count differs')
        for p in pods:
            for c in p['status'].get('containerStatuses', []):
                if not c.get('ready') or IMAGES[c['name']].rsplit('@', 1)[1] not in c.get('imageID', ''): raise ValueError('actual Higress imageID differs from approved amd64 manifest')
                ids.append(c['imageID'])
    for expected in plugin_defaults(plugins):
        p = get('wasmplugin', expected['metadata']['name'], NS)
        if not owned(p) or p['spec'] != expected['spec']: raise ValueError('platform Wasm plugin is not the credential-free default')
    service = get('service', 'higress-gateway', NS)
    expected_ports = [HTTP_PORT] + ([HTTPS_PORT] if TLS_SECRET else [])
    if sorted(p['nodePort'] for p in service['spec']['ports']) != sorted(expected_ports): raise ValueError('Higress exposed/reserved port declaration differs')
    return {'status': 'pass', 'imageIDs': ids, 'gatewayClass': GATEWAY_CLASS, 'gateway': NS + '/' + GATEWAY,
            'httpNodePort': HTTP_PORT, 'https': 'configured' if TLS_SECRET else 'reserved-not-listening',
            'wasmRegistry': REGISTRY, 'plugins': 'fixed-disabled-defaults-await-ANI-config', 'sharedProtection': facts}


def gateway_pods():
    d = get('deployment', 'higress-gateway', NS)
    selector = ','.join(k + '=' + v for k, v in d['spec']['selector']['matchLabels'].items())
    return json.loads(kube('get', 'pods', '-n', NS, '-l', selector, '-o', 'json'))['items']


def config_dump(pod):
    # This response contains plugin credentials. It stays in memory and is
    # never emitted or copied into logs/evidence.
    return kube('exec', pod, '-n', NS, '-c', 'higress-gateway', '--',
                'curl', '-fsS', 'http://127.0.0.1:15000/config_dump')


def loaded_modules(plugins):
    expected = {p['wasmDigest'].split(':', 1)[1]: p['name'] for p in plugins}
    loaded = {}
    for pod in gateway_pods():
        name = pod['metadata']['name']
        tree = json.loads(config_dump(name))
        paths = set()
        def walk(o):
            if isinstance(o, dict):
                for key, value in o.items():
                    if key == 'filename' and isinstance(value, str) and value.endswith('.wasm') and value.startswith('/') and re.fullmatch(r'[A-Za-z0-9/_.:-]+', value): paths.add(value)
                    else: walk(value)
            elif isinstance(o, list):
                for value in o: walk(value)
        walk(tree)
        actual = {}
        for path in paths:
            raw = kube('exec', name, '-n', NS, '-c', 'higress-gateway', '--', 'sha256sum', path)
            sha = raw.split()[0]
            if sha in expected: actual[expected[sha]] = {'contentDigest': 'sha256:' + sha, 'loadedPath': path}
        if set(actual) != {p['name'] for p in plugins}: return False
        loaded[name] = actual
    return loaded if len(loaded) == 2 else False


def probe():
    result = check()
    plugins = materials()
    before = shared()
    defaults = plugin_defaults(plugins)
    plugin_uids = {o['metadata']['name']: get('wasmplugin', o['metadata']['name'], NS)['metadata']['uid'] for o in defaults}
    if get('namespace', PROBE_NS): raise ValueError('probe namespace already exists; preserve and inspect failed prior probe')
    key = protected(KEY_FILE) if KEY_FILE else secrets.token_urlsafe(36)
    host = HOSTNAME.replace('*.', 'probe.', 1) if HOSTNAME else PROBE_NS + '.invalid'
    marker = 'HIGRESS_EXTERNAL_ENGINE_BOUNDARY:' + secrets.token_hex(12)
    print(json.dumps({'probeNamespace': PROBE_NS, 'scope': 'external-inference-engine-boundary', 'stage': 'creating'}), flush=True)
    apply([{'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': meta(PROBE_NS)},
           {'apiVersion': 'v1', 'kind': 'Secret', 'metadata': meta('temporary-platform-probe-key', PROBE_NS), 'type': 'Opaque',
            'data': {'api-key': base64.b64encode(key.encode()).decode()}}])
    model_a, model_b = PROBE_NS + '-a', PROBE_NS + '-b'
    backend_facts = {}
    for index, suffix in enumerate(['a', 'b']):
        name = 'engine-boundary-' + suffix
        payload = {'boundary': 'external-inference-engine', 'backend': suffix, 'marker': marker}
        script = '#!/bin/sh\nprintf "Content-Type: application/json\\r\\n\\r\\n"\nprintf %s ' + "'" + json.dumps(payload, separators=(',', ':')) + "'\n"
        objects = [
            {'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': meta(name, PROBE_NS), 'data': {'engine': script}},
            {'apiVersion': 'apps/v1', 'kind': 'Deployment', 'metadata': meta(name, PROBE_NS),
             'spec': {'replicas': 1, 'selector': {'matchLabels': {'ani.io/higress-engine-boundary': name}},
                      'template': {'metadata': {'labels': {'ani.io/managed-by': OWNER, 'ani.io/higress-engine-boundary': name}},
                                   'spec': {'nodeName': NODES[index], 'automountServiceAccountToken': False,
                                            'securityContext': {'runAsUser': 1000, 'runAsGroup': 1000},
                                            'volumes': [{'name': 'engine-boundary', 'configMap': {'name': name, 'defaultMode': 0o555}}],
                                            'containers': [{'name': 'external-engine-boundary', 'image': BACKEND_IMAGE,
                                                            'command': ['httpd', '-f', '-p', '8080', '-h', '/www'],
                                                            'volumeMounts': [{'name': 'engine-boundary', 'mountPath': '/www/cgi-bin', 'readOnly': True}],
                                                            'readinessProbe': {'tcpSocket': {'port': 8080}, 'periodSeconds': 2},
                                                            'resources': {'requests': {'cpu': '10m', 'memory': '16Mi'}, 'limits': {'cpu': '100m', 'memory': '64Mi'}}}]}}}},
            {'apiVersion': 'v1', 'kind': 'Service', 'metadata': meta(name, PROBE_NS),
             'spec': {'selector': {'ani.io/higress-engine-boundary': name}, 'ports': [{'name': 'http', 'port': 80, 'targetPort': 8080}]}}]
        apply(objects)
        kube('rollout', 'status', 'deployment/' + name, '-n', PROBE_NS, '--timeout=180s')
        pods = json.loads(kube('get', 'pods', '-n', PROBE_NS, '-l', 'ani.io/higress-engine-boundary=' + name, '-o', 'json'))['items']
        if len(pods) != 1 or pods[0]['spec']['nodeName'] != NODES[index] or not any(BACKEND_IMAGE.rsplit('@', 1)[1] in c.get('imageID', '') for c in pods[0]['status'].get('containerStatuses', [])):
            raise ValueError('external-engine boundary scheduling/image identity differs')
        slices = json.loads(kube('get', 'endpointslices', '-n', PROBE_NS, '-l', 'kubernetes.io/service-name=' + name, '-o', 'json'))['items']
        endpoints = [e for s in slices for e in s.get('endpoints', []) if e.get('conditions', {}).get('ready')]
        if len(endpoints) != 1 or endpoints[0].get('targetRef', {}).get('uid') != pods[0]['metadata']['uid']:
            raise ValueError('backend Service EndpointSlice does not target the real scheduled Pod')
        backend_facts[suffix] = {'podUID': pods[0]['metadata']['uid'], 'node': NODES[index], 'serviceUID': get('service', name, PROBE_NS)['metadata']['uid']}
    activated = plugin_defaults(plugins)
    configs = {
        'model-router': ('AUTHN', 900, {'modelToHeader': 'x-higress-llm-model', 'enableOnPathSuffix': ['/completions']}),
        'key-auth': ('AUTHN', 310, {'global_auth': True, 'consumers': [{'name': 'temporary-platform-probe', 'credential': key}],
                                 'keys': ['x-api-key'], 'in_header': True, 'in_query': False}),
        'key-rate-limit': ('UNSPECIFIED_PHASE', 10, {'limit_by_header': 'x-higress-llm-model', 'limit_keys': [{'key': model_a, 'query_per_second': 2}, {'key': model_b, 'query_per_second': 2}]}),
        'ai-statistics': ('STATS', 200, {'enable_path_suffixes': ['/completions'], 'use_default_response_attributes': True}),
    }
    for o in activated:
        name = o['metadata']['name'].removeprefix('ani-platform-')
        phase, priority, config = configs[name]
        o['spec'].update({'phase': phase, 'priority': priority, 'matchRules': [{'domain': [host], 'config': config, 'configDisable': False}]})
    apply(activated)
    # Readiness, including actual module bytes on both replicas, precedes
    # publication. Failed plugin loading must leave this route unpublished.
    loaded = wait('all four actual Wasm modules on each gateway replica', lambda: loaded_modules(plugins), 300)
    route = {'apiVersion': 'gateway.networking.k8s.io/v1', 'kind': 'HTTPRoute', 'metadata': meta('external-engine-boundary', PROBE_NS),
             'spec': {'parentRefs': [{'name': GATEWAY, 'namespace': NS, 'sectionName': 'http'}], 'hostnames': [host],
                      'rules': [{'matches': [{'path': {'type': 'Exact', 'value': '/v1/completions'}, 'headers': [{'name': 'x-higress-llm-model', 'value': model}]}],
                                 'filters': [{'type': 'URLRewrite', 'urlRewrite': {'path': {'type': 'ReplaceFullPath', 'replaceFullPath': '/cgi-bin/engine'}}}],
                                 'backendRefs': [{'name': 'engine-boundary-' + suffix, 'port': 80}]} for model, suffix in [(model_a, 'a'), (model_b, 'b')]]}}
    apply([route])
    def route_ready():
        o = get('httproute', 'external-engine-boundary', PROBE_NS)
        for p in o.get('status', {}).get('parents', []):
            if p.get('controllerName', '').startswith('higress.io/gateway-controller') and all(any(c['type'] == k and c['status'] == 'True' and c.get('observedGeneration') == o['metadata']['generation'] for c in p.get('conditions', [])) for k in ['Accepted', 'ResolvedRefs']): return o
        return False
    wait('probe HTTPRoute accepted and refs resolved', route_ready)
    def request(address, model, credential):
        headers = {'Host': host, 'Content-Type': 'application/json'}
        if credential is not None: headers['x-api-key'] = credential
        body = json.dumps({'model': model, 'boundary': 'external-inference-engine'}).encode()
        try:
            with http_open(urllib.request.Request('http://' + address + ':' + str(HTTP_PORT) + '/v1/completions', data=body, headers=headers), timeout=15) as r: return r.status, r.read()
        except urllib.error.HTTPError as e: return e.code, e.read()
    assertions = []
    for address in ADDRESSES:
        for model, suffix in [(model_a, 'a'), (model_b, 'b')]:
            time.sleep(1.1)
            code, data = request(address, model, key)
            if code != 200 or json.loads(data) != {'boundary': 'external-inference-engine', 'backend': suffix, 'marker': marker}:
                raise ValueError('body.model did not reach the exact real external-engine boundary response')
            assertions.append({'nodeAddress': address, 'behavior': 'body.model-' + suffix, 'status': 200})
        for credential, label in [(None, 'missing-key'), ('wrong-platform-probe-key', 'wrong-key')]:
            code, _ = request(address, model_a, credential)
            if code != 401: raise ValueError('key-auth did not reject ' + label)
            assertions.append({'nodeAddress': address, 'behavior': label, 'status': code})
        code, _ = request(address, 'unknown-external-engine-model', key)
        if code != 404: raise ValueError('unknown model did not return 404')
        assertions.append({'nodeAddress': address, 'behavior': 'unknown-model', 'status': code})
        time.sleep(1.1)
        codes = [request(address, model_a, key)[0] for _ in range(30)]
        if 429 not in codes or any(c not in [200, 429] for c in codes): raise ValueError('real key-rate-limit did not enforce 429')
        assertions.append({'nodeAddress': address, 'behavior': 'rate-limit', 'status': 429})
    # Cleanup is reached only after every assertion succeeds. A failure retains
    # its first code, route, identity and backend scene for diagnosis.
    apply(defaults)
    kube('delete', 'namespace', PROBE_NS, '--wait=true', '--timeout=300s')
    if get('namespace', PROBE_NS): raise ValueError('probe namespace/identity normal finalization incomplete')
    wait('temporary API key removed from active gateway configuration', lambda: all(key not in config_dump(p['metadata']['name']) for p in gateway_pods()), 120)
    if plugin_uids != {o['metadata']['name']: get('wasmplugin', o['metadata']['name'], NS)['metadata']['uid'] for o in defaults}: raise ValueError('platform plugin identity changed during probe')
    if shared() != before: raise ValueError('shared API/Envoy/cert-manager objects changed during gateway probe')
    result.update({'scope': 'external-inference-engine-boundary', 'assertions': assertions, 'loadedModules': loaded,
                   'backends': backend_facts, 'normalCleanup': 'pass', 'temporaryCredentialCleanup': 'pass'})
    check()
    return result


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('mode', choices=['install', 'check', 'probe'])
    parser.add_argument('--output-dir')
    args = parser.parse_args()
    try:
        result = {'install': install, 'check': check, 'probe': probe}[args.mode]()
        if args.output_dir:
            path = pathlib.Path(args.output_dir) / 'higress-behavior.json'
            path.write_text(json.dumps(result, sort_keys=True, indent=2) + '\n')
            path.chmod(0o600)
        print(json.dumps(result, sort_keys=True))
    except ValueError as e:
        print(json.dumps({'status': 'fail', 'reason': str(e)}), file=sys.stderr)
        sys.exit(1)
    except (OSError, urllib.error.URLError) as e:
        print(json.dumps({'status': 'fail', 'errorType': type(e).__name__}), file=sys.stderr)
        sys.exit(1)

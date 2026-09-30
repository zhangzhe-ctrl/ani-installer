"""Offline contract check against downloaded Charts; never a release package."""
import json
import base64
import os
from pathlib import Path
import sys
import tempfile
import unittest

import yaml

sys.path.insert(0, str(Path(__file__).parents[1] / 'scripts'))
import render_gateways
import render_application


@unittest.skipUnless(os.environ.get('ANI_CHART_TEST_BUNDLE'), 'Fedora downloaded Chart input required')
class GatewayChartTest(unittest.TestCase):
    def test_downloaded_charts_keep_tls_and_digest_images(self):
        lock = json.loads((Path(__file__).parents[1] / 'materials/actual-images.lock.json').read_text())
        # Isolated test references only. The real lock remains not_run until Harbor readback.
        lock['registryImport'] = 'verified'
        for image in lock['images']:
            image['targetDigestRef'] = 'fixture.invalid/ani/' + image['id'] + '@' + image['manifestDigest']
        site = {key + '_url': 'https://' + key + '.example.invalid'
                for key in ('console', 'boss', 'api', 'inference')}
        site['websocket_url'] = 'wss://session.example.invalid'
        site.update({'s3_public_endpoint': 'https://s3.example.invalid', 'entry_node_port': 30443, 'inference_node_port': 30444})
        with tempfile.TemporaryDirectory(prefix='ani-chart-test-') as directory:
            output = Path(directory) / 'manifests'
            render_gateways.render(Path(os.environ['ANI_CHART_TEST_BUNDLE']), lock,
                Path(os.environ['ANI_CHART_TEST_HELM']), Path(os.environ['ANI_CHART_TEST_REFERENCE']),
                site, {'mcp_session_seed': 'test-only-seed-never-for-deployment', 's3_ca_pem': 'test-only-ca'}, output)
            controllers = list(yaml.safe_load_all((output / 'controllers.yaml').read_text()))
            webhook = next(doc for doc in controllers if doc['kind'] == 'MutatingWebhookConfiguration')
            self.assertIn('cert-manager.io/inject-ca-from', webhook['metadata']['annotations'])
            expected = {row['targetDigestRef'] for row in lock['images']}
            for doc in controllers:
                pod = doc.get('spec', {}).get('template', {}).get('spec', {})
                for container in pod.get('containers', []) + pod.get('initContainers', []):
                    self.assertIn(container['image'], expected)
            routes = list(yaml.safe_load_all((output / 'gateway.yaml').read_text()))
            policy = next(doc for doc in routes if doc['kind'] == 'SecurityPolicy')
            self.assertFalse(policy['spec']['extAuth']['failOpen'])
            self.assertEqual(policy['spec']['targetRefs'][0]['name'], 'ani-aigw')
            gateways = [doc for doc in routes if doc['kind'] == 'Gateway']
            self.assertEqual({doc['metadata']['name'] for doc in gateways}, {'ani-aigw', 'ani-entry'})
            for gateway in gateways:
                self.assertEqual(gateway['spec']['listeners'][0]['protocol'], 'HTTPS')
            self.assertEqual(len([doc for doc in routes if doc['kind'] == 'HTTPRoute']), 5)
            self.assertFalse(any(doc['kind'] == 'ServiceAccount' and doc['metadata']['name'] == 'default' for doc in routes))
            reference = Path(os.environ['ANI_CHART_TEST_REFERENCE'])
            contracts = json.loads((reference / 'secrets/contracts.json').read_text())
            secrets = []
            for contract in contracts:
                if contract['namespace'] != 'ani-system' or contract['type'] != 'Opaque':
                    continue
                data = {key: base64.b64encode(b'isolated-test-value').decode() for key in contract['keys']}
                if contract['name'] == 'ani-session-gateway-secrets':
                    data['ticket-encryption-key'] = base64.b64encode(b'x' * 32).decode()
                secrets.append({'apiVersion': 'v1', 'kind': 'Secret',
                    'metadata': {'name': contract['name'], 'namespace': 'ani-system'}, 'data': data})
            site.update({'s3_endpoint': 'https://s3.example.invalid', 's3_public_endpoint': 'https://s3.example.invalid',
                'milvus_endpoint': 'milvus.ani-platform.svc:19530', 'prometheus_url': 'http://prometheus.ani-platform.svc',
                'loki_url': 'http://loki.ani-platform.svc', 'storage_class': 'test-storage',
                'prometheus_namespace': 'ani-observability',
                'target_nodes': ['172.16.101.10', '172.16.101.11', '172.16.101.12'],
                'kubernetes_service_ip': '10.96.0.1',
                'deployment_env': {'ani-gateway': {
                    'KAIWU_CONSOLE_PUBLIC_URL': '', 'KAIWU_BOSS_PUBLIC_URL': '',
                    'KAIWU_CONSOLE_SECRET': 'test-only-integration-secret',
                    'KAIWU_BOSS_SECRET': 'test-only-integration-secret',
                    'HARBOR_ENDPOINT': 'https://fixture.invalid', 'HARBOR_USERNAME': 'test-only-user',
                    'HARBOR_PASSWORD': 'test-only-password'}}})
            application_output = Path(directory) / 'application'
            render_application.render(reference, site, lock, {'secrets': secrets, 'site_ca_pem': 'test-only-ca'},
                                      application_output, output)
            app_docs = [doc for stage in ('prepare', 'core', 'apps', 'gateway')
                        for doc in yaml.safe_load_all((application_output / (stage + '.yaml')).read_text()) if doc]
            deployments = {doc['metadata']['name'] for doc in app_docs if doc['kind'] == 'Deployment'}
            self.assertTrue(render_application.CORE | render_application.APPS <= deployments)


if __name__ == '__main__':
    unittest.main()

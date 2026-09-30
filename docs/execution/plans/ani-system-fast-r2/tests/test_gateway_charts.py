"""Offline contract check against downloaded Charts; never a release package."""
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest

import yaml

sys.path.insert(0, str(Path(__file__).parents[1] / 'scripts'))
import render_gateways


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
        with tempfile.TemporaryDirectory(prefix='ani-chart-test-') as directory:
            output = Path(directory) / 'manifests'
            render_gateways.render(Path(os.environ['ANI_CHART_TEST_BUNDLE']), lock,
                Path(os.environ['ANI_CHART_TEST_HELM']), Path(os.environ['ANI_CHART_TEST_REFERENCE']),
                site, {'mcp_session_seed': 'test-only-seed-never-for-deployment'}, output)
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
            self.assertEqual(len([doc for doc in routes if doc['kind'] == 'HTTPRoute']), 4)
            self.assertFalse(any(doc['kind'] == 'ServiceAccount' and doc['metadata']['name'] == 'default' for doc in routes))


if __name__ == '__main__':
    unittest.main()

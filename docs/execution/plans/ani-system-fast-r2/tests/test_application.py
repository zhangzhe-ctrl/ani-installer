import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('application', Path(__file__).parents[1] / 'scripts/render_application.py')
application = importlib.util.module_from_spec(spec)
spec.loader.exec_module(application)


class ApplicationTest(unittest.TestCase):
    def test_crd_default_conversion_does_not_hide_real_changes(self):
        original = {'group': 'gateway.networking.k8s.io', 'versions': [{'name': 'v1'}]}
        defaulted = {**original, 'conversion': {'strategy': 'None'}}
        webhook = {**original, 'conversion': {'strategy': 'Webhook'}}
        changed_version = {**defaulted, 'versions': [{'name': 'v2'}]}
        self.assertEqual(application.crd_spec_digest(original), application.crd_spec_digest(defaulted))
        self.assertNotEqual(application.crd_spec_digest(original), application.crd_spec_digest(webhook))
        self.assertNotEqual(application.crd_spec_digest(original), application.crd_spec_digest(changed_version))
        self.assertNotIn('conversion', original)

    def test_runtime_mapping_requires_verified_target(self):
        with self.assertRaisesRegex(ValueError, 'verified registry digest'):
            application.image_mappings({'registryImport': 'verified', 'images': [{'id': 'model-import-worker-dynamic', 'targetDigestRef': None}]})

    def test_dynamic_worker_uses_stay_distinct(self):
        rows = []
        for number, use in enumerate(('service', 'dynamic')):
            rows.append({'id': 'model-import-worker-' + use,
                         'targetDigestRef': 'registry.example/ani/worker-' + use + '@sha256:' + str(number) * 64,
                         'uses': [{'resource': 'ani-system/ConfigMap/materialization',
                                   'path': '/data/' + use, 'reference': 'worker@sha256:' + str(number) * 64}]})
        origins, _ = application.image_mappings({'registryImport': 'verified', 'images': rows})
        self.assertNotEqual(origins[('ani-system/ConfigMap/materialization', '/data/service')],
                            origins[('ani-system/ConfigMap/materialization', '/data/dynamic')])

    def test_redacted_reference_is_restored_to_secret_key(self):
        container = {'env': [{'name': 'REDIS_URL', 'value': '__SECRET_REF__ani-system/runtime:redis_url__'}]}
        application.update_env(container, {})
        self.assertEqual(container['env'][0], {'name': 'REDIS_URL', 'valueFrom': {'secretKeyRef': {'name': 'runtime', 'key': 'redis_url'}}})

    def test_generated_metadata_and_service_addresses_are_removed(self):
        doc = {'kind': 'Service', 'metadata': {'name': 'api', 'uid': 'old', 'annotations': {'old': 'value'}},
               'status': {'loadBalancer': {}}, 'spec': {'clusterIP': '10.0.0.1', 'clusterIPs': ['10.0.0.1'], 'ports': [{'port': 80, 'nodePort': 30080}]}}
        result = application.clean(doc)
        self.assertNotIn('status', result)
        self.assertNotIn('uid', result['metadata'])
        self.assertNotIn('clusterIP', result['spec'])
        self.assertNotIn('nodePort', result['spec']['ports'][0])
        self.assertEqual(result['metadata']['labels']['ani.io/app-task'], application.TASK)
        self.assertEqual(doc['metadata']['uid'], 'old')

    def test_ca_injection_annotation_survives_release_cleanup(self):
        doc = {'kind': 'MutatingWebhookConfiguration', 'metadata': {'name': 'webhook',
               'annotations': {'cert-manager.io/inject-ca-from': 'ns/certificate',
                               'meta.helm.sh/release-name': 'old'}}}
        self.assertEqual(application.clean(doc)['metadata']['annotations'],
                         {'cert-manager.io/inject-ca-from': 'ns/certificate'})


if __name__ == '__main__':
    unittest.main()

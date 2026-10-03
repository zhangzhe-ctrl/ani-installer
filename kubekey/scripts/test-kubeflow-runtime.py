#!/usr/bin/env python3
"""Regression for observed webhook bootstrap replay, preserving rotation guards."""
import pathlib
import sys
import tempfile
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1] / "ani/kubeflow"))
from common import Cluster


class ExistingSecret(Cluster):
    def read(self, value):
        return {"metadata": {**value["metadata"], "uid": "existing-uid", "resourceVersion": "7",
                            "labels": {"ani.io/managed-by": "ani-lab"}},
                "data": {"tls.crt": "Y2VydA==", "tls.key": "a2V5"}}

    def call(self, *args, **kwargs):
        raise AssertionError("an existing controller certificate must not be written")


class WebhookReplay(unittest.TestCase):
    def check_secret(self, namespace, name, data):
        with tempfile.TemporaryDirectory() as temporary:
            cluster = ExistingSecret({"owner": "ani-lab", "kubeconfig": "/unused"}, pathlib.Path(temporary) / "attempt")
            cluster.apply([{"apiVersion": "v1", "kind": "Secret", "metadata": {"namespace": namespace, "name": name}, "data": data}])
            self.assertEqual(cluster.writes, [])

    def test_both_known_empty_controller_placeholders_preserve_rotated_bytes(self):
        for name in ("kubeflow-trainer-webhook-cert", "jobset-webhook-server-cert"):
            self.check_secret("kubeflow-system", name, {})

    def test_same_name_in_another_namespace_cannot_use_exception(self):
        with self.assertRaisesRegex(RuntimeError, "refusing credential rotation"):
            self.check_secret("kubeflow", "kubeflow-trainer-webhook-cert", {})

    def test_nonempty_replacement_of_controller_certificate_is_refused(self):
        with self.assertRaisesRegex(RuntimeError, "refusing credential rotation"):
            self.check_secret("kubeflow-system", "kubeflow-trainer-webhook-cert", {"tls.crt": "d3Jvbmc="})

    def test_managed_database_placeholder_cannot_erase_credentials(self):
        with self.assertRaisesRegex(RuntimeError, "refusing credential rotation"):
            self.check_secret("kubeflow", "ani-kfp-api-db", {})


if __name__ == "__main__":
    unittest.main()

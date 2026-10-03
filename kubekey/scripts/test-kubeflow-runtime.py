#!/usr/bin/env python3
"""Regression for observed webhook bootstrap replay, preserving rotation guards."""
import pathlib
import copy
import json
import sys
import tempfile
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1] / "ani/kubeflow"))
from common import Cluster
from install import checked_policy


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


class DefaultedWebhook(Cluster):
    def __init__(self, site, directory, declared_scope=None):
        super().__init__(site, directory)
        self.calls = []
        self.declared_scope = declared_scope

    def read(self, value):
        live = copy.deepcopy(value)
        live["metadata"].update(uid="webhook-create-uid", resourceVersion="17")
        live["metadata"]["labels"] = {"ani.io/managed-by": "ani-lab"}
        for webhook in live["webhooks"]:
            for rule in webhook["rules"]:
                rule.setdefault("scope", "*")
        return live

    def call(self, args, value, **kwargs):
        self.calls.append(args)
        for webhook in value["webhooks"]:
            for rule in webhook["rules"]:
                if rule.get("scope") != (self.declared_scope or "*"):
                    raise RuntimeError("atomic webhook rules conflict with create manager")
        response = copy.deepcopy(value)
        response["metadata"].update(uid="webhook-create-uid", resourceVersion="18")
        return json.dumps(response)


class WebhookRuleDefaults(unittest.TestCase):
    def apply_webhook(self, kind, scope=None):
        with tempfile.TemporaryDirectory() as temporary:
            cluster = DefaultedWebhook({"owner": "ani-lab", "kubeconfig": "/unused"}, pathlib.Path(temporary) / "attempt", scope)
            rule = {"apiGroups": ["jobset.x-k8s.io"], "apiVersions": ["v1alpha2"], "operations": ["CREATE"], "resources": ["jobsets"]}
            if scope:
                rule["scope"] = scope
            cluster.apply([{"apiVersion": "admissionregistration.k8s.io/v1", "kind": kind,
                            "metadata": {"name": "task-webhook"}, "webhooks": [{"name": "jobset.kb.io", "rules": [rule]}]}])
            self.assertEqual(len(cluster.calls), 2)
            self.assertTrue(all("--force-conflicts" not in args for args in cluster.calls))
            self.assertEqual(cluster.writes[0]["uid"], "webhook-create-uid")
            self.assertEqual(cluster.writes[0]["result"], "CONFIRMED")

    def test_create_default_is_explicit_for_both_webhook_kinds_on_replay(self):
        for kind in ("MutatingWebhookConfiguration", "ValidatingWebhookConfiguration"):
            self.apply_webhook(kind)

    def test_explicit_scope_is_preserved(self):
        self.apply_webhook("ValidatingWebhookConfiguration", "Namespaced")


class ExistingPolicy(Cluster):
    def __init__(self, site, directory, manager):
        super().__init__(site, directory)
        self.manager = manager
        self.calls = []

    def read(self, value):
        return {"metadata": {**value["metadata"], "uid": "policy-create-uid", "resourceVersion": "23",
                "labels": {"ani.io/managed-by": "ani-lab"},
                "managedFields": [{"manager": self.manager, "fieldsV1": {"f:spec": {}}}]}}

    def call(self, args, value, **kwargs):
        self.calls.append(args)
        if args[0] != "replace" or "--field-manager=ani-kubeflow" not in args or "--force-conflicts" in args:
            raise AssertionError("legacy owned policy replacement must use UID/version conditions without force")
        if value["metadata"].get("uid") != "policy-create-uid" or value["metadata"].get("resourceVersion") != "23":
            raise AssertionError("policy identity/version conditions must survive replay")
        return json.dumps(value)


class PolicyCreatorReplay(unittest.TestCase):
    def apply_policy(self, manager):
        with tempfile.TemporaryDirectory() as temporary:
            cluster = ExistingPolicy({"owner": "ani-lab", "kubeconfig": "/unused"}, pathlib.Path(temporary) / "attempt", manager)
            cluster.apply([{"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingAdmissionPolicy",
                            "metadata": {"name": "ani-kfp-workspace"}, "spec": {"validations": [{"expression": "true"}]}}])
            self.assertEqual(len(cluster.calls), 2)
            self.assertEqual(cluster.writes[0]["uid"], "policy-create-uid")

    def test_legacy_task_policy_replacement_keeps_uid_version_conditions(self):
        self.apply_policy("kubectl-create")

    def test_foreign_policy_spec_manager_is_refused_before_a_request(self):
        with self.assertRaisesRegex(RuntimeError, "foreign policy spec manager"):
            self.apply_policy("foreign-operator")


class PolicyTypeCheckCompletion(unittest.TestCase):
    def current(self):
        # The actual 1.35.8 warning-to-empty SSA response keeps controller
        # ownership while omitting status.typeChecking from the JSON object.
        return {"metadata": {"name": "ani-kfp-workspace", "generation": 2,
                "managedFields": [
                    {"manager": "ani-kubeflow", "time": "2026-10-03T10:49:13Z", "fieldsV1": {"f:spec": {}}},
                    {"manager": "validatingadmissionpolicy-status", "operation": "Apply", "subresource": "status",
                     "time": "2026-10-03T10:49:13Z", "fieldsV1": {"f:status": {"f:observedGeneration": {}, "f:typeChecking": {}}}}]},
                "status": {"observedGeneration": 2}}

    def test_controller_completed_empty_transition_is_recognized(self):
        self.assertTrue(checked_policy(self.current()))

    def test_observed_generation_without_controller_completion_is_not_ready(self):
        value = self.current()
        value["metadata"]["managedFields"] = []
        self.assertFalse(checked_policy(value))

    def test_stale_generation_and_stale_controller_time_are_not_ready(self):
        value = self.current()
        value["status"]["observedGeneration"] = 1
        self.assertFalse(checked_policy(value))
        value = self.current()
        value["metadata"]["managedFields"][1]["time"] = "2026-10-03T10:49:12Z"
        self.assertFalse(checked_policy(value))

    def test_wrong_status_manager_or_incomplete_field_ownership_is_not_ready(self):
        value = self.current()
        value["metadata"]["managedFields"][1]["manager"] = "other-manager"
        self.assertFalse(checked_policy(value))
        value = self.current()
        del value["metadata"]["managedFields"][1]["fieldsV1"]["f:status"]["f:typeChecking"]
        self.assertFalse(checked_policy(value))

    def test_actual_type_warning_is_a_failure(self):
        value = self.current()
        value["status"]["typeChecking"] = {"expressionWarnings": [{"warning": "undefined field requests"}]}
        with self.assertRaisesRegex(RuntimeError, "type checking failed"):
            checked_policy(value)

    def test_explicit_empty_type_checking_is_ready(self):
        value = self.current()
        value["metadata"]["managedFields"] = []
        value["status"]["typeChecking"] = {}
        self.assertTrue(checked_policy(value))


if __name__ == "__main__":
    unittest.main()

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
from install import checked_policy, materialize
from storage import same_policy, same_identity
from resources import NGINX


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


class ScopedStorageReadback(unittest.TestCase):
    def policy(self):
        return {"Version": "2012-10-17", "Statement": [{"Effect": "Allow",
            "Action": ["s3:GetObject", "s3:PutObject"], "Resource": ["arn:aws:s3:::owned/artifacts/*"],
            "Condition": {"StringLike": {"s3:prefix": ["artifacts", "artifacts/", "artifacts/*"]}}}]}

    def test_equivalent_iam_sets_survive_actual_server_reordering(self):
        desired = self.policy()
        actual = copy.deepcopy(desired)
        actual["Statement"][0]["Action"].reverse()
        actual["Statement"][0]["Condition"]["StringLike"]["s3:prefix"].reverse()
        self.assertTrue(same_policy(actual, desired))

    def test_broader_prefix_or_resource_and_missing_condition_are_rejected(self):
        for change in (
            lambda s: s["Condition"]["StringLike"].update({"s3:prefix": ["*"]}),
            lambda s: s.update(Resource=["arn:aws:s3:::owned/*"]),
            lambda s: s.pop("Condition"),
        ):
            actual = self.policy()
            change(actual["Statement"][0])
            self.assertFalse(same_policy(actual, self.policy()))

    def test_extra_permission_statement_or_duplicate_is_rejected(self):
        for change in (
            lambda p: p["Statement"][0]["Action"].append("s3:DeleteObject"),
            lambda p: p["Statement"].append({"Effect": "Allow", "Action": ["s3:*"], "Resource": ["*"]}),
            lambda p: p["Statement"][0]["Action"].append("s3:GetObject"),
        ):
            actual = self.policy()
            change(actual)
            self.assertFalse(same_policy(actual, self.policy()))

    def test_account_identity_status_and_explicit_scope_are_required(self):
        info = {"accessKey": "task-key", "parentUser": "root-key", "userType": "Service Account",
                "accountStatus": "on", "impliedPolicy": False, "policy": json.dumps(self.policy())}
        self.assertTrue(same_identity(info, "task-key", "root-key", self.policy()))
        for key, invalid in (("accessKey", "other"), ("parentUser", "other"), ("userType", "Regular"),
                             ("accountStatus", "off"), ("impliedPolicy", True), ("policy", "not-json")):
            actual = dict(info, **{key: invalid})
            self.assertFalse(same_identity(actual, "task-key", "root-key", self.policy()))


class MySQLDependencyReadiness(unittest.TestCase):
    def test_materialized_mysql_waits_for_authenticated_final_tcp_server(self):
        root = pathlib.Path(__file__).resolve().parents[1] / "ani/kubeflow"
        references = json.loads((root / "source-images.json").read_text())
        values = materialize(root / "overlay", {"images": {name: "offline/" + name for name in references},
                            "database_class": "ani-block", "database_size": "20Gi"})
        database = next(v for v in values if v["kind"] == "Deployment" and v["metadata"]["name"] == "mysql")
        container = database["spec"]["template"]["spec"]["containers"][0]
        for key in ("startupProbe", "readinessProbe"):
            probe = container[key]
            query = probe["exec"]["command"][-1]
            self.assertIn("--protocol=TCP -h127.0.0.1", query)
            self.assertIn('MYSQL_PWD="$MYSQL_ROOT_PASSWORD"', query)
            self.assertIn('SELECT 1', query)
            self.assertLessEqual(probe["failureThreshold"] * probe["periodSeconds"], 500)
        self.assertEqual(database["spec"]["template"]["spec"]["volumes"][0]["persistentVolumeClaim"]["claimName"], "mysql-pv-claim")


class LegacyVisualizationExcluded(unittest.TestCase):
    def test_backend_startup_configuration_keeps_excluded_service_closed(self):
        root = pathlib.Path(__file__).resolve().parents[1] / "ani/kubeflow"
        references = json.loads((root / "source-images.json").read_text())
        values = materialize(root / "overlay", {"images": {name: "offline/" + name for name in references},
                            "database_class": "ani-block", "database_size": "20Gi"})
        api = next(v for v in values if v["kind"] == "Deployment" and v["metadata"]["name"] == "ml-pipeline")
        environment = {v["name"]: v.get("value") for v in api["spec"]["template"]["spec"]["containers"][0]["env"]}
        self.assertEqual(environment["ML_PIPELINE_VISUALIZATIONSERVER_SERVICE_HOST"], "127.0.0.1")
        self.assertEqual(environment["ML_PIPELINE_VISUALIZATIONSERVER_SERVICE_PORT"], "9")
        self.assertFalse(any("visualizationserver" in v["metadata"]["name"] for v in values))
        self.assertIn("location ^~ /apis/v1beta1/visualizations { return 404; }", NGINX)
        self.assertIn("location ^~ /api.VisualizationService/ { return 404; }", NGINX)


class ExistingDownwardDeployment(Cluster):
    def read(self, value):
        current = copy.deepcopy(value)
        current["metadata"].update(uid="deployment-create-uid", resourceVersion="29", labels={"ani.io/managed-by": "ani-lab"})
        return current

    def call(self, args, value, **kwargs):
        selector = value["spec"]["template"]["spec"]["containers"][0]["env"][0]["valueFrom"]["fieldRef"]
        if selector != {"apiVersion": "v1", "fieldPath": "metadata.namespace"}:
            raise RuntimeError("atomic ObjectFieldSelector conflicts with API-defaulted v1")
        if "--force-conflicts" in args:
            raise AssertionError("defaults must not force field ownership")
        return json.dumps(value)


class DownwardAPIReplay(unittest.TestCase):
    def test_namespace_selector_replays_with_explicit_server_default(self):
        with tempfile.TemporaryDirectory() as temporary:
            cluster = ExistingDownwardDeployment({"owner": "ani-lab", "kubeconfig": "/unused"}, pathlib.Path(temporary) / "attempt")
            cluster.apply([{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "ml-pipeline", "namespace": "kubeflow"},
                "spec": {"template": {"spec": {"containers": [{"name": "api", "env": [{"name": "POD_NAMESPACE", "valueFrom": {"fieldRef": {"fieldPath": "metadata.namespace"}}}]}]}}}}])
            self.assertEqual(cluster.writes[0]["uid"], "deployment-create-uid")
            self.assertEqual(cluster.writes[0]["result"], "CONFIRMED")


if __name__ == "__main__":
    unittest.main()

#!/usr/bin/env python3
"""Regression for observed webhook bootstrap replay, preserving rotation guards."""
import ast
import pathlib
import copy
import json
import sys
import tempfile
import runpy
import unittest
from unittest import mock

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1] / "ani/kubeflow"))
from common import Cluster, endpoint
from install import checked_policy, materialize, entry_ports_available
from storage import same_policy, same_identity
from resources import NGINX, tenant
import stage2
from storage import model_policy

KUBEOVN_CAPABILITY = {"network_stack": "kubeovn", "network_policy": "required", "network_policy_contract": "kubeovn-required-v1"}


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
                            "database_class": "ani-block", "database_size": "20Gi", **KUBEOVN_CAPABILITY})
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


class WorkflowPodNaming(unittest.TestCase):
    def test_kcn_uses_argo_node_ids_and_kubeovn_keeps_default_names(self):
        root = pathlib.Path(__file__).resolve().parents[1] / "ani/kubeflow"
        references = json.loads((root / "source-images.json").read_text())
        for capability, expected in (({"network_stack": "kcn", "network_policy": "unsupported",
                                      "network_policy_contract": "kcn-test-unsupported-v1"}, "v1"),
                                     (KUBEOVN_CAPABILITY, None)):
            with self.subTest(provider=capability["network_stack"]):
                values = materialize(root / "overlay", {
                    "images": {name: "offline/" + name for name in references},
                    "database_class": "ani-block", "database_size": "20Gi", **capability})
                controller = next(v for v in values if v["kind"] == "Deployment"
                                  and v["metadata"]["name"] == "workflow-controller")
                environment = controller["spec"]["template"]["spec"]["containers"][0]["env"]
                self.assertEqual([e for e in environment if e["name"] == "POD_NAMES"],
                                 [] if expected is None else [{"name": "POD_NAMES", "value": expected}])
                self.assertTrue(any(e["name"] == "LEADER_ELECTION_IDENTITY" and e.get("valueFrom")
                                    for e in environment))


class LegacyVisualizationExcluded(unittest.TestCase):
    def test_backend_startup_configuration_keeps_excluded_service_closed(self):
        root = pathlib.Path(__file__).resolve().parents[1] / "ani/kubeflow"
        references = json.loads((root / "source-images.json").read_text())
        values = materialize(root / "overlay", {"images": {name: "offline/" + name for name in references},
                            "database_class": "ani-block", "database_size": "20Gi", **KUBEOVN_CAPABILITY})
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


class EntryPortsBeforeWrites(unittest.TestCase):
    def cluster(self, namespace, name, port, owner="ani-lab"):
        class ReadOnlyServices:
            site = {"http_port": 30445, "grpc_port": 30446, "owner": "ani-lab"}
            def call(self, args):
                if args != ["get", "services", "--all-namespaces", "-o", "json"]:
                    raise AssertionError("port preflight must perform only the cluster Service read")
                return json.dumps({"items": [{"metadata": {"namespace": namespace, "name": name,
                      "labels": {"ani.io/managed-by": owner}}, "spec": {"ports": [{"nodePort": port}]}}]})
        return ReadOnlyServices()

    def test_foreign_allocation_fails_before_any_persistent_request(self):
        with self.assertRaisesRegex(RuntimeError, "already allocated to ani-business-envoy/ani-entry"):
            entry_ports_available(self.cluster("ani-business-envoy", "ani-entry", 30445))

    def test_unoccupied_ports_and_owned_entry_replay_are_allowed(self):
        entry_ports_available(self.cluster("ani-business-envoy", "ani-entry", 30443))
        entry_ports_available(self.cluster("kubeflow", "ani-kfp-entry", 30445))
        with self.assertRaisesRegex(RuntimeError, "already allocated"):
            entry_ports_available(self.cluster("kubeflow", "ani-kfp-entry", 30445, "foreign-owner"))


class EntryConfigurationReplay(unittest.TestCase):
    def update(self, manager="ani-kubeflow", extra_data=False):
        class ExistingEntry(Cluster):
            def read(self, value):
                return {"metadata": {**value["metadata"], "uid": "entry-create-uid", "resourceVersion": "31",
                    "labels": {"ani.io/managed-by": "ani-lab", "retained-label": "keep"},
                    "annotations": {"retained-annotation": "keep"},
                    "managedFields": [{"manager": manager, "operation": "Update", "fieldsV1": {"f:data": {}}}]},
                    "data": {"nginx.conf": "old", **({"foreign.conf": "keep"} if extra_data else {})}}

            def call(self, args, value, **kwargs):
                if args[0] != "replace" or "--force-conflicts" in args:
                    raise AssertionError("entry update must retain optimistic concurrency")
                metadata = value["metadata"]
                if metadata["uid"] != "entry-create-uid" or metadata["resourceVersion"] != "31" or metadata["labels"]["retained-label"] != "keep" or metadata["annotations"]["retained-annotation"] != "keep":
                    raise AssertionError("actual identity/version and external metadata must survive")
                return json.dumps(value)
        with tempfile.TemporaryDirectory() as temporary:
            cluster = ExistingEntry({"owner": "ani-lab", "kubeconfig": "/unused"}, pathlib.Path(temporary) / "attempt")
            cluster.apply([{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "ani-kfp-entry", "namespace": "kubeflow"}, "data": {"nginx.conf": NGINX}}])
            self.assertEqual(cluster.writes[0]["uid"], "entry-create-uid")

    def test_owned_entry_updates_with_uid_version_and_preserves_metadata(self):
        self.update()

    def test_foreign_data_manager_is_rejected_before_any_request(self):
        with self.assertRaisesRegex(RuntimeError, "foreign entry configuration"):
            self.update(manager="foreign-operator")

    def test_unknown_entry_data_cannot_be_erased(self):
        with self.assertRaisesRegex(RuntimeError, "foreign entry configuration"):
            self.update(extra_data=True)


class APIWorkflowConfigurationReplay(unittest.TestCase):
    def update(self, manager="ani-kubeflow"):
        class ExistingAPI(Cluster):
            def read(self, value):
                live = copy.deepcopy(value)
                live["metadata"].update(uid="api-create-uid", resourceVersion="37",
                    labels={"ani.io/managed-by": "ani-lab", "retained-label": "keep"},
                    annotations={"retained-annotation": "keep"},
                    managedFields=[{"manager": manager, "fieldsV1": {"f:spec": {}}},
                                   {"manager": "kube-controller-manager", "subresource": "status", "fieldsV1": {"f:status": {}}}])
                live["spec"]["template"]["spec"]["containers"][0]["env"][0]["value"] = "{}"
                return live

            def call(self, args, value, **kwargs):
                metadata = value["metadata"]
                if args[0] != "replace" or "--force-conflicts" in args or metadata["uid"] != "api-create-uid" or metadata["resourceVersion"] != "37" or metadata["annotations"]["retained-annotation"] != "keep":
                    raise AssertionError("compiler configuration update must retain identity/version and metadata")
                return json.dumps(value)
        with tempfile.TemporaryDirectory() as temporary:
            cluster = ExistingAPI({"owner": "ani-lab", "kubeconfig": "/unused"}, pathlib.Path(temporary) / "attempt")
            cluster.apply([{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "ml-pipeline", "namespace": "kubeflow"},
                "spec": {"template": {"spec": {"containers": [{"name": "api", "env": [{"name": "COMPILED_PIPELINE_SPEC_PATCH", "value": '{"podSpecPatch":"{}"}'}]}]}}}}])
            self.assertEqual(cluster.writes[0]["uid"], "api-create-uid")

    def test_owned_api_config_update_preserves_uid_version_and_controller_status_ownership(self):
        self.update()

    def test_foreign_api_spec_manager_is_rejected_before_any_request(self):
        with self.assertRaisesRegex(RuntimeError, "foreign API deployment spec manager"):
            self.update(manager="foreign-operator")


class TenantRunStopAuthorization(unittest.TestCase):
    def test_terminate_is_limited_to_runs_in_the_requested_tenant(self):
        role = next(v for v in tenant({}, "tenant-a", []) if v["kind"] == "Role" and v["metadata"]["name"] == "ani-kfp-api-client")
        self.assertEqual(role["metadata"]["namespace"], "tenant-a")
        self.assertEqual([r for r in role["rules"] if "terminate" in r["verbs"]],
                         [{"apiGroups": ["pipelines.kubeflow.org"], "resources": ["runs"], "verbs": ["terminate"]}])

    def update(self, manager="kubectl-create"):
        class ExistingRole(Cluster):
            def read(self, value):
                live = copy.deepcopy(value)
                live["rules"] = live["rules"][:-1]
                live["metadata"].update(uid="role-create-uid", resourceVersion="42",
                    labels={"ani.io/managed-by": "ani-lab"}, annotations={"retained": "keep"},
                    managedFields=[{"manager": manager, "fieldsV1": {"f:rules": {}}}])
                return live

            def call(self, args, value, **kwargs):
                if args[0] != "replace" or value["metadata"]["uid"] != "role-create-uid" or value["metadata"]["resourceVersion"] != "42" or value["metadata"]["annotations"]["retained"] != "keep":
                    raise AssertionError("tenant rules update must preserve identity/version and metadata")
                return json.dumps(value)
        with tempfile.TemporaryDirectory() as temporary:
            cluster = ExistingRole({"owner": "ani-lab", "tenants": ["tenant-a"], "kubeconfig": "/unused"}, pathlib.Path(temporary) / "attempt")
            role = next(v for v in tenant({}, "tenant-a", []) if v["kind"] == "Role" and v["metadata"]["name"] == "ani-kfp-api-client")
            cluster.apply([role])
            self.assertEqual(cluster.writes[0]["uid"], "role-create-uid")

    def test_old_owned_rules_use_uid_and_version(self):
        self.update()

    def test_foreign_rules_owner_rejected_before_request(self):
        with self.assertRaisesRegex(RuntimeError, "foreign tenant API rules manager"):
            self.update("another-operator")


class DeploymentPodConvergence(unittest.TestCase):
    def converge(self, first):
        healthy = {"metadata": {"uid": "new-pod", "name": "new"}, "spec": {"nodeName": "ani-03"},
                   "status": {"conditions": [{"type": "Ready", "status": "True"}]}}
        class Rolling(Cluster):
            def read(self, value):
                return {"metadata": {"uid": "api-uid", "generation": 3},
                    "spec": {"replicas": 1, "selector": {"matchLabels": {"app": "ml-pipeline"}}},
                    "status": {"observedGeneration": 3, "updatedReplicas": 1, "availableReplicas": 1, "readyReplicas": 1, "replicas": 1}}

            def call(self, args):
                self.lookups += 1
                return json.dumps({"items": first if self.lookups == 1 else [healthy]})
        with tempfile.TemporaryDirectory() as temporary:
            cluster = Rolling({"owner": "ani-lab", "kubeconfig": "/unused"}, pathlib.Path(temporary) / "attempt")
            cluster.lookups = 0
            with mock.patch("common.time.sleep") as sleep:
                observed = cluster.deployment({"kind": "Deployment", "metadata": {"namespace": "kubeflow", "name": "ml-pipeline"}})
                self.assertEqual(sleep.call_count, 1)
            self.assertEqual([p["uid"] for p in observed["pods"]], ["new-pod"])

    def test_terminating_old_pod_waits_for_current_ready_pod(self):
        self.converge([{"metadata": {"uid": "old-pod", "deletionTimestamp": "2026-10-03T12:32:00Z"},
                        "status": {"conditions": [{"type": "Ready", "status": "True"}]}}])

    def test_empty_pod_observation_does_not_accept_deployment_available(self):
        self.converge([])

    def test_unready_pod_waits_within_the_same_deadline(self):
        self.converge([{"metadata": {"uid": "new-pod"}, "status": {"conditions": [{"type": "Ready", "status": "False"}]}}])


class Stage2Protection(unittest.TestCase):
    def test_kserve_source_ca_uses_the_controller_namespace_and_upstream_filename(self):
        self.assertEqual(stage2.controller_ca("public-ca")["data"], {"cabundle.crt": "public-ca"})
        self.assertEqual(stage2.controller_ca()["metadata"]["namespace"], "kserve")
        workspace = stage2.namespace_contract({"workspace_max_size": "5Gi", **KUBEOVN_CAPABILITY}, stage2.NAMESPACES[0], "public-ca")
        self.assertEqual(next(v for v in workspace if v["kind"] == "ConfigMap")["data"], {"ca.crt": "public-ca"})

    def test_model_roles_cannot_reverse_their_storage_responsibility(self):
        writer = model_policy("bucket-a", "models", "model-writer")
        reader = model_policy("bucket-a", "models", "model-reader")
        def actions(policy):
            return {action for s in policy["Statement"] for action in s["Action"]}
        self.assertIn("s3:PutObject", actions(writer))
        self.assertNotIn("s3:GetObject", actions(writer))
        self.assertNotIn("s3:ListBucket", actions(writer))
        self.assertIn("s3:GetObject", actions(reader))
        self.assertNotIn("s3:PutObject", actions(reader))
        for policy in (writer, reader):
            object_scopes = [s["Resource"] for s in policy["Statement"] if any(v in s["Action"] for v in ("s3:GetObject", "s3:PutObject"))]
            self.assertEqual(object_scopes, [["arn:aws:s3:::bucket-a/models/*"]])

    def test_native_workload_contract_does_not_inherit_pipeline_permissions(self):
        site = {"workspace_class": "ani-cephfs", "workspace_max_size": "5Gi", "images": {stage2.WORKSPACE_IMAGE: "registry/jupyter@sha256:" + "a" * 64}, **KUBEOVN_CAPABILITY}
        pvc, notebook = stage2.notebook(site, stage2.NAMESPACES[0], "native", "case-a")
        pod = notebook["spec"]["template"]["spec"]
        self.assertFalse(pod["automountServiceAccountToken"])
        self.assertEqual(pod["serviceAccountName"], "notebook-workload")
        self.assertFalse(pvc["metadata"].get("ownerReferences"))
        secrets = {v["valueFrom"]["secretKeyRef"]["name"] for v in pod["containers"][0]["env"] if "valueFrom" in v}
        self.assertEqual(secrets, {"ani-jupyter-auth", "ani-model-writer"})
        values = stage2.namespace_contract(site, stage2.NAMESPACES[0])
        self.assertFalse(any(v["kind"] in ("Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding") for v in values))
        default = next(v for v in values if v["kind"] == "NetworkPolicy" and v["metadata"]["name"] == "ani-stage2-default")
        self.assertEqual(default["spec"]["ingress"], [])
        self.assertEqual({p["port"] for rule in default["spec"]["egress"] for p in rule["ports"]}, {53})


class MainProbeResume(unittest.TestCase):
    def test_predictor_delete_refuses_replaced_uid_before_dry_run_or_mutation(self):
        delete = runpy.run_path(str(pathlib.Path(__file__).resolve().parents[1] / "ani/kubeflow/stage2/probe-contracts.py"))["delete_owned_pod"]
        cluster = mock.Mock()
        cluster.owned.return_value = {"metadata": {"uid": "replacement", "resourceVersion": "19"}}
        with self.assertRaisesRegex(ValueError, "identity differs"):
            delete(cluster, {"kind": "Pod", "metadata": {"name": "predictor", "namespace": stage2.NAMESPACES[0]}}, "original")
        cluster.call.assert_not_called()

    def test_prediction_reconciliation_rejects_another_cluster_before_any_workload_call(self):
        reconcile = runpy.run_path(str(pathlib.Path(__file__).resolve().parents[1] / "ani/kubeflow/stage2/probe-main.py"))["reconcile_prediction"]
        cluster = mock.Mock()
        previous = {"status": "FAIL", "run_id": "case-a", "namespace": stage2.NAMESPACES[0], "cluster_uid": "other-cluster",
                    "kernel": {"status": "REAL_JUPYTER_KERNEL_MODEL_UPLOADED"}, "s3": {"status": "PASS"},
                    "inference_service": {"uid": "original-is"}}
        with self.assertRaisesRegex(ValueError, "original cluster"):
            reconcile(cluster, "case-a", previous, {"cluster_uid": "current-cluster"})
        cluster.owned.assert_not_called()
        cluster.call.assert_not_called()

    def test_resume_image_change_is_conditioned_and_does_not_replace_atomic_containers(self):
        change = runpy.run_path(str(pathlib.Path(__file__).resolve().parents[1] / "ani/kubeflow/stage2/probe-main.py"))["resume_notebook_image"]
        with tempfile.TemporaryDirectory() as temporary:
            cluster = Cluster({"owner": "ani-lab", "kubeconfig": "/unused"}, pathlib.Path(temporary) / "attempt")
            live = {"apiVersion": "kubeflow.org/v1", "kind": "Notebook", "metadata": {"name": "native", "namespace": stage2.NAMESPACES[0],
                    "uid": "original-notebook", "resourceVersion": "17", "labels": {"ani.io/managed-by": "ani-lab"}},
                    "spec": {"template": {"spec": {"containers": [{"name": "native", "image": "old@sha256:aaa"}]}}}}
            desired = copy.deepcopy(live); desired["spec"]["template"]["spec"]["containers"][0]["image"] = "fixed@sha256:bbb"
            def server(args, **kwargs):
                self.assertEqual(args[0], "patch")
                patch = json.loads(args[args.index("--patch") + 1])
                self.assertEqual([v["path"] for v in patch if v["op"] == "replace"], ["/spec/template/spec/containers/0/image"])
                self.assertEqual(patch[0], {"op": "test", "path": "/metadata/uid", "value": "original-notebook"})
                self.assertEqual(patch[1], {"op": "test", "path": "/metadata/resourceVersion", "value": "17"})
                return json.dumps(desired)
            with mock.patch.object(cluster, "read", return_value=live), mock.patch.object(cluster, "call", side_effect=server):
                change(cluster, desired)
            self.assertEqual(cluster.writes, [{"identity": "Notebook/ani-kf-stage2-a/native", "uid": "original-notebook", "action": "patch-approved-image", "result": "CONFIRMED"}])

    def test_resume_keeps_original_uids_and_rejects_replacement_or_kernel_outputs(self):
        resume = runpy.run_path(str(pathlib.Path(__file__).resolve().parents[1] / "ani/kubeflow/stage2/probe-main.py"))["resume_workspace"]
        site = {"workspace_class": "ani-cephfs", "workspace_max_size": "5Gi", "images": {stage2.WORKSPACE_IMAGE: "registry/jupyter@sha256:" + "a" * 64}}
        pvc, notebook = stage2.notebook(site, stage2.NAMESPACES[0], "native", "case-a")
        claim, native = copy.deepcopy(pvc), copy.deepcopy(notebook)
        claim["metadata"]["uid"], native["metadata"]["uid"] = "original-pvc", "original-notebook"
        claim["status"] = {"phase": "Bound"}
        cluster = mock.Mock()
        cluster.owned.side_effect = lambda value: claim if value["kind"] == "PersistentVolumeClaim" else native
        previous = {"status": "FAIL", "namespace": stage2.NAMESPACES[0], "run_id": "case-a",
                    "workspace": {"name": pvc["metadata"]["name"], "uid": claim["metadata"]["uid"]},
                    "notebook": {"name": "native", "uid": native["metadata"]["uid"]}}
        self.assertEqual(resume(cluster, previous, stage2.NAMESPACES[0], "case-a", pvc, notebook)["metadata"]["uid"], "original-pvc")
        for key in ("workspace", "notebook"):
            wrong = copy.deepcopy(previous); wrong[key]["uid"] = "replacement"
            with self.assertRaisesRegex(ValueError, "identity differs"):
                resume(cluster, wrong, stage2.NAMESPACES[0], "case-a", pvc, notebook)
        with self.assertRaisesRegex(ValueError, "pre-kernel"):
            resume(cluster, {**previous, "kernel": {}}, stage2.NAMESPACES[0], "case-a", pvc, notebook)


class ExplicitResourceScope(unittest.TestCase):
    def test_kserve_configuration_update_requires_our_original_field_owner_and_version(self):
        value = {"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "inferenceservice-config", "namespace": "kserve"},
                 "data": {"credentials": "reviewed-ca-filename"}}
        old = copy.deepcopy(value); old["data"]["credentials"] = "old-ca-filename"
        old["metadata"].update(uid="original-config", resourceVersion="19", labels={"ani.io/managed-by": "ani-lab"},
                               managedFields=[{"manager": "ani-kubeflow", "fieldsV1": {"f:data": {}}}])
        with tempfile.TemporaryDirectory() as temporary:
            cluster = Cluster({"owner": "ani-lab", "kubeconfig": "/unused"}, pathlib.Path(temporary) / "attempt")
            def server(args, body, **kwargs):
                self.assertEqual(args[0], "replace")
                self.assertEqual(body["metadata"]["uid"], "original-config")
                self.assertEqual(body["metadata"]["resourceVersion"], "19")
                self.assertNotIn("--force-conflicts", args)
                return json.dumps(body)
            with mock.patch.object(cluster, "read", return_value=old), mock.patch.object(cluster, "call", side_effect=server):
                cluster.apply([value])
            old["metadata"]["managedFields"][0]["manager"] = "another-owner"
            with mock.patch.object(cluster, "read", return_value=old), mock.patch.object(cluster, "call") as call:
                with self.assertRaisesRegex(RuntimeError, "foreign KServe configuration"):
                    cluster.apply([value])
                call.assert_not_called()

    def test_replay_preserves_controller_computed_aggregate_rules(self):
        value = {"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "metadata": {"name": "notebook-admin"},
                 "aggregationRule": {"clusterRoleSelectors": [{"matchLabels": {"aggregate": "true"}}]}, "rules": []}
        with tempfile.TemporaryDirectory() as temporary:
            cluster = Cluster({"owner": "ani-lab", "kubeconfig": "/unused"}, pathlib.Path(temporary) / "attempt")
            live = {**copy.deepcopy(value), "rules": [{"apiGroups": ["kubeflow.org"], "resources": ["notebooks"], "verbs": ["get"]}]}
            live["metadata"].update(uid="aggregate-role-uid", resourceVersion="17", labels={"ani.io/managed-by": "ani-lab"})
            def server(args, body, **kwargs):
                if "rules" in body:
                    raise RuntimeError("conflict with clusterrole-aggregation-controller")
                self.assertEqual(body["aggregationRule"], live["aggregationRule"])
                return json.dumps(live)
            with mock.patch.object(cluster, "read", return_value=live), mock.patch.object(cluster, "call", side_effect=server):
                cluster.apply([value])
            self.assertEqual(cluster.writes[0]["uid"], live["metadata"]["uid"])

    def test_missing_controller_identity_namespace_is_rejected_before_lookup(self):
        for kind, api in (("ServiceAccount", "v1"), ("Role", "rbac.authorization.k8s.io/v1"), ("RoleBinding", "rbac.authorization.k8s.io/v1")):
            with self.assertRaisesRegex(ValueError, "requires explicit namespace"):
                endpoint({"apiVersion": api, "kind": kind, "metadata": {"name": "controller"}})

    def test_same_named_service_accounts_resolve_to_different_scopes(self):
        value = {"apiVersion": "v1", "kind": "ServiceAccount", "metadata": {"name": "controller", "namespace": "kserve"}}
        self.assertEqual(endpoint(value), "/api/v1/namespaces/kserve/serviceaccounts/controller")
        value["metadata"]["namespace"] = "default"
        self.assertEqual(endpoint(value), "/api/v1/namespaces/default/serviceaccounts/controller")


class PipelineStorageInitializationReads(unittest.TestCase):
    class ApiError(Exception):
        def __init__(self, status, headers=None):
            self.status, self.headers = status, headers

    def wrapper(self, original):
        path = pathlib.Path(__file__).resolve().parents[1] / "ani/kubeflow/probes/pipeline.py"
        component = next(n for n in ast.parse(path.read_text()).body
                         if isinstance(n, ast.FunctionDef) and n.name == "external_training")
        function = next(n for n in component.body if isinstance(n, ast.FunctionDef) and n.name == "bounded")
        clock = mock.Mock()
        scope = {"original_call": original, "ApiException": self.ApiError, "time": clock}
        exec(compile(ast.Module(body=[function], type_ignores=[]), str(path), "exec"), scope)
        return scope["bounded"], clock

    def test_jobset_get_recovers_from_actual_storage_initializing_response(self):
        response = {"items": [{"metadata": {"uid": "original-jobset-uid"}}]}
        call = mock.Mock(side_effect=[self.ApiError(429, {"Retry-After": "1"}), response])
        bounded, clock = self.wrapper(call)
        self.assertIs(bounded("/apis/jobset.x-k8s.io/v1alpha2/namespaces/probe/jobsets", "GET"), response)
        self.assertEqual(call.call_count, 2)
        clock.sleep.assert_called_once_with(1)
        self.assertTrue(all(c.kwargs["_request_timeout"] == (5, 30) for c in call.call_args_list))

    def test_persistent_requests_are_never_replayed_on_429(self):
        for method in ("POST", "PUT", "PATCH", "DELETE"):
            error = self.ApiError(429, {"Retry-After": "1"})
            call = mock.Mock(side_effect=error)
            bounded, clock = self.wrapper(call)
            with self.assertRaises(self.ApiError) as raised:
                bounded("/apis/trainer.kubeflow.org/v1alpha1/namespaces/probe/trainjobs", method)
            self.assertIs(raised.exception, error)
            self.assertEqual(call.call_count, 1)
            clock.sleep.assert_not_called()

    def test_authorization_and_other_read_failures_are_not_retried(self):
        for status in (401, 403, 404, 500):
            call = mock.Mock(side_effect=self.ApiError(status))
            bounded, clock = self.wrapper(call)
            with self.assertRaises(self.ApiError):
                bounded("/original-resource", "GET")
            self.assertEqual(call.call_count, 1)
            clock.sleep.assert_not_called()

    def test_read_initialization_has_a_finite_attempt_limit(self):
        error = self.ApiError(429, {"Retry-After": "120"})
        call = mock.Mock(side_effect=error)
        bounded, clock = self.wrapper(call)
        with self.assertRaises(self.ApiError) as raised:
            bounded("/original-resource", "GET")
        self.assertIs(raised.exception, error)
        self.assertEqual(call.call_count, 5)
        self.assertEqual(clock.sleep.call_args_list, [mock.call(5)] * 4)

    def test_successful_read_preserves_caller_timeout_and_arguments(self):
        response = object()
        call = mock.Mock(return_value=response)
        bounded, clock = self.wrapper(call)
        self.assertIs(bounded("/original-resource", "GET", _request_timeout=(2, 9), _preload_content=False), response)
        call.assert_called_once_with("/original-resource", "GET", _request_timeout=(2, 9), _preload_content=False)
        clock.sleep.assert_not_called()


if __name__ == "__main__":
    unittest.main()

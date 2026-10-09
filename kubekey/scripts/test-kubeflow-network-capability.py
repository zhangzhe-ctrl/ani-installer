#!/usr/bin/env python3
"""Provider capability must survive the real runtime site boundary."""
import json
import copy
import ast
import contextlib
import errno
import io
import pathlib
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1] / "ani/kubeflow"))
from common import load_site, Cluster
from resources import RELEASE, isolation, network_resources, obj, NETWORK_IMAGE_DIGESTS, NETWORK_IMAGE_PINS
import stage2


class OrdinaryWorkloadCapability(unittest.TestCase):
    def probe(self, policy, control_error=None, api_status=403, api_error=None):
        path = pathlib.Path(__file__).resolve().parents[2] / "docs/execution/kf-env-p00/scripts/check-workload-boundary.py"
        tree = ast.parse(path.read_text())
        code = next(n.value.value for n in ast.walk(tree) if isinstance(n, ast.Assign)
                    and any(isinstance(t, ast.Name) and t.id == "command" for t in n.targets)
                    and isinstance(n.value, ast.Constant))
        target = {"name": "ml-pipeline", "address": "192.0.2.20", "port": 8888}
        api = {"name": "kubernetes", "address": "10.96.0.1", "port": 443}
        response = mock.Mock(status=api_status)
        client = mock.Mock()
        client.getresponse.return_value = response
        client.request.side_effect = api_error
        stream = io.StringIO()
        argv = ["probe", json.dumps([target]), api["address"], "public-ca", json.dumps([api]),
                "offline-image", "ani-kfp-a", policy]
        with mock.patch.object(sys, "argv", argv), mock.patch.dict("os.environ", {}, clear=True), \
                mock.patch("pathlib.Path.exists", return_value=False), \
                mock.patch("socket.getaddrinfo", return_value=[(None, None, None, None, (api["address"], 443))]), \
                mock.patch("socket.create_connection", side_effect=control_error, return_value=mock.MagicMock()), \
                mock.patch("ssl.create_default_context"), \
                mock.patch("http.client.HTTPSConnection", return_value=client), contextlib.redirect_stdout(stream):
            exec(compile(code, str(path), "exec"), {})
        return json.loads(stream.getvalue())

    def test_kcn_records_reachability_and_actual_api_authorization_denial(self):
        evidence = self.probe("unsupported")
        self.assertEqual(evidence["targets"][0]["result"], "REACHABLE_NETWORKPOLICY_UNSUPPORTED")
        self.assertEqual(evidence["networkIsolation"], "unsupported")
        self.assertEqual(evidence["kubernetesCreateDryRuns"][0]["httpStatus"], 403)
        for arguments in ({"api_status": 200}, {"api_error": TimeoutError()}, {"control_error": TimeoutError()}):
            with self.subTest(arguments=arguments), self.assertRaises((AssertionError, TimeoutError)):
                self.probe("unsupported", **arguments)

    def test_kubeovn_still_requires_healthy_control_denial(self):
        evidence = self.probe("required", control_error=TimeoutError())
        self.assertEqual(evidence["targets"][0]["result"], "TIMEOUT_DENIED")
        self.assertEqual(evidence["networkIsolation"], "denial_verified")
        with self.assertRaisesRegex(RuntimeError, "reached control port"):
            self.probe("required")
        with self.assertRaisesRegex(AssertionError, "unavailable target"):
            self.probe("required", control_error=OSError(errno.ECONNREFUSED, "not ready"))


class NetworkCapability(unittest.TestCase):
    def test_kubeovn_source_list_is_not_its_running_amd64_manifest(self):
        # The approved 82cd source list selects 2b505 for linux/amd64. These
        # are the actual shapes observed from the formally installed provider.
        platform = "sha256:2b505e4dab411036f21985d765ec25fd2c5aff46521a96cf5e5dc964f302e805"
        ref = "192.0.2.11:5001/kubeovn/kube-ovn:v1.16.6"
        site = {"network_stack": "kubeovn", "network_policy": "required",
                "network_policy_contract": "kubeovn-required-v1", "registry": "192.0.2.11:5001",
                "network_image": ref + "@" + platform, "node_addresses": ["192.0.2.11", "192.0.2.12", "192.0.2.13"]}
        workloads = {}
        for kind, name, desired in (("daemonset", "kube-ovn-cni", 3), ("deployment", "kube-ovn-controller", 1)):
            workloads[kind] = {"metadata": {"name": name, "uid": name, "generation": 1},
                "spec": {"replicas": desired, "selector": {"matchLabels": {"app": name}},
                         "template": {"spec": {"containers": [{"name": "provider", "image": ref, "args": ["--enable-np=true"]}]}}},
                "status": {"observedGeneration": 1, "desiredNumberScheduled": desired, "numberReady": desired, "readyReplicas": desired}}
        pods = lambda count: {"items": [{"metadata": {}, "status": {"containerStatuses": [
            {"name": "provider", "ready": True, "imageID": ref.split(":v1")[0] + "@" + platform}]}} for _ in range(count)]}
        c = Cluster.__new__(Cluster)
        c.site = site
        def call(args):
            if args[1] in workloads: return json.dumps(workloads[args[1]])
            if args[1] == "pods": return json.dumps(pods(3 if "app=kube-ovn-cni" in args else 1))
            return '{"items": []}'
        c.call = call
        self.assertEqual(c.network_capability()["networkPolicy"], "required")
        # A tag alone cannot certify the image; every live imageID is checked.
        original = copy.deepcopy(workloads)
        workloads["deployment"]["spec"]["template"]["spec"]["containers"][0]["args"] = []
        with self.assertRaisesRegex(ValueError, "policy controller enabled"): c.network_capability()
        workloads = original
        for rejected in (NETWORK_IMAGE_PINS["kubeovn"], "sha256:" + "0" * 64):
            wrong = pods(3)
            wrong["items"][0]["status"]["containerStatuses"][0]["imageID"] = ref + "@" + rejected
            c.call = lambda args, value=wrong: json.dumps(value) if args[1] == "pods" else call(args)
            with self.assertRaisesRegex(ValueError, "runtime imageID"): c.network_capability()

    def load(self, **capability):
        with tempfile.TemporaryDirectory() as temporary:
            site = {"release": RELEASE, "workspace_mode": "managed-execution-pvc-v1",
                    "registry": "192.0.2.11:5001", "images": {}, **capability}
            site.setdefault("network_image", "192.0.2.11:5001/cni:fixed@" + NETWORK_IMAGE_DIGESTS.get(site.get("network_stack"), "sha256:" + "0" * 64))
            site.update({key: temporary for key in ("kubeconfig", "artifact_root", "logs_dir", "connections_dir")})
            path = pathlib.Path(temporary) / "site.json"
            path.write_text(json.dumps(site))
            return load_site(path)

    def test_missing_or_forged_provider_capability_fails(self):
        cases = ({}, {"network_stack": "kcn"},
                 {"network_stack": "kcn", "network_policy": "required", "network_policy_contract": "kubeovn-required-v1"},
                 {"network_stack": "kubeovn", "network_policy": "unsupported", "network_policy_contract": "kcn-test-unsupported-v1"},
                 {"network_stack": "kcn", "network_policy": "unsupported", "network_policy_contract": "skip"})
        for capability in cases:
            with self.subTest(capability=capability), self.assertRaisesRegex(ValueError, "network capability"):
                self.load(**capability)

    def test_forged_image_binding_fails_even_with_an_exact_capability_contract(self):
        with self.assertRaisesRegex(ValueError, "network capability image"):
            self.load(network_stack="kcn", network_policy="unsupported", network_policy_contract="kcn-test-unsupported-v1",
                      network_image="192.0.2.11:5001/fake:fixed@sha256:" + "0" * 64)

    def test_source_image_capabilities_match_the_formal_image_table(self):
        root = pathlib.Path(__file__).resolve().parents[1]
        rows = [line.split("\t") for line in (root / "ani/images.tsv").read_text().splitlines()]
        digests = {row[0]: row[2] for row in rows if len(row) == 4}
        self.assertEqual(digests["docker.io/kubeovn/kube-ovn:v1.16.6"], NETWORK_IMAGE_PINS["kubeovn"])
        self.assertEqual(digests["docker.changqingyun.cn/kubercloud/kc-networking@sha256:494432d2f7b896eb953647166c6aa18d6ea2113b133d40d82127cfcbe416712f"], NETWORK_IMAGE_DIGESTS["kcn"])

    def test_both_exact_contracts_load(self):
        for provider, policy, contract in (("kcn", "unsupported", "kcn-test-unsupported-v1"),
                                          ("kubeovn", "required", "kubeovn-required-v1")):
            self.assertEqual(self.load(network_stack=provider, network_policy=policy,
                                       network_policy_contract=contract)["network_policy"], policy)

    def test_kcn_keeps_identity_and_quota_without_claiming_network_policy(self):
        for provider, policy, contract in (("kcn", "unsupported", "kcn-test-unsupported-v1"),
                                          ("kubeovn", "required", "kubeovn-required-v1")):
            site = {"network_stack": provider, "network_policy": policy, "network_policy_contract": contract,
                    "workspace_max_size": "5Gi", "tenants": ["ani-kfp-a"],
                    "node_addresses": ["192.0.2.11"], "kubernetes_service_ip": "10.96.0.1"}
            values = stage2.namespace_contract(site, "ani-kf-stage2-a")
            self.assertEqual({v["kind"] for v in values if v["kind"] != "NetworkPolicy"},
                             {"Namespace", "ServiceAccount", "ResourceQuota", "LimitRange"})
            policies = isolation(site) + [v for v in values if v["kind"] == "NetworkPolicy"]
            self.assertEqual(bool(policies), provider == "kubeovn")
            independent = [obj("Role", "probe", "ani-kfp-a"), obj("Secret", "probe", "ani-kfp-a"),
                           obj("ValidatingAdmissionPolicy", "probe", api="admissionregistration.k8s.io/v1")]
            self.assertEqual(network_resources(site, independent), independent)


if __name__ == "__main__":
    unittest.main()

#!/usr/bin/env python3
"""Provider capability must survive the real runtime site boundary."""
import json
import pathlib
import sys
import tempfile
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1] / "ani/kubeflow"))
from common import load_site
from resources import RELEASE, isolation, network_resources, obj, NETWORK_IMAGE_DIGESTS
import stage2


class NetworkCapability(unittest.TestCase):
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
        self.assertEqual(digests["docker.io/kubeovn/kube-ovn:v1.16.6"], NETWORK_IMAGE_DIGESTS["kubeovn"])
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

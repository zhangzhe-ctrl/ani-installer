#!/usr/bin/env python3
"""Lab-only actual PVC admission positives/negatives, without persistent writes."""
import argparse
import copy
import hashlib
import json
import pathlib
import sys

parser = argparse.ArgumentParser()
parser.add_argument("--site", required=True)
parser.add_argument("--cluster-uid", required=True)
parser.add_argument("--output", required=True)
args = parser.parse_args()
site = json.loads(pathlib.Path(args.site).read_text())
material = pathlib.Path(site["artifact_root"]) / "manifests/kubeflow" / site["release"]
sys.path.insert(0, str(material))
from common import Cluster, assets, atomic, load_site
from resources import obj

assets(material)
site = load_site(args.site)
if site["workspace_max_size"] != "5Gi":
    raise ValueError("this bounded lab matrix requires the reviewed 5Gi maximum")
output = pathlib.Path(args.output)
if not output.is_absolute():
    raise ValueError("absolute output required")
cluster = Cluster(site, output)
report = {"schema": "ani.kubeflow.workspace-dryrun.v1", "status": "IN_PROGRESS", "persistentRequests": 0,
          "sourceSha256": hashlib.sha256(pathlib.Path(__file__).read_bytes()).hexdigest(), "requests": [],
          "acceptance": "EAC_NOT_ATTESTED"}
atomic(output / "report.json", report)
try:
    if cluster.read(obj("Namespace", "kube-system"))["metadata"]["uid"] != args.cluster_uid:
        raise RuntimeError("cluster identity differs")
    for namespace in site["tenants"]:
        positive = obj("PersistentVolumeClaim", "ani-kfp-workspace-admission-dryrun", namespace,
                       spec={"storageClassName": site["workspace_class"], "accessModes": ["ReadWriteMany"],
                             "resources": {"requests": {"storage": "5Gi"}}})
        cases = [("approved", positive, None)]
        for name, change, message in (
            ("oversized", lambda v: v["spec"]["resources"]["requests"].update(storage="6Gi"), "Workspace storage exceeds"),
            ("other-class", lambda v: v["spec"].update(storageClassName="unapproved-class"), "Workspace StorageClass"),
            ("other-name", lambda v: v["metadata"].update(name="unmanaged-workspace"), "Use a managed"),
            ("other-mode", lambda v: v["spec"].update(accessModes=["ReadWriteOnce"]), "Workspace requires declared RWX"),
            ("workflow-gc", lambda v: v["metadata"].update(ownerReferences=[{
                "apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "name": "dryrun-owner",
                "uid": "00000000-0000-4000-8000-000000000001"}]), "Workspace must survive"),
        ):
            value = copy.deepcopy(positive)
            change(value)
            cases.append((name, value, message))
        for name, value, denied in cases:
            try:
                response = cluster.call(["create", "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json"], value)
            except RuntimeError as error:
                text = str(error)
                if denied is None or "ani-kfp-workspace" not in text or denied not in text:
                    raise
                report["requests"].append({"namespace": namespace, "case": name, "result": "POLICY_DENIED", "message": denied})
            else:
                if denied is not None:
                    raise RuntimeError("invalid workspace was accepted: " + name)
                report["requests"].append({"namespace": namespace, "case": name, "result": "ACCEPTED",
                                           "responseSha256": hashlib.sha256(response.encode()).hexdigest()})
            atomic(output / "report.json", report)
    report["status"] = "CURRENT_WORKSPACE_ADMISSION_VERIFIED"
except BaseException as error:
    report.update(status="FAIL", error=str(error))
    atomic(output / "report.json", report)
    raise
atomic(output / "report.json", report)
print(json.dumps({"status": report["status"], "requests": len(report["requests"]), "persistentRequests": 0}))

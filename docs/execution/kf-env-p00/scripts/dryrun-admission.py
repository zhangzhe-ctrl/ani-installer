#!/usr/bin/env python3
"""Lab-only actual API preflight of the role's entire admission dependency group.

Calls the same role prepare method. Sends no persistent resource mutation and
does not attest admission behavior/type-controller or environment acceptance.
"""
import argparse
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
from common import Cluster, assets, atomic, identity, load_site
from resources import admission, obj, runtime

assets(material)
site = load_site(args.site)
output = pathlib.Path(args.output)
if not output.is_absolute():
    raise ValueError("absolute task output required")


class ReadOnlyPreflight(Cluster):
    def __init__(self, *values):
        super().__init__(*values)
        self.requests = []

    def call(self, arguments, value=None, **keywords):
        if "--dry-run=server" not in arguments or arguments[0] not in ("create", "apply", "replace"):
            raise RuntimeError("preflight refused a persistent or unsupported request")
        response = super().call(arguments, value, **keywords)
        self.requests.append({"identity": identity(value), "operation": arguments[0],
                              "responseSha256": hashlib.sha256(response.encode()).hexdigest()})
        return response


cluster = ReadOnlyPreflight(site, output)
report = {"schema": "ani.kubeflow.admission-dryrun.v1", "status": "IN_PROGRESS", "persistentRequests": 0,
          "sourceSha256": hashlib.sha256(pathlib.Path(__file__).read_bytes()).hexdigest(), "acceptance": "EAC_NOT_ATTESTED"}
atomic(output / "report.json", report)
try:
    actual = cluster.read(obj("Namespace", "kube-system"))["metadata"]["uid"]
    if actual != args.cluster_uid:
        raise RuntimeError("cluster identity differs")
    approved = runtime(site["images"])
    prepared = cluster.prepare(admission(site, approved["metadata"]["name"], site["images"]["ani.local/kubeflow-execution:26.03-v1"]))
    if len(prepared) != 10 or len(cluster.requests) != 10 or cluster.writes:
        raise RuntimeError("the two-tenant admission group is incomplete or mutated")
    report.update(status="CURRENT_SERVER_DRY_RUN_ACCEPTED", clusterUid=actual, objects=cluster.requests)
    atomic(output / "report.json", report)
except BaseException as error:
    report.update(status="FAIL", error=str(error), objects=cluster.requests)
    atomic(output / "report.json", report)
    raise
print(json.dumps({"status": report["status"], "requests": len(cluster.requests), "report": str(output / "report.json")}))

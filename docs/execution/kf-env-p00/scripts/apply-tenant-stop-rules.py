#!/usr/bin/env python3
"""Lab-only call of the existing first-install tenant Role resources."""
import argparse
import json
import pathlib
import sys

parser = argparse.ArgumentParser()
parser.add_argument("--site", required=True)
parser.add_argument("--output", required=True)
args = parser.parse_args()
site = json.loads(pathlib.Path(args.site).read_text())
material = pathlib.Path(site["artifact_root"]) / "manifests/kubeflow" / site["release"]
sys.path.insert(0, str(material))
from common import Cluster, assets, atomic, load_site
from install import product_lock
from resources import tenant

assets(material)
site = load_site(args.site)
output = pathlib.Path(args.output)
if not output.is_absolute():
    raise ValueError("absolute independent output required")
cluster = Cluster(site, output)
report = {"status": "IN_PROGRESS", "lock": product_lock(), "scope": "existing first-install tenant API Roles only"}
atomic(output / "report.json", report)
try:
    roles = [next(v for v in tenant(site, namespace, []) if v["kind"] == "Role" and v["metadata"]["name"] == "ani-kfp-api-client") for namespace in site["tenants"]]
    cluster.apply(roles)
    for role in roles:
        actual = cluster.owned(role)
        if actual["rules"] != role["rules"]:
            raise RuntimeError("actual tenant stop rules differ")
    report.update(status="TENANT_STOP_RULES_CHECKED", writes=cluster.writes)
except BaseException as error:
    report.update(status="FAIL", error=str(error), writes=cluster.writes)
    atomic(output / "report.json", report)
    raise
atomic(output / "report.json", report)
print(json.dumps({"status": report["status"], "writes": len(cluster.writes)}))

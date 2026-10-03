#!/usr/bin/env python3
"""Lab acceptance: one graceful restart of this task's dedicated KFP MySQL.

Never restart a shared database or delete/rebuild a Pod. Read identical KFP
Run and MLMD artifact/event records before and after the container restart.
"""
import argparse
import hashlib
import json
import pathlib
import sys
import uuid

parser = argparse.ArgumentParser()
parser.add_argument("--site", required=True)
parser.add_argument("--private-success-report", required=True)
parser.add_argument("--expected-deployment-uid", required=True)
parser.add_argument("--expected-pvc-uid", required=True)
parser.add_argument("--output", required=True)
args = parser.parse_args()
site = json.loads(pathlib.Path(args.site).read_text())
material = pathlib.Path(site["artifact_root"]) / "manifests/kubeflow" / site["release"]
sys.path.insert(0, str(material))
from common import Cluster, assets, atomic, load_site
from install import product_lock
from resources import obj

assets(material)
site = load_site(args.site)
output = pathlib.Path(args.output)
if not output.is_absolute():
    raise ValueError("independent absolute output required")
cluster = Cluster(site, output)
report = {"status": "IN_PROGRESS", "lock": product_lock(), "scope": "dedicated kubeflow/mysql container only",
          "sourceSha256": hashlib.sha256(pathlib.Path(__file__).read_bytes()).hexdigest()}
atomic(output / "report.json", report)
try:
    record = json.loads(pathlib.Path(args.private_success_report).read_text())
    run_id = str(uuid.UUID(record["run"]["id"]))
    if record["status"] != "RUN_STATE_CORRELATED" or record["mode"] != "success" or record["run"]["state"] != "SUCCEEDED" or record["namespace"] not in site["tenants"]:
        raise ValueError("confirmed successful execution required")
    deployment = obj("Deployment", "mysql", "kubeflow", api="apps/v1")
    current = cluster.owned(deployment)
    if current["metadata"]["uid"] != args.expected_deployment_uid or current["spec"].get("replicas") != 1:
        raise ValueError("dedicated database creation UID differs")
    ready = cluster.deployment(deployment)
    if len(ready["pods"]) != 1:
        raise ValueError("dedicated database Pod count differs")
    pods = json.loads(cluster.call(["get", "pods", "-n", "kubeflow", "-l", "app=mysql", "-o", "json"]))["items"]
    if len(pods) != 1 or pods[0]["metadata"]["uid"] != ready["pods"][0]["uid"]:
        raise ValueError("current dedicated database Pod identity differs")
    pod = pods[0]
    claims = [v["persistentVolumeClaim"]["claimName"] for v in pod["spec"]["volumes"] if "persistentVolumeClaim" in v]
    if claims != ["mysql-pv-claim"]:
        raise ValueError("dedicated database claim differs")
    pvc = obj("PersistentVolumeClaim", claims[0], "kubeflow")
    if cluster.owned(pvc)["metadata"]["uid"] != args.expected_pvc_uid:
        raise ValueError("dedicated database PVC creation UID differs")
    states = pod["status"]["containerStatuses"]
    if len(states) != 1 or states[0]["name"] != "mysql" or not states[0]["ready"]:
        raise ValueError("dedicated database container differs")
    original = states[0]
    pod_ref = obj("Pod", pod["metadata"]["name"], "kubeflow")
    query = ("SELECT UUID,Namespace,State FROM mlpipeline.run_details WHERE UUID='" + run_id + "'; "
             "SELECT a.id,a.uri,e.execution_id,e.type FROM metadb.Artifact a "
             "JOIN metadb.Event e ON e.artifact_id=a.id JOIN metadb.Attribution at ON at.artifact_id=a.id "
             "JOIN metadb.Context c ON c.id=at.context_id WHERE c.name='" + run_id + "' ORDER BY a.id,e.execution_id,e.type;")
    def snapshot():
        return cluster.call(["exec", pod_ref["metadata"]["name"], "-n", "kubeflow", "-c", "mysql", "--", "sh", "-c",
            'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql --protocol=TCP -h127.0.0.1 -uroot -N -e "$1"', "sh", query], sensitive=True)
    before = snapshot()
    if run_id + "\t" + record["namespace"] + "\tSUCCEEDED" not in before or "/model\t" not in before or "/link\t" not in before:
        raise RuntimeError("actual KFP/MLMD records missing before restart")
    atomic(output / "before-records.json", {"runId": run_id, "rows": before.splitlines()})
    report.update(restart={"result": "UNKNOWN", "podUid": pod["metadata"]["uid"], "originalContainerId": original["containerID"],
                           "originalRestartCount": original["restartCount"], "deploymentUid": args.expected_deployment_uid, "pvcUid": args.expected_pvc_uid})
    atomic(output / "report.json", report)
    cluster.call(["exec", pod_ref["metadata"]["name"], "-n", "kubeflow", "-c", "mysql", "--", "sh", "-c",
                  'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysqladmin --protocol=TCP -h127.0.0.1 -uroot shutdown'], sensitive=True)
    report["restart"]["requestResult"] = "CONFIRMED"
    atomic(output / "report.json", report)
    def restarted(value):
        if value["metadata"]["uid"] != pod["metadata"]["uid"]:
            raise RuntimeError("database Pod was replaced; this test authorized only a container restart")
        actual = value["status"].get("containerStatuses", [])
        if len(actual) == 1 and actual[0]["restartCount"] > original["restartCount"] + 1:
            raise RuntimeError("database container restarted more than once")
        return (len(actual) == 1 and actual[0]["restartCount"] == original["restartCount"] + 1
                and actual[0].get("containerID") != original["containerID"] and actual[0]["ready"])
    live = cluster.wait(pod_ref, restarted, timeout=600)
    after = snapshot()
    if after != before or cluster.owned(pvc)["metadata"]["uid"] != args.expected_pvc_uid:
        raise RuntimeError("actual persisted records or PVC identity differ after restart")
    atomic(output / "after-records.json", {"runId": run_id, "rows": after.splitlines()})
    report["restart"].update(result="CONFIRMED", actualRestartCount=live["status"]["containerStatuses"][0]["restartCount"], actualContainerId=live["status"]["containerStatuses"][0]["containerID"])
    report.update(status="DEDICATED_DB_RECORDS_PERSISTED", recordsSha256=hashlib.sha256(before.encode()).hexdigest(), recordCount=len(before.splitlines()), runId=run_id)
except BaseException as error:
    report.update(status="FAIL", error=str(error))
    atomic(output / "report.json", report)
    raise
atomic(output / "report.json", report)
print(json.dumps({"status": report["status"], "report": str(output / "report.json")}))

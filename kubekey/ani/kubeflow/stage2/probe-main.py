#!/usr/bin/env python3
"""Administrator-only native component probe; never mounted into a workload.

This creates task objects through the same reviewed resource contracts as the
installer. Training runs solely in the native Jupyter kernel via its protocol.
The independent S3 hash runs in a tokenless Pod with the read-only model role.
Failures retain resources and the first cause; this script does not reset them.
"""
import argparse
import datetime
import hashlib
import json
import os
import pathlib
import re
import subprocess
import time
import urllib.request

from common import Cluster, atomic, assets, identity, load_site
from install import product_lock
from resources import obj
import stage2


def pod_ready(v):
    return not v["metadata"].get("deletionTimestamp") and any(
        c["type"] == "Ready" and c["status"] == "True" for c in v.get("status", {}).get("conditions", []))


def predict(cluster, namespace, name):
    """Use the actual Standard Service; keep the administrator tunnel local."""
    log = cluster.directory / (name + "-forward.log")
    with log.open("w+") as stream:
        process = subprocess.Popen(cluster.command + ["-n", namespace, "port-forward", "service/" + name + "-predictor",
                                   ":80", "--address=127.0.0.1"], stdout=stream, stderr=subprocess.STDOUT)
        try:
            deadline = time.monotonic() + 30
            port = None
            while time.monotonic() < deadline and process.poll() is None:
                match = re.search(r"Forwarding from 127\.0\.0\.1:([0-9]+)", log.read_text())
                if match:
                    port = int(match[1]); break
                time.sleep(1)
            if port is None: raise RuntimeError("Standard Service port-forward failed")
            body = json.dumps({"instances": [[0.0, 0.0], [10.0, 10.0], [20.0, 20.0]]}).encode()
            req = urllib.request.Request("http://127.0.0.1:%d/v1/models/%s:predict" % (port, name),
                data=body, headers={"Content-Type": "application/json"}, method="POST")
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            with opener.open(req, timeout=30) as response:
                actual = json.load(response)
            if actual.get("predictions") != [0, 1, 2]:
                raise RuntimeError("prediction differs: " + json.dumps(actual))
            return {"input": json.loads(body), "response": actual, "status": "PASS"}
        finally:
            process.terminate()
            try: process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill(); process.wait(timeout=10)


def execute(cluster, root, run_id, report):
    report["lock"] = product_lock()
    namespace = stage2.NAMESPACES[0]
    name = "native-" + run_id
    model_name = "sklearn-" + run_id
    model_key = "models/" + run_id + "/model.joblib"
    pvc, notebook = stage2.notebook(cluster.site, namespace, name, run_id)
    service = obj("Service", name, namespace)
    sts = obj("StatefulSet", name, namespace, api="apps/v1")
    pod = obj("Pod", name + "-0", namespace)
    inference = stage2.inference(namespace, model_name, "s3://ani-kf-stage2-" + namespace + "/models/" + run_id + "/", run_id)
    reader = obj("Pod", "model-readback-" + run_id, namespace)
    for value in (pvc, notebook, inference, reader):
        if cluster.read(value): raise RuntimeError("fresh probe resource already exists: " + identity(value))
    cluster.apply([pvc])
    observed_pvc = cluster.wait(pvc, lambda v: v.get("status", {}).get("phase") == "Bound")
    report["workspace"] = {"name": pvc["metadata"]["name"], "uid": observed_pvc["metadata"]["uid"],
                           "class": observed_pvc["spec"]["storageClassName"], "size": observed_pvc["spec"]["resources"]["requests"]["storage"]}
    atomic(cluster.directory / "report.json", report)
    cluster.apply([notebook])
    observed_nb = cluster.read(notebook)
    report["notebook"] = {"name": name, "uid": observed_nb["metadata"]["uid"]}
    atomic(cluster.directory / "report.json", report)
    observed_sts = cluster.wait(sts, lambda v: any(o["uid"] == report["notebook"]["uid"] for o in v["metadata"].get("ownerReferences", [])))
    observed_pod = cluster.wait(pod, pod_ready, timeout=600)
    if not any(o["uid"] == observed_sts["metadata"]["uid"] for o in observed_pod["metadata"].get("ownerReferences", [])):
        raise RuntimeError("Notebook Pod owner differs")
    if observed_pod["spec"].get("automountServiceAccountToken") is not False or observed_pod["spec"]["serviceAccountName"] != "notebook-workload":
        raise RuntimeError("Notebook workload identity differs")
    expected = cluster.site["images"][stage2.WORKSPACE_IMAGE].rsplit("@", 1)[1]
    if observed_pod["status"]["containerStatuses"][0]["imageID"].rsplit("@", 1)[-1] != expected:
        raise RuntimeError("Notebook running image digest differs")
    observed_service = cluster.wait(service, lambda v: any(p["port"] == 8888 for p in v["spec"]["ports"]))
    report["native_resources"] = {"statefulset_uid": observed_sts["metadata"]["uid"], "pod_uid": observed_pod["metadata"]["uid"],
                                  "service_uid": observed_service["metadata"]["uid"], "base_path": "/notebook/" + namespace + "/" + name + "/"}
    atomic(cluster.directory / "report.json", report)
    # The image probe uses Jupyter REST + shell/iopub WebSocket channels. This
    # kubectl exec only starts that protocol client; it does not train directly.
    result = cluster.call(["-n", namespace, "exec", "pod/" + pod["metadata"]["name"], "-c", name, "--", "python",
                           "/opt/ani/kernel-probe.py", "--run-id", run_id, "--model-key", model_key], sensitive=True, timeout=180)
    kernel = json.loads(result)
    report["kernel"] = kernel
    atomic(cluster.directory / "report.json", report)
    # Separate read-only credential, no installer token or kubeconfig. Hash the
    # exact bytes uploaded by this kernel rather than substituting a fixture.
    code = "import boto3,hashlib,json,os; from botocore.config import Config; c=boto3.client('s3',endpoint_url=os.environ['ANI_S3_ENDPOINT'],verify=os.environ['AWS_CA_BUNDLE'],region_name='us-east-1',config=Config(signature_version='s3v4',s3={'addressing_style':'path'},retries={'max_attempts':0})); b=c.get_object(Bucket=os.environ['ANI_MODEL_BUCKET'],Key=os.environ['ANI_MODEL_KEY'])['Body'].read(); print(json.dumps({'sha256':hashlib.sha256(b).hexdigest(),'bytes':len(b)}))"
    reader["metadata"]["labels"].update({"ani.io/run-id": run_id, "ani.io/stage2-workload": "true"})
    reader["spec"] = {"serviceAccountName": "predictor-workload", "automountServiceAccountToken": False, "restartPolicy": "Never",
        "securityContext": {"runAsNonRoot": True, "runAsUser": 10001, "runAsGroup": 10001},
        "containers": [{"name": "readback", "image": cluster.site["images"]["ani.local/kubeflow-execution:26.03-v1"],
            "command": ["python", "-c", code], "env": [
                {"name": "ANI_S3_ENDPOINT", "value": stage2.S3_ENDPOINT}, {"name": "AWS_CA_BUNDLE", "value": "/etc/ani-model-ca/ca.crt"},
                {"name": "ANI_MODEL_BUCKET", "value": "ani-kf-stage2-" + namespace}, {"name": "ANI_MODEL_KEY", "value": model_key},
                *[{"name": key, "valueFrom": {"secretKeyRef": {"name": "ani-model-reader", "key": key}}} for key in ("AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY")]],
            "resources": {"requests": {"cpu": "100m", "memory": "128Mi"}, "limits": {"cpu": "1", "memory": "512Mi"}},
            "securityContext": {"allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}},
            "volumeMounts": [{"name": "ca", "mountPath": "/etc/ani-model-ca", "readOnly": True}]}],
        "volumes": [{"name": "ca", "configMap": {"name": "ani-model-ca"}}]}
    cluster.apply([reader])
    finished = cluster.wait(reader, lambda v: v.get("status", {}).get("phase") in ("Succeeded", "Failed"), timeout=180)
    if finished["status"]["phase"] != "Succeeded": raise RuntimeError("independent S3 readback Pod failed")
    readback = json.loads(cluster.call(["-n", namespace, "logs", reader["metadata"]["name"]], sensitive=True))
    # kernel-probe returns the model contract from the executed cell.
    if readback["sha256"] != kernel["model"]["sha256"]: raise RuntimeError("uploaded S3 bytes differ from kernel model")
    report["s3"] = {"bucket": "ani-kf-stage2-" + namespace, "key": model_key, **readback, "status": "PASS"}
    atomic(cluster.directory / "report.json", report)
    cluster.apply([inference])
    observed_is = cluster.read(inference)
    report["inference_service"] = {"name": model_name, "uid": observed_is["metadata"]["uid"], "storage_uri": inference["spec"]["predictor"]["model"]["storageUri"]}
    atomic(cluster.directory / "report.json", report)
    deployment = obj("Deployment", model_name + "-predictor", namespace, api="apps/v1")
    cluster.wait(deployment, lambda v: any(o["uid"] == observed_is["metadata"]["uid"] for o in v["metadata"].get("ownerReferences", [])), timeout=180)
    report["predictor"] = cluster.deployment(deployment)
    report["prediction"] = predict(cluster, namespace, model_name)
    report.update(status="PASS", main_flow="NATIVE_KERNEL_S3_SAME_MODEL_CORRECT_PREDICTION", persistence="NOT_RUN", negative_cases="NOT_RUN", first_stage="NOT_RUN")


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--site", required=True)
    p.add_argument("--run-id", required=True)
    p.add_argument("--report-dir", type=pathlib.Path, required=True)
    a = p.parse_args()
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,35}", a.run_id): raise ValueError("invalid bounded run id")
    os.umask(0o077)
    site = load_site(a.site)
    root = assets(pathlib.Path(__file__).parent)
    cluster = Cluster(site, a.report_dir)
    report = {"schema": "ani.kubeflow.stage2-main.v1", "release": site["release"], "run_id": a.run_id,
              "namespace": stage2.NAMESPACES[0], "status": "IN_PROGRESS", "started": datetime.datetime.now(datetime.timezone.utc).isoformat()}
    atomic(cluster.directory / "report.json", report)
    try:
        execute(cluster, root, a.run_id, report)
    except BaseException as error:
        report.update(status="FAIL", error=str(error), writes=cluster.writes)
        atomic(cluster.directory / "report.json", report)
        cluster.evidence()
        raise
    atomic(cluster.directory / "report.json", report)
    print(json.dumps({"status": report["status"], "report": str(cluster.directory / "report.json")}))


if __name__ == "__main__":
    main()

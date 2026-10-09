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


def resume_workspace(cluster, previous, namespace, run_id, pvc, notebook):
    if (previous.get("status") != "FAIL" or previous.get("namespace") != namespace
            or previous.get("run_id") != run_id
            or any(key in previous for key in ("kernel", "s3", "inference_service"))):
        raise ValueError("only a failed pre-kernel main probe can resume its original workspace")
    claim, native = cluster.owned(pvc), cluster.owned(notebook)
    for key, live, desired in (("workspace", claim, pvc), ("notebook", native, notebook)):
        recorded = previous.get(key, {})
        if (not live or recorded.get("name") != desired["metadata"]["name"]
                or recorded.get("uid") != live["metadata"]["uid"]):
            raise ValueError("original resume resource identity differs: " + key)
    if (claim.get("status", {}).get("phase") != "Bound" or claim["metadata"].get("ownerReferences")
            or claim["spec"]["storageClassName"] != pvc["spec"]["storageClassName"]
            or claim["spec"]["resources"]["requests"]["storage"] != pvc["spec"]["resources"]["requests"]["storage"]):
        raise ValueError("original resume workspace persistence contract differs")
    return claim


def resume_notebook_image(cluster, notebook):
    live = cluster.owned(notebook)
    actual = live["spec"]["template"]["spec"]["containers"]
    desired = notebook["spec"]["template"]["spec"]["containers"]
    if len(actual) != 1 or len(desired) != 1 or actual[0]["name"] != desired[0]["name"]:
        raise ValueError("original Notebook container identity differs")
    if actual[0]["image"] == desired[0]["image"]:
        return
    # CRD containers are atomic. The create manager's Update ownership cannot
    # be overwritten through SSA, even using the same manager name. Patch only
    # the approved image on the original UID/version; preserve all other data.
    patch = [{"op": "test", "path": "/metadata/uid", "value": live["metadata"]["uid"]},
             {"op": "test", "path": "/metadata/resourceVersion", "value": live["metadata"]["resourceVersion"]},
             {"op": "test", "path": "/spec/template/spec/containers/0/name", "value": actual[0]["name"]},
             {"op": "test", "path": "/spec/template/spec/containers/0/image", "value": actual[0]["image"]},
             {"op": "replace", "path": "/spec/template/spec/containers/0/image", "value": desired[0]["image"]}]
    args = ["patch", "notebooks", notebook["metadata"]["name"], "-n", notebook["metadata"]["namespace"],
            "--type=json", "--patch", json.dumps(patch), "-o", "json"]
    cluster.call(args + ["--dry-run=server"])
    pending = {"identity": identity(notebook), "uid": live["metadata"]["uid"], "action": "patch-approved-image", "result": "UNKNOWN"}
    cluster.writes.append(pending)
    atomic(cluster.directory / "writes.json", cluster.writes)
    response = json.loads(cluster.call(args))
    if response["metadata"]["uid"] != live["metadata"]["uid"]:
        raise RuntimeError("Notebook patch response identity differs")
    pending["result"] = "CONFIRMED"
    atomic(cluster.directory / "writes.json", cluster.writes)


def finish_prediction(cluster, namespace, name, inference, report):
    observed_is = cluster.owned(inference)
    if not observed_is or observed_is["metadata"]["uid"] != report["inference_service"]["uid"]:
        raise ValueError("original InferenceService identity differs")
    deployment = obj("Deployment", name + "-predictor", namespace, api="apps/v1")
    cluster.wait(deployment, lambda v: any(o["uid"] == observed_is["metadata"]["uid"] for o in v["metadata"].get("ownerReferences", [])), timeout=180)
    report["predictor"] = cluster.deployment(deployment)
    report["prediction"] = predict(cluster, namespace, name)
    report.update(status="PASS", main_flow="NATIVE_KERNEL_S3_SAME_MODEL_CORRECT_PREDICTION", persistence="NOT_RUN", negative_cases="NOT_RUN", first_stage="NOT_RUN")


def reconcile_prediction(cluster, run_id, previous, report):
    """Continue a recorded model handoff without training or creating objects."""
    namespace = stage2.NAMESPACES[0]
    name = "native-" + run_id
    model_name = "sklearn-" + run_id
    bucket = "ani-kf-stage2-" + namespace
    key = "models/" + run_id + "/model.joblib"
    uri = "s3://" + bucket + "/models/" + run_id + "/"
    if (previous.get("status") != "FAIL" or previous.get("run_id") != run_id
            or previous.get("namespace") != namespace or previous.get("cluster_uid") != report["cluster_uid"]
            or previous.get("kernel", {}).get("status") != "REAL_JUPYTER_KERNEL_MODEL_UPLOADED"
            or previous.get("s3", {}).get("status") != "PASS"
            or not previous.get("inference_service", {}).get("uid")):
        raise ValueError("prediction reconciliation requires the failed original cluster/kernel/S3/InferenceService handoff")
    kernel, s3 = previous["kernel"], previous["s3"]
    if (s3.get("bucket") != bucket or s3.get("key") != key
            or s3.get("sha256") != kernel["model"].get("sha256")
            or previous["inference_service"].get("name") != model_name
            or previous["inference_service"].get("storage_uri") != uri):
        raise ValueError("original model handoff differs")
    pvc, notebook = stage2.notebook(cluster.site, namespace, name, run_id)
    # Reuse the persistence/UID guard while deliberately checking the already
    # completed kernel/S3 phase separately, rather than replaying that phase.
    workspace = {k: previous[k] for k in ("status", "namespace", "run_id", "workspace", "notebook")}
    resume_workspace(cluster, workspace, namespace, run_id, pvc, notebook)
    live_nb = cluster.owned(notebook)
    if live_nb["spec"]["template"]["spec"]["containers"][0]["image"] != cluster.site["images"][stage2.WORKSPACE_IMAGE]:
        raise ValueError("original Notebook approved image differs")
    inference = stage2.inference(namespace, model_name, uri, run_id)
    live_is = cluster.owned(inference)
    if (not live_is or live_is["metadata"]["uid"] != previous["inference_service"]["uid"]
            or live_is["spec"]["predictor"]["model"]["storageUri"] != uri
            or live_is["metadata"].get("annotations", {}).get("serving.kserve.io/deploymentMode") != "Standard"):
        raise ValueError("original InferenceService identity or model differs")
    reader = obj("Pod", "model-readback-" + run_id, namespace)
    live_reader = cluster.owned(reader)
    receipt = next((v for v in previous.get("writes", []) if v["identity"] == identity(reader) and v["result"] == "CONFIRMED"), None)
    if (not live_reader or not receipt or receipt["uid"] != live_reader["metadata"]["uid"]
            or live_reader.get("status", {}).get("phase") != "Succeeded"):
        raise ValueError("original independent S3 readback identity differs")
    readback = json.loads(cluster.call(["-n", namespace, "exec", "pod/" + name + "-0", "-c", name, "--", "python",
        "/opt/ani/kernel-probe.py", "--run-id", run_id, "--model-key", key, "--readback"], sensitive=True, timeout=90))
    if readback.get("model_sha256") != s3["sha256"] or readback.get("marker_sha256") != kernel["marker_sha256"]:
        raise ValueError("preserved original workspace bytes differ")
    independent = json.loads(cluster.call(["-n", namespace, "logs", reader["metadata"]["name"]], sensitive=True))
    if independent.get("sha256") != s3["sha256"] or independent.get("bytes") != s3["bytes"]:
        raise ValueError("original independent S3 readback bytes differ")
    for field in ("workspace", "notebook", "native_resources", "kernel", "s3", "inference_service"):
        report[field] = previous[field]
    report["resumption"] = {"phase": "PREDICTION", "new_workspace_creates": 0, "new_model_uploads": 0,
                            "new_inference_service_creates": 0, "workspace_readback": readback}
    atomic(cluster.directory / "report.json", report)
    # The failed original IS may be in controller exponential backoff. A
    # conditional metadata event reconciles it immediately after a source fix;
    # no predictor/controller restart or model replacement is involved.
    patch = [{"op": "test", "path": "/metadata/uid", "value": live_is["metadata"]["uid"]},
             {"op": "test", "path": "/metadata/resourceVersion", "value": live_is["metadata"]["resourceVersion"]},
             {"op": "add", "path": "/metadata/annotations/ani.io~1model-reconcile",
              "value": datetime.datetime.now(datetime.timezone.utc).isoformat()}]
    args = ["patch", "inferenceservices", model_name, "-n", namespace, "--type=json", "--patch", json.dumps(patch), "-o", "json"]
    cluster.call(args + ["--dry-run=server"])
    receipt = {"identity": identity(inference), "uid": live_is["metadata"]["uid"], "action": "reconcile-model-metadata", "result": "UNKNOWN"}
    cluster.writes.append(receipt); atomic(cluster.directory / "writes.json", cluster.writes)
    result = json.loads(cluster.call(args))
    if result["metadata"]["uid"] != live_is["metadata"]["uid"]:
        raise ValueError("reconciliation event identity differs")
    receipt["result"] = "CONFIRMED"; atomic(cluster.directory / "writes.json", cluster.writes)
    finish_prediction(cluster, namespace, model_name, inference, report)


def execute(cluster, root, run_id, report, previous=None, reconcile=False):
    report["lock"] = product_lock()
    if reconcile:
        return reconcile_prediction(cluster, run_id, previous, report)
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
    for value in ((pvc, notebook, inference, reader) if previous is None else (inference, reader)):
        if cluster.read(value): raise RuntimeError("fresh probe resource already exists: " + identity(value))
    if previous is None:
        cluster.apply([pvc])
        observed_pvc = cluster.wait(pvc, lambda v: v.get("status", {}).get("phase") == "Bound")
    else:
        observed_pvc = resume_workspace(cluster, previous, namespace, run_id, pvc, notebook)
        report["resumption"] = {"original_notebook_uid": previous["notebook"]["uid"],
                                "original_workspace_uid": previous["workspace"]["uid"], "new_workspace_creates": 0}
    report["workspace"] = {"name": pvc["metadata"]["name"], "uid": observed_pvc["metadata"]["uid"],
                           "class": observed_pvc["spec"]["storageClassName"], "size": observed_pvc["spec"]["resources"]["requests"]["storage"]}
    atomic(cluster.directory / "report.json", report)
    if previous is None:
        cluster.apply([notebook])
    else:
        resume_notebook_image(cluster, notebook)
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
    observed_service = cluster.wait(service, lambda v: any(p["port"] == 80 and p["targetPort"] == 8888 for p in v["spec"]["ports"]))
    report["native_resources"] = {"statefulset_uid": observed_sts["metadata"]["uid"], "pod_uid": observed_pod["metadata"]["uid"],
                                  "service_uid": observed_service["metadata"]["uid"], "service_port": 80, "container_port": 8888,
                                  "base_path": "/notebook/" + namespace + "/" + name + "/"}
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
    finish_prediction(cluster, namespace, model_name, inference, report)


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--site", required=True)
    p.add_argument("--run-id", required=True)
    p.add_argument("--report-dir", type=pathlib.Path, required=True)
    recovery = p.add_mutually_exclusive_group()
    recovery.add_argument("--resume-report", type=pathlib.Path, help="resume only recorded failed pre-kernel Notebook/PVC UIDs; never replace data")
    recovery.add_argument("--reconcile-report", type=pathlib.Path, help="continue the original completed kernel/S3 handoff at prediction; no training or creates")
    a = p.parse_args()
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,35}", a.run_id): raise ValueError("invalid bounded run id")
    os.umask(0o077)
    site = load_site(a.site)
    root = assets(pathlib.Path(__file__).parent)
    cluster = Cluster(site, a.report_dir)
    report = {"schema": "ani.kubeflow.stage2-main.v1", "release": site["release"], "run_id": a.run_id,
              "namespace": stage2.NAMESPACES[0], "status": "IN_PROGRESS", "started": datetime.datetime.now(datetime.timezone.utc).isoformat()}
    previous = None
    if a.resume_report or a.reconcile_report:
        source = (a.resume_report or a.reconcile_report).read_bytes()
        previous = json.loads(source)
        report["original_report_sha256"] = hashlib.sha256(source).hexdigest()
    atomic(cluster.directory / "report.json", report)
    try:
        report["networkCapability"] = cluster.network_capability()
        if previous and previous.get("networkCapability") != report["networkCapability"]:
            raise ValueError("original main flow network capability differs")
        report["cluster_uid"] = cluster.read(obj("Namespace", "kube-system"))["metadata"]["uid"]
        execute(cluster, root, a.run_id, report, previous, bool(a.reconcile_report))
    except BaseException as error:
        report.update(status="FAIL", error=str(error), writes=cluster.writes)
        atomic(cluster.directory / "report.json", report)
        cluster.evidence()
        raise
    atomic(cluster.directory / "report.json", report)
    print(json.dumps({"status": report["status"], "report": str(cluster.directory / "report.json")}))


if __name__ == "__main__":
    main()

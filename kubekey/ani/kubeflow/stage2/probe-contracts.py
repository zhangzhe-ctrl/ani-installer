#!/usr/bin/env python3
"""Bounded native lifecycle/failure acceptance after the real main flow.

Administrator orchestration only. Workloads keep their scoped identities; no
training, shared credential changes, automatic cleanup or cluster recovery.
"""
import argparse
import datetime
import hashlib
import json
import os
import pathlib
import re
import runpy
import secrets

from common import Cluster, assets, atomic, decode, endpoint, identity, load_site
from install import product_lock
from resources import obj
import stage2


def patch_owned(cluster, value, operations):
    live = cluster.owned(value)
    patch = [{"op": "test", "path": "/metadata/uid", "value": live["metadata"]["uid"]},
             {"op": "test", "path": "/metadata/resourceVersion", "value": live["metadata"]["resourceVersion"]}, *operations]
    args = ["patch", value["kind"], value["metadata"]["name"], "-n", value["metadata"]["namespace"],
            "--type=json", "--patch", json.dumps(patch), "-o", "json"]
    cluster.call(args + ["--dry-run=server"])
    receipt = {"identity": identity(value), "uid": live["metadata"]["uid"], "action": "patch", "result": "UNKNOWN"}
    cluster.writes.append(receipt); atomic(cluster.directory / "writes.json", cluster.writes)
    result = json.loads(cluster.call(args))
    if result["metadata"]["uid"] != live["metadata"]["uid"]:
        raise ValueError("patch response identity differs")
    receipt["result"] = "CONFIRMED"; atomic(cluster.directory / "writes.json", cluster.writes)


def delete_owned_pod(cluster, pod, uid):
    live = cluster.owned(pod)
    if not live or live["metadata"]["uid"] != uid or live["metadata"].get("deletionTimestamp"):
        raise ValueError("original predictor Pod identity differs")
    options = {"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": {
        "uid": uid, "resourceVersion": live["metadata"]["resourceVersion"]}}
    args = ["delete", "--raw", endpoint(pod), "-f", "-"]
    cluster.call(args, {**options, "dryRun": ["All"]})
    receipt = {"identity": identity(pod), "uid": uid, "action": "delete-original-predictor-pod", "result": "UNKNOWN"}
    cluster.writes.append(receipt); atomic(cluster.directory / "writes.json", cluster.writes)
    cluster.call(args, options)
    receipt["result"] = "CONFIRMED"; atomic(cluster.directory / "writes.json", cluster.writes)


def workspace_files(cluster, main):
    code = "import pathlib,hashlib,json; p=pathlib.Path(" + repr("acceptance-" + main["run_id"]) + "); print(json.dumps({n:hashlib.sha256((p/n).read_bytes()).hexdigest() for n in ('main-flow.ipynb','fixed-content.txt','model.joblib','model-contract.json')}))"
    namespace, name = main["namespace"], main["notebook"]["name"]
    return json.loads(cluster.call(["-n", namespace, "exec", "pod/" + name + "-0", "-c", name, "--", "python", "-c", code], sensitive=True))


def notebook_resume(cluster, main, report, helpers):
    namespace, name = main["namespace"], main["notebook"]["name"]
    notebook = obj("Notebook", name, namespace, api="kubeflow.org/v1")
    sts = obj("StatefulSet", name, namespace, api="apps/v1")
    pod = obj("Pod", name + "-0", namespace)
    pvc = obj("PersistentVolumeClaim", main["workspace"]["name"], namespace)
    native, claim, original = cluster.owned(notebook), cluster.owned(pvc), cluster.owned(pod)
    if (native["metadata"]["uid"] != main["notebook"]["uid"] or claim["metadata"]["uid"] != main["workspace"]["uid"]
            or original["metadata"]["uid"] != main["native_resources"]["pod_uid"]
            or not helpers["pod_ready"](original) or "kubeflow-resource-stopped" in native["metadata"].get("annotations", {})):
        raise ValueError("original active Notebook/workspace identity differs")
    before = workspace_files(cluster, main)
    if before["model.joblib"] != main["s3"]["sha256"] or before["fixed-content.txt"] != main["kernel"]["marker_sha256"]:
        raise ValueError("original workspace files differ")
    report.update(before=before, original_pod_uid=original["metadata"]["uid"], pvc_uid=claim["metadata"]["uid"])
    atomic(cluster.directory / "report.json", report)
    annotations = native["metadata"].get("annotations", {})
    stamp = datetime.datetime.now(datetime.timezone.utc).isoformat()
    operations = ([{"op": "add", "path": "/metadata/annotations", "value": {"kubeflow-resource-stopped": stamp}}]
                  if not annotations else [{"op": "add", "path": "/metadata/annotations/kubeflow-resource-stopped", "value": stamp}])
    patch_owned(cluster, notebook, operations)
    cluster.wait(sts, lambda v: v["spec"].get("replicas") == 0 and not cluster.read(pod), timeout=180)
    if cluster.owned(pvc)["metadata"]["uid"] != claim["metadata"]["uid"]:
        raise ValueError("stopped Notebook lost its original PVC")
    report["stopped"] = {"pod_absent": True, "pvc_uid": claim["metadata"]["uid"]}; atomic(cluster.directory / "report.json", report)
    patch_owned(cluster, notebook, [{"op": "remove", "path": "/metadata/annotations/kubeflow-resource-stopped"}])
    resumed = cluster.wait(pod, lambda v: v["metadata"]["uid"] != original["metadata"]["uid"] and helpers["pod_ready"](v), timeout=600)
    after = workspace_files(cluster, main)
    if before != after or cluster.owned(pvc)["metadata"]["uid"] != claim["metadata"]["uid"]:
        raise ValueError("Notebook stop/resume did not preserve PVC and file bytes")
    readback = json.loads(cluster.call(["-n", namespace, "exec", "pod/" + name + "-0", "-c", name, "--", "python",
        "/opt/ani/kernel-probe.py", "--run-id", main["run_id"], "--model-key", main["s3"]["key"], "--readback"], sensitive=True, timeout=90))
    if readback.get("model_sha256") != main["s3"]["sha256"]:
        raise ValueError("native Jupyter readback differs after resume")
    report.update(after=after, resumed_pod_uid=resumed["metadata"]["uid"], native_readback=readback, status="PASS")


def predictor_recreate(cluster, main, report, helpers):
    namespace, name = main["namespace"], main["inference_service"]["name"]
    deployment = obj("Deployment", name + "-predictor", namespace, api="apps/v1")
    before = cluster.deployment(deployment)
    if before["uid"] != main["predictor"]["uid"] or len(before["pods"]) != 1 or before["pods"][0]["uid"] != main["predictor"]["pods"][0]["uid"]:
        raise ValueError("original predictor Deployment/Pod identity differs")
    report["before"] = before; report["prediction_before"] = helpers["predict"](cluster, namespace, name)
    atomic(cluster.directory / "report.json", report)
    delete_owned_pod(cluster, obj("Pod", before["pods"][0]["name"], namespace), before["pods"][0]["uid"])
    after = cluster.deployment(deployment)
    if after["uid"] != before["uid"] or len(after["pods"]) != 1 or after["pods"][0]["uid"] == before["pods"][0]["uid"]:
        raise ValueError("predictor reconstruction identity differs")
    report.update(after=after, prediction_after=helpers["predict"](cluster, namespace, name), status="PASS")


def model_failure(cluster, main, report, helpers, wrong_credentials=False):
    namespace, run_id = main["namespace"], main["run_id"]
    suffix = "credential" if wrong_credentials else "path"
    name = "bad-" + suffix + "-" + run_id
    uri = main["inference_service"]["storage_uri"]
    if not wrong_credentials:
        uri += "missing-model/"
    value = stage2.inference(namespace, name, uri, run_id)
    values = []
    credentials = []
    if wrong_credentials:
        secret_name = "bad-model-" + run_id
        credentials = [secrets.token_hex(16), secrets.token_hex(32)]
        values += [obj("Secret", secret_name, namespace, type="Opaque", stringData=dict(zip(("AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"), credentials))),
                   obj("ServiceAccount", secret_name, namespace, automountServiceAccountToken=False, secrets=[{"name": secret_name}])]
        value["metadata"]["annotations"]["serving.kserve.io/secretName"] = secret_name
        value["spec"]["predictor"]["serviceAccountName"] = secret_name
    values.append(value)
    for v in values:
        if cluster.read(v):
            raise ValueError("negative test object already exists; preserve it")
    report["positive_control_before"] = helpers["predict"](cluster, namespace, main["inference_service"]["name"])
    cluster.apply(values)
    isvc = cluster.owned(value); report["inference_service"] = {"name": name, "uid": isvc["metadata"]["uid"], "storage_uri": uri}
    atomic(cluster.directory / "report.json", report)
    deployment = obj("Deployment", name + "-predictor", namespace, api="apps/v1")
    cluster.wait(deployment, lambda v: any(o["uid"] == isvc["metadata"]["uid"] for o in v["metadata"].get("ownerReferences", [])), timeout=180)
    failed = []
    def observed_failure(v):
        pods = json.loads(cluster.call(["get", "pods", "-n", namespace, "-l", "serving.kserve.io/inferenceservice=" + name, "-o", "json"]))["items"]
        for pod in pods:
            for state in pod.get("status", {}).get("initContainerStatuses", []) + pod.get("status", {}).get("containerStatuses", []):
                terminal = state.get("state", {}).get("terminated") or state.get("lastState", {}).get("terminated")
                if terminal and terminal.get("exitCode", 0) != 0:
                    failed[:] = [(pod, state, terminal)]; return True
        return False
    cluster.wait(deployment, observed_failure, timeout=240)
    pod, state, terminal = failed[0]
    args = ["-n", namespace, "logs", pod["metadata"]["name"], "-c", state["name"]]
    if not state.get("state", {}).get("terminated"):
        args.append("--previous")
    log = cluster.call(args, sensitive=True)
    for secret in ("ani-model-reader", "ani-model-writer"):
        record = cluster.owned(obj("Secret", secret, namespace))
        credentials += [decode(record, key) for key in ("AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY")]
    for credential in credentials:
        log = log.replace(credential, "<redacted>")
    log = re.sub(r"https?://[^\s\"']+", lambda m: m.group(0).split("?", 1)[0], log)
    cause = (r"InvalidAccessKeyId|SignatureDoesNotMatch|AccessDenied|InvalidToken" if wrong_credentials
             else r"NoSuchKey|no .*files|does not exist|not found|No model|Failed to fetch|object.*not.*found|empty.*model")
    if not re.search(cause, log, re.I):
        (cluster.directory / "first-error-redacted.log").write_text(log[-12000:])
        raise ValueError("negative model failure is not attributable to the requested case")
    current = cluster.owned(value)
    if any(c["type"] == "Ready" and c["status"] == "True" for c in current.get("status", {}).get("conditions", [])):
        raise ValueError("negative model unexpectedly Ready")
    (cluster.directory / "first-error-redacted.log").write_text(log[-12000:])
    report.update(status="PASS", failure={"pod_uid": pod["metadata"]["uid"], "container": state["name"],
        "exit_code": terminal["exitCode"], "cause": suffix, "log_sha256": hashlib.sha256(log.encode()).hexdigest()},
        positive_control_after=helpers["predict"](cluster, namespace, main["inference_service"]["name"]))


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--site", required=True)
    p.add_argument("--main-report", type=pathlib.Path, required=True)
    p.add_argument("--report-dir", type=pathlib.Path, required=True)
    p.add_argument("--case", choices=("notebook-resume", "predictor-recreate", "model-path", "model-credential"), required=True)
    a = p.parse_args(); os.umask(0o077)
    site = load_site(a.site); root = assets(pathlib.Path(__file__).parent)
    source = a.main_report.read_bytes(); original = json.loads(source)
    if original.get("status") != "PASS" or original.get("main_flow") != "NATIVE_KERNEL_S3_SAME_MODEL_CORRECT_PREDICTION":
        raise ValueError("actual successful native main flow required")
    cluster = Cluster(site, a.report_dir)
    report = {"schema": "ani.kubeflow.stage2-contract.v1", "case": a.case, "status": "IN_PROGRESS",
              "main_report_sha256": hashlib.sha256(source).hexdigest(), "run_id": original["run_id"], "namespace": original["namespace"]}
    atomic(cluster.directory / "report.json", report)
    try:
        report["networkCapability"] = cluster.network_capability()
        if original.get("networkCapability") != report["networkCapability"]:
            raise ValueError("original main flow network capability differs")
        report["lock"] = product_lock()
        report["cluster_uid"] = cluster.read(obj("Namespace", "kube-system"))["metadata"]["uid"]
        if report["cluster_uid"] != original["cluster_uid"] or original["namespace"] != stage2.NAMESPACES[0]:
            raise ValueError("original main flow cluster/namespace differs")
        helpers = runpy.run_path(str(root / "stage2-probe-main.py"))
        if a.case == "notebook-resume": notebook_resume(cluster, original, report, helpers)
        elif a.case == "predictor-recreate": predictor_recreate(cluster, original, report, helpers)
        else: model_failure(cluster, original, report, helpers, a.case == "model-credential")
        report["writes"] = cluster.writes
    except BaseException as error:
        report.update(status="FAIL", error=str(error), writes=cluster.writes)
        atomic(cluster.directory / "report.json", report); cluster.evidence(); raise
    atomic(cluster.directory / "report.json", report)
    print(json.dumps({"status": report["status"], "case": a.case, "report": str(cluster.directory / "report.json")}))


if __name__ == "__main__":
    main()

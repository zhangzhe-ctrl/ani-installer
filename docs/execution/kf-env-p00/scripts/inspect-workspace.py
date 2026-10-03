#!/usr/bin/env python3
"""Lab acceptance only: retain and read the actual execution PVC by create UID.

The operator holds the existing product lock. This does not give the restricted
client Pod creation permission or create a modeldev workspace owner.
"""
import argparse
import hashlib
import json
import pathlib
import sys
import time
import uuid

parser = argparse.ArgumentParser()
parser.add_argument("--site", required=True)
parser.add_argument("--private-report", required=True)
parser.add_argument("--reader-node", required=True)
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
    raise ValueError("explicit absolute output required")
cluster = Cluster(site, output)
report = {"schema": "ani.kubeflow.workspace-readback.v1", "status": "IN_PROGRESS",
          "lock": product_lock(), "cleanup": "NOT_REQUESTED", "sourceSha256": hashlib.sha256(pathlib.Path(__file__).read_bytes()).hexdigest()}
atomic(output / "report.json", report)
try:
    record = json.loads(pathlib.Path(args.private_report).read_text())
    namespace, execution, mode = record["namespace"], record["execution"], record["mode"]
    if namespace not in site["tenants"] or record["status"] != "RUN_STATE_CORRELATED" or not execution.startswith("env-"):
        raise ValueError("not an accepted environment execution receipt")
    workspace, receipt = record["workspace"], record["creationReceipt"]
    if workspace["creationResult"] != "CONFIRMED" or receipt["workspaceUid"] != workspace["uid"] or receipt["execution"] != execution:
        raise ValueError("workspace create response differs")
    claim = cluster.read(obj("PersistentVolumeClaim", workspace["name"], namespace))
    if not claim or claim["metadata"]["uid"] != workspace["uid"] or claim["metadata"].get("ownerReferences") or claim["status"].get("phase") != "Bound":
        raise ValueError("retained workspace identity/lifecycle differs")
    node = json.loads(cluster.call(["get", "node", args.reader_node, "-o", "json"]))
    if not any(a["type"] == "InternalIP" and a["address"] in site["node_addresses"] for a in node["status"]["addresses"]):
        raise ValueError("reader node is outside the fixed target")
    name = "ani-kfp-workspace-reader-" + uuid.uuid4().hex[:16]
    command = r'''
import errno,hashlib,json,pathlib,sys
execution,mode,workspace_uid,train_uid=sys.argv[1:]
root=pathlib.Path('/workspace')
try:
    (root/'readback-negative').write_text('must-not-write')
except OSError as error:
    assert error.errno in (errno.EROFS,errno.EACCES)
    readonly_errno=error.errno
else:
    raise RuntimeError('reader mount was writable')
def read(relative):
    path=root/relative
    assert not path.is_symlink() and path.is_file() and path.stat().st_size <= 65536
    body=path.read_bytes()
    return json.loads(body),hashlib.sha256(body).hexdigest()
dataset,input_sha=read('input/dataset.json')
unique,unique_sha=read('output/unique.json')
correlation,correlation_sha=read('output/correlation.json')
assert dataset['execution']==unique['execution']==correlation['execution']==execution
assert unique['inputSha256']==correlation['inputSha256']==input_sha
assert unique['readonlyErrno'] in (errno.EROFS,errno.EACCES)
assert correlation['workspace']['uid']==workspace_uid and correlation['trainJob']['uid']==train_uid
assert correlation['trainJob']['creationResult']=='CONFIRMED'
result={'execution':execution,'mode':mode,'inputSha256':input_sha,'uniqueSha256':unique_sha,'correlationSha256':correlation_sha,'readOnlyMountErrno':readonly_errno,'trainingReadOnlyInputErrno':unique['readonlyErrno']}
if mode=='success':
    model,model_sha=read('output/model.json')
    assert model['execution']==execution and model['inputSha256']==input_sha and model['loss']<0.1
    result.update(modelSha256=model_sha,loss=model['loss'])
else:
    assert not (root/'output/model.json').exists()
assert not pathlib.Path('/var/run/secrets/kubernetes.io/serviceaccount/token').exists()
print(json.dumps(result,sort_keys=True))
'''
    security = {"allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}, "readOnlyRootFilesystem": True}
    pod = obj("Pod", name, namespace, spec={"serviceAccountName": "trainer-workload", "automountServiceAccountToken": False,
        "nodeSelector": {"kubernetes.io/hostname": args.reader_node}, "restartPolicy": "Never",
        "activeDeadlineSeconds": 120, "securityContext": {"runAsNonRoot": True, "runAsUser": 1000, "runAsGroup": 1000, "seccompProfile": {"type": "RuntimeDefault"}},
        "containers": [{"name": "reader", "image": site["images"]["ani.local/kubeflow-execution:26.03-v1"], "command": ["python", "-c", command],
            "args": [execution, mode, workspace["uid"], receipt["trainJobUid"]], "securityContext": security,
            "resources": {"requests": {"cpu": "100m", "memory": "128Mi"}, "limits": {"cpu": "500m", "memory": "256Mi"}},
            "volumeMounts": [{"name": "workspace", "mountPath": "/workspace", "readOnly": True}]}],
        "volumes": [{"name": "workspace", "persistentVolumeClaim": {"claimName": workspace["name"], "readOnly": True}}]})
    if cluster.read(pod):
        raise RuntimeError("reader name occupied; no replacement")
    cluster.apply([pod])
    created_uid = cluster.writes[-1]["uid"]
    report.update(execution=execution, namespace=namespace, mode=mode, workspace={"name": workspace["name"], "uid": workspace["uid"]},
                  reader={"name": name, "uid": created_uid, "creationResult": "CONFIRMED", "node": args.reader_node})
    atomic(output / "report.json", report)
    deadline = time.monotonic() + 150
    while time.monotonic() < deadline:
        live = cluster.read(pod)
        if not live or live["metadata"]["uid"] != created_uid:
            raise RuntimeError("reader identity changed")
        if live.get("status", {}).get("phase") in ("Failed", "Succeeded"):
            break
        time.sleep(3)
    else:
        raise TimeoutError("reader deadline")
    if live["status"]["phase"] != "Succeeded":
        raise RuntimeError("reader failed; preserve Pod/PVC and inspect actual exit")
    states = live["status"].get("containerStatuses", [])
    if len(states) != 1 or states[0]["state"].get("terminated", {}).get("exitCode") != 0:
        raise RuntimeError("reader process exit differs")
    if states[0].get("imageID", "").rsplit("@", 1)[-1] != site["images"]["ani.local/kubeflow-execution:26.03-v1"].rsplit("@", 1)[-1]:
        raise RuntimeError("reader runtime digest differs")
    data = json.loads(cluster.call(["logs", name, "-n", namespace, "-c", "reader"]))
    final = cluster.read(obj("PersistentVolumeClaim", workspace["name"], namespace))
    if final["metadata"]["uid"] != workspace["uid"] or final["metadata"].get("ownerReferences"):
        raise RuntimeError("workspace lifecycle changed during readback")
    report.update(status="FILES_READ_BACK", fileEvidence=data, readerImageID=states[0].get("imageID"), retained=True)
    atomic(output / "report.json", report)
except BaseException as error:
    report.update(status="FAIL", error=str(error))
    atomic(output / "report.json", report)
    cluster.evidence()
    raise
print(json.dumps({"status": report["status"], "report": str(output / "report.json")}))

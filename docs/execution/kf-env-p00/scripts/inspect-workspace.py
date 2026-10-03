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
parser.add_argument("--diagnostic-failure", action="store_true",
                    help="export an earlier terminal setup failure; never an acceptance PASS")
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
    required_status = "FAIL" if args.diagnostic_failure else "RUN_STATE_CORRELATED"
    if namespace not in site["tenants"] or record["status"] != required_status or not execution.startswith("env-"):
        raise ValueError("not an accepted environment execution receipt")
    workspace, receipt = record["workspace"], record.get("creationReceipt", {})
    if workspace["creationResult"] != "CONFIRMED" or (receipt and (receipt["workspaceUid"] != workspace["uid"] or receipt["execution"] != execution)):
        raise ValueError("workspace create response differs")
    if not args.diagnostic_failure and not receipt:
        raise ValueError("external training receipt is missing")
    claim = cluster.read(obj("PersistentVolumeClaim", workspace["name"], namespace))
    if not claim or claim["metadata"]["uid"] != workspace["uid"] or claim["metadata"].get("ownerReferences") or claim["status"].get("phase") != "Bound":
        raise ValueError("retained workspace identity/lifecycle differs")
    if args.diagnostic_failure:
        partial_stop = (mode == "stop" and record.get("stop", {}).get("trainJobResult") == "CONFIRMED"
                        and record.get("errorStatus") == 403 and not record["stop"].get("runResult"))
        if record["run"]["creationResult"] != "CONFIRMED" or (record["run"]["state"] != "FAILED" and not partial_stop):
            raise ValueError("diagnostic source is not a confirmed terminal failed Run")
        workflows = json.loads(cluster.call(["get", "workflows", "-n", namespace, "-o", "json"]))["items"]
        matched = [w for w in workflows if w["metadata"].get("labels", {}).get("pipeline/runid") == record["run"]["id"]]
        if len(matched) != 1 or matched[0].get("status", {}).get("phase") != "Failed":
            raise ValueError("actual failed Workflow differs")
        pods = json.loads(cluster.call(["get", "pods", "-n", namespace, "-o", "json"]))["items"]
        using = [p for p in pods if any(v.get("persistentVolumeClaim", {}).get("claimName") == workspace["name"] for v in p["spec"].get("volumes", []))]
        if any(p.get("status", {}).get("phase") not in ("Failed", "Succeeded") for p in using):
            raise ValueError("a live Pod still uses the diagnostic workspace")
        job = cluster.read(obj("TrainJob", "ani-kfp-train-" + execution, namespace, api="trainer.kubeflow.org/v1alpha1"))
        if receipt:
            condition = "Suspended" if partial_stop else "Complete"
            if not job or job["metadata"]["uid"] != receipt["trainJobUid"] or not any(c["type"] == condition and c["status"] == "True" for c in job.get("status", {}).get("conditions", [])) or (partial_stop and not job["spec"].get("suspend")):
                raise ValueError("diagnostic completed TrainJob differs")
        elif job:
            raise ValueError("unexpected TrainJob without a creation receipt")
        report.update(diagnostic=True, acceptance="NOT_ATTESTED", workflowUid=matched[0]["metadata"]["uid"],
                      originalReportSha256=hashlib.sha256(pathlib.Path(args.private_report).read_bytes()).hexdigest())
    node = json.loads(cluster.call(["get", "node", args.reader_node, "-o", "json"]))
    if not any(a["type"] == "InternalIP" and a["address"] in site["node_addresses"] for a in node["status"]["addresses"]):
        raise ValueError("reader node is outside the fixed target")
    name = "ani-kfp-workspace-reader-" + uuid.uuid4().hex[:16]
    command = r'''
import base64,errno,hashlib,json,pathlib,sys
execution,mode,workspace_uid,train_uid=sys.argv[1:]
root=pathlib.Path('/workspace')
try:
    (root/'readback-negative').write_text('must-not-write')
except OSError as error:
    assert error.errno in (errno.EROFS,errno.EACCES)
    readonly_errno=error.errno
else:
    raise RuntimeError('reader mount was writable')
files={}
def read(relative):
    path=root/relative
    assert not path.is_symlink() and path.is_file() and path.stat().st_size <= 65536
    body=path.read_bytes()
    files[relative]={'sha256':hashlib.sha256(body).hexdigest(),'size':len(body),'base64':base64.b64encode(body).decode()}
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
result['files']=files
print(json.dumps(result,sort_keys=True))
'''
    if args.diagnostic_failure:
        command = r'''
import base64,errno,hashlib,json,pathlib,sys
execution,mode,workspace_uid,train_uid=sys.argv[1:]
root=pathlib.Path('/workspace')
assert not pathlib.Path('/var/run/secrets/kubernetes.io/serviceaccount/token').exists()
try:
    (root/'readback-negative').write_text('must-not-write')
except OSError as error:
    assert error.errno in (errno.EROFS,errno.EACCES)
    readonly_errno=error.errno
else:
    raise RuntimeError('diagnostic mount was writable')
files={}
allowed={'input/dataset.json','output/unique.json','output/correlation.json','output/model.json'}
for path in root.rglob('*'):
    assert not path.is_symlink()
    if path.is_dir():
        assert path.relative_to(root).as_posix() in ('input','output')
        continue
    relative=path.relative_to(root).as_posix()
    assert relative in allowed and path.is_file() and path.stat().st_size<=65536
    body=path.read_bytes();data=json.loads(body)
    assert data['execution']==execution
    files[relative]={'sha256':hashlib.sha256(body).hexdigest(),'size':len(body),'base64':base64.b64encode(body).decode()}
if train_uid:
    assert set(files)==(allowed-{'output/model.json'} if mode=='stop' else allowed)
    correlation=json.loads(base64.b64decode(files['output/correlation.json']['base64']))
    unique=json.loads(base64.b64decode(files['output/unique.json']['base64']))
    assert correlation['workspace']['uid']==workspace_uid and correlation['trainJob']['uid']==train_uid
    assert unique['inputSha256']==correlation['inputSha256']==files['input/dataset.json']['sha256']
else:
    assert set(files)<= {'input/dataset.json'}
print(json.dumps({'execution':execution,'files':files,'readOnlyMountErrno':readonly_errno},sort_keys=True))
'''
    security = {"allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}, "readOnlyRootFilesystem": True}
    pod = obj("Pod", name, namespace, spec={"serviceAccountName": "trainer-workload", "automountServiceAccountToken": False,
        "nodeSelector": {"kubernetes.io/hostname": args.reader_node}, "restartPolicy": "Never",
        "activeDeadlineSeconds": 120, "securityContext": {"runAsNonRoot": True, "runAsUser": 1000, "runAsGroup": 1000, "seccompProfile": {"type": "RuntimeDefault"}},
        "containers": [{"name": "reader", "image": site["images"]["ani.local/kubeflow-execution:26.03-v1"], "command": ["python", "-c", command],
            "args": [execution, mode, workspace["uid"], receipt.get("trainJobUid", "")], "securityContext": security,
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
    report.update(status="DIAGNOSTIC_FILES_EXPORTED" if args.diagnostic_failure else "FILES_READ_BACK", fileEvidence=data, readerImageID=states[0].get("imageID"), retained=True)
    atomic(output / "report.json", report)
except BaseException as error:
    report.update(status="FAIL", error=str(error))
    atomic(output / "report.json", report)
    cluster.evidence()
    raise
print(json.dumps({"status": report["status"], "report": str(output / "report.json")}))

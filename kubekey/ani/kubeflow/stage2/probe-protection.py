#!/usr/bin/env python3
"""Actual scoped S3, ordinary workload and control authorization checks."""
import argparse
import hashlib
import json
import os
import pathlib

from common import Cluster, assets, atomic, decode, load_site
from install import product_lock
from resources import obj
import stage2

S3_CODE = r'''
import boto3,hashlib,json,os,pathlib,sys
from botocore.config import Config
from botocore.exceptions import ClientError
mode,namespace,other,key=sys.argv[1:]
assert not pathlib.Path('/var/run/secrets/kubernetes.io/serviceaccount/token').exists()
c=boto3.client('s3',endpoint_url=os.environ['ANI_S3_ENDPOINT'],verify=os.environ['AWS_CA_BUNDLE'],region_name='us-east-1',config=Config(signature_version='s3v4',s3={'addressing_style':'path'},retries={'max_attempts':0},connect_timeout=5,read_timeout=15))
bucket='ani-kf-stage2-'+namespace;other_bucket='ani-kf-stage2-'+other
body=('ANI scoped model identity '+namespace+'\n').encode();rows=[]
def denied(name,operation):
 try: operation()
 except ClientError as e:
  code=e.response['Error']['Code'];status=e.response['ResponseMetadata']['HTTPStatusCode']
  assert status==403 and code in ('AccessDenied','Forbidden'), 'failure must be policy denial'
  rows.append({'case':name,'http_status':status,'code':code})
 else: raise RuntimeError('scoped identity acquired forbidden operation: '+name)
assert c.get_bucket_location(Bucket=bucket)['ResponseMetadata']['HTTPStatusCode']==200
if mode=='writer':
 assert c.put_object(Bucket=bucket,Key=key,Body=body)['ResponseMetadata']['HTTPStatusCode']==200
 denied('writer-cannot-read-own-object',lambda:c.get_object(Bucket=bucket,Key=key))
 denied('writer-cannot-list',lambda:c.list_objects_v2(Bucket=bucket,Prefix='models/'))
 denied('writer-cannot-write-outside',lambda:c.put_object(Bucket=bucket,Key='outside/'+key.rsplit('/',1)[-1],Body=body))
 denied('writer-cannot-write-other-namespace',lambda:c.put_object(Bucket=other_bucket,Key=key+'.writer-denied',Body=body))
else:
 actual=c.get_object(Bucket=bucket,Key=key)['Body'].read();assert actual==body
 assert c.list_objects_v2(Bucket=bucket,Prefix=key)['ResponseMetadata']['HTTPStatusCode']==200
 denied('reader-cannot-write',lambda:c.put_object(Bucket=bucket,Key=key+'.reader-denied',Body=body))
 denied('reader-cannot-read-owner-marker',lambda:c.get_object(Bucket=bucket,Key='ani-installer/owner.json'))
 denied('reader-cannot-list-root',lambda:c.list_objects_v2(Bucket=bucket,Prefix=''))
 denied('reader-cannot-read-other-namespace',lambda:c.get_object(Bucket=other_bucket,Key=key))
print(json.dumps({'mode':mode,'namespace':namespace,'bucket':bucket,'key':key,'sha256':hashlib.sha256(body).hexdigest(),'positive':'PUT_OBJECT' if mode=='writer' else 'GET_IDENTICAL_BYTES_AND_PREFIX_LIST','denials':rows,'token_present':False}))
'''

NETWORK_CODE = r'''
import errno,http.client,json,pathlib,socket,ssl,sys
targets,api_targets,root_ca,model_ca,namespace,image=json.loads(sys.argv[1]),json.loads(sys.argv[2]),sys.argv[3],sys.argv[4],sys.argv[5],sys.argv[6]
assert not pathlib.Path('/var/run/secrets/kubernetes.io/serviceaccount/token').exists()
dns=sorted({x[4][0] for x in socket.getaddrinfo('kubernetes.default.svc.cluster.local',443,type=socket.SOCK_STREAM)})
assert api_targets[0]['address'] in dns
# Verified TLS to the healthy permitted destination distinguishes denied ports
# from a broken Pod network or resolver. This contains no S3 credentials.
host='ani-rustfs-svc.ani-platform.svc.cluster.local'
with socket.create_connection((host,9000),timeout=5) as raw:
 with ssl.create_default_context(cadata=model_ca).wrap_socket(raw,server_hostname=host) as secured: assert secured.getpeercert()
rows=[]
for t in targets:
 try:
  with socket.create_connection((t['address'],t['port']),timeout=2): raise RuntimeError('ordinary workload reached protected control endpoint '+t['name'])
 except (TimeoutError,socket.timeout): rows.append({**t,'result':'TIMEOUT_DENIED'})
 except OSError as e:
  assert e.errno in (errno.ETIMEDOUT,errno.EACCES,errno.EPERM), 'connection refusal does not prove policy denial'
  rows.append({**t,'result':'POLICY_DENIED','errno':e.errno})
body=json.dumps({'apiVersion':'v1','kind':'Pod','metadata':{'name':'unauthenticated-stage2-boundary'},'spec':{'restartPolicy':'Never','containers':[{'name':'probe','image':image}]}})
api_rows=[]
for t in api_targets:
 c=http.client.HTTPSConnection(t['address'],t['port'],context=ssl.create_default_context(cadata=root_ca),timeout=3)
 try:
  c.request('POST','/api/v1/namespaces/'+namespace+'/pods?dryRun=All',body=body,headers={'Content-Type':'application/json'})
  r=c.getresponse();r.read(65536);assert r.status in (401,403)
  api_rows.append({**t,'result':'AUTHORIZATION_DENIED','http_status':r.status})
 except (TimeoutError,socket.timeout): api_rows.append({**t,'result':'TRANSPORT_DENIED'})
 finally: c.close()
print(json.dumps({'dns':dns,'s3_tls_positive':True,'token_present':False,'control_targets':rows,'kubernetes_create_dry_run':api_rows}))
'''


def storage_pod(cluster, namespace, other, run_id, mode, report):
    name = "s3-" + mode + "-" + run_id
    key = "models/policy-" + run_id + "/proof.txt"
    secret = "ani-model-" + mode
    image = cluster.site["images"]["ani.local/kubeflow-execution:26.03-v1"]
    pod = obj("Pod", name, namespace, spec={"serviceAccountName": "notebook-workload" if mode == "writer" else "predictor-workload",
        "automountServiceAccountToken": False, "restartPolicy": "Never", "activeDeadlineSeconds": 150,
        "securityContext": {"runAsNonRoot": True, "runAsUser": 10001, "runAsGroup": 10001, "seccompProfile": {"type": "RuntimeDefault"}},
        "containers": [{"name": "probe", "image": image, "command": ["python", "-c", S3_CODE], "args": [mode, namespace, other, key],
            "env": [{"name": "ANI_S3_ENDPOINT", "value": stage2.S3_ENDPOINT}, {"name": "AWS_CA_BUNDLE", "value": "/etc/ani-model-ca/ca.crt"},
                    *[{"name": key, "valueFrom": {"secretKeyRef": {"name": secret, "key": key}}} for key in ("AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY")]],
            "resources": {"requests": {"cpu": "100m", "memory": "128Mi"}, "limits": {"cpu": "500m", "memory": "256Mi"}},
            "securityContext": {"allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}},
            "volumeMounts": [{"name": "ca", "mountPath": "/etc/ani-model-ca", "readOnly": True}]}],
        "volumes": [{"name": "ca", "configMap": {"name": "ani-model-ca"}}]})
    pod["metadata"]["labels"].update({"ani.io/stage2-workload": "true", "ani.io/run-id": run_id})
    if cluster.read(pod): raise ValueError("original storage-policy probe already exists; do not repeat its writes")
    cluster.apply([pod]); uid = cluster.writes[-1]["uid"]
    report["storage_pods"].append({"namespace": namespace, "name": name, "uid": uid, "mode": mode, "status": "IN_PROGRESS"})
    atomic(cluster.directory / "report.json", report)
    live = cluster.wait(pod, lambda v: v.get("status", {}).get("phase") in ("Failed", "Succeeded"), timeout=180)
    states = live.get("status", {}).get("containerStatuses", [])
    if (live["metadata"]["uid"] != uid or live["status"]["phase"] != "Succeeded" or len(states) != 1
            or states[0]["state"].get("terminated", {}).get("exitCode") != 0
            or states[0].get("imageID", "").rsplit("@", 1)[-1] != image.rsplit("@", 1)[-1]):
        raise RuntimeError("storage-policy probe failed; preserve the original Pod")
    evidence = json.loads(cluster.call(["-n", namespace, "logs", name], sensitive=True))
    if evidence.get("namespace") != namespace or evidence.get("mode") != mode or len(evidence.get("denials", [])) != 4:
        raise ValueError("storage-policy result differs")
    report["storage_pods"][-1].update(status="PASS", evidence=evidence)
    atomic(cluster.directory / "report.json", report)


def workload_boundary(cluster, original, report):
    namespace = original["namespace"]
    targets, services = [], []
    for name, ports in (("ml-pipeline", [8888, 8887]), ("metadata-grpc-service", [8080]), ("metadata-envoy-service", [9090]), ("mysql", [3306])):
        service = cluster.owned(obj("Service", name, "kubeflow"))
        slices = json.loads(cluster.call(["get", "endpointslices", "-n", "kubeflow", "-l", "kubernetes.io/service-name=" + name, "-o", "json"]))["items"]
        endpoints = [e for s in slices for e in s.get("endpoints", []) if e.get("conditions", {}).get("ready") and not e.get("conditions", {}).get("terminating")]
        if len(endpoints) != 1 or endpoints[0].get("targetRef", {}).get("kind") != "Pod": raise ValueError("protected endpoint is not healthy: " + name)
        ref = endpoints[0]["targetRef"]; pod = cluster.read(obj("Pod", ref["name"], "kubeflow"))
        if pod["metadata"]["uid"] != ref["uid"] or not any(v["type"] == "Ready" and v["status"] == "True" for v in pod["status"]["conditions"]):
            raise ValueError("protected endpoint Pod differs")
        services.append({"name": name, "uid": service["metadata"]["uid"], "pod_uid": ref["uid"]})
        targets += [{"name": name, "address": address, "port": port} for address in (service["spec"]["clusterIP"], endpoints[0]["addresses"][0]) for port in ports]
    api_service = cluster.read(obj("Service", "kubernetes", "default"))
    api_targets = [{"name": "kubernetes", "address": api_service["spec"]["clusterIP"], "port": 443}]
    api_targets += [{"name": "kubernetes-node", "address": address, "port": 6443} for address in cluster.site["node_addresses"]]
    root_ca = cluster.read(obj("ConfigMap", "kube-root-ca.crt", namespace))["data"]["ca.crt"]
    model_ca = cluster.owned(obj("ConfigMap", "ani-model-ca", namespace))["data"]["ca.crt"]
    nb_name, model_name = original["notebook"]["name"], original["inference_service"]["name"]
    native = cluster.owned(obj("Notebook", nb_name, namespace, api="kubeflow.org/v1"))
    isvc = cluster.owned(obj("InferenceService", model_name, namespace, api="serving.kserve.io/v1beta1"))
    if native["metadata"]["uid"] != original["notebook"]["uid"] or isvc["metadata"]["uid"] != original["inference_service"]["uid"]:
        raise ValueError("original workload owner identity differs")
    predictor = cluster.deployment(obj("Deployment", model_name + "-predictor", namespace, api="apps/v1"))
    probes = [(obj("Pod", nb_name + "-0", namespace), nb_name, "notebook-workload", {"ani-jupyter-auth", "ani-model-writer"}),
              (obj("Pod", predictor["pods"][0]["name"], namespace), "kserve-container", "predictor-workload", {"ani-model-reader"})]
    report["protected_services"] = services; report["ordinary_workloads"] = []
    for value, container, sa, allowed_secrets in probes:
        live = cluster.owned(value); spec = live["spec"]
        if spec.get("automountServiceAccountToken") is not False or spec.get("serviceAccountName") != sa:
            raise ValueError("ordinary workload token/SA contract differs")
        referenced = {e["valueFrom"]["secretKeyRef"]["name"] for c in spec.get("containers", []) + spec.get("initContainers", [])
                      for e in c.get("env", []) if "secretKeyRef" in e.get("valueFrom", {})}
        if referenced - allowed_secrets or any(v.get("secret") or v.get("projected") for v in spec.get("volumes", [])):
            raise ValueError("ordinary workload received an unexpected identity volume/Secret")
        storage_secret = "ani-model-writer" if sa == "notebook-workload" else "ani-model-reader"
        binding = cluster.owned(obj("Secret", storage_secret, namespace))
        for c in spec.get("containers", []) + spec.get("initContainers", []):
            for e in c.get("env", []):
                if e["name"] in ("AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"):
                    ref = e.get("valueFrom", {}).get("secretKeyRef")
                    if (ref and ref != {"name": storage_secret, "key": e["name"]}) or (not ref and e.get("value") != decode(binding, e["name"])):
                        raise ValueError("ordinary workload S3 credential differs from its dedicated role")
        args = [json.dumps(targets), json.dumps(api_targets), root_ca, model_ca, namespace, cluster.site["images"]["ani.local/kubeflow-execution:26.03-v1"]]
        evidence = json.loads(cluster.call(["-n", namespace, "exec", "pod/" + value["metadata"]["name"], "-c", container, "--", "python", "-c", NETWORK_CODE, *args], sensitive=True, timeout=120))
        if len(evidence.get("control_targets", [])) != len(targets) or len(evidence.get("kubernetes_create_dry_run", [])) != len(api_targets):
            raise ValueError("ordinary workload boundary targets are incomplete")
        report["ordinary_workloads"].append({"name": value["metadata"]["name"], "uid": live["metadata"]["uid"], "service_account": sa,
                                             "secret_references": sorted(referenced), "evidence": evidence})
        atomic(cluster.directory / "report.json", report)
    report["authorization"] = []
    for ns in stage2.NAMESPACES:
        for sa in ("notebook-workload", "predictor-workload"):
            for group, resource, verb, target in (("", "pods", "create", ns), ("", "persistentvolumeclaims", "create", ns),
                    ("trainer.kubeflow.org", "trainjobs", "create", ns), ("kubeflow.org", "notebooks", "create", ns),
                    ("serving.kserve.io", "inferenceservices", "create", ns), ("", "secrets", "get", "kubeflow")):
                query = {"apiVersion": "authorization.k8s.io/v1", "kind": "SubjectAccessReview", "spec": {
                    "user": "system:serviceaccount:" + ns + ":" + sa,
                    "groups": ["system:serviceaccounts", "system:serviceaccounts:" + ns, "system:authenticated"],
                    "resourceAttributes": {"namespace": target, "group": group, "resource": resource, "verb": verb}}}
                response = json.loads(cluster.call(["create", "-f", "-", "-o", "json"], query))
                if response["status"].get("allowed") is not False: raise ValueError("ordinary SA gained control permission")
                report["authorization"].append({"namespace": ns, "service_account": sa, "resource": resource, "verb": verb, "allowed": False})
    atomic(cluster.directory / "report.json", report)


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--site", required=True); p.add_argument("--main-report", type=pathlib.Path, required=True)
    p.add_argument("--report-dir", type=pathlib.Path, required=True)
    a = p.parse_args(); os.umask(0o077); assets(pathlib.Path(__file__).parent)
    source = a.main_report.read_bytes(); original = json.loads(source)
    if original.get("status") != "PASS" or original.get("main_flow") != "NATIVE_KERNEL_S3_SAME_MODEL_CORRECT_PREDICTION": raise ValueError("real successful main flow required")
    cluster = Cluster(load_site(a.site), a.report_dir)
    report = {"schema": "ani.kubeflow.stage2-protection.v1", "status": "IN_PROGRESS", "storage_pods": [],
              "main_report_sha256": hashlib.sha256(source).hexdigest(), "run_id": original["run_id"]}
    atomic(cluster.directory / "report.json", report)
    try:
        report["lock"] = product_lock(); report["cluster_uid"] = cluster.read(obj("Namespace", "kube-system"))["metadata"]["uid"]
        if report["cluster_uid"] != original["cluster_uid"]: raise ValueError("original cluster differs")
        workload_boundary(cluster, original, report)
        for mode in ("writer", "reader"):
            for index, namespace in enumerate(stage2.NAMESPACES):
                storage_pod(cluster, namespace, stage2.NAMESPACES[1-index], original["run_id"], mode, report)
        report.update(status="PASS", writes=cluster.writes, scope="two environment identities, not product tenant authorization")
    except BaseException as error:
        report.update(status="FAIL", error=str(error), writes=cluster.writes); atomic(cluster.directory / "report.json", report); cluster.evidence(); raise
    atomic(cluster.directory / "report.json", report); print(json.dumps({"status": report["status"], "report": str(cluster.directory / "report.json")}))


if __name__ == "__main__": main()

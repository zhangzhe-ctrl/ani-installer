#!/usr/bin/env python3
"""Lab acceptance: ordinary workload to live control targets, without credentials."""
import argparse
import hashlib
import json
import pathlib
import sys
import time
import uuid

parser = argparse.ArgumentParser()
parser.add_argument("--site", required=True)
parser.add_argument("--namespace", required=True)
parser.add_argument("--node", required=True)
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
if not output.is_absolute() or args.namespace not in site["tenants"]:
    raise ValueError("fixed tenant and absolute independent output required")
cluster = Cluster(site, output)
report = {"status": "IN_PROGRESS", "lock": product_lock(), "cleanup": "NOT_REQUESTED",
          "sourceSha256": hashlib.sha256(pathlib.Path(__file__).read_bytes()).hexdigest()}
atomic(output / "report.json", report)
try:
    report["networkCapability"] = cluster.network_capability()
    node = json.loads(cluster.call(["get", "node", args.node, "-o", "json"]))
    if not any(v["type"] == "InternalIP" and v["address"] in site["node_addresses"] for v in node["status"]["addresses"]):
        raise ValueError("node differs from fixed targets")
    targets, services = [], []
    for name, ports in (("ml-pipeline", [8888, 8887]), ("metadata-grpc-service", [8080]),
                        ("metadata-envoy-service", [9090]), ("mysql", [3306])):
        service = cluster.read(obj("Service", name, "kubeflow"))
        slices = json.loads(cluster.call(["get", "endpointslices", "-n", "kubeflow", "-l", "kubernetes.io/service-name=" + name, "-o", "json"]))["items"]
        endpoints = [e for s in slices for e in s.get("endpoints", []) if e.get("conditions", {}).get("ready") is True and not e.get("conditions", {}).get("terminating")]
        if len(endpoints) != 1 or endpoints[0].get("targetRef", {}).get("kind") != "Pod":
            raise ValueError("live control endpoint set differs: " + name)
        ref = endpoints[0]["targetRef"]
        pod = cluster.read(obj("Pod", ref["name"], "kubeflow"))
        if pod["metadata"]["uid"] != ref["uid"] or not any(c["type"] == "Ready" and c["status"] == "True" for c in pod["status"]["conditions"]):
            raise ValueError("live target Pod identity/readiness differs")
        services.append({"name": name, "uid": service["metadata"]["uid"], "podUid": ref["uid"], "clusterIP": service["spec"]["clusterIP"], "podIP": endpoints[0]["addresses"][0]})
        for address in (service["spec"]["clusterIP"], endpoints[0]["addresses"][0]):
            targets += [{"name": name, "address": address, "port": p} for p in ports]
    api_service = cluster.read(obj("Service", "kubernetes", "default"))
    api_address = api_service["spec"]["clusterIP"]
    if api_service["spec"]["ports"][0]["port"] != 443 or not api_address:
        raise ValueError("actual Kubernetes Service differs from the verified API contract")
    api_targets = [{"name": "kubernetes", "address": api_address, "port": 443}]
    api_targets += [{"name": "kubernetes-node", "address": address, "port": 6443} for address in site["node_addresses"]]
    # Public server trust is not an identity credential. Supply no client
    # certificate, key, SA token or Secret; all TLS requests remain verified.
    public_ca = cluster.read(obj("ConfigMap", "kube-root-ca.crt", args.namespace))["data"]["ca.crt"]
    command = r'''
import errno,http.client,json,os,pathlib,socket,ssl,sys
targets=json.loads(sys.argv[1])
policy=sys.argv[7]
assert policy in ('required','unsupported')
assert not pathlib.Path('/var/run/secrets/kubernetes.io/serviceaccount/token').exists()
assert not any(k in os.environ for k in ('AWS_ACCESS_KEY_ID','AWS_SECRET_ACCESS_KEY','MYSQL_ROOT_PASSWORD'))
dns=sorted({v[4][0] for v in socket.getaddrinfo('kubernetes.default.svc.cluster.local',443,type=socket.SOCK_STREAM)})
assert sys.argv[2] in dns
results=[]
for target in targets:
    if policy=='unsupported':
        with socket.create_connection((target['address'],target['port']),timeout=5): pass
        results.append({**target,'result':'REACHABLE_NETWORKPOLICY_UNSUPPORTED'})
        continue
    try:
        with socket.create_connection((target['address'],target['port']),timeout=2):
            raise RuntimeError('ordinary workload reached control port: '+target['name']+':'+str(target['port']))
    except (TimeoutError,socket.timeout):
        results.append({**target,'result':'TIMEOUT_DENIED'})
    except OSError as error:
        assert error.errno in (errno.ETIMEDOUT,errno.EACCES,errno.EPERM), 'unavailable target does not prove policy denial'
        results.append({**target,'result':'POLICY_DENIED','errno':error.errno})
context=ssl.create_default_context(cadata=sys.argv[3])
api_results=[]
body=json.dumps({'apiVersion':'v1','kind':'Pod','metadata':{'name':'unauthenticated-boundary-dryrun'},'spec':{'restartPolicy':'Never','containers':[{'name':'probe','image':sys.argv[5]}]}})
for target in json.loads(sys.argv[4]):
    connection=http.client.HTTPSConnection(target['address'],target['port'],context=context,timeout=3)
    try:
        connection.request('POST','/api/v1/namespaces/'+sys.argv[6]+'/pods?dryRun=All',body=body,headers={'Content-Type':'application/json'})
        response=connection.getresponse();data=response.read(65536)
        assert response.status in (401,403), 'ordinary anonymous workload acquired Kubernetes create authorization'
        api_results.append({**target,'result':'AUTHORIZATION_DENIED','httpStatus':response.status})
    except (TimeoutError,socket.timeout):
        assert policy=='required', 'KCN Kubernetes authorization must be reached and denied by the API'
        api_results.append({**target,'result':'TRANSPORT_DENIED'})
    finally:
        connection.close()
print(json.dumps({'dns':dns,'targets':results,'kubernetesCreateDryRuns':api_results,'serviceAccountTokenPresent':False,
    'networkIsolation':'unsupported' if policy=='unsupported' else 'denial_verified'},sort_keys=True))
'''
    name = "ani-kfp-boundary-" + uuid.uuid4().hex[:16]
    image = site["images"]["ani.local/kubeflow-execution:26.03-v1"]
    pod = obj("Pod", name, args.namespace, spec={"serviceAccountName": "trainer-workload", "automountServiceAccountToken": False,
        "nodeSelector": {"kubernetes.io/hostname": args.node}, "restartPolicy": "Never", "activeDeadlineSeconds": 120,
        "securityContext": {"runAsNonRoot": True, "runAsUser": 1000, "runAsGroup": 1000, "seccompProfile": {"type": "RuntimeDefault"}},
        "containers": [{"name": "probe", "image": image, "command": ["python", "-c", command],
            "args": [json.dumps(targets), api_address, public_ca, json.dumps(api_targets), image, args.namespace, site["network_policy"]],
            "securityContext": {"allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}, "readOnlyRootFilesystem": True},
            "resources": {"requests": {"cpu": "100m", "memory": "128Mi"}, "limits": {"cpu": "500m", "memory": "256Mi"}}}]})
    if cluster.read(pod):
        raise ValueError("name occupied; no replacement")
    cluster.apply([pod])
    uid = cluster.writes[-1]["uid"]
    report.update(pod={"name": name, "uid": uid, "creationResult": "CONFIRMED"}, services=services,
                  kubernetesService={"uid": api_service["metadata"]["uid"], "clusterIP": api_address},
                  publicServerCaSha256=hashlib.sha256(public_ca.encode()).hexdigest(), identityCredentialsSupplied=False)
    atomic(output / "report.json", report)
    live = cluster.wait(pod, lambda p: p["metadata"]["uid"] == uid and p.get("status", {}).get("phase") in ("Failed", "Succeeded"), timeout=150)
    states = live["status"].get("containerStatuses", [])
    if live["status"]["phase"] != "Succeeded" or len(states) != 1 or states[0]["state"].get("terminated", {}).get("exitCode") != 0 or states[0].get("imageID", "").rsplit("@", 1)[-1] != image.rsplit("@", 1)[-1]:
        raise RuntimeError("actual boundary probe exit/digest differs; preserve Pod")
    evidence = json.loads(cluster.call(["logs", name, "-n", args.namespace, "-c", "probe"]))
    if len(evidence["targets"]) != len(targets) or len(evidence["kubernetesCreateDryRuns"]) != len(api_targets):
        raise RuntimeError("boundary probe omitted targets")
    report.update(status="ORDINARY_WORKLOAD_AUTHORIZATION_CHECKED" if site["network_policy"] == "unsupported" else "ORDINARY_WORKLOAD_CONTROL_DENIED",
                  evidence=evidence, imageID=states[0]["imageID"])
except BaseException as error:
    report.update(status="FAIL", error=str(error))
    atomic(output / "report.json", report)
    raise
atomic(output / "report.json", report)
print(json.dumps({"status": report["status"], "report": str(output / "report.json")}))

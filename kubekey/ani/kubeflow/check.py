#!/usr/bin/env python3
"""Readiness/protocol checker; never repairs CNI/CSI or deletes/restarts Pods.

This check does not attest EAC success/failure/stop/workspace integration.
Those bounded probes produce independent records after control-plane checks.
"""
import argparse
import datetime
import hashlib
import json
import pathlib
import socket
import ssl
import urllib.error
import urllib.parse
import urllib.request

from common import Cluster, assets, atomic, identity, load_site
from install import checked_policy, materialize
from resources import RELEASE, entry, obj, runtime
import stage2


def check(cluster, root, report):
    site = cluster.site
    report["clusterUid"] = cluster.read(obj("Namespace", "kube-system"))["metadata"]["uid"]
    values = materialize(root, site)
    values += entry(site, site["images"])
    report["deployments"] = {}
    for value in values:
        existing = cluster.owned(value)
        if not existing:
            raise RuntimeError("required installed object is absent: " + identity(value))
        if value["kind"] == "CustomResourceDefinition" and not any(c["type"] == "Established" and c["status"] == "True" for c in existing.get("status", {}).get("conditions", [])):
            raise RuntimeError("CRD is not Established: " + identity(value))
        if value["kind"] == "Deployment":
            evidence = cluster.deployment(value)
            desired = {c["name"]: c["image"].rsplit("@", 1)[1] for c in value["spec"]["template"]["spec"]["containers"]}
            for pod in evidence["pods"]:
                if not pod["containers"] or any(c["name"] not in desired or not c["imageID"] or c["imageID"].rsplit("@", 1)[-1] != desired[c["name"]] for c in pod["containers"]):
                    raise RuntimeError("running container platform digest differs: " + identity(value))
            report["deployments"][identity(value)] = evidence
        elif value["kind"] in ("MutatingWebhookConfiguration", "ValidatingWebhookConfiguration"):
            if any(not w["clientConfig"].get("caBundle") for w in existing["webhooks"]):
                raise RuntimeError("webhook CA is absent: " + identity(value))
        elif value["kind"] == "PersistentVolumeClaim" and existing.get("status", {}).get("phase") != "Bound":
            raise RuntimeError("required database claim is not Bound")
    report["namespaces"] = {name: cluster.owned(obj("Namespace", name))["metadata"]["uid"] for name in ("kubeflow", "kubeflow-system", *site["tenants"])}
    for namespace, services in (("kubeflow", ["ml-pipeline", "mysql", "metadata-grpc-service", "metadata-envoy-service", "ani-kfp-entry"]),
                                ("kubeflow-system", ["jobset-webhook-service", "kubeflow-trainer-controller-manager"])):
        for service in services:
            slices = json.loads(cluster.call(["get", "endpointslices", "-n", namespace, "-l", "kubernetes.io/service-name=" + service, "-o", "json"]))["items"]
            if not any(e.get("conditions", {}).get("ready") is True and e.get("addresses") for item in slices for e in item.get("endpoints", [])):
                raise RuntimeError("no current ready EndpointSlice: " + namespace + "/" + service)
    public_ca = cluster.owned(obj("ConfigMap", "ani-kfp-ca", "kubeflow"))["data"]["ca.crt"]
    context = ssl.create_default_context(cadata=public_ca)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context))
    address = "https://" + site["entry_address"] + ":" + str(site["http_port"])
    requests = []

    def request(path, headers=None, expected=(200,)):
        try:
            with opener.open(urllib.request.Request(address + path, headers=headers or {}), timeout=30) as response:
                status, body = response.status, response.read()
        except urllib.error.HTTPError as error:
            status, body = error.code, error.read()
        requests.append({"path": path, "status": status, "bodySha256": hashlib.sha256(body).hexdigest()})
        if status not in expected:
            raise RuntimeError("TLS API protocol expectation differs: " + path + " status=" + str(status))
        return body

    request("/healthz")
    health = json.loads(request("/apis/v1beta1/healthz"))
    if health.get("tag_name") != "2.16.0":
        raise RuntimeError("KFP health version differs from the locked release")
    tenant = site["tenants"][0]
    path = "/apis/v2beta1/experiments?namespace=" + urllib.parse.quote(tenant)
    request(path, expected=(401, 403))
    request(path, {"kubeflow-userid": "system:serviceaccount:" + tenant + ":api-client"}, expected=(400,))
    requests.append({"operation": "TokenRequest", "namespace": tenant, "serviceAccount": "api-client", "audience": "pipelines.kubeflow.org", "duration": "10m"})
    token = cluster.call(["create", "token", "api-client", "-n", tenant, "--audience=pipelines.kubeflow.org", "--duration=10m"], sensitive=True).strip()
    request(path, {"Authorization": "Bearer " + token})
    if len(site["tenants"]) > 1:
        request("/apis/v2beta1/experiments?namespace=" + urllib.parse.quote(site["tenants"][1]), {"Authorization": "Bearer " + token}, expected=(403,))
    # TLS validation on the separate gRPC port. Full gRPC authorization and
    # duplicate/variant probes remain ENV05; a TLS handshake is not that pass.
    with socket.create_connection((site["entry_address"], site["grpc_port"]), timeout=15) as connection:
        grpc_context = ssl.create_default_context(cadata=public_ca)
        grpc_context.set_alpn_protocols(["h2"])
        with grpc_context.wrap_socket(connection, server_hostname=site["entry_address"]) as secured:
            if secured.selected_alpn_protocol() != "h2":
                raise RuntimeError("gRPC TLS entry did not negotiate h2")
    approved_runtime = runtime(site["images"])
    installed = cluster.owned(approved_runtime)
    if not installed:
        raise RuntimeError("versioned Runtime is absent")
    expected = json.loads(cluster.call(["apply", "--server-side", "--field-manager=ani-kubeflow", "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json"], approved_runtime))
    if installed["spec"] != expected["spec"]:
        raise RuntimeError("versioned Runtime spec differs")
    for namespace in site["tenants"]:
        admission = obj("TrainJob", "ani-kfp-control-check", namespace, api="trainer.kubeflow.org/v1alpha1", spec={"runtimeRef": {"name": approved_runtime["metadata"]["name"]}, "trainer": {"numNodes": 1, "numProcPerNode": 1}})
        cluster.call(["create", "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json"], admission)
    for name in ("ani-kfp-workspace", "ani-kfp-trainjob"):
        policy = cluster.owned(obj("ValidatingAdmissionPolicy", name, api="admissionregistration.k8s.io/v1"))
        if not policy or not checked_policy(policy):
            raise RuntimeError("admission expression type checking differs: " + name)
    stage2.check(cluster, root, report)
    report.update(status="CONTROL_PLANE_CHECKED", httpRequests=requests, grpcTls="h2_verified / grpc_authorization_NOT_RUN",
                  runtime={"name": installed["metadata"]["name"], "uid": installed["metadata"]["uid"]},
                  acceptance="EAC01-EAC14_NOT_ATTESTED_BY_THIS_CHECK")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--site", required=True)
    parser.add_argument("--kubeconfig", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    site = load_site(args.site, args.kubeconfig)
    root = assets(pathlib.Path(__file__).parent)
    output = pathlib.Path(args.output)
    if not output.is_absolute():
        raise ValueError("checker output must be the explicit absolute run directory")
    output.mkdir(mode=0o700, parents=True, exist_ok=True)
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")
    cluster = Cluster(site, output / ("kubeflow-check-" + stamp))
    report = {"schema": "ani.kubeflow.check.v1", "release": RELEASE, "status": "IN_PROGRESS", "persistentObjectWrites": 0}
    try:
        check(cluster, root, report)
    except BaseException as error:
        report.update(status="FAIL", error=str(error))
        atomic(cluster.directory / "report.json", report)
        cluster.evidence()
        raise
    atomic(cluster.directory / "report.json", report)
    print(json.dumps({"status": report["status"], "report": str(cluster.directory / "report.json")}))


if __name__ == "__main__":
    main()

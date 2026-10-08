"""The Notebook/KServe portion of the same first-install role."""
import hashlib
import json
import secrets

from common import identity, decode
from resources import obj, network, peers, ports

WORKSPACE_IMAGE = "ani.local/kubeflow-jupyter:26.03-stage2-v1"
NAMESPACES = ("ani-kf-stage2-a", "ani-kf-stage2-b")
RUNTIME = "kserve-sklearnserver"
S3_ENDPOINT = "https://ani-rustfs-svc.ani-platform.svc.cluster.local:9000"


def controller_ca(ca=None):
    # KServe 0.16 reads this source in its controller namespace, then copies
    # cabundle.crt to global-ca-bundle in the InferenceService namespace.
    value = obj("ConfigMap", "ani-model-ca", "kserve")
    if ca is not None:
        value["data"] = {"cabundle.crt": ca}
    return value


def materialize(root, site):
    lock = json.loads((root / "stage2-overlay.lock.json").read_text())
    raw = (root / "stage2-resources.json").read_bytes()
    if lock["release"] != site["release"] or hashlib.sha256(raw).hexdigest() != lock["resources_sha256"]:
        raise ValueError("stage2 overlay differs from its reviewed source bytes")
    text = raw.decode()
    for original, image in site["images"].items():
        text = text.replace("ANI_IMAGE_" + original, image)
    if "ANI_IMAGE_" in text:
        raise ValueError("stage2 image material is missing")
    values = json.loads(text)
    if len(values) != lock["resource_count"]:
        raise ValueError("stage2 overlay count differs")
    return values


def namespace_contract(site, namespace, ca=None):
    """Separate probe workspace quota; the KFP execution admission stays intact."""
    labels = {"ani.io/stage2-probe": "true"}
    ns = obj("Namespace", namespace)
    ns["metadata"]["labels"].update(labels)
    values = [ns,
        obj("ServiceAccount", "notebook-workload", namespace, automountServiceAccountToken=False),
        obj("ServiceAccount", "predictor-workload", namespace, automountServiceAccountToken=False,
            secrets=[{"name": "ani-model-reader"}]),
        obj("ResourceQuota", "ani-stage2-budget", namespace, spec={"hard": {
            "count/persistentvolumeclaims": "1", "requests.storage": site["workspace_max_size"],
            "requests.cpu": "4", "requests.memory": "8Gi", "limits.cpu": "8", "limits.memory": "16Gi", "count/pods": "8"}}),
        obj("LimitRange", "ani-stage2-budget", namespace, spec={"limits": [{"type": "Container",
            "defaultRequest": {"cpu": "100m", "memory": "128Mi"}, "default": {"cpu": "1", "memory": "2Gi"},
            "max": {"cpu": "2", "memory": "4Gi"}}]})]
    dns = {"to": [peers("kube-system", {"k8s-app": "kube-dns"})],
           "ports": [{"protocol": "UDP", "port": 53}, {"protocol": "TCP", "port": 53}]}
    values += [network("ani-stage2-default", namespace, {}, ingress=[], egress=[dns]),
        network("ani-stage2-s3", namespace, {"ani.io/stage2-workload": "true"}, egress=[
            {"to": [peers("ani-platform", {"app.kubernetes.io/instance": "ani-rustfs"})], "ports": ports(9000)}])]
    if ca is not None:
        values.append(obj("ConfigMap", "ani-model-ca", namespace, data={"ca.crt": ca}))
    return values


def notebook(site, namespace, name, run_id):
    """An administrator creates the dedicated PVC separately from Notebook CR."""
    labels = {"ani.io/stage2-workload": "true", "ani.io/run-id": run_id}
    pvc = obj("PersistentVolumeClaim", "ani-notebook-workspace", namespace, spec={
        "storageClassName": site["workspace_class"], "accessModes": ["ReadWriteMany"],
        "resources": {"requests": {"storage": site["workspace_max_size"]}}})
    pvc["metadata"]["labels"].update(labels)
    env = [{"name": "JUPYTER_TOKEN", "valueFrom": {"secretKeyRef": {"name": "ani-jupyter-auth", "key": "token"}}},
           {"name": "ANI_S3_ENDPOINT", "value": S3_ENDPOINT},
           {"name": "ANI_MODEL_BUCKET", "value": "ani-kf-stage2-" + namespace},
           {"name": "ANI_MODEL_PREFIX", "value": "models"},
           {"name": "AWS_CA_BUNDLE", "value": "/etc/ani-model-ca/ca.crt"}]
    for variable, key in (("AWS_ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID"), ("AWS_SECRET_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY")):
        env.append({"name": variable, "valueFrom": {"secretKeyRef": {"name": "ani-model-writer", "key": key}}})
    pod = {"serviceAccountName": "notebook-workload", "automountServiceAccountToken": False,
        "securityContext": {"runAsNonRoot": True, "runAsUser": 1000, "runAsGroup": 1000, "fsGroup": 1000,
                            "fsGroupChangePolicy": "OnRootMismatch", "seccompProfile": {"type": "RuntimeDefault"}},
        "containers": [{"name": name, "image": site["images"][WORKSPACE_IMAGE], "env": env,
            "ports": [{"name": "notebook-port", "containerPort": 8888}],
            "resources": {"requests": {"cpu": "500m", "memory": "512Mi"}, "limits": {"cpu": "2", "memory": "2Gi"}},
            "securityContext": {"allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}},
            "volumeMounts": [{"name": "workspace", "mountPath": "/home/jovyan"},
                             {"name": "ca", "mountPath": "/etc/ani-model-ca", "readOnly": True}]}],
        "volumes": [{"name": "workspace", "persistentVolumeClaim": {"claimName": pvc["metadata"]["name"]}},
                    {"name": "ca", "configMap": {"name": "ani-model-ca"}}]}
    value = obj("Notebook", name, namespace, api="kubeflow.org/v1", spec={"template": {"spec": pod}})
    value["metadata"]["labels"].update(labels)
    return pvc, value


def inference(namespace, name, model_uri, run_id, secret="ani-model-reader"):
    value = obj("InferenceService", name, namespace, api="serving.kserve.io/v1beta1", spec={"predictor": {
        "serviceAccountName": "predictor-workload", "automountServiceAccountToken": False,
        "minReplicas": 1, "maxReplicas": 1,
        "model": {"modelFormat": {"name": "sklearn", "version": "1"}, "runtime": RUNTIME,
                  "storageUri": model_uri, "resources": {
                      "requests": {"cpu": "500m", "memory": "512Mi"}, "limits": {"cpu": "1", "memory": "2Gi"}}}}})
    value["metadata"]["labels"].update({"ani.io/stage2-workload": "true", "ani.io/run-id": run_id})
    value["metadata"]["annotations"] = {"serving.kserve.io/deploymentMode": "Standard",
                                         "serving.kserve.io/secretName": secret}
    return value


def preflight(cluster, root):
    values = materialize(root, cluster.site)
    cluster.owned(controller_ca())
    for value in values:
        cluster.owned(value)
    for namespace in NAMESPACES:
        for value in namespace_contract(cluster.site, namespace) + [obj("ConfigMap", "ani-model-ca", namespace)]:
            cluster.owned(value)
        for name in ("ani-jupyter-auth", "ani-model-writer", "ani-model-reader"):
            cluster.owned(obj("Secret", name, namespace))
    return values


def install(cluster, root, report, ca):
    values = preflight(cluster, root)
    cluster.apply([v for v in values if v["kind"] == "Namespace"])
    crds = [v for v in values if v["kind"] == "CustomResourceDefinition"]
    cluster.apply(crds)
    for value in crds:
        cluster.wait(value, lambda v: any(c["type"] == "Established" and c["status"] == "True"
                                       for c in v.get("status", {}).get("conditions", [])))
    late = ("Namespace", "CustomResourceDefinition", "Certificate", "Deployment",
            "MutatingWebhookConfiguration", "ValidatingWebhookConfiguration",
            "ClusterServingRuntime", "ClusterStorageContainer")
    cluster.apply([v for v in values if v["kind"] not in late])
    cluster.apply([controller_ca(ca)])
    certificates = [v for v in values if v["kind"] == "Certificate"]
    cluster.apply(certificates)
    for value in certificates:
        cluster.wait(value, lambda v: any(c["type"] == "Ready" and c["status"] == "True"
                                       for c in v.get("status", {}).get("conditions", [])))
    controllers = [v for v in values if v["kind"] == "Deployment"]
    cluster.apply(controllers)
    for value in controllers:
        report["deployments"][identity(value)] = cluster.deployment(value)
    webhooks = [v for v in values if v["kind"] in ("MutatingWebhookConfiguration", "ValidatingWebhookConfiguration")]
    cluster.apply(webhooks)
    for value in webhooks:
        cluster.wait(value, lambda v: all(w["clientConfig"].get("caBundle") for w in v["webhooks"]))
    cluster.apply([v for v in values if v["kind"] in ("ClusterServingRuntime", "ClusterStorageContainer")])
    for namespace in NAMESPACES:
        contracts = namespace_contract(cluster.site, namespace, ca)
        cluster.apply(contracts[:1])
        cluster.apply(contracts[1:])
        auth = obj("Secret", "ani-jupyter-auth", namespace, type="Opaque")
        old = cluster.owned(auth)
        if old:
            token = decode(old, "token")
            if old.get("type") != "Opaque" or len(token) != 64 or any(c not in "0123456789abcdef" for c in token):
                raise ValueError("Jupyter token binding differs; no rotation")
        else:
            auth["stringData"] = {"token": secrets.token_hex(32)}
            cluster.apply([auth])
    report["completedPhases"].append("NOTEBOOK_KSERVE_STANDARD_CONTROLLERS_WEBHOOKS_RUNTIME")


def check(cluster, root, report):
    values = materialize(root, cluster.site)
    for value in values:
        existing = cluster.owned(value)
        if not existing:
            raise RuntimeError("required stage2 object is absent: " + identity(value))
        if value["kind"] == "Deployment":
            evidence = cluster.deployment(value)
            expected = {c["name"]: c["image"].rsplit("@", 1)[1] for c in value["spec"]["template"]["spec"]["containers"]}
            for pod in evidence["pods"]:
                if not pod["containers"] or any(c["imageID"].rsplit("@", 1)[-1] != expected.get(c["name"])
                                               for c in pod["containers"]):
                    raise RuntimeError("stage2 running digest differs: " + identity(value))
            report["deployments"][identity(value)] = evidence
        elif value["kind"] == "CustomResourceDefinition":
            if not any(c["type"] == "Established" and c["status"] == "True" for c in existing.get("status", {}).get("conditions", [])):
                raise RuntimeError("stage2 CRD is not Established")
        elif value["kind"] in ("MutatingWebhookConfiguration", "ValidatingWebhookConfiguration"):
            if not all(w["clientConfig"].get("caBundle") for w in existing["webhooks"]):
                raise RuntimeError("stage2 webhook lacks its CA")
        elif value["kind"] in ("ConfigMap", "ClusterServingRuntime", "ClusterStorageContainer"):
            dry = json.loads(cluster.call(["apply", "--server-side", "--field-manager=ani-kubeflow",
                "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json"], value))
            key = "data" if value["kind"] == "ConfigMap" else "spec"
            if existing[key] != dry[key]:
                raise RuntimeError("stage2 installed configuration differs: " + identity(value))
    runtime_list = json.loads(cluster.call(["get", "clusterservingruntimes", "-o", "json"]))["items"]
    if [v["metadata"]["name"] for v in runtime_list] != [RUNTIME]:
        raise RuntimeError("the environment has runtimes beyond the fixed sklearn contract")
    identities = []
    public_ca = cluster.owned(obj("ConfigMap", "ani-kfp-ca", "kubeflow"))["data"]["ca.crt"]
    source_ca = cluster.owned(controller_ca())
    if not source_ca or source_ca.get("data") != {"cabundle.crt": public_ca}:
        raise RuntimeError("KServe controller model CA source differs from the installed public CA")
    for namespace in NAMESPACES:
        for value in namespace_contract(cluster.site, namespace):
            existing = cluster.owned(value)
            if not existing:
                raise RuntimeError("stage2 workload protection is absent: " + identity(value))
            if value["kind"] == "ServiceAccount":
                if existing.get("automountServiceAccountToken") is not False:
                    raise RuntimeError("stage2 workload SA token automount differs")
                if value["metadata"]["name"] == "predictor-workload" and existing.get("secrets") != [{"name": "ani-model-reader"}]:
                    raise RuntimeError("predictor SA model reader binding differs")
            elif value["kind"] != "Namespace":
                dry = json.loads(cluster.call(["apply", "--server-side", "--field-manager=ani-kubeflow",
                    "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json"], value))
                if existing["spec"] != dry["spec"]:
                    raise RuntimeError("stage2 workload protection differs: " + identity(value))
        reader = cluster.owned(obj("Secret", "ani-model-reader", namespace))
        writer = cluster.owned(obj("Secret", "ani-model-writer", namespace))
        if not reader or not writer or any(set(v.get("data", {})) != {"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} for v in (reader, writer)):
            raise RuntimeError("stage2 model credential fields differ")
        if decode(reader, "AWS_ACCESS_KEY_ID") == decode(writer, "AWS_ACCESS_KEY_ID"):
            raise RuntimeError("model writer and reader share one identity")
        ca = cluster.owned(obj("ConfigMap", "ani-model-ca", namespace))
        if not ca or ca.get("data") != {"ca.crt": public_ca}:
            raise RuntimeError("stage2 model CA differs from the installed public CA")
        identities.append({"namespace": namespace, "writer_secret": "ani-model-writer", "reader_secret": "ani-model-reader",
                           "bucket": "ani-kf-stage2-" + namespace, "prefix": "models/", "ca_configmap": "ani-model-ca"})
    report["stage2"] = {"status": "CONTROLLERS_CHECKED", "main_flow": "NOT_ATTESTED",
                         "namespaces": list(NAMESPACES), "runtime": RUNTIME, "model_identities": identities,
                         "kserve_ca_source": {"namespace": "kserve", "name": "ani-model-ca", "key": "cabundle.crt",
                                              "sha256": hashlib.sha256(public_ca.encode()).hexdigest()},
                         "notebook_workspace": {"storage_class": cluster.site["workspace_class"],
                                                "size": cluster.site["workspace_max_size"], "uid_gid": "1000:1000"}}

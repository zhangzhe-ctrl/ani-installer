"""Runtime helpers for the existing ANI first-install role (stdlib only)."""
import base64
import hashlib
import json
import os
import pathlib
import re
import subprocess
import time

from resources import RELEASE

PLURALS = {
    "Namespace": "namespaces", "ServiceAccount": "serviceaccounts", "Secret": "secrets",
    "ConfigMap": "configmaps", "Service": "services", "PersistentVolumeClaim": "persistentvolumeclaims",
    "Deployment": "deployments", "Role": "roles", "ClusterRole": "clusterroles",
    "RoleBinding": "rolebindings", "ClusterRoleBinding": "clusterrolebindings",
    "CustomResourceDefinition": "customresourcedefinitions", "PriorityClass": "priorityclasses",
    "MutatingWebhookConfiguration": "mutatingwebhookconfigurations", "ValidatingWebhookConfiguration": "validatingwebhookconfigurations",
    "Certificate": "certificates", "NetworkPolicy": "networkpolicies", "ResourceQuota": "resourcequotas",
    "LimitRange": "limitranges", "ValidatingAdmissionPolicy": "validatingadmissionpolicies",
    "ValidatingAdmissionPolicyBinding": "validatingadmissionpolicybindings", "ClusterTrainingRuntime": "clustertrainingruntimes",
    "Pod": "pods", "Job": "jobs", "TrainJob": "trainjobs", "Workflow": "workflows",
    "Notebook": "notebooks", "StatefulSet": "statefulsets", "InferenceService": "inferenceservices",
    "Issuer": "issuers", "ClusterServingRuntime": "clusterservingruntimes",
    "ClusterStorageContainer": "clusterstoragecontainers",
}


def atomic(path, value):
    path = pathlib.Path(path)
    temporary = path.with_suffix(path.suffix + ".new")
    with temporary.open("x") as stream:
        os.chmod(temporary, 0o600)
        json.dump(value, stream, indent=2)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, path)


def load_site(path, kubeconfig=None):
    site = json.loads(pathlib.Path(path).read_text())
    if site["release"] != RELEASE or site["workspace_mode"] != "managed-execution-pvc-v1":
        raise ValueError("unsupported Kubeflow release or workspace mode")
    if kubeconfig is not None and kubeconfig != site["kubeconfig"]:
        raise ValueError("checker kubeconfig differs from this install run")
    for key in ("kubeconfig", "artifact_root", "logs_dir", "connections_dir"):
        if not pathlib.Path(site[key]).is_absolute() or not pathlib.Path(site[key]).exists():
            raise ValueError("missing fixed execution path: " + key)
    for original, image in site["images"].items():
        if not image.startswith(site["registry"] + "/") or not re.fullmatch(r".+@sha256:[a-f0-9]{64}", image):
            raise ValueError("image is not mapped to an approved offline platform digest: " + original)
    return site


def endpoint(obj):
    version = obj["apiVersion"]
    prefix = "/api/v1" if version == "v1" else "/apis/" + version
    namespace = obj["metadata"].get("namespace")
    if namespace:
        prefix += "/namespaces/" + namespace
    return prefix + "/" + PLURALS[obj["kind"]] + "/" + obj["metadata"]["name"]


def identity(obj):
    return "/".join((obj["kind"], obj["metadata"].get("namespace", "_"), obj["metadata"]["name"]))


class Cluster:
    def __init__(self, site, directory):
        self.site = site
        self.directory = pathlib.Path(directory)
        self.directory.mkdir(mode=0o700)
        self.command = ["kubectl", "--kubeconfig", site["kubeconfig"], "--request-timeout=30s"]
        self.writes = []
        self.sequence = 0

    def call(self, args, value=None, sensitive=False, timeout=45):
        result = subprocess.run(self.command + args, input=None if value is None else json.dumps(value),
                                text=True, capture_output=True, timeout=timeout)
        if result.returncode:
            if sensitive:
                raise RuntimeError("kubectl sensitive operation failed rc=" + str(result.returncode))
            raise RuntimeError("kubectl " + " ".join(args[:3]) + ": " + result.stderr.strip())
        return result.stdout

    def read(self, obj):
        result = subprocess.run(self.command + ["get", "--raw", endpoint(obj)], text=True, capture_output=True, timeout=45)
        if result.returncode:
            if "(NotFound)" in result.stderr:
                return None
            raise RuntimeError("object lookup failed: " + identity(obj) + ": " + result.stderr.strip())
        return json.loads(result.stdout)

    def owned(self, obj):
        existing = self.read(obj)
        if existing and existing["metadata"].get("labels", {}).get("ani.io/managed-by") != self.site["owner"]:
            raise RuntimeError("foreign object; refusing mutation: " + identity(obj))
        if existing and existing["metadata"].get("deletionTimestamp"):
            raise RuntimeError("object is terminating: " + identity(obj))
        return existing

    def prepare(self, objects):
        # Preflight the whole dependency group before its first mutation.
        prepared = []
        for obj in objects:
            # ObjectFieldSelector is atomic for SSA. Creation defaults its
            # apiVersion to v1; replay must explicitly retain that same value
            # instead of conflicting with the initial create operation.
            if obj.get("apiVersion") == "apps/v1" and obj["kind"] == "Deployment":
                pod = obj["spec"]["template"]["spec"]
                for container in pod.get("containers", []) + pod.get("initContainers", []):
                    for variable in container.get("env", []):
                        selector = variable.get("valueFrom", {}).get("fieldRef")
                        if selector is not None:
                            selector.setdefault("apiVersion", "v1")
            # Rule lists are atomic in admissionregistration/v1. A create
            # response defaults omitted scope to '*'; a later SSA with an
            # omitted scope conflicts with the initial create manager. Make
            # this API default explicit for both create and replay. Other
            # differences retain their normal SSA conflict protection.
            if obj.get("apiVersion") == "admissionregistration.k8s.io/v1" and obj["kind"] in ("MutatingWebhookConfiguration", "ValidatingWebhookConfiguration"):
                for webhook in obj.get("webhooks", []):
                    for rule in webhook.get("rules", []):
                        rule.setdefault("scope", "*")
            obj["metadata"].setdefault("labels", {})["ani.io/managed-by"] = self.site["owner"]
            old = self.owned(obj)
            if old and obj["kind"] == "Secret":
                # Controller-generated cert bytes and stable credentials survive retries.
                controller_placeholder = (obj["metadata"].get("namespace") == "kubeflow-system"
                    and obj["metadata"]["name"] in ("jobset-webhook-server-cert", "kubeflow-trainer-webhook-cert")
                    and obj.get("data", {}) == {} and not obj.get("stringData"))
                if "data" in obj and obj["data"] != old.get("data") and not controller_placeholder:
                    raise RuntimeError("refusing credential rotation: " + identity(obj))
                continue
            if old:
                obj["metadata"].update(uid=old["metadata"]["uid"], resourceVersion=old["metadata"]["resourceVersion"])
            secret = obj["kind"] == "Secret"
            manager = "ani-kubeflow"
            replace_owned = False
            if old and obj["kind"] == "Role" and obj["metadata"].get("namespace") in self.site["tenants"] and obj["metadata"]["name"] == "ani-kfp-api-client":
                rules_managers = {m["manager"] for m in old["metadata"].get("managedFields", []) if "f:rules" in m.get("fieldsV1", {})}
                if not rules_managers or rules_managers - {"kubectl-create", manager}:
                    raise RuntimeError("foreign tenant API rules manager; refusing mutation: " + identity(obj))
                if old.get("rules") != obj.get("rules"):
                    metadata = {**old["metadata"], **obj["metadata"]}
                    metadata["labels"] = {**old["metadata"].get("labels", {}), **obj["metadata"].get("labels", {})}
                    metadata.pop("managedFields", None)
                    obj["metadata"] = metadata
                    replace_owned = True
            if old and obj["kind"] == "ValidatingAdmissionPolicy" and obj["metadata"]["name"] in ("ani-kfp-workspace", "ani-kfp-trainjob"):
                # Earlier role attempts used kubectl's default create manager.
                # Its Update-owned atomic lists conflict even with SSA using
                # the same name. Replace only these owned policies, conditioned
                # on their actual UID+resourceVersion, preserving metadata.
                spec_managers = {m["manager"] for m in old["metadata"].get("managedFields", []) if "f:spec" in m.get("fieldsV1", {})}
                if spec_managers - {"kubectl-create", "ani-kubeflow"}:
                    raise RuntimeError("foreign policy spec manager; refusing mutation: " + identity(obj))
                if "kubectl-create" in spec_managers:
                    replace_owned = True
                    metadata = {**old["metadata"], **obj["metadata"]}
                    metadata["labels"] = {**old["metadata"].get("labels", {}), **obj["metadata"].get("labels", {})}
                    metadata.pop("managedFields", None)
                    obj["metadata"] = metadata
            if old and obj["kind"] == "ConfigMap" and obj["metadata"].get("namespace") == "kubeflow" and obj["metadata"]["name"] == "ani-kfp-entry":
                # This role's create operation owns nginx.conf with Update.
                # Use optimistic concurrency for its reviewed configuration
                # update instead of forcing SSA across that ownership.
                data_managers = {m["manager"] for m in old["metadata"].get("managedFields", []) if "f:data" in m.get("fieldsV1", {})}
                if data_managers != {manager} or set(old.get("data", {})) != {"nginx.conf"} or old.get("binaryData"):
                    raise RuntimeError("foreign entry configuration; refusing mutation: " + identity(obj))
                metadata = {**old["metadata"], **obj["metadata"]}
                metadata["labels"] = {**old["metadata"].get("labels", {}), **obj["metadata"].get("labels", {})}
                metadata.pop("managedFields", None)
                obj["metadata"] = metadata
                if "immutable" in old:
                    obj["immutable"] = old["immutable"]
                replace_owned = True
            if old and obj["kind"] == "Deployment" and obj["metadata"].get("namespace") == "kubeflow" and obj["metadata"]["name"] == "ml-pipeline":
                def workflow_patch(value):
                    return next((e.get("value") for c in value.get("spec", {}).get("template", {}).get("spec", {}).get("containers", [])
                                 for e in c.get("env", []) if e["name"] == "COMPILED_PIPELINE_SPEC_PATCH"), None)
                if workflow_patch(obj) is not None and workflow_patch(obj) != workflow_patch(old):
                    # The initial create Update owns this value. A reviewed
                    # compiler configuration change may replace only our API
                    # Deployment's spec, with its actual UID/version conditions.
                    spec_managers = {m["manager"] for m in old["metadata"].get("managedFields", []) if "f:spec" in m.get("fieldsV1", {})}
                    if spec_managers != {manager}:
                        raise RuntimeError("foreign API deployment spec manager; refusing mutation: " + identity(obj))
                    metadata = {**old["metadata"], **obj["metadata"]}
                    metadata["labels"] = {**old["metadata"].get("labels", {}), **obj["metadata"].get("labels", {})}
                    metadata["annotations"] = {**old["metadata"].get("annotations", {}), **obj["metadata"].get("annotations", {})}
                    metadata.pop("managedFields", None)
                    obj["metadata"] = metadata
                    replace_owned = True
            verb = ["apply", "--server-side", "--field-manager=" + manager, "--validate=strict"] if old else ["create", "--field-manager=ani-kubeflow", "--validate=strict"]
            if replace_owned:
                verb = ["replace", "--field-manager=ani-kubeflow", "--validate=strict"]
            self.call(verb + ["--dry-run=server", "-f", "-", "-o", "json"], obj, sensitive=secret)
            prepared.append((obj, verb, secret))
        return prepared

    def apply(self, objects):
        prepared = self.prepare(objects)
        for obj, verb, secret in prepared:
            self.sequence += 1
            name = identity(obj)
            pending = {"sequence": self.sequence, "identity": name, "result": "UNKNOWN", "action": verb[0]}
            self.writes.append(pending)
            atomic(self.directory / "writes.json", self.writes)
            # Unknown timeout/cancellation is reconciled by read-only investigation,
            # never automatically sent again under a fresh name.
            response = json.loads(self.call(verb + ["-f", "-", "-o", "json"], obj, sensitive=secret))
            pending.update(result="CONFIRMED", uid=response["metadata"]["uid"], generation=response["metadata"].get("generation"))
            atomic(self.directory / "writes.json", self.writes)

    def wait(self, obj, predicate, timeout=600):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            value = self.read(obj)
            if value and predicate(value):
                return value
            time.sleep(3)
        raise TimeoutError("deadline exceeded: " + identity(obj))

    def deployment(self, obj):
        pods = []
        def ready(value):
            status = value.get("status", {})
            replicas = value["spec"].get("replicas", 1)
            if not (status.get("observedGeneration", 0) >= value["metadata"].get("generation", 0)
                    and status.get("updatedReplicas", 0) == replicas and status.get("availableReplicas", 0) == replicas
                    and status.get("readyReplicas", 0) == replicas and status.get("replicas", 0) == replicas):
                return False
            selector = ",".join(k + "=" + v for k, v in value["spec"]["selector"]["matchLabels"].items())
            pods[:] = json.loads(self.call(["get", "pods", "-n", obj["metadata"]["namespace"], "-l", selector, "-o", "json"]))["items"]
            # Deployment status may exclude the terminating old ReplicaSet's
            # Pod before it disappears from the list. Wait for both views to
            # agree within the same deadline; never accept only Available.
            return len(pods) == replicas and all(not p["metadata"].get("deletionTimestamp")
                and any(c["type"] == "Ready" and c["status"] == "True" for c in p.get("status", {}).get("conditions", [])) for p in pods)
        value = self.wait(obj, ready)
        return {"uid": value["metadata"]["uid"], "generation": value["metadata"].get("generation"),
                "pods": [{"uid": p["metadata"]["uid"], "name": p["metadata"]["name"], "node": p["spec"].get("nodeName"),
                          "containers": [{"name": c["name"], "imageID": c.get("imageID"), "restarts": c.get("restartCount")} for c in p["status"].get("containerStatuses", [])]} for p in pods]}

    def evidence(self):
        # No Secret, pod environment or arbitrary ConfigMap content in diagnostics.
        for namespace in ("kubeflow", "kubeflow-system", "notebook-controller-system", "kserve",
                          "ani-kf-stage2-a", "ani-kf-stage2-b", *self.site["tenants"]):
            for resource in ("events", "pods", "endpointslices", "persistentvolumeclaims"):
                result = subprocess.run(self.command + ["get", resource, "-n", namespace, "-o", "json"], text=True, capture_output=True, timeout=45)
                if result.returncode == 0:
                    value = json.loads(result.stdout)
                    if resource == "pods":
                        for item in value.get("items", []):
                            item.pop("spec", None)
                            item.get("metadata", {}).pop("annotations", None)
                    atomic(self.directory / (namespace + "-" + resource + ".json"), value)


def decode(secret, key):
    return base64.b64decode(secret["data"][key]).decode()


def assets(root):
    root = pathlib.Path(root)
    lock = json.loads((root / "assets.lock.json").read_text())
    for name, expected in lock["files"].items():
        if pathlib.Path(name).is_absolute() or ".." in pathlib.Path(name).parts:
            raise ValueError("unsafe asset path")
        if hashlib.sha256((root / name).read_bytes()).hexdigest() != expected:
            raise ValueError("offline asset digest differs: " + name)
    return root

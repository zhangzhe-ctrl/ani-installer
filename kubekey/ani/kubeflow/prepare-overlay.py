#!/usr/bin/env python3
"""Build the reviewed ANI object subset on Fedora, from byte-pinned inputs.

No cluster writes. Unknown source bytes fail, instead of inheriting an upstream
addition. Every input object appears in the disposition inventory for review.
"""
import argparse
import copy
import hashlib
import json
import pathlib
import yaml

INPUT_SHA256 = {
    "kfp": "e0dcb42db7dc3104383d13953c4905445f8f9b4cdf8c621864174527f6a644b5",
    "trainer": "db69ab7f57697e1391e5621a863817cb4b922071582053ddfda4ddee7d8b302f",
}
SOURCES = {
    "kfp": "e4ebca310f404dac306e16bc880de12bbbf63b95",
    "trainer": "73c9bece741ce17d2124abda3ec6bfcf5b5b8e87",
    "argo": "bded09fe4abd37cb98d7fc81b4c14a6f5034e9ab",
    "jobset": "da72d32bb0060354aa04b1a5868f3b363b088446",
}
EXCLUDED_NAMES = {
    # The V1 cache admission webhook is not the V2 driver/MLMD cache path.
    "cache-server", "cache-deployer-deployment", "kubeflow-pipelines-cache",
    "kubeflow-pipelines-cache-deployer-sa", "kubeflow-pipelines-cache-role",
    "kubeflow-pipelines-cache-deployer-role", "kubeflow-pipelines-cache-binding",
    "kubeflow-pipelines-cache-deployer-rolebinding", "kubeflow-pipelines-cache-deployer-clusterrole",
    "kubeflow-pipelines-cache-deployer-clusterrolebinding",
    "kubeflow-pipelines-container-builder", "kubeflow-pipelines-public",
    "ml-pipeline-ui", "ml-pipeline-ui-configmap", "ml-pipeline-visualizationserver",
    "ml-pipeline-viewer-crd", "ml-pipeline-viewer-crd-service-account",
    "ml-pipeline-viewer-controller-role", "ml-pipeline-viewer-crd-binding",
    "ml-pipeline-viewer-controller-role", "kubeflow-pipelines-viewer",
    "seaweedfs", "seaweedfs-pvc", "metacontroller", "kubeflow-metacontroller",
    "meta-controller-service", "meta-controller-cluster-role-binding",
    "kubeflow-pipelines-profile-controller", "pipeline-runner", "pipeline-runner-binding",
    "aggregate-to-kubeflow-pipelines-edit", "aggregate-to-kubeflow-pipelines-view",
    "kubeflow-pipelines-edit", "kubeflow-pipelines-view", "argo-aggregate-to-admin",
    "argo-aggregate-to-edit", "argo-aggregate-to-view",
    "compositecontrollers.metacontroller.k8s.io", "controllerrevisions.metacontroller.k8s.io",
    "decoratorcontrollers.metacontroller.k8s.io", "viewers.kubeflow.org",
}


def exclusion(obj):
    kind, name = obj["kind"], obj["metadata"]["name"]
    if kind in ("DestinationRule", "VirtualService", "AuthorizationPolicy"):
        return "Istio is outside this release; replaced by TLS entry and explicit authorization/network policy"
    if name in EXCLUDED_NAMES or name.startswith("kubeflow-pipelines-profile-controller-"):
        if "cache" in name:
            return "legacy V1 cache webhook; V2 execution cache remains in driver/API/MLMD"
        if "profile-controller" in name or "metacontroller" in name or "meta-controller" in name:
            return "profile bootstrap replaced by managed tenant SA/RBAC/launcher/storage resources"
        if "seaweedfs" in name:
            return "declared RustFS provider replaces the upstream object service"
        if "aggregate" in name or name in ("kubeflow-pipelines-edit", "kubeflow-pipelines-view"):
            return "explicit tenant permissions replace aggregate/shared administrator roles"
        if name.startswith("pipeline-runner"):
            return "runner identity is provisioned per managed tenant"
        return "frontend, visualization, public/default access or in-cluster image builder is outside this release"
    return None


def env(container, name, value=None, secret=None, key=None):
    rows = container.setdefault("env", [])
    rows[:] = [row for row in rows if row["name"] != name]
    if secret is not None:
        rows.append({"name": name, "valueFrom": {"secretKeyRef": {"name": secret, "key": key}}})
    elif value is not None:
        rows.append({"name": name, "value": value})


def volume(pod, container, name, mount, source):
    pod.setdefault("volumes", []).append(dict(name=name, **source))
    container.setdefault("volumeMounts", []).append({"name": name, "mountPath": mount, "readOnly": True})


def patch(obj):
    kind, name = obj["kind"], obj["metadata"]["name"]
    if kind == "PersistentVolumeClaim" and name == "mysql-pv-claim":
        obj["spec"]["storageClassName"] = "ANI_DATABASE_CLASS"
        obj["spec"]["resources"]["requests"]["storage"] = "ANI_DATABASE_SIZE"
    if kind == "ClusterRole" and name == "ml-pipeline":
        obj["rules"] = [rule for rule in obj["rules"] if rule["apiGroups"] in
                        [["authorization.k8s.io"], ["authentication.k8s.io"]]]
        obj["rules"].append({"apiGroups": [""], "resources": ["namespaces"], "verbs": ["get", "list"]})
    if kind == "ConfigMap" and name == "pipeline-install-config":
        obj["data"].update(bucketName="ani-kfp-control", defaultSecurityContextRunAsUser="1000",
                           defaultSecurityContextRunAsGroup="1000", defaultSecurityContextRunAsNonRoot="true")
        # Not deployed: the legacy cache image must not remain a latent pull.
        obj["data"].pop("cacheImage", None)
        obj["data"].pop("warning", None)
    if kind == "ConfigMap" and name == "workflow-controller-configmap":
        # Each managed tenant supplies an explicit credentials-scoped repository.
        obj["data"].pop("artifactRepository", None)
        obj["data"]["executor"] = json.dumps({"image": "ANI_IMAGE_quay.io/argoproj/argoexec:v3.7.3", "imagePullPolicy": "IfNotPresent", "securityContext": {
            "runAsNonRoot": True, "runAsUser": 1000, "allowPrivilegeEscalation": False,
            "capabilities": {"drop": ["ALL"]}, "seccompProfile": {"type": "RuntimeDefault"}}})
    if kind == "ConfigMap" and name.startswith("pipeline-api-server-config-"):
        obj["data"].update(MULTIUSER="true", MULTIUSER_SHARED_READ="false")
    if kind == "Secret" and name in ("mysql-secret", "mlpipeline-minio-artifact"):
        return None  # credentials are generated/reused by the installer, not source constants
    if kind == "Deployment":
        pod = obj["spec"]["template"]["spec"]
        for container in pod.get("containers", []):
            if name == "mysql":
                env(container, "MYSQL_ALLOW_EMPTY_PASSWORD")
                env(container, "MYSQL_ROOT_PASSWORD", secret="ani-kfp-mysql-root", key="password")
                volume(pod, container, "init-schema", "/docker-entrypoint-initdb.d",
                       {"secret": {"secretName": "ani-kfp-mysql-init"}})
                container["resources"] = {"requests": {"cpu": "250m", "memory": "512Mi"},
                                          "limits": {"cpu": "2", "memory": "2Gi"}}
            elif name == "ml-pipeline":
                env(container, "KUBEFLOW_USERID_HEADER", ":")
                env(container, "KUBEFLOW_USERID_PREFIX", "")
                env(container, "MULTIUSER", "true")
                env(container, "MULTIUSER_SHARED_READ", "false")
                env(container, "REQUIRE_NAMESPACE_FOR_PIPELINES", "true")
                env(container, "TOKEN_REVIEW_AUDIENCE", "pipelines.kubeflow.org")
                env(container, "DEFAULTPIPELINERUNNERSERVICEACCOUNT", "pipeline-runner")
                env(container, "OBJECTSTORECONFIG_SECURE", "true")
                env(container, "OBJECTSTORECONFIG_HOST", "ani-rustfs-svc.ani-platform.svc.cluster.local")
                env(container, "OBJECTSTORECONFIG_PORT", "9000")
                env(container, "OBJECTSTORECONFIG_ACCESSKEY", secret="ani-kfp-control-s3", key="accesskey")
                env(container, "OBJECTSTORECONFIG_SECRETACCESSKEY", secret="ani-kfp-control-s3", key="secretkey")
                env(container, "V2_DRIVER_IMAGE", "ANI_IMAGE_ghcr.io/kubeflow/kfp-driver:2.16.0")
                env(container, "V2_LAUNCHER_IMAGE", "ANI_IMAGE_ghcr.io/kubeflow/kfp-launcher:2.16.0")
                env(container, "SSL_CERT_FILE", "/etc/ani-ca/ca.crt")
                for variable in ("DBCONFIG_USER", "DBCONFIG_MYSQLCONFIG_USER"):
                    env(container, variable, secret="ani-kfp-api-db", key="username")
                for variable in ("DBCONFIG_PASSWORD", "DBCONFIG_MYSQLCONFIG_PASSWORD"):
                    env(container, variable, secret="ani-kfp-api-db", key="password")
                volume(pod, container, "internal-ca", "/etc/ani-ca", {"configMap": {"name": "ani-kfp-ca"}})
                container["startupProbe"]["failureThreshold"] = 60
            elif name == "metadata-grpc-deployment":
                env(container, "DBCONFIG_USER", secret="ani-kfp-mlmd-db", key="username")
                env(container, "DBCONFIG_PASSWORD", secret="ani-kfp-mlmd-db", key="password")
            # Preserve upstream workflow GC. Workspace retention uses managed
            # per-execution PVCs without Workflow ownerReferences instead.
    return obj


def images(value, references):
    if isinstance(value, dict):
        for key, item in value.items():
            if key == "image" and isinstance(item, str) and item in references:
                value[key] = "ANI_IMAGE_" + item
            else:
                images(item, references)
    elif isinstance(value, list):
        for item in value:
            images(item, references)


parser = argparse.ArgumentParser()
parser.add_argument("--upstream", type=pathlib.Path, required=True)
parser.add_argument("--output", type=pathlib.Path, required=True)
parser.add_argument("--source-commit", required=True)
args = parser.parse_args()
args.output.mkdir(mode=0o700)
references = json.loads((pathlib.Path(__file__).parent / "source-images.json").read_text())
resources, inventory = [], []
for component, expected in INPUT_SHA256.items():
    data = (args.upstream / (component + ".raw.yaml")).read_bytes()
    if hashlib.sha256(data).hexdigest() != expected:
        raise ValueError("upstream render bytes changed: " + component)
    for original in yaml.safe_load_all(data):
        obj = copy.deepcopy(original)
        identity = "/".join([obj["apiVersion"], obj["kind"], obj["metadata"].get("namespace", "_"), obj["metadata"]["name"]])
        reason = exclusion(obj)
        if reason:
            inventory.append({"identity": identity, "action": "exclude", "reason": reason})
            continue
        obj = patch(obj)
        if obj is None:
            inventory.append({"identity": identity, "action": "replace", "reason": "managed isolated identities replace upstream constant credentials"})
            continue
        changed = obj != original
        inventory.append({"identity": identity, "action": "replace" if changed else "keep",
                          "reason": "ANI authentication/storage/credential/dependency policy" if changed else "fixed required backend/controller/API resource"})
        if obj["metadata"]["name"] == "mysql" and obj["kind"] == "Deployment":
            obj["spec"]["template"]["spec"]["containers"][0]["image"] = "docker.io/library/mysql:8.4.11"
        images(obj, references)
        obj["metadata"].setdefault("labels", {})["ani.io/kubeflow-release"] = "26.03-kfp2.16-trainer2.1-v1"
        resources.append(obj)
payload = json.dumps(resources, sort_keys=True, indent=2).encode() + b"\n"
(args.output / "resources.json").write_bytes(payload)
lock = {"schema": "ani.kubeflow.overlay.v1", "status": "PRE_REVIEW",
        "generator_source_commit": args.source_commit, "upstream_commits": SOURCES,
        "input_sha256": INPUT_SHA256, "resources_sha256": hashlib.sha256(payload).hexdigest(),
        "resource_count": len(resources), "inventory": inventory,
        "api_authentication": "TokenReview only; ':' is not a legal client HTTP/gRPC header name",
        "header_auth_live_status": "NOT_RUN", "compatibility_status": "NOT_RUN"}
(args.output / "overlay.lock.json").write_text(json.dumps(lock, indent=2) + "\n")
print(json.dumps({"input_objects": len(inventory), "output_objects": len(resources),
                  "resources_sha256": lock["resources_sha256"]}))

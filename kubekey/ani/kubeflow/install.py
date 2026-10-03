#!/usr/bin/env python3
"""Implementation of the existing ANI Kubeflow first-install role.

Not an append-install entry point. The role is invoked under the ANI product
lock with the configuration and offline resources selected by the same run.
"""
import argparse
import base64
import copy
import datetime
import hashlib
import json
import os
import pathlib
import secrets

from common import Cluster, assets, atomic, decode, identity, load_site
from resources import RELEASE, admission, entry, isolation, obj, runtime, tenant
from storage import scoped_storage


def product_lock():
    # A lock file/PID file alone is insufficient: inspect the kernel's actual
    # FLOCK holder on the exact device+inode used by the existing ANI mutex.
    lock = pathlib.Path("/var/lib/ani-installer/ani-install.lock")
    stat = lock.stat()
    key = "%02x:%02x:%d" % (os.major(stat.st_dev), os.minor(stat.st_dev), stat.st_ino)
    holders = []
    for line in pathlib.Path("/proc/locks").read_text().splitlines():
        fields = line.split()
        if len(fields) >= 7 and fields[1] == "FLOCK" and fields[3] == "WRITE" and fields[5] == key:
            pid = int(fields[4])
            command = pathlib.Path("/proc/%d/cmdline" % pid).read_bytes().replace(b"\0", b" ").decode()
            holders.append({"pid": pid, "command": command})
    if len(holders) != 1:
        raise RuntimeError("the actual ANI product lock must be held by one foreground installer")
    holder = holders[0]
    command = holder["command"]
    development = os.environ.get("ANI_KUBEFLOW_DEVELOPMENT") == "1"
    if " ani install " not in command and not (development and ("flock " in command or "install.py" in command)):
        raise RuntimeError("ANI product lock holder is not the current install or bounded development invocation")
    return {"path": str(lock), "holderPid": holder["pid"], "deviceInode": key, "development": development}


def materialize(root, site):
    lock = json.loads((root / "overlay.lock.json").read_text())
    data = (root / "resources.json").read_bytes()
    if hashlib.sha256(data).hexdigest() != lock["resources_sha256"]:
        raise ValueError("reviewed overlay resource hash differs")
    text = data.decode()
    for key, value in site["images"].items():
        text = text.replace("ANI_IMAGE_" + key, value)
    for key, value in {"ANI_DATABASE_CLASS": site["database_class"], "ANI_DATABASE_SIZE": site["database_size"]}.items():
        text = text.replace(key, value)
    if "ANI_IMAGE_" in text or "ANI_DATABASE_" in text:
        raise ValueError("unresolved offline material mapping")
    resources = json.loads(text)
    if len(resources) != lock["resource_count"]:
        raise ValueError("overlay resource count differs")
    for value in resources:
        if value["kind"] == "Deployment" and value["metadata"]["name"] == "mysql":
            # Docker's first initialization uses a temporary socket-only
            # server before installing the credentials/schema. A Running Pod
            # or successful socket query cannot admit downstream consumers.
            database = value["spec"]["template"]["spec"]["containers"][0]
            query = ["sh", "-ec", 'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql --protocol=TCP -h127.0.0.1 -uroot --connect-timeout=3 --batch --skip-column-names -e "SELECT 1"']
            database["startupProbe"] = {"exec": {"command": query}, "periodSeconds": 5,
                                        "timeoutSeconds": 5, "failureThreshold": 100}
            database["readinessProbe"] = {"exec": {"command": query}, "periodSeconds": 5,
                                          "timeoutSeconds": 5, "failureThreshold": 3}
    return resources


def database_secrets(cluster, config):
    output, credentials = [], {}
    for name, username in (("ani-kfp-mysql-root", None), ("ani-kfp-api-db", "kfp_pipeline"), ("ani-kfp-mlmd-db", "kfp_metadata")):
        secret = obj("Secret", name, "kubeflow", type="Opaque")
        old = cluster.owned(secret)
        if old:
            if old.get("type") != "Opaque" or (username and decode(old, "username") != username):
                raise ValueError("database identity differs; no rotation")
            password = decode(old, "password")
        else:
            password = secrets.token_hex(32)
            secret["stringData"] = {"password": password}
            if username:
                secret["stringData"]["username"] = username
            output.append(secret)
        if len(password) != 64 or any(c not in "0123456789abcdef" for c in password):
            raise ValueError("managed database password format differs")
        credentials[name] = password
    for key in ("pipelineDb", "mlmdDb"):
        value = config[key]
        if not value or any(c not in "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_" for c in value):
            raise ValueError("unapproved database name")
    sql = ""
    for database, username, password in ((config["pipelineDb"], "kfp_pipeline", credentials["ani-kfp-api-db"]),
                                          (config["mlmdDb"], "kfp_metadata", credentials["ani-kfp-mlmd-db"])):
        sql += "CREATE DATABASE IF NOT EXISTS `%s`;\nCREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY '%s';\nGRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'%%';\n" % (database, username, password, database, username)
    seed = obj("Secret", "ani-kfp-mysql-init", "kubeflow", type="Opaque", data={"01-kfp.sql": base64.b64encode(sql.encode()).decode()})
    old = cluster.owned(seed)
    if old and old.get("data") != seed["data"]:
        raise RuntimeError("database initialization binding differs; refusing replacement")
    if not old:
        output.append(seed)
    cluster.apply(output)


def checked_policy(value):
    status = value.get("status", {})
    if status.get("observedGeneration", 0) < value["metadata"].get("generation", 1):
        return False
    checking = status.get("typeChecking")
    if checking is None:
        # Kubernetes 1.35's status controller always applies observedGeneration
        # and typeChecking together. Removing the final warning via SSA can
        # omit the now-empty object from the JSON result. Require the actual
        # status controller's ownership and current update time in that case;
        # observedGeneration alone is never completion evidence.
        fields = value["metadata"].get("managedFields", [])
        specification_times = [field.get("time", "") for field in fields
                               if "f:spec" in field.get("fieldsV1", {})]
        completed = any(field.get("manager") == "validatingadmissionpolicy-status"
                        and field.get("operation") == "Apply" and field.get("subresource") == "status"
                        and {"f:observedGeneration", "f:typeChecking"}.issubset(field.get("fieldsV1", {}).get("f:status", {}))
                        and field.get("time") and field["time"] >= max(specification_times, default="")
                        for field in fields)
        if not completed:
            return False
        checking = {}
    if not isinstance(checking, dict):
        return False
    warnings = checking.get("expressionWarnings", [])
    if warnings:
        raise RuntimeError("admission policy type checking failed: " + value["metadata"]["name"] + ": " + json.dumps(warnings))
    return True


def install(cluster, root, report):
    site = cluster.site
    report["lock"] = product_lock()
    values = materialize(root, site)
    # Check collisions for all fixed upstream identities before any mutation.
    for value in values:
        cluster.owned(value)
    for name in ("kubeflow", *site["tenants"]):
        cluster.owned(obj("Namespace", name))
    for storage_class in (site["database_class"], site["workspace_class"]):
        if not cluster.call(["get", "storageclass", storage_class, "-o", "json"]):
            raise RuntimeError("declared StorageClass is absent")
    issuer = json.loads(cluster.call(["get", "clusterissuer", "ani-ca", "-o", "json"]))
    if not any(c["type"] == "Ready" and c["status"] == "True" for c in issuer.get("status", {}).get("conditions", [])):
        raise RuntimeError("declared internal CA issuer is not ready")
    ca_value = cluster.owned(obj("ConfigMap", "ani-rustfs-ca", "ani-platform"))
    ca = ca_value["data"]["ca.crt"]
    if "-----BEGIN CERTIFICATE-----" not in ca or "PRIVATE KEY" in ca:
        raise ValueError("public CA contract differs")
    site["kubernetes_service_ip"] = cluster.read(obj("Service", "kubernetes", "default"))["spec"]["clusterIP"]
    pipeline_role = next(v["rules"] for v in values if v["kind"] == "Role" and v["metadata"]["name"] == "ml-pipeline")
    approved_runtime = runtime(site["images"])
    dynamic = entry(site, site["images"]) + isolation(site) + admission(site, approved_runtime["metadata"]["name"], site["images"]["ani.local/kubeflow-execution:26.03-v1"])
    dynamic.append(approved_runtime)
    for namespace in site["tenants"]:
        dynamic += tenant(site, namespace, pipeline_role)
    for namespace in ("kubeflow", *site["tenants"]):
        dynamic += [obj("ConfigMap", "ani-kfp-ca", namespace), obj("Secret", "ani-kfp-ca", namespace)]
    dynamic += [obj("Secret", name, "kubeflow") for name in ("ani-kfp-mysql-root", "ani-kfp-api-db", "ani-kfp-mlmd-db", "ani-kfp-mysql-init", "ani-kfp-control-s3")]
    dynamic += [obj("Secret", "mlpipeline-minio-artifact", namespace) for namespace in site["tenants"]]
    for value in dynamic:
        cluster.owned(value)
    cluster_uid = cluster.read(obj("Namespace", "kube-system"))["metadata"]["uid"]
    report["clusterUid"] = cluster_uid
    atomic(cluster.directory / "report.json", report)

    namespaces = [obj("Namespace", "kubeflow")] + [v for v in values if v["kind"] == "Namespace"]
    cluster.apply(namespaces)
    crds = [v for v in values if v["kind"] == "CustomResourceDefinition"]
    cluster.apply(crds)
    for value in crds:
        cluster.wait(value, lambda v: any(c["type"] == "Established" and c["status"] == "True" for c in v.get("status", {}).get("conditions", [])))
    report["completedPhases"].append("CRDS_ESTABLISHED")
    report["changesStarted"] = bool(cluster.writes)
    atomic(cluster.directory / "report.json", report)

    fixed = [v for v in values if v["kind"] not in ("Namespace", "CustomResourceDefinition", "Deployment", "PersistentVolumeClaim", "MutatingWebhookConfiguration", "ValidatingWebhookConfiguration")]
    cluster.apply(fixed)
    webhooks = [v for v in values if v["kind"] in ("MutatingWebhookConfiguration", "ValidatingWebhookConfiguration")]
    for value in webhooks:
        # Self-managed cert rotators require the webhook registrations to
        # exist while controllers start; no TrainJob/JobSet consumer yet.
        for webhook in value["webhooks"]:
            if not webhook["clientConfig"].get("caBundle"):
                webhook["clientConfig"].pop("caBundle", None)
    cluster.apply(webhooks)
    controllers = [v for v in values if v["kind"] == "Deployment" and v["metadata"].get("namespace") == "kubeflow-system"]
    cluster.apply(controllers)
    report["deployments"] = {identity(v): cluster.deployment(v) for v in controllers}
    for value in webhooks:
        cluster.wait(value, lambda v: all(w["clientConfig"].get("caBundle") for w in v["webhooks"]))
    report["completedPhases"].append("TRAINER_JOBSET_CONTROLLERS_WEBHOOK_CA")
    atomic(cluster.directory / "report.json", report)

    for namespace in site["tenants"]:
        resources = tenant(site, namespace, pipeline_role)
        cluster.apply([v for v in resources if v["kind"] == "Namespace"])
        cluster.apply([v for v in resources if v["kind"] != "Namespace"])
    public_ca = []
    for namespace in ("kubeflow", *site["tenants"]):
        public_ca += [obj("ConfigMap", "ani-kfp-ca", namespace, data={"ca.crt": ca}),
                      obj("Secret", "ani-kfp-ca", namespace, type="Opaque", data={"ca.crt": base64.b64encode(ca.encode()).decode()})]
    cluster.apply(public_ca)
    policies = admission(site, approved_runtime["metadata"]["name"], site["images"]["ani.local/kubeflow-execution:26.03-v1"])
    cluster.apply(policies)
    for value in policies:
        if value["kind"] == "ValidatingAdmissionPolicy":
            cluster.wait(value, checked_policy)
    cluster.apply(isolation(site))
    scoped_storage(cluster, ca)
    config = next(v["data"] for v in values if v["kind"] == "ConfigMap" and v["metadata"]["name"] == "pipeline-install-config")
    database_secrets(cluster, config)
    claims = [v for v in values if v["kind"] == "PersistentVolumeClaim"]
    cluster.apply(claims)
    database = next(v for v in values if v["kind"] == "Deployment" and v["metadata"]["name"] == "mysql")
    cluster.apply([database])
    report["deployments"][identity(database)] = cluster.deployment(database)
    for value in claims:
        cluster.wait(value, lambda v: v.get("status", {}).get("phase") == "Bound")
    # Authenticate against MySQL, not just Deployment Ready or a TCP socket.
    result = cluster.call(["-n", "kubeflow", "exec", "deployment/mysql", "--", "sh", "-ec",
                           'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql --protocol=TCP -h127.0.0.1 -uroot --connect-timeout=3 --batch --skip-column-names -e "SELECT 1"'], sensitive=True)
    if result.strip() != "1":
        raise RuntimeError("MySQL authenticated protocol check differs")
    report["completedPhases"].append("ISOLATED_STORAGE_DATABASE_AUTHENTICATED")
    atomic(cluster.directory / "report.json", report)
    consumers = [v for v in values if v["kind"] == "Deployment" and v["metadata"].get("namespace") == "kubeflow" and v["metadata"]["name"] != "mysql"]
    # MLMD before KFP API, then API consumers/Argo. First failed dependency
    # stops all later writes; no controller Pod deletion or automatic repair.
    order = {"metadata-grpc-deployment": 0, "metadata-envoy-deployment": 1, "ml-pipeline": 2, "workflow-controller": 3}
    consumers.sort(key=lambda v: order.get(v["metadata"]["name"], 4))
    for value in consumers:
        cluster.apply([value])
        report["deployments"][identity(value)] = cluster.deployment(value)
    ingress = entry(site, site["images"])
    cluster.apply(ingress)
    certificate = next(v for v in ingress if v["kind"] == "Certificate")
    cluster.wait(certificate, lambda v: any(c["type"] == "Ready" and c["status"] == "True" for c in v.get("status", {}).get("conditions", [])))
    report["deployments"]["Deployment/kubeflow/ani-kfp-entry"] = cluster.deployment(next(v for v in ingress if v["kind"] == "Deployment"))
    # The admission webhook must handle this exact current Runtime dry-run.
    cluster.apply([approved_runtime])
    report["runtime"] = {"name": approved_runtime["metadata"]["name"], "uid": cluster.read(approved_runtime)["metadata"]["uid"],
                         "image": site["images"]["ani.local/kubeflow-execution:26.03-v1"], "workspaceMode": site["workspace_mode"]}
    report["completedPhases"].append("KFP_CONSUMERS_ENTRY_RUNTIME_INSTALLED")
    report["status"] = "INSTALLED_PENDING_CURRENT_CHECK"


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--site", required=True)
    args = parser.parse_args()
    os.umask(0o077)
    site = load_site(args.site)
    root = assets(pathlib.Path(__file__).parent)
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")
    cluster = Cluster(site, pathlib.Path(site["logs_dir"]) / ("kubeflow-install-" + stamp))
    report = {"schema": "ani.kubeflow.install.v1", "release": RELEASE, "status": "IN_PROGRESS", "completedPhases": [], "changesStarted": False}
    try:
        install(cluster, root, report)
    except BaseException as error:
        report.update(status="FAIL", error=str(error), changesStarted=bool(cluster.writes),
                      completedWrites=sum(v["result"] == "CONFIRMED" for v in cluster.writes))
        atomic(cluster.directory / "report.json", report)
        cluster.evidence()
        raise
    report["changesStarted"] = bool(cluster.writes)
    report["completedWrites"] = sum(v["result"] == "CONFIRMED" for v in cluster.writes)
    atomic(cluster.directory / "report.json", report)
    print(json.dumps({"status": report["status"], "report": str(cluster.directory / "report.json")}))


if __name__ == "__main__":
    main()

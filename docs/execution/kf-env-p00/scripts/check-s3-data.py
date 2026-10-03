#!/usr/bin/env python3
"""Lab acceptance: actual scoped S3 I/O and independent Pipeline artifact read.

Use the shipped mature rc client and existing managed scoped Secrets. No root
identity, account changes, overwrite, retry or automatic object cleanup.
"""
import argparse
import hashlib
import json
import os
import pathlib
import re
import subprocess
import sys
import tempfile
import time
import uuid

parser = argparse.ArgumentParser()
parser.add_argument("--site", required=True)
parser.add_argument("--private-success-report", required=True)
parser.add_argument("--model-sha256", required=True)
parser.add_argument("--output", required=True)
parser.add_argument("--resume-report", help="reuse confirmed objects and reconcile the earlier failed negative upload; no replacement positive uploads")
args = parser.parse_args()
site = json.loads(pathlib.Path(args.site).read_text())
material = pathlib.Path(site["artifact_root"]) / "manifests/kubeflow" / site["release"]
sys.path.insert(0, str(material))
from common import Cluster, assets, atomic, decode, load_site
from install import product_lock
from resources import obj

assets(material)
site = load_site(args.site)
output = pathlib.Path(args.output)
if not output.is_absolute() or not re.fullmatch(r"[a-f0-9]{64}", args.model_sha256):
    raise ValueError("absolute output and actual model hash required")
cluster = Cluster(site, output)
    report = {"schema": "ani.kubeflow.s3-data.v1", "status": "IN_PROGRESS", "lock": product_lock(),
          "requests": [], "retainedObjects": [], "cleanup": "NOT_REQUESTED",
          "sourceSha256": hashlib.sha256(pathlib.Path(__file__).read_bytes()).hexdigest()}
atomic(output / "report.json", report)
try:
    previous = None
    if args.resume_report:
        previous_path = pathlib.Path(args.resume_report)
        previous = json.loads(previous_path.read_text())
        if previous["schema"] != report["schema"] or previous["status"] != "FAIL" or len(previous["retainedObjects"]) != 3:
            raise ValueError("not the earlier confirmed three-identity S3 attempt")
        unknown = [r for r in previous["requests"] if r["result"] == "UNKNOWN"]
        if len(unknown) != 1 or unknown[0]["identity"] != "kubeflow" or unknown[0].get("exitCode") != 7 or not unknown[0]["mutation"]:
            raise ValueError("unsupported unknown S3 outcome; read-only investigation required")
        report.update(requests=previous["requests"], retainedObjects=previous["retainedObjects"],
                      resumedFromSha256=hashlib.sha256(previous_path.read_bytes()).hexdigest())
    record = json.loads(pathlib.Path(args.private_success_report).read_text())
    if record["status"] != "RUN_STATE_CORRELATED" or record["mode"] != "success" or record["run"]["state"] != "SUCCEEDED" or record["namespace"] not in site["tenants"]:
        raise ValueError("confirmed successful execution required")
    receipt = record["creationReceipt"]
    run_id = str(uuid.UUID(record["run"]["id"]))
    if receipt["kfpRunId"] != run_id or receipt["workspaceUid"] != record["workspace"]["uid"]:
        raise ValueError("successful execution receipt differs")
    ca = cluster.owned(obj("ConfigMap", "ani-rustfs-ca", "ani-platform"))["data"]["ca.crt"]
    bindings = [("kubeflow", "ani-kfp-control-s3", "ani-kfp-control", "pipelines")]
    bindings += [(n, "mlpipeline-minio-artifact", "ani-kfp-" + n, "artifacts") for n in site["tenants"]]
    credentials = []
    with tempfile.TemporaryDirectory(prefix="s3-data-private-", dir=output) as directory:
        work = pathlib.Path(directory)
        (work / "ca.crt").write_text(ca)
        with (work / "forward.log").open("w+") as log:
            forward = subprocess.Popen(cluster.command + ["-n", "ani-platform", "port-forward", "service/ani-rustfs-svc", ":9000", "--address=127.0.0.1"], stdout=log, stderr=subprocess.STDOUT)
            try:
                deadline, port = time.monotonic() + 25, None
                while time.monotonic() < deadline and forward.poll() is None:
                    match = re.search(r"Forwarding from 127\.0\.0\.1:([0-9]+)", (work / "forward.log").read_text())
                    if match:
                        port = int(match.group(1)); break
                    time.sleep(1)
                if port is None:
                    raise RuntimeError("scoped S3 forward deadline")
                config = work / "config"
                config.mkdir(mode=0o700)
                aliases = []
                for namespace, secret_name, bucket, prefix in bindings:
                    secret = cluster.owned(obj("Secret", secret_name, namespace))
                    access, password = decode(secret, "accesskey"), decode(secret, "secretkey")
                    credentials.extend((access, password))
                    alias = {"name": namespace, "endpoint": "https://127.0.0.1:" + str(port), "access_key": access,
                             "secret_key": password, "region": "us-east-1", "signature": "v4", "bucket_lookup": "path",
                             "insecure": False, "ca_bundle": str(work / "ca.crt")}
                    aliases.append("[[aliases]]\n" + "\n".join(k + " = " + json.dumps(v) for k, v in alias.items()))
                if previous:
                    # Root is used solely to resolve the exact earlier unknown
                    # object outcome. All acceptance I/O uses scoped aliases.
                    root = cluster.owned(obj("Secret", "ani-rustfs-root", "ani-platform"))
                    root_access, root_secret = decode(root, "RUSTFS_ACCESS_KEY"), decode(root, "RUSTFS_SECRET_KEY")
                    credentials.extend((root_access, root_secret))
                    alias = {"name": "reconcile-root", "endpoint": "https://127.0.0.1:" + str(port), "access_key": root_access,
                             "secret_key": root_secret, "region": "us-east-1", "signature": "v4", "bucket_lookup": "path",
                             "insecure": False, "ca_bundle": str(work / "ca.crt")}
                    aliases.append("[[aliases]]\n" + "\n".join(k + " = " + json.dumps(v) for k, v in alias.items()))
                (config / "config.toml").write_text("schema_version = 1\n" + "\n".join(aliases) + "\n")
                os.chmod(config / "config.toml", 0o600)
                environment = dict(os.environ, RC_CONFIG_DIR=str(config), AWS_MAX_ATTEMPTS="1")
                executable = str(pathlib.Path(site["artifact_root"]) / "bin/rc")

                def request(arguments, identity, mutation=False, denied=False, body=None):
                    row = {"identity": identity, "operation": arguments[:2], "result": "UNKNOWN", "mutation": mutation}
                    report["requests"].append(row)
                    atomic(output / "report.json", report)
                    result = subprocess.run([executable] + arguments, input=body, capture_output=True, timeout=60, env=environment)
                    row["exitCode"] = result.returncode
                    if denied:
                        message = (result.stdout + result.stderr).decode(errors="replace")
                        for credential in credentials:
                            message = message.replace(credential, "<redacted>")
                        row["diagnostic"] = message[:4096]
                        atomic(output / "report.json", report)
                        if result.returncode == 0 or not re.search(r"AccessDenied|Access Denied|Forbidden|StatusCode.?403|status.?403", message, re.I):
                            raise RuntimeError("negative S3 request lacks explicit access denial")
                        row["result"] = "DENIED"
                    elif result.returncode:
                        raise RuntimeError("scoped S3 request failed rc=" + str(result.returncode))
                    else:
                        row["result"] = "CONFIRMED"
                    atomic(output / "report.json", report)
                    return result.stdout

                attempt = "env-s3-" + uuid.uuid4().hex
                if previous:
                    keys = {r["key"].rsplit("/", 1)[-1] for r in previous["retainedObjects"]}
                    if len(keys) != 1 or not re.fullmatch(r"env-s3-[a-f0-9]{32}\.json", next(iter(keys))):
                        raise ValueError("earlier unique S3 keys differ")
                    attempt = next(iter(keys))[:-5]
                    target = "reconcile-root/ani-kfp-control/outside/" + attempt + ".json"
                    result = subprocess.run([executable, "--json", "object", "stat", target], capture_output=True, timeout=60, env=environment)
                    message = (result.stdout + result.stderr).decode(errors="replace")
                    if result.returncode != 5 or not re.search(r"NoSuchKey|Not.?Found|not exist|status.?404", message, re.I):
                        raise RuntimeError("earlier unknown upload is not confirmed absent; no resend")
                    unknown[0].update(result="RECONCILED_NOT_CREATED", reconciliation={"method": "read-only root HEAD of exact unknown key", "exitCode": result.returncode})
                    atomic(output / "report.json", report)
                objects = {}
                for namespace, secret_name, bucket, prefix in bindings:
                    body = json.dumps({"attempt": attempt, "namespace": namespace}, sort_keys=True).encode()
                    path = work / (namespace + ".json")
                    path.write_bytes(body)
                    key = prefix + "/environment-probes/" + attempt + ".json"
                    destination = namespace + "/" + bucket + "/" + key
                    if previous:
                        matched = [r for r in previous["retainedObjects"] if r["namespace"] == namespace]
                        if len(matched) != 1 or matched[0] != {"namespace": namespace, "bucket": bucket, "key": key, "sha256": hashlib.sha256(body).hexdigest()}:
                            raise ValueError("earlier confirmed scoped object differs")
                    else:
                        request(["put", str(path), destination, "--overwrite", "false", "--retry-attempts", "1"], namespace, mutation=True)
                        report["retainedObjects"].append({"namespace": namespace, "bucket": bucket, "key": key, "sha256": hashlib.sha256(body).hexdigest()})
                    actual = request(["object", "show", destination], namespace)
                    if actual != body:
                        raise RuntimeError("scoped S3 byte readback differs")
                    objects[namespace] = (bucket, key, path)
                for namespace, _, bucket, _ in bindings:
                    request(["--json", "object", "show", namespace + "/" + bucket + "/ani-installer/owner.json"], namespace, denied=True)
                    for other, (other_bucket, key, _) in objects.items():
                        if other != namespace:
                            request(["--json", "object", "show", namespace + "/" + other_bucket + "/" + key], namespace, denied=True)
                    path = objects[namespace][2]
                    request(["--json", "pipe", namespace + "/" + bucket + "/outside/" + attempt + ".json"], namespace, mutation=True, denied=True, body=path.read_bytes())
                namespace = record["namespace"]
                bucket = "ani-kfp-" + namespace
                prefix = namespace + "/" + bucket + "/artifacts/ani-environment-handoff/" + run_id + "/external-training/"
                listing = json.loads(request(["--json", "object", "list", prefix, "--recursive"], namespace))
                items = listing["items"]
                artifacts = {}
                for name in ("model", "link"):
                    matched = [item["key"] for item in items if item["key"].endswith("/" + name)]
                    if len(matched) != 1:
                        raise RuntimeError("actual artifact key set differs: " + name)
                    data = request(["object", "show", namespace + "/" + bucket + "/" + matched[0]], namespace)
                    if len(data) > 65536:
                        raise RuntimeError("artifact exceeds bounded probe size")
                    value = json.loads(data)
                    if value["execution"] != record["execution"]:
                        raise RuntimeError("S3 artifact execution differs")
                    if name == "model" and hashlib.sha256(data).hexdigest() != args.model_sha256:
                        raise RuntimeError("S3 model differs from independent PVC readback")
                    if name == "link" and (value["kfpRunId"] != run_id or value["workspace"]["uid"] != receipt["workspaceUid"] or value["trainJob"]["uid"] != receipt["trainJobUid"]):
                        raise RuntimeError("S3 Link creation UIDs differ")
                    artifacts[name] = {"key": matched[0], "sha256": hashlib.sha256(data).hexdigest(), "size": len(data)}
                report.update(status="SCOPED_IO_AND_ARTIFACT_READ_BACK", artifacts=artifacts, runId=run_id)
            finally:
                forward.terminate()
                try:
                    forward.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    forward.kill(); forward.wait(timeout=5)
except BaseException as error:
    report.update(status="FAIL", error=str(error))
    atomic(output / "report.json", report)
    raise
atomic(output / "report.json", report)
print(json.dumps({"status": report["status"], "report": str(output / "report.json")}))

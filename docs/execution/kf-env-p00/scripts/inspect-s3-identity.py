#!/usr/bin/env python3
"""Lab-only sanitized readback of task-created S3 service accounts; no mutation."""
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

parser = argparse.ArgumentParser()
parser.add_argument("--site", required=True)
parser.add_argument("--cluster-uid", required=True)
parser.add_argument("--output", required=True)
args = parser.parse_args()
site = json.loads(pathlib.Path(args.site).read_text())
material = pathlib.Path(site["artifact_root"]) / "manifests/kubeflow" / site["release"]
sys.path.insert(0, str(material))
from common import Cluster, assets, atomic, decode, load_site
from resources import obj

assets(material)
site = load_site(args.site)
output = pathlib.Path(args.output)
if not output.is_absolute():
    raise ValueError("absolute output required")
cluster = Cluster(site, output)
if cluster.read(obj("Namespace", "kube-system"))["metadata"]["uid"] != args.cluster_uid:
    raise RuntimeError("cluster identity differs")
root = cluster.owned(obj("Secret", "ani-rustfs-root", "ani-platform"))
access, secret = decode(root, "RUSTFS_ACCESS_KEY"), decode(root, "RUSTFS_SECRET_KEY")
ca = cluster.owned(obj("ConfigMap", "ani-rustfs-ca", "ani-platform"))["data"]["ca.crt"]
report = {"schema": "ani.kubeflow.s3-readback.v1", "persistentRequests": 0, "identities": [], "acceptance": "EAC_NOT_ATTESTED"}
with tempfile.TemporaryDirectory(prefix="s3-readonly-", dir=output) as directory:
    work = pathlib.Path(directory)
    (work / "ca.crt").write_text(ca)
    with (work / "forward.log").open("w+") as log:
        forward = subprocess.Popen(cluster.command + ["-n", "ani-platform", "port-forward", "service/ani-rustfs-svc", ":9000", "--address=127.0.0.1"], stdout=log, stderr=subprocess.STDOUT)
        try:
            deadline = time.monotonic() + 25
            port = None
            while time.monotonic() < deadline and forward.poll() is None:
                match = re.search(r"Forwarding from 127\.0\.0\.1:([0-9]+)", (work / "forward.log").read_text())
                if match:
                    port = int(match.group(1)); break
                time.sleep(1)
            if port is None:
                raise RuntimeError("read-only S3 forward deadline")
            config = work / "config"
            config.mkdir(mode=0o700)
            alias = {"name": "root", "endpoint": "https://127.0.0.1:" + str(port), "access_key": access,
                     "secret_key": secret, "region": "us-east-1", "signature": "v4", "bucket_lookup": "path",
                     "insecure": False, "ca_bundle": str(work / "ca.crt")}
            (config / "config.toml").write_text("schema_version = 1\n[[aliases]]\n" + "\n".join(k + " = " + json.dumps(v) for k, v in alias.items()) + "\n")
            os.chmod(config / "config.toml", 0o600)
            for namespace, name in [("kubeflow", "ani-kfp-control-s3")] + [(n, "mlpipeline-minio-artifact") for n in site["tenants"]]:
                binding = cluster.owned(obj("Secret", name, namespace))
                if binding is None:
                    report["identities"].append({"namespace": namespace, "managedSecret": "ABSENT"})
                    continue
                key = decode(binding, "accesskey")
                response = subprocess.run([str(pathlib.Path(site["artifact_root"]) / "bin/rc"), "--json", "admin", "access-key", "info", "root", key], capture_output=True, text=True, timeout=60,
                                          env=dict(os.environ, RC_CONFIG_DIR=str(config)))
                if response.returncode:
                    raise RuntimeError("read-only S3 identity request rc=" + str(response.returncode))
                value = json.loads(response.stdout)
                policy = value.get("policy")
                if isinstance(policy, str):
                    policy = json.loads(policy)
                report["identities"].append({"namespace": namespace, "responseFields": sorted(value),
                    "accessMatches": value.get("accessKey") == key, "parentMatches": value.get("parentUser") == access,
                    "userType": value.get("userType"), "accountStatus": value.get("accountStatus"),
                    "impliedPolicy": value.get("impliedPolicy"), "policy": policy})
        finally:
            forward.terminate()
            try:
                forward.wait(timeout=10)
            except subprocess.TimeoutExpired:
                forward.kill(); forward.wait(timeout=5)
atomic(output / "report.json", report)
print(json.dumps(report))

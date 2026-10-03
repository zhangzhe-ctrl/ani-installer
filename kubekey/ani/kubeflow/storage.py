"""Task-scoped KFP RustFS identities; no mutation of existing ANI credentials."""
import hashlib
import hmac
import json
import os
import pathlib
import re
import subprocess
import tempfile
import time

from common import atomic, decode
from resources import obj


def scoped_storage(cluster, ca):
    site = cluster.site
    namespace = "ani-platform"
    for kind, name in (("Secret", "ani-rustfs-root"), ("ConfigMap", "ani-rustfs-ca"),
                       ("Certificate", "ani-rustfs-server"), ("Deployment", "ani-rustfs"), ("Service", "ani-rustfs-svc")):
        version = "cert-manager.io/v1" if kind == "Certificate" else "apps/v1" if kind == "Deployment" else "v1"
        value = cluster.owned(obj(kind, name, namespace, api=version))
        if not value:
            raise RuntimeError("declared RustFS dependency is absent: " + name)
    root = cluster.read(obj("Secret", "ani-rustfs-root", namespace))
    access, secret = decode(root, "RUSTFS_ACCESS_KEY"), decode(root, "RUSTFS_SECRET_KEY")
    if not re.fullmatch(r"[a-f0-9]{32}", access) or not re.fullmatch(r"[a-f0-9]{64}", secret):
        raise ValueError("declared RustFS root identity has an unexpected format")
    executable = pathlib.Path(site["artifact_root"]) / "bin/rc"
    bindings = [("kubeflow", "ani-kfp-control", "ani-kfp-control-s3", "pipelines")]
    bindings += [(tenant, "ani-kfp-" + tenant, "mlpipeline-minio-artifact", "artifacts") for tenant in site["tenants"]]
    with tempfile.TemporaryDirectory(prefix="rustfs-private-", dir=cluster.directory) as temporary:
        work = pathlib.Path(temporary)
        (work / "ca.crt").write_text(ca)
        forward_log = (work / "port-forward.log").open("w+")
        forward = subprocess.Popen(cluster.command + ["-n", namespace, "port-forward", "service/ani-rustfs-svc", ":9000", "--address=127.0.0.1"], stdout=forward_log, stderr=subprocess.STDOUT)
        try:
            deadline = time.monotonic() + 25
            port = None
            while time.monotonic() < deadline and forward.poll() is None:
                match = re.search(r"Forwarding from 127\.0\.0\.1:([0-9]+)", (work / "port-forward.log").read_text())
                if match:
                    port = int(match.group(1)); break
                time.sleep(1)
            if port is None:
                raise RuntimeError("RustFS port-forward did not become ready")
            config = work / "config"
            config.mkdir(mode=0o700)
            environment = dict(os.environ, RC_CONFIG_DIR=str(config))

            def configure(key, password):
                aliases = []
                for name, user, credential in (("root", access, secret), ("app", key, password)):
                    aliases.append("[[aliases]]\n" + "\n".join(k + " = " + json.dumps(v) for k, v in {
                        "name": name, "endpoint": "https://127.0.0.1:" + str(port), "access_key": user,
                        "secret_key": credential, "region": "us-east-1", "signature": "v4", "bucket_lookup": "path",
                        "insecure": False, "ca_bundle": str(work / "ca.crt")}.items()))
                (config / "config.toml").write_text("schema_version = 1\n" + "\n".join(aliases) + "\n")
                os.chmod(config / "config.toml", 0o600)

            def rc(arguments, accepted=(0,), mutation=None):
                if mutation:
                    pending = {"operation": mutation, "result": "UNKNOWN"}
                    mutations.append(pending)
                    atomic(cluster.directory / "s3-writes.json", mutations)
                result = subprocess.run([str(executable)] + arguments, text=True, capture_output=True, timeout=60, env=environment)
                if result.returncode not in accepted:
                    # Retain the first cause without logging credentials or
                    # effective signed URLs. Do not repeat an unknown mutation.
                    def sanitized(value):
                        for credential in (access, secret, key, password):
                            value = value.replace(credential, "<redacted>")
                        value = re.sub(r"https?://[^\s\"']+", lambda m: m.group(0).split("?", 1)[0], value)
                        return value[:8192]
                    atomic(cluster.directory / "rustfs-first-error.json", {"exit_code": result.returncode,
                        "operation": mutation or arguments[:3], "stdout": sanitized(result.stdout), "stderr": sanitized(result.stderr)})
                    raise RuntimeError("RustFS scoped operation failed rc=" + str(result.returncode))
                if mutation:
                    pending["result"] = "CONFIRMED"
                    atomic(cluster.directory / "s3-writes.json", mutations)
                return result

            mutations = []
            for target, bucket, secret_name, prefix in bindings:
                key = "ani-kfp-" + hashlib.sha256((site["owner"] + ":" + target).encode()).hexdigest()[:16]
                password = hmac.new(secret.encode(), ("ani-kfp-s3-v1:" + site["owner"] + ":" + target).encode(), hashlib.sha256).hexdigest()[:40]
                configure(key, password)
                rc(["ready", "root"])
                marker = {"schema": "ani.kubeflow.s3.v1", "owner": site["owner"], "namespace": target, "bucket": bucket, "prefix": prefix}
                marker_text = json.dumps(marker, sort_keys=True, separators=(",", ":"))
                (work / "owner.json").write_text(marker_text)
                policy = {"Version": "2012-10-17", "Statement": [
                    {"Effect": "Allow", "Action": ["s3:GetBucketLocation"], "Resource": ["arn:aws:s3:::" + bucket]},
                    {"Effect": "Allow", "Action": ["s3:ListBucket", "s3:ListBucketMultipartUploads"], "Resource": ["arn:aws:s3:::" + bucket], "Condition": {"StringLike": {"s3:prefix": [prefix, prefix + "/", prefix + "/*"]}}},
                    {"Effect": "Allow", "Action": ["s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"], "Resource": ["arn:aws:s3:::" + bucket + "/" + prefix + "/*"]}]}
                (work / "policy.json").write_text(json.dumps(policy, separators=(",", ":")))
                desired = obj("Secret", secret_name, target, type="Opaque", stringData={"accesskey": key, "secretkey": password})
                old = cluster.owned(desired)
                if old and (decode(old, "accesskey") != key or decode(old, "secretkey") != password or old.get("type") != "Opaque"):
                    raise RuntimeError("existing scoped S3 identity differs; no rotation")
                buckets = json.loads(rc(["--json", "bucket", "list", "root/"]).stdout)["items"]
                exists = bucket in [item["key"] for item in buckets]
                account = rc(["--json", "admin", "access-key", "info", "root", key], accepted=(0, 5))
                if exists:
                    actual = rc(["object", "show", "root/" + bucket + "/ani-installer/owner.json"]).stdout
                    if json.loads(actual) != marker:
                        raise RuntimeError("foreign S3 bucket ownership")
                elif account.returncode == 0:
                    raise RuntimeError("scoped S3 account exists without its owned bucket")
                if account.returncode == 0:
                    info = json.loads(account.stdout)
                    if (info.get("accessKey") != key or info.get("parentUser") != access or info.get("userType") != "Service Account"
                            or info.get("accountStatus") != "on" or info.get("impliedPolicy") is not False or json.loads(info["policy"]) != policy):
                        raise RuntimeError("S3 identity scope differs")
                    if not old:
                        raise RuntimeError("S3 account exists without its managed Secret")
                if not old:
                    cluster.apply([desired])
                if not exists:
                    rc(["bucket", "create", "root/" + bucket, "--region", "us-east-1"], mutation="create-bucket:" + bucket)
                    rc(["put", str(work / "owner.json"), "root/" + bucket + "/ani-installer/owner.json", "--overwrite", "false"], mutation="mark-bucket:" + bucket)
                if account.returncode == 5:
                    rc(["--json", "admin", "service-account", "create", "root", key, password,
                        "--policy", str(work / "policy.json"), "--name", key], mutation="create-account:" + key)
                final = json.loads(rc(["--json", "admin", "access-key", "info", "root", key]).stdout)
                if final.get("parentUser") != access or final.get("impliedPolicy") is not False or json.loads(final["policy"]) != policy:
                    raise RuntimeError("scoped S3 readback differs")
                rc(["--json", "bucket", "list", "app/" + bucket + "/" + prefix + "/"])
                atomic(cluster.directory / (target + "-s3-binding.json"), {**marker, "secret": target + "/" + secret_name, "status": "IDENTITY_AUTHENTICATED", "crossTenantProbe": "NOT_RUN", "writeReadProbe": "NOT_RUN"})
        finally:
            forward.terminate()
            try:
                forward.wait(timeout=10)
            except subprocess.TimeoutExpired:
                forward.kill(); forward.wait(timeout=5)
            forward_log.close()

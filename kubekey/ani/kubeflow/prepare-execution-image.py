#!/usr/bin/env python3
"""Remote rootless build, closed wheel set, and byte-verified image output."""
import argparse
import email
import hashlib
import json
import pathlib
import shutil
import subprocess
import zipfile

parser = argparse.ArgumentParser()
parser.add_argument("--base-index", type=pathlib.Path, required=True)
parser.add_argument("--output", type=pathlib.Path, required=True)
parser.add_argument("--source-commit", required=True)
args = parser.parse_args()
raw = args.base_index.read_bytes()
expected_index = "65a93d69fa75478d554f4ad27c85c1e69fa184956261b4301ebaf6dbb0a3543d"
if hashlib.sha256(raw).hexdigest() != expected_index:
    parser.error("fixed Python base index bytes changed")
manifests = [m for m in json.loads(raw)["manifests"] if m.get("platform", {}).get("os") == "linux"
             and m.get("platform", {}).get("architecture") == "amd64"]
if len(manifests) != 1:
    parser.error("Python base has no unique linux/amd64 manifest")
base = "docker.io/library/python@" + manifests[0]["digest"]
args.output.mkdir(mode=0o700)
(args.output / "wheels").mkdir()
image_source = pathlib.Path(__file__).parent / "execution-image"
for name in ("requirements.in", "Containerfile"):
    shutil.copyfile(image_source / name, args.output / name)
tag = "localhost/ani-kubeflow-execution:26.03-" + args.source_commit[:12]
record = {"schema": "ani.kubeflow.execution-image.v1", "status": "IN_PROGRESS",
          "source_commit": args.source_commit, "base_original": "docker.io/library/python:3.11.14-slim-bookworm",
          "base_index_digest": "sha256:" + expected_index,
          "base_amd64_digest": manifests[0]["digest"], "local_build_tag": tag,
          "registry_readback": "NOT_RUN", "runtime_pull": "NOT_RUN", "steps": [], "wheels": []}


def persist():
    (args.output / "execution-image.json").write_text(json.dumps(record, indent=2) + "\n")


def run(name, command):
    with (args.output / (name + ".log")).open("wb") as log:
        result = subprocess.run(command, stdout=log, stderr=subprocess.STDOUT, timeout=1800)
    record["steps"].append({"name": name, "exit_code": result.returncode, "log": name + ".log"})
    if result.returncode:
        record.update(status="FAIL", first_error=name)
    persist()
    if result.returncode:
        raise RuntimeError(name + " failed; preserve " + str(args.output / (name + ".log")))


persist()
run("pull-fixed-base", ["podman", "pull", "--arch=amd64", base])
# The source image pins Python/pip/build tools. Resolution is allowed only at
# this material preparation step; the deployed image installs locked wheels
# offline and does not run pip during probes.
run("resolve-wheels", ["podman", "run", "--rm", "--pull=never", "--user=0:0",
    "-v", str(args.output) + ":/build:Z", base,
    "python", "-m", "pip", "wheel", "--no-build-isolation", "--wheel-dir=/build/wheels",
    "-r", "/build/requirements.in"])
requirements, seen = [], set()
for path in sorted((args.output / "wheels").glob("*.whl")):
    with zipfile.ZipFile(path) as wheel:
        metadata_names = [n for n in wheel.namelist() if n.endswith(".dist-info/METADATA")]
        if len(metadata_names) != 1:
            raise ValueError("wheel metadata is ambiguous: " + path.name)
        metadata = email.message_from_bytes(wheel.read(metadata_names[0]))
    name, version = metadata["Name"], metadata["Version"]
    canonical = name.lower().replace("_", "-")
    if canonical in seen:
        raise ValueError("two versions of one package in the wheel set: " + name)
    seen.add(canonical)
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    record["wheels"].append({"name": name, "version": version, "file": path.name, "sha256": digest})
    requirements.append(name + "==" + version + " --hash=sha256:" + digest)
if not {"kfp", "kfp-kubernetes", "boto3"}.issubset(seen):
    raise ValueError("required SDK/probe wheels are missing")
(args.output / "requirements.lock").write_text("\n".join(requirements) + "\n")
persist()
run("build-offline", ["podman", "build", "--pull=never", "--network=none",
    "--build-arg", "BASE_IMAGE=" + base, "-t", tag, "-f", str(args.output / "Containerfile"), str(args.output)])
run("verify-sdk", ["podman", "run", "--rm", "--pull=never", "--network=none", tag,
    "python", "-c", "import kfp,kfp.kubernetes,boto3; assert kfp.__version__ == '2.16.0'; assert kfp.kubernetes.__version__ == '2.16.0'; print(kfp.__version__, kfp.kubernetes.__version__)"])
run("copy-image", ["skopeo", "copy", "--preserve-digests", "containers-storage:" + tag,
                   "dir:" + str(args.output / "image")])
image_dir = args.output / "image"
manifest_bytes = (image_dir / "manifest.json").read_bytes()
manifest = json.loads(manifest_bytes)
for blob in [manifest["config"]] + manifest["layers"]:
    expected = blob["digest"].removeprefix("sha256:")
    data = (image_dir / expected).read_bytes()
    if len(data) != blob["size"] or hashlib.sha256(data).hexdigest() != expected:
        raise ValueError("execution image blob verification failed")
record.update(status="REMOTE_IMAGE_BUILT_BLOBS_VERIFIED",
              amd64_manifest_digest="sha256:" + hashlib.sha256(manifest_bytes).hexdigest(),
              original_ref="ani.local/kubeflow-execution:26.03-v1")
persist()
print(json.dumps({"status": record["status"], "source_commit": args.source_commit,
                  "wheel_count": len(record["wheels"]), "amd64_manifest_digest": record["amd64_manifest_digest"]}))

#!/usr/bin/env python3
"""Copy approved linux/amd64 bytes on the build host; never resolve tags again."""
import argparse
import hashlib
import json
import pathlib
import shutil
import subprocess

parser = argparse.ArgumentParser()
parser.add_argument("--lock", type=pathlib.Path, required=True)
parser.add_argument("--output", type=pathlib.Path, required=True)
parser.add_argument("--source-commit", required=True)
parser.add_argument("--reuse", type=pathlib.Path, help="reuse only completed entries after rechecking every byte")
args = parser.parse_args()
lock = json.loads(args.lock.read_text())
if lock["status"] != "SOURCE_IDENTITIES_COLLECTED":
    parser.error("input source identities are incomplete")
args.output.mkdir(mode=0o700)  # attempts are never merged or silently reused
record = {"schema": "ani.kubeflow.image-blobs.v1", "source_commit": args.source_commit,
          "identity_source_commit": lock["source_commit"], "status": "IN_PROGRESS",
          "images": [], "registry_readback": "NOT_RUN", "runtime_pull": "NOT_RUN"}


def persist():
    (args.output / "materialization.json").write_text(json.dumps(record, indent=2) + "\n")


persist()
reusable = {}
if args.reuse:
    previous = json.loads((args.reuse / "materialization.json").read_text())
    reusable = {row["original_ref"]: row for row in previous["images"]}
for row in lock["images"]:
    ref = row["original_ref"].rsplit(":", 1)[0] + "@" + row["amd64_manifest_digest"]
    directory = args.output / hashlib.sha256(row["original_ref"].encode()).hexdigest()[:16]
    print("materializing " + row["original_ref"], flush=True)
    old = reusable.get(row["original_ref"])
    if old and old["amd64_manifest_digest"] == row["amd64_manifest_digest"]:
        shutil.copytree(args.reuse / old["directory"], directory)
    else:
        result = subprocess.run(["skopeo", "copy", "--retry-times", "2", "--preserve-digests", "docker://" + ref,
                                 "dir:" + str(directory)], capture_output=True, timeout=1200)
        if result.returncode:
            record.update(status="FAIL", first_error={"image": row["original_ref"],
                                                       "exit_code": result.returncode})
            persist()
            raise RuntimeError("skopeo failed for " + row["original_ref"] + ": " + result.stderr.decode())
    manifest = (directory / "manifest.json").read_bytes()
    if "sha256:" + hashlib.sha256(manifest).hexdigest() != row["amd64_manifest_digest"]:
        raise ValueError("copied manifest digest changed: " + ref)
    doc = json.loads(manifest)
    blobs = [doc["config"]] + doc["layers"]
    for blob in blobs:
        algorithm, digest = blob["digest"].split(":", 1)
        if algorithm != "sha256":
            raise ValueError("unsupported blob digest: " + blob["digest"])
        data = (directory / digest).read_bytes()
        if len(data) != blob["size"] or hashlib.sha256(data).hexdigest() != digest:
            raise ValueError("blob bytes disagree with approved manifest: " + blob["digest"])
    record["images"].append(dict(row, directory=directory.name, verified_blobs=len(blobs)))
    persist()
record["status"] = "SOURCE_BLOBS_VERIFIED"
persist()

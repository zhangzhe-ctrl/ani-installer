#!/usr/bin/env python3
"""Collect source image identities on the remote build host, without cluster writes.

This is a development material step, not another product installer. Every
manifest and blob is checked again by the existing offline material pipeline.
"""
import argparse
import hashlib
import json
import pathlib
import subprocess


def raw(ref):
    result = subprocess.run(["skopeo", "inspect", "--raw", "docker://" + ref],
                            capture_output=True, timeout=180)
    if result.returncode:
        (args.output / "first-error.json").write_text(json.dumps({"operation": "source-manifest-read",
            "reference": ref, "exit_code": result.returncode,
            "stderr": result.stderr.decode(errors="replace")[:8192]}, indent=2) + "\n")
        raise RuntimeError("source image read failed rc=%d; preserve first-error.json" % result.returncode)
    return result.stdout


def digest(data):
    return "sha256:" + hashlib.sha256(data).hexdigest()


parser = argparse.ArgumentParser()
parser.add_argument("--references", type=pathlib.Path, required=True)
parser.add_argument("--output", type=pathlib.Path, required=True)
parser.add_argument("--source-commit", required=True)
args = parser.parse_args()
if args.output.exists():
    parser.error("output already exists; each collection attempt must be independent")
args.output.mkdir(parents=True, mode=0o700)
images = []
for ref in json.loads(args.references.read_text()):
    if not isinstance(ref, str) or ":" not in ref.rsplit("/", 1)[-1]:
        raise ValueError("every source image must have an explicit version: " + repr(ref))
    if ref.rsplit(":", 1)[-1] in ("latest", "master", "dummy", "8.4"):
        raise ValueError("floating or placeholder source image: " + ref)
    source = raw(ref)
    source_digest = digest(source)
    doc = json.loads(source)
    if "manifests" in doc:
        candidates = [m for m in doc["manifests"]
                      if m.get("platform", {}).get("os") == "linux"
                      and m.get("platform", {}).get("architecture") == "amd64"]
        if len(candidates) != 1:
            raise ValueError("expected exactly one linux/amd64 manifest: " + ref)
        platform_digest = candidates[0]["digest"]
        platform = raw(ref.rsplit(":", 1)[0] + "@" + platform_digest)
        if digest(platform) != platform_digest:
            raise ValueError("registry bytes disagree with platform digest: " + ref)
    else:
        platform_digest, platform = source_digest, source
        config = subprocess.run(["skopeo", "inspect", "--config", "docker://" +
                                 ref.rsplit(":", 1)[0] + "@" + platform_digest],
                                check=True, capture_output=True, timeout=180)
        image_config = json.loads(config.stdout)
        if (image_config.get("os"), image_config.get("architecture")) != ("linux", "amd64"):
            raise ValueError("single manifest is not linux/amd64: " + ref)
    stem = hashlib.sha256(ref.encode()).hexdigest()[:16]
    (args.output / (stem + ".source.json")).write_bytes(source)
    (args.output / (stem + ".amd64.json")).write_bytes(platform)
    row = {"original_ref": ref, "source_manifest_digest": source_digest,
           "amd64_manifest_digest": platform_digest, "platform": "linux/amd64",
           "source_evidence": stem + ".source.json",
           "platform_evidence": stem + ".amd64.json"}
    images.append(row)
    print(ref + " " + platform_digest, flush=True)
record = {"schema": "ani.kubeflow.source-images.v1", "source_commit": args.source_commit,
          "status": "SOURCE_IDENTITIES_COLLECTED", "blob_materialization": "NOT_RUN",
          "registry_readback": "NOT_RUN", "runtime_pull": "NOT_RUN", "images": images}
(args.output / "images.lock.json").write_text(json.dumps(record, indent=2) + "\n")

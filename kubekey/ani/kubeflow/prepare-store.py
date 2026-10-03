#!/usr/bin/env python3
"""Prepare a new first-install OCI material store from byte-verified inputs.

This build-host helper never modifies a running registry or an input store.
The existing build-offline registry-content gate remains the ship authority.
"""
import argparse
import hashlib
import json
import pathlib
import shutil
import subprocess

parser = argparse.ArgumentParser()
parser.add_argument("--source-root", type=pathlib.Path, required=True)
parser.add_argument("--blobs", type=pathlib.Path, required=True)
parser.add_argument("--execution", type=pathlib.Path, required=True)
parser.add_argument("--source-evidence", type=pathlib.Path, required=True)
parser.add_argument("--base-store", type=pathlib.Path, help="approved cumulative first-install store, copied into a new output")
parser.add_argument("--output", type=pathlib.Path, required=True)
args = parser.parse_args()
root = args.source_root
source = json.loads((args.blobs / "materialization.json").read_text())
execution = json.loads((args.execution / "execution-image.json").read_text())
if source["status"] != "SOURCE_BLOBS_VERIFIED" or execution["status"] != "REMOTE_IMAGE_BUILT_BLOBS_VERIFIED":
    raise ValueError("inputs have not passed actual byte verification")
approved = json.loads((root / "ani/kubeflow/execution-image.lock.json").read_text())
if execution["amd64_manifest_digest"] != approved["amd64_manifest_digest"] or execution["source_commit"] != approved["source_commit"]:
    raise ValueError("execution image differs from the committed approval")
table = {}
for line in (root / "ani/images.tsv").read_text().splitlines():
    if line and not line.startswith("original_ref"):
        original, reference, digest, use = line.split("\t")
        table[original] = {"reference": reference.removeprefix("127.0.0.1:5000/"), "digest": digest}
args.output.mkdir(mode=0o700)
store = args.output / "store"
if args.base_store:
    subprocess.run(["cp", "--reflink=auto", "-a", str(args.base_store), str(store)], check=True)
    index = json.loads((store / "index.json").read_text())
else:
    store.mkdir(mode=0o700)
    index = {"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": []}
    (store / "oci-layout").write_text('{"imageLayoutVersion":"1.0.0"}\n')
blobs = store / "blobs/sha256"
blobs.mkdir(parents=True, exist_ok=True)
evidence = args.output / "evidence"
evidence.mkdir()
identities = json.loads((root / "ani/kubeflow/source-images.lock.json").read_text())
identity_rows = {row["original_ref"]: row for row in identities["images"]}
source_evidence = args.source_evidence
excluded = {"ghcr.io/kubeflow/kfp-cache-deployer:2.16.0", "ghcr.io/kubeflow/kfp-cache-server:2.16.0"}
records = []


def verify(data, digest, size=None):
    if "sha256:" + hashlib.sha256(data).hexdigest() != digest or (size is not None and len(data) != size):
        raise ValueError("OCI material blob digest/size differs: " + digest)


def place(path, digest, size):
    data = path.read_bytes()
    verify(data, digest, size)
    destination = blobs / digest.removeprefix("sha256:")
    if destination.exists():
        verify(destination.read_bytes(), digest, size)
    else:
        shutil.copyfile(path, destination)


rows = [(row["original_ref"], args.blobs / row["directory"], row["amd64_manifest_digest"]) for row in source["images"] if row["original_ref"] not in excluded]
rows.append((execution["original_ref"], args.execution / "image", execution["amd64_manifest_digest"]))
existing = {d.get("annotations", {}).get("org.opencontainers.image.ref.name") for d in index["manifests"]}
for original, directory, digest in rows:
    approved_row = table[original]
    if approved_row["digest"] != digest or approved_row["reference"] in existing:
        raise ValueError("material identity differs or is already present in the input store")
    raw = (directory / "manifest.json").read_bytes()
    verify(raw, digest)
    manifest = json.loads(raw)
    place(directory / "manifest.json", digest, len(raw))
    for item in [manifest["config"], *manifest["layers"]]:
        place(directory / item["digest"].removeprefix("sha256:"), item["digest"], item["size"])
    index["manifests"].append({"mediaType": manifest["mediaType"], "digest": digest, "size": len(raw),
        "annotations": {"kind": "dev.hauler/image", "org.opencontainers.image.ref.name": approved_row["reference"]}})
    (evidence / (digest.removeprefix("sha256:") + ".json")).write_bytes(raw)
    if original in identity_rows:
        row = identity_rows[original]
        raw_source = (source_evidence / row["source_evidence"]).read_bytes()
        verify(raw_source, row["source_manifest_digest"])
        (evidence / (row["source_manifest_digest"].removeprefix("sha256:") + ".json")).write_bytes(raw_source)
    records.append({"original": original, **approved_row, "blobCount": 1 + len(manifest["layers"])})
if len(records) != 15:
    raise ValueError("the closed Kubeflow first-install image set is incomplete")
(store / "index.json").write_text(json.dumps(index, separators=(",", ":")) + "\n")
(args.output / "store-preparation.json").write_text(json.dumps({"schema": "ani.kubeflow.store-preparation.v1",
    "sourceCommit": subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip(),
    "status": "SOURCE_BYTES_STAGED / REGISTRY_RUNTIME_NOT_RUN", "images": records}, indent=2) + "\n")
print(json.dumps({"newFirstInstallImages": len(records), "storeImages": len(index["manifests"]), "status": "SOURCE_BYTES_STAGED"}))

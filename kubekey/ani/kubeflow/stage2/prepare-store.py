#!/usr/bin/env python3
"""Extend an unchanged cumulative OCI store; reverify every reused blob."""
import argparse
import hashlib
import json
import pathlib
import shutil
import subprocess

p = argparse.ArgumentParser()
for key in ("source-root", "base-store", "blobs", "workspace", "source-evidence", "output"):
    p.add_argument("--" + key, type=pathlib.Path, required=True)
a = p.parse_args()
table = {}
for line in (a.source_root / "ani/images.tsv").read_text().splitlines()[1:]:
    original, ref, digest, use = line.split("\t")
    table[original] = {"reference": ref.removeprefix("127.0.0.1:5000/"), "digest": digest}
material = json.loads((a.blobs / "materialization.json").read_text())
built = json.loads((a.workspace / "workspace-image.lock.json").read_text())
approved = json.loads((a.source_root / "ani/kubeflow/stage2/workspace-image.lock.json").read_text())
identities = json.loads((a.source_root / "ani/kubeflow/stage2/source-images.lock.json").read_text())
if material["status"] != "SOURCE_BLOBS_VERIFIED" or built != approved:
    raise ValueError("actual input bytes/build differ from committed source approvals")
rows = [(r["original_ref"], a.blobs / r["directory"], r["amd64_manifest_digest"]) for r in material["images"]]
rows.append((built["original_ref"], a.workspace / "image", built["amd64_manifest_digest"]))
if len(rows) != 8 or {r[0] for r in rows} != {r["original_ref"] for r in identities["images"]} | {built["original_ref"]}:
    raise ValueError("second-stage image closure is incomplete")

def verify(data, digest, size=None):
    if digest != "sha256:" + hashlib.sha256(data).hexdigest() or (size is not None and len(data) != size):
        raise ValueError("OCI blob bytes differ: " + digest)

index = json.loads((a.base_store / "index.json").read_text())
by_ref = {r["reference"]: r["digest"] for r in table.values()}
expected_base = set(by_ref) - {table[r[0]]["reference"] for r in rows}
base_refs = [d.get("annotations", {}).get("org.opencontainers.image.ref.name") for d in index["manifests"]]
if len(base_refs) != len(set(base_refs)) or set(base_refs) != expected_base:
    raise ValueError("cumulative store contains unknown, duplicate or missing baseline images")
for d in index["manifests"]:
    if d["digest"] != by_ref[d["annotations"]["org.opencontainers.image.ref.name"]]:
        raise ValueError("baseline image identity changed")
    raw = (a.base_store / "blobs/sha256" / d["digest"].removeprefix("sha256:")).read_bytes()
    verify(raw, d["digest"], d["size"])
    manifest = json.loads(raw)
    for b in [manifest["config"], *manifest["layers"]]:
        verify((a.base_store / "blobs/sha256" / b["digest"].removeprefix("sha256:")).read_bytes(), b["digest"], b["size"])
a.output.mkdir(mode=0o700)
store = a.output / "store"
subprocess.run(["cp", "--reflink=auto", "-a", str(a.base_store), str(store)], check=True)
evidence = a.output / "evidence"
evidence.mkdir()
records = []
for original, directory, digest in rows:
    row = table[original]
    if row["digest"] != digest:
        raise ValueError("new image differs from committed material table")
    raw = (directory / "manifest.json").read_bytes()
    verify(raw, digest)
    manifest = json.loads(raw)
    blobs = [(directory / "manifest.json", digest, len(raw))]
    blobs += [(directory / b["digest"].removeprefix("sha256:"), b["digest"], b["size"]) for b in [manifest["config"], *manifest["layers"]]]
    for path, sha, size in blobs:
        verify(path.read_bytes(), sha, size)
        dest = store / "blobs/sha256" / sha.removeprefix("sha256:")
        if dest.exists(): verify(dest.read_bytes(), sha, size)
        else: shutil.copyfile(path, dest)
    index["manifests"].append({"mediaType": manifest["mediaType"], "digest": digest, "size": len(raw),
                             "annotations": {"kind": "dev.hauler/image", "org.opencontainers.image.ref.name": row["reference"]}})
    (evidence / (digest.removeprefix("sha256:") + ".json")).write_bytes(raw)
    records.append({"original": original, **row})
for row in identities["images"]:
    raw = (a.source_evidence / row["source_evidence"]).read_bytes()
    verify(raw, row["source_manifest_digest"])
    (evidence / (row["source_manifest_digest"].removeprefix("sha256:") + ".json")).write_bytes(raw)
(store / "index.json").write_text(json.dumps(index, separators=(",", ":")) + "\n")
record = {"schema": "ani.kubeflow.stage2-store.v1", "source_commit": subprocess.check_output(
    ["git", "-C", str(a.source_root), "rev-parse", "HEAD"], text=True).strip(),
    "status": "ALL_OCI_BLOBS_VERIFIED / REGISTRY_RUNTIME_NOT_RUN", "reused_images": len(base_refs), "new_images": records}
(a.output / "store-preparation.json").write_text(json.dumps(record, indent=2) + "\n")
print(json.dumps({"reused_images": len(base_refs), "new_images": len(rows), "status": record["status"]}))

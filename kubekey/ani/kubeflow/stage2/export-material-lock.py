#!/usr/bin/env python3
"""Extend the reviewed first-stage material set with actual second-stage bytes."""
import argparse
import json
import pathlib
import yaml

p = argparse.ArgumentParser()
p.add_argument("--source-root", type=pathlib.Path, required=True)
p.add_argument("--output", type=pathlib.Path, required=True)
a = p.parse_args()
root = a.source_root
collected = json.loads((root / "ani/kubeflow/stage2/source-images.lock.json").read_text())
built = json.loads((root / "ani/kubeflow/stage2/workspace-image.lock.json").read_text())
if collected["status"] != "SOURCE_IDENTITIES_COLLECTED" or built["status"] != "REMOTE_IMAGE_BUILT_BLOBS_VERIFIED":
    raise ValueError("actual image identities are incomplete")
rows = []
for row in collected["images"]:
    original = row["original_ref"]
    rows.append({"original": original, "haulerRef": "127.0.0.1:5000/ani-kubeflow/" + original.split("/", 1)[1],
                 "sourceManifestDigest": row["source_manifest_digest"], "amd64ManifestDigest": row["amd64_manifest_digest"],
                 "platform": "linux/amd64", "use": "Kubeflow standalone Notebook/Standard serving: " + original})
rows.append({"original": built["original_ref"], "haulerRef": "127.0.0.1:5000/ani/kubeflow-jupyter:26.03-stage2-v1",
             "sourceManifestDigest": built["amd64_manifest_digest"], "amd64ManifestDigest": built["amd64_manifest_digest"],
             "platform": "linux/amd64", "use": "Native Jupyter kernel, fixed sklearn/joblib model and scoped S3 upload"})
if len(rows) != 8 or len({r["original"] for r in rows}) != 8:
    raise ValueError("the selected second-stage image set is incomplete")
table = (root / "ani/images.tsv").read_text()
existing = {line.split("\t")[0] for line in table.splitlines() if line and not line.startswith("original_ref")}
if any(r["original"] in existing for r in rows):
    raise ValueError("second-stage identities already exist; refuse duplicate or replacement")
lock_text = (root / "ani/components.lock.yaml").read_text()
lock = yaml.safe_load(lock_text)
entry = lock["kubeflow"]
if entry["release"] != "26.03-kfp2.16-trainer2.1-v1":
    raise ValueError("this extension requires the unchanged first-stage baseline")
entry["release"] = "26.03-kubeflow-stage2-v1"
entry["images"].extend(rows)
entry["stage2"] = {"sourceIdentitiesCommit": collected["source_commit"],
                   "workspaceImageBuildCommit": built["source_commit"],
                   "workspace": {"python": "3.11.14", "sklearn": "1.5.2", "joblib": "1.4.2", "format": "joblib"},
                   "runtimeStatus": "NOT_RUN"}
# Retain every preceding component byte, including its fixed version and source.
prefix, separator, old = lock_text.partition("kubeflow:\n")
if not separator or yaml.safe_load(separator + old) != {"kubeflow": yaml.safe_load(lock_text)["kubeflow"]}:
    raise ValueError("Kubeflow must remain the final source lock section")
a.output.mkdir(mode=0o700)
(a.output / "components.lock.yaml").write_text(prefix + yaml.safe_dump({"kubeflow": entry}, sort_keys=False))
additions = "".join("\t".join([r["original"], r["haulerRef"], r["amd64ManifestDigest"], r["use"]]) + "\n" for r in rows)
(a.output / "images.tsv").write_text(table.rstrip("\n") + "\n" + additions)
print(json.dumps({"new_images": len(rows), "runtime_status": "NOT_RUN"}))

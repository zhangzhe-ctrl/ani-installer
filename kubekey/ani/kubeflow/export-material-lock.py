#!/usr/bin/env python3
"""Export actual collected identities into the existing ANI material formats."""
import argparse
import json
import pathlib
import yaml

parser = argparse.ArgumentParser()
parser.add_argument("--source-root", type=pathlib.Path, required=True)
parser.add_argument("--output", type=pathlib.Path, required=True)
args = parser.parse_args()
args.output.mkdir(mode=0o700)
root = args.source_root
collected = json.loads((root / "ani/kubeflow/source-images.lock.json").read_text())
built = json.loads((root / "ani/kubeflow/execution-image.lock.json").read_text())
if collected["status"] != "SOURCE_IDENTITIES_COLLECTED" or built["status"] != "REMOTE_IMAGE_BUILT_BLOBS_VERIFIED":
    raise ValueError("source identities or execution image are incomplete")
# Preserved as research evidence, but the V1 cache webhook is not shipped.
excluded = {"ghcr.io/kubeflow/kfp-cache-deployer:2.16.0", "ghcr.io/kubeflow/kfp-cache-server:2.16.0"}
rows = []
for row in collected["images"]:
    original = row["original_ref"]
    if original in excluded:
        continue
    rows.append({"original": original, "haulerRef": "127.0.0.1:5000/ani-kubeflow/" + original.split("/", 1)[1],
        "sourceManifestDigest": row["source_manifest_digest"], "amd64ManifestDigest": row["amd64_manifest_digest"],
        "platform": "linux/amd64", "use": "Kubeflow first-install backend/dynamic runtime: " + original})
rows.append({"original": built["original_ref"], "haulerRef": "127.0.0.1:5000/ani/kubeflow-execution:26.03-v1",
    "sourceManifestDigest": built["amd64_manifest_digest"], "amd64ManifestDigest": built["amd64_manifest_digest"],
    "platform": "linux/amd64", "use": "Kubeflow single-process runtime and offline SDK/environment probes"})
table = (root / "ani/images.tsv").read_text()
existing = {line.split("\t")[0] for line in table.splitlines() if line and not line.startswith("original_ref")}
if any(row["original"] in existing for row in rows):
    raise ValueError("Kubeflow rows already exist; do not append a second version blindly")
new_table = "".join("\t".join([r["original"], r["haulerRef"], r["amd64ManifestDigest"], r["use"]]) + "\n" for r in rows)
(args.output / "images.tsv").write_text(table.rstrip("\n") + "\n" + new_table)
lock = (root / "ani/components.lock.yaml").read_text()
if "kubeflow" in yaml.safe_load(lock):
    raise ValueError("Kubeflow lock already exists")
entry = {"release": "26.03-kfp2.16-trainer2.1-v1", "status": "source_bytes_verified / runtime_not_verified",
         "sourceIdentitiesCommit": collected["source_commit"], "executionImageBuildCommit": built["source_commit"],
         "images": rows, "sdk": {"kfp": "2.16.0", "kfp-kubernetes": "2.16.0", "wheels": built["wheels"]}}
(args.output / "components.lock.yaml").write_text(lock.rstrip("\n") + "\n\n# Kubeflow first-install-only material; no addition contract.\n" + yaml.safe_dump({"kubeflow": entry}, sort_keys=False))
print(json.dumps({"new_image_count": len(rows), "status": entry["status"]}))

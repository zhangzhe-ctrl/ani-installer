#!/usr/bin/env python3
"""Remote-only preparation of source-bound offline Kubeflow role assets."""
import argparse
import hashlib
import json
import pathlib
import shutil
import subprocess

FILES = {
    "common.py": "ani/kubeflow/common.py", "resources.py": "ani/kubeflow/resources.py",
    "storage.py": "ani/kubeflow/storage.py", "install.py": "ani/kubeflow/install.py",
    "check.py": "ani/kubeflow/check.py", "resources.json": "ani/kubeflow/overlay/resources.json",
    "overlay.lock.json": "ani/kubeflow/overlay/overlay.lock.json",
    "execution-image.lock.json": "ani/kubeflow/execution-image.lock.json",
    "requirements.lock": "ani/kubeflow/execution-image/requirements.lock",
    "probe-run.py": "ani/kubeflow/probes/run.py",
    "probe-pipeline.py": "ani/kubeflow/probes/pipeline.py",
    "probe-pipeline.yaml": "ani/kubeflow/probes/pipeline.yaml",
}
RELEASE = "26.03-kfp2.16-trainer2.1-v1"
parser = argparse.ArgumentParser()
parser.add_argument("--source-root", type=pathlib.Path, required=True)
parser.add_argument("--overlay-dir", type=pathlib.Path, help="same-commit generated overlay for an approval proposal only")
parser.add_argument("--wheels-dir", type=pathlib.Path, help="actual closed SDK wheel directory, required when packaging")
group = parser.add_mutually_exclusive_group(required=True)
group.add_argument("--approval-output", type=pathlib.Path)
group.add_argument("--output", type=pathlib.Path)
args = parser.parse_args()
root = args.source_root.resolve()
if args.overlay_dir and not args.approval_output:
    raise ValueError("a build consumes committed overlay bytes, not a proposal directory")
actual = {name: hashlib.sha256((args.overlay_dir / name if args.overlay_dir and name in ("resources.json", "overlay.lock.json") else root / source).read_bytes()).hexdigest() for name, source in FILES.items()}
if args.approval_output:
    # Generate outside the checkout. Return for local review and commit before
    # the final published commit's remote gate/build consumes this approval.
    record = {"schema": "ani.kubeflow.assets.v1", "release": RELEASE,
              "generatedFromCommit": subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip(),
              "files": actual}
    with args.approval_output.open("x") as stream:
        json.dump(record, stream, indent=2); stream.write("\n")
else:
    approval = root / "pkg/ani/kubeflow-assets.lock.json"
    record = json.loads(approval.read_text())
    if record["release"] != RELEASE or record["files"] != actual:
        raise ValueError("source assets differ from the reviewed code-bound approval")
    image = json.loads((root / FILES["execution-image.lock.json"]).read_text())
    if not args.wheels_dir or not args.wheels_dir.is_dir():
        raise ValueError("the closed SDK wheels must be physically supplied")
    if {p.name for p in args.wheels_dir.iterdir()} != {w["file"] for w in image["wheels"]}:
        raise ValueError("wheel directory differs from the approved closed set")
    for wheel in image["wheels"]:
        name = wheel["file"]
        if pathlib.Path(name).name != name or hashlib.sha256((args.wheels_dir / name).read_bytes()).hexdigest() != wheel["sha256"]:
            raise ValueError("wheel digest differs from the source-bound image approval")
    args.output.mkdir(mode=0o700)
    for name, source in FILES.items():
        shutil.copyfile(root / source, args.output / name)
    shutil.copyfile(approval, args.output / "assets.lock.json")
    shutil.copytree(args.wheels_dir, args.output / "wheels")
    print(json.dumps({"release": RELEASE, "files": len(FILES), "wheels": len(image["wheels"]), "approvalSha256": hashlib.sha256(approval.read_bytes()).hexdigest()}))

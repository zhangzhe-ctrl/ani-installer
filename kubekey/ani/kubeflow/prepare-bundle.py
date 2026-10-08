#!/usr/bin/env python3
"""Remote-only preparation of source-bound offline Kubeflow role assets."""
import argparse
import hashlib
import json
import pathlib
import shutil
import subprocess
import yaml

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
    "probe-compile.lock.json": "ani/kubeflow/probes/compile.lock.json",
    "stage2.py": "ani/kubeflow/stage2.py",
    "stage2-resources.json": "ani/kubeflow/stage2/overlay/resources.json",
    "stage2-overlay.lock.json": "ani/kubeflow/stage2/overlay/overlay.lock.json",
    "workspace-image.lock.json": "ani/kubeflow/stage2/workspace-image.lock.json",
    "workspace-requirements.lock": "ani/kubeflow/stage2/workspace/requirements.lock",
    "kernel-probe.py": "ani/kubeflow/stage2/workspace/kernel-probe.py",
    "model.py": "ani/kubeflow/stage2/workspace/model.py",
}
RELEASE = "26.03-kubeflow-stage2-v1"
parser = argparse.ArgumentParser()
parser.add_argument("--source-root", type=pathlib.Path, required=True)
parser.add_argument("--overlay-dir", type=pathlib.Path, help="same-commit generated overlay for an approval proposal only")
parser.add_argument("--wheels-dir", type=pathlib.Path, help="actual closed SDK wheel directory, required when packaging")
parser.add_argument("--grpc-source-archive", type=pathlib.Path, help="actual pinned client source archive, required when packaging")
parser.add_argument("--workspace-wheels-dir", type=pathlib.Path, help="closed Notebook image wheel set")
group = parser.add_mutually_exclusive_group(required=True)
group.add_argument("--approval-output", type=pathlib.Path)
group.add_argument("--output", type=pathlib.Path)
group.add_argument("--verify-output", type=pathlib.Path, help="verify the exact source-bound material directory without writes")
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
    wheels_dir = args.wheels_dir or (args.verify_output / "wheels" if args.verify_output else None)
    grpc_source = args.grpc_source_archive or (args.verify_output / "grpc-source.tar" if args.verify_output else None)
    if not wheels_dir or not wheels_dir.is_dir():
        raise ValueError("the closed SDK wheels must be physically supplied")
    if {p.name for p in wheels_dir.iterdir()} != {w["file"] for w in image["wheels"]}:
        raise ValueError("wheel directory differs from the approved closed set")
    for wheel in image["wheels"]:
        name = wheel["file"]
        if pathlib.Path(name).name != name or hashlib.sha256((wheels_dir / name).read_bytes()).hexdigest() != wheel["sha256"]:
            raise ValueError("wheel digest differs from the source-bound image approval")
    workspace = json.loads((root / FILES["workspace-image.lock.json"]).read_text())
    workspace_wheels = args.workspace_wheels_dir or (args.verify_output / "workspace-wheels" if args.verify_output else None)
    if workspace["status"] != "REMOTE_IMAGE_BUILT_BLOBS_VERIFIED" or not workspace_wheels or not workspace_wheels.is_dir():
        raise ValueError("the closed Notebook workspace materials must be physically supplied")
    if {p.name for p in workspace_wheels.iterdir()} != {w["file"] for w in workspace["wheels"]}:
        raise ValueError("Notebook wheel directory differs from the approved closed set")
    for wheel in workspace["wheels"]:
        if pathlib.Path(wheel["file"]).name != wheel["file"] or hashlib.sha256((workspace_wheels / wheel["file"]).read_bytes()).hexdigest() != wheel["sha256"]:
            raise ValueError("Notebook wheel bytes differ from source approval")
    tool = yaml.safe_load((root / "ani/components.lock.yaml").read_text())["tools"]["ani-kfp-grpc-check"]
    if not grpc_source or hashlib.sha256(grpc_source.read_bytes()).hexdigest() != tool["sourceTarballSha256"]:
        raise ValueError("gRPC client source archive differs from the reviewed material lock")
    if args.verify_output:
        destination = args.verify_output
        if destination.name != RELEASE or destination.parent.name != "kubeflow" or destination.parent.parent.name != "manifests":
            raise ValueError("only the fixed Kubeflow material release directory is allowed")
        if {p.name for p in destination.parent.parent.iterdir()} != {"kubeflow"} or {p.name for p in destination.parent.iterdir()} != {RELEASE}:
            raise ValueError("unknown manifest material directory")
        expected = set(FILES) | {"assets.lock.json", "grpc-source.tar", "wheels", "workspace-wheels"}
        if {p.name for p in destination.iterdir()} != expected:
            raise ValueError("unknown or missing release material")
        for path in (destination.parent.parent, destination.parent, destination, *destination.parent.parent.rglob("*")):
            if path.is_symlink() or not (path.is_file() or path.is_dir()):
                raise ValueError("material must be regular files/directories")
        if (destination / "assets.lock.json").read_bytes() != approval.read_bytes():
            raise ValueError("material approval differs from the source-bound approval")
        for name, digest in actual.items():
            if hashlib.sha256((destination / name).read_bytes()).hexdigest() != digest:
                raise ValueError("material differs from source-approved bytes: " + name)
        print(json.dumps({"release": RELEASE, "status": "SOURCE_BOUND_LAYOUT_VERIFIED", "files": len(FILES), "wheels": len(image["wheels"])}))
        raise SystemExit(0)
    args.output.mkdir(mode=0o700)
    for name, source in FILES.items():
        shutil.copyfile(root / source, args.output / name)
    shutil.copyfile(approval, args.output / "assets.lock.json")
    shutil.copytree(wheels_dir, args.output / "wheels")
    shutil.copytree(workspace_wheels, args.output / "workspace-wheels")
    shutil.copyfile(grpc_source, args.output / "grpc-source.tar")
    print(json.dumps({"release": RELEASE, "files": len(FILES), "wheels": len(image["wheels"]), "approvalSha256": hashlib.sha256(approval.read_bytes()).hexdigest()}))

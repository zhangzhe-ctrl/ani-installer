#!/usr/bin/env python3
"""Build-host regression using the actual reviewed role/wheels/source material."""
import argparse
import hashlib
import json
import pathlib
import shutil
import subprocess
import tempfile

parser = argparse.ArgumentParser()
parser.add_argument("--source-root", type=pathlib.Path, required=True)
parser.add_argument("--wheels-dir", required=True)
parser.add_argument("--grpc-source-archive", required=True)
args = parser.parse_args()
helper = args.source_root / "ani/kubeflow/prepare-bundle.py"
release = "26.03-kfp2.16-trainer2.1-v1"

with tempfile.TemporaryDirectory(prefix="material-layout-regression-") as temporary:
    root = pathlib.Path(temporary)
    baseline = root / "baseline/manifests/kubeflow" / release
    baseline.parent.mkdir(parents=True)
    subprocess.run(["python3", str(helper), "--source-root", str(args.source_root), "--wheels-dir", args.wheels_dir,
                    "--grpc-source-archive", args.grpc_source_archive, "--output", str(baseline)], check=True)

    def add_unknown_manifest(destination):
        (destination.parent.parent / "unknown.yaml").write_text("unexpected: true\n")

    def add_unknown_release(destination):
        (destination.parent / "unapproved-release").mkdir()

    def add_unknown_file(destination):
        (destination / "unapproved.py").write_text("print('unapproved')\n")

    def mutate_asset(destination):
        (destination / "common.py").write_text("print('wrong source')\n")

    def regenerate_approval(destination):
        mutate_asset(destination)
        path = destination / "assets.lock.json"
        approval = json.loads(path.read_text())
        approval["files"]["common.py"] = hashlib.sha256((destination / "common.py").read_bytes()).hexdigest()
        path.write_text(json.dumps(approval))

    def missing_wheel(destination):
        next((destination / "wheels").iterdir()).unlink()

    def changed_wheel(destination):
        next((destination / "wheels").iterdir()).write_bytes(b"wrong wheel bytes")

    def symlink_asset(destination):
        path = destination / "common.py"
        path.unlink()
        path.symlink_to(baseline / "common.py")

    cases = [("reviewed-layout", None), ("unknown-manifest", add_unknown_manifest), ("unknown-release", add_unknown_release),
             ("unknown-file", add_unknown_file), ("changed-source", mutate_asset), ("regenerated-approval", regenerate_approval),
             ("missing-wheel", missing_wheel), ("changed-wheel", changed_wheel), ("symlink", symlink_asset)]
    for name, mutation in cases:
        destination = root / name / "manifests/kubeflow" / release
        shutil.copytree(baseline, destination)
        if mutation:
            mutation(destination)
        result = subprocess.run(["python3", str(helper), "--source-root", str(args.source_root), "--verify-output", str(destination)],
                                text=True, capture_output=True, timeout=30)
        passed = result.returncode != 0 if mutation else result.returncode == 0
        print(json.dumps({"case": name, "exit_code": result.returncode, "result": "PASS" if passed else "FAIL"}), flush=True)
        if not passed:
            raise RuntimeError("layout regression failed: " + name + ": " + result.stderr)
    print(json.dumps({"cases": len(cases), "result": "PASS"}))

#!/usr/bin/env python3
"""Build and inspect the closed Jupyter workspace image on Fedora only."""
import argparse
import email
import hashlib
import json
import os
import pathlib
import shutil
import subprocess
import zipfile


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-index", type=pathlib.Path, required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--storage-root", type=pathlib.Path, required=True)
    parser.add_argument("--run-root", type=pathlib.Path, required=True)
    args = parser.parse_args()
    root = pathlib.Path(__file__).resolve().parents[3]
    head = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()
    if head != args.source_commit:
        raise ValueError("workspace build must consume the declared pushed source commit")
    raw = args.base_index.read_bytes()
    expected = "65a93d69fa75478d554f4ad27c85c1e69fa184956261b4301ebaf6dbb0a3543d"
    if hashlib.sha256(raw).hexdigest() != expected:
        raise ValueError("fixed Python 3.11.14 base index bytes differ")
    manifests = [v for v in json.loads(raw)["manifests"] if v.get("platform", {}).get("os") == "linux"
                 and v.get("platform", {}).get("architecture") == "amd64"]
    if len(manifests) != 1:
        raise ValueError("base image platform is ambiguous")
    base = "docker.io/library/python@" + manifests[0]["digest"]
    args.output.mkdir(mode=0o700)
    if not all(p.is_absolute() for p in (args.output, args.storage_root, args.run_root)):
        raise ValueError("image work must use absolute task-private paths")
    storage = args.output / "storage.conf"
    storage.write_text('[storage]\ndriver="overlay"\ngraphroot=' + json.dumps(str(args.storage_root)) +
                       "\nrunroot=" + json.dumps(str(args.run_root)) + "\n")
    os.environ["CONTAINERS_STORAGE_CONF"] = str(storage)
    source = pathlib.Path(__file__).parent / "workspace"
    for name in ("requirements.in", "Containerfile", "model.py"):
        shutil.copyfile(source / name, args.output / name)
    (args.output / "wheels").mkdir()
    tag = "localhost/ani-kubeflow-jupyter:stage2-" + head[:12]
    record = {"schema": "ani.kubeflow.workspace-image.v1", "source_commit": head,
              "original_ref": "ani.local/kubeflow-jupyter:26.03-stage2-v1", "status": "IN_PROGRESS",
              "base_index_digest": "sha256:" + expected, "base_amd64_digest": manifests[0]["digest"],
              "steps": [], "wheels": [], "runtime_pull": "NOT_RUN"}

    def persist():
        (args.output / "workspace-image.lock.json").write_text(json.dumps(record, indent=2) + "\n")

    def run(name, command):
        with (args.output / (name + ".log")).open("wb") as log:
            result = subprocess.run(command, stdout=log, stderr=subprocess.STDOUT, timeout=1800)
        record["steps"].append({"name": name, "exit_code": result.returncode, "log": name + ".log"})
        if result.returncode:
            record.update(status="FAIL", first_error=name)
        persist()
        if result.returncode:
            raise RuntimeError(name + " failed; preserve its first-error log")

    persist()
    run("pull-fixed-base", ["podman", "pull", "--arch=amd64", base])
    run("resolve-wheels", ["podman", "run", "--rm", "--pull=never", "--network=host", "--user=0:0",
        "-v", str(args.output) + ":/build:Z", base, "python", "-m", "pip", "download",
        "--only-binary=:all:", "--dest=/build/wheels", "-r", "/build/requirements.in"])
    requirements = []
    for path in sorted((args.output / "wheels").glob("*.whl")):
        with zipfile.ZipFile(path) as wheel:
            names = [n for n in wheel.namelist() if n.endswith(".dist-info/METADATA")]
            if len(names) != 1:
                raise ValueError("ambiguous wheel metadata")
            metadata = email.message_from_bytes(wheel.read(names[0]))
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        record["wheels"].append({"name": metadata["Name"], "version": metadata["Version"],
                                "file": path.name, "sha256": digest})
        requirements.append(metadata["Name"] + "==" + metadata["Version"] + " --hash=sha256:" + digest)
    (args.output / "requirements.lock").write_text("\n".join(requirements) + "\n")
    run("build-offline", ["podman", "build", "--pull=never", "--network=none", "--build-arg", "BASE_IMAGE=" + base,
                           "-t", tag, "-f", str(args.output / "Containerfile"), str(args.output)])
    run("verify-model", ["podman", "run", "--rm", "--pull=never", "--network=none", tag, "python", "-c",
        "import json,jupyterlab,ipykernel,model; print(json.dumps(model.generate('/tmp/model'))); "
        "assert jupyterlab.__version__ == '4.4.10'; assert ipykernel.__version__ == '6.30.1'"])
    run("save-image", ["podman", "save", "--format=oci-archive", "--output=" + str(args.output / "workspace.oci.tar"), tag])
    run("copy-image", ["skopeo", "copy", "--preserve-digests", "oci-archive:" + str(args.output / "workspace.oci.tar"),
                       "dir:" + str(args.output / "image")])
    data = (args.output / "image/manifest.json").read_bytes()
    manifest = json.loads(data)
    for blob in [manifest["config"], *manifest["layers"]]:
        content = (args.output / "image" / blob["digest"].removeprefix("sha256:")).read_bytes()
        if len(content) != blob["size"] or "sha256:" + hashlib.sha256(content).hexdigest() != blob["digest"]:
            raise ValueError("workspace image blob bytes differ")
    record.update(status="REMOTE_IMAGE_BUILT_BLOBS_VERIFIED", amd64_manifest_digest="sha256:" + hashlib.sha256(data).hexdigest())
    persist()
    print(json.dumps({"status": record["status"], "source_commit": head,
                      "amd64_manifest_digest": record["amd64_manifest_digest"], "wheels": len(record["wheels"])}))


if __name__ == "__main__":
    main()

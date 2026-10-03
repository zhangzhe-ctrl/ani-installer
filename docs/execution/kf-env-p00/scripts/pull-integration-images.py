"""Lab-only explicit containerd pulls from the declared offline registry.

The coordinator holds the real ANI product lock on ani-01 while executing this
bounded preparation on the verified nodes. No daemon reconfiguration/restart,
cache deletion, floating reference or public fallback. This development cache
evidence cannot replace the final first-install's normal kubelet pull evidence.
"""
import argparse
import hashlib
import json
import pathlib
import subprocess

parser = argparse.ArgumentParser()
parser.add_argument("--site", required=True)
parser.add_argument("--output", required=True)
args = parser.parse_args()
data = pathlib.Path(args.site).read_bytes()
if hashlib.sha256(data).hexdigest() != "dddf711c5217afff54536096f57bcbe07648f1c1f02ef5cde4786aa867b2da27":
    raise ValueError("development site differs from the reviewed binding")
images = json.loads(data)["images"]
if len(images) != 15 or any(not v.startswith("172.16.101.10:5001/") or "@sha256:" not in v or
    (not v.startswith("172.16.101.10:5001/ani-kubeflow/") and k != "ani.local/kubeflow-execution:26.03-v1")
    for k, v in images.items()):
    raise ValueError("development image scope differs")
output = pathlib.Path(args.output)
output.mkdir(mode=0o700)
record = {"status": "IN_PROGRESS", "transport": "declared HTTP offline registry; no TLS bypass", "images": []}
for index, (original, image) in enumerate(sorted(images.items())):
    named, digest = image.rsplit("@", 1)
    # Kubelet/CRI normalizes a tag@digest to repository@digest. Store the
    # exact same manifest under that identity, not an unusable tag@digest key.
    reference = named.rsplit(":", 1)[0] + "@" + digest
    row = {"original": original, "image": image, "clientReference": reference, "result": "UNKNOWN"}
    record["images"].append(row)
    (output / "report.json").write_text(json.dumps(record, indent=2) + "\n")
    with (output / (str(index) + ".log")).open("x") as log:
        result = subprocess.run(["timeout", "120", "ctr", "--namespace", "k8s.io", "images", "pull", "--plain-http", reference], stdout=log, stderr=subprocess.STDOUT)
    row.update(exitCode=result.returncode, result="CONFIRMED" if result.returncode == 0 else "FAIL")
    if result.returncode:
        record["status"] = "FAIL"
        (output / "report.json").write_text(json.dumps(record, indent=2) + "\n")
        raise SystemExit(result.returncode)
    inspection = subprocess.run(["crictl", "--runtime-endpoint", "unix:///run/containerd/containerd.sock",
        "--image-endpoint", "unix:///run/containerd/containerd.sock", "inspecti", reference], text=True, capture_output=True, timeout=30)
    row["criInspectExitCode"] = inspection.returncode
    status = json.loads(inspection.stdout)["status"] if inspection.returncode == 0 else {}
    row["cri"] = {"id": status.get("id"), "repoDigests": status.get("repoDigests", [])}
    if inspection.returncode or reference not in row["cri"]["repoDigests"]:
        record["status"] = "FAIL"
        (output / "report.json").write_text(json.dumps(record, indent=2) + "\n")
        raise SystemExit(inspection.returncode or 1)
    (output / "report.json").write_text(json.dumps(record, indent=2) + "\n")
record["status"] = "EXPLICIT_CONTAINERD_PULLS_COMPLETE"
(output / "report.json").write_text(json.dumps(record, indent=2) + "\n")
print(json.dumps({"status": record["status"], "images": len(images)}))

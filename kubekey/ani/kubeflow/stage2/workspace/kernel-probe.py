#!/usr/bin/env python3
"""Exercise the running Jupyter server and its real kernel, never shell-train."""
import argparse
import hashlib
import json
import os
import pathlib
import re
import time
import urllib.parse
import urllib.request
import uuid

import websocket


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--model-key", required=True)
    parser.add_argument("--readback", action="store_true")
    args = parser.parse_args()
    if not re.fullmatch(r"[a-z0-9-]{1,63}", args.run_id):
        raise ValueError("invalid dedicated probe run ID")
    token = os.environ["JUPYTER_TOKEN"]
    prefix = os.environ.get("NB_PREFIX", "/").rstrip("/") + "/"
    base = "http://127.0.0.1:8888" + prefix
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def request(path, method="GET", value=None):
        payload = None if value is None else json.dumps(value).encode()
        req = urllib.request.Request(base + path, data=payload, method=method,
            headers={"Authorization": "token " + token, "Content-Type": "application/json"})
        with opener.open(req, timeout=30) as response:
            return response.read()

    lab = request("lab").decode()
    sources = re.findall(r'<script[^>]+src="([^"]+)"', lab)
    sources = [p for p in sources if "static/" in p]
    if not sources:
        raise RuntimeError("native JupyterLab page has no application script")
    asset = urllib.parse.urljoin(base + "lab", sources[0])
    if urllib.parse.urlsplit(asset).netloc != "127.0.0.1:8888":
        raise RuntimeError("JupyterLab asset is outside the native server")
    relative = asset.removeprefix(base)
    if relative == asset or not request(relative):
        raise RuntimeError("JupyterLab application resource failed to load")
    directory = "acceptance-" + args.run_id
    notebook_path = directory + "/main-flow.ipynb"
    if args.readback:
        content = json.loads(request("api/contents/" + notebook_path))
        if content["type"] != "notebook" or not content["content"]["cells"]:
            raise RuntimeError("saved native notebook is absent after resume")
        marker = pathlib.Path(directory) / "fixed-content.txt"
        if marker.read_text() != "ANI stage2 persistent workspace\n":
            raise RuntimeError("workspace content differs after resume")
        model = pathlib.Path(directory) / "model.joblib"
        print(json.dumps({"status": "NATIVE_CONTENT_READBACK", "marker_sha256": hashlib.sha256(marker.read_bytes()).hexdigest(),
                          "model_sha256": hashlib.sha256(model.read_bytes()).hexdigest(), "base_path": prefix}))
        return
    if pathlib.Path(directory).exists():
        raise RuntimeError("this run already owns workspace outputs; use explicit readback")
    code = ("import json,pathlib,model\n"
        "directory=" + repr(directory) + "\n"
        "record=model.generate(directory)\n"
        "pathlib.Path(directory,'fixed-content.txt').write_text('ANI stage2 persistent workspace\\n')\n"
        "record['s3']=model.upload(directory," + repr(args.model_key) + ")\n"
        "print('ANI_RESULT='+json.dumps(record))\n")
    request("api/contents/" + directory, "PUT", {"type": "directory"})
    document = {"cells": [{"cell_type": "code", "execution_count": None, "metadata": {}, "outputs": [], "source": code}],
                "metadata": {"kernelspec": {"display_name": "Python 3", "language": "python", "name": "python3"}},
                "nbformat": 4, "nbformat_minor": 5}
    request("api/contents/" + notebook_path, "PUT", {"type": "notebook", "format": "json", "content": document})
    saved = json.loads(request("api/contents/" + notebook_path))
    if saved["content"]["cells"][0]["source"] != code:
        raise RuntimeError("native notebook save/read differs")
    kernel = json.loads(request("api/kernels", "POST", {"name": "python3"}))
    socket = None
    try:
        address = base.replace("http://", "ws://") + "api/kernels/" + kernel["id"] + "/channels"
        socket = websocket.create_connection(address, header=["Authorization: token " + token],
            origin="http://127.0.0.1:8888", timeout=30, http_no_proxy=["127.0.0.1"])
        msg_id = str(uuid.uuid4())
        socket.send(json.dumps({"header": {"msg_id": msg_id, "username": "ani-environment-probe",
            "session": str(uuid.uuid4()), "msg_type": "execute_request", "version": "5.3"},
            "parent_header": {}, "metadata": {}, "channel": "shell", "content": {"code": code,
            "silent": False, "store_history": True, "user_expressions": {}, "allow_stdin": False, "stop_on_error": True}}))
        output, execution, idle = "", None, False
        deadline = time.monotonic() + 180
        while time.monotonic() < deadline and not (execution and idle):
            message = json.loads(socket.recv())
            if message.get("parent_header", {}).get("msg_id") != msg_id:
                continue
            kind = message["header"]["msg_type"]
            if kind == "stream" and message["content"]["name"] == "stdout":
                output += message["content"]["text"]
            elif kind == "execute_reply":
                execution = message["content"]
                if execution["status"] != "ok":
                    raise RuntimeError("real Jupyter kernel failed: " + execution.get("ename", "unknown"))
            elif kind == "status" and message["content"]["execution_state"] == "idle":
                idle = True
        if not execution or not idle:
            raise RuntimeError("real Jupyter kernel execution did not reach idle")
        lines = [v.removeprefix("ANI_RESULT=") for v in output.splitlines() if v.startswith("ANI_RESULT=")]
        if len(lines) != 1:
            raise RuntimeError("kernel model result is absent or ambiguous")
        record = json.loads(lines[0])
        if record["expected"] != [0, 1, 2] or record["sha256"] != record["s3"]["sha256"]:
            raise RuntimeError("kernel model and upload identities differ")
        document["cells"][0].update(execution_count=execution["execution_count"], outputs=[
            {"output_type": "stream", "name": "stdout", "text": "ANI_RESULT=" + json.dumps(record) + "\n"}])
        request("api/contents/" + notebook_path, "PUT", {"type": "notebook", "format": "json", "content": document})
        final = json.loads(request("api/contents/" + notebook_path))
        if final["content"]["cells"][0]["execution_count"] != execution["execution_count"]:
            raise RuntimeError("executed notebook was not saved")
        print(json.dumps({"status": "REAL_JUPYTER_KERNEL_MODEL_UPLOADED", "base_path": prefix,
            "kernel_id": kernel["id"], "execution_count": execution["execution_count"], "model": record,
            "notebook": notebook_path, "marker_sha256": hashlib.sha256(pathlib.Path(directory, "fixed-content.txt").read_bytes()).hexdigest()}))
    finally:
        if socket:
            socket.close()
        request("api/kernels/" + kernel["id"], "DELETE")


if __name__ == "__main__":
    main()

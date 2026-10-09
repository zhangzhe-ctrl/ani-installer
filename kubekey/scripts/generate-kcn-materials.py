#!/usr/bin/env python3
"""Generate the complete, pinned KCN first-install assets on the build host.

The approved archive is extracted into a private temporary tree. Neither the
upstream checkout nor its incomplete make targets are used as runtime inputs.
"""
import argparse
import hashlib
import json
import pathlib
import subprocess
import tarfile
import tempfile

import yaml

SOURCE = "2b9467c66dc9024fbf321a4b3a98a9e5f3fef377"
ARCHIVE_SHA256 = "303324e153bb23557b475c1f95a73eca12967e0a27106d9696734ad005917855"
IMAGE = "docker.changqingyun.cn/kubercloud/kc-networking@sha256:494432d2f7b896eb953647166c6aa18d6ea2113b133d40d82127cfcbe416712f"
AMD64 = "sha256:26470989d18f7c14ba21823c80709678b44149d7a1ec8282aab1906f11a77234"


def verify_image(archive):
    with tarfile.open(archive) as stream:
        def blob(digest):
            data = stream.extractfile("blobs/sha256/" + digest.split(":")[1]).read()
            if "sha256:" + hashlib.sha256(data).hexdigest() != digest:
                raise ValueError("KCN supplied image blob digest differs")
            return data
        index = json.loads(blob(IMAGE.split("@")[1]))
        platforms = [m for m in index['manifests'] if m.get('platform') == {'architecture': 'amd64', 'os': 'linux'}]
        if len(platforms) != 1 or platforms[0]['digest'] != AMD64:
            raise ValueError("KCN supplied index does not bind the approved amd64 manifest")
        manifest = json.loads(blob(AMD64))
        config = json.loads(blob(manifest['config']['digest']))
        if config['architecture'] != 'amd64' or config['os'] != 'linux':
            raise ValueError("KCN platform config differs")
        for layer in manifest['layers']: blob(layer['digest'])


def generate(archive, image_archive, kustomize, output):
    if hashlib.sha256(archive.read_bytes()).hexdigest() != ARCHIVE_SHA256:
        raise ValueError("KCN source archive differs from the fixed approved source")
    verify_image(image_archive)
    version = subprocess.check_output([str(kustomize), "version"], text=True).strip()
    if version != "v5.8.1":
        raise ValueError("KCN generation requires kustomize v5.8.1")
    output.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory(prefix="kcn-materials-") as temporary:
        source = pathlib.Path(temporary)
        with tarfile.open(archive) as stream:
            stream.extractall(source, filter="data")
        build = source / "config/build-installer/kustomization.yaml"
        controller = source / "config/controller/kustomization.yaml"
        if not build.is_file() or not controller.is_file():
            raise ValueError("approved source archive layout differs")
        build_data = yaml.safe_load(build.read_text())
        build_data["resources"].append("../controller")
        build.write_text(yaml.safe_dump(build_data, sort_keys=False))
        controller_data = yaml.safe_load(controller.read_text())
        controller_data["resources"].append("configmap/configmap.yaml")
        controller_data["images"] = [{"name": "controller", "newName": IMAGE.split("@")[0], "digest": IMAGE.split("@")[1]}]
        controller.write_text(yaml.safe_dump(controller_data, sort_keys=False))
        raw = subprocess.check_output([str(kustomize), "build", str(build.parent)])
    values = list(yaml.safe_load_all(raw))
    if any(not isinstance(v, dict) for v in values):
        raise ValueError("KCN generated an empty object")
    ids = [(v["kind"], v["metadata"]["name"]) for v in values]
    if len(ids) != len(set(ids)):
        raise ValueError("duplicate KCN object identity")
    crds = sorted(v["metadata"]["name"] for v in values if v["kind"] == "CustomResourceDefinition")
    if len(crds) != 17 or "basicnetworkisolations.networking.kubercloud.com" not in crds:
        raise ValueError("KCN requires the full 17-CRD source contract")
    workloads = {(v["kind"], v["metadata"]["name"]) for v in values if v["kind"] in ("Deployment", "DaemonSet")}
    if workloads != {("Deployment", "kcn-controller"), ("Deployment", "kcn-ovn-central"),
                     ("DaemonSet", "kcn-cni-ds"), ("DaemonSet", "kcn-ovs-ds")}:
        raise ValueError("KCN four-workload contract differs")
    for required in (("Namespace", "kcn-system"), ("ConfigMap", "kcn-config"),
                     ("ServiceAccount", "kcn-controller")):
        if required not in ids:
            raise ValueError("missing KCN foundation: " + str(required))
    if not all(any(v["kind"] == kind for v in values) for kind in ("Role", "ClusterRole", "RoleBinding", "ClusterRoleBinding")):
        raise ValueError("missing KCN RBAC")
    for value in values:
        value["metadata"].setdefault("labels", {})["ani.io/managed-by"] = "{{ .kubernetes.cluster_name }}"
        if value["kind"] == "ConfigMap":
            data = value["data"]
            data.update(managedDevices='{{ join "," .ani.network.kcn.managedDevices }}',
                        encapNetworks='{{ join "," .ani.network.kcn.encapNetworks }}',
                        hasMultusCNI="{{ .ani.network.multus.enabled }}", intranetNetworks="ANI_INTRAnet_TEMPLATE")
        if value["kind"] not in ("Deployment", "DaemonSet"):
            continue
        pod = value["spec"]["template"]
        pod.setdefault("metadata", {}).setdefault("annotations", {})["ani.io/config-checksum"] = "{{ .ani.network.kcn.configChecksum }}"
        for container in pod["spec"].get("containers", []) + pod["spec"].get("initContainers", []):
            if container["image"] != IMAGE:
                raise ValueError("unapproved KCN image: " + container["image"])
            container["image"] = '{{ index .ani.images "' + IMAGE + '" }}'
            container["imagePullPolicy"] = "IfNotPresent"
            if "args" in container:
                args = [a for a in container["args"] if not a.startswith(("--service-cluster-ip-range=", "--default-cidr="))]
                args.append("--service-cluster-ip-range={{ .ani.network.service_cidr }}")
                if container["name"] == "kcn-controller":
                    args.append("--default-cidr={{ .ani.network.pod_cidr }}")
                container["args"] = args
            for env in container.get("env", []):
                if env["name"] in ("NODE_IPS", "OVN_DB_IPS"):
                    env["value"] = '{{ join "," .ani.node_addresses }}'
            if container in pod["spec"].get("containers", []):
                container.setdefault("env", []).extend([
                    {"name": "KUBERNETES_SERVICE_HOST", "value": "{{ .ani.network.bootstrap_address }}"},
                    {"name": "KUBERNETES_SERVICE_PORT", "value": "6443"}])
    foundation = [v for v in values if v["kind"] in ("Namespace", "CustomResourceDefinition")]
    runtime = [v for v in values if v["kind"] not in ("Namespace", "CustomResourceDefinition")]
    files = {"namespace-crds.yaml": yaml.safe_dump_all(foundation, sort_keys=False),
             "install.yaml": yaml.safe_dump_all(runtime, sort_keys=False)}
    files["install.yaml"] = files["install.yaml"].replace("intranetNetworks: ANI_INTRAnet_TEMPLATE",
        "intranetNetworks: |\n{{- range .ani.network.kcn.intranetNetworks }}\n    - {{ . }}\n{{- end }}")
    for name, text in files.items():
        (output / name).write_text(text)
    lock = {"schema": "ani.kcn.materials.v1", "sourceCommit": SOURCE,
            "sourceArchiveSha256": ARCHIVE_SHA256, "sourceImage": IMAGE, "amd64ManifestDigest": AMD64, "kustomize": version,
            "rawBundleSha256": hashlib.sha256(raw).hexdigest(), "crds": crds,
            "objects": [{"kind": kind, "name": name} for kind, name in ids],
            "files": {name: hashlib.sha256(text.encode()).hexdigest() for name, text in files.items()}}
    (output / "materials.lock.json").write_text(json.dumps(lock, indent=2) + "\n")
    print(json.dumps({"source": SOURCE, "crds": len(crds), "objects": len(ids), "output": str(output)}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--source-archive", type=pathlib.Path, required=True)
    parser.add_argument("--image-archive", type=pathlib.Path, required=True)
    parser.add_argument("--kustomize", type=pathlib.Path, required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    args = parser.parse_args()
    generate(args.source_archive, args.image_archive, args.kustomize, args.output)

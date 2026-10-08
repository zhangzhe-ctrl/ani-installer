#!/usr/bin/env python3
"""Render the fixed standalone sources on Fedora, without cluster access."""
import argparse
import copy
import hashlib
import json
import pathlib
import subprocess

import yaml

SOURCES = {"kserve": "5b033a4024429302440b72180472ae2d26b44086",
           "notebooks": "90e987bf87d3e7c900926310b00bfa16b59e41eb"}
RELEASE = "26.03-kubeflow-stage2-v1"
TEMPLATES = ["certificate", "clusterrole", "clusterrolebinding", "configmap", "deployment",
             "role", "rolebinding", "service", "serviceaccount", "webhookconfiguration",
             "clusterstoragecontainer"]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--upstream", type=pathlib.Path, required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    parser.add_argument("--helm", required=True)
    parser.add_argument("--kustomize", required=True)
    args = parser.parse_args()
    args.output.mkdir(mode=0o700)
    for name, commit in SOURCES.items():
        root = args.upstream / name
        actual = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()
        dirty = subprocess.check_output(["git", "-C", str(root), "status", "--porcelain"], text=True).strip()
        if actual != commit or dirty:
            raise ValueError("upstream must be the unchanged fixed source: " + name)

    resources, inputs = [], {}

    def render(name, command):
        raw = subprocess.check_output(command)
        (args.output / (name + ".raw.yaml")).write_bytes(raw)
        inputs[name] = hashlib.sha256(raw).hexdigest()
        return [v for v in yaml.safe_load_all(raw) if v]

    notebook_root = args.upstream / "notebooks/components/notebook-controller/config/overlays/standalone"
    resources += render("notebooks", [args.kustomize, "build", str(notebook_root)])
    charts = args.upstream / "kserve/charts"
    resources += render("kserve-crds", [args.helm, "template", "ani-kserve", str(charts / "kserve-crd"), "--namespace=kserve"])
    command = [args.helm, "template", "ani-kserve", str(charts / "kserve-resources"), "--namespace=kserve",
               "--set", "kserve.controller.deploymentMode=Standard",
               "--set", "kserve.controller.gateway.disableIngressCreation=true",
               "--set", "kserve.controller.gateway.disableIstioVirtualHost=true",
               "--set", "kserve.controller.gateway.ingressGateway.enableGatewayApi=false",
               "--set", "kserve.controller.gateway.ingressGateway.createGateway=false",
               "--set", "kserve.storage.enableModelcar=false",
               "--set", "kserve.localmodel.enabled=false"]
    for template in TEMPLATES:
        command += ["--show-only", "templates/" + template + ".yaml"]
    resources += render("kserve-controller", command)
    runtimes = render("kserve-runtime", command[:command.index("--show-only")] +
                      ["--show-only", "templates/clusterservingruntimes.yaml"])
    selected = [v for v in runtimes if v["metadata"]["name"] == "kserve-sklearnserver"]
    if len(selected) != 1:
        raise ValueError("fixed upstream sklearn Runtime is absent or ambiguous")
    resources += selected
    resources.append({"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": "kserve"}})
    inventory = []
    references = set()
    for value in resources:
        original = copy.deepcopy(value)
        kind, name = value["kind"], value["metadata"]["name"]
        if kind == "Issuer" and name == "selfsigned-issuer":
            inventory.append({"kind": kind, "name": name, "action": "exclude", "reason": "reuse the installed ani-ca ClusterIssuer"})
            continue
        if kind == "Certificate":
            value["spec"]["issuerRef"] = {"kind": "ClusterIssuer", "name": "ani-ca"}
        if kind == "ClusterStorageContainer":
            # The shipped download path is S3; prevent latent public/modelcar paths.
            value["spec"]["supportedUriFormats"] = [{"prefix": "s3://"}]
        if kind == "ConfigMap" and name == "inferenceservice-config":
            data = value["data"]
            data.pop("_example", None)
            data["explainers"] = "{}"
            storage = json.loads(data["storageInitializer"])
            storage.update(enableModelcar=False, caBundleConfigMapName="ani-model-ca",
                           caBundleVolumeMountPath="/etc/ani-model-ca")
            data["storageInitializer"] = json.dumps(storage)
            credentials = json.loads(data["credentials"])
            credentials["s3"].update(s3Endpoint="ani-rustfs-svc.ani-platform.svc.cluster.local:9000",
                                     s3UseHttps="1", s3VerifySSL="1", s3Region="us-east-1",
                                     s3UseVirtualBucket="0", s3UseAnonymousCredential="false",
                                     s3CABundle="/etc/ani-model-ca/ca.crt")
            data["credentials"] = json.dumps(credentials)
        if kind == "Deployment":
            for container in value["spec"]["template"]["spec"]["containers"]:
                budget = container.setdefault("resources", {})
                budget.setdefault("requests", {"cpu": "100m", "memory": "256Mi"})
                budget.setdefault("limits", {"cpu": "1", "memory": "1Gi"})

        def images(obj):
            if isinstance(obj, dict):
                for key, item in list(obj.items()):
                    if key == "image" and isinstance(item, str):
                        # The upstream agent/router configuration separates tag and repository.
                        if ":" not in item.rsplit("/", 1)[-1]:
                            version = obj.get("defaultVersion", obj.get("defaultImageVersion"))
                            if not version:
                                raise ValueError("unversioned image has no fixed dynamic version: " + item)
                            item += ":" + version
                        if not item.startswith(("docker.io/", "ghcr.io/", "quay.io/", "registry.k8s.io/")):
                            item = "docker.io/" + item
                        references.add(item)
                        obj[key] = "ANI_IMAGE_" + item
                    elif isinstance(item, str) and item.startswith("{"):
                        try:
                            nested = json.loads(item)
                        except ValueError:
                            continue
                        images(nested)
                        obj[key] = json.dumps(nested)
                    else:
                        images(item)
            elif isinstance(obj, list):
                for item in obj:
                    images(item)

        images(value)
        value["metadata"].setdefault("labels", {})["ani.io/kubeflow-release"] = RELEASE
        inventory.append({"kind": kind, "name": name, "namespace": value["metadata"].get("namespace"),
                          "action": "replace" if value != original else "keep"})
    final = [v for v in resources if not (v["kind"] == "Issuer" and v["metadata"]["name"] == "selfsigned-issuer")]
    raw = (json.dumps(final, indent=2) + "\n").encode()
    (args.output / "resources.json").write_bytes(raw)
    (args.output / "source-images.json").write_text(json.dumps(sorted(references), indent=2) + "\n")
    record = {"schema": "ani.kubeflow.stage2-overlay.v1", "release": RELEASE, "sources": SOURCES,
              "upstream_render_sha256": inputs, "resources_sha256": hashlib.sha256(raw).hexdigest(),
              "resource_count": len(final), "inventory": inventory, "runtime_status": "NOT_RUN"}
    (args.output / "overlay.lock.json").write_text(json.dumps(record, indent=2) + "\n")
    print(json.dumps({"resources": len(final), "images": sorted(references), "runtime_status": "NOT_RUN"}))


if __name__ == "__main__":
    main()

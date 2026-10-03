#!/usr/bin/env python3
"""Read-only ENV00 inventory. Run on the verified installerNode, never locally.

Credentials and Secret payloads are deliberately outside the collected fields.
"""
import datetime
import json
import subprocess

KUBECONFIG = "/etc/kubernetes/admin.conf"
errors = []


def command(args):
    result = subprocess.run(args, capture_output=True, text=True, timeout=45)
    if result.returncode:
        errors.append({"command": args, "exit_code": result.returncode,
                       "stderr": result.stderr[:1500]})
        return None
    return result.stdout


def get(resource):
    data = command(["sudo", "-n", "kubectl", "--kubeconfig=" + KUBECONFIG,
                    "--request-timeout=30s", "get", resource, "-A", "-o", "json"])
    return json.loads(data).get("items", []) if data else []


def identity(obj):
    metadata = obj["metadata"]
    return {key: metadata[key] for key in ("name", "namespace", "uid", "generation")
            if key in metadata}


record = {"schema_version": "1.0", "stage": "ENV00",
          "captured_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
          "kubeconfig_reference": KUBECONFIG, "cluster_writes": 0}
view = command(["sudo", "-n", "kubectl", "--kubeconfig=" + KUBECONFIG,
                "config", "view", "--minify", "-o", "json"])
if view:
    config = json.loads(view)
    record["context"] = config.get("current-context")
    record["api_server"] = config["clusters"][0]["cluster"]["server"]
record["namespaces"] = [identity(obj) for obj in get("namespaces")]
record["nodes"] = []
for obj in get("nodes"):
    status = obj["status"]
    record["nodes"].append(dict(identity(obj), addresses=status["addresses"],
                                allocatable=status["allocatable"],
                                capacity=status["capacity"],
                                conditions=status["conditions"],
                                node_info=status["nodeInfo"]))
record["storage_classes"] = [dict(identity(obj), provisioner=obj["provisioner"],
                                  parameters=obj.get("parameters", {}),
                                  reclaim_policy=obj.get("reclaimPolicy"),
                                  binding_mode=obj.get("volumeBindingMode"))
                             for obj in get("storageclasses")]
record["csi_drivers"] = [identity(obj) for obj in get("csidrivers")]
record["crds"] = [identity(obj) for obj in get("crds")]
record["pvcs"] = [dict(identity(obj), spec=obj["spec"], status=obj.get("status", {}))
                  for obj in get("pvc")]
record["workloads"] = []
for kind in ("deployments", "statefulsets", "daemonsets"):
    for obj in get(kind):
        pod = obj["spec"]["template"]["spec"]
        containers = pod.get("initContainers", []) + pod.get("containers", [])
        record["workloads"].append(dict(identity(obj), kind=obj["kind"],
                                        images=[c["image"] for c in containers],
                                        resources=[c.get("resources", {}) for c in containers],
                                        replicas=obj["spec"].get("replicas"),
                                        status=obj.get("status", {})))
record["pods"] = []
for obj in get("pods"):
    status = obj.get("status", {})
    record["pods"].append(dict(identity(obj), node=obj["spec"].get("nodeName"),
                               phase=status.get("phase"),
                               conditions=status.get("conditions", []),
                               containers=[{key: c[key] for key in
                                            ("name", "image", "imageID", "ready", "restartCount")
                                            if key in c}
                                           for c in status.get("containerStatuses", [])]))
record["services"] = [dict(identity(obj), spec={key: obj["spec"].get(key)
                                              for key in ("type", "clusterIP", "ports", "selector")})
                      for obj in get("services")]
record["network_policies"] = [dict(identity(obj), spec=obj["spec"])
                              for obj in get("networkpolicies")]
record["ceph_clusters"] = [dict(identity(obj), status=obj.get("status", {}))
                          for obj in get("cephclusters.ceph.rook.io")]
record["ceph_pools"] = [dict(identity(obj), spec=obj["spec"])
                       for obj in get("cephblockpools.ceph.rook.io")]
record["ceph_filesystems"] = [dict(identity(obj), spec=obj["spec"])
                             for obj in get("cephfilesystems.ceph.rook.io")]
record["time_sync"] = command(["timedatectl", "show", "-p", "NTPSynchronized", "-p", "TimeUSec"])
record["errors"] = errors
print(json.dumps(record, ensure_ascii=False, indent=2))
raise SystemExit(1 if errors else 0)

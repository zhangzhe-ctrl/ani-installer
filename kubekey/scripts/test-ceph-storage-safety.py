#!/usr/bin/env python3
"""Behavioural tests for R05: Ceph disk pre-flight and default StorageClass policy.

Run from the kubekey module directory:

    python3 scripts/test-ceph-storage-safety.py

The role templates are executed as shell, exactly like the installer does, with
recording fakes for every tool that would touch a disk or the cluster. No host
device is read and no cluster is contacted: the fakes answer from a fixture file
and log every call.
"""
from __future__ import annotations

import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

import yaml

KUBEKEY = Path(__file__).resolve().parent.parent
PREFLIGHT = KUBEKEY / "builtin/core/roles/ani/ceph/templates/ceph-preflight.sh"
CEPH_TASKS = KUBEKEY / "builtin/core/roles/ani/ceph/tasks/main.yaml"
DEVICE_LIST_TEMPLATE = KUBEKEY / "builtin/core/roles/ani/ceph/templates/storage-devices.txt"

# Tools the pre-flight is allowed to call: all read-only. A device-writing tool
# is intentionally absent from the fake PATH, so the script cannot even invoke
# one without failing.
READONLY_TOOLS = ["lsblk", "findmnt", "blkid", "readlink", "awk", "sort", "tr", "command", "basename"]
WRITE_TOOLS = ["wipefs", "sgdisk", "parted", "mkfs", "mkfs.ext4", "dd", "blkdiscard", "shred"]

FAKE_TOOL = '''#!/usr/bin/env python3
"""Recording fake for {tool}: answers from the fixture and never touches a device."""
import json, os, sys

fixture = json.load(open(os.environ["ANI_TEST_FIXTURE"]))
with open(os.environ["ANI_TEST_CALLS"], "a") as log:
    log.write("__TOOL__ " + " ".join(sys.argv[1:]) + "\\n")

args = sys.argv[1:]
tool = "__TOOL__"


def device_for(path):
    for declared, entry in fixture["devices"].items():
        if entry.get("resolved") == path or declared == path:
            return entry
    return None


if tool == "readlink":
    target = args[-1] if args else ""
    entry = device_for(target)
    if not entry:
        sys.exit(1)
    print(entry["resolved"])
    sys.exit(0)

if tool == "findmnt":
    # only "-rn -o SOURCE" is used
    for source in fixture.get("mount_sources", []):
        print(source)
    sys.exit(0)

if tool == "blkid":
    entry = device_for(args[-1]) if args else None
    if entry and entry.get("blkid"):
        print(entry["blkid"])
    sys.exit(0)

if tool == "lsblk":
    flags = [a for a in args if a.startswith("-")]
    # lsblk accepts bundled short options ("-ndo TYPE <dev>"), so the column
    # list is the non-flag argument that is not a device path.
    rest = [a for a in args if not a.startswith("-")]
    target = rest[-1] if rest else ""
    columns = ""
    if len(rest) >= 2 and "/" not in rest[-2]:
        columns = rest[-2]
    entry = device_for(target)
    if not entry:
        sys.exit(0)
    # Short options arrive bundled ("-ndo"), so the letters matter, not the
    # "-x" substring: -n no-header, -d device-only, -s ancestors, -r recursive.
    letters = set("".join(f.lstrip("-") for f in flags))
    if "s" in letters:
        if "TYPE" in columns:
            for kind in entry.get("ancestor_types", []):
                print(" " + kind)
        else:
            for name in entry.get("ancestors", []):
                print(" " + name)
        sys.exit(0)
    if "d" in letters:
        if columns == "TYPE":
            print(" " + entry["type"])
        elif columns == "NAME":
            print(" " + entry["name"])
        elif columns == "FSTYPE":
            print(" " + entry.get("fstype", ""))
        elif "MOUNTPOINT" in columns:
            print(" {name} {size} {kind} {fstype} {mountpoint}".format(
                name=entry["name"], size=entry.get("size", "50G"), kind=entry["type"],
                fstype=entry.get("fstype", ""), mountpoint=entry.get("mountpoint", "")))
        else:
            print(" " + entry["name"])
        sys.exit(0)
    # recursive listing (-r)
    if "MOUNTPOINT" in columns:
        for name, mount in entry.get("children", []):
            print(" {name} {mount}".format(name=name, mount=mount))
        sys.exit(0)
    for name in entry.get("subtree", []):
        print(" " + name)
    sys.exit(0)

sys.exit(2)
'''


class Result:
    def __init__(self) -> None:
        self.passed: list[str] = []
        self.failed: list[str] = []

    def check(self, test_id: str, condition: bool, detail: str) -> bool:
        (self.passed if condition else self.failed).append(f"{test_id}: {detail}")
        print(f"  {'PASS' if condition else 'FAIL'}  {test_id} — {detail}")
        return condition


class Sandbox:
    def __init__(self, fixture: dict) -> None:
        self.dir = Path(tempfile.mkdtemp(prefix="r05-storage-"))
        self.bin = self.dir / "bin"
        self.bin.mkdir()
        self.calls = self.dir / "calls.log"
        self.calls.touch()
        self.fixture = self.dir / "fixture.json"
        self.fixture.write_text(json.dumps(fixture), encoding="utf-8")
        for tool in READONLY_TOOLS:
            path = self.bin / tool
            if tool in ("awk", "sort", "tr", "basename", "command"):
                continue  # real coreutils/shell builtins are fine
            path.write_text(FAKE_TOOL.replace("__TOOL__", tool), encoding="utf-8")
            path.chmod(0o700)
        # kubectl is only used by the default-class task.
        kubectl = self.bin / "kubectl"
        kubectl.write_text('''#!/usr/bin/env python3
import json, os, sys
with open(os.environ["ANI_TEST_CALLS"], "a") as log:
    log.write("kubectl " + " ".join(sys.argv[1:]) + "\\n")
fixture = json.load(open(os.environ["ANI_TEST_FIXTURE"]))
args = sys.argv[1:]
if args[:2] == ["get", "storageclass"] and "jsonpath" in " ".join(args):
    for name, default in fixture.get("storageclasses", []):
        print(f"{name} {default}")
    sys.exit(0)
if "patch" in args:
    sys.exit(0)
sys.exit(0)
''', encoding="utf-8")
        kubectl.chmod(0o700)

    def env(self, **extra: str) -> dict[str, str]:
        env = dict(os.environ)
        env["PATH"] = f"{self.bin}:{env.get('PATH', '/usr/bin:/bin')}"
        env["ANI_TEST_FIXTURE"] = str(self.fixture)
        env["ANI_TEST_CALLS"] = str(self.calls)
        env.update(extra)
        return env

    def run(self, args: list[str], **extra: str) -> tuple[int, str, str]:
        proc = subprocess.run(args, env=self.env(**extra), stdout=subprocess.PIPE,
                              stderr=subprocess.PIPE, text=True, timeout=120)
        return proc.returncode, proc.stdout, proc.stderr

    def run_shell(self, script: str, **extra: str) -> tuple[int, str, str]:
        return self.run(["/bin/bash", "-c", script], **extra)

    def call_lines(self) -> list[str]:
        return [l for l in self.calls.read_text(encoding="utf-8").splitlines() if l]

    def close(self) -> None:
        shutil.rmtree(self.dir, ignore_errors=True)


def clean_device(resolved: str, name: str) -> dict:
    return {"resolved": resolved, "name": name, "type": "disk", "size": "50G", "fstype": "",
            "mountpoint": "", "ancestors": [name], "ancestor_types": ["disk"],
            "subtree": [name], "children": [], "blkid": ""}


# ---------------------------------------------------------------------------
# T-R05-02: a declared device that is not a blank data disk must fail the
# pre-flight, and the check must never write to a device.
# ---------------------------------------------------------------------------
def test_preflight_rejections(res: Result, work: Path) -> None:
    print("T-R05-02 缺盘/根盘/子分区/已挂载/有签名/重复设备都必须前置失败")

    cases = {
        "missing device": {
            "devices": {},
            "mount_sources": ["/dev/sda2"],
        },
        "device is a partition": {
            "devices": {"/dev/disk/by-id/x": {**clean_device("/dev/sdb1", "sdb1"), "type": "part"}},
            "mount_sources": ["/dev/sda2"],
        },
        "device carries a signature": {
            "devices": {"/dev/disk/by-id/x": {**clean_device("/dev/sdb", "sdb"),
                                              "blkid": "/dev/sdb: TYPE=\"ext4\""}},
            "mount_sources": ["/dev/sda2"],
        },
        "device is mounted": {
            "devices": {"/dev/disk/by-id/x": {**clean_device("/dev/sdb", "sdb"), "mountpoint": "/data"}},
            "mount_sources": ["/dev/sdb", "/dev/sda2"],
        },
        "device is the system disk": {
            "devices": {"/dev/disk/by-id/x": {**clean_device("/dev/sda", "sda"),
                                              "subtree": ["sda", "sda1", "sda2"],
                                              "children": [["sda1", "/boot"], ["sda2", "/"]]}},
            "mount_sources": ["/dev/sda2"],
        },
        "device has a mounted child": {
            "devices": {"/dev/disk/by-id/x": {**clean_device("/dev/sdb", "sdb"),
                                              "subtree": ["sdb", "sdb1"],
                                              "children": [["sdb1", "/var/lib"]]}},
            "mount_sources": ["/dev/sdb1"],
        },
        "device sits on LVM": {
            "devices": {"/dev/disk/by-id/x": {**clean_device("/dev/dm-3", "dm-3"),
                                              "ancestors": ["dm-3", "vg-root", "sda3"],
                                              "ancestor_types": ["lvm", "lvm", "part"]}},
            "mount_sources": ["/dev/mapper/vg-root"],
        },
    }
    for name, fixture in cases.items():
        box = Sandbox(fixture)
        try:
            rc, out, err = box.run(["bash", str(PREFLIGHT), "node1", "/dev/disk/by-id/x"])
            res.check(f"T-R05-02a-{name}", rc != 0 and "FAILED" in err,
                      f"{name}: 非零且报错（rc={rc}）")
        finally:
            box.close()

    # A bare kernel name is not a stable identity even when the disk is blank.
    box = Sandbox({"devices": {"/dev/sdb": clean_device("/dev/sdb", "sdb")},
                   "mount_sources": ["/dev/sda2"]})
    try:
        rc, out, err = box.run(["bash", str(PREFLIGHT), "node1", "/dev/sdb"])
        res.check("T-R05-02b", rc != 0 and "stable identity" in err,
                  f"裸设备名被拒绝（rc={rc}）")
    finally:
        box.close()

    # The same device twice in one node's declaration.
    box = Sandbox({"devices": {"/dev/disk/by-id/x": clean_device("/dev/sdb", "sdb")},
                   "mount_sources": ["/dev/sda2"]})
    try:
        rc, out, err = box.run(["bash", str(PREFLIGHT), "node1",
                                "/dev/disk/by-id/x", "/dev/disk/by-id/x"])
        res.check("T-R05-02c", rc != 0 and "more than once" in err,
                  f"重复声明被拒绝（rc={rc}）")
    finally:
        box.close()

    # A blank disk passes, and nothing is written anywhere.
    box = Sandbox({"devices": {"/dev/disk/by-id/x": clean_device("/dev/sdb", "sdb")},
                   "mount_sources": ["/dev/sda2"]})
    try:
        rc, out, err = box.run(["bash", str(PREFLIGHT), "node1", "/dev/disk/by-id/x"])
        res.check("T-R05-02d", rc == 0 and "pre-flight OK" in out,
                  f"空白盘通过（rc={rc}）")
        calls = box.call_lines()
        writes = [c for c in calls if c.split()[0] in WRITE_TOOLS]
        res.check("T-R05-02e", not writes, f"预检没有调用任何写盘工具（{writes}）")
        res.check("T-R05-02f", any(c.startswith("blkid") for c in calls),
                  "预检确实检查了设备签名（调用过 blkid）")
    finally:
        box.close()

    # Reading the devices from the rendered per-node list is how the role runs it.
    box = Sandbox({"devices": {"/dev/disk/by-id/x": clean_device("/dev/sdb", "sdb")},
                   "mount_sources": ["/dev/sda2"]})
    devlist = work / "storage-devices.txt"
    devlist.write_text("node1 /dev/disk/by-id/x\nnode2 /dev/disk/by-id/y\n", encoding="utf-8")
    try:
        rc, out, err = box.run(["bash", str(PREFLIGHT), "node1"],
                               ANI_CEPH_DEVICE_LIST=str(devlist))
        res.check("T-R05-02g", rc == 0, f"按节点读取渲染清单并通过（rc={rc}）")
        rc, out, err = box.run(["bash", str(PREFLIGHT), "node3"],
                               ANI_CEPH_DEVICE_LIST=str(devlist))
        res.check("T-R05-02h", rc != 0 and "no line for this node" in err,
                  f"清单里没有该节点时失败（rc={rc}）")
    finally:
        box.close()


# ---------------------------------------------------------------------------
# T-R05-04: the default StorageClass policy follows the site flag and never
# clears another component's marker.
# ---------------------------------------------------------------------------
def default_class_block(template_value: str) -> str:
    doc = yaml.safe_load(CEPH_TASKS.read_text(encoding="utf-8"))
    block = ""
    for task in doc:
        if isinstance(task, dict) and task.get("name") == "ANI Ceph | Enforce the default StorageClass policy":
            block = task["command"]
    if not block:
        raise AssertionError("the default StorageClass task is missing")
    return block.replace("{{ .ani.storage.makeDefaultStorageClass }}", template_value)


def test_default_class_policy(res: Result) -> None:
    print("T-R05-04 默认类策略：关闭时不动任何标记；冲突时失败且不撤销别人的标记")

    box = Sandbox({"storageclasses": [("ani-block", ""), ("other-sc", "true")]})
    try:
        rc, out, err = box.run_shell(default_class_block("false"))
        res.check("T-R05-04a", rc == 0 and "untouched" in out,
                  f"flag=false 时直接返回且不动标记（rc={rc}）")
        res.check("T-R05-04b", not [c for c in box.call_lines() if "patch" in c],
                  "flag=false 时没有任何 patch 调用")
    finally:
        box.close()

    box = Sandbox({"storageclasses": [("ani-block", ""), ("other-sc", "true")]})
    try:
        rc, out, err = box.run_shell(default_class_block("true"))
        res.check("T-R05-04c", rc != 0 and "other-sc" in err,
                  f"已有其它默认类时失败并指名冲突（rc={rc}）")
        res.check("T-R05-04d", not [c for c in box.call_lines() if "patch" in c],
                  "冲突时不执行任何 patch（不撤销别人的标记）")
    finally:
        box.close()

    box = Sandbox({"storageclasses": [("ani-block", ""), ("third", "")]})
    try:
        rc, out, err = box.run_shell(default_class_block("true"))
        patches = [c for c in box.call_lines() if "patch" in c]
        res.check("T-R05-04e", rc == 0 and len(patches) == 1 and "ani-block" in patches[0],
                  f"无冲突时只把 ani-block 标为默认（rc={rc}, patches={len(patches)}）")
    finally:
        box.close()

    box = Sandbox({"storageclasses": [("ani-block", "true")]})
    try:
        rc, out, err = box.run_shell(default_class_block("true"))
        res.check("T-R05-04f", rc == 0,
                  f"ani-block 已是默认时幂等成功（rc={rc}）")
    finally:
        box.close()


# ---------------------------------------------------------------------------
# T-R05-01 (rendered side): the shipped device list carries only what the site
# declared, and the CephCluster template reads it instead of scanning.
# ---------------------------------------------------------------------------
def test_declared_list_and_cluster_template(res: Result) -> None:
    print("T-R05-01 设备清单与 CephCluster 只来自站点声明，不再扫描")
    cluster = (KUBEKEY / "builtin/core/roles/ani/ceph/templates/cluster.yaml").read_text(encoding="utf-8")
    for forbidden in ("deviceFilter", "useAllNodes: true", "useAllDevices: true"):
        res.check(f"T-R05-01a-{forbidden}", forbidden not in cluster,
                  f"CephCluster 模板不含 {forbidden}")
    res.check("T-R05-01b", ".ani.storage.nodes" in cluster and "useAllNodes: false" in cluster,
              "CephCluster 模板使用站点声明的节点设备且关闭自动发现")
    res.check("T-R05-01c", ".ani.storage.nodes" in DEVICE_LIST_TEMPLATE.read_text(encoding="utf-8"),
              "设备清单模板来自站点声明")
    tasks = CEPH_TASKS.read_text(encoding="utf-8")
    res.check("T-R05-01d", "ceph-preflight.sh {{ .item }}" in tasks,
              "每个节点在 Rook 拿到磁盘之前先跑预检")
    preflight = PREFLIGHT.read_text(encoding="utf-8")
    for tool in WRITE_TOOLS:
        res.check(f"T-R05-01e-{tool}", tool not in preflight,
                  f"预检脚本不含写盘工具 {tool}")


def main() -> int:
    for required in (PREFLIGHT, CEPH_TASKS, DEVICE_LIST_TEMPLATE):
        if not required.exists():
            print(f"missing required file: {required}", file=sys.stderr)
            return 2
    res = Result()
    with tempfile.TemporaryDirectory(prefix="r05-work-") as tmp:
        work = Path(tmp)
        test_declared_list_and_cluster_template(res)
        test_preflight_rejections(res, work)
        test_default_class_policy(res)

    print()
    print(f"cases passed: {len(res.passed)}")
    print(f"cases failed: {len(res.failed)}")
    for item in res.failed:
        print(f"  FAILED {item}")
    if res.failed:
        return 1
    print("ALL R05 STORAGE SAFETY TESTS PASSED")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

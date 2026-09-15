#!/usr/bin/env python3
"""Remove orphan auto-created KCN pod ports before an Envoy rollout."""

import json
import re
import subprocess
import sys

NODE_ADDRESSES = '{{ .ani.node_addresses | join "," }}'.split(",")
OVN_DB = ",".join(f"tcp:[{address}]:6641" for address in NODE_ADDRESSES)
LSP_NAME_RE = re.compile(r"^[^.]+\..*auto-.*-vnic-.*$")


def run(command, *, capture=True):
    result = subprocess.run(
        command,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.PIPE,
        text=True,
    )
    if result.returncode != 0:
        sys.stderr.write(result.stderr or "")
        raise SystemExit(result.returncode)
    return result.stdout if capture else ""


def run_in_ovn_central(*command):
    return run(
        [
            "kubectl",
            "exec",
            "-n",
            "kcn-system",
            "deploy/kcn-ovn-central",
            "-c",
            "ovn-central",
            "--",
            *command,
        ]
    )


def main():
    vnic_list = json.loads(
        run(["kubectl", "get", "vnics.networking.kubercloud.com", "-A", "-o", "json"])
    )
    current_vnics = {
        f"{item['metadata']['namespace']}.{item['metadata']['name']}"
        for item in vnic_list["items"]
    }
    query = json.dumps(
        [
            "OVN_Northbound",
            {
                "op": "select",
                "table": "Logical_Switch_Port",
                "where": [],
                "columns": ["name", "addresses"],
            },
        ]
    )
    lsp_result = json.loads(run_in_ovn_central("ovsdb-client", "query", OVN_DB, query))
    stale = [
        row["name"]
        for row in lsp_result[0]["rows"]
        if LSP_NAME_RE.match(row["name"]) and row["name"] not in current_vnics
    ]
    for name in stale:
        run_in_ovn_central("ovn-nbctl", f"--db={OVN_DB}", "lsp-del", name)
    print(f"ANI-KCN-CLEANUP removed {len(stale)} orphan pod logical switch port(s)")
    if stale:
        for name in stale:
            print(f"removed {name}")


if __name__ == "__main__":
    main()

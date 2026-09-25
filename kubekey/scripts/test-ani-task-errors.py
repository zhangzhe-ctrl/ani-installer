#!/usr/bin/env python3
"""Behavioural tests for R03: a failing command in a role task must stop the task.

Run from the kubekey module directory:

    python3 scripts/test-ani-task-errors.py

What this actually executes
---------------------------
KubeKey runs a task's ``command`` through the remote shell as ``<shell> -c "<text>"``
(pkg/connector/ssh_connector.go resolves ``$SHELL`` and falls back to /bin/bash),
and pkg/executor/block_executor.go returns the first task error, which aborts the
playbook. These tests therefore:

* take the real command text out of the ANI role task files,
* run it with ``/bin/bash -c`` like the connector does,
* put recording fakes for the external commands on PATH so nothing touches the
  host filesystem or a cluster, and
* inject a failure at a chosen command, then assert the block returns non-zero
  and that no later command in the block ran.

The fakes never reach the network and never write outside their temp directory.
"""
from __future__ import annotations

import glob
import os
import shutil
import subprocess
import sys
import re
import tempfile
import textwrap
from pathlib import Path

import yaml

KUBEKEY = Path(__file__).resolve().parent.parent
ROLES = KUBEKEY / "builtin" / "core" / "roles" / "ani"
FAILURE_CODE = 42

# External commands the ANI task blocks invoke. Each name gets the same
# recording fake, so a block can never reach the real binary.
FAKE_COMMANDS = [
    "kubectl", "install", "chown", "chmod", "sh", "bash", "sysctl", "systemctl",
    "head", "tr", "cut", "base64", "cat", "sed", "awk", "grep", "wc", "sort",
    "date", "sleep", "seq", "tee", "curl", "cp", "mv", "rm", "mkdir", "openssl",
]

# Multi-command blocks that deliberately carry their own strict shell: the whole
# block is a single `sh -c '...'` invocation whose inner script already sets -e.
# (Recorded so the completeness check below stays honest instead of skipping.)
ALLOWED_WITHOUT_BLOCK_LEVEL_SET_E = {
    ("opensearch", "ANI OpenSearch | Raise vm.max_map_count on every schedulable node"):
        "single `sh -c` command whose inner script sets -e and exits 1 on a bad value",
}

FAKE_DISPATCHER = """#!/bin/bash
# Recording fake for every external command used by the ANI role task blocks.
# Only shell builtins are used here: the fake must not call another faked
# command (that would swallow the injected failure and break the counting).
name="$(basename "$0")"
{ printf '%s\t%s\n' "$name" "$*"; } >> "$ANI_TEST_LOG"

lines=()
while IFS= read -r line; do lines+=("$line"); done < "$ANI_TEST_LOG"
invocation="${#lines[@]}"

if [ -n "${ANI_TEST_FAIL_FIRST:-}" ] && [ "$invocation" = "1" ]; then
  exit "${ANI_TEST_FAIL_CODE:-42}"
fi

if [ -n "${ANI_TEST_FAIL_INDEX:-}" ] && [ "$name" = "${ANI_TEST_FAIL_NAME:-kubectl}" ]; then
  matched=0
  for line in "${lines[@]}"; do
    if [[ "$line" == "${ANI_TEST_FAIL_NAME:-kubectl}"$'\t'* ]]; then
      matched=$((matched + 1))
    fi
  done
  if [ "$matched" = "${ANI_TEST_FAIL_INDEX}" ]; then
    exit "${ANI_TEST_FAIL_CODE:-42}"
  fi
fi

if [ -n "${ANI_TEST_FAIL_TOKENS:-}" ]; then
  ok=1
  IFS='|' read -ra want <<< "${ANI_TEST_FAIL_TOKENS}"
  for w in "${want[@]}"; do
    case "$name $*" in *"$w"*) ;; *) ok=0 ;; esac
  done
  if [ "$ok" = "1" ]; then exit "${ANI_TEST_FAIL_CODE:-42}"; fi
fi

if [ -n "${ANI_TEST_FAIL_SUBSTR:-}" ]; then
  case "$name $*" in
    *"${ANI_TEST_FAIL_SUBSTR}"*) exit "${ANI_TEST_FAIL_CODE:-42}" ;;
  esac
fi

# Optional canned answer so a read-only query under test does not simply fail
# for lack of a cluster (used by the StorageClass listing assertion).
if [ -n "${ANI_TEST_STORAGECLASSES:-}" ]; then
  case "$name $*" in
    *"get storageclass"*) printf '%s\n' "$ANI_TEST_STORAGECLASSES"; exit 0 ;;
  esac
fi
exit 0
"""


class Sandbox:
    """Temp directory with the fakes on PATH and a call log."""

    def __init__(self) -> None:
        self.dir = Path(tempfile.mkdtemp(prefix="r03-tasks-"))
        self.bin = self.dir / "bin"
        self.bin.mkdir()
        self.log = self.dir / "calls.log"
        self.log.touch()
        for name in FAKE_COMMANDS:
            target = self.bin / name
            target.write_text(FAKE_DISPATCHER, encoding="utf-8")
            target.chmod(0o700)

    def env(self, **extra: str) -> dict[str, str]:
        env = dict(os.environ)
        env["PATH"] = f"{self.bin}:{env.get('PATH', '/usr/bin:/bin')}"
        env["ANI_TEST_LOG"] = str(self.log)
        env["ANI_TEST_FAIL_CODE"] = str(FAILURE_CODE)
        for key in ("ANI_TEST_FAIL_FIRST", "ANI_TEST_FAIL_INDEX", "ANI_TEST_FAIL_NAME",
                    "ANI_TEST_FAIL_SUBSTR", "ANI_TEST_FAIL_TOKENS", "ANI_TEST_STORAGECLASSES"):
            env.pop(key, None)
        for key, value in extra.items():
            env[key] = value
        return env

    def run(self, block: str, **extra: str) -> tuple[int, str, str]:
        """Run one task's command text the way the connector does."""
        env = self.env(**extra)
        proc = subprocess.run(["/bin/bash", "-c", block], cwd=str(KUBEKEY), env=env,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
                              timeout=120)
        return proc.returncode, proc.stdout, proc.stderr

    def calls(self) -> list[tuple[str, str]]:
        rows = []
        for line in self.log.read_text(encoding="utf-8").splitlines():
            name, _, args = line.partition("\t")
            rows.append((name, args))
        return rows

    def reset(self) -> None:
        self.log.write_text("", encoding="utf-8")

    def close(self) -> None:
        shutil.rmtree(self.dir, ignore_errors=True)


class Result:
    def __init__(self) -> None:
        self.passed: list[str] = []
        self.failed: list[str] = []

    def check(self, test_id: str, condition: bool, detail: str) -> bool:
        (self.passed if condition else self.failed).append(f"{test_id}: {detail}")
        print(f"  {'PASS' if condition else 'FAIL'}  {test_id} — {detail}")
        return condition


def load_tasks(path: Path) -> list[dict]:
    doc = yaml.safe_load(path.read_text(encoding="utf-8"))
    return [t for t in doc if isinstance(t, dict)] if isinstance(doc, list) else []


def role_task(role: str, name: str) -> str:
    for task in load_tasks(ROLES / role / "tasks" / "main.yaml"):
        if task.get("name") == name:
            return task["command"]
    raise KeyError(f"{role}: {name}")


def head_task(role: str, name: str) -> str:
    """The pre-R03 text of a task, straight from git HEAD (control fixture)."""
    rel = f"kubekey/builtin/core/roles/ani/{role}/tasks/main.yaml"
    out = subprocess.run(["git", "-C", str(KUBEKEY.parent), "show", f"HEAD:{rel}"],
                         stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, check=True).stdout
    for task in [t for t in yaml.safe_load(out) if isinstance(t, dict)]:
        if task.get("name") == name:
            return task["command"]
    raise KeyError(f"HEAD {role}: {name}")


def commands_of(block: str) -> list[str]:
    """Command lines of a block, with shell line-continuations folded."""
    joined = block.replace("\\\n", " ")
    return [l.strip() for l in joined.split("\n")
            if l.strip() and not l.strip().startswith("#")]


WRITE_KUBECTL = ("apply", "create", "delete", "patch", "replace", "scale",
                "annotate", "label", "rollout restart", "set ", "edit")
WRITE_BINARIES = ("install", "chown", "chmod", "cp", "mv", "rm", "mkdir", "tee", "sysctl")


SET_LINE = re.compile(r"^set\s+-")


def real_command_lines(block: str) -> list[str]:
    """Command lines of a block, ignoring the leading `set -...` option lines."""
    return [l for l in commands_of(block) if not SET_LINE.match(l)]


def command_tokens(block: str) -> list[str]:
    """Stable tokens identifying the block's first *external* command.

    A block can start with control flow ("if [ ... ]; then") or a builtin
    ("echo"), which the fake PATH cannot intercept; injecting a failure there
    would prove nothing. So the target is the first token that names one of the
    faked external commands, wherever it appears (an assignment's command
    substitution counts), plus the next stable tokens.
    """
    names = set(FAKE_COMMANDS)
    for line in real_command_lines(block):
        # Stop at the first template placeholder: an argument that comes from
        # the site config cannot be part of a literal injection target.
        line = line.split("{{", 1)[0]
        tokens = [t.strip("'\"") for t in re.split(r"[\s|;&()=]+", line) if t.strip("'\"")]
        for index, token in enumerate(tokens):
            if token not in names:
                continue
            picked = [token]
            for candidate in tokens[index + 1:]:
                if "$" in candidate or candidate.startswith("{{"):
                    break
                picked.append(candidate)
                if len(picked) == 3:
                    break
            return picked
    return []


def tokens_match(target: list[str], row: tuple[str, str]) -> bool:
    text = f"{row[0]} {row[1]}"
    return all(token in text for token in target)


WRITE_KUBECTL = ("apply", "create", "delete", "patch", "replace", "scale",
                "annotate", "label", "rollout restart", "set ", "edit")
WRITE_BINARIES = ("install", "chown", "chmod", "cp", "mv", "rm", "mkdir", "tee", "sysctl")


def is_write(row: tuple[str, str]) -> bool:
    """True when the recorded call changes the host or the cluster."""
    name, args = row
    if name == "kubectl":
        return any(f" {verb}" in f" {args}" for verb in WRITE_KUBECTL)
    return name in WRITE_BINARIES


def applied(rows: list[tuple[str, str]], path_fragment: str) -> bool:
    return any(name == "kubectl" and "apply" in args and path_fragment in args
               for name, args in rows)


# ---------------------------------------------------------------------------
# T-R03-01: the Ceph apply sequence stops at the first failing apply
# ---------------------------------------------------------------------------
def test_ceph_apply_sequence(box: Sandbox, res: Result) -> None:
    print("T-R03-01 Ceph apply 序列：第一/中间/最后一条 apply 失败都必须在失败点停住")
    tasks = load_tasks(ROLES / "ceph" / "tasks" / "main.yaml")
    fragments = ["00-crds.yaml", "01-common.yaml", "02-csi-operator.yaml"]
    apply_tasks = [t for t in tasks
                   if any(f in str(t.get("command", "")) for f in fragments)]
    names = [t["name"] for t in apply_tasks]
    res.check("T-R03-01a", len(apply_tasks) == 3 and len(set(names)) == 3,
              f"Ceph 的三次 apply 已拆成三个独立 task（实际 {len(apply_tasks)} 个：{names}）")

    for fail_at in (1, 2, 3):
        box.reset()
        rc = 0
        injected = fragments[fail_at - 1]
        # Mirror pkg/executor/block_executor.go: the first failing task returns
        # and later tasks in the role never run.
        for task in apply_tasks:
            rc, _out, _err = box.run(task["command"], ANI_TEST_FAIL_SUBSTR=injected)
            if rc != 0:
                break
        rows = box.calls()
        later_ran = [f for f, task in zip(fragments, apply_tasks)
                     if fragments.index(f) + 1 > fail_at and applied(rows, f)]
        res.check(f"T-R03-01b{fail_at}", rc == FAILURE_CODE,
                  f"第 {fail_at} 条 apply 失败时整体返回 {FAILURE_CODE}（实际 {rc}）")
        res.check(f"T-R03-01c{fail_at}", not later_ran,
                  f"第 {fail_at} 条失败后没有更晚的 apply 被执行（实际运行了 {later_ran}）")
        res.check(f"T-R03-01d{fail_at}", len([n for n, a in rows if "apply" in a]) == fail_at,
                  f"只发生了 {fail_at} 次 apply（成功 {fail_at - 1} 次 + 失败 1 次）")

    # Control: the pre-R03 single task ran all three applies and still reported
    # the LAST command's status, hiding the earlier failure.
    box.reset()
    control = head_task("ceph", "ANI Ceph | Apply CRDs, common and csi-operator")
    rc, _out, _err = box.run(control, ANI_TEST_FAIL_SUBSTR="00-crds.yaml")
    rows = box.calls()
    control_later = [f for f in fragments[1:] if applied(rows, f)]
    res.check("T-R03-01e", rc == 0 and control_later == fragments[1:],
              f"反向对照：R03 之前的单 task 写法在第一条 apply 失败后仍执行了后面的 apply 且返回 0"
              f"（rc={rc}，后续 {control_later}）")


# ---------------------------------------------------------------------------
# T-R03-02: a failing producer in a pipeline fails the whole task
# ---------------------------------------------------------------------------
def test_pipeline_failure(box: Sandbox, res: Result) -> None:
    print("T-R03-02 管道前段失败、后段成功，整体必须非零")
    block = role_task("envoy", "ANI Envoy | Write controller configuration")
    box.reset()
    rc, _out, err = box.run(block, ANI_TEST_FAIL_SUBSTR="create configmap")
    rows = box.calls()
    res.check("T-R03-02a", rc != 0,
              f"kubectl create 失败时 task 非零（rc={rc}）")
    res.check("T-R03-02b", any("apply" in args for _n, args in rows),
              "后段（消费端）确实启动了：这正是靠 pipefail 才能把前段失败暴露出来的场景")

    box.reset()
    control = head_task("envoy", "ANI Envoy | Write controller configuration")
    rc, _out, _err = box.run(control, ANI_TEST_FAIL_SUBSTR="create configmap")
    rows = box.calls()
    res.check("T-R03-02c", rc == 0 and any("apply" in args for _n, args in rows),
              f"反向对照：R03 之前的单行管道写法掩盖了前段失败并返回 0（rc={rc}，调用 {rows}）")


# ---------------------------------------------------------------------------
# T-R03-03: no silent skipping when a required step fails, and nothing extra runs.
#
# R05 changed this task's contract: it used to make ani-block the default and
# clear every other class's default marker best-effort (the only task-level
# `|| true` in the ANI roles). It now does exactly what storage.
# makeDefaultStorageClass asks for and never touches another class, so the
# assertions below check the new contract instead of the removed tolerance.
def test_diagnostic_tolerance(box: Sandbox, res: Result) -> None:
    print("T-R03-03 必需步骤失败不得静默跳过；无故障时只执行必需的写操作")
    block = role_task("ceph", "ANI Ceph | Enforce the default StorageClass policy")
    on = block.replace("{{ .ani.storage.makeDefaultStorageClass }}", "true")
    off = block.replace("{{ .ani.storage.makeDefaultStorageClass }}", "false")

    # (a) the required listing fails -> the task must fail, not continue blindly
    box.reset()
    rc, _out, err = box.run(on, ANI_TEST_FAIL_SUBSTR="get storageclass")
    res.check("T-R03-03a", rc != 0,
              f"列出 StorageClass 失败时 task 非零，而不是当作没有默认类继续（rc={rc}）")

    # (b) flag off: the task is a no-op, with no write of any kind
    box.reset()
    rc, out, _err = box.run(off, ANI_TEST_STORAGECLASSES="ani-block other-sc")
    writes = ["%s %s" % row for row in box.calls()
              if any(verb in row[1] for verb in ("patch", "apply", "delete"))]
    res.check("T-R03-03b", rc == 0 and not writes,
              f"makeDefaultStorageClass=false 时任务成功且不写任何东西（rc={rc}, writes={writes}）")

    # (c) flag on, no conflict: exactly the one required patch runs
    box.reset()
    rc, _out, _err = box.run(on, ANI_TEST_STORAGECLASSES="ani-block third")
    patched = ["%s %s" % row for row in box.calls() if "patch" in row[1]]
    res.check("T-R03-03c", rc == 0 and len(patched) == 1 and "ani-block" in patched[0],
              f"无故障时只执行必需的那一次 patch（rc={rc}, patches={patched}）")
    res.check("T-R03-03d", not any("third" in c for c in patched),
              "不碰其它 StorageClass 的标记")


# ---------------------------------------------------------------------------
# Coverage: every multi-command block stops at its first failure
# ---------------------------------------------------------------------------
def test_every_multicommand_block_stops(box: Sandbox, res: Result) -> None:
    print("覆盖：每个多命令块在首个命令失败时都必须非零且不继续")
    total = 0
    executed = 0
    skipped: list[str] = []
    for path in sorted(glob.glob(str(ROLES / "*" / "tasks" / "*.yaml"))):
        role = Path(path).parent.parent.name
        for task in load_tasks(Path(path)):
            command = task.get("command")
            if not isinstance(command, str):
                continue
            lines = commands_of(command)
            if len(lines) <= 1:
                continue
            name = str(task.get("name", ""))
            if (role, name) in ALLOWED_WITHOUT_BLOCK_LEVEL_SET_E:
                res.check(f"R03-coverage-allow-{role}", lines[0].startswith("sh -c"),
                          f"{role} | {name}：允许列表条目确实是单条 sh -c（{lines[0][:24]}…）")
                continue
            total += 1
            target = command_tokens(command)
            if not target:
                res.check(f"R03-coverage-{role}:{name}", False,
                          "无法从块中确定首命令的注入 token")
                continue
            box.reset()
            rc, _out, err = box.run(command, ANI_TEST_FAIL_TOKENS="|".join(target))
            rows = box.calls()
            # The consumer of a pipeline always starts; there the exit status is
            # the property under test. A sequential first command must also be
            # the last thing that ran.
            sequential = "|" not in real_command_lines(command)[0]
            if not rows:
                # No command reached the sandbox: either a template placeholder
                # gates the whole block (this harness does not render templates)
                # or the block needs host paths this sandbox does not provide.
                # Counted and reported rather than silently treated as covered;
                # the executed total is asserted below.
                skipped.append(f"{role}: {name}")
                continue
            leaked = [row for row in rows if is_write(row) and not tokens_match(target, row)] if sequential else []
            if rc == 0 or leaked:
                res.check(f"R03-coverage-{role}:{name}", False,
                          f"注入 {target} 失败后 rc={rc}，且后续还有写操作 {leaked}；stderr={err.strip()[:100]}")
            else:
                executed += 1
                res.check(f"R03-coverage-{role}:{name}", True,
                          f"首命令 {target} 失败即暴露（rc={rc}，块内命令 {len(real_command_lines(command))} 条）")
    res.check("R03-coverage-total", executed >= 50 and executed + len(skipped) == total,
              f"覆盖 {executed}/{total} 个多命令块真正执行（未执行 {len(skipped)} 个：{skipped}）")


def main() -> int:
    if not ROLES.is_dir():
        print(f"role directory not found: {ROLES}", file=sys.stderr)
        return 2
    res = Result()
    box = Sandbox()
    try:
        test_ceph_apply_sequence(box, res)
        test_pipeline_failure(box, res)
        test_diagnostic_tolerance(box, res)
        test_every_multicommand_block_stops(box, res)
    finally:
        box.close()

    print()
    print(f"cases passed: {len(res.passed)}")
    print(f"cases failed: {len(res.failed)}")
    for item in res.failed:
        print(f"  FAILED {item}")
    if res.failed:
        return 1
    print("ALL R03 TASK-ERROR TESTS PASSED")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

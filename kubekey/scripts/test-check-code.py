#!/usr/bin/env python3
"""Behavioural tests for R04: the code gate and the release identity check.

Run from the kubekey module directory:

    python3 scripts/test-check-code.py

These tests never build a release in place and never touch the network. Faults
are injected into a throwaway copy of the module (about 25 MB, no .git) so the
working tree keeps exactly the content the gate is being asked to accept.
"""
from __future__ import annotations

import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

import yaml

KUBEKEY = Path(__file__).resolve().parent.parent
REPO = KUBEKEY.parent
CHECK = KUBEKEY / "scripts" / "check-code.sh"
BUILD = KUBEKEY / "scripts" / "build-code.sh"
WORKFLOW = REPO / ".github" / "workflows" / "ani-check.yaml"
# The gate now builds, vets and tests BOTH tree shapes (untagged and
# -tags builtin), so a nested full run needs room; a fault is still caught at
# the first failing step.
GATE_TIMEOUT = 420


class Result:
    def __init__(self) -> None:
        self.passed: list[str] = []
        self.failed: list[str] = []

    def check(self, test_id: str, condition: bool, detail: str) -> bool:
        (self.passed if condition else self.failed).append(f"{test_id}: {detail}")
        print(f"  {'PASS' if condition else 'FAIL'}  {test_id} — {detail}")
        return condition


def copy_module(dst: Path) -> Path:
    """Copy the module without VCS metadata or build output (fast, faithful)."""
    dst.mkdir(parents=True, exist_ok=True)
    tar = subprocess.run(
        ["tar", "-C", str(KUBEKEY), "-cf", "-",
         "--exclude=./.git", "--exclude=./_output", "--exclude=./build", "."],
        stdout=subprocess.PIPE, check=True)
    subprocess.run(["tar", "-C", str(dst), "-xf", "-"], input=tar.stdout, check=True)
    return dst


def run(script: Path, cwd: Path, env_extra: dict[str, str] | None = None,
        timeout: int = GATE_TIMEOUT, in_copy: bool = False) -> tuple[int, str, str]:
    env = dict(os.environ)
    if in_copy:
        env["ANI_CHECK_IN_COPY"] = "1"
    if env_extra:
        env.update(env_extra)
    try:
        proc = subprocess.run(["bash", str(script)], cwd=str(cwd), env=env,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                              text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        return 124, "", f"still running after {timeout}s"
    return proc.returncode, proc.stdout, proc.stderr


# ---------------------------------------------------------------------------
# T-R04-01: the workflow triggers on template/script-only changes and its
# working directory is real.
# ---------------------------------------------------------------------------
def test_workflow(res: Result) -> None:
    print("T-R04-01 工作流覆盖模板/脚本改动，工作目录真实存在，且不限定上游仓库")
    res.check("T-R04-01a", WORKFLOW.is_file(), f"仓库根存在 {WORKFLOW.relative_to(REPO)}")
    if not WORKFLOW.is_file():
        return
    text = WORKFLOW.read_text(encoding="utf-8")
    doc = yaml.safe_load(text)
    # PyYAML reads the bare `on:` key as the boolean True, so look it up both ways.
    triggers = doc.get("on", doc.get(True))
    res.check("T-R04-01b", isinstance(triggers, dict) and {"push", "pull_request"} <= set(triggers),
              f"触发事件包含 push 与 pull_request（实际 {sorted(triggers) if isinstance(triggers, dict) else triggers}）")

    workdir = (doc.get("jobs", {}).get("check-code", {})
               .get("defaults", {}).get("run", {}).get("working-directory"))
    res.check("T-R04-01c", workdir == "kubekey" and (REPO / "kubekey").is_dir(),
              f"working-directory={workdir!r} 且该目录真实存在")

    # T37 / F10: every input the gate reads must be able to trigger it — including
    # the repository-root experiment scripts and the declared lab topology.
    needed = ["kubekey/builtin/**", "kubekey/scripts/**", "kubekey/pkg/**", "kubekey/cmd/**",
              "kubekey/ani/**", "kubekey/hack/**", "kubekey/go.mod", "kubekey/lab/**",
              "run_on_node.sh", "restore_esxi_snapshots.sh", "config/**",
              ".github/workflows/ani-check.yaml"]
    collected = {}
    for event in ("push", "pull_request"):
        paths = triggers.get(event, {}).get("paths", []) if isinstance(triggers, dict) else []
        collected[event] = paths
        missing = [p for p in needed if p not in paths]
        res.check(f"T-R04-01d-{event}", not missing,
                  f"{event} 的 paths 覆盖门禁的全部输入（缺 {missing}）")
    res.check("T-R04-01d-identical", collected.get("push") == collected.get("pull_request"),
              "push 与 pull_request 使用同一 paths 集合（否则一条路径永远无人守）")

    guards = []
    for job in doc.get("jobs", {}).values():
        if isinstance(job, dict):
            if job.get("if"):
                guards.append(str(job["if"]))
            for step in job.get("steps", []):
                if isinstance(step, dict) and step.get("if"):
                    guards.append(str(step["if"]))
    restricted = [g for g in guards if "repository" in g]
    res.check("T-R04-01e", not restricted,
              f"没有任何 job/step 带仓库限定条件（否则在本 fork 上永不触发；发现 {restricted}）")

    steps = doc.get("jobs", {}).get("check-code", {}).get("steps", [])
    runs = [s.get("run", "") for s in steps if isinstance(s, dict)]
    res.check("T-R04-01f", any("scripts/check-code.sh" in r for r in runs),
              "CI 调用的正是唯一门禁脚本 scripts/check-code.sh")
    res.check("T-R04-01g", CHECK.is_file(), "scripts/check-code.sh 存在")


# ---------------------------------------------------------------------------
# T-R04-02: injected faults fail the gate, and no release is produced.
# ---------------------------------------------------------------------------
FAULTS = {
    "bad-heredoc": "here-document <<EOF is not terminated",
    "unknown-task-key": "unknown task keys",
    "literal-external-url": "literal external URL",
}

# T37 / F10: a Go fault in each product tree must fail the gate. The third is
# visible only with -tags builtin, which is exactly the code that ships.
GO_FAULTS = {
    "cmd-builtin": ("cmd/kk/app/builtin/ani.go", "the shipped ANI CLI"),
    "connector": ("pkg/connector/ssh_connector.go", "pkg/connector"),
    "builtin-tagged": ("pkg/ani/components_project_builtin.go", "the embedded builtin project"),
}


def inject_go_fault(module: Path, relative: str) -> None:
    """Append a declaration that cannot compile to one real source file."""
    path = module / relative
    if not path.is_file():
        raise AssertionError(f"injection target {path} is not a file")
    path.write_text(path.read_text(encoding="utf-8") + "\nthis is not go code\n", encoding="utf-8")


def inject_into_ceph_tasks(module: Path, name: str) -> None:
    """Replace one Ceph apply command with the fault under test.

    The anchor is the single-line apply task as the role ships it today; the
    replacement keeps the YAML valid (except for the explicit bad-YAML case) so
    the failure has to come from the gate, not from the injector.
    """
    path = module / "builtin/core/roles/ani/ceph/tasks/main.yaml"
    text = path.read_text(encoding="utf-8")
    anchor = re.compile(r"^  command: kubectl apply .*01-common\.yaml$", re.M)
    if not anchor.search(text):
        raise AssertionError(f"injection anchor not found in {path}")
    replacement = {
        "bad-heredoc": "  command: |\n    cat <<EOF\n",
        "unknown-task-key": "  commmand: echo typo",
        "literal-external-url": ("  command: |\n    echo "
                                 "\"https://raw.githubusercontent.com/kubesphere/kubekey/main/README.md\"\n"),
    }[name]
    path.write_text(anchor.sub(replacement.rstrip("\n"), text, count=1) + "\n", encoding="utf-8")


def test_gate_rejects_injected_faults(res: Result, workdir: Path) -> None:
    print("T-R04-02 注入坏 YAML / 参数错 / 外部 URL 后门禁非零，且不生成可发布包")
    for name, expected in FAULTS.items():
        module = copy_module(workdir / name)
        inject_into_ceph_tasks(module, name)
        rc, out, err = run(module / "scripts/check-code.sh", module, in_copy=True)
        res.check(f"T-R04-02a-{name}", rc != 0 and expected in err + out,
                  f"{name}: 门禁非零且指出 {expected!r}（rc={rc}）")

    for name, (relative, label) in GO_FAULTS.items():
        module = copy_module(workdir / f"go-{name}")
        inject_go_fault(module, relative)
        rc, out, err = run(module / "scripts/check-code.sh", module, in_copy=True)
        combined = out + err
        # The failing file must be named by the compiler, and the step that
        # caught it must be a Go build step (not an incidental later failure).
        res.check(f"T-R04-02g-{name}", rc != 0 and relative in combined and "go build" in combined,
                  f"{label}: 门禁必须编译到 {relative} 并非零（rc={rc}）")

    module = copy_module(workdir / "bad-yaml")
    # An unterminated flow sequence is a genuine YAML error (the previous
    # fixture was only a block scalar that happened to look broken).
    (module / "builtin/core/roles/ani/smoke/tasks/broken.yaml").write_text(
        "- name: broken\n  command: echo ok\n  when: [unterminated\n", encoding="utf-8")
    rc, out, err = run(module / "scripts/check-code.sh", module, in_copy=True)
    res.check("T-R04-02a-bad-yaml", rc != 0 and "YAML parse error" in err + out,
              f"坏 YAML: 门禁非零且报 YAML 解析错误（rc={rc}）")

    # build-code.sh must refuse to produce a release when the gate fails.
    module = copy_module(workdir / "build-refuses")
    inject_into_ceph_tasks(module, "unknown-task-key")
    out_dir = module.parent / "release-should-not-exist"
    rc, out, err = run(module / "scripts/build-code.sh", module,
                       {"ANI_CODE_OUT": str(out_dir), "GO_BIN": shutil.which("go") or "go"})
    res.check("T-R04-02b", rc != 0 and not out_dir.exists(),
              f"门禁失败时 build-code.sh 非零且未创建发布目录（rc={rc}，exists={out_dir.exists()}）")


# ---------------------------------------------------------------------------
# T-R04-03: an unsatisfying toolchain fails clearly; the tests keep off $HOME.
# ---------------------------------------------------------------------------
def test_toolchain_and_home(res: Result, workdir: Path) -> None:
    print("T-R04-03 工具链不满足时清楚失败；测试不写真实 HOME")
    shim_dir = workdir / "shim-bin"
    shim_dir.mkdir(parents=True, exist_ok=True)
    shim_log = workdir / "shim-go.log"
    shim = shim_dir / "go"
    shim.write_text(f"""#!/usr/bin/env bash
printf '%s\\n' "$*" >> "{shim_log}"
case "$1" in
  version) echo "go version go1.20.0 linux/amd64" ;;
  *) echo "shim: unexpected call $*" >&2; exit 90 ;;
esac
""", encoding="utf-8")
    shim.chmod(0o700)
    rc, out, err = run(CHECK, KUBEKEY, {"GO_BIN": str(shim), "GOTOOLCHAIN": "local"})
    combined = out + err
    res.check("T-R04-03a", rc != 0 and "older than go.mod" in combined and "GOTOOLCHAIN=local" in combined,
              f"旧工具链 + GOTOOLCHAIN=local 时清楚失败（rc={rc}）")
    calls = shim_log.read_text(encoding="utf-8") if shim_log.exists() else ""
    res.check("T-R04-03b", calls.strip() == "version",
              f"失败发生在任何构建之前，只调用过 go version（实际调用 {calls.split() or calls!r}）")

    # The gate's suites must not depend on, or write to, the real HOME: two of
    # them are run here with HOME pointed at an empty temporary directory.
    fake_home = workdir / "fake-home"
    fake_home.mkdir(exist_ok=True)
    for suite in ("test-lab-credentials.py", "test-debian-repository.py"):
        proc = subprocess.run([sys.executable, str(KUBEKEY / "scripts" / suite)],
                              cwd=str(KUBEKEY), env={**os.environ, "HOME": str(fake_home)},
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
                              timeout=GATE_TIMEOUT)
        res.check(f"T-R04-03c-{suite}", proc.returncode == 0,
                  f"{suite} 在临时 HOME 下仍通过（rc={proc.returncode}）")
    leftovers = sorted(p.name for p in fake_home.iterdir())
    res.check("T-R04-03d", not leftovers,
              f"行为测试没有写入 HOME（临时 HOME 内 {leftovers or '空'}）")


# ---------------------------------------------------------------------------
# T-R04-04: a source change after the gate fails the release identity check.
# ---------------------------------------------------------------------------
def test_release_identity(res: Result, workdir: Path) -> None:
    print("T-R04-04 门禁之后源码被改动，发布必须被拒绝")
    module = copy_module(workdir / "identity")
    gate = module / "scripts/check-code.sh"
    gate.write_text("""#!/usr/bin/env bash
# Injected fault: a gate that "passes" while changing the source tree.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
printf '\\n# changed after the gate\\n' >> "$ROOT/scripts/install.sh"
exit 0
""", encoding="utf-8")
    gate.chmod(0o700)
    out_dir = module.parent / "identity-release"
    rc, out, err = run(module / "scripts/build-code.sh", module,
                       {"ANI_CODE_OUT": str(out_dir), "GO_BIN": shutil.which("go") or "go"})
    res.check("T-R04-04a", rc != 0 and "source tree changed while the code gate ran" in err,
              f"门禁后被改动 -> 拒绝发布（rc={rc}）")
    res.check("T-R04-04b", not out_dir.exists(),
              f"没有生成任何发布目录（exists={out_dir.exists()}）")


IN_COPY = os.environ.get("ANI_CHECK_IN_COPY") == "1"


def main() -> int:
    for required in (CHECK, BUILD, KUBEKEY / "scripts" / "test-ani-task-errors.py"):
        if not required.exists():
            print(f"missing required file: {required}", file=sys.stderr)
            return 2
    res = Result()
    with tempfile.TemporaryDirectory(prefix="r04-check-") as tmp:
        workdir = Path(tmp)
        test_workflow(res)
        if IN_COPY:
            # This run happens inside a copy that the outer meta-test made. The
            # copy-based fault injection and the toolchain checks belong to the
            # outer run; the cheap workflow assertions above still run here, so a
            # broken workflow is caught in a copy too.
            print("  (run inside a copied tree: copy-based fault injection and "
                  "toolchain checks are done by the outer run)")
            workdir_used = False
        else:
            workdir_used = True
        if not workdir_used:
            print_res(res)
            return 1 if res.failed else 0
        test_gate_rejects_injected_faults(res, workdir)
        test_toolchain_and_home(res, workdir)
        test_release_identity(res, workdir)

    print_res(res)
    return 1 if res.failed else 0


def print_res(res: Result) -> None:
    print()
    print(f"cases passed: {len(res.passed)}")
    print(f"cases failed: {len(res.failed)}")
    for item in res.failed:
        print(f"  FAILED {item}")
    if not res.failed:
        print("ALL R04 GATE TESTS PASSED")


if __name__ == "__main__":
    raise SystemExit(main())

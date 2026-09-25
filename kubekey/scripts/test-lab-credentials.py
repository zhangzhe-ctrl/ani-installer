#!/usr/bin/env python3
"""Behavioral tests for ANI lab credentials and remote payload safety (R01).

Run from the kubekey module directory:

    python3 scripts/test-lab-credentials.py

Everything runs against fake ``ssh``/``scp``/``setsid`` binaries placed first on
PATH. No real connection is possible from these tests: the fakes never open a
socket, they only record argv and simulate a remote filesystem in a temp dir.
Any test-specific secret is synthetic ("FAKE-...") and must never appear in the
captured output.
"""
from __future__ import annotations

import os
import shutil
import stat
import subprocess
import sys
import tempfile
from pathlib import Path

TEST_SECRET = "FAKE-DO-NOT-USE-S3cr3t-value"
FAKE_NODE_IP = "203.0.113.10"          # TEST-NET-3, never a lab node
FAKE_ESXI_IP = "198.51.100.10"         # TEST-NET-2, never the ESXi host

KUBEKEY = Path(__file__).resolve().parent.parent
REPO = KUBEKEY.parent
RUN_ON_NODE = REPO / "run_on_node.sh"
RESTORE = REPO / "restore_esxi_snapshots.sh"
BUILD_CODE = KUBEKEY / "scripts" / "build-code.sh"
BUILD_OFFLINE = KUBEKEY / "scripts" / "build-offline.sh"

FAKE_SSH = """#!/bin/sh
# Fake ssh: records argv, simulates upload/exec against FAKE_ROOT. Never connects.
{ printf 'CALL ssh argv0=%s' "$0"
  for a in "$@"; do printf ' [%s]' "$a"; done
  printf '\\n'; } >> "$CALL_LOG"
# remote command = the argument right after user@host
cmd=""
seen=0
for a in "$@"; do
  if [ "$seen" = 1 ]; then cmd="$a"; break; fi
  case "$a" in *@*) seen=1;; esac
done
# Upload failure is simulated ONLY for the upload command: a later exec would
# succeed, which is exactly the dangerous legacy behavior we must detect.
case "$cmd" in
  "cat > "*)
    if [ -n "${FAKE_FAIL_UPLOAD:-}" ]; then
      printf 'UPLOAD_FAIL\\n' >> "$CALL_LOG"
      exit 1
    fi
    path=$(printf '%s' "$cmd" | sed -e "s/^cat > *//" -e "s/'//g")
    cat > "$FAKE_ROOT$path" 2>/dev/null || exit 1
    printf 'UPLOAD_OK %s\\n' "$path" >> "$CALL_LOG"
    exit 0
    ;;
  *)
    exec_path=$(printf '%s' "$cmd" | sed -nE "s#.*bash +'?([^ ';]+)'?.*#\\1#p")
    printf 'EXEC path=%s\\n' "$exec_path" >> "$CALL_LOG"
    # 模拟远端真实执行：把被执行文件的首行也记进日志，
    # 这样“是否执行了残留旧脚本”才是可观测的，而不是永真断言。
    if [ -n "$exec_path" ] && [ -f "$FAKE_ROOT$exec_path" ]; then
      head -n1 "$FAKE_ROOT$exec_path" >> "$CALL_LOG"
    fi
    exit 0
    ;;
esac
"""

FAKE_PING = """#!/bin/sh
# Fake ping: 永不真实发包，只记录调用。
printf 'CALL ping\\n' >> "$CALL_LOG"
exit 1
"""

FAKE_SETSID = """#!/bin/sh
# Fake setsid: strips -w and execs the real command (still on the fake PATH).
{ printf 'CALL setsid'
  for a in "$@"; do printf ' [%s]' "$a"; done
  printf '\\n'; } >> "$CALL_LOG"
[ "$1" = "-w" ] && shift
exec "$@"
"""

FAKE_SCP = """#!/bin/sh
{ printf 'CALL scp'
  for a in "$@"; do printf ' [%s]' "$a"; done
  printf '\\n'; } >> "$CALL_LOG"
exit ${FAKE_FAIL_UPLOAD:-0}
"""


class Result:
    def __init__(self) -> None:
        self.passed: list[str] = []
        self.failed: list[str] = []

    def check(self, test_id: str, condition: bool, detail: str) -> bool:
        (self.passed if condition else self.failed).append(f"{test_id}: {detail}")
        print(f"  {'PASS' if condition else 'FAIL'}  {test_id} — {detail}")
        return condition


def write_script(path: Path, body: str, mode: int = 0o700) -> None:
    path.write_text(body, encoding="utf-8")
    path.chmod(mode)


def base_env(fake_bin: Path, log: Path, fake_root: Path, extra: dict[str, str] | None = None) -> dict[str, str]:
    env = dict(os.environ)
    env["PATH"] = f"{fake_bin}:{os.environ.get('PATH', '/usr/bin:/bin')}"
    env["CALL_LOG"] = str(log)
    env["FAKE_ROOT"] = str(fake_root)
    for var in ("ESXI_PASS", "ESXI_PASS_FILE", "NODE_ACCESS_DIR", "NODE_ASKPASS",
                "NODE_PWFILE", "FAKE_FAIL_UPLOAD", "ANI_RELEASE_CHECK_ONLY"):
        env.pop(var, None)
    env["ESXI_HOST"] = FAKE_ESXI_IP
    env["ESXI_USER"] = "fake-user"
    env["ESXI_SSH_PORT"] = "22"
    # 关键隔离：把 NODE_ACCESS_DIR 钉在沙箱里，保证默认路径永远不会
    # 落到真实凭据目录（/home/chabking/ani-installer-runs/.../access）。
    sandbox_access = sandbox_access_dir(fake_root)
    env["NODE_ACCESS_DIR"] = str(sandbox_access)
    if extra:
        env.update(extra)
    return env


def sandbox_access_dir(fake_root: Path) -> Path:
    """沙箱凭据目录：内容全为虚构值，用于替代真实 access 目录默认值。"""
    d = fake_root.parent / "sandbox-access"
    if not d.exists():
        d.mkdir(parents=True, exist_ok=True)
        askpass = d / "askpass.sh"
        write_script(askpass, f"#!/bin/sh\nprintf '%s\\n' \"{TEST_SECRET}\"\n", 0o700)
        pw = d / "node-password"
        pw.write_text(TEST_SECRET + "\n", encoding="utf-8")
        pw.chmod(0o600)
    return d


def run(args: list[str], env: dict[str, str], cwd: Path | None = None, timeout: int = 120):
    proc = subprocess.run(args, env=env, cwd=str(cwd or REPO), timeout=timeout,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
                          errors="replace")
    return proc.returncode, proc.stdout, proc.stderr


def read_log(log: Path) -> list[str]:
    return log.read_text(encoding="utf-8").splitlines() if log.exists() else []


def count_exec(log: Path) -> int:
    return sum(1 for line in read_log(log) if line.startswith("EXEC "))


def count_scp_calls(log: Path) -> int:
    return sum(1 for line in read_log(log) if line.startswith("CALL scp"))


def count_ssh_calls(log: Path) -> int:
    return sum(1 for line in read_log(log) if line.startswith("CALL ssh"))


def upload_paths(log: Path) -> list[str]:
    return [line.split(None, 1)[1] for line in read_log(log) if line.startswith("UPLOAD_OK ")]


def make_access(tmp: Path) -> tuple[Path, Path]:
    access = tmp / "access"
    access.mkdir(parents=True, exist_ok=True)
    askpass = access / "askpass.sh"
    write_script(askpass, f"#!/bin/sh\nprintf '%s\\n' \"$NODE_TEST_SECRET\"\n", 0o700)
    pwfile = access / "node-password"
    pwfile.write_text(TEST_SECRET + "\n", encoding="utf-8")
    pwfile.chmod(0o600)
    return askpass, pwfile


# --------------------------------------------------------------------------
# T-R01-01: missing credentials -> non-zero exit, zero ssh calls
# --------------------------------------------------------------------------
def test_missing_credentials(tmp: Path, res: Result) -> None:
    print("T-R01-01 缺少凭据时必须非零退出且不发起连接")
    fake_bin, log, root = setup_fakes(tmp / "t01")
    env = base_env(fake_bin, log, root)

    rc, out, err = run(["bash", str(RESTORE), "dry-run"], env)
    res.check("T-R01-01a", rc != 0, f"restore 缺凭据返回非零（rc={rc}）")
    res.check("T-R01-01b", count_ssh_calls(log) == 0,
              f"ssh 调用数为 0（实际 {count_ssh_calls(log)}）")
    res.check("T-R01-01c", "ESXI_PASS" in (out + err) and TEST_SECRET not in (out + err),
              "错误信息点名所需凭据来源，且不含任何秘密值")

    # 凭据文件不可读
    log.write_text("", encoding="utf-8")
    bad = tmp / "t01" / "missing-cred"
    rc, out, err = run(["bash", str(RESTORE), "dry-run"], base_env(fake_bin, log, root, {"ESXI_PASS_FILE": str(bad)}))
    res.check("T-R01-01d", rc != 0, f"ESXI_PASS_FILE 不存在时非零退出（rc={rc}）")
    res.check("T-R01-01e", count_ssh_calls(log) == 0, "该路径下 ssh 调用数为 0")

    # 凭据文件权限过宽
    log.write_text("", encoding="utf-8")
    loose = tmp / "t01" / "loose-cred"
    loose.write_text(TEST_SECRET + "\n", encoding="utf-8")
    loose.chmod(0o644)
    rc, out, err = run(["bash", str(RESTORE), "dry-run"], base_env(fake_bin, log, root, {"ESXI_PASS_FILE": str(loose)}))
    res.check("T-R01-01f", rc == 67, f"凭据文件权限非 0600 时以 67 退出（rc={rc}）")
    res.check("T-R01-01f2", count_ssh_calls(log) == 0, "该路径下 ssh 调用数为 0")
    res.check("T-R01-01f3", TEST_SECRET not in (out + err), "该路径下输出不含凭据文件内容")

    # run_on_node：ASKPASS 缺失
    log.write_text("", encoding="utf-8")
    payload = tmp / "t01" / "payload.sh"
    payload.write_text("echo hi\n", encoding="utf-8")
    rc, out, err = run(["bash", str(RUN_ON_NODE), FAKE_NODE_IP, str(payload)],
                       base_env(fake_bin, log, root, {"NODE_ASKPASS": str(tmp / "t01" / "nope.sh")}))
    res.check("T-R01-01g", rc != 0, f"run_on_node 缺 ASKPASS 非零退出（rc={rc}）")
    res.check("T-R01-01h", count_ssh_calls(log) == 0, "该路径下 ssh 调用数为 0")

    # run_on_node：payload 缺失
    log.write_text("", encoding="utf-8")
    askpass, _ = make_access(tmp / "t01")
    rc, out, err = run(["bash", str(RUN_ON_NODE), FAKE_NODE_IP, str(tmp / "t01" / "no-such.sh")],
                       base_env(fake_bin, log, root, {"NODE_ASKPASS": str(askpass)}))
    res.check("T-R01-01i", rc != 0, f"payload 缺失时非零退出（rc={rc}）")
    res.check("T-R01-01j", count_ssh_calls(log) == 0, "该路径下 ssh 调用数为 0")

    # sudo 模式：空的节点密码文件必须被拒绝
    log.write_text("", encoding="utf-8")
    access = tmp / "t01" / "empty"
    access.mkdir(parents=True, exist_ok=True)
    askpass, _ = make_access(access)
    empty_pw = access / "empty-password"
    empty_pw.write_text("", encoding="utf-8")
    empty_pw.chmod(0o600)
    rc, out, err = run(["bash", str(RUN_ON_NODE), FAKE_NODE_IP, str(payload), "sudo"],
                       base_env(fake_bin, log, root, {"NODE_ASKPASS": str(askpass), "NODE_PWFILE": str(empty_pw)}))
    res.check("T-R01-01l", rc != 0, f"sudo 模式密码文件为空时非零退出（rc={rc}）")
    res.check("T-R01-01m", count_ssh_calls(log) == 0, "该路径下 ssh 调用数为 0")

    # run_on_node：NODE_ACCESS_DIR 指向空目录时必须失败，绝不回退到真实凭据目录
    log.write_text("", encoding="utf-8")
    empty_access = tmp / "t01" / "empty-access"
    empty_access.mkdir(parents=True, exist_ok=True)
    rc, out, err = run(["bash", str(RUN_ON_NODE), FAKE_NODE_IP, str(payload)],
                       base_env(fake_bin, log, root, {"NODE_ACCESS_DIR": str(empty_access)}))
    res.check("T-R01-01n", rc != 0, f"覆盖后的凭据目录为空时非零退出（rc={rc}）")
    res.check("T-R01-01n2", count_ssh_calls(log) == 0,
              "未回退到真实 access 目录，ssh 调用数为 0")

    # 帮助路径不应被凭据预检阻断
    rc, out, err = run(["bash", str(RESTORE), "--help"], base_env(fake_bin, log, root))
    res.check("T-R01-01k", rc == 0, f"--help 仍可用且不受凭据预检影响（rc={rc}）")


# --------------------------------------------------------------------------
# T-R01-02: secrets never leak into logs; release fixtures carry no credentials
# --------------------------------------------------------------------------
def test_secret_absent_and_packaging(tmp: Path, res: Result) -> None:
    print("T-R01-02 测试用秘密不得出现在日志；发布 fixture 不得含凭据与实验脚本")
    fake_bin, log, root = setup_fakes(tmp / "t02")
    askpass, pwfile = make_access(tmp / "t02")
    env = base_env(fake_bin, log, root,
                   {"ESXI_PASS": TEST_SECRET, "NODE_ASKPASS": str(askpass), "NODE_PWFILE": str(pwfile)})

    rc, out, err = run(["bash", str(RESTORE), "dry-run"], env)
    combined = (out or "") + (err or "")
    res.check("T-R01-02a", TEST_SECRET not in combined, "restore 输出不含测试秘密")

    payload = tmp / "t02" / "payload.sh"
    payload.write_text("echo same semantics\n", encoding="utf-8")
    rc, out, err = run(["bash", str(RUN_ON_NODE), FAKE_NODE_IP, str(payload), "sudo"], env)
    combined = (out or "") + (err or "")
    res.check("T-R01-02b", TEST_SECRET not in combined, "run_on_node(sudo) 输出不含测试秘密")
    res.check("T-R01-02c", TEST_SECRET not in log.read_text(encoding="utf-8"), "调用日志不含测试秘密")

    # 发布体检：含凭据/实验脚本的目录必须被拒绝
    dirty = tmp / "t02" / "release-dirty"
    dirty.mkdir(parents=True, exist_ok=True)
    for name in ("kk", "install.sh", "restore_esxi_snapshots.sh", "run_on_node.sh", "lab.env",
                 "askpass.sh", "node-password", "id_ed25519", "kubeconfig", "admin.conf",
                 "site-secrets.pem", "service.key", "app-credential.txt"):
        (dirty / name).write_text("placeholder\n", encoding="utf-8")
    before_builds = sorted(p.name for p in (KUBEKEY / "build").glob("*")) if (KUBEKEY / "build").exists() else []
    rc, out, err = run(["bash", str(BUILD_CODE)], base_env(fake_bin, log, root, {"ANI_RELEASE_CHECK_ONLY": str(dirty)}))
    res.check("T-R01-02d", rc != 0 and "forbidden credential/lab file" in err,
              f"build-code.sh 由发布体检本身拒绝该目录（rc={rc}）")
    after_builds = sorted(p.name for p in (KUBEKEY / "build").glob("*")) if (KUBEKEY / "build").exists() else []
    res.check("T-R01-02e", before_builds == after_builds, "体检模式未产生任何真实发布物")

    rc, out, err = run(["bash", str(BUILD_OFFLINE)], base_env(fake_bin, log, root, {"ANI_RELEASE_CHECK_ONLY": str(dirty)}))
    res.check("T-R01-02f", rc != 0 and "forbidden credential/lab file" in err,
              f"build-offline.sh 同样由体检本身拒绝（rc={rc}）")

    res.check("T-R01-02h", count_scp_calls(log) == 0,
              "被测脚本的既有约定是 ssh stdin 上传；fake scp 全程未被调用（tripwire）")

    clean = tmp / "t02" / "release-clean"
    clean.mkdir(parents=True, exist_ok=True)
    (clean / "kk").write_text("binary\n", encoding="utf-8")
    (clean / "install.sh").write_text("#!/bin/sh\n", encoding="utf-8")
    rc, out, err = run(["bash", str(BUILD_CODE)], base_env(fake_bin, log, root, {"ANI_RELEASE_CHECK_ONLY": str(clean)}))
    res.check("T-R01-02g", rc == 0, f"干净的发布目录通过体检（rc={rc}）")


# --------------------------------------------------------------------------
# T-R01-03: upload failure -> zero payload executions; unique payload paths
# --------------------------------------------------------------------------
def test_upload_failure_and_unique_paths(tmp: Path, res: Result) -> None:
    print("T-R01-03 上传失败时 payload 执行次数为 0；并发调用路径唯一")
    # 预置一个“旧的残留 payload”，验证上传失败后不会执行它
    fake_bin, log, root = setup_fakes(tmp / "t03")
    askpass, pwfile = make_access(tmp / "t03")
    stale = root / "tmp"
    stale.mkdir(parents=True, exist_ok=True)
    stale_ron = stale / "_ron.sh"
    stale_ron.write_text("echo STALE\n", encoding="utf-8")

    payload = tmp / "t03" / "payload.sh"
    payload.write_text("echo new\n", encoding="utf-8")

    env = base_env(fake_bin, log, root,
                   {"NODE_ASKPASS": str(askpass), "NODE_PWFILE": str(pwfile), "FAKE_FAIL_UPLOAD": "1"})
    rc, out, err = run(["bash", str(RUN_ON_NODE), FAKE_NODE_IP, str(payload)], env)
    res.check("T-R01-03a", rc != 0, f"上传失败时 run_on_node 非零退出（rc={rc}）")
    res.check("T-R01-03b", count_exec(log) == 0, f"payload 执行次数为 0（实际 {count_exec(log)}）")
    res.check("T-R01-03c", "STALE" not in log.read_text(encoding="utf-8"),
              "日志中不存在旧残留脚本被执行的内容")

    # 并发：两次成功调用必须使用不同的 payload 路径，且各执行一次。
    # 每个进程使用独立日志，避免并发追加同一文件时丢失事件造成假结果。
    logs = []
    procs = []
    for i in range(2):
        plog = tmp / "t03" / f"concurrent-{i}.log"
        plog.write_text("", encoding="utf-8")
        logs.append(plog)
        env_i = base_env(fake_bin, plog, root, {"NODE_ASKPASS": str(askpass), "NODE_PWFILE": str(pwfile)})
        procs.append((subprocess.Popen(["bash", str(RUN_ON_NODE), FAKE_NODE_IP, str(payload)],
                                       env=env_i, cwd=str(REPO), stdout=subprocess.PIPE,
                                       stderr=subprocess.PIPE, text=True), plog))
    rcs = [p.wait(timeout=120) for p, _ in procs]
    per_proc = [upload_paths(plog) for _, plog in procs]
    paths = [p for group in per_proc for p in group]
    lost = [i for i, group in enumerate(per_proc) if len(group) != 1]
    res.check("T-R01-03d", all(rc == 0 for rc in rcs), f"并发成功路径均返回 0（rcs={rcs}）")
    res.check("T-R01-03e", len(set(paths)) == 2 and len(paths) == 2,
              f"并发使用互不相同的 payload 路径：{paths}")
    res.check("T-R01-03e2", not lost, f"每个并发进程都记录了各自的上传事件（丢失组={lost}）")
    res.check("T-R01-03f", count_exec(log) == 0 and sum(count_exec(plog) for _, plog in procs) == 2,
              f"payload 各执行一次（实际 {sum(count_exec(plog) for _, plog in procs)}）")
    res.check("T-R01-03g", all(p.startswith("/tmp/_ron.") for p in paths),
              "不再使用固定 /tmp/_ron.sh")

    # 正常路径：上传成功后远端收到的是本次 payload 内容
    payload2 = tmp / "t03" / "payload2.sh"
    payload2.write_text("echo expected-body\n", encoding="utf-8")
    nlog = tmp / "t03" / "normal.log"
    nlog.write_text("", encoding="utf-8")
    env_ok = base_env(fake_bin, nlog, root, {"NODE_ASKPASS": str(askpass), "NODE_PWFILE": str(pwfile)})
    rc, out, err = run(["bash", str(RUN_ON_NODE), FAKE_NODE_IP, str(payload2)], env_ok)
    uploaded = upload_paths(nlog)[0]
    delivered = (root / uploaded.lstrip("/")).read_text(encoding="utf-8")
    lines = read_log(nlog)
    target_ok = any(f"[ubuntu@{FAKE_NODE_IP}]" in line for line in lines if line.startswith("CALL ssh"))
    res.check("T-R01-03h", rc == 0, f"正常上传+执行返回 0（rc={rc}）")
    res.check("T-R01-03i", delivered == "echo expected-body\n", "payload 内容原样送达")
    res.check("T-R01-03j", target_ok, f"ssh 目标为 ubuntu@{FAKE_NODE_IP}，参数语义未变")
    res.check("T-R01-03k", count_exec(nlog) == 1, "正常路径 payload 只执行一次")


def test_negative_control(tmp: Path, res: Result) -> None:
    """反向对照：旧版 run_on_node.sh（HEAD 中的版本）在上传失败后仍会执行残留脚本。

    如果这一项不成立，说明本套测试缺乏鉴别力，其余用例的通过就不构成证据。
    """
    print("CTRL 反向对照：旧版实现应被本套测试判定为有缺陷")
    base = tmp / "ctrl"
    base.mkdir(parents=True, exist_ok=True)
    legacy = base / "run_on_node.legacy.sh"
    proc = subprocess.run(["git", "-C", str(REPO), "show", "HEAD:run_on_node.sh"],
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, check=False)
    if proc.returncode != 0:
        res.failed.append("CTRL-R01: 无法取回 HEAD 版 run_on_node.sh")
        return
    legacy.write_text(proc.stdout, encoding="utf-8")

    fake_bin, log, root = setup_fakes(base / "fakes")
    root.mkdir(parents=True, exist_ok=True)
    (root / "tmp").mkdir(parents=True, exist_ok=True)
    (root / "tmp" / "_ron.sh").write_text("echo STALE\n", encoding="utf-8")
    askpass, _ = make_access(base)
    payload = base / "payload.sh"
    payload.write_text("echo new-body\n", encoding="utf-8")

    env = base_env(fake_bin, log, root,
                   {"NODE_ASKPASS": str(askpass), "FAKE_FAIL_UPLOAD": "1"})
    rc, out, err = run(["bash", str(legacy), FAKE_NODE_IP, str(payload)], env)
    execs = count_exec(log)
    ctrl_log = log.read_text(encoding="utf-8")
    res.check("CTRL-R01a", execs >= 1 and "STALE" in ctrl_log,
              f"旧实现在上传失败后仍执行了残留旧脚本（EXEC {execs} 次，日志可见残留内容）——缺陷被复现")
    res.check("CTRL-R01b", rc == 0,
              f"旧实现上传失败却返回成功（rc={rc}）——这正是本次要修的行为")


def setup_fakes(base: Path) -> tuple[Path, Path, Path]:
    base.mkdir(parents=True, exist_ok=True)
    fake_bin = base / "fake-bin"
    fake_bin.mkdir(exist_ok=True)
    log = base / "calls.log"
    log.write_text("", encoding="utf-8")
    root = base / "fake-root"        # 模拟节点文件系统
    (root / "tmp").mkdir(parents=True, exist_ok=True)
    write_script(fake_bin / "ssh", FAKE_SSH)
    write_script(fake_bin / "scp", FAKE_SCP)
    write_script(fake_bin / "setsid", FAKE_SETSID)
    write_script(fake_bin / "ping", FAKE_PING)
    return fake_bin, log, root


def main() -> int:
    if not (RUN_ON_NODE.exists() and RESTORE.exists()):
        print(f"missing scripts under test: {RUN_ON_NODE} / {RESTORE}", file=sys.stderr)
        return 2

    res = Result()
    with tempfile.TemporaryDirectory(prefix="r01-creds-") as tmpdir:
        tmp = Path(tmpdir)
        try:
            test_negative_control(tmp, res)
            test_missing_credentials(tmp, res)
            test_secret_absent_and_packaging(tmp, res)
            test_upload_failure_and_unique_paths(tmp, res)
        except subprocess.TimeoutExpired as exc:
            res.failed.append(f"timeout: {exc}")

    print()
    print(f"cases passed: {len(res.passed)}")
    print(f"cases failed: {len(res.failed)}")
    for item in res.failed:
        print(f"  FAILED {item}")
    if res.failed:
        return 1
    print("ALL R01 LOCAL TESTS PASSED")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

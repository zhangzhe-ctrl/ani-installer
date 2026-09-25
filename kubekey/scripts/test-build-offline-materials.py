#!/usr/bin/env python3
"""Behavioural tests for R07.2: the offline build verifies materials against the
approved lock, ships the CONFIG it actually consumed, and never accepts an
in-package SHA256SUMS as a substitute for the lock.

Run from the kubekey module directory:

    python3 scripts/test-build-offline-materials.py

Every build runs in a throwaway directory with fixture inputs. No hauler store,
no registry, no network: the tests always pass KUBEKEY_ARTIFACT and
HAULER_ARCHIVE so build-offline.sh never pulls or loads images.
"""
from __future__ import annotations

import hashlib
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

KUBEKEY = Path(__file__).resolve().parent.parent
BUILD = KUBEKEY / "scripts" / "build-offline.sh"


class Result:
    def __init__(self) -> None:
        self.passed: list[str] = []
        self.failed: list[str] = []

    def check(self, test_id: str, condition: bool, detail: str) -> bool:
        (self.passed if condition else self.failed).append(f"{test_id}: {detail}")
        print(f"  {'PASS' if condition else 'FAIL'}  {test_id} — {detail}")
        return condition


def sha256_file(path: Path) -> str:
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


class Fixture:
    """A throwaway module tree plus the fixture inputs build-offline.sh needs."""

    def __init__(self) -> None:
        self.dir = Path(tempfile.mkdtemp(prefix="r072-build-"))
        self.module = self.dir / "kubekey"
        self.inputs = self.dir / "inputs"
        self.inputs.mkdir(parents=True)
        shutil.copytree(KUBEKEY, self.module,
                        ignore=shutil.ignore_patterns(".git", "_output", "build", "__pycache__"))

        # helm content; the fixture lock approves exactly this binary.
        self.helm = self.inputs / "helm-bin"
        self.helm.write_bytes(b"fixture helm binary for linux amd64\n")
        self.helm.chmod(0o755)
        helm_sha = sha256_file(self.helm)

        # repository ISO content and its source-side checksum record.
        self.iso = self.inputs / "ubuntu-24.04-debs-amd64.iso"
        self.iso.write_bytes(b"fixture repository iso bytes\n")
        self.iso_checksums = self.inputs / "repository-iso-checksums.txt"
        self.iso_checksums.write_text(
            f"ubuntu-24.04-debs-amd64.iso {sha256_file(self.iso)}\n", encoding="utf-8")

        # package.yaml (the CONFIG this build consumes).
        self.config = self.inputs / "package.yaml"
        self.config.write_text(
            "# fixture artifact config\napiVersion: ani.artifact/v1\nkind: Package\n"
            "images:\n  - docker.io/example/fixture:1\n", encoding="utf-8")

        self.images_tsv = self.inputs / "images.tsv"
        self.images_tsv.write_text(
            "original_ref\thauler_ref\tactual_digest\tuse_location\n"
            "docker.io/example/fixture:1\t127.0.0.1:5000/example/fixture:1\t"
            f"sha256:{hashlib.sha256(b'fixture image').hexdigest()}\ttest\n", encoding="utf-8")

        # two charts; the fixture lock approves exactly these two files.
        self.charts_dir = self.inputs / "charts"
        (self.charts_dir / "cert-manager" / "1.0.0.tgz").parent.mkdir(parents=True)
        self.chart_a = self.charts_dir / "cert-manager" / "1.0.0.tgz"
        self.chart_a.write_bytes(b"fixture chart A bytes\n")
        (self.charts_dir / "nats" / "2.0.0.tgz").parent.mkdir(parents=True)
        self.chart_b = self.charts_dir / "nats" / "2.0.0.tgz"
        self.chart_b.write_bytes(b"fixture chart B bytes\n")

        self.lock = self.inputs / "components.lock.yaml"
        self.lock.write_text(
            "apiVersion: ani.installer/v1\nkind: ComponentMaterialLock\n"
            "tools:\n  helm:\n    version: v0-fix\n"
            f"    binarySha256: {sha256_file(self.helm)}\n"
            "    artifactPath: bin/helm\n"
            "components:\n"
            "  cert-manager:\n"
            "    chartSha256: " + sha256_file(self.chart_a) + "\n"
            "    artifactChartPath: charts/cert-manager/1.0.0.tgz\n"
            "  nats:\n"
            "    chartSha256: " + sha256_file(self.chart_b) + "\n"
            "    artifactChartPath: charts/nats/2.0.0.tgz\n",
            encoding="utf-8")

        self.hauler = self.inputs / "hauler-bin"
        self.hauler.write_bytes(b"fixture hauler binary\n")
        self.hauler.chmod(0o755)
        self.kk_artifact = self.inputs / "kubekey-artifact.tgz"
        self.kk_artifact.write_bytes(b"fixture kubekey artifact\n")
        self.hauler_archive = self.inputs / "images.haul.tar.zst"
        self.hauler_archive.write_bytes(b"fixture hauler archive\n")

    def build(self, name: str) -> tuple[int, str, str, Path]:
        out = self.dir / f"out-{name}"
        env = dict(os.environ)
        env.update({
            "ANI_ARTIFACT_OUT": str(out),
            "CONFIG": str(self.config),
            "IMAGES_TSV": str(self.images_tsv),
            "COMPONENT_LOCK": str(self.lock),
            "CHARTS_DIR": str(self.charts_dir),
            "HAULER_BIN": str(self.hauler),
            "HELM_BIN": str(self.helm),
            "REPOSITORY_ISO": str(self.iso),
            "ISO_CHECKSUMS": str(self.iso_checksums),
            "KUBEKEY_ARTIFACT": str(self.kk_artifact),
            "HAULER_ARCHIVE": str(self.hauler_archive),
        })
        proc = subprocess.run(["bash", str(BUILD)], cwd=str(self.module), env=env,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
                              timeout=300)
        return proc.returncode, proc.stdout, proc.stderr, out

    def close(self) -> None:
        shutil.rmtree(self.dir, ignore_errors=True)


def test_positive_build(res: Result, fixture: Fixture) -> None:
    print("正向构建：全部材料与锁一致，产物按锁精确成形")
    rc, out, err, out_dir = fixture.build("positive")
    if rc != 0:
        res.check("R07.2-positive-build", False, f"构建失败 rc={rc}\n{err}")
        return
    res.check("R07.2-positive-build", True, "构建成功")
    charts = sorted(p.name for p in (out_dir / "charts").glob("*/*.tgz"))
    res.check("R07.2-charts", charts == ["1.0.0.tgz", "2.0.0.tgz"],
              f"产物内的 chart 集合与锁一致（{charts}）")
    shipped_config = (out_dir / "config" / "package.yaml").read_text(encoding="utf-8")
    res.check("T-R07-06", shipped_config == fixture.config.read_text(encoding="utf-8"),
              "包内 config/package.yaml 与本次构建实际消费的 CONFIG 逐字节一致")
    record = (out_dir / "config" / "materials-source.txt").read_text(encoding="utf-8")
    expected_digest = sha256_file(fixture.config)
    res.check("T-R07-06-record",
              f"config.package.yaml sha256:{expected_digest}" in record
              and "bin.helm" in record and "repository.iso" in record,
              "materials-source.txt 记录了 CONFIG/工具/ISO 的源摘要与材料来源")
    sums = subprocess.run(["sha256sum", "-c", "SHA256SUMS"], cwd=str(out_dir),
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    res.check("R07.2-sums", sums.returncode == 0, "产物 SHA256SUMS 自校验通过")


def test_rejected_builds(res: Result, fixture: Fixture) -> None:
    print("T-R07-01 损坏/未知 chart、工具/ISO 摘要错、锁 chart 缺失都必须失败")
    cases = {
        "corrupted-chart": lambda f: (f.charts_dir / "cert-manager" / "1.0.0.tgz")
            .write_bytes(b"tampered chart bytes\n"),
        "unknown-chart": lambda f: (
            (f.charts_dir / "extra" / "9.9.9.tgz").parent.mkdir(parents=True),
            (f.charts_dir / "extra" / "9.9.9.tgz").write_bytes(b"not in the lock\n")),
        "wrong-helm-digest": lambda f: f.helm.write_bytes(b"helm binary with a different digest\n"),
        "wrong-iso-digest": lambda f: f.iso.write_bytes(b"iso bytes that do not match the record\n"),
        "lock-chart-missing": lambda f: (f.charts_dir / "nats" / "2.0.0.tgz").unlink(),
    }
    expected = {
        "corrupted-chart": "not approved in",
        "unknown-chart": "not approved in",
        "wrong-helm-digest": "helm binary sha256",
        "wrong-iso-digest": "repository ISO sha256",
        "lock-chart-missing": "chart set does not match the lock",
    }
    for name, mutate in cases.items():
        case_fixture = Fixture()
        try:
            mutate(case_fixture)
            rc, out, err, out_dir = case_fixture.build(f"rej-{name}")
            want = expected[name]
            record = out_dir / "config" / "materials-source.txt"
            res.check(f"T-R07-01-{name}",
                      rc != 0 and want in err and not record.exists(),
                      f"{name}: 非零且指出 {want!r}；materials-source 记录未写出（rc={rc}）")
        finally:
            case_fixture.close()


# T-R07-05: an attacker who corrupts a shipped chart and regenerates the
# artifact's own SHA256SUMS is still caught, because the approval lives in the
# lock, not in the artifact.
def test_recomputed_sums_cannot_help(res: Result, fixture: Fixture) -> None:
    print("T-R07-05 重算包内 SHA256SUMS 也无法让篡改过的 chart 合格")
    rc, out, err, out_dir = fixture.build("attack")
    if rc != 0:
        res.check("T-R07-05", False, f"基线构建失败 rc={rc}：{err}")
        return
    shipped_chart = out_dir / "charts" / "cert-manager" / "1.0.0.tgz"
    shipped_chart.write_bytes(b"attacker content\n")
    # The attacker regenerates the in-package sums so they are self-consistent.
    subprocess.run(
        ["bash", "-c",
         "cd \"$1\" && find . -type f ! -name SHA256SUMS -print0 | sort -z | "
         "xargs -0 sha256sum > SHA256SUMS",
         "sh", str(out_dir)],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, check=True)
    sums = subprocess.run(["sha256sum", "-c", "SHA256SUMS"], cwd=str(out_dir),
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    res.check("T-R07-05a", sums.returncode == 0,
              "包内 SHA256SUMS 在篡改后是自洽的（攻击者的第一步确实成立）")
    new_hash = sha256_file(shipped_chart)
    shipped_lock = (out_dir / "config" / "components.lock.yaml").read_text(encoding="utf-8")
    res.check("T-R07-05b", new_hash not in shipped_lock,
              "但独立批准锁（随包携带的 components.lock.yaml）里没有该哈希：锁校验仍会拒绝")
    res.check("T-R07-05c",
              sha256_file(fixture.chart_a) in shipped_lock,
              "锁里仍是原始批准哈希，重建包无法替换它")


def main() -> int:
    if not BUILD.is_file():
        print(f"missing required file: {BUILD}", file=sys.stderr)
        return 2
    res = Result()
    fixture = Fixture()
    try:
        test_positive_build(res, fixture)
        test_rejected_builds(res, fixture)
        test_recomputed_sums_cannot_help(res, fixture)
    finally:
        fixture.close()

    print()
    print(f"cases passed: {len(res.passed)}")
    print(f"cases failed: {len(res.failed)}")
    for item in res.failed:
        print(f"  FAILED {item}")
    if res.failed:
        return 1
    print("ALL R07.2 BUILD MATERIAL TESTS PASSED")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

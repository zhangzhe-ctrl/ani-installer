#!/usr/bin/env python3
"""Behavioural tests for R07.2 and F06: the offline build places and verifies
every material through kk's own Go material steps (per-entry chart binding, a
content-checked nested repository ISO, and the install's registry content gate
for the image store), ships the CONFIG it actually consumed, and never accepts an
in-package SHA256SUMS as a substitute for the lock.

Run from the kubekey module directory:

    python3 scripts/test-build-offline-materials.py

Every build runs in a throwaway directory with fixture inputs and the kk binary
this module just built. No hauler binary and no network: the fixture serves a
deterministic in-process OCI registry and passes PACKAGED_REGISTRY_ADDRESS, which
replaces only the script's own `hauler serve` — the image gate itself still runs
against those bytes, so a wrong or missing image fails the build here.
"""
from __future__ import annotations

import gzip
import hashlib
import io
import json
import os
import shutil
import subprocess
import sys
import tarfile
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
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


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sha256_file(path: Path) -> str:
    return sha256_bytes(Path(path).read_bytes())


# ---------------------------------------------------------------------------
# The in-process registry the packaged store is verified against.
# ---------------------------------------------------------------------------

def blob_body(kind: str, repo: str) -> bytes:
    """The bytes a blob actually is: the gate hashes what it downloads."""
    return f"{kind}:{repo}".encode()


def platform_manifest(repo: str) -> bytes:
    """The single linux/amd64 manifest served for one repository."""
    config = "sha256:" + sha256_bytes(blob_body("config", repo))
    layer = "sha256:" + sha256_bytes(blob_body("layer", repo))
    return json.dumps(
        {"schemaVersion": 2,
         "config": {"digest": config, "size": len(blob_body("config", repo))},
         "layers": [{"digest": layer, "size": len(blob_body("layer", repo))}]},
        separators=(",", ":"),
    ).encode()


def known_blobs(repo: str) -> set[str]:
    return {sha256_bytes(f"config:{repo}".encode()), sha256_bytes(f"layer:{repo}".encode())}


class Registry:
    """Serves manifests and blobs that obey the digest contract, so the install's
    content gate can actually be exercised. Knobs model a broken package."""

    def __init__(self) -> None:
        self.tamper: set[str] = set()      # repo substrings: serve wrong bytes
        self.absent: set[str] = set()      # repo substrings: no manifest at all
        self.drop_blobs: set[str] = set()  # repo substrings: blobs missing
        self.bad_blobs: set[str] = set()   # repo substrings: blobs served as other bytes
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args) -> None:  # keep the suite quiet
                pass

            def _split(self) -> tuple[str, str, str] | None:
                path = self.path
                if not path.startswith("/v2/"):
                    return None
                rest = path[len("/v2/"):]
                for kind in ("/manifests/", "/blobs/"):
                    if kind in rest:
                        head, reference = rest.split(kind, 1)
                        return kind, head, reference
                return None

            def _refused(self, repo: str) -> bool:
                return any(fragment in repo for fragment in outer.absent)

            def _respond(self, status: int, body: bytes = b"") -> None:
                self.send_response(status)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                if self.command != "HEAD" and body:
                    self.wfile.write(body)

            def do_GET(self) -> None:  # noqa: N802
                self._handle()

            def do_HEAD(self) -> None:  # noqa: N802
                self._handle()

            def _handle(self) -> None:
                split = self._split()
                if split is None:
                    self._respond(200)
                    return
                kind, repo, reference = split
                if kind == "/manifests/":
                    if self._refused(repo):
                        self._respond(404)
                        return
                    body = platform_manifest(repo)
                    if any(fragment in repo for fragment in outer.tamper):
                        body = body + b" "
                    if reference.startswith("sha256:") and reference != "sha256:" + sha256_bytes(body):
                        self._respond(404)
                        return
                    self._respond(200, body)
                    return
                digest = reference.split("/")[-1]
                if digest.startswith("sha256:"):
                    digest = digest[len("sha256:"):]
                ok = digest in known_blobs(repo) and not any(
                    fragment in repo for fragment in outer.drop_blobs)
                if not ok:
                    self._respond(404)
                    return
                kind = "config" if digest == sha256_bytes(blob_body("config", repo)) else "layer"
                body = blob_body(kind, repo)
                if any(fragment in repo for fragment in outer.bad_blobs):
                    body = b"someone else's bytes"
                self._respond(200, body)

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    @property
    def address(self) -> str:
        host, port = self.server.server_address
        return f"{host}:{port}"

    def start(self) -> None:
        self.thread.start()

    def stop(self) -> None:
        self.server.shutdown()
        self.server.server_close()


# ---------------------------------------------------------------------------
# Fixture inputs.
# ---------------------------------------------------------------------------

def chart_archive(name: str, version: str, extra: bytes = b"") -> bytes:
    """A real Helm chart archive: a gzipped tar whose top level is <name>/."""
    chart_yaml = f"apiVersion: v2\nname: {name}\nversion: {version}\n".encode()
    values = b"# values\n"
    buffer = io.BytesIO()
    with gzip.GzipFile(fileobj=buffer, mode="wb", mtime=0) as gz:
        with tarfile.open(fileobj=gz, mode="w") as archive:
            for entry_name, body in ((f"{name}/Chart.yaml", chart_yaml), (f"{name}/values.yaml", values)):
                info = tarfile.TarInfo(entry_name)
                info.size = len(body)
                info.mode = 0o644
                archive.addfile(info, io.BytesIO(body))
            if extra:
                info = tarfile.TarInfo(f"{name}/EXTRA")
                info.size = len(extra)
                info.mode = 0o644
                archive.addfile(info, io.BytesIO(extra))
    return buffer.getvalue()


class Fixture:
    """A throwaway module tree plus the fixture inputs build-offline.sh needs."""

    def __init__(self, registry: Registry) -> None:
        self.registry = registry
        self.dir = Path(tempfile.mkdtemp(prefix="f06-build-"))
        self.module = self.dir / "kubekey"
        self.inputs = self.dir / "inputs"
        self.inputs.mkdir(parents=True)
        shutil.copytree(KUBEKEY, self.module,
                        ignore=shutil.ignore_patterns(".git", "_output", "build", "__pycache__"))

        # helm content; the fixture lock approves exactly this binary.
        self.helm = self.inputs / "helm-bin"
        self.helm.write_bytes(b"fixture helm binary for linux amd64\n")
        self.helm.chmod(0o755)

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

        # two charts, in the directory layout the lock's names imply.
        self.charts_dir = self.inputs / "charts"
        self.chart_a = self.charts_dir / "cert-manager" / "1.0.0.tgz"
        self.chart_a.parent.mkdir(parents=True)
        self.chart_a.write_bytes(chart_archive("cert-manager", "1.0.0"))
        self.chart_b = self.charts_dir / "nats" / "2.0.0.tgz"
        self.chart_b.parent.mkdir(parents=True)
        self.chart_b.write_bytes(chart_archive("nats", "2.0.0"))

        # A stand-in hauler: packaging only needs `store save` to produce the
        # shipped archive, and `store copy` to fail so the store is staged by
        # copy instead. Everything else must fail, so no test can accidentally
        # depend on a real network or a real registry.
        self.hauler = self.inputs / "hauler-bin"
        self.hauler.write_text(
            "#!/bin/sh\n"
            'if [ "$1" = store ] && [ "$2" = save ]; then\n'
            '  s=""; f=""\n'
            '  shift 2\n'
            '  while [ $# -gt 0 ]; do case "$1" in -s) s="$2"; shift 2;; -f) f="$2"; shift 2;; *) shift;; esac; done\n'
            '  [ -n "$s" ] && [ -n "$f" ] || exit 1\n'
            '  tar -C "$s" -cf "$f" . ; exit $?\n'
            "fi\n"
            "exit 1\n", encoding="utf-8")
        self.hauler.chmod(0o755)

        self.lock = self.inputs / "components.lock.yaml"
        self.write_lock()

        # one approved image row; its digest is whatever the fixture registry
        # really serves for that repository.
        self.images_tsv = self.inputs / "images.tsv"
        self.write_images_tsv()

        # An empty KubeKey artifact tarball: the ISO step must inject into it and
        # preserve it. A real build gets it from `kk artifact export`.
        self.kk_artifact = self.inputs / "kubekey-artifact.tgz"
        buffer = io.BytesIO()
        with gzip.GzipFile(fileobj=buffer, mode="wb", mtime=0) as gz:
            with tarfile.open(fileobj=gz, mode="w") as archive:
                body = b"fixture kubekey payload\n"
                info = tarfile.TarInfo("bin/kk")
                info.size = len(body)
                info.mode = 0o755
                archive.addfile(info, io.BytesIO(body))
        self.kk_artifact.write_bytes(buffer.getvalue())
        # The archive bytes are what ships; their content is irrelevant because the
        # image gate runs against the served store the caller declares.
        self.hauler_archive = self.inputs / "images.haul.tar.zst"
        self.hauler_archive.write_bytes(b"fixture hauler archive\n")

        # A local store holding the same content-addressed objects a real hauler
        # store would: landing reads blobs from it and writes a new store.
        self.store = self.dir / "source-store"
        self.write_store()

        # The approved source manifests, addressed by their own digest: exactly
        # what packaging must be given to be able to derive and prove a landing.
        self.evidence = self.dir / "evidence"
        self.write_evidence()

    def write_store(self) -> None:
        blobs = self.store / "blobs" / "sha256"
        blobs.mkdir(parents=True)
        manifests = []
        for _original, hauler_ref in self.image_rows():
            repo = self.repo_of(hauler_ref)
            body = platform_manifest(repo)
            if any(fragment in repo for fragment in self.registry.tamper):
                # A store assembled from tampered content hashes to what it holds,
                # exactly like a real one: the pin and the store then disagree.
                body = body + b" "
            digest = "sha256:" + sha256_bytes(body)
            (blobs / digest.split(":")[1]).write_bytes(body)
            for kind in ("config", "layer"):
                blob = blob_body(kind, repo)
                (blobs / sha256_bytes(blob)).write_bytes(blob)
            manifests.append({"mediaType": "application/vnd.oci.image.manifest.v1+json",
                              "digest": digest, "size": len(body),
                              "annotations": {"org.opencontainers.image.ref.name":
                                              hauler_ref.split("/", 1)[1]}})
        (self.store / "oci-layout").write_text('{"imageLayoutVersion":"1.0.0"}\n', encoding="utf-8")
        (self.store / "index.json").write_text(json.dumps(
            {"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json",
             "manifests": manifests}, indent=1), encoding="utf-8")

    def write_evidence(self, forged: bool = False) -> Path:
        self.evidence.mkdir(parents=True, exist_ok=True)
        for _original, hauler_ref in self.image_rows():
            repo = self.repo_of(hauler_ref)
            body = b'{"forged":true}' if forged else platform_manifest(repo)
            digest = sha256_bytes(platform_manifest(repo))
            (self.evidence / f"{digest}.json").write_bytes(body)
        return self.evidence

    # -- generated inputs ---------------------------------------------------
    def repo_of(self, hauler_ref: str) -> str:
        """The registry repository a hauler_ref names: no host, no tag."""
        without_host = hauler_ref.split("/", 1)[1] if "/" in hauler_ref else hauler_ref
        return without_host.rsplit(":", 1)[0]

    def image_rows(self) -> list[tuple[str, str]]:
        return [("docker.io/example/fixture:1", "127.0.0.1:5000/example/fixture:1")]

    def write_images_tsv(self) -> None:
        lines = ["original_ref\thauler_ref\tactual_digest\tuse_location"]
        for original, hauler_ref in self.image_rows():
            digest = "sha256:" + sha256_bytes(platform_manifest(self.repo_of(hauler_ref)))
            lines.append(f"{original}\t{hauler_ref}\t{digest}\tfixture")
        self.images_tsv.write_text("\n".join(lines) + "\n", encoding="utf-8")

    def write_lock(self) -> None:
        self.lock.write_text(
            "apiVersion: ani.installer/v1\n"
            "kind: ComponentMaterialLock\n"
            'lockedAt: "2026-09-26"\n'
            "lockBatch: T-F06\n"
            "tools:\n"
            "  helm:\n"
            "    version: v3.20.0\n"
            "    source: https://get.helm.sh/helm-v3.20.0-linux-amd64.tar.gz\n"
            f"    sourceTarballSha256: {'0' * 64}\n"
            f"    binarySha256: {sha256_file(self.helm)}\n"
            "    artifactPath: bin/helm\n"
            "  hauler:\n"
            "    version: v2.0.3\n"
            "    source: https://github.com/hauler-dev/hauler/releases/download/v2.0.3/hauler_2.0.3_linux_amd64.tar.gz\n"
            f"    sourceTarballSha256: {'1' * 64}\n"
            f"    binarySha256: {sha256_file(self.hauler)}\n"
            "    artifactPath: bin/hauler\n"
            "components:\n"
            "  cert-manager:\n"
            "    chartVersion: 1.0.0\n"
            f"    sha256: {sha256_file(self.chart_a)}\n"
            "    artifactChartPath: charts/cert-manager/1.0.0.tgz\n"
            "  nats:\n"
            "    chartVersion: 2.0.0\n"
            f"    sha256: {sha256_file(self.chart_b)}\n"
            "    artifactChartPath: charts/nats/2.0.0.tgz\n",
            encoding="utf-8")

    def build(self, name: str, mutate_env: dict | None = None) -> tuple[int, str, str, Path]:
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
            "HAULER_STORE": str(self.store),
            "PACKAGED_REGISTRY_ADDRESS": self.registry.address,
            "EVIDENCE_SOURCES": str(self.evidence),
            "MATERIALS_KK": str(MATERIALS_KK),
        })
        if mutate_env:
            env.update({key: value for key, value in mutate_env.items()})
        proc = subprocess.run(["bash", str(BUILD)], cwd=str(self.module), env=env,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
                              timeout=300)
        return proc.returncode, proc.stdout, proc.stderr, out

    def close(self) -> None:
        shutil.rmtree(self.dir, ignore_errors=True)


MATERIALS_KK: Path = Path()


def build_materials_kk() -> Path:
    """The material steps live in Go; the script calls this binary (F06)."""
    directory = Path(tempfile.mkdtemp(prefix="f06-kk-"))
    target = directory / "kk"
    proc = subprocess.run(["go", "build", "-tags", "builtin", "-o", str(target), "./cmd/kk"],
                          cwd=str(KUBEKEY), stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                          text=True, timeout=600)
    if proc.returncode != 0:
        raise SystemExit(f"cannot build the kk the material steps need: {proc.stderr[-2000:]}")
    return target


def shipped_paths(out_dir: Path) -> list[str]:
    return sorted(str(path.relative_to(out_dir)) for path in out_dir.rglob("*") if path.is_file())


def test_positive_build(res: Result, fixture: Fixture) -> None:
    print("正向构建：每项材料按自己的锁条目放置、落盘复读，镜像库过安装同款内容门")
    rc, out, err, out_dir = fixture.build("positive")
    if rc != 0:
        res.check("R07.2-positive-build", False, f"构建失败 rc={rc}\n{out[-3000:]}\n{err[-3000:]}")
        return
    res.check("R07.2-positive-build", True, "构建成功")

    paths = shipped_paths(out_dir)
    res.check("R07.2-charts",
              {"charts/cert-manager/1.0.0.tgz", "charts/nats/2.0.0.tgz"} <= set(paths),
              f"产物内的 chart 集合与锁一致（{paths}）")
    res.check("F06-iso-nested", "packages/kubekey-artifact.tgz" in paths
              and "repository/ubuntu-24.04-debs-amd64.iso" in paths,
              "ISO 既 loose 交付也在 artifact tarball 内")
    shipped_config = (out_dir / "config" / "package.yaml").read_text(encoding="utf-8")
    res.check("T-R07-06", shipped_config == fixture.config.read_text(encoding="utf-8"),
              "包内 config/package.yaml 与本次构建实际消费的 CONFIG 逐字节一致")

    record = (out_dir / "config" / "materials-source.txt").read_text(encoding="utf-8")
    res.check("T-R07-06-record",
              f"config.package.yaml sha256:{sha256_file(fixture.config)}" in record
              and f"bin.helm sha256:{sha256_file(out_dir / 'bin' / 'helm')}" in record
              and f"bin.hauler sha256:{sha256_file(out_dir / 'bin' / 'hauler')}" in record
              and record.count("the digest of THIS lock entry, re-read after placement") == 2
              and "NOT lock-approved" not in record,
              "materials-source.txt 记录两个落盘二进制摘要，且都对应自己的锁条目")
    # The package carries its own approved-source evidence: an offline target must be
    # able to re-derive why a packaged object is the pinned one.
    shipped_evidence = [name for name in paths if name.startswith("images/evidence/")]
    res.check("F06-evidence-shipped",
              "images/evidence/records.tsv" in shipped_evidence
              and any(name.endswith(".json") for name in shipped_evidence),
              f"包内交付了批准源 manifest 与关系记录（{shipped_evidence}）")

    evidence = (out_dir / "config" / "materials-verification.txt").read_text(encoding="utf-8")
    res.check("F06-evidence-chart-binding",
              "each bound to its own lock entry" in evidence and "re-read: sha256" in evidence,
              "验证证据逐条记录 chart 按条目绑定并落盘复读")
    res.check("F06-evidence-registry-gate",
              "packaged image docker.io/example/fixture:1" in evidence
              and "packaged image store verified" in evidence,
              "镜像库经过安装同款内容门，且报告写明批准来源")
    res.check("F06-evidence-iso",
              "verified: " in evidence and "repository/ubuntu-24.04-debs-amd64.iso" in evidence,
              "ISO 注入后从 tarball 内重新读取校验")

    sums = subprocess.run(["sha256sum", "-c", "SHA256SUMS"], cwd=str(out_dir),
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    res.check("R07.2-sums", sums.returncode == 0, "产物 SHA256SUMS 自校验通过")


def test_rejected_builds(res: Result, fixture_factory) -> None:
    print("T-R07-01/F06 负向：篡改、错目录、缺条目、错摘要、镜像内容不符都必须非零")
    def add_unknown_chart(case: "Fixture") -> None:
        (case.charts_dir / "extra").mkdir(parents=True, exist_ok=True)
        (case.charts_dir / "extra" / "9.9.9.tgz").write_bytes(chart_archive("extra", "9.9.9"))

    cases = {
        "corrupted-chart": lambda f: f.chart_a.write_bytes(b"tampered chart bytes\n"),
        "unknown-chart": add_unknown_chart,
        "wrong-helm-digest": lambda f: f.helm.write_bytes(b"helm binary with a different digest\n"),
        "wrong-iso-digest": lambda f: f.iso.write_bytes(b"iso bytes that do not match the record\n"),
        "lock-chart-missing": lambda f: f.chart_b.unlink(),
    }
    expected = {
        "corrupted-chart": "no chart archive under",
        "unknown-chart": "not the exact match of any lock entry",
        "wrong-helm-digest": "but the lock approves",
        "wrong-iso-digest": "source-side record says",
        "lock-chart-missing": "no chart archive under",
    }
    for name, mutate in cases.items():
        case = fixture_factory()
        try:
            mutate(case)
            rc, out, err, out_dir = case.build(f"rej-{name}")
            want = expected[name]
            combined = out + err
            res.check(f"T-R07-01-{name}",
                      rc != 0 and want in combined and not (out_dir / "config" / "materials-source.txt").exists(),
                      f"{name}: 非零且指出 {want!r}；materials-source 未写出（rc={rc}）")
        finally:
            case.close()


def test_chart_wrong_directory_repro(res: Result, fixture_factory) -> None:
    print("F06-2 复现：beta Chart 放进 alpha 目录，旧 grep 会退出 0；现在必须拒绝")

    # Variant A: the alpha directory now holds only beta's archive. The old script
    # matched "sha appears somewhere in the lock" and copied it to alpha's official
    # path with exit 0.
    case = fixture_factory()
    try:
        beta_bytes = case.chart_b.read_bytes()
        case.chart_a.unlink()
        (case.charts_dir / "cert-manager" / "moved-beta.tgz").write_bytes(beta_bytes)
        rc, out, err, out_dir = case.build("repro-a")
        combined = out + err
        res.check("F06-2a-foreign-archive-in-own-dir",
                  rc != 0 and "no chart archive under" in combined,
                  f"beta 档案冒充 alpha 必须非零（rc={rc}）")
        shipped = out_dir / "charts" / "cert-manager" / "1.0.0.tgz"
        res.check("F06-2a-no-landed-file", not shipped.exists(),
                  "拒绝的放置不得在 alpha 正式路径留下产物")
    finally:
        case.close()

    # Variant B: the lock's only chart entry approves beta's bytes under alpha's
    # name and path. The digest matches exactly one archive, so only the archive's
    # own Chart.yaml can refuse it — the per-entry identity binding.
    case = fixture_factory()
    try:
        beta_bytes = case.chart_b.read_bytes()
        case.chart_a.unlink()
        case.chart_b.unlink()
        (case.charts_dir / "cert-manager" / "1.0.0.tgz").write_bytes(beta_bytes)
        case.lock.write_text(
            "apiVersion: ani.installer/v1\n"
            "kind: ComponentMaterialLock\n"
            'lockedAt: "2026-09-26"\n'
            "lockBatch: T-F06\n"
            "tools:\n"
            "  helm:\n"
            "    version: v3.20.0\n"
            "    source: https://get.helm.sh/helm-v3.20.0-linux-amd64.tar.gz\n"
            f"    sourceTarballSha256: {'0' * 64}\n"
            f"    binarySha256: {sha256_file(case.helm)}\n"
            "    artifactPath: bin/helm\n"
            "  hauler:\n"
            "    version: v2.0.3\n"
            "    source: https://github.com/hauler-dev/hauler/releases/download/v2.0.3/hauler_2.0.3_linux_amd64.tar.gz\n"
            f"    sourceTarballSha256: {'1' * 64}\n"
            f"    binarySha256: {sha256_file(case.hauler)}\n"
            "    artifactPath: bin/hauler\n"
            "components:\n"
            "  cert-manager:\n"
            "    chartVersion: 1.0.0\n"
            f"    sha256: {sha256_bytes(beta_bytes)}\n"
            "    artifactChartPath: charts/cert-manager/1.0.0.tgz\n",
            encoding="utf-8")
        rc, out, err, out_dir = case.build("repro-b")
        combined = out + err
        res.check("F06-2b-declares-itself-otherwise",
                  rc != 0 and "declares itself nats 2.0.0" in combined,
                  "摘要对得上但 Chart.yaml 名实不符也必须拒绝（rc=%d）\n%s" % (rc, combined[-1500:]))
    finally:
        case.close()


def test_evidence_inputs_resisted(res: Result, fixture_factory) -> None:
    """B-1 negatives at the packaging boundary: no evidence, and evidence that is
    not a blob source. Both must refuse before anything ships."""
    case = fixture_factory()
    try:
        saved = case.evidence
        shutil.rmtree(saved)
        rc, out, err, out_dir = case.build("no-evidence")
        combined = out + err
        res.check("F06-no-evidence-refused",
                  rc != 0 and "no source store or evidence root carries blob" in combined
                  and not (out_dir / "images" / "images.haul.tar.zst").exists(),
                  f"证据根存在但没有 blob 时必须按 blob 拒绝（rc={rc}）\n{combined[-400:]}")
    finally:
        case.close()

    case = fixture_factory()
    try:
        env_saved = None
        rc, out, err, out_dir = case.build("missing-evidence-root", mutate_env={
            "EVIDENCE_SOURCES": str(case.dir / "does-not-exist")})
        combined = out + err
        res.check("F06-evidence-root-missing-refused",
                  rc != 0 and "EVIDENCE_SOURCES entry is not a directory" in combined,
                  f"EVIDENCE_SOURCES 指向不存在的目录必须拒绝（rc={rc}）\n{combined[-400:]}")
        del env_saved
    finally:
        case.close()


def test_image_store_content_gate(res: Result, fixture_factory, registry: Registry) -> None:
    print("F06-1/4 负向：镜像缺失、内容不符、层 blob 缺失，制包阶段就失败且不交付归档")
    cases = {
        "store-missing-image": ("absent", "returned HTTP 404"),
        "store-wrong-content": ("tamper", "does not match the pinned transport identity"),
        "store-missing-blob": ("drop_blobs", "blob sha256:"),
    }
    for name, (knob, want) in cases.items():
        getattr(registry, knob).add("example/fixture")
        try:
            case = fixture_factory()
            try:
                rc, out, err, out_dir = case.build(name)
                combined = out + err
                archive = out_dir / "images" / "images.haul.tar.zst"
                res.check(f"F06-{name}",
                          rc != 0 and want in combined and not archive.exists(),
                          f"{name}: 非零、指出 {want!r} 且未交付镜像归档（rc={rc}）")
            finally:
                case.close()
        finally:
            getattr(registry, knob).discard("example/fixture")


def test_nested_iso_by_content(res: Result, fixture_factory) -> None:
    print("F06-3 复现：tarball 内已有同名 ISO 但内容不同，必须按内容替换而非跳过")
    case = fixture_factory()
    try:
        wrong = b"a DIFFERENT repository iso that only matches by name\n"
        buffer = io.BytesIO()
        with gzip.GzipFile(fileobj=buffer, mode="wb", mtime=0) as gz:
            with tarfile.open(fileobj=gz, mode="w") as archive:
                for entry_name, body in (
                        ("bin/kk", b"fixture kubekey payload\n"),
                        ("repository/ubuntu-24.04-debs-amd64.iso", wrong)):
                    info = tarfile.TarInfo(entry_name)
                    info.size = len(body)
                    info.mode = 0o644
                    archive.addfile(info, io.BytesIO(body))
        case.kk_artifact.write_bytes(buffer.getvalue())
        rc, out, err, out_dir = case.build("iso-name-collision")
        combined = out + err
        res.check("F06-3-detected-by-content",
                  rc == 0 and "replacing that entry" in combined,
                  f"同名不同内容的 ISO 必须被识别并替换（rc={rc}）")
        if rc == 0:
            with tarfile.open(out_dir / "packages" / "kubekey-artifact.tgz", "r:gz") as archive:
                member = archive.extractfile("repository/ubuntu-24.04-debs-amd64.iso").read()
            res.check("F06-3-replaced-with-approved",
                      sha256_bytes(member) == sha256_file(case.iso),
                      "tarball 内的 ISO 现在是批准内容")
    finally:
        case.close()


def test_recomputed_sums_cannot_help(res: Result, fixture: Fixture) -> None:
    print("T-R07-05 重算包内 SHA256SUMS 也无法让篡改过的 chart 合格")
    rc, out, err, out_dir = fixture.build("attack")
    if rc != 0:
        res.check("T-R07-05", False, f"基线构建失败 rc={rc}：{err[-2000:]}")
        return
    shipped_chart = out_dir / "charts" / "cert-manager" / "1.0.0.tgz"
    shipped_chart.write_bytes(b"attacker content\n")
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
              "但独立批准锁里没有该哈希：安装前的内容门仍会拒绝")
    res.check("T-R07-05c",
              sha256_file(fixture.chart_a) in shipped_lock,
              "锁里仍是原始批准哈希，重建包无法替换它")


def main() -> int:
    global MATERIALS_KK
    if not BUILD.is_file():
        print(f"missing required file: {BUILD}", file=sys.stderr)
        return 2
    MATERIALS_KK = build_materials_kk()
    registry = Registry()
    registry.start()

    def factory() -> Fixture:
        return Fixture(registry)

    res = Result()
    fixture = factory()
    try:
        test_positive_build(res, fixture)
        test_rejected_builds(res, factory)
        test_chart_wrong_directory_repro(res, factory)
        test_image_store_content_gate(res, factory, registry)
        test_nested_iso_by_content(res, factory)
        test_recomputed_sums_cannot_help(res, factory())
    finally:
        fixture.close()
        registry.stop()
        shutil.rmtree(MATERIALS_KK.parent, ignore_errors=True)

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


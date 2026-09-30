#!/usr/bin/env python3
"""ANI image transfer helper. Python 3.9+, skopeo; Helm only for --with-charts.

Exports single-platform images with unmodified manifests to skopeo dir transport.
Does NOT deploy, change registries, download tools or send files to Fedora.
`push` is dry-run unless --execute; use only an approved writable registry.
"""
from __future__ import annotations
import argparse
import concurrent.futures
import datetime as dt
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile

HERE = Path(__file__).resolve().parent
DIGEST = re.compile(r"sha256:[0-9a-f]{64}\Z")
ID = re.compile(r"[a-z0-9][a-z0-9._-]{0,90}\Z")


def sha_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open('rb') as f:
        for b in iter(lambda: f.read(4 * 1024 * 1024), b''):
            h.update(b)
    return h.hexdigest()


def digest(b: bytes) -> str:
    return 'sha256:' + hashlib.sha256(b).hexdigest()


def write_json(path: Path, value) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + '.tmp')
    tmp.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    os.replace(tmp, path)


def read_json(path: Path):
    return json.loads(path.read_text(encoding='utf-8'))


def safe_ref(ref: str) -> str:
    # Accept registry image references, never shell commands, credential URLs or bare image IDs.
    if (not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._:/@+-]*', ref)
            or '://' in ref or ref.startswith('sha256:') or '..' in ref.split('/')):
        raise ValueError('非法/非仓库镜像引用: ' + ref)
    if '@' in ref:
        repo, d = ref.rsplit('@', 1)
        if not DIGEST.fullmatch(d) or '@' in repo:
            raise ValueError('仅支持完整 sha256 manifest 引用: ' + ref)
    return ref


def repository(ref: str) -> str:
    r = safe_ref(ref).split('@', 1)[0]
    if ':' in r.rsplit('/', 1)[-1]:
        r = r.rsplit(':', 1)[0]
    return r


def platform_parts(platform: str):
    a = platform.split('/')
    if len(a) not in (2, 3) or not all(re.fullmatch('[a-z0-9_-]+', s) for s in a):
        raise ValueError('platform 应为 linux/amd64 或 linux/arm64/v8')
    return a


def run(argv, timeout=1800, log: Path | None = None) -> bytes:
    """No shell; credentials are supplied by authfile, never CLI passwords."""
    try:
        p = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                           timeout=timeout, check=False)
    except subprocess.TimeoutExpired as e:
        if log:
            log.parent.mkdir(parents=True, exist_ok=True)
            log.write_bytes((e.stdout or b'') + (e.stderr or b'') + b'\nTIMEOUT\n')
        raise RuntimeError(f'{argv[0]} 超时；日志={log}') from e
    if log:
        log.parent.mkdir(parents=True, exist_ok=True)
        log.write_bytes(p.stdout + b'\n--- stderr ---\n' + p.stderr + f'\nrc={p.returncode}\n'.encode())
    if p.returncode:
        # Logs are private; avoid dumping arbitrary registry output/URLs to public reports.
        raise RuntimeError(f'{argv[0]} rc={p.returncode}; 日志={log or "未指定"}')
    return p.stdout


def registry_options(args, prefix=''):
    opts = ['--' + prefix + 'tls-verify=true']
    if args.authfile:
        opts += ['--' + prefix + 'authfile', str(Path(args.authfile).expanduser().resolve())]
    if args.cert_dir:
        opts += ['--' + prefix + 'cert-dir', str(Path(args.cert_dir).expanduser().resolve())]
    return opts


def resolve_image(row, args, log_dir):
    repo = repository(row['sourceRef'])
    src = row['sourceRef']
    chain = []
    selected = None
    for depth in range(5):
        raw = run(['skopeo', 'inspect', '--raw', '--retry-times', str(args.retries)]
                  + registry_options(args) + ['docker://' + src], args.timeout,
                  log_dir / f'manifest-{depth}.log')
        actual = digest(raw)
        if '@' in src and src.rsplit('@', 1)[1] != actual:
            raise ValueError('仓库返回内容与请求 manifest digest 不一致')
        doc = json.loads(raw)
        chain.append((raw, actual))
        if 'manifests' not in doc:
            if doc.get('schemaVersion') != 2 or 'config' not in doc or 'layers' not in doc:
                raise ValueError('仅支持 OCI / Docker schema2 镜像；不自动转换 schema1 或 Helm 工件')
            selected = actual
            break
        parts = platform_parts(args.platform)
        matches = [d for d in doc['manifests']
                   if d.get('platform', {}).get('os') == parts[0]
                   and d.get('platform', {}).get('architecture') == parts[1]
                   and (len(parts) == 2 or d.get('platform', {}).get('variant', '') == parts[2])]
        if len(matches) != 1:
            raise ValueError(f'{args.platform} 子 manifest 匹配数为 {len(matches)}，请明确平台；不下载所有架构')
        child = matches[0]['digest']
        if not DIGEST.fullmatch(child):
            raise ValueError('子 manifest 摘要不是受支持的 sha256')
        src = repo + '@' + child
    if not selected:
        raise ValueError('manifest index 嵌套过深')
    return repo + '@' + selected, chain


def verify_image(folder: Path, expected: str, platform: str):
    mf = folder / 'manifest.json'
    if mf.is_symlink() or not mf.is_file() or 'sha256:' + sha_file(mf) != expected:
        raise ValueError(f'manifest 摘要不符: {folder}')
    m = read_json(mf)
    if m.get('schemaVersion') != 2 or 'manifests' in m:
        raise ValueError('传输目录必须是单平台的 OCI/Docker schema2 image')
    for d in [m['config']] + m['layers']:
        if not DIGEST.fullmatch(d['digest']):
            raise ValueError('不支持的 blob 摘要')
        p = folder / d['digest'].split(':')[1]
        if p.is_symlink() or not p.is_file() or p.stat().st_size != d['size']:
            raise ValueError(f'blob 缺失/大小不符: {p.name}')
        if 'sha256:' + sha_file(p) != d['digest']:
            raise ValueError(f'blob 内容摘要不符: {p.name}')
    config = read_json(folder / m['config']['digest'].split(':')[1])
    parts = platform_parts(platform)
    if config.get('os') != parts[0] or config.get('architecture') != parts[1]:
        raise ValueError(f'镜像 config 平台不是 {platform}')
    if len(parts) == 3 and config.get('variant') not in (None, '', parts[2]):
        raise ValueError('镜像 variant 不符')
    return m['config']['digest']


def export_one(row, args, bundle: Path, work: Path):
    ident = row['id']
    dest = bundle / 'images' / ident
    logdir = work / 'logs' / ident
    if dest.exists():
        meta = read_json(dest / 'metadata.json')
        if meta['request'] != row or meta['platform'] != args.platform:
            raise ValueError(f'{ident}: 输入与已有缓存不同，请使用新 --out；不覆盖已导出材料')
        verify_image(dest / 'image', meta['manifestDigest'], args.platform)
        print(f'[复用已校验] {ident}', flush=True)
        return meta
    selected, chain = resolve_image(row, args, logdir)
    temp = Path(tempfile.mkdtemp(prefix=ident + '-', dir=work / 'partial'))
    image = temp / 'image'
    image.mkdir()
    run(['skopeo', 'copy', '--preserve-digests', '--retry-times', str(args.retries)]
        + registry_options(args, 'src-') + ['docker://' + selected, 'dir:' + str(image)],
        args.timeout, logdir / 'copy.log')
    selected_digest = selected.rsplit('@', 1)[1]
    config_digest = verify_image(image, selected_digest, args.platform)
    for n, (raw, _) in enumerate(chain):
        (temp / f'source-manifest-{n}.json').write_bytes(raw)
    known = {d for _, d in chain} | {config_digest}
    observations = []
    for observed in row.get('capturedRuntimeIDs', []):
        d = observed.rsplit('@', 1)[-1]
        relation = ('matches_config' if d == config_digest else
                    'matches_manifest' if d in known else 'not_matched_needs_review')
        observations.append({'captured': observed, 'relation': relation})
    meta = {'id': ident, 'request': row, 'platform': args.platform,
            'sourceDigest': chain[0][1], 'resolvedRef': selected,
            'manifestDigest': selected_digest, 'configDigest': config_digest,
            'captureComparisons': observations,
            'exportedAt': dt.datetime.now(dt.timezone.utc).isoformat()}
    write_json(temp / 'metadata.json', meta)
    os.replace(temp, dest)
    print(f'[导出] {ident} {selected_digest}', flush=True)
    return meta


def export_chart(row, args, bundle, work):
    ident = row['id']
    dest = bundle / 'charts' / ident
    if dest.exists():
        meta = read_json(dest / 'metadata.json')
        if meta['request'] != row or sha_file(dest / meta['file']) != meta['sha256']:
            raise ValueError(f'{ident}: Chart 缓存或输入不一致，请用新 --out')
        return meta
    temp = Path(tempfile.mkdtemp(prefix='chart-' + ident + '-', dir=work / 'partial'))
    run(['helm', 'pull', row['source'], '--version', row['version'], '--destination', str(temp)],
        args.timeout, work / 'logs' / ('chart-' + ident + '.log'))
    tgzs = list(temp.glob('*.tgz'))
    if len(tgzs) != 1:
        raise ValueError('helm pull 没有产生恰好一份 Chart')
    raw = run(['helm', 'show', 'chart', str(tgzs[0])], args.timeout).decode()
    def scalar(key):
        m = re.search(r'^' + key + r':\s*(.+?)\s*$', raw, re.MULTILINE)
        return m.group(1).strip('\"\'') if m else ''
    if scalar('name') != row['name'] or scalar('version').lstrip('v') != row['version'].lstrip('v'):
        raise ValueError('Chart.yaml name/version 与请求不符')
    (temp / 'Chart.metadata.yaml').write_text(raw)
    meta = {'id': ident, 'request': row, 'file': tgzs[0].name,
            'sha256': sha_file(tgzs[0]), 'digestBasis': 'downloaded-file-sha256-not-publisher-signature'}
    write_json(temp / 'metadata.json', meta)
    os.replace(temp, dest)
    print(f'[Chart] {ident} {row["version"]}', flush=True)
    return meta


def write_sums(bundle):
    paths = sorted(p for p in bundle.rglob('*') if p.is_file() and p.name != 'SHA256SUMS')
    lines = []
    for p in paths:
        if p.is_symlink():
            raise ValueError('归档中不允许符号链接')
        rel = p.relative_to(bundle).as_posix()
        if '\n' in rel or '\r' in rel:
            raise ValueError('非法归档文件名')
        lines.append(sha_file(p) + '  ' + rel)
    (bundle / 'SHA256SUMS').write_text('\n'.join(lines) + '\n')


def verify_bundle(bundle):
    root = bundle.resolve()
    seen = set()
    for line in (root / 'SHA256SUMS').read_text().splitlines():
        expected, rel = line.split('  ', 1)
        p = root / rel
        if (rel in seen or not re.fullmatch('[0-9a-f]{64}', expected)
                or Path(rel).is_absolute() or '..' in Path(rel).parts
                or p.is_symlink() or not p.is_file() or root not in p.resolve().parents):
            raise ValueError('SHA256SUMS 包含非法/重复/缺失路径: ' + rel)
        if sha_file(p) != expected:
            raise ValueError('SHA256SUMS 不匹配: ' + rel)
        seen.add(rel)
    actual = {p.relative_to(root).as_posix() for p in root.rglob('*') if p.is_file() and p.name != 'SHA256SUMS'}
    if seen != actual or 'bundle.json' not in seen:
        raise ValueError('清单与实际文件集合不一致')
    doc = read_json(root / 'bundle.json')
    if not doc['complete']:
        raise ValueError('这是 PARTIAL 包，请查看 failed.json；不能当作完整材料')
    for m in doc['images']:
        if not ID.fullmatch(m['id']):
            raise ValueError('非法 image id')
        verify_image(root / 'images' / m['id'] / 'image', m['manifestDigest'], doc['platform'])
    print(f'材料校验通过：{len(doc["images"])} 个镜像；这不是应用安装验收。', flush=True)
    return doc


def export(args):
    requested = read_json(Path(args.manifest))
    rows = requested['images']
    if args.only:
        ids = set(args.only.split(','))
        unknown = ids - {r['id'] for r in rows}
        if unknown:
            raise ValueError('未知 --only: ' + ','.join(sorted(unknown)))
        rows = [r for r in rows if r['id'] in ids]
    if not rows or len({r['id'] for r in rows}) != len(rows):
        raise ValueError('镜像列表为空或 id 重复')
    for r in rows:
        if not ID.fullmatch(r['id']): raise ValueError('非法 id')
        safe_ref(r['sourceRef'])
    platform_parts(args.platform)
    out = Path(args.out).expanduser().resolve()
    out.mkdir(parents=True, exist_ok=True)
    with (out / '.export.lock').open('a') as lock:
        try: fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError: raise ValueError('同一输出目录已有导出进程；不重复启动')
        bundle, work = out / 'ani-images-bundle', out / 'work'
        selection = {'platform': args.platform, 'images': rows,
                     'charts': read_json(Path(args.charts))['charts'] if args.with_charts else []}
        state = out / 'request.json'
        if state.exists() and read_json(state) != selection:
            raise ValueError('本输出目录请求不同；修改清单/平台/Charts选择后请使用新 --out')
        write_json(state, selection)
        for p in [bundle / 'images', bundle / 'charts', work / 'partial', work / 'logs']:
            p.mkdir(parents=True, exist_ok=True)
        for name in ['skopeo'] + (['helm'] if args.with_charts else []):
            if not shutil.which(name):
                raise ValueError(f'缺少 {name}；脚本不自动安装工具')
        results, failures = [], []
        with concurrent.futures.ThreadPoolExecutor(max_workers=args.jobs) as pool:
            futures = {pool.submit(export_one, r, args, bundle, work): r for r in rows}
            for future in concurrent.futures.as_completed(futures):
                r = futures[future]
                try: results.append(future.result())
                except Exception as e:
                    failures.append({'id': r['id'], 'sourceRef': r['sourceRef'], 'error': str(e)})
                    print(f'[失败，其他项继续] {r["id"]}: {e}', file=sys.stderr, flush=True)
        charts = []
        for r in selection['charts']:
            try: charts.append(export_chart(r, args, bundle, work))
            except Exception as e: failures.append({'id': 'chart:' + r['id'], 'error': str(e)})
        results.sort(key=lambda m: m['id'])
        warnings = [m['id'] for m in results if any(c['relation'] == 'not_matched_needs_review'
                    for c in m['captureComparisons'])]
        doc = {'formatVersion': 1, 'storageFormat': 'skopeo-dir', 'platform': args.platform,
               'complete': not failures, 'applicationReady': False,
               'requestedImageCount': len(rows), 'referenceSHA256': requested.get('referenceSHA256'),
               'images': results, 'chartsRequested': args.with_charts, 'charts': charts,
               'capturedRuntimeIdentityUnconfirmed': warnings,
               'notes': ['单平台 manifest/config/压缩层摘要分别保留；不是 docker save 或 Hauler archive。',
                         '裸 runtime imageID 不用作拉取地址；不匹配时材料仍保存但不得声称与参考运行版本相同。',
                         '镜像集合来自参考记录，最终 Chart 动态镜像和初始化工具仍须核对；不代表完整部署已验证。']}
        write_json(bundle / 'bundle.json', doc)
        write_json(bundle / 'request.json', selection)
        write_json(bundle / 'failed.json', failures)
        header = ['id', 'sourceRef', 'sourceDigest', 'manifestDigest', 'configDigest', 'platform', 'imageDir']
        lines = ['\t'.join(header)]
        for m in results:
            lines.append('\t'.join([m['id'], m['request']['sourceRef'], m['sourceDigest'],
                         m['manifestDigest'], m['configDigest'], m['platform'], 'images/' + m['id'] + '/image']))
        (bundle / 'images.lock.tsv').write_text('\n'.join(lines) + '\n')
        write_sums(bundle)
        name = 'ani-images-PARTIAL.tar.gz' if failures else 'ani-images-bundle.tar.gz'
        target = out / name
        tmp = target.with_name(target.name + '.part')
        with tarfile.open(tmp, 'w:gz', compresslevel=1, dereference=True) as t:
            t.add(bundle, arcname='ani-images-bundle')
        os.replace(tmp, target)
        Path(str(target) + '.sha256').write_text(sha_file(target) + '  ' + name + '\n')
        print(f'\n产物: {target}\n完整材料: {not failures}; 成功 {len(results)}/{len(rows)}; charts {len(charts)}', flush=True)
        if warnings:
            print('注意：参考 runtime ID 未匹配，需人工裁定，不能当作原运行版本: ' + ', '.join(warnings))
        if failures:
            print('同一命令可续跑；已完成镜像重新校验后复用，不重拉。查看 bundle/failed.json 和 work/logs。')
            return 2
        return 0


def push(args):
    bundle = Path(args.bundle).expanduser().resolve()
    doc = verify_bundle(bundle)
    prefix = args.registry.rstrip('/')
    if (not re.fullmatch(r'[a-z0-9][a-z0-9.:-]*/[a-z0-9][a-z0-9/_-]*', prefix)
            or any(x in ('', '.', '..') for x in prefix.split('/'))):
        raise ValueError('--registry 必须是 host[:port]/已创建项目，不含协议或凭据')
    mapping = []
    for m in doc['images']:
        repo = prefix + '/' + m['id']
        dst = repo + ':sha256-' + m['manifestDigest'].split(':')[1]
        mapping.append({'id': m['id'], 'sourceRefs': m['request']['specRefs'],
                        'targetTag': dst, 'targetDigestRef': repo + '@' + m['manifestDigest'],
                        'imageDir': 'images/' + m['id'] + '/image'})
    if not args.execute:
        print('只生成预览；未登录、未推送。确认目标是可写 Harbor/Registry 后加 --execute。')
        print(json.dumps(mapping, ensure_ascii=False, indent=2))
        return 0
    if not shutil.which('skopeo'): raise ValueError('Fedora 缺少 skopeo')
    result = Path(args.result).expanduser().resolve()
    if result == bundle or bundle in result.parents:
        raise ValueError('--result 必须在校验后的 bundle 目录外')
    result.mkdir(parents=True, exist_ok=True)
    completed = []
    for m in mapping:
        expected = m['targetDigestRef'].rsplit('@', 1)[1]
        run(['skopeo', 'copy', '--preserve-digests', '--retry-times', str(args.retries)]
            + registry_options(args, 'dest-')
            + ['dir:' + str(bundle / m['imageDir']), 'docker://' + m['targetTag']],
            args.timeout, result / (m['id'] + '.push.log'))
        raw = run(['skopeo', 'inspect', '--raw'] + registry_options(args)
                  + ['docker://' + m['targetDigestRef']], args.timeout,
                  result / (m['id'] + '.inspect.log'))
        if digest(raw) != expected: raise ValueError('目标仓库 manifest 摘要不一致: ' + m['id'])
        completed.append(m)
        write_json(result / 'image-map.partial.json', completed)
        print('[已推送并回读] ' + m['id'], flush=True)
    write_json(result / 'image-map.json', completed)
    print('完成。模板和动态镜像配置应使用 image-map.json 的 targetDigestRef；未运行任何 Kubernetes 命令。')
    return 0


def main():
    os.umask(0o077)
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest='command', required=True)
    e = sub.add_parser('export', help='联网下载/导出/压缩，用户手动上传 Fedora')
    e.add_argument('--manifest', default=str(HERE.parent / 'materials/images.request.json'))
    e.add_argument('--out', required=True)
    e.add_argument('--platform', default='linux/amd64')
    e.add_argument('--jobs', type=int, choices=range(1, 9), default=3)
    e.add_argument('--only', help='逗号分隔 id，仅导出选定子集；不代表整套材料齐全')
    e.add_argument('--with-charts', action='store_true', help='顺便 helm pull 参考版的三份业务网关 Chart')
    e.add_argument('--charts', default=str(HERE.parent / 'materials/charts.request.json'))
    e.set_defaults(func=export)
    v = sub.add_parser('verify', help='完全离线验证解包目录，不需 skopeo')
    v.add_argument('--bundle', required=True)
    v.set_defaults(func=lambda a: (verify_bundle(Path(a.bundle).expanduser()), 0)[1])
    u = sub.add_parser('push', help='Fedora 推入明确获准的可写本地 Registry，默认只预览')
    u.add_argument('--bundle', required=True)
    u.add_argument('--registry', required=True)
    u.add_argument('--execute', action='store_true')
    u.add_argument('--result', default='./ani-image-import-result')
    u.set_defaults(func=push)
    for parser in (e, u):
        parser.add_argument('--authfile', help='已登录的容器认证文件，绝不打包该文件')
        parser.add_argument('--cert-dir', help='仓库可信 CA/证书目录；不关闭 TLS 校验')
        parser.add_argument('--timeout', type=int, default=1800, help='单个外部命令超时秒数')
        parser.add_argument('--retries', type=int, default=2, help='skopeo 有界重试次数')
    args = p.parse_args()
    if hasattr(args, 'timeout') and (args.timeout <= 0 or not 0 <= args.retries <= 10):
        p.error('timeout 必须为正，retries 必须在 0..10')
    try: return args.func(args)
    except KeyboardInterrupt:
        print('已中断；保留缓存和局部输出。确认无残余任务后用同一命令继续。', file=sys.stderr)
        return 130
    except Exception as exc:
        print('ERROR: ' + str(exc), file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())

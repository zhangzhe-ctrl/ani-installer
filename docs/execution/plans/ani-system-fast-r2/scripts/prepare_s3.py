#!/usr/bin/env python3
"""Fedora-only dedicated RustFS user/policy; preserve base and Milvus identities.

Uses native signed admin API documented at
https://docs.rustfs.com/en/security-compliance/iam/policies .
Run under the existing cluster lock. The short-lived TLS port-forward closes
in finally. No root credential is placed into business manifests.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import selectors
import subprocess
import time
from urllib.parse import urlencode

NAME = 'ani-system-fast-20260930'
HOST = 'ani-rustfs-svc.ani-platform.svc'


def prepare(root, kubectl):
    private = root / 'private'
    runtime = json.loads((private / 'runtime.json').read_text())
    own = runtime['own_s3']
    data = json.loads(subprocess.run([str(kubectl), '-n', 'ani-platform', 'get', 'secret',
        'ani-rustfs-root', '-o', 'json'], check=True, capture_output=True).stdout)['data']
    username = base64.b64decode(data['RUSTFS_ACCESS_KEY']).decode()
    password = base64.b64decode(data['RUSTFS_SECRET_KEY']).decode()
    config = ('user = ' + json.dumps(username + ':' + password) + '\n'
              'aws-sigv4 = "aws:amz:us-east-1:s3"\n')
    log = (root / 'logs/s3-port-forward.log').open('a')
    forward = subprocess.Popen([str(kubectl), '-n', 'ani-platform', 'port-forward',
        'service/ani-rustfs-svc', ':9000', '--address', '127.0.0.1'], stdout=subprocess.PIPE, stderr=log)
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(forward.stdout, selectors.EVENT_READ)
            if not selector.select(timeout=30): raise RuntimeError('S3 port-forward did not become available')
            line = forward.stdout.readline().decode()
            log.write(line); log.flush()
            match = re.fullmatch(r'Forwarding from 127\.0\.0\.1:(\d+) -> 9000\s*', line)
            if not match: raise RuntimeError('S3 port-forward failed before its own listener was ready')
            port = match[1]
        def request(method, path, body=None):
            cmd = ['curl', '--silent', '--show-error', '--connect-timeout', '10', '--max-time', '60',
                '--cacert', str(private / 'access/rustfs-ca.pem'), '--config', '-',
                '--connect-to', HOST + ':9000:127.0.0.1:' + port, '-X', method,
                'https://' + HOST + ':9000/rustfs/admin/v3/' + path,
                '--write-out', '\n%{http_code}']
            # Keep even the dedicated user password in a private body file.
            body_file = private / 's3-request-body.json'
            if body is not None:
                body_file.write_text(json.dumps(body)); body_file.chmod(0o600)
                cmd += ['-H', 'Content-Type: application/json', '--data-binary', '@' + str(body_file)]
            result = subprocess.run(cmd, input=config.encode(), capture_output=True)
            (root / 'logs/s3-admin-curl.stderr').write_bytes(result.stderr)
            if result.returncode: raise RuntimeError('signed S3 admin request failed; rc=' + str(result.returncode))
            payload, status = result.stdout.rsplit(b'\n', 1)
            (private / 's3-last-response.json').write_bytes(payload)
            return int(status), payload
        marker = private / 's3-task-identity.json'
        query = urlencode({'accessKey': own['access_key']})
        status, payload = request('GET', 'user-info?' + query)
        if status == 200 and not marker.exists():
            raise RuntimeError('S3 identity exists without task creation record; no takeover')
        if status != 200:
            if status not in (400, 404) or b'NoSuchUser' not in payload:
                raise RuntimeError('S3 user lookup failed; HTTP ' + str(status))
            status, _ = request('PUT', 'add-user?' + query, {'secretKey': own['secret_key'], 'status': 'enabled'})
            if status != 200: raise RuntimeError('dedicated S3 user creation failed; HTTP ' + str(status))
            marker.write_text(json.dumps({'name': NAME, 'accessKey': own['access_key']})); marker.chmod(0o600)
        policy = {'Version': '2012-10-17', 'Statement': [
            {'Effect': 'Allow', 'Action': ['s3:*'], 'Resource': ['arn:aws:s3:::ani-fast-*', 'arn:aws:s3:::ani-fast-*/*']}]}
        digest = hashlib.sha256(json.dumps(policy, sort_keys=True).encode()).hexdigest()
        policy_marker = private / 's3-task-policy.sha256'
        status, payload = request('GET', 'info-canned-policy?' + urlencode({'name': NAME}))
        if status == 200 and not policy_marker.exists():
            raise RuntimeError('S3 policy exists without task creation record; no takeover')
        if status != 200:
            status, _ = request('PUT', 'add-canned-policy?' + urlencode({'name': NAME}), policy)
            if status != 200: raise RuntimeError('dedicated S3 policy creation failed; HTTP ' + str(status))
            policy_marker.write_text(digest + '\n')
        if policy_marker.read_text().strip() != digest: raise RuntimeError('task S3 policy identity changed')
        status, _ = request('POST', 'idp/builtin/policy/attach', {'policies': [NAME], 'user': own['access_key']})
        if status != 200: raise RuntimeError('dedicated S3 policy attachment failed; HTTP ' + str(status))
        print('Dedicated S3 identity and ani-fast-* bucket policy prepared; base credentials unchanged')
    finally:
        forward.terminate()
        try: forward.wait(timeout=10)
        except subprocess.TimeoutExpired: forward.kill(); forward.wait()
        log.close()


if __name__ == '__main__':
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, required=True)
    parser.add_argument('--kubectl', type=Path, required=True)
    args = parser.parse_args()
    prepare(args.root, args.kubectl)

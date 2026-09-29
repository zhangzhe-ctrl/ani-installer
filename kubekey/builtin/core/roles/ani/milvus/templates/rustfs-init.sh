#!/usr/bin/env bash
# Create only the fixed Milvus bucket and its restricted RustFS identity.
# Reuse requires the exact bucket marker, parent identity, policy and secret.
set -euo pipefail
umask 077
KUBECONFIG_FILE="${ANI_RUN_KUBECONFIG:?}"
OUT_DIR="${ANI_RUN_LOGS_DIR:?}"
test -f "$KUBECONFIG_FILE"
install -d -m 0700 "$OUT_DIR"
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_FILE")
RC='{{ .ani.artifact_root }}/bin/rc'
test -x "$RC"
NS='{{ .ani.objectStorage.milvus_s3.secret_namespace }}'
OWNER='{{ .kubernetes.cluster_name }}'
BUCKET='{{ .ani.objectStorage.milvus_s3.bucket }}'
ROOT_PATH='{{ .ani.objectStorage.milvus_s3.root_path }}'
APP_SECRET='{{ .ani.objectStorage.milvus_s3.secret_name }}'
APP_KEY=ani-milvus-app
work="$(mktemp -d "$OUT_DIR/rustfs-init.XXXXXX")"
pf_pid=''
cleanup() {
  if [ -n "$pf_pid" ]; then kill "$pf_pid" 2>/dev/null || true; wait "$pf_pid" 2>/dev/null || true; fi
  rm -rf "$work"
}
trap cleanup EXIT

for ref in secret/ani-rustfs-root configmap/ani-rustfs-ca certificate/ani-rustfs-server \
           deployment/ani-rustfs service/ani-rustfs-svc; do
  "${KUBECTL[@]}" -n "$NS" get "$ref" >/dev/null
  ref_owner="$("${KUBECTL[@]}" -n "$NS" get "$ref" -o jsonpath='{.metadata.labels.ani\.io/managed-by}')"
  [ "$ref_owner" = "$OWNER" ] || { echo "foreign $NS/$ref" >&2; exit 1; }
done
existing_secret="$("${KUBECTL[@]}" -n "$NS" get "secret/$APP_SECRET" --ignore-not-found -o name)"
if [ -n "$existing_secret" ]; then
  secret_owner="$("${KUBECTL[@]}" -n "$NS" get "secret/$APP_SECRET" -o jsonpath='{.metadata.labels.ani\.io/managed-by}')"
  secret_type="$("${KUBECTL[@]}" -n "$NS" get "secret/$APP_SECRET" -o jsonpath='{.type}')"
  [ "$secret_owner" = "$OWNER" ] && [ "$secret_type" = Opaque ] || {
    echo "foreign or incompatible $NS/secret/$APP_SECRET" >&2; exit 1;
  }
fi
"${KUBECTL[@]}" -n "$NS" get secret/ani-rustfs-root -o jsonpath='{.data.RUSTFS_ACCESS_KEY}' | base64 -d > "$work/root-access"
"${KUBECTL[@]}" -n "$NS" get secret/ani-rustfs-root -o jsonpath='{.data.RUSTFS_SECRET_KEY}' | base64 -d > "$work/root-secret"
"${KUBECTL[@]}" -n "$NS" get configmap/ani-rustfs-ca -o jsonpath='{.data.ca\.crt}' > "$work/ca.crt"
test -s "$work/ca.crt"
root_access="$(cat "$work/root-access")"
root_secret="$(cat "$work/root-secret")"
[[ "$root_access" =~ ^[0-9a-f]{32}$ && "$root_secret" =~ ^[0-9a-f]{64}$ ]] || {
  echo 'RustFS root identity does not match the installer-created format' >&2; exit 1;
}

"${KUBECTL[@]}" -n "$NS" port-forward service/ani-rustfs-svc :9000 --address 127.0.0.1 > "$work/port-forward.log" 2>&1 &
pf_pid=$!
port=''
for _ in $(seq 1 20); do
  if ! kill -0 "$pf_pid" 2>/dev/null; then cat "$work/port-forward.log" >&2; exit 1; fi
  port="$(sed -nE 's/.*Forwarding from 127\.0\.0\.1:([0-9]+) -> 9000.*/\1/p' "$work/port-forward.log" | head -1)"
  if [ -n "$port" ]; then break; fi
  sleep 1
done
[ -n "$port" ] || { echo 'RustFS port-forward did not become ready' >&2; exit 1; }
curl --fail --silent --show-error --max-time 20 --cacert "$work/ca.crt" "https://127.0.0.1:$port/health/ready" > "$work/health-response"

python3 - "$work" "$OWNER" "$BUCKET" "$ROOT_PATH" <<'PY'
import hashlib, hmac, json, sys
from pathlib import Path
p = Path(sys.argv[1])
owner, bucket, root_path = sys.argv[2:]
root_secret = p.joinpath('root-secret').read_bytes().strip()
# RustFS 1.0.0 rejects a 64-character service secret; 40 hex characters
# retain 160 bits and are reproducible if account creation preceded a crash.
app_secret = hmac.new(root_secret, f'ani-rustfs-milvus-v1:{owner}'.encode(), hashlib.sha256).hexdigest()[:40]
p.joinpath('app-secret').write_text(app_secret)
policy = {'Version': '2012-10-17', 'Statement': [
    {'Effect': 'Allow', 'Action': ['s3:GetBucketLocation', 's3:ListBucket', 's3:ListBucketMultipartUploads'], 'Resource': [f'arn:aws:s3:::{bucket}']},
    {'Effect': 'Allow', 'Action': ['s3:GetObject', 's3:PutObject', 's3:DeleteObject', 's3:AbortMultipartUpload', 's3:ListMultipartUploadParts'], 'Resource': [f'arn:aws:s3:::{bucket}/{root_path}/*']},
]}
p.joinpath('policy.json').write_text(json.dumps(policy, separators=(',', ':')))
p.joinpath('owner.json').write_text(json.dumps({'schema': 1, 'owner': owner, 'provider': 'rustfs', 'bucket': bucket, 'rootPath': root_path}, separators=(',', ':')))
PY
app_secret="$(cat "$work/app-secret")"
if [ -n "$existing_secret" ]; then
  "${KUBECTL[@]}" -n "$NS" get "secret/$APP_SECRET" -o 'jsonpath={.data.AWS_ACCESS_KEY_ID}' | base64 -d > "$work/existing-access"
  "${KUBECTL[@]}" -n "$NS" get "secret/$APP_SECRET" -o 'jsonpath={.data.AWS_SECRET_ACCESS_KEY}' | base64 -d > "$work/existing-secret"
  [ "$(cat "$work/existing-access")" = "$APP_KEY" ] && cmp -s "$work/app-secret" "$work/existing-secret" || {
    echo 'existing RustFS Milvus Secret differs; refusing credential rotation' >&2; exit 1;
  }
fi

export RC_CONFIG_DIR="$work/rc"
install -d -m 0700 "$RC_CONFIG_DIR"
{
  printf 'schema_version = 1\n[[aliases]]\nname = "root"\nendpoint = "https://127.0.0.1:%s"\n' "$port"
  printf 'access_key = "%s"\nsecret_key = "%s"\n' "$root_access" "$root_secret"
  printf 'region = "us-east-1"\nsignature = "v4"\nbucket_lookup = "path"\ninsecure = false\nca_bundle = "%s"\n' "$work/ca.crt"
  printf '[[aliases]]\nname = "app"\nendpoint = "https://127.0.0.1:%s"\n' "$port"
  printf 'access_key = "%s"\nsecret_key = "%s"\n' "$APP_KEY" "$app_secret"
  printf 'region = "us-east-1"\nsignature = "v4"\nbucket_lookup = "path"\ninsecure = false\nca_bundle = "%s"\n' "$work/ca.crt"
} > "$RC_CONFIG_DIR/config.toml"
chmod 0600 "$RC_CONFIG_DIR/config.toml"
"$RC" ready root > "$work/ready.log"
"$RC" --json bucket list root/ > "$work/buckets.json"
python3 - "$work/buckets.json" "$BUCKET" "$work/bucket-exists" <<'PY'
import json, sys
items = json.load(open(sys.argv[1]))['items']
assert all(isinstance(item.get('key'), str) for item in items)
open(sys.argv[3], 'w').write('yes' if sys.argv[2] in [item['key'] for item in items] else 'no')
PY
set +e
"$RC" --json admin access-key info root "$APP_KEY" > "$work/access-info.json" 2> "$work/access-info.err"
account_rc=$?
set -e
if [ "$account_rc" -ne 0 ] && [ "$account_rc" -ne 5 ]; then
  echo "RustFS access-key lookup failed rc=$account_rc" >&2; exit "$account_rc"
fi
if [ "$(cat "$work/bucket-exists")" = yes ]; then
  "$RC" object show "root/$BUCKET/ani-installer/owner.json" > "$work/existing-owner.json" || {
    echo 'existing RustFS Milvus bucket lacks an installer ownership marker' >&2; exit 1;
  }
  cmp -s "$work/owner.json" "$work/existing-owner.json" || {
    echo 'existing RustFS Milvus bucket has a different owner or binding' >&2; exit 1;
  }
elif [ "$account_rc" -eq 0 ] || [ -n "$existing_secret" ]; then
  echo 'RustFS Milvus identity exists but its owned bucket is missing' >&2; exit 1
fi
if [ "$account_rc" -eq 0 ]; then
  python3 - "$work/access-info.json" "$work/policy.json" "$APP_KEY" "$root_access" <<'PY'
import json, sys
account, expected = json.load(open(sys.argv[1])), json.load(open(sys.argv[2]))
assert account.get('accessKey') == sys.argv[3] and account.get('parentUser') == sys.argv[4], 'foreign RustFS service account'
assert account.get('userType') == 'Service Account' and account.get('accountStatus') == 'on', 'inactive or foreign RustFS service account'
assert account.get('impliedPolicy') is False and json.loads(account['policy']) == expected, 'RustFS service account policy differs'
PY
  "$RC" --json bucket list "app/$BUCKET/$ROOT_PATH/" > "$work/app-list.json" || {
    echo 'retained RustFS Milvus credentials cannot access their bucket' >&2; exit 1;
  }
elif [ -n "$existing_secret" ]; then
  echo 'RustFS Milvus Secret exists without its service account; refusing rotation' >&2; exit 1
fi

if [ "$(cat "$work/bucket-exists")" = no ]; then
  "$RC" bucket create "root/$BUCKET" --region us-east-1 > "$work/bucket-create.log"
  "$RC" put "$work/owner.json" "root/$BUCKET/ani-installer/owner.json" --overwrite false > "$work/owner-put.log"
fi
if [ "$account_rc" -eq 5 ]; then
  "$RC" --json admin service-account create root "$APP_KEY" "$app_secret" --policy "$work/policy.json" --name ani-milvus-app > "$work/account-create.json"
  python3 - "$work/account-create.json" "$APP_KEY" "$work/app-secret" <<'PY'
import json, sys
created = json.load(open(sys.argv[1]))
assert created.get('success') is True and created.get('access_key') == sys.argv[2]
assert created.get('secret_key') == open(sys.argv[3]).read()
PY
fi
"$RC" --json admin access-key info root "$APP_KEY" > "$work/access-final.json"
python3 - "$work/access-final.json" "$work/policy.json" "$APP_KEY" "$root_access" <<'PY'
import json, sys
account, expected = json.load(open(sys.argv[1])), json.load(open(sys.argv[2]))
assert account.get('accessKey') == sys.argv[3] and account.get('parentUser') == sys.argv[4]
assert account.get('userType') == 'Service Account' and account.get('accountStatus') == 'on'
assert account.get('impliedPolicy') is False and json.loads(account['policy']) == expected
PY
"$RC" --json bucket list "app/$BUCKET/$ROOT_PATH/" > "$work/app-list-final.json"
if [ -z "$existing_secret" ]; then
  {
    printf 'apiVersion: v1\nkind: Secret\nmetadata:\n  name: %s\n  namespace: %s\n  labels:\n    ani.io/managed-by: %s\ntype: Opaque\ndata:\n' "$APP_SECRET" "$NS" "$OWNER"
    printf '  AWS_ACCESS_KEY_ID: %s\n' "$(printf '%s' "$APP_KEY" | base64 -w0)"
    printf '  AWS_SECRET_ACCESS_KEY: %s\n' "$(printf '%s' "$app_secret" | base64 -w0)"
  } > "$work/app-secret.yaml"
  "${KUBECTL[@]}" create -f "$work/app-secret.yaml" >/dev/null
fi
printf 'ANI-RUSTFS-MILVUS-BINDING-OK bucket=%s rootPath=%s secret=%s/%s\n' "$BUCKET" "$ROOT_PATH" "$NS" "$APP_SECRET"

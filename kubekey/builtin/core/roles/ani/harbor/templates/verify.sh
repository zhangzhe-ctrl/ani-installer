#!/usr/bin/env bash
# B07's real Harbor API and runtime check. A failure leaves owned project,
# credentials, Pods and reports in place for diagnosis; there is no retry loop
# that silently replaces a failed install.
set -euo pipefail
k() { kubectl --kubeconfig '{{ .ani.run.kubeconfig }}' "$@"; }
DIR=/etc/kubernetes/ani/harbor
EVID='{{ .ani.run.logs_dir }}/b07-harbor'
mkdir -p "$EVID"
chmod 0700 "$EVID"
ADDR='{{ (index .ani.components "harbor").external_address }}'
BASE="https://$ADDR:30003"
API="$BASE/api/v2.0"
CA="$DIR/ca.crt"
HOSTS=/etc/containerd/certs.d
command -v python3 >/dev/null
command -v ctr >/dev/null
command -v curl >/dev/null
printf 'machine %s login admin password %s\n' "$ADDR" "$(cat "$DIR/admin-password")" > "$DIR/admin.netrc"
chmod 0600 "$DIR/admin.netrc"
admin_curl() { curl --silent --show-error --cacert "$CA" --netrc-file "$DIR/admin.netrc" "$@"; }

# Prove the packaged database is mounted in the scanner before exercising API.
for spec in \
  'db/trivy.db 4bf01c98f9af59d4ec9ab6c8f4f22740230830e8be9172970e74dcb8257cd53b' \
  'db/metadata.json eec14e933a21b2dba78c45d2c0de61a6408e14cee008a07908079e4cd6c905d7' \
  'java-db/trivy-java.db e99e2d1212f4f1ef8281f95ffa72b667ce3fb8d00db0c4b2ddfcc3283b87612c' \
  'java-db/metadata.json f730c0616742e306095594ef51359f81cd05e48cff224112a59c8fbd68449e31'; do
  set -- $spec
  actual="$(k -n ani-harbor exec statefulset/ani-harbor-trivy -c trivy -- sha256sum "/home/scanner/.cache/trivy/$1" | awk '{print $1}')"
  [ "$actual" = "$2" ] || { echo "live scanner cache $1 digest mismatch: $actual" >&2; exit 1; }
done

PROJECT=ani-b07-test
cat > "$DIR/project.json" <<'PROJECT_JSON'
{"project_name":"ani-b07-test","metadata":{"public":"false","auto_scan":"false","prevent_vul":"true","severity":"high"}}
PROJECT_JSON
code="$(admin_curl -o "$DIR/project-create-response.json" -w '%{http_code}' \
  -H 'Content-Type: application/json' --data-binary @"$DIR/project.json" "$API/projects")"
[ "$code" = 201 ] || { echo "Harbor project creation HTTP $code" >&2; exit 1; }
anonymous="$(curl --silent --show-error --cacert "$CA" -o /dev/null -w '%{http_code}' \
  "$BASE/v2/$PROJECT/busybox/manifests/1.37.0")"
case "$anonymous" in 401|403) ;; *) echo "private Harbor repository anonymous access HTTP $anonymous" >&2; exit 1;; esac

cat > "$DIR/robot-push-request.json" <<'PUSH_JSON'
{"name":"ani-b07-push","description":"B07 own digest verification","level":"project","duration":1,"permissions":[{"kind":"project","namespace":"ani-b07-test","access":[{"resource":"repository","action":"pull","effect":"allow"},{"resource":"repository","action":"push","effect":"allow"}]}]}
PUSH_JSON
cat > "$DIR/robot-pull-request.json" <<'PULL_JSON'
{"name":"ani-b07-pull","description":"ANI project read-only registry access","level":"project","duration":365,"permissions":[{"kind":"project","namespace":"ani-b07-test","access":[{"resource":"repository","action":"pull","effect":"allow"}]}]}
PULL_JSON
for role in push pull; do
  code="$(admin_curl -o "$DIR/robot-$role-created.json" -w '%{http_code}' \
    -H 'Content-Type: application/json' --data-binary @"$DIR/robot-$role-request.json" "$API/robots")"
  [ "$code" = 201 ] || { echo "Harbor $role robot creation HTTP $code" >&2; exit 1; }
  chmod 0600 "$DIR/robot-$role-created.json"
done
python3 - "$DIR" "$ADDR" <<'PY'
import base64,json,os,sys
directory,host=sys.argv[1:]
for role in ("push","pull"):
    with open(f"{directory}/robot-{role}-created.json") as f: data=json.load(f)
    name,secret=data.get("name"),data.get("secret")
    if not name or not secret: raise SystemExit(f"robot {role} lacks returned name or secret")
    for field,value in (("name",name),("secret",secret)):
        path=f"{directory}/robot-{role}-{field}"
        fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
        with os.fdopen(fd,"w") as f:f.write(value)
with open(f"{directory}/robot-pull-created.json") as f: pull=json.load(f)
auth=base64.b64encode(f"{pull['name']}:{pull['secret']}".encode()).decode()
cfg={"auths":{f"{host}:30003":{"username":pull["name"],"password":pull["secret"],"auth":auth}}}
path=f"{directory}/pull-dockerconfig.json"
fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
with os.fdopen(fd,"w") as f:json.dump(cfg,f)
PY
k -n ani-harbor create secret generic ani-harbor-pull \
  --type=kubernetes.io/dockerconfigjson \
  --from-file=.dockerconfigjson="$DIR/pull-dockerconfig.json" \
  --dry-run=client -o yaml | k apply --server-side -f -

SOURCE='{{ .ani.registry }}/library/busybox:1.37.0'
TARGET="$ADDR:30003/$PROJECT/busybox:1.37.0"
DENIED="$ADDR:30003/$PROJECT/busybox:unauthorized-check"
PUSH_USER="$(cat "$DIR/robot-push-name")"
PUSH_SECRET="$(cat "$DIR/robot-push-secret")"
PULL_USER="$(cat "$DIR/robot-pull-name")"
PULL_SECRET="$(cat "$DIR/robot-pull-secret")"
ctr -n k8s.io images pull --plain-http "$SOURCE" > "$EVID/bootstrap-pull.log" 2>&1
ctr -n k8s.io images tag --force "$SOURCE" "$TARGET" > "$EVID/local-tag.log" 2>&1
ctr -n k8s.io images push --hosts-dir "$HOSTS" \
  --user "$PUSH_USER:$PUSH_SECRET" "$TARGET" > "$EVID/harbor-push.log" 2>&1
admin_curl -o "$EVID/artifact-before-pull.json" \
  "$API/projects/$PROJECT/repositories/busybox/artifacts/1.37.0?with_scan_overview=true"
DIGEST="$(python3 - "$EVID/artifact-before-pull.json" <<'PY'
import json,sys
with open(sys.argv[1]) as f: obj=json.load(f)
digest=obj.get("digest","")
if not digest.startswith("sha256:"): raise SystemExit("Harbor artifact has no SHA256 digest")
print(digest)
PY
)"
# Project policy blocks unscanned image pulls; scan before the first pull.
code="$(admin_curl -o "$EVID/scan-request.json" -w '%{http_code}' \
  -X POST -H 'Content-Type: application/json' \
  "$API/projects/$PROJECT/repositories/busybox/artifacts/1.37.0/scan")"
[ "$code" = 202 ] || { echo "Harbor scan request HTTP $code" >&2; exit 1; }
deadline=$((SECONDS + 900))
scan_state=pending
while [ "$SECONDS" -lt "$deadline" ]; do
  admin_curl -o "$EVID/artifact-scan.json" \
    "$API/projects/$PROJECT/repositories/busybox/artifacts/1.37.0?with_scan_overview=true"
  if python3 - "$EVID/artifact-scan.json" <<'PY'
import json,sys
with open(sys.argv[1]) as f: data=json.load(f)
summaries=(data.get("scan_overview") or {}).values()
if any(x.get("scan_status")=="Success" and x.get("report_id") for x in summaries): sys.exit(0)
if any(x.get("scan_status") in ("Error","Stopped","Failed") for x in summaries): sys.exit(20)
sys.exit(10)
PY
  then scan_state=success; break; else rc=$?; fi
  [ "$rc" -ne 20 ] || { echo 'Harbor scan reached a failure terminal state' >&2; exit 1; }
  sleep 10
done
[ "$scan_state" = success ] || { echo 'Harbor scan did not reach Success with report ID before deadline' >&2; exit 1; }
code="$(admin_curl -o "$EVID/vulnerabilities.json" -w '%{http_code}' \
  "$API/projects/$PROJECT/repositories/busybox/artifacts/1.37.0/additions/vulnerabilities")"
[ "$code" = 200 ] || { echo "Harbor vulnerability report HTTP $code" >&2; exit 1; }
python3 - "$EVID/vulnerabilities.json" <<'PY'
import json,sys
with open(sys.argv[1]) as f: report=json.load(f)
if not isinstance(report,dict) or not report: raise SystemExit("empty or invalid Harbor vulnerability report")
PY

# The private Harbor reference is removed locally before the authenticated
# pull. Source/bootstrap references and their content blobs are preserved.
ctr -n k8s.io images rm "$TARGET" > "$EVID/harbor-local-untag.log" 2>&1
ctr -n k8s.io images pull --hosts-dir "$HOSTS" \
  --user "$PULL_USER:$PULL_SECRET" "$TARGET" > "$EVID/harbor-pull.log" 2>&1
LOCAL_DIGEST="$(ctr -n k8s.io images ls | awk -v ref="$TARGET" '$1==ref {print $3}')"
[ "$LOCAL_DIGEST" = "$DIGEST" ] || { echo "Harbor push/pull digest mismatch: $LOCAL_DIGEST vs $DIGEST" >&2; exit 1; }
ctr -n k8s.io images tag --force "$SOURCE" "$DENIED" > "$EVID/denied-local-tag.log" 2>&1
if ctr -n k8s.io images push --hosts-dir "$HOSTS" \
    --user "$PULL_USER:$PULL_SECRET" "$DENIED" > "$EVID/denied-push.log" 2>&1; then
  echo 'pull-only robot could push; refusing B07 pass' >&2
  exit 1
fi
grep -Eiq 'denied|forbidden|insufficient_scope|unauthorized' "$EVID/denied-push.log" || {
  echo 'pull-only push failed for an unclassified reason' >&2; exit 1;
}
ctr -n k8s.io images rm "$DENIED" >/dev/null 2>&1

# The Pods use the real CRI on each declared node, an Always pull of the new
# Harbor ref, and a project-scoped pull-only imagePullSecret.
k apply --server-side -f "$DIR/runtime-pods.yaml"
for node in {{ range .ani.nodes }}{{ . }} {{ end }}; do
  pod="ani-b07-runtime-$node"
  k -n ani-harbor wait --for=jsonpath='{.status.phase}'=Succeeded "pod/$pod" --timeout=600s
  imageid="$(k -n ani-harbor get pod "$pod" -o jsonpath='{.status.containerStatuses[0].imageID}')"
  case "$imageid" in *"$DIGEST"*) ;; *) echo "$pod runtime image digest mismatch: $imageid" >&2; exit 1;; esac
  k -n ani-harbor logs "$pod" | grep -qx 'ANI_B07_RUNTIME_PULL_OK'
  printf '%s %s\n' "$pod" "$imageid" >> "$EVID/runtime-imageids.txt"
done

k -n ani-harbor delete pod -l app=ani-b07-runtime-check --wait=true --timeout=180s
printf 'B07 PASS: project=%s digest=%s scanner=%s report_sha256=%s\n' \
  "$PROJECT" "$DIGEST" 'Harbor-Trivy-offline' "$(sha256sum "$EVID/vulnerabilities.json" | awk '{print $1}')" \
  | tee "$EVID/result.txt"

#!/usr/bin/env bash
set -euo pipefail
ART='{{ .ani.artifact_root }}'
DIR=/etc/kubernetes/ani/harbor
export KUBECONFIG='{{ .ani.run.kubeconfig }}'
"$ART/bin/helm" template ani-harbor "$ART/charts/harbor/1.19.2.tgz" \
  --namespace ani-harbor --kube-version 1.35.8 --values "$DIR/values.yaml" \
  --set-file database.internal.password="$DIR/db-password" \
  --set-file registry.credentials.password="$DIR/registry-password" --include-crds > "$DIR/rendered.yaml"
# Helm's htpasswd function can render an error string without failing the
# template. Reject that output before any scanner or Harbor workload is made.
mapfile -t htpasswd_rows < <(awk '$1=="REGISTRY_HTPASSWD:" {gsub(/"/, "", $2); print $2}' "$DIR/rendered.yaml")
[ "${#htpasswd_rows[@]}" -eq 1 ] || { echo 'Harbor registry htpasswd Secret missing or duplicated' >&2; exit 1; }
registry_htpasswd="$(printf '%s' "${htpasswd_rows[0]}" | base64 -d)" || { echo 'Harbor registry htpasswd encoding invalid' >&2; exit 1; }
if ! printf '%s\n' "$registry_htpasswd" | grep -Eq '^harbor_registry_user:\$2[aby]\$[0-9]{2}\$[./A-Za-z0-9]{53}$'; then
  echo 'Harbor Chart did not render a bcrypt registry htpasswd' >&2; exit 1
fi
unset registry_htpasswd htpasswd_rows
echo 'Harbor registry htpasswd bcrypt format verified'
awk '$1=="image:" && $2!="" {gsub(/"/, "", $2); print $2}' "$DIR/rendered.yaml" | sort -u > "$DIR/rendered-images.txt"
cat > "$DIR/expected-images.txt" <<'IMAGES'
{{ .ani.registry }}/goharbor/nginx-photon:v2.15.2
{{ .ani.registry }}/goharbor/harbor-portal:v2.15.2
{{ .ani.registry }}/goharbor/harbor-core:v2.15.2
{{ .ani.registry }}/goharbor/harbor-jobservice:v2.15.2
{{ .ani.registry }}/goharbor/registry-photon:v2.15.2
{{ .ani.registry }}/goharbor/harbor-registryctl:v2.15.2
{{ .ani.registry }}/goharbor/trivy-adapter-photon:v2.15.2
{{ .ani.registry }}/goharbor/harbor-db:v2.15.2
{{ .ani.registry }}/goharbor/valkey-photon:v2.15.2
{{ index .ani.images "docker.io/library/busybox:1.37.0" }}
IMAGES
sort -o "$DIR/expected-images.txt" "$DIR/expected-images.txt"
diff -u "$DIR/expected-images.txt" "$DIR/rendered-images.txt"
grep -q 'name: ani-harbor-trivy' "$DIR/rendered.yaml"
# The singleton jobservice carries an RWO log PVC; enforce the chosen strategy.
awk '
  /^# Source: harbor\/templates\/jobservice\/jobservice-dpl.yaml$/ {in_job=1; next}
  /^# Source:/ {in_job=0}
  in_job && /^[[:space:]]*type: Recreate$/ {found=1}
  END {exit !found}
' "$DIR/rendered.yaml" || { echo 'Harbor Jobservice must render Recreate for its RWO PVC' >&2; exit 1; }
grep -Eq 'name: "?ani-harbor-database"?' "$DIR/rendered.yaml"
grep -q 'name: ani-harbor-redis' "$DIR/rendered.yaml"
grep -q 'claimName: ani-harbor-trivy-cache' "$DIR/rendered.yaml"
grep -q 'name: "SCANNER_TRIVY_SKIP_UPDATE"' "$DIR/rendered.yaml"
grep -q 'name: "SCANNER_TRIVY_SKIP_JAVA_DB_UPDATE"' "$DIR/rendered.yaml"
grep -q 'name: "SCANNER_TRIVY_OFFLINE_SCAN"' "$DIR/rendered.yaml"
echo 'B07 fixed Chart render and offline scanner settings verified'

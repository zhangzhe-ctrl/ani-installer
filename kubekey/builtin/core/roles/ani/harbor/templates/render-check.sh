#!/usr/bin/env bash
set -euo pipefail
ART='{{ .ani.artifact_root }}'
DIR=/etc/kubernetes/ani/harbor
export KUBECONFIG='{{ .ani.run.kubeconfig }}'
"$ART/bin/helm" template ani-harbor "$ART/charts/harbor/1.19.2.tgz" \
  --namespace ani-harbor --kube-version 1.35.8 --values "$DIR/values.yaml" \
  --set-file database.internal.password="$DIR/db-password" \
  --set-file registry.credentials.password="$DIR/registry-password" --include-crds > "$DIR/rendered.yaml"
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
grep -Eq 'name: \"?ani-harbor-database\"?' "$DIR/rendered.yaml"
grep -q 'name: ani-harbor-redis' "$DIR/rendered.yaml"
grep -q 'claimName: ani-harbor-trivy-cache' "$DIR/rendered.yaml"
grep -q 'name: "SCANNER_TRIVY_SKIP_UPDATE"' "$DIR/rendered.yaml"
grep -q 'name: "SCANNER_TRIVY_SKIP_JAVA_DB_UPDATE"' "$DIR/rendered.yaml"
grep -q 'name: "SCANNER_TRIVY_OFFLINE_SCAN"' "$DIR/rendered.yaml"
echo 'B07 fixed Chart render and offline scanner settings verified'

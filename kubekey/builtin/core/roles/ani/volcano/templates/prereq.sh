#!/usr/bin/env bash
# Check the exact local chart, its rendered images and foreign global APIs.
set -euo pipefail
KUBECONFIG_FILE="${ANI_RUN_KUBECONFIG:?}"
test -f "$KUBECONFIG_FILE"
K=(kubectl --kubeconfig "$KUBECONFIG_FILE")
CHART='{{ .ani.artifact_root }}/charts/volcano/1.15.2.tgz'
HELM='{{ .ani.artifact_root }}/bin/helm'
test -x "$HELM" && test -s "$CHART"
echo '84b13ab82dd73c95307131ac105566f1dcf96383eed14bf30c4a74b0020b3fdd  '"$CHART" | sha256sum -c -
if KUBECONFIG="$KUBECONFIG_FILE" "$HELM" status ani-volcano -n volcano-system >/dev/null 2>&1; then
  echo 'Volcano Helm release already exists; first-install role never upgrades it' >&2
  exit 1
fi
KUBECONFIG="$KUBECONFIG_FILE" "$HELM" template ani-volcano "$CHART" -n volcano-system \
  -f /etc/kubernetes/ani/volcano/values.yaml --include-crds > /etc/kubernetes/ani/volcano/rendered.yaml
# The v1.15.2 controller blocks Queue reconciliation until both Flow API
# informers sync. The official subchart renders exactly these two CRDs and no
# Flow workload. Keep the APIs but reject actual JobFlow objects or images.
for flow_crd in jobflows.flow.volcano.sh jobtemplates.flow.volcano.sh; do
  grep -Eq "^[[:space:]]*name: $flow_crd[[:space:]]*$" /etc/kubernetes/ani/volcano/rendered.yaml || {
    echo "official Volcano chart omits controller-required CRD $flow_crd" >&2; exit 1;
  }
done
if grep -Eqi '^kind: (JobFlow|JobTemplate)[[:space:]]*$|^[[:space:]]*image:.*jobflow' /etc/kubernetes/ani/volcano/rendered.yaml; then
  echo 'the selected CPU chart renders a JobFlow object or image' >&2; exit 1
fi
awk '$1 == "image:" && $2 != "" {print $2}' /etc/kubernetes/ani/volcano/rendered.yaml | sort -u > /etc/kubernetes/ani/volcano/rendered-images.txt
cat > /etc/kubernetes/ani/volcano/expected-images.txt <<'IMAGES'
{{ .ani.registry }}/volcanosh/vc-controller-manager:v1.15.2
{{ .ani.registry }}/volcanosh/vc-scheduler:v1.15.2
{{ .ani.registry }}/volcanosh/vc-webhook-manager:v1.15.2
IMAGES
sort -o /etc/kubernetes/ani/volcano/expected-images.txt /etc/kubernetes/ani/volcano/expected-images.txt
diff -u /etc/kubernetes/ani/volcano/expected-images.txt /etc/kubernetes/ani/volcano/rendered-images.txt
# The pre-install hook Job creates the admission TLS Secret; it must be present.
grep -q 'name: ani-volcano-admission-init' /etc/kubernetes/ani/volcano/rendered.yaml

for crd in queues.scheduling.volcano.sh podgroups.scheduling.volcano.sh jobs.batch.volcano.sh jobflows.flow.volcano.sh jobtemplates.flow.volcano.sh; do
  existing="$("${K[@]}" get "crd/$crd" --ignore-not-found -o name)"
  [ -z "$existing" ] || { echo "foreign or pre-existing Volcano CRD $crd; refusing takeover" >&2; exit 1; }
done
for workload in ani-volcano-scheduler ani-volcano-controllers ani-volcano-admission; do
  existing="$("${K[@]}" -n volcano-system get "deployment/$workload" --ignore-not-found -o name)"
  [ -z "$existing" ] || { echo "pre-existing Volcano workload $workload; refusing takeover" >&2; exit 1; }
done

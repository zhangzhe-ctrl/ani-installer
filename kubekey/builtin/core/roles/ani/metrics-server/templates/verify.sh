#!/usr/bin/env bash
# B03 checks a fresh PodMetrics sample from the aggregation API, not Prometheus.
set -euo pipefail
KUBECONFIG_FILE="${ANI_VERIFY_KUBECONFIG:?}"
test -f "$KUBECONFIG_FILE"
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_FILE")
OUT_DIR="${ANI_VERIFY_OUTPUT_DIR:?}"
mkdir -p "$OUT_DIR"
"${KUBECTL[@]}" wait --for=condition=Available apiservice/v1beta1.metrics.k8s.io --timeout=300s
pod="ani-metrics-cpu-$(date +%Y%m%d%H%M%S)-$$"
start="$(date -u +%s)"
cat > "$OUT_DIR/$pod.yaml" <<POD_EOF
apiVersion: v1
kind: Pod
metadata:
  name: $pod
  namespace: kube-system
  labels: {ani.io/check: metrics-server}
spec:
  restartPolicy: Never
  containers:
    - name: cpu
      image: {{ index .ani.images "docker.io/library/busybox:1.37.0" }}
      imagePullPolicy: IfNotPresent
      command: ["/bin/sh", "-ec"]
      args: ["dd if=/dev/zero of=/tmp/marker bs=1M count=16 >/dev/null 2>&1; while :; do :; done"]
      resources:
        requests: {cpu: 100m, memory: 32Mi}
        limits: {cpu: "1", memory: 128Mi}
POD_EOF
"${KUBECTL[@]}" apply --server-side -f "$OUT_DIR/$pod.yaml"
"${KUBECTL[@]}" -n kube-system wait --for=condition=Ready "pod/$pod" --timeout=180s
result="$OUT_DIR/$pod-metrics.json"
for attempt in $(seq 1 24); do
  if "${KUBECTL[@]}" get --raw "/apis/metrics.k8s.io/v1beta1/namespaces/kube-system/pods/$pod" > "$result" 2> "$OUT_DIR/$pod-error.log"; then
    if python3 - "$result" "$pod" "$start" <<'PY'
import datetime, json, re, sys
obj=json.load(open(sys.argv[1]))
assert obj.get('metadata',{}).get('name') == sys.argv[2]
assert obj.get('metadata',{}).get('namespace') == 'kube-system'
ts=datetime.datetime.fromisoformat(obj['timestamp'].replace('Z','+00:00')).timestamp()
now=datetime.datetime.now(datetime.timezone.utc).timestamp()
assert int(sys.argv[3])-10 <= ts <= now and now-ts <= 90, f'stale timestamp: {obj["timestamp"]}'
containers=obj.get('containers') or []
assert len(containers)==1 and containers[0]['name']=='cpu'
usage=containers[0]['usage']
def positive_quantity(value, units):
    match=re.fullmatch(r'([0-9]+(?:\.[0-9]+)?)([A-Za-z]*)',value)
    assert match and match.group(2) in units, f'invalid quantity {value}'
    assert float(match.group(1))>0, f'non-positive quantity {value}'
positive_quantity(usage['cpu'], {'','n','u','m'})
positive_quantity(usage['memory'], {'','Ki','Mi','Gi','Ti'})
print(f'ANI-POD-METRICS-OK pod={sys.argv[2]} timestamp={obj["timestamp"]} cpu={usage["cpu"]} memory={usage["memory"]}')
PY
    then
      "${KUBECTL[@]}" -n kube-system delete "pod/$pod" --wait=false >/dev/null
      exit 0
    fi
  fi
  sleep 5
done
echo "PodMetrics lacked a fresh positive CPU/memory sample; test Pod retained; evidence=$OUT_DIR" >&2
exit 1

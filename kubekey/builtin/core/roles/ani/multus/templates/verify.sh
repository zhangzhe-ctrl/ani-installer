#!/usr/bin/env bash
# B01a: use two Pods on one Ready node, proving net1, eth0 and DNS together.
set -euo pipefail
KUBECONFIG_FILE="${ANI_VERIFY_KUBECONFIG:?}"
OUT_DIR="${ANI_VERIFY_OUTPUT_DIR:?}"
test -f "$KUBECONFIG_FILE"
install -d -m 0700 "$OUT_DIR"
K=(kubectl --kubeconfig "$KUBECONFIG_FILE")
NS=ani-platform
IMAGE='{{ index .ani.images "docker.io/library/busybox:1.37.0" }}'
node="$("${K[@]}" get nodes -o json | python3 -c '
import json,sys
nodes=json.load(sys.stdin)["items"]
for n in nodes:
    if n["spec"].get("unschedulable"): continue
    if any(c["type"]=="Ready" and c["status"]=="True" for c in n["status"]["conditions"]):
        print(n["metadata"]["name"]); break
')"
[ -n "$node" ] || { echo 'no schedulable Ready node' >&2; exit 1; }
suffix="$(date -u +%Y%m%d%H%M%S)-$$"
a="ani-b01-a-$suffix"; b="ani-b01-b-$suffix"
for pod in "$a" "$b"; do
  cat > "$OUT_DIR/$pod.yaml" <<POD
apiVersion: v1
kind: Pod
metadata:
  name: $pod
  namespace: $NS
  labels: {ani.io/managed-by: '{{ .kubernetes.cluster_name }}', ani.io/check: multus}
  annotations:
    k8s.v1.cni.cncf.io/networks: ani-b01-local
spec:
  nodeName: $node
  restartPolicy: Never
  containers:
    - name: checker
      image: $IMAGE
      imagePullPolicy: IfNotPresent
      command: ["/bin/sh", "-c", "sleep 600"]
POD
  "${K[@]}" apply --server-side -f "$OUT_DIR/$pod.yaml"
done
if ! "${K[@]}" -n "$NS" wait "pod/$a" "pod/$b" --for=condition=Ready --timeout=240s; then
  "${K[@]}" -n "$NS" get pod "$a" "$b" -o yaml > "$OUT_DIR/b01-pods-failed.yaml" || true
  echo "B01 Pod readiness failed; own Pods retained, evidence=$OUT_DIR" >&2
  exit 1
fi
for pod in "$a" "$b"; do
  "${K[@]}" -n "$NS" get "pod/$pod" -o json > "$OUT_DIR/$pod.json"
  "${K[@]}" -n "$NS" exec "$pod" -- sh -ec 'ip -4 addr show dev eth0; ip -4 addr show dev net1; ip route show default; nslookup kubernetes.default.svc.cluster.local' > "$OUT_DIR/$pod.network.txt"
  grep -q 'inet ' "$OUT_DIR/$pod.network.txt"
  grep -q 'dev eth0' "$OUT_DIR/$pod.network.txt"
  grep -q 'kubernetes.default.svc.cluster.local' "$OUT_DIR/$pod.network.txt"
  "${K[@]}" -n "$NS" get "pod/$pod" -o json | python3 -c '
import json,sys
x=json.load(sys.stdin)
status=json.loads(x["metadata"]["annotations"]["k8s.v1.cni.cncf.io/network-status"])
assert any(v.get("interface")=="eth0" for v in status),status
assert any(v.get("interface")=="net1" and v.get("name","").endswith("ani-b01-local") for v in status),status
'
done
ip_a="$("${K[@]}" -n "$NS" exec "$a" -- sh -ec "ip -4 addr show dev net1 | awk '/inet / {print \$2}' | cut -d/ -f1")"
ip_b="$("${K[@]}" -n "$NS" exec "$b" -- sh -ec "ip -4 addr show dev net1 | awk '/inet / {print \$2}' | cut -d/ -f1")"
[ -n "$ip_a" ] && [ -n "$ip_b" ] && [ "$ip_a" != "$ip_b" ] || { echo 'net1 IP allocation failed or duplicate' >&2; exit 1; }
"${K[@]}" -n "$NS" exec "$a" -- ping -c 2 -W 2 "$ip_b" > "$OUT_DIR/net1-a-to-b.txt"
"${K[@]}" -n "$NS" exec "$b" -- ping -c 2 -W 2 "$ip_a" > "$OUT_DIR/net1-b-to-a.txt"
eth_b="$("${K[@]}" -n "$NS" get "pod/$b" -o jsonpath='{.status.podIP}')"
"${K[@]}" -n "$NS" exec "$a" -- ping -c 2 -W 2 "$eth_b" > "$OUT_DIR/eth0-a-to-b.txt"
"${K[@]}" -n "$NS" delete "pod/$a" "pod/$b" --wait=true >/dev/null
printf 'B01a PASS node=%s net1_a=%s net1_b=%s eth0_b=%s\n' "$node" "$ip_a" "$ip_b" "$eth_b" | tee "$OUT_DIR/result.txt"

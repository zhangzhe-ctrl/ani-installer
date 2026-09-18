#!/usr/bin/env bash
# LAB-ONLY K-5 remediation ladder. Order mandated by the user for B3:
#   1) restart the kcn controller (fix the dataplane, not the symptom)
#   2) if the target Pod is still unreachable, recreate it (the B2 workaround)
set -uo pipefail
K="kubectl --kubeconfig /etc/kubernetes/admin.conf"
NS=ani-platform
IMG=$($K -n $NS get statefulset/valkey -o jsonpath='{.spec.template.spec.containers[0].image}')
reachable() {
  $K -n $NS delete job heal-ck --ignore-not-found >/dev/null 2>&1
  cat <<POD | $K apply -f - >/dev/null 2>&1
apiVersion: batch/v1
kind: Job
metadata: { name: heal-ck, namespace: $NS }
spec:
  backoffLimit: 0
  template:
    spec:
      nodeName: node1
      restartPolicy: Never
      containers:
        - name: c
          image: $IMG
          env:
            - name: REDISCLI_AUTH
              valueFrom: { secretKeyRef: { name: ani-valkey-auth, key: valkey-password } }
          command: ["/bin/sh","-c"]
          args: ["valkey-cli -h valkey.$NS.svc.cluster.local -t 8 GET ani-heal-probe >/dev/null 2>&1; valkey-cli -h valkey.$NS.svc.cluster.local -t 8 PING 2>&1 | grep -q PONG && echo REACH-OK"]
POD
  $K -n $NS wait --for=condition=complete job/heal-ck --timeout=90s >/dev/null 2>&1 || true
  ok=$($K -n $NS logs job/heal-ck 2>/dev/null | grep -c REACH-OK || true)
  $K -n $NS delete job heal-ck --wait=false >/dev/null 2>&1
  [ "$ok" -ge 1 ]
}
restart_kcn_controller() {
  local w
  w=$($K -n kcn-system get deploy -o name 2>/dev/null | grep -i kcn | head -1)
  if [ -n "$w" ]; then
    echo "  restarting $w"
    $K -n kcn-system rollout restart "$w" >/dev/null 2>&1
    $K -n kcn-system rollout status "$w" --timeout=180s >/dev/null 2>&1 || true
  else
    local p
    p=$($K -n kcn-system get pods -o name 2>/dev/null | grep -iE "kcn-controller|kcn-control" | head -1)
    [ -n "$p" ] && { echo "  restarting pod $p"; $K -n kcn-system delete "$p" --wait=true >/dev/null 2>&1 || true; }
  fi
  sleep 15
}
for a in 1 2 3; do
  echo "reachability attempt $a @ $(date -u +%H:%M:%S)"
  if reachable; then echo "POD_REACHABLE"; exit 0; fi
  echo "  unreachable -> step 1: restart kcn-controller"
  restart_kcn_controller
  if reachable; then echo "KCN-RESTART-HEALED"; exit 0; fi
  echo "  still unreachable -> step 2: recreate valkey-0"
  $K -n $NS delete pod valkey-0 --wait=true >/dev/null 2>&1
  $K -n $NS wait --for=condition=Ready pod/valkey-0 --timeout=180s >/dev/null 2>&1
  sleep 6
  if reachable; then echo "POD-RECREATE-HEALED"; exit 0; fi
done
echo "POD_UNREACHABLE"; exit 1

#!/usr/bin/env bash
set -uo pipefail
K="kubectl --kubeconfig /etc/kubernetes/admin.conf"
NS=ani-platform
IMG=$($K -n $NS get sts postgresql -o jsonpath='{.spec.template.spec.containers[0].image}')
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
            - { name: PGCONNECT_TIMEOUT, value: "8" }
            - { name: PGUSER, value: ani_app }
            - { name: PGDATABASE, value: ani }
            - name: PGPASSWORD
              valueFrom: { secretKeyRef: { name: ani-postgres-app, key: app-password } }
          command: ["/bin/sh","-c"]
          args: ["psql -h postgresql.ani-platform.svc.cluster.local -tA -c \"SELECT 'REACH-OK'\" 2>&1"]
POD
  $K -n $NS wait --for=condition=complete job/heal-ck --timeout=90s >/dev/null 2>&1 || true
  ok=$($K -n $NS logs job/heal-ck 2>/dev/null | grep -c REACH-OK || true)
  $K -n $NS delete job heal-ck --wait=false >/dev/null 2>&1
  [ "$ok" -ge 1 ]
}
for a in $(seq 1 4); do
  echo "reachability attempt $a @ $(date -u +%H:%M:%S)"
  if reachable; then echo "POD_REACHABLE"; exit 0; fi
  echo "  not reachable; recreating postgresql-0"
  $K -n $NS delete pod postgresql-0 --wait=true >/dev/null 2>&1
  $K -n $NS wait --for=condition=Ready pod/postgresql-0 --timeout=180s >/dev/null 2>&1
  sleep 6
done
echo "POD_UNREACHABLE"; exit 1

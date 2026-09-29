#!/usr/bin/env bash
# B06 uses a single Pod, then a second Pod, against minMember=2. A first Pod
# with no nodeName after a scheduling window is the negative gang assertion.
set -euo pipefail
KUBECONFIG_FILE="${ANI_VERIFY_KUBECONFIG:?}"
OUT_DIR="${ANI_VERIFY_OUTPUT_DIR:?}"
test -f "$KUBECONFIG_FILE"
install -d -m 0700 "$OUT_DIR"
K=(kubectl --kubeconfig "$KUBECONFIG_FILE")
NS=ani-platform
OWNER='{{ .kubernetes.cluster_name }}'
IMAGE='{{ index .ani.images "docker.io/library/busybox:1.37.0" }}'
# The first install performs the gang, cancellation and cleanup exercise.
# Smoke checks that evidence and the current controllers without new Jobs.
if [ "${ANI_VERIFY_LEVEL:-}" = smoke ]; then
  EVID='{{ .ani.run.logs_dir }}'
  test -s "$EVID/b06-result.txt"
  read -r QUEUE GROUP < <(python3 - "$EVID" <<'PY'
import json,pathlib,re,sys
p=pathlib.Path(sys.argv[1])
line=(p/'b06-result.txt').read_text().strip()
m=re.fullmatch(r'B06 PASS Queue=(ani-b06-cpu-([0-9-]+)) PodGroup=(ani-b06-gang-\2) first-Pod-unscheduled=true completion=2/2 own-cancel=true accounting-released=true',line)
if not m: raise SystemExit('B06 first-install result is missing or malformed')
queue,suffix,group=m.group(1,2,3)
if json.loads((p/f'ani-b06-a-{suffix}-wait.json').read_text())['spec'].get('nodeName'): raise SystemExit('first gang Pod was scheduled alone')
for letter,mark in (('a','B06_CPU_DONE_A'),('b','B06_CPU_DONE_B')):
 name=f'ani-b06-{letter}-{suffix}'
 pod=json.loads((p/f'{name}-done.json').read_text())
 if pod['status']['phase']!='Succeeded' or pod['spec']['schedulerName']!='volcano': raise SystemExit(f'{name} did not finish under Volcano')
 if (p/f'{name}.log').read_text().strip()!=mark: raise SystemExit(f'{name} completion marker differs')
q=json.loads((p/'b06-queue-after.json').read_text())
st=q.get('status',{})
zero=lambda v: re.fullmatch(r'0(?:\.0+)?(?:m|Ki|Mi|Gi)?',str(v)) is not None
if not all(zero(v) for v in st.get('allocated',{}).values()) or st.get('inqueue',0)!=0 or st.get('pending',0)!=0: raise SystemExit('B06 Queue accounting was not released')
print(queue,group)
PY
  )
  for deployment in ani-volcano-admission ani-volcano-controllers ani-volcano-scheduler; do
    "${K[@]}" -n volcano-system rollout status "deployment/$deployment" --timeout=60s
  done
  test -z "$("${K[@]}" get "queue/$QUEUE" --ignore-not-found -o name)"
  test -z "$("${K[@]}" -n "$NS" get "podgroup/$GROUP" --ignore-not-found -o name)"
  "${K[@]}" -n kube-system get pods -l component=kube-scheduler -o json > "$OUT_DIR/b06-default-scheduler-smoke.json"
  "${K[@]}" get validatingwebhookconfigurations,mutatingwebhookconfigurations -o json > "$OUT_DIR/b06-webhooks-smoke.json"
  python3 - "$OUT_DIR" "$OWNER" <<'PY'
import json,pathlib,sys
p=pathlib.Path(sys.argv[1]); owner=sys.argv[2]
pods=json.loads((p/'b06-default-scheduler-smoke.json').read_text())['items']
if not pods or any(x['status']['phase']!='Running' for x in pods): raise SystemExit('default scheduler unhealthy')
items=[x for x in json.loads((p/'b06-webhooks-smoke.json').read_text())['items'] if x['metadata']['name'].startswith('volcano-admission-service-')]
if {x['kind'] for x in items}!={'ValidatingWebhookConfiguration','MutatingWebhookConfiguration'}: raise SystemExit('Volcano admission webhooks missing')
for item in items:
 if item['metadata'].get('labels',{}).get('ani.io/managed-by')!=owner: raise SystemExit('foreign Volcano webhook')
 for hook in item['webhooks']:
  if not hook['clientConfig'].get('caBundle') or hook.get('failurePolicy','Fail')!='Fail': raise SystemExit('Volcano webhook security changed')
PY
  printf 'B06 smoke PASS: first-install gang evidence, own cleanup and live controllers/webhooks/default scheduler\n'
  exit 0
fi
SUFFIX="$(date -u +%Y%m%d%H%M%S)-$$"
QUEUE_NAME="ani-b06-cpu-$SUFFIX"
GROUP="ani-b06-gang-$SUFFIX"
CANCEL="ani-b06-cancel-$SUFFIX"
A="ani-b06-a-$SUFFIX"
B="ani-b06-b-$SUFFIX"
C="ani-b06-c-$SUFFIX"
"${K[@]}" get nodes -o json > "$OUT_DIR/b06-nodes-before.json"
python3 - "$OUT_DIR/b06-nodes-before.json" <<'PY'
import json,sys
nodes=json.load(open(sys.argv[1]))['items']
ready=[n for n in nodes if not n['spec'].get('unschedulable') and any(c['type']=='Ready' and c['status']=='True' for c in n['status']['conditions'])]
def millicpu(s):return int(s[:-1]) if s.endswith('m') else int(float(s)*1000)
def mib(s):
 if s.endswith('Ki'):return int(s[:-2])//1024
 if s.endswith('Mi'):return int(s[:-2])
 if s.endswith('Gi'):return int(s[:-2])*1024
 return int(s)//1048576
cpu=sum(millicpu(n['status']['allocatable']['cpu']) for n in ready)
mem=sum(mib(n['status']['allocatable']['memory']) for n in ready)
if cpu<200 or mem<128:raise SystemExit(f'B06 needs 200m CPU/128Mi memory; Ready-node allocatable={cpu}m/{mem}Mi, deficit={max(0,200-cpu)}m/{max(0,128-mem)}Mi')
print(f'B06 Ready-node allocatable={cpu}m/{mem}Mi; test requests=200m/128Mi')
PY
"${K[@]}" -n kube-system get pods -l component=kube-scheduler -o json > "$OUT_DIR/b06-default-scheduler-before.json"
python3 - "$OUT_DIR/b06-default-scheduler-before.json" <<'PY'
import json,sys
pods=json.load(open(sys.argv[1]))['items']
assert pods and all(p['status']['phase']=='Running' for p in pods), 'base kube-scheduler Pods are not running'
PY
"${K[@]}" get validatingwebhookconfigurations,mutatingwebhookconfigurations -o json > "$OUT_DIR/b06-webhooks.json"
python3 - "$OUT_DIR/b06-webhooks.json" "$OWNER" <<'PY'
import json,sys
items=[x for x in json.load(open(sys.argv[1]))['items']
       if x['metadata']['name'].startswith('volcano-admission-service-')]
assert items, 'Volcano admission webhooks absent'
assert {x['kind'] for x in items} == {'ValidatingWebhookConfiguration','MutatingWebhookConfiguration'}, 'Volcano validating or mutating admission missing'
for item in items:
 assert item['metadata'].get('labels',{}).get('ani.io/managed-by')==sys.argv[2],f'{item["metadata"]["name"]} is foreign'
 for wh in item['webhooks']:
  assert wh['clientConfig'].get('caBundle'),f'{item["metadata"]["name"]} missing CA bundle'
  assert wh.get('failurePolicy','Fail')=='Fail',f'{item["metadata"]["name"]} ignores admission failure'
PY
cat > "$OUT_DIR/b06-queue.yaml" <<QUEUE
apiVersion: scheduling.volcano.sh/v1beta1
kind: Queue
metadata:
  name: $QUEUE_NAME
  labels: {ani.io/managed-by: "$OWNER"}
spec:
  weight: 1
  reclaimable: true
  capability: {cpu: 200m, memory: 128Mi}
QUEUE
if [ -n "$("${K[@]}" get "queue/$QUEUE_NAME" --ignore-not-found -o name)" ]; then
  existing="$("${K[@]}" get "queue/$QUEUE_NAME" -o jsonpath='{.metadata.labels.ani\.io/managed-by}')"
  [ "$existing" = "$OWNER" ] || { echo "foreign Queue $QUEUE_NAME owner=$existing" >&2; exit 1; }
fi
"${K[@]}" apply --server-side -f "$OUT_DIR/b06-queue.yaml"
# Queue status is reconciled asynchronously. Admission only accepts a group
# after the controller marks the Queue Open.
queue_open=false
for attempt in $(seq 1 45); do
  state="$("${K[@]}" get "queue/$QUEUE_NAME" -o jsonpath='{.status.state}')"
  if [ "$state" = Open ]; then queue_open=true; break; fi
  sleep 2
done
[ "$queue_open" = true ] || { echo "Queue $QUEUE_NAME did not become Open; state=$state" >&2; exit 1; }
make_group() {
  local group="$1"
  cat > "$OUT_DIR/$group.yaml" <<PG
apiVersion: scheduling.volcano.sh/v1beta1
kind: PodGroup
metadata:
  name: $group
  namespace: $NS
  labels: {ani.io/managed-by: "$OWNER"}
spec:
  minMember: 2
  queue: $QUEUE_NAME
  minResources: {cpu: 200m, memory: 128Mi}
PG
  "${K[@]}" apply --server-side -f "$OUT_DIR/$group.yaml"
}
make_pod() {
  local pod="$1" group="$2" marker="$3"
  cat > "$OUT_DIR/$pod.yaml" <<POD
apiVersion: v1
kind: Pod
metadata:
  name: $pod
  namespace: $NS
  labels: {ani.io/managed-by: "$OWNER", ani.io/check: volcano}
  annotations:
    scheduling.k8s.io/group-name: $group
spec:
  schedulerName: volcano
  restartPolicy: Never
  containers:
    - name: cpu
      image: $IMAGE
      imagePullPolicy: IfNotPresent
      command: ["/bin/sh", "-ec"]
      args: ["echo $marker; sleep 30"]
      resources:
        requests: {cpu: 100m, memory: 64Mi}
        limits: {cpu: 100m, memory: 64Mi}
POD
  "${K[@]}" apply --server-side -f "$OUT_DIR/$pod.yaml"
}
make_group "$GROUP"
make_pod "$A" "$GROUP" B06_CPU_DONE_A
sleep 15
"${K[@]}" -n "$NS" get "pod/$A" -o json > "$OUT_DIR/$A-wait.json"
"${K[@]}" -n "$NS" get "podgroup/$GROUP" -o json > "$OUT_DIR/$GROUP-wait.json"
node="$("${K[@]}" -n "$NS" get "pod/$A" -o jsonpath='{.spec.nodeName}')"
[ -z "$node" ] || { echo "gang violation: first Pod scheduled with only one of minMember=2" >&2; exit 1; }
make_pod "$B" "$GROUP" B06_CPU_DONE_B
"${K[@]}" -n "$NS" wait "pod/$A" "pod/$B" --for=condition=Ready --timeout=180s || {
  "${K[@]}" -n "$NS" describe "pod/$A" "pod/$B" > "$OUT_DIR/b06-schedule-failure.txt" 2>&1 || true
  echo "B06 gang did not become Ready; see own Pod events in $OUT_DIR" >&2; exit 1;
}
"${K[@]}" -n "$NS" get "podgroup/$GROUP" -o json > "$OUT_DIR/$GROUP-running.json"
"${K[@]}" -n "$NS" wait "pod/$A" "pod/$B" --for=jsonpath='{.status.phase}=Succeeded' --timeout=180s
for item in "$A:B06_CPU_DONE_A" "$B:B06_CPU_DONE_B"; do
  pod="${item%%:*}" marker="${item#*:}"
  "${K[@]}" -n "$NS" logs "$pod" > "$OUT_DIR/$pod.log"
  grep -qx "$marker" "$OUT_DIR/$pod.log"
  "${K[@]}" -n "$NS" get "pod/$pod" -o json > "$OUT_DIR/$pod-done.json"
  "${K[@]}" -n "$NS" get "pod/$pod" -o jsonpath='{.spec.schedulerName}' | grep -qx volcano
done
# Cancellation is confined to this run's own pending Pod and PodGroup.
make_group "$CANCEL"
make_pod "$C" "$CANCEL" B06_SHOULD_NOT_RUN
sleep 10
[ -z "$("${K[@]}" -n "$NS" get "pod/$C" -o jsonpath='{.spec.nodeName}')" ]
"${K[@]}" -n "$NS" delete "pod/$C" "podgroup/$CANCEL" --wait=true >/dev/null
[ -z "$("${K[@]}" -n "$NS" get "pod/$C" "podgroup/$CANCEL" --ignore-not-found -o name)" ]
"${K[@]}" -n "$NS" delete "pod/$A" "pod/$B" "podgroup/$GROUP" --wait=true >/dev/null
# Observe released Queue accounting after all owned Pods and PodGroups are gone.
for attempt in $(seq 1 30); do
  "${K[@]}" get "queue/$QUEUE_NAME" -o json > "$OUT_DIR/b06-queue-after.json"
  if python3 - "$OUT_DIR/b06-queue-after.json" <<'PY'
import json,re,sys
q=json.load(open(sys.argv[1]))
status=q.get('status',{})
allocated=status.get('allocated',{})
zero=lambda value: re.fullmatch(r'0(?:\.0+)?(?:m|Ki|Mi|Gi)?',str(value)) is not None
assert all(zero(v) for v in allocated.values()),f'Queue allocation not released: {allocated}'
assert status.get('inqueue',0)==0 and status.get('pending',0)==0,f'Queue groups remain: {status}'
PY
  then break; fi
  if [ "$attempt" -eq 30 ]; then echo "Queue resource accounting did not recover" >&2; exit 1; fi
  sleep 2
done
"${K[@]}" delete "queue/$QUEUE_NAME" --wait=true >/dev/null
"${K[@]}" -n kube-system get pods -l component=kube-scheduler -o json > "$OUT_DIR/b06-default-scheduler-after.json"
python3 - "$OUT_DIR/b06-default-scheduler-after.json" <<'PY'
import json,sys
pods=json.load(open(sys.argv[1]))['items']
assert pods and all(p['status']['phase']=='Running' for p in pods), 'base kube-scheduler Pods unhealthy after B06'
PY
printf 'B06 PASS Queue=%s PodGroup=%s first-Pod-unscheduled=true completion=2/2 own-cancel=true accounting-released=true\n' "$QUEUE_NAME" "$GROUP" | tee "$OUT_DIR/b06-result.txt"

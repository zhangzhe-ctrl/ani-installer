#!/usr/bin/env bash
# ANI generic network smoke checker (R11/A04, stack-independent).
#
# This script proves Pod networking with real requests and content
# assertions; it is deliberately free of any kcn/Envoy concept so both the
# kcn and the kube-ovn stack run exactly the same generic network check
# (the kcn-only Envoy probe stays in probe.sh, called from the kcn path).
#
# Contract (R11):
#   - a run-scoped namespace ani-net-smoke-<run-id> is created for this run
#     only; on success it is deleted by its exact name, on failure everything
#     is kept for forensics and only read-only evidence is collected;
#   - one server Pod and one client Pod per other node, all using the
#     locally locked smoke image (ANI_NETSMOKE_IMAGE, resolved by verify.sh
#     from the artifact image table against the configured registry);
#   - pods are NOT pinned: the checker waits for the actual scheduling
#     outcome and fails unless every client landed on a different node than
#     the server (a same-node schedule can never report a cross-node pass);
#   - every client asserts client->server PodIP, client->ClusterIP and
#     DNS-resolved Service name, each with an exact HTTP body match against
#     the per-run token — HTTP 200 with a wrong body is a failure;
#   - one evidence line per source/destination pair: nodes, markers, time;
#   - nothing is repaired: no route, LSP, CNI or proxy changes.
set -Eeuo pipefail

KUBECONFIG_FILE="${KUBECONFIG_FILE:-/etc/kubernetes/admin.conf}"
OUTPUT_BASE="${ANI_SMOKE_OUTPUT:-/tmp/ani-net-smoke}"
# The image must come from the local material lock, resolved for this site's
# registry by the caller (verify.sh reads the artifact image table). Refusing
# a default keeps the checker from ever pulling from an outside registry.
IMAGE_REF="${ANI_NETSMOKE_IMAGE:?ANI_NETSMOKE_IMAGE must be set to the locally locked smoke image reference}"
RUN_ID="$(printf '%s' "${ANI_NETSMOKE_RUN_ID:-$(date +%Y%m%d-%H%M%S)-$$-$RANDOM}" | tr -c 'a-zA-Z0-9-' '-')"
NAMESPACE="ani-net-smoke-${RUN_ID}"
TOKEN="ANI-NET-${RUN_ID}-BODY"
SERVER_NAME_PREFIX="ani-net-smoke-server-"
CLIENT_NAME_PREFIX="ani-net-smoke-client-"
SERVICE_NAME="ani-net-smoke-svc"
CLIENT_PORT="3000"
readonly NET_PODIP_OK="NET-PODIP-OK"
readonly NET_SVCIP_OK="NET-SVCIP-OK"
readonly NET_DNS_OK="NET-DNS-OK"
readonly NET_SUCCESS="ANI-NETWORK-OK"

if [[ ! -f "$KUBECONFIG_FILE" ]]; then
  echo "kubeconfig not found: $KUBECONFIG_FILE" >&2
  exit 1
fi

mkdir -p "$OUTPUT_BASE"
OUTPUT_DIR="$(mktemp -d "$OUTPUT_BASE/net-run-XXXXXX")"
# R12/A12: every kubectl request is bounded; the scheduling waits below run in
# this script with their own timeouts, so 60s covers all single requests.
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_FILE" --request-timeout=60s)
CURRENT_POD=""

on_exit() {
  local rc=$?
  if (( rc != 0 )); then
    printf '%s\n' "result=fail" >> "$OUTPUT_DIR/summary.txt"
    printf '%s\n' "finished_at=$(date -u +'%Y-%m-%dT%H:%M:%SZ')" >> "$OUTPUT_DIR/summary.txt"
    # Read-only forensics only: events, object status, describe, logs. The
    # run namespace and every object in it are kept for the operator.
    "${KUBECTL[@]}" get events -n "$NAMESPACE" > "$OUTPUT_DIR/events.txt" 2>&1 || true
    "${KUBECTL[@]}" get namespace "$NAMESPACE" -o yaml > "$OUTPUT_DIR/namespace.yaml" 2>&1 || true
    "${KUBECTL[@]}" get pods -n "$NAMESPACE" -o wide > "$OUTPUT_DIR/pods.txt" 2>&1 || true
    if [[ -n "$CURRENT_POD" ]]; then
      "${KUBECTL[@]}" get pod "$CURRENT_POD" -n "$NAMESPACE" -o yaml > "$OUTPUT_DIR/${CURRENT_POD}-pod.yaml" 2>&1 || true
      "${KUBECTL[@]}" describe pod "$CURRENT_POD" -n "$NAMESPACE" > "$OUTPUT_DIR/${CURRENT_POD}-describe.log" 2>&1 || true
      "${KUBECTL[@]}" logs "$CURRENT_POD" -n "$NAMESPACE" > "$OUTPUT_DIR/${CURRENT_POD}-logs.log" 2>&1 || true
    fi
  fi
}
trap on_exit EXIT

log() {
  printf '[ani-net-smoke] %s\n' "$*"
}

append_summary() {
  printf '%s\n' "$*" >> "$OUTPUT_DIR/summary.txt"
}

wait_for_pod_phase() {
  local pod="$1" want="$2" timeout="$3" label="$4"
  local start=$SECONDS phase
  while :; do
    phase="$("${KUBECTL[@]}" get pod "$pod" -n "$NAMESPACE" -o jsonpath='{.status.phase}')"
    if [[ "$phase" == "$want" ]]; then
      return 0
    fi
    if [[ "$phase" == "Failed" ]]; then
      echo "$label reached Failed phase" >&2
      return 1
    fi
    if (( SECONDS - start >= timeout )); then
      echo "$label timed out after ${timeout}s in phase $phase" >&2
      return 1
    fi
    sleep 2
  done
}

pod_field() {
  local pod="$1" field="$2"
  "${KUBECTL[@]}" get pod "$pod" -n "$NAMESPACE" -o jsonpath="$field"
}

append_summary "started_at=$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
append_summary "kubeconfig=$KUBECONFIG_FILE"
append_summary "image=$IMAGE_REF"
append_summary "run_id=$RUN_ID"
append_summary "namespace=$NAMESPACE"
append_summary "output_dir=$OUTPUT_DIR"

mapfile -t node_names < <("${KUBECTL[@]}" get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort)
if [[ "${#node_names[@]}" -lt 2 ]]; then
  log "need at least 2 nodes for a cross-node pair, got ${#node_names[@]}"
  exit 1
fi
ready_count="$("${KUBECTL[@]}" get nodes -o jsonpath='{range .items[*]}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}' | grep -cx True)"
if [[ "$ready_count" != "${#node_names[@]}" ]]; then
  log "expected ${#node_names[@]} Ready nodes, got $ready_count"
  exit 1
fi

# The namespace is created for this run only and carries the run label, so a
# success cleanup can target it by exact name (never a shared namespace).
"${KUBECTL[@]}" create namespace "$NAMESPACE" >/dev/null
"${KUBECTL[@]}" label namespace "$NAMESPACE" ani-installer.io/network-smoke=true "ani-installer.io/run-id=$RUN_ID" >/dev/null

CURRENT_POD=""
server_name="$("${KUBECTL[@]}" create -f - -o jsonpath='{.metadata.name}' <<EOF
apiVersion: v1
kind: Pod
metadata:
  generateName: $SERVER_NAME_PREFIX
  namespace: $NAMESPACE
  labels:
    app: ani-net-smoke-server
    ani-installer.io/run-id: $RUN_ID
spec:
  restartPolicy: Never
  containers:
    - name: server
      image: "$IMAGE_REF"
      imagePullPolicy: IfNotPresent
      command: ["/bin/sh", "-ec"]
      args:
        - |
          mkdir -p /www
          printf '$TOKEN\n' > /www/index.html
          exec busybox httpd -f -p $CLIENT_PORT -h /www
      ports:
        - name: http
          containerPort: $CLIENT_PORT
EOF
)"
CURRENT_POD="$server_name"
append_summary "server_pod=$server_name"

wait_for_pod_phase "$server_name" "Running" 180 "network smoke server"
server_node="$(pod_field "$server_name" '{.spec.nodeName}')"
server_ip="$(pod_field "$server_name" '{.status.podIP}')"
if [[ -z "$server_node" || -z "$server_ip" ]]; then
  log "server Pod node or IP is missing"
  exit 1
fi
append_summary "server_pod_ip=$server_ip"
append_summary "server_node=$server_node"

"${KUBECTL[@]}" create -f - >/dev/null <<EOF
apiVersion: v1
kind: Service
metadata:
  name: $SERVICE_NAME
  namespace: $NAMESPACE
  labels:
    app: ani-net-smoke-server
    ani-installer.io/run-id: $RUN_ID
spec:
  selector:
    app: ani-net-smoke-server
    ani-installer.io/run-id: $RUN_ID
  ports:
    - name: http
      port: $CLIENT_PORT
      targetPort: $CLIENT_PORT
EOF
service_ip="$("${KUBECTL[@]}" get service "$SERVICE_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.clusterIP}')"
if [[ -z "$service_ip" ]]; then
  log "smoke Service ClusterIP is missing"
  exit 1
fi
append_summary "service_cluster_ip=$service_ip"

# One client per non-server node. Clients are never pinned to a node: they
# carry a required anti-affinity against every pod of this run, so the
# scheduler must spread them, and the checker still waits for the actual
# scheduling result and asserts it below (a same-node outcome fails).
client_count=0
for node in "${node_names[@]}"; do
  [[ "$node" == "$server_node" ]] && continue
  client_count=$((client_count + 1))
done
if [[ "$client_count" -lt 1 ]]; then
  log "no node other than $server_node is available for a cross-node client"
  exit 1
fi

CURRENT_POD=""
declare -a client_names=()
for _ in $(seq 1 "$client_count"); do
  client_name="$("${KUBECTL[@]}" create -f - -o jsonpath='{.metadata.name}' <<EOF
apiVersion: v1
kind: Pod
metadata:
  generateName: $CLIENT_NAME_PREFIX
  namespace: $NAMESPACE
  labels:
    app: ani-net-smoke-client
    ani-installer.io/run-id: $RUN_ID
spec:
  restartPolicy: Never
  affinity:
    podAntiAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
        - labelSelector:
            matchLabels:
              ani-installer.io/run-id: $RUN_ID
          topologyKey: kubernetes.io/hostname
  containers:
    - name: client
      image: "$IMAGE_REF"
      imagePullPolicy: IfNotPresent
      command: ["/bin/sh", "-ec"]
      args:
        - |
          body="\$(wget -T 10 -qO- http://$server_ip:$CLIENT_PORT/)" || exit 10
          test "\$body" = "$TOKEN" || exit 11
          printf '$NET_PODIP_OK\n'
          body="\$(wget -T 10 -qO- http://$service_ip:$CLIENT_PORT/)" || exit 12
          test "\$body" = "$TOKEN" || exit 13
          printf '$NET_SVCIP_OK\n'
          body="\$(wget -T 10 -qO- http://$SERVICE_NAME.$NAMESPACE.svc.cluster.local:$CLIENT_PORT/)" || exit 14
          test "\$body" = "$TOKEN" || exit 15
          printf '$NET_DNS_OK\n'
          printf '$NET_SUCCESS\n'
EOF
)"
  client_names+=("$client_name")
done

pair=0
for client_name in "${client_names[@]}"; do
  CURRENT_POD="$client_name"
  append_summary "client_pod=$client_name"
  wait_for_pod_phase "$client_name" "Succeeded" 240 "network smoke client $client_name"
  client_exit_code="$(pod_field "$client_name" '{.status.containerStatuses[0].state.terminated.exitCode}')"
  if [[ "$client_exit_code" != "0" ]]; then
    log "client $client_name exited $client_exit_code (10/12/14=connect fail, 11/13/15=body mismatch)"
    exit 1
  fi
  client_node="$(pod_field "$client_name" '{.spec.nodeName}')"
  if [[ -z "$client_node" ]]; then
    log "client $client_name has no scheduling node"
    exit 1
  fi
  # The cross-node claim comes from the actual scheduling outcome, not from
  # the manifest: a same-node schedule can never report a cross-node pass.
  if [[ "$client_node" == "$server_node" ]]; then
    log "client $client_name was scheduled on the server node $server_node; the cross-node pair cannot be asserted"
    exit 1
  fi
  client_log="$("${KUBECTL[@]}" logs "$client_name" -n "$NAMESPACE")"
  printf '%s\n' "$client_log" > "$OUTPUT_DIR/${client_name}.log"
  markers_seen=0
  for marker in "$NET_PODIP_OK" "$NET_SVCIP_OK" "$NET_DNS_OK" "$NET_SUCCESS"; do
    if printf '%s\n' "$client_log" | grep -Fqx "$marker"; then
      markers_seen=$((markers_seen + 1))
    fi
  done
  if [[ "$markers_seen" -ne 4 ]]; then
    log "client $client_name log has $markers_seen/4 expected markers"
    exit 1
  fi
  pair=$((pair + 1))
  append_summary "pair=$pair src=$client_node dst=$server_node client=$client_name markers=$markers_seen/4 result=pass at=$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
done
CURRENT_POD=""

append_summary "result=pass"
append_summary "finished_at=$(date -u +'%Y-%m-%dT%H:%M:%SZ')"

# Success cleanup targets exactly this run's namespace (created above, run
# labelled); user resources are never touched. A failed run never gets here.
"${KUBECTL[@]}" delete namespace "$NAMESPACE" --wait=true >/dev/null

log "generic network smoke passed: $pair cross-node pair(s)"
printf 'ANI_NETSMOKE_OUTPUT_DIR=%s\n' "$OUTPUT_DIR"
printf '%s\n' "$NET_SUCCESS"

#!/usr/bin/env bash
# ANI kcn-stack Envoy smoke probe (R11/A04).
#
# Since R11 this probe covers ONLY the kcn-specific Envoy data plane: it
# discovers the Envoy Service of the ani-smoke Gateway and requests the
# installed backend through it. The generic Pod/Service/DNS network
# verification moved to the stack-independent network-probe.sh (new file,
# per-run namespace), which verify.sh runs for BOTH stacks. Nothing here may
# grow a generic network check again — that would re-mix the two contracts.
set -Eeuo pipefail

readonly NAMESPACE="ani-installer-smoke"
readonly BACKEND_POD="ani-smoke-backend"
readonly BACKEND_SERVICE="ani-smoke-backend"
readonly GATEWAY="ani-smoke"
readonly LISTENER="http-b"
readonly LISTENER_PORT="9090"
readonly ENVOY_SUCCESS="ANI-ENVOY-OK"

KUBECONFIG_FILE="${KUBECONFIG_FILE:-/etc/kubernetes/admin.conf}"
OUTPUT_BASE="${ANI_SMOKE_OUTPUT:-/tmp/ani-smoke}"

if [[ ! -f "$KUBECONFIG_FILE" ]]; then
  echo "kubeconfig not found: $KUBECONFIG_FILE" >&2
  exit 1
fi

mkdir -p "$OUTPUT_BASE"
OUTPUT_DIR="$(mktemp -d "$OUTPUT_BASE/run-XXXXXX")"
# R12/A12: every kubectl request is bounded.
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_FILE" --request-timeout=60s)
CURRENT_KIND=""
CURRENT_POD=""

on_exit() {
  local rc=$?
  if (( rc != 0 )); then
    printf 'ANI smoke probe failed; exit=%s\n' "$rc" >> "$OUTPUT_DIR/summary.txt"
    if [[ -n "$CURRENT_POD" ]]; then
      "${KUBECTL[@]}" get pod "$CURRENT_POD" -n "$NAMESPACE" -o yaml > "$OUTPUT_DIR/${CURRENT_KIND:-client}-pod.yaml" 2>&1 || true
      "${KUBECTL[@]}" describe pod "$CURRENT_POD" -n "$NAMESPACE" > "$OUTPUT_DIR/${CURRENT_KIND:-client}-describe.log" 2>&1 || true
      "${KUBECTL[@]}" logs "$CURRENT_POD" -n "$NAMESPACE" > "$OUTPUT_DIR/${CURRENT_KIND:-client}-logs.log" 2>&1 || true
    fi
  fi
}
trap on_exit EXIT

log() {
  printf '[ani-smoke] %s\n' "$*"
}

append_summary() {
  printf '%s\n' "$*" >> "$OUTPUT_DIR/summary.txt"
}

wait_for_pod() {
  local pod="$1"
  local timeout="$2"
  local label="$3"
  local start=$SECONDS
  local phase
  while :; do
    phase="$("${KUBECTL[@]}" get pod "$pod" -n "$NAMESPACE" -o jsonpath='{.status.phase}')"
    if [[ "$phase" == "Succeeded" ]]; then
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

append_summary "started_at=$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
append_summary "kubeconfig=$KUBECONFIG_FILE"
append_summary "output_dir=$OUTPUT_DIR"

"${KUBECTL[@]}" get nodes -o wide > "$OUTPUT_DIR/nodes.txt"
"${KUBECTL[@]}" get pods -n "$NAMESPACE" -o wide > "$OUTPUT_DIR/pods-before.txt"
"${KUBECTL[@]}" get gateway "$GATEWAY" -n "$NAMESPACE" -o yaml > "$OUTPUT_DIR/gateway.yaml"
"${KUBECTL[@]}" get service -n "$NAMESPACE" -o yaml > "$OUTPUT_DIR/services.yaml"
"${KUBECTL[@]}" get endpointslice -n "$NAMESPACE" -o yaml > "$OUTPUT_DIR/endpointslices.yaml"

mapfile -t node_names < <("${KUBECTL[@]}" get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort)
if [[ "${#node_names[@]}" -ne 3 ]]; then
  log "expected exactly 3 nodes, got ${#node_names[@]}"
  exit 1
fi
ready_count="$("${KUBECTL[@]}" get nodes -o jsonpath='{range .items[*]}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}' | grep -cx True)"
if [[ "$ready_count" != "3" ]]; then
  log "expected 3 Ready nodes, got $ready_count"
  exit 1
fi

backend_ready="$("${KUBECTL[@]}" get pod "$BACKEND_POD" -n "$NAMESPACE" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')"
if [[ "$backend_ready" != "True" ]]; then
  log "backend Pod is not Ready"
  exit 1
fi
backend_phase="$("${KUBECTL[@]}" get pod "$BACKEND_POD" -n "$NAMESPACE" -o jsonpath='{.status.phase}')"
if [[ "$backend_phase" != "Running" ]]; then
  log "backend Pod is not Running"
  exit 1
fi
backend_ip="$("${KUBECTL[@]}" get pod "$BACKEND_POD" -n "$NAMESPACE" -o jsonpath='{.status.podIP}')"
backend_node="$("${KUBECTL[@]}" get pod "$BACKEND_POD" -n "$NAMESPACE" -o jsonpath='{.spec.nodeName}')"
busybox_image="$("${KUBECTL[@]}" get pod "$BACKEND_POD" -n "$NAMESPACE" -o jsonpath='{.spec.containers[?(@.name=="backend")].image}')"
if [[ -z "$backend_ip" || -z "$backend_node" || -z "$busybox_image" ]]; then
  log "backend Pod IP, node, or image is missing"
  exit 1
fi

backend_service_ip="$("${KUBECTL[@]}" get service "$BACKEND_SERVICE" -n "$NAMESPACE" -o jsonpath='{.spec.clusterIP}')"
if [[ -z "$backend_service_ip" ]]; then
  log "backend Service ClusterIP is missing"
  exit 1
fi

listener_jsonpath="$(printf '{range .spec.listeners[?(@.name=="%s")]}{.port}{"\\n"}{end}' "$LISTENER")"
listener_count="$("${KUBECTL[@]}" get gateway "$GATEWAY" -n "$NAMESPACE" -o jsonpath="$listener_jsonpath" | awk -v port="$LISTENER_PORT" '$1==port {count++} END {print count+0}')"
if [[ "$listener_count" != "1" ]]; then
  log "expected exactly one $LISTENER listener on port $LISTENER_PORT, got $listener_count"
  exit 1
fi

mapfile -t service_names < <("${KUBECTL[@]}" get service -n "$NAMESPACE" -o name | sed 's|^[^/]*/||')
if [[ "${#service_names[@]}" -eq 0 ]]; then
  log "no Services found in $NAMESPACE"
  exit 1
fi

envoy_candidates=()
for service_name in "${service_names[@]}"; do
  owning_gateway_name="$("${KUBECTL[@]}" get service "$service_name" -n "$NAMESPACE" -o jsonpath='{.metadata.labels.gateway\.envoyproxy\.io/owning-gateway-name}')"
  owning_gateway_namespace="$("${KUBECTL[@]}" get service "$service_name" -n "$NAMESPACE" -o jsonpath='{.metadata.labels.gateway\.envoyproxy\.io/owning-gateway-namespace}')"
  managed_by="$("${KUBECTL[@]}" get service "$service_name" -n "$NAMESPACE" -o jsonpath='{.metadata.labels.app\.kubernetes\.io/managed-by}')"
  component_name="$("${KUBECTL[@]}" get service "$service_name" -n "$NAMESPACE" -o jsonpath='{.metadata.labels.app\.kubernetes\.io/name}')"
  if [[ "$owning_gateway_name" == "$GATEWAY" && "$owning_gateway_namespace" == "$NAMESPACE" && "$managed_by" == "envoy-gateway" && "$component_name" == "envoy" ]]; then
    envoy_candidates+=("$service_name")
  fi
done

if [[ "${#envoy_candidates[@]}" -ne 1 ]]; then
  log "expected exactly one Envoy Service for Gateway $NAMESPACE/$GATEWAY, got ${#envoy_candidates[@]}: ${envoy_candidates[*]:-none}"
  exit 1
fi
envoy_service="${envoy_candidates[0]}"
envoy_service_ip="$("${KUBECTL[@]}" get service "$envoy_service" -n "$NAMESPACE" -o jsonpath='{.spec.clusterIP}')"
if [[ -z "$envoy_service_ip" ]]; then
  log "Envoy Service ClusterIP is missing"
  exit 1
fi
if [[ "$envoy_service" == "$BACKEND_SERVICE" || "$envoy_service_ip" == "$backend_service_ip" ]]; then
  log "selected Service is the backend Service rather than an Envoy Service"
  exit 1
fi
envoy_port_count="$("${KUBECTL[@]}" get service "$envoy_service" -n "$NAMESPACE" -o jsonpath='{range .spec.ports[*]}{.port}{"\n"}{end}' | awk -v port="$LISTENER_PORT" '$1==port {count++} END {print count+0}')"
if [[ "$envoy_port_count" != "1" ]]; then
  log "expected exactly one port $LISTENER_PORT on Envoy Service $envoy_service, got $envoy_port_count"
  exit 1
fi

mapfile -t envoy_endpointslices < <("${KUBECTL[@]}" get endpointslice -n "$NAMESPACE" -l "kubernetes.io/service-name=$envoy_service,gateway.envoyproxy.io/owning-gateway-name=$GATEWAY,gateway.envoyproxy.io/owning-gateway-namespace=$NAMESPACE" -o name | sed 's|^[^/]*/||')
if [[ "${#envoy_endpointslices[@]}" -ne 1 ]]; then
  log "expected exactly one EndpointSlice for Envoy Service $envoy_service, got ${#envoy_endpointslices[@]}"
  exit 1
fi
envoy_endpointslice="${envoy_endpointslices[0]}"
target_kinds="$("${KUBECTL[@]}" get endpointslice "$envoy_endpointslice" -n "$NAMESPACE" -o jsonpath='{range .endpoints[*]}{.targetRef.kind}{"\n"}{end}')"
if [[ -z "$target_kinds" ]]; then
  endpoint_count=0
  pod_count=0
else
  endpoint_count="$(printf '%s\n' "$target_kinds" | wc -l | tr -d ' ')"
  pod_count="$(printf '%s\n' "$target_kinds" | grep -cx Pod)"
fi
if [[ "$endpoint_count" -eq 0 || "$endpoint_count" -ne "$pod_count" ]]; then
  log "Envoy EndpointSlice does not exclusively target Pods"
  exit 1
fi
ready_endpoints="$("${KUBECTL[@]}" get endpointslice "$envoy_endpointslice" -n "$NAMESPACE" -o jsonpath='{range .endpoints[*]}{.conditions.ready}{"\n"}{end}')"
if [[ -z "$ready_endpoints" ]]; then
  ready_endpoint_count=0
else
  ready_endpoint_count="$(printf '%s\n' "$ready_endpoints" | grep -cx true)"
fi
if [[ "$ready_endpoint_count" -lt 1 ]]; then
  log "Envoy EndpointSlice has no ready endpoint"
  exit 1
fi

other_nodes=()
for node in "${node_names[@]}"; do
  if [[ "$node" != "$backend_node" ]]; then
    other_nodes+=("$node")
  fi
done
if [[ "${#other_nodes[@]}" -ne 2 ]]; then
  log "expected exactly two nodes other than backend node $backend_node"
  exit 1
fi
envoy_client_node="${other_nodes[0]}"

append_summary "backend_pod=$BACKEND_POD"
append_summary "backend_pod_ip=$backend_ip"
append_summary "backend_node=$backend_node"
append_summary "backend_service=$BACKEND_SERVICE"
append_summary "backend_service_ip=$backend_service_ip"
append_summary "gateway=$NAMESPACE/$GATEWAY"
append_summary "listener=$LISTENER:$LISTENER_PORT"
append_summary "envoy_service=$envoy_service"
append_summary "envoy_service_ip=$envoy_service_ip"
append_summary "envoy_port=$LISTENER_PORT"
append_summary "envoy_endpointslice=$envoy_endpointslice"
old_client_uid="$("${KUBECTL[@]}" get pod ani-smoke-client -n "$NAMESPACE" -o jsonpath='{.metadata.uid}' 2>/dev/null || true)"
append_summary "old_client_uid=${old_client_uid:-not_found}"

CURRENT_KIND="envoy-client"
CURRENT_POD=""
envoy_client_name="$("${KUBECTL[@]}" create -f - -o jsonpath='{.metadata.name}' <<EOF
apiVersion: v1
kind: Pod
metadata:
  generateName: ani-smoke-client-
  namespace: $NAMESPACE
spec:
  nodeName: $envoy_client_node
  restartPolicy: Never
  containers:
    - name: client
      image: "$busybox_image"
      imagePullPolicy: IfNotPresent
      command: ["/bin/sh", "-ec"]
      args:
        - |
          response="\$(wget -T 10 -qO- http://$envoy_service_ip:$LISTENER_PORT/)"
          test "\$response" = "ANI-INSTALLER-OK"
          printf 'ANI-INSTALLER-OK\n'
          printf 'ANI-ENVOY-OK\n'
EOF
)"
CURRENT_POD="$envoy_client_name"
envoy_client_uid="$("${KUBECTL[@]}" get pod "$envoy_client_name" -n "$NAMESPACE" -o jsonpath='{.metadata.uid}')"
envoy_client_created="$("${KUBECTL[@]}" get pod "$envoy_client_name" -n "$NAMESPACE" -o jsonpath='{.metadata.creationTimestamp}')"
envoy_client_node_actual="$("${KUBECTL[@]}" get pod "$envoy_client_name" -n "$NAMESPACE" -o jsonpath='{.spec.nodeName}')"
envoy_client_image="$("${KUBECTL[@]}" get pod "$envoy_client_name" -n "$NAMESPACE" -o jsonpath='{.spec.containers[?(@.name=="client")].image}')"
if [[ -n "$old_client_uid" && "$envoy_client_uid" == "$old_client_uid" ]]; then
  log "Envoy client UID matches the old client UID"
  exit 1
fi
append_summary "envoy_client_name=$envoy_client_name"
append_summary "envoy_client_uid=$envoy_client_uid"
append_summary "envoy_client_created=$envoy_client_created"
append_summary "envoy_client_node=$envoy_client_node_actual"
append_summary "envoy_client_image=$envoy_client_image"

wait_for_pod "$envoy_client_name" 180 "Envoy client"
envoy_phase="$("${KUBECTL[@]}" get pod "$envoy_client_name" -n "$NAMESPACE" -o jsonpath='{.status.phase}')"
envoy_exit_code="$("${KUBECTL[@]}" get pod "$envoy_client_name" -n "$NAMESPACE" -o jsonpath='{.status.containerStatuses[0].state.terminated.exitCode}')"
if [[ "$envoy_phase" != "Succeeded" || "$envoy_exit_code" != "0" ]]; then
  log "Envoy client did not finish successfully (phase=$envoy_phase exit=$envoy_exit_code)"
  exit 1
fi
envoy_log="$("${KUBECTL[@]}" logs "$envoy_client_name" -n "$NAMESPACE")"
printf '%s\n' "$envoy_log" > "$OUTPUT_DIR/envoy-client.log"
for marker in ANI-INSTALLER-OK "$ENVOY_SUCCESS"; do
  if ! printf '%s\n' "$envoy_log" | grep -Fqx "$marker"; then
    log "Envoy client log missing marker: $marker"
    exit 1
  fi
done

"${KUBECTL[@]}" get pods -n "$NAMESPACE" -o wide > "$OUTPUT_DIR/pods-after.txt"
append_summary "result=pass"
append_summary "envoy_result=pass"
append_summary "finished_at=$(date -u +'%Y-%m-%dT%H:%M:%SZ')"

log "Envoy probe passed"
printf 'ANI_SMOKE_OUTPUT_DIR=%s\n' "$OUTPUT_DIR"
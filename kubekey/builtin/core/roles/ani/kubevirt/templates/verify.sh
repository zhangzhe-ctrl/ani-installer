#!/usr/bin/env bash
# Package-sourced DataVolume, actual guest command, persistent disk after a
# normal VM stop/start. The DV/PVC is deliberately retained for inspection.
set -euo pipefail
k() { kubectl --kubeconfig '{{ .ani.run.kubeconfig }}' "$@"; }
V='{{ .ani.artifact_root }}/bin/virtctl'
export KUBECONFIG='{{ .ani.run.kubeconfig }}'
ART='{{ .ani.artifact_root }}'
LOG='{{ .ani.run.logs_dir }}/b05-kubevirt.log'
# Smoke reads the installed VM and its persisted guest marker. The first
# install already exercised the normal stop/start path and recorded its PVC UID.
# A repeat smoke must not apply resources or restart that running VM.
if [ "${ANI_VERIFY_LEVEL:-}" = smoke ]; then
  OUT="${ANI_VERIFY_OUTPUT_DIR:?smoke evidence directory is required}"
  install -d -m 0700 "$OUT"
  LINE="$(grep -E '^B05 PASS: DataVolume Succeeded, guest command executed, PVC [0-9a-f-]+ and marker ani-b05-[0-9TZ]+ preserved across stop/start$' "$LOG" | tail -n 1)" || {
    echo 'B05 first-install stop/start evidence is missing' >&2; exit 1;
  }
  if [[ ! "$LINE" =~ PVC[[:space:]]([0-9a-f-]+)[[:space:]]and[[:space:]]marker[[:space:]](ani-b05-[0-9TZ]+)[[:space:]]preserved ]]; then
    echo 'B05 first-install evidence is malformed' >&2; exit 1
  fi
  EXPECTED_PVC_UID="${BASH_REMATCH[1]}"
  EXPECTED_MARK="${BASH_REMATCH[2]}"
  k -n kubevirt wait --for=condition=Available kubevirt/kubevirt --timeout=60s
  k -n cdi rollout status deployment/cdi-deployment --timeout=60s
  test "$(k -n ani-platform get dv ani-b05-guest -o jsonpath='{.status.phase}')" = Succeeded
  OWNER='{{ .kubernetes.cluster_name }}'
  test "$(k -n ani-platform get vm ani-b05-guest -o jsonpath='{.metadata.labels.ani\.io/managed-by}')" = "$OWNER"
  test "$(k -n ani-platform get dv ani-b05-guest -o jsonpath='{.metadata.labels.ani\.io/managed-by}')" = "$OWNER"
  test "$(k -n ani-platform get vm ani-b05-guest -o jsonpath='{.status.printableStatus}')" = Running
  k -n ani-platform wait --for=condition=Ready vmi/ani-b05-guest --timeout=60s
  VMI_UID="$(k -n ani-platform get vmi ani-b05-guest -o jsonpath='{.metadata.uid}')"
  PVC_UID="$(k -n ani-platform get pvc ani-b05-guest -o jsonpath='{.metadata.uid}')"
  test -n "$VMI_UID" && test "$PVC_UID" = "$EXPECTED_PVC_UID" || {
    echo 'B05 VM or PVC identity changed since the first install' >&2; exit 1;
  }
  KEY=/etc/kubernetes/ani/kubevirt/guest-ssh-key
  KNOWN=/etc/kubernetes/ani/kubevirt/known_hosts
  test -s "$KEY"
  GUEST_OUT="$(timeout 40 "$V" ssh --namespace ani-platform --identity-file "$KEY" \
    --known-hosts "$KNOWN" --local-ssh-opts='-o StrictHostKeyChecking=yes' \
    --command 'uname -m; id -un; cat /home/cirros/ani-b05-marker' cirros@vm/ani-b05-guest)"
  printf '%s\n' "$GUEST_OUT" > "$OUT/guest-command.txt"
  printf '%s\n' "$GUEST_OUT" | grep -qx x86_64
  printf '%s\n' "$GUEST_OUT" | grep -qx cirros
  READBACK="$(printf '%s\n' "$GUEST_OUT" | tail -n 1 | tr -d '\r')"
  test "$READBACK" = "$EXPECTED_MARK" || {
    echo "B05 guest disk marker changed: $READBACK" >&2; exit 1;
  }
  test "$(k -n ani-platform get vmi ani-b05-guest -o jsonpath='{.metadata.uid}')" = "$VMI_UID"
  test "$(k -n ani-platform get pvc ani-b05-guest -o jsonpath='{.metadata.uid}')" = "$PVC_UID"
  printf 'B05 smoke PASS: existing VM guest command and marker %s on PVC %s\n' "$EXPECTED_MARK" "$PVC_UID"
  exit 0
fi
mkdir -p "$(dirname "$LOG")"
exec > >(tee -a "$LOG") 2>&1
sha256sum "$ART/guest/cirros-0.6.3-x86_64-disk.img"
k -n ani-platform wait --for=condition=Ready pod/ani-b05-guest-source --timeout=180s
k -n ani-platform cp "$ART/guest/cirros-0.6.3-x86_64-disk.img" ani-b05-guest-source:/srv/cirros.img -c httpd
ACTUAL="$(k -n ani-platform exec ani-b05-guest-source -c httpd -- sha256sum /srv/cirros.img | awk '{print $1}')"
test "$ACTUAL" = 7d6355852aeb6dbcd191bcda7cd74f1536cfe5cbf8a10495a7283a8396e4b75b

cat > /etc/kubernetes/ani/kubevirt/dv.yaml <<'DV'
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: ani-b05-guest
  namespace: ani-platform
  labels:
    ani.io/managed-by: '{{ .kubernetes.cluster_name }}'
spec:
  source:
    http:
      url: http://ani-b05-guest-source.ani-platform.svc.cluster.local:8080/cirros.img
  pvc:
    accessModes: [ReadWriteOnce]
    resources:
      requests:
        storage: '{{ (index .ani.components "kubevirt").storage_size }}'
    storageClassName: '{{ (index .ani.components "kubevirt").storage_class }}'
DV
k apply --server-side -f /etc/kubernetes/ani/kubevirt/dv.yaml
deadline=$((SECONDS + 600))
phase=''
while [ "$SECONDS" -lt "$deadline" ]; do
  phase="$(k -n ani-platform get dv ani-b05-guest -o jsonpath='{.status.phase}')"
  if [ "$phase" = Succeeded ]; then break; fi
  case "$phase" in Failed|Unknown)
    k -n ani-platform describe dv ani-b05-guest
    echo "CDI DataVolume import ended in $phase" >&2; exit 1;;
  esac
  sleep 5
done
test "$phase" = Succeeded || { echo "CDI DataVolume import timed out in $phase" >&2; exit 1; }
PVC_UID="$(k -n ani-platform get pvc ani-b05-guest -o jsonpath='{.metadata.uid}')"
test -n "$PVC_UID"

KEY=/etc/kubernetes/ani/kubevirt/guest-ssh-key
if [ ! -s "$KEY" ]; then
  ssh-keygen -q -t ed25519 -N '' -f "$KEY"
fi
chmod 0600 "$KEY"
PUB="$(cat "$KEY.pub")"
cat > /etc/kubernetes/ani/kubevirt/vm.yaml <<VM
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: ani-b05-guest
  namespace: ani-platform
  labels:
    ani.io/managed-by: '{{ .kubernetes.cluster_name }}'
spec:
  runStrategy: Manual
  template:
    metadata:
      labels:
        kubevirt.io/domain: ani-b05-guest
    spec:
      nodeSelector:
        kubernetes.io/hostname: '{{ (index .ani.components "kubevirt").vm_node }}'
      domain:
        cpu:
          cores: 1
        resources:
          requests:
            memory: 256Mi
        devices:
          disks:
            - name: rootdisk
              disk: {bus: virtio}
            - name: cloudinit
              disk: {bus: virtio}
          interfaces:
            - name: default
              masquerade: {}
      networks:
        - name: default
          pod: {}
      volumes:
        - name: rootdisk
          dataVolume:
            name: ani-b05-guest
        - name: cloudinit
          cloudInitNoCloud:
            # The pinned CirrOS image executes NoCloud user-data as a shell
            # script; it does not apply full cloud-config YAML modules.
            userData: |
              #!/bin/sh
              set -eu
              mkdir -p /home/cirros/.ssh
              printf '%s\n' '$PUB' > /home/cirros/.ssh/authorized_keys
              chown -R cirros:cirros /home/cirros/.ssh
              chmod 0700 /home/cirros/.ssh
              chmod 0600 /home/cirros/.ssh/authorized_keys
VM
k apply --server-side -f /etc/kubernetes/ani/kubevirt/vm.yaml
wait_vmi_ready() {
  local deadline=$((SECONDS + 180))
  # The VM controller creates the VMI asynchronously after virtctl start.
  # A named kubectl wait fails immediately with NotFound before that happens.
  until k -n ani-platform get vmi ani-b05-guest >/dev/null 2>&1; do
    [ "$SECONDS" -lt "$deadline" ] || { echo 'VMI did not appear after start' >&2; return 1; }
    sleep 2
  done
  k -n ani-platform wait --for=condition=Ready vmi/ani-b05-guest --timeout=360s
}
"$V" start ani-b05-guest -n ani-platform
wait_vmi_ready

KNOWN=/etc/kubernetes/ani/kubevirt/known_hosts
run_guest() {
  local cmd="$1" deadline=$((SECONDS + 180))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if timeout 25 "$V" ssh --namespace ani-platform --identity-file "$KEY" \
      --known-hosts "$KNOWN" --local-ssh-opts='-o StrictHostKeyChecking=accept-new' \
      --command "$cmd" cirros@vm/ani-b05-guest; then return 0; fi
    sleep 5
  done
  return 1
}
run_guest 'uname -m; id -un'
MARK="ani-b05-$(date -u +%Y%m%dT%H%M%SZ)"
run_guest "printf '%s\n' '$MARK' > /home/cirros/ani-b05-marker && sync && cat /home/cirros/ani-b05-marker"
"$V" stop ani-b05-guest -n ani-platform
deadline=$((SECONDS + 180))
while k -n ani-platform get vmi ani-b05-guest >/dev/null 2>&1; do
  [ "$SECONDS" -lt "$deadline" ] || { echo 'VMI did not stop normally' >&2; exit 1; }
  sleep 4
done
test "$(k -n ani-platform get pvc ani-b05-guest -o jsonpath='{.metadata.uid}')" = "$PVC_UID"
"$V" start ani-b05-guest -n ani-platform
wait_vmi_ready
READBACK="$(run_guest 'cat /home/cirros/ani-b05-marker' | tail -n 1)"
test "$READBACK" = "$MARK" || { echo "disk marker mismatch after VM restart: $READBACK" >&2; exit 1; }
test "$(k -n ani-platform get pvc ani-b05-guest -o jsonpath='{.metadata.uid}')" = "$PVC_UID"
echo "B05 PASS: DataVolume Succeeded, guest command executed, PVC $PVC_UID and marker $MARK preserved across stop/start"

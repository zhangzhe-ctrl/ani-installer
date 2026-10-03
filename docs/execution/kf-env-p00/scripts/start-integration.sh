#!/usr/bin/env bash
# Lab-only bounded invocation of the reviewed unified first-install role.
# This is not shipped in the product or an append-install command.
set -euo pipefail
base=/home/ubuntu/kf-env-p00/integration-attempt-01
artifact="$base/artifact"
hauler=/home/ubuntu/ani-kubeovn-rustfs-20260929-full-4003b12/artifact-c165ae7-candidate-r2/bin/hauler
unit=ani-kfp-lab-registry-20261003.service
test "$(id -u)" -eq 0
test "$(hostname)" = ani-01
test "$(sha256sum "$hauler" | awk '{print $1}')" = 4eaf2d370feb3fe26ad58863e79e49c8c7f4579b675dc42a46a798c437e03a61
test "$(sha256sum "$base/site.json" | awk '{print $1}')" = dddf711c5217afff54536096f57bcbe07648f1c1f02ef5cde4786aa867b2da27
test ! -e "/etc/systemd/system/$unit"
test ! -e "$base/registry-store"
test -z "$(ss -ltnH 'sport = :5001')"
test "$(kubectl --kubeconfig=/etc/kubernetes/admin.conf get namespace kube-system -o jsonpath='{.metadata.uid}')" = 87ecef8e-ac4e-442b-8e15-5e906263be6b
cd "$base/input"
sha256sum -c SHA256SUMS
"$hauler" store load --store "$base/registry-store" --filename "$base/input/kubeflow-images.haul.tar.zst" > "$base/logs/registry-load.log" 2>&1
cat > "/etc/systemd/system/$unit" <<EOF
[Unit]
Description=ANI KF ENV P00 isolated integration material registry
After=network-online.target
[Service]
Type=simple
WorkingDirectory=$base
ExecStart=$hauler store serve registry --port 5001 --directory $base/registry-backend --readonly=true --store $base/registry-store
Restart=no
[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl start "$unit"
deadline=$((SECONDS + 120))
until curl --fail --max-time 5 --silent http://172.16.101.10:5001/v2/ > /dev/null; do
  test "$SECONDS" -lt "$deadline"
  systemctl is-active --quiet "$unit"
  sleep 2
done
# The development projection is only the 15 source-locked Kubeflow images.
# The final first-install package must pass the complete material content gate.
"$base/input/kk" ani materials verify-registry --registry-address 172.16.101.10:5001 \
  --images-tsv "$base/input/development-kubeflow.images.tsv" \
  --lock "$base/input/development-kubeflow.lock.yaml" \
  --evidence-dir "$base/input/image-evidence" --verify-blob-bytes > "$base/logs/registry-content.log" 2>&1
export ANI_KUBEFLOW_DEVELOPMENT=1 PYTHONDONTWRITEBYTECODE=1
python3 "$artifact/manifests/kubeflow/26.03-kfp2.16-trainer2.1-v1/install.py" --site "$base/site.json"

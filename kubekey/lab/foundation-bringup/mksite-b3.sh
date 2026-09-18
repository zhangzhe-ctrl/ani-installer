#!/usr/bin/env bash
set -euo pipefail
R=~/ani-installer-runs/foundation-20260918
A=~/ani-installer-runs/platform-20260918/access
PW="$(cat "$A/node-password")"
mkdir -p "$R/inputs"
umask 077
cat > "$R/inputs/site-b3-cluster.yaml" <<YAML
name: ani-lab
installerNode: node1
ssh:
  user: ubuntu
  port: 22
  password: $PW
nodes:
  - name: node1
    address: 172.16.101.20
  - name: node2
    address: 172.16.101.21
  - name: node3
    address: 172.16.101.22
network:
  managementInterface: ens34
  podCIDR: 10.16.0.0/16
  serviceCIDR: 10.96.0.0/16
  kcn:
    managedDevices: [ens35]
    encapNetworks: [172.16.101.0/24]
    intranetNetworks: [172.16.101.0/24, 10.96.0.0/16]
registry:
  port: 5000

components:
  certManager:
    enabled: true
  postgresql:
    enabled: true
    storageClass: ani-block
    storageSize: 10Gi
  valkey:
    enabled: true
    storageClass: ani-block
    storageSize: 2Gi
  nats:
    enabled: false
YAML
sha256sum "$R/inputs/site-b3-cluster.yaml"
echo "B3_SITE_WRITTEN"

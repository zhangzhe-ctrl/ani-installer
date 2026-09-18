#!/usr/bin/env bash
#
# mksite.sh — 生成 B1 站点配置 inputs/site-b1-cluster.yaml（仅启用 certManager）。
# 节点密码运行时从 access/node-password 注入，不写死在脚本里。
# 参考副本（原件：~/ani-installer-runs/foundation-20260918/lab/mksite.sh on fedora）。
# 调整 R / A 到你的工作区即可复用；改 components 块可测其它批次。
set -euo pipefail
R=/home/chabking/ani-installer-runs/foundation-20260918
A=/home/chabking/ani-installer-runs/platform-20260918/access
PW="$(cat "$A/node-password")"
mkdir -p "$R/inputs"
umask 077
cat > "$R/inputs/site-b1-cluster.yaml" <<EOF
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
    enabled: false
  valkey:
    enabled: false
  nats:
    enabled: false
EOF
stat -c '%a %n' "$R/inputs/site-b1-cluster.yaml"
sha256sum "$R/inputs/site-b1-cluster.yaml"
echo "lines=$(wc -l < "$R/inputs/site-b1-cluster.yaml")"

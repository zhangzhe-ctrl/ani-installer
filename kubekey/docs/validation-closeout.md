# ANI offline installer validation closeout

Date: 2026-09-15. Source: `kubekey/` at repository commit `8ad7c899977450d4a6554af881cd0c770ee62004` with the closeout working-tree changes. No commit or push was made.

## Scope

This closeout fixes two false-positive risks in the existing install's active validation:

1. Any failed network request or unexpected response now fails the probe and `verify.sh`.
2. The Envoy request now selects the Service owned by the `ani-smoke` Gateway with all four required labels and the exact `9090` port; it cannot fall back to `ani-smoke-backend`.

Both install-time smoke validation and standalone package verification use the same `probe.sh`. Every verification creates two fresh clients and trusts only those clients' UIDs, commands, exit codes, and logs. Historical `Succeeded` Pods are reference evidence only.

## Result table

| Item | Result |
|---|---|
| Code and regression tests | pass |
| Revised `s2f` package build and checksums | pass |
| Existing cluster active network verification | pass |
| Existing cluster active Envoy verification | pass |
| Repeat run created fresh clients | pass |
| Historical first offline install | pass (historical; preserved) |
| Historical clean-snapshot offline reinstall | pass (historical; preserved) |
| Current installer-node external isolation condition | fail (external request unexpectedly succeeded) |
| New clean installation with revised code | not_verified |
| Clean-snapshot revalidation of the later local-connector fix | not_verified |

The historical S1–S6 records remain unchanged. Their active network/Envoy evidence had the two selection/failure-propagation risks described above; the two new client runs in this closeout are the corrected active evidence for the existing cluster.

## Code and tests

Changed implementation:

- Added `builtin/core/roles/ani/smoke/templates/probe.sh` as the common active probe.
- Updated the smoke role to render and run that script.
- Updated `scripts/verify.sh` to execute the packaged common probe, capture the real exit code, and write a unique verify log directory.
- Updated `scripts/build-offline.sh` to package the common probe.
- Removed the obsolete fixed-name client manifests.
- Added `pkg/ani/smoke_probe_test.go` with successful-path and failure regressions, Envoy Service selection checks, old-Pod non-reuse checks, and packaging checks.
- Updated `ani/README.md`.

Regression coverage includes request failure and wrong-response cases for the backend Pod IP, backend Service IP, DNS name, and Envoy Service, zero/multiple/missing-port Envoy Service candidates, and an old `Succeeded` client followed by a new failing client.

On SSH host `fedora`, in `/home/chabking/ani-installer/kubekey`:

```text
go test ./pkg/ani                     -> pass
go test -tags=builtin ./pkg/ani       -> pass
bash -n scripts/verify.sh             -> pass
bash -n scripts/build-offline.sh      -> pass
bash -n builtin/core/roles/ani/smoke/templates/probe.sh -> pass
```

The fully qualified EndpointSlice name returned by the real cluster (`endpointslice.discovery.k8s.io/<name>`) is now covered by the tests. The first real run exposed this incompatibility; the probe was corrected and the tests above were rerun before the two successful verifications.

## Revised package

The revised package was built as a copy of the existing verified `s2e` package, replacing only the revised `kk`, `verify.sh`, `README.md`, and `manifests/ani/smoke/`. No images were recollected and the historical `s2e` package was not overwritten.

Fedora package:

`/home/chabking/ani-installer/ani-offline-ubuntu24-amd64-20260915-s2f`

Installer-node package:

`/home/ubuntu/ani-installer/ani-offline-ubuntu24-amd64-20260915-s2f`

Validated on both hosts:

- `sha256sum -c SHA256SUMS`: pass (28 files)
- `bash -n verify.sh`: pass
- `bash -n manifests/ani/smoke/templates/probe.sh`: pass
- `bin/kk version -s`: `v4.0.7-closeout`
- `bin/kk ani install --help`: pass
- no obsolete `client-pod.yaml` or `network-client-pod.yaml`

Final SHA-256 values:

```text
bin/kk                                      aec593c3550adaf71f9cfd2b8b0fa67ced86da81328e4f09d66bd992e5997ace
verify.sh                                  9f7e5f84805aef93d18d4262e55565ae9c4267ca713527d53fdcb444d0d77670
manifests/ani/smoke/templates/probe.sh    a93d9a6860095ea7d7cd6f57175982d8ab44651f59a601e92fd13ee810214850
```

## Existing environment observation

Preflight evidence was captured at:

`/home/ubuntu/ani-installer/closeout-preflight-20260915T113857Z-50645`

The cluster still had three Ready nodes with the expected names and InternalIPs. The selected Envoy Service was `ani-smoke` (`10.96.129.239:9090`), and the backend Service was `ani-smoke-backend` (`10.96.153.157:80`). The backend Pod was `ani-smoke-backend` (`10.16.0.9`, UID `3d6ab62a-fae8-47d5-805c-4aad10962078`) on `test-installer-01`.

Old client UIDs retained only as reference, not success evidence:

```text
ani-smoke-network-client  c2f54e35-71fa-4794-ab17-f2b3c687697e
ani-smoke-client          402b9d5c-fea9-4e6c-9b33-9a053f1f0809
```

The installer node currently has a default IPv4 route and a timeout-bounded `curl http://example.com` unexpectedly returned exit code `0`. `iptables` also showed `OUTPUT ACCEPT`; no network, route, firewall, DNS, or cluster-network component was changed to force an offline result. Therefore, the two closeout verifications below are active cluster network and Envoy tests, not current external-isolation tests. The historical offline install and clean-snapshot reinstall records are preserved separately.

`nft list ruleset` exited `139` during read-only evidence capture; `iptables -S` and `iptables-save` succeeded. This is a host-tool observation only and did not affect the active Kubernetes traffic verification.

## Two active verifications

Command used on the installer node:

```bash
cd /home/ubuntu/ani-installer/ani-offline-ubuntu24-amd64-20260915-s2f
KUBECONFIG_FILE=/home/ubuntu/.kube/config ./verify.sh /home/ubuntu/ani-installer/cluster.yaml
```

### Run 1

- Verify exit code: `0`
- Probe result: pass
- Fresh network client: `ani-smoke-network-client-4whrk`
  - UID: `59a56f32-0095-4eba-9d19-c865a4dede55`
  - Node: `test-installer-03`
  - Phase/exit: `Succeeded` / `0`
  - Log: `NETWORK-POD-IP-OK`, `NETWORK-SERVICE-IP-OK`, `NETWORK-DNS-OK`, `ANI-NETWORK-OK`
- Fresh Envoy client: `ani-smoke-client-t49nz`
  - UID: `62ae647d-3e37-4fcb-9d55-a159df7255e1`
  - Node: `test-installer-02`
  - Phase/exit: `Succeeded` / `0`
  - Log: `ANI-INSTALLER-OK`, `ANI-ENVOY-OK`

### Run 2

- Verify exit code: `0`
- Probe result: pass
- Fresh network client: `ani-smoke-network-client-8gdgk`
  - UID: `dd3c8846-5e73-4651-9592-54cdfa90a532`
  - Node: `test-installer-03`
  - Phase/exit: `Succeeded` / `0`
  - Log: `NETWORK-POD-IP-OK`, `NETWORK-SERVICE-IP-OK`, `NETWORK-DNS-OK`, `ANI-NETWORK-OK`
- Fresh Envoy client: `ani-smoke-client-5r6zn`
  - UID: `a93fcde7-ec3d-481b-a8e3-924f1b79ac12`
  - Node: `test-installer-02`
  - Phase/exit: `Succeeded` / `0`
  - Log: `ANI-INSTALLER-OK`, `ANI-ENVOY-OK`

The two runs created four new clients. Both network-client UIDs differ, and both Envoy-client UIDs differ. None matches the historical client UID values.

The real network client commands used:

- backend Pod IP: `http://10.16.0.9:3000/`
- backend Service IP: `http://10.96.153.157/`
- DNS: `http://ani-smoke-backend.ani-installer-smoke.svc.cluster.local/`

The real Envoy client command used:

- selected Envoy Service: `http://10.96.129.239:9090/`

The selected Service remained `ani-smoke`, not `ani-smoke-backend`. Its EndpointSlice `ani-smoke-vz42m` had two ready Pod targetRefs.

## Evidence paths

Installer-node raw evidence:

- Run 1: `/home/ubuntu/ani-installer/closeout-verify-run1-fixed-20260915T114125Z-59279`
- Run 2: `/home/ubuntu/ani-installer/closeout-verify-run2-fixed-20260915T114140Z-59432`
- Comparison: `/home/ubuntu/ani-installer/closeout-verification-comparison-20260915T114214Z-59936`
- Run 1 package probe: `/home/ubuntu/ani-installer/ani-offline-ubuntu24-amd64-20260915-s2f/logs/ani-lab/verify-20260915-194126-202393-15861/run-R9hDYN`
- Run 2 package probe: `/home/ubuntu/ani-installer/ani-offline-ubuntu24-amd64-20260915-s2f/logs/ani-lab/verify-20260915-194141-203426-24606/run-am1oA7`

The first pre-fix verification attempt failed with exit `1` at `/home/ubuntu/ani-installer/closeout-verify-run1-20260915T113930Z-50855`; it is retained as failure evidence, not counted as a successful active verification.

## Not verified in this round

This round did not reinstall the cluster, restore a snapshot, or run a new clean installation. A new clean install with the revised probe remains `not_verified`, as does a clean-snapshot revalidation of the later local-connector fix.
## 2026-09-17 clean-snapshot offline precondition check

The user restored the three designated Ubuntu nodes to their pre-install snapshots. Clean-state checks showed `test-installer-01/02/03` on `172.16.101.20/21/22`, `ens34` UP with the expected management address, no `/etc/kubernetes`, no `/var/lib/ani-installer`, and inactive `containerd`/`kubelet`.

Before any release transfer or installation, a read-only offline check was run on each node. All three nodes routed `1.1.1.1` through `172.16.101.1` on `ens34`, resolved `example.com`, and received HTTP/2 200 from `https://example.com`. Therefore the required offline precondition failed. No route, DNS, firewall, service, filesystem, or cluster state was changed, and neither `install.sh` nor `verify.sh` was run.

Evidence on `fedora`:

`/home/chabking/ani-installer-runs/platform-20260917/logs/offline-precheck-20260917-200926.log`

Result: clean offline first install `not_verified`; current blocker is external network access in the restored environment. This is an environment precondition failure, not evidence of an installer or component defect.

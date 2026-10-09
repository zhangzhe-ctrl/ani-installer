# ANI Multus B01a

- Primary CNI: `{{ .ani.network.stack }}` (retained; Multus is the CNI wrapper).
- NAD API: `k8s.cni.cncf.io/v1`, namespaced.
- Functional sample: `ani-platform/ani-b01-local`, test CIDR `{{ .ani.network.multus.test_cidr }}`.
{{ if eq .ani.network.stack "kcn" }}
- The sample uses native `kc-networking`, VPC `ani-platform/ani-b01-vpc` and Subnet `ani-platform/ani-b01-secondary`. The probe pins `net1` to that Subnet and keeps the primary `eth0` network.
{{ else }}
- The sample uses `bridge` plus `host-local` IPAM **on one node only**. It does not provide cross-node address uniqueness or a production secondary network design.
{{ end }}
- Check result and Pod evidence: `{{ .ani.run.logs_dir }}`.
- External LB: B01b is independent and not enabled by this role.

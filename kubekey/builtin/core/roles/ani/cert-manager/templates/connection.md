## cert-manager

- namespace: `cert-manager`
- version: chart `v1.21.2` (app `v1.21.2`)
- workloads: `deployment/cert-manager`, `deployment/cert-manager-webhook`, `deployment/cert-manager-cainjector`
- webhook service: `cert-manager-webhook.cert-manager.svc.cluster.local:443`
- internal CA (this build's only issuer chain):
  - root CA Secret: `cert-manager/ani-root-ca` (keys `tls.crt`, `tls.key`, `ca.crt`; validity 87600h, renewBefore 720h, ECDSA P-256)
  - bootstrap Issuer: `issuer/ani-ca-bootstrap` (self-signed, namespace `cert-manager`)
  - cluster-wide signer: `clusterissuer/ani-ca` (CA-backed by the root Secret)
  - test leaf (proof only): `ani-cert-test/ani-ca-test-leaf`
- PVC: none
- retention: n/a
- credentials: certificate private keys live only in the Secrets above; this document never contains key material
- verification: `verify.sh` (packaged at `/etc/kubernetes/ani/cert-manager/verify.sh`) checks workload availability, the issuer/certificate conditions, both key pairs, and runs an offline `openssl` Job that validates the leaf chain, SANs and CA basic constraints against the internal root

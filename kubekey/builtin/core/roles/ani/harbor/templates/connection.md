# ANI Harbor connection (B07)

- HTTPS registry and API: `https://{{ (index .ani.components "harbor").external_address }}:30003`; API root `/api/v2.0`.
- CA on installerNode: `/etc/kubernetes/ani/harbor/ca.crt`; the same CA is installed into each selected node runtime trust. Clients must validate this CA and the IP SAN.
- Admin secret: Kubernetes `ani-harbor/ani-harbor-admin`; the installerNode copy is `/etc/kubernetes/ani/harbor/admin-password` (mode 0600). Do not put this credential in ANI business configuration.
- Dedicated internal PostgreSQL, Valkey, and filesystem PVCs are Chart managed within `ani-harbor`. The original bootstrap registry remains separate.
- ANI image pull: use a project scoped, pull-only Harbor robot. The B07 proof robot and Kubernetes image pull Secret are `ani-b07-pull` and `ani-harbor/ani-harbor-pull`; credential file paths are `/etc/kubernetes/ani/harbor/robot-pull-{name,secret}` on installerNode. Create a new project scoped robot for production ANI projects using `/api/v2.0/robots`, and give its Secret only to the required namespaces.
- Artifact API: `GET /api/v2.0/projects/{project}/repositories/{repository}/artifacts/{reference}?with_scan_overview=true`. Request a scan with `POST` on the corresponding `/scan` endpoint, then inspect `scan_overview` for terminal `Success` and report ID. Retrieve `/additions/vulnerabilities` for the report. The project vulnerability policy remains enabled.
- B07 proof results and raw API responses: `{{ .ani.run.logs_dir }}/b07-harbor/`. The offline scanner DB bytes are package supplied under `scanner/db` and `scanner/java-db`; the locked metadata dates are recorded in `components.lock.yaml`.

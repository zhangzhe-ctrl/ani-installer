# B07 material investigation (2026-09-28)

Status: official Chart and nine image manifest identities read on Fedora; Trivy DB manifests identified but payloads not yet landed; implementation, formal package and live checks **not_run**.

- Official Harbor Helm Chart `harbor-1.19.2.tgz` from `https://helm.goharbor.io/harbor-1.19.2.tgz`: SHA256 `36d8eeb41b4df1aeff18c9af7709110a2fac2194b491d37957822b3359cd5e9a`; its `Chart.yaml` declares Harbor `appVersion: 2.15.2`. The official chart values support HTTPS NodePort and internal dedicated `harbor-db` / `valkey-photon`.
- An actual Helm 3.20.0 NodePort/TLS-secret render produces these nine Docker Hub `goharbor/*:v2.15.2` images. Each source is a single linux/amd64 manifest, so the listed digest is both the source manifest and the amd64 manifest (checked using `skopeo inspect --raw` and `skopeo inspect`):
  - `nginx-photon`: `0ccc1eca228ecea120d7ad9e4386b8e86ced200212f00060a9abc753dbe7f17f`
  - `harbor-portal`: `b811b67f7a7f6f1614e4bdf7478b207d13e8aa3f815ff6ad84f216a5098d213d`
  - `harbor-core`: `d7b780d23721a000f0fb8e181add6675eafa336de5d033b77fd7da0c171ff7f7`
  - `harbor-jobservice`: `f71a4452a095bcd2a8f0683648a3ae28415432df87ccfb00ec7625f3e340a598`
  - `registry-photon`: `c4ebef61ceb50a3d8bc21d149bc280422db20a1e69df317b64b74098c3615855`
  - `harbor-registryctl`: `223d5cb49d5dbf0ba4ae022e7dd9775fa459904112dc12084d3aaa36c1408362`
  - `trivy-adapter-photon`: `215c07b71c37fc7fc16e02d9185d936dcb8884a80e810817c2cd058bbd7c4e98`
  - `harbor-db`: `5ebfb345b33d673fa8483ffc997013113b7d14ea56abfa65cfcb621b4acf3039`
  - `valkey-photon`: `7337783e8148f01a25b113f5c7b1d640f78b1860fcaf80a33d0dbb294f7b99d0`
- Official Chart values state `trivy.skipUpdate`, `skipJavaDBUpdate` and `offlineScan` are separate flags. With update disabled, real `db/trivy.db`, `db/metadata.json` and `java-db/trivy-java.db` must be present on Trivy's persistent cache volume. The first two flags and `offlineScan` do not supply database bytes.
- At investigation time, `ghcr.io/aquasecurity/trivy-db:2` was OCI manifest SHA256 `4567b9f40c2dc13ead24d5e314563033a890a06b21fd6cb526e7236bfd0f0a72`, created `2026-09-27T13:14:15Z`, DB layer 123356153 bytes SHA256 `81e96e2fa71a2e6a6520f02ab2b371bbf59fa06bdd64cf5ea07d3a4e653a9bf6`. `ghcr.io/aquasecurity/trivy-java-db:1` was manifest SHA256 `e5922bfd4ec2aafa93226148b28f8cce4df87557544ed1bbc80a5b24d218d123`, created `2026-09-27T01:10:04Z`, layer 973426852 bytes SHA256 `75d3ac93f17d38f845b20fdb181bed7e2645f6a67adddb3189ddca4f38920989`. These moving tags are research snapshots, not yet locked distributable DB files; final inputs need extracted-file digests and metadata dates.
- No registry, credential, trust or scan claim is made by this investigation. B07 requires a real offline scan terminal state and node runtime pull before PASS.

## Landed OCI database bytes (Fedora, 2026-09-28)

The two `skopeo copy` operations completed with rc=0 in task-owned directories. Re-reading every downloaded blob and OCI manifest produced exactly the pinned OCI digests above. The Java layer had one logged `unexpected EOF` at byte 367212570; `skopeo` resumed and its final file rehashed to the expected SHA256 `75d3ac93f17d38f845b20fdb181bed7e2645f6a67adddb3189ddca4f38920989`.

Extracted payloads (not yet in a formal artifact):

| File | SHA256 | Metadata |
| --- | --- | --- |
| `trivy.db` | `4bf01c98f9af59d4ec9ab6c8f4f22740230830e8be9172970e74dcb8257cd53b` | DB v2, `UpdatedAt=2026-09-27T13:06:25.102527619Z`, `NextUpdate=2026-09-28T13:06:25.102527368Z` |
| `db/metadata.json` | `eec14e933a21b2dba78c45d2c0de61a6408e14cee008a07908079e4cd6c905d7` | From the same v2 OCI layer |
| `trivy-java.db` | `e99e2d1212f4f1ef8281f95ffa72b667ce3fb8d00db0c4b2ddfcc3283b87612c` | Java DB v1, `UpdatedAt=2026-09-27T01:08:08.997214067Z`, `NextUpdate=2026-09-30T01:08:08.997213636Z` |
| `java-db/metadata.json` | `f730c0616742e306095594ef51359f81cd05e48cff224112a59c8fbd68449e31` | From the same v1 OCI layer |

The dates make staleness an actual B07 runtime risk. These are fixed historical databases, not a claim of current coverage. A package must carry them and the scanner must reach a real scan terminal state offline before B07 passes.

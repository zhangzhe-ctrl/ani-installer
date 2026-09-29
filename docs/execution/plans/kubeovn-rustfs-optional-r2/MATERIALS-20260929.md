# RustFS material research — 2026-09-29

Status: **candidate inputs verified on Fedora; packaging and runtime not verified**.
This file is a research checkpoint, not a release lock or deployment acceptance.

Fedora source tree: `8faba788f2e11581dcc60ac69b2f1da807c65cd8` for the
first selection tests; source material was inspected outside the tree under
`/home/chabking/ani-installer-runs/kubeovn-rustfs/material-research/`.

| Input | Verified source and identity |
| --- | --- |
| Server | `docker.io/rustfs/rustfs:1.0.0`; source index `sha256:8cc9801755448b71a786705ce76692c77e14936cccd87cf2fc31842e58f4d1ff`; linux/amd64 manifest `sha256:ba0a1b53e36f321c0d46f3867104abef169f7bc59c467c664ddac87e7ddc9a8b`. Fedora reread the exact tag with `skopeo inspect --raw`; the index bytes' SHA-256 matched the source digest. |
| Chart | Official `https://charts.rustfs.com/rustfs-1.0.0.tgz`, Chart `rustfs` version/appVersion `1.0.0`, archive SHA-256 `f11e9304fd4c3599ac365af9845ab4a2f99fab3edd30d0c8f89eb9428bcfb711`; the official index entry names the same digest. |
| Management client | Official `rustfs/cli` release `v0.1.36`, `rustfs-cli-linux-amd64-v0.1.36.tar.gz`; archive SHA-256 `4a8128911ccad4e7b481f26635a4cfd1ec064412210526e57ad2c748d356f3b7`, matching its release sidecar; extracted `rc` SHA-256 `15490337489f5c1bb2d33f77e308be52ac29adaa27069d7c77f0d808b165f786`; `rc --version` printed `rc 0.1.36`. |

The fixed Chart was rendered with Fedora's packaged Helm and standalone mode,
`ani-block`, a 20Gi data PVC, an existing root Secret, no log PVC, no Ingress,
no Gateway, and explicitly pinned server/init image names. The rendered owners
were `Deployment/ani-rustfs`, `Service/ani-rustfs-svc`, and
`PersistentVolumeClaim/ani-rustfs-data` in `ani-platform`. It also rendered a
busybox init container. A Chart test Pod is present in the rendered template
set; ordinary `helm install` does not run it.

**Open Chart integration defect:** standalone native TLS is selected by
`RUSTFS_TLS_PATH`, but Chart 1.0.0's rendered readiness and liveness probes
still say `scheme: HTTP` on port 9000. The source template hardcodes this when
`mtls.enabled=false`, even though values advertise probe scheme. Runtime
integration must change the exact rendered probes to HTTPS with a bounded,
packaged transformation and verify the resulting manifest. Disabling probes
does not satisfy this task.

The official client help was read from the fixed binary. `rc alias set` supports
`--ca-bundle` and `--bucket-lookup path`; management commands include
`admin user`, `admin policy`, and `admin service-account`. Actual server API,
repeat semantics, limited policy, and Milvus workload remain **not_verified**.

Primary sources: [RustFS Helm guide](https://docs.rustfs.com/en/installation/cloud-native),
[native TLS guide](https://docs.rustfs.com/en/integration/tls-configured),
[RustFS releases](https://github.com/rustfs/rustfs/releases),
[rc releases](https://github.com/rustfs/cli/releases).

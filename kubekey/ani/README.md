# ANI installer releases and use

This iteration builds a **small code release** and a **large fixed artifact release** separately. The artifact contains only third-party dependencies and images; the code release contains `kk` plus the small install/verification scripts.

## Build

All builds and tests must run through `ssh fedora` in `kubekey/`.

Build or rebuild the code release after source changes:

```bash
PATH=/usr/local/go/bin:$PATH scripts/build-code.sh
```

Build a new artifact only when its fixed material changes. `scripts/build-offline.sh` never builds or copies `kk`, scripts, roles, templates, or manifests. To reuse fixed materials without exporting or collecting them again, provide the existing KubeKey artifact and Hauler image archive:

```bash
KK_BIN=_output/bin/kk \
HAULER_BIN=/path/to/hauler \
REPOSITORY_ISO=/path/to/ubuntu-24.04-debs-amd64.iso \
KUBEKEY_ARTIFACT=/path/to/kubekey-artifact.tgz \
HAULER_ARCHIVE=/path/to/images.haul.tar.zst \
scripts/build-offline.sh
```

A code-only change reuses the existing artifact and transfers only the new code release. Do not rebuild or resend the large artifact.

## Install

Transfer the code release and the fixed artifact to distinct directories on the installer node, then run from the installer node:

```bash
sudo /opt/ani-installer/code/<code-id>/kk ani install \
  --config /opt/ani-installer/site/cluster.yaml \
  --package-root /opt/ani-installer/artifacts/<artifact-id>
```

The optional wrapper has the same behavior:

```bash
sudo /opt/ani-installer/code/<code-id>/install.sh \
  /opt/ani-installer/site/cluster.yaml \
  /opt/ani-installer/artifacts/<artifact-id>
```

`--package-root` names the independent artifact root; `kk` is located from the executable that is running and is not expected inside the artifact.

Installation writes all generated configs, Hauler working data, and logs under `/var/lib/ani-installer/<cluster-name>/`. It does not modify the artifact. If that cluster runtime root already exists, installation stops rather than reusing or cleaning a half-installed site. Restore the agreed clean snapshots before a new formal install.

## Verify

Run the formal verification entry from the installer node:

```bash
sudo /opt/ani-installer/code/<code-id>/verify.sh \
  /opt/ani-installer/site/cluster.yaml \
  /opt/ani-installer/artifacts/<artifact-id>
```

Verification checks the artifact checksums, registry manifests, three Ready nodes, controller/backend readiness, and then runs the active probe. Each run creates fresh generated-name clients and sends real requests to the backend Pod IP, Service IP, DNS name, and the selected Envoy Service. It accepts only the exact expected responses and a zero client exit code. Logs are written under `/var/lib/ani-installer/<cluster-name>/logs/verify-*/`.

## Current validation status

The code/artifact split, runtime path separation, fixed-version checks, and active probe changes in this iteration are implemented in source, but **not yet validated by a clean-snapshot offline installation**. Do not cite historical package tests, earlier installs, manual fixes, or cached logs as validation for this release.
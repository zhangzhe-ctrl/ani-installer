# ani-installer workspace

This repository is the outer product workspace. The complete KubeKey fork lives in [`kubekey/`](./kubekey) as a nested Go module; do not split it into partial directories.

## Layout

- `docs/`: product, deployment, operations, and design documents.
- `config/examples/`: sanitized site configuration examples and customer profiles. Do not put real passwords, private keys, tokens, or generated kubeconfigs here.
- `kubekey/`: complete KubeKey fork, including Go module, built-in assets, scripts, source tests, and `kubekey/docs/progress.md`.
- `kubekey/docs/`: fork/upstream documentation and implementation progress.
- Local build artifacts, transfer archives, real site secrets, and generated offline packages are not source and should not be committed.

## Building

Build the Linux offline package from `kubekey/` on Ubuntu 24.04 amd64. The scripts resolve their paths from their own locations, so the nested layout remains relocatable.

## Execution plan entry (added by R00)

`docs/execution/README.md` is the single entry point for the current remediation and rollout plan; `docs/execution/progress.yaml` is the live status table, and `docs/execution/status-decision.md` records conflict rulings and open items (first written by R00 on 2026-09-24).

Standing decisions that tasks must not silently override: keep the KubeKey fork and Go entry with fixed roles; the kcn-dedicated Envoy stays dedicated and is never reused by business gateway flows (business Envoy is suspended); LWS, vCluster and GPU stay suspended; Istio and Knative are not installed by default; a missing dependency must raise an explicit error rather than auto-resolving a suspended item.

Status addendum (2026-09-25, R16 closeout): `docs/execution/README.md` is part of the frozen planning kit and still carries its 2026-09-24 wording ("remediation not yet executed in this round"); the kit is not rewritten, and `docs/execution/progress.yaml` is the authoritative live status. As of this push, R01–R16 have executed and R16 signs off exactly one combination (kcn + Ubuntu 24.04 amd64 + Ceph + six components, run `ani-ani-lab-20260925-120811`), with its published evidence under `docs/execution/evidence/R16-20260925/`; every other combination and the remaining live_pending items stay not_verified as recorded there.

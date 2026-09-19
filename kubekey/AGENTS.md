# ani-installer execution rules

- Current extension scope (2026-09-19): follow `../docs/observability-components-batch-execution-plan-20260919.md` for the metrics stack and optional Loki/OpenSearch logging with Fluent Bit, one card at a time. The first-round plan remains the contract for cert-manager, PostgreSQL, Valkey and NATS, not authority to add unrelated components.
- Keep all runtime work on `ssh fedora`. User-authorized snapshot restores apply only to the three dedicated test VMs and belong in the lab workflow, never in installer code or artifacts. The old kcn delayed-DEL defect is documented in `../docs/foundation-b5-verification-20260919.md`; a newer fix is user-reported but its material is not supplied. Until the user supplies the fixed material and schedules live validation, proceed only with Fedora code/material/render checks, never retry old kcn or change its source. Mark live checks not_verified.

- Work from KubeKey v4.0.7 and make only evidence-based changes needed by the offline first-install task.
- Preserve KubeKey roles/connectors and use its artifact/create-cluster flow; do not restore the old Kubespray/Ansible installer.
- Keep the base fixed to Ubuntu 24.04 amd64, three dedicated VMs, containerd, and the supplied Envoy Gateway material. kcn v0.6.2 is the historical failing reference; use only a later fixed material explicitly supplied by the user for the next live run, without guessing a tag or changing component code.
- Prefer running the actual command and reading the real log over adding speculative recovery logic.
- Update docs/progress.md after every completed stage; distinguish code/build completion, first real install, and clean-snapshot reinstall.
- Never push, publish, reset, wipe, or mutate non-task clusters or hosts.

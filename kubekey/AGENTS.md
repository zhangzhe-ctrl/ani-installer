# ani-installer execution rules

- Work from KubeKey v4.0.7 and make only evidence-based changes needed by the offline first-install task.
- Preserve KubeKey roles/connectors and use its artifact/create-cluster flow; do not restore the old Kubespray/Ansible installer.
- Keep the scope fixed to Ubuntu 24.04 amd64, three dedicated VMs, containerd, kcn v0.6.2, and the supplied Envoy Gateway material.
- Prefer running the actual command and reading the real log over adding speculative recovery logic.
- Update docs/progress.md after every completed stage; distinguish code/build completion, first real install, and clean-snapshot reinstall.
- Never push, publish, reset, wipe, or mutate non-task clusters or hosts.

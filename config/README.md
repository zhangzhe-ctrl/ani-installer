# Site configuration examples

Put reusable, sanitized ANI site configuration examples and customer profiles here.

Recommended structure:

- `config/examples/cluster.example.yaml`: a complete example without credentials.
- `config/profiles/<profile-name>/`: profile-specific overrides and notes.
- `config/README.md`: conventions and allowed fields.

Keep real `cluster.yaml`, passwords, private keys, generated Inventory, kubeconfigs, certificates, and package archives outside Git. The real installer should continue to use `/home/ubuntu/ani-installer/cluster.yaml` on the installer VM.

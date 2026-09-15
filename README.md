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

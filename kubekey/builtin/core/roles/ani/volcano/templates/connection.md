# ANI Volcano B06 CPU scheduling

- Namespace / Helm release: `volcano-system/ani-volcano` (Chart 1.15.2 CPU slice).
- Explicit scheduler: `volcano`; Kubernetes `default-scheduler` remains in service.
- Test Queue: run-scoped `ani-b06-cpu-*`, capability 200m CPU and 128Mi memory. It is deleted only after owned jobs finish and allocation returns to zero.
- Gang check evidence: `{{ .ani.run.logs_dir }}`. A single Pod must wait under `minMember: 2`; both own Pods must complete after the second arrives. Cancellation deletes only run-owned Pods and PodGroup.
- No GPU, vGPU, LWS, Trainer or JobFlow is installed by this role.

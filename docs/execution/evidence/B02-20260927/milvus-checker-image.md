# B02 Milvus checker image material

Status: Fedora material built and inspected; artifact packaging and live B02 checks not run.

Source in this branch: `kubekey/ani/checkers/milvus/Containerfile`, `verify.py`, `requirements.txt`, `wheelhouse.sha256`, and `build-checker.sh`. The build script checks every locked wheel, requires the pinned Python base, builds with `podman build --network=none --pull=never`, runs `pip check` inside the image, and saves a docker archive. Its source directory is not a target-host dependency.

- Base: `docker.io/library/python:3.13.11-slim-bookworm`, measured amd64 manifest `sha256:ac76900038d8606cc99b413d4ede77bc7152f1e42b94cf5d50d4b80a999652fe`.
- Offline wheelhouse: 20 wheel SHA256 rows in `wheelhouse.sha256`; source and build context under `/home/chabking/ani-installer-runs/b00-b07/materials/milvus/` on Fedora.
- Docker archive: `/home/chabking/ani-installer-runs/b00-b07/materials/milvus/ani-milvus-checker-v1.docker.tar`, SHA256 `618448ca600dba64ae424e50aa5f1533ae3df731de824c9e5c41a312aac2643f`.
- Conversion command on Fedora: `skopeo copy --format oci docker-archive:$archive oci:$layout:ani/milvus-checker:v1`, with `archive` and `layout` set to the paths above and `/home/chabking/ani-installer-runs/b00-b07/materials/milvus/checker-oci-v1` respectively.
- OCI layout: one `ani/milvus-checker:v1` manifest, `sha256:0f197f3d0e30215238dd52bee6b8838b118a2c87fdf8173b8905929fb4de84d5`; eight layers; config declares linux/amd64. Each config and layer digest and size was checked against its actual blob.

The OCI manifest, rather than the docker archive's different schema-2 manifest, is the pin in `images.tsv`. The final cumulative package must carry this exact OCI content through the local registry and pass the normal content gate; this report alone is not package or installation evidence.

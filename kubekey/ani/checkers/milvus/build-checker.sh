#!/usr/bin/env bash
# Build the B02 API checker from the reviewed source and a fixed offline wheel set.
set -euo pipefail
wheelhouse="${1:?usage: build-checker.sh WHEELHOUSE OUT_DOCKER_ARCHIVE WORK_DIR}"
archive="${2:?usage: build-checker.sh WHEELHOUSE OUT_DOCKER_ARCHIVE WORK_DIR}"
work="${3:?usage: build-checker.sh WHEELHOUSE OUT_DOCKER_ARCHIVE WORK_DIR}"
source_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
case "$wheelhouse:$archive:$work" in
  /*:/*:/*) ;;
  *) echo 'all paths must be absolute' >&2; exit 1 ;;
esac
[ -d "$wheelhouse" ]
[ ! -e "$archive" ] || { echo "archive already exists: $archive" >&2; exit 1; }
[ ! -e "$work" ] || { echo "work directory already exists: $work" >&2; exit 1; }
mkdir -p "$work/wheelhouse"
(cd "$wheelhouse" && sha256sum -c "$source_dir/wheelhouse.sha256")
expected="$(wc -l < "$source_dir/wheelhouse.sha256")"
actual="$(find "$wheelhouse" -maxdepth 1 -type f -name '*.whl' | wc -l)"
[ "$expected" -eq "$actual" ] || { echo "wheel count mismatch: expected=$expected actual=$actual" >&2; exit 1; }
cp "$wheelhouse"/*.whl "$work/wheelhouse/"
cp "$source_dir/Containerfile" "$source_dir/requirements.txt" "$source_dir/verify.py" "$work/"
base='docker.io/library/python:3.13.11-slim-bookworm'
podman image exists "$base" || { echo "approved base image $base is absent; fetch it on Fedora before this offline build" >&2; exit 1; }
expected_base='ac76900038d8606cc99b413d4ede77bc7152f1e42b94cf5d50d4b80a999652fe'
actual_base="$(skopeo inspect --raw "containers-storage:$base" | sha256sum | cut -d' ' -f1)"
[ "$actual_base" = "$expected_base" ] || { echo "Python base image manifest mismatch: $actual_base" >&2; exit 1; }
podman build --network=none --pull=never --file "$work/Containerfile" --tag 127.0.0.1:5000/ani/milvus-checker:v1 "$work"
podman save --format docker-archive --output "$archive" 127.0.0.1:5000/ani/milvus-checker:v1
sha256sum "$archive"

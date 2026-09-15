#!/usr/bin/env bash

# Build the image this repository makes, then bring the demo up.
#
# Two commands rather than one because the image is built from ./dist, which
# the `build` target fills in -- see docker-bake.hcl. `docker compose up` on its
# own works once this has been run at least once.

set -o errexit
set -o pipefail

__dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
__root="$(cd "$__dir/../.." && pwd)"

cd "$__root"
echo "==> building ghcr.io/heojeongbo/wick:local"
docker buildx bake build
docker buildx bake app --load

cd "$__dir"
echo "==> up"
exec docker compose up "$@"

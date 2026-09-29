#!/usr/bin/env bash
set -euo pipefail

tag=${1:?release tag required}
sha=${2:?release commit required}
if [[ ! "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(alpha|beta|rc)\.(0|[1-9][0-9]*))?$ ]]; then
  echo "unsupported release tag: $tag" >&2
  exit 1
fi
[[ "$sha" =~ ^[a-f0-9]{40}$ ]]
test "$(git rev-parse HEAD)" = "$sha"
test "$(git rev-parse "refs/tags/$tag^{commit}")" = "$sha"

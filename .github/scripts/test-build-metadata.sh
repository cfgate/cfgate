#!/usr/bin/env bash
set -euo pipefail

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
git init -q "$work"
cd "$work"
git config user.name 'cfgate metadata test'
git config user.email 'metadata@example.invalid'
git -c commit.gpgsign=false commit -q --allow-empty -m initial
unset VERSION_SUFFIX

check() {
  local expected_version=$1 expected_release=$2 expected_commit=$3
  # shellcheck source=hack/build-metadata.sh
  . "$repo/hack/build-metadata.sh"
  [[ "$version" == "$expected_version" && "$is_release" == "$expected_release" && "$commit" == "$expected_commit" ]]
  [[ "$build_date" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]]
}

sha=$(git rev-parse --short HEAD)
check "0.0.0-dev+$sha" false "$sha"
git -c tag.gpgsign=false tag v1.2.3
check 1.2.3 true "$sha"
VERSION_SUFFIX=-candidate check 1.2.3-candidate true "$sha"
git -c commit.gpgsign=false commit -q --allow-empty -m next
sha=$(git rev-parse --short HEAD)
check "1.2.3-dev+$sha" false "$sha"
VERSION_SUFFIX=-candidate check "1.2.3-dev+$sha-candidate" false "$sha"
printf 'local build metadata contracts pass\n'

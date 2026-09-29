#!/usr/bin/env bash
set -euo pipefail

validator="$(cd "$(dirname "$0")" && pwd)/validate-release-ref.sh"
repo=$(mktemp -d)
trap 'rm -rf "$repo"' EXIT
export GIT_AUTHOR_NAME=release-test GIT_AUTHOR_EMAIL=release-test@example.invalid
export GIT_COMMITTER_NAME=$GIT_AUTHOR_NAME GIT_COMMITTER_EMAIL=$GIT_AUTHOR_EMAIL
git init --quiet "$repo"
cd "$repo"
git commit --quiet --allow-empty -m initial
sha=$(git rev-parse HEAD)
for tag in v0.2.0-alpha.6 v1.0.0-beta.0 v1.0.0-rc.1 v1.0.0; do
  git tag "$tag"
  "$validator" "$tag" "$sha"
done
# Deliberately literal command-substitution payload exercises input rejection.
# shellcheck disable=SC2016
for tag in 'v01.2.0' 'v1.02.0' 'v1.0.0-alpha.01' 'v1.0.0-alpha' '--help' 'v1.0.0;touch injected' 'v1.0.0$(touch injected)' $'v1.0.0\ninjected'; do
  if "$validator" "$tag" "$sha" >/dev/null 2>&1; then
    echo "accepted invalid tag: $tag" >&2
    exit 1
  fi
done
test ! -e injected
if "$validator" v1.0.0 invalid-sha >/dev/null 2>&1; then exit 1; fi
git commit --quiet --allow-empty -m changed
new_sha=$(git rev-parse HEAD)
# A moved checkout must fail; a tag still pointing to old source must also fail.
if "$validator" v1.0.0 "$sha" >/dev/null 2>&1; then exit 1; fi
if "$validator" v1.0.0 "$new_sha" >/dev/null 2>&1; then exit 1; fi
printf 'release ref contracts passed\n'

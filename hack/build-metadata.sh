#!/bin/sh
# shellcheck disable=SC2034
# Source from the repository root. Shared by local binary and Docker tasks;
# release workflows supply their verified tag, full SHA and build date directly.
tag="$(git describe --tags --abbrev=0 2>/dev/null || true)"
commit="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
is_release=false
if [ -n "$tag" ] && [ "$(git describe --tags --exact-match 2>/dev/null || true)" = "$tag" ]; then
  version="${tag#v}"
  is_release=true
else
  base="${tag#v}"
  version="${base:-0.0.0}-dev+${commit}"
fi
if [ -n "${VERSION_SUFFIX:-}" ]; then
  version="${version}${VERSION_SUFFIX}"
fi

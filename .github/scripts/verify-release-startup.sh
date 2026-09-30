#!/usr/bin/env bash
set -euo pipefail

: "${RELEASE_VERSION:?release version required}"
: "${RELEASE_SHA:?release commit required}"
: "${RELEASE_BUILD_DATE:?release build date required}"
test "$2" = 1
jq -e --slurp --arg version "$RELEASE_VERSION" --arg commit "$RELEASE_SHA" --arg date "$RELEASE_BUILD_DATE" '
  [.[] | select(.msg == "starting cfgate controller manager")] as $startup |
  ($startup | length) == 1 and
  $startup[0].version == $version and $startup[0].commit == $commit and
  $startup[0].buildDate == $date and
  any(.[]; .msg == "unable to get kubeconfig" and .level == "error")
' "$1"

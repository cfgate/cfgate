#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
export RELEASE_VERSION=0.2.0-alpha.6
export RELEASE_SHA=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
export RELEASE_BUILD_DATE=2026-09-29T12:00:00Z
jq -nc --arg version "$RELEASE_VERSION" --arg commit "$RELEASE_SHA" --arg date "$RELEASE_BUILD_DATE" \
  '{msg:"starting cfgate controller manager",version:$version,commit:$commit,buildDate:$date},
   {msg:"unable to get kubeconfig",level:"error"}' >"$work/valid.jsonl"
"$script_dir/verify-release-startup.sh" "$work/valid.jsonl" 1 >/dev/null

for field in version commit buildDate; do
  jq --arg field "$field" 'if .version then .[$field] = "wrong" else . end' \
    "$work/valid.jsonl" >"$work/wrong.jsonl"
  if "$script_dir/verify-release-startup.sh" "$work/wrong.jsonl" 1 >/dev/null 2>&1; then
    printf 'incorrect %s was accepted\n' "$field" >&2
    exit 1
  fi
done
for status in 0 2 125 137; do
  if "$script_dir/verify-release-startup.sh" "$work/valid.jsonl" "$status" >/dev/null 2>&1; then
    printf 'unexpected exit %s was accepted\n' "$status" >&2
    exit 1
  fi
done
for filter in 'select(.version == null)' 'select(.version != null)' '., select(.version != null)' 'del(.commit)'; do
  jq "$filter" "$work/valid.jsonl" >"$work/missing-or-duplicate.jsonl"
  if "$script_dir/verify-release-startup.sh" "$work/missing-or-duplicate.jsonl" 1 >/dev/null 2>&1; then
    printf 'missing or duplicate startup evidence was accepted\n' >&2
    exit 1
  fi
done
printf 'not JSON\n' >"$work/invalid.jsonl"
if "$script_dir/verify-release-startup.sh" "$work/invalid.jsonl" 1 >/dev/null 2>&1; then
  printf 'invalid startup evidence was accepted\n' >&2
  exit 1
fi
printf 'release startup metadata contracts pass\n'

#!/usr/bin/env bash
set -eu

if [ "$#" -lt 3 ]; then
  echo "usage: $0 <output> <profile1> <profile2> [profile...]" >&2
  exit 1
fi

output="$1"
shift

mode=""
tmp_data="$(mktemp)"
cleanup() {
  rm -f "${tmp_data}"
}
trap cleanup EXIT

for profile in "$@"; do
  if [ ! -f "${profile}" ]; then
    echo "coverage profile not found: ${profile}" >&2
    exit 1
  fi

  profile_mode="$(sed -n '1p' "${profile}")"
  case "${profile_mode}" in
    mode:*)
      ;;
    *)
      echo "invalid coverage profile header in ${profile}: ${profile_mode}" >&2
      exit 1
      ;;
  esac

  if [ -z "${mode}" ]; then
    mode="${profile_mode}"
  elif [ "${profile_mode}" != "${mode}" ]; then
    echo "coverage mode mismatch: ${profile} uses ${profile_mode}, expected ${mode}" >&2
    exit 1
  fi
done

awk '
FNR == 1 { next }
NF == 3 {
  key = $1 FS $2
  count[key] += $3
}
END {
  for (key in count) {
    printf "%s %s\n", key, count[key]
  }
}
' "$@" | LC_ALL=C sort > "${tmp_data}"

mkdir -p "$(dirname "${output}")"
{
  printf "%s\n" "${mode}"
  cat "${tmp_data}"
} > "${output}"

printf "Wrote %s\n" "${output}"

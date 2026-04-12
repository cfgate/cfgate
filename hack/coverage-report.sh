#!/usr/bin/env bash
set -eu

if [ "$#" -ne 4 ]; then
  echo "usage: $0 <unit-profile> <e2e-profile> <merged-profile> <summary-output>" >&2
  exit 1
fi

unit_profile="$1"
e2e_profile="$2"
merged_profile="$3"
summary_output="$4"

for profile in "${unit_profile}" "${e2e_profile}" "${merged_profile}"; do
  if [ ! -f "${profile}" ]; then
    echo "coverage profile not found: ${profile}" >&2
    exit 1
  fi
done

unit_total="$(go tool cover -func="${unit_profile}" | awk '$1 == "total:" { print $NF }')"
e2e_total="$(go tool cover -func="${e2e_profile}" | awk '$1 == "total:" { print $NF }')"
merged_total="$(go tool cover -func="${merged_profile}" | awk '$1 == "total:" { print $NF }')"

mkdir -p "$(dirname "${summary_output}")"
{
  printf "Coverage Totals\n"
  printf "unit   %s\n" "${unit_total}"
  printf "e2e    %s\n" "${e2e_total}"
  printf "merged %s\n" "${merged_total}"
  printf "\nPer-file merged coverage (ascending)\n"
  awk '
    NR == 1 { next }
    {
      split($1, a, ":")
      file = a[1]
      gsub(/^cfgate.io\/cfgate\//, "", file)
      statements = $2 + 0
      hits = $3 + 0
      total[file] += statements
      if (hits > 0) {
        covered[file] += statements
      }
    }
    END {
      for (file in total) {
        pct = 0
        if (total[file] > 0) {
          pct = (covered[file] / total[file]) * 100
        }
        printf "%6.1f%% %4d/%-4d %s\n", pct, covered[file], total[file], file
      }
    }
  ' "${merged_profile}" \
    | LC_ALL=C sort -n
} > "${summary_output}"

cat "${summary_output}"

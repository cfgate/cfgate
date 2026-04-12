#!/usr/bin/env bash
set -eu

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <merged-profile> <e2e-report-json> <output-json>" >&2
  exit 1
fi

merged_profile="$1"
e2e_report="$2"
output_json="$3"

if [ ! -f "${merged_profile}" ]; then
  echo "merged coverage profile not found: ${merged_profile}" >&2
  exit 1
fi

coverage_pct="$(go tool cover -func="${merged_profile}" | awk '$1 == "total:" { gsub(/%/, "", $NF); print $NF }')"

assurance_automated_possible=70
total_automated_possible=170

invariants_points=0
if [ -f test/e2e/invariants_test.go ] && [ -f "${e2e_report}" ]; then
  invariants_points=20
fi
invariants_automated_possible=20

lifecycle_points=0
if [ -f test/e2e/combined_test.go ] && [ -f internal/controller/cloudflaredns_cleanup_test.go ]; then
  lifecycle_points=15
fi
lifecycle_automated_possible=15

concurrency_points=0
if rg -q "concurrent|contention|rapid create|rapid delete|churn" test/e2e internal 2>/dev/null; then
  concurrency_points=5
fi
concurrency_automated_possible=5

cf_failure_points=0
if rg -q "429|rate limit|timeout|temporary|transient 5xx|malformed" internal/cloudflare/*_test.go test/e2e 2>/dev/null; then
  cf_failure_points=5
fi
cf_failure_automated_possible=5

k8s_failure_points=0
if rg -q "conflict|resourceVersion|Eventually\\(func\\(\\) error" test/e2e cmd internal/controller 2>/dev/null; then
  k8s_failure_points=5
fi
k8s_failure_automated_possible=5

fuzz_points=0
if rg -q '^func Fuzz' . 2>/dev/null; then
  fuzz_points=10
fi
fuzz_automated_possible=10

stress_points=0
if rg -q --glob '*_test.go' '\bstress\b|\bsoak\b|rate[- ]limit' test/e2e internal cmd 2>/dev/null; then
  stress_points=5
fi
stress_automated_possible=5

replay_points=0
if rg -Fq 'e2e-{type}-{node}-{line}' docs/TESTING.md test/e2e 2>/dev/null \
  || rg -q 'deterministic.*reproducible|reproducible.*deterministic' docs/TESTING.md test/e2e 2>/dev/null; then
  replay_points=5
fi
replay_automated_possible=5

behavioral_points=$((invariants_points + lifecycle_points + concurrency_points + cf_failure_points + k8s_failure_points + fuzz_points + stress_points + replay_points))
total_points="$(awk -v cov="${coverage_pct}" -v beh="${behavioral_points}" 'BEGIN { printf "%.1f", cov + beh }')"
generated_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

mkdir -p "$(dirname "${output_json}")"
cat > "${output_json}" <<EOF
{
  "generated_at": "${generated_at}",
  "artifacts": {
    "merged_coverprofile": "${merged_profile}",
    "e2e_report": "${e2e_report}"
  },
  "ledgers": {
    "coverage": {
      "earned": ${coverage_pct},
      "possible": 100,
      "basis": "merged.coverprofile"
    },
    "assurance": {
      "earned": ${behavioral_points},
      "possible": 100,
      "automated_possible": ${assurance_automated_possible},
      "scoring_note": "This script currently automates 70 of 100 behavioral rubric points. Remaining rubric points are manual or aspirational until new checks land.",
      "categories": [
        {
          "name": "invariants",
          "earned": ${invariants_points},
          "possible": 20,
          "automated_possible": ${invariants_automated_possible},
          "evidence": ["test/e2e/invariants_test.go", "${e2e_report}"]
        },
        {
          "name": "lifecycle_and_deletion_ordering",
          "earned": ${lifecycle_points},
          "possible": 15,
          "automated_possible": ${lifecycle_automated_possible},
          "evidence": ["test/e2e/combined_test.go", "internal/controller/cloudflaredns_cleanup_test.go"]
        },
        {
          "name": "concurrency_and_churn",
          "earned": ${concurrency_points},
          "possible": 15,
          "automated_possible": ${concurrency_automated_possible}
        },
        {
          "name": "cloudflare_failure_injection",
          "earned": ${cf_failure_points},
          "possible": 15,
          "automated_possible": ${cf_failure_automated_possible}
        },
        {
          "name": "kubernetes_conflict_and_stale_state",
          "earned": ${k8s_failure_points},
          "possible": 10,
          "automated_possible": ${k8s_failure_automated_possible}
        },
        {
          "name": "fuzzing",
          "earned": ${fuzz_points},
          "possible": 10,
          "automated_possible": ${fuzz_automated_possible}
        },
        {
          "name": "scale_and_rate_limit_stress",
          "earned": ${stress_points},
          "possible": 10,
          "automated_possible": ${stress_automated_possible}
        },
        {
          "name": "replay_and_determinism",
          "earned": ${replay_points},
          "possible": 5,
          "automated_possible": ${replay_automated_possible}
        }
      ]
    }
  },
  "total": {
    "earned": ${total_points},
    "possible": 200,
    "automated_possible": ${total_automated_possible}
  }
}
EOF

cat "${output_json}"

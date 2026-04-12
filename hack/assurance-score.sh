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

invariants_points=0
if [ -f test/e2e/invariants_test.go ] && [ -f "${e2e_report}" ]; then
  invariants_points=20
fi

lifecycle_points=0
if [ -f test/e2e/combined_test.go ] && [ -f internal/controller/cloudflaredns_cleanup_test.go ]; then
  lifecycle_points=15
fi

concurrency_points=0
if rg -q "concurrent|contention|rapid create|rapid delete|churn" test/e2e internal 2>/dev/null; then
  concurrency_points=5
fi

cf_failure_points=0
if rg -q "429|rate limit|timeout|temporary|transient 5xx|malformed" internal/cloudflare/*_test.go test/e2e 2>/dev/null; then
  cf_failure_points=5
fi

k8s_failure_points=0
if rg -q "conflict|resourceVersion|Eventually\\(func\\(\\) error" test/e2e cmd internal/controller 2>/dev/null; then
  k8s_failure_points=5
fi

fuzz_points=0
if rg -q '^func Fuzz' . 2>/dev/null; then
  fuzz_points=10
fi

stress_points=0
if rg -q "stress|bench|rate limit|soak" test/e2e internal/*_test.go cmd/*_test.go 2>/dev/null; then
  stress_points=5
fi

replay_points=0
if rg -Fq 'e2e-{type}-{node}-{line}' docs/TESTING.md test/e2e 2>/dev/null \
  || rg -q 'deterministic.*reproducible|reproducible.*deterministic' docs/TESTING.md test/e2e 2>/dev/null; then
  replay_points=5
fi

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
      "categories": [
        {
          "name": "invariants",
          "earned": ${invariants_points},
          "possible": 20,
          "evidence": ["test/e2e/invariants_test.go", "${e2e_report}"]
        },
        {
          "name": "lifecycle_and_deletion_ordering",
          "earned": ${lifecycle_points},
          "possible": 15,
          "evidence": ["test/e2e/combined_test.go", "internal/controller/cloudflaredns_cleanup_test.go"]
        },
        {
          "name": "concurrency_and_churn",
          "earned": ${concurrency_points},
          "possible": 15
        },
        {
          "name": "cloudflare_failure_injection",
          "earned": ${cf_failure_points},
          "possible": 15
        },
        {
          "name": "kubernetes_conflict_and_stale_state",
          "earned": ${k8s_failure_points},
          "possible": 10
        },
        {
          "name": "fuzzing",
          "earned": ${fuzz_points},
          "possible": 10
        },
        {
          "name": "scale_and_rate_limit_stress",
          "earned": ${stress_points},
          "possible": 10
        },
        {
          "name": "replay_and_determinism",
          "earned": ${replay_points},
          "possible": 5
        }
      ]
    }
  },
  "total": {
    "earned": ${total_points},
    "possible": 200
  }
}
EOF

cat "${output_json}"

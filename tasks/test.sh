#!/bin/sh
# Run unit tests across all internal packages.
# Env: TEST_ARGS - extra args passed to go test (default: none)
# Env: TEST_PKG  - package pattern (default: ./internal/...)
set -eu

pkg="${TEST_PKG:-./internal/...}"

echo "Running unit tests: ${pkg}"
go test -race -count=1 ${TEST_ARGS:-} ${pkg}

#!/bin/sh
# Run unit tests with coverage and print a summary.
# Env: TEST_PKG - package pattern (default: ./internal/...)
# Output: out/unit.coverprofile
set -eu

pkg="${TEST_PKG:-./internal/...}"

mkdir -p out

echo "Running unit tests with coverage: ${pkg}"
go test -race -count=1 -coverprofile=out/unit.coverprofile ${pkg}

echo ""
echo "Coverage summary:"
go tool cover -func=out/unit.coverprofile | tail -1

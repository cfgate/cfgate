// Package main provides a standalone cleanup utility for orphaned E2E test resources.
//
// The cleanup tool scans the Cloudflare account for resources matching local E2E
// test naming patterns and deletes them. It is designed to be run before E2E
// suites to ensure a clean environment, or after failed test runs to remove
// orphaned resources.
//
// # Safety
//
// The tool ONLY deletes resources matching these patterns:
//   - e2e-* prefix: Standard E2E test resources
//   - recovery-* prefix: Test recovery scenarios
//   - _cfgate.e2e-* prefix: DNS ownership TXT records for E2E tests
//
// Production resources are never touched. The tool will not delete resources
// that do not match the defined patterns.
//
// # Resource Types
//
// The following Cloudflare resource types are scanned and cleaned:
//   - Cloudflare Tunnels
//   - DNS Records
//   - Access Applications
//   - Access Service Tokens
//
// # Required Environment Variables
//
//	CLOUDFLARE_API_TOKEN    Cloudflare API token with appropriate permissions
//	CLOUDFLARE_ACCOUNT_ID   Cloudflare account ID
//	CLOUDFLARE_ZONE_NAME    Zone name for DNS record cleanup (optional)
//
// # API Token Permissions
//
// The API token requires the following permissions:
//   - Account > Cloudflare Tunnel > Edit (for tunnel cleanup)
//   - Zone > DNS > Edit (for DNS record cleanup)
//   - Account > Access: Apps and Policies > Edit (for Access cleanup)
//
// # Usage
//
// Run via mise (recommended):
//
//	mise run e2e:cleanup
//
// Or directly:
//
//	mise exec -- go run ./cmd/cleanup
//
// The tool outputs a summary of found and deleted resources. Exit code is 0
// on success, 1 if any deletions fail.
//
// # Example Output
//
//	=== cfgate E2E Resource Cleanup ===
//	Account ID: abc123
//	Zone: example.com
//
//	--- Scanning Tunnels ---
//	  Found: e2e-tunnel-1234 (ID: uuid-1)
//
//	--- Deleting Resources ---
//	  Deleting tunnel: e2e-tunnel-1234 ... OK
//
//	=== Cleanup Summary ===
//	Deleted: 1
//	Failed:  0
package main

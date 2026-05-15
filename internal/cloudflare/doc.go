// Package cloudflare provides a Cloudflare API client wrapper for cfgate.
//
// This package abstracts the cloudflare-go v6 SDK to provide cfgate-specific
// operations for Tunnel, DNS, and Access management. Controllers interact with
// high-level services (TunnelService, DNSService, AccessService) rather than
// the SDK directly.
//
// # Architecture
//
// The package follows a layered architecture:
//
//	Client (interface)     - Low-level API wrapper
//	  |
//	  +-- TunnelService   - Tunnel lifecycle operations
//	  +-- DNSService      - DNS record sync with ownership
//	  +-- AccessService   - Access application/policy management
//	  |
//	CredentialCache       - Thread-safe client caching
//
// # Client Interface
//
// Client defines the low-level Cloudflare API operations. It wraps cloudflare-go v6
// and handles error normalization, 404 patterns, and SDK quirks. Controllers should
// use the high-level services rather than Client directly.
//
//	client, err := cloudflare.NewClient(apiToken)
//	if err != nil {
//	    return err
//	}
//
// # TunnelService
//
// TunnelService provides idempotent tunnel lifecycle management:
//
//	svc := cloudflare.NewTunnelService(client, log)
//	tunnel, created, err := svc.EnsureTunnel(ctx, accountID, "my-tunnel")
//	token, err := svc.GetToken(ctx, accountID, tunnel.ID)
//	err = svc.UpdateConfiguration(ctx, accountID, tunnel.ID, config)
//	err = svc.Delete(ctx, accountID, tunnel.ID)
//
// # DNSService
//
// DNSService handles DNS record sync with external-dns compatible ownership tracking:
//
//	svc := cloudflare.NewDNSService(client, log)
//	record, modified, err := svc.SyncRecord(ctx, zoneID, desired, ownerID)
//	err = svc.CreateOwnershipRecord(ctx, zoneID, params)
//	err = svc.DeleteRecordWithPolicy(ctx, zoneID, recordID, policy)
//
// Ownership records use the format:
//
//	heritage=cfgate,cfgate/owner=<id>,cfgate/resource=<kind>/<ns>/<name>
//
// # AccessService
//
// AccessService manages Access applications, policies, and service tokens:
//
//	svc := cloudflare.NewAccessService(client, log)
//	app, err := svc.EnsureApplicationByIDOrTags(ctx, accountID, statusID, params)
//	policy, err := svc.EnsureReusablePolicy(ctx, accountID, statusID, desired)
//	token, err := svc.EnsureServiceToken(ctx, accountID, params, secretWriter)
//
// Service token secret writers may implement ServiceTokenSecretRefreshChecker to
// rotate existing unexpired tokens when stored credentials are missing or stale.
//
// # CredentialCache
//
// CredentialCache provides thread-safe caching of validated clients:
//
//	cache := cloudflare.NewCredentialCache(30 * time.Second)
//	client, err := cache.GetOrCreate(ctx, secret, func() (cloudflare.Client, error) {
//	    return cloudflare.NewClient(string(secret.Data["apiToken"]))
//	})
//
// Cache keys are based on Secret UID and ResourceVersion for automatic invalidation.
//
// # Error Handling
//
// The package normalizes Cloudflare API errors:
//   - 404/not found: Returns nil, nil (resource doesn't exist)
//   - Duplicate record (81053, 81058): Handled in SyncRecord with race condition recovery
//   - Rate limiting: Handled by cloudflare-go SDK with automatic backoff
//
// # SDK Workarounds
//
// The package implements workarounds for known SDK issues:
//   - ListAutoPaging pagination bug (cloudflare-python#2584): Uses direct List() for accounts
//   - Missing policy fields: Uses option.WithJSONSet() for name, decision, rules
//
// # Logging
//
// All services use logr for structured logging:
//   - Info level: User-visible operations (create, update, delete)
//   - V(1) level: Debug details (adoption, skipping updates)
//   - Error level: API failures with context
//
// Sensitive data (secrets, tokens) is never logged.
package cloudflare

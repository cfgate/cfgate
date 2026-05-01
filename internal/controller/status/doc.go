// Package status provides condition management utilities for cfgate controllers.
//
// It centralizes condition management logic to ensure consistent status handling
// across all cfgate CRDs:
//   - CloudflareTunnel: Tunnel lifecycle, credentials, cloudflared deployment
//   - CloudflareDNS: DNS sync, zone resolution, ownership verification
//   - CloudflareAccessPolicy: reusable Access policy and service tokens
//   - CloudflareAccessApplication: Gateway API target binding and policy links
//
// The package adapts patterns from Envoy Gateway for condition merging,
// message formatting, and Gateway API PolicyStatus handling.
//
// # Core Functions
//
// MergeConditions merges condition updates into an existing slice:
//   - Preserves LastTransitionTime when status unchanged
//   - Truncates messages to MaxConditionMessageLength (32768)
//   - Returns new slice (does not modify input)
//
// Error2ConditionMsg formats errors for human-readable condition messages:
//   - Capitalizes first letter
//   - Ensures trailing period
//   - Handles nil errors gracefully
//
// Utility functions for condition slice manipulation:
//   - NewCondition: Generic condition constructor with timestamps
//   - FindCondition: Lookup condition by type
//   - SetCondition: Add or update a condition
//   - RemoveCondition: Remove a condition by type
//   - ConditionTrue/ConditionFalse/ConditionUnknown: Status checks
//
// # Condition Types
//
// CloudflareTunnel conditions:
//   - Ready: Overall tunnel ready (all sub-conditions true)
//   - CredentialsValid: Cloudflare API credentials validated
//   - TunnelCreated: Tunnel exists in Cloudflare
//   - TunnelConfigured: Tunnel configuration synced
//   - DeploymentReady: cloudflared Deployment pods ready
//
// CloudflareDNS conditions:
//   - Ready: Overall DNS sync ready
//   - CredentialsValid: Cloudflare API credentials validated
//   - ZonesResolved: All configured zones resolved via API
//   - RecordsSynced: DNS records synced to Cloudflare
//   - OwnershipVerified: TXT ownership records verified
//
// CloudflareAccessPolicy conditions:
//   - Ready: Reusable policy fully synced
//   - CredentialsValid: Cloudflare API credentials validated
//   - ServiceTokensReady: All service tokens created and stored
//   - PolicySynced: Reusable Access policy exists in Cloudflare
//
// CloudflareAccessApplication conditions:
//   - Ready: Applications fully synced and policies linked
//   - CredentialsValid: Cloudflare API credentials validated
//   - TargetsResolved: All targetRefs found and valid
//   - ReferenceGrantValid: Cross-namespace references are permitted
//   - PoliciesResolved: Referenced reusable policies are ready
//   - ApplicationSynced: Access Applications exist in Cloudflare
//   - PoliciesLinked: Reusable policies attached to applications
//
// # CRD-Specific Constructors
//
// Each CRD has typed condition constructors:
//
//	// CloudflareTunnel
//	NewCredentialsValidCondition(valid bool, reason, message string, generation int64)
//	NewTunnelCreatedCondition(created bool, reason, message string, generation int64)
//	NewTunnelConfiguredCondition(configured bool, reason, message string, generation int64)
//	NewDeploymentReadyCondition(ready bool, reason, message string, generation int64)
//	NewTunnelReadyCondition(conditions []metav1.Condition, generation int64)
//
//	// CloudflareDNS
//	NewZonesResolvedCondition(resolved bool, reason, message string, generation int64)
//	NewRecordsSyncedCondition(synced bool, reason, message string, generation int64)
//	NewOwnershipVerifiedCondition(verified bool, reason, message string, generation int64)
//	NewDNSReadyCondition(conditions []metav1.Condition, generation int64)
//
//	// CloudflareAccessPolicy
//	NewServiceTokensReadyCondition(ready bool, reason, message string, generation int64)
//	NewPolicySyncedCondition(synced bool, reason, message string, generation int64)
//	NewAccessPolicyReadyCondition(conditions []metav1.Condition, hasServiceTokens bool, generation int64)
//
//	// CloudflareAccessApplication
//	NewTargetsResolvedCondition(resolved bool, reason, message string, generation int64)
//	NewReferenceGrantValidCondition(valid bool, reason, message string, generation int64)
//	NewPoliciesResolvedCondition(resolved bool, reason, message string, generation int64)
//	NewApplicationSyncedCondition(synced bool, reason, message string, generation int64)
//	NewPoliciesLinkedCondition(linked bool, reason, message string, generation int64)
//	NewAccessApplicationReadyCondition(conditions []metav1.Condition, generation int64)
//
// # Logging
//
// The package provides logging helpers for condition changes:
//
//	status.LogConditionChange(log, "tunnel", "Ready", oldStatus, newStatus, reason)
//	status.LogStatusUpdate(log, "tunnel", conditions)
//
// LogConditionChange logs at Info level when status changes.
// LogStatusUpdate logs at V(1) debug level for routine updates.
//
// # Example Usage
//
// Typical reconciler pattern:
//
//	conditions := tunnel.Status.Conditions
//	conditions = status.MergeConditions(conditions,
//	    status.NewCredentialsValidCondition(true,
//	        status.ReasonCredentialsValid,
//	        "API token validated successfully.",
//	        tunnel.Generation,
//	    ),
//	)
//	readyCondition := status.NewTunnelReadyCondition(conditions, tunnel.Generation)
//	conditions = status.MergeConditions(conditions, readyCondition)
//	tunnel.Status.Conditions = conditions
package status

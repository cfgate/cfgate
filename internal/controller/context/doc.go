// Package context provides wrapper types for clean separation between raw API
// types and processing logic. Based on Envoy Gateway's GatewayContext and
// ListenerContext patterns, aligned with cfgate's composable CRD architecture.
//
// The package provides three primary context types, each wrapping a cfgate CRD
// or Gateway API resource with computed state and helper methods:
//
//   - TunnelContext: Wraps CloudflareTunnel with resolved account ID and clients
//   - DNSContext: Wraps CloudflareDNS with resolved tunnel reference and zones
//   - AccessPolicyContext: Wraps CloudflareAccessPolicy with resolved targets
//
// Context wrappers serve multiple purposes:
//   - Cache computed values to avoid repeated calculation in reconcile loops
//   - Provide type-safe access to resolved references
//   - Encapsulate validation logic for cross-resource relationships
//   - Capture partial resolution errors without failing the entire operation
//
// # TunnelContext
//
// TunnelContext wraps CloudflareTunnel with resolved Cloudflare credentials:
//
//	tc := context.NewTunnelContext(tunnel, accountID, client)
//	accountID := tc.AccountID()
//	client := tc.TunnelClient()
//
// Note: DNS management is handled separately by CloudflareDNS CRD.
// TunnelContext is tunnel-only and does not manage DNS records.
//
// # DNSContext
//
// DNSContext wraps CloudflareDNS with resolved tunnel (or external target)
// and zone configuration:
//
//	dc, err := context.NewDNSContext(ctx, dns, k8sClient, dnsClient)
//	if err != nil {
//	    // Handle tunnel resolution error
//	}
//	tunnelDomain := dc.TunnelDomain()
//	shouldDelete := dc.ShouldDeleteOnResourceRemoval()
//
// DNSContext supports both tunnelRef and externalTarget modes. When using
// tunnelRef, it validates the tunnel exists and has a domain ready in status.
//
// # AccessPolicyContext
//
// AccessPolicyContext wraps CloudflareAccessPolicy with resolved targetRefs:
//
//	apc := context.NewAccessPolicyContext(ctx, policy, k8sClient)
//	if apc.HasFailedTargets() {
//	    // Handle resolution failures
//	}
//	hostnames, err := apc.ExtractHostnames(ctx, k8sClient)
//
// Target resolution includes ReferenceGrant checking for cross-namespace
// references. Errors are captured per-target in TargetInfo.Error to allow
// partial resolution.
//
// # Builder Functions
//
// Builder functions create fully-initialized contexts from NamespacedName refs:
//
//	tc, err := context.BuildTunnelContext(ctx, k8sClient, cfClient, ref, accountID)
//	dc, err := context.BuildDNSContext(ctx, k8sClient, dnsClient, ref)
//	apc, err := context.BuildAccessPolicyContext(ctx, k8sClient, ref)
//	rc, err := context.BuildRouteContext(ctx, k8sClient, "HTTPRoute", ref)
//
// Builder functions return nil (not error) when the target resource is not
// found, allowing callers to distinguish between "not found" and "error".
//
// # TargetInfo
//
// TargetInfo represents a resolved policy target with resolution status:
//
//	type TargetInfo struct {
//	    Kind        string   // HTTPRoute, Gateway, etc.
//	    Namespace   string
//	    Name        string
//	    Resolved    bool
//	    SectionName *string  // Optional listener/rule selector
//	    Error       error    // Resolution error (nil if successful)
//	}
//
// TargetInfo captures partial resolution results. A target may fail to resolve
// due to not being found or lacking a ReferenceGrant, but other targets in the
// same policy can still succeed.
//
// # Logging
//
// Context constructors use logr with named logger hierarchy:
//
//	ctrl.Log.WithName("context").WithName("tunnel")
//	ctrl.Log.WithName("context").WithName("dns")
//	ctrl.Log.WithName("context").WithName("accesspolicy")
//	ctrl.Log.WithName("context").WithName("route")
//
// Debug-level (V(1)) logs are emitted for resolution results and config parsing.
package context

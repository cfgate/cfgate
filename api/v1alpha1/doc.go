// Package v1alpha1 contains API Schema definitions for the cfgate.io v1alpha1 API group.
//
// cfgate is a Gateway API-native Kubernetes operator for Cloudflare Tunnel, DNS, and Access management.
// This package defines four composable Custom Resource Definitions (CRDs) that enable declarative
// management of Cloudflare resources from Kubernetes.
//
// # Custom Resource Definitions
//
// CloudflareTunnel manages the lifecycle of Cloudflare Tunnels and cloudflared daemon deployments.
// It handles tunnel creation, credential management, and deploys the cloudflared connector to
// establish secure connections between Kubernetes services and Cloudflare's edge network.
//
// CloudflareDNS provides standalone DNS record management with support for tunnel references or
// external targets. It implements ownership tracking via TXT records (aligned with external-dns patterns)
// and supports multiple DNS policies: sync, upsert-only, and create-only.
//
// CloudflareAccessPolicy manages reusable account-level Cloudflare Access policies.
//
// CloudflareAccessApplication binds reusable Access policies to Gateway API host/path targets and
// supports cross-namespace references via ReferenceGrant resources.
//
// # Architecture
//
// The CRDs follow a composable architecture where each resource manages a single concern:
//   - CloudflareTunnel: tunnel lifecycle only (no embedded DNS)
//   - CloudflareDNS: DNS records (references tunnels via tunnelRef)
//   - CloudflareAccessPolicy: reusable Access policies
//   - CloudflareAccessApplication: Access Applications and policy links for Gateway API resources
//
// This separation enables independent lifecycles, cross-namespace references, and flexible composition.
//
// # Version
//
// This is the v1alpha1 version of the API. Alpha versions may introduce breaking changes
// between releases. Migrate to stable versions when available.
//
// # Related Documentation
//
// For usage examples and detailed configuration, see:
//   - CloudflareTunnel: tunnel lifecycle, cloudflared deployment configuration
//   - CloudflareDNS: DNS sync policies, ownership tracking, hostname sources
//   - CloudflareAccessPolicy: reusable Access rules and service tokens
//   - CloudflareAccessApplication: Gateway API target binding and reusable policy links
//
// +kubebuilder:object:generate=true
// +groupName=cfgate.io
package v1alpha1

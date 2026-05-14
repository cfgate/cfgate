// Package controller provides Kubernetes reconciliation controllers for cfgate CRDs.
//
// cfgate is a Gateway API-native Kubernetes operator for Cloudflare Tunnel, DNS,
// and Access management. This package contains the core reconciliation logic that
// synchronizes Kubernetes resources with Cloudflare APIs.
//
// # Controllers
//
// The package provides six reconcilers:
//
//   - [CloudflareTunnelReconciler]: Manages CloudflareTunnel CRD lifecycle including
//     tunnel creation/adoption, cloudflared deployment, and ingress configuration sync.
//
//   - [CloudflareDNSReconciler]: Manages CloudflareDNS CRD lifecycle including
//     hostname collection from Gateway API routes and DNS record synchronization.
//
//   - [CloudflareAccessPolicyReconciler]: Manages CloudflareAccessPolicy CRD lifecycle
//     including reusable Access policies and service token synchronization.
//
//   - [CloudflareAccessApplicationReconciler]: Manages CloudflareAccessApplication CRD
//     lifecycle including Gateway API target binding and reusable policy links.
//
//   - [GatewayReconciler]: Validates Gateway resources that reference CloudflareTunnel
//     via the cfgate.io/tunnel-ref annotation.
//
//   - [HTTPRouteReconciler]: Validates HTTPRoute resources against parent Gateways,
//     resolves backend Services, and validates CloudflareAccessPolicy references.
//
// # Architecture
//
// Controllers follow the controller-runtime reconciliation pattern:
//
//  1. Fetch the primary resource
//  2. Handle deletion via finalizers
//  3. Execute reconciliation phases
//  4. Update status with conditions
//  5. Emit events for significant state changes
//
// Each controller uses [GenerationChangedPredicate] to prevent reconciliation loops
// caused by status-only updates.
//
// # Logging
//
// Controllers use the logr interface from controller-runtime:
//
//	log := log.FromContext(ctx).WithName("controller").WithName("<type>")
//
// Log levels follow Kubernetes conventions:
//   - Info (0): Reconciliation start/end, major state transitions
//   - V(1): Intermediate steps, diagnostic information
//   - Error: Failures with context for debugging
//
// # Events
//
// Controllers emit Kubernetes events via [events.EventRecorder]:
//   - Normal: Successful operations (Created, Reconciled, Synced)
//   - Warning: Recoverable errors, deprecation warnings
//
// # Feature Gates
//
// Optional Gateway API CRD support is controlled by [features.FeatureGates]:
//   - ReferenceGrant: Required for cross-namespace policy attachment
//
// Controllers check feature gates before registering watches to avoid startup
// failures when optional CRDs are not installed.
//
// # Subpackages
//
// The controller package has several subpackages:
//
//   - annotations: Annotation key constants and parsing utilities
//   - context: Context wrappers for passing reconciliation state
//   - features: FeatureGates for optional CRD detection
//   - status: Condition builders and status utilities
package controller

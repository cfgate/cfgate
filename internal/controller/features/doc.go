// Package features provides CRD detection and feature flags for optional
// Gateway API resources, enabling graceful degradation when supporting
// resources are unavailable.
//
// cfgate assumes Gateway and HTTPRoute are part of the required Gateway API
// installation. This package detects optional CRDs that refine behavior.
//
// # Detected CRDs
//
// The package checks for ReferenceGrant (v1beta1/standard), which cfgate uses for
// cross-namespace secret and policy target validation.
//
// # Usage
//
// Detect features at manager startup using the discovery client:
//
//	dc, err := discovery.NewDiscoveryClientForConfig(mgr.GetConfig())
//	if err != nil {
//	    return err
//	}
//	gates, err := features.DetectFeatures(dc)
//	if err != nil {
//	    return err
//	}
//	gates.LogFeatures(setupLog)
//
// Pass FeatureGates to reconcilers that need conditional behavior:
//
//	reconciler := &CloudflareAccessPolicyReconciler{
//	    Client:       mgr.GetClient(),
//	    FeatureGates: gates,
//	}
//
// # Conditional Watches
//
// Use feature gates to conditionally register watches in SetupWithManager:
//
//	if r.FeatureGates != nil && r.FeatureGates.HasReferenceGrantSupport() {
//	    controllerBuilder = controllerBuilder.Watches(
//	        &gatewayv1b1.ReferenceGrant{},
//	        handler.EnqueueRequestsFromMapFunc(r.findPoliciesForReferenceGrant),
//	    )
//	}
//
// # Nil Safety
//
// All FeatureGates checks include nil guards to support testing without
// feature detection:
//
//	if r.FeatureGates != nil && r.FeatureGates.HasReferenceGrantSupport() {
//	    // Feature available
//	}
//
// This allows tests to skip feature gate injection and provides backward
// compatibility during gradual integration.
//
// # Detection Behavior
//
// Detection failures are non-fatal; features default to disabled. This ensures
// the controller can start even if detection fails for some CRDs. If the API
// server is unreachable, DetectFeatures returns an error and the manager should
// fail fast rather than start with incomplete feature state.
//
// CRD availability is detected once at startup and cached for the controller
// lifetime, since CRDs don't typically change at runtime.
//
// # Logging
//
// LogFeatures logs detection results at Info level:
//
//	gates.LogFeatures(setupLog)
//	// Output: "Gateway API feature detection complete" httpRouteAvailable=true ...
//
// Missing optional features are logged at V(1) with install hints:
//
//	// V(1): "ReferenceGrant CRD not found, cross-namespace references disabled"
//	//       requiredVersion=v1beta1 installHint="Install Gateway API standard channel CRDs"
package features

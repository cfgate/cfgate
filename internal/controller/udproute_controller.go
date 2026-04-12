package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"cfgate.io/cfgate/internal/controller/features"
)

// UDPRouteReconciler reconciles UDPRoute resources.
// NOTE: This is a spec-only stub for alpha.3. Full implementation deferred to v0.2.0.
//
// UDPRoute support requires:
// - Gateway API experimental channel CRDs (v1alpha2)
// - Cloudflare Spectrum (enterprise feature for UDP proxy)
//
// Unlike HTTPRoute, UDPRoute has no spec.hostnames field.
// Hostname must be provided via cfgate.io/hostname annotation.
type UDPRouteReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	Recorder     events.EventRecorder
	FeatureGates *features.FeatureGates
}

// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=udproutes,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=udproutes/status,verbs=get;update;patch

// Reconcile handles the reconciliation loop for UDPRoute resources.
// NOTE: This is a stub for alpha.3 - returns early without processing.
// Full implementation deferred to v0.2.0.
//
// When fully implemented, reconciliation will:
//  1. Validate cfgate.io/hostname annotation (required for UDPRoute)
//  2. Validate parent Gateway references
//  3. Trigger TunnelReconciler to build Spectrum rules
//  4. Update route status conditions
func (r *UDPRouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithName("controller").WithName("udproute")

	// alpha.3: Log spec-only status and return early.
	// Full reconciliation logic will be implemented in v0.2.0.
	log.V(1).Info("reconciliation skipped: spec-only in alpha.3",
		"name", req.Name,
		"namespace", req.Namespace,
		"implementationVersion", "v0.2.0",
	)

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
// IMPORTANT: Only call this if FeatureGates.HasUDPRouteSupport() returns true.
// The manager startup in main.go should check feature gates before registration.
//
// NOTE: In alpha.3, this registers a no-op controller for API compatibility.
// Full implementation in v0.2.0 will add proper watches and reconciliation.
func (r *UDPRouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Check feature gate - skip registration if UDPRoute CRD not installed
	if r.FeatureGates != nil && !r.FeatureGates.HasUDPRouteSupport() {
		return nil
	}

	log := mgr.GetLogger().WithName("controller").WithName("udproute")
	// alpha.3: Register minimal controller without For() clause.
	// We cannot use For(&gwapiv1alpha2.UDPRoute{}) without importing
	// the experimental types, which we defer to v0.2.0.
	//
	// This stub exists to:
	// 1. Establish the controller structure
	// 2. Document the FeatureGates integration pattern
	// 3. Enable main.go conditional registration testing

	// Note: Not registering any watches in alpha.3.
	// v0.2.0 will add: For(&gwapiv1alpha2.UDPRoute{})
	log.Info("registering controller with manager (spec-only in alpha.3)")
	return nil
}

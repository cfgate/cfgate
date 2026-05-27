package controller

import (
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/controller/annotations"
)

// predicateLog is the package-level logger for predicate decisions.
// Uses V(1) per @P-LOG-GO-004 for predicate pass/filter decisions.
var predicateLog = ctrl.Log.WithName("predicate")

// CfgateAnnotationOrGenerationPredicate passes events when:
//  1. metadata.generation changed (spec change), OR
//  2. Any cfgate.io/* annotation changed (value added, removed, or modified)
//
// This replaces GenerationChangedPredicate on cross-resource watchers where
// cfgate annotations (cfgate.io/tunnel-ref, cfgate.io/origin-protocol, etc.) are
// meaningful triggers that don't increment generation.
//
// For CRDs with status subresource, annotation-only changes do NOT increment
// metadata.generation. GenerationChangedPredicate alone would filter these events,
// causing the DNS controller to miss annotation additions post-Gateway-creation.
//
// Unlike AnnotationChangedPredicate (which fires on ANY annotation change including
// Helm, Kiali, kubectl metadata), this predicate only checks cfgate.io/* prefixed
// annotations for more precise filtering.
var CfgateAnnotationOrGenerationPredicate = predicate.Or(
	predicate.GenerationChangedPredicate{},
	predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			changed := cfgateAnnotationsChanged(
				e.ObjectOld.GetAnnotations(),
				e.ObjectNew.GetAnnotations(),
			)
			if changed {
				predicateLog.V(1).Info("cfgate annotation change detected",
					"namespace", e.ObjectNew.GetNamespace(),
					"name", e.ObjectNew.GetName(),
					"kind", e.ObjectNew.GetObjectKind().GroupVersionKind().Kind,
				)
			}
			return changed
		},
	},
)

// GenerationOrDeletionPredicate passes events when:
//  1. metadata.generation changed (spec change), OR
//  2. DeletionTimestamp was just set (object marked for deletion)
//
// This replaces GenerationChangedPredicate on For() clauses for CRDs that use
// finalizers. Without this, setting DeletionTimestamp (which does NOT increment
// generation) produces an Update event that GenerationChangedPredicate filters.
// The reconciler never sees the deletion until a stale RequeueAfter timer fires
// potentially minutes later, causing delayed or failed cleanup.
var GenerationOrDeletionPredicate = predicate.Or(
	predicate.GenerationChangedPredicate{},
	predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return false
			}
			// Pass if DeletionTimestamp was just set.
			if e.ObjectOld.GetDeletionTimestamp() == nil && e.ObjectNew.GetDeletionTimestamp() != nil {
				predicateLog.V(1).Info("deletion timestamp detected",
					"namespace", e.ObjectNew.GetNamespace(),
					"name", e.ObjectNew.GetName(),
				)
				return true
			}
			return false
		},
	},
)

// AccessPolicyReferenceChangedPredicate passes policy updates that can affect
// applications referencing the policy, including status-only readiness changes.
var AccessPolicyReferenceChangedPredicate = predicate.ResourceVersionChangedPredicate{}

// DataResourceChangedPredicate passes Secret/ConfigMap updates where data
// changes do not increment metadata.generation.
var DataResourceChangedPredicate = predicate.ResourceVersionChangedPredicate{}

// TunnelIDChangedPredicate filters CloudflareTunnel events to only those where
// Status.TunnelID has changed. This is used by GatewayReconciler to detect when
// a tunnel has been created/adopted and the TunnelID becomes available.
var TunnelIDChangedPredicate = predicate.Funcs{
	CreateFunc: func(e event.CreateEvent) bool {
		// Pass Creates — a new tunnel with TunnelID already set needs processing
		tunnel, ok := e.Object.(*cfgatev1alpha1.CloudflareTunnel)
		if !ok {
			return false
		}
		return tunnel.Status.TunnelID != ""
	},
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldTunnel, ok := e.ObjectOld.(*cfgatev1alpha1.CloudflareTunnel)
		if !ok {
			return false
		}
		newTunnel, ok := e.ObjectNew.(*cfgatev1alpha1.CloudflareTunnel)
		if !ok {
			return false
		}
		return oldTunnel.Status.TunnelID != newTunnel.Status.TunnelID
	},
	DeleteFunc: func(e event.DeleteEvent) bool {
		return false // Gateway doesn't need to react to tunnel deletion
	},
}

// GatewayCreateAnnotationFilter rejects Gateway Create events that lack cfgate
// annotations. Use this on Gateway watches (AND-combined with CfgateAnnotationOrGenerationPredicate)
// to prevent "poisoned first reconcile" where a bare Gateway Create triggers
// unnecessary reconciliation that finds 0 hostnames.
//
// This predicate only filters Create events. Update, Delete, and Generic events
// pass through unconditionally. This ensures annotation-add Updates still trigger
// reconciliation normally.
var GatewayCreateAnnotationFilter = predicate.Funcs{
	CreateFunc: func(e event.CreateEvent) bool {
		for k := range e.Object.GetAnnotations() {
			if strings.HasPrefix(k, annotations.AnnotationPrefix) {
				return true
			}
		}
		return false
	},
	UpdateFunc:  func(e event.UpdateEvent) bool { return true },
	DeleteFunc:  func(e event.DeleteEvent) bool { return true },
	GenericFunc: func(e event.GenericEvent) bool { return true },
}

// cfgateAnnotationsChanged returns true if any cfgate.io/* prefixed annotation
// was added, removed, or had its value changed between old and new.
func cfgateAnnotationsChanged(old, new map[string]string) bool {
	// Check for new or changed cfgate annotations
	for k, v := range new {
		if strings.HasPrefix(k, annotations.AnnotationPrefix) {
			if oldV, ok := old[k]; !ok || oldV != v {
				return true // new annotation or changed value
			}
		}
	}
	// Check for removed cfgate annotations
	for k := range old {
		if strings.HasPrefix(k, annotations.AnnotationPrefix) {
			if _, ok := new[k]; !ok {
				return true // annotation removed
			}
		}
	}
	return false
}

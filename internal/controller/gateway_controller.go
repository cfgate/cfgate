package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/controller/annotations"
	"cfgate.io/cfgate/internal/controller/status"
)

const (
	// GatewayControllerName is the controller name for GatewayClass.
	GatewayControllerName = "cfgate.io/cloudflare-tunnel-controller"
)

// GatewayReconciler reconciles Gateway resources that reference CloudflareTunnel.
//
// It validates tunnel references via the cfgate.io/tunnel-ref annotation, updates
// Gateway status conditions and addresses based on tunnel state, and counts attached
// routes for listener status. This controller does NOT manage DNS (see CloudflareDNS CRD)
// or tunnel lifecycle (see CloudflareTunnel CRD).
type GatewayReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gatewayclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gatewayclasses/status,verbs=get;update;patch

// Reconcile handles the reconciliation loop for Gateway resources.
// It validates the tunnel reference and updates Gateway status.
//
// The reconciliation proceeds through these phases:
//  1. Fetch the Gateway resource
//  2. Verify GatewayClass is managed by cfgate
//  3. Validate cfgate.io/tunnel-ref annotation
//  4. Resolve the referenced CloudflareTunnel
//  5. Update Gateway status (addresses, conditions, listeners)
//
// On error or missing tunnel, the controller requeues after 30 seconds.
// On success, it requeues after 5 minutes for periodic status sync.
func (r *GatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithName("controller").WithName("gateway").
		WithValues("namespace", req.Namespace, "name", req.Name)
	log.Info("starting reconciliation")

	// 1. Fetch Gateway resource
	var gateway gwapiv1.Gateway
	if err := r.Get(ctx, req.NamespacedName, &gateway); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Gateway not found, ignoring")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get Gateway: %w", err)
	}

	// 2. Check if GatewayClass is ours
	isOurs, err := r.isOurGatewayClass(ctx, &gateway)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check GatewayClass: %w", err)
	}
	if !isOurs {
		log.Info("Gateway is not managed by cfgate, ignoring")
		return ctrl.Result{}, nil
	}

	// 3. Validate tunnel reference annotation
	tunnelRef := annotations.GetAnnotation(&gateway, annotations.AnnotationTunnelRef)
	if tunnelRef == "" {
		log.V(1).Info("Gateway has no tunnel reference annotation")
		meta.SetStatusCondition(&gateway.Status.Conditions, metav1.Condition{
			Type:               string(gwapiv1.GatewayConditionAccepted),
			Status:             metav1.ConditionFalse,
			Reason:             status.ReasonMissingTunnelRef,
			Message:            "cfgate.io/tunnel-ref annotation is required",
			ObservedGeneration: gateway.Generation,
		})
		if err := r.Status().Update(ctx, &gateway); err != nil {
			log.Error(err, "failed to update gateway status")
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// 4. Resolve referenced CloudflareTunnel
	tunnel, err := r.resolveTunnelRef(ctx, &gateway)
	if err != nil {
		log.Error(err, "failed to resolve tunnel reference", "ref", tunnelRef)
		meta.SetStatusCondition(&gateway.Status.Conditions, metav1.Condition{
			Type:               string(gwapiv1.GatewayConditionAccepted),
			Status:             metav1.ConditionFalse,
			Reason:             status.ReasonTunnelNotFound,
			Message:            err.Error(),
			ObservedGeneration: gateway.Generation,
		})
		if err := r.Status().Update(ctx, &gateway); err != nil {
			log.Error(err, "failed to update gateway status")
		}
		r.Recorder.Eventf(&gateway, nil, corev1.EventTypeWarning, "TunnelNotFound", "Validate", "%s", err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// 5. Update Gateway status
	if err := r.updateGatewayStatus(ctx, &gateway, tunnel); err != nil {
		log.Error(err, "failed to update gateway status")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	r.Recorder.Eventf(&gateway, nil, corev1.EventTypeNormal, "Reconciled", "Reconcile", "Gateway reconciled successfully")
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// SetupWithManager sets up the controller with the Manager.
//
// Watched resources:
//   - Gateway (primary resource, with GenerationChangedPredicate)
//   - CloudflareTunnel (with TunnelIDChangedPredicate, triggers status update when tunnel becomes ready)
//   - HTTPRoute (with GenerationChangedPredicate, triggers attachedRoutes recount on route changes)
//
// The controller only processes Gateways whose GatewayClass specifies
// cfgate.io/cloudflare-tunnel-controller as the controller name.
// GenerationChangedPredicate prevents reconciliation on status-only updates,
// reducing spurious reconciliations (201 reconciles/4h observed without predicate).
func (r *GatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	log := mgr.GetLogger().WithName("controller").WithName("gateway")
	log.Info("registering controller with manager")
	return ctrl.NewControllerManagedBy(mgr).
		For(&gwapiv1.Gateway{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Watches(
			&cfgatev1alpha1.CloudflareTunnel{},
			handler.EnqueueRequestsFromMapFunc(r.findGatewaysForTunnel),
			builder.WithPredicates(TunnelIDChangedPredicate),
		).
		Watches(
			&gwapiv1.HTTPRoute{},
			handler.EnqueueRequestsFromMapFunc(r.findGatewaysForHTTPRoute),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Complete(r)
}

// isOurGatewayClass checks if the Gateway's GatewayClass is managed by cfgate.
// Returns true if the GatewayClass spec.controllerName matches GatewayControllerName.
func (r *GatewayReconciler) isOurGatewayClass(ctx context.Context, gateway *gwapiv1.Gateway) (bool, error) {
	var gc gwapiv1.GatewayClass
	if err := r.Get(ctx, types.NamespacedName{Name: string(gateway.Spec.GatewayClassName)}, &gc); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to get GatewayClass: %w", err)
	}

	return string(gc.Spec.ControllerName) == GatewayControllerName, nil
}

// resolveTunnelRef resolves the cfgate.io/tunnel-ref annotation to a CloudflareTunnel.
// The annotation must be in "namespace/name" format. Returns the tunnel or an error
// if the annotation is missing, malformed, or the tunnel does not exist.
func (r *GatewayReconciler) resolveTunnelRef(ctx context.Context, gateway *gwapiv1.Gateway) (*cfgatev1alpha1.CloudflareTunnel, error) {
	tunnelRef := annotations.GetAnnotation(gateway, annotations.AnnotationTunnelRef)
	if tunnelRef == "" {
		return nil, fmt.Errorf("missing %s annotation", annotations.AnnotationTunnelRef)
	}

	namespace, name, err := annotations.ParseNamespacedName(tunnelRef, gateway.Namespace)
	if err != nil {
		return nil, fmt.Errorf("invalid tunnel reference %q: %w", tunnelRef, err)
	}

	var tunnel cfgatev1alpha1.CloudflareTunnel
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &tunnel); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("tunnel %s/%s not found", namespace, name)
		}
		return nil, fmt.Errorf("failed to get tunnel: %w", err)
	}

	return &tunnel, nil
}

// updateGatewayStatus updates the Gateway status based on tunnel state.
// It sets addresses to the tunnel domain, updates Accepted/Programmed conditions,
// and populates listener status with attached route counts.
func (r *GatewayReconciler) updateGatewayStatus(ctx context.Context, gateway *gwapiv1.Gateway, tunnel *cfgatev1alpha1.CloudflareTunnel) error {
	// Set addresses to tunnel domain
	if tunnel.Status.TunnelDomain != "" {
		gateway.Status.Addresses = []gwapiv1.GatewayStatusAddress{
			{
				Type:  ptrTo(gwapiv1.HostnameAddressType),
				Value: tunnel.Status.TunnelDomain,
			},
		}
	}

	// Set conditions
	if tunnel.Status.TunnelID != "" {
		meta.SetStatusCondition(&gateway.Status.Conditions, metav1.Condition{
			Type:               string(gwapiv1.GatewayConditionAccepted),
			Status:             metav1.ConditionTrue,
			Reason:             status.ReasonTunnelReady,
			Message:            "Gateway is bound to tunnel",
			ObservedGeneration: gateway.Generation,
		})
		meta.SetStatusCondition(&gateway.Status.Conditions, metav1.Condition{
			Type:               string(gwapiv1.GatewayConditionProgrammed),
			Status:             metav1.ConditionTrue,
			Reason:             "Programmed",
			Message:            "Gateway configuration applied",
			ObservedGeneration: gateway.Generation,
		})
	} else {
		meta.SetStatusCondition(&gateway.Status.Conditions, metav1.Condition{
			Type:               string(gwapiv1.GatewayConditionAccepted),
			Status:             metav1.ConditionTrue,
			Reason:             status.ReasonTunnelPending,
			Message:            "Waiting for tunnel to be ready",
			ObservedGeneration: gateway.Generation,
		})
		meta.SetStatusCondition(&gateway.Status.Conditions, metav1.Condition{
			Type:               string(gwapiv1.GatewayConditionProgrammed),
			Status:             metav1.ConditionFalse,
			Reason:             status.ReasonTunnelNotReady,
			Message:            "Tunnel is not ready",
			ObservedGeneration: gateway.Generation,
		})
	}

	// Update listener status
	gateway.Status.Listeners = make([]gwapiv1.ListenerStatus, len(gateway.Spec.Listeners))
	for i, listener := range gateway.Spec.Listeners {
		attachedRoutes := r.countAttachedRoutes(ctx, gateway, listener)
		gateway.Status.Listeners[i] = gwapiv1.ListenerStatus{
			Name:           listener.Name,
			AttachedRoutes: attachedRoutes,
			SupportedKinds: []gwapiv1.RouteGroupKind{
				{
					Group: ptrTo(gwapiv1.Group("gateway.networking.k8s.io")),
					Kind:  "HTTPRoute",
				},
			},
			Conditions: []metav1.Condition{
				{
					Type:               string(gwapiv1.ListenerConditionAccepted),
					Status:             metav1.ConditionTrue,
					Reason:             "Accepted",
					Message:            "Listener accepted",
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               string(gwapiv1.ListenerConditionProgrammed),
					Status:             metav1.ConditionTrue,
					Reason:             "Programmed",
					Message:            "Listener programmed",
					LastTransitionTime: metav1.Now(),
				},
			},
		}
	}

	return r.Status().Update(ctx, gateway)
}

// countAttachedRoutes counts the number of HTTPRoutes attached to a Gateway listener.
// It matches routes by parentRef name/namespace and optionally by sectionName.
func (r *GatewayReconciler) countAttachedRoutes(ctx context.Context, gateway *gwapiv1.Gateway, listener gwapiv1.Listener) int32 {
	log := log.FromContext(ctx)
	var routes gwapiv1.HTTPRouteList
	if err := r.List(ctx, &routes); err != nil {
		log.Error(err, "failed to list HTTPRoutes for attached route count")
		return 0
	}

	var count int32
	for _, route := range routes.Items {
		for _, parentRef := range route.Spec.ParentRefs {
			// Skip non-Gateway parentRefs (consistent with findGatewaysForHTTPRoute guard)
			if parentRef.Group != nil && string(*parentRef.Group) != gwapiv1.GroupName {
				continue
			}
			if parentRef.Kind != nil && string(*parentRef.Kind) != "Gateway" {
				continue
			}

			parentNS := route.Namespace
			if parentRef.Namespace != nil {
				parentNS = string(*parentRef.Namespace)
			}

			if string(parentRef.Name) == gateway.Name && parentNS == gateway.Namespace {
				// Check section name if specified
				if parentRef.SectionName != nil && *parentRef.SectionName != listener.Name {
					continue
				}
				count++
			}
		}
	}

	return count
}

// ptrTo returns a pointer to the given value. Generic helper for Gateway API types
// that require pointers for optional fields.
func ptrTo[T any](v T) *T {
	return &v
}

// findGatewaysForTunnel maps a CloudflareTunnel to all Gateways that reference it
// via the cfgate.io/tunnel-ref annotation. Used by the Tunnel watch to trigger
// Gateway reconciliation when TunnelID becomes available.
func (r *GatewayReconciler) findGatewaysForTunnel(ctx context.Context, obj client.Object) []reconcile.Request {
	tunnel, ok := obj.(*cfgatev1alpha1.CloudflareTunnel)
	if !ok {
		return nil
	}

	var gateways gwapiv1.GatewayList
	if err := r.List(ctx, &gateways); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, gw := range gateways.Items {
		ref := annotations.GetAnnotation(&gw, annotations.AnnotationTunnelRef)
		if ref == "" {
			continue
		}
		// Parse annotation to handle both "name" and "namespace/name" formats
		ns, name, err := annotations.ParseNamespacedName(ref, gw.Namespace)
		if err != nil {
			continue
		}
		if name == tunnel.Name && ns == tunnel.Namespace {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: gw.Namespace,
					Name:      gw.Name,
				},
			})
		}
	}
	return requests
}

// findGatewaysForHTTPRoute maps an HTTPRoute to the Gateways it references
// via parentRefs. Used by the HTTPRoute watch to trigger Gateway reconciliation
// when routes are created, updated, or deleted, keeping listener.attachedRoutes
// counts current.
func (r *GatewayReconciler) findGatewaysForHTTPRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	route, ok := obj.(*gwapiv1.HTTPRoute)
	if !ok {
		return nil
	}

	var requests []reconcile.Request
	seen := make(map[types.NamespacedName]bool)
	for _, parentRef := range route.Spec.ParentRefs {
		if parentRef.Group != nil && string(*parentRef.Group) != gwapiv1.GroupName {
			continue
		}
		if parentRef.Kind != nil && string(*parentRef.Kind) != "Gateway" {
			continue
		}

		gwNamespace := route.Namespace
		if parentRef.Namespace != nil {
			gwNamespace = string(*parentRef.Namespace)
		}
		nn := types.NamespacedName{
			Name:      string(parentRef.Name),
			Namespace: gwNamespace,
		}
		if seen[nn] {
			continue
		}
		seen[nn] = true
		requests = append(requests, reconcile.Request{NamespacedName: nn})
	}
	return requests
}

// GatewayClassReconciler reconciles GatewayClass resources to set Accepted status.
//
// Per Gateway API spec (GEP-1364), controllers MUST set the Accepted condition on
// GatewayClass resources whose spec.controllerName matches. This reconciler sets
// Accepted=True for GatewayClasses managed by cfgate, enabling tools like Kiali and
// kubectl to show the class as ready. Non-matching GatewayClasses are ignored.
type GatewayClassReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gatewayclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gatewayclasses/status,verbs=get;update;patch

// Reconcile handles the reconciliation loop for GatewayClass resources.
// It sets Accepted=True on GatewayClasses with matching controllerName.
//
// The reconciliation proceeds through these phases:
//  1. Fetch the GatewayClass resource
//  2. Check if spec.controllerName matches GatewayControllerName
//  3. If match: set Accepted=True condition (only if not already set)
//  4. Update status subresource
//
// Non-matching GatewayClasses are ignored (another controller owns them).
// Periodic requeue (5m) provides self-healing: if a status update is lost due to
// conflict or transient error, the controller will re-verify and restore the
// Accepted condition on the next cycle.
func (r *GatewayClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithName("controller").WithName("gatewayclass").
		WithValues("name", req.Name)

	// 1. Fetch GatewayClass
	var gc gwapiv1.GatewayClass
	if err := r.Get(ctx, req.NamespacedName, &gc); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("GatewayClass not found, ignoring")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get GatewayClass: %w", err)
	}

	// 2. Check if this GatewayClass is managed by cfgate
	if string(gc.Spec.ControllerName) != GatewayControllerName {
		return ctrl.Result{}, nil
	}

	log.Info("reconciling GatewayClass")

	// 3. Check if Accepted condition already matches desired state
	existing := meta.FindStatusCondition(gc.Status.Conditions, string(gwapiv1.GatewayClassConditionStatusAccepted))
	if existing != nil &&
		existing.Status == metav1.ConditionTrue &&
		existing.Reason == string(gwapiv1.GatewayClassReasonAccepted) &&
		existing.ObservedGeneration == gc.Generation {
		log.V(1).Info("GatewayClass already accepted, skipping status update")
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}

	// 4. Set Accepted=True
	meta.SetStatusCondition(&gc.Status.Conditions, metav1.Condition{
		Type:               string(gwapiv1.GatewayClassConditionStatusAccepted),
		Status:             metav1.ConditionTrue,
		Reason:             string(gwapiv1.GatewayClassReasonAccepted),
		Message:            "cfgate accepts this GatewayClass",
		ObservedGeneration: gc.Generation,
	})

	if err := r.Status().Update(ctx, &gc); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update GatewayClass status: %w", err)
	}

	log.Info("GatewayClass accepted")
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// SetupWithManager sets up the GatewayClass controller with the Manager.
//
// Watched resources:
//   - GatewayClass (primary resource, with GenerationChangedPredicate)
//
// GatewayClass is cluster-scoped. This is a separate controller from GatewayReconciler
// because GatewayClass and Gateway have different scoping and reconciliation needs.
func (r *GatewayClassReconciler) SetupWithManager(mgr ctrl.Manager) error {
	log := mgr.GetLogger().WithName("controller").WithName("gatewayclass")
	log.Info("registering controller with manager")
	return ctrl.NewControllerManagedBy(mgr).
		For(&gwapiv1.GatewayClass{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Complete(r)
}

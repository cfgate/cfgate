package controller

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	gwapiv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/controller/annotations"
	"cfgate.io/cfgate/internal/controller/status"
)

// HTTPRouteReconciler reconciles HTTPRoute resources.
// It validates routes against Gateway configuration, resolves backend
// Services, checks annotation validity, and resolves CloudflareAccessPolicy
// references.
type HTTPRouteReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gatewayclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=referencegrants,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflareaccesspolicies,verbs=get;list;watch

// Reconcile handles the reconciliation loop for HTTPRoute resources.
// It validates the route against parent Gateways, validates annotations,
// resolves backend Services, and resolves CloudflareAccessPolicy references.
//
// The reconciliation proceeds through these phases:
//  1. Fetch the HTTPRoute resource
//  2. Validate cfgate.io/* annotations (emit warnings for deprecated ones)
//  3. Preserve other controllers' status.parents[] entries
//  4. Filter and validate only cfgate-managed parentRefs
//  5. Resolve backend Service references
//  6. Resolve cfgate.io/access-policy reference (if present)
//  7. Merge conditions and update route status
//  8. Emit reconciled event
//
// parents[] preservation: Per Gateway API spec, controllers MUST NOT modify
// entries with non-matching controllerName. This implementation preserves
// entries from other controllers (e.g., Istio) and only rebuilds cfgate's
// own entries. Non-cfgate parentRefs are skipped entirely.
//
// On error, the controller requeues after 30 seconds. On success, it requeues
// after 5 minutes for periodic validation.
func (r *HTTPRouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithName("controller").WithName("httproute")
	log.Info("starting reconciliation", "namespace", req.Namespace, "name", req.Name)

	// 1. Fetch HTTPRoute resource
	var route gwapiv1.HTTPRoute
	if err := r.Get(ctx, req.NamespacedName, &route); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("HTTPRoute not found, ignoring")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get HTTPRoute: %w", err)
	}

	// 2. Validate annotations
	validationResult := annotations.ValidateRouteAnnotations(&route, false /* requireHostname */)
	for _, warning := range validationResult.Warnings {
		r.Recorder.Eventf(&route, nil, corev1.EventTypeWarning, "DeprecatedAnnotation", "Validate", warning)
		log.Info("deprecated annotation detected", "warning", warning)
	}
	for _, errMsg := range validationResult.Errors {
		r.Recorder.Eventf(&route, nil, corev1.EventTypeWarning, "InvalidAnnotation", "Validate", errMsg)
		log.Info("invalid annotation value", "error", errMsg)
	}

	// 2b. Emit notices for regex path matches; Exact and PathPrefix are translated
	// into anchored cloudflared regexes during tunnel configuration sync.
	r.validatePathTypes(&route)

	// 3. Preserve other controllers' status entries (spec: MUST NOT modify
	// entries with non-matching controllerName). Start from existing parents,
	// remove only cfgate's entries, then rebuild cfgate's entries below.
	var preserved []gwapiv1.RouteParentStatus
	hasCfgateStatusEntries := false
	for _, p := range route.Status.Parents {
		if string(p.ControllerName) == GatewayControllerName {
			hasCfgateStatusEntries = true
			continue
		}
		preserved = append(preserved, p)
	}

	// 4. Validate each parentRef; only process cfgate-managed parents.
	// Per spec: "Implementations of this API can only populate Route status
	// for the Gateways/parent resources they are responsible for."
	var cfgateParentStatuses []gwapiv1.RouteParentStatus
	for _, parentRef := range route.Spec.ParentRefs {
		isCfgate, err := r.isCfgateParentRef(ctx, &route, parentRef)
		if err != nil {
			log.Error(err, "failed to check parentRef ownership")
			continue
		}
		if !isCfgate {
			log.V(1).Info("skipping non-cfgate parentRef",
				"parentRef", parentRef.Name,
			)
			continue
		}
		parentStatus := r.validateParentRef(ctx, &route, parentRef)
		cfgateParentStatuses = append(cfgateParentStatuses, parentStatus)
	}

	// 5. Resolve backend Services
	resolvedRefsCondition := r.resolveBackends(ctx, &route)

	// 6. Resolve access policy reference (condition omitted when annotation absent)
	accessPolicyCondition, hasAccessPolicy := r.resolveAccessPolicy(ctx, &route)

	// 7. Update route status - merge conditions into each cfgate parent status,
	// then combine with preserved entries from other controllers.
	for i := range cfgateParentStatuses {
		conditions := []metav1.Condition{resolvedRefsCondition}
		if hasAccessPolicy {
			conditions = append(conditions, accessPolicyCondition)
		}
		cfgateParentStatuses[i].Conditions = status.MergeConditions(
			cfgateParentStatuses[i].Conditions,
			conditions...,
		)
	}
	if len(preserved) == 0 && len(cfgateParentStatuses) == 0 && !hasCfgateStatusEntries {
		log.V(1).Info("no parent statuses to write")
		r.Recorder.Eventf(&route, nil, corev1.EventTypeNormal, "Reconciled", "Reconcile", "HTTPRoute reconciled successfully")
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}

	if len(preserved) == 0 && len(cfgateParentStatuses) == 0 {
		route.Status.Parents = make([]gwapiv1.RouteParentStatus, 0)
	} else {
		route.Status.Parents = append(preserved, cfgateParentStatuses...)
	}

	if err := r.Status().Update(ctx, &route); err != nil {
		log.Error(err, "failed to update route status")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// 8. Emit reconciled event
	r.Recorder.Eventf(&route, nil, corev1.EventTypeNormal, "Reconciled", "Reconcile", "HTTPRoute reconciled successfully")
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// SetupWithManager sets up the controller with the Manager.
//
// Watched resources:
//   - HTTPRoute (primary resource, with CfgateAnnotationOrGenerationPredicate)
//   - Gateway (with CfgateAnnotationOrGenerationPredicate for cfgate.io/* annotation awareness)
//   - Service (no predicate -- service changes are rare and important)
//   - CloudflareAccessPolicy (with GenerationChangedPredicate to filter status-only updates)
//
// CfgateAnnotationOrGenerationPredicate on For() prevents status-only update loops
// while still allowing annotation-only cfgate.io/* changes to trigger reconcile.
func (r *HTTPRouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	log := mgr.GetLogger().WithName("controller").WithName("httproute")
	log.Info("registering controller with manager")
	return ctrl.NewControllerManagedBy(mgr).
		For(&gwapiv1.HTTPRoute{},
			builder.WithPredicates(CfgateAnnotationOrGenerationPredicate),
		).
		Watches(
			&gwapiv1.Gateway{},
			handler.EnqueueRequestsFromMapFunc(r.findRoutesForGateway),
			builder.WithPredicates(CfgateAnnotationOrGenerationPredicate, GatewayCreateAnnotationFilter),
		).
		Watches(
			&corev1.Service{},
			handler.EnqueueRequestsFromMapFunc(r.findRoutesForService),
		).
		Watches(
			&cfgatev1alpha1.CloudflareAccessPolicy{},
			handler.EnqueueRequestsFromMapFunc(r.findRoutesForAccessPolicy),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Complete(r)
}

// findRoutesForGateway returns HTTPRoutes that reference the given Gateway.
func (r *HTTPRouteReconciler) findRoutesForGateway(ctx context.Context, obj client.Object) []reconcile.Request {
	gateway := obj.(*gwapiv1.Gateway)
	log := log.FromContext(ctx)

	var routes gwapiv1.HTTPRouteList
	if err := r.List(ctx, &routes); err != nil {
		log.Error(err, "failed to list HTTPRoutes")
		return nil
	}

	var requests []reconcile.Request
	for _, route := range routes.Items {
		for _, ref := range route.Spec.ParentRefs {
			// Skip non-Gateway parentRefs (consistent with isCfgateParentRef guard)
			if ref.Group != nil && string(*ref.Group) != gwapiv1.GroupName {
				continue
			}
			if ref.Kind != nil && string(*ref.Kind) != "Gateway" {
				continue
			}

			refNS := route.Namespace
			if ref.Namespace != nil {
				refNS = string(*ref.Namespace)
			}
			if string(ref.Name) == gateway.Name && refNS == gateway.Namespace {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      route.Name,
						Namespace: route.Namespace,
					},
				})
				break
			}
		}
	}

	return requests
}

// findRoutesForService returns HTTPRoutes that reference the given Service.
func (r *HTTPRouteReconciler) findRoutesForService(ctx context.Context, obj client.Object) []reconcile.Request {
	svc := obj.(*corev1.Service)
	log := log.FromContext(ctx)

	var routes gwapiv1.HTTPRouteList
	if err := r.List(ctx, &routes); err != nil {
		log.Error(err, "failed to list HTTPRoutes")
		return nil
	}

	var requests []reconcile.Request
	for _, route := range routes.Items {
		for _, rule := range route.Spec.Rules {
			for _, backend := range rule.BackendRefs {
				// Skip non-Service backends
				if backend.Kind != nil && *backend.Kind != "Service" {
					continue
				}
				backendNS := route.Namespace
				if backend.Namespace != nil {
					backendNS = string(*backend.Namespace)
				}
				if string(backend.Name) == svc.Name && backendNS == svc.Namespace {
					requests = append(requests, reconcile.Request{
						NamespacedName: types.NamespacedName{
							Name:      route.Name,
							Namespace: route.Namespace,
						},
					})
					break
				}
			}
		}
	}

	return requests
}

// findRoutesForAccessPolicy returns HTTPRoutes that reference the given CloudflareAccessPolicy.
func (r *HTTPRouteReconciler) findRoutesForAccessPolicy(ctx context.Context, obj client.Object) []reconcile.Request {
	policy := obj.(*cfgatev1alpha1.CloudflareAccessPolicy)
	log := log.FromContext(ctx)

	var routes gwapiv1.HTTPRouteList
	if err := r.List(ctx, &routes); err != nil {
		log.Error(err, "failed to list HTTPRoutes")
		return nil
	}

	var requests []reconcile.Request
	for _, route := range routes.Items {
		policyRef := annotations.GetAnnotation(&route, annotations.AnnotationAccessPolicy)
		if policyRef == "" {
			continue
		}

		// Parse namespace/name format
		policyNS, policyName, err := parsePolicyRef(policyRef, route.Namespace)
		if err != nil {
			continue
		}
		if policyName == policy.Name && policyNS == policy.Namespace {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      route.Name,
					Namespace: route.Namespace,
				},
			})
		}
	}

	return requests
}

// isCfgateParentRef checks whether a parentRef points to a Gateway whose
// GatewayClass is managed by cfgate. Returns (true, nil) for cfgate-managed
// parents, (false, nil) for non-cfgate parents or missing resources, and
// (false, err) on unexpected API errors.
func (r *HTTPRouteReconciler) isCfgateParentRef(
	ctx context.Context,
	route *gwapiv1.HTTPRoute,
	ref gwapiv1.ParentReference,
) (bool, error) {
	// Validate Group defaults to gateway.networking.k8s.io
	if ref.Group != nil && string(*ref.Group) != gwapiv1.GroupName {
		return false, nil
	}

	// Validate Kind defaults to "Gateway"
	if ref.Kind != nil && string(*ref.Kind) != "Gateway" {
		return false, nil
	}

	// Resolve Gateway namespace
	gwNamespace := route.Namespace
	if ref.Namespace != nil {
		gwNamespace = string(*ref.Namespace)
	}

	// Look up the Gateway
	var gateway gwapiv1.Gateway
	if err := r.Get(ctx, types.NamespacedName{
		Name:      string(ref.Name),
		Namespace: gwNamespace,
	}, &gateway); err != nil {
		if apierrors.IsNotFound(err) {
			// Gateway not found, cannot determine ownership, skip gracefully
			return false, nil
		}
		return false, fmt.Errorf("failed to get Gateway %s/%s: %w", gwNamespace, ref.Name, err)
	}

	// Look up the GatewayClass
	var gc gwapiv1.GatewayClass
	if err := r.Get(ctx, types.NamespacedName{Name: string(gateway.Spec.GatewayClassName)}, &gc); err != nil {
		if apierrors.IsNotFound(err) {
			// GatewayClass not found, cannot determine ownership, skip gracefully
			return false, nil
		}
		return false, fmt.Errorf("failed to get GatewayClass %s: %w", gateway.Spec.GatewayClassName, err)
	}

	return string(gc.Spec.ControllerName) == GatewayControllerName, nil
}

// validateParentRef validates that the parent Gateway accepts this route.
// Returns a RouteParentStatus with appropriate conditions.
func (r *HTTPRouteReconciler) validateParentRef(
	ctx context.Context,
	route *gwapiv1.HTTPRoute,
	ref gwapiv1.ParentReference,
) gwapiv1.RouteParentStatus {
	log := log.FromContext(ctx)

	// Build base status
	parentNS := gwapiv1.Namespace(route.Namespace)
	if ref.Namespace != nil {
		parentNS = *ref.Namespace
	}

	parentStatus := gwapiv1.RouteParentStatus{
		ParentRef: gwapiv1.ParentReference{
			Group:       ref.Group,
			Kind:        ref.Kind,
			Namespace:   &parentNS,
			Name:        ref.Name,
			SectionName: ref.SectionName,
		},
		ControllerName: GatewayControllerName,
		Conditions: []metav1.Condition{
			status.NewCondition(
				string(gwapiv1.RouteConditionAccepted),
				metav1.ConditionTrue,
				"Accepted",
				"Route accepted by Gateway",
				route.Generation,
			),
		},
	}

	// Get the Gateway
	gwNamespace := route.Namespace
	if ref.Namespace != nil {
		gwNamespace = string(*ref.Namespace)
	}

	var gateway gwapiv1.Gateway
	if err := r.Get(ctx, types.NamespacedName{
		Name:      string(ref.Name),
		Namespace: gwNamespace,
	}, &gateway); err != nil {
		if apierrors.IsNotFound(err) {
			parentStatus.Conditions[0] = status.NewCondition(
				string(gwapiv1.RouteConditionAccepted),
				metav1.ConditionFalse,
				status.ReasonNoMatchingParent,
				fmt.Sprintf("Gateway %s/%s not found", gwNamespace, ref.Name),
				route.Generation,
			)
			return parentStatus
		}
		log.Error(err, "failed to get Gateway")
		return parentStatus
	}

	// Check if Gateway's GatewayClass is ours
	var gc gwapiv1.GatewayClass
	if err := r.Get(ctx, types.NamespacedName{Name: string(gateway.Spec.GatewayClassName)}, &gc); err != nil {
		parentStatus.Conditions[0] = status.NewCondition(
			string(gwapiv1.RouteConditionAccepted),
			metav1.ConditionFalse,
			status.ReasonNoMatchingParent,
			fmt.Sprintf("GatewayClass %s not found", gateway.Spec.GatewayClassName),
			route.Generation,
		)
		return parentStatus
	}

	if string(gc.Spec.ControllerName) != GatewayControllerName {
		parentStatus.Conditions[0] = status.NewCondition(
			string(gwapiv1.RouteConditionAccepted),
			metav1.ConditionFalse,
			status.ReasonNoMatchingParent,
			"Gateway is not managed by cfgate",
			route.Generation,
		)
		return parentStatus
	}

	// Check if Gateway has tunnel reference
	if annotations.GetAnnotation(&gateway, annotations.AnnotationTunnelRef) == "" {
		parentStatus.Conditions[0] = status.NewCondition(
			string(gwapiv1.RouteConditionAccepted),
			metav1.ConditionFalse,
			status.ReasonNoTunnelRef,
			"Gateway has no tunnel reference annotation",
			route.Generation,
		)
		return parentStatus
	}

	listenerOK, reason, message := r.routeAllowedByListeners(ctx, route, &gateway, ref)
	if !listenerOK {
		parentStatus.Conditions[0] = status.NewCondition(
			string(gwapiv1.RouteConditionAccepted),
			metav1.ConditionFalse,
			reason,
			message,
			route.Generation,
		)
		return parentStatus
	}

	return parentStatus
}

// resolveBackends resolves backend Service references.
// Returns a ResolvedRefs condition indicating success or failure.
func (r *HTTPRouteReconciler) resolveBackends(
	ctx context.Context,
	route *gwapiv1.HTTPRoute,
) metav1.Condition {
	log := log.FromContext(ctx)

	for _, rule := range route.Spec.Rules {
		for _, match := range rule.Matches {
			if err := validateCloudflaredPathMatch(match); err != nil {
				return status.NewCondition(
					string(gwapiv1.RouteConditionResolvedRefs),
					metav1.ConditionFalse,
					status.ReasonUnsupportedValue,
					err.Error(),
					route.Generation,
				)
			}
		}

		if len(rule.BackendRefs) > 1 {
			return status.NewCondition(
				string(gwapiv1.RouteConditionResolvedRefs),
				metav1.ConditionFalse,
				status.ReasonUnsupportedValue,
				"multiple backendRefs are not supported by cfgate tunnel ingress",
				route.Generation,
			)
		}

		for _, backend := range rule.BackendRefs {
			if backend.Group != nil && *backend.Group != "" && *backend.Group != "core" {
				return status.NewCondition(
					string(gwapiv1.RouteConditionResolvedRefs),
					metav1.ConditionFalse,
					status.ReasonUnsupportedValue,
					fmt.Sprintf("unsupported backend group %q: only core Service backends are supported by cfgate tunnel ingress", *backend.Group),
					route.Generation,
				)
			}
			if backend.Kind != nil && *backend.Kind != "" && *backend.Kind != "Service" {
				return status.NewCondition(
					string(gwapiv1.RouteConditionResolvedRefs),
					metav1.ConditionFalse,
					status.ReasonUnsupportedValue,
					fmt.Sprintf("unsupported backend kind %q: only Service backends are supported by cfgate tunnel ingress", *backend.Kind),
					route.Generation,
				)
			}

			// Get the Service
			namespace := route.Namespace
			if backend.Namespace != nil {
				namespace = string(*backend.Namespace)
			}

			if namespace != route.Namespace {
				permitted, err := r.backendReferencePermitted(ctx, route.Namespace, namespace, string(backend.Name))
				if err != nil {
					return status.NewCondition(
						string(gwapiv1.RouteConditionResolvedRefs),
						metav1.ConditionFalse,
						status.ReasonRefNotPermitted,
						fmt.Sprintf("Failed to check ReferenceGrant for Service %s/%s: %v", namespace, backend.Name, err),
						route.Generation,
					)
				}
				if !permitted {
					return status.NewCondition(
						string(gwapiv1.RouteConditionResolvedRefs),
						metav1.ConditionFalse,
						status.ReasonRefNotPermitted,
						fmt.Sprintf("Service %s/%s is not permitted by ReferenceGrant", namespace, backend.Name),
						route.Generation,
					)
				}
			}

			var svc corev1.Service
			if err := r.Get(ctx, types.NamespacedName{
				Name:      string(backend.Name),
				Namespace: namespace,
			}, &svc); err != nil {
				if apierrors.IsNotFound(err) {
					log.Info("backend Service not found",
						"service", backend.Name,
						"namespace", namespace,
					)
					return status.NewCondition(
						string(gwapiv1.RouteConditionResolvedRefs),
						metav1.ConditionFalse,
						status.ReasonBackendNotFound,
						fmt.Sprintf("Service %s/%s not found", namespace, backend.Name),
						route.Generation,
					)
				}
				log.Error(err, "failed to get Service")
				return status.NewCondition(
					string(gwapiv1.RouteConditionResolvedRefs),
					metav1.ConditionFalse,
					status.ReasonBackendNotFound,
					fmt.Sprintf("Failed to get Service %s/%s: %v", namespace, backend.Name, err),
					route.Generation,
				)
			}
		}
	}

	return status.NewCondition(
		string(gwapiv1.RouteConditionResolvedRefs),
		metav1.ConditionTrue,
		"ResolvedRefs",
		"All backend references resolved",
		route.Generation,
	)
}

// resolveAccessPolicy resolves the referenced CloudflareAccessPolicy.
// Returns a condition indicating the resolution status and whether the condition
// should be set. When no access-policy annotation is present, returns (zero, false)
// so the caller omits the condition entirely — "not applicable" is best represented
// by absence rather than a misleading True/False status.
func (r *HTTPRouteReconciler) resolveAccessPolicy(
	ctx context.Context,
	route *gwapiv1.HTTPRoute,
) (metav1.Condition, bool) {
	log := log.FromContext(ctx)

	policyRef := annotations.GetAnnotation(route, annotations.AnnotationAccessPolicy)
	if policyRef == "" {
		// No access policy annotation — condition not applicable, omit entirely
		return metav1.Condition{}, false
	}

	// Parse namespace/name format
	policyNS, policyName, err := parsePolicyRef(policyRef, route.Namespace)
	if err != nil {
		log.Info("invalid access policy reference", "ref", policyRef, "error", err)
		r.Recorder.Eventf(route, nil, corev1.EventTypeWarning, status.ReasonInvalidPolicyRef, "Validate",
			"Invalid access policy reference %q: %v", policyRef, err)
		return status.NewCondition(
			status.ConditionTypeAccessPolicyResolved,
			metav1.ConditionFalse,
			status.ReasonInvalidPolicyRef,
			err.Error(),
			route.Generation,
		), true
	}

	var policy cfgatev1alpha1.CloudflareAccessPolicy
	if err := r.Get(ctx, types.NamespacedName{
		Name:      policyName,
		Namespace: policyNS,
	}, &policy); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("referenced CloudflareAccessPolicy not found",
				"policy", policyRef,
				"parsedNamespace", policyNS,
				"parsedName", policyName,
			)
			r.Recorder.Eventf(route, nil, corev1.EventTypeWarning, status.ReasonAccessPolicyNotFound, "Resolve",
				"Referenced CloudflareAccessPolicy %q not found", policyRef)
			return status.NewCondition(
				status.ConditionTypeAccessPolicyResolved,
				metav1.ConditionFalse,
				status.ReasonAccessPolicyNotFound,
				fmt.Sprintf("CloudflareAccessPolicy %s/%s not found", policyNS, policyName),
				route.Generation,
			), true
		}
		log.Error(err, "failed to get CloudflareAccessPolicy")
		return status.NewCondition(
			status.ConditionTypeAccessPolicyResolved,
			metav1.ConditionFalse,
			status.ReasonAccessPolicyError,
			fmt.Sprintf("Failed to resolve CloudflareAccessPolicy: %v", err),
			route.Generation,
		), true
	}

	log.V(1).Info("resolved deprecated access policy annotation",
		"policy", policyRef,
		"policyId", policy.Status.PolicyID,
	)
	r.Recorder.Eventf(route, nil, corev1.EventTypeNormal, status.ConditionTypeAccessPolicyResolved, "Resolve",
		"Deprecated annotation resolved CloudflareAccessPolicy %q; create CloudflareAccessApplication to attach Access resources", policyRef)

	return status.NewCondition(
		status.ConditionTypeAccessPolicyResolved,
		metav1.ConditionTrue,
		status.ReasonResolved,
		fmt.Sprintf("Deprecated annotation resolved CloudflareAccessPolicy %s/%s; create CloudflareAccessApplication to attach Access resources", policyNS, policyName),
		route.Generation,
	), true
}

// validatePathTypes emits notice events for raw regex path matches.
func (r *HTTPRouteReconciler) validatePathTypes(route *gwapiv1.HTTPRoute) {
	for _, rule := range route.Spec.Rules {
		for _, match := range rule.Matches {
			if match.Path == nil || match.Path.Type == nil {
				continue
			}
			pathVal := ""
			if match.Path.Value != nil {
				pathVal = *match.Path.Value
			}
			switch *match.Path.Type {
			case gwapiv1.PathMatchRegularExpression:
				r.Recorder.Eventf(route, nil, corev1.EventTypeWarning, "PathTypeNotice", "Validate",
					"RegularExpression path %q is passed directly to cloudflared regex engine (substring match, not full-string)", pathVal)
			}
		}
	}
}

// parsePolicyRef parses a policy reference in "namespace/name" or "name" format.
// Delegates to annotations.ParseNamespacedName for consistent validation.
func parsePolicyRef(ref string, defaultNS string) (string, string, error) {
	return annotations.ParseNamespacedName(ref, defaultNS)
}

func (r *HTTPRouteReconciler) routeAllowedByListeners(
	ctx context.Context,
	route *gwapiv1.HTTPRoute,
	gateway *gwapiv1.Gateway,
	ref gwapiv1.ParentReference,
) (bool, string, string) {
	foundListener := false
	for _, listener := range gateway.Spec.Listeners {
		if ref.SectionName != nil && listener.Name != *ref.SectionName {
			continue
		}
		foundListener = true
		if listener.Protocol != gwapiv1.HTTPProtocolType && listener.Protocol != gwapiv1.HTTPSProtocolType {
			continue
		}
		if !listenerAllowsHTTPRouteKind(listener) {
			continue
		}
		if !r.listenerAllowsRouteNamespace(ctx, route, gateway, listener) {
			continue
		}
		if !listenerHostnameCompatible(route, listener) {
			continue
		}
		return true, "Accepted", "Route accepted by Gateway"
	}

	if !foundListener {
		if ref.SectionName != nil {
			return false, status.ReasonNoMatchingListenerHostname, fmt.Sprintf("Listener %s not found", *ref.SectionName)
		}
		return false, status.ReasonNoMatchingListenerHostname, "No compatible listener found"
	}
	return false, status.ReasonNotAllowedByListeners, "Route is not allowed by Gateway listeners"
}

func listenerAllowsHTTPRouteKind(listener gwapiv1.Listener) bool {
	if listener.AllowedRoutes == nil || len(listener.AllowedRoutes.Kinds) == 0 {
		return true
	}
	for _, kind := range listener.AllowedRoutes.Kinds {
		if kind.Group != nil && string(*kind.Group) != gwapiv1.GroupName {
			continue
		}
		if kind.Kind == "HTTPRoute" {
			return true
		}
	}
	return false
}

func (r *HTTPRouteReconciler) listenerAllowsRouteNamespace(
	ctx context.Context,
	route *gwapiv1.HTTPRoute,
	gateway *gwapiv1.Gateway,
	listener gwapiv1.Listener,
) bool {
	from := gwapiv1.NamespacesFromSame
	var selector *metav1.LabelSelector
	if listener.AllowedRoutes != nil && listener.AllowedRoutes.Namespaces != nil {
		if listener.AllowedRoutes.Namespaces.From != nil {
			from = *listener.AllowedRoutes.Namespaces.From
		}
		selector = listener.AllowedRoutes.Namespaces.Selector
	}

	switch from {
	case gwapiv1.NamespacesFromAll:
		return true
	case gwapiv1.NamespacesFromSelector:
		if selector == nil {
			return false
		}
		labelSelector, err := metav1.LabelSelectorAsSelector(selector)
		if err != nil {
			return false
		}
		var ns corev1.Namespace
		if err := r.Get(ctx, types.NamespacedName{Name: route.Namespace}, &ns); err != nil {
			return false
		}
		return labelSelector.Matches(labelsSet(ns.Labels))
	default:
		return route.Namespace == gateway.Namespace
	}
}

func labelsSet(m map[string]string) labelsAdapter {
	return labelsAdapter(m)
}

type labelsAdapter map[string]string

func (l labelsAdapter) Has(label string) bool {
	_, ok := l[label]
	return ok
}

func (l labelsAdapter) Get(label string) string {
	return l[label]
}

func (l labelsAdapter) Lookup(label string) (string, bool) {
	v, ok := l[label]
	return v, ok
}

func listenerHostnameCompatible(route *gwapiv1.HTTPRoute, listener gwapiv1.Listener) bool {
	if listener.Hostname == nil || *listener.Hostname == "" || len(route.Spec.Hostnames) == 0 {
		return true
	}
	for _, routeHostname := range route.Spec.Hostnames {
		if hostnameMatches(string(routeHostname), string(*listener.Hostname)) {
			return true
		}
	}
	return false
}

func hostnameMatches(routeHostname, listenerHostname string) bool {
	routeHostname = strings.ToLower(routeHostname)
	listenerHostname = strings.ToLower(listenerHostname)
	if routeHostname == listenerHostname {
		return true
	}
	if strings.HasPrefix(listenerHostname, "*.") {
		suffix := strings.TrimPrefix(listenerHostname, "*")
		return strings.HasSuffix(routeHostname, suffix) && routeHostname != strings.TrimPrefix(suffix, ".")
	}
	if strings.HasPrefix(routeHostname, "*.") {
		suffix := strings.TrimPrefix(routeHostname, "*")
		return strings.HasSuffix(listenerHostname, suffix) && listenerHostname != strings.TrimPrefix(suffix, ".")
	}
	return false
}

func validateCloudflaredPathMatch(match gwapiv1.HTTPRouteMatch) error {
	if match.Path == nil || match.Path.Value == nil {
		return nil
	}
	matchType := gwapiv1.PathMatchPathPrefix
	if match.Path.Type != nil {
		matchType = *match.Path.Type
	}
	switch matchType {
	case gwapiv1.PathMatchPathPrefix, gwapiv1.PathMatchExact:
		return nil
	case gwapiv1.PathMatchRegularExpression:
		if _, err := regexp.Compile(*match.Path.Value); err != nil {
			return fmt.Errorf("unsupported path regular expression %q: %w", *match.Path.Value, err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported path match type %q", matchType)
	}
}

func (r *HTTPRouteReconciler) backendReferencePermitted(ctx context.Context, fromNamespace, toNamespace, serviceName string) (bool, error) {
	var grants gwapiv1b1.ReferenceGrantList
	if err := r.List(ctx, &grants, client.InNamespace(toNamespace)); err != nil {
		return false, err
	}

	for _, grant := range grants.Items {
		fromOK := false
		for _, from := range grant.Spec.From {
			if from.Group == gwapiv1.GroupName && from.Kind == "HTTPRoute" && string(from.Namespace) == fromNamespace {
				fromOK = true
				break
			}
		}
		if !fromOK {
			continue
		}
		for _, to := range grant.Spec.To {
			if to.Group != "" || to.Kind != "Service" {
				continue
			}
			if to.Name == nil || string(*to.Name) == serviceName {
				return true, nil
			}
		}
	}
	return false, nil
}

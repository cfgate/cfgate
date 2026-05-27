package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"cfgate.io/cfgate/internal/controller/annotations"
	"cfgate.io/cfgate/internal/controller/status"
)

type httpRouteParentEvaluationOptions struct {
	requireTunnelRef    bool
	validatePathMatches bool
}

type httpRouteParentEvaluation struct {
	Accepted          bool
	IsCfgateParent    bool
	Gateway           *gatewayv1.Gateway
	AcceptedListeners []gatewayv1.Listener
	Status            gatewayv1.RouteParentStatus
	Reason            string
	Message           string
}

type httpRouteListenerEvaluation struct {
	Accepted bool
	Reason   string
	Message  string
}

func evaluateHTTPRouteParentRef(ctx context.Context, reader client.Reader, route *gatewayv1.HTTPRoute, ref gatewayv1.ParentReference, opts httpRouteParentEvaluationOptions) (httpRouteParentEvaluation, error) {
	eval := httpRouteParentEvaluation{
		Status:  newHTTPRouteParentStatus(route, ref),
		Reason:  "Accepted",
		Message: "Route accepted by Gateway",
	}

	if !isGatewayParentRef(ref) {
		eval.Reason = status.ReasonNoMatchingParent
		eval.Message = "ParentRef is not a Gateway"
		eval.Status.Conditions[0] = acceptedCondition(route, metav1.ConditionFalse, eval.Reason, eval.Message)
		return eval, nil
	}

	gwNamespace := route.Namespace
	if ref.Namespace != nil {
		gwNamespace = string(*ref.Namespace)
	}

	var gateway gatewayv1.Gateway
	if err := reader.Get(ctx, types.NamespacedName{Name: string(ref.Name), Namespace: gwNamespace}, &gateway); err != nil {
		if apierrors.IsNotFound(err) {
			eval.Reason = status.ReasonNoMatchingParent
			eval.Message = fmt.Sprintf("Gateway %s/%s not found", gwNamespace, ref.Name)
			eval.Status.Conditions[0] = acceptedCondition(route, metav1.ConditionFalse, eval.Reason, eval.Message)
			return eval, nil
		}
		return eval, fmt.Errorf("failed to get Gateway %s/%s: %w", gwNamespace, ref.Name, err)
	}
	eval.Gateway = &gateway

	managed, err := gatewayClassManagedByCfgate(ctx, reader, &gateway)
	if err != nil {
		eval.Reason = status.ReasonNoMatchingParent
		eval.Message = err.Error()
		eval.Status.Conditions[0] = acceptedCondition(route, metav1.ConditionFalse, eval.Reason, eval.Message)
		return eval, nil
	}
	if !managed {
		eval.Reason = status.ReasonNoMatchingParent
		eval.Message = "Gateway is not managed by cfgate"
		eval.Status.Conditions[0] = acceptedCondition(route, metav1.ConditionFalse, eval.Reason, eval.Message)
		return eval, nil
	}
	eval.IsCfgateParent = true

	if opts.requireTunnelRef && annotations.GetAnnotation(&gateway, annotations.AnnotationTunnelRef) == "" {
		eval.Reason = status.ReasonNoTunnelRef
		eval.Message = "Gateway has no tunnel reference annotation"
		eval.Status.Conditions[0] = acceptedCondition(route, metav1.ConditionFalse, eval.Reason, eval.Message)
		return eval, nil
	}

	var foundListener bool
	var hostnameMismatch bool
	for _, listener := range gateway.Spec.Listeners {
		listenerEval := evaluateHTTPRouteForListener(ctx, reader, route, &gateway, ref, listener)
		if ref.SectionName == nil || listener.Name == *ref.SectionName {
			foundListener = true
		}
		if listenerEval.Reason == status.ReasonNoMatchingListenerHostname {
			hostnameMismatch = true
		}
		if listenerEval.Accepted {
			eval.AcceptedListeners = append(eval.AcceptedListeners, listener)
		}
	}

	if len(eval.AcceptedListeners) == 0 {
		eval.Reason, eval.Message = rejectedListenerReason(ref, foundListener, hostnameMismatch)
		eval.Status.Conditions[0] = acceptedCondition(route, metav1.ConditionFalse, eval.Reason, eval.Message)
		return eval, nil
	}

	if opts.validatePathMatches {
		if err := validateCloudflaredPathMatches(route); err != nil {
			eval.Reason = status.ReasonUnsupportedValue
			eval.Message = err.Error()
			eval.Status.Conditions[0] = acceptedCondition(route, metav1.ConditionFalse, eval.Reason, eval.Message)
			return eval, nil
		}
	}

	eval.Accepted = true
	return eval, nil
}

func evaluateHTTPRouteForListener(ctx context.Context, reader client.Reader, route *gatewayv1.HTTPRoute, gateway *gatewayv1.Gateway, ref gatewayv1.ParentReference, listener gatewayv1.Listener) httpRouteListenerEvaluation {
	if ref.SectionName != nil && listener.Name != *ref.SectionName {
		return httpRouteListenerEvaluation{Reason: status.ReasonNoMatchingListenerHostname, Message: fmt.Sprintf("Listener %s not found", *ref.SectionName)}
	}
	if listener.Protocol != gatewayv1.HTTPProtocolType && listener.Protocol != gatewayv1.HTTPSProtocolType {
		return httpRouteListenerEvaluation{Reason: status.ReasonNotAllowedByListeners, Message: "Route is not allowed by Gateway listeners"}
	}
	if !listenerAllowsHTTPRouteKind(listener) {
		return httpRouteListenerEvaluation{Reason: status.ReasonNotAllowedByListeners, Message: "Route is not allowed by Gateway listeners"}
	}
	if !listenerAllowsRouteNamespace(ctx, reader, route, gateway, listener) {
		return httpRouteListenerEvaluation{Reason: status.ReasonNotAllowedByListeners, Message: "Route is not allowed by Gateway listeners"}
	}
	if !listenerHostnameCompatible(route, listener) {
		if ref.SectionName != nil {
			return httpRouteListenerEvaluation{Reason: status.ReasonNoMatchingListenerHostname, Message: fmt.Sprintf("Route hostnames are not compatible with listener %s", *ref.SectionName)}
		}
		return httpRouteListenerEvaluation{Reason: status.ReasonNoMatchingListenerHostname, Message: "No matching listener hostname found"}
	}
	return httpRouteListenerEvaluation{Accepted: true, Reason: "Accepted", Message: "Route accepted by Gateway"}
}

func acceptedHTTPRouteHostnames(route *gatewayv1.HTTPRoute, listeners []gatewayv1.Listener) []gatewayv1.Hostname {
	routeHostnames := effectiveHTTPRouteHostnames(route)
	seen := map[string]struct{}{}
	var hostnames []gatewayv1.Hostname
	if len(routeHostnames) > 0 {
		for _, routeHostname := range routeHostnames {
			for _, listener := range listeners {
				if listener.Hostname == nil || *listener.Hostname == "" || hostnameMatches(string(routeHostname), string(*listener.Hostname)) {
					key := string(routeHostname)
					if _, ok := seen[key]; ok {
						continue
					}
					seen[key] = struct{}{}
					hostnames = append(hostnames, routeHostname)
					break
				}
			}
		}
		return hostnames
	}
	for _, listener := range listeners {
		if listener.Hostname == nil || *listener.Hostname == "" {
			continue
		}
		key := string(*listener.Hostname)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		hostnames = append(hostnames, *listener.Hostname)
	}
	return hostnames
}

func validateHTTPRouteBackendRefs(ctx context.Context, reader client.Reader, route *gatewayv1.HTTPRoute) metav1.Condition {
	for _, rule := range route.Spec.Rules {
		if len(rule.BackendRefs) > 1 {
			return status.NewCondition(
				string(gatewayv1.RouteConditionResolvedRefs),
				metav1.ConditionFalse,
				status.ReasonUnsupportedValue,
				"multiple backendRefs are not supported by cfgate tunnel ingress",
				route.Generation,
			)
		}

		for _, backend := range rule.BackendRefs {
			if backend.Group != nil && *backend.Group != "" && *backend.Group != "core" {
				return status.NewCondition(
					string(gatewayv1.RouteConditionResolvedRefs),
					metav1.ConditionFalse,
					status.ReasonUnsupportedValue,
					fmt.Sprintf("unsupported backend group %q: only core Service backends are supported by cfgate tunnel ingress", *backend.Group),
					route.Generation,
				)
			}
			if backend.Kind != nil && *backend.Kind != "" && *backend.Kind != "Service" {
				return status.NewCondition(
					string(gatewayv1.RouteConditionResolvedRefs),
					metav1.ConditionFalse,
					status.ReasonUnsupportedValue,
					fmt.Sprintf("unsupported backend kind %q: only Service backends are supported by cfgate tunnel ingress", *backend.Kind),
					route.Generation,
				)
			}

			namespace := route.Namespace
			if backend.Namespace != nil {
				namespace = string(*backend.Namespace)
			}
			if namespace != route.Namespace {
				permitted, err := referenceGrantPermits(ctx, reader, route.Namespace, namespace, gatewayv1.GroupName, "HTTPRoute", "", "Service", string(backend.Name))
				if err != nil {
					return status.NewCondition(
						string(gatewayv1.RouteConditionResolvedRefs),
						metav1.ConditionFalse,
						status.ReasonRefNotPermitted,
						fmt.Sprintf("Failed to check ReferenceGrant for Service %s/%s: %v", namespace, backend.Name, err),
						route.Generation,
					)
				}
				if !permitted {
					return status.NewCondition(
						string(gatewayv1.RouteConditionResolvedRefs),
						metav1.ConditionFalse,
						status.ReasonRefNotPermitted,
						fmt.Sprintf("Service %s/%s is not permitted by ReferenceGrant", namespace, backend.Name),
						route.Generation,
					)
				}
			}

			var svc corev1.Service
			if err := reader.Get(ctx, types.NamespacedName{Name: string(backend.Name), Namespace: namespace}, &svc); err != nil {
				if apierrors.IsNotFound(err) {
					return status.NewCondition(
						string(gatewayv1.RouteConditionResolvedRefs),
						metav1.ConditionFalse,
						status.ReasonBackendNotFound,
						fmt.Sprintf("Service %s/%s not found", namespace, backend.Name),
						route.Generation,
					)
				}
				return status.NewCondition(
					string(gatewayv1.RouteConditionResolvedRefs),
					metav1.ConditionFalse,
					status.ReasonBackendNotFound,
					fmt.Sprintf("Failed to get Service %s/%s: %v", namespace, backend.Name, err),
					route.Generation,
				)
			}
		}
	}

	return status.NewCondition(
		string(gatewayv1.RouteConditionResolvedRefs),
		metav1.ConditionTrue,
		"ResolvedRefs",
		"All backend references resolved",
		route.Generation,
	)
}

func newHTTPRouteParentStatus(route *gatewayv1.HTTPRoute, ref gatewayv1.ParentReference) gatewayv1.RouteParentStatus {
	parentNS := gatewayv1.Namespace(route.Namespace)
	if ref.Namespace != nil {
		parentNS = *ref.Namespace
	}
	return gatewayv1.RouteParentStatus{
		ParentRef: gatewayv1.ParentReference{
			Group:       ref.Group,
			Kind:        ref.Kind,
			Namespace:   &parentNS,
			Name:        ref.Name,
			SectionName: ref.SectionName,
		},
		ControllerName: GatewayControllerName,
		Conditions: []metav1.Condition{
			acceptedCondition(route, metav1.ConditionTrue, "Accepted", "Route accepted by Gateway"),
		},
	}
}

func acceptedCondition(route *gatewayv1.HTTPRoute, conditionStatus metav1.ConditionStatus, reason, message string) metav1.Condition {
	return status.NewCondition(
		string(gatewayv1.RouteConditionAccepted),
		conditionStatus,
		reason,
		message,
		route.Generation,
	)
}

func rejectedListenerReason(ref gatewayv1.ParentReference, foundListener, hostnameMismatch bool) (string, string) {
	if hostnameMismatch {
		if ref.SectionName != nil {
			return status.ReasonNoMatchingListenerHostname, fmt.Sprintf("Route hostnames are not compatible with listener %s", *ref.SectionName)
		}
		return status.ReasonNoMatchingListenerHostname, "No matching listener hostname found"
	}
	if !foundListener {
		if ref.SectionName != nil {
			return status.ReasonNoMatchingListenerHostname, fmt.Sprintf("Listener %s not found", *ref.SectionName)
		}
		return status.ReasonNoMatchingListenerHostname, "No compatible listener found"
	}
	return status.ReasonNotAllowedByListeners, "Route is not allowed by Gateway listeners"
}

func gatewayClassManagedByCfgate(ctx context.Context, reader client.Reader, gateway *gatewayv1.Gateway) (bool, error) {
	var gc gatewayv1.GatewayClass
	if err := reader.Get(ctx, types.NamespacedName{Name: string(gateway.Spec.GatewayClassName)}, &gc); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Errorf("GatewayClass %s not found", gateway.Spec.GatewayClassName)
		}
		return false, fmt.Errorf("failed to get GatewayClass %s: %w", gateway.Spec.GatewayClassName, err)
	}
	return string(gc.Spec.ControllerName) == GatewayControllerName, nil
}

func listenerAllowsRouteNamespace(ctx context.Context, reader client.Reader, route *gatewayv1.HTTPRoute, gateway *gatewayv1.Gateway, listener gatewayv1.Listener) bool {
	from := gatewayv1.NamespacesFromSame
	var selector *metav1.LabelSelector
	if listener.AllowedRoutes != nil && listener.AllowedRoutes.Namespaces != nil {
		if listener.AllowedRoutes.Namespaces.From != nil {
			from = *listener.AllowedRoutes.Namespaces.From
		}
		selector = listener.AllowedRoutes.Namespaces.Selector
	}

	switch from {
	case gatewayv1.NamespacesFromAll:
		return true
	case gatewayv1.NamespacesFromSelector:
		if selector == nil {
			return false
		}
		labelSelector, err := metav1.LabelSelectorAsSelector(selector)
		if err != nil {
			return false
		}
		var ns corev1.Namespace
		if err := reader.Get(ctx, types.NamespacedName{Name: route.Namespace}, &ns); err != nil {
			return false
		}
		return labelSelector.Matches(labels.Set(ns.Labels))
	default:
		return route.Namespace == gateway.Namespace
	}
}

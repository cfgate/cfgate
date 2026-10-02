package controller

import (
	"cfgate.io/cfgate/internal/controller/annotations"
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	"cfgate.io/cfgate/internal/controller/status"
)

func validateHTTPRouteBackendRefs(
	ctx context.Context,
	reader client.Reader,
	route *gwapiv1.HTTPRoute,
) (metav1.Condition, error) {
	for _, rule := range route.Spec.Rules {
		if len(rule.BackendRefs) > 1 {
			return status.NewCondition(
				string(gwapiv1.RouteConditionResolvedRefs),
				metav1.ConditionFalse,
				status.ReasonUnsupportedValue,
				"multiple backendRefs are not supported by cfgate tunnel ingress",
				route.Generation,
			), nil
		}

		for _, backend := range rule.BackendRefs {
			if backend.Group != nil && *backend.Group != "" {
				return status.NewCondition(
					string(gwapiv1.RouteConditionResolvedRefs),
					metav1.ConditionFalse,
					status.ReasonUnsupportedValue,
					fmt.Sprintf("unsupported backend group %q: only core Service backends are supported by cfgate tunnel ingress", *backend.Group),
					route.Generation,
				), nil
			}
			if backend.Kind != nil && *backend.Kind != "" && *backend.Kind != "Service" {
				return status.NewCondition(
					string(gwapiv1.RouteConditionResolvedRefs),
					metav1.ConditionFalse,
					status.ReasonUnsupportedValue,
					fmt.Sprintf("unsupported backend kind %q: only Service backends are supported by cfgate tunnel ingress", *backend.Kind),
					route.Generation,
				), nil
			}

			if backend.Weight != nil && *backend.Weight == 0 {
				continue
			}

			// Get the Service
			namespace := route.Namespace
			if backend.Namespace != nil {
				namespace = string(*backend.Namespace)
			}

			if namespace != route.Namespace {
				permitted, err := backendReferencePermitted(ctx, reader, route.Namespace, namespace, string(backend.Name))
				if err != nil {
					return status.NewCondition(
						string(gwapiv1.RouteConditionResolvedRefs),
						metav1.ConditionFalse,
						status.ReasonRefNotPermitted,
						fmt.Sprintf("Failed to check ReferenceGrant for Service %s/%s: %v", namespace, backend.Name, err),
						route.Generation,
					), err
				}
				if !permitted {
					return status.NewCondition(
						string(gwapiv1.RouteConditionResolvedRefs),
						metav1.ConditionFalse,
						status.ReasonRefNotPermitted,
						fmt.Sprintf("Service %s/%s is not permitted by ReferenceGrant", namespace, backend.Name),
						route.Generation,
					), nil
				}
			}

			var svc corev1.Service
			if err := reader.Get(ctx, types.NamespacedName{
				Name:      string(backend.Name),
				Namespace: namespace,
			}, &svc); err != nil {
				if apierrors.IsNotFound(err) {
					return status.NewCondition(
						string(gwapiv1.RouteConditionResolvedRefs),
						metav1.ConditionFalse,
						status.ReasonBackendNotFound,
						fmt.Sprintf("Service %s/%s not found", namespace, backend.Name),
						route.Generation,
					), nil
				}
				return status.NewCondition(
					string(gwapiv1.RouteConditionResolvedRefs),
					metav1.ConditionFalse,
					status.ReasonBackendNotFound,
					fmt.Sprintf("Failed to get Service %s/%s: %v", namespace, backend.Name, err),
					route.Generation,
				), err
			}
			portNumber := int32(80)
			if backend.Port != nil {
				portNumber = int32(*backend.Port)
			}
			{
				found := false
				for _, port := range svc.Spec.Ports {
					if port.Port == portNumber {
						found = true
						break
					}
				}
				if !found {
					return status.NewCondition(string(gwapiv1.RouteConditionResolvedRefs), metav1.ConditionFalse, status.ReasonBackendNotFound,
						fmt.Sprintf("Service %s/%s has no port %d", namespace, backend.Name, portNumber), route.Generation), nil
				}
			}
		}
	}

	return status.NewCondition(
		string(gwapiv1.RouteConditionResolvedRefs),
		metav1.ConditionTrue,
		"ResolvedRefs",
		"All backend references resolved",
		route.Generation,
	), nil
}

// acceptedRouteHostnames intersects route hostnames with the current eligible
// listeners. Status may lag configuration or ReferenceGrant changes.
func acceptedRouteHostnames(ctx context.Context, reader client.Reader, route *gwapiv1.HTTPRoute, gateway *gwapiv1.Gateway, ref gwapiv1.ParentReference) ([]gwapiv1.Hostname, error) {
	var hostnames []gwapiv1.Hostname
	seen := map[gwapiv1.Hostname]bool{}
	for _, listener := range gateway.Spec.Listeners {
		if (ref.SectionName != nil && listener.Name != *ref.SectionName) || (ref.Port != nil && listener.Port != *ref.Port) {
			continue
		}
		if listener.Protocol != gwapiv1.HTTPProtocolType && listener.Protocol != gwapiv1.HTTPSProtocolType {
			continue
		}
		if !listenerAllowsHTTPRouteKind(listener) {
			continue
		}
		allowed, err := listenerAllowsRouteNamespace(ctx, reader, route, gateway, listener)
		if err != nil {
			return nil, err
		}
		if !allowed {
			continue
		}
		routeHostnames := effectiveHTTPRouteHostnames(route)
		if len(routeHostnames) == 0 && listener.Hostname != nil {
			routeHostnames = []gwapiv1.Hostname{*listener.Hostname}
		}
		for _, hostname := range routeHostnames {
			if listener.Hostname != nil && *listener.Hostname != "" {
				if !hostnameMatches(string(hostname), string(*listener.Hostname)) {
					continue
				}
				// A wildcard route must not expand a more specific listener.
				if strings.HasPrefix(string(hostname), "*.") && (!strings.HasPrefix(string(*listener.Hostname), "*.") || len(*listener.Hostname) > len(hostname)) {
					hostname = *listener.Hostname
				}
			}
			if hostname != "" && !seen[hostname] {
				seen[hostname] = true
				hostnames = append(hostnames, hostname)
			}
		}
	}
	return hostnames, nil
}

func backendReferencePermitted(ctx context.Context, reader client.Reader, fromNamespace, toNamespace, serviceName string) (bool, error) {
	var grants gwapiv1b1.ReferenceGrantList
	if err := reader.List(ctx, &grants, client.InNamespace(toNamespace)); err != nil {
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

func listenerAllowsRouteNamespace(
	ctx context.Context,
	reader client.Reader,
	route *gwapiv1.HTTPRoute,
	gateway *gwapiv1.Gateway,
	listener gwapiv1.Listener,
) (bool, error) {
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
		return true, nil
	case gwapiv1.NamespacesFromSelector:
		if selector == nil {
			return false, nil
		}
		labelSelector, err := metav1.LabelSelectorAsSelector(selector)
		if err != nil {
			return false, nil
		}
		var ns corev1.Namespace
		if err := reader.Get(ctx, types.NamespacedName{Name: route.Namespace}, &ns); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return labelSelector.Matches(labels.Set(ns.Labels)), nil
	default:
		return route.Namespace == gateway.Namespace, nil
	}
}

// validateHTTPRouteFeatures defines the subset the tunnel renderer can preserve.
// Status and rendering both reject restrictions that would otherwise be dropped.
func validateHTTPRouteFeatures(route *gwapiv1.HTTPRoute) error {
	for i, rule := range route.Spec.Rules {
		if len(rule.Filters) > 0 || rule.Timeouts != nil || rule.Retry != nil || rule.SessionPersistence != nil {
			return fmt.Errorf("rule %d: filters, timeouts, retries and session persistence are not supported by cfgate tunnel ingress", i)
		}
		for _, backend := range rule.BackendRefs {
			if len(backend.Filters) > 0 {
				return fmt.Errorf("rule %d: backend filters are not supported by cfgate tunnel ingress", i)
			}
		}
		for _, match := range rule.Matches {
			if match.Method != nil || len(match.Headers) > 0 || len(match.QueryParams) > 0 {
				return fmt.Errorf("rule %d: method, header and query matches are not supported by cfgate tunnel ingress", i)
			}
		}
	}
	return validateCloudflaredPathMatches(route)
}

// managedGatewayTunnel resolves only controller-owned, authorized Gateway edges.
func managedGatewayTunnel(ctx context.Context, reader client.Reader, gw *gwapiv1.Gateway, classes map[gwapiv1.ObjectName]bool) (types.NamespacedName, bool, error) {
	ns, name, err := annotations.ParseNamespacedName(annotations.GetAnnotation(gw, annotations.AnnotationTunnelRef), gw.Namespace)
	if err != nil {
		return types.NamespacedName{}, false, nil
	}
	key := types.NamespacedName{Namespace: ns, Name: name}
	managed, checked := classes[gw.Spec.GatewayClassName]
	if !checked {
		var class gwapiv1.GatewayClass
		if err := reader.Get(ctx, types.NamespacedName{Name: string(gw.Spec.GatewayClassName)}, &class); err != nil {
			if !apierrors.IsNotFound(err) {
				return key, false, err
			}
		} else {
			managed = string(class.Spec.ControllerName) == GatewayControllerName
		}
		classes[gw.Spec.GatewayClassName] = managed
	}
	if !managed {
		return key, false, nil
	}
	if err := requireReferenceGrant(ctx, reader, gw.Namespace, gwapiv1.GroupName, "Gateway", ns, "cfgate.io", "CloudflareTunnel", name); err != nil {
		if errors.Is(err, errReferenceNotPermitted) {
			return key, false, nil
		}
		return key, false, err
	}
	return key, true, nil
}

package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/controller/annotations"
	"cfgate.io/cfgate/internal/controller/status"
)

func TestHTTPRouteHelperFinders(t *testing.T) {
	scheme := controllerTestScheme(t)
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "app"},
	}
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "app"},
	}
	policy := &cfgatev1alpha1.CloudflareAccessPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "app"},
	}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route",
			Namespace: "app",
			Annotations: map[string]string{
				annotations.AnnotationAccessPolicy: "app/policy",
			},
		},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name: "gateway",
				}},
			},
			Rules: []gatewayv1.HTTPRouteRule{{
				BackendRefs: []gatewayv1.HTTPBackendRef{{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "svc",
						},
					},
				}},
			}},
		},
	}

	r := newHTTPRouteTestReconciler(t, scheme, gateway, service, policy, route)
	ctx := context.Background()

	reqs := r.findRoutesForGateway(ctx, gateway)
	assertRequests(t, reqs, types.NamespacedName{Namespace: "app", Name: "route"})

	reqs = r.findRoutesForService(ctx, service)
	assertRequests(t, reqs, types.NamespacedName{Namespace: "app", Name: "route"})

	reqs = r.findRoutesForAccessPolicy(ctx, policy)
	assertRequests(t, reqs, types.NamespacedName{Namespace: "app", Name: "route"})
}

func TestIsCfgateParentRef(t *testing.T) {
	scheme := controllerTestScheme(t)
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "app"},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "cfgate",
		},
	}
	gatewayClass := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "cfgate"},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: gatewayv1.GatewayController(GatewayControllerName),
		},
	}
	route := &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"}}

	r := newHTTPRouteTestReconciler(t, scheme, gateway, gatewayClass)
	isCfgate, err := r.isCfgateParentRef(context.Background(), route, gatewayv1.ParentReference{Name: "gateway"})
	if err != nil {
		t.Fatalf("isCfgateParentRef() error = %v", err)
	}
	if !isCfgate {
		t.Fatal("isCfgateParentRef() = false, want true")
	}

	otherClass := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "other"},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: "example.com/other",
		},
	}
	gateway.Spec.GatewayClassName = "other"
	r = newHTTPRouteTestReconciler(t, scheme, gateway, otherClass)
	isCfgate, err = r.isCfgateParentRef(context.Background(), route, gatewayv1.ParentReference{Name: "gateway"})
	if err != nil {
		t.Fatalf("isCfgateParentRef() other error = %v", err)
	}
	if isCfgate {
		t.Fatal("isCfgateParentRef() = true, want false")
	}
}

func TestValidateParentRef(t *testing.T) {
	scheme := controllerTestScheme(t)
	listenerName := gatewayv1.SectionName("http")
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gateway",
			Namespace: "app",
			Annotations: map[string]string{
				annotations.AnnotationTunnelRef: "cfgate-system/tunnel",
			},
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "cfgate",
			Listeners: []gatewayv1.Listener{{
				Name:     listenerName,
				Protocol: gatewayv1.HTTPProtocolType,
			}},
		},
	}
	gatewayClass := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "cfgate"},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: gatewayv1.GatewayController(GatewayControllerName),
		},
	}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"},
	}
	r := newHTTPRouteTestReconciler(t, scheme, gateway, gatewayClass)

	statusResult := r.validateParentRef(context.Background(), route, gatewayv1.ParentReference{
		Name:        "gateway",
		SectionName: &listenerName,
	})
	if len(statusResult.Conditions) != 1 || statusResult.Conditions[0].Status != metav1.ConditionTrue {
		t.Fatalf("validateParentRef() = %+v, want accepted condition", statusResult.Conditions)
	}

	gateway = gateway.DeepCopy()
	delete(gateway.Annotations, annotations.AnnotationTunnelRef)
	r = newHTTPRouteTestReconciler(t, scheme, gateway, gatewayClass)
	statusResult = r.validateParentRef(context.Background(), route, gatewayv1.ParentReference{Name: "gateway"})
	if statusResult.Conditions[0].Reason != status.ReasonNoTunnelRef {
		t.Fatalf("validateParentRef() reason = %q, want %q", statusResult.Conditions[0].Reason, status.ReasonNoTunnelRef)
	}

	same := gatewayv1.NamespacesFromSame
	gateway = gateway.DeepCopy()
	gateway.Annotations = map[string]string{annotations.AnnotationTunnelRef: "cfgate-system/tunnel"}
	gateway.Spec.Listeners[0].AllowedRoutes = &gatewayv1.AllowedRoutes{
		Namespaces: &gatewayv1.RouteNamespaces{From: &same},
	}
	route.Namespace = "other"
	gatewayNS := gatewayv1.Namespace("app")
	r = newHTTPRouteTestReconciler(t, scheme, gateway, gatewayClass)
	statusResult = r.validateParentRef(context.Background(), route, gatewayv1.ParentReference{
		Namespace:   &gatewayNS,
		Name:        "gateway",
		SectionName: &listenerName,
	})
	if statusResult.Conditions[0].Reason != status.ReasonNotAllowedByListeners {
		t.Fatalf("validateParentRef() reason = %q, want %q", statusResult.Conditions[0].Reason, status.ReasonNotAllowedByListeners)
	}

	selectorFrom := gatewayv1.NamespacesFromSelector
	gateway = gateway.DeepCopy()
	gateway.Annotations = map[string]string{annotations.AnnotationTunnelRef: "cfgate-system/tunnel"}
	gateway.Spec.Listeners[0].AllowedRoutes = &gatewayv1.AllowedRoutes{
		Namespaces: &gatewayv1.RouteNamespaces{
			From: &selectorFrom,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"team": "payments"},
			},
		},
	}
	route.Namespace = "payments"
	routeNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "payments",
			Labels: map[string]string{"team": "payments"},
		},
	}
	r = newHTTPRouteTestReconciler(t, scheme, gateway, gatewayClass, routeNS)
	statusResult = r.validateParentRef(context.Background(), route, gatewayv1.ParentReference{
		Namespace:   &gatewayNS,
		Name:        "gateway",
		SectionName: &listenerName,
	})
	if statusResult.Conditions[0].Status != metav1.ConditionTrue {
		t.Fatalf("validateParentRef() selector match = %+v, want accepted condition", statusResult.Conditions)
	}

	routeNS.Labels = map[string]string{"team": "platform"}
	r = newHTTPRouteTestReconciler(t, scheme, gateway, gatewayClass, routeNS)
	statusResult = r.validateParentRef(context.Background(), route, gatewayv1.ParentReference{
		Namespace:   &gatewayNS,
		Name:        "gateway",
		SectionName: &listenerName,
	})
	if statusResult.Conditions[0].Reason != status.ReasonNotAllowedByListeners {
		t.Fatalf("validateParentRef() selector miss reason = %q, want %q", statusResult.Conditions[0].Reason, status.ReasonNotAllowedByListeners)
	}
}

func TestValidateParentRefStatusReasons(t *testing.T) {
	scheme := controllerTestScheme(t)
	listenerName := gatewayv1.SectionName("https")
	listenerHostname := gatewayv1.Hostname("api.example.com")
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gateway",
			Namespace: "app",
			Annotations: map[string]string{
				annotations.AnnotationTunnelRef: "cfgate-system/tunnel",
			},
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "cfgate",
			Listeners: []gatewayv1.Listener{{
				Name:     listenerName,
				Protocol: gatewayv1.HTTPSProtocolType,
				Hostname: &listenerHostname,
			}},
		},
	}
	gatewayClass := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "cfgate"},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: gatewayv1.GatewayController(GatewayControllerName),
		},
	}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"},
		Spec: gatewayv1.HTTPRouteSpec{
			Hostnames: []gatewayv1.Hostname{"app.example.com"},
			Rules: []gatewayv1.HTTPRouteRule{{
				BackendRefs: []gatewayv1.HTTPBackendRef{{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "svc"},
					},
				}},
			}},
		},
	}

	r := newHTTPRouteTestReconciler(t, scheme, gateway, gatewayClass)
	statusResult := r.validateParentRef(context.Background(), route, gatewayv1.ParentReference{
		Name:        "gateway",
		SectionName: &listenerName,
	})
	condition := statusResult.Conditions[0]
	if condition.Type != string(gatewayv1.RouteConditionAccepted) ||
		condition.Status != metav1.ConditionFalse ||
		condition.Reason != status.ReasonNoMatchingListenerHostname ||
		!strings.Contains(condition.Message, "hostname") {
		t.Fatalf("validateParentRef() hostname mismatch condition = %+v, want NoMatchingListenerHostname", condition)
	}

	annotatedRoute := route.DeepCopy()
	annotatedRoute.Spec.Hostnames = nil
	annotatedRoute.Annotations = map[string]string{annotations.AnnotationHostname: "app.example.com"}
	statusResult = r.validateParentRef(context.Background(), annotatedRoute, gatewayv1.ParentReference{
		Name:        "gateway",
		SectionName: &listenerName,
	})
	condition = statusResult.Conditions[0]
	if condition.Status != metav1.ConditionFalse ||
		condition.Reason != status.ReasonNoMatchingListenerHostname {
		t.Fatalf("validateParentRef() annotation hostname mismatch = %+v, want NoMatchingListenerHostname", condition)
	}

	annotatedRoute.Annotations = map[string]string{annotations.AnnotationHostname: string(listenerHostname)}
	statusResult = r.validateParentRef(context.Background(), annotatedRoute, gatewayv1.ParentReference{
		Name:        "gateway",
		SectionName: &listenerName,
	})
	condition = statusResult.Conditions[0]
	if condition.Status != metav1.ConditionTrue {
		t.Fatalf("validateParentRef() annotation hostname match = %+v, want Accepted=True", condition)
	}

	otherNamespace := gatewayv1.Namespace("app")
	for _, tt := range []struct {
		name   string
		mutate func(*gatewayv1.Gateway, *gatewayv1.HTTPRoute)
	}{
		{
			name: "unsupported protocol",
			mutate: func(testGateway *gatewayv1.Gateway, _ *gatewayv1.HTTPRoute) {
				testGateway.Spec.Listeners[0].Protocol = gatewayv1.ProtocolType("TCP")
				testGateway.Spec.Listeners[0].Hostname = nil
			},
		},
		{
			name: "unsupported route kind",
			mutate: func(testGateway *gatewayv1.Gateway, _ *gatewayv1.HTTPRoute) {
				testGateway.Spec.Listeners[0].Hostname = nil
				testGateway.Spec.Listeners[0].AllowedRoutes = &gatewayv1.AllowedRoutes{
					Kinds: []gatewayv1.RouteGroupKind{{Kind: gatewayv1.Kind("TLSRoute")}},
				}
			},
		},
		{
			name: "namespace not allowed",
			mutate: func(testGateway *gatewayv1.Gateway, testRoute *gatewayv1.HTTPRoute) {
				testGateway.Spec.Listeners[0].Hostname = nil
				same := gatewayv1.NamespacesFromSame
				testGateway.Spec.Listeners[0].AllowedRoutes = &gatewayv1.AllowedRoutes{
					Namespaces: &gatewayv1.RouteNamespaces{From: &same},
				}
				testRoute.Namespace = "other"
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			testGateway := gateway.DeepCopy()
			testRoute := route.DeepCopy()
			tt.mutate(testGateway, testRoute)
			r := newHTTPRouteTestReconciler(t, scheme, testGateway, gatewayClass)
			statusResult := r.validateParentRef(context.Background(), testRoute, gatewayv1.ParentReference{
				Namespace:   &otherNamespace,
				Name:        "gateway",
				SectionName: &listenerName,
			})
			if statusResult.Conditions[0].Reason != status.ReasonNotAllowedByListeners {
				t.Fatalf("validateParentRef() reason = %q, want %q", statusResult.Conditions[0].Reason, status.ReasonNotAllowedByListeners)
			}
		})
	}
}

func TestAcceptedHTTPRouteHostnamesFiltersByAcceptedListeners(t *testing.T) {
	route := &gatewayv1.HTTPRoute{
		Spec: gatewayv1.HTTPRouteSpec{
			Hostnames: []gatewayv1.Hostname{"app.example.com", "other.example.com"},
		},
	}
	listenerHostname := gatewayv1.Hostname("app.example.com")
	got := acceptedHTTPRouteHostnames(route, []gatewayv1.Listener{{
		Name:     "http",
		Protocol: gatewayv1.HTTPProtocolType,
		Hostname: &listenerHostname,
	}})
	if len(got) != 1 || got[0] != "app.example.com" {
		t.Fatalf("acceptedHTTPRouteHostnames() = %#v, want app.example.com only", got)
	}

	route.Spec.Hostnames = nil
	got = acceptedHTTPRouteHostnames(route, []gatewayv1.Listener{{
		Name:     "http",
		Protocol: gatewayv1.HTTPProtocolType,
		Hostname: &listenerHostname,
	}})
	if len(got) != 1 || got[0] != listenerHostname {
		t.Fatalf("acceptedHTTPRouteHostnames() listener fallback = %#v, want %s", got, listenerHostname)
	}
}

func TestValidateParentRefPathSupport(t *testing.T) {
	scheme := controllerTestScheme(t)
	listenerName := gatewayv1.SectionName("https")
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gateway",
			Namespace: "app",
			Annotations: map[string]string{
				annotations.AnnotationTunnelRef: "cfgate-system/tunnel",
			},
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "cfgate",
			Listeners: []gatewayv1.Listener{{
				Name:     listenerName,
				Protocol: gatewayv1.HTTPSProtocolType,
			}},
		},
	}
	gatewayClass := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "cfgate"},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: gatewayv1.GatewayController(GatewayControllerName),
		},
	}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "app"}}
	regexType := gatewayv1.PathMatchRegularExpression
	invalidRegex := "["
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{{
				Matches: []gatewayv1.HTTPRouteMatch{{
					Path: &gatewayv1.HTTPPathMatch{Type: &regexType, Value: &invalidRegex},
				}},
				BackendRefs: []gatewayv1.HTTPBackendRef{{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "svc"},
					},
				}},
			}},
		},
	}

	r := newHTTPRouteTestReconciler(t, scheme, gateway, gatewayClass, service)
	statusResult := r.validateParentRef(context.Background(), route, gatewayv1.ParentReference{
		Name:        "gateway",
		SectionName: &listenerName,
	})
	condition := statusResult.Conditions[0]
	if condition.Status != metav1.ConditionFalse ||
		condition.Reason != status.ReasonUnsupportedValue ||
		!strings.Contains(condition.Message, "unsupported path regular expression") {
		t.Fatalf("validateParentRef() invalid regex condition = %+v, want Accepted UnsupportedValue", condition)
	}
	resolvedRefs := r.resolveBackends(context.Background(), route)
	if resolvedRefs.Type != string(gatewayv1.RouteConditionResolvedRefs) || resolvedRefs.Status != metav1.ConditionTrue {
		t.Fatalf("resolveBackends() = %+v, want ResolvedRefs=True", resolvedRefs)
	}

	unsupportedType := gatewayv1.PathMatchType("ImplementationSpecific")
	unsupportedPath := "/unsupported"
	unsupportedRoute := route.DeepCopy()
	unsupportedRoute.Spec.Rules[0].Matches[0].Path.Type = &unsupportedType
	unsupportedRoute.Spec.Rules[0].Matches[0].Path.Value = &unsupportedPath
	statusResult = r.validateParentRef(context.Background(), unsupportedRoute, gatewayv1.ParentReference{
		Name:        "gateway",
		SectionName: &listenerName,
	})
	condition = statusResult.Conditions[0]
	if condition.Status != metav1.ConditionFalse ||
		condition.Reason != status.ReasonUnsupportedValue ||
		!strings.Contains(condition.Message, "unsupported path match type") {
		t.Fatalf("validateParentRef() unsupported path type condition = %+v, want Accepted UnsupportedValue", condition)
	}
}

func TestHostnameMatches(t *testing.T) {
	tests := []struct {
		name             string
		routeHostname    string
		listenerHostname string
		want             bool
	}{
		{name: "exact same hostname", routeHostname: "app.example.com", listenerHostname: "app.example.com", want: true},
		{name: "case insensitive exact match", routeHostname: "APP.EXAMPLE.COM", listenerHostname: "app.example.com", want: true},
		{name: "listener wildcard one label", routeHostname: "foo.example.com", listenerHostname: "*.example.com", want: true},
		{name: "listener wildcard deep subdomain", routeHostname: "a.b.example.com", listenerHostname: "*.example.com"},
		{name: "listener wildcard apex", routeHostname: "example.com", listenerHostname: "*.example.com"},
		{name: "route wildcard exact listener one label", routeHostname: "*.example.com", listenerHostname: "foo.example.com", want: true},
		{name: "route wildcard deep listener", routeHostname: "*.example.com", listenerHostname: "a.b.example.com"},
		{name: "same wildcard hostname", routeHostname: "*.example.com", listenerHostname: "*.example.com", want: true},
		{name: "unrelated hostname", routeHostname: "foo.example.com", listenerHostname: "bar.example.net"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hostnameMatches(tt.routeHostname, tt.listenerHostname)
			if got != tt.want {
				t.Fatalf("hostnameMatches(%q, %q) = %v, want %v", tt.routeHostname, tt.listenerHostname, got, tt.want)
			}
		})
	}
}

func TestResolveBackends(t *testing.T) {
	scheme := controllerTestScheme(t)
	if err := gatewayv1b1.Install(scheme); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "app"},
	}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{{
				BackendRefs: []gatewayv1.HTTPBackendRef{{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "svc"},
					},
				}},
			}},
		},
	}

	r := newHTTPRouteTestReconciler(t, scheme, service)
	condition := r.resolveBackends(context.Background(), route)
	if condition.Status != metav1.ConditionTrue {
		t.Fatalf("resolveBackends() status = %q, want %q", condition.Status, metav1.ConditionTrue)
	}

	unsupportedTests := []struct {
		name        string
		mutate      func(*gatewayv1.HTTPRoute)
		wantMessage string
	}{
		{
			name: "multiple backendRefs",
			mutate: func(testRoute *gatewayv1.HTTPRoute) {
				testRoute.Spec.Rules[0].BackendRefs = append(testRoute.Spec.Rules[0].BackendRefs, gatewayv1.HTTPBackendRef{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "other"},
					},
				})
			},
			wantMessage: "multiple backendRefs are not supported by cfgate tunnel ingress",
		},
		{
			name: "unsupported backend group",
			mutate: func(testRoute *gatewayv1.HTTPRoute) {
				group := gatewayv1.Group("example.com")
				testRoute.Spec.Rules[0].BackendRefs[0].Group = &group
			},
			wantMessage: "unsupported backend group",
		},
		{
			name: "unsupported backend kind",
			mutate: func(testRoute *gatewayv1.HTTPRoute) {
				kind := gatewayv1.Kind("ConfigMap")
				testRoute.Spec.Rules[0].BackendRefs[0].Kind = &kind
			},
			wantMessage: "unsupported backend kind",
		},
	}
	for _, tt := range unsupportedTests {
		t.Run(tt.name, func(t *testing.T) {
			testRoute := route.DeepCopy()
			tt.mutate(testRoute)
			condition := r.resolveBackends(context.Background(), testRoute)
			if condition.Status != metav1.ConditionFalse || condition.Reason != status.ReasonUnsupportedValue {
				t.Fatalf("resolveBackends() = %+v, want UnsupportedValue", condition)
			}
			if !strings.Contains(condition.Message, tt.wantMessage) {
				t.Fatalf("resolveBackends() message = %q, want substring %q", condition.Message, tt.wantMessage)
			}
		})
	}

	route.Spec.Rules[0].BackendRefs[0].Name = "missing"
	condition = r.resolveBackends(context.Background(), route)
	if condition.Status != metav1.ConditionFalse || condition.Reason != status.ReasonBackendNotFound {
		t.Fatalf("resolveBackends() = %+v, want missing backend condition", condition)
	}

	backendNS := gatewayv1.Namespace("backend")
	route.Namespace = "app"
	route.Spec.Rules[0].BackendRefs[0].Name = "svc"
	route.Spec.Rules[0].BackendRefs[0].Namespace = &backendNS
	crossNSService := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "backend"}}
	r = newHTTPRouteTestReconciler(t, scheme, crossNSService)
	condition = r.resolveBackends(context.Background(), route)
	if condition.Status != metav1.ConditionFalse || condition.Reason != status.ReasonRefNotPermitted {
		t.Fatalf("resolveBackends() = %+v, want RefNotPermitted", condition)
	}

	serviceName := gatewayv1.ObjectName("svc")
	grant := &gatewayv1b1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-svc", Namespace: "backend"},
		Spec: gatewayv1b1.ReferenceGrantSpec{
			From: []gatewayv1b1.ReferenceGrantFrom{{
				Group:     gatewayv1.Group(gatewayv1.GroupName),
				Kind:      gatewayv1.Kind("HTTPRoute"),
				Namespace: gatewayv1.Namespace("app"),
			}},
			To: []gatewayv1b1.ReferenceGrantTo{{
				Group: gatewayv1.Group(""),
				Kind:  gatewayv1.Kind("Service"),
				Name:  &serviceName,
			}},
		},
	}
	r = newHTTPRouteTestReconciler(t, scheme, crossNSService, grant)
	condition = r.resolveBackends(context.Background(), route)
	if condition.Status != metav1.ConditionTrue {
		t.Fatalf("resolveBackends() = %+v, want resolved refs", condition)
	}
}

func TestResolveAccessPolicy(t *testing.T) {
	scheme := controllerTestScheme(t)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"},
	}
	recorder := &fakeEventRecorder{}
	r := newHTTPRouteTestReconcilerWithRecorder(t, scheme, recorder)

	condition, ok := r.resolveAccessPolicy(context.Background(), route)
	if ok {
		t.Fatalf("resolveAccessPolicy() ok = true, want false; condition = %+v", condition)
	}

	route.Annotations = map[string]string{annotations.AnnotationAccessPolicy: "bad/ref/format"}
	condition, ok = r.resolveAccessPolicy(context.Background(), route)
	if !ok || condition.Reason != status.ReasonInvalidPolicyRef {
		t.Fatalf("resolveAccessPolicy() = (%+v, %v), want invalid ref condition", condition, ok)
	}

	policy := &cfgatev1alpha1.CloudflareAccessPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "app"},
	}
	route.Annotations = map[string]string{annotations.AnnotationAccessPolicy: "policy"}
	r = newHTTPRouteTestReconcilerWithRecorder(t, scheme, recorder, policy)
	condition, ok = r.resolveAccessPolicy(context.Background(), route)
	if !ok || condition.Status != metav1.ConditionTrue || condition.Reason != status.ReasonResolved {
		t.Fatalf("resolveAccessPolicy() = (%+v, %v), want resolved condition", condition, ok)
	}
}

func TestValidatePathTypes(t *testing.T) {
	scheme := controllerTestScheme(t)
	recorder := &fakeEventRecorder{}
	r := newHTTPRouteTestReconcilerWithRecorder(t, scheme, recorder)

	exactType := gatewayv1.PathMatchExact
	regexType := gatewayv1.PathMatchRegularExpression
	exactPath := "/exact"
	regexPath := "^/re"
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{{
				Matches: []gatewayv1.HTTPRouteMatch{
					{Path: &gatewayv1.HTTPPathMatch{Type: &exactType, Value: &exactPath}},
					{Path: &gatewayv1.HTTPPathMatch{Type: &regexType, Value: &regexPath}},
				},
			}},
		},
	}

	r.validatePathTypes(route)

	assertEventContains(t, recorder, "PathTypeNotice")
}

func TestParsePolicyRef(t *testing.T) {
	ns, name, err := parsePolicyRef("policy", "default")
	if err != nil {
		t.Fatalf("parsePolicyRef() error = %v", err)
	}
	if ns != "default" || name != "policy" {
		t.Fatalf("parsePolicyRef() = (%q, %q), want (%q, %q)", ns, name, "default", "policy")
	}

	_, _, err = parsePolicyRef("too/many/parts", "default")
	if err == nil {
		t.Fatal("parsePolicyRef() error = nil, want invalid ref error")
	}
}

func TestHTTPRouteReconcileSkipsEmptyParentStatusWrite(t *testing.T) {
	scheme := controllerTestScheme(t)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route",
			Namespace: "app",
		},
	}

	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(route).Build()
	statusWriter := &countingStatusWriter{}
	r := &HTTPRouteReconciler{
		Client:   &statusTrackingClient{Client: baseClient, writer: statusWriter},
		Scheme:   scheme,
		Recorder: &fakeEventRecorder{},
	}

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "app", Name: "route"},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if statusWriter.updates != 0 {
		t.Fatalf("Status().Update() called %d times, want 0", statusWriter.updates)
	}
	if result.RequeueAfter != 5*time.Minute {
		t.Fatalf("Reconcile() RequeueAfter = %v, want %v", result.RequeueAfter, 5*time.Minute)
	}
}

func TestHTTPRouteReconcileClearsStaleCfgateParentStatus(t *testing.T) {
	scheme := controllerTestScheme(t)
	controllerName := gatewayv1.GatewayController(GatewayControllerName)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route",
			Namespace: "app",
		},
		Status: gatewayv1.HTTPRouteStatus{
			RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ControllerName: controllerName,
				}},
			},
		},
	}

	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(route).Build()
	statusWriter := &countingStatusWriter{}
	r := &HTTPRouteReconciler{
		Client:   &statusTrackingClient{Client: baseClient, writer: statusWriter},
		Scheme:   scheme,
		Recorder: &fakeEventRecorder{},
	}

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "app", Name: "route"},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if statusWriter.updates != 1 {
		t.Fatalf("Status().Update() called %d times, want 1", statusWriter.updates)
	}
	if statusWriter.lastRoute == nil {
		t.Fatal("Status().Update() did not receive an HTTPRoute")
	}
	if statusWriter.lastRoute.Status.Parents == nil {
		t.Fatal("Status().Update() wrote nil parents, want empty slice")
	}
	if len(statusWriter.lastRoute.Status.Parents) != 0 {
		t.Fatalf("Status().Update() parents = %#v, want empty slice", statusWriter.lastRoute.Status.Parents)
	}
	if result.RequeueAfter != 5*time.Minute {
		t.Fatalf("Reconcile() RequeueAfter = %v, want %v", result.RequeueAfter, 5*time.Minute)
	}
}

func TestHTTPRouteReconcilePreservesForeignParentStatusWhileClearingStaleCfgateEntries(t *testing.T) {
	scheme := controllerTestScheme(t)
	cfgateController := gatewayv1.GatewayController(GatewayControllerName)
	foreignController := gatewayv1.GatewayController("example.com/other")
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route",
			Namespace: "app",
		},
		Status: gatewayv1.HTTPRouteStatus{
			RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{
					{ControllerName: cfgateController},
					{ControllerName: foreignController},
				},
			},
		},
	}

	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(route).Build()
	statusWriter := &countingStatusWriter{}
	r := &HTTPRouteReconciler{
		Client:   &statusTrackingClient{Client: baseClient, writer: statusWriter},
		Scheme:   scheme,
		Recorder: &fakeEventRecorder{},
	}

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "app", Name: "route"},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if statusWriter.updates != 1 {
		t.Fatalf("Status().Update() called %d times, want 1", statusWriter.updates)
	}
	if statusWriter.lastRoute == nil {
		t.Fatal("Status().Update() did not receive an HTTPRoute")
	}
	if len(statusWriter.lastRoute.Status.Parents) != 1 {
		t.Fatalf("Status().Update() parents = %#v, want 1 preserved entry", statusWriter.lastRoute.Status.Parents)
	}
	if statusWriter.lastRoute.Status.Parents[0].ControllerName != foreignController {
		t.Fatalf("Status().Update() preserved controller = %q, want %q", statusWriter.lastRoute.Status.Parents[0].ControllerName, foreignController)
	}
	if result.RequeueAfter != 5*time.Minute {
		t.Fatalf("Reconcile() RequeueAfter = %v, want %v", result.RequeueAfter, 5*time.Minute)
	}
}

func newHTTPRouteTestReconciler(t *testing.T, scheme *runtime.Scheme, objects ...client.Object) *HTTPRouteReconciler {
	t.Helper()
	return newHTTPRouteTestReconcilerWithRecorder(t, scheme, &fakeEventRecorder{}, objects...)
}

func newHTTPRouteTestReconcilerWithRecorder(t *testing.T, scheme *runtime.Scheme, recorder *fakeEventRecorder, objects ...client.Object) *HTTPRouteReconciler {
	t.Helper()
	return &HTTPRouteReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(),
		Scheme:   scheme,
		Recorder: recorder,
	}
}

func controllerTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(corev1) error = %v", err)
	}
	if err := cfgatev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(cfgate) error = %v", err)
	}
	if err := gatewayv1.Install(scheme); err != nil {
		t.Fatalf("Install(gateway/v1) error = %v", err)
	}
	return scheme
}

func assertRequests(t *testing.T, reqs []reconcile.Request, want types.NamespacedName) {
	t.Helper()
	if len(reqs) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(reqs))
	}
	if reqs[0].NamespacedName != want {
		t.Fatalf("request = %v, want %v", reqs[0].NamespacedName, want)
	}
}

func assertEventContains(t *testing.T, recorder *fakeEventRecorder, want string) {
	t.Helper()
	for _, event := range recorder.events {
		if strings.Contains(event, want) {
			return
		}
	}
	t.Fatalf("did not receive event containing %q", want)
}

type fakeEventRecorder struct {
	events []string
}

func (r *fakeEventRecorder) Eventf(regarding runtime.Object, related runtime.Object, eventtype, reason, action, note string, args ...interface{}) {
	_ = regarding
	_ = related
	r.events = append(r.events, strings.Join([]string{
		eventtype,
		reason,
		action,
		note,
	}, " "))
}

type statusTrackingClient struct {
	client.Client
	writer client.StatusWriter
}

func (c *statusTrackingClient) Status() client.StatusWriter {
	return c.writer
}

type countingStatusWriter struct {
	updates   int
	lastRoute *gatewayv1.HTTPRoute
}

func (w *countingStatusWriter) Create(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	return nil
}

func (w *countingStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	w.updates++
	if route, ok := obj.(*gatewayv1.HTTPRoute); ok {
		w.lastRoute = route.DeepCopy()
	}
	return nil
}

func (w *countingStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return nil
}

func (w *countingStatusWriter) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
	return nil
}

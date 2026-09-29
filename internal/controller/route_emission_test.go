package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/controller/annotations"
)

func emissionFixtures() (*cfgatev1alpha1.CloudflareTunnel, *gatewayv1.GatewayClass, *gatewayv1.Gateway, *gatewayv1.HTTPRoute, *corev1.Service) {
	tunnel := &cfgatev1alpha1.CloudflareTunnel{ObjectMeta: metav1.ObjectMeta{Name: "edge", Namespace: "app"}}
	class := &gatewayv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "cfgate"}, Spec: gatewayv1.GatewayClassSpec{ControllerName: GatewayControllerName}}
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "app", Annotations: map[string]string{annotations.AnnotationTunnelRef: "edge"}},
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: "cfgate", Listeners: []gatewayv1.Listener{{Name: "http", Port: 80, Protocol: gatewayv1.HTTPProtocolType}}},
	}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app", Generation: 2},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{{Name: "gateway", Namespace: ptr.To(gatewayv1.Namespace("app"))}}},
			Hostnames:       []gatewayv1.Hostname{"app.example.com"},
			Rules:           []gatewayv1.HTTPRouteRule{{BackendRefs: []gatewayv1.HTTPBackendRef{{BackendRef: gatewayv1.BackendRef{BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend", Port: ptr.To(gatewayv1.PortNumber(8080))}}}}}},
		},
	}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: "app"}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}}}
	return tunnel, class, gw, route, service
}

func TestGatewayTunnelGrantAlignsStatusAndPublication(t *testing.T) {
	for _, permitted := range []bool{false, true} {
		t.Run(fmt.Sprint(permitted), func(t *testing.T) {
			tunnel, class, gw, route, service := emissionFixtures()
			tunnel.Namespace = "infra"
			gw.Annotations[annotations.AnnotationTunnelRef] = "infra/edge"
			objects := []client.Object{tunnel, class, gw, route, service}
			if permitted {
				objects = append(objects, &gatewayv1beta1.ReferenceGrant{ObjectMeta: metav1.ObjectMeta{Name: "allow-tunnel", Namespace: "infra"}, Spec: gatewayv1beta1.ReferenceGrantSpec{
					From: []gatewayv1beta1.ReferenceGrantFrom{{Group: gatewayv1.GroupName, Kind: "Gateway", Namespace: "app"}},
					To:   []gatewayv1beta1.ReferenceGrantTo{{Group: "cfgate.io", Kind: "CloudflareTunnel", Name: ptr.To(gatewayv1beta1.ObjectName("edge"))}},
				}})
			}
			scheme := controllerTestScheme(t)
			if err := gatewayv1beta1.Install(scheme); err != nil {
				t.Fatal(err)
			}
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			parent := (&HTTPRouteReconciler{Client: kube}).validateParentRef(context.Background(), route, route.Spec.ParentRefs[0])
			if (parent.Conditions[0].Status == metav1.ConditionTrue) != permitted {
				t.Fatalf("parent=%+v", parent)
			}
			rules, _, err := (&CloudflareTunnelReconciler{Client: kube, Recorder: &fakeEventRecorder{}}).collectIngressRules(context.Background(), tunnel)
			if err != nil {
				t.Fatal(err)
			}
			if (len(rules) > 0) != permitted {
				t.Fatalf("emission=%+v permitted=%v", rules, permitted)
			}
		})
	}
}

func TestCollectIngressRulesEnforcesCurrentReferences(t *testing.T) {
	for _, tt := range []struct {
		name     string
		change   func(*gatewayv1.GatewayClass, *gatewayv1.Gateway, *gatewayv1.HTTPRoute, *corev1.Service) []client.Object
		want     int
		hostname string
		service  string
	}{
		{name: "valid without status", want: 1},
		{name: "valid despite stale rejection", want: 1, change: func(_ *gatewayv1.GatewayClass, _ *gatewayv1.Gateway, route *gatewayv1.HTTPRoute, _ *corev1.Service) []client.Object {
			route.Status.Parents = []gatewayv1.RouteParentStatus{{ParentRef: route.Spec.ParentRefs[0], ControllerName: GatewayControllerName, Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionFalse, ObservedGeneration: 1}}}}
			return nil
		}},
		{name: "wildcard narrowed to exact listener", want: 1, hostname: "app.example.com", change: func(_ *gatewayv1.GatewayClass, gw *gatewayv1.Gateway, route *gatewayv1.HTTPRoute, _ *corev1.Service) []client.Object {
			route.Spec.Hostnames = []gatewayv1.Hostname{"*.example.com"}
			gw.Spec.Listeners[0].Hostname = ptr.To(gatewayv1.Hostname("app.example.com"))
			return nil
		}},
		{name: "wildcard narrowed to listener wildcard", want: 1, hostname: "*.app.example.com", change: func(_ *gatewayv1.GatewayClass, gw *gatewayv1.Gateway, route *gatewayv1.HTTPRoute, _ *corev1.Service) []client.Object {
			route.Spec.Hostnames = []gatewayv1.Hostname{"*.example.com"}
			gw.Spec.Listeners[0].Hostname = ptr.To(gatewayv1.Hostname("*.app.example.com"))
			return nil
		}},
		{name: "listener hostname fallback", want: 1, hostname: "app.example.com", change: func(_ *gatewayv1.GatewayClass, gw *gatewayv1.Gateway, route *gatewayv1.HTTPRoute, _ *corev1.Service) []client.Object {
			route.Spec.Hostnames = nil
			gw.Spec.Listeners[0].Hostname = ptr.To(gatewayv1.Hostname("app.example.com"))
			return nil
		}},
		{name: "annotation hostname override", want: 1, hostname: "override.example.com", change: func(_ *gatewayv1.GatewayClass, gw *gatewayv1.Gateway, route *gatewayv1.HTTPRoute, _ *corev1.Service) []client.Object {
			route.Annotations = map[string]string{annotations.AnnotationHostname: "override.example.com"}
			gw.Spec.Listeners[0].Hostname = ptr.To(gatewayv1.Hostname("override.example.com"))
			return nil
		}},
		{name: "selector admits namespace", want: 1, change: func(_ *gatewayv1.GatewayClass, gw *gatewayv1.Gateway, route *gatewayv1.HTTPRoute, service *corev1.Service) []client.Object {
			route.Namespace = "other"
			service.Namespace = "other"
			gw.Spec.Listeners[0].AllowedRoutes = &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{From: ptr.To(gatewayv1.NamespacesFromSelector), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "app"}}}}
			return []client.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "other", Labels: map[string]string{"team": "app"}}}}
		}},
		{name: "missing backend service", want: 1, service: "http_status:500", change: func(_ *gatewayv1.GatewayClass, _ *gatewayv1.Gateway, _ *gatewayv1.HTTPRoute, service *corev1.Service) []client.Object {
			service.Name = "different"
			return nil
		}},
		{name: "missing backend port", want: 1, service: "http_status:500", change: func(_ *gatewayv1.GatewayClass, _ *gatewayv1.Gateway, _ *gatewayv1.HTTPRoute, service *corev1.Service) []client.Object {
			service.Spec.Ports[0].Port = 81
			return nil
		}},
		{name: "denied sibling keeps valid route", want: 1, change: func(_ *gatewayv1.GatewayClass, _ *gatewayv1.Gateway, route *gatewayv1.HTTPRoute, _ *corev1.Service) []client.Object {
			denied := route.DeepCopy()
			denied.Name = "denied"
			denied.Spec.ParentRefs[0].SectionName = ptr.To(gatewayv1.SectionName("absent"))
			return []client.Object{denied}
		}},
		{name: "namespace denied despite stale accepted status", change: func(_ *gatewayv1.GatewayClass, _ *gatewayv1.Gateway, route *gatewayv1.HTTPRoute, service *corev1.Service) []client.Object {
			route.Namespace = "other"
			service.Namespace = "other"
			route.Status.Parents = []gatewayv1.RouteParentStatus{{ParentRef: route.Spec.ParentRefs[0], ControllerName: GatewayControllerName, Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue, ObservedGeneration: 1}}}}
			return nil
		}},
		{name: "backend grant missing", want: 1, service: "http_status:500", change: func(_ *gatewayv1.GatewayClass, _ *gatewayv1.Gateway, route *gatewayv1.HTTPRoute, service *corev1.Service) []client.Object {
			route.Spec.Rules[0].BackendRefs[0].Namespace = ptr.To(gatewayv1.Namespace("backend"))
			service.Namespace = "backend"
			return nil
		}},
		{name: "backend grant permits", want: 1, change: func(_ *gatewayv1.GatewayClass, _ *gatewayv1.Gateway, route *gatewayv1.HTTPRoute, service *corev1.Service) []client.Object {
			route.Spec.Rules[0].BackendRefs[0].Namespace = ptr.To(gatewayv1.Namespace("backend"))
			service.Namespace = "backend"
			return []client.Object{&gatewayv1beta1.ReferenceGrant{ObjectMeta: metav1.ObjectMeta{Name: "allow", Namespace: "backend"}, Spec: gatewayv1beta1.ReferenceGrantSpec{
				From: []gatewayv1beta1.ReferenceGrantFrom{{Group: gatewayv1.GroupName, Kind: "HTTPRoute", Namespace: "app"}},
				To:   []gatewayv1beta1.ReferenceGrantTo{{Group: "", Kind: "Service", Name: ptr.To(gatewayv1beta1.ObjectName("backend"))}},
			}}}
		}},
		{name: "foreign gateway class", change: func(class *gatewayv1.GatewayClass, _ *gatewayv1.Gateway, _ *gatewayv1.HTTPRoute, _ *corev1.Service) []client.Object {
			class.Spec.ControllerName = "other.example/controller"
			return nil
		}},
		{name: "missing listener section", change: func(_ *gatewayv1.GatewayClass, _ *gatewayv1.Gateway, route *gatewayv1.HTTPRoute, _ *corev1.Service) []client.Object {
			route.Spec.ParentRefs[0].SectionName = ptr.To(gatewayv1.SectionName("missing"))
			return nil
		}},
		{name: "mismatched listener port", change: func(_ *gatewayv1.GatewayClass, _ *gatewayv1.Gateway, route *gatewayv1.HTTPRoute, _ *corev1.Service) []client.Object {
			route.Spec.ParentRefs[0].Port = ptr.To(gatewayv1.PortNumber(81))
			return nil
		}},
		{name: "hostname outside listener", change: func(_ *gatewayv1.GatewayClass, gw *gatewayv1.Gateway, _ *gatewayv1.HTTPRoute, _ *corev1.Service) []client.Object {
			gw.Spec.Listeners[0].Hostname = ptr.To(gatewayv1.Hostname("other.example.com"))
			return nil
		}},
		{name: "filter individual hostnames", want: 1, change: func(_ *gatewayv1.GatewayClass, gw *gatewayv1.Gateway, route *gatewayv1.HTTPRoute, _ *corev1.Service) []client.Object {
			gw.Spec.Listeners[0].Hostname = ptr.To(gatewayv1.Hostname("app.example.com"))
			route.Spec.Hostnames = append(route.Spec.Hostnames, "other.example.com")
			return nil
		}},
		{name: "second parent accepted", want: 1, change: func(_ *gatewayv1.GatewayClass, _ *gatewayv1.Gateway, route *gatewayv1.HTTPRoute, _ *corev1.Service) []client.Object {
			bad := route.Spec.ParentRefs[0]
			bad.SectionName = ptr.To(gatewayv1.SectionName("missing"))
			route.Spec.ParentRefs = append([]gatewayv1.ParentReference{bad}, route.Spec.ParentRefs...)
			return nil
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tunnel, class, gw, route, service := emissionFixtures()
			objects := []client.Object{class, gw, route, service}
			if tt.change != nil {
				objects = append(objects, tt.change(class, gw, route, service)...)
			}
			scheme := controllerTestScheme(t)
			if err := gatewayv1beta1.Install(scheme); err != nil {
				t.Fatal(err)
			}
			r := &CloudflareTunnelReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(), Recorder: &fakeEventRecorder{}}
			rules, _, err := r.collectIngressRules(context.Background(), tunnel)
			if err != nil {
				t.Fatalf("collectIngressRules() error = %v", err)
			}
			if len(rules) != tt.want {
				t.Fatalf("emitted rules = %#v, want %d", rules, tt.want)
			}
			if tt.service != "" && rules[0].Service != tt.service {
				t.Fatalf("service = %q, want %q", rules[0].Service, tt.service)
			}
			if tt.hostname != "" && rules[0].Hostname != tt.hostname {
				t.Fatalf("hostname = %q, want %q", rules[0].Hostname, tt.hostname)
			}
		})
	}
}

type emissionReadClient struct {
	client.Client
	failKind string
	reads    map[string]int
	err      error
}

func (c *emissionReadClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	kind := fmt.Sprintf("%T", obj)
	c.reads[kind]++
	if kind == c.failKind {
		return c.err
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *emissionReadClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	kind := fmt.Sprintf("%T", list)
	c.reads[kind]++
	if kind == c.failKind {
		return c.err
	}
	return c.Client.List(ctx, list, opts...)
}

func TestIngressValidationErrorsPreventRemoteSync(t *testing.T) {
	for _, kind := range []string{"*v1.Service", "*v1.GatewayClass", "*v1.Namespace", "*v1beta1.ReferenceGrantList", "*v1.HTTPRouteList", "*v1.GatewayList"} {
		t.Run(kind, func(t *testing.T) {
			tunnel, class, gw, route, service := emissionFixtures()
			tunnel.Status.TunnelID = "tunnel-id"
			route.Spec.Rules[0].BackendRefs[0].Namespace = ptr.To(gatewayv1.Namespace("backend"))
			service.Namespace = "backend"
			gw.Spec.Listeners[0].AllowedRoutes = &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{From: ptr.To(gatewayv1.NamespacesFromSelector), Selector: &metav1.LabelSelector{}}}
			grant := &gatewayv1beta1.ReferenceGrant{ObjectMeta: metav1.ObjectMeta{Name: "allow", Namespace: "backend"}, Spec: gatewayv1beta1.ReferenceGrantSpec{From: []gatewayv1beta1.ReferenceGrantFrom{{Group: gatewayv1.GroupName, Kind: "HTTPRoute", Namespace: "app"}}, To: []gatewayv1beta1.ReferenceGrantTo{{Kind: "Service"}}}}
			scheme := controllerTestScheme(t)
			if err := gatewayv1beta1.Install(scheme); err != nil {
				t.Fatal(err)
			}
			readErr := errors.New("temporary read failure")
			c := &emissionReadClient{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(class, gw, route, service, grant, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app"}}).Build(), failKind: kind, reads: map[string]int{}, err: readErr}
			mock := cloudflare.NewMockClient()
			mock.UpdateTunnelConfigurationFunc = func(context.Context, string, string, cloudflare.TunnelConfiguration) error {
				t.Fatal("must not publish incomplete configuration")
				return nil
			}
			r := &CloudflareTunnelReconciler{Client: c, CFClient: mock, Recorder: &fakeEventRecorder{}}
			installTunnelClaimForTest(t, r, tunnel)
			if err := r.syncConfiguration(context.Background(), tunnel); !errors.Is(err, readErr) {
				t.Fatalf("sync error = %v, want read failure", err)
			}
		})
	}
}

func TestIngressValidationReadCountAndGrantRevocation(t *testing.T) {
	tunnel, class, gw, route, service := emissionFixtures()
	route.Spec.Rules[0].BackendRefs[0].Namespace = ptr.To(gatewayv1.Namespace("backend"))
	service.Namespace = "backend"
	route.Spec.Hostnames = []gatewayv1.Hostname{"one.example.com", "two.example.com"}
	route.Spec.Rules[0].Matches = []gatewayv1.HTTPRouteMatch{{Path: &gatewayv1.HTTPPathMatch{Value: ptr.To("/one")}}, {Path: &gatewayv1.HTTPPathMatch{Value: ptr.To("/two")}}}
	other := gw.DeepCopy()
	other.Name = "second"
	route.Spec.ParentRefs = append(route.Spec.ParentRefs, gatewayv1.ParentReference{Name: "second"})
	grant := &gatewayv1beta1.ReferenceGrant{ObjectMeta: metav1.ObjectMeta{Name: "allow", Namespace: "backend"}, Spec: gatewayv1beta1.ReferenceGrantSpec{From: []gatewayv1beta1.ReferenceGrantFrom{{Group: gatewayv1.GroupName, Kind: "HTTPRoute", Namespace: "app"}}, To: []gatewayv1beta1.ReferenceGrantTo{{Kind: "Service"}}}}
	scheme := controllerTestScheme(t)
	if err := gatewayv1beta1.Install(scheme); err != nil {
		t.Fatal(err)
	}
	c := &emissionReadClient{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(class, gw, other, route, service, grant).Build(), reads: map[string]int{}}
	r := &CloudflareTunnelReconciler{Client: c, Recorder: &fakeEventRecorder{}}
	rules, count, err := r.collectIngressRules(context.Background(), tunnel)
	if err != nil || len(rules) != 4 || count != 1 {
		t.Fatalf("collect = %v, %d rules, %d routes; want nil,4,1", err, len(rules), count)
	}
	for _, kind := range []string{"*v1.Service", "*v1.GatewayClass", "*v1beta1.ReferenceGrantList"} {
		if c.reads[kind] != 1 {
			t.Fatalf("%s reads = %d, want 1 regardless of hostnames/matches/parents", kind, c.reads[kind])
		}
	}
	if err := c.Delete(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	rules, _, err = r.collectIngressRules(context.Background(), tunnel)
	if err != nil || len(rules) != 4 {
		t.Fatalf("after revocation = %v, %d rules; want four blocking responses", err, len(rules))
	}
	for _, rule := range rules {
		if rule.Service != "http_status:500" {
			t.Fatalf("revoked backend still forwarded: %+v", rule)
		}
	}
	grant.ResourceVersion = ""
	if err := c.Create(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	rules, _, err = r.collectIngressRules(context.Background(), tunnel)
	if err != nil || len(rules) != 4 {
		t.Fatalf("after grant restored = %v, %d rules; want4", err, len(rules))
	}
	// Parent status is never consulted or written by collection.
	var current gatewayv1.HTTPRoute
	if err := c.Get(context.Background(), types.NamespacedName{Name: route.Name, Namespace: route.Namespace}, &current); err != nil {
		t.Fatal(err)
	}
	if len(current.Status.Parents) != 0 {
		t.Fatal("collection unexpectedly changed status")
	}
}

func TestRemoteIngressOmitsDeniedRouteAndKeepsValidSibling(t *testing.T) {
	tunnel, class, gw, route, service := emissionFixtures()
	tunnel.Status.TunnelID = "tunnel-id"
	tunnel.Spec.Cloudflare.AccountID = "account"
	denied := route.DeepCopy()
	denied.Name = "denied"
	denied.Spec.Hostnames = []gatewayv1.Hostname{"denied.example.com"}
	denied.Spec.ParentRefs[0].SectionName = ptr.To(gatewayv1.SectionName("missing"))
	c := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithObjects(tunnel, class, gw, route, denied, service).Build()
	called := false
	mock := cloudflare.NewMockClient()
	mock.UpdateTunnelConfigurationFunc = func(_ context.Context, _, _ string, config cloudflare.TunnelConfiguration) error {
		called = true
		if len(config.Ingress) != 2 || config.Ingress[0].Hostname != "app.example.com" || config.Ingress[1].Service != "http_status:404" {
			t.Fatalf("remote ingress = %#v, want valid route and fallback only", config.Ingress)
		}
		return nil
	}
	r := &CloudflareTunnelReconciler{Client: c, CFClient: mock, Recorder: &fakeEventRecorder{}}
	installTunnelClaimForTest(t, r, tunnel)
	if err := r.syncConfiguration(context.Background(), tunnel); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("remote config update was not called")
	}
}

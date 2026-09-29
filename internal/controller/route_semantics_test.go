package controller

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"cfgate.io/cfgate/internal/cloudflare"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	g "sigs.k8s.io/gateway-api/apis/v1"
	gb "sigs.k8s.io/gateway-api/apis/v1beta1"
)

func TestUnsupportedRouteNeverPublishes(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*g.HTTPRouteRule)
	}{
		{"method", func(r *g.HTTPRouteRule) { r.Matches = []g.HTTPRouteMatch{{Method: ptr.To(g.HTTPMethod("POST"))}} }},
		{"headers", func(r *g.HTTPRouteRule) {
			r.Matches = []g.HTTPRouteMatch{{Headers: []g.HTTPHeaderMatch{{Name: "x-auth", Value: "yes"}}}}
		}},
		{"query", func(r *g.HTTPRouteRule) {
			r.Matches = []g.HTTPRouteMatch{{QueryParams: []g.HTTPQueryParamMatch{{Name: "role", Value: "admin"}}}}
		}},
		{"rule filter", func(r *g.HTTPRouteRule) {
			r.Filters = []g.HTTPRouteFilter{{Type: g.HTTPRouteFilterRequestHeaderModifier}}
		}},
		{"backend filter", func(r *g.HTTPRouteRule) {
			r.BackendRefs[0].Filters = []g.HTTPRouteFilter{{Type: g.HTTPRouteFilterRequestHeaderModifier}}
		}},
		{"timeout", func(r *g.HTTPRouteRule) { r.Timeouts = &g.HTTPRouteTimeouts{Request: ptr.To(g.Duration("1s"))} }},
		{"retry", func(r *g.HTTPRouteRule) { r.Retry = &g.HTTPRouteRetry{} }},
		{"session", func(r *g.HTTPRouteRule) { r.SessionPersistence = &g.SessionPersistence{} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tunnel, class, gw, route, svc := emissionFixtures()
			tt.change(&route.Spec.Rules[0])
			tunnel.Spec.Cloudflare.AccountID = "account"
			tunnel.Status.TunnelID = "id"
			kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithObjects(tunnel, class, gw, route, svc).Build()
			status := (&HTTPRouteReconciler{Client: kube}).validateParentRef(context.Background(), route, route.Spec.ParentRefs[0])
			if status.Conditions[0].Status != metav1.ConditionFalse {
				t.Error("unsupported restriction accepted in parent status")
			}
			called := false
			mock := cloudflare.NewMockClient()
			mock.UpdateTunnelConfigurationFunc = func(_ context.Context, _, _ string, c cloudflare.TunnelConfiguration) error {
				called = true
				for _, rule := range c.Ingress {
					if strings.Contains(rule.Service, "backend.app") {
						t.Errorf("unsupported route published backend: %+v", rule)
					}
				}
				return nil
			}
			reconciler := &CloudflareTunnelReconciler{Client: kube, CFClient: mock, Recorder: &fakeEventRecorder{}}
			installTunnelClaimForTest(t, reconciler, tunnel)
			if err := reconciler.syncConfiguration(context.Background(), tunnel); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("no remote update observed")
			}
		})
	}
}

func TestInvalidBackendKeepsMatchedFailureAndValidSiblings(t *testing.T) {
	for _, cause := range []string{"missing service", "denied grant", "missing port", "noncanonical group", "multiple backends"} {
		t.Run(cause, func(t *testing.T) {
			tunnel, class, gw, route, svc := emissionFixtures()
			tunnel.Spec.Cloudflare.AccountID, tunnel.Status.TunnelID = "account", "remote"
			valid := route.Spec.Rules[0]
			valid.Matches = []g.HTTPRouteMatch{{Path: &g.HTTPPathMatch{Value: ptr.To("/valid")}}}
			invalid := *valid.DeepCopy()
			invalid.Matches = []g.HTTPRouteMatch{{Path: &g.HTTPPathMatch{Value: ptr.To("/private")}}}
			switch cause {
			case "missing service":
				invalid.BackendRefs[0].Name = "missing"
			case "denied grant":
				invalid.BackendRefs[0].Namespace = ptr.To(g.Namespace("restricted"))
			case "missing port":
				invalid.BackendRefs[0].Port = nil // defaults to 80, which this Service does not expose
			case "noncanonical group":
				invalid.BackendRefs[0].Group = ptr.To(g.Group("core"))
			case "multiple backends":
				invalid.BackendRefs = append(invalid.BackendRefs, invalid.BackendRefs[0])
			}
			route.Spec.Rules = []g.HTTPRouteRule{valid, invalid}
			broad := route.DeepCopy()
			broad.Name = "public"
			broad.Spec.Rules = []g.HTTPRouteRule{valid}
			broad.Spec.Rules[0].Matches = nil
			scheme := controllerTestScheme(t)
			if err := gb.Install(scheme); err != nil {
				t.Fatal(err)
			}
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tunnel, class, gw, route, broad, svc).Build()
			condition, err := validateHTTPRouteBackendRefs(context.Background(), kube, route)
			if err != nil || condition.Status != metav1.ConditionFalse {
				t.Fatalf("backend condition=%+v, err=%v", condition, err)
			}
			var published cloudflare.TunnelConfiguration
			mock := cloudflare.NewMockClient()
			mock.UpdateTunnelConfigurationFunc = func(_ context.Context, _, _ string, c cloudflare.TunnelConfiguration) error {
				published = c
				return nil
			}
			r := &CloudflareTunnelReconciler{Client: kube, CFClient: mock, Recorder: &fakeEventRecorder{}}
			installTunnelClaimForTest(t, r, tunnel)
			if err := r.syncConfiguration(context.Background(), tunnel); err != nil {
				t.Fatal(err)
			}
			for path, expected := range map[string]string{"/private": "http_status:500", "/private/nested": "http_status:500", "/valid": "http://backend.app.svc.cluster.local:8080", "/other": "http://backend.app.svc.cluster.local:8080"} {
				matched := ""
				for _, rule := range published.Ingress {
					if rule.Hostname != "app.example.com" {
						continue
					}
					if rule.Path == "" || regexp.MustCompile(rule.Path).MatchString(path) {
						matched = rule.Service
						break
					}
				}
				if matched != expected {
					t.Errorf("%s matched %q, want %q; config=%+v", path, matched, expected, published)
				}
			}
		})
	}
}

func TestRoutePrecedenceUsesSourceSemantics(t *testing.T) {
	for _, tt := range []struct {
		name, path string
		exact      bool
		value      string
		older      bool
		want       string
	}{
		{"exact beats prefix", "/foo", true, "/foo", false, "challenger"},
		{"longer prefix wins", "/foo/bar", false, "/foo/bar", false, "challenger"},
		{"trailing slash ignores final slash", "/foo", false, "/foo/", true, "challenger"},
		{"older route wins", "/foo", false, "/foo", true, "challenger"},
		{"name breaks same time tie", "/foo", false, "/foo", false, "base"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tunnel, class, gw, base, svc := emissionFixtures()
			base.Name = "a-base"
			base.CreationTimestamp = metav1.NewTime(time.Unix(20, 0))
			base.Spec.Rules[0].Matches = []g.HTTPRouteMatch{{Path: &g.HTTPPathMatch{Type: ptr.To(g.PathMatchPathPrefix), Value: ptr.To("/foo")}}}
			other := base.DeepCopy()
			other.Name = "b-challenger"
			other.Spec.Rules[0].BackendRefs[0].Name = "challenger"
			other.Spec.Rules[0].Matches[0].Path.Value = &tt.value
			if tt.exact {
				other.Spec.Rules[0].Matches[0].Path.Type = ptr.To(g.PathMatchExact)
			}
			if tt.older {
				other.CreationTimestamp = metav1.NewTime(time.Unix(10, 0))
			}
			backend := svc.DeepCopy()
			backend.Name = "challenger"
			kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithObjects(tunnel, class, gw, base, other, svc, backend).Build()
			rules, _, err := (&CloudflareTunnelReconciler{Client: kube, Recorder: &fakeEventRecorder{}}).collectIngressRules(context.Background(), tunnel)
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			for _, rule := range rules {
				if rule.Path == "" || regexp.MustCompile(rule.Path).MatchString(tt.path) {
					got = rule.Service
					break
				}
			}
			want := "backend"
			if tt.want == "challenger" {
				want = "challenger"
			}
			if !strings.Contains(got, "://"+want+".") {
				t.Fatalf("request %s chose %s, want %s", tt.path, got, want)
			}
		})
	}
}

func TestZeroWeightDoesNotFallThrough(t *testing.T) {
	tunnel, class, gw, route, svc := emissionFixtures()
	route.Spec.Rules[0].BackendRefs[0].Weight = ptr.To(int32(0))
	route.Spec.Rules[0].Matches = []g.HTTPRouteMatch{{Path: &g.HTTPPathMatch{Type: ptr.To(g.PathMatchExact), Value: ptr.To("/blocked")}}}
	fallback := route.DeepCopy()
	fallback.Name = "fallback"
	fallback.Spec.Rules[0].BackendRefs[0].Weight = nil
	fallback.Spec.Rules[0].Matches = nil
	objects := []client.Object{tunnel, class, gw, route, fallback, svc}
	kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithObjects(objects...).Build()
	rules, _, err := (&CloudflareTunnelReconciler{Client: kube}).collectIngressRules(context.Background(), tunnel)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range rules {
		if rule.Path == "" || regexp.MustCompile(rule.Path).MatchString("/blocked") {
			if rule.Service != "http_status:500" {
				t.Fatalf("zero-weight request forwarded to %s", rule.Service)
			}
			return
		}
	}
	t.Fatal("no zero-weight rejection rule")
}

func TestRouteRegexAndWildcardPrecedence(t *testing.T) {
	tunnel, class, gw, route, svc := emissionFixtures()
	route.Spec.Hostnames = []g.Hostname{"*.example.com"}
	route.Spec.Rules[0].Matches = []g.HTTPRouteMatch{{Path: &g.HTTPPathMatch{Type: ptr.To(g.PathMatchExact), Value: ptr.To("/foo")}}}
	exact := route.DeepCopy()
	exact.Name = "specific-host"
	exact.Spec.Hostnames = []g.Hostname{"app.example.com"}
	exact.Spec.Rules[0].Matches = nil
	exact.Spec.Rules[0].BackendRefs[0].Name = "specific"
	regex := exact.DeepCopy()
	regex.Name = "regex"
	regex.Spec.Rules[0].Matches = []g.HTTPRouteMatch{{Path: &g.HTTPPathMatch{Type: ptr.To(g.PathMatchRegularExpression), Value: ptr.To("^/v[0-9]+$")}}}
	regex.Spec.Rules[0].BackendRefs[0].Name = "regex"
	specificService := svc.DeepCopy()
	specificService.Name = "specific"
	regexService := svc.DeepCopy()
	regexService.Name = "regex"
	kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithObjects(tunnel, class, gw, route, exact, regex, svc, specificService, regexService).Build()
	rules, _, err := (&CloudflareTunnelReconciler{Client: kube}).collectIngressRules(context.Background(), tunnel)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ path, backend string }{{"/foo", "specific"}, {"/v2", "regex"}} {
		got := ""
		for _, rule := range rules {
			if rule.Path == "" || regexp.MustCompile(rule.Path).MatchString(tt.path) {
				got = rule.Service
				break
			}
		}
		if !strings.Contains(got, "://"+tt.backend+".") {
			t.Fatalf("%s chose %s, want %s", tt.path, got, tt.backend)
		}
	}
}

func TestPositiveSingleWeightAndRuleOrder(t *testing.T) {
	tunnel, class, gw, route, svc := emissionFixtures()
	route.Spec.Rules[0].BackendRefs[0].Weight = ptr.To(int32(100))
	second := route.Spec.Rules[0].DeepCopy()
	second.BackendRefs[0].Name = "second"
	route.Spec.Rules = append(route.Spec.Rules, *second)
	other := svc.DeepCopy()
	other.Name = "second"
	kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithObjects(tunnel, class, gw, route, svc, other).Build()
	rules, _, err := (&CloudflareTunnelReconciler{Client: kube}).collectIngressRules(context.Background(), tunnel)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 || !strings.Contains(rules[0].Service, "://backend.") {
		t.Fatalf("single positive weight or first rule not preserved: %+v", rules)
	}
}

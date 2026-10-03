package controller

import (
	"context"
	"fmt"
	"k8s.io/utils/ptr"
	"regexp"
	g "sigs.k8s.io/gateway-api/apis/v1"
	gb "sigs.k8s.io/gateway-api/apis/v1beta1"
	"strings"
	"testing"
	"time"

	"cfgate.io/cfgate/internal/cloudflare"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestInvalidEffectiveTransportKeepsSiblingAndWithdrawal(t *testing.T) {
	for _, scenario := range []string{"inherited-conflict", "https-h2c", "inherited-https-h2c"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			tunnel, class, gw, route, svc := emissionFixtures()
			tunnel.Spec.Cloudflare.AccountID, tunnel.Status.TunnelID = "account", "remote"
			sibling := route.DeepCopy()
			sibling.Name = "sibling"
			sibling.Spec.Rules[0].Matches = nil
			route.Spec.Rules[0].Matches = []g.HTTPRouteMatch{{Path: &g.HTTPPathMatch{Value: ptr.To("/private")}}}
			withdrawn := route.DeepCopy()
			withdrawn.Name = "withdrawn"
			withdrawn.Spec.Hostnames[0] = "withdrawn.example.com"
			remoteService := svc.DeepCopy()
			remoteService.Namespace = "remote"
			withdrawn.Spec.Rules[0].BackendRefs[0].Namespace = ptr.To(g.Namespace("remote"))
			grant := &gb.ReferenceGrant{ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: "remote"}, Spec: gb.ReferenceGrantSpec{From: []gb.ReferenceGrantFrom{{Group: g.GroupName, Kind: "HTTPRoute", Namespace: "app"}}, To: []gb.ReferenceGrantTo{{Group: "", Kind: "Service"}}}}
			scheme := controllerTestScheme(t)
			if err := gb.Install(scheme); err != nil {
				t.Fatal(err)
			}
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tunnel, class, gw, route, sibling, withdrawn, svc, remoteService, grant).Build()
			var published cloudflare.TunnelConfiguration
			mock := cloudflare.NewMockClient()
			mock.UpdateTunnelConfigurationFunc = func(_ context.Context, _, _ string, c cloudflare.TunnelConfiguration) error {
				published = c
				return nil
			}
			r := &CloudflareTunnelReconciler{Client: kube, CFClient: mock, Recorder: &fakeEventRecorder{}}
			installTunnelClaimForTest(t, r, tunnel)
			if err := r.syncConfiguration(ctx, tunnel); err != nil {
				t.Fatal(err)
			}
			route.Annotations = map[string]string{"cfgate.io/origin-h2c": "true"}
			switch scenario {
			case "inherited-conflict":
				tunnel.Spec.OriginDefaults.HTTP2Origin = true
			case "https-h2c":
				route.Annotations["cfgate.io/origin-protocol"] = "https"
			case "inherited-https-h2c":
				tunnel.Spec.OriginDefaults.H2cOrigin = true
				route.Annotations = map[string]string{"cfgate.io/origin-protocol": "https"}
			}
			if err := kube.Update(ctx, tunnel); err != nil {
				t.Fatal(err)
			}
			if err := kube.Update(ctx, route); err != nil {
				t.Fatal(err)
			}
			if err := kube.Delete(ctx, grant); err != nil {
				t.Fatal(err)
			}
			parent := (&HTTPRouteReconciler{Client: kube}).validateParentRef(ctx, route, route.Spec.ParentRefs[0])
			if parent.Conditions[0].Status != metav1.ConditionFalse {
				t.Errorf("invalid transport accepted: %+v", parent)
			}
			if err := r.syncConfiguration(ctx, tunnel); err != nil {
				t.Fatalf("invalid route prevented publication: %v", err)
			}
			match := func(host, path string) string {
				for _, rule := range published.Ingress {
					if rule.Hostname == host && (rule.Path == "" || regexp.MustCompile(rule.Path).MatchString(path)) {
						return rule.Service
					}
				}
				return ""
			}
			if match("app.example.com", "/private/x") != "http_status:503" || !strings.HasPrefix(match("app.example.com", "/public"), "http://") || match("withdrawn.example.com", "/private") != "http_status:500" {
				t.Fatalf("unexpected publication: %+v", published)
			}

			if err := cloudflare.ValidateTunnelConfiguration(published, cloudflare.ClientSettings{}); err != nil {
				t.Fatal(err)
			}
			if scenario == "inherited-conflict" {
				route.Annotations["cfgate.io/origin-http2"] = "false"
			} else {
				route.Annotations["cfgate.io/origin-h2c"] = "false"
			}
			if err := kube.Update(ctx, route); err != nil {
				t.Fatal(err)
			}
			if parent := (&HTTPRouteReconciler{Client: kube}).validateParentRef(ctx, route, route.Spec.ParentRefs[0]); parent.Conditions[0].Status != metav1.ConditionTrue {
				t.Fatalf("explicit disable not accepted: %+v", parent)
			}
			if err := r.syncConfiguration(ctx, tunnel); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(match("app.example.com", "/private/x"), "://backend.app") {
				t.Fatalf("fixed transport did not recover %+v", published)
			}

		})
	}
}

func TestInvalidTransportPreservesAccessDenialPrecedence(t *testing.T) {
	for _, annotations := range []map[string]string{
		{"cfgate.io/origin-protocol": "https", "cfgate.io/origin-h2c": "true"},
		{"cfgate.io/origin-ca-pool": "/unmounted/ca.pem"},
		{"cfgate.io/origin-ca-pool": "/etc/cfgate/origin-ca-pool/ca.pem"},
		{"cfgate.io/origin-protocol": "invalid"},
		{"cfgate.io/origin-h2c": "sometimes"},
		{"cfgate.io/origin-connect-timeout": "500ms"},
	} {
		t.Run(fmt.Sprint(annotations), func(t *testing.T) {
			tunnel, class, gw, route, svc := emissionFixtures()
			route.Annotations = annotations
			route.Annotations["cfgate.io/access-required"] = "missing"
			route.CreationTimestamp = metav1.NewTime(time.Now())
			public := route.DeepCopy()
			public.Name = "public"
			public.Annotations = nil
			public.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
			kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithObjects(tunnel, class, gw, route, public, svc).Build()
			rules, _, err := (&CloudflareTunnelReconciler{Client: kube}).collectIngressRules(context.Background(), tunnel, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(rules) != 2 || rules[0].Service != "http_status:503" {
				t.Fatalf("public forwarding precedes protected denial: %+v", rules)
			}
		})
	}
}

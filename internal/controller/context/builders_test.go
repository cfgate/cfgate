package context

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
)

type errorClient struct {
	client.Client
	getErr  error
	listErr error
}

func (c *errorClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if c.getErr != nil {
		return c.getErr
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *errorClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if c.listErr != nil {
		return c.listErr
	}
	return c.Client.List(ctx, list, opts...)
}

func TestBuildTunnelContext(t *testing.T) {
	scheme := testScheme(t)
	tunnel := &cfgatev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tunnel", Namespace: "cfgate-system"},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tunnel).Build()
	ctx := context.Background()

	got, err := BuildTunnelContext(ctx, k8sClient, cloudflare.NewMockClient(), types.NamespacedName{
		Namespace: "cfgate-system",
		Name:      "tunnel",
	}, "acct-1")
	if err != nil {
		t.Fatalf("BuildTunnelContext() error = %v", err)
	}
	if got == nil {
		t.Fatal("BuildTunnelContext() returned nil context")
	}
	if got.AccountID() != "acct-1" {
		t.Fatalf("AccountID() = %q, want %q", got.AccountID(), "acct-1")
	}

	missing, err := BuildTunnelContext(ctx, k8sClient, cloudflare.NewMockClient(), types.NamespacedName{
		Namespace: "cfgate-system",
		Name:      "missing",
	}, "acct-1")
	if err != nil {
		t.Fatalf("BuildTunnelContext() missing error = %v", err)
	}
	if missing != nil {
		t.Fatalf("BuildTunnelContext() missing = %#v, want nil", missing)
	}
}

func TestBuildDNSContext(t *testing.T) {
	scheme := testScheme(t)
	tunnel := &cfgatev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tunnel", Namespace: "cfgate-system"},
		Status: cfgatev1alpha1.CloudflareTunnelStatus{
			TunnelDomain: "uuid.cfargotunnel.com",
		},
		Spec: cfgatev1alpha1.CloudflareTunnelSpec{
			Tunnel: cfgatev1alpha1.TunnelIdentity{Name: "prod"},
		},
	}
	dns := &cfgatev1alpha1.CloudflareDNS{
		ObjectMeta: metav1.ObjectMeta{Name: "dns", Namespace: "cfgate-system"},
		Spec: cfgatev1alpha1.CloudflareDNSSpec{
			TunnelRef: &cfgatev1alpha1.DNSTunnelRef{Name: "tunnel"},
			Zones:     []cfgatev1alpha1.DNSZoneConfig{{Name: "example.com"}},
		},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tunnel, dns).Build()
	got, err := BuildDNSContext(context.Background(), k8sClient, nil, types.NamespacedName{
		Namespace: "cfgate-system",
		Name:      "dns",
	})
	if err != nil {
		t.Fatalf("BuildDNSContext() error = %v", err)
	}
	if got == nil {
		t.Fatal("BuildDNSContext() returned nil context")
	}
	if got.TunnelDomain() != "uuid.cfargotunnel.com" {
		t.Fatalf("TunnelDomain() = %q, want %q", got.TunnelDomain(), "uuid.cfargotunnel.com")
	}
	if got.TunnelName() != "prod" {
		t.Fatalf("TunnelName() = %q, want %q", got.TunnelName(), "prod")
	}

	external := &cfgatev1alpha1.CloudflareDNS{
		ObjectMeta: metav1.ObjectMeta{Name: "external", Namespace: "cfgate-system"},
		Spec: cfgatev1alpha1.CloudflareDNSSpec{
			ExternalTarget: &cfgatev1alpha1.ExternalTarget{
				Type:  cfgatev1alpha1.RecordTypeCNAME,
				Value: "external.example.com",
			},
			Zones: []cfgatev1alpha1.DNSZoneConfig{{Name: "example.com"}},
		},
	}

	k8sClient = fake.NewClientBuilder().WithScheme(scheme).WithObjects(external).Build()
	got, err = BuildDNSContext(context.Background(), k8sClient, nil, types.NamespacedName{
		Namespace: "cfgate-system",
		Name:      "external",
	})
	if err != nil {
		t.Fatalf("BuildDNSContext() external error = %v", err)
	}
	if got == nil || got.TunnelDomain() != "external.example.com" {
		t.Fatalf("BuildDNSContext() external = %#v, want external target", got)
	}
}

func TestBuildAccessApplicationContext(t *testing.T) {
	scheme := testScheme(t)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"},
	}
	app := &cfgatev1alpha1.CloudflareAccessApplication{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "app"},
		Spec: cfgatev1alpha1.CloudflareAccessApplicationSpec{
			TargetRef: &cfgatev1alpha1.PolicyTargetReference{
				Group: "gateway.networking.k8s.io",
				Kind:  "HTTPRoute",
				Name:  "route",
			},
		},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(route, app).Build()
	got, err := BuildAccessApplicationContext(context.Background(), k8sClient, types.NamespacedName{
		Namespace: "app",
		Name:      "app",
	})
	if err != nil {
		t.Fatalf("BuildAccessApplicationContext() error = %v", err)
	}
	if got == nil {
		t.Fatal("BuildAccessApplicationContext() returned nil context")
	}
	if !got.AllTargetsResolved() {
		t.Fatalf("AllTargetsResolved() = false, want true")
	}
}

func TestResolveTarget(t *testing.T) {
	scheme := testScheme(t)
	targetNS := "target"
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: targetNS},
	}
	grant := &gatewayv1b1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant", Namespace: targetNS},
		Spec: gatewayv1b1.ReferenceGrantSpec{
			From: []gatewayv1b1.ReferenceGrantFrom{{
				Group:     gatewayv1.Group(cfgatev1alpha1.GroupVersion.Group),
				Kind:      gatewayv1.Kind("CloudflareAccessApplication"),
				Namespace: gatewayv1.Namespace("policy-ns"),
			}},
			To: []gatewayv1b1.ReferenceGrantTo{{
				Group: gatewayv1.Group(gatewayv1.GroupName),
				Kind:  gatewayv1.Kind("HTTPRoute"),
			}},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(route, grant).Build()

	ref := &cfgatev1alpha1.PolicyTargetReference{
		Group:     gatewayv1.GroupName,
		Kind:      "HTTPRoute",
		Name:      "route",
		Namespace: &targetNS,
	}

	info := resolveTarget(context.Background(), ref, "policy-ns", k8sClient, testLogger())
	if !info.Resolved || info.Error != nil {
		t.Fatalf("resolveTarget() = %+v, want resolved target", info)
	}

	info = resolveTarget(context.Background(), &cfgatev1alpha1.PolicyTargetReference{
		Group: gatewayv1.GroupName,
		Kind:  "Service",
		Name:  "svc",
	}, "policy-ns", k8sClient, testLogger())
	if info.Error == nil || !strings.Contains(info.Error.Error(), "unsupported target kind") {
		t.Fatalf("resolveTarget() error = %v, want unsupported kind error", info.Error)
	}
}

func TestResolveTargets(t *testing.T) {
	scheme := testScheme(t)
	route1 := &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "route-1", Namespace: "app"}}
	route2 := &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "route-2", Namespace: "app"}}
	app := &cfgatev1alpha1.CloudflareAccessApplication{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "app"},
		Spec: cfgatev1alpha1.CloudflareAccessApplicationSpec{
			TargetRef: &cfgatev1alpha1.PolicyTargetReference{
				Group: gatewayv1.GroupName,
				Kind:  "HTTPRoute",
				Name:  "route-1",
			},
			TargetRefs: []cfgatev1alpha1.PolicyTargetReference{{
				Group: gatewayv1.GroupName,
				Kind:  "HTTPRoute",
				Name:  "route-2",
			}},
		},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(route1, route2).Build()
	targets := resolveTargets(context.Background(), app, k8sClient, testLogger())
	if len(targets) != 2 {
		t.Fatalf("len(resolveTargets()) = %d, want 2", len(targets))
	}
}

func TestTargetExists(t *testing.T) {
	scheme := testScheme(t)
	route := &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"}}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(route).Build()

	exists, err := targetExists(context.Background(), k8sClient, "HTTPRoute", "app", "route")
	if err != nil || !exists {
		t.Fatalf("targetExists(HTTPRoute) = (%v, %v), want (true, nil)", exists, err)
	}

	exists, err = targetExists(context.Background(), k8sClient, "HTTPRoute", "app", "missing")
	if err != nil || exists {
		t.Fatalf("targetExists(missing) = (%v, %v), want (false, nil)", exists, err)
	}
}

func TestCheckReferenceGrant(t *testing.T) {
	scheme := testScheme(t)
	grant := &gatewayv1b1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant", Namespace: "target"},
		Spec: gatewayv1b1.ReferenceGrantSpec{
			From: []gatewayv1b1.ReferenceGrantFrom{{
				Group:     gatewayv1.Group(cfgatev1alpha1.GroupVersion.Group),
				Kind:      gatewayv1.Kind("CloudflareAccessApplication"),
				Namespace: gatewayv1.Namespace("source"),
			}},
			To: []gatewayv1b1.ReferenceGrantTo{{
				Group: gatewayv1.Group(gatewayv1.GroupName),
				Kind:  gatewayv1.Kind("HTTPRoute"),
			}},
		},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(grant).Build()
	granted, err := checkReferenceGrant(context.Background(), k8sClient, "source", "target", "HTTPRoute")
	if err != nil || !granted {
		t.Fatalf("checkReferenceGrant() = (%v, %v), want (true, nil)", granted, err)
	}

	granted, err = checkReferenceGrant(context.Background(), k8sClient, "other", "target", "HTTPRoute")
	if err != nil || granted {
		t.Fatalf("checkReferenceGrant() = (%v, %v), want (false, nil)", granted, err)
	}

	errClient := &errorClient{Client: k8sClient, listErr: errors.New("list failed")}
	_, err = checkReferenceGrant(context.Background(), errClient, "source", "target", "HTTPRoute")
	if err == nil || err.Error() != "list failed" {
		t.Fatalf("checkReferenceGrant() error = %v, want list failed", err)
	}
}

func TestExtractHostnamesFromTarget(t *testing.T) {
	scheme := testScheme(t)
	hostname := gatewayv1.Hostname("app.example.com")
	gwHostname := gatewayv1.Hostname("gw.example.com")
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"},
		Spec: gatewayv1.HTTPRouteSpec{
			Hostnames: []gatewayv1.Hostname{hostname},
		},
	}
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "app"},
		Spec: gatewayv1.GatewaySpec{
			Listeners: []gatewayv1.Listener{{
				Name:     "http",
				Hostname: &gwHostname,
			}},
		},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(route, gateway).Build()
	hostnames, err := extractHostnamesFromTarget(context.Background(), k8sClient, TargetInfo{
		Kind:      "HTTPRoute",
		Namespace: "app",
		Name:      "route",
	})
	if err != nil || len(hostnames) != 1 || hostnames[0] != "app.example.com" {
		t.Fatalf("extractHostnamesFromTarget(HTTPRoute) = (%v, %v), want app.example.com", hostnames, err)
	}

	hostnames, err = extractHostnamesFromTarget(context.Background(), k8sClient, TargetInfo{
		Kind:      "Gateway",
		Namespace: "app",
		Name:      "gateway",
	})
	if err != nil || len(hostnames) != 1 || hostnames[0] != "gw.example.com" {
		t.Fatalf("extractHostnamesFromTarget(Gateway) = (%v, %v), want gw.example.com", hostnames, err)
	}

	_, err = extractHostnamesFromTarget(context.Background(), k8sClient, TargetInfo{
		Kind:      "UnsupportedRoute",
		Namespace: "app",
		Name:      "unsupported",
	})
	if err == nil {
		t.Fatal("extractHostnamesFromTarget(UnsupportedRoute) error = nil, want unsupported target kind")
	}
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := cfgatev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(cfgate) error = %v", err)
	}
	if err := gatewayv1.Install(scheme); err != nil {
		t.Fatalf("Install(gateway/v1) error = %v", err)
	}
	if err := gatewayv1b1.Install(scheme); err != nil {
		t.Fatalf("Install(gateway/v1beta1) error = %v", err)
	}
	return scheme
}

func testLogger() logr.Logger {
	return logr.Discard()
}

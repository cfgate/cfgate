package controller

import (
	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/controller/status"
	"context"
	"crypto/sha256"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gateway "sigs.k8s.io/gateway-api/apis/v1"
	gatewaybeta "sigs.k8s.io/gateway-api/apis/v1beta1"
	"strings"
	"testing"
	"time"
)

func TestAccessRequiredMissingApplicationDeniesMatchingRoute(t *testing.T) {
	tunnel, class, gw, route, service := emissionFixtures()
	route.Annotations = map[string]string{"cfgate.io/access-required": "app/protection"}
	c := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithObjects(class, gw, route, service).Build()
	r := &CloudflareTunnelReconciler{Client: c, APIReader: c, Recorder: &fakeEventRecorder{}}
	rules, _, err := r.collectIngressRules(context.Background(), tunnel, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Service != "http_status:503" {
		t.Fatalf("required but absent Access application forwarded route: %+v", rules)
	}
}

type accessFixture struct {
	r            *CloudflareTunnelReconciler
	tunnel       *cfg.CloudflareTunnel
	route        *gateway.HTTPRoute
	app          *cfg.CloudflareAccessApplication
	policy       *cfg.CloudflareAccessPolicy
	remoteApp    *cloudflare.AccessApplication
	remotePolicy *cloudflare.AccessPolicy
	remoteConfig *cloudflare.TunnelConfiguration
	otherApps    []cloudflare.AccessApplication
	puts         int
}

func newAccessFixture(t *testing.T) *accessFixture {
	t.Helper()
	r, tunnel, _ := lifecycleFixture(t)
	r.AccessLocks = NewAccessLocks()
	_, class, gw, route, service := emissionFixtures()
	gw.Namespace = tunnel.Namespace
	route.Namespace = tunnel.Namespace
	service.Namespace = tunnel.Namespace
	parentNS := gateway.Namespace(tunnel.Namespace)
	route.Spec.ParentRefs[0].Namespace = &parentNS
	route.Annotations = map[string]string{"cfgate.io/access-required": tunnel.Namespace + "/protection"}
	app := &cfg.CloudflareAccessApplication{ObjectMeta: metav1.ObjectMeta{Name: "protection", Namespace: tunnel.Namespace, UID: "app-uid", Generation: 1}, Spec: cfg.CloudflareAccessApplicationSpec{TargetRef: &cfg.PolicyTargetReference{Kind: "HTTPRoute", Name: route.Name}, PolicyRefs: []cfg.AccessPolicyReference{{Name: "allow"}}}}
	ready := []metav1.Condition{{Type: status.ConditionTypeReady, Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: 1, LastTransitionTime: metav1.Now()}}
	app.Status = cfg.CloudflareAccessApplicationStatus{AccountID: "account", ObservedGeneration: 1, Conditions: ready, Applications: []cfg.AccessApplicationObserved{{ID: "remote-app", Domain: "app.example.com"}}}
	policy := &cfg.CloudflareAccessPolicy{ObjectMeta: metav1.ObjectMeta{Name: "allow", Namespace: tunnel.Namespace, UID: "policy-uid", Generation: 1}, Spec: cfg.CloudflareAccessPolicySpec{Decision: "allow"}}
	policy.Status = cfg.CloudflareAccessPolicyStatus{AccountID: "account", PolicyID: "remote-policy", ObservedGeneration: 1, Conditions: ready}
	app.Status.OwnerID = fmt.Sprintf("%x", sha256.Sum256([]byte("installation-uid/"+string(app.UID))))[:28]
	for _, obj := range []client.Object{class, gw, route, service, app, policy, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "operator", UID: "installation-uid"}}} {
		if err := r.Create(context.Background(), obj); err != nil {
			t.Fatal(err)
		}
	}
	f := &accessFixture{r: r, tunnel: tunnel, route: route, app: app, policy: policy, remoteApp: &cloudflare.AccessApplication{Tags: []string{accessApplicationOwnerTag(app)}, ID: "remote-app", Type: "self_hosted", Domain: "app.example.com", Destinations: []string{"app.example.com"}, Policies: []cloudflare.ApplicationPolicyLink{{ID: "remote-policy"}}}, remotePolicy: &cloudflare.AccessPolicy{ID: "remote-policy", Decision: "allow", Include: []cloudflare.AccessRuleParam{{EmailDomain: ptr.To("example.com")}}}}
	mock := r.CFClient.(*cloudflare.MockClient)
	mock.GetAccessApplicationFunc = func(context.Context, string, string) (*cloudflare.AccessApplication, error) { return f.remoteApp, nil }
	mock.GetAccessPolicyFunc = func(context.Context, string, string) (*cloudflare.AccessPolicy, error) { return f.remotePolicy, nil }
	mock.ListAccessApplicationsFunc = func(context.Context, string) ([]cloudflare.AccessApplication, error) {
		apps := append([]cloudflare.AccessApplication(nil), f.otherApps...)
		if f.remoteApp != nil {
			apps = append(apps, *f.remoteApp)
		}
		return apps, nil
	}
	mock.GetTunnelConfigurationFunc = func(context.Context, string, string) (*cloudflare.TunnelConfiguration, error) {
		return f.remoteConfig, nil
	}
	mock.UpdateTunnelConfigurationFunc = func(_ context.Context, _, _ string, configuration cloudflare.TunnelConfiguration) error {
		f.puts++
		f.remoteConfig = &configuration
		return nil
	}
	return f
}
func (f *accessFixture) sync(t *testing.T) {
	t.Helper()
	if err := f.r.syncConfiguration(context.Background(), f.tunnel); err != nil {
		t.Fatal(err)
	}
}
func TestAccessRequiredRemoteVerification(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*testing.T, *accessFixture)
		allowed bool
	}{
		{"ready remote protection", func(*testing.T, *accessFixture) {}, true},
		{"missing remote app", func(_ *testing.T, f *accessFixture) { f.remoteApp = nil }, false},
		{"wrong remote host", func(_ *testing.T, f *accessFixture) { f.remoteApp.Domain = "other.example.com" }, false},
		{"partial remote path", func(_ *testing.T, f *accessFixture) { f.remoteApp.Domain = "app.example.com/private" }, false},
		{"OPTIONS bypass", func(_ *testing.T, f *accessFixture) { f.remoteApp.OptionsPreflightBypass = true }, false},
		{"destination override", func(_ *testing.T, f *accessFixture) { f.remoteApp.UnsupportedProtection = true }, false},
		{"changed policy attachment", func(_ *testing.T, f *accessFixture) { f.remoteApp.Policies[0].ID = "unknown" }, false},
		{"bypass policy", func(_ *testing.T, f *accessFixture) { f.remotePolicy.Decision = "bypass" }, false},
		{"allow everyone", func(_ *testing.T, f *accessFixture) {
			f.remotePolicy.Include = []cloudflare.AccessRuleParam{{Everyone: ptr.To(true)}}
		}, false},
		{"unrelated supported app", func(_ *testing.T, f *accessFixture) {
			f.otherApps = []cloudflare.AccessApplication{{ID: "unrelated", Type: "self_hosted", Domain: "other.example.com", Destinations: []string{"other.example.com"}}}
		}, true},
		{"unrelated opaque destination", func(_ *testing.T, f *accessFixture) {
			f.otherApps = []cloudflare.AccessApplication{{ID: "private", UnsupportedProtection: true}}
		}, false},
		{"path shadow", func(_ *testing.T, f *accessFixture) {
			f.otherApps = []cloudflare.AccessApplication{{ID: "shadow", Domain: "app.example.com/private"}}
		}, false},
		{"wildcard shadow", func(_ *testing.T, f *accessFixture) {
			f.otherApps = []cloudflare.AccessApplication{{ID: "shadow", Domain: "*.example.com"}}
		}, false},
		{"stale app", func(t *testing.T, f *accessFixture) {
			f.app.Generation++
			if err := f.r.Update(context.Background(), f.app); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"stale policy", func(t *testing.T, f *accessFixture) {
			f.policy.Generation++
			if err := f.r.Update(context.Background(), f.policy); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"wrong account", func(t *testing.T, f *accessFixture) {
			f.app.Status.AccountID = "other"
			if err := f.r.Status().Update(context.Background(), f.app); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"generic h2c preserved", func(t *testing.T, f *accessFixture) {
			f.route.Annotations["cfgate.io/origin-h2c"] = "true"
			if err := f.r.Update(context.Background(), f.route); err != nil {
				t.Fatal(err)
			}
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newAccessFixture(t)
			tt.mutate(t, f)
			f.sync(t)
			if f.remoteConfig == nil || len(f.remoteConfig.Ingress) < 2 {
				t.Fatal("no captured config PUT")
			}
			service := f.remoteConfig.Ingress[0].Service
			if tt.allowed != strings.HasPrefix(service, "http://") {
				t.Fatalf("allowed=%v actualservice=%s", tt.allowed, service)
			}
			if tt.name == "generic h2c preserved" && (f.remoteConfig.Ingress[0].OriginRequest == nil || !f.remoteConfig.Ingress[0].OriginRequest.H2cOrigin) {
				t.Fatal("h2c origin transport lost")
			}
			if !tt.allowed && service != "http_status:503" {
				t.Fatalf("unsafe failure %s", service)
			}
		})
	}
}
func TestAccessRequiredRemoteChangeWithdrawsDespiteUnchangedLocalHash(t *testing.T) {
	f := newAccessFixture(t)
	f.sync(t)
	f.remotePolicy.Decision = "bypass"
	f.sync(t)
	if f.puts != 2 || f.remoteConfig.Ingress[0].Service != "http_status:503" {
		t.Fatal("stale local hash retained protected backend")
	}
}
func TestAccessLocksCancelCleanUpAndOrder(t *testing.T) {
	locks := NewAccessLocks()
	release, err := locks.acquire(context.Background(), []string{"b", "a", "a"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := locks.acquire(ctx, []string{"a", "b"}); err == nil {
		t.Fatal("blocked acquire ignored cancellation")
	}
	unrelated, err := locks.acquire(context.Background(), []string{"c"})
	if err != nil {
		t.Fatal(err)
	}
	unrelated()
	release()
	locks.mu.Lock()
	size := len(locks.entries)
	locks.mu.Unlock()
	if size != 0 {
		t.Fatalf("registry retained %d keys", size)
	}
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			keys := []string{"a", "b"}
			if i == 1 {
				keys = []string{"b", "a"}
			}
			for j := 0; j < 10; j++ {
				release, err := locks.acquire(ctx, keys)
				if err != nil {
					done <- fmt.Errorf("worker%d: %w", i, err)
					return
				}
				release()
			}
			done <- nil
		}(i)
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestAccessRequiredCrossNamespaceGrants(t *testing.T) {
	f := newAccessFixture(t)
	if err := gatewaybeta.Install(f.r.Scheme); err != nil {
		t.Fatal(err)
	}
	app := f.app.DeepCopy()
	app.Namespace = "protection"
	app.ResourceVersion = ""
	app.UID = "other-app"
	app.Spec.TargetRef.Namespace = ptr.To(f.route.Namespace)
	app.Spec.PolicyRefs[0].Namespace = f.policy.Namespace
	if err := f.r.Create(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	f.route.Annotations["cfgate.io/access-required"] = "protection/protection"
	if err := f.r.Update(context.Background(), f.route); err != nil {
		t.Fatal(err)
	}
	f.sync(t)
	if f.remoteConfig.Ingress[0].Service != "http_status:503" {
		t.Fatal("ungranted cross-namespace app forwarded")
	}
	routeGrant := &gatewaybeta.ReferenceGrant{ObjectMeta: metav1.ObjectMeta{Name: "route-app", Namespace: app.Namespace}, Spec: gatewaybeta.ReferenceGrantSpec{From: []gatewaybeta.ReferenceGrantFrom{{Group: gateway.GroupName, Kind: "HTTPRoute", Namespace: gatewaybeta.Namespace(f.route.Namespace)}}, To: []gatewaybeta.ReferenceGrantTo{{Group: "cfgate.io", Kind: "CloudflareAccessApplication", Name: ptr.To(gatewaybeta.ObjectName(app.Name))}}}}
	appGrant := &gatewaybeta.ReferenceGrant{ObjectMeta: metav1.ObjectMeta{Name: "app-dependencies", Namespace: f.route.Namespace}, Spec: gatewaybeta.ReferenceGrantSpec{From: []gatewaybeta.ReferenceGrantFrom{{Group: "cfgate.io", Kind: "CloudflareAccessApplication", Namespace: gatewaybeta.Namespace(app.Namespace)}}, To: []gatewaybeta.ReferenceGrantTo{{Group: gateway.GroupName, Kind: "HTTPRoute", Name: ptr.To(gatewaybeta.ObjectName(f.route.Name))}, {Group: "cfgate.io", Kind: "CloudflareAccessPolicy", Name: ptr.To(gatewaybeta.ObjectName(f.policy.Name))}}}}
	for _, grant := range []*gatewaybeta.ReferenceGrant{routeGrant, appGrant} {
		if err := f.r.Create(context.Background(), grant); err != nil {
			t.Fatal(err)
		}
	}
	f.sync(t)
	if !strings.HasPrefix(f.remoteConfig.Ingress[0].Service, "http://") {
		t.Fatal("explicit scoped grants did not permit protected forwarding")
	}
	if err := f.r.Delete(context.Background(), routeGrant); err != nil {
		t.Fatal(err)
	}
	f.sync(t)
	if f.remoteConfig.Ingress[0].Service != "http_status:503" {
		t.Fatal("revoked app grant retained backend in captured PUT")
	}
}

func TestAccessRequiredRejectsDuplicatePolicyAttachment(t *testing.T) {
	f := newAccessFixture(t)
	other := f.policy.DeepCopy()
	other.Name = "second"
	other.UID = "second-policy"
	other.ResourceVersion = ""
	other.Status.PolicyID = "second-remote"
	if err := f.r.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	f.app.Spec.PolicyRefs = append(f.app.Spec.PolicyRefs, cfg.AccessPolicyReference{Name: other.Name})
	if err := f.r.Update(context.Background(), f.app); err != nil {
		t.Fatal(err)
	}
	f.remoteApp.Policies = append(f.remoteApp.Policies, f.remoteApp.Policies[0])
	f.r.CFClient.(*cloudflare.MockClient).GetAccessPolicyFunc = func(_ context.Context, _, id string) (*cloudflare.AccessPolicy, error) {
		p := *f.remotePolicy
		p.ID = id
		return &p, nil
	}
	f.sync(t)
	if f.remoteConfig.Ingress[0].Service != "http_status:503" {
		t.Fatal("duplicate remote policy hid absent expected attachment")
	}
}

func TestAccessRemoteReadsAreBoundedPerConfiguration(t *testing.T) {
	f := newAccessFixture(t)
	f.app.Spec.TargetRef = &cfg.PolicyTargetReference{Kind: "Gateway", Name: "gateway"}
	if err := f.r.Update(context.Background(), f.app); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		route := f.route.DeepCopy()
		route.Name = fmt.Sprintf("route-%d", i)
		route.ResourceVersion = ""
		route.UID = ""
		if err := f.r.Create(context.Background(), route); err != nil {
			t.Fatal(err)
		}
	}
	apps, policies, lists := 0, 0, 0
	mock := f.r.CFClient.(*cloudflare.MockClient)
	mock.GetAccessApplicationFunc = func(context.Context, string, string) (*cloudflare.AccessApplication, error) {
		apps++
		return f.remoteApp, nil
	}
	mock.GetAccessPolicyFunc = func(context.Context, string, string) (*cloudflare.AccessPolicy, error) {
		policies++
		return f.remotePolicy, nil
	}
	mock.ListAccessApplicationsFunc = func(context.Context, string) ([]cloudflare.AccessApplication, error) {
		lists++
		return []cloudflare.AccessApplication{*f.remoteApp}, nil
	}
	f.sync(t)
	if apps != 1 || policies != 1 || lists != 1 {
		t.Fatalf("shared app repeated remote reads: apps=%d policies=%d lists=%d", apps, policies, lists)
	}
	if f.remoteConfig.Ingress[0].Service == "http_status:503" {
		t.Fatal("read limit achieved by refusing valid routes")
	}
}

func TestAccessIsolationUnadmittedBudget(t *testing.T) {
	tunnel, class, gw, route, _ := emissionFixtures()
	route.Namespace = "untrusted"
	objects := []client.Object{tunnel, class, gw}
	for i := 0; i < 257; i++ {
		next := route.DeepCopy()
		next.Name = fmt.Sprintf("route-%d", i)
		next.Annotations = map[string]string{"cfgate.io/access-required": fmt.Sprintf("app/protection-%d", i)}
		objects = append(objects, next)
	}
	c := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithObjects(objects...).Build()
	r := &CloudflareTunnelReconciler{Client: c, APIReader: c, Recorder: &fakeEventRecorder{}}
	rules, _, err := r.collectIngressRules(context.Background(), tunnel, nil)
	if err != nil || len(rules) != 0 {
		t.Fatalf("denied routes unexpectedly emitted: %v %v", rules, err)
	}
	_, err = r.accessKeysForTunnel(context.Background(), tunnel)
	if err != nil {
		t.Fatalf("expected reproduced budget failure, got %v", err)
	}
}

func TestAccessIsolationForgedWithdrawal(t *testing.T) {
	f := newAccessFixture(t)
	ctx := context.Background()
	// No publication receipt; a public route already forwards the same hostname.
	if err := f.r.Delete(ctx, f.route); err != nil {
		t.Fatal(err)
	}
	forged := f.route.DeepCopy()
	forged.ResourceVersion = ""
	forged.UID = ""
	forged.Namespace = "untrusted"
	if err := f.r.Create(ctx, forged); err != nil {
		t.Fatal(err)
	}
	f.remoteConfig = &cloudflare.TunnelConfiguration{Ingress: []cloudflare.IngressRule{{Hostname: "app.example.com", Service: "http://public"}}}
	if len(f.tunnel.Status.AccessDependencies) != 0 {
		t.Fatal("unexpected receipt")
	}
	err := f.r.verifyAccessWithdrawal(ctx, []string{client.ObjectKeyFromObject(f.app).String()})
	if err != nil {
		t.Fatalf("expected forged withdrawal block: %v", err)
	}
}

func TestAccessIsolationDeniedAccessGrantReceipt(t *testing.T) {
	f := newAccessFixture(t)
	ctx := context.Background()
	if err := gatewaybeta.Install(f.r.Scheme); err != nil {
		t.Fatal(err)
	}
	other := f.app.DeepCopy()
	other.Namespace = "protection"
	other.ResourceVersion = ""
	other.UID = "foreign-app"
	if err := f.r.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	f.route.Annotations["cfgate.io/access-required"] = "protection/protection"
	if err := f.r.Update(ctx, f.route); err != nil {
		t.Fatal(err)
	}
	f.sync(t)
	if f.remoteConfig.Ingress[0].Service != "http_status:503" {
		t.Fatal("expected denied forwarding")
	}
	var current cfg.CloudflareTunnel
	if err := f.r.Get(ctx, client.ObjectKeyFromObject(f.tunnel), &current); err != nil {
		t.Fatal(err)
	}
	if len(current.Status.AccessDependencies) != 0 {
		t.Fatalf("unexpected deps: %+v", current.Status.AccessDependencies)
	}
}

func TestOverloadWithdrawsRevokedBackend(t *testing.T) {
	f := newAccessFixture(t)
	if err := gatewaybeta.Install(f.r.Scheme); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	delete(f.route.Annotations, "cfgate.io/access-required")
	f.route.Spec.Rules[0].BackendRefs[0].Namespace = ptr.To(gateway.Namespace("backend-ns"))
	if err := f.r.Update(ctx, f.route); err != nil {
		t.Fatal(err)
	}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: "backend-ns"}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}}}
	grant := &gatewaybeta.ReferenceGrant{ObjectMeta: metav1.ObjectMeta{Name: "permit", Namespace: "backend-ns"}, Spec: gatewaybeta.ReferenceGrantSpec{From: []gatewaybeta.ReferenceGrantFrom{{Group: gateway.GroupName, Kind: "HTTPRoute", Namespace: gatewaybeta.Namespace(f.route.Namespace)}}, To: []gatewaybeta.ReferenceGrantTo{{Kind: "Service"}}}}
	for _, obj := range []client.Object{service, grant} {
		if err := f.r.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	f.r.ClientSettings.MaxIngressRules = 2
	f.sync(t)
	puts := f.puts
	if err := f.r.Delete(ctx, grant); err != nil {
		t.Fatal(err)
	}
	extra := f.route.DeepCopy()
	extra.Name = "extra"
	extra.ResourceVersion = ""
	extra.UID = ""
	extra.Spec.Hostnames = []gateway.Hostname{"extra.example.com"}
	if err := f.r.Create(ctx, extra); err != nil {
		t.Fatal(err)
	}
	err := f.r.syncConfiguration(ctx, f.tunnel)
	if err == nil || !strings.Contains(err.Error(), "ingress rules") {
		t.Fatalf("expected overload: %v", err)
	}
	if f.puts != puts+1 || len(f.remoteConfig.Ingress) != 1 || f.remoteConfig.Ingress[0].Service != "http_status:503" {
		t.Fatal("revocation scenario not reproduced")
	}
	if len(f.tunnel.Status.AccessDependencies) != 0 {
		t.Fatal("confirmed withdrawal retained receipts")
	}
}

func TestOverloadWithdrawalPreservesReceiptsUntilConfirmed(t *testing.T) {
	for _, failure := range []string{"write", "readback", "none"} {
		t.Run(failure, func(t *testing.T) {
			f := newAccessFixture(t)
			f.sync(t)
			f.r.ClientSettings.MaxIngressRules = 1
			mock := f.r.CFClient.(*cloudflare.MockClient)
			if failure == "write" {
				mock.UpdateTunnelConfigurationFunc = func(context.Context, string, string, cloudflare.TunnelConfiguration) error {
					return fmt.Errorf("write failed")
				}
			}
			if failure == "readback" {
				mock.GetTunnelConfigurationFunc = func(context.Context, string, string) (*cloudflare.TunnelConfiguration, error) {
					return nil, fmt.Errorf("read failed")
				}
			}
			if err := f.r.syncConfiguration(context.Background(), f.tunnel); err == nil {
				t.Fatal("overload not reported")
			}
			var current cfg.CloudflareTunnel
			if err := f.r.Get(context.Background(), client.ObjectKeyFromObject(f.tunnel), &current); err != nil {
				t.Fatal(err)
			}
			if (len(current.Status.AccessDependencies) > 0) != (failure != "none") {
				t.Fatalf("receipt state after %s: %+v", failure, current.Status.AccessDependencies)
			}
			if failure == "none" {
				f.r.ClientSettings.MaxIngressRules = 1000
				f.sync(t)
				if !strings.HasPrefix(f.remoteConfig.Ingress[0].Service, "http://") {
					t.Fatal("recovery retained emergency denial")
				}
			}
		})
	}
}

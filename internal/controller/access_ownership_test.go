package controller

import (
	"context"
	"fmt"
	"github.com/go-logr/logr"
	"strings"
	"testing"

	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gateway "sigs.k8s.io/gateway-api/apis/v1"
)

func TestAccessMutationRequiresOwnership(t *testing.T) {
	for _, kind := range []string{"application", "policy", "token"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			mutations := 0
			mock := cloudflare.NewMockClient()
			mock.GetAccessApplicationFunc = func(context.Context, string, string) (*cloudflare.AccessApplication, error) {
				return &cloudflare.AccessApplication{ID: "remote", Tags: []string{"cfgate:foreign"}}, nil
			}
			mock.GetAccessPolicyFunc = func(context.Context, string, string) (*cloudflare.AccessPolicy, error) {
				return &cloudflare.AccessPolicy{ID: "remote", Name: "shared"}, nil
			}
			mock.GetServiceTokenFunc = func(context.Context, string, string) (*cloudflare.ServiceToken, error) {
				return &cloudflare.ServiceToken{ID: "remote", Name: "shared"}, nil
			}
			mock.DeleteAccessApplicationFunc = func(context.Context, string, string) error { mutations++; return nil }
			mock.DeleteAccessPolicyFunc = func(context.Context, string, string) error { mutations++; return nil }
			mock.DeleteServiceTokenFunc = func(context.Context, string, string) error { mutations++; return nil }
			mock.UpdateAccessApplicationFunc = func(context.Context, string, string, cloudflare.ApplicationParams) (*cloudflare.AccessApplication, error) {
				mutations++
				return nil, nil
			}
			mock.UpdateAccessPolicyFunc = func(context.Context, string, string, cloudflare.PolicyParams) (*cloudflare.AccessPolicy, error) {
				mutations++
				return nil, nil
			}
			mock.RotateServiceTokenFunc = func(context.Context, string, string) (*cloudflare.ServiceTokenWithSecret, error) {
				mutations++
				return nil, nil
			}
			kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).Build()
			owner := &cfg.CloudflareAccessPolicy{ObjectMeta: metav1.ObjectMeta{Name: "mine", Namespace: "app", UID: "mine"}}
			owned := &ownedAccessClient{Client: mock, kube: kube, reader: kube, installation: "operator", identity: "mine", owner: owner, legacy: map[string]bool{kind + "/remote": true}}
			var updateErr, deleteErr error
			switch kind {
			case "application":
				_, updateErr = owned.UpdateAccessApplication(ctx, "account", "remote", cloudflare.ApplicationParams{})
				deleteErr = owned.DeleteAccessApplication(ctx, "account", "remote")
			case "policy":
				_, updateErr = owned.UpdateAccessPolicy(ctx, "account", "remote", cloudflare.PolicyParams{})
				deleteErr = owned.DeleteAccessPolicy(ctx, "account", "remote")
			case "token":
				_, updateErr = owned.RotateServiceToken(ctx, "account", "remote")
				deleteErr = owned.DeleteServiceToken(ctx, "account", "remote")
			}
			if updateErr == nil || deleteErr == nil || mutations != 0 {
				t.Fatalf("foreign effects: update=%v delete=%v count=%d", updateErr, deleteErr, mutations)
			}
			owner.Annotations = map[string]string{adoptExistingAnnotation: "true"}
			if err := owned.verify(ctx, "account", kind, "remote"); err != nil {
				t.Fatal(err)
			}
			replacement := *owned
			replacement.identity = "replacement"
			if err := replacement.verify(ctx, "account", kind, "remote"); err == nil {
				t.Fatal("explicit adoption stole an existing claim")
			}
		})
	}
}

func TestAccessIdentitySeparatesInstallationsAndIncarnations(t *testing.T) {
	ctx := context.Background()
	kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "a", UID: "install-a"}}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "b", UID: "install-b"}}).Build()
	seen := map[string]bool{}
	for _, installation := range []string{"a", "b"} {
		for _, uid := range []types.UID{"old", "new"} {
			owner := &cfg.CloudflareAccessApplication{ObjectMeta: metav1.ObjectMeta{Name: "same", Namespace: "app", UID: uid}}
			id, err := accessOwnerIdentity(ctx, kube, installation, owner, "")
			if err != nil {
				t.Fatal(err)
			}
			name := ownedAccessName("shared", id)
			if seen[name] {
				t.Fatal("ownership identity collision")
			}
			seen[name] = true
			if _, err := accessOwnerIdentity(ctx, kube, installation, owner, "another"); err == nil {
				t.Fatal("changed installation identity accepted")
			}
		}
	}
	prefix := strings.Repeat("long", 40)
	if ownedAccessName(prefix+"a", "owner") == ownedAccessName(prefix+"b", "owner") {
		t.Fatal("long logical token names collide")
	}
}

type accessFaultClient struct {
	client.Client
	failStatus, failClaim bool
}

func (c *accessFaultClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if _, ok := obj.(*corev1.ConfigMap); ok && c.failClaim {
		return fmt.Errorf("injected lost claim write")
	}
	return c.Client.Create(ctx, obj, opts...)
}
func (c *accessFaultClient) Status() client.SubResourceWriter {
	return &accessFaultStatus{SubResourceWriter: c.Client.Status(), parent: c}
}

type accessFaultStatus struct {
	client.SubResourceWriter
	parent *accessFaultClient
}

func (w *accessFaultStatus) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	if app, ok := obj.(*cfg.CloudflareAccessApplication); ok && len(app.Status.PendingApplications) > 0 && w.parent.failStatus {
		return fmt.Errorf("injected lost status write")
	}
	return w.SubResourceWriter.Update(ctx, obj, opts...)
}

func TestApplicationDeletionRecoversPartialOperations(t *testing.T) {
	for _, failure := range []string{"second target", "status write", "claim write"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			app := appWithFinalizer("app", "protection")
			app.Spec.CloudflareRef = &cfg.CloudflareSecretRef{Name: "cf", AccountID: "account"}
			app.Spec.TargetRef = &cfg.PolicyTargetReference{Group: gateway.GroupName, Kind: "HTTPRoute", Name: "route"}
			app.Spec.PolicyRefs = []cfg.AccessPolicyReference{{Name: "policy"}}
			route := &gateway.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"}, Spec: gateway.HTTPRouteSpec{Hostnames: []gateway.Hostname{"a.example.com", "b.example.com"}}}
			policy := readyAccessPolicy("app", "policy", "account", "policy-id")
			remote := map[string]cloudflare.AccessApplication{}
			creates := 0
			mock := cloudflare.NewMockClient()
			mock.ListAccessApplicationsFunc = func(context.Context, string) ([]cloudflare.AccessApplication, error) {
				var out []cloudflare.AccessApplication
				for _, app := range remote {
					out = append(out, app)
				}
				return out, nil
			}
			mock.GetAccessApplicationFunc = func(_ context.Context, _, id string) (*cloudflare.AccessApplication, error) {
				app, ok := remote[id]
				if !ok {
					return nil, nil
				}
				return &app, nil
			}
			mock.CreateAccessApplicationFunc = func(_ context.Context, _ string, p cloudflare.ApplicationParams) (*cloudflare.AccessApplication, error) {
				creates++
				if creates == 2 {
					return nil, fmt.Errorf("injected second-target failure")
				}
				app := cloudflare.AccessApplication{ID: "created", Domain: p.Domain, Tags: p.Tags}
				remote[app.ID] = app
				return &app, nil
			}
			mock.DeleteAccessApplicationFunc = func(_ context.Context, _, id string) error { delete(remote, id); return nil }
			r := newAccessAppReconciler(t, mock, route, policy, app)
			fault := &accessFaultClient{Client: r.Client, failStatus: failure == "status write", failClaim: failure == "claim write"}
			r.Client = fault
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(app)}
			_, err := r.Reconcile(ctx, req)
			if failure == "status write" && err == nil {
				t.Fatal("status fault not reached")
			}
			if len(remote) != 1 {
				t.Fatalf("remote success not reached: %v", remote)
			}
			fault.failStatus = false
			fault.failClaim = false
			if err := r.Get(ctx, req.NamespacedName, app); err != nil {
				t.Fatal(err)
			}
			if failure != "second target" && len(app.Status.Applications) != 0 {
				t.Fatal("expected lost observation")
			}
			if err := r.Delete(ctx, app); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			if len(remote) != 0 {
				t.Fatal("owned remote application leaked after deletion")
			}
			var claims corev1.ConfigMapList
			if err := r.List(ctx, &claims); err != nil {
				t.Fatal(err)
			}
			if len(claims.Items) != 0 {
				t.Fatal("completed cleanup retained claims")
			}
		})
	}
}

func TestAccessRecoveryReplacesAbsentRecordedIDs(t *testing.T) {
	ctx := context.Background()
	app := appWithFinalizer("app", "protection")
	app.Status.AccountID = "account"
	app.Status.Applications = []cfg.AccessApplicationObserved{{ID: "absent", Domain: "app.example.com"}}
	mock := cloudflare.NewMockClient()
	r := newAccessAppReconciler(t, mock, app)
	remote := cloudflare.AccessApplication{ID: "replacement", Domain: "app.example.com", Tags: []string{accessApplicationOwnerTag(app)}}
	mock.ListAccessApplicationsFunc = func(context.Context, string) ([]cloudflare.AccessApplication, error) {
		return []cloudflare.AccessApplication{remote}, nil
	}
	mock.GetAccessApplicationFunc = func(_ context.Context, _, id string) (*cloudflare.AccessApplication, error) {
		if id == "absent" {
			return nil, nil
		}
		return &remote, nil
	}
	creds := &accessApplicationCredentials{Service: cloudflare.NewAccessService(mock, logr.Discard()), AccountID: "account"}
	if err := r.prepareOwnedApplications(ctx, app, creds); err != nil {
		t.Fatal(err)
	}
	if len(creds.Recovered) != 1 || creds.Recovered[0].ID != "replacement" {
		t.Fatalf("lost recovered application: %+v", creds.Recovered)
	}
	policy := baseAccessPolicy("app", "policy")
	policy.Status.PolicyID = "absent"
	policy.Status.ServiceTokenIDs = map[string]string{"token": "absent"}
	policy.Spec.ServiceTokens = []cfg.ServiceTokenConfig{{Name: "token"}}
	pr := newAccessPolicyReconciler(t, mock, policy)
	identity, err := accessOwnerIdentity(ctx, pr.Client, "operator", policy, "")
	if err != nil {
		t.Fatal(err)
	}
	policyRemote := cloudflare.AccessPolicy{ID: "replacement-policy", Name: ownedAccessName(policy.Spec.Name, identity)}
	tokenRemote := cloudflare.ServiceToken{ID: "replacement-token", Name: ownedAccessName("token", identity)}
	mock.ListAccessPoliciesFunc = func(context.Context, string) ([]cloudflare.AccessPolicy, error) {
		return []cloudflare.AccessPolicy{policyRemote}, nil
	}
	mock.GetAccessPolicyFunc = func(_ context.Context, _, id string) (*cloudflare.AccessPolicy, error) {
		if id == "absent" {
			return nil, nil
		}
		return &policyRemote, nil
	}
	mock.ListServiceTokensFunc = func(context.Context, string) ([]cloudflare.ServiceToken, error) {
		return []cloudflare.ServiceToken{tokenRemote}, nil
	}
	mock.GetServiceTokenFunc = func(_ context.Context, _, id string) (*cloudflare.ServiceToken, error) {
		if id == "absent" {
			return nil, nil
		}
		return &tokenRemote, nil
	}
	pc := &accessPolicyCredentials{Service: cloudflare.NewAccessService(mock, logr.Discard()), AccountID: "account-1"}
	if err := pr.prepareOwnedPolicy(ctx, policy, pc); err != nil {
		t.Fatal(err)
	}
	if policy.Status.PolicyID != policyRemote.ID || policy.Status.ServiceTokenIDs["token"] != tokenRemote.ID {
		t.Fatalf("lost recovered policy/token: %+v", policy.Status)
	}
}

func TestReplacementFailureRetainsPreviousProtection(t *testing.T) {
	ctx := context.Background()
	app := appWithFinalizer("app", "protection")
	app.Spec.CloudflareRef = &cfg.CloudflareSecretRef{Name: "cf", AccountID: "account"}
	app.Spec.TargetRef = &cfg.PolicyTargetReference{Group: gateway.GroupName, Kind: "HTTPRoute", Name: "route"}
	app.Spec.PolicyRefs = []cfg.AccessPolicyReference{{Name: "policy"}}
	app.Status.Applications = []cfg.AccessApplicationObserved{{ID: "old", Domain: "old.example.com"}}
	route := &gateway.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"}, Spec: gateway.HTTPRouteSpec{Hostnames: []gateway.Hostname{"new.example.com"}}}
	policy := readyAccessPolicy("app", "policy", "account", "policy-id")
	mock := cloudflare.NewMockClient()
	deleted := false
	r := newAccessAppReconciler(t, mock, route, policy, app)
	old := cloudflare.AccessApplication{ID: "old", Domain: "old.example.com", Tags: []string{accessApplicationOwnerTag(app)}}
	mock.ListAccessApplicationsFunc = func(context.Context, string) ([]cloudflare.AccessApplication, error) {
		return []cloudflare.AccessApplication{old}, nil
	}
	mock.GetAccessApplicationFunc = func(context.Context, string, string) (*cloudflare.AccessApplication, error) { return &old, nil }
	mock.CreateAccessApplicationFunc = func(context.Context, string, cloudflare.ApplicationParams) (*cloudflare.AccessApplication, error) {
		return nil, fmt.Errorf("replacement unavailable")
	}
	mock.DeleteAccessApplicationFunc = func(context.Context, string, string) error { deleted = true; return nil }
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(app)}); err != nil {
		t.Fatal(err)
	}
	if deleted {
		t.Fatal("old protection deleted before successful replacement")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(app), app); err != nil {
		t.Fatal(err)
	}
	if len(app.Status.Applications) != 1 || app.Status.Applications[0].ID != "old" {
		t.Fatal("previous protection receipt lost")
	}
}

func TestAccessTargetLimitPrecedesRemoteChanges(t *testing.T) {
	app := appWithFinalizer("app", "protection")
	objects := []client.Object{app}
	for i := 0; i < 5; i++ {
		route := &gateway.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("route-%d", i), Namespace: "app"}}
		for j := 0; j < 13; j++ {
			route.Spec.Hostnames = append(route.Spec.Hostnames, gateway.Hostname(fmt.Sprintf("host-%d-%d.example.com", i, j)))
		}
		objects = append(objects, route)
		app.Spec.TargetRefs = append(app.Spec.TargetRefs, cfg.PolicyTargetReference{Group: gateway.GroupName, Kind: "HTTPRoute", Name: route.Name})
	}
	r := newAccessAppReconciler(t, cloudflare.NewMockClient(), objects...)
	if _, _, err := r.resolveApplicationTargets(context.Background(), app); err == nil {
		t.Fatal("uncheckpointable target expansion accepted")
	}
}

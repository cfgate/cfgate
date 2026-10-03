package controller

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gateway "sigs.k8s.io/gateway-api/apis/v1"
)

func applicationReconcilerForFixture(f *accessFixture) *CloudflareAccessApplicationReconciler {
	return &CloudflareAccessApplicationReconciler{Client: f.r.Client, APIReader: f.r.APIReader, CFClient: f.r.CFClient, Scheme: f.r.Scheme, Recorder: &fakeEventRecorder{}, AccessLocks: f.r.AccessLocks, InstallationNamespace: f.r.InstallationNamespace}
}
func prepareApplicationDeletion(t *testing.T, f *accessFixture) {
	t.Helper()
	f.app.Finalizers = []string{accessApplicationFinalizer}
	f.app.Spec.CloudflareRef = &cfg.CloudflareSecretRef{Name: "credentials", AccountID: "account"}
	if err := f.r.Update(context.Background(), f.app); err != nil {
		t.Fatal(err)
	}
}
func TestAccessDeletionWaitsForInflightPublicationAndConfirmedWithdrawal(t *testing.T) {
	f := newAccessFixture(t)
	prepareApplicationDeletion(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan struct{})
	allow := make(chan struct{})
	published := make(chan error, 1)
	mock := f.r.CFClient.(*cloudflare.MockClient)
	mock.UpdateTunnelConfigurationFunc = func(ctx context.Context, _, _ string, config cloudflare.TunnelConfiguration) error {
		if f.puts == 0 {
			close(started)
			select {
			case <-allow:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		f.puts++
		f.remoteConfig = &config
		return nil
	}
	deleted := false
	mock.DeleteAccessApplicationFunc = func(context.Context, string, string) error {
		if f.remoteConfig == nil || f.remoteConfig.Ingress[0].Service != "http_status:503" {
			t.Error("Access DELETE occurred before confirmed blocking configuration")
		}
		deleted = true
		return nil
	}
	go func() { published <- f.r.syncConfiguration(ctx, f.tunnel) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := f.r.Delete(ctx, f.app); err != nil {
		t.Fatal(err)
	}
	appR := applicationReconcilerForFixture(f)
	deletionResult := make(chan error, 1)
	go func() {
		_, err := appR.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.app)})
		deletionResult <- err
	}()
	select {
	case err := <-deletionResult:
		t.Fatalf("deletion bypassed outstanding config PUT: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(allow)
	if err := <-published; err != nil {
		t.Fatal(err)
	}
	if err := <-deletionResult; err != nil {
		t.Fatal(err)
	}
	if deleted {
		t.Fatal("Access removed while backend remained published")
	}
	f.sync(t)
	if _, err := appR.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.app)}); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("Access deletion did not proceed after confirmed withdrawal")
	}
	var remaining cfg.CloudflareAccessApplication
	if err := f.r.Get(ctx, client.ObjectKeyFromObject(f.app), &remaining); !apierrors.IsNotFound(err) {
		t.Fatalf("finalizer not completed: %v", err)
	}
}
func TestAccessWithdrawalFailureRetainsProtectionAndDependencyIntent(t *testing.T) {
	f := newAccessFixture(t)
	prepareApplicationDeletion(t, f)
	f.sync(t)
	mock := f.r.CFClient.(*cloudflare.MockClient)
	mock.UpdateTunnelConfigurationFunc = func(context.Context, string, string, cloudflare.TunnelConfiguration) error {
		return errors.New("write unavailable")
	}
	if err := f.r.Delete(context.Background(), f.app); err != nil {
		t.Fatal(err)
	}
	if err := f.r.syncConfiguration(context.Background(), f.tunnel); err == nil {
		t.Fatal("failed withdrawal reported success")
	}
	var persisted cfg.CloudflareTunnel
	if err := f.r.Get(context.Background(), client.ObjectKeyFromObject(f.tunnel), &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.Status.AccessDependencies) != 1 {
		t.Fatal("failed PUT erased dependency intent")
	}
	deleted := false
	mock.DeleteAccessApplicationFunc = func(context.Context, string, string) error { deleted = true; return nil }
	if _, err := applicationReconcilerForFixture(f).Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.app)}); err != nil {
		t.Fatal(err)
	}
	var app cfg.CloudflareAccessApplication
	if err := f.r.Get(context.Background(), client.ObjectKeyFromObject(f.app), &app); err != nil {
		t.Fatal(err)
	}
	if deleted || len(app.Finalizers) == 0 {
		t.Fatal("failed withdrawal removed Access protection/finalizer")
	}
}
func TestAccessRemovedRouteRetainsDependencyUntilRemoteWithdrawal(t *testing.T) {
	f := newAccessFixture(t)
	prepareApplicationDeletion(t, f)
	f.sync(t)
	if err := f.r.Delete(context.Background(), f.route); err != nil {
		t.Fatal(err)
	}
	appR := applicationReconcilerForFixture(f)
	ctx, release, err := appR.beginApplicationMutation(context.Background(), f.app)
	if err != nil {
		t.Fatal(err)
	}
	err = guardAccessMutations(ctx, f.r.CFClient).DeleteAccessApplication(ctx, "account", "remote-app")
	release()
	if err == nil {
		t.Fatal("removing route hid still-published dependency")
	}
	f.sync(t)
	var current cfg.CloudflareTunnel
	if err := f.r.Get(context.Background(), client.ObjectKeyFromObject(f.tunnel), &current); err != nil {
		t.Fatal(err)
	}
	if len(current.Status.AccessDependencies) != 0 {
		t.Fatal("verified withdrawal retained obsolete dependency")
	}
	ctx, release, err = appR.beginApplicationMutation(context.Background(), f.app)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := guardAccessMutations(ctx, f.r.CFClient).DeleteAccessApplication(ctx, "account", "remote-app"); err != nil {
		t.Fatal(err)
	}
}
func TestSelectedPolicyMutationWaitsForWithdrawal(t *testing.T) {
	f := newAccessFixture(t)
	f.sync(t)
	f.policy.Generation++
	f.policy.Spec.Decision = "bypass"
	if err := f.r.Update(context.Background(), f.policy); err != nil {
		t.Fatal(err)
	}
	r := &CloudflareAccessPolicyReconciler{Client: f.r.Client, APIReader: f.r.APIReader, CFClient: f.r.CFClient, AccessLocks: f.r.AccessLocks, InstallationNamespace: f.r.InstallationNamespace}
	changed := false
	f.r.CFClient.(*cloudflare.MockClient).UpdateAccessPolicyFunc = func(context.Context, string, string, cloudflare.PolicyParams) (*cloudflare.AccessPolicy, error) {
		changed = true
		return f.remotePolicy, nil
	}
	attempt := func() error {
		ctx, release, err := r.beginPolicyMutation(context.Background(), f.policy)
		if err != nil {
			return err
		}
		defer release()
		_, err = guardAccessMutations(ctx, f.r.CFClient).UpdateAccessPolicy(ctx, "account", "remote-policy", cloudflare.PolicyParams{Decision: "bypass"})
		return err
	}
	if err := attempt(); err == nil || changed {
		t.Fatal("policy changed while required backend remained published")
	}
	f.sync(t)
	if err := attempt(); err != nil || !changed {
		t.Fatalf("policy mutation after withdrawal: changed=%v err=%v", changed, err)
	}
}
func TestAccessFailureKeepsMatchedDenialAndPublicRoutes(t *testing.T) {
	f := newAccessFixture(t)
	f.route.Spec.Rules[0].Matches = []gateway.HTTPRouteMatch{{Path: &gateway.HTTPPathMatch{Value: ptr.To("/private")}}}
	if err := f.r.Update(context.Background(), f.route); err != nil {
		t.Fatal(err)
	}
	public := f.route.DeepCopy()
	public.Name = "public"
	public.ResourceVersion = ""
	public.UID = ""
	public.Annotations = nil
	public.Spec.Rules[0].Matches = nil
	if err := f.r.Create(context.Background(), public); err != nil {
		t.Fatal(err)
	}
	f.remoteApp = nil
	f.sync(t)
	for path, private := range map[string]bool{"/private": true, "/private/nested": true, "/public": false} {
		matched := ""
		for _, rule := range f.remoteConfig.Ingress {
			if rule.Hostname == "app.example.com" && (rule.Path == "" || regexp.MustCompile(rule.Path).MatchString(path)) {
				matched = rule.Service
				break
			}
		}
		if private && matched != "http_status:503" {
			t.Fatalf("protected path %s fell through to %s", path, matched)
		}
		if !private && !strings.HasPrefix(matched, "http://") {
			t.Fatalf("public path lost forwarding: %s", matched)
		}
	}
}
func TestKnownGRPCDoesNotClaimAccessAuthentication(t *testing.T) {
	f := newAccessFixture(t)
	var service corev1.Service
	if err := f.r.Get(context.Background(), client.ObjectKey{Name: "backend", Namespace: f.tunnel.Namespace}, &service); err != nil {
		t.Fatal(err)
	}
	service.Spec.Ports[0].AppProtocol = ptr.To("grpc")
	if err := f.r.Update(context.Background(), &service); err != nil {
		t.Fatal(err)
	}
	f.sync(t)
	if f.remoteConfig.Ingress[0].Service != "http_status:503" {
		t.Fatal("known gRPC backend accepted Access dependency as authentication")
	}
}

func TestPolicyMutationRetainsRemovedReferenceAuthority(t *testing.T) {
	f := newAccessFixture(t)
	f.sync(t)
	f.app.Spec.PolicyRefs = nil
	f.app.Generation++
	if err := f.r.Update(context.Background(), f.app); err != nil {
		t.Fatal(err)
	}
	f.policy.Spec.Decision = "bypass"
	f.policy.Generation++
	if err := f.r.Update(context.Background(), f.policy); err != nil {
		t.Fatal(err)
	}
	r := &CloudflareAccessPolicyReconciler{Client: f.r.Client, APIReader: f.r.APIReader, CFClient: f.r.CFClient, AccessLocks: f.r.AccessLocks, InstallationNamespace: f.r.InstallationNamespace}
	writes := 0
	f.r.CFClient.(*cloudflare.MockClient).UpdateAccessPolicyFunc = func(context.Context, string, string, cloudflare.PolicyParams) (*cloudflare.AccessPolicy, error) {
		writes++
		return f.remotePolicy, nil
	}
	ctx, release, err := r.beginPolicyMutation(context.Background(), f.policy)
	if err != nil {
		t.Fatal(err)
	}
	_, err = guardAccessMutations(ctx, f.r.CFClient).UpdateAccessPolicy(ctx, "account", "remote-policy", cloudflare.PolicyParams{Decision: "bypass"})
	release()
	if err == nil || writes != 0 {
		t.Fatal("removed desired ref hid remote policy attachment")
	}
	f.sync(t)
	ctx, release, err = r.beginPolicyMutation(context.Background(), f.policy)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := guardAccessMutations(ctx, f.r.CFClient).UpdateAccessPolicy(ctx, "account", "remote-policy", cloudflare.PolicyParams{Decision: "bypass"}); err != nil {
		t.Fatal(err)
	}
}
func TestAccessReferenceChangeAfterLockIsDenied(t *testing.T) {
	f := newAccessFixture(t)
	session, release, err := f.r.beginAccessSync(context.Background(), f.tunnel)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	f.route.Annotations["cfgate.io/access-required"] = "default/replacement"
	if err := f.r.Update(context.Background(), f.route); err != nil {
		t.Fatal(err)
	}
	if err := f.r.requiredAccessAllows(context.Background(), session, f.tunnel, f.route, f.route.Spec.Hostnames); err == nil {
		t.Fatal("unlocked replacement application authorized publication")
	}
}
func TestCancelledPublicationRetainsUnconfirmedIntent(t *testing.T) {
	f := newAccessFixture(t)
	prepareApplicationDeletion(t, f)
	mock := f.r.CFClient.(*cloudflare.MockClient)
	started := make(chan struct{})
	mock.UpdateTunnelConfigurationFunc = func(ctx context.Context, _, _ string, _ cloudflare.TunnelConfiguration) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- f.r.syncConfiguration(ctx, f.tunnel) }()
	<-started
	cancel()
	if err := <-result; err == nil {
		t.Fatal("cancelled write reported success")
	}
	// An immediate GET with no config cannot resolve an uncertain in-flight server write.
	if err := f.r.Delete(context.Background(), f.app); err != nil {
		t.Fatal(err)
	}
	deleted := false
	mock.DeleteAccessApplicationFunc = func(context.Context, string, string) error { deleted = true; return nil }
	if _, err := applicationReconcilerForFixture(f).Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.app)}); err != nil {
		t.Fatal(err)
	}
	if deleted {
		t.Fatal("unconfirmed cancelled publication allowed protection deletion")
	}
	mock.UpdateTunnelConfigurationFunc = func(_ context.Context, _, _ string, config cloudflare.TunnelConfiguration) error {
		f.remoteConfig = &config
		return nil
	}
	f.sync(t)
	if _, err := applicationReconcilerForFixture(f).Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.app)}); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("confirmed retry did not unblock cleanup")
	}
}

func TestWrongAccountApplicationCanWithdrawAndDelete(t *testing.T) {
	f := newAccessFixture(t)
	prepareApplicationDeletion(t, f)
	f.app.Status.AccountID = "other"
	if err := f.r.Status().Update(context.Background(), f.app); err != nil {
		t.Fatal(err)
	}
	f.sync(t)
	if f.remoteConfig.Ingress[0].Service != "http_status:503" {
		t.Fatal("wrong-account protection forwarded")
	}
	if err := f.r.verifyAccessWithdrawal(context.Background(), []string{client.ObjectKeyFromObject(f.app).String()}); err != nil {
		t.Fatal(err)
	}
}

func TestAccessWithdrawalUsesOnlyAuthorizedOwnedTunnel(t *testing.T) {
	for _, failure := range []string{"foreign claim", "credential grant absent"} {
		t.Run(failure, func(t *testing.T) {
			f := newAccessFixture(t)
			f.sync(t)
			remoteReads := 0
			f.r.CFClient.(*cloudflare.MockClient).GetTunnelFunc = func(context.Context, string, string) (*cloudflare.Tunnel, error) {
				remoteReads++
				return nil, nil
			}
			f.r.CFClient.(*cloudflare.MockClient).GetTunnelConfigurationFunc = func(context.Context, string, string) (*cloudflare.TunnelConfiguration, error) {
				remoteReads++
				return f.remoteConfig, nil
			}
			switch failure {
			case "foreign claim":
				key, err := f.r.tunnelClaimKey("account", f.tunnel.Status.TunnelID)
				if err != nil {
					t.Fatal(err)
				}
				var claim corev1.ConfigMap
				if err := f.r.Get(context.Background(), key, &claim); err != nil {
					t.Fatal(err)
				}
				if err := f.r.Delete(context.Background(), &claim); err != nil {
					t.Fatal(err)
				}
				claim.ResourceVersion = ""
				claim.UID = ""
				claim.Data["ownerUID"] = "foreign"
				if err := f.r.Create(context.Background(), &claim); err != nil {
					t.Fatal(err)
				}
			case "credential grant absent":
				if err := f.r.Get(context.Background(), client.ObjectKeyFromObject(f.tunnel), f.tunnel); err != nil {
					t.Fatal(err)
				}
				f.tunnel.Spec.Cloudflare.SecretRef.Namespace = "restricted"
				if err := f.r.Update(context.Background(), f.tunnel); err != nil {
					t.Fatal(err)
				}
				f.r.CFClient = nil
			}
			if err := f.r.verifyAccessWithdrawal(context.Background(), []string{client.ObjectKeyFromObject(f.app).String()}); err == nil {
				t.Fatal("unauthorized tunnel observation permitted")
			}
			if remoteReads != 0 {
				t.Fatal("foreign/unauthorized tunnel queried externally")
			}
		})
	}
}

func TestAccessSyncRecoversDependenciesFromDirectReader(t *testing.T) {
	f := newAccessFixture(t)
	f.sync(t)
	stale := f.tunnel.DeepCopy()
	stale.Status.AccessDependencies = nil
	if err := f.r.Delete(context.Background(), f.route); err != nil {
		t.Fatal(err)
	}
	session, release, err := f.r.beginAccessSync(context.Background(), stale)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if session == nil {
		t.Fatal("stale empty informer status skipped persisted dependency lock")
	}
	if len(stale.Status.AccessDependencies) == 0 {
		t.Fatal("direct-reader receipts not recovered")
	}
}

type failProtocolRead struct {
	client.Reader
	serviceReads int
}

func (r *failProtocolRead) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*corev1.Service); ok {
		r.serviceReads++
		if r.serviceReads > 1 {
			return errors.New("transient protocol read failure")
		}
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}
func TestAccessProtocolReadFailureDeniesActualPublication(t *testing.T) {
	f := newAccessFixture(t)
	f.r.APIReader = &failProtocolRead{Reader: f.r.APIReader}
	f.sync(t)
	if f.remoteConfig.Ingress[0].Service != "http_status:503" {
		t.Fatal("failed protocol verification retained backend")
	}
}

func TestAccessReplacementCreationWaitsForConfirmedWithdrawal(t *testing.T) {
	f := newAccessFixture(t)
	f.sync(t)
	f.app.Spec.Application.Path = "/restricted"
	f.app.Generation++
	if err := f.r.Update(context.Background(), f.app); err != nil {
		t.Fatal(err)
	}
	creates, deletes := 0, 0
	mock := f.r.CFClient.(*cloudflare.MockClient)
	mock.CreateAccessApplicationFunc = func(_ context.Context, _ string, params cloudflare.ApplicationParams) (*cloudflare.AccessApplication, error) {
		creates++
		if f.remoteConfig.Ingress[0].Service != "http_status:503" {
			t.Error("replacement created before withdrawal")
		}
		return &cloudflare.AccessApplication{ID: "replacement", Domain: params.Domain}, nil
	}
	mock.DeleteAccessApplicationFunc = func(context.Context, string, string) error {
		deletes++
		if f.remoteConfig.Ingress[0].Service != "http_status:503" {
			t.Error("stale protection removed before withdrawal")
		}
		return nil
	}
	appR := applicationReconcilerForFixture(f)
	params := cloudflare.ApplicationParams{Domain: "app.example.com/restricted", Type: "self_hosted"}
	replace := func() error {
		ctx, release, err := appR.beginApplicationMutation(context.Background(), f.app)
		if err != nil {
			return err
		}
		defer release()
		service := cloudflare.NewAccessService(guardAccessMutations(ctx, mock), logr.Discard())
		if _, err := service.EnsureApplicationByIDOrTags(ctx, "account", "", params); err != nil {
			return err
		}
		return deleteStaleAccessApplications(ctx, service, "account", f.app.Status.Applications, map[string]struct{}{params.Domain: {}})
	}
	if err := replace(); err == nil || creates != 0 || deletes != 0 {
		t.Fatalf("replacement bypassed forwarding: creates=%d deletes=%d err=%v", creates, deletes, err)
	}
	f.sync(t)
	if err := replace(); err != nil {
		t.Fatal(err)
	}
	if creates != 1 || deletes != 1 {
		t.Fatalf("replacement did not finish: creates=%d deletes=%d", creates, deletes)
	}
}

func TestAccessDefaultBackendPortPreservesProtocolDenial(t *testing.T) {
	f := newAccessFixture(t)
	f.route.Spec.Rules[0].BackendRefs[0].Port = nil
	if err := f.r.Update(context.Background(), f.route); err != nil {
		t.Fatal(err)
	}
	var service corev1.Service
	if err := f.r.Get(context.Background(), client.ObjectKey{Name: "backend", Namespace: f.tunnel.Namespace}, &service); err != nil {
		t.Fatal(err)
	}
	service.Spec.Ports[0].Port = 80
	service.Spec.Ports[0].AppProtocol = ptr.To("grpc")
	if err := f.r.Update(context.Background(), &service); err != nil {
		t.Fatal(err)
	}
	f.sync(t)
	if f.remoteConfig.Ingress[0].Service != "http_status:503" {
		t.Fatal("legacy default-port backend bypassed known protocol denial")
	}
}

func TestAccessDenialPrecedesOlderEqualPublicRoute(t *testing.T) {
	f := newAccessFixture(t)
	public := f.route.DeepCopy()
	public.Name = "older-public"
	public.ResourceVersion = ""
	public.UID = ""
	public.Annotations = nil
	public.CreationTimestamp = metav1.NewTime(time.Unix(1, 0))
	if err := f.r.Create(context.Background(), public); err != nil {
		t.Fatal(err)
	}
	f.route.CreationTimestamp = metav1.NewTime(time.Unix(2, 0))
	if err := f.r.Update(context.Background(), f.route); err != nil {
		t.Fatal(err)
	}
	f.remoteApp = nil
	f.sync(t)
	if f.remoteConfig.Ingress[0].Service != "http_status:503" {
		t.Fatalf("equal public route bypassed denial: %+v", f.remoteConfig.Ingress)
	}
}

func TestSelectedPolicyTokenMutationsWaitForWithdrawal(t *testing.T) {
	for _, operation := range []string{"delete", "rotate"} {
		t.Run(operation, func(t *testing.T) {
			f := newAccessFixture(t)
			f.sync(t)
			f.policy.Generation++
			if err := f.r.Update(context.Background(), f.policy); err != nil {
				t.Fatal(err)
			}
			r := &CloudflareAccessPolicyReconciler{Client: f.r.Client, APIReader: f.r.APIReader, CFClient: f.r.CFClient, AccessLocks: f.r.AccessLocks, InstallationNamespace: f.r.InstallationNamespace}
			changed := false
			mock := f.r.CFClient.(*cloudflare.MockClient)
			mock.DeleteServiceTokenFunc = func(context.Context, string, string) error { changed = true; return nil }
			mock.RotateServiceTokenFunc = func(context.Context, string, string, cloudflare.ServiceTokenRotateParams) (*cloudflare.ServiceTokenWithSecret, error) {
				changed = true
				return &cloudflare.ServiceTokenWithSecret{}, nil
			}
			attempt := func() error {
				ctx, release, err := r.beginPolicyMutation(context.Background(), f.policy)
				if err != nil {
					return err
				}
				defer release()
				guarded := guardAccessMutations(ctx, mock)
				if operation == "delete" {
					return guarded.DeleteServiceToken(ctx, "account", "token")
				}
				_, err = guarded.RotateServiceToken(ctx, "account", "token", cloudflare.ServiceTokenRotateParams{})
				return err
			}
			if err := attempt(); err == nil || changed {
				t.Fatal("token mutated before forwarding withdrawal")
			}
			f.sync(t)
			if err := attempt(); err != nil || !changed {
				t.Fatalf("token mutation after withdrawal: changed=%v err=%v", changed, err)
			}
		})
	}
}

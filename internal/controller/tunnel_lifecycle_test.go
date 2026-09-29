package controller

import (
	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/cloudflared"
	"cfgate.io/cfgate/internal/controller/status"
	"context"
	"errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"reflect"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	gateway "sigs.k8s.io/gateway-api/apis/v1"
	"strings"
	"testing"
	"time"
)

func lifecycleFixture(t *testing.T) (*CloudflareTunnelReconciler, *cfg.CloudflareTunnel, *int) {
	t.Helper()
	scheme := controllerTestScheme(t)
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	now := metav1.NewTime(time.Now().Add(-time.Minute))
	tunnel := &cfg.CloudflareTunnel{ObjectMeta: metav1.ObjectMeta{Name: "edge", Namespace: "default", UID: types.UID("tunnel-uid"), Generation: 1, Finalizers: []string{tunnelFinalizer}}, Spec: cfg.CloudflareTunnelSpec{Tunnel: cfg.TunnelIdentity{Name: "edge"}, Cloudflare: cfg.CloudflareConfig{AccountID: "account", SecretRef: cfg.SecretRef{Name: "credentials"}}}, Status: cfg.CloudflareTunnelStatus{TunnelID: "remote-id", ObservedGeneration: 1, LastSyncTime: &now, Conditions: []metav1.Condition{{Type: status.ConditionTypeReady, Status: metav1.ConditionTrue, Reason: status.ReasonTunnelOperational, LastTransitionTime: now}}}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "default", UID: "credentials-uid"}, Data: map[string][]byte{"CLOUDFLARE_API_TOKEN": []byte("test-token")}}
	tokenSecret := cloudflared.NewBuilder().BuildTokenSecret(tunnel, "connector-token")
	deployment := cloudflared.NewBuilder().BuildDeployment(tunnel, "connector-token")
	if err := controllerutil.SetControllerReference(tunnel, tokenSecret, scheme); err != nil {
		t.Fatal(err)
	}
	if err := controllerutil.SetControllerReference(tunnel, deployment, scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&cfg.CloudflareTunnel{}, &appsv1.Deployment{}).WithObjects(tunnel, secret, tokenSecret, deployment).Build()
	validations := new(int)
	mock := cloudflare.NewMockClient()
	mock.ValidateTokenFunc = func(context.Context, string) error { *validations++; return nil }
	mock.GetTunnelByNameFunc = func(context.Context, string, string) (*cloudflare.Tunnel, error) {
		return &cloudflare.Tunnel{ID: "remote-id", Name: "edge"}, nil
	}
	mock.GetTunnelTokenFunc = func(context.Context, string, string) (string, error) { return "connector-token", nil }
	mock.GetTunnelFunc = func(context.Context, string, string) (*cloudflare.Tunnel, error) {
		return &cloudflare.Tunnel{ID: "remote-id", Name: "edge", AccountTag: "account"}, nil
	}
	r := &CloudflareTunnelReconciler{Client: c, APIReader: c, Scheme: scheme, Recorder: &fakeEventRecorder{}, CFClient: mock}
	installTunnelClaimForTest(t, r, tunnel)
	return r, tunnel, validations
}

func TestLifecycleMissingDeploymentRepair(t *testing.T) {
	r, tunnel, _ := lifecycleFixture(t)
	ctx := context.Background()
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: cloudflared.DeploymentName(tunnel.Name), Namespace: tunnel.Namespace}}
	if err := r.Delete(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(tunnel)}); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
		t.Fatalf("deleted connector was not repaired immediately: %v", err)
	}
}

func reconcileLifecycle(t *testing.T, r *CloudflareTunnelReconciler, tunnel *cfg.CloudflareTunnel) {
	t.Helper()
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(tunnel)}); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(tunnel), tunnel); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleClockIgnoresConfigurationAndReplicaChurn(t *testing.T) {
	r, tunnel, calls := lifecycleFixture(t)
	ctx := context.Background()
	reconcileLifecycle(t, r, tunnel)
	if *calls != 1 || tunnel.Status.LastFullReconcileTime == nil || tunnel.Status.LifecycleDependencyHash == "" {
		t.Fatalf("legacy status did not trigger full lifecycle: calls=%d status=%+v", *calls, tunnel.Status)
	}
	oldFull := metav1.NewTime(time.Now().Add(-20 * time.Minute))
	tunnel.Status.LastFullReconcileTime = &oldFull
	if err := r.Status().Update(ctx, tunnel); err != nil {
		t.Fatal(err)
	}
	for i := int32(1); i <= 3; i++ {
		var d appsv1.Deployment
		if err := r.Get(ctx, client.ObjectKey{Namespace: tunnel.Namespace, Name: cloudflared.DeploymentName(tunnel.Name)}, &d); err != nil {
			t.Fatal(err)
		}
		d.Status.ReadyReplicas = i
		if err := r.Status().Update(ctx, &d); err != nil {
			t.Fatal(err)
		}
		reconcileLifecycle(t, r, tunnel)
		if *calls != 1 {
			t.Fatalf("status-only update forced lifecycle: calls=%d", *calls)
		}
		if time.Since(tunnel.Status.LastFullReconcileTime.Time) < 19*time.Minute {
			t.Fatal("configuration pass advanced full lifecycle clock")
		}
	}
	expired := metav1.NewTime(time.Now().Add(-31 * time.Minute))
	tunnel.Status.LastFullReconcileTime = &expired
	if err := r.Status().Update(ctx, tunnel); err != nil {
		t.Fatal(err)
	}
	reconcileLifecycle(t, r, tunnel)
	if *calls != 2 || time.Since(tunnel.Status.LastFullReconcileTime.Time) > time.Minute {
		t.Fatal("recent config sync postponed expired full lifecycle")
	}
}

func TestLifecycleDependenciesInvalidateFastPath(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, *CloudflareTunnelReconciler, *cfg.CloudflareTunnel)
	}{
		{"credential revision", func(t *testing.T, r *CloudflareTunnelReconciler, tunnel *cfg.CloudflareTunnel) {
			var s corev1.Secret
			key := client.ObjectKey{Namespace: tunnel.Namespace, Name: "credentials"}
			if err := r.Get(context.Background(), key, &s); err != nil {
				t.Fatal(err)
			}
			s.Data["CLOUDFLARE_API_TOKEN"] = []byte("rotated")
			if err := r.Update(context.Background(), &s); err != nil {
				t.Fatal(err)
			}
		}},
		{"token revision", func(t *testing.T, r *CloudflareTunnelReconciler, tunnel *cfg.CloudflareTunnel) {
			var s corev1.Secret
			key := client.ObjectKey{Namespace: tunnel.Namespace, Name: cloudflared.TokenSecretName(tunnel.Name)}
			if err := r.Get(context.Background(), key, &s); err != nil {
				t.Fatal(err)
			}
			s.Data = map[string][]byte{"token": []byte("rotated")}
			if err := r.Update(context.Background(), &s); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing token", func(t *testing.T, r *CloudflareTunnelReconciler, tunnel *cfg.CloudflareTunnel) {
			s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: tunnel.Namespace, Name: cloudflared.TokenSecretName(tunnel.Name)}}
			if err := r.Delete(context.Background(), s); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing deployment", func(t *testing.T, r *CloudflareTunnelReconciler, tunnel *cfg.CloudflareTunnel) {
			d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: tunnel.Namespace, Name: cloudflared.DeploymentName(tunnel.Name)}}
			if err := r.Delete(context.Background(), d); err != nil {
				t.Fatal(err)
			}
		}},
		{"deployment generation", func(t *testing.T, r *CloudflareTunnelReconciler, tunnel *cfg.CloudflareTunnel) {
			var d appsv1.Deployment
			key := client.ObjectKey{Namespace: tunnel.Namespace, Name: cloudflared.DeploymentName(tunnel.Name)}
			if err := r.Get(context.Background(), key, &d); err != nil {
				t.Fatal(err)
			}
			d.Generation++
			d.Spec.Template.Spec.Containers[0].Image = "unexpected-image"
			if err := r.Update(context.Background(), &d); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			r, tunnel, calls := lifecycleFixture(t)
			reconcileLifecycle(t, r, tunnel)
			reconcileLifecycle(t, r, tunnel)
			if *calls != 1 {
				t.Fatal("unchanged lifecycle failed to skip")
			}
			tt.mutate(t, r, tunnel)
			reconcileLifecycle(t, r, tunnel)
			if *calls != 2 {
				t.Fatalf("dependency change failed to force lifecycle: %d", *calls)
			}
			var d appsv1.Deployment
			if err := r.Get(context.Background(), client.ObjectKey{Namespace: tunnel.Namespace, Name: cloudflared.DeploymentName(tunnel.Name)}, &d); err != nil {
				t.Fatal(err)
			}
			if d.Spec.Template.Spec.Containers[0].Image == "unexpected-image" {
				t.Fatal("deployment drift not repaired")
			}
			var secret corev1.Secret
			if err := r.Get(context.Background(), client.ObjectKey{Namespace: tunnel.Namespace, Name: cloudflared.TokenSecretName(tunnel.Name)}, &secret); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type lifecycleReadFailure struct {
	client.Client
	err error
}

func (c lifecycleReadFailure) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if key.Name == "credentials" {
		return c.err
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func TestLifecycleReadAndAPIFailuresDoNotAdvanceClock(t *testing.T) {
	r, tunnel, calls := lifecycleFixture(t)
	reconcileLifecycle(t, r, tunnel)
	before := tunnel.Status.LastFullReconcileTime.DeepCopy()
	sentinel := errors.New("read interrupted")
	base := r.Client
	r.Client = lifecycleReadFailure{Client: base, err: sentinel}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(tunnel)}); !errors.Is(err, sentinel) {
		t.Fatalf("error=%v", err)
	}
	if *calls != 1 {
		t.Fatal("API mutation attempted despite dependency read failure")
	}
	r.Client = base
	r.CFClient.(*cloudflare.MockClient).ValidateTokenFunc = func(context.Context, string) error { return errors.New("temporary API failure") }
	tunnel.Generation++
	if err := r.Update(context.Background(), tunnel); err != nil {
		t.Fatal(err)
	}
	reconcileLifecycle(t, r, tunnel)
	if !tunnel.Status.LastFullReconcileTime.Equal(before) {
		t.Fatal("failed full reconciliation advanced lifecycle clock")
	}
}

func TestLifecycleFingerprintTracksCredentialSelectionAndCA(t *testing.T) {
	r, tunnel, _ := lifecycleFixture(t)
	ctx := context.Background()
	first, err := r.lifecycleDependencyHash(ctx, r.Client, tunnel)
	if err != nil {
		t.Fatal(err)
	}
	tunnel.Spec.Cloudflare.SecretKeys.APIToken = "other-token"
	second, err := r.lifecycleDependencyHash(ctx, r.Client, tunnel)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("selected token key missing from dependency identity")
	}
	tunnel.Spec.OriginDefaults.CAPoolSecretRef = &cfg.CAPoolSecretRef{Name: "ca"}
	if h, err := r.lifecycleDependencyHash(ctx, r.Client, tunnel); err != nil || h != "" {
		t.Fatalf("missing CA permits shortcut: %q %v", h, err)
	}
	ca := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "ca", Namespace: tunnel.Namespace}, Data: map[string][]byte{"ca.crt": []byte("bundle")}}
	if err := r.Create(ctx, ca); err != nil {
		t.Fatal(err)
	}
	withCA, err := r.lifecycleDependencyHash(ctx, r.Client, tunnel)
	if err != nil {
		t.Fatal(err)
	}
	ca.Data["ca.crt"] = []byte("new bundle")
	if err := r.Update(ctx, ca); err != nil {
		t.Fatal(err)
	}
	changedCA, err := r.lifecycleDependencyHash(ctx, r.Client, tunnel)
	if err != nil {
		t.Fatal(err)
	}
	if changedCA == withCA {
		t.Fatal("CA revision missing from dependency identity")
	}
}

func TestTokenRevisionRollsConnectorOnlyAfterSecretChange(t *testing.T) {
	r, tunnel, _ := lifecycleFixture(t)
	ctx := context.Background()
	token := "initial-token"
	r.CFClient.(*cloudflare.MockClient).GetTunnelTokenFunc = func(context.Context, string, string) (string, error) { return token, nil }
	template := func() corev1.PodTemplateSpec {
		t.Helper()
		var d appsv1.Deployment
		if err := r.Get(ctx, client.ObjectKey{Namespace: tunnel.Namespace, Name: cloudflared.DeploymentName(tunnel.Name)}, &d); err != nil {
			t.Fatal(err)
		}
		return d.Spec.Template
	}
	if err := r.deployCloudflared(ctx, tunnel); err != nil {
		t.Fatal(err)
	}
	first := template()
	if err := r.deployCloudflared(ctx, tunnel); err != nil {
		t.Fatal(err)
	}
	second := template()
	if !reflect.DeepEqual(first, second) {
		t.Fatal("unchanged token triggered rollout")
	}
	token = "rotated-connector-token"
	if err := r.deployCloudflared(ctx, tunnel); err != nil {
		t.Fatal(err)
	}
	rotated := template()
	if reflect.DeepEqual(first, rotated) {
		t.Fatal("token rotation did not alter pod template")
	}
	marker := rotated.Annotations["cfgate.io/tunnel-token-revision"]
	if marker == "" || strings.Contains(marker, token) {
		t.Fatal("revision marker missing or discloses token")
	}
	old := r.Client
	r.Client = lifecycleSecretWriteFailure{Client: old}
	token = "failed-rotation"
	if err := r.deployCloudflared(ctx, tunnel); err == nil {
		t.Fatal("token write failure ignored")
	}
	r.Client = old
	if !reflect.DeepEqual(rotated, template()) {
		t.Fatal("failed token write rolled connector")
	}
}

type lifecycleSecretWriteFailure struct{ client.Client }

func (c lifecycleSecretWriteFailure) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if _, ok := obj.(*corev1.Secret); ok {
		return errors.New("secret write failed")
	}
	return c.Client.Update(ctx, obj, opts...)
}

func TestConnectorReadinessRequiresCurrentCompleteRollout(t *testing.T) {
	for _, tt := range []struct {
		name                             string
		observed                         int64
		ready, available, updated, total int32
		want                             bool
	}{
		{"none", 2, 0, 0, 2, 2, false}, {"partial", 2, 1, 1, 2, 2, false}, {"not available", 2, 2, 1, 2, 2, false}, {"stale generation", 1, 2, 2, 2, 2, false}, {"old healthy replicas", 2, 2, 2, 0, 2, false}, {"surged new replicas pending", 2, 2, 2, 2, 4, false}, {"complete", 2, 2, 2, 2, 2, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, tunnel, _ := lifecycleFixture(t)
			deployment := cloudflared.NewBuilder().BuildDeployment(tunnel, "token")
			deployment.Generation = 2
			deployment.Status = appsv1.DeploymentStatus{ObservedGeneration: tt.observed, ReadyReplicas: tt.ready, AvailableReplicas: tt.available, UpdatedReplicas: tt.updated, Replicas: tt.total}
			for _, typ := range []string{status.ConditionTypeCredentialsValid, status.ConditionTypeTunnelReady, status.ConditionTypeConfigurationSynced} {
				r.setCondition(tunnel, typ, metav1.ConditionTrue, "Ready", "ready")
			}
			r.observeConnectorDeployment(tunnel, deployment)
			r.updateTunnelReadiness(tunnel)
			if got := isTunnelHealthy(tunnel); got != tt.want {
				t.Fatalf("Ready=%v want%v", got, tt.want)
			}
		})
	}
}

func TestTunnelDeletionDrainsBeforeRemoteMutation(t *testing.T) {
	r, tunnel, _ := lifecycleFixture(t)
	ctx := context.Background()
	remoteDeletes := 0
	r.CFClient.(*cloudflare.MockClient).DeleteTunnelFunc = func(context.Context, string, string) error { remoteDeletes++; return nil }
	r.CFClient.(*cloudflare.MockClient).GetTunnelFunc = func(context.Context, string, string) (*cloudflare.Tunnel, error) {
		if remoteDeletes > 0 {
			return nil, nil
		}
		return &cloudflare.Tunnel{ID: "remote-id", Name: "edge"}, nil
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "connector", Namespace: tunnel.Namespace, Labels: cloudflared.Selector(tunnel.Name)}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if err := r.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if _, err := r.reconcileDelete(ctx, tunnel); err != nil {
		t.Fatal(err)
	}
	var d appsv1.Deployment
	if err := r.Get(ctx, client.ObjectKey{Namespace: tunnel.Namespace, Name: cloudflared.DeploymentName(tunnel.Name)}, &d); err != nil {
		t.Fatal(err)
	}
	if d.Spec.Replicas == nil || *d.Spec.Replicas != 0 || remoteDeletes != 0 {
		t.Fatal("remote deleted before deployment scaled down")
	}
	if _, err := r.reconcileDelete(ctx, tunnel); err != nil {
		t.Fatal(err)
	}
	if remoteDeletes != 0 {
		t.Fatal("remote deleted while connector Pod running")
	}
	if err := r.Delete(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if _, err := r.reconcileDelete(ctx, tunnel); err != nil {
		t.Fatal(err)
	}
	if remoteDeletes != 1 {
		t.Fatalf("remote delete calls=%d", remoteDeletes)
	}
	var stored cfg.CloudflareTunnel
	if err := r.Get(ctx, client.ObjectKeyFromObject(tunnel), &stored); err != nil {
		t.Fatal(err)
	}
	for _, finalizer := range stored.Finalizers {
		if finalizer == tunnelFinalizer {
			t.Fatal("confirmed remote deletion retained finalizer")
		}
	}
	if err := r.verifyTunnelClaim(ctx, tunnel, "account"); err == nil {
		t.Fatal("confirmed remote deletion retained ownership claim")
	}

}

func TestTunnelDrainRejectsForeignDeployment(t *testing.T) {
	r, tunnel, _ := lifecycleFixture(t)
	ctx := context.Background()
	var d appsv1.Deployment
	if err := r.Get(ctx, client.ObjectKey{Namespace: tunnel.Namespace, Name: cloudflared.DeploymentName(tunnel.Name)}, &d); err != nil {
		t.Fatal(err)
	}
	d.OwnerReferences[0].UID = "foreign-owner"
	if err := r.Update(ctx, &d); err != nil {
		t.Fatal(err)
	}
	if _, err := r.drainConnector(ctx, tunnel); err == nil {
		t.Fatal("foreign connector scaled")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(&d), &d); err != nil {
		t.Fatal(err)
	}
	if *d.Spec.Replicas == 0 {
		t.Fatal("foreign deployment mutated")
	}
}

func TestBackendCustomClusterDomain(t *testing.T) {
	port := gateway.PortNumber(8080)
	route := &gateway.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Namespace: "app"}, Spec: gateway.HTTPRouteSpec{Hostnames: []gateway.Hostname{"app.example.test"}, Rules: []gateway.HTTPRouteRule{{BackendRefs: []gateway.HTTPBackendRef{{BackendRef: gateway.BackendRef{BackendObjectReference: gateway.BackendObjectReference{Name: "backend", Port: &port}}}}}}}}
	r := &CloudflareTunnelReconciler{ClusterDomain: "corp.internal"}
	rules, err := r.buildRulesFromHTTPRoute(route)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) == 1 {
		t.Logf("generatedServiceURL=%s", rules[0].Service)
	}
	if len(rules) != 1 || rules[0].Service != "http://backend.app.svc.corp.internal:8080" {
		t.Fatalf("rendered backend=%+v", rules)
	}
}

func TestRemoteConfigurationDriftAndReplacement(t *testing.T) {
	r, tunnel, _ := lifecycleFixture(t)
	ctx := context.Background()
	calls := 0
	reads := 0
	var remote *cloudflare.TunnelConfiguration
	mock := r.CFClient.(*cloudflare.MockClient)
	mock.UpdateTunnelConfigurationFunc = func(_ context.Context, _, _ string, config cloudflare.TunnelConfiguration) error {
		calls++
		remote = &config
		return nil
	}
	mock.GetTunnelConfigurationFunc = func(context.Context, string, string) (*cloudflare.TunnelConfiguration, error) {
		reads++
		return remote, nil
	}
	reconcileLifecycle(t, r, tunnel)
	reconcileLifecycle(t, r, tunnel)
	if calls != 1 || reads != 1 {
		t.Fatalf("initial calls/reads=%d/%d", calls, reads)
	}
	remote.Ingress[0].Service = "http_status:500"
	old := metav1.NewTime(time.Now().Add(-31 * time.Minute))
	tunnel.Status.LastFullReconcileTime = &old
	if err := r.Status().Update(ctx, tunnel); err != nil {
		t.Fatal(err)
	}
	reconcileLifecycle(t, r, tunnel)
	if calls != 2 || remote.Ingress[0].Service != "http_status:404" {
		t.Fatalf("remote drift not repaired: calls%d remote%+v", calls, remote)
	}
	tunnel.Status.TunnelID = "replacement-id"
	installTunnelClaimForTest(t, r, tunnel)
	if err := r.syncConfiguration(ctx, tunnel); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal("replacement tunnel inherited stale configuration hash")
	}
	tunnel.Status.LastFullReconcileTime = &old
	mock.GetTunnelConfigurationFunc = func(context.Context, string, string) (*cloudflare.TunnelConfiguration, error) {
		return nil, errors.New("read failed")
	}
	if err := r.syncConfiguration(ctx, tunnel); err == nil {
		t.Fatal("remote read failure ignored")
	}
	if calls != 3 {
		t.Fatal("remote mutated after failed verification")
	}
}

func TestDeletionRetriesAfterReleasedClaimWithoutRemoteMutation(t *testing.T) {
	for _, absent := range []bool{true, false} {
		t.Run(map[bool]string{true: "remote absent", false: "remote exists"}[absent], func(t *testing.T) {
			r, tunnel, _ := lifecycleFixture(t)
			ctx := context.Background()
			if err := r.Delete(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: cloudflared.DeploymentName(tunnel.Name), Namespace: tunnel.Namespace}}); err != nil {
				t.Fatal(err)
			}
			if err := r.releaseTunnelClaim(ctx, tunnel, "account"); err != nil {
				t.Fatal(err)
			}
			mutations := 0
			mock := r.CFClient.(*cloudflare.MockClient)
			mock.GetTunnelFunc = func(context.Context, string, string) (*cloudflare.Tunnel, error) {
				if absent {
					return nil, nil
				}
				return &cloudflare.Tunnel{ID: "remote-id"}, nil
			}
			mock.DeleteTunnelFunc = func(context.Context, string, string) error { mutations++; return nil }
			_, err := r.reconcileDelete(ctx, tunnel)
			if absent && err != nil {
				t.Fatal(err)
			}
			if !absent && err == nil {
				t.Fatal("unclaimed existing remote allowed cleanup")
			}
			if mutations != 0 {
				t.Fatal("unclaimed tunnel mutated")
			}
		})
	}
}

package controller

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/cloudflared"
	"cfgate.io/cfgate/internal/controller/annotations"
	"cfgate.io/cfgate/internal/controller/status"
)

func TestSyncConfigurationPreservesStatusWhenPatchingConfigHash(t *testing.T) {
	ctx := context.Background()
	scheme := controllerTestScheme(t)

	storedTunnel := &cfgatev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "edge",
			Namespace: "default",
		},
		Spec: cfgatev1alpha1.CloudflareTunnelSpec{
			Tunnel: cfgatev1alpha1.TunnelIdentity{
				Name: "edge",
			},
			Cloudflare: cfgatev1alpha1.CloudflareConfig{
				AccountID: "account",
			},
			OriginDefaults: cfgatev1alpha1.OriginDefaults{
				CAPoolSecretRef: &cfgatev1alpha1.CAPoolSecretRef{Name: "origin-ca"},
			},
		},
		Status: cfgatev1alpha1.CloudflareTunnelStatus{
			TunnelID:     "old-id",
			TunnelName:   "edge",
			TunnelDomain: cloudflare.TunnelDomain("old-id"),
			AccountID:    "account",
		},
	}
	tunnel := storedTunnel.DeepCopy()
	tunnel.Status.TunnelID = "new-id"
	tunnel.Status.TunnelDomain = cloudflare.TunnelDomain("new-id")

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gateway",
			Namespace: "default",
			Annotations: map[string]string{
				annotations.AnnotationTunnelRef: "default/edge",
			},
		},
	}
	servicePort := gatewayv1.PortNumber(8080)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route",
			Namespace: "default",
			Annotations: map[string]string{
				"cfgate.io/origin-http-host-header": "origin.example.com",
				"cfgate.io/origin-server-name":      "tls.example.com",
				"cfgate.io/origin-ca-pool":          "/etc/cfgate/origin-ca-pool/ca.pem",
				"cfgate.io/origin-ssl-verify":       "false",
				"cfgate.io/origin-http2":            "true",
				"cfgate.io/origin-h2c":              "true",
				"cfgate.io/origin-connect-timeout":  "12s",
			},
		},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name: "gateway",
				}},
			},
			Hostnames: []gatewayv1.Hostname{"app.example.com"},
			Rules: []gatewayv1.HTTPRouteRule{{
				BackendRefs: []gatewayv1.HTTPBackendRef{{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "app",
							Port: &servicePort,
						},
					},
				}},
			}},
		},
	}

	baseClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&cfgatev1alpha1.CloudflareTunnel{}).
		WithObjects(storedTunnel, gateway, route).
		Build()
	patchClient := &statusResettingPatchClient{Client: baseClient}

	updateCalled := false
	mockClient := cloudflare.NewMockClient()
	mockClient.UpdateTunnelConfigurationFunc = func(_ context.Context, accountID, tunnelID string, config cloudflare.TunnelConfiguration) error {
		updateCalled = true
		if accountID != "account" {
			t.Fatalf("accountID = %q, want account", accountID)
		}
		if tunnelID != "new-id" {
			t.Fatalf("tunnelID = %q, want new-id", tunnelID)
		}
		if len(config.Ingress) == 0 || config.Ingress[0].Hostname != "app.example.com" {
			t.Fatalf("config.Ingress = %#v, want app.example.com rule", config.Ingress)
		}
		if config.OriginRequest == nil || config.OriginRequest.CAPool != "/etc/cfgate/origin-ca-pool/ca.pem" {
			t.Fatalf("config.OriginRequest = %#v, want global caPool", config.OriginRequest)
		}
		origin := config.Ingress[0].OriginRequest
		if origin == nil {
			t.Fatal("ingress OriginRequest is nil")
		}
		if origin.HTTPHostHeader != "origin.example.com" ||
			origin.OriginServerName != "tls.example.com" ||
			origin.CAPool != "/etc/cfgate/origin-ca-pool/ca.pem" ||
			!origin.NoTLSVerify ||
			!origin.HTTP2Origin ||
			!origin.H2cOrigin ||
			origin.ConnectTimeout != "12s" {
			t.Fatalf("ingress OriginRequest = %#v, want propagated route annotations", origin)
		}
		return nil
	}

	reconciler := &CloudflareTunnelReconciler{
		Client:    patchClient,
		APIReader: baseClient,
		CFClient:  mockClient,
		Recorder:  &fakeEventRecorder{},
	}
	if err := reconciler.syncConfiguration(ctx, tunnel); err != nil {
		t.Fatalf("syncConfiguration() error = %v", err)
	}

	if !updateCalled {
		t.Fatal("UpdateTunnelConfiguration was not called")
	}
	if tunnel.Status.TunnelID != "new-id" {
		t.Fatalf("tunnel.Status.TunnelID = %q, want new-id", tunnel.Status.TunnelID)
	}
	if tunnel.Status.TunnelDomain != cloudflare.TunnelDomain("new-id") {
		t.Fatalf("tunnel.Status.TunnelDomain = %q, want %q", tunnel.Status.TunnelDomain, cloudflare.TunnelDomain("new-id"))
	}
	if tunnel.Status.ConnectedRouteCount != 1 {
		t.Fatalf("tunnel.Status.ConnectedRouteCount = %d, want 1", tunnel.Status.ConnectedRouteCount)
	}

	var current cfgatev1alpha1.CloudflareTunnel
	if err := baseClient.Get(ctx, client.ObjectKeyFromObject(storedTunnel), &current); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	storedHash := current.Annotations[configHashAnnotation]
	if storedHash == "" {
		t.Fatal("config hash annotation was not stored")
	}
	if tunnel.Annotations[configHashAnnotation] != storedHash {
		t.Fatalf("in-memory config hash = %q, want %q", tunnel.Annotations[configHashAnnotation], storedHash)
	}
	if tunnel.ResourceVersion != current.ResourceVersion {
		t.Fatalf("in-memory ResourceVersion = %q, want %q", tunnel.ResourceVersion, current.ResourceVersion)
	}
	if current.Status.TunnelID != "old-id" {
		t.Fatalf("stored Status.TunnelID = %q, want old-id", current.Status.TunnelID)
	}
}

func TestValidateOriginCAPoolSecretRef(t *testing.T) {
	ctx := context.Background()
	scheme := controllerTestScheme(t)
	tunnel := &cfgatev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "edge", Namespace: "default"},
		Spec: cfgatev1alpha1.CloudflareTunnelSpec{
			OriginDefaults: cfgatev1alpha1.OriginDefaults{
				CAPoolSecretRef: &cfgatev1alpha1.CAPoolSecretRef{Name: "origin-ca"},
			},
		},
	}

	t.Run("passes with default key", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "origin-ca", Namespace: "default"},
			Data:       map[string][]byte{"ca.crt": []byte("pem")},
		}
		reconciler := &CloudflareTunnelReconciler{
			Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build(),
		}
		if err := reconciler.validateOriginCAPoolSecretRef(ctx, tunnel); err != nil {
			t.Fatalf("validateOriginCAPoolSecretRef() error = %v", err)
		}
	})

	t.Run("reports missing secret", func(t *testing.T) {
		reconciler := &CloudflareTunnelReconciler{
			Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		}
		if err := reconciler.validateOriginCAPoolSecretRef(ctx, tunnel); err == nil {
			t.Fatal("validateOriginCAPoolSecretRef() error = nil, want missing Secret error")
		}
	})

	t.Run("reports missing explicit key", func(t *testing.T) {
		explicit := tunnel.DeepCopy()
		explicit.Spec.OriginDefaults.CAPoolSecretRef.Key = "bundle.pem"
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "origin-ca", Namespace: "default"},
			Data:       map[string][]byte{"ca.crt": []byte("pem")},
		}
		reconciler := &CloudflareTunnelReconciler{
			Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build(),
		}
		if err := reconciler.validateOriginCAPoolSecretRef(ctx, explicit); err == nil {
			t.Fatal("validateOriginCAPoolSecretRef() error = nil, want missing key error")
		}
	})
}

func TestReconcileGuardPathPatchesBackendTLSPolicyMountBeforeConfigSync(t *testing.T) {
	ctx := context.Background()
	scheme := controllerTestScheme(t)
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(appsv1) error = %v", err)
	}

	lastSync := metav1.NewTime(time.Now())
	tunnel := &cfgatev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "tunnel",
			Namespace:  "cfgate",
			Generation: 7,
			Finalizers: []string{tunnelFinalizer},
		},
		Spec: cfgatev1alpha1.CloudflareTunnelSpec{
			Tunnel: cfgatev1alpha1.TunnelIdentity{
				Name: "tunnel",
			},
			Cloudflare: cfgatev1alpha1.CloudflareConfig{
				AccountID: "account",
			},
		},
		Status: cfgatev1alpha1.CloudflareTunnelStatus{
			ObservedGeneration: 7,
			TunnelID:           "tunnel-id",
			TunnelName:         "tunnel",
			TunnelDomain:       cloudflare.TunnelDomain("tunnel-id"),
			AccountID:          "account",
			LastSyncTime:       &lastSync,
			Conditions: []metav1.Condition{{
				Type:               status.ConditionTypeReady,
				Status:             metav1.ConditionTrue,
				Reason:             status.ReasonTunnelOperational,
				Message:            "ready",
				ObservedGeneration: 7,
				LastTransitionTime: metav1.Now(),
			}},
		},
	}
	deployment := cloudflared.NewBuilder().BuildDeployment(tunnel, "token")
	gateway := gatewayForTunnelTest()
	route := routeForBackendTest("app-route", "apps", "app")
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "apps"},
	}
	policy := backendTLSPolicyForTest("tls-app", "apps", "app", "app.example.com", "ca-app")
	ca := caConfigMapForTest("ca-app", "apps")

	baseClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&cfgatev1alpha1.CloudflareTunnel{}).
		WithObjects(tunnel, deployment, gateway, route, service, policy, ca).
		Build()

	configUpdated := false
	mockClient := cloudflare.NewMockClient()
	mockClient.GetTunnelTokenFunc = func(context.Context, string, string) (string, error) {
		t.Fatal("GetTunnelToken must not be called in guard path")
		return "", nil
	}
	mockClient.UpdateTunnelConfigurationFunc = func(_ context.Context, accountID, tunnelID string, config cloudflare.TunnelConfiguration) error {
		configUpdated = true
		if accountID != "account" || tunnelID != "tunnel-id" {
			t.Fatalf("UpdateTunnelConfiguration account/tunnel = %s/%s, want account/tunnel-id", accountID, tunnelID)
		}
		assertDeploymentHasBackendTLSPolicyMount(t, ctx, baseClient)
		assertGeneratedBackendTLSSecretExists(t, ctx, baseClient, tunnel, policy)

		var appRule *cloudflare.IngressRule
		for i := range config.Ingress {
			if config.Ingress[i].Service == "https://app.apps.svc.cluster.local:8443" {
				appRule = &config.Ingress[i]
				break
			}
		}
		if appRule == nil {
			t.Fatalf("config.Ingress = %#v, want https app service rule", config.Ingress)
		}
		if appRule.OriginRequest == nil {
			t.Fatal("app rule OriginRequest is nil")
		}
		if appRule.OriginRequest.CAPool != cloudflared.BackendTLSCAPoolPath("apps", "tls-app") {
			t.Fatalf("app rule CAPool = %q, want %q", appRule.OriginRequest.CAPool, cloudflared.BackendTLSCAPoolPath("apps", "tls-app"))
		}
		return nil
	}

	reconciler := &CloudflareTunnelReconciler{
		Client:    baseClient,
		APIReader: baseClient,
		Scheme:    scheme,
		CFClient:  mockClient,
		Recorder:  &fakeEventRecorder{},
	}
	result, err := reconciler.Reconcile(ctx, ctrlRequest("cfgate", "tunnel"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != requeueAfterSuccess {
		t.Fatalf("Reconcile() RequeueAfter = %v, want %v", result.RequeueAfter, requeueAfterSuccess)
	}
	if !configUpdated {
		t.Fatal("UpdateTunnelConfiguration was not called")
	}
	assertDeploymentHasBackendTLSPolicyMount(t, ctx, baseClient)
}

func TestEnsureCloudflaredOriginCAPoolDeploymentRemovesStaleBackendTLSPolicyMount(t *testing.T) {
	ctx := context.Background()
	scheme := controllerTestScheme(t)
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(appsv1) error = %v", err)
	}

	tunnel := tunnelForOriginRuntimeTest()
	tunnel.Status.TunnelID = "tunnel-id"
	deployment := cloudflared.NewBuilder().BuildDeployment(tunnel, "token")
	staleVolumeName := cloudflared.OriginCAPoolVolumeNameFor("backendtls", "apps", "tls-app")
	staleSecretName := generatedOriginCASecretName(tunnel.Name, "backendtls", "apps", "tls-app", "ca-app")
	deployment.Spec.Template.Spec.Volumes = append(deployment.Spec.Template.Spec.Volumes,
		corev1.Volume{Name: "custom-config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "custom-config"}}}},
		corev1.Volume{Name: staleVolumeName, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: staleSecretName}}},
	)
	container := cloudflaredContainer(&deployment.Spec.Template.Spec)
	if container == nil {
		t.Fatal("test deployment has no cloudflared container")
	}
	container.VolumeMounts = append(container.VolumeMounts,
		corev1.VolumeMount{Name: "custom-config", MountPath: "/etc/custom", ReadOnly: true},
		corev1.VolumeMount{Name: staleVolumeName, MountPath: strings.TrimSuffix(cloudflared.BackendTLSCAPoolPath("apps", "tls-app"), "/"+cloudflared.OriginCAPoolFileName), ReadOnly: true},
	)
	staleSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      staleSecretName,
			Namespace: tunnel.Namespace,
			Labels: map[string]string{
				generatedOriginCASecretLabel:   "true",
				"app.kubernetes.io/instance":   tunnel.Name,
				"app.kubernetes.io/managed-by": "cfgate",
				"app.kubernetes.io/component":  "origin-ca-pool",
			},
		},
		Data: map[string][]byte{cloudflared.DefaultOriginCAPoolSecretKey: []byte("old")},
	}
	baseClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tunnel, deployment, staleSecret).
		Build()
	reconciler := &CloudflareTunnelReconciler{
		Client: baseClient,
		Scheme: scheme,
	}

	if err := reconciler.ensureCloudflaredOriginCAPoolDeployment(ctx, tunnel); err != nil {
		t.Fatalf("ensureCloudflaredOriginCAPoolDeployment() error = %v", err)
	}

	var got appsv1.Deployment
	if err := baseClient.Get(ctx, types.NamespacedName{Namespace: tunnel.Namespace, Name: deployment.Name}, &got); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if hasVolume(got.Spec.Template.Spec.Volumes, staleVolumeName) {
		t.Fatalf("stale managed volume %q still exists", staleVolumeName)
	}
	gotContainer := cloudflaredContainer(&got.Spec.Template.Spec)
	if gotContainer == nil {
		t.Fatal("updated deployment has no cloudflared container")
	}
	if hasMount(gotContainer.VolumeMounts, staleVolumeName, "") {
		t.Fatalf("stale managed mount %q still exists", staleVolumeName)
	}
	if !hasVolume(got.Spec.Template.Spec.Volumes, "custom-config") {
		t.Fatal("non-managed volume was removed")
	}
	if !hasMount(gotContainer.VolumeMounts, "custom-config", "/etc/custom") {
		t.Fatal("non-managed mount was removed")
	}
	var gotSecret corev1.Secret
	err := baseClient.Get(ctx, types.NamespacedName{Namespace: tunnel.Namespace, Name: staleSecretName}, &gotSecret)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("stale generated Secret get error = %v, want not found", err)
	}
}

func TestFindTunnelsForSecretScopesOriginCAReferences(t *testing.T) {
	ctx := context.Background()
	scheme := controllerTestScheme(t)
	tunnels := []client.Object{
		&cfgatev1alpha1.CloudflareTunnel{
			ObjectMeta: metav1.ObjectMeta{Name: "default-ca", Namespace: "apps"},
			Spec: cfgatev1alpha1.CloudflareTunnelSpec{
				OriginDefaults: cfgatev1alpha1.OriginDefaults{
					CAPoolSecretRef: &cfgatev1alpha1.CAPoolSecretRef{Name: "origin-ca"},
				},
			},
		},
		&cfgatev1alpha1.CloudflareTunnel{
			ObjectMeta: metav1.ObjectMeta{Name: "local-pool", Namespace: "apps"},
			Spec: cfgatev1alpha1.CloudflareTunnelSpec{
				OriginCAPools: []cfgatev1alpha1.OriginCAPool{{
					Name: "local",
					SecretRef: cfgatev1alpha1.OriginCAPoolSecretRef{
						Name: "origin-ca",
					},
				}},
			},
		},
		&cfgatev1alpha1.CloudflareTunnel{
			ObjectMeta: metav1.ObjectMeta{Name: "cross-pool", Namespace: "apps"},
			Spec: cfgatev1alpha1.CloudflareTunnelSpec{
				OriginCAPools: []cfgatev1alpha1.OriginCAPool{{
					Name: "shared",
					SecretRef: cfgatev1alpha1.OriginCAPoolSecretRef{
						Namespace: stringPtr("security"),
						Name:      "shared-ca",
					},
				}},
			},
		},
		&cfgatev1alpha1.CloudflareTunnel{
			ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "apps"},
			Spec: cfgatev1alpha1.CloudflareTunnelSpec{
				OriginDefaults: cfgatev1alpha1.OriginDefaults{
					CAPoolSecretRef: &cfgatev1alpha1.CAPoolSecretRef{Name: "different-ca"},
				},
			},
		},
		&cfgatev1alpha1.CloudflareTunnel{
			ObjectMeta: metav1.ObjectMeta{Name: "default-ca", Namespace: "other"},
			Spec: cfgatev1alpha1.CloudflareTunnelSpec{
				OriginDefaults: cfgatev1alpha1.OriginDefaults{
					CAPoolSecretRef: &cfgatev1alpha1.CAPoolSecretRef{Name: "origin-ca"},
				},
			},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tunnels...).Build()
	reconciler := &CloudflareTunnelReconciler{Client: k8sClient, APIReader: k8sClient}

	appsSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "origin-ca", Namespace: "apps"}}
	assertReconcileRequestKeys(t, reconciler.findTunnelsForSecret(ctx, appsSecret), []string{"apps/default-ca", "apps/local-pool"})

	sharedSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "shared-ca", Namespace: "security"}}
	assertReconcileRequestKeys(t, reconciler.findTunnelsForSecret(ctx, sharedSecret), []string{"apps/cross-pool"})

	unrelatedSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "apps"}}
	assertReconcileRequestKeys(t, reconciler.findTunnelsForSecret(ctx, unrelatedSecret), nil)
}

func TestFindTunnelsForConfigMapScopesBackendTLSPolicyCARefs(t *testing.T) {
	ctx := context.Background()
	scheme := controllerTestScheme(t)
	tunnel := tunnelForOriginRuntimeTest()
	gw := gatewayForTunnelTest()
	route := routeForBackendTest("app-route", "apps", "app")
	relevant := backendTLSPolicyForTest("tls-app", "apps", "app", "app.example.com", "ca-app")
	unused := backendTLSPolicyForTest("tls-unused", "apps", "unused", "unused.example.com", "ca-app")
	other := backendTLSPolicyForTest("tls-other", "other", "other", "other.example.com", "ca-other")
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tunnel, gw, route, relevant, unused, other).
		Build()
	reconciler := &CloudflareTunnelReconciler{Client: k8sClient, APIReader: k8sClient}

	appCA := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "ca-app", Namespace: "apps"}}
	assertReconcileRequestKeys(t, reconciler.findTunnelsForConfigMap(ctx, appCA), []string{"cfgate/tunnel"})

	otherCA := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "ca-other", Namespace: "other"}}
	assertReconcileRequestKeys(t, reconciler.findTunnelsForConfigMap(ctx, otherCA), nil)

	notCA := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "not-ca", Namespace: "apps"}}
	assertReconcileRequestKeys(t, reconciler.findTunnelsForConfigMap(ctx, notCA), nil)
}

func TestBuildRulesFromHTTPRouteUsesHostnameAnnotation(t *testing.T) {
	servicePort := gatewayv1.PortNumber(8080)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route",
			Namespace: "default",
			Annotations: map[string]string{
				annotations.AnnotationHostname: "override.example.com",
			},
		},
		Spec: gatewayv1.HTTPRouteSpec{
			Hostnames: []gatewayv1.Hostname{"ignored.example.com"},
			Rules: []gatewayv1.HTTPRouteRule{{
				BackendRefs: []gatewayv1.HTTPBackendRef{{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "app",
							Port: &servicePort,
						},
					},
				}},
			}},
		},
	}

	rules, err := (&CloudflareTunnelReconciler{}).buildRulesFromHTTPRoute(route)
	if err != nil {
		t.Fatalf("buildRulesFromHTTPRoute() error = %v", err)
	}
	if len(rules) != 1 || rules[0].Hostname != "override.example.com" {
		t.Fatalf("rules = %#v, want hostname annotation override", rules)
	}
}

func TestBuildRulesFromHTTPRouteValidatesOriginCAPool(t *testing.T) {
	servicePort := gatewayv1.PortNumber(8080)
	baseRoute := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route",
			Namespace: "default",
		},
		Spec: gatewayv1.HTTPRouteSpec{
			Hostnames: []gatewayv1.Hostname{"app.example.com"},
			Rules: []gatewayv1.HTTPRouteRule{{
				BackendRefs: []gatewayv1.HTTPBackendRef{{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "app",
							Port: &servicePort,
						},
					},
				}},
			}},
		},
	}

	tests := []struct {
		name              string
		caPool            string
		originCAPoolMount bool
		wantErr           string
	}{
		{
			name:              "managed path accepted when mounted",
			caPool:            "/etc/cfgate/origin-ca-pool/ca.pem",
			originCAPoolMount: true,
		},
		{
			name:    "managed path requires tunnel secret mount",
			caPool:  "/etc/cfgate/origin-ca-pool/ca.pem",
			wantErr: "requires CloudflareTunnel spec.originDefaults.caPoolSecretRef",
		},
		{
			name:              "arbitrary path rejected even when mounted",
			caPool:            "/etc/cfgate/custom-ca.pem",
			originCAPoolMount: true,
			wantErr:           "must be \"/etc/cfgate/origin-ca-pool/ca.pem\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route := baseRoute.DeepCopy()
			route.Annotations = map[string]string{annotations.AnnotationOriginCAPool: tt.caPool}
			rules, err := (&CloudflareTunnelReconciler{}).buildRulesFromHTTPRouteForHostnames(route, effectiveHTTPRouteHostnames(route), tt.originCAPoolMount)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("buildRulesFromHTTPRouteForHostnames() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildRulesFromHTTPRouteForHostnames() error = %v", err)
			}
			if len(rules) != 1 || rules[0].OriginRequest == nil || rules[0].OriginRequest.CAPool != tt.caPool {
				t.Fatalf("rules = %#v, want managed caPool origin request", rules)
			}
		})
	}
}

func TestRouteHostnamesForGatewayFallsBackToListener(t *testing.T) {
	section := gatewayv1.SectionName("web")
	listenerHostname := gatewayv1.Hostname("listener.example.com")
	gw := &gatewayv1.Gateway{
		Spec: gatewayv1.GatewaySpec{
			Listeners: []gatewayv1.Listener{{
				Name:     section,
				Protocol: gatewayv1.HTTPProtocolType,
				Hostname: &listenerHostname,
			}},
		},
	}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "default"},
	}
	parentRef := gatewayv1.ParentReference{Name: "gateway", SectionName: &section}

	hostnames := routeHostnamesForGateway(route, gw, parentRef)
	if len(hostnames) != 1 || hostnames[0] != listenerHostname {
		t.Fatalf("routeHostnamesForGateway() = %#v, want listener hostname", hostnames)
	}
}

func TestCloudflaredPathRegex(t *testing.T) {
	tests := []struct {
		name    string
		match   gatewayv1.HTTPRouteMatch
		want    string
		wantErr bool
	}{
		{name: "omitted path", match: gatewayv1.HTTPRouteMatch{}, want: ""},
		{
			name: "path prefix",
			match: gatewayv1.HTTPRouteMatch{Path: &gatewayv1.HTTPPathMatch{
				Type:  pathMatchType(gatewayv1.PathMatchPathPrefix),
				Value: stringPtr("/foo"),
			}},
			want: "^/foo(?:/.*)?$",
		},
		{
			name: "path prefix with trailing slash",
			match: gatewayv1.HTTPRouteMatch{Path: &gatewayv1.HTTPPathMatch{
				Type:  pathMatchType(gatewayv1.PathMatchPathPrefix),
				Value: stringPtr("/api/v1/"),
			}},
			want: "^/api/v1/.*$",
		},
		{
			name: "exact",
			match: gatewayv1.HTTPRouteMatch{Path: &gatewayv1.HTTPPathMatch{
				Type:  pathMatchType(gatewayv1.PathMatchExact),
				Value: stringPtr("/foo"),
			}},
			want: "^/foo$",
		},
		{
			name: "regular expression",
			match: gatewayv1.HTTPRouteMatch{Path: &gatewayv1.HTTPPathMatch{
				Type:  pathMatchType(gatewayv1.PathMatchRegularExpression),
				Value: stringPtr("^/v[0-9]+/"),
			}},
			want: "^/v[0-9]+/",
		},
		{
			name: "invalid regular expression",
			match: gatewayv1.HTTPRouteMatch{Path: &gatewayv1.HTTPPathMatch{
				Type:  pathMatchType(gatewayv1.PathMatchRegularExpression),
				Value: stringPtr("["),
			}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cloudflaredPathRegex(tt.match)
			if tt.wantErr {
				if err == nil {
					t.Fatal("cloudflaredPathRegex() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("cloudflaredPathRegex() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("cloudflaredPathRegex() = %q, want %q", got, tt.want)
			}
		})
	}

	prefixRegex := regexp.MustCompile("^/foo(?:/.*)?$")
	if !prefixRegex.MatchString("/foo") || !prefixRegex.MatchString("/foo/bar") || prefixRegex.MatchString("/foobar") {
		t.Fatal("path prefix regex should match /foo and /foo/bar, but not /foobar")
	}
	trailingSlashRegex := regexp.MustCompile("^/api/v1/.*$")
	if !trailingSlashRegex.MatchString("/api/v1/") || !trailingSlashRegex.MatchString("/api/v1/users") || trailingSlashRegex.MatchString("/api/v1") {
		t.Fatal("trailing slash path prefix regex should match /api/v1/ and /api/v1/users, but not /api/v1")
	}
}

func assertDeploymentHasBackendTLSPolicyMount(t *testing.T, ctx context.Context, k8sClient client.Client) {
	t.Helper()
	var deployment appsv1.Deployment
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "cfgate", Name: "tunnel-cloudflared"}, &deployment); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	volumeName := cloudflared.OriginCAPoolVolumeNameFor("backendtls", "apps", "tls-app")
	if !hasVolume(deployment.Spec.Template.Spec.Volumes, volumeName) {
		t.Fatalf("deployment volumes = %#v, want %q", deployment.Spec.Template.Spec.Volumes, volumeName)
	}
	container := cloudflaredContainer(&deployment.Spec.Template.Spec)
	if container == nil {
		t.Fatal("deployment has no cloudflared container")
	}
	mountPath := strings.TrimSuffix(cloudflared.BackendTLSCAPoolPath("apps", "tls-app"), "/"+cloudflared.OriginCAPoolFileName)
	if !hasMount(container.VolumeMounts, volumeName, mountPath) {
		t.Fatalf("cloudflared mounts = %#v, want %s at %s", container.VolumeMounts, volumeName, mountPath)
	}
}

func assertGeneratedBackendTLSSecretExists(t *testing.T, ctx context.Context, k8sClient client.Client, tunnel *cfgatev1alpha1.CloudflareTunnel, policy *gatewayv1.BackendTLSPolicy) {
	t.Helper()
	secretName := generatedOriginCASecretName(tunnel.Name, "backendtls", policy.Namespace, policy.Name, "ca-app")
	var secret corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: tunnel.Namespace, Name: secretName}, &secret); err != nil {
		t.Fatalf("generated BackendTLSPolicy Secret missing: %v", err)
	}
}

func hasVolume(volumes []corev1.Volume, name string) bool {
	for _, volume := range volumes {
		if volume.Name == name {
			return true
		}
	}
	return false
}

func hasMount(mounts []corev1.VolumeMount, name, path string) bool {
	for _, mount := range mounts {
		if mount.Name != name {
			continue
		}
		return path == "" || mount.MountPath == path
	}
	return false
}

func assertReconcileRequestKeys(t *testing.T, reqs []reconcile.Request, want []string) {
	t.Helper()
	got := make([]string, 0, len(reqs))
	for _, req := range reqs {
		got = append(got, req.String())
	}
	if len(got) != len(want) {
		t.Fatalf("reconcile requests = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reconcile requests = %v, want %v", got, want)
		}
	}
}

type statusResettingPatchClient struct {
	client.Client
}

func (c *statusResettingPatchClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	serverStatus := cfgatev1alpha1.CloudflareTunnelStatus{}
	hasServerStatus := false
	if tunnel, ok := obj.(*cfgatev1alpha1.CloudflareTunnel); ok {
		var current cfgatev1alpha1.CloudflareTunnel
		if err := c.Get(ctx, client.ObjectKeyFromObject(tunnel), &current); err != nil {
			return err
		}
		serverStatus = current.Status
		hasServerStatus = true
	}

	if err := c.Client.Patch(ctx, obj, patch, opts...); err != nil {
		return err
	}

	if tunnel, ok := obj.(*cfgatev1alpha1.CloudflareTunnel); ok && hasServerStatus {
		tunnel.Status = serverStatus
	}
	return nil
}

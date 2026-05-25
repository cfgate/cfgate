package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	crlog "sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflared"
	"cfgate.io/cfgate/internal/controller/annotations"
)

func TestBuildRulesAppliesOriginPolicyBackendTLSPolicyAndAnnotations(t *testing.T) {
	ctx := context.Background()
	scheme := controllerTestScheme(t)

	ruleName := gatewayv1.SectionName("api")
	port := gatewayv1.PortNumber(8443)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route",
			Namespace: "apps",
			Annotations: map[string]string{
				annotations.AnnotationOriginHTTPHostHeader: "annotation.example.com",
				annotations.AnnotationOriginCAPoolRef:      "internal",
			},
		},
		Spec: gatewayv1.HTTPRouteSpec{
			Hostnames: []gatewayv1.Hostname{"app.example.com"},
			Rules: []gatewayv1.HTTPRouteRule{{
				Name: &ruleName,
				BackendRefs: []gatewayv1.HTTPBackendRef{{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "app",
							Port: &port,
						},
					},
				}},
			}},
		},
	}
	originPolicy := &cfgatev1alpha1.CloudflareOriginPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "origin",
			Namespace:         "apps",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour)),
		},
		Spec: cfgatev1alpha1.CloudflareOriginPolicySpec{
			TargetRefs: []cfgatev1alpha1.OriginPolicyTargetReference{{
				Group:       gatewayv1.GroupName,
				Kind:        "HTTPRoute",
				Name:        "route",
				SectionName: "api",
			}},
			Origin: cfgatev1alpha1.OriginSettings{
				Protocol:       cfgatev1alpha1.OriginProtocolHTTP,
				HTTPHostHeader: "policy.example.com",
				TLS: &cfgatev1alpha1.OriginTLSSettings{
					TLSTimeout: "9s",
				},
			},
		},
	}
	backendTLS := &gatewayv1.BackendTLSPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "backend",
			Namespace: "apps",
		},
		Spec: gatewayv1.BackendTLSPolicySpec{
			TargetRefs: []gatewayv1.LocalPolicyTargetReferenceWithSectionName{{
				LocalPolicyTargetReference: gatewayv1.LocalPolicyTargetReference{
					Group: "",
					Kind:  "Service",
					Name:  "app",
				},
			}},
			Validation: gatewayv1.BackendTLSPolicyValidation{
				WellKnownCACertificates: ptrWellKnown(gatewayv1.WellKnownCACertificatesSystem),
				Hostname:                gatewayv1.PreciseHostname("backend.example.com"),
			},
		},
	}
	reconciler := &CloudflareTunnelReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(originPolicy, backendTLS).Build(),
	}

	rules, err := reconciler.buildRulesFromHTTPRouteForHostnamesWithRuntime(ctx, route, []gatewayv1.Hostname{"app.example.com"}, false, &originRuntime{
		namedCAPoolPaths:      map[string]string{"internal": "/etc/cfgate/origin-ca-pools/internal/ca.pem"},
		backendTLSCAPoolPaths: map[types.NamespacedName]string{},
	})
	if err != nil {
		t.Fatalf("buildRulesFromHTTPRouteForHostnamesWithRuntime() error = %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("len(rules) = %d, want 1", len(rules))
	}
	if rules[0].Service != "https://app.apps.svc.cluster.local:8443" {
		t.Fatalf("service = %q, want BackendTLSPolicy https service", rules[0].Service)
	}
	origin := rules[0].OriginRequest
	if origin == nil {
		t.Fatal("OriginRequest is nil")
	}
	if origin.HTTPHostHeader != "annotation.example.com" {
		t.Fatalf("HTTPHostHeader = %q, want annotation override", origin.HTTPHostHeader)
	}
	if origin.OriginServerName != "backend.example.com" {
		t.Fatalf("OriginServerName = %q, want BackendTLSPolicy hostname", origin.OriginServerName)
	}
	if origin.TLSTimeout != "9s" {
		t.Fatalf("TLSTimeout = %q, want policy timeout", origin.TLSTimeout)
	}
	if origin.CAPool != "/etc/cfgate/origin-ca-pools/internal/ca.pem" {
		t.Fatalf("CAPool = %q, want annotation pool ref path", origin.CAPool)
	}
}

func TestBuildRulesFromHTTPRouteOriginCAPoolRefOnly(t *testing.T) {
	ctx := context.Background()
	port := gatewayv1.PortNumber(8443)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route",
			Namespace: "apps",
			Annotations: map[string]string{
				annotations.AnnotationOriginCAPoolRef: "internal",
			},
		},
		Spec: gatewayv1.HTTPRouteSpec{
			Hostnames: []gatewayv1.Hostname{"app.example.com"},
			Rules: []gatewayv1.HTTPRouteRule{{
				BackendRefs: []gatewayv1.HTTPBackendRef{{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "app",
							Port: &port,
						},
					},
				}},
			}},
		},
	}
	runtime := &originRuntime{
		namedCAPoolPaths:      map[string]string{"internal": "/etc/cfgate/origin-ca-pools/internal/ca.pem"},
		backendTLSCAPoolPaths: map[types.NamespacedName]string{},
	}

	rules, err := (&CloudflareTunnelReconciler{}).buildRulesFromHTTPRouteForHostnamesWithRuntime(ctx, route, []gatewayv1.Hostname{"app.example.com"}, false, runtime)
	if err != nil {
		t.Fatalf("buildRulesFromHTTPRouteForHostnamesWithRuntime() error = %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("len(rules) = %d, want 1", len(rules))
	}
	if rules[0].OriginRequest == nil {
		t.Fatal("OriginRequest is nil")
	}
	if rules[0].OriginRequest.CAPool != "/etc/cfgate/origin-ca-pools/internal/ca.pem" {
		t.Fatalf("CAPool = %q, want named pool path", rules[0].OriginRequest.CAPool)
	}

	route.Annotations[annotations.AnnotationOriginCAPoolRef] = "missing"
	_, err = (&CloudflareTunnelReconciler{}).buildRulesFromHTTPRouteForHostnamesWithRuntime(ctx, route, []gatewayv1.Hostname{"app.example.com"}, false, runtime)
	if err == nil || !strings.Contains(err.Error(), `references unknown origin CA pool "missing"`) {
		t.Fatalf("buildRulesFromHTTPRouteForHostnamesWithRuntime() error = %v, want unknown pool ref error", err)
	}
}

func TestCloudflareOriginPolicyReconcilerStatusAndConflict(t *testing.T) {
	ctx := context.Background()
	scheme := controllerTestScheme(t)
	ruleName := gatewayv1.SectionName("api")
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "apps"},
		Spec:       gatewayv1.HTTPRouteSpec{Rules: []gatewayv1.HTTPRouteRule{{Name: &ruleName}}},
	}
	oldPolicy := originPolicyForTest("old", time.Now().Add(-time.Hour))
	newPolicy := originPolicyForTest("new", time.Now())
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&cfgatev1alpha1.CloudflareOriginPolicy{}).
		WithObjects(route, oldPolicy, newPolicy).
		Build()
	reconciler := &CloudflareOriginPolicyReconciler{Client: client, Scheme: scheme}

	if _, err := reconciler.Reconcile(ctx, ctrlRequest("apps", "old")); err != nil {
		t.Fatalf("reconcile old policy: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, ctrlRequest("apps", "new")); err != nil {
		t.Fatalf("reconcile new policy: %v", err)
	}

	var accepted cfgatev1alpha1.CloudflareOriginPolicy
	if err := client.Get(ctx, types.NamespacedName{Namespace: "apps", Name: "old"}, &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.Status.AttachedTargets != 1 {
		t.Fatalf("old AttachedTargets = %d, want 1", accepted.Status.AttachedTargets)
	}
	var conflicted cfgatev1alpha1.CloudflareOriginPolicy
	if err := client.Get(ctx, types.NamespacedName{Namespace: "apps", Name: "new"}, &conflicted); err != nil {
		t.Fatal(err)
	}
	if conflicted.Status.AttachedTargets != 0 || len(conflicted.Status.Ancestors) != 1 ||
		conflicted.Status.Ancestors[0].Conditions[0].Reason != "Conflicted" {
		t.Fatalf("new status = %#v, want conflicted unattached policy", conflicted.Status)
	}
}

func TestCloudflareOriginPolicyListError(t *testing.T) {
	ctx := context.Background()
	scheme := controllerTestScheme(t)
	policy := originPolicyForTest("origin", time.Now())
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).Build()
	reconciler := &CloudflareOriginPolicyReconciler{
		Client: &listErrorClient{
			Client:  k8sClient,
			listErr: errors.New("cache unavailable"),
			failFor: func(list client.ObjectList) bool {
				_, ok := list.(*cfgatev1alpha1.CloudflareOriginPolicyList)
				return ok
			},
		},
		Scheme: scheme,
	}

	ancestors, attached, targetsOK, grantsOK, err := reconciler.evaluateOriginPolicy(ctx, policy)
	if err == nil || !strings.Contains(err.Error(), "list CloudflareOriginPolicies") {
		t.Fatalf("evaluateOriginPolicy() error = %v, want list error", err)
	}
	if ancestors != nil || attached != 0 || targetsOK || grantsOK {
		t.Fatalf("evaluateOriginPolicy() = (%#v, %d, %v, %v), want zero values on list error", ancestors, attached, targetsOK, grantsOK)
	}
}

func TestBackendTLSPolicyReconcilerRejectsUnsupportedFields(t *testing.T) {
	ctx := context.Background()
	scheme := controllerTestScheme(t)
	policy := &gatewayv1.BackendTLSPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: "apps"},
		Spec: gatewayv1.BackendTLSPolicySpec{
			TargetRefs: []gatewayv1.LocalPolicyTargetReferenceWithSectionName{{
				LocalPolicyTargetReference: gatewayv1.LocalPolicyTargetReference{Group: "", Kind: "Service", Name: "app"},
			}},
			Validation: gatewayv1.BackendTLSPolicyValidation{
				WellKnownCACertificates: ptrWellKnown(gatewayv1.WellKnownCACertificatesSystem),
				Hostname:                gatewayv1.PreciseHostname("backend.example.com"),
				SubjectAltNames: []gatewayv1.SubjectAltName{{
					Type:     gatewayv1.HostnameSubjectAltNameType,
					Hostname: gatewayv1.Hostname("backend.example.com"),
				}},
			},
		},
	}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "apps"}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&gatewayv1.BackendTLSPolicy{}).WithObjects(policy, svc).Build()
	reconciler := &BackendTLSPolicyReconciler{Client: client, Scheme: scheme}
	if _, err := reconciler.Reconcile(ctx, ctrlRequest("apps", "backend")); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	var got gatewayv1.BackendTLSPolicy
	if err := client.Get(ctx, types.NamespacedName{Namespace: "apps", Name: "backend"}, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.Ancestors) != 1 || got.Status.Ancestors[0].Conditions[0].Status != metav1.ConditionFalse {
		t.Fatalf("status = %#v, want Accepted=False", got.Status)
	}
}

func TestBackendTLSPolicyListError(t *testing.T) {
	ctx := context.Background()
	scheme := controllerTestScheme(t)
	policy := &gatewayv1.BackendTLSPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: "apps"},
		Spec: gatewayv1.BackendTLSPolicySpec{
			TargetRefs: []gatewayv1.LocalPolicyTargetReferenceWithSectionName{{
				LocalPolicyTargetReference: gatewayv1.LocalPolicyTargetReference{Group: "", Kind: "Service", Name: "app"},
			}},
			Validation: gatewayv1.BackendTLSPolicyValidation{
				WellKnownCACertificates: ptrWellKnown(gatewayv1.WellKnownCACertificatesSystem),
				Hostname:                gatewayv1.PreciseHostname("backend.example.com"),
			},
		},
	}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "apps"}}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy, svc).Build()
	reconciler := &BackendTLSPolicyReconciler{
		Client: &listErrorClient{
			Client:  k8sClient,
			listErr: errors.New("cache unavailable"),
			failFor: func(list client.ObjectList) bool {
				_, ok := list.(*gatewayv1.BackendTLSPolicyList)
				return ok
			},
		},
		Scheme: scheme,
	}

	_, _, _, _, _, _, err := reconciler.evaluateBackendTLSPolicy(ctx, policy)
	if err == nil || !strings.Contains(err.Error(), "list BackendTLSPolicies") {
		t.Fatalf("evaluateBackendTLSPolicy() error = %v, want list error", err)
	}
}

func TestBackendTLSPolicyReconcileLogsErrors(t *testing.T) {
	scheme := controllerTestScheme(t)
	policy := backendTLSPolicyForTest("backend", "apps", "app", "backend.example.com", "ca")
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "apps"}}

	t.Run("evaluation error", func(t *testing.T) {
		k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy.DeepCopy(), svc.DeepCopy()).Build()
		var logs []string
		ctx := crlog.IntoContext(context.Background(), logr.New(&recordingLogSink{errors: &logs}))
		reconciler := &BackendTLSPolicyReconciler{
			Client: &listErrorClient{
				Client:  k8sClient,
				listErr: errors.New("cache unavailable"),
				failFor: func(list client.ObjectList) bool {
					_, ok := list.(*gatewayv1.BackendTLSPolicyList)
					return ok
				},
			},
			Scheme: scheme,
		}

		result, err := reconciler.Reconcile(ctx, ctrlRequest("apps", "backend"))
		if err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if result.RequeueAfter != requeueAfterError {
			t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, requeueAfterError)
		}
		if !logMessagesContain(logs, "failed to evaluate BackendTLSPolicy") {
			t.Fatalf("logs = %#v, want evaluation error log", logs)
		}
	})

	t.Run("status update error", func(t *testing.T) {
		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&gatewayv1.BackendTLSPolicy{}).
			WithObjects(policy.DeepCopy(), svc.DeepCopy(), caConfigMapForTest("ca", "apps")).
			Build()
		var logs []string
		ctx := crlog.IntoContext(context.Background(), logr.New(&recordingLogSink{errors: &logs}))
		reconciler := &BackendTLSPolicyReconciler{
			Client: &statusUpdateErrorClient{
				Client:    k8sClient,
				updateErr: errors.New("status unavailable"),
			},
			Scheme: scheme,
		}

		result, err := reconciler.Reconcile(ctx, ctrlRequest("apps", "backend"))
		if err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if result.RequeueAfter != requeueAfterError {
			t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, requeueAfterError)
		}
		if !logMessagesContain(logs, "failed to update BackendTLSPolicy status") {
			t.Fatalf("logs = %#v, want status update error log", logs)
		}
	})
}

func TestBackendTLSPolicyServiceGetErrorRequeues(t *testing.T) {
	ctx := context.Background()
	scheme := controllerTestScheme(t)
	policy := backendTLSPolicyForTest("backend", "apps", "app", "backend.example.com", "ca")
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "apps"}}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&gatewayv1.BackendTLSPolicy{}).
		WithObjects(policy, svc, caConfigMapForTest("ca", "apps")).
		Build()
	var logs []string
	ctx = crlog.IntoContext(ctx, logr.New(&recordingLogSink{errors: &logs}))
	reconciler := &BackendTLSPolicyReconciler{
		Client: &getErrorClient{
			Client: k8sClient,
			getErr: errors.New("cache unavailable"),
			failFor: func(name types.NamespacedName, obj client.Object) bool {
				_, ok := obj.(*corev1.Service)
				return ok && name.Namespace == "apps" && name.Name == "app"
			},
		},
		Scheme: scheme,
	}

	result, err := reconciler.Reconcile(ctx, ctrlRequest("apps", "backend"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != requeueAfterError {
		t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, requeueAfterError)
	}
	var got gatewayv1.BackendTLSPolicy
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "apps", Name: "backend"}, &got); err != nil {
		t.Fatal(err)
	}
	for _, ancestor := range got.Status.Ancestors {
		for _, condition := range ancestor.Conditions {
			if condition.Type == string(gatewayv1.PolicyConditionAccepted) && condition.Reason == string(gatewayv1.PolicyReasonTargetNotFound) {
				t.Fatalf("status = %#v, want no TargetNotFound status on transient Service get error", got.Status)
			}
		}
	}
	if !logMessagesContain(logs, "failed to evaluate BackendTLSPolicy") {
		t.Fatalf("logs = %#v, want evaluation error log", logs)
	}
}

func TestBackendTLSPolicyMissingServiceSetsTargetNotFound(t *testing.T) {
	ctx := context.Background()
	scheme := controllerTestScheme(t)
	policy := backendTLSPolicyForTest("backend", "apps", "app", "backend.example.com", "ca")
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&gatewayv1.BackendTLSPolicy{}).
		WithObjects(policy, caConfigMapForTest("ca", "apps")).
		Build()
	reconciler := &BackendTLSPolicyReconciler{Client: k8sClient, Scheme: scheme}

	result, err := reconciler.Reconcile(ctx, ctrlRequest("apps", "backend"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if !result.IsZero() {
		t.Fatalf("result = %#v, want empty result", result)
	}
	var got gatewayv1.BackendTLSPolicy
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "apps", Name: "backend"}, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.Ancestors) != 1 {
		t.Fatalf("status = %#v, want one ancestor", got.Status)
	}
	conditions := got.Status.Ancestors[0].Conditions
	if len(conditions) != 2 {
		t.Fatalf("conditions = %#v, want two conditions", conditions)
	}
	accepted := conditions[0]
	if accepted.Type != string(gatewayv1.PolicyConditionAccepted) ||
		accepted.Status != metav1.ConditionFalse ||
		accepted.Reason != string(gatewayv1.PolicyReasonTargetNotFound) {
		t.Fatalf("Accepted condition = %#v, want Accepted=False/TargetNotFound", accepted)
	}
	resolved := conditions[1]
	if resolved.Type != string(gatewayv1.BackendTLSPolicyConditionResolvedRefs) ||
		resolved.Status != metav1.ConditionFalse {
		t.Fatalf("ResolvedRefs condition = %#v, want ResolvedRefs=False", resolved)
	}
}

func TestBackendTLSPolicyMountsScopedToTunnelBackends(t *testing.T) {
	ctx := context.Background()
	scheme := controllerTestScheme(t)
	tunnel := tunnelForOriginRuntimeTest()
	gw := gatewayForTunnelTest()
	route := routeForBackendTest("app-route", "apps", "app")
	relevant := backendTLSPolicyForTest("tls-app", "apps", "app", "app.example.com", "ca-app")
	unrelated := backendTLSPolicyForTest("tls-other", "other", "other", "other.example.com", "ca-other")
	reconciler := &CloudflareTunnelReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(tunnel, gw, route, relevant, unrelated, caConfigMapForTest("ca-app", "apps"), caConfigMapForTest("ca-other", "other")).
			Build(),
		Scheme: scheme,
	}

	policies, err := reconciler.backendTLSPoliciesForTunnel(ctx, tunnel)
	if err != nil {
		t.Fatalf("backendTLSPoliciesForTunnel() error = %v", err)
	}
	if len(policies) != 1 || policies[0].Namespace != "apps" || policies[0].Name != "tls-app" {
		t.Fatalf("backendTLSPoliciesForTunnel() = %#v, want apps/tls-app only", policies)
	}

	mounts, _, paths, err := reconciler.resolveOriginCAPoolMounts(ctx, tunnel, policies)
	if err != nil {
		t.Fatalf("resolveOriginCAPoolMounts() error = %v", err)
	}
	if _, ok := paths[types.NamespacedName{Namespace: "apps", Name: "tls-app"}]; !ok {
		t.Fatalf("paths = %#v, want apps/tls-app", paths)
	}
	if _, ok := paths[types.NamespacedName{Namespace: "other", Name: "tls-other"}]; ok {
		t.Fatalf("paths = %#v, did not want other/tls-other", paths)
	}
	if len(mounts) != 1 || mounts[0].Name != cloudflared.OriginCAPoolVolumeNameFor("backendtls", "apps", "tls-app") {
		t.Fatalf("mounts = %#v, want only relevant backendtls mount", mounts)
	}
}

func TestGeneratedOriginCASecretsScopedAndPruned(t *testing.T) {
	ctx := context.Background()
	scheme := controllerTestScheme(t)
	tunnel := tunnelForOriginRuntimeTest()
	relevant := backendTLSPolicyForTest("tls-app", "apps", "app", "app.example.com", "ca-app")
	unrelated := backendTLSPolicyForTest("tls-other", "other", "other", "other.example.com", "ca-other")
	staleName := generatedOriginCASecretName(tunnel.Name, "backendtls", unrelated.Namespace, unrelated.Name, "ca-other")
	stale := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      staleName,
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
	reconciler := &CloudflareTunnelReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(tunnel, relevant, unrelated, caConfigMapForTest("ca-app", "apps"), caConfigMapForTest("ca-other", "other"), stale).
			Build(),
		Scheme: scheme,
	}

	if err := reconciler.syncGeneratedOriginCASecrets(ctx, tunnel, []gatewayv1.BackendTLSPolicy{*relevant}); err != nil {
		t.Fatalf("syncGeneratedOriginCASecrets() error = %v", err)
	}
	relevantName := generatedOriginCASecretName(tunnel.Name, "backendtls", relevant.Namespace, relevant.Name, "ca-app")
	var relevantSecret corev1.Secret
	if err := reconciler.Get(ctx, types.NamespacedName{Namespace: tunnel.Namespace, Name: relevantName}, &relevantSecret); err != nil {
		t.Fatalf("get relevant generated secret: %v", err)
	}
	if string(relevantSecret.Data[cloudflared.DefaultOriginCAPoolSecretKey]) != "apps-ca" {
		t.Fatalf("relevant CA data = %q, want apps-ca", relevantSecret.Data[cloudflared.DefaultOriginCAPoolSecretKey])
	}
	var unrelatedSecret corev1.Secret
	if err := reconciler.Get(ctx, types.NamespacedName{Namespace: tunnel.Namespace, Name: staleName}, &unrelatedSecret); err == nil {
		t.Fatalf("unrelated generated secret still exists")
	}
	unrelatedName := generatedOriginCASecretName(tunnel.Name, "backendtls", unrelated.Namespace, unrelated.Name, "ca-other")
	if unrelatedName != staleName {
		t.Fatalf("test setup staleName = %q, unrelatedName = %q", staleName, unrelatedName)
	}
}

func TestSyncGeneratedOriginCASecretsConfigMapGetErrorPreservesExistingSecret(t *testing.T) {
	ctx := context.Background()
	scheme := controllerTestScheme(t)
	tunnel := tunnelForOriginRuntimeTest()
	policy := backendTLSPolicyForTest("tls-app", "apps", "app", "app.example.com", "ca-app")
	existingName := generatedOriginCASecretName(tunnel.Name, "backendtls", "apps", "tls-app", "ca-app")
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      existingName,
			Namespace: tunnel.Namespace,
			Labels: map[string]string{
				generatedOriginCASecretLabel:   "true",
				"app.kubernetes.io/instance":   tunnel.Name,
				"app.kubernetes.io/managed-by": "cfgate",
				"app.kubernetes.io/component":  "origin-ca-pool",
			},
		},
		Data: map[string][]byte{cloudflared.DefaultOriginCAPoolSecretKey: []byte("old-ca")},
	}

	t.Run("get error preserves existing secret", func(t *testing.T) {
		baseClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(tunnel.DeepCopy(), policy.DeepCopy(), existing.DeepCopy()).
			Build()
		sentinel := errors.New("cache unavailable")
		reconciler := &CloudflareTunnelReconciler{
			Client: &getErrorClient{
				Client: baseClient,
				getErr: sentinel,
				failFor: func(name types.NamespacedName, obj client.Object) bool {
					_, ok := obj.(*corev1.ConfigMap)
					return ok && name == (types.NamespacedName{Namespace: "apps", Name: "ca-app"})
				},
			},
			Scheme: scheme,
		}

		err := reconciler.syncGeneratedOriginCASecrets(ctx, tunnel, []gatewayv1.BackendTLSPolicy{*policy})
		if err == nil || !strings.Contains(err.Error(), "get BackendTLSPolicy CA ConfigMap apps/ca-app") {
			t.Fatalf("syncGeneratedOriginCASecrets() error = %v, want ConfigMap get error", err)
		}
		var got corev1.Secret
		if err := baseClient.Get(ctx, types.NamespacedName{Namespace: tunnel.Namespace, Name: existingName}, &got); err != nil {
			t.Fatalf("existing generated Secret was not preserved: %v", err)
		}
		if string(got.Data[cloudflared.DefaultOriginCAPoolSecretKey]) != "old-ca" {
			t.Fatalf("existing generated Secret data = %q, want old-ca", got.Data[cloudflared.DefaultOriginCAPoolSecretKey])
		}
	})

	t.Run("not found prunes stale secret", func(t *testing.T) {
		baseClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(tunnel.DeepCopy(), policy.DeepCopy(), existing.DeepCopy()).
			Build()
		reconciler := &CloudflareTunnelReconciler{
			Client: baseClient,
			Scheme: scheme,
		}

		if err := reconciler.syncGeneratedOriginCASecrets(ctx, tunnel, []gatewayv1.BackendTLSPolicy{*policy}); err != nil {
			t.Fatalf("syncGeneratedOriginCASecrets() error = %v", err)
		}
		var got corev1.Secret
		err := baseClient.Get(ctx, types.NamespacedName{Namespace: tunnel.Namespace, Name: existingName}, &got)
		if !apierrors.IsNotFound(err) {
			t.Fatalf("stale generated Secret get error = %v, want not found", err)
		}
	})
}

func originPolicyForTest(name string, ts time.Time) *cfgatev1alpha1.CloudflareOriginPolicy {
	return &cfgatev1alpha1.CloudflareOriginPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "apps",
			CreationTimestamp: metav1.NewTime(ts),
		},
		Spec: cfgatev1alpha1.CloudflareOriginPolicySpec{
			TargetRefs: []cfgatev1alpha1.OriginPolicyTargetReference{{
				Group:       gatewayv1.GroupName,
				Kind:        "HTTPRoute",
				Name:        "route",
				SectionName: "api",
			}},
		},
	}
}

func ptrWellKnown(v gatewayv1.WellKnownCACertificatesType) *gatewayv1.WellKnownCACertificatesType {
	return &v
}

func tunnelForOriginRuntimeTest() *cfgatev1alpha1.CloudflareTunnel {
	return &cfgatev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tunnel", Namespace: "cfgate"},
	}
}

func gatewayForTunnelTest() *gatewayv1.Gateway {
	return &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gateway",
			Namespace: "cfgate",
			Annotations: map[string]string{
				annotations.AnnotationTunnelRef: "cfgate/tunnel",
			},
		},
	}
}

func routeForBackendTest(name, namespace, serviceName string) *gatewayv1.HTTPRoute {
	gatewayNS := gatewayv1.Namespace("cfgate")
	port := gatewayv1.PortNumber(8443)
	return &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Namespace: &gatewayNS,
					Name:      "gateway",
				}},
			},
			Hostnames: []gatewayv1.Hostname{"app.example.com"},
			Rules: []gatewayv1.HTTPRouteRule{{
				BackendRefs: []gatewayv1.HTTPBackendRef{{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: gatewayv1.ObjectName(serviceName),
							Port: &port,
						},
					},
				}},
			}},
		},
	}
}

func backendTLSPolicyForTest(name, namespace, serviceName, hostname, caConfigMap string) *gatewayv1.BackendTLSPolicy {
	return &gatewayv1.BackendTLSPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: gatewayv1.BackendTLSPolicySpec{
			TargetRefs: []gatewayv1.LocalPolicyTargetReferenceWithSectionName{{
				LocalPolicyTargetReference: gatewayv1.LocalPolicyTargetReference{
					Group: "",
					Kind:  "Service",
					Name:  gatewayv1.ObjectName(serviceName),
				},
			}},
			Validation: gatewayv1.BackendTLSPolicyValidation{
				CACertificateRefs: []gatewayv1.LocalObjectReference{{
					Group: "",
					Kind:  "ConfigMap",
					Name:  gatewayv1.ObjectName(caConfigMap),
				}},
				Hostname: gatewayv1.PreciseHostname(hostname),
			},
		},
	}
}

func caConfigMapForTest(name, namespace string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data: map[string]string{
			cloudflared.DefaultOriginCAPoolSecretKey: namespace + "-ca",
		},
	}
}

func ctrlRequest(namespace, name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}
}

func logMessagesContain(messages []string, want string) bool {
	for _, message := range messages {
		if strings.Contains(message, want) {
			return true
		}
	}
	return false
}

type recordingLogSink struct {
	errors *[]string
}

func (s *recordingLogSink) Init(logr.RuntimeInfo) {}

func (s *recordingLogSink) Enabled(int) bool { return true }

func (s *recordingLogSink) Info(int, string, ...interface{}) {}

func (s *recordingLogSink) Error(err error, msg string, _ ...interface{}) {
	*s.errors = append(*s.errors, msg+": "+err.Error())
}

func (s *recordingLogSink) WithValues(...interface{}) logr.LogSink { return s }

func (s *recordingLogSink) WithName(string) logr.LogSink { return s }

type listErrorClient struct {
	client.Client
	listErr error
	failFor func(client.ObjectList) bool
}

func (c *listErrorClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if c.failFor != nil && c.failFor(list) {
		return c.listErr
	}
	return c.Client.List(ctx, list, opts...)
}

type getErrorClient struct {
	client.Client
	getErr  error
	failFor func(types.NamespacedName, client.Object) bool
}

func (c *getErrorClient) Get(ctx context.Context, name types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
	if c.failFor != nil && c.failFor(name, obj) {
		return c.getErr
	}
	return c.Client.Get(ctx, name, obj, opts...)
}

type statusUpdateErrorClient struct {
	client.Client
	updateErr error
}

func (c *statusUpdateErrorClient) Status() client.SubResourceWriter {
	return &statusUpdateErrorWriter{
		SubResourceWriter: c.Client.Status(),
		updateErr:         c.updateErr,
	}
}

type statusUpdateErrorWriter struct {
	client.SubResourceWriter
	updateErr error
}

func (w *statusUpdateErrorWriter) Update(context.Context, client.Object, ...client.SubResourceUpdateOption) error {
	return w.updateErr
}

func (w *statusUpdateErrorWriter) Apply(ctx context.Context, obj k8sruntime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
	return w.SubResourceWriter.Apply(ctx, obj, opts...)
}

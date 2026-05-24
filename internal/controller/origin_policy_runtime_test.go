package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
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

func ctrlRequest(namespace, name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}
}

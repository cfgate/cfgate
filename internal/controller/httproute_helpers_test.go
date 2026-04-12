package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/controller/annotations"
	"cfgate.io/cfgate/internal/controller/status"
)

func TestHTTPRouteHelperFinders(t *testing.T) {
	scheme := controllerTestScheme(t)
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "app"},
	}
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "app"},
	}
	policy := &cfgatev1alpha1.CloudflareAccessPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "app"},
	}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route",
			Namespace: "app",
			Annotations: map[string]string{
				annotations.AnnotationAccessPolicy: "app/policy",
			},
		},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name: "gateway",
				}},
			},
			Rules: []gatewayv1.HTTPRouteRule{{
				BackendRefs: []gatewayv1.HTTPBackendRef{{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "svc",
						},
					},
				}},
			}},
		},
	}

	r := newHTTPRouteTestReconciler(t, scheme, gateway, service, policy, route)
	ctx := context.Background()

	reqs := r.findRoutesForGateway(ctx, gateway)
	assertRequests(t, reqs, types.NamespacedName{Namespace: "app", Name: "route"})

	reqs = r.findRoutesForService(ctx, service)
	assertRequests(t, reqs, types.NamespacedName{Namespace: "app", Name: "route"})

	reqs = r.findRoutesForAccessPolicy(ctx, policy)
	assertRequests(t, reqs, types.NamespacedName{Namespace: "app", Name: "route"})
}

func TestIsCfgateParentRef(t *testing.T) {
	scheme := controllerTestScheme(t)
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "app"},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "cfgate",
		},
	}
	gatewayClass := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "cfgate"},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: gatewayv1.GatewayController(GatewayControllerName),
		},
	}
	route := &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"}}

	r := newHTTPRouteTestReconciler(t, scheme, gateway, gatewayClass)
	isCfgate, err := r.isCfgateParentRef(context.Background(), route, gatewayv1.ParentReference{Name: "gateway"})
	if err != nil {
		t.Fatalf("isCfgateParentRef() error = %v", err)
	}
	if !isCfgate {
		t.Fatal("isCfgateParentRef() = false, want true")
	}

	otherClass := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "other"},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: "example.com/other",
		},
	}
	gateway.Spec.GatewayClassName = "other"
	r = newHTTPRouteTestReconciler(t, scheme, gateway, otherClass)
	isCfgate, err = r.isCfgateParentRef(context.Background(), route, gatewayv1.ParentReference{Name: "gateway"})
	if err != nil {
		t.Fatalf("isCfgateParentRef() other error = %v", err)
	}
	if isCfgate {
		t.Fatal("isCfgateParentRef() = true, want false")
	}
}

func TestValidateParentRef(t *testing.T) {
	scheme := controllerTestScheme(t)
	listenerName := gatewayv1.SectionName("http")
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gateway",
			Namespace: "app",
			Annotations: map[string]string{
				annotations.AnnotationTunnelRef: "cfgate-system/tunnel",
			},
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "cfgate",
			Listeners: []gatewayv1.Listener{{
				Name: listenerName,
			}},
		},
	}
	gatewayClass := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "cfgate"},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: gatewayv1.GatewayController(GatewayControllerName),
		},
	}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"},
	}
	r := newHTTPRouteTestReconciler(t, scheme, gateway, gatewayClass)

	statusResult := r.validateParentRef(context.Background(), route, gatewayv1.ParentReference{
		Name:        "gateway",
		SectionName: &listenerName,
	})
	if len(statusResult.Conditions) != 1 || statusResult.Conditions[0].Status != metav1.ConditionTrue {
		t.Fatalf("validateParentRef() = %+v, want accepted condition", statusResult.Conditions)
	}

	gateway = gateway.DeepCopy()
	delete(gateway.Annotations, annotations.AnnotationTunnelRef)
	r = newHTTPRouteTestReconciler(t, scheme, gateway, gatewayClass)
	statusResult = r.validateParentRef(context.Background(), route, gatewayv1.ParentReference{Name: "gateway"})
	if statusResult.Conditions[0].Reason != status.ReasonNoTunnelRef {
		t.Fatalf("validateParentRef() reason = %q, want %q", statusResult.Conditions[0].Reason, status.ReasonNoTunnelRef)
	}
}

func TestResolveBackends(t *testing.T) {
	scheme := controllerTestScheme(t)
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "app"},
	}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{{
				BackendRefs: []gatewayv1.HTTPBackendRef{{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "svc"},
					},
				}},
			}},
		},
	}

	r := newHTTPRouteTestReconciler(t, scheme, service)
	condition := r.resolveBackends(context.Background(), route)
	if condition.Status != metav1.ConditionTrue {
		t.Fatalf("resolveBackends() status = %q, want %q", condition.Status, metav1.ConditionTrue)
	}

	route.Spec.Rules[0].BackendRefs[0].Name = "missing"
	condition = r.resolveBackends(context.Background(), route)
	if condition.Status != metav1.ConditionFalse || condition.Reason != status.ReasonBackendNotFound {
		t.Fatalf("resolveBackends() = %+v, want missing backend condition", condition)
	}
}

func TestResolveAccessPolicy(t *testing.T) {
	scheme := controllerTestScheme(t)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"},
	}
	recorder := &fakeEventRecorder{}
	r := newHTTPRouteTestReconcilerWithRecorder(t, scheme, recorder)

	condition, ok := r.resolveAccessPolicy(context.Background(), route)
	if ok {
		t.Fatalf("resolveAccessPolicy() ok = true, want false; condition = %+v", condition)
	}

	route.Annotations = map[string]string{annotations.AnnotationAccessPolicy: "bad/ref/format"}
	condition, ok = r.resolveAccessPolicy(context.Background(), route)
	if !ok || condition.Reason != status.ReasonInvalidPolicyRef {
		t.Fatalf("resolveAccessPolicy() = (%+v, %v), want invalid ref condition", condition, ok)
	}

	policy := &cfgatev1alpha1.CloudflareAccessPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "app"},
	}
	route.Annotations = map[string]string{annotations.AnnotationAccessPolicy: "policy"}
	r = newHTTPRouteTestReconcilerWithRecorder(t, scheme, recorder, policy)
	condition, ok = r.resolveAccessPolicy(context.Background(), route)
	if !ok || condition.Status != metav1.ConditionTrue || condition.Reason != status.ReasonResolved {
		t.Fatalf("resolveAccessPolicy() = (%+v, %v), want resolved condition", condition, ok)
	}
}

func TestValidatePathTypes(t *testing.T) {
	scheme := controllerTestScheme(t)
	recorder := &fakeEventRecorder{}
	r := newHTTPRouteTestReconcilerWithRecorder(t, scheme, recorder)

	exactType := gatewayv1.PathMatchExact
	regexType := gatewayv1.PathMatchRegularExpression
	exactPath := "/exact"
	regexPath := "^/re"
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{{
				Matches: []gatewayv1.HTTPRouteMatch{
					{Path: &gatewayv1.HTTPPathMatch{Type: &exactType, Value: &exactPath}},
					{Path: &gatewayv1.HTTPPathMatch{Type: &regexType, Value: &regexPath}},
				},
			}},
		},
	}

	r.validatePathTypes(route)

	assertEventContains(t, recorder, "UnsupportedPathType")
	assertEventContains(t, recorder, "PathTypeNotice")
}

func TestParsePolicyRef(t *testing.T) {
	ns, name, err := parsePolicyRef("policy", "default")
	if err != nil {
		t.Fatalf("parsePolicyRef() error = %v", err)
	}
	if ns != "default" || name != "policy" {
		t.Fatalf("parsePolicyRef() = (%q, %q), want (%q, %q)", ns, name, "default", "policy")
	}

	_, _, err = parsePolicyRef("too/many/parts", "default")
	if err == nil {
		t.Fatal("parsePolicyRef() error = nil, want invalid ref error")
	}
}

func TestHTTPRouteReconcileSkipsEmptyParentStatusWrite(t *testing.T) {
	scheme := controllerTestScheme(t)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route",
			Namespace: "app",
		},
	}

	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(route).Build()
	statusWriter := &countingStatusWriter{}
	r := &HTTPRouteReconciler{
		Client:   &statusTrackingClient{Client: baseClient, writer: statusWriter},
		Scheme:   scheme,
		Recorder: &fakeEventRecorder{},
	}

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "app", Name: "route"},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if statusWriter.updates != 0 {
		t.Fatalf("Status().Update() called %d times, want 0", statusWriter.updates)
	}
	if result.RequeueAfter != 5*time.Minute {
		t.Fatalf("Reconcile() RequeueAfter = %v, want %v", result.RequeueAfter, 5*time.Minute)
	}
}

func newHTTPRouteTestReconciler(t *testing.T, scheme *runtime.Scheme, objects ...client.Object) *HTTPRouteReconciler {
	t.Helper()
	return newHTTPRouteTestReconcilerWithRecorder(t, scheme, &fakeEventRecorder{}, objects...)
}

func newHTTPRouteTestReconcilerWithRecorder(t *testing.T, scheme *runtime.Scheme, recorder *fakeEventRecorder, objects ...client.Object) *HTTPRouteReconciler {
	t.Helper()
	return &HTTPRouteReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(),
		Scheme:   scheme,
		Recorder: recorder,
	}
}

func controllerTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(corev1) error = %v", err)
	}
	if err := cfgatev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(cfgate) error = %v", err)
	}
	if err := gatewayv1.Install(scheme); err != nil {
		t.Fatalf("Install(gateway/v1) error = %v", err)
	}
	return scheme
}

func assertRequests(t *testing.T, reqs []reconcile.Request, want types.NamespacedName) {
	t.Helper()
	if len(reqs) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(reqs))
	}
	if reqs[0].NamespacedName != want {
		t.Fatalf("request = %v, want %v", reqs[0].NamespacedName, want)
	}
}

func assertEventContains(t *testing.T, recorder *fakeEventRecorder, want string) {
	t.Helper()
	for _, event := range recorder.events {
		if strings.Contains(event, want) {
			return
		}
	}
	t.Fatalf("did not receive event containing %q", want)
}

type fakeEventRecorder struct {
	events []string
}

func (r *fakeEventRecorder) Eventf(regarding runtime.Object, related runtime.Object, eventtype, reason, action, note string, args ...interface{}) {
	_ = regarding
	_ = related
	r.events = append(r.events, strings.Join([]string{
		eventtype,
		reason,
		action,
		note,
	}, " "))
}

type statusTrackingClient struct {
	client.Client
	writer client.StatusWriter
}

func (c *statusTrackingClient) Status() client.StatusWriter {
	return c.writer
}

type countingStatusWriter struct {
	updates int
}

func (w *countingStatusWriter) Create(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	return nil
}

func (w *countingStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	w.updates++
	return nil
}

func (w *countingStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return nil
}

func (w *countingStatusWriter) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
	return nil
}

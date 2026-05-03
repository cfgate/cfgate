package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/controller/annotations"
	"cfgate.io/cfgate/internal/controller/features"
	"cfgate.io/cfgate/internal/controller/status"
)

func TestApplicationAncestorsUsesAccessControllerName(t *testing.T) {
	targets := []accessApplicationTarget{{
		Ref: cfgatev1alpha1.PolicyTargetReference{
			Group: "gateway.networking.k8s.io",
			Kind:  "HTTPRoute",
			Name:  "route",
		},
	}}

	ancestors := applicationAncestors(targets, 7)
	if len(ancestors) != 1 {
		t.Fatalf("applicationAncestors() got %d ancestors, want 1", len(ancestors))
	}
	if ancestors[0].ControllerName != "cfgate.io/cloudflare-access-controller" {
		t.Fatalf("ControllerName = %q, want cfgate.io/cloudflare-access-controller", ancestors[0].ControllerName)
	}

	for _, condition := range ancestors[0].Conditions {
		if condition.Type == "Accepted" && condition.Status == metav1.ConditionTrue {
			return
		}
	}
	t.Fatalf("Accepted=True condition missing from ancestors: %#v", ancestors[0].Conditions)
}

func TestValidateAccessApplicationPath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{name: "empty", path: "", wantErr: true},
		{name: "missing leading slash", path: "admin", wantErr: true},
		{name: "query string", path: "/admin?debug=true", wantErr: true},
		{name: "fragment", path: "/admin#section", wantErr: true},
		{name: "normal path", path: "/admin"},
		{name: "colon segment", path: "/api:v1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAccessApplicationPath(tt.path)
			if tt.wantErr && err == nil {
				t.Fatalf("validateAccessApplicationPath(%q) got nil error, want error", tt.path)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validateAccessApplicationPath(%q) error = %v, want nil", tt.path, err)
			}
		})
	}
}

func TestDeleteStaleAccessApplications(t *testing.T) {
	deleted := []string{}
	mock := cloudflare.NewMockClient()
	mock.DeleteAccessApplicationFunc = func(_ context.Context, accountID, appID string) error {
		if accountID != "account-1" {
			t.Fatalf("accountID = %q, want account-1", accountID)
		}
		deleted = append(deleted, appID)
		return nil
	}
	accessService := cloudflare.NewAccessService(mock, logr.Discard())
	existing := []cfgatev1alpha1.AccessApplicationObserved{
		{ID: "keep", Domain: "app.example.com"},
		{ID: "drop", Domain: "old.example.com"},
		{Domain: "missing-id.example.com"},
	}
	desired := map[string]struct{}{"app.example.com": {}}

	if err := deleteStaleAccessApplications(context.Background(), accessService, "account-1", existing, desired); err != nil {
		t.Fatalf("deleteStaleAccessApplications() error = %v", err)
	}
	if got := strings.Join(deleted, ","); got != "drop" {
		t.Fatalf("deleted applications = %q, want drop", got)
	}
}

func TestBlockApplicationDeletionEmitsCleanupFailedBeforeBudget(t *testing.T) {
	reconciler := &CloudflareAccessApplicationReconciler{Recorder: &accessApplicationEventRecorder{}}
	app := accessApplicationWithDeletionTimestamp(time.Now())

	result, err := reconciler.blockApplicationDeletion(context.Background(), app, "Failed to delete Access application app-1: boom")
	if err != nil {
		t.Fatalf("blockApplicationDeletion() error = %v", err)
	}
	if result.RequeueAfter != accessDeletionRequeueInterval {
		t.Fatalf("RequeueAfter = %v, want %v", result.RequeueAfter, accessDeletionRequeueInterval)
	}

	recorder := reconciler.Recorder.(*accessApplicationEventRecorder)
	assertAccessApplicationEventContains(t, recorder, "CleanupFailed")
	assertAccessApplicationEventNotContains(t, recorder, "CleanupBlocked")
}

func TestBlockApplicationDeletionEmitsCleanupBlockedAfterBudget(t *testing.T) {
	reconciler := &CloudflareAccessApplicationReconciler{Recorder: &accessApplicationEventRecorder{}}
	app := accessApplicationWithDeletionTimestamp(time.Now().Add(-accessDeletionRetryBudget - time.Second))

	result, err := reconciler.blockApplicationDeletion(context.Background(), app, "Failed to delete Access application app-1: boom")
	if err != nil {
		t.Fatalf("blockApplicationDeletion() error = %v", err)
	}
	if result.RequeueAfter != accessDeletionRequeueInterval {
		t.Fatalf("RequeueAfter = %v, want %v", result.RequeueAfter, accessDeletionRequeueInterval)
	}

	recorder := reconciler.Recorder.(*accessApplicationEventRecorder)
	assertAccessApplicationEventContains(t, recorder, "CleanupBlocked")
	assertAccessApplicationEventContains(t, recorder, "blocked after")
	assertAccessApplicationEventContains(t, recorder, "Set annotation cfgate.io/deletion-policy=orphan")
}

func TestReconcileApplicationDeleteRemovesFinalizerWhenNoObservedApplications(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := cfgatev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	app := &cfgatev1alpha1.CloudflareAccessApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "app",
			Namespace:  "default",
			Finalizers: []string{accessApplicationFinalizer},
		},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(app).Build()
	reconciler := &CloudflareAccessApplicationReconciler{Client: k8sClient}

	if _, err := reconciler.reconcileApplicationDelete(ctx, app); err != nil {
		t.Fatalf("reconcileApplicationDelete() error = %v", err)
	}

	var current cfgatev1alpha1.CloudflareAccessApplication
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: app.Name, Namespace: app.Namespace}, &current); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(current.Finalizers) != 0 {
		t.Fatalf("finalizers = %v, want none", current.Finalizers)
	}
}

func TestAccessApplicationReconcileAddsFinalizer(t *testing.T) {
	ctx := context.Background()
	app := &cfgatev1alpha1.CloudflareAccessApplication{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "app"},
		Spec: cfgatev1alpha1.CloudflareAccessApplicationSpec{
			TargetRef: &cfgatev1alpha1.PolicyTargetReference{Group: gwapiv1.GroupName, Kind: "HTTPRoute", Name: "route"},
		},
	}
	reconciler := newAccessAppReconciler(t, cloudflare.NewMockClient(), app)

	result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "app", Namespace: "app"}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if !resultRequeues(result) {
		t.Fatalf("Reconcile() Requeue = false, want true")
	}
	var current cfgatev1alpha1.CloudflareAccessApplication
	if err := reconciler.Get(ctx, types.NamespacedName{Name: "app", Namespace: "app"}, &current); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !containsString(current.Finalizers, accessApplicationFinalizer) {
		t.Fatalf("finalizers = %#v, want %s", current.Finalizers, accessApplicationFinalizer)
	}
}

func TestAccessApplicationReconcileHTTPRouteSuccess(t *testing.T) {
	ctx := context.Background()
	hostname := gwapiv1.Hostname("app.example.com")
	route := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"},
		Spec:       gwapiv1.HTTPRouteSpec{Hostnames: []gwapiv1.Hostname{hostname}},
	}
	policy := readyAccessPolicy("app", "policy", "account-1", "policy-1")
	app := appWithFinalizer("app", "app")
	app.Spec.CloudflareRef = &cfgatev1alpha1.CloudflareSecretRef{Name: "cf", AccountID: "account-1"}
	app.Spec.TargetRef = &cfgatev1alpha1.PolicyTargetReference{Group: gwapiv1.GroupName, Kind: "HTTPRoute", Name: "route"}
	app.Spec.PolicyRefs = []cfgatev1alpha1.AccessPolicyReference{{Name: "policy"}}

	var created cloudflare.ApplicationParams
	mock := cloudflare.NewMockClient()
	mock.ListAccessTagsFunc = func(context.Context, string) ([]cloudflare.AccessTag, error) {
		return []cloudflare.AccessTag{{Name: accessApplicationTag}, {Name: accessApplicationOwnerTag(app)}}, nil
	}
	mock.ListAccessApplicationsFunc = func(context.Context, string) ([]cloudflare.AccessApplication, error) {
		return nil, nil
	}
	mock.CreateAccessApplicationFunc = func(_ context.Context, accountID string, params cloudflare.ApplicationParams) (*cloudflare.AccessApplication, error) {
		if accountID != "account-1" {
			t.Fatalf("CreateAccessApplication accountID = %q, want account-1", accountID)
		}
		created = params
		return &cloudflare.AccessApplication{ID: "app-id", AUD: "aud", Domain: params.Domain}, nil
	}
	reconciler := newAccessAppReconciler(t, mock, route, policy, app)

	result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "app", Namespace: "app"}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != accessApplicationRequeueAfterSuccess {
		t.Fatalf("RequeueAfter = %v, want %v", result.RequeueAfter, accessApplicationRequeueAfterSuccess)
	}
	var current cfgatev1alpha1.CloudflareAccessApplication
	if err := reconciler.Get(ctx, types.NamespacedName{Name: "app", Namespace: "app"}, &current); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(current.Status.Applications) != 1 || current.Status.Applications[0].ID != "app-id" ||
		current.Status.Applications[0].Domain != "app.example.com" {
		t.Fatalf("applications = %#v", current.Status.Applications)
	}
	if current.Status.AttachedTargets != 1 {
		t.Fatalf("AttachedTargets = %d, want 1", current.Status.AttachedTargets)
	}
	if current.Status.AccountID != "account-1" {
		t.Fatalf("AccountID = %q, want account-1", current.Status.AccountID)
	}
	if current.Status.CredentialSecretRef == nil ||
		current.Status.CredentialSecretRef.Name != "cf" ||
		current.Status.CredentialSecretRef.Namespace != "app" {
		t.Fatalf("CredentialSecretRef = %+v, want app/cf", current.Status.CredentialSecretRef)
	}
	if !status.ConditionTrue(current.Status.Conditions, status.ConditionTypeReady) {
		t.Fatalf("Ready condition missing from %#v", current.Status.Conditions)
	}
	if !reflect.DeepEqual(created.Destinations, []string{"app.example.com"}) {
		t.Fatalf("Destinations = %#v, want app.example.com", created.Destinations)
	}
	if !reflect.DeepEqual(created.Policies, []cloudflare.ApplicationPolicyLink{{ID: "policy-1", Precedence: 1}}) {
		t.Fatalf("Policies = %#v", created.Policies)
	}
	if !reflect.DeepEqual(created.Tags, []string{accessApplicationTag, accessApplicationOwnerTag(app)}) {
		t.Fatalf("Tags = %#v", created.Tags)
	}
}

func TestAccessApplicationReconcileInheritedCredentialsStoresCleanupRef(t *testing.T) {
	ctx := context.Background()
	route := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"},
		Spec: gwapiv1.HTTPRouteSpec{
			Hostnames:       []gwapiv1.Hostname{"app.example.com"},
			CommonRouteSpec: gwapiv1.CommonRouteSpec{ParentRefs: []gwapiv1.ParentReference{{Name: "gateway"}}},
		},
	}
	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "app", Annotations: map[string]string{annotations.AnnotationTunnelRef: "tunnel"}},
	}
	tunnel := &cfgatev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tunnel", Namespace: "app"},
		Spec: cfgatev1alpha1.CloudflareTunnelSpec{Cloudflare: cfgatev1alpha1.CloudflareConfig{
			SecretRef: cfgatev1alpha1.SecretRef{Name: "cf", Namespace: "secrets"},
		}},
		Status: cfgatev1alpha1.CloudflareTunnelStatus{AccountID: "account-1"},
	}
	policy := readyAccessPolicy("app", "policy", "account-1", "policy-1")
	app := appWithFinalizer("app", "app")
	app.Spec.TargetRef = &cfgatev1alpha1.PolicyTargetReference{Group: gwapiv1.GroupName, Kind: "HTTPRoute", Name: "route"}
	app.Spec.PolicyRefs = []cfgatev1alpha1.AccessPolicyReference{{Name: "policy"}}

	mock := cloudflare.NewMockClient()
	mock.ListAccessTagsFunc = func(context.Context, string) ([]cloudflare.AccessTag, error) {
		return []cloudflare.AccessTag{{Name: accessApplicationTag}, {Name: accessApplicationOwnerTag(app)}}, nil
	}
	mock.ListAccessApplicationsFunc = func(context.Context, string) ([]cloudflare.AccessApplication, error) {
		return nil, nil
	}
	mock.CreateAccessApplicationFunc = func(_ context.Context, _ string, params cloudflare.ApplicationParams) (*cloudflare.AccessApplication, error) {
		return &cloudflare.AccessApplication{ID: "app-id", AUD: "aud", Domain: params.Domain}, nil
	}
	reconciler := newAccessAppReconciler(t, mock, route, gateway, tunnel, policy, app)

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "app", Namespace: "app"}}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	var current cfgatev1alpha1.CloudflareAccessApplication
	if err := reconciler.Get(ctx, types.NamespacedName{Name: "app", Namespace: "app"}, &current); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if current.Status.AccountID != "account-1" ||
		current.Status.CredentialSecretRef == nil ||
		current.Status.CredentialSecretRef.Name != "cf" ||
		current.Status.CredentialSecretRef.Namespace != "secrets" {
		t.Fatalf("status credentials = %q/%+v, want account-1 secrets/cf", current.Status.AccountID, current.Status.CredentialSecretRef)
	}
}

func TestAccessApplicationReconcileMultipleTargetsDeletesStaleStatusApp(t *testing.T) {
	ctx := context.Background()
	hostname := gwapiv1.Hostname("app.example.com")
	route := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"},
		Spec:       gwapiv1.HTTPRouteSpec{Hostnames: []gwapiv1.Hostname{hostname}},
	}
	app := appWithFinalizer("app", "app")
	app.Spec.CloudflareRef = &cfgatev1alpha1.CloudflareSecretRef{Name: "cf", AccountID: "account-1"}
	app.Spec.TargetRef = &cfgatev1alpha1.PolicyTargetReference{Group: gwapiv1.GroupName, Kind: "HTTPRoute", Name: "route"}
	app.Status.Applications = []cfgatev1alpha1.AccessApplicationObserved{
		{ID: "keep", Domain: "app.example.com"},
		{ID: "stale", Domain: "old.example.com"},
	}
	deleted := ""
	mock := cloudflare.NewMockClient()
	mock.ListAccessTagsFunc = func(context.Context, string) ([]cloudflare.AccessTag, error) {
		return []cloudflare.AccessTag{{Name: accessApplicationTag}, {Name: accessApplicationOwnerTag(app)}}, nil
	}
	mock.GetAccessApplicationFunc = func(context.Context, string, string) (*cloudflare.AccessApplication, error) {
		return &cloudflare.AccessApplication{ID: "keep", AUD: "aud", Domain: "app.example.com", Tags: []string{accessApplicationTag, accessApplicationOwnerTag(app)}, Destinations: []string{"app.example.com"}}, nil
	}
	mock.UpdateAccessApplicationFunc = func(_ context.Context, _ string, appID string, params cloudflare.ApplicationParams) (*cloudflare.AccessApplication, error) {
		return &cloudflare.AccessApplication{ID: appID, AUD: "aud", Domain: params.Domain}, nil
	}
	mock.DeleteAccessApplicationFunc = func(_ context.Context, _ string, appID string) error {
		deleted = appID
		return nil
	}
	reconciler := newAccessAppReconciler(t, mock, route, app)

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "app", Namespace: "app"}}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if deleted != "stale" {
		t.Fatalf("deleted = %q, want stale", deleted)
	}
	var current cfgatev1alpha1.CloudflareAccessApplication
	if err := reconciler.Get(ctx, types.NamespacedName{Name: "app", Namespace: "app"}, &current); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(current.Status.Applications) != 1 || current.Status.Applications[0].ID != "keep" {
		t.Fatalf("applications = %#v, want keep only", current.Status.Applications)
	}
}

func TestAccessApplicationReconcileTargetErrors(t *testing.T) {
	regexPathType := gwapiv1.PathMatchRegularExpression
	regexPath := "/v[0-9]+"
	gatewayHostless := &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "app"}}
	targetNS := "target"
	crossRoute := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: targetNS},
		Spec:       gwapiv1.HTTPRouteSpec{Hostnames: []gwapiv1.Hostname{"cross.example.com"}},
	}
	regexRoute := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"},
		Spec: gwapiv1.HTTPRouteSpec{
			Hostnames: []gwapiv1.Hostname{"app.example.com"},
			Rules: []gwapiv1.HTTPRouteRule{{
				Name: sectionName("admin"),
				Matches: []gwapiv1.HTTPRouteMatch{{
					Path: &gwapiv1.HTTPPathMatch{Type: &regexPathType, Value: &regexPath},
				}},
			}},
		},
	}

	tests := []struct {
		name       string
		ref        cfgatev1alpha1.PolicyTargetReference
		objects    []client.Object
		condition  string
		wantReason string
	}{
		{
			name:       "missing HTTPRoute",
			ref:        cfgatev1alpha1.PolicyTargetReference{Group: gwapiv1.GroupName, Kind: "HTTPRoute", Name: "missing"},
			condition:  status.ConditionTypeTargetsResolved,
			wantReason: status.ReasonTargetNotFound,
		},
		{
			name:       "regex path match",
			ref:        cfgatev1alpha1.PolicyTargetReference{Group: gwapiv1.GroupName, Kind: "HTTPRoute", Name: "route", SectionName: stringPtr("admin")},
			objects:    []client.Object{regexRoute},
			condition:  status.ConditionTypeTargetsResolved,
			wantReason: status.ReasonUnsupportedPathMatch,
		},
		{
			name:       "cross namespace target denied",
			ref:        cfgatev1alpha1.PolicyTargetReference{Group: gwapiv1.GroupName, Kind: "HTTPRoute", Name: "route", Namespace: &targetNS},
			objects:    []client.Object{crossRoute},
			condition:  status.ConditionTypeReferenceGrantValid,
			wantReason: status.ReasonReferenceGrantRequired,
		},
		{
			name:       "gateway listener missing hostname",
			ref:        cfgatev1alpha1.PolicyTargetReference{Group: gwapiv1.GroupName, Kind: "Gateway", Name: "gateway", SectionName: stringPtr("https")},
			objects:    []client.Object{gatewayHostless},
			condition:  status.ConditionTypeTargetsResolved,
			wantReason: status.ReasonTargetResolutionFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := appWithFinalizer("app", "app")
			app.Spec.TargetRef = &tt.ref
			app.Spec.CloudflareRef = &cfgatev1alpha1.CloudflareSecretRef{Name: "cf", AccountID: "account-1"}
			objects := append([]client.Object{app}, tt.objects...)
			reconciler := newAccessAppReconciler(t, cloudflare.NewMockClient(), objects...)
			if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "app", Namespace: "app"}}); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			var current cfgatev1alpha1.CloudflareAccessApplication
			if err := reconciler.Get(context.Background(), types.NamespacedName{Name: "app", Namespace: "app"}, &current); err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			condition := status.FindCondition(current.Status.Conditions, tt.condition)
			if condition == nil || condition.Reason != tt.wantReason {
				t.Fatalf("%s condition = %+v, want reason %s", tt.condition, condition, tt.wantReason)
			}
		})
	}
}

func TestResolveApplicationTarget(t *testing.T) {
	route := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "route",
			Namespace:   "app",
			Annotations: map[string]string{annotations.AnnotationHostname: "annotated.example.com"},
		},
		Spec: gwapiv1.HTTPRouteSpec{
			Hostnames: []gwapiv1.Hostname{"ignored.example.com"},
			Rules: []gwapiv1.HTTPRouteRule{
				{Name: sectionName("prefix"), Matches: []gwapiv1.HTTPRouteMatch{{Path: &gwapiv1.HTTPPathMatch{Type: pathMatchType(gwapiv1.PathMatchPathPrefix), Value: stringPtr("/api")}}}},
				{Name: sectionName("exact"), Matches: []gwapiv1.HTTPRouteMatch{{Path: &gwapiv1.HTTPPathMatch{Type: pathMatchType(gwapiv1.PathMatchExact), Value: stringPtr("/login")}}}},
				{Name: sectionName("empty")},
			},
		},
	}
	gatewayHost := gwapiv1.Hostname("gw.example.com")
	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "app"},
		Spec:       gwapiv1.GatewaySpec{Listeners: []gwapiv1.Listener{{Name: "https", Hostname: &gatewayHost}}},
	}
	crossNS := "target"
	crossRoute := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "cross", Namespace: crossNS},
		Spec:       gwapiv1.HTTPRouteSpec{Hostnames: []gwapiv1.Hostname{"cross.example.com"}},
	}
	grant := referenceGrant("app", crossNS, "HTTPRoute", "cross")
	reconciler := newAccessAppReconciler(t, cloudflare.NewMockClient(), route, gateway, crossRoute, grant)

	app := appWithFinalizer("app", "app")
	tests := []struct {
		name       string
		ref        cfgatev1alpha1.PolicyTargetReference
		path       string
		wantDomain []string
	}{
		{
			name:       "HTTPRoute annotation hostname overrides spec",
			ref:        cfgatev1alpha1.PolicyTargetReference{Group: gwapiv1.GroupName, Kind: "HTTPRoute", Name: "route"},
			wantDomain: []string{"annotated.example.com"},
		},
		{
			name:       "HTTPRoute named prefix rule",
			ref:        cfgatev1alpha1.PolicyTargetReference{Group: gwapiv1.GroupName, Kind: "HTTPRoute", Name: "route", SectionName: stringPtr("prefix")},
			wantDomain: []string{"annotated.example.com/api"},
		},
		{
			name:       "HTTPRoute named exact rule",
			ref:        cfgatev1alpha1.PolicyTargetReference{Group: gwapiv1.GroupName, Kind: "HTTPRoute", Name: "route", SectionName: stringPtr("exact")},
			wantDomain: []string{"annotated.example.com/login"},
		},
		{
			name:       "HTTPRoute named empty rule",
			ref:        cfgatev1alpha1.PolicyTargetReference{Group: gwapiv1.GroupName, Kind: "HTTPRoute", Name: "route", SectionName: stringPtr("empty")},
			wantDomain: []string{"annotated.example.com"},
		},
		{
			name:       "application path override",
			ref:        cfgatev1alpha1.PolicyTargetReference{Group: gwapiv1.GroupName, Kind: "HTTPRoute", Name: "route", SectionName: stringPtr("prefix")},
			path:       "/override",
			wantDomain: []string{"annotated.example.com/override"},
		},
		{
			name:       "Gateway target",
			ref:        cfgatev1alpha1.PolicyTargetReference{Group: gwapiv1.GroupName, Kind: "Gateway", Name: "gateway"},
			wantDomain: []string{"gw.example.com"},
		},
		{
			name:       "cross namespace target with ReferenceGrant",
			ref:        cfgatev1alpha1.PolicyTargetReference{Group: gwapiv1.GroupName, Kind: "HTTPRoute", Name: "cross", Namespace: &crossNS},
			wantDomain: []string{"cross.example.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app.Spec.Application.Path = tt.path
			got, granted, err := reconciler.resolveApplicationTarget(context.Background(), app, tt.ref)
			if err != nil {
				t.Fatalf("resolveApplicationTarget() error = %v", err)
			}
			if !granted {
				t.Fatal("resolveApplicationTarget() granted = false, want true")
			}
			var domains []string
			for _, target := range got {
				domains = append(domains, target.Domain)
			}
			if !reflect.DeepEqual(domains, tt.wantDomain) {
				t.Fatalf("domains = %#v, want %#v", domains, tt.wantDomain)
			}
		})
	}
}

func TestResolveApplicationCredentials(t *testing.T) {
	ctx := context.Background()
	mock := cloudflare.NewMockClient()
	mock.GetAccountByNameFunc = func(_ context.Context, name string) (*cloudflare.Account, error) {
		if name != "prod" {
			t.Fatalf("GetAccountByName name = %q, want prod", name)
		}
		return &cloudflare.Account{ID: "account-by-name", Name: name}, nil
	}
	reconciler := newAccessAppReconciler(t, mock)

	app := appWithFinalizer("app", "app")
	app.Spec.CloudflareRef = &cfgatev1alpha1.CloudflareSecretRef{Name: "cf", AccountID: "account-1"}
	creds, err := reconciler.resolveApplicationCredentials(ctx, app, []accessApplicationTarget{
		{Kind: "Gateway", Namespace: "app", Name: "missing-a"},
		{Kind: "Gateway", Namespace: "app", Name: "missing-b"},
	})
	if err != nil {
		t.Fatalf("resolveApplicationCredentials() explicit error = %v", err)
	}
	if creds.AccountID != "account-1" || creds.Service.Client() != mock {
		t.Fatalf("explicit credentials = (%s, %#v)", creds.AccountID, creds.Service.Client())
	}
	if creds.CredentialSecretRef == nil || creds.CredentialSecretRef.Name != "cf" || creds.CredentialSecretRef.Namespace != "app" {
		t.Fatalf("explicit CredentialSecretRef = %+v, want app/cf", creds.CredentialSecretRef)
	}

	app.Spec.CloudflareRef = &cfgatev1alpha1.CloudflareSecretRef{Name: "cf", AccountName: "prod"}
	creds, err = reconciler.resolveApplicationCredentials(ctx, app, []accessApplicationTarget{{Kind: "Gateway", Namespace: "app", Name: "gateway"}})
	if err != nil {
		t.Fatalf("resolveApplicationCredentials() account name error = %v", err)
	}
	if creds.AccountID != "account-by-name" {
		t.Fatalf("accountID = %q, want account-by-name", creds.AccountID)
	}

	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "app", Annotations: map[string]string{annotations.AnnotationTunnelRef: "tunnel"}},
	}
	route := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"},
		Spec:       gwapiv1.HTTPRouteSpec{CommonRouteSpec: gwapiv1.CommonRouteSpec{ParentRefs: []gwapiv1.ParentReference{{Name: "gateway"}}}},
	}
	tunnel := &cfgatev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tunnel", Namespace: "app"},
		Spec: cfgatev1alpha1.CloudflareTunnelSpec{Cloudflare: cfgatev1alpha1.CloudflareConfig{
			SecretRef: cfgatev1alpha1.SecretRef{Name: "cf", Namespace: "secrets"},
		}},
		Status: cfgatev1alpha1.CloudflareTunnelStatus{AccountID: "inherited-account"},
	}
	reconciler = newAccessAppReconciler(t, mock, gateway, route, tunnel)
	app.Spec.CloudflareRef = nil
	creds, err = reconciler.resolveApplicationCredentials(ctx, app, []accessApplicationTarget{{Kind: "HTTPRoute", Namespace: "app", Name: "route"}})
	if err != nil {
		t.Fatalf("resolveApplicationCredentials() inherited error = %v", err)
	}
	if creds.AccountID != "inherited-account" {
		t.Fatalf("inherited accountID = %q, want inherited-account", creds.AccountID)
	}
	if creds.CredentialSecretRef == nil ||
		creds.CredentialSecretRef.Name != "cf" ||
		creds.CredentialSecretRef.Namespace != "secrets" {
		t.Fatalf("inherited CredentialSecretRef = %+v, want secrets/cf", creds.CredentialSecretRef)
	}

	routeA := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route-a", Namespace: "app"},
		Spec:       gwapiv1.HTTPRouteSpec{CommonRouteSpec: gwapiv1.CommonRouteSpec{ParentRefs: []gwapiv1.ParentReference{{Name: "gateway-a"}}}},
	}
	gatewayA := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway-a", Namespace: "app", Annotations: map[string]string{annotations.AnnotationTunnelRef: "tunnel-a"}},
	}
	tunnelA := &cfgatev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tunnel-a", Namespace: "app"},
		Spec: cfgatev1alpha1.CloudflareTunnelSpec{Cloudflare: cfgatev1alpha1.CloudflareConfig{
			SecretRef: cfgatev1alpha1.SecretRef{Name: "cf-a", Namespace: "secrets-a"},
		}},
		Status: cfgatev1alpha1.CloudflareTunnelStatus{AccountID: "account-1"},
	}
	routeB := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route-b", Namespace: "app"},
		Spec:       gwapiv1.HTTPRouteSpec{CommonRouteSpec: gwapiv1.CommonRouteSpec{ParentRefs: []gwapiv1.ParentReference{{Name: "gateway-b"}}}},
	}
	gatewayB := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway-b", Namespace: "app", Annotations: map[string]string{annotations.AnnotationTunnelRef: "tunnel-b"}},
	}
	tunnelB := &cfgatev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tunnel-b", Namespace: "app"},
		Spec: cfgatev1alpha1.CloudflareTunnelSpec{Cloudflare: cfgatev1alpha1.CloudflareConfig{
			SecretRef: cfgatev1alpha1.SecretRef{Name: "cf-b", Namespace: "secrets-b"},
		}},
		Status: cfgatev1alpha1.CloudflareTunnelStatus{AccountID: "account-1"},
	}
	reconciler = newAccessAppReconciler(t, mock, routeA, gatewayA, tunnelA, routeB, gatewayB, tunnelB)
	creds, err = reconciler.resolveApplicationCredentials(ctx, app, []accessApplicationTarget{
		{Kind: "HTTPRoute", Namespace: "app", Name: "route-a"},
		{Kind: "HTTPRoute", Namespace: "app", Name: "route-b"},
	})
	if err != nil {
		t.Fatalf("resolveApplicationCredentials() multi-target same account error = %v", err)
	}
	if creds.AccountID != "account-1" ||
		creds.CredentialSecretRef == nil ||
		creds.CredentialSecretRef.Name != "cf-a" ||
		creds.CredentialSecretRef.Namespace != "secrets-a" {
		t.Fatalf("multi-target credentials = %q/%+v, want account-1 secrets-a/cf-a", creds.AccountID, creds.CredentialSecretRef)
	}

	tunnelBMismatch := tunnelB.DeepCopy()
	tunnelBMismatch.Status.AccountID = "account-2"
	reconciler = newAccessAppReconciler(t, mock, routeA.DeepCopy(), gatewayA.DeepCopy(), tunnelA.DeepCopy(), routeB.DeepCopy(), gatewayB.DeepCopy(), tunnelBMismatch)
	_, err = reconciler.resolveApplicationCredentials(ctx, app, []accessApplicationTarget{
		{Kind: "HTTPRoute", Namespace: "app", Name: "route-a"},
		{Kind: "HTTPRoute", Namespace: "app", Name: "route-b"},
	})
	wantMismatch := "credential inheritance resolved multiple Cloudflare accounts: target app/route-a uses account account-1, target app/route-b uses account account-2; set spec.cloudflareRef to use one account explicitly"
	if err == nil || err.Error() != wantMismatch {
		t.Fatalf("resolveApplicationCredentials() mismatch error = %v, want %q", err, wantMismatch)
	}

	for _, tt := range []struct {
		name    string
		targets []accessApplicationTarget
		objects []client.Object
		wantErr string
	}{
		{name: "no targets", wantErr: "no targets available"},
		{name: "missing tunnel annotation", targets: []accessApplicationTarget{{Kind: "Gateway", Namespace: "app", Name: "gateway"}}, objects: []client.Object{&gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "app"}}}, wantErr: "missing cfgate.io/tunnel-ref"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := newAccessAppReconciler(t, mock, tt.objects...)
			_, err := reconciler.resolveApplicationCredentials(ctx, app, tt.targets)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("resolveApplicationCredentials() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestResolveApplicationPolicyRefs(t *testing.T) {
	ctx := context.Background()
	basePolicy := readyAccessPolicy("app", "policy", "account-1", "policy-1")
	otherPolicy := readyAccessPolicy("app", "other", "account-1", "policy-2")
	crossNS := "shared"
	crossPolicy := readyAccessPolicy(crossNS, "cross", "account-1", "policy-cross")
	crossSameNamePolicy := readyAccessPolicy(crossNS, "policy", "account-1", "policy-shared")
	grant := referenceGrant("app", crossNS, "CloudflareAccessPolicy", "cross")
	sameNameGrant := referenceGrant("app", crossNS, "CloudflareAccessPolicy", "policy")

	t.Run("default precedence", func(t *testing.T) {
		app := appWithFinalizer("app", "app")
		app.Spec.PolicyRefs = []cfgatev1alpha1.AccessPolicyReference{{Name: "policy"}, {Name: "other"}}
		reconciler := newAccessAppReconciler(t, cloudflare.NewMockClient(), basePolicy, otherPolicy)
		got, err := reconciler.resolveApplicationPolicyRefs(ctx, app, "account-1")
		if err != nil {
			t.Fatalf("resolveApplicationPolicyRefs() error = %v", err)
		}
		want := []cloudflare.ApplicationPolicyLink{{ID: "policy-1", Precedence: 1}, {ID: "policy-2", Precedence: 2}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("links = %#v, want %#v", got, want)
		}
	})

	t.Run("explicit precedence", func(t *testing.T) {
		app := appWithFinalizer("app", "app")
		first := 7
		second := 3
		app.Spec.PolicyRefs = []cfgatev1alpha1.AccessPolicyReference{{Name: "policy", Precedence: &first}, {Name: "other", Precedence: &second}}
		reconciler := newAccessAppReconciler(t, cloudflare.NewMockClient(), basePolicy, otherPolicy)
		got, err := reconciler.resolveApplicationPolicyRefs(ctx, app, "account-1")
		if err != nil {
			t.Fatalf("resolveApplicationPolicyRefs() error = %v", err)
		}
		want := []cloudflare.ApplicationPolicyLink{{ID: "policy-1", Precedence: 7}, {ID: "policy-2", Precedence: 3}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("links = %#v, want %#v", got, want)
		}
	})

	t.Run("same name in different namespaces", func(t *testing.T) {
		app := appWithFinalizer("app", "app")
		app.Spec.PolicyRefs = []cfgatev1alpha1.AccessPolicyReference{{Name: "policy"}, {Name: "policy", Namespace: crossNS}}
		reconciler := newAccessAppReconciler(t, cloudflare.NewMockClient(), basePolicy, crossSameNamePolicy, sameNameGrant)
		got, err := reconciler.resolveApplicationPolicyRefs(ctx, app, "account-1")
		if err != nil {
			t.Fatalf("resolveApplicationPolicyRefs() error = %v", err)
		}
		want := []cloudflare.ApplicationPolicyLink{{ID: "policy-1", Precedence: 1}, {ID: "policy-shared", Precedence: 2}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("links = %#v, want %#v", got, want)
		}
	})

	for _, tt := range []struct {
		name    string
		refs    []cfgatev1alpha1.AccessPolicyReference
		objects []client.Object
		wantErr string
	}{
		{name: "duplicate ref", refs: []cfgatev1alpha1.AccessPolicyReference{{Name: "policy"}, {Name: "policy"}}, objects: []client.Object{basePolicy}, wantErr: "duplicate policyRef"},
		{name: "mixed precedence", refs: []cfgatev1alpha1.AccessPolicyReference{{Name: "policy"}, {Name: "other", Precedence: intPtr(2)}}, objects: []client.Object{basePolicy, otherPolicy}, wantErr: "all specify precedence"},
		{name: "duplicate precedence", refs: []cfgatev1alpha1.AccessPolicyReference{{Name: "policy", Precedence: intPtr(2)}, {Name: "other", Precedence: intPtr(2)}}, objects: []client.Object{basePolicy, otherPolicy}, wantErr: "duplicate policyRef precedence 2"},
		{name: "missing policy", refs: []cfgatev1alpha1.AccessPolicyReference{{Name: "missing"}}, wantErr: "not found"},
		{name: "not ready policy", refs: []cfgatev1alpha1.AccessPolicyReference{{Name: "policy"}}, objects: []client.Object{notReadyAccessPolicy("app", "policy", "account-1", "policy-1")}, wantErr: "is not ready"},
		{name: "missing policy ID", refs: []cfgatev1alpha1.AccessPolicyReference{{Name: "policy"}}, objects: []client.Object{readyAccessPolicy("app", "policy", "account-1", "")}, wantErr: "has no policyId"},
		{name: "account mismatch", refs: []cfgatev1alpha1.AccessPolicyReference{{Name: "policy"}}, objects: []client.Object{readyAccessPolicy("app", "policy", "other-account", "policy-1")}, wantErr: status.ReasonAccountMismatch},
		{name: "cross namespace denied", refs: []cfgatev1alpha1.AccessPolicyReference{{Name: "cross", Namespace: crossNS}}, objects: []client.Object{crossPolicy}, wantErr: "ReferenceGrant"},
		{name: "cross namespace allowed", refs: []cfgatev1alpha1.AccessPolicyReference{{Name: "cross", Namespace: crossNS}}, objects: []client.Object{crossPolicy, grant}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			app := appWithFinalizer("app", "app")
			app.Spec.PolicyRefs = tt.refs
			reconciler := newAccessAppReconciler(t, cloudflare.NewMockClient(), tt.objects...)
			got, err := reconciler.resolveApplicationPolicyRefs(ctx, app, "account-1")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("resolveApplicationPolicyRefs() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveApplicationPolicyRefs() error = %v", err)
			}
			if len(got) != 1 || got[0].ID != "policy-cross" {
				t.Fatalf("links = %#v, want cross policy", got)
			}
		})
	}
}

func TestAccessApplicationDeletePaths(t *testing.T) {
	ctx := context.Background()

	t.Run("no finalizer", func(t *testing.T) {
		reconciler := newAccessAppReconciler(t, cloudflare.NewMockClient())
		if result, err := reconciler.reconcileApplicationDelete(ctx, &cfgatev1alpha1.CloudflareAccessApplication{}); err != nil || result != (ctrl.Result{}) {
			t.Fatalf("reconcileApplicationDelete() = (%+v, %v), want zero", result, err)
		}
	})

	t.Run("orphan removes finalizer", func(t *testing.T) {
		app := appWithFinalizer("app", "app")
		app.Annotations = map[string]string{"cfgate.io/deletion-policy": "orphan"}
		mock := cloudflare.NewMockClient()
		mock.DeleteAccessApplicationFunc = func(context.Context, string, string) error {
			t.Fatal("DeleteAccessApplication called for orphan")
			return nil
		}
		mock.DeleteAccessTagFunc = func(context.Context, string, string) error {
			t.Fatal("DeleteAccessTag called for orphan")
			return nil
		}
		reconciler := newAccessAppReconciler(t, mock, app)
		if _, err := reconciler.reconcileApplicationDelete(ctx, app); err != nil {
			t.Fatalf("reconcileApplicationDelete() error = %v", err)
		}
		assertAccessApplicationFinalizerRemoved(t, reconciler)
	})

	t.Run("observed apps delete and remove finalizer", func(t *testing.T) {
		app := appWithFinalizer("app", "app")
		app.Spec.CloudflareRef = &cfgatev1alpha1.CloudflareSecretRef{Name: "cf", AccountID: "account-1"}
		app.Status.Applications = []cfgatev1alpha1.AccessApplicationObserved{{ID: "app-1", Domain: "app.example.com"}}
		deleted := ""
		deletedTag := ""
		mock := cloudflare.NewMockClient()
		mock.DeleteAccessApplicationFunc = func(_ context.Context, accountID, appID string) error {
			if accountID != "account-1" {
				t.Fatalf("DeleteAccessApplication accountID = %q, want account-1", accountID)
			}
			deleted = appID
			return nil
		}
		mock.DeleteAccessTagFunc = func(_ context.Context, accountID, tagName string) error {
			if accountID != "account-1" {
				t.Fatalf("DeleteAccessTag accountID = %q, want account-1", accountID)
			}
			deletedTag = tagName
			return nil
		}
		reconciler := newAccessAppReconciler(t, mock, app)
		if _, err := reconciler.reconcileApplicationDelete(ctx, app); err != nil {
			t.Fatalf("reconcileApplicationDelete() error = %v", err)
		}
		if deleted != "app-1" {
			t.Fatalf("deleted = %q, want app-1", deleted)
		}
		if deletedTag != accessApplicationOwnerTag(app) {
			t.Fatalf("deletedTag = %q, want %q", deletedTag, accessApplicationOwnerTag(app))
		}
		assertAccessApplicationFinalizerRemoved(t, reconciler)
	})

	t.Run("cached credentials delete when target is missing", func(t *testing.T) {
		app := appWithFinalizer("app", "app")
		app.Status.AccountID = "account-1"
		app.Status.CredentialSecretRef = &cfgatev1alpha1.SecretReference{Name: "cf", Namespace: "app"}
		app.Status.Applications = []cfgatev1alpha1.AccessApplicationObserved{{
			ID:     "app-1",
			Domain: "app.example.com",
			TargetRef: cfgatev1alpha1.PolicyTargetReference{
				Kind: "Gateway",
				Name: "deleted-gateway",
			},
		}}
		deleted := ""
		deletedTag := ""
		mock := cloudflare.NewMockClient()
		mock.DeleteAccessApplicationFunc = func(_ context.Context, accountID, appID string) error {
			if accountID != "account-1" {
				t.Fatalf("DeleteAccessApplication accountID = %q, want account-1", accountID)
			}
			deleted = appID
			return nil
		}
		mock.DeleteAccessTagFunc = func(_ context.Context, accountID, tagName string) error {
			if accountID != "account-1" {
				t.Fatalf("DeleteAccessTag accountID = %q, want account-1", accountID)
			}
			deletedTag = tagName
			return nil
		}
		reconciler := newAccessAppReconciler(t, mock, app)
		if _, err := reconciler.reconcileApplicationDelete(ctx, app); err != nil {
			t.Fatalf("reconcileApplicationDelete() error = %v", err)
		}
		if deleted != "app-1" {
			t.Fatalf("deleted = %q, want app-1", deleted)
		}
		if deletedTag != accessApplicationOwnerTag(app) {
			t.Fatalf("deletedTag = %q, want %q", deletedTag, accessApplicationOwnerTag(app))
		}
		assertAccessApplicationFinalizerRemoved(t, reconciler)
	})

	t.Run("empty status with cached credentials deletes owner tag and removes finalizer", func(t *testing.T) {
		app := appWithFinalizer("app", "app")
		app.Status.AccountID = "account-1"
		app.Status.CredentialSecretRef = &cfgatev1alpha1.SecretReference{Name: "cf", Namespace: "app"}
		deletedTag := ""
		mock := cloudflare.NewMockClient()
		mock.DeleteAccessApplicationFunc = func(context.Context, string, string) error {
			t.Fatal("DeleteAccessApplication called with empty status")
			return nil
		}
		mock.DeleteAccessTagFunc = func(_ context.Context, accountID, tagName string) error {
			if accountID != "account-1" {
				t.Fatalf("DeleteAccessTag accountID = %q, want account-1", accountID)
			}
			deletedTag = tagName
			return nil
		}
		reconciler := newAccessAppReconciler(t, mock, app)
		if _, err := reconciler.reconcileApplicationDelete(ctx, app); err != nil {
			t.Fatalf("reconcileApplicationDelete() error = %v", err)
		}
		if deletedTag != accessApplicationOwnerTag(app) {
			t.Fatalf("deletedTag = %q, want %q", deletedTag, accessApplicationOwnerTag(app))
		}
		assertAccessApplicationFinalizerRemoved(t, reconciler)
	})

	t.Run("delete error requeues and emits event", func(t *testing.T) {
		app := appWithFinalizer("app", "app")
		setDeletionTimestamp(app)
		app.Spec.CloudflareRef = &cfgatev1alpha1.CloudflareSecretRef{Name: "cf", AccountID: "account-1"}
		app.Status.Applications = []cfgatev1alpha1.AccessApplicationObserved{{ID: "app-1", Domain: "app.example.com"}}
		mock := cloudflare.NewMockClient()
		mock.DeleteAccessApplicationFunc = func(context.Context, string, string) error { return errors.New("delete failed") }
		reconciler := newAccessAppReconciler(t, mock, app)
		result, err := reconciler.reconcileApplicationDelete(ctx, app)
		if err != nil {
			t.Fatalf("reconcileApplicationDelete() error = %v", err)
		}
		if result.RequeueAfter != accessDeletionRequeueInterval {
			t.Fatalf("RequeueAfter = %v, want %v", result.RequeueAfter, accessDeletionRequeueInterval)
		}
		assertAccessApplicationEventContains(t, reconciler.Recorder.(*accessApplicationEventRecorder), "Cleanup")
	})

	t.Run("owner tag delete error requeues and emits cleanup event", func(t *testing.T) {
		app := appWithFinalizer("app", "app")
		setDeletionTimestamp(app)
		app.Spec.CloudflareRef = &cfgatev1alpha1.CloudflareSecretRef{Name: "cf", AccountID: "account-1"}
		app.Status.Applications = []cfgatev1alpha1.AccessApplicationObserved{{ID: "app-1", Domain: "app.example.com"}}
		mock := cloudflare.NewMockClient()
		mock.DeleteAccessApplicationFunc = func(context.Context, string, string) error { return nil }
		mock.DeleteAccessTagFunc = func(context.Context, string, string) error { return errors.New("tag delete failed") }
		reconciler := newAccessAppReconciler(t, mock, app)
		result, err := reconciler.reconcileApplicationDelete(ctx, app)
		if err != nil {
			t.Fatalf("reconcileApplicationDelete() error = %v", err)
		}
		if result.RequeueAfter != accessDeletionRequeueInterval {
			t.Fatalf("RequeueAfter = %v, want %v", result.RequeueAfter, accessDeletionRequeueInterval)
		}
		assertAccessApplicationEventContains(t, reconciler.Recorder.(*accessApplicationEventRecorder), "Cleanup")
		assertAccessApplicationEventContains(t, reconciler.Recorder.(*accessApplicationEventRecorder), "delete Access application owner tag")
	})

	t.Run("credential error requeues", func(t *testing.T) {
		app := appWithFinalizer("app", "app")
		setDeletionTimestamp(app)
		app.Status.Applications = []cfgatev1alpha1.AccessApplicationObserved{{ID: "app-1", Domain: "app.example.com", TargetRef: cfgatev1alpha1.PolicyTargetReference{Kind: "Gateway", Name: "missing"}}}
		reconciler := newAccessAppReconciler(t, cloudflare.NewMockClient(), app)
		result, err := reconciler.reconcileApplicationDelete(ctx, app)
		if err != nil {
			t.Fatalf("reconcileApplicationDelete() error = %v", err)
		}
		if result.RequeueAfter != accessDeletionRequeueInterval {
			t.Fatalf("RequeueAfter = %v, want %v", result.RequeueAfter, accessDeletionRequeueInterval)
		}
		assertAccessApplicationEventContains(t, reconciler.Recorder.(*accessApplicationEventRecorder), "Cleanup")
	})
}

func TestAccessApplicationStatusAndReasonHelpers(t *testing.T) {
	base := &cfgatev1alpha1.CloudflareAccessApplicationStatus{
		AccountID:           "account-1",
		CredentialSecretRef: &cfgatev1alpha1.SecretReference{Name: "cf", Namespace: "app"},
		AttachedTargets:     1,
		ObservedGeneration:  2,
		Applications:        []cfgatev1alpha1.AccessApplicationObserved{{ID: "app-1", Domain: "app.example.com"}},
		Ancestors:           []cfgatev1alpha1.PolicyAncestorStatus{{ControllerName: AccessPolicyControllerName}},
		Conditions:          []metav1.Condition{status.NewCondition(status.ConditionTypeReady, metav1.ConditionTrue, status.ReasonReady, "ready", 2)},
	}
	if !accessApplicationStatusEqual(base, base.DeepCopy()) {
		t.Fatal("accessApplicationStatusEqual() = false, want true")
	}
	for _, tt := range []struct {
		name   string
		modify func(*cfgatev1alpha1.CloudflareAccessApplicationStatus)
	}{
		{name: "account ID", modify: func(s *cfgatev1alpha1.CloudflareAccessApplicationStatus) { s.AccountID = "other" }},
		{name: "credential secret ref", modify: func(s *cfgatev1alpha1.CloudflareAccessApplicationStatus) { s.CredentialSecretRef.Name = "other" }},
		{name: "attached targets", modify: func(s *cfgatev1alpha1.CloudflareAccessApplicationStatus) { s.AttachedTargets = 2 }},
		{name: "generation", modify: func(s *cfgatev1alpha1.CloudflareAccessApplicationStatus) { s.ObservedGeneration = 3 }},
		{name: "applications", modify: func(s *cfgatev1alpha1.CloudflareAccessApplicationStatus) { s.Applications[0].ID = "other" }},
		{name: "ancestors", modify: func(s *cfgatev1alpha1.CloudflareAccessApplicationStatus) { s.Ancestors = nil }},
		{name: "conditions", modify: func(s *cfgatev1alpha1.CloudflareAccessApplicationStatus) { s.Conditions[0].Reason = "Other" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			changed := base.DeepCopy()
			tt.modify(changed)
			if accessApplicationStatusEqual(base, changed) {
				t.Fatal("accessApplicationStatusEqual() = true, want false")
			}
		})
	}

	if conditionStatus(true) != metav1.ConditionTrue || conditionStatus(false) != metav1.ConditionFalse {
		t.Fatal("conditionStatus() returned unexpected status")
	}
	if referenceGrantReason(false) != status.ReasonReferenceGrantRequired || referenceGrantMessage(false) == "" ||
		referenceGrantReason(true) != status.ReasonResolved || referenceGrantMessage(true) == "" {
		t.Fatal("referenceGrant helpers returned unexpected values")
	}
	if targetResolutionReason(errors.New(status.ReasonUnsupportedPathMatch+": bad")) != status.ReasonUnsupportedPathMatch ||
		targetResolutionReason(errors.New("route not found")) != status.ReasonTargetNotFound ||
		targetResolutionReason(errors.New("boom")) != status.ReasonTargetResolutionFailed {
		t.Fatal("targetResolutionReason() returned unexpected value")
	}
	if policyResolutionReason(errors.New(status.ReasonAccountMismatch+": bad")) != status.ReasonAccountMismatch ||
		policyResolutionReason(errors.New("ReferenceGrant required")) != status.ReasonReferenceGrantRequired ||
		policyResolutionReason(errors.New("boom")) != status.ReasonPolicyError {
		t.Fatal("policyResolutionReason() returned unexpected value")
	}
	ids := applicationStatusIDs(base.Applications)
	if ids["app.example.com"] != "app-1" {
		t.Fatalf("applicationStatusIDs() = %#v", ids)
	}
	domains := accessApplicationTargetDomains([]accessApplicationTarget{{Domain: "app.example.com"}})
	if _, ok := domains["app.example.com"]; !ok {
		t.Fatalf("accessApplicationTargetDomains() = %#v", domains)
	}
	if accessApplicationDomain("app.example.com", "/") != "app.example.com" ||
		accessApplicationDomain("app.example.com", "/admin") != "app.example.com/admin" {
		t.Fatal("accessApplicationDomain() returned unexpected values")
	}
	if sanitizeAccessApplicationName("Example.COM/Admin") != "example-com-admin" ||
		sanitizeAccessApplicationName("!!!") != "target" {
		t.Fatal("sanitizeAccessApplicationName() returned unexpected values")
	}
}

func TestAccessApplicationWatchMappers(t *testing.T) {
	sameTarget := &cfgatev1alpha1.CloudflareAccessApplication{
		ObjectMeta: metav1.ObjectMeta{Name: "same", Namespace: "app"},
		Spec: cfgatev1alpha1.CloudflareAccessApplicationSpec{
			TargetRef:  &cfgatev1alpha1.PolicyTargetReference{Group: gwapiv1.GroupName, Kind: "HTTPRoute", Name: "route"},
			PolicyRefs: []cfgatev1alpha1.AccessPolicyReference{{Name: "policy"}},
		},
	}
	otherTargetNS := "target"
	multiTarget := &cfgatev1alpha1.CloudflareAccessApplication{
		ObjectMeta: metav1.ObjectMeta{Name: "multi", Namespace: "app"},
		Spec: cfgatev1alpha1.CloudflareAccessApplicationSpec{
			TargetRefs: []cfgatev1alpha1.PolicyTargetReference{{Group: gwapiv1.GroupName, Kind: "Gateway", Name: "gateway", Namespace: &otherTargetNS}},
			PolicyRefs: []cfgatev1alpha1.AccessPolicyReference{{Name: "other"}},
		},
	}
	reconciler := newAccessAppReconciler(t, cloudflare.NewMockClient(), sameTarget, multiTarget)

	policyReqs := reconciler.findApplicationsForPolicy(context.Background(), &cfgatev1alpha1.CloudflareAccessPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "app"},
	})
	if len(policyReqs) != 1 || policyReqs[0].Name != "same" {
		t.Fatalf("findApplicationsForPolicy() = %#v, want same", policyReqs)
	}
	routeReqs := reconciler.findApplicationsReferencingTarget(context.Background(), "HTTPRoute", "app", "route")
	if len(routeReqs) != 1 || routeReqs[0].Name != "same" {
		t.Fatalf("findApplicationsReferencingTarget(route) = %#v, want same", routeReqs)
	}
	gatewayReqs := reconciler.findApplicationsReferencingTarget(context.Background(), "Gateway", "target", "gateway")
	if len(gatewayReqs) != 1 || gatewayReqs[0].Name != "multi" {
		t.Fatalf("findApplicationsReferencingTarget(gateway) = %#v, want multi", gatewayReqs)
	}
	allReqs := reconciler.findAllAccessApplications(context.Background(), &cfgatev1alpha1.CloudflareTunnel{})
	if len(allReqs) != 2 {
		t.Fatalf("findAllAccessApplications() len = %d, want 2", len(allReqs))
	}
}

func newAccessAppReconciler(t *testing.T, mockClient cloudflare.Client, objects ...client.Object) *CloudflareAccessApplicationReconciler {
	t.Helper()
	scheme := controllerTestScheme(t)
	if err := gwapiv1b1.Install(scheme); err != nil {
		t.Fatalf("Install(gateway/v1beta1) error = %v", err)
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&cfgatev1alpha1.CloudflareAccessApplication{}, &cfgatev1alpha1.CloudflareAccessPolicy{}).
		Build()
	return &CloudflareAccessApplicationReconciler{
		Client:       k8sClient,
		Scheme:       scheme,
		CFClient:     mockClient,
		Recorder:     &accessApplicationEventRecorder{},
		FeatureGates: &features.FeatureGates{ReferenceGrantCRDExists: true},
	}
}

func appWithFinalizer(namespace, name string) *cfgatev1alpha1.CloudflareAccessApplication {
	return &cfgatev1alpha1.CloudflareAccessApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Finalizers: []string{accessApplicationFinalizer},
		},
	}
}

func readyAccessPolicy(namespace, name, accountID, policyID string) *cfgatev1alpha1.CloudflareAccessPolicy {
	policy := notReadyAccessPolicy(namespace, name, accountID, policyID)
	policy.Status.Conditions = []metav1.Condition{
		status.NewCondition(status.ConditionTypeReady, metav1.ConditionTrue, status.ReasonReady, "ready", 1),
	}
	return policy
}

func notReadyAccessPolicy(namespace, name, accountID, policyID string) *cfgatev1alpha1.CloudflareAccessPolicy {
	return &cfgatev1alpha1.CloudflareAccessPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Status: cfgatev1alpha1.CloudflareAccessPolicyStatus{
			AccountID: accountID,
			PolicyID:  policyID,
		},
	}
}

func referenceGrant(fromNamespace, toNamespace, toKind, toName string) *gwapiv1b1.ReferenceGrant {
	objectName := gwapiv1.ObjectName(toName)
	return &gwapiv1b1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant-" + strings.ToLower(toKind), Namespace: toNamespace},
		Spec: gwapiv1b1.ReferenceGrantSpec{
			From: []gwapiv1b1.ReferenceGrantFrom{{
				Group:     gwapiv1.Group(cfgatev1alpha1.GroupVersion.Group),
				Kind:      gwapiv1.Kind("CloudflareAccessApplication"),
				Namespace: gwapiv1.Namespace(fromNamespace),
			}},
			To: []gwapiv1b1.ReferenceGrantTo{{
				Group: gwapiv1.Group(groupForReferenceKind(toKind)),
				Kind:  gwapiv1.Kind(toKind),
				Name:  &objectName,
			}},
		},
	}
}

func groupForReferenceKind(kind string) string {
	if kind == "CloudflareAccessPolicy" {
		return cfgatev1alpha1.GroupVersion.Group
	}
	return gwapiv1.GroupName
}

func sectionName(name string) *gwapiv1.SectionName {
	value := gwapiv1.SectionName(name)
	return &value
}

func pathMatchType(matchType gwapiv1.PathMatchType) *gwapiv1.PathMatchType {
	return &matchType
}

func intPtr(value int) *int {
	return &value
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func resultRequeues(result ctrl.Result) bool {
	return reflect.ValueOf(result).FieldByName("Requeue").Bool()
}

func setDeletionTimestamp(obj client.Object) {
	deletionTimestamp := metav1.NewTime(time.Now())
	obj.SetDeletionTimestamp(&deletionTimestamp)
}

func assertAccessApplicationFinalizerRemoved(t *testing.T, reconciler *CloudflareAccessApplicationReconciler) {
	t.Helper()
	var current cfgatev1alpha1.CloudflareAccessApplication
	if err := reconciler.Get(context.Background(), types.NamespacedName{Name: "app", Namespace: "app"}, &current); err != nil {
		if apierrors.IsNotFound(err) {
			return
		}
		t.Fatalf("Get() error = %v", err)
	}
	if containsString(current.Finalizers, accessApplicationFinalizer) {
		t.Fatalf("finalizers = %#v, want finalizer removed", current.Finalizers)
	}
}

func accessApplicationWithDeletionTimestamp(ts time.Time) *cfgatev1alpha1.CloudflareAccessApplication {
	deletionTimestamp := metav1.NewTime(ts)
	return &cfgatev1alpha1.CloudflareAccessApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "app",
			Namespace:         "default",
			DeletionTimestamp: &deletionTimestamp,
		},
	}
}

type accessApplicationEventRecorder struct {
	events []string
}

func (r *accessApplicationEventRecorder) Eventf(regarding runtime.Object, related runtime.Object, eventtype, reason, action, note string, args ...interface{}) {
	_ = regarding
	_ = related
	r.events = append(r.events, strings.Join([]string{
		eventtype,
		reason,
		action,
		fmt.Sprintf(note, args...),
	}, " "))
}

func assertAccessApplicationEventContains(t *testing.T, recorder *accessApplicationEventRecorder, want string) {
	t.Helper()
	for _, event := range recorder.events {
		if strings.Contains(event, want) {
			return
		}
	}
	t.Fatalf("did not receive event containing %q: %#v", want, recorder.events)
}

func assertAccessApplicationEventNotContains(t *testing.T, recorder *accessApplicationEventRecorder, unwanted string) {
	t.Helper()
	for _, event := range recorder.events {
		if strings.Contains(event, unwanted) {
			t.Fatalf("received event containing %q: %q", unwanted, event)
		}
	}
}

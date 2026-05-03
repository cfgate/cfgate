package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
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

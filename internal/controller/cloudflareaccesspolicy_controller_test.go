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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/controller/features"
	"cfgate.io/cfgate/internal/controller/status"
)

func TestAccessPolicyReconcileAddsFinalizer(t *testing.T) {
	policy := baseAccessPolicy("app", "policy")
	reconciler := newAccessPolicyReconciler(t, cloudflare.NewMockClient(), policy)

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "policy", Namespace: "app"}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if !resultRequeues(result) {
		t.Fatalf("Requeue = false, want true")
	}
	var current cfgatev1alpha1.CloudflareAccessPolicy
	if err := reconciler.Get(context.Background(), types.NamespacedName{Name: "policy", Namespace: "app"}, &current); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !containsString(current.Finalizers, accessPolicyFinalizer) {
		t.Fatalf("finalizers = %#v, want %s", current.Finalizers, accessPolicyFinalizer)
	}
}

func TestAccessPolicyReconcileSuccess(t *testing.T) {
	policy := baseAccessPolicy("app", "policy")
	policy.Finalizers = []string{accessPolicyFinalizer}
	mock := cloudflare.NewMockClient()
	mock.ListAccessPoliciesFunc = func(context.Context, string) ([]cloudflare.AccessPolicy, error) { return nil, nil }
	mock.CreateAccessPolicyFunc = func(_ context.Context, accountID string, params cloudflare.PolicyParams) (*cloudflare.AccessPolicy, error) {
		if accountID != "account-1" {
			t.Fatalf("accountID = %q, want account-1", accountID)
		}
		if len(params.Include) != 1 || params.Include[0].Everyone == nil || !*params.Include[0].Everyone {
			t.Fatalf("include = %#v, want everyone", params.Include)
		}
		return &cloudflare.AccessPolicy{ID: "policy-id", Name: params.Name, Reusable: true, AppCount: 0}, nil
	}
	reconciler := newAccessPolicyReconciler(t, mock, policy)

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "policy", Namespace: "app"}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != accessPolicyRequeueAfterSuccess {
		t.Fatalf("RequeueAfter = %v, want %v", result.RequeueAfter, accessPolicyRequeueAfterSuccess)
	}
	var current cfgatev1alpha1.CloudflareAccessPolicy
	if err := reconciler.Get(context.Background(), types.NamespacedName{Name: "policy", Namespace: "app"}, &current); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if current.Status.PolicyID != "policy-id" || current.Status.AccountID != "account-1" || !current.Status.Reusable {
		t.Fatalf("status = %#v", current.Status)
	}
	if !status.ConditionTrue(current.Status.Conditions, status.ConditionTypeReady) ||
		!status.ConditionTrue(current.Status.Conditions, status.ConditionTypePolicySynced) {
		t.Fatalf("conditions = %#v", current.Status.Conditions)
	}
}

func TestAccessPolicyReconcileCredentialError(t *testing.T) {
	policy := baseAccessPolicy("app", "policy")
	policy.Finalizers = []string{accessPolicyFinalizer}
	policy.Spec.CloudflareRef.AccountID = ""
	reconciler := newAccessPolicyReconciler(t, cloudflare.NewMockClient(), policy)

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "policy", Namespace: "app"}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != accessPolicyRequeueAfterError {
		t.Fatalf("RequeueAfter = %v, want %v", result.RequeueAfter, accessPolicyRequeueAfterError)
	}
	var current cfgatev1alpha1.CloudflareAccessPolicy
	if err := reconciler.Get(context.Background(), types.NamespacedName{Name: "policy", Namespace: "app"}, &current); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	condition := status.FindCondition(current.Status.Conditions, status.ConditionTypeCredentialsValid)
	if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != status.ReasonCredentialsInvalid {
		t.Fatalf("CredentialsValid condition = %+v", condition)
	}
}

func TestAccessPolicyResolveCredentialsAccountName(t *testing.T) {
	ctx := context.Background()
	policy := baseAccessPolicy("app", "policy")
	policy.Spec.CloudflareRef.AccountID = ""
	policy.Spec.CloudflareRef.AccountName = "prod"
	var calls int
	mock := cloudflare.NewMockClient()
	mock.GetAccountByNameFunc = func(_ context.Context, name string) (*cloudflare.Account, error) {
		calls++
		if name != "prod" {
			t.Fatalf("GetAccountByName name = %q, want prod", name)
		}
		return &cloudflare.Account{ID: "account-1", Name: name}, nil
	}
	reconciler := newAccessPolicyReconciler(t, mock, policy)

	service, accountID, err := reconciler.resolveCredentials(ctx, policy)
	if err != nil {
		t.Fatalf("resolveCredentials() error = %v", err)
	}
	if service == nil || accountID != "account-1" || calls != 1 {
		t.Fatalf("service = %v accountID = %q calls = %d", service, accountID, calls)
	}

	policy.Spec.CloudflareRef.AccountName = "missing"
	mock.GetAccountByNameFunc = func(context.Context, string) (*cloudflare.Account, error) {
		return nil, nil
	}
	if _, _, err := reconciler.resolveCredentials(ctx, policy); err == nil || !strings.Contains(err.Error(), `account "missing" not found`) {
		t.Fatalf("resolveCredentials() missing account error = %v", err)
	}
}

func TestBuildReusablePolicyParams(t *testing.T) {
	trueValue := true
	policy := baseAccessPolicy("app", "policy")
	policy.Spec.Exclude = []cfgatev1alpha1.AccessRule{{Email: &cfgatev1alpha1.AccessEmailRule{Addresses: []string{"blocked@example.com"}}}}
	policy.Spec.Require = []cfgatev1alpha1.AccessRule{{ServiceToken: &cfgatev1alpha1.AccessServiceTokenRule{Name: "svc"}}}
	policy.Status.ServiceTokenIDs = map[string]string{"svc": "token-id"}

	params, err := buildReusablePolicyParams(policy)
	if err != nil {
		t.Fatalf("buildReusablePolicyParams() error = %v", err)
	}
	if params.Name != "policy" || params.Decision != "allow" || len(params.Include) != 1 ||
		params.Include[0].Everyone == nil || !*params.Include[0].Everyone {
		t.Fatalf("params = %+v", params)
	}
	if len(params.Exclude) != 1 || params.Exclude[0].Email == nil || *params.Exclude[0].Email != "blocked@example.com" {
		t.Fatalf("Exclude = %#v", params.Exclude)
	}
	if len(params.Require) != 1 || params.Require[0].ServiceTokenID == nil || *params.Require[0].ServiceTokenID != "token-id" {
		t.Fatalf("Require = %#v", params.Require)
	}

	for _, tt := range []struct {
		name    string
		rules   []cfgatev1alpha1.AccessRule
		wantErr string
	}{
		{name: "missing service token ID", rules: []cfgatev1alpha1.AccessRule{{ServiceToken: &cfgatev1alpha1.AccessServiceTokenRule{Name: "missing"}}}, wantErr: "service token \"missing\" is not ready"},
		{name: "ip list missing ID", rules: []cfgatev1alpha1.AccessRule{{IPList: &cfgatev1alpha1.AccessIPListRule{Name: "corp"}}}, wantErr: "listID is required"},
		{name: "email list missing ID", rules: []cfgatev1alpha1.AccessRule{{EmailList: &cfgatev1alpha1.AccessEmailListRule{Name: "team"}}}, wantErr: "listID is required"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			policy := baseAccessPolicy("app", "policy")
			policy.Spec.Include = tt.rules
			policy.Status.ServiceTokenIDs = map[string]string{"svc": "token-id"}
			_, err := buildReusablePolicyParams(policy)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("buildReusablePolicyParams() error = %v, want %q", err, tt.wantErr)
			}
		})
	}

	params, err = buildReusablePolicyParams(&cfgatev1alpha1.CloudflareAccessPolicy{
		Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
			Name:     "policy",
			Decision: "allow",
			Include:  []cfgatev1alpha1.AccessRule{{Everyone: &trueValue}},
		},
	})
	if err != nil || len(params.Include) != 1 {
		t.Fatalf("buildReusablePolicyParams(everyone) = (%+v, %v)", params, err)
	}
}

func TestK8sSecretWriterWriteSecret(t *testing.T) {
	ctx := context.Background()
	scheme := controllerTestScheme(t)
	owner := baseAccessPolicy("app", "policy")
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(owner).Build()
	writer := &k8sSecretWriter{
		client:    k8sClient,
		namespace: "app",
		secretRef: cfgatev1alpha1.ServiceTokenSecretRef{Name: "svc-secret"},
		owner:     owner,
		scheme:    scheme,
	}

	if err := writer.WriteSecret(ctx, "svc", map[string][]byte{"a": []byte("b")}); err != nil {
		t.Fatalf("WriteSecret(create) error = %v", err)
	}
	var secret corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "svc-secret", Namespace: "app"}, &secret); err != nil {
		t.Fatalf("Get(secret) error = %v", err)
	}
	if string(secret.Data["a"]) != "b" || len(secret.OwnerReferences) != 1 {
		t.Fatalf("secret = %#v", secret)
	}

	if err := writer.WriteSecret(ctx, "svc", map[string][]byte{"a": []byte("updated")}); err != nil {
		t.Fatalf("WriteSecret(update) error = %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "svc-secret", Namespace: "app"}, &secret); err != nil {
		t.Fatalf("Get(secret) error = %v", err)
	}
	if string(secret.Data["a"]) != "updated" {
		t.Fatalf("secret data = %#v, want updated", secret.Data)
	}

	badWriter := &k8sSecretWriter{
		client:    k8sClient,
		namespace: "app",
		secretRef: cfgatev1alpha1.ServiceTokenSecretRef{Name: "bad-secret"},
		owner:     owner,
		scheme:    runtime.NewScheme(),
	}
	if err := badWriter.WriteSecret(ctx, "svc", nil); err == nil || !strings.Contains(err.Error(), "setting owner reference") {
		t.Fatalf("WriteSecret(bad scheme) error = %v, want owner reference error", err)
	}
}

func TestEnsureServiceTokens(t *testing.T) {
	ctx := context.Background()
	policy := baseAccessPolicy("app", "policy")
	policy.Spec.ServiceTokens = []cfgatev1alpha1.ServiceTokenConfig{{Name: "svc", Duration: "8760h", SecretRef: cfgatev1alpha1.ServiceTokenSecretRef{Name: "svc-secret"}}}
	mock := cloudflare.NewMockClient()
	mock.ListServiceTokensFunc = func(context.Context, string) ([]cloudflare.ServiceToken, error) { return nil, nil }
	mock.CreateServiceTokenFunc = func(context.Context, string, cloudflare.ServiceTokenParams) (*cloudflare.ServiceTokenWithSecret, error) {
		return &cloudflare.ServiceTokenWithSecret{
			ServiceToken: cloudflare.ServiceToken{ID: "token-id", Name: "svc", ClientID: "client-id", ExpiresAt: time.Now().Add(time.Hour)},
			ClientSecret: "client-secret",
		}, nil
	}
	reconciler := newAccessPolicyReconciler(t, mock, policy)
	service := cloudflare.NewAccessService(mock, logr.Discard())
	if err := reconciler.ensureServiceTokens(ctx, service, "account-1", policy); err != nil {
		t.Fatalf("ensureServiceTokens() error = %v", err)
	}
	if policy.Status.ServiceTokenIDs["svc"] != "token-id" {
		t.Fatalf("ServiceTokenIDs = %#v", policy.Status.ServiceTokenIDs)
	}
	var secret corev1.Secret
	if err := reconciler.Get(ctx, types.NamespacedName{Name: "svc-secret", Namespace: "app"}, &secret); err != nil {
		t.Fatalf("Get(secret) error = %v", err)
	}
	if string(secret.Data["CF_ACCESS_CLIENT_ID"]) != "client-id" || string(secret.Data["CF_ACCESS_CLIENT_SECRET"]) != "client-secret" {
		t.Fatalf("secret data = %#v", secret.Data)
	}

	mock = cloudflare.NewMockClient()
	mock.ListServiceTokensFunc = func(context.Context, string) ([]cloudflare.ServiceToken, error) {
		return nil, errors.New("list failed")
	}
	err := reconciler.ensureServiceTokens(ctx, cloudflare.NewAccessService(mock, logr.Discard()), "account-1", policy)
	if err == nil || !strings.Contains(err.Error(), "failed to ensure service token svc") {
		t.Fatalf("ensureServiceTokens() error = %v, want token name wrapper", err)
	}
}

func TestAccessPolicyDeletePaths(t *testing.T) {
	ctx := context.Background()

	t.Run("no finalizer", func(t *testing.T) {
		reconciler := newAccessPolicyReconciler(t, cloudflare.NewMockClient())
		if result, err := reconciler.reconcileDelete(ctx, &cfgatev1alpha1.CloudflareAccessPolicy{}); err != nil || result != (ctrl.Result{}) {
			t.Fatalf("reconcileDelete() = (%+v, %v), want zero", result, err)
		}
	})

	t.Run("orphan removes finalizer", func(t *testing.T) {
		policy := baseAccessPolicy("app", "policy")
		policy.Finalizers = []string{accessPolicyFinalizer}
		policy.Annotations = map[string]string{"cfgate.io/deletion-policy": "orphan"}
		reconciler := newAccessPolicyReconciler(t, cloudflare.NewMockClient(), policy)
		if _, err := reconciler.reconcileDelete(ctx, policy); err != nil {
			t.Fatalf("reconcileDelete() error = %v", err)
		}
		assertAccessPolicyFinalizerRemoved(t, reconciler)
	})

	t.Run("linked policy blocks deletion", func(t *testing.T) {
		policy := policyWithDeleteStatus()
		mock := cloudflare.NewMockClient()
		mock.GetAccessPolicyFunc = func(context.Context, string, string) (*cloudflare.AccessPolicy, error) {
			return &cloudflare.AccessPolicy{ID: "policy-id", AppCount: 2}, nil
		}
		reconciler := newAccessPolicyReconciler(t, mock, policy)
		result, err := reconciler.reconcileDelete(ctx, policy)
		if err != nil {
			t.Fatalf("reconcileDelete() error = %v", err)
		}
		if result.RequeueAfter != accessDeletionRequeueInterval {
			t.Fatalf("RequeueAfter = %v, want %v", result.RequeueAfter, accessDeletionRequeueInterval)
		}
		assertAccessApplicationEventContains(t, reconciler.Recorder.(*accessApplicationEventRecorder), "Cleanup")
	})

	t.Run("deletes policy and service tokens", func(t *testing.T) {
		policy := policyWithDeleteStatus()
		deletedPolicy := ""
		deletedTokens := []string{}
		mock := cloudflare.NewMockClient()
		mock.GetAccessPolicyFunc = func(context.Context, string, string) (*cloudflare.AccessPolicy, error) {
			return &cloudflare.AccessPolicy{ID: "policy-id", AppCount: 0}, nil
		}
		mock.DeleteAccessPolicyFunc = func(_ context.Context, _ string, policyID string) error {
			deletedPolicy = policyID
			return nil
		}
		mock.DeleteServiceTokenFunc = func(_ context.Context, _ string, tokenID string) error {
			deletedTokens = append(deletedTokens, tokenID)
			return nil
		}
		reconciler := newAccessPolicyReconciler(t, mock, policy)
		if _, err := reconciler.reconcileDelete(ctx, policy); err != nil {
			t.Fatalf("reconcileDelete() error = %v", err)
		}
		if deletedPolicy != "policy-id" || len(deletedTokens) != 1 || deletedTokens[0] != "token-id" {
			t.Fatalf("deleted policy/tokens = %q/%#v", deletedPolicy, deletedTokens)
		}
		assertAccessPolicyFinalizerRemoved(t, reconciler)
	})

	for _, tt := range []struct {
		name  string
		setup func(*cloudflare.MockClient)
	}{
		{name: "get policy error", setup: func(mock *cloudflare.MockClient) {
			mock.GetAccessPolicyFunc = func(context.Context, string, string) (*cloudflare.AccessPolicy, error) {
				return nil, errors.New("get failed")
			}
		}},
		{name: "delete policy error", setup: func(mock *cloudflare.MockClient) {
			mock.GetAccessPolicyFunc = func(context.Context, string, string) (*cloudflare.AccessPolicy, error) {
				return &cloudflare.AccessPolicy{AppCount: 0}, nil
			}
			mock.DeleteAccessPolicyFunc = func(context.Context, string, string) error { return errors.New("delete failed") }
		}},
		{name: "delete service token error", setup: func(mock *cloudflare.MockClient) {
			mock.GetAccessPolicyFunc = func(context.Context, string, string) (*cloudflare.AccessPolicy, error) {
				return &cloudflare.AccessPolicy{AppCount: 0}, nil
			}
			mock.DeleteAccessPolicyFunc = func(context.Context, string, string) error { return nil }
			mock.DeleteServiceTokenFunc = func(context.Context, string, string) error { return errors.New("token delete failed") }
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			policy := policyWithDeleteStatus()
			mock := cloudflare.NewMockClient()
			tt.setup(mock)
			reconciler := newAccessPolicyReconciler(t, mock, policy)
			result, err := reconciler.reconcileDelete(ctx, policy)
			if err != nil {
				t.Fatalf("reconcileDelete() error = %v", err)
			}
			if result.RequeueAfter != accessDeletionRequeueInterval {
				t.Fatalf("RequeueAfter = %v, want %v", result.RequeueAfter, accessDeletionRequeueInterval)
			}
		})
	}

	t.Run("credential resolution failure blocks", func(t *testing.T) {
		policy := policyWithDeleteStatus()
		policy.Spec.CloudflareRef.AccountID = ""
		reconciler := newAccessPolicyReconciler(t, cloudflare.NewMockClient(), policy)
		result, err := reconciler.reconcileDelete(ctx, policy)
		if err != nil {
			t.Fatalf("reconcileDelete() error = %v", err)
		}
		if result.RequeueAfter != accessDeletionRequeueInterval {
			t.Fatalf("RequeueAfter = %v, want %v", result.RequeueAfter, accessDeletionRequeueInterval)
		}
	})
}

func TestAccessPolicyStatusHelpers(t *testing.T) {
	base := &cfgatev1alpha1.CloudflareAccessPolicyStatus{
		PolicyID:           "policy-id",
		AccountID:          "account-1",
		Reusable:           true,
		AppCount:           1,
		ServiceTokenIDs:    map[string]string{"svc": "token-id"},
		ObservedGeneration: 2,
		Conditions:         []metav1.Condition{status.NewCondition(status.ConditionTypeReady, metav1.ConditionTrue, status.ReasonReady, "ready", 2)},
	}
	if !accessPolicyStatusEqual(base, base.DeepCopy()) {
		t.Fatal("accessPolicyStatusEqual() = false, want true")
	}
	for _, tt := range []struct {
		name   string
		modify func(*cfgatev1alpha1.CloudflareAccessPolicyStatus)
	}{
		{name: "policy ID", modify: func(s *cfgatev1alpha1.CloudflareAccessPolicyStatus) { s.PolicyID = "other" }},
		{name: "account ID", modify: func(s *cfgatev1alpha1.CloudflareAccessPolicyStatus) { s.AccountID = "other" }},
		{name: "reusable", modify: func(s *cfgatev1alpha1.CloudflareAccessPolicyStatus) { s.Reusable = false }},
		{name: "app count", modify: func(s *cfgatev1alpha1.CloudflareAccessPolicyStatus) { s.AppCount = 2 }},
		{name: "service tokens", modify: func(s *cfgatev1alpha1.CloudflareAccessPolicyStatus) { s.ServiceTokenIDs["svc"] = "other" }},
		{name: "generation", modify: func(s *cfgatev1alpha1.CloudflareAccessPolicyStatus) { s.ObservedGeneration = 3 }},
		{name: "conditions", modify: func(s *cfgatev1alpha1.CloudflareAccessPolicyStatus) { s.Conditions[0].Reason = "Other" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			changed := base.DeepCopy()
			tt.modify(changed)
			if accessPolicyStatusEqual(base, changed) {
				t.Fatal("accessPolicyStatusEqual() = true, want false")
			}
		})
	}
	if conditionsEqual(
		[]metav1.Condition{{Type: "A", Status: metav1.ConditionTrue}, {Type: "B", Status: metav1.ConditionTrue}},
		[]metav1.Condition{{Type: "B", Status: metav1.ConditionTrue}, {Type: "A", Status: metav1.ConditionTrue}},
	) {
		t.Fatal("conditionsEqual() = true for reordered conditions, want order-sensitive false")
	}

	policy := baseAccessPolicy("app", "policy")
	policy.Finalizers = []string{accessPolicyFinalizer}
	reconciler := newAccessPolicyReconciler(t, cloudflare.NewMockClient(), policy)
	reconciler.Client = &patchNotFoundClient{Client: reconciler.Client}
	if _, err := reconciler.removeFinalizer(context.Background(), policy); err != nil {
		t.Fatalf("removeFinalizer() NotFound error = %v, want nil", err)
	}
}

func newAccessPolicyReconciler(t *testing.T, mockClient cloudflare.Client, objects ...client.Object) *CloudflareAccessPolicyReconciler {
	t.Helper()
	scheme := controllerTestScheme(t)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&cfgatev1alpha1.CloudflareAccessPolicy{}).
		Build()
	return &CloudflareAccessPolicyReconciler{
		Client:       k8sClient,
		Scheme:       scheme,
		CFClient:     mockClient,
		Recorder:     &accessApplicationEventRecorder{},
		FeatureGates: &features.FeatureGates{ReferenceGrantCRDExists: true},
	}
}

func baseAccessPolicy(namespace, name string) *cfgatev1alpha1.CloudflareAccessPolicy {
	trueValue := true
	return &cfgatev1alpha1.CloudflareAccessPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
			CloudflareRef: cfgatev1alpha1.CloudflareSecretRef{Name: "cf", AccountID: "account-1"},
			Name:          name,
			Decision:      "allow",
			Include:       []cfgatev1alpha1.AccessRule{{Everyone: &trueValue}},
		},
	}
}

func policyWithDeleteStatus() *cfgatev1alpha1.CloudflareAccessPolicy {
	policy := baseAccessPolicy("app", "policy")
	policy.Finalizers = []string{accessPolicyFinalizer}
	setDeletionTimestamp(policy)
	policy.Status.PolicyID = "policy-id"
	policy.Status.ServiceTokenIDs = map[string]string{"svc": "token-id"}
	return policy
}

func assertAccessPolicyFinalizerRemoved(t *testing.T, reconciler *CloudflareAccessPolicyReconciler) {
	t.Helper()
	var current cfgatev1alpha1.CloudflareAccessPolicy
	if err := reconciler.Get(context.Background(), types.NamespacedName{Name: "policy", Namespace: "app"}, &current); err != nil {
		if apierrors.IsNotFound(err) {
			return
		}
		t.Fatalf("Get() error = %v", err)
	}
	if containsString(current.Finalizers, accessPolicyFinalizer) {
		t.Fatalf("finalizers = %#v, want finalizer removed", current.Finalizers)
	}
}

type patchNotFoundClient struct {
	client.Client
}

func (c *patchNotFoundClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	return apierrors.NewNotFound(schema.GroupResource{Group: cfgatev1alpha1.GroupVersion.Group, Resource: "cloudflareaccesspolicies"}, obj.GetName())
}

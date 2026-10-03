package controller

import (
	"cfgate.io/cfgate/internal/controller/status"
	"context"
	"errors"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"testing"

	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestTokenUpdateAndRefreshEnforceGuards(t *testing.T) {
	for _, operation := range []string{"update", "refresh"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			mock := cloudflare.NewMockClient()
			calls := 0
			mock.GetServiceTokenFunc = func(context.Context, string, string) (*cloudflare.ServiceToken, error) {
				return &cloudflare.ServiceToken{ID: "remote", Name: "foreign"}, nil
			}
			mock.UpdateServiceTokenFunc = func(context.Context, string, string, cloudflare.ServiceTokenParams) (*cloudflare.ServiceToken, error) {
				calls++
				return &cloudflare.ServiceToken{ID: "remote"}, nil
			}
			mock.RefreshServiceTokenFunc = func(context.Context, string, string) (*cloudflare.ServiceToken, error) {
				calls++
				return &cloudflare.ServiceToken{ID: "remote"}, nil
			}
			run := func(c cloudflare.AccessClient) error {
				if operation == "update" {
					_, err := c.UpdateServiceToken(ctx, "account", "remote", cloudflare.ServiceTokenParams{})
					return err
				}
				_, err := c.RefreshServiceToken(ctx, "account", "remote")
				return err
			}
			kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).Build()
			owner := &cfg.CloudflareAccessPolicy{ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: "app", UID: "owner"}}
			owned := &ownedAccessClient{Client: mock, kube: kube, reader: kube, installation: "operator", identity: "owner", owner: owner}
			if err := run(owned); err == nil || calls != 0 {
				t.Fatalf("foreign mutation: calls=%d err=%v", calls, err)
			}
			if err := owned.claim(ctx, "account", "token", "remote", true); err != nil {
				t.Fatal(err)
			}
			blocked := errors.New("withdrawal pending")
			guard := &accessMutationClient{Client: owned, before: func(context.Context) error { return blocked }}
			if err := run(guard); !errors.Is(err, blocked) || calls != 0 {
				t.Fatalf("unwithdrawn mutation: calls=%d err=%v", calls, err)
			}
			guard.before = func(context.Context) error { return nil }
			if err := run(guard); err != nil || calls != 1 {
				t.Fatalf("owned withdrawn mutation: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestConditionsCompareObservedGeneration(t *testing.T) {
	base := []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Invalid", Message: "same error", ObservedGeneration: 1}}
	newer := append([]metav1.Condition(nil), base...)
	newer[0].ObservedGeneration = 2
	if conditionsEqual(base, newer) {
		t.Fatal("new failed generation was discarded")
	}
	timestamp := append([]metav1.Condition(nil), base...)
	timestamp[0].LastTransitionTime = metav1.Now()
	if !conditionsEqual(base, timestamp) {
		t.Fatal("timestamp-only change should not force a write")
	}
	for _, resource := range []string{"policy", "application"} {
		t.Run(resource, func(t *testing.T) {
			ctx := context.Background()
			scheme := controllerTestScheme(t)
			if resource == "policy" {
				obj := &cfg.CloudflareAccessPolicy{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "app", UID: "uid"}, Status: cfg.CloudflareAccessPolicyStatus{Conditions: base}}
				kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(obj).WithStatusSubresource(obj).Build()
				r := &CloudflareAccessPolicyReconciler{Client: kube}
				obj.Status.Conditions = newer
				if err := r.updateStatus(ctx, obj); err != nil {
					t.Fatal(err)
				}
				if err := kube.Get(ctx, client.ObjectKey{Namespace: "app", Name: "x"}, obj); err != nil {
					t.Fatal(err)
				}
				if obj.Status.Conditions[0].ObservedGeneration != 2 {
					t.Fatal("policy generation not persisted")
				}
			} else {
				obj := &cfg.CloudflareAccessApplication{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "app", UID: "uid"}, Status: cfg.CloudflareAccessApplicationStatus{Conditions: base}}
				kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(obj).WithStatusSubresource(obj).Build()
				r := &CloudflareAccessApplicationReconciler{Client: kube}
				obj.Status.Conditions = newer
				if err := r.updateApplicationStatus(ctx, obj); err != nil {
					t.Fatal(err)
				}
				if err := kube.Get(ctx, client.ObjectKey{Namespace: "app", Name: "x"}, obj); err != nil {
					t.Fatal(err)
				}
				if obj.Status.Conditions[0].ObservedGeneration != 2 {
					t.Fatal("application generation not persisted")
				}
			}
		})
	}
}

func TestOwnershipCheckpointCannotPublishUncheckedGeneration(t *testing.T) {
	ctx := context.Background()
	scheme := controllerTestScheme(t)
	policy := baseAccessPolicy("app", "policy")
	policy.Generation = 2
	policy.Status.Conditions = []metav1.Condition{
		status.NewCondition(status.ConditionTypeCredentialsValid, metav1.ConditionTrue, "Valid", "ok", 1),
		status.NewCondition(status.ConditionTypePolicySynced, metav1.ConditionTrue, "Synced", "ok", 1),
		status.NewCondition(status.ConditionTypeReady, metav1.ConditionTrue, "Ready", "ok", 1),
	}
	installation := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "operator", UID: "installation"}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy, installation).WithStatusSubresource(policy).Build()
	mock := cloudflare.NewMockClient()
	failure := errors.New("inventory unavailable")
	mock.ListAccessPoliciesFunc = func(context.Context, string) ([]cloudflare.AccessPolicy, error) { return nil, failure }
	r := &CloudflareAccessPolicyReconciler{Client: kube, APIReader: kube, InstallationNamespace: "operator"}
	err := r.prepareOwnedPolicy(ctx, policy, &accessPolicyCredentials{Service: cloudflare.NewAccessService(mock, logr.Discard()), AccountID: "account"})
	if !errors.Is(err, failure) {
		t.Fatalf("expected inventory failure, got %v", err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(policy), policy); err != nil {
		t.Fatal(err)
	}
	ready := status.FindCondition(policy.Status.Conditions, status.ConditionTypeReady)
	if ready == nil || ready.Status != metav1.ConditionUnknown || ready.ObservedGeneration != 2 {
		t.Fatalf("unchecked generation reported ready: %+v", ready)
	}
}

func TestAccessTagDeletionEnforcesOwnershipAndWithdrawal(t *testing.T) {
	ctx := context.Background()
	mock := cloudflare.NewMockClient()
	calls := 0
	mock.DeleteAccessTagFunc = func(context.Context, string, string) error { calls++; return nil }
	owned := &ownedAccessClient{Client: mock, identity: "mine"}
	if err := owned.DeleteAccessTag(ctx, "account", "cfgate:foreign"); err == nil || calls != 0 {
		t.Fatal("foreign tag deleted")
	}
	blocked := errors.New("withdrawal pending")
	guard := &accessMutationClient{Client: owned, before: func(context.Context) error { return blocked }}
	if err := guard.DeleteAccessTag(ctx, "account", "cfgate:mine"); !errors.Is(err, blocked) || calls != 0 {
		t.Fatal("unwithdrawn tag deleted")
	}
	guard.before = func(context.Context) error { return nil }
	if err := guard.DeleteAccessTag(ctx, "account", "cfgate:mine"); err != nil || calls != 1 {
		t.Fatalf("owned tag deletion: %v calls=%d", err, calls)
	}
}

func TestOwnedTokenRotationRejectsChangedIdentity(t *testing.T) {
	for _, result := range []string{"error", "empty", "other", "remote"} {
		t.Run(result, func(t *testing.T) {
			ctx := context.Background()
			mock := cloudflare.NewMockClient()
			mock.RotateServiceTokenFunc = func(context.Context, string, string, cloudflare.ServiceTokenRotateParams) (*cloudflare.ServiceTokenWithSecret, error) {
				if result == "error" {
					return nil, errors.New("provider unavailable")
				}
				if result == "empty" {
					return nil, nil
				}
				return &cloudflare.ServiceTokenWithSecret{ServiceToken: cloudflare.ServiceToken{ID: result}}, nil
			}
			kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).Build()
			owner := baseAccessPolicy("app", "policy")
			owned := &ownedAccessClient{Client: mock, kube: kube, reader: kube, installation: "operator", identity: "mine", owner: owner}
			if err := owned.claim(ctx, "account", "token", "remote", true); err != nil {
				t.Fatal(err)
			}
			_, err := owned.RotateServiceToken(ctx, "account", "remote", cloudflare.ServiceTokenRotateParams{})
			if (err == nil) != (result == "remote") {
				t.Fatalf("rotation result=%s error=%v", result, err)
			}
			var claims corev1.ConfigMapList
			if err := kube.List(ctx, &claims); err != nil {
				t.Fatal(err)
			}
			if len(claims.Items) != 1 || claims.Items[0].Data["remoteID"] != "remote" {
				t.Fatal("rotation claimed another identity")
			}
		})
	}
}

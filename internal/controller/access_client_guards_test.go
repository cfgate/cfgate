package controller

import (
	"context"
	"errors"
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

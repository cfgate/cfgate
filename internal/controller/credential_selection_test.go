package controller

import (
	cfgate "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/controller/annotations"
	"context"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gateway "sigs.k8s.io/gateway-api/apis/v1"
	gatewaybeta "sigs.k8s.io/gateway-api/apis/v1beta1"
	"testing"
)

func TestAccessInheritedTokenKeyPersistsForDeletion(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := gatewaybeta.Install(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := gateway.Install(scheme); err != nil {
		t.Fatal(err)
	}
	if err := cfgate.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	gw := &gateway.Gateway{Spec: gateway.GatewaySpec{GatewayClassName: "cfgate"}, ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "app", Annotations: map[string]string{annotations.AnnotationTunnelRef: "tunnel"}}}
	tunnel := &cfgate.CloudflareTunnel{ObjectMeta: metav1.ObjectMeta{Name: "tunnel", Namespace: "app"}, Spec: cfgate.CloudflareTunnelSpec{Cloudflare: cfgate.CloudflareConfig{AccountID: "account", SecretRef: cfgate.SecretRef{Name: "credentials", Namespace: "infra"}, SecretKeys: cfgate.SecretKeys{APIToken: "selected"}}}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "infra", UID: "credential-uid", ResourceVersion: "1"}, Data: map[string][]byte{"selected": []byte("custom-test-token")}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&gateway.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "cfgate"}, Spec: gateway.GatewayClassSpec{ControllerName: GatewayControllerName}}, gw, tunnel, secret, credentialGrant("app", "infra", "credentials", "CloudflareTunnel", "CloudflareAccessApplication")).Build()
	r := &CloudflareAccessApplicationReconciler{Client: kube, CredentialCache: cloudflare.NewCredentialCache(0)}
	creds, err := r.resolveInheritedApplicationCredentials(context.Background(), "app", accessApplicationTarget{Kind: "Gateway", Namespace: "app", Name: "gateway"})
	if err != nil {
		t.Fatal(err)
	}
	if creds.CredentialSecretKeys.APIToken != "selected" || creds.CredentialSecretRef.Namespace != "infra" || creds.AccountID != "account" {
		t.Fatalf("incomplete inherited credential selection: %+v", creds)
	}
	app := &cfgate.CloudflareAccessApplication{ObjectMeta: metav1.ObjectMeta{Namespace: "app"}, Status: cfgate.CloudflareAccessApplicationStatus{AccountID: creds.AccountID, CredentialSecretRef: creds.CredentialSecretRef, CredentialSecretKeys: creds.CredentialSecretKeys}}
	if err := kube.Delete(context.Background(), tunnel); err != nil {
		t.Fatal(err)
	}
	cleanup, err := r.resolveApplicationDeletionCredentials(context.Background(), app, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup.Service.Client() != creds.Service.Client() || cleanup.CredentialSecretKeys.APIToken != "selected" {
		t.Fatal("cleanup did not preserve inherited token selection")
	}
	changed := app.Status
	changed.CredentialSecretKeys.APIToken = "other"
	if accessApplicationStatusEqual(&app.Status, &changed) {
		t.Fatal("status equality ignored selected token key")
	}
}

func TestPolicyCleanupPreservesSelectedTokenKey(t *testing.T) {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "app", UID: "policy-secret", ResourceVersion: "1"}, Data: map[string][]byte{"selected": []byte("test-token")}}
	kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithObjects(secret).Build()
	r := &CloudflareAccessPolicyReconciler{Client: kube, CredentialCache: cloudflare.NewCredentialCache(0)}
	policy := &cfgate.CloudflareAccessPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: "app"}}
	policy.Spec.CloudflareRef = cfgate.CloudflareSecretRef{Name: secret.Name, AccountID: "account", SecretKeys: cfgate.SecretKeys{APIToken: "selected"}}
	creds, err := r.resolveCredentials(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	policy.Status.AccountID, policy.Status.CredentialSecretRef, policy.Status.CredentialSecretKeys = creds.AccountID, creds.CredentialSecretRef, creds.CredentialSecretKeys
	policy.Spec.CloudflareRef = cfgate.CloudflareSecretRef{}
	cleanup, err := r.resolvePolicyDeletionCredentials(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup.Service.Client() != creds.Service.Client() || cleanup.CredentialSecretKeys.APIToken != "selected" {
		t.Fatal("policy cleanup lost token selection")
	}
}

package controller

import (
	"cfgate.io/cfgate/internal/cloudflare"
	"context"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"testing"

	cfg "cfgate.io/cfgate/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDNSOwnerIdentityPersistsInstallationAndResourceUID(t *testing.T) {
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "operator", UID: "install-one"}}
	dns := &cfg.CloudflareDNS{ObjectMeta: metav1.ObjectMeta{Name: "dns", Namespace: "app", UID: "resource-one"}}
	dns.Spec.Ownership.OwnerID = "legacy/override"
	kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithStatusSubresource(dns).WithObjects(ns, dns).Build()
	r := &CloudflareDNSReconciler{Client: kube, InstallationNamespace: "operator"}
	if err := r.ensureDNSOwnerIdentity(ctx, dns); err != nil {
		t.Fatal(err)
	}
	var persisted cfg.CloudflareDNS
	if err := kube.Get(ctx, client.ObjectKeyFromObject(dns), &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Status.OwnerID != "install-one/resource-one" {
		t.Fatalf("owner=%q", persisted.Status.OwnerID)
	}
	if err := r.ensureDNSOwnerIdentity(ctx, &persisted); err != nil {
		t.Fatal(err)
	}
	if err := kube.Delete(ctx, ns); err != nil {
		t.Fatal(err)
	}
	ns.ResourceVersion, ns.UID = "", "install-two"
	if err := kube.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureDNSOwnerIdentity(ctx, &persisted); err == nil {
		t.Fatal("namespace recreation silently replaced persisted owner")
	}
	persisted.Status.OwnerID = ""
	if err := r.ensureDNSOwnerIdentity(ctx, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Status.OwnerID != "install-two/resource-one" {
		t.Fatalf("new installation=%q", persisted.Status.OwnerID)
	}
	r.InstallationNamespace = ""
	if err := r.ensureDNSOwnerIdentity(ctx, &persisted); err == nil {
		t.Fatal("missing installation fell back to weak name identity")
	}
}

func TestDNSEarlyDeletionInitializesOwnership(t *testing.T) {
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "operator", UID: "install-one"}}
	dns := &cfg.CloudflareDNS{ObjectMeta: metav1.ObjectMeta{Name: "dns", Namespace: "app", UID: "resource-one", Finalizers: []string{dnsFinalizer}}, Spec: cfg.CloudflareDNSSpec{Policy: cfg.DNSPolicySync, Cloudflare: &cfg.CloudflareConfig{SecretRef: cfg.SecretRef{Name: "credentials"}}}}
	kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithStatusSubresource(dns).WithObjects(ns, dns).Build()
	r := &CloudflareDNSReconciler{Client: kube, InstallationNamespace: "operator", CFClient: cloudflare.NewMockClient(), Recorder: &fakeEventRecorder{}}
	if err := kube.Delete(ctx, dns); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dns)}); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(dns), &cfg.CloudflareDNS{}); !apierrors.IsNotFound(err) {
		t.Fatalf("early deletion stranded finalizer: %v", err)
	}
}

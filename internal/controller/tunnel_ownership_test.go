package controller

import (
	cfg "cfgate.io/cfgate/api/v1alpha1"
	"context"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sync"
	"testing"
)

func installTunnelClaimForTest(t *testing.T, r *CloudflareTunnelReconciler, tunnel *cfg.CloudflareTunnel) {
	t.Helper()
	if tunnel.UID == "" {
		tunnel.UID = types.UID("test-" + tunnel.Namespace + "-" + tunnel.Name)
	}
	r.InstallationNamespace = "operator"
	account := tunnel.Spec.Cloudflare.AccountID
	if account == "" {
		account = tunnel.Status.AccountID
	}
	claim := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: tunnelClaimName(account, tunnel.Status.TunnelID), Namespace: "operator", UID: "claim-uid"}, Immutable: ptr.To(true), Data: map[string]string{"ownerUID": string(tunnel.UID), "accountID": account, "tunnelID": tunnel.Status.TunnelID}}
	if err := r.Create(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
}

func TestTunnelClaimHasSingleWinner(t *testing.T) {
	kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).Build()
	r := &CloudflareTunnelReconciler{Client: kube, InstallationNamespace: "operator"}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, uid := range []types.UID{"first", "second"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tunnel := &cfg.CloudflareTunnel{ObjectMeta: metav1.ObjectMeta{Name: string(uid), Namespace: "app", UID: uid, Annotations: map[string]string{adoptExistingAnnotation: "true"}}}
			results <- r.claimTunnel(context.Background(), tunnel, "account", "remote", false)
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("successful claimants=%d, want exactly one", success)
	}
}

func TestTunnelClaimRejectsAutomaticAndForeignAdoption(t *testing.T) {
	kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).Build()
	r := &CloudflareTunnelReconciler{Client: kube, InstallationNamespace: "operator"}
	tunnel := &cfg.CloudflareTunnel{ObjectMeta: metav1.ObjectMeta{Name: "mine", Namespace: "app", UID: "mine"}}
	if err := r.claimTunnel(context.Background(), tunnel, "account", "remote", false); err == nil {
		t.Fatal("unmarked remote automatically adopted")
	}
	tunnel.Annotations = map[string]string{adoptExistingAnnotation: "true"}
	if err := r.claimTunnel(context.Background(), tunnel, "account", "remote", false); err != nil {
		t.Fatal(err)
	}
	foreign := tunnel.DeepCopy()
	foreign.UID = "foreign"
	if err := r.claimTunnel(context.Background(), foreign, "account", "remote", false); err == nil {
		t.Fatal("explicit adoption overwrote foreign claim")
	}
	foreign.Status.TunnelID = "remote"
	if err := r.releaseTunnelClaim(context.Background(), foreign, "account"); err == nil {
		t.Fatal("foreign claim deleted")
	}
}

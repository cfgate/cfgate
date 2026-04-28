package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/controller/annotations"
)

func TestSyncConfigurationPreservesStatusWhenPatchingConfigHash(t *testing.T) {
	ctx := context.Background()
	scheme := controllerTestScheme(t)

	storedTunnel := &cfgatev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "edge",
			Namespace: "default",
		},
		Spec: cfgatev1alpha1.CloudflareTunnelSpec{
			Tunnel: cfgatev1alpha1.TunnelIdentity{
				Name: "edge",
			},
			Cloudflare: cfgatev1alpha1.CloudflareConfig{
				AccountID: "account",
			},
		},
		Status: cfgatev1alpha1.CloudflareTunnelStatus{
			TunnelID:     "old-id",
			TunnelName:   "edge",
			TunnelDomain: cloudflare.TunnelDomain("old-id"),
			AccountID:    "account",
		},
	}
	tunnel := storedTunnel.DeepCopy()
	tunnel.Status.TunnelID = "new-id"
	tunnel.Status.TunnelDomain = cloudflare.TunnelDomain("new-id")

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gateway",
			Namespace: "default",
			Annotations: map[string]string{
				annotations.AnnotationTunnelRef: "default/edge",
			},
		},
	}
	servicePort := gatewayv1.PortNumber(8080)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route",
			Namespace: "default",
		},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name: "gateway",
				}},
			},
			Hostnames: []gatewayv1.Hostname{"app.example.com"},
			Rules: []gatewayv1.HTTPRouteRule{{
				BackendRefs: []gatewayv1.HTTPBackendRef{{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "app",
							Port: &servicePort,
						},
					},
				}},
			}},
		},
	}

	baseClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&cfgatev1alpha1.CloudflareTunnel{}).
		WithObjects(storedTunnel, gateway, route).
		Build()
	patchClient := &statusResettingPatchClient{Client: baseClient}

	updateCalled := false
	mockClient := cloudflare.NewMockClient()
	mockClient.UpdateTunnelConfigurationFunc = func(_ context.Context, accountID, tunnelID string, config cloudflare.TunnelConfiguration) error {
		updateCalled = true
		if accountID != "account" {
			t.Fatalf("accountID = %q, want account", accountID)
		}
		if tunnelID != "new-id" {
			t.Fatalf("tunnelID = %q, want new-id", tunnelID)
		}
		if len(config.Ingress) == 0 || config.Ingress[0].Hostname != "app.example.com" {
			t.Fatalf("config.Ingress = %#v, want app.example.com rule", config.Ingress)
		}
		return nil
	}

	reconciler := &CloudflareTunnelReconciler{
		Client:    patchClient,
		APIReader: baseClient,
		CFClient:  mockClient,
		Recorder:  &fakeEventRecorder{},
	}
	if err := reconciler.syncConfiguration(ctx, tunnel); err != nil {
		t.Fatalf("syncConfiguration() error = %v", err)
	}

	if !updateCalled {
		t.Fatal("UpdateTunnelConfiguration was not called")
	}
	if tunnel.Status.TunnelID != "new-id" {
		t.Fatalf("tunnel.Status.TunnelID = %q, want new-id", tunnel.Status.TunnelID)
	}
	if tunnel.Status.TunnelDomain != cloudflare.TunnelDomain("new-id") {
		t.Fatalf("tunnel.Status.TunnelDomain = %q, want %q", tunnel.Status.TunnelDomain, cloudflare.TunnelDomain("new-id"))
	}
	if tunnel.Status.ConnectedRouteCount != 1 {
		t.Fatalf("tunnel.Status.ConnectedRouteCount = %d, want 1", tunnel.Status.ConnectedRouteCount)
	}

	var current cfgatev1alpha1.CloudflareTunnel
	if err := baseClient.Get(ctx, client.ObjectKeyFromObject(storedTunnel), &current); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	storedHash := current.Annotations[configHashAnnotation]
	if storedHash == "" {
		t.Fatal("config hash annotation was not stored")
	}
	if tunnel.Annotations[configHashAnnotation] != storedHash {
		t.Fatalf("in-memory config hash = %q, want %q", tunnel.Annotations[configHashAnnotation], storedHash)
	}
	if tunnel.ResourceVersion != current.ResourceVersion {
		t.Fatalf("in-memory ResourceVersion = %q, want %q", tunnel.ResourceVersion, current.ResourceVersion)
	}
	if current.Status.TunnelID != "old-id" {
		t.Fatalf("stored Status.TunnelID = %q, want old-id", current.Status.TunnelID)
	}
}

type statusResettingPatchClient struct {
	client.Client
}

func (c *statusResettingPatchClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	serverStatus := cfgatev1alpha1.CloudflareTunnelStatus{}
	hasServerStatus := false
	if tunnel, ok := obj.(*cfgatev1alpha1.CloudflareTunnel); ok {
		var current cfgatev1alpha1.CloudflareTunnel
		if err := c.Get(ctx, client.ObjectKeyFromObject(tunnel), &current); err != nil {
			return err
		}
		serverStatus = current.Status
		hasServerStatus = true
	}

	if err := c.Client.Patch(ctx, obj, patch, opts...); err != nil {
		return err
	}

	if tunnel, ok := obj.(*cfgatev1alpha1.CloudflareTunnel); ok && hasServerStatus {
		tunnel.Status = serverStatus
	}
	return nil
}

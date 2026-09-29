package controller

import (
	"context"
	"testing"

	"cfgate.io/cfgate/internal/cloudflared"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gateway "sigs.k8s.io/gateway-api/apis/v1"
)

func TestKnownStockCloudflaredImages(t *testing.T) {
	for _, image := range []string{"cloudflare/cloudflared", "cloudflare/cloudflared:2026.9.3", "docker.io/cloudflare/cloudflared:latest", "index.docker.io/cloudflare/cloudflared@sha256:abc", "registry-1.docker.io/cloudflare/cloudflared:latest@sha256:abc"} {
		if !knownStockCloudflared(image) {
			t.Errorf("stock image not recognized: %s", image)
		}
	}
	for _, image := range []string{"", cloudflared.DefaultImage, "registry.example:5000/cloudflare/cloudflared:custom", "example.com/cloudflare/cloudflared:custom", "cloudflare/cloudflared-custom:latest"} {
		if knownStockCloudflared(image) {
			t.Errorf("custom image incorrectly rejected: %s", image)
		}
	}
}

func TestStockConnectorWithdrawsH2CAndRestoresFork(t *testing.T) {
	f := newAccessFixture(t)
	delete(f.route.Annotations, "cfgate.io/access-required")
	f.route.Annotations["cfgate.io/origin-h2c"] = "true"
	if err := f.r.Update(context.Background(), f.route); err != nil {
		t.Fatal(err)
	}
	public := f.route.DeepCopy()
	public.Name = "public-http"
	public.ResourceVersion, public.UID = "", ""
	public.Spec.Hostnames = []gateway.Hostname{"public.example.com"}
	public.Annotations = nil
	if err := f.r.Create(context.Background(), public); err != nil {
		t.Fatal(err)
	}
	f.sync(t)
	if f.remoteConfig.Ingress[0].Service == "http_status:503" {
		t.Fatal("default fork did not publish h2c")
	}
	for _, image := range []string{"docker.io/cloudflare/cloudflared:2026.9.3", cloudflared.DefaultImage, "registry.example/custom-h2c:1"} {
		f.tunnel.Spec.Cloudflared.Image = image
		if err := f.r.Update(context.Background(), f.tunnel); err != nil {
			t.Fatal(err)
		}
		f.sync(t)
		matched := map[string]bool{}
		for _, rule := range f.remoteConfig.Ingress {
			matched[rule.Hostname] = true
			if rule.Hostname == "app.example.com" {
				if knownStockCloudflared(image) {
					if rule.Service != "http_status:503" || rule.OriginRequest != nil {
						t.Fatalf("stock image retained h2c forwarding: %+v", rule)
					}
				} else if rule.Service == "http_status:503" || rule.OriginRequest == nil || !rule.OriginRequest.H2cOrigin {
					t.Fatalf("fork/custom h2c not restored: %+v", rule)
				}
			} else if rule.Hostname == "public.example.com" && rule.Service == "http_status:503" {
				t.Fatal("stock image blocked unrelated HTTP routing")
			}
		}
		if !matched["app.example.com"] || !matched["public.example.com"] {
			t.Fatal("compatibility filtering must preserve both route matches")
		}
	}
}

func TestStockConnectorBlocksGlobalH2CForwardingFallback(t *testing.T) {
	f := newAccessFixture(t)
	delete(f.route.Annotations, "cfgate.io/access-required")
	if err := f.r.Update(context.Background(), f.route); err != nil {
		t.Fatal(err)
	}
	f.tunnel.Spec.OriginDefaults.H2cOrigin = true
	f.tunnel.Spec.FallbackTarget = "http://fallback.example:8080"
	f.sync(t)
	f.tunnel.Spec.Cloudflared.Image = "cloudflare/cloudflared@sha256:abc"
	if err := f.r.Update(context.Background(), f.tunnel); err != nil {
		t.Fatal(err)
	}
	f.sync(t)
	if f.remoteConfig.OriginRequest != nil && f.remoteConfig.OriginRequest.H2cOrigin {
		t.Fatal("unsupported global h2c survived stock image selection")
	}
	for _, rule := range f.remoteConfig.Ingress {
		if rule.Service != "http_status:503" {
			t.Fatalf("global h2c origin/fallback still forwarded: %+v", rule)
		}
	}
	// Changing the desired global protocol back to HTTP restores the same stock image.
	if err := f.r.Get(context.Background(), client.ObjectKeyFromObject(f.tunnel), f.tunnel); err != nil {
		t.Fatal(err)
	}
	f.tunnel.Spec.OriginDefaults.H2cOrigin = false
	if err := f.r.Update(context.Background(), f.tunnel); err != nil {
		t.Fatal(err)
	}
	f.sync(t)
	if f.remoteConfig.Ingress[len(f.remoteConfig.Ingress)-1].Service != "http://fallback.example:8080" {
		t.Fatal("ordinary HTTP fallback not restored")
	}
}

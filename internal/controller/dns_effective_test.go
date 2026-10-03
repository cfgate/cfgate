package controller

import (
	cfg "cfgate.io/cfgate/api/v1alpha1"
	"context"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"testing"
)

func TestDNSRouteTTLInheritsLikeExplicit(t *testing.T) {
	for _, proxied := range []bool{false, true} {
		for _, ttl := range []string{"", "1", "600"} {
			tunnel, class, gw, route, service := emissionFixtures()
			route.Annotations = map[string]string{}
			if ttl != "" {
				route.Annotations["cfgate.io/ttl"] = ttl
			}
			kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithObjects(tunnel, class, gw, route, service).Build()
			dns := dnsIdentityFixture()
			dns.Spec.Defaults = cfg.DNSRecordDefaults{TTL: 3600, Proxied: proxied}
			dns.Spec.Source = cfg.DNSHostnameSource{GatewayRoutes: &cfg.DNSGatewayRoutesSource{Enabled: true}}
			r := &CloudflareDNSReconciler{Client: kube, APIReader: kube}
			hosts, err := r.collectHostnames(context.Background(), dns, tunnel)
			if err != nil {
				t.Fatal(err)
			}
			if len(hosts) != 1 {
				t.Fatalf("wanted one hostname, got %v", hosts)
			}
			for name, config := range hosts {
				effective := effectiveDNSHostnameConfig(dns, name, config)
				want := int32(3600)
				if ttl == "1" || proxied {
					want = 1
				} else if ttl == "600" {
					want = 600
				}
				if effective.TTL != want {
					t.Fatalf("proxy=%v ttl=%q got %d want %d", proxied, ttl, effective.TTL, want)
				}
			}
		}
	}
}

func TestDNSEffectiveDuplicateSettings(t *testing.T) {
	for _, scenario := range []string{"inherited", "proxied-ttl", "zone-override", "proxy-conflict", "ttl-conflict", "changed-default"} {
		dns := dnsIdentityFixture()
		dns.Spec.Defaults = cfg.DNSRecordDefaults{TTL: 3600, Proxied: true}
		a := cfg.DNSExplicitHostname{Hostname: "App.example.com"}
		b := cfg.DNSExplicitHostname{Hostname: "app.example.com.", Proxied: ptr.To(true)}
		switch scenario {
		case "proxied-ttl":
			a.TTL = 600
			b.TTL = 3600
		case "zone-override":
			dns.Spec.Defaults.Proxied = false
			dns.Spec.Zones[0].Proxied = ptr.To(true)
		case "proxy-conflict":
			b.Proxied = ptr.To(false)
		case "ttl-conflict":
			a.Proxied = ptr.To(false)
			b.Proxied = ptr.To(false)
			b.TTL = 600
		case "changed-default":
			dns.Spec.Defaults.Proxied = false
		}
		dns.Spec.Source.Explicit = []cfg.DNSExplicitHostname{a, b}
		_, err := (&CloudflareDNSReconciler{}).collectHostnames(context.Background(), dns, nil)
		conflict := scenario == "proxy-conflict" || scenario == "ttl-conflict" || scenario == "changed-default"
		if (err != nil) != conflict {
			t.Fatalf("%s unexpected error %v", scenario, err)
		}
	}
}

func TestDNSDiscoveredEffectiveDuplicates(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		tunnel, class, gw, route, service := emissionFixtures()
		other := route.DeepCopy()
		other.Name = "other"
		other.Annotations = map[string]string{"cfgate.io/cloudflare-proxied": "true", "cfgate.io/ttl": "600"}
		if conflict {
			other.Annotations["cfgate.io/cloudflare-proxied"] = "false"
		}
		kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithObjects(tunnel, class, gw, route, other, service).Build()
		dns := dnsIdentityFixture()
		dns.Spec.Defaults = cfg.DNSRecordDefaults{TTL: 3600, Proxied: true}
		dns.Spec.Source = cfg.DNSHostnameSource{GatewayRoutes: &cfg.DNSGatewayRoutesSource{Enabled: true}}
		r := &CloudflareDNSReconciler{Client: kube, APIReader: kube}
		hosts, err := r.collectHostnames(context.Background(), dns, tunnel)
		if (err != nil) != conflict {
			t.Fatalf("conflict=%v error=%v hosts=%v", conflict, err, hosts)
		}
		if !conflict && len(hosts) != 1 {
			t.Fatalf("wanted one record, got %v", hosts)
		}
	}
}

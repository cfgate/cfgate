package context

import (
	"testing"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
)

func BenchmarkZoneForHostname(b *testing.B) {
	dnsCtx := &DNSContext{
		CloudflareDNS: &cfgatev1alpha1.CloudflareDNS{
			Spec: cfgatev1alpha1.CloudflareDNSSpec{
				Zones: []cfgatev1alpha1.DNSZoneConfig{
					{Name: "example.com"},
					{Name: "internal.example.com"},
				},
			},
		},
	}

	b.ReportAllocs()
	for b.Loop() {
		_, _ = dnsCtx.ZoneForHostname("app.internal.example.com")
	}
}

func BenchmarkCountResolved(b *testing.B) {
	targets := []TargetInfo{
		{Kind: "HTTPRoute", Namespace: "app", Name: "route-a", Resolved: true},
		{Kind: "Gateway", Namespace: "app", Name: "gateway-a", Resolved: true},
		{Kind: "HTTPRoute", Namespace: "app", Name: "route-b", Error: assertErr{}},
	}

	b.ReportAllocs()
	for b.Loop() {
		_ = countResolved(targets)
		_ = countFailed(targets)
	}
}

type assertErr struct{}

func (assertErr) Error() string { return "err" }

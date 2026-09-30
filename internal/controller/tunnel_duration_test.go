package controller

import (
	"cfgate.io/cfgate/internal/cloudflare"
	"testing"
)

func TestTunnelDurationComparisonUsesDesiredWireSemantics(t *testing.T) {
	for _, test := range []struct {
		remote, desired string
		same            bool
	}{
		{"60s", "1m", true}, {"1s", "1500ms", true}, {"30s", "500ms", true}, {"30s", "0s", true}, {"30s", "invalid", true},
		{"0s", "30s", false}, {"0s", "0s", false}, {"0s", "500ms", false}, {"1s", "2s", false}, {"", "30s", false},
	} {
		t.Run(test.remote+"/"+test.desired, func(t *testing.T) {
			remote := cloudflare.TunnelConfiguration{OriginRequest: &cloudflare.OriginRequestConfig{ConnectTimeout: test.remote, H2cOrigin: true}, Ingress: []cloudflare.IngressRule{{Service: "http://origin", OriginRequest: &cloudflare.OriginRequestConfig{ConnectTimeout: test.remote, H2cOrigin: true}}, {Service: "http_status:404"}}}
			desired := cloudflare.TunnelConfiguration{OriginRequest: &cloudflare.OriginRequestConfig{ConnectTimeout: test.desired, H2cOrigin: true}, Ingress: []cloudflare.IngressRule{{Service: "http://origin", OriginRequest: &cloudflare.OriginRequestConfig{ConnectTimeout: test.desired, H2cOrigin: true}}, {Service: "http_status:404"}}}
			if got := equivalentTunnelConfiguration(remote, desired); got != test.same {
				t.Fatalf("same=%t want%t", got, test.same)
			}
			if remote.OriginRequest.ConnectTimeout != test.remote || desired.OriginRequest.ConnectTimeout != test.desired || remote.Ingress[0].OriginRequest.ConnectTimeout != test.remote || desired.Ingress[0].OriginRequest.ConnectTimeout != test.desired {
				t.Fatal("comparison mutated caller configuration")
			}
			desired.OriginRequest.H2cOrigin = false
			if equivalentTunnelConfiguration(remote, desired) {
				t.Fatal("h2c change normalized away")
			}
		})
	}
}

func TestTunnelWarpRoutingDefaultComparison(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		remote := cloudflare.TunnelConfiguration{Ingress: []cloudflare.IngressRule{{Service: "http_status:404"}}, WarpRouting: &cloudflare.WarpRoutingConfig{Enabled: enabled}}
		desired := cloudflare.TunnelConfiguration{Ingress: []cloudflare.IngressRule{{Service: "http_status:404"}}}
		if got := equivalentTunnelConfiguration(remote, desired); got == enabled {
			t.Fatalf("observed warp enabled=%v compared as equal=%v", enabled, got)
		}
		if remote.WarpRouting == nil || remote.WarpRouting.Enabled != enabled {
			t.Fatal("comparison mutated observed config")
		}
	}
}

package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClientSettingsDefaultsAndOverrides(t *testing.T) {
	defaults := DefaultClientSettings()
	normalized, err := NormalizeClientSettings(ClientSettings{})
	if err != nil || normalized != defaults {
		t.Fatalf("defaults=%+v err=%v", normalized, err)
	}
	custom := ClientSettings{AttemptTimeout: time.Second, MaxIngressRules: 2000, MaxConfigurationBytes: 2 << 20}
	c, err := NewClient("test-token", WithClientSettings(custom))
	if err != nil {
		t.Fatal(err)
	}
	if c.(*clientImpl).settings != custom {
		t.Fatalf("settings=%+v", c.(*clientImpl).settings)
	}
	for _, invalid := range []ClientSettings{{AttemptTimeout: -time.Second}, {MaxIngressRules: -1}, {MaxConfigurationBytes: -1}} {
		if _, err := NewClient("test-token", WithClientSettings(invalid)); err == nil {
			t.Fatalf("invalid settings accepted: %+v", invalid)
		}
	}
}

func TestConfigurationBudgetBoundaries(t *testing.T) {
	config := TunnelConfiguration{Ingress: []IngressRule{{Service: "test"}}}
	exact := len(`{"ingress":[{"hostname":"","path":"","service":"test"}]}`)
	settings := ClientSettings{MaxIngressRules: 1, MaxConfigurationBytes: exact}
	if err := ValidateTunnelConfiguration(config, settings); err != nil {
		t.Fatal(err)
	}
	settings.MaxConfigurationBytes--
	if err := ValidateTunnelConfiguration(config, settings); err == nil {
		t.Fatal("accepted byte overflow")
	}
	settings.MaxConfigurationBytes = 1 << 20
	config.Ingress = append(config.Ingress, IngressRule{Service: "other"})
	if err := ValidateTunnelConfiguration(config, settings); err == nil {
		t.Fatal("accepted rule overflow")
	}
	settings.MaxIngressRules = 2
	if err := ValidateTunnelConfiguration(config, settings); err != nil {
		t.Fatalf("explicit larger limit rejected: %v", err)
	}
	config.OriginRequest = &OriginRequestConfig{OriginServerName: strings.Repeat("x", settings.MaxConfigurationBytes)}
	if err := ValidateTunnelConfiguration(config, settings); err == nil {
		t.Fatal("origin defaults bypass byte limit")
	}
}

func TestOversizedConfigurationNeverPublished(t *testing.T) {
	for _, kind := range []string{"count", "bytes"} {
		t.Run(kind, func(t *testing.T) {
			var called bool
			transport := deadlineTransport(func(*http.Request) (*http.Response, error) {
				called = true
				return nil, fmt.Errorf("unexpected outbound request")
			})
			settings := ClientSettings{MaxIngressRules: 1, MaxConfigurationBytes: 128}
			client, err := NewClient("test-token", WithHTTPClient(&http.Client{Transport: transport}), WithClientSettings(settings))
			if err != nil {
				t.Fatal(err)
			}
			config := TunnelConfiguration{Ingress: []IngressRule{{Service: "test"}}}
			if kind == "count" {
				config.Ingress = append(config.Ingress, IngressRule{Service: "other"})
			} else {
				config.Ingress[0].Path = strings.Repeat("x", 129)
			}
			if err := client.UpdateTunnelConfiguration(context.Background(), "account", "tunnel", config); err == nil {
				t.Fatal("expected work-limit error")
			}
			if called {
				t.Fatal("oversized configuration reached HTTP transport")
			}
		})
	}
}

func FuzzTunnelConfigurationBudget(f *testing.F) {
	f.Add("http://origin:80", uint16(1))
	f.Add("\\\"\n", uint16(32))
	f.Add("test", uint16(33))
	f.Fuzz(func(t *testing.T, service string, count uint16) {
		if len(service) > 512 {
			service = service[:512]
		}
		rules := make([]IngressRule, int(count)%65)
		for i := range rules {
			rules[i] = IngressRule{Service: service}
		}
		config := TunnelConfiguration{Ingress: rules}
		settings := ClientSettings{MaxIngressRules: 32, MaxConfigurationBytes: 4096}
		err := ValidateTunnelConfiguration(config, settings)
		// Match the SDK's required empty hostname/path fields independently.
		wireRules := make([]map[string]string, len(rules))
		for i, rule := range rules {
			wireRules[i] = map[string]string{"hostname": rule.Hostname, "path": rule.Path, "service": rule.Service}
		}
		payload, marshalErr := json.Marshal(map[string]any{"ingress": wireRules})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		wantRejected := len(rules) > 32 || len(payload) > 4096
		if (err != nil) != wantRejected {
			t.Fatalf("%d rules / %d encoded bytes: rejected=%v want=%v", len(rules), len(payload), err != nil, wantRejected)
		}
	})
}

func BenchmarkTunnelConfigurationBudget(b *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			rules := make([]IngressRule, count)
			for i := range rules {
				rules[i] = IngressRule{Hostname: fmt.Sprintf("route-%d.example.com", i), Path: "^/api(/.*)?$", Service: "http://service.namespace.svc.cluster.local:8080"}
			}
			config := TunnelConfiguration{Ingress: rules}
			settings := DefaultClientSettings()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err := ValidateTunnelConfiguration(config, settings); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkUpdateTunnelConfiguration(b *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			rules := make([]IngressRule, count)
			for i := range rules {
				rules[i] = IngressRule{Hostname: fmt.Sprintf("route-%d.example.com", i), Path: "^/api(/.*)?$", Service: "http://service.namespace.svc.cluster.local:8080"}
			}
			config := TunnelConfiguration{Ingress: rules}
			transport := deadlineTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"success":true,"result":{}}`)), Request: r}, nil
			})
			client, err := NewClient("test-token", WithHTTPClient(&http.Client{Transport: transport}))
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err := client.UpdateTunnelConfiguration(context.Background(), "account", "tunnel", config); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestUpdateTunnelConfigurationPreservesWarpRouting(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			transport := &tunnelConfigCaptureTransport{}
			c, err := NewClient("test-token", WithHTTPClient(&http.Client{Transport: transport}))
			if err != nil {
				t.Fatal(err)
			}
			config := TunnelConfiguration{Ingress: []IngressRule{{Service: "http_status:404"}}, WarpRouting: &WarpRoutingConfig{Enabled: enabled}}
			if err := c.UpdateTunnelConfiguration(context.Background(), "account", "tunnel", config); err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Config struct {
					WarpRouting *WarpRoutingConfig `json:"warp-routing"`
				} `json:"config"`
			}
			if err := json.Unmarshal(transport.body, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Config.WarpRouting == nil || payload.Config.WarpRouting.Enabled != enabled {
				t.Fatalf("WARP routing changed: %s", transport.body)
			}
		})
	}
}

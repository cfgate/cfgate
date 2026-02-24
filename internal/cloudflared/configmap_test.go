package cloudflared

import (
	"strings"
	"testing"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// newTestTunnel creates a CloudflareTunnel with the given name and optional modifiers.
func newTestTunnel(name string, opts ...func(*cfgatev1alpha1.CloudflareTunnel)) *cfgatev1alpha1.CloudflareTunnel {
	tunnel := &cfgatev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: cfgatev1alpha1.CloudflareTunnelSpec{
			Tunnel: cfgatev1alpha1.TunnelIdentity{
				Name: name,
			},
			Cloudflare: cfgatev1alpha1.CloudflareConfig{
				AccountID: "abc123",
				SecretRef: cfgatev1alpha1.SecretRef{
					Name: "cf-secret",
				},
			},
		},
	}
	for _, opt := range opts {
		opt(tunnel)
	}
	return tunnel
}

func TestNewTunnelConfig(t *testing.T) {
	tests := []struct {
		name             string
		tunnel           *cfgatev1alpha1.CloudflareTunnel
		tunnelID         string
		wantOriginNil    bool
		wantH2c          bool
		wantHTTP2        bool
		wantNoTLSVerify  bool
		wantTimeout      string
		wantFallback     string
		wantCatchAllLast bool
	}{
		{
			name:             "default tunnel no origin settings",
			tunnel:           newTestTunnel("test"),
			tunnelID:         "test-id",
			wantOriginNil:    true,
			wantFallback:     "http_status:404",
			wantCatchAllLast: true,
		},
		{
			name: "h2c origin enabled",
			tunnel: newTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
				t.Spec.OriginDefaults.H2cOrigin = true
			}),
			tunnelID:         "test-id",
			wantOriginNil:    false,
			wantH2c:          true,
			wantCatchAllLast: true,
			wantFallback:     "http_status:404",
		},
		{
			name: "http2 origin enabled",
			tunnel: newTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
				t.Spec.OriginDefaults.HTTP2Origin = true
			}),
			tunnelID:         "test-id",
			wantOriginNil:    false,
			wantHTTP2:        true,
			wantCatchAllLast: true,
			wantFallback:     "http_status:404",
		},
		{
			name: "connect timeout set",
			tunnel: newTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
				t.Spec.OriginDefaults.ConnectTimeout = "10s"
			}),
			tunnelID:         "test-id",
			wantOriginNil:    false,
			wantTimeout:      "10s",
			wantCatchAllLast: true,
			wantFallback:     "http_status:404",
		},
		{
			name: "no TLS verify enabled",
			tunnel: newTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
				t.Spec.OriginDefaults.NoTLSVerify = true
			}),
			tunnelID:         "test-id",
			wantOriginNil:    false,
			wantNoTLSVerify:  true,
			wantCatchAllLast: true,
			wantFallback:     "http_status:404",
		},
		{
			name: "custom fallback target",
			tunnel: newTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
				t.Spec.FallbackTarget = "http://fallback.default.svc:8080"
			}),
			tunnelID:         "test-id",
			wantOriginNil:    true,
			wantFallback:     "http://fallback.default.svc:8080",
			wantCatchAllLast: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := NewTunnelConfig(tt.tunnel, tt.tunnelID)

			if config.TunnelID != tt.tunnelID {
				t.Errorf("TunnelID = %q, want %q", config.TunnelID, tt.tunnelID)
			}

			if tt.wantOriginNil {
				if config.OriginRequest != nil {
					t.Errorf("OriginRequest should be nil, got %+v", config.OriginRequest)
				}
			} else {
				if config.OriginRequest == nil {
					t.Fatal("OriginRequest should not be nil")
				}
				if config.OriginRequest.H2cOrigin != tt.wantH2c {
					t.Errorf("H2cOrigin = %v, want %v", config.OriginRequest.H2cOrigin, tt.wantH2c)
				}
				if config.OriginRequest.HTTP2Origin != tt.wantHTTP2 {
					t.Errorf("HTTP2Origin = %v, want %v", config.OriginRequest.HTTP2Origin, tt.wantHTTP2)
				}
				if config.OriginRequest.NoTLSVerify != tt.wantNoTLSVerify {
					t.Errorf("NoTLSVerify = %v, want %v", config.OriginRequest.NoTLSVerify, tt.wantNoTLSVerify)
				}
				if tt.wantTimeout != "" && config.OriginRequest.ConnectTimeout != tt.wantTimeout {
					t.Errorf("ConnectTimeout = %q, want %q", config.OriginRequest.ConnectTimeout, tt.wantTimeout)
				}
			}

			// Verify catch-all is always last
			if tt.wantCatchAllLast {
				if len(config.Ingress) == 0 {
					t.Fatal("Ingress should not be empty")
				}
				last := config.Ingress[len(config.Ingress)-1]
				if last.Hostname != "" || last.Path != "" {
					t.Errorf("last rule should be catch-all, got hostname=%q path=%q", last.Hostname, last.Path)
				}
				if last.Service != tt.wantFallback {
					t.Errorf("fallback service = %q, want %q", last.Service, tt.wantFallback)
				}
			}
		})
	}
}

func TestBuildOriginConfig(t *testing.T) {
	tests := []struct {
		name        string
		defaults    *cfgatev1alpha1.OriginDefaults
		annotations map[string]string
		wantNil     bool
		wantH2c     bool
		wantHTTP2   bool
	}{
		{
			name:        "nil defaults nil annotations",
			defaults:    nil,
			annotations: nil,
			wantNil:     true,
		},
		{
			name: "CRD defaults h2c",
			defaults: &cfgatev1alpha1.OriginDefaults{
				H2cOrigin: true,
			},
			annotations: nil,
			wantNil:     false,
			wantH2c:     true,
		},
		{
			name:     "annotation override h2c",
			defaults: nil,
			annotations: map[string]string{
				"cfgate.io/origin-h2c": "true",
			},
			wantNil: false,
			wantH2c: true,
		},
		{
			name: "CRD defaults plus annotation override",
			defaults: &cfgatev1alpha1.OriginDefaults{
				HTTP2Origin: true,
			},
			annotations: map[string]string{
				"cfgate.io/origin-h2c": "true",
			},
			wantNil:   false,
			wantH2c:   true,
			wantHTTP2: true,
		},
		{
			name:        "all fields empty",
			defaults:    &cfgatev1alpha1.OriginDefaults{},
			annotations: map[string]string{},
			wantNil:     true,
		},
		{
			name: "HTTP2Origin from CRD default",
			defaults: &cfgatev1alpha1.OriginDefaults{
				HTTP2Origin: true,
			},
			annotations: nil,
			wantNil:     false,
			wantHTTP2:   true,
		},
		{
			name:     "origin-http2 annotation",
			defaults: nil,
			annotations: map[string]string{
				"cfgate.io/origin-http2": "true",
			},
			wantNil:   false,
			wantHTTP2: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := BuildOriginConfig(tt.defaults, tt.annotations)

			if tt.wantNil {
				if config != nil {
					t.Errorf("expected nil, got %+v", config)
				}
				return
			}

			if config == nil {
				t.Fatal("expected non-nil config")
				return
			}
			if config.H2cOrigin != tt.wantH2c {
				t.Errorf("H2cOrigin = %v, want %v", config.H2cOrigin, tt.wantH2c)
			}
			if config.HTTP2Origin != tt.wantHTTP2 {
				t.Errorf("HTTP2Origin = %v, want %v", config.HTTP2Origin, tt.wantHTTP2)
			}
		})
	}
}

func TestAddRule(t *testing.T) {
	t.Run("add rule before catch-all", func(t *testing.T) {
		tunnel := newTestTunnel("test")
		config := NewTunnelConfig(tunnel, "test-id")

		rule := IngressRule{
			Hostname: "example.com",
			Service:  "http://web.default.svc:80",
		}
		config.AddRule(rule)

		if len(config.Ingress) != 2 {
			t.Fatalf("expected 2 rules, got %d", len(config.Ingress))
		}

		// First rule should be the added rule
		if config.Ingress[0].Hostname != "example.com" {
			t.Errorf("first rule hostname = %q, want %q", config.Ingress[0].Hostname, "example.com")
		}

		// Last rule should still be catch-all
		last := config.Ingress[len(config.Ingress)-1]
		if last.Hostname != "" || last.Path != "" {
			t.Errorf("last rule should be catch-all, got hostname=%q path=%q", last.Hostname, last.Path)
		}
	})

	t.Run("add rule to empty config", func(t *testing.T) {
		config := &TunnelConfig{
			TunnelID: "test-id",
			Ingress:  []IngressRule{},
		}

		rule := IngressRule{
			Hostname: "example.com",
			Service:  "http://web.default.svc:80",
		}
		config.AddRule(rule)

		if len(config.Ingress) != 1 {
			t.Fatalf("expected 1 rule, got %d", len(config.Ingress))
		}
		if config.Ingress[0].Hostname != "example.com" {
			t.Errorf("rule hostname = %q, want %q", config.Ingress[0].Hostname, "example.com")
		}
	})
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  *TunnelConfig
		wantErr bool
	}{
		{
			name: "valid config",
			config: &TunnelConfig{
				TunnelID: "test-id",
				Ingress: []IngressRule{
					{Hostname: "example.com", Service: "http://web:80"},
					{Service: "http_status:404"},
				},
			},
			wantErr: false,
		},
		{
			name: "missing tunnel ID",
			config: &TunnelConfig{
				Ingress: []IngressRule{
					{Service: "http_status:404"},
				},
			},
			wantErr: true,
		},
		{
			name: "empty ingress",
			config: &TunnelConfig{
				TunnelID: "test-id",
				Ingress:  []IngressRule{},
			},
			wantErr: true,
		},
		{
			name: "no catch-all",
			config: &TunnelConfig{
				TunnelID: "test-id",
				Ingress: []IngressRule{
					{Hostname: "example.com", Service: "http://web:80"},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr && err == nil {
				t.Errorf("Validate() expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Validate() unexpected error: %v", err)
			}
		})
	}
}

func TestMarshal(t *testing.T) {
	t.Run("marshal with h2cOrigin", func(t *testing.T) {
		config := &TunnelConfig{
			TunnelID: "test-id",
			Ingress: []IngressRule{
				{Service: "http_status:404"},
			},
			OriginRequest: &OriginRequestConfig{
				H2cOrigin: true,
			},
		}

		data, err := config.Marshal()
		if err != nil {
			t.Fatalf("Marshal() error: %v", err)
		}

		yaml := string(data)
		if !strings.Contains(yaml, "h2cOrigin: true") {
			t.Errorf("marshaled YAML should contain 'h2cOrigin: true', got:\n%s", yaml)
		}
	})

	t.Run("marshal without h2cOrigin", func(t *testing.T) {
		config := &TunnelConfig{
			TunnelID: "test-id",
			Ingress: []IngressRule{
				{Service: "http_status:404"},
			},
		}

		data, err := config.Marshal()
		if err != nil {
			t.Fatalf("Marshal() error: %v", err)
		}

		yaml := string(data)
		if strings.Contains(yaml, "h2cOrigin") {
			t.Errorf("marshaled YAML should NOT contain 'h2cOrigin', got:\n%s", yaml)
		}
	})
}

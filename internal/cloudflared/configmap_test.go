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
		wantCAPool       string
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
			name: "ca pool secret ref set",
			tunnel: newTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
				t.Spec.OriginDefaults.CAPoolSecretRef = &cfgatev1alpha1.CAPoolSecretRef{Name: "origin-ca"}
			}),
			tunnelID:         "test-id",
			wantOriginNil:    false,
			wantCAPool:       OriginCAPoolPath(),
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
				if tt.wantCAPool != "" && config.OriginRequest.CAPool != tt.wantCAPool {
					t.Errorf("CAPool = %q, want %q", config.OriginRequest.CAPool, tt.wantCAPool)
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
		{
			name: "h2c and http2 mutually exclusive",
			config: &TunnelConfig{
				TunnelID: "test-id",
				Ingress: []IngressRule{
					{Service: "http_status:404"},
				},
				OriginRequest: &OriginRequestConfig{
					H2cOrigin:   true,
					HTTP2Origin: true,
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

func TestNewTunnelConfigProtocol(t *testing.T) {
	tests := []struct {
		name         string
		tunnel       *cfgatev1alpha1.CloudflareTunnel
		tunnelID     string
		wantProtocol string
		wantMetrics  string
		checkOrigin  bool
		wantH2c      bool
		wantTimeout  string
	}{
		{
			name: "protocol quic is set",
			tunnel: newTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
				t.Spec.Cloudflared.Protocol = "quic"
			}),
			tunnelID:     "test-id",
			wantProtocol: "quic",
		},
		{
			name: "protocol http2 is set",
			tunnel: newTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
				t.Spec.Cloudflared.Protocol = "http2"
			}),
			tunnelID:     "test-id",
			wantProtocol: "http2",
		},
		{
			name: "protocol auto is omitted",
			tunnel: newTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
				t.Spec.Cloudflared.Protocol = "auto"
			}),
			tunnelID:     "test-id",
			wantProtocol: "",
		},
		{
			name:         "protocol empty is omitted",
			tunnel:       newTestTunnel("test"),
			tunnelID:     "test-id",
			wantProtocol: "",
		},
		{
			name: "custom metrics port propagation",
			tunnel: newTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
				t.Spec.Cloudflared.Metrics.Port = 9090
			}),
			tunnelID:    "test-id",
			wantMetrics: "0.0.0.0:9090",
		},
		{
			name: "combined origin defaults h2c and connect timeout",
			tunnel: newTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
				t.Spec.OriginDefaults.H2cOrigin = true
				t.Spec.OriginDefaults.ConnectTimeout = "5s"
			}),
			tunnelID:    "test-id",
			checkOrigin: true,
			wantH2c:     true,
			wantTimeout: "5s",
		},
		{
			name:     "NoAutoUpdate invariant",
			tunnel:   newTestTunnel("test"),
			tunnelID: "test-id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := NewTunnelConfig(tt.tunnel, tt.tunnelID)

			if config.Protocol != tt.wantProtocol {
				t.Errorf("Protocol = %q, want %q", config.Protocol, tt.wantProtocol)
			}
			if tt.wantMetrics != "" && config.Metrics != tt.wantMetrics {
				t.Errorf("Metrics = %q, want %q", config.Metrics, tt.wantMetrics)
			}
			if !config.NoAutoUpdate {
				t.Error("NoAutoUpdate should always be true")
			}
			if tt.checkOrigin {
				if config.OriginRequest == nil {
					t.Fatal("OriginRequest should not be nil")
				}
				if config.OriginRequest.H2cOrigin != tt.wantH2c {
					t.Errorf("H2cOrigin = %v, want %v", config.OriginRequest.H2cOrigin, tt.wantH2c)
				}
				if tt.wantTimeout != "" && config.OriginRequest.ConnectTimeout != tt.wantTimeout {
					t.Errorf("ConnectTimeout = %q, want %q", config.OriginRequest.ConnectTimeout, tt.wantTimeout)
				}
			}
		})
	}
}

func TestSetCatchAll(t *testing.T) {
	t.Run("replace existing catch-all", func(t *testing.T) {
		tunnel := newTestTunnel("test")
		config := NewTunnelConfig(tunnel, "test-id")

		config.SetCatchAll("http_status:503")

		if len(config.Ingress) != 1 {
			t.Fatalf("expected 1 rule, got %d", len(config.Ingress))
		}
		if config.Ingress[0].Service != "http_status:503" {
			t.Errorf("catch-all service = %q, want %q", config.Ingress[0].Service, "http_status:503")
		}
	})

	t.Run("set on empty ingress", func(t *testing.T) {
		config := &TunnelConfig{
			TunnelID: "test-id",
			Ingress:  []IngressRule{},
		}

		config.SetCatchAll("http_status:404")

		if len(config.Ingress) != 1 {
			t.Fatalf("expected 1 rule, got %d", len(config.Ingress))
		}
		if config.Ingress[0].Service != "http_status:404" {
			t.Errorf("service = %q, want %q", config.Ingress[0].Service, "http_status:404")
		}
	})

	t.Run("set when last rule has hostname", func(t *testing.T) {
		config := &TunnelConfig{
			TunnelID: "test-id",
			Ingress: []IngressRule{
				{Hostname: "example.com", Service: "http://web:80"},
			},
		}

		config.SetCatchAll("http_status:404")

		if len(config.Ingress) != 2 {
			t.Fatalf("expected 2 rules, got %d", len(config.Ingress))
		}
		last := config.Ingress[len(config.Ingress)-1]
		if last.Hostname != "" || last.Path != "" {
			t.Errorf("last rule should be catch-all, got hostname=%q path=%q", last.Hostname, last.Path)
		}
		if last.Service != "http_status:404" {
			t.Errorf("catch-all service = %q, want %q", last.Service, "http_status:404")
		}
	})

	t.Run("preserves other rules", func(t *testing.T) {
		config := &TunnelConfig{
			TunnelID: "test-id",
			Ingress: []IngressRule{
				{Hostname: "a.com", Service: "http://a:80"},
				{Hostname: "b.com", Service: "http://b:80"},
				{Service: "http_status:404"},
			},
		}

		config.SetCatchAll("http_status:503")

		if len(config.Ingress) != 3 {
			t.Fatalf("expected 3 rules, got %d", len(config.Ingress))
		}
		if config.Ingress[0].Hostname != "a.com" {
			t.Errorf("first rule hostname = %q, want %q", config.Ingress[0].Hostname, "a.com")
		}
		if config.Ingress[1].Hostname != "b.com" {
			t.Errorf("second rule hostname = %q, want %q", config.Ingress[1].Hostname, "b.com")
		}
		if config.Ingress[2].Service != "http_status:503" {
			t.Errorf("catch-all service = %q, want %q", config.Ingress[2].Service, "http_status:503")
		}
	})
}

func TestAddRuleExtended(t *testing.T) {
	t.Run("three rules in sequence preserve ordering", func(t *testing.T) {
		tunnel := newTestTunnel("test")
		config := NewTunnelConfig(tunnel, "test-id")

		config.AddRule(IngressRule{Hostname: "a.com", Service: "http://a:80"})
		config.AddRule(IngressRule{Hostname: "b.com", Service: "http://b:80"})
		config.AddRule(IngressRule{Hostname: "c.com", Service: "http://c:80"})

		if len(config.Ingress) != 4 {
			t.Fatalf("expected 4 rules, got %d", len(config.Ingress))
		}
		if config.Ingress[0].Hostname != "a.com" {
			t.Errorf("rule 0 hostname = %q, want %q", config.Ingress[0].Hostname, "a.com")
		}
		if config.Ingress[1].Hostname != "b.com" {
			t.Errorf("rule 1 hostname = %q, want %q", config.Ingress[1].Hostname, "b.com")
		}
		if config.Ingress[2].Hostname != "c.com" {
			t.Errorf("rule 2 hostname = %q, want %q", config.Ingress[2].Hostname, "c.com")
		}
		last := config.Ingress[3]
		if last.Hostname != "" || last.Path != "" {
			t.Errorf("last rule should be catch-all, got hostname=%q path=%q", last.Hostname, last.Path)
		}
	})

	t.Run("rule with path only", func(t *testing.T) {
		tunnel := newTestTunnel("test")
		config := NewTunnelConfig(tunnel, "test-id")

		config.AddRule(IngressRule{Path: "/api", Service: "http://api:80"})

		if len(config.Ingress) != 2 {
			t.Fatalf("expected 2 rules, got %d", len(config.Ingress))
		}
		if config.Ingress[0].Path != "/api" {
			t.Errorf("rule path = %q, want %q", config.Ingress[0].Path, "/api")
		}
	})

	t.Run("rule with hostname and path", func(t *testing.T) {
		tunnel := newTestTunnel("test")
		config := NewTunnelConfig(tunnel, "test-id")

		config.AddRule(IngressRule{
			Hostname: "example.com",
			Path:     "/api",
			Service:  "http://api:80",
		})

		if len(config.Ingress) != 2 {
			t.Fatalf("expected 2 rules, got %d", len(config.Ingress))
		}
		if config.Ingress[0].Hostname != "example.com" {
			t.Errorf("hostname = %q, want %q", config.Ingress[0].Hostname, "example.com")
		}
		if config.Ingress[0].Path != "/api" {
			t.Errorf("path = %q, want %q", config.Ingress[0].Path, "/api")
		}
	})

	t.Run("add when last rule has hostname", func(t *testing.T) {
		config := &TunnelConfig{
			TunnelID: "test-id",
			Ingress: []IngressRule{
				{Hostname: "existing.com", Service: "http://web:80"},
			},
		}

		config.AddRule(IngressRule{Hostname: "new.com", Service: "http://new:80"})

		if len(config.Ingress) != 2 {
			t.Fatalf("expected 2 rules, got %d", len(config.Ingress))
		}
		if config.Ingress[1].Hostname != "new.com" {
			t.Errorf("rule 1 hostname = %q, want %q", config.Ingress[1].Hostname, "new.com")
		}
	})
}

func TestParseConfig(t *testing.T) {
	roundTrip := &TunnelConfig{
		TunnelID:     "test-id",
		NoAutoUpdate: true,
		Metrics:      "0.0.0.0:44483",
		Ingress: []IngressRule{
			{Hostname: "example.com", Service: "http://web:80"},
			{Service: "http_status:404"},
		},
	}
	roundTripData, err := roundTrip.Marshal()
	if err != nil {
		t.Fatalf("setup: Marshal() error: %v", err)
	}

	tests := []struct {
		name    string
		input   []byte
		wantErr bool
		check   func(t *testing.T, config *TunnelConfig)
	}{
		{
			name:  "round-trip from marshaled config",
			input: roundTripData,
			check: func(t *testing.T, config *TunnelConfig) {
				if config.TunnelID != "test-id" {
					t.Errorf("TunnelID = %q, want %q", config.TunnelID, "test-id")
				}
				if len(config.Ingress) != 2 {
					t.Fatalf("expected 2 ingress rules, got %d", len(config.Ingress))
				}
				if config.Ingress[0].Hostname != "example.com" {
					t.Errorf("rule 0 hostname = %q, want %q", config.Ingress[0].Hostname, "example.com")
				}
			},
		},
		{
			name:  "minimal YAML",
			input: []byte("tunnel: abc\ningress:\n- service: http_status:404\n"),
			check: func(t *testing.T, config *TunnelConfig) {
				if config.TunnelID != "abc" {
					t.Errorf("TunnelID = %q, want %q", config.TunnelID, "abc")
				}
				if len(config.Ingress) != 1 {
					t.Fatalf("expected 1 ingress rule, got %d", len(config.Ingress))
				}
				if config.Ingress[0].Service != "http_status:404" {
					t.Errorf("service = %q, want %q", config.Ingress[0].Service, "http_status:404")
				}
			},
		},
		{
			name:    "invalid YAML",
			input:   []byte(":::bad yaml"),
			wantErr: true,
		},
		{
			name:  "empty data",
			input: []byte(""),
			check: func(t *testing.T, config *TunnelConfig) {
				if config.TunnelID != "" {
					t.Errorf("TunnelID = %q, want empty", config.TunnelID)
				}
			},
		},
		{
			name: "all fields populated",
			input: []byte("tunnel: full-test\n" +
				"credentials-file: /etc/creds\n" +
				"protocol: quic\n" +
				"loglevel: debug\n" +
				"no-autoupdate: true\n" +
				"metrics: 0.0.0.0:9090\n" +
				"originRequest:\n" +
				"  connectTimeout: 10s\n" +
				"  noTLSVerify: true\n" +
				"  h2cOrigin: true\n" +
				"warp-routing:\n" +
				"  enabled: true\n" +
				"ingress:\n" +
				"- hostname: example.com\n" +
				"  service: http://web:80\n" +
				"- service: http_status:404\n"),
			check: func(t *testing.T, config *TunnelConfig) {
				if config.TunnelID != "full-test" {
					t.Errorf("TunnelID = %q, want %q", config.TunnelID, "full-test")
				}
				if config.CredentialsFile != "/etc/creds" {
					t.Errorf("CredentialsFile = %q, want %q", config.CredentialsFile, "/etc/creds")
				}
				if config.Protocol != "quic" {
					t.Errorf("Protocol = %q, want %q", config.Protocol, "quic")
				}
				if config.LogLevel != "debug" {
					t.Errorf("LogLevel = %q, want %q", config.LogLevel, "debug")
				}
				if !config.NoAutoUpdate {
					t.Error("NoAutoUpdate should be true")
				}
				if config.Metrics != "0.0.0.0:9090" {
					t.Errorf("Metrics = %q, want %q", config.Metrics, "0.0.0.0:9090")
				}
				if config.OriginRequest == nil {
					t.Fatal("OriginRequest should not be nil")
				}
				if config.OriginRequest.ConnectTimeout != "10s" {
					t.Errorf("ConnectTimeout = %q, want %q", config.OriginRequest.ConnectTimeout, "10s")
				}
				if !config.OriginRequest.NoTLSVerify {
					t.Error("NoTLSVerify should be true")
				}
				if !config.OriginRequest.H2cOrigin {
					t.Error("H2cOrigin should be true")
				}
				if config.WarpRouting == nil {
					t.Fatal("WarpRouting should not be nil")
				}
				if !config.WarpRouting.Enabled {
					t.Error("WarpRouting.Enabled should be true")
				}
				if len(config.Ingress) != 2 {
					t.Fatalf("expected 2 ingress rules, got %d", len(config.Ingress))
				}
				if config.Ingress[0].Hostname != "example.com" {
					t.Errorf("rule 0 hostname = %q, want %q", config.Ingress[0].Hostname, "example.com")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := ParseConfig(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("ParseConfig() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseConfig() unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, config)
			}
		})
	}
}

func TestValidateExtended(t *testing.T) {
	tests := []struct {
		name    string
		config  *TunnelConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "rule with empty service string",
			config: &TunnelConfig{
				TunnelID: "test-id",
				Ingress: []IngressRule{
					{Hostname: "example.com", Service: ""},
					{Service: "http_status:404"},
				},
			},
			wantErr: true,
			errMsg:  "service is required",
		},
		{
			name: "valid with multiple rules",
			config: &TunnelConfig{
				TunnelID: "test-id",
				Ingress: []IngressRule{
					{Hostname: "a.com", Service: "http://a:80"},
					{Hostname: "b.com", Service: "http://b:80"},
					{Hostname: "c.com", Service: "http://c:80"},
					{Service: "http_status:404"},
				},
			},
			wantErr: false,
		},
		{
			name: "only catch-all rule is valid",
			config: &TunnelConfig{
				TunnelID: "test-id",
				Ingress: []IngressRule{
					{Service: "http_status:404"},
				},
			},
			wantErr: false,
		},
		{
			name: "nil OriginRequest passes",
			config: &TunnelConfig{
				TunnelID: "test-id",
				Ingress: []IngressRule{
					{Service: "http_status:404"},
				},
			},
			wantErr: false,
		},
		{
			name: "h2c only passes",
			config: &TunnelConfig{
				TunnelID: "test-id",
				Ingress: []IngressRule{
					{Service: "http_status:404"},
				},
				OriginRequest: &OriginRequestConfig{
					H2cOrigin: true,
				},
			},
			wantErr: false,
		},
		{
			name: "http2 only passes",
			config: &TunnelConfig{
				TunnelID: "test-id",
				Ingress: []IngressRule{
					{Service: "http_status:404"},
				},
				OriginRequest: &OriginRequestConfig{
					HTTP2Origin: true,
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				if err == nil {
					t.Error("Validate() expected error, got nil")
				} else if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("error = %q, want to contain %q", err.Error(), tt.errMsg)
				}
				return
			}
			if err != nil {
				t.Errorf("Validate() unexpected error: %v", err)
			}
		})
	}
}

func TestMarshalExtended(t *testing.T) {
	t.Run("round-trip consistency", func(t *testing.T) {
		original := &TunnelConfig{
			TunnelID:     "test-id",
			NoAutoUpdate: true,
			Metrics:      "0.0.0.0:44483",
			Protocol:     "quic",
			Ingress: []IngressRule{
				{Hostname: "example.com", Service: "http://web:80"},
				{Service: "http_status:404"},
			},
			OriginRequest: &OriginRequestConfig{
				ConnectTimeout: "10s",
				H2cOrigin:      true,
			},
		}

		data, err := original.Marshal()
		if err != nil {
			t.Fatalf("Marshal() error: %v", err)
		}

		parsed, err := ParseConfig(data)
		if err != nil {
			t.Fatalf("ParseConfig() error: %v", err)
		}

		if parsed.TunnelID != original.TunnelID {
			t.Errorf("TunnelID = %q, want %q", parsed.TunnelID, original.TunnelID)
		}
		if parsed.Protocol != original.Protocol {
			t.Errorf("Protocol = %q, want %q", parsed.Protocol, original.Protocol)
		}
		if parsed.NoAutoUpdate != original.NoAutoUpdate {
			t.Errorf("NoAutoUpdate = %v, want %v", parsed.NoAutoUpdate, original.NoAutoUpdate)
		}
		if len(parsed.Ingress) != len(original.Ingress) {
			t.Errorf("Ingress len = %d, want %d", len(parsed.Ingress), len(original.Ingress))
		}
		if parsed.OriginRequest == nil {
			t.Fatal("OriginRequest should not be nil")
		}
		if parsed.OriginRequest.ConnectTimeout != original.OriginRequest.ConnectTimeout {
			t.Errorf("ConnectTimeout = %q, want %q", parsed.OriginRequest.ConnectTimeout, original.OriginRequest.ConnectTimeout)
		}
	})

	t.Run("protocol field present in YAML", func(t *testing.T) {
		config := &TunnelConfig{
			TunnelID: "test-id",
			Protocol: "quic",
			Ingress: []IngressRule{
				{Service: "http_status:404"},
			},
		}

		data, err := config.Marshal()
		if err != nil {
			t.Fatalf("Marshal() error: %v", err)
		}

		yamlStr := string(data)
		if !strings.Contains(yamlStr, "protocol: quic") {
			t.Errorf("YAML should contain 'protocol: quic', got:\n%s", yamlStr)
		}
	})

	t.Run("WarpRouting present in YAML", func(t *testing.T) {
		config := &TunnelConfig{
			TunnelID: "test-id",
			Ingress: []IngressRule{
				{Service: "http_status:404"},
			},
			WarpRouting: &WarpRoutingConfig{Enabled: true},
		}

		data, err := config.Marshal()
		if err != nil {
			t.Fatalf("Marshal() error: %v", err)
		}

		yamlStr := string(data)
		if !strings.Contains(yamlStr, "warp-routing") {
			t.Errorf("YAML should contain 'warp-routing', got:\n%s", yamlStr)
		}
	})

	t.Run("all OriginRequest fields present", func(t *testing.T) {
		config := &TunnelConfig{
			TunnelID: "test-id",
			Ingress: []IngressRule{
				{Service: "http_status:404"},
			},
			OriginRequest: &OriginRequestConfig{
				ConnectTimeout:         "10s",
				TLSTimeout:             "15s",
				TCPKeepAlive:           "30s",
				NoHappyEyeballs:        true,
				KeepAliveConnections:   10,
				KeepAliveTimeout:       "60s",
				HTTPHostHeader:         "custom.host",
				OriginServerName:       "origin.local",
				CAPool:                 "/path/to/ca",
				NoTLSVerify:            true,
				DisableChunkedEncoding: true,
				BastionMode:            true,
				ProxyAddress:           "127.0.0.1",
				ProxyPort:              8080,
				ProxyType:              "socks5",
				HTTP2Origin:            true,
			},
		}

		data, err := config.Marshal()
		if err != nil {
			t.Fatalf("Marshal() error: %v", err)
		}

		yamlStr := string(data)
		for _, field := range []string{
			"connectTimeout:", "tlsTimeout:", "tcpKeepAlive:",
			"noHappyEyeballs:", "keepAliveConnections:", "keepAliveTimeout:",
			"httpHostHeader:", "originServerName:", "caPool:",
			"noTLSVerify:", "disableChunkedEncoding:", "bastionMode:",
			"proxyAddress:", "proxyPort:", "proxyType:", "http2Origin:",
		} {
			if !strings.Contains(yamlStr, field) {
				t.Errorf("YAML should contain %q, got:\n%s", field, yamlStr)
			}
		}
	})
}

func TestBuildOriginConfigAnnotations(t *testing.T) {
	tests := []struct {
		name               string
		defaults           *cfgatev1alpha1.OriginDefaults
		annotations        map[string]string
		wantNil            bool
		wantConnectTimeout string
		wantNoTLSVerify    bool
		wantHTTPHostHeader string
		wantServerName     string
		wantCAPool         string
		wantH2c            bool
		wantHTTP2          bool
	}{
		{
			name:     "origin-connect-timeout annotation",
			defaults: nil,
			annotations: map[string]string{
				"cfgate.io/origin-connect-timeout": "5s",
			},
			wantConnectTimeout: "5s",
		},
		{
			name:     "origin-ssl-verify false enables NoTLSVerify",
			defaults: nil,
			annotations: map[string]string{
				"cfgate.io/origin-ssl-verify": "false",
			},
			wantNoTLSVerify: true,
		},
		{
			name:     "origin-ssl-verify true has no effect",
			defaults: nil,
			annotations: map[string]string{
				"cfgate.io/origin-ssl-verify": "true",
			},
			wantNil: true,
		},
		{
			name:     "origin-http-host-header annotation",
			defaults: nil,
			annotations: map[string]string{
				"cfgate.io/origin-http-host-header": "custom.host",
			},
			wantHTTPHostHeader: "custom.host",
		},
		{
			name:     "origin-server-name annotation",
			defaults: nil,
			annotations: map[string]string{
				"cfgate.io/origin-server-name": "origin.local",
			},
			wantServerName: "origin.local",
		},
		{
			name:     "origin-ca-pool annotation",
			defaults: nil,
			annotations: map[string]string{
				"cfgate.io/origin-ca-pool": "/path/to/ca",
			},
			wantCAPool: "/path/to/ca",
		},
		{
			name:     "h2c false has no effect",
			defaults: nil,
			annotations: map[string]string{
				"cfgate.io/origin-h2c": "false",
			},
			wantNil: true,
		},
		{
			name:     "http2 false has no effect",
			defaults: nil,
			annotations: map[string]string{
				"cfgate.io/origin-http2": "false",
			},
			wantNil: true,
		},
		{
			name: "annotation overrides default connect timeout",
			defaults: &cfgatev1alpha1.OriginDefaults{
				ConnectTimeout: "30s",
			},
			annotations: map[string]string{
				"cfgate.io/origin-connect-timeout": "5s",
			},
			wantConnectTimeout: "5s",
		},
		{
			name: "all defaults set simultaneously",
			defaults: &cfgatev1alpha1.OriginDefaults{
				ConnectTimeout: "30s",
				NoTLSVerify:    true,
				HTTP2Origin:    true,
			},
			annotations:        nil,
			wantConnectTimeout: "30s",
			wantNoTLSVerify:    true,
			wantHTTP2:          true,
		},
		{
			name:     "all annotations set simultaneously",
			defaults: nil,
			annotations: map[string]string{
				"cfgate.io/origin-connect-timeout":  "5s",
				"cfgate.io/origin-ssl-verify":       "false",
				"cfgate.io/origin-http-host-header": "custom.host",
				"cfgate.io/origin-server-name":      "origin.local",
				"cfgate.io/origin-ca-pool":          "/path/to/ca",
				"cfgate.io/origin-http2":            "true",
				"cfgate.io/origin-h2c":              "true",
			},
			wantConnectTimeout: "5s",
			wantNoTLSVerify:    true,
			wantHTTPHostHeader: "custom.host",
			wantServerName:     "origin.local",
			wantCAPool:         "/path/to/ca",
			wantHTTP2:          true,
			wantH2c:            true,
		},
		{
			name:     "unknown annotation ignored",
			defaults: nil,
			annotations: map[string]string{
				"cfgate.io/unknown": "value",
			},
			wantNil: true,
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
			}
			if tt.wantConnectTimeout != "" && config.ConnectTimeout != tt.wantConnectTimeout {
				t.Errorf("ConnectTimeout = %q, want %q", config.ConnectTimeout, tt.wantConnectTimeout)
			}
			if config.NoTLSVerify != tt.wantNoTLSVerify {
				t.Errorf("NoTLSVerify = %v, want %v", config.NoTLSVerify, tt.wantNoTLSVerify)
			}
			if tt.wantHTTPHostHeader != "" && config.HTTPHostHeader != tt.wantHTTPHostHeader {
				t.Errorf("HTTPHostHeader = %q, want %q", config.HTTPHostHeader, tt.wantHTTPHostHeader)
			}
			if tt.wantServerName != "" && config.OriginServerName != tt.wantServerName {
				t.Errorf("OriginServerName = %q, want %q", config.OriginServerName, tt.wantServerName)
			}
			if tt.wantCAPool != "" && config.CAPool != tt.wantCAPool {
				t.Errorf("CAPool = %q, want %q", config.CAPool, tt.wantCAPool)
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

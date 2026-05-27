package cloudflared

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloudflaredH2CContract(t *testing.T) {
	bin := os.Getenv("CLOUDFLARED_H2C_BIN")
	if bin == "" {
		t.Skip("CLOUDFLARED_H2C_BIN is not set")
	}

	tests := []struct {
		name        string
		config      *TunnelConfig
		wantFailure bool
	}{
		{
			name: "per-rule h2c http origin",
			config: cloudflaredH2CContractConfig(
				nil,
				&OriginRequestConfig{H2cOrigin: true},
				"http://app.default.svc.cluster.local:8080",
			),
		},
		{
			name: "global h2c http origin",
			config: cloudflaredH2CContractConfig(
				&OriginRequestConfig{H2cOrigin: true},
				nil,
				"http://app.default.svc.cluster.local:8080",
			),
		},
		{
			name: "per-rule h2c https origin",
			config: cloudflaredH2CContractConfig(
				nil,
				&OriginRequestConfig{H2cOrigin: true},
				"https://app.default.svc.cluster.local:8443",
			),
			wantFailure: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeCloudflaredH2CContractConfig(t, tt.config)
			cmd := exec.Command(bin, "tunnel", "ingress", "validate", "--config", path, "--json")
			output, err := cmd.CombinedOutput()
			if !tt.wantFailure {
				if err != nil {
					t.Fatalf("cloudflared validate error = %v, output:\n%s", err, output)
				}
				return
			}
			if err == nil {
				t.Fatalf("cloudflared validate succeeded, want h2c HTTPS rejection. output:\n%s", output)
			}
			lower := strings.ToLower(string(output))
			hasTransportTerm := strings.Contains(lower, "http") || strings.Contains(lower, "https") || strings.Contains(lower, "cleartext")
			if !strings.Contains(lower, "h2c") || !hasTransportTerm {
				t.Fatalf("cloudflared validate output = %q, want h2c HTTP or HTTPS conflict", output)
			}
		})
	}
}

func cloudflaredH2CContractConfig(global, perRule *OriginRequestConfig, service string) *TunnelConfig {
	return &TunnelConfig{
		TunnelID:      "00000000-0000-0000-0000-000000000000",
		OriginRequest: global,
		Ingress: []IngressRule{
			{
				Hostname:      "app.example.com",
				Service:       service,
				OriginRequest: perRule,
			},
			{Service: "http_status:404"},
		},
	}
}

func writeCloudflaredH2CContractConfig(t *testing.T, config *TunnelConfig) string {
	t.Helper()
	data, err := config.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

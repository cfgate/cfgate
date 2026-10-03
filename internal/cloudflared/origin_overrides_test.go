package cloudflared

import (
	cfg "cfgate.io/cfgate/api/v1alpha1"
	"go.yaml.in/yaml/v3"
	"strings"
	"testing"
)

func TestOriginExplicitFalseSurvivesSerialization(t *testing.T) {
	for _, field := range []struct{ annotation, wire string }{
		{"origin-ssl-verify", "noTLSVerify"}, {"origin-http2", "http2Origin"}, {"origin-h2c", "h2cOrigin"},
	} {
		t.Run(field.annotation, func(t *testing.T) {
			value := "FALSE"
			if field.annotation == "origin-ssl-verify" {
				value = "TRUE"
			}
			config := BuildOriginConfig(&cfg.OriginDefaults{NoTLSVerify: true, HTTP2Origin: true, H2cOrigin: true}, map[string]string{"cfgate.io/" + field.annotation: value})
			data, err := yaml.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), field.wire+": false") {
				t.Fatalf("explicit false lost: %s", data)
			}
		})
	}
}

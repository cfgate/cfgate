package cloudflare

import (
	"context"
	"encoding/json"
	"k8s.io/utils/ptr"
	"net/http"
	"testing"
)

func TestOriginBooleanWireInheritance(t *testing.T) {
	for _, field := range []string{"noTLSVerify", "http2Origin", "h2cOrigin"} {
		for _, global := range []bool{false, true} {
			for _, override := range []*bool{nil, ptr.To(false), ptr.To(true)} {
				globalConfig, ruleConfig := &OriginRequestConfig{}, &OriginRequestConfig{}
				switch field {
				case "noTLSVerify":
					globalConfig.NoTLSVerify = ptr.To(global)
					ruleConfig.NoTLSVerify = override
				case "http2Origin":
					globalConfig.HTTP2Origin = ptr.To(global)
					ruleConfig.HTTP2Origin = override
				case "h2cOrigin":
					globalConfig.H2cOrigin = ptr.To(global)
					ruleConfig.H2cOrigin = override
				}
				transport := &tunnelConfigCaptureTransport{}
				client, err := NewClient("test-token", WithHTTPClient(&http.Client{Transport: transport}))
				if err != nil {
					t.Fatal(err)
				}
				err = client.UpdateTunnelConfiguration(context.Background(), "account", "tunnel", TunnelConfiguration{OriginRequest: globalConfig, Ingress: []IngressRule{{Service: "http://origin", OriginRequest: ruleConfig}, {Service: "http_status:404"}}})
				if err != nil {
					t.Fatal(err)
				}
				var wire struct {
					Config struct {
						OriginRequest map[string]bool
						Ingress       []struct{ OriginRequest map[string]bool }
					}
				}
				if err = json.Unmarshal(transport.body, &wire); err != nil {
					t.Fatal(err)
				}
				value, present := wire.Config.Ingress[0].OriginRequest[field]
				if present != (override != nil) || (present && value != *override) {
					t.Fatalf("%s override %v lost: %s", field, override, transport.body)
				}
				if value, present := wire.Config.OriginRequest[field]; !present || value != global {
					t.Fatalf("global lost: %s", transport.body)
				}
			}
		}
	}
}

func TestOriginProtocolInheritanceValidation(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		rule := &OriginRequestConfig{HTTP2Origin: ptr.To(true)}
		if disabled {
			rule.H2cOrigin = ptr.To(false)
		}
		err := validateOriginRequests(TunnelConfiguration{OriginRequest: &OriginRequestConfig{H2cOrigin: ptr.To(true)}, Ingress: []IngressRule{{Service: "https://origin", OriginRequest: rule}}})
		if (err == nil) != disabled {
			t.Fatalf("explicit disable=%v error=%v", disabled, err)
		}
	}
}

package cloudflare

import (
	"k8s.io/utils/ptr"
	"testing"
)

func TestEffectiveOriginTransportMatrix(t *testing.T) {
	for _, service := range []string{"http://origin", "https://origin", "HTTPS://origin", "ws://origin", "wss://origin", "unix:/socket", "unix+tls:/socket", "hello_world", "hello-world", "http_status:503"} {
		t.Run(service, func(t *testing.T) {
			for _, globalH2C := range []bool{false, true} {
				for _, override := range []*bool{nil, ptr.To(false), ptr.To(true)} {
					defaults := &OriginRequestConfig{H2cOrigin: ptr.To(globalH2C)}
					rule := &OriginRequestConfig{H2cOrigin: override}
					h2c := ptr.Deref(override, globalH2C)
					invalid := h2c && (service == "https://origin" || service == "HTTPS://origin" || service == "wss://origin" || service == "unix+tls:/socket" || service == "hello_world" || service == "hello-world")
					err := ValidateTunnelConfiguration(TunnelConfiguration{OriginRequest: defaults, Ingress: []IngressRule{{Service: service, OriginRequest: rule}}}, ClientSettings{})
					if (err != nil) != invalid {
						t.Fatalf("global=%v override=%v error=%v", globalH2C, override, err)
					}
				}
			}
		})
	}
}

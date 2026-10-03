package cloudflare

import (
	"fmt"
	"strings"

	"k8s.io/utils/ptr"
)

// ValidateOriginTransport checks effective HTTP origin settings against the
// connector's transport constraints. Non-HTTP services do not use these settings.
func ValidateOriginTransport(service string, defaults, override *OriginRequestConfig) error {
	scheme, _, _ := strings.Cut(service, ":")
	scheme = strings.ToLower(scheme)
	if service == "hello_world" || service == "hello-world" {
		scheme = "https"
	}
	switch scheme {
	case "http", "https", "ws", "wss", "unix", "unix+tls":
	default:
		return nil
	}
	http2, h2c := false, false
	for _, config := range []*OriginRequestConfig{defaults, override} {
		if config != nil {
			http2 = ptr.Deref(config.HTTP2Origin, http2)
			h2c = ptr.Deref(config.H2cOrigin, h2c)
		}
	}
	if http2 && h2c {
		return fmt.Errorf("http2Origin and h2cOrigin are mutually exclusive; explicitly disable the inherited transport")
	}
	if h2c && (scheme == "https" || scheme == "wss" || scheme == "unix+tls") {
		return fmt.Errorf("h2cOrigin requires a cleartext origin; disable h2cOrigin for %s", scheme)
	}
	return nil
}

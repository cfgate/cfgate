package controller

import (
	"strings"

	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"

	"k8s.io/utils/ptr"
)

// knownStockCloudflared recognizes only the upstream Docker repository. Other
// images are administrator-selected capabilities, not verified h2c implementations.
func knownStockCloudflared(image string) bool {
	name, _, _ := strings.Cut(image, "@")
	if colon := strings.LastIndex(name, ":"); colon > strings.LastIndex(name, "/") {
		name = name[:colon]
	}
	switch name {
	case "cloudflare/cloudflared", "docker.io/cloudflare/cloudflared", "index.docker.io/cloudflare/cloudflared", "registry-1.docker.io/cloudflare/cloudflared":
		return true
	default:
		return false
	}
}

// applyConnectorCompatibility keeps incompatible matches in place as denials,
// including forwarding fallbacks. Returning an error would retain stale forwarding.
func applyConnectorCompatibility(tunnel *cfg.CloudflareTunnel, configuration *cloudflare.TunnelConfiguration) int {
	if !knownStockCloudflared(tunnel.Spec.Cloudflared.Image) {
		return 0
	}
	globalH2C := configuration.OriginRequest != nil && ptr.Deref(configuration.OriginRequest.H2cOrigin, false)
	if configuration.OriginRequest != nil {
		configuration.OriginRequest.H2cOrigin = nil
	}
	blocked := 0
	for i := range configuration.Ingress {
		rule := &configuration.Ingress[i]
		h2c := globalH2C
		if rule.OriginRequest != nil {
			h2c = ptr.Deref(rule.OriginRequest.H2cOrigin, globalH2C)
		}
		if rule.OriginRequest != nil {
			rule.OriginRequest.H2cOrigin = nil
		}
		service := strings.ToLower(rule.Service)
		if h2c && (strings.HasPrefix(service, "http://") || strings.HasPrefix(service, "https://") || strings.HasPrefix(service, "ws://") || strings.HasPrefix(service, "wss://") || strings.HasPrefix(service, "unix:") || strings.HasPrefix(service, "unix+tls:") || service == "hello_world" || service == "hello-world") {
			rule.Service = "http_status:503"
			rule.OriginRequest = nil
			blocked++
		}
	}
	return blocked
}

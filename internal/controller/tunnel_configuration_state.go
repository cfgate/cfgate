package controller

import (
	"cfgate.io/cfgate/internal/cloudflare"
	"crypto/sha256"
	"fmt"
	"reflect"
)

func appliedTunnelConfigHash(accountID, tunnelID string, config cloudflare.TunnelConfiguration) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(accountID+"\x00"+tunnelID+"\x00"+tunnelConfigHash(config))))
}

func equivalentTunnelConfiguration(a, b cloudflare.TunnelConfiguration) bool {
	normalize := func(config cloudflare.TunnelConfiguration) cloudflare.TunnelConfiguration {
		config.Ingress = append([]cloudflare.IngressRule(nil), config.Ingress...)
		empty := func(origin *cloudflare.OriginRequestConfig) bool {
			return origin != nil && reflect.DeepEqual(*origin, cloudflare.OriginRequestConfig{})
		}
		if empty(config.OriginRequest) {
			config.OriginRequest = nil
		}
		for i := range config.Ingress {
			if empty(config.Ingress[i].OriginRequest) {
				config.Ingress[i].OriginRequest = nil
			}
		}
		return config
	}
	return reflect.DeepEqual(normalize(a), normalize(b))
}

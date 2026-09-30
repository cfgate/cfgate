package controller

import (
	"cfgate.io/cfgate/internal/cloudflare"
	"crypto/sha256"
	"fmt"
	"reflect"
	"time"
)

func appliedTunnelConfigHash(accountID, tunnelID string, config cloudflare.TunnelConfiguration) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(accountID+"\x00"+tunnelID+"\x00"+tunnelConfigHash(config))))
}

// equivalentTunnelConfiguration compares a remote read with desired API write semantics.
func equivalentTunnelConfiguration(remote, desired cloudflare.TunnelConfiguration) bool {
	normalize := func(config cloudflare.TunnelConfiguration, desired bool) cloudflare.TunnelConfiguration {
		config.Ingress = append([]cloudflare.IngressRule(nil), config.Ingress...)
		// Cloudflare returns an explicit disabled WARP block even when omitted.
		// Enabled routing remains a meaningful difference.
		if config.WarpRouting != nil && !config.WarpRouting.Enabled {
			config.WarpRouting = nil
		}
		normalizeOrigin := func(origin *cloudflare.OriginRequestConfig) *cloudflare.OriginRequestConfig {
			if origin == nil {
				return nil
			}
			value := *origin
			if desired {
				value.ConnectTimeout = cloudflare.CanonicalOriginConnectTimeout(value.ConnectTimeout)
			}
			for _, duration := range []*string{&value.ConnectTimeout, &value.TLSTimeout, &value.TCPKeepAlive, &value.KeepAliveTimeout} {
				if parsed, err := time.ParseDuration(*duration); err == nil {
					*duration = parsed.String()
				}
			}
			if reflect.DeepEqual(value, cloudflare.OriginRequestConfig{}) {
				return nil
			}
			return &value
		}
		config.OriginRequest = normalizeOrigin(config.OriginRequest)
		for i := range config.Ingress {
			config.Ingress[i].OriginRequest = normalizeOrigin(config.Ingress[i].OriginRequest)
		}
		return config
	}
	return reflect.DeepEqual(normalize(remote, false), normalize(desired, true))
}

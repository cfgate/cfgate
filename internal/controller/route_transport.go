package controller

import (
	"fmt"

	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/cloudflared"
	"cfgate.io/cfgate/internal/controller/annotations"

	"k8s.io/utils/ptr"
	gateway "sigs.k8s.io/gateway-api/apis/v1"
)

func validateRouteTransport(route *gateway.HTTPRoute, tunnel *cfg.CloudflareTunnel) error {
	if err := validateRouteOriginCAPool(route, tunnel.Spec.OriginDefaults.CAPoolSecretRef != nil); err != nil {
		return err
	}
	defaults := cloudflaredOriginRequestToCloudflare(cloudflared.BuildOriginConfig(&tunnel.Spec.OriginDefaults, nil))
	override := cloudflaredOriginRequestToCloudflare(cloudflared.BuildOriginConfig(nil, route.Annotations))
	service := annotations.ParseOriginConfig(route, "http").Protocol + "://origin"
	if err := cloudflare.ValidateOriginTransport(service, defaults, override); err != nil {
		return err
	}
	h2c := tunnel.Spec.OriginDefaults.H2cOrigin
	if override != nil {
		h2c = ptr.Deref(override.H2cOrigin, h2c)
	}
	if h2c && knownStockCloudflared(tunnel.Spec.Cloudflared.Image) {
		return fmt.Errorf("stock cloudflared does not support h2cOrigin")
	}
	return nil
}

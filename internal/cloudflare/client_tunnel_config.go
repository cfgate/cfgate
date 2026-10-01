package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	cf "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/zero_trust"
)

// GetTunnelConfiguration retains SDK-unknown origin fields such as h2cOrigin from the raw response.
func (c *clientImpl) GetTunnelConfiguration(ctx context.Context, accountID, tunnelID string) (*TunnelConfiguration, error) {
	ctx, cancel := context.WithTimeout(ctx, apiOperationTimeout)
	defer cancel()
	response, err := c.api.ZeroTrust.Tunnels.Cloudflared.Configurations.Get(ctx, tunnelID, zero_trust.TunnelCloudflaredConfigurationGetParams{AccountID: cf.F(accountID)})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get tunnel configuration: %w", err)
	}
	if response == nil {
		return nil, fmt.Errorf("get tunnel configuration: empty response")
	}
	// Newly created remote tunnels explicitly return config:null until the first PUT.
	// A missing or malformed field must still fail remote verification.
	if response.JSON.Config.IsNull() && !response.JSON.Config.IsMissing() {
		return nil, nil
	}
	var config TunnelConfiguration
	if err := json.Unmarshal([]byte(response.Config.JSON.RawJSON()), &config); err != nil {
		return nil, fmt.Errorf("decode tunnel configuration: %w", err)
	}
	return &config, nil
}

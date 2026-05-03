package cloudflare

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"

	cf "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/accounts"
	"github.com/cloudflare/cloudflare-go/v6/dns"
	"github.com/cloudflare/cloudflare-go/v6/option"
	"github.com/cloudflare/cloudflare-go/v6/zero_trust"
	"github.com/cloudflare/cloudflare-go/v6/zones"
)

// ClientOption is a functional option for configuring the client.
type ClientOption func(*clientOptions)

// clientOptions holds configuration for creating a client.
type clientOptions struct {
	httpClient *http.Client
}

// WithHTTPClient sets a custom HTTP client for the Cloudflare API.
// Useful for testing or custom transport configuration.
func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(opts *clientOptions) {
		opts.httpClient = httpClient
	}
}

// clientImpl implements the Client interface using cloudflare-go v6 SDK.
type clientImpl struct {
	api *cf.Client
}

// NewClient creates a new Cloudflare client with the given API token.
func NewClient(apiToken string, opts ...ClientOption) (Client, error) {
	if apiToken == "" {
		return nil, errors.New("API token is required")
	}

	// Apply functional options
	clientOpts := &clientOptions{}
	for _, opt := range opts {
		opt(clientOpts)
	}

	// Build cloudflare-go options
	cfOpts := []option.RequestOption{
		option.WithAPIToken(apiToken),
	}
	if clientOpts.httpClient != nil {
		cfOpts = append(cfOpts, option.WithHTTPClient(clientOpts.httpClient))
	}

	api := cf.NewClient(cfOpts...)

	c := &clientImpl{
		api: api,
	}

	return c, nil
}

// isNotFound reports whether err is a Cloudflare API 404 response.
func isNotFound(err error) bool {
	var apiErr *cf.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// hasErrorCode reports whether err is a Cloudflare API error containing the given error code.
func hasErrorCode(err error, code int64) bool {
	var apiErr *cf.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, e := range apiErr.Errors {
		if e.Code == code {
			return true
		}
	}
	return false
}

// GetTunnel retrieves a tunnel by ID.
func (c *clientImpl) GetTunnel(ctx context.Context, accountID, tunnelID string) (*Tunnel, error) {
	tunnel, err := c.api.ZeroTrust.Tunnels.Cloudflared.Get(ctx, tunnelID, zero_trust.TunnelCloudflaredGetParams{
		AccountID: cf.F(accountID),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get tunnel: %w", err)
	}

	return tunnelFromAPI(tunnel), nil
}

// GetTunnelByName retrieves a tunnel by name.
// Returns nil if the tunnel does not exist.
func (c *clientImpl) GetTunnelByName(ctx context.Context, accountID, name string) (*Tunnel, error) {
	tunnels, err := c.api.ZeroTrust.Tunnels.Cloudflared.List(ctx, zero_trust.TunnelCloudflaredListParams{
		AccountID: cf.F(accountID),
		Name:      cf.F(name),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list tunnels: %w", err)
	}

	for _, tunnel := range tunnels.Result {
		if tunnel.Name == name && string(tunnel.Status) != "deleted" {
			return &Tunnel{
				ID:         tunnel.ID,
				Name:       tunnel.Name,
				Status:     string(tunnel.Status),
				AccountTag: accountID,
			}, nil
		}
	}

	return nil, nil // Not found
}

// CreateTunnel creates a new tunnel with the given name.
// Uses config_src: "cloudflare" for remote management.
func (c *clientImpl) CreateTunnel(ctx context.Context, accountID string, params CreateTunnelParams) (*Tunnel, error) {
	tunnelSecret, err := generateTunnelSecret()
	if err != nil {
		return nil, err
	}

	configSrc := zero_trust.TunnelCloudflaredNewParamsConfigSrcCloudflare
	if params.ConfigSrc == "local" {
		configSrc = zero_trust.TunnelCloudflaredNewParamsConfigSrcLocal
	}

	tunnel, err := c.api.ZeroTrust.Tunnels.Cloudflared.New(ctx, zero_trust.TunnelCloudflaredNewParams{
		AccountID:    cf.F(accountID),
		Name:         cf.F(params.Name),
		TunnelSecret: cf.F(tunnelSecret),
		ConfigSrc:    cf.F(configSrc),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create tunnel: %w", err)
	}

	return tunnelFromAPI(tunnel), nil
}

// DeleteTunnel deletes a tunnel by ID.
// Requires all connections to be deleted first.
func (c *clientImpl) DeleteTunnel(ctx context.Context, accountID, tunnelID string) error {
	_, err := c.api.ZeroTrust.Tunnels.Cloudflared.Delete(ctx, tunnelID, zero_trust.TunnelCloudflaredDeleteParams{
		AccountID: cf.F(accountID),
	})
	if err != nil {
		if isNotFound(err) {
			return nil // Already deleted
		}
		return fmt.Errorf("failed to delete tunnel: %w", err)
	}

	return nil
}

// DeleteTunnelConnections deletes all active connections for a tunnel.
// Must be called before DeleteTunnel.
func (c *clientImpl) DeleteTunnelConnections(ctx context.Context, accountID, tunnelID string) error {
	_, err := c.api.ZeroTrust.Tunnels.Cloudflared.Connections.Delete(ctx, tunnelID, zero_trust.TunnelCloudflaredConnectionDeleteParams{
		AccountID: cf.F(accountID),
	})
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to delete tunnel connections: %w", err)
	}

	return nil
}

// GetTunnelToken retrieves the tunnel token for cloudflared authentication.
func (c *clientImpl) GetTunnelToken(ctx context.Context, accountID, tunnelID string) (string, error) {
	tokenPtr, err := c.api.ZeroTrust.Tunnels.Cloudflared.Token.Get(ctx, tunnelID, zero_trust.TunnelCloudflaredTokenGetParams{
		AccountID: cf.F(accountID),
	})
	if err != nil {
		return "", fmt.Errorf("failed to get tunnel token: %w", err)
	}

	if tokenPtr == nil {
		return "", errors.New("token is nil")
	}

	return *tokenPtr, nil
}

// UpdateTunnelConfiguration updates the tunnel's ingress configuration.
// This is an atomic replacement of the entire configuration.
func (c *clientImpl) UpdateTunnelConfiguration(ctx context.Context, accountID, tunnelID string, config TunnelConfiguration) error {
	if err := validateOriginRequests(config); err != nil {
		return err
	}

	// Convert our config to cloudflare-go config
	ingressRules := make([]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, len(config.Ingress))
	for i, rule := range config.Ingress {
		ingressRules[i] = zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
			Hostname: cf.F(rule.Hostname),
			Path:     cf.F(rule.Path),
			Service:  cf.F(rule.Service),
		}

		if rule.OriginRequest != nil {
			ingressRules[i].OriginRequest = cf.F(ingressOriginRequestToAPI(rule.OriginRequest))
		}
	}

	cfConfig := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfig{
		Ingress: cf.F(ingressRules),
	}

	if config.OriginRequest != nil {
		cfConfig.OriginRequest = cf.F(globalOriginRequestToAPI(config.OriginRequest))
	}

	// Collect extra options for h2cOrigin, which is not in the SDK schema.
	// The CF API stores the full config JSON — cloudflared parses it directly,
	// so undocumented fields are preserved and returned.
	var opts []option.RequestOption
	if config.OriginRequest != nil && config.OriginRequest.H2cOrigin {
		opts = append(opts, option.WithJSONSet("config.originRequest.h2cOrigin", true))
	}
	for i, rule := range config.Ingress {
		if rule.OriginRequest != nil && rule.OriginRequest.H2cOrigin {
			opts = append(opts, option.WithJSONSet(fmt.Sprintf("config.ingress.%d.originRequest.h2cOrigin", i), true))
		}
	}

	_, err := c.api.ZeroTrust.Tunnels.Cloudflared.Configurations.Update(ctx, tunnelID, zero_trust.TunnelCloudflaredConfigurationUpdateParams{
		AccountID: cf.F(accountID),
		Config:    cf.F(cfConfig),
	}, opts...)
	if err != nil {
		return fmt.Errorf("failed to update tunnel configuration: %w", err)
	}

	return nil
}

// ListDNSRecords lists all DNS records in a zone.
func (c *clientImpl) ListDNSRecords(ctx context.Context, zoneID string) ([]DNSRecord, error) {
	var records []DNSRecord

	page := c.api.DNS.Records.ListAutoPaging(ctx, dns.RecordListParams{
		ZoneID: cf.F(zoneID),
	})

	for page.Next() {
		records = append(records, dnsRecordFromSDK(page.Current(), zoneID))
	}

	if err := page.Err(); err != nil {
		return nil, fmt.Errorf("failed to list DNS records: %w", err)
	}

	return records, nil
}

// ListDNSRecordsByNameType lists DNS records filtered by exact name and record type.
func (c *clientImpl) ListDNSRecordsByNameType(ctx context.Context, zoneID, name, recordType string) ([]DNSRecord, error) {
	var records []DNSRecord

	params := dns.RecordListParams{
		ZoneID: cf.F(zoneID),
		Name:   cf.F(dns.RecordListParamsName{Exact: cf.F(name)}),
		Type:   cf.F(dns.RecordListParamsType(recordType)),
	}

	page := c.api.DNS.Records.ListAutoPaging(ctx, params)

	for page.Next() {
		records = append(records, dnsRecordFromSDK(page.Current(), zoneID))
	}

	if err := page.Err(); err != nil {
		return nil, fmt.Errorf("failed to list DNS records by name and type: %w", err)
	}

	return records, nil
}

// CreateDNSRecord creates a new DNS record.
func (c *clientImpl) CreateDNSRecord(ctx context.Context, zoneID string, record DNSRecord) (*DNSRecord, error) {
	var result *dns.RecordResponse
	var err error

	switch record.Type {
	case "CNAME":
		result, err = c.api.DNS.Records.New(ctx, dns.RecordNewParams{
			ZoneID: cf.F(zoneID),
			Body: dns.CNAMERecordParam{
				Name:    cf.F(record.Name),
				Type:    cf.F(dns.CNAMERecordTypeCNAME),
				Content: cf.F(record.Content),
				TTL:     cf.F(dns.TTL(record.TTL)),
				Proxied: cf.F(record.Proxied),
				Comment: cf.F(record.Comment),
			},
		})
	case "TXT":
		result, err = c.api.DNS.Records.New(ctx, dns.RecordNewParams{
			ZoneID: cf.F(zoneID),
			Body: dns.TXTRecordParam{
				Name:    cf.F(record.Name),
				Type:    cf.F(dns.TXTRecordTypeTXT),
				Content: cf.F(record.Content),
				TTL:     cf.F(dns.TTL(record.TTL)),
				Comment: cf.F(record.Comment),
			},
		})
	case "A":
		result, err = c.api.DNS.Records.New(ctx, dns.RecordNewParams{
			ZoneID: cf.F(zoneID),
			Body: dns.ARecordParam{
				Name:    cf.F(record.Name),
				Type:    cf.F(dns.ARecordTypeA),
				Content: cf.F(record.Content),
				TTL:     cf.F(dns.TTL(record.TTL)),
				Proxied: cf.F(record.Proxied),
				Comment: cf.F(record.Comment),
			},
		})
	case "AAAA":
		result, err = c.api.DNS.Records.New(ctx, dns.RecordNewParams{
			ZoneID: cf.F(zoneID),
			Body: dns.AAAARecordParam{
				Name:    cf.F(record.Name),
				Type:    cf.F(dns.AAAARecordTypeAAAA),
				Content: cf.F(record.Content),
				TTL:     cf.F(dns.TTL(record.TTL)),
				Proxied: cf.F(record.Proxied),
				Comment: cf.F(record.Comment),
			},
		})
	default:
		return nil, fmt.Errorf("unsupported record type: %s", record.Type)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create DNS record: %w", err)
	}

	rec := dnsRecordFromSDK(*result, zoneID)
	return &rec, nil
}

// UpdateDNSRecord updates an existing DNS record.
func (c *clientImpl) UpdateDNSRecord(ctx context.Context, zoneID, recordID string, record DNSRecord) (*DNSRecord, error) {
	var result *dns.RecordResponse
	var err error

	switch record.Type {
	case "CNAME":
		result, err = c.api.DNS.Records.Update(ctx, recordID, dns.RecordUpdateParams{
			ZoneID: cf.F(zoneID),
			Body: dns.CNAMERecordParam{
				Name:    cf.F(record.Name),
				Type:    cf.F(dns.CNAMERecordTypeCNAME),
				Content: cf.F(record.Content),
				TTL:     cf.F(dns.TTL(record.TTL)),
				Proxied: cf.F(record.Proxied),
				Comment: cf.F(record.Comment),
			},
		})
	case "TXT":
		result, err = c.api.DNS.Records.Update(ctx, recordID, dns.RecordUpdateParams{
			ZoneID: cf.F(zoneID),
			Body: dns.TXTRecordParam{
				Name:    cf.F(record.Name),
				Type:    cf.F(dns.TXTRecordTypeTXT),
				Content: cf.F(record.Content),
				TTL:     cf.F(dns.TTL(record.TTL)),
				Comment: cf.F(record.Comment),
			},
		})
	case "A":
		result, err = c.api.DNS.Records.Update(ctx, recordID, dns.RecordUpdateParams{
			ZoneID: cf.F(zoneID),
			Body: dns.ARecordParam{
				Name:    cf.F(record.Name),
				Type:    cf.F(dns.ARecordTypeA),
				Content: cf.F(record.Content),
				TTL:     cf.F(dns.TTL(record.TTL)),
				Proxied: cf.F(record.Proxied),
				Comment: cf.F(record.Comment),
			},
		})
	case "AAAA":
		result, err = c.api.DNS.Records.Update(ctx, recordID, dns.RecordUpdateParams{
			ZoneID: cf.F(zoneID),
			Body: dns.AAAARecordParam{
				Name:    cf.F(record.Name),
				Type:    cf.F(dns.AAAARecordTypeAAAA),
				Content: cf.F(record.Content),
				TTL:     cf.F(dns.TTL(record.TTL)),
				Proxied: cf.F(record.Proxied),
				Comment: cf.F(record.Comment),
			},
		})
	default:
		return nil, fmt.Errorf("unsupported record type: %s", record.Type)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to update DNS record: %w", err)
	}

	rec := dnsRecordFromSDK(*result, zoneID)
	return &rec, nil
}

// DeleteDNSRecord deletes a DNS record.
func (c *clientImpl) DeleteDNSRecord(ctx context.Context, zoneID, recordID string) error {
	_, err := c.api.DNS.Records.Delete(ctx, recordID, dns.RecordDeleteParams{
		ZoneID: cf.F(zoneID),
	})
	if err != nil {
		if isNotFound(err) {
			return nil // Already deleted
		}
		return fmt.Errorf("failed to delete DNS record: %w", err)
	}

	return nil
}

// ListZones lists all zones accessible with the current credentials.
func (c *clientImpl) ListZones(ctx context.Context) ([]Zone, error) {
	var zoneList []Zone

	page := c.api.Zones.ListAutoPaging(ctx, zones.ZoneListParams{})

	for page.Next() {
		zone := page.Current()
		zoneList = append(zoneList, Zone{
			ID:        zone.ID,
			Name:      zone.Name,
			Status:    string(zone.Status),
			AccountID: zone.Account.ID,
		})
	}

	if err := page.Err(); err != nil {
		return nil, fmt.Errorf("failed to list zones: %w", err)
	}

	return zoneList, nil
}

// GetZoneByName retrieves a zone by domain name.
// Returns nil if the zone does not exist.
func (c *clientImpl) GetZoneByName(ctx context.Context, name string) (*Zone, error) {
	zoneList, err := c.api.Zones.List(ctx, zones.ZoneListParams{
		Name: cf.F(name),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list zones: %w", err)
	}

	for _, zone := range zoneList.Result {
		if zone.Name == name {
			return &Zone{
				ID:        zone.ID,
				Name:      zone.Name,
				Status:    string(zone.Status),
				AccountID: zone.Account.ID,
			}, nil
		}
	}

	return nil, nil // Not found
}

// ValidateToken verifies the API token by attempting actual operations.
// Uses operational validation (ListZones + ListTunnels) to work with both
// User API Tokens and Account API Tokens.
// Returns an error if the token is invalid or missing permissions.
func (c *clientImpl) ValidateToken(ctx context.Context, accountID string) error {
	// Validate zone access (required for DNS operations)
	zones, err := c.ListZones(ctx)
	if err != nil {
		return fmt.Errorf("token validation failed (zone access): %w", err)
	}
	if len(zones) == 0 {
		return fmt.Errorf("token has no zone access - DNS operations will fail")
	}

	// Validate tunnel access (required for tunnel operations)
	_, err = c.api.ZeroTrust.Tunnels.Cloudflared.List(ctx, zero_trust.TunnelCloudflaredListParams{
		AccountID: cf.F(accountID),
	})
	if err != nil {
		return fmt.Errorf("token validation failed (tunnel access): %w", err)
	}

	return nil
}

// ListAccounts lists all accounts accessible with the current credentials.
// Uses direct List() instead of ListAutoPaging() to avoid SDK pagination bug
// (cloudflare-python#2584) where has_next_page() incorrectly returns true
// with account-scoped tokens, causing infinite loops.
func (c *clientImpl) ListAccounts(ctx context.Context) ([]Account, error) {
	resp, err := c.api.Accounts.List(ctx, accounts.AccountListParams{
		PerPage: cf.F(float64(50)), // Most users have < 50 accounts
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list accounts: %w", err)
	}

	var result []Account
	for _, acc := range resp.Result {
		result = append(result, Account{
			ID:   acc.ID,
			Name: acc.Name,
		})
	}

	return result, nil
}

// GetAccountByName retrieves an account by name.
// Returns nil if the account does not exist.
func (c *clientImpl) GetAccountByName(ctx context.Context, name string) (*Account, error) {
	accounts, err := c.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}

	for _, acc := range accounts {
		if acc.Name == name {
			return &acc, nil
		}
	}

	return nil, nil
}

// validateOriginRequests checks that no origin config sets both HTTP2Origin and
// H2cOrigin. CRD validation prevents this, but the client layer should not
// depend on admission control alone.
func validateOriginRequests(config TunnelConfiguration) error {
	if config.OriginRequest != nil && config.OriginRequest.HTTP2Origin && config.OriginRequest.H2cOrigin {
		return errors.New("http2Origin and h2cOrigin are mutually exclusive in global origin defaults")
	}
	for i, rule := range config.Ingress {
		if rule.OriginRequest != nil && rule.OriginRequest.HTTP2Origin && rule.OriginRequest.H2cOrigin {
			return fmt.Errorf("http2Origin and h2cOrigin are mutually exclusive in ingress rule %d (%s)", i, rule.Hostname)
		}
	}
	return nil
}

// generateTunnelSecret generates a cryptographically random tunnel secret.
// Returns 32 random bytes encoded as base64, as required by the Cloudflare API
// for cloudflared connector authentication.
func generateTunnelSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate tunnel secret: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// =============================================================================
// Access Application operations
// =============================================================================

// CreateAccessApplication creates a new Access application.
func (c *clientImpl) CreateAccessApplication(ctx context.Context, accountID string, params ApplicationParams) (*AccessApplication, error) {
	appType := zero_trust.ApplicationType(params.Type)
	if params.Type == "" {
		appType = zero_trust.ApplicationTypeSelfHosted
	}

	sessionDuration := params.SessionDuration
	if sessionDuration == "" {
		sessionDuration = "24h"
	}

	httpOnly := true
	if params.HttpOnlyCookieAttribute != nil {
		httpOnly = *params.HttpOnlyCookieAttribute
	}

	sameSite := params.SameSiteCookieAttribute
	if sameSite == "" {
		sameSite = "lax"
	}

	body := zero_trust.AccessApplicationNewParamsBodySelfHostedApplication{
		Domain:                      cf.F(params.Domain),
		Type:                        cf.F(appType),
		Name:                        cf.F(params.Name),
		SessionDuration:             cf.F(sessionDuration),
		AllowedIdPs:                 cf.F(params.AllowedIdps),
		AutoRedirectToIdentity:      cf.F(params.AutoRedirectToIdentity),
		EnableBindingCookie:         cf.F(params.EnableBindingCookie),
		HTTPOnlyCookieAttribute:     cf.F(httpOnly),
		SameSiteCookieAttribute:     cf.F(sameSite),
		SkipInterstitial:            cf.F(params.SkipInterstitial),
		LogoURL:                     cf.F(params.LogoURL),
		AppLauncherVisible:          cf.F(params.AppLauncherVisible),
		CustomDenyMessage:           cf.F(params.CustomDenyMessage),
		CustomDenyURL:               cf.F(params.CustomDenyURL),
		OptionsPreflightBypass:      cf.F(params.OptionsPreflightBypass),
		PathCookieAttribute:         cf.F(params.PathCookieAttribute),
		ServiceAuth401Redirect:      cf.F(params.ServiceAuth401Redirect),
		CustomNonIdentityDenyURL:    cf.F(params.CustomNonIdentityDenyURL),
		ReadServiceTokensFromHeader: cf.F(params.ReadServiceTokensFromHeader),
		Destinations:                cf.F(accessApplicationNewDestinations(params)),
		Policies:                    cf.F(accessApplicationNewPolicyLinks(params.Policies)),
		Tags:                        cf.F(params.Tags),
	}
	if params.CORSHeaders != nil {
		body.CORSHeaders = cf.F(corsHeadersToSDK(params.CORSHeaders))
	}

	result, err := c.api.ZeroTrust.Access.Applications.New(ctx, zero_trust.AccessApplicationNewParams{
		AccountID: cf.F(accountID),
		Body:      body,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create access application: %w", err)
	}

	return applicationFromNewResponse(result), nil
}

// GetAccessApplication retrieves an Access application by ID.
func (c *clientImpl) GetAccessApplication(ctx context.Context, accountID, appID string) (*AccessApplication, error) {
	result, err := c.api.ZeroTrust.Access.Applications.Get(ctx, appID, zero_trust.AccessApplicationGetParams{
		AccountID: cf.F(accountID),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get access application: %w", err)
	}

	return applicationFromGetResponse(result), nil
}

// UpdateAccessApplication updates an existing Access application.
func (c *clientImpl) UpdateAccessApplication(ctx context.Context, accountID, appID string, params ApplicationParams) (*AccessApplication, error) {
	httpOnly := true
	if params.HttpOnlyCookieAttribute != nil {
		httpOnly = *params.HttpOnlyCookieAttribute
	}

	sameSite := params.SameSiteCookieAttribute
	if sameSite == "" {
		sameSite = "lax"
	}

	appType := zero_trust.ApplicationType(params.Type)
	if params.Type == "" {
		appType = zero_trust.ApplicationTypeSelfHosted
	}

	body := zero_trust.AccessApplicationUpdateParamsBodySelfHostedApplication{
		Domain:                      cf.F(params.Domain),
		Type:                        cf.F(appType),
		Name:                        cf.F(params.Name),
		SessionDuration:             cf.F(params.SessionDuration),
		AllowedIdPs:                 cf.F(params.AllowedIdps),
		AutoRedirectToIdentity:      cf.F(params.AutoRedirectToIdentity),
		EnableBindingCookie:         cf.F(params.EnableBindingCookie),
		HTTPOnlyCookieAttribute:     cf.F(httpOnly),
		SameSiteCookieAttribute:     cf.F(sameSite),
		SkipInterstitial:            cf.F(params.SkipInterstitial),
		LogoURL:                     cf.F(params.LogoURL),
		AppLauncherVisible:          cf.F(params.AppLauncherVisible),
		CustomDenyMessage:           cf.F(params.CustomDenyMessage),
		CustomDenyURL:               cf.F(params.CustomDenyURL),
		OptionsPreflightBypass:      cf.F(params.OptionsPreflightBypass),
		PathCookieAttribute:         cf.F(params.PathCookieAttribute),
		ServiceAuth401Redirect:      cf.F(params.ServiceAuth401Redirect),
		CustomNonIdentityDenyURL:    cf.F(params.CustomNonIdentityDenyURL),
		ReadServiceTokensFromHeader: cf.F(params.ReadServiceTokensFromHeader),
		Destinations:                cf.F(accessApplicationUpdateDestinations(params)),
		Policies:                    cf.F(accessApplicationUpdatePolicyLinks(params.Policies)),
		Tags:                        cf.F(params.Tags),
	}
	if params.CORSHeaders != nil {
		body.CORSHeaders = cf.F(corsHeadersToSDK(params.CORSHeaders))
	}

	result, err := c.api.ZeroTrust.Access.Applications.Update(ctx, appID, zero_trust.AccessApplicationUpdateParams{
		AccountID: cf.F(accountID),
		Body:      body,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update access application: %w", err)
	}

	return applicationFromUpdateResponse(result), nil
}

func accessApplicationDestinationURIs(params ApplicationParams) []string {
	destinations := params.Destinations
	if len(destinations) == 0 && params.Domain != "" {
		destinations = []string{params.Domain}
	}
	return destinations
}

func accessApplicationNewDestinations(params ApplicationParams) []zero_trust.AccessApplicationNewParamsBodySelfHostedApplicationDestinationUnion {
	uris := accessApplicationDestinationURIs(params)
	result := make([]zero_trust.AccessApplicationNewParamsBodySelfHostedApplicationDestinationUnion, 0, len(uris))
	for _, uri := range uris {
		result = append(result, zero_trust.AccessApplicationNewParamsBodySelfHostedApplicationDestinationsPublicDestination{
			Type: cf.F(zero_trust.AccessApplicationNewParamsBodySelfHostedApplicationDestinationsPublicDestinationTypePublic),
			URI:  cf.F(uri),
		})
	}
	return result
}

func accessApplicationUpdateDestinations(params ApplicationParams) []zero_trust.AccessApplicationUpdateParamsBodySelfHostedApplicationDestinationUnion {
	uris := accessApplicationDestinationURIs(params)
	result := make([]zero_trust.AccessApplicationUpdateParamsBodySelfHostedApplicationDestinationUnion, 0, len(uris))
	for _, uri := range uris {
		result = append(result, zero_trust.AccessApplicationUpdateParamsBodySelfHostedApplicationDestinationsPublicDestination{
			Type: cf.F(zero_trust.AccessApplicationUpdateParamsBodySelfHostedApplicationDestinationsPublicDestinationTypePublic),
			URI:  cf.F(uri),
		})
	}
	return result
}

func accessApplicationNewPolicyLinks(links []ApplicationPolicyLink) []zero_trust.AccessApplicationNewParamsBodySelfHostedApplicationPolicyUnion {
	result := make([]zero_trust.AccessApplicationNewParamsBodySelfHostedApplicationPolicyUnion, 0, len(links))
	for _, link := range links {
		result = append(result, zero_trust.AccessApplicationNewParamsBodySelfHostedApplicationPoliciesAccessAppPolicyLink{
			ID:         cf.F(link.ID),
			Precedence: cf.F(int64(link.Precedence)),
		})
	}
	return result
}

func accessApplicationUpdatePolicyLinks(links []ApplicationPolicyLink) []zero_trust.AccessApplicationUpdateParamsBodySelfHostedApplicationPolicyUnion {
	result := make([]zero_trust.AccessApplicationUpdateParamsBodySelfHostedApplicationPolicyUnion, 0, len(links))
	for _, link := range links {
		result = append(result, zero_trust.AccessApplicationUpdateParamsBodySelfHostedApplicationPoliciesAccessAppPolicyLink{
			ID:         cf.F(link.ID),
			Precedence: cf.F(int64(link.Precedence)),
		})
	}
	return result
}

// DeleteAccessApplication deletes an Access application.
func (c *clientImpl) DeleteAccessApplication(ctx context.Context, accountID, appID string) error {
	_, err := c.api.ZeroTrust.Access.Applications.Delete(ctx, appID, zero_trust.AccessApplicationDeleteParams{
		AccountID: cf.F(accountID),
	})
	if err != nil {
		if isNotFound(err) {
			return nil // Already deleted
		}
		return fmt.Errorf("failed to delete access application: %w", err)
	}

	return nil
}

// ListAccessApplications lists all Access applications.
func (c *clientImpl) ListAccessApplications(ctx context.Context, accountID string) ([]AccessApplication, error) {
	var apps []AccessApplication

	page := c.api.ZeroTrust.Access.Applications.ListAutoPaging(ctx, zero_trust.AccessApplicationListParams{
		AccountID: cf.F(accountID),
	})

	for page.Next() {
		app := page.Current()
		apps = append(apps, *applicationFromListResponse(&app))
	}

	if err := page.Err(); err != nil {
		return nil, fmt.Errorf("failed to list access applications: %w", err)
	}

	return apps, nil
}

// CreateAccessTag creates a new Access tag.
func (c *clientImpl) CreateAccessTag(ctx context.Context, accountID, tagName string) (*AccessTag, error) {
	result, err := c.api.ZeroTrust.Access.Tags.New(ctx, zero_trust.AccessTagNewParams{
		AccountID: cf.F(accountID),
		Name:      cf.F(tagName),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create access tag: %w", err)
	}
	return &AccessTag{Name: result.Name}, nil
}

// ListAccessTags lists all Access tags.
func (c *clientImpl) ListAccessTags(ctx context.Context, accountID string) ([]AccessTag, error) {
	var tags []AccessTag

	page := c.api.ZeroTrust.Access.Tags.ListAutoPaging(ctx, zero_trust.AccessTagListParams{
		AccountID: cf.F(accountID),
	})
	for page.Next() {
		tag := page.Current()
		tags = append(tags, AccessTag{Name: tag.Name})
	}
	if err := page.Err(); err != nil {
		return nil, fmt.Errorf("failed to list access tags: %w", err)
	}
	return tags, nil
}

// DeleteAccessTag deletes an Access tag.
func (c *clientImpl) DeleteAccessTag(ctx context.Context, accountID, tagName string) error {
	_, err := c.api.ZeroTrust.Access.Tags.Delete(ctx, tagName, zero_trust.AccessTagDeleteParams{
		AccountID: cf.F(accountID),
	})
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to delete access tag: %w", err)
	}
	return nil
}

// =============================================================================
// Reusable Access Policy operations
// =============================================================================

// CreateAccessPolicy creates a new reusable Access policy.
func (c *clientImpl) CreateAccessPolicy(ctx context.Context, accountID string, params PolicyParams) (*AccessPolicy, error) {
	result, err := c.api.ZeroTrust.Access.Policies.New(ctx, zero_trust.AccessPolicyNewParams{
		AccountID:                    cf.F(accountID),
		Name:                         cf.F(params.Name),
		Decision:                     cf.F(zero_trust.Decision(params.Decision)),
		Include:                      cf.F(accessRulesToAPI(params.Include)),
		Exclude:                      cf.F(accessRulesToAPI(params.Exclude)),
		Require:                      cf.F(accessRulesToAPI(params.Require)),
		SessionDuration:              cf.F(params.SessionDuration),
		PurposeJustificationRequired: cf.F(params.PurposeJustificationRequired),
		PurposeJustificationPrompt:   cf.F(params.PurposeJustificationPrompt),
		ApprovalRequired:             cf.F(params.ApprovalRequired),
		ApprovalGroups:               cf.F(approvalGroupsToAPI(params.ApprovalGroups)),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create access policy: %w", err)
	}

	return policyFromNewResponse(result), nil
}

// GetAccessPolicy retrieves an Access policy by ID.
func (c *clientImpl) GetAccessPolicy(ctx context.Context, accountID, policyID string) (*AccessPolicy, error) {
	result, err := c.api.ZeroTrust.Access.Policies.Get(ctx, policyID, zero_trust.AccessPolicyGetParams{
		AccountID: cf.F(accountID),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get access policy: %w", err)
	}

	return policyFromGetResponse(result), nil
}

// UpdateAccessPolicy updates an existing Access policy.
func (c *clientImpl) UpdateAccessPolicy(ctx context.Context, accountID, policyID string, params PolicyParams) (*AccessPolicy, error) {
	result, err := c.api.ZeroTrust.Access.Policies.Update(ctx, policyID, zero_trust.AccessPolicyUpdateParams{
		AccountID:                    cf.F(accountID),
		Name:                         cf.F(params.Name),
		Decision:                     cf.F(zero_trust.Decision(params.Decision)),
		Include:                      cf.F(accessRulesToAPI(params.Include)),
		Exclude:                      cf.F(accessRulesToAPI(params.Exclude)),
		Require:                      cf.F(accessRulesToAPI(params.Require)),
		SessionDuration:              cf.F(params.SessionDuration),
		PurposeJustificationRequired: cf.F(params.PurposeJustificationRequired),
		PurposeJustificationPrompt:   cf.F(params.PurposeJustificationPrompt),
		ApprovalRequired:             cf.F(params.ApprovalRequired),
		ApprovalGroups:               cf.F(approvalGroupsToAPI(params.ApprovalGroups)),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update access policy: %w", err)
	}

	return policyFromUpdateResponse(result), nil
}

// DeleteAccessPolicy deletes an Access policy.
func (c *clientImpl) DeleteAccessPolicy(ctx context.Context, accountID, policyID string) error {
	_, err := c.api.ZeroTrust.Access.Policies.Delete(ctx, policyID, zero_trust.AccessPolicyDeleteParams{
		AccountID: cf.F(accountID),
	})
	if err != nil {
		if isNotFound(err) {
			return nil // Already deleted
		}
		return fmt.Errorf("failed to delete access policy: %w", err)
	}

	return nil
}

// ListAccessPolicies lists all reusable Access policies.
func (c *clientImpl) ListAccessPolicies(ctx context.Context, accountID string) ([]AccessPolicy, error) {
	var policies []AccessPolicy

	page := c.api.ZeroTrust.Access.Policies.ListAutoPaging(ctx, zero_trust.AccessPolicyListParams{
		AccountID: cf.F(accountID),
	})

	for page.Next() {
		policy := page.Current()
		policies = append(policies, *policyFromListResponse(&policy))
	}

	if err := page.Err(); err != nil {
		return nil, fmt.Errorf("failed to list access policies: %w", err)
	}

	return policies, nil
}

// =============================================================================
// Access Group operations
// =============================================================================

// CreateAccessGroup creates a new Access group.
func (c *clientImpl) CreateAccessGroup(ctx context.Context, accountID string, params GroupParams) (*AccessGroup, error) {
	result, err := c.api.ZeroTrust.Access.Groups.New(ctx, zero_trust.AccessGroupNewParams{
		AccountID: cf.F(accountID),
		Name:      cf.F(params.Name),
		Include:   cf.F(accessRulesToAPI(params.Include)),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create access group: %w", err)
	}

	return groupFromNewResponse(result), nil
}

// GetAccessGroup retrieves an Access group by ID.
func (c *clientImpl) GetAccessGroup(ctx context.Context, accountID, groupID string) (*AccessGroup, error) {
	result, err := c.api.ZeroTrust.Access.Groups.Get(ctx, groupID, zero_trust.AccessGroupGetParams{
		AccountID: cf.F(accountID),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get access group: %w", err)
	}

	return groupFromGetResponse(result), nil
}

// UpdateAccessGroup updates an existing Access group.
func (c *clientImpl) UpdateAccessGroup(ctx context.Context, accountID, groupID string, params GroupParams) (*AccessGroup, error) {
	result, err := c.api.ZeroTrust.Access.Groups.Update(ctx, groupID, zero_trust.AccessGroupUpdateParams{
		AccountID: cf.F(accountID),
		Name:      cf.F(params.Name),
		Include:   cf.F(accessRulesToAPI(params.Include)),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update access group: %w", err)
	}

	return groupFromUpdateResponse(result), nil
}

// DeleteAccessGroup deletes an Access group.
func (c *clientImpl) DeleteAccessGroup(ctx context.Context, accountID, groupID string) error {
	_, err := c.api.ZeroTrust.Access.Groups.Delete(ctx, groupID, zero_trust.AccessGroupDeleteParams{
		AccountID: cf.F(accountID),
	})
	if err != nil {
		if isNotFound(err) {
			return nil // Already deleted
		}
		return fmt.Errorf("failed to delete access group: %w", err)
	}

	return nil
}

// ListAccessGroups lists all Access groups.
func (c *clientImpl) ListAccessGroups(ctx context.Context, accountID string) ([]AccessGroup, error) {
	var groups []AccessGroup

	page := c.api.ZeroTrust.Access.Groups.ListAutoPaging(ctx, zero_trust.AccessGroupListParams{
		AccountID: cf.F(accountID),
	})

	for page.Next() {
		group := page.Current()
		groups = append(groups, *groupFromListResponse(&group))
	}

	if err := page.Err(); err != nil {
		return nil, fmt.Errorf("failed to list access groups: %w", err)
	}

	return groups, nil
}

// GetAccessGroupByName retrieves an Access group by name.
// Returns nil if the group does not exist.
func (c *clientImpl) GetAccessGroupByName(ctx context.Context, accountID, name string) (*AccessGroup, error) {
	groups, err := c.ListAccessGroups(ctx, accountID)
	if err != nil {
		return nil, err
	}

	for _, group := range groups {
		if group.Name == name {
			groupCopy := group
			return &groupCopy, nil
		}
	}

	return nil, nil // Not found
}

// =============================================================================
// Service Token operations
// =============================================================================

// CreateServiceToken creates a new service token.
func (c *clientImpl) CreateServiceToken(ctx context.Context, accountID string, params ServiceTokenParams) (*ServiceTokenWithSecret, error) {
	result, err := c.api.ZeroTrust.Access.ServiceTokens.New(ctx, zero_trust.AccessServiceTokenNewParams{
		AccountID: cf.F(accountID),
		Name:      cf.F(params.Name),
		Duration:  cf.F(params.Duration),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create service token: %w", err)
	}

	return &ServiceTokenWithSecret{
		ServiceToken: ServiceToken{
			ID:       result.ID,
			Name:     result.Name,
			ClientID: result.ClientID,
			Duration: result.Duration,
			// ExpiresAt not in create response
		},
		ClientSecret: result.ClientSecret,
	}, nil
}

// GetServiceToken retrieves a service token by ID.
func (c *clientImpl) GetServiceToken(ctx context.Context, accountID, tokenID string) (*ServiceToken, error) {
	// The SDK doesn't have a direct Get method, so we list and filter
	tokens, err := c.ListServiceTokens(ctx, accountID)
	if err != nil {
		return nil, err
	}

	for _, token := range tokens {
		if token.ID == tokenID {
			tokenCopy := token
			return &tokenCopy, nil
		}
	}

	return nil, nil // Not found
}

// UpdateServiceToken updates an existing service token.
func (c *clientImpl) UpdateServiceToken(ctx context.Context, accountID, tokenID string, params ServiceTokenParams) (*ServiceToken, error) {
	result, err := c.api.ZeroTrust.Access.ServiceTokens.Update(ctx, tokenID, zero_trust.AccessServiceTokenUpdateParams{
		AccountID: cf.F(accountID),
		Name:      cf.F(params.Name),
		Duration:  cf.F(params.Duration),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update service token: %w", err)
	}

	return &ServiceToken{
		ID:       result.ID,
		Name:     result.Name,
		ClientID: result.ClientID,
		Duration: result.Duration,
		// ExpiresAt not in update response
	}, nil
}

// DeleteServiceToken deletes a service token.
func (c *clientImpl) DeleteServiceToken(ctx context.Context, accountID, tokenID string) error {
	_, err := c.api.ZeroTrust.Access.ServiceTokens.Delete(ctx, tokenID, zero_trust.AccessServiceTokenDeleteParams{
		AccountID: cf.F(accountID),
	})
	if err != nil {
		if isNotFound(err) {
			return nil // Already deleted
		}
		return fmt.Errorf("failed to delete service token: %w", err)
	}

	return nil
}

// ListServiceTokens lists all service tokens.
func (c *clientImpl) ListServiceTokens(ctx context.Context, accountID string) ([]ServiceToken, error) {
	var tokens []ServiceToken

	page := c.api.ZeroTrust.Access.ServiceTokens.ListAutoPaging(ctx, zero_trust.AccessServiceTokenListParams{
		AccountID: cf.F(accountID),
	})

	for page.Next() {
		token := page.Current()
		tokens = append(tokens, ServiceToken{
			ID:        token.ID,
			Name:      token.Name,
			ClientID:  token.ClientID,
			Duration:  token.Duration,
			ExpiresAt: token.ExpiresAt,
		})
	}

	if err := page.Err(); err != nil {
		return nil, fmt.Errorf("failed to list service tokens: %w", err)
	}

	return tokens, nil
}

// RotateServiceToken rotates a service token.
func (c *clientImpl) RotateServiceToken(ctx context.Context, accountID, tokenID string) (*ServiceTokenWithSecret, error) {
	result, err := c.api.ZeroTrust.Access.ServiceTokens.Rotate(ctx, tokenID, zero_trust.AccessServiceTokenRotateParams{
		AccountID: cf.F(accountID),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to rotate service token: %w", err)
	}

	return &ServiceTokenWithSecret{
		ServiceToken: ServiceToken{
			ID:       result.ID,
			Name:     result.Name,
			ClientID: result.ClientID,
			Duration: result.Duration,
			// ExpiresAt not in rotate response
		},
		ClientSecret: result.ClientSecret,
	}, nil
}

// RefreshServiceToken refreshes a service token's expiration.
func (c *clientImpl) RefreshServiceToken(ctx context.Context, accountID, tokenID string) (*ServiceToken, error) {
	result, err := c.api.ZeroTrust.Access.ServiceTokens.Refresh(ctx, tokenID, zero_trust.AccessServiceTokenRefreshParams{
		AccountID: cf.F(accountID),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to refresh service token: %w", err)
	}

	return &ServiceToken{
		ID:        result.ID,
		Name:      result.Name,
		ClientID:  result.ClientID,
		Duration:  result.Duration,
		ExpiresAt: result.ExpiresAt,
	}, nil
}

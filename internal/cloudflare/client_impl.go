package cloudflare

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

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
		record := page.Current()
		records = append(records, DNSRecord{
			ID:      record.ID,
			Type:    string(record.Type),
			Name:    record.Name,
			Content: record.Content,
			TTL:     int(record.TTL),
			Proxied: record.Proxied,
			Comment: record.Comment,
			ZoneID:  zoneID,
		})
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
		record := page.Current()
		records = append(records, DNSRecord{
			ID:      record.ID,
			Type:    string(record.Type),
			Name:    record.Name,
			Content: record.Content,
			TTL:     int(record.TTL),
			Proxied: record.Proxied,
			Comment: record.Comment,
			ZoneID:  zoneID,
		})
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
	default:
		return nil, fmt.Errorf("unsupported record type: %s", record.Type)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create DNS record: %w", err)
	}

	return &DNSRecord{
		ID:      result.ID,
		Type:    string(result.Type),
		Name:    result.Name,
		Content: result.Content,
		TTL:     int(result.TTL),
		Proxied: result.Proxied,
		Comment: result.Comment,
		ZoneID:  zoneID,
	}, nil
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
	default:
		return nil, fmt.Errorf("unsupported record type: %s", record.Type)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to update DNS record: %w", err)
	}

	return &DNSRecord{
		ID:      result.ID,
		Type:    string(result.Type),
		Name:    result.Name,
		Content: result.Content,
		TTL:     int(result.TTL),
		Proxied: result.Proxied,
		Comment: result.Comment,
		ZoneID:  zoneID,
	}, nil
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

// tunnelFromAPI converts a cloudflare-go SDK tunnel response to the domain Tunnel type.
func tunnelFromAPI(t *zero_trust.CloudflareTunnel) *Tunnel {
	if t == nil {
		return nil
	}

	return &Tunnel{
		ID:         t.ID,
		Name:       t.Name,
		Status:     string(t.Status),
		AccountTag: t.AccountTag,
		CreatedAt:  t.CreatedAt.String(),
	}
}

// ingressOriginRequestToAPI converts OriginRequestConfig to the SDK per-ingress origin request format.
func ingressOriginRequestToAPI(config *OriginRequestConfig) zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest {
	req := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest{}

	if config.ConnectTimeout != "" {
		req.ConnectTimeout = cf.F(parseDurationSeconds(config.ConnectTimeout, 30))
	}
	if config.NoHappyEyeballs {
		req.NoHappyEyeballs = cf.F(true)
	}
	if config.KeepAliveConnections > 0 {
		req.KeepAliveConnections = cf.F(int64(config.KeepAliveConnections))
	}
	if config.HTTPHostHeader != "" {
		req.HTTPHostHeader = cf.F(config.HTTPHostHeader)
	}
	if config.OriginServerName != "" {
		req.OriginServerName = cf.F(config.OriginServerName)
	}
	if config.CAPool != "" {
		req.CAPool = cf.F(config.CAPool)
	}
	if config.NoTLSVerify {
		req.NoTLSVerify = cf.F(true)
	}
	if config.DisableChunkedEncoding {
		req.DisableChunkedEncoding = cf.F(true)
	}
	if config.HTTP2Origin {
		req.HTTP2Origin = cf.F(true)
	}

	return req
}

// globalOriginRequestToAPI converts OriginRequestConfig to the SDK global origin request format.
func globalOriginRequestToAPI(config *OriginRequestConfig) zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigOriginRequest {
	req := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigOriginRequest{}

	if config.ConnectTimeout != "" {
		req.ConnectTimeout = cf.F(parseDurationSeconds(config.ConnectTimeout, 30))
	}
	if config.NoHappyEyeballs {
		req.NoHappyEyeballs = cf.F(true)
	}
	if config.KeepAliveConnections > 0 {
		req.KeepAliveConnections = cf.F(int64(config.KeepAliveConnections))
	}
	if config.HTTPHostHeader != "" {
		req.HTTPHostHeader = cf.F(config.HTTPHostHeader)
	}
	if config.OriginServerName != "" {
		req.OriginServerName = cf.F(config.OriginServerName)
	}
	if config.CAPool != "" {
		req.CAPool = cf.F(config.CAPool)
	}
	if config.NoTLSVerify {
		req.NoTLSVerify = cf.F(true)
	}
	if config.DisableChunkedEncoding {
		req.DisableChunkedEncoding = cf.F(true)
	}
	if config.HTTP2Origin {
		req.HTTP2Origin = cf.F(true)
	}

	return req
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
func (c *clientImpl) CreateAccessApplication(ctx context.Context, accountID string, params CreateApplicationParams) (*AccessApplication, error) {
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
func (c *clientImpl) UpdateAccessApplication(ctx context.Context, accountID, appID string, params UpdateApplicationParams) (*AccessApplication, error) {
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

// GetAccessApplicationByName retrieves an Access application by name.
// Returns nil if the application does not exist.
func (c *clientImpl) GetAccessApplicationByName(ctx context.Context, accountID, name string) (*AccessApplication, error) {
	apps, err := c.ListAccessApplications(ctx, accountID)
	if err != nil {
		return nil, err
	}

	for _, app := range apps {
		if app.Name == name {
			appCopy := app
			return &appCopy, nil
		}
	}

	return nil, nil // Not found
}

// =============================================================================
// Access Policy operations (application-scoped)
// =============================================================================

// CreateAccessPolicy creates a new Access policy for an application.
func (c *clientImpl) CreateAccessPolicy(ctx context.Context, accountID, appID string, params CreatePolicyParams) (*AccessPolicy, error) {
	// Build request options with fields missing from SDK's NewParams struct.
	// cloudflare-go v6.6.0 doesn't include name, decision, include, exclude, require in NewParams.
	opts := []option.RequestOption{
		option.WithJSONSet("name", params.Name),
		option.WithJSONSet("decision", params.Decision),
	}
	if len(params.Include) > 0 {
		opts = append(opts, option.WithJSONSet("include", accessRulesToAPI(params.Include)))
	}
	if len(params.Exclude) > 0 {
		opts = append(opts, option.WithJSONSet("exclude", accessRulesToAPI(params.Exclude)))
	}
	if len(params.Require) > 0 {
		opts = append(opts, option.WithJSONSet("require", accessRulesToAPI(params.Require)))
	}

	result, err := c.api.ZeroTrust.Access.Applications.Policies.New(ctx, appID, zero_trust.AccessApplicationPolicyNewParams{
		AccountID:                    cf.F(accountID),
		Precedence:                   cf.F(int64(params.Precedence)),
		SessionDuration:              cf.F(params.SessionDuration),
		PurposeJustificationRequired: cf.F(params.PurposeJustificationRequired),
		PurposeJustificationPrompt:   cf.F(params.PurposeJustificationPrompt),
		ApprovalRequired:             cf.F(params.ApprovalRequired),
	}, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create access policy: %w", err)
	}

	return policyFromNewResponse(result, params.Name, params.Decision), nil
}

// GetAccessPolicy retrieves an Access policy by ID.
func (c *clientImpl) GetAccessPolicy(ctx context.Context, accountID, appID, policyID string) (*AccessPolicy, error) {
	result, err := c.api.ZeroTrust.Access.Applications.Policies.Get(ctx, appID, policyID, zero_trust.AccessApplicationPolicyGetParams{
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
func (c *clientImpl) UpdateAccessPolicy(ctx context.Context, accountID, appID, policyID string, params UpdatePolicyParams) (*AccessPolicy, error) {
	// Build request options with fields missing from SDK's UpdateParams struct.
	opts := []option.RequestOption{
		option.WithJSONSet("name", params.Name),
		option.WithJSONSet("decision", params.Decision),
	}
	if len(params.Include) > 0 {
		opts = append(opts, option.WithJSONSet("include", accessRulesToAPI(params.Include)))
	}
	if len(params.Exclude) > 0 {
		opts = append(opts, option.WithJSONSet("exclude", accessRulesToAPI(params.Exclude)))
	}
	if len(params.Require) > 0 {
		opts = append(opts, option.WithJSONSet("require", accessRulesToAPI(params.Require)))
	}

	result, err := c.api.ZeroTrust.Access.Applications.Policies.Update(ctx, appID, policyID, zero_trust.AccessApplicationPolicyUpdateParams{
		AccountID:                    cf.F(accountID),
		Precedence:                   cf.F(int64(params.Precedence)),
		SessionDuration:              cf.F(params.SessionDuration),
		PurposeJustificationRequired: cf.F(params.PurposeJustificationRequired),
		PurposeJustificationPrompt:   cf.F(params.PurposeJustificationPrompt),
		ApprovalRequired:             cf.F(params.ApprovalRequired),
	}, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to update access policy: %w", err)
	}

	return policyFromUpdateResponse(result, params.Name, params.Decision), nil
}

// DeleteAccessPolicy deletes an Access policy.
func (c *clientImpl) DeleteAccessPolicy(ctx context.Context, accountID, appID, policyID string) error {
	_, err := c.api.ZeroTrust.Access.Applications.Policies.Delete(ctx, appID, policyID, zero_trust.AccessApplicationPolicyDeleteParams{
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

// ListAccessPolicies lists all Access policies for an application.
func (c *clientImpl) ListAccessPolicies(ctx context.Context, accountID, appID string) ([]AccessPolicy, error) {
	var policies []AccessPolicy

	page := c.api.ZeroTrust.Access.Applications.Policies.ListAutoPaging(ctx, appID, zero_trust.AccessApplicationPolicyListParams{
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
func (c *clientImpl) CreateAccessGroup(ctx context.Context, accountID string, params CreateGroupParams) (*AccessGroup, error) {
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
func (c *clientImpl) UpdateAccessGroup(ctx context.Context, accountID, groupID string, params UpdateGroupParams) (*AccessGroup, error) {
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
func (c *clientImpl) CreateServiceToken(ctx context.Context, accountID string, params CreateServiceTokenParams) (*ServiceTokenWithSecret, error) {
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
func (c *clientImpl) UpdateServiceToken(ctx context.Context, accountID, tokenID string, params UpdateServiceTokenParams) (*ServiceToken, error) {
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

// =============================================================================
// mTLS Certificate operations
// =============================================================================

// CreateMTLSCertificate creates a new mTLS certificate.
func (c *clientImpl) CreateMTLSCertificate(ctx context.Context, accountID string, params CreateCertificateParams) (*MTLSCertificate, error) {
	result, err := c.api.ZeroTrust.Access.Certificates.New(ctx, zero_trust.AccessCertificateNewParams{
		AccountID:           cf.F(accountID),
		Name:                cf.F(params.Name),
		Certificate:         cf.F(params.Certificate),
		AssociatedHostnames: cf.F(params.AssociatedHostnames),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create mTLS certificate: %w", err)
	}

	return &MTLSCertificate{
		ID:                  result.ID,
		Name:                result.Name,
		Fingerprint:         result.Fingerprint,
		AssociatedHostnames: result.AssociatedHostnames,
		ExpiresOn:           result.ExpiresOn,
	}, nil
}

// GetMTLSCertificate retrieves an mTLS certificate by ID.
func (c *clientImpl) GetMTLSCertificate(ctx context.Context, accountID, certID string) (*MTLSCertificate, error) {
	result, err := c.api.ZeroTrust.Access.Certificates.Get(ctx, certID, zero_trust.AccessCertificateGetParams{
		AccountID: cf.F(accountID),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get mTLS certificate: %w", err)
	}

	return &MTLSCertificate{
		ID:                  result.ID,
		Name:                result.Name,
		Fingerprint:         result.Fingerprint,
		AssociatedHostnames: result.AssociatedHostnames,
		ExpiresOn:           result.ExpiresOn,
	}, nil
}

// UpdateMTLSCertificate updates an existing mTLS certificate.
func (c *clientImpl) UpdateMTLSCertificate(ctx context.Context, accountID, certID string, params UpdateCertificateParams) (*MTLSCertificate, error) {
	result, err := c.api.ZeroTrust.Access.Certificates.Update(ctx, certID, zero_trust.AccessCertificateUpdateParams{
		AccountID:           cf.F(accountID),
		Name:                cf.F(params.Name),
		AssociatedHostnames: cf.F(params.AssociatedHostnames),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update mTLS certificate: %w", err)
	}

	return &MTLSCertificate{
		ID:                  result.ID,
		Name:                result.Name,
		Fingerprint:         result.Fingerprint,
		AssociatedHostnames: result.AssociatedHostnames,
		ExpiresOn:           result.ExpiresOn,
	}, nil
}

// DeleteMTLSCertificate deletes an mTLS certificate.
func (c *clientImpl) DeleteMTLSCertificate(ctx context.Context, accountID, certID string) error {
	_, err := c.api.ZeroTrust.Access.Certificates.Delete(ctx, certID, zero_trust.AccessCertificateDeleteParams{
		AccountID: cf.F(accountID),
	})
	if err != nil {
		if isNotFound(err) {
			return nil // Already deleted
		}
		return fmt.Errorf("failed to delete mTLS certificate: %w", err)
	}

	return nil
}

// ListMTLSCertificates lists all mTLS certificates.
func (c *clientImpl) ListMTLSCertificates(ctx context.Context, accountID string) ([]MTLSCertificate, error) {
	var certs []MTLSCertificate

	page := c.api.ZeroTrust.Access.Certificates.ListAutoPaging(ctx, zero_trust.AccessCertificateListParams{
		AccountID: cf.F(accountID),
	})

	for page.Next() {
		cert := page.Current()
		certs = append(certs, MTLSCertificate{
			ID:                  cert.ID,
			Name:                cert.Name,
			Fingerprint:         cert.Fingerprint,
			AssociatedHostnames: cert.AssociatedHostnames,
			ExpiresOn:           cert.ExpiresOn,
		})
	}

	if err := page.Err(); err != nil {
		return nil, fmt.Errorf("failed to list mTLS certificates: %w", err)
	}

	return certs, nil
}

// =============================================================================
// mTLS Certificate Settings
// =============================================================================

// GetMTLSCertificateSettings retrieves mTLS certificate settings.
func (c *clientImpl) GetMTLSCertificateSettings(ctx context.Context, accountID string) ([]CertificateSettings, error) {
	result, err := c.api.ZeroTrust.Access.Certificates.Settings.Get(ctx, zero_trust.AccessCertificateSettingGetParams{
		AccountID: cf.F(accountID),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get mTLS certificate settings: %w", err)
	}

	settings := make([]CertificateSettings, len(result.Result))
	for i, s := range result.Result {
		settings[i] = CertificateSettings{
			Hostname:                    s.Hostname,
			ChinaNetwork:                s.ChinaNetwork,
			ClientCertificateForwarding: s.ClientCertificateForwarding,
		}
	}

	return settings, nil
}

// UpdateMTLSCertificateSettings updates mTLS certificate settings.
func (c *clientImpl) UpdateMTLSCertificateSettings(ctx context.Context, accountID string, settings []CertificateSettingsParam) ([]CertificateSettings, error) {
	apiSettings := make([]zero_trust.CertificateSettingsParam, len(settings))
	for i, s := range settings {
		apiSettings[i] = zero_trust.CertificateSettingsParam{
			Hostname:                    cf.F(s.Hostname),
			ChinaNetwork:                cf.F(s.ChinaNetwork),
			ClientCertificateForwarding: cf.F(s.ClientCertificateForwarding),
		}
	}

	result, err := c.api.ZeroTrust.Access.Certificates.Settings.Update(ctx, zero_trust.AccessCertificateSettingUpdateParams{
		AccountID: cf.F(accountID),
		Settings:  cf.F(apiSettings),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update mTLS certificate settings: %w", err)
	}

	resultSettings := make([]CertificateSettings, len(result.Result))
	for i, s := range result.Result {
		resultSettings[i] = CertificateSettings{
			Hostname:                    s.Hostname,
			ChinaNetwork:                s.ChinaNetwork,
			ClientCertificateForwarding: s.ClientCertificateForwarding,
		}
	}

	return resultSettings, nil
}

// =============================================================================
// SDK response conversion helpers
// =============================================================================

// parseDurationSeconds parses a Go duration string and returns the value in whole seconds.
// Falls back to defaultSec if the string is not a valid duration.
func parseDurationSeconds(s string, defaultSec int64) int64 {
	d, err := time.ParseDuration(s)
	if err != nil {
		return defaultSec
	}
	sec := int64(d.Seconds())
	if sec <= 0 {
		return defaultSec
	}
	return sec
}

// corsHeadersToSDK converts internal CORSHeadersParam to SDK CORSHeadersParam.
func corsHeadersToSDK(h *CORSHeadersParam) zero_trust.CORSHeadersParam {
	p := zero_trust.CORSHeadersParam{
		AllowAllHeaders:  cf.F(h.AllowAllHeaders),
		AllowAllMethods:  cf.F(h.AllowAllMethods),
		AllowAllOrigins:  cf.F(h.AllowAllOrigins),
		AllowCredentials: cf.F(h.AllowCredentials),
		MaxAge:           cf.F(float64(h.MaxAge)),
	}
	if len(h.AllowedHeaders) > 0 {
		headers := make([]zero_trust.AllowedHeadersParam, len(h.AllowedHeaders))
		for i, hdr := range h.AllowedHeaders {
			headers[i] = zero_trust.AllowedHeadersParam(hdr)
		}
		p.AllowedHeaders = cf.F(headers)
	}
	if len(h.AllowedMethods) > 0 {
		methods := make([]zero_trust.AllowedMethods, len(h.AllowedMethods))
		for i, m := range h.AllowedMethods {
			methods[i] = zero_trust.AllowedMethods(m)
		}
		p.AllowedMethods = cf.F(methods)
	}
	if len(h.AllowedOrigins) > 0 {
		origins := make([]zero_trust.AllowedOriginsParam, len(h.AllowedOrigins))
		for i, o := range h.AllowedOrigins {
			origins[i] = zero_trust.AllowedOriginsParam(o)
		}
		p.AllowedOrigins = cf.F(origins)
	}
	return p
}

// corsHeadersFromSDK converts SDK CORSHeaders response to internal CORSHeadersParam.
func corsHeadersFromSDK(h *zero_trust.CORSHeaders) *CORSHeadersParam {
	p := &CORSHeadersParam{
		AllowAllHeaders:  h.AllowAllHeaders,
		AllowAllMethods:  h.AllowAllMethods,
		AllowAllOrigins:  h.AllowAllOrigins,
		AllowCredentials: h.AllowCredentials,
		MaxAge:           int(h.MaxAge),
	}
	for _, hdr := range h.AllowedHeaders {
		p.AllowedHeaders = append(p.AllowedHeaders, string(hdr))
	}
	for _, m := range h.AllowedMethods {
		p.AllowedMethods = append(p.AllowedMethods, string(m))
	}
	for _, o := range h.AllowedOrigins {
		p.AllowedOrigins = append(p.AllowedOrigins, string(o))
	}
	return p
}

// extractAllowedIdPs extracts AllowedIdPs from the SDK union response interface{}.
// The CF SDK v6 uses apijson custom unmarshaling which may produce []string ([]AllowedIdPs)
// instead of []interface{} depending on the response type. Handle both.
func extractAllowedIdPs(v interface{}) []string {
	switch idps := v.(type) {
	case []string:
		return idps
	case []interface{}:
		var result []string
		for _, idp := range idps {
			if s, ok := idp.(string); ok {
				result = append(result, s)
			}
		}
		return result
	default:
		return nil
	}
}

// approvalGroupsFromAPI converts SDK ApprovalGroup responses to internal ApprovalGroupParam.
func approvalGroupsFromAPI(groups []zero_trust.ApprovalGroup) []ApprovalGroupParam {
	if len(groups) == 0 {
		return nil
	}
	result := make([]ApprovalGroupParam, len(groups))
	for i, g := range groups {
		result[i] = ApprovalGroupParam{
			EmailAddresses:  g.EmailAddresses,
			EmailListUUID:   g.EmailListUUID,
			ApprovalsNeeded: int(g.ApprovalsNeeded),
		}
	}
	return result
}

// applicationFromNewResponse converts AccessApplicationNewResponse to AccessApplication.
func applicationFromNewResponse(resp *zero_trust.AccessApplicationNewResponse) *AccessApplication {
	if resp == nil {
		return nil
	}

	// Use flat fields directly from response - no union extraction needed for common fields
	app := &AccessApplication{
		ID:                          resp.ID,
		AUD:                         resp.AUD,
		Name:                        resp.Name,
		Domain:                      resp.Domain,
		Type:                        string(resp.Type),
		SessionDuration:             resp.SessionDuration,
		AutoRedirectToIdentity:      resp.AutoRedirectToIdentity,
		EnableBindingCookie:         resp.EnableBindingCookie,
		HttpOnlyCookieAttribute:     resp.HTTPOnlyCookieAttribute,
		SameSiteCookieAttribute:     resp.SameSiteCookieAttribute,
		SkipInterstitial:            resp.SkipInterstitial,
		LogoURL:                     resp.LogoURL,
		AppLauncherVisible:          resp.AppLauncherVisible,
		CustomDenyMessage:           resp.CustomDenyMessage,
		CustomDenyURL:               resp.CustomDenyURL,
		OptionsPreflightBypass:      resp.OptionsPreflightBypass,
		PathCookieAttribute:         resp.PathCookieAttribute,
		ServiceAuth401Redirect:      resp.ServiceAuth401Redirect,
		CustomNonIdentityDenyURL:    resp.CustomNonIdentityDenyURL,
		ReadServiceTokensFromHeader: resp.ReadServiceTokensFromHeader,
		// CreatedAt and UpdatedAt not available in application responses
	}

	// Handle AllowedIdPs which is an interface{} in union response types.
	// SDK apijson may unmarshal as []string or []interface{} depending on type.
	app.AllowedIdps = extractAllowedIdPs(resp.AllowedIdPs)

	// Parse CORSHeaders if any sub-field is non-zero
	if resp.CORSHeaders.AllowAllHeaders || resp.CORSHeaders.AllowAllMethods ||
		resp.CORSHeaders.AllowAllOrigins || resp.CORSHeaders.AllowCredentials ||
		len(resp.CORSHeaders.AllowedHeaders) > 0 || len(resp.CORSHeaders.AllowedMethods) > 0 ||
		len(resp.CORSHeaders.AllowedOrigins) > 0 || resp.CORSHeaders.MaxAge > 0 {
		app.CORSHeaders = corsHeadersFromSDK(&resp.CORSHeaders)
	}

	return app
}

// applicationFromGetResponse converts AccessApplicationGetResponse to AccessApplication.
func applicationFromGetResponse(resp *zero_trust.AccessApplicationGetResponse) *AccessApplication {
	if resp == nil {
		return nil
	}

	app := &AccessApplication{
		ID:                          resp.ID,
		AUD:                         resp.AUD,
		Name:                        resp.Name,
		Domain:                      resp.Domain,
		Type:                        string(resp.Type),
		SessionDuration:             resp.SessionDuration,
		AutoRedirectToIdentity:      resp.AutoRedirectToIdentity,
		EnableBindingCookie:         resp.EnableBindingCookie,
		HttpOnlyCookieAttribute:     resp.HTTPOnlyCookieAttribute,
		SameSiteCookieAttribute:     resp.SameSiteCookieAttribute,
		SkipInterstitial:            resp.SkipInterstitial,
		LogoURL:                     resp.LogoURL,
		AppLauncherVisible:          resp.AppLauncherVisible,
		CustomDenyMessage:           resp.CustomDenyMessage,
		CustomDenyURL:               resp.CustomDenyURL,
		OptionsPreflightBypass:      resp.OptionsPreflightBypass,
		PathCookieAttribute:         resp.PathCookieAttribute,
		ServiceAuth401Redirect:      resp.ServiceAuth401Redirect,
		CustomNonIdentityDenyURL:    resp.CustomNonIdentityDenyURL,
		ReadServiceTokensFromHeader: resp.ReadServiceTokensFromHeader,
	}

	// Handle AllowedIdPs which is an interface{} in union response types.
	// SDK apijson may unmarshal as []string or []interface{} depending on type.
	app.AllowedIdps = extractAllowedIdPs(resp.AllowedIdPs)

	// Parse CORSHeaders if any sub-field is non-zero
	if resp.CORSHeaders.AllowAllHeaders || resp.CORSHeaders.AllowAllMethods ||
		resp.CORSHeaders.AllowAllOrigins || resp.CORSHeaders.AllowCredentials ||
		len(resp.CORSHeaders.AllowedHeaders) > 0 || len(resp.CORSHeaders.AllowedMethods) > 0 ||
		len(resp.CORSHeaders.AllowedOrigins) > 0 || resp.CORSHeaders.MaxAge > 0 {
		app.CORSHeaders = corsHeadersFromSDK(&resp.CORSHeaders)
	}

	return app
}

// applicationFromUpdateResponse converts AccessApplicationUpdateResponse to AccessApplication.
func applicationFromUpdateResponse(resp *zero_trust.AccessApplicationUpdateResponse) *AccessApplication {
	if resp == nil {
		return nil
	}

	app := &AccessApplication{
		ID:                          resp.ID,
		AUD:                         resp.AUD,
		Name:                        resp.Name,
		Domain:                      resp.Domain,
		Type:                        string(resp.Type),
		SessionDuration:             resp.SessionDuration,
		AutoRedirectToIdentity:      resp.AutoRedirectToIdentity,
		EnableBindingCookie:         resp.EnableBindingCookie,
		HttpOnlyCookieAttribute:     resp.HTTPOnlyCookieAttribute,
		SameSiteCookieAttribute:     resp.SameSiteCookieAttribute,
		SkipInterstitial:            resp.SkipInterstitial,
		LogoURL:                     resp.LogoURL,
		AppLauncherVisible:          resp.AppLauncherVisible,
		CustomDenyMessage:           resp.CustomDenyMessage,
		CustomDenyURL:               resp.CustomDenyURL,
		OptionsPreflightBypass:      resp.OptionsPreflightBypass,
		PathCookieAttribute:         resp.PathCookieAttribute,
		ServiceAuth401Redirect:      resp.ServiceAuth401Redirect,
		CustomNonIdentityDenyURL:    resp.CustomNonIdentityDenyURL,
		ReadServiceTokensFromHeader: resp.ReadServiceTokensFromHeader,
	}

	// Handle AllowedIdPs which is an interface{} in union response types.
	// SDK apijson may unmarshal as []string or []interface{} depending on type.
	app.AllowedIdps = extractAllowedIdPs(resp.AllowedIdPs)

	// Parse CORSHeaders if any sub-field is non-zero
	if resp.CORSHeaders.AllowAllHeaders || resp.CORSHeaders.AllowAllMethods ||
		resp.CORSHeaders.AllowAllOrigins || resp.CORSHeaders.AllowCredentials ||
		len(resp.CORSHeaders.AllowedHeaders) > 0 || len(resp.CORSHeaders.AllowedMethods) > 0 ||
		len(resp.CORSHeaders.AllowedOrigins) > 0 || resp.CORSHeaders.MaxAge > 0 {
		app.CORSHeaders = corsHeadersFromSDK(&resp.CORSHeaders)
	}

	return app
}

// applicationFromListResponse converts AccessApplicationListResponse to AccessApplication.
func applicationFromListResponse(resp *zero_trust.AccessApplicationListResponse) *AccessApplication {
	if resp == nil {
		return nil
	}

	app := &AccessApplication{
		ID:                          resp.ID,
		AUD:                         resp.AUD,
		Name:                        resp.Name,
		Domain:                      resp.Domain,
		Type:                        string(resp.Type),
		SessionDuration:             resp.SessionDuration,
		AutoRedirectToIdentity:      resp.AutoRedirectToIdentity,
		EnableBindingCookie:         resp.EnableBindingCookie,
		HttpOnlyCookieAttribute:     resp.HTTPOnlyCookieAttribute,
		SameSiteCookieAttribute:     resp.SameSiteCookieAttribute,
		SkipInterstitial:            resp.SkipInterstitial,
		LogoURL:                     resp.LogoURL,
		AppLauncherVisible:          resp.AppLauncherVisible,
		CustomDenyMessage:           resp.CustomDenyMessage,
		CustomDenyURL:               resp.CustomDenyURL,
		OptionsPreflightBypass:      resp.OptionsPreflightBypass,
		PathCookieAttribute:         resp.PathCookieAttribute,
		ServiceAuth401Redirect:      resp.ServiceAuth401Redirect,
		CustomNonIdentityDenyURL:    resp.CustomNonIdentityDenyURL,
		ReadServiceTokensFromHeader: resp.ReadServiceTokensFromHeader,
	}

	// Handle AllowedIdPs which is an interface{} in union response types.
	// SDK apijson may unmarshal as []string or []interface{} depending on type.
	app.AllowedIdps = extractAllowedIdPs(resp.AllowedIdPs)

	// Parse CORSHeaders if any sub-field is non-zero
	if resp.CORSHeaders.AllowAllHeaders || resp.CORSHeaders.AllowAllMethods ||
		resp.CORSHeaders.AllowAllOrigins || resp.CORSHeaders.AllowCredentials ||
		len(resp.CORSHeaders.AllowedHeaders) > 0 || len(resp.CORSHeaders.AllowedMethods) > 0 ||
		len(resp.CORSHeaders.AllowedOrigins) > 0 || resp.CORSHeaders.MaxAge > 0 {
		app.CORSHeaders = corsHeadersFromSDK(&resp.CORSHeaders)
	}

	return app
}

// policyFromNewResponse converts AccessApplicationPolicyNewResponse to AccessPolicy.
func policyFromNewResponse(resp *zero_trust.AccessApplicationPolicyNewResponse, name, decision string) *AccessPolicy {
	if resp == nil {
		return nil
	}

	return &AccessPolicy{
		ID:                           resp.ID,
		Name:                         name,
		Decision:                     decision,
		Precedence:                   int(resp.Precedence),
		Include:                      accessRulesFromAPI(resp.Include),
		Exclude:                      accessRulesFromAPI(resp.Exclude),
		Require:                      accessRulesFromAPI(resp.Require),
		SessionDuration:              resp.SessionDuration,
		PurposeJustificationRequired: resp.PurposeJustificationRequired,
		PurposeJustificationPrompt:   resp.PurposeJustificationPrompt,
		ApprovalRequired:             resp.ApprovalRequired,
		ApprovalGroups:               approvalGroupsFromAPI(resp.ApprovalGroups),
		CreatedAt:                    resp.CreatedAt,
		UpdatedAt:                    resp.UpdatedAt,
	}
}

// policyFromGetResponse converts AccessApplicationPolicyGetResponse to AccessPolicy.
func policyFromGetResponse(resp *zero_trust.AccessApplicationPolicyGetResponse) *AccessPolicy {
	if resp == nil {
		return nil
	}

	return &AccessPolicy{
		ID:                           resp.ID,
		Name:                         resp.Name,
		Decision:                     string(resp.Decision),
		Precedence:                   int(resp.Precedence),
		Include:                      accessRulesFromAPI(resp.Include),
		Exclude:                      accessRulesFromAPI(resp.Exclude),
		Require:                      accessRulesFromAPI(resp.Require),
		SessionDuration:              resp.SessionDuration,
		PurposeJustificationRequired: resp.PurposeJustificationRequired,
		PurposeJustificationPrompt:   resp.PurposeJustificationPrompt,
		ApprovalRequired:             resp.ApprovalRequired,
		ApprovalGroups:               approvalGroupsFromAPI(resp.ApprovalGroups),
		CreatedAt:                    resp.CreatedAt,
		UpdatedAt:                    resp.UpdatedAt,
	}
}

// policyFromUpdateResponse converts AccessApplicationPolicyUpdateResponse to AccessPolicy.
func policyFromUpdateResponse(resp *zero_trust.AccessApplicationPolicyUpdateResponse, name, decision string) *AccessPolicy {
	if resp == nil {
		return nil
	}

	return &AccessPolicy{
		ID:                           resp.ID,
		Name:                         name,
		Decision:                     decision,
		Precedence:                   int(resp.Precedence),
		Include:                      accessRulesFromAPI(resp.Include),
		Exclude:                      accessRulesFromAPI(resp.Exclude),
		Require:                      accessRulesFromAPI(resp.Require),
		SessionDuration:              resp.SessionDuration,
		PurposeJustificationRequired: resp.PurposeJustificationRequired,
		PurposeJustificationPrompt:   resp.PurposeJustificationPrompt,
		ApprovalRequired:             resp.ApprovalRequired,
		ApprovalGroups:               approvalGroupsFromAPI(resp.ApprovalGroups),
		CreatedAt:                    resp.CreatedAt,
		UpdatedAt:                    resp.UpdatedAt,
	}
}

// policyFromListResponse converts AccessApplicationPolicyListResponse to AccessPolicy.
func policyFromListResponse(resp *zero_trust.AccessApplicationPolicyListResponse) *AccessPolicy {
	if resp == nil {
		return nil
	}

	return &AccessPolicy{
		ID:                           resp.ID,
		Name:                         resp.Name,
		Decision:                     string(resp.Decision),
		Precedence:                   int(resp.Precedence),
		Include:                      accessRulesFromAPI(resp.Include),
		Exclude:                      accessRulesFromAPI(resp.Exclude),
		Require:                      accessRulesFromAPI(resp.Require),
		SessionDuration:              resp.SessionDuration,
		PurposeJustificationRequired: resp.PurposeJustificationRequired,
		PurposeJustificationPrompt:   resp.PurposeJustificationPrompt,
		ApprovalRequired:             resp.ApprovalRequired,
		ApprovalGroups:               approvalGroupsFromAPI(resp.ApprovalGroups),
		CreatedAt:                    resp.CreatedAt,
		UpdatedAt:                    resp.UpdatedAt,
	}
}

// groupFromNewResponse converts AccessGroupNewResponse to AccessGroup.
func groupFromNewResponse(resp *zero_trust.AccessGroupNewResponse) *AccessGroup {
	if resp == nil {
		return nil
	}

	return &AccessGroup{
		ID:   resp.ID,
		Name: resp.Name,
		// CreatedAt and UpdatedAt not available in group responses
	}
}

// groupFromGetResponse converts AccessGroupGetResponse to AccessGroup.
func groupFromGetResponse(resp *zero_trust.AccessGroupGetResponse) *AccessGroup {
	if resp == nil {
		return nil
	}

	return &AccessGroup{
		ID:   resp.ID,
		Name: resp.Name,
	}
}

// groupFromUpdateResponse converts AccessGroupUpdateResponse to AccessGroup.
func groupFromUpdateResponse(resp *zero_trust.AccessGroupUpdateResponse) *AccessGroup {
	if resp == nil {
		return nil
	}

	return &AccessGroup{
		ID:   resp.ID,
		Name: resp.Name,
	}
}

// groupFromListResponse converts AccessGroupListResponse to AccessGroup.
func groupFromListResponse(resp *zero_trust.AccessGroupListResponse) *AccessGroup {
	if resp == nil {
		return nil
	}

	return &AccessGroup{
		ID:   resp.ID,
		Name: resp.Name,
	}
}

// accessRulesFromAPI converts SDK AccessRule responses back to internal AccessRuleParam.
// Returns nil when the input is empty or all rules are unrecognized types, so that
// reflect.DeepEqual comparisons against nil slices from convertAccessRules do not
// produce spurious drift.
func accessRulesFromAPI(rules []zero_trust.AccessRule) []AccessRuleParam {
	if len(rules) == 0 {
		return nil
	}

	var result []AccessRuleParam
	for _, rule := range rules {
		if param := accessRuleFromAPI(&rule); param != nil {
			result = append(result, *param)
		}
	}
	return result
}

// accessRuleFromAPI converts a single SDK AccessRule response to internal AccessRuleParam.
// Returns nil if the rule type is not recognized. SDK AccessRule fields are interface{}
// union types where only one field is non-nil per rule.
func accessRuleFromAPI(rule *zero_trust.AccessRule) *AccessRuleParam {
	if rule == nil {
		return nil
	}

	if ip, ok := rule.IP.(zero_trust.IPRuleIP); ok {
		return &AccessRuleParam{IPRange: &ip.IP}
	}
	if geo, ok := rule.Geo.(zero_trust.CountryRuleGeo); ok {
		return &AccessRuleParam{Country: &geo.CountryCode}
	}
	if _, ok := rule.Everyone.(zero_trust.EveryoneRuleEveryone); ok {
		t := true
		return &AccessRuleParam{Everyone: &t}
	}
	if st, ok := rule.ServiceToken.(zero_trust.ServiceTokenRuleServiceToken); ok {
		return &AccessRuleParam{ServiceTokenID: &st.TokenID}
	}
	if _, ok := rule.AnyValidServiceToken.(zero_trust.AnyValidServiceTokenRuleAnyValidServiceToken); ok {
		t := true
		return &AccessRuleParam{AnyValidServiceToken: &t}
	}
	if email, ok := rule.Email.(zero_trust.EmailRuleEmail); ok {
		return &AccessRuleParam{Email: &email.Email}
	}
	if domain, ok := rule.EmailDomain.(zero_trust.DomainRuleEmailDomain); ok {
		return &AccessRuleParam{EmailDomain: &domain.Domain}
	}
	if list, ok := rule.EmailList.(zero_trust.EmailListRuleEmailList); ok {
		return &AccessRuleParam{EmailListID: &list.ID}
	}
	if ipList, ok := rule.IPList.(zero_trust.IPListRuleIPList); ok {
		return &AccessRuleParam{IPListID: &ipList.ID}
	}
	if oidc, ok := rule.OIDC.(zero_trust.AccessRuleAccessOIDCClaimRuleOIDC); ok {
		return &AccessRuleParam{OIDCClaim: &OIDCClaimParam{
			IdentityProviderID: oidc.IdentityProviderID,
			ClaimName:          oidc.ClaimName,
			ClaimValue:         oidc.ClaimValue,
		}}
	}
	if gs, ok := rule.GSuite.(zero_trust.GSuiteGroupRuleGSuite); ok {
		return &AccessRuleParam{GSuiteGroup: &GSuiteGroupParam{
			IdentityProviderID: gs.IdentityProviderID,
			Email:              gs.Email,
		}}
	}
	if _, ok := rule.Certificate.(zero_trust.CertificateRuleCertificate); ok {
		t := true
		return &AccessRuleParam{Certificate: &t}
	}
	if cn, ok := rule.CommonName.(zero_trust.AccessRuleAccessCommonNameRuleCommonName); ok {
		return &AccessRuleParam{CommonName: &cn.CommonName}
	}
	if grp, ok := rule.Group.(zero_trust.GroupRuleGroup); ok {
		return &AccessRuleParam{GroupID: &grp.ID}
	}

	return nil
}

// accessRulesToAPI converts a slice of AccessRuleParam to SDK AccessRuleUnionParam.
func accessRulesToAPI(rules []AccessRuleParam) []zero_trust.AccessRuleUnionParam {
	if len(rules) == 0 {
		return nil
	}

	result := make([]zero_trust.AccessRuleUnionParam, 0, len(rules))
	for _, rule := range rules {
		if apiRule := accessRuleToAPI(&rule); apiRule != nil {
			result = append(result, apiRule)
		}
	}

	return result
}

// accessRuleToAPI converts a single AccessRuleParam to SDK AccessRuleUnionParam.
// Returns nil if no rule type is set. Only one field should be set per rule.
func accessRuleToAPI(rule *AccessRuleParam) zero_trust.AccessRuleUnionParam {
	if rule == nil {
		return nil
	}

	// ============================================================
	// P0: No IdP required
	// ============================================================

	// IPRange -> zero_trust.IPRuleParam
	if rule.IPRange != nil {
		return zero_trust.IPRuleParam{
			IP: cf.F(zero_trust.IPRuleIPParam{
				IP: cf.F(*rule.IPRange),
			}),
		}
	}

	// IPListID -> zero_trust.IPListRuleParam
	if rule.IPListID != nil {
		return zero_trust.IPListRuleParam{
			IPList: cf.F(zero_trust.IPListRuleIPListParam{
				ID: cf.F(*rule.IPListID),
			}),
		}
	}

	// Country -> zero_trust.CountryRuleParam
	if rule.Country != nil {
		return zero_trust.CountryRuleParam{
			Geo: cf.F(zero_trust.CountryRuleGeoParam{
				CountryCode: cf.F(*rule.Country),
			}),
		}
	}

	// Everyone -> zero_trust.EveryoneRuleParam
	if rule.Everyone != nil && *rule.Everyone {
		return zero_trust.EveryoneRuleParam{
			Everyone: cf.F(zero_trust.EveryoneRuleEveryoneParam{}),
		}
	}

	// ServiceTokenID -> zero_trust.ServiceTokenRuleParam
	if rule.ServiceTokenID != nil {
		return zero_trust.ServiceTokenRuleParam{
			ServiceToken: cf.F(zero_trust.ServiceTokenRuleServiceTokenParam{
				TokenID: cf.F(*rule.ServiceTokenID),
			}),
		}
	}

	// AnyValidServiceToken -> zero_trust.AnyValidServiceTokenRuleParam
	if rule.AnyValidServiceToken != nil && *rule.AnyValidServiceToken {
		return zero_trust.AnyValidServiceTokenRuleParam{
			AnyValidServiceToken: cf.F(zero_trust.AnyValidServiceTokenRuleAnyValidServiceTokenParam{}),
		}
	}

	// ============================================================
	// P1: Basic IdP (Google Workspace)
	// ============================================================

	// Email -> zero_trust.EmailRuleParam
	if rule.Email != nil {
		return zero_trust.EmailRuleParam{
			Email: cf.F(zero_trust.EmailRuleEmailParam{
				Email: cf.F(*rule.Email),
			}),
		}
	}

	// EmailDomain -> zero_trust.DomainRuleParam
	// Note: SDK type is "DomainRule" not "EmailDomainRule"
	if rule.EmailDomain != nil {
		return zero_trust.DomainRuleParam{
			EmailDomain: cf.F(zero_trust.DomainRuleEmailDomainParam{
				Domain: cf.F(*rule.EmailDomain),
			}),
		}
	}

	// EmailListID -> zero_trust.EmailListRuleParam
	if rule.EmailListID != nil {
		return zero_trust.EmailListRuleParam{
			EmailList: cf.F(zero_trust.EmailListRuleEmailListParam{
				ID: cf.F(*rule.EmailListID),
			}),
		}
	}

	// OIDCClaim -> zero_trust.AccessRuleAccessOIDCClaimRuleParam
	if rule.OIDCClaim != nil {
		return zero_trust.AccessRuleAccessOIDCClaimRuleParam{
			OIDC: cf.F(zero_trust.AccessRuleAccessOIDCClaimRuleOIDCParam{
				IdentityProviderID: cf.F(rule.OIDCClaim.IdentityProviderID),
				ClaimName:          cf.F(rule.OIDCClaim.ClaimName),
				ClaimValue:         cf.F(rule.OIDCClaim.ClaimValue),
			}),
		}
	}

	// ============================================================
	// P2: Google Workspace Groups
	// ============================================================

	// GSuiteGroup -> zero_trust.GSuiteGroupRuleParam
	if rule.GSuiteGroup != nil {
		return zero_trust.GSuiteGroupRuleParam{
			GSuite: cf.F(zero_trust.GSuiteGroupRuleGSuiteParam{
				IdentityProviderID: cf.F(rule.GSuiteGroup.IdentityProviderID),
				Email:              cf.F(rule.GSuiteGroup.Email),
			}),
		}
	}

	// ============================================================
	// P3: v0.2.0 (kept for backward compatibility)
	// ============================================================

	if rule.Certificate != nil && *rule.Certificate {
		return zero_trust.CertificateRuleParam{
			Certificate: cf.F(zero_trust.CertificateRuleCertificateParam{}),
		}
	}

	if rule.GroupID != nil {
		return zero_trust.GroupRuleParam{
			Group: cf.F(zero_trust.GroupRuleGroupParam{
				ID: cf.F(*rule.GroupID),
			}),
		}
	}

	if rule.CommonName != nil {
		return zero_trust.AccessRuleAccessCommonNameRuleParam{
			CommonName: cf.F(zero_trust.AccessRuleAccessCommonNameRuleCommonNameParam{
				CommonName: cf.F(*rule.CommonName),
			}),
		}
	}

	return nil
}

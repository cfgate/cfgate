// convert.go converts between cfgate domain types and cloudflare-go SDK types.
package cloudflare

import (
	"encoding/json"
	"fmt"
	"time"

	cf "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/dns"
	"github.com/cloudflare/cloudflare-go/v6/zero_trust"
)

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

// dnsRecordFromSDK converts a cloudflare-go DNS record to a cfgate DNSRecord.
func dnsRecordFromSDK(r dns.RecordResponse, zoneID string) DNSRecord {
	return DNSRecord{
		ID:      r.ID,
		Type:    string(r.Type),
		Name:    r.Name,
		Content: r.Content,
		TTL:     int(r.TTL),
		Proxied: r.Proxied,
		Comment: r.Comment,
		ZoneID:  zoneID,
	}
}

// ingressOriginRequestToAPI converts OriginRequestConfig to the SDK per-ingress origin request format.
func ingressOriginRequestToAPI(config *OriginRequestConfig) zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest {
	req := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest{}

	if config.ConnectTimeout != "" {
		req.ConnectTimeout = cf.F(parseDurationSeconds(config.ConnectTimeout, 30))
	}
	if config.TLSTimeout != "" {
		req.TLSTimeout = cf.F(parseDurationSeconds(config.TLSTimeout, 10))
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
	if config.MatchSNIToHost {
		req.MatchSnItoHost = cf.F(true)
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
	if config.TLSTimeout != "" {
		req.TLSTimeout = cf.F(parseDurationSeconds(config.TLSTimeout, 10))
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
	if config.MatchSNIToHost {
		req.MatchSnItoHost = cf.F(true)
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

// approvalGroupsToAPI converts internal approval group params to SDK params.
func approvalGroupsToAPI(groups []ApprovalGroupParam) []zero_trust.ApprovalGroupParam {
	if len(groups) == 0 {
		return nil
	}
	result := make([]zero_trust.ApprovalGroupParam, len(groups))
	for i, g := range groups {
		result[i] = zero_trust.ApprovalGroupParam{
			EmailAddresses:  cf.F(g.EmailAddresses),
			EmailListUUID:   cf.F(g.EmailListUUID),
			ApprovalsNeeded: cf.F(float64(g.ApprovalsNeeded)),
		}
	}
	return result
}

// =============================================================================
// Access Application response converters
// =============================================================================

// applicationFromNewResponse converts AccessApplicationNewResponse to AccessApplication.
func applicationFromNewResponse(resp *zero_trust.AccessApplicationNewResponse) (*AccessApplication, error) {
	if resp == nil {
		return nil, nil
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
	if err := applyApplicationExtras(app, resp.JSON.RawJSON()); err != nil {
		return nil, fmt.Errorf("parse access application create response extras: %w", err)
	}

	return app, nil
}

// applicationFromGetResponse converts AccessApplicationGetResponse to AccessApplication.
func applicationFromGetResponse(resp *zero_trust.AccessApplicationGetResponse) (*AccessApplication, error) {
	if resp == nil {
		return nil, nil
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
	if err := applyApplicationExtras(app, resp.JSON.RawJSON()); err != nil {
		return nil, fmt.Errorf("parse access application get response extras: %w", err)
	}

	return app, nil
}

// applicationFromUpdateResponse converts AccessApplicationUpdateResponse to AccessApplication.
func applicationFromUpdateResponse(resp *zero_trust.AccessApplicationUpdateResponse) (*AccessApplication, error) {
	if resp == nil {
		return nil, nil
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
	if err := applyApplicationExtras(app, resp.JSON.RawJSON()); err != nil {
		return nil, fmt.Errorf("parse access application update response extras: %w", err)
	}

	return app, nil
}

// applicationFromListResponse converts AccessApplicationListResponse to AccessApplication.
func applicationFromListResponse(resp *zero_trust.AccessApplicationListResponse) (*AccessApplication, error) {
	if resp == nil {
		return nil, nil
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
	if err := applyApplicationExtras(app, resp.JSON.RawJSON()); err != nil {
		return nil, fmt.Errorf("parse access application list response extras: %w", err)
	}

	return app, nil
}

func applyApplicationExtras(app *AccessApplication, raw string) error {
	if app == nil || raw == "" {
		return nil
	}
	var extra struct {
		Tags         []string `json:"tags"`
		Destinations []struct {
			Type string `json:"type"`
			URI  string `json:"uri"`
		} `json:"destinations"`
		Policies []struct {
			ID         string `json:"id"`
			Precedence int64  `json:"precedence"`
		} `json:"policies"`
	}
	if err := json.Unmarshal([]byte(raw), &extra); err != nil {
		return fmt.Errorf("unmarshal access application extras: %w", err)
	}
	app.Tags = extra.Tags
	for _, destination := range extra.Destinations {
		if destination.Type == "public" && destination.URI != "" {
			app.Destinations = append(app.Destinations, destination.URI)
		}
	}
	for _, policy := range extra.Policies {
		if policy.ID != "" {
			app.Policies = append(app.Policies, ApplicationPolicyLink{
				ID:         policy.ID,
				Precedence: int(policy.Precedence),
			})
		}
	}
	return nil
}

// =============================================================================
// Access Policy response converters
// =============================================================================

// policyFromNewResponse converts AccessPolicyNewResponse to AccessPolicy.
func policyFromNewResponse(resp *zero_trust.AccessPolicyNewResponse) *AccessPolicy {
	if resp == nil {
		return nil
	}

	return &AccessPolicy{
		ID:                           resp.ID,
		Name:                         resp.Name,
		Decision:                     string(resp.Decision),
		Reusable:                     bool(resp.Reusable),
		AppCount:                     resp.AppCount,
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

// policyFromGetResponse converts AccessPolicyGetResponse to AccessPolicy.
func policyFromGetResponse(resp *zero_trust.AccessPolicyGetResponse) *AccessPolicy {
	if resp == nil {
		return nil
	}

	return &AccessPolicy{
		ID:                           resp.ID,
		Name:                         resp.Name,
		Decision:                     string(resp.Decision),
		Reusable:                     bool(resp.Reusable),
		AppCount:                     resp.AppCount,
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

// policyFromUpdateResponse converts AccessPolicyUpdateResponse to AccessPolicy.
func policyFromUpdateResponse(resp *zero_trust.AccessPolicyUpdateResponse) *AccessPolicy {
	if resp == nil {
		return nil
	}

	return &AccessPolicy{
		ID:                           resp.ID,
		Name:                         resp.Name,
		Decision:                     string(resp.Decision),
		Reusable:                     bool(resp.Reusable),
		AppCount:                     resp.AppCount,
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

// policyFromListResponse converts AccessPolicyListResponse to AccessPolicy.
func policyFromListResponse(resp *zero_trust.AccessPolicyListResponse) *AccessPolicy {
	if resp == nil {
		return nil
	}

	return &AccessPolicy{
		ID:                           resp.ID,
		Name:                         resp.Name,
		Decision:                     string(resp.Decision),
		Reusable:                     bool(resp.Reusable),
		AppCount:                     resp.AppCount,
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

// =============================================================================
// Access Group response converters
// =============================================================================

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

// =============================================================================
// Access Rule converters
// =============================================================================

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
	// P3: retained only for SDK round-trip compatibility
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

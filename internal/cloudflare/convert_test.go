package cloudflare

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/cloudflare/cloudflare-go/v6/zero_trust"
)

func TestParseDurationSeconds(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		defaultSec int64
		want       int64
	}{
		{name: "valid duration", input: "45s", defaultSec: 30, want: 45},
		{name: "valid minutes", input: "2m", defaultSec: 30, want: 120},
		{name: "invalid duration falls back", input: "bad", defaultSec: 30, want: 30},
		{name: "zero duration falls back", input: "0s", defaultSec: 30, want: 30},
		{name: "negative duration falls back", input: "-5s", defaultSec: 30, want: 30},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseDurationSeconds(tt.input, tt.defaultSec); got != tt.want {
				t.Fatalf("parseDurationSeconds(%q, %d) = %d, want %d", tt.input, tt.defaultSec, got, tt.want)
			}
		})
	}
}

func TestCORSHeadersFromSDK(t *testing.T) {
	input := &zero_trust.CORSHeaders{
		AllowAllHeaders:  true,
		AllowAllMethods:  false,
		AllowAllOrigins:  true,
		AllowCredentials: true,
		AllowedHeaders:   []zero_trust.AllowedHeaders{"X-Test", "X-Trace"},
		AllowedMethods:   []zero_trust.AllowedMethods{"GET", "POST"},
		AllowedOrigins:   []zero_trust.AllowedOrigins{"https://app.example.com"},
		MaxAge:           600,
	}

	got := corsHeadersFromSDK(input)
	if got == nil {
		t.Fatal("corsHeadersFromSDK() = nil")
		return
	}
	if !got.AllowAllHeaders || !got.AllowAllOrigins || !got.AllowCredentials {
		t.Fatalf("corsHeadersFromSDK() flags = %+v", got)
	}
	if got.MaxAge != 600 {
		t.Fatalf("MaxAge = %d, want 600", got.MaxAge)
	}
	if len(got.AllowedHeaders) != 2 || got.AllowedHeaders[0] != "X-Test" {
		t.Fatalf("AllowedHeaders = %#v", got.AllowedHeaders)
	}
	if len(got.AllowedMethods) != 2 || got.AllowedMethods[1] != "POST" {
		t.Fatalf("AllowedMethods = %#v", got.AllowedMethods)
	}
	if len(got.AllowedOrigins) != 1 || got.AllowedOrigins[0] != "https://app.example.com" {
		t.Fatalf("AllowedOrigins = %#v", got.AllowedOrigins)
	}
}

func TestExtractAllowedIdPs(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
		want  []string
	}{
		{name: "string slice", input: []string{"idp-1", "idp-2"}, want: []string{"idp-1", "idp-2"}},
		{name: "interface slice", input: []interface{}{"idp-1", "idp-2", 42}, want: []string{"idp-1", "idp-2"}},
		{name: "unsupported type", input: 123, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractAllowedIdPs(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("extractAllowedIdPs() = %#v, want %#v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("extractAllowedIdPs() = %#v, want %#v", got, tt.want)
				}
			}
		})
	}
}

func TestApprovalGroupsFromAPI(t *testing.T) {
	input := []zero_trust.ApprovalGroup{
		{
			EmailAddresses:  []string{"alice@example.com", "bob@example.com"},
			EmailListUUID:   "list-1",
			ApprovalsNeeded: 2,
		},
		{
			EmailAddresses:  nil,
			EmailListUUID:   "list-2",
			ApprovalsNeeded: 1,
		},
	}

	got := approvalGroupsFromAPI(input)
	if len(got) != 2 {
		t.Fatalf("len(approvalGroupsFromAPI()) = %d, want 2", len(got))
	}
	if got[0].ApprovalsNeeded != 2 || got[0].EmailListUUID != "list-1" {
		t.Fatalf("first approval group = %+v", got[0])
	}
	if got[1].EmailListUUID != "list-2" {
		t.Fatalf("second approval group = %+v", got[1])
	}
}

func TestAccessRulesToAPIAndFromAPI(t *testing.T) {
	trueValue := true
	outbound := []struct {
		name string
		rule AccessRuleParam
		want string
	}{
		{name: "ip range", rule: AccessRuleParam{IPRange: strPtr("192.0.2.0/24")}, want: `{"ip":{"ip":"192.0.2.0/24"}}`},
		{name: "ip list", rule: AccessRuleParam{IPListID: strPtr("ip-list-1")}, want: `{"ip_list":{"id":"ip-list-1"}}`},
		{name: "country", rule: AccessRuleParam{Country: strPtr("US")}, want: `{"geo":{"country_code":"US"}}`},
		{name: "everyone", rule: AccessRuleParam{Everyone: &trueValue}, want: `{"everyone":{}}`},
		{name: "service token", rule: AccessRuleParam{ServiceTokenID: strPtr("token-1")}, want: `{"service_token":{"token_id":"token-1"}}`},
		{name: "any valid service token", rule: AccessRuleParam{AnyValidServiceToken: &trueValue}, want: `{"any_valid_service_token":{}}`},
		{name: "email", rule: AccessRuleParam{Email: strPtr("user@example.com")}, want: `{"email":{"email":"user@example.com"}}`},
		{name: "email list", rule: AccessRuleParam{EmailListID: strPtr("email-list-1")}, want: `{"email_list":{"id":"email-list-1"}}`},
		{name: "email domain", rule: AccessRuleParam{EmailDomain: strPtr("example.com")}, want: `{"email_domain":{"domain":"example.com"}}`},
		{
			name: "oidc claim",
			rule: AccessRuleParam{OIDCClaim: &OIDCClaimParam{IdentityProviderID: "idp-1", ClaimName: "groups", ClaimValue: "eng"}},
			want: `{"oidc":{"claim_name":"groups","claim_value":"eng","identity_provider_id":"idp-1"}}`,
		},
		{
			name: "gsuite group",
			rule: AccessRuleParam{GSuiteGroup: &GSuiteGroupParam{IdentityProviderID: "idp-1", Email: "eng@example.com"}},
			want: `{"gsuite":{"email":"eng@example.com","identity_provider_id":"idp-1"}}`,
		},
		{name: "certificate", rule: AccessRuleParam{Certificate: &trueValue}, want: `{"certificate":{}}`},
		{name: "common name", rule: AccessRuleParam{CommonName: strPtr("client.example.com")}, want: `{"common_name":{"common_name":"client.example.com"}}`},
		{name: "group", rule: AccessRuleParam{GroupID: strPtr("group-1")}, want: `{"group":{"id":"group-1"}}`},
	}

	for _, tt := range outbound {
		t.Run("outbound "+tt.name, func(t *testing.T) {
			got := accessRuleToAPI(&tt.rule)
			assertJSONEqual(t, got, tt.want)
		})
	}
	if got := accessRuleToAPI(nil); got != nil {
		t.Fatalf("accessRuleToAPI(nil) = %#v, want nil", got)
	}
	if got := accessRuleToAPI(&AccessRuleParam{}); got != nil {
		t.Fatalf("accessRuleToAPI(empty) = %#v, want nil", got)
	}
	if got := accessRulesToAPI(nil); got != nil {
		t.Fatalf("accessRulesToAPI(nil) = %#v, want nil", got)
	}
	if got := accessRulesToAPI([]AccessRuleParam{{}}); len(got) != 0 {
		t.Fatalf("accessRulesToAPI(empty rule) len = %d, want 0", len(got))
	}

	inbound := []struct {
		name string
		raw  string
		want AccessRuleParam
	}{
		{name: "ip range", raw: `{"ip":{"ip":"192.0.2.0/24"}}`, want: AccessRuleParam{IPRange: strPtr("192.0.2.0/24")}},
		{name: "ip list", raw: `{"ip_list":{"id":"ip-list-1"}}`, want: AccessRuleParam{IPListID: strPtr("ip-list-1")}},
		{name: "country", raw: `{"geo":{"country_code":"US"}}`, want: AccessRuleParam{Country: strPtr("US")}},
		{name: "everyone", raw: `{"everyone":{}}`, want: AccessRuleParam{Everyone: &trueValue}},
		{name: "service token", raw: `{"service_token":{"token_id":"token-1"}}`, want: AccessRuleParam{ServiceTokenID: strPtr("token-1")}},
		{name: "any valid service token", raw: `{"any_valid_service_token":{}}`, want: AccessRuleParam{AnyValidServiceToken: &trueValue}},
		{name: "email", raw: `{"email":{"email":"user@example.com"}}`, want: AccessRuleParam{Email: strPtr("user@example.com")}},
		{name: "email list", raw: `{"email_list":{"id":"email-list-1"}}`, want: AccessRuleParam{EmailListID: strPtr("email-list-1")}},
		{name: "email domain", raw: `{"email_domain":{"domain":"example.com"}}`, want: AccessRuleParam{EmailDomain: strPtr("example.com")}},
		{
			name: "oidc claim",
			raw:  `{"oidc":{"claim_name":"groups","claim_value":"eng","identity_provider_id":"idp-1"}}`,
			want: AccessRuleParam{OIDCClaim: &OIDCClaimParam{IdentityProviderID: "idp-1", ClaimName: "groups", ClaimValue: "eng"}},
		},
		{
			name: "gsuite group",
			raw:  `{"gsuite":{"email":"eng@example.com","identity_provider_id":"idp-1"}}`,
			want: AccessRuleParam{GSuiteGroup: &GSuiteGroupParam{IdentityProviderID: "idp-1", Email: "eng@example.com"}},
		},
		{name: "certificate", raw: `{"certificate":{}}`, want: AccessRuleParam{Certificate: &trueValue}},
		{name: "common name", raw: `{"common_name":{"common_name":"client.example.com"}}`, want: AccessRuleParam{CommonName: strPtr("client.example.com")}},
		{name: "group", raw: `{"group":{"id":"group-1"}}`, want: AccessRuleParam{GroupID: strPtr("group-1")}},
	}

	for _, tt := range inbound {
		t.Run("inbound "+tt.name, func(t *testing.T) {
			rule := sdkAccessRule(t, tt.raw)
			got := accessRuleFromAPI(&rule)
			if !reflect.DeepEqual(got, &tt.want) {
				t.Fatalf("accessRuleFromAPI() = %#v, want %#v", got, &tt.want)
			}
		})
	}
	if got := accessRuleFromAPI(nil); got != nil {
		t.Fatalf("accessRuleFromAPI(nil) = %#v, want nil", got)
	}
	if got := accessRulesFromAPI(nil); got != nil {
		t.Fatalf("accessRulesFromAPI(nil) = %#v, want nil", got)
	}
	if got := accessRulesFromAPI([]zero_trust.AccessRule{{}}); got != nil {
		t.Fatalf("accessRulesFromAPI(unrecognized) = %#v, want nil", got)
	}
}

func TestApprovalGroupsToAPI(t *testing.T) {
	if got := approvalGroupsToAPI(nil); got != nil {
		t.Fatalf("approvalGroupsToAPI(nil) = %#v, want nil", got)
	}
	got := approvalGroupsToAPI([]ApprovalGroupParam{{
		EmailAddresses:  []string{"alice@example.com", "bob@example.com"},
		EmailListUUID:   "list-1",
		ApprovalsNeeded: 2,
	}})
	if len(got) != 1 {
		t.Fatalf("len(approvalGroupsToAPI()) = %d, want 1", len(got))
	}
	if !reflect.DeepEqual(got[0].EmailAddresses.Value, []string{"alice@example.com", "bob@example.com"}) {
		t.Fatalf("EmailAddresses = %#v", got[0].EmailAddresses.Value)
	}
	if got[0].EmailListUUID.Value != "list-1" || got[0].ApprovalsNeeded.Value != 2 {
		t.Fatalf("approval group = %+v", got[0])
	}
}

func TestApplicationResponseConverters(t *testing.T) {
	resp := zero_trust.AccessApplicationNewResponse{
		ID:                          "app-1",
		AUD:                         "aud-1",
		Name:                        "app",
		Domain:                      "app.example.com",
		Type:                        zero_trust.ApplicationTypeSelfHosted,
		SessionDuration:             "24h",
		AllowedIdPs:                 []interface{}{"idp-1", "idp-2"},
		AutoRedirectToIdentity:      true,
		EnableBindingCookie:         true,
		HTTPOnlyCookieAttribute:     true,
		SameSiteCookieAttribute:     "lax",
		SkipInterstitial:            true,
		LogoURL:                     "https://example.com/logo.png",
		AppLauncherVisible:          true,
		CustomDenyMessage:           "denied",
		CustomDenyURL:               "https://example.com/deny",
		OptionsPreflightBypass:      true,
		PathCookieAttribute:         true,
		ServiceAuth401Redirect:      true,
		CustomNonIdentityDenyURL:    "https://example.com/service-deny",
		ReadServiceTokensFromHeader: "X-CF-Service-Token",
		CORSHeaders: zero_trust.CORSHeaders{
			AllowAllHeaders:  true,
			AllowAllMethods:  true,
			AllowAllOrigins:  true,
			AllowCredentials: true,
			AllowedHeaders:   []zero_trust.AllowedHeaders{"Authorization"},
			AllowedMethods:   []zero_trust.AllowedMethods{"GET"},
			AllowedOrigins:   []zero_trust.AllowedOrigins{"https://app.example.com"},
			MaxAge:           300,
		},
	}

	assertApplication(t, applicationFromNewResponse(&resp))
	assertApplication(t, applicationFromGetResponse(&zero_trust.AccessApplicationGetResponse{
		ID:                          resp.ID,
		AUD:                         resp.AUD,
		Name:                        resp.Name,
		Domain:                      resp.Domain,
		Type:                        resp.Type,
		SessionDuration:             resp.SessionDuration,
		AllowedIdPs:                 []string{"idp-1", "idp-2"},
		AutoRedirectToIdentity:      resp.AutoRedirectToIdentity,
		EnableBindingCookie:         resp.EnableBindingCookie,
		HTTPOnlyCookieAttribute:     resp.HTTPOnlyCookieAttribute,
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
		CORSHeaders:                 resp.CORSHeaders,
	}))
	assertApplication(t, applicationFromUpdateResponse(&zero_trust.AccessApplicationUpdateResponse{
		ID:                          resp.ID,
		AUD:                         resp.AUD,
		Name:                        resp.Name,
		Domain:                      resp.Domain,
		Type:                        resp.Type,
		SessionDuration:             resp.SessionDuration,
		AllowedIdPs:                 []string{"idp-1", "idp-2"},
		AutoRedirectToIdentity:      resp.AutoRedirectToIdentity,
		EnableBindingCookie:         resp.EnableBindingCookie,
		HTTPOnlyCookieAttribute:     resp.HTTPOnlyCookieAttribute,
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
		CORSHeaders:                 resp.CORSHeaders,
	}))
	assertApplication(t, applicationFromListResponse(&zero_trust.AccessApplicationListResponse{
		ID:                          resp.ID,
		AUD:                         resp.AUD,
		Name:                        resp.Name,
		Domain:                      resp.Domain,
		Type:                        resp.Type,
		SessionDuration:             resp.SessionDuration,
		AllowedIdPs:                 []string{"idp-1", "idp-2"},
		AutoRedirectToIdentity:      resp.AutoRedirectToIdentity,
		EnableBindingCookie:         resp.EnableBindingCookie,
		HTTPOnlyCookieAttribute:     resp.HTTPOnlyCookieAttribute,
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
		CORSHeaders:                 resp.CORSHeaders,
	}))

	if applicationFromNewResponse(nil) != nil || applicationFromGetResponse(nil) != nil ||
		applicationFromUpdateResponse(nil) != nil || applicationFromListResponse(nil) != nil {
		t.Fatal("nil application response converted to non-nil app")
	}
}

func TestApplyApplicationExtras(t *testing.T) {
	app := &AccessApplication{}
	applyApplicationExtras(app, `{
		"tags":["cfgate","owner"],
		"destinations":[
			{"type":"public","uri":"app.example.com"},
			{"type":"private","uri":"ignored.example.com"},
			{"type":"public","uri":""}
		],
		"policies":[{"id":"policy-1","precedence":3},{"id":"","precedence":9}]
	}`)
	if !reflect.DeepEqual(app.Tags, []string{"cfgate", "owner"}) {
		t.Fatalf("Tags = %#v", app.Tags)
	}
	if !reflect.DeepEqual(app.Destinations, []string{"app.example.com"}) {
		t.Fatalf("Destinations = %#v", app.Destinations)
	}
	if !reflect.DeepEqual(app.Policies, []ApplicationPolicyLink{{ID: "policy-1", Precedence: 3}}) {
		t.Fatalf("Policies = %#v", app.Policies)
	}
	applyApplicationExtras(app, `bad-json`)
	if !reflect.DeepEqual(app.Tags, []string{"cfgate", "owner"}) {
		t.Fatalf("malformed JSON changed app: %#v", app.Tags)
	}
	applyApplicationExtras(nil, `{"tags":["ignored"]}`)
}

func TestPolicyAndGroupResponseConverters(t *testing.T) {
	include := []zero_trust.AccessRule{sdkAccessRule(t, `{"everyone":{}}`)}
	approvals := []zero_trust.ApprovalGroup{{EmailAddresses: []string{"alice@example.com"}, EmailListUUID: "list-1", ApprovalsNeeded: 2}}

	assertPolicy(t, policyFromNewResponse(&zero_trust.AccessPolicyNewResponse{
		ID: "policy-1", Name: "policy", Decision: zero_trust.DecisionAllow, Reusable: true, AppCount: 4,
		Include: include, ApprovalGroups: approvals, ApprovalRequired: true, SessionDuration: "24h",
		PurposeJustificationRequired: true, PurposeJustificationPrompt: "why",
	}))
	assertPolicy(t, policyFromGetResponse(&zero_trust.AccessPolicyGetResponse{
		ID: "policy-1", Name: "policy", Decision: zero_trust.DecisionAllow, Reusable: true, AppCount: 4,
		Include: include, ApprovalGroups: approvals, ApprovalRequired: true, SessionDuration: "24h",
		PurposeJustificationRequired: true, PurposeJustificationPrompt: "why",
	}))
	assertPolicy(t, policyFromUpdateResponse(&zero_trust.AccessPolicyUpdateResponse{
		ID: "policy-1", Name: "policy", Decision: zero_trust.DecisionAllow, Reusable: true, AppCount: 4,
		Include: include, ApprovalGroups: approvals, ApprovalRequired: true, SessionDuration: "24h",
		PurposeJustificationRequired: true, PurposeJustificationPrompt: "why",
	}))
	assertPolicy(t, policyFromListResponse(&zero_trust.AccessPolicyListResponse{
		ID: "policy-1", Name: "policy", Decision: zero_trust.DecisionAllow, Reusable: true, AppCount: 4,
		Include: include, ApprovalGroups: approvals, ApprovalRequired: true, SessionDuration: "24h",
		PurposeJustificationRequired: true, PurposeJustificationPrompt: "why",
	}))

	if policyFromNewResponse(nil) != nil || policyFromGetResponse(nil) != nil ||
		policyFromUpdateResponse(nil) != nil || policyFromListResponse(nil) != nil {
		t.Fatal("nil policy response converted to non-nil policy")
	}

	assertGroup(t, groupFromNewResponse(&zero_trust.AccessGroupNewResponse{ID: "group-1", Name: "group"}))
	assertGroup(t, groupFromGetResponse(&zero_trust.AccessGroupGetResponse{ID: "group-1", Name: "group"}))
	assertGroup(t, groupFromUpdateResponse(&zero_trust.AccessGroupUpdateResponse{ID: "group-1", Name: "group"}))
	assertGroup(t, groupFromListResponse(&zero_trust.AccessGroupListResponse{ID: "group-1", Name: "group"}))
	if groupFromNewResponse(nil) != nil || groupFromGetResponse(nil) != nil ||
		groupFromUpdateResponse(nil) != nil || groupFromListResponse(nil) != nil {
		t.Fatal("nil group response converted to non-nil group")
	}
}

func TestOriginRequestConverters(t *testing.T) {
	config := &OriginRequestConfig{
		ConnectTimeout:         "45s",
		NoHappyEyeballs:        true,
		KeepAliveConnections:   7,
		HTTPHostHeader:         "origin.example.com",
		OriginServerName:       "server.example.com",
		CAPool:                 "pool-1",
		NoTLSVerify:            true,
		DisableChunkedEncoding: true,
		HTTP2Origin:            true,
	}
	ingress := ingressOriginRequestToAPI(config)
	if ingress.ConnectTimeout.Value != 45 || !ingress.NoHappyEyeballs.Value || ingress.KeepAliveConnections.Value != 7 ||
		ingress.HTTPHostHeader.Value != "origin.example.com" || ingress.OriginServerName.Value != "server.example.com" ||
		ingress.CAPool.Value != "pool-1" || !ingress.NoTLSVerify.Value || !ingress.DisableChunkedEncoding.Value || !ingress.HTTP2Origin.Value {
		t.Fatalf("ingressOriginRequestToAPI() = %+v", ingress)
	}
	global := globalOriginRequestToAPI(config)
	if global.ConnectTimeout.Value != 45 || !global.NoHappyEyeballs.Value || global.KeepAliveConnections.Value != 7 ||
		global.HTTPHostHeader.Value != "origin.example.com" || global.OriginServerName.Value != "server.example.com" ||
		global.CAPool.Value != "pool-1" || !global.NoTLSVerify.Value || !global.DisableChunkedEncoding.Value || !global.HTTP2Origin.Value {
		t.Fatalf("globalOriginRequestToAPI() = %+v", global)
	}
	config.ConnectTimeout = "not-a-duration"
	if got := ingressOriginRequestToAPI(config).ConnectTimeout.Value; got != 30 {
		t.Fatalf("invalid ingress ConnectTimeout = %d, want 30", got)
	}
	if got := globalOriginRequestToAPI(config).ConnectTimeout.Value; got != 30 {
		t.Fatalf("invalid global ConnectTimeout = %d, want 30", got)
	}
}

func sdkAccessRule(t *testing.T, raw string) zero_trust.AccessRule {
	t.Helper()
	var rule zero_trust.AccessRule
	if err := json.Unmarshal([]byte(raw), &rule); err != nil {
		t.Fatalf("Unmarshal AccessRule %s: %v", raw, err)
	}
	return rule
}

func assertJSONEqual(t *testing.T, got interface{}, want string) {
	t.Helper()
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var gotJSON interface{}
	var wantJSON interface{}
	if err := json.Unmarshal(data, &gotJSON); err != nil {
		t.Fatalf("Unmarshal got JSON %s: %v", data, err)
	}
	if err := json.Unmarshal([]byte(want), &wantJSON); err != nil {
		t.Fatalf("Unmarshal want JSON %s: %v", want, err)
	}
	if !reflect.DeepEqual(gotJSON, wantJSON) {
		t.Fatalf("JSON = %s, want %s", data, want)
	}
}

func assertApplication(t *testing.T, app *AccessApplication) {
	t.Helper()
	if app == nil {
		t.Fatal("application = nil")
	}
	if app.ID != "app-1" || app.AUD != "aud-1" || app.Name != "app" || app.Domain != "app.example.com" ||
		app.Type != "self_hosted" || app.SessionDuration != "24h" || !reflect.DeepEqual(app.AllowedIdps, []string{"idp-1", "idp-2"}) ||
		!app.AutoRedirectToIdentity || !app.EnableBindingCookie || !app.HttpOnlyCookieAttribute ||
		app.SameSiteCookieAttribute != "lax" || !app.SkipInterstitial || app.LogoURL != "https://example.com/logo.png" ||
		!app.AppLauncherVisible || app.CustomDenyMessage != "denied" || app.CustomDenyURL != "https://example.com/deny" ||
		!app.OptionsPreflightBypass || !app.PathCookieAttribute || !app.ServiceAuth401Redirect ||
		app.CustomNonIdentityDenyURL != "https://example.com/service-deny" ||
		app.ReadServiceTokensFromHeader != "X-CF-Service-Token" {
		t.Fatalf("application fields = %+v", app)
	}
	if app.CORSHeaders == nil || !app.CORSHeaders.AllowAllHeaders || app.CORSHeaders.MaxAge != 300 ||
		!reflect.DeepEqual(app.CORSHeaders.AllowedHeaders, []string{"Authorization"}) {
		t.Fatalf("CORSHeaders = %+v", app.CORSHeaders)
	}
}

func assertPolicy(t *testing.T, policy *AccessPolicy) {
	t.Helper()
	if policy == nil {
		t.Fatal("policy = nil")
	}
	if policy.ID != "policy-1" || policy.Name != "policy" || policy.Decision != "allow" ||
		!policy.Reusable || policy.AppCount != 4 || !policy.ApprovalRequired ||
		policy.SessionDuration != "24h" || !policy.PurposeJustificationRequired ||
		policy.PurposeJustificationPrompt != "why" {
		t.Fatalf("policy fields = %+v", policy)
	}
	if len(policy.Include) != 1 || policy.Include[0].Everyone == nil || !*policy.Include[0].Everyone {
		t.Fatalf("Include = %#v", policy.Include)
	}
	if len(policy.ApprovalGroups) != 1 || policy.ApprovalGroups[0].EmailListUUID != "list-1" ||
		policy.ApprovalGroups[0].ApprovalsNeeded != 2 {
		t.Fatalf("ApprovalGroups = %#v", policy.ApprovalGroups)
	}
}

func assertGroup(t *testing.T, group *AccessGroup) {
	t.Helper()
	if group == nil || group.ID != "group-1" || group.Name != "group" {
		t.Fatalf("group = %+v, want group-1/group", group)
	}
}

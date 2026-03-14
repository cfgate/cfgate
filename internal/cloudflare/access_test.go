package cloudflare

import (
	"testing"
)

func boolPtr(b bool) *bool    { return &b }
func strPtr(s string) *string { return &s }

func makeMatchingAppPair() (*AccessApplication, *ApplicationParams) {
	httpOnly := true
	return &AccessApplication{
			Name:                        "Test App",
			Domain:                      "app.example.com",
			Type:                        "self_hosted",
			SessionDuration:             "24h",
			AllowedIdps:                 []string{"idp-1", "idp-2"},
			AutoRedirectToIdentity:      false,
			EnableBindingCookie:         false,
			HttpOnlyCookieAttribute:     true,
			SameSiteCookieAttribute:     "lax",
			SkipInterstitial:            false,
			LogoURL:                     "",
			AppLauncherVisible:          false,
			CustomDenyMessage:           "",
			CustomDenyURL:               "",
			CustomNonIdentityDenyURL:    "",
			CORSHeaders:                 nil,
			OptionsPreflightBypass:      false,
			PathCookieAttribute:         false,
			ServiceAuth401Redirect:      false,
			ReadServiceTokensFromHeader: "",
		}, &ApplicationParams{
			Name:                        "Test App",
			Domain:                      "app.example.com",
			Type:                        "self_hosted",
			SessionDuration:             "24h",
			AllowedIdps:                 []string{"idp-1", "idp-2"},
			AutoRedirectToIdentity:      false,
			EnableBindingCookie:         false,
			HttpOnlyCookieAttribute:     &httpOnly,
			SameSiteCookieAttribute:     "lax",
			SkipInterstitial:            false,
			LogoURL:                     "",
			AppLauncherVisible:          false,
			CustomDenyMessage:           "",
			CustomDenyURL:               "",
			CustomNonIdentityDenyURL:    "",
			CORSHeaders:                 nil,
			OptionsPreflightBypass:      false,
			PathCookieAttribute:         false,
			ServiceAuth401Redirect:      false,
			ReadServiceTokensFromHeader: "",
		}
}

func makeMatchingPolicyPair() (*AccessPolicy, *PolicyParams) {
	return &AccessPolicy{
			Name:                         "allow-engineering",
			Decision:                     "allow",
			Precedence:                   1,
			Include:                      []AccessRuleParam{{Everyone: boolPtr(true)}},
			Exclude:                      nil,
			Require:                      nil,
			SessionDuration:              "24h",
			PurposeJustificationRequired: false,
			PurposeJustificationPrompt:   "",
			ApprovalRequired:             false,
			ApprovalGroups:               nil,
		}, &PolicyParams{
			Name:                         "allow-engineering",
			Decision:                     "allow",
			Precedence:                   1,
			Include:                      []AccessRuleParam{{Everyone: boolPtr(true)}},
			Exclude:                      nil,
			Require:                      nil,
			SessionDuration:              "24h",
			PurposeJustificationRequired: false,
			PurposeJustificationPrompt:   "",
			ApprovalRequired:             false,
			ApprovalGroups:               nil,
		}
}

func TestAccessApplicationNeedsUpdate(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*AccessApplication, *ApplicationParams)
		want   bool
	}{
		{
			name:   "all fields match",
			modify: func(_ *AccessApplication, _ *ApplicationParams) {},
			want:   false,
		},

		// Per-field drift detection (20 fields)

		{
			name:   "Name drift",
			modify: func(_ *AccessApplication, p *ApplicationParams) { p.Name = "Different" },
			want:   true,
		},
		{
			name:   "Domain drift",
			modify: func(_ *AccessApplication, p *ApplicationParams) { p.Domain = "other.example.com" },
			want:   true,
		},
		{
			name:   "Type drift explicit",
			modify: func(_ *AccessApplication, p *ApplicationParams) { p.Type = "ssh" },
			want:   true,
		},
		{
			name:   "SessionDuration drift explicit",
			modify: func(_ *AccessApplication, p *ApplicationParams) { p.SessionDuration = "12h" },
			want:   true,
		},
		{
			name:   "SkipInterstitial drift",
			modify: func(_ *AccessApplication, p *ApplicationParams) { p.SkipInterstitial = true },
			want:   true,
		},
		{
			name:   "EnableBindingCookie drift",
			modify: func(_ *AccessApplication, p *ApplicationParams) { p.EnableBindingCookie = true },
			want:   true,
		},
		{
			name:   "AutoRedirectToIdentity drift",
			modify: func(_ *AccessApplication, p *ApplicationParams) { p.AutoRedirectToIdentity = true },
			want:   true,
		},
		{
			name:   "AppLauncherVisible drift",
			modify: func(_ *AccessApplication, p *ApplicationParams) { p.AppLauncherVisible = true },
			want:   true,
		},
		{
			name:   "LogoURL drift",
			modify: func(_ *AccessApplication, p *ApplicationParams) { p.LogoURL = "https://logo.example.com/icon.png" },
			want:   true,
		},
		{
			name:   "CustomDenyMessage drift",
			modify: func(_ *AccessApplication, p *ApplicationParams) { p.CustomDenyMessage = "Access denied" },
			want:   true,
		},
		{
			name:   "CustomDenyURL drift",
			modify: func(_ *AccessApplication, p *ApplicationParams) { p.CustomDenyURL = "https://deny.example.com" },
			want:   true,
		},
		{
			name: "CustomNonIdentityDenyURL drift",
			modify: func(_ *AccessApplication, p *ApplicationParams) {
				p.CustomNonIdentityDenyURL = "https://noid.example.com"
			},
			want: true,
		},
		{
			name:   "OptionsPreflightBypass drift",
			modify: func(_ *AccessApplication, p *ApplicationParams) { p.OptionsPreflightBypass = true },
			want:   true,
		},
		{
			name:   "PathCookieAttribute drift",
			modify: func(_ *AccessApplication, p *ApplicationParams) { p.PathCookieAttribute = true },
			want:   true,
		},
		{
			name:   "ServiceAuth401Redirect drift",
			modify: func(_ *AccessApplication, p *ApplicationParams) { p.ServiceAuth401Redirect = true },
			want:   true,
		},
		{
			name:   "ReadServiceTokensFromHeader drift",
			modify: func(_ *AccessApplication, p *ApplicationParams) { p.ReadServiceTokensFromHeader = "X-Custom-Token" },
			want:   true,
		},
		{
			name:   "SameSiteCookieAttribute drift explicit",
			modify: func(_ *AccessApplication, p *ApplicationParams) { p.SameSiteCookieAttribute = "strict" },
			want:   true,
		},
		{
			name:   "HttpOnlyCookieAttribute drift to false",
			modify: func(_ *AccessApplication, p *ApplicationParams) { p.HttpOnlyCookieAttribute = boolPtr(false) },
			want:   true,
		},
		{
			name:   "AllowedIdps drift different values",
			modify: func(_ *AccessApplication, p *ApplicationParams) { p.AllowedIdps = []string{"idp-3"} },
			want:   true,
		},
		{
			name: "CORSHeaders drift nil to non-nil",
			modify: func(_ *AccessApplication, p *ApplicationParams) {
				p.CORSHeaders = &CORSHeadersParam{AllowAllOrigins: true}
			},
			want: true,
		},

		// Default value behavior

		{
			name: "Type empty defaults to self_hosted matches existing",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.Type = "self_hosted"
				p.Type = ""
			},
			want: false,
		},
		{
			name: "Type empty defaults to self_hosted differs from ssh",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.Type = "ssh"
				p.Type = ""
			},
			want: true,
		},
		{
			name: "SessionDuration empty defaults to 24h matches existing",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.SessionDuration = "24h"
				p.SessionDuration = ""
			},
			want: false,
		},
		{
			name: "SessionDuration empty defaults to 24h differs from 12h",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.SessionDuration = "12h"
				p.SessionDuration = ""
			},
			want: true,
		},
		{
			name: "SameSite empty defaults to lax matches existing",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.SameSiteCookieAttribute = "lax"
				p.SameSiteCookieAttribute = ""
			},
			want: false,
		},
		{
			name: "SameSite empty defaults to lax differs from strict",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.SameSiteCookieAttribute = "strict"
				p.SameSiteCookieAttribute = ""
			},
			want: true,
		},
		{
			name: "HttpOnly nil defaults to true matches existing true",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.HttpOnlyCookieAttribute = true
				p.HttpOnlyCookieAttribute = nil
			},
			want: false,
		},
		{
			name: "HttpOnly nil defaults to true differs from existing false",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.HttpOnlyCookieAttribute = false
				p.HttpOnlyCookieAttribute = nil
			},
			want: true,
		},
		{
			name: "HttpOnly explicit false matches existing false",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.HttpOnlyCookieAttribute = false
				p.HttpOnlyCookieAttribute = boolPtr(false)
			},
			want: false,
		},

		// AllowedIdps edge cases

		{
			name: "AllowedIdps both nil",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.AllowedIdps = nil
				p.AllowedIdps = nil
			},
			want: false,
		},
		{
			name: "AllowedIdps nil vs empty",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.AllowedIdps = nil
				p.AllowedIdps = []string{}
			},
			want: false,
		},
		{
			name: "AllowedIdps empty vs nil",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.AllowedIdps = []string{}
				p.AllowedIdps = nil
			},
			want: false,
		},
		{
			name: "AllowedIdps same elements different order",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.AllowedIdps = []string{"idp-a", "idp-b", "idp-c"}
				p.AllowedIdps = []string{"idp-c", "idp-a", "idp-b"}
			},
			want: false,
		},
		{
			name: "AllowedIdps subset is drift",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.AllowedIdps = []string{"idp-1", "idp-2"}
				p.AllowedIdps = []string{"idp-1"}
			},
			want: true,
		},

		// CORSHeaders edge cases

		{
			name: "CORSHeaders both nil",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.CORSHeaders = nil
				p.CORSHeaders = nil
			},
			want: false,
		},
		{
			name: "CORSHeaders nil vs empty struct",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.CORSHeaders = nil
				p.CORSHeaders = &CORSHeadersParam{}
			},
			want: true,
		},
		{
			name: "CORSHeaders empty struct vs nil",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.CORSHeaders = &CORSHeadersParam{}
				p.CORSHeaders = nil
			},
			want: true,
		},
		{
			name: "CORSHeaders matching non-nil",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.CORSHeaders = &CORSHeadersParam{AllowAllOrigins: true, MaxAge: 3600}
				p.CORSHeaders = &CORSHeadersParam{AllowAllOrigins: true, MaxAge: 3600}
			},
			want: false,
		},
		{
			name: "CORSHeaders bool field differs",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.CORSHeaders = &CORSHeadersParam{AllowAllOrigins: true}
				p.CORSHeaders = &CORSHeadersParam{AllowAllOrigins: false}
			},
			want: true,
		},
		{
			name: "CORSHeaders MaxAge differs",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.CORSHeaders = &CORSHeadersParam{MaxAge: 3600}
				p.CORSHeaders = &CORSHeadersParam{MaxAge: 7200}
			},
			want: true,
		},
		{
			name: "CORSHeaders AllowedOrigins order insensitive",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.CORSHeaders = &CORSHeadersParam{AllowedOrigins: []string{"https://a.com", "https://b.com"}}
				p.CORSHeaders = &CORSHeadersParam{AllowedOrigins: []string{"https://b.com", "https://a.com"}}
			},
			want: false,
		},
		{
			name: "CORSHeaders AllowedOrigins different values is drift",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.CORSHeaders = &CORSHeadersParam{AllowedOrigins: []string{"https://a.com"}}
				p.CORSHeaders = &CORSHeadersParam{AllowedOrigins: []string{"https://b.com"}}
			},
			want: true,
		},
		{
			name: "CORSHeaders AllowedMethods order insensitive",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.CORSHeaders = &CORSHeadersParam{AllowedMethods: []string{"GET", "POST", "PUT"}}
				p.CORSHeaders = &CORSHeadersParam{AllowedMethods: []string{"PUT", "GET", "POST"}}
			},
			want: false,
		},
		{
			name: "CORSHeaders AllowedHeaders order insensitive",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.CORSHeaders = &CORSHeadersParam{AllowedHeaders: []string{"Content-Type", "Authorization"}}
				p.CORSHeaders = &CORSHeadersParam{AllowedHeaders: []string{"Authorization", "Content-Type"}}
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			existing, desired := makeMatchingAppPair()
			tt.modify(existing, desired)
			got := accessApplicationNeedsUpdate(existing, desired)
			if got != tt.want {
				t.Errorf("accessApplicationNeedsUpdate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAccessPolicyEqual(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*AccessPolicy, *PolicyParams)
		want   bool
	}{
		{
			name:   "all fields match",
			modify: func(_ *AccessPolicy, _ *PolicyParams) {},
			want:   true,
		},

		// Per-field drift detection

		{
			name:   "Name drift",
			modify: func(_ *AccessPolicy, d *PolicyParams) { d.Name = "different" },
			want:   false,
		},
		{
			name:   "Decision drift",
			modify: func(_ *AccessPolicy, d *PolicyParams) { d.Decision = "deny" },
			want:   false,
		},
		{
			name:   "Precedence drift",
			modify: func(_ *AccessPolicy, d *PolicyParams) { d.Precedence = 2 },
			want:   false,
		},
		{
			name:   "SessionDuration drift",
			modify: func(_ *AccessPolicy, d *PolicyParams) { d.SessionDuration = "12h" },
			want:   false,
		},
		{
			name: "SessionDuration empty vs set detects drift",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.SessionDuration = ""
				d.SessionDuration = "24h"
			},
			want: false,
		},
		{
			name:   "PurposeJustificationRequired drift",
			modify: func(_ *AccessPolicy, d *PolicyParams) { d.PurposeJustificationRequired = true },
			want:   false,
		},
		{
			name:   "PurposeJustificationPrompt drift",
			modify: func(_ *AccessPolicy, d *PolicyParams) { d.PurposeJustificationPrompt = "Why?" },
			want:   false,
		},
		{
			name:   "ApprovalRequired drift",
			modify: func(_ *AccessPolicy, d *PolicyParams) { d.ApprovalRequired = true },
			want:   false,
		},

		// Rule slice drift

		{
			name: "Include drift different rule",
			modify: func(_ *AccessPolicy, d *PolicyParams) {
				d.Include = []AccessRuleParam{{IPRange: strPtr("10.0.0.0/8")}}
			},
			want: false,
		},
		{
			name: "Include drift added rule",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				extra := AccessRuleParam{IPRange: strPtr("10.0.0.0/8")}
				p.Include = append(p.Include, extra)
			},
			want: false,
		},
		{
			name: "Include drift removed all rules",
			modify: func(_ *AccessPolicy, d *PolicyParams) {
				d.Include = nil
			},
			want: false,
		},
		{
			name: "Exclude drift nil to non-nil",
			modify: func(_ *AccessPolicy, d *PolicyParams) {
				d.Exclude = []AccessRuleParam{{Country: strPtr("US")}}
			},
			want: false,
		},
		{
			name: "Require drift nil to non-nil",
			modify: func(_ *AccessPolicy, d *PolicyParams) {
				d.Require = []AccessRuleParam{{Certificate: boolPtr(true)}}
			},
			want: false,
		},
		{
			name: "ApprovalGroups drift nil to non-nil",
			modify: func(_ *AccessPolicy, d *PolicyParams) {
				d.ApprovalGroups = []ApprovalGroupParam{
					{EmailAddresses: []string{"a@b.com"}, ApprovalsNeeded: 1},
				}
			},
			want: false,
		},

		// Nil vs empty slice equivalence

		{
			name: "Include nil vs nil",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = nil
				d.Include = nil
			},
			want: true,
		},
		{
			name: "Include nil vs empty",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = nil
				d.Include = []AccessRuleParam{}
			},
			want: true,
		},
		{
			name: "Exclude nil vs empty",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Exclude = nil
				d.Exclude = []AccessRuleParam{}
			},
			want: true,
		},
		{
			name: "ApprovalGroups nil vs empty",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.ApprovalGroups = nil
				d.ApprovalGroups = []ApprovalGroupParam{}
			},
			want: true,
		},

		// Order insensitivity for rule slices

		{
			name: "Include same rules different order",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				ruleA := AccessRuleParam{IPRange: strPtr("10.0.0.0/8")}
				ruleB := AccessRuleParam{Email: strPtr("a@b.com")}
				p.Include = []AccessRuleParam{ruleA, ruleB}
				d.Include = []AccessRuleParam{ruleB, ruleA}
			},
			want: true,
		},
		{
			name: "Exclude same rules different order",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				ruleA := AccessRuleParam{Country: strPtr("US")}
				ruleB := AccessRuleParam{Country: strPtr("GB")}
				p.Exclude = []AccessRuleParam{ruleA, ruleB}
				d.Exclude = []AccessRuleParam{ruleB, ruleA}
			},
			want: true,
		},
		{
			name: "ApprovalGroups same groups different order",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				groupA := ApprovalGroupParam{EmailAddresses: []string{"a@b.com"}, ApprovalsNeeded: 1}
				groupB := ApprovalGroupParam{EmailAddresses: []string{"c@d.com"}, ApprovalsNeeded: 2}
				p.ApprovalGroups = []ApprovalGroupParam{groupA, groupB}
				d.ApprovalGroups = []ApprovalGroupParam{groupB, groupA}
			},
			want: true,
		},
		{
			name: "ApprovalGroups inner EmailAddresses order insensitive",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.ApprovalGroups = []ApprovalGroupParam{
					{EmailAddresses: []string{"a@b.com", "c@d.com"}, ApprovalsNeeded: 1},
				}
				d.ApprovalGroups = []ApprovalGroupParam{
					{EmailAddresses: []string{"c@d.com", "a@b.com"}, ApprovalsNeeded: 1},
				}
			},
			want: true,
		},

		// Rule type coverage: matching

		{
			name: "IPRange rule match",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = []AccessRuleParam{{IPRange: strPtr("10.0.0.0/8")}}
				d.Include = []AccessRuleParam{{IPRange: strPtr("10.0.0.0/8")}}
			},
			want: true,
		},
		{
			name: "IPListID rule match",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = []AccessRuleParam{{IPListID: strPtr("list-1")}}
				d.Include = []AccessRuleParam{{IPListID: strPtr("list-1")}}
			},
			want: true,
		},
		{
			name: "Country rule match",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = []AccessRuleParam{{Country: strPtr("US")}}
				d.Include = []AccessRuleParam{{Country: strPtr("US")}}
			},
			want: true,
		},
		{
			name: "Everyone rule match",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = []AccessRuleParam{{Everyone: boolPtr(true)}}
				d.Include = []AccessRuleParam{{Everyone: boolPtr(true)}}
			},
			want: true,
		},
		{
			name: "ServiceTokenID rule match",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = []AccessRuleParam{{ServiceTokenID: strPtr("tok-1")}}
				d.Include = []AccessRuleParam{{ServiceTokenID: strPtr("tok-1")}}
			},
			want: true,
		},
		{
			name: "AnyValidServiceToken rule match",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = []AccessRuleParam{{AnyValidServiceToken: boolPtr(true)}}
				d.Include = []AccessRuleParam{{AnyValidServiceToken: boolPtr(true)}}
			},
			want: true,
		},
		{
			name: "Email rule match",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = []AccessRuleParam{{Email: strPtr("user@example.com")}}
				d.Include = []AccessRuleParam{{Email: strPtr("user@example.com")}}
			},
			want: true,
		},
		{
			name: "EmailListID rule match",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = []AccessRuleParam{{EmailListID: strPtr("elist-1")}}
				d.Include = []AccessRuleParam{{EmailListID: strPtr("elist-1")}}
			},
			want: true,
		},
		{
			name: "EmailDomain rule match",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = []AccessRuleParam{{EmailDomain: strPtr("example.com")}}
				d.Include = []AccessRuleParam{{EmailDomain: strPtr("example.com")}}
			},
			want: true,
		},
		{
			name: "OIDCClaim rule match",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = []AccessRuleParam{{OIDCClaim: &OIDCClaimParam{
					IdentityProviderID: "idp-1", ClaimName: "role", ClaimValue: "admin",
				}}}
				d.Include = []AccessRuleParam{{OIDCClaim: &OIDCClaimParam{
					IdentityProviderID: "idp-1", ClaimName: "role", ClaimValue: "admin",
				}}}
			},
			want: true,
		},
		{
			name: "GSuiteGroup rule match",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = []AccessRuleParam{{GSuiteGroup: &GSuiteGroupParam{
					IdentityProviderID: "idp-1", Email: "eng@company.com",
				}}}
				d.Include = []AccessRuleParam{{GSuiteGroup: &GSuiteGroupParam{
					IdentityProviderID: "idp-1", Email: "eng@company.com",
				}}}
			},
			want: true,
		},
		{
			name: "Certificate rule match",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = []AccessRuleParam{{Certificate: boolPtr(true)}}
				d.Include = []AccessRuleParam{{Certificate: boolPtr(true)}}
			},
			want: true,
		},
		{
			name: "CommonName rule match",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = []AccessRuleParam{{CommonName: strPtr("*.example.com")}}
				d.Include = []AccessRuleParam{{CommonName: strPtr("*.example.com")}}
			},
			want: true,
		},
		{
			name: "GroupID rule match",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = []AccessRuleParam{{GroupID: strPtr("grp-1")}}
				d.Include = []AccessRuleParam{{GroupID: strPtr("grp-1")}}
			},
			want: true,
		},

		// Rule type coverage: drift

		{
			name: "IPRange rule differ",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = []AccessRuleParam{{IPRange: strPtr("10.0.0.0/8")}}
				d.Include = []AccessRuleParam{{IPRange: strPtr("192.168.0.0/16")}}
			},
			want: false,
		},
		{
			name: "Country rule differ",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = []AccessRuleParam{{Country: strPtr("US")}}
				d.Include = []AccessRuleParam{{Country: strPtr("GB")}}
			},
			want: false,
		},
		{
			name: "ServiceTokenID rule differ",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = []AccessRuleParam{{ServiceTokenID: strPtr("tok-1")}}
				d.Include = []AccessRuleParam{{ServiceTokenID: strPtr("tok-2")}}
			},
			want: false,
		},
		{
			name: "OIDCClaim rule differ value",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = []AccessRuleParam{{OIDCClaim: &OIDCClaimParam{
					IdentityProviderID: "idp-1", ClaimName: "role", ClaimValue: "admin",
				}}}
				d.Include = []AccessRuleParam{{OIDCClaim: &OIDCClaimParam{
					IdentityProviderID: "idp-1", ClaimName: "role", ClaimValue: "user",
				}}}
			},
			want: false,
		},
		{
			name: "GroupID rule differ",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = []AccessRuleParam{{GroupID: strPtr("grp-1")}}
				d.Include = []AccessRuleParam{{GroupID: strPtr("grp-2")}}
			},
			want: false,
		},
		{
			name: "different rule types is drift",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = []AccessRuleParam{{IPRange: strPtr("10.0.0.0/8")}}
				d.Include = []AccessRuleParam{{Country: strPtr("US")}}
			},
			want: false,
		},

		// Mixed rule types with order insensitivity

		{
			name: "mixed rule types same order",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				rules := []AccessRuleParam{
					{IPRange: strPtr("10.0.0.0/8")},
					{Email: strPtr("admin@example.com")},
					{Country: strPtr("US")},
				}
				p.Include = make([]AccessRuleParam, len(rules))
				copy(p.Include, rules)
				d.Include = make([]AccessRuleParam, len(rules))
				copy(d.Include, rules)
			},
			want: true,
		},
		{
			name: "mixed rule types different order",
			modify: func(p *AccessPolicy, d *PolicyParams) {
				p.Include = []AccessRuleParam{
					{IPRange: strPtr("10.0.0.0/8")},
					{Email: strPtr("admin@example.com")},
					{Country: strPtr("US")},
				}
				d.Include = []AccessRuleParam{
					{Country: strPtr("US")},
					{IPRange: strPtr("10.0.0.0/8")},
					{Email: strPtr("admin@example.com")},
				}
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			existing, desired := makeMatchingPolicyPair()
			tt.modify(existing, desired)
			got := accessPolicyEqual(existing, desired)
			if got != tt.want {
				t.Errorf("accessPolicyEqual() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStringSlicesEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want bool
	}{
		{"nil nil", nil, nil, true},
		{"nil empty", nil, []string{}, true},
		{"empty nil", []string{}, nil, true},
		{"empty empty", []string{}, []string{}, true},
		{"same elements same order", []string{"a", "b"}, []string{"a", "b"}, true},
		{"same elements different order", []string{"a", "b", "c"}, []string{"c", "a", "b"}, true},
		{"different elements", []string{"a", "b"}, []string{"a", "c"}, false},
		{"different lengths", []string{"a", "b"}, []string{"a"}, false},
		{"single element match", []string{"x"}, []string{"x"}, true},
		{"single element differ", []string{"x"}, []string{"y"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stringSlicesEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("stringSlicesEqual(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestAccessRuleKey(t *testing.T) {
	tests := []struct {
		name string
		rule AccessRuleParam
		want string
	}{
		{"IPRange", AccessRuleParam{IPRange: strPtr("10.0.0.0/8")}, "ip:10.0.0.0/8"},
		{"IPListID", AccessRuleParam{IPListID: strPtr("list-1")}, "iplist:list-1"},
		{"Country", AccessRuleParam{Country: strPtr("US")}, "country:US"},
		{"Everyone", AccessRuleParam{Everyone: boolPtr(true)}, "everyone"},
		{"ServiceTokenID", AccessRuleParam{ServiceTokenID: strPtr("tok-1")}, "servicetoken:tok-1"},
		{"AnyValidServiceToken", AccessRuleParam{AnyValidServiceToken: boolPtr(true)}, "anyservicetoken"},
		{"Email", AccessRuleParam{Email: strPtr("a@b.com")}, "email:a@b.com"},
		{"EmailListID", AccessRuleParam{EmailListID: strPtr("elist-1")}, "emaillist:elist-1"},
		{"EmailDomain", AccessRuleParam{EmailDomain: strPtr("b.com")}, "emaildomain:b.com"},
		{"OIDCClaim", AccessRuleParam{OIDCClaim: &OIDCClaimParam{
			IdentityProviderID: "idp-1", ClaimName: "role", ClaimValue: "admin",
		}}, "oidc:idp-1:role:admin"},
		{"GSuiteGroup", AccessRuleParam{GSuiteGroup: &GSuiteGroupParam{
			IdentityProviderID: "idp-1", Email: "eng@co.com",
		}}, "gsuite:idp-1:eng@co.com"},
		{"Certificate", AccessRuleParam{Certificate: boolPtr(true)}, "cert"},
		{"CommonName", AccessRuleParam{CommonName: strPtr("*.example.com")}, "cn:*.example.com"},
		{"GroupID", AccessRuleParam{GroupID: strPtr("grp-1")}, "group:grp-1"},
		{"empty rule", AccessRuleParam{}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := accessRuleKey(tt.rule); got != tt.want {
				t.Errorf("accessRuleKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAccessRulesEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b []AccessRuleParam
		want bool
	}{
		{"nil nil", nil, nil, true},
		{"nil empty", nil, []AccessRuleParam{}, true},
		{"empty nil", []AccessRuleParam{}, nil, true},
		{
			"same rules same order",
			[]AccessRuleParam{{IPRange: strPtr("10.0.0.0/8")}},
			[]AccessRuleParam{{IPRange: strPtr("10.0.0.0/8")}},
			true,
		},
		{
			"same rules different order",
			[]AccessRuleParam{
				{IPRange: strPtr("10.0.0.0/8")},
				{Email: strPtr("a@b.com")},
			},
			[]AccessRuleParam{
				{Email: strPtr("a@b.com")},
				{IPRange: strPtr("10.0.0.0/8")},
			},
			true,
		},
		{
			"different rules",
			[]AccessRuleParam{{IPRange: strPtr("10.0.0.0/8")}},
			[]AccessRuleParam{{IPRange: strPtr("192.168.0.0/16")}},
			false,
		},
		{
			"different lengths",
			[]AccessRuleParam{{IPRange: strPtr("10.0.0.0/8")}},
			[]AccessRuleParam{
				{IPRange: strPtr("10.0.0.0/8")},
				{Email: strPtr("a@b.com")},
			},
			false,
		},
		{
			"different types",
			[]AccessRuleParam{{IPRange: strPtr("10.0.0.0/8")}},
			[]AccessRuleParam{{Country: strPtr("US")}},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := accessRulesEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("accessRulesEqual() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApprovalGroupsEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b []ApprovalGroupParam
		want bool
	}{
		{"nil nil", nil, nil, true},
		{"nil empty", nil, []ApprovalGroupParam{}, true},
		{
			"matching groups",
			[]ApprovalGroupParam{{EmailAddresses: []string{"a@b.com"}, ApprovalsNeeded: 1}},
			[]ApprovalGroupParam{{EmailAddresses: []string{"a@b.com"}, ApprovalsNeeded: 1}},
			true,
		},
		{
			"different order",
			[]ApprovalGroupParam{
				{EmailAddresses: []string{"a@b.com"}, ApprovalsNeeded: 1},
				{EmailAddresses: []string{"c@d.com"}, ApprovalsNeeded: 2},
			},
			[]ApprovalGroupParam{
				{EmailAddresses: []string{"c@d.com"}, ApprovalsNeeded: 2},
				{EmailAddresses: []string{"a@b.com"}, ApprovalsNeeded: 1},
			},
			true,
		},
		{
			"inner EmailAddresses order insensitive",
			[]ApprovalGroupParam{{EmailAddresses: []string{"a@b.com", "c@d.com"}, ApprovalsNeeded: 1}},
			[]ApprovalGroupParam{{EmailAddresses: []string{"c@d.com", "a@b.com"}, ApprovalsNeeded: 1}},
			true,
		},
		{
			"different ApprovalsNeeded",
			[]ApprovalGroupParam{{EmailAddresses: []string{"a@b.com"}, ApprovalsNeeded: 1}},
			[]ApprovalGroupParam{{EmailAddresses: []string{"a@b.com"}, ApprovalsNeeded: 2}},
			false,
		},
		{
			"different EmailListUUID",
			[]ApprovalGroupParam{{EmailListUUID: "uuid-1", ApprovalsNeeded: 1}},
			[]ApprovalGroupParam{{EmailListUUID: "uuid-2", ApprovalsNeeded: 1}},
			false,
		},
		{
			"different lengths",
			[]ApprovalGroupParam{
				{EmailAddresses: []string{"a@b.com"}, ApprovalsNeeded: 1},
			},
			[]ApprovalGroupParam{
				{EmailAddresses: []string{"a@b.com"}, ApprovalsNeeded: 1},
				{EmailAddresses: []string{"c@d.com"}, ApprovalsNeeded: 2},
			},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := approvalGroupsEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("approvalGroupsEqual() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCorsHeadersEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b *CORSHeadersParam
		want bool
	}{
		{"nil nil", nil, nil, true},
		{"nil vs non-nil", nil, &CORSHeadersParam{}, false},
		{"non-nil vs nil", &CORSHeadersParam{}, nil, false},
		{
			"matching empty structs",
			&CORSHeadersParam{},
			&CORSHeadersParam{},
			true,
		},
		{
			"matching with fields",
			&CORSHeadersParam{AllowAllOrigins: true, MaxAge: 3600},
			&CORSHeadersParam{AllowAllOrigins: true, MaxAge: 3600},
			true,
		},
		{
			"AllowAllHeaders differs",
			&CORSHeadersParam{AllowAllHeaders: true},
			&CORSHeadersParam{AllowAllHeaders: false},
			false,
		},
		{
			"AllowAllMethods differs",
			&CORSHeadersParam{AllowAllMethods: true},
			&CORSHeadersParam{AllowAllMethods: false},
			false,
		},
		{
			"AllowAllOrigins differs",
			&CORSHeadersParam{AllowAllOrigins: true},
			&CORSHeadersParam{AllowAllOrigins: false},
			false,
		},
		{
			"AllowCredentials differs",
			&CORSHeadersParam{AllowCredentials: true},
			&CORSHeadersParam{AllowCredentials: false},
			false,
		},
		{
			"MaxAge differs",
			&CORSHeadersParam{MaxAge: 3600},
			&CORSHeadersParam{MaxAge: 7200},
			false,
		},
		{
			"AllowedHeaders order insensitive",
			&CORSHeadersParam{AllowedHeaders: []string{"Content-Type", "Authorization"}},
			&CORSHeadersParam{AllowedHeaders: []string{"Authorization", "Content-Type"}},
			true,
		},
		{
			"AllowedMethods order insensitive",
			&CORSHeadersParam{AllowedMethods: []string{"GET", "POST"}},
			&CORSHeadersParam{AllowedMethods: []string{"POST", "GET"}},
			true,
		},
		{
			"AllowedOrigins order insensitive",
			&CORSHeadersParam{AllowedOrigins: []string{"https://a.com", "https://b.com"}},
			&CORSHeadersParam{AllowedOrigins: []string{"https://b.com", "https://a.com"}},
			true,
		},
		{
			"AllowedHeaders different values",
			&CORSHeadersParam{AllowedHeaders: []string{"Content-Type"}},
			&CORSHeadersParam{AllowedHeaders: []string{"Authorization"}},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := corsHeadersEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("corsHeadersEqual() = %v, want %v", got, tt.want)
			}
		})
	}
}

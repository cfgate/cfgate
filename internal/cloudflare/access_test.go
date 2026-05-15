package cloudflare

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

func boolPtr(b bool) *bool    { return &b }
func strPtr(s string) *string { return &s }

func makeMatchingAppPair() (*AccessApplication, *ApplicationParams) {
	httpOnly := true
	return &AccessApplication{
			Name:                        "Test App",
			Domain:                      "app.example.com",
			Destinations:                []string{"app.example.com"},
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

		// Per-field drift detection

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
			name: "existing missing destinations updates to desired domain destination",
			modify: func(a *AccessApplication, _ *ApplicationParams) {
				a.Destinations = nil
			},
			want: true,
		},
		{
			name: "existing missing tags updates to desired cfgate tags",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.Tags = nil
				p.Tags = []string{"cfgate", "cfgate:default:app"}
			},
			want: true,
		},
		{
			name: "existing missing policies updates to desired policy links",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.Policies = nil
				p.Policies = []ApplicationPolicyLink{{ID: "policy-1", Precedence: 1}}
			},
			want: true,
		},
		{
			name: "policy links order insensitive by precedence",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.Policies = []ApplicationPolicyLink{
					{ID: "policy-b", Precedence: 1},
					{ID: "policy-a", Precedence: 3},
				}
				p.Policies = []ApplicationPolicyLink{
					{ID: "policy-a", Precedence: 3},
					{ID: "policy-b", Precedence: 1},
				}
			},
			want: false,
		},
		{
			name: "policy links order insensitive with duplicate precedence",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.Policies = []ApplicationPolicyLink{
					{ID: "policy-b", Precedence: 1},
					{ID: "policy-a", Precedence: 1},
				}
				p.Policies = []ApplicationPolicyLink{
					{ID: "policy-a", Precedence: 1},
					{ID: "policy-b", Precedence: 1},
				}
			},
			want: false,
		},
		{
			name: "empty tags and policies match",
			modify: func(a *AccessApplication, p *ApplicationParams) {
				a.Tags = nil
				p.Tags = nil
				a.Policies = nil
				p.Policies = nil
			},
			want: false,
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

func TestEnsureApplicationByIDOrTagsEnsuresTagsBeforeCreate(t *testing.T) {
	ctx := context.Background()
	mock := NewMockClient()
	createdTags := []string{}
	createCalled := false

	mock.ListAccessTagsFunc = func(_ context.Context, accountID string) ([]AccessTag, error) {
		if accountID != "account-1" {
			t.Fatalf("ListAccessTags accountID = %q, want account-1", accountID)
		}
		return []AccessTag{{Name: "cfgate"}}, nil
	}
	mock.CreateAccessTagFunc = func(_ context.Context, accountID, tagName string) (*AccessTag, error) {
		if accountID != "account-1" {
			t.Fatalf("CreateAccessTag accountID = %q, want account-1", accountID)
		}
		createdTags = append(createdTags, tagName)
		return &AccessTag{Name: tagName}, nil
	}
	mock.ListAccessApplicationsFunc = func(context.Context, string) ([]AccessApplication, error) {
		return nil, nil
	}
	mock.CreateAccessApplicationFunc = func(_ context.Context, _ string, params ApplicationParams) (*AccessApplication, error) {
		createCalled = true
		if !reflect.DeepEqual(params.Tags, []string{"cfgate", "cfgate:default:app"}) {
			t.Fatalf("CreateAccessApplication tags = %v, want cfgate tags", params.Tags)
		}
		return &AccessApplication{ID: "app-1", Name: params.Name, Domain: params.Domain, Tags: params.Tags}, nil
	}

	params := ApplicationParams{
		Name:   "app",
		Domain: "app.example.com",
		Tags:   []string{"cfgate", "cfgate:default:app", "cfgate:default:app"},
	}
	app, err := NewAccessService(mock, logr.Discard()).EnsureApplicationByIDOrTags(ctx, "account-1", "", params)
	if err != nil {
		t.Fatalf("EnsureApplicationByIDOrTags() error = %v", err)
	}
	if app == nil || app.ID != "app-1" {
		t.Fatalf("EnsureApplicationByIDOrTags() app = %+v, want app-1", app)
	}
	if !createCalled {
		t.Fatal("CreateAccessApplication was not called")
	}
	if !reflect.DeepEqual(createdTags, []string{"cfgate:default:app"}) {
		t.Fatalf("created tags = %v, want [cfgate:default:app]", createdTags)
	}
}

func TestEnsureApplicationByIDOrTagsStatusIDMissingCreatesWithoutList(t *testing.T) {
	ctx := context.Background()
	mock := NewMockClient()
	createCalled := 0

	mock.ListAccessTagsFunc = func(context.Context, string) ([]AccessTag, error) {
		return []AccessTag{{Name: "cfgate"}, {Name: "cfgate:default:app"}}, nil
	}
	mock.GetAccessApplicationFunc = func(_ context.Context, _ string, appID string) (*AccessApplication, error) {
		if appID != "missing-app" {
			t.Fatalf("GetAccessApplication appID = %q, want missing-app", appID)
		}
		return nil, nil
	}
	mock.ListAccessApplicationsFunc = func(context.Context, string) ([]AccessApplication, error) {
		t.Fatal("ListAccessApplications called after status ID miss")
		return nil, nil
	}
	mock.CreateAccessApplicationFunc = func(_ context.Context, _ string, params ApplicationParams) (*AccessApplication, error) {
		createCalled++
		return &AccessApplication{ID: "app-new", Name: params.Name, Domain: params.Domain, Tags: params.Tags}, nil
	}

	params := ApplicationParams{
		Name:   "app",
		Domain: "app.example.com",
		Tags:   []string{"cfgate", "cfgate:default:app"},
	}
	app, err := NewAccessService(mock, logr.Discard()).EnsureApplicationByIDOrTags(ctx, "account-1", "missing-app", params)
	if err != nil {
		t.Fatalf("EnsureApplicationByIDOrTags() error = %v", err)
	}
	if app == nil || app.ID != "app-new" {
		t.Fatalf("EnsureApplicationByIDOrTags() app = %+v, want app-new", app)
	}
	if createCalled != 1 {
		t.Fatalf("CreateAccessApplication calls = %d, want 1", createCalled)
	}
}

func TestEnsureApplicationByIDOrTagsStatusIDFoundMatchingDoesNotListOrUpdate(t *testing.T) {
	ctx := context.Background()
	mock := NewMockClient()
	existing, params := makeMatchingAppPair()
	existing.ID = "app-1"
	existing.Tags = []string{"cfgate", "cfgate:default:app"}
	params.Tags = []string{"cfgate", "cfgate:default:app"}

	mock.ListAccessTagsFunc = func(context.Context, string) ([]AccessTag, error) {
		return []AccessTag{{Name: "cfgate"}, {Name: "cfgate:default:app"}}, nil
	}
	mock.GetAccessApplicationFunc = func(context.Context, string, string) (*AccessApplication, error) {
		return existing, nil
	}
	mock.ListAccessApplicationsFunc = func(context.Context, string) ([]AccessApplication, error) {
		t.Fatal("ListAccessApplications called for matching status ID")
		return nil, nil
	}
	mock.UpdateAccessApplicationFunc = func(context.Context, string, string, ApplicationParams) (*AccessApplication, error) {
		t.Fatal("UpdateAccessApplication called for matching app")
		return nil, nil
	}
	mock.CreateAccessApplicationFunc = func(context.Context, string, ApplicationParams) (*AccessApplication, error) {
		t.Fatal("CreateAccessApplication called for matching app")
		return nil, nil
	}

	app, err := NewAccessService(mock, logr.Discard()).EnsureApplicationByIDOrTags(ctx, "account-1", "app-1", *params)
	if err != nil {
		t.Fatalf("EnsureApplicationByIDOrTags() error = %v", err)
	}
	if app != existing {
		t.Fatalf("EnsureApplicationByIDOrTags() app = %+v, want existing", app)
	}
}

func TestEnsureApplicationByIDOrTagsFailsWhenRequiredTagCannotBeCreated(t *testing.T) {
	ctx := context.Background()
	mock := NewMockClient()
	createCalled := false

	mock.ListAccessTagsFunc = func(context.Context, string) ([]AccessTag, error) {
		return []AccessTag{{Name: "cfgate"}}, nil
	}
	mock.CreateAccessTagFunc = func(_ context.Context, _ string, tagName string) (*AccessTag, error) {
		if tagName != "cfgate:default:app" {
			t.Fatalf("CreateAccessTag tagName = %q, want cfgate:default:app", tagName)
		}
		return nil, cfError(400, 12146)
	}
	mock.ListAccessApplicationsFunc = func(context.Context, string) ([]AccessApplication, error) {
		return nil, nil
	}
	mock.CreateAccessApplicationFunc = func(_ context.Context, _ string, params ApplicationParams) (*AccessApplication, error) {
		createCalled = true
		return nil, nil
	}

	params := ApplicationParams{
		Name:   "app",
		Domain: "app.example.com",
		Tags:   []string{"cfgate", "cfgate:default:app"},
	}
	_, err := NewAccessService(mock, logr.Discard()).EnsureApplicationByIDOrTags(ctx, "account-1", "", params)
	if err == nil || !strings.Contains(err.Error(), `failed to create access tag "cfgate:default:app"`) {
		t.Fatalf("EnsureApplicationByIDOrTags() error = %v, want owner tag failure", err)
	}
	if createCalled {
		t.Fatal("CreateAccessApplication called after tag creation failed")
	}
}

func TestEnsureApplicationTagsRequiresAllTags(t *testing.T) {
	ctx := context.Background()

	t.Run("owner tag limit returns error", func(t *testing.T) {
		mock := NewMockClient()
		mock.ListAccessTagsFunc = func(context.Context, string) ([]AccessTag, error) {
			return nil, nil
		}
		mock.CreateAccessTagFunc = func(_ context.Context, _ string, tagName string) (*AccessTag, error) {
			if strings.HasPrefix(tagName, "cfgate:") {
				return nil, cfError(400, 12146)
			}
			return &AccessTag{Name: tagName}, nil
		}

		_, err := NewAccessService(mock, logr.Discard()).ensureApplicationTags(ctx, "account-1", []string{"cfgate:default:app", "cfgate"})
		if err == nil || !strings.Contains(err.Error(), `failed to create access tag "cfgate:default:app"`) {
			t.Fatalf("ensureApplicationTags() error = %v, want owner tag failure", err)
		}
	})

	t.Run("base tag limit is required", func(t *testing.T) {
		mock := NewMockClient()
		mock.ListAccessTagsFunc = func(context.Context, string) ([]AccessTag, error) {
			return nil, nil
		}
		mock.CreateAccessTagFunc = func(context.Context, string, string) (*AccessTag, error) {
			return nil, cfError(400, 12146)
		}

		_, err := NewAccessService(mock, logr.Discard()).ensureApplicationTags(ctx, "account-1", []string{"cfgate"})
		if err == nil || !strings.Contains(err.Error(), `failed to create access tag "cfgate"`) {
			t.Fatalf("ensureApplicationTags() error = %v, want cfgate create failure", err)
		}
	})
}

func TestEnsureReusablePolicy(t *testing.T) {
	baseExisting, baseDesired := makeMatchingPolicyPair()
	baseExisting.ID = "policy-1"

	tests := []struct {
		name        string
		statusID    string
		getPolicy   *AccessPolicy
		getErr      error
		list        []AccessPolicy
		listErr     error
		wantID      string
		wantErr     string
		wantGet     int
		wantList    int
		wantCreate  int
		wantUpdate  int
		updateID    string
		createID    string
		listAllowed bool
	}{
		{
			name:     "status ID matches existing policy",
			statusID: "policy-1",
			getPolicy: &AccessPolicy{
				ID:                           "policy-1",
				Name:                         baseExisting.Name,
				Decision:                     baseExisting.Decision,
				Include:                      baseExisting.Include,
				SessionDuration:              baseExisting.SessionDuration,
				PurposeJustificationRequired: baseExisting.PurposeJustificationRequired,
				PurposeJustificationPrompt:   baseExisting.PurposeJustificationPrompt,
				ApprovalRequired:             baseExisting.ApprovalRequired,
			},
			wantID:  "policy-1",
			wantGet: 1,
		},
		{
			name:     "status ID policy drifts",
			statusID: "policy-1",
			getPolicy: &AccessPolicy{
				ID:              "policy-1",
				Name:            "old",
				Decision:        baseExisting.Decision,
				Include:         baseExisting.Include,
				SessionDuration: baseExisting.SessionDuration,
			},
			wantID:     "policy-1",
			wantGet:    1,
			wantUpdate: 1,
			updateID:   "policy-1",
		},
		{
			name:        "status ID missing creates when no exact name match",
			statusID:    "policy-1",
			list:        []AccessPolicy{{ID: "other", Name: "other"}},
			wantID:      "created",
			wantGet:     1,
			wantList:    1,
			wantCreate:  1,
			createID:    "created",
			listAllowed: true,
		},
		{
			name: "adopts exact name match",
			list: []AccessPolicy{{
				ID:              "policy-2",
				Name:            baseExisting.Name,
				Decision:        baseExisting.Decision,
				Include:         baseExisting.Include,
				SessionDuration: baseExisting.SessionDuration,
			}},
			wantID:      "policy-2",
			wantList:    1,
			listAllowed: true,
		},
		{
			name: "updates exact name match with drift",
			list: []AccessPolicy{{
				ID:              "policy-2",
				Name:            baseExisting.Name,
				Decision:        "deny",
				Include:         baseExisting.Include,
				SessionDuration: baseExisting.SessionDuration,
			}},
			wantID:      "policy-2",
			wantList:    1,
			wantUpdate:  1,
			updateID:    "policy-2",
			listAllowed: true,
		},
		{
			name: "duplicate exact name matches fail",
			list: []AccessPolicy{
				{ID: "policy-2", Name: baseExisting.Name},
				{ID: "policy-3", Name: baseExisting.Name},
			},
			wantErr:     "ambiguous adoption",
			wantList:    1,
			listAllowed: true,
		},
		{
			name:     "get error is wrapped",
			statusID: "policy-1",
			getErr:   errors.New("get failed"),
			wantErr:  "failed to get reusable policy policy-1",
			wantGet:  1,
		},
		{
			name:        "list error is wrapped",
			listErr:     errors.New("list failed"),
			wantErr:     "failed to list reusable policies",
			wantList:    1,
			listAllowed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := NewMockClient()
			var gotGet, gotList, gotCreate, gotUpdate int
			mock.GetAccessPolicyFunc = func(_ context.Context, accountID, policyID string) (*AccessPolicy, error) {
				gotGet++
				if accountID != "account-1" {
					t.Fatalf("GetAccessPolicy accountID = %q, want account-1", accountID)
				}
				if policyID != tt.statusID {
					t.Fatalf("GetAccessPolicy policyID = %q, want %q", policyID, tt.statusID)
				}
				return tt.getPolicy, tt.getErr
			}
			mock.ListAccessPoliciesFunc = func(context.Context, string) ([]AccessPolicy, error) {
				gotList++
				if !tt.listAllowed && tt.wantList == 0 {
					t.Fatal("ListAccessPolicies called unexpectedly")
				}
				return tt.list, tt.listErr
			}
			mock.CreateAccessPolicyFunc = func(_ context.Context, _ string, params PolicyParams) (*AccessPolicy, error) {
				gotCreate++
				if !accessPolicyEqual(&AccessPolicy{
					Name:                         params.Name,
					Decision:                     params.Decision,
					Include:                      params.Include,
					Exclude:                      params.Exclude,
					Require:                      params.Require,
					SessionDuration:              params.SessionDuration,
					PurposeJustificationRequired: params.PurposeJustificationRequired,
					PurposeJustificationPrompt:   params.PurposeJustificationPrompt,
					ApprovalRequired:             params.ApprovalRequired,
					ApprovalGroups:               params.ApprovalGroups,
				}, baseDesired) {
					t.Fatalf("CreateAccessPolicy params = %+v, want desired", params)
				}
				return &AccessPolicy{ID: tt.createID, Name: params.Name}, nil
			}
			mock.UpdateAccessPolicyFunc = func(_ context.Context, _ string, policyID string, params PolicyParams) (*AccessPolicy, error) {
				gotUpdate++
				if policyID != tt.updateID {
					t.Fatalf("UpdateAccessPolicy policyID = %q, want %q", policyID, tt.updateID)
				}
				return &AccessPolicy{ID: policyID, Name: params.Name}, nil
			}

			got, err := NewAccessService(mock, logr.Discard()).EnsureReusablePolicy(context.Background(), "account-1", tt.statusID, *baseDesired)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("EnsureReusablePolicy() error = %v, want %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("EnsureReusablePolicy() error = %v", err)
			} else if got == nil || got.ID != tt.wantID {
				t.Fatalf("EnsureReusablePolicy() = %+v, want ID %q", got, tt.wantID)
			}
			if gotGet != tt.wantGet || gotList != tt.wantList || gotCreate != tt.wantCreate || gotUpdate != tt.wantUpdate {
				t.Fatalf("calls get/list/create/update = %d/%d/%d/%d, want %d/%d/%d/%d",
					gotGet, gotList, gotCreate, gotUpdate, tt.wantGet, tt.wantList, tt.wantCreate, tt.wantUpdate)
			}
		})
	}
}

func TestEnsureServiceToken(t *testing.T) {
	ctx := context.Background()
	params := ServiceTokenParams{Name: "svc", Duration: "8760h"}

	t.Run("existing unexpired token returns without secret write", func(t *testing.T) {
		mock := NewMockClient()
		writer := &recordingSecretWriter{}
		mock.ListServiceTokensFunc = func(context.Context, string) ([]ServiceToken, error) {
			return []ServiceToken{{ID: "token-1", Name: "svc", ClientID: "client-id", ExpiresAt: time.Now().Add(time.Hour)}}, nil
		}
		got, err := NewAccessService(mock, logr.Discard()).EnsureServiceToken(ctx, "account-1", params, writer)
		if err != nil {
			t.Fatalf("EnsureServiceToken() error = %v", err)
		}
		if got.ID != "token-1" {
			t.Fatalf("token ID = %q, want token-1", got.ID)
		}
		if writer.calls != 0 {
			t.Fatalf("secret writes = %d, want 0", writer.calls)
		}
	})

	t.Run("existing unexpired token with fresh secret returns without rotation", func(t *testing.T) {
		mock := NewMockClient()
		writer := &refreshCheckingSecretWriter{}
		mock.ListServiceTokensFunc = func(context.Context, string) ([]ServiceToken, error) {
			return []ServiceToken{{ID: "token-1", Name: "svc", ClientID: "client-id", ExpiresAt: time.Now().Add(time.Hour)}}, nil
		}
		mock.RotateServiceTokenFunc = func(context.Context, string, string) (*ServiceTokenWithSecret, error) {
			t.Fatal("RotateServiceToken should not be called")
			return nil, nil
		}
		got, err := NewAccessService(mock, logr.Discard()).EnsureServiceToken(ctx, "account-1", params, writer)
		if err != nil {
			t.Fatalf("EnsureServiceToken() error = %v", err)
		}
		if got.ID != "token-1" || writer.calls != 0 || writer.checks != 1 {
			t.Fatalf("got token/checks/writes = %q/%d/%d, want token-1/1/0", got.ID, writer.checks, writer.calls)
		}
	})

	t.Run("existing unexpired token with stale secret rotates and writes", func(t *testing.T) {
		mock := NewMockClient()
		writer := &refreshCheckingSecretWriter{needsRefresh: true}
		mock.ListServiceTokensFunc = func(context.Context, string) ([]ServiceToken, error) {
			return []ServiceToken{{ID: "token-1", Name: "svc", ClientID: "old-client", ExpiresAt: time.Now().Add(time.Hour)}}, nil
		}
		mock.RotateServiceTokenFunc = func(_ context.Context, _ string, tokenID string) (*ServiceTokenWithSecret, error) {
			if tokenID != "token-1" {
				t.Fatalf("RotateServiceToken tokenID = %q, want token-1", tokenID)
			}
			return &ServiceTokenWithSecret{
				ServiceToken: ServiceToken{ID: "token-2", Name: "svc", ClientID: "client-id", ExpiresAt: time.Now().Add(time.Hour)},
				ClientSecret: "client-secret",
			}, nil
		}
		got, err := NewAccessService(mock, logr.Discard()).EnsureServiceToken(ctx, "account-1", params, writer)
		if err != nil {
			t.Fatalf("EnsureServiceToken() error = %v", err)
		}
		if got.ID != "token-2" || writer.checkedClientID != "old-client" {
			t.Fatalf("got token/checked client ID = %q/%q, want token-2/old-client", got.ID, writer.checkedClientID)
		}
		assertSecretData(t, &writer.recordingSecretWriter, "svc", "client-id", "client-secret")
	})

	t.Run("existing unexpired token secret check error does not rotate", func(t *testing.T) {
		mock := NewMockClient()
		writer := &refreshCheckingSecretWriter{checkErr: errors.New("check failed")}
		mock.ListServiceTokensFunc = func(context.Context, string) ([]ServiceToken, error) {
			return []ServiceToken{{ID: "token-1", Name: "svc", ClientID: "client-id", ExpiresAt: time.Now().Add(time.Hour)}}, nil
		}
		mock.RotateServiceTokenFunc = func(context.Context, string, string) (*ServiceTokenWithSecret, error) {
			t.Fatal("RotateServiceToken should not be called")
			return nil, nil
		}
		_, err := NewAccessService(mock, logr.Discard()).EnsureServiceToken(ctx, "account-1", params, writer)
		if err == nil || !strings.Contains(err.Error(), "failed to check service token secret") {
			t.Fatalf("EnsureServiceToken() error = %v, want check error", err)
		}
	})

	t.Run("expired token rotates and writes secret", func(t *testing.T) {
		mock := NewMockClient()
		writer := &recordingSecretWriter{}
		mock.ListServiceTokensFunc = func(context.Context, string) ([]ServiceToken, error) {
			return []ServiceToken{{ID: "token-1", Name: "svc", ExpiresAt: time.Now().Add(-time.Hour)}}, nil
		}
		mock.RotateServiceTokenFunc = func(_ context.Context, _ string, tokenID string) (*ServiceTokenWithSecret, error) {
			if tokenID != "token-1" {
				t.Fatalf("RotateServiceToken tokenID = %q, want token-1", tokenID)
			}
			return &ServiceTokenWithSecret{
				ServiceToken: ServiceToken{ID: "token-2", Name: "svc", ClientID: "client-id", ExpiresAt: time.Now().Add(time.Hour)},
				ClientSecret: "client-secret",
			}, nil
		}
		got, err := NewAccessService(mock, logr.Discard()).EnsureServiceToken(ctx, "account-1", params, writer)
		if err != nil {
			t.Fatalf("EnsureServiceToken() error = %v", err)
		}
		if got.ID != "token-2" {
			t.Fatalf("token ID = %q, want token-2", got.ID)
		}
		assertSecretData(t, writer, "svc", "client-id", "client-secret")
	})

	t.Run("rotate secret write failure deletes rotated token", func(t *testing.T) {
		mock := NewMockClient()
		writer := &recordingSecretWriter{err: errors.New("write failed")}
		deleted := ""
		mock.ListServiceTokensFunc = func(context.Context, string) ([]ServiceToken, error) {
			return []ServiceToken{{ID: "token-1", Name: "svc", ExpiresAt: time.Now().Add(-time.Hour)}}, nil
		}
		mock.RotateServiceTokenFunc = func(context.Context, string, string) (*ServiceTokenWithSecret, error) {
			return &ServiceTokenWithSecret{ServiceToken: ServiceToken{ID: "token-2", Name: "svc"}, ClientSecret: "secret"}, nil
		}
		mock.DeleteServiceTokenFunc = func(_ context.Context, _ string, tokenID string) error {
			deleted = tokenID
			return nil
		}
		_, err := NewAccessService(mock, logr.Discard()).EnsureServiceToken(ctx, "account-1", params, writer)
		if err == nil || !strings.Contains(err.Error(), "failed to store rotated service token secret") {
			t.Fatalf("EnsureServiceToken() error = %v, want rotated secret error", err)
		}
		if deleted != "token-2" {
			t.Fatalf("deleted token = %q, want token-2", deleted)
		}
	})

	t.Run("no existing token creates and writes secret", func(t *testing.T) {
		mock := NewMockClient()
		writer := &recordingSecretWriter{}
		mock.ListServiceTokensFunc = func(context.Context, string) ([]ServiceToken, error) { return nil, nil }
		mock.CreateServiceTokenFunc = func(_ context.Context, _ string, got ServiceTokenParams) (*ServiceTokenWithSecret, error) {
			if got != params {
				t.Fatalf("CreateServiceToken params = %+v, want %+v", got, params)
			}
			return &ServiceTokenWithSecret{
				ServiceToken: ServiceToken{ID: "token-1", Name: "svc", ClientID: "client-id", ExpiresAt: time.Now().Add(time.Hour)},
				ClientSecret: "client-secret",
			}, nil
		}
		got, err := NewAccessService(mock, logr.Discard()).EnsureServiceToken(ctx, "account-1", params, writer)
		if err != nil {
			t.Fatalf("EnsureServiceToken() error = %v", err)
		}
		if got.ID != "token-1" {
			t.Fatalf("token ID = %q, want token-1", got.ID)
		}
		assertSecretData(t, writer, "svc", "client-id", "client-secret")
	})

	t.Run("create secret write failure deletes created token", func(t *testing.T) {
		mock := NewMockClient()
		writer := &recordingSecretWriter{err: errors.New("write failed")}
		deleted := ""
		mock.ListServiceTokensFunc = func(context.Context, string) ([]ServiceToken, error) { return nil, nil }
		mock.CreateServiceTokenFunc = func(context.Context, string, ServiceTokenParams) (*ServiceTokenWithSecret, error) {
			return &ServiceTokenWithSecret{ServiceToken: ServiceToken{ID: "token-1", Name: "svc"}, ClientSecret: "secret"}, nil
		}
		mock.DeleteServiceTokenFunc = func(_ context.Context, _ string, tokenID string) error {
			deleted = tokenID
			return nil
		}
		_, err := NewAccessService(mock, logr.Discard()).EnsureServiceToken(ctx, "account-1", params, writer)
		if err == nil || !strings.Contains(err.Error(), "failed to store service token secret") {
			t.Fatalf("EnsureServiceToken() error = %v, want store secret error", err)
		}
		if deleted != "token-1" {
			t.Fatalf("deleted token = %q, want token-1", deleted)
		}
	})

	for _, tt := range []struct {
		name    string
		setup   func(*MockClient)
		wantErr string
	}{
		{
			name: "list error",
			setup: func(mock *MockClient) {
				mock.ListServiceTokensFunc = func(context.Context, string) ([]ServiceToken, error) {
					return nil, errors.New("list failed")
				}
			},
			wantErr: "failed to list service tokens",
		},
		{
			name: "create error",
			setup: func(mock *MockClient) {
				mock.ListServiceTokensFunc = func(context.Context, string) ([]ServiceToken, error) { return nil, nil }
				mock.CreateServiceTokenFunc = func(context.Context, string, ServiceTokenParams) (*ServiceTokenWithSecret, error) {
					return nil, errors.New("create failed")
				}
			},
			wantErr: "failed to create service token",
		},
		{
			name: "rotate error",
			setup: func(mock *MockClient) {
				mock.ListServiceTokensFunc = func(context.Context, string) ([]ServiceToken, error) {
					return []ServiceToken{{ID: "token-1", Name: "svc", ExpiresAt: time.Now().Add(-time.Hour)}}, nil
				}
				mock.RotateServiceTokenFunc = func(context.Context, string, string) (*ServiceTokenWithSecret, error) {
					return nil, errors.New("rotate failed")
				}
			},
			wantErr: "failed to rotate service token",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mock := NewMockClient()
			tt.setup(mock)
			_, err := NewAccessService(mock, logr.Discard()).EnsureServiceToken(ctx, "account-1", params, &recordingSecretWriter{})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("EnsureServiceToken() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestAccessServiceClient(t *testing.T) {
	mock := NewMockClient()
	if got := NewAccessService(mock, logr.Discard()).Client(); got != mock {
		t.Fatalf("Client() = %#v, want mock", got)
	}
}

type recordingSecretWriter struct {
	calls int
	name  string
	data  map[string][]byte
	err   error
}

func (w *recordingSecretWriter) WriteSecret(_ context.Context, name string, data map[string][]byte) error {
	w.calls++
	w.name = name
	w.data = data
	return w.err
}

type refreshCheckingSecretWriter struct {
	recordingSecretWriter
	checks          int
	needsRefresh    bool
	checkErr        error
	checkedName     string
	checkedClientID string
}

func (w *refreshCheckingSecretWriter) ServiceTokenSecretNeedsRefresh(_ context.Context, name, clientID string) (bool, error) {
	w.checks++
	w.checkedName = name
	w.checkedClientID = clientID
	return w.needsRefresh, w.checkErr
}

func assertSecretData(t *testing.T, writer *recordingSecretWriter, name, clientID, clientSecret string) {
	t.Helper()
	if writer.calls != 1 {
		t.Fatalf("secret writes = %d, want 1", writer.calls)
	}
	if writer.name != name {
		t.Fatalf("secret name = %q, want %q", writer.name, name)
	}
	if string(writer.data["CF_ACCESS_CLIENT_ID"]) != clientID {
		t.Fatalf("client ID = %q, want %q", writer.data["CF_ACCESS_CLIENT_ID"], clientID)
	}
	if string(writer.data["CF_ACCESS_CLIENT_SECRET"]) != clientSecret {
		t.Fatalf("client secret = %q, want %q", writer.data["CF_ACCESS_CLIENT_SECRET"], clientSecret)
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

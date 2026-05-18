package cloudflare

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cf "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/option"
	"github.com/cloudflare/cloudflare-go/v6/zero_trust"
)

func TestAPIErrorError(t *testing.T) {
	err := &APIError{
		Code:    429,
		Message: "rate limited",
	}

	got := err.Error()
	want := "cloudflare API error (code 429): rate limited"
	if got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestAccessApplicationDestinationURIs(t *testing.T) {
	tests := []struct {
		name   string
		params ApplicationParams
		want   []string
	}{
		{
			name:   "explicit destinations",
			params: ApplicationParams{Domain: "fallback.example.com", Destinations: []string{"one.example.com", "two.example.com/path"}},
			want:   []string{"one.example.com", "two.example.com/path"},
		},
		{
			name:   "domain fallback",
			params: ApplicationParams{Domain: "fallback.example.com"},
			want:   []string{"fallback.example.com"},
		},
		{
			name:   "empty",
			params: ApplicationParams{},
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := accessApplicationDestinationURIs(tt.params)
			if len(got) != len(tt.want) {
				t.Fatalf("accessApplicationDestinationURIs() = %#v, want %#v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("accessApplicationDestinationURIs() = %#v, want %#v", got, tt.want)
				}
			}
		})
	}
}

func TestAccessApplicationDestinationParams(t *testing.T) {
	params := ApplicationParams{Destinations: []string{"one.example.com", "two.example.com/path"}}

	newDestinations := accessApplicationNewDestinations(params)
	if len(newDestinations) != 2 {
		t.Fatalf("len(accessApplicationNewDestinations()) = %d, want 2", len(newDestinations))
	}
	for i, destination := range newDestinations {
		got, ok := destination.(zero_trust.AccessApplicationNewParamsBodySelfHostedApplicationDestinationsPublicDestination)
		if !ok {
			t.Fatalf("new destination[%d] type = %T", i, destination)
		}
		if got.Type.Value != zero_trust.AccessApplicationNewParamsBodySelfHostedApplicationDestinationsPublicDestinationTypePublic ||
			got.URI.Value != params.Destinations[i] {
			t.Fatalf("new destination[%d] = %+v", i, got)
		}
	}

	updateDestinations := accessApplicationUpdateDestinations(params)
	if len(updateDestinations) != 2 {
		t.Fatalf("len(accessApplicationUpdateDestinations()) = %d, want 2", len(updateDestinations))
	}
	for i, destination := range updateDestinations {
		got, ok := destination.(zero_trust.AccessApplicationUpdateParamsBodySelfHostedApplicationDestinationsPublicDestination)
		if !ok {
			t.Fatalf("update destination[%d] type = %T", i, destination)
		}
		if got.Type.Value != zero_trust.AccessApplicationUpdateParamsBodySelfHostedApplicationDestinationsPublicDestinationTypePublic ||
			got.URI.Value != params.Destinations[i] {
			t.Fatalf("update destination[%d] = %+v", i, got)
		}
	}
}

func TestAccessApplicationPolicyLinkParams(t *testing.T) {
	links := []ApplicationPolicyLink{{ID: "policy-1", Precedence: 1}, {ID: "policy-2", Precedence: 5}}

	newLinks := accessApplicationNewPolicyLinks(links)
	if len(newLinks) != 2 {
		t.Fatalf("len(accessApplicationNewPolicyLinks()) = %d, want 2", len(newLinks))
	}
	for i, link := range newLinks {
		got, ok := link.(zero_trust.AccessApplicationNewParamsBodySelfHostedApplicationPoliciesAccessAppPolicyLink)
		if !ok {
			t.Fatalf("new policy link[%d] type = %T", i, link)
		}
		if got.ID.Value != links[i].ID || got.Precedence.Value != int64(links[i].Precedence) {
			t.Fatalf("new policy link[%d] = %+v", i, got)
		}
	}

	updateLinks := accessApplicationUpdatePolicyLinks(links)
	if len(updateLinks) != 2 {
		t.Fatalf("len(accessApplicationUpdatePolicyLinks()) = %d, want 2", len(updateLinks))
	}
	for i, link := range updateLinks {
		got, ok := link.(zero_trust.AccessApplicationUpdateParamsBodySelfHostedApplicationPoliciesAccessAppPolicyLink)
		if !ok {
			t.Fatalf("update policy link[%d] type = %T", i, link)
		}
		if got.ID.Value != links[i].ID || got.Precedence.Value != int64(links[i].Precedence) {
			t.Fatalf("update policy link[%d] = %+v", i, got)
		}
	}
}

func TestClientAccessApplicationConversionErrors(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		response   string
		call       func(context.Context, *clientImpl) error
		wantPrefix string
	}{
		{
			name:     "create",
			method:   http.MethodPost,
			path:     "/accounts/account/access/apps",
			response: malformedAccessApplicationEnvelope(),
			call: func(ctx context.Context, client *clientImpl) error {
				_, err := client.CreateAccessApplication(ctx, "account", ApplicationParams{Name: "app", Domain: "app.example.com"})
				return err
			},
			wantPrefix: "failed to convert created access application",
		},
		{
			name:     "get",
			method:   http.MethodGet,
			path:     "/accounts/account/access/apps/app-1",
			response: malformedAccessApplicationEnvelope(),
			call: func(ctx context.Context, client *clientImpl) error {
				_, err := client.GetAccessApplication(ctx, "account", "app-1")
				return err
			},
			wantPrefix: "failed to convert access application",
		},
		{
			name:     "update",
			method:   http.MethodPut,
			path:     "/accounts/account/access/apps/app-1",
			response: malformedAccessApplicationEnvelope(),
			call: func(ctx context.Context, client *clientImpl) error {
				_, err := client.UpdateAccessApplication(ctx, "account", "app-1", ApplicationParams{Name: "app", Domain: "app.example.com"})
				return err
			},
			wantPrefix: "failed to convert updated access application",
		},
		{
			name:     "list",
			method:   http.MethodGet,
			path:     "/accounts/account/access/apps",
			response: malformedAccessApplicationListEnvelope(),
			call: func(ctx context.Context, client *clientImpl) error {
				_, err := client.ListAccessApplications(ctx, "account")
				return err
			},
			wantPrefix: "failed to convert listed access application",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := testClientImpl(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tt.method {
					t.Fatalf("method = %s, want %s", r.Method, tt.method)
				}
				if r.URL.Path != tt.path {
					t.Fatalf("path = %s, want %s", r.URL.Path, tt.path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.response))
			})

			err := tt.call(context.Background(), client)
			if err == nil {
				t.Fatal("access application conversion error = nil")
			}
			if !strings.Contains(err.Error(), tt.wantPrefix) {
				t.Fatalf("error = %q, want prefix %q", err.Error(), tt.wantPrefix)
			}
			if !strings.Contains(err.Error(), "unmarshal access application extras") {
				t.Fatalf("error = %q, want unmarshal context", err.Error())
			}
		})
	}
}

func TestMockClientUnstubbedAccessMethods(t *testing.T) {
	mock := NewMockClient()
	ctx := context.Background()
	if app, err := mock.CreateAccessApplication(ctx, "account", ApplicationParams{}); app != nil || err != nil {
		t.Fatalf("CreateAccessApplication() = (%+v, %v), want nil nil", app, err)
	}
	if app, err := mock.GetAccessApplication(ctx, "account", "app"); app != nil || err != nil {
		t.Fatalf("GetAccessApplication() = (%+v, %v), want nil nil", app, err)
	}
	if app, err := mock.UpdateAccessApplication(ctx, "account", "app", ApplicationParams{}); app != nil || err != nil {
		t.Fatalf("UpdateAccessApplication() = (%+v, %v), want nil nil", app, err)
	}
	if err := mock.DeleteAccessApplication(ctx, "account", "app"); err != nil {
		t.Fatalf("DeleteAccessApplication() error = %v", err)
	}
	if apps, err := mock.ListAccessApplications(ctx, "account"); apps != nil || err != nil {
		t.Fatalf("ListAccessApplications() = (%+v, %v), want nil nil", apps, err)
	}
	if policy, err := mock.CreateAccessPolicy(ctx, "account", PolicyParams{}); policy != nil || err != nil {
		t.Fatalf("CreateAccessPolicy() = (%+v, %v), want nil nil", policy, err)
	}
	if policy, err := mock.GetAccessPolicy(ctx, "account", "policy"); policy != nil || err != nil {
		t.Fatalf("GetAccessPolicy() = (%+v, %v), want nil nil", policy, err)
	}
	if policy, err := mock.UpdateAccessPolicy(ctx, "account", "policy", PolicyParams{}); policy != nil || err != nil {
		t.Fatalf("UpdateAccessPolicy() = (%+v, %v), want nil nil", policy, err)
	}
	if err := mock.DeleteAccessPolicy(ctx, "account", "policy"); err != nil {
		t.Fatalf("DeleteAccessPolicy() error = %v", err)
	}
	if policies, err := mock.ListAccessPolicies(ctx, "account"); policies != nil || err != nil {
		t.Fatalf("ListAccessPolicies() = (%+v, %v), want nil nil", policies, err)
	}
	if tag, err := mock.CreateAccessTag(ctx, "account", "tag"); tag != nil || err != nil {
		t.Fatalf("CreateAccessTag() = (%+v, %v), want nil nil", tag, err)
	}
	if tags, err := mock.ListAccessTags(ctx, "account"); tags != nil || err != nil {
		t.Fatalf("ListAccessTags() = (%+v, %v), want nil nil", tags, err)
	}
	if err := mock.DeleteAccessTag(ctx, "account", "tag"); err != nil {
		t.Fatalf("DeleteAccessTag() error = %v", err)
	}
	if token, err := mock.CreateServiceToken(ctx, "account", ServiceTokenParams{}); token != nil || err != nil {
		t.Fatalf("CreateServiceToken() = (%+v, %v), want nil nil", token, err)
	}
	if token, err := mock.GetServiceToken(ctx, "account", "token"); token != nil || err != nil {
		t.Fatalf("GetServiceToken() = (%+v, %v), want nil nil", token, err)
	}
	if token, err := mock.UpdateServiceToken(ctx, "account", "token", ServiceTokenParams{}); token != nil || err != nil {
		t.Fatalf("UpdateServiceToken() = (%+v, %v), want nil nil", token, err)
	}
	if err := mock.DeleteServiceToken(ctx, "account", "token"); err != nil {
		t.Fatalf("DeleteServiceToken() error = %v", err)
	}
	if tokens, err := mock.ListServiceTokens(ctx, "account"); tokens != nil || err != nil {
		t.Fatalf("ListServiceTokens() = (%+v, %v), want nil nil", tokens, err)
	}
	if token, err := mock.RotateServiceToken(ctx, "account", "token"); token != nil || err != nil {
		t.Fatalf("RotateServiceToken() = (%+v, %v), want nil nil", token, err)
	}
	if token, err := mock.RefreshServiceToken(ctx, "account", "token"); token != nil || err != nil {
		t.Fatalf("RefreshServiceToken() = (%+v, %v), want nil nil", token, err)
	}
}

func testClientImpl(t *testing.T, handler http.HandlerFunc) *clientImpl {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &clientImpl{api: cf.NewClient(
		option.WithAPIToken("test-token"),
		option.WithBaseURL(server.URL),
	)}
}

func malformedAccessApplicationEnvelope() string {
	return `{
		"success": true,
		"errors": [],
		"messages": [],
		"result": {
			"id": "app-1",
			"aud": "aud-1",
			"name": "app",
			"domain": "app.example.com",
			"type": "self_hosted",
			"tags": 1
		}
	}`
}

func malformedAccessApplicationListEnvelope() string {
	return `{
		"result": [{
			"id": "app-1",
			"aud": "aud-1",
			"name": "app",
			"domain": "app.example.com",
			"type": "self_hosted",
			"tags": 1
		}],
		"result_info": {"page": 1, "per_page": 20}
	}`
}

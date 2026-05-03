package cloudflare

import (
	"context"
	"testing"

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

package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"github.com/go-logr/logr"
)

type currentTokenSecret struct{ stale bool }

func (s currentTokenSecret) ServiceTokenSecretNeedsRefresh(context.Context, string, string) (bool, error) {
	return s.stale, nil
}
func (currentTokenSecret) BeginServiceTokenRotation(context.Context, string) error {
	return fmt.Errorf("unexpected rotation")
}
func (currentTokenSecret) WriteSecret(context.Context, string, map[string][]byte) error {
	return fmt.Errorf("unexpected secret write")
}

func TestHealthyTokenRenewalDoesNotWithdrawForwarding(t *testing.T) {
	for _, scenario := range []string{"healthy", "expired", "duration edit", "name edit", "stale credential"} {
		t.Run(scenario, func(t *testing.T) {
			token := cloudflare.ServiceToken{ID: "token", Name: "svc", ClientID: "client", Duration: "1h", ExpiresAt: time.Now().Add(time.Minute)}
			switch scenario {
			case "expired":
				token.ExpiresAt = time.Now().Add(-time.Minute)
			case "duration edit":
				token.Duration = "2h"
			case "name edit":
				token.Name = "old"
			}
			raw := cloudflare.NewMockClient()
			raw.ListServiceTokensFunc = func(context.Context, string) ([]cloudflare.ServiceToken, error) {
				return []cloudflare.ServiceToken{token}, nil
			}
			extensions := 0
			raw.ExtendServiceTokenExpirationFunc = func(context.Context, string, string, cloudflare.ServiceToken) (*cloudflare.ServiceToken, error) {
				extensions++
				token.ExpiresAt = time.Now().Add(time.Hour)
				return &token, nil
			}
			withdrawals := 0
			guarded := &accessMutationClient{Client: raw, before: func(context.Context) error { withdrawals++; return fmt.Errorf("forwarding still active") }}
			service := cloudflare.NewAccessService(guarded, logr.Discard())
			_, err := service.EnsureServiceTokenByID(context.Background(), "account", "token", cloudflare.ServiceTokenParams{Name: "svc", Duration: "1h"}, currentTokenSecret{stale: scenario == "stale credential"})
			if scenario == "healthy" {
				if err != nil || extensions != 1 || withdrawals != 0 {
					t.Fatalf("healthy extension: %v, extensions=%d withdrawals=%d", err, extensions, withdrawals)
				}
			} else if err == nil || extensions != 0 || withdrawals != 1 {
				t.Fatalf("unsafe extension: %v, extensions=%d withdrawals=%d", err, extensions, withdrawals)
			}
		})
	}
}

func TestTokenRenewalRequeueUsesEarliestDeadline(t *testing.T) {
	policy := baseAccessPolicy("app", "policy")
	policy.Spec.ServiceTokens = []cfgatev1alpha1.ServiceTokenConfig{
		{Name: "long", Duration: "48h", SecretRef: cfgatev1alpha1.ServiceTokenSecretRef{Name: "long-secret"}},
		{Name: "soon", Duration: "1h", SecretRef: cfgatev1alpha1.ServiceTokenSecretRef{Name: "soon-secret"}},
	}
	raw := cloudflare.NewMockClient()
	raw.CreateServiceTokenFunc = func(_ context.Context, _ string, params cloudflare.ServiceTokenParams) (*cloudflare.ServiceTokenWithSecret, error) {
		lifetime := 48 * time.Hour
		if params.Name == "soon" {
			lifetime = 8 * time.Minute
		}
		return &cloudflare.ServiceTokenWithSecret{ServiceToken: cloudflare.ServiceToken{ID: params.Name, ClientID: params.Name, Name: params.Name, Duration: params.Duration, ExpiresAt: time.Now().Add(lifetime)}, ClientSecret: "secret"}, nil
	}
	reconciler := newAccessPolicyReconciler(t, raw, policy)
	deadline, err := reconciler.syncServiceTokens(context.Background(), cloudflare.NewAccessService(raw, logr.Discard()), "account", policy)
	if err != nil {
		t.Fatal(err)
	}
	delay := time.Until(deadline)
	if delay < 59*time.Second || delay > time.Minute {
		t.Fatalf("next check %v: expected one minute, not fixed five-minute poll", delay)
	}
}

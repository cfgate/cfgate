package cloudflare

import (
	"context"
	"fmt"
	"time"
)

// ServiceTokenRenewalWindow renews short-lived tokens within their last tenth,
// and longer-lived tokens within their last day.
func ServiceTokenRenewalWindow(lifetime time.Duration) time.Duration {
	return min(lifetime/10, 24*time.Hour)
}

// ServiceTokenRenewalTime reserves up to one minute before the renewal window
// for queue delay and a retry. The window itself remains available for retries.
func ServiceTokenRenewalTime(expires time.Time, lifetime time.Duration) time.Time {
	window := ServiceTokenRenewalWindow(lifetime)
	return expires.Add(-window - min(window/6, time.Minute))
}

func (s *AccessService) EnsureServiceToken(ctx context.Context, accountID string, params ServiceTokenParams, store SecretWriter) (*ServiceToken, error) {
	return s.EnsureServiceTokenByID(ctx, accountID, "", params, store)
}

// EnsureServiceTokenByID treats expiration, desired duration, and credential
// distribution as separate state. Updating duration renews expiration; rotating
// a secret does not. All credential mutations require a durable local intent.
func (s *AccessService) EnsureServiceTokenByID(ctx context.Context, accountID, statusID string, params ServiceTokenParams, store SecretWriter) (*ServiceToken, error) {
	if params.Duration == "" {
		params.Duration = "8760h"
	}
	lifetime, err := time.ParseDuration(params.Duration)
	if err != nil || lifetime <= 0 {
		return nil, fmt.Errorf("invalid service token duration %q", params.Duration)
	}
	if params.RotationOverlap < 0 || params.RotationOverlap > 30*24*time.Hour {
		return nil, fmt.Errorf("rotation overlap must be between zero and 720h")
	}
	if store == nil {
		return nil, fmt.Errorf("service token credential store is required")
	}
	tokens, err := s.client.ListServiceTokens(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to list service tokens: %w", err)
	}
	var existing *ServiceToken
	for i := range tokens {
		if (statusID != "" && tokens[i].ID == statusID) || (statusID == "" && tokens[i].Name == params.Name) {
			if existing != nil {
				return nil, fmt.Errorf("ambiguous service token identity for %q", params.Name)
			}
			existing = &tokens[i]
		}
	}
	clientID := ""
	if existing != nil {
		clientID = existing.ClientID
	}
	stale, err := store.ServiceTokenSecretNeedsRefresh(ctx, params.Name, clientID)
	if err != nil {
		return nil, fmt.Errorf("failed to check service token secret: %w", err)
	}
	if existing == nil {
		if err := store.BeginServiceTokenRotation(ctx, params.Name); err != nil {
			return nil, fmt.Errorf("recording credential intent: %w", err)
		}
		created, err := s.client.CreateServiceToken(ctx, accountID, params)
		if err != nil {
			return nil, fmt.Errorf("failed to create service token: %w", err)
		}
		if err := storeTokenCredential(ctx, store, params.Name, created); err != nil {
			return nil, err
		}
		if created.ExpiresAt.IsZero() {
			observed, err := s.client.GetServiceToken(ctx, accountID, created.ID)
			if err != nil {
				return nil, fmt.Errorf("reading created service token: %w", err)
			}
			if observed == nil || observed.ID != created.ID || observed.ClientID != created.ClientID {
				return nil, fmt.Errorf("created service token could not be verified")
			}
			created.ServiceToken = *observed
		}
		if err := serviceTokenUsable(&created.ServiceToken, s.now()); err != nil {
			return nil, err
		}
		return &created.ServiceToken, nil
	}
	if existing.ID == "" || existing.ClientID == "" {
		return nil, fmt.Errorf("service token has incomplete identity")
	}
	if existing.Enabled != nil && !*existing.Enabled {
		return nil, fmt.Errorf("service token %s is disabled", existing.ID)
	}
	now := s.now()
	observedDuration, parseErr := time.ParseDuration(existing.Duration)
	renew := !existing.ExpiresAt.IsZero() && !now.Before(ServiceTokenRenewalTime(existing.ExpiresAt, lifetime))
	if parseErr != nil || observedDuration != lifetime || existing.Name != params.Name || renew {
		var updated *ServiceToken
		var err error
		if renew && !stale && parseErr == nil && observedDuration == lifetime && existing.Name == params.Name && existing.ExpiresAt.After(now) {
			updated, err = s.client.ExtendServiceTokenExpiration(ctx, accountID, existing.ID, *existing)
		} else {
			updated, err = s.client.UpdateServiceToken(ctx, accountID, existing.ID, params)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to renew or update service token: %w", err)
		}
		if updated == nil || updated.ID != existing.ID || updated.ClientID != existing.ClientID {
			return nil, fmt.Errorf("service token update returned mismatched identity")
		}
		if updated.ExpiresAt.IsZero() || !updated.ExpiresAt.After(now) {
			return nil, fmt.Errorf("service token update did not confirm a future expiration")
		}
		existing = updated
	}
	if err := serviceTokenUsable(existing, now); err != nil {
		return nil, err
	}
	if !stale {
		return existing, nil
	}
	if err := store.BeginServiceTokenRotation(ctx, params.Name); err != nil {
		return nil, fmt.Errorf("recording credential intent: %w", err)
	}
	rotation := ServiceTokenRotateParams{}
	if params.RotationOverlap > 0 {
		rotation.PreviousClientSecretExpiresAt = now.Add(params.RotationOverlap)
	}
	rotated, err := s.client.RotateServiceToken(ctx, accountID, existing.ID, rotation)
	if err != nil {
		return nil, fmt.Errorf("failed to rotate service token: %w", err)
	}
	if rotated == nil || rotated.ID != existing.ID || rotated.ClientID != existing.ClientID {
		return nil, fmt.Errorf("service token rotation returned mismatched identity")
	}
	// Rotation responses omit expiration. Preserve the separately observed renewal.
	rotated.ExpiresAt = existing.ExpiresAt
	if err := storeTokenCredential(ctx, store, params.Name, rotated); err != nil {
		return nil, err
	}
	return &rotated.ServiceToken, nil
}

func storeTokenCredential(ctx context.Context, store SecretWriter, name string, token *ServiceTokenWithSecret) error {
	if token == nil || token.ID == "" || token.ClientID == "" || token.ClientSecret == "" {
		return fmt.Errorf("service token response has incomplete credentials")
	}
	if err := store.WriteSecret(ctx, name, map[string][]byte{
		"CF_ACCESS_CLIENT_ID": []byte(token.ClientID), "CF_ACCESS_CLIENT_SECRET": []byte(token.ClientSecret),
	}); err != nil {
		return fmt.Errorf("failed to store service token secret: %w", err)
	}
	return nil
}

func serviceTokenUsable(token *ServiceToken, now time.Time) error {
	if token.Enabled != nil && !*token.Enabled {
		return fmt.Errorf("service token %s is disabled", token.ID)
	}
	if token.ExpiresAt.IsZero() || !token.ExpiresAt.After(now) {
		return fmt.Errorf("service token %s has no confirmed future expiration", token.ID)
	}
	return nil
}

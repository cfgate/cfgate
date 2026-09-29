package cloudflare

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

// DefaultAPITokenKey is the default Secret data key for Cloudflare authentication.
const DefaultAPITokenKey = "CLOUDFLARE_API_TOKEN"

// APITokenKey normalizes an omitted token key to the default Secret data key.
func APITokenKey(key string) string {
	if key == "" {
		return DefaultAPITokenKey
	}
	return key
}

func credentialCacheKey(secret *corev1.Secret, tokenKey string) string {
	return credentialSettingsCacheKey(secret, tokenKey, DefaultClientSettings())
}

func credentialSettingsCacheKey(secret *corev1.Secret, tokenKey string, settings ClientSettings) string {
	return fmt.Sprintf("%s:%s:%s:%d:%d:%d", secret.UID, secret.ResourceVersion, APITokenKey(tokenKey), settings.AttemptTimeout, settings.MaxIngressRules, settings.MaxConfigurationBytes)
}

// NewClientFromSecret selects and validates one Secret data key before consulting
// the shared credential cache. It never falls back to a different token key.
func NewClientFromSecret(ctx context.Context, secret *corev1.Secret, tokenKey string, cache *CredentialCache, settings ClientSettings) (Client, error) {
	settings, err := NormalizeClientSettings(settings)
	if err != nil {
		return nil, err
	}
	tokenKey = APITokenKey(tokenKey)
	token, ok := secret.Data[tokenKey]
	if !ok || len(token) == 0 {
		return nil, fmt.Errorf("API token key %q is missing or empty in secret", tokenKey)
	}
	create := func() (Client, error) { return NewClient(string(token), WithClientSettings(settings)) }
	if cache != nil {
		return cache.GetOrCreateWithSettings(ctx, secret, tokenKey, settings, create)
	}
	return create()
}

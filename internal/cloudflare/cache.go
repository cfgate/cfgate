package cloudflare

import (
	"context"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
)

const (
	// DefaultCacheTTL is the default TTL for cached credentials.
	DefaultCacheTTL = 30 * time.Second
)

// CredentialCache caches validated Cloudflare clients to avoid repeated API validations.
// The cache key includes Secret UID, ResourceVersion, and the selected token key.
// Account IDs are request parameters, not client authentication options.
type CredentialCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	ttl     time.Duration
}

// cacheEntry stores a cached client with its expiration time.
type cacheEntry struct {
	client    Client    // The cached Cloudflare client
	expiresAt time.Time // When this entry expires
}

// NewCredentialCache creates a new CredentialCache with the specified TTL.
func NewCredentialCache(ttl time.Duration) *CredentialCache {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &CredentialCache{
		entries: make(map[string]cacheEntry),
		ttl:     ttl,
	}
}

// cacheKey selects the default token key for the legacy cache accessors.
func cacheKey(secret *corev1.Secret) string {
	return credentialCacheKey(secret, "")
}

// Get retrieves a cached client for the given secret.
// Returns nil if the entry is not found or expired.
func (c *CredentialCache) Get(secret *corev1.Secret) Client {
	return c.get(credentialCacheKey(secret, ""))
}

func (c *CredentialCache) get(key string) Client {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()

	if !ok {
		return nil
	}

	if time.Now().After(entry.expiresAt) {
		// Entry expired, remove it
		c.mu.Lock()
		delete(c.entries, key)
		c.mu.Unlock()
		return nil
	}

	return entry.client
}

// Set stores a client in the cache for the given secret.
func (c *CredentialCache) Set(secret *corev1.Secret, client Client) {
	c.set(credentialCacheKey(secret, ""), client)
}

func (c *CredentialCache) set(key string, client Client) {
	c.mu.Lock()
	c.entries[key] = cacheEntry{
		client:    client,
		expiresAt: time.Now().Add(c.ttl),
	}
	c.mu.Unlock()
}

// GetOrCreate retrieves a cached client or creates a new one using the provided function.
// The createFn is only called if no valid cached entry exists.
// Expired entries are cleaned up on each call to prevent unbounded growth.
func (c *CredentialCache) GetOrCreate(ctx context.Context, secret *corev1.Secret, createFn func() (Client, error)) (Client, error) {
	return c.GetOrCreateForKey(ctx, secret, "", createFn)
}

// GetOrCreateForKey isolates cached clients by their selected API token data key.
// An empty key selects CLOUDFLARE_API_TOKEN. Factories must not vary other authentication settings.
func (c *CredentialCache) GetOrCreateForKey(ctx context.Context, secret *corev1.Secret, tokenKey string, createFn func() (Client, error)) (Client, error) {
	return c.GetOrCreateWithSettings(ctx, secret, tokenKey, DefaultClientSettings(), createFn)
}

// GetOrCreateWithSettings isolates clients by authentication and request limits.
func (c *CredentialCache) GetOrCreateWithSettings(ctx context.Context, secret *corev1.Secret, tokenKey string, settings ClientSettings, createFn func() (Client, error)) (Client, error) {
	settings, err := NormalizeClientSettings(settings)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.Cleanup()
	key := credentialSettingsCacheKey(secret, tokenKey, settings)
	if client := c.get(key); client != nil {
		return client, nil
	}

	// Create new client
	client, err := createFn()
	if err != nil {
		return nil, err
	}

	// Store in cache
	c.set(key, client)

	return client, nil
}

// Invalidate removes all selected-token entries for one Secret version.
func (c *CredentialCache) Invalidate(secret *corev1.Secret) {
	prefix := string(secret.UID) + ":" + secret.ResourceVersion + ":"
	c.mu.Lock()
	for key := range c.entries {
		if strings.HasPrefix(key, prefix) {
			delete(c.entries, key)
		}
	}
	c.mu.Unlock()
}

// Clear removes all entries from the cache.
func (c *CredentialCache) Clear() {
	c.mu.Lock()
	c.entries = make(map[string]cacheEntry)
	c.mu.Unlock()
}

// Cleanup removes expired entries from the cache.
// This can be called periodically to prevent memory growth.
func (c *CredentialCache) Cleanup() {
	now := time.Now()

	c.mu.Lock()
	for key, entry := range c.entries {
		if now.After(entry.expiresAt) {
			delete(c.entries, key)
		}
	}
	c.mu.Unlock()
}

// Size returns the current number of entries in the cache.
func (c *CredentialCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

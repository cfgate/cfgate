package cloudflare

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func testSecret(uid, version string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			UID:             types.UID(uid),
			ResourceVersion: version,
		},
	}
}

func TestCacheNew(t *testing.T) {
	tests := []struct {
		name    string
		ttl     time.Duration
		wantTTL time.Duration
	}{
		{"default TTL", DefaultCacheTTL, DefaultCacheTTL},
		{"custom TTL", 60 * time.Second, 60 * time.Second},
		{"zero TTL defaults", 0, DefaultCacheTTL},
		{"negative TTL defaults", -time.Second, DefaultCacheTTL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCredentialCache(tt.ttl)
			if c.ttl != tt.wantTTL {
				t.Errorf("ttl = %v, want %v", c.ttl, tt.wantTTL)
			}
			if c.Size() != 0 {
				t.Errorf("Size() = %d, want 0", c.Size())
			}
		})
	}
}

func TestCacheKey(t *testing.T) {
	tests := []struct {
		name    string
		uid     string
		version string
		want    string
	}{
		{"normal key", "abc-123", "42", "abc-123:42"},
		{"different UID same version", "xyz-456", "42", "xyz-456:42"},
		{"same UID different version", "abc-123", "99", "abc-123:99"},
		{"empty UID", "", "42", ":42"},
		{"empty version", "abc-123", "", "abc-123:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testSecret(tt.uid, tt.version)
			if got := cacheKey(s); got != tt.want {
				t.Errorf("cacheKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCacheGet(t *testing.T) {
	t.Run("valid entry", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		s := testSecret("uid-1", "v1")
		client := &MockClient{}
		c.Set(s, client)

		if got := c.Get(s); got != client {
			t.Error("Get() did not return the cached client")
		}
	})

	t.Run("expired entry", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		s := testSecret("uid-1", "v1")
		key := cacheKey(s)
		c.entries[key] = cacheEntry{
			client:    &MockClient{},
			expiresAt: time.Now().Add(-time.Second),
		}

		if got := c.Get(s); got != nil {
			t.Error("Get() returned non-nil for expired entry")
		}
	})

	t.Run("missing key", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		s := testSecret("nonexistent", "v1")

		if got := c.Get(s); got != nil {
			t.Error("Get() returned non-nil for missing key")
		}
	})

	t.Run("nil client entry", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		s := testSecret("uid-1", "v1")
		key := cacheKey(s)
		c.entries[key] = cacheEntry{
			client:    nil,
			expiresAt: time.Now().Add(time.Minute),
		}

		if got := c.Get(s); got != nil {
			t.Error("Get() returned non-nil for nil client entry")
		}
	})

	t.Run("expired entry is deleted", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		s := testSecret("uid-1", "v1")
		key := cacheKey(s)
		c.entries[key] = cacheEntry{
			client:    &MockClient{},
			expiresAt: time.Now().Add(-time.Second),
		}

		c.Get(s)

		if c.Size() != 0 {
			t.Errorf("expired entry not deleted, Size() = %d", c.Size())
		}
	})

	t.Run("get after set", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		s := testSecret("uid-1", "v1")
		client := &MockClient{}
		c.Set(s, client)

		if got := c.Get(s); got != client {
			t.Error("Get() after Set() did not return same client")
		}
	})
}

func TestCacheSet(t *testing.T) {
	t.Run("new entry", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		s := testSecret("uid-1", "v1")
		client := &MockClient{}
		c.Set(s, client)

		if c.Size() != 1 {
			t.Errorf("Size() = %d, want 1", c.Size())
		}
		if got := c.Get(s); got != client {
			t.Error("Get() did not return the set client")
		}
	})

	t.Run("overwrite existing", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		s := testSecret("uid-1", "v1")
		c.Set(s, &MockClient{})

		newClient := &MockClient{}
		c.Set(s, newClient)

		if c.Size() != 1 {
			t.Errorf("Size() = %d, want 1", c.Size())
		}
		if got := c.Get(s); got != newClient {
			t.Error("Get() did not return the overwritten client")
		}
	})

	t.Run("set updates expiry", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		s := testSecret("uid-1", "v1")
		key := cacheKey(s)
		c.entries[key] = cacheEntry{
			client:    &MockClient{},
			expiresAt: time.Now().Add(time.Millisecond),
		}

		c.Set(s, &MockClient{})

		entry := c.entries[key]
		if time.Until(entry.expiresAt) < 30*time.Second {
			t.Error("Set() did not refresh expiry")
		}
	})
}

func TestCacheGetOrCreate(t *testing.T) {
	t.Run("cache hit", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		s := testSecret("uid-1", "v1")
		cached := &MockClient{}
		c.Set(s, cached)

		called := false
		got, err := c.GetOrCreate(context.Background(), s, func() (Client, error) {
			called = true
			return &MockClient{}, nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != cached {
			t.Error("did not return cached client")
		}
		if called {
			t.Error("createFn was called on cache hit")
		}
	})

	t.Run("cache miss", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		s := testSecret("uid-1", "v1")
		created := &MockClient{}

		got, err := c.GetOrCreate(context.Background(), s, func() (Client, error) {
			return created, nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != created {
			t.Error("did not return created client")
		}
		if c.Size() != 1 {
			t.Errorf("Size() = %d, want 1", c.Size())
		}
	})

	t.Run("createFn error", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		s := testSecret("uid-1", "v1")
		wantErr := fmt.Errorf("auth failed")

		got, err := c.GetOrCreate(context.Background(), s, func() (Client, error) {
			return nil, wantErr
		})
		if err != wantErr {
			t.Errorf("error = %v, want %v", err, wantErr)
		}
		if got != nil {
			t.Error("returned non-nil client on error")
		}
		if c.Size() != 0 {
			t.Errorf("Size() = %d, want 0", c.Size())
		}
	})

	t.Run("expired entry triggers create", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		s := testSecret("uid-1", "v1")
		key := cacheKey(s)
		c.entries[key] = cacheEntry{
			client:    &MockClient{},
			expiresAt: time.Now().Add(-time.Second),
		}

		created := &MockClient{}
		got, err := c.GetOrCreate(context.Background(), s, func() (Client, error) {
			return created, nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != created {
			t.Error("did not create new client for expired entry")
		}
	})

	t.Run("createFn not called on hit", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		s := testSecret("uid-1", "v1")
		c.Set(s, &MockClient{})

		_, err := c.GetOrCreate(context.Background(), s, func() (Client, error) {
			t.Fatal("createFn must not be called on cache hit")
			return nil, nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestCacheInvalidate(t *testing.T) {
	t.Run("entry exists", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		s := testSecret("uid-1", "v1")
		c.Set(s, &MockClient{})

		c.Invalidate(s)

		if c.Size() != 0 {
			t.Errorf("Size() = %d, want 0", c.Size())
		}
		if got := c.Get(s); got != nil {
			t.Error("Get() returned non-nil after Invalidate()")
		}
	})

	t.Run("entry absent", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		s := testSecret("uid-1", "v1")

		c.Invalidate(s)

		if c.Size() != 0 {
			t.Errorf("Size() = %d, want 0", c.Size())
		}
	})

	t.Run("other entries unaffected", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		s1 := testSecret("uid-1", "v1")
		s2 := testSecret("uid-2", "v1")
		client2 := &MockClient{}
		c.Set(s1, &MockClient{})
		c.Set(s2, client2)

		c.Invalidate(s1)

		if c.Size() != 1 {
			t.Errorf("Size() = %d, want 1", c.Size())
		}
		if got := c.Get(s2); got != client2 {
			t.Error("other entry was affected by Invalidate()")
		}
	})
}

func TestCacheClear(t *testing.T) {
	t.Run("non-empty cache", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		for i := 0; i < 3; i++ {
			c.Set(testSecret(fmt.Sprintf("uid-%d", i), "v1"), &MockClient{})
		}

		c.Clear()

		if c.Size() != 0 {
			t.Errorf("Size() = %d, want 0", c.Size())
		}
	})

	t.Run("empty cache", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)

		c.Clear()

		if c.Size() != 0 {
			t.Errorf("Size() = %d, want 0", c.Size())
		}
	})
}

func TestCacheCleanup(t *testing.T) {
	t.Run("all expired", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		for i := 0; i < 3; i++ {
			key := cacheKey(testSecret(fmt.Sprintf("uid-%d", i), "v1"))
			c.entries[key] = cacheEntry{
				client:    &MockClient{},
				expiresAt: time.Now().Add(-time.Second),
			}
		}

		c.Cleanup()

		if c.Size() != 0 {
			t.Errorf("Size() = %d, want 0", c.Size())
		}
	})

	t.Run("none expired", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		for i := 0; i < 3; i++ {
			c.Set(testSecret(fmt.Sprintf("uid-%d", i), "v1"), &MockClient{})
		}

		c.Cleanup()

		if c.Size() != 3 {
			t.Errorf("Size() = %d, want 3", c.Size())
		}
	})

	t.Run("mixed", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		c.Set(testSecret("uid-0", "v1"), &MockClient{})
		c.Set(testSecret("uid-1", "v1"), &MockClient{})
		key := cacheKey(testSecret("uid-2", "v1"))
		c.entries[key] = cacheEntry{
			client:    &MockClient{},
			expiresAt: time.Now().Add(-time.Second),
		}

		c.Cleanup()

		if c.Size() != 2 {
			t.Errorf("Size() = %d, want 2", c.Size())
		}
	})

	t.Run("empty cache", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)

		c.Cleanup()

		if c.Size() != 0 {
			t.Errorf("Size() = %d, want 0", c.Size())
		}
	})

	t.Run("exact expiry boundary", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		key := cacheKey(testSecret("uid-1", "v1"))
		// Set expiresAt to now. Cleanup captures its own time.Now() which will
		// be strictly after this value, so the entry is removed.
		c.entries[key] = cacheEntry{
			client:    &MockClient{},
			expiresAt: time.Now(),
		}

		c.Cleanup()

		if c.Size() != 0 {
			t.Errorf("Size() = %d, want 0", c.Size())
		}
	})
}

func TestCacheSize(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		if c.Size() != 0 {
			t.Errorf("Size() = %d, want 0", c.Size())
		}
	})

	t.Run("non-empty", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		for i := 0; i < 3; i++ {
			c.Set(testSecret(fmt.Sprintf("uid-%d", i), "v1"), &MockClient{})
		}

		if c.Size() != 3 {
			t.Errorf("Size() = %d, want 3", c.Size())
		}
	})

	t.Run("after cleanup", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		c.Set(testSecret("uid-0", "v1"), &MockClient{})
		c.Set(testSecret("uid-1", "v1"), &MockClient{})
		key := cacheKey(testSecret("uid-2", "v1"))
		c.entries[key] = cacheEntry{
			client:    &MockClient{},
			expiresAt: time.Now().Add(-time.Second),
		}

		if c.Size() != 3 {
			t.Errorf("before cleanup: Size() = %d, want 3", c.Size())
		}

		c.Cleanup()

		if c.Size() != 2 {
			t.Errorf("after cleanup: Size() = %d, want 2", c.Size())
		}
	})
}

func TestCacheConcurrency(t *testing.T) {
	t.Run("parallel reads", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		s := testSecret("uid-1", "v1")
		client := &MockClient{}
		c.Set(s, client)

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if got := c.Get(s); got != client {
					t.Errorf("concurrent Get() returned wrong client")
				}
			}()
		}
		wg.Wait()
	})

	t.Run("concurrent writes", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				c.Set(testSecret(fmt.Sprintf("uid-%d", n), "v1"), &MockClient{})
			}(i)
		}
		wg.Wait()

		if c.Size() != 100 {
			t.Errorf("Size() = %d, want 100", c.Size())
		}
	})

	t.Run("read and write", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		s := testSecret("uid-1", "v1")
		c.Set(s, &MockClient{})

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			if i%2 == 0 {
				go func() {
					defer wg.Done()
					c.Get(s)
				}()
			} else {
				go func() {
					defer wg.Done()
					c.Set(s, &MockClient{})
				}()
			}
		}
		wg.Wait()
	})

	t.Run("concurrent GetOrCreate", func(t *testing.T) {
		// GetOrCreate is not atomic: concurrent calls for the same missing key
		// may both invoke createFn. This is by design; client creation is
		// idempotent and last-write-wins is correct.
		c := NewCredentialCache(time.Minute)
		s := testSecret("uid-1", "v1")

		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := c.GetOrCreate(context.Background(), s, func() (Client, error) {
					return &MockClient{}, nil
				})
				if err != nil {
					t.Errorf("GetOrCreate() error: %v", err)
				}
			}()
		}
		wg.Wait()

		if c.Size() != 1 {
			t.Errorf("Size() = %d, want 1", c.Size())
		}
	})

	t.Run("cleanup and get", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		s := testSecret("uid-1", "v1")
		c.Set(s, &MockClient{})

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			if i%2 == 0 {
				go func() {
					defer wg.Done()
					c.Cleanup()
				}()
			} else {
				go func() {
					defer wg.Done()
					c.Get(s)
				}()
			}
		}
		wg.Wait()
	})

	t.Run("invalidate and get", func(t *testing.T) {
		// Exercises the TOCTOU window in Get (RUnlock-to-Lock gap for expired
		// entry deletion). Safe because Go map delete on a missing key is a no-op.
		c := NewCredentialCache(time.Minute)
		s := testSecret("uid-1", "v1")
		c.Set(s, &MockClient{})

		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				c.Invalidate(s)
			}()
			go func() {
				defer wg.Done()
				c.Get(s)
			}()
		}
		wg.Wait()
	})

	t.Run("clear and operations", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		s := testSecret("uid-1", "v1")
		c.Set(s, &MockClient{})

		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(1)
			switch i % 4 {
			case 0:
				go func() {
					defer wg.Done()
					c.Clear()
				}()
			case 1:
				go func() {
					defer wg.Done()
					c.Get(s)
				}()
			case 2:
				go func() {
					defer wg.Done()
					c.Set(s, &MockClient{})
				}()
			default:
				go func() {
					defer wg.Done()
					c.Size()
				}()
			}
		}
		wg.Wait()
	})
}

func TestCacheTTLBoundary(t *testing.T) {
	t.Run("not expired within TTL", func(t *testing.T) {
		c := NewCredentialCache(50 * time.Millisecond)
		s := testSecret("uid-1", "v1")
		client := &MockClient{}
		c.Set(s, client)

		if got := c.Get(s); got != client {
			t.Error("Get() within TTL returned nil")
		}
	})

	t.Run("expired after TTL", func(t *testing.T) {
		c := NewCredentialCache(10 * time.Millisecond)
		s := testSecret("uid-1", "v1")
		c.Set(s, &MockClient{})

		time.Sleep(30 * time.Millisecond)

		if got := c.Get(s); got != nil {
			t.Error("Get() after TTL returned non-nil")
		}
	})

	t.Run("key rotation invalidates old", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		secretV1 := testSecret("uid-1", "v1")
		secretV2 := testSecret("uid-1", "v2")
		c.Set(secretV1, &MockClient{})

		// New ResourceVersion produces a different cache key
		if got := c.Get(secretV2); got != nil {
			t.Error("Get() with new version should not find old entry")
		}
	})

	t.Run("key rotation preserves new", func(t *testing.T) {
		c := NewCredentialCache(time.Minute)
		secretV1 := testSecret("uid-1", "v1")
		secretV2 := testSecret("uid-1", "v2")
		clientV2 := &MockClient{}
		c.Set(secretV1, &MockClient{})
		c.Set(secretV2, clientV2)

		if got := c.Get(secretV2); got != clientV2 {
			t.Error("Get() with new version should return new client")
		}
	})

	t.Run("at boundary", func(t *testing.T) {
		c := NewCredentialCache(100 * time.Millisecond)
		s := testSecret("uid-1", "v1")
		client := &MockClient{}
		c.Set(s, client)

		time.Sleep(50 * time.Millisecond)

		if got := c.Get(s); got != client {
			t.Error("Get() before TTL boundary returned nil")
		}
	})
}

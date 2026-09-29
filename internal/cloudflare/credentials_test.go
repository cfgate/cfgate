package cloudflare

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type credentialHeaderTransport struct{ headers []string }

func (r *credentialHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.headers = append(r.headers, req.Header.Get("Authorization"))
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"success":true,"result":[],"errors":[],"messages":[]}`)), Request: req}, nil
}

func TestSecretClientTokenSelection(t *testing.T) {
	transport := &credentialHeaderTransport{}
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	secret := testSecret("one-secret", "1")
	secret.Data = map[string][]byte{"restricted": []byte("restricted-test-token"), "privileged": []byte("privileged-test-token"), DefaultAPITokenKey: []byte("default-test-token")}
	cache := NewCredentialCache(0)
	for _, key := range []string{"restricted", "privileged", "restricted", "", DefaultAPITokenKey} {
		c, err := NewClientFromSecret(context.Background(), secret, key, cache, ClientSettings{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = c.ListAccounts(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"Bearer restricted-test-token", "Bearer privileged-test-token", "Bearer restricted-test-token", "Bearer default-test-token", "Bearer default-test-token"}
	for i, header := range want {
		if transport.headers[i] != header {
			t.Errorf("request %d used incorrect selected credential", i)
		}
	}
	if cache.Size() != 3 {
		t.Fatalf("cache size=%d, want 3 distinct keys", cache.Size())
	}
	secret.ResourceVersion = "2"
	secret.Data["restricted"] = []byte("rotated-test-token")
	c, err := NewClientFromSecret(context.Background(), secret, "restricted", cache, ClientSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ListAccounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if transport.headers[5] != "Bearer rotated-test-token" {
		t.Fatal("rotated Secret reused prior credential")
	}
}

func TestSecretClientNeverFallsBack(t *testing.T) {
	secret := testSecret("one-secret", "1")
	secret.Data = map[string][]byte{DefaultAPITokenKey: []byte("default-test-token"), "empty": {}}
	for _, key := range []string{"missing", "empty"} {
		if _, err := NewClientFromSecret(context.Background(), secret, key, NewCredentialCache(0), ClientSettings{}); err == nil {
			t.Errorf("key %s: want rejection", key)
		}
	}
}

func TestSecretClientInvalidateSelectedKeys(t *testing.T) {
	cache := NewCredentialCache(0)
	secret := testSecret("secret", "1")
	secret.Data = map[string][]byte{"a": []byte("token-a"), "b": []byte("token-b")}
	for _, key := range []string{"a", "b"} {
		if _, err := NewClientFromSecret(context.Background(), secret, key, cache, ClientSettings{}); err != nil {
			t.Fatal(err)
		}
	}
	other := secret.DeepCopy()
	other.UID = "other"
	if _, err := NewClientFromSecret(context.Background(), other, "a", cache, ClientSettings{}); err != nil {
		t.Fatal(err)
	}
	cache.Invalidate(secret)
	if cache.Size() != 1 {
		t.Fatalf("invalidation retained selected keys or removed other Secret: size=%d", cache.Size())
	}
}

func TestSecretClientSettingsIsolation(t *testing.T) {
	secret := testSecret("secret", "1")
	secret.Data = map[string][]byte{DefaultAPITokenKey: []byte("test-token")}
	cache := NewCredentialCache(0)
	first, err := NewClientFromSecret(context.Background(), secret, "", cache, ClientSettings{})
	if err != nil {
		t.Fatal(err)
	}
	equivalent, err := NewClientFromSecret(context.Background(), secret, DefaultAPITokenKey, cache, DefaultClientSettings())
	if err != nil {
		t.Fatal(err)
	}
	if first != equivalent {
		t.Fatal("normalized defaults did not reuse client")
	}
	settings := DefaultClientSettings()
	settings.MaxIngressRules++
	second, err := NewClientFromSecret(context.Background(), secret, "", cache, settings)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("different request settings reused client")
	}
	settings.AttemptTimeout++
	third, err := NewClientFromSecret(context.Background(), secret, "", cache, settings)
	if err != nil {
		t.Fatal(err)
	}
	if second == third {
		t.Fatal("different deadline reused client")
	}
	settings.MaxConfigurationBytes++
	fourth, err := NewClientFromSecret(context.Background(), secret, "", cache, settings)
	if err != nil {
		t.Fatal(err)
	}
	if third == fourth {
		t.Fatal("different byte limit reused client")
	}
}

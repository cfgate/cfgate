package cloudflare

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAccessProtectionRetainsUnsupportedRemoteDestinationSignal(t *testing.T) {
	for _, raw := range []string{
		`{"destinations":[{"type":"public","uri":"app.example.com","overrides":[{"path_pattern":"/public","uri":"other.example.com"}]}]}`,
		`{"destinations":[{"type":"private","hostname":"app.internal"}]}`,
	} {
		app := &AccessApplication{}
		if err := applyApplicationExtras(app, raw); err != nil {
			t.Fatal(err)
		}
		if !app.UnsupportedProtection {
			t.Fatal("unknown security semantics disappeared in SDK conversion")
		}
	}
	app := &AccessApplication{}
	if err := applyApplicationExtras(app, `{"destinations":[{"type":"public","uri":"app.example.com"}]}`); err != nil {
		t.Fatal(err)
	}
	if app.UnsupportedProtection || len(app.Destinations) != 1 {
		t.Fatal("ordinary public destination rejected")
	}
}

func TestRemotePolicyEveryoneSelectorSurvivesSDK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"success":true,"result":{"id":"policy","decision":"allow","include":[{"everyone":{}}]}}`)
	}))
	defer server.Close()
	c, err := NewClient("test-token", WithHTTPClient(&http.Client{Transport: &rewriteConfigTransport{target: server.URL, delegate: server.Client().Transport}}))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := c.GetAccessPolicy(context.Background(), "account", "policy")
	if err != nil {
		t.Fatal(err)
	}
	if policy == nil || len(policy.Include) != 1 || policy.Include[0].Everyone == nil || !*policy.Include[0].Everyone {
		t.Fatalf("Everyone selector disappeared: %+v", policy)
	}
}

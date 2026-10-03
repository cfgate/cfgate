package cloudflare

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"k8s.io/utils/ptr"
)

func TestReadTunnelConfigurationPreservesH2c(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"success":true,"result":{"config":{"ingress":[{"service":"http_status:404","originRequest":{"h2cOrigin":true}}],"originRequest":{"h2cOrigin":true}}}}`)
	}))
	defer server.Close()
	transport := &rewriteConfigTransport{target: server.URL, delegate: server.Client().Transport}
	client, err := NewClient("test-token", WithHTTPClient(&http.Client{Transport: transport}))
	if err != nil {
		t.Fatal(err)
	}
	config, err := client.GetTunnelConfiguration(context.Background(), "account", "tunnel")
	if err != nil {
		t.Fatal(err)
	}
	if config.OriginRequest == nil || !ptr.Deref(config.OriginRequest.H2cOrigin, false) || len(config.Ingress) != 1 || !ptr.Deref(config.Ingress[0].OriginRequest.H2cOrigin, false) {
		t.Fatalf("h2c response lost: %+v", config)
	}
}

func TestReadTunnelConfigurationDistinguishesUnsetAndMalformed(t *testing.T) {
	for _, test := range []struct {
		name, body           string
		wantUnset, wantError bool
	}{
		{"new tunnel has explicit null config", `{"success":true,"result":{"config":null,"version":0,"source":"cloudflare"}}`, true, false},
		{"empty configuration is present", `{"success":true,"result":{"config":{}}}`, false, false},
		{"missing configuration", `{"success":true,"result":{"version":0}}`, false, true},
		{"wrong configuration type", `{"success":true,"result":{"config":"invalid"}}`, false, true},
		{"null result", `{"success":true,"result":null}`, false, true},
		{"malformed response", `{"success":true,"result":{"config":`, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			transport := &rewriteConfigTransport{target: server.URL, delegate: server.Client().Transport}
			client, err := NewClient("test-token", WithHTTPClient(&http.Client{Transport: transport}))
			if err != nil {
				t.Fatal(err)
			}
			config, err := client.GetTunnelConfiguration(context.Background(), "account", "tunnel")
			if (err != nil) != test.wantError {
				t.Fatalf("error=%v, want error=%t", err, test.wantError)
			}
			if !test.wantError && (config == nil) != test.wantUnset {
				t.Fatalf("unset=%t, want %t", config == nil, test.wantUnset)
			}
		})
	}
}

func TestReadTunnelDistinguishesDownAndDeleted(t *testing.T) {
	for _, test := range []struct {
		name, deletedAt string
		wantAbsent      bool
	}{
		{"down active tunnel", "null", false},
		{"deleted tombstone", `"2026-09-29T21:19:00Z"`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"success":true,"result":{"id":"tunnel","name":"example","status":"down","deleted_at":%s}}`, test.deletedAt)
			}))
			defer server.Close()
			client, err := NewClient("test-token", WithHTTPClient(&http.Client{Transport: &rewriteConfigTransport{target: server.URL, delegate: server.Client().Transport}}))
			if err != nil {
				t.Fatal(err)
			}
			tunnel, err := client.GetTunnel(context.Background(), "account", "tunnel")
			if err != nil {
				t.Fatal(err)
			}
			if (tunnel == nil) != test.wantAbsent {
				t.Fatalf("absent=%t, want %t", tunnel == nil, test.wantAbsent)
			}
		})
	}
}

func TestTunnelNameLookupSkipsDeletedTombstones(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprint(active), func(t *testing.T) {
			body := `{"success":true,"result":[{"id":"deleted","name":"example","status":"down","deleted_at":"2026-09-29T21:19:00Z"}`
			if active {
				body += `,{"id":"unrelated","name":"example-other","status":"down","deleted_at":null},{"id":"active","name":"example","status":"down","deleted_at":null}`
			}
			body += `]}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("is_deleted") != "false" {
					t.Error("name lookup must exclude tombstones server-side")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, body)
			}))
			defer server.Close()
			client, err := NewClient("test-token", WithHTTPClient(&http.Client{Transport: &rewriteConfigTransport{target: server.URL, delegate: server.Client().Transport}}))
			if err != nil {
				t.Fatal(err)
			}
			tunnel, err := client.GetTunnelByName(context.Background(), "account", "example")
			if err != nil {
				t.Fatal(err)
			}
			if !active && tunnel != nil {
				t.Fatal("deleted tunnel remained adoptable")
			}
			if active && (tunnel == nil || tunnel.ID != "active") {
				t.Fatalf("exact active tunnel not selected: %+v", tunnel)
			}
		})
	}
}

func TestReadTunnelPreservesAPIFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"success":false,"errors":[{"code":9109,"message":"forbidden"}]}`)
	}))
	defer server.Close()
	client, err := NewClient("test-token", WithHTTPClient(&http.Client{Transport: &rewriteConfigTransport{target: server.URL, delegate: server.Client().Transport}}))
	if err != nil {
		t.Fatal(err)
	}
	if tunnel, err := client.GetTunnel(context.Background(), "account", "tunnel"); err == nil || tunnel != nil {
		t.Fatal("failed tunnel read must not establish absence")
	}
	if tunnel, err := client.GetTunnelByName(context.Background(), "account", "example"); err == nil || tunnel != nil {
		t.Fatal("failed name lookup must not establish absence")
	}
}

type rewriteConfigTransport struct {
	target   string
	delegate http.RoundTripper
}

func (t *rewriteConfigTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, err := url.Parse(t.target)
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.URL.Scheme = target.Scheme
	clone.URL.Host = target.Host
	return t.delegate.RoundTrip(clone)
}

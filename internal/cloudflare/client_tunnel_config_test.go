package cloudflare

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
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
	if config.OriginRequest == nil || !config.OriginRequest.H2cOrigin || len(config.Ingress) != 1 || !config.Ingress[0].OriginRequest.H2cOrigin {
		t.Fatalf("h2c response lost: %+v", config)
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

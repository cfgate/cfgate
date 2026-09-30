package cloudflare

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadTunnelConfigurationDurationWireForms(t *testing.T) {
	for _, test := range []struct {
		name, wire, expected string
		invalid              bool
	}{
		{"seconds", "60", "1m0s", false},
		{"duration string", `"60s"`, "1m0s", false},
		{"equivalent string", `"1m"`, "1m0s", false},
		{"zero", "0", "0s", false},
		{"absent value string", `""`, "", false},
		{"null", "null", "", true},
		{"negative seconds", "-1", "", true},
		{"fractional seconds", "1.5", "", true},
		{"duration overflow", "9223372037", "", true},
		{"integer overflow", "9223372036854775808", "", true},
		{"negative string", `"-1s"`, "", true},
		{"overflow string", `"999999999999999999h"`, "", true},
		{"invalid string", `"invalid"`, "", true},
		{"boolean", "true", "", true},
		{"object", "{}", "", true},
		{"array", "[]", "", true},
	} {
		for _, field := range []string{"connectTimeout", "tlsTimeout", "tcpKeepAlive", "keepAliveTimeout"} {
			t.Run(test.name+"/"+field, func(t *testing.T) {
				origin := fmt.Sprintf(`{%q:%s,"h2cOrigin":true,"httpHostHeader":"origin.example","noTLSVerify":true}`, field, test.wire)
				body := fmt.Sprintf(`{"success":true,"result":{"config":{"originRequest":%s,"ingress":[{"hostname":"app.example","path":"^/api$","service":"http://origin:8080","originRequest":%s},{"service":"http_status:404"}]}}}`, origin, origin)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, body)
				}))
				defer server.Close()
				client, err := NewClient("test-token", WithHTTPClient(&http.Client{Transport: &rewriteConfigTransport{target: server.URL, delegate: server.Client().Transport}}))
				if err != nil {
					t.Fatal(err)
				}
				config, err := client.GetTunnelConfiguration(context.Background(), "account", "tunnel")
				if test.invalid {
					if err == nil {
						t.Fatal("invalid duration must prevent remote agreement")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(config.Ingress) != 2 || config.Ingress[0].Hostname != "app.example" || config.Ingress[0].Path != "^/api$" || config.Ingress[1].Service != "http_status:404" {
					t.Fatal("ingress match order changed")
				}
				for _, origin := range []*OriginRequestConfig{config.OriginRequest, config.Ingress[0].OriginRequest} {
					if origin == nil || !origin.H2cOrigin || origin.HTTPHostHeader != "origin.example" || !origin.NoTLSVerify {
						t.Fatal("origin fields lost")
					}
					values := map[string]string{"connectTimeout": origin.ConnectTimeout, "tlsTimeout": origin.TLSTimeout, "tcpKeepAlive": origin.TCPKeepAlive, "keepAliveTimeout": origin.KeepAliveTimeout}
					if values[field] != test.expected {
						t.Fatalf("duration=%q want %q", values[field], test.expected)
					}
				}
			})
		}
	}
}

func TestGetTunnelRejectsUnconfirmedIdentity(t *testing.T) {
	for _, body := range []string{
		`{"success":true,"result":null}`,
		`{"success":true,"result":{}}`,
		`{"success":true,"result":{"deleted_at":"2026-09-29T21:19:00Z"}}`,
		`{"success":true,"result":{"id":"foreign","deleted_at":"2026-09-29T21:19:00Z"}}`,
		`{"success":true,"result":{"id":"foreign","deleted_at":null}}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, body)
			}))
			defer server.Close()
			client, err := NewClient("test-token", WithHTTPClient(&http.Client{Transport: &rewriteConfigTransport{target: server.URL, delegate: server.Client().Transport}}))
			if err != nil {
				t.Fatal(err)
			}
			if tunnel, err := client.GetTunnel(context.Background(), "account", "tunnel"); err == nil || tunnel != nil {
				t.Fatal("unconfirmed identity must not authorize absence")
			}
		})
	}
}

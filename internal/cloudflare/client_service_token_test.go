package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-logr/logr"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServiceTokenWireContract(t *testing.T) {
	expiry := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["duration"] != "48h" || body["enabled"] != nil {
				t.Errorf("unexpected renewal body: %+v", body)
			}
		}
		if strings.HasSuffix(r.URL.Path, "/rotate") {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["previous_client_secret_expires_at"] != expiry.Format(time.RFC3339) {
				t.Errorf("overlap lost: %+v", body)
			}
			_, _ = fmt.Fprint(w, `{"success":true,"result":{"id":"token","name":"svc","client_id":"client","client_secret":"new","duration":"48h"}}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"success":true,"result":{"id":"token","name":"svc","client_id":"client","duration":"48h","enabled":false,"expires_at":%q}}`, expiry.Format(time.RFC3339))
	}))
	defer server.Close()
	c, err := NewClient("test", WithHTTPClient(&http.Client{Transport: &rewriteConfigTransport{target: server.URL, delegate: server.Client().Transport}}))
	if err != nil {
		t.Fatal(err)
	}
	token, err := c.GetServiceToken(context.Background(), "account", "token")
	if err != nil {
		t.Fatal(err)
	}
	if token.Enabled == nil || *token.Enabled || !token.ExpiresAt.Equal(expiry) {
		t.Fatalf("lost provider state: %+v", token)
	}
	token, err = c.UpdateServiceToken(context.Background(), "account", "token", ServiceTokenParams{Name: "svc", Duration: "48h"})
	if err != nil {
		t.Fatal(err)
	}
	if !token.ExpiresAt.Equal(expiry) {
		t.Fatal("update lost expiration")
	}
	rotated, err := c.RotateServiceToken(context.Background(), "account", "token", ServiceTokenRotateParams{PreviousClientSecretExpiresAt: expiry})
	if err != nil {
		t.Fatal(err)
	}
	if rotated.ClientID != "client" || rotated.ClientSecret != "new" {
		t.Fatal("lost credentials")
	}
	if len(methods) != 3 || methods[0] != "GET /client/v4/accounts/account/access/service_tokens/token" {
		t.Fatalf("unexpected calls: %v", methods)
	}
}

func TestCreatedTokenReadbackFollowsSecretStorage(t *testing.T) {
	writer := &intentSecretWriter{}
	expiry := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/service_tokens"):
			_, _ = fmt.Fprint(w, `{"success":true,"result":[],"result_info":{"page":1,"per_page":20,"total_count":0,"total_pages":1}}`)
		case r.Method == http.MethodPost:
			if !writer.pending {
				t.Error("create without intent")
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["duration"] != "48h" || body["name"] != "svc" {
				t.Errorf("bad creation request: %+v", body)
			}
			_, _ = fmt.Fprint(w, `{"success":true,"result":{"id":"token","name":"svc","client_id":"client","client_secret":"secret","duration":"48h"}}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/token"):
			if writer.pending || string(writer.data["CF_ACCESS_CLIENT_SECRET"]) != "secret" {
				t.Error("credential not stored before readback")
			}
			_, _ = fmt.Fprintf(w, `{"success":true,"result":{"id":"token","name":"svc","client_id":"client","duration":"48h","enabled":true,"expires_at":%q}}`, expiry.Format(time.RFC3339))
		default:
			t.Errorf("unexpected API call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	c, err := NewClient("test", WithHTTPClient(&http.Client{Transport: &rewriteConfigTransport{target: server.URL, delegate: server.Client().Transport}}))
	if err != nil {
		t.Fatal(err)
	}
	token, err := NewAccessService(c, logr.Discard()).EnsureServiceToken(context.Background(), "account", ServiceTokenParams{Name: "svc", Duration: "48h"}, writer)
	if err != nil {
		t.Fatal(err)
	}
	if !token.ExpiresAt.Equal(expiry) || token.Enabled == nil || !*token.Enabled || writer.calls != 1 {
		t.Fatalf("incorrect created token: %+v writes=%d", token, writer.calls)
	}
}

func TestExpirationOnlyRenewalWireContract(t *testing.T) {
	for _, scenario := range []string{"extend", "disabled", "expired", "changed client", "changed name", "changed duration", "would shorten", "unconfirmed expiration"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			token := ServiceToken{ID: "token", Name: "svc", ClientID: "client", Duration: "1h", ExpiresAt: now.Add(time.Minute)}
			puts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				current := token
				enabled := true
				switch r.Method {
				case http.MethodGet:
					switch scenario {
					case "disabled":
						enabled = false
					case "expired":
						current.ExpiresAt = now.Add(-time.Minute)
					case "changed client":
						current.ClientID = "other"
					case "changed name":
						current.Name = "other"
					case "changed duration":
						current.Duration = "2h"
					case "would shorten":
						current.ExpiresAt = now.Add(2 * time.Hour)
					}
				case http.MethodPut:
					puts++
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if len(body) != 2 || body["duration"] != "1h" || body["name"] != current.Name {
						t.Errorf("renewal widened mutation: %+v", body)
					}
					if scenario != "unconfirmed expiration" && body["name"] == current.Name {
						current.ExpiresAt = now.Add(time.Hour)
					}
				default:
					t.Errorf("unexpected method %s", r.Method)
				}
				_, _ = fmt.Fprintf(w, `{"success":true,"result":{"id":%q,"name":%q,"client_id":%q,"duration":%q,"enabled":%t,"expires_at":%q}}`, current.ID, current.Name, current.ClientID, current.Duration, enabled, current.ExpiresAt.Format(time.RFC3339))
			}))
			defer server.Close()
			c, err := NewClient("test", WithHTTPClient(&http.Client{Transport: &rewriteConfigTransport{target: server.URL, delegate: server.Client().Transport}}))
			if err != nil {
				t.Fatal(err)
			}
			renewed, err := c.ExtendServiceTokenExpiration(context.Background(), "account", "token", token)
			if scenario == "extend" {
				if err != nil || !renewed.ExpiresAt.After(token.ExpiresAt) {
					t.Fatalf("renewal failed: %+v %v", renewed, err)
				}
			} else if err == nil {
				t.Fatal("unsafe renewal accepted")
			}
			expected := 0
			if scenario == "extend" || scenario == "unconfirmed expiration" {
				expected = 1
			}
			if puts != expected {
				t.Fatalf("mutations=%d want %d", puts, expected)
			}
		})
	}
}

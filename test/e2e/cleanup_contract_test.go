package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cloudflare "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/option"
)

type cleanupTransport func(*http.Request) (*http.Response, error)

func (f cleanupTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCleanupCloudflareTargets(t *testing.T) {
	priorEnv, priorRun := testEnv, testRunID
	t.Cleanup(func() { testEnv, testRunID = priorEnv, priorRun })
	testRunID = "current"
	testEnv = &E2ETestEnv{CloudflareAccountID: "account", CloudflareZoneName: "example.com"}
	t.Setenv(EnvCleanOrphans, "")
	owner := "cfgate:0123456789abcdef0123456789ab"
	shared := "cfgate:abcdef0123456789abcdef012345"
	foreign := "cfgate:9999999999999999999999999999"
	for _, scenario := range []string{"normal", "application list failed", "application relist failed", "application delete failed", "skip"} {
		t.Run(scenario, func(t *testing.T) {
			testEnv.SkipCleanup = scenario == "skip"
			defer func() { testEnv.SkipCleanup = false }()
			var deleted []string
			appLists := 0
			transport := cleanupTransport(func(r *http.Request) (*http.Response, error) {
				path := r.URL.Path
				result := "[]"
				status := 200
				if r.Method == http.MethodDelete {
					deleted = append(deleted, path)
					result = `{}`
					if scenario == "application delete failed" && strings.HasSuffix(path, "/apps/app-current") {
						status = 500
					}
				} else if page := r.URL.Query().Get("page"); page != "" && page != "1" {
					result = "[]"
				} else {
					switch {
					case strings.HasSuffix(path, "/cfd_tunnel"):
						result = `[{"id":"current","name":"e2e-current-tunnel-1-2","created_at":"2026-01-01T00:00:00Z"},{"id":"other","name":"e2e-other-tunnel-1-2","created_at":"2026-01-01T00:00:00Z"}]`
					case strings.HasSuffix(path, "/apps"):
						appLists++
						if (scenario == "application list failed" && appLists == 1) || (scenario == "application relist failed" && appLists == 2) {
							status = 500
							break
						}
						if appLists == 1 || scenario == "application delete failed" {
							result = fmt.Sprintf(`[{"id":"app-current","name":"admin-app","domain":"e2e-current-access-1-2.example.com/admin","tags":["%s","%s"]},{"id":"app-other","name":"other","domain":"e2e-other-access-1-2.example.com","tags":["%s"]}]`, owner, shared, shared)
						} else {
							result = fmt.Sprintf(`[{"id":"app-other","name":"other","tags":["%s"]}]`, shared)
						}
					case strings.HasSuffix(path, "/tags"):
						result = fmt.Sprintf(`[{"name":"%s"},{"name":"%s"},{"name":"%s"}]`, owner, shared, foreign)
					default:
						t.Fatalf("unexpected request %s %s", r.Method, path)
					}
				}
				body := fmt.Sprintf(`{"success":%t,"result":%s,"result_info":{"page":1,"total_pages":1},"errors":[]}`, status == 200, result)
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			cf := cloudflare.NewClient(option.WithHTTPClient(&http.Client{Transport: transport}), option.WithAPIToken("test-token"), option.WithMaxRetries(0))
			options := e2eCleanupOptions{includeCurrentRun: true, minAge: 2 * time.Hour}
			cleanOrphanedTunnels(context.Background(), cf, options)
			tags := cleanOrphanedAccessApplications(context.Background(), cf, options)
			cleanOrphanedAccessTags(context.Background(), cf, tags)
			prefix := "/client/v4/accounts/account/"
			want := []string{prefix + "cfd_tunnel/current/connections", prefix + "cfd_tunnel/current"}
			if scenario == "normal" || scenario == "application relist failed" || scenario == "application delete failed" {
				want = append(want, prefix+"access/apps/app-current")
			}
			if scenario == "normal" {
				want = append(want, prefix+"access/tags/"+owner)
			}
			if scenario == "skip" {
				want = nil
			}
			if !reflect.DeepEqual(deleted, want) {
				t.Fatalf("DELETE targets=%v want=%v", deleted, want)
			}
		})
	}
}

func TestCleanupStartupDefaultDoesNothing(t *testing.T) {
	previous := testEnv
	t.Cleanup(func() { testEnv = previous })
	testEnv = &E2ETestEnv{}
	t.Setenv(EnvCleanOrphans, "")
	// The default returns before By, client construction, or any network operation.
	cleanOrphanedE2EResources(false)
}

// Verification helpers must not turn an expired poll into a successful absence
// check, or lose its deadline when the SDK fetches another page.
func TestE2EVerificationDeadlines(t *testing.T) {
	previous := testEnv
	t.Cleanup(func() { testEnv = previous })
	testEnv = &E2ETestEnv{CloudflareAPIToken: "test-token"}
	operations := map[string]func(context.Context, *cloudflare.Client) error{
		"DNS": func(ctx context.Context, cf *cloudflare.Client) error {
			_, err := getDNSRecordFromCloudflare(ctx, cf, "zone", "host.example.com", "CNAME")
			return err
		},
		"tunnels": func(ctx context.Context, cf *cloudflare.Client) error {
			_, err := listTunnelsByPrefixFromCloudflare(ctx, cf, "account", "e2e-")
			return err
		},
		"applications": func(ctx context.Context, cf *cloudflare.Client) error {
			_, err := getAccessApplicationFromCloudflare(ctx, cf, "account", "wanted")
			return err
		},
		"tokens": func(ctx context.Context, cf *cloudflare.Client) error {
			_, err := getServiceTokenFromCloudflare(ctx, cf, "account", "wanted")
			return err
		},
	}
	for name, operation := range operations {
		for _, body := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/body=%t", name, body), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if calls.Add(1) == 1 && name != "DNS" {
						_, _ = io.WriteString(w, `{"success":true,"result":[{"id":"one","name":"other"}],"result_info":{"page":1,"total_pages":2}}`)
						return
					}
					if body {
						_, _ = io.WriteString(w, `{"success":true,"result":[`)
						w.(http.Flusher).Flush()
					}
					select {
					case <-r.Context().Done():
					case <-time.After(2 * time.Second):
						// Bound the fixture even if a regression loses cancellation.
						_, _ = io.WriteString(w, `{"success":true,"result":[],"result_info":{"page":2,"total_pages":2}}`)
					}
				}))
				defer server.Close()
				defer server.CloseClientConnections()
				cf := getCloudflareClient(option.WithBaseURL(server.URL))
				ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
				defer cancel()
				started := time.Now()
				err := operation(ctx, cf)
				// The transport's cancellation hook may win the race with the
				// inherited deadline; both errors mean the request stopped.
				if ctx.Err() != context.DeadlineExceeded || (!errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled)) {
					t.Fatalf("expected expired caller and canceled request, ctx=%v err=%v", ctx.Err(), err)
				}
				if time.Since(started) > time.Second {
					t.Fatal("verification escaped its polling deadline")
				}
				wantCalls := int32(2)
				if name == "DNS" {
					wantCalls = 1
				}
				if calls.Load() != wantCalls {
					t.Fatalf("calls=%d want=%d", calls.Load(), wantCalls)
				}
			})
		}
	}
}

func TestE2EServiceTokenIdentity(t *testing.T) {
	previous := testEnv
	t.Cleanup(func() { testEnv = previous })
	testEnv = &E2ETestEnv{CloudflareAPIToken: "test-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"success":true,"result":[{"id":"foreign","name":"wanted"},{"id":"wanted","name":"qualified [cfgate:owner]"}],"result_info":{"page":1,"total_pages":1}}`)
	}))
	defer server.Close()
	cf := getCloudflareClient(option.WithBaseURL(server.URL))
	token, err := getServiceTokenFromCloudflare(t.Context(), cf, "account", "wanted")
	if err != nil || token == nil || token.ID != "wanted" {
		t.Fatalf("recorded token identity not recovered: token=%v err=%v", token, err)
	}
}

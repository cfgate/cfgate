package e2e_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	cloudflare "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/option"
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

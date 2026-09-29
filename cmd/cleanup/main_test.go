package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	cloudflare "github.com/cloudflare/cloudflare-go/v6"
)

type fakeCleanupClient struct {
	deleted  []string
	tunnels  []resource
	records  []resource
	apps     []resource
	policies []resource
	tags     []resource
	tokens   []resource
	zoneID   string

	tunnelsErr  error
	recordsErr  error
	appsErr     error
	policiesErr error
	tagsErr     error
	tokensErr   error
	zoneErr     error

	deleteTunnelErr map[string]error
	deleteRecordErr map[string]error
	deleteAppErr    map[string]error
	deletePolicyErr map[string]error
	deleteTagErr    map[string]error
	deleteTokenErr  map[string]error
}

func TestExecuteCleanup(t *testing.T) {
	t.Run("config failure returns usage-style exit code", func(t *testing.T) {
		buf := &bytes.Buffer{}
		code := executeCleanup(func(string) string { return "" }, buf, cleanupRuntime{
			newClient: func(context.Context, string) cleanupClient {
				t.Fatal("newClient should not be called on config error")
				return nil
			},
		})
		if code != 1 {
			t.Fatalf("executeCleanup() = %d, want 1", code)
		}
		if !strings.Contains(buf.String(), "Usage: mise run e2e:cleanup") {
			t.Fatalf("output = %q, want usage hint", buf.String())
		}
	})

	t.Run("cleanup failure returns non-zero", func(t *testing.T) {
		buf := &bytes.Buffer{}
		code := executeCleanup(func(key string) string {
			switch key {
			case "CLOUDFLARE_API_TOKEN":
				return "token"
			case "E2E_CLEAN_ORPHANS", "E2E_CLEANUP_APPLY":
				return "true"
			case "CLOUDFLARE_ACCOUNT_ID":
				return "account"
			default:
				return ""
			}
		}, buf, cleanupRuntime{
			newClient: func(context.Context, string) cleanupClient {
				return &fakeCleanupClient{
					tunnels: []resource{{ID: "t1", Name: "e2e-old-tunnel-1-2", Type: "tunnel", Created: time.Now().Add(-3 * time.Hour)}},
					deleteTunnelErr: map[string]error{
						"t1": errors.New("boom"),
					},
				}
			},
		})
		if code != 1 {
			t.Fatalf("executeCleanup() = %d, want 1", code)
		}
	})

	t.Run("success returns zero", func(t *testing.T) {
		buf := &bytes.Buffer{}
		code := executeCleanup(func(key string) string {
			switch key {
			case "CLOUDFLARE_API_TOKEN":
				return "token"
			case "CLOUDFLARE_ACCOUNT_ID":
				return "account"
			default:
				return ""
			}
		}, buf, cleanupRuntime{
			newClient: func(context.Context, string) cleanupClient {
				return &fakeCleanupClient{}
			},
		})
		if code != 0 {
			t.Fatalf("executeCleanup() = %d, want 0", code)
		}
	})
}

func (f *fakeCleanupClient) ListOrphanedTunnels(context.Context, string) ([]resource, error) {
	return f.tunnels, f.tunnelsErr
}

func (f *fakeCleanupClient) ListOrphanedDNSRecords(context.Context, string) ([]resource, error) {
	return f.records, f.recordsErr
}

func (f *fakeCleanupClient) ListOrphanedAccessApplications(context.Context, string) ([]resource, error) {
	return f.apps, f.appsErr
}

func (f *fakeCleanupClient) ListOrphanedAccessPolicies(context.Context, string) ([]resource, error) {
	return f.policies, f.policiesErr
}

func (f *fakeCleanupClient) ListOrphanedAccessTags(context.Context, string) ([]resource, error) {
	return f.tags, f.tagsErr
}

func (f *fakeCleanupClient) ListOrphanedServiceTokens(context.Context, string) ([]resource, error) {
	return f.tokens, f.tokensErr
}

func (f *fakeCleanupClient) ResolveZoneID(context.Context, string) (string, error) {
	if f.zoneErr != nil {
		return "", f.zoneErr
	}
	return f.zoneID, nil
}

func (f *fakeCleanupClient) DeleteTunnel(_ context.Context, _, tunnelID string) error {
	f.deleted = append(f.deleted, tunnelID)
	if err := f.deleteTunnelErr[tunnelID]; err != nil {
		return err
	}
	return nil
}

func (f *fakeCleanupClient) DeleteDNSRecord(_ context.Context, _, recordID string) error {
	f.deleted = append(f.deleted, recordID)
	if err := f.deleteRecordErr[recordID]; err != nil {
		return err
	}
	return nil
}

func (f *fakeCleanupClient) DeleteAccessApplication(_ context.Context, _, appID string) error {
	f.deleted = append(f.deleted, appID)
	if err := f.deleteAppErr[appID]; err != nil {
		return err
	}
	return nil
}

func (f *fakeCleanupClient) DeleteAccessPolicy(_ context.Context, _, policyID string) error {
	f.deleted = append(f.deleted, policyID)
	if err := f.deletePolicyErr[policyID]; err != nil {
		return err
	}
	return nil
}

func (f *fakeCleanupClient) DeleteAccessTag(_ context.Context, _, tagName string) error {
	f.deleted = append(f.deleted, tagName)
	if err := f.deleteTagErr[tagName]; err != nil {
		return err
	}
	return nil
}

func (f *fakeCleanupClient) DeleteServiceToken(_ context.Context, _, tokenID string) error {
	f.deleted = append(f.deleted, tokenID)
	if err := f.deleteTokenErr[tokenID]; err != nil {
		return err
	}
	return nil
}

func TestLoadCleanupConfig(t *testing.T) {
	t.Run("requires token and account ID", func(t *testing.T) {
		_, err := loadCleanupConfig(func(string) string { return "" })
		if err == nil || !strings.Contains(err.Error(), "CLOUDFLARE_API_TOKEN") {
			t.Fatalf("loadCleanupConfig() error = %v, want missing credential error", err)
		}
	})

	t.Run("loads optional zone", func(t *testing.T) {
		cfg, err := loadCleanupConfig(func(key string) string {
			switch key {
			case "CLOUDFLARE_API_TOKEN":
				return "token"
			case "CLOUDFLARE_ACCOUNT_ID":
				return "account"
			case "CLOUDFLARE_ZONE_NAME":
				return "example.com"
			default:
				return ""
			}
		})
		if err != nil {
			t.Fatalf("loadCleanupConfig() error = %v", err)
		}
		if cfg.ZoneName != "example.com" {
			t.Fatalf("ZoneName = %q, want %q", cfg.ZoneName, "example.com")
		}
	})
}

func TestRunCleanup(t *testing.T) {
	old := time.Now().Add(-3 * time.Hour)
	owner := "cfgate:0123456789abcdef0123456789ab"
	foreign := "cfgate:abcdef0123456789abcdef012345"
	cfg := cleanupConfig{APIToken: "token", AccountID: "account", ZoneName: "example.com", MinAge: 2 * time.Hour, Apply: true}
	fixture := func() *fakeCleanupClient {
		return &fakeCleanupClient{
			tunnels:  []resource{{ID: "old", Name: "e2e-old-tunnel-1-2", Created: old}, {ID: "fresh", Name: "e2e-new-tunnel-1-2", Created: time.Now()}, {ID: "unknown", Name: "e2e-old-tunnel-1-3"}, {ID: "foreign", Name: "production-e2e-old-tunnel-1-2", Created: old}},
			records:  []resource{{ID: "dns", Name: "_custom.e2e-old-dns-1-4.example.com", Created: old}},
			apps:     []resource{{ID: "app", Name: "admin-app", Domain: "e2e-old-access-1-5.example.com/admin", Created: old, Tags: []string{owner}}},
			policies: []resource{{ID: "policy", Name: "e2e-old-policy-1-6", Created: old}},
			tokens:   []resource{{ID: "token", Name: "e2e-old-access-1-7-token", Created: old}},
			tags:     []resource{{ID: owner, Name: owner}, {ID: foreign, Name: foreign}}, zoneID: "zone",
		}
	}
	t.Run("preview has no side effects", func(t *testing.T) {
		c := fixture()
		preview := cfg
		preview.Apply = false
		summary, err := runCleanup(context.Background(), preview, c, &bytes.Buffer{})
		if err != nil || summary.Found != 5 || summary.Deleted != 0 || len(c.deleted) != 0 {
			t.Fatalf("summary=%+v deletes=%v err=%v", summary, c.deleted, err)
		}
	})
	t.Run("apply targets only old complete markers and attributed tags", func(t *testing.T) {
		c := fixture()
		summary, err := runCleanup(context.Background(), cfg, c, &bytes.Buffer{})
		want := []string{"old", "dns", "app", "policy", "token", owner}
		if err != nil || summary.Deleted != 6 || !reflect.DeepEqual(c.deleted, want) {
			t.Fatalf("summary=%+v deletes=%v err=%v", summary, c.deleted, err)
		}
	})
	for _, which := range []string{"tunnels", "records", "apps", "policies", "tokens", "zone"} {
		t.Run("incomplete inventory "+which, func(t *testing.T) {
			c := fixture()
			boom := errors.New("inventory failed")
			switch which {
			case "tunnels":
				c.tunnelsErr = boom
			case "records":
				c.recordsErr = boom
			case "apps":
				c.appsErr = boom
			case "policies":
				c.policiesErr = boom
			case "tokens":
				c.tokensErr = boom
			case "zone":
				c.zoneErr = boom
			}
			_, err := runCleanup(context.Background(), cfg, c, &bytes.Buffer{})
			if err == nil || len(c.deleted) != 0 {
				t.Fatalf("deletes=%v err=%v", c.deleted, err)
			}
		})
	}
	t.Run("failed tag relist preserves every tag", func(t *testing.T) {
		c := fixture()
		c.tagsErr = errors.New("relist failed")
		_, err := runCleanup(context.Background(), cfg, c, &bytes.Buffer{})
		if err == nil || len(c.deleted) != 5 {
			t.Fatalf("deletes=%v err=%v", c.deleted, err)
		}
	})
	t.Run("unattributed tags are preserved", func(t *testing.T) {
		c := &fakeCleanupClient{tags: []resource{{ID: owner, Name: owner}}}
		summary, err := runCleanup(context.Background(), cfg, c, &bytes.Buffer{})
		if err != nil || summary.Deleted != 0 || len(c.deleted) != 0 {
			t.Fatalf("summary=%+v err=%v", summary, err)
		}
	})
	for _, which := range []string{"tunnel", "dns", "app", "policy", "token", "tag"} {
		t.Run("delete failure "+which, func(t *testing.T) {
			c := fixture()
			boom := errors.New("delete failed")
			switch which {
			case "tunnel":
				c.deleteTunnelErr = map[string]error{"old": boom}
			case "dns":
				c.deleteRecordErr = map[string]error{"dns": boom}
			case "app":
				c.deleteAppErr = map[string]error{"app": boom}
			case "policy":
				c.deletePolicyErr = map[string]error{"policy": boom}
			case "token":
				c.deleteTokenErr = map[string]error{"token": boom}
			case "tag":
				c.deleteTagErr = map[string]error{owner: boom}
			}
			summary, err := runCleanup(context.Background(), cfg, c, &bytes.Buffer{})
			if err == nil || summary.Failed != 1 || len(summary.FailedResources) != 1 {
				t.Fatalf("summary=%+v err=%v", summary, err)
			}
		})
	}
}

func TestCleanupApplyOptIns(t *testing.T) {
	for _, tc := range []struct {
		orphans, apply, skip string
		want                 bool
	}{{"", "", "", false}, {"true", "", "", false}, {"", "true", "", false}, {"true", "true", "", true}, {"true", "true", "true", false}} {
		values := map[string]string{"CLOUDFLARE_API_TOKEN": "token", "CLOUDFLARE_ACCOUNT_ID": "account", "E2E_CLEAN_ORPHANS": tc.orphans, "E2E_CLEANUP_APPLY": tc.apply, "E2E_SKIP_CLEANUP": tc.skip}
		cfg, err := loadCleanupConfig(func(k string) string { return values[k] })
		if err != nil || cfg.Apply != tc.want {
			t.Fatalf("case=%+v cfg=%+v err=%v", tc, cfg, err)
		}
	}
}

func TestPrintScanSection(t *testing.T) {
	buf := &bytes.Buffer{}
	printScanSection(buf, "DNS Records", nil, errors.New("scan failed"))
	output := buf.String()
	if !strings.Contains(output, "Warning: scan failed") {
		t.Fatalf("output = %q, want warning", output)
	}
	if !strings.Contains(output, "No orphaned dns records found") {
		t.Fatalf("output = %q, want empty state", output)
	}
}

func TestCloudflareCleanupClientDelegates(t *testing.T) {
	origListTunnels := listOrphanedTunnelsFn
	origListRecords := listOrphanedDNSRecordsFn
	origListApps := listOrphanedAccessApplicationsFn
	origListPolicies := listOrphanedAccessPoliciesFn
	origListTags := listOrphanedAccessTagsFn
	origListTokens := listOrphanedServiceTokensFn
	origGetZoneID := getZoneIDFn
	origDeleteTunnel := deleteTunnelFn
	origDeleteDNS := deleteDNSRecordFn
	origDeleteApp := deleteAccessApplicationFn
	origDeletePolicy := deleteAccessPolicyFn
	origDeleteTag := deleteAccessTagFn
	origDeleteToken := deleteServiceTokenFn
	t.Cleanup(func() {
		listOrphanedTunnelsFn = origListTunnels
		listOrphanedDNSRecordsFn = origListRecords
		listOrphanedAccessApplicationsFn = origListApps
		listOrphanedAccessPoliciesFn = origListPolicies
		listOrphanedAccessTagsFn = origListTags
		listOrphanedServiceTokensFn = origListTokens
		getZoneIDFn = origGetZoneID
		deleteTunnelFn = origDeleteTunnel
		deleteDNSRecordFn = origDeleteDNS
		deleteAccessApplicationFn = origDeleteApp
		deleteAccessPolicyFn = origDeletePolicy
		deleteAccessTagFn = origDeleteTag
		deleteServiceTokenFn = origDeleteToken
	})

	client := &cloudflareCleanupClient{}
	ctx := context.Background()

	listOrphanedTunnelsFn = func(gotCtx context.Context, _ *cloudflare.Client, accountID string) ([]resource, error) {
		if gotCtx != ctx || accountID != "account" {
			t.Fatalf("ListOrphanedTunnels forwarded (%v, %q), want (%v, %q)", gotCtx, accountID, ctx, "account")
		}
		return []resource{{ID: "t1"}}, nil
	}
	gotTunnels, err := client.ListOrphanedTunnels(ctx, "account")
	if err != nil || len(gotTunnels) != 1 {
		t.Fatalf("ListOrphanedTunnels() = (%v, %v), want one tunnel and nil error", gotTunnels, err)
	}

	listOrphanedDNSRecordsFn = func(gotCtx context.Context, _ *cloudflare.Client, zoneName string) ([]resource, error) {
		if gotCtx != ctx || zoneName != "example.com" {
			t.Fatalf("ListOrphanedDNSRecords forwarded (%v, %q), want (%v, %q)", gotCtx, zoneName, ctx, "example.com")
		}
		return []resource{{ID: "r1"}}, nil
	}
	gotRecords, err := client.ListOrphanedDNSRecords(ctx, "example.com")
	if err != nil || len(gotRecords) != 1 {
		t.Fatalf("ListOrphanedDNSRecords() = (%v, %v), want one record and nil error", gotRecords, err)
	}

	listOrphanedAccessApplicationsFn = func(gotCtx context.Context, _ *cloudflare.Client, accountID string) ([]resource, error) {
		if gotCtx != ctx || accountID != "account" {
			t.Fatalf("ListOrphanedAccessApplications forwarded (%v, %q), want (%v, %q)", gotCtx, accountID, ctx, "account")
		}
		return []resource{{ID: "a1"}}, nil
	}
	gotApps, err := client.ListOrphanedAccessApplications(ctx, "account")
	if err != nil || len(gotApps) != 1 {
		t.Fatalf("ListOrphanedAccessApplications() = (%v, %v), want one app and nil error", gotApps, err)
	}

	listOrphanedAccessPoliciesFn = func(gotCtx context.Context, _ *cloudflare.Client, accountID string) ([]resource, error) {
		if gotCtx != ctx || accountID != "account" {
			t.Fatalf("ListOrphanedAccessPolicies forwarded (%v, %q), want (%v, %q)", gotCtx, accountID, ctx, "account")
		}
		return []resource{{ID: "p1"}}, nil
	}
	gotPolicies, err := client.ListOrphanedAccessPolicies(ctx, "account")
	if err != nil || len(gotPolicies) != 1 {
		t.Fatalf("ListOrphanedAccessPolicies() = (%v, %v), want one policy and nil error", gotPolicies, err)
	}

	listOrphanedAccessTagsFn = func(gotCtx context.Context, _ *cloudflare.Client, accountID string) ([]resource, error) {
		if gotCtx != ctx || accountID != "account" {
			t.Fatalf("ListOrphanedAccessTags forwarded (%v, %q), want (%v, %q)", gotCtx, accountID, ctx, "account")
		}
		return []resource{{ID: "cfgate:0123456789abcdef0123456789ab"}}, nil
	}
	gotTags, err := client.ListOrphanedAccessTags(ctx, "account")
	if err != nil || len(gotTags) != 1 {
		t.Fatalf("ListOrphanedAccessTags() = (%v, %v), want one tag and nil error", gotTags, err)
	}

	listOrphanedServiceTokensFn = func(gotCtx context.Context, _ *cloudflare.Client, accountID string) ([]resource, error) {
		if gotCtx != ctx || accountID != "account" {
			t.Fatalf("ListOrphanedServiceTokens forwarded (%v, %q), want (%v, %q)", gotCtx, accountID, ctx, "account")
		}
		return []resource{{ID: "s1"}}, nil
	}
	gotTokens, err := client.ListOrphanedServiceTokens(ctx, "account")
	if err != nil || len(gotTokens) != 1 {
		t.Fatalf("ListOrphanedServiceTokens() = (%v, %v), want one token and nil error", gotTokens, err)
	}

	getZoneIDFn = func(gotCtx context.Context, _ *cloudflare.Client, zoneName string) (string, error) {
		if gotCtx != ctx || zoneName != "example.com" {
			t.Fatalf("ResolveZoneID forwarded (%v, %q), want (%v, %q)", gotCtx, zoneName, ctx, "example.com")
		}
		return "zone-1", nil
	}
	zoneID, err := client.ResolveZoneID(ctx, "example.com")
	if err != nil || zoneID != "zone-1" {
		t.Fatalf("ResolveZoneID() = (%q, %v), want (%q, nil)", zoneID, err, "zone-1")
	}

	deleteTunnelFn = func(gotCtx context.Context, _ *cloudflare.Client, accountID, tunnelID string) error {
		if gotCtx != ctx || accountID != "account" || tunnelID != "t1" {
			t.Fatalf("DeleteTunnel forwarded (%v, %q, %q), want (%v, %q, %q)", gotCtx, accountID, tunnelID, ctx, "account", "t1")
		}
		return nil
	}
	if err := client.DeleteTunnel(ctx, "account", "t1"); err != nil {
		t.Fatalf("DeleteTunnel() error = %v", err)
	}

	deleteDNSRecordFn = func(gotCtx context.Context, _ *cloudflare.Client, zoneID, recordID string) error {
		if gotCtx != ctx || zoneID != "zone-1" || recordID != "r1" {
			t.Fatalf("DeleteDNSRecord forwarded (%v, %q, %q), want (%v, %q, %q)", gotCtx, zoneID, recordID, ctx, "zone-1", "r1")
		}
		return nil
	}
	if err := client.DeleteDNSRecord(ctx, "zone-1", "r1"); err != nil {
		t.Fatalf("DeleteDNSRecord() error = %v", err)
	}

	deleteAccessApplicationFn = func(gotCtx context.Context, _ *cloudflare.Client, accountID, appID string) error {
		if gotCtx != ctx || accountID != "account" || appID != "a1" {
			t.Fatalf("DeleteAccessApplication forwarded (%v, %q, %q), want (%v, %q, %q)", gotCtx, accountID, appID, ctx, "account", "a1")
		}
		return nil
	}
	if err := client.DeleteAccessApplication(ctx, "account", "a1"); err != nil {
		t.Fatalf("DeleteAccessApplication() error = %v", err)
	}

	deleteAccessPolicyFn = func(gotCtx context.Context, _ *cloudflare.Client, accountID, policyID string) error {
		if gotCtx != ctx || accountID != "account" || policyID != "p1" {
			t.Fatalf("DeleteAccessPolicy forwarded (%v, %q, %q), want (%v, %q, %q)", gotCtx, accountID, policyID, ctx, "account", "p1")
		}
		return nil
	}
	if err := client.DeleteAccessPolicy(ctx, "account", "p1"); err != nil {
		t.Fatalf("DeleteAccessPolicy() error = %v", err)
	}

	deleteAccessTagFn = func(gotCtx context.Context, _ *cloudflare.Client, accountID, tagName string) error {
		if gotCtx != ctx || accountID != "account" || tagName != "cfgate:0123456789abcdef0123456789ab" {
			t.Fatalf("DeleteAccessTag forwarded (%v, %q, %q), want (%v, %q, %q)", gotCtx, accountID, tagName, ctx, "account", "cfgate:0123456789abcdef0123456789ab")
		}
		return nil
	}
	if err := client.DeleteAccessTag(ctx, "account", "cfgate:0123456789abcdef0123456789ab"); err != nil {
		t.Fatalf("DeleteAccessTag() error = %v", err)
	}

	deleteServiceTokenFn = func(gotCtx context.Context, _ *cloudflare.Client, accountID, tokenID string) error {
		if gotCtx != ctx || accountID != "account" || tokenID != "s1" {
			t.Fatalf("DeleteServiceToken forwarded (%v, %q, %q), want (%v, %q, %q)", gotCtx, accountID, tokenID, ctx, "account", "s1")
		}
		return nil
	}
	if err := client.DeleteServiceToken(ctx, "account", "s1"); err != nil {
		t.Fatalf("DeleteServiceToken() error = %v", err)
	}
}

func TestIsE2EAccessApplication(t *testing.T) {
	tests := []struct {
		name    string
		appName string
		domain  string
		want    bool
	}{
		{name: "e2e name", appName: "e2e-app", domain: "app.example.com", want: true},
		{name: "e2e domain", appName: "admin-app", domain: "e2e-admin.example.com", want: true},
		{name: "e2e later in domain", appName: "admin-app", domain: "admin.e2e-example.com", want: false},
		{name: "non e2e", appName: "admin-app", domain: "admin.example.com", want: false},
		{name: "e2e outside prefix in name", appName: "admin-e2e-app", domain: "admin.example.com", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isE2EAccessApplication(tt.appName, tt.domain); got != tt.want {
				t.Errorf("isE2EAccessApplication(%q, %q) = %t, want %t", tt.appName, tt.domain, got, tt.want)
			}
		})
	}
}

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	cloudflare "github.com/cloudflare/cloudflare-go/v6"
)

type fakeCleanupClient struct {
	tunnels []resource
	records []resource
	apps    []resource
	tokens  []resource
	zoneID  string

	tunnelsErr error
	recordsErr error
	appsErr    error
	tokensErr  error
	zoneErr    error

	deleteTunnelErr map[string]error
	deleteRecordErr map[string]error
	deleteAppErr    map[string]error
	deleteTokenErr  map[string]error
}

func TestExecuteCleanup(t *testing.T) {
	t.Run("config failure returns usage-style exit code", func(t *testing.T) {
		buf := &bytes.Buffer{}
		code := executeCleanup(func(string) string { return "" }, buf, cleanupRuntime{
			newClient: func(string) cleanupClient {
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
			case "CLOUDFLARE_ACCOUNT_ID":
				return "account"
			default:
				return ""
			}
		}, buf, cleanupRuntime{
			newClient: func(string) cleanupClient {
				return &fakeCleanupClient{
					tunnels: []resource{{ID: "t1", Name: "e2e-tunnel", Type: "tunnel"}},
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
			newClient: func(string) cleanupClient {
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
	if err := f.deleteTunnelErr[tunnelID]; err != nil {
		return err
	}
	return nil
}

func (f *fakeCleanupClient) DeleteDNSRecord(_ context.Context, _, recordID string) error {
	if err := f.deleteRecordErr[recordID]; err != nil {
		return err
	}
	return nil
}

func (f *fakeCleanupClient) DeleteAccessApplication(_ context.Context, _, appID string) error {
	if err := f.deleteAppErr[appID]; err != nil {
		return err
	}
	return nil
}

func (f *fakeCleanupClient) DeleteServiceToken(_ context.Context, _, tokenID string) error {
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
	cfg := cleanupConfig{
		APIToken:  "token",
		AccountID: "account",
		ZoneName:  "example.com",
	}

	t.Run("reports no resources", func(t *testing.T) {
		buf := &bytes.Buffer{}
		summary, err := runCleanup(context.Background(), cfg, &fakeCleanupClient{}, buf)
		if err != nil {
			t.Fatalf("runCleanup() error = %v", err)
		}
		if summary.Found != 0 || summary.Deleted != 0 || summary.Failed != 0 {
			t.Fatalf("summary = %+v, want all zero", summary)
		}
		if !strings.Contains(buf.String(), "No orphaned E2E resources found") {
			t.Fatalf("output = %q, want no resources message", buf.String())
		}
	})

	t.Run("deletes resources and reports failures", func(t *testing.T) {
		client := &fakeCleanupClient{
			tunnels: []resource{{ID: "t1", Name: "e2e-tunnel", Type: "tunnel"}},
			records: []resource{{ID: "r1", Name: "e2e.example.com", Type: "dns"}},
			apps:    []resource{{ID: "a1", Name: "e2e-app", Type: "access_app"}},
			tokens:  []resource{{ID: "s1", Name: "e2e-token", Type: "service_token"}},
			zoneID:  "zone-1",
			deleteRecordErr: map[string]error{
				"r1": errors.New("dns delete failed"),
			},
			deleteAppErr:   map[string]error{},
			deleteTokenErr: map[string]error{},
		}
		buf := &bytes.Buffer{}

		summary, err := runCleanup(context.Background(), cfg, client, buf)
		if err == nil || !strings.Contains(err.Error(), "cleanup failed") {
			t.Fatalf("runCleanup() error = %v, want cleanup failure", err)
		}
		if summary.Found != 4 {
			t.Fatalf("Found = %d, want 4", summary.Found)
		}
		if summary.Deleted != 3 {
			t.Fatalf("Deleted = %d, want 3", summary.Deleted)
		}
		if summary.Failed != 1 {
			t.Fatalf("Failed = %d, want 1", summary.Failed)
		}
		if len(summary.FailedResources) != 1 || summary.FailedResources[0].Type != "dns" {
			t.Fatalf("FailedResources = %+v, want one dns resource", summary.FailedResources)
		}
		if !strings.Contains(buf.String(), "Deleting DNS record") {
			t.Fatalf("output = %q, want DNS delete log", buf.String())
		}
	})

	t.Run("skips DNS deletion when zone resolution fails", func(t *testing.T) {
		client := &fakeCleanupClient{
			records: []resource{{ID: "r1", Name: "e2e.example.com", Type: "dns"}},
			zoneErr: errors.New("missing zone"),
		}
		buf := &bytes.Buffer{}

		summary, err := runCleanup(context.Background(), cfg, client, buf)
		if err != nil {
			t.Fatalf("runCleanup() error = %v", err)
		}
		if summary.Deleted != 0 || summary.Failed != 0 {
			t.Fatalf("summary = %+v, want no deletions or failures", summary)
		}
		if !strings.Contains(buf.String(), "Warning: failed to resolve zone ID") {
			t.Fatalf("output = %q, want zone warning", buf.String())
		}
	})

	t.Run("counts tunnel deletion failures", func(t *testing.T) {
		client := &fakeCleanupClient{
			tunnels: []resource{{ID: "t1", Name: "e2e-tunnel", Type: "tunnel"}},
			zoneID:  "zone-1",
			deleteTunnelErr: map[string]error{
				"t1": errors.New("tunnel delete failed"),
			},
		}
		buf := &bytes.Buffer{}

		summary, err := runCleanup(context.Background(), cfg, client, buf)
		if err == nil || !strings.Contains(err.Error(), "cleanup failed") {
			t.Fatalf("runCleanup() error = %v, want cleanup failure", err)
		}
		if summary.Failed != 1 || summary.Deleted != 0 {
			t.Fatalf("summary = %+v, want one failed tunnel deletion", summary)
		}
		if !strings.Contains(buf.String(), "Deleting tunnel: e2e-tunnel ... FAILED") {
			t.Fatalf("output = %q, want failed tunnel deletion log", buf.String())
		}
	})
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
	origListTokens := listOrphanedServiceTokensFn
	origGetZoneID := getZoneIDFn
	origDeleteTunnel := deleteTunnelFn
	origDeleteDNS := deleteDNSRecordFn
	origDeleteApp := deleteAccessApplicationFn
	origDeleteToken := deleteServiceTokenFn
	t.Cleanup(func() {
		listOrphanedTunnelsFn = origListTunnels
		listOrphanedDNSRecordsFn = origListRecords
		listOrphanedAccessApplicationsFn = origListApps
		listOrphanedServiceTokensFn = origListTokens
		getZoneIDFn = origGetZoneID
		deleteTunnelFn = origDeleteTunnel
		deleteDNSRecordFn = origDeleteDNS
		deleteAccessApplicationFn = origDeleteApp
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

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
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

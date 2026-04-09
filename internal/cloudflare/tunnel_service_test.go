package cloudflare

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
)

func TestEnsureTunnel(t *testing.T) {
	ctx := context.Background()

	t.Run("adopts existing tunnel", func(t *testing.T) {
		mock := NewMockClient()
		mock.GetTunnelByNameFunc = func(context.Context, string, string) (*Tunnel, error) {
			return &Tunnel{ID: "t-1", Name: "edge"}, nil
		}

		tunnel, created, err := NewTunnelService(mock, logr.Discard()).EnsureTunnel(ctx, "account", "edge")
		if err != nil {
			t.Fatalf("EnsureTunnel() error = %v", err)
		}
		if created {
			t.Fatal("EnsureTunnel() created = true, want false")
		}
		if tunnel == nil || tunnel.ID != "t-1" {
			t.Fatalf("EnsureTunnel() tunnel = %+v, want adopted tunnel", tunnel)
		}
	})

	t.Run("creates missing tunnel with cloudflare config source", func(t *testing.T) {
		mock := NewMockClient()
		mock.CreateTunnelFunc = func(_ context.Context, accountID string, params CreateTunnelParams) (*Tunnel, error) {
			if accountID != "account" {
				t.Fatalf("accountID = %q, want %q", accountID, "account")
			}
			if params.Name != "edge" {
				t.Fatalf("params.Name = %q, want %q", params.Name, "edge")
			}
			if params.ConfigSrc != "cloudflare" {
				t.Fatalf("params.ConfigSrc = %q, want %q", params.ConfigSrc, "cloudflare")
			}
			return &Tunnel{ID: "t-2", Name: params.Name}, nil
		}

		tunnel, created, err := NewTunnelService(mock, logr.Discard()).EnsureTunnel(ctx, "account", "edge")
		if err != nil {
			t.Fatalf("EnsureTunnel() error = %v", err)
		}
		if !created {
			t.Fatal("EnsureTunnel() created = false, want true")
		}
		if tunnel == nil || tunnel.ID != "t-2" {
			t.Fatalf("EnsureTunnel() tunnel = %+v, want created tunnel", tunnel)
		}
	})

	t.Run("wraps lookup error", func(t *testing.T) {
		mock := NewMockClient()
		mock.GetTunnelByNameFunc = func(context.Context, string, string) (*Tunnel, error) {
			return nil, errors.New("lookup failed")
		}

		_, _, err := NewTunnelService(mock, logr.Discard()).EnsureTunnel(ctx, "account", "edge")
		if err == nil || err.Error() != "failed to check for existing tunnel: lookup failed" {
			t.Fatalf("EnsureTunnel() error = %v", err)
		}
	})

	t.Run("wraps create error", func(t *testing.T) {
		mock := NewMockClient()
		mock.CreateTunnelFunc = func(context.Context, string, CreateTunnelParams) (*Tunnel, error) {
			return nil, errors.New("create failed")
		}

		_, _, err := NewTunnelService(mock, logr.Discard()).EnsureTunnel(ctx, "account", "edge")
		if err == nil || err.Error() != "failed to create tunnel: create failed" {
			t.Fatalf("EnsureTunnel() error = %v", err)
		}
	})
}

func TestTunnelUpdateConfiguration(t *testing.T) {
	ctx := context.Background()

	t.Run("adds catch-all rule before update", func(t *testing.T) {
		mock := NewMockClient()
		mock.UpdateTunnelConfigurationFunc = func(_ context.Context, _, _ string, config TunnelConfiguration) error {
			if len(config.Ingress) != 2 {
				t.Fatalf("len(config.Ingress) = %d, want 2", len(config.Ingress))
			}
			last := config.Ingress[len(config.Ingress)-1]
			if last.Hostname != "" || last.Path != "" || last.Service != "http_status:404" {
				t.Fatalf("last ingress = %+v, want catch-all rule", last)
			}
			return nil
		}

		err := NewTunnelService(mock, logr.Discard()).UpdateConfiguration(ctx, "account", "tunnel-1", TunnelConfiguration{
			Ingress: []IngressRule{{
				Hostname: "app.example.com",
				Service:  "http://svc.default.svc.cluster.local:8080",
			}},
		})
		if err != nil {
			t.Fatalf("UpdateConfiguration() error = %v", err)
		}
	})

	t.Run("wraps update errors", func(t *testing.T) {
		mock := NewMockClient()
		mock.UpdateTunnelConfigurationFunc = func(context.Context, string, string, TunnelConfiguration) error {
			return errors.New("update failed")
		}

		err := NewTunnelService(mock, logr.Discard()).UpdateConfiguration(ctx, "account", "tunnel-1", TunnelConfiguration{})
		if err == nil || err.Error() != "failed to update tunnel configuration: update failed" {
			t.Fatalf("UpdateConfiguration() error = %v", err)
		}
	})
}

func TestTunnelDelete(t *testing.T) {
	ctx := context.Background()

	t.Run("continues when connection deletion fails", func(t *testing.T) {
		mock := NewMockClient()
		deleted := false
		mock.DeleteTunnelConnectionsFunc = func(context.Context, string, string) error {
			return errors.New("connection delete failed")
		}
		mock.DeleteTunnelFunc = func(context.Context, string, string) error {
			deleted = true
			return nil
		}

		err := NewTunnelService(mock, logr.Discard()).Delete(ctx, "account", "tunnel-1")
		if err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if !deleted {
			t.Fatal("Delete() did not continue to tunnel deletion")
		}
	})

	t.Run("wraps tunnel deletion errors", func(t *testing.T) {
		mock := NewMockClient()
		mock.DeleteTunnelFunc = func(context.Context, string, string) error {
			return errors.New("delete failed")
		}

		err := NewTunnelService(mock, logr.Discard()).Delete(ctx, "account", "tunnel-1")
		if err == nil || err.Error() != "failed to delete tunnel: delete failed" {
			t.Fatalf("Delete() error = %v", err)
		}
	})
}

func TestTunnelHealthAndHelpers(t *testing.T) {
	ctx := context.Background()

	t.Run("reports active and healthy statuses as healthy", func(t *testing.T) {
		for _, status := range []string{"healthy", "active"} {
			mock := NewMockClient()
			mock.GetTunnelFunc = func(context.Context, string, string) (*Tunnel, error) {
				return &Tunnel{ID: "tunnel-1", Status: status}, nil
			}

			healthy, err := NewTunnelService(mock, logr.Discard()).IsHealthy(ctx, "account", "tunnel-1")
			if err != nil {
				t.Fatalf("IsHealthy() error = %v", err)
			}
			if !healthy {
				t.Fatalf("IsHealthy() = false, want true for status %q", status)
			}
		}
	})

	t.Run("returns false for missing or unhealthy tunnel", func(t *testing.T) {
		mock := NewMockClient()
		mock.GetTunnelFunc = func(context.Context, string, string) (*Tunnel, error) {
			return nil, nil
		}
		healthy, err := NewTunnelService(mock, logr.Discard()).IsHealthy(ctx, "account", "tunnel-1")
		if err != nil {
			t.Fatalf("IsHealthy() error = %v", err)
		}
		if healthy {
			t.Fatal("IsHealthy() = true, want false for missing tunnel")
		}

		mock.GetTunnelFunc = func(context.Context, string, string) (*Tunnel, error) {
			return &Tunnel{ID: "tunnel-1", Status: "inactive"}, nil
		}
		healthy, err = NewTunnelService(mock, logr.Discard()).IsHealthy(ctx, "account", "tunnel-1")
		if err != nil {
			t.Fatalf("IsHealthy() error = %v", err)
		}
		if healthy {
			t.Fatal("IsHealthy() = true, want false for inactive tunnel")
		}
	})

	t.Run("wraps get errors", func(t *testing.T) {
		mock := NewMockClient()
		mock.GetTunnelFunc = func(context.Context, string, string) (*Tunnel, error) {
			return nil, errors.New("get failed")
		}

		_, err := NewTunnelService(mock, logr.Discard()).IsHealthy(ctx, "account", "tunnel-1")
		if err == nil || err.Error() != "failed to get tunnel: get failed" {
			t.Fatalf("IsHealthy() error = %v", err)
		}
	})

	t.Run("build configuration ensures catch-all rule", func(t *testing.T) {
		config := BuildConfiguration([]IngressRule{{
			Hostname: "app.example.com",
			Service:  "http://svc.default.svc.cluster.local:8080",
		}}, nil)
		if len(config.Ingress) != 2 {
			t.Fatalf("len(config.Ingress) = %d, want 2", len(config.Ingress))
		}
		last := config.Ingress[len(config.Ingress)-1]
		if last.Service != "http_status:404" {
			t.Fatalf("last.Service = %q, want %q", last.Service, "http_status:404")
		}
	})

	t.Run("keeps existing catch-all rule", func(t *testing.T) {
		config := ensureCatchAllRule(TunnelConfiguration{
			Ingress: []IngressRule{
				{Hostname: "app.example.com", Service: "http://svc.default.svc.cluster.local:8080"},
				{Service: "http_status:404"},
			},
		})
		if len(config.Ingress) != 2 {
			t.Fatalf("len(config.Ingress) = %d, want 2", len(config.Ingress))
		}
	})

	t.Run("builds tunnel domain", func(t *testing.T) {
		if got := TunnelDomain("abcd1234"); got != "abcd1234.cfargotunnel.com" {
			t.Fatalf("TunnelDomain() = %q, want %q", got, "abcd1234.cfargotunnel.com")
		}
	})
}

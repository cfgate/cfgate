package controller

import (
	"context"
	"errors"
	"testing"

	"cfgate.io/cfgate/internal/cloudflare"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestAccessWithdrawalDistinguishesDeletedAndDownTunnels(t *testing.T) {
	for _, test := range []struct {
		name          string
		remote        *cloudflare.Tunnel
		pending       bool
		readError     error
		wantBlocked   bool
		wantConfigGet bool
	}{
		{name: "deleted retains old configuration"},
		{name: "deleted resolves uncertain publication", pending: true},
		{name: "down remains published", remote: &cloudflare.Tunnel{ID: "remote-id", Status: "down"}, wantBlocked: true, wantConfigGet: true},
		{name: "active pending remains uncertain", remote: &cloudflare.Tunnel{ID: "remote-id"}, pending: true, wantBlocked: true},
		{name: "read failure preserves protection", readError: errors.New("remote observation failed"), wantBlocked: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newAccessFixture(t)
			f.sync(t)
			if len(f.tunnel.Status.AccessDependencies) == 0 {
				t.Fatal("published Access dependency missing")
			}
			if test.pending {
				for i := range f.tunnel.Status.AccessDependencies {
					f.tunnel.Status.AccessDependencies[i].Pending = true
				}
				if err := f.r.Status().Update(context.Background(), f.tunnel); err != nil {
					t.Fatal(err)
				}
			}
			mock := f.r.CFClient.(*cloudflare.MockClient)
			existenceReads, configReads := 0, 0
			mock.GetTunnelFunc = func(_ context.Context, account, id string) (*cloudflare.Tunnel, error) {
				existenceReads++
				if account != "account" || id != "remote-id" {
					t.Fatalf("observed wrong remote identity: %s/%s", account, id)
				}
				return test.remote, test.readError
			}
			mock.GetTunnelConfigurationFunc = func(context.Context, string, string) (*cloudflare.TunnelConfiguration, error) {
				configReads++
				return f.remoteConfig, nil
			}
			err := f.r.verifyAccessWithdrawal(context.Background(), []string{client.ObjectKeyFromObject(f.app).String()})
			if (err != nil) != test.wantBlocked {
				t.Fatalf("withdrawal error=%v, want blocked=%t", err, test.wantBlocked)
			}
			if test.readError != nil && !errors.Is(err, test.readError) {
				t.Fatalf("remote observation error lost: %v", err)
			}
			if existenceReads == 0 || (configReads > 0) != test.wantConfigGet {
				t.Fatalf("unexpected observations: tunnel=%d config=%d", existenceReads, configReads)
			}
		})
	}
}

package cloudflare

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

func TestEnsureServiceToken(t *testing.T) {
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name               string
		duration           string
		expires            time.Duration
		stale, disabled    bool
		updates, rotations int
	}{
		{name: "unchanged", duration: "48h", expires: 48 * time.Hour},
		{name: "equivalent duration", duration: "2880m", expires: 48 * time.Hour},
		{name: "expired renews without rotation", duration: "48h", expires: -time.Hour, updates: 1},
		{name: "renew before expiration", duration: "48h", expires: time.Hour, updates: 1},
		{name: "duration edit", duration: "24h", expires: 24 * time.Hour, updates: 1},
		{name: "missing secret rotates without renewal", duration: "48h", expires: 48 * time.Hour, stale: true, rotations: 1},
		{name: "expired missing secret needs both", duration: "48h", expires: -time.Hour, stale: true, updates: 1, rotations: 1},
		{name: "disabled is unavailable", duration: "48h", expires: -time.Hour, disabled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			enabled := !tt.disabled
			token := ServiceToken{ID: "token", Name: "svc", ClientID: "client", Duration: tt.duration, ExpiresAt: now.Add(tt.expires), Enabled: &enabled}
			mock := NewMockClient()
			updates, rotations := 0, 0
			mock.ListServiceTokensFunc = func(context.Context, string) ([]ServiceToken, error) { return []ServiceToken{token}, nil }
			mock.UpdateServiceTokenFunc = func(_ context.Context, _, id string, p ServiceTokenParams) (*ServiceToken, error) {
				updates++
				if id != token.ID {
					t.Fatal("identity changed")
				}
				token.Duration = p.Duration
				token.ExpiresAt = now.Add(48 * time.Hour)
				return &token, nil
			}
			mock.RotateServiceTokenFunc = func(_ context.Context, _, id string, p ServiceTokenRotateParams) (*ServiceTokenWithSecret, error) {
				rotations++
				if id != token.ID || !p.PreviousClientSecretExpiresAt.Equal(now.Add(time.Hour)) {
					t.Fatalf("bad rotation: %s %+v", id, p)
				}
				// Provider-faithful: rotation preserves identity and expiration.
				return &ServiceTokenWithSecret{ServiceToken: token, ClientSecret: "new-secret"}, nil
			}
			writer := &refreshCheckingSecretWriter{needsRefresh: tt.stale}
			svc := NewAccessService(mock, logr.Discard())
			svc.now = func() time.Time { return now }
			params := ServiceTokenParams{Name: "svc", Duration: "48h", RotationOverlap: time.Hour}
			got, err := svc.EnsureServiceToken(context.Background(), "account", params, writer)
			if tt.disabled {
				if err == nil {
					t.Fatal("disabled token accepted")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !got.ExpiresAt.After(now) {
					t.Fatal("token still expired")
				}
				writer.needsRefresh = false
				if _, err := svc.EnsureServiceToken(context.Background(), "account", params, writer); err != nil {
					t.Fatal(err)
				}
			}
			if updates != tt.updates || rotations != tt.rotations || writer.calls != tt.rotations {
				t.Fatalf("updates/rotations/writes=%d/%d/%d", updates, rotations, writer.calls)
			}
		})
	}
}

type intentSecretWriter struct {
	recordingSecretWriter
	pending  bool
	beginErr error
}

func (w *intentSecretWriter) BeginServiceTokenRotation(context.Context, string) error {
	if w.beginErr != nil {
		return w.beginErr
	}
	w.pending = true
	return nil
}
func (w *intentSecretWriter) ServiceTokenSecretNeedsRefresh(context.Context, string, string) (bool, error) {
	return w.pending, nil
}
func (w *intentSecretWriter) WriteSecret(ctx context.Context, name string, data map[string][]byte) error {
	if err := w.recordingSecretWriter.WriteSecret(ctx, name, data); err != nil {
		return err
	}
	w.pending = false
	return nil
}

func TestServiceTokenMutationRecovery(t *testing.T) {
	for _, creation := range []bool{true, false} {
		t.Run(map[bool]string{true: "create", false: "rotate"}[creation], func(t *testing.T) {
			token := ServiceToken{ID: "token", Name: "svc", ClientID: "client", Duration: "48h", ExpiresAt: time.Now().Add(48 * time.Hour)}
			exists := !creation
			calls := 0
			writer := &intentSecretWriter{recordingSecretWriter: recordingSecretWriter{err: errors.New("write unavailable")}, pending: true}
			mock := NewMockClient()
			mock.ListServiceTokensFunc = func(context.Context, string) ([]ServiceToken, error) {
				if exists {
					return []ServiceToken{token}, nil
				}
				return nil, nil
			}
			mutate := func() (*ServiceTokenWithSecret, error) {
				if !writer.pending {
					t.Fatal("mutation without durable intent")
				}
				calls++
				exists = true
				return &ServiceTokenWithSecret{ServiceToken: token, ClientSecret: "new-secret"}, nil
			}
			mock.CreateServiceTokenFunc = func(context.Context, string, ServiceTokenParams) (*ServiceTokenWithSecret, error) { return mutate() }
			mock.RotateServiceTokenFunc = func(context.Context, string, string, ServiceTokenRotateParams) (*ServiceTokenWithSecret, error) {
				return mutate()
			}
			mock.DeleteServiceTokenFunc = func(context.Context, string, string) error { t.Fatal("deleted recoverable identity"); return nil }
			svc := NewAccessService(mock, logr.Discard())
			params := ServiceTokenParams{Name: "svc", Duration: "48h"}
			if _, err := svc.EnsureServiceToken(context.Background(), "account", params, writer); err == nil {
				t.Fatal("expected write error")
			}
			if !writer.pending {
				t.Fatal("lost recovery obligation")
			}
			writer.err = nil
			if _, err := svc.EnsureServiceToken(context.Background(), "account", params, writer); err != nil {
				t.Fatal(err)
			}
			if writer.pending || calls != 2 {
				t.Fatalf("pending=%v calls=%d", writer.pending, calls)
			}
			if _, err := svc.EnsureServiceToken(context.Background(), "account", params, writer); err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatal("unnecessary repeated rotation")
			}
		})
	}
}

func TestServiceTokenMutationFailures(t *testing.T) {
	for _, stage := range []string{"list", "preflight", "intent", "create", "rotate", "update", "bad identity", "empty secret"} {
		t.Run(stage, func(t *testing.T) {
			mock := NewMockClient()
			writer := &intentSecretWriter{pending: true}
			boom := errors.New("unavailable")
			token := ServiceToken{ID: "token", Name: "svc", ClientID: "client", Duration: "48h", ExpiresAt: time.Now().Add(48 * time.Hour)}
			mock.ListServiceTokensFunc = func(context.Context, string) ([]ServiceToken, error) {
				if stage == "list" {
					return nil, boom
				}
				if stage == "create" {
					return nil, nil
				}
				if stage == "update" {
					token.ExpiresAt = time.Now().Add(-time.Hour)
				}
				return []ServiceToken{token}, nil
			}
			if stage == "intent" {
				writer.beginErr = boom
			}
			mock.CreateServiceTokenFunc = func(context.Context, string, ServiceTokenParams) (*ServiceTokenWithSecret, error) { return nil, boom }
			mock.UpdateServiceTokenFunc = func(context.Context, string, string, ServiceTokenParams) (*ServiceToken, error) { return nil, boom }
			mock.RotateServiceTokenFunc = func(context.Context, string, string, ServiceTokenRotateParams) (*ServiceTokenWithSecret, error) {
				if stage == "bad identity" {
					token.ID = "other"
					return &ServiceTokenWithSecret{ServiceToken: token, ClientSecret: "secret"}, nil
				}
				if stage == "empty secret" {
					return &ServiceTokenWithSecret{ServiceToken: token}, nil
				}
				return nil, boom
			}
			var store SecretWriter = writer
			if stage == "preflight" {
				store = &refreshCheckingSecretWriter{checkErr: boom}
			}
			if _, err := NewAccessService(mock, logr.Discard()).EnsureServiceToken(context.Background(), "account", ServiceTokenParams{Name: "svc", Duration: "48h"}, store); err == nil {
				t.Fatal("expected failure")
			}
		})
	}
}

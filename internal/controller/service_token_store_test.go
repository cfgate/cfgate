package controller

import (
	"context"
	"errors"
	"testing"

	cfg "cfgate.io/cfgate/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestTokenSecretPendingSurvivesFailedWrite(t *testing.T) {
	ctx := context.Background()
	scheme := controllerTestScheme(t)
	owner := baseAccessPolicy("app", "policy")
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: "app"}, Data: map[string][]byte{"CF_ACCESS_CLIENT_ID": []byte("client"), "CF_ACCESS_CLIENT_SECRET": []byte("old")}}
	if err := controllerutil.SetControllerReference(owner, secret, scheme); err != nil {
		t.Fatal(err)
	}
	fail := false
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(owner, secret).WithInterceptorFuncs(interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if fail {
				return errors.New("write denied")
			}
			return c.Update(ctx, obj, opts...)
		},
	}).Build()
	newWriter := func() *k8sSecretWriter {
		return &k8sSecretWriter{client: c, reader: c, namespace: "app", secretRef: cfg.ServiceTokenSecretRef{Name: "token"}, owner: owner, scheme: scheme}
	}
	w := newWriter()
	if err := w.BeginServiceTokenRotation(ctx, "svc"); err != nil {
		t.Fatal(err)
	}
	data := map[string][]byte{"CF_ACCESS_CLIENT_ID": []byte("client"), "CF_ACCESS_CLIENT_SECRET": []byte("new")}
	fail = true
	if err := w.WriteSecret(ctx, "svc", data); err == nil {
		t.Fatal("expected failed write")
	}
	w = newWriter() // Restart: no in-memory mutation state survives.
	stale, err := w.ServiceTokenSecretNeedsRefresh(ctx, "svc", "client")
	if err != nil || !stale {
		t.Fatalf("lost pending intent: %v %v", stale, err)
	}
	fail = false
	if err := w.WriteSecret(ctx, "svc", data); err != nil {
		t.Fatal(err)
	}
	stale, err = w.ServiceTokenSecretNeedsRefresh(ctx, "svc", "client")
	if err != nil || stale {
		t.Fatalf("did not complete intent: %v %v", stale, err)
	}
}

func TestTokenImmutableSecretPreflight(t *testing.T) {
	for _, complete := range []bool{false, true} {
		scheme := controllerTestScheme(t)
		owner := baseAccessPolicy("app", "policy")
		immutable := true
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: "app"}, Immutable: &immutable}
		if complete {
			secret.Data = map[string][]byte{"CF_ACCESS_CLIENT_ID": []byte("client"), "CF_ACCESS_CLIENT_SECRET": []byte("secret")}
		}
		if err := controllerutil.SetControllerReference(owner, secret, scheme); err != nil {
			t.Fatal(err)
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(owner, secret).Build()
		w := &k8sSecretWriter{client: c, namespace: "app", secretRef: cfg.ServiceTokenSecretRef{Name: "token"}, owner: owner, scheme: scheme}
		_, err := w.ServiceTokenSecretNeedsRefresh(context.Background(), "svc", "client")
		if (err == nil) != complete {
			t.Fatalf("complete=%v err=%v", complete, err)
		}
		if err := w.BeginServiceTokenRotation(context.Background(), "svc"); err == nil {
			t.Fatal("immutable destination allowed rotation")
		}
	}
}

func TestServiceTokenConfigurationValidation(t *testing.T) {
	valid := cfg.ServiceTokenConfig{Name: "one", Duration: "48h", SecretRef: cfg.ServiceTokenSecretRef{Name: "one"}}
	for _, tt := range []struct {
		name      string
		tokens    []cfg.ServiceTokenConfig
		wantError bool
	}{
		{"valid", []cfg.ServiceTokenConfig{valid}, false},
		{"duplicate token", []cfg.ServiceTokenConfig{valid, {Name: "one", SecretRef: cfg.ServiceTokenSecretRef{Name: "two"}}}, true},
		{"duplicate destination", []cfg.ServiceTokenConfig{valid, {Name: "two", SecretRef: cfg.ServiceTokenSecretRef{Name: "one"}}}, true},
		{"zero duration", []cfg.ServiceTokenConfig{{Name: "one", Duration: "0h"}}, true},
		{"excess overlap", []cfg.ServiceTokenConfig{{Name: "one", RotationOverlap: "721h"}}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateServiceTokens(tt.tokens); (err != nil) != tt.wantError {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

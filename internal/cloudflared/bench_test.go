package cloudflared

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
)

func BenchmarkBuildOriginConfig(b *testing.B) {
	defaults := &cfgatev1alpha1.OriginDefaults{
		ConnectTimeout: "30s",
		HTTP2Origin:    true,
	}
	annotations := map[string]string{
		"cfgate.io/origin-no-tls-verify":     "true",
		"cfgate.io/origin-keepalive-timeout": "30s",
	}

	b.ReportAllocs()
	for b.Loop() {
		_ = BuildOriginConfig(defaults, annotations)
	}
}

func BenchmarkBuildArgs(b *testing.B) {
	tunnel := &cfgatev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "bench", Namespace: "cfgate-system"},
		Spec: cfgatev1alpha1.CloudflareTunnelSpec{
			Tunnel: cfgatev1alpha1.TunnelIdentity{Name: "bench"},
		},
	}

	b.ReportAllocs()
	for b.Loop() {
		_ = buildArgs(tunnel)
	}
}

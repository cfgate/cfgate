package cloudflared

import (
	"fmt"
	"testing"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newDeploymentTestTunnel(name string, opts ...func(*cfgatev1alpha1.CloudflareTunnel)) *cfgatev1alpha1.CloudflareTunnel {
	tunnel := &cfgatev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: cfgatev1alpha1.CloudflareTunnelSpec{
			Tunnel: cfgatev1alpha1.TunnelIdentity{
				Name: name,
			},
			Cloudflare: cfgatev1alpha1.CloudflareConfig{
				AccountID: "abc123",
				SecretRef: cfgatev1alpha1.SecretRef{
					Name: "cf-secret",
				},
			},
		},
	}
	for _, opt := range opts {
		opt(tunnel)
	}
	return tunnel
}

func TestBuildProbes(t *testing.T) {
	metricsPort := int32(DefaultMetricsPort)
	liveness, readiness := buildProbes(metricsPort)

	t.Run("liveness probe path", func(t *testing.T) {
		if liveness.HTTPGet == nil {
			t.Fatal("liveness probe HTTPGet should not be nil")
		}
		if liveness.HTTPGet.Path != "/healthcheck" {
			t.Errorf("liveness path = %q, want %q", liveness.HTTPGet.Path, "/healthcheck")
		}
	})

	t.Run("readiness probe path", func(t *testing.T) {
		if readiness.HTTPGet == nil {
			t.Fatal("readiness probe HTTPGet should not be nil")
		}
		if readiness.HTTPGet.Path != "/ready" {
			t.Errorf("readiness path = %q, want %q", readiness.HTTPGet.Path, "/ready")
		}
	})

	t.Run("probes use correct metrics port", func(t *testing.T) {
		if liveness.HTTPGet.Port.IntVal != metricsPort {
			t.Errorf("liveness port = %d, want %d", liveness.HTTPGet.Port.IntVal, metricsPort)
		}
		if readiness.HTTPGet.Port.IntVal != metricsPort {
			t.Errorf("readiness port = %d, want %d", readiness.HTTPGet.Port.IntVal, metricsPort)
		}
	})
}

func TestBuildArgs(t *testing.T) {
	t.Run("default args", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		args := buildArgs(tunnel)

		wantContains := []string{"tunnel", "--no-autoupdate", "--metrics", "run", "--token"}
		for _, want := range wantContains {
			found := false
			for _, arg := range args {
				if arg == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("args should contain %q, got %v", want, args)
			}
		}
	})

	t.Run("protocol specified", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.Cloudflared.Protocol = "quic"
		})
		args := buildArgs(tunnel)

		found := false
		for i, arg := range args {
			if arg == "--protocol" && i+1 < len(args) && args[i+1] == "quic" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("args should contain '--protocol quic', got %v", args)
		}
	})

	t.Run("protocol auto omitted", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.Cloudflared.Protocol = "auto"
		})
		args := buildArgs(tunnel)

		for _, arg := range args {
			if arg == "--protocol" {
				t.Errorf("args should NOT contain '--protocol' when protocol is auto, got %v", args)
				break
			}
		}
	})

	t.Run("extra args included", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.Cloudflared.ExtraArgs = []string{"--loglevel", "debug"}
		})
		args := buildArgs(tunnel)

		foundLoglevel := false
		foundDebug := false
		for _, arg := range args {
			if arg == "--loglevel" {
				foundLoglevel = true
			}
			if arg == "debug" {
				foundDebug = true
			}
		}
		if !foundLoglevel || !foundDebug {
			t.Errorf("args should contain '--loglevel debug', got %v", args)
		}
	})
}

func TestBuildContainer(t *testing.T) {
	t.Run("default image", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		container := buildContainer(tunnel, "test-token-secret")

		if container.Image != DefaultImage {
			t.Errorf("Image = %q, want %q", container.Image, DefaultImage)
		}
	})

	t.Run("custom image", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.Cloudflared.Image = "custom/cloudflared:v1.0"
		})
		container := buildContainer(tunnel, "test-token-secret")

		if container.Image != "custom/cloudflared:v1.0" {
			t.Errorf("Image = %q, want %q", container.Image, "custom/cloudflared:v1.0")
		}
	})

	t.Run("default pull policy", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		container := buildContainer(tunnel, "test-token-secret")

		if container.ImagePullPolicy != corev1.PullIfNotPresent {
			t.Errorf("ImagePullPolicy = %q, want %q", container.ImagePullPolicy, corev1.PullIfNotPresent)
		}
	})

	t.Run("default resources set", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		container := buildContainer(tunnel, "test-token-secret")

		if container.Resources.Requests == nil {
			t.Error("default resource requests should be set")
		}
		if container.Resources.Limits == nil {
			t.Error("default resource limits should be set")
		}
	})
}

func TestBuildDeployment(t *testing.T) {
	builder := NewBuilder()

	t.Run("correct labels", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		deployment := builder.BuildDeployment(tunnel, "token-value")

		wantLabels := Labels("test")
		for k, v := range wantLabels {
			if deployment.Labels[k] != v {
				t.Errorf("label %q = %q, want %q", k, deployment.Labels[k], v)
			}
		}
	})

	t.Run("default replicas", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		deployment := builder.BuildDeployment(tunnel, "token-value")

		if deployment.Spec.Replicas == nil {
			t.Fatal("Replicas should not be nil")
		}
		if *deployment.Spec.Replicas != 2 {
			t.Errorf("Replicas = %d, want 2", *deployment.Spec.Replicas)
		}
	})

	t.Run("custom replicas", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.Cloudflared.Replicas = 5
		})
		deployment := builder.BuildDeployment(tunnel, "token-value")

		if *deployment.Spec.Replicas != 5 {
			t.Errorf("Replicas = %d, want 5", *deployment.Spec.Replicas)
		}
	})

	t.Run("node selector applied", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.Cloudflared.NodeSelector = map[string]string{
				"node-role": "edge",
			}
		})
		deployment := builder.BuildDeployment(tunnel, "token-value")

		ns := deployment.Spec.Template.Spec.NodeSelector
		if ns == nil || ns["node-role"] != "edge" {
			t.Errorf("NodeSelector = %v, want map[node-role:edge]", ns)
		}
	})

	t.Run("tolerations applied", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.Cloudflared.Tolerations = []corev1.Toleration{
				{Key: "special", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
			}
		})
		deployment := builder.BuildDeployment(tunnel, "token-value")

		tols := deployment.Spec.Template.Spec.Tolerations
		if len(tols) != 1 {
			t.Fatalf("expected 1 toleration, got %d", len(tols))
		}
		if tols[0].Key != "special" {
			t.Errorf("toleration key = %q, want %q", tols[0].Key, "special")
		}
	})

	t.Run("deployment name format", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("my-tunnel")
		deployment := builder.BuildDeployment(tunnel, "token-value")

		want := fmt.Sprintf("%s-cloudflared", "my-tunnel")
		if deployment.Name != want {
			t.Errorf("deployment name = %q, want %q", deployment.Name, want)
		}
	})
}

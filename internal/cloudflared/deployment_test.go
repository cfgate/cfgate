package cloudflared

import (
	"fmt"
	"strings"
	"testing"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
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

func TestOriginCAPoolVolumeNameFor(t *testing.T) {
	t.Run("short name unchanged", func(t *testing.T) {
		got := OriginCAPoolVolumeNameFor("backendtls", "apps", "tls-app")
		if got != "origin-ca-backendtls-apps-tls-app" {
			t.Fatalf("OriginCAPoolVolumeNameFor() = %q, want origin-ca-backendtls-apps-tls-app", got)
		}
	})

	t.Run("long name fallback is valid", func(t *testing.T) {
		got := OriginCAPoolVolumeNameFor(strings.Repeat("a-", 30) + "backendtls")
		if len(got) > 63 {
			t.Fatalf("len(OriginCAPoolVolumeNameFor()) = %d, want <= 63: %q", len(got), got)
		}
		if errs := validation.IsDNS1123Label(got); len(errs) > 0 {
			t.Fatalf("OriginCAPoolVolumeNameFor() = %q, want DNS-1123 label: %v", got, errs)
		}
		if !strings.HasPrefix(got, "origin-ca-") {
			t.Fatalf("OriginCAPoolVolumeNameFor() = %q, want origin-ca- prefix", got)
		}
		if strings.Contains(got, "--") {
			t.Fatalf("OriginCAPoolVolumeNameFor() = %q, want no double hyphen", got)
		}
	})

	t.Run("fallback deterministic", func(t *testing.T) {
		parts := []string{"backendtls", strings.Repeat("service-", 12), "tls-app"}
		first := OriginCAPoolVolumeNameFor(parts...)
		second := OriginCAPoolVolumeNameFor(parts...)
		if first != second {
			t.Fatalf("OriginCAPoolVolumeNameFor() = %q then %q, want deterministic output", first, second)
		}
	})
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

	t.Run("default metrics port is 44483", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		args := buildArgs(tunnel)

		found := false
		for i, arg := range args {
			if arg == "--metrics" && i+1 < len(args) && args[i+1] == "0.0.0.0:44483" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("args should contain default metrics address, got %v", args)
		}
	})

	t.Run("metrics disabled omits metrics arg", func(t *testing.T) {
		disabled := false
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.Cloudflared.Metrics.Enabled = &disabled
		})
		args := buildArgs(tunnel)

		for _, arg := range args {
			if arg == "--metrics" {
				t.Errorf("args should not contain metrics when disabled, got %v", args)
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

	t.Run("metrics disabled omits port", func(t *testing.T) {
		disabled := false
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.Cloudflared.Metrics.Enabled = &disabled
		})
		container := buildContainer(tunnel, "test-token-secret")

		if len(container.Ports) != 0 {
			t.Fatalf("Ports = %+v, want none", container.Ports)
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

	t.Run("metrics disabled omits probes", func(t *testing.T) {
		disabled := false
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.Cloudflared.Metrics.Enabled = &disabled
		})
		deployment := builder.BuildDeployment(tunnel, "token-value")
		container := deployment.Spec.Template.Spec.Containers[0]

		if container.LivenessProbe != nil || container.ReadinessProbe != nil {
			t.Fatalf("probes = (%+v, %+v), want nil", container.LivenessProbe, container.ReadinessProbe)
		}
	})

	t.Run("ca pool secret volume and mount", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.OriginDefaults.CAPoolSecretRef = &cfgatev1alpha1.CAPoolSecretRef{
				Name: "origin-ca",
				Key:  "bundle.pem",
			}
		})
		deployment := builder.BuildDeployment(tunnel, "token-value")

		if len(deployment.Spec.Template.Spec.Volumes) != 1 {
			t.Fatalf("volumes = %+v, want 1", deployment.Spec.Template.Spec.Volumes)
		}
		volume := deployment.Spec.Template.Spec.Volumes[0]
		if volume.Name != OriginCAPoolVolumeName || volume.Secret == nil || volume.Secret.SecretName != "origin-ca" {
			t.Fatalf("volume = %+v, want origin CA Secret volume", volume)
		}
		if got := volume.Secret.Items[0].Key; got != "bundle.pem" {
			t.Fatalf("Secret item key = %q, want bundle.pem", got)
		}
		if got := volume.Secret.Items[0].Path; got != OriginCAPoolFileName {
			t.Fatalf("Secret item path = %q, want %q", got, OriginCAPoolFileName)
		}

		mounts := deployment.Spec.Template.Spec.Containers[0].VolumeMounts
		if len(mounts) != 1 {
			t.Fatalf("mounts = %+v, want 1", mounts)
		}
		if mounts[0].Name != OriginCAPoolVolumeName || mounts[0].MountPath != OriginCAPoolMountPath || !mounts[0].ReadOnly {
			t.Fatalf("mount = %+v, want read-only origin CA mount", mounts[0])
		}
	})

	t.Run("ca pool empty key defaults to ca.crt", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.OriginDefaults.CAPoolSecretRef = &cfgatev1alpha1.CAPoolSecretRef{Name: "origin-ca"}
		})
		deployment := builder.BuildDeployment(tunnel, "token-value")
		item := deployment.Spec.Template.Spec.Volumes[0].Secret.Items[0]

		if item.Key != DefaultOriginCAPoolSecretKey {
			t.Fatalf("Secret item key = %q, want %q", item.Key, DefaultOriginCAPoolSecretKey)
		}
	})
}

func TestBuildConfigMap(t *testing.T) {
	builder := NewBuilder()

	t.Run("name follows convention", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("my-tunnel")
		config := &TunnelConfig{
			TunnelID: "test-id",
			Ingress:  []IngressRule{{Service: "http_status:404"}},
		}
		cm, err := builder.BuildConfigMap(tunnel, config)
		if err != nil {
			t.Fatalf("BuildConfigMap() error: %v", err)
		}
		want := "my-tunnel-cloudflared-config"
		if cm.Name != want {
			t.Errorf("name = %q, want %q", cm.Name, want)
		}
	})

	t.Run("namespace matches tunnel", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Namespace = "prod"
		})
		config := &TunnelConfig{
			TunnelID: "test-id",
			Ingress:  []IngressRule{{Service: "http_status:404"}},
		}
		cm, err := builder.BuildConfigMap(tunnel, config)
		if err != nil {
			t.Fatalf("BuildConfigMap() error: %v", err)
		}
		if cm.Namespace != "prod" {
			t.Errorf("namespace = %q, want %q", cm.Namespace, "prod")
		}
	})

	t.Run("labels match standard labels", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		config := &TunnelConfig{
			TunnelID: "test-id",
			Ingress:  []IngressRule{{Service: "http_status:404"}},
		}
		cm, err := builder.BuildConfigMap(tunnel, config)
		if err != nil {
			t.Fatalf("BuildConfigMap() error: %v", err)
		}
		wantLabels := Labels("test")
		for k, v := range wantLabels {
			if cm.Labels[k] != v {
				t.Errorf("label %q = %q, want %q", k, cm.Labels[k], v)
			}
		}
	})

	t.Run("config data key is config.yaml", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		config := &TunnelConfig{
			TunnelID: "test-id",
			Ingress:  []IngressRule{{Service: "http_status:404"}},
		}
		cm, err := builder.BuildConfigMap(tunnel, config)
		if err != nil {
			t.Fatalf("BuildConfigMap() error: %v", err)
		}
		if _, ok := cm.Data["config.yaml"]; !ok {
			t.Error("ConfigMap data should have 'config.yaml' key")
		}
	})

	t.Run("config data is valid YAML", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		config := &TunnelConfig{
			TunnelID: "test-id",
			Ingress: []IngressRule{
				{Hostname: "example.com", Service: "http://web:80"},
				{Service: "http_status:404"},
			},
		}
		cm, err := builder.BuildConfigMap(tunnel, config)
		if err != nil {
			t.Fatalf("BuildConfigMap() error: %v", err)
		}
		parsed, err := ParseConfig([]byte(cm.Data["config.yaml"]))
		if err != nil {
			t.Fatalf("config data is not valid YAML: %v", err)
		}
		if parsed.TunnelID != "test-id" {
			t.Errorf("parsed TunnelID = %q, want %q", parsed.TunnelID, "test-id")
		}
	})

	t.Run("config with multiple ingress rules", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		config := &TunnelConfig{
			TunnelID: "test-id",
			Ingress: []IngressRule{
				{Hostname: "a.com", Service: "http://a:80"},
				{Hostname: "b.com", Service: "http://b:80"},
				{Service: "http_status:404"},
			},
		}
		cm, err := builder.BuildConfigMap(tunnel, config)
		if err != nil {
			t.Fatalf("BuildConfigMap() error: %v", err)
		}
		parsed, err := ParseConfig([]byte(cm.Data["config.yaml"]))
		if err != nil {
			t.Fatalf("ParseConfig() error: %v", err)
		}
		if len(parsed.Ingress) != 3 {
			t.Errorf("expected 3 ingress rules, got %d", len(parsed.Ingress))
		}
	})
}

func TestBuildTokenSecret(t *testing.T) {
	builder := NewBuilder()

	t.Run("name follows convention", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("my-tunnel")
		secret := builder.BuildTokenSecret(tunnel, "token-value")
		want := "my-tunnel-tunnel-token"
		if secret.Name != want {
			t.Errorf("name = %q, want %q", secret.Name, want)
		}
	})

	t.Run("namespace matches tunnel", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Namespace = "prod"
		})
		secret := builder.BuildTokenSecret(tunnel, "token-value")
		if secret.Namespace != "prod" {
			t.Errorf("namespace = %q, want %q", secret.Namespace, "prod")
		}
	})

	t.Run("labels match standard labels", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		secret := builder.BuildTokenSecret(tunnel, "token-value")
		wantLabels := Labels("test")
		for k, v := range wantLabels {
			if secret.Labels[k] != v {
				t.Errorf("label %q = %q, want %q", k, secret.Labels[k], v)
			}
		}
	})

	t.Run("secret type is Opaque", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		secret := builder.BuildTokenSecret(tunnel, "token-value")
		if secret.Type != corev1.SecretTypeOpaque {
			t.Errorf("type = %q, want %q", secret.Type, corev1.SecretTypeOpaque)
		}
	})

	t.Run("token stored under correct key", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		secret := builder.BuildTokenSecret(tunnel, "my-token-123")
		if v, ok := secret.StringData[TokenSecretKey]; !ok {
			t.Errorf("StringData should have key %q", TokenSecretKey)
		} else if v != "my-token-123" {
			t.Errorf("token = %q, want %q", v, "my-token-123")
		}
	})

	t.Run("empty token", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		secret := builder.BuildTokenSecret(tunnel, "")
		if v := secret.StringData[TokenSecretKey]; v != "" {
			t.Errorf("token = %q, want empty", v)
		}
	})
}

func TestNameHelpers(t *testing.T) {
	t.Run("ConfigMapName format", func(t *testing.T) {
		if got := ConfigMapName("my-tunnel"); got != "my-tunnel-cloudflared-config" {
			t.Errorf("ConfigMapName = %q, want %q", got, "my-tunnel-cloudflared-config")
		}
	})

	t.Run("TokenSecretName format", func(t *testing.T) {
		if got := TokenSecretName("my-tunnel"); got != "my-tunnel-tunnel-token" {
			t.Errorf("TokenSecretName = %q, want %q", got, "my-tunnel-tunnel-token")
		}
	})

	t.Run("Labels has 4 keys", func(t *testing.T) {
		labels := Labels("test")
		if len(labels) != 4 {
			t.Errorf("Labels has %d keys, want 4", len(labels))
		}
	})

	t.Run("Labels instance matches tunnel name", func(t *testing.T) {
		labels := Labels("prod")
		if labels["app.kubernetes.io/instance"] != "prod" {
			t.Errorf("instance = %q, want %q", labels["app.kubernetes.io/instance"], "prod")
		}
	})

	t.Run("Selector has 2 keys", func(t *testing.T) {
		sel := Selector("test")
		if len(sel) != 2 {
			t.Errorf("Selector has %d keys, want 2", len(sel))
		}
	})

	t.Run("Selector is subset of Labels", func(t *testing.T) {
		labels := Labels("test")
		sel := Selector("test")
		for k, v := range sel {
			if labels[k] != v {
				t.Errorf("selector key %q = %q, but Labels has %q", k, v, labels[k])
			}
		}
	})
}

func TestGetMetricsPort(t *testing.T) {
	tests := []struct {
		name     string
		tunnel   *cfgatev1alpha1.CloudflareTunnel
		wantPort int32
	}{
		{
			name:     "zero port returns default",
			tunnel:   newDeploymentTestTunnel("test"),
			wantPort: DefaultMetricsPort,
		},
		{
			name: "custom port",
			tunnel: newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
				t.Spec.Cloudflared.Metrics.Port = 9090
			}),
			wantPort: 9090,
		},
		{
			name: "boundary port 1",
			tunnel: newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
				t.Spec.Cloudflared.Metrics.Port = 1
			}),
			wantPort: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getMetricsPort(tt.tunnel)
			if got != tt.wantPort {
				t.Errorf("getMetricsPort() = %d, want %d", got, tt.wantPort)
			}
		})
	}
}

func TestBuildDeploymentExtended(t *testing.T) {
	builder := NewBuilder()

	t.Run("pod annotations applied", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.Cloudflared.PodAnnotations = map[string]string{
				"prometheus.io/scrape": "true",
			}
		})
		deployment := builder.BuildDeployment(tunnel, "token")
		ann := deployment.Spec.Template.Annotations
		if ann["prometheus.io/scrape"] != "true" {
			t.Errorf("annotation = %q, want %q", ann["prometheus.io/scrape"], "true")
		}
	})

	t.Run("empty pod annotations", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		deployment := builder.BuildDeployment(tunnel, "token")
		ann := deployment.Spec.Template.Annotations
		if ann == nil {
			t.Error("Annotations should not be nil")
		}
	})

	t.Run("namespace propagated", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Namespace = "prod"
		})
		deployment := builder.BuildDeployment(tunnel, "token")
		if deployment.Namespace != "prod" {
			t.Errorf("namespace = %q, want %q", deployment.Namespace, "prod")
		}
	})

	t.Run("selector is subset of template labels", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		deployment := builder.BuildDeployment(tunnel, "token")
		templateLabels := deployment.Spec.Template.Labels
		for k, v := range deployment.Spec.Selector.MatchLabels {
			if templateLabels[k] != v {
				t.Errorf("selector key %q = %q not in template labels (has %q)", k, v, templateLabels[k])
			}
		}
	})

	t.Run("container has liveness probe", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		deployment := builder.BuildDeployment(tunnel, "token")
		container := deployment.Spec.Template.Spec.Containers[0]
		if container.LivenessProbe == nil {
			t.Error("container should have liveness probe")
		}
	})

	t.Run("container has readiness probe", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		deployment := builder.BuildDeployment(tunnel, "token")
		container := deployment.Spec.Template.Spec.Containers[0]
		if container.ReadinessProbe == nil {
			t.Error("container should have readiness probe")
		}
	})

	t.Run("token env var name", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		deployment := builder.BuildDeployment(tunnel, "token")
		container := deployment.Spec.Template.Spec.Containers[0]
		if len(container.Env) == 0 {
			t.Fatal("container should have env vars")
		}
		if container.Env[0].Name != TokenEnvVar {
			t.Errorf("env var name = %q, want %q", container.Env[0].Name, TokenEnvVar)
		}
	})

	t.Run("token env var references secret", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		deployment := builder.BuildDeployment(tunnel, "token")
		container := deployment.Spec.Template.Spec.Containers[0]
		ref := container.Env[0].ValueFrom.SecretKeyRef
		if ref == nil {
			t.Fatal("env var should reference a secret")
			return
		}
		if ref.Key != TokenSecretKey {
			t.Errorf("secret key = %q, want %q", ref.Key, TokenSecretKey)
		}
		wantSecretName := TokenSecretName("test")
		if ref.Name != wantSecretName {
			t.Errorf("secret name = %q, want %q", ref.Name, wantSecretName)
		}
	})

	t.Run("default metrics container port", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		deployment := builder.BuildDeployment(tunnel, "token")
		container := deployment.Spec.Template.Spec.Containers[0]
		if len(container.Ports) == 0 {
			t.Fatal("container should have ports")
		}
		if container.Ports[0].ContainerPort != DefaultMetricsPort {
			t.Errorf("port = %d, want %d", container.Ports[0].ContainerPort, DefaultMetricsPort)
		}
	})

	t.Run("custom metrics container port", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.Cloudflared.Metrics.Port = 9090
		})
		deployment := builder.BuildDeployment(tunnel, "token")
		container := deployment.Spec.Template.Spec.Containers[0]
		if container.Ports[0].ContainerPort != 9090 {
			t.Errorf("port = %d, want %d", container.Ports[0].ContainerPort, 9090)
		}
	})

	t.Run("replicas 1", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.Cloudflared.Replicas = 1
		})
		deployment := builder.BuildDeployment(tunnel, "token")
		if *deployment.Spec.Replicas != 1 {
			t.Errorf("Replicas = %d, want 1", *deployment.Spec.Replicas)
		}
	})

	t.Run("no node selector by default", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		deployment := builder.BuildDeployment(tunnel, "token")
		if deployment.Spec.Template.Spec.NodeSelector != nil {
			t.Errorf("NodeSelector should be nil, got %v", deployment.Spec.Template.Spec.NodeSelector)
		}
	})

	t.Run("no tolerations by default", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		deployment := builder.BuildDeployment(tunnel, "token")
		if deployment.Spec.Template.Spec.Tolerations != nil {
			t.Errorf("Tolerations should be nil, got %v", deployment.Spec.Template.Spec.Tolerations)
		}
	})

	t.Run("pod security context", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		deployment := builder.BuildDeployment(tunnel, "token")
		securityContext := deployment.Spec.Template.Spec.SecurityContext
		if securityContext == nil {
			t.Fatal("SecurityContext should not be nil")
			return
		}
		if securityContext.RunAsNonRoot == nil {
			t.Fatal("RunAsNonRoot should not be nil")
		}
		if !*securityContext.RunAsNonRoot {
			t.Error("RunAsNonRoot should be true")
		}
		if securityContext.SeccompProfile == nil {
			t.Fatal("SeccompProfile should not be nil")
		}
		if securityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
			t.Errorf("SeccompProfile.Type = %q, want %q", securityContext.SeccompProfile.Type, corev1.SeccompProfileTypeRuntimeDefault)
		}
	})
}

func TestBuildContainerExtended(t *testing.T) {
	t.Run("custom pull policy Always", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.Cloudflared.ImagePullPolicy = corev1.PullAlways
		})
		container := buildContainer(tunnel, "test-secret")
		if container.ImagePullPolicy != corev1.PullAlways {
			t.Errorf("ImagePullPolicy = %q, want %q", container.ImagePullPolicy, corev1.PullAlways)
		}
	})

	t.Run("custom pull policy Never", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.Cloudflared.ImagePullPolicy = corev1.PullNever
		})
		container := buildContainer(tunnel, "test-secret")
		if container.ImagePullPolicy != corev1.PullNever {
			t.Errorf("ImagePullPolicy = %q, want %q", container.ImagePullPolicy, corev1.PullNever)
		}
	})

	t.Run("custom resources override defaults", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.Cloudflared.Resources = corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("200m"),
					corev1.ResourceMemory: resource.MustParse("256Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("512Mi"),
				},
			}
		})
		container := buildContainer(tunnel, "test-secret")
		cpu := container.Resources.Requests[corev1.ResourceCPU]
		if cpu.Cmp(resource.MustParse("200m")) != 0 {
			t.Errorf("CPU request = %s, want 200m", cpu.String())
		}
	})

	t.Run("only requests set triggers custom path", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.Cloudflared.Resources = corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("200m"),
				},
			}
		})
		container := buildContainer(tunnel, "test-secret")
		if container.Resources.Requests == nil {
			t.Error("Requests should not be nil")
		}
		if container.Resources.Limits != nil {
			t.Error("Limits should be nil when only requests set")
		}
	})

	t.Run("only limits set triggers custom path", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.Cloudflared.Resources = corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("1"),
				},
			}
		})
		container := buildContainer(tunnel, "test-secret")
		if container.Resources.Limits == nil {
			t.Error("Limits should not be nil")
		}
		if container.Resources.Requests != nil {
			t.Error("Requests should be nil when only limits set")
		}
	})

	t.Run("default resource values", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		container := buildContainer(tunnel, "test-secret")
		cpuReq := container.Resources.Requests[corev1.ResourceCPU]
		if cpuReq.Cmp(resource.MustParse("100m")) != 0 {
			t.Errorf("default CPU request = %s, want 100m", cpuReq.String())
		}
		memReq := container.Resources.Requests[corev1.ResourceMemory]
		if memReq.Cmp(resource.MustParse("128Mi")) != 0 {
			t.Errorf("default memory request = %s, want 128Mi", memReq.String())
		}
		cpuLim := container.Resources.Limits[corev1.ResourceCPU]
		if cpuLim.Cmp(resource.MustParse("500m")) != 0 {
			t.Errorf("default CPU limit = %s, want 500m", cpuLim.String())
		}
		memLim := container.Resources.Limits[corev1.ResourceMemory]
		if memLim.Cmp(resource.MustParse("256Mi")) != 0 {
			t.Errorf("default memory limit = %s, want 256Mi", memLim.String())
		}
	})

	t.Run("custom metrics port in container ports", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.Cloudflared.Metrics.Port = 3000
		})
		container := buildContainer(tunnel, "test-secret")
		if len(container.Ports) == 0 {
			t.Fatal("container should have ports")
		}
		if container.Ports[0].ContainerPort != 3000 {
			t.Errorf("port = %d, want 3000", container.Ports[0].ContainerPort)
		}
	})

	t.Run("env var references correct secret name", func(t *testing.T) {
		container := buildContainer(newDeploymentTestTunnel("test"), "my-secret-name")
		ref := container.Env[0].ValueFrom.SecretKeyRef
		if ref.Name != "my-secret-name" {
			t.Errorf("secret ref name = %q, want %q", ref.Name, "my-secret-name")
		}
	})

	t.Run("security context", func(t *testing.T) {
		container := buildContainer(newDeploymentTestTunnel("test"), "test-secret")
		securityContext := container.SecurityContext
		if securityContext == nil {
			t.Fatal("SecurityContext should not be nil")
			return
		}
		if securityContext.AllowPrivilegeEscalation == nil {
			t.Fatal("AllowPrivilegeEscalation should not be nil")
		}
		if *securityContext.AllowPrivilegeEscalation {
			t.Error("AllowPrivilegeEscalation should be false")
		}
		if securityContext.Capabilities == nil {
			t.Fatal("Capabilities should not be nil")
		}
		wantDrop := []corev1.Capability{"ALL"}
		if len(securityContext.Capabilities.Drop) != len(wantDrop) {
			t.Fatalf("Capabilities.Drop = %v, want %v", securityContext.Capabilities.Drop, wantDrop)
		}
		for i, want := range wantDrop {
			if securityContext.Capabilities.Drop[i] != want {
				t.Errorf("Capabilities.Drop[%d] = %q, want %q", i, securityContext.Capabilities.Drop[i], want)
			}
		}
	})
}

func TestBuildProbesExtended(t *testing.T) {
	t.Run("custom port reflected in both probes", func(t *testing.T) {
		liveness, readiness := buildProbes(9090)
		if liveness.HTTPGet.Port.IntVal != 9090 {
			t.Errorf("liveness port = %d, want 9090", liveness.HTTPGet.Port.IntVal)
		}
		if readiness.HTTPGet.Port.IntVal != 9090 {
			t.Errorf("readiness port = %d, want 9090", readiness.HTTPGet.Port.IntVal)
		}
	})

	t.Run("liveness probe timing values", func(t *testing.T) {
		liveness, _ := buildProbes(DefaultMetricsPort)
		if liveness.InitialDelaySeconds != 10 {
			t.Errorf("InitialDelaySeconds = %d, want 10", liveness.InitialDelaySeconds)
		}
		if liveness.PeriodSeconds != 10 {
			t.Errorf("PeriodSeconds = %d, want 10", liveness.PeriodSeconds)
		}
		if liveness.TimeoutSeconds != 5 {
			t.Errorf("TimeoutSeconds = %d, want 5", liveness.TimeoutSeconds)
		}
		if liveness.FailureThreshold != 3 {
			t.Errorf("FailureThreshold = %d, want 3", liveness.FailureThreshold)
		}
	})

	t.Run("readiness probe timing values", func(t *testing.T) {
		_, readiness := buildProbes(DefaultMetricsPort)
		if readiness.InitialDelaySeconds != 5 {
			t.Errorf("InitialDelaySeconds = %d, want 5", readiness.InitialDelaySeconds)
		}
		if readiness.PeriodSeconds != 5 {
			t.Errorf("PeriodSeconds = %d, want 5", readiness.PeriodSeconds)
		}
		if readiness.TimeoutSeconds != 5 {
			t.Errorf("TimeoutSeconds = %d, want 5", readiness.TimeoutSeconds)
		}
		if readiness.FailureThreshold != 3 {
			t.Errorf("FailureThreshold = %d, want 3", readiness.FailureThreshold)
		}
	})
}

func TestBuildArgsExtended(t *testing.T) {
	t.Run("custom metrics port in args", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.Cloudflared.Metrics.Port = 9090
		})
		args := buildArgs(tunnel)
		found := false
		for _, arg := range args {
			if arg == "0.0.0.0:9090" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("args should contain '0.0.0.0:9090', got %v", args)
		}
	})

	t.Run("run and token are last three args", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test")
		args := buildArgs(tunnel)
		n := len(args)
		if n < 3 {
			t.Fatalf("expected at least 3 args, got %d", n)
		}
		if args[n-3] != "run" {
			t.Errorf("args[-3] = %q, want %q", args[n-3], "run")
		}
		if args[n-2] != "--token" {
			t.Errorf("args[-2] = %q, want %q", args[n-2], "--token")
		}
		want := fmt.Sprintf("$(%s)", TokenEnvVar)
		if args[n-1] != want {
			t.Errorf("args[-1] = %q, want %q", args[n-1], want)
		}
	})

	t.Run("extra args appear before run", func(t *testing.T) {
		tunnel := newDeploymentTestTunnel("test", func(t *cfgatev1alpha1.CloudflareTunnel) {
			t.Spec.Cloudflared.ExtraArgs = []string{"--edge-ip-version", "auto"}
		})
		args := buildArgs(tunnel)
		extraIdx := -1
		runIdx := -1
		for i, arg := range args {
			if arg == "--edge-ip-version" {
				extraIdx = i
			}
			if arg == "run" {
				runIdx = i
			}
		}
		if extraIdx == -1 {
			t.Fatal("--edge-ip-version not found in args")
		}
		if runIdx == -1 {
			t.Fatal("run not found in args")
		}
		if extraIdx >= runIdx {
			t.Errorf("extra args (index %d) should appear before run (index %d)", extraIdx, runIdx)
		}
	})
}

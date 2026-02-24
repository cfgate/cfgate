// Package cloudflared provides utilities for managing cloudflared Kubernetes resources.
package cloudflared

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
)

const (
	// DefaultImage is the default cloudflared container image.
	DefaultImage = "cloudflare/cloudflared:latest"

	// DefaultMetricsPort is the default port for cloudflared metrics.
	DefaultMetricsPort = 2000

	// TokenEnvVar is the environment variable name for the tunnel token.
	TokenEnvVar = "TUNNEL_TOKEN"

	// TokenSecretKey is the key in the secret containing the token.
	TokenSecretKey = "token"
)

// Builder creates Kubernetes resources for cloudflared deployments.
type Builder interface {
	// BuildDeployment creates a Deployment for cloudflared.
	// The deployment uses the tunnel token for authentication.
	BuildDeployment(tunnel *cfgatev1alpha1.CloudflareTunnel, token string) *appsv1.Deployment

	// BuildConfigMap creates a ConfigMap for cloudflared configuration.
	// This is used when running with a config file instead of remote config.
	BuildConfigMap(tunnel *cfgatev1alpha1.CloudflareTunnel, config *TunnelConfig) (*corev1.ConfigMap, error)

	// BuildTokenSecret creates a Secret containing the tunnel token.
	BuildTokenSecret(tunnel *cfgatev1alpha1.CloudflareTunnel, token string) *corev1.Secret
}

// DefaultBuilder is the default implementation of Builder.
type DefaultBuilder struct{}

// NewBuilder creates a new DefaultBuilder.
func NewBuilder() *DefaultBuilder {
	return &DefaultBuilder{}
}

// BuildDeployment creates a Deployment for cloudflared.
// The deployment includes:
// - Proper labels for selection
// - Resource limits and requests
// - Liveness and readiness probes
// - Token-based authentication
// - Metrics endpoint configuration
func (b *DefaultBuilder) BuildDeployment(tunnel *cfgatev1alpha1.CloudflareTunnel, token string) *appsv1.Deployment {
	labels := Labels(tunnel.Name)
	selector := Selector(tunnel.Name)
	tokenSecretName := TokenSecretName(tunnel.Name)

	replicas := tunnel.Spec.Cloudflared.Replicas
	if replicas == 0 {
		replicas = 2
	}

	container := buildContainer(tunnel, tokenSecretName)
	liveness, readiness := buildProbes(getMetricsPort(tunnel))

	container.LivenessProbe = liveness
	container.ReadinessProbe = readiness

	// Merge pod annotations from spec
	podAnnotations := map[string]string{}
	for k, v := range tunnel.Spec.Cloudflared.PodAnnotations {
		podAnnotations[k] = v
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DeploymentName(tunnel.Name),
			Namespace: tunnel.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: selector,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: podAnnotations,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{container},
				},
			},
		},
	}

	// Add node selector if specified
	if len(tunnel.Spec.Cloudflared.NodeSelector) > 0 {
		deployment.Spec.Template.Spec.NodeSelector = tunnel.Spec.Cloudflared.NodeSelector
	}

	// Add tolerations if specified
	if len(tunnel.Spec.Cloudflared.Tolerations) > 0 {
		deployment.Spec.Template.Spec.Tolerations = tunnel.Spec.Cloudflared.Tolerations
	}

	return deployment
}

// BuildConfigMap creates a ConfigMap for cloudflared configuration.
// This is used when running with a config file instead of remote config.
func (b *DefaultBuilder) BuildConfigMap(tunnel *cfgatev1alpha1.CloudflareTunnel, config *TunnelConfig) (*corev1.ConfigMap, error) {
	configData, err := config.Marshal()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal tunnel config: %w", err)
	}

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ConfigMapName(tunnel.Name),
			Namespace: tunnel.Namespace,
			Labels:    Labels(tunnel.Name),
		},
		Data: map[string]string{
			"config.yaml": string(configData),
		},
	}, nil
}

// BuildTokenSecret creates a Secret containing the tunnel token.
func (b *DefaultBuilder) BuildTokenSecret(tunnel *cfgatev1alpha1.CloudflareTunnel, token string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TokenSecretName(tunnel.Name),
			Namespace: tunnel.Namespace,
			Labels:    Labels(tunnel.Name),
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			TokenSecretKey: token,
		},
	}
}

// DeploymentName returns the name for the cloudflared Deployment.
func DeploymentName(tunnelName string) string {
	return tunnelName + "-cloudflared"
}

// ConfigMapName returns the name for the cloudflared ConfigMap.
func ConfigMapName(tunnelName string) string {
	return tunnelName + "-cloudflared-config"
}

// TokenSecretName returns the name for the tunnel token Secret.
func TokenSecretName(tunnelName string) string {
	return tunnelName + "-tunnel-token"
}

// Labels returns the standard labels for cloudflared resources.
func Labels(tunnelName string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "cloudflared",
		"app.kubernetes.io/instance":   tunnelName,
		"app.kubernetes.io/component":  "tunnel",
		"app.kubernetes.io/managed-by": "cfgate",
	}
}

// Selector returns the label selector for cloudflared pods.
func Selector(tunnelName string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":     "cloudflared",
		"app.kubernetes.io/instance": tunnelName,
	}
}

// buildContainer creates the cloudflared container spec.
func buildContainer(tunnel *cfgatev1alpha1.CloudflareTunnel, tokenSecretName string) corev1.Container {
	image := tunnel.Spec.Cloudflared.Image
	if image == "" {
		image = DefaultImage
	}

	pullPolicy := tunnel.Spec.Cloudflared.ImagePullPolicy
	if pullPolicy == "" {
		pullPolicy = corev1.PullIfNotPresent
	}

	args := buildArgs(tunnel)
	metricsPort := getMetricsPort(tunnel)

	container := corev1.Container{
		Name:            "cloudflared",
		Image:           image,
		ImagePullPolicy: pullPolicy,
		Args:            args,
		Env: []corev1.EnvVar{
			{
				Name: TokenEnvVar,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: tokenSecretName,
						},
						Key: TokenSecretKey,
					},
				},
			},
		},
		Ports: []corev1.ContainerPort{
			{
				Name:          "metrics",
				ContainerPort: metricsPort,
				Protocol:      corev1.ProtocolTCP,
			},
		},
	}

	// Add resource requirements if specified
	if tunnel.Spec.Cloudflared.Resources.Limits != nil || tunnel.Spec.Cloudflared.Resources.Requests != nil {
		container.Resources = tunnel.Spec.Cloudflared.Resources
	} else {
		// Default resources
		container.Resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		}
	}

	return container
}

// buildProbes creates liveness and readiness probes for cloudflared.
func buildProbes(metricsPort int32) (liveness, readiness *corev1.Probe) {
	liveness = &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/healthcheck",
				Port: intstr.FromInt32(metricsPort),
			},
		},
		InitialDelaySeconds: 10,
		PeriodSeconds:       10,
		TimeoutSeconds:      5,
		FailureThreshold:    3,
	}

	readiness = &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/ready",
				Port: intstr.FromInt32(metricsPort),
			},
		},
		InitialDelaySeconds: 5,
		PeriodSeconds:       5,
		TimeoutSeconds:      5,
		FailureThreshold:    3,
	}

	return liveness, readiness
}

// buildArgs creates the command line arguments for cloudflared.
func buildArgs(tunnel *cfgatev1alpha1.CloudflareTunnel) []string {
	args := []string{
		"tunnel",
		"--no-autoupdate",
	}

	// Add metrics endpoint
	metricsPort := getMetricsPort(tunnel)
	args = append(args, "--metrics", fmt.Sprintf("0.0.0.0:%d", metricsPort))

	// Add protocol if specified
	if tunnel.Spec.Cloudflared.Protocol != "" && tunnel.Spec.Cloudflared.Protocol != "auto" {
		args = append(args, "--protocol", tunnel.Spec.Cloudflared.Protocol)
	}

	// Add extra args
	args = append(args, tunnel.Spec.Cloudflared.ExtraArgs...)

	// Add run command with token from environment
	args = append(args, "run", "--token", fmt.Sprintf("$(%s)", TokenEnvVar))

	return args
}

// getMetricsPort returns the metrics port for the tunnel.
func getMetricsPort(tunnel *cfgatev1alpha1.CloudflareTunnel) int32 {
	if tunnel.Spec.Cloudflared.Metrics.Port > 0 {
		return tunnel.Spec.Cloudflared.Metrics.Port
	}
	return DefaultMetricsPort
}

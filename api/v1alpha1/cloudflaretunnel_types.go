// Package v1alpha1 contains API Schema definitions for the cfgate v1alpha1 API group.
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TunnelIdentity defines the tunnel identification configuration for CloudflareTunnel.
//
// TunnelIdentity uses a single idempotent pathway: the controller resolves the tunnel
// by name and creates it if it does not exist. The resolved tunnel ID is stored in the
// CloudflareTunnelStatus after resolution. This design ensures that multiple CloudflareTunnel
// resources with the same tunnel name will adopt the same Cloudflare tunnel rather than
// creating duplicates.
type TunnelIdentity struct {
	// Name is the tunnel name in Cloudflare. If tunnel with this name exists, adopt it.
	// If not, create it. Tunnel ID is stored in status after resolution/creation.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
}

// CloudflareConfig defines the Cloudflare API credentials configuration for tunnel operations.
//
// CloudflareConfig requires either AccountID or AccountName to identify the Cloudflare account.
// When AccountName is specified, the controller performs an API lookup to resolve the account ID.
// The resolved ID is cached in CloudflareTunnelStatus.AccountID for subsequent reconciliations.
//
// The SecretRef must reference a Kubernetes Secret containing a Cloudflare API token with
// the following permissions:
//   - Account -> Cloudflare Tunnel -> Edit (required)
//   - Account -> Account Settings -> Read (required if using AccountName)
//
// +kubebuilder:validation:XValidation:rule="has(self.accountId) || has(self.accountName)",message="either accountId or accountName must be specified"
// +kubebuilder:validation:XValidation:rule="!has(self.accountId) || self.accountId.matches('^[a-f0-9]{32}$')",message="accountId must be a 32-character hex string"
type CloudflareConfig struct {
	// AccountID is the Cloudflare Account ID.
	// +optional
	// +kubebuilder:validation:MaxLength=32
	AccountID string `json:"accountId,omitempty"`

	// AccountName is the Cloudflare Account name. Will be looked up via API.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	AccountName string `json:"accountName,omitempty"`

	// SecretRef references the Secret containing Cloudflare API credentials.
	// The secret must contain an API token (not tunnel token).
	// +kubebuilder:validation:Required
	SecretRef SecretRef `json:"secretRef"`

	// SecretKeys defines the key mappings within the secret.
	// +optional
	SecretKeys SecretKeys `json:"secretKeys,omitempty"`
}

// SecretRef references a Kubernetes Secret containing Cloudflare API credentials.
//
// SecretRef is used to locate the Secret containing the Cloudflare API token.
// The Namespace field defaults to the referencing resource's namespace if omitted.
type SecretRef struct {
	// Name of the secret.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Namespace of the secret. Defaults to the tunnel's namespace.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace,omitempty"`
}

// SecretReference references a Kubernetes Secret for fallback credentials.
//
// SecretReference is used for fallback credentials that enable cleanup of Cloudflare
// resources when the primary credentials secret has been deleted. This supports
// scenarios where tunnel credentials are managed separately from operator credentials.
type SecretReference struct {
	// Name of the secret.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Namespace of the secret. Defaults to the resource's namespace if empty.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace,omitempty"`
}

// SecretKeys defines the key names for credentials within a Kubernetes Secret.
//
// SecretKeys allows customization of the key names used to retrieve credentials
// from the referenced Secret. The default key name is CLOUDFLARE_API_TOKEN.
type SecretKeys struct {
	// APIToken is the key name for the Cloudflare API token.
	// +kubebuilder:default=CLOUDFLARE_API_TOKEN
	// +kubebuilder:validation:MaxLength=253
	APIToken string `json:"apiToken,omitempty"`
}

// CloudflaredConfig defines the cloudflared daemon deployment configuration.
//
// CloudflaredConfig controls how the cloudflared connector pods are deployed in the cluster.
// The controller creates a Deployment with the specified replicas, image, and resource
// requirements. Each replica establishes an independent connection to Cloudflare's edge
// network for high availability.
type CloudflaredConfig struct {
	// Replicas is the number of cloudflared replicas.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	// +kubebuilder:default=2
	Replicas int32 `json:"replicas,omitempty"`

	// Image is the cloudflared container image.
	// +kubebuilder:default="cloudflare/cloudflared:latest"
	// +kubebuilder:validation:MaxLength=255
	Image string `json:"image,omitempty"`

	// ImagePullPolicy is the pull policy for the cloudflared image.
	// +kubebuilder:validation:Enum=Always;Never;IfNotPresent
	// +kubebuilder:default=IfNotPresent
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// Protocol is the tunnel transport protocol: auto, quic, http2.
	// +kubebuilder:validation:Enum=auto;quic;http2
	// +kubebuilder:default=auto
	Protocol string `json:"protocol,omitempty"`

	// Resources are the resource requirements for cloudflared containers.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// NodeSelector is a selector for nodes to run cloudflared on.
	// +optional
	// +kubebuilder:validation:MaxProperties=50
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations are tolerations for the cloudflared pods.
	// +optional
	// +kubebuilder:validation:MaxItems=20
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// PodAnnotations are annotations to add to cloudflared pods.
	// +optional
	// +kubebuilder:validation:MaxProperties=50
	PodAnnotations map[string]string `json:"podAnnotations,omitempty"`

	// ExtraArgs are additional arguments to pass to cloudflared.
	// +optional
	// +kubebuilder:validation:MaxItems=20
	ExtraArgs []string `json:"extraArgs,omitempty"`

	// Metrics configures the cloudflared metrics endpoint.
	// +optional
	Metrics MetricsConfig `json:"metrics,omitempty"`
}

// MetricsConfig defines the cloudflared metrics endpoint configuration.
//
// MetricsConfig controls the Prometheus-compatible metrics endpoint exposed by cloudflared.
// When enabled, metrics are available at http://localhost:{Port}/metrics on each cloudflared pod.
type MetricsConfig struct {
	// Enabled enables the metrics endpoint.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// Port is the port for the metrics endpoint.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=44483
	Port int32 `json:"port,omitempty"`
}

// OriginDefaults defines default settings for origin (backend) connections.
//
// OriginDefaults configures how cloudflared connects to backend services in the cluster.
// These settings apply to all ingress rules unless overridden by route-specific annotations.
// Use NoTLSVerify with caution in production environments.
//
// +kubebuilder:validation:XValidation:rule="!(self.http2Origin && self.h2cOrigin)",message="http2Origin and h2cOrigin are mutually exclusive"
type OriginDefaults struct {
	// ConnectTimeout is the timeout for connecting to the origin.
	// +kubebuilder:default="30s"
	// +kubebuilder:validation:Pattern=`^[0-9]+(s|m|h)$`
	ConnectTimeout string `json:"connectTimeout,omitempty"`

	// NoTLSVerify disables TLS verification for origin connections.
	// +kubebuilder:default=false
	NoTLSVerify bool `json:"noTLSVerify,omitempty"`

	// HTTP2Origin enables HTTP/2 for origin connections.
	// +kubebuilder:default=false
	HTTP2Origin bool `json:"http2Origin,omitempty"`

	// H2cOrigin enables HTTP/2 cleartext (h2c) for origin connections.
	// Use this for origins that speak HTTP/2 without TLS (e.g., gRPC services).
	// Mutually exclusive with http2Origin (TLS-based HTTP/2).
	// +kubebuilder:default=false
	H2cOrigin bool `json:"h2cOrigin,omitempty"`

	// CAPoolSecretRef references a Secret containing CA certificates for origin verification.
	// +optional
	CAPoolSecretRef *CAPoolSecretRef `json:"caPoolSecretRef,omitempty"`
}

// CAPoolSecretRef references a Kubernetes Secret containing CA certificates for origin verification.
//
// CAPoolSecretRef is used when backend services use TLS certificates signed by a private CA.
// The referenced Secret must contain the CA certificate chain in PEM format.
type CAPoolSecretRef struct {
	// Name of the secret.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Key is the key within the secret data.
	// +kubebuilder:default="ca.crt"
	// +kubebuilder:validation:MaxLength=253
	Key string `json:"key,omitempty"`
}

// CloudflareTunnelSpec defines the desired state of a CloudflareTunnel resource.
//
// CloudflareTunnelSpec configures the tunnel identity, Cloudflare credentials, cloudflared
// deployment settings, and origin connection defaults. The tunnel manages lifecycle only;
// DNS records are managed separately via CloudflareDNS resources.
type CloudflareTunnelSpec struct {
	// Tunnel defines the tunnel identity configuration.
	// +kubebuilder:validation:Required
	Tunnel TunnelIdentity `json:"tunnel"`

	// Cloudflare defines the Cloudflare API credentials.
	// +kubebuilder:validation:Required
	Cloudflare CloudflareConfig `json:"cloudflare"`

	// Cloudflared defines the cloudflared deployment configuration.
	// +optional
	Cloudflared CloudflaredConfig `json:"cloudflared,omitempty"`

	// OriginDefaults defines default settings for origin connections.
	// +optional
	OriginDefaults OriginDefaults `json:"originDefaults,omitempty"`

	// FallbackTarget is the service for unmatched requests.
	// +kubebuilder:default="http_status:404"
	// +kubebuilder:validation:MaxLength=255
	FallbackTarget string `json:"fallbackTarget,omitempty"`

	// FallbackCredentialsRef references a secret containing fallback Cloudflare API credentials.
	// Used during deletion when primary credentials (in Cloudflare.SecretRef) are unavailable.
	// This enables cleanup of Cloudflare resources even if the per-tunnel secret is deleted.
	// The secret must contain the same keys as the primary credentials secret.
	// +optional
	FallbackCredentialsRef *SecretReference `json:"fallbackCredentialsRef,omitempty"`
}

// CloudflareTunnelStatus defines the observed state of a CloudflareTunnel resource.
//
// CloudflareTunnelStatus captures the tunnel's Cloudflare-assigned identifiers, deployment
// status, and reconciliation state. The TunnelDomain field provides the CNAME target
// ({tunnelId}.cfargotunnel.com) that CloudflareDNS uses for DNS record creation.
type CloudflareTunnelStatus struct {
	// TunnelID is the Cloudflare tunnel ID.
	TunnelID string `json:"tunnelId,omitempty"`

	// TunnelName is the Cloudflare tunnel name.
	TunnelName string `json:"tunnelName,omitempty"`

	// TunnelDomain is the tunnel's CNAME target domain (e.g., {tunnelId}.cfargotunnel.com).
	TunnelDomain string `json:"tunnelDomain,omitempty"`

	// AccountID is the resolved Cloudflare account ID.
	AccountID string `json:"accountId,omitempty"`

	// Replicas is the total number of cloudflared replicas.
	Replicas int32 `json:"replicas,omitempty"`

	// ReadyReplicas is the number of ready cloudflared replicas.
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// ObservedGeneration is the generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastSyncTime is the last time the configuration was synced to Cloudflare.
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// ConnectedRouteCount is the number of routes connected to this tunnel.
	ConnectedRouteCount int32 `json:"connectedRouteCount,omitempty"`

	// Conditions represent the latest available observations of the tunnel's state.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// CloudflareTunnel is the Schema for the cloudflaretunnels API.
//
// CloudflareTunnel manages the lifecycle of a Cloudflare Tunnel and its cloudflared daemon
// deployment. It handles tunnel creation or adoption, credential management, and deploys
// cloudflared pods that establish secure connections to Cloudflare's edge network.
//
// CloudflareTunnel follows a composable architecture where tunnel lifecycle is separate from
// DNS management. Use CloudflareDNS with a tunnelRef to create DNS records pointing to this
// tunnel's domain.
//
// Status conditions:
//   - Ready: tunnel is fully operational
//   - CredentialsValid: API credentials have been validated
//   - TunnelCreated: tunnel exists in Cloudflare
//   - ConfigurationSynced: ingress configuration is synced
//   - DeploymentReady: cloudflared pods are running
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=cft;cftunnel
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Tunnel ID",type="string",JSONPath=".status.tunnelId"
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".status.readyReplicas"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type CloudflareTunnel struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CloudflareTunnelSpec   `json:"spec,omitempty"`
	Status CloudflareTunnelStatus `json:"status,omitempty"`
}

// CloudflareTunnelList contains a list of CloudflareTunnel resources.
//
// +kubebuilder:object:root=true
type CloudflareTunnelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareTunnel `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CloudflareTunnel{}, &CloudflareTunnelList{})
}

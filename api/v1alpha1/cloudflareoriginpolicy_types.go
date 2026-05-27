package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// OriginProtocol is the scheme cloudflared uses when connecting to a backend Service.
// +kubebuilder:validation:Enum=http;https
type OriginProtocol string

const (
	// OriginProtocolHTTP connects to backend Services with HTTP.
	OriginProtocolHTTP OriginProtocol = "http"
	// OriginProtocolHTTPS connects to backend Services with HTTPS.
	OriginProtocolHTTPS OriginProtocol = "https"
)

// OriginCAPoolReference selects a named CA pool from the referenced CloudflareTunnel.
type OriginCAPoolReference struct {
	// Name is the spec.originCAPools name on the CloudflareTunnel.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
}

// OriginPolicyTargetReference identifies a Gateway API HTTPRoute for origin policy attachment.
//
// Cross-namespace references require a ReferenceGrant in the target namespace that permits
// CloudflareOriginPolicy resources from the policy namespace.
//
// +kubebuilder:validation:XValidation:rule="self.group == 'gateway.networking.k8s.io'",message="group must be gateway.networking.k8s.io"
// +kubebuilder:validation:XValidation:rule="self.kind == 'HTTPRoute'",message="kind must be HTTPRoute"
type OriginPolicyTargetReference struct {
	// Group is the API group of the target resource.
	// +kubebuilder:default="gateway.networking.k8s.io"
	// +kubebuilder:validation:MaxLength=253
	Group string `json:"group"`

	// Kind is the target kind. CloudflareOriginPolicy targets HTTPRoute resources.
	// +kubebuilder:validation:Enum=HTTPRoute
	// +kubebuilder:validation:MaxLength=63
	Kind string `json:"kind"`

	// Name is the name of the target resource.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Namespace is the namespace of the target resource.
	// Cross-namespace targeting requires ReferenceGrant.
	// +kubebuilder:default=""
	// +kubebuilder:validation:MaxLength=253
	Namespace string `json:"namespace"`

	// SectionName targets a named HTTPRoute rule.
	// +kubebuilder:default=""
	// +kubebuilder:validation:MaxLength=253
	SectionName string `json:"sectionName"`
}

// OriginTLSSettings defines cloudflared TLS settings for origin connections.
type OriginTLSSettings struct {
	// OriginServerName is the hostname cloudflared expects on the origin certificate
	// and uses for SNI when connecting to HTTPS origins.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	OriginServerName string `json:"originServerName,omitempty"`

	// MatchSNIToHost makes cloudflared set SNI to the incoming request hostname.
	// +optional
	MatchSNIToHost bool `json:"matchSNItoHost,omitempty"`

	// NoTLSVerify disables TLS certificate verification for HTTPS origins.
	// +optional
	NoTLSVerify bool `json:"noTLSVerify,omitempty"`

	// TLSTimeout is the timeout for completing the TLS handshake.
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9]+(s|m|h)$`
	TLSTimeout string `json:"tlsTimeout,omitempty"`

	// CAPoolRef selects a named origin CA pool managed by cfgate.
	// +optional
	CAPoolRef *OriginCAPoolReference `json:"caPoolRef,omitempty"`
}

// OriginHTTPSettings defines cloudflared HTTP settings for origin connections.
type OriginHTTPSettings struct {
	// HTTPHostHeader sets the HTTP Host header on requests sent to the backend Service.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	HTTPHostHeader string `json:"httpHostHeader,omitempty"`

	// DisableChunkedEncoding disables chunked transfer encoding to HTTP/1.1 origins.
	// +optional
	DisableChunkedEncoding bool `json:"disableChunkedEncoding,omitempty"`
}

// OriginConnectionSettings defines cloudflared connection settings for origin connections.
type OriginConnectionSettings struct {
	// ConnectTimeout is the timeout for establishing a new TCP connection to the origin.
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9]+(s|m|h)$`
	ConnectTimeout string `json:"connectTimeout,omitempty"`
}

// OriginSettings defines cfgate and cloudflared origin request configuration.
//
// +kubebuilder:validation:XValidation:rule="!(self.http2Origin && self.h2cOrigin)",message="http2Origin and h2cOrigin are mutually exclusive"
type OriginSettings struct {
	// Protocol is the backend Service scheme. Empty defaults to HTTP.
	// +optional
	Protocol OriginProtocol `json:"protocol,omitempty"`

	// ConnectTimeout is the timeout for establishing a new TCP connection to the origin.
	// Deprecated: use connection.connectTimeout.
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9]+(s|m|h)$`
	ConnectTimeout string `json:"connectTimeout,omitempty"`

	// HTTPHostHeader sets the HTTP Host header sent to the backend Service.
	// Deprecated: use http.httpHostHeader.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	HTTPHostHeader string `json:"httpHostHeader,omitempty"`

	// OriginServerName is the SNI and expected certificate hostname for HTTPS origins.
	// Deprecated: use tls.originServerName.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	OriginServerName string `json:"originServerName,omitempty"`

	// NoTLSVerify disables TLS verification for HTTPS origins.
	// Deprecated: use tls.noTLSVerify.
	// +optional
	NoTLSVerify bool `json:"noTLSVerify,omitempty"`

	// HTTP2Origin makes cloudflared connect to HTTPS origins with HTTP/2.
	// +optional
	HTTP2Origin bool `json:"http2Origin,omitempty"`

	// H2cOrigin makes cloudflared connect to cleartext HTTP/2 origins.
	// +optional
	H2cOrigin bool `json:"h2cOrigin,omitempty"`

	// CAPoolRef selects a named origin CA pool managed by cfgate.
	// Deprecated: use tls.caPoolRef.
	// +optional
	CAPoolRef *OriginCAPoolReference `json:"caPoolRef,omitempty"`

	// TLS contains origin TLS settings.
	// +optional
	TLS *OriginTLSSettings `json:"tls,omitempty"`

	// HTTP contains origin HTTP settings.
	// +optional
	HTTP *OriginHTTPSettings `json:"http,omitempty"`

	// Connection contains origin connection settings.
	// +optional
	Connection *OriginConnectionSettings `json:"connection,omitempty"`
}

// CloudflareOriginPolicySpec defines origin behavior for Gateway API targets.
//
// CloudflareOriginPolicy is a cfgate-specific Direct Policy Attachment surface for
// origin settings that do not have a portable Gateway API policy. BackendTLSPolicy
// remains preferred for standard backend TLS validation when it fits.
type CloudflareOriginPolicySpec struct {
	// TargetRefs identifies Gateway API HTTPRoute resources this policy applies to.
	// Cross-namespace targets require a Gateway API ReferenceGrant.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +listType=map
	// +listMapKey=group
	// +listMapKey=kind
	// +listMapKey=name
	// +listMapKey=namespace
	// +listMapKey=sectionName
	TargetRefs []OriginPolicyTargetReference `json:"targetRefs"`

	// Origin defines cloudflared origin behavior for the selected targets.
	// +optional
	Origin OriginSettings `json:"origin,omitempty"`
}

// CloudflareOriginPolicyStatus defines observed origin policy state.
type CloudflareOriginPolicyStatus struct {
	// AttachedTargets is the count of successfully accepted Gateway API targets.
	AttachedTargets int32 `json:"attachedTargets,omitempty"`

	// ObservedGeneration is the last generation processed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Ancestors contains status for each targetRef.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Ancestors []PolicyAncestorStatus `json:"ancestors,omitempty"`

	// Conditions describe current state.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// CloudflareOriginPolicy attaches cfgate-specific origin settings to HTTPRoutes.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=cfop;cforiginpolicy
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Targets",type="integer",JSONPath=".status.attachedTargets"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type CloudflareOriginPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CloudflareOriginPolicySpec   `json:"spec,omitempty"`
	Status CloudflareOriginPolicyStatus `json:"status,omitempty"`
}

// CloudflareOriginPolicyList contains a list of CloudflareOriginPolicy resources.
//
// +kubebuilder:object:root=true
type CloudflareOriginPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareOriginPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CloudflareOriginPolicy{}, &CloudflareOriginPolicyList{})
}

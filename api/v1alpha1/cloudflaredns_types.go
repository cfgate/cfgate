// Package v1alpha1 contains API Schema definitions for the cfgate v1alpha1 API group.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DNSTunnelRef references a CloudflareTunnel resource for DNS CNAME target resolution.
//
// DNSTunnelRef creates a dependency between CloudflareDNS and CloudflareTunnel resources.
// When specified, DNS CNAME records are created pointing to the tunnel's domain
// ({tunnelId}.cfargotunnel.com). The controller waits for the tunnel to be ready before
// creating DNS records.
type DNSTunnelRef struct {
	// Name is the name of the CloudflareTunnel.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// Namespace is the namespace of the CloudflareTunnel.
	// Defaults to the CloudflareDNS's namespace.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace,omitempty"`
}

// RecordType represents DNS record types supported by CloudflareDNS.
//
// RecordType is used in ExternalTarget to specify the DNS record type for non-tunnel targets.
// CNAME is used for tunnel references and external domain targets. A and AAAA are used for
// direct IP address targets.
//
// +kubebuilder:validation:Enum=CNAME;A;AAAA
type RecordType string

const (
	// RecordTypeCNAME represents a CNAME record type for alias records.
	RecordTypeCNAME RecordType = "CNAME"
	// RecordTypeA represents an A record type for IPv4 addresses.
	RecordTypeA RecordType = "A"
	// RecordTypeAAAA represents an AAAA record type for IPv6 addresses.
	RecordTypeAAAA RecordType = "AAAA"
)

// DNSPolicy defines the DNS record lifecycle policy.
//
// DNSPolicy controls how the controller manages DNS records throughout their lifecycle.
// This is aligned with external-dns patterns for compatibility with existing workflows.
//
// +kubebuilder:validation:Enum=sync;upsert-only;create-only
type DNSPolicy string

const (
	// DNSPolicySync creates, updates, and deletes DNS records to match desired state.
	// This is the default policy providing full lifecycle management.
	DNSPolicySync DNSPolicy = "sync"
	// DNSPolicyUpsertOnly creates new records and updates existing ones but never deletes.
	// Use this policy to prevent accidental deletion of DNS records.
	DNSPolicyUpsertOnly DNSPolicy = "upsert-only"
	// DNSPolicyCreateOnly creates new records but never updates or deletes them.
	// Use this policy for records that should be immutable after creation.
	DNSPolicyCreateOnly DNSPolicy = "create-only"
)

// ExternalTarget defines a non-tunnel DNS target for external CNAME, A, or AAAA records.
//
// ExternalTarget enables CloudflareDNS to manage DNS records that point to external resources
// rather than Cloudflare tunnels. This supports use cases like pointing to external load
// balancers, CDN origins, or third-party services.
type ExternalTarget struct {
	// Type is the DNS record type.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=CNAME;A;AAAA
	Type RecordType `json:"type"`

	// Value is the target value (domain for CNAME, IP for A/AAAA).
	// Max 253: RFC 1035 section 2.3.4 FQDN presentation-format limit.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Value string `json:"value"`
}

// DNSZoneConfig defines a DNS zone where records will be managed.
//
// DNSZoneConfig identifies a Cloudflare DNS zone either by name (requiring API lookup)
// or by explicit zone ID. The optional Proxied field sets the default proxy behavior
// for all records in this zone.
type DNSZoneConfig struct {
	// Name is the zone domain name (e.g., example.com).
	// Max 253: RFC 1035 section 2.3.4 FQDN presentation-format limit.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self.split('.').all(s, size(s) <= 63)",message="each DNS label must not exceed 63 octets (RFC 1035 section 2.3.4)"
	Name string `json:"name"`

	// ID is the optional explicit zone ID (skips API lookup).
	// +optional
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{32}$`
	ID string `json:"id,omitempty"`

	// Proxied sets the default proxied setting for this zone.
	// nil inherits from spec.defaults.proxied.
	// +optional
	Proxied *bool `json:"proxied,omitempty"`
}

// DNSNamespaceSelector limits Gateway API route discovery to specific namespaces.
//
// DNSNamespaceSelector filters which namespaces the controller watches for HTTPRoute and
// other Gateway API route resources. This enables multi-tenant scenarios where different
// CloudflareDNS resources manage routes from different namespaces.
//
// +kubebuilder:validation:XValidation:rule="has(self.matchLabels) || has(self.matchNames)",message="at least one selector must be specified"
type DNSNamespaceSelector struct {
	// MatchLabels selects namespaces with matching labels.
	// +optional
	// +kubebuilder:validation:MaxProperties=10
	MatchLabels map[string]string `json:"matchLabels,omitempty"`

	// MatchNames selects namespaces by name.
	// +optional
	// +kubebuilder:validation:MaxItems=50
	MatchNames []string `json:"matchNames,omitempty"`
}

// DNSGatewayRoutesSource configures automatic hostname discovery from Gateway API routes.
//
// DNSGatewayRoutesSource enables the controller to watch HTTPRoute resources to
// automatically discover hostnames that need DNS records.
// Routes can be filtered by annotation and namespace to control which routes trigger DNS sync.
type DNSGatewayRoutesSource struct {
	// Enabled enables watching Gateway API routes.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// AnnotationFilter only syncs routes with this annotation.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	AnnotationFilter string `json:"annotationFilter,omitempty"`

	// NamespaceSelector limits route discovery to specific namespaces.
	// +optional
	NamespaceSelector *DNSNamespaceSelector `json:"namespaceSelector,omitempty"`
}

// DNSExplicitHostname defines an explicit hostname to sync with optional per-hostname configuration.
//
// DNSExplicitHostname provides direct specification of DNS hostnames without depending on
// Gateway API route discovery. The Target field supports the {{ .TunnelDomain }} template
// variable for dynamic resolution when using tunnelRef.
//
// +kubebuilder:validation:XValidation:rule="!has(self.ttl) || self.ttl == 1 || (self.ttl >= 60 && self.ttl <= 86400)",message="TTL must be 1 (auto) or between 60 and 86400 seconds"
type DNSExplicitHostname struct {
	// Hostname is the DNS hostname to create.
	// Max 253: RFC 1035 section 2.3.4 FQDN presentation-format limit.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self.split('.').all(s, size(s) <= 63)",message="each DNS label must not exceed 63 octets (RFC 1035 section 2.3.4)"
	Hostname string `json:"hostname"`

	// Target overrides the resolved record target for this hostname.
	// Supports template variable {{ .TunnelDomain }} when tunnelRef is used.
	// Defaults to the resource-level resolved target when omitted.
	// Max 253: RFC 1035 section 2.3.4 FQDN presentation-format limit.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Target string `json:"target,omitempty"`

	// Proxied enables Cloudflare proxy for this record.
	// nil inherits from zone or defaults.
	// +optional
	Proxied *bool `json:"proxied,omitempty"`

	// TTL is the DNS record TTL in seconds. 1 means auto (Cloudflare managed).
	// Valid values: 1 (auto) or 60-86400 (explicit).
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=86400
	// +kubebuilder:default=1
	TTL int32 `json:"ttl,omitempty"`
}

// DNSHostnameSource defines the sources from which hostnames are collected for DNS sync.
//
// DNSHostnameSource supports two complementary sources: automatic discovery from Gateway API
// routes and explicit hostname definitions. Both can be used together; explicit hostnames
// take precedence over route-discovered hostnames when there are conflicts.
type DNSHostnameSource struct {
	// GatewayRoutes configures watching Gateway API routes.
	// +optional
	GatewayRoutes *DNSGatewayRoutesSource `json:"gatewayRoutes,omitempty"`

	// Explicit defines explicit hostnames to sync.
	// +optional
	// +kubebuilder:validation:MaxItems=100
	Explicit []DNSExplicitHostname `json:"explicit,omitempty"`
}

// DNSRecordDefaults defines default settings applied to all DNS records.
//
// DNSRecordDefaults provides fallback values for records that do not specify explicit
// settings. Per-hostname and per-zone settings take precedence over these defaults.
// A TTL of 1 indicates "auto" which lets Cloudflare manage the TTL (typically 300s).
//
// +kubebuilder:validation:XValidation:rule="!has(self.ttl) || self.ttl == 1 || (self.ttl >= 60 && self.ttl <= 86400)",message="TTL must be 1 (auto) or between 60 and 86400 seconds"
type DNSRecordDefaults struct {
	// Proxied enables Cloudflare proxy by default.
	// +kubebuilder:default=true
	Proxied bool `json:"proxied,omitempty"`

	// TTL is the default DNS record TTL in seconds.
	// Valid values: 1 (auto) or 60-86400 (explicit).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=86400
	// +kubebuilder:default=1
	TTL int32 `json:"ttl,omitempty"`
}

// DNSTXTRecordOwnership configures TXT record-based ownership tracking for DNS records.
//
// DNSTXTRecordOwnership creates companion TXT records that identify which cfgate installation
// owns each DNS record. This enables safe multi-cluster deployments and prevents accidental
// deletion of records created by other installations. The format aligns with external-dns:
// heritage=cfgate,cfgate/owner=<owner-id>,cfgate/resource=cloudflaredns/<namespace>/<name>
type DNSTXTRecordOwnership struct {
	// Enabled enables TXT record ownership tracking.
	// nil defaults to true.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Prefix is the prefix for TXT record names.
	// +kubebuilder:default="_cfgate"
	// +kubebuilder:validation:MaxLength=63
	Prefix string `json:"prefix,omitempty"`
}

// DNSCommentOwnership configures comment-based ownership tracking in DNS records.
//
// Deprecated: since v0.1.0-alpha.13. The controller ignores both fields and always
// writes a hardcoded "managed by cfgate" comment on managed DNS records. These fields
// will be removed in a future cleanup release. No migration is needed; the hardcoded behavior
// is identical to the previous default values. Remove the spec.ownership.comment section
// from your CloudflareDNS resources to silence future validation warnings.
type DNSCommentOwnership struct {
	// Enabled enables comment-based ownership tracking.
	//
	// Deprecated: since v0.1.0-alpha.13. This field is ignored. The controller always
	// writes a "managed by cfgate" comment. Will be removed in a future cleanup release.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`

	// Template is the comment template.
	//
	// Deprecated: since v0.1.0-alpha.13. This field is ignored. The controller always
	// uses "managed by cfgate" as the comment. Will be removed in a future cleanup release.
	// +kubebuilder:default="managed by cfgate"
	// +kubebuilder:validation:MaxLength=255
	Template string `json:"template,omitempty"`
}

// DNSOwnershipConfig defines how record ownership is tracked and verified.
//
// DNSOwnershipConfig supports two ownership strategies: TXT records and comments.
// TXT record ownership is recommended for production use as it provides reliable
// multi-cluster support. The OwnerID identifies this installation and defaults to
// the CloudflareDNS resource's namespace/name.
type DNSOwnershipConfig struct {
	// OwnerID is the cluster/installation identifier used in TXT ownership records.
	// Used to distinguish records created by different cfgate installations.
	// Defaults to the CloudflareDNS resource's namespace/name if not specified.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(/[a-z0-9]([-a-z0-9]*[a-z0-9])?)?$`
	OwnerID string `json:"ownerId,omitempty"`

	// TXTRecord configures TXT record-based ownership.
	// +optional
	TXTRecord DNSTXTRecordOwnership `json:"txtRecord,omitempty"`

	// Comment configures comment-based ownership.
	//
	// Deprecated: since v0.1.0-alpha.13. All fields are ignored. Will be removed in a future cleanup release.
	// +optional
	Comment DNSCommentOwnership `json:"comment,omitempty"`
}

// DNSCleanupPolicy defines cleanup behavior when records are no longer needed.
//
// DNSCleanupPolicy controls what happens to DNS records when source routes are deleted
// or when the CloudflareDNS resource itself is deleted. All fields use pointer booleans
// to distinguish between "not set" (nil, defaults to true) and "explicitly false".
type DNSCleanupPolicy struct {
	// DeleteOnRouteRemoval deletes records when the source route is deleted.
	// nil defaults to true.
	// +optional
	DeleteOnRouteRemoval *bool `json:"deleteOnRouteRemoval,omitempty"`

	// DeleteOnResourceRemoval deletes records when CloudflareDNS resource is deleted.
	// nil defaults to true.
	// +optional
	DeleteOnResourceRemoval *bool `json:"deleteOnResourceRemoval,omitempty"`

	// OnlyManaged only deletes records that were created by cfgate (verified via ownership).
	// nil defaults to true.
	// +optional
	OnlyManaged *bool `json:"onlyManaged,omitempty"`
}

// CloudflareDNSSpec defines the desired state of a CloudflareDNS resource.
//
// CloudflareDNSSpec configures DNS record synchronization, including the target
// (tunnel or external), zones to manage, hostname sources, and ownership tracking.
// Either tunnelRef or externalTarget must be specified (mutually exclusive).
//
// +kubebuilder:validation:XValidation:rule="has(self.tunnelRef) || has(self.externalTarget)",message="either tunnelRef or externalTarget must be specified"
// +kubebuilder:validation:XValidation:rule="!(has(self.tunnelRef) && has(self.externalTarget))",message="tunnelRef and externalTarget are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="has(self.tunnelRef) || has(self.cloudflare)",message="cloudflare credentials required when using externalTarget"
type CloudflareDNSSpec struct {
	// TunnelRef references a CloudflareTunnel for CNAME target resolution.
	// +optional
	TunnelRef *DNSTunnelRef `json:"tunnelRef,omitempty"`

	// ExternalTarget specifies a non-tunnel DNS target.
	// +optional
	ExternalTarget *ExternalTarget `json:"externalTarget,omitempty"`

	// Zones defines the DNS zones to manage.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=10
	Zones []DNSZoneConfig `json:"zones"`

	// Policy controls DNS record lifecycle.
	// +kubebuilder:validation:Enum=sync;upsert-only;create-only
	// +kubebuilder:default=sync
	Policy DNSPolicy `json:"policy,omitempty"`

	// Source defines where to get hostnames to sync.
	// +optional
	Source DNSHostnameSource `json:"source,omitempty"`

	// Defaults defines default settings for DNS records.
	// +optional
	Defaults DNSRecordDefaults `json:"defaults,omitempty"`

	// Ownership defines how to track record ownership.
	// +optional
	Ownership DNSOwnershipConfig `json:"ownership,omitempty"`

	// CleanupPolicy defines cleanup behavior for records.
	// +optional
	CleanupPolicy DNSCleanupPolicy `json:"cleanupPolicy,omitempty"`

	// Cloudflare API credentials (required when using externalTarget).
	// When using tunnelRef, credentials are inherited from the tunnel.
	// +optional
	Cloudflare *CloudflareConfig `json:"cloudflare,omitempty"`

	// FallbackCredentialsRef references fallback Cloudflare API credentials.
	// Used during deletion when primary credentials are unavailable.
	// +optional
	FallbackCredentialsRef *SecretReference `json:"fallbackCredentialsRef,omitempty"`
}

// DNSRecordSyncStatus represents the synchronization status of a single DNS record.
//
// DNSRecordSyncStatus tracks individual DNS record state including the Cloudflare record ID,
// current configuration, and sync status. The Status field indicates: Synced (successfully
// synchronized), Pending (awaiting sync), or Failed (sync failed, see Error field).
type DNSRecordSyncStatus struct {
	// Hostname is the DNS hostname.
	Hostname string `json:"hostname"`

	// Type is the DNS record type (CNAME, A, AAAA).
	Type string `json:"type"`

	// Target is the record target/content.
	Target string `json:"target"`

	// Proxied indicates if Cloudflare proxy is enabled.
	Proxied bool `json:"proxied"`

	// TTL is the record TTL.
	TTL int32 `json:"ttl,omitempty"`

	// Status is the sync status: Synced, Pending, Failed.
	Status string `json:"status"`

	// RecordID is the Cloudflare record ID.
	// +optional
	RecordID string `json:"recordId,omitempty"`

	// ZoneID is the Cloudflare zone ID where the record was created.
	// +optional
	ZoneID string `json:"zoneId,omitempty"`

	// Error contains the error message if status is Failed.
	// +optional
	Error string `json:"error,omitempty"`
}

// CloudflareDNSStatus defines the observed state of a CloudflareDNS resource.
//
// CloudflareDNSStatus captures the synchronization state of all DNS records, including
// counts of synced, pending, and failed records. The ResolvedTarget field shows the
// actual CNAME target being used (either from tunnel or external target).
type CloudflareDNSStatus struct {
	// SyncedRecords is the number of successfully synced records.
	SyncedRecords int32 `json:"syncedRecords,omitempty"`

	// PendingRecords is the number of records pending sync.
	PendingRecords int32 `json:"pendingRecords,omitempty"`

	// FailedRecords is the number of records that failed to sync.
	FailedRecords int32 `json:"failedRecords,omitempty"`

	// Records contains the status of individual DNS records.
	// +optional
	// +kubebuilder:validation:MaxItems=1000
	Records []DNSRecordSyncStatus `json:"records,omitempty"`

	// ResolvedTarget is the resolved CNAME target (tunnel domain or external value).
	// +optional
	ResolvedTarget string `json:"resolvedTarget,omitempty"`

	// ObservedGeneration is the generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastSyncTime is the last time records were synced.
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// Conditions represent the latest available observations.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// CloudflareDNS is the Schema for the cloudflarednses API.
//
// CloudflareDNS manages DNS record synchronization independently from CloudflareTunnel resources.
// It supports two target modes: tunnel references (for tunnel-based CNAME records) and external
// targets (for non-tunnel DNS management). DNS records can be sourced from Gateway API routes
// or explicitly defined.
//
// CloudflareDNS implements ownership tracking via TXT records (aligned with external-dns patterns)
// to enable safe multi-cluster deployments and prevent accidental deletion of records created
// by other installations.
//
// Status conditions:
//   - Ready: DNS sync is fully operational
//   - CredentialsValid: Cloudflare credentials have been validated
//   - TargetResolved: Tunnel reference or external target has been resolved
//   - ZonesDiscovered: All configured zones have been discovered via API
//   - DNSSynced: DNS records have been synchronized to Cloudflare
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=cfdns;dns
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Synced",type="integer",JSONPath=".status.syncedRecords"
// +kubebuilder:printcolumn:name="Pending",type="integer",JSONPath=".status.pendingRecords"
// +kubebuilder:printcolumn:name="Failed",type="integer",JSONPath=".status.failedRecords"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type CloudflareDNS struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CloudflareDNSSpec   `json:"spec,omitempty"`
	Status CloudflareDNSStatus `json:"status,omitempty"`
}

// CloudflareDNSList contains a list of CloudflareDNS resources.
//
// +kubebuilder:object:root=true
type CloudflareDNSList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareDNS `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CloudflareDNS{}, &CloudflareDNSList{})
}

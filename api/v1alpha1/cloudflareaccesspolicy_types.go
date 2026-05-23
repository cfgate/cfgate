// Package v1alpha1 contains API Schema definitions for the cfgate v1alpha1 API group.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PolicyTargetReference identifies a Gateway API resource for Access application attachment.
//
// PolicyTargetReference follows the Gateway API LocalPolicyTargetReferenceWithSectionName
// pattern. It targets Gateway API Gateway and HTTPRoute resources and extracts
// hostnames and paths from those resources to create corresponding Cloudflare Access
// applications.
//
// Cross-namespace references require a ReferenceGrant in the target namespace that permits
// CloudflareAccessApplication resources from the application's namespace.
//
// +kubebuilder:validation:XValidation:rule="self.group == 'gateway.networking.k8s.io'",message="group must be gateway.networking.k8s.io"
// +kubebuilder:validation:XValidation:rule="self.kind in ['Gateway', 'HTTPRoute']",message="kind must be Gateway or HTTPRoute"
type PolicyTargetReference struct {
	// Group is the API group of the target resource.
	// +kubebuilder:default="gateway.networking.k8s.io"
	// +kubebuilder:validation:MaxLength=253
	Group string `json:"group"`

	// Kind is the kind of the target resource.
	// +kubebuilder:validation:Enum=Gateway;HTTPRoute
	// +kubebuilder:validation:MaxLength=63
	Kind string `json:"kind"`

	// Name is the name of the target resource.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Namespace is the namespace of the target resource.
	// Cross-namespace targeting requires ReferenceGrant.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Namespace *string `json:"namespace,omitempty"`

	// SectionName targets specific listener (Gateway) or rule (Route).
	// +optional
	// +kubebuilder:validation:MaxLength=253
	SectionName *string `json:"sectionName,omitempty"`
}

// CloudflareSecretRef references Cloudflare credentials for Access API operations.
//
// CloudflareSecretRef identifies the Secret containing Cloudflare API credentials.
// CloudflareAccessPolicy requires this reference explicitly. CloudflareAccessApplication
// may omit it and inherit credentials from the associated CloudflareTunnel via the
// target Gateway's tunnel binding.
type CloudflareSecretRef struct {
	// Name of the secret containing credentials.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Namespace of the secret (defaults to policy namespace).
	// +optional
	Namespace *string `json:"namespace,omitempty"`

	// AccountID is the Cloudflare account ID.
	// +optional
	// +kubebuilder:validation:MaxLength=32
	AccountID string `json:"accountId,omitempty"`

	// AccountName is the Cloudflare account name (looked up via API).
	// +optional
	// +kubebuilder:validation:MaxLength=255
	AccountName string `json:"accountName,omitempty"`
}

// CORSAllowedMethod is an HTTP method allowed for CORS requests.
// +kubebuilder:validation:Enum=GET;POST;HEAD;PUT;DELETE;CONNECT;OPTIONS;TRACE;PATCH
type CORSAllowedMethod string

// CORSHeaders configures Cross-Origin Resource Sharing (CORS) for the Access Application.
// When set, Cloudflare responds to OPTIONS preflight requests on behalf of the origin.
// Mutually exclusive with optionsPreflightBypass on the parent AccessApplication.
type CORSHeaders struct {
	// AllowAllHeaders allows all HTTP request headers.
	// +optional
	AllowAllHeaders bool `json:"allowAllHeaders,omitempty"`

	// AllowAllMethods allows all HTTP request methods.
	// +optional
	AllowAllMethods bool `json:"allowAllMethods,omitempty"`

	// AllowAllOrigins allows all origins.
	// +optional
	AllowAllOrigins bool `json:"allowAllOrigins,omitempty"`

	// AllowCredentials includes credentials (cookies, authorization headers,
	// or TLS client certificates) with CORS requests.
	// +optional
	AllowCredentials bool `json:"allowCredentials,omitempty"`

	// AllowedHeaders lists specific allowed HTTP request headers.
	// Ignored when allowAllHeaders is true.
	// +optional
	// +kubebuilder:validation:MaxItems=50
	AllowedHeaders []string `json:"allowedHeaders,omitempty"`

	// AllowedMethods lists specific allowed HTTP request methods.
	// Ignored when allowAllMethods is true.
	// +optional
	// +kubebuilder:validation:MaxItems=9
	AllowedMethods []CORSAllowedMethod `json:"allowedMethods,omitempty"`

	// AllowedOrigins lists specific allowed origins.
	// Ignored when allowAllOrigins is true.
	// +optional
	// +kubebuilder:validation:MaxItems=50
	AllowedOrigins []string `json:"allowedOrigins,omitempty"`

	// MaxAge is the maximum number of seconds preflight results can be cached.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=86400
	MaxAge *int `json:"maxAge,omitempty"`
}

// AccessApplication defines Cloudflare Access Application configuration.
//
// AccessApplication configures the Cloudflare Access Application that protects the target
// hostnames. The application appears in the Cloudflare dashboard and controls session
// management, cookie settings, and denial behavior.
//
// +kubebuilder:validation:XValidation:rule="!(has(self.corsHeaders) && has(self.optionsPreflightBypass) && self.optionsPreflightBypass)",message="corsHeaders and optionsPreflightBypass are mutually exclusive"
type AccessApplication struct {
	// Name is the display name in Cloudflare dashboard.
	// Defaults to CR name if omitted.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	Name string `json:"name,omitempty"`

	// Domain is the protected domain (auto-generated from routes if omitted).
	// Max 253: RFC 1035 section 2.3.4 FQDN presentation-format limit.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self == '' || self.split('.').all(s, size(s) <= 63)",message="each DNS label must not exceed 63 octets (RFC 1035 section 2.3.4)"
	Domain string `json:"domain,omitempty"`

	// Path restricts protection to a specific absolute path prefix.
	// Cloudflare Access paths must not include query strings or fragments.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:Pattern=`^/[^?#]*$`
	Path string `json:"path,omitempty"`

	// SessionDuration controls session cookie lifetime.
	// +optional
	// +kubebuilder:default="24h"
	// +kubebuilder:validation:Pattern=`^([0-9]+(ns|us|ms|s|m|h))+$`
	SessionDuration string `json:"sessionDuration,omitempty"`

	// Type is the application type.
	// +kubebuilder:validation:Enum=self_hosted
	// +kubebuilder:default=self_hosted
	Type string `json:"type,omitempty"`

	// LogoURL is the application logo in dashboard.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	LogoURL string `json:"logoUrl,omitempty"`

	// SkipInterstitial bypasses the Access login page for API requests.
	// +optional
	// +kubebuilder:default=false
	SkipInterstitial bool `json:"skipInterstitial,omitempty"`

	// EnableBindingCookie enables binding cookies for sticky sessions.
	// +optional
	// +kubebuilder:default=false
	EnableBindingCookie bool `json:"enableBindingCookie,omitempty"`

	// HttpOnlyCookieAttribute adds HttpOnly to session cookies.
	// +optional
	// +kubebuilder:default=true
	HttpOnlyCookieAttribute bool `json:"httpOnlyCookieAttribute,omitempty"`

	// SameSiteCookieAttribute controls cross-site cookie behavior.
	// +kubebuilder:validation:Enum=strict;lax;none
	// +kubebuilder:default=lax
	SameSiteCookieAttribute string `json:"sameSiteCookieAttribute,omitempty"`

	// CustomDenyMessage shown when access is denied.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	CustomDenyMessage string `json:"customDenyMessage,omitempty"`

	// CustomDenyURL redirects to this URL when denied (instead of message).
	// +optional
	CustomDenyURL string `json:"customDenyUrl,omitempty"`

	// AllowedIdps restricts which identity providers can authenticate.
	// Values are Cloudflare Identity Provider UUIDs.
	// When empty, all IdPs configured in the account are allowed.
	// +optional
	// +kubebuilder:validation:MaxItems=25
	AllowedIdps []string `json:"allowedIdps,omitempty"`

	// AutoRedirectToIdentity auto-redirects to the identity provider
	// when a single IdP is configured in allowedIdps. Skips the IdP
	// selection page.
	// +optional
	AutoRedirectToIdentity bool `json:"autoRedirectToIdentity,omitempty"`

	// AppLauncherVisible controls whether the application appears in the
	// Cloudflare App Launcher dashboard. Use pointer to distinguish
	// explicit false (hidden) from absent (default visible).
	// +optional
	// +kubebuilder:default=true
	AppLauncherVisible *bool `json:"appLauncherVisible,omitempty"`

	// CORSHeaders configures CORS for browser-based APIs behind Access.
	// When set, Cloudflare responds to OPTIONS preflight on behalf of the origin.
	// Mutually exclusive with optionsPreflightBypass.
	// +optional
	CORSHeaders *CORSHeaders `json:"corsHeaders,omitempty"`

	// OptionsPreflightBypass allows OPTIONS preflight requests to bypass
	// Access authentication and go directly to the origin. Enabling this
	// removes all CORS header settings. Mutually exclusive with corsHeaders.
	// +optional
	OptionsPreflightBypass bool `json:"optionsPreflightBypass,omitempty"`

	// PathCookieAttribute scopes the Access JWT cookie to the application
	// path instead of the hostname. When enabled, users must re-authenticate
	// for different paths on the same hostname.
	// +optional
	PathCookieAttribute bool `json:"pathCookieAttribute,omitempty"`

	// ServiceAuth401Redirect returns a 401 status code instead of
	// redirecting to the Access login page when a request is blocked by a
	// Service Auth (non_identity) policy. Enable for API consumers.
	// +optional
	ServiceAuth401Redirect bool `json:"serviceAuth401Redirect,omitempty"`

	// CustomNonIdentityDenyURL is the URL users are redirected to when
	// denied by a non-identity (service auth) policy. Separate from
	// customDenyUrl which handles identity-based denials.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	CustomNonIdentityDenyURL string `json:"customNonIdentityDenyUrl,omitempty"`

	// ReadServiceTokensFromHeader enables reading service tokens from a
	// single custom HTTP header instead of the standard CF-Access-Client-Id
	// and CF-Access-Client-Secret header pair. The value is the header name.
	// The header value must contain a JSON object with "cf-access-client-id"
	// and "cf-access-client-secret" keys.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	ReadServiceTokensFromHeader string `json:"readServiceTokensFromHeader,omitempty"`
}

// AccessRule defines identity matching criteria for Access policies.
//
// AccessRule specifies conditions that identify users or services. Rules are organized
// into implementation tiers based on IdP requirements:
//   - P0 (no IdP): IP, IPList, Country, Everyone, ServiceToken, AnyValidServiceToken
//   - P1 (basic IdP): Email, EmailList, EmailDomain, OIDCClaim
//   - P2 (Google Workspace): GSuiteGroup
//   - P3 (not in current product scope): Certificate, CommonName, Group, GitHub, Azure, Okta, SAML, etc.
//
// SDK types map directly to cloudflare-go v6 SDK: IPRule, IPListRule, CountryRule,
// EveryoneRule, ServiceTokenRule, AnyValidServiceTokenRule, EmailRule, DomainRule,
// EmailListRule, AccessOIDCClaimRule, GSuiteGroupRule.
//
// +kubebuilder:validation:XValidation:rule="[has(self.ip), has(self.ipList), has(self.country), has(self.everyone), has(self.serviceToken), has(self.anyValidServiceToken), has(self.email), has(self.emailList), has(self.emailDomain), has(self.oidcClaim), has(self.gsuiteGroup), has(self.group)].filter(x, x).size() == 1",message="exactly one rule type must be specified"
type AccessRule struct {
	// ============================================================
	// P0: No IdP Required
	// ============================================================

	// IP matches source IP CIDR ranges.
	// SDK: IPRule
	// +optional
	IP *AccessIPRule `json:"ip,omitempty"`

	// IPList references a Cloudflare IP List.
	// SDK: IPListRule
	// +optional
	IPList *AccessIPListRule `json:"ipList,omitempty"`

	// Country matches source country codes (ISO 3166-1 alpha-2).
	// SDK: CountryRule
	// +optional
	Country *AccessCountryRule `json:"country,omitempty"`

	// Everyone matches all users (use with caution).
	// SDK: EveryoneRule
	// +optional
	Everyone *bool `json:"everyone,omitempty"`

	// ServiceToken matches a specific service token by ID.
	// SDK: ServiceTokenRule
	// +optional
	ServiceToken *AccessServiceTokenRule `json:"serviceToken,omitempty"`

	// AnyValidServiceToken matches any valid service token.
	// SDK: AnyValidServiceTokenRule
	// +optional
	AnyValidServiceToken *bool `json:"anyValidServiceToken,omitempty"`

	// ============================================================
	// P1: Basic IdP Required (Google Workspace)
	// ============================================================

	// Email matches specific email addresses.
	// SDK: EmailRule
	// +optional
	Email *AccessEmailRule `json:"email,omitempty"`

	// EmailList references a Cloudflare Access email list.
	// SDK: EmailListRule
	// +optional
	EmailList *AccessEmailListRule `json:"emailList,omitempty"`

	// EmailDomain matches email domain suffix.
	// SDK: DomainRule
	// +optional
	EmailDomain *AccessEmailDomainRule `json:"emailDomain,omitempty"`

	// OIDCClaim matches OIDC token claims.
	// SDK: AccessOIDCClaimRule
	// +optional
	OIDCClaim *AccessOIDCClaimRule `json:"oidcClaim,omitempty"`

	// ============================================================
	// P2: Google Workspace Groups
	// ============================================================

	// GSuiteGroup matches Google Workspace groups.
	// SDK: GSuiteGroupRule
	// +optional
	GSuiteGroup *AccessGSuiteGroupRule `json:"gsuiteGroup,omitempty"`

	// Group matches a Cloudflare Access Group by ID.
	// SDK: GroupRule
	// +optional
	Group *AccessGroupRule `json:"group,omitempty"`

	// ============================================================
	// P3: Not in current product scope.
	// ============================================================
	// The following rule types are not part of the current CRD surface:
	// - Certificate (CertificateRule) - client certificate matching
	// - CommonName (AccessCommonNameRule) - certificate common-name matching
	// - GitHub (GitHubOrganizationRule) - GitHub org/team
	// - Azure (AzureGroupRule) - Azure AD groups
	// - Okta (OktaGroupRule) - Okta groups
	// - SAML (SAMLGroupRule) - SAML attributes
	// - AuthenticationMethod (AuthenticationMethodRule) - MFA enforcement
	// - DevicePosture (AccessDevicePostureRule) - Device compliance
	// - ExternalEvaluation (ExternalEvaluationRule) - External eval
	// - LoginMethod (AccessLoginMethodRule) - Login method
}

// AccessIPRule matches source IP CIDR ranges (P0 - no IdP required).
//
// AccessIPRule is used to allow or deny access based on the client's IP address.
// Both IPv4 and IPv6 CIDR notation are supported. Maps to cloudflare-go IPRule.
type AccessIPRule struct {
	// Ranges are CIDR blocks (IPv4 or IPv6).
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=50
	Ranges []string `json:"ranges"`
}

// AccessIPListRule references a Cloudflare IP List (P0 - no IdP required).
//
// AccessIPListRule references a managed IP list in Cloudflare by ID. Name-based
// lookup is not supported in v1alpha1. Using lists enables centralized IP
// management across multiple Access policies. Maps to cloudflare-go IPListRule.
//
// +kubebuilder:validation:XValidation:rule="has(self.id)",message="id must be specified"
type AccessIPListRule struct {
	// ID of the IP list in Cloudflare.
	// +optional
	// +kubebuilder:validation:MaxLength=36
	ID string `json:"id,omitempty"`

	// Name is not supported for lookup in v1alpha1. Specify id instead.
	// Deprecated: use id.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	Name string `json:"name,omitempty"`
}

// AccessCountryRule matches source country codes (P0 - no IdP required).
//
// AccessCountryRule allows or denies access based on the client's geographic location.
// Country codes must be ISO 3166-1 alpha-2 format (e.g., "US", "GB", "DE").
// Maps to cloudflare-go CountryRule.
type AccessCountryRule struct {
	// Codes are ISO 3166-1 alpha-2 country codes.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=50
	Codes []string `json:"codes"`
}

// AccessServiceTokenRule matches a specific service token by ID or by serviceTokens name.
//
// AccessServiceTokenRule enables machine-to-machine authentication using a specific
// Cloudflare service token. The TokenID is the Cloudflare-assigned identifier for
// the service token. Maps to cloudflare-go ServiceTokenRule.
//
// +kubebuilder:validation:XValidation:rule="has(self.tokenId) || has(self.name)",message="either tokenId or name must be specified"
type AccessServiceTokenRule struct {
	// TokenID is the Cloudflare service token ID.
	// +optional
	// +kubebuilder:validation:MaxLength=36
	TokenID string `json:"tokenId,omitempty"`

	// Name references an entry in spec.serviceTokens. The controller replaces it
	// with the created Cloudflare service token ID during policy sync.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	Name string `json:"name,omitempty"`
}

// AccessEmailRule matches specific email addresses (P1 - basic IdP required).
//
// AccessEmailRule allows access to users with specific email addresses authenticated
// through a configured identity provider. Maps to cloudflare-go EmailRule.
type AccessEmailRule struct {
	// Addresses to match.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=50
	Addresses []string `json:"addresses"`
}

// AccessEmailListRule references a Cloudflare Access email list (P1 - basic IdP required).
//
// AccessEmailListRule references a managed list of email addresses in Cloudflare
// by ID. Name-based lookup is not supported in v1alpha1. Using lists enables
// centralized email management across multiple Access policies. Maps to
// cloudflare-go EmailListRule.
//
// +kubebuilder:validation:XValidation:rule="has(self.id)",message="id must be specified"
type AccessEmailListRule struct {
	// ID of the Access list in Cloudflare.
	// +optional
	// +kubebuilder:validation:MaxLength=36
	ID string `json:"id,omitempty"`

	// Name is not supported for lookup in v1alpha1. Specify id instead.
	// Deprecated: use id.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	Name string `json:"name,omitempty"`
}

// AccessEmailDomainRule matches email domain suffix (P1 - basic IdP required).
//
// AccessEmailDomainRule allows access to any user whose email ends with the specified
// domain. Useful for allowing entire organizations or teams. Maps to cloudflare-go DomainRule.
type AccessEmailDomainRule struct {
	// Domain suffix (e.g., "example.com").
	// Max 253: RFC 1035 section 2.3.4 FQDN presentation-format limit.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Domain string `json:"domain"`
}

// AccessOIDCClaimRule matches OIDC token claims (P1 - basic IdP required).
//
// AccessOIDCClaimRule allows access based on specific claims in the OIDC token.
// Requires specifying the identity provider ID and the claim name/value to match.
// Maps to cloudflare-go AccessOIDCClaimRule.
type AccessOIDCClaimRule struct {
	// IdentityProviderID in Cloudflare.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=36
	IdentityProviderID string `json:"identityProviderId"`

	// ClaimName is the OIDC claim to match.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	ClaimName string `json:"claimName"`

	// ClaimValue is the expected value.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	ClaimValue string `json:"claimValue"`
}

// AccessGSuiteGroupRule matches Google Workspace groups (P2 - Google Workspace required).
//
// AccessGSuiteGroupRule allows access based on Google Workspace group membership.
// Requires a Google Workspace identity provider configured in Cloudflare Access.
// Maps to cloudflare-go GSuiteGroupRule.
type AccessGSuiteGroupRule struct {
	// IdentityProviderID in Cloudflare.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=36
	IdentityProviderID string `json:"identityProviderId"`

	// Email is the Google Workspace group email.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=320
	Email string `json:"email"`
}

// AccessGroupRule matches a Cloudflare Access Group by ID.
type AccessGroupRule struct {
	// ID is the Cloudflare Access Group ID.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=36
	ID string `json:"id"`
}

// AccessGroupRef references a Cloudflare Access Group.
//
// AccessGroupRef enables referencing reusable identity rules defined as Access Groups
// in Cloudflare. Groups can be referenced by Kubernetes CR name (future feature) or
// by Cloudflare ID.
//
// +kubebuilder:validation:XValidation:rule="has(self.name) || has(self.cloudflareId)",message="either name or cloudflareId must be specified"
type AccessGroupRef struct {
	// Name of AccessGroup CR in same namespace.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name,omitempty"`

	// CloudflareID of group in Cloudflare (bypasses CR lookup).
	// +optional
	// +kubebuilder:validation:MaxLength=36
	CloudflareID string `json:"cloudflareId,omitempty"`
}

// ApprovalGroup defines who can approve access requests for approval-required policies.
//
// ApprovalGroup specifies approvers by email address or email list UUID. When a
// policy requires approval, users matching this group can approve or deny access requests.
//
// +kubebuilder:validation:XValidation:rule="(has(self.emails) && size(self.emails) > 0) || has(self.emailListUuid)",message="at least one approver (emails or emailListUuid) must be specified"
type ApprovalGroup struct {
	// Emails of approvers.
	// +optional
	// +kubebuilder:validation:MaxItems=50
	Emails []string `json:"emails,omitempty"`

	// EmailListUUID is a Cloudflare Access email list UUID whose members can approve.
	// +optional
	// +kubebuilder:validation:MaxLength=36
	EmailListUUID string `json:"emailListUuid,omitempty"`

	// ApprovalsNeeded is number of approvals required.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	ApprovalsNeeded int `json:"approvalsNeeded,omitempty"`
}

// ServiceTokenConfig defines configuration for Cloudflare Access service tokens.
//
// ServiceTokenConfig enables machine-to-machine authentication. The controller creates
// the service token in Cloudflare and stores the credentials (client ID and secret) in
// the referenced Kubernetes Secret. The secret is only visible at creation time.
type ServiceTokenConfig struct {
	// Name is the token display name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	Name string `json:"name"`

	// Duration is the token validity period using Go duration format.
	// Only hours (h) supported by Cloudflare API. Use "8760h" for 1 year.
	// +kubebuilder:validation:Pattern=`^[0-9]+h$`
	// +kubebuilder:default="8760h"
	Duration string `json:"duration,omitempty"`

	// SecretRef stores the generated token credentials.
	// +kubebuilder:validation:Required
	SecretRef ServiceTokenSecretRef `json:"secretRef"`
}

// ServiceTokenSecretRef references a Kubernetes Secret for service token credential storage.
//
// ServiceTokenSecretRef identifies where to store the service token credentials
// (CF_ACCESS_CLIENT_ID and CF_ACCESS_CLIENT_SECRET keys) after token creation.
type ServiceTokenSecretRef struct {
	// Name of the Secret.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// CloudflareAccessPolicySpec defines a reusable Cloudflare Access policy.
//
// CloudflareAccessPolicySpec manages account-level reusable Access policies. Applications
// attach these policies through CloudflareAccessApplication policyRefs.
//
// +kubebuilder:validation:XValidation:rule="size(self.include) > 0",message="include rules are required"
type CloudflareAccessPolicySpec struct {
	// CloudflareRef references Cloudflare credentials.
	CloudflareRef CloudflareSecretRef `json:"cloudflareRef"`

	// Name is the Cloudflare Access policy display name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	Name string `json:"name"`

	// Decision is the policy action.
	// +kubebuilder:validation:Enum=allow;deny;bypass;non_identity
	// +kubebuilder:default=allow
	Decision string `json:"decision"`

	// Include rules (ANY must match for policy to apply).
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=25
	Include []AccessRule `json:"include"`

	// Exclude rules (if ANY match, policy does not apply).
	// +optional
	// +kubebuilder:validation:MaxItems=25
	Exclude []AccessRule `json:"exclude,omitempty"`

	// Require rules (ALL must match for policy to apply).
	// +optional
	// +kubebuilder:validation:MaxItems=25
	Require []AccessRule `json:"require,omitempty"`

	// SessionDuration overrides application session duration for this policy.
	// +optional
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:Pattern=`^([0-9]+(ns|us|ms|s|m|h))+$`
	SessionDuration string `json:"sessionDuration,omitempty"`

	// PurposeJustificationRequired requires user to provide justification.
	// +optional
	// +kubebuilder:default=false
	PurposeJustificationRequired bool `json:"purposeJustificationRequired,omitempty"`

	// PurposeJustificationPrompt is the prompt shown to user.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	PurposeJustificationPrompt string `json:"purposeJustificationPrompt,omitempty"`

	// ApprovalRequired requires approval from specific users.
	// +optional
	// +kubebuilder:default=false
	ApprovalRequired bool `json:"approvalRequired,omitempty"`

	// ApprovalGroups defines who can approve access.
	// +optional
	// +kubebuilder:validation:MaxItems=10
	ApprovalGroups []ApprovalGroup `json:"approvalGroups,omitempty"`

	// ServiceTokens for machine-to-machine authentication.
	// +optional
	// +kubebuilder:validation:MaxItems=10
	ServiceTokens []ServiceTokenConfig `json:"serviceTokens,omitempty"`
}

// PolicyAncestorStatus describes the policy attachment status for a specific target.
//
// PolicyAncestorStatus follows the Gateway API PolicyAncestorStatus pattern to report
// per-target attachment status. Each target reference in the spec has a corresponding
// ancestor status entry showing whether the policy was successfully attached.
type PolicyAncestorStatus struct {
	// AncestorRef identifies the target.
	AncestorRef PolicyTargetReference `json:"ancestorRef"`

	// ControllerName identifies the controller managing this attachment.
	// +kubebuilder:validation:MaxLength=253
	ControllerName string `json:"controllerName"`

	// Conditions for this specific target.
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// CloudflareAccessPolicyStatus defines the observed state of a CloudflareAccessPolicy resource.
type CloudflareAccessPolicyStatus struct {
	// PolicyID is the Cloudflare Access reusable policy ID.
	PolicyID string `json:"policyId,omitempty"`

	// AccountID is the Cloudflare account ID used for this policy.
	AccountID string `json:"accountId,omitempty"`

	// CredentialSecretRef is the resolved credentials Secret used for cleanup.
	// The namespace is always stored explicitly.
	CredentialSecretRef *SecretReference `json:"credentialSecretRef,omitempty"`

	// Reusable reports whether Cloudflare returned this policy as reusable.
	Reusable bool `json:"reusable,omitempty"`

	// AppCount is the number of Access Applications currently using this policy.
	AppCount int64 `json:"appCount,omitempty"`

	// ServiceTokenIDs maps token names to Cloudflare IDs.
	ServiceTokenIDs map[string]string `json:"serviceTokenIds,omitempty"`

	// ObservedGeneration is the last generation processed.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions describe current state.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// CloudflareAccessPolicy is the Schema for the cloudflareaccesspolicies API.
//
// CloudflareAccessPolicy manages a reusable account-level Cloudflare Access Policy.
// CloudflareAccessApplication attaches reusable policies to Gateway API targets.
//
// Access rules are organized into implementation tiers based on IdP requirements:
//   - P0: IP, IPList, Country, Everyone, ServiceToken, AnyValidServiceToken (no IdP)
//   - P1: Email, EmailList, EmailDomain, OIDCClaim (basic IdP required)
//   - P2: GSuiteGroup (Google Workspace required)
//   - P3: not in current product scope (Certificate, CommonName, Group, GitHub, Azure, Okta, SAML, etc.)
//
// Status conditions:
//   - Ready: policy is synced and service tokens are ready when configured
//   - CredentialsValid: Cloudflare credentials have been validated
//   - ServiceTokensReady: all service tokens have been created
//   - PolicySynced: reusable Access policy exists in Cloudflare
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=cfap;cfaccess
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Policy",type="string",JSONPath=".status.policyId"
// +kubebuilder:printcolumn:name="Apps",type="integer",JSONPath=".status.appCount"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type CloudflareAccessPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CloudflareAccessPolicySpec   `json:"spec,omitempty"`
	Status CloudflareAccessPolicyStatus `json:"status,omitempty"`
}

// CloudflareAccessPolicyList contains a list of CloudflareAccessPolicy resources.
//
// +kubebuilder:object:root=true
type CloudflareAccessPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareAccessPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CloudflareAccessPolicy{}, &CloudflareAccessPolicyList{})
}

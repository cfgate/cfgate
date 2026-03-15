package cloudflare

import (
	"cmp"
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"
)

// AccessService handles Cloudflare Access operations including applications, policies,
// service tokens, and mTLS certificates. It wraps the Client interface with cfgate-specific
// logic for idempotent ensure operations and declarative policy synchronization.
type AccessService struct {
	client Client
	log    logr.Logger
}

// NewAccessService creates a new AccessService with the given client and logger.
// The logger is named "access-service" for structured logging context.
func NewAccessService(client Client, log logr.Logger) *AccessService {
	return &AccessService{
		client: client,
		log:    log.WithName("access-service"),
	}
}

// SecretWriter is an interface for storing secrets in Kubernetes.
// Used by EnsureServiceToken to persist client credentials.
type SecretWriter interface {
	// WriteSecret creates or updates a secret with the given name and data.
	WriteSecret(ctx context.Context, name string, data map[string][]byte) error
}

// AccessApplication represents a Cloudflare Access Application.
type AccessApplication struct {
	// ID is the unique application identifier.
	ID string

	// AUD is the Application Audience Tag for JWT validation.
	AUD string

	// Name is the application display name.
	Name string

	// Domain is the protected domain.
	Domain string

	// Type is the application type (self_hosted, saas, ssh, vnc, etc.).
	Type string

	// SessionDuration is the session cookie lifetime (e.g., "24h").
	SessionDuration string

	// AllowedIdps is the list of allowed identity provider IDs.
	AllowedIdps []string

	// AutoRedirectToIdentity auto-redirects to IdP if single provider.
	AutoRedirectToIdentity bool

	// EnableBindingCookie enables session binding cookies.
	EnableBindingCookie bool

	// HttpOnlyCookieAttribute sets HttpOnly flag on session cookies.
	HttpOnlyCookieAttribute bool

	// SameSiteCookieAttribute sets SameSite cookie attribute.
	SameSiteCookieAttribute string

	// SkipInterstitial skips Access login page for API requests.
	SkipInterstitial bool

	// LogoURL is the application logo URL.
	LogoURL string

	// AppLauncherVisible shows the app in App Launcher.
	AppLauncherVisible bool

	// CustomDenyMessage is the custom denial message.
	CustomDenyMessage string

	// CustomDenyURL is the custom denial redirect URL.
	CustomDenyURL string

	// CustomNonIdentityDenyURL is the denial URL for non-identity requests.
	CustomNonIdentityDenyURL string

	// CORSHeaders is the CORS configuration (nil if not set).
	CORSHeaders *CORSHeadersParam

	// OptionsPreflightBypass bypasses Access for OPTIONS preflight.
	OptionsPreflightBypass bool

	// PathCookieAttribute scopes JWT cookie to application path.
	PathCookieAttribute bool

	// ServiceAuth401Redirect returns 401 instead of redirect for service auth.
	ServiceAuth401Redirect bool

	// ReadServiceTokensFromHeader reads service tokens from a single header.
	ReadServiceTokensFromHeader string

	// CreatedAt is the creation timestamp.
	CreatedAt time.Time

	// UpdatedAt is the last update timestamp.
	UpdatedAt time.Time
}

// CORSHeadersParam represents CORS configuration for an Access Application.
type CORSHeadersParam struct {
	AllowAllHeaders  bool
	AllowAllMethods  bool
	AllowAllOrigins  bool
	AllowCredentials bool
	AllowedHeaders   []string
	AllowedMethods   []string
	AllowedOrigins   []string
	MaxAge           int
}

// ApplicationParams contains parameters for creating or updating an Access application.
type ApplicationParams struct {
	// Name is the application display name.
	Name string

	// Domain is the protected domain.
	Domain string

	// Type is the application type. Defaults to self_hosted.
	Type string

	// SessionDuration is the session lifetime. Defaults to "24h".
	SessionDuration string

	// AllowedIdps is the list of allowed identity provider IDs.
	AllowedIdps []string

	// AutoRedirectToIdentity auto-redirects if single IdP.
	AutoRedirectToIdentity bool

	// EnableBindingCookie enables sticky sessions.
	EnableBindingCookie bool

	// HttpOnlyCookieAttribute sets HttpOnly flag. Defaults to true.
	HttpOnlyCookieAttribute *bool

	// SameSiteCookieAttribute sets SameSite attribute. Defaults to "lax".
	SameSiteCookieAttribute string

	// SkipInterstitial skips login page for APIs.
	SkipInterstitial bool

	// LogoURL is the logo URL.
	LogoURL string

	// AppLauncherVisible shows in App Launcher.
	AppLauncherVisible bool

	// CustomDenyMessage is the denial message.
	CustomDenyMessage string

	// CustomDenyURL is the denial redirect.
	CustomDenyURL string

	// CORSHeaders configures CORS for the application.
	CORSHeaders *CORSHeadersParam

	// OptionsPreflightBypass bypasses Access for OPTIONS preflight.
	OptionsPreflightBypass bool

	// PathCookieAttribute scopes JWT cookie to application path.
	PathCookieAttribute bool

	// ServiceAuth401Redirect returns 401 instead of redirect for service auth.
	ServiceAuth401Redirect bool

	// CustomNonIdentityDenyURL is the denial URL for non-identity requests.
	CustomNonIdentityDenyURL string

	// ReadServiceTokensFromHeader reads service tokens from a single header.
	ReadServiceTokensFromHeader string
}

// AccessPolicy represents a Cloudflare Access Policy.
type AccessPolicy struct {
	// ID is the unique policy identifier.
	ID string

	// Name is the policy display name.
	Name string

	// Decision is the policy action (allow, deny, bypass, non_identity).
	Decision string

	// Precedence is the evaluation order (lower = first).
	Precedence int

	// Include are rules that must match (ANY).
	Include []AccessRuleParam

	// Exclude are rules that exclude (ANY).
	Exclude []AccessRuleParam

	// Require are rules that must match (ALL).
	Require []AccessRuleParam

	// SessionDuration overrides application session duration.
	SessionDuration string

	// PurposeJustificationRequired requires purpose justification.
	PurposeJustificationRequired bool

	// PurposeJustificationPrompt is the justification prompt text.
	PurposeJustificationPrompt string

	// ApprovalRequired requires manager approval.
	ApprovalRequired bool

	// ApprovalGroups is the approval configuration.
	ApprovalGroups []ApprovalGroupParam

	// CreatedAt is the creation timestamp.
	CreatedAt time.Time

	// UpdatedAt is the last update timestamp.
	UpdatedAt time.Time
}

// PolicyParams contains parameters for creating or updating an Access policy.
type PolicyParams struct {
	// Name is the policy display name.
	Name string

	// Decision is the policy action.
	Decision string

	// Precedence is the evaluation order.
	Precedence int

	// Include are rules that must match (ANY).
	Include []AccessRuleParam

	// Exclude are rules that exclude (ANY).
	Exclude []AccessRuleParam

	// Require are rules that must match (ALL).
	Require []AccessRuleParam

	// SessionDuration overrides application session duration.
	SessionDuration string

	// PurposeJustificationRequired requires purpose justification.
	PurposeJustificationRequired bool

	// PurposeJustificationPrompt is the justification prompt text.
	PurposeJustificationPrompt string

	// ApprovalRequired requires manager approval.
	ApprovalRequired bool

	// ApprovalGroups is the approval configuration.
	ApprovalGroups []ApprovalGroupParam
}

// AccessRuleParam represents an access rule parameter.
// Only one field should be set per rule.
type AccessRuleParam struct {
	// ============================================================
	// P0: No IdP required (always testable)
	// ============================================================

	// IPRange matches an IP range (CIDR notation).
	IPRange *string

	// IPListID matches IPs from a Cloudflare IP List.
	IPListID *string

	// Country matches a country code (ISO 3166-1 alpha-2).
	Country *string

	// Everyone matches everyone (set to true).
	Everyone *bool

	// ServiceTokenID matches a specific service token.
	ServiceTokenID *string

	// AnyValidServiceToken matches any valid service token.
	AnyValidServiceToken *bool

	// ============================================================
	// P1: Basic IdP (Google Workspace)
	// ============================================================

	// Email matches a specific email address.
	Email *string

	// EmailListID matches emails from a Cloudflare Access list.
	EmailListID *string

	// EmailDomain matches an email domain.
	EmailDomain *string

	// OIDCClaim matches an OIDC token claim.
	OIDCClaim *OIDCClaimParam

	// ============================================================
	// P2: Google Workspace Groups
	// ============================================================

	// GSuiteGroup matches Google Workspace group membership.
	GSuiteGroup *GSuiteGroupParam

	// ============================================================
	// P3: v0.2.0 (not implemented in alpha.3)
	// ============================================================

	// Certificate requires a valid client certificate (set to true).
	Certificate *bool

	// CommonName matches certificate common name.
	CommonName *string

	// GroupID references an Access Group.
	GroupID *string
}

// OIDCClaimParam represents an OIDC claim rule parameter.
type OIDCClaimParam struct {
	IdentityProviderID string
	ClaimName          string
	ClaimValue         string
}

// GSuiteGroupParam represents a Google Workspace group rule parameter.
type GSuiteGroupParam struct {
	IdentityProviderID string
	Email              string // Group email address
}

// ApprovalGroupParam represents an approval configuration.
type ApprovalGroupParam struct {
	EmailAddresses  []string
	EmailListUUID   string
	ApprovalsNeeded int
}

// AccessGroup represents a Cloudflare Access Group.
type AccessGroup struct {
	// ID is the unique group identifier.
	ID string

	// Name is the group display name.
	Name string

	// Include are rules that must match (ANY).
	Include []AccessRuleParam

	// Exclude are rules that exclude (ANY).
	Exclude []AccessRuleParam

	// Require are rules that must match (ALL).
	Require []AccessRuleParam

	// CreatedAt is the creation timestamp.
	CreatedAt time.Time

	// UpdatedAt is the last update timestamp.
	UpdatedAt time.Time
}

// GroupParams contains parameters for creating or updating an Access group.
type GroupParams struct {
	// Name is the group display name.
	Name string

	// Include are rules that must match (ANY).
	Include []AccessRuleParam

	// Exclude are rules that exclude (ANY).
	Exclude []AccessRuleParam

	// Require are rules that must match (ALL).
	Require []AccessRuleParam
}

// ServiceToken represents a Cloudflare Access Service Token.
type ServiceToken struct {
	// ID is the unique token identifier.
	ID string

	// Name is the token display name.
	Name string

	// ClientID is the Client ID (CF-Access-Client-Id header).
	ClientID string

	// Duration is the token validity period.
	Duration string

	// ExpiresAt is the expiration timestamp.
	ExpiresAt time.Time
}

// ServiceTokenWithSecret includes the secret, returned only on create/rotate.
type ServiceTokenWithSecret struct {
	ServiceToken

	// ClientSecret is the Client Secret (CF-Access-Client-Secret header).
	// Only returned on create or rotate operations.
	ClientSecret string
}

// ServiceTokenParams contains parameters for creating or updating a service token.
type ServiceTokenParams struct {
	// Name is the token display name.
	Name string

	// Duration is the token validity period in hours (e.g., "8760h" for 1 year).
	Duration string
}

// MTLSCertificate represents a Cloudflare mTLS Certificate.
type MTLSCertificate struct {
	// ID is the unique certificate identifier.
	ID string

	// Name is the certificate display name.
	Name string

	// Fingerprint is the SHA-256 fingerprint.
	Fingerprint string

	// AssociatedHostnames are hostnames using this certificate.
	AssociatedHostnames []string

	// ExpiresOn is the certificate expiration.
	ExpiresOn time.Time
}

// CreateCertificateParams contains parameters for creating a certificate.
type CreateCertificateParams struct {
	// Name is the certificate display name.
	Name string

	// Certificate is the PEM-encoded certificate.
	Certificate string

	// AssociatedHostnames are hostnames using this certificate.
	AssociatedHostnames []string
}

// UpdateCertificateParams contains parameters for updating a certificate.
type UpdateCertificateParams struct {
	// Name is the certificate display name.
	Name string

	// AssociatedHostnames are hostnames using this certificate.
	AssociatedHostnames []string
}

// CertificateSettings represents mTLS certificate settings.
type CertificateSettings struct {
	// Hostname is the hostname for mTLS.
	Hostname string

	// ChinaNetwork enables China network mTLS.
	ChinaNetwork bool

	// ClientCertificateForwarding forwards client cert to origin.
	ClientCertificateForwarding bool
}

// EnsureApplication ensures an application exists with the given configuration.
// If an application with the name exists, it is adopted and updated if managed fields
// have drifted. Otherwise, a new application is created.
// Returns the application and whether it was created (vs adopted/updated).
func (s *AccessService) EnsureApplication(ctx context.Context, accountID string, params ApplicationParams) (*AccessApplication, bool, error) {
	s.log.Info("ensuring access application exists",
		"accountID", accountID,
		"domain", params.Domain,
	)

	existing, err := s.client.GetAccessApplicationByName(ctx, accountID, params.Name)
	if err != nil {
		return nil, false, fmt.Errorf("failed to check for existing application: %w", err)
	}

	if existing != nil {
		// Warn if application type has drifted (cannot be changed via API)
		desiredType := params.Type
		if desiredType == "" {
			desiredType = "self_hosted"
		}
		if existing.Type != desiredType {
			s.log.Info("application type changed in CR but Cloudflare API does not support type updates; delete and recreate the application to change type",
				"applicationId", existing.ID,
				"existingType", existing.Type,
				"desiredType", desiredType,
			)
		}

		if accessApplicationNeedsUpdate(existing, &params) {
			s.log.Info("access application drift detected, updating",
				"applicationId", existing.ID,
				"domain", existing.Domain,
			)
			updated, err := s.client.UpdateAccessApplication(ctx, accountID, existing.ID, params)
			if err != nil {
				return nil, false, fmt.Errorf("failed to update application: %w", err)
			}
			return updated, false, nil
		}

		s.log.V(1).Info("access application unchanged, adopting",
			"applicationId", existing.ID,
			"domain", existing.Domain,
		)
		return existing, false, nil
	}

	s.log.Info("creating new access application",
		"accountID", accountID,
		"domain", params.Domain,
		"name", params.Name,
	)

	app, err := s.client.CreateAccessApplication(ctx, accountID, params)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create application: %w", err)
	}

	return app, true, nil
}

// accessApplicationNeedsUpdate compares an existing application against desired params.
// Returns true if any managed field has drifted and an update is needed.
//
// Note: Type is compared for drift detection but cannot be changed via the
// Cloudflare API. The caller should emit a warning when Type has drifted.
func accessApplicationNeedsUpdate(existing *AccessApplication, desired *ApplicationParams) bool {
	if existing.Name != desired.Name {
		return true
	}
	if existing.Domain != desired.Domain {
		return true
	}
	desiredType := desired.Type
	if desiredType == "" {
		desiredType = "self_hosted"
	}
	if existing.Type != desiredType {
		return true
	}
	desiredSession := desired.SessionDuration
	if desiredSession == "" {
		desiredSession = "24h"
	}
	if existing.SessionDuration != desiredSession {
		return true
	}
	if existing.SkipInterstitial != desired.SkipInterstitial {
		return true
	}
	if existing.EnableBindingCookie != desired.EnableBindingCookie {
		return true
	}
	if existing.AutoRedirectToIdentity != desired.AutoRedirectToIdentity {
		return true
	}
	if existing.AppLauncherVisible != desired.AppLauncherVisible {
		return true
	}
	if existing.LogoURL != desired.LogoURL {
		return true
	}
	if existing.CustomDenyMessage != desired.CustomDenyMessage {
		return true
	}
	if existing.CustomDenyURL != desired.CustomDenyURL {
		return true
	}
	if existing.CustomNonIdentityDenyURL != desired.CustomNonIdentityDenyURL {
		return true
	}
	if existing.OptionsPreflightBypass != desired.OptionsPreflightBypass {
		return true
	}
	if existing.PathCookieAttribute != desired.PathCookieAttribute {
		return true
	}
	if existing.ServiceAuth401Redirect != desired.ServiceAuth401Redirect {
		return true
	}
	if existing.ReadServiceTokensFromHeader != desired.ReadServiceTokensFromHeader {
		return true
	}
	desiredSameSite := desired.SameSiteCookieAttribute
	if desiredSameSite == "" {
		desiredSameSite = "lax"
	}
	if existing.SameSiteCookieAttribute != desiredSameSite {
		return true
	}
	desiredHttpOnly := true
	if desired.HttpOnlyCookieAttribute != nil {
		desiredHttpOnly = *desired.HttpOnlyCookieAttribute
	}
	if existing.HttpOnlyCookieAttribute != desiredHttpOnly {
		return true
	}
	if !stringSlicesEqual(existing.AllowedIdps, desired.AllowedIdps) {
		return true
	}
	if !corsHeadersEqual(existing.CORSHeaders, desired.CORSHeaders) {
		return true
	}
	return false
}

// SyncPolicies synchronizes access policies for an application.
// It deletes policies not in the desired set, updates existing policies if different,
// and creates new policies for additions.
// Returns the policy IDs after sync.
func (s *AccessService) SyncPolicies(ctx context.Context, accountID, appID string, desired []PolicyParams) ([]string, error) {
	// List existing policies
	existing, err := s.client.ListAccessPolicies(ctx, accountID, appID)
	if err != nil {
		return nil, fmt.Errorf("failed to list existing policies: %w", err)
	}

	s.log.Info("syncing access policies",
		"applicationId", appID,
		"desiredCount", len(desired),
		"existingCount", len(existing),
	)

	// Build maps for comparison
	existingByName := make(map[string]*AccessPolicy)
	for i := range existing {
		existingByName[existing[i].Name] = &existing[i]
	}

	desiredByName := make(map[string]PolicyParams)
	for _, p := range desired {
		desiredByName[p.Name] = p
	}

	var resultIDs []string
	var toCreate, toUpdate, toDelete int

	// Delete policies not in desired set
	for name, policy := range existingByName {
		if _, found := desiredByName[name]; !found {
			toDelete++
			s.log.V(1).Info("policy operation",
				"applicationId", appID,
				"policyName", name,
				"operation", "delete",
			)
			if err := s.client.DeleteAccessPolicy(ctx, accountID, appID, policy.ID); err != nil {
				return nil, fmt.Errorf("failed to delete policy %s: %w", name, err)
			}
		}
	}

	// Create or update policies
	for name, params := range desiredByName {
		if existingPolicy, found := existingByName[name]; found {
			// Update if different
			if !accessPolicyEqual(existingPolicy, &params) {
				toUpdate++
				s.log.V(1).Info("policy operation",
					"applicationId", appID,
					"policyName", name,
					"operation", "update",
				)
				updated, err := s.client.UpdateAccessPolicy(ctx, accountID, appID, existingPolicy.ID, params)
				if err != nil {
					return nil, fmt.Errorf("failed to update policy %s: %w", name, err)
				}
				resultIDs = append(resultIDs, updated.ID)
			} else {
				s.log.V(1).Info("skipping policy update, content unchanged",
					"applicationId", appID,
					"policyName", name,
				)
				resultIDs = append(resultIDs, existingPolicy.ID)
			}
		} else {
			// Create new policy
			toCreate++
			s.log.V(1).Info("policy operation",
				"applicationId", appID,
				"policyName", name,
				"operation", "create",
			)
			created, err := s.client.CreateAccessPolicy(ctx, accountID, appID, params)
			if err != nil {
				return nil, fmt.Errorf("failed to create policy %s: %w", name, err)
			}
			resultIDs = append(resultIDs, created.ID)
		}
	}

	s.log.Info("access policies synced",
		"applicationId", appID,
		"toCreate", toCreate,
		"toUpdate", toUpdate,
		"toDelete", toDelete,
	)

	return resultIDs, nil
}

// accessPolicyEqual compares desired vs existing access policy content.
// Returns true if no update is needed. Uses deep comparison for rule slices
// to avoid unnecessary Cloudflare API calls when policy content is unchanged.
func accessPolicyEqual(existing *AccessPolicy, desired *PolicyParams) bool {
	if existing.Name != desired.Name {
		return false
	}
	if existing.Decision != desired.Decision {
		return false
	}
	if existing.Precedence != desired.Precedence {
		return false
	}
	if existing.SessionDuration != desired.SessionDuration {
		return false
	}
	if existing.PurposeJustificationRequired != desired.PurposeJustificationRequired {
		return false
	}
	if existing.PurposeJustificationPrompt != desired.PurposeJustificationPrompt {
		return false
	}
	if existing.ApprovalRequired != desired.ApprovalRequired {
		return false
	}
	if !accessRulesEqual(existing.Include, desired.Include) {
		return false
	}
	if !accessRulesEqual(existing.Exclude, desired.Exclude) {
		return false
	}
	if !accessRulesEqual(existing.Require, desired.Require) {
		return false
	}
	if !approvalGroupsEqual(existing.ApprovalGroups, desired.ApprovalGroups) {
		return false
	}
	return true
}

// stringSlicesEqual compares two string slices without regard to order.
// Returns true if both slices contain the same elements regardless of position.
// Treats nil and empty slices as equal.
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	sa := make([]string, len(a))
	copy(sa, a)
	sb := make([]string, len(b))
	copy(sb, b)
	slices.Sort(sa)
	slices.Sort(sb)
	return slices.Equal(sa, sb)
}

// accessRuleKey returns a canonical sort key for an AccessRuleParam.
// Only one field should be set per rule; the key encodes the active field type and value.
func accessRuleKey(r AccessRuleParam) string {
	switch {
	case r.IPRange != nil:
		return "ip:" + *r.IPRange
	case r.IPListID != nil:
		return "iplist:" + *r.IPListID
	case r.Country != nil:
		return "country:" + *r.Country
	case r.Everyone != nil:
		return "everyone"
	case r.ServiceTokenID != nil:
		return "servicetoken:" + *r.ServiceTokenID
	case r.AnyValidServiceToken != nil:
		return "anyservicetoken"
	case r.Email != nil:
		return "email:" + *r.Email
	case r.EmailListID != nil:
		return "emaillist:" + *r.EmailListID
	case r.EmailDomain != nil:
		return "emaildomain:" + *r.EmailDomain
	case r.OIDCClaim != nil:
		return "oidc:" + r.OIDCClaim.IdentityProviderID + ":" + r.OIDCClaim.ClaimName + ":" + r.OIDCClaim.ClaimValue
	case r.GSuiteGroup != nil:
		return "gsuite:" + r.GSuiteGroup.IdentityProviderID + ":" + r.GSuiteGroup.Email
	case r.Certificate != nil:
		return "cert"
	case r.CommonName != nil:
		return "cn:" + *r.CommonName
	case r.GroupID != nil:
		return "group:" + *r.GroupID
	default:
		return ""
	}
}

// accessRulesEqual compares two AccessRuleParam slices without regard to order.
// Rules are sorted by canonical key before comparison to prevent spurious drift
// when the Cloudflare API returns rules in a different order than submitted.
func accessRulesEqual(a, b []AccessRuleParam) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	sa := make([]AccessRuleParam, len(a))
	copy(sa, a)
	sb := make([]AccessRuleParam, len(b))
	copy(sb, b)
	cmpKey := func(x, y AccessRuleParam) int {
		return cmp.Compare(accessRuleKey(x), accessRuleKey(y))
	}
	slices.SortFunc(sa, cmpKey)
	slices.SortFunc(sb, cmpKey)
	return reflect.DeepEqual(sa, sb)
}

// approvalGroupsEqual compares two ApprovalGroupParam slices without regard to order.
// Inner EmailAddresses slices are also compared order-insensitively.
func approvalGroupsEqual(a, b []ApprovalGroupParam) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	sortKey := func(g ApprovalGroupParam) string {
		emails := make([]string, len(g.EmailAddresses))
		copy(emails, g.EmailAddresses)
		slices.Sort(emails)
		return fmt.Sprintf("%s:%d:%s", g.EmailListUUID, g.ApprovalsNeeded, strings.Join(emails, ","))
	}
	sa := make([]ApprovalGroupParam, len(a))
	copy(sa, a)
	sb := make([]ApprovalGroupParam, len(b))
	copy(sb, b)
	slices.SortFunc(sa, func(x, y ApprovalGroupParam) int {
		return cmp.Compare(sortKey(x), sortKey(y))
	})
	slices.SortFunc(sb, func(x, y ApprovalGroupParam) int {
		return cmp.Compare(sortKey(x), sortKey(y))
	})
	for i := range sa {
		if sa[i].EmailListUUID != sb[i].EmailListUUID {
			return false
		}
		if sa[i].ApprovalsNeeded != sb[i].ApprovalsNeeded {
			return false
		}
		if !stringSlicesEqual(sa[i].EmailAddresses, sb[i].EmailAddresses) {
			return false
		}
	}
	return true
}

// corsHeadersEqual compares two CORSHeadersParam values, comparing inner string
// slices without regard to order. Handles nil for both operands.
func corsHeadersEqual(a, b *CORSHeadersParam) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if a.AllowAllHeaders != b.AllowAllHeaders {
		return false
	}
	if a.AllowAllMethods != b.AllowAllMethods {
		return false
	}
	if a.AllowAllOrigins != b.AllowAllOrigins {
		return false
	}
	if a.AllowCredentials != b.AllowCredentials {
		return false
	}
	if a.MaxAge != b.MaxAge {
		return false
	}
	if !stringSlicesEqual(a.AllowedHeaders, b.AllowedHeaders) {
		return false
	}
	if !stringSlicesEqual(a.AllowedMethods, b.AllowedMethods) {
		return false
	}
	if !stringSlicesEqual(a.AllowedOrigins, b.AllowedOrigins) {
		return false
	}
	return true
}

// EnsureServiceToken ensures a service token exists with the given configuration.
// If a token with the name exists and is not expired, it is returned (no secret available).
// If expired, the token is rotated and the new secret is stored.
// If not exists, a new token is created and the secret is stored.
func (s *AccessService) EnsureServiceToken(ctx context.Context, accountID string, params ServiceTokenParams, secretWriter SecretWriter) (*ServiceToken, error) {
	s.log.Info("ensuring service token exists",
		"accountID", accountID,
		"tokenName", params.Name,
	)

	// Try to find existing token by name
	tokens, err := s.client.ListServiceTokens(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to list service tokens: %w", err)
	}

	var existing *ServiceToken
	for i := range tokens {
		if tokens[i].Name == params.Name {
			existing = &tokens[i]
			break
		}
	}

	if existing != nil {
		// Check if expired
		if time.Now().After(existing.ExpiresAt) {
			s.log.Info("service token expired, rotating",
				"tokenId", existing.ID,
				"tokenName", existing.Name,
				"expiredAt", existing.ExpiresAt,
			)

			rotated, err := s.client.RotateServiceToken(ctx, accountID, existing.ID)
			if err != nil {
				return nil, fmt.Errorf("failed to rotate service token: %w", err)
			}

			// Store the new secret. If this fails, the old secret is already
			// invalidated by rotation. Delete the token so the next reconcile
			// creates a fresh token+secret pair.
			if secretWriter != nil {
				if err := secretWriter.WriteSecret(ctx, params.Name, map[string][]byte{
					"CF_ACCESS_CLIENT_ID":     []byte(rotated.ClientID),
					"CF_ACCESS_CLIENT_SECRET": []byte(rotated.ClientSecret),
				}); err != nil {
					s.log.Info("secret write failed after token rotation, deleting token to allow retry on next reconcile",
						"tokenId", rotated.ID,
						"tokenName", rotated.Name,
						"writeError", err.Error(),
					)
					if delErr := s.client.DeleteServiceToken(ctx, accountID, rotated.ID); delErr != nil {
						s.log.Error(delErr, "failed to delete service token after secret write failure",
							"tokenId", rotated.ID,
						)
					}
					return nil, fmt.Errorf("failed to store rotated service token secret: %w", err)
				}
				s.log.Info("service token rotated, secret stored",
					"tokenId", rotated.ID,
					"tokenName", rotated.Name,
					"expiresAt", rotated.ExpiresAt,
				)
			}

			return &rotated.ServiceToken, nil
		}

		s.log.V(1).Info("service token already exists",
			"tokenId", existing.ID,
			"tokenName", existing.Name,
			"expiresAt", existing.ExpiresAt,
		)
		return existing, nil
	}

	// Create new token
	s.log.Info("creating new service token",
		"accountID", accountID,
		"tokenName", params.Name,
	)

	created, err := s.client.CreateServiceToken(ctx, accountID, params)
	if err != nil {
		return nil, fmt.Errorf("failed to create service token: %w", err)
	}

	// Store the secret. If this fails, delete the token so the next reconcile
	// creates a fresh token+secret pair. The client secret is only available at
	// creation time, so an orphaned token without a stored secret is unusable.
	if secretWriter != nil {
		if err := secretWriter.WriteSecret(ctx, params.Name, map[string][]byte{
			"CF_ACCESS_CLIENT_ID":     []byte(created.ClientID),
			"CF_ACCESS_CLIENT_SECRET": []byte(created.ClientSecret),
		}); err != nil {
			s.log.Info("secret write failed after token creation, deleting token to allow retry on next reconcile",
				"tokenId", created.ID,
				"tokenName", created.Name,
				"writeError", err.Error(),
			)
			if delErr := s.client.DeleteServiceToken(ctx, accountID, created.ID); delErr != nil {
				s.log.Error(delErr, "failed to delete service token after secret write failure",
					"tokenId", created.ID,
				)
			}
			return nil, fmt.Errorf("failed to store service token secret: %w", err)
		}
		s.log.Info("service token created, secret stored",
			"tokenId", created.ID,
			"tokenName", created.Name,
			"expiresAt", created.ExpiresAt,
		)
	}

	return &created.ServiceToken, nil
}

// EnsureMTLSCertificate ensures an mTLS certificate exists with the given configuration.
// If a certificate with the name or fingerprint exists, it is returned.
// Otherwise, a new certificate is created.
func (s *AccessService) EnsureMTLSCertificate(ctx context.Context, accountID string, params CreateCertificateParams) (*MTLSCertificate, bool, error) {
	s.log.Info("ensuring mTLS certificate exists",
		"accountID", accountID,
		"certificateName", params.Name,
	)

	// List existing certificates
	certs, err := s.client.ListMTLSCertificates(ctx, accountID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list mTLS certificates: %w", err)
	}

	// Find by name
	for i := range certs {
		if certs[i].Name == params.Name {
			s.log.V(1).Info("mTLS certificate already exists",
				"certificateId", certs[i].ID,
				"certificateName", certs[i].Name,
			)
			return &certs[i], false, nil
		}
	}

	// Create new certificate
	s.log.Info("creating new mTLS certificate",
		"accountID", accountID,
		"certificateName", params.Name,
	)

	cert, err := s.client.CreateMTLSCertificate(ctx, accountID, params)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create mTLS certificate: %w", err)
	}

	s.log.Info("mTLS certificate created",
		"certificateId", cert.ID,
		"certificateName", cert.Name,
		"fingerprint", cert.Fingerprint,
	)

	return cert, true, nil
}

// UpdateMTLSHostnames updates mTLS hostname associations.
func (s *AccessService) UpdateMTLSHostnames(ctx context.Context, accountID string, hostnames []string, enableForwarding bool) error {
	settings := make([]CertificateSettings, len(hostnames))
	for i, hostname := range hostnames {
		settings[i] = CertificateSettings{
			Hostname:                    hostname,
			ClientCertificateForwarding: enableForwarding,
		}
	}

	s.log.Info("updating mTLS hostname settings",
		"accountID", accountID,
		"hostnames", hostnames,
		"enableForwarding", enableForwarding,
	)

	_, err := s.client.UpdateMTLSCertificateSettings(ctx, accountID, settings)
	if err != nil {
		return fmt.Errorf("failed to update mTLS certificate settings: %w", err)
	}

	return nil
}

// DeleteApplication deletes an application and all associated resources.
func (s *AccessService) DeleteApplication(ctx context.Context, accountID, appID string) error {
	s.log.Info("deleting access application",
		"applicationId", appID,
	)

	// Delete the application (policies are deleted automatically)
	if err := s.client.DeleteAccessApplication(ctx, accountID, appID); err != nil {
		return fmt.Errorf("failed to delete access application: %w", err)
	}

	return nil
}

// Client returns the underlying Cloudflare client.
// Used for direct API operations not wrapped by AccessService.
func (s *AccessService) Client() Client {
	return s.client
}

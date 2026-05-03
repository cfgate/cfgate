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

// AccessService handles Cloudflare Access operations including applications,
// policies, and service tokens. It wraps the Client interface with cfgate-specific
// logic for idempotent ensure operations and declarative policy synchronization.
type AccessService struct {
	client Client
	log    logr.Logger
}

const accessTagLimitErrorCode int64 = 12146

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

	// Destinations are public destination URIs secured by Access.
	Destinations []string

	// Tags are Cloudflare Access application tags.
	Tags []string

	// Policies are reusable policy links attached to the application.
	Policies []ApplicationPolicyLink

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

// AccessTag represents a Cloudflare Access tag.
type AccessTag struct {
	// Name is the unique tag name.
	Name string
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

	// Destinations are public destination URIs secured by Access.
	Destinations []string

	// Tags are Cloudflare Access application tags.
	Tags []string

	// Policies are reusable policy links attached to the application.
	Policies []ApplicationPolicyLink

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

	// Reusable reports whether the policy is reusable at account level.
	Reusable bool

	// AppCount is the number of Access applications using the reusable policy.
	AppCount int64

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

// ApplicationPolicyLink links a reusable policy to an application.
type ApplicationPolicyLink struct {
	// ID is the reusable policy ID.
	ID string

	// Precedence is the execution order within the application.
	Precedence int
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
	// P3: retained for internal model and SDK round-trip compatibility
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

// EnsureApplicationByIDOrTags ensures an application exists without adopting by name.
// It first uses statusID, then adopts only an application with the same domain and
// all desired tags. This keeps cfgate ownership unambiguous.
func (s *AccessService) EnsureApplicationByIDOrTags(ctx context.Context, accountID, statusID string, params ApplicationParams) (*AccessApplication, error) {
	params.Tags = uniqueNonEmptyStrings(params.Tags)
	tags, err := s.ensureApplicationTags(ctx, accountID, params.Tags)
	if err != nil {
		return nil, err
	}
	params.Tags = tags

	var existing *AccessApplication
	if statusID != "" {
		existing, err = s.client.GetAccessApplication(ctx, accountID, statusID)
		if err != nil {
			return nil, fmt.Errorf("failed to get application by status ID: %w", err)
		}
	}
	if existing == nil {
		apps, err := s.client.ListAccessApplications(ctx, accountID)
		if err != nil {
			return nil, fmt.Errorf("failed to list applications for adoption: %w", err)
		}
		for i := range apps {
			if apps[i].Domain == params.Domain && stringSliceContainsAll(apps[i].Tags, params.Tags) {
				if existing != nil {
					return nil, fmt.Errorf("multiple cfgate-tagged applications found for domain %q", params.Domain)
				}
				existing = &apps[i]
			}
		}
	}
	if existing != nil {
		if accessApplicationNeedsUpdate(existing, &params) {
			updated, err := s.client.UpdateAccessApplication(ctx, accountID, existing.ID, params)
			if err != nil {
				return nil, fmt.Errorf("failed to update application: %w", err)
			}
			return updated, nil
		}
		return existing, nil
	}
	created, err := s.client.CreateAccessApplication(ctx, accountID, params)
	if err != nil {
		return nil, fmt.Errorf("failed to create application: %w", err)
	}
	return created, nil
}

func (s *AccessService) ensureApplicationTags(ctx context.Context, accountID string, tags []string) ([]string, error) {
	desired := uniqueNonEmptyStrings(tags)
	if len(desired) == 0 {
		return nil, nil
	}

	existingTags, err := s.client.ListAccessTags(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to list access tags: %w", err)
	}
	existing := make(map[string]struct{}, len(existingTags))
	for _, tag := range existingTags {
		existing[tag.Name] = struct{}{}
	}

	ensured := make([]string, 0, len(desired))
	for _, tag := range desired {
		if _, ok := existing[tag]; ok {
			ensured = append(ensured, tag)
			continue
		}
		if _, err := s.client.CreateAccessTag(ctx, accountID, tag); err != nil {
			if strings.HasPrefix(tag, "cfgate:") && hasErrorCode(err, accessTagLimitErrorCode) {
				s.log.Info("skipping optional access application tag because Cloudflare account tag limit was reached", "tag", tag)
				continue
			}
			return nil, fmt.Errorf("failed to create access tag %q: %w", tag, err)
		}
		existing[tag] = struct{}{}
		ensured = append(ensured, tag)
	}
	return ensured, nil
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func stringSliceContainsAll(haystack, needles []string) bool {
	for _, needle := range needles {
		if !slices.Contains(haystack, needle) {
			return false
		}
	}
	return true
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
	desiredDestinations := desired.Destinations
	if len(desiredDestinations) == 0 && desired.Domain != "" {
		desiredDestinations = []string{desired.Domain}
	}
	if !stringSlicesEqual(existing.Destinations, desiredDestinations) {
		return true
	}
	if !stringSlicesEqual(existing.Tags, desired.Tags) {
		return true
	}
	if !policyLinksEqual(existing.Policies, desired.Policies) {
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

func policyLinksEqual(a, b []ApplicationPolicyLink) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}

	sa := make([]ApplicationPolicyLink, len(a))
	copy(sa, a)
	sb := make([]ApplicationPolicyLink, len(b))
	copy(sb, b)
	slices.SortFunc(sa, comparePolicyLinks)
	slices.SortFunc(sb, comparePolicyLinks)

	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

func comparePolicyLinks(x, y ApplicationPolicyLink) int {
	if c := cmp.Compare(x.Precedence, y.Precedence); c != 0 {
		return c
	}
	return cmp.Compare(x.ID, y.ID)
}

// EnsureReusablePolicy ensures an account-level reusable Access policy exists.
// If statusID is present it is preferred. Otherwise an existing reusable policy is
// adopted only when exactly one policy has the desired name.
func (s *AccessService) EnsureReusablePolicy(ctx context.Context, accountID, statusID string, desired PolicyParams) (*AccessPolicy, error) {
	if statusID != "" {
		existing, err := s.client.GetAccessPolicy(ctx, accountID, statusID)
		if err != nil {
			return nil, fmt.Errorf("failed to get reusable policy %s: %w", statusID, err)
		}
		if existing != nil {
			if accessPolicyEqual(existing, &desired) {
				return existing, nil
			}
			return s.client.UpdateAccessPolicy(ctx, accountID, existing.ID, desired)
		}
	}

	existing, err := s.findReusablePolicyByExactName(ctx, accountID, desired.Name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if accessPolicyEqual(existing, &desired) {
			return existing, nil
		}
		return s.client.UpdateAccessPolicy(ctx, accountID, existing.ID, desired)
	}

	return s.client.CreateAccessPolicy(ctx, accountID, desired)
}

func (s *AccessService) findReusablePolicyByExactName(ctx context.Context, accountID, name string) (*AccessPolicy, error) {
	policies, err := s.client.ListAccessPolicies(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to list reusable policies: %w", err)
	}
	var matches []AccessPolicy
	for _, policy := range policies {
		if policy.Name == name {
			matches = append(matches, policy)
		}
	}
	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		return &matches[0], nil
	default:
		return nil, fmt.Errorf("found %d reusable policies named %q; refusing ambiguous adoption", len(matches), name)
	}
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

// Client returns the underlying Cloudflare client.
// Used for direct API operations not wrapped by AccessService.
func (s *AccessService) Client() Client {
	return s.client
}

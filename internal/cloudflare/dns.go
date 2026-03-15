package cloudflare

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	"golang.org/x/net/publicsuffix"
)

// Error code constants for DNS operations.
const (
	ErrCodeRecordAlreadyExists   = 81053 // A/AAAA/CNAME already exists with that host
	ErrCodeIdenticalRecordExists = 81058 // Identical record already exists
	ErrCodeRecordNotFound        = 81044 // Record does not exist
	ErrCodeInvalidRequestBody    = 9207  // Malformed request
	ErrCodeCrossAccountCNAME     = 1014  // Cross-account CNAME error
)

// DNSPolicy represents the DNS record lifecycle policy.
type DNSPolicy string

const (
	// PolicySync enables full lifecycle management: create, update, and delete.
	PolicySync DNSPolicy = "sync"
	// PolicyUpsertOnly enables create and update, but never delete.
	PolicyUpsertOnly DNSPolicy = "upsert-only"
	// PolicyCreateOnly enables initial creation only, no updates or deletes.
	PolicyCreateOnly DNSPolicy = "create-only"
)

// OwnershipParams contains parameters for creating ownership records.
type OwnershipParams struct {
	// Hostname is the DNS hostname being tracked (e.g., "app.example.com").
	Hostname string
	// OwnerID is the controller instance identifier (e.g., "cluster-a").
	OwnerID string
	// Resource is the Kubernetes resource reference (e.g., "httproute/default/api-route").
	Resource string
	// Prefix is the TXT record name prefix (e.g., "_cfgate").
	Prefix string
}

// OwnershipMetadata represents parsed ownership information from a TXT record.
type OwnershipMetadata struct {
	// Heritage identifies the managing system ("cfgate").
	Heritage string
	// OwnerID is the controller instance identifier.
	OwnerID string
	// Resource is the Kubernetes resource reference.
	Resource string
}

// PolicyChecker validates operations against DNS policy.
type PolicyChecker struct {
	policy DNSPolicy
	log    logr.Logger
}

// AllowsCreate returns true if the policy allows record creation.
func (p *PolicyChecker) AllowsCreate() bool {
	return true // All policies allow create
}

// AllowsUpdate returns true if the policy allows record updates.
func (p *PolicyChecker) AllowsUpdate() bool {
	return p.policy != PolicyCreateOnly
}

// AllowsDelete returns true if the policy allows record deletion.
func (p *PolicyChecker) AllowsDelete() bool {
	return p.policy == PolicySync
}

// DNSRecordCache provides per-reconcile caching for DNS record lookups.
// Create a new cache at the start of each reconcile and pass it through
// to avoid duplicate remote reads within a single reconcile cycle.
type DNSRecordCache struct {
	entries map[string]*DNSRecord
}

// NewDNSRecordCache creates a new empty record cache.
func NewDNSRecordCache() *DNSRecordCache {
	return &DNSRecordCache{entries: make(map[string]*DNSRecord)}
}

func (c *DNSRecordCache) key(zoneID, name, recordType string) string {
	return zoneID + ":" + name + ":" + recordType
}

// Get retrieves a cached record. Returns the record and whether it was found in cache.
// A nil record with found=true means a previous lookup returned no results.
func (c *DNSRecordCache) Get(zoneID, name, recordType string) (*DNSRecord, bool) {
	r, ok := c.entries[c.key(zoneID, name, recordType)]
	return r, ok
}

// Set stores a record in the cache.
func (c *DNSRecordCache) Set(zoneID, name, recordType string, record *DNSRecord) {
	c.entries[c.key(zoneID, name, recordType)] = record
}

// DNSService handles DNS record operations including sync, ownership tracking,
// and policy-based lifecycle management. It wraps the Client interface with
// cfgate-specific logic for idempotent record sync and external-dns compatible ownership.
type DNSService struct {
	client Client
	log    logr.Logger
	cache  *DNSRecordCache
}

// NewDNSService creates a new DNSService with the given client and logger.
// The logger is named "dns-service" for structured logging context.
func NewDNSService(client Client, log logr.Logger) *DNSService {
	return &DNSService{
		client: client,
		log:    log.WithName("dns-service"),
	}
}

// WithCache returns a copy of the DNSService that uses the given record cache.
// The cache is scoped to a single reconcile cycle to avoid stale data across reconciles.
func (s *DNSService) WithCache(cache *DNSRecordCache) *DNSService {
	return &DNSService{
		client: s.client,
		log:    s.log,
		cache:  cache,
	}
}

// SyncRecord ensures a DNS record exists with the desired configuration.
// Creates the record if it doesn't exist, updates it if it differs.
// Respects ownership - will NOT update records not owned by cfgate.
// Returns the record, whether it was modified, and any error.
func (s *DNSService) SyncRecord(ctx context.Context, zoneID string, desired DNSRecord, ownerID string) (*DNSRecord, bool, error) {
	// Find existing record
	existing, err := s.FindRecordByName(ctx, zoneID, desired.Name, desired.Type)
	if err != nil {
		return nil, false, fmt.Errorf("failed to find existing record: %w", err)
	}

	// Create if doesn't exist
	if existing == nil {
		record, err := s.client.CreateDNSRecord(ctx, zoneID, desired)
		if err != nil {
			// Handle duplicate error (race condition)
			if IsDuplicateRecordError(err) {
				s.log.V(1).Info("record created by another process, fetching",
					"name", desired.Name, "type", desired.Type)
				found, findErr := s.FindRecordByName(ctx, zoneID, desired.Name, desired.Type)
				if findErr != nil {
					return nil, false, fmt.Errorf("failed to find record after duplicate error: %w", findErr)
				}
				return found, false, nil
			}
			return nil, false, fmt.Errorf("failed to create DNS record: %w", err)
		}
		s.log.Info("created DNS record", "name", desired.Name, "type", desired.Type, "id", record.ID)
		return record, true, nil
	}

	// Check ownership before updating - only update records we own
	if !IsOwnedByCfgate(existing, ownerID) {
		s.log.V(1).Info("record not owned by cfgate, skipping update",
			"name", existing.Name, "owner", ownerID)
		return existing, false, nil
	}

	// Check if update needed
	if recordsMatch(existing, &desired) {
		return existing, false, nil
	}

	// Update existing record (we own it)
	record, err := s.client.UpdateDNSRecord(ctx, zoneID, existing.ID, desired)
	if err != nil {
		return nil, false, fmt.Errorf("failed to update DNS record: %w", err)
	}
	s.log.Info("updated DNS record", "name", desired.Name, "type", desired.Type, "id", record.ID)

	return record, true, nil
}

// SyncRecordWithPolicy ensures a DNS record exists with policy-based lifecycle management.
func (s *DNSService) SyncRecordWithPolicy(ctx context.Context, zoneID string, desired DNSRecord, ownerID string, policy DNSPolicy) (*DNSRecord, bool, error) {
	existing, err := s.FindRecordByName(ctx, zoneID, desired.Name, desired.Type)
	if err != nil {
		return nil, false, fmt.Errorf("failed to find existing record: %w", err)
	}

	checker := &PolicyChecker{policy: policy, log: s.log}

	if existing == nil {
		if !checker.AllowsCreate() {
			return nil, false, nil // Should never happen, all policies allow create
		}
		return s.createRecord(ctx, zoneID, desired)
	}

	if !IsOwnedByCfgate(existing, ownerID) {
		return existing, false, nil
	}

	if ownerID != "" && isLegacyCommentOwnership(existing) {
		s.log.V(1).Info("record matched via legacy comment ownership, consider enabling TXT ownership for multi-cluster safety",
			"name", existing.Name, "type", existing.Type)
	}

	if recordsMatch(existing, &desired) {
		return existing, false, nil
	}

	if !checker.AllowsUpdate() {
		s.log.Info("skipping update due to policy",
			"policy", policy, "hostname", desired.Name)
		return existing, false, nil
	}

	return s.updateRecord(ctx, zoneID, existing.ID, desired)
}

// createRecord creates a DNS record with duplicate error handling.
func (s *DNSService) createRecord(ctx context.Context, zoneID string, desired DNSRecord) (*DNSRecord, bool, error) {
	record, err := s.client.CreateDNSRecord(ctx, zoneID, desired)
	if err != nil {
		if IsDuplicateRecordError(err) {
			s.log.V(1).Info("record created by another process, fetching",
				"name", desired.Name, "type", desired.Type)
			found, findErr := s.FindRecordByName(ctx, zoneID, desired.Name, desired.Type)
			if findErr != nil {
				return nil, false, fmt.Errorf("failed to find record after duplicate error: %w", findErr)
			}
			return found, false, nil
		}
		return nil, false, fmt.Errorf("failed to create DNS record: %w", err)
	}
	s.log.Info("created DNS record", "name", desired.Name, "type", desired.Type, "id", record.ID)
	return record, true, nil
}

// updateRecord updates a DNS record.
func (s *DNSService) updateRecord(ctx context.Context, zoneID, recordID string, desired DNSRecord) (*DNSRecord, bool, error) {
	record, err := s.client.UpdateDNSRecord(ctx, zoneID, recordID, desired)
	if err != nil {
		return nil, false, fmt.Errorf("failed to update DNS record: %w", err)
	}
	s.log.Info("updated DNS record", "name", desired.Name, "type", desired.Type, "id", record.ID)
	return record, true, nil
}

// recordsMatch checks if two records have identical content, proxied, TTL, and comment.
// Name and Type are assumed to match from the lookup.
func recordsMatch(a, b *DNSRecord) bool {
	return a.Content == b.Content &&
		a.Proxied == b.Proxied &&
		a.TTL == b.TTL &&
		a.Comment == b.Comment
}

// DeleteRecord deletes a DNS record by ID with idempotent handling.
func (s *DNSService) DeleteRecord(ctx context.Context, zoneID, recordID string) error {
	err := s.client.DeleteDNSRecord(ctx, zoneID, recordID)
	if err != nil {
		if IsRecordNotFoundError(err) {
			return nil // Already deleted
		}
		return fmt.Errorf("failed to delete DNS record: %w", err)
	}
	s.log.Info("deleted DNS record", "id", recordID)
	return nil
}

// DeleteRecordWithPolicy deletes a DNS record respecting the policy.
func (s *DNSService) DeleteRecordWithPolicy(ctx context.Context, zoneID, recordID string, policy DNSPolicy) error {
	checker := &PolicyChecker{policy: policy, log: s.log}
	if !checker.AllowsDelete() {
		s.log.Info("skipping delete due to policy", "policy", policy, "recordID", recordID)
		return nil
	}
	return s.DeleteRecord(ctx, zoneID, recordID)
}

// FindRecordByName finds a DNS record by name and type using targeted API lookup.
// Uses the reconcile-local cache when available to avoid duplicate remote reads.
// Returns nil if not found.
func (s *DNSService) FindRecordByName(ctx context.Context, zoneID, name, recordType string) (*DNSRecord, error) {
	if s.cache != nil {
		if cached, ok := s.cache.Get(zoneID, name, recordType); ok {
			s.log.V(1).Info("dns record cache hit", "zone", zoneID, "name", name, "type", recordType)
			return cached, nil
		}
	}

	records, err := s.client.ListDNSRecordsByNameType(ctx, zoneID, name, recordType)
	if err != nil {
		return nil, fmt.Errorf("failed to find DNS record: %w", err)
	}

	var result *DNSRecord
	for _, record := range records {
		if record.Name == name && record.Type == recordType {
			recordCopy := record
			result = &recordCopy
			break
		}
	}

	if s.cache != nil {
		s.cache.Set(zoneID, name, recordType, result)
	}

	return result, nil
}

// ListManagedRecords lists DNS records managed by cfgate for the given owner.
// When ownershipPrefix is non-empty and ownerID is non-empty, non-TXT records
// matched only by comment ("managed by cfgate") are cross-referenced against
// TXT ownership records. If a TXT record exists for the hostname but belongs to
// a different owner, the non-TXT record is excluded. This prevents one
// CloudflareDNS resource's cleanup from deleting another resource's records.
func (s *DNSService) ListManagedRecords(ctx context.Context, zoneID, ownerID, ownershipPrefix string) ([]DNSRecord, error) {
	records, err := s.client.ListDNSRecords(ctx, zoneID)
	if err != nil {
		return nil, fmt.Errorf("failed to list DNS records: %w", err)
	}

	// Build TXT ownership index: hostname -> ownerID from all cfgate TXT records
	// in the zone, regardless of owner. Used to cross-reference non-TXT records.
	txtOwnerByHostname := make(map[string]string)
	if ownerID != "" && ownershipPrefix != "" {
		prefix := ownershipPrefix + "."
		for _, record := range records {
			if record.Type == "TXT" && strings.HasPrefix(record.Content, "heritage=cfgate") && strings.HasPrefix(record.Name, prefix) {
				hostname := strings.TrimPrefix(record.Name, prefix)
				meta, _ := ParseOwnershipRecord(record.Content)
				if meta != nil && meta.OwnerID != "" {
					txtOwnerByHostname[hostname] = meta.OwnerID
				}
			}
		}
	}

	var managed []DNSRecord
	for _, record := range records {
		if !IsOwnedByCfgate(&record, ownerID) {
			continue
		}

		// Cross-reference: non-TXT records matched by comment fallback need TXT
		// verification to prevent cross-resource deletion. If a TXT ownership
		// record exists for this hostname but belongs to a different owner, skip.
		if ownerID != "" && ownershipPrefix != "" && isLegacyCommentOwnership(&record) {
			if txtOwner, hasTXT := txtOwnerByHostname[record.Name]; hasTXT && txtOwner != ownerID {
				s.log.V(1).Info("skipping record owned by different resource",
					"record", record.Name,
					"txtOwner", txtOwner,
					"requestedOwner", ownerID,
				)
				continue
			}
		}

		managed = append(managed, record)
	}

	return managed, nil
}

// CreateOwnershipRecord creates or updates a TXT record for ownership tracking.
// Uses upsert pattern: checks if record exists before creating to avoid duplicate errors.
func (s *DNSService) CreateOwnershipRecord(ctx context.Context, zoneID string, params OwnershipParams) error {
	record := BuildOwnershipTXTRecord(params.Hostname, params.OwnerID, params.Resource, params.Prefix)

	// Check if ownership record already exists
	existing, err := s.FindRecordByName(ctx, zoneID, record.Name, record.Type)
	if err != nil {
		return fmt.Errorf("failed to check existing ownership record: %w", err)
	}

	if existing != nil {
		// Record exists - check if update needed
		if existing.Content == record.Content && existing.Comment == record.Comment {
			return nil // Already up to date
		}
		// Update existing record
		_, err := s.client.UpdateDNSRecord(ctx, zoneID, existing.ID, record)
		if err != nil {
			return fmt.Errorf("failed to update ownership record: %w", err)
		}
		s.log.V(1).Info("updated ownership record", "hostname", params.Hostname)
		return nil
	}

	// Create new record
	_, err = s.client.CreateDNSRecord(ctx, zoneID, record)
	if err != nil {
		// Handle duplicate error (race condition)
		if IsDuplicateRecordError(err) {
			s.log.V(1).Info("ownership record created by another process", "hostname", params.Hostname)
			return nil
		}
		return fmt.Errorf("failed to create ownership record: %w", err)
	}
	s.log.V(1).Info("created ownership record", "hostname", params.Hostname)
	return nil
}

// DeleteOwnershipRecord deletes the TXT record for ownership tracking.
func (s *DNSService) DeleteOwnershipRecord(ctx context.Context, zoneID, hostname, prefix string) error {
	txtName := fmt.Sprintf("%s.%s", prefix, hostname)
	record, err := s.FindRecordByName(ctx, zoneID, txtName, "TXT")
	if err != nil {
		return fmt.Errorf("failed to find ownership record: %w", err)
	}

	if record == nil {
		return nil // Already deleted
	}

	return s.DeleteRecord(ctx, zoneID, record.ID)
}

// ResolveZone resolves a zone name to a Zone.
// Returns nil if the zone doesn't exist or isn't accessible.
func (s *DNSService) ResolveZone(ctx context.Context, zoneName string) (*Zone, error) {
	return s.client.GetZoneByName(ctx, zoneName)
}

// ExtractZoneFromHostname extracts the zone name from a hostname using public suffix list.
// Handles complex TLDs correctly (e.g., .co.uk, .com.au).
func ExtractZoneFromHostname(hostname string) string {
	// Use public suffix list for correct TLD handling
	etld, err := publicsuffix.EffectiveTLDPlusOne(hostname)
	if err != nil {
		// Fallback to simple heuristic for non-standard hostnames
		parts := strings.Split(hostname, ".")
		if len(parts) < 2 {
			return hostname
		}
		return strings.Join(parts[len(parts)-2:], ".")
	}
	return etld
}

// ValidateTTL validates a TTL value before API calls.
// Valid values: 1 (auto) or 60-86400 seconds.
func ValidateTTL(ttl int) error {
	if ttl == 1 {
		return nil // Auto TTL (300 seconds)
	}
	if ttl < 60 || ttl > 86400 {
		return fmt.Errorf("TTL must be 1 (auto) or between 60 and 86400 seconds, got %d", ttl)
	}
	return nil
}

// BuildDNSRecord builds a DNS record with the specified type (CNAME, A, or AAAA).
// If ttl is zero or negative, it defaults to 1 (Cloudflare auto TTL, resolves to 300s).
func BuildDNSRecord(hostname, target, recordType string, proxied bool, ttl int, comment string) DNSRecord {
	if ttl <= 0 {
		ttl = 1
	}
	return DNSRecord{
		Type:    recordType,
		Name:    hostname,
		Content: target,
		TTL:     ttl,
		Proxied: proxied,
		Comment: comment,
	}
}

// BuildCNAMERecord builds a CNAME record for a tunnel.
// Delegates to BuildDNSRecord with recordType "CNAME".
func BuildCNAMERecord(hostname, tunnelDomain string, proxied bool, ttl int, comment string) DNSRecord {
	return BuildDNSRecord(hostname, tunnelDomain, "CNAME", proxied, ttl, comment)
}

// ValidateHostnameDepth reports whether hostname has more than one subdomain level
// relative to zoneName. Cloudflare Universal SSL covers only single-level wildcards
// (*.example.com); deeper subdomains (e.g., deep.sub.example.com) require Advanced
// Certificate Manager or a paid TLS tier.
func ValidateHostnameDepth(hostname, zoneName string) bool {
	suffix := "." + zoneName
	if !strings.HasSuffix(hostname, suffix) {
		return false
	}
	subdomain := strings.TrimSuffix(hostname, suffix)
	return strings.Contains(subdomain, ".")
}

// BuildOwnershipTXTRecord builds a TXT record for ownership tracking using external-dns aligned format.
func BuildOwnershipTXTRecord(hostname, ownerID, resource, prefix string) DNSRecord {
	content := fmt.Sprintf("heritage=cfgate,cfgate/owner=%s,cfgate/resource=%s", ownerID, resource)
	return DNSRecord{
		Type:    "TXT",
		Name:    fmt.Sprintf("%s.%s", prefix, hostname),
		Content: content,
		TTL:     1, // Auto TTL
		Proxied: false,
		Comment: "cfgate ownership record",
	}
}

// IsOwnedByCfgate checks if a DNS record is managed by cfgate.
// TXT ownership records (heritage=cfgate in Content) are the authoritative ownership
// mechanism with ownerID verification for multi-cluster safety. When ownerID is provided,
// the content is split on commas and the cfgate/owner field must match exactly to prevent
// substring false-positives (e.g., "ns/foo" must not match "ns/foobar").
// Comment-based detection ("managed by cfgate") is a cosmetic fallback that matches
// any cfgate installation without owner discrimination.
func IsOwnedByCfgate(record *DNSRecord, ownerID string) bool {
	if record == nil {
		return false
	}

	// TXT ownership record: heritage=cfgate with ownerID verification
	if strings.HasPrefix(record.Content, "heritage=cfgate") {
		if ownerID == "" {
			return true
		}
		// Exact field match: split comma-delimited content and compare the full field.
		ownerField := fmt.Sprintf("cfgate/owner=%s", ownerID)
		for _, field := range strings.Split(record.Content, ",") {
			if field == ownerField {
				return true
			}
		}
		return false
	}

	// Comment-based detection: cosmetic signal only, no owner verification.
	// Any record with "managed by cfgate" in the comment is treated as ours.
	// For multi-cluster owner discrimination, enable TXT ownership records.
	return strings.Contains(record.Comment, "managed by cfgate")
}

// isLegacyCommentOwnership returns true if the record relies on comment-only
// ownership (no companion TXT record). These records lack multi-cluster owner
// discrimination. The warning in SyncRecordWithPolicy recommends enabling TXT ownership.
func isLegacyCommentOwnership(record *DNSRecord) bool {
	if record == nil {
		return false
	}
	return !strings.HasPrefix(record.Content, "heritage=cfgate") &&
		strings.Contains(record.Comment, "managed by cfgate")
}

// ParseOwnershipRecord parses ownership metadata from TXT record content.
func ParseOwnershipRecord(content string) (*OwnershipMetadata, error) {
	// Alpha.3 format: heritage=cfgate,cfgate/owner=X,cfgate/resource=Y
	if strings.HasPrefix(content, "heritage=cfgate") {
		meta := &OwnershipMetadata{Heritage: "cfgate"}
		parts := strings.Split(content, ",")
		for _, part := range parts {
			if strings.HasPrefix(part, "cfgate/owner=") {
				meta.OwnerID = strings.TrimPrefix(part, "cfgate/owner=")
			}
			if strings.HasPrefix(part, "cfgate/resource=") {
				meta.Resource = strings.TrimPrefix(part, "cfgate/resource=")
			}
		}
		return meta, nil
	}

	// Alpha.2 format: managed by cfgate, tunnel=X
	if strings.Contains(content, "managed by cfgate") {
		meta := &OwnershipMetadata{Heritage: "cfgate"}
		if idx := strings.Index(content, "tunnel="); idx != -1 {
			// Extract tunnel name as resource (legacy)
			tunnelPart := content[idx+len("tunnel="):]
			if end := strings.Index(tunnelPart, ","); end != -1 {
				meta.Resource = tunnelPart[:end]
			} else {
				meta.Resource = tunnelPart
			}
		}
		return meta, nil
	}

	return nil, fmt.Errorf("unrecognized ownership format: %s", content)
}

// IsDuplicateRecordError returns true if the error indicates a duplicate record.
// Checks Cloudflare DNS error codes 81053 (already exists) and 81058 (identical exists).
func IsDuplicateRecordError(err error) bool {
	if err == nil {
		return false
	}
	return hasErrorCode(err, ErrCodeRecordAlreadyExists) || hasErrorCode(err, ErrCodeIdenticalRecordExists)
}

// IsRecordNotFoundError returns true if the error indicates a record was not found.
// Checks both the HTTP status (404) and the Cloudflare DNS error code (81044).
func IsRecordNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	if isNotFound(err) {
		return true
	}
	return hasErrorCode(err, ErrCodeRecordNotFound)
}

package annotations

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Standard errors for annotation validation.
var (
	ErrMissingHostnameAnnotation  = errors.New("missing required hostname annotation")
	ErrInvalidHostnameFormat      = errors.New("invalid hostname format")
	ErrInvalidProtocol            = errors.New("invalid origin protocol")
	ErrInvalidTTL                 = errors.New("invalid TTL value")
	ErrMissingTunnelRef           = errors.New("missing tunnel reference annotation")
	ErrInvalidTunnelRefFormat     = errors.New("invalid tunnel reference format")
	ErrInvalidNamespacedName      = errors.New("invalid namespaced name format")
	ErrEmptyNamespacedNameSegment = errors.New("empty segment in namespaced name")
)

// AnnotationPrefix is the prefix for all cfgate annotations.
const AnnotationPrefix = "cfgate.io/"

// Infrastructure-level annotations (CloudflareTunnel/Gateway).
const (
	// AnnotationTunnelTarget specifies the tunnel endpoint.
	// Example: "uuid.cfargotunnel.com"
	// Read from: Gateway, CloudflareTunnel
	AnnotationTunnelTarget = AnnotationPrefix + "tunnel-target"

	// AnnotationTunnelRef references the CloudflareTunnel resource.
	// Format: "namespace/name" or "name" (same namespace)
	// Read from: Gateway
	AnnotationTunnelRef = AnnotationPrefix + "tunnel-ref"
)

// Application-level annotations (HTTPRoute/TCPRoute/UDPRoute/GRPCRoute).
const (
	// AnnotationOriginProtocol specifies the origin protocol.
	// Values: "http", "https", "tcp", "udp"
	// Default: "http" for HTTPRoute, "tcp" for TCPRoute, "udp" for UDPRoute
	// Read from: Routes
	AnnotationOriginProtocol = AnnotationPrefix + "origin-protocol"

	// AnnotationOriginSSLVerify enables/disables SSL certificate verification.
	// Values: "true", "false"
	// Default: "true"
	// Read from: Routes
	AnnotationOriginSSLVerify = AnnotationPrefix + "origin-ssl-verify"

	// AnnotationTTL specifies the DNS record TTL in seconds.
	// Values: "1" to "86400" (1 second to 24 hours)
	// Special: "1" means "auto" (Cloudflare managed)
	// Default: "1" (auto)
	// Read from: Routes
	AnnotationTTL = AnnotationPrefix + "ttl"

	// AnnotationCloudflareProxied enables/disables Cloudflare proxy (orange cloud).
	// Values: "true", "false"
	// Default: "true"
	// Read from: Routes
	AnnotationCloudflareProxied = AnnotationPrefix + "cloudflare-proxied"

	// AnnotationAccessPolicy references a CloudflareAccessPolicy.
	// Values: "name" (same namespace) or "namespace/name"
	// Read from: Routes
	AnnotationAccessPolicy = AnnotationPrefix + "access-policy"

	// AnnotationHostname specifies the hostname for routes without spec.hostnames.
	// REQUIRED for TCPRoute and UDPRoute (Gateway API has no hostnames field for these).
	// Optional for HTTPRoute/GRPCRoute (overrides spec.hostnames).
	// Values: RFC 1123 hostname
	// Read from: Routes
	AnnotationHostname = AnnotationPrefix + "hostname"
)

// Origin configuration annotations (processed by cloudflared-builder).
const (
	// AnnotationOriginConnectTimeout specifies origin connection timeout.
	// Values: duration string (e.g., "30s", "1m")
	// Default: "30s"
	AnnotationOriginConnectTimeout = AnnotationPrefix + "origin-connect-timeout"

	// AnnotationOriginHTTPHostHeader overrides the Host header sent to origin.
	// Values: hostname string
	AnnotationOriginHTTPHostHeader = AnnotationPrefix + "origin-http-host-header"

	// AnnotationOriginServerName specifies the TLS SNI server name.
	// Values: hostname string
	AnnotationOriginServerName = AnnotationPrefix + "origin-server-name"

	// AnnotationOriginCAPool specifies path to CA certificate pool.
	// Values: file path string
	AnnotationOriginCAPool = AnnotationPrefix + "origin-ca-pool"

	// AnnotationOriginHTTP2 enables HTTP/2 to origin.
	// Values: "true", "false"
	// Default: "false"
	AnnotationOriginHTTP2 = AnnotationPrefix + "origin-http2"

	// AnnotationOriginH2c enables HTTP/2 cleartext (h2c) to origin.
	// Values: "true", "false"
	// Default: "false"
	AnnotationOriginH2c = AnnotationPrefix + "origin-h2c"
)

// Validation constants.
const (
	// MaxTTL is the maximum allowed TTL value in seconds.
	MaxTTL = 86400

	// MinTTL is the minimum allowed TTL value in seconds.
	// Value 1 is special (Cloudflare auto TTL).
	MinTTL = 1

	// MaxHostnameLength is the maximum length of a hostname per RFC 1123.
	MaxHostnameLength = 253

	// MaxLabelLength is the maximum length of a DNS label per RFC 1123.
	MaxLabelLength = 63
)

// ValidOriginProtocols defines the allowed origin protocol values.
var ValidOriginProtocols = map[string]bool{
	"http":  true,
	"https": true,
	"tcp":   true,
	"udp":   true,
}

// hostnameRegex validates RFC 1123 hostnames.
// - Lowercase alphanumeric characters and hyphens
// - Labels separated by dots
// - Each label max 63 characters
// - Total max 253 characters
var hostnameRegex = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

// --- Parsing Utilities ---

// GetAnnotation retrieves an annotation value from an object's metadata.
// Returns empty string if annotation not present or object is nil.
// Works with any client.Object (HTTPRoute, TCPRoute, Gateway, etc.).
func GetAnnotation(obj client.Object, key string) string {
	if obj == nil {
		return ""
	}
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return ""
	}
	return annotations[key]
}

// GetAnnotationBool parses a boolean annotation value.
// Returns defaultValue if annotation not present or not a valid boolean.
// Accepts: "true", "false", "1", "0", "yes", "no" (case-insensitive)
func GetAnnotationBool(obj client.Object, key string, defaultValue bool) bool {
	value := GetAnnotation(obj, key)
	if value == "" {
		return defaultValue
	}

	switch strings.ToLower(value) {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		return defaultValue
	}
}

// GetAnnotationInt parses an integer annotation value.
// Returns defaultValue if annotation not present or not a valid integer.
func GetAnnotationInt(obj client.Object, key string, defaultValue int) int {
	value := GetAnnotation(obj, key)
	if value == "" {
		return defaultValue
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return defaultValue
	}
	return parsed
}

// GetAnnotationDuration parses a duration annotation value.
// Returns defaultValue if annotation not present or not a valid duration.
// Accepts Go duration strings: "30s", "5m", "1h30m"
func GetAnnotationDuration(obj client.Object, key string, defaultValue time.Duration) time.Duration {
	value := GetAnnotation(obj, key)
	if value == "" {
		return defaultValue
	}

	parsed, err := time.ParseDuration(value)
	if err != nil {
		return defaultValue
	}
	return parsed
}

// ParseNamespacedName parses a reference in "namespace/name" or "name" format.
// Returns (namespace, name, nil) on success.
// If ref contains no slash, defaultNS is used as the namespace.
// Returns an error if ref is empty, contains more than one slash, or has empty segments.
func ParseNamespacedName(ref string, defaultNS string) (string, string, error) {
	if ref == "" {
		return "", "", fmt.Errorf("%w: reference is empty", ErrInvalidNamespacedName)
	}

	parts := strings.Split(ref, "/")
	switch len(parts) {
	case 1:
		if parts[0] == "" {
			return "", "", fmt.Errorf("%w: name is empty", ErrEmptyNamespacedNameSegment)
		}
		return defaultNS, parts[0], nil
	case 2:
		if parts[0] == "" {
			return "", "", fmt.Errorf("%w: namespace is empty in %q", ErrEmptyNamespacedNameSegment, ref)
		}
		if parts[1] == "" {
			return "", "", fmt.Errorf("%w: name is empty in %q", ErrEmptyNamespacedNameSegment, ref)
		}
		return parts[0], parts[1], nil
	default:
		return "", "", fmt.Errorf("%w: expected \"namespace/name\" or \"name\", got %q", ErrInvalidNamespacedName, ref)
	}
}

// --- Validation Functions ---

// ValidateOriginProtocol validates the origin protocol annotation value.
// Returns error if value is not a valid protocol.
// Empty value is valid (will use default).
func ValidateOriginProtocol(value string) error {
	if value == "" {
		return nil // Empty is valid (will use default)
	}
	if !ValidOriginProtocols[strings.ToLower(value)] {
		return fmt.Errorf("invalid origin protocol %q: must be one of http, https, tcp, udp", value)
	}
	return nil
}

// ValidateTTL validates the TTL annotation value.
// Returns error if value is not a valid TTL (1-86400 seconds).
// Empty value is valid (will use default).
func ValidateTTL(value string) error {
	if value == "" {
		return nil // Empty is valid (will use default)
	}

	ttl, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("invalid TTL %q: must be an integer", value)
	}

	if ttl < MinTTL || ttl > MaxTTL {
		return fmt.Errorf("invalid TTL %d: must be between %d and %d", ttl, MinTTL, MaxTTL)
	}

	return nil
}

// ValidateHostname validates a hostname annotation value.
// Returns error if hostname format is invalid per RFC 1123.
// Empty value returns an error (use requireHostname=false in ValidateRouteAnnotations
// to allow empty hostnames).
func ValidateHostname(value string) error {
	if value == "" {
		return fmt.Errorf("hostname cannot be empty")
	}

	if len(value) > MaxHostnameLength {
		return fmt.Errorf("hostname %q exceeds maximum length of %d characters", value, MaxHostnameLength)
	}

	// Check each label length
	labels := strings.Split(value, ".")
	for _, label := range labels {
		if len(label) > MaxLabelLength {
			return fmt.Errorf("hostname label %q exceeds maximum length of %d characters", label, MaxLabelLength)
		}
	}

	// Normalize to lowercase for validation
	normalized := strings.ToLower(value)
	if !hostnameRegex.MatchString(normalized) {
		return fmt.Errorf("hostname %q is not a valid RFC 1123 hostname", value)
	}

	return nil
}

// --- Validation Result ---

// ValidationResult holds the result of annotation validation.
type ValidationResult struct {
	// Valid indicates whether all validations passed.
	Valid bool

	// Errors contains validation error messages.
	Errors []string

	// Warnings contains non-fatal warning messages (e.g., deprecated annotations).
	Warnings []string
}

// ValidateRouteAnnotations validates all cfgate annotations on a route object.
// Set requireHostname=true for TCPRoute/UDPRoute (no spec.hostnames in Gateway API).
// Returns validation result with errors and warnings.
func ValidateRouteAnnotations(obj client.Object, requireHostname bool) ValidationResult {
	result := ValidationResult{Valid: true}

	// Validate origin protocol
	if protocol := GetAnnotation(obj, AnnotationOriginProtocol); protocol != "" {
		if err := ValidateOriginProtocol(protocol); err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, err.Error())
		}
	}

	// Validate TTL
	if ttl := GetAnnotation(obj, AnnotationTTL); ttl != "" {
		if err := ValidateTTL(ttl); err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, err.Error())
		}
	}

	// Validate hostname
	hostname := GetAnnotation(obj, AnnotationHostname)
	if requireHostname {
		if hostname == "" {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("annotation %s is required", AnnotationHostname))
		} else if err := ValidateHostname(hostname); err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, err.Error())
		}
	} else if hostname != "" {
		if err := ValidateHostname(hostname); err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, err.Error())
		}
	}

	return result
}

// --- Origin Configuration ---

// OriginConfig represents the parsed origin configuration from route annotations.
type OriginConfig struct {
	// Protocol is the origin protocol (http, https, tcp, udp).
	Protocol string

	// SSLVerify enables/disables SSL certificate verification.
	SSLVerify bool

	// ConnectTimeout is the origin connection timeout.
	ConnectTimeout time.Duration

	// HTTPHostHeader overrides the Host header sent to origin.
	HTTPHostHeader string

	// ServerName specifies the TLS SNI server name.
	ServerName string

	// CAPool specifies path to CA certificate pool.
	CAPool string

	// HTTP2 enables HTTP/2 to origin.
	HTTP2 bool

	// H2c enables HTTP/2 cleartext (h2c) to origin.
	H2c bool
}

// ParseOriginConfig extracts origin configuration from route annotations.
// Uses sensible defaults when annotations are not present.
func ParseOriginConfig(obj client.Object, defaultProtocol string) OriginConfig {
	config := OriginConfig{
		Protocol:       GetAnnotation(obj, AnnotationOriginProtocol),
		SSLVerify:      GetAnnotationBool(obj, AnnotationOriginSSLVerify, true),
		ConnectTimeout: GetAnnotationDuration(obj, AnnotationOriginConnectTimeout, 30*time.Second),
		HTTPHostHeader: GetAnnotation(obj, AnnotationOriginHTTPHostHeader),
		ServerName:     GetAnnotation(obj, AnnotationOriginServerName),
		CAPool:         GetAnnotation(obj, AnnotationOriginCAPool),
		HTTP2:          GetAnnotationBool(obj, AnnotationOriginHTTP2, false),
		H2c:            GetAnnotationBool(obj, AnnotationOriginH2c, false),
	}

	// Apply default protocol if not specified
	if config.Protocol == "" {
		config.Protocol = defaultProtocol
	}

	return config
}

// --- DNS Configuration ---

// DNSConfig represents the parsed DNS configuration from route annotations.
type DNSConfig struct {
	// TTL is the DNS record TTL in seconds.
	// Value 1 means Cloudflare auto TTL.
	TTL int

	// Proxied enables Cloudflare proxy (orange cloud).
	Proxied bool
}

// ParseDNSConfig extracts DNS configuration from route annotations.
func ParseDNSConfig(obj client.Object) DNSConfig {
	return DNSConfig{
		TTL:     GetAnnotationInt(obj, AnnotationTTL, 1),
		Proxied: GetAnnotationBool(obj, AnnotationCloudflareProxied, true),
	}
}

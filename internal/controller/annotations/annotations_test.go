package annotations

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// fakeObject implements client.Object for testing.
// We only need the metav1.Object interface methods for annotation tests.
type fakeObject struct {
	metav1.TypeMeta
	metav1.ObjectMeta
}

// Implement runtime.Object interface
func (f *fakeObject) DeepCopyObject() runtime.Object { return f }

func newFakeObject(annotations map[string]string) *fakeObject {
	return &fakeObject{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-object",
			Namespace:   "default",
			Annotations: annotations,
		},
	}
}

func TestGetAnnotation(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		key         string
		want        string
	}{
		{
			name:        "annotation exists",
			annotations: map[string]string{AnnotationTTL: "300"},
			key:         AnnotationTTL,
			want:        "300",
		},
		{
			name:        "annotation missing",
			annotations: map[string]string{},
			key:         AnnotationTTL,
			want:        "",
		},
		{
			name:        "nil annotations",
			annotations: nil,
			key:         AnnotationTTL,
			want:        "",
		},
		{
			name:        "different annotation exists",
			annotations: map[string]string{AnnotationOriginProtocol: "https"},
			key:         AnnotationTTL,
			want:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := newFakeObject(tt.annotations)
			got := GetAnnotation(obj, tt.key)
			if got != tt.want {
				t.Errorf("GetAnnotation() = %q, want %q", got, tt.want)
			}
		})
	}

	// Test nil object
	t.Run("nil object", func(t *testing.T) {
		got := GetAnnotation(nil, AnnotationTTL)
		if got != "" {
			t.Errorf("GetAnnotation(nil) = %q, want empty", got)
		}
	})
}

func TestGetAnnotationBool(t *testing.T) {
	tests := []struct {
		name         string
		annotations  map[string]string
		key          string
		defaultValue bool
		want         bool
	}{
		{"true", map[string]string{AnnotationCloudflareProxied: "true"}, AnnotationCloudflareProxied, false, true},
		{"false", map[string]string{AnnotationCloudflareProxied: "false"}, AnnotationCloudflareProxied, true, false},
		{"yes", map[string]string{AnnotationCloudflareProxied: "yes"}, AnnotationCloudflareProxied, false, true},
		{"no", map[string]string{AnnotationCloudflareProxied: "no"}, AnnotationCloudflareProxied, true, false},
		{"1", map[string]string{AnnotationCloudflareProxied: "1"}, AnnotationCloudflareProxied, false, true},
		{"0", map[string]string{AnnotationCloudflareProxied: "0"}, AnnotationCloudflareProxied, true, false},
		{"TRUE", map[string]string{AnnotationCloudflareProxied: "TRUE"}, AnnotationCloudflareProxied, false, true},
		{"FALSE", map[string]string{AnnotationCloudflareProxied: "FALSE"}, AnnotationCloudflareProxied, true, false},
		{"Yes", map[string]string{AnnotationCloudflareProxied: "Yes"}, AnnotationCloudflareProxied, false, true},
		{"No", map[string]string{AnnotationCloudflareProxied: "No"}, AnnotationCloudflareProxied, true, false},
		{"missing uses default true", nil, AnnotationCloudflareProxied, true, true},
		{"missing uses default false", nil, AnnotationCloudflareProxied, false, false},
		{"invalid uses default", map[string]string{AnnotationCloudflareProxied: "invalid"}, AnnotationCloudflareProxied, true, true},
		{"empty uses default", map[string]string{AnnotationCloudflareProxied: ""}, AnnotationCloudflareProxied, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := newFakeObject(tt.annotations)
			got := GetAnnotationBool(obj, tt.key, tt.defaultValue)
			if got != tt.want {
				t.Errorf("GetAnnotationBool() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetAnnotationInt(t *testing.T) {
	tests := []struct {
		name         string
		annotations  map[string]string
		key          string
		defaultValue int
		want         int
	}{
		{"valid int", map[string]string{AnnotationTTL: "300"}, AnnotationTTL, 1, 300},
		{"zero", map[string]string{AnnotationTTL: "0"}, AnnotationTTL, 1, 0},
		{"negative int", map[string]string{AnnotationTTL: "-1"}, AnnotationTTL, 1, -1},
		{"large int", map[string]string{AnnotationTTL: "86400"}, AnnotationTTL, 1, 86400},
		{"missing uses default", nil, AnnotationTTL, 1, 1},
		{"empty uses default", map[string]string{AnnotationTTL: ""}, AnnotationTTL, 1, 1},
		{"invalid uses default", map[string]string{AnnotationTTL: "invalid"}, AnnotationTTL, 1, 1},
		{"float uses default", map[string]string{AnnotationTTL: "1.5"}, AnnotationTTL, 1, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := newFakeObject(tt.annotations)
			got := GetAnnotationInt(obj, tt.key, tt.defaultValue)
			if got != tt.want {
				t.Errorf("GetAnnotationInt() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestGetAnnotationDuration(t *testing.T) {
	tests := []struct {
		name         string
		annotations  map[string]string
		key          string
		defaultValue time.Duration
		want         time.Duration
	}{
		{"30s", map[string]string{AnnotationOriginConnectTimeout: "30s"}, AnnotationOriginConnectTimeout, time.Minute, 30 * time.Second},
		{"5m", map[string]string{AnnotationOriginConnectTimeout: "5m"}, AnnotationOriginConnectTimeout, time.Minute, 5 * time.Minute},
		{"1h30m", map[string]string{AnnotationOriginConnectTimeout: "1h30m"}, AnnotationOriginConnectTimeout, time.Minute, 90 * time.Minute},
		{"1h", map[string]string{AnnotationOriginConnectTimeout: "1h"}, AnnotationOriginConnectTimeout, time.Minute, time.Hour},
		{"500ms", map[string]string{AnnotationOriginConnectTimeout: "500ms"}, AnnotationOriginConnectTimeout, time.Minute, 500 * time.Millisecond},
		{"missing uses default", nil, AnnotationOriginConnectTimeout, time.Minute, time.Minute},
		{"empty uses default", map[string]string{AnnotationOriginConnectTimeout: ""}, AnnotationOriginConnectTimeout, time.Minute, time.Minute},
		{"invalid uses default", map[string]string{AnnotationOriginConnectTimeout: "invalid"}, AnnotationOriginConnectTimeout, time.Minute, time.Minute},
		{"number without unit uses default", map[string]string{AnnotationOriginConnectTimeout: "30"}, AnnotationOriginConnectTimeout, time.Minute, time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := newFakeObject(tt.annotations)
			got := GetAnnotationDuration(obj, tt.key, tt.defaultValue)
			if got != tt.want {
				t.Errorf("GetAnnotationDuration() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateOriginProtocol(t *testing.T) {
	tests := []struct {
		value   string
		wantErr bool
	}{
		{"http", false},
		{"https", false},
		{"tcp", false},
		{"udp", false},
		{"HTTP", false},  // case insensitive
		{"HTTPS", false}, // case insensitive
		{"TCP", false},   // case insensitive
		{"UDP", false},   // case insensitive
		{"Http", false},  // mixed case
		{"", false},      // empty is valid (will use default)
		{"grpc", true},   // invalid (not in allowed list)
		{"ftp", true},    // invalid
		{"ws", true},     // invalid
		{"wss", true},    // invalid
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			err := ValidateOriginProtocol(tt.value)
			if tt.wantErr && err == nil {
				t.Errorf("ValidateOriginProtocol(%q) expected error, got nil", tt.value)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("ValidateOriginProtocol(%q) unexpected error: %v", tt.value, err)
			}
		})
	}
}

func TestValidateTTL(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"auto", "1", false},
		{"5 minutes", "300", false},
		{"1 hour", "3600", false},
		{"24 hours", "86400", false},
		{"empty", "", false},
		{"zero", "0", true},
		{"below minimum", "-1", true},
		{"above maximum", "86401", true},
		{"way above maximum", "1000000", true},
		{"not a number", "invalid", true},
		{"float", "1.5", true},
		{"with spaces", " 300 ", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTTL(tt.value)
			if tt.wantErr && err == nil {
				t.Errorf("ValidateTTL(%q) expected error, got nil", tt.value)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("ValidateTTL(%q) unexpected error: %v", tt.value, err)
			}
		})
	}
}

func TestValidateHostname(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"simple", "example.com", false},
		{"subdomain", "api.example.com", false},
		{"deep subdomain", "a.b.c.example.com", false},
		{"with numbers", "api2.example.com", false},
		{"with hyphens", "my-api.example.com", false},
		{"single label", "localhost", false},
		{"uppercase (normalized)", "Example.com", false},
		{"all uppercase", "EXAMPLE.COM", false},
		{"mixed case", "Api.Example.Com", false},
		{"empty", "", true},
		{"too long total", string(make([]byte, 254)), true},
		{"label too long", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.example.com", true},
		{"invalid chars underscore", "api_test.example.com", true},
		{"invalid chars space", "api test.example.com", true},
		{"starts with hyphen", "-api.example.com", true},
		{"ends with hyphen", "api-.example.com", true},
		{"double dots", "api..example.com", true},
		{"starts with dot", ".example.com", true},
		{"ends with dot", "example.com.", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateHostname(tt.value)
			if tt.wantErr && err == nil {
				t.Errorf("ValidateHostname(%q) expected error, got nil", tt.value)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("ValidateHostname(%q) unexpected error: %v", tt.value, err)
			}
		})
	}
}

func TestValidateRouteAnnotations(t *testing.T) {
	tests := []struct {
		name            string
		annotations     map[string]string
		requireHostname bool
		wantValid       bool
		wantErrors      int
		wantWarnings    int
	}{
		{
			name:            "valid HTTPRoute annotations",
			annotations:     map[string]string{AnnotationTTL: "300", AnnotationOriginProtocol: "https"},
			requireHostname: false,
			wantValid:       true,
			wantErrors:      0,
			wantWarnings:    0,
		},
		{
			name:            "valid TCPRoute with required hostname",
			annotations:     map[string]string{AnnotationHostname: "tcp.example.com"},
			requireHostname: true,
			wantValid:       true,
			wantErrors:      0,
			wantWarnings:    0,
		},
		{
			name:            "missing required hostname",
			annotations:     map[string]string{},
			requireHostname: true,
			wantValid:       false,
			wantErrors:      1,
			wantWarnings:    0,
		},
		{
			name:            "invalid hostname when required",
			annotations:     map[string]string{AnnotationHostname: "invalid_hostname"},
			requireHostname: true,
			wantValid:       false,
			wantErrors:      1,
			wantWarnings:    0,
		},
		{
			name:            "invalid protocol",
			annotations:     map[string]string{AnnotationOriginProtocol: "ftp"},
			requireHostname: false,
			wantValid:       false,
			wantErrors:      1,
			wantWarnings:    0,
		},
		{
			name:            "invalid TTL",
			annotations:     map[string]string{AnnotationTTL: "0"},
			requireHostname: false,
			wantValid:       false,
			wantErrors:      1,
			wantWarnings:    0,
		},
		{
			name: "multiple errors",
			annotations: map[string]string{
				AnnotationOriginProtocol: "invalid",
				AnnotationTTL:            "invalid",
			},
			requireHostname: false,
			wantValid:       false,
			wantErrors:      2,
			wantWarnings:    0,
		},
		{
			name:            "empty annotations valid for HTTPRoute",
			annotations:     map[string]string{},
			requireHostname: false,
			wantValid:       true,
			wantErrors:      0,
			wantWarnings:    0,
		},
		{
			name:            "optional hostname valid when present",
			annotations:     map[string]string{AnnotationHostname: "api.example.com"},
			requireHostname: false,
			wantValid:       true,
			wantErrors:      0,
			wantWarnings:    0,
		},
		{
			name:            "optional hostname invalid when present",
			annotations:     map[string]string{AnnotationHostname: "invalid_hostname"},
			requireHostname: false,
			wantValid:       false,
			wantErrors:      1,
			wantWarnings:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := newFakeObject(tt.annotations)
			result := ValidateRouteAnnotations(obj, tt.requireHostname)

			if result.Valid != tt.wantValid {
				t.Errorf("ValidateRouteAnnotations().Valid = %v, want %v", result.Valid, tt.wantValid)
			}
			if len(result.Errors) != tt.wantErrors {
				t.Errorf("ValidateRouteAnnotations() got %d errors, want %d: %v", len(result.Errors), tt.wantErrors, result.Errors)
			}
			if len(result.Warnings) != tt.wantWarnings {
				t.Errorf("ValidateRouteAnnotations() got %d warnings, want %d: %v", len(result.Warnings), tt.wantWarnings, result.Warnings)
			}
		})
	}
}

func TestParseOriginConfig(t *testing.T) {
	tests := []struct {
		name            string
		annotations     map[string]string
		defaultProtocol string
		wantProtocol    string
		wantSSLVerify   bool
		wantTimeout     time.Duration
		wantHTTP2       bool
		wantH2c         bool
	}{
		{
			name:            "all defaults",
			annotations:     map[string]string{},
			defaultProtocol: "http",
			wantProtocol:    "http",
			wantSSLVerify:   true,
			wantTimeout:     30 * time.Second,
			wantHTTP2:       false,
			wantH2c:         false,
		},
		{
			name: "explicit values",
			annotations: map[string]string{
				AnnotationOriginProtocol:       "https",
				AnnotationOriginSSLVerify:      "false",
				AnnotationOriginConnectTimeout: "1m",
				AnnotationOriginHTTP2:          "true",
			},
			defaultProtocol: "http",
			wantProtocol:    "https",
			wantSSLVerify:   false,
			wantTimeout:     time.Minute,
			wantHTTP2:       true,
			wantH2c:         false,
		},
		{
			name:            "default protocol for tcp",
			annotations:     map[string]string{},
			defaultProtocol: "tcp",
			wantProtocol:    "tcp",
			wantSSLVerify:   true,
			wantTimeout:     30 * time.Second,
			wantHTTP2:       false,
			wantH2c:         false,
		},
		{
			name: "h2c enabled",
			annotations: map[string]string{
				AnnotationOriginH2c: "true",
			},
			defaultProtocol: "http",
			wantProtocol:    "http",
			wantSSLVerify:   true,
			wantTimeout:     30 * time.Second,
			wantHTTP2:       false,
			wantH2c:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := newFakeObject(tt.annotations)
			config := ParseOriginConfig(obj, tt.defaultProtocol)

			if config.Protocol != tt.wantProtocol {
				t.Errorf("ParseOriginConfig().Protocol = %q, want %q", config.Protocol, tt.wantProtocol)
			}
			if config.SSLVerify != tt.wantSSLVerify {
				t.Errorf("ParseOriginConfig().SSLVerify = %v, want %v", config.SSLVerify, tt.wantSSLVerify)
			}
			if config.ConnectTimeout != tt.wantTimeout {
				t.Errorf("ParseOriginConfig().ConnectTimeout = %v, want %v", config.ConnectTimeout, tt.wantTimeout)
			}
			if config.HTTP2 != tt.wantHTTP2 {
				t.Errorf("ParseOriginConfig().HTTP2 = %v, want %v", config.HTTP2, tt.wantHTTP2)
			}
			if config.H2c != tt.wantH2c {
				t.Errorf("ParseOriginConfig().H2c = %v, want %v", config.H2c, tt.wantH2c)
			}
		})
	}
}

func TestParseDNSConfig(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		wantTTL     int
		wantProxied bool
	}{
		{
			name:        "all defaults",
			annotations: map[string]string{},
			wantTTL:     1, // auto
			wantProxied: true,
		},
		{
			name: "explicit values",
			annotations: map[string]string{
				AnnotationTTL:               "300",
				AnnotationCloudflareProxied: "false",
			},
			wantTTL:     300,
			wantProxied: false,
		},
		{
			name: "proxied only",
			annotations: map[string]string{
				AnnotationCloudflareProxied: "false",
			},
			wantTTL:     1,
			wantProxied: false,
		},
		{
			name: "ttl only",
			annotations: map[string]string{
				AnnotationTTL: "3600",
			},
			wantTTL:     3600,
			wantProxied: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := newFakeObject(tt.annotations)
			config := ParseDNSConfig(obj)

			if config.TTL != tt.wantTTL {
				t.Errorf("ParseDNSConfig().TTL = %d, want %d", config.TTL, tt.wantTTL)
			}
			if config.Proxied != tt.wantProxied {
				t.Errorf("ParseDNSConfig().Proxied = %v, want %v", config.Proxied, tt.wantProxied)
			}
		})
	}
}

func TestConstants(t *testing.T) {
	// Verify annotation prefix is consistent
	if AnnotationPrefix != "cfgate.io/" {
		t.Errorf("AnnotationPrefix = %q, want %q", AnnotationPrefix, "cfgate.io/")
	}

	// Verify all annotations use the prefix
	annotations := []string{
		AnnotationTunnelTarget,
		AnnotationTunnelRef,
		AnnotationOriginProtocol,
		AnnotationOriginSSLVerify,
		AnnotationTTL,
		AnnotationCloudflareProxied,
		AnnotationAccessPolicy,
		AnnotationHostname,
		AnnotationOriginConnectTimeout,
		AnnotationOriginHTTPHostHeader,
		AnnotationOriginServerName,
		AnnotationOriginCAPool,
		AnnotationOriginHTTP2,
		AnnotationOriginH2c,
	}

	for _, ann := range annotations {
		if len(ann) <= len(AnnotationPrefix) {
			t.Errorf("Annotation %q is too short", ann)
		}
		if ann[:len(AnnotationPrefix)] != AnnotationPrefix {
			t.Errorf("Annotation %q does not start with prefix %q", ann, AnnotationPrefix)
		}
	}

	// Verify validation constants
	if MinTTL != 1 {
		t.Errorf("MinTTL = %d, want 1", MinTTL)
	}
	if MaxTTL != 86400 {
		t.Errorf("MaxTTL = %d, want 86400", MaxTTL)
	}
	if MaxHostnameLength != 253 {
		t.Errorf("MaxHostnameLength = %d, want 253", MaxHostnameLength)
	}
	if MaxLabelLength != 63 {
		t.Errorf("MaxLabelLength = %d, want 63", MaxLabelLength)
	}
}

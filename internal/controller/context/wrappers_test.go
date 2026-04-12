package context

import (
	"errors"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
)

func boolPtr(b bool) *bool       { return &b }
func stringPtr(s string) *string { return &s }

// ---------------------------------------------------------------------------
// TunnelContext
// ---------------------------------------------------------------------------

func TestNewTunnelContext(t *testing.T) {
	tunnel := &cfgatev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "cfgate-system"},
		Spec: cfgatev1alpha1.CloudflareTunnelSpec{
			Tunnel: cfgatev1alpha1.TunnelIdentity{Name: "prod-tunnel"},
		},
	}
	mockClient := &cloudflare.MockClient{}

	tests := []struct {
		name      string
		tunnel    *cfgatev1alpha1.CloudflareTunnel
		accountID string
		client    cloudflare.Client
		wantAcct  string
		wantNil   bool
	}{
		{
			name:      "basic construction",
			tunnel:    tunnel,
			accountID: "acc123",
			client:    mockClient,
			wantAcct:  "acc123",
		},
		{
			name:      "empty account ID",
			tunnel:    tunnel,
			accountID: "",
			client:    mockClient,
			wantAcct:  "",
		},
		{
			name:      "nil client",
			tunnel:    tunnel,
			accountID: "acc123",
			client:    nil,
			wantAcct:  "acc123",
			wantNil:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := NewTunnelContext(tt.tunnel, tt.accountID, tt.client)
			if tc.AccountID() != tt.wantAcct {
				t.Errorf("AccountID() = %q, want %q", tc.AccountID(), tt.wantAcct)
			}
			if tt.wantNil && tc.TunnelClient() != nil {
				t.Errorf("TunnelClient() = %v, want nil", tc.TunnelClient())
			}
			if !tt.wantNil && tc.TunnelClient() != tt.client {
				t.Errorf("TunnelClient() = %v, want %v", tc.TunnelClient(), tt.client)
			}
		})
	}
}

func TestTunnelContextEmbeddedTunnel(t *testing.T) {
	tunnel := &cfgatev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "my-tunnel", Namespace: "ns1"},
		Spec: cfgatev1alpha1.CloudflareTunnelSpec{
			Tunnel: cfgatev1alpha1.TunnelIdentity{Name: "prod-tunnel"},
		},
	}
	tc := NewTunnelContext(tunnel, "acc", nil)

	if tc.Name != "my-tunnel" {
		t.Errorf("embedded Name = %q, want %q", tc.Name, "my-tunnel")
	}
	if tc.Namespace != "ns1" {
		t.Errorf("embedded Namespace = %q, want %q", tc.Namespace, "ns1")
	}
	if tc.Spec.Tunnel.Name != "prod-tunnel" {
		t.Errorf("embedded Spec.Tunnel.Name = %q, want %q", tc.Spec.Tunnel.Name, "prod-tunnel")
	}
}

func TestTunnelContextAccountID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"set value", "abc123", "abc123"},
		{"empty value", "", ""},
		{"hex account ID", "0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := &TunnelContext{resolvedAccountID: tt.id}
			if got := tc.AccountID(); got != tt.want {
				t.Errorf("AccountID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTunnelContextTunnelClient(t *testing.T) {
	mock := &cloudflare.MockClient{}
	tc := &TunnelContext{tunnelClient: mock}
	if got := tc.TunnelClient(); got != mock {
		t.Errorf("TunnelClient() = %v, want %v", got, mock)
	}

	tc2 := &TunnelContext{tunnelClient: nil}
	if got := tc2.TunnelClient(); got != nil {
		t.Errorf("TunnelClient() = %v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// DNSContext: ZoneForHostname
// ---------------------------------------------------------------------------

func TestZoneForHostname(t *testing.T) {
	tests := []struct {
		name     string
		zones    []cfgatev1alpha1.DNSZoneConfig
		hostname string
		wantZone string
		wantOK   bool
	}{
		{
			name:     "exact match",
			zones:    []cfgatev1alpha1.DNSZoneConfig{{Name: "example.com"}},
			hostname: "example.com",
			wantZone: "example.com",
			wantOK:   true,
		},
		{
			name:     "subdomain match",
			zones:    []cfgatev1alpha1.DNSZoneConfig{{Name: "example.com"}},
			hostname: "app.example.com",
			wantZone: "example.com",
			wantOK:   true,
		},
		{
			name:     "deep subdomain",
			zones:    []cfgatev1alpha1.DNSZoneConfig{{Name: "example.com"}},
			hostname: "a.b.c.example.com",
			wantZone: "example.com",
			wantOK:   true,
		},
		{
			name:     "no match",
			zones:    []cfgatev1alpha1.DNSZoneConfig{{Name: "example.com"}},
			hostname: "other.com",
			wantZone: "",
			wantOK:   false,
		},
		{
			name:     "label boundary: badexample.com must not match example.com",
			zones:    []cfgatev1alpha1.DNSZoneConfig{{Name: "example.com"}},
			hostname: "badexample.com",
			wantZone: "",
			wantOK:   false,
		},
		{
			name:     "multiple zones: first match wins",
			zones:    []cfgatev1alpha1.DNSZoneConfig{{Name: "a.com"}, {Name: "b.com"}},
			hostname: "app.a.com",
			wantZone: "a.com",
			wantOK:   true,
		},
		{
			name:     "multiple zones: second zone matches",
			zones:    []cfgatev1alpha1.DNSZoneConfig{{Name: "a.com"}, {Name: "b.com"}},
			hostname: "app.b.com",
			wantZone: "b.com",
			wantOK:   true,
		},
		{
			name:     "empty hostname",
			zones:    []cfgatev1alpha1.DNSZoneConfig{{Name: "example.com"}},
			hostname: "",
			wantZone: "",
			wantOK:   false,
		},
		{
			name:     "empty zones",
			zones:    nil,
			hostname: "app.example.com",
			wantZone: "",
			wantOK:   false,
		},
		{
			name:     "zone with trailing dot boundary",
			zones:    []cfgatev1alpha1.DNSZoneConfig{{Name: "example.com"}},
			hostname: "sub.example.com",
			wantZone: "example.com",
			wantOK:   true,
		},
		{
			name:     "hostname equals zone exactly",
			zones:    []cfgatev1alpha1.DNSZoneConfig{{Name: "example.com"}},
			hostname: "example.com",
			wantZone: "example.com",
			wantOK:   true,
		},
		{
			name:     "single label hostname",
			zones:    []cfgatev1alpha1.DNSZoneConfig{{Name: "example.com"}},
			hostname: "localhost",
			wantZone: "",
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := &DNSContext{
				CloudflareDNS: &cfgatev1alpha1.CloudflareDNS{
					Spec: cfgatev1alpha1.CloudflareDNSSpec{
						Zones: tt.zones,
					},
				},
			}
			gotZone, gotOK := dc.ZoneForHostname(tt.hostname)
			if gotZone != tt.wantZone || gotOK != tt.wantOK {
				t.Errorf("ZoneForHostname(%q) = (%q, %v), want (%q, %v)",
					tt.hostname, gotZone, gotOK, tt.wantZone, tt.wantOK)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DNSContext: Default Methods
// ---------------------------------------------------------------------------

func TestGetDefaultProxied(t *testing.T) {
	tests := []struct {
		name    string
		proxied bool
		want    bool
	}{
		{"true", true, true},
		{"false", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := &DNSContext{
				CloudflareDNS: &cfgatev1alpha1.CloudflareDNS{
					Spec: cfgatev1alpha1.CloudflareDNSSpec{
						Defaults: cfgatev1alpha1.DNSRecordDefaults{Proxied: tt.proxied},
					},
				},
			}
			if got := dc.GetDefaultProxied(); got != tt.want {
				t.Errorf("GetDefaultProxied() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetDefaultTTL(t *testing.T) {
	tests := []struct {
		name string
		ttl  int32
		want int32
	}{
		{"zero defaults to 1 (auto)", 0, 1},
		{"explicit value 300", 300, 300},
		{"explicit value 1", 1, 1},
		{"explicit value 60", 60, 60},
		{"explicit value 86400", 86400, 86400},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := &DNSContext{
				CloudflareDNS: &cfgatev1alpha1.CloudflareDNS{
					Spec: cfgatev1alpha1.CloudflareDNSSpec{
						Defaults: cfgatev1alpha1.DNSRecordDefaults{TTL: tt.ttl},
					},
				},
			}
			if got := dc.GetDefaultTTL(); got != tt.want {
				t.Errorf("GetDefaultTTL() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestGetPolicy(t *testing.T) {
	tests := []struct {
		name   string
		policy cfgatev1alpha1.DNSPolicy
		want   cfgatev1alpha1.DNSPolicy
	}{
		{"empty defaults to sync", "", cfgatev1alpha1.DNSPolicySync},
		{"explicit sync", cfgatev1alpha1.DNSPolicySync, cfgatev1alpha1.DNSPolicySync},
		{"upsert-only", cfgatev1alpha1.DNSPolicyUpsertOnly, cfgatev1alpha1.DNSPolicyUpsertOnly},
		{"create-only", cfgatev1alpha1.DNSPolicyCreateOnly, cfgatev1alpha1.DNSPolicyCreateOnly},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := &DNSContext{
				CloudflareDNS: &cfgatev1alpha1.CloudflareDNS{
					Spec: cfgatev1alpha1.CloudflareDNSSpec{Policy: tt.policy},
				},
			}
			if got := dc.GetPolicy(); got != tt.want {
				t.Errorf("GetPolicy() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetOwnerID(t *testing.T) {
	tests := []struct {
		name      string
		ownerID   string
		namespace string
		dnsName   string
		want      string
	}{
		{"explicit owner", "cluster-1", "ns", "dns1", "cluster-1"},
		{"default to namespace/name", "", "default", "my-dns", "default/my-dns"},
		{"empty namespace and name", "", "", "", "/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := &DNSContext{
				CloudflareDNS: &cfgatev1alpha1.CloudflareDNS{
					ObjectMeta: metav1.ObjectMeta{Name: tt.dnsName, Namespace: tt.namespace},
					Spec: cfgatev1alpha1.CloudflareDNSSpec{
						Ownership: cfgatev1alpha1.DNSOwnershipConfig{OwnerID: tt.ownerID},
					},
				},
			}
			if got := dc.GetOwnerID(); got != tt.want {
				t.Errorf("GetOwnerID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetOwnershipPrefix(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		want   string
	}{
		{"explicit prefix", "_custom", "_custom"},
		{"default", "", "_cfgate"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := &DNSContext{
				CloudflareDNS: &cfgatev1alpha1.CloudflareDNS{
					Spec: cfgatev1alpha1.CloudflareDNSSpec{
						Ownership: cfgatev1alpha1.DNSOwnershipConfig{
							TXTRecord: cfgatev1alpha1.DNSTXTRecordOwnership{Prefix: tt.prefix},
						},
					},
				},
			}
			if got := dc.GetOwnershipPrefix(); got != tt.want {
				t.Errorf("GetOwnershipPrefix() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShouldCreateTXTRecords(t *testing.T) {
	tests := []struct {
		name    string
		enabled *bool
		want    bool
	}{
		{"nil defaults to true", nil, true},
		{"explicit true", boolPtr(true), true},
		{"explicit false", boolPtr(false), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := &DNSContext{
				CloudflareDNS: &cfgatev1alpha1.CloudflareDNS{
					Spec: cfgatev1alpha1.CloudflareDNSSpec{
						Ownership: cfgatev1alpha1.DNSOwnershipConfig{
							TXTRecord: cfgatev1alpha1.DNSTXTRecordOwnership{Enabled: tt.enabled},
						},
					},
				},
			}
			if got := dc.ShouldCreateTXTRecords(); got != tt.want {
				t.Errorf("ShouldCreateTXTRecords() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldDeleteOnRouteRemoval(t *testing.T) {
	tests := []struct {
		name string
		val  *bool
		want bool
	}{
		{"nil defaults to true", nil, true},
		{"explicit true", boolPtr(true), true},
		{"explicit false", boolPtr(false), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := &DNSContext{
				CloudflareDNS: &cfgatev1alpha1.CloudflareDNS{
					Spec: cfgatev1alpha1.CloudflareDNSSpec{
						CleanupPolicy: cfgatev1alpha1.DNSCleanupPolicy{DeleteOnRouteRemoval: tt.val},
					},
				},
			}
			if got := dc.ShouldDeleteOnRouteRemoval(); got != tt.want {
				t.Errorf("ShouldDeleteOnRouteRemoval() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldDeleteOnResourceRemoval(t *testing.T) {
	tests := []struct {
		name string
		val  *bool
		want bool
	}{
		{"nil defaults to true", nil, true},
		{"explicit true", boolPtr(true), true},
		{"explicit false", boolPtr(false), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := &DNSContext{
				CloudflareDNS: &cfgatev1alpha1.CloudflareDNS{
					Spec: cfgatev1alpha1.CloudflareDNSSpec{
						CleanupPolicy: cfgatev1alpha1.DNSCleanupPolicy{DeleteOnResourceRemoval: tt.val},
					},
				},
			}
			if got := dc.ShouldDeleteOnResourceRemoval(); got != tt.want {
				t.Errorf("ShouldDeleteOnResourceRemoval() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOnlyDeleteManaged(t *testing.T) {
	tests := []struct {
		name string
		val  *bool
		want bool
	}{
		{"nil defaults to true", nil, true},
		{"explicit true", boolPtr(true), true},
		{"explicit false", boolPtr(false), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := &DNSContext{
				CloudflareDNS: &cfgatev1alpha1.CloudflareDNS{
					Spec: cfgatev1alpha1.CloudflareDNSSpec{
						CleanupPolicy: cfgatev1alpha1.DNSCleanupPolicy{OnlyManaged: tt.val},
					},
				},
			}
			if got := dc.OnlyDeleteManaged(); got != tt.want {
				t.Errorf("OnlyDeleteManaged() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DNSContext: Accessor Methods
// ---------------------------------------------------------------------------

func TestTunnelDomain(t *testing.T) {
	tests := []struct {
		name   string
		domain string
		want   string
	}{
		{"cached tunnel domain", "uuid.cfargotunnel.com", "uuid.cfargotunnel.com"},
		{"empty domain", "", ""},
		{"external target value", "cdn.example.com", "cdn.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := &DNSContext{
				CloudflareDNS: &cfgatev1alpha1.CloudflareDNS{},
				tunnelDomain:  tt.domain,
			}
			if got := dc.TunnelDomain(); got != tt.want {
				t.Errorf("TunnelDomain() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTunnelName(t *testing.T) {
	tests := []struct {
		name   string
		tunnel *cfgatev1alpha1.CloudflareTunnel
		want   string
	}{
		{
			name: "resolved tunnel",
			tunnel: &cfgatev1alpha1.CloudflareTunnel{
				Spec: cfgatev1alpha1.CloudflareTunnelSpec{
					Tunnel: cfgatev1alpha1.TunnelIdentity{Name: "prod"},
				},
			},
			want: "prod",
		},
		{
			name:   "nil tunnel returns empty",
			tunnel: nil,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := &DNSContext{
				CloudflareDNS:  &cfgatev1alpha1.CloudflareDNS{},
				resolvedTunnel: tt.tunnel,
			}
			if got := dc.TunnelName(); got != tt.want {
				t.Errorf("TunnelName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTunnelNamespacedName(t *testing.T) {
	tests := []struct {
		name   string
		tunnel *cfgatev1alpha1.CloudflareTunnel
		want   types.NamespacedName
	}{
		{
			name: "resolved tunnel",
			tunnel: &cfgatev1alpha1.CloudflareTunnel{
				ObjectMeta: metav1.ObjectMeta{Namespace: "ns1", Name: "tun1"},
			},
			want: types.NamespacedName{Namespace: "ns1", Name: "tun1"},
		},
		{
			name:   "nil tunnel returns empty",
			tunnel: nil,
			want:   types.NamespacedName{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := &DNSContext{
				CloudflareDNS:  &cfgatev1alpha1.CloudflareDNS{},
				resolvedTunnel: tt.tunnel,
			}
			if got := dc.TunnelNamespacedName(); got != tt.want {
				t.Errorf("TunnelNamespacedName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasTunnelRef(t *testing.T) {
	tests := []struct {
		name      string
		tunnelRef *cfgatev1alpha1.DNSTunnelRef
		want      bool
	}{
		{"set", &cfgatev1alpha1.DNSTunnelRef{Name: "t1"}, true},
		{"nil", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := &DNSContext{
				CloudflareDNS: &cfgatev1alpha1.CloudflareDNS{
					Spec: cfgatev1alpha1.CloudflareDNSSpec{TunnelRef: tt.tunnelRef},
				},
			}
			if got := dc.HasTunnelRef(); got != tt.want {
				t.Errorf("HasTunnelRef() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasExternalTarget(t *testing.T) {
	tests := []struct {
		name   string
		target *cfgatev1alpha1.ExternalTarget
		want   bool
	}{
		{"set", &cfgatev1alpha1.ExternalTarget{Value: "cdn.example.com"}, true},
		{"nil", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := &DNSContext{
				CloudflareDNS: &cfgatev1alpha1.CloudflareDNS{
					Spec: cfgatev1alpha1.CloudflareDNSSpec{ExternalTarget: tt.target},
				},
			}
			if got := dc.HasExternalTarget(); got != tt.want {
				t.Errorf("HasExternalTarget() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolvedZones(t *testing.T) {
	zones := map[string]string{"example.com": "zone-1", "other.com": "zone-2"}
	dc := &DNSContext{
		CloudflareDNS: &cfgatev1alpha1.CloudflareDNS{},
		resolvedZones: zones,
	}
	got := dc.ResolvedZones()
	if len(got) != 2 {
		t.Fatalf("ResolvedZones() len = %d, want 2", len(got))
	}
	if got["example.com"] != "zone-1" {
		t.Errorf("ResolvedZones()[example.com] = %q, want %q", got["example.com"], "zone-1")
	}
}

func TestSetResolvedZoneID(t *testing.T) {
	dc := &DNSContext{
		CloudflareDNS: &cfgatev1alpha1.CloudflareDNS{},
		resolvedZones: map[string]string{"example.com": ""},
	}
	dc.SetResolvedZoneID("example.com", "zone-abc")
	if got := dc.resolvedZones["example.com"]; got != "zone-abc" {
		t.Errorf("after SetResolvedZoneID: resolvedZones[example.com] = %q, want %q", got, "zone-abc")
	}

	dc.SetResolvedZoneID("new.com", "zone-new")
	if got := dc.resolvedZones["new.com"]; got != "zone-new" {
		t.Errorf("SetResolvedZoneID for new key: resolvedZones[new.com] = %q, want %q", got, "zone-new")
	}
}

func TestGetZoneID(t *testing.T) {
	dc := &DNSContext{
		CloudflareDNS: &cfgatev1alpha1.CloudflareDNS{},
		resolvedZones: map[string]string{"example.com": "zone-1"},
	}
	if got := dc.GetZoneID("example.com"); got != "zone-1" {
		t.Errorf("GetZoneID(example.com) = %q, want %q", got, "zone-1")
	}
	if got := dc.GetZoneID("missing.com"); got != "" {
		t.Errorf("GetZoneID(missing.com) = %q, want empty", got)
	}
}

func TestHasGatewayRoutesEnabled(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		want    bool
	}{
		{"enabled", true, true},
		{"disabled", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := &DNSContext{
				CloudflareDNS: &cfgatev1alpha1.CloudflareDNS{
					Spec: cfgatev1alpha1.CloudflareDNSSpec{
						Source: cfgatev1alpha1.DNSHostnameSource{
							GatewayRoutes: cfgatev1alpha1.DNSGatewayRoutesSource{Enabled: tt.enabled},
						},
					},
				},
			}
			if got := dc.HasGatewayRoutesEnabled(); got != tt.want {
				t.Errorf("HasGatewayRoutesEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetAnnotationFilter(t *testing.T) {
	tests := []struct {
		name   string
		filter string
		want   string
	}{
		{"set", "cfgate.io/dns-sync", "cfgate.io/dns-sync"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := &DNSContext{
				CloudflareDNS: &cfgatev1alpha1.CloudflareDNS{
					Spec: cfgatev1alpha1.CloudflareDNSSpec{
						Source: cfgatev1alpha1.DNSHostnameSource{
							GatewayRoutes: cfgatev1alpha1.DNSGatewayRoutesSource{AnnotationFilter: tt.filter},
						},
					},
				},
			}
			if got := dc.GetAnnotationFilter(); got != tt.want {
				t.Errorf("GetAnnotationFilter() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetExplicitHostnames(t *testing.T) {
	hostnames := []cfgatev1alpha1.DNSExplicitHostname{
		{Hostname: "app.example.com"},
		{Hostname: "api.example.com"},
	}
	dc := &DNSContext{
		CloudflareDNS: &cfgatev1alpha1.CloudflareDNS{
			Spec: cfgatev1alpha1.CloudflareDNSSpec{
				Source: cfgatev1alpha1.DNSHostnameSource{Explicit: hostnames},
			},
		},
	}
	got := dc.GetExplicitHostnames()
	if len(got) != 2 {
		t.Fatalf("GetExplicitHostnames() len = %d, want 2", len(got))
	}
	if got[0].Hostname != "app.example.com" {
		t.Errorf("GetExplicitHostnames()[0].Hostname = %q, want %q", got[0].Hostname, "app.example.com")
	}

	dc2 := &DNSContext{
		CloudflareDNS: &cfgatev1alpha1.CloudflareDNS{
			Spec: cfgatev1alpha1.CloudflareDNSSpec{},
		},
	}
	if got := dc2.GetExplicitHostnames(); len(got) != 0 {
		t.Errorf("GetExplicitHostnames() on empty = %d, want 0", len(got))
	}
}

// ---------------------------------------------------------------------------
// AccessPolicyContext: GetTargetRefs
// ---------------------------------------------------------------------------

func TestGetTargetRefs(t *testing.T) {
	refA := cfgatev1alpha1.PolicyTargetReference{Kind: "HTTPRoute", Name: "a"}
	refB := cfgatev1alpha1.PolicyTargetReference{Kind: "Gateway", Name: "b"}
	refC := cfgatev1alpha1.PolicyTargetReference{Kind: "HTTPRoute", Name: "c"}

	tests := []struct {
		name       string
		targetRef  *cfgatev1alpha1.PolicyTargetReference
		targetRefs []cfgatev1alpha1.PolicyTargetReference
		wantLen    int
	}{
		{"only targetRef set", &refA, nil, 1},
		{"only targetRefs set", nil, []cfgatev1alpha1.PolicyTargetReference{refB, refC}, 2},
		{"both set: targetRef first", &refA, []cfgatev1alpha1.PolicyTargetReference{refB, refC}, 3},
		{"neither set", nil, nil, 0},
		{"targetRefs empty slice", nil, []cfgatev1alpha1.PolicyTargetReference{}, 0},
		{"targetRef with empty targetRefs", &refA, []cfgatev1alpha1.PolicyTargetReference{}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apc := &AccessPolicyContext{
				CloudflareAccessPolicy: &cfgatev1alpha1.CloudflareAccessPolicy{
					Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
						TargetRef:  tt.targetRef,
						TargetRefs: tt.targetRefs,
					},
				},
			}
			got := apc.GetTargetRefs()
			if len(got) != tt.wantLen {
				t.Errorf("GetTargetRefs() len = %d, want %d", len(got), tt.wantLen)
			}
			if tt.targetRef != nil && len(got) > 0 && got[0].Name != tt.targetRef.Name {
				t.Errorf("GetTargetRefs()[0].Name = %q, want %q (targetRef first)", got[0].Name, tt.targetRef.Name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// AccessPolicyContext: Target Filtering Methods
// ---------------------------------------------------------------------------

func TestResolvedTargets(t *testing.T) {
	targets := []TargetInfo{
		{Kind: "HTTPRoute", Name: "a", Resolved: true},
		{Kind: "Gateway", Name: "b", Error: errors.New("fail")},
	}
	apc := &AccessPolicyContext{
		CloudflareAccessPolicy: &cfgatev1alpha1.CloudflareAccessPolicy{},
		resolvedTargets:        targets,
	}
	got := apc.ResolvedTargets()
	if len(got) != 2 {
		t.Errorf("ResolvedTargets() len = %d, want 2", len(got))
	}
}

func TestSuccessfullyResolvedTargets(t *testing.T) {
	tests := []struct {
		name    string
		targets []TargetInfo
		wantLen int
	}{
		{
			name: "all resolved",
			targets: []TargetInfo{
				{Resolved: true, Error: nil},
				{Resolved: true, Error: nil},
			},
			wantLen: 2,
		},
		{
			name: "mixed",
			targets: []TargetInfo{
				{Resolved: true, Error: nil},
				{Resolved: false, Error: errors.New("fail")},
			},
			wantLen: 1,
		},
		{
			name: "all failed",
			targets: []TargetInfo{
				{Error: errors.New("fail")},
			},
			wantLen: 0,
		},
		{
			name:    "empty",
			targets: nil,
			wantLen: 0,
		},
		{
			name: "resolved but with error",
			targets: []TargetInfo{
				{Resolved: true, Error: errors.New("partial")},
			},
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apc := &AccessPolicyContext{
				CloudflareAccessPolicy: &cfgatev1alpha1.CloudflareAccessPolicy{},
				resolvedTargets:        tt.targets,
			}
			if got := apc.SuccessfullyResolvedTargets(); len(got) != tt.wantLen {
				t.Errorf("SuccessfullyResolvedTargets() len = %d, want %d", len(got), tt.wantLen)
			}
		})
	}
}

func TestFailedTargets(t *testing.T) {
	tests := []struct {
		name    string
		targets []TargetInfo
		wantLen int
	}{
		{
			name: "none failed",
			targets: []TargetInfo{
				{Resolved: true, Error: nil},
			},
			wantLen: 0,
		},
		{
			name: "some failed",
			targets: []TargetInfo{
				{Error: nil},
				{Error: errors.New("fail")},
			},
			wantLen: 1,
		},
		{
			name: "all failed",
			targets: []TargetInfo{
				{Error: errors.New("e1")},
				{Error: errors.New("e2")},
			},
			wantLen: 2,
		},
		{
			name:    "empty",
			targets: nil,
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apc := &AccessPolicyContext{
				CloudflareAccessPolicy: &cfgatev1alpha1.CloudflareAccessPolicy{},
				resolvedTargets:        tt.targets,
			}
			if got := apc.FailedTargets(); len(got) != tt.wantLen {
				t.Errorf("FailedTargets() len = %d, want %d", len(got), tt.wantLen)
			}
		})
	}
}

func TestHasFailedTargets(t *testing.T) {
	tests := []struct {
		name    string
		targets []TargetInfo
		want    bool
	}{
		{"none failed", []TargetInfo{{Resolved: true}}, false},
		{"one failed", []TargetInfo{{Error: errors.New("fail")}}, true},
		{"empty", nil, false},
		{
			"mixed",
			[]TargetInfo{
				{Resolved: true, Error: nil},
				{Error: errors.New("fail")},
			},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apc := &AccessPolicyContext{
				CloudflareAccessPolicy: &cfgatev1alpha1.CloudflareAccessPolicy{},
				resolvedTargets:        tt.targets,
			}
			if got := apc.HasFailedTargets(); got != tt.want {
				t.Errorf("HasFailedTargets() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAllTargetsResolved(t *testing.T) {
	tests := []struct {
		name    string
		targets []TargetInfo
		want    bool
	}{
		{
			name:    "all resolved",
			targets: []TargetInfo{{Resolved: true, Error: nil}},
			want:    true,
		},
		{
			name: "multiple all resolved",
			targets: []TargetInfo{
				{Resolved: true, Error: nil},
				{Resolved: true, Error: nil},
			},
			want: true,
		},
		{
			name: "one failed",
			targets: []TargetInfo{
				{Resolved: true, Error: nil},
				{Error: errors.New("fail")},
			},
			want: false,
		},
		{
			name:    "empty returns false",
			targets: nil,
			want:    false,
		},
		{
			name:    "resolved but error",
			targets: []TargetInfo{{Resolved: true, Error: errors.New("err")}},
			want:    false,
		},
		{
			name:    "not resolved, no error",
			targets: []TargetInfo{{Resolved: false, Error: nil}},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apc := &AccessPolicyContext{
				CloudflareAccessPolicy: &cfgatev1alpha1.CloudflareAccessPolicy{},
				resolvedTargets:        tt.targets,
			}
			if got := apc.AllTargetsResolved(); got != tt.want {
				t.Errorf("AllTargetsResolved() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// AccessPolicyContext: Feature Checks
// ---------------------------------------------------------------------------

func TestRequiresServiceTokens(t *testing.T) {
	tests := []struct {
		name   string
		tokens []cfgatev1alpha1.ServiceTokenConfig
		want   bool
	}{
		{"empty", nil, false},
		{"empty slice", []cfgatev1alpha1.ServiceTokenConfig{}, false},
		{
			"one token",
			[]cfgatev1alpha1.ServiceTokenConfig{{Name: "svc-1"}},
			true,
		},
		{
			"multiple tokens",
			[]cfgatev1alpha1.ServiceTokenConfig{{Name: "svc-1"}, {Name: "svc-2"}},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apc := &AccessPolicyContext{
				CloudflareAccessPolicy: &cfgatev1alpha1.CloudflareAccessPolicy{
					Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{ServiceTokens: tt.tokens},
				},
			}
			if got := apc.RequiresServiceTokens(); got != tt.want {
				t.Errorf("RequiresServiceTokens() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasCrossNamespaceTargets(t *testing.T) {
	tests := []struct {
		name      string
		policyNS  string
		targetNSs []string
		want      bool
	}{
		{
			name:      "all same namespace",
			policyNS:  "ns1",
			targetNSs: []string{"ns1", "ns1"},
			want:      false,
		},
		{
			name:      "cross namespace",
			policyNS:  "ns1",
			targetNSs: []string{"ns2"},
			want:      true,
		},
		{
			name:      "mixed namespaces",
			policyNS:  "ns1",
			targetNSs: []string{"ns1", "ns2"},
			want:      true,
		},
		{
			name:      "empty targets",
			policyNS:  "ns1",
			targetNSs: nil,
			want:      false,
		},
		{
			name:      "single same namespace",
			policyNS:  "default",
			targetNSs: []string{"default"},
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var targets []TargetInfo
			for _, ns := range tt.targetNSs {
				targets = append(targets, TargetInfo{Namespace: ns})
			}
			apc := &AccessPolicyContext{
				CloudflareAccessPolicy: &cfgatev1alpha1.CloudflareAccessPolicy{
					ObjectMeta: metav1.ObjectMeta{Namespace: tt.policyNS},
				},
				resolvedTargets: targets,
			}
			if got := apc.HasCrossNamespaceTargets(); got != tt.want {
				t.Errorf("HasCrossNamespaceTargets() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TargetInfo Methods
// ---------------------------------------------------------------------------

func TestTargetInfoNamespacedName(t *testing.T) {
	tests := []struct {
		name string
		ti   TargetInfo
		want types.NamespacedName
	}{
		{
			name: "normal",
			ti:   TargetInfo{Namespace: "default", Name: "my-route"},
			want: types.NamespacedName{Namespace: "default", Name: "my-route"},
		},
		{
			name: "empty namespace",
			ti:   TargetInfo{Namespace: "", Name: "route"},
			want: types.NamespacedName{Namespace: "", Name: "route"},
		},
		{
			name: "empty name",
			ti:   TargetInfo{Namespace: "ns", Name: ""},
			want: types.NamespacedName{Namespace: "ns", Name: ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ti.NamespacedName(); got != tt.want {
				t.Errorf("NamespacedName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTargetInfoString(t *testing.T) {
	tests := []struct {
		name string
		ti   TargetInfo
		want string
	}{
		{
			name: "without section name",
			ti:   TargetInfo{Kind: "HTTPRoute", Namespace: "ns", Name: "route1"},
			want: "HTTPRoute/ns/route1",
		},
		{
			name: "with section name",
			ti:   TargetInfo{Kind: "Gateway", Namespace: "ns", Name: "gw1", SectionName: stringPtr("listener1")},
			want: "Gateway/ns/gw1/listener1",
		},
		{
			name: "empty section name pointer",
			ti:   TargetInfo{Kind: "HTTPRoute", Namespace: "ns", Name: "r1", SectionName: stringPtr("")},
			want: "HTTPRoute/ns/r1/",
		},
		{
			name: "all empty fields",
			ti:   TargetInfo{},
			want: "//",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ti.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTargetInfoIsHTTPRoute(t *testing.T) {
	tests := []struct {
		kind string
		want bool
	}{
		{"HTTPRoute", true},
		{"Gateway", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			ti := &TargetInfo{Kind: tt.kind}
			if got := ti.IsHTTPRoute(); got != tt.want {
				t.Errorf("IsHTTPRoute() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTargetInfoIsGateway(t *testing.T) {
	tests := []struct {
		kind string
		want bool
	}{
		{"Gateway", true},
		{"HTTPRoute", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			ti := &TargetInfo{Kind: tt.kind}
			if got := ti.IsGateway(); got != tt.want {
				t.Errorf("IsGateway() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTargetInfoIsKindExhaustive(t *testing.T) {
	kinds := []struct {
		kind        string
		isHTTPRoute bool
		isGateway   bool
	}{
		{"HTTPRoute", true, false},
		{"Gateway", false, true},
		{"FooRoute", false, false},
		{"", false, false},
	}

	for _, tt := range kinds {
		t.Run(fmt.Sprintf("kind=%s", tt.kind), func(t *testing.T) {
			ti := &TargetInfo{Kind: tt.kind}
			if ti.IsHTTPRoute() != tt.isHTTPRoute {
				t.Errorf("IsHTTPRoute() = %v, want %v", ti.IsHTTPRoute(), tt.isHTTPRoute)
			}
			if ti.IsGateway() != tt.isGateway {
				t.Errorf("IsGateway() = %v, want %v", ti.IsGateway(), tt.isGateway)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helper Functions: countResolved, countFailed
// ---------------------------------------------------------------------------

func TestCountResolved(t *testing.T) {
	tests := []struct {
		name    string
		targets []TargetInfo
		want    int
	}{
		{"empty", nil, 0},
		{
			"all resolved",
			[]TargetInfo{
				{Resolved: true, Error: nil},
				{Resolved: true, Error: nil},
				{Resolved: true, Error: nil},
			},
			3,
		},
		{
			"none resolved",
			[]TargetInfo{
				{Resolved: false, Error: errors.New("fail")},
			},
			0,
		},
		{
			"mixed resolved and failed",
			[]TargetInfo{
				{Resolved: true, Error: nil},
				{Resolved: true, Error: errors.New("partial")},
				{Resolved: false, Error: nil},
			},
			1,
		},
		{
			"resolved but with error excluded",
			[]TargetInfo{
				{Resolved: true, Error: errors.New("err")},
			},
			0,
		},
		{
			"not resolved, no error",
			[]TargetInfo{
				{Resolved: false, Error: nil},
			},
			0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countResolved(tt.targets); got != tt.want {
				t.Errorf("countResolved() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestCountFailed(t *testing.T) {
	tests := []struct {
		name    string
		targets []TargetInfo
		want    int
	}{
		{"empty", nil, 0},
		{
			"all failed",
			[]TargetInfo{
				{Error: errors.New("e1")},
				{Error: errors.New("e2")},
			},
			2,
		},
		{
			"none failed",
			[]TargetInfo{
				{Resolved: true, Error: nil},
			},
			0,
		},
		{
			"mixed",
			[]TargetInfo{
				{Error: nil},
				{Error: errors.New("fail")},
				{Resolved: true, Error: nil},
			},
			1,
		},
		{
			"resolved with error counts as failed",
			[]TargetInfo{
				{Resolved: true, Error: errors.New("err")},
			},
			1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countFailed(tt.targets); got != tt.want {
				t.Errorf("countFailed() = %d, want %d", got, tt.want)
			}
		})
	}
}

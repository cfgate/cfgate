package cloudflare

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	cf "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/shared"
	"github.com/go-logr/logr"
)

func TestRecordsMatch(t *testing.T) {
	tests := []struct {
		name string
		a, b *DNSRecord
		want bool
	}{
		{
			name: "identical records",
			a:    &DNSRecord{Content: "target.com", Proxied: true, TTL: 300, Comment: "test"},
			b:    &DNSRecord{Content: "target.com", Proxied: true, TTL: 300, Comment: "test"},
			want: true,
		},
		{
			name: "content differs",
			a:    &DNSRecord{Content: "a.com"},
			b:    &DNSRecord{Content: "b.com"},
			want: false,
		},
		{
			name: "proxied differs",
			a:    &DNSRecord{Content: "a.com", Proxied: true},
			b:    &DNSRecord{Content: "a.com", Proxied: false},
			want: false,
		},
		{
			name: "ttl differs",
			a:    &DNSRecord{Content: "a.com", TTL: 1},
			b:    &DNSRecord{Content: "a.com", TTL: 300},
			want: false,
		},
		{
			name: "comment differs",
			a:    &DNSRecord{Content: "a.com", Comment: "managed by cfgate"},
			b:    &DNSRecord{Content: "a.com", Comment: ""},
			want: false,
		},
		{
			name: "empty strings match",
			a:    &DNSRecord{},
			b:    &DNSRecord{},
			want: true,
		},
		{
			name: "ignored fields differ",
			a:    &DNSRecord{ID: "id-1", Name: "a.com", Type: "CNAME", ZoneID: "z1", Content: "target", Proxied: true, TTL: 1, Comment: "c"},
			b:    &DNSRecord{ID: "id-2", Name: "b.com", Type: "A", ZoneID: "z2", Content: "target", Proxied: true, TTL: 1, Comment: "c"},
			want: true,
		},
		{
			name: "all compared fields differ",
			a:    &DNSRecord{Content: "a.com", Proxied: true, TTL: 60, Comment: "one"},
			b:    &DNSRecord{Content: "b.com", Proxied: false, TTL: 300, Comment: "two"},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := recordsMatch(tt.a, tt.b); got != tt.want {
				t.Errorf("recordsMatch() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractZoneFromHostname(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		want     string
	}{
		{"simple subdomain", "app.example.com", "example.com"},
		{"complex TLD co.uk", "app.example.co.uk", "example.co.uk"},
		{"complex TLD com.au", "app.example.com.au", "example.com.au"},
		{"deep subdomain", "a.b.c.d.example.com", "example.com"},
		{"bare domain", "example.com", "example.com"},
		{"single label", "localhost", "localhost"},
		{"empty string", "", ""},
		{"two-part hostname", "example.org", "example.org"},
		{"three-level ccTLD", "sub.example.jp", "example.jp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractZoneFromHostname(tt.hostname); got != tt.want {
				t.Errorf("ExtractZoneFromHostname(%q) = %q, want %q", tt.hostname, got, tt.want)
			}
		})
	}
}

func TestValidateTTL(t *testing.T) {
	tests := []struct {
		name    string
		ttl     int
		wantErr bool
	}{
		{"auto TTL", 1, false},
		{"zero", 0, true},
		{"below minimum", 59, true},
		{"minimum", 60, false},
		{"mid range", 300, false},
		{"maximum", 86400, false},
		{"above maximum", 86401, true},
		{"negative", -1, true},
		{"gap between auto and minimum", 2, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTTL(tt.ttl)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateTTL(%d) error = %v, wantErr %v", tt.ttl, err, tt.wantErr)
			}
		})
	}
}

func TestBuildDNSRecord(t *testing.T) {
	tests := []struct {
		name       string
		hostname   string
		target     string
		recordType string
		proxied    bool
		ttl        int
		comment    string
		wantTTL    int
	}{
		{"CNAME record", "app.example.com", "uuid.cfargotunnel.com", "CNAME", true, 300, "managed", 300},
		{"A record", "app.example.com", "1.2.3.4", "A", true, 60, "managed", 60},
		{"AAAA record", "app.example.com", "2001:db8::1", "AAAA", false, 120, "managed", 120},
		{"ttl zero defaults to auto", "app.example.com", "1.2.3.4", "A", true, 0, "", 1},
		{"ttl negative defaults to auto", "app.example.com", "1.2.3.4", "A", false, -5, "", 1},
		{"ttl one passthrough", "app.example.com", "target.com", "CNAME", true, 1, "", 1},
		{"empty hostname", "", "1.2.3.4", "A", false, 60, "", 60},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildDNSRecord(tt.hostname, tt.target, tt.recordType, tt.proxied, tt.ttl, tt.comment)
			if got.Type != tt.recordType {
				t.Errorf("Type = %q, want %q", got.Type, tt.recordType)
			}
			if got.Name != tt.hostname {
				t.Errorf("Name = %q, want %q", got.Name, tt.hostname)
			}
			if got.Content != tt.target {
				t.Errorf("Content = %q, want %q", got.Content, tt.target)
			}
			if got.Proxied != tt.proxied {
				t.Errorf("Proxied = %v, want %v", got.Proxied, tt.proxied)
			}
			if got.TTL != tt.wantTTL {
				t.Errorf("TTL = %d, want %d", got.TTL, tt.wantTTL)
			}
			if got.Comment != tt.comment {
				t.Errorf("Comment = %q, want %q", got.Comment, tt.comment)
			}
		})
	}
}

func TestBuildCNAMERecord(t *testing.T) {
	tests := []struct {
		name         string
		hostname     string
		tunnelDomain string
		proxied      bool
		ttl          int
		comment      string
		wantTTL      int
	}{
		{"standard", "app.example.com", "uuid.cfargotunnel.com", true, 300, "managed", 300},
		{"ttl zero defaults to auto", "app.example.com", "uuid.cfargotunnel.com", true, 0, "", 1},
		{"ttl negative defaults to auto", "app.example.com", "uuid.cfargotunnel.com", false, -5, "", 1},
		{"ttl one passthrough", "app.example.com", "uuid.cfargotunnel.com", true, 1, "", 1},
		{"empty hostname", "", "uuid.cfargotunnel.com", false, 60, "", 60},
		{"not proxied", "app.example.com", "target", false, 300, "test", 300},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildCNAMERecord(tt.hostname, tt.tunnelDomain, tt.proxied, tt.ttl, tt.comment)
			if got.Type != "CNAME" {
				t.Errorf("Type = %q, want CNAME", got.Type)
			}
			if got.Name != tt.hostname {
				t.Errorf("Name = %q, want %q", got.Name, tt.hostname)
			}
			if got.Content != tt.tunnelDomain {
				t.Errorf("Content = %q, want %q", got.Content, tt.tunnelDomain)
			}
			if got.Proxied != tt.proxied {
				t.Errorf("Proxied = %v, want %v", got.Proxied, tt.proxied)
			}
			if got.TTL != tt.wantTTL {
				t.Errorf("TTL = %d, want %d", got.TTL, tt.wantTTL)
			}
			if got.Comment != tt.comment {
				t.Errorf("Comment = %q, want %q", got.Comment, tt.comment)
			}
		})
	}
}

func TestValidateHostnameDepth(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		zoneName string
		want     bool
	}{
		{"single level", "app.example.com", "example.com", false},
		{"multi level", "deep.sub.example.com", "example.com", true},
		{"exact zone match", "example.com", "example.com", false},
		{"three levels deep", "a.b.c.example.com", "example.com", true},
		{"zone not suffix", "app.other.com", "example.com", false},
		{"complex TLD single level", "app.example.co.uk", "example.co.uk", false},
		{"complex TLD multi level", "deep.sub.example.co.uk", "example.co.uk", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidateHostnameDepth(tt.hostname, tt.zoneName); got != tt.want {
				t.Errorf("ValidateHostnameDepth(%q, %q) = %v, want %v", tt.hostname, tt.zoneName, got, tt.want)
			}
		})
	}
}

func TestBuildOwnershipTXTRecord(t *testing.T) {
	tests := []struct {
		name        string
		hostname    string
		ownerID     string
		resource    string
		prefix      string
		wantName    string
		wantContent string
	}{
		{
			name:        "standard",
			hostname:    "app.example.com",
			ownerID:     "cluster-a",
			resource:    "httproute/default/api",
			prefix:      "_cfgate",
			wantName:    "_cfgate.app.example.com",
			wantContent: "heritage=cfgate,cfgate/owner=cluster-a,cfgate/resource=httproute/default/api",
		},
		{
			name:        "empty ownerID",
			hostname:    "app.example.com",
			ownerID:     "",
			resource:    "hr/ns/name",
			prefix:      "_cfgate",
			wantName:    "_cfgate.app.example.com",
			wantContent: "heritage=cfgate,cfgate/owner=,cfgate/resource=hr/ns/name",
		},
		{
			name:        "empty resource",
			hostname:    "app.example.com",
			ownerID:     "c1",
			resource:    "",
			prefix:      "_cfgate",
			wantName:    "_cfgate.app.example.com",
			wantContent: "heritage=cfgate,cfgate/owner=c1,cfgate/resource=",
		},
		{
			name:        "empty prefix",
			hostname:    "app.example.com",
			ownerID:     "c1",
			resource:    "hr/ns/name",
			prefix:      "",
			wantName:    ".app.example.com",
			wantContent: "heritage=cfgate,cfgate/owner=c1,cfgate/resource=hr/ns/name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildOwnershipTXTRecord(tt.hostname, tt.ownerID, tt.resource, tt.prefix)
			if got.Type != "TXT" {
				t.Errorf("Type = %q, want TXT", got.Type)
			}
			if got.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tt.wantName)
			}
			if got.Content != tt.wantContent {
				t.Errorf("Content = %q, want %q", got.Content, tt.wantContent)
			}
			if got.TTL != 1 {
				t.Errorf("TTL = %d, want 1", got.TTL)
			}
			if got.Proxied {
				t.Error("Proxied = true, want false")
			}
			if got.Comment != "cfgate ownership record" {
				t.Errorf("Comment = %q, want %q", got.Comment, "cfgate ownership record")
			}
		})
	}
}

func TestIsOwnedByCfgate(t *testing.T) {
	tests := []struct {
		name    string
		record  *DNSRecord
		ownerID string
		want    bool
	}{
		{
			name:   "nil record",
			record: nil,
			want:   false,
		},
		{
			name:    "TXT heritage, no filter",
			record:  &DNSRecord{Content: "heritage=cfgate,cfgate/owner=c1,cfgate/resource=hr/ns/r"},
			ownerID: "",
			want:    true,
		},
		{
			name:    "TXT heritage, matching owner",
			record:  &DNSRecord{Content: "heritage=cfgate,cfgate/owner=cluster-a,cfgate/resource=x"},
			ownerID: "cluster-a",
			want:    true,
		},
		{
			name:    "TXT heritage, non-matching owner",
			record:  &DNSRecord{Content: "heritage=cfgate,cfgate/owner=cluster-b,cfgate/resource=x"},
			ownerID: "cluster-a",
			want:    false,
		},
		{
			name:    "substring prevention",
			record:  &DNSRecord{Content: "heritage=cfgate,cfgate/owner=ns/foobar,cfgate/resource=x"},
			ownerID: "ns/foo",
			want:    false,
		},
		{
			name:    "comment-only ownership",
			record:  &DNSRecord{Content: "some-content", Comment: "managed by cfgate"},
			ownerID: "",
			want:    true,
		},
		{
			name:    "comment-only ownership with ownerID",
			record:  &DNSRecord{Content: "some-content", Comment: "managed by cfgate"},
			ownerID: "cluster-a",
			want:    true,
		},
		{
			name:    "no ownership signals",
			record:  &DNSRecord{Content: "random content", Comment: "random comment"},
			ownerID: "",
			want:    false,
		},
		{
			name:    "empty content and comment",
			record:  &DNSRecord{},
			ownerID: "",
			want:    false,
		},
		{
			name:    "heritage without owner field, ownerID provided",
			record:  &DNSRecord{Content: "heritage=cfgate"},
			ownerID: "c1",
			want:    false,
		},
		{
			name:    "heritage without owner field, no filter",
			record:  &DNSRecord{Content: "heritage=cfgate"},
			ownerID: "",
			want:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsOwnedByCfgate(tt.record, tt.ownerID); got != tt.want {
				t.Errorf("IsOwnedByCfgate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsLegacyCommentOwnership(t *testing.T) {
	tests := []struct {
		name   string
		record *DNSRecord
		want   bool
	}{
		{"nil record", nil, false},
		{"legacy comment ownership", &DNSRecord{Content: "cname.target", Comment: "managed by cfgate"}, true},
		{"heritage record", &DNSRecord{Content: "heritage=cfgate,cfgate/owner=c1", Comment: "managed by cfgate"}, false},
		{"no ownership", &DNSRecord{Content: "content", Comment: "some comment"}, false},
		{"empty record", &DNSRecord{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLegacyCommentOwnership(tt.record); got != tt.want {
				t.Errorf("isLegacyCommentOwnership() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseOwnershipRecord(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		wantHeritage string
		wantOwnerID  string
		wantResource string
		wantErr      bool
	}{
		{
			name:         "alpha.3 standard",
			content:      "heritage=cfgate,cfgate/owner=c1,cfgate/resource=hr/ns/r",
			wantHeritage: "cfgate",
			wantOwnerID:  "c1",
			wantResource: "hr/ns/r",
		},
		{
			name:         "alpha.3 no owner",
			content:      "heritage=cfgate,cfgate/resource=hr/ns/r",
			wantHeritage: "cfgate",
			wantOwnerID:  "",
			wantResource: "hr/ns/r",
		},
		{
			name:         "alpha.3 no resource",
			content:      "heritage=cfgate,cfgate/owner=c1",
			wantHeritage: "cfgate",
			wantOwnerID:  "c1",
			wantResource: "",
		},
		{
			name:         "alpha.3 heritage only",
			content:      "heritage=cfgate",
			wantHeritage: "cfgate",
			wantOwnerID:  "",
			wantResource: "",
		},
		{
			name:         "alpha.3 extra fields",
			content:      "heritage=cfgate,cfgate/owner=c1,cfgate/resource=r,extra=foo",
			wantHeritage: "cfgate",
			wantOwnerID:  "c1",
			wantResource: "r",
		},
		{
			name:         "alpha.2 with tunnel",
			content:      "managed by cfgate, tunnel=prod-tunnel",
			wantHeritage: "cfgate",
			wantOwnerID:  "",
			wantResource: "prod-tunnel",
		},
		{
			name:         "alpha.2 with tunnel and trailing comma",
			content:      "managed by cfgate, tunnel=prod,extra",
			wantHeritage: "cfgate",
			wantOwnerID:  "",
			wantResource: "prod",
		},
		{
			name:         "alpha.2 no tunnel",
			content:      "managed by cfgate",
			wantHeritage: "cfgate",
			wantOwnerID:  "",
			wantResource: "",
		},
		{
			name:    "unrecognized format",
			content: "random content",
			wantErr: true,
		},
		{
			name:    "empty string",
			content: "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseOwnershipRecord(tt.content)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseOwnershipRecord() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.Heritage != tt.wantHeritage {
				t.Errorf("Heritage = %q, want %q", got.Heritage, tt.wantHeritage)
			}
			if got.OwnerID != tt.wantOwnerID {
				t.Errorf("OwnerID = %q, want %q", got.OwnerID, tt.wantOwnerID)
			}
			if got.Resource != tt.wantResource {
				t.Errorf("Resource = %q, want %q", got.Resource, tt.wantResource)
			}
		})
	}
}

func TestListManagedRecords(t *testing.T) {
	ctx := context.Background()

	t.Run("skips comment-only record without companion txt in owner scoped cleanup", func(t *testing.T) {
		mock := NewMockClient()
		mock.ListDNSRecordsFunc = func(context.Context, string) ([]DNSRecord, error) {
			return []DNSRecord{{
				ID:      "record-1",
				Name:    "app.example.com",
				Type:    "CNAME",
				Content: "target.example.com",
				Comment: "managed by cfgate",
			}}, nil
		}

		records, err := NewDNSService(mock, logrDiscard()).ListManagedRecords(ctx, "zone-1", "default/dns", "_cfgate")
		if err != nil {
			t.Fatalf("ListManagedRecords() error = %v", err)
		}
		if len(records) != 0 {
			t.Fatalf("ListManagedRecords() = %#v, want no owner-scoped records", records)
		}
	})

	t.Run("includes comment-only record with matching companion txt", func(t *testing.T) {
		mock := NewMockClient()
		mock.ListDNSRecordsFunc = func(context.Context, string) ([]DNSRecord, error) {
			return []DNSRecord{
				{
					ID:      "record-1",
					Name:    "app.example.com",
					Type:    "CNAME",
					Content: "target.example.com",
					Comment: "managed by cfgate",
				},
				{
					ID:      "txt-1",
					Name:    "_cfgate.app.example.com",
					Type:    "TXT",
					Content: "heritage=cfgate,cfgate/owner=default/dns,cfgate/resource=CloudflareDNS/default/dns",
					Comment: "cfgate ownership record",
				},
			}, nil
		}

		records, err := NewDNSService(mock, logrDiscard()).ListManagedRecords(ctx, "zone-1", "default/dns", "_cfgate")
		if err != nil {
			t.Fatalf("ListManagedRecords() error = %v", err)
		}
		if len(records) != 2 {
			t.Fatalf("len(ListManagedRecords()) = %d, want 2", len(records))
		}
	})

	t.Run("skips comment-only record with different companion txt owner", func(t *testing.T) {
		mock := NewMockClient()
		mock.ListDNSRecordsFunc = func(context.Context, string) ([]DNSRecord, error) {
			return []DNSRecord{
				{
					ID:      "record-1",
					Name:    "app.example.com",
					Type:    "CNAME",
					Content: "target.example.com",
					Comment: "managed by cfgate",
				},
				{
					ID:      "txt-1",
					Name:    "_cfgate.app.example.com",
					Type:    "TXT",
					Content: "heritage=cfgate,cfgate/owner=default/other,cfgate/resource=CloudflareDNS/default/other",
					Comment: "cfgate ownership record",
				},
			}, nil
		}

		records, err := NewDNSService(mock, logrDiscard()).ListManagedRecords(ctx, "zone-1", "default/dns", "_cfgate")
		if err != nil {
			t.Fatalf("ListManagedRecords() error = %v", err)
		}
		if len(records) != 0 {
			t.Fatalf("ListManagedRecords() = %#v, want no matching records", records)
		}
	})
}

func TestDeleteOwnershipRecord(t *testing.T) {
	ctx := context.Background()

	t.Run("deletes matching ownership record", func(t *testing.T) {
		mock := NewMockClient()
		deleted := ""
		mock.ListDNSRecordsByNameTypeFunc = func(_ context.Context, _, name, recordType string) ([]DNSRecord, error) {
			if name != "_cfgate.app.example.com" || recordType != "TXT" {
				t.Fatalf("lookup = (%q, %q), want ownership TXT lookup", name, recordType)
			}
			return []DNSRecord{{
				ID:      "txt-1",
				Name:    name,
				Type:    "TXT",
				Content: "heritage=cfgate,cfgate/owner=default/dns,cfgate/resource=CloudflareDNS/default/dns",
				Comment: "cfgate ownership record",
			}}, nil
		}
		mock.DeleteDNSRecordFunc = func(_ context.Context, _, recordID string) error {
			deleted = recordID
			return nil
		}

		if err := NewDNSService(mock, logrDiscard()).DeleteOwnershipRecord(ctx, "zone-1", "app.example.com", "_cfgate", "default/dns"); err != nil {
			t.Fatalf("DeleteOwnershipRecord() error = %v", err)
		}
		if deleted != "txt-1" {
			t.Fatalf("deleted record ID = %q, want %q", deleted, "txt-1")
		}
	})

	t.Run("skips ownership record for different owner", func(t *testing.T) {
		mock := NewMockClient()
		deleted := false
		mock.ListDNSRecordsByNameTypeFunc = func(_ context.Context, _, _, _ string) ([]DNSRecord, error) {
			return []DNSRecord{{
				ID:      "txt-1",
				Name:    "_cfgate.app.example.com",
				Type:    "TXT",
				Content: "heritage=cfgate,cfgate/owner=default/other,cfgate/resource=CloudflareDNS/default/other",
				Comment: "cfgate ownership record",
			}}, nil
		}
		mock.DeleteDNSRecordFunc = func(context.Context, string, string) error {
			deleted = true
			return nil
		}

		if err := NewDNSService(mock, logrDiscard()).DeleteOwnershipRecord(ctx, "zone-1", "app.example.com", "_cfgate", "default/dns"); err != nil {
			t.Fatalf("DeleteOwnershipRecord() error = %v", err)
		}
		if deleted {
			t.Fatal("DeleteOwnershipRecord() deleted record for different owner")
		}
	})
}

func logrDiscard() logr.Logger {
	return logr.Discard()
}

func cfError(statusCode int, codes ...int64) error {
	errs := make([]shared.ErrorData, len(codes))
	for i, code := range codes {
		errs[i] = shared.ErrorData{Code: code}
	}
	return &cf.Error{StatusCode: statusCode, Errors: errs}
}

func TestIsDuplicateRecordError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"code 81053", cfError(400, ErrCodeRecordAlreadyExists), true},
		{"code 81058", cfError(400, ErrCodeIdenticalRecordExists), true},
		{"both codes", cfError(400, ErrCodeRecordAlreadyExists, ErrCodeIdenticalRecordExists), true},
		{"unrelated code", cfError(400, 9999), false},
		{"non-cf error", fmt.Errorf("some other error"), false},
		{"404 without matching code", cfError(404, 81044), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDuplicateRecordError(tt.err); got != tt.want {
				t.Errorf("IsDuplicateRecordError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsRecordNotFoundError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"404 status", cfError(http.StatusNotFound), true},
		{"code 81044", cfError(400, ErrCodeRecordNotFound), true},
		{"404 with code 81044", cfError(http.StatusNotFound, ErrCodeRecordNotFound), true},
		{"unrelated", cfError(400, 9999), false},
		{"non-cf error", fmt.Errorf("permission denied"), false},
		{"500 status no codes", cfError(500), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRecordNotFoundError(tt.err); got != tt.want {
				t.Errorf("IsRecordNotFoundError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDNSRecordCache(t *testing.T) {
	t.Run("new cache is empty", func(t *testing.T) {
		cache := NewDNSRecordCache()
		if cache == nil {
			t.Fatal("NewDNSRecordCache() returned nil")
		}
		r, ok := cache.Get("z1", "app.example.com", "CNAME")
		if ok {
			t.Error("Get on empty cache: ok = true, want false")
		}
		if r != nil {
			t.Error("Get on empty cache: record is non-nil")
		}
	})

	t.Run("set then get", func(t *testing.T) {
		cache := NewDNSRecordCache()
		record := &DNSRecord{ID: "r1", Name: "app.example.com", Type: "CNAME", Content: "target"}
		cache.Set("z1", "app.example.com", "CNAME", record)

		got, ok := cache.Get("z1", "app.example.com", "CNAME")
		if !ok {
			t.Fatal("Get after Set: ok = false, want true")
		}
		if got != record {
			t.Error("Get after Set: returned different pointer")
		}
	})

	t.Run("negative cache", func(t *testing.T) {
		cache := NewDNSRecordCache()
		cache.Set("z1", "missing.com", "A", nil)

		got, ok := cache.Get("z1", "missing.com", "A")
		if !ok {
			t.Fatal("negative cache: ok = false, want true")
		}
		if got != nil {
			t.Error("negative cache: record is non-nil, want nil")
		}
	})

	t.Run("cache miss different key", func(t *testing.T) {
		cache := NewDNSRecordCache()
		cache.Set("z1", "a.com", "CNAME", &DNSRecord{ID: "r1"})

		_, ok := cache.Get("z1", "b.com", "CNAME")
		if ok {
			t.Error("different key: ok = true, want false")
		}
	})

	t.Run("overwrite", func(t *testing.T) {
		cache := NewDNSRecordCache()
		r1 := &DNSRecord{ID: "r1"}
		r2 := &DNSRecord{ID: "r2"}
		cache.Set("z1", "a.com", "CNAME", r1)
		cache.Set("z1", "a.com", "CNAME", r2)

		got, ok := cache.Get("z1", "a.com", "CNAME")
		if !ok {
			t.Fatal("overwrite: ok = false")
		}
		if got != r2 {
			t.Errorf("overwrite: got ID %q, want %q", got.ID, r2.ID)
		}
	})

	t.Run("key format", func(t *testing.T) {
		cache := NewDNSRecordCache()
		got := cache.key("zone1", "app.com", "CNAME")
		want := "zone1:app.com:CNAME"
		if got != want {
			t.Errorf("key() = %q, want %q", got, want)
		}
	})
}

func TestPolicyChecker(t *testing.T) {
	tests := []struct {
		name       string
		policy     DNSPolicy
		wantCreate bool
		wantUpdate bool
		wantDelete bool
	}{
		{"sync", PolicySync, true, true, true},
		{"upsert-only", PolicyUpsertOnly, true, true, false},
		{"create-only", PolicyCreateOnly, true, false, false},
		{"empty string", DNSPolicy(""), true, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := &PolicyChecker{policy: tt.policy}
			if got := checker.AllowsCreate(); got != tt.wantCreate {
				t.Errorf("AllowsCreate() = %v, want %v", got, tt.wantCreate)
			}
			if got := checker.AllowsUpdate(); got != tt.wantUpdate {
				t.Errorf("AllowsUpdate() = %v, want %v", got, tt.wantUpdate)
			}
			if got := checker.AllowsDelete(); got != tt.wantDelete {
				t.Errorf("AllowsDelete() = %v, want %v", got, tt.wantDelete)
			}
		})
	}
}

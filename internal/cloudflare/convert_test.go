package cloudflare

import (
	"testing"

	"github.com/cloudflare/cloudflare-go/v6/zero_trust"
)

func TestParseDurationSeconds(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		defaultSec int64
		want       int64
	}{
		{name: "valid duration", input: "45s", defaultSec: 30, want: 45},
		{name: "valid minutes", input: "2m", defaultSec: 30, want: 120},
		{name: "invalid duration falls back", input: "bad", defaultSec: 30, want: 30},
		{name: "zero duration falls back", input: "0s", defaultSec: 30, want: 30},
		{name: "negative duration falls back", input: "-5s", defaultSec: 30, want: 30},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseDurationSeconds(tt.input, tt.defaultSec); got != tt.want {
				t.Fatalf("parseDurationSeconds(%q, %d) = %d, want %d", tt.input, tt.defaultSec, got, tt.want)
			}
		})
	}
}

func TestCORSHeadersFromSDK(t *testing.T) {
	input := &zero_trust.CORSHeaders{
		AllowAllHeaders:  true,
		AllowAllMethods:  false,
		AllowAllOrigins:  true,
		AllowCredentials: true,
		AllowedHeaders:   []zero_trust.AllowedHeaders{"X-Test", "X-Trace"},
		AllowedMethods:   []zero_trust.AllowedMethods{"GET", "POST"},
		AllowedOrigins:   []zero_trust.AllowedOrigins{"https://app.example.com"},
		MaxAge:           600,
	}

	got := corsHeadersFromSDK(input)
	if got == nil {
		t.Fatal("corsHeadersFromSDK() = nil")
		return
	}
	if !got.AllowAllHeaders || !got.AllowAllOrigins || !got.AllowCredentials {
		t.Fatalf("corsHeadersFromSDK() flags = %+v", got)
	}
	if got.MaxAge != 600 {
		t.Fatalf("MaxAge = %d, want 600", got.MaxAge)
	}
	if len(got.AllowedHeaders) != 2 || got.AllowedHeaders[0] != "X-Test" {
		t.Fatalf("AllowedHeaders = %#v", got.AllowedHeaders)
	}
	if len(got.AllowedMethods) != 2 || got.AllowedMethods[1] != "POST" {
		t.Fatalf("AllowedMethods = %#v", got.AllowedMethods)
	}
	if len(got.AllowedOrigins) != 1 || got.AllowedOrigins[0] != "https://app.example.com" {
		t.Fatalf("AllowedOrigins = %#v", got.AllowedOrigins)
	}
}

func TestExtractAllowedIdPs(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
		want  []string
	}{
		{name: "string slice", input: []string{"idp-1", "idp-2"}, want: []string{"idp-1", "idp-2"}},
		{name: "interface slice", input: []interface{}{"idp-1", "idp-2", 42}, want: []string{"idp-1", "idp-2"}},
		{name: "unsupported type", input: 123, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractAllowedIdPs(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("extractAllowedIdPs() = %#v, want %#v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("extractAllowedIdPs() = %#v, want %#v", got, tt.want)
				}
			}
		})
	}
}

func TestApprovalGroupsFromAPI(t *testing.T) {
	input := []zero_trust.ApprovalGroup{
		{
			EmailAddresses:  []string{"alice@example.com", "bob@example.com"},
			EmailListUUID:   "list-1",
			ApprovalsNeeded: 2,
		},
		{
			EmailAddresses:  nil,
			EmailListUUID:   "list-2",
			ApprovalsNeeded: 1,
		},
	}

	got := approvalGroupsFromAPI(input)
	if len(got) != 2 {
		t.Fatalf("len(approvalGroupsFromAPI()) = %d, want 2", len(got))
	}
	if got[0].ApprovalsNeeded != 2 || got[0].EmailListUUID != "list-1" {
		t.Fatalf("first approval group = %+v", got[0])
	}
	if got[1].EmailListUUID != "list-2" {
		t.Fatalf("second approval group = %+v", got[1])
	}
}

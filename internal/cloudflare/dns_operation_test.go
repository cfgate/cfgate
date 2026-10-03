package cloudflare

import (
	"strings"
	"testing"
)

func TestDNSOperationOwnershipMarkers(t *testing.T) {
	owner := strings.Repeat("a", 36) + "/" + strings.Repeat("b", 36)
	marker := OwnershipComment(owner) + ",op=0123456789"
	if len(marker) != 100 {
		t.Fatalf("provider comment budget changed: %d", len(marker))
	}
	for _, test := range []struct {
		comment   string
		owned     bool
		operation string
	}{
		{OwnershipComment(owner), true, ""},
		{marker, true, "0123456789"},
		{marker + ",cfgate/owner=other", false, ""},
		{OwnershipComment(owner) + ",op=short", false, ""},
		{OwnershipComment(owner) + ",op=012345678!", false, ""},
		{OwnershipComment("foreign") + ",op=0123456789", false, "0123456789"},
	} {
		record := &DNSRecord{Type: "CNAME", Comment: test.comment}
		if IsOwnedByCfgate(record, owner) != test.owned || DNSRecordOperation(record) != test.operation {
			t.Fatalf("incorrect ownership interpretation: %q", test.comment)
		}
	}
}

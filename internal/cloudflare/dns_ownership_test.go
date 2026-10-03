package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-logr/logr"
	"testing"
)

func TestAmbiguousOwnershipMarkersNeverAuthorize(t *testing.T) {
	for _, record := range []DNSRecord{
		{Type: "CNAME", Comment: "heritage=cfgate-other,cfgate/owner=ours"},
		{Type: "CNAME", Comment: "heritage=cfgate,cfgate/owner=foreign,cfgate/owner=ours"},
		{Type: "TXT", Content: "unrelated text", Comment: OwnershipComment("ours")},
		{Type: "TXT", Content: "heritage=cfgate,cfgate/owner=foreign", Comment: OwnershipComment("ours")},
	} {
		if IsOwnedByCfgate(&record, "ours") {
			t.Fatalf("ambiguous marker authorized: %+v", record)
		}
	}
}

func TestDNSOwnershipConflictsHaveNoWrites(t *testing.T) {
	for _, tt := range []struct {
		name    string
		records []DNSRecord
		adopt   bool
	}{
		{"conflicting A", []DNSRecord{{ID: "a", Name: "app.example", Type: "A", Content: "192.0.2.1"}}, false},
		{"conflicting AAAA", []DNSRecord{{ID: "aaaa", Name: "app.example", Type: "AAAA", Content: "2001:db8::1"}}, true},
		{"foreign TXT", []DNSRecord{BuildOwnershipTXTRecord("app.example", "foreign", "resource", "_cfgate")}, false},
		{"foreign TXT cannot be adopted", []DNSRecord{BuildOwnershipTXTRecord("app.example", "foreign", "resource", "_cfgate")}, true},
		{"unmarked data", []DNSRecord{{ID: "data", Name: "app.example", Type: "CNAME", Content: "old", Comment: "managed by cfgate"}}, false},
		{"marked data cannot be adopted", []DNSRecord{{ID: "data", Name: "app.example", Type: "CNAME", Content: "old", Comment: OwnershipComment("foreign")}}, true},
		{"ambiguous claims", []DNSRecord{BuildOwnershipTXTRecord("app.example", "ours", "resource", "_cfgate"), BuildOwnershipTXTRecord("app.example", "foreign", "resource", "_cfgate")}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			writes := 0
			mock := NewMockClient()
			mock.ListDNSRecordsByNameTypeFunc = func(_ context.Context, _, name, kind string) ([]DNSRecord, error) {
				var found []DNSRecord
				for _, record := range tt.records {
					if record.Name == name && record.Type == kind {
						found = append(found, record)
					}
				}
				return found, nil
			}
			mock.CreateDNSRecordFunc = func(_ context.Context, _ string, record DNSRecord) (*DNSRecord, error) { writes++; return &record, nil }
			mock.UpdateDNSRecordFunc = func(_ context.Context, _, _ string, record DNSRecord) (*DNSRecord, error) {
				writes++
				return &record, nil
			}
			_, _, err := NewDNSService(mock, logr.Discard()).SyncOwnedRecord(context.Background(), "zone", DNSRecord{Name: "app.example", Type: "CNAME", Content: "new"}, "ours", "resource", "_cfgate", PolicySync, true, tt.adopt)
			if err == nil || writes != 0 {
				t.Fatalf("conflict err=%v writes=%d", err, writes)
			}
		})
	}
}

func TestDNSPolicySkipAndForeignClaimDeletion(t *testing.T) {
	data := DNSRecord{ID: "data", Name: "app.example", Type: "CNAME", Content: "old", Comment: OwnershipComment("ours")}
	var claim *DNSRecord
	writes := 0
	mock := NewMockClient()
	mock.ListDNSRecordsByNameTypeFunc = func(_ context.Context, _, name, kind string) ([]DNSRecord, error) {
		if name == data.Name && kind == data.Type {
			return []DNSRecord{data}, nil
		}
		if claim != nil && name == claim.Name && kind == claim.Type {
			return []DNSRecord{*claim}, nil
		}
		return nil, nil
	}
	mock.UpdateDNSRecordFunc = func(_ context.Context, _, _ string, record DNSRecord) (*DNSRecord, error) {
		writes++
		return &record, nil
	}
	mock.DeleteDNSRecordFunc = func(context.Context, string, string) error { writes++; return nil }
	mock.CreateDNSRecordFunc = func(_ context.Context, _ string, record DNSRecord) (*DNSRecord, error) {
		if record.Type != "TXT" {
			t.Fatal("create-only drift mutated data")
		}
		record.ID = "claim"
		claim = &record
		return claim, nil
	}
	svc := NewDNSService(mock, logr.Discard())
	desired := data
	desired.Content = "new"
	_, changed, err := svc.SyncOwnedRecord(context.Background(), "zone", desired, "ours", "resource", "_cfgate", PolicyCreateOnly, true, false)
	if !errors.Is(err, ErrDNSRecordSkipped) || changed || writes != 0 {
		t.Fatalf("skipped err=%v changed=%v writes=%d", err, changed, writes)
	}
	if claim == nil || !IsOwnedByCfgate(claim, "ours") {
		t.Fatal("create-only drift did not repair missing ownership claim")
	}
	foreign := BuildOwnershipTXTRecord(data.Name, "foreign", "resource", "_cfgate")
	claim = &foreign
	deleted, err := svc.DeleteOwnedRecord(context.Background(), "zone", data, "ours", "_cfgate")
	if err == nil || deleted || writes != 0 {
		t.Fatalf("foreign claim deletion err=%v deleted=%v writes=%d", err, deleted, writes)
	}
}

func TestDNSConcurrentClaimDoesNotOverwriteWinner(t *testing.T) {
	for _, claimWins := range []bool{true, false} {
		t.Run(map[bool]string{true: "TXT race", false: "data race"}[claimWins], func(t *testing.T) {
			var records []DNSRecord
			dataWrites := 0
			updates := 0
			mock := NewMockClient()
			mock.ListDNSRecordsByNameTypeFunc = func(_ context.Context, _, name, kind string) ([]DNSRecord, error) {
				var found []DNSRecord
				for _, record := range records {
					if record.Name == name && record.Type == kind {
						found = append(found, record)
					}
				}
				return found, nil
			}
			mock.CreateDNSRecordFunc = func(_ context.Context, _ string, record DNSRecord) (*DNSRecord, error) {
				if record.Type != "TXT" {
					dataWrites++
					return &record, nil
				}
				if claimWins {
					foreign := BuildOwnershipTXTRecord("app.example", "foreign", "resource", "_cfgate")
					foreign.ID = "winner"
					records = append(records, foreign)
					return nil, errors.New("81058 identical record exists")
				}
				record.ID = "own-claim"
				records = append(records, record, DNSRecord{ID: "winner", Name: "app.example", Type: "CNAME", Content: "foreign", Comment: OwnershipComment("foreign")})
				return &record, nil
			}
			mock.UpdateDNSRecordFunc = func(_ context.Context, _, _ string, record DNSRecord) (*DNSRecord, error) {
				updates++
				return &record, nil
			}
			_, _, err := NewDNSService(mock, logr.Discard()).SyncOwnedRecord(context.Background(), "zone", DNSRecord{Name: "app.example", Type: "CNAME", Content: "desired"}, "ours", "resource", "_cfgate", PolicySync, true, false)
			if err == nil || dataWrites != 0 || updates != 0 {
				t.Fatalf("concurrent ownership err=%v datawrites=%d updates=%d", err, dataWrites, updates)
			}
		})
	}
}

func TestDNSExplicitUnmarkedAdoptionAndDeletion(t *testing.T) {
	records := map[string]DNSRecord{"app.example": {ID: "data", Name: "app.example", Type: "CNAME", Content: "legacy", Comment: "managed by cfgate"}}
	mock := NewMockClient()
	mock.ListDNSRecordsByNameTypeFunc = func(_ context.Context, _, name, kind string) ([]DNSRecord, error) {
		record, ok := records[name]
		if !ok || record.Type != kind {
			return nil, nil
		}
		return []DNSRecord{record}, nil
	}
	mock.CreateDNSRecordFunc = func(_ context.Context, _ string, record DNSRecord) (*DNSRecord, error) {
		record.ID = "claim"
		records[record.Name] = record
		return &record, nil
	}
	mock.UpdateDNSRecordFunc = func(_ context.Context, _, id string, record DNSRecord) (*DNSRecord, error) {
		record.ID = id
		records[record.Name] = record
		return &record, nil
	}
	deletes := 0
	mock.DeleteDNSRecordFunc = func(context.Context, string, string) error { deletes++; return nil }
	service := NewDNSService(mock, logr.Discard())
	record, changed, err := service.SyncOwnedRecord(context.Background(), "zone", DNSRecord{Name: "app.example", Type: "CNAME", Content: "desired"}, "ours", "resource", "_cfgate", PolicySync, true, true)
	if err != nil || !changed || !IsOwnedByCfgate(record, "ours") {
		t.Fatalf("adoption changed=%v err=%v record=%+v", changed, err, record)
	}
	foreign := *record
	foreign.Comment = OwnershipComment("foreign")
	records[record.Name] = foreign
	deleted, err := service.DeleteOwnedRecord(context.Background(), "zone", *record, "ours", "_cfgate")
	if err != nil || deleted || deletes != 0 {
		t.Fatalf("changed ownership deleted=%v calls=%d err=%v", deleted, deletes, err)
	}
}

func TestDNSNoopRequiresFreshOwnedPair(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(fmt.Sprint(foreign), func(t *testing.T) {
			record := BuildDNSRecord("app.example.com", "target.example.net", "CNAME", false, 1, OwnershipComment("ours")+",op=0123456789")
			record.ID = "data"
			claim := BuildOwnershipTXTRecord(record.Name, "ours", "resource", "_cfgate")
			cache := NewDNSRecordCache()
			cache.Set("zone", record.Name, "CNAME", &record)
			cache.Set("zone", claim.Name, "TXT", &claim)
			remoteClaim := claim
			if foreign {
				remoteClaim = BuildOwnershipTXTRecord(record.Name, "foreign", "resource", "_cfgate")
			}
			reads := 0
			mock := NewMockClient()
			mock.ListDNSRecordsByNameTypeFunc = func(_ context.Context, _, name, kind string) ([]DNSRecord, error) {
				reads++
				if name == record.Name && kind == "CNAME" {
					return []DNSRecord{record}, nil
				}
				if name == claim.Name && kind == "TXT" {
					return []DNSRecord{remoteClaim}, nil
				}
				return nil, nil
			}
			mock.CreateDNSRecordFunc = func(context.Context, string, DNSRecord) (*DNSRecord, error) {
				t.Fatal("unchanged pair must not write")
				return nil, nil
			}
			mock.UpdateDNSRecordFunc = func(context.Context, string, string, DNSRecord) (*DNSRecord, error) {
				t.Fatal("unchanged pair must not write")
				return nil, nil
			}
			result, changed, err := NewDNSService(mock, logr.Discard()).WithCache(cache).SyncOwnedRecord(context.Background(), "zone", record, "ours", "resource", "_cfgate", PolicySync, true, false)
			if foreign {
				if err == nil {
					t.Fatal("stale cached ownership accepted")
				}
			} else if err != nil || changed || result.ID != record.ID {
				t.Fatalf("unexpected no-op: result=%v changed=%v error=%v", result, changed, err)
			}
			if reads != 4 {
				t.Fatalf("expected fresh incompatible/data/claim reads, got %d", reads)
			}
		})
	}
}

func TestOwnedDNSProxyTTLTransitions(t *testing.T) {
	var current *DNSRecord
	writes := 0
	mock := NewMockClient()
	mock.ListDNSRecordsByNameTypeFunc = func(_ context.Context, _, name, kind string) ([]DNSRecord, error) {
		if current != nil && current.Name == name && current.Type == kind {
			return []DNSRecord{*current}, nil
		}
		return nil, nil
	}
	persist := func(record DNSRecord) (*DNSRecord, error) {
		writes++
		if record.Proxied && record.TTL != 1 {
			t.Fatalf("proxied TTL sent to provider: %d", record.TTL)
		}
		record.ID = "record-id"
		current = &record
		return current, nil
	}
	mock.CreateDNSRecordFunc = func(_ context.Context, _ string, record DNSRecord) (*DNSRecord, error) { return persist(record) }
	mock.UpdateDNSRecordFunc = func(_ context.Context, _, _ string, record DNSRecord) (*DNSRecord, error) { return persist(record) }
	svc := NewDNSService(mock, logr.Discard())
	for _, proxied := range []bool{true, false, true} {
		desired := BuildDNSRecord("app.example.com", "target.example.com", "CNAME", proxied, 3600, "")
		for pass := range 2 {
			before := writes
			record, _, err := svc.SyncOwnedRecord(context.Background(), "zone", desired, "ours", "resource", "_cfgate", PolicySync, false, false)
			if err != nil {
				t.Fatal(err)
			}
			wantTTL := 3600
			if proxied {
				wantTTL = 1
			}
			if record.TTL != wantTTL {
				t.Fatalf("TTL=%d, want %d", record.TTL, wantTTL)
			}
			if pass == 1 && writes != before {
				t.Fatal("converged record was rewritten")
			}
		}
	}
	if writes != 3 {
		t.Fatalf("writes=%d, want one per transition", writes)
	}
}

package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"

	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
)

func TestPartialDNSClaimCleanup(t *testing.T) {
	for _, mode := range []string{"owned claim", "foreign claim", "ownership disabled", "delete retry", "unrecorded data"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store := map[string]cloudflare.DNSRecord{}
			deletes := 0
			failDelete := mode == "delete retry"
			mock := cloudflare.NewMockClient()
			mock.ListDNSRecordsByNameTypeFunc = func(_ context.Context, zone, name, kind string) ([]cloudflare.DNSRecord, error) {
				var result []cloudflare.DNSRecord
				for _, r := range store {
					if r.Name == name && r.Type == kind {
						result = append(result, r)
					}
				}
				return result, nil
			}
			mock.CreateDNSRecordFunc = func(_ context.Context, _ string, r cloudflare.DNSRecord) (*cloudflare.DNSRecord, error) {
				if r.Type != "TXT" {
					return nil, errors.New("data create HTTP400")
				}
				r.ID = "owned-txt"
				store[r.ID] = r
				return &r, nil
			}
			mock.DeleteDNSRecordFunc = func(_ context.Context, _ string, id string) error {
				deletes++
				if failDelete {
					failDelete = false
					return errors.New("transient delete failure")
				}
				delete(store, id)
				return nil
			}
			mock.ListDNSRecordsFunc = func(context.Context, string) ([]cloudflare.DNSRecord, error) {
				var result []cloudflare.DNSRecord
				for _, r := range store {
					result = append(result, r)
				}
				return result, nil
			}
			dns := &cfg.CloudflareDNS{Spec: cfg.CloudflareDNSSpec{Cloudflare: &cfg.CloudflareConfig{SecretRef: cfg.SecretRef{Name: "credentials"}}, Zones: []cfg.DNSZoneConfig{{Name: "example.com", ID: "zone"}}}, Status: cfg.CloudflareDNSStatus{OwnerID: "installation/resource"}}
			r := &CloudflareDNSReconciler{CFClient: mock, Recorder: &fakeEventRecorder{}}
			if err := r.syncRecords(ctx, dns, "tunnel.example", map[string]HostnameConfig{"new.example.com": {}}, map[string]string{"example.com": "zone"}, cloudflare.NewDNSService(mock, logr.Discard())); err != nil {
				t.Fatal(err)
			}
			if len(store) != 1 || len(dns.Status.Records) != 1 || dns.Status.Records[0].Status != "Failed" {
				t.Fatalf("partial-creation precondition failed: store=%v status=%+v", store, dns.Status.Records)
			}
			switch mode {
			case "foreign claim":
				record := store["owned-txt"]
				record.Content = "heritage=cfgate,cfgate/owner=foreign,cfgate/resource=other"
				store[record.ID] = record
			case "ownership disabled":
				disabled := false
				dns.Spec.Ownership.TXTRecord.Enabled = &disabled
			case "unrecorded data":
				store["foreign-data"] = cloudflare.DNSRecord{ID: "foreign-data", Name: "new.example.com", Type: "CNAME", Content: "foreign", Comment: cloudflare.OwnershipComment("foreign")}
			}
			err := r.cleanupRecordsWithFallback(ctx, dns)
			if mode == "delete retry" {
				if err == nil || len(store) != 1 {
					t.Fatalf("retry should retain failed claim: err=%v store=%v", err, store)
				}
				err = r.cleanupRecordsWithFallback(ctx, dns)
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "owned claim" || mode == "delete retry" {
				if len(store) != 0 || deletes == 0 {
					t.Fatalf("owned claim leaked: status=%+v deletes=%d", dns.Status.Records, deletes)
				}
			} else if deletes != 0 {
				t.Fatalf("protected records deleted: mode=%s count=%d", mode, deletes)
			}
		})
	}
}

func TestDNSCrashBeforeStatus(t *testing.T) {
	for _, hasOldStatus := range []bool{false, true} {
		name := "empty prior status"
		if hasOldStatus {
			name = "existing prior status"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := map[string]cloudflare.DNSRecord{}
			mock := cloudflare.NewMockClient()
			mock.ListDNSRecordsByNameTypeFunc = func(_ context.Context, zone, name, kind string) ([]cloudflare.DNSRecord, error) {
				var result []cloudflare.DNSRecord
				for _, r := range store {
					if r.Name == name && r.Type == kind {
						result = append(result, r)
					}
				}
				return result, nil
			}
			mock.ListDNSRecordsFunc = func(context.Context, string) ([]cloudflare.DNSRecord, error) {
				var result []cloudflare.DNSRecord
				for _, r := range store {
					result = append(result, r)
				}
				return result, nil
			}
			mock.CreateDNSRecordFunc = func(_ context.Context, _ string, r cloudflare.DNSRecord) (*cloudflare.DNSRecord, error) {
				if r.Type != "TXT" {
					panic("simulated process exit before data write/status update")
				}
				r.ID = "owned-txt"
				store[r.ID] = r
				return &r, nil
			}
			mock.DeleteDNSRecordFunc = func(_ context.Context, _ string, id string) error { delete(store, id); return nil }
			dns := &cfg.CloudflareDNS{Spec: cfg.CloudflareDNSSpec{Cloudflare: &cfg.CloudflareConfig{SecretRef: cfg.SecretRef{Name: "credentials"}}, Zones: []cfg.DNSZoneConfig{{Name: "example.com", ID: "zone"}}}, Status: cfg.CloudflareDNSStatus{OwnerID: "installation/resource"}}
			if hasOldStatus {
				dns.Status.Records = []cfg.DNSRecordSyncStatus{{Hostname: "old.example.com", Type: "CNAME", RecordID: "old-data", ZoneID: "zone", Status: "Synced"}}
			}
			r := &CloudflareDNSReconciler{CFClient: mock, Recorder: &fakeEventRecorder{}}
			interrupted := false
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						if recovered != "simulated process exit before data write/status update" {
							panic(recovered)
						}
						interrupted = true
					}
				}()
				_ = r.syncRecords(ctx, dns, "tunnel.example", map[string]HostnameConfig{"new.example.com": {}}, map[string]string{"example.com": "zone"}, cloudflare.NewDNSService(mock, logr.Discard()))
			}()
			if !interrupted || len(store) != 1 {
				t.Fatal("interruption precondition missing")
			}
			if err := r.cleanupRecordsWithFallback(ctx, dns); err != nil {
				t.Fatal(err)
			}
			if len(store) != 0 {
				t.Fatalf("claim survives pre-status interruption with prior inventory: %+v", dns.Status.Records)
			}
		})
	}
}

// Recovery must inventory every known zone before deleting an unrecorded claim.
func TestDNSUnrecordedClaimsInventoryAndRetry(t *testing.T) {
	ctx := context.Background()
	const owner = "installation/resource"
	claim := func(id, host, claimOwner string) cloudflare.DNSRecord {
		record := cloudflare.BuildOwnershipTXTRecord(host, claimOwner, "CloudflareDNS/default/dns", "_cfgate")
		record.ID = id
		return record
	}
	store := map[string]map[string]cloudflare.DNSRecord{
		"old-zone": {"recover": claim("recover", "new.example.com", owner),
			"foreign":              claim("foreign", "foreign.example.com", "another-owner"),
			"ambiguous-own":        claim("ambiguous-own", "ambiguous.example.com", owner),
			"ambiguous-other":      claim("ambiguous-other", "ambiguous.example.com", "another-owner"),
			"unrelated":            {ID: "unrelated", Name: "_other.example.com", Type: "TXT", Content: "unrelated"},
			"data":                 {ID: "data", Name: "data.example.com", Type: "CNAME", Comment: cloudflare.OwnershipComment(owner)},
			"recorded-claim":       claim("recorded-claim", "recorded.example.com", owner),
			"recorded-replacement": {ID: "recorded-replacement", Name: "recorded.example.com", Type: "CNAME", Comment: cloudflare.OwnershipComment(owner)},
		},
		"configured-zone": {"recover2": claim("recover2", "second.example.net", owner)},
	}
	mock := cloudflare.NewMockClient()
	failInventory := true
	var deleted []string
	mock.ListDNSRecordsFunc = func(_ context.Context, zone string) ([]cloudflare.DNSRecord, error) {
		if _, ok := store[zone]; !ok {
			t.Fatalf("unexpected zone queried: %s", zone)
		}
		var records []cloudflare.DNSRecord
		for _, record := range store[zone] {
			records = append(records, record)
		}
		if zone == "configured-zone" && failInventory {
			return records, errors.New("incomplete inventory")
		}
		return records, nil
	}
	mock.ListDNSRecordsByNameTypeFunc = func(_ context.Context, zone, name, kind string) ([]cloudflare.DNSRecord, error) {
		var records []cloudflare.DNSRecord
		for _, record := range store[zone] {
			if record.Name == name && record.Type == kind {
				records = append(records, record)
			}
		}
		return records, nil
	}
	mock.DeleteDNSRecordFunc = func(_ context.Context, zone, id string) error {
		deleted = append(deleted, id)
		delete(store[zone], id)
		return nil
	}
	dns := &cfg.CloudflareDNS{
		Spec:   cfg.CloudflareDNSSpec{Cloudflare: &cfg.CloudflareConfig{SecretRef: cfg.SecretRef{Name: "credentials"}}, Zones: []cfg.DNSZoneConfig{{Name: "example.net", ID: "configured-zone"}}},
		Status: cfg.CloudflareDNSStatus{OwnerID: owner, Records: []cfg.DNSRecordSyncStatus{{Hostname: "recorded.example.com", Type: "CNAME", RecordID: "old-id", ZoneID: "old-zone", Status: "Synced"}}},
	}
	r := &CloudflareDNSReconciler{CFClient: mock}
	if err := r.cleanupRecordsWithFallback(ctx, dns); err == nil {
		t.Fatal("partial inventory must retain cleanup for retry")
	}
	if len(deleted) != 0 {
		t.Fatalf("mutated claims before complete inventory: %v", deleted)
	}
	failInventory = false
	if err := r.cleanupRecordsWithFallback(ctx, dns); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 2 {
		t.Fatalf("want exactly two unrecorded claims deleted, got %v", deleted)
	}
	for _, id := range deleted {
		if id != "recover" && id != "recover2" {
			t.Fatalf("protected record deleted: %s", id)
		}
	}
	if len(store["old-zone"]) != 7 || len(store["configured-zone"]) != 0 {
		t.Fatal("unexpected remaining remote records")
	}
}

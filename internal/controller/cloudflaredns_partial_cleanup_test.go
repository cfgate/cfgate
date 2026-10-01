package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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
		for _, phase := range []string{"claim only", "data with claim", "data without claim"} {
			t.Run(fmt.Sprintf("old status=%t/%s", hasOldStatus, phase), func(t *testing.T) {
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
					if r.Type != "TXT" && phase == "claim only" {
						panic("simulated process exit before status update")
					}
					r.ID = "owned-" + r.Type
					store[r.ID] = r
					if r.Type != "TXT" {
						panic("simulated process exit before status update")
					}
					return &r, nil
				}
				mock.DeleteDNSRecordFunc = func(_ context.Context, _ string, id string) error {
					if id == "owned-TXT" && store["owned-CNAME"].ID != "" {
						t.Fatal("ownership claim deleted before data")
					}
					delete(store, id)
					return nil
				}
				dns := &cfg.CloudflareDNS{Spec: cfg.CloudflareDNSSpec{Cloudflare: &cfg.CloudflareConfig{SecretRef: cfg.SecretRef{Name: "credentials"}}, Zones: []cfg.DNSZoneConfig{{Name: "example.com", ID: "zone"}}}, Status: cfg.CloudflareDNSStatus{OwnerID: "installation/resource"}}
				if hasOldStatus {
					dns.Status.Records = []cfg.DNSRecordSyncStatus{{Hostname: "old.example.com", Type: "CNAME", RecordID: "old-data", ZoneID: "zone", Status: "Synced"}}
				}
				if phase == "data without claim" {
					disabled := false
					dns.Spec.Ownership.TXTRecord.Enabled = &disabled
				}
				r := &CloudflareDNSReconciler{CFClient: mock, Recorder: &fakeEventRecorder{}}
				persisted := dns.DeepCopy()
				interrupted := false
				func() {
					defer func() {
						if recovered := recover(); recovered != nil {
							if recovered != "simulated process exit before status update" {
								panic(recovered)
							}
							interrupted = true
						}
					}()
					_ = r.syncRecords(ctx, dns, "tunnel.example", map[string]HostnameConfig{"new.example.com": {}}, map[string]string{"example.com": "zone"}, cloudflare.NewDNSService(mock, logr.Discard()))
				}()
				wantRecords := 1
				if phase == "data with claim" {
					wantRecords = 2
				}
				if !interrupted || len(store) != wantRecords {
					t.Fatalf("interruption precondition missing: records=%v", store)
				}
				// A restarted controller sees only the previously persisted status.
				if err := r.cleanupRecordsWithFallback(ctx, persisted); err != nil {
					t.Fatal(err)
				}
				if len(store) != 0 {
					t.Fatalf("records survive pre-status interruption: %v", store)
				}
			})
		}
	}
}

// Recovery must inventory every known zone before deleting unrecorded data or claims.
func TestDNSUnrecordedRecordsInventoryAndRetry(t *testing.T) {
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
			"foreign-data":         {ID: "foreign-data", Name: "foreign.example.com", Type: "CNAME", Comment: cloudflare.OwnershipComment(owner)},
			"ambiguous-own":        claim("ambiguous-own", "ambiguous.example.com", owner),
			"ambiguous-other":      claim("ambiguous-other", "ambiguous.example.com", "another-owner"),
			"ambiguous-data":       {ID: "ambiguous-data", Name: "ambiguous.example.com", Type: "CNAME", Comment: cloudflare.OwnershipComment(owner)},
			"unmarked-data":        {ID: "unmarked-data", Name: "unmarked.example.com", Type: "CNAME", Comment: "managed by cfgate"},
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
	if len(deleted) != 3 {
		t.Fatalf("want owned data and two unrecorded claims deleted, got %v", deleted)
	}
	for _, id := range deleted {
		if id != "recover" && id != "recover2" && id != "data" {
			t.Fatalf("protected record deleted: %s", id)
		}
	}
	if len(store["old-zone"]) != 9 || len(store["configured-zone"]) != 0 {
		t.Fatal("unexpected remaining remote records")
	}
}

func TestDNSUnrecordedCleanupRetainsFinalizerOnFailure(t *testing.T) {
	for _, txtEnabled := range []bool{false, true} {
		for _, failure := range []string{"inventory", "data deletion", "claim deletion", "changed data"} {
			if !txtEnabled && failure == "claim deletion" {
				continue
			}
			t.Run(fmt.Sprintf("TXT=%t/%s", txtEnabled, failure), func(t *testing.T) {
				ctx := context.Background()
				const owner = "installation/resource"
				data := cloudflare.DNSRecord{ID: "data", Name: "new.example.com", Type: "CNAME", Content: "target.example.com", Comment: cloudflare.OwnershipComment(owner)}
				store := map[string]cloudflare.DNSRecord{data.ID: data}
				if txtEnabled {
					claim := cloudflare.BuildOwnershipTXTRecord(data.Name, owner, "CloudflareDNS/default/dns", "_cfgate")
					claim.ID = "claim"
					store[claim.ID] = claim
				}
				fail := true
				mock := cloudflare.NewMockClient()
				mock.ListDNSRecordsFunc = func(context.Context, string) ([]cloudflare.DNSRecord, error) {
					var result []cloudflare.DNSRecord
					for _, record := range store {
						result = append(result, record)
					}
					if fail && failure == "inventory" {
						return result, errors.New("partial inventory")
					}
					if fail && failure == "changed data" {
						delete(store, "data")
						changed := data
						changed.ID = "replacement"
						store[changed.ID] = changed
					}
					return result, nil
				}
				mock.ListDNSRecordsByNameTypeFunc = func(_ context.Context, _, name, kind string) ([]cloudflare.DNSRecord, error) {
					var result []cloudflare.DNSRecord
					for _, record := range store {
						if record.Name == name && record.Type == kind {
							result = append(result, record)
						}
					}
					return result, nil
				}
				mock.DeleteDNSRecordFunc = func(_ context.Context, _, id string) error {
					if id == "claim" && (store["data"].ID != "" || store["replacement"].ID != "") {
						t.Fatal("ownership claim removed before data")
					}
					if fail && ((failure == "data deletion" && id == "data") || (failure == "claim deletion" && id == "claim")) {
						return errors.New("transient deletion failure")
					}
					delete(store, id)
					return nil
				}
				now := metav1.Now()
				dns := &cfg.CloudflareDNS{
					ObjectMeta: metav1.ObjectMeta{Name: "dns", Namespace: "default", Finalizers: []string{dnsFinalizer}, DeletionTimestamp: &now},
					Spec: cfg.CloudflareDNSSpec{
						Policy:     cfg.DNSPolicySync,
						Cloudflare: &cfg.CloudflareConfig{SecretRef: cfg.SecretRef{Name: "credentials"}},
						Zones:      []cfg.DNSZoneConfig{{Name: "example.com", ID: "zone"}},
						Ownership:  cfg.DNSOwnershipConfig{TXTRecord: cfg.DNSTXTRecordOwnership{Enabled: &txtEnabled}},
					},
					Status: cfg.CloudflareDNSStatus{OwnerID: owner, Records: []cfg.DNSRecordSyncStatus{{Hostname: "old.example.com", Type: "CNAME", RecordID: "old-data", ZoneID: "zone"}}},
				}
				k8s := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithObjects(dns).Build()
				r := &CloudflareDNSReconciler{Client: k8s, CFClient: mock, Recorder: &fakeEventRecorder{}}
				result, err := r.reconcileDelete(ctx, dns)
				if err != nil || result.RequeueAfter == 0 {
					t.Fatalf("failed cleanup must requeue: result=%v err=%v", result, err)
				}
				var current cfg.CloudflareDNS
				key := client.ObjectKeyFromObject(dns)
				if err := k8s.Get(ctx, key, &current); err != nil || len(current.Finalizers) != 1 {
					t.Fatalf("cleanup lost finalizer: object=%+v err=%v", current, err)
				}
				if len(store) == 0 || (txtEnabled && store["claim"].ID == "") {
					t.Fatalf("failed cleanup lost recovery evidence: %v", store)
				}
				fail = false
				if result, err := r.reconcileDelete(ctx, &current); err != nil || result.RequeueAfter != 0 {
					t.Fatalf("retry failed: result=%v err=%v", result, err)
				}
				if len(store) != 0 {
					t.Fatalf("retry leaked records: %v", store)
				}
				if err := k8s.Get(ctx, key, &current); !apierrors.IsNotFound(err) {
					t.Fatalf("completed cleanup did not release finalizer: %v", err)
				}
			})
		}
	}
}

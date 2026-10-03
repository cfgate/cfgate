package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
)

// This store models zone and record identities independently so tests cannot
// accidentally delete a parent-zone record through the delegated child's API.
func dnsLifecycleStore(t *testing.T, store map[string]map[string]cloudflare.DNSRecord) *cloudflare.MockClient {
	t.Helper()
	mock := cloudflare.NewMockClient()
	sequence := 0
	mock.ListDNSRecordsByNameTypeFunc = func(_ context.Context, zone, name, kind string) ([]cloudflare.DNSRecord, error) {
		var records []cloudflare.DNSRecord
		for _, record := range store[zone] {
			if record.Name == name && record.Type == kind {
				records = append(records, record)
			}
		}
		return records, nil
	}
	mock.ListDNSRecordsFunc = func(_ context.Context, zone string) ([]cloudflare.DNSRecord, error) {
		var records []cloudflare.DNSRecord
		for _, record := range store[zone] {
			records = append(records, record)
		}
		return records, nil
	}
	mock.CreateDNSRecordFunc = func(_ context.Context, zone string, record cloudflare.DNSRecord) (*cloudflare.DNSRecord, error) {
		if store[zone] == nil {
			store[zone] = map[string]cloudflare.DNSRecord{}
		}
		sequence++
		record.ID = fmt.Sprintf("%s/%s/%s/%d", zone, record.Name, record.Type, sequence)
		if _, exists := store[zone][record.ID]; exists {
			t.Fatalf("duplicate create: %s", record.ID)
		}
		store[zone][record.ID] = record
		return &record, nil
	}
	mock.UpdateDNSRecordFunc = func(_ context.Context, zone, id string, record cloudflare.DNSRecord) (*cloudflare.DNSRecord, error) {
		if _, exists := store[zone][id]; !exists {
			t.Fatalf("update missing record %s/%s", zone, id)
		}
		record.ID = id
		store[zone][id] = record
		return &record, nil
	}
	mock.DeleteDNSRecordFunc = func(_ context.Context, zone, id string) error {
		if _, exists := store[zone][id]; !exists {
			t.Fatalf("delete missing record %s/%s", zone, id)
		}
		delete(store[zone], id)
		return nil
	}
	return mock
}

func TestDNSDelegatedZoneLifecycle(t *testing.T) {
	ctx := context.Background()
	store := map[string]map[string]cloudflare.DNSRecord{}
	mock := dnsLifecycleStore(t, store)
	dns := &cfg.CloudflareDNS{
		Spec: cfg.CloudflareDNSSpec{
			Cloudflare: &cfg.CloudflareConfig{SecretRef: cfg.SecretRef{Name: "credentials"}},
			Zones:      []cfg.DNSZoneConfig{{Name: "EXAMPLE.com.", ID: "parent"}, {Name: "Team.Example.COM.", ID: "child"}},
		},
		Status: cfg.CloudflareDNSStatus{OwnerID: "installation/resource"},
	}
	r := &CloudflareDNSReconciler{CFClient: mock, Recorder: &fakeEventRecorder{}}
	service := cloudflare.NewDNSService(mock, logr.Discard())
	zones, err := r.resolveZones(ctx, dns, service)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.syncRecords(ctx, dns, "tunnel.example.net", map[string]HostnameConfig{"API.Team.Example.COM.": {}}, zones, service); err != nil {
		t.Fatal(err)
	}
	if len(store["parent"]) != 0 || len(store["child"]) != 2 || dns.Status.Records[0].ZoneID != "child" || dns.Status.Records[0].Hostname != "api.team.example.com" {
		t.Fatalf("wrong zone or hostname: store=%v status=%+v", store, dns.Status.Records)
	}
	if verified, err := r.verifyOwnership(ctx, dns, zones, []string{"API.Team.Example.COM."}, service); err != nil || !verified {
		t.Fatalf("verify ownership: verified=%t err=%v", verified, err)
	}
	// Legacy status without a zone ID uses the same delegated-zone selector.
	dns.Status.Records[0].ZoneID = ""
	if err := r.cleanupRecordsWithFallback(ctx, dns); err != nil {
		t.Fatal(err)
	}
	if len(store["child"]) != 0 {
		t.Fatalf("delegated records leaked: %v", store)
	}
}

func TestDNSCleanupRetryPersistsIdentity(t *testing.T) {
	for _, phase := range []string{"lookup", "data delete", "claim delete"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			store := map[string]map[string]cloudflare.DNSRecord{}
			mock := dnsLifecycleStore(t, store)
			dns := &cfg.CloudflareDNS{ObjectMeta: metav1.ObjectMeta{Name: "dns", Namespace: "default"}, Status: cfg.CloudflareDNSStatus{OwnerID: "installation/resource"}}
			r := &CloudflareDNSReconciler{Recorder: &fakeEventRecorder{}}
			zones := map[string]string{"example.com": "old-zone"}
			service := func() *cloudflare.DNSService {
				return cloudflare.NewDNSService(mock, logr.Discard()).WithCache(cloudflare.NewDNSRecordCache())
			}
			if err := r.syncRecords(ctx, dns, "tunnel.example.net", map[string]HostnameConfig{"api.example.com": {}}, zones, service()); err != nil {
				t.Fatal(err)
			}
			originalID := dns.Status.Records[0].RecordID
			lookup := mock.ListDNSRecordsByNameTypeFunc
			deleteRecord := mock.DeleteDNSRecordFunc
			fail := true
			mock.ListDNSRecordsByNameTypeFunc = func(ctx context.Context, zone, name, kind string) ([]cloudflare.DNSRecord, error) {
				if fail && phase == "lookup" {
					return nil, errors.New("temporary lookup failure")
				}
				return lookup(ctx, zone, name, kind)
			}
			mock.DeleteDNSRecordFunc = func(ctx context.Context, zone, id string) error {
				if fail && ((phase == "data delete" && store[zone][id].Type != "TXT") || (phase == "claim delete" && store[zone][id].Type == "TXT")) {
					return errors.New("temporary delete failure")
				}
				return deleteRecord(ctx, zone, id)
			}
			r.Client = fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithStatusSubresource(dns).WithObjects(dns).Build()
			// The original zone can disappear from the spec before deletion completes.
			if err := r.syncRecords(ctx, dns, "", nil, map[string]string{"elsewhere.net": "new-zone"}, service()); err == nil {
				t.Fatal("cleanup error was swallowed")
			}
			if len(dns.Status.Records) != 1 || dns.Status.Records[0].RecordID != originalID || dns.Status.Records[0].ZoneID != "old-zone" || dns.Status.FailedRecords != 1 {
				t.Fatalf("cleanup obligation lost: %+v", dns.Status)
			}
			if err := r.updateStatus(ctx, dns); err != nil {
				t.Fatal(err)
			}
			restarted := &cfg.CloudflareDNS{}
			if err := r.Get(ctx, client.ObjectKeyFromObject(dns), restarted); err != nil {
				t.Fatal(err)
			}
			fail = false
			if err := r.syncRecords(ctx, restarted, "", nil, nil, service()); err != nil {
				t.Fatal(err)
			}
			if len(store["old-zone"]) != 0 || len(restarted.Status.Records) != 0 || restarted.Status.FailedRecords != 0 {
				t.Fatalf("retry failed: store=%v status=%+v", store, restarted.Status)
			}
		})
	}
}

func TestDNSRecordIdentityTransitions(t *testing.T) {
	for _, transition := range []string{"type", "zone", "both"} {
		t.Run(transition, func(t *testing.T) {
			ctx := context.Background()
			store := map[string]map[string]cloudflare.DNSRecord{}
			mock := dnsLifecycleStore(t, store)
			dns := &cfg.CloudflareDNS{Status: cfg.CloudflareDNSStatus{OwnerID: "installation/resource"}}
			r := &CloudflareDNSReconciler{Recorder: &fakeEventRecorder{}}
			service := func() *cloudflare.DNSService {
				return cloudflare.NewDNSService(mock, logr.Discard()).WithCache(cloudflare.NewDNSRecordCache())
			}
			zones := map[string]string{"example.com": "parent"}
			hosts := map[string]HostnameConfig{"api.team.example.com": {}}
			if err := r.syncRecords(ctx, dns, "tunnel.example.net", hosts, zones, service()); err != nil {
				t.Fatal(err)
			}
			oldID := dns.Status.Records[0].RecordID
			wantZone, wantType := "parent", "CNAME"
			if transition != "type" {
				zones = map[string]string{"team.example.com": "child"}
				wantZone = "child"
			}
			if transition != "zone" {
				hosts["api.team.example.com"] = HostnameConfig{RecordType: "A", Target: "192.0.2.1"}
				wantType = "A"
			}
			create := mock.CreateDNSRecordFunc
			mock.CreateDNSRecordFunc = func(ctx context.Context, zone string, record cloudflare.DNSRecord) (*cloudflare.DNSRecord, error) {
				if _, exists := store["parent"][oldID]; exists {
					t.Fatal("replacement published before obsolete identity was withdrawn")
				}
				return create(ctx, zone, record)
			}
			if err := r.syncRecords(ctx, dns, "tunnel.example.net", hosts, zones, service()); err != nil {
				t.Fatal(err)
			}
			if dns.Status.FailedRecords != 0 || len(dns.Status.Records) != 1 || dns.Status.Records[0].ZoneID != wantZone || dns.Status.Records[0].Type != wantType {
				t.Fatalf("transition failed: %+v", dns.Status)
			}
			if _, exists := store["parent"][oldID]; exists {
				t.Fatal("old identity leaked")
			}
			if len(store[wantZone]) != 2 {
				t.Fatalf("replacement or ownership claim missing: %v", store)
			}
			if transition != "type" && len(store["parent"]) != 0 {
				t.Fatalf("old-zone ownership claim leaked: %v", store)
			}
			if err := r.syncRecords(ctx, dns, "tunnel.example.net", hosts, zones, service()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDNSUnknownHistoricalZoneBlocksCleanup(t *testing.T) {
	dns := &cfg.CloudflareDNS{Status: cfg.CloudflareDNSStatus{OwnerID: "installation/resource", Records: []cfg.DNSRecordSyncStatus{{Hostname: "old.example.com", Type: "A", RecordID: "old", Status: "Synced"}}}}
	mock := cloudflare.NewMockClient()
	mock.GetZoneByNameFunc = func(context.Context, string) (*cloudflare.Zone, error) {
		t.Fatal("guessed an unconfigured zone")
		return nil, nil
	}
	r := &CloudflareDNSReconciler{Recorder: &fakeEventRecorder{}}
	err := r.syncRecords(context.Background(), dns, "", nil, map[string]string{"elsewhere.net": "new"}, cloudflare.NewDNSService(mock, logr.Discard()))
	if err == nil || len(dns.Status.Records) != 1 || dns.Status.Records[0].RecordID != "old" {
		t.Fatalf("unknown cleanup identity forgotten: err=%v status=%+v", err, dns.Status)
	}
}

func TestDNSRetainedRecordsRemainAvailableForFinalCleanup(t *testing.T) {
	for _, policy := range []cfg.DNSPolicy{cfg.DNSPolicySync, cfg.DNSPolicyUpsertOnly, cfg.DNSPolicyCreateOnly} {
		t.Run(string(policy), func(t *testing.T) {
			ctx := context.Background()
			store := map[string]map[string]cloudflare.DNSRecord{}
			mock := dnsLifecycleStore(t, store)
			dns := &cfg.CloudflareDNS{Spec: cfg.CloudflareDNSSpec{Cloudflare: &cfg.CloudflareConfig{SecretRef: cfg.SecretRef{Name: "credentials"}}}, Status: cfg.CloudflareDNSStatus{OwnerID: "installation/resource"}}
			r := &CloudflareDNSReconciler{CFClient: mock, Recorder: &fakeEventRecorder{}}
			service := cloudflare.NewDNSService(mock, logr.Discard())
			if err := r.syncRecords(ctx, dns, "tunnel.example.net", map[string]HostnameConfig{"api.example.com": {}}, map[string]string{"example.com": "old-zone"}, service); err != nil {
				t.Fatal(err)
			}
			dns.Spec.Policy = policy
			if policy == cfg.DNSPolicySync {
				disabled := false
				dns.Spec.CleanupPolicy.DeleteOnRouteRemoval = &disabled
			}
			if err := r.syncRecords(ctx, dns, "", nil, nil, service); err != nil {
				t.Fatal(err)
			}
			if len(dns.Status.Records) != 1 || len(store["old-zone"]) != 2 {
				t.Fatalf("retained inventory lost: %+v", dns.Status)
			}
			dns.Spec.Policy = cfg.DNSPolicySync
			if err := r.cleanupRecordsWithFallback(ctx, dns); err != nil {
				t.Fatal(err)
			}
			if len(store["old-zone"]) != 0 {
				t.Fatal("historical zone leaked on final deletion")
			}
		})
	}
}

func TestDNSFailedUpdatePreservesCleanupIdentity(t *testing.T) {
	ctx := context.Background()
	store := map[string]map[string]cloudflare.DNSRecord{}
	mock := dnsLifecycleStore(t, store)
	dns := &cfg.CloudflareDNS{Status: cfg.CloudflareDNSStatus{OwnerID: "installation/resource"}}
	r := &CloudflareDNSReconciler{Recorder: &fakeEventRecorder{}}
	service := cloudflare.NewDNSService(mock, logr.Discard())
	zones := map[string]string{"example.com": "zone"}
	hosts := map[string]HostnameConfig{"api.example.com": {}}
	if err := r.syncRecords(ctx, dns, "first.example.net", hosts, zones, service); err != nil {
		t.Fatal(err)
	}
	id := dns.Status.Records[0].RecordID
	mock.UpdateDNSRecordFunc = func(context.Context, string, string, cloudflare.DNSRecord) (*cloudflare.DNSRecord, error) {
		return nil, fmt.Errorf("temporary update failure")
	}
	if err := r.syncRecords(ctx, dns, "second.example.net", hosts, zones, service); err != nil {
		t.Fatal(err)
	}
	if dns.Status.FailedRecords != 1 || dns.Status.Records[0].RecordID != id {
		t.Fatalf("failed update lost identity: %+v", dns.Status)
	}
	if err := r.syncRecords(ctx, dns, "", nil, nil, service); err != nil {
		t.Fatal(err)
	}
	if len(store["zone"]) != 0 {
		t.Fatal("failed update leaked records on later removal")
	}
}

func TestDNSTransitionCrashRecoversNewIdentity(t *testing.T) {
	for _, transition := range []string{"type", "zone"} {
		t.Run(transition, func(t *testing.T) {
			ctx := context.Background()
			store := map[string]map[string]cloudflare.DNSRecord{}
			mock := dnsLifecycleStore(t, store)
			dns := &cfg.CloudflareDNS{Spec: cfg.CloudflareDNSSpec{Cloudflare: &cfg.CloudflareConfig{SecretRef: cfg.SecretRef{Name: "credentials"}}, Zones: []cfg.DNSZoneConfig{{Name: "example.com", ID: "parent"}}}, Status: cfg.CloudflareDNSStatus{OwnerID: "installation/resource"}}
			r := &CloudflareDNSReconciler{CFClient: mock, Recorder: &fakeEventRecorder{}}
			service := cloudflare.NewDNSService(mock, logr.Discard())
			zones := map[string]string{"example.com": "parent"}
			hosts := map[string]HostnameConfig{"api.team.example.com": {}}
			if err := r.syncRecords(ctx, dns, "tunnel.example.net", hosts, zones, service); err != nil {
				t.Fatal(err)
			}
			if transition == "zone" {
				dns.Spec.Zones = []cfg.DNSZoneConfig{{Name: "team.example.com", ID: "child"}}
				zones = map[string]string{"team.example.com": "child"}
			} else {
				hosts["api.team.example.com"] = HostnameConfig{RecordType: "A", Target: "192.0.2.1"}
			}
			persisted := dns.DeepCopy()
			create := mock.CreateDNSRecordFunc
			mock.CreateDNSRecordFunc = func(ctx context.Context, zone string, record cloudflare.DNSRecord) (*cloudflare.DNSRecord, error) {
				result, err := create(ctx, zone, record)
				if record.Type != "TXT" {
					panic("crash after remote success")
				}
				return result, err
			}
			crashed := false
			func() {
				defer func() {
					if value := recover(); value != nil {
						if value != "crash after remote success" {
							panic(value)
						}
						crashed = true
					}
				}()
				_ = r.syncRecords(ctx, dns, "tunnel.example.net", hosts, zones, service)
			}()
			if !crashed {
				t.Fatal("crash point was not reached")
			}
			if err := r.cleanupRecordsWithFallback(ctx, persisted); err != nil {
				t.Fatal(err)
			}
			for zone, records := range store {
				if len(records) != 0 {
					t.Fatalf("unrecorded transition leaked in %s: %v", zone, records)
				}
			}
		})
	}
}

func TestDNSInventoryLimitPreventsUntrackedWrites(t *testing.T) {
	dns := &cfg.CloudflareDNS{Spec: cfg.CloudflareDNSSpec{Policy: cfg.DNSPolicyUpsertOnly}, Status: cfg.CloudflareDNSStatus{OwnerID: "installation/resource"}}
	for i := 0; i < 1000; i++ {
		dns.Status.Records = append(dns.Status.Records, cfg.DNSRecordSyncStatus{Hostname: fmt.Sprintf("old-%d.example.com", i), Type: "A", RecordID: fmt.Sprint(i), ZoneID: "zone", Status: "Synced"})
	}
	mock := cloudflare.NewMockClient()
	mock.CreateDNSRecordFunc = func(context.Context, string, cloudflare.DNSRecord) (*cloudflare.DNSRecord, error) {
		t.Fatal("created record beyond status capacity")
		return nil, nil
	}
	r := &CloudflareDNSReconciler{Recorder: &fakeEventRecorder{}}
	err := r.syncRecords(context.Background(), dns, "tunnel.example.net", map[string]HostnameConfig{"new.example.com": {}}, map[string]string{"example.com": "zone"}, cloudflare.NewDNSService(mock, logr.Discard()))
	if err == nil || len(dns.Status.Records) != 1000 {
		t.Fatalf("inventory guard failed: err=%v count=%d", err, len(dns.Status.Records))
	}
}

func TestDNSLostCreateResponseRecoversOwnedRecord(t *testing.T) {
	for _, cleanup := range []string{"route removal", "resource removal"} {
		t.Run(cleanup, func(t *testing.T) {
			ctx := context.Background()
			store := map[string]map[string]cloudflare.DNSRecord{}
			mock := dnsLifecycleStore(t, store)
			create := mock.CreateDNSRecordFunc
			mock.CreateDNSRecordFunc = func(ctx context.Context, zone string, record cloudflare.DNSRecord) (*cloudflare.DNSRecord, error) {
				result, err := create(ctx, zone, record)
				if record.Type != "TXT" {
					return nil, errors.New("response lost after successful create")
				}
				return result, err
			}
			dns := &cfg.CloudflareDNS{Spec: cfg.CloudflareDNSSpec{Cloudflare: &cfg.CloudflareConfig{SecretRef: cfg.SecretRef{Name: "credentials"}}, Zones: []cfg.DNSZoneConfig{{Name: "example.com", ID: "zone"}}}, Status: cfg.CloudflareDNSStatus{OwnerID: "installation/resource"}}
			r := &CloudflareDNSReconciler{CFClient: mock, Recorder: &fakeEventRecorder{}}
			service := cloudflare.NewDNSService(mock, logr.Discard())
			zones := map[string]string{"example.com": "zone"}
			if err := r.syncRecords(ctx, dns, "tunnel.example.net", map[string]HostnameConfig{"api.example.com": {}}, zones, service); err != nil {
				t.Fatal(err)
			}
			if dns.Status.FailedRecords != 1 || dns.Status.Records[0].RecordID != "" || len(store["zone"]) != 2 {
				t.Fatalf("missing failed-response precondition: status=%+v store=%v", dns.Status, store)
			}
			var err error
			if cleanup == "route removal" {
				err = r.syncRecords(ctx, dns, "", nil, zones, service)
			} else {
				err = r.cleanupRecordsWithFallback(ctx, dns)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(store["zone"]) != 0 {
				t.Fatalf("uncertain create leaked: %v", store)
			}
		})
	}
}

func TestDNSNormalizedConfigurationConflicts(t *testing.T) {
	ctx := context.Background()
	mock := cloudflare.NewMockClient()
	mock.CreateDNSRecordFunc = func(context.Context, string, cloudflare.DNSRecord) (*cloudflare.DNSRecord, error) {
		t.Fatal("created record from ambiguous configuration")
		return nil, nil
	}
	dns := &cfg.CloudflareDNS{Spec: cfg.CloudflareDNSSpec{Zones: []cfg.DNSZoneConfig{{Name: "example.com", ID: "one"}, {Name: "EXAMPLE.COM.", ID: "two"}}}, Status: cfg.CloudflareDNSStatus{OwnerID: "installation/resource"}}
	r := &CloudflareDNSReconciler{Recorder: &fakeEventRecorder{}}
	service := cloudflare.NewDNSService(mock, logr.Discard())
	if _, err := r.resolveZones(ctx, dns, service); err == nil {
		t.Fatal("ambiguous zone accepted")
	}
	if err := r.syncRecords(ctx, dns, "tunnel.example.net", map[string]HostnameConfig{"api.example.com": {TTL: 60}, "API.EXAMPLE.COM.": {TTL: 120}}, map[string]string{"example.com": "one"}, service); err == nil {
		t.Fatal("ambiguous hostname accepted")
	}
}

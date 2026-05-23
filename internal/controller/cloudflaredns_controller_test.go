package controller

import (
	"context"
	"testing"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
)

func TestSyncRecordsUsesZoneProxiedBeforeDefaults(t *testing.T) {
	ctx := context.Background()
	zoneProxied := false
	hostnameProxied := true
	txtEnabled := false
	created := map[string]cloudflare.DNSRecord{}
	mock := cloudflare.NewMockClient()

	mock.ListDNSRecordsByNameTypeFunc = func(context.Context, string, string, string) ([]cloudflare.DNSRecord, error) {
		return nil, nil
	}
	mock.CreateDNSRecordFunc = func(_ context.Context, _ string, record cloudflare.DNSRecord) (*cloudflare.DNSRecord, error) {
		record.ID = "record-" + record.Name
		created[record.Name] = record
		return &record, nil
	}

	dns := &cfgatev1alpha1.CloudflareDNS{
		Spec: cfgatev1alpha1.CloudflareDNSSpec{
			Zones: []cfgatev1alpha1.DNSZoneConfig{{
				Name:    "example.com",
				ID:      "zone-1",
				Proxied: &zoneProxied,
			}},
			Defaults: cfgatev1alpha1.DNSRecordDefaults{
				Proxied: true,
			},
			Ownership: cfgatev1alpha1.DNSOwnershipConfig{
				TXTRecord: cfgatev1alpha1.DNSTXTRecordOwnership{
					Enabled: &txtEnabled,
				},
			},
		},
	}

	r := &CloudflareDNSReconciler{Recorder: &fakeEventRecorder{}}
	err := r.syncRecords(ctx, dns, "target.example.net", map[string]HostnameConfig{
		"zone-default.example.com": {},
		"host-override.example.com": {
			Proxied: &hostnameProxied,
		},
	}, map[string]string{"example.com": "zone-1"}, cloudflare.NewDNSService(mock, discardLogger()))
	if err != nil {
		t.Fatalf("syncRecords() error = %v", err)
	}

	if got := created["zone-default.example.com"].Proxied; got {
		t.Fatalf("zone-default.example.com proxied = %t, want false from zones[].proxied", got)
	}
	if got := created["host-override.example.com"].Proxied; !got {
		t.Fatalf("host-override.example.com proxied = %t, want true from hostname override", got)
	}
}

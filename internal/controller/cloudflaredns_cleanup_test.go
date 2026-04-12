package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
)

func TestDeleteManagedStatusRecord(t *testing.T) {
	ctx := context.Background()

	t.Run("skips cleanup when ownership txt belongs to different resource", func(t *testing.T) {
		mock := cloudflare.NewMockClient()
		deleted := false
		mock.ListDNSRecordsByNameTypeFunc = func(_ context.Context, _, name, recordType string) ([]cloudflare.DNSRecord, error) {
			switch {
			case name == "app.example.com" && recordType == "CNAME":
				return []cloudflare.DNSRecord{{
					ID:      "record-1",
					Name:    name,
					Type:    recordType,
					Content: "target.example.com",
					Comment: "managed by cfgate",
				}}, nil
			case name == "_cfgate.app.example.com" && recordType == "TXT":
				return []cloudflare.DNSRecord{{
					ID:      "txt-1",
					Name:    name,
					Type:    recordType,
					Content: "heritage=cfgate,cfgate/owner=default/other,cfgate/resource=CloudflareDNS/default/other",
					Comment: "cfgate ownership record",
				}}, nil
			default:
				return nil, nil
			}
		}
		mock.DeleteDNSRecordFunc = func(context.Context, string, string) error {
			deleted = true
			return nil
		}

		r := &CloudflareDNSReconciler{}
		dns := &cfgatev1alpha1.CloudflareDNS{}
		deletedRecord, err := r.deleteManagedStatusRecord(ctx, dns, cloudflare.NewDNSService(mock, discardLogger()), "zone-1", cfgatev1alpha1.DNSRecordSyncStatus{
			Hostname: "app.example.com",
			Type:     "CNAME",
			RecordID: "record-1",
		}, "default/dns", "_cfgate")
		if err != nil {
			t.Fatalf("deleteManagedStatusRecord() error = %v", err)
		}
		if deletedRecord {
			t.Fatal("deleteManagedStatusRecord() = true, want false when TXT owner differs")
		}
		if deleted {
			t.Fatal("deleteManagedStatusRecord() deleted record for different owner")
		}
	})

	t.Run("skips cleanup when current record identity changed", func(t *testing.T) {
		mock := cloudflare.NewMockClient()
		deleted := false
		mock.ListDNSRecordsByNameTypeFunc = func(_ context.Context, _, name, recordType string) ([]cloudflare.DNSRecord, error) {
			switch {
			case name == "app.example.com" && recordType == "CNAME":
				return []cloudflare.DNSRecord{{
					ID:      "record-2",
					Name:    name,
					Type:    recordType,
					Content: "target.example.com",
					Comment: "managed by cfgate",
				}}, nil
			case name == "_cfgate.app.example.com" && recordType == "TXT":
				return nil, nil
			default:
				return nil, nil
			}
		}
		mock.DeleteDNSRecordFunc = func(context.Context, string, string) error {
			deleted = true
			return nil
		}

		r := &CloudflareDNSReconciler{}
		dns := &cfgatev1alpha1.CloudflareDNS{}
		deletedRecord, err := r.deleteManagedStatusRecord(ctx, dns, cloudflare.NewDNSService(mock, discardLogger()), "zone-1", cfgatev1alpha1.DNSRecordSyncStatus{
			Hostname: "app.example.com",
			Type:     "CNAME",
			RecordID: "record-1",
		}, "default/dns", "_cfgate")
		if err != nil {
			t.Fatalf("deleteManagedStatusRecord() error = %v", err)
		}
		if deletedRecord {
			t.Fatal("deleteManagedStatusRecord() = true, want false when record ID changed")
		}
		if deleted {
			t.Fatal("deleteManagedStatusRecord() deleted record with changed identity")
		}
	})
}

func TestCleanupRecordsWithFallbackUsesStatusInventory(t *testing.T) {
	ctx := context.Background()
	mock := cloudflare.NewMockClient()
	deleted := []string{}
	listCalled := false

	mock.ListDNSRecordsFunc = func(context.Context, string) ([]cloudflare.DNSRecord, error) {
		listCalled = true
		return []cloudflare.DNSRecord{{
			ID:      "foreign-id",
			Name:    "foreign.example.com",
			Type:    "CNAME",
			Content: "target.example.com",
			Comment: "managed by cfgate",
		}}, nil
	}
	mock.ListDNSRecordsByNameTypeFunc = func(_ context.Context, _, name, recordType string) ([]cloudflare.DNSRecord, error) {
		switch {
		case name == "mine.example.com" && recordType == "CNAME":
			return []cloudflare.DNSRecord{{
				ID:      "mine-id",
				Name:    name,
				Type:    recordType,
				Content: "target.example.com",
				Comment: "managed by cfgate",
			}}, nil
		case name == "_cfgate.mine.example.com" && recordType == "TXT":
			return nil, nil
		default:
			return nil, nil
		}
	}
	mock.DeleteDNSRecordFunc = func(_ context.Context, _, recordID string) error {
		deleted = append(deleted, recordID)
		return nil
	}

	disabledTXT := false
	dns := &cfgatev1alpha1.CloudflareDNS{
		Spec: cfgatev1alpha1.CloudflareDNSSpec{
			Cloudflare: &cfgatev1alpha1.CloudflareConfig{
				SecretRef: cfgatev1alpha1.SecretRef{Name: "cloudflare-credentials"},
			},
			Zones: []cfgatev1alpha1.DNSZoneConfig{{
				Name: "example.com",
				ID:   "zone-1",
			}},
			Ownership: cfgatev1alpha1.DNSOwnershipConfig{
				TXTRecord: cfgatev1alpha1.DNSTXTRecordOwnership{
					Enabled: &disabledTXT,
					Prefix:  "_cfgate",
				},
			},
		},
		Status: cfgatev1alpha1.CloudflareDNSStatus{
			Records: []cfgatev1alpha1.DNSRecordSyncStatus{{
				Hostname: "mine.example.com",
				Type:     "CNAME",
				RecordID: "mine-id",
				ZoneID:   "zone-1",
			}},
		},
	}

	r := &CloudflareDNSReconciler{CFClient: mock}
	if err := r.cleanupRecordsWithFallback(ctx, dns); err != nil {
		t.Fatalf("cleanupRecordsWithFallback() error = %v", err)
	}

	if listCalled {
		t.Fatal("cleanupRecordsWithFallback() fell back to zone-wide managed record listing despite status inventory")
	}
	if len(deleted) != 1 || deleted[0] != "mine-id" {
		t.Fatalf("deleted record IDs = %#v, want only mine-id", deleted)
	}
}

func TestCleanupRecordsWithFallbackSkipsFailedStatusRecordsWithoutMaterializedRemoteState(t *testing.T) {
	ctx := context.Background()
	mock := cloudflare.NewMockClient()
	deleted := []string{}
	resolvedUnexpectedZone := false

	mock.ListDNSRecordsByNameTypeFunc = func(_ context.Context, _, name, recordType string) ([]cloudflare.DNSRecord, error) {
		switch {
		case name == "good.example.com" && recordType == "CNAME":
			return []cloudflare.DNSRecord{{
				ID:      "good-id",
				Name:    name,
				Type:    recordType,
				Content: "target.example.com",
				Comment: "managed by cfgate",
			}}, nil
		case name == "_cfgate.good.example.com" && recordType == "TXT":
			return nil, nil
		default:
			return nil, nil
		}
	}
	mock.GetZoneByNameFunc = func(_ context.Context, name string) (*cloudflare.Zone, error) {
		if name == "example.invalid" {
			resolvedUnexpectedZone = true
		}
		return nil, nil
	}
	mock.DeleteDNSRecordFunc = func(_ context.Context, _, recordID string) error {
		deleted = append(deleted, recordID)
		return nil
	}

	disabledTXT := false
	dns := &cfgatev1alpha1.CloudflareDNS{
		Spec: cfgatev1alpha1.CloudflareDNSSpec{
			Cloudflare: &cfgatev1alpha1.CloudflareConfig{
				SecretRef: cfgatev1alpha1.SecretRef{Name: "cloudflare-credentials"},
			},
			Zones: []cfgatev1alpha1.DNSZoneConfig{{
				Name: "example.com",
				ID:   "zone-1",
			}},
			Ownership: cfgatev1alpha1.DNSOwnershipConfig{
				TXTRecord: cfgatev1alpha1.DNSTXTRecordOwnership{
					Enabled: &disabledTXT,
					Prefix:  "_cfgate",
				},
			},
		},
		Status: cfgatev1alpha1.CloudflareDNSStatus{
			Records: []cfgatev1alpha1.DNSRecordSyncStatus{
				{
					Hostname: "good.example.com",
					Type:     "CNAME",
					Status:   "Synced",
					RecordID: "good-id",
					ZoneID:   "zone-1",
				},
				{
					Hostname: "bad.example.invalid",
					Type:     "CNAME",
					Status:   "Failed",
					Error:    "zone example.invalid not configured",
				},
			},
		},
	}

	r := &CloudflareDNSReconciler{CFClient: mock}
	if err := r.cleanupRecordsWithFallback(ctx, dns); err != nil {
		t.Fatalf("cleanupRecordsWithFallback() error = %v", err)
	}

	if resolvedUnexpectedZone {
		t.Fatal("cleanupRecordsWithFallback() tried to resolve an unconfigured zone for a failed, non-materialized record")
	}
	if len(deleted) != 1 || deleted[0] != "good-id" {
		t.Fatalf("deleted record IDs = %#v, want only good-id", deleted)
	}
}

func discardLogger() logr.Logger {
	return logr.Discard()
}

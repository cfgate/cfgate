package controller

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"slices"

	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
)

func dnsOwnershipPrefix(dns *cfg.CloudflareDNS) string {
	if dns.Status.OwnershipPrefix != "" {
		return dns.Status.OwnershipPrefix
	}
	if dns.Spec.Ownership.TXTRecord.Prefix != "" {
		return dns.Spec.Ownership.TXTRecord.Prefix
	}
	return dnsDefaultOwnershipPrefix
}

// recoverDNSWrites resolves only recorded operations. A matching owner alone
// does not authorize replacing a previously recorded remote incarnation.
func recoverDNSWrites(ctx context.Context, dns *cfg.CloudflareDNS, service *cloudflare.DNSService) error {
	for _, pending := range dns.Status.PendingWrites {
		record, err := service.FindRecordByName(ctx, pending.ZoneID, pending.Hostname, pending.Type)
		if err != nil {
			return err
		}
		if record == nil {
			claim, err := service.FindRecordByName(ctx, pending.ZoneID, dnsOwnershipPrefix(dns)+"."+pending.Hostname, "TXT")
			if err != nil {
				return err
			}
			if claim == nil {
				continue
			} // No side effect needs an inventory slot.
		}
		observed := cfg.DNSRecordSyncStatus{ZoneID: pending.ZoneID, Hostname: pending.Hostname, Type: pending.Type, Status: "Pending"}
		if record != nil {
			baseline := pending.PreviousRecordID != "" && record.ID == pending.PreviousRecordID
			owned := cloudflare.IsOwnedByCfgate(record, dns.Status.OwnerID)
			recognized := baseline && (!pending.PreviousOwned || owned) || owned && cloudflare.DNSRecordOperation(record) == pending.OperationID
			if !recognized {
				return fmt.Errorf("DNS recovery conflict for %s (%s, zone %s): record %s does not match the persisted write intent", pending.Hostname, pending.Type, pending.ZoneID, record.ID)
			}
			observed.RecordID, observed.Target, observed.Proxied, observed.TTL = record.ID, record.Content, record.Proxied, int32(record.TTL)
		}
		index := slices.IndexFunc(dns.Status.Records, func(old cfg.DNSRecordSyncStatus) bool {
			return old.ZoneID == pending.ZoneID && cloudflare.NormalizeDNSName(old.Hostname) == pending.Hostname && old.Type == pending.Type
		})
		if index >= 0 {
			dns.Status.Records[index] = observed
		} else {
			if len(dns.Status.Records) >= 1000 {
				if err := retireAbsentDNSRecords(ctx, dns, service); err != nil {
					return err
				}
				if len(dns.Status.Records) >= 1000 {
					return fmt.Errorf("DNS recovery inventory exceeds 1000 records")
				}
			}
			dns.Status.Records = append(dns.Status.Records, observed)
		}
	}
	dns.Status.PendingWrites = nil
	return nil
}

// prepareDNSWrites checkpoints destinations before either claims or data can be
// created. Recovery and its replacement inventory are persisted in the same write.
func (r *CloudflareDNSReconciler) prepareDNSWrites(ctx context.Context, dns *cfg.CloudflareDNS, hosts map[string]HostnameConfig, zones map[string]string, service *cloudflare.DNSService) error {
	prefix := dns.Spec.Ownership.TXTRecord.Prefix
	if prefix == "" {
		prefix = dnsDefaultOwnershipPrefix
	}
	if dns.Status.OwnershipPrefix != "" && dns.Status.OwnershipPrefix != prefix {
		return fmt.Errorf("TXT ownership prefix is immutable; recreate the DNS resource after cleanup to change it")
	}
	dns.Status.OwnershipPrefix = prefix
	if err := recoverDNSWrites(ctx, dns, service); err != nil {
		return err
	}
	if len(hosts) > 1000 {
		return fmt.Errorf("DNS write inventory exceeds 1000 records")
	}
	// Sorting makes the durable inventory independent of Go map iteration order.
	names := hostnameKeys(hosts)
	slices.Sort(names)
	for _, name := range names {
		config := hosts[name]
		name = cloudflare.NormalizeDNSName(name)
		_, zone, err := cloudflare.SelectDNSZone(name, zones)
		if err != nil {
			continue
		} // syncRecords reports this hostname's failure.
		kind := config.RecordType
		if kind == "" {
			kind = "CNAME"
		}
		current, err := service.FindRecordByName(ctx, zone, name, kind)
		if err != nil {
			return err
		}
		if current != nil && cloudflare.IsOwnedByCfgate(current, dns.Status.OwnerID) {
			for _, previous := range dns.Status.Records {
				if previous.ZoneID == zone && cloudflare.NormalizeDNSName(previous.Hostname) == name && previous.Type == kind && previous.RecordID != "" && previous.RecordID != current.ID {
					return fmt.Errorf("DNS recovery conflict for %s: recorded ID %s differs from current ID %s", name, previous.RecordID, current.ID)
				}
			}
		}
		var bytes [7]byte
		if _, err := rand.Read(bytes[:]); err != nil {
			return fmt.Errorf("create DNS operation identity: %w", err)
		}
		pending := cfg.DNSPendingWrite{ZoneID: zone, Hostname: name, Type: kind, OperationID: base64.RawURLEncoding.EncodeToString(bytes[:])}
		if current != nil {
			pending.PreviousRecordID = current.ID
			pending.PreviousOwned = cloudflare.IsOwnedByCfgate(current, dns.Status.OwnerID)
		}
		dns.Status.PendingWrites = append(dns.Status.PendingWrites, pending)
	}
	return r.updateStatus(ctx, dns)
}

func dnsWriteOperation(dns *cfg.CloudflareDNS, zone, hostname, kind string) string {
	for _, pending := range dns.Status.PendingWrites {
		if pending.ZoneID == zone && pending.Hostname == hostname && pending.Type == kind {
			return pending.OperationID
		}
	}
	return ""
}

func completeDNSWrite(dns *cfg.CloudflareDNS, zone, hostname, kind string) {
	dns.Status.PendingWrites = slices.DeleteFunc(dns.Status.PendingWrites, func(p cfg.DNSPendingWrite) bool {
		return p.ZoneID == zone && p.Hostname == hostname && p.Type == kind
	})
}

// A completed migration may have deleted an entire old inventory before its
// replacement status was saved. Reclaim capacity only after both objects are absent.
func retireAbsentDNSRecords(ctx context.Context, dns *cfg.CloudflareDNS, service *cloudflare.DNSService) error {
	retained := make([]cfg.DNSRecordSyncStatus, 0, len(dns.Status.Records))
	for _, old := range dns.Status.Records {
		if old.ZoneID == "" {
			retained = append(retained, old)
			continue
		}
		data, err := service.FindRecordByName(ctx, old.ZoneID, old.Hostname, old.Type)
		if err != nil {
			return err
		}
		claim, err := service.FindRecordByName(ctx, old.ZoneID, dnsOwnershipPrefix(dns)+"."+old.Hostname, "TXT")
		if err != nil {
			return err
		}
		if data != nil || claim != nil {
			retained = append(retained, old)
		}
	}
	dns.Status.Records = retained
	return nil
}

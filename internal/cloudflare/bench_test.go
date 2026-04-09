package cloudflare

import "testing"

func BenchmarkRecordsMatch(b *testing.B) {
	recordA := &DNSRecord{
		ID:      "r1",
		Type:    "CNAME",
		Name:    "app.example.com",
		Content: "uuid.cfargotunnel.com",
		TTL:     300,
		Proxied: true,
	}
	recordB := &DNSRecord{
		ID:      "r2",
		Type:    "CNAME",
		Name:    "app.example.com",
		Content: "uuid.cfargotunnel.com",
		TTL:     300,
		Proxied: true,
	}

	b.ReportAllocs()
	for b.Loop() {
		_ = recordsMatch(recordA, recordB)
	}
}

func BenchmarkTunnelDomain(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = TunnelDomain("01234567-89ab-cdef-0123-456789abcdef")
	}
}

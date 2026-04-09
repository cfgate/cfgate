package annotations

import "testing"

func BenchmarkParseNamespacedName(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_, _, _ = ParseNamespacedName("cfgate-system/policy", "default")
	}
}

func BenchmarkValidateHostname(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = ValidateHostname("app.example.com")
	}
}

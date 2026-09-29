package e2ecleanup

import (
	"testing"
	"time"
)

func TestSelect(t *testing.T) {
	now := time.Now()
	old := now.Add(-3 * time.Hour)
	for _, tc := range []struct {
		name, run              string
		created                time.Time
		current, orphans, want bool
	}{
		{"e2e-run-tunnel-1-2", "run", now, true, false, true},
		{"_custom.e2e-run-dns-1-2.example.com", "run", time.Time{}, true, false, true},
		{"e2e-other-access-1-2 e2e-run-access-1-2.example.com/path", "run", now, true, false, true},
		{"admin-app e2e-run-access-1-2.example.com/path", "run", now, true, false, true},
		{"e2e-other-tunnel-1-2", "run", old, true, false, false},
		{"e2e-run-tunnel-1-2", "run", old, false, true, false},
		{"e2e-other-tunnel-1-2", "run", old, false, true, true},
		{"e2e-other-tunnel-1-2", "run", time.Time{}, true, true, false},
		{"e2e-other-tunnel-1-2", "run", now, true, true, false},
		{"production-e2e-run-tunnel-1-2", "run", old, true, true, false},
		{"e2e-run-tunnel-1-2", "", now, true, false, false},
		{"e2e-not-a-generated-name", "run", old, true, true, false},
	} {
		if got := Select(tc.name, tc.run, tc.created, now, tc.current, tc.orphans, 2*time.Hour); got != tc.want {
			t.Errorf("%+v: got %v", tc, got)
		}
	}
}

func TestNamespaceSelected(t *testing.T) {
	now := time.Now()
	old := now.Add(-3 * time.Hour)
	for _, tc := range []struct {
		labels        map[string]string
		orphans, want bool
	}{
		{map[string]string{"cfgate.io/e2e-test": "true", "cfgate.io/e2e-run": "run"}, false, true},
		{map[string]string{"cfgate.io/e2e-test": "true", "cfgate.io/e2e-run": "other"}, false, false},
		{map[string]string{"cfgate.io/e2e-test": "true"}, false, false},
		{map[string]string{"cfgate.io/e2e-run": "run"}, false, false},
		{map[string]string{"cfgate.io/e2e-test": "true", "cfgate.io/e2e-run": "other"}, true, true},
		{map[string]string{"cfgate.io/e2e-test": "true"}, true, false},
	} {
		if got := NamespaceSelected(tc.labels, "run", old, now, tc.orphans, 2*time.Hour); got != tc.want {
			t.Errorf("%+v: got %v", tc, got)
		}
	}
	for _, id := range []string{"", "Upper", "with-hyphen", "with/slash", "abcdefghijklmnopqrstu"} {
		if ValidRunID(id) {
			t.Errorf("accepted invalid run %q", id)
		}
	}
}

func FuzzSelect(f *testing.F) {
	for _, name := range []string{"e2e-run-tunnel-1-2", "production-e2e-run-tunnel-1-2", "_cfgate.e2e-run-dns-2-55.example.org", ""} {
		f.Add(name)
	}
	f.Fuzz(func(t *testing.T, name string) {
		now := time.Unix(10000, 0)
		if Select(name, "", now, now, true, false, time.Hour) {
			t.Fatal("empty run authorized deletion")
		}
		if Select(name, "run", time.Time{}, now, false, true, time.Hour) {
			t.Fatal("unknown age authorized orphan deletion")
		}
	})
}

package cloudflare

import "testing"

func TestAPIErrorError(t *testing.T) {
	err := &APIError{
		Code:    429,
		Message: "rate limited",
	}

	got := err.Error()
	want := "cloudflare API error (code 429): rate limited"
	if got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

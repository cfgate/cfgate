package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestE2EVerificationRenewalBaseline(t *testing.T) {
	transient := errors.New("edge not ready")
	calls := 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := waitForOriginBaseline(ctx, func(context.Context) error {
		calls++
		if calls == 1 || calls == 3 {
			return transient
		}
		return nil
	}, time.Millisecond)
	if err != nil || calls != 5 {
		t.Fatalf("baseline must require consecutive successes: calls=%d err=%v", calls, err)
	}
}

func TestE2EVerificationRenewalBaselineDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := waitForOriginBaseline(ctx, func(context.Context) error { return errors.New("HTTP 503") }, time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("lost last observation or deadline: %v", err)
	}
}

func TestE2EVerificationRenewalSamplesSurfaceFailure(t *testing.T) {
	for _, count := range []int32{0, 4} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			var samples atomic.Int32
			samples.Store(count)
			failures := make(chan error, 1)
			original := errors.New("renewal phase 1: authenticated origin returned HTTP 503")
			failures <- original
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := waitForContinuitySamples(ctx, &samples, failures, 2); !errors.Is(err, original) {
				t.Fatalf("probe failure masked by count: %v", err)
			}
		})
	}
	var samples atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForContinuitySamples(ctx, &samples, make(chan error), 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	samples.Store(2)
	if err := waitForContinuitySamples(context.Background(), &samples, make(chan error), 2); err != nil {
		t.Fatal(err)
	}
}

func TestE2EVerificationAuthenticatedProbe(t *testing.T) {
	for _, scenario := range []string{"success", "unauthorized", "unavailable", "wrong-body"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Cf-Access-Client-Id") != "test-id" || r.Header.Get("Cf-Access-Client-Secret") != "test-secret" {
					t.Error("missing credentials")
				}
				switch scenario {
				case "unauthorized":
					w.WriteHeader(http.StatusUnauthorized)
				case "unavailable":
					w.WriteHeader(http.StatusServiceUnavailable)
				case "wrong-body":
					_, _ = w.Write([]byte("other"))
				default:
					_, _ = w.Write([]byte("origin\n"))
				}
			}))
			defer server.Close()
			err := probeAuthenticatedOrigin(context.Background(), server.Client(), strings.TrimPrefix(server.URL, "https://"), "origin", http.Header{"Cf-Access-Client-Id": {"test-id"}, "Cf-Access-Client-Secret": {"test-secret"}})
			if (err == nil) != (scenario == "success") {
				t.Fatalf("unexpected probe result: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "test-secret") {
				t.Fatal("error discloses credentials")
			}
			if scenario == "unauthorized" && !strings.Contains(err.Error(), "401") {
				t.Fatalf("status missing: %v", err)
			}
			if scenario == "unavailable" && !strings.Contains(err.Error(), "503") {
				t.Fatalf("status missing: %v", err)
			}
		})
	}
}

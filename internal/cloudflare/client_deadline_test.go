package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type deadlineTransport func(*http.Request) (*http.Response, error)

func (f deadlineTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func deadlineClient(t *testing.T, handler http.HandlerFunc, observe func(*http.Request)) Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	c, err := NewClient("test-token", WithHTTPClient(&http.Client{Transport: deadlineTransport(func(r *http.Request) (*http.Response, error) {
		if observe != nil {
			observe(r)
		}
		clone := r.Clone(r.Context())
		u := *r.URL
		u.Scheme = target.Scheme
		u.Host = target.Host
		clone.URL = &u
		return transport.RoundTrip(clone)
	})}))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestClientAttemptHasDefaultDeadline(t *testing.T) {
	var remaining time.Duration
	c := deadlineClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"success":true,"result":{"id":"tunnel"}}`)
	}, func(r *http.Request) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Error("request has no deadline")
		}
		remaining = time.Until(deadline)
	})
	if _, err := c.GetTunnel(context.Background(), "account", "tunnel"); err != nil {
		t.Fatal(err)
	}
	if remaining < 29*time.Second || remaining > apiAttemptTimeout {
		t.Fatalf("attempt budget=%s", remaining)
	}
}

func TestClientStalledRequestsHonorCallerBudget(t *testing.T) {
	for _, kind := range []string{"headers", "body", "retry wait", "shutdown"} {
		t.Run(kind, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			var calls atomic.Int32
			c := deadlineClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				select {
				case entered <- struct{}{}:
				default:
				}
				switch kind {
				case "body":
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"success":true,"result":`)
					w.(http.Flusher).Flush()
				case "retry wait":
					w.Header().Set("Retry-After", "60")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = fmt.Fprint(w, `{"success":false,"errors":[{"code":1000,"message":"rate limited"}]}`)
					return
				}
				<-r.Context().Done()
			}, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			if kind == "shutdown" {
				go func() { <-entered; cancel() }()
			}
			start := time.Now()
			_, err := c.GetTunnel(ctx, "account", "tunnel")
			expected := context.DeadlineExceeded
			if kind == "shutdown" {
				expected = context.Canceled
			}
			if !errors.Is(err, expected) {
				t.Fatalf("error=%v want=%v", err, expected)
			}
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Fatalf("call exceeded cancellation budget: %s", elapsed)
			}
			if calls.Load() != 1 {
				t.Fatalf("unexpected retries after deadline: %d", calls.Load())
			}
		})
	}
}

func TestClientRetryLimit(t *testing.T) {
	var calls atomic.Int32
	c := deadlineClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After-Ms", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, `{"success":false,"errors":[]}`)
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.GetTunnel(ctx, "account", "tunnel"); err == nil {
		t.Fatal("expected rate limit error")
	}
	if calls.Load() != 3 {
		t.Fatalf("attempts=%d want3", calls.Load())
	}
}

func TestClientEveryPagedOperationRetainsCancellation(t *testing.T) {
	operations := []struct {
		name, result string
		call         func(context.Context, Client) error
	}{
		{"dns", `{"id":"first","name":"test.example.com","type":"TXT","content":"test"}`, func(ctx context.Context, c Client) error { _, e := c.ListDNSRecords(ctx, "zone"); return e }},
		{"filtered dns", `{"id":"first","name":"test.example.com","type":"TXT","content":"test"}`, func(ctx context.Context, c Client) error {
			_, e := c.ListDNSRecordsByNameType(ctx, "zone", "test.example.com", "TXT")
			return e
		}},
		{"zones", `{"id":"first","name":"example.com","account":{"id":"account"}}`, func(ctx context.Context, c Client) error { _, e := c.ListZones(ctx); return e }},
		{"apps", `{"id":"first","name":"test","type":"self_hosted","domain":"example.com"}`, func(ctx context.Context, c Client) error { _, e := c.ListAccessApplications(ctx, "account"); return e }},
		{"tags", `{"name":"test"}`, func(ctx context.Context, c Client) error { _, e := c.ListAccessTags(ctx, "account"); return e }},
		{"policies", `{"id":"first","name":"test","decision":"allow"}`, func(ctx context.Context, c Client) error { _, e := c.ListAccessPolicies(ctx, "account"); return e }},
		{"groups", `{"id":"first","name":"test"}`, func(ctx context.Context, c Client) error { _, e := c.ListAccessGroups(ctx, "account"); return e }},
		{"tokens", `{"id":"first","name":"test"}`, func(ctx context.Context, c Client) error { _, e := c.ListServiceTokens(ctx, "account"); return e }},
	}
	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			var calls atomic.Int32
			c := deadlineClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if page := r.URL.Query().Get("page"); page == "" || page == "1" {
					_, _ = fmt.Fprintf(w, `{"success":true,"result":[%s]}`, op.result)
					return
				}
				<-r.Context().Done()
			}, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			if err := op.call(ctx, c); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("page2 error=%v", err)
			}
			if calls.Load() != 2 {
				t.Fatalf("requests=%d want2", calls.Load())
			}
		})
	}
}

func TestClientRepeatedPagesFailWithoutPartialInventory(t *testing.T) {
	var requests int
	transport := deadlineTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		body := `{"success":true,"result":[{"id":"repeat","name":"repeat.example.com","type":"TXT","content":"same"}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	c, err := NewClient("test-token", WithHTTPClient(&http.Client{Transport: transport}))
	if err != nil {
		t.Fatal(err)
	}
	records, err := c.ListDNSRecords(context.Background(), "zone")
	if err == nil || records != nil || requests != maxListPages {
		t.Fatalf("records=%d requests=%d err=%v", len(records), requests, err)
	}
}

package e2ecleanup

import (
	cfcloudflare "cfgate.io/cfgate/internal/cloudflare"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cloudflare "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/cloudflare/cloudflare-go/v7/packages/pagination"
	"github.com/cloudflare/cloudflare-go/v7/shared"
	"github.com/cloudflare/cloudflare-go/v7/zero_trust"
)

func TestCleanupPagerRetainsOperationCancellation(t *testing.T) {
	for _, stallBody := range []bool{false, true} {
		t.Run(fmt.Sprintf("body=%t", stallBody), func(t *testing.T) {
			var pages atomic.Int32
			canceled := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if pages.Add(1) == 1 {
					_, _ = io.WriteString(w, `{"success":true,"result":[{"id":"one","name":"e2e-run-test-1-1"}],"result_info":{"page":1,"total_pages":2}}`)
					return
				}
				if stallBody {
					_, _ = io.WriteString(w, `{"success":true,"result":[`)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
				close(canceled)
			}))
			defer server.Close()
			defer server.CloseClientConnections()
			operation, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			cf := cloudflare.NewClient(option.WithAPIToken("test-token"), option.WithBaseURL(server.URL), option.WithHTTPClient(HTTPClient(operation)), option.WithMaxRetries(2), option.WithRequestTimeout(2*time.Second))
			started := time.Now()
			iterator := cfcloudflare.AllPages(operation, func(opts ...option.RequestOption) (*pagination.V4PagePaginationArray[shared.CloudflareTunnel], error) {
				return cf.ZeroTrust.Tunnels.Cloudflared.List(operation, zero_trust.TunnelCloudflaredListParams{AccountID: cloudflare.F("test-account")}, opts...)
			})
			count := 0
			var pageError error
			for _, err := range iterator {
				if err != nil {
					pageError = err
					break
				}
				count++
			}
			if pageError == nil || count != 1 || pages.Load() != 2 {
				t.Fatalf("count=%d pages=%d err=%v", count, pages.Load(), pageError)
			}
			if time.Since(started) > time.Second {
				t.Fatal("later page escaped the operation deadline")
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("stalled response remained open")
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type observedBody struct{ closed chan struct{} }

func (b *observedBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b *observedBody) Close() error             { close(b.closed); return nil }

func TestCleanupTransportClosesBodyOnCancellation(t *testing.T) {
	operation, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := &observedBody{closed: make(chan struct{})}
	transport := operationTransport{operation: operation, base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: body, Header: http.Header{}, Request: r}, nil
	})}
	req, err := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-body.closed:
	case <-time.After(time.Second):
		t.Fatal("operation cancellation did not close response body")
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupTransportUsesEarlierDeadline(t *testing.T) {
	for _, requestFirst := range []bool{false, true} {
		early, late := time.Now().Add(time.Minute), time.Now().Add(2*time.Minute)
		operationDeadline, requestDeadline := early, late
		if requestFirst {
			operationDeadline, requestDeadline = late, early
		}
		operation, cancelOperation := context.WithDeadline(context.Background(), operationDeadline)
		requestContext, cancelRequest := context.WithDeadline(context.Background(), requestDeadline)
		transport := operationTransport{operation: operation, base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			deadline, ok := r.Context().Deadline()
			if !ok || !deadline.Equal(early) {
				t.Fatalf("deadline=%v want=%v", deadline, early)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
		})}
		req, err := http.NewRequestWithContext(requestContext, http.MethodGet, "https://example.invalid", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		cancelRequest()
		cancelOperation()
	}
}

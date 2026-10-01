package e2ecleanup

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"
)

// HTTPClient is confined to one cleanup operation, never cached or shared across
// operations. It bounds response-body reads to the cleanup context. Listings
// must also preserve that context through SDK retries with cloudflare.AllPages.
func HTTPClient(operation context.Context) *http.Client {
	return &http.Client{Timeout: 30 * time.Second, Transport: operationTransport{operation: operation, base: http.DefaultTransport}}
}

type operationTransport struct {
	operation context.Context
	base      http.RoundTripper
}

func (t operationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var ctx context.Context
	var cancel context.CancelFunc
	if deadline, ok := t.operation.Deadline(); ok {
		ctx, cancel = context.WithDeadline(request.Context(), deadline)
	} else {
		ctx, cancel = context.WithCancel(request.Context())
	}
	stopOperation := context.AfterFunc(t.operation, cancel)
	release := func() { stopOperation(); cancel() }
	if t.operation.Err() != nil {
		release()
		return nil, t.operation.Err()
	}
	response, err := t.base.RoundTrip(request.WithContext(ctx))
	if err != nil {
		release()
		return response, err
	}
	body := &onceBody{ReadCloser: response.Body}
	stopBody := context.AfterFunc(ctx, func() { _ = body.Close() })
	response.Body = &operationBody{ReadCloser: body, release: func() { stopBody(); release() }}
	return response, nil
}

type onceBody struct {
	io.ReadCloser
	once sync.Once
	err  error
}

func (b *onceBody) Close() error {
	b.once.Do(func() { b.err = b.ReadCloser.Close() })
	return b.err
}

type operationBody struct {
	io.ReadCloser
	release func()
}

func (b *operationBody) Close() error {
	defer b.release()
	return b.ReadCloser.Close()
}

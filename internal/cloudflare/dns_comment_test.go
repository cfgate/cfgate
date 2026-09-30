package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-logr/logr"
	"net/http"
	"net/http/httptest"
	"testing"
)

const commentTestOwner = "0549524e-1a1f-4974-b5ad-b30baa720f74/e6095682-60fb-42c4-8815-401d1141119c"

func TestOwnershipCommentFitsCloudflareLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		comment, _ := payload["comment"].(string)
		if len(comment) > 100 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"success":false,"errors":[{"code":9313,"message":"DNS record comment exceeds the maximum length of 100 characters."}]}`)
			return
		}
		payload["id"] = "record"
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": payload})
	}))
	defer server.Close()
	client, err := NewClient("test-token", WithHTTPClient(&http.Client{Transport: &rewriteConfigTransport{target: server.URL, delegate: server.Client().Transport}}))
	if err != nil {
		t.Fatal(err)
	}
	record, err := client.CreateDNSRecord(context.Background(), "zone", DNSRecord{Name: "app.example", Type: "CNAME", Content: "tunnel.example", Comment: OwnershipComment(commentTestOwner), TTL: 1})
	if err != nil {
		t.Fatal(err)
	}
	if record.Comment != "cfgate/owner="+commentTestOwner || !IsOwnedByCfgate(record, commentTestOwner) {
		t.Fatal("full persistent owner identity not preserved")
	}
}

func TestCompactAndLegacyDataOwnershipStayExact(t *testing.T) {
	for _, test := range []struct {
		comment string
		owned   bool
	}{
		{"cfgate/owner=" + commentTestOwner, true},
		{"heritage=cfgate,cfgate/owner=" + commentTestOwner, true},
		{"cfgate/owner=" + commentTestOwner + "suffix", false},
		{"cfgate/owner=foreign", false},
		{"cfgate/owner=" + commentTestOwner + ",cfgate/owner=foreign", false},
		{"cfgate/owner=foreign,cfgate/owner=" + commentTestOwner, false},
		{"heritage=cfgate,cfgate/owner=foreign,cfgate/owner=" + commentTestOwner, false},
		{"prefix cfgate/owner=" + commentTestOwner, false},
		{"managed by cfgate", false},
	} {
		t.Run(test.comment, func(t *testing.T) {
			data := DNSRecord{ID: "record", Name: "app.example", Type: "CNAME", Content: "tunnel.example", Comment: test.comment}
			if IsOwnedByCfgate(&data, commentTestOwner) != test.owned {
				t.Fatal("incorrect data ownership decision")
			}
			writes := 0
			mock := NewMockClient()
			mock.ListDNSRecordsByNameTypeFunc = func(_ context.Context, _, name, kind string) ([]DNSRecord, error) {
				if kind == "CNAME" && name == data.Name {
					return []DNSRecord{data}, nil
				}
				if kind == "TXT" && name == "_cfgate."+data.Name {
					return []DNSRecord{BuildOwnershipTXTRecord(data.Name, commentTestOwner, "resource", "_cfgate")}, nil
				}
				return nil, nil
			}
			mock.DeleteDNSRecordFunc = func(_ context.Context, _, id string) error {
				if id != "record" {
					t.Fatal("wrong delete target")
				}
				writes++
				return nil
			}
			deleted, err := NewDNSService(mock, logr.Discard()).DeleteOwnedRecord(context.Background(), "zone", data, commentTestOwner, "_cfgate")
			if err != nil || deleted != test.owned || writes != map[bool]int{true: 1, false: 0}[test.owned] {
				t.Fatalf("cleanup deleted=%t writes=%d err=%v", deleted, writes, err)
			}
		})
	}
	txt := DNSRecord{Type: "TXT", Content: "unrelated", Comment: OwnershipComment(commentTestOwner)}
	if IsOwnedByCfgate(&txt, commentTestOwner) {
		t.Fatal("data comment must not replace TXT content ownership")
	}
}

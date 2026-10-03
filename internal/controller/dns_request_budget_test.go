package controller

import (
	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/controller/status"
	"context"
	"errors"
	"fmt"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"testing"
)

// Counts logical provider operations. SDK pagination/retries can add HTTP requests.
type dnsRequestMeter struct {
	calls, remaining int
	limited          bool
}

func (m *dnsRequestMeter) request() error {
	m.calls++
	if m.limited {
		if m.remaining == 0 {
			return errors.New("simulated shared Cloudflare 429")
		}
		m.remaining--
	}
	return nil
}
func meteredDNSStore(t *testing.T, store map[string]map[string]cloudflare.DNSRecord, m *dnsRequestMeter) *cloudflare.MockClient {
	mock := dnsLifecycleStore(t, store)
	list, find, create, update, remove := mock.ListDNSRecordsFunc, mock.ListDNSRecordsByNameTypeFunc, mock.CreateDNSRecordFunc, mock.UpdateDNSRecordFunc, mock.DeleteDNSRecordFunc
	mock.ListDNSRecordsFunc = func(ctx context.Context, z string) ([]cloudflare.DNSRecord, error) {
		if err := m.request(); err != nil {
			return nil, err
		}
		return list(ctx, z)
	}
	mock.ListDNSRecordsByNameTypeFunc = func(ctx context.Context, z, n, k string) ([]cloudflare.DNSRecord, error) {
		if err := m.request(); err != nil {
			return nil, err
		}
		return find(ctx, z, n, k)
	}
	mock.CreateDNSRecordFunc = func(ctx context.Context, z string, r cloudflare.DNSRecord) (*cloudflare.DNSRecord, error) {
		if err := m.request(); err != nil {
			return nil, err
		}
		return create(ctx, z, r)
	}
	mock.UpdateDNSRecordFunc = func(ctx context.Context, z, id string, r cloudflare.DNSRecord) (*cloudflare.DNSRecord, error) {
		if err := m.request(); err != nil {
			return nil, err
		}
		return update(ctx, z, id, r)
	}
	mock.DeleteDNSRecordFunc = func(ctx context.Context, z, id string) error {
		if err := m.request(); err != nil {
			return err
		}
		return remove(ctx, z, id)
	}
	return mock
}

func TestDNSProviderOperationCounts(t *testing.T) {
	for _, size := range []int{10, 50, 100, 1000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			ctx := context.Background()
			dns := dnsIdentityFixture()
			dns.Spec.Source.Explicit = nil
			for i := range size {
				dns.Spec.Source.Explicit = append(dns.Spec.Source.Explicit, cfg.DNSExplicitHostname{Hostname: fmt.Sprintf("app-%04d.example.com", i)})
			}
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "operator", UID: "installation"}}
			kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithStatusSubresource(dns).WithObjects(ns, dns).Build()
			meter := &dnsRequestMeter{}
			store := map[string]map[string]cloudflare.DNSRecord{}
			mock := meteredDNSStore(t, store, meter)
			r := &CloudflareDNSReconciler{Client: kube, APIReader: kube, CFClient: mock, InstallationNamespace: "operator", Recorder: &fakeEventRecorder{}}
			run := func(phase string) {
				t.Helper()
				meter.calls = 0
				if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dns)}); err != nil {
					t.Fatal(err)
				}
				if err := kube.Get(ctx, client.ObjectKeyFromObject(dns), dns); err != nil {
					t.Fatal(err)
				}
				if c := status.FindCondition(dns.Status.Conditions, status.ConditionTypeReady); c == nil || c.Status != metav1.ConditionTrue {
					t.Fatalf("%s not ready: %+v", phase, dns.Status.Conditions)
				}
				if phase == "no-op" && meter.calls > 6*size {
					t.Fatalf("unchanged-record request budget regressed: %d for %d records", meter.calls, size)
				}
				t.Logf("%s: %d operations for %d hostnames", phase, meter.calls, size)
			}
			run("create")
			run("no-op")
			hosts, err := r.collectHostnames(ctx, dns, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.prepareDNSWrites(ctx, dns, hosts, map[string]string{"example.com": "zone"}, cloudflare.NewDNSService(mock, logr.Discard())); err != nil {
				t.Fatal(err)
			}
			run("recover-existing-intent")
			meter.calls = 0
			if err := r.cleanupRecordsWithFallback(ctx, dns); err != nil {
				t.Fatal(err)
			}
			t.Logf("cleanup: %d operations for %d hostnames", meter.calls, size)
			if len(store["zone"]) != 0 {
				t.Fatal("cleanup leaked records")
			}
		})
	}
}

func TestDNSSharedQuotaRetryProgress(t *testing.T) {
	for _, size := range []int{10, 50, 100} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			ctx := context.Background()
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "operator", UID: "installation"}}
			kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithStatusSubresource(&cfg.CloudflareDNS{}).WithObjects(ns).Build()
			meter := &dnsRequestMeter{limited: true}
			store := map[string]map[string]cloudflare.DNSRecord{}
			mock := meteredDNSStore(t, store, meter)
			r := &CloudflareDNSReconciler{Client: kube, APIReader: kube, CFClient: mock, InstallationNamespace: "operator", Recorder: &fakeEventRecorder{}}
			var resources []*cfg.CloudflareDNS
			for n := range 2 {
				dns := dnsIdentityFixture()
				dns.Name = fmt.Sprintf("dns-%d", n)
				dns.UID = types.UID(dns.Name)
				dns.Status.OwnerID = "installation/" + dns.Name
				dns.Spec.Source.Explicit = nil
				for i := range size {
					dns.Spec.Source.Explicit = append(dns.Spec.Source.Explicit, cfg.DNSExplicitHostname{Hostname: fmt.Sprintf("app-%d-%04d.example.com", n, i)})
				}
				if err := kube.Create(ctx, dns); err != nil {
					t.Fatal(err)
				}
				resources = append(resources, dns)
			}
			// Half the first window is consumed by other controllers using this identity.
			complete := false
			previousRecords := -1
			for window := range 8 {
				meter.remaining = 1200
				if window == 0 {
					meter.remaining = 600
				}
				meter.calls = 0
				complete = true
				for _, dns := range resources {
					if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dns)}); err != nil {
						t.Fatal(err)
					}
					if err := kube.Get(ctx, client.ObjectKeyFromObject(dns), dns); err != nil {
						t.Fatal(err)
					}
					c := status.FindCondition(dns.Status.Conditions, status.ConditionTypeReady)
					complete = complete && c != nil && c.Status == metav1.ConditionTrue
				}
				records := len(store["zone"])
				t.Logf("window %d: %d records, %d attempted operations, ready=%v", window, records, meter.calls, complete)
				if complete {
					break
				}
				if records <= previousRecords {
					t.Fatalf("retry made no progress: previous=%d current=%d", previousRecords, records)
				}
				previousRecords = records
			}
			if !complete {
				t.Fatal("resources did not converge within renewed shared windows")
			}
			// Interrupted deletion must also converge without bypassing ownership checks.
			for range 8 {
				meter.remaining = 1200
				complete = true
				for _, dns := range resources {
					if err := r.cleanupRecordsWithFallback(ctx, dns); err != nil {
						complete = false
					}
				}
				if complete {
					break
				}
			}
			if !complete || len(store["zone"]) != 0 {
				t.Fatalf("cleanup did not converge: %d records remain", len(store["zone"]))
			}
		})
	}
}

package controller

import (
	"context"
	"errors"
	"testing"

	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/controller/status"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func dnsIdentityFixture() *cfg.CloudflareDNS {
	return &cfg.CloudflareDNS{ObjectMeta: metav1.ObjectMeta{Name: "dns", Namespace: "app", UID: "resource", Generation: 1, Finalizers: []string{dnsFinalizer}}, Spec: cfg.CloudflareDNSSpec{Cloudflare: &cfg.CloudflareConfig{SecretRef: cfg.SecretRef{Name: "creds"}}, ExternalTarget: &cfg.ExternalTarget{Value: "target.example.net"}, Zones: []cfg.DNSZoneConfig{{Name: "example.com", ID: "zone"}}, Source: cfg.DNSHostnameSource{Explicit: []cfg.DNSExplicitHostname{{Hostname: "app.example.com"}}}}, Status: cfg.CloudflareDNSStatus{OwnerID: "installation/resource"}}
}

func TestDNSEquivalentNamesRecoverAfterLostCheckpoint(t *testing.T) {
	for _, spelling := range []string{"App.example.com", "app.example.com."} {
		t.Run(spelling, func(t *testing.T) {
			ctx := context.Background()
			dns := dnsIdentityFixture()
			dns.Spec.Source.Explicit = append(dns.Spec.Source.Explicit, cfg.DNSExplicitHostname{Hostname: spelling})
			fail := false
			kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithStatusSubresource(dns).WithObjects(dns).WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if fail {
					return errors.New("lost checkpoint")
				}
				return c.SubResource(sub).Update(ctx, obj, opts...)
			}}).Build()
			store := map[string]map[string]cloudflare.DNSRecord{}
			mock := dnsLifecycleStore(t, store)
			r := &CloudflareDNSReconciler{Client: kube, APIReader: kube, CFClient: mock, Recorder: &fakeEventRecorder{}}
			hosts, err := r.collectHostnames(ctx, dns, nil)
			if err != nil {
				t.Fatal(err)
			}
			svc := cloudflare.NewDNSService(mock, logr.Discard())
			zones := map[string]string{"example.com": "zone"}
			if err := r.prepareDNSWrites(ctx, dns, hosts, zones, svc); err != nil {
				t.Fatal(err)
			}
			if len(hosts) != 1 || len(dns.Status.PendingWrites) != 1 {
				t.Fatalf("duplicate canonical identity: hosts=%v intents=%v", hosts, dns.Status.PendingWrites)
			}
			if err := r.syncRecords(ctx, dns, "target.example.net", hosts, zones, svc); err != nil {
				t.Fatal(err)
			}
			fail = true
			if err := r.updateStatus(ctx, dns); err == nil {
				t.Fatal("checkpoint unexpectedly persisted")
			}
			fail = false
			if err := kube.Get(ctx, client.ObjectKeyFromObject(dns), dns); err != nil {
				t.Fatal(err)
			}
			if err := r.cleanupRecordsWithFallback(ctx, dns); err != nil {
				t.Fatal(err)
			}
			if len(store["zone"]) != 0 {
				t.Fatalf("leaked records: %v", store)
			}
		})
	}
}

func TestDNSRecoveryVerifiesNewClaimInSameReconcile(t *testing.T) {
	ctx := context.Background()
	dns := dnsIdentityFixture()
	dns.Status.PendingWrites = []cfg.DNSPendingWrite{{ZoneID: "zone", Hostname: "app.example.com", Type: "CNAME", OperationID: "0123456789"}}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "operator", UID: "installation"}}
	kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithStatusSubresource(dns).WithObjects(ns, dns).Build()
	store := map[string]map[string]cloudflare.DNSRecord{}
	mock := dnsLifecycleStore(t, store)
	r := &CloudflareDNSReconciler{Client: kube, APIReader: kube, CFClient: mock, InstallationNamespace: "operator", Recorder: &fakeEventRecorder{}}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dns)}); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(dns), dns); err != nil {
		t.Fatal(err)
	}
	if len(store["zone"]) != 2 {
		t.Fatalf("expected published record and claim: %v", store)
	}
	for _, kind := range []string{status.ConditionTypeOwnershipVerified, status.ConditionTypeReady} {
		c := status.FindCondition(dns.Status.Conditions, kind)
		if c == nil || c.Status != metav1.ConditionTrue {
			t.Fatalf("%s: %+v", kind, c)
		}
	}
}

func TestDNSLegacyDuplicateIntents(t *testing.T) {
	for _, scenario := range []string{"first-operation", "second-operation", "foreign", "unknown-operation", "different-baseline"} {
		t.Run(scenario, func(t *testing.T) {
			dns := dnsIdentityFixture()
			dns.Status.PendingWrites = []cfg.DNSPendingWrite{{ZoneID: "zone", Hostname: "App.example.com", Type: "CNAME", OperationID: "first00000"}, {ZoneID: "zone", Hostname: "app.example.com.", Type: "CNAME", OperationID: "second0000"}}
			op := "first00000"
			if scenario == "second-operation" {
				op = "second0000"
			}
			if scenario == "unknown-operation" {
				op = "unknown000"
			}
			owner := dns.Status.OwnerID
			if scenario == "foreign" {
				owner = "foreign/resource"
			}
			if scenario == "different-baseline" {
				dns.Status.PendingWrites[1].PreviousRecordID = "other"
			}
			record := cloudflare.DNSRecord{ID: "created", Name: "app.example.com", Type: "CNAME", Comment: cloudflare.OwnershipComment(owner) + ",op=" + op}
			store := map[string]map[string]cloudflare.DNSRecord{"zone": {"created": record}}
			err := recoverDNSWrites(context.Background(), dns, cloudflare.NewDNSService(dnsLifecycleStore(t, store), logr.Discard()))
			allowed := scenario == "first-operation" || scenario == "second-operation"
			if allowed {
				if err != nil || len(dns.Status.Records) != 1 || len(dns.Status.PendingWrites) != 0 {
					t.Fatalf("recovery: err=%v status=%+v", err, dns.Status)
				}
			} else {
				if err == nil || len(dns.Status.PendingWrites) != 2 {
					t.Fatalf("conflict evidence lost: err=%v status=%+v", err, dns.Status)
				}
			}
			if len(store["zone"]) != 1 {
				t.Fatal("recovery mutated remote records")
			}
		})
	}
}

func TestDNSCanonicalCollectionAndConflicts(t *testing.T) {
	ctx := context.Background()
	dns := dnsIdentityFixture()
	tunnel, class, gw, route, _ := emissionFixtures()
	dns.Spec.TunnelRef = &cfg.DNSTunnelRef{Name: tunnel.Name}
	dns.Spec.Source.GatewayRoutes = &cfg.DNSGatewayRoutesSource{Enabled: true}
	dns.Spec.Source.Explicit = []cfg.DNSExplicitHostname{{Hostname: "APP.EXAMPLE.COM.", TTL: 300}}
	kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithObjects(tunnel, class, gw, route).Build()
	r := &CloudflareDNSReconciler{Client: kube, APIReader: kube}
	hosts, err := r.collectHostnames(ctx, dns, tunnel)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts["app.example.com"].TTL != 300 {
		t.Fatalf("explicit precedence lost: %v", hosts)
	}
	dns.Spec.Source.Explicit = append(dns.Spec.Source.Explicit, cfg.DNSExplicitHostname{Hostname: "app.example.com", TTL: 600})
	if _, err := r.collectHostnames(ctx, dns, tunnel); err == nil {
		t.Fatal("conflicting explicit settings accepted")
	}
	// Direct planning must also reject equivalent keys before any remote read/write.
	mock := cloudflare.NewMockClient()
	mock.ListDNSRecordsByNameTypeFunc = func(context.Context, string, string, string) ([]cloudflare.DNSRecord, error) {
		t.Fatal("conflict reached provider")
		return nil, nil
	}
	if err := r.prepareDNSWrites(ctx, dns, map[string]HostnameConfig{"APP.example.com": {TTL: 300}, "app.example.com.": {TTL: 600}}, map[string]string{"example.com": "zone"}, cloudflare.NewDNSService(mock, logr.Discard())); err == nil {
		t.Fatal("conflicting intents accepted")
	}
}

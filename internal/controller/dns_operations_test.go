package controller

import (
	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"context"
	"errors"
	"fmt"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"strings"
	"testing"
)

func TestDNSWriteRecoverySurvivesLostStatusAndSpecEdits(t *testing.T) {
	for _, scenario := range []string{"recreated", "historical-zone", "foreign", "unexplained-owned"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			dns := &cfg.CloudflareDNS{ObjectMeta: metav1.ObjectMeta{Name: "dns", Namespace: "app", UID: "resource"}, Spec: cfg.CloudflareDNSSpec{Policy: cfg.DNSPolicySync, Cloudflare: &cfg.CloudflareConfig{SecretRef: cfg.SecretRef{Name: "creds"}}, Zones: []cfg.DNSZoneConfig{{Name: "example.com", ID: "a"}}}, Status: cfg.CloudflareDNSStatus{OwnerID: "installation/resource"}}
			failStatus := false
			kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithStatusSubresource(dns).WithObjects(dns).WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if failStatus {
					return errors.New("status unavailable")
				}
				return c.SubResource(sub).Update(ctx, obj, opts...)
			}}).Build()
			store := map[string]map[string]cloudflare.DNSRecord{}
			mock := dnsLifecycleStore(t, store)
			service := cloudflare.NewDNSService(mock, logr.Discard())
			r := &CloudflareDNSReconciler{Client: kube, APIReader: kube, CFClient: mock, Recorder: &fakeEventRecorder{}}
			hosts := map[string]HostnameConfig{"app.example.com": {}}
			zones := map[string]string{"example.com": "a"}
			if err := r.prepareDNSWrites(ctx, dns, hosts, zones, service); err != nil {
				t.Fatal(err)
			}
			if err := r.syncRecords(ctx, dns, "target.example.net", hosts, zones, service); err != nil {
				t.Fatal(err)
			}
			if err := r.updateStatus(ctx, dns); err != nil {
				t.Fatal(err)
			}
			oldID := dns.Status.Records[0].RecordID
			zone := "a"
			if scenario == "historical-zone" {
				zone = "b"
				zones["example.com"] = zone
			} else {
				delete(store["a"], oldID)
			}
			if err := r.prepareDNSWrites(ctx, dns, hosts, zones, service); err != nil {
				t.Fatal(err)
			}
			if err := r.syncRecords(ctx, dns, "target.example.net", hosts, zones, service); err != nil {
				t.Fatal(err)
			}
			newID := dns.Status.Records[0].RecordID
			if newID == oldID {
				t.Fatal("fixture did not model a new incarnation")
			}
			failStatus = true
			if err := r.updateStatus(ctx, dns); err == nil {
				t.Fatal("expected lost status write")
			}
			failStatus = false
			// Restart from durable state, then change the desired destination again.
			if err := kube.Get(ctx, client.ObjectKeyFromObject(dns), dns); err != nil {
				t.Fatal(err)
			}
			if len(dns.Status.PendingWrites) != 1 {
				t.Fatalf("intent lost: %+v", dns.Status)
			}
			dns.Spec.Zones = []cfg.DNSZoneConfig{{Name: "example.com", ID: "c"}}
			if err := kube.Update(ctx, dns); err != nil {
				t.Fatal(err)
			}
			if scenario == "foreign" || scenario == "unexplained-owned" {
				replacement := store[zone][newID]
				replacement.ID = "unrelated"
				replacement.Comment = cloudflare.OwnershipComment(dns.Status.OwnerID)
				if scenario == "foreign" {
					replacement.Comment = cloudflare.OwnershipComment("another/owner")
				}
				delete(store[zone], newID)
				store[zone][replacement.ID] = replacement
				if err := r.cleanupRecordsWithFallback(ctx, dns); err == nil || !strings.Contains(err.Error(), "recovery conflict") {
					t.Fatalf("wanted recovery conflict, got %v", err)
				}
				if _, exists := store[zone][replacement.ID]; !exists {
					t.Fatal("unrelated replacement deleted")
				}
				return
			}
			if err := r.cleanupRecordsWithFallback(ctx, dns); err != nil {
				t.Fatal(err)
			}
			for zone, records := range store {
				if len(records) != 0 {
					t.Fatalf("zone %s leaked records: %v", zone, records)
				}
			}
		})
	}
}

func TestDNSWriteIntentMustBePersistedBeforeMutation(t *testing.T) {
	ctx := context.Background()
	dns := &cfg.CloudflareDNS{ObjectMeta: metav1.ObjectMeta{Name: "dns", Namespace: "app"}, Status: cfg.CloudflareDNSStatus{OwnerID: "install/resource"}}
	kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithStatusSubresource(dns).WithObjects(dns).WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
		return errors.New("status unavailable")
	}}).Build()
	store := map[string]map[string]cloudflare.DNSRecord{}
	service := cloudflare.NewDNSService(dnsLifecycleStore(t, store), logr.Discard())
	r := &CloudflareDNSReconciler{Client: kube, APIReader: kube}
	if err := r.prepareDNSWrites(ctx, dns, map[string]HostnameConfig{"app.example.com": {}}, map[string]string{"example.com": "zone"}, service); err == nil {
		t.Fatal("unpersisted intent accepted")
	}
	if len(store) != 0 {
		t.Fatal("remote writes preceded durable intent")
	}
}

func TestDNSOwnershipPrefixAndDisabledClaims(t *testing.T) {
	ctx := context.Background()
	dns := &cfg.CloudflareDNS{ObjectMeta: metav1.ObjectMeta{Name: "dns", Namespace: "app"}, Spec: cfg.CloudflareDNSSpec{Cloudflare: &cfg.CloudflareConfig{SecretRef: cfg.SecretRef{Name: "creds"}}, Zones: []cfg.DNSZoneConfig{{Name: "example.com", ID: "zone"}}}, Status: cfg.CloudflareDNSStatus{OwnerID: "install/resource"}}
	kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithStatusSubresource(dns).WithObjects(dns).Build()
	store := map[string]map[string]cloudflare.DNSRecord{}
	mock := dnsLifecycleStore(t, store)
	service := cloudflare.NewDNSService(mock, logr.Discard())
	r := &CloudflareDNSReconciler{Client: kube, APIReader: kube, CFClient: mock, Recorder: &fakeEventRecorder{}}
	hosts := map[string]HostnameConfig{"app.example.com": {}}
	zones := map[string]string{"example.com": "zone"}
	if err := r.prepareDNSWrites(ctx, dns, hosts, zones, service); err != nil {
		t.Fatal(err)
	}
	if err := r.syncRecords(ctx, dns, "target.example.net", hosts, zones, service); err != nil {
		t.Fatal(err)
	}
	if err := r.updateStatus(ctx, dns); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(dns), dns); err != nil {
		t.Fatal(err)
	}
	dns.Spec.Ownership.TXTRecord.Prefix = "_replacement"
	if err := r.prepareDNSWrites(ctx, dns, hosts, zones, service); err == nil {
		t.Fatal("prefix change accepted")
	}
	dns.Spec.Ownership.TXTRecord.Prefix = ""
	disabled := false
	dns.Spec.Ownership.TXTRecord.Enabled = &disabled
	if err := r.prepareDNSWrites(ctx, dns, hosts, zones, service); err != nil {
		t.Fatal(err)
	}
	if err := r.syncRecords(ctx, dns, "target.example.net", hosts, zones, service); err != nil {
		t.Fatal(err)
	}
	if len(store["zone"]) != 2 {
		t.Fatal("disabling claims discarded historical claim")
	}
	dns.Spec.Ownership.TXTRecord.Prefix = "_replacement" // Even a bypassed admission cannot redirect cleanup.
	if err := r.cleanupRecordsWithFallback(ctx, dns); err != nil {
		t.Fatal(err)
	}
	if len(store["zone"]) != 0 {
		t.Fatalf("claim leaked: %v", store)
	}
}

func TestDNSRecoveryAtInventoryLimit(t *testing.T) {
	ctx := context.Background()
	dns := &cfg.CloudflareDNS{Status: cfg.CloudflareDNSStatus{OwnerID: "install/resource"}}
	store := map[string]map[string]cloudflare.DNSRecord{"new": {}}
	for i := 0; i < 1000; i++ {
		host := fmt.Sprintf("app-%d.example.com", i)
		dns.Status.Records = append(dns.Status.Records, cfg.DNSRecordSyncStatus{ZoneID: "old", Hostname: host, Type: "CNAME", RecordID: fmt.Sprint(i)})
		dns.Status.PendingWrites = append(dns.Status.PendingWrites, cfg.DNSPendingWrite{ZoneID: "new", Hostname: host, Type: "CNAME", OperationID: "0123456789"})
		store["new"][host] = cloudflare.DNSRecord{ID: host, Name: host, Type: "CNAME", Comment: cloudflare.OwnershipComment(dns.Status.OwnerID) + ",op=0123456789"}
	}
	if err := recoverDNSWrites(ctx, dns, cloudflare.NewDNSService(dnsLifecycleStore(t, store), logr.Discard())); err != nil {
		t.Fatal(err)
	}
	if len(dns.Status.Records) != 1000 || len(dns.Status.PendingWrites) != 0 {
		t.Fatal("incomplete recovery")
	}
	for _, record := range dns.Status.Records {
		if record.ZoneID != "new" {
			t.Fatal("obsolete record retained")
		}
	}
}

func TestDNSConflictCanBeExplicitlyAdoptedOnRetry(t *testing.T) {
	ctx := context.Background()
	dns := &cfg.CloudflareDNS{ObjectMeta: metav1.ObjectMeta{Name: "dns", Namespace: "app"}, Status: cfg.CloudflareDNSStatus{OwnerID: "install/resource"}}
	kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithStatusSubresource(dns).WithObjects(dns).Build()
	store := map[string]map[string]cloudflare.DNSRecord{"zone": {"existing": {ID: "existing", Name: "app.example.com", Type: "CNAME", Content: "target.example.net"}}}
	mock := dnsLifecycleStore(t, store)
	service := cloudflare.NewDNSService(mock, logr.Discard())
	r := &CloudflareDNSReconciler{Client: kube, APIReader: kube, Recorder: &fakeEventRecorder{}}
	hosts := map[string]HostnameConfig{"app.example.com": {}}
	zones := map[string]string{"example.com": "zone"}
	if err := r.prepareDNSWrites(ctx, dns, hosts, zones, service); err != nil {
		t.Fatal(err)
	}
	if err := r.syncRecords(ctx, dns, "target.example.net", hosts, zones, service); err != nil {
		t.Fatal(err)
	}
	if dns.Status.FailedRecords != 1 {
		t.Fatal("unmarked record adopted without permission")
	}
	if err := r.updateStatus(ctx, dns); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(dns), dns); err != nil {
		t.Fatal(err)
	}
	dns.Annotations = map[string]string{adoptExistingAnnotation: "true"}
	if err := r.prepareDNSWrites(ctx, dns, hosts, zones, service); err != nil {
		t.Fatal(err)
	}
	if err := r.syncRecords(ctx, dns, "target.example.net", hosts, zones, service); err != nil {
		t.Fatal(err)
	}
	if dns.Status.FailedRecords != 0 || dns.Status.SyncedRecords != 1 {
		t.Fatalf("adoption retry failed: %+v", dns.Status)
	}
}

func TestDNSPendingInitialPublicationIsWithdrawnAfterRouteRemoval(t *testing.T) {
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "operator", UID: "install"}}
	tunnel := &cfg.CloudflareTunnel{ObjectMeta: metav1.ObjectMeta{Name: "tunnel", Namespace: "app"}, Status: cfg.CloudflareTunnelStatus{TunnelDomain: "tunnel.example.net"}}
	dns := &cfg.CloudflareDNS{ObjectMeta: metav1.ObjectMeta{Name: "dns", Namespace: "app", UID: "resource", Finalizers: []string{dnsFinalizer}}, Spec: cfg.CloudflareDNSSpec{TunnelRef: &cfg.DNSTunnelRef{Name: "tunnel"}, Zones: []cfg.DNSZoneConfig{{Name: "example.com", ID: "zone"}}, Source: cfg.DNSHostnameSource{GatewayRoutes: &cfg.DNSGatewayRoutesSource{Enabled: true}}}, Status: cfg.CloudflareDNSStatus{OwnerID: "install/resource", PendingWrites: []cfg.DNSPendingWrite{{Hostname: "app.example.com", Type: "CNAME", ZoneID: "zone", OperationID: "0123456789"}}}}
	kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithStatusSubresource(dns).WithObjects(ns, tunnel, dns).Build()
	store := map[string]map[string]cloudflare.DNSRecord{"zone": {"record": {ID: "record", Name: "app.example.com", Type: "CNAME", Comment: cloudflare.OwnershipComment(dns.Status.OwnerID) + ",op=0123456789"}}}
	r := &CloudflareDNSReconciler{Client: kube, APIReader: kube, InstallationNamespace: "operator", CFClient: dnsLifecycleStore(t, store), Recorder: &fakeEventRecorder{}}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dns)}); err != nil {
		t.Fatal(err)
	}
	if len(store["zone"]) != 0 {
		t.Fatal("route removal left pending initial publication active")
	}
}

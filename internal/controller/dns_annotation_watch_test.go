package controller

import (
	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"context"
	"github.com/go-logr/logr"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"testing"
)

func TestDNSCustomAnnotationChangeEnqueuesPublicationAndWithdrawal(t *testing.T) {
	ctx := context.Background()
	tunnel, class, gw, route, _ := emissionFixtures()
	dns := dnsIdentityFixture()
	dns.Spec.Source.Explicit = nil
	dns.Spec.Source.GatewayRoutes = &cfg.DNSGatewayRoutesSource{Enabled: true, AnnotationFilter: "platform.example.com/publish-dns=true"}
	kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithStatusSubresource(dns).WithObjects(dns, tunnel, class, gw, route).WithIndex(&cfg.CloudflareDNS{}, dnsGatewayRoutesEnabledIndex, extractDNSGatewayRoutesEnabled).Build()
	r := &CloudflareDNSReconciler{Client: kube, APIReader: kube, Recorder: &fakeEventRecorder{}}
	store := map[string]map[string]cloudflare.DNSRecord{}
	mock := dnsLifecycleStore(t, store)
	for _, enabled := range []bool{true, false} {
		old := route.DeepCopy()
		if route.Annotations == nil {
			route.Annotations = map[string]string{}
		}
		if enabled {
			route.Annotations["platform.example.com/publish-dns"] = "true"
		} else {
			delete(route.Annotations, "platform.example.com/publish-dns")
		}
		change := event.UpdateEvent{ObjectOld: old, ObjectNew: route}
		if !DNSRouteChangePredicate.Update(change) {
			t.Fatal("annotation-only change filtered")
		}
		if err := kube.Update(ctx, route); err != nil {
			t.Fatal(err)
		}
		queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
		handler.EnqueueRequestsFromMapFunc(r.findAffectedDNSByRoute).Update(ctx, change, queue)
		if queue.Len() != 1 {
			t.Fatalf("got %d enqueues", queue.Len())
		}
		item, _ := queue.Get()
		queue.Done(item)
		queue.ShutDown()
		if item.NamespacedName != client.ObjectKeyFromObject(dns) {
			t.Fatalf("wrong DNS enqueued: %v", item)
		}
		hosts, err := r.collectHostnamesFromRoutes(ctx, dns, tunnel)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.syncRecords(ctx, dns, "target.example.net", hosts, map[string]string{"example.com": "zone"}, cloudflare.NewDNSService(mock, logr.Discard())); err != nil {
			t.Fatal(err)
		}
		if (len(store["zone"]) > 0) != enabled {
			t.Fatalf("publication=%v records=%v", enabled, store)
		}
	}
	if DNSRouteChangePredicate.Update(event.UpdateEvent{ObjectOld: route, ObjectNew: route.DeepCopy()}) {
		t.Fatal("unchanged desired metadata enqueued")
	}
}

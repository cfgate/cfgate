package controller

import (
	"context"
	"errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gateway "sigs.k8s.io/gateway-api/apis/v1"
	"testing"
	"time"
)

func TestGatewayReassignmentQueuesOldAndNewTunnel(t *testing.T) {
	r, _, _ := lifecycleFixture(t)
	old := &gateway.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "app", Annotations: map[string]string{"cfgate.io/tunnel-ref": "edge-a"}}}
	updated := old.DeepCopy()
	updated.Annotations["cfgate.io/tunnel-ref"] = "edge-b"
	change := event.UpdateEvent{ObjectOld: old, ObjectNew: updated}
	if !CfgateAnnotationOrGenerationPredicate.Update(change) {
		t.Fatal("annotation-only reassignment filtered")
	}
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer queue.ShutDown()
	handler.EnqueueRequestsFromMapFunc(r.findTunnelsForGateway).Update(context.Background(), change, queue)
	if queue.Len() != 2 {
		t.Fatalf("queued %d owners, want old and new", queue.Len())
	}
	got := map[string]bool{}
	for range 2 {
		item, _ := queue.Get()
		got[item.Name] = true
		queue.Done(item)
	}
	if !got["edge-a"] || !got["edge-b"] {
		t.Fatalf("owners=%v", got)
	}
}

func TestCredentialDependencyEventTargetsReferencingTunnel(t *testing.T) {
	r, tunnel, _ := lifecycleFixture(t)
	for _, name := range []string{"credentials", "unrelated"} {
		requests := r.findTunnelsForCredentialSecret(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: tunnel.Namespace}})
		if name == "credentials" {
			if len(requests) != 1 || requests[0].NamespacedName != client.ObjectKeyFromObject(tunnel) {
				t.Fatalf("credential event lost owner: %v", requests)
			}
		} else if len(requests) != 0 {
			t.Fatal("unrelated secret event targeted tunnel")
		}
	}
}

func TestProgressRecordsCompletionNotExternalSuccess(t *testing.T) {
	name := "test-cancelled-progress"
	defer lastReconcileProgress.DeleteLabelValues(name)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	finished := make(chan error, 1)
	wrapped := withReconcileProgress(name, reconcile.Func(func(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
		close(started)
		<-ctx.Done()
		return reconcile.Result{}, ctx.Err()
	}))
	go func() { _, err := wrapped.Reconcile(ctx, reconcile.Request{}); finished <- err }()
	<-started
	if got := progressMetricValue(t, name); got != 0 {
		t.Fatal("unfinished work recorded as completed")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled reconcile did not finish")
	}
	if got := progressMetricValue(t, name); got == 0 {
		t.Fatal("failed but completed iteration not recorded")
	}
}

func progressMetricValue(t *testing.T, name string) float64 {
	t.Helper()
	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "cfgate_controller_last_completed_reconcile_timestamp_seconds" {
			for _, metric := range family.Metric {
				for _, label := range metric.Label {
					if label.GetName() == "controller" && label.GetValue() == name {
						return metric.GetGauge().GetValue()
					}
				}
			}
		}
	}
	return 0
}

package controller

import (
	"context"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"time"
)

var lastReconcileProgress = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "cfgate_controller_last_completed_reconcile_timestamp_seconds", Help: "Unix time of the last completed reconciliation by controller, including failed attempts; this measures worker progress, not external availability."}, []string{"controller"})

func init() { metrics.Registry.MustRegister(lastReconcileProgress) }

type progressReconciler struct {
	name     string
	delegate reconcile.Reconciler
}

func withReconcileProgress(name string, delegate reconcile.Reconciler) reconcile.Reconciler {
	return progressReconciler{name: name, delegate: delegate}
}
func (r progressReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	result, err := r.delegate.Reconcile(ctx, request)
	lastReconcileProgress.WithLabelValues(r.name).Set(float64(time.Now().Unix()))
	return result, err
}

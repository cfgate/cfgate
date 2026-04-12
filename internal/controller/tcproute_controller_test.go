package controller

import (
	"context"
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"

	"cfgate.io/cfgate/internal/controller/features"
)

func TestTCPRouteReconcile(t *testing.T) {
	r := &TCPRouteReconciler{}
	got, err := r.Reconcile(context.Background(), ctrl.Request{})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if got != (ctrl.Result{}) {
		t.Fatalf("Reconcile() = %#v, want empty result", got)
	}
}

func TestTCPRouteSetupWithManagerSkipsWhenFeatureMissing(t *testing.T) {
	r := &TCPRouteReconciler{
		FeatureGates: &features.FeatureGates{},
	}

	if err := r.SetupWithManager(nil); err != nil {
		t.Fatalf("SetupWithManager() error = %v, want nil skip", err)
	}
}

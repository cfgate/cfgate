package controller

import (
	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/controller/status"
	"context"
	"errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"testing"
)

func TestDNSFailedChecksClearReadiness(t *testing.T) {
	for _, scenario := range []string{"credentials", "zones"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "operator", UID: "install"}}
			dns := &cfg.CloudflareDNS{ObjectMeta: metav1.ObjectMeta{Name: "dns", Namespace: "app", UID: "resource", Generation: 1, Finalizers: []string{dnsFinalizer}}, Spec: cfg.CloudflareDNSSpec{Cloudflare: &cfg.CloudflareConfig{SecretRef: cfg.SecretRef{Name: "missing"}}, ExternalTarget: &cfg.ExternalTarget{Value: "target.example.net"}, Zones: []cfg.DNSZoneConfig{{Name: "example.com"}}}, Status: cfg.CloudflareDNSStatus{OwnerID: "install/resource", Conditions: []metav1.Condition{status.NewCondition(status.ConditionTypeReady, metav1.ConditionTrue, "Ready", "operational", 1)}}}
			kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithStatusSubresource(dns).WithObjects(ns, dns).Build()
			r := &CloudflareDNSReconciler{Client: kube, APIReader: kube, InstallationNamespace: "operator", Recorder: &fakeEventRecorder{}}
			kind := status.ConditionTypeCredentialsValid
			if scenario == "zones" {
				mock := cloudflare.NewMockClient()
				mock.GetZoneByNameFunc = func(context.Context, string) (*cloudflare.Zone, error) { return nil, errors.New("zone lookup failed") }
				r.CFClient = mock
				kind = status.ConditionTypeZonesResolved
			}
			for _, generation := range []int64{1, 2, 3} {
				if err := kube.Get(ctx, client.ObjectKeyFromObject(dns), dns); err != nil {
					t.Fatal(err)
				}
				dns.Generation = generation
				if err := kube.Update(ctx, dns); err != nil {
					t.Fatal(err)
				}
				if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dns)}); err != nil {
					t.Fatal(err)
				}
				if err := kube.Get(ctx, client.ObjectKeyFromObject(dns), dns); err != nil {
					t.Fatal(err)
				}
				for _, conditionType := range []string{status.ConditionTypeReady, kind} {
					got := status.FindCondition(dns.Status.Conditions, conditionType)
					if got == nil || got.Status != metav1.ConditionFalse || got.ObservedGeneration != generation {
						t.Fatalf("%s generation %d: %+v", conditionType, generation, got)
					}
				}
			}
		})
	}
}

package controller

import (
	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"context"
	"github.com/go-logr/logr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gateway "sigs.k8s.io/gateway-api/apis/v1"
	"testing"
)

func TestDNSDiscoveryRequiresAdmittedParent(t *testing.T) {
	for _, scenario := range []string{"allowed", "namespace", "hostname", "class", "section", "port", "backend-unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			tunnel, class, gw, route, _ := emissionFixtures()
			switch scenario {
			case "namespace":
				route.Namespace = "tenant"
			case "hostname":
				gw.Spec.Listeners[0].Hostname = ptr.To(gateway.Hostname("other.example.com"))
			case "class":
				class.Spec.ControllerName = "other.example/controller"
			case "section":
				route.Spec.ParentRefs[0].SectionName = ptr.To(gateway.SectionName("absent"))
			case "port":
				route.Spec.ParentRefs[0].Port = ptr.To(gateway.PortNumber(443))
			}
			kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithObjects(tunnel, class, gw, route).Build()
			dns := &cfg.CloudflareDNS{Spec: cfg.CloudflareDNSSpec{Source: cfg.DNSHostnameSource{GatewayRoutes: &cfg.DNSGatewayRoutesSource{Enabled: true}}}, Status: cfg.CloudflareDNSStatus{OwnerID: "installation/resource"}}
			r := &CloudflareDNSReconciler{Client: kube, APIReader: kube, Recorder: &fakeEventRecorder{}}
			hosts, err := r.collectHostnamesFromRoutes(context.Background(), dns, tunnel)
			if err != nil {
				t.Fatal(err)
			}
			store := map[string]map[string]cloudflare.DNSRecord{}
			mock := dnsLifecycleStore(t, store)
			if err := r.syncRecords(context.Background(), dns, "tunnel.example.net", hosts, map[string]string{"example.com": "zone"}, cloudflare.NewDNSService(mock, logr.Discard())); err != nil {
				t.Fatal(err)
			}
			permitted := scenario == "allowed" || scenario == "backend-unavailable"
			if (len(store["zone"]) > 0) != permitted {
				t.Fatalf("unexpected DNS writes: %v", store)
			}
		})
	}
}

package controller

import (
	"context"
	"encoding/json"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"strings"
	"testing"
)

func TestRendererPreservesOriginTransportAnnotations(t *testing.T) {
	for _, protocol := range []string{"http", "HTTP", "https", "HTTPS", "HtTpS", "grpc"} {
		for _, boolean := range []string{"true", "TRUE", "1", "yes", "false", "FALSE", "0", "no", "invalid", ""} {
			t.Run(protocol+"/"+boolean, func(t *testing.T) {
				tunnel, class, gw, route, service := emissionFixtures()
				route.Annotations = map[string]string{"cfgate.io/origin-protocol": protocol, "cfgate.io/origin-ssl-verify": boolean}
				kube := fake.NewClientBuilder().WithScheme(controllerTestScheme(t)).WithObjects(tunnel, class, gw, route, service).Build()
				r := &CloudflareTunnelReconciler{Client: kube}
				rules, _, err := r.collectIngressRules(context.Background(), tunnel, nil)
				if err != nil {
					t.Fatal(err)
				}
				invalid := protocol == "grpc" || boolean == "invalid" || boolean == ""
				if invalid {
					if len(rules) != 0 {
						t.Fatalf("invalid transport emitted: %+v", rules)
					}
					return
				}
				if len(rules) != 1 {
					t.Fatalf("wanted one rule, got %+v", rules)
				}
				if !strings.HasPrefix(rules[0].Service, strings.ToLower(protocol)+"://") {
					t.Fatal(rules[0].Service)
				}
				wantInsecure := boolean == "false" || boolean == "FALSE" || boolean == "0" || boolean == "no"
				origin := rules[0].OriginRequest
				if origin == nil || origin.NoTLSVerify == nil || ptr.Deref(origin.NoTLSVerify, !wantInsecure) != wantInsecure {
					t.Fatalf("lost override: %+v", origin)
				}
				wire, err := json.Marshal(origin)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(wire), "\"noTLSVerify\":") {
					t.Fatalf("lost wire presence: %s", wire)
				}
			})
		}
	}
}

package features

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
)

// mockDiscovery implements discovery.DiscoveryInterface for testing.
// Only ServerResourcesForGroupVersion is used by DetectFeatures.
type mockDiscovery struct {
	discovery.DiscoveryInterface
	resources map[string]*metav1.APIResourceList
	errors    map[string]error
}

func (m *mockDiscovery) ServerResourcesForGroupVersion(gv string) (*metav1.APIResourceList, error) {
	if err, ok := m.errors[gv]; ok {
		return nil, err
	}
	if rl, ok := m.resources[gv]; ok {
		return rl, nil
	}
	return nil, fmt.Errorf("group version %q not found", gv)
}

func newMockDiscovery() *mockDiscovery {
	return &mockDiscovery{
		resources: make(map[string]*metav1.APIResourceList),
		errors:    make(map[string]error),
	}
}

func (m *mockDiscovery) withResource(gv, resource string) *mockDiscovery {
	if _, ok := m.resources[gv]; !ok {
		m.resources[gv] = &metav1.APIResourceList{
			GroupVersion: gv,
		}
	}
	m.resources[gv].APIResources = append(
		m.resources[gv].APIResources,
		metav1.APIResource{Name: resource},
	)
	return m
}

func (m *mockDiscovery) withError(gv string, err error) *mockDiscovery {
	m.errors[gv] = err
	return m
}

func (m *mockDiscovery) withEmptyGroup(gv string) *mockDiscovery {
	m.resources[gv] = &metav1.APIResourceList{GroupVersion: gv}
	return m
}

// Group version constants used in tests.
var (
	gvAlpha2 = schema.GroupVersion{Group: GatewayAPIGroup, Version: V1Alpha2}.String()
	gvBeta1  = schema.GroupVersion{Group: GatewayAPIGroup, Version: V1Beta1}.String()
	gvV1     = schema.GroupVersion{Group: GatewayAPIGroup, Version: V1}.String()
)

// fullMock returns a mock with all 4 CRDs present.
func fullMock() *mockDiscovery {
	return newMockDiscovery().
		withResource(gvAlpha2, TCPRouteResource).
		withResource(gvAlpha2, UDPRouteResource).
		withResource(gvV1, GRPCRouteResource).
		withResource(gvBeta1, ReferenceGrantResource)
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

func TestConstants(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"GatewayAPIGroup", GatewayAPIGroup, "gateway.networking.k8s.io"},
		{"V1Alpha2", V1Alpha2, "v1alpha2"},
		{"V1Beta1", V1Beta1, "v1beta1"},
		{"V1", V1, "v1"},
		{"TCPRouteResource", TCPRouteResource, "tcproutes"},
		{"UDPRouteResource", UDPRouteResource, "udproutes"},
		{"GRPCRouteResource", GRPCRouteResource, "grpcroutes"},
		{"ReferenceGrantResource", ReferenceGrantResource, "referencegrants"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// crdExists (tested indirectly via DetectFeatures)
// ---------------------------------------------------------------------------

func TestCrdExists(t *testing.T) {
	tcpGVR := schema.GroupVersionResource{
		Group: GatewayAPIGroup, Version: V1Alpha2, Resource: TCPRouteResource,
	}

	t.Run("group not found returns false", func(t *testing.T) {
		mock := newMockDiscovery()
		if crdExists(mock, tcpGVR) {
			t.Error("expected false when group not found")
		}
	})

	t.Run("resource found returns true", func(t *testing.T) {
		mock := newMockDiscovery().withResource(gvAlpha2, TCPRouteResource)
		if !crdExists(mock, tcpGVR) {
			t.Error("expected true when resource found")
		}
	})

	t.Run("resource not in group returns false", func(t *testing.T) {
		mock := newMockDiscovery().withResource(gvAlpha2, "httproutes")
		if crdExists(mock, tcpGVR) {
			t.Error("expected false when resource not in group")
		}
	})

	t.Run("empty resource list returns false", func(t *testing.T) {
		mock := newMockDiscovery().withEmptyGroup(gvAlpha2)
		if crdExists(mock, tcpGVR) {
			t.Error("expected false with empty resource list")
		}
	})

	t.Run("resource found among multiple", func(t *testing.T) {
		mock := newMockDiscovery().
			withResource(gvAlpha2, "httproutes").
			withResource(gvAlpha2, TCPRouteResource)
		if !crdExists(mock, tcpGVR) {
			t.Error("expected true when resource found among multiple")
		}
	})
}

// ---------------------------------------------------------------------------
// DetectFeatures
// ---------------------------------------------------------------------------

func TestDetectFeatures(t *testing.T) {
	tests := []struct {
		name string
		mock *mockDiscovery
		want FeatureGates
	}{
		{
			name: "all CRDs present",
			mock: fullMock(),
			want: FeatureGates{
				TCPRouteCRDExists:      true,
				UDPRouteCRDExists:      true,
				GRPCRouteCRDExists:     true,
				ReferenceGrantCRDExists: true,
			},
		},
		{
			name: "no CRDs present",
			mock: newMockDiscovery(),
			want: FeatureGates{},
		},
		{
			name: "only TCPRoute present",
			mock: newMockDiscovery().withResource(gvAlpha2, TCPRouteResource),
			want: FeatureGates{TCPRouteCRDExists: true},
		},
		{
			name: "only UDPRoute present",
			mock: newMockDiscovery().withResource(gvAlpha2, UDPRouteResource),
			want: FeatureGates{UDPRouteCRDExists: true},
		},
		{
			name: "only GRPCRoute present",
			mock: newMockDiscovery().withResource(gvV1, GRPCRouteResource),
			want: FeatureGates{GRPCRouteCRDExists: true},
		},
		{
			name: "only ReferenceGrant present",
			mock: newMockDiscovery().withResource(gvBeta1, ReferenceGrantResource),
			want: FeatureGates{ReferenceGrantCRDExists: true},
		},
		{
			name: "experimental channel only (TCP+UDP)",
			mock: newMockDiscovery().
				withResource(gvAlpha2, TCPRouteResource).
				withResource(gvAlpha2, UDPRouteResource),
			want: FeatureGates{
				TCPRouteCRDExists: true,
				UDPRouteCRDExists: true,
			},
		},
		{
			name: "standard channel only (GRPC+ReferenceGrant)",
			mock: newMockDiscovery().
				withResource(gvV1, GRPCRouteResource).
				withResource(gvBeta1, ReferenceGrantResource),
			want: FeatureGates{
				GRPCRouteCRDExists:     true,
				ReferenceGrantCRDExists: true,
			},
		},
		{
			name: "mixed: TCP+GRPC present, UDP+ReferenceGrant missing",
			mock: newMockDiscovery().
				withResource(gvAlpha2, TCPRouteResource).
				withResource(gvV1, GRPCRouteResource),
			want: FeatureGates{
				TCPRouteCRDExists:  true,
				GRPCRouteCRDExists: true,
			},
		},
		{
			name: "discovery error for alpha2 group",
			mock: newMockDiscovery().
				withError(gvAlpha2, fmt.Errorf("connection refused")).
				withResource(gvV1, GRPCRouteResource).
				withResource(gvBeta1, ReferenceGrantResource),
			want: FeatureGates{
				GRPCRouteCRDExists:     true,
				ReferenceGrantCRDExists: true,
			},
		},
		{
			name: "discovery error for v1 group",
			mock: newMockDiscovery().
				withError(gvV1, fmt.Errorf("timeout")).
				withResource(gvAlpha2, TCPRouteResource).
				withResource(gvAlpha2, UDPRouteResource).
				withResource(gvBeta1, ReferenceGrantResource),
			want: FeatureGates{
				TCPRouteCRDExists:      true,
				UDPRouteCRDExists:      true,
				ReferenceGrantCRDExists: true,
			},
		},
		{
			name: "discovery error for beta1 group",
			mock: newMockDiscovery().
				withError(gvBeta1, fmt.Errorf("forbidden")).
				withResource(gvAlpha2, TCPRouteResource).
				withResource(gvAlpha2, UDPRouteResource).
				withResource(gvV1, GRPCRouteResource),
			want: FeatureGates{
				TCPRouteCRDExists:  true,
				UDPRouteCRDExists:  true,
				GRPCRouteCRDExists: true,
			},
		},
		{
			name: "empty resource list for alpha2",
			mock: newMockDiscovery().
				withEmptyGroup(gvAlpha2).
				withResource(gvV1, GRPCRouteResource).
				withResource(gvBeta1, ReferenceGrantResource),
			want: FeatureGates{
				GRPCRouteCRDExists:     true,
				ReferenceGrantCRDExists: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gates, err := DetectFeatures(tt.mock)
			if err != nil {
				t.Fatalf("DetectFeatures() error = %v", err)
			}
			if *gates != tt.want {
				t.Errorf("DetectFeatures() = %+v, want %+v", *gates, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Has*Support accessors
// ---------------------------------------------------------------------------

func TestHasSupport(t *testing.T) {
	tests := []struct {
		name   string
		gates  FeatureGates
		method string
		want   bool
	}{
		{"TCP true", FeatureGates{TCPRouteCRDExists: true}, "HasTCPRouteSupport", true},
		{"TCP false", FeatureGates{}, "HasTCPRouteSupport", false},
		{"UDP true", FeatureGates{UDPRouteCRDExists: true}, "HasUDPRouteSupport", true},
		{"UDP false", FeatureGates{}, "HasUDPRouteSupport", false},
		{"GRPC true", FeatureGates{GRPCRouteCRDExists: true}, "HasGRPCRouteSupport", true},
		{"GRPC false", FeatureGates{}, "HasGRPCRouteSupport", false},
		{"ReferenceGrant true", FeatureGates{ReferenceGrantCRDExists: true}, "HasReferenceGrantSupport", true},
		{"ReferenceGrant false", FeatureGates{}, "HasReferenceGrantSupport", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := reflect.ValueOf(&tt.gates)
			result := v.MethodByName(tt.method).Call(nil)
			got := result[0].Bool()
			if got != tt.want {
				t.Errorf("%s() = %v, want %v", tt.method, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SupportedRouteKinds
// ---------------------------------------------------------------------------

func TestSupportedRouteKinds(t *testing.T) {
	tests := []struct {
		name  string
		gates FeatureGates
		want  []string
	}{
		{
			name:  "none enabled",
			gates: FeatureGates{},
			want:  []string{"HTTPRoute"},
		},
		{
			name: "all enabled",
			gates: FeatureGates{
				TCPRouteCRDExists:  true,
				UDPRouteCRDExists:  true,
				GRPCRouteCRDExists: true,
			},
			want: []string{"HTTPRoute", "TCPRoute", "UDPRoute", "GRPCRoute"},
		},
		{
			name:  "TCP only",
			gates: FeatureGates{TCPRouteCRDExists: true},
			want:  []string{"HTTPRoute", "TCPRoute"},
		},
		{
			name:  "UDP only",
			gates: FeatureGates{UDPRouteCRDExists: true},
			want:  []string{"HTTPRoute", "UDPRoute"},
		},
		{
			name:  "GRPC only",
			gates: FeatureGates{GRPCRouteCRDExists: true},
			want:  []string{"HTTPRoute", "GRPCRoute"},
		},
		{
			name:  "TCP+UDP",
			gates: FeatureGates{TCPRouteCRDExists: true, UDPRouteCRDExists: true},
			want:  []string{"HTTPRoute", "TCPRoute", "UDPRoute"},
		},
		{
			name:  "TCP+GRPC",
			gates: FeatureGates{TCPRouteCRDExists: true, GRPCRouteCRDExists: true},
			want:  []string{"HTTPRoute", "TCPRoute", "GRPCRoute"},
		},
		{
			name:  "HTTPRoute always included",
			gates: FeatureGates{ReferenceGrantCRDExists: true},
			want:  []string{"HTTPRoute"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.gates.SupportedRouteKinds()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("SupportedRouteKinds() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// LogFeatures
// ---------------------------------------------------------------------------

func TestLogFeatures(t *testing.T) {
	t.Run("all present: no panic", func(t *testing.T) {
		gates := &FeatureGates{
			TCPRouteCRDExists:      true,
			UDPRouteCRDExists:      true,
			GRPCRouteCRDExists:     true,
			ReferenceGrantCRDExists: true,
		}
		gates.LogFeatures(logr.Discard())
	})

	t.Run("all missing: no panic", func(t *testing.T) {
		gates := &FeatureGates{}
		gates.LogFeatures(logr.Discard())
	})

	t.Run("mixed: no panic", func(t *testing.T) {
		gates := &FeatureGates{
			TCPRouteCRDExists:  true,
			UDPRouteCRDExists:  false,
			GRPCRouteCRDExists: true,
		}
		gates.LogFeatures(logr.Discard())
	})

	t.Run("GRPC missing only: no panic", func(t *testing.T) {
		gates := &FeatureGates{
			TCPRouteCRDExists:      true,
			UDPRouteCRDExists:      true,
			GRPCRouteCRDExists:     false,
			ReferenceGrantCRDExists: true,
		}
		gates.LogFeatures(logr.Discard())
	})

	t.Run("UDP+ReferenceGrant missing: no panic", func(t *testing.T) {
		gates := &FeatureGates{
			TCPRouteCRDExists:  true,
			GRPCRouteCRDExists: true,
		}
		gates.LogFeatures(logr.Discard())
	})

	t.Run("only ReferenceGrant missing: no panic", func(t *testing.T) {
		gates := &FeatureGates{
			TCPRouteCRDExists:  true,
			UDPRouteCRDExists:  true,
			GRPCRouteCRDExists: true,
		}
		gates.LogFeatures(logr.Discard())
	})
}

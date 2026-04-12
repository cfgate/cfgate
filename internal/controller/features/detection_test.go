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
	gvBeta1  = schema.GroupVersion{Group: GatewayAPIGroup, Version: V1Beta1}.String()
)

// fullMock returns a mock with the optional ReferenceGrant CRD present.
func fullMock() *mockDiscovery {
	return newMockDiscovery().withResource(gvBeta1, ReferenceGrantResource)
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
		{"V1Beta1", V1Beta1, "v1beta1"},
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
	referenceGrantGVR := schema.GroupVersionResource{
		Group: GatewayAPIGroup, Version: V1Beta1, Resource: ReferenceGrantResource,
	}

	t.Run("group not found returns false", func(t *testing.T) {
		mock := newMockDiscovery()
		if crdExists(mock, referenceGrantGVR) {
			t.Error("expected false when group not found")
		}
	})

	t.Run("resource found returns true", func(t *testing.T) {
		mock := newMockDiscovery().withResource(gvBeta1, ReferenceGrantResource)
		if !crdExists(mock, referenceGrantGVR) {
			t.Error("expected true when resource found")
		}
	})

	t.Run("resource not in group returns false", func(t *testing.T) {
		mock := newMockDiscovery().withResource(gvBeta1, "httproutes")
		if crdExists(mock, referenceGrantGVR) {
			t.Error("expected false when resource not in group")
		}
	})

	t.Run("empty resource list returns false", func(t *testing.T) {
		mock := newMockDiscovery().withEmptyGroup(gvBeta1)
		if crdExists(mock, referenceGrantGVR) {
			t.Error("expected false with empty resource list")
		}
	})

	t.Run("resource found among multiple", func(t *testing.T) {
		mock := newMockDiscovery().
			withResource(gvBeta1, "httproutes").
			withResource(gvBeta1, ReferenceGrantResource)
		if !crdExists(mock, referenceGrantGVR) {
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
			name: "ReferenceGrant present",
			mock: fullMock(),
			want: FeatureGates{
				ReferenceGrantCRDExists: true,
			},
		},
		{
			name: "no CRDs present",
			mock: newMockDiscovery(),
			want: FeatureGates{},
		},
		{
			name: "only ReferenceGrant present",
			mock: newMockDiscovery().withResource(gvBeta1, ReferenceGrantResource),
			want: FeatureGates{ReferenceGrantCRDExists: true},
		},
		{
			name: "discovery error for beta1 group",
			mock: newMockDiscovery().withError(gvBeta1, fmt.Errorf("forbidden")),
			want: FeatureGates{},
		},
		{
			name: "empty resource list for beta1",
			mock: newMockDiscovery().withEmptyGroup(gvBeta1),
			want: FeatureGates{},
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
		{"base route surface", FeatureGates{}, []string{"HTTPRoute"}},
		{"ReferenceGrant does not change route surface", FeatureGates{ReferenceGrantCRDExists: true}, []string{"HTTPRoute"}},
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
	t.Run("ReferenceGrant present: no panic", func(t *testing.T) {
		gates := &FeatureGates{ReferenceGrantCRDExists: true}
		gates.LogFeatures(logr.Discard())
	})

	t.Run("all missing: no panic", func(t *testing.T) {
		gates := &FeatureGates{}
		gates.LogFeatures(logr.Discard())
	})

	t.Run("only ReferenceGrant missing: no panic", func(t *testing.T) {
		gates := &FeatureGates{}
		gates.LogFeatures(logr.Discard())
	})
}

package features

import (
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
)

// Gateway API group constant.
const (
	// GatewayAPIGroup is the API group for Gateway API resources.
	GatewayAPIGroup = "gateway.networking.k8s.io"
)

// Gateway API version constants.
const (
	// V1Beta1 is the standard channel version.
	V1Beta1 = "v1beta1"
)

// Gateway API resource names (plural form used in API discovery).
const (
	// ReferenceGrantResource is the plural resource name for ReferenceGrant.
	ReferenceGrantResource = "referencegrants"
)

// FeatureGates tracks which optional Gateway API CRDs are available.
// CRD availability is detected once at startup and cached for the
// controller lifetime (CRDs don't change at runtime in practice).
type FeatureGates struct {
	// ReferenceGrantCRDExists indicates ReferenceGrant (v1beta1) is installed.
	// Required for cross-namespace secret/service references.
	ReferenceGrantCRDExists bool
}

// DetectFeatures checks for the existence of optional Gateway API CRDs
// using the discovery client. Results are cached in FeatureGates.
// Each CRD check is independent; detection failures disable that feature.
func DetectFeatures(dc discovery.DiscoveryInterface) (*FeatureGates, error) {
	gates := &FeatureGates{}

	// Check ReferenceGrant (standard channel)
	gates.ReferenceGrantCRDExists = crdExists(dc, schema.GroupVersionResource{
		Group:    GatewayAPIGroup,
		Version:  V1Beta1,
		Resource: ReferenceGrantResource,
	})

	return gates, nil
}

// crdExists checks if a CRD is installed by attempting to list its resources.
// Returns true if the resource exists, false otherwise.
func crdExists(dc discovery.DiscoveryInterface, gvr schema.GroupVersionResource) bool {
	resources, err := dc.ServerResourcesForGroupVersion(gvr.GroupVersion().String())
	if err != nil {
		// Group/version not found means CRD not installed
		return false
	}

	// Verify the specific resource exists within the group
	for _, r := range resources.APIResources {
		if r.Name == gvr.Resource {
			return true
		}
	}
	return false
}

// HasReferenceGrantSupport returns true if ReferenceGrant CRD is available.
func (g *FeatureGates) HasReferenceGrantSupport() bool {
	return g.ReferenceGrantCRDExists
}

// SupportedRouteKinds returns the list of route kinds exposed by the current product.
func (g *FeatureGates) SupportedRouteKinds() []string {
	return []string{"HTTPRoute"}
}

// LogFeatures logs the detected feature availability at startup.
// Called once during manager initialization.
func (g *FeatureGates) LogFeatures(log logr.Logger) {
	log.Info("Gateway API feature detection complete",
		"httpRouteAvailable", true,
		"referenceGrantAvailable", g.ReferenceGrantCRDExists,
	)

	if !g.ReferenceGrantCRDExists {
		log.V(1).Info("ReferenceGrant CRD not found, cross-namespace references disabled",
			"requiredVersion", V1Beta1,
			"installHint", "Install Gateway API standard channel CRDs",
		)
	}
}

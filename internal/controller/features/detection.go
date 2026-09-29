package features

import (
	"errors"
	"fmt"
	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/util/retry"
	"net"
	"time"
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

// FeatureGates tracks optional Gateway API resources discovered at startup.
// Restart the manager after installing or removing Gateway API CRDs.
type FeatureGates struct {
	// ReferenceGrantCRDExists indicates ReferenceGrant (v1beta1) is installed.
	ReferenceGrantCRDExists bool
}

// DetectFeatures validates required Gateway API resources and discovers optional ReferenceGrant support.
// Transient discovery failures are retried; authorization and exhausted transient errors fail startup.
// Configure a finite timeout on the discovery client's REST configuration.
func DetectFeatures(dc discovery.DiscoveryInterface) (*FeatureGates, error) {
	resources, err := discoverResources(dc, GatewayAPIGroup+"/v1")
	if err != nil {
		return nil, fmt.Errorf("required Gateway API v1 discovery failed: %w", err)
	}
	for _, required := range []string{"gatewayclasses", "gateways", "httproutes"} {
		if !containsResource(resources, required) {
			return nil, fmt.Errorf("required Gateway API resource %s is not installed", required)
		}
	}
	resources, err = discoverResources(dc, GatewayAPIGroup+"/"+V1Beta1)
	if apierrors.IsNotFound(err) {
		return &FeatureGates{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ReferenceGrant discovery failed: %w", err)
	}
	return &FeatureGates{ReferenceGrantCRDExists: containsResource(resources, ReferenceGrantResource)}, nil
}

func discoverResources(dc discovery.DiscoveryInterface, groupVersion string) (resources *metav1.APIResourceList, err error) {
	err = retry.OnError(wait.Backoff{Duration: 100 * time.Millisecond, Factor: 2, Steps: 4}, func(err error) bool {
		var networkError net.Error
		return apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) || apierrors.IsTooManyRequests(err) || apierrors.IsServiceUnavailable(err) || errors.As(err, &networkError)
	}, func() error { resources, err = dc.ServerResourcesForGroupVersion(groupVersion); return err })
	return resources, err
}

func containsResource(resources *metav1.APIResourceList, name string) bool {
	if resources == nil {
		return false
	}
	for _, resource := range resources.APIResources {
		if resource.Name == name {
			return true
		}
	}
	return false
}

// crdExists is used by focused discovery tests; production detection preserves errors.
func crdExists(dc discovery.DiscoveryInterface, gvr schema.GroupVersionResource) bool {
	resources, err := discoverResources(dc, gvr.GroupVersion().String())
	return err == nil && containsResource(resources, gvr.Resource)
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

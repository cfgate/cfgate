// Package v1alpha1 contains API Schema definitions for the cfgate v1alpha1 API group.
// +groupName=cfgate.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the API group and version for cfgate resources.
	// All cfgate CRDs (CloudflareTunnel, CloudflareDNS, CloudflareAccessPolicy,
	// CloudflareAccessApplication, CloudflareOriginPolicy) are registered under this GroupVersion.
	GroupVersion = schema.GroupVersion{Group: "cfgate.io", Version: "v1alpha1"}

	// SchemeBuilder is used to register cfgate types with a Kubernetes scheme.
	// Call SchemeBuilder.AddToScheme to add cfgate types to your scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the cfgate v1alpha1 types to a Kubernetes scheme.
	// This function is typically called during controller initialization to register
	// the CloudflareTunnel, CloudflareDNS, CloudflareAccessPolicy,
	// CloudflareAccessApplication, and CloudflareOriginPolicy types.
	AddToScheme = SchemeBuilder.AddToScheme
)

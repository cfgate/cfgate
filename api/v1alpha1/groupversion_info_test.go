package v1alpha1

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestAddToSchemeRegistersCfgateTypes(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}

	for _, obj := range []runtime.Object{
		&CloudflareTunnel{},
		&CloudflareTunnelList{},
		&CloudflareDNS{},
		&CloudflareDNSList{},
		&CloudflareAccessPolicy{},
		&CloudflareAccessPolicyList{},
		&CloudflareAccessApplication{},
		&CloudflareAccessApplicationList{},
	} {
		gvks, _, err := scheme.ObjectKinds(obj)
		if err != nil {
			t.Fatalf("ObjectKinds(%T) error = %v", obj, err)
		}
		if len(gvks) == 0 {
			t.Fatalf("ObjectKinds(%T) returned no GVKs", obj)
		}
		if gvks[0].Group != GroupVersion.Group || gvks[0].Version != GroupVersion.Version {
			t.Fatalf("ObjectKinds(%T) = %v, want group/version %s", obj, gvks[0], GroupVersion.String())
		}
	}
}

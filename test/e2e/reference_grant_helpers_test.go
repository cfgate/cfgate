package e2e_test

import (
	"context"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gateway "sigs.k8s.io/gateway-api/apis/v1beta1"
)

// createExplicitReferenceGrant authorizes only a fixture's declared dependency.
// Callers must opt in; resource helpers do not silently grant cross-namespace access.
func createExplicitReferenceGrant(ctx context.Context, c client.Client, fromNamespace, fromGroup, fromKind, toNamespace, toGroup, toKind, toName string) {
	grant := &gateway.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "e2e-reference-",
			Namespace:    toNamespace,
			Labels:       map[string]string{"cfgate.io/e2e-test": "true", "cfgate.io/e2e-run": testRunID},
		},
		Spec: gateway.ReferenceGrantSpec{
			From: []gateway.ReferenceGrantFrom{{Group: gateway.Group(fromGroup), Kind: gateway.Kind(fromKind), Namespace: gateway.Namespace(fromNamespace)}},
			To:   []gateway.ReferenceGrantTo{{Group: gateway.Group(toGroup), Kind: gateway.Kind(toKind), Name: ptrTo(gateway.ObjectName(toName))}},
		},
	}
	Expect(c.Create(ctx, grant)).To(Succeed())
}

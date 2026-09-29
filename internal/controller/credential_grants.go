package controller

import (
	"context"
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
	gateway "sigs.k8s.io/gateway-api/apis/v1beta1"
)

var errReferenceNotPermitted = errors.New("reference is not permitted by ReferenceGrant")

func requireReferenceGrant(ctx context.Context, reader client.Reader, fromNS, fromGroup, fromKind, toNS, toGroup, toKind, toName string) error {
	if fromNS == toNS {
		return nil
	}
	var grants gateway.ReferenceGrantList
	if err := reader.List(ctx, &grants, client.InNamespace(toNS)); err != nil {
		return fmt.Errorf("read ReferenceGrants in %s: %w", toNS, err)
	}
	for _, grant := range grants.Items {
		fromOK := false
		for _, from := range grant.Spec.From {
			if string(from.Namespace) == fromNS && string(from.Group) == fromGroup && string(from.Kind) == fromKind {
				fromOK = true
				break
			}
		}
		if !fromOK {
			continue
		}
		for _, to := range grant.Spec.To {
			if string(to.Group) == toGroup && string(to.Kind) == toKind && (to.Name == nil || string(*to.Name) == toName) {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: %s %s to %s %s/%s", errReferenceNotPermitted, fromKind, fromNS, toKind, toNS, toName)
}

func requireCredentialGrant(ctx context.Context, reader client.Reader, fromNS, fromKind, secretNS, secretName string) error {
	return requireReferenceGrant(ctx, reader, fromNS, "cfgate.io", fromKind, secretNS, "", "Secret", secretName)
}

package controller

import (
	cfg "cfgate.io/cfgate/api/v1alpha1"
	"context"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const adoptExistingAnnotation = "cfgate.io/adopt-existing"

func (r *CloudflareDNSReconciler) ensureDNSOwnerIdentity(ctx context.Context, dns *cfg.CloudflareDNS) error {
	if r.InstallationNamespace == "" || dns.UID == "" {
		return fmt.Errorf("installation namespace and resource UID are required for DNS ownership")
	}
	var namespace corev1.Namespace
	if err := r.Get(ctx, client.ObjectKey{Name: r.InstallationNamespace}, &namespace); err != nil {
		return fmt.Errorf("resolve installation namespace: %w", err)
	}
	if namespace.UID == "" {
		return fmt.Errorf("installation namespace has no UID")
	}
	expected := string(namespace.UID) + "/" + string(dns.UID)
	if dns.Status.OwnerID != "" {
		if dns.Status.OwnerID != expected {
			return fmt.Errorf("persisted DNS owner belongs to a different installation or resource; explicit migration is required")
		}
		return nil
	}
	dns.Status.OwnerID = expected
	if err := r.Status().Update(ctx, dns); err != nil {
		return fmt.Errorf("persist DNS ownership identity: %w", err)
	}
	return nil
}

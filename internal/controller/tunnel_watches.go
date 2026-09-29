package controller

import (
	cfg "cfgate.io/cfgate/api/v1alpha1"
	"context"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Dependency changes can affect routes across namespaces, so these events fan out to managed tunnels.
func (r *CloudflareTunnelReconciler) findTunnelsForDependency(ctx context.Context, _ client.Object) []reconcile.Request {
	var tunnels cfg.CloudflareTunnelList
	if err := r.lifecycleReader().List(ctx, &tunnels); err != nil {
		log.FromContext(ctx).Error(err, "list tunnels for dependency event")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(tunnels.Items))
	for i := range tunnels.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&tunnels.Items[i])})
	}
	return requests
}

func (r *CloudflareTunnelReconciler) findTunnelsForCredentialSecret(ctx context.Context, secret client.Object) []reconcile.Request {
	var tunnels cfg.CloudflareTunnelList
	if err := r.lifecycleReader().List(ctx, &tunnels); err != nil {
		log.FromContext(ctx).Error(err, "list tunnels for credential event")
		return nil
	}
	var requests []reconcile.Request
	for i := range tunnels.Items {
		tunnel := &tunnels.Items[i]
		namespace := tunnel.Spec.Cloudflare.SecretRef.Namespace
		if namespace == "" {
			namespace = tunnel.Namespace
		}
		credentials := namespace == secret.GetNamespace() && tunnel.Spec.Cloudflare.SecretRef.Name == secret.GetName()
		ca := tunnel.Namespace == secret.GetNamespace() && tunnel.Spec.OriginDefaults.CAPoolSecretRef != nil && tunnel.Spec.OriginDefaults.CAPoolSecretRef.Name == secret.GetName()
		if credentials || ca {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(tunnel)})
		}
	}
	return requests
}

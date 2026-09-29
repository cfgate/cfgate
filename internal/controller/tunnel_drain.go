package controller

import (
	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflared"
	"context"
	"fmt"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// drainConnector stops only the owned Deployment and waits for matching Pods to exit.
// Matching orphan or foreign Pods block remote deletion; they are never deleted here.
func (r *CloudflareTunnelReconciler) drainConnector(ctx context.Context, tunnel *cfg.CloudflareTunnel) (bool, error) {
	var deployment appsv1.Deployment
	err := r.lifecycleReader().Get(ctx, client.ObjectKey{Namespace: tunnel.Namespace, Name: cloudflared.DeploymentName(tunnel.Name)}, &deployment)
	if err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	if err == nil {
		if err := requireControllerOwner(&deployment, tunnel); err != nil {
			return false, err
		}
		if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0 {
			patch := client.MergeFromWithOptions(deployment.DeepCopy(), client.MergeFromWithOptimisticLock{})
			deployment.Spec.Replicas = ptr.To(int32(0))
			if err := r.Patch(ctx, &deployment, patch); err != nil {
				return false, fmt.Errorf("scale connector to zero: %w", err)
			}
			return false, nil
		}
		if deployment.Status.ObservedGeneration < deployment.Generation || deployment.Status.Replicas != 0 {
			return false, nil
		}
	}
	var pods corev1.PodList
	if err := r.lifecycleReader().List(ctx, &pods, client.InNamespace(tunnel.Namespace), client.MatchingLabels(cloudflared.Selector(tunnel.Name))); err != nil {
		return false, err
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
			return false, nil
		}
	}
	return true, nil
}

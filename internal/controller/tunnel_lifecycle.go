package controller

import (
	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflared"
	"cfgate.io/cfgate/internal/controller/status"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"time"
)

const fullLifecycleInterval = 30 * time.Minute

func (r *CloudflareTunnelReconciler) lifecycleReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func (r *CloudflareTunnelReconciler) canSkipTunnelLifecycle(ctx context.Context, tunnel *cfg.CloudflareTunnel) (bool, error) {
	if tunnel.Generation != tunnel.Status.ObservedGeneration || !hasUsableTunnelLifecycle(tunnel) || tunnel.Status.LastFullReconcileTime == nil || time.Since(tunnel.Status.LastFullReconcileTime.Time) >= fullLifecycleInterval || tunnel.Status.LifecycleDependencyHash == "" {
		return false, nil
	}
	fingerprint, err := r.lifecycleDependencyHash(ctx, r.Client, tunnel)
	return fingerprint != "" && fingerprint == tunnel.Status.LifecycleDependencyHash, err
}

// lifecycleDependencyHash records only dependency metadata, never credential data.
// An absent or terminating dependency prevents the configuration-only fast path.
func (r *CloudflareTunnelReconciler) lifecycleDependencyHash(ctx context.Context, reader client.Reader, tunnel *cfg.CloudflareTunnel) (string, error) {
	type dependency struct {
		Kind, Namespace, Name, Key, UID, ResourceVersion string
		Generation                                       int64
	}
	refs := []dependency{{Kind: "Secret", Namespace: tunnel.Spec.Cloudflare.SecretRef.Namespace, Name: tunnel.Spec.Cloudflare.SecretRef.Name, Key: tunnel.Spec.Cloudflare.SecretKeys.APIToken}, {Kind: "Secret", Namespace: tunnel.Namespace, Name: cloudflared.TokenSecretName(tunnel.Name), Key: cloudflared.TokenSecretKey}}
	if refs[0].Namespace == "" {
		refs[0].Namespace = tunnel.Namespace
	}
	if refs[0].Key == "" {
		refs[0].Key = "CLOUDFLARE_API_TOKEN"
	}
	if ref := tunnel.Spec.OriginDefaults.CAPoolSecretRef; ref != nil {
		key := ref.Key
		if key == "" {
			key = cloudflared.DefaultOriginCAPoolSecretKey
		}
		refs = append(refs, dependency{Kind: "Secret", Namespace: tunnel.Namespace, Name: ref.Name, Key: key})
	}
	for i := range refs {
		var secret corev1.Secret
		err := reader.Get(ctx, client.ObjectKey{Namespace: refs[i].Namespace, Name: refs[i].Name}, &secret)
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if !secret.DeletionTimestamp.IsZero() {
			return "", nil
		}
		refs[i].UID = string(secret.UID)
		refs[i].ResourceVersion = secret.ResourceVersion
	}
	var deployment appsv1.Deployment
	err := reader.Get(ctx, client.ObjectKey{Namespace: tunnel.Namespace, Name: cloudflared.DeploymentName(tunnel.Name)}, &deployment)
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !deployment.DeletionTimestamp.IsZero() {
		return "", nil
	}
	refs = append(refs, dependency{Kind: "Deployment", Namespace: deployment.Namespace, Name: deployment.Name, UID: string(deployment.UID), Generation: deployment.Generation})
	data, err := json.Marshal(refs)
	if err != nil {
		return "", fmt.Errorf("encode lifecycle dependencies: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func hasUsableTunnelLifecycle(tunnel *cfg.CloudflareTunnel) bool {
	return tunnel.Status.TunnelID != "" && meta.IsStatusConditionTrue(tunnel.Status.Conditions, status.ConditionTypeCredentialsValid) && meta.IsStatusConditionTrue(tunnel.Status.Conditions, status.ConditionTypeTunnelReady)
}

func (r *CloudflareTunnelReconciler) observeConnectorDeployment(tunnel *cfg.CloudflareTunnel, deployment *appsv1.Deployment) {
	tunnel.Status.Replicas = deployment.Status.Replicas
	tunnel.Status.ReadyReplicas = deployment.Status.ReadyReplicas
	desired := int32(2)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	if desired > 0 && deployment.Status.ObservedGeneration >= deployment.Generation && deployment.Status.ReadyReplicas >= desired && deployment.Status.AvailableReplicas >= desired {
		r.setCondition(tunnel, status.ConditionTypeCloudflaredDeployed, metav1.ConditionTrue, status.ReasonDeploymentReady, "All desired connector replicas are ready and available")
		return
	}
	r.setCondition(tunnel, status.ConditionTypeCloudflaredDeployed, metav1.ConditionFalse, status.ReasonDeploymentNotReady, fmt.Sprintf("Waiting for connector rollout: %d/%d replicas ready and %d available", deployment.Status.ReadyReplicas, desired, deployment.Status.AvailableReplicas))
}

func (r *CloudflareTunnelReconciler) updateTunnelReadiness(tunnel *cfg.CloudflareTunnel) {
	for _, conditionType := range []string{status.ConditionTypeCredentialsValid, status.ConditionTypeTunnelReady, status.ConditionTypeCloudflaredDeployed, status.ConditionTypeConfigurationSynced} {
		condition := meta.FindStatusCondition(tunnel.Status.Conditions, conditionType)
		if condition == nil || condition.Status != metav1.ConditionTrue {
			reason, message := status.ReasonReconciling, "Waiting for "+conditionType
			if condition != nil {
				reason = condition.Reason
				message = condition.Message
			}
			r.setCondition(tunnel, status.ConditionTypeReady, metav1.ConditionFalse, reason, message)
			return
		}
	}
	r.setCondition(tunnel, status.ConditionTypeReady, metav1.ConditionTrue, status.ReasonTunnelOperational, "Configuration synced and all desired connectors available; origin reachability is not checked")
}

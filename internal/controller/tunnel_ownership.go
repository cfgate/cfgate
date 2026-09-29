package controller

import (
	cfg "cfgate.io/cfgate/api/v1alpha1"
	"context"
	"crypto/sha256"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func tunnelClaimName(accountID, tunnelID string) string {
	sum := sha256.Sum256([]byte(accountID + "/" + tunnelID))
	return fmt.Sprintf("cfgate-tunnel-%x", sum[:16])
}

func (r *CloudflareTunnelReconciler) tunnelClaimKey(accountID, tunnelID string) (client.ObjectKey, error) {
	if r.InstallationNamespace == "" || accountID == "" || tunnelID == "" {
		return client.ObjectKey{}, fmt.Errorf("installation namespace, account and tunnel ID are required for ownership")
	}
	return client.ObjectKey{Namespace: r.InstallationNamespace, Name: tunnelClaimName(accountID, tunnelID)}, nil
}

func verifyClaimData(claim *corev1.ConfigMap, tunnel *cfg.CloudflareTunnel, accountID, tunnelID string) error {
	if tunnel.UID == "" || claim.Data["ownerUID"] != string(tunnel.UID) || claim.Data["accountID"] != accountID || claim.Data["tunnelID"] != tunnelID {
		return fmt.Errorf("remote tunnel %s has a foreign ownership claim", tunnelID)
	}
	return nil
}

func (r *CloudflareTunnelReconciler) claimTunnel(ctx context.Context, tunnel *cfg.CloudflareTunnel, accountID, tunnelID string, created bool) error {
	key, err := r.tunnelClaimKey(accountID, tunnelID)
	if err != nil {
		return err
	}
	if tunnel.UID == "" {
		return fmt.Errorf("resource UID is required for tunnel ownership")
	}
	var others cfg.CloudflareTunnelList
	if err := r.lifecycleReader().List(ctx, &others); err != nil {
		return err
	}
	for _, other := range others.Items {
		if other.UID == tunnel.UID {
			continue
		}
		account := other.Status.AccountID
		if account == "" {
			account = other.Spec.Cloudflare.AccountID
		}
		if account == accountID && other.Status.TunnelID == tunnelID {
			return fmt.Errorf("remote tunnel %s is already referenced by %s/%s", tunnelID, other.Namespace, other.Name)
		}
	}
	var existing corev1.ConfigMap
	err = r.lifecycleReader().Get(ctx, key, &existing)
	if err == nil {
		return verifyClaimData(&existing, tunnel, accountID, tunnelID)
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	if !created && tunnel.Annotations[adoptExistingAnnotation] != "true" {
		return fmt.Errorf("existing remote tunnel has no ownership claim; explicit %s=true migration is required", adoptExistingAnnotation)
	}
	claim := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}, Immutable: ptr.To(true), Data: map[string]string{"ownerUID": string(tunnel.UID), "ownerNamespace": tunnel.Namespace, "ownerName": tunnel.Name, "accountID": accountID, "tunnelID": tunnelID}}
	if err := r.Create(ctx, claim); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		if err := r.lifecycleReader().Get(ctx, key, &existing); err != nil {
			return err
		}
		return verifyClaimData(&existing, tunnel, accountID, tunnelID)
	}
	return nil
}

func (r *CloudflareTunnelReconciler) verifyTunnelClaim(ctx context.Context, tunnel *cfg.CloudflareTunnel, accountID string) error {
	key, err := r.tunnelClaimKey(accountID, tunnel.Status.TunnelID)
	if err != nil {
		return err
	}
	var claim corev1.ConfigMap
	if err := r.lifecycleReader().Get(ctx, key, &claim); err != nil {
		return fmt.Errorf("read remote tunnel ownership claim: %w", err)
	}
	return verifyClaimData(&claim, tunnel, accountID, tunnel.Status.TunnelID)
}

func (r *CloudflareTunnelReconciler) releaseTunnelClaim(ctx context.Context, tunnel *cfg.CloudflareTunnel, accountID string) error {
	key, err := r.tunnelClaimKey(accountID, tunnel.Status.TunnelID)
	if err != nil {
		return err
	}
	var claim corev1.ConfigMap
	if err := r.lifecycleReader().Get(ctx, key, &claim); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if err := verifyClaimData(&claim, tunnel, accountID, tunnel.Status.TunnelID); err != nil {
		return err
	}
	uid, version := claim.UID, claim.ResourceVersion
	return r.Delete(ctx, &claim, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}})
}

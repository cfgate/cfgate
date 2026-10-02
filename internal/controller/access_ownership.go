package controller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode/utf8"

	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func accessOwnerIdentity(ctx context.Context, reader client.Reader, installation string, obj client.Object, recorded string) (string, error) {
	if installation == "" || obj.GetUID() == "" {
		return "", fmt.Errorf("installation namespace and resource UID are required for Access ownership")
	}
	var ns corev1.Namespace
	if err := reader.Get(ctx, client.ObjectKey{Name: installation}, &ns); err != nil {
		return "", err
	}
	if ns.UID == "" {
		return "", fmt.Errorf("installation namespace has no UID")
	}
	identity := fmt.Sprintf("%x", sha256.Sum256([]byte(string(ns.UID)+"/"+string(obj.GetUID()))))[:28]
	if recorded != "" && recorded != identity {
		return "", fmt.Errorf("access owner belongs to another installation or resource")
	}
	return identity, nil
}

func ownedAccessName(name, identity string) string {
	if identity == "" {
		return name
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))[:12]
	if len(name) > 70 {
		name = name[:70]
		for !utf8.ValidString(name) {
			name = name[:len(name)-1]
		}
	}
	return name + "-" + digest + " [cfgate:" + identity + "]"
}

type ownedAccessClient struct {
	cloudflare.Client
	kube                   client.Client
	reader                 client.Reader
	installation, identity string
	owner                  client.Object
	legacy                 map[string]bool
}

func (c *ownedAccessClient) claimKey(account, kind, id string) client.ObjectKey {
	return client.ObjectKey{Namespace: c.installation, Name: fmt.Sprintf("cfgate-access-%x", sha256.Sum256([]byte(account+"/"+kind+"/"+id)))[:46]}
}
func (c *ownedAccessClient) claim(ctx context.Context, account, kind, id string, evidence bool) error {
	if id == "" {
		return fmt.Errorf("missing remote Access identity")
	}
	key := c.claimKey(account, kind, id)
	var existing corev1.ConfigMap
	err := c.reader.Get(ctx, key, &existing)
	if err == nil {
		if existing.Data["ownerID"] != c.identity || existing.Data["accountID"] != account || existing.Data["kind"] != kind || existing.Data["remoteID"] != id {
			return fmt.Errorf("foreign Access ownership claim for %s %s", kind, id)
		}
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	if !evidence && (c.owner.GetAnnotations()[adoptExistingAnnotation] != "true" || !c.legacy[kind+"/"+id]) {
		return fmt.Errorf("unclaimed Access %s %s requires explicit %s=true adoption", kind, id, adoptExistingAnnotation)
	}
	obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}, Immutable: ptr.To(true), Data: map[string]string{"ownerID": c.identity, "accountID": account, "kind": kind, "remoteID": id, "ownerUID": string(c.owner.GetUID()), "ownerNamespace": c.owner.GetNamespace(), "ownerName": c.owner.GetName()}}
	if err := c.kube.Create(ctx, obj); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return c.claim(ctx, account, kind, id, false)
		}
		return err
	}
	return nil
}
func (c *ownedAccessClient) verify(ctx context.Context, account, kind, id string) error {
	var existing corev1.ConfigMap
	if err := c.reader.Get(ctx, c.claimKey(account, kind, id), &existing); err == nil {
		if err := c.claim(ctx, account, kind, id, false); err != nil {
			return err
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	owned := false
	switch kind {
	case "application":
		obj, err := c.GetAccessApplication(ctx, account, id)
		if err != nil {
			return err
		}
		if obj == nil {
			return nil
		}
		if obj.ID != id {
			return fmt.Errorf("remote Access %s response identity mismatch", kind)
		}
		for _, tag := range obj.Tags {
			if tag == "cfgate:"+c.identity {
				owned = true
			}
		}
	case "policy":
		obj, err := c.GetAccessPolicy(ctx, account, id)
		if err != nil {
			return err
		}
		if obj == nil {
			return nil
		}
		if obj.ID != id {
			return fmt.Errorf("remote Access %s response identity mismatch", kind)
		}
		owned = strings.HasSuffix(obj.Name, " [cfgate:"+c.identity+"]")
	case "token":
		obj, err := c.GetServiceToken(ctx, account, id)
		if err != nil {
			return err
		}
		if obj == nil {
			return nil
		}
		if obj.ID != id {
			return fmt.Errorf("remote Access %s response identity mismatch", kind)
		}
		owned = strings.HasSuffix(obj.Name, " [cfgate:"+c.identity+"]")
	default:
		return fmt.Errorf("unknown Access resource kind %q", kind)
	}
	return c.claim(ctx, account, kind, id, owned)
}
func (c *ownedAccessClient) release(ctx context.Context, account, kind, id string) error {
	var claim corev1.ConfigMap
	if err := c.reader.Get(ctx, c.claimKey(account, kind, id), &claim); err != nil {
		return client.IgnoreNotFound(err)
	}
	if claim.Data["ownerID"] != c.identity || claim.Data["accountID"] != account || claim.Data["kind"] != kind || claim.Data["remoteID"] != id {
		return fmt.Errorf("refusing foreign Access claim deletion")
	}
	uid, rv := claim.UID, claim.ResourceVersion
	return c.kube.Delete(ctx, &claim, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}})
}
func (c *ownedAccessClient) CreateAccessApplication(ctx context.Context, a string, p cloudflare.ApplicationParams) (*cloudflare.AccessApplication, error) {
	obj, err := c.Client.CreateAccessApplication(ctx, a, p)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, fmt.Errorf("empty application creation response")
	}
	return obj, c.claim(ctx, a, "application", obj.ID, true)
}
func (c *ownedAccessClient) UpdateAccessApplication(ctx context.Context, a, id string, p cloudflare.ApplicationParams) (*cloudflare.AccessApplication, error) {
	if err := c.verify(ctx, a, "application", id); err != nil {
		return nil, err
	}
	return c.Client.UpdateAccessApplication(ctx, a, id, p)
}
func (c *ownedAccessClient) DeleteAccessApplication(ctx context.Context, a, id string) error {
	if err := c.verify(ctx, a, "application", id); err != nil {
		return err
	}
	if err := c.Client.DeleteAccessApplication(ctx, a, id); err != nil {
		return err
	}
	return c.release(ctx, a, "application", id)
}
func (c *ownedAccessClient) CreateAccessPolicy(ctx context.Context, a string, p cloudflare.PolicyParams) (*cloudflare.AccessPolicy, error) {
	obj, err := c.Client.CreateAccessPolicy(ctx, a, p)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, fmt.Errorf("empty policy creation response")
	}
	return obj, c.claim(ctx, a, "policy", obj.ID, true)
}
func (c *ownedAccessClient) UpdateAccessPolicy(ctx context.Context, a, id string, p cloudflare.PolicyParams) (*cloudflare.AccessPolicy, error) {
	if err := c.verify(ctx, a, "policy", id); err != nil {
		return nil, err
	}
	return c.Client.UpdateAccessPolicy(ctx, a, id, p)
}
func (c *ownedAccessClient) DeleteAccessPolicy(ctx context.Context, a, id string) error {
	if err := c.verify(ctx, a, "policy", id); err != nil {
		return err
	}
	if err := c.Client.DeleteAccessPolicy(ctx, a, id); err != nil {
		return err
	}
	return c.release(ctx, a, "policy", id)
}
func (c *ownedAccessClient) CreateServiceToken(ctx context.Context, a string, p cloudflare.ServiceTokenParams) (*cloudflare.ServiceTokenWithSecret, error) {
	obj, err := c.Client.CreateServiceToken(ctx, a, p)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, fmt.Errorf("empty token creation response")
	}
	return obj, c.claim(ctx, a, "token", obj.ID, true)
}
func (c *ownedAccessClient) RotateServiceToken(ctx context.Context, a, id string) (*cloudflare.ServiceTokenWithSecret, error) {
	if err := c.verify(ctx, a, "token", id); err != nil {
		return nil, err
	}
	obj, err := c.Client.RotateServiceToken(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, fmt.Errorf("empty token rotation response")
	}
	return obj, c.claim(ctx, a, "token", obj.ID, true)
}
func (c *ownedAccessClient) DeleteServiceToken(ctx context.Context, a, id string) error {
	if err := c.verify(ctx, a, "token", id); err != nil {
		return err
	}
	if err := c.Client.DeleteServiceToken(ctx, a, id); err != nil {
		return err
	}
	return c.release(ctx, a, "token", id)
}

func (r *CloudflareAccessApplicationReconciler) prepareOwnedApplications(ctx context.Context, app *cfg.CloudflareAccessApplication, creds *accessApplicationCredentials) error {
	identity, err := accessOwnerIdentity(ctx, accessReader(r.APIReader, r.Client), r.InstallationNamespace, app, app.Status.OwnerID)
	if err != nil {
		return err
	}
	if app.Status.AccountID != "" && app.Status.AccountID != creds.AccountID {
		return fmt.Errorf("access account change requires cleanup or explicit orphaning first")
	}
	app.Status.OwnerID = identity
	// Persist cleanup authority before any remote operation.
	app.Status.AccountID = creds.AccountID
	app.Status.CredentialSecretRef = creds.CredentialSecretRef
	app.Status.CredentialSecretKeys = creds.CredentialSecretKeys
	if err := r.updateApplicationStatus(ctx, app); err != nil {
		return err
	}
	owned := &ownedAccessClient{Client: creds.Service.Client(), kube: r.Client, reader: accessReader(r.APIReader, r.Client), installation: r.InstallationNamespace, identity: identity, owner: app, legacy: map[string]bool{}}
	inventory := append(append([]cfg.AccessApplicationObserved(nil), app.Status.Applications...), app.Status.PendingApplications...)
	for _, item := range inventory {
		owned.legacy["application/"+item.ID] = true
	}
	retained := inventory[:0]
	retainedIDs := map[string]bool{}
	for _, item := range inventory {
		remote, err := owned.GetAccessApplication(ctx, creds.AccountID, item.ID)
		if err != nil {
			return err
		}
		if remote == nil {
			if err := owned.release(ctx, creds.AccountID, "application", item.ID); err != nil {
				return err
			}
			continue
		}
		if !retainedIDs[item.ID] {
			retained = append(retained, item)
			retainedIDs[item.ID] = true
		}
	}
	inventory = retained
	remote, err := owned.ListAccessApplications(ctx, creds.AccountID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, item := range inventory {
		seen[item.ID] = true
	}
	for _, item := range remote {
		eligible := false
		for _, tag := range item.Tags {
			if tag == "cfgate:"+identity || (app.Annotations[adoptExistingAnnotation] == "true" && tag == legacyAccessApplicationOwnerTag(app)) {
				eligible = true
			}
		}
		if !eligible {
			continue
		}
		owned.legacy["application/"+item.ID] = true
		if err := owned.verify(ctx, creds.AccountID, "application", item.ID); err != nil {
			return err
		}
		if !seen[item.ID] {
			inventory = append(inventory, cfg.AccessApplicationObserved{ID: item.ID, AUD: item.AUD, Domain: item.Domain})
			seen[item.ID] = true
		}
	}
	domains := map[string]string{}
	for _, item := range inventory {
		if previous := domains[item.Domain]; previous != "" && previous != item.ID {
			return fmt.Errorf("ambiguous owned applications for domain %q", item.Domain)
		}
		domains[item.Domain] = item.ID
	}
	for _, item := range inventory {
		if err := owned.verify(ctx, creds.AccountID, "application", item.ID); err != nil {
			return err
		}
	}

	creds.Recovered = inventory

	creds.Service = cloudflare.NewAccessService(owned, logr.FromContextOrDiscard(ctx))
	return nil
}

func (r *CloudflareAccessPolicyReconciler) prepareOwnedPolicy(ctx context.Context, policy *cfg.CloudflareAccessPolicy, creds *accessPolicyCredentials) error {
	identity, err := accessOwnerIdentity(ctx, accessReader(r.APIReader, r.Client), r.InstallationNamespace, policy, policy.Status.OwnerID)
	if err != nil {
		return err
	}
	if policy.Status.AccountID != "" && policy.Status.AccountID != creds.AccountID {
		return fmt.Errorf("access account change requires cleanup or explicit orphaning first")
	}
	policy.Status.OwnerID = identity
	policy.Status.AccountID = creds.AccountID
	policy.Status.CredentialSecretRef = creds.CredentialSecretRef
	policy.Status.CredentialSecretKeys = creds.CredentialSecretKeys
	if err := r.updateStatus(ctx, policy); err != nil {
		return err
	}
	owned := &ownedAccessClient{Client: creds.Service.Client(), kube: r.Client, reader: accessReader(r.APIReader, r.Client), installation: r.InstallationNamespace, identity: identity, owner: policy, legacy: map[string]bool{}}
	owned.legacy["policy/"+policy.Status.PolicyID] = true
	for _, id := range policy.Status.ServiceTokenIDs {
		owned.legacy["token/"+id] = true
	}
	if policy.Status.PolicyID != "" {
		remote, err := owned.GetAccessPolicy(ctx, creds.AccountID, policy.Status.PolicyID)
		if err != nil {
			return err
		}
		if remote == nil {
			if err := owned.release(ctx, creds.AccountID, "policy", policy.Status.PolicyID); err != nil {
				return err
			}
			policy.Status.PolicyID = ""
		}
	}
	for name, id := range policy.Status.ServiceTokenIDs {
		remote, err := owned.GetServiceToken(ctx, creds.AccountID, id)
		if err != nil {
			return err
		}
		if remote == nil {
			if err := owned.release(ctx, creds.AccountID, "token", id); err != nil {
				return err
			}
			delete(policy.Status.ServiceTokenIDs, name)
		}
	}
	policies, err := owned.ListAccessPolicies(ctx, creds.AccountID)
	if err != nil {
		return err
	}
	for _, item := range policies {
		if !strings.HasSuffix(item.Name, " [cfgate:"+identity+"]") && (policy.Annotations[adoptExistingAnnotation] != "true" || item.Name != policy.Spec.Name) {
			continue
		}
		if policy.Status.PolicyID != "" && policy.Status.PolicyID != item.ID {
			return fmt.Errorf("ambiguous owned policy inventory")
		}
		owned.legacy["policy/"+item.ID] = true
		policy.Status.PolicyID = item.ID
	}
	if policy.Status.PolicyID != "" {
		if err := owned.verify(ctx, creds.AccountID, "policy", policy.Status.PolicyID); err != nil {
			return err
		}
	}
	tokens, err := owned.ListServiceTokens(ctx, creds.AccountID)
	if err != nil {
		return err
	}
	if policy.Status.ServiceTokenIDs == nil {
		policy.Status.ServiceTokenIDs = map[string]string{}
	}
	for _, item := range tokens {
		name := ""
		suffix := " [cfgate:" + identity + "]"
		if strings.HasSuffix(item.Name, suffix) {
			name = "recovered/" + item.ID
		}
		for _, desired := range policy.Spec.ServiceTokens {
			if item.Name == ownedAccessName(desired.Name, identity) || (policy.Annotations[adoptExistingAnnotation] == "true" && item.Name == desired.Name) {
				name = desired.Name
			}
		}
		if name == "" {
			continue
		}
		if old := policy.Status.ServiceTokenIDs[name]; old != "" && old != item.ID {
			return fmt.Errorf("ambiguous owned token inventory")
		}
		owned.legacy["token/"+item.ID] = true
		policy.Status.ServiceTokenIDs[name] = item.ID
	}
	for _, id := range policy.Status.ServiceTokenIDs {
		if err := owned.verify(ctx, creds.AccountID, "token", id); err != nil {
			return err
		}
	}
	if err := r.updateStatus(ctx, policy); err != nil {
		return err
	}
	creds.Service = cloudflare.NewAccessService(owned, logr.FromContextOrDiscard(ctx))
	return nil
}

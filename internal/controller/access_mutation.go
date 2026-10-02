package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type accessMutationContextKey struct{}
type accessMutationClient struct {
	cloudflare.Client
	before func(context.Context) error
}

func (c *accessMutationClient) CreateAccessApplication(ctx context.Context, account string, params cloudflare.ApplicationParams) (*cloudflare.AccessApplication, error) {
	if err := c.before(ctx); err != nil {
		return nil, err
	}
	return c.Client.CreateAccessApplication(ctx, account, params)
}
func (c *accessMutationClient) UpdateAccessApplication(ctx context.Context, account, id string, params cloudflare.ApplicationParams) (*cloudflare.AccessApplication, error) {
	if err := c.before(ctx); err != nil {
		return nil, err
	}
	return c.Client.UpdateAccessApplication(ctx, account, id, params)
}
func (c *accessMutationClient) DeleteAccessApplication(ctx context.Context, account, id string) error {
	if err := c.before(ctx); err != nil {
		return err
	}
	return c.Client.DeleteAccessApplication(ctx, account, id)
}
func (c *accessMutationClient) UpdateAccessPolicy(ctx context.Context, account, id string, params cloudflare.PolicyParams) (*cloudflare.AccessPolicy, error) {
	if err := c.before(ctx); err != nil {
		return nil, err
	}
	return c.Client.UpdateAccessPolicy(ctx, account, id, params)
}
func (c *accessMutationClient) DeleteAccessPolicy(ctx context.Context, account, id string) error {
	if err := c.before(ctx); err != nil {
		return err
	}
	return c.Client.DeleteAccessPolicy(ctx, account, id)
}
func (c *accessMutationClient) DeleteServiceToken(ctx context.Context, account, id string) error {
	if err := c.before(ctx); err != nil {
		return err
	}
	return c.Client.DeleteServiceToken(ctx, account, id)
}
func (c *accessMutationClient) RotateServiceToken(ctx context.Context, account, id string) (*cloudflare.ServiceTokenWithSecret, error) {
	if err := c.before(ctx); err != nil {
		return nil, err
	}
	return c.Client.RotateServiceToken(ctx, account, id)
}
func guardAccessMutations(ctx context.Context, cfClient cloudflare.Client) cloudflare.Client {
	if before, ok := ctx.Value(accessMutationContextKey{}).(func(context.Context) error); ok {
		return &accessMutationClient{Client: cfClient, before: before}
	}
	return cfClient
}

func accessReader(reader client.Reader, fallback client.Client) client.Reader {
	if reader != nil {
		return reader
	}
	return fallback
}

// verifyAccessWithdrawal is read-only. A pending result releases application locks
// at the reconcile boundary so the ordinary tunnel worker can publish denials.
func (r *CloudflareTunnelReconciler) verifyAccessWithdrawal(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	inventory, err := accessRouteInventory(ctx, r.lifecycleReader())
	if err != nil {
		return err
	}
	wanted := map[string]bool{}
	for _, key := range keys {
		wanted[key] = true
	}
	var tunnels cfg.CloudflareTunnelList
	if err := r.lifecycleReader().List(ctx, &tunnels); err != nil {
		return err
	}
	for i := range tunnels.Items {
		tunnel := &tunnels.Items[i]
		deps := append([]cfg.TunnelAccessDependency(nil), tunnel.Status.AccessDependencies...)
		for key := range inventory[client.ObjectKeyFromObject(tunnel)] {
			if !wanted[key] {
				continue
			}
			parts := strings.SplitN(key, "/", 2)
			var app cfg.CloudflareAccessApplication
			if err := r.lifecycleReader().Get(ctx, types.NamespacedName{Namespace: parts[0], Name: parts[1]}, &app); err != nil {
				return err
			}
			var hosts []string
			for _, observed := range app.Status.Applications {
				hosts = append(hosts, strings.SplitN(observed.Domain, "/", 2)[0])
			}
			if tunnel.Status.TunnelID != "" && len(hosts) > 0 {
				deps = append(deps, cfg.TunnelAccessDependency{Namespace: parts[0], Name: parts[1], UID: string(app.UID), TunnelID: tunnel.Status.TunnelID, Hostnames: hosts})
			}
		}
		for _, dep := range deps {
			if !wanted[dep.Namespace+"/"+dep.Name] {
				continue
			}
			cfClient, err := r.getCloudflareClient(ctx, tunnel)
			if err != nil {
				return err
			}
			account, err := r.resolveAccountID(ctx, cfClient, tunnel)
			if err != nil {
				return err
			}
			if dep.AccountID != "" && account != dep.AccountID {
				return fmt.Errorf("cannot verify previous Access dependency account")
			}
			claimed := tunnel.DeepCopy()
			claimed.Status.TunnelID = dep.TunnelID
			if err := r.verifyTunnelClaim(ctx, claimed, account); err != nil {
				return err
			}
			remoteTunnel, err := cfClient.GetTunnel(ctx, account, dep.TunnelID)
			if err != nil {
				return err
			}
			// A deleted tunnel can retain its old configuration without forwarding.
			if remoteTunnel == nil {
				continue
			}
			if dep.Pending {
				return fmt.Errorf("required protection change waits for tunnel %s/%s to confirm its pending configuration", tunnel.Namespace, tunnel.Name)
			}
			remote, err := cfClient.GetTunnelConfiguration(ctx, account, dep.TunnelID)
			if err != nil {
				return err
			}
			if remote == nil {
				continue
			}
			for _, rule := range remote.Ingress {
				if strings.HasPrefix(rule.Service, "http_status:") {
					continue
				}
				for _, host := range dep.Hostnames {
					if rule.Hostname == "" || accessHostnameOverlaps(rule.Hostname, host) {
						return fmt.Errorf("required protection change waits for tunnel %s/%s to withdraw forwarding for %s", tunnel.Namespace, tunnel.Name, host)
					}
				}
			}
		}
	}
	return nil
}

func (r *CloudflareAccessApplicationReconciler) beginApplicationMutation(ctx context.Context, app *cfg.CloudflareAccessApplication) (context.Context, func(), error) {
	if r.AccessLocks == nil {
		return ctx, func() {}, nil
	}
	keys := []string{client.ObjectKeyFromObject(app).String()}
	release, err := r.AccessLocks.acquire(ctx, keys)
	if err != nil {
		return ctx, nil, err
	}
	if err := accessReader(r.APIReader, r.Client).Get(ctx, client.ObjectKeyFromObject(app), app); err != nil {
		release()
		return ctx, nil, err
	}
	verifier := &CloudflareTunnelReconciler{Client: r.Client, APIReader: r.APIReader, CFClient: r.CFClient, CredentialCache: r.CredentialCache, ClientSettings: r.ClientSettings, InstallationNamespace: r.InstallationNamespace}
	before := func(ctx context.Context) error { return verifier.verifyAccessWithdrawal(ctx, keys) }
	return context.WithValue(ctx, accessMutationContextKey{}, before), release, nil
}

func (r *CloudflareAccessPolicyReconciler) beginPolicyMutation(ctx context.Context, policy *cfg.CloudflareAccessPolicy) (context.Context, func(), error) {
	if r.AccessLocks == nil {
		return ctx, func() {}, nil
	}
	reader := accessReader(r.APIReader, r.Client)
	var apps cfg.CloudflareAccessApplicationList
	if err := reader.List(ctx, &apps); err != nil {
		return ctx, nil, err
	}
	var keys []string
	for _, app := range apps.Items {
		for _, ref := range app.Spec.PolicyRefs {
			ns := ref.Namespace
			if ns == "" {
				ns = app.Namespace
			}
			if ns == policy.Namespace && ref.Name == policy.Name {
				if err := requireReferenceGrant(ctx, reader, app.Namespace, "cfgate.io", "CloudflareAccessApplication", policy.Namespace, "cfgate.io", "CloudflareAccessPolicy", policy.Name); err != nil {
					if errors.Is(err, errReferenceNotPermitted) {
						continue
					}
					return ctx, nil, err
				}
				keys = append(keys, client.ObjectKeyFromObject(&app).String())
				break
			}
		}
	}
	// Local policyRefs may already be removed while old remote links still protect published routes.
	// Retained publication dependencies keep those application locks until confirmed withdrawal.
	var tunnels cfg.CloudflareTunnelList
	if err := reader.List(ctx, &tunnels); err != nil {
		return ctx, nil, err
	}
	for _, tunnel := range tunnels.Items {
		for _, dep := range tunnel.Status.AccessDependencies {
			for _, ref := range dep.Policies {
				if ref.Namespace == policy.Namespace && ref.Name == policy.Name {
					keys = append(keys, dep.Namespace+"/"+dep.Name)
					break
				}
			}
		}
	}
	release, err := r.AccessLocks.acquire(ctx, keys)
	if err != nil {
		return ctx, nil, err
	}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(policy), policy); err != nil {
		release()
		return ctx, nil, err
	}
	verifier := &CloudflareTunnelReconciler{Client: r.Client, APIReader: r.APIReader, CFClient: r.CFClient, CredentialCache: r.CredentialCache, ClientSettings: r.ClientSettings, InstallationNamespace: r.InstallationNamespace}
	before := func(ctx context.Context) error { return verifier.verifyAccessWithdrawal(ctx, keys) }
	return context.WithValue(ctx, accessMutationContextKey{}, before), release, nil
}

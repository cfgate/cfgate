package controller

import (
	"cfgate.io/cfgate/internal/cloudflare"
	"context"
	"fmt"
)

var _ cloudflare.AccessClient = (*ownedAccessClient)(nil)
var _ cloudflare.AccessClient = (*accessMutationClient)(nil)

func (c *ownedAccessClient) GetAccessApplication(ctx context.Context, account, id string) (*cloudflare.AccessApplication, error) {
	return c.Client.GetAccessApplication(ctx, account, id)
}
func (c *ownedAccessClient) ListAccessApplications(ctx context.Context, account string) ([]cloudflare.AccessApplication, error) {
	return c.Client.ListAccessApplications(ctx, account)
}

func (c *ownedAccessClient) GetAccessPolicy(ctx context.Context, account, id string) (*cloudflare.AccessPolicy, error) {
	return c.Client.GetAccessPolicy(ctx, account, id)
}
func (c *ownedAccessClient) ListAccessPolicies(ctx context.Context, account string) ([]cloudflare.AccessPolicy, error) {
	return c.Client.ListAccessPolicies(ctx, account)
}

func (c *ownedAccessClient) GetServiceToken(ctx context.Context, account, id string) (*cloudflare.ServiceToken, error) {
	return c.Client.GetServiceToken(ctx, account, id)
}
func (c *ownedAccessClient) ListServiceTokens(ctx context.Context, account string) ([]cloudflare.ServiceToken, error) {
	return c.Client.ListServiceTokens(ctx, account)
}
func (c *ownedAccessClient) CreateAccessTag(ctx context.Context, account, name string) (*cloudflare.AccessTag, error) {
	return c.Client.CreateAccessTag(ctx, account, name)
}
func (c *ownedAccessClient) ListAccessTags(ctx context.Context, account string) ([]cloudflare.AccessTag, error) {
	return c.Client.ListAccessTags(ctx, account)
}
func (c *ownedAccessClient) UpdateServiceToken(ctx context.Context, account, id string, p cloudflare.ServiceTokenParams) (*cloudflare.ServiceToken, error) {
	if err := c.verify(ctx, account, "token", id); err != nil {
		return nil, err
	}
	return c.Client.UpdateServiceToken(ctx, account, id, p)
}
func (c *ownedAccessClient) RefreshServiceToken(ctx context.Context, account, id string) (*cloudflare.ServiceToken, error) {
	if err := c.verify(ctx, account, "token", id); err != nil {
		return nil, err
	}
	return c.Client.RefreshServiceToken(ctx, account, id)
}

func (c *accessMutationClient) GetAccessApplication(ctx context.Context, account, id string) (*cloudflare.AccessApplication, error) {
	return c.Client.GetAccessApplication(ctx, account, id)
}
func (c *accessMutationClient) ListAccessApplications(ctx context.Context, account string) ([]cloudflare.AccessApplication, error) {
	return c.Client.ListAccessApplications(ctx, account)
}

func (c *accessMutationClient) GetAccessPolicy(ctx context.Context, account, id string) (*cloudflare.AccessPolicy, error) {
	return c.Client.GetAccessPolicy(ctx, account, id)
}
func (c *accessMutationClient) ListAccessPolicies(ctx context.Context, account string) ([]cloudflare.AccessPolicy, error) {
	return c.Client.ListAccessPolicies(ctx, account)
}

func (c *accessMutationClient) GetServiceToken(ctx context.Context, account, id string) (*cloudflare.ServiceToken, error) {
	return c.Client.GetServiceToken(ctx, account, id)
}
func (c *accessMutationClient) ListServiceTokens(ctx context.Context, account string) ([]cloudflare.ServiceToken, error) {
	return c.Client.ListServiceTokens(ctx, account)
}
func (c *accessMutationClient) CreateAccessTag(ctx context.Context, account, name string) (*cloudflare.AccessTag, error) {
	return c.Client.CreateAccessTag(ctx, account, name)
}
func (c *accessMutationClient) ListAccessTags(ctx context.Context, account string) ([]cloudflare.AccessTag, error) {
	return c.Client.ListAccessTags(ctx, account)
}
func (c *accessMutationClient) UpdateServiceToken(ctx context.Context, account, id string, p cloudflare.ServiceTokenParams) (*cloudflare.ServiceToken, error) {
	if err := c.before(ctx); err != nil {
		return nil, err
	}
	return c.Client.UpdateServiceToken(ctx, account, id, p)
}
func (c *accessMutationClient) RefreshServiceToken(ctx context.Context, account, id string) (*cloudflare.ServiceToken, error) {
	if err := c.before(ctx); err != nil {
		return nil, err
	}
	return c.Client.RefreshServiceToken(ctx, account, id)
}
func (c *accessMutationClient) CreateAccessPolicy(ctx context.Context, account string, p cloudflare.PolicyParams) (*cloudflare.AccessPolicy, error) {
	return c.Client.CreateAccessPolicy(ctx, account, p)
}
func (c *accessMutationClient) CreateServiceToken(ctx context.Context, account string, p cloudflare.ServiceTokenParams) (*cloudflare.ServiceTokenWithSecret, error) {
	return c.Client.CreateServiceToken(ctx, account, p)
}

func (c *ownedAccessClient) DeleteAccessTag(ctx context.Context, account, name string) error {
	if name != "cfgate:"+c.identity {
		return fmt.Errorf("refusing to delete foreign Access tag %q", name)
	}
	return c.Client.DeleteAccessTag(ctx, account, name)
}
func (c *accessMutationClient) DeleteAccessTag(ctx context.Context, account, name string) error {
	if err := c.before(ctx); err != nil {
		return err
	}
	return c.Client.DeleteAccessTag(ctx, account, name)
}

func (c *ownedAccessClient) ExtendServiceTokenExpiration(ctx context.Context, account, id string, expected cloudflare.ServiceToken) (*cloudflare.ServiceToken, error) {
	if err := c.verify(ctx, account, "token", id); err != nil {
		return nil, err
	}
	return c.Client.ExtendServiceTokenExpiration(ctx, account, id, expected)
}
func (c *accessMutationClient) ExtendServiceTokenExpiration(ctx context.Context, account, id string, expected cloudflare.ServiceToken) (*cloudflare.ServiceToken, error) {
	// beginPolicyMutation still holds the affected application locks. This narrow
	// capability preserves authentication material and never changes policy rules.
	return c.Client.ExtendServiceTokenExpiration(ctx, account, id, expected)
}

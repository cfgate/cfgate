// Package e2e contains end-to-end tests for cfgate.
package e2e_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	cloudflare "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/dns"
	"github.com/cloudflare/cloudflare-go/v6/option"
	"github.com/cloudflare/cloudflare-go/v6/zero_trust"
	"github.com/cloudflare/cloudflare-go/v6/zones"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"
)

// ============================================================
// Timeouts
// ============================================================

const (
	// DefaultTimeout is the default timeout for waiting operations.
	DefaultTimeout = 5 * time.Minute

	// DefaultInterval is the default polling interval.
	DefaultInterval = 5 * time.Second

	// ShortTimeout is for quick operations.
	ShortTimeout = 30 * time.Second

	// LongTimeout is for operations that may take longer.
	LongTimeout = 10 * time.Minute
)

// ============================================================
// Utility Functions
// ============================================================

// ptrTo returns a pointer to the given value.
func ptrTo[T any](v T) *T {
	return &v
}

// mustParseQuantity parses a quantity string and panics on error.
func mustParseQuantity(s string) resource.Quantity {
	return resource.MustParse(s)
}

// deleteTunnelInCloudflare deletes a tunnel by ID via the Cloudflare API.
// Clears active connections first (required before deletion).
func deleteTunnelInCloudflare(ctx context.Context, cfClient *cloudflare.Client, accountID, tunnelID string) error {
	// Clear connections first (required by CF API before deletion).
	_, _ = cfClient.ZeroTrust.Tunnels.Cloudflared.Connections.Delete(ctx, tunnelID, zero_trust.TunnelCloudflaredConnectionDeleteParams{
		AccountID: cloudflare.F(accountID),
	})

	_, err := cfClient.ZeroTrust.Tunnels.Cloudflared.Delete(ctx, tunnelID, zero_trust.TunnelCloudflaredDeleteParams{
		AccountID: cloudflare.F(accountID),
	})
	if err != nil {
		return fmt.Errorf("failed to delete tunnel %s: %w", tunnelID, err)
	}
	return nil
}

// listTunnelsByPrefixFromCloudflare lists all non-deleted tunnels matching a name prefix.
func listTunnelsByPrefixFromCloudflare(ctx context.Context, cfClient *cloudflare.Client, accountID, prefix string) ([]CloudflareTunnelInfo, error) {
	iter := cfClient.ZeroTrust.Tunnels.Cloudflared.ListAutoPaging(ctx, zero_trust.TunnelCloudflaredListParams{
		AccountID: cloudflare.F(accountID),
	})

	var tunnels []CloudflareTunnelInfo
	for iter.Next() {
		t := iter.Current()
		if strings.HasPrefix(t.Name, prefix) && t.DeletedAt.IsZero() {
			tunnels = append(tunnels, CloudflareTunnelInfo{
				ID:        t.ID,
				Name:      t.Name,
				Status:    string(t.Status),
				DeletedAt: t.DeletedAt,
			})
		}
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("failed to list tunnels: %w", err)
	}
	return tunnels, nil
}

// createCloudflareAccessPolicyWithApplicationFields creates an AccessPolicy with custom application fields.
func createCloudflareAccessPolicyWithApplicationFields(
	ctx context.Context,
	k8sClient client.Client,
	name, namespace, targetRouteName, hostname string,
	appConfig cfgatev1alpha1.AccessApplication,
) *cfgatev1alpha1.CloudflareAccessPolicy {
	appConfig.Name = name
	appConfig.Domain = hostname

	policy := &cfgatev1alpha1.CloudflareAccessPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
			TargetRef: &cfgatev1alpha1.PolicyTargetReference{
				Group: "gateway.networking.k8s.io",
				Kind:  "HTTPRoute",
				Name:  targetRouteName,
			},
			CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
				Name:      "cloudflare-credentials",
				AccountID: testEnv.CloudflareAccountID,
			},
			Application: appConfig,
			Policies: []cfgatev1alpha1.AccessPolicyRule{
				{
					Name:     "allow-all",
					Decision: "allow",
					Include: []cfgatev1alpha1.AccessRule{
						{
							Everyone: ptrTo(true),
						},
					},
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, policy)).To(Succeed())
	return policy
}

// Note: testID is defined in e2e_suite_test.go

// ============================================================
// Cloudflare Client
// ============================================================

// getCloudflareClient creates a Cloudflare client for E2E test verification.
func getCloudflareClient() *cloudflare.Client {
	return cloudflare.NewClient(option.WithAPIToken(testEnv.CloudflareAPIToken))
}

// ============================================================
// Tunnel Info Types and Helpers
// ============================================================

// CloudflareTunnelInfo holds information about a Cloudflare tunnel.
type CloudflareTunnelInfo struct {
	ID        string
	Name      string
	Status    string
	DeletedAt time.Time
}

// getTunnelFromCloudflare fetches a tunnel directly from the Cloudflare API.
func getTunnelFromCloudflare(ctx context.Context, cfClient *cloudflare.Client, accountID, tunnelName string) (*CloudflareTunnelInfo, error) {
	tunnels, err := cfClient.ZeroTrust.Tunnels.Cloudflared.List(ctx, zero_trust.TunnelCloudflaredListParams{
		AccountID: cloudflare.F(accountID),
		Name:      cloudflare.F(tunnelName),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list tunnels: %w", err)
	}

	for _, tunnel := range tunnels.Result {
		if tunnel.Name == tunnelName && string(tunnel.Status) != "deleted" {
			return &CloudflareTunnelInfo{
				ID:     tunnel.ID,
				Name:   tunnel.Name,
				Status: string(tunnel.Status),
			}, nil
		}
	}

	return nil, nil // Not found.
}

// getTunnelByIDFromCloudflare fetches a tunnel by ID from the Cloudflare API.
func getTunnelByIDFromCloudflare(ctx context.Context, cfClient *cloudflare.Client, accountID, tunnelID string) (*CloudflareTunnelInfo, error) {
	tunnel, err := cfClient.ZeroTrust.Tunnels.Cloudflared.Get(ctx, tunnelID, zero_trust.TunnelCloudflaredGetParams{
		AccountID: cloudflare.F(accountID),
	})
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "404") {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get tunnel: %w", err)
	}

	if string(tunnel.Status) == "deleted" || !tunnel.DeletedAt.IsZero() {
		return nil, nil
	}

	return &CloudflareTunnelInfo{
		ID:        tunnel.ID,
		Name:      tunnel.Name,
		Status:    string(tunnel.Status),
		DeletedAt: tunnel.DeletedAt,
	}, nil
}

// createTunnelInCloudflare creates a tunnel directly via the Cloudflare API.
func createTunnelInCloudflare(ctx context.Context, cfClient *cloudflare.Client, accountID, tunnelName string) (*CloudflareTunnelInfo, error) {
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return nil, fmt.Errorf("failed to generate tunnel secret: %w", err)
	}
	tunnelSecret := base64.StdEncoding.EncodeToString(secretBytes)

	tunnel, err := cfClient.ZeroTrust.Tunnels.Cloudflared.New(ctx, zero_trust.TunnelCloudflaredNewParams{
		AccountID:    cloudflare.F(accountID),
		Name:         cloudflare.F(tunnelName),
		TunnelSecret: cloudflare.F(tunnelSecret),
		ConfigSrc:    cloudflare.F(zero_trust.TunnelCloudflaredNewParamsConfigSrcCloudflare),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create tunnel: %w", err)
	}

	return &CloudflareTunnelInfo{
		ID:     tunnel.ID,
		Name:   tunnel.Name,
		Status: string(tunnel.Status),
	}, nil
}

// ============================================================
// Tunnel Wait Functions
// ============================================================

// waitForTunnelReady waits for a CloudflareTunnel to have Ready=True condition
// and essential status fields populated (TunnelID, TunnelDomain).
func waitForTunnelReady(ctx context.Context, k8sClient client.Client, name, namespace string, timeout time.Duration) *cfgatev1alpha1.CloudflareTunnel {
	var tunnel cfgatev1alpha1.CloudflareTunnel

	Eventually(func() bool {
		err := k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &tunnel)
		if err != nil {
			return false
		}
		if tunnel.Status.TunnelID == "" || tunnel.Status.TunnelDomain == "" {
			return false
		}
		for _, cond := range tunnel.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == metav1.ConditionTrue {
				return true
			}
		}
		return false
	}, timeout, DefaultInterval).Should(BeTrue(), "Tunnel did not become ready")

	return &tunnel
}

// waitForTunnelCondition waits for a specific condition on a CloudflareTunnel.
func waitForTunnelCondition(ctx context.Context, k8sClient client.Client, name, namespace, conditionType string, status metav1.ConditionStatus, timeout time.Duration) *cfgatev1alpha1.CloudflareTunnel {
	var tunnel cfgatev1alpha1.CloudflareTunnel

	Eventually(func() bool {
		err := k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &tunnel)
		if err != nil {
			return false
		}
		for _, cond := range tunnel.Status.Conditions {
			if cond.Type == conditionType && cond.Status == status {
				return true
			}
		}
		return false
	}, timeout, DefaultInterval).Should(BeTrue(), fmt.Sprintf("Tunnel condition %s did not become %s", conditionType, status))

	return &tunnel
}

// waitForTunnelDeleted waits for a CloudflareTunnel to be deleted from Kubernetes.
func waitForTunnelDeleted(ctx context.Context, k8sClient client.Client, name, namespace string, timeout time.Duration) {
	Eventually(func() bool {
		var tunnel cfgatev1alpha1.CloudflareTunnel
		err := k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &tunnel)
		return apierrors.IsNotFound(err)
	}, timeout, DefaultInterval).Should(BeTrue(), "Tunnel was not deleted")
}

// waitForTunnelDeletedFromCloudflare waits for a tunnel to be deleted from Cloudflare.
func waitForTunnelDeletedFromCloudflare(ctx context.Context, cfClient *cloudflare.Client, accountID, tunnelName string, timeout time.Duration) {
	Eventually(func() bool {
		tunnel, err := getTunnelFromCloudflare(ctx, cfClient, accountID, tunnelName)
		if err != nil {
			GinkgoWriter.Printf("waitForTunnelDeletedFromCloudflare: API error (will retry): %v\n", err)
			return false
		}
		return tunnel == nil
	}, timeout, DefaultInterval).Should(BeTrue(), "Tunnel was not deleted from Cloudflare")
}

// waitForTunnelDeletedByIDFromCloudflare waits for a tunnel ID to disappear from Cloudflare.
func waitForTunnelDeletedByIDFromCloudflare(ctx context.Context, cfClient *cloudflare.Client, accountID, tunnelID string, timeout time.Duration) {
	Eventually(func() bool {
		tunnel, err := getTunnelByIDFromCloudflare(ctx, cfClient, accountID, tunnelID)
		if err != nil {
			GinkgoWriter.Printf("waitForTunnelDeletedByIDFromCloudflare: API error (will retry): %v\n", err)
			return false
		}
		return tunnel == nil
	}, timeout, DefaultInterval).Should(BeTrue(), "Tunnel was not deleted from Cloudflare")
}

// waitForDeploymentSpec waits for a Deployment to exist with the expected replica count in spec.
// This verifies the controller created the Deployment correctly without requiring cloudflared
// pods to establish QUIC connections to Cloudflare edge (which is environment-dependent).
func waitForDeploymentSpec(ctx context.Context, k8sClient client.Client, name, namespace string, replicas int32, timeout time.Duration) *appsv1.Deployment {
	var deployment appsv1.Deployment

	Eventually(func() bool {
		err := k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &deployment)
		if err != nil {
			return false
		}
		return deployment.Spec.Replicas != nil && *deployment.Spec.Replicas == replicas
	}, timeout, DefaultInterval).Should(BeTrue(), "Deployment was not created with expected replicas")

	return &deployment
}

// ============================================================
// DNS Info Types and Helpers
// ============================================================

// CloudflareDNSRecordInfo holds information about a DNS record.
type CloudflareDNSRecordInfo struct {
	ID      string
	Type    string
	Name    string
	Content string
	Proxied bool
	TTL     float64
	Comment string
}

// getDNSRecordFromCloudflare fetches a DNS record from Cloudflare.
func getDNSRecordFromCloudflare(ctx context.Context, cfClient *cloudflare.Client, zoneID, hostname, recordType string) (*CloudflareDNSRecordInfo, error) {
	records, err := cfClient.DNS.Records.List(ctx, dns.RecordListParams{
		ZoneID: cloudflare.F(zoneID),
		Name:   cloudflare.F(dns.RecordListParamsName{Exact: cloudflare.F(hostname)}),
		Type:   cloudflare.F(dns.RecordListParamsType(recordType)),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list DNS records: %w", err)
	}

	for _, record := range records.Result {
		if record.Name == hostname {
			return &CloudflareDNSRecordInfo{
				ID:      record.ID,
				Type:    string(record.Type),
				Name:    record.Name,
				Content: record.Content,
				Proxied: record.Proxied,
				TTL:     float64(record.TTL),
				Comment: record.Comment,
			}, nil
		}
	}

	return nil, nil // Not found.
}

// getZoneIDByName gets the zone ID for a zone name.
func getZoneIDByName(ctx context.Context, cfClient *cloudflare.Client, zoneName string) (string, error) {
	zoneList, err := cfClient.Zones.List(ctx, zones.ZoneListParams{
		Name: cloudflare.F(zoneName),
	})
	if err != nil {
		return "", fmt.Errorf("failed to list zones: %w", err)
	}

	for _, zone := range zoneList.Result {
		if zone.Name == zoneName {
			return zone.ID, nil
		}
	}

	return "", fmt.Errorf("zone %s not found", zoneName)
}

// Note: waitForDNSReady, waitForDNSCondition, waitForDNSDeleted are defined in dns_test.go
// waitForDNSCondition waits for a specific condition on a CloudflareDNS.
func waitForDNSCondition(ctx context.Context, k8sClient client.Client, name, namespace, conditionType string, status metav1.ConditionStatus, timeout time.Duration) *cfgatev1alpha1.CloudflareDNS {
	var dnsResource cfgatev1alpha1.CloudflareDNS

	Eventually(func() bool {
		err := k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &dnsResource)
		if err != nil {
			return false
		}
		for _, cond := range dnsResource.Status.Conditions {
			if cond.Type == conditionType && cond.Status == status {
				return true
			}
		}
		return false
	}, timeout, DefaultInterval).Should(BeTrue(), fmt.Sprintf("CloudflareDNS condition %s did not become %s", conditionType, status))

	return &dnsResource
}

// ============================================================
// Access Application Info Types and Helpers
// ============================================================

// CloudflareAccessApplicationInfo holds information about an Access Application.
type CloudflareAccessApplicationInfo struct {
	ID              string
	Name            string
	Domain          string
	AUD             string
	SessionDuration string

	// P0 fields for §6.3 test assertions.
	AllowedIdps            []string
	AutoRedirectToIdentity bool
	AppLauncherVisible     bool

	// P1 fields for future test expansion.
	OptionsPreflightBypass      bool
	PathCookieAttribute         bool
	ServiceAuth401Redirect      bool
	CustomNonIdentityDenyUrl    string
	ReadServiceTokensFromHeader string
}

// getAccessApplicationFromCloudflare fetches an Access Application by name from Cloudflare.
func getAccessApplicationFromCloudflare(ctx context.Context, cfClient *cloudflare.Client, accountID, appName string) (*CloudflareAccessApplicationInfo, error) {
	iter := cfClient.ZeroTrust.Access.Applications.ListAutoPaging(ctx, zero_trust.AccessApplicationListParams{
		AccountID: cloudflare.F(accountID),
	})

	for iter.Next() {
		app := iter.Current()
		if app.Name == appName {
			info := &CloudflareAccessApplicationInfo{
				ID:                          app.ID,
				Name:                        app.Name,
				Domain:                      app.Domain,
				AUD:                         app.AUD,
				SessionDuration:             app.SessionDuration,
				AutoRedirectToIdentity:      app.AutoRedirectToIdentity,
				AppLauncherVisible:          app.AppLauncherVisible,
				OptionsPreflightBypass:      app.OptionsPreflightBypass,
				PathCookieAttribute:         app.PathCookieAttribute,
				ServiceAuth401Redirect:      app.ServiceAuth401Redirect,
				CustomNonIdentityDenyUrl:    app.CustomNonIdentityDenyURL,
				ReadServiceTokensFromHeader: app.ReadServiceTokensFromHeader,
			}
			// AllowedIdPs is interface{} in the union response type.
			if idps, ok := app.AllowedIdPs.([]interface{}); ok {
				for _, idp := range idps {
					if s, ok := idp.(string); ok {
						info.AllowedIdps = append(info.AllowedIdps, s)
					}
				}
			}
			return info, nil
		}
	}

	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("failed to list Access applications: %w", err)
	}

	return nil, nil // Not found.
}

// getAccessApplicationByIDFromCloudflare fetches an Access Application by ID.
func getAccessApplicationByIDFromCloudflare(ctx context.Context, cfClient *cloudflare.Client, accountID, appID string) (*CloudflareAccessApplicationInfo, error) {
	app, err := cfClient.ZeroTrust.Access.Applications.Get(ctx, appID, zero_trust.AccessApplicationGetParams{
		AccountID: cloudflare.F(accountID),
	})
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "404") {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get Access application: %w", err)
	}

	info := &CloudflareAccessApplicationInfo{
		ID:                          app.ID,
		Name:                        app.Name,
		Domain:                      app.Domain,
		AUD:                         app.AUD,
		SessionDuration:             app.SessionDuration,
		AutoRedirectToIdentity:      app.AutoRedirectToIdentity,
		AppLauncherVisible:          app.AppLauncherVisible,
		OptionsPreflightBypass:      app.OptionsPreflightBypass,
		PathCookieAttribute:         app.PathCookieAttribute,
		ServiceAuth401Redirect:      app.ServiceAuth401Redirect,
		CustomNonIdentityDenyUrl:    app.CustomNonIdentityDenyURL,
		ReadServiceTokensFromHeader: app.ReadServiceTokensFromHeader,
	}
	// AllowedIdPs is interface{} in the union response type.
	// SDK apijson may unmarshal as []string or []interface{} depending on type.
	switch idps := app.AllowedIdPs.(type) {
	case []string:
		info.AllowedIdps = idps
	case []interface{}:
		for _, idp := range idps {
			if s, ok := idp.(string); ok {
				info.AllowedIdps = append(info.AllowedIdps, s)
			}
		}
	}
	return info, nil
}

// ============================================================
// AccessPolicy Wait Functions
// ============================================================

// waitForAccessPolicyReady waits for a CloudflareAccessPolicy to have Ready=True condition.
func waitForAccessPolicyReady(ctx context.Context, k8sClient client.Client, name, namespace string, timeout time.Duration) *cfgatev1alpha1.CloudflareAccessPolicy {
	var policy cfgatev1alpha1.CloudflareAccessPolicy

	Eventually(func() bool {
		err := k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &policy)
		if err != nil {
			return false
		}
		for _, cond := range policy.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == metav1.ConditionTrue {
				return true
			}
		}
		return false
	}, timeout, DefaultInterval).Should(BeTrue(), "AccessPolicy did not become ready")

	return &policy
}

// waitForAccessPolicyCondition waits for a specific condition on a CloudflareAccessPolicy.
func waitForAccessPolicyCondition(ctx context.Context, k8sClient client.Client, name, namespace, conditionType string, status metav1.ConditionStatus, timeout time.Duration) *cfgatev1alpha1.CloudflareAccessPolicy {
	var policy cfgatev1alpha1.CloudflareAccessPolicy

	Eventually(func() bool {
		err := k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &policy)
		if err != nil {
			return false
		}
		for _, cond := range policy.Status.Conditions {
			if cond.Type == conditionType && cond.Status == status {
				return true
			}
		}
		return false
	}, timeout, DefaultInterval).Should(BeTrue(), fmt.Sprintf("AccessPolicy condition %s did not become %s", conditionType, status))

	return &policy
}

// waitForAccessPolicyDeleted waits for a CloudflareAccessPolicy to be deleted from Kubernetes.
func waitForAccessPolicyDeleted(ctx context.Context, k8sClient client.Client, name, namespace string, timeout time.Duration) {
	Eventually(func() bool {
		var policy cfgatev1alpha1.CloudflareAccessPolicy
		err := k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &policy)
		return apierrors.IsNotFound(err)
	}, timeout, DefaultInterval).Should(BeTrue(), "AccessPolicy was not deleted")
}

// waitForGatewayCondition waits for a specific condition on a Gateway.
func waitForGatewayCondition(ctx context.Context, k8sClient client.Client, name, namespace, conditionType string, status metav1.ConditionStatus, timeout time.Duration) *gatewayv1.Gateway {
	var gateway gatewayv1.Gateway

	Eventually(func() bool {
		err := k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &gateway)
		if err != nil {
			return false
		}
		for _, cond := range gateway.Status.Conditions {
			if cond.Type == conditionType && cond.Status == status {
				return true
			}
		}
		return false
	}, timeout, DefaultInterval).Should(BeTrue(), fmt.Sprintf("Gateway condition %s did not become %s", conditionType, status))

	return &gateway
}

// waitForHTTPRouteParentCondition waits for a specific condition on a cfgate-managed parent status.
func waitForHTTPRouteParentCondition(ctx context.Context, k8sClient client.Client, name, namespace, parentNamespace, parentName, conditionType string, status metav1.ConditionStatus, timeout time.Duration) *gatewayv1.HTTPRoute {
	var route gatewayv1.HTTPRoute

	Eventually(func() bool {
		err := k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &route)
		if err != nil {
			return false
		}

		for _, parent := range route.Status.Parents {
			if string(parent.ControllerName) != "cfgate.io/cloudflare-tunnel-controller" {
				continue
			}

			actualParentNamespace := route.Namespace
			if parent.ParentRef.Namespace != nil {
				actualParentNamespace = string(*parent.ParentRef.Namespace)
			}
			if actualParentNamespace != parentNamespace || string(parent.ParentRef.Name) != parentName {
				continue
			}

			for _, cond := range parent.Conditions {
				if cond.Type == conditionType && cond.Status == status {
					return true
				}
			}
		}

		return false
	}, timeout, DefaultInterval).Should(BeTrue(), fmt.Sprintf("HTTPRoute parent condition %s did not become %s", conditionType, status))

	return &route
}

// waitForAccessApplicationDeletedFromCloudflare waits for an Access Application to be deleted from Cloudflare.
func waitForAccessApplicationDeletedFromCloudflare(ctx context.Context, cfClient *cloudflare.Client, accountID, appName string, timeout time.Duration) {
	Eventually(func() bool {
		app, err := getAccessApplicationFromCloudflare(ctx, cfClient, accountID, appName)
		if err != nil {
			GinkgoWriter.Printf("waitForAccessApplicationDeletedFromCloudflare: API error (will retry): %v\n", err)
			return false
		}
		return app == nil
	}, timeout, DefaultInterval).Should(BeTrue(), "Access Application was not deleted from Cloudflare")
}

// ============================================================
// Service Token Info Types and Helpers
// ============================================================

// CloudflareServiceTokenInfo holds information about a Service Token.
type CloudflareServiceTokenInfo struct {
	ID        string
	Name      string
	ClientID  string
	ExpiresAt string
}

// getServiceTokenFromCloudflare fetches a Service Token by name from Cloudflare.
func getServiceTokenFromCloudflare(ctx context.Context, cfClient *cloudflare.Client, accountID, tokenName string) (*CloudflareServiceTokenInfo, error) {
	iter := cfClient.ZeroTrust.Access.ServiceTokens.ListAutoPaging(ctx, zero_trust.AccessServiceTokenListParams{
		AccountID: cloudflare.F(accountID),
	})

	for iter.Next() {
		token := iter.Current()
		if token.Name == tokenName {
			return &CloudflareServiceTokenInfo{
				ID:        token.ID,
				Name:      token.Name,
				ClientID:  token.ClientID,
				ExpiresAt: token.ExpiresAt.String(),
			}, nil
		}
	}

	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("failed to list service tokens: %w", err)
	}

	return nil, nil // Not found.
}

// waitForServiceTokenDeletedFromCloudflare waits for a Service Token to be deleted from Cloudflare.
func waitForServiceTokenDeletedFromCloudflare(ctx context.Context, cfClient *cloudflare.Client, accountID, tokenName string, timeout time.Duration) {
	Eventually(func() bool {
		token, err := getServiceTokenFromCloudflare(ctx, cfClient, accountID, tokenName)
		if err != nil {
			GinkgoWriter.Printf("waitForServiceTokenDeletedFromCloudflare: API error (will retry): %v\n", err)
			return false
		}
		return token == nil
	}, timeout, DefaultInterval).Should(BeTrue(), "Service Token was not deleted from Cloudflare")
}

// waitForServiceTokenSecretCreated waits for a service token Secret to be created.
func waitForServiceTokenSecretCreated(ctx context.Context, k8sClient client.Client, name, namespace string, timeout time.Duration) *corev1.Secret {
	var secret corev1.Secret

	Eventually(func() bool {
		err := k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &secret)
		if err != nil {
			return false
		}
		// Verify it has the expected keys.
		_, hasClientID := secret.Data["CF_ACCESS_CLIENT_ID"]
		_, hasClientSecret := secret.Data["CF_ACCESS_CLIENT_SECRET"]
		return hasClientID && hasClientSecret
	}, timeout, DefaultInterval).Should(BeTrue(), "Service token secret was not created")

	return &secret
}

// listEventsForObject lists events for an involved object.
func listEventsForObject(ctx context.Context, namespace, objectName, objectKind string) ([]corev1.Event, error) {
	eventList, err := k8sClientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.name=%s", objectName),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list events for %s/%s: %w", namespace, objectName, err)
	}

	var filtered []corev1.Event
	for _, event := range eventList.Items {
		if objectKind != "" && event.InvolvedObject.Kind != objectKind {
			continue
		}
		filtered = append(filtered, event)
	}

	return filtered, nil
}

// waitForEventReason waits for an event with the given reason and type.
func waitForEventReason(ctx context.Context, namespace, objectName, objectKind, reason, eventType string, timeout time.Duration) *corev1.Event {
	var matched *corev1.Event

	Eventually(func() bool {
		events, err := listEventsForObject(ctx, namespace, objectName, objectKind)
		if err != nil {
			GinkgoWriter.Printf("waitForEventReason: event list error (will retry): %v\n", err)
			return false
		}

		for i := range events {
			if events[i].Reason == reason && (eventType == "" || events[i].Type == eventType) {
				candidate := events[i]
				matched = &candidate
				return true
			}
		}
		return false
	}, timeout, DefaultInterval).Should(BeTrue(), fmt.Sprintf("Event %s was not observed for %s/%s", reason, namespace, objectName))

	return matched
}

// ============================================================
// Gateway API Resource Creation Helpers
// ============================================================

// createGatewayClassWithController creates a GatewayClass for a specific controller.
func createGatewayClassWithController(ctx context.Context, k8sClient client.Client, name, controllerName string) *gatewayv1.GatewayClass {
	gc := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: gatewayv1.GatewayController(controllerName),
		},
	}
	err := k8sClient.Create(ctx, gc)
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			existing := &gatewayv1.GatewayClass{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name}, existing)).To(Succeed())
			return existing
		}
		Expect(err).NotTo(HaveOccurred(), "Failed to create GatewayClass")
	}
	return gc
}

// createGatewayClass creates or retrieves a GatewayClass for testing.
func createGatewayClass(ctx context.Context, k8sClient client.Client, name string) *gatewayv1.GatewayClass {
	return createGatewayClassWithController(ctx, k8sClient, name, "cfgate.io/cloudflare-tunnel-controller")
}

// createReferenceGrant creates a ReferenceGrant resource for cross-namespace tests.
func createReferenceGrant(ctx context.Context, k8sClient client.Client, name, namespace, fromGroup, fromKind, fromNamespace, toGroup, toKind string) *gatewayv1b1.ReferenceGrant {
	grant := &gatewayv1b1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: gatewayv1b1.ReferenceGrantSpec{
			From: []gatewayv1b1.ReferenceGrantFrom{
				{
					Group:     gatewayv1b1.Group(fromGroup),
					Kind:      gatewayv1b1.Kind(fromKind),
					Namespace: gatewayv1b1.Namespace(fromNamespace),
				},
			},
			To: []gatewayv1b1.ReferenceGrantTo{
				{
					Group: gatewayv1b1.Group(toGroup),
					Kind:  gatewayv1b1.Kind(toKind),
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, grant)).To(Succeed())
	return grant
}

// createGateway creates a Gateway for testing.
func createGateway(ctx context.Context, k8sClient client.Client, name, namespace, gatewayClassName, tunnelRef string) *gatewayv1.Gateway {
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				"cfgate.io/tunnel-ref": tunnelRef,
			},
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: gatewayv1.ObjectName(gatewayClassName),
			Listeners: []gatewayv1.Listener{
				{
					Name:     "https",
					Protocol: gatewayv1.HTTPSProtocolType,
					Port:     443,
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, gw)).To(Succeed())
	return gw
}

// createHTTPRoute creates an HTTPRoute for testing.
func createHTTPRoute(ctx context.Context, k8sClient client.Client, name, namespace, gatewayName string, hostnames []string, serviceName string, servicePort int32) *gatewayv1.HTTPRoute {
	parentNS := gatewayv1.Namespace(namespace)

	hr := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{
						Name:      gatewayv1.ObjectName(gatewayName),
						Namespace: &parentNS,
					},
				},
			},
			Hostnames: make([]gatewayv1.Hostname, len(hostnames)),
			Rules: []gatewayv1.HTTPRouteRule{
				{
					BackendRefs: []gatewayv1.HTTPBackendRef{
						{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{
									Name: gatewayv1.ObjectName(serviceName),
									Port: ptrTo(gatewayv1.PortNumber(servicePort)),
								},
							},
						},
					},
				},
			},
		},
	}

	for i, h := range hostnames {
		hr.Spec.Hostnames[i] = gatewayv1.Hostname(h)
	}

	Expect(k8sClient.Create(ctx, hr)).To(Succeed())
	return hr
}

// createTestService creates a simple Service for testing.
func createTestService(ctx context.Context, k8sClient client.Client, name, namespace string, port int32) *corev1.Service {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port: port,
				},
			},
			Selector: map[string]string{
				"app": name,
			},
		},
	}
	Expect(k8sClient.Create(ctx, svc)).To(Succeed())
	return svc
}

// ============================================================
// CloudflareTunnel Creation Helpers
// ============================================================

// createCloudflareTunnel creates a CloudflareTunnel CR for testing.
func createCloudflareTunnel(ctx context.Context, k8sClient client.Client, name, namespace, tunnelName string) *cfgatev1alpha1.CloudflareTunnel {
	tunnel := &cfgatev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: cfgatev1alpha1.CloudflareTunnelSpec{
			Tunnel: cfgatev1alpha1.TunnelIdentity{
				Name: tunnelName,
			},
			Cloudflare: cfgatev1alpha1.CloudflareConfig{
				AccountID: testEnv.CloudflareAccountID,
				SecretRef: cfgatev1alpha1.SecretRef{
					Name: "cloudflare-credentials",
				},
			},
			FallbackCredentialsRef: e2eFallbackCredentialsRef(),
			Cloudflared: cfgatev1alpha1.CloudflaredConfig{
				Replicas: 1,
			},
		},
	}
	Expect(k8sClient.Create(ctx, tunnel)).To(Succeed())
	return tunnel
}

func e2eFallbackCredentialsRef() *cfgatev1alpha1.SecretReference {
	return &cfgatev1alpha1.SecretReference{
		Name:      e2eFallbackCredentialsSecret,
		Namespace: e2eFallbackCredentialsNamespace,
	}
}

// createCloudflareTunnelInContext creates a CloudflareTunnel for context-level sharing.
// Note: In alpha.3, DNS is a separate CRD (CloudflareDNS). To configure DNS records,
// create a separate CloudflareDNS resource using createCloudflareDNS.
func createCloudflareTunnelInContext(ctx context.Context, k8sClient client.Client, name, namespace, tunnelName string) *cfgatev1alpha1.CloudflareTunnel {
	return createCloudflareTunnel(ctx, k8sClient, name, namespace, tunnelName)
}

// createCloudflareTunnelWithInvalidToken creates a CloudflareTunnel with invalid credentials.
func createCloudflareTunnelWithInvalidToken(ctx context.Context, k8sClient client.Client, name, namespace, tunnelName string) *cfgatev1alpha1.CloudflareTunnel {
	// First create a secret with invalid token.
	invalidSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cloudflare-invalid-credentials",
			Namespace: namespace,
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"CLOUDFLARE_API_TOKEN": "invalid-token-for-testing",
		},
	}
	Expect(k8sClient.Create(ctx, invalidSecret)).To(Succeed())

	tunnel := &cfgatev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: cfgatev1alpha1.CloudflareTunnelSpec{
			Tunnel: cfgatev1alpha1.TunnelIdentity{
				Name: tunnelName,
			},
			Cloudflare: cfgatev1alpha1.CloudflareConfig{
				AccountID: testEnv.CloudflareAccountID,
				SecretRef: cfgatev1alpha1.SecretRef{
					Name: "cloudflare-invalid-credentials",
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, tunnel)).To(Succeed())
	return tunnel
}

// getTunnelConfigurationFromCloudflare fetches remote tunnel configuration from Cloudflare.
func getTunnelConfigurationFromCloudflare(ctx context.Context, cfClient *cloudflare.Client, accountID, tunnelID string) (*zero_trust.TunnelCloudflaredConfigurationGetResponse, error) {
	config, err := cfClient.ZeroTrust.Tunnels.Cloudflared.Configurations.Get(ctx, tunnelID, zero_trust.TunnelCloudflaredConfigurationGetParams{
		AccountID: cloudflare.F(accountID),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get tunnel configuration: %w", err)
	}
	return config, nil
}

// ============================================================
// CloudflareDNS Creation Helpers (alpha.3: separate CRD)
// ============================================================
//
// NOTE: dns_test.go defines its own DNS helpers (createCloudflareDNSWithTunnelRef,
// createCloudflareDNSWithExternalTarget, etc.) that include explicit hostnames and
// tunnel namespace parameters. Only createCloudflareDNSWithGatewayRoutes is shared
// here as it's used by annotations_test.go and combined_test.go.

// createCloudflareDNSWithGatewayRoutes creates a CloudflareDNS with GatewayRoutes source.
// Uses annotationFilter to watch routes with the specified annotation.
func createCloudflareDNSWithGatewayRoutes(ctx context.Context, k8sClient client.Client, name, namespace, tunnelRefName string, zones []string, annotationFilter string) *cfgatev1alpha1.CloudflareDNS {
	dnsZones := make([]cfgatev1alpha1.DNSZoneConfig, len(zones))
	for i, zone := range zones {
		dnsZones[i] = cfgatev1alpha1.DNSZoneConfig{Name: zone}
	}

	dnsResource := &cfgatev1alpha1.CloudflareDNS{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: cfgatev1alpha1.CloudflareDNSSpec{
			TunnelRef: &cfgatev1alpha1.DNSTunnelRef{
				Name: tunnelRefName,
			},
			Zones:  dnsZones,
			Policy: cfgatev1alpha1.DNSPolicySync,
			Source: cfgatev1alpha1.DNSHostnameSource{
				GatewayRoutes: cfgatev1alpha1.DNSGatewayRoutesSource{
					Enabled:          true,
					AnnotationFilter: annotationFilter,
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, dnsResource)).To(Succeed())
	return dnsResource
}

// ============================================================
// CloudflareAccessPolicy Creation Helpers
// ============================================================

// createCloudflareAccessPolicy creates a CloudflareAccessPolicy CR with "everyone" rule (no IdP required).
func createCloudflareAccessPolicy(ctx context.Context, k8sClient client.Client, name, namespace, targetRouteName, hostname string) *cfgatev1alpha1.CloudflareAccessPolicy {
	policy := &cfgatev1alpha1.CloudflareAccessPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
			TargetRef: &cfgatev1alpha1.PolicyTargetReference{
				Group: "gateway.networking.k8s.io",
				Kind:  "HTTPRoute",
				Name:  targetRouteName,
			},
			CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
				Name:      "cloudflare-credentials",
				AccountID: testEnv.CloudflareAccountID,
			},
			Application: cfgatev1alpha1.AccessApplication{
				Name:   name,
				Domain: hostname,
			},
			Policies: []cfgatev1alpha1.AccessPolicyRule{
				{
					Name:     "allow-all",
					Decision: "allow",
					Include: []cfgatev1alpha1.AccessRule{
						{
							Everyone: ptrTo(true),
						},
					},
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, policy)).To(Succeed())
	return policy
}

// createCloudflareAccessPolicyWithServiceToken creates an AccessPolicy with service token authentication.
func createCloudflareAccessPolicyWithServiceToken(ctx context.Context, k8sClient client.Client, name, namespace, targetRouteName, hostname, tokenSecretName string) *cfgatev1alpha1.CloudflareAccessPolicy {
	policy := &cfgatev1alpha1.CloudflareAccessPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
			TargetRef: &cfgatev1alpha1.PolicyTargetReference{
				Group: "gateway.networking.k8s.io",
				Kind:  "HTTPRoute",
				Name:  targetRouteName,
			},
			CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
				Name:      "cloudflare-credentials",
				AccountID: testEnv.CloudflareAccountID,
			},
			Application: cfgatev1alpha1.AccessApplication{
				Name:   name,
				Domain: hostname,
			},
			Policies: []cfgatev1alpha1.AccessPolicyRule{
				{
					Name:     "allow-service-token",
					Decision: "non_identity",
					Include: []cfgatev1alpha1.AccessRule{
						{
							AnyValidServiceToken: ptrTo(true),
						},
					},
				},
			},
			ServiceTokens: []cfgatev1alpha1.ServiceTokenConfig{
				{
					Name:     name + "-token",
					Duration: "8760h",
					SecretRef: cfgatev1alpha1.ServiceTokenSecretRef{
						Name: tokenSecretName,
					},
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, policy)).To(Succeed())
	return policy
}

// createCloudflareAccessPolicyWithIPRule creates an AccessPolicy with IP range allow rule.
// Uses SDK-aligned AccessIPRule type with Ranges field.
func createCloudflareAccessPolicyWithIPRule(ctx context.Context, k8sClient client.Client, name, namespace, targetRouteName, hostname string, ipRanges []string) *cfgatev1alpha1.CloudflareAccessPolicy {
	policy := &cfgatev1alpha1.CloudflareAccessPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
			TargetRef: &cfgatev1alpha1.PolicyTargetReference{
				Group: "gateway.networking.k8s.io",
				Kind:  "HTTPRoute",
				Name:  targetRouteName,
			},
			CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
				Name:      "cloudflare-credentials",
				AccountID: testEnv.CloudflareAccountID,
			},
			Application: cfgatev1alpha1.AccessApplication{
				Name:   name,
				Domain: hostname,
			},
			Policies: []cfgatev1alpha1.AccessPolicyRule{
				{
					Name:     "allow-ip-ranges",
					Decision: "bypass",
					Include: []cfgatev1alpha1.AccessRule{
						{
							IP: &cfgatev1alpha1.AccessIPRule{
								Ranges: ipRanges,
							},
						},
					},
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, policy)).To(Succeed())
	return policy
}

// createCloudflareAccessPolicyWithCountryRule creates an AccessPolicy with country allow rule.
// Uses SDK-aligned AccessCountryRule type with Codes field (ISO 3166-1 alpha-2).
func createCloudflareAccessPolicyWithCountryRule(ctx context.Context, k8sClient client.Client, name, namespace, targetRouteName, hostname string, countryCodes []string) *cfgatev1alpha1.CloudflareAccessPolicy {
	policy := &cfgatev1alpha1.CloudflareAccessPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
			TargetRef: &cfgatev1alpha1.PolicyTargetReference{
				Group: "gateway.networking.k8s.io",
				Kind:  "HTTPRoute",
				Name:  targetRouteName,
			},
			CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
				Name:      "cloudflare-credentials",
				AccountID: testEnv.CloudflareAccountID,
			},
			Application: cfgatev1alpha1.AccessApplication{
				Name:   name,
				Domain: hostname,
			},
			Policies: []cfgatev1alpha1.AccessPolicyRule{
				{
					Name:     "allow-countries",
					Decision: "allow",
					Include: []cfgatev1alpha1.AccessRule{
						{
							Country: &cfgatev1alpha1.AccessCountryRule{
								Codes: countryCodes,
							},
						},
					},
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, policy)).To(Succeed())
	return policy
}

// createCloudflareAccessPolicyWithEmailRule creates an AccessPolicy with email allow rule (requires IdP).
// Uses SDK-aligned AccessEmailRule type with Addresses field.
func createCloudflareAccessPolicyWithEmailRule(ctx context.Context, k8sClient client.Client, name, namespace, targetRouteName, hostname string, emails []string) *cfgatev1alpha1.CloudflareAccessPolicy {
	policy := &cfgatev1alpha1.CloudflareAccessPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
			TargetRef: &cfgatev1alpha1.PolicyTargetReference{
				Group: "gateway.networking.k8s.io",
				Kind:  "HTTPRoute",
				Name:  targetRouteName,
			},
			CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
				Name:      "cloudflare-credentials",
				AccountID: testEnv.CloudflareAccountID,
			},
			Application: cfgatev1alpha1.AccessApplication{
				Name:   name,
				Domain: hostname,
			},
			Policies: []cfgatev1alpha1.AccessPolicyRule{
				{
					Name:     "allow-emails",
					Decision: "allow",
					Include: []cfgatev1alpha1.AccessRule{
						{
							Email: &cfgatev1alpha1.AccessEmailRule{
								Addresses: emails,
							},
						},
					},
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, policy)).To(Succeed())
	return policy
}

// createCloudflareAccessPolicyWithEmailDomainRule creates an AccessPolicy with email domain allow rule (requires IdP).
// Uses SDK-aligned AccessEmailDomainRule type.
func createCloudflareAccessPolicyWithEmailDomainRule(ctx context.Context, k8sClient client.Client, name, namespace, targetRouteName, hostname, emailDomain string) *cfgatev1alpha1.CloudflareAccessPolicy {
	policy := &cfgatev1alpha1.CloudflareAccessPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
			TargetRef: &cfgatev1alpha1.PolicyTargetReference{
				Group: "gateway.networking.k8s.io",
				Kind:  "HTTPRoute",
				Name:  targetRouteName,
			},
			CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
				Name:      "cloudflare-credentials",
				AccountID: testEnv.CloudflareAccountID,
			},
			Application: cfgatev1alpha1.AccessApplication{
				Name:   name,
				Domain: hostname,
			},
			Policies: []cfgatev1alpha1.AccessPolicyRule{
				{
					Name:     "allow-email-domain",
					Decision: "allow",
					Include: []cfgatev1alpha1.AccessRule{
						{
							EmailDomain: &cfgatev1alpha1.AccessEmailDomainRule{
								Domain: emailDomain,
							},
						},
					},
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, policy)).To(Succeed())
	return policy
}

// createCloudflareAccessPolicyWithOIDCClaimRule creates an AccessPolicy with OIDC claim rule (requires IdP).
// Uses SDK-aligned AccessOIDCClaimRule type.
func createCloudflareAccessPolicyWithOIDCClaimRule(ctx context.Context, k8sClient client.Client, name, namespace, targetRouteName, hostname, idpID, claimName, claimValue string) *cfgatev1alpha1.CloudflareAccessPolicy {
	policy := &cfgatev1alpha1.CloudflareAccessPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
			TargetRef: &cfgatev1alpha1.PolicyTargetReference{
				Group: "gateway.networking.k8s.io",
				Kind:  "HTTPRoute",
				Name:  targetRouteName,
			},
			CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
				Name:      "cloudflare-credentials",
				AccountID: testEnv.CloudflareAccountID,
			},
			Application: cfgatev1alpha1.AccessApplication{
				Name:   name,
				Domain: hostname,
			},
			Policies: []cfgatev1alpha1.AccessPolicyRule{
				{
					Name:     "allow-oidc-claim",
					Decision: "allow",
					Include: []cfgatev1alpha1.AccessRule{
						{
							OIDCClaim: &cfgatev1alpha1.AccessOIDCClaimRule{
								IdentityProviderID: idpID,
								ClaimName:          claimName,
								ClaimValue:         claimValue,
							},
						},
					},
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, policy)).To(Succeed())
	return policy
}

// createCloudflareAccessPolicyWithGSuiteGroupRule creates an AccessPolicy with GSuite group rule (requires GSuite IdP).
// Uses SDK-aligned AccessGSuiteGroupRule type.
func createCloudflareAccessPolicyWithGSuiteGroupRule(ctx context.Context, k8sClient client.Client, name, namespace, targetRouteName, hostname, idpID, groupEmail string) *cfgatev1alpha1.CloudflareAccessPolicy {
	policy := &cfgatev1alpha1.CloudflareAccessPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
			TargetRef: &cfgatev1alpha1.PolicyTargetReference{
				Group: "gateway.networking.k8s.io",
				Kind:  "HTTPRoute",
				Name:  targetRouteName,
			},
			CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
				Name:      "cloudflare-credentials",
				AccountID: testEnv.CloudflareAccountID,
			},
			Application: cfgatev1alpha1.AccessApplication{
				Name:   name,
				Domain: hostname,
			},
			Policies: []cfgatev1alpha1.AccessPolicyRule{
				{
					Name:     "allow-gsuite-group",
					Decision: "allow",
					Include: []cfgatev1alpha1.AccessRule{
						{
							GSuiteGroup: &cfgatev1alpha1.AccessGSuiteGroupRule{
								IdentityProviderID: idpID,
								Email:              groupEmail,
							},
						},
					},
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, policy)).To(Succeed())
	return policy
}

// Note: skipIfNoIdP and skipIfNoGSuiteGroup are defined in e2e_suite_test.go

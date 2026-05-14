package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	cloudflare "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/dns"
	"github.com/cloudflare/cloudflare-go/v6/option"
	"github.com/cloudflare/cloudflare-go/v6/zero_trust"
	"github.com/cloudflare/cloudflare-go/v6/zones"
)

const (
	e2ePrefix       = "e2e-"          // Identifies E2E test resources
	recoveryPrefix  = "recovery-"     // Identifies recovery test resources
	ownershipPrefix = "_cfgate.e2e-"  // Ownership TXT record prefix for E2E
	cleanupTimeout  = 5 * time.Minute // Maximum time for cleanup operation
)

// resource represents a Cloudflare resource identified for cleanup.
type resource struct {
	ID   string // Cloudflare resource ID
	Name string // Resource name (for display)
	Type string // Resource type: tunnel, dns, access_app, access_tag, service_token
}

type cleanupConfig struct {
	APIToken  string
	AccountID string
	ZoneName  string
}

type cleanupSummary struct {
	Found           int
	Deleted         int
	Failed          int
	FailedResources []resource
}

type cleanupClient interface {
	ListOrphanedTunnels(context.Context, string) ([]resource, error)
	ListOrphanedDNSRecords(context.Context, string) ([]resource, error)
	ListOrphanedAccessApplications(context.Context, string) ([]resource, error)
	ListOrphanedAccessTags(context.Context, string) ([]resource, error)
	ListOrphanedServiceTokens(context.Context, string) ([]resource, error)
	ResolveZoneID(context.Context, string) (string, error)
	DeleteTunnel(context.Context, string, string) error
	DeleteDNSRecord(context.Context, string, string) error
	DeleteAccessApplication(context.Context, string, string) error
	DeleteAccessTag(context.Context, string, string) error
	DeleteServiceToken(context.Context, string, string) error
}

type cloudflareCleanupClient struct {
	client *cloudflare.Client
}

type cleanupRuntime struct {
	newClient func(string) cleanupClient
}

var (
	// Test hooks below are intentionally swappable in serial tests; do not use with t.Parallel().
	listOrphanedTunnelsFn            = listOrphanedTunnels
	listOrphanedDNSRecordsFn         = listOrphanedDNSRecords
	listOrphanedAccessApplicationsFn = listOrphanedAccessApplications
	listOrphanedAccessTagsFn         = listOrphanedAccessTags
	listOrphanedServiceTokensFn      = listOrphanedServiceTokens
	getZoneIDFn                      = getZoneID
	deleteTunnelFn                   = deleteTunnel
	deleteDNSRecordFn                = deleteDNSRecord
	deleteAccessApplicationFn        = deleteAccessApplication
	deleteAccessTagFn                = deleteAccessTag
	deleteServiceTokenFn             = deleteServiceToken
)

func main() {
	os.Exit(executeCleanup(os.Getenv, os.Stdout, defaultCleanupRuntime()))
}

func defaultCleanupRuntime() cleanupRuntime {
	return cleanupRuntime{
		newClient: func(apiToken string) cleanupClient {
			return &cloudflareCleanupClient{
				client: cloudflare.NewClient(option.WithAPIToken(apiToken)),
			}
		},
	}
}

func executeCleanup(getenv func(string) string, out io.Writer, runtime cleanupRuntime) int {
	cfg, err := loadCleanupConfig(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(out, "ERROR: %v\n", err)
		_, _ = fmt.Fprintln(out, "Usage: mise run e2e:cleanup")
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()

	if _, err := runCleanup(ctx, cfg, runtime.newClient(cfg.APIToken), out); err != nil {
		return 1
	}
	return 0
}

func loadCleanupConfig(getenv func(string) string) (cleanupConfig, error) {
	cfg := cleanupConfig{
		APIToken:  getenv("CLOUDFLARE_API_TOKEN"),
		AccountID: getenv("CLOUDFLARE_ACCOUNT_ID"),
		ZoneName:  getenv("CLOUDFLARE_ZONE_NAME"),
	}
	if cfg.APIToken == "" || cfg.AccountID == "" {
		return cleanupConfig{}, fmt.Errorf("CLOUDFLARE_API_TOKEN and CLOUDFLARE_ACCOUNT_ID must be set")
	}
	return cfg, nil
}

func runCleanup(ctx context.Context, cfg cleanupConfig, client cleanupClient, out io.Writer) (cleanupSummary, error) {
	summary := cleanupSummary{}

	_, _ = fmt.Fprintln(out, "=== cfgate E2E Resource Cleanup ===")
	_, _ = fmt.Fprintf(out, "Account ID: %s\n", cfg.AccountID)
	_, _ = fmt.Fprintf(out, "Zone: %s\n\n", cfg.ZoneName)

	tunnels, err := client.ListOrphanedTunnels(ctx, cfg.AccountID)
	printScanSection(out, "Tunnels", tunnels, err)
	summary.Found += len(tunnels)

	var records []resource
	if cfg.ZoneName != "" {
		records, err = client.ListOrphanedDNSRecords(ctx, cfg.ZoneName)
		printScanSection(out, "DNS Records", records, err)
		summary.Found += len(records)
	}

	apps, err := client.ListOrphanedAccessApplications(ctx, cfg.AccountID)
	printScanSection(out, "Access Applications", apps, err)
	summary.Found += len(apps)

	tokens, err := client.ListOrphanedServiceTokens(ctx, cfg.AccountID)
	printScanSection(out, "Service Tokens", tokens, err)
	summary.Found += len(tokens)

	deleteResources := func(resources []resource, label string, deleteFn func(resource) error) {
		for _, res := range resources {
			_, _ = fmt.Fprintf(out, "  Deleting %s: %s ... ", label, res.Name)
			if err := deleteFn(res); err != nil {
				_, _ = fmt.Fprintf(out, "FAILED: %v\n", err)
				summary.Failed++
				summary.FailedResources = append(summary.FailedResources, res)
				continue
			}

			_, _ = fmt.Fprintln(out, "OK")
			summary.Deleted++
		}
	}

	if summary.Found > 0 {
		_, _ = fmt.Fprintf(out, "=== Found %d orphaned resources ===\n\n", summary.Found)
		_, _ = fmt.Fprintln(out, "--- Deleting Resources ---")

		deleteResources(tunnels, "tunnel", func(res resource) error {
			return client.DeleteTunnel(ctx, cfg.AccountID, res.ID)
		})

		if cfg.ZoneName != "" && len(records) > 0 {
			zoneID, err := client.ResolveZoneID(ctx, cfg.ZoneName)
			if err != nil {
				_, _ = fmt.Fprintf(out, "Warning: failed to resolve zone ID for %s: %v\n", cfg.ZoneName, err)
			} else {
				deleteResources(records, "DNS record", func(res resource) error {
					return client.DeleteDNSRecord(ctx, zoneID, res.ID)
				})
			}
		}

		deleteResources(apps, "Access application", func(res resource) error {
			return client.DeleteAccessApplication(ctx, cfg.AccountID, res.ID)
		})

		deleteResources(tokens, "service token", func(res resource) error {
			return client.DeleteServiceToken(ctx, cfg.AccountID, res.ID)
		})
	}

	tags, err := client.ListOrphanedAccessTags(ctx, cfg.AccountID)
	printScanSection(out, "Access Tags", tags, err)
	summary.Found += len(tags)

	if len(tags) > 0 && summary.Deleted+summary.Failed == 0 {
		_, _ = fmt.Fprintf(out, "=== Found %d orphaned resources ===\n\n", summary.Found)
		_, _ = fmt.Fprintln(out, "--- Deleting Resources ---")
	}

	deleteResources(tags, "Access tag", func(res resource) error {
		return client.DeleteAccessTag(ctx, cfg.AccountID, res.ID)
	})

	if summary.Found == 0 {
		_, _ = fmt.Fprintln(out, "=== No orphaned E2E resources found ===")
		return summary, nil
	}

	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "=== Cleanup Summary ===")
	_, _ = fmt.Fprintf(out, "Deleted: %d\n", summary.Deleted)
	_, _ = fmt.Fprintf(out, "Failed:  %d\n", summary.Failed)

	if summary.Failed > 0 {
		_, _ = fmt.Fprintln(out, "\nFailed resources:")
		for _, res := range summary.FailedResources {
			_, _ = fmt.Fprintf(out, "  - %s: %s (ID: %s)\n", res.Type, res.Name, res.ID)
		}
		return summary, fmt.Errorf("cleanup failed for %d resource(s)", summary.Failed)
	}

	return summary, nil
}

func printScanSection(out io.Writer, title string, resources []resource, err error) {
	_, _ = fmt.Fprintf(out, "--- Scanning %s ---\n", title)
	if err != nil {
		_, _ = fmt.Fprintf(out, "  Warning: %v\n", err)
	}
	for _, res := range resources {
		_, _ = fmt.Fprintf(out, "  Found: %s (ID: %s)\n", res.Name, res.ID)
	}
	if len(resources) == 0 {
		_, _ = fmt.Fprintf(out, "  No orphaned %s found\n", strings.ToLower(title))
	}
	_, _ = fmt.Fprintln(out)
}

func (c *cloudflareCleanupClient) ListOrphanedTunnels(ctx context.Context, accountID string) ([]resource, error) {
	return listOrphanedTunnelsFn(ctx, c.client, accountID)
}

func (c *cloudflareCleanupClient) ListOrphanedDNSRecords(ctx context.Context, zoneName string) ([]resource, error) {
	return listOrphanedDNSRecordsFn(ctx, c.client, zoneName)
}

func (c *cloudflareCleanupClient) ListOrphanedAccessApplications(ctx context.Context, accountID string) ([]resource, error) {
	return listOrphanedAccessApplicationsFn(ctx, c.client, accountID)
}

func (c *cloudflareCleanupClient) ListOrphanedAccessTags(ctx context.Context, accountID string) ([]resource, error) {
	return listOrphanedAccessTagsFn(ctx, c.client, accountID)
}

func (c *cloudflareCleanupClient) ListOrphanedServiceTokens(ctx context.Context, accountID string) ([]resource, error) {
	return listOrphanedServiceTokensFn(ctx, c.client, accountID)
}

func (c *cloudflareCleanupClient) ResolveZoneID(ctx context.Context, zoneName string) (string, error) {
	return getZoneIDFn(ctx, c.client, zoneName)
}

func (c *cloudflareCleanupClient) DeleteTunnel(ctx context.Context, accountID, tunnelID string) error {
	return deleteTunnelFn(ctx, c.client, accountID, tunnelID)
}

func (c *cloudflareCleanupClient) DeleteDNSRecord(ctx context.Context, zoneID, recordID string) error {
	return deleteDNSRecordFn(ctx, c.client, zoneID, recordID)
}

func (c *cloudflareCleanupClient) DeleteAccessApplication(ctx context.Context, accountID, appID string) error {
	return deleteAccessApplicationFn(ctx, c.client, accountID, appID)
}

func (c *cloudflareCleanupClient) DeleteAccessTag(ctx context.Context, accountID, tagName string) error {
	return deleteAccessTagFn(ctx, c.client, accountID, tagName)
}

func (c *cloudflareCleanupClient) DeleteServiceToken(ctx context.Context, accountID, tokenID string) error {
	return deleteServiceTokenFn(ctx, c.client, accountID, tokenID)
}

// listOrphanedTunnels finds tunnels with e2e- or recovery- name prefix.
func listOrphanedTunnels(ctx context.Context, client *cloudflare.Client, accountID string) ([]resource, error) {
	var results []resource
	iter := client.ZeroTrust.Tunnels.Cloudflared.ListAutoPaging(ctx, zero_trust.TunnelCloudflaredListParams{
		AccountID: cloudflare.F(accountID),
	})

	for iter.Next() {
		t := iter.Current()
		if strings.HasPrefix(t.Name, e2ePrefix) || strings.HasPrefix(t.Name, recoveryPrefix) {
			results = append(results, resource{ID: t.ID, Name: t.Name, Type: "tunnel"})
		}
	}

	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("failed to list tunnels: %w", err)
	}

	return results, nil
}

// listOrphanedDNSRecords finds DNS records with e2e- in name or _cfgate.e2e- prefix.
func listOrphanedDNSRecords(ctx context.Context, client *cloudflare.Client, zoneName string) ([]resource, error) {
	zoneID, err := getZoneID(ctx, client, zoneName)
	if err != nil {
		return nil, err
	}

	var results []resource
	iter := client.DNS.Records.ListAutoPaging(ctx, dns.RecordListParams{
		ZoneID: cloudflare.F(zoneID),
	})

	for iter.Next() {
		record := iter.Current()
		if strings.Contains(record.Name, e2ePrefix) || strings.HasPrefix(record.Name, ownershipPrefix) {
			results = append(results, resource{ID: record.ID, Name: record.Name, Type: "dns"})
		}
	}

	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("failed to list DNS records: %w", err)
	}

	return results, nil
}

// listOrphanedAccessApplications finds Access applications with e2e- name prefix.
func listOrphanedAccessApplications(ctx context.Context, client *cloudflare.Client, accountID string) ([]resource, error) {
	var results []resource
	iter := client.ZeroTrust.Access.Applications.ListAutoPaging(ctx, zero_trust.AccessApplicationListParams{
		AccountID: cloudflare.F(accountID),
	})

	for iter.Next() {
		app := iter.Current()
		if strings.HasPrefix(app.Name, e2ePrefix) {
			results = append(results, resource{ID: app.ID, Name: app.Name, Type: "access_app"})
		}
	}

	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("failed to list Access applications: %w", err)
	}

	return results, nil
}

// listOrphanedAccessTags finds unreferenced cfgate owner tags.
func listOrphanedAccessTags(ctx context.Context, client *cloudflare.Client, accountID string) ([]resource, error) {
	referenced := map[string]struct{}{}
	appIter := client.ZeroTrust.Access.Applications.ListAutoPaging(ctx, zero_trust.AccessApplicationListParams{
		AccountID: cloudflare.F(accountID),
	})
	for appIter.Next() {
		for _, tag := range accessApplicationTagNames(appIter.Current().Tags) {
			referenced[tag] = struct{}{}
		}
	}
	if err := appIter.Err(); err != nil {
		return nil, fmt.Errorf("failed to list Access applications for tag references: %w", err)
	}

	var results []resource
	tagIter := client.ZeroTrust.Access.Tags.ListAutoPaging(ctx, zero_trust.AccessTagListParams{
		AccountID: cloudflare.F(accountID),
	})
	for tagIter.Next() {
		tag := tagIter.Current()
		if !isCfgateOwnerTag(tag.Name) {
			continue
		}
		if _, ok := referenced[tag.Name]; ok {
			continue
		}
		results = append(results, resource{ID: tag.Name, Name: tag.Name, Type: "access_tag"})
	}
	if err := tagIter.Err(); err != nil {
		return nil, fmt.Errorf("failed to list Access tags: %w", err)
	}

	return results, nil
}

func isCfgateOwnerTag(name string) bool {
	const prefix = "cfgate:"
	if len(name) != len(prefix)+28 || !strings.HasPrefix(name, prefix) {
		return false
	}
	for _, r := range name[len(prefix):] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func accessApplicationTagNames(tags interface{}) []string {
	switch typed := tags.(type) {
	case nil:
		return nil
	case []string:
		return typed
	case []interface{}:
		names := make([]string, 0, len(typed))
		for _, tag := range typed {
			if name, ok := tag.(string); ok {
				names = append(names, name)
			}
		}
		return names
	default:
		return nil
	}
}

// listOrphanedServiceTokens finds service tokens with e2e- name prefix.
func listOrphanedServiceTokens(ctx context.Context, client *cloudflare.Client, accountID string) ([]resource, error) {
	var results []resource
	iter := client.ZeroTrust.Access.ServiceTokens.ListAutoPaging(ctx, zero_trust.AccessServiceTokenListParams{
		AccountID: cloudflare.F(accountID),
	})

	for iter.Next() {
		token := iter.Current()
		if strings.HasPrefix(token.Name, e2ePrefix) {
			results = append(results, resource{ID: token.ID, Name: token.Name, Type: "service_token"})
		}
	}

	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("failed to list service tokens: %w", err)
	}

	return results, nil
}

// getZoneID resolves a zone name to its Cloudflare zone ID.
func getZoneID(ctx context.Context, client *cloudflare.Client, zoneName string) (string, error) {
	zoneList, err := client.Zones.List(ctx, zones.ZoneListParams{
		Name: cloudflare.F(zoneName),
	})
	if err != nil {
		return "", fmt.Errorf("failed to get zone ID for %s: %w", zoneName, err)
	}
	if len(zoneList.Result) == 0 {
		return "", fmt.Errorf("failed to get zone ID for %s: zone not found", zoneName)
	}
	return zoneList.Result[0].ID, nil
}

// deleteTunnel removes a tunnel after clearing its connections.
func deleteTunnel(ctx context.Context, client *cloudflare.Client, accountID, tunnelID string) error {
	_, _ = client.ZeroTrust.Tunnels.Cloudflared.Connections.Delete(ctx, tunnelID, zero_trust.TunnelCloudflaredConnectionDeleteParams{
		AccountID: cloudflare.F(accountID),
	})

	_, err := client.ZeroTrust.Tunnels.Cloudflared.Delete(ctx, tunnelID, zero_trust.TunnelCloudflaredDeleteParams{
		AccountID: cloudflare.F(accountID),
	})
	if err != nil && !strings.Contains(err.Error(), "not found") {
		return err
	}
	return nil
}

// deleteDNSRecord removes a DNS record by ID, ignoring not-found errors.
func deleteDNSRecord(ctx context.Context, client *cloudflare.Client, zoneID, recordID string) error {
	_, err := client.DNS.Records.Delete(ctx, recordID, dns.RecordDeleteParams{
		ZoneID: cloudflare.F(zoneID),
	})
	if err != nil && !strings.Contains(err.Error(), "not found") {
		return err
	}
	return nil
}

// deleteAccessApplication removes an Access application by ID.
func deleteAccessApplication(ctx context.Context, client *cloudflare.Client, accountID, appID string) error {
	_, err := client.ZeroTrust.Access.Applications.Delete(ctx, appID, zero_trust.AccessApplicationDeleteParams{
		AccountID: cloudflare.F(accountID),
	})
	if err != nil && !strings.Contains(err.Error(), "not found") {
		return err
	}
	return nil
}

// deleteAccessTag removes an Access tag by name.
func deleteAccessTag(ctx context.Context, client *cloudflare.Client, accountID, tagName string) error {
	_, err := client.ZeroTrust.Access.Tags.Delete(ctx, tagName, zero_trust.AccessTagDeleteParams{
		AccountID: cloudflare.F(accountID),
	})
	if err != nil && !strings.Contains(err.Error(), "not found") {
		return err
	}
	return nil
}

// deleteServiceToken removes a service token by ID.
func deleteServiceToken(ctx context.Context, client *cloudflare.Client, accountID, tokenID string) error {
	_, err := client.ZeroTrust.Access.ServiceTokens.Delete(ctx, tokenID, zero_trust.AccessServiceTokenDeleteParams{
		AccountID: cloudflare.F(accountID),
	})
	if err != nil && !strings.Contains(err.Error(), "not found") {
		return err
	}
	return nil
}

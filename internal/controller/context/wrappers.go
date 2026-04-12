package context

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
)

// -----------------------------------------------------------------------------
// TunnelContext
// -----------------------------------------------------------------------------

// TunnelContext wraps CloudflareTunnel with computed state and helper methods.
// It provides a clean interface for reconcilers to work with tunnels without
// repeatedly computing derived values.
//
// Note: DNS management is handled separately by CloudflareDNS CRD.
// TunnelContext is tunnel-only and does not manage DNS records.
type TunnelContext struct {
	// Embedded tunnel resource
	*cfgatev1alpha1.CloudflareTunnel

	// Computed fields (populated by NewTunnelContext)
	resolvedAccountID string
	tunnelClient      cloudflare.Client

	// Logger for this context
	log logr.Logger
}

// NewTunnelContext creates a TunnelContext with resolved clients.
// Returns error if credentials cannot be resolved.
func NewTunnelContext(
	tunnel *cfgatev1alpha1.CloudflareTunnel,
	accountID string,
	tunnelClient cloudflare.Client,
) *TunnelContext {
	log := ctrl.Log.WithName("context").WithName("tunnel").
		WithValues("tunnel", tunnel.Namespace+"/"+tunnel.Name)

	return &TunnelContext{
		CloudflareTunnel:  tunnel,
		resolvedAccountID: accountID,
		tunnelClient:      tunnelClient,
		log:               log,
	}
}

// AccountID returns the resolved Cloudflare account ID.
func (tc *TunnelContext) AccountID() string {
	return tc.resolvedAccountID
}

// TunnelClient returns the tunnel API client.
func (tc *TunnelContext) TunnelClient() cloudflare.Client {
	return tc.tunnelClient
}

// -----------------------------------------------------------------------------
// DNSContext
// -----------------------------------------------------------------------------

// DNSContext wraps CloudflareDNS with resolved dependencies.
// It provides a clean interface for the DNSReconciler to manage DNS records
// independent of the tunnel reconciler.
type DNSContext struct {
	// Embedded DNS resource
	*cfgatev1alpha1.CloudflareDNS

	// Resolved tunnel reference (nil if using externalTarget)
	resolvedTunnel *cfgatev1alpha1.CloudflareTunnel
	tunnelDomain   string // Cached from tunnel status

	// Resolved zones (name -> zone ID mapping)
	resolvedZones map[string]string

	// DNS client for API operations
	dnsClient *cloudflare.DNSService

	// Logger for this context
	log logr.Logger
}

// NewDNSContext creates a DNSContext with resolved tunnel and zones.
// Returns error if the tunnel cannot be resolved (when tunnelRef is specified).
func NewDNSContext(
	ctx context.Context,
	dns *cfgatev1alpha1.CloudflareDNS,
	k8sClient client.Client,
	dnsClient *cloudflare.DNSService,
) (*DNSContext, error) {
	log := ctrl.Log.WithName("context").WithName("dns").
		WithValues("dns", dns.Namespace+"/"+dns.Name)

	dc := &DNSContext{
		CloudflareDNS: dns,
		resolvedZones: make(map[string]string),
		dnsClient:     dnsClient,
		log:           log,
	}

	// Resolve tunnel reference if specified
	if dns.Spec.TunnelRef != nil {
		tunnelNamespace := dns.Spec.TunnelRef.Namespace
		if tunnelNamespace == "" {
			tunnelNamespace = dns.Namespace
		}

		var tunnel cfgatev1alpha1.CloudflareTunnel
		tunnelKey := types.NamespacedName{
			Namespace: tunnelNamespace,
			Name:      dns.Spec.TunnelRef.Name,
		}
		if err := k8sClient.Get(ctx, tunnelKey, &tunnel); err != nil {
			if apierrors.IsNotFound(err) {
				log.V(1).Info("referenced tunnel not found", "tunnel", tunnelKey)
				return nil, fmt.Errorf("tunnel %s not found", tunnelKey)
			}
			return nil, fmt.Errorf("fetching tunnel: %w", err)
		}

		// Check tunnel has required status fields
		if tunnel.Status.TunnelDomain == "" {
			log.V(1).Info("tunnel domain not yet available", "tunnel", tunnelKey)
			return nil, fmt.Errorf("tunnel %s domain not ready", tunnelKey)
		}

		dc.resolvedTunnel = &tunnel
		dc.tunnelDomain = tunnel.Status.TunnelDomain
	} else if dns.Spec.ExternalTarget != nil {
		// External target - use the value directly as the target
		dc.tunnelDomain = dns.Spec.ExternalTarget.Value
	}

	// Resolve zones to IDs
	for _, zone := range dns.Spec.Zones {
		if zone.ID != "" {
			dc.resolvedZones[zone.Name] = zone.ID
		} else {
			// Zone ID will be resolved lazily during sync
			dc.resolvedZones[zone.Name] = ""
		}
	}

	return dc, nil
}

// ResolvedTunnel returns the resolved CloudflareTunnel (nil if using externalTarget).
func (dc *DNSContext) ResolvedTunnel() *cfgatev1alpha1.CloudflareTunnel {
	return dc.resolvedTunnel
}

// TunnelDomain returns the tunnel's CNAME target domain (e.g., {tunnelId}.cfargotunnel.com)
// or the external target value.
func (dc *DNSContext) TunnelDomain() string {
	return dc.tunnelDomain
}

// TunnelName returns the tunnel's name (empty if using externalTarget).
func (dc *DNSContext) TunnelName() string {
	if dc.resolvedTunnel == nil {
		return ""
	}
	return dc.resolvedTunnel.Spec.Tunnel.Name
}

// TunnelNamespacedName returns the tunnel's namespaced name.
// Returns empty NamespacedName if using externalTarget.
func (dc *DNSContext) TunnelNamespacedName() types.NamespacedName {
	if dc.resolvedTunnel == nil {
		return types.NamespacedName{}
	}
	return types.NamespacedName{
		Namespace: dc.resolvedTunnel.Namespace,
		Name:      dc.resolvedTunnel.Name,
	}
}

// HasTunnelRef returns true if this DNS context uses a tunnel reference.
func (dc *DNSContext) HasTunnelRef() bool {
	return dc.Spec.TunnelRef != nil
}

// HasExternalTarget returns true if this DNS context uses an external target.
func (dc *DNSContext) HasExternalTarget() bool {
	return dc.Spec.ExternalTarget != nil
}

// ResolvedZones returns the zone name to ID mapping.
// IDs may be empty if not pre-configured and not yet resolved.
func (dc *DNSContext) ResolvedZones() map[string]string {
	return dc.resolvedZones
}

// SetResolvedZoneID updates the zone ID after API lookup.
func (dc *DNSContext) SetResolvedZoneID(zoneName, zoneID string) {
	dc.resolvedZones[zoneName] = zoneID
}

// GetZoneID returns the zone ID for a zone name.
// Returns empty string if not resolved.
func (dc *DNSContext) GetZoneID(zoneName string) string {
	return dc.resolvedZones[zoneName]
}

// ZoneForHostname returns the matching zone for a hostname.
// Matches at DNS label boundaries: "app.example.com" matches zone "example.com"
// but "badexample.com" does not.
func (dc *DNSContext) ZoneForHostname(hostname string) (string, bool) {
	for _, zone := range dc.Spec.Zones {
		if hostname == zone.Name || strings.HasSuffix(hostname, "."+zone.Name) {
			return zone.Name, true
		}
	}
	return "", false
}

// HasGatewayRoutesEnabled returns true if Gateway API routes are a hostname source.
func (dc *DNSContext) HasGatewayRoutesEnabled() bool {
	return dc.Spec.Source.GatewayRoutes != nil && dc.Spec.Source.GatewayRoutes.Enabled
}

// GetAnnotationFilter returns the annotation filter for route selection.
func (dc *DNSContext) GetAnnotationFilter() string {
	if dc.Spec.Source.GatewayRoutes == nil {
		return ""
	}
	return dc.Spec.Source.GatewayRoutes.AnnotationFilter
}

// GetExplicitHostnames returns explicitly configured hostnames.
func (dc *DNSContext) GetExplicitHostnames() []cfgatev1alpha1.DNSExplicitHostname {
	return dc.Spec.Source.Explicit
}

// GetDefaultProxied returns the default proxied setting.
func (dc *DNSContext) GetDefaultProxied() bool {
	return dc.Spec.Defaults.Proxied
}

// GetDefaultTTL returns the default TTL (1 for auto, or explicit value).
func (dc *DNSContext) GetDefaultTTL() int32 {
	if dc.Spec.Defaults.TTL == 0 {
		return 1 // auto
	}
	return dc.Spec.Defaults.TTL
}

// GetPolicy returns the DNS policy (defaults to sync).
func (dc *DNSContext) GetPolicy() cfgatev1alpha1.DNSPolicy {
	if dc.Spec.Policy == "" {
		return cfgatev1alpha1.DNSPolicySync
	}
	return dc.Spec.Policy
}

// GetOwnerID returns the owner identifier for TXT records.
// Defaults to namespace/name if not explicitly configured.
func (dc *DNSContext) GetOwnerID() string {
	if dc.Spec.Ownership.OwnerID != "" {
		return dc.Spec.Ownership.OwnerID
	}
	return dc.Namespace + "/" + dc.Name
}

// GetOwnershipPrefix returns the TXT ownership record prefix.
func (dc *DNSContext) GetOwnershipPrefix() string {
	if dc.Spec.Ownership.TXTRecord.Prefix == "" {
		return "_cfgate"
	}
	return dc.Spec.Ownership.TXTRecord.Prefix
}

// ShouldCreateTXTRecords returns true if TXT ownership tracking is enabled.
func (dc *DNSContext) ShouldCreateTXTRecords() bool {
	if dc.Spec.Ownership.TXTRecord.Enabled == nil {
		return true // default enabled
	}
	return *dc.Spec.Ownership.TXTRecord.Enabled
}

// ShouldDeleteOnRouteRemoval returns true if records should be deleted when routes are removed.
// nil defaults to true (delete by default).
func (dc *DNSContext) ShouldDeleteOnRouteRemoval() bool {
	if dc.Spec.CleanupPolicy.DeleteOnRouteRemoval == nil {
		return true // default
	}
	return *dc.Spec.CleanupPolicy.DeleteOnRouteRemoval
}

// ShouldDeleteOnResourceRemoval returns true if records should be deleted when CloudflareDNS is deleted.
// nil defaults to true (delete by default).
func (dc *DNSContext) ShouldDeleteOnResourceRemoval() bool {
	if dc.Spec.CleanupPolicy.DeleteOnResourceRemoval == nil {
		return true // default
	}
	return *dc.Spec.CleanupPolicy.DeleteOnResourceRemoval
}

// OnlyDeleteManaged returns true if only cfgate-managed records should be cleaned up.
// nil defaults to true (only managed by default).
func (dc *DNSContext) OnlyDeleteManaged() bool {
	if dc.Spec.CleanupPolicy.OnlyManaged == nil {
		return true // default
	}
	return *dc.Spec.CleanupPolicy.OnlyManaged
}

// DNSClient returns the DNS API client.
func (dc *DNSContext) DNSClient() *cloudflare.DNSService {
	return dc.dnsClient
}

// -----------------------------------------------------------------------------
// AccessPolicyContext
// -----------------------------------------------------------------------------

// AccessPolicyContext wraps CloudflareAccessPolicy with resolved targets and
// helper methods for reconciliation.
type AccessPolicyContext struct {
	// Embedded policy resource
	*cfgatev1alpha1.CloudflareAccessPolicy

	// Computed fields (populated by NewAccessPolicyContext)
	resolvedTargets []TargetInfo

	// Logger for this context
	log logr.Logger
}

// NewAccessPolicyContext creates an AccessPolicyContext with resolved targets.
// Target resolution errors are captured in TargetInfo.Error (partial resolution).
func NewAccessPolicyContext(
	ctx context.Context,
	policy *cfgatev1alpha1.CloudflareAccessPolicy,
	k8sClient client.Client,
) *AccessPolicyContext {
	log := ctrl.Log.WithName("context").WithName("accesspolicy").
		WithValues("policy", policy.Namespace+"/"+policy.Name)

	// Resolve targets
	targets := resolveTargets(ctx, policy, k8sClient, log)
	log.V(1).Info("resolved targets",
		"total", len(targets),
		"resolved", countResolved(targets),
		"failed", countFailed(targets),
	)

	return &AccessPolicyContext{
		CloudflareAccessPolicy: policy,
		resolvedTargets:        targets,
		log:                    log,
	}
}

// GetTargetRefs returns all target references (merged from targetRef and targetRefs).
func (apc *AccessPolicyContext) GetTargetRefs() []cfgatev1alpha1.PolicyTargetReference {
	var refs []cfgatev1alpha1.PolicyTargetReference

	if apc.Spec.TargetRef != nil {
		refs = append(refs, *apc.Spec.TargetRef)
	}
	refs = append(refs, apc.Spec.TargetRefs...)

	return refs
}

// ResolvedTargets returns all resolved target info.
func (apc *AccessPolicyContext) ResolvedTargets() []TargetInfo {
	return apc.resolvedTargets
}

// SuccessfullyResolvedTargets returns only targets that resolved without error.
func (apc *AccessPolicyContext) SuccessfullyResolvedTargets() []TargetInfo {
	var resolved []TargetInfo
	for _, t := range apc.resolvedTargets {
		if t.Resolved && t.Error == nil {
			resolved = append(resolved, t)
		}
	}
	return resolved
}

// FailedTargets returns targets that failed to resolve.
func (apc *AccessPolicyContext) FailedTargets() []TargetInfo {
	var failed []TargetInfo
	for _, t := range apc.resolvedTargets {
		if t.Error != nil {
			failed = append(failed, t)
		}
	}
	return failed
}

// HasFailedTargets returns true if any target failed to resolve.
func (apc *AccessPolicyContext) HasFailedTargets() bool {
	for _, t := range apc.resolvedTargets {
		if t.Error != nil {
			return true
		}
	}
	return false
}

// AllTargetsResolved returns true if all targets resolved successfully.
func (apc *AccessPolicyContext) AllTargetsResolved() bool {
	if len(apc.resolvedTargets) == 0 {
		return false
	}
	for _, t := range apc.resolvedTargets {
		if !t.Resolved || t.Error != nil {
			return false
		}
	}
	return true
}

// RequiresServiceTokens returns true if service tokens are configured.
func (apc *AccessPolicyContext) RequiresServiceTokens() bool {
	return len(apc.Spec.ServiceTokens) > 0
}

// HasCrossNamespaceTargets returns true if any targets are in different namespace.
func (apc *AccessPolicyContext) HasCrossNamespaceTargets() bool {
	for _, t := range apc.resolvedTargets {
		if t.Namespace != apc.Namespace {
			return true
		}
	}
	return false
}

// ExtractHostnames extracts unique hostnames from resolved HTTPRoute and Gateway targets.
func (apc *AccessPolicyContext) ExtractHostnames(
	ctx context.Context,
	k8sClient client.Client,
) ([]string, error) {
	hostnameSet := make(map[string]struct{})

	for _, target := range apc.SuccessfullyResolvedTargets() {
		hostnames, err := extractHostnamesFromTarget(ctx, k8sClient, target)
		if err != nil {
			apc.log.V(1).Info("failed to extract hostnames",
				"target", target.Namespace+"/"+target.Name,
				"kind", target.Kind,
				"error", err.Error(),
			)
			continue
		}
		for _, h := range hostnames {
			hostnameSet[h] = struct{}{}
		}
	}

	var hostnames []string
	for h := range hostnameSet {
		hostnames = append(hostnames, h)
	}
	sort.Strings(hostnames)
	return hostnames, nil
}

// -----------------------------------------------------------------------------
// TargetInfo
// -----------------------------------------------------------------------------

// TargetInfo holds information about a resolved policy target.
type TargetInfo struct {
	// Kind of the target resource (HTTPRoute, Gateway, etc.)
	Kind string

	// Namespace of the target resource
	Namespace string

	// Name of the target resource
	Name string

	// Resolved indicates if the target was successfully resolved
	Resolved bool

	// SectionName targets specific listener (Gateway) or rule (Route)
	SectionName *string

	// Error contains the resolution error (nil if Resolved=true)
	Error error
}

// NamespacedName returns the namespaced name for k8s lookups.
func (ti *TargetInfo) NamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: ti.Namespace,
		Name:      ti.Name,
	}
}

// String returns a human-readable representation.
func (ti *TargetInfo) String() string {
	base := fmt.Sprintf("%s/%s/%s", ti.Kind, ti.Namespace, ti.Name)
	if ti.SectionName != nil {
		base += "/" + *ti.SectionName
	}
	return base
}

// IsHTTPRoute returns true if target is an HTTPRoute.
func (ti *TargetInfo) IsHTTPRoute() bool {
	return ti.Kind == "HTTPRoute"
}

// IsGateway returns true if target is a Gateway.
func (ti *TargetInfo) IsGateway() bool {
	return ti.Kind == "Gateway"
}

// -----------------------------------------------------------------------------
// Builder Functions
// -----------------------------------------------------------------------------

// BuildTunnelContext creates a TunnelContext with full initialization.
// Returns nil and logs warning if tunnel not found.
//
// Note: DNS is handled separately by CloudflareDNS CRD and its reconciler.
func BuildTunnelContext(
	ctx context.Context,
	k8sClient client.Client,
	cfClient cloudflare.Client,
	ref types.NamespacedName,
	accountID string,
) (*TunnelContext, error) {
	log := ctrl.Log.WithName("context").WithValues("tunnel", ref)

	// Fetch tunnel
	var tunnel cfgatev1alpha1.CloudflareTunnel
	if err := k8sClient.Get(ctx, ref, &tunnel); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("tunnel not found")
			return nil, nil
		}
		return nil, fmt.Errorf("fetching tunnel: %w", err)
	}

	return NewTunnelContext(&tunnel, accountID, cfClient), nil
}

// BuildDNSContext creates a DNSContext with resolved tunnel and zones.
// Returns nil if DNS resource not found.
func BuildDNSContext(
	ctx context.Context,
	k8sClient client.Client,
	dnsClient *cloudflare.DNSService,
	ref types.NamespacedName,
) (*DNSContext, error) {
	log := ctrl.Log.WithName("context").WithValues("dns", ref)

	// Fetch DNS resource
	var dns cfgatev1alpha1.CloudflareDNS
	if err := k8sClient.Get(ctx, ref, &dns); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("dns resource not found")
			return nil, nil
		}
		return nil, fmt.Errorf("fetching dns: %w", err)
	}

	// Build context (resolves tunnel reference if specified)
	return NewDNSContext(ctx, &dns, k8sClient, dnsClient)
}

// BuildAccessPolicyContext creates an AccessPolicyContext with full initialization.
func BuildAccessPolicyContext(
	ctx context.Context,
	k8sClient client.Client,
	ref types.NamespacedName,
) (*AccessPolicyContext, error) {
	log := ctrl.Log.WithName("context").WithValues("policy", ref)

	// Fetch policy
	var policy cfgatev1alpha1.CloudflareAccessPolicy
	if err := k8sClient.Get(ctx, ref, &policy); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("policy not found")
			return nil, nil
		}
		return nil, fmt.Errorf("fetching policy: %w", err)
	}

	// Build context
	return NewAccessPolicyContext(ctx, &policy, k8sClient), nil
}

// -----------------------------------------------------------------------------
// Helper Functions
// -----------------------------------------------------------------------------

// resolveTargets resolves all targetRefs to TargetInfo.
func resolveTargets(
	ctx context.Context,
	policy *cfgatev1alpha1.CloudflareAccessPolicy,
	k8sClient client.Client,
	log logr.Logger,
) []TargetInfo {
	var targets []TargetInfo

	// Handle single targetRef
	if policy.Spec.TargetRef != nil {
		target := resolveTarget(ctx, policy.Spec.TargetRef, policy.Namespace, k8sClient, log)
		targets = append(targets, target)
	}

	// Handle multiple targetRefs
	for i := range policy.Spec.TargetRefs {
		target := resolveTarget(ctx, &policy.Spec.TargetRefs[i], policy.Namespace, k8sClient, log)
		targets = append(targets, target)
	}

	return targets
}

// resolveTarget resolves a single PolicyTargetReference.
func resolveTarget(
	ctx context.Context,
	ref *cfgatev1alpha1.PolicyTargetReference,
	defaultNamespace string,
	k8sClient client.Client,
	log logr.Logger,
) TargetInfo {
	namespace := defaultNamespace
	if ref.Namespace != nil && *ref.Namespace != "" {
		namespace = *ref.Namespace
	}

	info := TargetInfo{
		Kind:        ref.Kind,
		Namespace:   namespace,
		Name:        ref.Name,
		SectionName: ref.SectionName,
		Resolved:    false,
	}

	// Check if target exists
	exists, err := targetExists(ctx, k8sClient, ref.Kind, namespace, ref.Name)
	if err != nil {
		log.V(1).Info("target resolution failed",
			"kind", ref.Kind,
			"namespace", namespace,
			"name", ref.Name,
			"error", err.Error(),
		)
		info.Error = err
		return info
	}

	if !exists {
		info.Error = fmt.Errorf("target %s/%s not found", namespace, ref.Name)
		return info
	}

	// Check ReferenceGrant if cross-namespace
	if namespace != defaultNamespace {
		granted, err := checkReferenceGrant(ctx, k8sClient, defaultNamespace, namespace, ref.Kind)
		if err != nil {
			info.Error = fmt.Errorf("checking ReferenceGrant: %w", err)
			return info
		}
		if !granted {
			info.Error = fmt.Errorf("cross-namespace reference to %s/%s not permitted by ReferenceGrant",
				namespace, ref.Name)
			return info
		}
	}

	info.Resolved = true
	return info
}

// targetExists checks if a target resource exists.
func targetExists(
	ctx context.Context,
	k8sClient client.Client,
	kind, namespace, name string,
) (bool, error) {
	var obj client.Object

	switch kind {
	case "Gateway":
		obj = &gwapiv1.Gateway{}
	case "HTTPRoute":
		obj = &gwapiv1.HTTPRoute{}
	default:
		return false, fmt.Errorf("unsupported target kind: %s", kind)
	}

	key := types.NamespacedName{Namespace: namespace, Name: name}
	if err := k8sClient.Get(ctx, key, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// checkReferenceGrant checks if cross-namespace reference is permitted.
func checkReferenceGrant(
	ctx context.Context,
	k8sClient client.Client,
	fromNamespace, toNamespace, targetKind string,
) (bool, error) {
	var grants gwapiv1b1.ReferenceGrantList
	if err := k8sClient.List(ctx, &grants, client.InNamespace(toNamespace)); err != nil {
		return false, err
	}

	for _, grant := range grants.Items {
		for _, from := range grant.Spec.From {
			if from.Group != "cfgate.io" {
				continue
			}
			if from.Kind != "CloudflareAccessPolicy" {
				continue
			}
			// Namespace is a required string type in ReferenceGrantFrom
			if string(from.Namespace) != fromNamespace {
				continue
			}

			// Check if target kind is permitted
			for _, to := range grant.Spec.To {
				if to.Group == "gateway.networking.k8s.io" &&
					string(to.Kind) == targetKind {
					return true, nil
				}
			}
		}
	}

	return false, nil
}

// extractHostnamesFromTarget extracts hostnames from a resolved target.
func extractHostnamesFromTarget(
	ctx context.Context,
	k8sClient client.Client,
	target TargetInfo,
) ([]string, error) {
	switch target.Kind {
	case "HTTPRoute":
		var route gwapiv1.HTTPRoute
		if err := k8sClient.Get(ctx, target.NamespacedName(), &route); err != nil {
			return nil, err
		}
		hostnames := make([]string, len(route.Spec.Hostnames))
		for i, h := range route.Spec.Hostnames {
			hostnames[i] = string(h)
		}
		return hostnames, nil

	case "Gateway":
		var gw gwapiv1.Gateway
		if err := k8sClient.Get(ctx, target.NamespacedName(), &gw); err != nil {
			return nil, err
		}
		// Extract hostnames from listeners
		var hostnames []string
		for _, listener := range gw.Spec.Listeners {
			if listener.Hostname != nil {
				hostnames = append(hostnames, string(*listener.Hostname))
			}
		}
		return hostnames, nil

	default:
		return nil, fmt.Errorf("unsupported target kind: %s", target.Kind)
	}
}

// countResolved counts targets that resolved successfully.
func countResolved(targets []TargetInfo) int {
	count := 0
	for _, t := range targets {
		if t.Resolved && t.Error == nil {
			count++
		}
	}
	return count
}

// countFailed counts targets that failed to resolve.
func countFailed(targets []TargetInfo) int {
	count := 0
	for _, t := range targets {
		if t.Error != nil {
			count++
		}
	}
	return count
}

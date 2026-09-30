package controller

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/controller/annotations"
	"cfgate.io/cfgate/internal/controller/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gateway "sigs.k8s.io/gateway-api/apis/v1"
)

type accessApplicationRead struct {
	value *cloudflare.AccessApplication
	err   error
}
type accessPolicyRead struct {
	value *cloudflare.AccessPolicy
	err   error
}

// accessSyncSession is confined to one locked publication attempt. It is never
// shared across reconciliations; callers pass coordination dependencies explicitly.
type accessSyncSession struct {
	keys         map[string]bool
	reader       client.Reader
	client       cloudflare.Client
	account      string
	dependencies []cfg.TunnelAccessDependency
	remoteApps   []cloudflare.AccessApplication
	listed       bool
	listError    error
	applications map[string]accessApplicationRead
	policies     map[string]accessPolicyRead
}

type directReadClient struct {
	client.Client
	reader client.Reader
}

func (c directReadClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return c.reader.Get(ctx, key, obj, opts...)
}
func (c directReadClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return c.reader.List(ctx, list, opts...)
}

func accessRequiredReference(route *gateway.HTTPRoute) (types.NamespacedName, bool, error) {
	value, required := route.Annotations[annotations.AnnotationAccessRequired]
	if !required {
		return types.NamespacedName{}, false, nil
	}
	parts := strings.Split(value, "/")
	if len(parts) != 2 || len(validation.IsDNS1123Label(parts[0])) > 0 || len(validation.IsDNS1123Subdomain(parts[1])) > 0 {
		return types.NamespacedName{}, true, fmt.Errorf("access-required must be explicit namespace/name")
	}
	return types.NamespacedName{Namespace: parts[0], Name: parts[1]}, true, nil
}

// accessRouteInventory reads each inventory once, independent of tunnel count.
func accessRouteInventory(ctx context.Context, reader client.Reader) (map[types.NamespacedName]map[string]bool, error) {
	var routes gateway.HTTPRouteList
	if err := reader.List(ctx, &routes); err != nil {
		return nil, err
	}
	requiredRoutes := make([]gateway.HTTPRoute, 0)
	for _, route := range routes.Items {
		if _, required, err := accessRequiredReference(&route); required && err == nil {
			requiredRoutes = append(requiredRoutes, route)
		}
	}
	inventory := map[types.NamespacedName]map[string]bool{}
	if len(requiredRoutes) == 0 {
		return inventory, nil
	}
	var gateways gateway.GatewayList
	if err := reader.List(ctx, &gateways); err != nil {
		return nil, err
	}
	parents := map[types.NamespacedName]types.NamespacedName{}
	for _, gw := range gateways.Items {
		ns, name, err := annotations.ParseNamespacedName(annotations.GetAnnotation(&gw, annotations.AnnotationTunnelRef), gw.Namespace)
		if err == nil {
			parents[client.ObjectKeyFromObject(&gw)] = types.NamespacedName{Namespace: ns, Name: name}
		}
	}
	for _, route := range requiredRoutes {
		ref, _, _ := accessRequiredReference(&route)
		for _, parent := range route.Spec.ParentRefs {
			if !isGatewayParentRef(parent) {
				continue
			}
			ns := route.Namespace
			if parent.Namespace != nil {
				ns = string(*parent.Namespace)
			}
			tunnel, found := parents[types.NamespacedName{Namespace: ns, Name: string(parent.Name)}]
			if !found {
				continue
			}
			if inventory[tunnel] == nil {
				inventory[tunnel] = map[string]bool{}
			}
			inventory[tunnel][ref.String()] = true
		}
	}
	return inventory, nil
}

func (r *CloudflareTunnelReconciler) accessKeysForTunnel(ctx context.Context, tunnel *cfg.CloudflareTunnel) ([]string, error) {
	inventory, err := accessRouteInventory(ctx, r.lifecycleReader())
	if err != nil {
		return nil, err
	}
	keys := inventory[client.ObjectKeyFromObject(tunnel)]
	if keys == nil {
		keys = map[string]bool{}
	}
	for _, dep := range tunnel.Status.AccessDependencies {
		keys[dep.Namespace+"/"+dep.Name] = true
	}
	if len(keys) > 256 {
		return nil, fmt.Errorf("more than 256 required Access applications in one tunnel")
	}
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out, nil
}

func (r *CloudflareTunnelReconciler) beginAccessSync(ctx context.Context, tunnel *cfg.CloudflareTunnel) (*accessSyncSession, func(), error) {
	if r.APIReader != nil {
		var current cfg.CloudflareTunnel
		if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(tunnel), &current); err != nil {
			return nil, nil, err
		}
		if current.UID != tunnel.UID {
			return nil, nil, fmt.Errorf("tunnel identity changed before Access synchronization")
		}
		tunnel.Status.AccessDependencies = current.Status.AccessDependencies
	}
	// Ordinary public-only installations need no additional uncached inventory reads.
	if len(tunnel.Status.AccessDependencies) == 0 {
		inventory, err := accessRouteInventory(ctx, r.Client)
		if err != nil {
			return nil, nil, err
		}
		if len(inventory[client.ObjectKeyFromObject(tunnel)]) == 0 {
			return nil, func() {}, nil
		}
	}
	keys, err := r.accessKeysForTunnel(ctx, tunnel)
	if err != nil {
		return nil, nil, err
	}
	if len(keys) == 0 {
		return nil, func() {}, nil
	}
	if r.AccessLocks == nil {
		return nil, nil, fmt.Errorf("required Access coordination is not initialized")
	}
	release, err := r.AccessLocks.acquire(ctx, keys)
	if err != nil {
		return nil, nil, err
	}
	current, err := r.accessKeysForTunnel(ctx, tunnel)
	if err != nil {
		release()
		return nil, nil, err
	}
	if !reflect.DeepEqual(keys, current) {
		release()
		return nil, nil, fmt.Errorf("required Access dependencies changed while awaiting publication")
	}
	state := &accessSyncSession{reader: r.lifecycleReader(), keys: make(map[string]bool), applications: make(map[string]accessApplicationRead), policies: make(map[string]accessPolicyRead)}
	for _, key := range keys {
		state.keys[key] = true
	}
	return state, release, nil
}

func currentReady(conditions []metav1.Condition, generation, observed int64) bool {
	if generation != observed {
		return false
	}
	for _, condition := range conditions {
		if condition.Type == status.ConditionTypeReady {
			return condition.Status == metav1.ConditionTrue && condition.ObservedGeneration == generation
		}
	}
	return false
}

func (r *CloudflareTunnelReconciler) requiredAccessAllows(ctx context.Context, state *accessSyncSession, tunnel *cfg.CloudflareTunnel, route *gateway.HTTPRoute, hosts []gateway.Hostname) error {
	ref, required, err := accessRequiredReference(route)
	if !required {
		return nil
	}
	if err != nil {
		return err
	}
	if state == nil {
		return fmt.Errorf("required Access publication has no coordination session")
	}
	if !state.keys[ref.String()] {
		return fmt.Errorf("required Access dependency changed after lock acquisition")
	}
	var app cfg.CloudflareAccessApplication
	if err := state.reader.Get(ctx, ref, &app); err != nil {
		return fmt.Errorf("required Access application: %w", err)
	}
	dep := cfg.TunnelAccessDependency{Namespace: ref.Namespace, Name: ref.Name, UID: string(app.UID)}
	for _, host := range hosts {
		dep.Hostnames = append(dep.Hostnames, string(host))
	}
	dependencyIndex := len(state.dependencies)
	state.dependencies = append(state.dependencies, dep)
	if err := requireReferenceGrant(ctx, state.reader, route.Namespace, gateway.GroupName, "HTTPRoute", app.Namespace, "cfgate.io", "CloudflareAccessApplication", app.Name); err != nil {
		return err
	}
	if !app.DeletionTimestamp.IsZero() || !currentReady(app.Status.Conditions, app.Generation, app.Status.ObservedGeneration) {
		return fmt.Errorf("required Access application is not current and ready")
	}
	if app.Spec.Application.Type != "" && app.Spec.Application.Type != "self_hosted" {
		return fmt.Errorf("required Access-required supports self_hosted applications only")
	}
	if app.Spec.Application.Path != "" && app.Spec.Application.Path != "/" {
		return fmt.Errorf("required Access-required requires whole-host root protection")
	}
	if app.Spec.Application.OptionsPreflightBypass {
		return fmt.Errorf("required Access-required rejects OPTIONS preflight bypass")
	}
	if err := r.requiredAccessTargetPermitted(ctx, state.reader, &app, route); err != nil {
		return err
	}
	if err := knownGRPCBackend(ctx, state.reader, route); err != nil {
		return err
	}
	if state.client == nil {
		state.client, err = r.getCloudflareClient(ctx, tunnel)
		if err != nil {
			return err
		}
		state.account, err = r.resolveAccountID(ctx, state.client, tunnel)
		if err != nil {
			return err
		}
	}
	if app.Status.AccountID != state.account {
		return fmt.Errorf("required Access application account does not match tunnel")
	}
	links := map[string]bool{}
	for _, ref := range app.Spec.PolicyRefs {
		ns := ref.Namespace
		if ns == "" {
			ns = app.Namespace
		}
		if err := requireReferenceGrant(ctx, state.reader, app.Namespace, "cfgate.io", "CloudflareAccessApplication", ns, "cfgate.io", "CloudflareAccessPolicy", ref.Name); err != nil {
			return err
		}
		var policy cfg.CloudflareAccessPolicy
		if err := state.reader.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &policy); err != nil {
			return err
		}
		state.dependencies[dependencyIndex].Policies = append(state.dependencies[dependencyIndex].Policies, cfg.TunnelAccessPolicyDependency{Namespace: ns, Name: ref.Name, UID: string(policy.UID), PolicyID: policy.Status.PolicyID})
		if !policy.DeletionTimestamp.IsZero() || !currentReady(policy.Status.Conditions, policy.Generation, policy.Status.ObservedGeneration) || policy.Status.AccountID != state.account || policy.Status.PolicyID == "" {
			return fmt.Errorf("required Access policy is not current, ready and in the tunnel account")
		}
		remote, err := state.readPolicy(ctx, policy.Status.PolicyID)
		if err != nil {
			return err
		}
		if remote == nil || remote.ID != policy.Status.PolicyID || !protectedPolicyDecision(remote.Decision) || remote.Decision != policy.Spec.Decision {
			return fmt.Errorf("remote Access policy is absent, bypassed or unsupported")
		}
		if len(remote.Include) == 0 {
			return fmt.Errorf("remote Access policy has no supported Include selectors")
		}
		if remote.Decision == "allow" {
			for _, rule := range remote.Include {
				if rule.Everyone != nil && *rule.Everyone {
					return fmt.Errorf("required Access-required rejects Allow Include Everyone")
				}
			}
		}
		links[remote.ID] = true
	}
	if len(links) == 0 {
		return fmt.Errorf("required Access application has no verified policies")
	}
	if !state.listed {
		state.listed = true
		state.remoteApps, state.listError = state.client.ListAccessApplications(ctx, state.account)
	}
	if state.listError != nil {
		return state.listError
	}
	for _, hostValue := range hosts {
		host := string(hostValue)
		if strings.Contains(host, "*") || len(validation.IsDNS1123Subdomain(host)) > 0 {
			return fmt.Errorf("required Access-required supports exact hostnames only")
		}
		remoteID := ""
		for _, observed := range app.Status.Applications {
			if strings.TrimSuffix(observed.Domain, "/") == host {
				remoteID = observed.ID
				break
			}
		}
		if remoteID == "" {
			return fmt.Errorf("required Access application does not cover hostname %s at root", host)
		}
		remote, err := state.readApplication(ctx, remoteID)
		if err != nil {
			return err
		}
		if remote == nil || remote.ID != remoteID || remote.Type != "self_hosted" || remote.UnsupportedProtection || remote.OptionsPreflightBypass || strings.TrimSuffix(remote.Domain, "/") != host {
			return fmt.Errorf("remote Access application does not provide supported root protection")
		}
		if len(remote.Destinations) != 1 || strings.TrimSuffix(remote.Destinations[0], "/") != host {
			return fmt.Errorf("remote Access destinations do not match root hostname")
		}
		if len(remote.Policies) != len(links) {
			return fmt.Errorf("remote Access policy attachments changed")
		}
		seenLinks := make(map[string]bool, len(remote.Policies))
		for _, link := range remote.Policies {
			if !links[link.ID] || seenLinks[link.ID] {
				return fmt.Errorf("remote Access contains unverified or duplicated policy")
			}
			seenLinks[link.ID] = true
		}
		for _, other := range state.remoteApps {
			if other.ID == remoteID {
				continue
			}
			if other.UnsupportedProtection {
				return fmt.Errorf("another Access application has unsupported destination semantics")
			}
			destinations := append([]string{other.Domain}, other.Destinations...)
			for _, destination := range destinations {
				if accessHostnameOverlaps(destination, host) {
					return fmt.Errorf("another Access application may shadow required hostname %s", host)
				}
			}
		}
	}
	return nil
}

func protectedPolicyDecision(decision string) bool {
	return decision == "allow" || decision == "deny" || decision == "non_identity"
}
func accessHostnameOverlaps(destination, host string) bool {
	pattern := strings.SplitN(destination, "/", 2)[0]
	if pattern == "" {
		return false
	}
	expr := "^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), "\\*", ".*") + "$"
	matched, _ := regexp.MatchString(expr, host)
	return matched
}

func (r *CloudflareTunnelReconciler) requiredAccessTargetPermitted(ctx context.Context, reader client.Reader, app *cfg.CloudflareAccessApplication, route *gateway.HTTPRoute) error {
	for _, ref := range accessApplicationTargetRefs(app) {
		ns := app.Namespace
		if ref.Namespace != nil && *ref.Namespace != "" {
			ns = *ref.Namespace
		}
		if ref.Group != "" && ref.Group != gateway.GroupName {
			continue
		}
		if ref.SectionName != nil && *ref.SectionName != "" {
			continue
		}
		target := ref.Kind == "HTTPRoute" && ns == route.Namespace && ref.Name == route.Name
		if ref.Kind == "Gateway" {
			for _, parent := range route.Spec.ParentRefs {
				parentNS := route.Namespace
				if parent.Namespace != nil {
					parentNS = string(*parent.Namespace)
				}
				if isGatewayParentRef(parent) && ns == parentNS && ref.Name == string(parent.Name) {
					target = true
				}
			}
		}
		if target {
			return requireReferenceGrant(ctx, reader, app.Namespace, "cfgate.io", "CloudflareAccessApplication", ns, gateway.GroupName, ref.Kind, ref.Name)
		}
	}
	return fmt.Errorf("required Access application does not target this route or its Gateway")
}

func knownGRPCBackend(ctx context.Context, reader client.Reader, route *gateway.HTTPRoute) error {
	for _, rule := range route.Spec.Rules {
		for _, backend := range rule.BackendRefs {
			if backend.Weight != nil && *backend.Weight == 0 {
				continue
			}
			ns := route.Namespace
			if backend.Namespace != nil {
				ns = string(*backend.Namespace)
			}
			var service corev1.Service
			if err := reader.Get(ctx, types.NamespacedName{Namespace: ns, Name: string(backend.Name)}, &service); err != nil {
				return fmt.Errorf("verify backend protocol for Access-required route: %w", err)
			}
			backendPort := int32(80)
			if backend.Port != nil {
				backendPort = int32(*backend.Port)
			}
			for _, port := range service.Spec.Ports {
				if backendPort == port.Port && port.AppProtocol != nil {
					if *port.AppProtocol == "grpc" || *port.AppProtocol == "grpcs" {
						return fmt.Errorf("required Access routing does not authenticate gRPC; use origin authentication")
					}
				}
			}
		}
	}
	return nil
}

// Remote observations are reused only within one locked configuration attempt.
func (s *accessSyncSession) readApplication(ctx context.Context, id string) (*cloudflare.AccessApplication, error) {
	result, found := s.applications[id]
	if !found {
		result.value, result.err = s.client.GetAccessApplication(ctx, s.account, id)
		s.applications[id] = result
	}
	return result.value, result.err
}
func (s *accessSyncSession) readPolicy(ctx context.Context, id string) (*cloudflare.AccessPolicy, error) {
	result, found := s.policies[id]
	if !found {
		result.value, result.err = s.client.GetAccessPolicy(ctx, s.account, id)
		s.policies[id] = result
	}
	return result.value, result.err
}

package controller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	gateway "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/cloudflared"
)

const (
	generatedOriginCASecretLabel = "cfgate.io/generated-origin-ca-pool"
)

type originRuntime struct {
	namedCAPoolPaths      map[string]string
	backendTLSCAPoolPaths map[types.NamespacedName]string
}

func (r *CloudflareTunnelReconciler) buildOriginRuntime(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel) (*originRuntime, error) {
	_, namedPaths, backendTLSPaths, err := r.resolveOriginCAPoolMounts(ctx, tunnel)
	if err != nil {
		return nil, err
	}
	if err := r.syncGeneratedOriginCASecrets(ctx, tunnel); err != nil {
		return nil, err
	}
	return &originRuntime{
		namedCAPoolPaths:      namedPaths,
		backendTLSCAPoolPaths: backendTLSPaths,
	}, nil
}

func (r *CloudflareTunnelReconciler) resolveOriginCAPoolMounts(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel) ([]cloudflared.OriginCAPoolMount, map[string]string, map[types.NamespacedName]string, error) {
	var mounts []cloudflared.OriginCAPoolMount
	namedPaths := make(map[string]string, len(tunnel.Spec.OriginCAPools))
	backendTLSPaths := map[types.NamespacedName]string{}

	for _, pool := range tunnel.Spec.OriginCAPools {
		refNS := tunnel.Namespace
		if pool.SecretRef.Namespace != nil && *pool.SecretRef.Namespace != "" {
			refNS = *pool.SecretRef.Namespace
		}
		key := pool.SecretRef.Key
		if key == "" {
			key = cloudflared.DefaultOriginCAPoolSecretKey
		}
		source := types.NamespacedName{Namespace: refNS, Name: pool.SecretRef.Name}
		if err := r.validateSecretKey(ctx, source, key, fmt.Sprintf("origin CA pool %q", pool.Name)); err != nil {
			return nil, nil, nil, err
		}
		secretName := pool.SecretRef.Name
		mountKey := key
		if refNS != tunnel.Namespace {
			ok, err := r.referenceGrantPermits(ctx, tunnel.Namespace, refNS, cfgatev1alpha1.GroupVersion.Group, "CloudflareTunnel", "", "Secret", pool.SecretRef.Name)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("checking ReferenceGrant for origin CA pool %q: %w", pool.Name, err)
			}
			if !ok {
				return nil, nil, nil, fmt.Errorf("origin CA pool %q Secret %s/%s requires ReferenceGrant", pool.Name, refNS, pool.SecretRef.Name)
			}
			secretName = generatedOriginCASecretName(tunnel.Name, "pool", refNS, pool.SecretRef.Name, key)
			mountKey = cloudflared.DefaultOriginCAPoolSecretKey
		}
		mountName := cloudflared.OriginCAPoolVolumeNameFor("pool", pool.Name)
		mounts = append(mounts, cloudflared.OriginCAPoolMount{
			Name:       mountName,
			SecretName: secretName,
			Key:        mountKey,
			MountPath:  strings.TrimSuffix(cloudflared.NamedOriginCAPoolPath(pool.Name), "/"+cloudflared.OriginCAPoolFileName),
		})
		namedPaths[pool.Name] = cloudflared.NamedOriginCAPoolPath(pool.Name)
	}

	var policies gateway.BackendTLSPolicyList
	if err := r.List(ctx, &policies); err != nil {
		return nil, nil, nil, fmt.Errorf("list BackendTLSPolicies: %w", err)
	}
	for _, policy := range policies.Items {
		if len(policy.Spec.Validation.CACertificateRefs) != 1 {
			continue
		}
		ref := policy.Spec.Validation.CACertificateRefs[0]
		if string(ref.Group) != "" || string(ref.Kind) != "ConfigMap" {
			continue
		}
		source := types.NamespacedName{Namespace: policy.Namespace, Name: string(ref.Name)}
		var cm corev1.ConfigMap
		if err := r.Get(ctx, source, &cm); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, nil, nil, fmt.Errorf("get BackendTLSPolicy CA ConfigMap %s/%s: %w", source.Namespace, source.Name, err)
		}
		if _, ok := cm.Data[cloudflared.DefaultOriginCAPoolSecretKey]; !ok {
			continue
		}
		key := types.NamespacedName{Namespace: policy.Namespace, Name: policy.Name}
		secretName := generatedOriginCASecretName(tunnel.Name, "backendtls", policy.Namespace, policy.Name, string(ref.Name))
		mounts = append(mounts, cloudflared.OriginCAPoolMount{
			Name:       cloudflared.OriginCAPoolVolumeNameFor("backendtls", policy.Namespace, policy.Name),
			SecretName: secretName,
			Key:        cloudflared.DefaultOriginCAPoolSecretKey,
			MountPath:  strings.TrimSuffix(cloudflared.BackendTLSCAPoolPath(policy.Namespace, policy.Name), "/"+cloudflared.OriginCAPoolFileName),
		})
		backendTLSPaths[key] = cloudflared.BackendTLSCAPoolPath(policy.Namespace, policy.Name)
	}
	sort.Slice(mounts, func(i, j int) bool { return mounts[i].Name < mounts[j].Name })
	return mounts, namedPaths, backendTLSPaths, nil
}

func (r *CloudflareTunnelReconciler) syncGeneratedOriginCASecrets(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel) error {
	desired := map[string]struct{}{}
	for _, pool := range tunnel.Spec.OriginCAPools {
		refNS := tunnel.Namespace
		if pool.SecretRef.Namespace != nil && *pool.SecretRef.Namespace != "" {
			refNS = *pool.SecretRef.Namespace
		}
		if refNS == tunnel.Namespace {
			continue
		}
		key := pool.SecretRef.Key
		if key == "" {
			key = cloudflared.DefaultOriginCAPoolSecretKey
		}
		sourceName := types.NamespacedName{Namespace: refNS, Name: pool.SecretRef.Name}
		var source corev1.Secret
		if err := r.Get(ctx, sourceName, &source); err != nil {
			return fmt.Errorf("get cross-namespace origin CA Secret %s/%s: %w", refNS, pool.SecretRef.Name, err)
		}
		targetName := generatedOriginCASecretName(tunnel.Name, "pool", refNS, pool.SecretRef.Name, key)
		desired[targetName] = struct{}{}
		if err := r.upsertGeneratedOriginCASecret(ctx, tunnel, targetName, source.Data[key]); err != nil {
			return err
		}
	}

	var policies gateway.BackendTLSPolicyList
	if err := r.List(ctx, &policies); err != nil {
		return fmt.Errorf("list BackendTLSPolicies: %w", err)
	}
	for _, policy := range policies.Items {
		if len(policy.Spec.Validation.CACertificateRefs) != 1 {
			continue
		}
		ref := policy.Spec.Validation.CACertificateRefs[0]
		if string(ref.Group) != "" || string(ref.Kind) != "ConfigMap" {
			continue
		}
		var cm corev1.ConfigMap
		if err := r.Get(ctx, types.NamespacedName{Namespace: policy.Namespace, Name: string(ref.Name)}, &cm); err != nil {
			continue
		}
		data, ok := cm.Data[cloudflared.DefaultOriginCAPoolSecretKey]
		if !ok {
			continue
		}
		targetName := generatedOriginCASecretName(tunnel.Name, "backendtls", policy.Namespace, policy.Name, string(ref.Name))
		desired[targetName] = struct{}{}
		if err := r.upsertGeneratedOriginCASecret(ctx, tunnel, targetName, []byte(data)); err != nil {
			return err
		}
	}

	var existing corev1.SecretList
	if err := r.List(ctx, &existing, client.InNamespace(tunnel.Namespace), client.MatchingLabels{
		generatedOriginCASecretLabel:   "true",
		"app.kubernetes.io/instance":   tunnel.Name,
		"app.kubernetes.io/managed-by": "cfgate",
		"app.kubernetes.io/component":  "origin-ca-pool",
	}); err != nil {
		return fmt.Errorf("list generated origin CA Secrets: %w", err)
	}
	for i := range existing.Items {
		secret := &existing.Items[i]
		if _, ok := desired[secret.Name]; ok {
			continue
		}
		if err := r.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete stale generated origin CA Secret %s/%s: %w", secret.Namespace, secret.Name, err)
		}
	}
	return nil
}

func (r *CloudflareTunnelReconciler) upsertGeneratedOriginCASecret(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel, name string, ca []byte) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: tunnel.Namespace,
			Labels: map[string]string{
				generatedOriginCASecretLabel:   "true",
				"app.kubernetes.io/name":       "cfgate-origin-ca-pool",
				"app.kubernetes.io/instance":   tunnel.Name,
				"app.kubernetes.io/managed-by": "cfgate",
				"app.kubernetes.io/component":  "origin-ca-pool",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{cloudflared.DefaultOriginCAPoolSecretKey: ca},
	}
	if err := controllerutil.SetControllerReference(tunnel, secret, r.Scheme); err != nil {
		return err
	}
	var existing corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			return r.Create(ctx, secret)
		}
		return err
	}
	existing.Labels = secret.Labels
	existing.OwnerReferences = secret.OwnerReferences
	existing.Type = secret.Type
	existing.Data = secret.Data
	return r.Update(ctx, &existing)
}

func (r *CloudflareTunnelReconciler) validateSecretKey(ctx context.Context, name types.NamespacedName, key, label string) error {
	var secret corev1.Secret
	if err := r.Get(ctx, name, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("%s Secret %s/%s not found", label, name.Namespace, name.Name)
		}
		return fmt.Errorf("get %s Secret %s/%s: %w", label, name.Namespace, name.Name, err)
	}
	if _, ok := secret.Data[key]; !ok {
		return fmt.Errorf("%s Secret %s/%s missing key %q", label, name.Namespace, name.Name, key)
	}
	return nil
}

func generatedOriginCASecretName(parts ...string) string {
	joined := strings.Join(parts, "-")
	sum := sha256.Sum256([]byte(joined))
	return fmt.Sprintf("cfgate-origin-ca-%x", sum[:8])
}

func (r *CloudflareTunnelReconciler) referenceGrantPermits(ctx context.Context, fromNamespace, toNamespace, fromGroup, fromKind, toGroup, toKind, toName string) (bool, error) {
	if fromNamespace == toNamespace {
		return true, nil
	}
	var grants gatewayv1beta1.ReferenceGrantList
	if err := r.List(ctx, &grants, client.InNamespace(toNamespace)); err != nil {
		return false, err
	}
	for _, grant := range grants.Items {
		fromOK := false
		for _, from := range grant.Spec.From {
			if string(from.Group) == fromGroup && string(from.Kind) == fromKind && string(from.Namespace) == fromNamespace {
				fromOK = true
				break
			}
		}
		if !fromOK {
			continue
		}
		for _, to := range grant.Spec.To {
			if string(to.Group) != toGroup || string(to.Kind) != toKind {
				continue
			}
			if to.Name == nil || string(*to.Name) == toName {
				return true, nil
			}
		}
	}
	return false, nil
}

func (r *CloudflareTunnelReconciler) originPolicyForRule(ctx context.Context, route *gateway.HTTPRoute, rule gateway.HTTPRouteRule) (*cfgatev1alpha1.CloudflareOriginPolicy, error) {
	if r == nil || r.Client == nil {
		return nil, nil
	}
	var policies cfgatev1alpha1.CloudflareOriginPolicyList
	if err := r.List(ctx, &policies); err != nil {
		return nil, fmt.Errorf("list CloudflareOriginPolicies: %w", err)
	}
	var matches []cfgatev1alpha1.CloudflareOriginPolicy
	for _, policy := range policies.Items {
		if !originPolicyTargetsRoute(policy, route, rule) {
			continue
		}
		if policy.Namespace != route.Namespace {
			ok, err := r.referenceGrantPermits(ctx, policy.Namespace, route.Namespace, cfgatev1alpha1.GroupVersion.Group, "CloudflareOriginPolicy", gateway.GroupName, "HTTPRoute", route.Name)
			if err != nil {
				return nil, fmt.Errorf("checking CloudflareOriginPolicy ReferenceGrant: %w", err)
			}
			if !ok {
				continue
			}
		}
		matches = append(matches, policy)
	}
	if len(matches) == 0 {
		return nil, nil
	}
	sortOriginPolicies(matches)
	return &matches[0], nil
}

func originPolicyTargetsRoute(policy cfgatev1alpha1.CloudflareOriginPolicy, route *gateway.HTTPRoute, rule gateway.HTTPRouteRule) bool {
	ruleName := ""
	if rule.Name != nil {
		ruleName = string(*rule.Name)
	}
	for _, ref := range policy.Spec.TargetRefs {
		group := ref.Group
		if group == "" {
			group = gateway.GroupName
		}
		kind := ref.Kind
		if kind == "" {
			kind = "HTTPRoute"
		}
		ns := ref.Namespace
		if ns == "" {
			ns = policy.Namespace
		}
		if group != gateway.GroupName || kind != "HTTPRoute" || ns != route.Namespace || ref.Name != route.Name {
			continue
		}
		if ref.SectionName == "" || ref.SectionName == ruleName {
			return true
		}
	}
	return false
}

func sortOriginPolicies(policies []cfgatev1alpha1.CloudflareOriginPolicy) {
	sort.SliceStable(policies, func(i, j int) bool {
		left, right := policies[i], policies[j]
		if !left.CreationTimestamp.Equal(&right.CreationTimestamp) {
			return left.CreationTimestamp.Before(&right.CreationTimestamp)
		}
		return left.Namespace+"/"+left.Name < right.Namespace+"/"+right.Name
	})
}

func applyOriginPolicy(config *cloudflare.OriginRequestConfig, policy *cfgatev1alpha1.CloudflareOriginPolicy, namedPaths map[string]string) (string, error) {
	if policy == nil {
		return "", nil
	}
	if config == nil {
		config = &cloudflare.OriginRequestConfig{}
	}
	origin := policy.Spec.Origin
	protocol := string(origin.Protocol)
	if origin.ConnectTimeout != "" {
		config.ConnectTimeout = origin.ConnectTimeout
	}
	if origin.HTTPHostHeader != "" {
		config.HTTPHostHeader = origin.HTTPHostHeader
	}
	if origin.OriginServerName != "" {
		config.OriginServerName = origin.OriginServerName
	}
	if origin.NoTLSVerify {
		config.NoTLSVerify = true
	}
	if origin.HTTP2Origin {
		config.HTTP2Origin = true
	}
	if origin.H2cOrigin {
		config.H2cOrigin = true
	}
	if origin.CAPoolRef != nil {
		path, ok := namedPaths[origin.CAPoolRef.Name]
		if !ok {
			return protocol, fmt.Errorf("CloudflareOriginPolicy %s/%s references unknown origin CA pool %q", policy.Namespace, policy.Name, origin.CAPoolRef.Name)
		}
		config.CAPool = path
	}
	if origin.TLS != nil {
		if origin.TLS.OriginServerName != "" {
			config.OriginServerName = origin.TLS.OriginServerName
		}
		if origin.TLS.MatchSNIToHost {
			config.MatchSNIToHost = true
		}
		if origin.TLS.NoTLSVerify {
			config.NoTLSVerify = true
		}
		if origin.TLS.TLSTimeout != "" {
			config.TLSTimeout = origin.TLS.TLSTimeout
		}
		if origin.TLS.CAPoolRef != nil {
			path, ok := namedPaths[origin.TLS.CAPoolRef.Name]
			if !ok {
				return protocol, fmt.Errorf("CloudflareOriginPolicy %s/%s references unknown origin CA pool %q", policy.Namespace, policy.Name, origin.TLS.CAPoolRef.Name)
			}
			config.CAPool = path
		}
	}
	if origin.HTTP != nil {
		if origin.HTTP.HTTPHostHeader != "" {
			config.HTTPHostHeader = origin.HTTP.HTTPHostHeader
		}
		if origin.HTTP.DisableChunkedEncoding {
			config.DisableChunkedEncoding = true
		}
	}
	if origin.Connection != nil && origin.Connection.ConnectTimeout != "" {
		config.ConnectTimeout = origin.Connection.ConnectTimeout
	}
	return protocol, nil
}

func (r *CloudflareTunnelReconciler) backendTLSPolicyForBackend(ctx context.Context, backendNS, backendName string) (*gateway.BackendTLSPolicy, error) {
	if r == nil || r.Client == nil {
		return nil, nil
	}
	var policies gateway.BackendTLSPolicyList
	if err := r.List(ctx, &policies, client.InNamespace(backendNS)); err != nil {
		return nil, fmt.Errorf("list BackendTLSPolicies in %s: %w", backendNS, err)
	}
	var matches []gateway.BackendTLSPolicy
	for _, policy := range policies.Items {
		if backendTLSPolicyTargetsService(policy, backendName) {
			matches = append(matches, policy)
		}
	}
	if len(matches) == 0 {
		return nil, nil
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if !matches[i].CreationTimestamp.Equal(&matches[j].CreationTimestamp) {
			return matches[i].CreationTimestamp.Before(&matches[j].CreationTimestamp)
		}
		return matches[i].Namespace+"/"+matches[i].Name < matches[j].Namespace+"/"+matches[j].Name
	})
	return &matches[0], nil
}

func backendTLSPolicyTargetsService(policy gateway.BackendTLSPolicy, serviceName string) bool {
	if len(policy.Spec.TargetRefs) != 1 {
		return false
	}
	ref := policy.Spec.TargetRefs[0]
	if string(ref.Group) != "" && string(ref.Group) != "core" {
		return false
	}
	if string(ref.Kind) != "Service" || string(ref.Name) != serviceName {
		return false
	}
	return ref.SectionName == nil || *ref.SectionName == ""
}

func applyBackendTLSPolicy(config *cloudflare.OriginRequestConfig, policy *gateway.BackendTLSPolicy, caPoolPaths map[types.NamespacedName]string) (bool, error) {
	if policy == nil {
		return false, nil
	}
	if len(policy.Spec.TargetRefs) != 1 {
		return false, fmt.Errorf("BackendTLSPolicy %s/%s must have exactly one targetRef", policy.Namespace, policy.Name)
	}
	if len(policy.Spec.Options) > 0 {
		return false, fmt.Errorf("BackendTLSPolicy %s/%s options are not supported", policy.Namespace, policy.Name)
	}
	if len(policy.Spec.Validation.SubjectAltNames) > 0 {
		return false, fmt.Errorf("BackendTLSPolicy %s/%s subjectAltNames are not supported", policy.Namespace, policy.Name)
	}
	if config == nil {
		config = &cloudflare.OriginRequestConfig{}
	}
	config.OriginServerName = string(policy.Spec.Validation.Hostname)
	if policy.Spec.Validation.WellKnownCACertificates != nil {
		if *policy.Spec.Validation.WellKnownCACertificates != gateway.WellKnownCACertificatesSystem {
			return false, fmt.Errorf("BackendTLSPolicy %s/%s wellKnownCACertificates %q is not supported", policy.Namespace, policy.Name, *policy.Spec.Validation.WellKnownCACertificates)
		}
		return true, nil
	}
	if len(policy.Spec.Validation.CACertificateRefs) != 1 {
		return false, fmt.Errorf("BackendTLSPolicy %s/%s must reference exactly one CA ConfigMap", policy.Namespace, policy.Name)
	}
	ref := policy.Spec.Validation.CACertificateRefs[0]
	if string(ref.Group) != "" || string(ref.Kind) != "ConfigMap" {
		return false, fmt.Errorf("BackendTLSPolicy %s/%s CA ref must be a core ConfigMap", policy.Namespace, policy.Name)
	}
	path, ok := caPoolPaths[types.NamespacedName{Namespace: policy.Namespace, Name: policy.Name}]
	if !ok {
		return false, fmt.Errorf("BackendTLSPolicy %s/%s CA ConfigMap is not mounted", policy.Namespace, policy.Name)
	}
	config.CAPool = path
	return true, nil
}

func mergeOriginRequest(base, override *cloudflare.OriginRequestConfig) *cloudflare.OriginRequestConfig {
	if base == nil {
		base = &cloudflare.OriginRequestConfig{}
	}
	if override == nil {
		if originRequestEmpty(base) {
			return nil
		}
		return base
	}
	if override.ConnectTimeout != "" {
		base.ConnectTimeout = override.ConnectTimeout
	}
	if override.TLSTimeout != "" {
		base.TLSTimeout = override.TLSTimeout
	}
	if override.HTTPHostHeader != "" {
		base.HTTPHostHeader = override.HTTPHostHeader
	}
	if override.OriginServerName != "" {
		base.OriginServerName = override.OriginServerName
	}
	if override.CAPool != "" {
		base.CAPool = override.CAPool
	}
	base.NoTLSVerify = base.NoTLSVerify || override.NoTLSVerify
	base.DisableChunkedEncoding = base.DisableChunkedEncoding || override.DisableChunkedEncoding
	base.HTTP2Origin = base.HTTP2Origin || override.HTTP2Origin
	base.H2cOrigin = base.H2cOrigin || override.H2cOrigin
	base.MatchSNIToHost = base.MatchSNIToHost || override.MatchSNIToHost
	if originRequestEmpty(base) {
		return nil
	}
	return base
}

func originRequestEmpty(config *cloudflare.OriginRequestConfig) bool {
	return config == nil ||
		(config.ConnectTimeout == "" &&
			config.TLSTimeout == "" &&
			config.HTTPHostHeader == "" &&
			config.OriginServerName == "" &&
			config.CAPool == "" &&
			!config.NoTLSVerify &&
			!config.DisableChunkedEncoding &&
			!config.HTTP2Origin &&
			!config.H2cOrigin &&
			!config.MatchSNIToHost)
}

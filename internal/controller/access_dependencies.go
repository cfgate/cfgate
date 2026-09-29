package controller

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	cfg "cfgate.io/cfgate/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func normalizedAccessDependencies(deps []cfg.TunnelAccessDependency, account, tunnelID string) ([]cfg.TunnelAccessDependency, error) {
	merged := map[string]cfg.TunnelAccessDependency{}
	for _, dep := range deps {
		if dep.AccountID == "" {
			dep.AccountID = account
		}
		if dep.TunnelID == "" {
			dep.TunnelID = tunnelID
		}
		key := dep.Namespace + "/" + dep.Name + "/" + dep.UID + "/" + dep.AccountID + "/" + dep.TunnelID
		previous := merged[key]
		hosts := map[string]bool{}
		for _, host := range previous.Hostnames {
			hosts[host] = true
		}
		for _, host := range dep.Hostnames {
			hosts[host] = true
		}
		dep.Hostnames = nil
		for host := range hosts {
			dep.Hostnames = append(dep.Hostnames, host)
		}
		sort.Strings(dep.Hostnames)
		if len(dep.Hostnames) > 64 {
			return nil, fmt.Errorf("more than 64 hostnames for one Access dependency")
		}
		policies := map[string]cfg.TunnelAccessPolicyDependency{}
		for _, policy := range append(append([]cfg.TunnelAccessPolicyDependency(nil), previous.Policies...), dep.Policies...) {
			policies[policy.Namespace+"/"+policy.Name+"/"+policy.UID+"/"+policy.PolicyID] = policy
		}
		if len(policies) > 64 {
			return nil, fmt.Errorf("more than 64 retained policies for one Access dependency")
		}
		policyKeys := make([]string, 0, len(policies))
		for key := range policies {
			policyKeys = append(policyKeys, key)
		}
		sort.Strings(policyKeys)
		dep.Policies = nil
		for _, key := range policyKeys {
			dep.Policies = append(dep.Policies, policies[key])
		}
		merged[key] = dep
	}
	if len(merged) > 256 {
		return nil, fmt.Errorf("more than 256 Access dependencies for one tunnel")
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out []cfg.TunnelAccessDependency
	for _, key := range keys {
		out = append(out, merged[key])
	}
	return out, nil
}

func (r *CloudflareTunnelReconciler) persistAccessDependencies(ctx context.Context, tunnel *cfg.CloudflareTunnel, deps []cfg.TunnelAccessDependency) error {
	var current cfg.CloudflareTunnel
	if err := r.lifecycleReader().Get(ctx, client.ObjectKeyFromObject(tunnel), &current); err != nil {
		return err
	}
	if current.UID != tunnel.UID {
		return fmt.Errorf("tunnel identity changed before Access dependency persistence")
	}
	if !reflect.DeepEqual(current.Status.AccessDependencies, deps) {
		patch := client.MergeFromWithOptions(current.DeepCopy(), client.MergeFromWithOptimisticLock{})
		current.Status.AccessDependencies = deps
		if err := r.Status().Patch(ctx, &current, patch); err != nil {
			return fmt.Errorf("persist Access dependencies before publication: %w", err)
		}
	}
	tunnel.Status.AccessDependencies = deps
	tunnel.ResourceVersion = current.ResourceVersion
	return nil
}

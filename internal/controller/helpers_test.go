package controller

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/controller/annotations"
	"cfgate.io/cfgate/internal/controller/status"
)

func TestDNSHelperFunctions(t *testing.T) {
	t.Run("extracts tunnel reference with namespace defaulting", func(t *testing.T) {
		dns := &cfgatev1alpha1.CloudflareDNS{
			ObjectMeta: metav1.ObjectMeta{Namespace: "cfgate-system"},
			Spec: cfgatev1alpha1.CloudflareDNSSpec{
				TunnelRef: &cfgatev1alpha1.DNSTunnelRef{Name: "edge"},
			},
		}

		keys := extractDNSTunnelRefName(dns)
		if len(keys) != 1 || keys[0] != "cfgate-system/edge" {
			t.Fatalf("extractDNSTunnelRefName() = %#v", keys)
		}

		dns.Spec.TunnelRef.Namespace = "networking"
		keys = extractDNSTunnelRefName(dns)
		if len(keys) != 1 || keys[0] != "networking/edge" {
			t.Fatalf("extractDNSTunnelRefName() explicit namespace = %#v", keys)
		}
	})

	t.Run("extracts gateway routes enabled flag", func(t *testing.T) {
		dns := &cfgatev1alpha1.CloudflareDNS{
			Spec: cfgatev1alpha1.CloudflareDNSSpec{
				Source: cfgatev1alpha1.DNSHostnameSource{
					GatewayRoutes: cfgatev1alpha1.DNSGatewayRoutesSource{Enabled: true},
				},
			},
		}

		keys := extractDNSGatewayRoutesEnabled(dns)
		if len(keys) != 1 || keys[0] != "true" {
			t.Fatalf("extractDNSGatewayRoutesEnabled() = %#v", keys)
		}
	})

	t.Run("returns sorted hostname keys", func(t *testing.T) {
		keys := hostnameKeys(map[string]HostnameConfig{
			"b.example.com": {},
			"a.example.com": {},
		})
		slices.Sort(keys)
		if !slices.Equal(keys, []string{"a.example.com", "b.example.com"}) {
			t.Fatalf("hostnameKeys() = %#v", keys)
		}
	})

	t.Run("validates gateway parent refs", func(t *testing.T) {
		if !isGatewayParentRef(gatewayv1.ParentReference{Name: "gw"}) {
			t.Fatal("isGatewayParentRef() = false, want true for default Gateway ref")
		}

		group := gatewayv1.Group("example.com")
		if isGatewayParentRef(gatewayv1.ParentReference{Name: "gw", Group: &group}) {
			t.Fatal("isGatewayParentRef() = true, want false for non-gateway group")
		}

		kind := gatewayv1.Kind("Service")
		if isGatewayParentRef(gatewayv1.ParentReference{Name: "gw", Kind: &kind}) {
			t.Fatal("isGatewayParentRef() = true, want false for non-Gateway kind")
		}
	})
}

func TestDNSAndTunnelStatusHelpers(t *testing.T) {
	baseDNS := &cfgatev1alpha1.CloudflareDNSStatus{
		ObservedGeneration: 3,
		ResolvedTarget:     "abcd.cfargotunnel.com",
		SyncedRecords:      2,
		PendingRecords:     1,
		FailedRecords:      0,
		Conditions: []metav1.Condition{{
			Type:               status.ConditionTypeReady,
			Status:             metav1.ConditionTrue,
			Reason:             status.ReasonResolved,
			Message:            "ok",
			LastTransitionTime: metav1.Now(),
		}},
		Records: []cfgatev1alpha1.DNSRecordSyncStatus{{
			Hostname: "app.example.com",
			Type:     "CNAME",
			Status:   "Synced",
		}},
	}
	sameDNS := baseDNS.DeepCopy()
	sameDNS.Conditions[0].LastTransitionTime = metav1.NewTime(baseDNS.Conditions[0].LastTransitionTime.Add(5))
	if !dnsStatusEqual(baseDNS, sameDNS) {
		t.Fatal("dnsStatusEqual() = false, want true when only transition time differs")
	}
	sameDNS.FailedRecords = 1
	if dnsStatusEqual(baseDNS, sameDNS) {
		t.Fatal("dnsStatusEqual() = true, want false when record counts differ")
	}

	baseTunnel := &cfgatev1alpha1.CloudflareTunnelStatus{
		ObservedGeneration:  2,
		TunnelID:            "tunnel-1",
		TunnelName:          "edge",
		TunnelDomain:        "tunnel-1.cfargotunnel.com",
		AccountID:           "account-1",
		Replicas:            2,
		ReadyReplicas:       2,
		ConnectedRouteCount: 4,
		Conditions: []metav1.Condition{{
			Type:               status.ConditionTypeReady,
			Status:             metav1.ConditionTrue,
			Reason:             status.ReasonResolved,
			Message:            "ok",
			LastTransitionTime: metav1.Now(),
		}},
	}
	sameTunnel := baseTunnel.DeepCopy()
	sameTunnel.Conditions[0].LastTransitionTime = metav1.NewTime(baseTunnel.Conditions[0].LastTransitionTime.Add(5))
	if !tunnelStatusEqual(baseTunnel, sameTunnel) {
		t.Fatal("tunnelStatusEqual() = false, want true when only transition time differs")
	}
	sameTunnel.ReadyReplicas = 1
	if tunnelStatusEqual(baseTunnel, sameTunnel) {
		t.Fatal("tunnelStatusEqual() = true, want false when replica counts differ")
	}

	tunnel := &cfgatev1alpha1.CloudflareTunnel{Status: *baseTunnel}
	if !isTunnelHealthy(tunnel) {
		t.Fatal("isTunnelHealthy() = false, want true for ready tunnel")
	}
	tunnel.Status.TunnelID = ""
	if isTunnelHealthy(tunnel) {
		t.Fatal("isTunnelHealthy() = true, want false when tunnel ID is missing")
	}

	configA := cloudflare.TunnelConfiguration{
		Ingress: []cloudflare.IngressRule{
			{Hostname: "b.example.com", Service: "http://svc-b"},
			{Hostname: "a.example.com", Service: "http://svc-a"},
		},
	}
	configB := cloudflare.TunnelConfiguration{
		Ingress: []cloudflare.IngressRule{
			{Hostname: "a.example.com", Service: "http://svc-a"},
			{Hostname: "b.example.com", Service: "http://svc-b"},
		},
	}
	if tunnelConfigHash(configA) != tunnelConfigHash(configB) {
		t.Fatal("tunnelConfigHash() should be stable across ingress ordering")
	}

	if got := ptrTo("cfgate"); got == nil || *got != "cfgate" {
		t.Fatalf("ptrTo() = %#v", got)
	}
}

func TestGatewayAndAccessPolicyHelpers(t *testing.T) {
	scheme := controllerTestScheme(t)

	t.Run("maps gateways for tunnel references", func(t *testing.T) {
		tunnel := &cfgatev1alpha1.CloudflareTunnel{
			ObjectMeta: metav1.ObjectMeta{Name: "edge", Namespace: "cfgate-system"},
		}
		gateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "public",
				Namespace: "app",
				Annotations: map[string]string{
					annotations.AnnotationTunnelRef: "cfgate-system/edge",
				},
			},
		}
		r := &GatewayReconciler{
			Client: newHTTPRouteTestReconciler(t, scheme, gateway).Client,
		}

		reqs := r.findGatewaysForTunnel(context.Background(), tunnel)
		if len(reqs) != 1 || reqs[0].NamespacedName != (types.NamespacedName{Namespace: "app", Name: "public"}) {
			t.Fatalf("findGatewaysForTunnel() = %#v", reqs)
		}
	})

	t.Run("maps gateways for route parent refs without duplicates", func(t *testing.T) {
		section := gatewayv1.SectionName("http")
		route := &gatewayv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"},
			Spec: gatewayv1.HTTPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{
						{Name: "public", SectionName: &section},
						{Name: "public"},
					},
				},
			},
		}

		reqs := (&GatewayReconciler{}).findGatewaysForHTTPRoute(context.Background(), route)
		if len(reqs) != 1 || reqs[0].NamespacedName != (types.NamespacedName{Namespace: "app", Name: "public"}) {
			t.Fatalf("findGatewaysForHTTPRoute() = %#v", reqs)
		}
	})

	t.Run("indexes access policy targets", func(t *testing.T) {
		otherNS := "edge"
		policy := &cfgatev1alpha1.CloudflareAccessPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "app"},
			Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
				TargetRef: &cfgatev1alpha1.PolicyTargetReference{
					Group: "gateway.networking.k8s.io",
					Kind:  "Gateway",
					Name:  "public",
				},
				TargetRefs: []cfgatev1alpha1.PolicyTargetReference{{
					Group:     "gateway.networking.k8s.io",
					Kind:      "HTTPRoute",
					Name:      "route",
					Namespace: &otherNS,
				}},
			},
		}

		keys := accessPolicyTargetIndexFunc(policy)
		if !slices.Equal(keys, []string{"Gateway/app/public", "HTTPRoute/edge/route"}) {
			t.Fatalf("accessPolicyTargetIndexFunc() = %#v", keys)
		}
		if got := accessPolicyTargetKey("HTTPRoute", "app", "route"); got != "HTTPRoute/app/route" {
			t.Fatalf("accessPolicyTargetKey() = %q", got)
		}
	})

	t.Run("evaluates reference grant access", func(t *testing.T) {
		grant := gatewayv1beta1.ReferenceGrant{
			Spec: gatewayv1beta1.ReferenceGrantSpec{
				From: []gatewayv1beta1.ReferenceGrantFrom{{
					Group:     "cfgate.io",
					Kind:      "CloudflareAccessPolicy",
					Namespace: "app",
				}},
				To: []gatewayv1beta1.ReferenceGrantTo{{
					Group: "gateway.networking.k8s.io",
					Kind:  "HTTPRoute",
				}},
			},
		}

		r := &CloudflareAccessPolicyReconciler{}
		if !r.grantPermitsAccess(grant, "app", "HTTPRoute") {
			t.Fatal("grantPermitsAccess() = false, want true")
		}
		if r.grantPermitsAccess(grant, "other", "HTTPRoute") {
			t.Fatal("grantPermitsAccess() = true, want false for wrong namespace")
		}
	})

	t.Run("converts access rules and approval groups", func(t *testing.T) {
		everyone := true
		anyServiceToken := true
		rules, err := convertAccessRules([]cfgatev1alpha1.AccessRule{
			{IP: &cfgatev1alpha1.AccessIPRule{Ranges: []string{"10.0.0.0/8", "192.168.0.0/16"}}},
			{IPList: &cfgatev1alpha1.AccessIPListRule{ID: "ip-list-1"}},
			{Country: &cfgatev1alpha1.AccessCountryRule{Codes: []string{"US", "CA"}}},
			{Everyone: &everyone},
			{ServiceToken: &cfgatev1alpha1.AccessServiceTokenRule{TokenID: "token-1"}},
			{AnyValidServiceToken: &anyServiceToken},
			{Email: &cfgatev1alpha1.AccessEmailRule{Addresses: []string{"dev@example.com"}}},
			{EmailList: &cfgatev1alpha1.AccessEmailListRule{ID: "email-list-1"}},
			{EmailDomain: &cfgatev1alpha1.AccessEmailDomainRule{Domain: "example.com"}},
			{OIDCClaim: &cfgatev1alpha1.AccessOIDCClaimRule{
				IdentityProviderID: "idp-1",
				ClaimName:          "groups",
				ClaimValue:         "engineering",
			}},
			{GSuiteGroup: &cfgatev1alpha1.AccessGSuiteGroupRule{
				IdentityProviderID: "idp-2",
				Email:              "eng@example.com",
			}},
		})
		if err != nil {
			t.Fatalf("convertAccessRules() error = %v", err)
		}
		if len(rules) != 13 {
			t.Fatalf("len(convertAccessRules()) = %d, want 13", len(rules))
		}
		if rules[0].IPRange == nil || *rules[0].IPRange != "10.0.0.0/8" {
			t.Fatalf("first rule = %+v", rules[0])
		}
		if rules[11].OIDCClaim == nil || rules[11].OIDCClaim.ClaimName != "groups" {
			t.Fatalf("OIDC rule = %+v", rules[11])
		}
		if rules[12].GSuiteGroup == nil || rules[12].GSuiteGroup.Email != "eng@example.com" {
			t.Fatalf("GSuite rule = %+v", rules[12])
		}

		_, err = convertAccessRules([]cfgatev1alpha1.AccessRule{{
			IPList: &cfgatev1alpha1.AccessIPListRule{Name: "office"},
		}})
		if err == nil {
			t.Fatal("convertAccessRules() error = nil, want missing list ID error")
		}

		approvalGroups := convertApprovalGroups([]cfgatev1alpha1.ApprovalGroup{{
			Emails:          []string{"approver@example.com"},
			ApprovalsNeeded: 2,
		}})
		if len(approvalGroups) != 1 || approvalGroups[0].ApprovalsNeeded != 2 {
			t.Fatalf("convertApprovalGroups() = %#v", approvalGroups)
		}
	})

	t.Run("compares access policy statuses ignoring transition time", func(t *testing.T) {
		base := &cfgatev1alpha1.CloudflareAccessPolicyStatus{
			ApplicationID:      "app-1",
			ApplicationAUD:     "aud-1",
			MTLSRuleID:         "rule-1",
			AttachedTargets:    2,
			ObservedGeneration: 5,
			ServiceTokenIDs:    map[string]string{"svc": "token-1"},
			Conditions: []metav1.Condition{{
				Type:               status.ConditionTypeReady,
				Status:             metav1.ConditionTrue,
				Reason:             status.ReasonResolved,
				Message:            "ok",
				LastTransitionTime: metav1.Now(),
			}},
			Ancestors: []cfgatev1alpha1.PolicyAncestorStatus{{
				AncestorRef: cfgatev1alpha1.PolicyTargetReference{
					Group: "gateway.networking.k8s.io",
					Kind:  "HTTPRoute",
					Name:  "route",
				},
				ControllerName: GatewayControllerName,
				Conditions: []metav1.Condition{{
					Type:               status.ConditionTypeReady,
					Status:             metav1.ConditionTrue,
					Reason:             status.ReasonResolved,
					Message:            "ok",
					LastTransitionTime: metav1.Now(),
				}},
			}},
		}
		same := base.DeepCopy()
		same.Conditions[0].LastTransitionTime = metav1.NewTime(base.Conditions[0].LastTransitionTime.Add(10))
		same.Ancestors[0].Conditions[0].LastTransitionTime = metav1.NewTime(base.Ancestors[0].Conditions[0].LastTransitionTime.Add(10))
		if !accessPolicyStatusEqual(base, same) {
			t.Fatal("accessPolicyStatusEqual() = false, want true when only transition times differ")
		}
		same.ApplicationAUD = "other"
		if accessPolicyStatusEqual(base, same) {
			t.Fatal("accessPolicyStatusEqual() = true, want false when application data differs")
		}
	})
}

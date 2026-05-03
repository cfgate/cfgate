package controller

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/controller/annotations"
	"cfgate.io/cfgate/internal/controller/status"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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
					GatewayRoutes: &cfgatev1alpha1.DNSGatewayRoutesSource{Enabled: true},
				},
			},
		}

		keys := extractDNSGatewayRoutesEnabled(dns)
		if len(keys) != 1 || keys[0] != "true" {
			t.Fatalf("extractDNSGatewayRoutesEnabled() = %#v", keys)
		}
	})

	t.Run("ignores nil gateway routes source", func(t *testing.T) {
		dns := &cfgatev1alpha1.CloudflareDNS{
			Spec: cfgatev1alpha1.CloudflareDNSSpec{
				Source: cfgatev1alpha1.DNSHostnameSource{},
			},
		}

		if keys := extractDNSGatewayRoutesEnabled(dns); len(keys) != 0 {
			t.Fatalf("extractDNSGatewayRoutesEnabled() = %#v, want nil", keys)
		}
	})

	t.Run("returns all hostname keys (order not guaranteed)", func(t *testing.T) {
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

	t.Run("resolves explicit hostname targets from defaults, templates, and overrides", func(t *testing.T) {
		tunnelDNS := &cfgatev1alpha1.CloudflareDNS{
			Spec: cfgatev1alpha1.CloudflareDNSSpec{
				TunnelRef: &cfgatev1alpha1.DNSTunnelRef{Name: "edge"},
			},
		}
		tunnel := &cfgatev1alpha1.CloudflareTunnel{
			Status: cfgatev1alpha1.CloudflareTunnelStatus{
				TunnelDomain: "edge.cfargotunnel.com",
			},
		}
		externalDNS := &cfgatev1alpha1.CloudflareDNS{
			Spec: cfgatev1alpha1.CloudflareDNSSpec{
				ExternalTarget: &cfgatev1alpha1.ExternalTarget{
					Type:  cfgatev1alpha1.RecordTypeCNAME,
					Value: "origin.example.net",
				},
			},
		}

		if got := resolveExplicitHostnameTarget(tunnelDNS, tunnel, cfgatev1alpha1.DNSExplicitHostname{}); got != "edge.cfargotunnel.com" {
			t.Fatalf("resolveExplicitHostnameTarget() default tunnel target = %q, want %q", got, "edge.cfargotunnel.com")
		}
		if got := resolveExplicitHostnameTarget(tunnelDNS, tunnel, cfgatev1alpha1.DNSExplicitHostname{Target: "{{ .TunnelDomain }}"}); got != "edge.cfargotunnel.com" {
			t.Fatalf("resolveExplicitHostnameTarget() template = %q, want %q", got, "edge.cfargotunnel.com")
		}
		if got := resolveExplicitHostnameTarget(tunnelDNS, tunnel, cfgatev1alpha1.DNSExplicitHostname{Target: "custom.example.net"}); got != "custom.example.net" {
			t.Fatalf("resolveExplicitHostnameTarget() tunnel override = %q, want %q", got, "custom.example.net")
		}
		if got := resolveExplicitHostnameTarget(externalDNS, nil, cfgatev1alpha1.DNSExplicitHostname{}); got != "origin.example.net" {
			t.Fatalf("resolveExplicitHostnameTarget() external default = %q, want %q", got, "origin.example.net")
		}
		if got := resolveExplicitHostnameTarget(externalDNS, nil, cfgatev1alpha1.DNSExplicitHostname{Target: "override.example.net"}); got != "override.example.net" {
			t.Fatalf("resolveExplicitHostnameTarget() external override = %q, want %q", got, "override.example.net")
		}
	})

	t.Run("external target route discovery is a no-op without tunnel", func(t *testing.T) {
		r := &CloudflareDNSReconciler{}
		dns := &cfgatev1alpha1.CloudflareDNS{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "dns",
				Namespace: "app",
			},
			Spec: cfgatev1alpha1.CloudflareDNSSpec{
				ExternalTarget: &cfgatev1alpha1.ExternalTarget{
					Type:  cfgatev1alpha1.RecordTypeCNAME,
					Value: "origin.example.net",
				},
				Source: cfgatev1alpha1.DNSHostnameSource{
					GatewayRoutes: &cfgatev1alpha1.DNSGatewayRoutesSource{Enabled: true},
				},
			},
		}

		hostnames, err := r.collectHostnamesFromRoutes(context.Background(), dns, nil)
		if err != nil {
			t.Fatalf("collectHostnamesFromRoutes() error = %v", err)
		}
		if hostnames != nil {
			t.Fatalf("collectHostnamesFromRoutes() = %#v, want nil", hostnames)
		}
	})
}

func TestCloudflareDNSCollectHostnames(t *testing.T) {
	t.Run("explicit hostnames override colliding route-discovered config while route discovery stays additive", func(t *testing.T) {
		scheme := controllerTestScheme(t)
		sharedHostname := "app.example.com"
		routeOnlyHostname := "route.example.com"

		gateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "public",
				Namespace: "apps",
				Annotations: map[string]string{
					annotations.AnnotationTunnelRef: "cfgate-system/edge",
				},
			},
		}
		route := &gatewayv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "route",
				Namespace: "apps",
				Annotations: map[string]string{
					"e2e.dns/merge":                         "enabled",
					annotations.AnnotationTTL:               "600",
					annotations.AnnotationCloudflareProxied: "true",
				},
			},
			Spec: gatewayv1.HTTPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{
						Name: "public",
					}},
				},
				Hostnames: []gatewayv1.Hostname{
					gatewayv1.Hostname(sharedHostname),
					gatewayv1.Hostname(routeOnlyHostname),
				},
			},
		}
		dns := &cfgatev1alpha1.CloudflareDNS{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "dns",
				Namespace: "cfgate-system",
			},
			Spec: cfgatev1alpha1.CloudflareDNSSpec{
				TunnelRef: &cfgatev1alpha1.DNSTunnelRef{Name: "edge"},
				Source: cfgatev1alpha1.DNSHostnameSource{
					GatewayRoutes: &cfgatev1alpha1.DNSGatewayRoutesSource{
						Enabled:          true,
						AnnotationFilter: "e2e.dns/merge=enabled",
					},
					Explicit: []cfgatev1alpha1.DNSExplicitHostname{{
						Hostname: sharedHostname,
						Target:   "custom.example.net",
						TTL:      300,
						Proxied:  ptrTo(false),
					}},
				},
			},
		}
		tunnel := &cfgatev1alpha1.CloudflareTunnel{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "edge",
				Namespace: "cfgate-system",
			},
			Status: cfgatev1alpha1.CloudflareTunnelStatus{
				TunnelDomain: "edge.cfargotunnel.com",
			},
		}

		r := &CloudflareDNSReconciler{
			APIReader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(gateway, route).Build(),
		}

		hostnames, err := r.collectHostnames(context.Background(), dns, tunnel)
		if err != nil {
			t.Fatalf("collectHostnames() error = %v", err)
		}
		if len(hostnames) != 2 {
			t.Fatalf("len(hostnames) = %d, want 2", len(hostnames))
		}

		sharedConfig, ok := hostnames[sharedHostname]
		if !ok {
			t.Fatalf("shared hostname %q missing from collected hostnames", sharedHostname)
		}
		if sharedConfig.Target != "custom.example.net" {
			t.Fatalf("shared Target = %q, want %q", sharedConfig.Target, "custom.example.net")
		}
		if sharedConfig.TTL != 300 {
			t.Fatalf("shared TTL = %d, want 300", sharedConfig.TTL)
		}
		if sharedConfig.Proxied == nil || *sharedConfig.Proxied {
			t.Fatalf("shared Proxied = %#v, want false", sharedConfig.Proxied)
		}
		if sharedConfig.RecordType != "CNAME" {
			t.Fatalf("shared RecordType = %q, want CNAME", sharedConfig.RecordType)
		}

		routeConfig, ok := hostnames[routeOnlyHostname]
		if !ok {
			t.Fatalf("route-only hostname %q missing from collected hostnames", routeOnlyHostname)
		}
		if routeConfig.Target != "" {
			t.Fatalf("route-only Target = %q, want empty", routeConfig.Target)
		}
		if routeConfig.TTL != 600 {
			t.Fatalf("route-only TTL = %d, want 600", routeConfig.TTL)
		}
		if routeConfig.Proxied == nil || !*routeConfig.Proxied {
			t.Fatalf("route-only Proxied = %#v, want true", routeConfig.Proxied)
		}
		if routeConfig.RecordType != "CNAME" {
			t.Fatalf("route-only RecordType = %q, want CNAME", routeConfig.RecordType)
		}
	})
}

func TestCloudflareDNSSyncRecords(t *testing.T) {
	t.Run("uses effective per-hostname target and does not let route-derived config override explicit config", func(t *testing.T) {
		ctx := context.Background()
		txtDisabled := false
		created := map[string]cloudflare.DNSRecord{}
		mock := cloudflare.NewMockClient()
		mock.ListDNSRecordsByNameTypeFunc = func(context.Context, string, string, string) ([]cloudflare.DNSRecord, error) {
			return nil, nil
		}
		mock.CreateDNSRecordFunc = func(_ context.Context, _ string, record cloudflare.DNSRecord) (*cloudflare.DNSRecord, error) {
			record.ID = record.Name + "-id"
			created[record.Name] = record
			return &record, nil
		}

		dns := &cfgatev1alpha1.CloudflareDNS{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "dns",
				Namespace: "default",
			},
			Spec: cfgatev1alpha1.CloudflareDNSSpec{
				Policy: cfgatev1alpha1.DNSPolicyUpsertOnly,
				Defaults: cfgatev1alpha1.DNSRecordDefaults{
					Proxied: true,
					TTL:     1,
				},
				Ownership: cfgatev1alpha1.DNSOwnershipConfig{
					TXTRecord: cfgatev1alpha1.DNSTXTRecordOwnership{
						Enabled: &txtDisabled,
					},
				},
			},
		}
		explicitProxied := false
		routeProxied := true
		hostnameConfigs := map[string]HostnameConfig{
			"app.example.com": {
				Target:     "custom.example.net",
				TTL:        300,
				Proxied:    &explicitProxied,
				RecordType: "CNAME",
			},
			"route.example.com": {
				Proxied:    &routeProxied,
				RecordType: "CNAME",
			},
		}
		r := &CloudflareDNSReconciler{Recorder: &fakeEventRecorder{}}

		err := r.syncRecords(ctx, dns, "edge.cfargotunnel.com", hostnameConfigs, map[string]string{"example.com": "zone-1"}, cloudflare.NewDNSService(mock, discardLogger()))
		if err != nil {
			t.Fatalf("syncRecords() error = %v", err)
		}

		explicitRecord, ok := created["app.example.com"]
		if !ok {
			t.Fatal("syncRecords() did not create app.example.com")
		}
		if explicitRecord.Content != "custom.example.net" {
			t.Fatalf("explicit Content = %q, want %q", explicitRecord.Content, "custom.example.net")
		}
		if explicitRecord.TTL != 300 {
			t.Fatalf("explicit TTL = %d, want 300", explicitRecord.TTL)
		}
		if explicitRecord.Proxied {
			t.Fatalf("explicit Proxied = %v, want false", explicitRecord.Proxied)
		}

		routeRecord, ok := created["route.example.com"]
		if !ok {
			t.Fatal("syncRecords() did not create route.example.com")
		}
		if routeRecord.Content != "edge.cfargotunnel.com" {
			t.Fatalf("route Content = %q, want %q", routeRecord.Content, "edge.cfargotunnel.com")
		}
		if routeRecord.TTL != 1 {
			t.Fatalf("route TTL = %d, want 1", routeRecord.TTL)
		}
		if !routeRecord.Proxied {
			t.Fatalf("route Proxied = %v, want true", routeRecord.Proxied)
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

	t.Run("collects access application targets", func(t *testing.T) {
		otherNS := "edge"
		app := &cfgatev1alpha1.CloudflareAccessApplication{
			ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "app"},
			Spec: cfgatev1alpha1.CloudflareAccessApplicationSpec{
				TargetRefs: []cfgatev1alpha1.PolicyTargetReference{{
					Group: "gateway.networking.k8s.io",
					Kind:  "Gateway",
					Name:  "public",
				}, {
					Group:     "gateway.networking.k8s.io",
					Kind:      "HTTPRoute",
					Name:      "route",
					Namespace: &otherNS,
				}},
			},
		}

		refs := accessApplicationTargetRefs(app)
		if refs[0].Kind != "Gateway" || refs[0].Name != "public" {
			t.Fatalf("first target ref = %#v", refs[0])
		}
		if refs[1].Kind != "HTTPRoute" || refs[1].Name != "route" || refs[1].Namespace == nil || *refs[1].Namespace != "edge" {
			t.Fatalf("second target ref = %#v", refs[1])
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
			PolicyID:           "policy-1",
			AccountID:          "account-1",
			Reusable:           true,
			AppCount:           2,
			ObservedGeneration: 5,
			ServiceTokenIDs:    map[string]string{"svc": "token-1"},
			Conditions: []metav1.Condition{{
				Type:               status.ConditionTypeReady,
				Status:             metav1.ConditionTrue,
				Reason:             status.ReasonResolved,
				Message:            "ok",
				LastTransitionTime: metav1.Now(),
			}},
		}
		same := base.DeepCopy()
		same.Conditions[0].LastTransitionTime = metav1.NewTime(base.Conditions[0].LastTransitionTime.Add(10))
		if !accessPolicyStatusEqual(base, same) {
			t.Fatal("accessPolicyStatusEqual() = false, want true when only transition times differ")
		}
		same.PolicyID = "other"
		if accessPolicyStatusEqual(base, same) {
			t.Fatal("accessPolicyStatusEqual() = true, want false when policy data differs")
		}
	})
}

func TestAccessApplicationOwnerTag(t *testing.T) {
	app := &cfgatev1alpha1.CloudflareAccessApplication{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "cfgate-invariants-e2e-55gqr",
			Name:      "e2e-policy-1-293",
		},
	}

	tag := accessApplicationOwnerTag(app)
	if len(tag) > 35 {
		t.Fatalf("accessApplicationOwnerTag() length = %d, want <= 35", len(tag))
	}
	if tag != accessApplicationOwnerTag(app) {
		t.Fatal("accessApplicationOwnerTag() is not deterministic")
	}
	if tag == accessApplicationOwnerTag(&cfgatev1alpha1.CloudflareAccessApplication{
		ObjectMeta: metav1.ObjectMeta{Namespace: app.Namespace, Name: "other"},
	}) {
		t.Fatal("accessApplicationOwnerTag() did not change for a different app")
	}
}

func TestHTTPRouteAccessPathsDerivesNamedRulesWithoutOverride(t *testing.T) {
	admin := gatewayv1.SectionName("admin")
	repos := gatewayv1.SectionName("repos")
	prefix := gatewayv1.PathMatchPathPrefix
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "repo-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{
					Name: &admin,
					Matches: []gatewayv1.HTTPRouteMatch{{
						Path: &gatewayv1.HTTPPathMatch{Type: &prefix, Value: ptrTo("/admin")},
					}},
				},
				{
					Name: &repos,
					Matches: []gatewayv1.HTTPRouteMatch{{
						Path: &gatewayv1.HTTPPathMatch{Type: &prefix, Value: ptrTo("/repos")},
					}},
				},
			},
		},
	}

	paths, err := httpRouteAccessPaths(route, ptrTo("repos"), "")
	if err != nil {
		t.Fatalf("httpRouteAccessPaths() error = %v", err)
	}
	if !slices.Equal(paths, []string{"/repos"}) {
		t.Fatalf("httpRouteAccessPaths() = %#v, want [/repos]", paths)
	}

	paths, err = httpRouteAccessPaths(route, ptrTo("repos"), "/")
	if err != nil {
		t.Fatalf("httpRouteAccessPaths() override error = %v", err)
	}
	if !slices.Equal(paths, []string{"/"}) {
		t.Fatalf("httpRouteAccessPaths() override = %#v, want [/]", paths)
	}
}

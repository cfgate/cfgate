// Package e2e contains end-to-end tests for cfgate.
// invariants_test.go tests structural properties that must ALWAYS hold
// when a resource reaches a known state (Ready=True, Accepted=True, etc.).
// These are cross-cutting checks verifying controller internal consistency
// and Cloudflare API sync.
//
// Spec: ~/.atlas/integrator/specs/cfgate/e2e-invariant-spec-2026-02-09.md
//
// Invariant categories:
//
//	I1: CloudflareTunnel Ready       (INV-T1..T9)
//	I2: CloudflareDNS Ready          (INV-D1..D8)
//	I3: CloudflareAccessPolicy Ready (INV-A1..A8, INV-ST1..ST4)
//	I4: Gateway Status               (INV-GW1..GW4)
//	I5: HTTPRoute Parent Status      (INV-HR1..HR3)
//	I6: GatewayClass                 (INV-GC1..GC2)
//	I7: Cross-CRD Consistency        (INV-X1..X3)
//	I8: Deletion                     (INV-DEL1..DEL4)
//
// WARP/0.2.0 future invariants are documented in the spec but not tested here.
package e2e_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	cloudflare "github.com/cloudflare/cloudflare-go/v6"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
)

var _ = Describe("Invariants E2E", Label("cloudflare", "invariants"), Ordered, func() {
	var (
		namespace    *corev1.Namespace
		cfClient     *cloudflare.Client
		zoneID       string
		sharedTunnel *cfgatev1alpha1.CloudflareTunnel
		gcName       string
		gwName       string
	)

	BeforeAll(func() {
		skipIfNoZone()

		namespace = createTestNamespace("cfgate-invariants-e2e")
		gcName = testID("gc")
		gwName = testID("gw")

		createCloudflareCredentialsSecret(namespace.Name)

		cfClient = getCloudflareClient()

		var err error
		zoneID, err = getZoneIDByName(ctx, cfClient, testEnv.CloudflareZoneName)
		Expect(err).NotTo(HaveOccurred())
		Expect(zoneID).NotTo(BeEmpty())

		By("Creating shared tunnel for invariant tests")
		tunnelName := testID("inv-tunnel")
		sharedTunnel = createCloudflareTunnel(ctx, k8sClient, testID("inv-t"), namespace.Name, tunnelName)
		sharedTunnel = waitForTunnelReady(ctx, k8sClient, sharedTunnel.Name, sharedTunnel.Namespace, DefaultTimeout)

		By("Creating shared GatewayClass + Gateway")
		createGatewayClass(ctx, k8sClient, gcName)
		tunnelRef := fmt.Sprintf("%s/%s", namespace.Name, sharedTunnel.Name)
		createGateway(ctx, k8sClient, gwName, namespace.Name, gcName, tunnelRef)

		DeferCleanup(func() {
			if testEnv.SkipCleanup {
				return
			}
			if namespace != nil {
				deleteTestNamespace(namespace)
			}
		})
	})

	// ============================================================
	// I1: CloudflareTunnel Ready Invariants
	// ============================================================
	Context("CloudflareTunnel Ready invariants", func() {
		It("should satisfy all structural invariants when Ready=True [INV-T1..T9]", SpecTimeout(3*time.Minute), func(ctx SpecContext) {
			var tunnel cfgatev1alpha1.CloudflareTunnel
			Expect(k8sClient.Get(ctx, client.ObjectKey{
				Name:      sharedTunnel.Name,
				Namespace: sharedTunnel.Namespace,
			}, &tunnel)).To(Succeed())

			By("Verifying Ready=True (precondition)")
			readyCond := findCondition(tunnel.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil(), "Ready condition must exist")
			Expect(readyCond.Status).To(Equal(metav1.ConditionTrue), "Tunnel must be Ready=True")

			By("INV-T1: All sub-conditions must be True when Ready=True")
			// Condition names from cloudflaretunnel_controller.go (NOT from status/ package)
			for _, condType := range []string{
				"CredentialsValid",
				"TunnelReady",
				"CloudflaredDeployed",
				"ConfigurationSynced",
			} {
				cond := findCondition(tunnel.Status.Conditions, condType)
				Expect(cond).NotTo(BeNil(), "Condition %s must exist", condType)
				Expect(cond.Status).To(Equal(metav1.ConditionTrue),
					"Condition %s must be True when Ready=True, got %s (reason: %s, message: %s)",
					condType, cond.Status, cond.Reason, cond.Message)
			}

			By("INV-T2: TunnelID must be populated")
			Expect(tunnel.Status.TunnelID).NotTo(BeEmpty(), "TunnelID must be populated")

			By("INV-T3: TunnelDomain must follow Cloudflare convention")
			Expect(tunnel.Status.TunnelDomain).NotTo(BeEmpty(), "TunnelDomain must be populated")
			Expect(tunnel.Status.TunnelDomain).To(HaveSuffix(".cfargotunnel.com"),
				"TunnelDomain must end with .cfargotunnel.com")
			Expect(tunnel.Status.TunnelDomain).To(HavePrefix(tunnel.Status.TunnelID),
				"TunnelDomain must start with TunnelID")

			By("INV-T4: AccountID must be populated")
			Expect(tunnel.Status.AccountID).NotTo(BeEmpty(), "AccountID must be populated")

			By("INV-T5: Finalizer must be present on non-deleted resource")
			Expect(tunnel.Finalizers).To(ContainElement("cfgate.io/tunnel-cleanup"),
				"Finalizer cfgate.io/tunnel-cleanup must be present")

			By("INV-T6: ObservedGeneration must match resource generation")
			Expect(tunnel.Status.ObservedGeneration).To(Equal(tunnel.Generation),
				"ObservedGeneration (%d) must match Generation (%d)",
				tunnel.Status.ObservedGeneration, tunnel.Generation)

			By("INV-T7: Tunnel must exist in Cloudflare API")
			cfTunnel, err := getTunnelByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, tunnel.Status.TunnelID)
			Expect(err).NotTo(HaveOccurred(), "Cloudflare API call must succeed")
			Expect(cfTunnel).NotTo(BeNil(), "Tunnel must exist in Cloudflare when Ready=True")
			Expect(cfTunnel.DeletedAt.IsZero()).To(BeTrue(), "Tunnel must not be marked deleted in Cloudflare")
			Expect(cfTunnel.Name).To(Equal(tunnel.Spec.Tunnel.Name),
				"Cloudflare tunnel name must match spec.tunnel.name")

			By("INV-T8: Deployment must exist with correct replica count")
			var deployment appsv1.Deployment
			deployName := fmt.Sprintf("%s-cloudflared", tunnel.Name)
			err = k8sClient.Get(ctx, client.ObjectKey{Name: deployName, Namespace: tunnel.Namespace}, &deployment)
			Expect(err).NotTo(HaveOccurred(), "cloudflared Deployment must exist")
			Expect(deployment.Spec.Replicas).NotTo(BeNil())
			Expect(*deployment.Spec.Replicas).To(Equal(tunnel.Spec.Cloudflared.Replicas),
				"Deployment replicas must match spec.cloudflared.replicas")

			By("INV-T9: Tunnel config hash annotation must be present on CR")
			// config-hash lives on the CloudflareTunnel CR (not the Deployment pod template).
			// It gates redundant Cloudflare API UpdateConfiguration calls.
			Expect(tunnel.Annotations).To(HaveKey("cfgate.io/config-hash"),
				"CloudflareTunnel must have config-hash annotation after configuration sync")
		})
	})

	// ============================================================
	// I2: CloudflareDNS Ready Invariants
	// ============================================================
	Context("CloudflareDNS Ready invariants", func() {
		It("should satisfy all structural invariants when Ready=True [INV-D1..D8]", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			By("Creating an HTTPRoute with hostname for DNS")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			hostname := fmt.Sprintf("%s.%s", testID("inv-dns"), testEnv.CloudflareZoneName)
			routeName := testID("route")

			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						// User-defined filter key (NOT a cfgate system annotation).
						// The CloudflareDNS annotationFilter matches this value.
						"e2e/invariant-filter": "dns-inv-test",
					},
				},
				Spec: gatewayv1.HTTPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{
							{
								Name:      gatewayv1.ObjectName(gwName),
								Namespace: (*gatewayv1.Namespace)(&namespace.Name),
							},
						},
					},
					Hostnames: []gatewayv1.Hostname{gatewayv1.Hostname(hostname)},
					Rules: []gatewayv1.HTTPRouteRule{
						{
							BackendRefs: []gatewayv1.HTTPBackendRef{
								{
									BackendRef: gatewayv1.BackendRef{
										BackendObjectReference: gatewayv1.BackendObjectReference{
											Name: gatewayv1.ObjectName(svcName),
											Port: ptrTo(gatewayv1.PortNumber(8080)),
										},
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, route)).To(Succeed())

			By("Creating CloudflareDNS with GatewayRoutes source")
			dnsName := testID("dns")
			dnsResource := createCloudflareDNSWithGatewayRoutes(ctx, k8sClient,
				dnsName, namespace.Name,
				sharedTunnel.Name,
				[]string{testEnv.CloudflareZoneName},
				"e2e/invariant-filter=dns-inv-test")

			By("Waiting for DNS to reach Ready=True")
			Eventually(func() bool {
				var d cfgatev1alpha1.CloudflareDNS
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: dnsResource.Name, Namespace: dnsResource.Namespace}, &d); err != nil {
					return false
				}
				cond := findCondition(d.Status.Conditions, "Ready")
				return cond != nil && cond.Status == metav1.ConditionTrue
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "CloudflareDNS must reach Ready=True")

			var dns cfgatev1alpha1.CloudflareDNS
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: dnsResource.Name, Namespace: dnsResource.Namespace}, &dns)).To(Succeed())

			By("INV-D1: Core sub-conditions must be True when Ready=True")
			// Condition names from status/conditions.go via CloudflareDNS controller.
			// NOTE: OwnershipVerified is intentionally excluded -- it's non-fatal.
			for _, condType := range []string{
				"CredentialsValid",
				"ZonesResolved",
				"RecordsSynced",
			} {
				cond := findCondition(dns.Status.Conditions, condType)
				Expect(cond).NotTo(BeNil(), "Condition %s must exist", condType)
				Expect(cond.Status).To(Equal(metav1.ConditionTrue),
					"Condition %s must be True when Ready=True, got %s (reason: %s)",
					condType, cond.Status, cond.Reason)
			}

			By("INV-D2: SyncedRecords must be >= 1")
			// NOTE: FailedRecords can be >0 even with Ready=True (per-record failures are non-fatal).
			Expect(dns.Status.SyncedRecords).To(BeNumerically(">=", 1),
				"SyncedRecords must be >= 1 when Ready=True")

			By("INV-D3: ResolvedTarget must be populated")
			Expect(dns.Status.ResolvedTarget).NotTo(BeEmpty(),
				"ResolvedTarget must be populated when Ready=True")

			By("INV-D4: ResolvedTarget must match tunnel domain (tunnelRef mode)")
			var tunnel cfgatev1alpha1.CloudflareTunnel
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: sharedTunnel.Name, Namespace: sharedTunnel.Namespace}, &tunnel)).To(Succeed())
			Expect(dns.Status.ResolvedTarget).To(Equal(tunnel.Status.TunnelDomain),
				"ResolvedTarget (%s) must match tunnel's TunnelDomain (%s)",
				dns.Status.ResolvedTarget, tunnel.Status.TunnelDomain)

			By("INV-D5: Finalizer must be present")
			Expect(dns.Finalizers).To(ContainElement("cfgate.io/dns-cleanup"),
				"Finalizer cfgate.io/dns-cleanup must be present")

			By("INV-D6: ObservedGeneration must match")
			Expect(dns.Status.ObservedGeneration).To(Equal(dns.Generation),
				"ObservedGeneration must match Generation")

			By("INV-D7: DNS record must exist in Cloudflare API")
			record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
			Expect(err).NotTo(HaveOccurred())
			Expect(record).NotTo(BeNil(), "CNAME record must exist in Cloudflare when Ready=True")
			Expect(record.Content).To(Equal(tunnel.Status.TunnelDomain),
				"CNAME content (%s) must point to tunnel domain (%s)",
				record.Content, tunnel.Status.TunnelDomain)

			By("INV-D8: OwnershipVerified condition exists (non-fatal)")
			ownerCond := findCondition(dns.Status.Conditions, "OwnershipVerified")
			Expect(ownerCond).NotTo(BeNil(),
				"OwnershipVerified condition should exist (but True/False does NOT gate Ready)")
		})
	})

	// ============================================================
	// I3: CloudflareAccessPolicy Ready Invariants
	// ============================================================
	Context("CloudflareAccessPolicy Ready invariants", func() {
		It("should satisfy all structural invariants when Ready=True [INV-A1..A8]", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			By("Creating an HTTPRoute for the policy target")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			hostname := fmt.Sprintf("%s.%s", testID("inv-access"), testEnv.CloudflareZoneName)
			routeName := testID("route")
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gwName, []string{hostname}, svcName, 8080)

			By("Creating CloudflareAccessPolicy")
			policyName := testID("policy")
			policy := createCloudflareAccessPolicy(ctx, k8sClient, policyName, namespace.Name, routeName, hostname)

			By("Waiting for AccessPolicy to reach Ready=True")
			waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			var ap cfgatev1alpha1.CloudflareAccessPolicy
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: policy.Name, Namespace: policy.Namespace}, &ap)).To(Succeed())
			app := waitForAccessApplicationReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("INV-A1: Policy sub-conditions must be True when Ready=True")
			for _, condType := range []string{
				"CredentialsValid",
				"PolicySynced",
			} {
				cond := findCondition(ap.Status.Conditions, condType)
				Expect(cond).NotTo(BeNil(), "Condition %s must exist", condType)
				Expect(cond.Status).To(Equal(metav1.ConditionTrue),
					"Condition %s must be True when Ready=True, got %s (reason: %s)",
					condType, cond.Status, cond.Reason)
			}

			By("INV-A2: Application sub-conditions must be True when Ready=True")
			for _, condType := range []string{
				"CredentialsValid",
				"TargetsResolved",
				"ReferenceGrantValid",
				"PoliciesResolved",
				"ApplicationSynced",
				"PoliciesLinked",
			} {
				cond := findCondition(app.Status.Conditions, condType)
				Expect(cond).NotTo(BeNil(), "Condition %s must exist", condType)
				Expect(cond.Status).To(Equal(metav1.ConditionTrue),
					"Condition %s must be True when Ready=True, got %s (reason: %s)",
					condType, cond.Status, cond.Reason)
			}

			By("INV-A3: Application ID must be populated")
			Expect(firstAccessApplicationID(app)).NotTo(BeEmpty(),
				"Application ID must be populated when Ready=True")

			By("INV-A4: AttachedTargets must be >= 1")
			Expect(app.Status.AttachedTargets).To(BeNumerically(">=", 1),
				"AttachedTargets must be >= 1 when Ready=True")

			By("INV-A5: Finalizer must be present")
			Expect(ap.Finalizers).To(ContainElement("cfgate.io/access-policy-cleanup"),
				"Finalizer cfgate.io/access-policy-cleanup must be present")

			By("INV-A6: ObservedGeneration must match")
			Expect(ap.Status.ObservedGeneration).To(Equal(ap.Generation),
				"ObservedGeneration must match Generation")

			By("INV-A7: Access Application must exist in Cloudflare API")
			cfApp, err := getAccessApplicationByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, firstAccessApplicationID(app))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfApp).NotTo(BeNil(), "Access Application must exist in Cloudflare when Ready=True")

			By("INV-A8: Cloudflare app domain and name must match")
			Expect(cfApp.Domain).To(Equal(hostname),
				"Cloudflare app domain (%s) must match hostname (%s)",
				cfApp.Domain, hostname)
			Expect(cfApp.Name).To(Equal(policyName),
				"Cloudflare app name (%s) must match application name (%s)",
				cfApp.Name, policyName)
		})

		It("should satisfy service token invariants when Ready=True [INV-ST1..ST4]", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			By("Creating an HTTPRoute for the policy target")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			hostname := fmt.Sprintf("%s.%s", testID("inv-token"), testEnv.CloudflareZoneName)
			routeName := testID("route")
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gwName, []string{hostname}, svcName, 8080)

			By("Creating CloudflareAccessPolicy with service token")
			policyName := testID("st-policy")
			tokenSecretName := testID("token-secret")
			policy := createCloudflareAccessPolicyWithServiceToken(ctx, k8sClient,
				policyName, namespace.Name, routeName, hostname, tokenSecretName)

			By("Waiting for AccessPolicy to reach Ready=True")
			waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			var ap cfgatev1alpha1.CloudflareAccessPolicy
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: policy.Name, Namespace: policy.Namespace}, &ap)).To(Succeed())

			By("INV-A2: ServiceTokensReady condition must be True (gated in Ready formula)")
			stCond := findCondition(ap.Status.Conditions, "ServiceTokensReady")
			Expect(stCond).NotTo(BeNil(), "ServiceTokensReady condition must exist when service tokens configured")
			Expect(stCond.Status).To(Equal(metav1.ConditionTrue),
				"ServiceTokensReady must be True when Ready=True")

			By("INV-ST1: ServiceTokenIDs map must have entry for each configured token")
			Expect(ap.Status.ServiceTokenIDs).NotTo(BeEmpty(),
				"ServiceTokenIDs must be populated")
			expectedTokenName := policyName + "-token"
			Expect(ap.Status.ServiceTokenIDs).To(HaveKey(expectedTokenName),
				"ServiceTokenIDs must contain token %s", expectedTokenName)
			Expect(ap.Status.ServiceTokenIDs[expectedTokenName]).NotTo(BeEmpty(),
				"Service token ID must be non-empty")

			By("INV-ST2: Service token Secret must exist with required keys")
			secret := waitForServiceTokenSecretCreated(ctx, k8sClient, tokenSecretName, namespace.Name, DefaultTimeout)
			Expect(secret.Data).To(HaveKey("CF_ACCESS_CLIENT_ID"),
				"Secret must contain CF_ACCESS_CLIENT_ID")
			Expect(secret.Data).To(HaveKey("CF_ACCESS_CLIENT_SECRET"),
				"Secret must contain CF_ACCESS_CLIENT_SECRET")
			Expect(secret.Data["CF_ACCESS_CLIENT_ID"]).NotTo(BeEmpty(),
				"CF_ACCESS_CLIENT_ID must be non-empty")
			Expect(secret.Data["CF_ACCESS_CLIENT_SECRET"]).NotTo(BeEmpty(),
				"CF_ACCESS_CLIENT_SECRET must be non-empty")

			By("INV-ST3: Service token must exist in Cloudflare API")
			cfToken, err := getServiceTokenFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, expectedTokenName)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfToken).NotTo(BeNil(), "Service token must exist in Cloudflare when Ready=True")
			Expect(cfToken.ID).To(Equal(ap.Status.ServiceTokenIDs[expectedTokenName]),
				"Cloudflare token ID must match status.serviceTokenIDs")
		})
	})

	// ============================================================
	// I4: Gateway Status Invariants
	// ============================================================
	Context("Gateway status invariants", func() {
		It("should satisfy Gateway conditions when tunnel is Ready [INV-GW1..GW4]", SpecTimeout(3*time.Minute), func(ctx SpecContext) {
			By("Waiting for Gateway to reflect tunnel status")
			var gw gatewayv1.Gateway
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: gwName, Namespace: namespace.Name}, &gw); err != nil {
					return false
				}
				for _, cond := range gw.Status.Conditions {
					if cond.Type == string(gatewayv1.GatewayConditionProgrammed) && cond.Status == metav1.ConditionTrue {
						return true
					}
				}
				return false
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "Gateway must reach Programmed=True")

			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: gwName, Namespace: namespace.Name}, &gw)).To(Succeed())

			By("INV-GW1: Accepted condition must be True")
			var acceptedFound bool
			for _, cond := range gw.Status.Conditions {
				if cond.Type == string(gatewayv1.GatewayConditionAccepted) {
					Expect(cond.Status).To(Equal(metav1.ConditionTrue),
						"Gateway Accepted must be True, got %s (reason: %s)", cond.Status, cond.Reason)
					acceptedFound = true
				}
			}
			Expect(acceptedFound).To(BeTrue(), "Gateway must have Accepted condition")

			By("INV-GW2: Programmed condition must be True")
			var programmedFound bool
			for _, cond := range gw.Status.Conditions {
				if cond.Type == string(gatewayv1.GatewayConditionProgrammed) {
					Expect(cond.Status).To(Equal(metav1.ConditionTrue),
						"Gateway Programmed must be True, got %s (reason: %s)", cond.Status, cond.Reason)
					programmedFound = true
				}
			}
			Expect(programmedFound).To(BeTrue(), "Gateway must have Programmed condition")

			By("INV-GW3: Addresses must contain tunnel domain")
			var tunnel cfgatev1alpha1.CloudflareTunnel
			Expect(k8sClient.Get(ctx, client.ObjectKey{
				Name:      sharedTunnel.Name,
				Namespace: sharedTunnel.Namespace,
			}, &tunnel)).To(Succeed())

			Expect(gw.Status.Addresses).NotTo(BeEmpty(), "Gateway must have addresses when Programmed=True")
			var foundTunnelDomain bool
			for _, addr := range gw.Status.Addresses {
				if addr.Value == tunnel.Status.TunnelDomain {
					Expect(string(*addr.Type)).To(Equal(string(gatewayv1.HostnameAddressType)),
						"Tunnel domain address must be Hostname type")
					foundTunnelDomain = true
				}
			}
			Expect(foundTunnelDomain).To(BeTrue(),
				"Gateway addresses must contain tunnel domain %s", tunnel.Status.TunnelDomain)

			By("INV-GW4: Listeners must support HTTPRoute")
			Expect(gw.Status.Listeners).NotTo(BeEmpty(), "Gateway must have listener status")
			for _, listener := range gw.Status.Listeners {
				var supportsHTTPRoute bool
				for _, kind := range listener.SupportedKinds {
					if kind.Kind == "HTTPRoute" {
						supportsHTTPRoute = true
					}
				}
				Expect(supportsHTTPRoute).To(BeTrue(),
					"Listener %s must support HTTPRoute", listener.Name)
			}
		})
	})

	// ============================================================
	// I5: HTTPRoute Parent Status Invariants
	// ============================================================
	Context("HTTPRoute parent status invariants", func() {
		It("should have cfgate parent status with correct conditions [INV-HR1..HR3]", SpecTimeout(3*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute with valid backend")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			hostname := fmt.Sprintf("%s.%s", testID("inv-hr"), testEnv.CloudflareZoneName)
			routeName := testID("hr-route")
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gwName, []string{hostname}, svcName, 8080)

			By("Waiting for HTTPRoute parent status to be set")
			var hr gatewayv1.HTTPRoute
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: routeName, Namespace: namespace.Name}, &hr); err != nil {
					return false
				}
				for _, parent := range hr.Status.Parents {
					if string(parent.ControllerName) == "cfgate.io/cloudflare-tunnel-controller" {
						return true
					}
				}
				return false
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "HTTPRoute must have cfgate parent status")

			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: routeName, Namespace: namespace.Name}, &hr)).To(Succeed())

			By("INV-HR1: parents[] must contain cfgate controller entry")
			var cfgateParent *gatewayv1.RouteParentStatus
			for i, parent := range hr.Status.Parents {
				if string(parent.ControllerName) == "cfgate.io/cloudflare-tunnel-controller" {
					cfgateParent = &hr.Status.Parents[i]
					break
				}
			}
			Expect(cfgateParent).NotTo(BeNil(),
				"HTTPRoute must have parent status with controllerName cfgate.io/cloudflare-tunnel-controller")

			By("INV-HR2: Accepted must be True")
			var acceptedFound bool
			for _, cond := range cfgateParent.Conditions {
				if cond.Type == string(gatewayv1.RouteConditionAccepted) {
					Expect(cond.Status).To(Equal(metav1.ConditionTrue),
						"HTTPRoute Accepted must be True for cfgate parent, got %s (reason: %s)",
						cond.Status, cond.Reason)
					acceptedFound = true
				}
			}
			Expect(acceptedFound).To(BeTrue(), "cfgate parent status must have Accepted condition")

			By("INV-HR3: ResolvedRefs must be True (backend service exists)")
			var resolvedFound bool
			for _, cond := range cfgateParent.Conditions {
				if cond.Type == string(gatewayv1.RouteConditionResolvedRefs) {
					Expect(cond.Status).To(Equal(metav1.ConditionTrue),
						"HTTPRoute ResolvedRefs must be True, got %s (reason: %s)",
						cond.Status, cond.Reason)
					resolvedFound = true
				}
			}
			Expect(resolvedFound).To(BeTrue(), "cfgate parent status must have ResolvedRefs condition")
		})
	})

	// ============================================================
	// I6: GatewayClass Invariants
	// ============================================================
	Context("GatewayClass invariants", func() {
		It("should set Accepted condition on GatewayClass managed by cfgate [INV-GC1..GC2]", SpecTimeout(2*time.Minute), func(ctx SpecContext) {
			By("Reading the shared GatewayClass")
			var gc gatewayv1.GatewayClass
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: gcName}, &gc)).To(Succeed())

			By("INV-GC1: ControllerName must match cfgate controller")
			Expect(string(gc.Spec.ControllerName)).To(Equal("cfgate.io/cloudflare-tunnel-controller"),
				"GatewayClass controllerName must be cfgate.io/cloudflare-tunnel-controller")

			By("INV-GC2: Accepted condition should be True")
			Eventually(func() bool {
				Expect(k8sClient.Get(ctx, client.ObjectKey{Name: gcName}, &gc)).To(Succeed())
				for _, cond := range gc.Status.Conditions {
					if cond.Type == string(gatewayv1.GatewayClassConditionStatusAccepted) &&
						cond.Status == metav1.ConditionTrue {
						return true
					}
				}
				return false
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(),
				"GatewayClass must have Accepted=True condition")
		})
	})

	// ============================================================
	// I7: Cross-CRD Consistency Invariants
	// ============================================================
	Context("cross-CRD consistency", func() {
		It("should maintain consistent tunnel domain across DNS and tunnel resources [INV-X1..X2]", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			By("Reading the shared tunnel's current state")
			var tunnel cfgatev1alpha1.CloudflareTunnel
			Expect(k8sClient.Get(ctx, client.ObjectKey{
				Name:      sharedTunnel.Name,
				Namespace: sharedTunnel.Namespace,
			}, &tunnel)).To(Succeed())
			tunnelDomain := tunnel.Status.TunnelDomain
			Expect(tunnelDomain).NotTo(BeEmpty())

			By("Creating DNS resource pointing to the same tunnel")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			hostname := fmt.Sprintf("%s.%s", testID("inv-cross"), testEnv.CloudflareZoneName)
			routeName := testID("route")

			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"e2e/invariant-filter": "cross-crd-test",
					},
				},
				Spec: gatewayv1.HTTPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{
							{
								Name:      gatewayv1.ObjectName(gwName),
								Namespace: (*gatewayv1.Namespace)(&namespace.Name),
							},
						},
					},
					Hostnames: []gatewayv1.Hostname{gatewayv1.Hostname(hostname)},
					Rules: []gatewayv1.HTTPRouteRule{
						{
							BackendRefs: []gatewayv1.HTTPBackendRef{
								{
									BackendRef: gatewayv1.BackendRef{
										BackendObjectReference: gatewayv1.BackendObjectReference{
											Name: gatewayv1.ObjectName(svcName),
											Port: ptrTo(gatewayv1.PortNumber(8080)),
										},
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, route)).To(Succeed())

			dns := createCloudflareDNSWithGatewayRoutes(ctx, k8sClient,
				testID("cross-dns"), namespace.Name,
				sharedTunnel.Name,
				[]string{testEnv.CloudflareZoneName},
				"e2e/invariant-filter=cross-crd-test")

			By("Waiting for DNS Ready=True")
			Eventually(func() bool {
				var d cfgatev1alpha1.CloudflareDNS
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: dns.Name, Namespace: dns.Namespace}, &d); err != nil {
					return false
				}
				cond := findCondition(d.Status.Conditions, "Ready")
				return cond != nil && cond.Status == metav1.ConditionTrue
			}, DefaultTimeout, DefaultInterval).Should(BeTrue())

			var dnsStatus cfgatev1alpha1.CloudflareDNS
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: dns.Name, Namespace: dns.Namespace}, &dnsStatus)).To(Succeed())

			By("INV-X1: DNS ResolvedTarget must equal tunnel TunnelDomain")
			Expect(dnsStatus.Status.ResolvedTarget).To(Equal(tunnelDomain),
				"DNS ResolvedTarget (%s) must equal Tunnel TunnelDomain (%s)",
				dnsStatus.Status.ResolvedTarget, tunnelDomain)

			By("INV-X2: Cloudflare CNAME record content must equal tunnel domain")
			record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
			Expect(err).NotTo(HaveOccurred())
			Expect(record).NotTo(BeNil())
			Expect(record.Content).To(Equal(tunnelDomain),
				"CNAME content (%s) must point to tunnel domain (%s)",
				record.Content, tunnelDomain)
		})

		It("should inherit credentials through HTTPRoute -> Gateway -> Tunnel chain [INV-X3]", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			By("Creating an HTTPRoute targeting the shared Gateway")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			hostname := fmt.Sprintf("%s.%s", testID("inv-cred"), testEnv.CloudflareZoneName)
			routeName := testID("cred-route")
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gwName, []string{hostname}, svcName, 8080)

			By("Creating reusable AccessPolicy and AccessApplication WITHOUT explicit cloudflareRef (must inherit)")
			policyName := testID("cred-policy")
			policy := createReusableAccessPolicy(ctx, k8sClient, policyName, namespace.Name, "allow",
				[]cfgatev1alpha1.AccessRule{{Everyone: ptrTo(true)}}, nil)
			app := &cfgatev1alpha1.CloudflareAccessApplication{
				ObjectMeta: metav1.ObjectMeta{
					Name:      policyName,
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareAccessApplicationSpec{
					TargetRef: &cfgatev1alpha1.PolicyTargetReference{
						Group: "gateway.networking.k8s.io",
						Kind:  "HTTPRoute",
						Name:  routeName,
					},
					Application: cfgatev1alpha1.AccessApplication{
						Name: policyName,
					},
					PolicyRefs: []cfgatev1alpha1.AccessPolicyReference{{Name: policyName}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			By("Waiting for AccessPolicy and AccessApplication to reach Ready=True")
			waitForAccessPolicyReady(ctx, k8sClient, policy.Name, namespace.Name, DefaultTimeout)
			readyApp := waitForAccessApplicationReady(ctx, k8sClient, app.Name, namespace.Name, DefaultTimeout)

			Expect(findCondition(readyApp.Status.Conditions, "CredentialsValid")).NotTo(BeNil())
			Expect(findCondition(readyApp.Status.Conditions, "CredentialsValid").Status).To(Equal(metav1.ConditionTrue),
				"CredentialsValid must be True via inherited credentials")
			Expect(firstAccessApplicationID(readyApp)).NotTo(BeEmpty(),
				"ApplicationID must be populated (proves CF API call succeeded with inherited creds)")
		})
	})

	// ============================================================
	// I8: Deletion Invariants
	// ============================================================
	Context("deletion invariants", func() {
		It("should clean up Cloudflare resources and remove finalizers on CR deletion [INV-DEL1..DEL4]", SpecTimeout(8*time.Minute), func(ctx SpecContext) {
			By("Creating a separate namespace for deletion test")
			delNS := createTestNamespace("cfgate-del-inv-e2e")
			createCloudflareCredentialsSecret(delNS.Name)

			By("Creating tunnel")
			tunnelName := testID("del-tunnel")
			tunnel := createCloudflareTunnel(ctx, k8sClient, testID("del-t"), delNS.Name, tunnelName)
			tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, delNS.Name, DefaultTimeout)
			tunnelID := tunnel.Status.TunnelID

			By("Creating GatewayClass + Gateway for deletion test")
			delGCName := testID("del-gc")
			createGatewayClass(ctx, k8sClient, delGCName)
			tunnelRef := fmt.Sprintf("%s/%s", delNS.Name, tunnel.Name)
			createGateway(ctx, k8sClient, testID("del-gw"), delNS.Name, delGCName, tunnelRef)

			By("Creating DNS with explicit hostname")
			hostname := fmt.Sprintf("%s.%s", testID("del-dns"), testEnv.CloudflareZoneName)
			dnsResource := &cfgatev1alpha1.CloudflareDNS{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("del-d"),
					Namespace: delNS.Name,
				},
				Spec: cfgatev1alpha1.CloudflareDNSSpec{
					TunnelRef: &cfgatev1alpha1.DNSTunnelRef{
						Name: tunnel.Name,
					},
					Zones: []cfgatev1alpha1.DNSZoneConfig{
						{Name: testEnv.CloudflareZoneName},
					},
					Policy: cfgatev1alpha1.DNSPolicySync,
					Source: cfgatev1alpha1.DNSHostnameSource{
						Explicit: []cfgatev1alpha1.DNSExplicitHostname{
							{Hostname: hostname},
						},
					},
					// FallbackCredentialsRef points to the shared namespace's credentials.
					// During namespace deletion, the tunnel CR and its secret may be GC'd
					// before the DNS finalizer runs. Without fallback, the DNS controller
					// cannot authenticate to delete records, leaving them orphaned.
					FallbackCredentialsRef: &cfgatev1alpha1.SecretReference{
						Name:      "cloudflare-credentials",
						Namespace: namespace.Name,
					},
				},
			}
			Expect(k8sClient.Create(ctx, dnsResource)).To(Succeed())

			By("Waiting for DNS record to appear in Cloudflare")
			Eventually(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				return err == nil && record != nil
			}, DefaultTimeout, DefaultInterval).Should(BeTrue())

			By("Creating AccessPolicy with explicit credentials")
			delSvc := createTestService(ctx, k8sClient, testID("del-svc"), delNS.Name, 8080)
			delRouteName := testID("del-route")
			createHTTPRoute(ctx, k8sClient, delRouteName, delNS.Name, testID("del-gw"), []string{hostname}, delSvc.Name, 8080)

			policyName := testID("del-policy")
			policy := createCloudflareAccessPolicy(ctx, k8sClient, policyName, delNS.Name, delRouteName, hostname)
			waitForAccessPolicyReady(ctx, k8sClient, policy.Name, delNS.Name, DefaultTimeout)
			delApp := waitForAccessApplicationReady(ctx, k8sClient, policyName, delNS.Name, DefaultTimeout)
			appID := firstAccessApplicationID(delApp)
			Expect(appID).NotTo(BeEmpty())

			By("INV-DEL1: Deleting namespace triggers finalizer-based cleanup")
			deleteTestNamespace(delNS)

			By("INV-DEL2: Tunnel must be deleted from Cloudflare after finalizer runs")
			Eventually(func() bool {
				t, err := getTunnelByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, tunnelID)
				if err != nil {
					return false
				}
				return t == nil || !t.DeletedAt.IsZero()
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(),
				"Tunnel must be deleted from Cloudflare")

			By("INV-DEL3: DNS record must be deleted from Cloudflare after finalizer runs")
			Eventually(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				if err != nil {
					GinkgoWriter.Printf("INV-DEL3 DNS check error: %v\n", err)
					return false
				}
				if record != nil {
					GinkgoWriter.Printf("INV-DEL3 DNS record still exists: ID=%s, Content=%s, Comment=%q\n", record.ID, record.Content, record.Comment)
				}
				return record == nil
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(),
				"DNS record must be deleted from Cloudflare")

			By("INV-DEL4: Access Application must be deleted from Cloudflare after finalizer runs")
			Eventually(func() bool {
				app, err := getAccessApplicationByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, appID)
				if err != nil {
					return false
				}
				return app == nil
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(),
				"Access Application must be deleted from Cloudflare")
		})
	})
})

// findCondition returns the condition with the given type, or nil if not found.
func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

// Package e2e contains end-to-end tests for cfgate.
// combined_test.go tests multi-resource interactions and complex scenarios.
package e2e_test

import (
	"fmt"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	cloudflare "github.com/cloudflare/cloudflare-go/v6"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
)

var _ = Describe("Multi-Resource E2E", Label("cloudflare"), Ordered, func() {
	var (
		namespace    *corev1.Namespace
		cfClient     *cloudflare.Client
		zoneID       string
		sharedTunnel *cfgatev1alpha1.CloudflareTunnel
		gcName       string
		gwName       string
	)

	BeforeAll(func() {
		skipIfNoZone() // Combined tests need DNS zone for DNS+Access scenarios

		// Create unique namespace for combined tests
		namespace = createTestNamespace("cfgate-combined-e2e")

		// Generate unique names
		gcName = testID("gc")
		gwName = testID("gw")

		// Create Cloudflare credentials secret
		createCloudflareCredentialsSecret(namespace.Name)

		// Create Cloudflare client for verification
		cfClient = getCloudflareClient()

		// Get zone ID for DNS operations
		var err error
		zoneID, err = getZoneIDByName(ctx, cfClient, testEnv.CloudflareZoneName)
		Expect(err).NotTo(HaveOccurred(), "Failed to get zone ID")
		Expect(zoneID).NotTo(BeEmpty())

		// Create shared tunnel for combined tests
		By("Creating shared CloudflareTunnel for combined tests")
		tunnelName := testID("combined-tunnel")
		sharedTunnel = createCloudflareTunnel(ctx, k8sClient, testID("combined"), namespace.Name, tunnelName)
		sharedTunnel = waitForTunnelReady(ctx, k8sClient, sharedTunnel.Name, sharedTunnel.Namespace, DefaultTimeout)

		// Create shared GatewayClass
		By("Creating shared GatewayClass")
		createGatewayClass(ctx, k8sClient, gcName)

		// Create shared Gateway
		By("Creating shared Gateway")
		tunnelRef := fmt.Sprintf("%s/%s", namespace.Name, sharedTunnel.Name)
		createGateway(ctx, k8sClient, gwName, namespace.Name, gcName, tunnelRef)

		DeferCleanup(func() {
			if testEnv.SkipCleanup {
				return
			}
			// Delete namespace - controller finalizers will handle Cloudflare cleanup
			if namespace != nil {
				deleteTestNamespace(namespace)
			}
		})
	})

	Context("DNS and Access combined", func() {
		It("should create both DNS record and Access policy for HTTPRoute with both enabled", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			By("Creating test Service")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			By("Creating HTTPRoute with dns-sync annotation")
			hostname := fmt.Sprintf("%s.%s", testID("combined"), testEnv.CloudflareZoneName)
			routeName := testID("route")
			policyName := testID("policy")

			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/dns-sync": "combined-create",
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

			By("Creating CloudflareDNS to sync the route's hostname")
			dns := createCloudflareDNSWithGatewayRoutes(ctx, k8sClient,
				testID("dns"), namespace.Name,
				sharedTunnel.Name,
				[]string{testEnv.CloudflareZoneName},
				"cfgate.io/dns-sync=combined-create")

			By("Creating CloudflareAccessPolicy targeting the route")
			policy := createCloudflareAccessPolicy(ctx, k8sClient, policyName, namespace.Name, routeName, hostname)

			By("Waiting for DNS record to be created")
			Eventually(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				return err == nil && record != nil
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "DNS record should be created")

			By("Waiting for Access Application to be created")
			waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)
			Eventually(func() bool {
				app, err := getAccessApplicationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policyName)
				return err == nil && app != nil && app.Domain == hostname
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "Access Application should be created with matching domain")

			By("Verifying both resources are in sync")
			var dnsStatus cfgatev1alpha1.CloudflareDNS
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: dns.Name, Namespace: dns.Namespace}, &dnsStatus)).To(Succeed())
			Expect(dnsStatus.Status.SyncedRecords).To(BeNumerically(">", 0), "DNS should have synced records")

			var policyStatus cfgatev1alpha1.CloudflareAccessPolicy
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: policy.Name, Namespace: policy.Namespace}, &policyStatus)).To(Succeed())
			Expect(policyStatus.Status.ApplicationID).NotTo(BeEmpty(), "AccessPolicy should have application ID")
		})

		It("should clean up both DNS record and Access policy on HTTPRoute deletion", SpecTimeout(12*time.Minute), func(ctx SpecContext) {
			By("Creating test Service")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			By("Creating HTTPRoute")
			hostname := fmt.Sprintf("%s.%s", testID("cleanup"), testEnv.CloudflareZoneName)
			routeName := testID("route")
			policyName := testID("policy")

			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/dns-sync": "combined-cleanup",
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

			By("Creating CloudflareDNS with cleanup enabled")
			dns := &cfgatev1alpha1.CloudflareDNS{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("dns"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareDNSSpec{
					TunnelRef: &cfgatev1alpha1.DNSTunnelRef{
						Name: sharedTunnel.Name,
					},
					Zones: []cfgatev1alpha1.DNSZoneConfig{
						{Name: testEnv.CloudflareZoneName},
					},
					Policy: cfgatev1alpha1.DNSPolicySync,
					Source: cfgatev1alpha1.DNSHostnameSource{
						GatewayRoutes: &cfgatev1alpha1.DNSGatewayRoutesSource{
							Enabled:          true,
							AnnotationFilter: "cfgate.io/dns-sync=combined-cleanup",
						},
					},
					CleanupPolicy: cfgatev1alpha1.DNSCleanupPolicy{
						DeleteOnRouteRemoval: ptrTo(true),
					},
				},
			}
			Expect(k8sClient.Create(ctx, dns)).To(Succeed())

			By("Creating CloudflareAccessPolicy")
			policy := createCloudflareAccessPolicy(ctx, k8sClient, policyName, namespace.Name, routeName, hostname)

			By("Waiting for resources to be created in Cloudflare")
			Eventually(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				return err == nil && record != nil
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "DNS record should be created")

			waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)
			Eventually(func() bool {
				app, err := getAccessApplicationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policyName)
				return err == nil && app != nil
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "Access Application should be created")

			By("Deleting the HTTPRoute")
			Expect(k8sClient.Delete(ctx, route)).To(Succeed())

			By("Waiting for DNS record to be cleaned up")
			Eventually(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				if err != nil {
					GinkgoWriter.Printf("DNS deletion check error: %v\n", err)
					return false
				}
				if record != nil {
					GinkgoWriter.Printf("DNS record still exists: ID=%s, Content=%s, Comment=%q\n", record.ID, record.Content, record.Comment)
				}
				return record == nil
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "DNS record should be deleted when route is removed")

			By("Deleting the AccessPolicy")
			Expect(k8sClient.Delete(ctx, policy)).To(Succeed())

			By("Waiting for Access Application to be cleaned up")
			waitForAccessApplicationDeletedFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policyName, LongTimeout)
		})
	})

	Context("concurrent modifications", func() {
		It("should handle concurrent HTTPRoute updates without race conditions", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			By("Creating test Service")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			By("Creating multiple HTTPRoutes")
			const numRoutes = 5
			var routes []*gatewayv1.HTTPRoute
			var hostnames []string

			for i := 0; i < numRoutes; i++ {
				hostname := fmt.Sprintf("%s-%d.%s", testID("concurrent"), i, testEnv.CloudflareZoneName)
				hostnames = append(hostnames, hostname)
				routeName := fmt.Sprintf("%s-%d", testID("route"), i)

				route := &gatewayv1.HTTPRoute{
					ObjectMeta: metav1.ObjectMeta{
						Name:      routeName,
						Namespace: namespace.Name,
						Annotations: map[string]string{
							"cfgate.io/dns-sync": "combined-concurrent",
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
				routes = append(routes, route)
			}

			By("Creating CloudflareDNS to watch all routes")
			dns := createCloudflareDNSWithGatewayRoutes(ctx, k8sClient,
				testID("dns"), namespace.Name,
				sharedTunnel.Name,
				[]string{testEnv.CloudflareZoneName},
				"cfgate.io/dns-sync=combined-concurrent")

			By("Waiting for all DNS records to be created")
			Eventually(func() int {
				var d cfgatev1alpha1.CloudflareDNS
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: dns.Name, Namespace: dns.Namespace}, &d); err != nil {
					return 0
				}
				return int(d.Status.SyncedRecords)
			}, LongTimeout, DefaultInterval).Should(BeNumerically(">=", numRoutes), "All DNS records should be synced")

			By("Concurrently updating all routes")
			var wg sync.WaitGroup
			for i, route := range routes {
				wg.Add(1)
				go func(idx int, r *gatewayv1.HTTPRoute) {
					defer wg.Done()
					defer GinkgoRecover()

					// Update the route's annotation
					var current gatewayv1.HTTPRoute
					Expect(k8sClient.Get(ctx, client.ObjectKey{Name: r.Name, Namespace: r.Namespace}, &current)).To(Succeed())
					if current.Annotations == nil {
						current.Annotations = make(map[string]string)
					}
					current.Annotations["cfgate.io/test-update"] = fmt.Sprintf("update-%d", idx)
					Expect(k8sClient.Update(ctx, &current)).To(Succeed())
				}(i, route)
			}
			wg.Wait()

			By("Verifying all DNS records still exist after concurrent updates")
			for _, hostname := range hostnames {
				Eventually(func() bool {
					record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
					return err == nil && record != nil
				}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "DNS record for %s should exist", hostname)
			}

			By("Verifying no duplicate records were created")
			var finalDNS cfgatev1alpha1.CloudflareDNS
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: dns.Name, Namespace: dns.Namespace}, &finalDNS)).To(Succeed())
			Expect(finalDNS.Status.SyncedRecords).To(BeNumerically("==", numRoutes), "Should have exactly %d records, no duplicates", numRoutes)
		})

		It("should handle concurrent tunnel config sync without conflicts", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			By("Creating test Service")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			By("Creating HTTPRoute")
			hostname := fmt.Sprintf("%s.%s", testID("configsync"), testEnv.CloudflareZoneName)
			routeName := testID("route")

			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
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

			By("Waiting for tunnel to sync configuration")
			Eventually(func() int32 {
				var t cfgatev1alpha1.CloudflareTunnel
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: sharedTunnel.Name, Namespace: sharedTunnel.Namespace}, &t); err != nil {
					return 0
				}
				return t.Status.ConnectedRouteCount
			}, DefaultTimeout, DefaultInterval).Should(BeNumerically(">", 0), "Tunnel should have connected routes")

			By("Concurrently updating tunnel spec and route")
			var wg sync.WaitGroup

			// Update tunnel spec (retry on conflict, controller may update status concurrently)
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer GinkgoRecover()

				Eventually(func() error {
					var t cfgatev1alpha1.CloudflareTunnel
					if err := k8sClient.Get(ctx, client.ObjectKey{Name: sharedTunnel.Name, Namespace: sharedTunnel.Namespace}, &t); err != nil {
						return err
					}
					if t.Annotations == nil {
						t.Annotations = make(map[string]string)
					}
					t.Annotations["cfgate.io/test-concurrent"] = "tunnel-update"
					return k8sClient.Update(ctx, &t)
				}, 30*time.Second, 1*time.Second).Should(Succeed())
			}()

			// Update route (retry on conflict, controller may update status concurrently)
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer GinkgoRecover()

				Eventually(func() error {
					var r gatewayv1.HTTPRoute
					if err := k8sClient.Get(ctx, client.ObjectKey{Name: routeName, Namespace: namespace.Name}, &r); err != nil {
						return err
					}
					if r.Annotations == nil {
						r.Annotations = make(map[string]string)
					}
					r.Annotations["cfgate.io/test-concurrent"] = "route-update"
					return k8sClient.Update(ctx, &r)
				}, 30*time.Second, 1*time.Second).Should(Succeed())
			}()

			wg.Wait()

			By("Verifying tunnel is still ready after concurrent updates")
			Eventually(func() bool {
				var t cfgatev1alpha1.CloudflareTunnel
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: sharedTunnel.Name, Namespace: sharedTunnel.Namespace}, &t); err != nil {
					return false
				}
				for _, cond := range t.Status.Conditions {
					if cond.Type == "Ready" && cond.Status == metav1.ConditionTrue {
						return true
					}
				}
				return false
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "Tunnel should remain ready")
		})
	})

	// ============================================================
	// §6.5: Cleanup Verification (No Orphaned CF Resources)
	// ============================================================
	Context("cleanup verification", func() {
		It("should clean up all Cloudflare resources when namespace is deleted", SpecTimeout(8*time.Minute), func(ctx SpecContext) {
			By("Creating a SEPARATE namespace for cleanup test")
			cleanupNS := createTestNamespace("cfgate-cleanup-e2e")
			createCloudflareCredentialsSecret(cleanupNS.Name)

			By("Creating tunnel in cleanup namespace")
			tunnelName := testID("cleanup-tunnel")
			tunnel := createCloudflareTunnel(ctx, k8sClient, testID("cleanup-t"), cleanupNS.Name, tunnelName)
			tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, cleanupNS.Name, DefaultTimeout)
			tunnelID := tunnel.Status.TunnelID
			Expect(tunnelID).NotTo(BeEmpty())

			By("Creating DNS in cleanup namespace")
			hostname := fmt.Sprintf("%s.%s", testID("cleanup-dns"), testEnv.CloudflareZoneName)
			dnsResource := &cfgatev1alpha1.CloudflareDNS{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("cleanup-d"),
					Namespace: cleanupNS.Name,
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
				},
			}
			Expect(k8sClient.Create(ctx, dnsResource)).To(Succeed())

			By("Creating Access policy in cleanup namespace")
			cleanupGCName := testID("cleanup-gc")
			createGatewayClass(ctx, k8sClient, cleanupGCName)
			cleanupGW := createGateway(ctx, k8sClient, testID("cleanup-gw"), cleanupNS.Name, cleanupGCName, "")
			cleanupSvc := createTestService(ctx, k8sClient, testID("cleanup-svc"), cleanupNS.Name, 8080)
			cleanupRouteName := testID("cleanup-route")
			createHTTPRoute(ctx, k8sClient, cleanupRouteName, cleanupNS.Name, cleanupGW.Name, []string{hostname}, cleanupSvc.Name, 8080)

			policyName := testID("cleanup-policy")
			policy := createCloudflareAccessPolicy(ctx, k8sClient, policyName, cleanupNS.Name, cleanupRouteName, hostname)

			By("Waiting for all resources to be ready")
			waitForAccessPolicyReady(ctx, k8sClient, policy.Name, cleanupNS.Name, DefaultTimeout)
			Eventually(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				return err == nil && record != nil
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "DNS record should exist")

			By("Recording CF resource IDs")
			cfApp, err := getAccessApplicationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policyName)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfApp).NotTo(BeNil())
			appID := cfApp.ID

			By("Deleting the cleanup namespace")
			deleteTestNamespace(cleanupNS)

			By("Verifying via CF API: tunnel is deleted (deleted_at set or 404)")
			Eventually(func() bool {
				t, err := getTunnelByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, tunnelID)
				if err != nil {
					return false
				}
				// Tunnel should be nil (404/not found) or have deleted_at set.
				// Note: CF API status enum is {inactive,degraded,healthy,down}; there is no
				// "deleted" status. Deletion is indicated by a non-zero deleted_at timestamp.
				return t == nil || !t.DeletedAt.IsZero()
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(),
				"Tunnel should be deleted from Cloudflare after namespace deletion")

			By("Verifying via CF API: DNS records gone")
			Eventually(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				if err != nil {
					GinkgoWriter.Printf("DNS deletion check error: %v\n", err)
					return false
				}
				if record != nil {
					GinkgoWriter.Printf("DNS record still exists: ID=%s, Content=%s, Comment=%q\n", record.ID, record.Content, record.Comment)
				}
				return record == nil
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(),
				"DNS record should be deleted from Cloudflare after namespace deletion")

			By("Verifying via CF API: Access application gone")
			Eventually(func() bool {
				app, err := getAccessApplicationByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, appID)
				if err != nil {
					return false
				}
				return app == nil
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(),
				"Access application should be deleted from Cloudflare after namespace deletion")
		})
	})

	// ============================================================
	// §6.6: Concurrent CR Creation (Stress/Race Detection)
	// ============================================================
	Context("concurrent CR creation", func() {
		It("should handle concurrent CR creation without duplicates", SpecTimeout(10*time.Minute), func(ctx SpecContext) {
			const numTunnels = 3

			By("Creating 3 CloudflareTunnel CRs simultaneously")
			tunnelNames := make([]string, numTunnels)
			crNames := make([]string, numTunnels)
			for i := 0; i < numTunnels; i++ {
				tunnelNames[i] = fmt.Sprintf("%s-%d", testID("concurrent-t"), i)
				crNames[i] = fmt.Sprintf("%s-%d", testID("concurrent-cr"), i)
			}

			var wg sync.WaitGroup
			for i := 0; i < numTunnels; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					defer GinkgoRecover()

					createCloudflareTunnel(ctx, k8sClient, crNames[idx], namespace.Name, tunnelNames[idx])
				}(i)
			}
			wg.Wait()

			By("Waiting for all 3 tunnels to become ready")
			tunnelIDs := make([]string, numTunnels)
			for i := 0; i < numTunnels; i++ {
				tunnel := waitForTunnelReady(ctx, k8sClient, crNames[i], namespace.Name, DefaultTimeout)
				tunnelIDs[i] = tunnel.Status.TunnelID
				Expect(tunnelIDs[i]).NotTo(BeEmpty(), "Tunnel %d should have a tunnel ID", i)
			}

			By("Verifying each CR's status.tunnelID is unique")
			uniqueIDs := make(map[string]bool)
			for i, id := range tunnelIDs {
				Expect(uniqueIDs).NotTo(HaveKey(id),
					"Tunnel ID %s is duplicated (tunnel %d)", id, i)
				uniqueIDs[id] = true
			}

			By("Listing all tunnels in CF API matching the test prefix")
			prefix := testID("concurrent-t")
			cfTunnels, err := listTunnelsByPrefixFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, prefix)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying exactly 3 tunnels exist (no duplicates)")
			Expect(cfTunnels).To(HaveLen(numTunnels),
				"Should have exactly %d tunnels in CF, got %d (possible duplicates)", numTunnels, len(cfTunnels))

			By("Verifying tunnel names match what we created")
			cfNames := make(map[string]bool)
			for _, t := range cfTunnels {
				cfNames[t.Name] = true
			}
			for _, name := range tunnelNames {
				Expect(cfNames).To(HaveKey(name),
					"CF should have tunnel named %s", name)
			}
		})
	})

	Context("cross-namespace references", func() {
		It("should resolve AccessPolicy from different namespace via ReferenceGrant", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			By("Creating a second namespace for the policy")
			policyNamespace := createTestNamespace("cfgate-policy-ns")
			createCloudflareCredentialsSecret(policyNamespace.Name)

			By("Creating test Service in main namespace")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			By("Creating HTTPRoute in main namespace")
			hostname := fmt.Sprintf("%s.%s", testID("crossns"), testEnv.CloudflareZoneName)
			routeName := testID("route")

			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
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

			By("Creating ReferenceGrant in main namespace to allow policy namespace to reference routes")
			rg := &gatewayv1b1.ReferenceGrant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("grant"),
					Namespace: namespace.Name, // Grant is in the target namespace (where routes are)
				},
				Spec: gatewayv1b1.ReferenceGrantSpec{
					From: []gatewayv1b1.ReferenceGrantFrom{
						{
							Group:     "cfgate.io",
							Kind:      "CloudflareAccessPolicy",
							Namespace: gatewayv1b1.Namespace(policyNamespace.Name),
						},
					},
					To: []gatewayv1b1.ReferenceGrantTo{
						{
							Group: "gateway.networking.k8s.io",
							Kind:  "HTTPRoute",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, rg)).To(Succeed())

			By("Creating CloudflareAccessPolicy in policy namespace targeting route in main namespace")
			policyName := testID("policy")
			everyone := true
			policy := &cfgatev1alpha1.CloudflareAccessPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      policyName,
					Namespace: policyNamespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
					TargetRef: &cfgatev1alpha1.PolicyTargetReference{
						Group:     "gateway.networking.k8s.io",
						Kind:      "HTTPRoute",
						Name:      routeName,
						Namespace: &namespace.Name, // Cross-namespace reference
					},
					CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
						Name:      "cloudflare-credentials",
						AccountID: testEnv.CloudflareAccountID,
					},
					Application: cfgatev1alpha1.AccessApplication{
						Name:   policyName,
						Domain: hostname,
					},
					Policies: []cfgatev1alpha1.AccessPolicyRule{
						{
							Name:     "allow-all",
							Decision: "allow",
							Include: []cfgatev1alpha1.AccessRule{
								{Everyone: &everyone},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())

			By("Waiting for AccessPolicy to become ready")
			waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying Access Application is created in Cloudflare")
			Eventually(func() bool {
				app, err := getAccessApplicationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policyName)
				return err == nil && app != nil && app.Domain == hostname
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "Access Application should be created with correct domain")

			By("Cleaning up policy namespace")
			if !testEnv.SkipCleanup {
				deleteTestNamespace(policyNamespace)
			}
		})
	})
})

// Package e2e contains end-to-end tests for cfgate.
package e2e_test

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	cloudflare "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/dns"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// CloudflareDNS E2E tests.
// Tests the CloudflareDNS composable CRD.
// CloudflareDNS manages DNS records independently from CloudflareTunnel.
var _ = Describe("CloudflareDNS E2E", Label("cloudflare"), Ordered, func() {
	var (
		namespace *corev1.Namespace
		cfClient  *cloudflare.Client
		zoneID    string

		// Shared tunnel for tests that need tunnelRef mode.
		sharedTunnel *cfgatev1alpha1.CloudflareTunnel
	)

	BeforeAll(func() {
		skipIfNoZone() // Requires zone in addition to credentials.

		// Create unique namespace for DNS tests.
		namespace = createTestNamespace("cfgate-dns-e2e")

		// Create Cloudflare credentials secret.
		createCloudflareCredentialsSecret(namespace.Name)

		// Create Cloudflare client for verification.
		cfClient = getCloudflareClient()

		// Get zone ID for DNS operations.
		var err error
		zoneID, err = getZoneIDByName(ctx, cfClient, testEnv.CloudflareZoneName)
		Expect(err).NotTo(HaveOccurred(), "Failed to get zone ID")
		Expect(zoneID).NotTo(BeEmpty())

		// Create a shared tunnel for tunnelRef tests.
		tunnelName := testID("dns-shared-tunnel")
		sharedTunnel = createCloudflareTunnel(ctx, k8sClient, testID("dns-shared"), namespace.Name, tunnelName)
		sharedTunnel = waitForTunnelReady(ctx, k8sClient, sharedTunnel.Name, namespace.Name, DefaultTimeout)
		Expect(sharedTunnel.Status.TunnelDomain).NotTo(BeEmpty(), "Shared tunnel domain should be populated")

		// Register cleanup via DeferCleanup (Ginkgo #1284 pattern).
		DeferCleanup(func() {
			if testEnv.SkipCleanup {
				return
			}
			// Delete namespace - controller finalizers will handle cleanup.
			if namespace != nil {
				deleteTestNamespace(namespace)
			}
		})
	})

	// =========================================================================
	// Section 1: TunnelRef Mode Tests
	// =========================================================================

	Context("tunnelRef mode", Ordered, func() {
		var (
			dnsResource *cfgatev1alpha1.CloudflareDNS
			hostname    string
		)

		It("creates CNAME record pointing to tunnel domain", SpecTimeout(6*time.Minute), func(ctx SpecContext) {
			By("Creating CloudflareDNS with tunnelRef")
			hostname = fmt.Sprintf("%s.%s", testID("tunnelref"), testEnv.CloudflareZoneName)
			dnsResource = createCloudflareDNSWithTunnelRef(ctx, k8sClient,
				testID("dns-tunnelref"), namespace.Name,
				sharedTunnel.Name, namespace.Name,
				[]string{hostname},
				cfgatev1alpha1.DNSPolicySync,
				true, // TXT ownership enabled
			)

			By("Waiting for CloudflareDNS to be ready")
			dnsResource = waitForDNSReady(ctx, k8sClient, dnsResource.Name, namespace.Name, DefaultTimeout)

			By("Verifying CNAME record exists pointing to tunnel domain")
			Eventually(func(g Gomega) {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(record).NotTo(BeNil(), "CNAME record should exist")
				g.Expect(record.Content).To(Equal(sharedTunnel.Status.TunnelDomain), "CNAME should point to tunnel domain")
			}, DefaultTimeout, DefaultInterval).Should(Succeed())
		})

		It("creates proxied DNS records by default", SpecTimeout(6*time.Minute), func(ctx SpecContext) {
			By("Verifying record is proxied (orange cloud)")
			Eventually(func(g Gomega) {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(record).NotTo(BeNil())
				g.Expect(record.Proxied).To(BeTrue(), "Record should be proxied by default")
			}, DefaultTimeout, DefaultInterval).Should(Succeed())
		})

		It("populates status with resolved target", func() {
			By("Verifying status.resolvedTarget matches tunnel domain")
			var current cfgatev1alpha1.CloudflareDNS
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: dnsResource.Name, Namespace: namespace.Name}, &current)).To(Succeed())
			Expect(current.Status.ResolvedTarget).To(Equal(sharedTunnel.Status.TunnelDomain))
		})

		It("tracks synced records in status", func() {
			By("Verifying status.syncedRecords count")
			var current cfgatev1alpha1.CloudflareDNS
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: dnsResource.Name, Namespace: namespace.Name}, &current)).To(Succeed())
			Expect(current.Status.SyncedRecords).To(BeNumerically(">=", 1), "Should have at least 1 synced record")
		})

		It("cleans up DNS record on CloudflareDNS deletion", SpecTimeout(6*time.Minute), func(ctx SpecContext) {
			By("Deleting the CloudflareDNS resource")
			Expect(k8sClient.Delete(ctx, dnsResource)).To(Succeed())

			By("Waiting for CloudflareDNS to be deleted from Kubernetes")
			waitForDNSDeleted(ctx, k8sClient, dnsResource.Name, namespace.Name, DefaultTimeout)

			By("Verifying DNS record is deleted from Cloudflare")
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
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "DNS record should be deleted on cleanup")
		})
	})

	// =========================================================================
	// Section 2: ExternalTarget Mode Tests
	// =========================================================================

	Context("externalTarget mode", Ordered, func() {
		var (
			dnsResource *cfgatev1alpha1.CloudflareDNS
			hostname    string
		)

		It("creates CNAME record with external target", SpecTimeout(6*time.Minute), func(ctx SpecContext) {
			By("Creating CloudflareDNS with externalTarget")
			hostname = fmt.Sprintf("%s.%s", testID("external"), testEnv.CloudflareZoneName)
			dnsResource = createCloudflareDNSWithExternalTarget(ctx, k8sClient,
				testID("dns-external"), namespace.Name,
				cfgatev1alpha1.RecordTypeCNAME,
				"example.com",
				[]string{hostname},
				cfgatev1alpha1.DNSPolicySync,
			)

			By("Waiting for CloudflareDNS to be ready")
			dnsResource = waitForDNSReady(ctx, k8sClient, dnsResource.Name, namespace.Name, DefaultTimeout)

			By("Verifying CNAME record exists with external target")
			Eventually(func(g Gomega) {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(record).NotTo(BeNil(), "CNAME record should exist")
				g.Expect(record.Content).To(Equal("example.com"), "CNAME should point to external target")
			}, DefaultTimeout, DefaultInterval).Should(Succeed())
		})

		It("populates status with external target value", func() {
			By("Verifying status.resolvedTarget matches external target")
			var current cfgatev1alpha1.CloudflareDNS
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: dnsResource.Name, Namespace: namespace.Name}, &current)).To(Succeed())
			Expect(current.Status.ResolvedTarget).To(Equal("example.com"))
		})

		It("cleans up on deletion", SpecTimeout(6*time.Minute), func(ctx SpecContext) {
			By("Deleting the CloudflareDNS resource")
			Expect(k8sClient.Delete(ctx, dnsResource)).To(Succeed())

			By("Waiting for DNS record cleanup")
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
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "DNS record should be deleted on cleanup")
		})
	})

	// =========================================================================
	// Section 3: DNS Policy Tests
	// =========================================================================

	Context("policy modes", func() {
		It("sync policy creates, updates, and deletes records", SpecTimeout(4*time.Minute), func(ctx SpecContext) {
			hostname := fmt.Sprintf("%s.%s", testID("sync-policy"), testEnv.CloudflareZoneName)

			By("Creating CloudflareDNS with sync policy")
			dnsResource := createCloudflareDNSWithTunnelRef(ctx, k8sClient,
				testID("dns-sync-policy"), namespace.Name,
				sharedTunnel.Name, namespace.Name,
				[]string{hostname},
				cfgatev1alpha1.DNSPolicySync,
				false,
			)
			dnsResource = waitForDNSReady(ctx, k8sClient, dnsResource.Name, namespace.Name, DefaultTimeout)

			By("Verifying CREATE: record is created")
			Eventually(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				return err == nil && record != nil
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "Record should be created")

			By("Verifying DELETE: deleting CloudflareDNS deletes records")
			Expect(k8sClient.Delete(ctx, dnsResource)).To(Succeed())
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
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "Record should be deleted with sync policy")
		})

		It("upsert-only policy creates but never deletes", SpecTimeout(4*time.Minute), func(ctx SpecContext) {
			hostname := fmt.Sprintf("%s.%s", testID("upsert-policy"), testEnv.CloudflareZoneName)

			By("Creating CloudflareDNS with upsert-only policy")
			dnsResource := createCloudflareDNSWithTunnelRef(ctx, k8sClient,
				testID("dns-upsert-policy"), namespace.Name,
				sharedTunnel.Name, namespace.Name,
				[]string{hostname},
				cfgatev1alpha1.DNSPolicyUpsertOnly,
				false,
			)
			dnsResource = waitForDNSReady(ctx, k8sClient, dnsResource.Name, namespace.Name, DefaultTimeout)

			By("Verifying CREATE: record is created")
			recordID := waitForDNSRecordID(ctx, cfClient, zoneID, hostname, "CNAME", DefaultTimeout)

			By("Deleting the CloudflareDNS resource")
			Expect(k8sClient.Delete(ctx, dnsResource)).To(Succeed())
			waitForDNSDeleted(ctx, k8sClient, dnsResource.Name, namespace.Name, DefaultTimeout)

			By("Verifying NO DELETE: record should still exist with upsert-only policy")
			Consistently(func() bool {
				return dnsRecordByIDStillExists(ctx, cfClient, zoneID, recordID)
			}, ShortTimeout, DefaultInterval).Should(BeTrue(), "Record should NOT be deleted with upsert-only policy")

			// Manual cleanup for test hygiene.
			cleanupDNSRecord(ctx, cfClient, zoneID, hostname, "CNAME")
		})

		It("create-only policy creates but never updates or deletes", SpecTimeout(4*time.Minute), func(ctx SpecContext) {
			hostname := fmt.Sprintf("%s.%s", testID("createonly-policy"), testEnv.CloudflareZoneName)

			By("Creating a pre-existing DNS record with different target")
			record, err := cfClient.DNS.Records.New(ctx, dns.RecordNewParams{
				ZoneID: cloudflare.F(zoneID),
				Body: dns.CNAMERecordParam{
					Name:    cloudflare.F(hostname),
					Type:    cloudflare.F(dns.CNAMERecordTypeCNAME),
					Content: cloudflare.F("original.example.com"),
					TTL:     cloudflare.F(dns.TTL(1)),
					Proxied: cloudflare.F(true),
				},
			})
			Expect(err).NotTo(HaveOccurred())
			recordID := record.ID

			By("Creating CloudflareDNS with create-only policy")
			dnsResource := createCloudflareDNSWithTunnelRef(ctx, k8sClient,
				testID("dns-createonly-policy"), namespace.Name,
				sharedTunnel.Name, namespace.Name,
				[]string{hostname},
				cfgatev1alpha1.DNSPolicyCreateOnly,
				false,
			)
			// Wait for reconciliation to attempt (may not set Ready if record exists).
			time.Sleep(10 * time.Second)

			By("Verifying NO UPDATE: record keeps original content")
			Consistently(func() string {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				if err != nil {
					// Treat transient API errors as "content unchanged" to avoid
					// flaky Consistently failures under CF rate limiting.
					GinkgoWriter.Printf("getDNSRecordFromCloudflare: API error (treating as unchanged): %v\n", err)
					return "original.example.com"
				}
				if record == nil {
					return ""
				}
				return record.Content
			}, ShortTimeout, DefaultInterval).Should(Equal("original.example.com"), "Record should NOT be updated with create-only policy")

			By("Deleting the CloudflareDNS resource")
			Expect(k8sClient.Delete(ctx, dnsResource)).To(Succeed())
			waitForDNSDeleted(ctx, k8sClient, dnsResource.Name, namespace.Name, DefaultTimeout)

			By("Verifying NO DELETE: record should still exist")
			Consistently(func() bool {
				return dnsRecordByIDStillExists(ctx, cfClient, zoneID, recordID)
			}, ShortTimeout, DefaultInterval).Should(BeTrue(), "Record should NOT be deleted with create-only policy")

			// Manual cleanup.
			cleanupDNSRecord(ctx, cfClient, zoneID, hostname, "CNAME")
		})
	})

	// =========================================================================
	// Section 4: TXT Ownership Record Tests
	// =========================================================================

	Context("TXT ownership records", func() {
		It("creates TXT ownership record when enabled", SpecTimeout(6*time.Minute), func(ctx SpecContext) {
			hostname := fmt.Sprintf("%s.%s", testID("txt-enabled"), testEnv.CloudflareZoneName)

			By("Creating CloudflareDNS with TXT ownership enabled")
			dnsResource := createCloudflareDNSWithTunnelRef(ctx, k8sClient,
				testID("dns-txt-enabled"), namespace.Name,
				sharedTunnel.Name, namespace.Name,
				[]string{hostname},
				cfgatev1alpha1.DNSPolicySync,
				true, // TXT enabled
			)
			dnsResource = waitForDNSReady(ctx, k8sClient, dnsResource.Name, namespace.Name, DefaultTimeout)

			By("Verifying CNAME record exists")
			Eventually(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				return err == nil && record != nil
			}, DefaultTimeout, DefaultInterval).Should(BeTrue())

			By("Verifying TXT ownership record exists with correct format")
			txtHostname := fmt.Sprintf("_cfgate.%s", hostname)
			Eventually(func(g Gomega) {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, txtHostname, "TXT")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(record).NotTo(BeNil(), "TXT ownership record should exist")
				g.Expect(record.Name).To(Equal(txtHostname), "TXT record should have correct name format")
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			// Cleanup.
			Expect(k8sClient.Delete(ctx, dnsResource)).To(Succeed())
		})

		It("skips TXT record when ownership disabled", SpecTimeout(6*time.Minute), func(ctx SpecContext) {
			hostname := fmt.Sprintf("%s.%s", testID("txt-disabled"), testEnv.CloudflareZoneName)

			By("Creating CloudflareDNS with TXT ownership disabled")
			dnsResource := createCloudflareDNSWithTunnelRef(ctx, k8sClient,
				testID("dns-txt-disabled"), namespace.Name,
				sharedTunnel.Name, namespace.Name,
				[]string{hostname},
				cfgatev1alpha1.DNSPolicySync,
				false, // TXT disabled
			)
			dnsResource = waitForDNSReady(ctx, k8sClient, dnsResource.Name, namespace.Name, DefaultTimeout)

			By("Verifying CNAME record exists")
			Eventually(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				return err == nil && record != nil
			}, DefaultTimeout, DefaultInterval).Should(BeTrue())

			By("Verifying no TXT ownership record exists")
			txtHostname := fmt.Sprintf("_cfgate.%s", hostname)
			Consistently(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, txtHostname, "TXT")
				if err != nil {
					GinkgoWriter.Printf("getDNSRecordFromCloudflare: API error (treating as not-created): %v\n", err)
					return true
				}
				return record == nil
			}, ShortTimeout, DefaultInterval).Should(BeTrue(), "No TXT record should be created when ownership disabled")

			// Cleanup.
			Expect(k8sClient.Delete(ctx, dnsResource)).To(Succeed())
		})

		It("uses custom TXT prefix when specified", SpecTimeout(6*time.Minute), func(ctx SpecContext) {
			hostname := fmt.Sprintf("%s.%s", testID("txt-custom"), testEnv.CloudflareZoneName)
			customPrefix := "_custom-owner"

			By("Creating CloudflareDNS with custom TXT prefix")
			dnsResource := createCloudflareDNSWithCustomTXTPrefix(ctx, k8sClient,
				testID("dns-txt-custom"), namespace.Name,
				sharedTunnel.Name, namespace.Name,
				[]string{hostname},
				customPrefix,
			)
			dnsResource = waitForDNSReady(ctx, k8sClient, dnsResource.Name, namespace.Name, DefaultTimeout)

			By("Verifying CNAME record exists")
			Eventually(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				return err == nil && record != nil
			}, DefaultTimeout, DefaultInterval).Should(BeTrue())

			By("Verifying TXT record uses custom prefix")
			customTXTHostname := fmt.Sprintf("%s.%s", customPrefix, hostname)
			Eventually(func(g Gomega) {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, customTXTHostname, "TXT")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(record).NotTo(BeNil(), "TXT record with custom prefix should exist")
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			// Cleanup.
			Expect(k8sClient.Delete(ctx, dnsResource)).To(Succeed())
		})
	})

	// =========================================================================
	// Section 5: Zone Resolution Tests
	// =========================================================================

	Context("zone resolution", func() {
		It("resolves zone by name", SpecTimeout(6*time.Minute), func(ctx SpecContext) {
			hostname := fmt.Sprintf("%s.%s", testID("zone-resolve"), testEnv.CloudflareZoneName)

			By("Creating CloudflareDNS with zone name only (no ID)")
			dnsResource := createCloudflareDNSWithTunnelRef(ctx, k8sClient,
				testID("dns-zone-resolve"), namespace.Name,
				sharedTunnel.Name, namespace.Name,
				[]string{hostname},
				cfgatev1alpha1.DNSPolicySync,
				false,
			)
			dnsResource = waitForDNSReady(ctx, k8sClient, dnsResource.Name, namespace.Name, DefaultTimeout)

			By("Verifying record was created in correct zone")
			Eventually(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				return err == nil && record != nil
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "Record should be created in resolved zone")

			// Cleanup.
			Expect(k8sClient.Delete(ctx, dnsResource)).To(Succeed())
		})

		It("applies zone-level proxied when hostname does not override it", SpecTimeout(6*time.Minute), func(ctx SpecContext) {
			hostname := fmt.Sprintf("%s.%s", testID("zone-proxied"), testEnv.CloudflareZoneName)
			zoneProxied := false

			By("Creating CloudflareDNS with defaults.proxied=true and zones[].proxied=false")
			dnsResource := &cfgatev1alpha1.CloudflareDNS{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("dns-zone-proxied"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareDNSSpec{
					TunnelRef: &cfgatev1alpha1.DNSTunnelRef{
						Name:      sharedTunnel.Name,
						Namespace: namespace.Name,
					},
					Zones: []cfgatev1alpha1.DNSZoneConfig{{
						Name:    testEnv.CloudflareZoneName,
						Proxied: &zoneProxied,
					}},
					Defaults: cfgatev1alpha1.DNSRecordDefaults{
						Proxied: true,
					},
					Policy: cfgatev1alpha1.DNSPolicySync,
					Source: cfgatev1alpha1.DNSHostnameSource{
						Explicit: []cfgatev1alpha1.DNSExplicitHostname{{
							Hostname: hostname,
						}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, dnsResource)).To(Succeed())
			DeferCleanup(func(ctx SpecContext) {
				_ = k8sClient.Delete(ctx, dnsResource)
			})
			waitForDNSReady(ctx, k8sClient, dnsResource.Name, dnsResource.Namespace, DefaultTimeout)

			By("Verifying zone-level proxied overrides defaults.proxied")
			Eventually(func(g Gomega) {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(record).NotTo(BeNil())
				g.Expect(record.Proxied).To(BeFalse())
			}, DefaultTimeout, DefaultInterval).Should(Succeed())
		})

		It("handles hostname matching zone correctly", SpecTimeout(6*time.Minute), func(ctx SpecContext) {
			// Test that a hostname is matched to the correct zone.
			hostname := fmt.Sprintf("%s.%s", testID("zone-match"), testEnv.CloudflareZoneName)

			By("Creating CloudflareDNS")
			dnsResource := createCloudflareDNSWithTunnelRef(ctx, k8sClient,
				testID("dns-zone-match"), namespace.Name,
				sharedTunnel.Name, namespace.Name,
				[]string{hostname},
				cfgatev1alpha1.DNSPolicySync,
				false,
			)
			dnsResource = waitForDNSReady(ctx, k8sClient, dnsResource.Name, namespace.Name, DefaultTimeout)

			By("Verifying status has correct zone info")
			var current cfgatev1alpha1.CloudflareDNS
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: dnsResource.Name, Namespace: namespace.Name}, &current)).To(Succeed())
			Expect(current.Status.SyncedRecords).To(BeNumerically(">=", 1))

			// Cleanup.
			Expect(k8sClient.Delete(ctx, dnsResource)).To(Succeed())
		})
	})

	Context("main-lane expansion", func() {
		It("should discover routes from namespaces selected by name or label using union semantics", SpecTimeout(8*time.Minute), func(ctx SpecContext) {
			By("Creating additional namespaces for selector coverage")
			matchByNameNS := createTestNamespace("cfgate-dns-name")
			matchByLabelNS := createTestNamespace("cfgate-dns-label")
			unmatchedNS := createTestNamespace("cfgate-dns-other")
			var dnsResource *cfgatev1alpha1.CloudflareDNS
			matchByLabelNS.Labels["e2e.dns/selection"] = "label"
			Expect(k8sClient.Update(ctx, matchByLabelNS)).To(Succeed())

			DeferCleanup(func() {
				if testEnv.SkipCleanup {
					return
				}
				cleanupCtx := context.Background()
				if dnsResource != nil {
					Expect(k8sClient.Delete(cleanupCtx, dnsResource)).To(SatisfyAny(Succeed(), WithTransform(apierrors.IsNotFound, BeTrue())))
					waitForDNSDeleted(cleanupCtx, k8sClient, dnsResource.Name, dnsResource.Namespace, DefaultTimeout)
				}
				deleteTestNamespace(matchByNameNS)
				deleteTestNamespace(matchByLabelNS)
				deleteTestNamespace(unmatchedNS)
			})

			By("Creating Gateways and HTTPRoutes across the selected namespaces")
			gatewayClassName := testID("gc")
			createGatewayClass(ctx, k8sClient, gatewayClassName)
			tunnelRef := fmt.Sprintf("%s/%s", namespace.Name, sharedTunnel.Name)
			annotationFilter := "e2e.dns/selection=union"

			makeRoute := func(targetNamespace *corev1.Namespace, suffix string) string {
				gatewayName := testID("gw-" + suffix)
				createGateway(ctx, k8sClient, gatewayName, targetNamespace.Name, gatewayClassName, tunnelRef)
				serviceName := testID("svc-" + suffix)
				createTestService(ctx, k8sClient, serviceName, targetNamespace.Name, 8080)
				hostname := fmt.Sprintf("%s.%s", testID("selector-"+suffix), testEnv.CloudflareZoneName)
				route := createHTTPRoute(ctx, k8sClient, testID("route-"+suffix), targetNamespace.Name, gatewayName, []string{hostname}, serviceName, 8080)
				updateHTTPRouteAnnotations(ctx, k8sClient, route.Name, route.Namespace, func(annotations map[string]string) {
					annotations["e2e.dns/selection"] = "union"
				})
				return hostname
			}

			hostnameByName := makeRoute(matchByNameNS, "name")
			hostnameByLabel := makeRoute(matchByLabelNS, "label")
			hostnameUnmatched := makeRoute(unmatchedNS, "other")

			By("Creating CloudflareDNS with namespaceSelector matchNames + matchLabels")
			dnsResource = &cfgatev1alpha1.CloudflareDNS{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("dns-selector-union"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareDNSSpec{
					TunnelRef: &cfgatev1alpha1.DNSTunnelRef{
						Name:      sharedTunnel.Name,
						Namespace: namespace.Name,
					},
					Zones: []cfgatev1alpha1.DNSZoneConfig{
						{Name: testEnv.CloudflareZoneName},
					},
					Policy: cfgatev1alpha1.DNSPolicySync,
					Source: cfgatev1alpha1.DNSHostnameSource{
						GatewayRoutes: &cfgatev1alpha1.DNSGatewayRoutesSource{
							Enabled:          true,
							AnnotationFilter: annotationFilter,
							NamespaceSelector: &cfgatev1alpha1.DNSNamespaceSelector{
								MatchNames: []string{matchByNameNS.Name},
								MatchLabels: map[string]string{
									"e2e.dns/selection": "label",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, dnsResource)).To(Succeed())
			waitForDNSReady(ctx, k8sClient, dnsResource.Name, dnsResource.Namespace, DefaultTimeout)

			By("Verifying records exist for the name-selected and label-selected namespaces")
			Eventually(func(g Gomega) {
				recordByName, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostnameByName, "CNAME")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(recordByName).NotTo(BeNil())

				recordByLabel, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostnameByLabel, "CNAME")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(recordByLabel).NotTo(BeNil())
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			By("Verifying routes outside the selector are excluded")
			Consistently(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostnameUnmatched, "CNAME")
				if err != nil {
					GinkgoWriter.Printf("selector exclusion lookup error (treating as not-created): %v\n", err)
					return true
				}
				return record == nil
			}, ShortTimeout, DefaultInterval).Should(BeTrue())
		})

		It("should delete orphaned records when source routes are removed and deleteOnRouteRemoval=true", SpecTimeout(8*time.Minute), func(ctx SpecContext) {
			By("Creating Gateway-backed route discovery resources")
			gatewayClassName := testID("gc")
			createGatewayClass(ctx, k8sClient, gatewayClassName)
			gatewayName := testID("gw")
			tunnelRef := fmt.Sprintf("%s/%s", namespace.Name, sharedTunnel.Name)
			createGateway(ctx, k8sClient, gatewayName, namespace.Name, gatewayClassName, tunnelRef)

			serviceName := testID("svc")
			createTestService(ctx, k8sClient, serviceName, namespace.Name, 8080)
			hostname := fmt.Sprintf("%s.%s", testID("route-cleanup"), testEnv.CloudflareZoneName)
			routeName := testID("route")
			route := createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gatewayName, []string{hostname}, serviceName, 8080)
			route = updateHTTPRouteAnnotations(ctx, k8sClient, route.Name, route.Namespace, func(annotations map[string]string) {
				annotations["e2e.dns/cleanup"] = "enabled"
			})

			By("Creating CloudflareDNS with deleteOnRouteRemoval enabled")
			dnsResource := &cfgatev1alpha1.CloudflareDNS{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("dns-route-cleanup"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareDNSSpec{
					TunnelRef: &cfgatev1alpha1.DNSTunnelRef{Name: sharedTunnel.Name},
					Zones: []cfgatev1alpha1.DNSZoneConfig{
						{Name: testEnv.CloudflareZoneName},
					},
					Policy: cfgatev1alpha1.DNSPolicySync,
					Source: cfgatev1alpha1.DNSHostnameSource{
						GatewayRoutes: &cfgatev1alpha1.DNSGatewayRoutesSource{
							Enabled:          true,
							AnnotationFilter: "e2e.dns/cleanup=enabled",
						},
					},
					CleanupPolicy: cfgatev1alpha1.DNSCleanupPolicy{
						DeleteOnRouteRemoval: ptrTo(true),
					},
				},
			}
			Expect(k8sClient.Create(ctx, dnsResource)).To(Succeed())
			waitForDNSReady(ctx, k8sClient, dnsResource.Name, dnsResource.Namespace, DefaultTimeout)
			waitForDNSRecordID(ctx, cfClient, zoneID, hostname, "CNAME", DefaultTimeout)

			By("Deleting the source HTTPRoute and verifying record cleanup")
			Expect(k8sClient.Delete(ctx, route)).To(Succeed())
			Eventually(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				if err != nil {
					GinkgoWriter.Printf("route removal cleanup lookup error: %v\n", err)
					return false
				}
				return record == nil
			}, DefaultTimeout, DefaultInterval).Should(BeTrue())
		})

		It("should preserve records after route removal when deleteOnRouteRemoval=false", SpecTimeout(8*time.Minute), func(ctx SpecContext) {
			By("Creating Gateway-backed discovery resources")
			gatewayClassName := testID("gc")
			createGatewayClass(ctx, k8sClient, gatewayClassName)
			gatewayName := testID("gw")
			tunnelRef := fmt.Sprintf("%s/%s", namespace.Name, sharedTunnel.Name)
			createGateway(ctx, k8sClient, gatewayName, namespace.Name, gatewayClassName, tunnelRef)

			serviceName := testID("svc")
			createTestService(ctx, k8sClient, serviceName, namespace.Name, 8080)
			hostname := fmt.Sprintf("%s.%s", testID("route-preserve"), testEnv.CloudflareZoneName)
			routeName := testID("route")
			route := createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gatewayName, []string{hostname}, serviceName, 8080)
			route = updateHTTPRouteAnnotations(ctx, k8sClient, route.Name, route.Namespace, func(annotations map[string]string) {
				annotations["e2e.dns/cleanup"] = "preserve"
			})

			By("Creating CloudflareDNS with deleteOnRouteRemoval disabled")
			dnsResource := &cfgatev1alpha1.CloudflareDNS{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("dns-route-preserve"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareDNSSpec{
					TunnelRef: &cfgatev1alpha1.DNSTunnelRef{Name: sharedTunnel.Name},
					Zones: []cfgatev1alpha1.DNSZoneConfig{
						{Name: testEnv.CloudflareZoneName},
					},
					Policy: cfgatev1alpha1.DNSPolicySync,
					Source: cfgatev1alpha1.DNSHostnameSource{
						GatewayRoutes: &cfgatev1alpha1.DNSGatewayRoutesSource{
							Enabled:          true,
							AnnotationFilter: "e2e.dns/cleanup=preserve",
						},
					},
					CleanupPolicy: cfgatev1alpha1.DNSCleanupPolicy{
						DeleteOnRouteRemoval: ptrTo(false),
					},
				},
			}
			Expect(k8sClient.Create(ctx, dnsResource)).To(Succeed())
			waitForDNSReady(ctx, k8sClient, dnsResource.Name, dnsResource.Namespace, DefaultTimeout)
			recordID := waitForDNSRecordID(ctx, cfClient, zoneID, hostname, "CNAME", DefaultTimeout)

			By("Deleting the route and verifying the record remains")
			Expect(k8sClient.Delete(ctx, route)).To(Succeed())
			Consistently(func() bool {
				return dnsRecordByIDStillExists(ctx, cfClient, zoneID, recordID)
			}, ShortTimeout, DefaultInterval).Should(BeTrue())

			By("Deleting the DNS resource so the preserved record can be cleaned up on resource removal")
			Expect(k8sClient.Delete(ctx, dnsResource)).To(Succeed())
			waitForDNSDeleted(ctx, k8sClient, dnsResource.Name, dnsResource.Namespace, DefaultTimeout)
			Eventually(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				if err != nil {
					GinkgoWriter.Printf("post-delete cleanup lookup error: %v\n", err)
					return false
				}
				return record == nil
			}, DefaultTimeout, DefaultInterval).Should(BeTrue())
		})

		It("should let explicit hostnames override colliding route-discovered hostnames while still adding route-only hostnames", SpecTimeout(8*time.Minute), func(ctx SpecContext) {
			By("Creating Gateway-backed discovery resources")
			gatewayClassName := testID("gc")
			createGatewayClass(ctx, k8sClient, gatewayClassName)
			gatewayName := testID("gw")
			tunnelRef := fmt.Sprintf("%s/%s", namespace.Name, sharedTunnel.Name)
			createGateway(ctx, k8sClient, gatewayName, namespace.Name, gatewayClassName, tunnelRef)

			By("Creating an HTTPRoute with one colliding hostname and one route-only hostname")
			serviceName := testID("svc")
			createTestService(ctx, k8sClient, serviceName, namespace.Name, 8080)
			explicitHostname := fmt.Sprintf("%s.%s", testID("explicit-wins"), testEnv.CloudflareZoneName)
			routeOnlyHostname := fmt.Sprintf("%s.%s", testID("route-adds"), testEnv.CloudflareZoneName)
			route := createHTTPRoute(ctx, k8sClient, testID("route"), namespace.Name, gatewayName, []string{explicitHostname, routeOnlyHostname}, serviceName, 8080)
			updateHTTPRouteAnnotations(ctx, k8sClient, route.Name, route.Namespace, func(annotations map[string]string) {
				annotations["e2e.dns/merge"] = "enabled"
				annotations["cfgate.io/cloudflare-proxied"] = "true"
			})

			By("Creating CloudflareDNS with colliding explicit hostname settings")
			customTarget := "custom-origin.example.net"
			dnsResource := &cfgatev1alpha1.CloudflareDNS{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("dns-explicit-precedence"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareDNSSpec{
					TunnelRef: &cfgatev1alpha1.DNSTunnelRef{Name: sharedTunnel.Name},
					Zones: []cfgatev1alpha1.DNSZoneConfig{
						{Name: testEnv.CloudflareZoneName},
					},
					Defaults: cfgatev1alpha1.DNSRecordDefaults{
						Proxied: false,
					},
					Policy: cfgatev1alpha1.DNSPolicySync,
					Source: cfgatev1alpha1.DNSHostnameSource{
						GatewayRoutes: &cfgatev1alpha1.DNSGatewayRoutesSource{
							Enabled:          true,
							AnnotationFilter: "e2e.dns/merge=enabled",
						},
						Explicit: []cfgatev1alpha1.DNSExplicitHostname{{
							Hostname: explicitHostname,
							Target:   customTarget,
							Proxied:  ptrTo(false),
							TTL:      300,
						}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, dnsResource)).To(Succeed())
			waitForDNSReady(ctx, k8sClient, dnsResource.Name, dnsResource.Namespace, DefaultTimeout)

			By("Verifying the colliding hostname uses explicit target, ttl, and proxied settings")
			Eventually(func(g Gomega) {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, explicitHostname, "CNAME")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(record).NotTo(BeNil())
				g.Expect(record.Content).To(Equal(customTarget))
				g.Expect(record.Proxied).To(BeFalse())
				g.Expect(record.TTL).To(Equal(float64(300)))
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			By("Verifying route discovery still adds non-conflicting hostnames")
			Eventually(func(g Gomega) {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, routeOnlyHostname, "CNAME")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(record).NotTo(BeNil())
				g.Expect(record.Content).To(Equal(sharedTunnel.Status.TunnelDomain))
				g.Expect(record.Proxied).To(BeTrue())
			}, DefaultTimeout, DefaultInterval).Should(Succeed())
		})

		It("should use cfgate.io/hostname for tunnel config and DNS route discovery", SpecTimeout(8*time.Minute), func(ctx SpecContext) {
			By("Creating Gateway-backed discovery resources")
			gatewayClassName := testID("gc")
			createGatewayClass(ctx, k8sClient, gatewayClassName)
			gatewayName := testID("gw")
			tunnelRef := fmt.Sprintf("%s/%s", namespace.Name, sharedTunnel.Name)
			createGateway(ctx, k8sClient, gatewayName, namespace.Name, gatewayClassName, tunnelRef)

			By("Creating an HTTPRoute with a hostname override")
			serviceName := testID("svc")
			createTestService(ctx, k8sClient, serviceName, namespace.Name, 8080)
			overrideHostname := fmt.Sprintf("%s.%s", testID("override"), testEnv.CloudflareZoneName)
			ignoredHostname := fmt.Sprintf("%s.%s", testID("ignored"), testEnv.CloudflareZoneName)
			route := createHTTPRoute(ctx, k8sClient, testID("route"), namespace.Name, gatewayName, []string{ignoredHostname}, serviceName, 8080)
			updateHTTPRouteAnnotations(ctx, k8sClient, route.Name, route.Namespace, func(annotations map[string]string) {
				annotations["e2e.dns/hostname-override"] = "enabled"
				annotations["cfgate.io/hostname"] = overrideHostname
			})

			By("Creating CloudflareDNS route discovery")
			dnsResource := &cfgatev1alpha1.CloudflareDNS{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("dns-hostname-override"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareDNSSpec{
					TunnelRef: &cfgatev1alpha1.DNSTunnelRef{Name: sharedTunnel.Name},
					Zones: []cfgatev1alpha1.DNSZoneConfig{
						{Name: testEnv.CloudflareZoneName},
					},
					Policy: cfgatev1alpha1.DNSPolicySync,
					Source: cfgatev1alpha1.DNSHostnameSource{
						GatewayRoutes: &cfgatev1alpha1.DNSGatewayRoutesSource{
							Enabled:          true,
							AnnotationFilter: "e2e.dns/hostname-override=enabled",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, dnsResource)).To(Succeed())
			waitForDNSReady(ctx, k8sClient, dnsResource.Name, dnsResource.Namespace, DefaultTimeout)

			By("Verifying remote tunnel config uses the override hostname")
			Eventually(func(g Gomega) {
				var current cfgatev1alpha1.CloudflareTunnel
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sharedTunnel), &current)).To(Succeed())
				config, err := getRawTunnelConfigurationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, current.Status.TunnelID)
				g.Expect(err).NotTo(HaveOccurred())

				_, ok := findRawTunnelIngress(config, overrideHostname)
				g.Expect(ok).To(BeTrue(), "expected ingress rule for hostname override")
				_, ok = findRawTunnelIngress(config, ignoredHostname)
				g.Expect(ok).To(BeFalse(), "ignored spec hostname should not be synced")
			}, LongTimeout, DefaultInterval).Should(Succeed())

			By("Verifying DNS sync uses the override hostname")
			Eventually(func(g Gomega) {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, overrideHostname, "CNAME")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(record).NotTo(BeNil())
				g.Expect(record.Content).To(Equal(sharedTunnel.Status.TunnelDomain))

				ignoredRecord, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, ignoredHostname, "CNAME")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(ignoredRecord).To(BeNil())
			}, DefaultTimeout, DefaultInterval).Should(Succeed())
		})

		It("should use fallback credentials during DNS deletion when primary tunnel credentials disappear", SpecTimeout(12*time.Minute), func(ctx SpecContext) {
			By("Creating dedicated primary credentials for a deletion-path tunnel")
			primarySecretName := testID("primary-creds")
			primarySecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      primarySecretName,
					Namespace: namespace.Name,
				},
				Type: corev1.SecretTypeOpaque,
				StringData: map[string]string{
					"CLOUDFLARE_API_TOKEN": testEnv.CloudflareAPIToken,
				},
			}
			Expect(k8sClient.Create(ctx, primarySecret)).To(Succeed())

			By("Creating a dedicated tunnel that uses the primary secret plus fallback credentials")
			tunnelName := testID("dns-fallback-tunnel")
			tunnel := &cfgatev1alpha1.CloudflareTunnel{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("dns-fallback"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareTunnelSpec{
					Tunnel: cfgatev1alpha1.TunnelIdentity{
						Name: tunnelName,
					},
					Cloudflare: cfgatev1alpha1.CloudflareConfig{
						AccountID: testEnv.CloudflareAccountID,
						SecretRef: cfgatev1alpha1.SecretRef{
							Name: primarySecretName,
						},
					},
					FallbackCredentialsRef: e2eFallbackCredentialsRef(),
					Cloudflared: cfgatev1alpha1.CloudflaredConfig{
						Replicas: 1,
					},
				},
			}
			Expect(k8sClient.Create(ctx, tunnel)).To(Succeed())
			tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)

			By("Creating CloudflareDNS with fallbackCredentialsRef")
			hostname := fmt.Sprintf("%s.%s", testID("dns-fallback"), testEnv.CloudflareZoneName)
			dnsResource := &cfgatev1alpha1.CloudflareDNS{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("dns-fallback"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareDNSSpec{
					TunnelRef: &cfgatev1alpha1.DNSTunnelRef{
						Name:      tunnel.Name,
						Namespace: namespace.Name,
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
					FallbackCredentialsRef: e2eFallbackCredentialsRef(),
				},
			}
			Expect(k8sClient.Create(ctx, dnsResource)).To(Succeed())
			waitForDNSReady(ctx, k8sClient, dnsResource.Name, dnsResource.Namespace, DefaultTimeout)

			By("Deleting the primary tunnel credentials secret and then deleting the DNS resource")
			Expect(k8sClient.Delete(ctx, primarySecret)).To(Succeed())
			Expect(k8sClient.Delete(ctx, dnsResource)).To(Succeed())
			waitForDNSDeleted(ctx, k8sClient, dnsResource.Name, dnsResource.Namespace, DefaultTimeout)

			By("Verifying the DNS record is removed via fallback credentials")
			Eventually(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				if err != nil {
					GinkgoWriter.Printf("fallback cleanup lookup error: %v\n", err)
					return false
				}
				return record == nil
			}, LongTimeout, DefaultInterval).Should(BeTrue())
		})

		It("should create external A and AAAA records and also support the explicit zone ID fast path", SpecTimeout(10*time.Minute), func(ctx SpecContext) {
			By("Creating an external A record")
			aHostname := fmt.Sprintf("%s.%s", testID("external-a"), testEnv.CloudflareZoneName)
			aRecord := &cfgatev1alpha1.CloudflareDNS{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("dns-external-a"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareDNSSpec{
					ExternalTarget: &cfgatev1alpha1.ExternalTarget{
						Type:  cfgatev1alpha1.RecordTypeA,
						Value: "198.51.100.10",
					},
					Zones: []cfgatev1alpha1.DNSZoneConfig{
						{Name: testEnv.CloudflareZoneName},
					},
					Defaults: cfgatev1alpha1.DNSRecordDefaults{
						Proxied: false,
					},
					Policy: cfgatev1alpha1.DNSPolicySync,
					Source: cfgatev1alpha1.DNSHostnameSource{
						Explicit: []cfgatev1alpha1.DNSExplicitHostname{
							{Hostname: aHostname},
						},
					},
					Cloudflare: &cfgatev1alpha1.CloudflareConfig{
						AccountID: testEnv.CloudflareAccountID,
						SecretRef: cfgatev1alpha1.SecretRef{Name: "cloudflare-credentials"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, aRecord)).To(Succeed())
			waitForDNSReady(ctx, k8sClient, aRecord.Name, aRecord.Namespace, DefaultTimeout)

			Eventually(func(g Gomega) {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, aHostname, "A")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(record).NotTo(BeNil())
				g.Expect(record.Type).To(Equal("A"))
				g.Expect(record.Content).To(Equal("198.51.100.10"))
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			By("Creating an external AAAA record")
			aaaaHostname := fmt.Sprintf("%s.%s", testID("external-aaaa"), testEnv.CloudflareZoneName)
			aaaaRecord := &cfgatev1alpha1.CloudflareDNS{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("dns-external-aaaa"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareDNSSpec{
					ExternalTarget: &cfgatev1alpha1.ExternalTarget{
						Type:  cfgatev1alpha1.RecordTypeAAAA,
						Value: "2001:db8::10",
					},
					Zones: []cfgatev1alpha1.DNSZoneConfig{
						{Name: testEnv.CloudflareZoneName},
					},
					Defaults: cfgatev1alpha1.DNSRecordDefaults{
						Proxied: false,
					},
					Policy: cfgatev1alpha1.DNSPolicySync,
					Source: cfgatev1alpha1.DNSHostnameSource{
						Explicit: []cfgatev1alpha1.DNSExplicitHostname{
							{Hostname: aaaaHostname},
						},
					},
					Cloudflare: &cfgatev1alpha1.CloudflareConfig{
						AccountID: testEnv.CloudflareAccountID,
						SecretRef: cfgatev1alpha1.SecretRef{Name: "cloudflare-credentials"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, aaaaRecord)).To(Succeed())
			waitForDNSReady(ctx, k8sClient, aaaaRecord.Name, aaaaRecord.Namespace, DefaultTimeout)

			Eventually(func(g Gomega) {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, aaaaHostname, "AAAA")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(record).NotTo(BeNil())
				g.Expect(record.Type).To(Equal("AAAA"))
				g.Expect(record.Content).To(Equal("2001:db8::10"))
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			By("Creating a tunnel-backed record that uses an explicit zone ID")
			fastPathHostname := fmt.Sprintf("%s.%s", testID("zoneid"), testEnv.CloudflareZoneName)
			fastPath := &cfgatev1alpha1.CloudflareDNS{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("dns-zoneid"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareDNSSpec{
					TunnelRef: &cfgatev1alpha1.DNSTunnelRef{Name: sharedTunnel.Name},
					Zones: []cfgatev1alpha1.DNSZoneConfig{
						{Name: testEnv.CloudflareZoneName, ID: zoneID},
					},
					Policy: cfgatev1alpha1.DNSPolicySync,
					Source: cfgatev1alpha1.DNSHostnameSource{
						Explicit: []cfgatev1alpha1.DNSExplicitHostname{
							{Hostname: fastPathHostname},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, fastPath)).To(Succeed())
			waitForDNSReady(ctx, k8sClient, fastPath.Name, fastPath.Namespace, DefaultTimeout)
			waitForDNSRecordID(ctx, cfClient, zoneID, fastPathHostname, "CNAME", DefaultTimeout)
		})

		It("should surface NoHostnamesDiscovered and recover once matching routes appear", SpecTimeout(8*time.Minute), func(ctx SpecContext) {
			By("Creating Gateway infrastructure without any matching routes yet")
			gatewayClassName := testID("gc")
			createGatewayClass(ctx, k8sClient, gatewayClassName)
			gatewayName := testID("gw")
			tunnelRef := fmt.Sprintf("%s/%s", namespace.Name, sharedTunnel.Name)
			createGateway(ctx, k8sClient, gatewayName, namespace.Name, gatewayClassName, tunnelRef)

			By("Creating CloudflareDNS that relies on gatewayRoutes")
			dnsResource := &cfgatev1alpha1.CloudflareDNS{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("dns-no-hostnames"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareDNSSpec{
					TunnelRef: &cfgatev1alpha1.DNSTunnelRef{Name: sharedTunnel.Name},
					Zones: []cfgatev1alpha1.DNSZoneConfig{
						{Name: testEnv.CloudflareZoneName},
					},
					Policy: cfgatev1alpha1.DNSPolicySync,
					Source: cfgatev1alpha1.DNSHostnameSource{
						GatewayRoutes: &cfgatev1alpha1.DNSGatewayRoutesSource{
							Enabled:          true,
							AnnotationFilter: "e2e.dns/recovery=enabled",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, dnsResource)).To(Succeed())

			By("Waiting for the no-hostnames early requeue conditions")
			Eventually(func(g Gomega) {
				var current cfgatev1alpha1.CloudflareDNS
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: dnsResource.Name, Namespace: dnsResource.Namespace}, &current)).To(Succeed())
				recordsSynced := findCondition(current.Status.Conditions, "RecordsSynced")
				ready := findCondition(current.Status.Conditions, "Ready")
				g.Expect(recordsSynced).NotTo(BeNil())
				g.Expect(ready).NotTo(BeNil())
				g.Expect(recordsSynced.Status).To(Equal(metav1.ConditionUnknown))
				g.Expect(recordsSynced.Reason).To(Equal("NoHostnamesDiscovered"))
				g.Expect(ready.Status).To(Equal(metav1.ConditionUnknown))
				g.Expect(ready.Reason).To(Equal("NoHostnamesDiscovered"))
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			By("Creating a matching route and verifying recovery")
			serviceName := testID("svc")
			createTestService(ctx, k8sClient, serviceName, namespace.Name, 8080)
			hostname := fmt.Sprintf("%s.%s", testID("recovered"), testEnv.CloudflareZoneName)
			route := createHTTPRoute(ctx, k8sClient, testID("route"), namespace.Name, gatewayName, []string{hostname}, serviceName, 8080)
			updateHTTPRouteAnnotations(ctx, k8sClient, route.Name, route.Namespace, func(annotations map[string]string) {
				annotations["e2e.dns/recovery"] = "enabled"
			})

			waitForDNSReady(ctx, k8sClient, dnsResource.Name, dnsResource.Namespace, DefaultTimeout)
			waitForDNSRecordID(ctx, cfClient, zoneID, hostname, "CNAME", DefaultTimeout)
		})

		It("should report partial sync when some hostnames belong to unconfigured zones", SpecTimeout(8*time.Minute), func(ctx SpecContext) {
			By("Creating CloudflareDNS with one good hostname and one hostname in an unconfigured zone")
			goodHostname := fmt.Sprintf("%s.%s", testID("partial-good"), testEnv.CloudflareZoneName)
			badHostname := fmt.Sprintf("%s.example.invalid", testID("partial-bad"))
			dnsResource := &cfgatev1alpha1.CloudflareDNS{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("dns-partial"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareDNSSpec{
					TunnelRef: &cfgatev1alpha1.DNSTunnelRef{Name: sharedTunnel.Name},
					Zones: []cfgatev1alpha1.DNSZoneConfig{
						{Name: testEnv.CloudflareZoneName},
					},
					Policy: cfgatev1alpha1.DNSPolicySync,
					Source: cfgatev1alpha1.DNSHostnameSource{
						Explicit: []cfgatev1alpha1.DNSExplicitHostname{
							{Hostname: goodHostname},
							{Hostname: badHostname},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, dnsResource)).To(Succeed())

			By("Waiting for mixed success/failure accounting to appear in status")
			Eventually(func(g Gomega) {
				var current cfgatev1alpha1.CloudflareDNS
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: dnsResource.Name, Namespace: dnsResource.Namespace}, &current)).To(Succeed())
				g.Expect(current.Status.FailedRecords).To(Equal(int32(1)))
				g.Expect(current.Status.SyncedRecords).To(BeNumerically(">=", 1))
				g.Expect(findCondition(current.Status.Conditions, "RecordsSynced")).NotTo(BeNil())
				g.Expect(findCondition(current.Status.Conditions, "RecordsSynced").Status).To(Equal(metav1.ConditionFalse))
				g.Expect(findCondition(current.Status.Conditions, "Ready")).NotTo(BeNil())
				g.Expect(findCondition(current.Status.Conditions, "Ready").Status).To(Equal(metav1.ConditionFalse))

				var foundFailed, foundSynced bool
				for _, record := range current.Status.Records {
					if record.Hostname == badHostname && record.Status == "Failed" {
						foundFailed = true
						g.Expect(record.Error).To(ContainSubstring("zone"))
					}
					if record.Hostname == goodHostname && record.Status == "Synced" {
						foundSynced = true
					}
				}
				g.Expect(foundFailed).To(BeTrue())
				g.Expect(foundSynced).To(BeTrue())
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			By("Verifying the valid hostname still syncs to Cloudflare")
			Eventually(func(g Gomega) {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, goodHostname, "CNAME")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(record).NotTo(BeNil())
			}, DefaultTimeout, DefaultInterval).Should(Succeed())
		})
	})

	// =========================================================================
	// Section 5.5: §6.4 Annotation-Triggered Reconciliation
	// =========================================================================

	Context("annotation-triggered reconciliation", func() {
		It("should reconcile DNS when annotation is added post-creation", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			By("Creating GatewayClass and Gateway without dns-sync annotation")
			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)

			tunnelRef := fmt.Sprintf("%s/%s", namespace.Name, sharedTunnel.Name)
			gwName := testID("gw")
			createGateway(ctx, k8sClient, gwName, namespace.Name, gcName, tunnelRef)

			By("Creating test Service")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			By("Creating HTTPRoute WITHOUT annotation")
			hostname := fmt.Sprintf("%s.%s", testID("annot-dns"), testEnv.CloudflareZoneName)
			routeName := testID("route")

			route := createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gwName, []string{hostname}, svcName, 8080)

			By("Creating CloudflareDNS with annotationFilter requiring cfgate.io/dns-sync=enabled")
			dnsResource := createCloudflareDNSWithGatewayRoutes(ctx, k8sClient,
				testID("dns-annot"), namespace.Name,
				sharedTunnel.Name,
				[]string{testEnv.CloudflareZoneName},
				"cfgate.io/dns-sync=enabled",
			)

			By("Verifying no DNS records created initially (annotation doesn't match)")
			Consistently(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				if err != nil {
					GinkgoWriter.Printf("getDNSRecordFromCloudflare: API error (treating as not-created): %v\n", err)
					return true
				}
				return record == nil
			}, ShortTimeout, DefaultInterval).Should(BeTrue(),
				"DNS record should NOT be created when annotation filter doesn't match")

			By("Patching HTTPRoute to add cfgate.io/dns-sync=enabled annotation")
			// Use Eventually to retry on conflict (controller may update status concurrently)
			Eventually(func() error {
				var currentRoute gatewayv1.HTTPRoute
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: route.Name, Namespace: route.Namespace}, &currentRoute); err != nil {
					return err
				}
				if currentRoute.Annotations == nil {
					currentRoute.Annotations = make(map[string]string)
				}
				currentRoute.Annotations["cfgate.io/dns-sync"] = "enabled"
				return k8sClient.Update(ctx, &currentRoute)
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			By("Verifying DNS records appear after annotation triggers reconciliation")
			Eventually(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				return err == nil && record != nil
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(),
				"DNS record should be created after annotation is added")

			// Cleanup.
			Expect(k8sClient.Delete(ctx, dnsResource)).To(Succeed())
		})
	})

	// =========================================================================
	// Section 6: Cleanup Policy Tests
	// =========================================================================

	Context("cleanup policy", func() {
		It("respects deleteOnResourceRemoval=false", SpecTimeout(6*time.Minute), func(ctx SpecContext) {
			hostname := fmt.Sprintf("%s.%s", testID("cleanup-false"), testEnv.CloudflareZoneName)

			By("Creating CloudflareDNS with deleteOnResourceRemoval=false")
			dnsResource := createCloudflareDNSWithCleanupPolicy(ctx, k8sClient,
				testID("dns-cleanup-false"), namespace.Name,
				sharedTunnel.Name, namespace.Name,
				[]string{hostname},
				false, // deleteOnResourceRemoval=false
			)
			dnsResource = waitForDNSReady(ctx, k8sClient, dnsResource.Name, namespace.Name, DefaultTimeout)

			By("Verifying record is created")
			recordID := waitForDNSRecordID(ctx, cfClient, zoneID, hostname, "CNAME", DefaultTimeout)
			DeferCleanup(func() {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
				defer cancel()
				cleanupDNSRecord(cleanupCtx, cfClient, zoneID, hostname, "CNAME")
			})

			By("Verifying cleanup policy is explicitly disabled")
			var current cfgatev1alpha1.CloudflareDNS
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: dnsResource.Name, Namespace: dnsResource.Namespace}, &current)).To(Succeed())
			Expect(current.Spec.CleanupPolicy.DeleteOnResourceRemoval).NotTo(BeNil())
			Expect(*current.Spec.CleanupPolicy.DeleteOnResourceRemoval).To(BeFalse())

			By("Deleting the CloudflareDNS resource")
			Expect(k8sClient.Delete(ctx, dnsResource)).To(Succeed())
			waitForDNSDeleted(ctx, k8sClient, dnsResource.Name, namespace.Name, DefaultTimeout)

			By("Verifying record is NOT deleted (cleanup disabled)")
			Consistently(func(g Gomega) {
				exists, detail := dnsRecordByIDStatus(ctx, cfClient, zoneID, recordID)
				g.Expect(exists).To(BeTrue(), "Record should NOT be deleted when cleanup disabled: %s", detail)
			}, ShortTimeout, DefaultInterval).Should(Succeed())
		})
	})

	// =========================================================================
	// Section 7: Orphan Deletion Policy Tests
	// =========================================================================

	Context("orphan deletion policy", func() {
		It("should preserve DNS records when deletion policy is orphan", SpecTimeout(6*time.Minute), func(ctx SpecContext) {
			hostname := fmt.Sprintf("%s.%s", testID("dns-orphan"), testEnv.CloudflareZoneName)

			By("Creating CloudflareDNS with tunnelRef")
			dnsResource := createCloudflareDNSWithTunnelRef(ctx, k8sClient,
				testID("dns-orphan"), namespace.Name,
				sharedTunnel.Name, namespace.Name,
				[]string{hostname},
				cfgatev1alpha1.DNSPolicySync,
				false,
			)

			By("Waiting for CloudflareDNS to be ready")
			dnsResource = waitForDNSReady(ctx, k8sClient, dnsResource.Name, namespace.Name, DefaultTimeout)

			By("Verifying DNS record exists in Cloudflare")
			Eventually(func(g Gomega) {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(record).NotTo(BeNil(), "CNAME record should exist before deletion")
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			By("Setting orphan deletion policy annotation")
			Eventually(func() error {
				var current cfgatev1alpha1.CloudflareDNS
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: dnsResource.Name, Namespace: namespace.Name}, &current); err != nil {
					return err
				}
				if current.Annotations == nil {
					current.Annotations = make(map[string]string)
				}
				current.Annotations["cfgate.io/deletion-policy"] = "orphan"
				return k8sClient.Update(ctx, &current)
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			By("Deleting CloudflareDNS resource")
			Expect(k8sClient.Delete(ctx, dnsResource)).To(Succeed())

			By("Waiting for CloudflareDNS to be deleted from Kubernetes")
			waitForDNSDeleted(ctx, k8sClient, dnsResource.Name, namespace.Name, DefaultTimeout)

			By("Verifying DNS record still exists in Cloudflare (orphaned)")
			record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
			Expect(err).NotTo(HaveOccurred())
			Expect(record).NotTo(BeNil(), "DNS record should still exist in Cloudflare with orphan policy")

			cleanupDNSRecord(ctx, cfClient, zoneID, hostname, "CNAME")
		})
	})

	// =========================================================================
	// Section 8: Comment Length Regression Guard
	// =========================================================================

	Context("comment length regression guard", func() {
		It("syncs DNS record with long resource name without exceeding Cloudflare 100-char comment limit", SpecTimeout(6*time.Minute), func(ctx SpecContext) {
			// Regression guard for alpha.13 fix.
			// Prior to alpha.13, the comment included owner/resource metadata:
			//   "managed by cfgate, owner=<ns>/<name>, dns=<ns>/<name>"
			// This exceeded the Cloudflare API's 100-char comment limit with
			// long namespace/name combinations, causing sync failures.
			// After alpha.13, the comment is fixed: "managed by cfgate" (16 chars).
			// This test uses a deliberately long resource name so the old format
			// would produce a ~125-char comment, catching any regression.

			By("Creating CloudflareDNS with a long resource name")
			hostname := fmt.Sprintf("%s.%s", testID("cmtregr"), testEnv.CloudflareZoneName)
			dnsResource := createCloudflareDNSWithTunnelRef(ctx, k8sClient,
				testID("dns-comment-regression"), namespace.Name,
				sharedTunnel.Name, namespace.Name,
				[]string{hostname},
				cfgatev1alpha1.DNSPolicySync,
				true, // TXT ownership enabled
			)

			By("Verifying DNS becomes Ready (comment accepted by Cloudflare API)")
			dnsResource = waitForDNSReady(ctx, k8sClient, dnsResource.Name, namespace.Name, DefaultTimeout)
			Expect(dnsResource.Status.SyncedRecords).To(BeNumerically(">=", 1),
				"Record should sync successfully — if this fails, check that the DNS record comment does not exceed 100 chars")
		})
	})
})

// =============================================================================
// Helper Functions for CloudflareDNS Tests
// =============================================================================

// createCloudflareDNSWithTunnelRef creates a CloudflareDNS with tunnelRef mode.
func createCloudflareDNSWithTunnelRef(
	ctx context.Context,
	k8sClient client.Client,
	name, namespace string,
	tunnelName, tunnelNamespace string,
	hostnames []string,
	policy cfgatev1alpha1.DNSPolicy,
	txtEnabled bool,
) *cfgatev1alpha1.CloudflareDNS {
	explicitHostnames := make([]cfgatev1alpha1.DNSExplicitHostname, len(hostnames))
	for i, h := range hostnames {
		explicitHostnames[i] = cfgatev1alpha1.DNSExplicitHostname{
			Hostname: h,
		}
	}

	dns := &cfgatev1alpha1.CloudflareDNS{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: cfgatev1alpha1.CloudflareDNSSpec{
			TunnelRef: &cfgatev1alpha1.DNSTunnelRef{
				Name:      tunnelName,
				Namespace: tunnelNamespace,
			},
			Zones: []cfgatev1alpha1.DNSZoneConfig{
				{
					Name: testEnv.CloudflareZoneName,
				},
			},
			Policy: policy,
			Source: cfgatev1alpha1.DNSHostnameSource{
				Explicit: explicitHostnames,
			},
			Ownership: cfgatev1alpha1.DNSOwnershipConfig{
				TXTRecord: cfgatev1alpha1.DNSTXTRecordOwnership{
					Enabled: ptrTo(txtEnabled),
					Prefix:  "_cfgate",
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, dns)).To(Succeed())
	return dns
}

// createCloudflareDNSWithExternalTarget creates a CloudflareDNS with externalTarget mode.
func createCloudflareDNSWithExternalTarget(
	ctx context.Context,
	k8sClient client.Client,
	name, namespace string,
	recordType cfgatev1alpha1.RecordType,
	targetValue string,
	hostnames []string,
	policy cfgatev1alpha1.DNSPolicy,
) *cfgatev1alpha1.CloudflareDNS {
	explicitHostnames := make([]cfgatev1alpha1.DNSExplicitHostname, len(hostnames))
	for i, h := range hostnames {
		explicitHostnames[i] = cfgatev1alpha1.DNSExplicitHostname{
			Hostname: h,
		}
	}

	dns := &cfgatev1alpha1.CloudflareDNS{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: cfgatev1alpha1.CloudflareDNSSpec{
			ExternalTarget: &cfgatev1alpha1.ExternalTarget{
				Type:  recordType,
				Value: targetValue,
			},
			Zones: []cfgatev1alpha1.DNSZoneConfig{
				{
					Name: testEnv.CloudflareZoneName,
				},
			},
			Policy: policy,
			Source: cfgatev1alpha1.DNSHostnameSource{
				Explicit: explicitHostnames,
			},
			Cloudflare: &cfgatev1alpha1.CloudflareConfig{
				AccountID: testEnv.CloudflareAccountID,
				SecretRef: cfgatev1alpha1.SecretRef{
					Name: "cloudflare-credentials",
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, dns)).To(Succeed())
	return dns
}

// createCloudflareDNSWithCustomTXTPrefix creates a CloudflareDNS with custom TXT prefix.
func createCloudflareDNSWithCustomTXTPrefix(
	ctx context.Context,
	k8sClient client.Client,
	name, namespace string,
	tunnelName, tunnelNamespace string,
	hostnames []string,
	txtPrefix string,
) *cfgatev1alpha1.CloudflareDNS {
	explicitHostnames := make([]cfgatev1alpha1.DNSExplicitHostname, len(hostnames))
	for i, h := range hostnames {
		explicitHostnames[i] = cfgatev1alpha1.DNSExplicitHostname{
			Hostname: h,
		}
	}

	dns := &cfgatev1alpha1.CloudflareDNS{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: cfgatev1alpha1.CloudflareDNSSpec{
			TunnelRef: &cfgatev1alpha1.DNSTunnelRef{
				Name:      tunnelName,
				Namespace: tunnelNamespace,
			},
			Zones: []cfgatev1alpha1.DNSZoneConfig{
				{
					Name: testEnv.CloudflareZoneName,
				},
			},
			Policy: cfgatev1alpha1.DNSPolicySync,
			Source: cfgatev1alpha1.DNSHostnameSource{
				Explicit: explicitHostnames,
			},
			Ownership: cfgatev1alpha1.DNSOwnershipConfig{
				TXTRecord: cfgatev1alpha1.DNSTXTRecordOwnership{
					Enabled: ptrTo(true),
					Prefix:  txtPrefix,
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, dns)).To(Succeed())
	return dns
}

// createCloudflareDNSWithCleanupPolicy creates a CloudflareDNS with specific cleanup policy.
func createCloudflareDNSWithCleanupPolicy(
	ctx context.Context,
	k8sClient client.Client,
	name, namespace string,
	tunnelName, tunnelNamespace string,
	hostnames []string,
	deleteOnResourceRemoval bool,
) *cfgatev1alpha1.CloudflareDNS {
	explicitHostnames := make([]cfgatev1alpha1.DNSExplicitHostname, len(hostnames))
	for i, h := range hostnames {
		explicitHostnames[i] = cfgatev1alpha1.DNSExplicitHostname{
			Hostname: h,
		}
	}

	dns := &cfgatev1alpha1.CloudflareDNS{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: cfgatev1alpha1.CloudflareDNSSpec{
			TunnelRef: &cfgatev1alpha1.DNSTunnelRef{
				Name:      tunnelName,
				Namespace: tunnelNamespace,
			},
			Zones: []cfgatev1alpha1.DNSZoneConfig{
				{
					Name: testEnv.CloudflareZoneName,
				},
			},
			Policy: cfgatev1alpha1.DNSPolicySync,
			Source: cfgatev1alpha1.DNSHostnameSource{
				Explicit: explicitHostnames,
			},
			CleanupPolicy: cfgatev1alpha1.DNSCleanupPolicy{
				DeleteOnResourceRemoval: &deleteOnResourceRemoval,
			},
		},
	}
	Expect(k8sClient.Create(ctx, dns)).To(Succeed())
	return dns
}

// waitForDNSReady waits for a CloudflareDNS to have Ready=True condition.
func waitForDNSReady(ctx context.Context, k8sClient client.Client, name, namespace string, timeout time.Duration) *cfgatev1alpha1.CloudflareDNS {
	var dns cfgatev1alpha1.CloudflareDNS

	Eventually(func() bool {
		err := k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &dns)
		if err != nil {
			return false
		}
		for _, cond := range dns.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == metav1.ConditionTrue {
				return true
			}
		}
		return false
	}, timeout, DefaultInterval).Should(BeTrue(), "CloudflareDNS did not become ready")

	return &dns
}

// waitForDNSDeleted waits for a CloudflareDNS to be deleted from Kubernetes.
func waitForDNSDeleted(ctx context.Context, k8sClient client.Client, name, namespace string, timeout time.Duration) {
	Eventually(func() bool {
		var dns cfgatev1alpha1.CloudflareDNS
		err := k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &dns)
		return client.IgnoreNotFound(err) == nil && err != nil
	}, timeout, DefaultInterval).Should(BeTrue(), "CloudflareDNS was not deleted")
}

// waitForDNSRecordID waits for a DNS record to exist and returns its record ID.
func waitForDNSRecordID(ctx context.Context, cfClient *cloudflare.Client, zoneID, hostname, recordType string, timeout time.Duration) string {
	var recordID string
	Eventually(func(g Gomega) {
		record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, recordType)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(record).NotTo(BeNil(), "DNS record should exist")
		recordID = record.ID
		g.Expect(recordID).NotTo(BeEmpty(), "DNS record should have an ID")
	}, timeout, DefaultInterval).Should(Succeed())
	return recordID
}

// dnsRecordByIDStillExists uses the direct record lookup API to reduce flakiness
// from exact-name list calls under parallel E2E load.
func dnsRecordByIDStillExists(ctx context.Context, cfClient *cloudflare.Client, zoneID, recordID string) bool {
	exists, _ := dnsRecordByIDStatus(ctx, cfClient, zoneID, recordID)
	return exists
}

func dnsRecordByIDStatus(ctx context.Context, cfClient *cloudflare.Client, zoneID, recordID string) (bool, string) {
	record, err := cfClient.DNS.Records.Get(ctx, recordID, dns.RecordGetParams{
		ZoneID: cloudflare.F(zoneID),
	})
	if err == nil {
		if record == nil {
			return false, fmt.Sprintf("record %s returned nil", recordID)
		}
		return true, fmt.Sprintf("record %s still exists", recordID)
	}
	if strings.Contains(strings.ToLower(err.Error()), "not found") {
		return false, fmt.Sprintf("record %s not found", recordID)
	}
	GinkgoWriter.Printf("DNS record get-by-ID error (treating as still-exists): id=%s err=%v\n", recordID, err)
	return true, fmt.Sprintf("record %s lookup error treated as still-exists: %v", recordID, err)
}

// cleanupDNSRecord manually deletes a DNS record for test hygiene.
func cleanupDNSRecord(ctx context.Context, cfClient *cloudflare.Client, zoneID, hostname, recordType string) {
	record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, recordType)
	if err != nil || record == nil {
		return
	}
	_, _ = cfClient.DNS.Records.Delete(ctx, record.ID, dns.RecordDeleteParams{
		ZoneID: cloudflare.F(zoneID),
	})
}

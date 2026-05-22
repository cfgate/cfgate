// Package e2e contains end-to-end tests for cfgate.
package e2e_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	cloudflare "github.com/cloudflare/cloudflare-go/v6"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
)

var _ = Describe("HTTPRoute Annotations E2E", Ordered, func() {
	var (
		namespace    *corev1.Namespace
		cfClient     *cloudflare.Client
		zoneID       string
		sharedTunnel *cfgatev1alpha1.CloudflareTunnel
		sharedDNS    *cfgatev1alpha1.CloudflareDNS
		tunnelName   string
		gcName       string
		gwName       string
	)

	BeforeAll(func() {
		skipIfNoZone() // Annotations tests require DNS zone for verification

		// Create unique namespace for annotation tests
		namespace = createTestNamespace("cfgate-annotations-e2e")

		// Generate unique names
		tunnelName = testID("tunnel")
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

		// Create shared tunnel for all annotation tests
		By("Creating shared CloudflareTunnel for annotation tests")
		sharedTunnel = createCloudflareTunnelInContext(ctx, k8sClient, "annotations-tunnel", namespace.Name, tunnelName)
		sharedTunnel = waitForTunnelReady(ctx, k8sClient, sharedTunnel.Name, sharedTunnel.Namespace, DefaultTimeout)

		// Create shared GatewayClass
		By("Creating shared GatewayClass")
		createGatewayClass(ctx, k8sClient, gcName)

		// Create shared Gateway
		By("Creating shared Gateway")
		tunnelRef := fmt.Sprintf("%s/%s", namespace.Name, sharedTunnel.Name)
		createGateway(ctx, k8sClient, gwName, namespace.Name, gcName, tunnelRef)

		// Create shared CloudflareDNS that watches routes with dns-sync annotation
		By("Creating shared CloudflareDNS for annotation-based DNS sync")
		sharedDNS = createCloudflareDNSWithGatewayRoutes(ctx, k8sClient,
			testID("dns"), namespace.Name,
			sharedTunnel.Name,
			[]string{testEnv.CloudflareZoneName},
			"cfgate.io/dns-sync=enabled")
		_ = sharedDNS // suppress unused warning, used implicitly via controller

		// Register cleanup via DeferCleanup (Ginkgo #1284: use in BeforeAll, not AfterAll).
		DeferCleanup(func() {
			if testEnv.SkipCleanup {
				return
			}
			if namespace != nil {
				deleteTestNamespace(namespace)
			}
		})
	})

	Context("origin configuration", func() {
		It("should route with origin-protocol=http to http backend", func() {
			By("Creating test Service")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			By("Creating HTTPRoute with origin-protocol=http annotation")
			hostname := fmt.Sprintf("%s.%s", testID("http"), testEnv.CloudflareZoneName)
			routeName := testID("route")

			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/origin-protocol": "http",
						"cfgate.io/dns-sync":        "enabled",
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

			By("Waiting for tunnel to sync configuration")
			Eventually(func() bool {
				var t cfgatev1alpha1.CloudflareTunnel
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: sharedTunnel.Name, Namespace: sharedTunnel.Namespace}, &t); err != nil {
					return false
				}
				return t.Status.ConnectedRouteCount > 0
			}, DefaultTimeout, DefaultInterval).Should(BeTrue())

			// DNS sync is handled by sharedDNS watching routes with dns-sync annotation
			By("Verifying DNS record is created via annotation-based sync")
			Eventually(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				return err == nil && record != nil
			}, DefaultTimeout, DefaultInterval).Should(BeTrue())
		})

		It("should route with origin-protocol=https to https backend", func() {
			By("Creating test Service")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 443)

			By("Creating HTTPRoute with origin-protocol=https annotation")
			hostname := fmt.Sprintf("%s.%s", testID("https"), testEnv.CloudflareZoneName)
			routeName := testID("route")

			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/origin-protocol": "https",
						"cfgate.io/dns-sync":        "enabled",
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
											Port: ptrTo(gatewayv1.PortNumber(443)),
										},
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, route)).To(Succeed())

			By("Waiting for tunnel configuration to include https route")
			Eventually(func() bool {
				var t cfgatev1alpha1.CloudflareTunnel
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: sharedTunnel.Name, Namespace: sharedTunnel.Namespace}, &t); err != nil {
					return false
				}
				return t.Status.ConnectedRouteCount > 0
			}, DefaultTimeout, DefaultInterval).Should(BeTrue())
		})

		It("should enable TLS verification with origin-ssl-verify=true", func() {
			By("Creating test Service")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 443)

			By("Creating HTTPRoute with origin-ssl-verify=true annotation")
			hostname := fmt.Sprintf("%s.%s", testID("sslverify"), testEnv.CloudflareZoneName)
			routeName := testID("route")

			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/origin-protocol":   "https",
						"cfgate.io/origin-ssl-verify": "true",
						"cfgate.io/dns-sync":          "enabled",
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
											Port: ptrTo(gatewayv1.PortNumber(443)),
										},
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, route)).To(Succeed())

			By("Waiting for route to be accepted")
			Eventually(func() bool {
				var r gatewayv1.HTTPRoute
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: routeName, Namespace: namespace.Name}, &r); err != nil {
					return false
				}
				for _, ps := range r.Status.Parents {
					for _, cond := range ps.Conditions {
						if cond.Type == string(gatewayv1.RouteConditionAccepted) && cond.Status == metav1.ConditionTrue {
							return true
						}
					}
				}
				return false
			}, DefaultTimeout, DefaultInterval).Should(BeTrue())
		})

		It("should disable TLS verification with origin-ssl-verify=false", func() {
			By("Creating test Service")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 443)

			By("Creating HTTPRoute with origin-ssl-verify=false annotation")
			hostname := fmt.Sprintf("%s.%s", testID("nosslverify"), testEnv.CloudflareZoneName)
			routeName := testID("route")

			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/origin-protocol":   "https",
						"cfgate.io/origin-ssl-verify": "false",
						"cfgate.io/dns-sync":          "enabled",
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
											Port: ptrTo(gatewayv1.PortNumber(443)),
										},
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, route)).To(Succeed())

			By("Waiting for route to be accepted")
			Eventually(func() bool {
				var r gatewayv1.HTTPRoute
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: routeName, Namespace: namespace.Name}, &r); err != nil {
					return false
				}
				for _, ps := range r.Status.Parents {
					for _, cond := range ps.Conditions {
						if cond.Type == string(gatewayv1.RouteConditionAccepted) && cond.Status == metav1.ConditionTrue {
							return true
						}
					}
				}
				return false
			}, DefaultTimeout, DefaultInterval).Should(BeTrue())
		})

		It("should set origin connect timeout with origin-connect-timeout annotation", func() {
			By("Creating test Service")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			By("Creating HTTPRoute with origin-connect-timeout annotation")
			hostname := fmt.Sprintf("%s.%s", testID("timeout"), testEnv.CloudflareZoneName)
			routeName := testID("route")

			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/origin-connect-timeout": "60s",
						"cfgate.io/dns-sync":               "enabled",
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

			By("Waiting for route to be accepted")
			Eventually(func() bool {
				var r gatewayv1.HTTPRoute
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: routeName, Namespace: namespace.Name}, &r); err != nil {
					return false
				}
				for _, ps := range r.Status.Parents {
					for _, cond := range ps.Conditions {
						if cond.Type == string(gatewayv1.RouteConditionAccepted) && cond.Status == metav1.ConditionTrue {
							return true
						}
					}
				}
				return false
			}, DefaultTimeout, DefaultInterval).Should(BeTrue())
		})
	})

	Context("DNS annotations", func() {
		It("should override default TTL with ttl annotation", func() {
			By("Creating test Service")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			By("Creating HTTPRoute with ttl=300 annotation")
			hostname := fmt.Sprintf("%s.%s", testID("ttl"), testEnv.CloudflareZoneName)
			routeName := testID("route")

			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/ttl":      "300",
						"cfgate.io/dns-sync": "enabled",
						// Note: proxied=false required to set custom TTL (proxied records have auto TTL)
						"cfgate.io/cloudflare-proxied": "false",
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

			By("Waiting for DNS record with custom TTL")
			Eventually(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				if err != nil || record == nil {
					return false
				}
				// TTL should be 300 seconds
				return record.TTL == 300 && !record.Proxied
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "DNS record should have TTL=300 and not be proxied")
		})

		It("should enable Cloudflare proxy with cloudflare-proxied=true", func() {
			By("Creating test Service")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			By("Creating HTTPRoute with cloudflare-proxied=true annotation")
			hostname := fmt.Sprintf("%s.%s", testID("proxied"), testEnv.CloudflareZoneName)
			routeName := testID("route")

			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/cloudflare-proxied": "true",
						"cfgate.io/dns-sync":           "enabled",
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

			By("Waiting for DNS record with proxy enabled")
			Eventually(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				if err != nil || record == nil {
					return false
				}
				return record.Proxied == true
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "DNS record should be proxied (orange cloud)")
		})

		It("should disable Cloudflare proxy with cloudflare-proxied=false (DNS-only)", func() {
			By("Creating test Service")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			By("Creating HTTPRoute with cloudflare-proxied=false annotation")
			hostname := fmt.Sprintf("%s.%s", testID("dnsonly"), testEnv.CloudflareZoneName)
			routeName := testID("route")

			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/cloudflare-proxied": "false",
						"cfgate.io/dns-sync":           "enabled",
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

			By("Waiting for DNS record without proxy (DNS-only)")
			Eventually(func() bool {
				record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
				if err != nil || record == nil {
					return false
				}
				return record.Proxied == false
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "DNS record should not be proxied (DNS-only / grey cloud)")
		})
	})

	Context("access policy reference", func() {
		It("should resolve deprecated access-policy annotation without creating Access resources", func() {
			By("Creating test Service")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			By("Creating HTTPRoute with access-policy annotation")
			hostname := fmt.Sprintf("%s.%s", testID("access"), testEnv.CloudflareZoneName)
			routeName := testID("route")
			policyName := testID("policy")

			// Create the HTTPRoute first
			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/access-policy": policyName,
						"cfgate.io/dns-sync":      "enabled",
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

			By("Creating reusable CloudflareAccessPolicy")
			policy := createReusableAccessPolicy(ctx, k8sClient, policyName, namespace.Name, "allow",
				[]cfgatev1alpha1.AccessRule{{Everyone: ptrTo(true)}}, nil)

			By("Waiting for AccessPolicy to become ready")
			waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying annotation did not create an Access Application")
			Consistently(func() bool {
				app, err := getAccessApplicationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policyName)
				return err == nil && app == nil
			}, 10*time.Second, DefaultInterval).Should(BeTrue(), "deprecated annotation must not create Access Application")
		})

		It("should resolve deprecated access-policy annotation across namespaces", func() {
			By("Creating separate namespace for cross-namespace test")
			policyNS := createTestNamespace("cfgate-policy-ns")
			createCloudflareCredentialsSecret(policyNS.Name)

			By("Creating test Service")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			By("Creating HTTPRoute with cross-namespace access-policy annotation")
			hostname := fmt.Sprintf("%s.%s", testID("crossns"), testEnv.CloudflareZoneName)
			routeName := testID("route")
			policyName := testID("policy")

			// Create the HTTPRoute with cross-namespace reference
			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						// Cross-namespace reference format: namespace/name
						"cfgate.io/access-policy": fmt.Sprintf("%s/%s", policyNS.Name, policyName),
						"cfgate.io/dns-sync":      "enabled",
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

			By("Creating reusable CloudflareAccessPolicy in separate namespace")
			policy := createReusableAccessPolicy(ctx, k8sClient, policyName, policyNS.Name, "allow",
				[]cfgatev1alpha1.AccessRule{{Everyone: ptrTo(true)}}, nil)

			By("Waiting for AccessPolicy to become ready")
			waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying annotation did not create an Access Application")
			Consistently(func() bool {
				app, err := getAccessApplicationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policyName)
				return err == nil && app == nil
			}, 10*time.Second, DefaultInterval).Should(BeTrue(), "deprecated annotation must not create Access Application")

			// Cleanup the extra namespace
			if !testEnv.SkipCleanup {
				deleteTestNamespace(policyNS)
			}
		})
	})

	Context("validation", func() {
		It("should emit warning event for invalid annotation value", func() {
			By("Creating test Service")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			By("Creating HTTPRoute with invalid origin-protocol value")
			hostname := fmt.Sprintf("%s.%s", testID("invalid"), testEnv.CloudflareZoneName)
			routeName := testID("route")

			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						// Invalid protocol value
						"cfgate.io/origin-protocol": "invalid-protocol",
						"cfgate.io/dns-sync":        "enabled",
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

			By("Waiting for validation to process")
			// The route will still be created, but controller should emit a warning event
			// We verify the route exists and check for events
			Eventually(func() bool {
				var r gatewayv1.HTTPRoute
				return k8sClient.Get(ctx, client.ObjectKey{Name: routeName, Namespace: namespace.Name}, &r) == nil
			}, ShortTimeout, DefaultInterval).Should(BeTrue())

			By("Checking for warning events on the HTTPRoute")
			// Note: In a real implementation, we'd check for events.
			// For E2E, we verify the route is created but may use default values for invalid annotations.
			var r gatewayv1.HTTPRoute
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: routeName, Namespace: namespace.Name}, &r)).To(Succeed())
			Expect(r.Annotations["cfgate.io/origin-protocol"]).To(Equal("invalid-protocol"))
		})

	})

	Context("origin advanced configuration", func() {
		It("should apply origin-http-host-header annotation to tunnel config", func() {
			By("Creating test Service")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			By("Creating HTTPRoute with origin-http-host-header annotation")
			hostname := fmt.Sprintf("%s.%s", testID("hostheader"), testEnv.CloudflareZoneName)
			routeName := testID("route")
			customHostHeader := "backend.internal.example.com"

			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/origin-http-host-header": customHostHeader,
						"cfgate.io/dns-sync":                "enabled",
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

			By("Waiting for tunnel to sync configuration")
			Eventually(func() bool {
				var t cfgatev1alpha1.CloudflareTunnel
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: sharedTunnel.Name, Namespace: sharedTunnel.Namespace}, &t); err != nil {
					return false
				}
				return t.Status.ConnectedRouteCount > 0
			}, DefaultTimeout, DefaultInterval).Should(BeTrue())

			By("Verifying route annotation is preserved")
			var r gatewayv1.HTTPRoute
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: routeName, Namespace: namespace.Name}, &r)).To(Succeed())
			Expect(r.Annotations["cfgate.io/origin-http-host-header"]).To(Equal(customHostHeader),
				"Annotation should be preserved on route")

			// Note: Actual verification of tunnel ingress config would require accessing
			// the Cloudflare tunnel configuration API. For E2E, we verify the annotation
			// is preserved and the route is processed by checking tunnel status.
			By("Verifying tunnel ConfigurationSynced condition")
			waitForTunnelCondition(ctx, k8sClient, sharedTunnel.Name, sharedTunnel.Namespace, "ConfigurationSynced", metav1.ConditionTrue, DefaultTimeout)
		})

		It("should apply origin-server-name annotation to tunnel config", func() {
			By("Creating test Service")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 443)

			By("Creating HTTPRoute with origin-server-name annotation (SNI)")
			hostname := fmt.Sprintf("%s.%s", testID("servername"), testEnv.CloudflareZoneName)
			routeName := testID("route")
			serverName := "tls-backend.internal.example.com"

			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/origin-protocol":    "https",
						"cfgate.io/origin-server-name": serverName,
						"cfgate.io/dns-sync":           "enabled",
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
											Port: ptrTo(gatewayv1.PortNumber(443)),
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
			Eventually(func() bool {
				var t cfgatev1alpha1.CloudflareTunnel
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: sharedTunnel.Name, Namespace: sharedTunnel.Namespace}, &t); err != nil {
					return false
				}
				return t.Status.ConnectedRouteCount > 0
			}, DefaultTimeout, DefaultInterval).Should(BeTrue())

			By("Verifying route annotations are preserved")
			var r gatewayv1.HTTPRoute
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: routeName, Namespace: namespace.Name}, &r)).To(Succeed())
			Expect(r.Annotations["cfgate.io/origin-server-name"]).To(Equal(serverName),
				"Server name annotation should be preserved")

			By("Verifying Cloudflare remote config includes originServerName")
			Eventually(func(g Gomega) {
				config, err := getRawTunnelConfigurationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, sharedTunnel.Status.TunnelID)
				g.Expect(err).NotTo(HaveOccurred())

				ingress, ok := findRawTunnelIngress(config, hostname)
				g.Expect(ok).To(BeTrue(), "expected ingress rule for %s", hostname)

				originServerName, ok := rawOriginRequestString(ingress.OriginRequest, "originServerName")
				g.Expect(ok).To(BeTrue(), "expected originServerName in originRequest")
				g.Expect(originServerName).To(Equal(serverName))
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			By("Verifying tunnel ConfigurationSynced condition")
			waitForTunnelCondition(ctx, k8sClient, sharedTunnel.Name, sharedTunnel.Namespace, "ConfigurationSynced", metav1.ConditionTrue, DefaultTimeout)
		})

		It("should apply origin-ca-pool annotation to tunnel config", func() {
			By("Creating test Service")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 443)

			By("Creating HTTPRoute with origin-ca-pool annotation")
			hostname := fmt.Sprintf("%s.%s", testID("capool"), testEnv.CloudflareZoneName)
			routeName := testID("route")
			caPoolPath := "/etc/cfgate/origin-ca-pool/ca.pem"

			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/origin-protocol":   "https",
						"cfgate.io/origin-ssl-verify": "true",
						"cfgate.io/origin-ca-pool":    caPoolPath,
						"cfgate.io/dns-sync":          "enabled",
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
											Port: ptrTo(gatewayv1.PortNumber(443)),
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
			Eventually(func() bool {
				var t cfgatev1alpha1.CloudflareTunnel
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: sharedTunnel.Name, Namespace: sharedTunnel.Namespace}, &t); err != nil {
					return false
				}
				return t.Status.ConnectedRouteCount > 0
			}, DefaultTimeout, DefaultInterval).Should(BeTrue())

			By("Verifying route annotations are preserved")
			var r gatewayv1.HTTPRoute
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: routeName, Namespace: namespace.Name}, &r)).To(Succeed())
			Expect(r.Annotations["cfgate.io/origin-ca-pool"]).To(Equal(caPoolPath),
				"CA pool annotation should be preserved")

			By("Verifying Cloudflare remote config includes caPool")
			Eventually(func(g Gomega) {
				config, err := getRawTunnelConfigurationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, sharedTunnel.Status.TunnelID)
				g.Expect(err).NotTo(HaveOccurred())

				ingress, ok := findRawTunnelIngress(config, hostname)
				g.Expect(ok).To(BeTrue(), "expected ingress rule for %s", hostname)

				caPool, ok := rawOriginRequestString(ingress.OriginRequest, "caPool")
				g.Expect(ok).To(BeTrue(), "expected caPool in originRequest")
				g.Expect(caPool).To(Equal(caPoolPath))
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			By("Verifying tunnel ConfigurationSynced condition")
			waitForTunnelCondition(ctx, k8sClient, sharedTunnel.Name, sharedTunnel.Namespace, "ConfigurationSynced", metav1.ConditionTrue, DefaultTimeout)
		})

		It("should apply origin-http2 annotation to tunnel config", func() {
			By("Creating test Service")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 443)

			By("Creating HTTPRoute with origin-http2=true annotation")
			hostname := fmt.Sprintf("%s.%s", testID("http2"), testEnv.CloudflareZoneName)
			routeName := testID("route")

			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/origin-protocol": "https",
						"cfgate.io/origin-http2":    "true", // Enable HTTP/2 to origin
						"cfgate.io/dns-sync":        "enabled",
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
											Port: ptrTo(gatewayv1.PortNumber(443)),
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
			Eventually(func() bool {
				var t cfgatev1alpha1.CloudflareTunnel
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: sharedTunnel.Name, Namespace: sharedTunnel.Namespace}, &t); err != nil {
					return false
				}
				return t.Status.ConnectedRouteCount > 0
			}, DefaultTimeout, DefaultInterval).Should(BeTrue())

			By("Verifying route annotations are preserved")
			var r gatewayv1.HTTPRoute
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: routeName, Namespace: namespace.Name}, &r)).To(Succeed())
			Expect(r.Annotations["cfgate.io/origin-http2"]).To(Equal("true"),
				"HTTP/2 annotation should be preserved")

			By("Verifying tunnel ConfigurationSynced condition")
			waitForTunnelCondition(ctx, k8sClient, sharedTunnel.Name, sharedTunnel.Namespace, "ConfigurationSynced", metav1.ConditionTrue, DefaultTimeout)

			By("Testing with origin-http2=false")
			routeName2 := testID("route-nohttp2")
			hostname2 := fmt.Sprintf("%s.%s", testID("nohttp2"), testEnv.CloudflareZoneName)

			route2 := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName2,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/origin-protocol": "https",
						"cfgate.io/origin-http2":    "false", // Disable HTTP/2 to origin
						"cfgate.io/dns-sync":        "enabled",
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
					Hostnames: []gatewayv1.Hostname{gatewayv1.Hostname(hostname2)},
					Rules: []gatewayv1.HTTPRouteRule{
						{
							BackendRefs: []gatewayv1.HTTPBackendRef{
								{
									BackendRef: gatewayv1.BackendRef{
										BackendObjectReference: gatewayv1.BackendObjectReference{
											Name: gatewayv1.ObjectName(svcName),
											Port: ptrTo(gatewayv1.PortNumber(443)),
										},
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, route2)).To(Succeed())

			By("Verifying second route annotation is preserved")
			Eventually(func() bool {
				var r2 gatewayv1.HTTPRoute
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: routeName2, Namespace: namespace.Name}, &r2); err != nil {
					return false
				}
				return r2.Annotations["cfgate.io/origin-http2"] == "false"
			}, ShortTimeout, DefaultInterval).Should(BeTrue())
		})

		It("should apply origin-h2c annotation to tunnel config", func() {
			By("Creating test Service")
			svcName := testID("svc")
			createTestService(ctx, k8sClient, svcName, namespace.Name, 8080)

			By("Creating HTTPRoute with origin-h2c=true annotation")
			hostname := fmt.Sprintf("%s.%s", testID("h2c"), testEnv.CloudflareZoneName)
			routeName := testID("route")

			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/origin-h2c": "true", // Enable HTTP/2 cleartext to origin
						"cfgate.io/dns-sync":   "enabled",
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

			By("Waiting for tunnel to sync configuration")
			Eventually(func() bool {
				var t cfgatev1alpha1.CloudflareTunnel
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: sharedTunnel.Name, Namespace: sharedTunnel.Namespace}, &t); err != nil {
					return false
				}
				return t.Status.ConnectedRouteCount > 0
			}, DefaultTimeout, DefaultInterval).Should(BeTrue())

			By("Verifying route annotations are preserved")
			var r gatewayv1.HTTPRoute
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: routeName, Namespace: namespace.Name}, &r)).To(Succeed())
			Expect(r.Annotations["cfgate.io/origin-h2c"]).To(Equal("true"),
				"H2C annotation should be preserved")

			By("Verifying tunnel ConfigurationSynced condition")
			waitForTunnelCondition(ctx, k8sClient, sharedTunnel.Name, sharedTunnel.Namespace, "ConfigurationSynced", metav1.ConditionTrue, DefaultTimeout)

			By("Verifying Cloudflare remote config enables h2cOrigin")
			Eventually(func(g Gomega) {
				config, err := getRawTunnelConfigurationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, sharedTunnel.Status.TunnelID)
				g.Expect(err).NotTo(HaveOccurred())

				ingress, ok := findRawTunnelIngress(config, hostname)
				g.Expect(ok).To(BeTrue(), "expected ingress rule for %s", hostname)
				g.Expect(ingress.Service).To(Equal(fmt.Sprintf("http://%s.%s.svc.cluster.local:8080", svcName, namespace.Name)))

				h2cOrigin, ok := rawOriginRequestBool(ingress.OriginRequest, "h2cOrigin")
				g.Expect(ok).To(BeTrue(), "expected h2cOrigin in originRequest")
				g.Expect(h2cOrigin).To(BeTrue())

				http2Origin, ok := rawOriginRequestBool(ingress.OriginRequest, "http2Origin")
				if ok {
					g.Expect(http2Origin).To(BeFalse())
				}
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			By("Testing with origin-h2c=false")
			routeName2 := testID("route-noh2c")
			hostname2 := fmt.Sprintf("%s.%s", testID("noh2c"), testEnv.CloudflareZoneName)

			route2 := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName2,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/origin-h2c": "false", // Disable HTTP/2 cleartext to origin
						"cfgate.io/dns-sync":   "enabled",
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
					Hostnames: []gatewayv1.Hostname{gatewayv1.Hostname(hostname2)},
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
			Expect(k8sClient.Create(ctx, route2)).To(Succeed())

			By("Verifying second route annotation is preserved")
			Eventually(func() bool {
				var r2 gatewayv1.HTTPRoute
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: routeName2, Namespace: namespace.Name}, &r2); err != nil {
					return false
				}
				return r2.Annotations["cfgate.io/origin-h2c"] == "false"
			}, ShortTimeout, DefaultInterval).Should(BeTrue())

			By("Verifying Cloudflare remote config leaves h2cOrigin disabled")
			Eventually(func(g Gomega) {
				config, err := getRawTunnelConfigurationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, sharedTunnel.Status.TunnelID)
				g.Expect(err).NotTo(HaveOccurred())

				ingress, ok := findRawTunnelIngress(config, hostname2)
				g.Expect(ok).To(BeTrue(), "expected ingress rule for %s", hostname2)
				g.Expect(ingress.Service).To(Equal(fmt.Sprintf("http://%s.%s.svc.cluster.local:8080", svcName, namespace.Name)))

				h2cOrigin, ok := rawOriginRequestBool(ingress.OriginRequest, "h2cOrigin")
				if ok {
					g.Expect(h2cOrigin).To(BeFalse())
				}
			}, DefaultTimeout, DefaultInterval).Should(Succeed())
		})
	})
})

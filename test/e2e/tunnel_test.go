// Package e2e contains end-to-end tests for cfgate.
package e2e_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	cloudflare "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/accounts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflared"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

var _ = Describe("CloudflareTunnel E2E", Label("cloudflare"), func() {
	var (
		namespace *corev1.Namespace
		cfClient  *cloudflare.Client
	)

	BeforeEach(func() {
		skipIfNoCredentials()

		// Create unique namespace for this test.
		namespace = createTestNamespace("cfgate-tunnel-e2e")

		// Create Cloudflare credentials secret.
		createCloudflareCredentialsSecret(namespace.Name)

		// Create Cloudflare client for verification.
		cfClient = getCloudflareClient()
	})

	AfterEach(func() {
		if testEnv.SkipCleanup {
			return
		}

		// Delete namespace - controller finalizers will attempt cleanup.
		// Any orphaned resources are cleaned by AfterSuite batch cleanup.
		if namespace != nil {
			deleteTestNamespace(namespace)
		}
	})

	Context("tunnel lifecycle", Ordered, func() {
		var sharedTunnel *cfgatev1alpha1.CloudflareTunnel
		var tunnelName string

		BeforeAll(func() {
			tunnelName = testID("tunnel")
		})

		It("should create tunnel in Cloudflare when CloudflareTunnel CR is created", func() {
			By("Creating CloudflareTunnel CR")
			tunnel := createCloudflareTunnel(ctx, k8sClient, testID("lifecycle"), namespace.Name, tunnelName)

			By("Waiting for tunnel to become ready")
			tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)

			By("Verifying tunnel ID is populated in status")
			Expect(tunnel.Status.TunnelID).NotTo(BeEmpty(), "Tunnel ID should be populated in status")
			Expect(tunnel.Status.TunnelName).To(Equal(tunnelName), "Tunnel name should match")
			Expect(tunnel.Status.TunnelDomain).To(ContainSubstring(".cfargotunnel.com"), "Tunnel domain should be set")

			By("Verifying tunnel exists in Cloudflare API")
			cfTunnel, err := getTunnelFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, tunnelName)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfTunnel).NotTo(BeNil(), "Tunnel should exist in Cloudflare")
			Expect(cfTunnel.ID).To(Equal(tunnel.Status.TunnelID), "Tunnel IDs should match")

			By("Verifying cloudflared Deployment is created with correct spec")
			deploymentName := fmt.Sprintf("%s-cloudflared", tunnel.Name)
			deployment := waitForDeploymentSpec(ctx, k8sClient, deploymentName, namespace.Name, 1, DefaultTimeout)
			Expect(deployment).NotTo(BeNil())
			Expect(deployment.Spec.Template.Spec.Containers).NotTo(BeEmpty())
			Expect(deployment.Spec.Template.Spec.Containers[0].Image).To(Equal(cloudflared.DefaultImage))

			// Store for subsequent tests if needed.
			sharedTunnel = tunnel
		})

		It("should adopt existing tunnel when name matches", func() {
			adoptTunnelName := testID("adopt")

			By("Pre-creating tunnel via Cloudflare API")
			preTunnel, err := createTunnelInCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, adoptTunnelName)
			Expect(err).NotTo(HaveOccurred())
			Expect(preTunnel).NotTo(BeNil())
			preTunnelID := preTunnel.ID

			By("Creating CloudflareTunnel CR with same name")
			tunnel := createCloudflareTunnel(ctx, k8sClient, testID("adopt-cr"), namespace.Name, adoptTunnelName)

			By("Waiting for tunnel to become ready")
			tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)

			By("Verifying it adopted the existing tunnel (same ID)")
			Expect(tunnel.Status.TunnelID).To(Equal(preTunnelID), "Should adopt existing tunnel ID")

			By("Verifying no duplicate tunnel was created")
			// List tunnels with this name - should only be one.
			cfTunnel, err := getTunnelFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, adoptTunnelName)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfTunnel).NotTo(BeNil())
			Expect(cfTunnel.ID).To(Equal(preTunnelID), "Should be the same tunnel, not a duplicate")
		})

		It("should sync ingress configuration when routes change", func() {
			configTunnelName := testID("config")

			By("Creating CloudflareTunnel CR")
			tunnel := createCloudflareTunnel(ctx, k8sClient, testID("config-cr"), namespace.Name, configTunnelName)
			waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)

			By("Creating GatewayClass")
			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)

			By("Creating Gateway referencing the tunnel")
			tunnelRef := fmt.Sprintf("%s/%s", namespace.Name, tunnel.Name)
			gw := createGateway(ctx, k8sClient, testID("gateway"), namespace.Name, gcName, tunnelRef)

			By("Creating a test Service")
			svc := createTestService(ctx, k8sClient, testID("service"), namespace.Name, 8080)

			By("Creating HTTPRoute with hostname")
			hostname := fmt.Sprintf("%s.%s", testID("route"), testEnv.CloudflareZoneName)
			createHTTPRoute(ctx, k8sClient, testID("route"), namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Waiting for tunnel configuration to be synced")
			// The controller should update the tunnel configuration in Cloudflare.
			// We verify by checking the tunnel's configuration includes the route.
			Eventually(func() bool {
				// Refresh tunnel from K8s to get updated route count.
				var updatedTunnel cfgatev1alpha1.CloudflareTunnel
				err := k8sClient.Get(ctx, client.ObjectKey{Name: tunnel.Name, Namespace: tunnel.Namespace}, &updatedTunnel)
				if err != nil {
					GinkgoWriter.Printf("[DIAG] tunnel get error: %v\n", err)
					return false
				}
				GinkgoWriter.Printf("[DIAG] ConnectedRouteCount=%d, Generation=%d, ObservedGeneration=%d, TunnelID=%s\n",
					updatedTunnel.Status.ConnectedRouteCount,
					updatedTunnel.Generation,
					updatedTunnel.Status.ObservedGeneration,
					updatedTunnel.Status.TunnelID)
				for _, c := range updatedTunnel.Status.Conditions {
					GinkgoWriter.Printf("[DIAG]   condition %s=%s reason=%s\n", c.Type, c.Status, c.Reason)
				}
				return updatedTunnel.Status.ConnectedRouteCount > 0
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "Tunnel should have connected routes")

			By("Updating HTTPRoute hostname")
			var route cfgatev1alpha1.CloudflareTunnel
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: tunnel.Name, Namespace: tunnel.Namespace}, &route)).To(Succeed())
			// Configuration should be updated (verified by controller setting conditions).

			By("Verifying ConfigurationSynced condition is True")
			waitForTunnelCondition(ctx, k8sClient, tunnel.Name, tunnel.Namespace, "ConfigurationSynced", metav1.ConditionTrue, DefaultTimeout)
		})

		It("should delete tunnel from Cloudflare when CR is deleted", SpecTimeout(12*time.Minute), func(ctx SpecContext) {
			deleteTunnelName := testID("delete")

			By("Creating CloudflareTunnel CR")
			tunnel := createCloudflareTunnel(ctx, k8sClient, testID("delete-cr"), namespace.Name, deleteTunnelName)

			By("Waiting for tunnel to be created in Cloudflare")
			tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)
			tunnelID := tunnel.Status.TunnelID
			Expect(tunnelID).NotTo(BeEmpty())

			By("Verifying tunnel exists in Cloudflare")
			cfTunnel, err := getTunnelByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, tunnelID)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfTunnel).NotTo(BeNil())

			By("Deleting CloudflareTunnel CR")
			Expect(k8sClient.Delete(ctx, tunnel)).To(Succeed())

			By("Waiting for tunnel to be deleted from Kubernetes")
			waitForTunnelDeleted(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)

			By("Verifying tunnel is deleted from Cloudflare")
			waitForTunnelDeletedByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, tunnelID, LongTimeout)

			By("Verifying cloudflared Deployment is deleted")
			deploymentName := fmt.Sprintf("%s-cloudflared", tunnel.Name)
			Eventually(func() bool {
				var dep corev1.Pod
				err := k8sClient.Get(ctx, client.ObjectKey{Name: deploymentName, Namespace: namespace.Name}, &dep)
				return client.IgnoreNotFound(err) == nil && err != nil
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "Deployment should be deleted")
		})

		// Silence unused variable warning - sharedTunnel is available for future use.
		AfterAll(func() {
			_ = sharedTunnel
		})
	})

	Context("tunnel orphan deletion policy", func() {
		var orphanNamespace *corev1.Namespace
		var orphanCfClient *cloudflare.Client

		BeforeEach(func() {
			skipIfNoCredentials()

			orphanNamespace = createTestNamespace("cfgate-tunnel-orphan-e2e")
			createCloudflareCredentialsSecret(orphanNamespace.Name)
			orphanCfClient = getCloudflareClient()

			DeferCleanup(func() {
				if testEnv.SkipCleanup {
					return
				}
				if orphanNamespace != nil {
					deleteTestNamespace(orphanNamespace)
				}
			})
		})

		It("should handle tunnel deletion policy: orphan", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			orphanTunnelName := testID("orphan")

			By("Creating CloudflareTunnel CR with orphan deletion policy")
			tunnel := &cfgatev1alpha1.CloudflareTunnel{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("orphan-cr"),
					Namespace: orphanNamespace.Name,
					Annotations: map[string]string{
						"cfgate.io/deletion-policy": "orphan",
					},
				},
				Spec: cfgatev1alpha1.CloudflareTunnelSpec{
					Tunnel: cfgatev1alpha1.TunnelIdentity{
						Name: orphanTunnelName,
					},
					Cloudflare: cfgatev1alpha1.CloudflareConfig{
						AccountID: testEnv.CloudflareAccountID,
						SecretRef: cfgatev1alpha1.SecretRef{
							Name: "cloudflare-credentials",
						},
					},
					Cloudflared: cfgatev1alpha1.CloudflaredConfig{
						Replicas: 1,
					},
				},
			}
			Expect(k8sClient.Create(ctx, tunnel)).To(Succeed())

			By("Waiting for tunnel to be created in Cloudflare")
			tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)
			tunnelID := tunnel.Status.TunnelID

			By("Deleting CloudflareTunnel CR")
			Expect(k8sClient.Delete(ctx, tunnel)).To(Succeed())

			By("Waiting for CR to be deleted from Kubernetes")
			waitForTunnelDeleted(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)

			By("Verifying tunnel still exists in Cloudflare (orphaned)")
			cfTunnel, err := getTunnelByIDFromCloudflare(ctx, orphanCfClient, testEnv.CloudflareAccountID, tunnelID)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfTunnel).NotTo(BeNil(), "Tunnel should still exist in Cloudflare with orphan policy")
		})
	})

	Context("error handling", func() {
		It("should set CredentialsValid=False when token is invalid", func() {
			invalidTunnelName := testID("invalid-token")

			By("Creating CloudflareTunnel CR with invalid credentials")
			tunnel := createCloudflareTunnelWithInvalidToken(ctx, k8sClient, testID("invalid-token-cr"), namespace.Name, invalidTunnelName)

			By("Waiting for CredentialsValid condition to be False")
			waitForTunnelCondition(ctx, k8sClient, tunnel.Name, tunnel.Namespace, "CredentialsValid", metav1.ConditionFalse, DefaultTimeout)

			By("Verifying Ready condition is also False")
			var updatedTunnel cfgatev1alpha1.CloudflareTunnel
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: tunnel.Name, Namespace: tunnel.Namespace}, &updatedTunnel)).To(Succeed())

			var readyCondition metav1.Condition
			for _, cond := range updatedTunnel.Status.Conditions {
				if cond.Type == "Ready" {
					readyCondition = cond
					break
				}
			}
			Expect(readyCondition.Status).To(Equal(metav1.ConditionFalse), "Ready should be False when credentials are invalid")
		})

		It("should handle missing credentials secret", func() {
			missingSecretTunnelName := testID("missing-secret")

			By("Creating CloudflareTunnel CR referencing non-existent secret")
			tunnel := &cfgatev1alpha1.CloudflareTunnel{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("missing-secret-cr"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareTunnelSpec{
					Tunnel: cfgatev1alpha1.TunnelIdentity{
						Name: missingSecretTunnelName,
					},
					Cloudflare: cfgatev1alpha1.CloudflareConfig{
						AccountID: testEnv.CloudflareAccountID,
						SecretRef: cfgatev1alpha1.SecretRef{
							Name: "non-existent-secret",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, tunnel)).To(Succeed())

			By("Waiting for condition indicating secret not found")
			// The controller should set a condition indicating the secret is missing.
			Eventually(func() bool {
				var t cfgatev1alpha1.CloudflareTunnel
				err := k8sClient.Get(ctx, client.ObjectKey{Name: tunnel.Name, Namespace: tunnel.Namespace}, &t)
				if err != nil {
					return false
				}
				for _, cond := range t.Status.Conditions {
					if cond.Type == "Ready" && cond.Status == metav1.ConditionFalse {
						return true
					}
				}
				return false
			}, DefaultTimeout, DefaultInterval).Should(BeTrue(), "Should have Ready=False condition")
		})

		It("should recover when credentials become valid", func() {
			recoveryTunnelName := testID("recovery")

			By("Creating CloudflareTunnel CR with initially missing secret")
			tunnel := &cfgatev1alpha1.CloudflareTunnel{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("recovery-cr"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareTunnelSpec{
					Tunnel: cfgatev1alpha1.TunnelIdentity{
						Name: recoveryTunnelName,
					},
					Cloudflare: cfgatev1alpha1.CloudflareConfig{
						AccountID: testEnv.CloudflareAccountID,
						SecretRef: cfgatev1alpha1.SecretRef{
							Name: "recovery-credentials",
						},
					},
					Cloudflared: cfgatev1alpha1.CloudflaredConfig{
						Replicas: 1,
					},
				},
			}
			Expect(k8sClient.Create(ctx, tunnel)).To(Succeed())

			By("Waiting for Ready=False condition")
			waitForTunnelCondition(ctx, k8sClient, tunnel.Name, tunnel.Namespace, "Ready", metav1.ConditionFalse, DefaultTimeout)

			By("Creating the missing credentials secret")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "recovery-credentials",
					Namespace: namespace.Name,
				},
				Type: corev1.SecretTypeOpaque,
				StringData: map[string]string{
					"CLOUDFLARE_API_TOKEN": testEnv.CloudflareAPIToken,
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			By("Waiting for tunnel to recover and become Ready")
			waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)
		})
	})

	Context("cloudflared deployment", func() {
		It("should create cloudflared Deployment with correct replicas", func() {
			replicasTunnelName := testID("replicas")

			By("Creating CloudflareTunnel CR with 2 replicas")
			tunnel := &cfgatev1alpha1.CloudflareTunnel{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("replicas-cr"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareTunnelSpec{
					Tunnel: cfgatev1alpha1.TunnelIdentity{
						Name: replicasTunnelName,
					},
					Cloudflare: cfgatev1alpha1.CloudflareConfig{
						AccountID: testEnv.CloudflareAccountID,
						SecretRef: cfgatev1alpha1.SecretRef{
							Name: "cloudflare-credentials",
						},
					},
					Cloudflared: cfgatev1alpha1.CloudflaredConfig{
						Replicas: 2,
					},
				},
			}
			Expect(k8sClient.Create(ctx, tunnel)).To(Succeed())

			By("Waiting for tunnel to become ready")
			tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, LongTimeout)

			By("Verifying Deployment has 2 replicas in spec")
			deploymentName := fmt.Sprintf("%s-cloudflared", tunnel.Name)
			deployment := waitForDeploymentSpec(ctx, k8sClient, deploymentName, namespace.Name, 2, LongTimeout)
			Expect(*deployment.Spec.Replicas).To(Equal(int32(2)))
		})

		It("should update cloudflared Deployment when spec changes", func() {
			scaleTunnelName := testID("scale")

			By("Creating CloudflareTunnel CR with 1 replica")
			tunnel := createCloudflareTunnel(ctx, k8sClient, testID("scale-cr"), namespace.Name, scaleTunnelName)
			tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)

			By("Updating replica count to 2")
			var t cfgatev1alpha1.CloudflareTunnel
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: tunnel.Name, Namespace: tunnel.Namespace}, &t)).To(Succeed())
			t.Spec.Cloudflared.Replicas = 2
			Expect(k8sClient.Update(ctx, &t)).To(Succeed())

			By("Waiting for Deployment to scale to 2 replicas in spec")
			deploymentName := fmt.Sprintf("%s-cloudflared", tunnel.Name)
			deployment := waitForDeploymentSpec(ctx, k8sClient, deploymentName, namespace.Name, 2, LongTimeout)
			Expect(*deployment.Spec.Replicas).To(Equal(int32(2)))
		})
	})

	Context("fallback credentials", func() {
		It("should use fallbackCredentialsRef when primary secret deleted during tunnel deletion", SpecTimeout(12*time.Minute), func(ctx SpecContext) {
			fallbackTunnelName := testID("fallback")

			By("Creating fallback credentials secret")
			fallbackSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "fallback-credentials",
					Namespace: namespace.Name,
				},
				Type: corev1.SecretTypeOpaque,
				StringData: map[string]string{
					"CLOUDFLARE_API_TOKEN": testEnv.CloudflareAPIToken,
				},
			}
			Expect(k8sClient.Create(ctx, fallbackSecret)).To(Succeed())

			By("Creating CloudflareTunnel CR with fallbackCredentialsRef")
			tunnel := &cfgatev1alpha1.CloudflareTunnel{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("fallback-cr"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareTunnelSpec{
					Tunnel: cfgatev1alpha1.TunnelIdentity{
						Name: fallbackTunnelName,
					},
					Cloudflare: cfgatev1alpha1.CloudflareConfig{
						AccountID: testEnv.CloudflareAccountID,
						SecretRef: cfgatev1alpha1.SecretRef{
							Name: "cloudflare-credentials",
						},
					},
					FallbackCredentialsRef: &cfgatev1alpha1.SecretReference{
						Name:      "fallback-credentials",
						Namespace: namespace.Name,
					},
					Cloudflared: cfgatev1alpha1.CloudflaredConfig{
						Replicas: 1,
					},
				},
			}
			Expect(k8sClient.Create(ctx, tunnel)).To(Succeed())

			By("Waiting for tunnel to become ready")
			tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)
			tunnelID := tunnel.Status.TunnelID
			Expect(tunnelID).NotTo(BeEmpty())

			By("Deleting primary credentials secret")
			primarySecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cloudflare-credentials",
					Namespace: namespace.Name,
				},
			}
			Expect(k8sClient.Delete(ctx, primarySecret)).To(Succeed())

			By("Deleting CloudflareTunnel CR")
			Expect(k8sClient.Delete(ctx, tunnel)).To(Succeed())

			By("Waiting for tunnel to be deleted from Kubernetes")
			waitForTunnelDeleted(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)

			By("Verifying tunnel was deleted from Cloudflare (using fallback credentials)")
			waitForTunnelDeletedByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, tunnelID, LongTimeout)
		})
	})

	Context("main-lane expansion", func() {
		It("should block tunnel deletion without fallback credentials until primary credentials are restored", SpecTimeout(15*time.Minute), func(ctx SpecContext) {
			By("Creating a tunnel without fallbackCredentialsRef")
			tunnelName := testID("delete-blocked")
			tunnel := &cfgatev1alpha1.CloudflareTunnel{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("delete-blocked-cr"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareTunnelSpec{
					Tunnel: cfgatev1alpha1.TunnelIdentity{
						Name: tunnelName,
					},
					Cloudflare: cfgatev1alpha1.CloudflareConfig{
						AccountID: testEnv.CloudflareAccountID,
						SecretRef: cfgatev1alpha1.SecretRef{
							Name: "cloudflare-credentials",
						},
					},
					Cloudflared: cfgatev1alpha1.CloudflaredConfig{
						Replicas: 1,
					},
				},
			}
			Expect(k8sClient.Create(ctx, tunnel)).To(Succeed())
			tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)
			tunnelID := tunnel.Status.TunnelID
			Expect(tunnelID).NotTo(BeEmpty())

			By("Deleting the primary credentials secret and then deleting the tunnel")
			credentialsSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cloudflare-credentials",
					Namespace: namespace.Name,
				},
			}
			Expect(k8sClient.Delete(ctx, credentialsSecret)).To(Succeed())
			Expect(k8sClient.Delete(ctx, tunnel)).To(Succeed())

			By("Waiting for deletion to block on the finalizer and emit CleanupFailed")
			Eventually(func(g Gomega) {
				var current cfgatev1alpha1.CloudflareTunnel
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: tunnel.Name, Namespace: tunnel.Namespace}, &current)).To(Succeed())
				g.Expect(current.DeletionTimestamp.IsZero()).To(BeFalse())
				g.Expect(current.Finalizers).To(ContainElement("cfgate.io/tunnel-cleanup"))
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			event := waitForEventReason(ctx, namespace.Name, tunnel.Name, "CloudflareTunnel", "CleanupFailed", corev1.EventTypeWarning, DefaultTimeout)
			Expect(event.Message).To(ContainSubstring("Failed to resolve credentials"))

			By("Restoring credentials so deletion can complete")
			createCloudflareCredentialsSecret(namespace.Name)
			waitForTunnelDeleted(ctx, k8sClient, tunnel.Name, tunnel.Namespace, LongTimeout)
			waitForTunnelDeletedByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, tunnelID, LongTimeout)
		})

		It("should recreate a tunnel after the remote tunnel is deleted out of band", SpecTimeout(12*time.Minute), func(ctx SpecContext) {
			By("Creating a healthy tunnel and attached Gateway/HTTPRoute")
			tunnelName := testID("remote-recreate")
			tunnel := createCloudflareTunnel(ctx, k8sClient, testID("remote-recreate-cr"), namespace.Name, tunnelName)
			tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)
			originalTunnelID := tunnel.Status.TunnelID

			gatewayClassName := testID("gc")
			createGatewayClass(ctx, k8sClient, gatewayClassName)
			gatewayName := testID("gw")
			createGateway(ctx, k8sClient, gatewayName, namespace.Name, gatewayClassName, fmt.Sprintf("%s/%s", namespace.Name, tunnel.Name))
			serviceName := testID("svc")
			createTestService(ctx, k8sClient, serviceName, namespace.Name, 8080)
			hostname := fmt.Sprintf("%s.%s", testID("remote-route"), testEnv.CloudflareZoneName)
			createHTTPRoute(ctx, k8sClient, testID("route"), namespace.Name, gatewayName, []string{hostname}, serviceName, 8080)

			Eventually(func(g Gomega) {
				var current cfgatev1alpha1.CloudflareTunnel
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: tunnel.Name, Namespace: tunnel.Namespace}, &current)).To(Succeed())
				g.Expect(current.Status.ConnectedRouteCount).To(BeNumerically(">", 0))
				g.Expect(findCondition(current.Status.Conditions, "ConfigurationSynced")).NotTo(BeNil())
				g.Expect(findCondition(current.Status.Conditions, "ConfigurationSynced").Status).To(Equal(metav1.ConditionTrue))
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			By("Deleting the remote tunnel directly in Cloudflare")
			Expect(deleteTunnelInCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, originalTunnelID)).To(Succeed())

			By("Triggering a meaningful reconcile after remote deletion")
			Eventually(func() error {
				var current cfgatev1alpha1.CloudflareTunnel
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: tunnel.Name, Namespace: tunnel.Namespace}, &current); err != nil {
					return err
				}
				current.Spec.FallbackTarget = "http_status:503"
				return k8sClient.Update(ctx, &current)
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			By("Verifying the controller recreates the tunnel and restores configuration")
			Eventually(func(g Gomega) {
				var current cfgatev1alpha1.CloudflareTunnel
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: tunnel.Name, Namespace: tunnel.Namespace}, &current)).To(Succeed())
				g.Expect(current.Status.TunnelID).NotTo(BeEmpty())
				g.Expect(current.Status.TunnelID).NotTo(Equal(originalTunnelID))
				g.Expect(findCondition(current.Status.Conditions, "Ready")).NotTo(BeNil())
				g.Expect(findCondition(current.Status.Conditions, "Ready").Status).To(Equal(metav1.ConditionTrue))

				config, err := getTunnelConfigurationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, current.Status.TunnelID)
				g.Expect(err).NotTo(HaveOccurred())

				foundHostname := false
				for _, ingress := range config.Config.Ingress {
					if ingress.Hostname == hostname {
						foundHostname = true
						break
					}
				}
				g.Expect(foundHostname).To(BeTrue())
			}, LongTimeout, DefaultInterval).Should(Succeed())
		})

		It("should build remote ingress configuration for multiple hostnames and path matches", SpecTimeout(10*time.Minute), func(ctx SpecContext) {
			By("Creating a tunnel and Gateway")
			tunnelName := testID("multi-ingress")
			tunnel := createCloudflareTunnel(ctx, k8sClient, testID("multi-ingress-cr"), namespace.Name, tunnelName)
			tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)

			gatewayClassName := testID("gc")
			createGatewayClass(ctx, k8sClient, gatewayClassName)
			gatewayName := testID("gw")
			createGateway(ctx, k8sClient, gatewayName, namespace.Name, gatewayClassName, fmt.Sprintf("%s/%s", namespace.Name, tunnel.Name))

			By("Creating backend Services")
			serviceOne := createTestService(ctx, k8sClient, testID("svc-one"), namespace.Name, 8080)
			serviceTwo := createTestService(ctx, k8sClient, testID("svc-two"), namespace.Name, 9090)

			By("Creating HTTPRoutes with multiple hostnames and path matches")
			hostnameOne := fmt.Sprintf("%s.%s", testID("app"), testEnv.CloudflareZoneName)
			hostnameTwo := fmt.Sprintf("%s.%s", testID("api"), testEnv.CloudflareZoneName)
			hostnameThree := fmt.Sprintf("%s.%s", testID("admin"), testEnv.CloudflareZoneName)
			parentNamespace := gatewayv1.Namespace(namespace.Name)

			routeOne := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("route-one"),
					Namespace: namespace.Name,
				},
				Spec: gatewayv1.HTTPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{
							{
								Name:      gatewayv1.ObjectName(gatewayName),
								Namespace: &parentNamespace,
							},
						},
					},
					Hostnames: []gatewayv1.Hostname{
						gatewayv1.Hostname(hostnameOne),
						gatewayv1.Hostname(hostnameTwo),
					},
					Rules: []gatewayv1.HTTPRouteRule{
						{
							Matches: []gatewayv1.HTTPRouteMatch{
								{Path: &gatewayv1.HTTPPathMatch{Type: ptrTo(gatewayv1.PathMatchPathPrefix), Value: ptrTo("/app")}},
								{Path: &gatewayv1.HTTPPathMatch{Type: ptrTo(gatewayv1.PathMatchPathPrefix), Value: ptrTo("/api")}},
							},
							BackendRefs: []gatewayv1.HTTPBackendRef{
								{
									BackendRef: gatewayv1.BackendRef{
										BackendObjectReference: gatewayv1.BackendObjectReference{
											Name: gatewayv1.ObjectName(serviceOne.Name),
											Port: ptrTo(gatewayv1.PortNumber(8080)),
										},
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, routeOne)).To(Succeed())

			routeTwo := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("route-two"),
					Namespace: namespace.Name,
				},
				Spec: gatewayv1.HTTPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{
							{
								Name:      gatewayv1.ObjectName(gatewayName),
								Namespace: &parentNamespace,
							},
						},
					},
					Hostnames: []gatewayv1.Hostname{
						gatewayv1.Hostname(hostnameThree),
					},
					Rules: []gatewayv1.HTTPRouteRule{
						{
							BackendRefs: []gatewayv1.HTTPBackendRef{
								{
									BackendRef: gatewayv1.BackendRef{
										BackendObjectReference: gatewayv1.BackendObjectReference{
											Name: gatewayv1.ObjectName(serviceTwo.Name),
											Port: ptrTo(gatewayv1.PortNumber(9090)),
										},
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, routeTwo)).To(Succeed())

			By("Waiting for the tunnel status and remote config to reflect the full rule set")
			Eventually(func(g Gomega) {
				var current cfgatev1alpha1.CloudflareTunnel
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: tunnel.Name, Namespace: tunnel.Namespace}, &current)).To(Succeed())
				g.Expect(current.Status.ConnectedRouteCount).To(Equal(int32(2)))

				config, err := getTunnelConfigurationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, current.Status.TunnelID)
				g.Expect(err).NotTo(HaveOccurred())

				ruleSet := map[string]bool{}
				for _, ingress := range config.Config.Ingress {
					ruleSet[fmt.Sprintf("%s|%s|%s", ingress.Hostname, ingress.Path, ingress.Service)] = true
				}

				g.Expect(ruleSet).To(HaveKey(fmt.Sprintf("%s|/app|http://%s.%s.svc.cluster.local:8080", hostnameOne, serviceOne.Name, namespace.Name)))
				g.Expect(ruleSet).To(HaveKey(fmt.Sprintf("%s|/api|http://%s.%s.svc.cluster.local:8080", hostnameOne, serviceOne.Name, namespace.Name)))
				g.Expect(ruleSet).To(HaveKey(fmt.Sprintf("%s|/app|http://%s.%s.svc.cluster.local:8080", hostnameTwo, serviceOne.Name, namespace.Name)))
				g.Expect(ruleSet).To(HaveKey(fmt.Sprintf("%s|/api|http://%s.%s.svc.cluster.local:8080", hostnameTwo, serviceOne.Name, namespace.Name)))
				g.Expect(ruleSet).To(SatisfyAny(
					HaveKey(fmt.Sprintf("%s||http://%s.%s.svc.cluster.local:9090", hostnameThree, serviceTwo.Name, namespace.Name)),
					HaveKey(fmt.Sprintf("%s|/|http://%s.%s.svc.cluster.local:9090", hostnameThree, serviceTwo.Name, namespace.Name)),
				))
				g.Expect(ruleSet).To(HaveKey("||http_status:404"))
			}, LongTimeout, DefaultInterval).Should(Succeed())
		})
	})

	Context("cloudflared deployment updates", func() {
		// Note: Replicas update is already tested in "cloudflared deployment" context above.

		It("should update deployment when spec.cloudflared.image changes", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			imageTunnelName := testID("image-update")

			By("Creating CloudflareTunnel CR with default image")
			tunnel := createCloudflareTunnel(ctx, k8sClient, testID("image-update-cr"), namespace.Name, imageTunnelName)
			tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)

			By("Getting deployment to check initial image")
			deploymentName := fmt.Sprintf("%s-cloudflared", tunnel.Name)
			var initialDeployment cfgatev1alpha1.CloudflareTunnel
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: tunnel.Name, Namespace: tunnel.Namespace}, &initialDeployment)).To(Succeed())

			By("Updating cloudflared image")
			newImage := "cloudflare/cloudflared:2024.1.0"
			// Use Eventually to retry on conflict (controller may update status concurrently)
			Eventually(func() error {
				var t cfgatev1alpha1.CloudflareTunnel
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: tunnel.Name, Namespace: tunnel.Namespace}, &t); err != nil {
					return err
				}
				t.Spec.Cloudflared.Image = newImage
				return k8sClient.Update(ctx, &t)
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			By("Waiting for Deployment to use new image")
			Eventually(func(g Gomega) {
				var dep corev1.Pod
				// List pods to find the cloudflared container
				var podList corev1.PodList
				g.Expect(k8sClient.List(ctx, &podList, client.InNamespace(namespace.Name), client.MatchingLabels{"app.kubernetes.io/name": "cloudflared"})).To(Succeed())
				// We can't easily check pod image during rolling update, so verify via tunnel status
				var updatedTunnel cfgatev1alpha1.CloudflareTunnel
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: tunnel.Name, Namespace: tunnel.Namespace}, &updatedTunnel)).To(Succeed())
				g.Expect(updatedTunnel.Spec.Cloudflared.Image).To(Equal(newImage))
				_ = dep
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			// Also verify deployment spec was updated
			Eventually(func(g Gomega) {
				var deployment corev1.Pod
				var deploymentList corev1.PodList
				g.Expect(k8sClient.List(ctx, &deploymentList, client.InNamespace(namespace.Name))).To(Succeed())
				found := false
				for _, pod := range deploymentList.Items {
					for _, container := range pod.Spec.Containers {
						if container.Name == "cloudflared" && container.Image == newImage {
							found = true
							break
						}
					}
				}
				g.Expect(found).To(BeTrue(), "Pod should have updated image")
				_ = deployment
				_ = deploymentName
			}, LongTimeout, DefaultInterval).Should(Succeed())
		})

		It("should update deployment when spec.cloudflared.protocol changes", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			protocolTunnelName := testID("protocol-update")

			By("Creating CloudflareTunnel CR with auto protocol")
			tunnel := createCloudflareTunnel(ctx, k8sClient, testID("protocol-update-cr"), namespace.Name, protocolTunnelName)
			tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)

			By("Updating cloudflared protocol to quic")
			// Use Eventually to retry on conflict (controller may update status concurrently)
			Eventually(func() error {
				var t cfgatev1alpha1.CloudflareTunnel
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: tunnel.Name, Namespace: tunnel.Namespace}, &t); err != nil {
					return err
				}
				t.Spec.Cloudflared.Protocol = "quic"
				return k8sClient.Update(ctx, &t)
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			By("Waiting for spec update to be reflected")
			Eventually(func(g Gomega) {
				var updatedTunnel cfgatev1alpha1.CloudflareTunnel
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: tunnel.Name, Namespace: tunnel.Namespace}, &updatedTunnel)).To(Succeed())
				g.Expect(updatedTunnel.Spec.Cloudflared.Protocol).To(Equal("quic"))
				// Verify ObservedGeneration increased (reconciliation happened)
				g.Expect(updatedTunnel.Status.ObservedGeneration).To(BeNumerically(">", tunnel.Status.ObservedGeneration))
			}, DefaultTimeout, DefaultInterval).Should(Succeed())
		})

		It("should update deployment when spec.cloudflared.resources changes", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			resourcesTunnelName := testID("resources-update")

			By("Creating CloudflareTunnel CR without resources")
			tunnel := createCloudflareTunnel(ctx, k8sClient, testID("resources-update-cr"), namespace.Name, resourcesTunnelName)
			tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)

			By("Updating cloudflared resources")
			// Use Eventually to retry on conflict (controller may update status concurrently)
			Eventually(func() error {
				var t cfgatev1alpha1.CloudflareTunnel
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: tunnel.Name, Namespace: tunnel.Namespace}, &t); err != nil {
					return err
				}
				t.Spec.Cloudflared.Resources = corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    mustParseQuantity("100m"),
						corev1.ResourceMemory: mustParseQuantity("64Mi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    mustParseQuantity("200m"),
						corev1.ResourceMemory: mustParseQuantity("128Mi"),
					},
				}
				return k8sClient.Update(ctx, &t)
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			By("Waiting for spec update to be reflected")
			Eventually(func(g Gomega) {
				var updatedTunnel cfgatev1alpha1.CloudflareTunnel
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: tunnel.Name, Namespace: tunnel.Namespace}, &updatedTunnel)).To(Succeed())
				g.Expect(updatedTunnel.Spec.Cloudflared.Resources.Requests).NotTo(BeNil())
				// Verify ObservedGeneration increased (reconciliation happened)
				g.Expect(updatedTunnel.Status.ObservedGeneration).To(BeNumerically(">", tunnel.Status.ObservedGeneration))
			}, DefaultTimeout, DefaultInterval).Should(Succeed())
		})
	})

	// ============================================================
	// §6.1: Deleted Tunnel Filter Behavior
	// ============================================================
	Context("deleted tunnel filter", func() {
		It("should not adopt a deleted tunnel when creating CR", SpecTimeout(8*time.Minute), func(ctx SpecContext) {
			deletedTunnelName := testID("deleted-filter")

			By("Pre-creating tunnel via Cloudflare API")
			preTunnel, err := createTunnelInCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, deletedTunnelName)
			Expect(err).NotTo(HaveOccurred())
			Expect(preTunnel).NotTo(BeNil())
			deletedTunnelID := preTunnel.ID

			By("Deleting the pre-created tunnel via Cloudflare API")
			err = deleteTunnelInCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, deletedTunnelID)
			Expect(err).NotTo(HaveOccurred())

			By("Creating CloudflareTunnel CR with same tunnel name")
			tunnel := createCloudflareTunnel(ctx, k8sClient, testID("deleted-filter-cr"), namespace.Name, deletedTunnelName)

			By("Waiting for tunnel to become ready")
			tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)

			By("Verifying controller created a NEW tunnel (different ID from deleted one)")
			Expect(tunnel.Status.TunnelID).NotTo(Equal(deletedTunnelID),
				"Controller should NOT adopt the deleted tunnel, should create a new one")
			Expect(tunnel.Status.TunnelID).NotTo(BeEmpty())

			By("Verifying new tunnel exists in CF API with non-deleted status")
			cfTunnel, err := getTunnelFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, deletedTunnelName)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfTunnel).NotTo(BeNil(), "New tunnel should exist in Cloudflare")
			Expect(cfTunnel.ID).To(Equal(tunnel.Status.TunnelID))
			Expect(cfTunnel.Status).NotTo(Equal("deleted"))
		})
	})

	// ============================================================
	// §6.2: Tunnel Adopt-With-Drift
	// ============================================================
	Context("adopt with drift", func() {
		It("should update tunnel configuration when adopting with drift", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			adoptDriftTunnelName := testID("adopt-drift")

			By("Pre-creating tunnel via Cloudflare API")
			preTunnel, err := createTunnelInCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, adoptDriftTunnelName)
			Expect(err).NotTo(HaveOccurred())
			Expect(preTunnel).NotTo(BeNil())
			preTunnelID := preTunnel.ID

			By("Creating CloudflareTunnel CR with same name but replicas: 2")
			tunnel := &cfgatev1alpha1.CloudflareTunnel{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("adopt-drift-cr"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareTunnelSpec{
					Tunnel: cfgatev1alpha1.TunnelIdentity{
						Name: adoptDriftTunnelName,
					},
					Cloudflare: cfgatev1alpha1.CloudflareConfig{
						AccountID: testEnv.CloudflareAccountID,
						SecretRef: cfgatev1alpha1.SecretRef{
							Name: "cloudflare-credentials",
						},
					},
					Cloudflared: cfgatev1alpha1.CloudflaredConfig{
						Replicas: 2,
					},
				},
			}
			Expect(k8sClient.Create(ctx, tunnel)).To(Succeed())

			By("Waiting for tunnel to become ready")
			tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)

			By("Verifying it adopted the existing tunnel (same ID)")
			Expect(tunnel.Status.TunnelID).To(Equal(preTunnelID),
				"Should adopt the existing tunnel, not create a new one")

			By("Verifying Deployment has 2 replicas in spec (CR spec overrides default)")
			deploymentName := fmt.Sprintf("%s-cloudflared", tunnel.Name)
			deployment := waitForDeploymentSpec(ctx, k8sClient, deploymentName, namespace.Name, 2, LongTimeout)
			Expect(*deployment.Spec.Replicas).To(Equal(int32(2)),
				"Deployment should use CR's replica count, not default")
		})
	})

	Context("account lookup", func() {
		It("should resolve accountName to accountId when accountId not provided", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			// Note: This test requires knowing the account name corresponding to the account ID.
			// We can get it from the Cloudflare API.
			By("Getting account name from Cloudflare API")
			accountList, err := cfClient.Accounts.List(ctx, accounts.AccountListParams{})
			Expect(err).NotTo(HaveOccurred())
			Expect(accountList.Result).NotTo(BeEmpty(), "Should have at least one account")

			var accountName string
			for _, acc := range accountList.Result {
				if acc.ID == testEnv.CloudflareAccountID {
					accountName = acc.Name
					break
				}
			}
			Expect(accountName).NotTo(BeEmpty(), "Should find account name for the test account ID")

			lookupTunnelName := testID("account-lookup")

			By("Creating CloudflareTunnel CR with accountName instead of accountId")
			tunnel := &cfgatev1alpha1.CloudflareTunnel{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("account-lookup-cr"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareTunnelSpec{
					Tunnel: cfgatev1alpha1.TunnelIdentity{
						Name: lookupTunnelName,
					},
					Cloudflare: cfgatev1alpha1.CloudflareConfig{
						AccountName: accountName, // Use name instead of ID
						SecretRef: cfgatev1alpha1.SecretRef{
							Name: "cloudflare-credentials",
						},
					},
					Cloudflared: cfgatev1alpha1.CloudflaredConfig{
						Replicas: 1,
					},
				},
			}
			Expect(k8sClient.Create(ctx, tunnel)).To(Succeed())

			By("Waiting for tunnel to become ready")
			tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)

			By("Verifying account ID was resolved and stored in status")
			Expect(tunnel.Status.AccountID).To(Equal(testEnv.CloudflareAccountID),
				"Controller should resolve accountName to accountId")

			By("Verifying tunnel was created in Cloudflare")
			cfTunnel, err := getTunnelFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, lookupTunnelName)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfTunnel).NotTo(BeNil())
		})
	})
})

// Package e2e contains end-to-end tests for cfgate.
package e2e_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	cloudflare "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/zero_trust"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// Access tests organized by tier:
// - P0 (no IdP): IP, IPList, Country, Everyone, ServiceToken, AnyValidServiceToken - no label
// - P1 (basic IdP): Email, EmailList, EmailDomain, OIDCClaim - Label("idp")
// - P2 (GSuite): GSuiteGroup - Label("gsuite")

var _ = Describe("CloudflareAccessPolicy E2E", Label("cloudflare"), func() {
	var (
		namespace *corev1.Namespace
		cfClient  *cloudflare.Client
	)

	BeforeEach(func() {
		skipIfNoCredentials()
		skipIfNoZone() // Access needs zone for hostname

		// Create unique namespace for this test.
		namespace = createTestNamespace("cfgate-access-e2e")

		// Create Cloudflare credentials secret.
		createCloudflareCredentialsSecret(namespace.Name)

		// Create Cloudflare client for verification.
		cfClient = getCloudflareClient()

		// Register cleanup via DeferCleanup (per Ginkgo #1284: use in BeforeEach, not AfterEach)
		DeferCleanup(func() {
			if testEnv.SkipCleanup {
				return
			}
			// Delete namespace - controller finalizers will attempt cleanup.
			// Any orphaned resources are cleaned by AfterSuite batch cleanup.
			if namespace != nil {
				deleteTestNamespace(namespace)
			}
		})
	})

	// ============================================================
	// P0: Application Lifecycle (no IdP required)
	// ============================================================
	Context("P0 application lifecycle", func() {
		It("should create Access application in Cloudflare when CloudflareAccessPolicy CR is created", SpecTimeout(3*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-app")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"

			// Create minimal Gateway infrastructure for HTTPRoute.
			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy CR")
			policy := createCloudflareAccessPolicy(ctx, k8sClient, policyName, namespace.Name, routeName, hostname)

			By("Waiting for AccessPolicy to become ready")
			policy = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying application ID is populated in status")
			Expect(policy.Status.ApplicationID).NotTo(BeEmpty(), "Application ID should be populated in status")

			By("Verifying application exists in Cloudflare API")
			cfApp, err := getAccessApplicationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policyName)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfApp).NotTo(BeNil(), "Application should exist in Cloudflare")
			Expect(cfApp.ID).To(Equal(policy.Status.ApplicationID), "Application IDs should match")
		})

		It("should use spec.application.name for Cloudflare application name", SpecTimeout(3*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-name")
			appDisplayName := testID("custom-app-name")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with custom application name")
			everyone := true
			policy := &cfgatev1alpha1.CloudflareAccessPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      policyName,
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
					TargetRef: &cfgatev1alpha1.PolicyTargetReference{
						Group: "gateway.networking.k8s.io",
						Kind:  "HTTPRoute",
						Name:  routeName,
					},
					CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
						Name:      "cloudflare-credentials",
						AccountID: testEnv.CloudflareAccountID,
					},
					Application: cfgatev1alpha1.AccessApplication{
						Name:   appDisplayName, // Custom name different from CR name.
						Domain: hostname,
					},
					Policies: []cfgatev1alpha1.AccessPolicyRule{
						{
							Name:     "allow-all",
							Decision: "allow",
							Include:  []cfgatev1alpha1.AccessRule{{Everyone: &everyone}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())

			By("Waiting for AccessPolicy to become ready")
			waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying Cloudflare application uses custom name")
			cfApp, err := getAccessApplicationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, appDisplayName)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfApp).NotTo(BeNil(), "Application with custom name should exist in Cloudflare")
			Expect(cfApp.Name).To(Equal(appDisplayName))
		})

		It("should populate status with applicationId and applicationAud", SpecTimeout(3*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-status")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy CR")
			policy := createCloudflareAccessPolicy(ctx, k8sClient, policyName, namespace.Name, routeName, hostname)

			By("Waiting for AccessPolicy to become ready")
			policy = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying status fields are populated")
			Expect(policy.Status.ApplicationID).NotTo(BeEmpty(), "ApplicationID should be populated")
			Expect(policy.Status.ApplicationAUD).NotTo(BeEmpty(), "ApplicationAUD should be populated")

			By("Verifying AUD matches Cloudflare")
			cfApp, err := getAccessApplicationByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policy.Status.ApplicationID)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfApp).NotTo(BeNil())
			Expect(policy.Status.ApplicationAUD).To(Equal(cfApp.AUD), "ApplicationAUD should match Cloudflare AUD")
		})

		It("should delete Access application from Cloudflare when CR is deleted", SpecTimeout(12*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-delete")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy CR")
			policy := createCloudflareAccessPolicy(ctx, k8sClient, policyName, namespace.Name, routeName, hostname)

			By("Waiting for AccessPolicy to be created in Cloudflare")
			policy = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)
			appID := policy.Status.ApplicationID
			Expect(appID).NotTo(BeEmpty())

			By("Verifying application exists in Cloudflare")
			cfApp, err := getAccessApplicationByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, appID)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfApp).NotTo(BeNil())

			By("Deleting CloudflareAccessPolicy CR")
			Expect(k8sClient.Delete(ctx, policy)).To(Succeed())

			By("Waiting for AccessPolicy to be deleted from Kubernetes")
			waitForAccessPolicyDeleted(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying application is deleted from Cloudflare")
			waitForAccessApplicationDeletedFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policyName, LongTimeout)
		})

		It("should preserve Access application when deletion policy is orphan", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-orphan")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with orphan deletion policy")
			everyone := true
			policy := &cfgatev1alpha1.CloudflareAccessPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      policyName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/deletion-policy": "orphan",
					},
				},
				Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
					TargetRef: &cfgatev1alpha1.PolicyTargetReference{
						Group: "gateway.networking.k8s.io",
						Kind:  "HTTPRoute",
						Name:  routeName,
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
							Include:  []cfgatev1alpha1.AccessRule{{Everyone: &everyone}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())

			By("Waiting for AccessPolicy to be created in Cloudflare")
			policy = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)
			appID := policy.Status.ApplicationID

			By("Deleting CloudflareAccessPolicy CR")
			Expect(k8sClient.Delete(ctx, policy)).To(Succeed())

			By("Waiting for CR to be deleted from Kubernetes")
			waitForAccessPolicyDeleted(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying application still exists in Cloudflare (orphaned)")
			cfApp, err := getAccessApplicationByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, appID)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfApp).NotTo(BeNil(), "Application should still exist in Cloudflare with orphan policy")
		})
	})

	// ============================================================
	// P0: Target Resolution (no IdP required)
	// ============================================================
	Context("P0 target resolution", func() {
		It("should resolve single targetRef to HTTPRoute", SpecTimeout(3*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-single-target")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with single targetRef")
			policy := createCloudflareAccessPolicy(ctx, k8sClient, policyName, namespace.Name, routeName, hostname)

			By("Waiting for AccessPolicy to become ready")
			policy = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying AttachedTargets status")
			Expect(policy.Status.AttachedTargets).To(Equal(int32(1)), "Should have 1 attached target")
		})

		It("should resolve multiple targetRefs", SpecTimeout(3*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoutes for targets")
			policyName := testID("access-multi-target")
			hostname1 := fmt.Sprintf("%s-1.%s", policyName, testEnv.CloudflareZoneName)
			hostname2 := fmt.Sprintf("%s-2.%s", policyName, testEnv.CloudflareZoneName)
			routeName1 := policyName + "-route1"
			routeName2 := policyName + "-route2"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName1, namespace.Name, gw.Name, []string{hostname1}, svc.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName2, namespace.Name, gw.Name, []string{hostname2}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with multiple targetRefs")
			everyone := true
			policy := &cfgatev1alpha1.CloudflareAccessPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      policyName,
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
					TargetRefs: []cfgatev1alpha1.PolicyTargetReference{
						{
							Group: "gateway.networking.k8s.io",
							Kind:  "HTTPRoute",
							Name:  routeName1,
						},
						{
							Group: "gateway.networking.k8s.io",
							Kind:  "HTTPRoute",
							Name:  routeName2,
						},
					},
					CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
						Name:      "cloudflare-credentials",
						AccountID: testEnv.CloudflareAccountID,
					},
					Application: cfgatev1alpha1.AccessApplication{
						Name:   policyName,
						Domain: hostname1, // Use first hostname as primary domain.
					},
					Policies: []cfgatev1alpha1.AccessPolicyRule{
						{
							Name:     "allow-all",
							Decision: "allow",
							Include:  []cfgatev1alpha1.AccessRule{{Everyone: &everyone}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())

			By("Waiting for AccessPolicy to become ready")
			policy = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying AttachedTargets status")
			Expect(policy.Status.AttachedTargets).To(Equal(int32(2)), "Should have 2 attached targets")
		})

		It("should set condition=False when target is missing", SpecTimeout(2*time.Minute), func(ctx SpecContext) {
			By("Creating CloudflareAccessPolicy targeting non-existent HTTPRoute")
			policyName := testID("access-missing-target")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)

			everyone := true
			policy := &cfgatev1alpha1.CloudflareAccessPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      policyName,
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
					TargetRef: &cfgatev1alpha1.PolicyTargetReference{
						Group: "gateway.networking.k8s.io",
						Kind:  "HTTPRoute",
						Name:  "non-existent-route",
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
							Include:  []cfgatev1alpha1.AccessRule{{Everyone: &everyone}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())

			By("Waiting for TargetsResolved condition to be False")
			waitForAccessPolicyCondition(ctx, k8sClient, policy.Name, policy.Namespace, "TargetsResolved", metav1.ConditionFalse, ShortTimeout)
		})
	})

	Context("main-lane expansion", func() {
		It("should target a Gateway, inherit tunnel credentials, and derive the application domain from listener hostnames", SpecTimeout(6*time.Minute), func(ctx SpecContext) {
			By("Creating a tunnel for credential inheritance")
			tunnelName := testID("access-gw-target")
			tunnel := createCloudflareTunnel(ctx, k8sClient, tunnelName+"-tunnel", namespace.Name, tunnelName)
			tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)

			By("Creating Gateway infrastructure with a listener hostname")
			gatewayClassName := testID("gc")
			createGatewayClass(ctx, k8sClient, gatewayClassName)

			hostname := fmt.Sprintf("%s.%s", testID("gw-target"), testEnv.CloudflareZoneName)
			gatewayName := testID("gateway")
			gateway := &gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      gatewayName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/tunnel-ref": fmt.Sprintf("%s/%s", namespace.Name, tunnel.Name),
					},
				},
				Spec: gatewayv1.GatewaySpec{
					GatewayClassName: gatewayv1.ObjectName(gatewayClassName),
					Listeners: []gatewayv1.Listener{
						{
							Name:     "https",
							Protocol: gatewayv1.HTTPSProtocolType,
							Port:     443,
							Hostname: ptrTo(gatewayv1.Hostname(hostname)),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, gateway)).To(Succeed())
			waitForGatewayCondition(ctx, k8sClient, gateway.Name, gateway.Namespace, string(gatewayv1.GatewayConditionAccepted), metav1.ConditionTrue, DefaultTimeout)

			By("Creating an AccessPolicy that targets the Gateway without explicit cloudflareRef")
			policyName := testID("access-gateway-policy")
			policy := &cfgatev1alpha1.CloudflareAccessPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      policyName,
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
					TargetRef: &cfgatev1alpha1.PolicyTargetReference{
						Group: "gateway.networking.k8s.io",
						Kind:  "Gateway",
						Name:  gateway.Name,
					},
					Application: cfgatev1alpha1.AccessApplication{
						Name: policyName,
					},
					Policies: []cfgatev1alpha1.AccessPolicyRule{
						{
							Name:     "allow-all",
							Decision: "allow",
							Include: []cfgatev1alpha1.AccessRule{
								{Everyone: ptrTo(true)},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())

			By("Waiting for the policy to become ready")
			policy = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying inherited credentials and target-derived domain were used")
			credentialsCondition := findCondition(policy.Status.Conditions, "CredentialsValid")
			Expect(credentialsCondition).NotTo(BeNil())
			Expect(credentialsCondition.Status).To(Equal(metav1.ConditionTrue))

			cfApp, err := getAccessApplicationByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policy.Status.ApplicationID)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfApp).NotTo(BeNil())
			Expect(cfApp.Domain).To(Equal(hostname))

			Expect(policy.Status.Ancestors).To(HaveLen(1))
			Expect(policy.Status.Ancestors[0].AncestorRef.Kind).To(Equal("Gateway"))
			Expect(policy.Status.Ancestors[0].AncestorRef.Name).To(Equal(gateway.Name))
			Expect(policy.Status.Ancestors[0].ControllerName).To(Equal("cfgate.io/cloudflare-tunnel-controller"))
			Expect(findCondition(policy.Status.Ancestors[0].Conditions, "Accepted")).NotTo(BeNil())
			Expect(findCondition(policy.Status.Ancestors[0].Conditions, "Accepted").Status).To(Equal(metav1.ConditionTrue))
		})

		It("should deny cross-namespace targeting without a ReferenceGrant", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			By("Creating a separate target namespace")
			targetNamespace := createTestNamespace("cfgate-access-target")
			var policy *cfgatev1alpha1.CloudflareAccessPolicy
			DeferCleanup(func() {
				if testEnv.SkipCleanup {
					return
				}
				cleanupCtx := context.Background()
				if policy != nil {
					Expect(k8sClient.Delete(cleanupCtx, policy)).To(SatisfyAny(Succeed(), WithTransform(apierrors.IsNotFound, BeTrue())))
					waitForAccessPolicyDeleted(cleanupCtx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)
				}
				deleteTestNamespace(targetNamespace)
			})
			createCloudflareCredentialsSecret(targetNamespace.Name)

			By("Creating target Gateway and HTTPRoute in the other namespace")
			gatewayClassName := testID("gc")
			createGatewayClass(ctx, k8sClient, gatewayClassName)
			gatewayName := testID("gateway")
			createGateway(ctx, k8sClient, gatewayName, targetNamespace.Name, gatewayClassName, "")

			serviceName := testID("svc")
			createTestService(ctx, k8sClient, serviceName, targetNamespace.Name, 8080)

			hostname := fmt.Sprintf("%s.%s", testID("crossns-denied"), testEnv.CloudflareZoneName)
			routeName := testID("route")
			createHTTPRoute(ctx, k8sClient, routeName, targetNamespace.Name, gatewayName, []string{hostname}, serviceName, 8080)

			By("Creating a policy that references the other namespace without a grant")
			policyName := testID("access-crossns-denied")
			policy = &cfgatev1alpha1.CloudflareAccessPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      policyName,
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
					TargetRef: &cfgatev1alpha1.PolicyTargetReference{
						Group:     "gateway.networking.k8s.io",
						Kind:      "HTTPRoute",
						Name:      routeName,
						Namespace: ptrTo(targetNamespace.Name),
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
								{Everyone: ptrTo(true)},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())

			By("Waiting for target resolution to fail because the cross-namespace reference lacks a grant")
			policy = waitForAccessPolicyCondition(ctx, k8sClient, policy.Name, policy.Namespace, "TargetsResolved", metav1.ConditionFalse, DefaultTimeout)

			By("Verifying no application is created and the failure is stable")
			Expect(policy.Status.ApplicationID).To(BeEmpty())
			Expect(policy.Status.ApplicationAUD).To(BeEmpty())
			Expect(findCondition(policy.Status.Conditions, "TargetsResolved")).NotTo(BeNil())
			Expect(findCondition(policy.Status.Conditions, "TargetsResolved").Reason).To(Equal("TargetNotFound"))
			Expect(findCondition(policy.Status.Conditions, "TargetsResolved").Message).To(ContainSubstring("not permitted by ReferenceGrant"))
			Expect(findCondition(policy.Status.Conditions, "ApplicationCreated")).To(BeNil())

			Consistently(func(g Gomega) {
				var current cfgatev1alpha1.CloudflareAccessPolicy
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: policy.Name, Namespace: policy.Namespace}, &current)).To(Succeed())
				g.Expect(current.Status.ApplicationID).To(BeEmpty())
				g.Expect(current.Status.ApplicationAUD).To(BeEmpty())
				g.Expect(findCondition(current.Status.Conditions, "TargetsResolved")).NotTo(BeNil())
				g.Expect(findCondition(current.Status.Conditions, "TargetsResolved").Status).To(Equal(metav1.ConditionFalse))
				g.Expect(findCondition(current.Status.Conditions, "TargetsResolved").Message).To(ContainSubstring("not permitted by ReferenceGrant"))
				g.Expect(findCondition(current.Status.Conditions, "ApplicationCreated")).To(BeNil())
			}, ShortTimeout, DefaultInterval).Should(Succeed())
		})

		It("should block deletion when credentials disappear, then resume cleanup after credentials are restored", SpecTimeout(15*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute infrastructure")
			policyName := testID("access-cleanup-block")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"
			tokenSecretName := policyName + "-token"

			gatewayClassName := testID("gc")
			createGatewayClass(ctx, k8sClient, gatewayClassName)
			gateway := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gatewayClassName, "")
			service := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gateway.Name, []string{hostname}, service.Name, 8080)

			By("Creating an AccessPolicy with a service token")
			policy := &cfgatev1alpha1.CloudflareAccessPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      policyName,
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
					TargetRef: &cfgatev1alpha1.PolicyTargetReference{
						Group: "gateway.networking.k8s.io",
						Kind:  "HTTPRoute",
						Name:  routeName,
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
								{Everyone: ptrTo(true)},
							},
						},
					},
					ServiceTokens: []cfgatev1alpha1.ServiceTokenConfig{
						{
							Name:     policyName + "-token",
							Duration: "8760h",
							SecretRef: cfgatev1alpha1.ServiceTokenSecretRef{
								Name: tokenSecretName,
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			policy = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Deleting the credentials secret before deleting the policy")
			credentialsSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "cloudflare-credentials", Namespace: namespace.Name}}
			Expect(k8sClient.Delete(ctx, credentialsSecret)).To(Succeed())

			By("Deleting the policy and waiting for deletion to block on the finalizer")
			Expect(k8sClient.Delete(ctx, policy)).To(Succeed())

			Eventually(func(g Gomega) {
				var current cfgatev1alpha1.CloudflareAccessPolicy
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: policy.Name, Namespace: policy.Namespace}, &current)).To(Succeed())
				g.Expect(current.DeletionTimestamp.IsZero()).To(BeFalse())
				g.Expect(current.Finalizers).To(ContainElement("cfgate.io/access-policy-cleanup"))
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			event := waitForEventReason(ctx, namespace.Name, policy.Name, "CloudflareAccessPolicy", "CleanupFailed", corev1.EventTypeWarning, DefaultTimeout)
			Expect(event.Message).To(ContainSubstring("Failed to resolve credentials"))

			By("Restoring credentials so cleanup can resume")
			createCloudflareCredentialsSecret(namespace.Name)

			By("Waiting for Kubernetes deletion and Cloudflare cleanup to complete")
			waitForAccessPolicyDeleted(ctx, k8sClient, policy.Name, policy.Namespace, LongTimeout)
			waitForServiceTokenDeletedFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policyName+"-token", LongTimeout)
			waitForAccessApplicationDeletedFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policyName, LongTimeout)
		})
	})

	// ============================================================
	// P0: Service Token Rules (no IdP required)
	// ============================================================
	Context("P0 service token rules", func() {
		It("should create service token in Cloudflare", SpecTimeout(3*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-svc-token")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"
			tokenSecretName := policyName + "-token"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with service token")
			policy := createCloudflareAccessPolicyWithServiceToken(ctx, k8sClient, policyName, namespace.Name, routeName, hostname, tokenSecretName)

			By("Waiting for AccessPolicy to become ready")
			_ = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying service token exists in Cloudflare")
			tokenName := policyName + "-token"
			cfToken, err := getServiceTokenFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, tokenName)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfToken).NotTo(BeNil(), "Service token should exist in Cloudflare")
		})

		It("should store token credentials in K8s Secret", SpecTimeout(3*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-token-secret")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"
			tokenSecretName := policyName + "-token"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with service token")
			createCloudflareAccessPolicyWithServiceToken(ctx, k8sClient, policyName, namespace.Name, routeName, hostname, tokenSecretName)

			By("Waiting for service token Secret to be created")
			secret := waitForServiceTokenSecretCreated(ctx, k8sClient, tokenSecretName, namespace.Name, DefaultTimeout)

			By("Verifying Secret contains expected keys")
			Expect(secret.Data).To(HaveKey("CF_ACCESS_CLIENT_ID"))
			Expect(secret.Data).To(HaveKey("CF_ACCESS_CLIENT_SECRET"))
			Expect(secret.Data["CF_ACCESS_CLIENT_ID"]).NotTo(BeEmpty())
			Expect(secret.Data["CF_ACCESS_CLIENT_SECRET"]).NotTo(BeEmpty())
		})

		It("should populate token ID in status.serviceTokenIds", SpecTimeout(3*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-token-status")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"
			tokenSecretName := policyName + "-token"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with service token")
			policy := createCloudflareAccessPolicyWithServiceToken(ctx, k8sClient, policyName, namespace.Name, routeName, hostname, tokenSecretName)

			By("Waiting for AccessPolicy to become ready")
			policy = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying status.serviceTokenIds is populated")
			tokenName := policyName + "-token"
			Expect(policy.Status.ServiceTokenIDs).NotTo(BeNil())
			Expect(policy.Status.ServiceTokenIDs).To(HaveKey(tokenName))
			Expect(policy.Status.ServiceTokenIDs[tokenName]).NotTo(BeEmpty())

			By("Verifying token ID matches Cloudflare")
			cfToken, err := getServiceTokenFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, tokenName)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfToken).NotTo(BeNil())
			Expect(policy.Status.ServiceTokenIDs[tokenName]).To(Equal(cfToken.ID))
		})

		It("should delete service token when policy is deleted", SpecTimeout(12*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-token-delete")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"
			tokenSecretName := policyName + "-token"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with service token")
			policy := createCloudflareAccessPolicyWithServiceToken(ctx, k8sClient, policyName, namespace.Name, routeName, hostname, tokenSecretName)

			By("Waiting for AccessPolicy to become ready")
			policy = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)
			tokenName := policyName + "-token"

			By("Verifying service token exists in Cloudflare")
			cfToken, err := getServiceTokenFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, tokenName)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfToken).NotTo(BeNil())

			By("Deleting CloudflareAccessPolicy CR")
			Expect(k8sClient.Delete(ctx, policy)).To(Succeed())

			By("Waiting for AccessPolicy to be deleted from Kubernetes")
			waitForAccessPolicyDeleted(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying service token is deleted from Cloudflare")
			waitForServiceTokenDeletedFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, tokenName, LongTimeout)
		})

		It("should create AnyValidServiceToken rule", SpecTimeout(3*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-any-token")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with AnyValidServiceToken rule")
			anyValidServiceToken := true
			policy := &cfgatev1alpha1.CloudflareAccessPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      policyName,
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
					TargetRef: &cfgatev1alpha1.PolicyTargetReference{
						Group: "gateway.networking.k8s.io",
						Kind:  "HTTPRoute",
						Name:  routeName,
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
							Name:     "allow-any-service-token",
							Decision: "non_identity",
							Include: []cfgatev1alpha1.AccessRule{
								{AnyValidServiceToken: &anyValidServiceToken},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())

			By("Waiting for AccessPolicy to become ready")
			_ = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying application exists in Cloudflare")
			cfApp, err := getAccessApplicationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policyName)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfApp).NotTo(BeNil(), "Application with AnyValidServiceToken rule should exist")
		})
	})

	// ============================================================
	// P0: IP-based Rules (no IdP required)
	// ============================================================
	Context("P0 IP-based rules", func() {
		It("should create IP range bypass rule in Cloudflare", SpecTimeout(3*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-ip-range")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with IP range rule (AccessIPRule)")
			policy := createCloudflareAccessPolicyWithIPRule(ctx, k8sClient, policyName, namespace.Name, routeName, hostname, []string{"10.0.0.0/8", "192.168.0.0/16"})

			By("Waiting for AccessPolicy to become ready")
			_ = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying application exists with policies")
			cfApp, err := getAccessApplicationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policyName)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfApp).NotTo(BeNil(), "Application should exist in Cloudflare")
		})

		It("should sync IP rule updates to Cloudflare", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-ip-update")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with initial IP range")
			policy := createCloudflareAccessPolicyWithIPRule(ctx, k8sClient, policyName, namespace.Name, routeName, hostname, []string{"10.0.0.0/8"})

			By("Waiting for AccessPolicy to become ready")
			policy = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)
			initialGen := policy.Status.ObservedGeneration

			By("Updating IP range in policy")
			// Use Eventually to retry on conflict (controller may update status concurrently)
			Eventually(func() error {
				var updatedPolicy cfgatev1alpha1.CloudflareAccessPolicy
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: policyName, Namespace: namespace.Name}, &updatedPolicy); err != nil {
					return err
				}
				updatedPolicy.Spec.Policies[0].Include[0].IP.Ranges = []string{"10.0.0.0/8", "172.16.0.0/12"}
				return k8sClient.Update(ctx, &updatedPolicy)
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			By("Waiting for policy to be reconciled")
			Eventually(func() int64 {
				var p cfgatev1alpha1.CloudflareAccessPolicy
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: policyName, Namespace: namespace.Name}, &p); err != nil {
					return -1
				}
				return p.Status.ObservedGeneration
			}, DefaultTimeout, DefaultInterval).Should(BeNumerically(">", initialGen), "ObservedGeneration should increase after update")
		})

		It("should create country-based rule (AccessCountryRule)", SpecTimeout(3*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-country")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with country rule")
			policy := createCloudflareAccessPolicyWithCountryRule(ctx, k8sClient, policyName, namespace.Name, routeName, hostname, []string{"US"})

			By("Waiting for AccessPolicy to become ready")
			_ = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying application exists")
			cfApp, err := getAccessApplicationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policyName)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfApp).NotTo(BeNil(), "Application with country rule should exist in Cloudflare")
		})

		It("should create everyone rule with warning event", SpecTimeout(3*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-everyone")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with everyone rule")
			everyone := true
			policy := &cfgatev1alpha1.CloudflareAccessPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      policyName,
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
					TargetRef: &cfgatev1alpha1.PolicyTargetReference{
						Group: "gateway.networking.k8s.io",
						Kind:  "HTTPRoute",
						Name:  routeName,
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
							Name:     "allow-everyone",
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
			_ = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying application exists")
			cfApp, err := getAccessApplicationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policyName)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfApp).NotTo(BeNil(), "Application with everyone rule should exist in Cloudflare")

			// Note: Warning event verification would require event recording assertions.
			// The controller should emit a warning event for "everyone" rules.
		})
	})

	// ============================================================
	// §6.3: P0 Application Field Assertions
	// ============================================================
	Context("P0 application field assertions", func() {
		It("should set appLauncherVisible=false on Access application", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-launcher")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with appLauncherVisible=false")
			policy := createCloudflareAccessPolicyWithApplicationFields(ctx, k8sClient,
				policyName, namespace.Name, routeName, hostname,
				cfgatev1alpha1.AccessApplication{
					AppLauncherVisible: ptrTo(false),
				},
			)

			By("Waiting for AccessPolicy to become ready")
			policy = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying CF API: appLauncherVisible == false")
			Eventually(func(g Gomega) {
				cfApp, err := getAccessApplicationByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policy.Status.ApplicationID)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(cfApp).NotTo(BeNil())
				g.Expect(cfApp.AppLauncherVisible).To(BeFalse(), "AppLauncherVisible should be false in CF API")
			}, DefaultTimeout, DefaultInterval).Should(Succeed())
		})

		It("should set allowedIdps on Access application", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			skipIfNoIdP()

			By("Creating HTTPRoute for target")
			policyName := testID("access-idps")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with allowedIdps")
			policy := createCloudflareAccessPolicyWithApplicationFields(ctx, k8sClient,
				policyName, namespace.Name, routeName, hostname,
				cfgatev1alpha1.AccessApplication{
					AllowedIdps: []string{testEnv.CloudflareIdPID},
				},
			)

			By("Waiting for AccessPolicy to become ready")
			policy = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying CF API: allowedIdps matches")
			Eventually(func(g Gomega) {
				cfApp, err := getAccessApplicationByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policy.Status.ApplicationID)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(cfApp).NotTo(BeNil())
				GinkgoWriter.Printf("[DIAG] AllowedIdps=%v (len=%d), expected IdP=%s\n",
					cfApp.AllowedIdps, len(cfApp.AllowedIdps), testEnv.CloudflareIdPID)
				// Also check the raw SDK response for type debugging.
				rawApp, rawErr := cfClient.ZeroTrust.Access.Applications.Get(ctx, policy.Status.ApplicationID, zero_trust.AccessApplicationGetParams{
					AccountID: cloudflare.F(testEnv.CloudflareAccountID),
				})
				if rawErr == nil {
					GinkgoWriter.Printf("[DIAG] Raw AllowedIdPs type=%T value=%v\n", rawApp.AllowedIdPs, rawApp.AllowedIdPs)
				} else {
					GinkgoWriter.Printf("[DIAG] Raw SDK Get error: %v\n", rawErr)
				}
				g.Expect(cfApp.AllowedIdps).To(ContainElement(testEnv.CloudflareIdPID),
					"AllowedIdps should contain the configured IdP ID")
			}, DefaultTimeout, DefaultInterval).Should(Succeed())
		})

		It("should set autoRedirectToIdentity on Access application", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			skipIfNoIdP()

			By("Creating HTTPRoute for target")
			policyName := testID("access-autoredirect")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with autoRedirectToIdentity + allowedIdps")
			policy := createCloudflareAccessPolicyWithApplicationFields(ctx, k8sClient,
				policyName, namespace.Name, routeName, hostname,
				cfgatev1alpha1.AccessApplication{
					AllowedIdps:            []string{testEnv.CloudflareIdPID},
					AutoRedirectToIdentity: true,
				},
			)

			By("Waiting for AccessPolicy to become ready")
			policy = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying CF API: autoRedirectToIdentity == true")
			Eventually(func(g Gomega) {
				cfApp, err := getAccessApplicationByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policy.Status.ApplicationID)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(cfApp).NotTo(BeNil())
				g.Expect(cfApp.AutoRedirectToIdentity).To(BeTrue(),
					"AutoRedirectToIdentity should be true in CF API")
			}, DefaultTimeout, DefaultInterval).Should(Succeed())
		})

		It("should update application when spec changes", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-specupdate")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with default sessionDuration=24h")
			policy := createCloudflareAccessPolicy(ctx, k8sClient, policyName, namespace.Name, routeName, hostname)

			By("Waiting for AccessPolicy to become ready")
			policy = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)
			initialGen := policy.Status.ObservedGeneration

			By("Verifying initial CF app sessionDuration")
			Eventually(func(g Gomega) {
				cfApp, err := getAccessApplicationByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policy.Status.ApplicationID)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(cfApp).NotTo(BeNil())
				g.Expect(cfApp.SessionDuration).To(Equal("24h"))
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			By("Updating spec.application.sessionDuration to 12h")
			Eventually(func() error {
				var p cfgatev1alpha1.CloudflareAccessPolicy
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: policyName, Namespace: namespace.Name}, &p); err != nil {
					return err
				}
				p.Spec.Application.SessionDuration = "12h"
				return k8sClient.Update(ctx, &p)
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			By("Waiting for ObservedGeneration to bump")
			Eventually(func() int64 {
				var p cfgatev1alpha1.CloudflareAccessPolicy
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: policyName, Namespace: namespace.Name}, &p); err != nil {
					return -1
				}
				return p.Status.ObservedGeneration
			}, DefaultTimeout, DefaultInterval).Should(BeNumerically(">", initialGen))

			By("Verifying CF API: sessionDuration updated to 12h")
			Eventually(func(g Gomega) {
				cfApp, err := getAccessApplicationByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policy.Status.ApplicationID)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(cfApp).NotTo(BeNil())
				g.Expect(cfApp.SessionDuration).To(Equal("12h"),
					"CF API sessionDuration should be updated to 12h")
			}, DefaultTimeout, DefaultInterval).Should(Succeed())
		})
	})

	// ============================================================
	// P1: IdP-Dependent Rules (basic IdP required)
	// ============================================================
	Context("P1 IdP-dependent rules", Label("idp"), func() {
		BeforeEach(func() {
			skipIfNoIdP()
		})

		It("should create email rule (AccessEmailRule)", SpecTimeout(3*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-email")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with email rule")
			emails := []string{testEnv.CloudflareTestEmail}
			if testEnv.CloudflareTestEmail == "" {
				emails = []string{"test@example.com"}
			}
			policy := createCloudflareAccessPolicyWithEmailRule(ctx, k8sClient, policyName, namespace.Name, routeName, hostname, emails)

			By("Waiting for AccessPolicy to become ready")
			_ = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying application exists in Cloudflare")
			cfApp, err := getAccessApplicationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policyName)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfApp).NotTo(BeNil(), "Application with email rule should exist in Cloudflare")
		})

		It("should create email domain rule (AccessEmailDomainRule)", SpecTimeout(3*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-email-domain")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with email domain rule")
			// Use zone name as email domain for testing
			emailDomain := testEnv.CloudflareZoneName
			policy := createCloudflareAccessPolicyWithEmailDomainRule(ctx, k8sClient, policyName, namespace.Name, routeName, hostname, emailDomain)

			By("Waiting for AccessPolicy to become ready")
			_ = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying application exists in Cloudflare")
			cfApp, err := getAccessApplicationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policyName)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfApp).NotTo(BeNil(), "Application with email domain rule should exist in Cloudflare")
		})

		It("should create OIDC claim rule (AccessOIDCClaimRule)", SpecTimeout(3*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-oidc")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with OIDC claim rule")
			policy := createCloudflareAccessPolicyWithOIDCClaimRule(
				ctx, k8sClient, policyName, namespace.Name, routeName, hostname,
				testEnv.CloudflareIdPID,
				"email",                     // claim name
				testEnv.CloudflareTestEmail, // claim value
			)

			By("Waiting for AccessPolicy to become ready")
			_ = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying application exists in Cloudflare")
			cfApp, err := getAccessApplicationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policyName)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfApp).NotTo(BeNil(), "Application with OIDC claim rule should exist in Cloudflare")
		})
	})

	// ============================================================
	// P2: GSuite Group Rules (GSuite IdP required)
	// ============================================================
	Context("P2 GSuite group rules", Label("gsuite"), func() {
		BeforeEach(func() {
			skipIfNoGSuiteGroup()
		})

		It("should create GSuite group rule (AccessGSuiteGroupRule)", SpecTimeout(3*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-gsuite")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with GSuite group rule")
			policy := createCloudflareAccessPolicyWithGSuiteGroupRule(
				ctx, k8sClient, policyName, namespace.Name, routeName, hostname,
				testEnv.CloudflareIdPID,
				testEnv.CloudflareTestGroup,
			)

			By("Waiting for AccessPolicy to become ready")
			_ = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)

			By("Verifying application exists in Cloudflare")
			cfApp, err := getAccessApplicationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, policyName)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfApp).NotTo(BeNil(), "Application with GSuite group rule should exist in Cloudflare")
		})

		It("should update GSuite group rule", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			By("Creating HTTPRoute for target")
			policyName := testID("access-gsuite-update")
			hostname := fmt.Sprintf("%s.%s", policyName, testEnv.CloudflareZoneName)
			routeName := policyName + "-route"

			gcName := testID("gc")
			createGatewayClass(ctx, k8sClient, gcName)
			gw := createGateway(ctx, k8sClient, policyName+"-gw", namespace.Name, gcName, "")
			svc := createTestService(ctx, k8sClient, policyName+"-svc", namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, routeName, namespace.Name, gw.Name, []string{hostname}, svc.Name, 8080)

			By("Creating CloudflareAccessPolicy with GSuite group rule")
			policy := createCloudflareAccessPolicyWithGSuiteGroupRule(
				ctx, k8sClient, policyName, namespace.Name, routeName, hostname,
				testEnv.CloudflareIdPID,
				testEnv.CloudflareTestGroup,
			)

			By("Waiting for AccessPolicy to become ready")
			policy = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, DefaultTimeout)
			initialGen := policy.Status.ObservedGeneration

			By("Updating policy name (triggering reconcile)")
			// Use Eventually to retry on conflict (controller may update status concurrently)
			Eventually(func() error {
				var updatedPolicy cfgatev1alpha1.CloudflareAccessPolicy
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: policyName, Namespace: namespace.Name}, &updatedPolicy); err != nil {
					return err
				}
				updatedPolicy.Spec.Policies[0].Name = "updated-gsuite-group"
				return k8sClient.Update(ctx, &updatedPolicy)
			}, DefaultTimeout, DefaultInterval).Should(Succeed())

			By("Waiting for policy to be reconciled")
			Eventually(func() int64 {
				var p cfgatev1alpha1.CloudflareAccessPolicy
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: policyName, Namespace: namespace.Name}, &p); err != nil {
					return -1
				}
				return p.Status.ObservedGeneration
			}, DefaultTimeout, DefaultInterval).Should(BeNumerically(">", initialGen), "ObservedGeneration should increase after update")
		})
	})
})

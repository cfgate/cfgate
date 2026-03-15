// Package e2e contains end-to-end tests for cfgate.
// validation_test.go tests CEL validation rules defined in CRD types.
package e2e_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
)

// validationAccountID returns a Cloudflare account ID for CEL validation tests.
// If testEnv.CloudflareAccountID is set, it returns that value. Otherwise it
// returns a dummy 32-character hex string so CEL tests can run without
// Cloudflare credentials.
func validationAccountID() string {
	if testEnv.CloudflareAccountID != "" {
		return testEnv.CloudflareAccountID
	}
	return strings.Repeat("0", 32)
}

var _ = Describe("CEL Validation E2E", func() {
	var namespace *corev1.Namespace

	BeforeEach(func() {
		// Create unique namespace for this test.
		namespace = createTestNamespace("cfgate-validation-e2e")
		createCloudflareCredentialsSecret(namespace.Name)
	})

	AfterEach(func() {
		if testEnv.SkipCleanup {
			return
		}

		// Delete namespace - no Cloudflare resources created in validation tests.
		if namespace != nil {
			deleteTestNamespace(namespace)
		}
	})

	Context("CloudflareAccessPolicy validation", func() {
		It("should reject policy with no targets", func() {
			By("Creating AccessPolicy with neither targetRef nor targetRefs")
			everyone := true
			policy := &cfgatev1alpha1.CloudflareAccessPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("no-target"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
					// No targetRef, no targetRefs - should fail CEL validation
					CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
						Name:      "cloudflare-credentials",
						AccountID: validationAccountID(),
					},
					Application: cfgatev1alpha1.AccessApplication{
						Name:   "test-app",
						Domain: "test.example.com",
					},
					Policies: []cfgatev1alpha1.AccessPolicyRule{
						{
							Name:     "allow-all",
							Decision: "allow",
							Include: []cfgatev1alpha1.AccessRule{
								{
									Everyone: &everyone,
								},
							},
						},
					},
				},
			}

			By("Expecting API to reject the resource")
			err := k8sClient.Create(ctx, policy)
			Expect(err).To(HaveOccurred(), "API should reject policy with no targets")
			Expect(err.Error()).To(ContainSubstring("targetRef"),
				"Error should mention targetRef requirement")
		})

		It("should reject policy with both targetRef and targetRefs", func() {
			By("Creating AccessPolicy with both targetRef AND targetRefs")
			everyone := true
			policy := &cfgatev1alpha1.CloudflareAccessPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("both-targets"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
					// Both specified - should fail CEL validation (mutually exclusive)
					TargetRef: &cfgatev1alpha1.PolicyTargetReference{
						Group: "gateway.networking.k8s.io",
						Kind:  "HTTPRoute",
						Name:  "test-route",
					},
					TargetRefs: []cfgatev1alpha1.PolicyTargetReference{
						{
							Group: "gateway.networking.k8s.io",
							Kind:  "HTTPRoute",
							Name:  "other-route",
						},
					},
					CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
						Name:      "cloudflare-credentials",
						AccountID: validationAccountID(),
					},
					Application: cfgatev1alpha1.AccessApplication{
						Name:   "test-app",
						Domain: "test.example.com",
					},
					Policies: []cfgatev1alpha1.AccessPolicyRule{
						{
							Name:     "allow-all",
							Decision: "allow",
							Include: []cfgatev1alpha1.AccessRule{
								{
									Everyone: &everyone,
								},
							},
						},
					},
				},
			}

			By("Expecting API to reject the resource")
			err := k8sClient.Create(ctx, policy)
			Expect(err).To(HaveOccurred(), "API should reject policy with both targetRef and targetRefs")
			Expect(err.Error()).To(ContainSubstring("mutually exclusive"),
				"Error should mention mutual exclusivity")
		})
	})

	Context("CloudflareDNS validation", func() {
		It("should reject CloudflareDNS without zones", func() {
			By("Creating CloudflareDNS without zones")
			dns := &cfgatev1alpha1.CloudflareDNS{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("dns-no-zones"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareDNSSpec{
					TunnelRef: &cfgatev1alpha1.DNSTunnelRef{
						Name: "some-tunnel",
					},
					Zones: []cfgatev1alpha1.DNSZoneConfig{}, // Empty zones - should fail validation
				},
			}

			By("Expecting API to reject the resource")
			err := k8sClient.Create(ctx, dns)
			Expect(err).To(HaveOccurred(), "API should reject CloudflareDNS without zones")
			Expect(err.Error()).To(ContainSubstring("zones"),
				"Error should mention zones requirement")
		})

		It("should reject CloudflareDNS without tunnelRef or externalTarget", func() {
			By("Creating CloudflareDNS without tunnelRef or externalTarget")
			dns := &cfgatev1alpha1.CloudflareDNS{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("dns-no-target"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareDNSSpec{
					// Neither tunnelRef nor externalTarget specified
					Zones: []cfgatev1alpha1.DNSZoneConfig{
						{Name: "example.com"},
					},
				},
			}

			By("Expecting API to reject the resource")
			err := k8sClient.Create(ctx, dns)
			Expect(err).To(HaveOccurred(), "API should reject CloudflareDNS without target")
			Expect(err.Error()).To(ContainSubstring("tunnelRef"),
				"Error should mention tunnelRef or externalTarget requirement")
		})
	})

	Context("CloudflareTunnel validation", func() {
		It("should reject tunnel with both h2cOrigin and http2Origin", func() {
			By("Creating CloudflareTunnel with both h2cOrigin and http2Origin set")
			tunnel := &cfgatev1alpha1.CloudflareTunnel{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("h2c-http2-conflict"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareTunnelSpec{
					Tunnel: cfgatev1alpha1.TunnelIdentity{
						Name: testID("tunnel"),
					},
					Cloudflare: cfgatev1alpha1.CloudflareConfig{
						AccountID: validationAccountID(),
						SecretRef: cfgatev1alpha1.SecretRef{
							Name: "cloudflare-credentials",
						},
					},
					OriginDefaults: cfgatev1alpha1.OriginDefaults{
						HTTP2Origin: true,
						H2cOrigin:   true, // Mutually exclusive with HTTP2Origin
					},
				},
			}

			By("Expecting API to reject the resource")
			err := k8sClient.Create(ctx, tunnel)
			Expect(err).To(HaveOccurred(), "API should reject tunnel with both h2cOrigin and http2Origin")
			Expect(err.Error()).To(ContainSubstring("mutually exclusive"),
				"Error should mention mutual exclusivity")
		})

		It("should accept tunnel with only h2cOrigin", func() {
			By("Creating CloudflareTunnel with h2cOrigin only")
			tunnel := &cfgatev1alpha1.CloudflareTunnel{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("h2c-only"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareTunnelSpec{
					Tunnel: cfgatev1alpha1.TunnelIdentity{
						Name: testID("tunnel"),
					},
					Cloudflare: cfgatev1alpha1.CloudflareConfig{
						AccountID: validationAccountID(),
						SecretRef: cfgatev1alpha1.SecretRef{
							Name: "cloudflare-credentials",
						},
					},
					OriginDefaults: cfgatev1alpha1.OriginDefaults{
						H2cOrigin: true,
					},
				},
			}

			By("Expecting API to accept the resource")
			err := k8sClient.Create(ctx, tunnel)
			Expect(err).NotTo(HaveOccurred(), "API should accept tunnel with only h2cOrigin")

			By("Cleaning up created tunnel")
			Eventually(func() error {
				var created cfgatev1alpha1.CloudflareTunnel
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(tunnel), &created); err != nil {
					return err
				}
				if len(created.Finalizers) > 0 {
					created.Finalizers = nil
					return k8sClient.Update(ctx, &created)
				}
				return nil
			}, DefaultTimeout, DefaultInterval).Should(Succeed())
			Expect(k8sClient.Delete(ctx, tunnel)).To(Succeed())
		})
	})

	Context("valid resources", func() {
		It("should accept valid AccessPolicy with targetRef", func() {
			By("Creating valid AccessPolicy with targetRef")
			everyone := true
			policy := &cfgatev1alpha1.CloudflareAccessPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("valid-policy"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
					TargetRef: &cfgatev1alpha1.PolicyTargetReference{
						Group: "gateway.networking.k8s.io",
						Kind:  "HTTPRoute",
						Name:  "test-route",
					},
					CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
						Name:      "cloudflare-credentials",
						AccountID: validationAccountID(),
					},
					Application: cfgatev1alpha1.AccessApplication{
						Name:   "test-app",
						Domain: "test.example.com",
					},
					Policies: []cfgatev1alpha1.AccessPolicyRule{
						{
							Name:     "allow-all",
							Decision: "allow",
							Include: []cfgatev1alpha1.AccessRule{
								{
									Everyone: &everyone,
								},
							},
						},
					},
				},
			}

			By("Expecting API to accept the resource")
			err := k8sClient.Create(ctx, policy)
			Expect(err).NotTo(HaveOccurred(), "API should accept valid policy with targetRef")

			By("Cleaning up created policy")
			// Remove finalizers if any were added, then delete.
			// Use Eventually to retry on conflict (controller may update status concurrently)
			Eventually(func() error {
				var created cfgatev1alpha1.CloudflareAccessPolicy
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), &created); err != nil {
					return err
				}
				if len(created.Finalizers) > 0 {
					created.Finalizers = nil
					return k8sClient.Update(ctx, &created)
				}
				return nil
			}, DefaultTimeout, DefaultInterval).Should(Succeed())
			Expect(k8sClient.Delete(ctx, policy)).To(Succeed())
		})
	})

	Context("PolicyTargetReference validation", func() {
		It("should reject targetRef with invalid group", func() {
			By("Creating AccessPolicy with non-Gateway API group")
			everyone := true
			policy := &cfgatev1alpha1.CloudflareAccessPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("invalid-group"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
					TargetRef: &cfgatev1alpha1.PolicyTargetReference{
						Group: "apps", // Invalid - must be gateway.networking.k8s.io
						Kind:  "HTTPRoute",
						Name:  "test-route",
					},
					CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
						Name:      "cloudflare-credentials",
						AccountID: validationAccountID(),
					},
					Application: cfgatev1alpha1.AccessApplication{
						Name:   "test-app",
						Domain: "test.example.com",
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

			By("Expecting API to reject the resource")
			err := k8sClient.Create(ctx, policy)
			Expect(err).To(HaveOccurred(), "API should reject policy with invalid group")
			Expect(err.Error()).To(ContainSubstring("gateway.networking.k8s.io"),
				"Error should mention required group")
		})

		It("should reject targetRef with invalid kind", func() {
			By("Creating AccessPolicy with invalid Kind")
			everyone := true
			policy := &cfgatev1alpha1.CloudflareAccessPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("invalid-kind"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
					TargetRef: &cfgatev1alpha1.PolicyTargetReference{
						Group: "gateway.networking.k8s.io",
						Kind:  "Deployment", // Invalid - must be Gateway, HTTPRoute, etc.
						Name:  "test-route",
					},
					CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
						Name:      "cloudflare-credentials",
						AccountID: validationAccountID(),
					},
					Application: cfgatev1alpha1.AccessApplication{
						Name:   "test-app",
						Domain: "test.example.com",
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

			By("Expecting API to reject the resource")
			err := k8sClient.Create(ctx, policy)
			Expect(err).To(HaveOccurred(), "API should reject policy with invalid kind")
			Expect(err.Error()).To(ContainSubstring("kind"),
				"Error should mention kind validation")
		})
	})

	Context("AccessRule validation", func() {
		It("should reject policy rule with no include rules", func() {
			By("Creating AccessPolicy with empty include array")
			policy := &cfgatev1alpha1.CloudflareAccessPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("no-include-rules"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
					TargetRef: &cfgatev1alpha1.PolicyTargetReference{
						Group: "gateway.networking.k8s.io",
						Kind:  "HTTPRoute",
						Name:  "test-route",
					},
					CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
						Name:      "cloudflare-credentials",
						AccountID: validationAccountID(),
					},
					Application: cfgatev1alpha1.AccessApplication{
						Name:   "test-app",
						Domain: "test.example.com",
					},
					Policies: []cfgatev1alpha1.AccessPolicyRule{
						{
							Name:     "empty-include",
							Decision: "allow",
							Include:  []cfgatev1alpha1.AccessRule{{}}, // Empty rule - no type specified
						},
					},
				},
			}

			By("Expecting API to reject the resource")
			err := k8sClient.Create(ctx, policy)
			Expect(err).To(HaveOccurred(), "API should reject policy with empty include rule")
			Expect(err.Error()).To(ContainSubstring("rule type"),
				"Error should mention rule type requirement")
		})
	})

	Context("pattern validations", func() {
		It("should reject invalid sessionDuration format", func() {
			By("Creating AccessPolicy with invalid sessionDuration")
			everyone := true
			policy := &cfgatev1alpha1.CloudflareAccessPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("invalid-session"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
					TargetRef: &cfgatev1alpha1.PolicyTargetReference{
						Group: "gateway.networking.k8s.io",
						Kind:  "HTTPRoute",
						Name:  "test-route",
					},
					CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
						Name:      "cloudflare-credentials",
						AccountID: validationAccountID(),
					},
					Application: cfgatev1alpha1.AccessApplication{
						Name:            "test-app",
						Domain:          "test.example.com",
						SessionDuration: "invalid", // Must match ^[0-9]+(h|m|s)$
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

			By("Expecting API to reject the resource")
			err := k8sClient.Create(ctx, policy)
			Expect(err).To(HaveOccurred(), "API should reject policy with invalid sessionDuration")
			Expect(err.Error()).To(Or(
				ContainSubstring("sessionDuration"),
				ContainSubstring("pattern"),
			), "Error should mention sessionDuration or pattern validation")
		})

		It("should reject invalid duration format in service token", func() {
			By("Creating AccessPolicy with invalid service token duration")
			policy := &cfgatev1alpha1.CloudflareAccessPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("invalid-token-duration"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
					TargetRef: &cfgatev1alpha1.PolicyTargetReference{
						Group: "gateway.networking.k8s.io",
						Kind:  "HTTPRoute",
						Name:  "test-route",
					},
					CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
						Name:      "cloudflare-credentials",
						AccountID: validationAccountID(),
					},
					Application: cfgatev1alpha1.AccessApplication{
						Name:   "test-app",
						Domain: "test.example.com",
					},
					Policies: []cfgatev1alpha1.AccessPolicyRule{
						{
							Name:     "allow-token",
							Decision: "non_identity",
							Include:  []cfgatev1alpha1.AccessRule{{AnyValidServiceToken: ptrTo(true)}},
						},
					},
					ServiceTokens: []cfgatev1alpha1.ServiceTokenConfig{
						{
							Name:     "test-token",
							Duration: "365d", // Invalid - must be hours only (^[0-9]+h$)
							SecretRef: cfgatev1alpha1.ServiceTokenSecretRef{
								Name: "test-token-secret",
							},
						},
					},
				},
			}

			By("Expecting API to reject the resource")
			err := k8sClient.Create(ctx, policy)
			Expect(err).To(HaveOccurred(), "API should reject policy with invalid token duration")
			Expect(err.Error()).To(Or(
				ContainSubstring("duration"),
				ContainSubstring("pattern"),
			), "Error should mention duration or pattern validation")
		})
	})
})

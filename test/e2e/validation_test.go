package e2e_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
)

func validationAccountID() string {
	if testEnv.CloudflareAccountID != "" {
		return testEnv.CloudflareAccountID
	}
	return strings.Repeat("0", 32)
}

var _ = Describe("CEL Validation E2E", func() {
	var namespace *corev1.Namespace

	BeforeEach(func() {
		namespace = createTestNamespace("cfgate-validation-e2e")
		createCloudflareCredentialsSecret(namespace.Name)
	})

	AfterEach(func() {
		if !testEnv.SkipCleanup && namespace != nil {
			deleteTestNamespace(namespace)
		}
	})

	Context("CloudflareTunnel validation", func() {
		It("rejects mutually exclusive h2c and HTTP/2 origin defaults", func() {
			tunnel := &cfgatev1alpha1.CloudflareTunnel{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("h2c-http2"),
					Namespace: namespace.Name,
				},
				Spec: cfgatev1alpha1.CloudflareTunnelSpec{
					Tunnel: cfgatev1alpha1.TunnelIdentity{
						Name: testID("h2c-http2"),
					},
					Cloudflare: cfgatev1alpha1.CloudflareConfig{
						AccountID: validationAccountID(),
						SecretRef: cfgatev1alpha1.SecretRef{
							Name: "cloudflare-credentials",
						},
					},
					OriginDefaults: cfgatev1alpha1.OriginDefaults{
						HTTP2Origin: true,
						H2cOrigin:   true,
					},
				},
			}

			err := k8sClient.Create(ctx, tunnel)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("http2Origin and h2cOrigin are mutually exclusive"))
		})
	})

	Context("CloudflareAccessPolicy validation", func() {
		It("rejects reusable policy with no include rules", func() {
			policy := validReusablePolicy(testID("no-include"), namespace.Name)
			policy.Spec.Include = nil
			err := k8sClient.Create(ctx, policy)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("include"))
		})

		It("rejects invalid reusable policy sessionDuration", func() {
			policy := validReusablePolicy(testID("bad-duration"), namespace.Name)
			policy.Spec.SessionDuration = "365d"
			err := k8sClient.Create(ctx, policy)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Or(ContainSubstring("sessionDuration"), ContainSubstring("pattern")))
		})

		It("accepts Go-style reusable policy durations", func() {
			policy := validReusablePolicy(testID("good-duration"), namespace.Name)
			policy.Spec.SessionDuration = "2h45m"
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		})

		It("rejects empty AccessRule objects", func() {
			policy := validReusablePolicy(testID("empty-rule"), namespace.Name)
			policy.Spec.Include = []cfgatev1alpha1.AccessRule{{}}
			err := k8sClient.Create(ctx, policy)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("rule type"))
		})

		It("rejects invalid service token duration", func() {
			policy := validReusablePolicy(testID("bad-token-duration"), namespace.Name)
			policy.Spec.ServiceTokens = []cfgatev1alpha1.ServiceTokenConfig{{
				Name:      "token",
				Duration:  "365d",
				SecretRef: cfgatev1alpha1.ServiceTokenSecretRef{Name: "token-secret"},
			}}
			err := k8sClient.Create(ctx, policy)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Or(ContainSubstring("duration"), ContainSubstring("pattern")))
		})
	})

	Context("CloudflareAccessApplication validation", func() {
		It("rejects application with no targets", func() {
			app := validAccessApplication(testID("no-target"), namespace.Name, "policy")
			app.Spec.TargetRef = nil
			err := k8sClient.Create(ctx, app)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("targetRef"))
		})

		It("rejects application with both targetRef and targetRefs", func() {
			app := validAccessApplication(testID("both-targets"), namespace.Name, "policy")
			app.Spec.TargetRefs = []cfgatev1alpha1.PolicyTargetReference{{
				Group: "gateway.networking.k8s.io",
				Kind:  "HTTPRoute",
				Name:  "other",
			}}
			err := k8sClient.Create(ctx, app)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("mutually exclusive"))
		})

		It("rejects duplicate policyRefs", func() {
			app := validAccessApplication(testID("duplicate-policyrefs"), namespace.Name, "policy")
			app.Spec.PolicyRefs = []cfgatev1alpha1.AccessPolicyReference{{Name: "policy"}, {Name: "policy"}}
			err := k8sClient.Create(ctx, app)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("duplicate"))
		})

		It("rejects mixed implicit and explicit policyRef precedence", func() {
			precedence := 2
			app := validAccessApplication(testID("mixed-precedence"), namespace.Name, "policy")
			app.Spec.PolicyRefs = []cfgatev1alpha1.AccessPolicyReference{
				{Name: "policy"},
				{Name: "other", Precedence: &precedence},
			}
			err := k8sClient.Create(ctx, app)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("all omit precedence or all specify precedence"))
		})

		It("rejects duplicate explicit policyRef precedence", func() {
			precedence := 2
			app := validAccessApplication(testID("duplicate-precedence"), namespace.Name, "policy")
			app.Spec.PolicyRefs = []cfgatev1alpha1.AccessPolicyReference{
				{Name: "policy", Precedence: &precedence},
				{Name: "other", Precedence: &precedence},
			}
			err := k8sClient.Create(ctx, app)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("precedence values must be unique"))
		})

		It("accepts unique explicit policyRef precedence", func() {
			first := 10
			second := 20
			app := validAccessApplication(testID("explicit-precedence"), namespace.Name, "policy")
			app.Spec.PolicyRefs = []cfgatev1alpha1.AccessPolicyReference{
				{Name: "policy", Precedence: &first},
				{Name: "other", Precedence: &second},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
		})

		It("rejects invalid target group and kind", func() {
			app := validAccessApplication(testID("bad-target"), namespace.Name, "policy")
			app.Spec.TargetRef.Group = "apps"
			app.Spec.TargetRef.Kind = "Deployment"
			err := k8sClient.Create(ctx, app)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Or(ContainSubstring("gateway.networking.k8s.io"), ContainSubstring("Unsupported value")))
		})

		It("rejects Access app paths with query string", func() {
			app := validAccessApplication(testID("bad-path"), namespace.Name, "policy")
			app.Spec.Application.Path = "/admin?debug=true"
			err := k8sClient.Create(ctx, app)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Or(ContainSubstring("path"), ContainSubstring("pattern")))
		})

		It("accepts Access app paths with colon segments", func() {
			app := validAccessApplication(testID("colon-path"), namespace.Name, "policy")
			app.Spec.Application.Path = "/api:v1"
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
		})
	})
})

func validReusablePolicy(name, namespace string) *cfgatev1alpha1.CloudflareAccessPolicy {
	return &cfgatev1alpha1.CloudflareAccessPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				"cfgate.io/deletion-policy": "orphan",
			},
		},
		Spec: cfgatev1alpha1.CloudflareAccessPolicySpec{
			CloudflareRef:   cloudflareSecretRef(),
			Name:            name,
			Decision:        "allow",
			Include:         []cfgatev1alpha1.AccessRule{{Everyone: ptrTo(true)}},
			SessionDuration: "300ms",
		},
	}
}

func validAccessApplication(name, namespace, policyName string) *cfgatev1alpha1.CloudflareAccessApplication {
	return &cfgatev1alpha1.CloudflareAccessApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				"cfgate.io/deletion-policy": "orphan",
			},
		},
		Spec: cfgatev1alpha1.CloudflareAccessApplicationSpec{
			TargetRef: &cfgatev1alpha1.PolicyTargetReference{
				Group: "gateway.networking.k8s.io",
				Kind:  "HTTPRoute",
				Name:  "route",
			},
			CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
				Name:      "cloudflare-credentials",
				AccountID: validationAccountID(),
			},
			Application: cfgatev1alpha1.AccessApplication{
				Name: "app",
				Path: "/admin",
			},
			PolicyRefs: []cfgatev1alpha1.AccessPolicyReference{{Name: policyName}},
		},
	}
}

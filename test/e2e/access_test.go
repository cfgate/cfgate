package e2e_test

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/option"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CloudflareAccessPolicy and CloudflareAccessApplication E2E", Label("cloudflare"), func() {
	var namespace string
	var namespaceObj *corev1.Namespace
	var cfClient *cloudflare.Client

	BeforeEach(func(ctx SpecContext) {
		skipIfNoCredentials()
		cfClient = cloudflare.NewClient(option.WithAPIToken(testEnv.CloudflareAPIToken))
		ns := createTestNamespace("access")
		namespaceObj = ns
		namespace = ns.Name
		createCloudflareCredentialsSecret(namespace)
		tunnelName := testID("access-tunnel")
		createCloudflareTunnelInContext(ctx, k8sClient, "edge", namespace, tunnelName)
		createGatewayClass(ctx, k8sClient, testID("access-gc"))
	})

	AfterEach(func() {
		deleteTestNamespace(namespaceObj)
	})

	It("rejects non-self-hosted Access Application types at admission", func(ctx SpecContext) {
		appName := testID("saas-app")
		app := &cfgatev1alpha1.CloudflareAccessApplication{
			ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
			Spec: cfgatev1alpha1.CloudflareAccessApplicationSpec{
				TargetRef: &cfgatev1alpha1.PolicyTargetReference{
					Group: "gateway.networking.k8s.io",
					Kind:  "HTTPRoute",
					Name:  "route",
				},
				Application: cfgatev1alpha1.AccessApplication{
					Name: appName,
					Type: "saas",
				},
				PolicyRefs: []cfgatev1alpha1.AccessPolicyReference{{Name: "policy"}},
			},
		}

		err := k8sClient.Create(ctx, app)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(Or(
			ContainSubstring("Unsupported value: \"saas\""),
			ContainSubstring("supported values: \"self_hosted\""),
		))
	})

	It("syncs a reusable policy and binds one application to an HTTPRoute", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
		gatewayClassName := testID("access-gc-single")
		createGatewayClass(ctx, k8sClient, gatewayClassName)
		createGateway(ctx, k8sClient, "public", namespace, gatewayClassName, "edge")
		createTestService(ctx, k8sClient, "app", namespace, 80)
		hostname := fmt.Sprintf("%s.%s", testID("admin"), testEnv.CloudflareZoneName)
		createHTTPRoute(ctx, k8sClient, "admin", namespace, "public", []string{hostname}, "app", 80)

		policyName := testID("allow-all")
		createReusableAccessPolicy(ctx, k8sClient, policyName, namespace, "allow",
			[]cfgatev1alpha1.AccessRule{{Everyone: ptrTo(true)}}, nil)
		policy := waitForAccessPolicyReady(ctx, k8sClient, policyName, namespace, LongTimeout)
		Expect(policy.Status.PolicyID).NotTo(BeEmpty())
		Expect(policy.Status.Reusable).To(BeTrue())

		appName := testID("admin-app")
		createCloudflareAccessApplication(ctx, k8sClient, appName, namespace, "admin",
			cfgatev1alpha1.AccessApplication{Name: appName},
			cfgatev1alpha1.AccessPolicyReference{Name: policyName},
		)
		app := waitForAccessApplicationReady(ctx, k8sClient, appName, namespace, LongTimeout)
		Expect(app.Status.AttachedTargets).To(Equal(int32(1)))
		Expect(firstAccessApplicationID(app)).NotTo(BeEmpty())
		Expect(firstAccessApplicationAUD(app)).NotTo(BeEmpty())
		Expect(app.Status.Applications[0].Domain).To(Equal(hostname))

		cfApp, err := getAccessApplicationByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, firstAccessApplicationID(app))
		Expect(err).NotTo(HaveOccurred())
		Expect(cfApp.Domain).To(Equal(hostname))
	})

	It("binds separate reusable policies to named HTTPRoute paths while root stays public", SpecTimeout(6*time.Minute), func(ctx SpecContext) {
		gatewayClassName := testID("access-gc-paths")
		createGatewayClass(ctx, k8sClient, gatewayClassName)
		createGateway(ctx, k8sClient, "public", namespace, gatewayClassName, "edge")
		createTestService(ctx, k8sClient, "app", namespace, 80)
		hostname := fmt.Sprintf("%s.%s", testID("paths"), testEnv.CloudflareZoneName)
		createNamedPathRoute(ctx, "repo-route", namespace, "public", hostname)

		adminPolicy := testID("admin-policy")
		reposPolicy := testID("repos-policy")
		createReusableAccessPolicy(ctx, k8sClient, adminPolicy, namespace, "allow",
			[]cfgatev1alpha1.AccessRule{{Email: &cfgatev1alpha1.AccessEmailRule{Addresses: []string{testEnv.CloudflareTestEmail}}}}, nil)
		createReusableAccessPolicy(ctx, k8sClient, reposPolicy, namespace, "allow",
			[]cfgatev1alpha1.AccessRule{{Everyone: ptrTo(true)}}, nil)
		waitForAccessPolicyReady(ctx, k8sClient, adminPolicy, namespace, LongTimeout)
		waitForAccessPolicyReady(ctx, k8sClient, reposPolicy, namespace, LongTimeout)

		createPathApplication(ctx, "admin-app", namespace, "repo-route", "admin", adminPolicy)
		createPathApplication(ctx, "repos-app", namespace, "repo-route", "repos", reposPolicy)

		adminApp := waitForAccessApplicationReady(ctx, k8sClient, "admin-app", namespace, LongTimeout)
		reposApp := waitForAccessApplicationReady(ctx, k8sClient, "repos-app", namespace, LongTimeout)
		Expect(adminApp.Status.Applications[0].Domain).To(Equal(hostname + "/admin"))
		Expect(reposApp.Status.Applications[0].Domain).To(Equal(hostname + "/repos"))
	})

	It("keeps reusable policy when application is deleted and blocks policy deletion while linked", SpecTimeout(8*time.Minute), func(ctx SpecContext) {
		gatewayClassName := testID("access-gc-delete")
		createGatewayClass(ctx, k8sClient, gatewayClassName)
		createGateway(ctx, k8sClient, "public", namespace, gatewayClassName, "edge")
		createTestService(ctx, k8sClient, "app", namespace, 80)
		hostname := fmt.Sprintf("%s.%s", testID("delete"), testEnv.CloudflareZoneName)
		createHTTPRoute(ctx, k8sClient, "delete-route", namespace, "public", []string{hostname}, "app", 80)

		policyName := testID("linked-policy")
		createReusableAccessPolicy(ctx, k8sClient, policyName, namespace, "allow",
			[]cfgatev1alpha1.AccessRule{{Everyone: ptrTo(true)}}, nil)
		waitForAccessPolicyReady(ctx, k8sClient, policyName, namespace, LongTimeout)
		appName := testID("linked-app")
		createCloudflareAccessApplication(ctx, k8sClient, appName, namespace, "delete-route",
			cfgatev1alpha1.AccessApplication{Name: appName},
			cfgatev1alpha1.AccessPolicyReference{Name: policyName},
		)
		waitForAccessApplicationReady(ctx, k8sClient, appName, namespace, LongTimeout)

		var policy cfgatev1alpha1.CloudflareAccessPolicy
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: policyName, Namespace: namespace}, &policy)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &policy)).To(Succeed())
		Consistently(func() bool {
			var current cfgatev1alpha1.CloudflareAccessPolicy
			err := k8sClient.Get(ctx, client.ObjectKey{Name: policyName, Namespace: namespace}, &current)
			return err == nil && current.DeletionTimestamp != nil
		}, 10*time.Second, DefaultInterval).Should(BeTrue(), "linked policy should stay blocked")

		var app cfgatev1alpha1.CloudflareAccessApplication
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: appName, Namespace: namespace}, &app)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &app)).To(Succeed())
		waitForAccessApplicationDeleted(ctx, k8sClient, appName, namespace, LongTimeout)
		waitForAccessPolicyDeleted(ctx, k8sClient, policyName, namespace, LongTimeout)
	})

	It("creates service token policy and attaches it to an application", SpecTimeout(6*time.Minute), func(ctx SpecContext) {
		gatewayClassName := testID("access-gc-token")
		createGatewayClass(ctx, k8sClient, gatewayClassName)
		createGateway(ctx, k8sClient, "public", namespace, gatewayClassName, "edge")
		createTestService(ctx, k8sClient, "app", namespace, 80)
		hostname := fmt.Sprintf("%s.%s", testID("token"), testEnv.CloudflareZoneName)
		createHTTPRoute(ctx, k8sClient, "token-route", namespace, "public", []string{hostname}, "app", 80)

		policyName := testID("token-policy")
		tokenName := policyName + "-token"
		createReusableAccessPolicy(ctx, k8sClient, policyName, namespace, "non_identity",
			[]cfgatev1alpha1.AccessRule{{ServiceToken: &cfgatev1alpha1.AccessServiceTokenRule{Name: tokenName}}},
			[]cfgatev1alpha1.ServiceTokenConfig{{
				Name:      tokenName,
				Duration:  "8760h",
				SecretRef: cfgatev1alpha1.ServiceTokenSecretRef{Name: "token-secret"},
			}},
		)
		policy := waitForAccessPolicyReady(ctx, k8sClient, policyName, namespace, LongTimeout)
		Expect(policy.Status.ServiceTokenIDs).To(HaveKey(tokenName))

		var secret corev1.Secret
		Eventually(func() bool {
			return k8sClient.Get(ctx, client.ObjectKey{Name: "token-secret", Namespace: namespace}, &secret) == nil
		}, LongTimeout, DefaultInterval).Should(BeTrue())

		createCloudflareAccessApplication(ctx, k8sClient, "token-app", namespace, "token-route",
			cfgatev1alpha1.AccessApplication{Name: "token-app", ServiceAuth401Redirect: true},
			cfgatev1alpha1.AccessPolicyReference{Name: policyName},
		)
		waitForAccessApplicationReady(ctx, k8sClient, "token-app", namespace, LongTimeout)
	})
})

func createNamedPathRoute(ctx SpecContext, name, namespace, gatewayName, hostname string) {
	parentNS := gatewayv1.Namespace(namespace)
	admin := gatewayv1.SectionName("admin")
	repos := gatewayv1.SectionName("repos")
	prefix := gatewayv1.PathMatchPathPrefix
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{{
				Name:      gatewayv1.ObjectName(gatewayName),
				Namespace: &parentNS,
			}}},
			Hostnames: []gatewayv1.Hostname{gatewayv1.Hostname(hostname)},
			Rules: []gatewayv1.HTTPRouteRule{
				{Name: &admin, Matches: []gatewayv1.HTTPRouteMatch{{Path: &gatewayv1.HTTPPathMatch{Type: &prefix, Value: ptrTo("/admin")}}}, BackendRefs: routeBackend("app", 80)},
				{Name: &repos, Matches: []gatewayv1.HTTPRouteMatch{{Path: &gatewayv1.HTTPPathMatch{Type: &prefix, Value: ptrTo("/repos")}}}, BackendRefs: routeBackend("app", 80)},
			},
		},
	}
	Expect(k8sClient.Create(ctx, route)).To(Succeed())
}

func createPathApplication(ctx SpecContext, name, namespace, routeName, sectionName, policyName string) {
	section := sectionName
	app := &cfgatev1alpha1.CloudflareAccessApplication{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: cfgatev1alpha1.CloudflareAccessApplicationSpec{
			TargetRef: &cfgatev1alpha1.PolicyTargetReference{
				Group:       "gateway.networking.k8s.io",
				Kind:        "HTTPRoute",
				Name:        routeName,
				SectionName: &section,
			},
			CloudflareRef: &cfgatev1alpha1.CloudflareSecretRef{
				Name:      "cloudflare-credentials",
				AccountID: testEnv.CloudflareAccountID,
			},
			Application: cfgatev1alpha1.AccessApplication{Name: name},
			PolicyRefs:  []cfgatev1alpha1.AccessPolicyReference{{Name: policyName}},
		},
	}
	Expect(k8sClient.Create(ctx, app)).To(Succeed())
}

func routeBackend(service string, port int32) []gatewayv1.HTTPBackendRef {
	return []gatewayv1.HTTPBackendRef{{
		BackendRef: gatewayv1.BackendRef{
			BackendObjectReference: gatewayv1.BackendObjectReference{
				Name: gatewayv1.ObjectName(service),
				Port: ptrTo(gatewayv1.PortNumber(port)),
			},
		},
	}}
}

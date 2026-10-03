package e2e_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	cloudflare "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/zero_trust"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflared"
)

var _ = Describe("Maintenance external effects", Label("cloudflare", "maintenance"), func() {
	var namespace *corev1.Namespace
	var cfClient *cloudflare.Client
	var releaseHeldPod func()

	BeforeEach(func() {
		releaseHeldPod = nil
		namespace = nil
		skipIfNoCredentials()
		namespace = createTestNamespace("cfgate-maintenance-e2e")
		createCloudflareCredentialsSecret(namespace.Name)
		cfClient = getCloudflareClient()
	})
	AfterEach(func() {
		if releaseHeldPod != nil {
			releaseHeldPod()
		}
		deleteTestNamespace(namespace)
	})

	It("reloads origin CA trust after Secret rotation", SpecTimeout(12*time.Minute), func(ctx SpecContext) {
		skipIfNoZone()
		certA, keyA := e2eTLSCertificate()
		certB, keyB := e2eTLSCertificate()
		ca := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "origin-ca", Namespace: namespace.Name}, Data: map[string][]byte{"ca.crt": certA}}
		tlsSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "origin-tls", Namespace: namespace.Name}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": certA, "tls.key": keyA}}
		Expect(k8sClient.Create(ctx, ca)).To(Succeed())
		Expect(k8sClient.Create(ctx, tlsSecret)).To(Succeed())
		tunnel := createCloudflareTunnel(ctx, k8sClient, testID("ca-reload"), namespace.Name, testID("ca-reload-tunnel"))
		Eventually(ctx, func() error {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(tunnel), tunnel); err != nil {
				return err
			}
			tunnel.Spec.OriginDefaults.CAPoolSecretRef = &cfgatev1alpha1.CAPoolSecretRef{Name: ca.Name}
			return k8sClient.Update(ctx, tunnel)
		}, ShortTimeout, DefaultInterval).Should(Succeed())
		tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, LongTimeout)
		class := createGatewayClass(ctx, k8sClient, testID("ca-class"))
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(context.Background(), class))).To(Succeed()) })
		gateway := createGateway(ctx, k8sClient, "gateway", namespace.Name, class.Name, tunnel.Name)
		service := createTestService(ctx, k8sClient, "tls-origin", namespace.Name, 8080)
		deployMaintenanceOrigin(ctx, service, tlsSecret.Name)
		hostname := testID("ca-host") + "." + testEnv.CloudflareZoneName
		route := createHTTPRoute(ctx, k8sClient, "tls-route", namespace.Name, gateway.Name, []string{hostname}, service.Name, 8080)
		updateHTTPRouteAnnotations(ctx, k8sClient, route.Name, route.Namespace, func(a map[string]string) {
			a["cfgate.io/origin-protocol"] = "https"
			a["cfgate.io/origin-server-name"] = "origin.test"
			a["cfgate.io/dns-sync"] = "ca-reload"
		})
		dns := createCloudflareDNSWithGatewayRoutes(ctx, k8sClient, "dns", namespace.Name, tunnel.Name, []string{testEnv.CloudflareZoneName}, "cfgate.io/dns-sync=ca-reload")
		Eventually(ctx, func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dns), dns)).To(Succeed())
			g.Expect(meta.IsStatusConditionTrue(dns.Status.Conditions, "Ready")).To(BeTrue())
		}, LongTimeout, DefaultInterval).Should(Succeed())
		expectMaintenanceResponse(ctx, hostname, service.Name, "", http.StatusOK)
		var before appsv1.Deployment
		deploymentKey := client.ObjectKey{Namespace: tunnel.Namespace, Name: cloudflared.DeploymentName(tunnel.Name)}
		Expect(k8sClient.Get(ctx, deploymentKey, &before)).To(Succeed())
		By("Changing only the CA Secret, then rejecting the still-running A certificate")
		updateSecret := func(secret *corev1.Secret, data map[string][]byte) {
			Eventually(ctx, func() error {
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(secret), secret); err != nil {
					return err
				}
				secret.Data = data
				return k8sClient.Update(ctx, secret)
			}, ShortTimeout, DefaultInterval).Should(Succeed())
		}
		updateSecret(ca, map[string][]byte{"ca.crt": certB})
		Eventually(ctx, func(g Gomega) {
			var current appsv1.Deployment
			g.Expect(k8sClient.Get(ctx, deploymentKey, &current)).To(Succeed())
			g.Expect(current.Spec.Template.Annotations["cfgate.io/origin-ca-revision"]).NotTo(Equal(before.Spec.Template.Annotations["cfgate.io/origin-ca-revision"]))
			g.Expect(current.Status.ObservedGeneration).To(Equal(current.Generation))
			g.Expect(current.Status.UpdatedReplicas).To(Equal(*current.Spec.Replicas))
			g.Expect(current.Status.AvailableReplicas).To(Equal(*current.Spec.Replicas))
		}, LongTimeout, DefaultInterval).Should(Succeed())
		expectMaintenanceResponse(ctx, hostname, "", "", http.StatusBadGateway)
		By("Accepting B and continuing to reject A without manually restarting connectors")
		updateSecret(tlsSecret, map[string][]byte{"tls.crt": certB, "tls.key": keyB})
		expectMaintenanceResponse(ctx, hostname, service.Name, "", http.StatusOK)
		updateSecret(tlsSecret, map[string][]byte{"tls.crt": certA, "tls.key": keyA})
		expectMaintenanceResponse(ctx, hostname, "", "", http.StatusBadGateway)
	})

	It("withdraws revoked backends, watches annotations and repairs remote drift", SpecTimeout(12*time.Minute), func(ctx SpecContext) {
		skipIfNoZone()
		backendNamespace := createTestNamespace("cfgate-maintenance-backend")
		DeferCleanup(func() { deleteTestNamespace(backendNamespace) })
		tunnel := createCloudflareTunnel(ctx, k8sClient, testID("maintenance"), namespace.Name, testID("maintenance-tunnel"))
		tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, LongTimeout)
		class := createGatewayClass(ctx, k8sClient, testID("maintenance-class"))
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(context.Background(), class))).To(Succeed()) })
		gateway := createGateway(ctx, k8sClient, testID("maintenance-gw"), namespace.Name, class.Name, namespace.Name+"/"+tunnel.Name)
		service := createTestService(ctx, k8sClient, testID("maintenance-svc"), backendNamespace.Name, 8080)
		deployMaintenanceOrigin(ctx, service)
		hostname := testID("maintenance-host") + "." + testEnv.CloudflareZoneName
		grant := &gatewayv1b1.ReferenceGrant{
			ObjectMeta: metav1.ObjectMeta{Name: "maintenance-backend", Namespace: backendNamespace.Name},
			Spec: gatewayv1b1.ReferenceGrantSpec{
				From: []gatewayv1b1.ReferenceGrantFrom{{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute", Namespace: gatewayv1b1.Namespace(namespace.Name)}},
				To:   []gatewayv1b1.ReferenceGrantTo{{Group: "", Kind: "Service", Name: ptrTo(gatewayv1b1.ObjectName(service.Name))}},
			},
		}
		Expect(k8sClient.Create(ctx, grant)).To(Succeed())
		route := createHTTPRoute(ctx, k8sClient, testID("maintenance-route"), namespace.Name, gateway.Name, []string{hostname}, service.Name, 8080)
		Eventually(ctx, func() error {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(route), route); err != nil {
				return err
			}
			route.Spec.Rules[0].BackendRefs[0].Namespace = ptrTo(gatewayv1.Namespace(backendNamespace.Name))
			return k8sClient.Update(ctx, route)
		}, ShortTimeout, DefaultInterval).Should(Succeed())
		serviceURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:8080", service.Name, backendNamespace.Name)
		expectService := func(expected string) {
			Eventually(ctx, func(g Gomega) {
				config, err := getRawTunnelConfigurationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, tunnel.Status.TunnelID)
				g.Expect(err).NotTo(HaveOccurred())
				rule, found := findRawTunnelIngress(config, hostname)
				g.Expect(found).To(BeTrue())
				g.Expect(rule.Service).To(Equal(expected))
				if expected != serviceURL {
					for _, rule := range config.Config.Ingress {
						g.Expect(rule.Service).NotTo(Equal(serviceURL))
					}
				}
			}, 2*time.Minute, 3*time.Second).Should(Succeed())
		}
		expectService(serviceURL)

		By("Revoking only the grant and checking the actual remote forwarding rules")
		Expect(k8sClient.Delete(ctx, grant)).To(Succeed())
		expectService("http_status:500")
		By("Restoring the grant without touching the route or tunnel")
		grant.ResourceVersion, grant.UID = "", ""
		Expect(k8sClient.Create(ctx, grant)).To(Succeed())
		expectService(serviceURL)

		By("Changing only an annotation and verifying the SDK-unknown h2c field remotely")
		updateHTTPRouteAnnotations(ctx, k8sClient, route.Name, route.Namespace, func(a map[string]string) { a["cfgate.io/origin-h2c"] = "true" })
		Eventually(ctx, func(g Gomega) {
			config, err := getRawTunnelConfigurationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, tunnel.Status.TunnelID)
			g.Expect(err).NotTo(HaveOccurred())
			rule, found := findRawTunnelIngress(config, hostname)
			g.Expect(found).To(BeTrue())
			value, present := rawOriginRequestBool(rule.OriginRequest, "h2cOrigin")
			g.Expect(present && value).To(BeTrue())
		}, 2*time.Minute, 3*time.Second).Should(Succeed())

		By("Publishing only the run-owned hostname and proving HTTP/2 reaches the origin")
		updateHTTPRouteAnnotations(ctx, k8sClient, route.Name, route.Namespace, func(a map[string]string) { a["cfgate.io/dns-sync"] = "maintenance" })
		dns := createCloudflareDNSWithGatewayRoutes(ctx, k8sClient, testID("maintenance-dns"), namespace.Name, tunnel.Name, []string{testEnv.CloudflareZoneName}, "cfgate.io/dns-sync=maintenance")
		zoneID, err := getZoneIDByName(ctx, cfClient, testEnv.CloudflareZoneName)
		Expect(err).NotTo(HaveOccurred())
		Eventually(ctx, func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dns), dns)).To(Succeed())
			g.Expect(meta.IsStatusConditionTrue(dns.Status.Conditions, "Ready")).To(BeTrue())
			var installation corev1.Namespace
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: e2eSystemNamespace()}, &installation)).To(Succeed())
			g.Expect(dns.Status.OwnerID).To(Equal(string(installation.UID) + "/" + string(dns.UID)))
			record, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, hostname, "CNAME")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(record).NotTo(BeNil())
			g.Expect(record.Comment).To(Equal("cfgate/owner=" + dns.Status.OwnerID))
			g.Expect(record.Content).To(Equal(tunnel.Status.TunnelDomain))
			g.Expect(record.Proxied).To(BeTrue())
			ownership, err := getDNSRecordFromCloudflare(ctx, cfClient, zoneID, "_cfgate."+hostname, "TXT")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(ownership).NotTo(BeNil())
			g.Expect(ownership.Content).To(ContainSubstring("cfgate/owner=" + dns.Status.OwnerID))
		}, LongTimeout, 3*time.Second).Should(Succeed())
		expectMaintenanceH2C(ctx, hostname, service.Name)

		By("Changing only this test tunnel's dashboard configuration")
		_, err = cfClient.ZeroTrust.Tunnels.Cloudflared.Configurations.Update(ctx, tunnel.Status.TunnelID, zero_trust.TunnelCloudflaredConfigurationUpdateParams{
			AccountID: cloudflare.F(testEnv.CloudflareAccountID),
			Config:    cloudflare.F(zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfig{Ingress: cloudflare.F([]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{{Service: cloudflare.F("http_status:418")}})}),
		})
		Expect(err).NotTo(HaveOccurred())
		config, err := getRawTunnelConfigurationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, tunnel.Status.TunnelID)
		Expect(err).NotTo(HaveOccurred())
		Expect(config.Config.Ingress).To(HaveLen(1))
		Expect(config.Config.Ingress[0].Service).To(Equal("http_status:418"))
		By("Expiring persisted lifecycle age without changing desired configuration or applied hash")
		Eventually(ctx, func(g Gomega) {
			// An already-running reconciliation can overwrite the injected age.
			// Keep expiring it until a full audit actually restores remote state.
			expireMaintenanceLifecycle(ctx, tunnel)
			config, err := getRawTunnelConfigurationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, tunnel.Status.TunnelID)
			g.Expect(err).NotTo(HaveOccurred())
			rule, found := findRawTunnelIngress(config, hostname)
			g.Expect(found).To(BeTrue())
			g.Expect(rule.Service).To(Equal(serviceURL))
			value, present := rawOriginRequestBool(rule.OriginRequest, "h2cOrigin")
			g.Expect(present && value).To(BeTrue(), "drift repair must preserve h2cOrigin")
		}, 2*time.Minute, 3*time.Second).Should(Succeed())
		expectMaintenanceH2C(ctx, hostname, service.Name)
	})

	It("withdraws required Access forwarding across policy changes and application deletion", SpecTimeout(12*time.Minute), func(ctx SpecContext) {
		skipIfNoZone()
		tunnel := createCloudflareTunnel(ctx, k8sClient, testID("required-access"), namespace.Name, testID("required-access-tunnel"))
		tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, LongTimeout)
		class := createGatewayClass(ctx, k8sClient, testID("required-access-class"))
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(context.Background(), class))).To(Succeed()) })
		gateway := createGateway(ctx, k8sClient, testID("required-access-gw"), namespace.Name, class.Name, namespace.Name+"/"+tunnel.Name)
		service := createTestService(ctx, k8sClient, testID("required-access-svc"), namespace.Name, 8080)
		hostname := testID("required-access-host") + "." + testEnv.CloudflareZoneName
		route := createHTTPRoute(ctx, k8sClient, testID("required-access-route"), namespace.Name, gateway.Name, []string{hostname}, service.Name, 8080)
		appName := testID("required-access-app")
		updateHTTPRouteAnnotations(ctx, k8sClient, route.Name, route.Namespace, func(a map[string]string) { a["cfgate.io/access-required"] = namespace.Name + "/" + appName })
		serviceURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:8080", service.Name, namespace.Name)
		expectService := func(expected string) {
			Eventually(ctx, func(g Gomega) {
				config, err := getRawTunnelConfigurationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, tunnel.Status.TunnelID)
				g.Expect(err).NotTo(HaveOccurred())
				rule, found := findRawTunnelIngress(config, hostname)
				g.Expect(found).To(BeTrue())
				g.Expect(rule.Service).To(Equal(expected))
				if expected == "http_status:503" {
					for _, rule := range config.Config.Ingress {
						g.Expect(rule.Service).NotTo(Equal(serviceURL))
					}
				}
			}, LongTimeout, 3*time.Second).Should(Succeed())
		}
		By("Requiring an absent application and observing actual remote denial")
		expectService("http_status:503")
		policy := createReusableAccessPolicy(ctx, k8sClient, testID("required-access-policy"), namespace.Name, "deny", []cfgatev1alpha1.AccessRule{{Everyone: ptrTo(true)}}, nil)
		policy = waitForAccessPolicyReady(ctx, k8sClient, policy.Name, policy.Namespace, LongTimeout)
		app := createCloudflareAccessApplication(ctx, k8sClient, appName, namespace.Name, route.Name, cfgatev1alpha1.AccessApplication{Name: appName}, cfgatev1alpha1.AccessPolicyReference{Name: policy.Name})
		app = waitForAccessApplicationReady(ctx, k8sClient, app.Name, app.Namespace, LongTimeout)
		appID := firstAccessApplicationID(app)
		remoteApp, err := getAccessApplicationByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, appID)
		Expect(err).NotTo(HaveOccurred())
		Expect(remoteApp).NotTo(BeNil())
		Expect(remoteApp.Domain).To(Equal(hostname))
		expectService(serviceURL)
		for _, decision := range []string{"bypass", "deny"} {
			By("Changing the selected reusable policy to " + decision)
			Eventually(ctx, func() error {
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy); err != nil {
					return err
				}
				policy.Spec.Decision = decision
				return k8sClient.Update(ctx, policy)
			}, ShortTimeout, DefaultInterval).Should(Succeed())
			Eventually(ctx, func(g Gomega) {
				remote, err := cfClient.ZeroTrust.Access.Policies.Get(ctx, policy.Status.PolicyID, zero_trust.AccessPolicyGetParams{AccountID: cloudflare.F(testEnv.CloudflareAccountID)})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(remote.Decision)).To(Equal(decision))
			}, LongTimeout, 3*time.Second).Should(Succeed())
			if decision == "bypass" {
				expectService("http_status:503")
			} else {
				expectService(serviceURL)
			}
		}
		By("Deleting only the selected run-owned application and confirming remote withdrawal")
		Expect(k8sClient.Delete(ctx, app)).To(Succeed())
		waitForAccessApplicationDeleted(ctx, k8sClient, app.Name, app.Namespace, LongTimeout)
		Eventually(ctx, func(g Gomega) {
			remote, err := getAccessApplicationByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, appID)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(remote).To(BeNil())
		}, LongTimeout, 3*time.Second).Should(Succeed())
		expectService("http_status:503")
	})

	It("reports incomplete rollout and replaces every connector after token Secret repair", SpecTimeout(12*time.Minute), func(ctx SpecContext) {
		tunnel := createCloudflareTunnel(ctx, k8sClient, testID("rotation"), namespace.Name, testID("rotation-tunnel"))
		Eventually(ctx, func() error {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(tunnel), tunnel); err != nil {
				return err
			}
			tunnel.Spec.Cloudflared.Replicas = 2
			tunnel.Spec.Cloudflared.Metrics.Enabled = ptrTo(false)
			return k8sClient.Update(ctx, tunnel)
		}, ShortTimeout, DefaultInterval).Should(Succeed())
		tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, LongTimeout)
		initial := maintenanceReadyPods(ctx, tunnel, 2)
		oldUIDs := map[types.UID]bool{}
		for _, pod := range initial {
			oldUIDs[pod.UID] = true
			Expect(pod.Spec.AutomountServiceAccountToken).To(Equal(ptrTo(false)))
			Expect(pod.Spec.Containers[0].ReadinessProbe).NotTo(BeNil())
			Expect(pod.Spec.Containers[0].LivenessProbe).NotTo(BeNil())
			for _, port := range pod.Spec.Containers[0].Ports {
				Expect(port.Name).NotTo(Equal("metrics"))
			}
		}
		var secret corev1.Secret
		secretKey := client.ObjectKey{Namespace: tunnel.Namespace, Name: cloudflared.TokenSecretName(tunnel.Name)}
		Expect(k8sClient.Get(ctx, secretKey, &secret)).To(Succeed())
		oldRevision := secret.ResourceVersion

		By("Holding new replicas unschedulable while existing connectors remain available")
		Eventually(ctx, func() error {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(tunnel), tunnel); err != nil {
				return err
			}
			tunnel.Spec.Cloudflared.Replicas = 3
			tunnel.Spec.Cloudflared.NodeSelector = map[string]string{"cfgate.io/e2e-unavailable": testID("absent-node")}
			return k8sClient.Update(ctx, tunnel)
		}, ShortTimeout, DefaultInterval).Should(Succeed())
		Eventually(ctx, func(g Gomega) {
			var deployment appsv1.Deployment
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: tunnel.Namespace, Name: cloudflared.DeploymentName(tunnel.Name)}, &deployment)).To(Succeed())
			g.Expect(deployment.Status.ObservedGeneration).To(BeNumerically(">=", deployment.Generation))
			g.Expect(deployment.Status.ReadyReplicas).To(BeNumerically(">", 0))
			g.Expect(deployment.Status.ReadyReplicas).To(BeNumerically("<", 3))
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tunnel), tunnel)).To(Succeed())
			g.Expect(meta.IsStatusConditionFalse(tunnel.Status.Conditions, "Ready")).To(BeTrue())
		}, LongTimeout, DefaultInterval).Should(Succeed())

		By("Restoring a stable spec before testing a Secret-only rollout")
		Eventually(ctx, func() error {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(tunnel), tunnel); err != nil {
				return err
			}
			tunnel.Spec.Cloudflared.NodeSelector = nil
			tunnel.Spec.Cloudflared.Replicas = 2
			return k8sClient.Update(ctx, tunnel)
		}, ShortTimeout, DefaultInterval).Should(Succeed())
		waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, LongTimeout)
		oldUIDs = map[types.UID]bool{}
		for _, pod := range maintenanceReadyPods(ctx, tunnel, 2) {
			oldUIDs[pod.UID] = true
		}
		Expect(k8sClient.Get(ctx, secretKey, &secret)).To(Succeed())
		oldRevision = secret.ResourceVersion
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tunnel), tunnel)).To(Succeed())
		stableGeneration := tunnel.Generation
		By("Repairing a damaged run-owned connector token Secret without logging credential data")
		Eventually(ctx, func() error {
			if err := k8sClient.Get(ctx, secretKey, &secret); err != nil {
				return err
			}
			secret.Data[cloudflared.TokenSecretKey] = []byte("e2e-invalid-token")
			return k8sClient.Update(ctx, &secret)
		}, ShortTimeout, DefaultInterval).Should(Succeed())
		Eventually(ctx, func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, secretKey, &secret)).To(Succeed())
			g.Expect(secret.ResourceVersion).NotTo(Equal(oldRevision))
			g.Expect(len(secret.Data[cloudflared.TokenSecretKey]) > 0 && string(secret.Data[cloudflared.TokenSecretKey]) != "e2e-invalid-token").To(BeTrue())
		}, DefaultTimeout, DefaultInterval).Should(Succeed())
		waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, LongTimeout)
		Eventually(ctx, func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, secretKey, &secret)).To(Succeed())
			var pods corev1.PodList
			g.Expect(k8sClient.List(ctx, &pods, client.InNamespace(tunnel.Namespace), client.MatchingLabels(cloudflared.Selector(tunnel.Name)))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(2))
			for _, pod := range pods.Items {
				g.Expect(oldUIDs[pod.UID]).To(BeFalse(), "old connector must not retain the previous environment")
				g.Expect(pod.Annotations["cfgate.io/tunnel-token-revision"]).To(Equal(string(secret.UID) + "/" + secret.ResourceVersion))
				g.Expect(maintenancePodReady(&pod)).To(BeTrue())
			}
		}, LongTimeout, DefaultInterval).Should(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tunnel), tunnel)).To(Succeed())
		Expect(tunnel.Generation).To(Equal(stableGeneration), "token rollout must not depend on a spec change")
		By("Repairing a missing Deployment without waiting for the full lifecycle interval")
		var previous appsv1.Deployment
		deploymentKey := client.ObjectKey{Namespace: tunnel.Namespace, Name: cloudflared.DeploymentName(tunnel.Name)}
		Expect(k8sClient.Get(ctx, deploymentKey, &previous)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &previous)).To(Succeed())
		Eventually(ctx, func(g Gomega) {
			var repaired appsv1.Deployment
			g.Expect(k8sClient.Get(ctx, deploymentKey, &repaired)).To(Succeed())
			g.Expect(repaired.UID).NotTo(Equal(previous.UID))
			g.Expect(repaired.Status.ObservedGeneration).To(BeNumerically(">=", repaired.Generation))
			g.Expect(repaired.Status.ReadyReplicas).To(Equal(int32(2)))
		}, 2*time.Minute, DefaultInterval).Should(Succeed())
		waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, LongTimeout)
	})

	It("retains the owned remote tunnel while connector drain remains unresolved", SpecTimeout(12*time.Minute), func(ctx SpecContext) {
		tunnel := createCloudflareTunnel(ctx, k8sClient, testID("drain"), namespace.Name, testID("drain-tunnel"))
		tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, LongTimeout)
		maintenanceReadyPods(ctx, tunnel, 1)
		const hold = "e2e.cfgate.io/hold-drain"
		// Keep a real running connector alive during deletion. An unscheduled
		// terminating Pod can be marked Failed by Kubernetes' Pod GC.
		var deployment appsv1.Deployment
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: tunnel.Namespace, Name: cloudflared.DeploymentName(tunnel.Name)}, &deployment)).To(Succeed())
		pod := corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: testID("held-connector"), Namespace: tunnel.Namespace,
				Labels: cloudflared.Selector(tunnel.Name), Finalizers: []string{hold},
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(tunnel, cfgatev1alpha1.GroupVersion.WithKind("CloudflareTunnel"))},
			},
			Spec: *deployment.Spec.Template.Spec.DeepCopy(),
		}
		pod.Spec.TerminationGracePeriodSeconds = ptrTo(int64(180))
		pod.Spec.Containers[0].Lifecycle = &corev1.Lifecycle{PreStop: &corev1.LifecycleHandler{Sleep: &corev1.SleepAction{Seconds: 120}}}
		Expect(k8sClient.Create(ctx, &pod)).To(Succeed())
		releaseHold := func(cleanupCtx context.Context) {
			Expect(client.IgnoreNotFound(k8sClient.Delete(cleanupCtx, &pod, client.GracePeriodSeconds(0)))).To(Succeed())
			Eventually(cleanupCtx, func() error {
				if err := k8sClient.Get(cleanupCtx, client.ObjectKeyFromObject(&pod), &pod); err != nil {
					return client.IgnoreNotFound(err)
				}
				kept := []string{}
				for _, finalizer := range pod.Finalizers {
					if finalizer != hold {
						kept = append(kept, finalizer)
					}
				}
				pod.Finalizers = kept
				return k8sClient.Update(cleanupCtx, &pod)
			}, ShortTimeout, DefaultInterval).Should(Succeed())
		}
		releaseHeldPod = func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), ShortTimeout)
			defer cancel()
			releaseHold(cleanupCtx)
		}
		Eventually(ctx, func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&pod), &pod)).To(Succeed())
			g.Expect(maintenancePodReady(&pod)).To(BeTrue())
		}, LongTimeout, DefaultInterval).Should(Succeed())
		Expect(k8sClient.Delete(ctx, &pod)).To(Succeed())
		Expect(k8sClient.Delete(ctx, tunnel)).To(Succeed())
		Eventually(ctx, func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&pod), &pod)).To(Succeed())
			g.Expect(pod.DeletionTimestamp.IsZero()).To(BeFalse())
			var deployment appsv1.Deployment
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: tunnel.Namespace, Name: cloudflared.DeploymentName(tunnel.Name)}, &deployment)).To(Succeed())
			g.Expect(*deployment.Spec.Replicas).To(BeZero())
			g.Expect(deployment.Status.ObservedGeneration).To(BeNumerically(">=", deployment.Generation))
			g.Expect(deployment.Status.Replicas).To(BeZero())
			g.Expect(pod.Status.Phase).NotTo(BeElementOf(corev1.PodSucceeded, corev1.PodFailed))
		}, DefaultTimeout, DefaultInterval).Should(Succeed())
		Consistently(ctx, func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&pod), &pod)).To(Succeed())
			g.Expect(pod.Status.Phase).NotTo(BeElementOf(corev1.PodSucceeded, corev1.PodFailed))
			remote, err := getTunnelByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, tunnel.Status.TunnelID)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(remote).NotTo(BeNil())
		}, 15*time.Second, 3*time.Second).Should(Succeed())
		releaseHold(ctx)
		waitForTunnelDeleted(ctx, k8sClient, tunnel.Name, tunnel.Namespace, LongTimeout)
		waitForTunnelDeletedByIDFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, tunnel.Status.TunnelID, LongTimeout)
	})
})

func expireMaintenanceLifecycle(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel) {
	Eventually(ctx, func() error {
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(tunnel), tunnel); err != nil {
			return err
		}
		aged := metav1.NewTime(time.Now().Add(-time.Hour))
		tunnel.Status.LastFullReconcileTime = &aged
		return k8sClient.Status().Update(ctx, tunnel)
	}, ShortTimeout, DefaultInterval).Should(Succeed())
	// Status-only changes deliberately do not enqueue the controller. A harmless
	// watched annotation starts reconciliation without changing rendered inputs
	// or the applied hash; the persisted age selects the full lifecycle path.
	Eventually(ctx, func() error {
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(tunnel), tunnel); err != nil {
			return err
		}
		if tunnel.Annotations == nil {
			tunnel.Annotations = map[string]string{}
		}
		tunnel.Annotations["cfgate.io/e2e-reconcile"] = fmt.Sprint(time.Now().UnixNano())
		return k8sClient.Update(ctx, tunnel)
	}, ShortTimeout, DefaultInterval).Should(Succeed())
}

func maintenanceReadyPods(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel, count int) []corev1.Pod {
	var pods corev1.PodList
	Eventually(ctx, func(g Gomega) {
		g.Expect(k8sClient.List(ctx, &pods, client.InNamespace(tunnel.Namespace), client.MatchingLabels(cloudflared.Selector(tunnel.Name)))).To(Succeed())
		g.Expect(pods.Items).To(HaveLen(count))
		for _, pod := range pods.Items {
			g.Expect(maintenancePodReady(&pod)).To(BeTrue())
		}
	}, LongTimeout, DefaultInterval).Should(Succeed())
	return pods.Items
}

func maintenancePodReady(pod *corev1.Pod) bool {
	if !pod.DeletionTimestamp.IsZero() {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// The fixture reports the protocol received at the origin, not the protocol
// negotiated by the public client with Cloudflare. It makes no Access, QUIC,
// gRPC trailer or edge-atomicity claim.
const maintenanceOriginSource = `package main
import ("fmt"; "net"; "net/http"; "os"; "crypto/tls")
func main() {
 p := new(http.Protocols)
 p.SetHTTP1(true)
 p.SetUnencryptedHTTP2(true)
 server := &http.Server{Addr: ":8080", Protocols: p, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  w.Header().Set("Cache-Control", "no-store")
  w.Header().Set("X-Cfgate-Origin-Protocol", r.Proto)
  fmt.Fprint(w, os.Getenv("ORIGIN_MARKER"))
 })}
 address := os.Getenv("ORIGIN_ADDRESS")
 if address == "" { address = server.Addr }
 listener, err := net.Listen("tcp", address)
 if err != nil { panic(err) }
 fmt.Println(listener.Addr().String())
 if os.Getenv("ORIGIN_TLS")!="" {
  server.SetKeepAlivesEnabled(false)
  listener=tls.NewListener(listener,&tls.Config{MinVersion:tls.VersionTLS12,GetCertificate:func(*tls.ClientHelloInfo)(*tls.Certificate,error){cert,err:=tls.LoadX509KeyPair("/tls/tls.crt","/tls/tls.key");return &cert,err}})
 }
 if err := server.Serve(listener); err != nil { panic(err) }
}
`

func maintenanceKindClusterName(ctx context.Context) string {
	name := testEnv.KindClusterName
	if testEnv.UseExistingCluster {
		name = os.Getenv("CLUSTER_NAME")
	}
	Expect(name).NotTo(BeEmpty(), "origin fixture requires an explicitly selected kind cluster")
	verifySelectedKindCluster(ctx, name, cfg)
	return name
}

func deployMaintenanceOrigin(ctx context.Context, service *corev1.Service, tlsSecret ...string) {
	clusterName := maintenanceKindClusterName(ctx)
	var nodes corev1.NodeList
	Expect(k8sClient.List(ctx, &nodes)).To(Succeed())
	Expect(nodes.Items).NotTo(BeEmpty())
	arch := nodes.Items[0].Status.NodeInfo.Architecture
	Expect(arch).To(BeElementOf("amd64", "arm64"))
	for _, node := range nodes.Items {
		Expect(node.Status.NodeInfo.Architecture).To(Equal(arch), "fixture requires homogeneous kind architecture")
	}
	directory, err := os.MkdirTemp("", "cfgate-e2e-h2c-")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { Expect(os.RemoveAll(directory)).To(Succeed()) })
	Expect(os.WriteFile(filepath.Join(directory, "main.go"), []byte(maintenanceOriginSource), 0600)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(directory, "Dockerfile"), []byte("FROM scratch\nCOPY origin /origin\nUSER 65532:65532\nENTRYPOINT [\"/origin\"]\n"), 0600)).To(Succeed())
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", filepath.Join(directory, "origin"), filepath.Join(directory, "main.go"))
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	build.Stdout, build.Stderr = GinkgoWriter, GinkgoWriter
	Expect(build.Run()).To(Succeed())
	image := "cfgate-e2e-origin:" + testID("h2c")
	run := func(command string, args ...string) {
		cmd := exec.CommandContext(ctx, command, args...)
		cmd.Stdout, cmd.Stderr = GinkgoWriter, GinkgoWriter
		Expect(cmd.Run()).To(Succeed())
	}
	run("docker", "build", "--platform", "linux/"+arch, "--tag", image, directory)
	DeferCleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(cleanupCtx, "docker", "image", "rm", image)
		cmd.Stdout, cmd.Stderr = GinkgoWriter, GinkgoWriter
		Expect(cmd.Run()).To(Succeed())
	})
	run("kind", "load", "docker-image", "--name", clusterName, image)
	origin := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "h2c-origin", Namespace: service.Namespace, Labels: service.Spec.Selector},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: ptrTo(false),
			SecurityContext:              &corev1.PodSecurityContext{RunAsNonRoot: ptrTo(true), RunAsUser: ptrTo(int64(65532)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{{
				Name: "origin", Image: image, ImagePullPolicy: corev1.PullNever,
				Env:             []corev1.EnvVar{{Name: "ORIGIN_MARKER", Value: service.Name}},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptrTo(false), ReadOnlyRootFilesystem: ptrTo(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				ReadinessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromInt32(8080)}}},
			}},
		},
	}
	if len(tlsSecret) > 0 {
		origin.Spec.Volumes = []corev1.Volume{{Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: tlsSecret[0]}}}}
		container := &origin.Spec.Containers[0]
		container.Env = append(container.Env, corev1.EnvVar{Name: "ORIGIN_TLS", Value: "true"})
		container.VolumeMounts = []corev1.VolumeMount{{Name: "tls", MountPath: "/tls", ReadOnly: true}}
		container.ReadinessProbe.HTTPGet.Scheme = corev1.URISchemeHTTPS
	}
	Expect(k8sClient.Create(ctx, origin)).To(Succeed())
	Eventually(ctx, func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origin), origin)).To(Succeed())
		g.Expect(maintenancePodReady(origin)).To(BeTrue())
	}, LongTimeout, DefaultInterval).Should(Succeed())
}

func expectMaintenanceH2C(ctx context.Context, hostname, marker string) {
	expectMaintenanceResponse(ctx, hostname, marker, "HTTP/2.0", http.StatusOK)
}

func newMaintenanceHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if address := os.Getenv("E2E_PUBLIC_DNS_RESOLVER"); address != "" {
		_, _, err := net.SplitHostPort(address)
		Expect(err).NotTo(HaveOccurred(), "E2E_PUBLIC_DNS_RESOLVER must be host:port")
		resolverDialer := &net.Dialer{Timeout: 5 * time.Second}
		dialer := &net.Dialer{Timeout: 15 * time.Second, Resolver: &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return resolverDialer.DialContext(ctx, network, address)
			},
		}}
		transport.DialContext = dialer.DialContext
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func expectMaintenanceResponse(ctx context.Context, hostname, marker, protocol string, statusCode int, headers ...http.Header) {
	httpClient := newMaintenanceHTTPClient()
	defer httpClient.CloseIdleConnections()
	Eventually(ctx, func(g Gomega) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+hostname+"/?run="+testRunID+"&nonce="+fmt.Sprint(time.Now().UnixNano()), nil)
		g.Expect(err).NotTo(HaveOccurred())
		if len(headers) > 0 {
			request.Header = headers[0].Clone()
		}
		response, err := httpClient.Do(request)
		g.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = response.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(response.StatusCode).To(Equal(statusCode))
		if statusCode == http.StatusOK {
			if protocol != "" {
				g.Expect(response.Header.Get("X-Cfgate-Origin-Protocol")).To(Equal(protocol))
			}
			g.Expect(strings.TrimSpace(string(body))).To(Equal(marker))
		}
	}, LongTimeout, 5*time.Second).Should(Succeed())
}

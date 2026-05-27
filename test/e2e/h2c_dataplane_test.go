package e2e_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
)

var _ = Describe("h2c data-plane smoke", Label("cloudflare", "h2c-data-plane", "user-run"), func() {
	It("proxies cleartext h2c to the backend", SpecTimeout(8*time.Minute), func() {
		if os.Getenv("E2E_H2C_DATAPLANE") != "true" {
			Skip("E2E_H2C_DATAPLANE=true is required")
		}
		skipIfNoZone()

		image := os.Getenv("E2E_H2C_BACKEND_IMAGE")
		if image == "" {
			image = "cfgate/h2c-echo:e2e"
		}

		namespace := createTestNamespace("cfgate-h2c-e2e")
		createCloudflareCredentialsSecret(namespace.Name)
		cfClient := getCloudflareClient()
		hostname := fmt.Sprintf("%s.%s", testID("h2c-data"), testEnv.CloudflareZoneName)

		DeferCleanup(func() {
			if testEnv.SkipCleanup {
				return
			}
			deleteTestNamespace(namespace)
		})

		tunnel := createCloudflareTunnel(ctx, k8sClient, testID("h2c-tunnel"), namespace.Name, testID("h2c-tunnel"))
		tunnel = waitForTunnelReady(ctx, k8sClient, tunnel.Name, tunnel.Namespace, DefaultTimeout)

		gatewayClass := createGatewayClass(ctx, k8sClient, testID("h2c-gc"))
		gateway := createGateway(ctx, k8sClient, testID("h2c-gw"), namespace.Name, gatewayClass.Name, namespace.Name+"/"+tunnel.Name)
		dnsName := testID("h2c-dns")
		createCloudflareDNSWithGatewayRoutes(ctx, k8sClient, dnsName, namespace.Name, tunnel.Name, []string{testEnv.CloudflareZoneName}, "cfgate.io/dns-sync=enabled")

		appName := testID("h2c-backend")
		createH2CBackendDeployment(ctx, namespace.Name, appName, image)
		createTestService(ctx, k8sClient, appName, namespace.Name, 8080)
		waitForH2CBackendReady(namespace.Name, appName)

		route := createHTTPRoute(ctx, k8sClient, testID("h2c-route"), namespace.Name, gateway.Name, []string{hostname}, appName, 8080)
		updateHTTPRouteAnnotations(ctx, k8sClient, route.Name, route.Namespace, func(annotations map[string]string) {
			annotations["cfgate.io/origin-h2c"] = "true"
			annotations["cfgate.io/dns-sync"] = "enabled"
		})

		waitForTunnelCondition(ctx, k8sClient, tunnel.Name, tunnel.Namespace, "ConfigurationSynced", metav1.ConditionTrue, DefaultTimeout)
		waitForDNSReady(ctx, k8sClient, dnsName, namespace.Name, DefaultTimeout)

		Eventually(func(g Gomega) {
			var current cfgatev1alpha1.CloudflareTunnel
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: tunnel.Name, Namespace: tunnel.Namespace}, &current)).To(Succeed())
			config, err := getRawTunnelConfigurationFromCloudflare(ctx, cfClient, testEnv.CloudflareAccountID, current.Status.TunnelID)
			g.Expect(err).NotTo(HaveOccurred())

			ingress, ok := findRawTunnelIngress(config, hostname)
			g.Expect(ok).To(BeTrue(), "expected ingress rule for %s", hostname)
			h2cOrigin, ok := rawOriginRequestBool(ingress.OriginRequest, "h2cOrigin")
			g.Expect(ok).To(BeTrue(), "expected h2cOrigin in originRequest")
			g.Expect(h2cOrigin).To(BeTrue())
		}, DefaultTimeout, DefaultInterval).Should(Succeed())

		httpClient := &http.Client{Timeout: 15 * time.Second}
		Eventually(func(g Gomega) {
			resp, err := httpClient.Get("https://" + hostname + "/")
			g.Expect(err).NotTo(HaveOccurred())
			defer func() {
				_ = resp.Body.Close()
			}()
			body, err := io.ReadAll(resp.Body)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(resp.StatusCode).To(BeNumerically(">=", 200))
			g.Expect(resp.StatusCode).To(BeNumerically("<", 500))
			g.Expect(strings.TrimSpace(string(body))).To(ContainSubstring("proto=HTTP/2.0"))
		}, 3*time.Minute, 10*time.Second).Should(Succeed())
	})
})

func createH2CBackendDeployment(ctx context.Context, namespace, name, image string) {
	labels := map[string]string{"app": name}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptrTo(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "h2c-echo",
						Image: image,
						Ports: []corev1.ContainerPort{{ContainerPort: 8080}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/",
									Port: intstr.FromInt32(8080),
								},
							},
							PeriodSeconds: 2,
						},
					}},
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, deployment)).To(Succeed())
}

func waitForH2CBackendReady(namespace, name string) {
	Eventually(func(g Gomega) {
		var deployment appsv1.Deployment
		g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &deployment)).To(Succeed())
		g.Expect(deployment.Status.ReadyReplicas).To(BeNumerically(">=", 1))
	}, DefaultTimeout, DefaultInterval).Should(Succeed())
}

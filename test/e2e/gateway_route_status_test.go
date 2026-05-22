// Package e2e contains end-to-end tests for cfgate.
package e2e_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
)

var _ = Describe("Gateway and HTTPRoute Status E2E", Label("cloudflare"), Ordered, func() {
	var (
		namespace    *corev1.Namespace
		sharedTunnel *cfgatev1alpha1.CloudflareTunnel
	)

	BeforeAll(func() {
		skipIfNoCredentials()

		namespace = createTestNamespace("cfgate-gateway-route-e2e")
		createCloudflareCredentialsSecret(namespace.Name)

		tunnelName := testID("gateway-status-tunnel")
		sharedTunnel = createCloudflareTunnel(ctx, k8sClient, testID("gateway-status"), namespace.Name, tunnelName)
		sharedTunnel = waitForTunnelReady(ctx, k8sClient, sharedTunnel.Name, sharedTunnel.Namespace, DefaultTimeout)

		DeferCleanup(func() {
			if testEnv.SkipCleanup {
				return
			}
			if namespace != nil {
				deleteTestNamespace(namespace)
			}
		})
	})

	Context("Gateway negative and status paths", func() {
		It("should set Accepted=False when a cfgate-managed Gateway is missing cfgate.io/tunnel-ref", SpecTimeout(3*time.Minute), func(ctx SpecContext) {
			gatewayClassName := testID("gc")
			createGatewayClass(ctx, k8sClient, gatewayClassName)

			gateway := &gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("missing-tunnel-ref"),
					Namespace: namespace.Name,
				},
				Spec: gatewayv1.GatewaySpec{
					GatewayClassName: gatewayv1.ObjectName(gatewayClassName),
					Listeners: []gatewayv1.Listener{
						{
							Name:     "https",
							Protocol: gatewayv1.HTTPSProtocolType,
							Port:     443,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, gateway)).To(Succeed())

			gateway = waitForGatewayCondition(ctx, k8sClient, gateway.Name, gateway.Namespace, string(gatewayv1.GatewayConditionAccepted), metav1.ConditionFalse, DefaultTimeout)
			Expect(findCondition(gateway.Status.Conditions, string(gatewayv1.GatewayConditionAccepted))).NotTo(BeNil())
			Expect(findCondition(gateway.Status.Conditions, string(gatewayv1.GatewayConditionAccepted)).Reason).To(Equal("MissingTunnelRef"))
			Expect(gateway.Status.Addresses).To(BeEmpty())
		})

		It("should set Accepted=False and emit TunnelNotFound when cfgate.io/tunnel-ref is malformed", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			gatewayClassName := testID("gc")
			createGatewayClass(ctx, k8sClient, gatewayClassName)

			gateway := &gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("malformed-tunnel-ref"),
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/tunnel-ref": "bad/ref/format",
					},
				},
				Spec: gatewayv1.GatewaySpec{
					GatewayClassName: gatewayv1.ObjectName(gatewayClassName),
					Listeners: []gatewayv1.Listener{
						{
							Name:     "https",
							Protocol: gatewayv1.HTTPSProtocolType,
							Port:     443,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, gateway)).To(Succeed())

			gateway = waitForGatewayCondition(ctx, k8sClient, gateway.Name, gateway.Namespace, string(gatewayv1.GatewayConditionAccepted), metav1.ConditionFalse, DefaultTimeout)
			Expect(findCondition(gateway.Status.Conditions, string(gatewayv1.GatewayConditionAccepted))).NotTo(BeNil())
			Expect(findCondition(gateway.Status.Conditions, string(gatewayv1.GatewayConditionAccepted)).Reason).To(Equal("TunnelNotFound"))
			Expect(findCondition(gateway.Status.Conditions, string(gatewayv1.GatewayConditionAccepted)).Message).To(ContainSubstring("invalid tunnel reference"))
			Expect(gateway.Status.Addresses).To(BeEmpty())

			event := waitForEventReason(ctx, namespace.Name, gateway.Name, "Gateway", "TunnelNotFound", corev1.EventTypeWarning, DefaultTimeout)
			Expect(event.Message).To(ContainSubstring("invalid tunnel reference"))

			Consistently(func(g Gomega) {
				var current gatewayv1.Gateway
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: gateway.Name, Namespace: gateway.Namespace}, &current)).To(Succeed())
				g.Expect(findCondition(current.Status.Conditions, string(gatewayv1.GatewayConditionAccepted))).NotTo(BeNil())
				g.Expect(findCondition(current.Status.Conditions, string(gatewayv1.GatewayConditionAccepted)).Status).To(Equal(metav1.ConditionFalse))
				g.Expect(current.Status.Addresses).To(BeEmpty())
			}, ShortTimeout, DefaultInterval).Should(Succeed())
		})

		It("should leave Gateways using non-cfgate GatewayClasses untouched", SpecTimeout(3*time.Minute), func(ctx SpecContext) {
			otherGatewayClassName := testID("other-gc")
			createGatewayClassWithController(ctx, k8sClient, otherGatewayClassName, "example.com/other-controller")

			gateway := &gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("foreign-gw"),
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/tunnel-ref": fmt.Sprintf("%s/%s", namespace.Name, sharedTunnel.Name),
					},
				},
				Spec: gatewayv1.GatewaySpec{
					GatewayClassName: gatewayv1.ObjectName(otherGatewayClassName),
					Listeners: []gatewayv1.Listener{
						{
							Name:     "https",
							Protocol: gatewayv1.HTTPSProtocolType,
							Port:     443,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, gateway)).To(Succeed())

			Consistently(func(g Gomega) {
				var current gatewayv1.Gateway
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: gateway.Name, Namespace: gateway.Namespace}, &current)).To(Succeed())
				g.Expect(current.Status.Addresses).To(BeEmpty())

				accepted := findCondition(current.Status.Conditions, string(gatewayv1.GatewayConditionAccepted))
				if accepted != nil {
					g.Expect(accepted.Status).To(Equal(metav1.ConditionUnknown))
					g.Expect(accepted.Reason).To(Equal("Pending"))
					g.Expect(accepted.Message).To(ContainSubstring("Waiting for controller"))
				}
			}, ShortTimeout, DefaultInterval).Should(Succeed())
		})

		It("should report the correct AttachedRoutes count for multiple HTTPRoutes on one Gateway", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			gatewayClassName := testID("gc")
			createGatewayClass(ctx, k8sClient, gatewayClassName)

			gatewayName := testID("attached-routes-gw")
			createGateway(ctx, k8sClient, gatewayName, namespace.Name, gatewayClassName, fmt.Sprintf("%s/%s", namespace.Name, sharedTunnel.Name))

			serviceOne := createTestService(ctx, k8sClient, testID("svc-one"), namespace.Name, 8080)
			serviceTwo := createTestService(ctx, k8sClient, testID("svc-two"), namespace.Name, 8080)
			createHTTPRoute(ctx, k8sClient, testID("route-one"), namespace.Name, gatewayName, []string{fmt.Sprintf("%s.%s", testID("attached-one"), "example.test")}, serviceOne.Name, 8080)
			createHTTPRoute(ctx, k8sClient, testID("route-two"), namespace.Name, gatewayName, []string{fmt.Sprintf("%s.%s", testID("attached-two"), "example.test")}, serviceTwo.Name, 8080)

			Eventually(func(g Gomega) {
				var current gatewayv1.Gateway
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: gatewayName, Namespace: namespace.Name}, &current)).To(Succeed())
				g.Expect(current.Status.Listeners).NotTo(BeEmpty())
				g.Expect(current.Status.Listeners[0].AttachedRoutes).To(Equal(int32(2)))
			}, DefaultTimeout, DefaultInterval).Should(Succeed())
		})
	})

	Context("HTTPRoute negative paths", func() {
		It("should set ResolvedRefs=False when a backend Service does not exist", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			gatewayClassName := testID("gc")
			createGatewayClass(ctx, k8sClient, gatewayClassName)
			gatewayName := testID("missing-backend-gw")
			createGateway(ctx, k8sClient, gatewayName, namespace.Name, gatewayClassName, fmt.Sprintf("%s/%s", namespace.Name, sharedTunnel.Name))

			route := createHTTPRoute(ctx, k8sClient, testID("missing-backend-route"), namespace.Name, gatewayName, []string{fmt.Sprintf("%s.example.test", testID("missing-backend"))}, "service-does-not-exist", 8080)
			route = waitForHTTPRouteParentCondition(ctx, k8sClient, route.Name, route.Namespace, namespace.Name, gatewayName, string(gatewayv1.RouteConditionResolvedRefs), metav1.ConditionFalse, DefaultTimeout)

			var cfgateParent *gatewayv1.RouteParentStatus
			for i := range route.Status.Parents {
				if string(route.Status.Parents[i].ControllerName) == "cfgate.io/cloudflare-tunnel-controller" {
					cfgateParent = &route.Status.Parents[i]
					break
				}
			}
			Expect(cfgateParent).NotTo(BeNil())
			Expect(findCondition(cfgateParent.Conditions, string(gatewayv1.RouteConditionResolvedRefs))).NotTo(BeNil())
			Expect(findCondition(cfgateParent.Conditions, string(gatewayv1.RouteConditionResolvedRefs)).Reason).To(Equal("BackendNotFound"))
		})

		It("should set ResolvedRefs=False when one rule has multiple Service backendRefs", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			gatewayClassName := testID("gc")
			createGatewayClass(ctx, k8sClient, gatewayClassName)
			gatewayName := testID("multi-backend-gw")
			createGateway(ctx, k8sClient, gatewayName, namespace.Name, gatewayClassName, fmt.Sprintf("%s/%s", namespace.Name, sharedTunnel.Name))

			serviceOne := createTestService(ctx, k8sClient, testID("svc-one"), namespace.Name, 8080)
			serviceTwo := createTestService(ctx, k8sClient, testID("svc-two"), namespace.Name, 8080)
			parentNamespace := gatewayv1.Namespace(namespace.Name)
			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("multi-backend-route"),
					Namespace: namespace.Name,
				},
				Spec: gatewayv1.HTTPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{{
							Name:      gatewayv1.ObjectName(gatewayName),
							Namespace: &parentNamespace,
						}},
					},
					Hostnames: []gatewayv1.Hostname{
						gatewayv1.Hostname(fmt.Sprintf("%s.example.test", testID("multi-backend"))),
					},
					Rules: []gatewayv1.HTTPRouteRule{{
						BackendRefs: []gatewayv1.HTTPBackendRef{
							{
								BackendRef: gatewayv1.BackendRef{
									BackendObjectReference: gatewayv1.BackendObjectReference{
										Name: gatewayv1.ObjectName(serviceOne.Name),
										Port: ptrTo(gatewayv1.PortNumber(8080)),
									},
								},
							},
							{
								BackendRef: gatewayv1.BackendRef{
									BackendObjectReference: gatewayv1.BackendObjectReference{
										Name: gatewayv1.ObjectName(serviceTwo.Name),
										Port: ptrTo(gatewayv1.PortNumber(8080)),
									},
								},
							},
						},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, route)).To(Succeed())

			route = waitForHTTPRouteParentCondition(ctx, k8sClient, route.Name, route.Namespace, namespace.Name, gatewayName, string(gatewayv1.RouteConditionResolvedRefs), metav1.ConditionFalse, DefaultTimeout)
			cfgateParent := findCfgateRouteParent(route)
			Expect(cfgateParent).NotTo(BeNil())
			condition := findCondition(cfgateParent.Conditions, string(gatewayv1.RouteConditionResolvedRefs))
			Expect(condition).NotTo(BeNil())
			Expect(condition.Reason).To(Equal("UnsupportedValue"))
			Expect(condition.Message).To(ContainSubstring("multiple backendRefs are not supported by cfgate tunnel ingress"))
		})

		It("should set ResolvedRefs=False when backend kind or group is unsupported", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			gatewayClassName := testID("gc")
			createGatewayClass(ctx, k8sClient, gatewayClassName)
			gatewayName := testID("unsupported-backend-gw")
			createGateway(ctx, k8sClient, gatewayName, namespace.Name, gatewayClassName, fmt.Sprintf("%s/%s", namespace.Name, sharedTunnel.Name))

			exampleGroup := gatewayv1.Group("example.com")
			configMapKind := gatewayv1.Kind("ConfigMap")
			cases := []struct {
				name        string
				group       *gatewayv1.Group
				kind        *gatewayv1.Kind
				wantMessage string
			}{
				{name: "backend-group", group: &exampleGroup, wantMessage: "unsupported backend group"},
				{name: "backend-kind", kind: &configMapKind, wantMessage: "unsupported backend kind"},
			}

			for _, tc := range cases {
				parentNamespace := gatewayv1.Namespace(namespace.Name)
				route := &gatewayv1.HTTPRoute{
					ObjectMeta: metav1.ObjectMeta{
						Name:      testID(tc.name),
						Namespace: namespace.Name,
					},
					Spec: gatewayv1.HTTPRouteSpec{
						CommonRouteSpec: gatewayv1.CommonRouteSpec{
							ParentRefs: []gatewayv1.ParentReference{{
								Name:      gatewayv1.ObjectName(gatewayName),
								Namespace: &parentNamespace,
							}},
						},
						Hostnames: []gatewayv1.Hostname{
							gatewayv1.Hostname(fmt.Sprintf("%s.example.test", testID(tc.name))),
						},
						Rules: []gatewayv1.HTTPRouteRule{{
							BackendRefs: []gatewayv1.HTTPBackendRef{{
								BackendRef: gatewayv1.BackendRef{
									BackendObjectReference: gatewayv1.BackendObjectReference{
										Group: tc.group,
										Kind:  tc.kind,
										Name:  "svc",
										Port:  ptrTo(gatewayv1.PortNumber(8080)),
									},
								},
							}},
						}},
					},
				}
				Expect(k8sClient.Create(ctx, route)).To(Succeed())

				route = waitForHTTPRouteParentCondition(ctx, k8sClient, route.Name, route.Namespace, namespace.Name, gatewayName, string(gatewayv1.RouteConditionResolvedRefs), metav1.ConditionFalse, DefaultTimeout)
				cfgateParent := findCfgateRouteParent(route)
				Expect(cfgateParent).NotTo(BeNil())
				condition := findCondition(cfgateParent.Conditions, string(gatewayv1.RouteConditionResolvedRefs))
				Expect(condition).NotTo(BeNil())
				Expect(condition.Reason).To(Equal("UnsupportedValue"))
				Expect(condition.Message).To(ContainSubstring(tc.wantMessage))
			}
		})

		It("should set Accepted=False when a cross-namespace parent attachment is not allowed by listener policy", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			otherNamespace := createTestNamespace("cfgate-httproute-other")
			DeferCleanup(func() {
				if testEnv.SkipCleanup {
					return
				}
				deleteTestNamespace(otherNamespace)
			})

			gatewayClassName := testID("gc")
			createGatewayClass(ctx, k8sClient, gatewayClassName)
			gatewayName := testID("restricted-gw")

			fromSame := gatewayv1.NamespacesFromSame
			gateway := &gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      gatewayName,
					Namespace: namespace.Name,
					Annotations: map[string]string{
						"cfgate.io/tunnel-ref": fmt.Sprintf("%s/%s", namespace.Name, sharedTunnel.Name),
					},
				},
				Spec: gatewayv1.GatewaySpec{
					GatewayClassName: gatewayv1.ObjectName(gatewayClassName),
					Listeners: []gatewayv1.Listener{
						{
							Name:     "https",
							Protocol: gatewayv1.HTTPSProtocolType,
							Port:     443,
							AllowedRoutes: &gatewayv1.AllowedRoutes{
								Namespaces: &gatewayv1.RouteNamespaces{
									From: &fromSame,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, gateway)).To(Succeed())

			service := createTestService(ctx, k8sClient, testID("svc"), otherNamespace.Name, 8080)
			parentNamespace := gatewayv1.Namespace(namespace.Name)
			sectionName := gatewayv1.SectionName("https")
			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testID("crossns-route"),
					Namespace: otherNamespace.Name,
				},
				Spec: gatewayv1.HTTPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{
							{
								Name:        gatewayv1.ObjectName(gatewayName),
								Namespace:   &parentNamespace,
								SectionName: &sectionName,
							},
						},
					},
					Hostnames: []gatewayv1.Hostname{
						gatewayv1.Hostname(fmt.Sprintf("%s.example.test", testID("crossns"))),
					},
					Rules: []gatewayv1.HTTPRouteRule{
						{
							BackendRefs: []gatewayv1.HTTPBackendRef{
								{
									BackendRef: gatewayv1.BackendRef{
										BackendObjectReference: gatewayv1.BackendObjectReference{
											Name: gatewayv1.ObjectName(service.Name),
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

			route = waitForHTTPRouteParentCondition(ctx, k8sClient, route.Name, route.Namespace, namespace.Name, gatewayName, string(gatewayv1.RouteConditionAccepted), metav1.ConditionFalse, DefaultTimeout)

			var cfgateParent *gatewayv1.RouteParentStatus
			for i := range route.Status.Parents {
				if string(route.Status.Parents[i].ControllerName) == "cfgate.io/cloudflare-tunnel-controller" {
					cfgateParent = &route.Status.Parents[i]
					break
				}
			}
			Expect(cfgateParent).NotTo(BeNil())
			Expect(findCondition(cfgateParent.Conditions, string(gatewayv1.RouteConditionAccepted))).NotTo(BeNil())
			Expect(findCondition(cfgateParent.Conditions, string(gatewayv1.RouteConditionAccepted)).Reason).To(Equal("NotAllowedByListeners"))
		})

		It("should set AccessPolicyResolved=False when cfgate.io/access-policy points to a nonexistent policy", SpecTimeout(5*time.Minute), func(ctx SpecContext) {
			gatewayClassName := testID("gc")
			createGatewayClass(ctx, k8sClient, gatewayClassName)
			gatewayName := testID("access-policy-gw")
			createGateway(ctx, k8sClient, gatewayName, namespace.Name, gatewayClassName, fmt.Sprintf("%s/%s", namespace.Name, sharedTunnel.Name))

			service := createTestService(ctx, k8sClient, testID("svc"), namespace.Name, 8080)
			route := createHTTPRoute(ctx, k8sClient, testID("missing-policy-route"), namespace.Name, gatewayName, []string{fmt.Sprintf("%s.example.test", testID("missing-policy"))}, service.Name, 8080)
			route = updateHTTPRouteAnnotations(ctx, k8sClient, route.Name, route.Namespace, func(annotations map[string]string) {
				annotations["cfgate.io/access-policy"] = "does-not-exist"
			})

			route = waitForHTTPRouteParentCondition(ctx, k8sClient, route.Name, route.Namespace, namespace.Name, gatewayName, "AccessPolicyResolved", metav1.ConditionFalse, DefaultTimeout)

			var cfgateParent *gatewayv1.RouteParentStatus
			for i := range route.Status.Parents {
				if string(route.Status.Parents[i].ControllerName) == "cfgate.io/cloudflare-tunnel-controller" {
					cfgateParent = &route.Status.Parents[i]
					break
				}
			}
			Expect(cfgateParent).NotTo(BeNil())
			Expect(findCondition(cfgateParent.Conditions, "AccessPolicyResolved")).NotTo(BeNil())
			Expect(findCondition(cfgateParent.Conditions, "AccessPolicyResolved").Reason).To(Equal("AccessPolicyNotFound"))
		})
	})
})

func findCfgateRouteParent(route *gatewayv1.HTTPRoute) *gatewayv1.RouteParentStatus {
	for i := range route.Status.Parents {
		if string(route.Status.Parents[i].ControllerName) == "cfgate.io/cloudflare-tunnel-controller" {
			return &route.Status.Parents[i]
		}
	}
	return nil
}

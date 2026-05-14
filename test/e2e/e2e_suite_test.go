// Package e2e contains end-to-end tests for cfgate.
// These tests run against real Cloudflare API and real Kubernetes clusters.
// NO mocks, NO envtest - real APIs only.
package e2e_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	cloudflare "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/dns"
	"github.com/cloudflare/cloudflare-go/v6/option"
	"github.com/cloudflare/cloudflare-go/v6/zero_trust"
	"github.com/cloudflare/cloudflare-go/v6/zones"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlzap "sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	cfcloudflare "cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/cloudflared"
	"cfgate.io/cfgate/internal/controller"
	"cfgate.io/cfgate/internal/controller/features"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"
)

// Environment variables for E2E tests.
const (
	EnvCloudflareAPIToken  = "CLOUDFLARE_API_TOKEN"
	EnvCloudflareAccountID = "CLOUDFLARE_ACCOUNT_ID"
	EnvCloudflareZoneName  = "CLOUDFLARE_ZONE_NAME"
	EnvCloudflareIdPID     = "CLOUDFLARE_IDP_ID"
	EnvCloudflareTestEmail = "CLOUDFLARE_TEST_EMAIL"
	EnvCloudflareTestGroup = "CLOUDFLARE_TEST_GROUP"
	EnvSkipCleanup         = "E2E_SKIP_CLEANUP"
	EnvUseExistingCluster  = "E2E_USE_EXISTING_CLUSTER"
	EnvKubeconfig          = "KUBECONFIG"

	e2eFallbackCredentialsNamespace = "cfgate-e2e-system"
	e2eFallbackCredentialsSecret    = "cloudflare-credentials"
)

var (
	// testEnv holds E2E test environment configuration.
	testEnv *E2ETestEnv

	// k8sClient is the Kubernetes client for test operations.
	k8sClient client.Client

	// k8sClientset is the typed Kubernetes clientset.
	k8sClientset *kubernetes.Clientset

	// cfg is the Kubernetes REST config.
	cfg *rest.Config

	// scheme is the runtime scheme with all types registered.
	// Populated at init time to avoid data races with manager goroutines.
	scheme = runtime.NewScheme()

	// ctx is the test context.
	ctx context.Context

	// cancel is the context cancel function.
	cancel context.CancelFunc

	// projectRoot is the root directory of the cfgate project.
	projectRoot string

	// mgr is the controller manager running in-process.
	mgr manager.Manager

	// mgrCancel cancels the manager context.
	mgrCancel context.CancelFunc

	// featureGates tracks optional Gateway API CRD availability.
	featureGates *features.FeatureGates
)

// E2ETestEnv holds the E2E test environment configuration.
type E2ETestEnv struct {
	// CloudflareAPIToken is the Cloudflare API token.
	CloudflareAPIToken string

	// CloudflareAccountID is the Cloudflare account ID.
	CloudflareAccountID string

	// CloudflareZoneName is the Cloudflare zone for DNS tests.
	CloudflareZoneName string

	// CloudflareIdPID is the Cloudflare Identity Provider ID for IdP-dependent tests.
	CloudflareIdPID string

	// CloudflareTestEmail is a test email for email rule verification.
	CloudflareTestEmail string

	// CloudflareTestGroup is a test group for GSuite group rule verification.
	CloudflareTestGroup string

	// SkipCleanup skips resource cleanup for debugging.
	SkipCleanup bool

	// UseExistingCluster uses an existing cluster instead of creating kind.
	UseExistingCluster bool

	// KindClusterName is the name of the kind cluster (if created).
	KindClusterName string
}

func init() {
	// Register all schemes at package init time so the scheme is fully
	// populated before any goroutine (manager, informers, reflectors) reads it.
	// This eliminates the data race between SynchronizedBeforeSuite's second
	// function (which runs on all processes) and the manager's goroutines on
	// Process 1 that are already reading from the scheme.
	_ = clientgoscheme.AddToScheme(scheme)
	_ = cfgatev1alpha1.AddToScheme(scheme)
	_ = gatewayv1.Install(scheme)
	_ = gatewayv1b1.Install(scheme)
}

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "E2E Suite")
}

var _ = SynchronizedBeforeSuite(
	// Process 1: cluster + CRDs + controller manager setup.
	// Returns kubeconfig path as []byte for all processes.
	func() []byte {
		ctx, cancel = context.WithCancel(context.Background())

		// Initialize logger for controller-runtime.
		zapConfig := zap.NewDevelopmentConfig()
		zapConfig.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		zapLogger, err := zapConfig.Build()
		Expect(err).NotTo(HaveOccurred())
		ctrl.SetLogger(ctrlzap.New(ctrlzap.UseFlagOptions(&ctrlzap.Options{
			Development: true,
			ZapOpts:     []zap.Option{zap.WrapCore(func(_ zapcore.Core) zapcore.Core { return zapLogger.Core() })},
		})))

		// Schemes already registered in init().

		// Load environment configuration.
		testEnv = loadTestEnv()

		if testEnv.CloudflareAPIToken != "" && !testEnv.SkipCleanup {
			cleanOrphanedE2EResources()
		}

		// Find project root for CRD paths.
		projectRoot = findProjectRoot()
		Expect(projectRoot).NotTo(BeEmpty(), "Could not find project root")

		// Set up Kubernetes cluster and get kubeconfig path.
		var kubeconfigPath string
		if testEnv.UseExistingCluster {
			kubeconfigPath = setupExistingCluster()
		} else {
			kubeconfigPath = setupKindCluster()
		}

		// Install CRDs.
		installCRDs()

		// Create suite-scoped fallback credentials used during namespace teardown.
		ensureFallbackCredentialsSecret()

		// Start controller manager in-process (Process 1 only).
		By("Starting controller manager in-process")

		var mgrCtx context.Context
		mgrCtx, mgrCancel = context.WithCancel(context.Background())

		mgr, err = ctrl.NewManager(cfg, ctrl.Options{
			Scheme:                 scheme,
			LeaderElection:         false,
			HealthProbeBindAddress: "0",
			Metrics:                metricsserver.Options{BindAddress: "0"},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create manager")

		// Detect feature gates (optional Gateway API CRDs).
		featureGates, err = features.DetectFeatures(k8sClientset.Discovery())
		Expect(err).NotTo(HaveOccurred(), "Failed to detect feature gates")
		featureGates.LogFeatures(ctrl.Log.WithName("e2e"))

		// Initialize shared credential cache for all CF-facing reconcilers (B3 + F3).
		credCache := cfcloudflare.NewCredentialCache(0) // 0 = default TTL

		// Register all 6 controllers.
		tunnelReconciler := &controller.CloudflareTunnelReconciler{
			Client:          mgr.GetClient(),
			Scheme:          mgr.GetScheme(),
			Recorder:        mgr.GetEventRecorder("cloudflaretunnel-controller"),
			Builder:         cloudflared.NewBuilder(),
			APIReader:       mgr.GetAPIReader(),
			CredentialCache: credCache,
		}
		Expect(tunnelReconciler.SetupWithManager(mgr)).To(Succeed(), "Failed to setup tunnel controller")

		dnsReconciler := &controller.CloudflareDNSReconciler{
			Client:          mgr.GetClient(),
			Scheme:          mgr.GetScheme(),
			Recorder:        mgr.GetEventRecorder("cloudflaredns-controller"),
			CredentialCache: credCache,
		}
		Expect(dnsReconciler.SetupWithManager(mgr)).To(Succeed(), "Failed to setup DNS controller")

		accessReconciler := &controller.CloudflareAccessPolicyReconciler{
			Client:          mgr.GetClient(),
			Scheme:          mgr.GetScheme(),
			Recorder:        mgr.GetEventRecorder("cloudflareaccesspolicy-controller"),
			FeatureGates:    featureGates,
			CredentialCache: credCache,
		}
		Expect(accessReconciler.SetupWithManager(mgr)).To(Succeed(), "Failed to setup access policy controller")

		accessApplicationReconciler := &controller.CloudflareAccessApplicationReconciler{
			Client:          mgr.GetClient(),
			Scheme:          mgr.GetScheme(),
			Recorder:        mgr.GetEventRecorder("cloudflareaccessapplication-controller"),
			FeatureGates:    featureGates,
			CredentialCache: credCache,
		}
		Expect(accessApplicationReconciler.SetupWithManager(mgr)).To(Succeed(), "Failed to setup access application controller")

		httpRouteReconciler := &controller.HTTPRouteReconciler{
			Client:   mgr.GetClient(),
			Scheme:   mgr.GetScheme(),
			Recorder: mgr.GetEventRecorder("httproute-controller"),
		}
		Expect(httpRouteReconciler.SetupWithManager(mgr)).To(Succeed(), "Failed to setup HTTPRoute controller")

		// Gateway + GatewayClass controllers (F2: register both).
		gatewayReconciler := &controller.GatewayReconciler{
			Client:   mgr.GetClient(),
			Scheme:   mgr.GetScheme(),
			Recorder: mgr.GetEventRecorder("gateway-controller"),
		}
		Expect(gatewayReconciler.SetupWithManager(mgr)).To(Succeed(), "Failed to setup Gateway controller")

		gatewayClassReconciler := &controller.GatewayClassReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}
		Expect(gatewayClassReconciler.SetupWithManager(mgr)).To(Succeed(), "Failed to setup GatewayClass controller")

		// Start manager in background goroutine.
		go func() {
			defer GinkgoRecover()
			err := mgr.Start(mgrCtx)
			if err != nil && mgrCtx.Err() == nil {
				Fail(fmt.Sprintf("Manager failed to start: %v", err))
			}
		}()

		// Wait for manager caches to sync.
		Eventually(func() bool {
			return mgr.GetCache().WaitForCacheSync(mgrCtx)
		}, 30*time.Second, 100*time.Millisecond).Should(BeTrue(), "Manager caches did not sync")

		By("E2E test environment ready (Process 1)")
		return []byte(kubeconfigPath)
	},
	// All processes: create K8s clients from kubeconfig path.
	// NO manager creation; only Process 1 runs the controller.
	func(data []byte) {
		kubeconfigPath := string(data)

		// Schemes already registered in init().

		// Build clients from kubeconfig path.
		var err error
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		Expect(err).NotTo(HaveOccurred(), "Failed to build REST config")

		k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
		Expect(err).NotTo(HaveOccurred(), "Failed to create controller-runtime client")

		k8sClientset, err = kubernetes.NewForConfig(cfg)
		Expect(err).NotTo(HaveOccurred(), "Failed to create clientset")

		// Load test environment (env vars available in all processes).
		testEnv = loadTestEnv()

		// Set up context for all processes.
		ctx, cancel = context.WithCancel(context.Background())
	},
)

var _ = SynchronizedAfterSuite(
	// All processes: intentionally empty.
	// Each test cleans its own namespace via DeferCleanup/AfterAll.
	// We do NOT clean namespaces here because other parallel processes may still
	// have specs running; deleting their namespaces causes "forbidden: unable to
	// create new content in namespace" or "namespace not found" failures.
	func() {},
	// Process 1 only: runs AFTER all processes complete.
	// Sequential cleanup: namespaces → manager → CF sweep → cluster teardown.
	func() {
		// 1. Clean orphaned namespaces WHILE MANAGER IS STILL RUNNING.
		// Finalizers on CRDs need the controller to process deletions.
		if k8sClient != nil && testEnv != nil && !testEnv.SkipCleanup {
			cleanOrphanedTestNamespaces()
		}

		// 2. Stop manager AFTER namespace cleanup completes.
		if mgrCancel != nil {
			mgrCancel()
		}

		// 3. Sweep orphaned CF resources (doesn't need running manager).
		if testEnv != nil && !testEnv.SkipCleanup && testEnv.CloudflareAPIToken != "" {
			cleanOrphanedE2EResources()
		}

		// 4. Teardown cluster.
		if testEnv != nil && !testEnv.SkipCleanup && !testEnv.UseExistingCluster {
			teardownKindCluster()
		}

		cancel()
	},
)

// testID generates a deterministic resource name based on Ginkgo node for parallel safety.
// Format: e2e-{type}-{node}-{line}
// Cleanup relies on these patterns - all e2e-* resources are batch-deleted in AfterSuite.
func testID(resourceType string) string {
	specIndex := CurrentSpecReport().LeafNodeLocation.LineNumber
	return fmt.Sprintf("e2e-%s-%d-%d", resourceType, GinkgoParallelProcess(), specIndex)
}

// cleanOrphanedTestNamespaces removes any test namespaces that weren't cleaned up.
// Called in SynchronizedAfterSuite all-process phase while controller is still alive.
// The running controller processes finalizers naturally as CRs are deleted (F1: trust controller).
func cleanOrphanedTestNamespaces() {
	By("Cleaning orphaned test namespaces")

	var nsList corev1.NamespaceList
	if err := k8sClient.List(ctx, &nsList, client.MatchingLabels{
		"cfgate.io/e2e-test": "true",
	}); err != nil {
		GinkgoWriter.Printf("Warning: failed to list test namespaces: %v\n", err)
		return
	}

	for _, ns := range nsList.Items {
		deleteTestNamespace(ns.DeepCopy())
	}
}

func ensureFallbackCredentialsSecret() {
	if testEnv == nil || testEnv.CloudflareAPIToken == "" {
		return
	}

	fallbackNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: e2eFallbackCredentialsNamespace,
		},
	}
	if err := k8sClient.Create(ctx, fallbackNS); err != nil && !apierrors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred(), "failed to create fallback credentials namespace")
	}

	secretKey := client.ObjectKey{
		Name:      e2eFallbackCredentialsSecret,
		Namespace: e2eFallbackCredentialsNamespace,
	}
	var existing corev1.Secret
	err := k8sClient.Get(ctx, secretKey, &existing)
	if apierrors.IsNotFound(err) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      e2eFallbackCredentialsSecret,
				Namespace: e2eFallbackCredentialsNamespace,
			},
			Type: corev1.SecretTypeOpaque,
			StringData: map[string]string{
				"CLOUDFLARE_API_TOKEN": testEnv.CloudflareAPIToken,
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed(), "failed to create fallback credentials secret")
		return
	}
	Expect(err).NotTo(HaveOccurred(), "failed to get fallback credentials secret")

	if string(existing.Data["CLOUDFLARE_API_TOKEN"]) == testEnv.CloudflareAPIToken {
		return
	}

	existing.StringData = map[string]string{
		"CLOUDFLARE_API_TOKEN": testEnv.CloudflareAPIToken,
	}
	Expect(k8sClient.Update(ctx, &existing)).To(Succeed(), "failed to update fallback credentials secret")
}

// cleanOrphanedE2EResources deletes all E2E test resources from Cloudflare.
// This is the primary cleanup mechanism - runs before and after test execution.
// Cleans: tunnels (e2e-*, recovery-*), DNS records (e2e-*, _cfgate.e2e-*),
// Access applications (e2e-*), unreferenced cfgate owner tags, reusable Access
// policies (e2e-*), and service tokens (e2e-*).
func cleanOrphanedE2EResources() {
	By("Cleaning orphaned E2E resources from Cloudflare")

	cfClient := cloudflare.NewClient(option.WithAPIToken(testEnv.CloudflareAPIToken))
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// Clean tunnels.
	cleanOrphanedTunnels(cleanupCtx, cfClient)

	// Clean DNS records (if zone configured).
	if testEnv.CloudflareZoneName != "" {
		cleanOrphanedDNSRecords(cleanupCtx, cfClient)
	}

	// Clean Access applications and reusable policies (alpha.3+).
	cleanOrphanedAccessApplications(cleanupCtx, cfClient)
	cleanOrphanedAccessTags(cleanupCtx, cfClient)
	cleanOrphanedAccessPolicies(cleanupCtx, cfClient)

	// Clean service tokens (alpha.3).
	cleanOrphanedServiceTokens(cleanupCtx, cfClient)
}

// cleanOrphanedTunnels deletes e2e-* and recovery-* tunnels.
func cleanOrphanedTunnels(ctx context.Context, cfClient *cloudflare.Client) {
	iter := cfClient.ZeroTrust.Tunnels.Cloudflared.ListAutoPaging(ctx, zero_trust.TunnelCloudflaredListParams{
		AccountID: cloudflare.F(testEnv.CloudflareAccountID),
	})

	var orphaned []struct{ ID, Name string }
	for iter.Next() {
		t := iter.Current()
		if strings.HasPrefix(t.Name, "e2e-") || strings.HasPrefix(t.Name, "recovery-") {
			orphaned = append(orphaned, struct{ ID, Name string }{t.ID, t.Name})
		}
	}

	if err := iter.Err(); err != nil {
		GinkgoWriter.Printf("Warning: failed to list tunnels: %v\n", err)
		return
	}

	if len(orphaned) == 0 {
		GinkgoWriter.Printf("No orphaned tunnels found\n")
		return
	}

	GinkgoWriter.Printf("Deleting %d orphaned tunnels\n", len(orphaned))
	for _, t := range orphaned {
		_, _ = cfClient.ZeroTrust.Tunnels.Cloudflared.Connections.Delete(ctx, t.ID, zero_trust.TunnelCloudflaredConnectionDeleteParams{
			AccountID: cloudflare.F(testEnv.CloudflareAccountID),
		})
		_, err := cfClient.ZeroTrust.Tunnels.Cloudflared.Delete(ctx, t.ID, zero_trust.TunnelCloudflaredDeleteParams{
			AccountID: cloudflare.F(testEnv.CloudflareAccountID),
		})
		if err != nil && !strings.Contains(err.Error(), "not found") {
			GinkgoWriter.Printf("  Warning: %s: %v\n", t.Name, err)
		}
	}
}

// cleanOrphanedDNSRecords deletes e2e-* and _cfgate.e2e-* DNS records.
func cleanOrphanedDNSRecords(ctx context.Context, cfClient *cloudflare.Client) {
	// Get zone ID.
	zoneList, err := cfClient.Zones.List(ctx, zones.ZoneListParams{
		Name: cloudflare.F(testEnv.CloudflareZoneName),
	})
	if err != nil || len(zoneList.Result) == 0 {
		GinkgoWriter.Printf("Warning: failed to get zone ID: %v\n", err)
		return
	}
	zoneID := zoneList.Result[0].ID

	// List all records and filter for E2E patterns.
	iter := cfClient.DNS.Records.ListAutoPaging(ctx, dns.RecordListParams{
		ZoneID: cloudflare.F(zoneID),
	})

	var orphaned []struct{ ID, Name string }
	for iter.Next() {
		r := iter.Current()
		// Match e2e-* hostnames and _cfgate.e2e-* ownership TXT records.
		if strings.Contains(r.Name, "e2e-") || strings.HasPrefix(r.Name, "_cfgate.e2e-") {
			orphaned = append(orphaned, struct{ ID, Name string }{r.ID, r.Name})
		}
	}

	if err := iter.Err(); err != nil {
		GinkgoWriter.Printf("Warning: failed to list DNS records: %v\n", err)
		return
	}

	if len(orphaned) == 0 {
		GinkgoWriter.Printf("No orphaned DNS records found\n")
		return
	}

	GinkgoWriter.Printf("Deleting %d orphaned DNS records\n", len(orphaned))
	for _, r := range orphaned {
		_, err := cfClient.DNS.Records.Delete(ctx, r.ID, dns.RecordDeleteParams{
			ZoneID: cloudflare.F(zoneID),
		})
		if err != nil && !strings.Contains(err.Error(), "not found") {
			GinkgoWriter.Printf("  Warning: %s: %v\n", r.Name, err)
		}
	}
}

// cleanOrphanedAccessApplications deletes e2e-* Access applications.
func cleanOrphanedAccessApplications(ctx context.Context, cfClient *cloudflare.Client) {
	iter := cfClient.ZeroTrust.Access.Applications.ListAutoPaging(ctx, zero_trust.AccessApplicationListParams{
		AccountID: cloudflare.F(testEnv.CloudflareAccountID),
	})

	var orphaned []struct{ ID, Name string }
	for iter.Next() {
		app := iter.Current()
		// Match e2e-* application names and domains. Some focused Access tests use
		// stable app names such as admin-app but e2e-* hostnames.
		if strings.HasPrefix(app.Name, "e2e-") || strings.Contains(app.Domain, "e2e-") {
			orphaned = append(orphaned, struct{ ID, Name string }{app.ID, app.Name})
		}
	}

	if err := iter.Err(); err != nil {
		GinkgoWriter.Printf("Warning: failed to list Access applications: %v\n", err)
		return
	}

	if len(orphaned) == 0 {
		GinkgoWriter.Printf("No orphaned Access applications found\n")
		return
	}

	GinkgoWriter.Printf("Deleting %d orphaned Access applications\n", len(orphaned))
	for _, app := range orphaned {
		_, err := cfClient.ZeroTrust.Access.Applications.Delete(ctx, app.ID, zero_trust.AccessApplicationDeleteParams{
			AccountID: cloudflare.F(testEnv.CloudflareAccountID),
		})
		if err != nil && !strings.Contains(err.Error(), "not found") {
			GinkgoWriter.Printf("  Warning: %s: %v\n", app.Name, err)
		}
	}
}

// cleanOrphanedAccessTags deletes unreferenced cfgate owner tags.
func cleanOrphanedAccessTags(ctx context.Context, cfClient *cloudflare.Client) {
	referenced := map[string]struct{}{}
	appIter := cfClient.ZeroTrust.Access.Applications.ListAutoPaging(ctx, zero_trust.AccessApplicationListParams{
		AccountID: cloudflare.F(testEnv.CloudflareAccountID),
	})
	for appIter.Next() {
		for _, tag := range accessApplicationTagNames(appIter.Current().Tags) {
			referenced[tag] = struct{}{}
		}
	}
	if err := appIter.Err(); err != nil {
		GinkgoWriter.Printf("Warning: failed to list Access applications for tag references: %v\n", err)
		return
	}

	tagIter := cfClient.ZeroTrust.Access.Tags.ListAutoPaging(ctx, zero_trust.AccessTagListParams{
		AccountID: cloudflare.F(testEnv.CloudflareAccountID),
	})

	var orphaned []string
	for tagIter.Next() {
		tagName := tagIter.Current().Name
		if !isCfgateOwnerTag(tagName) {
			continue
		}
		if _, ok := referenced[tagName]; ok {
			continue
		}
		orphaned = append(orphaned, tagName)
	}

	if err := tagIter.Err(); err != nil {
		GinkgoWriter.Printf("Warning: failed to list Access tags: %v\n", err)
		return
	}

	if len(orphaned) == 0 {
		GinkgoWriter.Printf("No orphaned Access tags found\n")
		return
	}

	GinkgoWriter.Printf("Deleting %d orphaned Access tags\n", len(orphaned))
	for _, tagName := range orphaned {
		_, err := cfClient.ZeroTrust.Access.Tags.Delete(ctx, tagName, zero_trust.AccessTagDeleteParams{
			AccountID: cloudflare.F(testEnv.CloudflareAccountID),
		})
		if err != nil && !strings.Contains(err.Error(), "not found") {
			GinkgoWriter.Printf("  Warning: %s: %v\n", tagName, err)
		}
	}
}

func isCfgateOwnerTag(name string) bool {
	const prefix = "cfgate:"
	if len(name) != len(prefix)+28 || !strings.HasPrefix(name, prefix) {
		return false
	}
	for _, r := range name[len(prefix):] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func accessApplicationTagNames(tags interface{}) []string {
	switch typed := tags.(type) {
	case nil:
		return nil
	case []string:
		return typed
	case []interface{}:
		names := make([]string, 0, len(typed))
		for _, tag := range typed {
			if name, ok := tag.(string); ok {
				names = append(names, name)
			}
		}
		return names
	default:
		return nil
	}
}

// cleanOrphanedAccessPolicies deletes e2e-* reusable Access policies.
func cleanOrphanedAccessPolicies(ctx context.Context, cfClient *cloudflare.Client) {
	iter := cfClient.ZeroTrust.Access.Policies.ListAutoPaging(ctx, zero_trust.AccessPolicyListParams{
		AccountID: cloudflare.F(testEnv.CloudflareAccountID),
	})

	var orphaned []struct{ ID, Name string }
	for iter.Next() {
		policy := iter.Current()
		if strings.HasPrefix(policy.Name, "e2e-") {
			orphaned = append(orphaned, struct{ ID, Name string }{policy.ID, policy.Name})
		}
	}

	if err := iter.Err(); err != nil {
		GinkgoWriter.Printf("Warning: failed to list Access policies: %v\n", err)
		return
	}

	if len(orphaned) == 0 {
		GinkgoWriter.Printf("No orphaned Access policies found\n")
		return
	}

	GinkgoWriter.Printf("Deleting %d orphaned Access policies\n", len(orphaned))
	for _, policy := range orphaned {
		_, err := cfClient.ZeroTrust.Access.Policies.Delete(ctx, policy.ID, zero_trust.AccessPolicyDeleteParams{
			AccountID: cloudflare.F(testEnv.CloudflareAccountID),
		})
		if err != nil && !strings.Contains(err.Error(), "not found") {
			GinkgoWriter.Printf("  Warning: %s: %v\n", policy.Name, err)
		}
	}
}

// cleanOrphanedServiceTokens deletes e2e-* service tokens.
func cleanOrphanedServiceTokens(ctx context.Context, cfClient *cloudflare.Client) {
	iter := cfClient.ZeroTrust.Access.ServiceTokens.ListAutoPaging(ctx, zero_trust.AccessServiceTokenListParams{
		AccountID: cloudflare.F(testEnv.CloudflareAccountID),
	})

	var orphaned []struct{ ID, Name string }
	for iter.Next() {
		token := iter.Current()
		// Match e2e-* token names.
		if strings.HasPrefix(token.Name, "e2e-") {
			orphaned = append(orphaned, struct{ ID, Name string }{token.ID, token.Name})
		}
	}

	if err := iter.Err(); err != nil {
		GinkgoWriter.Printf("Warning: failed to list service tokens: %v\n", err)
		return
	}

	if len(orphaned) == 0 {
		GinkgoWriter.Printf("No orphaned service tokens found\n")
		return
	}

	GinkgoWriter.Printf("Deleting %d orphaned service tokens\n", len(orphaned))
	for _, token := range orphaned {
		_, err := cfClient.ZeroTrust.Access.ServiceTokens.Delete(ctx, token.ID, zero_trust.AccessServiceTokenDeleteParams{
			AccountID: cloudflare.F(testEnv.CloudflareAccountID),
		})
		if err != nil && !strings.Contains(err.Error(), "not found") {
			GinkgoWriter.Printf("  Warning: %s: %v\n", token.Name, err)
		}
	}
}

// loadTestEnv loads the test environment from environment variables.
func loadTestEnv() *E2ETestEnv {
	env := &E2ETestEnv{
		CloudflareAPIToken:  os.Getenv(EnvCloudflareAPIToken),
		CloudflareAccountID: os.Getenv(EnvCloudflareAccountID),
		CloudflareZoneName:  os.Getenv(EnvCloudflareZoneName),
		CloudflareIdPID:     os.Getenv(EnvCloudflareIdPID),
		CloudflareTestEmail: os.Getenv(EnvCloudflareTestEmail),
		CloudflareTestGroup: os.Getenv(EnvCloudflareTestGroup),
		SkipCleanup:         os.Getenv(EnvSkipCleanup) == "true",
		UseExistingCluster:  os.Getenv(EnvUseExistingCluster) == "true",
		KindClusterName:     fmt.Sprintf("cfgate-e2e-%d", time.Now().Unix()),
	}

	return env
}

// findProjectRoot finds the cfgate project root directory.
func findProjectRoot() string {
	// Start from the test directory and walk up to find go.mod.
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}

	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// setupKindCluster creates a kind cluster and returns the kubeconfig path.
// Also sets up cfg, k8sClient, k8sClientset for Process 1 use during CRD installation.
func setupKindCluster() string {
	By("Creating kind cluster: " + testEnv.KindClusterName)

	cmd := exec.CommandContext(ctx, "kind", "create", "cluster",
		"--name", testEnv.KindClusterName,
		"--wait", "5m",
	)
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter
	Expect(cmd.Run()).To(Succeed(), "Failed to create kind cluster")

	// Get kubeconfig from kind.
	kubeconfigBytes, err := exec.CommandContext(ctx, "kind", "get", "kubeconfig",
		"--name", testEnv.KindClusterName,
	).Output()
	Expect(err).NotTo(HaveOccurred(), "Failed to get kind kubeconfig")

	// Create temporary kubeconfig file.
	kubeconfigPath := filepath.Join(os.TempDir(), fmt.Sprintf("cfgate-e2e-%s.kubeconfig", testEnv.KindClusterName))
	Expect(os.WriteFile(kubeconfigPath, kubeconfigBytes, 0600)).To(Succeed())

	// Set up clients (Process 1 needs these for CRD installation + controller setup).
	cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	Expect(err).NotTo(HaveOccurred(), "Failed to build REST config")

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred(), "Failed to create controller-runtime client")

	k8sClientset, err = kubernetes.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred(), "Failed to create clientset")

	// Wait for API server to be ready.
	Eventually(func() error {
		_, err := k8sClientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
		return err
	}, 60*time.Second, 1*time.Second).Should(Succeed(), "API server not ready")

	return kubeconfigPath
}

// setupExistingCluster sets up clients for an existing cluster and returns the kubeconfig path.
// Also sets up cfg, k8sClient, k8sClientset for Process 1 use during CRD installation.
func setupExistingCluster() string {
	By("Using existing Kubernetes cluster")

	var err error

	// Load kubeconfig from default location or env.
	kubeconfigPath := os.Getenv(EnvKubeconfig)
	if kubeconfigPath == "" {
		kubeconfigPath = filepath.Join(os.Getenv("HOME"), ".kube", "config")
	}

	cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	Expect(err).NotTo(HaveOccurred(), "Failed to build REST config from kubeconfig")

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred(), "Failed to create controller-runtime client")

	k8sClientset, err = kubernetes.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred(), "Failed to create clientset")

	// Verify cluster is accessible.
	_, err = k8sClientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	Expect(err).NotTo(HaveOccurred(), "Failed to connect to existing cluster")

	return kubeconfigPath
}

// teardownKindCluster deletes the kind cluster.
func teardownKindCluster() {
	if testEnv.KindClusterName == "" {
		return
	}

	By("Deleting kind cluster: " + testEnv.KindClusterName)

	cmd := exec.CommandContext(context.Background(), "kind", "delete", "cluster",
		"--name", testEnv.KindClusterName,
	)
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter
	// Ignore errors on cleanup.
	_ = cmd.Run()
}

// installCRDs installs cfgate and Gateway API CRDs.
func installCRDs() {
	By("Installing CRDs")

	// Install cfgate CRDs.
	crdDir := filepath.Join(projectRoot, "config", "crd", "bases")
	files, err := os.ReadDir(crdDir)
	Expect(err).NotTo(HaveOccurred(), "Failed to read CRD directory")

	for _, file := range files {
		if filepath.Ext(file.Name()) != ".yaml" {
			continue
		}

		crdPath := filepath.Join(crdDir, file.Name())
		cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", crdPath)
		cmd.Stdout = GinkgoWriter
		cmd.Stderr = GinkgoWriter
		Expect(cmd.Run()).To(Succeed(), "Failed to install CRD: "+file.Name())
	}

	// Install Gateway API CRDs (standard channel).
	gatewayAPICRDs := "https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.5.1/standard-install.yaml"
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", gatewayAPICRDs)
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter
	Expect(cmd.Run()).To(Succeed(), "Failed to install Gateway API CRDs")

	// Wait for ALL cfgate CRDs to be established.
	// This prevents race conditions where the controller starts before
	// CRD API discovery is complete.
	Eventually(func() bool {
		// Check CloudflareTunnel API registration
		tunnel := &cfgatev1alpha1.CloudflareTunnel{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: "test", Namespace: "default"}, tunnel); err != nil {
			if !apierrors.IsNotFound(err) {
				return false // API not ready (kind not registered)
			}
		}

		// Check CloudflareDNS API registration
		dns := &cfgatev1alpha1.CloudflareDNS{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: "test", Namespace: "default"}, dns); err != nil {
			if !apierrors.IsNotFound(err) {
				return false // API not ready (kind not registered)
			}
		}

		// Check CloudflareAccessPolicy API registration
		policy := &cfgatev1alpha1.CloudflareAccessPolicy{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: "test", Namespace: "default"}, policy); err != nil {
			if !apierrors.IsNotFound(err) {
				return false // API not ready (kind not registered)
			}
		}

		app := &cfgatev1alpha1.CloudflareAccessApplication{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: "test", Namespace: "default"}, app); err != nil {
			if !apierrors.IsNotFound(err) {
				return false
			}
		}

		return true
	}, 30*time.Second, 1*time.Second).Should(BeTrue(), "cfgate CRDs not fully registered in API server")
}

// createTestNamespace creates a unique namespace for a test.
func createTestNamespace(prefix string) *corev1.Namespace {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: prefix + "-",
			Labels: map[string]string{
				"cfgate.io/e2e-test": "true",
			},
		},
	}
	Expect(k8sClient.Create(ctx, ns)).To(Succeed())
	return ns
}

// deleteTestNamespace deletes a test namespace and waits for termination.
// Deletion follows dependency order: DNS and Access CRs first (they need the
// tunnel's credentials for Cloudflare API cleanup), then Tunnels, then the
// namespace itself. Each phase waits for completion before proceeding.
func deleteTestNamespace(ns *corev1.Namespace) {
	if testEnv.SkipCleanup || ns == nil {
		return
	}

	if namespaceGone(ns.Name) {
		return
	}

	// Phase 1: Delete DNS and Access CRs. Both reference the tunnel for
	// credentials during reconcileDelete; the tunnel must still exist.
	var dnsRecords cfgatev1alpha1.CloudflareDNSList
	if err := k8sClient.List(ctx, &dnsRecords, client.InNamespace(ns.Name)); err == nil {
		for i := range dnsRecords.Items {
			_ = k8sClient.Delete(ctx, &dnsRecords.Items[i])
		}
	}

	var policies cfgatev1alpha1.CloudflareAccessPolicyList
	var apps cfgatev1alpha1.CloudflareAccessApplicationList
	if err := k8sClient.List(ctx, &apps, client.InNamespace(ns.Name)); err == nil {
		for i := range apps.Items {
			_ = k8sClient.Delete(ctx, &apps.Items[i])
		}
	}
	if err := k8sClient.List(ctx, &policies, client.InNamespace(ns.Name)); err == nil {
		for i := range policies.Items {
			_ = k8sClient.Delete(ctx, &policies.Items[i])
		}
	}

	// Wait for DNS and Access CRs to be fully deleted before touching tunnels.
	Eventually(func() bool {
		var dCheck cfgatev1alpha1.CloudflareDNSList
		var aCheck cfgatev1alpha1.CloudflareAccessApplicationList
		var pCheck cfgatev1alpha1.CloudflareAccessPolicyList
		dErr := k8sClient.List(ctx, &dCheck, client.InNamespace(ns.Name))
		aErr := k8sClient.List(ctx, &aCheck, client.InNamespace(ns.Name))
		pErr := k8sClient.List(ctx, &pCheck, client.InNamespace(ns.Name))
		dDone := dErr == nil && len(dCheck.Items) == 0
		aDone := aErr == nil && len(aCheck.Items) == 0
		pDone := pErr == nil && len(pCheck.Items) == 0
		if apierrors.IsNotFound(dErr) {
			dDone = true
		}
		if apierrors.IsNotFound(aErr) {
			aDone = true
		}
		if apierrors.IsNotFound(pErr) {
			pDone = true
		}
		return dDone && aDone && pDone
	}, 120*time.Second, 1*time.Second).Should(BeTrue(),
		"DNS/Access CRs in namespace %s did not terminate", ns.Name)

	// Phase 2: Delete Tunnels. No remaining CRs depend on tunnel credentials.
	var tunnels cfgatev1alpha1.CloudflareTunnelList
	if err := k8sClient.List(ctx, &tunnels, client.InNamespace(ns.Name)); err == nil {
		for i := range tunnels.Items {
			_ = k8sClient.Delete(ctx, &tunnels.Items[i])
		}
	}

	Eventually(func() bool {
		var tCheck cfgatev1alpha1.CloudflareTunnelList
		tErr := k8sClient.List(ctx, &tCheck, client.InNamespace(ns.Name))
		if apierrors.IsNotFound(tErr) {
			return true
		}
		return tErr == nil && len(tCheck.Items) == 0
	}, 120*time.Second, 1*time.Second).Should(BeTrue(),
		"Tunnel CRs in namespace %s did not terminate", ns.Name)

	// Phase 3: Delete namespace after all CRs are gone.
	currentNS, err := k8sClientset.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return
	}
	Expect(err).NotTo(HaveOccurred(), "failed to get namespace before deletion")
	if currentNS.DeletionTimestamp == nil {
		err = k8sClient.Delete(ctx, ns)
		Expect(err == nil || apierrors.IsNotFound(err)).To(BeTrue(), "failed to delete namespace %s", ns.Name)
	}

	Eventually(func() bool {
		_, err := k8sClientset.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err)
	}, 120*time.Second, 1*time.Second).Should(BeTrue(),
		"Namespace %s did not terminate", ns.Name)
}

func namespaceGone(name string) bool {
	_, err := k8sClientset.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	return apierrors.IsNotFound(err)
}

// createCloudflareCredentialsSecret creates the Cloudflare credentials secret.
func createCloudflareCredentialsSecret(namespace string) *corev1.Secret {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cloudflare-credentials",
			Namespace: namespace,
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"CLOUDFLARE_API_TOKEN": testEnv.CloudflareAPIToken,
		},
	}
	Expect(k8sClient.Create(ctx, secret)).To(Succeed())
	return secret
}

// skipIfNoCredentials skips the test if Cloudflare credentials are not available.
func skipIfNoCredentials() {
	if testEnv.CloudflareAPIToken == "" {
		Skip("CLOUDFLARE_API_TOKEN not set - skipping E2E test")
	}
	if testEnv.CloudflareAccountID == "" {
		Skip("CLOUDFLARE_ACCOUNT_ID not set - skipping E2E test")
	}
}

// skipIfNoZone skips the test if Cloudflare zone is not configured.
func skipIfNoZone() {
	skipIfNoCredentials()
	if testEnv.CloudflareZoneName == "" {
		Skip("CLOUDFLARE_ZONE_NAME not set - skipping DNS E2E test")
	}
}

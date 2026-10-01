package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	_ "k8s.io/client-go/plugin/pkg/client/auth" // Import all auth plugins for exec-entrypoint

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/discovery"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	controllerconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	cfcloudflare "cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/controller"
	"cfgate.io/cfgate/internal/controller/features"
)

const (
	defaultMetricsPort = 8080
	defaultHealthPort  = 8081
	envMetricsPort     = "CFGATE_METRICS_PORT"
	envHealthPort      = "CFGATE_HEALTH_PORT"
	exitCodeSuccess    = 0
	exitCodeRuntime    = 1
	exitCodeUsage      = 2
)

var (
	Version   = "0.0.0-dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

type managerConfig struct {
	accessLocks           *controller.AccessLocks
	ClusterDomain         string
	InstallationNamespace string
	ClientSettings        cfcloudflare.ClientSettings
	MetricsAddr           string
	ProbeAddr             string
	EnableLeaderElection  bool
	SecureMetrics         bool
	ZapOptions            zap.Options
}

type managerRuntime struct {
	setLogger           func(logr.Logger)
	createManager       func(managerConfig) (manager.Manager, *rest.Config, error)
	detectFeatures      func(*rest.Config) (*features.FeatureGates, error)
	registerControllers func(manager.Manager, *features.FeatureGates, managerConfig) error
	addProbeChecks      func(manager.Manager) error
	startManager        func(manager.Manager) error
}

type probeCheckAdder interface {
	GetCache() cache.Cache
	AddHealthzCheck(string, healthz.Checker) error
	AddReadyzCheck(string, healthz.Checker) error
}

type cliExitError struct {
	code    int
	err     error
	printed bool
}

var (
	// Test hooks below are intentionally swappable in serial tests; do not use with t.Parallel().
	setupTunnelController = func(mgr manager.Manager, credCache *cfcloudflare.CredentialCache, cfg managerConfig) error {
		return (&controller.CloudflareTunnelReconciler{
			AccessLocks:           cfg.accessLocks,
			APIReader:             mgr.GetAPIReader(),
			ClusterDomain:         cfg.ClusterDomain,
			InstallationNamespace: cfg.InstallationNamespace,
			Client:                mgr.GetClient(),
			Scheme:                mgr.GetScheme(),
			Recorder:              mgr.GetEventRecorder("cloudflaretunnel-controller"),
			CredentialCache:       credCache,
			ClientSettings:        cfg.ClientSettings,
		}).SetupWithManager(mgr)
	}
	setupDNSController = func(mgr manager.Manager, credCache *cfcloudflare.CredentialCache, cfg managerConfig) error {
		return (&controller.CloudflareDNSReconciler{
			InstallationNamespace: cfg.InstallationNamespace,
			Client:                mgr.GetClient(),
			Scheme:                mgr.GetScheme(),
			Recorder:              mgr.GetEventRecorder("cloudflaredns-controller"),
			CredentialCache:       credCache,
			ClientSettings:        cfg.ClientSettings,
		}).SetupWithManager(mgr)
	}
	setupGatewayController = func(mgr manager.Manager) error {
		return (&controller.GatewayReconciler{
			Client:   mgr.GetClient(),
			Scheme:   mgr.GetScheme(),
			Recorder: mgr.GetEventRecorder("gateway-controller"),
		}).SetupWithManager(mgr)
	}
	setupGatewayClassController = func(mgr manager.Manager) error {
		return (&controller.GatewayClassReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}).SetupWithManager(mgr)
	}
	setupHTTPRouteController = func(mgr manager.Manager) error {
		return (&controller.HTTPRouteReconciler{
			Client:   mgr.GetClient(),
			Scheme:   mgr.GetScheme(),
			Recorder: mgr.GetEventRecorder("httproute-controller"),
		}).SetupWithManager(mgr)
	}
	setupAccessPolicyController = func(mgr manager.Manager, featureGates *features.FeatureGates, credCache *cfcloudflare.CredentialCache, cfg managerConfig) error {
		return (&controller.CloudflareAccessPolicyReconciler{
			InstallationNamespace: cfg.InstallationNamespace,
			AccessLocks:           cfg.accessLocks,
			APIReader:             mgr.GetAPIReader(),
			Client:                mgr.GetClient(),
			Scheme:                mgr.GetScheme(),
			Recorder:              mgr.GetEventRecorder("cloudflareaccesspolicy-controller"),
			FeatureGates:          featureGates,
			CredentialCache:       credCache,
			ClientSettings:        cfg.ClientSettings,
		}).SetupWithManager(mgr)
	}
	setupAccessApplicationController = func(mgr manager.Manager, featureGates *features.FeatureGates, credCache *cfcloudflare.CredentialCache, cfg managerConfig) error {
		return (&controller.CloudflareAccessApplicationReconciler{
			InstallationNamespace: cfg.InstallationNamespace,
			AccessLocks:           cfg.accessLocks,
			APIReader:             mgr.GetAPIReader(),
			Client:                mgr.GetClient(),
			Scheme:                mgr.GetScheme(),
			Recorder:              mgr.GetEventRecorder("cloudflareaccessapplication-controller"),
			FeatureGates:          featureGates,
			CredentialCache:       credCache,
			ClientSettings:        cfg.ClientSettings,
		}).SetupWithManager(mgr)
	}
)

func (e cliExitError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e cliExitError) Unwrap() error {
	return e.err
}

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(cfgatev1alpha1.AddToScheme(scheme))
	utilruntime.Must(gwapiv1.Install(scheme))
	utilruntime.Must(gwapiv1beta1.Install(scheme))
}

func main() {
	os.Exit(execute(os.Args[1:], os.Getenv, os.Stderr, defaultManagerRuntime()))
}

func defaultManagerRuntime() managerRuntime {
	return managerRuntime{
		setLogger: func(logger logr.Logger) {
			ctrl.SetLogger(logger)
		},
		createManager: func(cfg managerConfig) (manager.Manager, *rest.Config, error) {
			kubeConfig := ctrl.GetConfigOrDie()
			mgr, err := ctrl.NewManager(kubeConfig, buildManagerOptions(cfg))
			if err != nil {
				return nil, nil, fmt.Errorf("unable to start manager: %w", err)
			}
			return mgr, kubeConfig, nil
		},
		detectFeatures: func(kubeConfig *rest.Config) (*features.FeatureGates, error) {
			discoveryConfig := rest.CopyConfig(kubeConfig)
			discoveryConfig.Timeout = 5 * time.Second
			dc, err := discoveryClientForConfig(discoveryConfig)
			if err != nil {
				return nil, fmt.Errorf("unable to create discovery client: %w", err)
			}

			featureGates, err := features.DetectFeatures(dc)
			if err != nil {
				return nil, fmt.Errorf("unable to detect feature gates: %w", err)
			}
			return featureGates, nil
		},
		registerControllers: func(mgr manager.Manager, gates *features.FeatureGates, cfg managerConfig) error {
			return registerControllers(mgr, gates, cfg)
		},
		addProbeChecks: func(mgr manager.Manager) error {
			return addProbeChecks(mgr)
		},
		startManager: func(mgr manager.Manager) error {
			if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
				return fmt.Errorf("manager stopped with error: %w", err)
			}
			return nil
		},
	}
}

func run(args []string, getenv func(string) string, stderr io.Writer, runtime managerRuntime) error {
	cfg, err := parseManagerConfig(args, getenv, stderr)
	if err != nil {
		return err
	}

	runtime.setLogger(zap.New(zap.UseFlagOptions(&cfg.ZapOptions)))

	setupLog.Info("starting cfgate controller manager",
		"version", Version,
		"commit", Commit,
		"buildDate", BuildDate,
		"metricsAddr", cfg.MetricsAddr,
		"healthProbeAddr", cfg.ProbeAddr,
		"leaderElection", cfg.EnableLeaderElection,
		"secureMetrics", cfg.SecureMetrics,
	)

	mgr, kubeConfig, err := runtime.createManager(cfg)
	if err != nil {
		return err
	}

	featureGates, err := runtime.detectFeatures(kubeConfig)
	if err != nil {
		return err
	}
	featureGates.LogFeatures(setupLog)

	if err := runtime.registerControllers(mgr, featureGates, cfg); err != nil {
		return err
	}

	if err := runtime.addProbeChecks(mgr); err != nil {
		return err
	}

	setupLog.Info("all controllers registered, starting manager")
	if err := runtime.startManager(mgr); err != nil {
		return err
	}
	setupLog.Info("manager shutdown complete")
	return nil
}

func execute(args []string, getenv func(string) string, stderr io.Writer, runtime managerRuntime) int {
	err := run(args, getenv, stderr, runtime)
	if err == nil {
		return exitCodeSuccess
	}

	var cliErr cliExitError
	if errors.As(err, &cliErr) {
		if cliErr.code == exitCodeSuccess {
			return exitCodeSuccess
		}
		if cliErr.err != nil && !cliErr.printed {
			_, _ = fmt.Fprintln(stderr, cliErr.err)
		}
		return cliErr.code
	}

	setupLog.Error(err, "unable to run manager")
	return exitCodeRuntime
}

func parseManagerConfig(args []string, getenv func(string) string, stderr io.Writer) (managerConfig, error) {
	cfg := managerConfig{
		ClusterDomain: "cluster.local",

		ClientSettings: cfcloudflare.DefaultClientSettings(),
		MetricsAddr:    fmt.Sprintf(":%d", defaultMetricsPort),
		ProbeAddr:      fmt.Sprintf(":%d", defaultHealthPort),
		ZapOptions: zap.Options{
			Development: false,
		},
	}

	fs := flag.NewFlagSet("cfgate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.MetricsAddr, "metrics-bind-address", cfg.MetricsAddr,
		"The address the metrics endpoint binds to. Use :8443 for HTTPS or :8080 for HTTP.")
	fs.StringVar(&cfg.ProbeAddr, "health-probe-bind-address", cfg.ProbeAddr,
		"The address the probe endpoint binds to.")
	fs.BoolVar(&cfg.EnableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")
	fs.BoolVar(&cfg.SecureMetrics, "metrics-secure", false,
		"If set, the metrics endpoint is served securely via HTTPS.")
	fs.StringVar(&cfg.ClusterDomain, "cluster-domain", cfg.ClusterDomain, "Kubernetes cluster DNS suffix for backend Service addresses.")
	fs.StringVar(&cfg.InstallationNamespace, "installation-namespace", cfg.InstallationNamespace, "Operator namespace used for persistent DNS ownership; defaults to POD_NAMESPACE.")
	fs.DurationVar(&cfg.ClientSettings.AttemptTimeout, "cloudflare-request-timeout", cfg.ClientSettings.AttemptTimeout, "Maximum duration of one Cloudflare API request attempt.")
	fs.IntVar(&cfg.ClientSettings.MaxIngressRules, "max-ingress-rules", cfg.ClientSettings.MaxIngressRules, "Maximum ingress rules per tunnel configuration, including fallback.")
	fs.IntVar(&cfg.ClientSettings.MaxConfigurationBytes, "max-configuration-bytes", cfg.ClientSettings.MaxConfigurationBytes, "Maximum serialized tunnel configuration size in bytes.")
	cfg.ZapOptions.BindFlags(fs)

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return managerConfig{}, cliExitError{code: exitCodeSuccess, err: err, printed: true}
		}
		return managerConfig{}, cliExitError{code: exitCodeUsage, err: err, printed: true}
	}

	var metricsFlag, probeFlag, installationFlag bool
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "installation-namespace":
			installationFlag = true
		case "metrics-bind-address":
			metricsFlag = true
		case "health-probe-bind-address":
			probeFlag = true
		}
	})
	if !metricsFlag {
		port, err := parsePortEnv(getenv, envMetricsPort, defaultMetricsPort)
		if err != nil {
			return managerConfig{}, cliExitError{code: exitCodeUsage, err: err}
		}
		cfg.MetricsAddr = fmt.Sprintf(":%d", port)
	}
	if !probeFlag {
		port, err := parsePortEnv(getenv, envHealthPort, defaultHealthPort)
		if err != nil {
			return managerConfig{}, cliExitError{code: exitCodeUsage, err: err}
		}
		cfg.ProbeAddr = fmt.Sprintf(":%d", port)
	}

	if !installationFlag {
		cfg.InstallationNamespace = getenv("POD_NAMESPACE")
	}
	cfg.ClusterDomain = strings.TrimSuffix(cfg.ClusterDomain, ".")
	if problems := validation.IsDNS1123Subdomain(cfg.ClusterDomain); len(problems) > 0 {
		return managerConfig{}, cliExitError{code: exitCodeUsage, err: fmt.Errorf("invalid cluster-domain: %s", strings.Join(problems, ", "))}
	}
	if cfg.InstallationNamespace != "" {
		if problems := validation.IsDNS1123Label(cfg.InstallationNamespace); len(problems) > 0 {
			return managerConfig{}, cliExitError{code: exitCodeUsage, err: fmt.Errorf("invalid installation-namespace: %s", strings.Join(problems, ", "))}
		}
	}
	if err := cfcloudflare.ValidateClientSettings(cfg.ClientSettings); err != nil {
		return managerConfig{}, cliExitError{code: exitCodeUsage, err: err}
	}
	return cfg, nil
}

func parsePortEnv(getenv func(string) string, key string, fallback int) (int, error) {
	value := getenv(key)
	if value == "" {
		return fallback, nil
	}
	// Kubernetes Service links share these environment names but contain endpoints, not bind ports.
	if endpoint, ok := strings.CutPrefix(value, "tcp://"); ok {
		if address, err := netip.ParseAddrPort(endpoint); err == nil && address.Port() != 0 && address.Addr().Zone() == "" {
			return fallback, nil
		}
	}

	port, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid integer: %w", key, err)
	}

	if port < 0 || port > 65535 {
		return 0, fmt.Errorf("%s must be between 0 and 65535", key)
	}

	return port, nil
}

func buildManagerOptions(cfg managerConfig) ctrl.Options {
	return ctrl.Options{
		Scheme:     scheme,
		Controller: controllerconfig.Controller{ReconciliationTimeout: controller.DefaultReconciliationTimeout},
		Metrics: metricsserver.Options{
			BindAddress:   cfg.MetricsAddr,
			SecureServing: cfg.SecureMetrics,
		},
		HealthProbeBindAddress: cfg.ProbeAddr,
		LeaderElection:         cfg.EnableLeaderElection,
		LeaderElectionID:       "cfgate.io",
	}
}

func registerControllers(mgr manager.Manager, featureGates *features.FeatureGates, configs ...managerConfig) error {
	cfg := managerConfig{ClusterDomain: "cluster.local", ClientSettings: cfcloudflare.DefaultClientSettings()}
	if len(configs) > 0 {
		cfg = configs[0]
	}
	credCache := cfcloudflare.NewCredentialCache(0)
	cfg.accessLocks = controller.NewAccessLocks()

	if err := setupTunnelController(mgr, credCache, cfg); err != nil {
		return fmt.Errorf("unable to create controller CloudflareTunnel: %w", err)
	}

	if err := setupDNSController(mgr, credCache, cfg); err != nil {
		return fmt.Errorf("unable to create controller CloudflareDNS: %w", err)
	}

	if err := setupGatewayController(mgr); err != nil {
		return fmt.Errorf("unable to create controller Gateway: %w", err)
	}

	if err := setupGatewayClassController(mgr); err != nil {
		return fmt.Errorf("unable to create controller GatewayClass: %w", err)
	}

	if err := setupHTTPRouteController(mgr); err != nil {
		return fmt.Errorf("unable to create controller HTTPRoute: %w", err)
	}

	if err := setupAccessPolicyController(mgr, featureGates, credCache, cfg); err != nil {
		return fmt.Errorf("unable to create controller CloudflareAccessPolicy: %w", err)
	}

	if err := setupAccessApplicationController(mgr, featureGates, credCache, cfg); err != nil {
		return fmt.Errorf("unable to create controller CloudflareAccessApplication: %w", err)
	}

	return nil
}

func addProbeChecks(mgr probeCheckAdder) error {
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up health check: %w", err)
	}

	if err := mgr.AddReadyzCheck("readyz", cacheReadyCheck(mgr.GetCache().WaitForCacheSync)); err != nil {
		return fmt.Errorf("unable to set up ready check: %w", err)
	}

	return nil
}

func discoveryClientForConfig(kubeConfig *rest.Config) (discovery.DiscoveryInterface, error) {
	return discovery.NewDiscoveryClientForConfig(kubeConfig)
}

func cacheReadyCheck(waitForSync func(context.Context) bool) healthz.Checker {
	return func(req *http.Request) error {
		ctx, cancel := context.WithTimeout(req.Context(), time.Second)
		defer cancel()
		if !waitForSync(ctx) {
			return errors.New("controller cache has not synchronized")
		}
		return nil
	}
}

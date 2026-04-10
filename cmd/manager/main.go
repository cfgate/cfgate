package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/go-logr/logr"
	_ "k8s.io/client-go/plugin/pkg/client/auth" // Import all auth plugins for exec-entrypoint

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/discovery"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
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
	MetricsAddr          string
	ProbeAddr            string
	EnableLeaderElection bool
	SecureMetrics        bool
	ZapOptions           zap.Options
}

type managerRuntime struct {
	setLogger           func(logr.Logger)
	createManager       func(managerConfig) (manager.Manager, *rest.Config, error)
	detectFeatures      func(*rest.Config) (*features.FeatureGates, error)
	registerControllers func(manager.Manager, *features.FeatureGates) error
	addProbeChecks      func(manager.Manager) error
	startManager        func(manager.Manager) error
}

type cliExitError struct {
	code    int
	err     error
	printed bool
}

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
			dc, err := discoveryClientForConfig(kubeConfig)
			if err != nil {
				return nil, fmt.Errorf("unable to create discovery client: %w", err)
			}

			featureGates, err := features.DetectFeatures(dc)
			if err != nil {
				return nil, fmt.Errorf("unable to detect feature gates: %w", err)
			}
			return featureGates, nil
		},
		registerControllers: registerControllers,
		addProbeChecks:      addProbeChecks,
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

	if err := runtime.registerControllers(mgr, featureGates); err != nil {
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
	metricsPort, err := parsePortEnv(getenv, envMetricsPort, defaultMetricsPort)
	if err != nil {
		return managerConfig{}, cliExitError{code: exitCodeUsage, err: err}
	}

	probePort, err := parsePortEnv(getenv, envHealthPort, defaultHealthPort)
	if err != nil {
		return managerConfig{}, cliExitError{code: exitCodeUsage, err: err}
	}

	cfg := managerConfig{
		MetricsAddr: fmt.Sprintf(":%d", metricsPort),
		ProbeAddr:   fmt.Sprintf(":%d", probePort),
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
	cfg.ZapOptions.BindFlags(fs)

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return managerConfig{}, cliExitError{code: exitCodeSuccess, err: err, printed: true}
		}
		return managerConfig{}, cliExitError{code: exitCodeUsage, err: err, printed: true}
	}

	return cfg, nil
}

func parsePortEnv(getenv func(string) string, key string, fallback int) (int, error) {
	value := getenv(key)
	if value == "" {
		return fallback, nil
	}

	port, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid integer: %w", key, err)
	}

	return port, nil
}

func buildManagerOptions(cfg managerConfig) ctrl.Options {
	return ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:   cfg.MetricsAddr,
			SecureServing: cfg.SecureMetrics,
		},
		HealthProbeBindAddress: cfg.ProbeAddr,
		LeaderElection:         cfg.EnableLeaderElection,
		LeaderElectionID:       "cfgate.io",
	}
}

func registerControllers(mgr manager.Manager, featureGates *features.FeatureGates) error {
	credCache := cfcloudflare.NewCredentialCache(0)

	if err := (&controller.CloudflareTunnelReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		Recorder:        mgr.GetEventRecorder("cloudflaretunnel-controller"),
		CredentialCache: credCache,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller CloudflareTunnel: %w", err)
	}

	if err := (&controller.CloudflareDNSReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		Recorder:        mgr.GetEventRecorder("cloudflaredns-controller"),
		CredentialCache: credCache,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller CloudflareDNS: %w", err)
	}

	if err := (&controller.GatewayReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorder("gateway-controller"),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller Gateway: %w", err)
	}

	if err := (&controller.GatewayClassReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller GatewayClass: %w", err)
	}

	if err := (&controller.HTTPRouteReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorder("httproute-controller"),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller HTTPRoute: %w", err)
	}

	if err := (&controller.CloudflareAccessPolicyReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		Recorder:        mgr.GetEventRecorder("cloudflareaccesspolicy-controller"),
		FeatureGates:    featureGates,
		CredentialCache: credCache,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller CloudflareAccessPolicy: %w", err)
	}

	return nil
}

func addProbeChecks(mgr manager.Manager) error {
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up health check: %w", err)
	}

	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up ready check: %w", err)
	}

	return nil
}

func discoveryClientForConfig(kubeConfig *rest.Config) (discovery.DiscoveryInterface, error) {
	return discovery.NewDiscoveryClientForConfig(kubeConfig)
}

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cfcloudflare "cfgate.io/cfgate/internal/cloudflare"
	"github.com/go-logr/logr"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"cfgate.io/cfgate/internal/controller/features"
)

type fakeProbeManager struct {
	healthErr error
	readyErr  error

	healthChecks []string
	readyChecks  []string
}

type probeCache struct{ cache.Cache }

func (probeCache) WaitForCacheSync(context.Context) bool { return true }
func (f *fakeProbeManager) GetCache() cache.Cache        { return probeCache{} }

func (f *fakeProbeManager) AddHealthzCheck(name string, _ healthz.Checker) error {
	f.healthChecks = append(f.healthChecks, name)
	return f.healthErr
}

func (f *fakeProbeManager) AddReadyzCheck(name string, _ healthz.Checker) error {
	f.readyChecks = append(f.readyChecks, name)
	return f.readyErr
}

func TestParsePortEnv(t *testing.T) {
	t.Run("uses fallback when unset", func(t *testing.T) {
		port, err := parsePortEnv(func(string) string { return "" }, envMetricsPort, defaultMetricsPort)
		if err != nil {
			t.Fatalf("parsePortEnv() error = %v", err)
		}
		if port != defaultMetricsPort {
			t.Fatalf("parsePortEnv() = %d, want %d", port, defaultMetricsPort)
		}
	})

	t.Run("parses configured port", func(t *testing.T) {
		port, err := parsePortEnv(func(string) string { return "9090" }, envMetricsPort, defaultMetricsPort)
		if err != nil {
			t.Fatalf("parsePortEnv() error = %v", err)
		}
		if port != 9090 {
			t.Fatalf("parsePortEnv() = %d, want 9090", port)
		}
	})

	t.Run("rejects invalid port", func(t *testing.T) {
		_, err := parsePortEnv(func(string) string { return "bad" }, envMetricsPort, defaultMetricsPort)
		if err == nil || !strings.Contains(err.Error(), envMetricsPort) {
			t.Fatalf("parsePortEnv() error = %v, want %q in error", err, envMetricsPort)
		}
	})
}

func TestCLIExitError(t *testing.T) {
	err := cliExitError{code: exitCodeUsage, err: errors.New("boom")}
	if got := err.Error(); got != "boom" {
		t.Fatalf("Error() = %q, want %q", got, "boom")
	}
	if !errors.Is(err, err.err) {
		t.Fatal("Unwrap() did not expose wrapped error")
	}

	empty := cliExitError{}
	if got := empty.Error(); got != "" {
		t.Fatalf("Error() = %q, want empty string", got)
	}
}

func TestServiceLinkPortEnv(t *testing.T) {
	for _, value := range []string{"tcp://10.96.0.1:8080", "tcp://[fd00::1]:8081"} {
		t.Run(value, func(t *testing.T) {
			port, err := parsePortEnv(func(string) string { return value }, envMetricsPort, defaultMetricsPort)
			if err != nil || port != defaultMetricsPort {
				t.Fatalf("parsePortEnv() = %d, %v, want %d, nil", port, err, defaultMetricsPort)
			}
		})
	}
	for _, value := range []string{
		"bad", "tcp://metrics:8080", "http://10.96.0.1:8080", "udp://10.96.0.1:8080",
		"tcp://10.96.0.1", "tcp://10.96.0.1:0", "tcp://10.96.0.1:65536",
		"tcp://10.96.0.1:8080/", "tcp://10.96.0.1:8080?x=1", "tcp://10.96.0.1:8080#x",
		"tcp://user@10.96.0.1:8080", "tcp://[fd00::1%eth0]:8080", " tcp://10.96.0.1:8080",
	} {
		t.Run(value, func(t *testing.T) {
			_, err := parsePortEnv(func(string) string { return value }, envMetricsPort, defaultMetricsPort)
			if err == nil || !strings.Contains(err.Error(), envMetricsPort) {
				t.Fatalf("parsePortEnv() error = %v, want %s error", err, envMetricsPort)
			}
		})
	}
}

func TestManagerBindAddressPrecedence(t *testing.T) {
	for _, tt := range []struct {
		name, metricsEnv, healthEnv, wantMetrics, wantHealth, wantError string
		args                                                            []string
	}{
		{name: "IPv4 service links", metricsEnv: "tcp://10.96.0.1:9191", healthEnv: "tcp://10.96.0.2:9292", wantMetrics: ":8080", wantHealth: ":8081"},
		{name: "IPv6 service links", metricsEnv: "tcp://[fd00::1]:9191", healthEnv: "tcp://[fd00::2]:9292", wantMetrics: ":8080", wantHealth: ":8081"},
		{name: "numeric ports preserved", metricsEnv: "9191", healthEnv: "9292", wantMetrics: ":9191", wantHealth: ":9292"},
		{name: "metrics flag only", metricsEnv: "bad", healthEnv: "9292", args: []string{"--metrics-bind-address=:8443"}, wantMetrics: ":8443", wantHealth: ":9292"},
		{name: "health flag only", metricsEnv: "9191", healthEnv: "bad", args: []string{"--health-probe-bind-address", ":9443"}, wantMetrics: ":9191", wantHealth: ":9443"},
		{name: "explicit default overrides env", metricsEnv: "bad", healthEnv: "bad", args: []string{"--metrics-bind-address=:8080", "--health-probe-bind-address=:8081"}, wantMetrics: ":8080", wantHealth: ":8081"},
		{name: "disable metrics", metricsEnv: "bad", healthEnv: "tcp://10.96.0.1:9292", args: []string{"--metrics-bind-address=0"}, wantMetrics: "0", wantHealth: ":8081"},
		{name: "IPv6 bind flag", metricsEnv: "bad", args: []string{"--metrics-bind-address=[::1]:8443"}, wantMetrics: "[::1]:8443", wantHealth: ":8081"},
		{name: "metrics override cannot hide health error", metricsEnv: "bad", healthEnv: "bad", args: []string{"--metrics-bind-address=0"}, wantError: envHealthPort},
		{name: "health override cannot hide metrics error", metricsEnv: "bad", healthEnv: "bad", args: []string{"--health-probe-bind-address=:9443"}, wantError: envMetricsPort},
		{name: "repeated flag uses last", metricsEnv: "bad", args: []string{"--metrics-bind-address=:9090", "--metrics-bind-address=0"}, wantMetrics: "0", wantHealth: ":8081"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parseManagerConfig(tt.args, func(key string) string {
				if key == envMetricsPort {
					return tt.metricsEnv
				}
				if key == envHealthPort {
					return tt.healthEnv
				}
				return ""
			}, io.Discard)
			if tt.wantError != "" {
				var cliErr cliExitError
				if !errors.As(err, &cliErr) || cliErr.code != exitCodeUsage || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("parseManagerConfig() error = %v, want usage error for %s", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.MetricsAddr != tt.wantMetrics || cfg.ProbeAddr != tt.wantHealth {
				t.Fatalf("bind addresses = %q, %q, want %q, %q", cfg.MetricsAddr, cfg.ProbeAddr, tt.wantMetrics, tt.wantHealth)
			}
		})
	}
}

func TestManagerHelpIgnoresPortEnvironment(t *testing.T) {
	for _, arg := range []string{"--help", "-h"} {
		t.Run(arg, func(t *testing.T) {
			var stderr bytes.Buffer
			code := execute([]string{arg}, func(string) string {
				t.Fatal("help must not read port environment")
				return "bad"
			}, &stderr, managerRuntime{})
			if code != exitCodeSuccess || !strings.Contains(stderr.String(), "Usage of cfgate") {
				t.Fatalf("help exit = %d, stderr = %q", code, stderr.String())
			}
		})
	}
}

func TestParseManagerConfig(t *testing.T) {
	t.Run("uses defaults", func(t *testing.T) {
		cfg, err := parseManagerConfig(nil, func(string) string { return "" }, io.Discard)
		if err != nil {
			t.Fatalf("parseManagerConfig() error = %v", err)
		}
		if cfg.MetricsAddr != ":8080" {
			t.Fatalf("MetricsAddr = %q, want %q", cfg.MetricsAddr, ":8080")
		}
		if cfg.ProbeAddr != ":8081" {
			t.Fatalf("ProbeAddr = %q, want %q", cfg.ProbeAddr, ":8081")
		}
	})

	t.Run("reads env defaults", func(t *testing.T) {
		cfg, err := parseManagerConfig(nil, func(key string) string {
			switch key {
			case envMetricsPort:
				return "9191"
			case envHealthPort:
				return "9292"
			default:
				return ""
			}
		}, io.Discard)
		if err != nil {
			t.Fatalf("parseManagerConfig() error = %v", err)
		}
		if cfg.MetricsAddr != ":9191" {
			t.Fatalf("MetricsAddr = %q, want %q", cfg.MetricsAddr, ":9191")
		}
		if cfg.ProbeAddr != ":9292" {
			t.Fatalf("ProbeAddr = %q, want %q", cfg.ProbeAddr, ":9292")
		}
	})

	t.Run("flags override env", func(t *testing.T) {
		cfg, err := parseManagerConfig([]string{
			"--metrics-bind-address=:8443",
			"--health-probe-bind-address=:9443",
			"--leader-elect",
			"--metrics-secure",
		}, func(key string) string {
			switch key {
			case envMetricsPort:
				return "9191"
			case envHealthPort:
				return "9292"
			default:
				return ""
			}
		}, io.Discard)
		if err != nil {
			t.Fatalf("parseManagerConfig() error = %v", err)
		}
		if cfg.MetricsAddr != ":8443" {
			t.Fatalf("MetricsAddr = %q, want %q", cfg.MetricsAddr, ":8443")
		}
		if cfg.ProbeAddr != ":9443" {
			t.Fatalf("ProbeAddr = %q, want %q", cfg.ProbeAddr, ":9443")
		}
		if !cfg.EnableLeaderElection {
			t.Fatal("EnableLeaderElection = false, want true")
		}
		if !cfg.SecureMetrics {
			t.Fatal("SecureMetrics = false, want true")
		}
	})
}

func TestDefaultManagerRuntime(t *testing.T) {
	runtime := defaultManagerRuntime()
	if runtime.setLogger == nil || runtime.createManager == nil || runtime.detectFeatures == nil || runtime.registerControllers == nil || runtime.addProbeChecks == nil || runtime.startManager == nil {
		t.Fatalf("defaultManagerRuntime() = %#v, want all callbacks initialized", runtime)
	}
}

func TestBuildManagerOptions(t *testing.T) {
	cfg := managerConfig{
		MetricsAddr:          ":8082",
		ProbeAddr:            ":8083",
		EnableLeaderElection: true,
		SecureMetrics:        true,
	}

	opts := buildManagerOptions(cfg)
	if opts.Metrics.BindAddress != ":8082" {
		t.Fatalf("Metrics.BindAddress = %q, want %q", opts.Metrics.BindAddress, ":8082")
	}
	if opts.HealthProbeBindAddress != ":8083" {
		t.Fatalf("HealthProbeBindAddress = %q, want %q", opts.HealthProbeBindAddress, ":8083")
	}
	if !opts.LeaderElection {
		t.Fatal("LeaderElection = false, want true")
	}
	if !opts.Metrics.SecureServing {
		t.Fatal("Metrics.SecureServing = false, want true")
	}
}

func TestAddProbeChecks(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		mgr := &fakeProbeManager{}
		if err := addProbeChecks(mgr); err != nil {
			t.Fatalf("addProbeChecks() error = %v", err)
		}
		if strings.Join(mgr.healthChecks, ",") != "healthz" {
			t.Fatalf("healthChecks = %v, want [healthz]", mgr.healthChecks)
		}
		if strings.Join(mgr.readyChecks, ",") != "readyz" {
			t.Fatalf("readyChecks = %v, want [readyz]", mgr.readyChecks)
		}
	})

	t.Run("health failure", func(t *testing.T) {
		mgr := &fakeProbeManager{healthErr: errors.New("health failed")}
		err := addProbeChecks(mgr)
		if err == nil || !strings.Contains(err.Error(), "unable to set up health check") {
			t.Fatalf("addProbeChecks() error = %v, want wrapped health error", err)
		}
	})

	t.Run("ready failure", func(t *testing.T) {
		mgr := &fakeProbeManager{readyErr: errors.New("ready failed")}
		err := addProbeChecks(mgr)
		if err == nil || !strings.Contains(err.Error(), "unable to set up ready check") {
			t.Fatalf("addProbeChecks() error = %v, want wrapped ready error", err)
		}
	})
}

func TestRegisterControllers(t *testing.T) {
	origTunnel := setupTunnelController
	origDNS := setupDNSController
	origGateway := setupGatewayController
	origGatewayClass := setupGatewayClassController
	origHTTPRoute := setupHTTPRouteController
	origAccess := setupAccessPolicyController
	origAccessApp := setupAccessApplicationController
	t.Cleanup(func() {
		setupTunnelController = origTunnel
		setupDNSController = origDNS
		setupGatewayController = origGateway
		setupGatewayClassController = origGatewayClass
		setupHTTPRouteController = origHTTPRoute
		setupAccessPolicyController = origAccess
		setupAccessApplicationController = origAccessApp
	})

	t.Run("success", func(t *testing.T) {
		var calls []string
		setupTunnelController = func(manager.Manager, *cfcloudflare.CredentialCache, managerConfig) error {
			calls = append(calls, "tunnel")
			return nil
		}
		setupDNSController = func(manager.Manager, *cfcloudflare.CredentialCache, managerConfig) error {
			calls = append(calls, "dns")
			return nil
		}
		setupGatewayController = func(manager.Manager) error {
			calls = append(calls, "gateway")
			return nil
		}
		setupGatewayClassController = func(manager.Manager) error {
			calls = append(calls, "gatewayclass")
			return nil
		}
		setupHTTPRouteController = func(manager.Manager) error {
			calls = append(calls, "httproute")
			return nil
		}
		setupAccessPolicyController = func(manager.Manager, *features.FeatureGates, *cfcloudflare.CredentialCache, managerConfig) error {
			calls = append(calls, "access")
			return nil
		}
		setupAccessApplicationController = func(manager.Manager, *features.FeatureGates, *cfcloudflare.CredentialCache, managerConfig) error {
			calls = append(calls, "accessapp")
			return nil
		}

		if err := registerControllers(nil, &features.FeatureGates{}); err != nil {
			t.Fatalf("registerControllers() error = %v", err)
		}

		want := "tunnel,dns,gateway,gatewayclass,httproute,access,accessapp"
		if got := strings.Join(calls, ","); got != want {
			t.Fatalf("calls = %q, want %q", got, want)
		}
	})

	t.Run("wraps controller failure", func(t *testing.T) {
		setupTunnelController = func(manager.Manager, *cfcloudflare.CredentialCache, managerConfig) error {
			return errors.New("boom")
		}

		err := registerControllers(nil, &features.FeatureGates{})
		if err == nil || !strings.Contains(err.Error(), "unable to create controller CloudflareTunnel") {
			t.Fatalf("registerControllers() error = %v, want wrapped tunnel error", err)
		}
	})

	for _, tt := range []struct {
		name string
		fail func()
		want string
	}{
		{
			name: "dns failure",
			fail: func() {
				setupDNSController = func(manager.Manager, *cfcloudflare.CredentialCache, managerConfig) error { return errors.New("boom") }
			},
			want: "unable to create controller CloudflareDNS",
		},
		{
			name: "gateway failure",
			fail: func() {
				setupGatewayController = func(manager.Manager) error { return errors.New("boom") }
			},
			want: "unable to create controller Gateway",
		},
		{
			name: "gateway class failure",
			fail: func() {
				setupGatewayClassController = func(manager.Manager) error { return errors.New("boom") }
			},
			want: "unable to create controller GatewayClass",
		},
		{
			name: "httproute failure",
			fail: func() {
				setupHTTPRouteController = func(manager.Manager) error { return errors.New("boom") }
			},
			want: "unable to create controller HTTPRoute",
		},
		{
			name: "access policy failure",
			fail: func() {
				setupAccessPolicyController = func(manager.Manager, *features.FeatureGates, *cfcloudflare.CredentialCache, managerConfig) error {
					return errors.New("boom")
				}
			},
			want: "unable to create controller CloudflareAccessPolicy",
		},
		{
			name: "access application failure",
			fail: func() {
				setupAccessApplicationController = func(manager.Manager, *features.FeatureGates, *cfcloudflare.CredentialCache, managerConfig) error {
					return errors.New("boom")
				}
			},
			want: "unable to create controller CloudflareAccessApplication",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setupTunnelController = func(manager.Manager, *cfcloudflare.CredentialCache, managerConfig) error { return nil }
			setupDNSController = func(manager.Manager, *cfcloudflare.CredentialCache, managerConfig) error { return nil }
			setupGatewayController = func(manager.Manager) error { return nil }
			setupGatewayClassController = func(manager.Manager) error { return nil }
			setupHTTPRouteController = func(manager.Manager) error { return nil }
			setupAccessPolicyController = func(manager.Manager, *features.FeatureGates, *cfcloudflare.CredentialCache, managerConfig) error {
				return nil
			}
			setupAccessApplicationController = func(manager.Manager, *features.FeatureGates, *cfcloudflare.CredentialCache, managerConfig) error {
				return nil
			}
			tt.fail()

			err := registerControllers(nil, &features.FeatureGates{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("registerControllers() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestExecuteManager(t *testing.T) {
	t.Run("success path", func(t *testing.T) {
		var calls []string
		gotMetrics := ""
		runtime := managerRuntime{
			setLogger: func(logr.Logger) {
				calls = append(calls, "setLogger")
			},
			createManager: func(cfg managerConfig) (manager.Manager, *rest.Config, error) {
				calls = append(calls, "createManager")
				gotMetrics = cfg.MetricsAddr
				return nil, &rest.Config{Host: "https://cfgate.test"}, nil
			},
			detectFeatures: func(*rest.Config) (*features.FeatureGates, error) {
				calls = append(calls, "detectFeatures")
				return &features.FeatureGates{ReferenceGrantCRDExists: true}, nil
			},
			registerControllers: func(manager.Manager, *features.FeatureGates, managerConfig) error {
				calls = append(calls, "registerControllers")
				return nil
			},
			addProbeChecks: func(manager.Manager) error {
				calls = append(calls, "addProbeChecks")
				return nil
			},
			startManager: func(manager.Manager) error {
				calls = append(calls, "startManager")
				return nil
			},
		}

		if code := execute(nil, func(string) string { return "" }, io.Discard, runtime); code != exitCodeSuccess {
			t.Fatalf("execute() = %d, want %d", code, exitCodeSuccess)
		}

		wantCalls := []string{
			"setLogger",
			"createManager",
			"detectFeatures",
			"registerControllers",
			"addProbeChecks",
			"startManager",
		}
		if strings.Join(calls, ",") != strings.Join(wantCalls, ",") {
			t.Fatalf("calls = %v, want %v", calls, wantCalls)
		}
		if gotMetrics != ":8080" {
			t.Fatalf("metrics address = %q, want %q", gotMetrics, ":8080")
		}
	})

	t.Run("help exits zero", func(t *testing.T) {
		for _, arg := range []string{"--help", "-h"} {
			t.Run(arg, func(t *testing.T) {
				stderr := &bytes.Buffer{}
				if code := execute([]string{arg}, func(string) string { return "" }, stderr, managerRuntime{}); code != exitCodeSuccess {
					t.Fatalf("execute() = %d, want %d", code, exitCodeSuccess)
				}
				if stderr.Len() == 0 {
					t.Fatal("stderr was empty, want help output")
				}
			})
		}
	})

	t.Run("bad env returns usage exit code", func(t *testing.T) {
		stderr := &bytes.Buffer{}
		code := execute(nil, func(string) string { return "bad" }, stderr, managerRuntime{})
		if code != exitCodeUsage {
			t.Fatalf("execute() = %d, want %d", code, exitCodeUsage)
		}
		if !strings.Contains(stderr.String(), envMetricsPort) {
			t.Fatalf("stderr = %q, want %q in error", stderr.String(), envMetricsPort)
		}
	})

	t.Run("runtime errors return runtime exit code", func(t *testing.T) {
		runtime := managerRuntime{
			setLogger: func(logr.Logger) {},
			createManager: func(managerConfig) (manager.Manager, *rest.Config, error) {
				return nil, &rest.Config{}, nil
			},
			detectFeatures: func(*rest.Config) (*features.FeatureGates, error) {
				return nil, errors.New("detect failed")
			},
		}

		if code := execute(nil, func(string) string { return "" }, io.Discard, runtime); code != exitCodeRuntime {
			t.Fatalf("execute() = %d, want %d", code, exitCodeRuntime)
		}
	})

	t.Run("unknown flags return usage exit code", func(t *testing.T) {
		stderr := &bytes.Buffer{}
		if code := execute([]string{"--unknown-flag"}, func(string) string { return "" }, stderr, managerRuntime{}); code != exitCodeUsage {
			t.Fatalf("execute() = %d, want %d", code, exitCodeUsage)
		}
		output := stderr.String()
		if output == "" {
			t.Fatal("stderr was empty, want flag usage output")
		}
		if !strings.Contains(output, "flag provided but not defined") {
			t.Fatalf("stderr = %q, want unknown flag error", output)
		}
	})
}

func TestPortEnvRange(t *testing.T) {
	for _, value := range []string{"-1", "65536"} {
		t.Run(value, func(t *testing.T) {
			if _, err := parsePortEnv(func(string) string { return value }, envMetricsPort, defaultMetricsPort); err == nil {
				t.Fatalf("accepted out-of-range port %q", value)
			}
		})
	}
	for _, value := range []string{"0", "1", "65535"} {
		t.Run(value, func(t *testing.T) {
			if _, err := parsePortEnv(func(string) string { return value }, envMetricsPort, defaultMetricsPort); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPortEnvZeroRetainsEphemeralBind(t *testing.T) {
	cfg, err := parseManagerConfig(nil, func(string) string { return "0" }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MetricsAddr != ":0" || cfg.ProbeAddr != ":0" {
		t.Fatalf("env zero bind addresses = %q, %q", cfg.MetricsAddr, cfg.ProbeAddr)
	}
	listener, err := net.Listen("tcp", "127.0.0.1"+cfg.MetricsAddr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Error(err)
		}
	})
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || addr.Port == 0 {
		t.Fatalf("ephemeral bind address = %v", listener.Addr())
	}
}

func TestManagerDeadlineAndCacheReadiness(t *testing.T) {
	if got := buildManagerOptions(managerConfig{}).Controller.ReconciliationTimeout; got != 2*time.Minute {
		t.Fatalf("reconcile timeout=%v", got)
	}
	request := httptest.NewRequest("GET", "/readyz", nil)
	if err := cacheReadyCheck(func(context.Context) bool { return false })(request); err == nil {
		t.Fatal("unsynchronized cache reported ready")
	}
	if err := cacheReadyCheck(func(context.Context) bool { return true })(request); err != nil {
		t.Fatal(err)
	}
	if err := healthz.Ping(request); err != nil {
		t.Fatal("process liveness unexpectedly depends on external state")
	}
}

func TestManagerInfrastructureSettings(t *testing.T) {
	cfg, err := parseManagerConfig([]string{"--cluster-domain=corp.internal.", "--installation-namespace=cfgate-system", "--cloudflare-request-timeout=5s", "--max-ingress-rules=200", "--max-configuration-bytes=20000"}, func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClusterDomain != "corp.internal" || cfg.InstallationNamespace != "cfgate-system" || cfg.ClientSettings.AttemptTimeout != 5*time.Second || cfg.ClientSettings.MaxIngressRules != 200 || cfg.ClientSettings.MaxConfigurationBytes != 20000 {
		t.Fatalf("settings=%+v", cfg)
	}
	for _, args := range [][]string{{"--cluster-domain=bad/domain"}, {"--installation-namespace=BAD"}, {"--max-ingress-rules=0"}, {"--max-configuration-bytes=-1"}, {"--cloudflare-request-timeout=0s"}} {
		if _, err := parseManagerConfig(args, func(string) string { return "" }, io.Discard); err == nil {
			t.Fatalf("invalid settings accepted %v", args)
		}
	}
}

func TestManagerEndpointCollisions(t *testing.T) {
	for _, tt := range []struct {
		name, metrics, health string
		conflict              bool
	}{
		{"same", ":8081", ":8081", true},
		{"wildcard ipv4", "0.0.0.0:8081", ":8081", true},
		{"wildcard ipv6", "[::]:8081", ":8081", true},
		{"wildcard and specific", ":8081", "127.0.0.1:8081", true},
		{"same ipv6", "[::1]:8081", "[0:0:0:0:0:0:0:1]:8081", true},
		{"mapped ipv4", "[::ffff:127.0.0.1]:8081", "127.0.0.1:8081", true},
		{"different ports", ":8080", ":8081", false},
		{"different hosts", "127.0.0.1:8081", "127.0.0.2:8081", false},
		{"metrics disabled", "0", ":8081", false},
		{"health disabled", ":8080", "0", false},
		{"ephemeral", ":0", ":0", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseManagerConfig([]string{"--metrics-bind-address=" + tt.metrics, "--health-probe-bind-address=" + tt.health}, func(string) string { return "" }, io.Discard)
			if (err != nil) != tt.conflict {
				t.Fatalf("error = %v, want conflict %v", err, tt.conflict)
			}
			if err != nil && !strings.Contains(err.Error(), "overlap") {
				t.Fatal(err)
			}
		})
	}
	t.Run("environment", func(t *testing.T) {
		_, err := parseManagerConfig(nil, func(key string) string {
			if key == envMetricsPort || key == envHealthPort {
				return "8090"
			}
			return ""
		}, io.Discard)
		if err == nil {
			t.Fatal("accepted overlapping environment ports")
		}
	})
}

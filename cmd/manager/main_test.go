package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"cfgate.io/cfgate/internal/controller/features"
)

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

func TestRunManager(t *testing.T) {
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
				return &features.FeatureGates{GRPCRouteCRDExists: true}, nil
			},
			registerControllers: func(manager.Manager, *features.FeatureGates) error {
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

		if err := run(nil, func(string) string { return "" }, io.Discard, runtime); err != nil {
			t.Fatalf("run() error = %v", err)
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

	t.Run("returns parse errors", func(t *testing.T) {
		err := run(nil, func(string) string { return "bad" }, io.Discard, managerRuntime{})
		if err == nil || !strings.Contains(err.Error(), envMetricsPort) {
			t.Fatalf("run() error = %v, want %q in error", err, envMetricsPort)
		}
	})

	t.Run("returns runtime errors", func(t *testing.T) {
		runtime := managerRuntime{
			setLogger: func(logr.Logger) {},
			createManager: func(managerConfig) (manager.Manager, *rest.Config, error) {
				return nil, &rest.Config{}, nil
			},
			detectFeatures: func(*rest.Config) (*features.FeatureGates, error) {
				return nil, errors.New("detect failed")
			},
		}

		err := run(nil, func(string) string { return "" }, io.Discard, runtime)
		if err == nil || !strings.Contains(err.Error(), "detect failed") {
			t.Fatalf("run() error = %v, want detect failure", err)
		}
	})

	t.Run("passes flagset errors through", func(t *testing.T) {
		stderr := &bytes.Buffer{}
		err := run([]string{"--unknown-flag"}, func(string) string { return "" }, stderr, managerRuntime{})
		if err == nil {
			t.Fatal("run() error = nil, want flag parsing error")
		}
		if stderr.Len() == 0 {
			t.Fatal("stderr was empty, want flag usage output")
		}
	})
}

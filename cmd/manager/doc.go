// Package main is the entry point for the cfgate controller manager.
//
// The controller manager is the central orchestrator for all cfgate reconcilers.
// It initializes controller-runtime, registers all controllers, and starts the
// reconciliation loop for managing Cloudflare resources from Kubernetes.
//
// # Controllers
//
// The manager registers the following reconcilers:
//   - CloudflareTunnelReconciler: Manages CloudflareTunnel CRDs and cloudflared deployments
//   - CloudflareDNSReconciler: Manages CloudflareDNS CRDs and DNS record synchronization
//   - CloudflareAccessPolicyReconciler: Manages CloudflareAccessPolicy CRDs and Access applications
//   - GatewayReconciler: Validates Gateway API resources bound to tunnels
//   - HTTPRouteReconciler: Processes HTTPRoute resources with cfgate annotations
//
// # Configuration
//
// The manager accepts the following command-line flags:
//
//	-metrics-bind-address    Address for metrics endpoint (default: :8080)
//	-health-probe-bind-address  Address for health probes (default: :8081)
//	-leader-elect            Enable leader election for HA deployments
//	-metrics-secure          Serve metrics over HTTPS
//	-zap-*                   Standard zap logging flags (log-level, encoder, etc.)
//
// Environment variables (CFGATE_ prefix) can override defaults:
//
//	CFGATE_METRICS_PORT    Metrics port (default: 8080)
//	CFGATE_HEALTH_PORT     Health probe port (default: 8081)
//
// # Health Endpoints
//
// The manager exposes standard Kubernetes health endpoints:
//
//	/healthz    Liveness probe
//	/readyz     Readiness probe
//
// # Running
//
// Build and run locally against a cluster:
//
//	mise run build
//	./bin/manager
//
// Deploy to cluster:
//
//	mise run local:deploy
//
// # Logging
//
// Logging uses controller-runtime's zap integration. Configure via flags:
//
//	--zap-log-level=info     Set log level (debug, info, error)
//	--zap-devel=true         Enable development mode (human-readable output)
//	--zap-encoder=console    Use console encoder (default: json in prod)
package main

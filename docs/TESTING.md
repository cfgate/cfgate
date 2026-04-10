# Testing

cfgate tests across two tiers: unit tests for pure functions and E2E tests against the live Cloudflare API.

## Philosophy

- **Real API for E2E.** Every E2E test creates and verifies actual Cloudflare resources (tunnels, DNS records, Access applications, service tokens). No mocks, no fixtures, no VCR. Controller reconciliation patterns are incompatible with cassette approaches (attempted and removed).
- **Pure-function unit tests.** Drift detection, status composition, annotation parsing, context transformation, caching, predicates, feature detection, and cloudflared builders are tested in isolation with table-driven Ginkgo specs.
- **API state verification.** E2E tests verify that Kubernetes CRD state and Cloudflare API state converge correctly.

## Unit Tests

Unit coverage is the primary CI coverage signal. The default unit test surface includes:

- `./api/...` for hand-written scheme registration and package initialization
- `./cmd/...` for manager and cleanup entrypoint orchestration
- `./internal/...` for controller helpers, annotations, status, feature detection, cloudflared builders, and Cloudflare client logic

```bash
mise run test          # unit tests
mise run test:cover    # unit tests with coverage (out/coverage/unit.coverprofile)
```

## E2E Tests

E2E specs run against a real kind cluster with the controller in-process, hitting the live Cloudflare API.

### Environment Variables

All variables are injected via `mise` from `secrets.enc.yaml` and `.env`. See [CONTRIBUTING.md](../CONTRIBUTING.md) for secrets setup.

#### Required

| Variable | Purpose |
|----------|---------|
| `CLOUDFLARE_API_TOKEN` | Cloudflare API token with required permissions |
| `CLOUDFLARE_ACCOUNT_ID` | Cloudflare account ID for tunnel and Access operations |

#### Required for DNS and Access Tests

| Variable | Purpose |
|----------|---------|
| `CLOUDFLARE_ZONE_NAME` | Zone domain name for test DNS records (e.g., `example.com`) |

Tests construct hostnames as `e2e-{type}-{node}-{line}.{CLOUDFLARE_ZONE_NAME}`. Without this variable, DNS and Access test suites are skipped.

> **Note:** `CLOUDFLARE_ZONE_NAME` is a test-only variable. The cfgate controller does not use it. Zones are configured per CloudflareDNS resource via `spec.zones[]`.

#### Optional

| Variable | Purpose |
|----------|---------|
| `CLOUDFLARE_IDP_ID` | Identity Provider ID for IdP-dependent Access rule tests |
| `CLOUDFLARE_TEST_EMAIL` | Test email address for email rule verification |
| `CLOUDFLARE_TEST_GROUP` | Test group name for GSuite group rule verification |
| `E2E_SKIP_CLEANUP` | Set to `true` to skip resource cleanup after tests (for debugging) |
| `E2E_USE_EXISTING_CLUSTER` | Set to `true` to use existing kubeconfig cluster instead of creating kind |
| `E2E_PROCS` | Ginkgo parallel process count (default: 4) |

#### API Token Permissions

The test token needs the same permissions as a production token:

| Scope | Permission | Required For |
|-------|------------|--------------|
| Account | Cloudflare Tunnel: Edit | Tunnel lifecycle tests |
| Account | Access: Apps and Policies: Edit | Access policy tests |
| Account | Access: Service Tokens: Edit | Service token rule tests |
| Zone | DNS: Edit | DNS record sync tests |

### Running Tests

#### Prerequisites

1. Install toolchain:

```bash
brew install mise
mise install
```

2. Configure secrets (see [CONTRIBUTING.md](../CONTRIBUTING.md#secrets-configuration))

3. Bootstrap a reachable local cluster. Both paths are supported equally:

```bash
# Path A: broader local stack bootstrap
cd ~/production/abaddon
mise run 000-colima
mise run 001-kind

# Path B: repo-local convenience helper
cd ~/production/cfgate/cfgate
mise run cluster:create
```

`mise run e2e` defaults to `E2E_USE_EXISTING_CLUSTER=true`, switches to `kind-abaddon` when needed, and now fails fast if that context exists but the API server is unreachable. The test suite installs CRDs and Gateway API resources automatically if not already present.

#### Run E2E Tests

```bash
mise run e2e
```

This runs the full local suite with:
- Ginkgo parallel execution (4 procs by default, configurable via `E2E_PROCS`)
- Race detection enabled
- JSON report output to `out/reports/e2e.json`
- Filtered coverage profile to `out/coverage/e2e.coverprofile`
- Coverage instrumentation for `./api/...`, `./cmd/...`, and `./internal/...`
- Progress polling after 15s silence

E2E is intentionally local-only. The normal GitHub Actions CI workflows do not provision a cluster, do not use Cloudflare secrets, and do not upload E2E coverage to Codecov.

#### Run Specific Tests

Use the `e2e:filter` task (alias `fe2e`) to run a subset with `--focus`:

```bash
# By CRD type
mise run fe2e "CloudflareTunnel"
mise run fe2e "CloudflareDNS"
mise run fe2e "CloudflareAccessPolicy"

# Invariant tests
mise run fe2e "Invariants"
mise run fe2e "INV-T"
mise run fe2e "deletion invariants"

# Annotations
mise run fe2e "HTTPRoute Annotations"

# Multi-CRD interactions
mise run fe2e "Combined"

# CEL validation (no Cloudflare API needed)
mise run fe2e "CEL Validation"
```

The filter argument is a Ginkgo `--focus` regex. It is required.

#### Adjust Parallelism

```bash
# Single process (useful for debugging ordering issues)
E2E_PROCS=1 mise run e2e

# Higher parallelism (if your API token rate limits allow)
E2E_PROCS=8 mise run e2e
```

#### Cleanup Orphaned Resources

If tests fail or `E2E_SKIP_CLEANUP=true` was set, resources may be left in Cloudflare. The cleanup utility removes them:

```bash
mise run e2e:cleanup
```

This scans for and deletes:
- Tunnels with `e2e-` or `recovery-` name prefix
- DNS records containing `e2e-` or `_cfgate.e2e-` in the name
- Access applications with `e2e-` name prefix
- Service tokens with `e2e-` name prefix

Run cleanup before E2E tests to ensure a clean slate if previous runs left orphans.

### Test Structure

```
test/e2e/
  e2e_suite_test.go     # Suite setup, framework init, cleanup helpers
  helpers_test.go       # Wait functions, resource creators, CF API verifiers
  tunnel_test.go        # CloudflareTunnel lifecycle (17 specs)
  dns_test.go           # CloudflareDNS sync, policies, ownership (19 specs)
  access_test.go        # CloudflareAccessPolicy rules and applications (26 specs)
  annotations_test.go   # HTTPRoute annotation parsing and propagation (16 specs)
  combined_test.go      # Multi-CRD interaction and cross-resource tests (7 specs)
  invariants_test.go    # Structural invariants across all CRDs (10 specs, 45 assertions)
  validation_test.go    # CEL validation rules, no Cloudflare API needed (12 specs)
```

#### Test Naming Convention

Resources created during tests follow the pattern:

```
e2e-{type}-{node}-{line}
```

The `{line}` component is the Ginkgo spec's source line number, making names deterministic and reproducible across runs. This ensures parallel test nodes do not collide and orphaned resources are identifiable.

### Test Patterns

#### SpecTimeout

Every spec that calls the Cloudflare API uses `SpecTimeout` to prevent hangs:

```go
It("creates CNAME record pointing to tunnel domain", SpecTimeout(6*time.Minute), func(ctx SpecContext) {
    // ctx is cancelled when SpecTimeout fires
})
```

Typical timeouts:
- Tunnel operations: 3-5 minutes (tunnel creation is the slowest API call)
- DNS operations: 6 minutes (propagation verification)
- Access operations: 3-5 minutes
- Validation-only specs: no timeout needed (no API calls)

#### Conflict Retry (Eventually + Get/Update)

When updating a resource that the controller may also be reconciling, wrap the Get/Update in `Eventually` to retry on 409 Conflict:

```go
// Use Eventually to retry on conflict (controller may update status concurrently)
Eventually(func() error {
    var current cfgatev1alpha1.CloudflareTunnel
    if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(tunnel), &current); err != nil {
        return err
    }
    current.Spec.Cloudflared.Image = "cloudflare/cloudflared:2026.3.0"
    return k8sClient.Update(ctx, &current)
}, DefaultTimeout, DefaultInterval).Should(Succeed())
```

The `func() error` form is the standard pattern for conflict retry. The fresh `Get` inside the loop fetches the latest `resourceVersion` on each attempt.

For assertion-heavy waits (where you need multiple `Expect` calls), use the `func(g Gomega)` form instead:

```go
Eventually(func(g Gomega) {
    var tunnel cfgatev1alpha1.CloudflareTunnel
    g.Expect(k8sClient.Get(ctx, key, &tunnel)).To(Succeed())
    g.Expect(tunnel.Status.TunnelID).NotTo(BeEmpty())
}, DefaultTimeout, DefaultInterval).Should(Succeed())
```

Never use bare `Get` followed by `Expect(Update).To(Succeed())`; the controller will race you.

#### Wait Helpers

`helpers_test.go` provides typed wait functions for all resources:

| Helper | Waits For |
|--------|-----------|
| `waitForTunnelReady` | Tunnel status Ready=True |
| `waitForTunnelCondition` | Specific condition on tunnel |
| `waitForTunnelDeleted` | Tunnel removed from K8s |
| `waitForTunnelDeletedFromCloudflare` | Tunnel removed from Cloudflare API |
| `waitForDeploymentSpec` | cloudflared Deployment matches expected replicas |
| `waitForDNSReady` | DNS status Ready=True (defined in dns_test.go) |
| `waitForDNSDeleted` | DNS resource removed from K8s (defined in dns_test.go) |
| `waitForAccessPolicyReady` | Access policy status Ready=True |
| `waitForAccessPolicyDeleted` | Access policy removed from K8s |
| `waitForAccessApplicationDeletedFromCloudflare` | Access app removed from Cloudflare API |
| `waitForServiceTokenSecretCreated` | Service token Secret created in K8s |

#### Resource Creators

`helpers_test.go` provides typed factory functions that create resources with sensible defaults:

| Creator | Creates |
|---------|---------|
| `createCloudflareTunnel` | CloudflareTunnel with standard config |
| `createCloudflareDNSWithGatewayRoutes` | CloudflareDNS with gateway route discovery |
| `createCloudflareAccessPolicy` | Basic Access policy |
| `createCloudflareAccessPolicyWith*` | Access policy with specific rule type (IP, country, email, OIDC, GSuite) |
| `createGatewayClass` | GatewayClass for cfgate |
| `createGateway` | Gateway with tunnel reference |
| `createHTTPRoute` | HTTPRoute with hostname and backend |
| `createTestService` | ClusterIP Service for backends |

#### Invariant Tests

Invariant tests (`invariants_test.go`) verify structural properties that MUST hold whenever a resource reaches a known state. Unlike scenario tests ("do X, expect Y"), invariant tests verify "whenever state S holds, properties P1..Pn MUST hold" regardless of how the resource reached that state.

Eight test contexts cover 45 assertions:

| Context | IDs | What it verifies |
|---------|-----|-----------------|
| CloudflareTunnel Ready | INV-T1..T9 | Sub-conditions, TunnelID, TunnelDomain format, finalizer, deployment, config-hash |
| CloudflareDNS Ready | INV-D1..D8 | Sub-conditions, SyncedRecords, ResolvedTarget, CF API CNAME, OwnershipVerified |
| CloudflareAccessPolicy Ready | INV-A1..A8 | Sub-conditions, ApplicationID, targets, finalizer, CF API app |
| Gateway status | INV-GW1..GW4 | Accepted, Programmed, addresses, supportedKinds |
| HTTPRoute parent status | INV-HR1..HR3 | parents[] controllerName, Accepted, ResolvedRefs |
| GatewayClass | INV-GC1..GC2 | controllerName match, Accepted |
| Cross-CRD consistency | INV-X1..X3 | DNS/tunnel domain, CNAME content, credential inheritance chain |
| Deletion cleanup | INV-DEL1..DEL4 | Namespace trigger, tunnel delete, DNS removal, Access app removal |

The invariant test context is `Ordered`; specs share a tunnel, GatewayClass, and Gateway. A failure in an early spec cascades to skip all subsequent specs in the context.

### Skipped Tests

Some tests are skipped when optional environment variables are missing:

| Missing Variable | Skipped Tests |
|-----------------|---------------|
| `CLOUDFLARE_ZONE_NAME` | All DNS tests, Access tests with hostnames, annotation tests |
| `CLOUDFLARE_IDP_ID` | IdP-dependent Access rule tests (OIDC claims, GSuite groups) |
| `CLOUDFLARE_TEST_EMAIL` | Email-based Access rule tests |
| `CLOUDFLARE_TEST_GROUP` | GSuite group-based Access rule tests |

### Test Output

After running `mise run e2e`:

| File | Contents |
|------|----------|
| `out/reports/e2e.json` | Ginkgo JSON report with pass/fail per spec |
| `out/coverage/e2e.coverprofile` | Local E2E Go coverage profile |

## Coverage

Use the local aggregate task to run both unit and E2E coverage:

```bash
mise run coverage
```

Both canonical coverage profiles filter out `api/v1alpha1/zz_generated.deepcopy.go` so local `go tool cover` output matches the hand-written-code coverage contract. CI uploads only `out/coverage/unit.coverprofile` to Codecov.

## Profiling

Local profiling and benchmarking tasks are available through `mise`:

```bash
mise run bench
mise run profile:bench
mise run profile:view out/profiles/bench.cpu.pb.gz
mise run profile:export out/profiles/bench.cpu.pb.gz
mise run smoke
```

- `bench` runs the benchmark suite with `-benchmem`
- `profile:bench` writes CPU and heap profiles under `out/profiles/`
- `profile:view` launches the pprof web UI
- `profile:export` writes `top`, `tree`, and `proto` outputs beside the selected profile
- `smoke` runs a fast local build-plus-test sanity pass

# Testing

cfgate tests across two tiers: unit tests for pure functions and E2E tests against the live Cloudflare API.

## Philosophy

- **Real API for E2E.** Every E2E test creates and verifies actual Cloudflare resources (tunnels, DNS records, reusable Access policies, Access applications, Access owner tags, service tokens). No mocks, no fixtures, no VCR. Controller reconciliation patterns are incompatible with cassette approaches (attempted and removed).
- **Pure-function unit tests.** Drift detection, status composition, annotation parsing, context transformation, caching, predicates, feature detection, and cloudflared builders are tested in isolation with table-driven Ginkgo specs.
- **API state verification.** E2E tests verify that Kubernetes CRD state and Cloudflare API state converge correctly.

## Unit Tests

Unit coverage is the primary CI coverage signal. The default unit test surface includes:

- `./api/...` for hand-written scheme registration and package initialization
- `./cmd/...` for manager and cleanup entrypoint orchestration
- `./internal/...` for controller helpers, annotations, status, feature detection, cloudflared builders, and Cloudflare client logic

Current tunnel correctness coverage includes:

- cloudflared metrics default port `44483` and `metrics.enabled: false` omission of args, ports, and HTTP probes
- `caPoolSecretRef` Secret volume/item/mount generation and global `originRequest.caPool`
- managed per-route `caPool`, `originServerName`, host header, TLS verify, HTTP/2, and h2c origin request propagation
- `CloudflareOriginPolicy` target status, conflict handling, and origin request precedence
- Gateway API `BackendTLSPolicy` validation/status and backend TLS origin request mapping
- named origin CA pool refs, managed/unmanaged CA path validation, and generated CA Secret materialization
- `cfgate.io/hostname` override for listener compatibility plus tunnel/DNS route discovery
- HTTPRoute path translation to anchored cloudflared regexes
- cross-namespace backend `Service` `ReferenceGrant` enforcement
- HTTPRoute unsupported backend status for multiple backendRefs and non-Service backend group/kind values
- CloudflareAccessApplication runtime validation for stale non-`self_hosted` application types

E2E image assumptions use the cfgate cloudflared fork. h2c-specific E2E assertions require `ghcr.io/inherent-design/cloudflared:*-h2c.*`; upstream cloudflared image overrides are no-h2c mode only.

```bash
mise run test          # unit tests
mise run test:cover    # unit tests with coverage (out/coverage/unit.coverprofile)
```

## v0.3.0-alpha.1 Test Matrix

Phase 3 adds coverage for the OriginPolicy, BackendTLSPolicy, Gateway API, and cloudflared-h2c contracts documented for `v0.3.0-alpha.1`.

Unit test targets:

- accepted and rejected route emission into Cloudflare Tunnel config
- Gateway `attachedRoutes` accepted-only counting
- backend Service existence checks
- cross-namespace backend Service `ReferenceGrant` checks
- h2c invalid combinations with HTTP/2, HTTPS protocol, HTTPS service URLs, WSS service URLs, and BackendTLSPolicy
- annotation bool override parsing for `true`, `false`, `1`, `0`, `yes`, and `no`
- explicit annotation false overriding inherited true
- Cloudflare raw JSON payload preserving global and per-rule `h2cOrigin`
- origin CA pool materialization from tunnel defaults, named pools, annotations, CloudflareOriginPolicy, and BackendTLSPolicy

Optional local contract test:

- Env var: `CLOUDFLARED_H2C_BIN`
- Generate cfgate-style ingress config.
- Run `cloudflared tunnel ingress validate --config <file> --json`.
- Positive fixture: h2c HTTP origin validates.
- Negative fixture: h2c HTTPS origin fails.

E2E boundary:

- Live Cloudflare e2e is user-run only unless explicitly requested.
- h2c data-plane smoke is opt-in and release-gated.
- h2c data-plane smoke must prove the backend observed cleartext `HTTP/2.0`.
- Agent validation should stop at unit tests and optional non-live contract tests by default.

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

Tests construct hostnames as `e2e-{run}-{type}-{node}-{line}.{CLOUDFLARE_ZONE_NAME}`. Without this variable, DNS and Access test suites are skipped.

> **Note:** `CLOUDFLARE_ZONE_NAME` is a test-only variable. The cfgate controller does not use it. Zones are configured per CloudflareDNS resource via `spec.zones[]`.

#### Optional

| Variable | Purpose |
|----------|---------|
| `CLOUDFLARE_IDP_ID` | Identity Provider ID for IdP-dependent Access rule tests |
| `CLOUDFLARE_TEST_EMAIL` | Test email address for email rule verification |
| `CLOUDFLARE_TEST_GROUP` | Test group name for GSuite group rule verification |
| `E2E_SKIP_CLEANUP` | Set to `true` to skip resource cleanup after tests (for debugging) |
| `E2E_USE_EXISTING_CLUSTER` | Set to `true` to use existing kubeconfig cluster instead of creating kind |
| `E2E_RUN_ID` | Optional per-suite run ID override for Cloudflare resource names; auto-generated when unset |
| `E2E_ORPHAN_MIN_AGE` | Minimum age for stale cross-run Cloudflare orphan cleanup (default: `2h`) |
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

E2E remains excluded from normal PR and push CI because of cost and Cloudflare rate-limit pressure. Tag-triggered releases still run release-gated E2E, and the manual `Remote Release E2E` workflow provides the non-publishing remote path for timing runs and Codecov uploads.

Use GitHub Actions, select `Remote Release E2E`, set `ref` to the target branch or commit, and override `e2e_procs` only when you need a different concurrency level.

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
mise run fe2e "origin-h2c"

# Multi-CRD interactions
mise run fe2e "Combined"

# CEL validation (no Cloudflare API needed)
mise run fe2e "CEL Validation"
mise run fe2e "CloudflareTunnel validation"
```

The filter argument is passed directly through as Ginkgo's `--focus` regex. It is required.

#### Adjust Parallelism

```bash
# Single process (useful for debugging ordering issues)
E2E_PROCS=1 mise run e2e

# Higher parallelism (if your API token rate limits allow)
E2E_PROCS=8 mise run e2e
```

#### Cleanup Orphaned Resources

Each E2E suite run gets a run ID. When `E2E_RUN_ID` is unset, the suite auto-generates one so concurrent local runs do not share Cloudflare resource names. `SynchronizedBeforeSuite` removes stale `e2e-*` resources from older runs using `E2E_ORPHAN_MIN_AGE`; `SynchronizedAfterSuite` removes resources from the current run regardless of age.

Tests that intentionally preserve a remote resource during CR deletion must register `DeferCleanup` immediately after discovering the remote ID, so failed assertions still clean up the business Cloudflare account.

If tests fail or `E2E_SKIP_CLEANUP=true` was set, resources may be left in Cloudflare. The cleanup utility removes them:

```bash
mise run e2e:cleanup
```

This scans for and deletes:
- Tunnels with `e2e-` or `recovery-` name prefix
- DNS records containing `e2e-` or `_cfgate.e2e-` in the name
- Access applications with `e2e-` name prefix or domain prefix
- Reusable Access policies with `e2e-` name prefix
- Unreferenced Access owner tags matching `cfgate:<28 lowercase hex>`
- Service tokens with `e2e-` name prefix

Run cleanup before E2E tests to ensure a clean slate if previous runs left orphans.

### Test Structure

```
test/e2e/
  e2e_suite_test.go     # Suite setup, framework init, cleanup helpers
  helpers_test.go       # Wait functions, resource creators, CF API verifiers
  tunnel_test.go        # CloudflareTunnel lifecycle, default cloudflared image, and recovery paths (~20 specs)
  dns_test.go           # CloudflareDNS sync, cleanup, ownership, and fallback paths (~27 specs)
  access_test.go        # CloudflareAccessPolicy/Application bindings, paths, service tokens, and Access Application type admission
  annotations_test.go   # HTTPRoute annotation parsing and remote config propagation (16 specs)
  combined_test.go      # Multi-CRD interaction and cross-resource tests (7 specs)
  gateway_route_status_test.go # Gateway / HTTPRoute negative and status coverage, including unsupported backendRefs (9 specs)
  invariants_test.go    # Structural invariants across tunnel, DNS, Access, Gateway, and HTTPRoute (10 specs)
  validation_test.go    # CEL validation rules, no Cloudflare API needed (13 specs)
```

#### Test Naming Convention

Resources created during tests follow the pattern:

```
e2e-{run}-{type}-{node}-{line}
```

The `{run}` component scopes resources to one suite invocation, while `{line}` is the Ginkgo spec's source line number. This ensures parallel test nodes do not collide, concurrent suite runs do not delete each other's fresh Cloudflare resources, and orphaned resources remain identifiable.

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
| `waitForTunnelDeletedByIDFromCloudflare` | Tunnel removed from Cloudflare API by tunnel ID |
| `getRawTunnelConfigurationFromCloudflare` | Remote tunnel config including SDK-unknown fields such as `h2cOrigin` |
| `waitForDeploymentSpec` | cloudflared Deployment matches expected replicas |
| `waitForDNSReady` | DNS status Ready=True (defined in dns_test.go) |
| `waitForDNSDeleted` | DNS resource removed from K8s (defined in dns_test.go) |
| `waitForAccessPolicyReady` | Access policy status Ready=True |
| `waitForAccessPolicyCondition` | Specific condition on Access policy |
| `waitForAccessPolicyDeleted` | Access policy removed from K8s |
| `waitForGatewayCondition` | Specific condition on Gateway |
| `waitForHTTPRouteParentCondition` | Specific parent condition on HTTPRoute |
| `waitForAccessApplicationDeletedFromCloudflare` | Access app removed from Cloudflare API |
| `waitForServiceTokenDeletedFromCloudflare` | Service token removed from Cloudflare API |
| `waitForServiceTokenSecretCreated` | Service token Secret created in K8s |
| `waitForEventReason` | Matching Kubernetes event emitted |

#### Release-Critical Surface Checks

The E2E suite includes release-critical checks for behavior that is easy to regress across cfgate, Cloudflare API, and cloudflared fork boundaries:

- `cfgate.io/origin-h2c` is verified against Cloudflare's remote tunnel config raw JSON so SDK-unknown fields such as `h2cOrigin` are not silently dropped.
- CloudflareTunnel CEL validation rejects mutually exclusive `originDefaults.http2Origin` and `originDefaults.h2cOrigin`.
- CloudflareTunnel deployment tests assert the default cloudflared image points at the inherent-design h2c fork.

These checks cover control-plane config propagation. A public data-plane h2c smoke test is intentionally separate because it depends on DNS propagation and an externally reachable h2c backend.

The intended h2c data-plane smoke deploys an in-cluster h2c backend, publishes it through a CloudflareTunnel using the inherent-design fork image, sends a public request through Cloudflare, and asserts the backend observed cleartext `HTTP/2.0`. Keep this smoke focused and user-run because it depends on live DNS, Cloudflare edge routing, and a reachable test zone.

#### Resource Creators

`helpers_test.go` provides typed factory functions that create resources with sensible defaults:

| Creator | Creates |
|---------|---------|
| `createCloudflareTunnel` | CloudflareTunnel with standard config |
| `createCloudflareDNSWithGatewayRoutes` | CloudflareDNS with gateway route discovery |
| `createCloudflareAccessPolicy` | Basic Access policy |
| `createCloudflareAccessPolicyWith*` | Access policy with specific rule type (IP, country, email, OIDC, GSuite) |
| `createCloudflareAccessApplication` | Access application binding to reusable policies |
| `createGatewayClassWithController` | GatewayClass with explicit controllerName |
| `createGatewayClass` | GatewayClass for cfgate |
| `createGateway` | Gateway with tunnel reference |
| `createHTTPRoute` | HTTPRoute with hostname and backend |
| `createTestService` | ClusterIP Service for backends |
| `createCloudflareCredentialsSecret` | Secret with Cloudflare API token for test namespaces |

#### Invariant Tests

Invariant tests (`invariants_test.go`) verify structural properties that MUST hold whenever a resource reaches a known state. Unlike scenario tests ("do X, expect Y"), invariant tests verify "whenever state S holds, properties P1..Pn MUST hold" regardless of how the resource reached that state.

The current invariant suite covers these resource families:

| Context | IDs | What it verifies |
|---------|-----|-----------------|
| CloudflareTunnel Ready | INV-T1..T9 | Sub-conditions, TunnelID, TunnelDomain format, finalizer, deployment, config-hash |
| CloudflareDNS Ready | INV-D1..D8 | Sub-conditions, SyncedRecords, ResolvedTarget, CF API CNAME, non-fatal OwnershipVerified |
| CloudflareAccessPolicy/Application Ready | INV-A1..A8 | Reusable policy state, application IDs, targets, finalizers, CF API resources |
| Service token invariants | INV-ST1..ST4 | Service token Secret shape, status IDs, Cloudflare token presence |
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

Use the local aggregate task to run unit coverage, E2E coverage, the merged coverage ledger, and the dual-ledger assurance score:

```bash
mise run coverage
```

Use the individual tasks when you want to recompute one stage without rerunning the whole stack:

```bash
mise run coverage:merge
mise run coverage:report
mise run coverage:score
```

Both canonical source coverage profiles filter out `api/v1alpha1/zz_generated.deepcopy.go` so local `go tool cover` output matches the hand-written-code coverage contract. `out/coverage/merged.coverprofile` is the canonical `100%` coverage ledger for hand-written Go code on `main`, and `out/reports/assurance-score.json` is the canonical `200%` dual-ledger report.

In `assurance-score.json`, each rubric `possible` value is the full behavioral ceiling, while `automated_possible` is the portion the current script can verify. Today the script can verify `70/100` behavioral points, so a fully green automated behavioral run tops out at `70`, not `100`.

Normal CI uploads only `out/coverage/unit.coverprofile` to Codecov. The manual `Remote Release E2E` workflow uploads `out/coverage/e2e.coverprofile` with `e2e,manual` flags. Merged coverage and assurance scoring are local synthesis artifacts built from those canonical unit and E2E profiles.

After running `mise run coverage`:

| File | Contents |
|------|----------|
| `out/coverage/unit.coverprofile` | Unit coverage profile |
| `out/coverage/e2e.coverprofile` | E2E coverage profile |
| `out/coverage/merged.coverprofile` | Merged hand-written-code coverage ledger |
| `out/coverage/merged-summary.txt` | Totals for unit, E2E, merged coverage plus per-file merged deltas |
| `out/reports/assurance-score.json` | Dual-ledger `200%` assurance report |

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
- `smoke` builds `bin/manager`, verifies `./bin/manager --help` exits successfully, then runs a fast local package test pass

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

- cloudflared metrics default port `44483`; `metrics.enabled: false` omits the scrape port while retaining the listener and health probes
- `caPoolSecretRef` Secret volume/item/mount generation and global `originRequest.caPool`
- managed per-route `caPool`, `originServerName`, host header, TLS verify, HTTP/2, and h2c origin request propagation
- `cfgate.io/hostname` override for listener compatibility plus tunnel/DNS route discovery
- HTTPRoute path translation to anchored cloudflared regexes
- HTTPRoute rejection asserted against emitted Cloudflare configuration, including unsupported restrictions
- Source-path precedence, zero-weight rejection responses, hostname overlap, route age and rule ordering
- cross-namespace backend `Service` `ReferenceGrant` enforcement
- HTTPRoute unsupported backend status for multiple backendRefs and non-Service backend group/kind values
- CloudflareAccessApplication runtime validation for stale non-`self_hosted` application types

E2E image assumptions use the cfgate cloudflared fork. h2c-specific E2E assertions require `ghcr.io/inherent-design/cloudflared:*‑h2c.*`; upstream cloudflared image overrides are no-h2c mode only.

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

Tests construct hostnames as `e2e-{run}-{type}-{node}-{line}.{CLOUDFLARE_ZONE_NAME}`. Without this variable, DNS and Access test suites are skipped.

> **Note:** `CLOUDFLARE_ZONE_NAME` is a test-only variable. The cfgate controller does not use it. Zones are configured per CloudflareDNS resource via `spec.zones[]`.

#### Optional

| Variable | Purpose |
|----------|---------|
| `CLOUDFLARE_IDP_ID` | Identity Provider ID for IdP-dependent Access rule tests |
| `CLOUDFLARE_TEST_EMAIL` | Test email address for email rule verification |
| `CLOUDFLARE_TEST_GROUP` | Test group name for GSuite group rule verification |
| `E2E_SKIP_CLEANUP` | Set to `true` to skip resource cleanup after tests (for debugging) |
| `E2E_PUBLIC_DNS_RESOLVER` | Optional `host:port` resolver for the public h2c probe, such as `1.1.1.1:53`; defaults to the system resolver and does not change system or cluster DNS |
| `E2E_USE_EXISTING_CLUSTER` | Set to `true` to use existing kubeconfig cluster instead of creating kind |
| `E2E_RUN_ID` | Optional 1-20 lowercase alphanumeric run ID for names and namespace labels; auto-generated when unset |
| `E2E_ORPHAN_MIN_AGE` | Positive minimum age for explicitly enabled orphan cleanup (default: `2h`); unknown ages are preserved |
| `E2E_CLEAN_ORPHANS` | Set to `true` for deliberate stale test-resource cleanup across runs; default is current-run teardown only |
| `E2E_CLEANUP_APPLY` | Set to `true` with `E2E_CLEAN_ORPHANS=true` to apply the cleanup CLI inventory; the CLI previews by default |
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

3. Start Docker or Colima. The default E2E task creates and removes its own kind cluster. These existing-cluster bootstrap paths remain available for explicitly selected disposable development clusters:

```bash
# Path A: broader local stack bootstrap
cd ~/production/abaddon
mise run 000-colima
mise run 001-kind

# Path B: repo-local convenience helper
cd ~/production/cfgate/cfgate
mise run cluster:create
```

`mise run e2e` creates a disposable kind cluster by default. Reusing a cluster requires `E2E_USE_EXISTING_CLUSTER=true` and an explicitly selected `CLUSTER_NAME`; the task fails if its API server is unreachable. The suite installs CRDs and Gateway API resources, so an existing cluster must also be disposable. Cloudflare account and zone isolation are separate from Kubernetes isolation.

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

The release and manual remote E2E workflows set `E2E_PUBLIC_DNS_RESOLVER=1.1.1.1:53` for the public h2c probe. This avoids relying on the runner stub resolver, which has returned NXDOMAIN for newly created test hostnames after successful Cloudflare record verification. Local runs keep the system resolver unless explicitly overridden. Cloudflare API record/ownership checks and the HTTPS status, origin marker, and HTTP/2 assertions remain required; system and cluster DNS are unchanged.

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

Each E2E suite run gets a run ID. When `E2E_RUN_ID` is unset, the suite auto-generates one so concurrent local runs do not share Cloudflare resource names. Default startup performs no Cloudflare cleanup; teardown selects only the current run.

Tests that intentionally preserve a remote resource during CR deletion must register `DeferCleanup` immediately after discovering the remote ID, so failed assertions still clean up their test resources.

If tests fail or `E2E_SKIP_CLEANUP=true` was set, resources may remain in Cloudflare. `mise run e2e:cleanup` previews aged orphan candidates without deleting them. Follow the explicit opt-in procedure under Test Naming Convention below after reviewing that inventory.

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

The `{run}` component scopes resources to one suite invocation, while `{line}` is the Ginkgo spec's source line number. Default startup performs no Cloudflare cleanup; teardown selects only the current run, including applications identified by their domain rather than name. Namespaces require both `cfgate.io/e2e-test=true` and the matching `cfgate.io/e2e-run` label. `E2E_SKIP_CLEANUP=true` takes precedence over cleanup opt-ins, but does not disable deliberate deletion assertions within specs.

For occasional maintenance, preview orphan candidates before applying cleanup:

```bash
mise run e2e:cleanup
E2E_CLEAN_ORPHANS=true E2E_CLEANUP_APPLY=true mise run e2e:cleanup
```

The CLI inventories again when applying. Both modes select complete generated test markers and resources older than `E2E_ORPHAN_MIN_AGE`. Unknown ages, malformed or legacy names without a complete marker, and unrelated resources are preserved. Review account, zone, names, IDs, and application tag candidates before applying. Never use the CLI as a broad account reset. `E2E_CLEAN_ORPHANS=true` also enables aged cross-run cleanup within the suite; ordinary CI leaves it unset.

Owner tags may be deleted only when attributed to selected test applications and unreferenced after a fresh complete application inventory. An arbitrary unreferenced `cfgate:*` tag is insufficient proof of test ownership and remains untouched. Failed inventories prevent deletion for that resource collection; the maintenance CLI requires a complete inventory before any deletion. Tags whose application vanished before inventory may remain for manual provenance review.

The `e2e:cleanup` command operates on Cloudflare test resources; it does not remove
local Kubernetes clusters. Suite teardown removes its own disposable cluster;
`cluster:delete` is the separate local cluster deletion task.

Run offline tooling checks without Cloudflare credentials, cluster setup, or
Ginkgo suite hooks:

```bash
mise run test:offline
```

Ordinary PR CI and release quality checks both use this task alongside race-enabled
unit tests. Cleanup regressions assert exact HTTP DELETE targets against an
in-memory transport, including another run, foreign/shared tags, failed inventories,
skip precedence, and default startup. The task also runs five-second fuzz checks
for cleanup selection and tunnel configuration budgets, plus the local build
metadata, release-reference, and release-startup script tests. It does not delete
live resources. This replaces the branch's narrower `test:e2e-cleanup` task name.

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
    current.Spec.Cloudflared.Replicas = 2
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

The `Maintenance external effects` specs additionally exercise grant revocation/restoration, annotation watches, remote drift repair, Access-required policy/application changes, a Secret-only connector rollout, missing Deployment repair, and deletion held by a nonterminal connector Pod. The h2c spec builds a disposable origin that reports its received protocol, publishes only a run-owned hostname, and verifies HTTP/2 at that origin. It requires Docker, kind, DNS propagation and live Cloudflare credentials. This establishes neither QUIC trailer support nor atomic edge protection.

Suite-managed clusters use a dedicated kubeconfig and an immutable Kubernetes 1.37.0 node image. `E2E_KIND_NODE_IMAGE` may select another digest-pinned image for explicit compatibility testing. Existing-cluster mode requires both `KUBECONFIG` and `CLUSTER_NAME`; the suite compares the selected kind API address and certificate authority before installing resources. Cleanup deletes only the suite-owned cluster and temporary kubeconfig, preserving unrelated contexts. The shared E2E task allows five minutes for interrupted cleanup, covering connector draining and bounded remote reconciliation. A second interrupt skips cleanup and requires explicit run-scoped recovery.

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

### Cloudflare request budgets

The in-process E2E manager uses the same two-minute reconciliation deadline as
`cmd/manager`. A deadline bounds one worker iteration, not time spent queued or
subsequent retries. DNS/Access and tunnel deletion phases use the existing
10-minute `LongTimeout` to allow an in-flight reconciliation, cleanup and a
retry under four-process suite load. They still require the resources to disappear;
no finalizers are stripped. Namespace termination retains its two-minute limit.
These are maximum waits, not sleeps or a production deletion SLA.

Verification clients share the controller's default 30-second attempt timeout,
bound response-body reads and permit at most two eligible SDK retries. Shared
verification helpers bound operations to two minutes; wait helpers propagate
shorter polling/spec deadlines. Paginated verification calls preserve the operation
context through the shared explicit-page iterator, including SDK retry delays. `mise run test:offline` exercises
stalled verification headers, bodies and later pages without live credentials.
Deletion warning thresholds (one minute for DNS/Access, two for tunnels) only
change event severity; cleanup retries continue until success or explicit orphaning.


The API client bounds each operation to two minutes, including pagination and retries; an earlier caller deadline takes precedence. Individual SDK attempts have a 30-second deadline and at most two retries. Explicit page fetches retain the caller context because the pinned SDK auto-pager resets it. Lists stop with an error after 1,000 nonempty pages; partial inventories are never returned as complete results. Tests exercise stalled headers and bodies, rate-limit retry waits, shutdown cancellation, and a stalled second page for every paginated operation without live credentials.

Aggregate tunnel configuration defaults to at most 1,000 ingress rules and 1 MiB of encoded ingress and origin settings. These are operator guardrails, not Cloudflare service limits. Manager overrides support larger explicitly budgeted installations; all cached clients receive the same immutable settings. Rejected configurations leave the previous remote configuration unchanged rather than publishing a truncated rule set. Rule-count, byte-boundary, default/override, and no-outbound-write tests run in ordinary CI, with a bounded configuration fuzz target. `BenchmarkTunnelConfigurationBudget` measures validation allocation and processing cost at 1, 100, and 1,000 rules; it does not establish end-to-end routing throughput.

## Release artifact verification

The image job mounts `binfmt_misc` on the Linux runner host before installing
QEMU, then checks the retained ARM64 handler and reported platform support.
Without the host mount on a fresh runner, registrations can disappear when the
installer container exits even though setup reports success. The later ARM64
container smoke test then fails with `exec format error` before scanning. These
preflight checks do not replace running both exact release images below.

The release workflow resolves a validated semantic-version tag to a commit once.
Quality, E2E, and image jobs check out that commit. The quality job runs formatting checks, lint,
race tests, cleanup effects, bounded fuzzing, and release-ref contracts. The image job builds both architectures
into one OCI archive, retaining BuildKit provenance and SBOM attestations. It
extracts each platform without changing its manifest digest, verifies binary
version/source metadata, and smoke-tests both architectures. Each image runs with
networking disabled and an explicitly absent kubeconfig; its JSON startup record
must contain the exact release version, source commit, and build date before the
expected missing-kubeconfig exit. Go build metadata separately verifies the
toolchain, operating system, and architecture. This preserves reproducible
`-trimpath` builds, which omit linker arguments from Go build metadata. Trivy scans the
single-platform OCI layout directories and fails on fixable HIGH or CRITICAL
OS/library vulnerabilities. Failed scans or missing attestations block promotion.

After E2E and both scans pass, the publication job verifies the source tag again,
checks the archive checksum and OCI index digest, and promotes that same index
with all platform and attestation manifests. It does not rebuild the images.
GitHub provenance, cosign signatures, generated manifests, release notes, and
Artifact Hub metadata remain part of publication. Scheduled monitoring scans
both published architectures with the same severity gate.

`bash .github/scripts/test-release-ref.sh` checks valid release channels,
malformed versions, literal shell payloads, and mismatched tag/checkout commits
without credentials or publication. `mise run test:offline` runs all three script
tests locally and in PR CI and release quality checks.
`bash .github/scripts/test-release-startup.sh` rejects missing, duplicate or incorrect
runtime metadata and unexpected startup exits without starting a controller.
`bash .github/scripts/test-build-metadata.sh` uses a disposable Git repository to
check untagged, exact-tag, later-commit and version-suffix behavior shared by local
binary and Docker tasks. CI watches `hack/` changes and runs these contracts.
Local OCI fixture tests establish archive mechanics only; final cfgate images
must still pass the actual release checks after dependency updates. Creating the
cfgate release tag remains gated on the user's final release review for alpha.6.

Cleanup SDK clients are scoped to one operation and have a 30-second HTTP attempt
limit. Explicit-page listing keeps SDK requests and retry delays within the
operation context; a scoped transport also bounds body reads. Earlier deadlines win;
operation cancellation closes response bodies. Mock HTTP tests stall second-page
headers and bodies and prove cleanup stops within its operation budget. These
clients are never stored in the manager credential cache.

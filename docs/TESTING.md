# Testing

Run unit and offline checks for every code change. Use live E2E to verify that
Kubernetes resources, Cloudflare state, and traffic converge as expected. The
live suite creates resources in a real Cloudflare account and a disposable kind
cluster; it needs separate credentials and cleanup planning.

## Philosophy

Unit tests isolate controller helpers, API clients, entrypoints, and configuration
builders. Offline checks use in-memory transports and bounded fault injection to
exercise cleanup selection, cancellation, retry behavior, and build contracts.
Live E2E verifies real tunnels, DNS records, Access policies and applications,
owner tags, service tokens, and selected traffic paths.

Some live tests control a precondition instead of waiting for it to occur
naturally. The renewal continuity test injects perceived token expiry, then uses
the real Cloudflare extension endpoint and authenticated origin requests. Its
scope is described under [Renewal continuity](#renewal-continuity).

## Unit Tests

Run race-enabled tests across `./api/...`, `./cmd/...`, and `./internal/...`:

```bash
mise run test
mise run test:cover
```

`test:cover` writes `out/coverage/unit.coverprofile`. Neither task needs Cloudflare
credentials or a cluster. Use `TEST_PKG` to select packages and `TEST_ARGS` to pass
additional arguments to `test`.

Coverage includes annotation parsing, status composition, route acceptance and
emitted configuration, ReferenceGrant enforcement, cloudflared defaults and TLS
settings, API deadlines, and tunnel configuration budgets. Check relevant
negative cases as well as readiness when changing reconciliation behavior.

Run offline cleanup, verification, fuzz, and script checks separately:

```bash
mise run test:offline
```

This task bypasses live Ginkgo suite setup. It runs race-enabled cleanup and E2E
verification helper tests, five-second fuzz targets for cleanup selection and
tunnel configuration budgets, and build-metadata, release-ref, and
release-startup script tests. HTTP transport tests check exact deletion targets,
foreign/shared tags, failed inventories, skip precedence, stalled responses, and
cancellation. It does not delete live resources.

## E2E Tests

The suite runs the controller in-process against kind and the live Cloudflare
API. Start Docker, install the tools with `mise install`, and configure
[encrypted secrets](../CONTRIBUTING.md#secrets-configuration) before running it.

### Environment Variables

E2E tasks load `secrets.enc.yaml` and `.env`; shell environment values also supply
runtime options. The release preflight requires all six Cloudflare values below,
while local runs may skip cases that need optional values.

| Variable | Requirement and purpose |
|----------|-------------------------|
| `CLOUDFLARE_API_TOKEN` | Required API token |
| `CLOUDFLARE_ACCOUNT_ID` | Required account for tunnel and Access operations |
| `CLOUDFLARE_ZONE_NAME` | Required for DNS and hostname-dependent Access/annotation tests |
| `CLOUDFLARE_IDP_ID` | Required for IdP-dependent Access rule tests |
| `CLOUDFLARE_TEST_EMAIL` | Required for email rule tests |
| `CLOUDFLARE_TEST_GROUP` | Required for GSuite group rule tests |

`CLOUDFLARE_ZONE_NAME` is test-only. The controller selects zones from each
CloudflareDNS resource's `spec.zones[]`.

| Option | Behavior and default |
|--------|----------------------|
| `E2E_PROCS` | Ginkgo process count; local default `4` |
| `E2E_RUN_ID` | Run identifier of 1 to 20 lowercase alphanumeric characters; generated when unset |
| `E2E_USE_EXISTING_CLUSTER` | Set `true` to reuse an explicitly selected disposable kind cluster; default `false` |
| `CLUSTER_NAME` | Required cluster name in existing-cluster mode |
| `KUBECONFIG` | Existing cluster kubeconfig; the mise wrapper defaults to `~/.kube/config` |
| `E2E_KIND_NODE_IMAGE` | Override the suite's digest-pinned Kubernetes 1.37.0 image with another digest-pinned image |
| `E2E_PUBLIC_DNS_RESOLVER` | Optional `host:port` resolver for the public h2c probe; system resolver by default |
| `E2E_SKIP_CLEANUP` | Set `true` to preserve test resources for debugging |
| `E2E_ORPHAN_MIN_AGE` | Positive minimum age for orphan selection; default `2h` |
| `E2E_CLEAN_ORPHANS` | Set `true` to enable aged cross-run cleanup; unset by default |
| `E2E_CLEANUP_APPLY` | Set `true` with `E2E_CLEAN_ORPHANS=true` to apply the cleanup CLI inventory; preview is the default |

#### API Token Permissions

The token needs Account permissions for Cloudflare Tunnel: Edit, Access: Apps
and Policies: Edit, and Access: Service Tokens: Edit, plus Zone DNS: Edit and Zone: Read for the
selected zone. See [token setup](../CONTRIBUTING.md#api-token-permissions).

### Running Tests

#### Prerequisites

The default suite creates a kind cluster with a dedicated temporary kubeconfig
and deletes that cluster during teardown. It does not require a separate
bootstrap repository. Cloudflare account and zone isolation remain your
responsibility.

To reuse a disposable development cluster, choose its name explicitly:

```bash
CLUSTER_NAME=cfgate-dev mise run cluster:create
E2E_USE_EXISTING_CLUSTER=true CLUSTER_NAME=cfgate-dev mise run e2e
```

The wrapper checks API reachability; the suite verifies the kind API address and
certificate authority before installing CRDs and Gateway API resources. Direct
suite invocation requires both `CLUSTER_NAME` and `KUBECONFIG`. The wrapper
supplies the default kubeconfig when omitted and restores a previous context
when it switched one. Existing-cluster teardown does not delete the cluster;
use `CLUSTER_NAME=cfgate-dev mise run cluster:delete` when finished.

#### Run E2E Tests

Check release credential presence and run the full suite:

```bash
mise run e2e:preflight
mise run e2e
```

Preflight checks presence only, not token validity or permissions. Local runs can
omit preflight when deliberately running a partial suite with optional cases
skipped.

The local task uses four Ginkgo processes by default, race detection, progress
polling after 15 seconds of silence, and coverage instrumentation for `./api/...`,
`./cmd/...`, and `./internal/...`. It writes a JSON report and a filtered coverage
profile under `out/`.

The task allows five minutes for interrupted cleanup, including connector
draining and bounded remote reconciliation. A second interrupt skips remaining
cleanup and may leave resources that require explicit recovery. Cleanup removes
only the suite-owned cluster and temporary kubeconfig; unrelated contexts and
existing clusters are preserved.

Release and manual remote workflows set `E2E_PUBLIC_DNS_RESOLVER=1.1.1.1:53` for
the public h2c probe to avoid stale runner DNS results. This affects that probe
only; system and cluster DNS remain unchanged. Cloudflare ownership checks and
HTTPS status, origin marker, and origin HTTP/2 assertions still apply.

#### Run Specific Tests

Pass a required Ginkgo focus regex to `e2e:filter` or its `fe2e` alias:

```bash
mise run e2e:filter -- 'CloudflareTunnel'
mise run fe2e 'CloudflareDNS'
mise run fe2e 'Invariants'
mise run fe2e 'origin-h2c'
mise run e2e:filter -- 'reloads origin CA trust'
```

CEL validation cases do not call Cloudflare, but filtering them still uses the
E2E task and suite setup. Use the unit and offline tasks when no live environment
is available.

#### Adjust Parallelism

Set the process count for a debugging run:

```bash
E2E_PROCS=1 mise run e2e
```

Higher counts increase Cloudflare request pressure. The manual `Remote Release
E2E` workflow defaults to two processes and accepts an `e2e_procs` input.

#### Cleanup Orphaned Resources

Default startup performs no Cloudflare cleanup. Teardown selects the current run
only. `E2E_SKIP_CLEANUP=true` takes precedence over cleanup opt-ins, but does not
disable deliberate deletion assertions inside tests.

Preview aged orphan candidates before considering deletion:

```bash
mise run e2e:cleanup
```

Review the account, zone, names, IDs, and application tag candidates. Apply only
that intended maintenance operation with both explicit opt-ins:

```bash
E2E_CLEAN_ORPHANS=true E2E_CLEANUP_APPLY=true mise run e2e:cleanup
```

The CLI inventories again before applying and requires a complete inventory
before any deletion. It preserves unknown ages, malformed or legacy names,
and unrelated resources. `E2E_CLEAN_ORPHANS=true` also enables aged cross-run
cleanup inside the suite; ordinary CI leaves it unset. Never use these commands
as a broad account reset. The CLI deletes Cloudflare test resources, not local
Kubernetes clusters.

### Test Structure

Tests live in [test/e2e/](../test/e2e/). Resource suites cover tunnel, DNS, Access,
annotations, combined behavior, route status, invariants, and CEL validation.
`maintenance_test.go` covers external effects and traffic; `token_renewal_test.go`
contains renewal continuity support. Shared setup and helpers are in
`e2e_suite_test.go` and `helpers_test.go`.

#### Test Naming Convention

Resource names and hostnames use `e2e-{run}-{type}-{node}-{line}`, with the zone
appended for hostnames. The run ID isolates concurrent invocations; `line` is the
Ginkgo spec source line. Namespace cleanup requires both
`cfgate.io/e2e-test=true` and the matching `cfgate.io/e2e-run` label. Application
selection also recognizes run-owned domains.

Orphan cleanup requires a complete generated marker and age greater than
`E2E_ORPHAN_MIN_AGE`. Owner tags are deleted only when attributed to selected test
applications and unreferenced after a fresh, complete application inventory. An
unreferenced `cfgate:*` tag alone is insufficient ownership evidence. Failed
inventories prevent deletion for the affected collection. Tags whose application
disappeared before inventory may need manual provenance review.

### Test Patterns

#### SpecTimeout

Bound each live test and pass its context to API calls. Shared ordered fixtures
need bounded `BeforeAll` and `DeferCleanup` contexts; cleanup must not inherit
the final test's nearly exhausted deadline. Invariant tests retain their
individual timeouts and give shared cleanup `LongTimeout`.

Register `DeferCleanup` as soon as a test learns the ID of a remote resource it
intentionally preserves during CR deletion. This keeps cleanup available when a
later assertion fails.

#### Renewal continuity

The test first establishes two consecutive authenticated origin responses within
a five-minute readiness budget. It then makes the selected real token appear due
for renewal by replacing its expiry in the returned inventory and inserts a
five-second delay around the actual Cloudflare extension call. The adapter uses
the real token and existing duration; it does not wait for natural token expiry.

The test samples real authenticated traffic before, during, and after extension,
checks response status and origin content, and preserves every sampling failure.
Readiness alone cannot satisfy it. The result covers this controlled renewal
sequence, not arbitrary edge convergence, natural expiry, or a universal
zero-downtime guarantee. Offline probe regressions run in `test:offline`.

#### Conflict Retry (Eventually + Get/Update)

Fetch the current resource inside each retry so an update uses the latest
`resourceVersion`:

```go
Eventually(func() error {
    var current cfgatev1alpha1.CloudflareTunnel
    if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(tunnel), &current); err != nil {
        return err
    }
    current.Spec.Cloudflared.Replicas = 2
    return k8sClient.Update(ctx, &current)
}, DefaultTimeout, DefaultInterval).Should(Succeed())
```

Use `func(g Gomega)` for waits with multiple assertions. Avoid a single Get/Update
assertion when the controller may concurrently update status.

#### Wait Helpers

Reuse typed helpers in `helpers_test.go` and the relevant resource suite. Wait
for the condition or remote state the behavior promises. For deletion, verify
remote disappearance as well as Kubernetes deletion. Raw tunnel configuration
helpers retain SDK-unknown fields such as `h2cOrigin`.

#### Release-Critical Surface Checks

The suite checks remote `h2cOrigin`, mutually exclusive HTTP/2 and h2c origin
settings, and the default inherent-design cloudflared fork image. h2c traffic
assertions require the fork's `ghcr.io/inherent-design/cloudflared:*-h2c.*`
images; upstream image overrides support no-h2c mode only.

Maintenance cases cover grant revocation/restoration, annotation watches, remote
drift repair, Access-required changes, Secret-triggered connector rollout,
missing Deployment repair, and deletion held by a nonterminal connector Pod.
The public h2c probe checks the protocol received by a disposable origin on a
run-owned hostname. It does not establish QUIC trailer support or atomic edge
protection.

Origin trust rotation changes the connector CA Secret, checks automatic rollout
and rejection of the old certificate, then verifies acceptance of the replacement
certificate and continued rejection of the old one. The focused command appears
under [Run Specific Tests](#run-specific-tests).

#### Resource Creators

Use the shared factories for Cloudflare resources, GatewayClass, Gateway,
HTTPRoute, backend Services, and credential Secrets. Preserve generated run
markers and cleanup registration when extending them.

#### Invariant Tests

`invariants_test.go` checks properties of ready resources: status consistency,
remote IDs, finalizers, connector deployment, DNS ownership, Access bindings,
service token Secrets, Gateway and route conditions, and cross-resource values.
It also checks deletion cleanup. Ordered contexts share fixtures; an early
failure can skip subsequent specs, so a report with skips is not full coverage.

### Skipped Tests

Missing zone, IdP, email, or group values skip dependent local cases. Review the
Ginkgo report rather than interpreting a successful partial run as release
coverage. Release workflows run the six-value preflight first.

### Test Output

| Artifact | Contents |
|----------|----------|
| `out/reports/e2e.json` | Ginkgo report with per-spec outcomes |
| `out/coverage/e2e.coverprofile` | Filtered E2E Go coverage profile |

## Coverage

Run the aggregate task when the live environment is configured:

```bash
mise run coverage
```

It runs unit and E2E coverage, merges them, writes a summary, and scores assurance.
To recompute from existing inputs, use `coverage:merge`, `coverage:report`, and
`coverage:score` individually. Use profiles and reports from the same source
revision when interpreting their combined result.

| Artifact | Contents |
|----------|----------|
| `out/coverage/unit.coverprofile` | Unit coverage |
| `out/coverage/e2e.coverprofile` | Live E2E coverage |
| `out/coverage/merged.coverprofile` | Combined hand-written Go coverage |
| `out/coverage/merged-summary.txt` | Totals and per-file coverage deltas |
| `out/reports/assurance-score.json` | Code and behavioral assurance ledgers |

Both source profiles exclude `api/v1alpha1/zz_generated.deepcopy.go`. The merged
profile is the canonical coverage ledger, with a 100% ceiling; this does not
claim that current coverage is 100%. The assurance report combines that ledger
with a separate 100-point behavioral rubric, giving a 200-point ceiling. Each
`possible` value is the full rubric ceiling; `automated_possible` is the portion
the script can verify. Behavioral automation currently covers 70 of 100 points.
A green automated run cannot establish the remaining 30.

Normal CI uploads unit coverage to Codecov. Release E2E uploads the E2E profile
with `e2e,release` flags; the manual workflow uses `e2e,manual`. Merged coverage and
assurance scores are local synthesis artifacts.

## Profiling

Run benchmarks or capture profiles before comparing a performance change:

```bash
mise run bench
mise run profile:bench
mise run profile:view out/profiles/bench.cpu.pb.gz
mise run profile:export out/profiles/bench.cpu.pb.gz
```

`bench` reports allocations; `profile:bench` writes CPU and heap profiles under
`out/profiles/`. `profile:view` starts a local pprof UI, and `profile:export`
writes text and proto views beside the selected profile.

`mise run smoke` builds `bin/manager`, requires its help command to exit
successfully, and runs package tests. It does not exercise live reconciliation
or establish the cleanup executable's CLI contract.

### Cloudflare request budgets

Controller and verification operations have a two-minute deadline, including
pagination and retries; earlier caller deadlines win. SDK attempts have a
30-second deadline and at most two eligible retries. Explicit page iteration
preserves caller cancellation, including retry waits. Lists reject more than
1,000 nonempty pages and never present a partial inventory as complete.

The in-process E2E manager uses the production reconciliation deadline. This
bounds one worker iteration, not time queued or later retries. DNS/Access and
tunnel deletion waits use the ten-minute `LongTimeout` to cover an in-flight
reconcile, cleanup, and retry under parallel load. Namespace termination retains
its two-minute limit. Tests require disappearance without stripping finalizers.
These maximum waits are not production deletion guarantees. Warning thresholds
change event severity, not whether cleanup retries continue.

Cleanup clients are operation-scoped, retain cancellation through page fetches
and body reads, and never enter the manager credential cache. Offline tests
exercise stalled headers, bodies, later pages, rate-limit waits, and shutdown
cancellation without credentials.

Tunnel configuration defaults to at most 1,000 ingress rules and 1 MiB of encoded
ingress and origin settings. These are operator guardrails, not Cloudflare service
limits. Manager overrides apply consistently to cached clients. The API adapter
rejects invalid payloads before sending them. The tunnel controller separately
handles budget errors by attempting a bounded HTTP 503 replacement, including
the fallback; this withdraws forwarding rather than keeping an over-limit plan.
Unit and fuzz tests cover adapter limits and absence of outbound writes. `BenchmarkTunnelConfigurationBudget` measures
validation cost at 1, 100, and 1,000 rules; it does not measure routing throughput.

## Release artifact verification

Normal PR/push CI does not provision live E2E. The manual `Remote Release E2E`
workflow accepts a branch or commit in `ref`, records timing, and uploads reports
and coverage without publishing. Tag-triggered releases use a separate gate in
[release.yml](../.github/workflows/release.yml).

The release workflow resolves a validated semantic-version tag to one commit.
Quality, E2E, and image jobs check out that commit. Quality checks run formatting,
lint, race tests, cleanup effects, bounded fuzzing, and build/release script
contracts. Publication requires those checks, live E2E, and both image scans.

The image job mounts host `binfmt_misc` before QEMU setup and verifies the
retained ARM64 handler and platform support. It then builds amd64 and arm64 into
one OCI archive with BuildKit provenance and SBOM attestations. Each platform is
extracted without changing its manifest digest. Both exact images run a startup
smoke test with networking disabled and an absent kubeconfig; the JSON startup
record must report the expected version, source commit, and build date before
the expected missing-kubeconfig exit. Go build metadata independently checks the
toolchain, operating system, and architecture.

Trivy scans both single-platform OCI layouts and rejects fixable HIGH or CRITICAL
OS/library vulnerabilities. Failed scans or missing attestations block promotion.
QEMU setup success and local OCI fixture tests do not replace these exact-image
checks.

Publication revalidates the tag, archive checksum, and OCI index digest, then
promotes the same index and all platform/attestation manifests without rebuilding.
Docker login supplies the shared registry credentials used by Skopeo, GitHub
attestation, and cosign. Publication also produces signatures, generated
manifests, release notes, and Artifact Hub metadata. Scheduled monitoring scans
both published architectures with the same severity gate.

The offline task includes these executable contracts:

| Script | Checks |
|--------|--------|
| `.github/scripts/test-release-ref.sh` | Release channels, malformed versions, literal shell payloads, and tag/checkout mismatch |
| `.github/scripts/test-release-startup.sh` | Missing, duplicate, or wrong runtime metadata and unexpected startup exits |
| `.github/scripts/test-build-metadata.sh` | Untagged, exact-tag, later-commit, and version-suffix behavior in a disposable Git repository |

These checks need no credentials and publish nothing. Dependency changes still
require the final cfgate images to pass the actual release gates.

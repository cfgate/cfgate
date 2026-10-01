# Compatibility and release validation

cfgate, its generated CRDs, the connector fork, Gateway API and the Helm chart are separate versioned components. A successful Go build does not establish compatibility for their deployed combination.

## Alpha.6 component pins

| Component | Candidate version | Purpose |
|---|---|---|
| cfgate | 0.2.0-alpha.6 | Controller and matching four cfgate.io/v1alpha1 CRDs |
| Go | 1.27.1 | Local and container compiler |
| Cloudflare SDK | 7.11.0 | Management API client; explicit retry/deadline settings |
| YAML | go.yaml.in/yaml/v3 3.0.5 | Maintained v3 API for connector YAML |
| Kubernetes Go libraries | 0.37.1 | api, apimachinery, client-go and apiextensions-apiserver |
| controller-runtime | 0.25.1 | Paired with Kubernetes 0.37 libraries |
| Gateway API | 1.6.2 | Go API types and installed standard bundle |
| controller-gen | 0.22.0 | DeepCopy, CRD and RBAC generation |
| Ginkgo / Gomega | 2.33.0 / 1.44.0 | Matching Ginkgo library and CLI |
| golangci-lint | 2.14.0 | Go 1.27-capable analysis |
| kind / kubectl | 0.33.0 / 1.37.1 | Disposable cluster harness and CLI |
| Kustomize | 5.8.2 | Manifest rendering |
| Trivy | 0.74.0 | Scheduled and release image vulnerability scans |
| cloudflared-h2c | 2026.9.3-h2c.1 | Required connector behavior for h2c origins |
| Helm chart | 1.5.0 | Downstream chart candidate; publish after cfgate |

The [controller-runtime 0.25.1 module](https://github.com/kubernetes-sigs/controller-runtime/blob/v0.25.1/go.mod) selects Kubernetes 0.37 and the associated dependency family. Kubernetes patch versions are aligned rather than updating client-go independently. The Cloudflare SDK is upgraded from v6.10.0 to [v7.11.0](https://github.com/cloudflare/cloudflare-go/releases/tag/v7.11.0). The tunnel API request models and pagination implementation retain the contracts used here; Access/DNS models and retry internals are rechecked through HTTP fixture and cancellation tests. The SDK-unknown `h2cOrigin` field retains its explicit wire handling. Earlier live results used v6 and do not certify v7 against Cloudflare.

The published connector index is `sha256:6c46ca006f9d6af5e973e59f2d71f5d6d3dc138c8a5484380d5797092171e3ad`, built from fork commit `d40bf36f`. Do not substitute a stock upstream image while retaining h2c configuration. An image name alone cannot certify arbitrary private-image compatibility.

## API minimum versus tested combinations

The standard Gateway API v1.6.2 installation bundle includes `admissionregistration.k8s.io/v1` ValidatingAdmissionPolicy resources, whose API is [stable since Kubernetes 1.30](https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/). Consequently, installing that entire bundle unchanged requires at least Kubernetes 1.30. This lower bound is not evidence that the full cfgate stack has passed on every later version.

[Gateway's own v1.6.2 CEL workflow](https://github.com/kubernetes-sigs/gateway-api/blob/v1.6.2/.github/workflows/crd-validation.yml) covers Kubernetes 1.32 through 1.36. Those are upstream schema checks, not cfgate release tests. Kubernetes Go library versions also do not set the API-server minimum by themselves.

| Validation | Actual scope | Candidate result |
|---|---|---|
| Unit, race, fault and authorization tests | API/controller/client behavior without live credentials | Dependency candidate passed all unit packages with race detection; rerun after later behavior changes |
| Generated schemas | controller-gen output, installed CEL admission and retained status fields | Pending final candidate validation |
| Custom cluster DNS | Disposable Kubernetes 1.35 cluster, `corp.internal`, actual rendered Service URL and HTTP request | Passed before dependency alignment; does not establish whole-stack support |
| Full live E2E | Selected disposable Kubernetes version, real scoped Cloudflare effects and connector readiness | Pending final candidate validation |
| Public h2c data plane | Origin reports HTTP/2, actual remote h2cOrigin readback | Pending final candidate validation |
| Operator images | Exact amd64 and arm64 OCI artifacts, smoke checks and fixable HIGH/CRITICAL scans | Pending final candidate validation |
| Chart integration | Final schemas/RBAC/settings and upgrade/restart on selected Kubernetes versions | Pending downstream candidate validation |

Record each tested Kubernetes server and node image explicitly. Do not infer a supported range from a single server, an upstream matrix or the chart's historical Helm `kubeVersion` admission expression. Gateway API remains an external chart prerequisite.

## Upgrade and release records

Install all four matching cfgate CRD schemas before running the updated controller, including with chart `installCRDs=false`. New persisted ownership, credential selection, lifecycle and Access dependency fields must not be pruned by old schemas. Follow the [ownership migration guide](authorization-and-ownership.md) and [Access-required guide](access-required.md); alpha.6 does not silently adopt foreign or legacy unclaimed resources.

Gateway's standard bundle installs an admission policy that restricts unsafe channel changes and downgrades. Inspect the existing installation before replacing it; do not remove shared-cluster policies as test cleanup. Release tests use explicitly selected disposable resources.

For each candidate, retain the controller source commit, Go/module versions, hashes of all four generated CRDs and RBAC, Gateway bundle version/hash, Kubernetes server/node digest, connector source/index/platform digests, operator index/platform digests, and chart source/version/package digest. Associate test and scan results with those exact artifacts. Changing an image or schema invalidates the corresponding earlier validation.

The release workflow builds once, scans and smoke-tests both platforms, then promotes the same attested OCI archive. cfgate alpha.6 publication requires user review. Chart 1.5.0 follows the released cfgate image and matching schemas; an available fork alone does not authorize cfgate publication.

## Dependency maintenance

The September 30 audit includes direct and used transitive Go modules, mise tools,
workflow actions and their separately pinned executables, generated-schema tooling,
Gateway bundle URLs, container bases, and the connector fork. Ten Renovate PRs
(#75–83 and #86) are superseded by the coordinated versions in #87. Rebase against
`origin/main` was already up to date; no dependency-only merge or history rewrite
was necessary.

The maintained [YAML v3 module](https://github.com/yaml/go-yaml) replaces cfgate's
archived `gopkg.in/yaml.v3` import without adopting the v4.0.0-rc.6 prerelease API migration.
Stable updates are applied to modules used by cfgate and its tests; Kubernetes
pseudo-version dependencies remain on the coordinated release graph. `go mod tidy`
removes obsolete direct requirements, and module verification checks downloaded
source integrity. Vulnerability checks use `govulncheck` in addition to image scans.

Workflow major updates include checkout v7, mise-action v5 and Codecov v7. The
checkout restriction on fork refs for privileged events does not affect cfgate's
current event types. mise-action v5's default 24-hour age requirement applies to
mise itself, not the pinned tools. Codecov v7 updates signature verification;
existing upload inputs and failure handling remain in place.

Renovate's built-in Dockerfile manager owns container base updates. Custom managers
cover the connector default, Trivy's workflow binary pin and Gateway installation
bundle versions. Keep the Gateway module and installed bundle aligned. The fork
remains manually reviewed: the current upstream release is still 2026.9.3, already
represented by 2026.9.3-h2c.1. Major module migrations require review of the used
API contracts; a newer version alone is not evidence of deployed compatibility.

The v7 migration exposed a retry/cancellation regression in the old SDK auto-pager:
three offline verification cases exceeded their caller deadline. Cleanup and
verification now reuse the controller's explicit-page iterator, which also bounds
page counts and rejects incomplete inventories. The existing stalled-page tests
pass with two retries enabled; the assertions and deadline limits were retained.
No live E2E rerun is claimed for this dependency update. Local validation covers
cancellation, serialization, conversion, ownership and cleanup effects; publication
still has its existing live release gate.

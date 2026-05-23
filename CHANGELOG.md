# Changelog

All notable changes to cfgate are documented in this file.


## [Unreleased]

### Bug Fixes

- **(tunnel)** Apply origin CA pool Secret mounts and remote `originRequest.caPool`
- **(tunnel)** Propagate per-route origin server name and managed CA pool annotations
- **(tunnel)** Reject route CA pool annotation paths that cfgate does not mount
- **(tunnel)** Honor route hostname overrides and listener hostname fallback in tunnel/DNS discovery
- **(tunnel)** Block cloudflared deployment when referenced origin CA Secrets or keys are missing
- **(cloudflared)** Align metrics fallback port with API/docs and honor metrics disablement
- **(gateway)** Translate HTTPRoute paths to cloudflared regexes and enforce backend ReferenceGrants
- **(gateway)** Report unsupported HTTPRoute backendRefs with `ResolvedRefs=False`
- **(access)** Reject unsupported application types and invalid policy selector combinations
- **(access)** Restrict Access Application CRD admission to `self_hosted`
- **(release)** Drop changelog writeback and tighten latest tags
- **(release)** Harden e2e preflight and coverage upload
- Correct assurance score metadata and review notes
- Harden coverage helper scripts
- Honor coverage mode in profile merge
- Fail coverage merge pipelines on awk errors

### Testing

- Add merged coverage tooling and orchestration tests
- Expand e2e coverage and drop access mtls
- Cover unsupported HTTPRoute backend status and Access Application type admission
- Drop unused e2e helper stubs

### Refactoring

- Remove retired route surface from current product

### CI/CD

- Add manual remote E2E workflow

### Other

- Prepare changelog and e2e automation
- Sync dev with main before coverage work
- Merge pull request #55 from cfgate/dev

test: add merged coverage tooling and orchestration tests

## [0.1.0-alpha.20] - 2026-04-10

### Bug Fixes

- Correct Artifact Hub shield URL
- **(manager)** Standardize CLI exit codes

### Testing

- Adopt h2c.2 and harden e2e cleanup
- Adopt h2c.2 and harden e2e cleanup

### Infrastructure

- Scope mise secrets to e2e tasks
- Update golangci-lint for Go 1.26
- Use Go 1.26 in Docker image
- Scope mise runtime defaults to tasks
- Scope mise secrets to e2e tasks
- Update golangci-lint for Go 1.26
- Use Go 1.26 in Docker image
- Scope mise runtime defaults to tasks

### CI/CD

- Add Renovate for dependency automation
- Migrate release workflow to Node 24 actions
- Gate dev coverage on lint
- Run pull request validation on dev
- Skip mise env loading in automation
- Gate dev coverage on lint
- Run pull request validation on dev
- Skip mise env loading in automation

### Maintenance

- Bump cloudflared fork to v2026.3.0-h2c.1 and document fork dependency
- Migrate cfgate automation to mise
- Migrate cfgate automation to mise

## [0.1.0-alpha.19] - 2026-03-15

### Features

- **(dns)** Support A/AAAA record types and subdomain depth warnings
- **(dns)** Implement NamespaceSelector for route filtering

### Bug Fixes

- **(access)** Order-insensitive comparison for drift detection
- **(client)** Replace string-based error detection with SDK type assertions
- **(dns)** Cross-reference TXT ownership during cleanup to prevent cross-resource deletion
- **(cloudflare)** Include email addresses in approvalGroupsEqual sort key
- Correct buildRulesFromHTTPRoute, convertAccessRules, and CEL test helper
- Block finalizer removal on cleanup failure with retry budgets
- Address PR #47 review findings
- **(e2e)** Reverse CR deletion order to respect credential dependencies

### Testing

- **(client)** Add MockClient for unit and integration testing
- Add P0 unit tests for status conditions, DNS diff, and access drift
- Add P2 unit tests for cache, predicates, features, and builders (#34, #35)

### Documentation

- Rewrite CONTRIBUTING.md to prose reference conventions
- Update TESTING.md for two-tier test strategy
- Add deletion behavior, record types, subdomain warnings, and DNS orphan E2E

### Refactoring

- **(client)** Compose Client interface from 9 domain interfaces (#26)
- **(client)** Unify param types and extract SDK conversions (#27, #28)

### CI/CD

- Bump trivy-action to 0.35.0
- Bump trivy-action to 0.35.0

### Maintenance

- Add unit test tasks and fix CONTRIBUTING.md task table
- Fix lint and format issues
- Remove unused ZoneName field from DNSRecord

## [0.1.0-alpha.18] - 2026-02-24

### Bug Fixes

- Translate h2cOrigin to CF API tunnel configuration
- Add runtime mutual exclusivity guard for h2cOrigin/http2Origin

### Testing

- Add E2E tests for h2cOrigin annotation and CEL validation

### Maintenance

- Bump default cloudflared image to 2026.2.0-h2c.2

### Other

- Merge pull request #3 from cfgate/dev

fix: translate h2cOrigin to CF API tunnel configuration

## [0.1.0-alpha.17] - 2026-02-24

### Bug Fixes

- Remove v prefix from default cloudflared image tag

## [0.1.0-alpha.16] - 2026-02-24

### Features

- Default cloudflared image to inherent-design fork

## [0.1.0-alpha.15] - 2026-02-24

### Features

- Add h2cOrigin support and fix liveness probe

### Bug Fixes

- Enforce h2cOrigin/http2Origin mutual exclusivity

### CI/CD

- **(release)** Stop marking GitHub releases as pre-release

## [0.1.0-alpha.13] - 2026-02-21

### Bug Fixes

- Sort ingress rules by path specificity, harden SDK and controllers
- **(controller)** Add isGatewayParentRef guard in findGatewaysForHTTPRoute
- DNS cleanup path, ownership matching, status constants, CEL rules
- **(crd)** Tighten DNS hostname validation per RFC 1035

### Documentation

- Rewrite README, extract service-mesh guide, add cross-references
- Apply prose style conventions across documentation

### Maintenance

- Generate changelog
- Bind container image
- Add CI workflows, ignore E2E output directory
- Generate changelog for v0.1.0-alpha.13

### Other

- Merge pull request #1 from cfgate/v0.1.0-alpha.13

v0.1.0-alpha.13

## [0.1.0-alpha.12] - 2026-02-19

### Features

- Add cosign keyless signing to container releases

### Bug Fixes

- Unmark GH release as pre-release

### Maintenance

- **(actions)** Add timesouts, pin versions

## [0.1.0-alpha.11] - 2026-02-19

### Features

- **(site)** Add "images"
- New image release tags for AH

### CI/CD

- Add Artifact Hub metadata and annotations

### Maintenance

- **(site)** Update packages
- **(site)** Update images
- Rename Cloudflare worker

### Other

- Inherent-design/cfgate -> cfgate/cfgate

## [0.1.0-alpha.10] - 2026-02-17

### Features

- Scaffold Astro alongside Hono worker
- **(site)** Wire static assets and OG tags into Astro layout
- **(site)** Integrate brand design system, i18n, and Starwind UI
- **(site)** Add Hindi translation, fix zh locale label

### Bug Fixes

- **(test)** Add fallback credentials to deletion invariant DNS resource
- Patch task scripts for empty-arg bug, fragile cd, contract docs
- **(site)** Update English subtitle to match translation structure

### Testing

- Fix invariant assertion, add conflict retry to bare Get/Update sites

### Documentation

- Convert ASCII diagrams to Mermaid

### Refactoring

- Extract shared task scripts for mise/CI invariance
- **(site)** Switch to published @inherent.design/brand package
- **(site)** Use brand components, theme button globally

### Maintenance

- Update pnpm-lock
- **(site)** Update packages
- **(site)** Remove stale scripts, rename deploy:cf -> deploy
- **(site)** Fix scripts
- Update brand package
- Update README
- Sync chart + app version

## [0.1.0-alpha.9] - 2026-02-09

### Testing

- Add E2E invariant tests for structural property verification

### Documentation

- Alpha.9 documentation overhaul, purge origin-no-tls-verify, fix examples

## [0.1.0-alpha.8] - 2026-02-09

### Bug Fixes

- **(controller)** Register v1beta1 scheme, sync AccessPolicy CRD, demote noisy log

### Documentation

- Fix deployment names, remove dead dns-sync annotations from examples

### Maintenance

- Update changelog

## [0.1.0-alpha.7] - 2026-02-09

### Features

- **(controller)** Alpha.7 reconciler stabilization and HTTPRoute credential inheritance

### Documentation

- Add shields.io badges to README
- Fix CRD table, add credential resolution and troubleshooting

### Maintenance

- Update changelog for alpha.6 and fix badge layout
- **(chart)** Bump to v1.0.3 / appVersion 0.1.0-alpha.7

## [0.1.0-alpha.6] - 2026-02-08

### Features

- Alpha.6 comprehensive stabilization (unreleased)

### Bug Fixes

- **(controller)** Alpha.6 reconcile, deletion, and API stabilization
- **(controller)** Logging guard and em-dash removal

### Testing

- **(e2e)** Alpha.6 coverage expansion and 94/94 stabilization

### Documentation

- Add git-cliff changelog and update project docs

### CI/CD

- Use git-cliff for release notes generation

### Maintenance

- Local dev fixes (docker cache, mise tasks)
- Reset kustomization.yaml after local deploy

## [0.1.0-alpha.5] - 2026-02-06

### Features

- Helm chart v1.0.1

### Bug Fixes

- Alpha.5 controller stabilization

### Infrastructure

- Cfgate.io v0.1.2 custom_domain for auto DNS

### Maintenance

- Local dev tasks + docs

## [0.1.0-alpha.4] - 2026-02-06

### Features

- Alpha.3 implementation
- CloudflareDNS CRD (composable architecture)
- Add helm chart v1.0.0

### Bug Fixes

- SA1019 events API migration + reconciliation bugs

### Testing

- Alpha.3 E2E suite (85/85 passing)

### Documentation

- Alpha.3 samples and examples
- Godoc comments and logging audit

### Infrastructure

- Initialize cfgate.io as wrangler
- Add version injection to builds
- Use kubectl kustomize
- Cfgate.io v0.1.1 with route fix
- Alpha.4 CI/CD improvements

### Maintenance

- Pin doc2go version
- Organize mise.toml
- Fix cfgate.io bootstrap

## [0.1.0-alpha.2] - 2026-02-02

### Bug Fixes

- Use release version tag in install.yaml

### Documentation

- Update README and examples for v0.1.0-alpha.1

## [0.1.0-alpha.1] - 2026-02-02

### Features

- Initial commit
- Add Dockerfile for container builds
- Add docs, ci, mise tooling

### Bug Fixes

- Remove deprecated Connections field (SA1019)
- Add kustomize directory structure

### Documentation

- Update Gateway API version and consolidate test tasks

### CI/CD

- Separate e2e, drop mise
- Bump golangci-lint to v2.8.0
- Update workflows; remove e2e
- Add path filter to pull_request trigger
- Remove workflow_dispatch from release

### Maintenance

- Clean CI workflows and dead code

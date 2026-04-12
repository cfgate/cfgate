# Contributing to cfgate

## Prerequisites

- [Go 1.26.2](https://go.dev/dl/)
- [mise](https://mise.jdx.dev/) (task runner and tool manager)
- [Docker](https://docs.docker.com/get-docker/) (container builds and kind clusters)
- [sops](https://github.com/getsops/sops) + [age](https://github.com/FiloSottile/age) (secrets management)
- A Cloudflare account with API token (see [API Token Permissions](#api-token-permissions))

## Getting Started

```bash
git clone https://github.com/cfgate/cfgate.git
cd cfgate
mise install
mise tasks
mise run codegen
mise run build
mise run lint
```

## Task Reference

| Task | Alias | Description |
|------|-------|-------------|
| `codegen` | `gen` | Generate DeepCopy and CRD manifests |
| `build` | `b` | Build manager binary with version info |
| `lint` | *none* | Run golangci-lint |
| `lint:fix` | `fix` | Run golangci-lint with auto-fix |
| `format` | `fmt` | Format and vet code |
| `manifests` | `dist` | Generate release manifests to `dist/` |
| `test` | `t` | Run unit tests |
| `test:cover` | *none* | Run unit tests with coverage report |
| `e2e` | *none* | Run local E2E tests against live Cloudflare API |
| `e2e:filter` | `fe2e` | Run E2E tests with a Ginkgo `--focus` filter |
| `e2e:cleanup` | `clean` | Clean orphaned E2E resources from Cloudflare |
| `coverage` | `cov` | Run local unit coverage plus local E2E coverage |
| `bench` | *none* | Run benchmark suite with allocation stats |
| `profile:bench` | *none* | Capture CPU and heap profiles for a benchmark package |
| `profile:view` | *none* | Open the pprof web UI for a captured profile |
| `profile:export` | *none* | Export text and proto views for a captured profile |
| `smoke` | *none* | Run a fast local smoke suite |
| `cluster:create` | *none* | Create dedicated cfgate dev cluster |
| `cluster:delete` | *none* | Delete cfgate dev cluster |
| `cluster:status` | *none* | Check cfgate dev cluster status |
| `local:install` | *none* | Install Gateway API and cfgate CRDs |
| `local:deploy` | *none* | Deploy controller to current cluster (kustomize) |
| `local:undeploy` | *none* | Remove controller from current cluster |
| `local:uninstall` | *none* | Uninstall CRDs from current cluster |
| `run` | *none* | Run controller locally (outside cluster) |
| `docker:build` | `db` | Build Docker image |
| `docker:push` | `dp` | Push Docker image to registry |
| `docker:buildx` | *none* | Build multi-arch image (amd64 + arm64) |

## Secrets Configuration

cfgate uses [sops](https://github.com/getsops/sops) with [age](https://github.com/FiloSottile/age) encryption for local development secrets. mise reads `secrets.enc.yaml` automatically via `[env] _.file`.

### Setting Up Secrets

1. Generate an age keypair:

```bash
age-keygen -o ~/.config/sops/age/keys.txt
```

The output includes your public key (starts with `age1...`). Save it for the next step.

2. Configure sops to use your key by editing `.sops.yaml` in the repo root:

```yaml
creation_rules:
  - age: age1your-public-key-here
```

3. Create and encrypt `secrets.enc.yaml`:

```bash
cat > secrets.enc.yaml <<'EOF'
CLOUDFLARE_API_TOKEN: your-api-token
CLOUDFLARE_ACCOUNT_ID: your-account-id
CLOUDFLARE_ZONE_NAME: your-zone.com
EOF

sops -e -i secrets.enc.yaml
```

### Required Keys

| Key | Purpose |
|-----|---------|
| `CLOUDFLARE_API_TOKEN` | Cloudflare API token |
| `CLOUDFLARE_ACCOUNT_ID` | Cloudflare account ID |

### Optional Keys

| Key | Purpose |
|-----|---------|
| `CLOUDFLARE_ZONE_NAME` | Zone for DNS and Access E2E tests |
| `CLOUDFLARE_IDP_ID` | Identity Provider ID for IdP-dependent tests |
| `CLOUDFLARE_TEST_EMAIL` | Email for email rule tests |
| `CLOUDFLARE_TEST_GROUP` | Group for GSuite group rule tests |

### Verifying Secrets

```bash
sops decrypt secrets.enc.yaml
sops secrets.enc.yaml
mise env | grep CLOUDFLARE
```

### API Token Permissions

Create a token at [Cloudflare Dashboard > API Tokens](https://dash.cloudflare.com/profile/api-tokens) with:

| Scope | Permission | Required For |
|-------|------------|--------------|
| Account | Cloudflare Tunnel: Edit | Tunnel tests |
| Account | Access: Apps and Policies: Edit | Access tests |
| Account | Access: Service Tokens: Edit | Service token tests |
| Zone | DNS: Edit | DNS tests |

Scope zone permissions to the zone matching `CLOUDFLARE_ZONE_NAME`.

## Testing

See [docs/TESTING.md](docs/TESTING.md) for the full testing guide.

```bash
mise run test              # unit tests
mise run test:cover        # unit tests with coverage
mise run coverage          # local unit + E2E coverage aggregate
mise run cluster:create    # repo-local helper for a dedicated kind cluster
mise run e2e               # local E2E against live Cloudflare API
mise run e2e:cleanup       # clean orphaned E2E resources
mise run bench             # benchmark suite
mise run smoke             # fast local sanity check
```

Normal GitHub Actions CI runs lint, unit tests, build validation, and unit coverage. It does not provision a cluster or run Cloudflare-backed E2E.

A manual GitHub Actions workflow named `Remote Release E2E` exists for release-grade remote E2E timing and Codecov upload without publishing release artifacts.

`mise run smoke` builds `bin/manager`, requires `./bin/manager --help` to exit successfully, then runs the fast package test set. `cmd/cleanup` is not part of the smoke or release CLI contract in this pass.

Two local bootstrap paths are first-class:

```bash
# Path A: broader local stack bootstrap
cd ~/production/abaddon
mise run 000-colima
mise run 001-kind

# Path B: repo-local convenience helper
cd ~/production/cfgate/cfgate
mise run cluster:create
```

`mise run e2e` defaults to `E2E_USE_EXISTING_CLUSTER=true` and expects a reachable `kind-abaddon` context, not just a kubeconfig entry with that name.

## Development Workflow

### Making Changes

1. Create a feature branch from `main`
2. Make changes
3. Regenerate CRDs if types changed: `mise run codegen`
4. Lint: `mise run lint`
5. Build: `mise run build`
6. Test: `mise run test`
7. Run local E2E when your change touches reconciler or Cloudflare behavior: `mise run e2e`
8. Submit PR against `main`

### CRD Changes

When modifying files in `api/v1alpha1/`, regenerate and reinstall:

```bash
mise run codegen
mise run local:install
```

### Running the Controller Locally

```bash
mise run run
```

The controller runs outside the cluster but connects via kubeconfig.

## Project Structure

```
cfgate/
  api/v1alpha1/             CRD type definitions
  cmd/
    manager/                Controller entrypoint
    cleanup/                E2E resource cleanup utility
  internal/
    controller/             Reconcilers (tunnel, dns, access, gateway, httproute)
    controller/annotations/ Annotation parsing and validation
    controller/context/     CRD-to-controller data wrappers
    controller/features/    Runtime feature gate detection
    controller/status/      Status condition composition
    cloudflare/             Cloudflare API client abstraction
    cloudflared/            cloudflared config and deployment builders
  config/
    crd/                    Generated CRD manifests
    default/                Kustomize overlay for deployment
    manager/                Controller deployment resources
    rbac/                   RBAC resources
  test/e2e/                 E2E test suite
  examples/                 Applyable YAML examples
  docs/                     User-facing reference documentation
  hack/                     Build utilities
```

## Related Repositories

| Repository | Description |
|------------|-------------|
| [cfgate/helm-chart](https://github.com/cfgate/helm-chart) | Helm chart (OCI at `oci://ghcr.io/cfgate/charts/cfgate`) |
| [cfgate/cfgate.io](https://github.com/cfgate/cfgate.io) | Project website |

## Commits

Conventional-style prefixes: `feat:`, `fix:`, `chore:`, `ci:`, `docs:`, `test:`, `refactor:`, `perf:`, `build:`

Subject line in imperative mood, under 72 characters. Body explains why, not what. Bullet points for multi-line bodies.

Scopes are optional; use when the change targets a specific subsystem:

```
fix(controller): correct DNS record drift detection
test(e2e): add multi-zone ownership verification
```

For contributor PRs, the maintainer squash-merges with a clean conventional subject line. You do not need to rewrite your branch history.

## Changelog

Release notes are generated via [git-cliff](https://git-cliff.org/) from commit history. Configuration is in `cliff.toml`. Do not edit `CHANGELOG.md` manually; regenerate it locally with `git-cliff` when you need to refresh the repo changelog.

## Code Style

### General

Run `mise run lint` before submitting; golangci-lint enforces style. Run `mise run format` to auto-format.

Follow existing patterns in the codebase. When in doubt, match the surrounding code.

### Logging

Use structured logging via `logr` (controller-runtime convention). Log at appropriate levels:

- `log.Info()` for reconciler phase transitions and significant state changes
- `log.V(1).Info()` for per-resource operational detail
- `log.Error()` for errors that will cause requeue or degraded status

### Comments

Default to no comments. Code should be self-explanatory through naming and structure. Comment when:

- The "why" is non-obvious (a workaround, an API quirk, a spec requirement)
- The behavior has surprising side effects
- A constant comes from an external specification

```go
// RFC 1035 section 2.3.4: DNS labels must not exceed 63 octets.
const maxDNSLabelLength = 63
```

Do not comment what the code already says.

### Doc Comments

Every exported type, function, and method gets a doc comment. The first sentence is a summary used by godoc. Cover what the symbol does, input expectations, side effects, and error conditions.

Write doc comments with future doc-gen tooling in mind: structured, factual, no marketing language.

```go
// TunnelService manages Cloudflare Tunnel lifecycle operations including
// creation, configuration updates, and deletion. It uses idempotent
// ensure semantics; calling Ensure on an existing tunnel updates its
// configuration rather than creating a duplicate.
type TunnelService struct { ... }
```

### Naming

- Match json tags in user-facing references (`sessionDuration`, not `SessionDuration`)
- Use descriptive names; avoid single-letter variables outside loop indices
- Constants use `camelCase` for package-level, `SCREAMING_SNAKE` is not idiomatic Go

## Documentation

### Where Things Live

**README.md** is the hub document: CRD tables, feature matrix, annotation summary, quickstart, and links to `docs/`. Keep it scannable.

**docs/*.md** files are deep reference, one file per topic. These are the source of truth for user-facing field documentation.

**CONTRIBUTING.md** covers development workflow, code style, and writing conventions. Not user-facing.

**examples/** contains applyable YAML. Every example directory should work with `kubectl apply -k examples/<name>` against a cluster with cfgate installed.

### When to Update Docs

CRD type changes (`api/v1alpha1/*_types.go`) and annotation changes (`internal/controller/annotations/annotations.go`) are the two sources of truth. When you change either, update the corresponding `docs/` file in the same commit. Treat it like running `mise run codegen`; it is part of the change, not a follow-up.

### Writing Style

Use complete sentences with natural compound structure. Technical reference tone; the voice of a well-written man page, not a keynote.

**Prohibited in prose:** em-dashes, en-dashes, double-hyphens. Use semicolons, commas, or colons instead. Double-hyphens in CLI flags (`--leader-elect`), YAML comments, and code are fine.

**Avoid:** superlatives ("powerful," "elegant," "seamless"), fragment-sentence drama ("One operator. Three concerns."), and long-then-short restatement. Say it once, clearly, and move on.

When documenting a field, state what it does, valid values, and the default, in that order. Skip explanation of why unless the behavior is surprising.

YAML snippets in docs must parse cleanly. Introduce code blocks with one sentence explaining when or why to use them, then let the code speak.

## License

By contributing, you agree that your contributions will be licensed under the [Apache 2.0 License](LICENSE).

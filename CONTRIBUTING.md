# Contributing to cfgate

Use the repository's mise tasks to build, format, and test changes. Unit and
offline tests need no Cloudflare credentials or Kubernetes cluster. Live E2E tests
create resources in a Cloudflare account and a disposable kind cluster.

## Prerequisites

Install [mise](https://mise.jdx.dev/), then run `mise install` to install the
versions pinned in [mise.toml](mise.toml), including Go 1.27.1. Docker is needed
for image builds and kind clusters. Live E2E also needs Cloudflare credentials
and access to the encrypted secrets loaded by its mise tasks.

## Getting Started

Clone the repository and run the local checks:

```bash
git clone https://github.com/cfgate/cfgate.git
cd cfgate
mise install
mise run build
mise run format:check
mise run lint
mise run test
mise run test:offline
```

`build` regenerates DeepCopy code and CRDs before compiling. Use `mise tasks` to
list available tasks and [docs/TESTING.md](docs/TESTING.md) for test setup,
coverage interpretation, and release verification.

## Task Reference

| Task | Purpose |
|------|---------|
| `codegen` (`gen`) | Generate DeepCopy code and CRD manifests |
| `build` (`b`) | Build the manager with version metadata |
| `format` (`fmt`), `format:check` | Apply or check Go, shell, and selected YAML formatting; `format` also vets Go |
| `lint`, `lint:fix` (`fix`) | Run golangci-lint, optionally applying fixes |
| `test` (`t`), `test:cover` | Run race-enabled unit tests, optionally recording coverage |
| `test:offline` | Check cleanup effects, bounded fuzzing, and build/release helpers without live services |
| `e2e`, `e2e:filter` (`fe2e`) | Run live E2E, optionally with a required Ginkgo focus regex |
| `e2e:preflight` | Check presence of all six release E2E credentials |
| `e2e:cleanup` (`clean`) | Preview aged orphaned test resources; deletion requires explicit opt-ins |
| `coverage` (`cov`) | Run unit and E2E coverage, merge profiles, and score assurance |
| `coverage:merge`, `coverage:report`, `coverage:score` | Recompute individual coverage stages from existing artifacts |
| `bench`, `profile:bench`, `profile:view`, `profile:export` | Run benchmarks and capture or inspect profiles |
| `smoke` | Build the manager, check its help command, and run package tests |
| `cluster:create`, `cluster:status`, `cluster:delete` | Manage a local kind cluster selected with `CLUSTER_NAME` |
| `local:install`, `local:uninstall` | Install or remove CRDs in the current kubeconfig cluster |
| `local:deploy`, `local:undeploy` | Deploy or remove the controller in the current kubeconfig cluster |
| `run` | Run the controller outside the cluster using kubeconfig |
| `docker:build` (`db`), `docker:push` (`dp`), `docker:buildx` | Build, push, or build multi-architecture images |
| `manifests` (`dist`) | Generate release manifests under `dist/` |

## Secrets Configuration

E2E, preflight, and cleanup tasks load the selected encrypted file and `.env`; ordinary
unit and offline tasks do not load `secrets.enc.yaml`. Local secrets use
[sops](https://github.com/getsops/sops) with
[age](https://github.com/FiloSottile/age). Keep plaintext credentials and private
age keys out of Git and command output.

### Setting Up Secrets

For the shared development account, use an age key already authorized for
`secrets.enc.yaml`. If you need access, ask a maintainer to add your **public** age
recipient and re-encrypt the file. Do not share private keys or edit `.sops.yaml`
to try to decrypt existing ciphertext; a new recipient does not authorize an
already encrypted file.

For your own Cloudflare account, keep credentials outside the checkout. Create
an age key only if you do not already have one at this path:

```bash
mkdir -p ~/.config/sops/age ~/.config/cfgate
age-keygen -o ~/.config/sops/age/keys.txt
```

Use your public recipient to create a new, separate encrypted file. Enter the six
Cloudflare keys below through the editor; do not copy the repository ciphertext
into this file:

```bash
CFGATE_AGE_RECIPIENT="$(age-keygen -y ~/.config/sops/age/keys.txt)"
sops --age "$CFGATE_AGE_RECIPIENT" "$HOME/.config/cfgate/secrets.enc.yaml"
```

If that personal file already exists, edit it using its authorized key instead of
creating another recipient. SOPS uses its editor for plaintext and saves ciphertext;
choose an editor that does not retain plaintext swap or backup files.

Select your personal file in the shell before running live tasks:

```bash
export CFGATE_E2E_SECRETS_FILE="$HOME/.config/cfgate/secrets.enc.yaml"
mise run e2e:preflight
```

`e2e`, `e2e:preflight`, and `e2e:cleanup` load that file instead of the shared
`secrets.enc.yaml`. `e2e:filter` and aggregate coverage delegate to `e2e` and use
the same selection. Repeat the export in each new shell. Without it, tasks retain
the repository file as their default. Review any `.env` overrides so they do not
select a different account. The repository's `.sops.yaml` and encrypted file
remain unchanged; preflight checks loading without printing credential values.

### Required Keys

| Key | Purpose |
|-----|---------|
| `CLOUDFLARE_API_TOKEN` | API token with the permissions below |
| `CLOUDFLARE_ACCOUNT_ID` | Account used for tunnel and Access tests |

### Additional Release E2E Keys

| Key | Purpose |
|-----|---------|
| `CLOUDFLARE_ZONE_NAME` | Zone used for DNS and hostname-dependent Access tests |
| `CLOUDFLARE_IDP_ID` | Identity provider for IdP-dependent rules |
| `CLOUDFLARE_TEST_EMAIL` | Email rule test value |
| `CLOUDFLARE_TEST_GROUP` | GSuite group rule test value |

Local tests skip dependent cases when these additional values are absent. Full
release coverage requires all six values.

### Verifying Secrets

Check required variable presence without printing values:

```bash
mise run e2e:preflight
```

Preflight does not verify token permissions, validity, or expiry.

### API Token Permissions

Create a token in the [Cloudflare dashboard](https://dash.cloudflare.com/profile/api-tokens):

| Scope | Permission | Used by |
|-------|------------|---------|
| Account | Cloudflare Tunnel: Edit | Tunnel tests |
| Account | Access: Apps and Policies: Edit | Access tests |
| Account | Access: Service Tokens: Edit | Service token tests |
| Zone | DNS: Edit | DNS tests |
| Zone | Zone: Read | Resolve the configured zone name |

Scope zone permissions to `CLOUDFLARE_ZONE_NAME`. Use an account and zone suitable
for creating and deleting test resources; Kubernetes isolation does not isolate
Cloudflare resources.

## Testing

Start with `mise run test` and `mise run test:offline`. For reconciler or
Cloudflare behavior changes, run the relevant live cases with `mise run e2e` or
`mise run e2e:filter -- '<focus regex>'`.

E2E creates and removes its own kind cluster by default. Start Docker before
running it. Reusing a cluster requires `E2E_USE_EXISTING_CLUSTER=true` and an
explicit `CLUSTER_NAME`; that cluster must be disposable because the suite
installs CRDs and Gateway API resources. See [E2E setup and cleanup](docs/TESTING.md#e2e-tests)
for kubeconfig checks, interruption behavior, and orphan recovery.

PR CI and manual CI dispatch run formatting, lint, race tests, offline checks,
build validation, and unit coverage. Pushes to `dev` run the reduced formatting,
lint, and unit coverage jobs. Normal CI does not run live E2E. The manual
`Remote Release E2E` workflow tests a selected ref without publishing artifacts;
release publication has a separate E2E gate.

## Development Workflow

### Making Changes

1. Create a feature branch from `main` and make a focused change.
2. Run `mise run codegen` after changing CRD types.
3. Run `mise run format`, `mise run lint`, `mise run build`, `mise run test`, and `mise run test:offline`.
4. Run relevant E2E cases for reconciler or Cloudflare behavior changes.
5. Update affected references and examples, review the diff, and open a PR against `main`.

Report the checks you ran and any checks you could not run.

Local binary and Docker tasks use [hack/build-metadata.sh](hack/build-metadata.sh).
An exact tag supplies its version without `v`; other commits use
`<tag>-dev+<commit>`, or `0.0.0-dev+<commit>` without a tag. Metadata includes a UTC
build date and optional `VERSION_SUFFIX`. Release workflows validate the tag and
full source SHA separately. Local version metadata does not certify a clean tree.

Keep reconciliation dependencies explicit. Access publication passes one
`accessSyncSession` through route collection and protection verification while
holding its locks. Its observations and caches belong to that attempt. Do not
retain them across reconciliations or hide them in context values. An absent
session permits public routes but cannot authorize Access-required routes. Keep
shared route acceptance helpers covered by both status and emitted-configuration
tests.

### CRD Changes

Regenerate CRDs and install them only in your selected development cluster:

```bash
mise run codegen
mise run local:install
```

### Running the Controller Locally

Run the controller against the cluster selected by kubeconfig:

```bash
mise run run
```

For a reusable disposable kind cluster, choose its name explicitly:

```bash
CLUSTER_NAME=cfgate-dev mise run cluster:create
CLUSTER_NAME=cfgate-dev mise run cluster:status
```

Use the same name with `cluster:delete` when finished. The cluster tasks default
to `abaddon` when `CLUSTER_NAME` is unset.

## Project Structure

| Path | Contents |
|------|----------|
| `api/v1alpha1/` | CRD types |
| `cmd/manager/`, `cmd/cleanup/` | Controller and test-resource cleanup entrypoints |
| `internal/controller/` | Reconcilers and route, annotation, status, and feature helpers |
| `internal/cloudflare/`, `internal/cloudflared/` | API clients and connector configuration/builders |
| `internal/accesstags/` | Access owner tag helpers |
| `config/` | Generated CRDs, deployment overlays, and RBAC |
| `test/e2e/` | Live E2E and offline test helpers |
| `examples/`, `docs/` | Deployable examples and user documentation |
| `hack/`, `.github/scripts/` | Build, coverage, and release utilities |

## Related Repositories

The [Helm chart](https://github.com/cfgate/helm-chart) is published at
`oci://ghcr.io/cfgate/charts/cfgate`. The website lives in
[cfgate/cfgate.io](https://github.com/cfgate/cfgate.io).

## Commits

Use conventional prefixes such as `feat:`, `fix:`, `docs:`, `test:`, `refactor:`,
`perf:`, `build:`, `ci:`, or `chore:`. Write an imperative subject under 72
characters; use the body to explain why. Scopes are optional.

Examples: `fix(controller): correct DNS record drift detection` and
`test(e2e): add multi-zone ownership verification`.

## Changelog

[git-cliff](https://git-cliff.org/) generates release notes from commit history
using `cliff.toml`. Do not edit `CHANGELOG.md` manually; run `git-cliff` when a
local regeneration is needed.

## Code Style

### General

Follow surrounding code and the pinned formatters. `.golangci.yml` configures
linting, including `ginkgolinter` and `gofmt`. Generated Go files are regenerated,
not hand-formatted. `.editorconfig` defines whitespace; shell files in `hack/`
and `.github/scripts/` use two-space indentation. `.yamlfmt.yml` limits YAML
formatting to workflows and lint/formatter configuration. It excludes generated
manifests, encrypted secrets, and examples. Embedded shell in mise or YAML is not
automatically reformatted.

### Logging

Use structured `logr` logging: `Info` for significant state changes,
`V(1).Info` for per-resource details, and `Error` for failures causing a requeue or
degraded status.

### Comments

Explain non-obvious reasons, side effects, workarounds, or external requirements.
Prefer clear names over comments that repeat the code.

### Doc Comments

Document every exported type, function, and method. Start with a factual summary,
then cover relevant input expectations, side effects, and errors.

### Naming

Match JSON field names in user-facing references, such as `sessionDuration`.
Use descriptive Go names and camelCase package-level constants; reserve short
variables for limited scopes such as loop indices.

## Documentation

### Where Things Live

[README.md](README.md) introduces the project and links to installation and
reference material. [docs/README.md](docs/README.md) indexes the guides and
references. `examples/` contains Kubernetes examples; `CONTRIBUTING.md` and
`docs/TESTING.md` cover contributor workflows.

### When to Update Docs

When changing CRD types or annotations, update the corresponding reference and
examples in the same change. Check generated schema defaults and validation
against `api/v1alpha1/*_types.go` and annotation behavior against
`internal/controller/annotations/annotations.go`.

### Writing Style

Use complete sentences, direct wording, and a technical reference tone. Avoid
superlatives, dramatic fragments, repeated summaries, and em-dashes or en-dashes
in prose. Double-hyphens belong in code and CLI flags, not prose punctuation.
For a field, state behavior, valid values, and default. Introduce code blocks
with their purpose and keep YAML examples parseable.

## License

Contributions use the [Apache 2.0 License](LICENSE).

# Compatibility

cfgate, its CRDs, the connector, Gateway API and Helm chart are separate versioned
components. Install matching cfgate CRDs before upgrading the controller so the
API server retains ownership and recovery status.

## Alpha.8 component pins

| Component | Version |
|---|---|
| cfgate | 0.2.0-alpha.8 |
| Go | 1.27.1 |
| Cloudflare SDK | 7.11.0 |
| Kubernetes Go libraries | 0.37.1 |
| controller-runtime | 0.25.2 |
| Gateway API | 1.6.2 |
| cloudflared-h2c | 2026.9.3-h2c.1 |

The default connector image includes the multi-architecture digest
`sha256:6c46ca006f9d6af5e973e59f2d71f5d6d3dc138c8a5484380d5797092171e3ad`,
from fork source `d40bf36f`. Explicit image overrides remain administrator choices.
Existing CRs keep their stored image references after a CRD default changes.
A stock cloudflared image cannot substitute for the fork when h2c is configured.

## Kubernetes requirements

The standard Gateway API v1.6.2 bundle includes stable ValidatingAdmissionPolicy
resources and requires Kubernetes 1.30 or later. This API minimum does not certify
cfgate on every later Kubernetes version. Client-library versions and a chart's
`kubeVersion` expression also do not establish a tested server range.

Gateway API remains an external chart prerequisite. Inspect an existing bundle
before replacing it; its admission policies may restrict channel changes and
downgrades. Installing or removing optional CRDs requires a manager restart.

## Upgrades and release evidence

Follow the [ownership migration notes](authorization-and-ownership.md) and
[Access-required contract](access-required.md). Keep the installation namespace,
credential Secrets and existing ownership claims until migration or cleanup is
complete. Adding a namespaced Role does not remove a pre-existing broad
ClusterRole grant.

Release workflows retain test, scan, source and artifact identity evidence with
the corresponding run and release assets. The published
[alpha.6 release](https://github.com/cfgate/cfgate/releases/tag/v0.2.0-alpha.6)
and [chart 1.5.0 release](https://github.com/cfgate/helm-chart/releases/tag/v1.5.0)
remain historical references; their results do not certify later source changes.
Use the run associated with the exact release being deployed.

The operator workflow gates publication on live E2E and quality checks, then builds,
scans and promotes the same attested images. Publish its chart afterward using the
published operator digest and matching schemas. Changelogs and release notes are
generated from Git history; do not duplicate validation logs in user documentation.

## Upgrade from v0.2.0-alpha.7 to v0.2.0-alpha.8

Install the matching CRDs before using service-token `rotationOverlap`. Managed
tokens now renew their configured lifetime before expiration; renewal does not
replace the secret. Token names and destination Secrets must be unique within
each policy, and durations must be positive. The default overlap remains zero.
See [service token lifecycle](cloudflare-access-policy.md#service-token-lifecycle)
for recovery and consumer reload requirements.

DNS records now use the most specific configured zone. Deployments with both
parent and delegated child zones should check the selected zone before rollout.
Obsolete zone/type records are removed before replacements when cleanup is
enabled. Failed cleanup remains visible and retries; it may delay publication.
See [DNS configuration](cloudflare-dns.md) for retention policy behavior.

The alpha.8 DNS controller requires the matching CRD for `status.pendingWrites`
and `status.ownershipPrefix`. Route-derived DNS now enforces Gateway/listener
admission in addition to discovery selectors. TXT prefixes are immutable; see
[DNS recovery and ownership](cloudflare-dns.md#interrupted-writes-and-ownership-changes)
for cleanup and migration behavior. Healthy Access token expiration extensions retain
forwarding; credential replacement and authorization edits keep withdrawal checks.

### Rollback with unfinished operations

Do not downgrade the controller or CRDs while DNS `status.pendingWrites` or Secret
`cfgate.io/service-token-rotation-pending` markers remain. An older controller cannot
interpret those obligations, and an older schema can prune DNS recovery fields.
Keep the current controller and matching schemas running until writes and cleanup
finish, then back up the objects and credentials before assessing a downgrade.
Removing status, finalizers, or pending markers is not a rollback procedure. There
is no automatic translation of unfinished operations for alpha.7 or older versions.

## Dependency maintenance

Keep Kubernetes libraries and controller-runtime on compatible release families,
and keep the Gateway Go module and installed bundle aligned. Renovate covers Go
modules, container bases, workflow actions and selected tool pins. The connector
fork receives manual review, including its tag and digest together.

Use `go mod tidy` and `go mod verify`, then run the repository checks described in
[CONTRIBUTING](../CONTRIBUTING.md). Major updates require review of the API contracts
cfgate uses. The SDK-unknown `h2cOrigin` field, bounded pagination and origin duration
wire formats have dedicated regressions; changes to the Cloudflare integration
also require live E2E.

# cfgate

[![Latest Release](https://img.shields.io/github/v/release/cfgate/cfgate?style=flat)](https://github.com/cfgate/cfgate/releases/latest) [![Image](https://img.shields.io/github/v/release/cfgate/cfgate?style=flat&label=image&logo=docker&logoColor=white&color=2496ED)](https://github.com/orgs/cfgate/packages/container/package/cfgate) [![Helm Chart](https://img.shields.io/badge/chart-GHCR-0F1689?style=flat&logo=helm&logoColor=white)](https://github.com/orgs/cfgate/packages/container/package/charts%2Fcfgate) [![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/cfgate)](https://artifacthub.io/packages/container/cfgate/cfgate)

[![Build Status](https://img.shields.io/github/actions/workflow/status/cfgate/cfgate/ci.yml?branch=main&style=flat)](https://github.com/cfgate/cfgate/actions/workflows/ci.yml) [![Coverage](https://codecov.io/gh/cfgate/cfgate/branch/main/graph/badge.svg)](https://codecov.io/gh/cfgate/cfgate) [![golangci-lint](https://img.shields.io/badge/lint-golangci--lint-blue)](https://golangci-lint.run/) [![Go Reference](https://pkg.go.dev/badge/github.com/cfgate/cfgate.svg)](https://pkg.go.dev/cfgate.io/cfgate/)

cfgate manages Cloudflare Tunnels, DNS records, and Access configuration from
Kubernetes resources. It uses Gateway API HTTPRoutes to configure a separate
cloudflared connector, which carries traffic from Cloudflare to your Services.
The manager handles configuration; it is not an application proxy.

## Getting started

Follow the [getting-started guide](docs/getting-started.md) to install the
controller, create credentials, deploy a sample service, publish a hostname, and
verify a request. It includes optional Access protection and scoped cleanup.

For an existing installation, read [compatibility and upgrades](docs/compatibility.md)
before changing the controller, chart, or CRDs. This documentation describes
`v0.2.0-alpha.11`; Helm chart `1.10.0` installs that release. Use versioned release
artifacts rather than assuming GitHub's `latest` endpoint selects an alpha.

## How it works

![How cfgate works](docs/images/how-it-works.svg)

A CloudflareTunnel manages a remote tunnel and its connector Deployment. A Gateway
selects that tunnel, and HTTPRoutes attach supported hostname/path matches to
Services. CloudflareDNS publishes hostnames separately. Access policies define
who may connect; Access applications attach those policies to host/path targets.
Creating a policy alone does not protect a route.

The connector establishes outbound connections, so this path does not require a
public cluster IP or an inbound LoadBalancer. Other cluster services can still
have their own exposure. The Gateway API resource model is shared with other
implementations; cfgate supports a [defined HTTPRoute subset](docs/gateway-api-primer.md#supported-httproute-behavior),
not every feature implemented by other controllers.

## Resource reference

| Resource | Responsibility |
| --- | --- |
| [CloudflareTunnel](docs/cloudflare-tunnel.md) | Remote tunnel, connector workload, and origin defaults |
| [CloudflareDNS](docs/cloudflare-dns.md) | Route-derived or explicit DNS records and ownership tracking |
| [CloudflareAccessPolicy](docs/cloudflare-access-policy.md) | Reusable Access policy and managed service tokens |
| [CloudflareAccessApplication](docs/cloudflare-access-application.md) | Policy attachment to Gateway or HTTPRoute targets |
| [Annotations](docs/annotations.md) | Route transport, discovery, lifecycle, and adoption settings |

## Documentation

The [documentation index](docs/README.md) groups installation, configuration,
operations, and contributor guides. Start with:

- [Gateway API concepts](docs/gateway-api-primer.md) for attachment and supported routing
- [Authorization and ownership](docs/authorization-and-ownership.md) for administrator boundaries, grants, and adoption
- [Access-required routing](docs/access-required.md) for explicit protection dependencies and their limits
- [Troubleshooting](docs/troubleshooting.md) for status, startup, DNS, and cleanup failures
- [Compatibility](docs/compatibility.md) for version pins and migrations

## Examples

Examples require a running controller and edited credentials, account IDs, and
hostnames. Their READMEs describe the required permissions and cleanup order.

| Example | Purpose |
| --- | --- |
| [Basic](examples/basic/README.md) | One public service through a tunnel |
| [Multiple services](examples/multi-service/README.md) | Shared tunnel with public and path-protected routes |
| [External target](examples/external-target/README.md) | DNS records without a tunnel |
| [Rancher](examples/with-rancher/README.md) | Integration considerations for a separate Gateway API application |

## Development

[CONTRIBUTING.md](CONTRIBUTING.md) covers setup and repository conventions.
[Testing](docs/TESTING.md) separates unit, schema, live API, and packaged-deployment
checks. Release notes are generated from Git history; see [CHANGELOG.md](CHANGELOG.md).

Related repositories: [Helm chart](https://github.com/cfgate/helm-chart),
[website](https://github.com/cfgate/cfgate.io), and
[cloudflared h2c fork](https://github.com/inherent-design/cloudflared).

## License

[Apache-2.0](LICENSE).

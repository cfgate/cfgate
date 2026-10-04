# cfgate documentation

cfgate translates Kubernetes resources into Cloudflare configuration and connector
workloads. These guides describe the released alpha.11 behavior. Start with installation,
then use the resource references and operations guides for your configuration.

## Installation and first request

1. [Getting started](getting-started.md): install cfgate, publish a sample service, verify it, and clean up.
2. [Gateway API concepts](gateway-api-primer.md): classes, listeners, route attachment, and supported behavior.
3. [Compatibility and upgrades](compatibility.md): component versions, schema requirements, and migrations.

## Configuration reference

| Guide | Use it for |
| --- | --- |
| [CloudflareTunnel](cloudflare-tunnel.md) | Tunnel identity, connector settings, origin defaults, and fallback behavior |
| [CloudflareDNS](cloudflare-dns.md) | Discovery, zones, effective settings, ownership, and retention |
| [CloudflareAccessPolicy](cloudflare-access-policy.md) | Identity rules and service-token lifecycle |
| [CloudflareAccessApplication](cloudflare-access-application.md) | Application destinations and policy bindings |
| [Annotations](annotations.md) | Per-route overrides and resource lifecycle controls |

## Operations and security

- [Authorization and ownership](authorization-and-ownership.md): permissions, grants, adoption, recovery, and decommissioning
- [Access-required routing](access-required.md): publication ordering and authentication limits
- [Connector hardening](connector-hardening.md): Pod defaults and network isolation
- [Troubleshooting](troubleshooting.md): symptoms, observations, and recovery steps
- [Service mesh integration](service-mesh.md): separate GatewayClasses and Kiali configuration

## Contributor guides

- [Contributing](../CONTRIBUTING.md): tools, changes, code generation, and writing conventions
- [Testing](TESTING.md): test scope, credentials, cleanup, coverage, and release evidence
- [Cloudflare client limits](CLOUDFLARE_CLIENT.md): deadlines, pagination, and configuration validation

A Ready condition reports a controller observation. Verify the application request
separately: Kubernetes readiness, remote configuration storage, connector
application, and successful authentication are different checks.

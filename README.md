# cfgate

[![Latest Release](https://img.shields.io/github/v/release/cfgate/cfgate?style=flat)](https://github.com/cfgate/cfgate/releases/latest) [![Image](https://img.shields.io/github/v/release/cfgate/cfgate?style=flat&label=image&logo=docker&logoColor=white&color=2496ED)](https://github.com/orgs/cfgate/packages/container/package/cfgate) [![Helm Chart](https://img.shields.io/badge/chart-GHCR-0F1689?style=flat&logo=helm&logoColor=white)](https://github.com/orgs/cfgate/packages/container/package/charts%2Fcfgate) [![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/cfgate)](https://artifacthub.io/packages/container/cfgate/cfgate)

[![Build Status](https://img.shields.io/github/actions/workflow/status/cfgate/cfgate/ci.yml?style=flat)](https://github.com/cfgate/cfgate/actions/workflows/ci.yml) [![Go Report Card](https://goreportcard.com/badge/github.com/cfgate/cfgate)](https://goreportcard.com/report/github.com/cfgate/cfgate) [![Go Reference](https://pkg.go.dev/badge/github.com/cfgate/cfgate.svg)](https://pkg.go.dev/cfgate.io/cfgate/)

cfgate is a Kubernetes operator that manages Cloudflare Tunnels, DNS records, and Access policies through three custom resources. It uses [Gateway API](https://gateway-api.sigs.k8s.io/), the CNCF standard replacing Ingress, so routing configuration works the same as Envoy, Istio, or Cilium. Clusters running cfgate need no public IP, no ingress controller, and no load balancer. Traffic reaches services through Cloudflare Tunnels: outbound-only connections from the cluster to Cloudflare's edge.

Gateway API is the Kubernetes successor to Ingress. If you're coming from Ingress, see the [Gateway API Primer](docs/gateway-api-primer.md).

### Why cfgate?

- **Three CRDs for tunnels, DNS, and access.** CloudflareTunnel, CloudflareDNS, and CloudflareAccessPolicy each manage a distinct piece of Cloudflare infrastructure as a Kubernetes CRD. Tunnels, DNS records, and zero-trust access policies all live in version-controlled YAML instead of the Cloudflare dashboard.
- **Outbound-only tunnel connections.** Cloudflare Tunnels establish outbound-only connections from the cluster to Cloudflare's edge. Services are never exposed via public IP or load balancer.
- **Built on Gateway API.** Uses the [Gateway API](https://gateway-api.sigs.k8s.io/) standard, not a proprietary abstraction. Existing community operators use the deprecated Ingress API and lack Access policy management.
- **Independent, composable resources.** Each CRD operates independently. Use all three together or pick the ones you need: a tunnel without DNS sync, DNS without Access, or the full stack.

## How It Works

![How cfgate works](docs/images/how-it-works.svg)

Define a CloudflareTunnel, point a Gateway at it, and attach HTTPRoutes to the Gateway. cfgate reconciles each resource against the Cloudflare API: it creates the tunnel, deploys cloudflared pods, syncs DNS records, and configures access policies. Traffic flows from Cloudflare's edge through the tunnel directly to in-cluster services. The cluster needs no public IP, no ingress controller, and no load balancer.

## Getting Started

### Install

**Kustomize**

```bash
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.4.1/standard-install.yaml

kubectl apply -f https://github.com/cfgate/cfgate/releases/latest/download/install.yaml
```

**Helm**

```bash
helm install cfgate oci://ghcr.io/cfgate/charts/cfgate \
  --namespace cfgate-system --create-namespace
```

> Both methods create the `cfgate-system` namespace. CloudflareTunnel and CloudflareDNS resources typically live here. Routes and services can be in any namespace.

### Quick Start

#### 1. Create credentials

```bash
kubectl create secret generic cloudflare-credentials \
  -n cfgate-system \
  --from-literal=CLOUDFLARE_API_TOKEN=<your-token>
```

#### 2. Create a tunnel

```yaml
apiVersion: cfgate.io/v1alpha1
kind: CloudflareTunnel
metadata:
  name: my-tunnel
  namespace: cfgate-system
spec:
  tunnel:
    name: my-tunnel
  cloudflare:
    accountId: "<account-id>"
    secretRef:
      name: cloudflare-credentials
  cloudflared:
    replicas: 2
```

#### 3. Create GatewayClass and Gateway

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: cfgate
spec:
  controllerName: cfgate.io/cloudflare-tunnel-controller
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: cloudflare-tunnel
  namespace: cfgate-system
  annotations:
    cfgate.io/tunnel-ref: cfgate-system/my-tunnel
spec:
  gatewayClassName: cfgate
  listeners:
    - name: http
      protocol: HTTP
      port: 80
      allowedRoutes:
        namespaces:
          from: All
```

GatewayClass declares the controller (`cfgate.io/cloudflare-tunnel-controller`). Gateway is the runtime instance that binds to a specific CloudflareTunnel via the `cfgate.io/tunnel-ref` annotation. Both are required.

#### 4. Set up DNS sync

```yaml
apiVersion: cfgate.io/v1alpha1
kind: CloudflareDNS
metadata:
  name: my-dns
  namespace: cfgate-system
spec:
  tunnelRef:
    name: my-tunnel
  zones:
    - name: example.com
  source:
    gatewayRoutes:
      enabled: true
```

With `gatewayRoutes.enabled: true` and no `annotationFilter`, cfgate syncs DNS records for all routes attached to the referenced tunnel's Gateways. To sync specific routes only, use the `annotationFilter` field. See [CloudflareDNS reference](docs/cloudflare-dns.md#specsourcegatewayroutes).

#### 5. Expose a service

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: my-app
  namespace: default
spec:
  parentRefs:
    - name: cloudflare-tunnel
      namespace: cfgate-system
  hostnames:
    - app.example.com
  rules:
    - backendRefs:
        - name: my-service
          port: 80
```

cfgate automatically:
- Creates a CNAME record `app.example.com` → `{tunnelId}.cfargotunnel.com`
- Adds a cloudflared ingress rule routing `app.example.com` → `http://my-service.default.svc:80`
- Manages ownership TXT records for safe multi-cluster deployments

## CRDs

**CloudflareTunnel** manages tunnel lifecycle and cloudflared deployment. A single tunnel serves any number of domains across zones. → [Full reference](docs/cloudflare-tunnel.md)

**CloudflareDNS** syncs DNS records independently from tunnel lifecycle, with multi-zone support and ownership tracking. → [Full reference](docs/cloudflare-dns.md)

**CloudflareAccessPolicy** manages Cloudflare Access applications and policies for zero-trust authentication against Gateway API targets. → [Full reference](docs/cloudflare-access-policy.md)

Per-route configuration (origin protocol, TLS settings, timeouts, DNS TTL) is set via annotations on Gateway API route resources. → [Full reference](docs/annotations.md)

TCPRoute and UDPRoute controllers are registered but not yet implemented (planned for v0.2.0).

## Documentation

| Document | Description |
|----------|-------------|
| [Gateway API Primer](docs/gateway-api-primer.md) | Gateway API concepts for Ingress users |
| [CloudflareTunnel](docs/cloudflare-tunnel.md) | Full CRD reference |
| [CloudflareDNS](docs/cloudflare-dns.md) | Full CRD reference, annotationFilter, ownership |
| [CloudflareAccessPolicy](docs/cloudflare-access-policy.md) | Full CRD reference, rule types, credential resolution |
| [Annotations](docs/annotations.md) | Complete annotation reference |
| [Service Mesh](docs/service-mesh.md) | Istio, Envoy Gateway, and Kiali integration |
| [Troubleshooting](docs/troubleshooting.md) | Diagnostic steps and solutions |
| [Testing](docs/TESTING.md) | Unit and E2E test strategy |
| [Contributing](CONTRIBUTING.md) | Development setup and workflow |
| [Changelog](CHANGELOG.md) | Release history |

## Examples

| Example | Description |
|---------|-------------|
| [basic](examples/basic) | Single tunnel + gateway + DNS sync |
| [multi-service](examples/multi-service) | Multiple services, one tunnel, Access policies |
| [with-rancher](examples/with-rancher) | Rancher 2.14+ integration |
| [external-target](examples/external-target) | A/AAAA records via ExternalTarget (no tunnel) |

## Requirements

### Cloudflare API Token

Create a token at [Cloudflare Dashboard → API Tokens](https://dash.cloudflare.com/profile/api-tokens) with:

| Scope | Permission | Used By |
|-------|------------|---------|
| Account | Cloudflare Tunnel: Edit | CloudflareTunnel |
| Account | Access: Apps and Policies: Edit | CloudflareAccessPolicy |
| Account | Access: Service Tokens: Edit | CloudflareAccessPolicy |
| Account | Account Settings: Read | CloudflareTunnel (accountName only)* |
| Zone | DNS: Edit | CloudflareDNS |

*Only required when using `spec.cloudflare.accountName` instead of `accountId`.

### Kubernetes

- Kubernetes 1.26+
- Gateway API v1.4.1+ CRDs installed
- cluster-admin access for CRD installation

## Related Repositories

| Repository | Description |
|------------|-------------|
| [cfgate/helm-chart](https://github.com/cfgate/helm-chart) | Helm chart for cfgate |
| [cfgate/cfgate.io](https://github.com/cfgate/cfgate.io) | Project website |

## Development

```bash
brew install mise
mise install
mise tasks
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for full development setup, secrets configuration, and contribution guidelines.

See [docs/TESTING.md](docs/TESTING.md) for test strategy, environment variables, and test execution.

## License

[Apache 2.0](LICENSE)

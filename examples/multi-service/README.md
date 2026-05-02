# Multi-Service Example

Multiple services exposed through a single Cloudflare tunnel.

```
api.example.com        -> api service
web.example.com/       -> web service (public)
web.example.com/admin  -> admin service (Access protected)
web.example.com/repos  -> api service (Access protected)
```

## Quick Start

```bash
# 1. Install cfgate (see basic example)

# 2. Edit configuration files
# - tunnel.yaml: set accountId
# - dns.yaml: set zones[].name
# - httproutes.yaml: set hostnames
# - accesspolicy.yaml: set accountId and identity rules

# 3. Deploy
kubectl apply -k examples/multi-service
```

## Components

- One `CloudflareTunnel` with 2 replicas
- One `Gateway` shared by all routes
- One `CloudflareDNS` watching all HTTPRoutes
- Three services: `api`, `web`, and `admin`
- Two HTTPRoutes: `api` and `web`
- Two reusable `CloudflareAccessPolicy` resources in `cfgate-system`
- Two tenant-local `CloudflareAccessApplication` resources protecting named `web` route rules
- One `ReferenceGrant` allowing tenant app bindings to reference central policies

> Access policies live in `cfgate-system`. Access applications live in `demo` and attach those policies to the `admin` and `repos` HTTPRoute rules. This cross-namespace policy reference requires a [ReferenceGrant](https://gateway-api.sigs.k8s.io/api-types/referencegrant/) in `cfgate-system`. See `referencegrant.yaml`.

## Adding Services

1. Add deployment + service to `services.yaml`
2. Add HTTPRoute to `httproutes.yaml`
3. DNS record created automatically

## Cleanup

```bash
kubectl delete -k examples/multi-service
```

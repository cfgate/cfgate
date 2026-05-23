# CloudflareOriginPolicy

`CloudflareOriginPolicy` attaches cfgate-specific origin settings to Gateway API `HTTPRoute` resources. It is the policy-form replacement for annotation sprawl when settings are not covered by portable Gateway API resources.

**API Version:** `cfgate.io/v1alpha1`
**Kind:** `CloudflareOriginPolicy`
**Short Names:** `cfop`, `cforiginpolicy`
**Scope:** Namespaced

## Status

Spec-first in `0.3.0-alpha.1`. The CRD defines the API contract; controller reconciliation follows in implementation PRs.

## Precedence

Origin settings resolve in this order:

1. HTTPRoute explicit cfgate annotations
2. Gateway API `BackendTLSPolicy` on the backend `Service`
3. `CloudflareOriginPolicy`
4. `CloudflareTunnel.spec.originDefaults`

Annotations remain the alpha-line legacy override. `BackendTLSPolicy` is preferred for standard backend TLS validation when it fits. Use `CloudflareOriginPolicy` for cfgate/cloudflared settings that Gateway API does not standardize.

## Spec Reference

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `spec.targetRefs[]` | `[]OriginPolicyTargetReference` | *none* | Yes | Gateway API `HTTPRoute` targets. Cross-namespace targets require `ReferenceGrant`. |
| `spec.origin.protocol` | `http`, `https` | `http` | No | Backend Service scheme used by cloudflared. |
| `spec.origin.connectTimeout` | Duration string | *none* | No | Deprecated flat alias for `connection.connectTimeout`. |
| `spec.origin.httpHostHeader` | Hostname string | *none* | No | Deprecated flat alias for `http.httpHostHeader`. |
| `spec.origin.originServerName` | Hostname string | *none* | No | Deprecated flat alias for `tls.originServerName`. |
| `spec.origin.noTLSVerify` | `bool` | `false` | No | Deprecated flat alias for `tls.noTLSVerify`. |
| `spec.origin.http2Origin` | `bool` | `false` | No | Connect to HTTPS origin using HTTP/2. Mutually exclusive with `h2cOrigin`. |
| `spec.origin.h2cOrigin` | `bool` | `false` | No | Connect to origin using cleartext HTTP/2. Requires cfgate cloudflared fork. |
| `spec.origin.caPoolRef.name` | `string` | *none* | No | Deprecated flat alias for `tls.caPoolRef.name`. |
| `spec.origin.tls.originServerName` | Hostname string | *none* | No | Expected certificate hostname and SNI for HTTPS origins. |
| `spec.origin.tls.matchSNItoHost` | `bool` | `false` | No | Set SNI to incoming request hostname. |
| `spec.origin.tls.noTLSVerify` | `bool` | `false` | No | Disable origin TLS certificate verification. |
| `spec.origin.tls.tlsTimeout` | Duration string | *none* | No | Timeout for completing TLS handshake. |
| `spec.origin.tls.caPoolRef.name` | `string` | *none* | No | Named CA pool from `CloudflareTunnel.spec.originCAPools`. |
| `spec.origin.http.httpHostHeader` | Hostname string | *none* | No | HTTP Host header sent to backend Service. |
| `spec.origin.http.disableChunkedEncoding` | `bool` | `false` | No | Disable chunked transfer encoding to HTTP/1.1 origins. |
| `spec.origin.connection.connectTimeout` | Duration string | *none* | No | TCP connection establishment timeout. |

Cloudflare documents these cloudflared origin parameters as remote/local tunnel settings: `originServerName`, `matchSNItoHost`, `caPool`, `noTLSVerify`, `tlsTimeout`, `http2Origin`, `httpHostHeader`, `disableChunkedEncoding`, and `connectTimeout`.

## Example

```yaml
apiVersion: cfgate.io/v1alpha1
kind: CloudflareOriginPolicy
metadata:
  name: internal-origin
  namespace: apps
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: HTTPRoute
      name: app
      namespace: ""
      sectionName: ""
  origin:
    protocol: https
    http2Origin: true
    tls:
      originServerName: app.internal
      caPoolRef:
        name: internal
    http:
      httpHostHeader: app.internal
    connection:
      connectTimeout: 10s
```

## BackendTLSPolicy Interop

Gateway API `v1.BackendTLSPolicy` is the standard API for backend TLS validation. cfgate's initial contract should support:

- Target: Kubernetes `Service` referenced by an accepted `HTTPRoute.backendRefs[]`.
- CA source: one same-namespace `ConfigMap` reference with key `ca.crt`.
- SNI/certificate hostname: `spec.validation.hostname`.
- Status: `Accepted` and `ResolvedRefs`.
- Conflict: oldest policy by creation timestamp wins; ties sort by `namespace/name`.

cfgate maps valid `BackendTLSPolicy` CA bundles to managed cloudflared `originRequest.caPool` paths. `WellKnownCACertificates: System` should map to no `caPool` override when supported.

## Unsupported Route Surfaces

`GRPCRoute` is deferred. Gateway API supports `GRPCRoute`, but Cloudflare documents that Tunnel gRPC is supported through private subnet routing and public hostname deployments are not currently supported. cfgate's current public-hostname Tunnel model therefore stays `HTTPRoute`-only.

`TCPRoute` and `TLSRoute` are not planned as full public-hostname E2E surfaces. Cloudflare Tunnel can publish TCP-like services only with client-side `cloudflared access tcp` or private routing, which does not match cfgate's current DNS/public-hostname HTTPRoute contract.

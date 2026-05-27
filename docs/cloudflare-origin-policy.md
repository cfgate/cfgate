# CloudflareOriginPolicy

`CloudflareOriginPolicy` attaches cfgate-specific origin settings to Gateway API `HTTPRoute` resources. It is the policy-form replacement for annotation sprawl when settings are not covered by portable Gateway API resources.

**API Version:** `cfgate.io/v1alpha1`
**Kind:** `CloudflareOriginPolicy`
**Short Names:** `cfop`, `cforiginpolicy`
**Scope:** Namespaced

## Status

Runtime-supported in `0.3.0-alpha.1`. The controller resolves `HTTPRoute` targets, writes status, and applies accepted policies to generated Cloudflare Tunnel ingress rules. `HTTPRoute` is the only target kind in this release. Cross-namespace target references require a Gateway API `ReferenceGrant` in the target namespace.

## Precedence

Origin settings resolve from lowest to highest precedence:

1. `CloudflareTunnel.spec.originDefaults`
2. `CloudflareOriginPolicy`
3. Gateway API `BackendTLSPolicy` on the backend `Service`
4. HTTPRoute explicit cfgate annotations

Annotations remain the alpha-line legacy override. `BackendTLSPolicy` is preferred for standard backend TLS validation when it fits. Use `CloudflareOriginPolicy` for cfgate/cloudflared settings that Gateway API does not standardize.

When both deprecated flat aliases and nested fields are set on a `CloudflareOriginPolicy`, nested fields win. Policies that target the same route section use Gateway API policy precedence: older `creationTimestamp` wins, and ties sort by `namespace/name`.

## Origin Transport Constraints

Origin transport settings must produce a cloudflared-valid origin service. cfgate validates the effective origin request after tunnel defaults, policy, BackendTLSPolicy, and annotations are composed.

- `http2Origin` and `h2cOrigin` are mutually exclusive.
- `http2Origin` is for HTTPS origins.
- `h2cOrigin` is for cleartext HTTP/2 origins.
- `h2cOrigin` requires HTTP origin protocol.
- `h2cOrigin` is invalid with `spec.origin.protocol: https`.
- `h2cOrigin` is invalid when BackendTLSPolicy applies to the same backend, because BackendTLSPolicy forces HTTPS origin service generation.
- `h2cOrigin` is invalid for generated service URLs that use `https://` or `wss://`.
- An HTTPRoute annotation can override compatible origin fields, but it cannot change a BackendTLSPolicy backend back to HTTP.

## Boolean Override Semantics

v1alpha1 CRD bool fields are enable-only for inheritance. Kubernetes stores omitted and explicit false as the same value, so `CloudflareOriginPolicy` and `CloudflareTunnel.spec.originDefaults` bool fields cannot express an explicit false override.

HTTPRoute annotations preserve field presence. Route annotations can explicitly disable inherited booleans with `false`, `0`, or `no`. This allows annotations to remain the final alpha-line override while policies replace annotation sprawl over time.

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

Gateway API `v1.BackendTLSPolicy` is the standard API for backend TLS validation. cfgate supports:

- Target: Kubernetes `Service` referenced by an accepted `HTTPRoute.backendRefs[]`.
- CA source: one same-namespace `ConfigMap` reference with key `ca.crt`.
- SNI/certificate hostname: `spec.validation.hostname`.
- Well-known CA source: `wellKnownCACertificates: System`.
- Status: `Accepted` and `ResolvedRefs`.
- Conflict: oldest policy by creation timestamp wins; ties sort by `namespace/name`.

cfgate maps valid `BackendTLSPolicy` CA bundles to generated, managed cloudflared `originRequest.caPool` paths. `WellKnownCACertificates: System` maps to no `caPool` override.

Unsupported in `0.3.0-alpha.1`: multiple `targetRefs`, non-Service targets, `sectionName`, multiple CA refs, non-ConfigMap CA refs, `subjectAltNames`, and implementation-specific `options`.

## BackendTLSPolicy Interop Details

BackendTLSPolicy targets Kubernetes `Service` resources only. When accepted for a backend Service, cfgate:

- Changes the generated origin service scheme to HTTPS.
- Sets `originRequest.originServerName` from `spec.validation.hostname`.
- Maps one supported ConfigMap CA reference to a managed cloudflared `originRequest.caPool` path.
- Uses system trust and no `caPool` override for `wellKnownCACertificates: System`.
- Rejects unsupported BackendTLSPolicy fields in status and gives them no runtime effect.

Because BackendTLSPolicy forces HTTPS origin service generation, it conflicts with effective `h2cOrigin` on the same backend.

Route annotations are applied after BackendTLSPolicy for overrideable origin request fields. They cannot violate the BackendTLSPolicy HTTPS invariant.

## Status Conditions

Top-level conditions:

- `TargetsResolved`
- `ReferenceGrantValid`
- `Ready`

Each target also gets an ancestor `Accepted` condition. `Accepted=False` uses `TargetNotFound`, `ReferenceGrantRequired`, `Conflicted`, or `Invalid` when applicable.

## Unsupported Route Surfaces

`GRPCRoute` is deferred. Gateway API supports `GRPCRoute`, but Cloudflare documents that Tunnel gRPC is supported through private subnet routing and public hostname deployments are not currently supported. cfgate's current public-hostname Tunnel model therefore stays `HTTPRoute`-only.

`TCPRoute` and `TLSRoute` are not planned as full public-hostname E2E surfaces. Cloudflare Tunnel can publish TCP-like services only with client-side `cloudflared access tcp` or private routing, which does not match cfgate's current DNS/public-hostname HTTPRoute contract.

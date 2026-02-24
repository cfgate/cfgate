# Annotations Reference

Complete reference for all cfgate annotations.

## Route Annotations

Per-route configuration applied to HTTPRoute, TCPRoute, UDPRoute, and GRPCRoute resources.

| Annotation | Values | Default | Description |
|---|---|---|---|
| `cfgate.io/origin-protocol` | `http`, `https`, `tcp`\*, `udp`\* | Route-type dependent | Backend protocol |
| `cfgate.io/origin-ssl-verify` | `true`, `false` | `true` | TLS certificate verification |
| `cfgate.io/origin-connect-timeout` | Duration string (`30s`, `1m`) | `30s` | Origin connection timeout |
| `cfgate.io/origin-http-host-header` | Hostname string | *none* | Host header override sent to origin |
| `cfgate.io/origin-server-name` | Hostname string | *none* | TLS SNI server name |
| `cfgate.io/origin-ca-pool` | File path string | *none* | CA certificate pool path |
| `cfgate.io/origin-http2` | `true`, `false` | `false` | HTTP/2 to origin |
| `cfgate.io/origin-h2c` | `true`, `false` | `false` | HTTP/2 cleartext (h2c) to origin |
| `cfgate.io/ttl` | `1`-`86400` | `1` (auto) | DNS record TTL in seconds |
| `cfgate.io/cloudflare-proxied` | `true`, `false` | `true` | Cloudflare proxy (orange cloud) |
| `cfgate.io/access-policy` | `name` or `namespace/name` | *none* | References a CloudflareAccessPolicy |
| `cfgate.io/hostname` | RFC 1123 hostname | *none* | Override or set hostname for the route |
\*`tcp` and `udp` protocol values are accepted but TCPRoute and UDPRoute controllers are stubs, planned for v0.2.0.

**Default for `cfgate.io/origin-protocol`:** `http` for HTTPRoute, `tcp` for TCPRoute, `udp` for UDPRoute. GRPCRoute defaults to `http` (cloudflared handles gRPC over HTTP).

### Detailed Annotation Documentation

---

#### `cfgate.io/origin-protocol`

Specifies the protocol used to connect from cloudflared to the backend service.

**Valid values:** `http`, `https`, `tcp`, `udp`

**Default:** Route-type dependent:
- HTTPRoute: `http`
- GRPCRoute: `http`
- TCPRoute: `tcp`
- UDPRoute: `udp`

**Read by:** CloudflareTunnel controller (via route collection), cloudflared-builder

When set to `https`, cloudflared opens a TLS connection to the origin. Combine with `origin-ssl-verify: "false"` if the origin uses a self-signed certificate.

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: secure-backend
  namespace: default
  annotations:
    cfgate.io/origin-protocol: "https"
spec:
  parentRefs:
    - name: cloudflare-tunnel
      namespace: cfgate-system
  hostnames:
    - secure.example.com
  rules:
    - backendRefs:
        - name: my-service
          port: 443
```

---

#### `cfgate.io/origin-ssl-verify`

Controls whether cloudflared verifies the TLS certificate presented by the origin server.

**Valid values:** `true`, `false`, `1`, `0`, `yes`, `no` (case-insensitive)

**Default:** `true`

**Read by:** CloudflareTunnel controller (via route collection), cloudflared-builder

Set to `false` when your origin uses a self-signed certificate or when using `origin-protocol: https` with services that have untrusted certs.

```yaml
metadata:
  annotations:
    cfgate.io/origin-protocol: "https"
    cfgate.io/origin-ssl-verify: "false"
```

---

#### `cfgate.io/origin-connect-timeout`

Maximum time cloudflared waits to establish a connection to the origin server.

**Valid values:** Go duration string (e.g., `30s`, `1m`, `1h30m`)

**Default:** `30s`

**Read by:** CloudflareTunnel controller (via route collection), cloudflared-builder

```yaml
metadata:
  annotations:
    cfgate.io/origin-connect-timeout: "10s"
```

---

#### `cfgate.io/origin-http-host-header`

Overrides the HTTP `Host` header sent to the origin server. Useful when the origin expects a specific hostname that differs from the public-facing hostname.

**Valid values:** Hostname string

**Default:** Not set (uses the route hostname)

**Read by:** CloudflareTunnel controller (via route collection), cloudflared-builder

```yaml
metadata:
  annotations:
    cfgate.io/origin-http-host-header: "internal-service.local"
```

---

#### `cfgate.io/origin-server-name`

Specifies the TLS SNI (Server Name Indication) server name for the connection to the origin. Used when the origin's TLS certificate is issued for a different name than the connecting hostname.

**Valid values:** Hostname string

**Default:** Not set

**Read by:** CloudflareTunnel controller (via route collection), cloudflared-builder

```yaml
metadata:
  annotations:
    cfgate.io/origin-server-name: "real-cert-name.internal"
```

---

#### `cfgate.io/origin-ca-pool`

Path to a CA certificate pool file used to verify the origin server's TLS certificate. The path must be accessible inside the cloudflared container.

**Valid values:** File path string

**Default:** Not set (system CA pool)

**Read by:** CloudflareTunnel controller (via route collection), cloudflared-builder

```yaml
metadata:
  annotations:
    cfgate.io/origin-ca-pool: "/etc/ssl/certs/custom-ca.pem"
```

---

#### `cfgate.io/origin-http2`

Enables HTTP/2 for the connection between cloudflared and the origin server.

**Valid values:** `true`, `false`, `1`, `0`, `yes`, `no` (case-insensitive)

**Default:** `false`

**Read by:** CloudflareTunnel controller (via route collection), cloudflared-builder

```yaml
metadata:
  annotations:
    cfgate.io/origin-http2: "true"
```

---

#### `cfgate.io/origin-h2c`

Enables HTTP/2 cleartext (h2c) for the connection between cloudflared and the origin server. Use this for backends that speak HTTP/2 without TLS, such as gRPC services, Envoy sidecars, or other h2c-speaking backends.

**Valid values:** `true`, `false`, `1`, `0`, `yes`, `no` (case-insensitive)

**Default:** `false`

**Read by:** CloudflareTunnel controller (via route collection), cloudflared-builder

Mutually exclusive with `cfgate.io/origin-http2` (TLS-based HTTP/2). Requires the cfgate cloudflared fork with h2cOrigin support.

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: grpc-backend
  namespace: default
  annotations:
    cfgate.io/origin-h2c: "true"
spec:
  parentRefs:
    - name: cloudflare-tunnel
      namespace: cfgate-system
  hostnames:
    - grpc.example.com
  rules:
    - backendRefs:
        - name: grpc-service
          port: 50051
```

---

#### `cfgate.io/ttl`

Sets the DNS record TTL (Time To Live) in seconds. Value `1` is special and means "auto" (Cloudflare-managed TTL). Only relevant when CloudflareDNS discovers this route via `gatewayRoutes`.

**Valid values:** Integer from `1` to `86400`

**Default:** `1` (auto)

**Read by:** CloudflareDNS controller (via route hostname collection). See [CloudflareDNS](cloudflare-dns.md#specdefaults) for default TTL configuration.

```yaml
metadata:
  annotations:
    cfgate.io/ttl: "300"
```

---

#### `cfgate.io/cloudflare-proxied`

Controls whether Cloudflare proxies traffic for the DNS record (the "orange cloud" toggle). When `true`, traffic passes through Cloudflare's network (DDoS protection, WAF, caching). When `false`, DNS resolves directly to the tunnel domain.

**Valid values:** `true`, `false`, `1`, `0`, `yes`, `no` (case-insensitive)

**Default:** `true`

**Read by:** CloudflareDNS controller (via route hostname collection). See [CloudflareDNS](cloudflare-dns.md#specdefaults) for default proxy configuration.

```yaml
metadata:
  annotations:
    cfgate.io/cloudflare-proxied: "false"
```

---

#### `cfgate.io/access-policy`

References a [CloudflareAccessPolicy](cloudflare-access-policy.md) resource to protect this route with Cloudflare Access zero-trust authentication.

**Valid values:** `name` (same namespace) or `namespace/name`

**Default:** Not set (no Access protection)

**Read by:** HTTPRoute controller

```yaml
metadata:
  annotations:
    cfgate.io/access-policy: "my-access-policy"
```

Or cross-namespace:

```yaml
metadata:
  annotations:
    cfgate.io/access-policy: "cfgate-system/shared-policy"
```

---

#### `cfgate.io/hostname`

Sets or overrides the hostname for a route. **Required** for TCPRoute and UDPRoute because the Gateway API spec does not include a `hostnames` field on these route types. Optional for HTTPRoute and GRPCRoute, where it overrides `spec.hostnames`.

**Valid values:** RFC 1123 hostname (max 253 characters, labels max 63 characters, lowercase alphanumeric and hyphens)

**Default:** Not set

**Read by:** Route controllers (TCPRoute, UDPRoute, HTTPRoute, GRPCRoute)

```yaml
apiVersion: gateway.networking.k8s.io/v1alpha2
kind: TCPRoute
metadata:
  name: my-tcp-service
  annotations:
    cfgate.io/hostname: "tcp.example.com"
spec:
  parentRefs:
    - name: cloudflare-tunnel
      namespace: cfgate-system
  rules:
    - backendRefs:
        - name: my-tcp-service
          port: 5432
```

---

## Infrastructure Annotations

Applied to Gateway resources to connect them to CloudflareTunnel resources.

| Annotation | Values | Description |
|---|---|---|
| `cfgate.io/tunnel-ref` | `namespace/name` or `name` | References the CloudflareTunnel resource this Gateway should use |
| `cfgate.io/tunnel-target` | Tunnel domain (e.g., `uuid.cfargotunnel.com`) | Set by controller (read-only) |

---

#### `cfgate.io/tunnel-ref`

Connects a Gateway to a [CloudflareTunnel](cloudflare-tunnel.md) resource. This annotation is the link between the [Gateway API](gateway-api-primer.md) layer and cfgate's tunnel management.

**Format:** `namespace/name` (recommended) or `name` (same namespace)

**Read by:** CloudflareTunnel controller (finds Gateways referencing this tunnel), CloudflareDNS controller (finds Gateways for route discovery), CloudflareAccessPolicy controller (credential inheritance)

```yaml
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

---

#### `cfgate.io/tunnel-target`

The tunnel endpoint domain, set automatically by the CloudflareTunnel controller after tunnel creation. Format is `{tunnelID}.cfargotunnel.com`. Do not set this manually.

**Read by:** Gateway controller, DNS controllers

---

## Lifecycle Annotations

Applied to CloudflareTunnel and CloudflareAccessPolicy resources to control deletion behavior.

| Annotation | Values | Default | Description |
|---|---|---|---|
| `cfgate.io/deletion-policy` | `orphan` | Not set (full cleanup) | When set to `orphan`, skips Cloudflare-side cleanup on resource deletion |

---

#### `cfgate.io/deletion-policy`

Controls what happens to Cloudflare-side resources when the Kubernetes resource is deleted.

**Valid values:** `orphan`

**Default:** Not set (the controller deletes the corresponding Cloudflare resource during finalization)

**Supported on:**
- **CloudflareTunnel:** When set to `orphan`, the tunnel remains in Cloudflare but the K8s resource is removed. The controller skips tunnel deletion and proceeds directly to finalizer removal.
- **CloudflareAccessPolicy:** When set to `orphan`, the Access Application, policies, service tokens, and mTLS certificates remain in Cloudflare. The controller skips all Cloudflare cleanup and proceeds directly to finalizer removal.

**Use cases:**
- Migrating resources between clusters (delete from old cluster without destroying the Cloudflare resource)
- Debugging tunnel or access issues (remove K8s resource without affecting live traffic)
- Emergency finalizer unblocking (annotate a stuck resource, then delete it)

```bash
# Annotate before deletion to orphan the tunnel
kubectl annotate cloudflaretunnel my-tunnel cfgate.io/deletion-policy=orphan

# Now delete: tunnel stays in Cloudflare, K8s resource removed
kubectl delete cloudflaretunnel my-tunnel
```

```bash
# Same for access policies
kubectl annotate cloudflareaccesspolicy my-policy cfgate.io/deletion-policy=orphan
kubectl delete cloudflareaccesspolicy my-policy
```

**Warning:** Orphaned resources in Cloudflare must be manually deleted via the Cloudflare dashboard or API. cfgate will not manage them again unless you re-create the Kubernetes resource with matching names.

---

## Internal Annotations

These annotations are managed by cfgate controllers and should not be set manually.

| Annotation | Applied To | Description |
|---|---|---|
| `cfgate.io/config-hash` | CloudflareTunnel | SHA-256 hash of the last-synced tunnel configuration. Used to skip redundant Cloudflare API updates when the configuration has not changed. |

---

## Notes on annotationFilter

The `annotationFilter` field on `CloudflareDNS.spec.source.gatewayRoutes` is **not** itself a cfgate annotation. It is a CRD spec field that accepts any user-chosen annotation key (or key=value pair) as a filter for route discovery.

When set, only HTTPRoutes bearing the specified annotation will be included in DNS sync. The annotation name and value are entirely user-defined.

**Supported formats:**
- `key=value`: Only syncs routes where the annotation key exists AND the value matches exactly
- `key`: Only syncs routes where the annotation key exists (any value)

**Example:** Using `cfgate.io/dns-sync=enabled` as a convention (but any annotation works):

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
      annotationFilter: "cfgate.io/dns-sync=enabled"
```

Then only HTTPRoutes with the matching annotation are synced:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: synced-app
  annotations:
    cfgate.io/dns-sync: "enabled"  # Matches filter, DNS record created
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

Routes without the annotation are ignored by this CloudflareDNS resource, even if they reference the same Gateway and tunnel.

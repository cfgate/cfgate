# Annotations Reference

cfgate annotations configure HTTPRoute origin transport, DNS overrides, Gateway tunnel bindings, and resource lifecycle behavior.

Annotation values are strings; quote booleans and numbers in YAML. Omitted settings inherit where noted. Installation and routing examples are in [Getting started](getting-started.md).

## Route Annotations

Apply these annotations to Gateway API HTTPRoutes:

| Annotation | Values | Default | Description |
|---|---|---|---|
| `cfgate.io/origin-protocol` | `http`, `https`, case-insensitive | `http` | Protocol from connector to backend. |
| `cfgate.io/origin-ssl-verify` | Boolean | Tunnel default | Verify the origin certificate; inversely maps to `noTLSVerify`. |
| `cfgate.io/origin-connect-timeout` | Positive whole-second duration | Tunnel default, otherwise `30s` | Time allowed to connect to the origin. |
| `cfgate.io/origin-http-host-header` | String | *none* | Override the origin HTTP Host header. |
| `cfgate.io/origin-server-name` | String | *none* | Override TLS SNI. |
| `cfgate.io/origin-ca-pool` | `/etc/cfgate/origin-ca-pool/ca.pem` | Tunnel CA configuration | Select the managed origin CA bundle; requires a tunnel CA Secret reference. |
| `cfgate.io/origin-http2` | Boolean | Tunnel default, otherwise `false` | Enable HTTP/2 origin transport. |
| `cfgate.io/origin-h2c` | Boolean | Tunnel default, otherwise `false` | Enable cleartext HTTP/2 origin transport. |
| `cfgate.io/ttl` | Integer from `1` to `86400` | DNS resource default | DNS TTL; `1` explicitly selects Auto. |
| `cfgate.io/cloudflare-proxied` | Boolean | DNS zone, then resource default | Enable Cloudflare proxying. |
| `cfgate.io/hostname` | RFC 1123 hostname | `spec.hostnames` | Override route hostnames. |
| `cfgate.io/access-required` | `namespace/name` | *none* | Require a named Access application before forwarding. |
| `cfgate.io/access-policy` | `name` or `namespace/name` | *none* | Deprecated policy reference for status and warnings only. |

Origin booleans accept `true`, `false`, `1`, `0`, `yes`, and `no`, case-insensitively. Invalid origin settings are rejected. DNS proxy parsing also accepts these spellings; use `true` or `false` for clarity.

### Examples

Use HTTPS with an origin certificate name and Host header that differ from the public hostname:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: secure-backend
  namespace: default
  annotations:
    cfgate.io/origin-protocol: "https"
    cfgate.io/origin-ssl-verify: "true"
    cfgate.io/origin-server-name: "origin.internal.example.com"
    cfgate.io/origin-http-host-header: "origin.internal.example.com"
    cfgate.io/origin-connect-timeout: "10s"
spec:
  parentRefs:
    - name: cloudflare-tunnel
      namespace: cfgate-system
  hostnames:
    - app.example.com
  rules:
    - backendRefs:
        - name: my-service
          port: 443
```

To override inherited HTTP/2 TLS settings for a cleartext HTTP/2 backend, set both transport flags explicitly:

```yaml
metadata:
  annotations:
    cfgate.io/origin-protocol: "http"
    cfgate.io/origin-http2: "false"
    cfgate.io/origin-h2c: "true"
```

### Detailed Annotation Documentation

#### `cfgate.io/origin-protocol`

Select the connector-to-Service protocol independently of the public URL or Gateway listener port. Service backends must expose a TCP port. UDP-only and SCTP-only ports set `ResolvedRefs=False` with reason `UnsupportedProtocol`; their matches return HTTP 500. An omitted Service protocol uses the Kubernetes TCP default.

#### `cfgate.io/origin-ssl-verify`

Omission inherits `originDefaults.noTLSVerify`. Explicit `true` requires certificate verification even when the tunnel default disables it; `false` disables verification for this route. For private certificates, configure a trusted CA bundle instead of disabling verification.

#### `cfgate.io/origin-connect-timeout`

The duration must resolve to a positive whole number of seconds, such as `10s`, `1m`, or `1h30m`. Values requiring rounding or SDK fallback are rejected.

#### `cfgate.io/origin-http-host-header`

Override the Host header when the origin expects a name different from the request hostname. Omitting it leaves the connector's normal Host behavior in place.

#### `cfgate.io/origin-server-name`

Select the SNI name used for origin TLS when it differs from the connecting hostname. This does not change the public hostname or DNS record.

#### `cfgate.io/origin-ca-pool`

Only the managed mount path is accepted, and only when the referenced tunnel configures `spec.originDefaults.caPoolSecretRef`. Configure the Secret on the tunnel; an annotation cannot mount an arbitrary file. See [origin defaults](cloudflare-tunnel.md#specorigindefaults).

#### `cfgate.io/origin-http2`

Explicit `false` disables an inherited HTTP/2 setting. It cannot be enabled alongside h2c.

#### `cfgate.io/origin-h2c`

h2c requires cleartext HTTP and a compatible connector. The default fork supports it; the known upstream `cloudflare/cloudflared` image is treated as incompatible for h2c forwarding. Custom image compatibility remains the administrator's responsibility. See [connector image selection](cloudflare-tunnel.md#image).

The effective transport is checked against each parent tunnel. HTTPS with h2c or simultaneous HTTP/2 and h2c sets `Accepted=False` and retains matching HTTP 503 responses. Invalid forwarding fallbacks also return 503; valid sibling routes and backend revocations can still publish. A ready Pod and stored remote configuration do not establish origin reachability.

#### `cfgate.io/ttl`

Omission inherits `CloudflareDNS.spec.defaults.ttl`; `"1"` explicitly selects Auto. The annotation parser accepts integers from 1 to 86400, while CloudflareDNS CRD fields restrict explicit TTLs to 60 through 86400 or Auto. Use `"1"` or a value of at least `"60"` for DNS-only records. Proxied records always publish TTL `1` after inheritance and overrides.

#### `cfgate.io/cloudflare-proxied`

An explicit value overrides the selected zone's proxy setting and the DNS resource default. With no override, proxying defaults to `true` unless the zone or DNS defaults disable it. See [DNS defaults](cloudflare-dns.md#specdefaults).

#### `cfgate.io/hostname`

A set value replaces `spec.hostnames`. It must be a lowercase RFC 1123 hostname, at most 253 characters with labels no longer than 63. Listener hostname intersection and route admission still apply.

#### `cfgate.io/access-required`

This namespaced application reference makes forwarding depend on verified Access protection. Use the full `namespace/name` form, including for a same-namespace application. See [Access-required routing](access-required.md) for grants, supported target coverage, withdrawal ordering, and asynchronous limits.

#### `cfgate.io/access-policy`

This deprecated annotation resolves a policy for HTTPRoute status and warnings. It neither creates an Access application nor attaches the policy. Use [CloudflareAccessApplication](cloudflare-access-application.md) and, when required, `cfgate.io/access-required`.

## Infrastructure Annotations

Apply the tunnel binding to a Gateway managed by cfgate:

| Annotation | Values | Default | Description |
|---|---|---|---|
| `cfgate.io/tunnel-ref` | `name` or `namespace/name` | *none* | Referenced CloudflareTunnel; unqualified names use the Gateway namespace. |
| `cfgate.io/tunnel-target` | Reserved | *none* | Legacy declared annotation; current controllers do not use it for routing or DNS. |

#### `cfgate.io/tunnel-ref`

The GatewayClass must select `cfgate.io/cloudflare-tunnel-controller`. A cross-namespace tunnel binding also needs a ReferenceGrant; the annotation alone does not authorize access.

This Gateway admits HTTPRoutes from its own namespace:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: cloudflare-tunnel
  namespace: cfgate-system
  annotations:
    cfgate.io/tunnel-ref: "my-tunnel"
spec:
  gatewayClassName: cfgate
  listeners:
    - name: http
      protocol: HTTP
      port: 80
```

#### `cfgate.io/tunnel-target`

Read the resolved domain from `CloudflareTunnel.status.tunnelDomain`. Do not depend on this annotation being populated. DNS discovery resolves the Gateway's authorized tunnel reference.

## Lifecycle Annotations

| Annotation | Applied to | Values | Default | Description |
|---|---|---|---|---|
| `cfgate.io/deletion-policy` | All four cfgate CRDs | `orphan` | Configured cleanup | Skip remote cleanup during deletion. |
| `cfgate.io/adopt-existing` | All four cfgate CRDs | `"true"` | Adoption disabled | Permit inspected legacy-resource adoption within the ownership rules. |

#### `cfgate.io/deletion-policy`

`orphan` removes the resource finalizer without deleting remote resources. It retains tunnel claims, DNS data/claims, reusable policies/service tokens, or Access applications/owner tags, depending on resource kind. Kubernetes garbage collection of owned local objects remains separate.

For an intentional orphan deletion:

```bash
kubectl annotate cloudflaretunnel my-tunnel -n cfgate-system \
  cfgate.io/deletion-policy=orphan
kubectl delete cloudflaretunnel my-tunnel -n cfgate-system
```

Orphaned resources remain your responsibility. A recreated Kubernetes object has a new UID and does not regain ownership from a matching name. Review [authorization and ownership](authorization-and-ownership.md) before transfer or adoption.

#### `cfgate.io/adopt-existing`

For tunnels, `"true"` permits acquiring an absent claim after administrator inspection. For DNS, it permits adopting inspected unmarked legacy data. For Access resources, it enables the inspected legacy policy/token name and application-tag migration described in [ownership migration](authorization-and-ownership.md#upgrade-from-v020-alpha6-to-v020-alpha7). It never overrides a foreign ownership claim. Remove the annotation after successful adoption.

## DNS Management Annotations

| Annotation | Applied to | Values | Default | Description |
|---|---|---|---|---|
| `cfgate.io/allow-deep-subdomains` | CloudflareDNS | `"true"` | Warning enabled | Suppress the informational `DeepSubdomain` event. |

#### `cfgate.io/allow-deep-subdomains`

The warning reports hostnames more than one label below their selected zone. Suppressing it neither configures certificates nor changes publication. Arrange appropriate edge certificate coverage first; see [multi-level subdomains](cloudflare-dns.md#multi-level-subdomains).

## Internal Annotations

These annotations are controller-managed; do not edit them to bypass reconciliation or recovery:

| Annotation | Applied to | Description |
|---|---|---|
| `cfgate.io/config-hash` | CloudflareTunnel | Hash of applied tunnel configuration used to avoid redundant updates. |
| `cfgate.io/service-token-rotation-pending` | Managed service-token Secret | Checkpoint for unfinished credential creation or rotation; retained until credentials are stored. |

## Notes on annotationFilter

`CloudflareDNS.spec.source.gatewayRoutes.annotationFilter` is a specification field, not a cfgate annotation. It accepts any user-selected annotation key:

| Filter | Match |
|---|---|
| `key` | The HTTPRoute contains that annotation, with any value. |
| `key=value` | The HTTPRoute contains the key with exactly that value. |

For example, this source selects routes with `cfgate.io/dns-sync: "enabled"`:

```yaml
spec:
  source:
    gatewayRoutes:
      annotationFilter: "cfgate.io/dns-sync=enabled"
```

The key is a convention chosen by the administrator. Routes must still pass admission and authorization for the referenced tunnel. See [CloudflareDNS](cloudflare-dns.md#specsourcegatewayroutes).
